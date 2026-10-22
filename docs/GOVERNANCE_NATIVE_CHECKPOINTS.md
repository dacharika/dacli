# Durable native bounded recovery

Checkpoint authority is cooperative local-file governance. It does not launch a
coding harness, terminate host processes, meter provider usage or accept tasks.
Explicit owner migration `native checkpoint upgrade --config <private-owner>`
changes schema11 to12. Existing preflight policies/history, capacity overrides,
operation receipts/retry counters, dispatch proof expiry, lifecycle history and
fenced recovery receipts remain intact. Current services read12; genuine old11
binaries refuse it. Global authority upgrades are never implicit.

The owner enables a project's gate using `native checkpoint configure --config
<private-owner> --request <private-policy> --json`. `CheckpointPolicy` has
`SchemaVersion:1`, `ProjectID`, unique immutable `Revision`, `Harness:"codex"`,
all placement `TaskIDs`, finite `MaxCycles` (1–1000), `MaxNoProgress` (1–100),
`ExpiresAt` (within24h), and optional `RequireEnforcedBudget`. Cycle limits are
owner estimates. Provider usage is unknown, and requiring enforcement blocks.
Policy changes cannot reset checkpoint bounds or substitute a harness/task set.
STOP remains the current owner swarm policy; only an explicit owner policy
transition can change it. Standalone schema11-style projects without a checkpoint
policy retain their explicit existing APIs; once enabled, every new wave is gated.

`native checkpoint controller add` requires owner `--config`, `--project`, fresh
private `--connection` and `--credential` paths and optional `--expires`. Its
separate session has existing swarm actions plus `checkpoint.read`,
`checkpoint.record`, `checkpoint.reconcile` and exact project/task resources.
An installed plugin can authenticate its identity; the current0.4 MCP catalog
has no checkpoint commands. Use the supported local CLI with that connection.

`native checkpoint record|reconcile --connection <private-connection>
--capability-file <private-capability> --request <private-request> --json` accepts
`CheckpointRequest`: `OperationKey`, `CheckpointID`, `ProjectID`, `InvocationID`,
`Phase` (pre-cycle/dispatch/verification/review/integration/acceptance/halt),
`ExpectedSequence`, `Refs`. Each reference has `TaskID`, optional exact
`DispatchID`, `IntentID`, `EvidenceID`, `ReviewID`, and future
`IntegrationOperationID`/`AcceptanceOperationID`. Verification references also
carry `VerificationKey`, `RunnerSession`, and exact `VerificationRequest` fields:
`OperationKey`, `WorkerSession`, `AttemptID`, `LeaseID`, `RevisionID`, `CheckID`,
`GrantID`, `ExpectedFence`. No credentials or command overrides are accepted.

Record appends identity references, never producer success. CAS rejects stale
sequence, key reuse with changed data, cross-project/task links, dropped old
references, replacement checkpoint IDs and caller-selected harnesses. Exact
operation replay is immutable historical evidence. Reconcile reobserves canonical
records, local trunk, complete preflight, the shared scheduler and durable
producer outcomes. Reads require current project and every policy task scope
before returning any linked record. Public projections expose IDs, states,
fences and remedies, never raw authority records, credential hashes, commands or
private profile paths.

Canonical task/project and local event/run snapshots use descriptor-first,
nonblocking, no-follow reads anchored with Unix `openat`. Limits are20,000
entries,256KiB per file,32MiB per snapshot and event-directory depth8. FIFO,
symlink, nonregular, oversized, malformed and duplicate canonical records fail
closed with unknown diagnostics. Other platforms report observation unsupported.
The local projection does not probe legacy process liveness or infer stopped
writers. Only scoped task/run/event IDs reach public diagnostics; a corrupt
unrelated file can invalidate the opaque snapshot without exposing its identity.
The observed trunk is the configured local landing base ref, never the workspace
feature HEAD. No network, user commands, filters or hooks run in this adapter.

Schema12 verification producers persist exact scoped operation bindings before
execution, so an omitted reference cannot hide a pending operation. Legacy pending
operations without bindings remain unknown until an original digest-bound
reference identifies them. Current review eligibility is rechecked independently
of historical proposal presence. An authenticated owner recovery decision can
retire an old protection and explain historical ineligible diagnostics; replacement
producer evidence must be independently current. Recovery never turns old evidence
into completion, and old IDs/fences remain in checkpoint history. Genuine canonical
readiness changes and audited owner recovery can count as progress; adding duplicate
references or changing timestamps cannot reset the no-progress bound.

Only a fresh `pre-cycle` reconciliation can authorize one new reservation. The
`native swarm reserve` request additionally supplies `CheckpointID`,
`CheckpointSequence`, `InvocationID`, `ReconciliationDigest`. The same authority
transaction rederives canonical/producer/policy state, validates the generation's
30s freshness and consumes it with the wave. A historical wave replay returns its
original receipt. It cannot authorize another wave. Bookkeeping and timestamps
cannot reset no-progress; actual producer state/outcome or local trunk advancement
is observed on the next bounded invocation using the original IDs and counters.

`native checkpoint status` accepts `ProjectID` and `CheckpointID` and returns
fresh typed diagnostics alongside persisted cycle/sequence/refs. It supplies no
live permit. Unknown acknowledgement, stale host report, pending verification,
ineligible review, retained protection, terminal-task event, inconsistent record,
resource conflict, pending integration/acceptance, STOP, exhausted bound and
transient preflight have separate diagnoses, knowledge, retryability and remedies.
A completed host observation does not prove all writers stopped. Expired/revoked
leases remain protected until explicit schema10 owner recovery. Checkpoints never
release leases, reservations or task claims.

Integration/acceptance contract references remain **unknown** until core592/594
provide authenticated outcomes and their crash proof. Optional external PR/CI
adapter recovery remains core596. Verification pending means durable reservation
without committed result, not a running process. Never choose a new execution
key after an uncertain result. Exit0 means the bounded command succeeded; a
retryable typed halt exits1, permanent/manual reconciliation exits3. Record exits0
while reporting `reconciliation_required`; it is not cycle completion.

Named fixtures interrupt orchestration after actual durable API producers and a
configured local command, reopen authority, and check exact immutable replay,
one command side effect, independent reviewer root and fresh revocation. These
are cooperative producer-boundary fixtures, not evidence of a model host launch
or universal process termination. Mutation evidence must fail actual assertions
when the pre-cycle gate is bypassed or unknown native outcome becomes success.
