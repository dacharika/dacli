# Cooperative Codex dispatch bridge (core/590)

The version 1 bridge consumes a reserved native task through an owner-authorized
manual provisioning channel. The calling Codex agent uses its host's native
collaboration tool after DACLI has persisted an intent. DACLI does not start a
coding CLI, create app-server model turns, or assume private child configuration
injection exists. This adapter joins a durable authority protocol to the host
agent's supported collaboration surface; it is not an unattended public spawn API.

## Measured capability

Run `python3 docs/probes/codex_native_dispatch_probe.py --help` for the version 1
diagnostic probe. Its receipt records the actual installed binary/version and
protocol hashes, method recognition, thread configuration declarations and
observation time. The measured Codex 0.159.2 protocol does not declare
`agent/spawn` or `collaboration/spawn_agent`; isolated read-only requests produced
unknown-method refusals. Native collaboration in the current agent host requires
separate owner attestation. Private child connection delivery, stable operation
lookup, and cancellation of a running turn remain unverified. Declaration of a
configuration field is not evidence of child delivery. Diagnostics do not create
threads, request model turns, or read worker credentials.

A future declared spawn method is reported as unverified without executing it.
Coherent unsupported and declared-but-unexecuted receipts both permit the
explicitly attested manual channel. Neither authorizes unattended execution.
Unsupported delivery is recorded as `manual_handoff_required`; uncertain delivery
or acknowledgement is `dispatch_unknown`. Both retain the original reservation.

## Explicit owner preparation

`native dispatch upgrade --config <private-owner-config>` explicitly migrates
schema 7 to 8. All sessions, task bindings, attempts, leases, evidence, reviews,
publication records, policies, reservations and immutable receipts are preserved.
Current worker, verifier, reviewer and swarm factories support existing schemas
6/7 and explicitly upgraded 8 where their respective features are available.
Genuine old schema 6/7 writers refuse 8. Startup never performs this migration.
Missing schema 8 maps and premature schema 8 records in older payloads fail closed.

Prepare a private schema 2 capability profile using the planning profile fields:

```json
{
  "schema_version": 2,
  "workspace_id": "<workspace>",
  "project_id": "<project>",
  "harness": "codex",
  "source": "owner-manual-native-handoff",
  "observed_at": "<current UTC time>",
  "expires_at": "<within one hour of observation>",
  "codex": {
    "schema_version": 1,
    "probe_file": "<absolute private 0600 copy of actual diagnostic receipt>",
    "probe_sha256": "<64 lowercase hex SHA-256 of that exact file>",
    "native_host_attestation": "owner-observed-native-collaboration",
    "child_profile_channel": "owner-manual-separate-process"
  }
}
```

The owner pins `sha256:<profile file SHA-256>` in the swarm policy before planning
and reservation. Profile freshness is at most one hour; probe freshness is at
most 24 hours. Probe identity, required fields, binary/protocol/schema hashes and
coherent diagnostic statuses are validated. The profile is cooperative owner
attestation, not cryptographic attestation of the host or its installed version.
Keep both files private and refresh policy revision when changing the profile.
Schema 1 remains usable for existing planning/manual handoff but cannot authorize
a new dispatch intent.

Owner-enroll the expected worker with `native enroll`, its exact canonical task
and resource paths, and a distinct connection/credential file. Its token remains
in the private credential file. Owner-enroll a distinct bridge:

```
dacli native dispatch bridge add --config <owner-config> --project <project> \
  --connection <new-private-bridge-connection> --credential <new-private-credential>
```

Bridge scope contains only `read` and `dispatch.correlate` for configured canonical
tasks and paths. It cannot enroll, acquire leases, acknowledge as a worker, or
request launches. Both expected worker and bridge must be independently owner
issued root sessions, distinct from the controller and each other. Delegated
sessions cannot substitute for this provisioning contract. Provisioning reserves
both output files before enrollment and retains safe session/path information
if credential persistence fails after issuance.

## Native host protocol

