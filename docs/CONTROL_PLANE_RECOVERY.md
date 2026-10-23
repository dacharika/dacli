# Control-plane recovery and retention runbook v1

**Status:** executable provider-neutral contract for the PostgreSQL 17.6
development boundary. It is not a production backup service, deployment
automation, an SLO, a compliance certification, or legal retention advice.

This runbook covers one API and one worker using one PostgreSQL database. The
database is the consistency boundary: a valid recovery point contains the
schema ledger, tenant graph, audit records, device sessions, inbox identities
and payloads, stream cursors/replay floors, and outbox/dead-letter state from
the same snapshot. Restoring selected tables is unsupported.

## Authority, objectives, and evidence

Only the deployment maintainer may create or restore a backup, rotate a
signing key, purge retained data, or activate an incident credential. A second
maintainer reviews production restore and purge plans. Application agents may
request these actions and inspect redacted evidence; they do not receive
database, backup-store, or signing-key credentials.

The reference assumptions are an operator-selected recovery point objective
(RPO) and recovery time objective (RTO), recorded before deployment. This
repository promises neither value. The operator records the snapshot time,
PostgreSQL image/version, migration-ledger digest, encrypted artifact identity,
restore destination, verifier, start/end times, and smoke-test result. Never
put credentials, payload rows, or a raw dump in an issue or dacli event.

Run the disposable proof from a clean checkout with Docker available:

```bash
./scripts/control-plane-recovery-smoke.sh
```

It starts two ephemeral `postgres:17.6-bookworm` containers without publishing
ports, applies the supported migration catalog, seeds two tenants and durable
replay state, creates a transactionally consistent custom-format `pg_dump`,
restores it into an empty database, compares the exact migration ledger and
recovery-critical stream fields, and queries through a non-superuser RLS role.
The trap removes both containers and the temporary dump. The script refuses a
different image rather than silently certifying an unreviewed version.

## Composed service qualification

The separate `scripts/qualify-hosted-postgres.sh` driver exercises the API and
worker composition using the cached immutable PostgreSQL 16.15 image
`postgres@sha256:1a6ab3f5345eb6dbe04a1349529caabdb0ab09293a09590fad07b2246bfa4b54`.
Its receipt records the actual server/client versions; this is distinct from the
17.6 smoke image above. Neither driver pulls an image to hide a missing local
prerequisite.

The composed proof uses synthetic tenant/device identities, an application role
with neither superuser nor RLS bypass, private disposable storage and no external
network. It checks readiness with complete schema checksums and required forced
RLS, authenticated and cross-tenant refusals, committed inbox work surviving
process/database interruption, bounded worker replay, and a custom-format dump
restored into a separate empty database. Recovery compares the migration ledger
and durable tenant, stream, identity, audit, inbox/proposal and outbox state before
using the restored runtime. Replay must retain the original identity and produce
one proposal. It never resets attempts, fences or replay floors to make recovery
succeed.

Receipts bind the source commit/tree and any dirty diff, test-binary digest,
engine version and every stage exit, including fixture cleanup. The driver
removes only its own disposable container and temporary fixture files. Existing
Docker volumes, local workspaces, live databases and provider credentials are not
inputs. No source/transcript endpoint, startup migration or new authority contract
is introduced. External OAuth, production backup scheduling, encrypted storage,
provider credential rotation and deployment remain separately unqualified.

## Backup

1. Confirm PostgreSQL is 17.6, migrations are fully applied and checksums match
   this binary, the backup destination is encrypted, and available capacity
   exceeds the database plus WAL growth during the operation.
2. Record the highest acknowledged inbound/outbound cursor and active worker
   lease window. Quiescing is optional for `pg_dump`, which uses one consistent
   snapshot, but pausing ingress shortens post-restore reconciliation.
3. Run `pg_dump --format=custom --no-owner` with a dedicated backup credential.
   Capture the artifact digest and database/migration metadata separately.
   Back up external signing public-key metadata and secrets with their own
   secret-store procedure; they are intentionally not database dump payload.
4. Verify the dump with `pg_restore --list`, store it under immutable retention,
   and run the disposable restore proof on the intended PostgreSQL patch line.

If verification fails, do not replace the last known-good artifact. Revoke the
new artifact, retain failure evidence, repair the backup path, and rerun from a
fresh snapshot.

## Restore and rollback

1. Declare an incident/change window, block writes, stop the worker, select an
   artifact at or before the approved recovery point, and preserve the failed
   database read-only for investigation. Never restore over it.
2. Create an empty PostgreSQL 17.6 database and required external roles. Restore
   with `pg_restore --exit-on-error --no-owner`; any warning/error fails the run.
