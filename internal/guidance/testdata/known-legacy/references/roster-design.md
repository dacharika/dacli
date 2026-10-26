# Designing roles, model tiers, and skill bundles

## Contents

- Responsibility-first roles
- Required role contract
- Model and cost routing
- Cross-runtime review
- Composable skill matrix
- Validation and migration

## Responsibility-first roles

Name a role for durable responsibility—`maintainer`, `frontend-reviewer`,
`loop-auditor`—not its provider, model, migration, or issue number. Runtime and
model are replaceable execution policy. Remove bootstrap and task-specific
roles after their wave; preserve historical agent/run attribution separately.

Keep the smallest roster supported by real code and workflow boundaries. Two
roles are distinct only when at least one mechanical fact differs: lifecycle
kind, grant, scope, skill bundle, capacity/cost, WIP, or escalation. A different
paragraph describing the same capability is duplication, not specialization.

Useful families are:

- Implementers: bounded junior, ordinary fixer, high-blast-radius maintainer,
  and a frontend specialist where the stack warrants it.
- Reviewers: general independent reviewer plus evidence-backed specialists for
  seams, mutations, Go systems, prompts, and frontend quality.
- Planner/designer/researcher: estimator, role architect, UX/product research,
  and UI/spec design only when those artifacts exist.
- Integrator and loop auditor: governance responsibilities, never disguised
  implementation roles.

## Required role contract

Every durable role should declare and explain:

| Field | Purpose |
|---|---|
| `version` | Auditable change to standing instructions |
| `role_kind` | Discovery/planning/design/implementation/review phase gate |
| `grant` | Truthful permission ceiling (`ro` only when enforced) |
| `runtime`, `model_id` | Replaceable execution choice |
| `cost_tier`, `max_task_points`, `context_limit` | Provider-neutral routing/capacity |
| `capability_tags` | Machine-readable specialization |
| `scope`, `out_of_scope` | Positive boundary plus explicit refusal surface |
| `escalate_to`, optional `fallback_to` | Decision and runtime recovery chains |
| `skills` | Reusable method delivered to the runtime |
| Prompt method | Evidence order, refusal conditions, and definition of done |

Scopes guide routing and claim selection; they are not operating-system
permissions. Runtime sandbox, dacli grant, claims, worktree isolation, and
repository permissions remain separate layers.

## Model and cost routing

Use the cheapest model with enough capacity for the task, then raise the tier
for consequence:

- Tier 1: small, reversible, well-tested work and bounded research.
- Tier 2: ordinary implementation, frontend work, planning, and focused review.
- Tier 3: architecture, auth/security, persistence, concurrency, process
  control, remote landing, destructive operations, and adversarial review.

`team assign` supplies the cost/capacity floor. The orchestrator agent raises
the tier for ambiguity or blast radius. If no role covers the task estimate,
decompose it instead of silently exceeding capacity. Model IDs are adapter data;
never encode a particular provider or “Opus everywhere” in framework policy.

## Cross-runtime review

Prefer a strict read-only reviewer on a different provider/model family from
the author. Verify the local sandbox behavior before unattended use. If another
runtime is available only with a write-capable grant, label that reviewer
cooperative, forbid edits in its method, exclude it from unattended loops, and
use it only as a supervised panel seat. Do not describe cooperation as enforced
read-only.

One loop invocation uses one implementation role; it does not automatically
alternate providers. Use explicit provider-assigned waves or a verification
panel when heterogeneous execution is required.

## Composable skill matrix

Keep common rules compact and compose domain knowledge:

| Skill | Assign to |
|---|---|
| dacli operation | Every governed role |
| evidence and mutation testing | Every implementer/reviewer/acceptor |
| Go architecture, persistence, performance | Go implementers and systems reviewers |
| runtime/process safety | Runtime, orchestration, integration, and seam roles |
| GitHub-first delivery | Implementers, integrator, loop auditor, PR reviewers |
| frontend quality | Frontend engineer/reviewer and UI designer |
| product research/design | Researchers, product strategist, estimator, UI designer |

Use short inline bodies for non-negotiable behavior and one-level-deep resources
for detailed lookup. Run `skill compile --role ... --runtime ... --dry-run` for
every pair. Omission means the role did not receive the skill; a large inline
total is a repeated per-turn cost. Document the chosen budget and observed tax.

## Validation and migration

1. Run `role list`, `skill list`, `runtime doctor`, and `doctor`.
2. Remove provider/bootstrap/task-specific roles only after confirming no live
   agent holds them. Terminal history remains attributable.
3. Add/bump reusable skills and role versions.
4. Run `preflight` for every active role.
5. Compile every role/runtime pair and inspect omissions plus inline totals.
6. Generate `docs/ROSTER.md` from the workspace definitions.
7. Validate and install the committed dacli skill from the same tree.
8. Deliver through a PR and wait for CI before accepting the roster task.

Treat authentication and configuration provenance separately from binary
probing. A CLI can be installed and advertise flags while still being logged
out or loading undeclared global plugins; record that limitation and do not call
the runtime operationally ready until the relevant behavior is probed.
