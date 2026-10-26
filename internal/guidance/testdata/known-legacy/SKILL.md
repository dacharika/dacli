---
name: dacli
description: "Run autonomous product-building swarms with dacli. Use when an orchestrator AI agent must plan a critical-path backlog, route models across coding-agent CLIs, execute bounded task/wave/loop profiles, recover durable state, or verify and land work through GitHub."
---

# dacli

Act as the orchestrator agent. Use dacli as the agent-facing control plane over
replaceable coding CLIs: interpret the product direction, maintain the critical
path, choose capable and economical roles/models, and judge evidence while
dacli enforces state, permissions, budgets, lifecycle, recovery, and landing.
Continuous operation is repeated bounded transactions with durable checkpoints,
not permissionless execution.

Keep authority explicit. The human governor supplies direction, credentials,
exceptions, emergency stop, and release policy. Worker coding agents implement,
review, and test. GitHub is the shared collaboration and landing surface; the
local dacli workspace is the canonical execution and evidence ledger.

Repository `AGENTS.md` and `CONTRIBUTING.md` override this skill. Negotiate the
installed surface before following examples, then measure live state through
machine-readable commands:

```bash
dacli version --compatibility --json
dacli capabilities --json
dacli whoami --json
dacli status --project <project> --json
dacli doctor --json
dacli task list --status open --project <project> --json
dacli next --project <project> --critical-path --json
dacli agents --project <project> --json
dacli loop status --project <project> --json
dacli explain --project <project> --json
```

This discovers the adjacent `capabilities.json` requirement document and
reports supported, optional-missing, required-missing, and incompatible-schema
states. A required gap is a policy refusal: update the binary or use guidance
generated for its `dacli capabilities --json` manifest. An optional gap names
an explicit fallback. Confirm `json: true` in the live manifest for every
command whose output the agent will parse—not only the bootstrap block, but
also `github projection`, `start --show`, `pr diagnose`, and optional `pr
wait`. Requirements v1 identifies commands and flags, while that live field is
the authority for machine-readable output. In particular, use `task check
--verify` only when
`cli.command.task.check.flag.verify` is advertised; otherwise run the verifier
separately, retain its output, and check only criteria whose evidence contract
allows that fallback.

## Choose the first mode

| Situation | First choice |
|---|---|
| Understand work | `dacli start --profile inspect` |
| One bounded change | `dacli start --profile task` |
| Independent ready tasks | `dacli start --profile wave` with fixed WIP and claims |
| Repeatable backlog improvement | `dacli start --profile loop` with bounded cycles and budgets |
| Resident single-project runner | `dacli start --profile service`; repeated finite loops |
| Multi-repo or multi-tenant control | Future control-plane vision, not a local-service promise |

Preview the resolved project policy before execution:

```bash
dacli start --project <project> --profile <mode> --dry-run
dacli start --project <project> --profile <mode> --configure
dacli start --project <project> --show --json
```

Choose the coding harness separately from the model tier. The safe default is
one harness family for the whole run: implementation, continuous-improvement
review, recovery, and fallback. For example, a Codex run is configured with
`--harness codex`; dacli may route among Codex roles/models, but it must not
silently try Claude Code, Gemini, Copilot, or a generic adapter. Cross-harness
work requires an explicit allowlist and `--hybrid`:

```bash
dacli start --project <project> --profile loop --harness codex --configure
dacli start --project <project> --profile loop \
  --harness codex --harness claude --hybrid --configure
```

Do not confuse independence with vendor switching. A single-harness run may use
a different role or model for review. Use hybrid only when the operator wants
cross-harness diversity and every listed CLI is authenticated and preflighted.

`--dry-run` neither writes nor launches; `--configure` persists without
launching. Inspect execution is read-only. Every other profile persists its
resolution and then delegates to the existing bounded loop strategy. Service
adds a lease and durable checkpoints around repeated finite invocations; it
never grants tag or release authority.

Never manufacture backlog work. When GitHub is the declared collaboration
surface, confirm or create the issue before implementation, then keep the issue,
task, branch, PR, checks, and accepted trunk state linked. Exit 3 is a policy
refusal: stop and follow the stated remedy rather than retrying unchanged.
For `accept --verify`, dacli merges configured checks with legacy branch
protection and applicable repository/organization rulesets. Treat an
unobservable check policy as a real refusal; the explicit
`--allow-unobservable-check-policy` exception is owner authority that is
recorded in the task log, not a routine retry strategy.

Use the short agent lifecycle surface for the ordinary path. `route <path>` is
the exact alias of `team route`; `pr wait --task <ref>` repeatedly consumes the
typed `pr diagnose` result within a bound; `pr land --task <ref>` delegates to
the existing integration transaction. Before removing old worktrees or local
branches, run `branches audit --project <project> --json`, then apply only its
exact content-addressed id with `branches prune --project <project>
--apply-safe <plan-id>`. These aliases do not create parallel state machines.

Treat writable claims as enforced authority, not planning hints. An isolated
RW run executes in a disposable `claim-sandbox/v1` checkout and projects only
validated claimed regular-file additions/modifications into the canonical task
worktree. If review proves the scope too narrow, root may record a reasoned
expansion for a later relaunch with `dacli claim expand --task <ref> --run
<terminal-run> --add <path> --reason <why>`; never infer authority from paths a
provider attempted to write.

## Read one focused reference before acting

- [operating-profiles.md](references/operating-profiles.md): choose task,
  wave, or bounded loop; WIP and budgets.
- [model-economics.md](references/model-economics.md): capability, Te, context,
  consequence uplift, quota/health, and independent review diversity.
- [critical-path-github.md](references/critical-path-github.md): deduplicate,
  estimate/depend, critical path, GitHub issue/PR/check/merge cycle.
- [continuous-operations.md](references/continuous-operations.md): STOP,
  heartbeats, journals, breakers, dead letters, observability, recovery.
- [workspace-tasks-projects.md](references/workspace-tasks-projects.md):
  project isolation, shared workspace state, references, and GitHub mappings.
- [runtimes-models-skills.md](references/runtimes-models-skills.md): harness
  pinning, explicit hybrid mode, model routing, and symmetric
  Codex/Claude Code/Gemini/Copilot/generic adapters.
- [roster-design.md](references/roster-design.md): capability tiers, role sizing,
  provider diversity, and reusable team policy.
- [swarms-loops.md](references/swarms-loops.md): claims, worktrees, supervision,
  dashboard observation, review timing, and bounded loop execution.
- [recovery.md](references/recovery.md): refusals, partial runs, PR conflicts,
  journals, and safe restart inspection.
- [github-landing.md](references/github-landing.md): GitHub disclosure,
  PR/landing policy, and release boundary.

The canonical orchestration narrative is the public
[operator playbook](https://github.com/mlnomadpy/dacli/blob/main/docs/OPERATOR_PLAYBOOK.md).
Keep this file a router; do not duplicate its detailed runbooks.
