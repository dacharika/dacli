#!/usr/bin/env python3
"""Exercise the synthetic qualification provider's bounded access-token TTL.

Run with: python3 scripts/test-device-identity-qualification-provider.py
This starts only a disposable loopback provider and uses synthetic credentials.
"""
from __future__ import annotations

import argparse
import base64
import json
import os
from pathlib import Path
import secrets
import select
import shutil
import subprocess
import tempfile
import time
from urllib.error import HTTPError
from urllib.request import Request, urlopen

ROOT = Path(__file__).resolve().parents[1]
SOURCE = ROOT / "scripts" / "device-identity-qualification-provider.go"


def request(
    url: str, method: str, payload: dict | None = None, token: str | None = None
) -> tuple[int, dict]:
    body = None if payload is None else json.dumps(payload).encode()
    headers = {"Content-Type": "application/json"}
    if token is not None:
        headers["Authorization"] = "Bearer " + token
    req = Request(url, data=body, headers=headers, method=method)
    try:
        with urlopen(req, timeout=3) as response:
            return response.status, json.loads(response.read())
    except HTTPError as exc:
        return exc.code, json.loads(exc.read())


def start_provider(binary: Path, args: list[str]) -> tuple[subprocess.Popen, str]:
    process = subprocess.Popen(
        [str(binary), *args], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
        stderr=subprocess.PIPE, text=True,
    )
    assert process.stdin is not None and process.stdout is not None
    process.stdin.write(secrets.token_urlsafe(48) + "\n")
    process.stdin.flush()
    if not select.select([process.stdout], [], [], 5)[0]:
        process.terminate()
        process.wait(timeout=2)
        raise AssertionError("provider did not bind loopback within five seconds")
    address = process.stdout.readline().strip()
    if not address.startswith("http://127.0.0.1:"):
        process.terminate()
        process.wait(timeout=2)
        raise AssertionError("provider did not bind an ephemeral loopback address")
    return process, address


def login(address: str, tenant: str) -> dict:
    status, device = request(
        address + "/v1/auth/device", "POST",
        {"tenant_id": tenant, "public_key": base64.b64encode(os.urandom(32)).decode("ascii")},
    )
    assert status == 200, "device authorization failed"
    status, token = request(
        address + "/v1/auth/token", "POST",
        {"grant_type": "urn:ietf:params:oauth:grant-type:device_code", "device_code": device["device_code"]},
    )
    assert status == 200, "synthetic token issuance failed"
    return token


def verify_lifetime(
    binary: Path, args: list[str], expected_seconds: int, wait_seconds: float, expected_after_wait: int
) -> None:
    process, address = start_provider(binary, args)
    try:
        token = login(address, "lifetime")
        assert token.get("expires_in") == expected_seconds, "advertised access lifetime did not match selected TTL"
        status, _ = request(address + "/v1/auth/whoami", "GET", token=token["access_token"])
        assert status == 200, "newly issued access token was rejected"
        time.sleep(wait_seconds)
        status, _ = request(address + "/v1/auth/whoami", "GET", token=token["access_token"])
        assert status == expected_after_wait, "provider enforcement did not match the selected TTL"
    finally:
        process.terminate()
        process.wait(timeout=3)
        if process.stdin is not None:
            process.stdin.close()
        if process.stdout is not None:
            process.stdout.close()
        if process.stderr is not None:
            process.stderr.close()


def reject_invalid_ttl(binary: Path, value: str) -> None:
    try:
        result = subprocess.run(
            [str(binary), "--access-ttl", value], input=secrets.token_urlsafe(48) + "\n",
            stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, timeout=5,
        )
    except subprocess.TimeoutExpired as exc:
        raise AssertionError(f"invalid TTL {value} was not refused before provider startup") from exc
    assert result.returncode == 2, (
        f"invalid TTL {value} exited {result.returncode}, expected usage refusal 2"
    )
    assert "access TTL" in result.stderr, f"invalid TTL {value} did not report its bounded option"


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--provider-overlay", type=Path, help=argparse.SUPPRESS)
    args = parser.parse_args()
    go = shutil.which("go")
    if go is None:
        raise SystemExit("Go is required to build the synthetic provider")
    with tempfile.TemporaryDirectory(prefix="dacli-provider-ttl-", dir="/private/tmp") as temp:
        binary = Path(temp) / "provider"
        command = [go, "build"]
        if args.provider_overlay is not None:
            command.extend(["-overlay", str(args.provider_overlay.resolve(strict=True))])
        command.extend(["-o", str(binary), str(SOURCE)])
        built = subprocess.run(
            command, cwd=ROOT, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=120
        )
        if built.returncode != 0:
            raise SystemExit("synthetic provider build failed")
        verify_lifetime(binary, [], expected_seconds=2, wait_seconds=2.2, expected_after_wait=401)
        verify_lifetime(binary, ["--access-ttl", "30s"], expected_seconds=30, wait_seconds=2.2, expected_after_wait=200)
        reject_invalid_ttl(binary, "500ms")
        reject_invalid_ttl(binary, "301s")
    print("synthetic provider default, bounded override, enforcement, and invalid TTL refusals passed")


if __name__ == "__main__":
    main()
