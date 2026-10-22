# Durable bounded native controller

The controller records a bounded invocation using existing native swarm,
checkpoint, lifecycle, verification and review authority. It does not launch a
coding CLI or borrow worker, runner, bridge or reviewer credentials. A host
performs each advertised action through that producer's separately authenticated
connection. The configured harness is pinned by policy; an owner-manual Codex
fixture does not establish model availability or host spending enforcement.

## Owner setup and explicit migration

`native controller upgrade --config OWNER` explicitly migrates schema15 to16.
All pre-existing authority maps and historical receipts remain intact. A genuine
schema15 writer refuses the migrated authority. This is a local owner action;
normal controller requests cannot migrate authority or enroll a principal.

`native controller configure --config OWNER --request POLICY --json` records a
policy revision. POLICY contains SchemaVersion1, ProjectID, Revision, Harness,
CapabilityDigest, TaskIDs, MaxWaves, MaxConcurrency, MaxCorrectionRounds,
MaxNoProgress, TransientRetries, MaxElapsed, RetryBackoff and ExpiresAt. TaskIDs
match the configured checkpoint task set. Duration fields use Go duration
nanoseconds in JSON. Bounds have finite validated maxima. Historical policy
replay cannot replace the current revision. STOP is the current owner swarm
policy's Stopped flag.

`native controller enroll --config OWNER --project PROJECT --connection FILE
--credential FILE [--expires 8h]` creates a root controller with exact configured
task/claim scope. New files use exclusive creation and mode0600. A failure after
issuance reports the retained session and private file paths for owner
reconciliation; it never reports the credential or claims automatic revocation.

`native controller start --config OWNER --request START --json` fixes the outer
invocation identity, deadline and policy. START has OperationKey, InvocationID,
ProjectID, PolicyRevision, ControllerSessionID, CheckpointID and Routes. Each
Routes task entry binds BridgeSession, WorkerSession, RunnerSession,
ReviewerSession, VerificationGrantID, ReviewGrantID, CheckID and PromptSpecDigest.
The actors must be independent enrolled roots with current exact repository,
task, checkout, grant and claim bindings. A new caller ID cannot reset bounds
while an earlier invocation retains uncertain producers or protections.

## Refreshing a review observation

`native controller refresh-review --config OWNER --review-config OLD_REVIEW
--request REQUEST --out NEW_REVIEW --json` is a separate, explicit owner
operation. OWNER supplies the private owner credential for the same durable
authority as OLD_REVIEW. OLD_REVIEW must be the existing private reviewer profile
for that workspace, project and task; its separately enrolled reviewer credential
is authenticated but never returned or copied into another authority. NEW_REVIEW
must be an absolute path that does not already exist. The command reserves it
exclusively with mode `0600`; it writes a refreshed profile derived from the old
profile only after the owner operation returns the replacement grant. A policy
refusal before issuance leaves OLD_REVIEW and the authority unchanged and removes
any unissued output. If issuance succeeds but writing the output fails, preserve
the authority and replay the identical owner request to a new private path to
recover the already-issued grant profile.

REQUEST is a private JSON file and binds all of these exact values:

`OperationKey`, `ProjectID`, `InvocationID`, `ExpectedSequence`, `TaskID`,
`ExpectedGrantID`, `ReviewerSessionID`, `WorkerSessionID`, `Repository`,
`AttemptID`, `RevisionID`, `LeaseID`, `ExpectedFence` and
`ExpectedChecklistDigest`.

The request must match the current nonterminal invocation and its exact pending
independent-review action. The invocation sequence, project/task route, old grant,
reviewer and worker sessions, repository, current attempt and revision, lease and
fence are all rechecked. The canonical task must still be open or active and its
observed checklist digest must have changed. The current policy and selected
checks, producer evidence, clean revision, and reviewer/worker independence must
remain the same and valid. A refresh cannot change any actor, check, evidence ID,
revision, policy, producer reference or budget allocation. It adds one replacement
grant, updates only that task's live route, advances the controller sequence and
appends a status/operation receipt; existing grants, producer records and history
are preserved.

