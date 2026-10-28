# Native executor host contract (version 1)

This is a compatibility contract for a future authenticated host adapter. The
Go types live in `internal/governance/executor`; they introduce no CLI, MCP
surface, durable executor store, or automatic execution. `Backend` is a
capability boundary, not an execution service. `LifecycleAuthority` delegates
to the existing `sessions.Authority` lifecycle operations, which remain the
only durable observation/cancellation records.

## Contract and authentication

Every registration, dispatch, lookup, callback, and cancellation request uses
contract version 1 and the owner-provisioned exact binding: workspace, project,
canonical task, attempt, host, enrolled session, process instance, opaque
credential/profile identifier, and expiry/revocation state. An adapter must
authenticate the underlying connection and compare every binding field against
that enrollment on each operation. A host/session/instance/profile mismatch,
expiry, or revocation fails closed. The identifiers are never credentials.
Host names, model labels, execution IDs, and other correlation strings grant no
authority. Session/task/attempt scope, lease fences, reservations, and exact
checkout remain enforced by existing native authorities.

`AuthenticateWorker` is the package boundary that mints its opaque binding: it
checks the current worker credential through `sessions.Authority.Authenticate`
and registers/recovers the exact attempt through `RegisterAttempt`. It also
authorizes the exact workspace/task scope and derives project, checkout, and
repository from that authority-observed attempt. Dispatch compares those values
with the fresh reservation returned by the existing intent operation before
any host call.
`Coordinator.DispatchOnce` authenticates that worker again at effect time and
asks the existing authority for `RequestDispatchIntent`. Only its first durable
permit can reach `Backend.Dispatch`. A historical permit performs original-key
lookup only; a new key for an already-intended dispatch is refused by the
existing authority. Callback delivery uses a separately authenticated bridge
and `LifecycleAuthority` writes the existing lifecycle record. There is no
boolean supplied by an adapter that can substitute for these authority checks.
Backend registration is checked against the same authenticated binding before
the adapter receives it. `DispatchSpecDigest` binds the exact prompt, profile
reference, repository, checkout, and worker binding; changing any of them under
an original key fails before a repeated host call.
Credential-bearing helpers redact worker, controller, and bridge tokens from
JSON and Go formatting output.

DACLI session authentication proves which enrolled session supplied the
credential. It does not cryptographically prove an OS process, host binary, or
model. Host and process-instance labels come from the owner-provisioned private
enrollment and remain cooperative claims. Replacing a process requires fresh
owner qualification and a new binding; after restart, adapters with only
ephemeral identity state fail closed until that requalification. This contract
does not claim hostile-process containment.

Capability values are `supported`, `unsupported`, or `unqualified`; the last
means no reliable qualification evidence exists. Model availability and
private-profile handoff are reported separately. An adapter must not infer one
capability from another or promote an unqualified result to supported.
`UnqualifiedCapabilities(host)` supplies this fail-closed default for every
capability until an owner-qualified adapter provides evidence.

The owner provisions a bounded, worker-specific private MCP/process profile
before handoff. Requests carry only an opaque profile reference, worker session,
scope digest, and expiry. The owner alone provisions or revokes it. Token bytes,
owner bootstrap credentials, profile contents, and credential-bearing paths do
not enter prompts, logs, status output, or public receipts. A prompt contains
task context only. The current manual `native swarm handoff --connection
<private-file> --capability-file <private-file> --request <private-file> --json`
continues to use the enrolled worker's own private connection and atomically
converts its reservation through the existing authority operation.

## Controller-produced first-use dispatch

The in-process SDK adds a composition path for a bounded controller that has
already committed its original dispatch intent. Its generic
`RequestDispatchIntent` restriction remains enforced. Calling standalone
`DispatchOnce` after `StepController` does not transfer the controller's first-use
authority; neither its status JSON nor `HostActionAuthorized` grants that right.

Use `Authority.StepControllerForDriver` for the original fresh step. It returns
an opaque `ControllerHostDispatchPermit` only after that exact new intent is
durable. The permit identifies the newly inserted action, including when a
width-two status also contains an earlier `host_dispatch` sibling. Historical
steps, reconciliation, reserve-only steps and failed persistence return no
usable permit. A prior ordinary step cannot later manufacture its lost permit.
The reference's checkpoint sequence identifies the current durable checkpoint;
it is not inferred from the status's reservation-checkpoint summary.

