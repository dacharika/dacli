# Native migration and command compatibility

This guide describes the transition from dacli-managed provider processes to
native coding-host sessions governed through dacli. It applies to the current
source checkout. The latest stable published release is still v0.3.1; its
archives and `go install ...@latest` provide the earlier CLI. No tagged native
release exists. Build this preview from a reviewed checkout and use the
[Codex plugin setup](../plugins/codex/README.md) to connect it to the host.

## What changed

The current source removes provider CLI launchers, adapter presets and the
generic process supervisor. dacli does not start `codex`, `claude`, Antigravity
or another coding-agent CLI. A host creates and executes coding turns; dacli
stores canonical task state and authenticates scoped governance operations.

The native system includes owner-provisioned worker, runner, reviewer and
optional controller sessions. Workers register externally created Git
checkouts, acquire fenced task/path leases, publish observed revisions and
submit evidence. Self-reports remain self-reports. Trusted named checks and
independent review require separately enrolled connections and owner-issued
grants. A separately configured native controller can plan bounded work,
record a loop and authorize particular host actions through its typed catalog.
It does not launch a provider process or contain a host's shell and filesystem
activity. The host must perform the authorized action through its own native
collaboration tools.

One MCP connection represents one enrolled session. Several native children
using it share that identity; host labels, model names, transcripts and child
IDs do not create separate principals. The recorded Codex qualification used
manually selected, separately enrolled profiles. It does not establish
automatic profile delivery, provider billing enforcement, universal host
interception or process termination. Antigravity packaging exists, but its
authenticated task/review workflow is not qualified. See the
[versioned qualification record](../contracts/governance/v1/qualification-2026-10-23.md)
and [Codex receipt](../plugins/codex/receipts/native-swarm-0.163.0-alpha.11.md).

## Removed command map

Current root command paths below are absent from the registry and return an
unknown-command usage error. The native entries describe scoped governance
operations, not drop-in process-launch replacements.

| Earlier command | Current replacement or status |
|---|---|
| `dacli spawn` | The host creates the child. A governed native worker registers its external checkout, acquires a lease, publishes a revision and submits evidence. A configured controller may authorize a bounded host handoff. dacli does not create the provider process. |
| `dacli supervise` | Unsupported as process supervision. Use host process controls; native controller, dispatch and lifecycle status tools expose recorded governance observations only. |
| `dacli start` | `dacli native controller start` starts a configured governance invocation. It can authorize bounded native host actions and does not start a coding CLI. |
| `dacli loop`, `dacli loop status` | Use configured native controller CLI `step`, `status` and `reconcile` operations plus MCP `step_loop`, `get_loop_status`, `reconcile_loop` and `stop_loop` tools to advance bounded recorded state. They require explicit controller authority and do not supervise a host process. |
| `dacli runtime add`, `runtime rm`, `runtime list` | Unsupported. Provider adapter presets and their management commands were removed; the host owns runtime configuration. |
| `dacli runtime doctor` | `dacli native doctor` inspects selected plugin, artifact, connection and compatibility observations. It does not list provider adapters or qualify a host. |
| `dacli verify` | Configure named checks with `dacli native verifier add`, then use `dacli native verify` for an exact fenced revision or select the private runner profile to expose typed asynchronous verification tools. The request cannot choose an arbitrary command or directory. |
| `dacli kill` | Unsupported as process signaling. A configured native lifecycle policy can record a cooperative cancellation request; current fixtures also show an explicit `unsupported` outcome. Host acknowledgement is a separate observation. Neither request nor acknowledgement proves termination; use the host's own process controls. |
| `dacli wait` | Unsupported as process waiting. Native operation status and reconciliation report durable governance state; unknown execution remains unknown pending owner recovery. `dacli pr wait` waits for pull-request checks only. |
| `dacli claim expand` | Unsupported for native sessions. Owner provisioning must issue a session with the intended task/path scope; an existing lease retains its exact resources and fence. |
| `dacli __run-guardian`, `dacli __run-watchdog` | Removed private legacy process entry points, with no public native replacement. |

`dacli agent spawn` is still registered for legacy workspace identity records.
It records an identity/token and lineage; it does not start a process, create a
native session, enroll a child or authorize native calls. Never use its token
with the native plugin. The tested removed-command set is maintained in
[`internal/cli/native_only_test.go`](../internal/cli/native_only_test.go).

## Historical state and recovery

Migration does not erase old runtime configuration, process records, run
outcomes, usage reports or probe receipts. The current `agents`, `runs
list/show`, `logs` and `replay` commands read recorded history for attribution
and recovery. Historical presence does not authorize a launch, dispatch or
resume; an acquisition receipt is not a native credential and a process record
does not establish current liveness.

Preserve historical receipt bytes and operation identities. Use
`dacli cleanup --dry-run`, inspect the exact plan and apply only that reviewed
safe plan. Unknown liveness, divergent state, referenced evidence and missing
recovery proof remain refusals. Expiry, revocation, a cancellation request or a
stale heartbeat alone does not prove a host process stopped or release a
possibly active writer's resources. Follow the owner recovery path for the
exact retained state; do not reset private authority or retry an uncertain
effect with a new operation key.

Old installed or externally copied dacli skills may still describe the
launcher-era commands or instruct use of `DACLI_AGENT`. Refresh them from the
current checked-in [`skills/dacli/SKILL.md`](../skills/dacli/SKILL.md), then
negotiate against the installed binary with:

```sh
dacli version --compatibility --json
dacli capabilities --json
```

The current checked-in skill and Codex plugin skill describe the native
workflow. A capability gap means the binary and guidance differ; do not infer a
replacement launcher from an old skill or version label.

See [RUNTIMES.md](RUNTIMES.md) for the host qualification and recovery boundary.
