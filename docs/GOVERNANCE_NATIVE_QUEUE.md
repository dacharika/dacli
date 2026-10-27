# Native queue diagnostics

Issue #1230 adds a bounded read of canonical tasks and authenticated authority
records. It produces `native-queue-diagnostics/v1`; it does not persist a queue,
reserve work, launch an agent or change admission policy.

## Access

Use a separately owner-issued project operator connection with `operator.read`
and the exact workspace/project scope. A normal worker or workspace reader is
insufficient. The existing owner command `native operator add` provisions that
connection; keep its profile and credentials private.

```sh
dacli native queue diagnostics --connection /private/operator.json --project core --limit 50 --json
dacli native queue diagnostics --connection /private/operator.json --project core --task EXACT_TASK_ID --json
```

Selecting that operator connection for native MCP exposes only `identity` and
`queue_diagnostics`. The tool accepts `schema_version: 1`, required `project`,
optional exact `task`, and optional integer `limit` from 1 through 100 (default
50). It accepts no caller-supplied source, command, profile or credential path.
Changing a selection requires restarting and observing a fresh server;
installing files does not reconnect a cached chat. Default reader and worker
catalogs and grants stay unchanged.

## What the result means

The six mutually exclusive task stages are ready, queued, running, check, review
and integration. Fresh authenticated execution takes precedence, followed by
current integration, review and verification work. Remaining tasks are queued
when a required gate is blocked, unknown or unsupported. A ready row describes
the observed predicates; a later admission can still refuse. Canonical completed
or cancelled tasks are excluded from these counts, while their retained writer
protection can remain in host aggregates. An exact task filter narrows returned
rows and counts, while occupancy still considers the authorized project graph.

Fixed reason codes distinguish dependencies, claims, retained paths/checkouts,
roles, models, preflight, host capacity, and downstream work. Historical evidence
does not become current eligibility. Missing or stale observations remain
unknown. Provider quota, usage and cost are unknown/null. Queue entry time and
age are null: task creation and file modification times do not establish them.

The host row covers the selected project's configured host. Exact schema22 uses
the existing host-slot admission predicate. Execution claims cover that admission
domain, while retained writers cover the selected project; they are different
quantities. A qualified completed turn can release an execution claim while
retaining its writer lease. Foreign tasks can affect private dependency or
shared-domain capacity calculations without exposing their identifiers or paths.
This is not discovery of every host or enforcement of provider billing.

## Bounds and consistency

Rows are sorted by task ID. Counts cover the full selected scope before
truncation, and `omitted_count` is exact. Continuation is explicitly unsupported.
At most 100 rows and eight reasons per row are returned. The encoded diagnostic
payload is limited to 1 MiB; MCP additionally wraps it as structured and text content.
The read permits two authority reads of at most 32 MiB each and one complete
canonical graph of at most 32 MiB, 20,000 entries and 256 KiB per file. Invalid,
unstable or oversized sources refuse instead of presenting partial counts.

The private identity is authenticated before the canonical read and revalidated
after it. Changed authority bytes or replaced connection/credential selection
refuse. Descriptor checks and bounded loops cooperate with a maximum 15-second
deadline; arbitrary OS I/O is not forcibly interruptible. This is a cooperative
observation across files, not an atomic Markdown/authority transaction.

The queue call does not create authority locks, write files, scan processes,
read per-task checkout state, call GitHub, run preflight or invoke a host adapter.
The direct CLI/kernel path is lockless from construction. MCP startup,
initialize, identity and tool discovery retain their existing authentication;
the queue tool call after initialization bypasses generic transaction-based
authentication and performs its own pinned read authorization.

## Source and live qualification

The projection strictly reads stored authority schemas6 through22 without
migration. Schemas below22 omit host rows and report
`host_capacity_unsupported` in task reasons; future schemas refuse. The development workspace remains
schema10. Schema22 mixed-stage, cap+1, restart, stale census and retirement tests
are controlled fixtures, not a measured twelve-worker run.

Issue #1217 still owns live host qualification. Issue #1218 still owns rolling
admission and host effects. This read supplies diagnostics for those follow-ups;
neither saved worker limits nor fixture results qualify live parallel capacity.
