#!/usr/bin/env python3
"""Qualify extracted dacli project sync against native Keychain and a loopback fixture.

This optional local Darwin qualification invokes the real CLI in two local
projects. It makes no production-provider, hosted-service, or deployment claim
and is never invoked by CI automatically.

Run from a dacli checkout with an extracted native artifact and a private,
empty evidence directory:

    python3 scripts/qualify-project-sync.py --binary /path/to/dacli --arch arm64 --evidence-dir /private/tmp/dacli-sync-evidence
"""

from __future__ import annotations

import argparse
import hashlib
import importlib.util
import json
import os
import re
import subprocess
import sys
from pathlib import Path
from typing import Any

BASE_PATH = Path(__file__).with_name("qualify-device-identity.py")
SPEC = importlib.util.spec_from_file_location("dacli_device_identity_qualification", BASE_PATH)
if SPEC is None or SPEC.loader is None:
    raise RuntimeError("could not load shared native Keychain qualification harness")
BASE = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = BASE
SPEC.loader.exec_module(BASE)

QualificationError = BASE.QualificationError
json_output = BASE.json_output
run_capture = BASE.run_capture

SENTINEL_TITLE = "DACLI-981-LOCAL-TASK-TITLE-"
SENTINEL_ACCEPTANCE = "DACLI-981-LOCAL-ACCEPTANCE-"
PROJECTS = ("alpha", "beta")
REPOSITORY_URL = "https://example.invalid/dacli-qualification"
REPOSITORY_ID = "qualification-repository"
TENANT = "alpha"
DEVICE = "device-alpha"
REMOTE_PROJECTS = {"alpha": "remote-alpha", "beta": "remote-beta"}


