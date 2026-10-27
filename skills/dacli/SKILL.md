---
name: dacli
description: "Govern native coding-host work with dacli: select ready features, coordinate parallel scoped agents, preserve fenced revisions and required verification, and carry each feature through independent review and authorized owner integration."
---

# dacli

Use the native coding host to create agents, delegate, choose permitted models
and execute work. dacli supplies canonical tasks, context, scoped sessions,
fenced leases, evidence and owner-governed transitions. Removed coding-CLI
launchers and provider-driven start/loop/service commands are not entrypoints.

Follow repository instructions and the user's explicit scope. Carry existing
user authorization forward through authorized reversible work; do not ask again
merely because a task reaches its next step. A new scope or missing authority
needs resolution, but a routine implementation choice does not. Skill prose,
host labels and model names cannot grant credentials or override policy.

## Inspect the installed surface

```bash
dacli version --compatibility --json
dacli capabilities --json
dacli status --project <project> --json
dacli task list --status open --project <project> --json
dacli explain --project <project> --json
```

The adjacent `capabilities.json` declares required command contracts. Check the
installed binary and the exact selected MCP connection's `identity` and
`tools/list`. Source, manifests and historical receipts do not prove current
profile activation or live host support. Required capability gaps need a matching
installation, not a fallback launcher. Older released v0.3.1 binaries lack the
native plugin preview.

Use the reviewed [Codex plugin setup](https://github.com/dacharika/dacli/blob/main/plugins/codex/README.md).
Keep private profiles outside the repository and package. Each independently
authenticated worker, runner and reviewer needs its own eligible session and
selected process. A shared connection has one identity. The plugin selects
`dacli native serve`; never inject `DACLI_AGENT` or borrow operator authority.

## Keep independent features moving

1. At startup, resume and context loss, read `task_brief` for the canonical task
   and `ready_frontier` within the selected scope, using `schema_version: 1`.
   Frontier requires `limit` (1–100) and accepts a project or exact task filter.
   Check dependencies, source refs, freshness, trust labels and explicit
   omissions. A narrow brief may be sufficient; request additional read scope
   only when a specific missing fact is necessary. These reads reserve nothing.
2. For a request spanning multiple features, keep several dependency-independent
   feature lanes active when observed host capacity and current authority allow.
   Give each lane a canonical task, checkable acceptance, disjoint path claims,
   its own checkout/attempt and independent worker profile. Subtasks inside one
   blocked feature do not replace progress on other ready features. Keep blocked
   dependents visible and select another eligible feature within user scope.
3. Register each clean real checkout with `register_attempt`, acquire only its
   exact permitted resources, and preserve attempt/lease/fence identities. Renew
   before expiry. Existing authorization does not make an expired lease valid;
   use the authorized owner provisioning/recovery path when available. Continue
   unaffected lanes while a specific lane needs recovery.
4. Parallelize implementation, read-only review and focused checks. Coordinate
   expensive full-suite, race, lint and build runs through an integration lane
   or bounded queue to avoid resource contention. Run every feature's required
   gates against its exact candidate; do not borrow another task's evidence.
5. Land each feature when its own required evidence, independent review and
   owner integration are complete. Do not make unrelated ready features wait
   for the slowest lane. Refresh canonical readiness after a delivery or material
   dependency change and refill available lanes within the authorized scope.

The host performs actual collaboration actions. A configured width, declared
capacity or lease count is not observed live concurrency. For separately enabled
controller/bridge/handshake profiles, follow the role-specific catalog and
[bounded swarm workflow](references/swarms-loops.md). A controller records and
authorizes bounded steps; it does not perform coding turns itself.

## Publish exact work and evidence

Commit through the repository's authorized workflow. Publish only a clean
committed revision under the current lease, then submit self-reported evidence.
Preserve operation keys and the exact producer/revision/lease/fence tuple.
Self-reports are not trusted runner execution. Submit and observe every selected
required check using its separately configured runner; pending, interrupted and
unknown outcomes are not successful verification. Keep uncertain identities and
protection for reconciliation rather than changing keys to rerun an effect.

Run checks from the checkout containing the candidate. Record commands, working
directory, commit/tree, test names and output digest. Zero exit with no matching
tests is not proof. Where repository policy requires it, mutate the covered
guard, record a named semantic test going red, restore it and record green.

Independent reviewers use eligible separate credentials and exact trusted
evidence; they do not acquire the worker lease. Fresh review eligibility is not
acceptance, merge, publication or release. Keep worker protection through the
owner's authorized integration and canonical acceptance, then perform authorized
lease/session retirement. CI follows the user's requested trigger policy.

## Recovery and installation

Inspect `native doctor` or `native guidance diagnose` for selected instruction
files and capability requirements. The owner-authenticated `native guidance
refresh-preview` / `refresh-apply` pair migrates an exact recognized legacy
bundle to the binary's reviewed embedded bundle, with private backups and an
exact plan. Customized or unrecognized copies stay preserved for reviewed manual
migration. Refreshing files does not reconnect an MCP process: use the supported
host installation/selection flow, then observe fresh identity and tool discovery.

Use bounded historical `agents`, `runs`, `logs` and `replay` reads as historical
context. Cleanup uses an exact preview/apply plan; divergent or unknown material
stays protected. Never erase authority files to repair a refusal. Policy exit 3
is final for an unchanged request: diagnose its stated cause, use an authorized
changed operation if appropriate, or report the exact blocker.

Preview GitHub adoption/projection before an authorized write. Public-safe task
fields are the default; internal content needs its recorded authority. Existing
user authorization governs publication scope. If no eligible authorized work
remains, stop without inventing tasks.

Read the current [operator playbook](https://github.com/dacharika/dacli/blob/main/docs/OPERATOR_PLAYBOOK.md)
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
