# Native estimated budget allocations (schema13)

These are local owner-estimated token allocations. Provider token usage is null,
provider/host spending enforcement is unsupported, and manual model availability
attestations remain owner assurance. A remaining estimate is never a measured or
enforceable provider allowance. `RequireEnforcedBudget` and token enforcement
requirements in preflight/checkpoints remain unsupported and block execution.
No coding CLI, nested model turn, billing API or new MCP tool is added.

The owner explicitly migrates an authority12 to13:

```sh
dacli native budget upgrade --config /absolute/private-owner.json
dacli native budget configure --config /absolute/private-owner.json --request /absolute/private-policy.json --json
dacli native budget controller add --config /absolute/private-owner.json --project PROJECT --connection /absolute/new-controller.json --credential /absolute/new-controller.token
```

Migration preserves all schema6–12 sessions, leases/fences, operation receipts,
lifecycle/recovery history, preflight policies/retries and durable checkpoints.
Current session, preflight and checkpoint services read13. Genuine12 binaries
refuse13 rather than dropping its ledger. Global authority is never migrated
implicitly; the owner chooses the exact private authority. Existing projects
without a configured budget policy retain their explicit existing compatibility
route. A budget policy does not silently enable a checkpoint policy; bounded loop
users configure both, preserving the mandatory single-wave checkpoint gate.

## Policy and estimates

`BudgetPolicy` uses `SchemaVersion:1`, `ProjectID`, immutable `Revision`,
`Harness:"codex"`, explicit owner accounting `CycleID`, `Rationale`, positive
`CycleLimit`/`WindowLimit`, `RecoveryFloor`, positive `Window` (Go duration
nanoseconds), `WindowOrigin` (fixed accounting clock lower bound), `ExpiresAt`,
optional `CooldownUntil`, and `Tasks` keyed by canonical task ID. Each task's
`Phases` contains `BudgetEstimate{RoleID,Model,Tokens,Provenance:"owner-estimate",
Samples,ObservedAt}`. Samples may be0; they describe the owner's estimate source,
not observed provider billing. Estimates must be fresh within30days.

Implementation, correction and recovery are required positive estimates.
Required independent review needs review and re-review estimates; required local
integration needs integration; required verification needs an explicit estimate.
Verification may explicitly use0 for a local command that invokes no model.
Missing estimates never become zero. Additional positive optional phase reserves
remain protected. Role/model estimates must match the current owner preflight
roles and fresh canonical observation. An owner changes the budget revision to
rebind changed roles/models; changing to another role with the same model does
not reuse an old role estimate.

Optional `Alternatives` maps owner-authorized fallback role IDs to estimates.
Quotes explain cost-tier ordering, exact current charter/grant/capacity/scope/
context/capability tags and phase preflight. A cheaper capable fallback is rejected
when the selected private worker profile pins another role; it requires explicit
owner reconfiguration. Unestimated alternatives expose null `MarginalEstimate`
and `EstimateKnowledge:"unknown"`; zero never means free. The total marginal
estimate includes protected downstream phases. No model switch or actual model
availability is claimed by this explanation.

`CycleID` is an explicit owner accounting cycle, separate from checkpoint
invocation/cycle counters. Reusing an ID cannot change its cycle limit. Changing
it preserves outstanding old-cycle liabilities in the new cycle and all recent
rolling charges. Window duration/origin cannot be reset by a new policy revision.
Changing owner limits or cooldown requires a new immutable policy revision and
never deletes allocations or producer receipts.

## Preview, reservation and status

```sh
dacli native budget quote --connection /absolute/controller.json --capability-file /absolute/capability.json --request /absolute/quote.json --json
dacli native budget status --connection /absolute/controller.json --capability-file /absolute/capability.json --request /absolute/status.json --json
```

Quote request: `{ "ProjectID":"PROJECT", "TaskID":"TASK" }`.
Status request: `{ "ProjectID":"PROJECT" }`. Quote returns typed JSON before
exit3 when admission is refused. Status is read-only, including under STOP.
Budget-specific read/quote/settle grants require the exact project and all task
scopes whose allocations would be disclosed. Automatic wave reservation retains
existing `swarm.reserve` and mandatory checkpoint grants; it does not obtain
owner authority or require a standalone budget command grant.