`Coordinator.DispatchControllerOnce` compares the request with the independently
authenticated worker/profile, exact source and reservation, prompt digest,
actor route, fence and independently owner-pinned host-slot scope. It consumes
the sealed handoff with the current controller credential before registering
the selected backend or dispatching the original key. Consumption rechecks
current invocation/checkpoint/action, policy, STOP, expiry and admission.
No backend execution occurs under the authority lock.

Permit value copies share one atomic state. A consume attempt spends it even
when fresh admission refuses; it cannot be restored after an error. Serialization
and deserialization refuse, formatting reveals no permission material, and the
reference contains identifiers only. Consume immediately before another controller
step or reconciliation: an advanced sequence makes an old handoff stale.
A STOP after consumption concerns an already in-flight effect; the SDK cannot
make a remote host and local policy one atomic operation.

After a lost grant, response or client restart, use
`Coordinator.LookupControllerDispatch` with the exact original request and
binding. This path authenticates the scoped controller and retained intent,
then performs backend binding verification and original-key lookup only. It
never registers a backend, creates an intent or dispatches a replacement.
An unsupported/unknown old execution remains uncertain, including when its
worker can no longer authenticate. An old identity cannot restore worker
permissions. A lookup miss after a committed-but-undelivered grant is not
permission to launch: retain protection for explicit owner recovery.

These are internal Go capabilities, not new MCP tools, serialized grants,
provider adapters or a daemon. They add no durable authority fields, ledger or
schema. Controller storage still requires its existing schema 16 prerequisite;
optional bound host slots require schema 22. Current live schema 10 and its
manual workflow remain unchanged. `Backend` remains synchronous: a caller
context cannot forcibly stop an arbitrary blocking implementation. The finite
driver uses the separate cooperative context contract below; legacy authority
and filesystem overhead still prevents a hard end-to-end OS deadline claim.

#1239 supplies this handoff. #1240 adds the finite effect driver; #1241 rolling
admission and a scoped keeper, and #1242 shared trusted validation and honest
measurements follow it and may proceed in parallel. Parent #1218 retains its
full acceptance. Real activation still requires separately selected #1216/#1217
qualification and owner compatibility steps. Isolated fake-host tests do not
prove a live executor, private provider handoff or twelve-worker capacity.
CI remains manual only.

## Cooperative context composition

`ContextBackend` is an explicit contract rather than an implicit adapter for a
synchronous `Backend`. Capabilities, registration, binding proof, dispatch,
original-key lookup, observation read, cancellation and cancellation lookup all
receive the current bounded context. No background timer goroutine abandons a
blocking legacy host call. A selected backend must honor cancellation and retain
original-key outcome knowledge; capability labels alone are not qualification.

`ContextCoordinator.DispatchControllerOnce` retains the same authenticated
worker, independent expected-slot and opaque first-use checks. Its read-only
`LookupControllerDispatch` accepts the pinned historical `WorkerEnrollment` so a
reopened application can identify an uncertain original execution without
reauthenticating an expired worker, registering another attempt or restoring
launch permission. The original controller must remain current and authorized.

Host observation reads return a typed newer observation; the old `Observe`
method remains an ingestion sink. Actual lifecycle ingestion uses the independent
bridge and exact stored binding/source sequence. Cancellation lookup resolves a
lost original request without another host cancellation. Acknowledgement never
establishes terminal execution or releases writer protection.

The [finite native driver](native-controller-driver.md) composes these ports with
existing independently granted verification/review producers and explicit owner
gates. Its CLI creates no host/provider backend or review decision, and current
live schema10 is unchanged.

## Dispatch, lookup, observation, cancellation

Dispatch requires a stable operation key and exact binding. The host adapter
must durably bind that original key before an external side effect. `Lookup`
always queries that same key after a lost response, process restart, timeout,
or uncertain result. Exact retries return the original execution; changed
payloads for a key refuse. An unknown lookup is uncertainty, never permission to
launch again. A host without durable idempotency and original-key lookup is
unsupported for dispatch.

Callbacks carry a strictly increasing sequence, source time, state and evidence
digest, and are authenticated against the original binding. Duplicates may
return the same historical result; altered duplicates, older/reordered
callbacks, process replacement, terminal regression, and distinct terminal
outcomes at one sequence refuse. The existing native lifecycle authority is
the durable writer and independently assigns received time. An observation is
not proof of lease release or canonical completion.
The callback path authenticates the current bridge through the existing
lifecycle authority and checks its complete status binding. It intentionally
does not reauthenticate the worker: an exact authorized bridge may report a
late terminal observation after worker expiry or revocation, without restoring
worker authority. An expired or revoked bridge cannot report.

