#!/usr/bin/env python3
"""Measure installed Codex protocol capabilities without starting coding agents.

This is a diagnostic, never a dispatch implementation or runtime fallback.
Native collaboration and child profile delivery require separate host evidence.
Run: python3 docs/probes/codex_native_dispatch_probe.py --codex /absolute/codex
"""

import argparse
import datetime
import hashlib
import json
import os
from pathlib import Path
import re
import selectors
import subprocess
import tempfile
import time


def probe(binary):
    binary = Path(binary).resolve(strict=True)
    with tempfile.TemporaryDirectory(prefix="dacli-codex-capability-") as temporary:
        root = Path(temporary)
        environment = os.environ.copy()
        environment["CODEX_HOME"] = str(root / "codex")
        Path(environment["CODEX_HOME"]).mkdir(mode=0o700)
        version = subprocess.run(
            [str(binary), "--version"], env=environment, cwd=root,
            capture_output=True, text=True, timeout=30, check=True,
        ).stdout.strip()
        generated = root / "protocol"
        subprocess.run(
            [str(binary), "app-server", "generate-ts", "--experimental", "--out", str(generated)],
            env=environment, cwd=root, capture_output=True, timeout=30, check=True,
        )
        requests = (generated / "ClientRequest.ts").read_bytes()
        methods = set(re.findall(r'"?method"?:\s*"([^"]+)"', requests.decode()))
        assert "thread/list" in methods and "turn/interrupt" in methods
        config_fields = {}
        for name in ("ThreadStartParams", "ThreadForkParams"):
            data = (generated / "v2" / (name + ".ts")).read_bytes()
            config_fields[name] = {
                "config_field_declared": bool(re.search(r"\bconfig\??:", data.decode())),
                "schema_sha256": hashlib.sha256(data).hexdigest(),
            }

        with (root / "server.stderr").open("wb") as errors:
            process = subprocess.Popen(
                [str(binary), "app-server", "--stdio"], env=environment, cwd=root,
                stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=errors,
            )
            selector = selectors.DefaultSelector()
            selector.register(process.stdout, selectors.EVENT_READ)
            pending = bytearray()
            sequence = 0

            def call(method, params):
                nonlocal sequence
                sequence += 1
                process.stdin.write((json.dumps({"id": sequence, "method": method, "params": params}) + "\n").encode())
                process.stdin.flush()
                deadline = time.monotonic() + 30
                while time.monotonic() < deadline:
                    while b"\n" in pending:
                        line, _, remainder = pending.partition(b"\n")
                        pending[:] = remainder
                        reply = json.loads(line)
                        if reply.get("id") == sequence:
                            return reply
                    if selector.select(max(0, deadline - time.monotonic())):
                        block = os.read(process.stdout.fileno(), 65536)
                        assert block, "app-server ended before its response"
                        pending.extend(block)
                        assert len(pending) <= 1048576, "response budget exceeded"
                raise TimeoutError("read-only capability observation timed out")

            try:
                initialized = call("initialize", {
                    "clientInfo": {"name": "dacli-native-dispatch-probe", "version": "1"},
                    "capabilities": {"experimentalApi": True},
                })
                assert "result" in initialized, "initialization failed"
                process.stdin.write(b'{"method":"initialized"}\n')
                process.stdin.flush()
                listing = call("thread/list", {"limit": 1})
                assert listing.get("result", {}).get("data") == [], "isolated home contains threads"
                observed = {}
                for method in ("agent/spawn", "collaboration/spawn_agent"):
                    if method in methods:
                        # Do not exercise a newly declared mutating method. Its
                        # execution semantics require a separately authorized test.
                        observed[method] = {
                            "declared_in_protocol": True,
                            "unknown_method_refusal_observed": False,
                            "status": "unverified",
                        }
                        continue
                    response = call(method, {})
                    message = response.get("error", {}).get("message", "")
                    unknown = "unknown variant" in message and method in message
                    observed[method] = {
                        "declared_in_protocol": method in methods,
                        "unknown_method_refusal_observed": unknown,
                        "status": "unsupported" if unknown and method not in methods else "unverified",
                    }
                cancellation = call("turn/interrupt", {
                    "threadId": "00000000-0000-0000-0000-000000000000",
                    "turnId": "00000000-0000-0000-0000-000000000000",
                })
                assert "error" in cancellation, "nonexistent turn unexpectedly acknowledged"
                cancellation_message = cancellation["error"].get("message", "")
                result = {
                    "schema_version": 1,
                    "probe": "dacli-codex-native-dispatch/v1",
                    "observed_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
                    "host_version": version,
                    "binary_sha256": hashlib.sha256(binary.read_bytes()).hexdigest(),
                    "client_protocol_sha256": hashlib.sha256(requests).hexdigest(),
                    "declared_method_count": len(methods),
                    "app_server_dispatch": observed,
                    "thread_config_schema": config_fields,
                    "native_collaboration": {"status": "requires-native-host-attestation"},
                    "private_child_profile_delivery": {"status": "unverified"},
                    "stable_dispatch_key_lookup": {"status": "unverified"},
                    "cancellation": {
                        "method_declared": "turn/interrupt" in methods,
                        "recognized_method_refusal": "unknown variant" not in cancellation_message,
                        "running_turn_cancellation": "unverified",
                    },
                    "scope": {
                        "isolated_codex_home": True,
                        "thread_created": False,
                        "model_turn_requested": False,
                        "worker_credentials_read": False,
                        "native_child_dispatched": False,
                    },
                }
            finally:
                process.stdin.close()
                try:
                    process.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    process.terminate()
                    process.wait(timeout=10)
                selector.close()
            assert process.returncode == 0, "app-server did not exit cleanly"
            result["app_server_exit_code"] = process.returncode
            return result


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--codex", required=True, help="installed Codex executable")
    arguments = parser.parse_args()
    print(json.dumps(probe(arguments.codex), indent=2))
