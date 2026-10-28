# Finite native controller driver

The driver advances one existing native controller invocation through bounded,
authenticated producer operations. Controller, checkpoint, dispatch, lifecycle,
verification and review records remain authoritative. The driver adds no durable
queue, counter ledger, scheduler or background service.

This is the source implementation of #1240. The full swarm milestone #1218 also
requires rolling admission and a scoped keeper (#1241), shared validation and
measurements (#1242), and separately selected live-host qualification. A source
fixture does not qualify a provider, model, operating-system process or capacity.

## Local invocation

```sh
dacli native controller drive \
  --profile /absolute/private/driver.json \
  --request /absolute/private/request.json \
  --json
```

The profile selects existing independent controller, worker, bridge, runner and
reviewer connections. It cannot contain owner credentials, enroll actors, create
grants, select arbitrary commands or supply completion booleans. Files are private,
bounded and pinned to their initial bytes; replacing a valid profile in place
requires a new explicit selection rather than silently changing this run.

The CLI supplies no host backend or independent review decision. It can reconcile
existing producer records and use an already configured local verification
producer. It stops before reserving new work or creating a new dispatch intent
when the required host port is unavailable. Manual controller commands remain
available. An authority below controller schema16 returns an explicit unsupported
result without migration, attempt registration or host effects; live schema10 is
not upgraded by installation or invocation.

The Go composition API inside dacli accepts explicit host and review ports:

```go
selected, err := application.OpenNativeControllerDriver(profile, backend, decisions)
// Handle err before invoking a selected adapter.
result, err := selected.Run(ctx, request)
```

`backend` implements `executor.ContextBackend`; a legacy synchronous `Backend`
cannot be supplied as a fallback. Every host callback receives the bounded
context, including capabilities, binding proof, original-key lookup, observation
read and cancellation lookup. `decisions` implements
`application.NativeDriverReviewDecisionSource` and supplies an independently
produced exact review proposal. A missing decision source never becomes approval.
Neither port launches an LLM, coding CLI or provider process on the driver's behalf.

## Profile and request

The private profile has `schema_version: 1`, workspace/installation/project,
invocation/checkpoint identifiers, absolute `controller_connection` and
`capability_file` paths, `limits`, and a bounded `tasks` list. Each task selects
`worker_connection`, `bridge_connection`, `worker_enrollment`,
`dispatch_template`, `runner_profile`, `reviewer_connection` and
`reviewer_profile`. The enrollment contains the same stable attempt request ID
used before restart. The private dispatch template contains its prompt; public
results never echo it. All connections must bind the same actual authority and
workspace, the stored task route and independent producer principals.

The request selects identity and mode, without overriding grants or limits:

```json
{
  "schema_version": 1,
  "project_id": "project",
  "invocation_id": "existing-invocation",
  "checkpoint_id": "existing-checkpoint",
  "expected_sequence": 1,
  "mode": "drive"
}
```

Use the observed current sequence. A stale request does not silently adopt a
newer sequence. Modes are `drive`, `stop` and `cancel`; `cancel` additionally names
the original `cancel_dispatch_id`. No mode accepts shell, model or credential
overrides.

Limits use snake-case JSON fields: `max_transitions` and `max_effects` (1–256),
`max_work` (1–4096), `max_no_progress` (1–100), `max_elapsed` (positive, at most
15 minutes), `operation_timeout` (positive, at most30 seconds), and an absolute
positive `max_sequence`. Durations are integer nanoseconds, matching
`time.Duration` JSON, rather than strings or implicit seconds. Existing policy,
persisted start/deadline and caller context further constrain these limits.
Reopening the adapter does not reset the invocation clock or absolute sequence
ceiling. Existing invocation history and distinct producer operations supply
the cross-resume counts; local lookups and scanned descriptors consume work.

Effects count reserved logical producer operations, rather than filesystem
writes or measured host executions. A Step or STOP costs one unit; a Step's
atomic intent creation is part of that operation. Before consuming a first-use
permit, the adapter reserves two units for host registration and dispatch.
Correlation, worker acknowledgement, observation ingestion, a configured
verification operation, an independent review proposal, cancellation request,
host cancellation and cancellation acknowledgement each have their own unit.
The existing verification producer's internal begin/execute/complete sequence
is one configured operation. Read-only lookups do not spend effect units.

Across reopen, the projection conservatively counts retained original and
derived operation identities, controller transitions, and reserved host slots
from existing intent and cancellation records. These counts may include a slot
whose reply or execution is unknown. They never imply a measured host call,
provider charge or permission to retry it. Budget exhaustion prevents another
producer call, including recovery writes; it retains protection for explicit
owner reconciliation.

## Effects and recovery

Only `StepControllerForDriver` can mint a first-use opaque dispatch permit after
the exact intent commits. The driver consumes it before another controller
transition can make it stale. Known host acceptance is correlated by the bridge
and acknowledged by the separately authenticated worker. Acceptance alone does
not imply a running or completed worker; a newer typed observation must pass the
existing lifecycle checks before ingestion.

On entry, restart or an interrupted reply, the driver reconciles original
dispatch, verification, review, integration, acceptance and cancellation keys
before requesting another effect. Missing permits and lookup misses remain
uncertain. They never produce another dispatch or a replacement operation key.
Historical worker enrollment can identify an old execution for read-only lookup;
it cannot renew worker authority, register a new attempt or launch work.

All required verification requests use their separately granted configured
runner and exact revision. Pending or interrupted checks cannot be executed
again under a new key. Review requires the existing independent reviewer and an
explicit decision. Integration, canonical acceptance, publication and release
remain owner gates: existing exact receipts may reconcile, while absent owner
effects return `owner_required` or `unsupported`.

STOP prevents new work and retains uncertain writers. Explicit cancellation
withdraws authority before its first host request; a lost reply uses cancellation
lookup. An acknowledgement is distinct from independently observed termination
and never releases a reservation or lease. Repeated unchanged observations return
without a polling sleep, renewal loop or model turn.

## Results and bounds

JSON uses `native-controller-driver/v1`, with invocation/checkpoint and sequence,
outcome/reason/next action, local transition/effect/work/no-progress counters,
elapsed milliseconds and bounded original-operation descriptors. Unknown
resource-retention, provider-token and provider-cost facts remain null.

`completed` requires existing terminal controller and producer/canonical truth.
`awaiting_external`, `owner_required`, `unsupported`, `uncertain`, `stopped`,
`cancelled`, `deadline`, `limit`, `no_progress`, `stale` and `refused` describe
bounded stops rather than completed task work. Policy refusal remains exit3 and
must not be retried unchanged. Interrupted effects retain their original keys.

The host context contract is cooperative: its implementation must return when
the context ends and retain original-key outcome knowledge. Legacy authority
operations retain their existing lock wait (up to5 seconds), independently bounded
Git/admission work and filesystem overhead. Context is checked around these
boundaries and before subsequent effects. This is not a hard end-to-end OS
deadline, process containment or proof that escaped descendants stopped. No
timer goroutine abandons a synchronous host call.

Validation is local; CI remains manual only. Fake-host runs and authority,
coordinator or application reopen are labelled separately from actual
OS/provider restart and live-host qualification.
