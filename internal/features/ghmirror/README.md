# GitHub mirror milestone behavior

`dacli github push <project>` retains the historical default: every mirrored
task issue is assigned the project title as its GitHub milestone (or the
project slug when the title is blank). This compatibility behavior can replace
human-selected per-issue milestones when the command is run without an explicit
plan.

Use `dacli github push <project> <canonical-task-id>... --milestone-plan plan.json
--dry-run` to preview or apply an owner-reviewed, exact set of milestone
changes. A plan has schema `github-milestone-plan/v1` and pins the project,
repository, canonical task ID, mapped issue number, task digest, current remote
milestone, and current remote labels for every selected task. The task digest
is `sha256:` plus the lowercase SHA-256 of compact JSON with fields `id`,
`project`, `status`, and `title` in that order. `expected_labels` is the full
current label-name array; label order does not matter.

Each assignment names an already existing target milestone. Plans must be
non-empty, cover the exact explicit task window once, and match the current
task, issue mapping, milestone, and label snapshot. A stale or conflicting
plan is refused before any remote write. Unlisted tasks are never included or
assigned the project default in plan mode. Plan mode changes only labels and
milestones for those mapped issues; it does not create/adopt issues, close
issues, or publish findings or decisions. `--dry-run` shows the exact before
and after milestone and label sets and performs no remote writes.

Plan JSON rejects duplicate keys at any nesting level, is limited to 1 MiB and
1,000 assignments, matching the mirror's issue window. Every repository label
that the selected issue edits need must already exist. The command checks this
before applying or previewing the plan and refuses with setup guidance when a
label is missing; create required labels separately, then refresh the plan.
Plan mode never creates, edits, or deletes repository-wide label definitions.

GitHub does not provide a transaction that atomically checks the complete task
and issue snapshot while editing it. A real push revalidates the plan before
writing, but a remote human edit can still race between that read and the
individual issue edit.