All following commands require `--connection <private-profile>`,
`--capability-file <private-schema2-profile>`, `--request <private-json-file>` and
`--json`. They accept no credential, command, grant, role or harness override.

1. Controller calls `native dispatch request` with `OperationKey`, `DispatchID`,
   `ExpectedFence`, `BridgeSessionID`, `WorkerSessionID`, and `PromptSpecDigest`
   (`sha256:<64 lowercase hex>` of the reviewed prompt specification).
   This rechecks admission, exact checkout/base, resources, policy, capacity,
   capability and current enrolled scopes under the workspace authority lock.
   Before returning `HostActionAuthorized:true`, it durably records schema 1
   `dispatch_requested`, sequence 1, the immutable reservation and bindings.
   No successful-looking permit is returned on persistence failure.
2. The calling agent consumes that first permit through the host's native
   collaboration tool. Supply the task and designated private connection-file
   path to the child, never credential bytes. The host's returned canonical ID
   (including `/root/...` identifiers) is opaque correlation, not a file path or
   authenticated child identity. No CLI fallback is permitted.
3. The separately authenticated bridge calls `native dispatch correlate` with
   `OperationKey`, `DispatchID`, `IntentID`, `ExpectedFence`, `ExpectedSequence`,
   `State` and `HostAgentID`. `host_correlated` requires a bounded nonempty opaque
   host ID. `manual_handoff_required` and `dispatch_unknown` require an empty ID.
   Each observation atomically advances the sequence and has an immutable receipt.
4. The child uses its designated connection in a separate plugin bootstrap/MCP
   process, authenticates its expected session and registers its real clean
   checkout attempt. Worker calls `native dispatch acknowledge` with
   `OperationKey`, `DispatchID`, `IntentID`, `AttemptID`, `HostAgentID`,
   `ExpectedFence` and `ExpectedSequence`. Exact assigned worker, attempt, task,
   checkout/base and current admission are checked. One authority transaction
   persists `acknowledged`, its next sequence and the converted worker lease.
   No worker may edit before that current lease exists.
5. `native dispatch status` accepts `{ "DispatchID": "..." }` for an authenticated
   assigned participant with read/task scope. Status is observation, not a launch
   permit or successful task completion. Its nested lease is the historical
   conversion receipt; release/renewal must be evaluated against current lease
   authority rather than inferred from that receipt. Completion remains the existing
   verification/review/publication workflow; cancellation/recovery are later work.

## Replay, loss and reconciliation

There is one intent per reservation, independently of request keys. An altered
payload or a second launch key is refused. An exact authenticated replay returns
its historical immutable receipt with `HostActionAuthorized:false`, even when
fresh capability has since disappeared. Historical observation and acknowledgement
receipts likewise do not reexecute. A revoked/expired caller cannot replay.
Persistence failure returns no committed-looking intent or lease.

A lost first response cannot safely be replaced with another launch. Inspect the
existing intent and reconcile through the bridge. Unknown/manual states may
advance to `host_correlated` for the original located execution using the current
sequence and fresh capabilities; this never grants another host action. A stale
sequence, changed correlation, state regression, changed expected worker, or
unassigned credential is refused. Unknown resources remain protected even after
expiry or revocation. This slice does not provide owner resource release or prove
an unknown process stopped.

Once an intent exists, the older `native swarm handoff` path is refused for that
reservation. It cannot bypass assigned worker/bridge acknowledgement. Existing
589 handoff remains available for reservations without an intent.

## Limits

Native host labels and shared MCP connections cannot prove independent identities.
The supported channel is explicit owner provisioning plus separate authenticated
processes and cooperative host correlation. There is no cryptographic host-child
binding, automatic child MCP override, stable host dispatch lookup or verified
running cancellation. External host calls are outside the authority transaction;
intent ordering and conservative replay prevent authorization of a duplicate,
not exactly-once host execution. Canonical Markdown edits, capability file changes
and direct host filesystem writes do not share the authority lock; fresh checks
are cooperative observations, not OS containment or serializable external files.