3. Before traffic, compare every migration version/name/checksum with the
   binary catalog. Refuse an unknown, missing, duplicated, or changed entry.
4. Compare row counts and integrity evidence, then use a non-owner,
   `NOBYPASSRLS` role to prove tenant A sees only A, tenant B sees only B, and an
   absent `dacli.tenant_id` sees no tenant rows.
5. Compare, without modifying, every stream's `last_sequence`, `replay_floor`,
   schema-version range; retained event/idempotency/producer-sequence identities;
   tenant mutation and envelope audit counts/digests; optimistic resource
   versions; and outbox state/attempt/lease/dead-letter evidence.
6. Expire stale leases by time—never by resetting attempts—then reconcile each
   producer from its restored cursor. Investigate gaps and dead letters before
   reopening writes. Monitor refusal, duplicate, replay, and delivery rates.

Rollback means routing back to the preserved pre-restore database after making
the failed target read-only. Never merge two independently writable recovery
targets. If either accepted traffic, reconcile immutable identities and obtain
an explicit operator decision before selecting one authoritative timeline.

## Signing-key rotation

Keys are tenant/project scoped and selected by immutable `key_id`. Rotation is
additive:

1. Generate a new Ed25519 key in the external secret store; publish its public
   key under a new ID and verify every consumer can resolve it.
2. Keep the prior public key active during a measured overlap covering offline
   producers and the maximum supported replay/retention window. Switch producers
   to the new key only after consumer readiness is evidenced.
3. Accept retained envelopes under either active key. A valid signature never
   bypasses tenant/project routing, schema-version, idempotency, replay-floor,
   sequence, or downgrade checks.
4. Revoke an old key only after no eligible retained envelope depends on it.
   Unknown or revoked IDs produce the closed tampered outcome. Never rewrite or
   resign stored envelopes.

For suspected private-key compromise, stop that producer, revoke the key,
preserve the public key and affected envelope/audit evidence in restricted
incident storage, issue a new ID, inspect all events since the last known-good
use, and advance replay policy only through an explicit operator decision.
`TestSigningKeyRotationPreservesVerificationAndDowngradeRefusal` is the
executable overlap/revocation fixture.

## Tenant lifecycle, deletion, and retention

Suspension and archive are reversible state transitions and are the default.
They revoke active access while preserving versions, relationships, audit, and
transport evidence. An active-database purge is a separate destructive,
two-maintainer operation after export/hold checks and dependency inventory.

Before purge, stop tenant ingress and delivery, revoke sessions and provider
credentials, drain or explicitly dead-letter the outbox, retain queryable
dead-letter metadata, and preserve append-only tenant/envelope audit under the
approved policy. Payload cleanup may delete expired inbox payload rows only;
it must not delete event IDs, idempotency keys, producer sequences, cursors, or
replay floors. A database purge does not claim immediate erasure from backups.
Backup copies expire through the independently configured immutable backup
retention schedule, including legal/security holds and deletion evidence.

## Incident matrix

| Incident | Contain | Recover and verify |
| --- | --- | --- |
| Service credential leaked | Revoke at the issuer, stop affected adapter, preserve redacted authentication logs | Issue a new credential, restart only after denial of the old one and tenant-bound access tests |
| Device credential leaked | Revoke the session/device and affected memberships; do not log the secret | Rotate with a new opaque credential, prove old/replayed/stale credentials are indistinguishable denials |
| Signing key leaked | Stop producer, revoke key ID, preserve restricted evidence | Follow emergency rotation; inspect event identities/sequences and refuse unknown/revoked signatures |
| Tenant-isolation suspected | Freeze writes and worker delivery for affected scope; preserve database and audit evidence | Test API/repository/job/cache/RLS paths using opaque IDs; restore/reroute only after the boundary is explained |
| Migration failed | Stop rollout; preserve logs and the prior database; never edit the migration ledger | Roll back to the untouched database or repair forward with a new migration; rerun checksum and two-tenant RLS proof |
| Poisoned/dead-letter queue | Stop the scoped producer/consumer; retain envelope identity and redacted error code | Repair cause, redrive with original identity/signature under bounded attempts; never reset attempts or rewrite payload |
| Cursor lost or corrupt | Stop acknowledgements for the stream and preserve both peers' state | Restore the consistent snapshot, compare durable identities, and reconcile from the lowest trusted cursor; never set floors to zero |

Production deployments still need encrypted scheduled backups, immutable
off-site retention, monitored restore drills, secret-manager rotation,
deployment composition, and organization-specific incident ownership. Their
absence must remain visible in release and deployment decisions.
