# Native host lifecycle and cancellation

Lifecycle records share the workspace authority transaction and fence history with
native dispatch reservations, attempts, verification, review and publication.
No coding CLI or app-server model turn is launched by these operations. The
Codex diagnostic probe only recognizes `turn/interrupt` on a nonexistent turn;
it does not establish cancellation of a running agent or its subprocesses.

## Owner setup

`dacli native lifecycle upgrade --config <private-owner-config>` explicitly
migrates schema8 to9. Credentials, grants, evidence, review, publication and
all dispatch receipts remain unchanged. No startup migration occurs. Current
worker/verifier/reviewer services read6/7/8/9; genuine6/7/8 writers refuse9.
Upgrade all participating binaries before owner migration. Existing owner-only
schema5→6,6→7,7→8 upgrades remain separate.

Configure a private owner policy with
`dacli native lifecycle configure --config <private-owner-config> --policy-file <private-policy>`:

```json
{"SchemaVersion":1,"ProjectID":"core","Revision":"lifecycle-v1","Source":"owner-manual-native-host","Freshness":60000000000,"CancellationEnabled":true,"CancellationChannel":"unsupported"}
```

Freshness is a positive duration in nanoseconds, at most one hour. An immutable
revision cannot be reused with different contents. The cancellation channel is
`unsupported` or explicitly owner-selected `owner-manual-native-host`. The latter
permits a cooperative native host call; it does not assert an unattended API,
cryptographic host identity, or termination of every writer. Use `unsupported`
when running cancellation has not been established for the selected host.

`native lifecycle controller add` and `native lifecycle bridge add` take explicit
owner config, project, new private connection and credential paths. They require
a configured lifecycle policy and reserve both0600 outputs before enrollment.
The controller receives normal swarm actions plus `dispatch.cancel`; the bridge
receives `read`, `dispatch.correlate`, `dispatch.observe`. Old actor grants are
not expanded. These actors receive neither worker acquisition nor owner
provisioning/revocation authority. Separately enrolled workers still acknowledge
through their own credentials.

## Exact binding and observation

`native lifecycle observe|cancel|cancel-ack|status --connection <private-profile> --request <private-request> --json`
uses an independently authenticated profile. There is no ambient identity and
no fresh dispatch capability requirement for late lifecycle reports.

All mutations carry `OperationKey` and `Binding`. Obtain the binding from
lifecycle status after dispatch correlation or worker acknowledgement. It includes
`DispatchID`, `IntentID`, `BridgeSessionID`, `WorkerSessionID`, canonical `TaskID`,
`Checkout`, `BaseCommit`, `HostAgentID`, `ReservationFence` and, after worker
acknowledgement, exact `AttemptID`, `LeaseID`, `LeaseFence`. Host IDs are opaque
correlation, never credentials. Altering any bound value refuses the request.

Observation requests additionally carry `ExpectedSequence` (initially0), a
strictly increasing positive `HostSequence`, `State`, `Source`, `SourceTime` and
`EvidenceDigest` (`sha256:<64 lower-case hex>`). States are `running`, `unknown`,
`completed`, `failed`, `cancelled`. The assigned bridge supplies source time and
an evidence digest; DACLI assigns `ReceivedAt`, sequence and cooperative
assurance. Exact authenticated operation retries recover immutable history,
including after restart. Altered retries, older host sequences, earlier source
timestamps and transitions after a terminal observation are refused. Terminal
reports remain historical even when stale; they never release a lease.

Status accepts `{"DispatchID":"..."}`. It returns the complete observation
ledger, exact binding, cancellation independently, and `Freshness` as `missing`,
`fresh` or `stale`. No heartbeat means `unknown`. A stale nonterminal heartbeat
becomes `unknown`, never completed or safely terminated. DACLI does not invent
spend or usage from missing reports. Source timestamps are cooperative claims;
receipt time records when DACLI received them. No private host API is inferred.

## Cancellation and retained protection

Cancellation requests carry only operation key and exact binding. Explicit owner
policy plus the assigned controller's current scoped `dispatch.cancel` authority
are required. Before returning any host-action permit, the same durable
transaction withdraws future authority for the bound worker and descendants on
the canonical task. Unrelated tasks remain valid; the broad worker session is
not revoked. A new checkout, attempt ID or delegated child cannot restore the
cancelled task's authority. RegisterAttempt, lease acquire/renew/release,
revision, evidence, trusted verification, review and publication all observe the
withdrawal, including current-lease and current-revision predicates.

Only the first durably persisted cooperative request may return
`HostActionAuthorized:true`. Replays return history and false. Storage errors
return no permit or committed-looking observation. Unsupported channels return
`unsupported`; missing host correlation returns `manual_handoff_required`.
Existing unconverted reservations and unreleased worker leases remain protected,
including expired or revoked writers. Cancellation never marks them released or
converted. Recovery/reclamation belongs to separately owner-authorized core595.

`cancel-ack` additionally carries `ExpectedSequence` (cancellation starts1),
`State` (`acknowledged`, `dispatch_unknown`, `unsupported`) and `EvidenceDigest`.
Only the assigned currently authenticated bridge can record this result.
Acknowledgement is separate from terminal outcome: a late completion can remain
`completed` even after cancellation acknowledgement. Neither observation nor
acknowledgement restores worker authority or frees resources. Late assigned
bridge reports remain admissible after controller/worker revocation; revoking
or expiring the bridge itself prevents further reports.

Plugin MCP lifecycle tools are deferred to core593. The local CLI and application
services use this exact kernel; no alternate ledger or harness wrapper exists.
