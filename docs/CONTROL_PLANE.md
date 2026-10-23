# Hosted control-plane implementation

**Status: development composition qualified locally; hosted product not shipped.**

The development skeleton shipped in source now includes authenticated API/worker
composition and the disposable PostgreSQL recovery proof described below.

The [`cloud/`](https://github.com/mlnomadpy/dacli/tree/main/cloud) boundary now
contains a runnable API lifecycle, background-worker lifecycle, strict typed
configuration, a transactional checksummed PostgreSQL migration runner, and a
tenant-scoped repository foundation. This is infrastructure for the Phase 1
plan, not a customer-ready SaaS claim. Device-code authentication and signed
metadata-sync client/hosted adapters are assembled in source, with native OS
credential storage and explicit operator-configured routes. The assembled
composition has local behavioral tests and an opt-in real PostgreSQL
qualification covering authenticated routes, durable worker replay and recovery.
The fixture does not qualify an external OAuth provider, a live synchronization
service or a deployment. A deployed tenant API, billing, approval service,
portfolio view, hosted GitHub App service and SLO remain pending.

## What exists

- one bounded HTTP process with health/readiness endpoints, request IDs,
  structured safe errors, request limits, timeouts, and graceful shutdown;
- separate bounded identity, tenant-list, and sync token buckets with
  privacy-preserving keys, deterministic LRU eviction, and no per-key
  goroutines;
- one cancellable worker process with immediate and periodic bounded cycles;
- configuration that resolves credentials from named environment variables,
  rejects unknown fields, and fails closed on unsafe production transport,
  default credentials, or incompatible contract versions;
- contiguous SQL migrations with SHA-256 ledger comparison and one transaction
  per migration plus ledger write;
- a loopback-only PostgreSQL 17.6 development topology with required credentials
  and an explicit named volume;
- a transport-independent tenant kernel with scoped versioned entities,
  deny-by-default role authorization, current-membership revalidation, and
  immutable digest-bound audit values;
- a PostgreSQL tenant schema with composite tenant keys, tenant-bearing foreign
  keys, forced row-level security, and database-enforced append-only audit
  events;
- canonical successful-mutation action identities binding scope, operation,
  target, optimistic versions, and before/after state, with explicit
  `succeeded`/`committed` evidence in the append-only audit stream;
- separately committed authenticated mutation-attempt evidence for
  authorization/invalid-state refusals, unavailable resources, optimistic
  conflicts, and persistence failures, with closed result/reason codes and
  correlation/action idempotency;
- an exact-schema repository boundary that binds the tenant into both the
  transaction-local RLS setting and every query, uses optimistic versions, and
  commits project mutations with their audit event atomically;
- revocable device-session authorization that stores only one-way credential
  digests, reloads current session/device/membership state for every protected
  operation, and rotates or revokes with optimistic transactional audit;
- a provider-neutral tenant-resource workflow for digest-only one-time
  invitations, project/environment lifecycle, and scoped account assignments;
  exact permissions are checked against current membership before persistence,
  while every mutation is optimistic and audit-atomic;
- injectable HTTP and worker tenant boundaries that derive scope only from a
  short-lived verified identity, reject stale policy revisions, use signed
  tenant/parent-bound snapshot cursors, and isolate a bounded versioned cache;
- a durable signed-envelope inbox/outbox boundary that commits permanent
  idempotency identity, expiring payload, cursor, and redacted audit before
  acknowledgement, plus leased bounded retry and queryable dead letters;
- manually dispatched CI coverage with explicit floors for both process entrypoints,
  configuration, migrations, service, tenant, and worker packages, plus an
  import-boundary test preventing the cloud service from coupling to local
  task-store or coding-agent execution internals.

The exact development commands and port, credential, volume, and migration
boundaries are in the
[`cloud` README](https://github.com/mlnomadpy/dacli/blob/main/cloud/README.md).

## What comes next

The program remains tracked by
[#446](https://github.com/mlnomadpy/dacli/issues/446). The ordered implementation
path begins with validating the assembled native device authentication and
metadata-only sync adapters, then signed role/policy publication, approval and budget
controls, the GitHub App service, and finally the cross-project operator view.
Each feature must satisfy
the [threat model](CONTROL_PLANE_THREAT_MODEL.md),
[privacy boundary](CONTROL_PLANE_PRIVACY.md), and public
[`controlplane/v1`](../contracts/controlplane/v1/README.md) contract before it
can be described as shipped.

Run `scripts/qualify-hosted-postgres.sh` from a clean checkout for the composed
service proof. It uses the cached immutable PostgreSQL 16.15 fixture image, no
external network, synthetic identities and disposable storage. Receipts bind the
source tree/diff, compiled test binary, actual PostgreSQL version and stage exits.
The proof covers database crash replay and a restore into a distinct empty
database; it does not qualify the separate PostgreSQL 17.6 recovery-smoke image.

The exact persistence, crash, retry, retention, and recovery contract is in the
[worker boundary](CONTROL_PLANE_WORKER.md). The versioned, executable
PostgreSQL restore, signing-key rotation, tenant-retention, and incident
procedure is in the [recovery runbook](CONTROL_PLANE_RECOVERY.md).

## Device authentication and local metadata sync source

The local commands are `auth login`, `auth logout`, `auth whoami`,
`project connect`, `project disconnect`, `project sync` and `project status`.
Authentication uses explicit provider/tenant/device selection and the native OS
credential store; unsupported stores refuse. Project sync reuses the signed v1
allowlist and immutable outbox/inbox rather than creating remote execution
permission. A local signer can queue offline, but authenticated server binding
and exact acknowledgment are required before delivery advances. Incoming signed
records remain proposals under existing local authority.

The [device identity guide](DEVICE_IDENTITY.md) describes exact selectors, native
storage, refresh/revocation, recovery without profile metadata and local
qualification boundaries.

The hosted source exposes device authorization, token exchange/refresh, whoami
and revocation plus exact sync binding and bounded exchange. Activation requires
explicit owner `hosted.device_oauth` configuration, approved subject mappings,
server public signing keys and `hosted.sync_bindings` for the exact local/remote
route. Client-secret configuration contains only the environment-variable name;
resolved values are excluded from safe config output. No user query or body
creates tenant authority, membership, device trust or a repository binding.

Migration `0011_device_auth.sql` adds tenant-isolated encrypted device flow state
and immutable serialized outbox delivery order. Run the existing explicit
operator migration workflow before activating a runtime that requires schema11;
startup does not migrate or provision accounts. Operators must also grant the
application database role the required auth-flow table privileges and sequence
USAGE for the new delivery ordinal sequence; trigger-based allocation is not an
automatic permission grant. Existing configured PostgreSQL
state, provider credentials and deployment remain operator responsibilities.
See [API wire contracts](CONTROL_PLANE_API.md) and [trust/undo](TRUST.md).
Source assembly is not a claim of live login, delivery, conformance or rollout.