Cancellation request and host acknowledgement are separate from observed
terminal state. `acknowledged` means the host accepted the request; `unknown`
and `unsupported` remain explicit. Neither request nor acknowledgement means
the process stopped, and neither releases reservations or leases. Only the
existing owner-authorized recovery operation can reconcile retained resources.
Before withdrawing authority, the adapter re-reads lifecycle status and
requires the complete binding plus the worker attempt's exact task, session,
repository, and checkout. It verifies the backend's host and currently
registered process binding. Its typed cancellation request carries a fresh
cancellation key, the original dispatch key when known, and the complete
dispatch/intent/host-correlation/attempt/lease target. A mismatch refuses before
the durable cancellation request or host cancellation call.

## Storage and compatibility boundary

The base executor contract adds no separate persisted executor record or ledger.
The authority22 extension below extends existing authority records and readers
through its explicit owner migration. Before a future separate durable executor
record is introduced, an owner-approved upgrade must define its
schema/version, atomic writer, restart lookup, migration, and minimum readers in
`docs/COMPATIBILITY.md` and the native compatibility ledger. Dispatch
reservations, operation-key receipts, worker leases, host intents, lifecycle
observations/cancellation, and checkpoints remain owned by their existing
`sessions` operations. There is no parallel reservation, dispatch, lifecycle,
or checkpoint ledger and no implicit schema migration. A future adapter with
only in-memory binding state must requalify registration after every restart
and refuse dispatch/callbacks until it does.

The existing operations have these minimum native authority storage schemas:
swarm reservations require schema 7, dispatch intents require schema 8, and
lifecycle callbacks and cancellation require schema 9. The current manual
workflow against live schema 10 remains unchanged. These are prerequisites of
the existing native operations; this contract adds no executor storage schema,
reader, or writer. Any later persisted executor feature must retain the support
and migration prerequisites recorded in the [governance support matrix](governance-support-matrix.md)
and the [native sessions compatibility ledger](../internal/governance/sessions/compatibility.go),
along with `docs/COMPATIBILITY.md`.

The version-1 fake adapter tests exercise contract binding, profile limits,
original-key lookup after a simulated lost response, duplicate and changed-key
dispatch, monotonic callbacks, and unknown cancellation. Integration cases use
the real sessions reservation, dispatch-intent, lease, observation, cancel,
acknowledgement, and lifecycle-status operations. They do not qualify a real
host, prove cryptographic process identity, or claim that any external
execution occurred. The adapter's dispatch store is test-only; production
durable host lookup is not implemented by this package.

## Manual compatibility and host qualification

The existing manual workflow remains unchanged: owner configures the capability
and policy, the controller reserves and admits a wave, the separately enrolled
worker uses its own connection to register the checkout and perform
`native swarm handoff`, then uses existing lease/revision/evidence services.
The capability profile is owner-attested and does not prove child credential
delivery, stable host lookup, actual model selection, cancellation, or process
termination. No extra native slots or unattended execution are enabled here.

Before a host is marked qualified, an owner must record a named checklist for
the exact host/version and private channel:

1. Verify authenticated registration rejects a different workspace, task,
   attempt, host, session, process instance, and owner-provisioned profile.
2. Verify expiry and revocation prevent new calls; late callbacks can only use
   the still-authorized bridge and cannot regain worker authority.
3. Verify dispatch is durable before side effect and lookup of the original key
   recovers a response lost across adapter restart without relaunch.
4. Verify callback replay, reordering, process replacement, and terminal
   regression refuse; verify cancellation request, acknowledgement, and
   terminal observation are independent.
5. Verify worker-specific profile delivery keeps credentials out of prompts,
   logs, and receipts, and verify model, lookup, observation, and cancellation
   capabilities independently.

Until that exact checklist is completed and owner-recorded, report those
capabilities as `unqualified` or `unsupported`. Codex is the current manual
compatibility target; other hosts require their own qualification. No live host
execution was performed for this contract.

## Four independent facts

Schema 1 keeps these values separate:

1. **Requested concurrency** is an owner configured policy for one exact
   workspace, installation, host, runtime, build, and host session.
2. **Host declaration** is the selected host's stated slot total.
3. **Observed capacity** is a fresh observer report of occupied and available
   slots. A known zero is different from unknown or unsupported.
4. **Qualified concurrency** is a separately owner authenticated qualification
   with an evidence digest and expiry. An observer cannot qualify itself.