Reuse the original operation key only with the identical request. Exact replay
returns a historical receipt and the same grant identity without minting another
grant, advancing state or providing a host-action permit. A changed request under
that key is refused. This operation does not run a review or start a host action;
the reviewer must use its refreshed private profile for a later review proposal.

## Advancing and inspecting

`native controller step --connection CONNECTION --capability-file CAPABILITY
--request REQUEST --json` performs at most one authenticated durable transition.
REQUEST contains OperationKey, ProjectID, InvocationID and ExpectedSequence.
The caller supplies no phase, success claim or counter. A stable outer invocation
uses kernel-derived inner cycle IDs. Fresh pre-cycle observation, selection,
reservation, budget allocations and the committed-wave counter share one
transaction. Dispatch intent and pending action association likewise commit
atomically. Failed late admission discards all trial fences and allocations.

`native controller reconcile --connection CONNECTION --capability-file CAPABILITY
--request REQUEST --json` refreshes scoped producer knowledge without new effects.
It remains available after STOP or a terminal bound, preserving that halt and its
counters. Diagnostic observations and status polls do not spend no-progress or
transient retries. Actual transient failures use bounded retry attempts and
backoff; an early poll spends no attempt. The existing authenticated preflight
retry generation/budget remains authoritative.

`native controller status --connection CONNECTION --capability-file CAPABILITY
--project PROJECT --invocation ID --json` returns historical/current knowledge,
limits, fixed deadline, exact producer references and next action, with no host
permit. JSON uses the native-controller/v1 envelope and a typed Result.

Only a first successful dispatch transition can return HostActionAuthorized.
Replay, restart/status and reconciliation cannot reproduce that permit. If the
response is lost before an external launch, retain the original intent and seek
bridge observation; never mint a replacement execution key. A pending command
keeps its original operation ID. Actual completed or failed lifecycle reports can
advance an existing wave; stale or missing reports remain unknown. Every staged
producer binding is rechecked after the final slow observer. These checks are a
cooperative snapshot, not containment of arbitrary concurrent filesystem writers.

## Bounds, outcomes and recovery

Committed waves, retained concurrency, elapsed time, correction rounds,
no-progress cycles and transient retries are durable and independent limits.
Every selected wave member can progress; waiting for an earlier host, correction
or integration result does not starve another eligible member. A permanent
refusal returns exit3; exact receipt replay preserves that classification while
returning Historical=true and no permit. A transient refusal preserves exit1.
The status endpoint remains a successful diagnostic read.

Verification references bind the actual configured runner request, revision,
lease, fence, check and grant. Failed trusted evidence may request one bounded
correction; it is never accepted as successful review evidence. Review references
require the current independent reviewer and exact eligible evidence. Integration
checkpoints persist those reviewed references but remain pending core592. Native
integration/acceptance success is not invented from contract metadata.

The controller never releases leases or reservations. Explicit schema10 owner
recovery remains the release authority. An exactly recovered member is preserved
as historical unknown knowledge and does not block a healthy different member.
Expiry or an unresolved foreign writer is not retirement. A genuinely empty
scoped backlog ends without creating a task; blocked or missing work is not empty.

Regression fixtures exercise real command execution, independent private review,
postcommit subprocess exit/reopen/replay, lost dispatch response before launch,
separate-process CAS competition, and pre-persistence failures for each stage.
Kernel multiwave fixtures supply canonical admission observations and invoke
actual owner recovery; they do not prove a native integration/acceptance producer
or model host execution. The actual integration producer remains core592.

The enrolled bounded controller and its delegated descendants cannot create generic checkpoint gates, reservations, dispatch admissions or host intents through public mutation APIs. These effects occur only inside atomic controller steps under the fixed invocation bounds. Planning, status and quotes remain diagnostic. Enrollment does not grant budget settlement authority. Independently owner-enrolled generic operators retain their existing APIs.
