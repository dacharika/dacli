#!/usr/bin/env bash
# Exercise the extracted release binary through the supported device-auth flow.
# The provider is synthetic and loopback-only; Keychain receives one unique test
# identity which auth logout removes. No hidden CLI mode or credential fallback.
set -euo pipefail

binary=${1:?usage: qualify-darwin-release-artifact.sh <extracted-dacli> <amd64|arm64>}
target_arch=${2:?usage: qualify-darwin-release-artifact.sh <extracted-dacli> <amd64|arm64>}
binary="$(cd "$(dirname "$binary")" && pwd -P)/$(basename "$binary")"
case "$target_arch" in amd64|arm64) ;; *) echo "invalid target architecture" >&2; exit 2 ;; esac

host_arch=$(uname -m)
runner_kind=native
if [ "$host_arch" = arm64 ] && [ "$target_arch" = amd64 ]; then
  if ! arch -x86_64 /usr/bin/true >/dev/null 2>&1; then
    echo "unverified: macOS amd64 artifact execution unavailable (Rosetta is not installed)"
    exit 77
  fi
  runner_kind=rosetta
elif [ "$host_arch" = x86_64 ] && [ "$target_arch" = arm64 ]; then
  echo "unverified: macOS arm64 artifact execution unavailable on Intel host"
  exit 77
fi

tmp=$(mktemp -d /private/tmp/dacli-artifact-qualification.XXXXXX)
trap 'rm -rf "$tmp"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
real_home=${HOME:?HOME is required to locate the existing macOS Keychains directory}
caller_gocache=$(go env GOCACHE)
caller_gopath=$(go env GOPATH)
caller_gomodcache=$(go env GOMODCACHE)
mkdir -p "$tmp/home/Library" "$tmp/config" "$tmp/work"
if [ ! -d "$real_home/Library/Keychains" ]; then
  echo "unverified: existing macOS Keychains directory is unavailable"
  exit 77
fi
# Isolate the device profile while letting Security.framework resolve its
# normal default keychain. Only the generated account digest is queried/changed.
ln -s "$real_home/Library/Keychains" "$tmp/home/Library/Keychains"
profile_dir="$tmp/home/Library/Application Support/dacli/device-identity"
export HOME="$tmp/home" XDG_CONFIG_HOME="$tmp/config"
export GOCACHE="$caller_gocache" GOPATH="$caller_gopath" GOMODCACHE="$caller_gomodcache"
provider_pid=
active_cli_pid=
active_security_pid=
cleanup_process_uncertain=false
login_succeeded=false
credential_removed=false
provider_url=

if [ "$runner_kind" = rosetta ]; then
  run_cli() { arch -x86_64 "$binary" "$@"; }
else
  run_cli() { "$binary" "$@"; }
