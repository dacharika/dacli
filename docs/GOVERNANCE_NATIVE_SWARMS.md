# Native swarm and bounded-loop contract

Contract version: **1 (proposed)**. Delivery: core/588–596, with core/539,
548, 552 and 575 retained as native implementation work. This document defines
requirements; it does not announce an implemented swarm controller.

## Present implementation and boundary

DACLI has owner-enrolled external sessions, separately registered Git attempts,
fenced task/path leases, exact revision publication, local trusted verification,
independent-review authority and publication decisions. See
[GOVERNANCE_NATIVE_MCP.md](GOVERNANCE_NATIVE_MCP.md),
[GOVERNANCE_NATIVE_EVIDENCE.md](GOVERNANCE_NATIVE_EVIDENCE.md) and
[GOVERNANCE_NATIVE_REVIEW.md](GOVERNANCE_NATIVE_REVIEW.md). Transport exposure can
lag kernel capability; inspect the installed catalog rather than infer tools
from this contract. Existing cooperative review is credential separation, not
proof of distinct human operators or hostile-process containment.

Core/589 adds [native planning and atomic dispatch reservations](GOVERNANCE_NATIVE_SWARM_PLANNING.md), with explicit owner-attested manual native handoff admission and a separate-worker lease conversion. Core/590 adds [cooperative Codex dispatch](GOVERNANCE_NATIVE_DISPATCH_BRIDGE.md): durable intents, sequenced host correlation and atomic acknowledgement by a separately owner-provisioned worker. Unattended public spawn APIs, automatic child connection delivery, bounded loops and loop-driven canonical acceptance remain **unimplemented by these services**. Coding CLI launchers are not a fallback. DACLI owns
coordination and admission; hosts own agent execution, model selection within
owner policy, and native cancellation. A controller never runs `codex`,
`claude`, Antigravity or another coding CLI subprocess. Named verification
commands are a separate owner-configured runner capability, not agent execution.

The initial integration target is local Git. Push, GitHub writes, deployment,
tagging and release require separate recorded authority and adapters. An owner
request for local integration grants none of those external actions.

## Authority and least privilege

An owner configures the workspace, native host bridge, policy and bounded loop
limits. A controller has explicit project/task resources and only the new
coordination actions it needs: `swarm.plan`, `swarm.reserve`, `swarm.dispatch`,
`swarm.observe`, `swarm.reconcile`, `loop.start`, `loop.advance`, `loop.stop`,
`dispatch.cancel` and read. These names are proposed new actions, not current
kernel or MCP capabilities. Different deployments may split controller and
bridge principals; a controller cannot invent authenticated host observations.
Neither controller nor bridge receives owner bootstrap credentials.

Each worker has its own owner-authorized enrolled session, private connection
and task/path scope: attempt registration, lease acquire/renew/release,
revision publication and evidence submission. A controller does not become the
worker merely by sending a task label. Enrollment uses a privileged private
provisioning channel configured by the owner, with a bounded allowed scope,
expiry and audit record. Existing owner enrollment can implement manual
provisioning. Automatic child delivery requires separately implemented host
support; credentials must never appear in prompts, dispatch/status records or
normal tool output. Parent-subset delegation alone cannot create independently
root-enrolled review authority.

Runner and reviewer are distinct owner-enrolled principals with separately
selected private connections. Required review binds the correct worker family,
current policy, evidence and exact revision. Workers/controllers cannot grant
review authority, submit a reviewer decision using their own connection, or
accept their own tasks. Local integration and canonical acceptance use separate
owner-authorized operations, constrained to the selected target and reviewed
result. A shared MCP server connection is one identity even when used by many
native child agents. Host names, model names and child IDs are correlation data,
not credentials.

All current calls reauthenticate session, expiry, revocation and ancestors.
Owner-approved future coordination scopes must remain narrower than the
owner's authority. Tokens stay in private files outside repository and plugin
archives. OS users can still modify cooperative checkouts and private files;
fences protect participating consumers, not hostile filesystem writers.

## Versioned records

All records carry `schema_version: 1`, immutable ID, workspace/authority binding,
created time, authenticated actor and canonical request digest. Status views
include observation time and source. Unknown versions or fields fail closed.
IDs identify records, never authorize mutations. No record contains credentials,
arbitrary command strings or caller-supplied admission booleans.

