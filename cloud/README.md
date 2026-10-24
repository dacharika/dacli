# Composed control-plane service

This directory is the Phase 1 reference service boundary decided in
[ADR 0001](../docs/decisions/0001-control-plane-boundary.md). The standalone API
and worker compose the PostgreSQL tenant repository, bounded tenant routes,
envelope inbox/outbox processing, signed hosted role catalogs, and the worker
lifecycle. Device OAuth routes
are enabled when explicit provider configuration is supplied; configured sync
bindings enable the sync routes. Billing, a GitHub service, a general hosted
queue, and remote execution are not included. PostgreSQL access uses the
repository's linked `lib/pq` driver, while credentials, tenant configuration,
OAuth endpoints and keys remain explicit deployment inputs.

The service source is implemented, but deployment and support qualification are
tracked separately. Run `scripts/qualify-hosted-postgres.sh` for the opt-in
composition and recovery proof on the cached immutable PostgreSQL 16.15 fixture.
It exercises restricted-role readiness, authenticated tenant/device routes,
signed role import/export, exact pin inheritance and tenant/member refusals,
durable worker crash replay and a whole-database dump/restore into a separate
empty fixture database. Receipts identify the actual engine, source, compiled
test binary and stage exits. It does not exercise external OAuth, Keychain or
production infrastructure, and does not qualify the separate PostgreSQL 17.6
smoke image in the recovery runbook. CI remains manually dispatched.

Neither process imports dacli's local workspace, task store, or execution
packages. The stable client/server boundary remains
[`contracts/controlplane/v1`](../contracts/controlplane/v1/README.md).

## Local topology

The development topology is deliberately small and explicit:

| Boundary | Binding | Credentials | Persistence |
| --- | --- | --- | --- |
| API | `127.0.0.1:8080` | service secret via environment | PostgreSQL |
| PostgreSQL 17.6 | `127.0.0.1:55432` | required environment value | named Docker volume |
| Worker | no network listener | same environment references | PostgreSQL |

Start PostgreSQL with a non-default local password:

```bash
export DACLI_CLOUD_POSTGRES_PASSWORD="$(openssl rand -hex 24)"
docker compose -f cloud/compose.yaml up -d
export DACLI_CLOUD_DATABASE_URL="postgres://dacli_control_plane:${DACLI_CLOUD_POSTGRES_PASSWORD}@127.0.0.1:55432/dacli_control_plane?sslmode=disable"
export DACLI_CLOUD_SERVICE_SECRET="$(openssl rand -hex 32)"
go run ./cloud/cmd/api --config cloud/config.development.json
```

Run the worker in another terminal with the same environment:

```bash
go run ./cloud/cmd/worker --config cloud/config.development.json
```

`sslmode=disable` is permitted only for this loopback development topology.
Production configuration requires an HTTPS public URL, a non-default service
secret of at least 32 bytes, a PostgreSQL URL that does not disable TLS, exact
contract version 1, and no unknown configuration fields.

## Bounded rate limits

The strict `rate_limits` object configures three independent token buckets:
`identity` for unauthenticated verification, `tenant_read` for authenticated
list traversal, and `sync` for authenticated envelope ingestion/delivery. Each
requires a capacity, refill interval, hard key bound, and idle TTL. The shared
implementation is a mutex-protected O(1) LRU with no goroutine per key.
Each elapsed refill interval restores one token up to the configured capacity.

Pre-authentication keys are HMAC digests of the direct network peer; forwarded
headers are not trusted. Post-authentication keys use only verified
tenant/account/device or trusted scheduler scope plus a purpose label. Raw key
material is never logged. Production deployments behind a proxy must pass the
real peer through an explicitly trusted adapter instead of enabling generic
forwarded-header trust.

Rate-limited HTTP requests return `controlplane-error/v1` code `rate_limited`,
status 429, and a bounded integer `Retry-After`. Request-size rejection remains
outside and before authentication limiting; page and worker batch bounds remain
independent safety limits.

The compose file publishes PostgreSQL only on loopback, requires the password
instead of providing a committed default, and stores database state in the
named `dacli-control-plane-postgres` volume. Removing that volume destroys the
local database and is never performed by the application.

## Migrations

Migration files use contiguous names such as `0001_service_state.sql`. The
runner hashes every byte, compares the complete applied ledger before writing,
and applies each missing migration and its ledger record in one SQL
transaction. It refuses gaps, unknown catalog files, duplicate/applied
versions, an edited checksum, and a database version newer than the binary.

