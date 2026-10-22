# Complete native loop preflight

Core548 adds schema11 complete-loop admission. DACLI remains a governance layer;
these operations never launch a coding CLI, open a model turn, or select another
host as a fallback. Existing schema6–10 authorities remain usable by their current
services. An owner explicitly upgrades10→11 using a private owner configuration;
no global configuration or authority is migrated automatically. Migration preserves
all identity, grant, verification, review, publication, swarm, dispatch, lifecycle
and recovery history. Genuine previous schema writers refuse11. New schema11 maps
are required even when empty; missing maps and newer records in older states fail
closed. Recovery and all participating worker/reviewer/runner services read11.

## Owner configuration

Use `native preflight upgrade --config <private-owner-file>`, then
`native preflight configure --config <private-owner-file> --request <private-policy-file>`.
All files use the existing private-file permissions and strict JSON decoding.
A `PreflightPolicy` has schema1, project/revision, pinned `Harness:"codex"`, rationale,
expiry (at most24h), `AcceptManualAssurance`, transient retry limit0–3 and `Tasks`.
Each task pins implementation, review and recovery roster names, the complete
canonical fallback list, and private worker/reviewer/runner/phase-contract files.
Controllers cannot substitute those paths or credentials through a dispatch
request. Required native review also requires preflight of its exact verification
checks and runner, even if `RequireVerification` was omitted.

The selected worker needs current task, claim and exact checkout resources plus
register, lease acquire/renew/release and, for verification/review, publication and
verification-request actions. Owner `native enroll --checkout <absolute-checkout>`
adds an exact canonical clean checkout in the same repository. The reviewer is a
separately owner-enrolled root, distinct from the worker family, with only native
review actions and a current grant bound to worker/task/repository/checklist/policy.
The runner is likewise independent and matches the profile-selected exact check
grants. Preflight does not require a worker attempt or existing executed evidence:
those are produced only after implementation.

## Task size and concurrency

Every resolved implementation, required reviewer, recovery and fallback role uses
`team.TaskCapacity` with the canonical PERT expected size. Role definitions and
roster, model profile, task and claim digests enter the stable proof identity.
Role concurrency remains `SwarmRole.Capacity`; it is never interpreted as task
points. Negative/nonfinite size or limits and mismatched canonical charters refuse.
Normal team assignment and native admission therefore share the size predicate.

An owner may use `native preflight override` with an immutable ID, exact task,
optional explicit `RoleID` (default implementation), reason and expiry. The stored
receipt names role, task, delta, owner actor, policy, task/role digests and harness.
It waives only that exact role's task-size refusal; grants, independence, claims,
concurrency, phase capability, STOP and expiry remain mandatory. Overrides are
expiry-bounded; no caller-provided invocation ID is treated as authority. If several
matching overrides are live, latest expiry wins, then lexicographically smallest
ID. Altered reuse refuses. Changing task/role/policy invalidates the old override.

## Phase contracts and assurance

The schema1 `NativePhaseContract` is fresh for at most one hour. It names the exact
workspace/project/task and Codex harness, explicitly available model IDs,
`ReviewOutputContract:"native-review-v1"`, and optional owner-manual integration
route/base/policy/checkout. Its private declarations establish **owner-manual**
assurance only. They cannot establish enforced model/token/budget controls, native
producer592, accounting552, automatic child MCP delivery or cryptographic host
identity. Required enforced token controls are unsupported; required enforced
rolling budgets are unknown and block. Optional controls remain visibly unavailable.

Required integration needs separate `OwnerAuthorizedIntegration`, owner acceptance
of manual assurance, route `owner-manual-local`, strict base/policy digests and a
fresh clean same-repository landing checkout at that exact commit. Its delivery,
remote authorization and check-policy observation remain explicitly cooperative
owner attestation. An unavailable native integration producer blocks before work.
No integration permission is inferred from worker task authority. Owner recovery595
is not an alternative host dispatcher; recovery/fallback remain pinned to Codex.

Verification preflight validates bounded exact configured check descriptors,
current independent grants, clean placement workdir/repository and the actual
`sh -c` interpreter. Shell assignments, builtins and quoted paths retain the existing
shell contract. The commands are not executed during preflight; execution
availability is labelled owner-manual, accepted only by explicit owner policy.
Executing trusted checks and recording evidence remain separate native operations.

## Shared admission and reports

`native preflight check --connection <private-controller-file> --capability-file
<private-capability-file> --request <private-request-file> --json` accepts
`ProjectID`, `TaskID`, `OperationKey`. It uses existing scoped `swarm.plan` authority,
not a new controller provisioning privilege. The durable schema1 receipt contains
safe phase verdicts, proof IDs/digests, assurance, capacity and remedies. Every
implementation/review/verification/integration/recovery/fallback/token/budget phase
is reported even when an earlier policy failure leaves it not evaluated. Private
credential hashes are internal only; no credential bytes/hashes or check commands
enter reports or proof digests.

Permanent capability/policy refusals produce exit3. Transient owner-attested external
failure returns the distinct `ErrPreflightTransient` through reservation, admission,
intent and handoff adapters; the CLI emits its durable JSON receipt and exit1.
Only authenticated, exact-task-scoped `native preflight check` operations spend the
durable per-policy/task retry budget. Each retry uses a new check operation key;
exhaustion returns permanent exit3. Live admission refusals roll back and do not
spend that budget: a controller must use bounded check operations before retrying
admission. A permanent refusal in any selected task dominates a transient refusal
in an atomic wave. Uncertain host execution must never be redispatched as a retry. Exact authenticated operation replay returns
history without spending another retry or authorizing work; altered replay refuses.
Preview, reservation, live intent, admission and handoff consult the same fresh
predicate and durable exhaustion guard. Old dispatches without a schema11 proof
cannot gain new permission after upgrade; historical receipts and owner recovery
remain usable. Audit observation times are excluded from stable plan identity.

Current policy/override/session/grant expiry, revocation and private/canonical
profile changes are rechecked before allocation, first host intent and lease
handoff. No result is returned on failed persistence. Pure bounded file observers
inside the workspace authority transaction never authenticate or open nested
transactions. Markdown/private-file writers do not share that lock; repeated
observations provide cooperative freshness, not serializable filesystem edits.

Task591's loop producer,552 accounting and592 integration producer remain separate
pending services. This preflight provides an honest bounded admission contract and
versioned reports; it does not claim those producers or their guarantees exist.
