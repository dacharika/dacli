#!/usr/bin/env python3
"""Qualify an extracted Darwin dacli artifact's native device identity flow.

This uses a loopback-only synthetic provider and the user's normal Keychain
with unique, digest-scoped fixture accounts. It makes no production-provider
or deployment claim and is never invoked by CI automatically.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import platform
import secrets
import selectors
import shutil
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
from pathlib import Path
from typing import Any

SERVICE = "org.dacli.device-identity.v1"
TENANTS = ("alpha", "beta", "revoked", "offline", "missing", "corrupt")
MAX_CHILD_SECONDS = 30


class QualificationError(RuntimeError):
    pass


def account_digest(provider: str, tenant: str, device: str) -> str:
    raw = f"{provider}\0{tenant}\0{device}".encode("utf-8")
    return hashlib.sha256(raw).hexdigest()


def check_arch(arch: str) -> list[str]:
    if arch not in ("amd64", "arm64"):
        raise QualificationError("architecture must be amd64 or arm64")
    machine = platform.machine().lower()
    if machine in ("arm64", "aarch64") and arch == "amd64":
        if not Path("/usr/bin/arch").exists():
            raise QualificationError("unverified: Rosetta runner is unavailable")
        return ["/usr/bin/arch", "-x86_64"]
    if machine in ("x86_64", "amd64") and arch == "arm64":
        raise QualificationError("unverified: arm64 artifact cannot run on this Intel host")
    if machine not in ("arm64", "aarch64", "x86_64", "amd64"):
        raise QualificationError(f"unverified: unsupported Darwin host architecture {machine}")
    return []


def run_capture(argv: list[str], env: dict[str, str], timeout: int = MAX_CHILD_SECONDS) -> subprocess.CompletedProcess[bytes]:
    try:
        return subprocess.run(argv, env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=timeout, check=False)
    except subprocess.TimeoutExpired as exc:
        raise QualificationError(f"child timed out after {timeout}s: {Path(argv[0]).name}") from exc


def json_output(result: subprocess.CompletedProcess[bytes], label: str) -> dict[str, Any]:
    try:
        value = json.loads(result.stdout)
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise QualificationError(f"{label} did not return JSON") from exc
    if not isinstance(value, dict):
        raise QualificationError(f"{label} returned a non-object result")
    return value


class Qualification:
    def __init__(self, binary: Path, arch: str, evidence_dir: Path) -> None:
        self.binary = binary.resolve(strict=True)
        self.arch = arch
        self.evidence_dir = evidence_dir.resolve()
        self.temp = Path(tempfile.mkdtemp(prefix="dacli-device-identity-", dir="/private/tmp"))
        self.prefix = check_arch(arch)
        self.processes: list[subprocess.Popen[bytes]] = []
        self.provider: subprocess.Popen[bytes] | None = None
        self.provider_url = ""
        self.control_key = secrets.token_urlsafe(48)
        self.secrets: set[bytes] = {self.control_key.encode()}
        self.env: dict[str, str] = {}
        self.accounts: set[str] = set()
        self.present_accounts: set[str] = set()
        self.uncertain_accounts: set[str] = set()
        self.secrets: set[bytes] = set()
        self.outputs: list[bytes] = []
        self.observations: dict[str, Any] = {"architecture": arch, "provider": "loopback synthetic fixture"}
        self.profile: Path | None = None

    def setup(self) -> None:
        if sys.platform != "darwin":
            raise QualificationError("unverified: native Darwin Keychain qualification requires macOS")
        if shutil.which("security") is None:
            raise QualificationError("unverified: macOS security utility is unavailable")
        home = Path(os.environ.get("HOME", ""))
        keychains = home / "Library" / "Keychains"
        if not home.is_absolute() or not keychains.is_dir():
            raise QualificationError("unverified: caller's normal Keychains directory is unavailable")
        isolated_home = self.temp / "home"
        (isolated_home / "Library").mkdir(parents=True)
        (isolated_home / "Library" / "Keychains").symlink_to(keychains, target_is_directory=True)
        config = self.temp / "config"
        config.mkdir(mode=0o700)
        work = self.temp / "work"
        work.mkdir(mode=0o700)
        self.profile = isolated_home / "Library" / "Application Support" / "dacli" / "device-identity"
        self.env = os.environ.copy()
        self.env.update({"HOME": str(isolated_home), "XDG_CONFIG_HOME": str(config), "TMPDIR": str(self.temp)})
        for key in ("GOCACHE", "GOPATH", "GOMODCACHE"):
            if key in os.environ:
                self.env[key] = os.environ[key]
        source = Path(__file__).with_name("device-identity-qualification-provider.go")
        provider_binary = self.temp / "provider"
        go = shutil.which("go")
        if not go:
            raise QualificationError("Go is required to build the synthetic qualification provider")
        built = run_capture([go, "build", "-o", str(provider_binary), str(source)], os.environ.copy(), timeout=120)
        if built.returncode != 0:
            raise QualificationError("synthetic provider build failed")
        self.outputs.extend((built.stdout, built.stderr))
        self._scan()
        self.provider = subprocess.Popen(
            [str(provider_binary)], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
            env=os.environ.copy(),
        )
        self.processes.append(self.provider)
        assert self.provider.stdin is not None and self.provider.stdout is not None
        self.provider.stdin.write((self.control_key + "\n").encode())
        self.provider.stdin.flush()
        line = self._read_provider_address(self.provider)
        if not line.startswith("http://127.0.0.1:"):
            raise QualificationError("synthetic provider did not bind an ephemeral loopback address")
        self.provider_url = line
        self._scan()
        (self.temp / "work").resolve()
        os.chdir(self.temp / "work")
        self.observations["host_architecture"] = platform.machine()

    @staticmethod
    def _read_provider_address(process: subprocess.Popen[bytes]) -> str:
        assert process.stdout is not None
        selector = selectors.DefaultSelector()
        selector.register(process.stdout, selectors.EVENT_READ)
        deadline = time.monotonic() + 5
        raw = bytearray()
        try:
            while time.monotonic() < deadline:
                if process.poll() is not None:
                    raise QualificationError("synthetic provider exited before binding loopback")
                events = selector.select(timeout=min(0.1, deadline - time.monotonic()))
                if not events:
                    continue
                chunk = os.read(process.stdout.fileno(), 512)
                if not chunk:
                    raise QualificationError("synthetic provider closed startup output")
                raw.extend(chunk)
                if len(raw) > 512:
                    raise QualificationError("synthetic provider startup output exceeded its bound")
                if b"\n" in raw:
                    line, _, remainder = raw.partition(b"\n")
                    if remainder:
                        raise QualificationError("synthetic provider emitted unexpected startup output")
                    return line.decode("ascii", "strict").strip()
            raise QualificationError("synthetic provider startup timed out after 5s")
        finally:
            selector.close()

    def _cli(self, *args: str, timeout: int = MAX_CHILD_SECONDS) -> subprocess.CompletedProcess[bytes]:
        argv = self.prefix + [str(self.binary), *args]
        # Command arguments are inspected before execution; no credential is
        # ever accepted as an argument by this qualification interface.
        self._scan_bytes(" ".join(argv).encode())
        process = subprocess.Popen(argv, env=self.env, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        self.processes.append(process)
        ps = run_capture(["/bin/ps", "-ww", "-p", str(process.pid), "-o", "command="], self.env, timeout=5)
        if ps.returncode == 0:
            self._scan_bytes(ps.stdout)
            self._scan_bytes(ps.stderr)
        stdout = stderr = b""
        deadline = time.monotonic() + timeout
        while True:
            try:
                stdout, stderr = process.communicate(timeout=min(0.1, max(0.001, deadline - time.monotonic())))
                break
            except subprocess.TimeoutExpired:
                if time.monotonic() >= deadline:
                    process.terminate()
                    try:
                        stdout, stderr = process.communicate(timeout=2)
                    except subprocess.TimeoutExpired:
                        process.kill()
                        try:
                            stdout, stderr = process.communicate(timeout=2)
                        except subprocess.TimeoutExpired as exc:
                            self.outputs.extend((stdout, stderr))
                            raise QualificationError("artifact child remained alive after terminate and kill") from exc
                    self.outputs.extend((stdout, stderr))
                    raise QualificationError(f"child timed out after {timeout}s: {Path(argv[0]).name}")
                ps = run_capture(["/bin/ps", "-ww", "-p", str(process.pid), "-o", "command="], self.env, timeout=2)
                if ps.returncode == 0:
                    self._scan_bytes(ps.stdout)
                    self._scan_bytes(ps.stderr)
                    self.observations["process_argv_samples"] = self.observations.get("process_argv_samples", 0) + 1
        result = subprocess.CompletedProcess(argv, process.returncode, stdout, stderr)
        self.outputs.extend((result.stdout, result.stderr))
        self._scan()
        return result

    def _admin(self, path: str, payload: dict[str, Any] | None = None) -> Any:
        body = None if payload is None else json.dumps(payload).encode()
        request = urllib.request.Request(
            self.provider_url + "/__qualification/" + path,
            data=body,
            headers={"X-Qualification-Control": self.control_key, "Content-Type": "application/json"},
            method="GET" if payload is None else "POST",
        )
        try:
            with urllib.request.urlopen(request, timeout=3) as response:
                value = json.loads(response.read(65537))
        except (OSError, urllib.error.URLError, json.JSONDecodeError) as exc:
            raise QualificationError(f"synthetic provider control request failed: {path}") from exc
        if path == "state":
            for identity in value.values():
                for secret in identity.get("secrets_for_in_memory_scan", []):
                    if isinstance(secret, str) and secret:
                        self.secrets.add(secret.encode())
            self._scan()
        return value

    def _scan_bytes(self, raw: bytes) -> None:
        for secret in self.secrets:
            if secret and secret in raw:
                raise QualificationError("synthetic credential appeared in command arguments or captured output")

    def _scan(self) -> None:
        for output in self.outputs:
            self._scan_bytes(output)
        if self.temp.exists():
            for path in self.temp.rglob("*"):
                if path.is_symlink() or path == self.temp / "provider":
                    continue
                if path.is_file():
                    try:
                        data = path.read_bytes()
                    except OSError:
                        continue
                    self._scan_bytes(data)

    def _command_result(self, tenant: str, label: str, result: subprocess.CompletedProcess[bytes], success: bool) -> dict[str, Any]:
        expected = 0 if success else 3
        if result.returncode != expected:
            raise QualificationError(f"{label} for tenant {tenant} exited {result.returncode}, expected {expected}")
        if success:
            return json_output(result, label)
        try:
            value = json.loads(result.stderr)
        except (UnicodeDecodeError, json.JSONDecodeError) as exc:
            raise QualificationError(f"{label} did not return a typed JSON refusal") from exc
        if not isinstance(value, dict) or value.get("exit_code") != 3:
            raise QualificationError(f"{label} returned an untyped or non-refusal error")
        return value

    def _identity_args(self, tenant: str, command: str = "whoami") -> tuple[str, ...]:
        return ("auth", command, "--provider", self.provider_url, "--tenant", tenant, "--device", "device-" + tenant, "--json")

    def login(self, tenant: str) -> None:
        # The native credential can be stored before login later fails on
        # metadata or output handling. Register its exact cleanup key first.
        account = account_digest(self.provider_url, tenant, "device-" + tenant)
        self.accounts.add(account)
        self.uncertain_accounts.add(account)
        result = self._cli("auth", "login", "--provider", self.provider_url, "--tenant", tenant,
                           "--name", "Qualification-" + tenant, "--json")
        value = self._command_result(tenant, "login", result, True)
        if value.get("tenant_id") != tenant or value.get("device_id") != "device-" + tenant:
            raise QualificationError(f"login returned an identity outside tenant {tenant}")
        self.uncertain_accounts.discard(account)
        self.present_accounts.add(account)
        self._admin("state")

    def logout(self, tenant: str, success: bool, expected_remote: bool | None = None) -> dict[str, Any]:
        result = self._cli(*self._identity_args(tenant, "logout"))
        value = json_output(result, "logout")
        if result.returncode != (0 if success else 1):
            raise QualificationError(f"logout for tenant {tenant} exited {result.returncode}")
        if value.get("TenantID") != tenant or value.get("DeviceID") != "device-" + tenant:
            raise QualificationError(f"logout selected the wrong identity for tenant {tenant}")
        if value.get("LocalRemoved") is not True:
            raise QualificationError(f"logout did not remove tenant {tenant}'s exact native credential")
        account = account_digest(self.provider_url, tenant, "device-" + tenant)
        self.present_accounts.discard(account)
        self.uncertain_accounts.discard(account)
        if expected_remote is not None and value.get("RemoteRevoked") is not expected_remote:
            raise QualificationError(f"logout remote revoke result did not match for tenant {tenant}")
        self._assert_keychain(tenant, exists=False)
        self._assert_neighbor_accounts()
        return value

    def _assert_keychain(self, tenant: str, exists: bool) -> None:
        account = account_digest(self.provider_url, tenant, "device-" + tenant)
        result = run_capture(["/usr/bin/security", "find-generic-password", "-s", SERVICE, "-a", account], self.env)
        self.outputs.extend((result.stdout, result.stderr))
        self._scan()
        if result.returncode != (0 if exists else 44):
            raise QualificationError(f"exact Keychain account presence mismatch for tenant {tenant}")

    def _assert_neighbor_accounts(self) -> None:
        for tenant in TENANTS:
            account = account_digest(self.provider_url, tenant, "device-" + tenant)
            result = run_capture(["/usr/bin/security", "find-generic-password", "-s", SERVICE, "-a", account], self.env)
            self.outputs.extend((result.stdout, result.stderr))
            self._scan()
            if result.returncode != (0 if account in self.present_accounts else 44):
                raise QualificationError(f"one exact identity operation changed a neighbor Keychain account ({tenant})")

    def exercise(self) -> None:
        self.login("alpha")
        if self.observations.get("process_argv_samples", 0) == 0:
            raise QualificationError("could not sample the running artifact's operating-system argv")
        self.login("beta")
        self._assert_keychain("alpha", True)
        self._assert_keychain("beta", True)
        beta_metadata = self._metadata_path("beta").read_bytes()
        beta_metadata_digest = hashlib.sha256(beta_metadata).hexdigest()
        self._assert_neighbor_accounts()

        # Wrong-tenant selection must refuse before it can access or alter a
        # neighboring native account.
        wrong = self._cli("auth", "whoami", "--provider", self.provider_url, "--tenant", "alpha",
                          "--device", "device-beta", "--json")
        self._command_result("alpha", "cross-tenant whoami", wrong, False)
        self._assert_neighbor_accounts()

        time.sleep(3)
        refreshed = self._cli(*self._identity_args("alpha"))
        identity = self._command_result("alpha", "expired-token whoami refresh", refreshed, True)
        if identity.get("tenant_id") != "alpha" or identity.get("device_id") != "device-alpha":
            raise QualificationError("refresh returned an identity outside alpha")
        state = self._admin("state")
        alpha_state = state.get("alpha", {})
        if alpha_state.get("refreshes") != 1:
            raise QualificationError("expired-token whoami did not rotate exactly one refresh credential")
        stale = self._admin("probe-old-access", {"tenant": "alpha"})
        if stale.get("rejected") is not True:
            raise QualificationError("synthetic provider accepted a superseded access credential")
        state = self._admin("state")
        alpha_state = state.get("alpha", {})
        self.observations["alpha_refreshes"] = alpha_state["refreshes"]
        self.observations["alpha_old_access_denied_by_provider"] = alpha_state.get("old_access_rejected") == 1

        alpha_logout = self.logout("alpha", True, expected_remote=True)
        alpha_after_logout = self._admin("state").get("alpha", {})
        if alpha_after_logout.get("revokes") != 1 or not alpha_after_logout.get("revoked"):
            raise QualificationError("synthetic provider did not revoke the refreshed alpha identity")
        self.observations["alpha_logout"] = {"LocalRemoved": alpha_logout.get("LocalRemoved"), "RemoteRevoked": alpha_logout.get("RemoteRevoked")}
        self._assert_keychain("beta", True)
        if hashlib.sha256(self._metadata_path("beta").read_bytes()).hexdigest() != beta_metadata_digest:
            raise QualificationError("alpha logout changed the neighboring beta metadata")
        beta_identity = self._cli(*self._identity_args("beta"))
        beta_value = self._command_result("beta", "neighbor tenant whoami", beta_identity, True)
        if beta_value.get("tenant_id") != "beta":
            raise QualificationError("alpha logout changed the neighboring beta identity")
        self._admin("state")

        if (self.temp / "work" / ".dacli").exists():
            raise QualificationError("qualification created repository governance state")

        self.login("revoked")
        self._admin("revoke-tenant", {"tenant": "revoked"})
        denied = self._cli(*self._identity_args("revoked"))
        self._command_result("revoked", "provider-revoked whoami", denied, False)
        self.observations["revoked_access_denied"] = True

        # The missing/corrupt metadata cases must still remove only the exact
        # native credential selected by provider, tenant and device.
        for tenant, mutation in (("missing", "unlink"), ("corrupt", "corrupt")):
            self.login(tenant)
            metadata = self._metadata_path(tenant)
            if mutation == "unlink":
                metadata.unlink()
            else:
                metadata.write_bytes(b'{"schema_version":broken}\n')
            self.logout(tenant, True, expected_remote=True)
            self.observations[tenant + "_metadata_logout"] = "removed exact credential and metadata"

        # Offline logout must remove local state and report the uncertain remote
        # result; the fixture provider is stopped before invoking the artifact.
        self.login("offline")
        assert self.provider is not None
        self.provider.terminate()
        self.provider.wait(timeout=5)
        offline = self.logout("offline", False, expected_remote=False)
        if offline.get("State") != "remote_revoke_unknown":
            raise QualificationError("offline logout did not report remote_revoke_unknown")
        self.observations["offline_logout"] = {"State": offline.get("State"), "LocalRemoved": offline.get("LocalRemoved"), "RemoteRevoked": offline.get("RemoteRevoked")}

        self.logout("beta", False, expected_remote=False)
        self._assert_neighbor_accounts()
        self._scan()

    def _metadata_path(self, tenant: str) -> Path:
        assert self.profile is not None
        return self.profile / (account_digest(self.provider_url, tenant, "device-" + tenant) + ".json")

    def cleanup(self) -> bool:
        cleanup_ok = True
        process_uncertain = False
        try:
            # First ask the artifact to remove remaining exact selections
            # while the provider is available to revoke them.
            if self.provider_url and self.env:
                for tenant in TENANTS:
                    account = account_digest(self.provider_url, tenant, "device-" + tenant)
                    if account not in self.present_accounts and account not in self.uncertain_accounts:
                        continue
                    try:
                        result = self._cli(*self._identity_args(tenant, "logout"), timeout=10)
                        value = json_output(result, "cleanup logout")
                        if value.get("LocalRemoved") is True:
                            self.present_accounts.discard(account)
                            self.uncertain_accounts.discard(account)
                    except (QualificationError, OSError, subprocess.SubprocessError):
                        # Exact native deletion and absence verification below
                        # resolve any product-path failure without broadening
                        # the selected identity.
                        continue
        except Exception:
            cleanup_ok = False
        finally:
            for process in reversed(self.processes):
                try:
                    if process.poll() is None:
                        process.terminate()
                    try:
                        stdout, stderr = process.communicate(timeout=3)
                    except subprocess.TimeoutExpired:
                        process.kill()
                        try:
                            stdout, stderr = process.communicate(timeout=3)
                        except subprocess.TimeoutExpired:
                            cleanup_ok = False
                            process_uncertain = True
                            continue
                    self.outputs.extend((stdout, stderr))
                except (OSError, subprocess.SubprocessError):
                    cleanup_ok = False
            self.provider = None
            for tenant in TENANTS:
                account = account_digest(self.provider_url, tenant, "device-" + tenant) if self.provider_url else ""
                if account not in self.accounts or not self.env:
                    continue
                try:
                    args = ["/usr/bin/security", "delete-generic-password", "-s", SERVICE, "-a", account]
                    result = run_capture(args, self.env, timeout=10)
                    self.outputs.extend((result.stdout, result.stderr))
                    verify = run_capture(["/usr/bin/security", "find-generic-password", "-s", SERVICE, "-a", account], self.env, timeout=10)
                    self.outputs.extend((verify.stdout, verify.stderr))
                    if verify.returncode != 44:
                        cleanup_ok = False
                    if self.profile is not None:
                        self._metadata_path(tenant).unlink(missing_ok=True)
                except (OSError, QualificationError, subprocess.SubprocessError):
                    cleanup_ok = False
            try:
                self._scan()
            except QualificationError:
                cleanup_ok = False
            if process_uncertain:
                cleanup_ok = False
            else:
                try:
                    shutil.rmtree(self.temp)
                except OSError:
                    cleanup_ok = False
        return cleanup_ok

    def write_evidence(self, status: str, error: str | None = None) -> None:
        self.evidence_dir.mkdir(parents=True, exist_ok=True, mode=0o700)
        if self.evidence_dir.stat().st_mode & 0o077:
            raise QualificationError("evidence directory must be private (mode 0700 or stricter)")
        report = dict(self.observations)
        report["result"] = status
        if error:
            report["failure"] = error
        path = self.evidence_dir / f"device-identity-{self.arch}.json"
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
    qualification: Qualification | None = None
    outcome = "failed"
    error_text: str | None = None
    return_code = 1
    try:
        qualification = Qualification(args.binary, args.arch, args.evidence_dir)
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
        print(f"qualified extracted darwin_{args.arch} artifact against synthetic device identity lifecycle")
    return return_code


if __name__ == "__main__":
    raise SystemExit(main())