| Record | Required fields and meaning |
|---|---|
| `SwarmPlan` | Project/task filter, canonical graph/selection digest, policy revision, capability snapshot ID, deterministic ordered candidate decisions, task/resource/role demands, target/base commit, limits and estimate provenance. A preview reserves nothing. |
| `Wave` | Plan ID/digest, bounded ordinal, admitted dispatch IDs, rejected/backfilled candidates and reasons, reservation accounting and current state. |
| `DispatchIntent` | Operation key, wave/task IDs, proposed canonical checkout locator/repository, exact resource set, controller/session binding, host capability snapshot, role/prompt specification digest, expiry, reservation fence, assigned worker session/attempt when acknowledged, and dispatch state. Prompt specifications contain work context, never authority. |
| `HostObservation` | Dispatch/host execution IDs, authenticated bridge identity, enrolled worker session and attempt if available, monotonically increasing per-dispatch sequence, host observed time, DACLI received time, source/assurance, lifecycle event, optional terminal outcome/artifact references and usage with provenance. |
| `LoopCheckpoint` | Loop/invocation IDs, policy and limits digest, transition revision, current phase, wave ordinal, dispatch operation IDs, attempt/lease/fence/revision/evidence/review references, target/base observations, pending integration/acceptance IDs, accounting, elapsed-time origin, STOP state, correction/no-progress counters and typed halt/next action. |
| `CapabilitySnapshot` | Host/bridge name and version, probe time/evidence digest, supported/unsupported/unknown values for dispatch, key reconciliation, child connection delivery, observations, cancellation acknowledgement, token controls and usage provenance. |

Plans use canonical task IDs; refs are resolved once with duplicate/malformed
records refused. Sort eligible tasks by owner-selected scheduling policy with
stable task-ID tie-breaks, and document that policy in the plan. Readiness uses
the complete dependency graph, project boundary, task state, WIP, capacity,
claims and owner policy. Colliding candidates are rejected before consuming a
worker slot; backfill from the next eligible task or report a smaller wave.
Never manufacture work when the ready frontier is empty.

## Minimal service and adapter interface

These are proposed service operations; core/593 will define strict MCP schemas
using the same services. Every mutation takes an operation key, expected
checkpoint/record revision and authenticated current session. Policy and limits
come from owner configuration, not request overrides.

| Service | Input and output |
|---|---|
| `PlanSwarm` | Scoped project/task selection → versioned read-only plan with every candidate verdict. |
| `ReserveWave` | Plan ID/digest → atomic admitted wave/dispatch reservations, or typed stale/conflict/preflight refusal. |
| `AdmitDispatch` | Dispatch ID/fence → current authorized dispatch envelope, after fresh preflight. |
| `AcknowledgeDispatch` | Dispatch ID/fence plus authenticated host execution correlation and independently provisioned worker binding → durable acknowledgement; scope and checkout/attempt identity validated server-side. |
| `ObserveDispatch` | Authenticated bridge event with dispatch ID/sequence → converged observation receipt. |
| `ReconcileDispatch` | Dispatch ID → authoritative host lookup result, freshness and next action; never implicit redispatch. |
| `CancelDispatch` | Dispatch ID/reason → durable authority withdrawal and host cancellation request; acknowledgement is a separate event. |
| `RecoverResources` | Owner-only exact workspace/task/dispatch/attempt and lease-or-reservation IDs, expected fence, operation key and terminal-evidence or explicit owner reconciliation decision → immutable fenced recovery receipt. Controller and worker scopes cannot call it. |
| `StartLoop` / `AdvanceLoop` | Owner-selected bounded policy ID / loop ID and checkpoint revision → durable checkpoint and pending action(s). Advance performs bounded controller work; it does not launch agent CLIs. |
| `LoopStatus` / `ReconcileLoop` / `StopLoop` | Loop ID → source/freshness/status, explicit recovery action or durable STOP receipt. |

The host adapter exposes capability negotiation, dispatch by stable operation
key, lookup by that key/host ID, observation and cancel. It cannot call back into
owner enrollment or acceptance unless separately authorized. The bridge records
an intention **before** calling the host. If the response is lost, reconciliation
must locate the original execution or produce `dispatch_unknown`; it never
blindly dispatches again. A host lacking stable-key idempotency must support a
reliable lookup or explicit manual reconciliation. Otherwise unattended dispatch
is unsupported. This is not a distributed exactly-once claim.

