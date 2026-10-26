#!/usr/bin/env python3
"""Minimal independent JSON-lines consumer for operator transport v1."""

import argparse
import datetime
import hashlib
import json
import os
import platform
import re
import select
import selectors
import shutil
import subprocess
import sys
import tempfile
import time
from pathlib import Path


VERSION = 1
MAX_REQUEST_FRAME = 64 * 1024
MAX_RESPONSE_FRAME = 1024 * 1024
ERROR_CODES = {"unsupported_version", "invalid_request", "unknown_operation", "unknown_field",
               "denied", "not_found", "stale_source", "limit_exceeded", "unsupported_pagination",
               "storage_unavailable", "internal"}
FIXTURES = Path(__file__).resolve().parents[2] / "contracts/operator/v1/testdata"


def decode_response(raw, request_id, workspace=None, project=None, task=""):
    frame = json.loads(raw)
    if not isinstance(frame, dict):
        raise ValueError("response must be a JSON object")
    if type(frame.get("schema_version")) is not int or frame["schema_version"] != VERSION:
        raise ValueError("response schema version mismatch")
    if type(frame.get("request_id")) is not str or not frame["request_id"] or frame["request_id"] != request_id:
        raise ValueError("response request_id mismatch")
    if type(frame.get("ok")) is not bool:
        raise ValueError("response ok must be boolean")
    common = {"schema_version", "request_id", "ok"}
    if frame["ok"] is True:
        required = common | {"scope", "observed_at", "source", "data"}
        if not required.issubset(frame) or "error" in frame or frame.keys() - (required | {"page"}):
            raise ValueError("success response has an invalid closed envelope shape")
        scope = frame.get("scope") or {}
        if (not isinstance(scope, dict) or not {"workspace_id", "project_id"}.issubset(scope)
                or scope.keys() - {"workspace_id", "project_id", "task_id"}
                or not all(type(scope.get(key)) is str and scope.get(key) for key in ("workspace_id", "project_id"))
                or scope.get("workspace_id") != workspace or scope.get("project_id") != project
                or ("task_id" in scope and type(scope["task_id"]) is not str)
                or (scope.get("task_id", "") or "") != (task or "")):
            raise ValueError("success response scope mismatch")
        if not isinstance(frame.get("data"), dict):
            raise ValueError("success response data must be an object")
        source = frame.get("source") or {}
        if (not isinstance(source, dict) or set(source) != {"producer", "schema", "observed_at"}
                or not all(type(source[key]) is str and source[key] for key in ("producer", "schema"))):
            raise ValueError("success response source identity is invalid")
        if not valid_timestamp(frame["observed_at"]) or not valid_timestamp(source["observed_at"]):
            raise ValueError("success response observation timestamp is invalid")
        if "page" in frame:
            page = frame["page"]
            if (not isinstance(page, dict) or not {"limit", "truncated", "partial"}.issubset(page)
                    or page.keys() - {"limit", "next_cursor", "truncated", "partial"}
                    or type(page.get("limit")) is not int or not 1 <= page["limit"] <= 512
                    or type(page.get("truncated")) is not bool or type(page.get("partial")) is not bool
                    or ("next_cursor" in page and (type(page["next_cursor"]) is not str or len(page["next_cursor"]) > 2048))):
                raise ValueError("success response page metadata is invalid")
    else:
        if set(frame) != common | {"error"}:
            raise ValueError("failure response has an invalid closed envelope shape")
        error = frame.get("error")
        if (not isinstance(error, dict) or set(error) != {"code", "message", "retryable"}
                or type(error.get("code")) is not str or error.get("code") not in ERROR_CODES
                or type(error.get("message")) is not str
                or not error["message"] or len(error["message"]) > 512
                or type(error.get("retryable")) is not bool):
            raise ValueError("failure response error object is invalid")
    return frame


def valid_timestamp(value):
    if type(value) is not str or not re.fullmatch(r"\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d{1,9})?(?:Z|[+-]\d\d:\d\d)", value):
        return False
    if "." in value:
        head, rest = value.split(".", 1)
        fraction, zone = re.match(r"(\d+)(Z|[+-]\d\d:\d\d)$", rest).groups()
        value = head + "." + fraction[:6].ljust(6, "0") + zone
    try:
        datetime.datetime.fromisoformat(value.replace("Z", "+00:00"))
        return True
    except ValueError:
        return False


