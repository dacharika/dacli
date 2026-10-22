#!/usr/bin/env bash
# Disposable real SQLStore/Runtime/API/worker qualification. No external OAuth,
# Keychain, backup restore, or full crash recovery claim is made by this driver.
set -euo pipefail

if [[ $# -gt 1 ]]; then
  echo "usage: $0 [new-receipt-directory]" >&2
  exit 2
fi
source_root=$(cd "$(dirname "$0")/.." && pwd -P)
image='postgres@sha256:1a6ab3f5345eb6dbe04a1349529caabdb0ab09293a09590fad07b2246bfa4b54'
receipt_dir=${1:-$(mktemp -d "${TMPDIR:-/tmp}/dacli-hosted-pg-receipts.XXXXXX")}
mkdir -p "$receipt_dir"
receipt_dir=$(cd "$receipt_dir" && pwd -P)
if [[ -e "$receipt_dir/exits.jsonl" ]]; then
  echo "receipt directory already contains exits.jsonl; select a new directory" >&2
  exit 2
fi
work_dir=$(mktemp -d "${TMPDIR:-/tmp}/dacli-hosted-pg-work.XXXXXX")
container_name="dacli-hosted-pg-$(basename "$work_dir" | tr '[:upper:]' '[:lower:]')"
container_created=0
printf '%s\n' "$image" > "$receipt_dir/image.txt"
git -C "$source_root" rev-parse HEAD > "$receipt_dir/source-commit.txt"
git -C "$source_root" rev-parse 'HEAD^{tree}' > "$receipt_dir/source-tree.txt"
git -C "$source_root" diff --binary HEAD > "$receipt_dir/source-diff.patch"

stage() {
  local label=$1
  shift
  local result
  if "$@" > "$receipt_dir/$label.log" 2>&1; then result=0; else result=$?; fi
  cat "$receipt_dir/$label.log"
  printf '{"stage":"%s","exit_code":%d}\n' "$label" "$result" >> "$receipt_dir/exits.jsonl"
  return "$result"
}
cleanup() {
  local prior=$?
  trap - EXIT
  local cleanup_result=0
  if [[ $container_created -eq 1 ]]; then
    if ! stage cleanup docker rm --force "$container_name"; then cleanup_result=1; fi
  fi
  rm -rf "$work_dir"
  printf 'Qualification receipts: %s\n' "$receipt_dir"
  if [[ $prior -eq 0 && $cleanup_result -ne 0 ]]; then prior=1; fi
  exit "$prior"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# Require the previously qualified cached immutable image; never pull or start
# another service to hide an unavailable prerequisite.
stage inspect-image docker image inspect "$image" --format '{{.Architecture}}'
image_arch=$(cat "$receipt_dir/inspect-image.log")
case "$image_arch" in amd64|arm64) ;; *) echo "unsupported image architecture: $image_arch" >&2; exit 2 ;; esac
# Cross compile on the host, then execute in the network-disabled image. Neither
# the Go build nor docker exec receives the host's provider environment.
go_path=$(go env GOPATH)
go_cache=$(go env GOCACHE)
go_mod_cache=$(go env GOMODCACHE)
(
  cd "$source_root"
  stage compile env -i PATH="$PATH" HOME="$HOME" GOPATH="$go_path" GOCACHE="$go_cache" GOMODCACHE="$go_mod_cache" GOPROXY=off GOTOOLCHAIN=local CGO_ENABLED=0 GOOS=linux GOARCH="$image_arch" go test -c -o "$work_dir/composition.test" ./cloud/internal/composition
)
if command -v sha256sum >/dev/null 2>&1; then
  stage test-binary-digest sha256sum "$work_dir/composition.test"
else
  stage test-binary-digest shasum -a 256 "$work_dir/composition.test"
fi
# Empty listen_addresses prevents TCP even inside the isolated network namespace.
container_created=1
stage start docker run --pull never --detach --name "$container_name" --network none --tmpfs /var/lib/postgresql/data:rw --env POSTGRES_HOST_AUTH_METHOD=trust "$image" -c listen_addresses= -c unix_socket_directories=/var/run/postgresql
ready=0
for attempt in $(seq 1 30); do
  if stage "ready-$attempt" docker exec "$container_name" sh -c 'test "$(cat /proc/1/comm)" = postgres && pg_isready -h /var/run/postgresql -U postgres'; then ready=1; break; fi
  sleep 1
done
if [[ $ready -ne 1 ]]; then echo "disposable PostgreSQL did not become ready" >&2; exit 1; fi
# The migration owner owns only the fixture DB; application connections have no
# superuser or RLS bypass capability, and do not inherit the owner's role.
cat > "$work_dir/bootstrap.sql" <<'SQL'
CREATE ROLE qualification_owner LOGIN NOSUPERUSER NOBYPASSRLS;
CREATE ROLE qualification_app LOGIN NOSUPERUSER NOBYPASSRLS;
CREATE DATABASE qualification OWNER qualification_owner;
SQL
stage bootstrap docker exec --interactive "$container_name" psql -h /var/run/postgresql -U postgres -d postgres -v ON_ERROR_STOP=1 < "$work_dir/bootstrap.sql"
stage create-directory docker exec "$container_name" mkdir /qualification
stage copy-test docker cp "$work_dir/composition.test" "$container_name:/qualification/composition.test"
stage copy-migrations docker cp "$source_root/cloud/migrations" "$container_name:/qualification/migrations"
stage qualify docker exec --env PGHOST=/var/run/postgresql --env DACLI_QUALIFY_HOSTED_POSTGRES=1 "$container_name" /qualification/composition.test -test.run '^TestHostedPostgresComposition$' -test.v -test.timeout 60s
