# Native output usage observations

`internal/usage` stores local `native-output-usage/v1` facts from a native
harness or provider callback. `application.NativeOutputUsageProducer` is the
write boundary: it performs a fresh read through the selected
`NativeDashboard`, proves that the event's task belongs to the requested
project, checks that the attempt is the newest registered attempt for that
task, and binds the observation's session to that attempt. A stable agent
profile is added only when an `NativeAgentProfileResolver` independently
resolves the profile from the host's selected profile and exact session/task/
attempt tuple. A profile ID in an incoming event is never accepted by itself.

## Observation contract

Each version 1 observation records workspace and project IDs, optional resolved
agent profile ID, enrolled session, task and attempt IDs, provider, model,
provider stream ID, source event ID, observation time, source and availability.
Output and input token counts are separate optional values. Every count has the
literal unit `tokens`, a nonnegative count and a stream sequence; its basis is
`delta` or `cumulative`. Provider or native-harness counts are marked
`measured`. Agent-declared counts are marked `declared` and remain separate in
the totals; only measured output tokens are exposed as growth-eligible facts.

When a harness reports no usage, the producer may persist an `unavailable`
observation with a reason such as `provider-usage-unsupported`. It must not
substitute zero or estimate from cost, elapsed time, characters, or output
text. A real measured zero can be represented as an explicit count of zero.
The persisted type intentionally has no prompt, source-code, credential, or raw
output field, and the reader rejects unknown JSON fields.

## Reconciliation and retention

Events are immutable and scoped to one workspace/project/task/attempt. A repeat
of the same event ID and bytes is idempotent; different bytes for the same ID
are refused. A correction names the earlier event IDs in `supersedes`; unknown
or self references are refused. Active observations are ordered by source
sequence within each provider/model/stream/provenance lane, so delivery order
does not change the result. Deltas add, cumulative checkpoints replace the
running total, and separate lanes add together. Attempt rollover creates a new
scope: reads and writes through the native producer refuse the older attempt.

The store writes mode `0600` JSON files under the workspace's ignored
`.dacli/build/output-usage/v1/<project>/<task>/<attempt>/` directory. It is
capped at 4,096 events and 32 MiB per attempt; when either bound is reached the
producer returns an explicit retention error and does not prune facts needed to
reconcile corrections. A per-attempt exclusive lock serializes reads and
appends across processes. A leftover lock after a crash is not stolen; an owner
must confirm there is no writer before removing it. There is no separate
billing or spend ledger.

## Producer capability boundary

The application API accepts already measured metadata from a native harness or
provider callback. It does not inspect a process, wrap a coding CLI, or scrape
transcripts. Provider and model fields describe that callback; the local API
does not cryptographically attest an upstream provider. A host adapter must
call the API only from its observed usage callback and pass `unavailable` when
that capability is absent. Incoming session, task, attempt, workspace and
profile fields are claims to be checked against fresh local authority reads,
not credentials.

The current macOS shell and native host transport do not expose a verified
provider usage callback to this Go producer yet. Until that host capability is
connected and observed, runtime usage is **unavailable**; fixture observations
exercise the contract only and must not be presented as live collection. A
host that lacks output-token reporting must retain that explicit unavailable
state. No CLI interception or inferred token values are supported.