The repository links `github.com/lib/pq` as its PostgreSQL `database/sql`
driver. Deployments must still provide a correctly scoped PostgreSQL URL,
connection settings, TLS policy, and credential rotation. Startup checks the
database and complete migration ledger but never applies migrations. The
standalone service has no migration subcommand, so operators must provision the
complete schema and ledger through an explicit reviewed procedure before startup.

Migration `0003_tenant_repository.sql` creates the tenant identity graph. Every
tenant-owned table has a composite tenant key, tenant-bearing relationships,
and enabled plus forced row-level security. Policies compare against the
transaction-local `dacli.tenant_id`; an absent setting sees and changes no
tenant rows. The audit table rejects updates and deletes with a database
trigger.

Migration `0005_tenant_workflows.sql` adds invitation and project/environment
assignment records. It stores only invitation-token digests, carries tenant
identity through every relationship, and forces row-level security on all
three tables. Tenant resources have no hard-delete repository method.

Migration `0006_stable_pagination.sql` adds immutable identity ordinals and
tenant-leading indexes for project and environment snapshot pagination. New
inserts receive larger ordinals; an in-progress traversal remains bound to its
original high-water mark.

Migration `0007_envelope_worker.sql` adds tenant/project/producer streams,
permanent inbox identity and sequence rows, separately expiring payload rows, a
leased outbox with queryable dead letters, and an append-only redacted envelope
audit. Every table forces tenant RLS. See the
[durable worker boundary](../docs/CONTROL_PLANE_WORKER.md) for ordering, crash,
retry, retention, key-rotation, and recovery requirements.

Migration `0008_mutation_audit_identity.sql` adds a fixed-size exact-action
digest and explicit result/reason to every tenant mutation audit. New action
digests canonically bind tenant, operation, target kind/identity, optimistic
before/after versions, and immutable before/after state digests. Existing
append-only rows are transactionally backfilled from their strongest legacy
binding (`after_digest`) while the named trigger is disabled, then the trigger
is re-enabled before constraints are finalized; the migration test upgrades a
real legacy row and proves mutation is refused afterward.

Migration `0009_mutation_attempts.sql` closes the audit reason vocabulary.
Authenticated refusals, optimistic conflicts, and persistence failures append
the same tenant/actor/device/correlation/action identity in a separate tenant
transaction after the failed resource transaction has rolled back. Repeating
the same correlation, identity, action, result, and reason is idempotent;
reusing a correlation for different evidence fails closed and never rewrites
the append-only row.
The closed `legacy_success` reason remains valid only for immutable rows
backfilled by migration 0008; new domain constructors cannot emit it.