class ProjectSyncQualification(BASE.Qualification):
    def __init__(self, binary: Path, arch: str, evidence_dir: Path) -> None:
        super().__init__(binary, arch, evidence_dir)
        self.workspace_id = ""
        # Project sync exercises durable retry/replay, not token expiry. Its
        # bounded protocol retries need more than the lifecycle fixture's 2s.
        self.provider_access_ttl = "60s"
        self.repository = self.temp / "work"
        self.routes: dict[str, dict[str, str]] = {}
        self.sent_title = SENTINEL_TITLE + self.control_key[:12]
        self.sent_acceptance = SENTINEL_ACCEPTANCE + self.control_key[12:24]

    def setup(self) -> None:
        os.chdir(Path(__file__).resolve().parents[1])
        super().setup()
        # The disposable workspace has its own root owner. Never reuse a host
        # task identity or grant inside this isolated fixture workspace.
        self.env.pop("DACLI_AGENT", None)
        self._git("init", "--initial-branch=main")
        self._git("config", "user.name", "dacli qualification")
        self._git("config", "user.email", "qualification@example.invalid")
        self._git("remote", "add", "origin", REPOSITORY_URL)
        initialized = self._cli("init", "--name", "qualification-sync")
        if initialized.returncode != 0:
            raise QualificationError("could not initialize isolated project-sync workspace")
        config = (self.repository / ".dacli" / "config.yml").read_text(encoding="utf-8")
        match = re.search(r"(?m)^id: ([0-9A-HJKMNP-TV-Z]{26})$", config)
        if not match:
            raise QualificationError("isolated workspace did not produce a canonical workspace ID")
        self.workspace_id = match.group(1)
        for slug in PROJECTS:
            result = self._cli("project", "add", "Qualification " + slug, "--slug", slug)
            if result.returncode != 0:
                raise QualificationError(f"could not create isolated local project {slug}")
        task = self._cli("task", "add", self.sent_title, "--project", "alpha", "--accept", self.sent_acceptance)
        if task.returncode != 0:
            raise QualificationError("could not create the local allowlist sentinel task")
        beta_task = self._cli("task", "add", "Qualification beta local task", "--project", "beta", "--accept", "inspect beta status")
        if beta_task.returncode != 0:
            raise QualificationError("could not create beta's local task")
        self.login(TENANT)
        for slug in PROJECTS:
            self.routes[slug] = {
                "workspace_id": self.workspace_id,
                "tenant_id": TENANT,
                "project_id": REMOTE_PROJECTS[slug],
                "environment_id": "environment-" + slug,
                "repository_id": REPOSITORY_ID,
                "device_id": DEVICE,
            }
        bindings = [
            {
                "schema_version": 1,
                "route": self.routes[slug],
                "binding_version": 1,
                "repository_url": REPOSITORY_URL,
                "default_branch": "main",
                "minimum_schema_version": 1,
                "maximum_schema_version": 1,
                "trusted_keys": {"qualification-server-signing-key": self._server_public_key()},
            }
            for slug in PROJECTS
        ]
        if self._admin("configure-bindings", {"bindings": bindings}).get("configured") is not True:
            raise QualificationError("loopback fixture did not install the exact two project bindings")
        self.observations.update({"workspace_id": self.workspace_id, "local_projects": list(PROJECTS), "provider": "loopback synthetic signed-sync fixture"})

    def _server_public_key(self) -> str:
        state = self._admin("state")
        value = state.get("alpha", {}).get("server_public_key")
        if not isinstance(value, str) or not value:
            raise QualificationError("loopback fixture did not expose its public server verification key")
        return value

    def _git(self, *args: str) -> subprocess.CompletedProcess[bytes]:
        result = run_capture(["git", *args], self.env, timeout=10)
        self.outputs.extend((result.stdout, result.stderr))
        self._scan()
        if result.returncode != 0:
            raise QualificationError("could not prepare isolated local git repository")
        return result

    def _admin(self, path: str, payload: dict[str, Any] | None = None) -> Any:
        return super()._admin(path, payload)

    def _cli_json(self, *args: str) -> Any:
        result = self._cli(*args, "--json")
        if result.returncode != 0:
            raise QualificationError(f"{' '.join(args)} failed with exit {result.returncode}")
        try:
            return json.loads(result.stdout)
        except (UnicodeDecodeError, json.JSONDecodeError) as exc:
            raise QualificationError(f"{' '.join(args)} did not return JSON") from exc

    def _status(self, slug: str) -> dict[str, Any]:
        return self._cli_json("project", "status", slug)

    def _task_rows(self, slug: str) -> list[dict[str, Any]]:
        result = self._cli_json("task", "list", "--project", slug)
        if not isinstance(result, list):
            raise QualificationError(f"task list for {slug} did not return a JSON array")
        return result

    def _outbox(self, slug: str) -> dict[str, bytes]:
        root = self.repository / ".dacli" / "cloudsync" / slug / "transport" / "outbox"
        if not root.is_dir():
            return {}
        return {entry.name: entry.read_bytes() for entry in sorted(root.glob("*.json"))}

    @staticmethod
    def _digest_rows(rows: dict[str, bytes]) -> dict[str, str]:
        return {name: hashlib.sha256(raw).hexdigest() for name, raw in rows.items()}

    def _connect(self, slug: str) -> dict[str, Any]:
        route = self.routes[slug]
        return self._cli_json(
            "project", "connect", slug, "--provider", self.provider_url,
            "--tenant", route["tenant_id"], "--device", route["device_id"],
            "--remote-project", route["project_id"], "--environment", route["environment_id"],
            "--repository", route["repository_id"],
        )

    def _sync(self, slug: str, expected_code: int = 0) -> dict[str, Any] | None:
        result = self._cli("project", "sync", slug, "--json")
        if result.returncode != expected_code:
            raise QualificationError(f"project sync {slug} exited {result.returncode}, expected {expected_code}")
        if expected_code == 0:
            return json_output(result, "project sync")
        if expected_code == 3:
            try:
                error = json.loads(result.stderr)
            except (UnicodeDecodeError, json.JSONDecodeError) as exc:
                raise QualificationError("refused project sync did not return a typed JSON refusal") from exc
            if not isinstance(error, dict) or error.get("exit_code") != 3:
                raise QualificationError("project sync returned an untyped policy refusal")
            return None
        return None

    def _expect_policy_refusal(self, *args: str) -> None:
        result = self._cli(*args, "--json")
        if result.returncode != 3:
            raise QualificationError(f"{' '.join(args)} exited {result.returncode}, expected policy refusal 3")
        try:
            error = json.loads(result.stderr)
        except (UnicodeDecodeError, json.JSONDecodeError) as exc:
            raise QualificationError(f"{' '.join(args)} did not return a typed JSON refusal") from exc
        if not isinstance(error, dict) or error.get("exit_code") != 3:
            raise QualificationError(f"{' '.join(args)} returned an untyped policy refusal")

    def _connection(self, slug: str) -> dict[str, Any]:
        path = self.repository / ".dacli" / "cloudsync" / slug / "connection.json"
        return json.loads(path.read_text(encoding="utf-8"))

    def _assert_inbound_is_proposal_only(self, slug: str, before_ids: set[str]) -> None:
        rows = self._task_rows(slug)
        if before_ids != {row.get("id") for row in rows}:
            raise QualificationError(f"signed inbound proposal changed canonical {slug} tasks")
        inbox = self.repository / ".dacli" / "cloudsync" / slug / "transport" / "inbox"
        files = sorted(inbox.glob("*.json")) if inbox.exists() else []
        if not files:
            raise QualificationError(f"signed inbound proposal was not persisted for {slug}")
        proposal_found = False
        for path in files:
            envelope = json.loads(path.read_text(encoding="utf-8"))
            if envelope.get("tenant_id") != TENANT or envelope.get("project_id") != REMOTE_PROJECTS[slug]:
                raise QualificationError("inbox contains an inbound envelope for a different route")
            if envelope.get("event_type") == "task_proposal":
                proposal_found = True
        if not proposal_found:
            raise QualificationError(f"inbound {slug} inbox does not contain the signed proposal")

    def _assert_sentinel_excluded(self, slug: str) -> None:
        for path in (self.repository / ".dacli" / "cloudsync" / slug / "transport" / "outbox").glob("*.json"):
            raw = path.read_bytes()
            if self.sent_title.encode() in raw or self.sent_acceptance.encode() in raw:
                raise QualificationError("local task title or acceptance prose escaped into signed sync metadata")

    def exercise(self) -> None:
        if self.observations.get("process_argv_samples", 0) == 0:
            raise QualificationError("could not sample the extracted artifact's operating-system argv")
        before = {slug: {row["id"] for row in self._task_rows(slug)} for slug in PROJECTS}
        for slug in PROJECTS:
            status = self._connect(slug)
            if status.get("connected") is not True or status.get("health") != "connected":
                raise QualificationError(f"connect did not bind exact project {slug}")
            if self._connection(slug).get("binding", {}).get("route") != self.routes[slug]:
                raise QualificationError(f"project {slug} persisted a different workspace or route binding")

        # Force a response loss after the fixture has durably accepted alpha's
        # envelopes. The retry must receive exact duplicate acknowledgements;
        # a later no-op sync must send no already acknowledged rows. Beta then
        # receives a deliberately malformed ack, which the CLI must reject
        # without advancing its durable acknowledgment cursor.
        self._admin("drop-next-exchange-response", {"drop": True})
        self._sync("alpha")
        self._admin("set-offline", {"offline": True})
        self._sync("beta", expected_code=1)
        beta_outbox_before = self._outbox("beta")
        if len(beta_outbox_before) != 2:
            raise QualificationError("offline beta setup did not persist its exact two-row signed queue")
        self._admin("set-offline", {"offline": False})
        self._admin("tamper-next-ack", {"tamper": True})
        self._expect_policy_refusal("project", "sync", "beta")
        beta_refused = self._status("beta")
        if beta_refused.get("backlog") != 2 or beta_refused.get("acknowledged_sequence") != 0:
            raise QualificationError("malformed beta ack advanced its cursor or discarded queued rows")
        beta_outbox_after_refusal = self._digest_rows(self._outbox("beta"))
        if beta_outbox_after_refusal != self._digest_rows(beta_outbox_before):
            raise QualificationError(f"malformed beta ack changed immutable outbox rows: before={self._digest_rows(beta_outbox_before)} after={beta_outbox_after_refusal}")
        self._sync("beta")
        state = self._admin("state")
        sync_state = state.get("sync", {})
        if sync_state.get("accepted", 0) < 4 or sync_state.get("duplicates", 0) < 4 or sync_state.get("tampered_acks") != 1:
            raise QualificationError("fixture did not observe signed envelopes and duplicate retry acknowledgements")
        alpha_count = len(sync_state.get("received_by_route", {}).get(self._route_key("alpha"), []))
        self._sync("alpha")
        after_noop = self._admin("state").get("sync", {})
        if len(after_noop.get("received_by_route", {}).get(self._route_key("alpha"), [])) != alpha_count:
            raise QualificationError("acknowledged alpha envelopes were resent by a later no-op sync")
        if after_noop.get("empty_exchanges", 0) < 1:
            raise QualificationError("provider did not observe an empty exchange after exact acknowledgements")
        self.observations["exact_ack_retry"] = {"accepted": sync_state.get("accepted"), "duplicate_acknowledgements": sync_state.get("duplicates"), "malformed_ack_refused_with_queue_preserved": True, "empty_exchange_no_resend": True}
        for slug in PROJECTS:
            self._assert_inbound_is_proposal_only(slug, before[slug])
            self._assert_sentinel_excluded(slug)
            status = self._status(slug)
            if status.get("backlog") != 0 or not status.get("last_success"):
                raise QualificationError(f"healthy JSON status did not report an empty acknowledged {slug} queue")
            human = self._cli("project", "status", slug)
            if human.returncode != 0 or b"Health:connected" not in human.stdout or b"Backlog:0" not in human.stdout:
                raise QualificationError(f"human project status did not report connected {slug} state")

        # Add another local task while the provider is offline. The CLI signs
        # the allowlisted digest into its durable outbox and process exit does
        # not change any immutable row on the next invocation.
        offline_title = "DACLI-981-OFFLINE-" + self.control_key[24:36]
        added = self._cli("task", "add", offline_title, "--project", "alpha", "--accept", "keep queued offline")
        if added.returncode != 0:
            raise QualificationError("could not add a new local event for offline queue replay")
        self._admin("set-offline", {"offline": True})
        self._sync("alpha", expected_code=1)
        queued = self._outbox("alpha")
        queued_digest = self._digest_rows(queued)
        failed_status = self._status("alpha")
        if failed_status.get("health") != "offline-or-refused" or failed_status.get("backlog") != 1 or failed_status.get("last_failure_code") != "exchange-unavailable":
            raise QualificationError("offline JSON status did not report the queued event and exchange failure")
        human_offline = self._cli("project", "status", "alpha")
        if human_offline.returncode != 0 or b"Health:offline-or-refused" not in human_offline.stdout or b"Backlog:1" not in human_offline.stdout:
            raise QualificationError("human project status did not expose the offline backlog")
        self._sync("alpha", expected_code=1)
        if self._digest_rows(self._outbox("alpha")) != queued_digest:
            raise QualificationError("offline retry changed an immutable queued envelope after process restart")
        self._admin("set-offline", {"offline": False})
        self._sync("alpha")
        replayed = self._status("alpha")
        if replayed.get("backlog") != 0 or replayed.get("health") != "connected" or not replayed.get("last_success"):
            raise QualificationError("restored provider did not replay and acknowledge the durable offline queue")
        self._assert_sentinel_excluded("alpha")
        self.observations["offline_replay"] = {"queued_rows": len(queued), "outbox_unchanged_across_restart": True, "backlog_after_replay": replayed.get("backlog")}

        # Disconnect records a tombstone and a registration event. Reconnect
        # to the same exact binding replays both historical generations.
        ids_before_disconnect = {row.get("id") for row in self._task_rows("alpha")}
        disconnected = self._cli_json("project", "disconnect", "alpha")
        tombstone = self._connection("alpha")
        if disconnected.get("connected") is not False or tombstone.get("connected") is not False or tombstone.get("generation") != 2 or not tombstone.get("disconnected_at"):
            raise QualificationError("disconnect did not preserve generation-2 tombstone")
        local_rows = self._task_rows("alpha")
        if {row.get("id") for row in local_rows} != ids_before_disconnect:
            raise QualificationError("disconnect prevented local task reads")
        self._sync("alpha", expected_code=3)
        tombstone_raw = (self.repository / ".dacli" / "cloudsync" / "alpha" / "connection.json").read_bytes()
        disconnected_outbox = self._outbox("alpha")
        self._admin("set-binding-drift", {"drift": True})
        self._expect_policy_refusal(
            "project", "connect", "alpha", "--provider", self.provider_url,
            "--tenant", TENANT, "--device", DEVICE,
            "--remote-project", REMOTE_PROJECTS["alpha"],
            "--environment", self.routes["alpha"]["environment_id"],
            "--repository", REPOSITORY_ID,
        )
        if (self.repository / ".dacli" / "cloudsync" / "alpha" / "connection.json").read_bytes() != tombstone_raw or self._digest_rows(self._outbox("alpha")) != self._digest_rows(disconnected_outbox):
            raise QualificationError("binding drift changed the disconnected tombstone or its queued history")
        self.observations["binding_drift_refused"] = {"tombstone_preserved": True, "queued_history_preserved": True}
        self._admin("set-binding-drift", {"drift": False})
        reconnected = self._connect("alpha")
        connected = self._connection("alpha")
        if reconnected.get("connected") is not True or connected.get("generation") != 3 or connected.get("binding", {}).get("route") != self.routes["alpha"]:
            raise QualificationError("reconnect changed the binding or failed to preserve transition generation")
        self._sync("alpha")
        registrations = self._admin("state").get("sync", {}).get("registrations", [])
        alpha_transitions = [(row.get("version"), row.get("state")) for row in registrations if row.get("project_id") == REMOTE_PROJECTS["alpha"]]
        if alpha_transitions != [(1, "connected"), (2, "disconnected"), (3, "connected")]:
            raise QualificationError(f"fixture did not receive ordered alpha reconnect transitions: {alpha_transitions}")
        self.observations["reconnect_transitions"] = alpha_transitions

        # Revoke the exact device after queueing a new local event. Refusal
        # must leave its immutable row available for later owner recovery.
        revoked_title = "DACLI-981-REVOKED-" + self.control_key[36:48]
        added = self._cli("task", "add", revoked_title, "--project", "alpha", "--accept", "preserve on revocation")
        if added.returncode != 0:
            raise QualificationError("could not add a local event for revoked identity refusal")
        self._admin("set-offline", {"offline": True})
        self._sync("alpha", expected_code=1)
        before_revoked = self._outbox("alpha")
        if self._status("alpha").get("backlog") != 1:
            raise QualificationError("revocation fixture did not establish one durable pending row")
        self._admin("revoke-tenant", {"tenant": TENANT})
        self._admin("set-offline", {"offline": False})
        self._sync("alpha", expected_code=3)
        if self._digest_rows(self._outbox("alpha")) != self._digest_rows(before_revoked):
            raise QualificationError("revoked identity refusal altered the durable signed queue")
        refused_status = self._status("alpha")
        # Revalidation reaches whoami, whose closed OAuth contract reports a
        # revoked bearer as invalid_grant; cloud status preserves that exact
        # typed refusal code while the immutable local queue remains pending.
        if refused_status.get("backlog") != 1 or refused_status.get("last_failure_code") != "invalid_grant":
            raise QualificationError(f"revoked identity status mismatch: backlog={refused_status.get('backlog')} failure={refused_status.get('last_failure_code')}")
        if len(self._task_rows("alpha")) != len(local_rows) + 1:
            raise QualificationError("revoked project sync changed canonical local tasks")
        self.observations["revoked_identity"] = {"refused": True, "queue_preserved": True, "backlog": refused_status.get("backlog")}
        self._scan()

    def _route_key(self, slug: str) -> str:
        return "\x00".join(self.routes[slug][field] for field in ("workspace_id", "tenant_id", "project_id", "environment_id", "repository_id", "device_id"))

    def write_evidence(self, status: str, error: str | None = None) -> None:
        self.evidence_dir.mkdir(parents=True, exist_ok=True, mode=0o700)
        if self.evidence_dir.stat().st_mode & 0o077:
            raise QualificationError("evidence directory must be private (mode 0700 or stricter)")
        report = dict(self.observations)
        report["result"] = status
        if error:
            report["failure"] = error
        path = self.evidence_dir / f"project-sync-{self.arch}.json"
        fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, "w", encoding="utf-8") as output:
            json.dump(report, output, sort_keys=True, indent=2)
            output.write("\n")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, type=Path, help="extracted native Darwin dacli executable")
    parser.add_argument("--arch", required=True, choices=("amd64", "arm64"))
    parser.add_argument("--evidence-dir", required=True, type=Path, help="private directory for sanitized result JSON")
    args = parser.parse_args()
    qualification: ProjectSyncQualification | None = None
    outcome = "failed"
    error_text: str | None = None
    return_code = 1
    try:
        qualification = ProjectSyncQualification(args.binary, args.arch, args.evidence_dir)
        qualification.setup()
        qualification.exercise()
        outcome = "passed"
        return_code = 0
    except (QualificationError, OSError, subprocess.SubprocessError) as exc:
        error_text = str(exc)
        print(error_text, file=sys.stderr)
        return_code = 1 if not error_text.startswith("unverified:") else 77
    finally:
        if qualification is not None:
            try:
                cleanup_ok = qualification.cleanup()
            except BaseException:
                cleanup_ok = False
            qualification.observations["cleanup_verified"] = cleanup_ok
            if not cleanup_ok:
                outcome = "failed"
                error_text = "exact Keychain and child-process cleanup could not be verified"
                return_code = 1
            try:
                qualification.write_evidence(outcome, error_text)
            except BaseException as exc:
                print(f"could not write sanitized qualification evidence: {exc}", file=sys.stderr)
                return_code = 1
    if return_code == 0:
        print("qualified extracted darwin artifact against loopback signed project sync and native device identity lifecycle")
    return return_code


if __name__ == "__main__":
    raise SystemExit(main())
