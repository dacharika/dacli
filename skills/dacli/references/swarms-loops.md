# Parallel features and bounded swarms

The native host creates and delegates agents. For multi-feature work, select
several ready, dependency-independent canonical tasks within the user's scope.
Give each feature a bounded acceptance contract, disjoint claims, a real checkout
and its own authenticated worker connection. Maintain a small lane record:
canonical task, dependencies, paths, attempt/lease/fence, current candidate,
focused checks, independent review and next owner action. Refill capacity as
features land; a blocked lane does not suspend unrelated eligible work.

Register the clean checkout, acquire scoped leases for its exact resources,
and renew protection before expiry. Claim overlap is a useful refusal: narrow
or sequence the conflicting edit while
other independent lanes continue. Use observed host availability, current policy
and qualified capacity where required by admission. Configured width, lease
counts and historical host declarations do not prove live concurrency; no fixed
worker count is implied by this skill. Never wrap a coding CLI to simulate a
native child.

Independent review requires a separately eligible authenticated session and
exact revision evidence. Coordinate expensive full-suite/race/build gates
centrally for exact feature candidates. Focused checks and read-only review
can proceed in parallel. Preserve each feature's full required check set and
independent review; a central queue
does not make evidence transferable across tasks. Merge and accept features
independently when their own contracts are satisfied.

## Diagnose blocked lanes

Use `ready_frontier` for scoped canonical readiness. If the owner has already
provided a separate project operator connection, its `queue_diagnostics` tool
reports ready, queued, running, check, review and integration stages with bounded
reason rows. That catalog contains only `identity` and `queue_diagnostics`; a
worker or generic reader cannot borrow its `operator.read` authority. Counts
cover the selected scope before row truncation. The read does not reserve work,
run preflight, renew leases or change admission. Queue age, provider quota, usage
and cost remain unknown when no supported source supplies them.

Host capacity policy, a fresh observed census and independently qualified
concurrency are separate facts. Where host-slot admission is enabled, execution
occupancy and retained writer/path protection are also separate: a terminal
host observation can free execution capacity while writer protection remains.
STOP, expiry, lost heartbeat and cancellation acknowledgement do not prove a
writer terminated. Inspect compatible configured facts; never migrate authority
merely to make a requested worker count appear available. See the
[queue diagnostics contract](https://github.com/dacharika/dacli/blob/main/docs/GOVERNANCE_NATIVE_QUEUE.md).

## Selected orchestration profiles

A separately owner-configured controller connection can advertise `plan_swarm`,
`start_loop`, `step_loop`, `reconcile_loop`, `get_loop_status`, `stop_loop` and
`cancel_dispatch`. Inspect that exact connection's catalog and
`get_orchestration_host_status`; a basic worker catalog is different. Preserve
invocation, checkpoint, expected sequence and original operation keys. Only a
fresh `HostActionAuthorized` receipt permits its specific host action; replay
and status are historical observations.

Perform the authorized action using actual native collaboration. The separately
configured bridge correlates the real child with `correlate_dispatch`. The child
registers its own attempt and calls `acknowledge_dispatch` through its own eligible
handshake connection. Handoff ID is the intent ID, distinct from dispatch ID;
acknowledgement uses a new operation key. Require the returned current lease
before editing. Independent runner and reviewer connections retain their own
named-check and review workflows. Reconcile durable outcomes before advancing.
`pending_local_integration` retains the exact producer tuple for the authorized
owner integration adapter; it is not completion.

`stop_loop` prevents future controller effects while retaining resources.
`cancel_dispatch` requests cancellation; bridge acknowledgement and actual host
terminal observations are separate. Missing heartbeat is unknown, not proof of
termination. Keep uncertain identities and protection for owner reconciliation.
Automatic host dispatch, automatic termination and unattended private-profile
delivery are not supplied by the merged cooperative interface. Source/fixture
support and historical receipts do not qualify a new live host or process.

See the [native swarm contract](https://github.com/dacharika/dacli/blob/main/docs/GOVERNANCE_NATIVE_SWARMS.md)
and the [Codex orchestration setup](https://github.com/dacharika/dacli/blob/main/plugins/codex/README.md#native-swarms-and-bounded-loops).