fi
wait_owned_process() {
  pid=$1
  max_wait=$2
  elapsed=0
  while [ "$elapsed" -lt "$max_wait" ] && owned_pid_alive "$pid"; do
    sleep 1
    elapsed=$((elapsed + 1))
  done
  if owned_pid_alive "$pid"; then return 1; fi
  wait "$pid" 2>/dev/null || true
  return 0
}
owned_pid_alive() {
  pid=$1
  kill -0 "$pid" 2>/dev/null || return 1
  process_state=$(ps -o stat= -p "$pid" 2>/dev/null | tr -d '[:space:]')
  case "$process_state" in Z*) return 1 ;; esac
  return 0
}
stop_owned_process() {
  pid=$1
  kill -TERM "$pid" >/dev/null 2>&1 || true
  if wait_owned_process "$pid" 2; then return 0; fi
  kill -KILL "$pid" >/dev/null 2>&1 || true
  if wait_owned_process "$pid" 2; then return 0; fi
  # Do not block on a process still observed alive after TERM and KILL.
  return 1
}
run_cli_capture() {
  stdout_file=$1
  stderr_file=$2
  shift 2
  if [ "$runner_kind" = rosetta ]; then
    (exec arch -x86_64 "$binary" "$@") >"$stdout_file" 2>"$stderr_file" &
  else
    (exec "$binary" "$@") >"$stdout_file" 2>"$stderr_file" &
  fi
  active_cli_pid=$!
  timeout_seconds=${DACLI_QUALIFICATION_TIMEOUT_SECONDS:-120}
  case "$timeout_seconds" in ''|*[!0-9]*) echo "invalid DACLI_QUALIFICATION_TIMEOUT_SECONDS" >&2; return 2 ;; esac
  elapsed=0
  while [ "$elapsed" -lt "$timeout_seconds" ] && owned_pid_alive "$active_cli_pid"; do
    sleep 1
    elapsed=$((elapsed + 1))
    if owned_pid_alive "$active_cli_pid"; then
      echo "waiting for synthetic artifact auth operation"
    fi
  done
  if owned_pid_alive "$active_cli_pid"; then
    echo "synthetic artifact auth operation timed out after ${timeout_seconds}s" >&2
    if stop_owned_process "$active_cli_pid"; then active_cli_pid=; else cleanup_process_uncertain=true; fi
    return 124
  fi
  if wait "$active_cli_pid"; then cli_status=0; else cli_status=$?; fi
  active_cli_pid=
  return "$cli_status"
}
run_security_delete() {
  account=$1
  security delete-generic-password -s org.dacli.device-identity.v1 -a "$account" >"$tmp/security-delete.stdout" 2>"$tmp/security-delete.stderr" &
  active_security_pid=$!
  timeout_seconds=${DACLI_QUALIFICATION_TIMEOUT_SECONDS:-120}
  case "$timeout_seconds" in ''|*[!0-9]*) echo "invalid DACLI_QUALIFICATION_TIMEOUT_SECONDS" >&2; return 2 ;; esac
  elapsed=0
  while [ "$elapsed" -lt "$timeout_seconds" ] && owned_pid_alive "$active_security_pid"; do
    sleep 1
    elapsed=$((elapsed + 1))
  done
  if owned_pid_alive "$active_security_pid"; then
    echo "synthetic Keychain cleanup timed out after ${timeout_seconds}s" >&2
    if stop_owned_process "$active_security_pid"; then active_security_pid=; else cleanup_process_uncertain=true; fi
    return 124
  fi
  if wait "$active_security_pid"; then delete_status=0; else delete_status=$?; fi
  active_security_pid=
  return "$delete_status"
}
print_sanitized() {
  python3 - "$1" <<'PY'
from pathlib import Path
import sys
text = Path(sys.argv[1]).read_text(encoding="utf-8", errors="replace")
for marker in ("dacli-synthetic-access-marker", "dacli-synthetic-refresh-marker"):
    text = text.replace(marker, "[redacted synthetic marker]")
sys.stderr.write(text)
PY
}
write_cleanup_evidence() {
  evidence_dir=${DACLI_QUALIFICATION_EVIDENCE_DIR:-${TMPDIR:-/tmp}}
  if ! mkdir -p "$evidence_dir"; then
    echo "warning: cleanup could not be verified; unable to create a sanitized evidence directory" >&2
    return 1
  fi
  if ! cleanup_report=$(mktemp "$evidence_dir/dacli-keychain-cleanup-${target_arch}.XXXXXX"); then
    echo "warning: cleanup could not be verified; unable to create a sanitized evidence file" >&2
    return 1
  fi
  if ! cat >"$cleanup_report" <<EOF
architecture=$target_arch
logout_status=${logout_status:-not-attempted}
exact_synthetic_account_delete_status=${delete_status:-not-attempted}
credential_cleanup=$credential_removed
provider_process_cleanup=${provider_cleanup_status:-not-started}
process_cleanup_uncertain=${cleanup_process_uncertain:-false}
EOF
  then
    echo "warning: cleanup could not be verified; sanitized evidence file could not be written" >&2
    return 1
  fi
  echo "warning: qualification cleanup is not fully verified; sanitized status retained at $cleanup_report" >&2
  return 0
}
cleanup() {
  status=$?
  trap - EXIT INT TERM
  cleanup_failed=false
  if [ -n "$active_cli_pid" ]; then
    if stop_owned_process "$active_cli_pid"; then active_cli_pid=; else cleanup_process_uncertain=true; fi
  fi
  if [ -n "$active_security_pid" ]; then
    if stop_owned_process "$active_security_pid"; then active_security_pid=; else cleanup_process_uncertain=true; fi
  fi
  if [ "$login_succeeded" = true ] && [ "$credential_removed" != true ]; then
    if [ -z "$active_cli_pid" ] && [ -z "$active_security_pid" ]; then
      if run_cli_capture "$tmp/cleanup-logout.stdout" "$tmp/cleanup-logout.stderr" auth logout --provider "$provider_url" --tenant qualification-tenant --device qualification-device --json >/dev/null 2>&1; then
        logout_status=0
      else
        logout_status=$?
      fi
      if [ -n "$active_cli_pid" ] && stop_owned_process "$active_cli_pid"; then active_cli_pid=; fi
      if [ "$logout_status" -eq 0 ] || python3 - "$tmp/cleanup-logout.stdout" <<'PY' >/dev/null 2>&1
import json, sys
try:
    result = json.load(open(sys.argv[1], encoding="utf-8"))
except (OSError, ValueError):
    raise SystemExit(1)
raise SystemExit(0 if result.get("LocalRemoved") is True else 1)
PY
      then
        credential_removed=true
      fi
    else
      logout_status=not-run-process-still-active
      cleanup_process_uncertain=true
    fi
  fi
  if [ -n "$provider_url" ] && [ "$credential_removed" != true ]; then
    if [ -n "$active_cli_pid" ] || [ -n "$active_security_pid" ] || [ "$cleanup_process_uncertain" = true ]; then
      cleanup_failed=true
      delete_status=not-run-process-still-active
    else
      # This digest matches the product key format exactly and selects only
      # this synthetic provider/tenant/device identity.
      account=$(printf '%s\0%s\0%s' "$provider_url" qualification-tenant qualification-device | shasum -a 256 | awk '{print $1}')
      if run_security_delete "$account"; then
        delete_status=0
        credential_removed=true
      else
        delete_status=$?
        cleanup_failed=true
      fi
    fi
  fi
  if [ -n "$provider_pid" ]; then
    if ! stop_owned_process "$provider_pid"; then
      cleanup_failed=true
      provider_cleanup_status=unverified
      echo "warning: synthetic provider process did not stop within its cleanup deadline" >&2
    else
      provider_cleanup_status=stopped
    fi
    provider_pid=
  fi
  if [ "$cleanup_failed" = true ] || [ "$cleanup_process_uncertain" = true ]; then
    write_cleanup_evidence || true
  fi
  rm -rf "$tmp"
  if [ "$cleanup_failed" = true ] && [ "$status" -eq 0 ]; then status=1; fi
  if [ "$cleanup_process_uncertain" = true ] && [ "$status" -eq 0 ]; then status=1; fi
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

go build -o "$tmp/provider" ./scripts/keychain-qualification-provider.go
"$tmp/provider" >"$tmp/provider.out" 2>"$tmp/provider.err" &
provider_pid=$!
for _ in $(seq 1 100); do
  if [ -s "$tmp/provider.out" ]; then break; fi
  kill -0 "$provider_pid" 2>/dev/null || { echo "synthetic provider failed to start" >&2; exit 1; }
  sleep 0.05
done
provider_url=$(head -n 1 "$tmp/provider.out")
case "$provider_url" in http://127.0.0.1:*) ;; *) echo "synthetic provider did not bind loopback" >&2; exit 1 ;; esac

cd "$tmp/work"
echo "running synthetic auth login on extracted darwin_${target_arch} binary"
if ! run_cli_capture "$tmp/login.stdout" "$tmp/login.stderr" auth login --provider "$provider_url" --tenant qualification-tenant --name ReleaseQualification --json; then
  echo "extracted artifact auth login failed" >&2
  print_sanitized "$tmp/login.stderr"
  exit 1
fi
login_succeeded=true
echo "synthetic auth login completed"
if ! run_cli_capture "$tmp/whoami.stdout" "$tmp/whoami.stderr" auth whoami --provider "$provider_url" --tenant qualification-tenant --device qualification-device --json; then
  echo "extracted artifact auth whoami could not read the Keychain credential" >&2
  print_sanitized "$tmp/whoami.stderr"
  exit 1
fi
echo "synthetic auth whoami completed"
if ! run_cli_capture "$tmp/logout.stdout" "$tmp/logout.stderr" auth logout --provider "$provider_url" --tenant qualification-tenant --device qualification-device --json; then
  echo "extracted artifact auth logout could not remove the Keychain credential" >&2
  if python3 - "$tmp/logout.stdout" <<'PY' >/dev/null 2>&1
import json, sys
try:
    result = json.load(open(sys.argv[1], encoding="utf-8"))
except (OSError, ValueError):
    raise SystemExit(1)
raise SystemExit(0 if result.get("LocalRemoved") is True else 1)
PY
  then credential_removed=true; fi
  exit 1
fi
login_succeeded=false
credential_removed=true
echo "synthetic auth logout completed"

if ! python3 - "$tmp/logout.stdout" <<'PY'
import json, sys
result = json.load(open(sys.argv[1], encoding="utf-8"))
assert result["LocalRemoved"] is True
assert result["RemoteRevoked"] is True
PY
then
  echo "extracted artifact did not report successful local and synthetic remote cleanup" >&2
  exit 1
fi

# Synthetic secret markers must stay out of output, profile metadata and files.
for marker in dacli-synthetic-access-marker dacli-synthetic-refresh-marker; do
  if grep -R -F -q "$marker" "$tmp"/login.stdout "$tmp"/login.stderr "$tmp"/whoami.stdout "$tmp"/whoami.stderr "$tmp"/logout.stdout "$tmp"/logout.stderr "$profile_dir"; then
    echo "synthetic credential marker escaped into output or profile metadata" >&2
    exit 1
  else
    grep_status=$?
    [ "$grep_status" -eq 1 ] || { echo "could not scan outputs/profile metadata for synthetic credential markers (grep exit $grep_status)" >&2; exit 1; }
  fi
done
[ ! -e "$tmp/work/.dacli" ] || { echo "qualification created .dacli state" >&2; exit 1; }

if run_cli_capture "$tmp/missing.stdout" "$tmp/missing.stderr" auth whoami --provider "$provider_url" --tenant qualification-tenant --device qualification-device --json; then
  missing_status=0
else
  missing_status=$?
fi
[ "$missing_status" -eq 3 ] || { echo "post-logout whoami exit=$missing_status, want refusal code 3" >&2; exit 1; }
if grep -R -F -q dacli-synthetic "$tmp/missing.stdout" "$tmp/missing.stderr" "$profile_dir"; then
  echo "synthetic credential marker appeared in missing-identity failure" >&2
  exit 1
else
  grep_status=$?
  [ "$grep_status" -eq 1 ] || { echo "could not scan missing-identity outputs/profile metadata (grep exit $grep_status)" >&2; exit 1; }
fi

echo "qualified extracted darwin_${target_arch} artifact: Keychain put/get/delete, missing identity, no plaintext credential output"