Each fact has an explicit `known`, `unknown`, or `unsupported` state. Readers
may diagnose `stale` and `contradictory` combinations. Unknown, unsupported,
stale, contradictory, or unqualified facts never produce positive capacity
evidence. The aggregate classification is informational and is not scheduler
admission.

The `consistency_state` diagnostic is independent from freshness, so a stale
observation can still report whether its declared and measured totals agreed.

## Observation identity and freshness

An owner enrolls a separate observer session for an exact host, runtime, build,
capability digest, observed executor process instance, and existing executor
host session. The process instance is explicit and is not inferred from the
host label, build, or authority session ID. The observer credential has only
the `capacity.observe` action and exact host/instance/workspace/session
resources. The executor host session is not the observer principal. Both
session ancestry and the observer enrollment are checked again for each write
and read. Policy, observations, and independent qualification bind the same
instance. A replacement instance cannot inherit the previous instance's
qualification or observations.

An observation binds the same host/runtime/build/session/instance/digest, an
immutable operation key, a strictly increasing sequence, and source time. The authority
sets received time. Identical operation replay returns the old receipt and
does not refresh freshness; changed replay, stale sequence, future source time,
identity substitution, revocation, expiry, negative values, and arithmetic
overflow are refused. A mismatch between the declared total and the observed
occupied plus available count is retained as contradictory evidence and is
never positive.

The adapter contract contains no bearer credentials. Credentials, owner
enrollment, persistence, locking, and replay checks remain in the sessions
authority. The `executor` package depends on `sessions`; `sessions` does not
depend on `executor`.

## Schema and compatibility

Capacity facts require authority schema 21. Existing schema 20 is opened with
its exact reader and can be upgraded only through the explicit owner-authenticated
20-to-21 migration. The migration refuses unknown source fields before
rewriting schema 20. Schema-21 readers refuse unknown top-level and nested
fields, future versions, and missing required capacity collections. Incoming
owner contract versions other than the supported version are refused. That
migration initializes the four capacity collections and preserves all
existing records. An already-open schema-20 handle continues
to refuse schema 21. `OpenCurrent` selects the exact current schema; legacy
`Open` remains bounded at schema 19. Schema 6 through 19 compatibility remains
bounded by each reader's explicit version gates. No authority is upgraded on
open.

Schema 10 is the currently observed live authority and is not migrated by this
feature. Its existing manual host workflow remains manual. Schema-21 support
here is source and fixture behavior only; it does not prove a live host
observer, install an observer, or change dispatch/admission authority.


## Bound host-slot extension (authority22, report2)

The separate version2 capacity DTO preserves the version1 contract and adds
`census`: schema1, an owner-selected `authority_domain_id`, and an explicit
`included_local_executions` array, including for a known empty census. Each
entry carries `dispatch_id`, `reservation_fence`, `intent_id`,
`original_operation_key` and `host_agent_id`. Unknown fields, duplicate keys,
missing/null required measurements, substituted scope and ambiguous identities
refuse before producing positive capacity. Explicit numeric zero remains known
zero. Host-session, observer principal and worker principal are distinct.

The selected single-authority swarm policy binds all scope and capacity fact
identities. Original dispatch reservations carry their slot claim; the executor
must compare that claim with independently owner-selected expected scope before
host effects and recheck the actual authority permit. A returned claim cannot
supply its own expected identity. Original-key uncertain dispatch remains
lookup-only; registration or replay never grants another host launch.

The authority subtracts exact census overlap from observed occupancy, counts
all unresolved claims regardless of nominal lease expiry, and retains existing
writer protection after an accepted terminal proof frees execution capacity.
An accepted census remains historical after a later terminal callback. Terminal
eligibility requires an owner-selected typed qualification with supported state,
exact source/domain/host/session/instance/runtime/build/capability, evidence
digest and validity interval. The proof freezes the qualifying historical policy
and evidence at accepted ingestion time, so later bridge expiry/revocation
cannot rewrite history. Cancellation acknowledgements are not termination.

`OpenHostSlots` is exact22; `OpenCapacityCurrent` accepts21/22. The owner-only
`native capacity slots upgrade` migrates21→22 without changing the old exact21
reader, version1 hashes or existing semantic history. No open operation migrates.
Source and fixture behavior does not activate live schema10, qualify a live
observer or serialize multiple authorities sharing one host. A real external
semaphore would be a separate idempotent effect with explicit unknown state.
