# Governance support matrix

This matrix separates executable local governance from host observations,
structural proposals and hosted metadata transport. Its repository baseline is
`f4996c5ac007e98cdf41393d6732010a3b8f619e` (2026-10-25). A source contract,
fixture, package, or successful identity call is evidence only for the surface
it actually exercises.

## Supported surfaces

| Surface | Status and actual interface | Required setup and schema | Evidence and limit |
|---|---|---|---|
| Local CLI | Implemented owner and operator commands are registered in [`nativegovernance.go`](../internal/features/nativegovernance/nativegovernance.go) and dispatched with declared mutability and authentication in [`cli.go`](../internal/cli/cli.go). Owner setup includes `dacli native bootstrap --config OWNER`, `dacli native enroll --config OWNER ...`, and `dacli native serve --connection CONNECTION`; owner recovery includes `native revoke` and explicit `native ... upgrade` commands. | Owner JSON uses `schema_version: 1` and a private owner credential reference. Worker connections use `schema_version: 1`; actor scope is issued by the owner and bound to exact workspace, task, repository and optional paths. See [native MCP setup](GOVERNANCE_NATIVE_MCP.md#private-owner-setup). | [`native_governance_test.go`](../internal/cli/native_governance_test.go) and [`nativegovernance` tests](../internal/features/nativegovernance) exercise local command and authority behavior. Owner commands do not spawn coding agents or automatically migrate authority. |
| Native MCP | Implemented local typed stdio server. `initialize`, `tools/list`, and `tools/call` use MCP `2025-06-18`; tool arguments have strict `schema_version: 1` envelopes and role-specific catalogs. Worker, runner, reviewer, and orchestration selections are distinct. | Select a private connection profile; optionally select one runner, reviewer, or orchestration profile. Their selection keys are `runner_config_file`, `review_config_file`, and `orchestration_config_file`. Review, runner, and orchestration authority is separately enrolled by the owner. Discover exact required fields with `tools/list`. | [MCP contract](GOVERNANCE_NATIVE_MCP.md#protocol-and-tools), [plugin catalog/setup](../plugins/codex/README.md), [`native_test.go`](../internal/mcp/native/native_test.go), and [orchestration smoke path](../plugins/codex/scripts/smoke.py). This is cooperative local authority, not host or OS containment. |
| Codex host | The native plugin and MCP transport are packaged. Current host-specific evidence is a measured `codex-cli 0.164.0-alpha.11.1` local app-server diagnostic, a separate child stdio probe, and an eight-case deterministic tool-hook fixture. | Install/select the private native MCP profile described in the [plugin guide](../plugins/codex/README.md). The child probe requires its own owner-provisioned profile; labels and thread IDs do not supply identity. | [Current qualification](codex-governance-qualification-2026-10-25.md), [host diagnostic receipt](../plugins/codex/probes/receipts/0.164.0-alpha.11.1/native-host-diagnostic.json), [child receipt](../plugins/codex/probes/receipts/0.164.0-alpha.11.1/native-child.json), and [hook matrix receipt](../plugins/codex/probes/receipts/0.164.0-alpha.11.1/native-hook-matrix.json). The loopback fixture qualifies only its listed local tool-pipeline cases; it makes no model request. The direct diagnostic path entered no callbacks. Automatic child-profile delivery, cryptographic host binding, provider usage enforcement, stable dispatch lookup, and running-turn termination remain unverified. |
| Codex multiworker history | A separate, historical actual native swarm run was recorded for Codex CLI `0.163.0-alpha.11`; it remains useful version-specific evidence, not a current-version guarantee. | Its workers, bridge, runner, and reviewer had separately provisioned profiles; delivery was manual. | [Historical swarm receipt](../plugins/codex/receipts/native-swarm-0.163.0-alpha.11.md) and [qualification record](../contracts/governance/v1/qualification-2026-10-23.md). Do not combine it with the 0.164 hook fixture as if both were one run. |
| Antigravity | Package and shared stdio conformance entrypoint exist. Live Antigravity governance is **deferred and unqualified**: there is no authenticated task/revision/review turn or versioned live host receipt. | Package/install metadata uses schema 1. The shared conformance fixture uses private, separately owner-issued selections and tool arguments with `schema_version: 1`; the selected server performs actual authentication. | [Antigravity limits and setup](../plugins/antigravity/README.md), [shared probe entrypoint](../plugins/antigravity/probes/conformance.py), [shared conformance contract](../contracts/governance/v1/README.md#shared-native-stdio-conformance-core573), and [package tests](../plugins/antigravity/tests/test_package.py). Package or backend tests do not qualify a live Antigravity host. Hooks/containment and dispatch/model routing are unsupported; cancellation is unverified. |
| Hosted control plane and project sync | Source includes a development API/worker composition and local signed metadata sync. The local workspace remains authoritative; remote records are proposals in the inbox. | `controlplane/v1` and sync route/binding/envelope use their documented closed schema 1 contracts. The operator explicitly selects exact tenant/project/environment/repository bindings with `dacli project connect`, then invokes `dacli project sync`, `status`, or `disconnect`, as registered in [`planning.go`](../internal/features/planning/planning.go). | [Control-plane status](CONTROL_PLANE.md), [API routes](CONTROL_PLANE_API.md), [project sync](PROJECT_SYNC.md), and [`controlplane/v1`](../contracts/controlplane/v1/README.md). Controlled local providers and PostgreSQL qualification do not establish a deployed hosted service, production provider, SLO, or hosted task acceptance. Inbound data cannot mutate local tasks, grant capability, start agents, accept work, or deploy. |

## Contract-only governance/v1

[`contracts/governance/v1`](../contracts/governance/v1/README.md) is a
structural lifecycle proposal with a closed record model and snapshot validator.
It checks references, versions, workspace consistency, digests and lifecycle
predecessors. It does not authenticate credentials, establish current policy,
atomically fence a live writer, stop a host process, or prove execution. Its
synthetic authenticated-host fixture is not a host receipt. The implemented
authenticated native authority described above is a separate local system with
its own versioned storage, private profiles, owner-only migrations and
tool-specific evidence paths; schema names shared between the two do not imply
wire or storage compatibility.

## Authority and profile versions

The qualified Codex probe read a live authority at storage `Version: 10`; see
the [current report](codex-governance-qualification-2026-10-25.md#measured-build-and-evidence).
This is an observation from that run, not a recommendation to migrate another
authority to version 10. The authority storage `Version` is distinct from the
`schema_version` fields on MCP arguments, connections, capabilities, policies,
receipts and proposal contracts.

Current source has more than one compatibility boundary:

- [`OpenCurrent`](../internal/governance/sessions/sessions.go) accepts stored
  authority versions 6 through 22. The older `Open` reader remains capped at
  19 so pre-schema-20 binaries refuse schema-20 role provenance.
- The native compatibility/doctor range is currently declared as 6 through 19
  in [`compatibility.go`](../internal/governance/sessions/compatibility.go) and
  [`doctor.go`](../internal/features/nativegovernance/doctor.go). This published
  range does not yet include the schema-20 reader, while role-registry
  selection specifically requires schema 20 in [`swarms.go`](../internal/governance/sessions/swarms.go)
  and the [role-registry owner implementation](../internal/application/native_role_registry_owner.go).
  Treat this as a compatibility-reporting gap; do not infer that every native
  feature is available at every accepted storage version.
- Features have their own minimum storage versions and explicit owner-only
  migrations. For example, [`native controller upgrade`](GOVERNANCE_NATIVE_CONTROLLER.md#owner-setup-and-explicit-migration)
  migrates 15 to 16, [`native swarm upgrade`](GOVERNANCE_NATIVE_SWARM_PLANNING.md#authority-and-schema-migration)
  migrates 6 to 7, and [`native dispatch upgrade`](GOVERNANCE_NATIVE_DISPATCH_BRIDGE.md#explicit-owner-preparation)
  migrates 7 to 8. These commands authenticate the private owner and preserve
  existing maps/history. There is no universal automatic upgrader or profile
  replacement. Use the exact migration documented for the subsystem and retain
  the existing authority and profiles for owner reconciliation.

## Evidence boundaries that apply across hosts

- `submit_evidence` records a **self-report**. Trusted verification requires a
  separately owner-enrolled runner with named commands/grants; independent
  review requires a separately enrolled reviewer. See [native evidence](GOVERNANCE_NATIVE_EVIDENCE.md),
  [native review](GOVERNANCE_NATIVE_REVIEW.md), the [typed MCP tool handlers](../internal/mcp/native/tools.go),
  [runner profile checks](../internal/application/native_verification_profile.go),
  and [`nativegovernance` tests](../internal/features/nativegovernance).
- Direct app-server RPC, hook inventory and startup notifications do not prove
  that a tool hook ran. In the current Codex report, the diagnostic path had no
  callback entries; the loopback fixture separately measured observe, deny,
  disabled, untrusted, malformed, timeout, error, and unsupported-callback
  cases. An unentered callback is unexercised, not a successful enforcement
  result.
- Cancellation requested, cancellation acknowledged, and process termination
  observed are separate facts. Codex automatic termination is unverified;
  Antigravity has no automatic host cancellation guarantee. A missing heartbeat
  is unknown, not proof of cessation. Keep the original operation identity and
  lease protection for owner reconciliation. See [native swarm lifecycle](GOVERNANCE_NATIVE_SWARMS.md)
  and [Antigravity limits](../plugins/antigravity/README.md#assurance-differences-and-pending-evidence).
- Hosted signed records remain proposals until the local authority separately
  verifies and applies an allowed action. A remote acceptance-looking field or
  transport acknowledgment is not local task acceptance.

## Reproducible source checks

From the repository root, inspect the tool catalogs and the linked contract
checks without opening or changing any private authority:

```sh
go test ./internal/governance/contracts ./internal/governance/sessions ./internal/mcp/... ./internal/features/nativegovernance ./internal/cli ./internal/features/planning
python3 -m unittest discover -s plugins/codex/probes -v
python3 -m unittest discover -s plugins/antigravity/tests -v
```

The source checks establish the committed contracts and fixtures only. To
inspect a real selected profile, an owner must supply that profile to
`dacli native serve` or the documented host-specific probe; never edit a live
authority to make a version or host appear supported.


### Capacity facts and current guidance

#1228 adds exact21 host-capacity facts and an explicit owner20→21 upgrade. Current source selection is6–22, while legacy compatibility/doctor authentication remains deliberately bounded6–19 and signed-role exact20 readers retain their own boundary. Unknown21 fields and unknown20 migration input refuse. New owner/scoped capacity commands do not activate live21 or admit additional workers. The live workspace remains10.

#1213 diagnoses reviewed instruction bytes and declared capability manifests. A supplied manifest is unverified runtime metadata; fresh process and cached connection state remain unknown until separately observed. Both refresh preview and apply require explicit owner configuration and are local-only. File replacement preserves checked private backups and recovery identity; it does not reconnect a host. No current App Server or Antigravity qualification is inferred.


#1229 adds exact22 host-slot admission, selected by explicit owner21→22 migration.
Claims commit with original dispatch reservations and are checked again before
launch, correlation and handoff. Version2 observations require an exact local
execution census so observed and reserved occupancy are not counted twice.
Supported terminal observations can free execution capacity while retaining
writer/lease/path protection. Heartbeat loss, expiry, STOP and cancellation
request or acknowledgement do not free either protection by themselves.

This is cooperative serialization within one selected authority and executor
host-session. Shared-host scope across authorities is unsupported; no global
host primitive, unattended driver, additional workers or paid capacity is
qualified. The live authority remains schema10. Rolling admission and the
host-effect driver remain #1218; live qualification remains #1217.

#1230 adds [bounded queue diagnostics](GOVERNANCE_NATIVE_QUEUE.md) through the
native CLI and a separate operator-only MCP catalog. It reads exact schemas6–22
without migration; below22 host rows are omitted and task reasons report
`host_capacity_unsupported`. Full
selected-scope stage counts precede bounded row truncation. No queue scheduler,
host effects, provider billing measurements or live twelve-worker qualification
are implied. Existing workspace readers do not acquire operator permission.