def wait_control_fd(fd, timeout):
    with selectors.DefaultSelector() as selector:
        selector.register(fd, selectors.EVENT_READ)
        if not selector.select(timeout=timeout):
            raise TimeoutError("owner revocation control signal timed out")
    if not os.read(fd, 1):
        raise RuntimeError("revocation control pipe closed without a signal")


def wait_control_signal(path, timeout):
    fd = os.open(path, os.O_RDWR | os.O_NONBLOCK)
    try:
        wait_control_fd(fd, timeout)
    finally:
        os.close(fd)


def check_fixtures():
    count = 0
    for path in sorted(FIXTURES.glob("*.json")):
        fixture = json.loads(path.read_text(encoding="utf-8"))
        request, response = fixture.get("request"), fixture.get("response")
        if not fixture.get("case") or not isinstance(request, dict) or not isinstance(response, dict):
            raise ValueError(f"{path.name}: incomplete fixture envelope")
        if request.get("request_id") != response.get("request_id"):
            raise ValueError(f"{path.name}: request/response id mismatch")
        try:
            decoded = decode_response(json.dumps(response), request["request_id"],
                                      request.get("workspace_id"), request.get("project_id"),
                                      request.get("task_id", ""))
        except ValueError as error:
            raise ValueError(f"{path.name}: {error}") from error
        if response.get("ok") is True:
            payload = json.dumps(decoded.get("data", {}), sort_keys=True)
            if fixture["case"] == "stale-source-remains-stale" and '"freshness": "stale"' not in payload:
                raise ValueError(f"{path.name}: stale marker was lost")
        elif not (response.get("error") or {}).get("code"):
            raise ValueError(f"{path.name}: denied fixture has no stable code")
        count += 1
    if count == 0:
        raise ValueError("no golden fixtures found")
    return count


class PipeClient:
    def __init__(self, binary, connection, timeout=20, shutdown_timeout=2):
        self.process = subprocess.Popen(
            [binary, "operator", "serve", "--connection", connection],
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL,
            bufsize=0,
        )
        self.timeout = timeout
        self.shutdown_timeout = shutdown_timeout
        self.pending = bytearray()
        os.set_blocking(self.process.stdin.fileno(), False)

    def terminate(self):
        if self.process.poll() is None:
            self.process.kill()
        try:
            return self.process.wait(timeout=1)
        except subprocess.TimeoutExpired:
            raise RuntimeError("operator child did not stop after kill")

    def exchange(self, request, workspace, project):
        payload = json.dumps(request, separators=(",", ":")).encode() + b"\n"
        if len(payload) > MAX_REQUEST_FRAME:
            raise ValueError("request exceeds protocol frame limit")
        deadline = time.monotonic() + self.timeout
        sent = 0
        input_fd, output_fd = self.process.stdin.fileno(), self.process.stdout.fileno()
        while sent < len(payload):
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                self.terminate()
                raise TimeoutError("operator child request write timed out")
            _, writable, _ = select.select([], [input_fd], [], remaining)
            if not writable:
                self.terminate()
                raise TimeoutError("operator child request write timed out")
            try:
                sent += os.write(input_fd, payload[sent:])
            except BlockingIOError:
                continue

        while True:
            newline = self.pending.find(b"\n")
            if newline >= 0:
                raw = bytes(self.pending[:newline])
                del self.pending[:newline + 1]
                return decode_response(raw, request["request_id"], workspace, project, request.get("task_id", ""))
            if len(self.pending) > MAX_RESPONSE_FRAME:
                self.terminate()
                raise ValueError("operator response exceeds protocol frame limit")
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                self.terminate()
                raise TimeoutError("operator child response timed out")
            readable, _, _ = select.select([output_fd], [], [], remaining)
            if not readable:
                self.terminate()
                raise TimeoutError("operator child response timed out")
            chunk = os.read(output_fd, min(65536, MAX_RESPONSE_FRAME + 1 - len(self.pending)))
            if not chunk:
                self.terminate()
                raise RuntimeError("operator child closed its protocol stream")
            self.pending.extend(chunk)

    def close(self):
        if self.process.poll() is None:
            try:
                self.process.stdin.close()
            except BrokenPipeError:
                pass
        try:
            status = self.process.wait(timeout=self.shutdown_timeout)
        except subprocess.TimeoutExpired:
            status = self.terminate()
        finally:
            self.process.stdout.close()
        return status