Migration `0012_role_registry.sql` adds immutable organization/team catalogs,
explicit publisher keys, exact signed release bytes, role-wide lifecycle
history, scope pin history and catalog mutation audit events. Each of its six
tables forces tenant RLS and preserves tenant-bearing foreign keys. The hosted
runtime requires the complete exact schema-12 ledger and refuses older or
altered schemas without applying migrations. See the
[hosted role catalog API](../docs/ROLE_REGISTRY.md#hosted-catalogs-and-inheritance)
for permissions, exact selectors, signed export boundaries and operator steps.
This hosted database migration does not upgrade the local native authority.

Backup and restore are whole-database operations. The executable
[recovery and retention runbook](../docs/CONTROL_PLANE_RECOVERY.md) pins the
supported PostgreSQL version, proves a restore into an empty database with two
tenant RLS checks, and defines key rotation, deletion, incident, and rollback
boundaries without claiming production infrastructure or an SLO.

## Tenant domain kernel

`internal/tenant` is the shared, transport-independent domain boundary. It
defines distinct opaque identifiers and versioned closed values for accounts,
organizations, teams, memberships, devices, projects, and environments. Every
tenant-owned validator requires an explicit organization scope.

Authorization reloads the current membership on every operation and requires
the caller's exact membership version. Removed, suspended, revoked, expired,
wrong-tenant, and stale-version memberships all return the same deny result.
The role matrix is closed and deny-by-default: owners receive every known
permission; administrators omit billing; managers govern teams, memberships,
projects, and environments; developers can use devices and write projects;
reviewers are read-only; billing is limited to organization/billing access;
auditors receive organization/project/audit read access. Unknown roles and
permissions never inherit access.

Audit records are pointer-free values binding tenant, actor/device, action,
target, optimistic before/after versions, fixed SHA-256 state and exact-action
values, explicit result/reason, and occurrence time. The canonical action
digest accepts only these closed fields. Credential-bearing records influence
it only through the one-way state digests already required by the domain.

## Tenant repository

`internal/tenantrepo` opens only against the exact migration version understood
by the binary. Every operation requires an explicit `tenant.Scope`, begins a
database transaction, sets the matching transaction-local RLS identity, and
also carries the tenant in each predicate. Cross-tenant and missing identifiers
therefore return the same result rather than disclosing existence.

Project writes use exact optimistic versions and append their immutable audit
event before the same transaction commits. A failed state write, audit append,
or commit leaves neither half visible. Membership reads use the same boundary
and implement `tenant.MembershipSource`, so authorization always reloads current
tenant state rather than trusting an HTTP claim or cache.

The repository also implements the provider-neutral authenticated-attempt
recorder. It never changes a resource row. If the separate audit append fails,
the API returns a dependency failure rather than claiming that a denial or
failure has durable evidence. Failures before identity verification never
enter a tenant audit stream.

## Tenant resource workflows

`internal/tenantresource` is the provider-neutral service boundary for
invitations, projects, environments, and their account assignments. Every
privileged operation reloads the actor's current membership and asks for the
one exact permission: membership management for issue/cancel/expire, project
management for project lifecycle and assignment, and environment management
for environment lifecycle and assignment. A denied or stale membership never
reaches persistence.

Invitation acceptance is the deliberate exception to membership authorization:
the invited account does not have a membership yet, so a minimum-strength
one-time credential and its bound opaque account identity authorize that one
transition. The raw credential is hashed before it reaches the repository.
Acceptance locks and advances the pending invitation, creates the membership,
and appends both audit facts in one transaction; expiry, cancellation, replay,
and concurrent acceptance fail closed. Projects, environments, and assignments
use exact version increments, and removal is represented by recoverable
archive/restore state transitions rather than deletion.

These records use closed structs and explicit SQL columns. They retain only
opaque IDs, names, roles, lifecycle, versions, and expiry. They cannot accept
source, diffs, prompts, transcripts, command output, paths, secrets,
environment values, or arbitrary metadata, matching the v1 privacy boundary.

## Bounded API, cache, and worker scope

`internal/tenantapi` composes current-membership authorization with the
repository. It refuses a stale policy revision before any database or cache
access. `internal/service` exposes bounded project and environment routes in the
standalone API composition. Device authorization, token, revocation and identity
routes are enabled only with explicit device OAuth provider and subject-binding
configuration; sync routes additionally require explicit route and key
bindings. Request bodies cannot carry tenant identity.

Project and environment lists use monotonic high-water snapshots, a maximum
page size of 100, and HMAC-bound opaque cursors. The bounded tenant cache keys
tenant, kind, resource, subject, resource/membership versions, and policy
revision; authorization is reloaded before cache access. The worker dispatcher
likewise derives scope only from its verified identity and ignores an untrusted
claimed tenant. See [the API boundary](../docs/CONTROL_PLANE_API.md).

## Durable signed envelopes

`internal/envelopeworker` authenticates and verifies v1 envelopes before a
single tenant-scoped inbox transaction checks duplicate/replay/sequence state
and commits identity, payload, cursor, and audit. Acknowledgement happens only
after commit. Its outbox claims a bounded batch under leases, preserves the
original signed envelope across capped exponential retries, honors
cancellation, and exposes scoped dead-letter metadata. Payload cleanup cannot
delete permanent idempotency or replay state.

The command in `cloud/cmd/worker` supplies the process lifecycle and repeatedly
processes only explicitly configured tenants. It handles inbox processing and,
when a tenant has a configured destination, bounded signed-envelope outbox
delivery. Deployments must supply tenant schedules, identities, signing keys,
destination URLs and credentials through strict configuration. The development
configuration is a local reference, not production qualification.

## Device sessions

`internal/devicesession` owns provider-neutral device authorization. Raw
credentials are accepted only at method boundaries and immediately reduced to
SHA-256 digests; records deliberately omit the digest from JSON. Every
authorization reloads the tenant-scoped session, device, and membership and
then uses a constant-time digest comparison. Expiry, suspension, membership
removal, device/session revocation, wrong-tenant use, and stale rotation are
indistinguishable denials.

Migration `0004_device_sessions.sql` stores only fixed-size credential digests,
uses composite tenant relationships and forced RLS, and uniquely scopes a
digest within its tenant. Rotation revokes the old record, creates the new
record, and appends both audit facts in one transaction; optimistic versions
make a replay or second revocation a conflict before side effects.
