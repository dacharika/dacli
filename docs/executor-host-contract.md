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

Cancellation request and host acknowledgement are separate from observed
terminal state. `acknowledged` means the host accepted the request; `unknown`
and `unsupported` remain explicit. Neither request nor acknowledgement means
the process stopped, and neither releases reservations or leases. Only the
existing owner-authorized recovery operation can reconcile retained resources.

## Storage and compatibility boundary

This implementation adds no persisted record or reader/writer. Before a future
durable executor record is introduced, an owner-approved upgrade must define its
schema/version, atomic writer, restart lookup, migration, and minimum readers in
`docs/COMPATIBILITY.md` and the native compatibility ledger. Dispatch
reservations, operation-key receipts, worker leases, host intents, lifecycle
observations/cancellation, and checkpoints remain owned by their existing
`sessions` operations. There is no parallel reservation, dispatch, lifecycle,
or checkpoint ledger and no implicit schema migration. A future adapter with
only in-memory binding state must requalify registration after every restart
and refuse dispatch/callbacks until it does.

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
