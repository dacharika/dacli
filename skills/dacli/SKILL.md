---
name: dacli
description: "Govern native coding-host work with dacli: inspect canonical tasks, use scoped plugin sessions and fenced leases, record exact revision evidence, and preserve explicit owner review, publication and recovery authority."
---

# dacli

Use the native coding host for agent creation, delegation, model selection and
execution. dacli supplies shared task/context records and authenticated governance
transitions. Coding-agent CLI launchers, runtime presets and provider-driven
start/loop/service commands have been removed from current source.

I will use this skill to inspect the workspace and preserve its authority and
evidence boundaries. Repository instructions and the user's explicit scope take
precedence. Do not fabricate credentials or treat a host label as authentication.

## Inspect the installed surface

```bash
dacli version --compatibility --json
dacli capabilities --json
dacli status --project <project> --json
dacli task list --status open --project <project> --json
dacli explain --project <project> --json
```

The adjacent `capabilities.json` declares required command contracts. Inspect
live JSON support before parsing output. A required capability gap means this
skill and binary do not match; do not invent a fallback launcher. Older released
v0.3.1 binaries do not contain the native plugin preview.

## Native task workflow

Use the reviewed [Codex plugin setup](https://github.com/mlnomadpy/dacli/blob/main/plugins/codex/README.md).
Owner provisioning/enrollment is explicit and local-only. Profiles and tokens
stay private, outside the repository and plugin archive. The plugin starts only
`dacli native serve` with its configured connection. Never inject `DACLI_AGENT`,
use an operator fallback, or spawn a nested coding CLI to obtain access.

1. Inspect native `identity`, `workspace_status` and `task_get` before acting.
2. Register the externally created clean Git checkout as an attempt and acquire
   a current scoped lease. Observe readiness/refusals; renew before expiry.
3. Use the host's tools within the assigned scope. Commit the finished change,
   publish the exact observed revision, then submit self-reported evidence.
4. Trusted runner evidence and independent review require distinct owner-issued
   grants. A shared MCP connection shares one identity; role/model/host metadata
   cannot establish reviewer independence. A reviewer does not take the worker lease.
5. The owner verifies exact-source artifacts and current acceptance. Native review
   registration and one-use publication decisions do not themselves close tasks,
   publish, merge or release. Release unused leases and revoke unused sessions.

Run actual checks from the checkout containing the change. Preserve command,
working directory, commit/tree, test names and output digest. Zero exit with no
matching tests is not proof. Make a named semantic test fail by mutating its guard,
restore it, and record both results when repository policy requires that evidence.

## Recovery and publication

Use bounded historical `agents`, `runs`, `logs` and `replay` reads for existing
records. Do not treat an old process record or acquisition receipt as live native
authority. Cleanup requires an exact preview/apply plan; unknown or divergent
material stays protected. Never erase an authority file to repair a refusal.

Preview GitHub adoption/projection before an authorized write; public-safe task
fields are the default. Internal content requires separate recorded authority.
Do not publish messages, issues or PRs without the user's scope authorizing them.
No eligible work is a reason to stop, not to create busywork.

Read the current [operator playbook](https://github.com/mlnomadpy/dacli/blob/main/docs/OPERATOR_PLAYBOOK.md)
and the relevant local reference:

- [Workspace tasks and projects](references/workspace-tasks-projects.md)
- [Operating boundaries](references/operating-profiles.md)
- [Native hosts and skills](references/runtimes-models-skills.md)
- [Host delegation](references/swarms-loops.md)
- [Repeated work](references/continuous-operations.md)
- [Model economics](references/model-economics.md)
- [Roles and independence](references/roster-design.md)
- [Critical-path GitHub work](references/critical-path-github.md)
- [Landing](references/github-landing.md)
- [Recovery](references/recovery.md)
