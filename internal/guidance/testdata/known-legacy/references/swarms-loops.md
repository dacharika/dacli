# Waves, swarms, supervision, and loops

The orchestrator agent owns decomposition, prioritization, and evidence
judgment. dacli owns deterministic selection, claims, launch, budgets, recovery,
and landing gates; coding-agent CLIs own isolated implementation and review.

## Contents

- Preparing a wave
- Claims and worktrees
- Spawning and finalizing
- Verification
- Governed loops
- Monitoring and stopping

## Preparing a wave

Parallelize only tasks whose dependencies and write paths are independent:

```bash
dacli task list --status open --project <project>
dacli critical-path --project <project>
dacli next --project <project> --parallel <width>
dacli team assign <ref>
dacli context <ref>
```

Estimate tasks first. Without estimates, scheduling falls back to priority and
sequence and cannot expose meaningful critical-path slack.

For each candidate, identify the smallest truthful code-path claim. If claims
overlap, schedule sequential waves. Do not dishonestly narrow a claim merely to
bypass a conflict refusal.

## Claims and worktrees

Use both mechanisms for parallel write agents:

- `--worktree` creates a task branch and isolated current checkout.
- `--claim path` reserves the future merge surface against live agents and
  constrains both provider writes and `dacli commit` staging. RW providers run
  in an independent `claim-sandbox/v1` checkout; only exact claimed regular-
  file additions/modifications project into the canonical task worktree.

The worktree branch begins at the task project's landing base, not at whichever
feature branch the operator happens to have checked out. With an `origin`,
dacli freshly observes `origin/<landing-base>` and refuses before creating the
branch if that observation fails; an intentionally local repository uses its
exact local base ref. A worktree spawn writes `worktree-base.json` beside the
run evidence with the selected ref and commit. Inspect that record when a
worker appears to have started from unexpected history.

```bash
dacli spawn --task <ref> --role <role> --grant rw --worktree \
  --claim internal/feature --pr --detach
```

Claim package or feature roots, not the entire repository. Include tests and
fixtures the task is expected to modify. Claim matching normalizes file,
directory, and glob presentations before checking overlap. If a truthful claim
is refused, inspect the reported normalized paths and use the named safe
recovery rather than bypassing claim enforcement.

An out-of-claim/generated write, traversal, symlink, rename, delete, stale
head, or dirty canonical checkout refuses the whole projection before any
canonical path changes. If the task truthfully requires more scope, root may
record it only after the prior run is terminal:

```bash
dacli claim expand --task <ref> --run <run-id> \
  --add <new-path> --reason "why the original scope was insufficient"
```

This writes `claim-expansion/v1` with task/run/actor/reason and exact old/new
scopes before a later relaunch. Never infer or auto-approve expansion from the
paths a provider attempted to write.

Task ownership and a path-scope claim are separate controls. `commit --json`
reports the task owner/ref, canonical worktree and branch associations, and the
effective spawned, transferred, or task-inferred path scope independently. A
`path_scope_unavailable` diagnostic does not mean the task is unclaimed; use
its read-only remediation command to inspect the context, then establish the
narrow scope before relying on file-scope enforcement.

Inside a dacli worktree, edit relative to the worktree's current directory.
Absolute paths pointing to the main checkout can put changes on the wrong
branch. The shared `.dacli` record intentionally resolves to the main workspace.

Some coding-agent sandboxes can edit that worktree but cannot create its
shared `.git/worktrees/.../index.lock`. Preflight records
`git-metadata-write` as a planned handoff; do not widen the harness or grant.
The worker preserves its claimed edits and verification evidence, and the
parent writes `parent-commit-request-v1.json` before creating the exact commit
through a temporary index. `parent-commit-receipt-v1.json` proves the atomic
branch update. Extra paths, changed bytes, stale heads, or identity/claim
mismatches refuse. On restart, the same deterministic commit is recovered and
the loop continues from `committed` without another worker spawn.

## Spawning and finalizing

Start detached agents only after preflight:

```bash
dacli preflight --role <role>
dacli spawn --task <ref> --role <role> --worktree \
  --claim <path> --pr --detach
dacli agents --tail
dacli logs <run-id-prefix|child-id> -f
dacli catchup --since 20m
dacli wait
dacli sync
```

For an independent review, preflight and launch are one exact contract rather
than two loosely related commands. `preflight --structured-review-result`
emits `runtime-launch-contract/v1`; governed loops carry its fingerprint into
the review spawn automatically. The launch must remain verified RO on the same
harness, adapter, sandbox, runtime, model, grant, and stdout result channel.
Cooperative RO and RW refuse. Inspect the run's `launch-contract.json` and
`review-validation.json` when the parent rejects a review envelope; expected
and actual schema, reviewer identity/role, runtime/model/grant, commit, and tree
are structured there. Never convert that refusal into a workspace-writing
reviewer fallback.

Use `agents` and `logs` to monitor. Use `wait` to finalize: it classifies the
outcome and records silent/no-result runs. A finished process that has not been
wait-finalized may still occupy operator attention and leave an incomplete
record.

Read-only agents propose workspace mutations as events. Run `sync` before
concluding that an auditor produced nothing. Retire completed agents when a WIP
slot remains occupied:

```bash
dacli agent retire <agent-id>
```

## Verification

Require evidence at three levels:

