# Native swarm planning and dispatch reservations

Core/589 implements scoped planning, atomic reservations, fresh manual dispatch
admission and separate-worker lease handoff. It does not launch coding agents,
run a bounded loop, acknowledge host execution or accept tasks. Core/590 supplies
the host bridge; core/593 supplies plugin swarm tools. The existing plugin catalog
continues to expose its existing session/lease/evidence/review operations.

## Authority and schema migration

`dacli native swarm upgrade --config <private-owner-file>` is an explicit
owner-only schema 6 → 7 migration. It preserves sessions, credentials, grants,
leases, operations, revisions, evidence, reviews and publication history. It
initializes the additional swarm maps in the same aggregate. Missing maps,
unknown schemas and attempts to downgrade are refused. Current services read
explicitly persisted schema 6 or 7, without upgrading on startup. Previous
schema-6-only binaries refuse schema 7, preventing lost reservation records.
Do not upgrade the actual workspace until all participating services use the
current binary. Preserve an owner-controlled backup and known live writer state;
there is no reset or destructive migration command.

`native swarm configure --config <private-owner-file> --policy-file <private-file>`
accepts a strict `sessions.SwarmPolicy` JSON record. Its capitalized keys are
`ProjectID`, `Revision`, `Harness`, `CapabilityDigest`, `Width`, `WIP`, `TTL`,
`Stopped`, `Roles` and `Placements`. `TTL` is nanoseconds and must be positive and
at most 24 hours. Roles declare `ID`, `Kind`, `Harness`, `Capacity`; placements
map canonical task IDs to `Checkout`, `ImplementKind`, `ReviewKind`. Wave width,
WIP and role capacities are positive owner limits. A placement must name a clean
existing Git checkout; no task rejection creates a checkout or worker identity.
The command validates canonical task/project bindings and observes repositories,
then atomically attests new bindings and stores the policy. Existing bindings
are preserved; a disabled or different-repository binding is refused rather
than silently reenabled. Policy revision reuse with altered content is refused.
A revision replaced by another revision cannot be restored under its old ID.

`native swarm controller add --config <private-owner-file> --project <project>
--connection <new-private-file> --credential <new-private-file>` enrolls a separate
controller with `read`, `swarm.plan`, `swarm.reserve`, `swarm.dispatch`. Scope is
limited to the selected project, configured task IDs and freshly declared claims.
It has no worker lease, owner enrollment, recovery, verification or acceptance
permission. Both output files are exclusively reserved before enrollment. After
issuance, failed private writes preserve the session and files and report the
safe enrollment ID/paths for owner reconciliation; no token enters diagnostics.

## Explicit manual native capability

The currently usable adapter requires a private owner capability file with
snake-case keys:

```json
{
  "schema_version": 1,
  "workspace_id": "canonical-workspace-id",
  "project_id": "core",
  "harness": "codex",
  "source": "owner-manual-native-handoff",
  "observed_at": "2026-10-03T03:00:00Z",
  "expires_at": "2026-10-03T03:30:00Z"
}
```

Set policy `CapabilityDigest` to `sha256:` plus the SHA-256 of the exact file
bytes. The attestation expires within one hour of observation; future timestamps
beyond 30 seconds, mismatched bindings, changed files, permissive permissions,
withdrawal and stale observations refuse admission. It is reread at planning,
reservation and admission/handoff boundaries, including the final boundary after
canonical/Git reads. This is explicit owner authority to perform a cooperative
manual native handoff. It does not claim a public Codex plugin spawn API,
unattended execution, authenticated child processes or hostile containment.
Absent trusted capability observation fails closed in the application service.
A bridge may supply its separately reviewed fresh capability adapter instead.

## Plan, reserve, admit and hand off

Use `native swarm plan --connection <controller-file> --capability-file <private-file>
--project <project> --json`. It returns a version-1 plan, digest/ID, controller,
policy revision, full canonical snapshot digest, authority fence and every task
verdict. Selection sorts MoSCoW priority then canonical task ID. It resolves the
complete workspace dependency graph before filtering the project. Malformed or
duplicate task records refuse planning; dependency faults remain explained.

Claims, task/checkout collisions, WIP, compatible implementation/review roles and
pinned harness are evaluated before consuming a slot. A colliding candidate is
rejected and later disjoint work backfills the wave. Missing compatible roles,
unsafe claims, unavailable checkouts and exhausted capacity remain explicit
verdicts. Scope never broadens automatically. Selected records include observed
base commit/tree, canonical repository/checkout and exact resources.

`native swarm reserve --connection ... --capability-file ... --request <private-file>
--json` accepts `OperationKey`, `Plan`, `ExpiresAt`. It freshly recomputes and
compares the entire plan while holding the workspace authority transaction.
The atomic file replacement stores dispatch IDs, controller identity, task,
repository, proposed checkout/base tree, policy, pinned roles/harness, resources,
fence and expiry. Lease acquisition and reservation share the overlap predicate
and authority lock; no parallel reservation ledger exists. Exact authenticated
operation replay returns its immutable historical wave. A stale plan, changed
payload or revoked controller refuses new admission.

`native swarm admit` accepts `DispatchID`, `ExpectedFence`. It freshly checks
controller lineage/scope, STOP, policy/capability, dependency readiness, declared
claims, role/WIP census and clean exact base commit/tree. Its result is permission
observation, not a record that a host launched an agent. The bridge must durably
record intent before an external host call and acknowledge/reconcile afterward.

The owner separately enrolls a worker through the existing `native enroll` flow.
That worker registers the exact checkout through its own MCP connection before
editing. `native swarm handoff` uses that worker's private connection and accepts
`OperationKey`, `DispatchID`, `AttemptID`, `ExpectedFence`. It freshly validates
both authorities, canonical readiness/capacity, capability and exact base, then
atomically converts the retained reservation to the worker lease with a newer
fence. The controller never borrows the worker credential. No unreserved gap or
second overlapping writer is admitted. Existing worker MCP services use the same
upgraded authority for revision/evidence and lease renewal/release.

Unreleased leases and retained reservations remain resource/capacity occupancy
after expiry or revocation: those facts do not prove a writer stopped. Known
native handoffs retain implementation/review role provenance. External unknown
writers conservatively occupy each configured role; canonical active tasks also
consume WIP/capacity. Failed or uncertain handoff keeps protection. Owner-only
fenced uncertain-writer recovery belongs to core/595; no controller escape is
introduced here.

Markdown writers do not share the authority lock. Repeated fresh observations
are cooperative readiness, not serializable task edits. A host completion or
manual operator narrative still cannot close or accept a task; exact trusted
checks, separate review and owner acceptance remain distinct later transitions.
