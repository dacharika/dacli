# Critical path and GitHub landing

List existing open and active tasks, inspect open GitHub issues, and preview inbound adoption with `github pull <project> --dry-run` (`github sync <project> --dry-run` previews both directions); then compare the proposed issue titles before adoption. Pull prevents duplicate issue mappings but does not perform semantic deduplication. Before pull, make each issue's `## Acceptance criteria` checkbox list independently checkable. If an adopted task needs criteria from its mapped issue, use `task acceptance migrate <ref> --dry-run` and apply only the exact content-addressed plan. After `github pull <project>`, estimate with `task estimate <ref> --estimate o,m,p`.

Create a locally planned dependent task with `task add "<title>" --project <project> --depends-on <ref[:TYPE]> --accept "<observable criterion>"`. Refine an existing or GitHub-adopted task with `task depend <ref> --add <dep[:FS|SS|FF|SF]>` and `--remove`; project-qualified references use `<project>/<ref>`. The command resolves stored edges to stable task IDs and validates missing or ambiguous references, self-edges, dependency types, and cycles before writing. A non-owner records a replay-safe proposal that the owner materializes with `sync`, preserving the task's GitHub mapping and audit history. Keep dependency-sensitive waves stopped until the recorded graph is truthful; then use `critical-path --project <project>` and `next --project <project> --parallel <width>` as scheduling aids, never as permission to violate WIP, claims, or review gates. After a verified task returns, use this minimum handoff:

```bash
dacli sync
dacli task check <ref> --n <n> --verify "<command>"
dacli push <ref>
dacli pr --task <ref> --with-verdicts
dacli pr status --task <ref>
dacli accept <ref> --verify "<command>"
dacli retro <task-or-project-ref> --well "..." --bad "..." --improve "..."
```

Read the acceptance summary as two facts: `newly_checked` is this invocation's
delta, while `satisfied/total` is the final criterion state. A zero delta with
a nonzero total means the boxes were already satisfied. `unverified: true`
instead means the owner explicitly closed a task that had no criteria; never
interpret those states as equivalent.

Keep GitHub as the visible projection: preview outbound changes with `github push <project> --dry-run`; push task branches with `push <ref>`, then open them with `pr --task <ref> --with-verdicts`. Before owner acceptance or issue closure, observe both the merged PR through `pr status` and its commit on freshly inspected trunk. `ship` is the separate wave transaction that owns accept-plus-integrate for its reviewed task window. Add `--auto` only when the repository's required checks and review policy make auto-merge trustworthy. A merge, CI result, or API call that cannot be observed is unverified. Never create tags or releases as part of ordinary loop work.

When one product task genuinely needs several independently reviewed PRs, keep
one parent lifecycle and create typed delivery child tasks:

```bash
dacli slice add --task <parent> --title "<bounded delivery>" --accept "<criterion>"
dacli pr --slice <parent>/g<generation>
dacli slice reconcile --task <parent> --json
dacli task progress <parent> --json
```

Required slices become ordinary FS dependencies of the parent, so `next` and
critical-path ordering see them without a second ledger. Each slice has its own
generation-scoped branch and exact PR/head/tree/merge evidence. Partial PRs
reference the parent's GitHub issue; they do not close it. Close the parent only
when `ready_to_close` is true after a fresh reconciliation. Corrective work
requires a reopened parent or a new slice generation—never reuse an old merged
slice as proof for a newer head.