1. Task-local acceptance criteria.
2. Targeted regression tests that fail when the protected behavior is broken.
3. Repository-wide quality gates appropriate to the change's blast radius.

Use `dacli verify --task <ref> --panel <runtime-a>,<runtime-b> --require 2`
for an adversarial panel. Prefer heterogeneous runtimes/models for independent
failure modes. Do not let the implementing agent be the only certifier when
independence is warranted; enforce that owner gate with
`dacli accept <ref> --require-independent --verify "<command>"` after the
panel and landing evidence exist.

Do not weaken or remove a failing test to make a wave green. If the test is
wrong, explain the false premise and replace it with a test that fails under a
specific relevant mutation.

## Governed loops

For GitHub-first collaboration, configure `landing.mode: pr` and its base when
required checks, CODEOWNERS/reviews, and a human-visible PR are part of the
landing contract. The loop resolves CLI override > project configuration >
legacy local default, previews the result, and journals it across restarts.
Remote failures leave tasks open; repair GitHub access and rerun so canonical
branches and existing PRs are reused.

Preview one cycle before unattended operation:

```bash
dacli loop --project <project> --width <n> --impl-role <implementer> \
  --review-role <reviewer> --max-cycles <n> --dry-run
dacli loop --project <project> --width <n> --impl-role <implementer> \
  --review-role <reviewer> --advise
```

Then choose explicit governance:

```bash
dacli loop --project <project> --width <n> --impl-role <implementer> \
  --review-role <reviewer> --max-cycles <n>

dacli loop --project <project> --width <n> --impl-role <implementer> \
  --review-role <reviewer> --yolo \
  --max-cycles <n> --window-tokens <tokens> --token-window 24h \
  --halt-after-idle 3
```

One cycle plans ready work, spawns implementers, waits/tests, lands, runs the
continuous-improvement reviewer, and records a retro. The review phase may file
the next evidence-backed task; it must not invent work when evidence is empty.

Use `--worker-timeout` when task estimates imply that the derived timeout is
inappropriate. Keep width within role WIP, machine capacity, token budget, and
the number of truly disjoint claims. More workers do not help when they contend
on the same package or CI bottleneck.

Run landing loops that share one repository/trunk sequentially unless repository
policy explicitly proves concurrent integration is safe. Project flags scope
scheduling; they do not give each project a separate Git history, event log, or
security boundary.

The stock `--review-role` is the cycle's post-land continuous-improvement
reviewer. It is not a blocking security approval for every implementation PR.
For pre-merge security review, make a cross-provider reviewer or external check
a required GitHub gate, or explicitly spawn a reviewer before integration:

```bash
dacli spawn --task <ref> --role <security-reviewer> \
  --review --pr-number <number> --advise
# inspect the preview, then repeat without --advise; wait and sync before land
```

One loop uses one explicit `--impl-role`. To guarantee multiple providers in a
single project, run explicit provider-assigned waves or separate bounded loop
runs; do not assume one loop alternates runtimes automatically.

## Monitoring and stopping

```bash
dacli loop status --project <project>
dacli agents --tail
dacli pr status --task <ref>
dacli events tail
dacli dashboard
```

The dashboard Agents route is the human supervision view for one selected
project. It combines durable phase/recovery checkpoints, wave allocation,
enforceable versus advisory token reservations, harness-bounded model routing,
capacity, verification, outcome analytics, and live worker evidence. Outcome
windows are descriptive evidence: require comparable task-size cohorts and
adequate samples, drill into exact task/run membership, and preserve missing
cost or absent historical timestamps as unknown rather than zero. Its poll is local and
read-only: it never resumes a loop, refreshes GitHub, retries a provider,
changes a budget, or approves a review. Missing, stale, partial,
external-unknown, policy-refused, and corrupt evidence are distinct from a
healthy run. Agents should continue branching on the versioned JSON commands;
operators can use this view to find the exact record to inspect next.

Start triage in the Overview `operator-attention/v1` queue. It deterministically
orders durable policy and delivery conditions by severity, critical-path
impact, age, and evidence confidence, then links the exact task, run, check, or
dependency record. Repeated observations retain their first/last timestamps
and occurrence count. The queue is observation only: agents must follow the
typed next action through the governed CLI/GitHub path, never treat a dashboard
dismissal as resolution, and never downgrade stale or unknown external state
to healthy.

The loop measures progress by trunk advancement, not by optimistic task-status
changes. Auto-merge can land after the spawning cycle; inspect PR and trunk
state before diagnosing a no-progress halt.

Stop at the next safe checkpoint:

```bash
touch .dacli/STOP
```

The stop file latches. Remove it only after inspecting state and intentionally
resuming. Use the no-progress halt as a thrash guard, not as evidence that no
work was attempted.

After a wave, preview cleanup:

```bash
dacli cleanup --project <project> --dry-run
dacli cleanup --project <project> --apply-safe <plan-id>
# If an audited generated artifact is needed again:
dacli cleanup --project <project> --restore <plan-id> --artifact <identity>
dacli runs prune --keep 20
```

Use the exact plan id printed by the dry-run; a changed worktree, task, run,
claim, branch, remote ref, or PR state makes apply refuse. Never delete a
worktree with unlanded material merely because its process has finished. Only
explicitly enumerated generated artifacts move into the recoverable cleanup
quarantine; durable run evidence stays in place. The
planner preserves unknown or ambiguous evidence and records recovery commands
for every worktree/branch operation it completes.
