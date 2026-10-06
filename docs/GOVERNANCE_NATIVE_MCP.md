# Native governance MCP

`dacli native serve --connection /absolute/private/connection.json` exposes
native session services directly. It does not wrap an agent CLI, dispatch the
legacy CLI executor, spawn providers, or require model authentication. It ignores
`DACLI_AGENT`. Missing/invalid explicit authority is a refusal, never local root.
The older `dacli mcp serve` remains a separate compatibility surface.

This authenticates a scoped session credential under cooperative local-file
integrity. It does not authenticate a human, host, or independently isolated
subagent, and it cannot prevent direct filesystem actions. Each request,
including catalog discovery and ping, reloads and authenticates the configured
session. Workspace reads additionally require `read` and the exact
`workspace:<workspace-id>` resource. Mutation methods enforce current scope,
attempt ownership, lease/fence and revision through the existing authority.
The connection is pinned at startup: changing its bindings requires restart;
credential files are read afresh. No credential or authority hash is returned.

## Private owner setup

Create a private directory outside tracked repository files. Owner provisioning
is local-only: neither native tools nor the legacy MCP CLI escape hatch expose
these commands. Owner configuration must be a regular OS-user-owned file with
mode 0600 or stricter, containing only:

```json
{
  "schema_version": 1,
  "workspace": "/absolute/workspace",
  "authority": "/absolute/private/authority.json",
  "installation_id": "owner-selected-installation",
  "owner_credential_file": "/absolute/private/owner.token"
}
```

The workspace must already exist at that exact root. Parent directories must
exist. Initialization never replaces an existing authority or credential:

```sh
dacli native bootstrap --config /absolute/private/owner.json
dacli native enroll --config /absolute/private/owner.json --task project/001 \
  --connection /absolute/private/session.json \
  --credential /absolute/private/session.token --path src --expires 1h
```