Core/589 must coordinate reservations and existing leases under the same
workspace authority lock/atomic replacement, with explicit schema migration.
A parallel reservation ledger that ignores existing worker leases is invalid.
Admission reobserves dependencies, scope, capability, STOP and policy before
host execution and before worker authority handoff. Conversion from dispatch
reservation to worker lease is one atomic transaction: no unreserved gap,
overlap or controller-held worker identity. The worker acknowledges and obtains
its lease before editing. Failed/uncertain handoff retains protected reservations.
Expired dispatch permission prohibits execution; expiry never proves a launched
worker stopped or automatically frees its resources. Owner reconciliation and
explicit release remain necessary when writer state is uncertain.

A revoked or expired worker cannot authenticate `ReleaseLease`; retaining its
reservation without an owner recovery path would prevent future progress.
Core/595 supplies `RecoverResources` through an owner-only local operation,
with current owner authentication and exact resource identities/fence checked
under the workspace authority transaction. Recovery requires authenticated
terminal host evidence for the same dispatch, or a separate explicit audited
owner reconciliation decision naming the unknown writer state and risk of
resource release. Expiry, revocation, heartbeat loss and cancellation request
alone cannot satisfy that gate. Recovery releases only matched retained
resources, advances the fencing generation and atomically records actor,
rationale, evidence/decision references and an immutable receipt. Exact
authenticated replay returns that receipt without releasing newly assigned
resources; altered operation payloads, stale fences or concurrent reassignment
are refused. Recovery permits cooperative resource reuse, not a claim that the
host stopped. Late observations remain auditable and never regain old writer
authority or replace a newer assignment.

Current `sessions` lease resources reject uppercase/non-ASCII path components,
symlinks, traversal and aliases. Mixed-case filenames require a conservatively
scoped lowercase parent directory (for example `docs`), explicitly enrolled by
the owner. Do not case-fold or broaden a refused scope client-side. Existing
Markdown task writers do not share the authority lock: fresh readiness is not
atomic against concurrent canonical edits. Until a shared-writer storage cutover,
this limitation must be exposed and rechecked at each admission boundary;
claims of serializable task edits would be false.

## State transitions, replay and observation

Dispatch states are `reserved → dispatch_requested → acknowledged → running →
completed|failed`. Observation may remain `unknown` or `disconnected` without
changing a known execution into terminal success. Completion before the running
notification can be admitted only if authenticated correlation and sequence
establish the same dispatch. Distinct terminal outcomes at the same sequence
are inconsistent records, not last-writer wins.

Cancellation is an orthogonal durable state: `none → requested → acknowledged
→ completed`, with `unsupported` and `unknown` explicit. Request immediately
prevents new authority/dispatch and coordinates worker-session revocation;
resource retention is independent of revocation. Acknowledgement means the host
accepted the cancellation request; only terminal host evidence means execution
ended. Unsupported cancellation never displays “killed”. A late completion is
retained as an observed outcome, but cannot resurrect withdrawn authority or
satisfy fresh acceptance automatically.

Duplicate event sequence with identical digest returns its historical receipt;
altered payload for that sequence is refused. Older events cannot regress
terminal state or overwrite newer usage. Future/implausible host timestamps
cannot refresh liveness: received time and configured skew/freshness limits
control the verdict. Silence or heartbeat expiry yields stale/unknown, never
completion. Only an authenticated bridge with documented provenance supplies
host evidence; worker self-reports remain self-reports.

Exact mutation replay reauthenticates and returns the original immutable receipt
after matching actor, operation and canonical digest. Altered payload/key reuse
is refused. Replay does not refresh admission, renew a lease, restart execution,
reset budgets or consume acceptance twice. Fresh eligibility is a separate
observation. Unknown schema, stale fence or changed request needs a new valid
operation/state transition, not retries around refusal.

## Bounded loop, recovery and accounting

Phase order is `reconcile → preflight → plan → reserve → dispatch → observe →
verify → review → integrate → accept → checkpoint`. Phases can wait or halt;
independent branches advance only within explicit WIP/concurrency bounds.
Integration and acceptance are separately authorized and do not occur because
an agent says it completed. Changed source/target invalidates admission for the
old tree; verify and independently review the resulting integration tree before
consuming the matching owner-authorized decision. Conflicting target updates
require serialized target admission and fresh comparison, not overwriting Git.

The owner must set finite maximum waves, elapsed time, concurrent workers,
correction rounds and no-progress cycles. Record start time durably; restart
cannot reset elapsed limits. STOP persists across restart and prevents every
new dispatch immediately, including a previously previewed plan. Existing
workers remain observed; cancellation is explicit and subject to host support.
A new invocation cannot clear STOP/counters implicitly. Any owner-authorized
reset is a separate auditable decision with scope/reason, never an unchanged
policy-refusal retry.

