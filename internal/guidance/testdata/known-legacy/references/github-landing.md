# GitHub collaboration and landing

## Contents

- Source-of-truth model
- Linking and diagnostics
- Issue synchronization
- Pull requests and reviews
- Integration and shipping
- Releases and GitHub Apps

## Source-of-truth model

Use GitHub as the shared collaboration surface for orchestrator agents, coding
agents, and humans; use dacli as the canonical execution/evidence record. Keep
them synchronized instead of allowing two independent backlogs:

- GitHub issues expose work across agent sessions and to external contributors.
- dacli tasks carry scheduling, dependencies, acceptance, ownership, briefs,
  events, and verification.
- PRs and checks govern landing.
- Marker-backed synchronization preserves identity and idempotency.

When project policy declares GitHub the main collaboration source, create or
confirm the issue first, then adopt it with `github pull`. Still record agent
execution and acceptance through dacli.

## Linking and diagnostics

```bash
dacli github link <project>
dacli github link <project> --allow-public
dacli github projection <project> --json
dacli github doctor
dacli project show <project>
dacli project show <project> --landing-mode pr --landing-base main
```

`--allow-public` records only the task/acceptance/reference public-safe
projection. Broader exact-repository authority is a separate
`github link <project> --allow-public --allow-internal` decision, and a
publisher must still request it with `github push --include-internal` or
`pr --with-verdicts`. Inspect the shared CLI/MCP policy with
`github projection`; unknown visibility fails closed to public-safe.

The shipped integration uses the authenticated `gh` CLI. A GitHub App is a
future installation-scoped control-plane option, not required for local or
single-maintainer operation. Prefer an App when centralized installations,
webhooks, organization policy, or service-mode credentials become requirements.

## Issue synchronization

Preview each synchronization direction before applying it:

```bash
dacli github push <project> --dry-run
dacli github sync <project> --dry-run
dacli github pull <project> --dry-run
```

Then apply the reviewed direction:

```bash
dacli github pull <project>
dacli github push <project>
dacli github sync <project>
dacli github project <project> --dry-run
```

`pull` adopts human-authored issues as tasks. `push` mirrors tasks and selected
records outward. `sync` pulls then pushes. Review dry-run counts carefully:
task-windowed operations may still include project-level decisions/findings,
depending on flags and policy.

Check for semantic duplicates before creating an issue. An issue can be closed
but cannot be unpublished. Include:

- Observed symptom or user outcome.
- Reproduction/evidence.
- Suspected cause, labeled as a hypothesis.
- Manual workaround.
- Checkable acceptance criteria.
- Explicit non-goals and safety constraints.

## Pull requests and reviews

Let `spawn --worktree` create the canonical `dacli/NNN-slug` task branch when
possible. Branch naming is part of task-to-branch resolution.

```bash
dacli push <ref>
dacli pr --task <ref> --with-verdicts
dacli pr --task <ref> --with-verdicts --auto
dacli pr status --task <ref>
```

Use `--auto` only when repository branch protection and required checks make
GitHub auto-merge trustworthy. Otherwise leave the PR open for review.

Select durable PR landing (`landing.mode: pr`, with `landing.base`) when GitHub
checks, required reviews, and PR discussion are the collaboration boundary.
Persist it with the `project show --landing-mode pr --landing-base main` form
above; despite the read-oriented command name, those flags update the policy.
CLI flags explicitly override project configuration; the legacy default is
local. Effective PR mode fails closed on remote errors. Diagnose with `dacli
github doctor` and `dacli pr status --task <ref>`, then rerun to reuse the
canonical task branch and recorded PR.

Review against the task brief and acceptance criteria. File defects both in
dacli findings and the PR review when humans need to see them. Use
`--approve`/`--request-changes` for real review states, not ambiguous comments.

## Integration and shipping

Use dacli's landing commands because raw `gh pr merge` omits task events,
verdicts, acceptance state, and record commits:

```bash
dacli integrate --tasks <refs> --into main --pr --no-merge
dacli integrate --tasks <refs> --into main --pr
dacli integrate --tasks <refs> --into main --pr --auto
dacli ship --into main --pr --dry-run
```

Run integration from the target branch checkout. Merge overlapping PRs one at
a time and re-check mergeability after each landing. `integrate` requires named
tasks to be done unless an explicit owner override is justified.

`ship` owns a wave accept-plus-integrate transaction: it closes a wave by
accepting and integrating only the tasks this run closes, committing the dacli
record, and optionally pushing. Use `--tasks` to constrain both the preview and
the real integration window. Dry-run simulates the same acceptance transition
as apply, so an explicit active-task window has the same eligibility result in
both modes.

Wait for required CI before merge. A green local tree does not substitute for
the repository's required checks, and a green CI run does not prove an unmerged
task branch reached trunk. On the direct-PR path, observe both the merged PR and
its commit on freshly fetched trunk before owner acceptance. Only then
synchronize/close the GitHub issue. This is distinct from `ship`, which owns the
reviewed wave's accept-plus-integrate transaction.

`accept --verify` resolves required checks from project configuration, legacy
branch protection, and every active repository or organization ruleset that
GitHub evaluates for the exact target branch. Inspect `accept --json` when an
agent needs the merged names and provenance. An unread policy is a refusal, not
an empty requirement list. Repair GitHub access first; use
`--allow-unobservable-check-policy` only as an explicit owner exception, which
is written to the task's audit log and does not waive an observed red or stale
check.

## Releases and GitHub Apps

Preview release engineering:

```bash
dacli github release <project> <tag> --dry-run
```

Creating or pushing a version tag is publication. Do it only with explicit
authority, even when release configuration and snapshot verification are in
scope.

Do not require a GitHub App merely to use dacli. The current `gh` adapter is
appropriate for local-first and maintainer-driven workflows. Consider an App
as a separate control-plane adapter when dacli needs webhook-driven operation,
installation-scoped permissions, organization-wide deployment, multi-user
identity, or hosted automation. Docker changes the execution envelope; it does
not itself provide GitHub identity or project isolation.
