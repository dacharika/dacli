# Bounded tenant API boundary

**Status: tenant boundary and device/sync adapters are assembled in source;
live provider, full verification and hosted deployment are unverified.**

The control-plane HTTP service can enable tenant routes only when it is given
three explicit dependencies: an identity verifier, a tenant backend, and at
least 32 bytes of cursor-signing key material. Health and readiness do not
pretend these routes are active when those dependencies have not been wired.

## Identity and scope

The temporary Phase 1 verifier accepts a short-lived, HMAC-authenticated bearer
identity containing an opaque tenant, account, optional device, exact
membership version, current policy revision, and expiry. Domain separation
prevents an identity signature from being reused as a page-cursor signature.
The source now also supplies a device-session verifier selected by the hosted
composition. It reloads current device/session/membership state and uses only
server-verified identity for authority. The injectable HMAC adapter remains an
explicit development boundary, not a client login or bearer-minting interface.

Handlers use only the verified identity to construct `tenant.Scope`, actor, and
device. A body, query, route, or queued work item may select a resource inside
that scope but cannot supply or replace the tenant. The application layer
reloads membership authorization before repository or cache access and rejects
a stale policy revision before either one. Unknown, removed, stale, and
cross-tenant resources share one `resource_unavailable` response.

## Rate-limit boundary

Oversized requests are rejected before identity verification. Tenant routes
then consume an unauthenticated bucket keyed by a service-secret HMAC of the
direct network peer; `X-Forwarded-For` and other caller headers are ignored.
After successful verification, project and environment list traversal consumes
a separate bucket keyed only by tenant/account/device from `VerifiedIdentity`
and a closed purpose label. Mutations do not consume list capacity.

An exhausted bucket returns the same structured `rate_limited` 429 response
without tenant/resource detail and an integer `Retry-After` bounded to one
hour. Each bucket has a hard key count, idle expiry, deterministic LRU eviction,
integer monotonic refill accounting, and no background goroutine. Existing page
size, body size, request timeout, and cache bounds remain independently
enforced.

## Routes

| Method and path | Contract |
| --- | --- |
| `GET /v1/projects?limit=&cursor=` | Stable snapshot page, default 25 and maximum 100 |
| `POST /v1/projects` | Closed ID/name body; active version-one creation |
| `GET /v1/projects/{id}?version=` | Exact-version read; authorization is reloaded even on a cache hit |
| `PATCH /v1/projects/{id}` | Closed name/state/expected-version body; optimistic audit-atomic update |
| `GET /v1/projects/{id}/environments?limit=&cursor=` | Stable project-bound environment page |
| `POST /v1/projects/{id}/environments` | Closed ID/name/kind body; active version-one creation |
| `PATCH /v1/projects/{id}/environments/{environment}` | Closed lifecycle update with exact expected version |

PATCH bodies reject unknown fields, multiple JSON values, invalid versions, and
chunked bodies that exceed the global request limit. Structured errors never
include database, credential, or object-existence detail.

## Mutation attempt evidence

After identity verification, every project/environment mutation either commits
its success audit atomically with state or appends a separate non-success audit
after the failed state transaction rolls back. Closed outcomes distinguish
authorization and invalid-state refusals, unavailable resources, version
conflicts, and persistence failures without changing the public
missing/cross-tenant response. If that audit append itself fails, the handler
returns a retryable dependency error and does not claim durable refusal
evidence. The request ID is the correlation identity: an exact replay is
idempotent, while reuse for a different actor, device, action, result, or reason
fails closed.

For recovery, inspect the tenant audit row by request/correlation ID before a
retry. Re-submit only the identical authenticated action when its recorded
outcome and current resource version make that safe; generate a new request ID
for a changed action. An audit-correlation conflict is evidence of ambiguous or
reused identity and requires operator investigation, never an overwrite or an
automatic retry with altered input.

## Stable pagination

Migration `0006_stable_pagination.sql` assigns immutable, monotonically
increasing ordinals to projects and environments. The first query captures a
tenant-scoped high-water ordinal in the same read transaction. Later pages use
`after < ordinal <= snapshot`, ascending order, and one bounded lookahead row.
Inserts after the snapshot therefore appear only in a new traversal and cannot
create a duplicate or gap in the current traversal.

