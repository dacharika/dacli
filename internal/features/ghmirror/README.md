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

## Link existing issues to canonical tasks

Use `dacli github issue link <project> <issue-number[=task-id]>... --dry-run`
to preview an exact selection of existing GitHub issues and existing canonical
tasks. Each issue number appears once and each task is named once. A task ID may
be omitted only when the issue title has one exact match in the configured
project. The preview fetches only the selected issues, resolves the linked
repository and visibility, and saves a digested plan under
`.dacli/plans/issue-links/`.

Apply with `dacli github issue link --apply <plan-id> --owner-config <private-owner-file>`.
Apply refreshes the repository/project binding, public
task-only consent, selected issue bodies and titles, and selected task bytes.
It refuses stale or conflicting mappings, ambiguous/missing targets, and
retained native leases or reservations. It writes the `github` mapping and a
task-log provenance line containing the issue-body digest. It never creates,
edits, comments on, or closes a GitHub issue and never imports acceptance
criteria; use `dacli task acceptance migrate` for that separate authorized
workflow.

The project and complete canonical mapping inventory are refreshed after the
selected issue reads and again at each local write boundary. The boundary uses
the established native-authority-then-task-lock order. These are cooperative
observations: the project/task store has no workspace-wide transaction, so a
non-cooperating writer can still race the final local read. No authority lock is
held across GitHub reads, and this workflow does not claim a distributed
transaction with GitHub.

Selected local task files are validated and staged together before writes. Each
selected write is durable on its own; a stopped multi-task apply can leave only
part of the explicit selection applied. Its private bounded regular-file
recovery journal is under `.dacli/recovery/issue-links/`; rerun the same
`--apply` plan to finish those selected changes. The journal's applied flags
never override observed task bytes: a task restored after an applied entry is
refused for owner review. This does not claim an atomic multi-file filesystem
commit.