Enrollment reads the existing canonical task and repository, binds permission
to record attempts, and issues workspace reads plus task-scoped registration,
lease, revision and self-report actions. `--path` is repeatable and adds exact
repository-relative path resources; it does not grant a wildcard. Omitting paths
still permits task-only leases. Canonical readiness is checked later through
`application.NewLeaseAdmission`; recording admission is not task readiness.
Enrollment does not spawn an agent or mint trusted runner/reviewer authority.
Use distinct credentials and MCP instances for independently acting sessions.
For the separate local trusted-verification workflow, owner enrollment may opt
into `--verification` to grant `verification.request`. The default MCP catalog
remains unchanged; an owner may explicitly select a private runner at server
startup to enable named asynchronous verification. See
[private named verification setup](GOVERNANCE_NATIVE_EVIDENCE.md#private-local-verification-setup)
for owner runner provisioning and the local `native verify` adapter.

```sh
dacli native serve --connection /absolute/private/session.json \
  --runner-config /absolute/private/runner.json
```

The runner is separately enrolled with exact named grants. Its profile must
match the worker's physical authority, workspace, installation, task and
repository. Configuration is pinned at startup and validated afresh for
verification calls; commands and credentials stay in private owner files.
Malformed, replaced, revoked or mismatched configuration refuses. Tools cannot
select a different runner, command, directory, credential or grant.

Owner and session secrets are written only into newly created private files,
never argv, stdout, tool arguments or diagnostics. Existing output paths refuse.
Config and credential reads reject final-component symlinks, nonregular files,
other owners, permissive modes and oversized contents. Verified POSIX adapters
are required; other platforms fail closed. Protect parent directories against
other local writers. These checks are cooperative hygiene, not hostile-user
confinement. If issuance is interrupted or a private file write fails, preserve
all authority and private files for owner reconciliation; never retry by resetting
storage. The commands do not silently migrate old schemas. Use the exact owner migration
below before opening a schema-5 authority with the current binary.

Enrollment writes this connection shape (the secret stays in a separate file):

```json
{
  "schema_version": 1,
  "workspace": "/absolute/workspace",
  "authority": "/absolute/private/authority.json",
  "workspace_id": "workspace-id-from-storage",
  "installation_id": "owner-selected-installation",
  "session_id": "issued-session-id",
  "credential_file": "/absolute/private/session.token"
}
```

## Explicit schema-5-to-6 upgrade

For an existing schema-5 profile, preserve the owner configuration, authority,
connection and credential files. Run this local owner command once:

```sh
dacli native upgrade --config /absolute/private/owner.json
```

It authenticates the explicit private owner credential and migrates exactly
schema 5 to 6 through `Authority.UpgradePublication`. Existing sessions,
attempts, leases, revisions, evidence, reviews and credential files survive.
Restart the native MCP process using the same connection and credential.
An already-schema-6, unsupported or malformed authority refuses with exit 3 and
preserves authority bytes. Missing or forged owner files also refuse. Preserve
storage for owner reconciliation; never bootstrap, reset or downgrade it as a
retry. Older schema-5 binaries cannot write the upgraded authority.
This command has no token arguments, is excluded from both MCP surfaces, and
never takes migration authority from `DACLI_AGENT`. Older source schemas need
their separately documented owner API migrations; this command is not a generic
version selector or an automatic startup upgrade.

## Owner recovery and revocation

Revoke the exact `session_id` recorded in the private connection file:

```sh
dacli native revoke --config /absolute/private/owner.json --session <session-id>
```

This command requires the explicit private owner credential, independently of
`DACLI_AGENT`. It exposes no credential flags and prints no credential. Missing,
forged, or unavailable owner authority and unknown session IDs refuse with exit
3. Revoking an already-revoked session is idempotent. The command is local-only
and excluded from both MCP surfaces. It preserves credentials and connection
files for reconciliation; it does not stop a host process or undo prior work.
An already-running server refuses its next discovery or tool request after
revocation. For recovery, enroll a new scoped session into new private files and
restart the client with that connection; never reset authority storage.

## Protocol and tools

Use MCP `2025-06-18` over newline-framed stdio. Initialize before discovery or
calls. If the client offers another version, initialization responds with the
supported `2025-06-18` version so the client can decide whether to continue.
Optional protocol `_meta` (including `progressToken`) is accepted and ignored;
it is never an identity or credential. Tool argument objects remain strict.
Request IDs are non-null strings or integer numbers. Incoming frames are
bounded to 1 MiB. Supported methods are `initialize`,
`ping`, `notifications/initialized`, `tools/list`, and `tools/call`.

The default tools also include `get_policy_status`, alongside `identity`, `workspace_status`, `task_list`, `task_get`,
`register_attempt`, `acquire_lease`, `renew_lease`, `release_lease`,
`publish_revision`, and `submit_evidence`. Discover their typed schemas rather
than guessing arguments. Every tool requires `schema_version: 1`; unknown
fields, absent required fields and unsupported versions refuse. Lifecycle tools
use a typed `request` object matching the existing shared request field names
(for example `OperationKey`, `AttemptID`, `LeaseID`, `ExpectedFence`). Time values
are RFC3339 strings. Scope and operation identity are derived from credentials;
callers cannot supply a replacement authenticated session.

With an explicitly configured runner, the catalog additionally advertises
`request_verification`, `get_verification_status` and `cancel_verification`.
All three require `schema_version: 1` and the same exact `request` object:

```json
{
  "operation_key": "owner-check-for-revision-1",
  "attempt_id": "registered-attempt-id",
  "lease_id": "current-lease-id",
  "revision_id": "published-revision-id",
  "check_id": "owner-configured-check-name",
  "expected_fence": 1
}
```

Their typed result contains `operation_key`, `state` and nullable `evidence`.
States are `absent`, `admitting`, `reserved`, `cancellation_requested`,
`completed` and `reconciliation_required`. Admission is not proof of process
start; reservation is durable but is not proof of a live process. Only a
completed receipt records an outcome; inspect its exit code and provenance.
Failed checks can have completed receipts with nonzero exit codes.

Jobs run outside the protocol reader. Two active jobs are admitted without a
queue; capacity is checked before reserving another execution. Exact duplicates
recover the same in-process state or durable receipt rather than executing
again. Status and cancellation authenticate the original caller and full
operation binding without reserving work. Completed receipts are historical
and do not extend lease eligibility. A pending reservation after restart is
`reconciliation_required`; preserve its key and configuration for the owner.
Do not use a new key to rerun an uncertain operation.

Up to sixteen tool calls are handled concurrently with serialized output.
Responses may arrive out of order; clients must match JSON-RPC request IDs.
Excess in-flight calls refuse rather than creating an unbounded queue. EOF,
context cancellation and transport failures cancel owned jobs and wait for
bounded teardown; incomplete durable outcomes remain available for recovery.

Cancellation requests target the exact in-process job. They do not fabricate a
successful receipt or erase a reservation. A committed receipt wins a race with
cancellation; otherwise interrupted reserved work remains uncertain. Native
checks use owned POSIX process groups with bounded pipe draining to terminate
cooperative children on cancellation/deadline and clean up background children
on completion. Unsupported platforms refuse execution. Children that leave the
group deliberately are outside this cooperative guarantee.

Progress is represented by bounded status snapshots. This transport does not
send progress notifications after a submission response has completed. Protocol
`notifications/cancelled` is distinct from cancelling a durable verification job;
use the typed cancellation tool and inspect status for that purpose. See the
[MCP progress](https://modelcontextprotocol.io/specification/2025-06-18/basic/utilities/progress)
and [cancellation](https://modelcontextprotocol.io/specification/2025-06-18/basic/utilities/cancellation)
specifications for request-scoped notification semantics.

Successes and refusals have tool-specific object output schemas and are checked
against the emitted schema before returning. `structuredContent` and the JSON
text fallback represent the same object. Refusals contain a stable `refused`
code and `exit_code: 3`, without internal storage diagnostics. An unchanged
refusal must not be retried. Session authentication failures at the protocol
boundary use JSON-RPC error `-32003`; malformed requests use protocol errors.
Session-file startup failures exit 3 before processing input.

There is no CLI/shell escape hatch, owner enrollment, policy/grant administration,
provider spawning, acceptance or unconfigured verification execution in this
catalog. Self-report evidence always stays self-reported. Trusted local
verification is available only through a separately owner-configured runner,
whether invoked with the local adapter or optional asynchronous tools.
An explicitly selected `--review-config` enables a separate reviewer connection
instead of worker mutations or runner jobs. Its catalog contains four read tools
plus typed `propose_review` and `evaluate_review`; see the
[native review setup and contract](GOVERNANCE_NATIVE_REVIEW.md#owner-provisioning-and-native-tools).
Review configuration and runner configuration cannot be combined. The owner
provisions the reviewer and required named-check policy locally; requests cannot
mint credentials, choose their grant or supply policy. Historical proposals and
fresh eligibility remain distinct. Acceptance consumption and task acceptance
are not exposed. A successful protocol call is not proof of task acceptance,
strong host enforcement, independent reasoning or execution by Codex.

Tests use real committed Git fixtures and explicit private session files. They
exercise lifecycle bindings, dirty revision refusal, revocation during a server
lifetime, wrong scope/session/workspace, forbidden tools, private file rejection,
typed output/fallback parity and legacy-transport owner-command exclusion.

## Scoped policy observations

`get_policy_status` requires `schema_version: 1`, exact canonical `project` and
`task` IDs, and optionally `decision_id`. The enrolled session must have `read`
authority for the workspace and exact task. It resolves canonical identity and
reads current task/project binding and policy under fresh authority. It never
loads an owner credential or changes policy, decisions, approvals, consumption
or task state. Worker, configured runner and separately configured reviewer
catalogs expose this read operation.

The typed result always includes `effect_authorized: false`. States are
`unavailable`, `stopped`, `expired`, `denied`, `approval_required` and `allowed`.
Even `allowed` describes an observation, not permission to perform an effect.
Authority schemas predating policy storage and authorized tasks without policy
return `unavailable`; reads never migrate them or infer allow. Unknown or
cross-task decision IDs refuse.

Optional decisions are historical. `matches_current_policy` distinguishes their
policy snapshot from current owner configuration. Approval, consumption and
observed outcome appear separately; an unknown outcome never becomes completed
through observation. Prepared canonical bodies, private paths and credentials
are absent from this projection. Actual owner acceptance still revalidates its
existing policy and provenance gates.

Fixed native catalog and schema fixtures preserve version-1 field names and
types. The foundation catalogs contain eleven worker, fourteen configured runner
and seven reviewer tools. The explicit legacy MCP transport remains separate;
its CLI compatibility never becomes a native agent shell gateway. Named check
progress remains bounded polling, and typed cancellation remains distinct from
request notifications and host termination.

## Owner-selected native orchestration

`native serve --connection <private> --orchestration-config <private>` selects a
separate controller, bridge or worker-handshake role. Runner, reviewer and
orchestration selections are mutually exclusive. The plugin's private selection
forwards `orchestration_config_file`; neither MCP requests nor host labels can
change it. Configuration and pinned capability bytes are read again on every
request, and the exact enrolled identity/action/project scope is authenticated.
The profile contains no commands or credentials. Owner enrollment, policy,
grants, placements and capability setup remain separate administrative actions;
opening the server never upgrades the real authority.

Controller operations plan, start, step, read status, reconcile, stop and request
dispatch cancellation through the same application/admission/controller services
as other adapters. Scoped start binds the caller's session and owner-pinned
policy/routes before the shared controller transaction; it never loads an owner
credential. Start accepts only operation/invocation/checkpoint identities. Step,
reconcile and stop bind expected sequence. Dispatch correlation, worker
acknowledgement, lifecycle observation and cancellation acknowledgement use the
existing exact dispatch/intent/reservation/lease fence checks and original
independently enrolled actors. Strict role catalogs refuse nonadvertised tools.

A bridge may correlate and observe; a worker handshake connection may acknowledge
its own dispatch. These separately configured catalogs do not replace the default
worker, named runner or reviewer catalog. All responses preserve structured/text
JSON parity. The new host status observation explicitly reports cooperative
manual Codex delivery, unsupported automatic dispatch and unverified automatic
termination, with `effect_authorized: false`. Unsupported harness capability
formats refuse. The interface remains portable without inventing host support.

Stop is a durable terminal controller halt, not an assertion that host processes
terminated. It retains pending actions and occupied resources. Cancellation
request, authenticated host acknowledgement and host terminal observation remain
separate lifecycle facts. Stale/missing nonterminal observation remains unknown;
replay and status grant no host action. Recovery preserves uncertain bindings and
requires owner reconciliation. Existing controller bounds and clean/fenced
producer checks remain in force. The local integration producer is separate;
`pending_local_integration` cannot be relabeled completed. No parent loop or
integration completion is inferred from adding these tools.

Owner-configured difficulty routing selects an approved role/model for a new
native child turn. A selection and its explanation belong to the shared swarm
verdict/dispatch record and therefore to the MCP result. A caller supplies no
model override, provider credentials or private route profile. Routing must
respect eligible strategy, fresh preflight, declared host capability and budget
accounting. Historical fixed-role dispatches retain their original selection;
reading them never upgrades their provenance. Harder-model correction is a
bounded controller transition tied to the task, invocation, prior dispatch and
actual producer outcome. It must not replace a retained uncertain writer or
claim that a running model was switched. Host/provider model availability and
billing enforcement require separate observations and are not inferred here.

The native owner command registry delegates model-routing upgrade/configure and
local-integration operations to their shared adapters. These are administrative
commands with explicit owner configuration; they are not unrestricted MCP tools.
A local-integration permit must bind the configured source and target checkout,
committed revision, observations, operation identity and actual governed
verification. Inspect/apply/reconcile refer to those durable identities; neither
an integration claim nor a passed source check proves the landing target changed.
The controller continues to wait on `pending_local_integration` until the actual
integration producer supplies the required bound outcome. Unknown apply effects
require inspection/reconciliation, not a replacement operation key. Routing
selection grants only the next permitted child turn, preserving independent
worker identity, retained-writer protection and correction accounting.
