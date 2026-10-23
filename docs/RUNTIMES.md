# Native hosts and removal of runtime adapters

The current source checkout contains a native governance preview. It does not
contain the earlier provider CLI launchers or runtime adapter presets. The
latest stable published release remains v0.3.1; its archives and `@latest`
installation retain the legacy CLI and do not contain this preview. See the
[native migration guide](NATIVE_MIGRATION.md) for the source-versus-release
boundary and the full removed-command map.

Native hosts create and execute coding turns. dacli supplies canonical task and
context records, owner-enrolled sessions, attempts, fenced leases, revision
receipts and bounded controller state. An optional native controller can
authorize a specific host action through its typed catalog; it does not start a
provider CLI. The selected host executes that action. These gates are
cooperative: direct host shell and filesystem work is outside them.

## Current integration and qualification

Use the [Codex plugin](../plugins/codex/README.md) with an explicit private
native session connection. The plugin starts `dacli native serve` and exposes a
typed catalog without a generic CLI or shell escape. Owner provisioning,
enrollment, revocation and schema migration are local-only; see the
[native transport](GOVERNANCE_NATIVE_MCP.md).

Codex is the only host with a recorded actual native multiworker qualification:
Codex CLI 0.163.0-alpha.11, using separately provisioned worker, bridge, runner
and reviewer connections delivered manually. That receipt establishes the
recorded workflow for that version and setup, not a guarantee for every Codex
version or host action. Automatic child profile delivery, provider billing and
usage enforcement, stable host dispatch lookup and running-turn termination
remain unverified. Antigravity packaging and deterministic backend tests exist,
but no authenticated Antigravity task/review turn was completed; Antigravity is
unqualified. See the [qualification record](../contracts/governance/v1/qualification-2026-10-23.md)
and [Codex receipt](../plugins/codex/receipts/native-swarm-0.163.0-alpha.11.md).

## Removed command compatibility

The removed root paths cover launch, supervision, adapter management,
verification panels, process control, claim expansion and private guardian or
watchdog entry points. The [native migration guide's compatibility table](NATIVE_MIGRATION.md#removed-command-map)
maps every tested removed path to the closest scoped workflow or marks it
unsupported. It does not describe a drop-in provider launcher.

`dacli agent spawn` is a different, still-registered legacy workspace identity
operation: it records an agent identity and token. It does not start a process,
create a native session, enroll a child, or grant native authority. Do not use
its token with the native plugin.

## Credentials and independence

Provider login lets a host access its model; it does not authenticate a dacli
principal. A role, model name, host session label, child ID or UUID is not a
credential. Every native MCP call reauthenticates its explicitly selected
session. Agents sharing one MCP connection share that session identity. A host
label or controller receipt cannot make those agents independent reviewers.
Independent review requires a separately enrolled eligible credential family
and owner-issued grant; see [native review](GOVERNANCE_NATIVE_REVIEW.md).

## Historical records and recovery

Existing `.dacli/runtimes` files, legacy agent/run records, usage reports and
probe receipts remain available for historical attribution and recovery. The
current `agents`, `runs list/show`, `logs` and `replay` readers reconstruct
recorded history; they do not revive a launcher. Historical acquisition
receipts and process records are not live native credentials, do not authorize
dispatch, and do not prove that a process is still running or has stopped.
Preserve receipt bytes and original operation identities.

For storage cleanup, run `dacli cleanup --dry-run` and apply only the exact
reviewed safe plan. Unknown liveness, divergent files, referenced evidence or
missing recovery evidence remain protected refusals. Expiry, revocation,
cancellation requests and stale heartbeats alone do not prove termination or
authorize releasing a possibly active writer's resources. Follow the exact
owner recovery procedure documented for the retained state; do not reset
private authority or make a new operation key to repeat an uncertain action.

The [version-specific Codex probe report](CODEX_GOVERNANCE_PROBE.md) describes
observed versions and measurements. An old installed or externally copied dacli
skill may still prescribe legacy launch commands or `DACLI_AGENT`; refresh it
from the current [`skills/dacli/SKILL.md`](../skills/dacli/SKILL.md), then check
`dacli version --compatibility --json` and `dacli capabilities --json` before
using its command guidance. The checked-in skill describes the native workflow.