Before every wave or resumed side effect reconcile task/event state, authority,
leases, dispatch acknowledgement, Git source/target, trusted checks, review and
pending integration. Persist action intent before an external side effect and
its result afterward. Crash recovery at either boundary first queries the
recorded operation identity. Unknown external state halts that branch; it does
not count as successful progress or authorize another worker. Maintain durable
identifiers for all pending verification/review/integration work.

Progress means a durable authoritative transition or new verified evidence that
changes the next action. A verified wait observes a currently confirmed live
execution. Commentary, a lock, a stale receipt or polling unknown state does not
reset no-progress counters. Fresh transient observations can permit bounded
retry only under configured backoff and retry limits; unchanged policy refusal
is final for that invocation.

Typed halts: `stop_requested`, `limits_exhausted`, `no_schedulable_work`,
`policy_refusal`, `unsupported_host`, `manual_handoff_required`,
`dispatch_unknown`, `stale_observation`, `external_blocker`,
`inconsistent_record`, `no_progress`, `verification_failed`, `review_changes`,
`integration_conflict`, `transient_infrastructure`. Each supplies affected
record/task/host/check references, source/time, retryability, minimum remedy and
next action. Empty work ends normally; “unsupported” is not a successful swarm.

Accounting distinguishes configured limits, estimated reservations, measured
usage, unknown usage, advisory host controls and enforced limits. Report cycle
and rolling-window limit/spent/remaining/reset, live dispatch reservations,
required review plus integration/recovery reserve, unallocated capacity, units
and observed times. Preview and execution share one calculation; shrink/refuse
before dispatch if all required phases cannot fit. Settle/release reservations
idempotently from durable outcomes. Unknown spend is never zero or enforceable
remaining capacity. If an owner requires enforced token ceilings and the host
only offers advisory controls, preflight refuses. Routing explains estimate and
capacity choices without silently changing the pinned harness.

## Codex capability evidence and unsupported behavior

Native collaboration tools available to this chat demonstrate host-managed
agent execution; they do not establish a public plugin API for starting agents,
installing a separate child MCP connection, injecting private credentials,
querying durable dispatch keys or cancellation acknowledgement. Likewise,
`codex mcp-server` transport/catalog tests prove governance transport, not native
swarm execution. The bridge must probe the actual installed host/version and
record each capability separately. Do not invent an API from tool names.

Core/590 may support an explicit owner/manual native-host handoff when APIs are
absent; status must say `manual_handoff_required`. Core/594's actual Codex
end-to-end acceptance remains unsatisfied until two independently scoped
workers, dependent later wave, trusted runner, separate reviewer and local
integration are actually observed through the approved bridge. A fixture is
not actual Codex evidence. Universal means a common contract plus host adapters;
a second harness remains unverified until separately exercised.

## Backlog migration and prerequisite audit

The audit below uses canonical local task files and current source, not closure
inferred from historical GitHub references. The owner has applied the local canonical
amendments described below. Recorded migration evidence is
`/tmp/dacli588-backlog-migration.json`, with original task files and hashes
retained in `/tmp/dacli588-original-backlog`. These are local backlog changes,
not GitHub issue updates or implemented controller delivery. All migrated
acceptance criteria remain unchecked.

Canonical dependencies were independently reread: 539 → 588/589/590/575/595/542;
548 → 588/589/530; 552 → 548/539/588/589; 575 → 581/580/588/590;
530 → 588/589; 527 → 589. Authoritative Acceptance sections retain respectively
7, 7, 8, 4, 3 and 4 unchecked criteria. Core/575 also retains its imported criteria
section; those duplicate entries do not represent eight distinct guarantees.