def request(req_id, operation, workspace, project, **extra):
    return {"schema_version": 1, "request_id": req_id, "operation": operation,
            "workspace_id": workspace, "project_id": project, **extra}


def require_failure(frame, code, label):
    if frame.get("ok") is not False or frame.get("error", {}).get("code") != code:
        raise AssertionError(f"{label}: expected {code}, got {frame}")


def assert_schema10_unsupported_runtime(frame):
    data = frame.get("data")
    if not isinstance(data, dict) or data.get("authority_version") != 10:
        raise AssertionError("disposable governance snapshot must report authority schema 10")
    services = data.get("services")
    if not isinstance(services, dict):
        raise AssertionError("governance snapshot is missing producer service availability facts")
    for service in ("budget", "policy", "controller"):
        if services.get(service) != "unsupported":
            raise AssertionError(f"schema-10 service {service!r} must remain explicitly unsupported")
    spend = data.get("spend")
    if (not isinstance(spend, dict)
            or any(spend.get(field) != "unavailable" for field in ("state", "provenance", "enforcement"))):
        raise AssertionError("schema-10 spend data must remain explicitly unavailable")
    return {"schema": 10, "services": {name: services[name] for name in ("budget", "policy", "controller")},
            "spend": {name: spend[name] for name in ("state", "provenance", "enforcement")}}


def provenance(binary, observations, transport_identity):
    repo = Path(__file__).resolve().parents[2]
    commit = subprocess.run(["git", "-C", str(repo), "rev-parse", "HEAD"],
                            check=True, capture_output=True, text=True).stdout.strip()
    source_paths = sorted((repo / "internal/operatorconformance").glob("*.go"))
    source_paths += sorted((repo / "scripts/operator-conformance").glob("*.py"))
    source_paths += [repo / "docs/operator-transport-conformance.md"]
    fixture_hashes = {
        path.name: hashlib.sha256(path.read_bytes()).hexdigest()
        for path in sorted(FIXTURES.glob("*.json"))
    }
    binary_path = Path(binary).resolve()
    go_version = None
    if shutil.which("go"):
        result = subprocess.run(["go", "version"], check=True, capture_output=True, text=True)
        go_version = result.stdout.strip()
    return {
        "source": {"commit": commit, "repository": str(repo),
                   "files": {str(path.relative_to(repo)): hashlib.sha256(path.read_bytes()).hexdigest()
                             for path in source_paths}},
        "observed_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
        "runtime": {"python": sys.version, "platform": platform.platform(), "go": go_version},
        "transport": {"identity": transport_identity, "binary": str(binary_path),
                       "binary_sha256": hashlib.sha256(binary_path.read_bytes()).hexdigest()},
        "fixtures": fixture_hashes,
        "observations": observations,
    }


