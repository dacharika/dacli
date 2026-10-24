#!/usr/bin/env bash
# Disposable real SQLStore/Runtime/API recovery qualification. It uses the
# already-cached PostgreSQL 16.15 image by digest, private container tmpfs and
# no network, host database, or persistent Docker volume.
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

# Require the immutable image already in the local cache; never pull it.
stage inspect-image docker image inspect "$image" --format '{{.Architecture}}'
image_arch=$(cat "$receipt_dir/inspect-image.log")
case "$image_arch" in amd64|arm64) ;; *) echo "unsupported image architecture: $image_arch" >&2; exit 2 ;; esac

# Use the caller's PATH-selected, pinned Go toolchain and its configured caches.
go_bin=$(command -v go)
go_path=$(go env GOPATH)
go_cache=$(go env GOCACHE)
go_mod_cache=$(go env GOMODCACHE)
(
  cd "$source_root"
  stage compile env -i PATH="$PATH" HOME="$HOME" GOPATH="$go_path" GOCACHE="$go_cache" GOMODCACHE="$go_mod_cache" GOPROXY=off GOTOOLCHAIN=local CGO_ENABLED=0 GOOS=linux GOARCH="$image_arch" "$go_bin" test -c -o "$work_dir/composition.test" ./cloud/internal/composition
)
if command -v sha256sum >/dev/null 2>&1; then
  stage test-binary-digest sha256sum "$work_dir/composition.test"
else
  stage test-binary-digest shasum -a 256 "$work_dir/composition.test"
fi

# Keep the container alive with inert PID 1. PostgreSQL itself is a child on a
# private tmpfs datadir, so pg_ctl immediate/restart retains only this fixture's
# bytes and needs no existing or named Docker volume.
container_created=1
stage create-container docker create --pull never --name "$container_name" --network none --tmpfs /qualification:rw,nosuid,size=1g --tmpfs /var/lib/postgresql/data:rw,nosuid,size=1m --entrypoint /bin/sh "$image" -c 'while :; do sleep 3600; done'
stage start-container docker start "$container_name"
stage prepare-fixture-dir docker exec --user root "$container_name" sh -c 'chown postgres:postgres /qualification && chmod 700 /qualification && mkdir -p /qualification/pgdata && chown postgres:postgres /qualification/pgdata && chmod 700 /qualification/pgdata'
stage prepare-socket-dir docker exec --user root "$container_name" sh -c 'mkdir -p /var/run/postgresql && chown postgres:postgres /var/run/postgresql'
stage copy-test docker cp "$work_dir/composition.test" "$container_name:/tmp/composition.test"
stage make-test-executable docker exec --user root "$container_name" chmod 755 /tmp/composition.test
COPYFILE_DISABLE=1 tar -cf "$work_dir/migrations.tar" -C "$source_root/cloud" migrations
stage copy-migrations docker exec --interactive "$container_name" tar -xf - -C /qualification < "$work_dir/migrations.tar"
stage initialize-postgres docker exec --user postgres "$container_name" initdb -D /qualification/pgdata --auth=trust --encoding=UTF8 --locale=C
stage start-postgres docker exec --user postgres "$container_name" pg_ctl -D /qualification/pgdata -o "-c listen_addresses= -c unix_socket_directories=/var/run/postgresql -c unix_socket_permissions=0777" -l /qualification/postgres.log start
stage postgres-ready docker exec --user postgres "$container_name" pg_isready -h /var/run/postgresql -U postgres -d postgres
stage postgres-version docker exec "$container_name" psql -h /var/run/postgresql -U postgres -d postgres -Atc 'SELECT version(); SHOW server_version_num;'
stage pg-dump-version docker exec "$container_name" pg_dump --version
if ! grep -Fq 'PostgreSQL 16.15' "$receipt_dir/postgres-version.log" || ! grep -Fq 'pg_dump (PostgreSQL) 16.15' "$receipt_dir/pg-dump-version.log"; then
  echo "cached fixture must report PostgreSQL server and pg_dump 16.15; see version receipts" >&2
  exit 1
fi

