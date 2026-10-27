# Native governance operator playbook

The native host runs agents and any repeated work cycle. dacli provides canonical
context and authenticated, revision-bound governance. Coding-agent launchers,
provider adapters and service supervisors are removed from this checkout.

## Inspect before acting

```bash
dacli version --compatibility --json
dacli capabilities --json
dacli status --project <project>
dacli task list --status open --project <project> --json
dacli explain --project <project> --json
```

Direct task references are workspace-wide. Use project-qualified references or
exact task IDs; ambiguity refuses. Record dependencies truthfully before using
`critical-path` or `next`; do not use `critical-path` or `next` to claim that
incomplete graph is authoritative. Choose bounded work based on real evidence;
no ready work is a reason to stop, not to invent another task.

## Native setup and execution

Build the reviewed checkout and follow the [Codex plugin setup](../plugins/codex/README.md).
An owner creates private authority and enrolls an explicit workspace/task scope.
The plugin reads a private connection profile and runs only `dacli native serve`.
Provider login, a legacy identity token or a host session label is not a substitute.

Use native `identity`, `workspace_status`, `task_list` and `task_get` to verify
scope. The host creates a clean Git checkout; register it with `register_attempt`,
then acquire/renew a fenced lease. Implement and test using the host's native
tools. Publish the committed revision and submit self-reported evidence through
the native tools. Release the lease when finished; revoke unused sessions through
the explicit owner command. A refusal is an answer: do not retry unchanged.

Host budgets and sandbox settings are configured in the host. dacli does not
switch providers, cap provider billing or supervise a persistent model process.
A shared MCP connection has one credential identity regardless of the number of
native children using it.

For bounded task-stage counts and host admission reasons, use the separate
project operator connection with `native queue diagnostics`; see the
[queue diagnostic contract](GOVERNANCE_NATIVE_QUEUE.md). This read preserves
unknown capacity and retained writer protection and grants no dispatch authority.

## Verification, review and acceptance

Run the actual repository verification bar from the exact worktree containing
the change. A successful command with no matching tests is not evidence. Record
the source commit/tree, command, working directory and retained output digest.

The [native evidence contract](GOVERNANCE_NATIVE_EVIDENCE.md) distinguishes an
agent assertion from an owner-granted trusted runner observation. The
[native review contract](GOVERNANCE_NATIVE_REVIEW.md) requires a separately
eligible credential family, current canonical checklist/policy and exact revision.
Changing a role label or model does not create independence. Native review
registration does not close a task or consume final acceptance.

The established owner task/acceptance commands remain available for explicit
verification and completion. Before owner acceptance or GitHub issue closure,
observe both the merged PR and its commit on trunk when PR landing is required.
Never accept on the strength of an unrelated branch's test output.

## GitHub-first critical-path cycle

Preview inbound adoption with `github pull <project> --dry-run`; preview outbound
projection with `github push <project> --dry-run`. `github sync <project> --dry-run`
preview projection changes first. Inspect `github projection` for the exact
allowlist. Public-safe task fields are the default; internal material requires
recorded repository authority and an explicit request.

Record landing policy with
`dacli project show <slug> --landing-mode pr --landing-base main`.
Use `dacli pr diagnose --task <ref> --json` to inspect the exact PR, CI, review and
topology evidence. `dacli push <ref>` is an explicit branch publication step;
`pr`, `pr wait`, `pr land`, `merge` and `integrate` retain their individual guards.
No native publication receipt itself creates a PR or merges code. Release
publication remains a separate maintainer decision; `dacli release train` does
not create a tag or release.

Record `dacli retro <task-or-project-ref> --well "..." --bad "..." --improve "..."`
after delivery. Repeat only while evidence-backed work and user authority remain.

## Historical diagnosis and recovery

`agents`, `runs list`, `runs show`, `replay` and
`dacli logs <run-id-prefix|child-id> -f` retain historical observations. Use bounded
JSON log pages for automation. Dashboard loop/routing/usage panels describe
retained records, not a running native controller or current billing authority.
Unknown GitHub state or stale evidence requires observation rather than inference.

```bash
dacli cleanup --project <slug> --dry-run
dacli cleanup --project <slug> --apply-safe <plan-id>
dacli cleanup --project <slug> --restore <plan-id> --artifact <identity>
```

Apply only the exact reviewed safe plan. Unknown liveness, unreadable evidence,
dirty/divergent content and referenced receipts remain protected. Recovery must
not overwrite existing material. Never delete private authority to repair an
error; use explicit owner migration/revocation and retain recovery evidence.

## Shipped, experimental, and future

The local native plugin and governance primitives are a cooperative preview.
Host-native work is real; authenticated child identities are not inferred from
host metadata. Native final acceptance and hosted multi-tenant management remain
future work. Historical research, probes and older released launcher behavior do
not expand the present permission or support boundary.