def run_live(args):
    observations = {"stale_source": "fixture-only"}
    client = PipeClient(args.binary, args.connection)
    try:
        baseline = client.exchange(request("py-live-tasks", "tasks.list", args.workspace,
                                           args.project, limit=1), args.workspace, args.project)
        if baseline.get("ok") is not True:
            raise AssertionError(f"selected transport denied its baseline read: {baseline}")
        observations["authorized_read"] = "live-success"

        snapshot = client.exchange(request("py-live-runtime-capabilities", "governance.snapshot",
                                           args.workspace, args.project), args.workspace, args.project)
        if snapshot.get("ok") is not True:
            raise AssertionError(f"selected transport denied its governance snapshot: {snapshot}")
        observations["runtime_capability_preservation"] = assert_schema10_unsupported_runtime(snapshot)

        mismatch = request("py-live-version", "tasks.list", args.workspace, args.project)
        mismatch["schema_version"] = 999
        require_failure(client.exchange(mismatch, args.workspace, args.project),
                        "unsupported_version", "version mismatch")
        observations["version_mismatch"] = "live-unsupported_version"

        first = client.exchange(request("py-live-activity-1", "activity.list", args.workspace,
                                        args.project, limit=1), args.workspace, args.project)
        if first.get("ok") is not True or not first.get("page", {}).get("next_cursor"):
            raise AssertionError(f"selected transport did not return a resumable first page: {first}")
        cursor = first["page"]["next_cursor"]
        second = client.exchange(request("py-live-activity-2", "activity.list", args.workspace,
                                         args.project, limit=1, cursor=cursor), args.workspace, args.project)
        if second.get("ok") is not True or second.get("page", {}).get("next_cursor") == cursor:
            raise AssertionError(f"selected transport did not advance its activity cursor: {second}")
        observations["pagination"] = "live-next_cursor-advanced"

        if args.scenario == "revoke":
            if not args.revoke_trigger or not args.receipt:
                raise ValueError("revoke scenario requires --revoke-trigger FIFO and --receipt path")
            trigger_fd = os.open(args.revoke_trigger, os.O_RDWR | os.O_NONBLOCK)
            try:
                stage = Path(args.receipt)
                staged = stage.with_suffix(stage.suffix + ".tmp")
                staged.write_text(json.dumps({"stage": "baseline-succeeded", "pid": os.getpid()}),
                                  encoding="utf-8")
                os.replace(staged, stage)
                wait_control_fd(trigger_fd, 30)
            finally:
                os.close(trigger_fd)
            revoked = client.exchange(request("py-live-after-revoke", "tasks.list", args.workspace,
                                               args.project), args.workspace, args.project)
            require_failure(revoked, "denied", "same-process scope revocation")
            observations["scope_revocation"] = "live-denied-on-same-child-after-owner-revoke"

        if args.missing_authority_connection:
            denied_child = PipeClient(args.binary, args.missing_authority_connection)
            try:
                denied_frame = denied_child.exchange(request("py-live-missing-authority", "tasks.list",
                                                             args.workspace, args.project),
                                                     args.workspace, args.project)
                require_failure(denied_frame, "denied", "ordinary read authority lacks operator.read")
                observations["missing_operator_authority"] = "live-denied"
            finally:
                denied_child.close()
    finally:
        status = client.close()
        if status not in (0, None):
            raise RuntimeError(f"operator child ended with status {status}")
    reconnect = PipeClient(args.binary, args.connection)
    try:
        frame = reconnect.exchange(request("py-live-reconnect", "tasks.list", args.workspace,
                                           args.project), args.workspace, args.project)
        if args.scenario == "revoke":
            require_failure(frame, "denied", "fresh child remains revoked")
            observations["reconnect_after_revoke"] = "live-denied"
        else:
            if frame.get("ok") is not True:
                raise AssertionError(f"fresh child reconnect failed: {frame}")
            observations["reconnect"] = "live-success-on-fresh-child"
    finally:
        status = reconnect.close()
        if status not in (0, None):
            raise RuntimeError(f"reconnected operator child ended with status {status}")
    return observations


def self_test():
    good = {"schema_version": 1, "request_id": "r", "ok": False,
            "error": {"code": "denied", "message": "denied", "retryable": False}}
    for label, bad in (("id", {**good, "request_id": "other"}),
                       ("version", {**good, "schema_version": 2})):
        try:
            decode_response(json.dumps(bad), "r")
        except ValueError:
            continue
        raise AssertionError(f"consumer accepted mutated {label} guard")
    try:
        require_failure({"ok": True}, "denied", "missing capability")
    except AssertionError:
        pass
    else:
        raise AssertionError("consumer accepted success where missing capability must deny")
    success = {"schema_version": 1, "request_id": "r", "ok": True,
               "scope": {"workspace_id": "ws", "project_id": "p"},
               "observed_at": "2026-10-25T16:09:02.123456789Z",
               "source": {"producer": "producer", "schema": "schema/v1", "observed_at": "2026-10-25T16:09:02Z"},
               "data": {"opaque": {"unknown": True}}}
    malformed = [
        {**success, "schema_version": True},
        {**success, "surprise": True},
        {key: value for key, value in success.items() if key != "observed_at"},
        {**success, "observed_at": "yesterday"},
        {**success, "ok": 1},
        {**success, "error": good["error"]},
        {**success, "data": []},
        {"schema_version": 1, "request_id": "r", "ok": False,
         "error": {"code": "denied", "message": "no", "retryable": False}, "scope": success["scope"]},
    ]
    for index, frame in enumerate(malformed):
        try:
            decode_response(json.dumps(frame), "r", "ws", "p")
        except (TypeError, ValueError, KeyError):
            continue
        raise AssertionError(f"consumer accepted malformed closed-envelope case {index}")
    swapped = dict(success)
    swapped["scope"] = {"workspace_id": "ws-foreign", "project_id": "p"}
    try:
        decode_response(json.dumps(swapped), "r", "ws", "p")
    except ValueError:
        pass
    else:
        raise AssertionError("consumer accepted golden response scope swapped from its request")
    pipe_self_test()
    return check_fixtures()