cat > "$work_dir/bootstrap.sql" <<'SQL'
CREATE ROLE qualification_owner LOGIN NOSUPERUSER NOBYPASSRLS;
CREATE ROLE qualification_app LOGIN NOSUPERUSER NOBYPASSRLS;
CREATE DATABASE qualification OWNER qualification_owner;
CREATE DATABASE qualification_sync OWNER qualification_owner;
CREATE DATABASE qualification_recovery OWNER qualification_owner;
CREATE DATABASE qualification_restored OWNER qualification_owner;
CREATE DATABASE qualification_roles OWNER qualification_owner;
SQL
stage bootstrap docker exec --interactive "$container_name" psql -h /var/run/postgresql -U postgres -d postgres -v ON_ERROR_STOP=1 < "$work_dir/bootstrap.sql"

stage test-catalog docker exec "$container_name" /tmp/composition.test -test.list .
for required_test in TestHostedPostgresComposition TestHostedPostgresProjectSync TestHostedPostgresReadinessRequiresOrganizationsRLS TestHostedPostgresRecovery TestHostedPostgresRoleRegistry; do
  if ! grep -qx "$required_test" "$receipt_dir/test-catalog.log"; then
    echo "assembled qualification binary is missing $required_test" >&2
    exit 1
  fi
done
stage qualify-composition docker exec --env DACLI_QUALIFY_HOSTED_POSTGRES=1 "$container_name" /tmp/composition.test -test.run '^TestHostedPostgresComposition$' -test.v -test.timeout 60s
stage qualify-project-sync docker exec --env DACLI_QUALIFY_HOSTED_POSTGRES=1 "$container_name" /tmp/composition.test -test.run '^TestHostedPostgresProjectSync$' -test.v -test.timeout 60s
stage qualify-readiness docker exec --env DACLI_QUALIFY_HOSTED_POSTGRES=1 "$container_name" /tmp/composition.test -test.run '^TestHostedPostgresReadinessRequiresOrganizationsRLS$' -test.v -test.timeout 60s
stage qualify-role-registry docker exec --env DACLI_QUALIFY_HOSTED_POSTGRES=1 "$container_name" /tmp/composition.test -test.run '^TestHostedPostgresRoleRegistry$' -test.v -test.timeout 120s

# Phase 1 commits both the authenticated inbox event and an outbox row, then
# commits a leased inbox claim and exits. Synthetic credentials stay in tmpfs.
stage recovery-seed docker exec --env DACLI_QUALIFY_HOSTED_POSTGRES=1 --env DACLI_QUALIFY_RECOVERY_PHASE=seed "$container_name" /tmp/composition.test -test.run '^TestHostedPostgresRecovery$' -test.v -test.timeout 60s

# Immediate mode aborts PostgreSQL without a clean checkpoint. Restart the
# child server against the same private tmpfs and let WAL recovery run.
stage postgres-stop-immediate docker exec --user postgres "$container_name" pg_ctl -D /qualification/pgdata stop -m immediate
stage postgres-restart docker exec --user postgres "$container_name" pg_ctl -D /qualification/pgdata -o "-c listen_addresses= -c unix_socket_directories=/var/run/postgresql -c unix_socket_permissions=0777" -l /qualification/postgres-restart.log start
stage postgres-recovered docker exec --user postgres "$container_name" pg_isready -h /var/run/postgresql -U postgres -d postgres
stage recovery-replay docker exec --env DACLI_QUALIFY_HOSTED_POSTGRES=1 --env DACLI_QUALIFY_RECOVERY_PHASE=recover "$container_name" /tmp/composition.test -test.run '^TestHostedPostgresRecovery$' -test.v -test.timeout 60s
stage recovery-snapshot docker exec --env DACLI_QUALIFY_HOSTED_POSTGRES=1 --env DACLI_QUALIFY_RECOVERY_PHASE=snapshot "$container_name" /tmp/composition.test -test.run '^TestHostedPostgresRecovery$' -test.v -test.timeout 60s

# pg_dump/pg_restore operate on complete databases with no table filtering.
stage pg-dump docker exec --user postgres "$container_name" pg_dump --format=custom --no-owner --host=/var/run/postgresql --username=postgres --dbname=qualification_recovery --file=/qualification/recovery.dump
stage pg-restore docker exec --user postgres "$container_name" pg_restore --exit-on-error --no-owner --host=/var/run/postgresql --username=postgres --dbname=qualification_restored /qualification/recovery.dump
stage restored-replay-and-rls docker exec --env DACLI_QUALIFY_HOSTED_POSTGRES=1 --env DACLI_QUALIFY_RECOVERY_PHASE=restored "$container_name" /tmp/composition.test -test.run '^TestHostedPostgresRecovery$' -test.v -test.timeout 60s