| Task | Native amendment and dependency decision |
|---|---|
| core/539 (#859) | Retain all seven recovery guarantees. Replace spawn/wait launcher checkpoints with dispatch intent/ack/observation, verification, review, local integration and acceptance. Transfer every push/PR/CI crash case and precise external diagnosis to 596, preserving their unsatisfied guarantees. Depend on 588/589/590/575/595 plus reusable 542 reconciliation; remove 540 as a hard prerequisite so local-only recovery can complete independently. |
| core/548 (#867) | Retain all seven preflight/capacity guarantees. Replace `start --profile loop` comparison with shared native plan/reserve/live admission verdicts (retain `team assign` comparison where supported). Replace runtime launch feasibility with supported host dispatch/child connection/reviewer capability. Depend on 588/589 and the migrated harness-pinning regression 530. Required review still preflights before implementation; owner overrides remain reasoned, scoped and expiring. |
| core/552 (#877) | Retain all eight reservation/usage guarantees. Replace live-run/spawn/landing language with dispatch reservations/acknowledged execution/local integration, retaining configured external-adapter pending landings. Keep dependencies 548/539; add 589 for reservation integration. Never turn unknown provider usage into billing certainty. |
| core/575 (#1079) | Retain all four lifecycle guarantees. Bind observations to native dispatch and authenticated bridge with sequence/freshness and explicit cancel request/ack/terminal separation. Keep 581/580 as kernel foundations; add 588/590 for the bridge. Process monitoring remains only an explicitly identified legacy adapter. |
| core/595 | Owner-only fenced recovery depends on 588/589/575. Explicit terminal evidence or audited owner uncertainty/risk decision permits retained-resource release; controller cancellation never grants owner recovery authority. Core 539 depends on 595. |
| core/596 | Optional GitHub recovery depends on 539/540; it retains push/PR/CI/external merge crash cases, all five PR/CI diagnoses, changed blocker/trunk resume, freshness, required branch checks, exact current PR generation and merge/accept ordering. Local-only 591 does not depend on 596. |

Detailed obsolete prerequisites of core/539:

- **542 (#856), done:** keep and reuse canonical read-only delivery projection;
  extend native observations, do not replace canonical facts with a new ledger.
- **540 (#858), open:** preserve PR/CI classifications. It is an external-adapter
  dependency of 596, removed from 539 hard prerequisites. No GitHub guarantee is
  deleted:596 retains every external crash case, diagnosis, resume/status and
  mutation requirement. This grants no GitHub write authority.
- **519 (#814), open:** PID/transcript/killed.txt launcher premise no longer
  applies to native workers. Remove as a native recovery prerequisite; carry the
  unsatisfied “termination must be proven before releasing resources” guarantee
  into 575/539/595, with explicit owner recovery if termination is unknown.
  Retain historical legacy-record reconciliation separately.
- **520 (#813), done:** current PR-generation identity remains relevant to
  optional external recovery; historical landed PR never proves a newer slice
  integrated. Record preserved rule; no new native implementation dependency.
- **521 (#811), open:** transferred root legacy claim state must not gate native
  worker leases. Remove this prerequisite for native loops; preserve explicit
  task-scoped resources and non-force local commit/integration in 589/592.
  Historical CLI claim repair is not declared fixed by this migration.
- **527 (#838), open:** migrate collision/backfill regression into native
  planner 589 and keep the canonical task as tracked unsatisfied work with
  dependency 589. Remove the obsolete launcher implementation dependency from
  539; acceptance still requires no identity/checkout creation for rejected work.
- **529 (#841), done:** preserve merge/accept ordering, failure recovery and
  fresh-target verification in 592. A prior legacy fix does not prove native
  acceptance consumption implemented.

Core/548's prerequisite **530 (#845), open** originally targeted removed
start/runtime resolution and an obsolete orchestration package test. The owner
has migrated it to a shared native harness-pinning/capacity regression dependent
on core/588 and core/589, preserving preview/live reviewer consistency and
fail-closed missing compatible roles. Removal of the old command did not close it.

The delivery DAG remains acyclic: 588 establishes contracts; 589 reserves;
590 bridges; 539/548/552/575 implement recovery, preflight, accounting and
lifecycle;595 adds owner recovery before 539 completes;591 composes bounded loops; 592 connects separate native owner
integration/acceptance; 593 exposes plugin operations; 594 proves actual use.
Core/579 and 578 remain separate prerequisites of 592 for exact evidence and
publication authority. Foundational 581/580 can remain open for their broader
unsatisfied guarantees despite existing partial kernel delivery. Optional 596 depends on 539/540 and is not a prerequisite of local 591.

## Required delivery evidence

Documentation-only 588 needs a contract review and recorded canonical amendments;
it introduces no executable tests. Implementations need named semantic mutation
failures for reservation exclusion, fresh admission, harness pinning,
pre-cycle reconciliation, bounds, budgets and owner acceptance. Exercise
concurrent planners, stale policy/dependencies, revoked scope, dispatch response
loss, every checkpoint crash, unknown host state, cancel acknowledgement, late
completion, unknown usage and target changes. Real 594 evidence includes exact
source/target trees, supported host version/APIs, enrolled identities without
secrets, terminal commands/output digests, independent review and resulting
local merge. Run repository-required static/full tests on final merged source.
A passing narrow fixture or replay receipt never proves current broad completion.