The cursor is an opaque base64url payload plus HMAC. Its signature binds tenant,
resource kind, optional parent project, snapshot, and last returned ordinal.
Cross-tenant, cross-kind, cross-parent, malformed, and modified cursors are
rejected before the repository is called.

## Cache and worker boundary

The bounded LRU cache key contains tenant, resource kind and ID, authenticated
subject, resource version, membership version, and policy revision. Every
dimension participates in equality. Authorization is always reloaded before a
cache lookup, so revocation takes effect on the next operation; mutations
invalidate that tenant's cached projects.

The worker dispatcher follows the same rule. Its verifier produces the
identity and scope, while an untrusted claimed tenant remains data only. Invalid
identity never reaches the processor, and the raw worker credential is excluded
from JSON.

## Device and signed-sync adapters

The device and sync routes are enabled only with explicit operator-selected
`hosted.device_oauth` dependencies and `hosted.sync_bindings`. Provider issuer,
device/token/identity endpoints, client ID, requested scope, approved
subject→tenant/account mappings and server public signing keys are non-secret
configuration. `client_secret_env` names the external secret; resolved secret
values are not emitted in safe configuration. Operators must apply migration11
explicitly; runtime startup neither migrates nor provisions membership/trust.
The application database role needs explicit auth-flow table privileges and
USAGE on the new delivery ordinal sequence. Its serialized allocation trigger
does not grant those permissions to a role.

| Method and path | Source contract |
| --- | --- |
| `POST /v1/auth/device` | Closed provider flow request and device public-key enrollment selection |
| `POST /v1/auth/token` | Device-code polling or exact refresh request; private bearer result |
| `GET /v1/auth/whoami` | Revalidated current session/device/membership and safe identity metadata |
| `POST /v1/auth/revoke` | Exact authenticated device-session revocation |
| `GET /v1/sync/binding` | Exact query workspace/tenant/project/environment/repository/device comparisons against approved server binding |
| `POST /v1/sync/exchange` | Closed schema1 route/version, at most 100 signed envelopes and bounded automatic signed proposal pull |

The binding response is `ProjectBindingResponse` from
`internal/cloudsync/http.go`: schema1, exact route, binding version, canonical
repository URL/default branch, supported schema range and trusted public keys.
Binding trusted keys contain only static project server keys matching configured
`DeviceOAuth.ServerSigningKeys`. Dynamically enrolled device keys verify envelope
signers only within their live project assignments; they never become client
server-trust anchors. Enrollment maps a provider-verified issuer/subject to an
explicit owner-approved account and its current membership, not caller identity.
Caller route IDs select only resources within verified device authority. A
binding is refused when project/environment/current membership/device access is
missing or changed; no caller creates trusted bindings or overwrites existing
local bindings. Local connect rejects ambiguous repositories and sync rechecks
the current repository before effects.

`ExchangeRequest` carries the exact route and binding version, immutable signed
envelopes, the local metadata cursor and a separate opaque `delivery_cursor`.
`ExchangeResponse` carries `acknowledged` entries with exact `event_id`,
`idempotency_key`, `sequence` and `outcome`, signed `inbound` envelopes and the
next opaque delivery cursor. Clients acknowledge only the contiguous prefix of
exact accepted/duplicate confirmations; an HTTP 200 is not an inferred ack-all.
Inbound signatures, tenant/project routes and closed payload schemas are checked
before proposal persistence. Delivery cursor advancement follows successful
local receive/ack persistence, making a failed restart safe to replay.

Device OAuth throttling uses OAuth-specific `slow_down` responses; transient
provider failures use `temporarily_unavailable`. The generic tenant API keeps its
existing control-plane JSON contract for rate-limit HTTP 429 responses.

The delivery cursor is HMAC-bound to the current route/device/account, binding
version and stable page bounds. It is distinct from the unsigned local metadata
cursor, which grants no server pagination authority. Migration11 serializes
producer delivery ordinal allocation until commit and prevents ordinal changes;
pull stays tenant/project scoped and rejects modified or cross-route cursors.
No received envelope applies a local canonical task, grants policy, launches an
agent or supplies acceptance/merge/deployment authority.

The assembled production source compiles. These are implemented source contracts,
not measured live-provider or hosted results. This integration wave ran no
behavioral verification; deployment,
provider consent and actual authenticated client/server flow proof remain pending.