`calculateBudgetQuote` is the same pure finite arithmetic used by quote and live
wave planning/admission. Planning shrinks/backfills only complete task cycles,
including implementation, review/re-review, correction, integration and recovery,
while preserving the global recovery floor. Known-zero headroom stops before
role/model preflight routing. Reservation atomically allocates the selected phases
with the original wave/dispatch/fence. Stale plans refuse before new intent.
Final first-intent admission requires the exact allocation/current policy, fresh
preflight and current local headroom. Replaying an original operation returns
its immutable receipt and never another host-action permit.

Status includes estimated cycle/window limit, charged estimates, outstanding
reservations, remaining estimate, required review/integration/recovery reserves,
unallocated headroom, policy/cycle/ledger identities, timestamps and provenance.
`ProviderUsage` is null throughout. Rolling spend sums charges in the preceding
`Window`; it is not a tumbling calendar bucket. `WindowReset` is the next actual
estimated charge expiry or null when no charge will expire. Unknown/live
reservations have no promised reset and carry indefinitely. STOP, cooldown,
checkpoint cycle/no-progress and resource protection remain separate durable gates.
Accounting changes invalidate checkpoint `ConditionDigest` without becoming
actual `ProgressDigest` progress or resetting no-progress.

## Producer-linked settlement and recovery boundaries

```sh
dacli native budget settle --connection /absolute/controller.json --capability-file /absolute/capability.json --request /absolute/settlement.json --json
```

Request: `{ "OperationKey":"UNIQUE", "DispatchID":"EXACT",
"Phase":"implementation", "ProducerID":"PUBLIC-PRODUCER-ID",
"ExpectedFence":42 }`. Obtain the producer ID from the matching dispatch's
`AvailableProducers` in status. The projection exposes only actionable allocated
phase IDs and safe knowledge labels; it excludes commands, private profiles,
credentials and unrelated tasks. Another dispatch's producer cannot settle this
one. This call records accounting, never producer execution.

Implementation completion/failure requires an exact durable cooperative terminal
lifecycle observation. Cancellation acknowledgement alone retains uncertainty:
the lifecycle ledger can still have missing/unknown terminal outcome. Pending,
stale, timeout or uncertain work remains reserved. Local verification uses the
exact native verification operation/attempt/lease/fence; pending execution retains
liability and cannot rerun. Independent review uses the original authenticated
proposal; its accounting is distinct from current review eligibility and canonical
acceptance. Correction uses an actual later published revision, and re-review
requires a later revision's separately credentialed proposal. Settlement charges
the original conservative estimate, never fabricated measured tokens. Historical
settlement receipts remain exact after later phases settle and after restart.

Optional `IntegrationOperationID` and `AcceptanceOperationID` are contract refs,
not producer outcomes. Authorized pending local integration retains its allocation;
actual integration/acceptance producers and proof remain core592/core594. External
adapter outcomes are unsupported here; configured future contracts remain unknown,
without invented core596 evidence. Recovery model work remains estimated/reserved;
a core595 owner protection decision does not prove that model phase executed.

No settlement releases a reservation/lease, changes native fences, cancels a host,
withdraws a worker, or authorizes another model turn. Resource release still needs
an exact authenticated core595 owner decision including recorded uncertainty/risk.
Crash recovery keeps original operation IDs, allocation IDs and fences. Never
create a new key to resolve an uncertain producer execution.

## Evidence boundaries

Kernel fixtures cover finite full-cycle arithmetic, protected recovery, concurrent
disjoint dispatches and exact replay, role/expiry/STOP/cooldown, true rolling charge
expiry and carried unknown liabilities, producer binding and immutable settlement.
The CLI fixture runs a real configured local command, owner-enrolls separate
reviewer credentials, produces correction/re-review, selects public producer IDs
and settles/replays them after reopen. Fixture host IDs/capability declarations
are contract inputs; they are not proof of a real model host launch or provider
usage. Existing539 actual command/review crash tests remain intact. Actual bounded
loop orchestration/catalog delivery are core591/core593; production integration,
acceptance and external status proof remain core592/core594/core596.
