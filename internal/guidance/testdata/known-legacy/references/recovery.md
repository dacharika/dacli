# Recovery and failure handling

## Contents

- Exit-code decisions
- First-response checklist
- Agent and runtime failures
- Task and record drift
- PR and landing failures
- Loop recovery
- Safe cleanup

## Exit-code decisions

Treat exit status as a protocol:

| Exit | Meaning | Next action |
|---:|---|---|
| 0 | Completed | Verify the resulting state |
| 1 | Operational failure | Diagnose environment, tool, network, or implementation |
| 2 | Usage error | Fix flags/arguments; consult command help |
| 3 | Policy refusal | Do not retry unchanged; follow the stated remedy |
| 4 | Not found | Check checkout, workspace, project, and reference |

For exit 2, prefer `--json`: the standard error envelope always carries
`suggestions` and `next_actions` arrays. These are bounded hints derived from
the installed binary's command registry, not permission to execute. Review the
suggestion first; ambiguous `show <ref>` recovery stays read-only and requires
you to choose `task show` or `project show` explicitly.

Capture the target command's exit status directly. `$?` after a pipeline is
normally the final process's status, not dacli's.

## First-response checklist

```bash
dacli whoami
dacli status
dacli doctor
dacli agents --tail
dacli runs list
dacli loop status --project <project>
git status --short --branch
git worktree list
```

Preserve evidence before changing state: exact command, exit code, stderr,
run ID, agent ID, task ID, branch, dirty paths, and PR/check status.

## Agent and runtime failures

| Symptom | Response |
|---|---|
| Runtime binary missing/stale | Run `runtime doctor`; repair binary/path/adapter |
| `ro` sandbox refused | Select a verified read-only preset or repair probing; use `--cooperative` only by explicit policy |
| `rw` agent cannot edit | Run `preflight`; select an `*-rw` preset or correct allowlisted tools |
| Spawn cost refused | Decompose, choose a cheaper capable role, raise an authorized limit, or wait for the rolling window |
| Agent timed out | Inspect partial events/transcript; narrow work or set justified timeout |
| Detached agent appears finished | Run `wait` to finalize outcome |
| Read-only audit appears empty | Run `sync`; inspect events and findings |
| WIP slot remains full | Verify process/run state, then `agent retire <id>` |
| Runaway process tree | Use `dacli kill <agent-id>`; inspect partial work afterward |

Exit 3 is a decision boundary. Never loop a refused spawn with the same task,
role, grant, runtime, claim, and limits.

## Task and record drift

Use full task IDs when a short ref is ambiguous. Confirm the task exists in the
current checkout; task files created only on another branch are genuinely
absent here.

Run `doctor` for duplicate status files, corrupt frontmatter, orphaned tasks,
stale agents, and broken references. Do not manually move or delete record files
until the diagnosis identifies the authoritative state.

Use:

```bash
dacli task reopen <ref> --reason "<why the prior closure was wrong>"
dacli accept <ref> --force
dacli task rm <ref> --force
```

only for the exact reconciliation each command describes. `task rm` deletes
work that should never have existed; it is not a substitute for `wont` or done.
Decisions, findings, and risks are durable history—supersede them with a new
record rather than erasing evidence.

## PR and landing failures

```bash
dacli pr status --task <ref>
gh pr checks <number>
git fetch origin main
```

Distinguish:

- Behind base: update the task branch and re-run tests.
- True conflict: resolve in the task branch, preserving both intended changes.
- Red CI: fix the named failing check; do not merge by override unless the
  repository's documented emergency policy explicitly authorizes it.
- Pending CI: wait or use GitHub auto-merge; do not report it as landed.
- Orphaned/unlanded task: locate the canonical branch/PR before acceptance.
- `0 PRs opened`: check canonical branch naming and whether the branch exists.

Use dacli integration after recovery so the landing event and task record agree.

## Loop recovery

When a loop halts or stops progressing:

1. Read `loop status` and the last run outcomes.
2. Check whether trunk advanced late through GitHub auto-merge.
3. Inspect blocked/problem dependency refs.
4. Run `wait` for unfinished detached runs and `sync` pending proposals.
5. Inspect CI and PR state for every task in the last wave.
6. Run `doctor` and preview worktree cleanup.
7. Correct the specific cause; do not merely reset the no-progress counter.
8. Remove `.dacli/STOP` only when intentionally resuming.

An operator can inspect the same local recovery, phase, reservation, routing,
and worker records together in the dashboard Agents route. A stale, partial,
corrupt, externally unknown, or policy-halted dashboard state is a pointer to
the named durable record, not permission to retry. Advisory or missing budget
remaining is unknown—not zero and not unlimited—and provider-reported usage is
not billing evidence.

The loop persists its resolved landing mode/base and outstanding canonical
branches. After interruption at push, PR creation, pending checks, or merge,
inspect `dacli pr status --task <ref>` and rerun the same bounded command. In
PR mode, repair `gh` authentication/connectivity; never treat an outage as
authority for an unaudited local merge.

An empty backlog should idle or terminate according to bounds. Do not create
speculative tasks simply to make the loop appear productive.

## Safe cleanup

Preview destructive or broad operations:

```bash
dacli cleanup --project <project> --dry-run
# Review the immutable plan, then apply that exact id:
dacli cleanup --project <project> --apply-safe <plan-id>
# Recover one quarantined generated artifact from its audit identity:
dacli cleanup --project <project> --restore <plan-id> --artifact <identity>
dacli worktree prune --dry-run
dacli ship --dry-run
dacli github push <project> --dry-run
```

Prefer `cleanup` for repository-wide decisions: it combines the canonical
worktree classifier with task, run/claim, PR, pushed-commit, protected-path,
and generated-artifact evidence. Apply accepts only the exact content-addressed
plan just reviewed and refuses when any observed state changes. Unknown,
ambiguous, dirty, unpushed, live, or nonterminal evidence is preservation, not
permission to guess. The narrower `worktree prune` remains useful when only its
local worktree policy is needed. Cleanup never quarantines process records,
outcomes, transcripts, or verification evidence. Eligible generated temporary
artifacts move into a plan-keyed workspace quarantine; the audit's recovery
command restores the exact digest and refuses to overwrite an occupied source.

`dacli project rm <project> --force` has no preview mode and irreversibly
deletes the selected project's local record. It is deletion, not recovery;
never run it merely to inspect or reconcile a backlog.

Never use broad recursive deletion, `git reset --hard`, or unreviewed
`git add -A` as recovery. Preserve unrelated dirty changes. Stage only claimed
paths, use `dacli commit` for agent attribution, and report anything that could
not be verified.

When dacli itself is defective, use:

```bash
dacli report "<symptom, suspected cause, and manual workaround>"
```

Include `--disclose` only when the human authority holder knowingly authorizes
sharing workspace or transcript context.