def pipe_self_test():
    with tempfile.TemporaryDirectory(prefix="operator-fake-child-") as temp:
        for name, content, expect in (
            ("silent", "#!/bin/sh\nexec sleep 30\n", TimeoutError),
            ("no-newline", "#!/bin/sh\nprintf partial\nexec sleep 30\n", TimeoutError),
            ("oversize", "#!/bin/sh\nhead -c 1048578 /dev/zero\nexec sleep 30\n", ValueError),
        ):
            path = Path(temp) / name
            path.write_text(content, encoding="utf-8")
            path.chmod(0o700)
            limit = 2 if name == "oversize" else 0.05
            client = PipeClient(str(path), "opaque-connection", timeout=limit, shutdown_timeout=0.05)
            started = time.monotonic()
            try:
                client.exchange(request("fake", "tasks.list", "ws", "p"), "ws", "p")
            except expect:
                pass
            except Exception as error:
                raise AssertionError(f"fake {name} child returned {type(error).__name__}, expected {expect.__name__}") from error
            else:
                raise AssertionError(f"fake {name} child was not refused")
            finally:
                client.close()
            if not client.process.stdout.closed:
                raise AssertionError(f"fake {name} child stdout descriptor remained open")
            if time.monotonic() - started > limit + 1:
                raise AssertionError(f"fake {name} child exceeded bounded response/cleanup time")

        path = Path(temp) / "shutdown"
        path.write_text("#!/bin/sh\nexec sleep 30\n", encoding="utf-8")
        path.chmod(0o700)
        client = PipeClient(str(path), "opaque-connection", shutdown_timeout=0.05)
        started = time.monotonic()
        client.close()
        if time.monotonic() - started > 1:
            raise AssertionError("fake child shutdown exceeded its deadline")
        if not client.process.stdout.closed:
            raise AssertionError("fake child stdout descriptor remained open after shutdown")

        control = Path(temp) / "control.fifo"
        os.mkfifo(control, 0o600)
        started = time.monotonic()
        try:
            wait_control_signal(str(control), 0.05)
        except TimeoutError:
            pass
        else:
            raise AssertionError("silent owner control pipe was not bounded")
        if time.monotonic() - started > 1:
            raise AssertionError("owner control wait exceeded its deadline")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--fixtures-only", action="store_true")
    parser.add_argument("--self-test", action="store_true")
    parser.add_argument("--binary")
    parser.add_argument("--connection", help="opaque path passed to the owned operator child")
    parser.add_argument("--missing-authority-connection", help="optional ordinary reader connection without operator.read authority")
    parser.add_argument("--workspace")
    parser.add_argument("--project")
    parser.add_argument("--scenario", choices=("active", "revoke"), default="active")
    parser.add_argument("--revoke-trigger", help="owner-controlled FIFO; client waits for one byte after baseline")
    parser.add_argument("--receipt", help="structured readiness receipt path for the owner")
    parser.add_argument("--transport-identity", default="owner-selected local operator connection")
    args = parser.parse_args()
    if args.self_test:
        print(json.dumps({"self_test": "passed", "fixtures": self_test()}))
        return
    if args.fixtures_only:
        print(json.dumps({"fixture_conformance": "passed", "fixtures": check_fixtures()}))
        return
    if not all((args.binary, args.connection, args.workspace, args.project)):
        parser.error("live qualification requires --binary, --connection, --workspace and --project")
    if args.scenario == "revoke" and not args.missing_authority_connection:
        print("revoke scenario uses only the active connection after the owner revokes it", file=sys.stderr)
    observations = run_live(args)
    receipt = provenance(args.binary, observations, args.transport_identity)
    receipt["live_transport"] = "passed"
    receipt["scenario"] = args.scenario
    if args.receipt:
        Path(args.receipt).write_text(json.dumps(receipt, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(json.dumps(receipt, sort_keys=True))


if __name__ == "__main__":
    main()
