#!/usr/bin/env python3
"""Host-neutral bounded native stdio conformance, never host certification.

Profiles and producer requests are independently owner-provisioned. This runner
never reads tokens, patches authority, infers identity from labels, or starts a
model. A lost reply remains unknown; original policy refusals are never retried.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import queue
import signal
import stat
import subprocess
import threading
import time

MAX_FRAME = 262144
MAX_CALLS = 128
TOOLS = {"identity", "register_attempt", "acquire_lease", "release_lease",
         "publish_revision", "request_verification", "submit_review",
         "task_get", "get_verification", "controller_status", "controller_step", "start_verification", "poll_verification",
         "cancel_verification", "get_dispatch_routing"}


def unique(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("duplicate field")
        result[key] = value
    return result


def private(path):
    path = Path(path)
    if not path.is_absolute():
        raise ValueError("absolute private input required")
    fd = os.open(path, os.O_RDONLY | os.O_NONBLOCK | os.O_NOFOLLOW)
    with os.fdopen(fd, "rb") as stream:
        info = os.fstat(stream.fileno())
        if not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid() or info.st_mode & 0o077:
            raise ValueError("private regular owner input required")
        raw = stream.read(MAX_FRAME + 1)
    if len(raw) > MAX_FRAME:
        raise ValueError("input limit")
    return json.loads(raw, object_pairs_hook=unique)


def field(value, pointer):
    if not pointer.startswith("/") or len(pointer) > 512:
        raise ValueError("exact pointer required")
    for part in pointer[1:].split("/"):
        part = part.replace("~1", "/").replace("~0", "~")
        value = value[int(part)] if isinstance(value, list) else value[part]
    return value


def envelope(result):
    typed = result.get("structuredContent")
    content = result.get("content")
    if not isinstance(typed, dict) or typed.get("schema_version") != 1 or not isinstance(typed.get("ok"), bool):
        raise ValueError("invalid envelope")
    if not isinstance(content, list) or len(content) != 1 or content[0].get("type") != "text" or json.loads(content[0]["text"]) != typed:
        raise ValueError("text structured parity")
    if bool(result.get("isError")) == typed["ok"]:
        raise ValueError("error parity")
    if not typed["ok"] and typed.get("error", {}).get("exit_code") not in (1, 2, 3, 4):
        raise ValueError("invalid refusal")
    return typed


class Broker:
    def __init__(self, bootstrap, selection):
        self.process = subprocess.Popen(["python3", str(bootstrap), str(selection)],
            stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
            start_new_session=True, env={k: v for k, v in os.environ.items() if k != "DACLI_AGENT"})
        self.frames = queue.Queue(maxsize=32)
        self.sequence = 0
        self.closed = False
        threading.Thread(target=self._read, daemon=True).start()
        try:
            self._initialize()
        except BaseException:
            self.close(crash=True)
            raise

    def _initialize(self):
        self.call("initialize", {"protocolVersion": "2025-06-18", "capabilities": {},
            "clientInfo": {"name": "dacli-backend-conformance", "version": "1"}})
        self.catalog = {x["name"] for x in self.call("tools/list", {})["tools"]}

    def _read(self):
        while True:
            raw = self.process.stdout.readline(MAX_FRAME + 1)
            if not raw:
                return
            try:
                value = json.loads(raw, object_pairs_hook=unique) if len(raw) <= MAX_FRAME else None
                self.frames.put(value, timeout=1)
            except (ValueError, queue.Full):
                return

    def call(self, method, params):
        self.sequence += 1
        if self.sequence > MAX_CALLS:
            raise ValueError("call limit")
        raw = json.dumps({"jsonrpc": "2.0", "id": self.sequence, "method": method, "params": params}).encode() + b"\n"
        if len(raw) > MAX_FRAME:
            raise ValueError("request limit")
        write_failure = []
        def send():
            try:
                self.process.stdin.write(raw)
                self.process.stdin.flush()
            except OSError:
                write_failure.append(True)
        writer = threading.Thread(target=send, daemon=True)
        writer.start()
        writer.join(timeout=2)
        if writer.is_alive() or write_failure:
            self.close(crash=True)
            raise TimeoutError("request delivery unknown")
        deadline = time.monotonic() + 10
        for _ in range(32):
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise TimeoutError("reply unknown")
            value = self.frames.get(timeout=remaining)
            if not isinstance(value, dict):
                raise ValueError("invalid frame")
            if "id" not in value:
                continue
            if value.get("id") != self.sequence or "error" in value:
                raise ValueError("RPC result mismatch")
            return value["result"]
        raise ValueError("notification limit")

    def tool(self, name, arguments):
        if name not in TOOLS or name not in self.catalog or not isinstance(arguments, dict) or arguments.get("schema_version") != 1:
            raise ValueError("unsupported exact tool request")
        return envelope(self.call("tools/call", {"name": name, "arguments": arguments}))

    def close(self, crash=False):
        if self.closed:
            return
        self.closed = True
        if not crash:
            self.process.stdin.close()
            try:
                self.process.wait(timeout=2)
            except subprocess.TimeoutExpired:
                pass
        # Even an exited broker may have descendants retaining pipes.
        try:
            os.killpg(self.process.pid, signal.SIGTERM)
        except ProcessLookupError:
            pass
        try:
            self.process.wait(timeout=2)
        except subprocess.TimeoutExpired:
            os.killpg(self.process.pid, signal.SIGKILL)
            self.process.wait(timeout=2)
        self.process.stdout.close()
        if not self.process.stdin.closed:
            self.process.stdin.close()


def validate_fixture(value, definitions):
    if set(value) != {"schema_version", "workspace", "profiles", "cases"} or value["schema_version"] != 1:
        raise ValueError("fixture shape")
    root = Path(value["workspace"]).resolve(strict=True)
    import tempfile
    roots = {Path("/tmp").resolve(), Path(tempfile.gettempdir()).resolve()}
    if not any(root != base and base in root.parents for base in roots) or not (root / ".git").exists():
        raise ValueError("disposable Git workspace required")
    if not isinstance(value["profiles"], dict) or not 1 <= len(value["profiles"]) <= 16:
        raise ValueError("profile limit")
    for name, selection in value["profiles"].items():
        if name not in definitions["roles"]:
            raise ValueError("unknown profile role")
        selected = private(selection)
        if set(selected) != {"schema_version", "binary", "connection_file"} or selected["schema_version"] != 1:
            raise ValueError("selection shape")
        connection = private(selected["connection_file"])
        if Path(connection["workspace"]).resolve() != root:
            raise ValueError("cross workspace profile")
    if set(value["cases"]) - set(definitions["cases"]):
        raise ValueError("unknown case")
    return value


def run(fixture, definitions, bootstrap, host):
    reports = []
    for name, spec in definitions["cases"].items():
        steps = fixture["cases"].get(name)
        if steps is None or spec["mode"] == "live-only":
            reports.append({"case": name, "status": "unsupported", "reason": spec["unsupported"]})
            continue
        if not isinstance(steps, list) or not 1 <= len(steps) <= 32:
            raise ValueError("step limit")
        brokers, results = {}, []
        try:
            for step_index, step in enumerate(steps):
                if set(step) != {"profile", "tool", "arguments", "expect", "restart"}:
                    raise ValueError("closed step shape")
                role = step["profile"]
                if role not in fixture["profiles"] or type(step["restart"]) is not bool:
                    raise ValueError("profile binding")
                if step["restart"] and role in brokers:
                    brokers.pop(role).close(crash=True)
                if role not in brokers:
                    brokers[role] = Broker(bootstrap, fixture["profiles"][role])
                result = brokers[role].tool(step["tool"], step["arguments"])
                expected = step["expect"]
                if set(expected) != {"exit_code", "fields", "equal_to"} or expected["exit_code"] not in spec["exit_codes"]:
                    raise ValueError("scenario expectation")
                code = 0 if result["ok"] else result["error"]["exit_code"]
                if code != expected["exit_code"]:
                    raise ValueError("wrong refusal class")
                for pointer, literal in expected["fields"].items():
                    if field(result, pointer) != literal:
                        raise ValueError("wrong producer binding")
                for pointer, reference in expected["equal_to"].items():
                    if set(reference) != {"step", "pointer"} or not 0 <= reference["step"] < len(results) or field(result, pointer) != field(results[reference["step"]], reference["pointer"]):
                        raise ValueError("immutable replay mismatch")
                # Require case-specific facts; any generic refusal is insufficient.
                if step_index == len(steps) - 1 and not set(spec["required_fields"]).issubset(expected["fields"].keys() | expected["equal_to"].keys()):
                    raise ValueError("missing causal expectation")
                results.append(result)
            if name in {"overlapping-lease", "forged-reviewer", "stale-evidence"}:
                # Negative tests require an earlier authenticated positive control;
                # a refusal caused by unrelated invalid setup is not conformance.
                if not any(x["ok"] for x in results[:-1]) or results[-1]["ok"]:
                    raise ValueError("missing positive control before negative")
            if name in {"exact-replay", "broker-restart", "duplicate-events"}:
                if len(results) < 2 or not any(x["restart"] for x in steps[1:]) and name == "broker-restart" or not steps[-1]["expect"]["equal_to"]:
                    raise ValueError("missing exact durable replay binding")
            reports.append({"case": name, "status": "observed-backend", "calls": len(results)})
        except (ValueError, KeyError, TypeError, OSError, TimeoutError, queue.Empty):
            reports.append({"case": name, "status": "failed-or-unknown", "calls": len(results)})
            break
        finally:
            for broker in brokers.values():
                broker.close()
    return {"schema_version": 1, "assurance": "backend-stdio-only", "host": host,
            "host_certified": False, "cases": reports}


def main(host="shared"):
    parser = argparse.ArgumentParser()
    parser.add_argument("--fixture", required=True)
    parser.add_argument("--bootstrap", required=True)
    parser.add_argument("--output", required=True)
    args = parser.parse_args()
    definitions = json.loads(Path(__file__).with_name("conformance-cases.json").read_text())
    report = run(validate_fixture(private(args.fixture), definitions), definitions, Path(args.bootstrap), host)
    raw = (json.dumps(report, indent=2) + "\n").encode()
    fd = os.open(args.output, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, "wb") as out:
        out.write(raw)
    return 1 if any(x["status"] == "failed-or-unknown" for x in report["cases"]) else 0


if __name__ == "__main__":
    raise SystemExit(main())
