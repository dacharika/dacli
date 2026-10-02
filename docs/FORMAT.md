# dacli on-disk format — v0

This is the stable interface. Any tool that can read YAML frontmatter and markdown can interoperate with `dacli` without linking it. The Go API is unstable; **this is not**.

`format: 0` in `config.yml` denotes a pre-1.0 format that may still change. From `1` onward, changes are additive.

---

## Common frontmatter

Every object file opens with YAML frontmatter. Fields present on all types:

```yaml
---
id: t-001                 # unique within its kind and project
kind: task                # project | task | note | queue | agent
created: 2026-07-21T14:03:11Z
created_by: a-01J8F3K9    # agent id
tags: [billing, urgent]
---
```

Unknown frontmatter keys are preserved verbatim on rewrite. Tools must not drop fields they do not understand — this is what keeps hand-editing and third-party tools safe.

Body content is free markdown. `[[wikilinks]]` may appear anywhere, in frontmatter values or body, and resolve against the whole workspace by object `id` or filename stem. An unresolved link is valid.

---

## Project — `projects/<slug>/project.md`

```markdown
---
id: p-ledger
kind: project
created: 2026-07-21T14:00:00Z
created_by: a-root
status: active            # active | paused | done | abandoned
stage: elicitation        # definition | elicitation | approach | design
landing.mode: local       # local | pr; absent in legacy records means local
landing.base: main        # optional non-empty target branch
tags: [billing]
---

# Migrate billing to the new ledger

## Vision
Long-term: one ledger of record for every money movement in the product.

## Goal
One sentence, near-term. This is what every child agent sees first.

## Constraints
- Writes to `balances` stay synchronous.
- No schema change before the Q3 freeze.

## Out of scope
- Refactoring the reporting pipeline.
- Anything touching the tax engine.

## Success criteria
- [ ] All write paths audited
- [ ] Shim passes the reconciliation suite
```

`## Vision`, `## Goal`, `## Constraints`, `## Out of scope`, and `## Success criteria` are **structural** — the context assembler reads them by heading. Other headings are free-form and are treated as body.

`## Out of scope` is emitted into **every** context brief in the project. Scope creep in an agent tree is not a client asking for more; it is a child agent deciding to also fix the adjacent thing. The boundary has to be in its context before the tokens are spent.

`stage:` places the project on the Cone of Uncertainty and widens every estimate inside it accordingly.

`landing.mode` and `landing.base` are the configured landing policy. Resolution
uses an explicit command-line override first, then these project fields, then
the legacy defaults (`local` mode and the consuming command's repository base).
An omitted base therefore preserves the repository's existing base resolution;
it is not persisted as an invented branch name.

---

## Task — `projects/<slug>/tasks/<status>/NNN-<slug>.md`

`<status>` is the containing folder: `open`, `active`, `blocked`, or `done`. It is deliberately *not* a frontmatter field; the folder is the single source of truth. A tool that disagrees with the folder is wrong.

```markdown
---
id: t-002
kind: task
created: 2026-07-21T14:06:20Z
created_by: a-root
owner: a-01J8F3K9         # the only agent that may rewrite this file
parent: [[t-001]]         # optional parent task; the parent chain is the WBS
priority: must            # must | should | could | wont
estimate:                 # three-point PERT; scalar estimates are rejected
  optimistic: 2
  probable: 5
  pessimistic: 14
depends_on:
  - on: [[t-004]]
    type: FS              # FS | SS | FF | SF; defaults to FS
traces:
  - internal/ledger/shim.go
  - internal/ledger/shim_test.go
tags: []
---

# Add the ledger write shim

## So that
reconciliation stops diverging at month end.

## Context
Why this task exists. Optional — the assembler synthesizes this from the
goal chain when absent.

## Acceptance
- [ ] Shim covers the nightly batch path
- [ ] Reconciliation suite green

## Log
Appended by `dacli sync` when child events are applied. Newest last.
```

**Identity, resolved.** The true `id` is `t-<ULID>`, assigned at creation — two agents creating tasks in the same instant cannot collide on it, because ULIDs don't collide and creation by a non-owner is an event file anyway. `NNN` is a display alias in the *filename only*, assigned by the project owner when the task is materialized at sync; a single allocator means no races. (Examples in this document use short ids like `t-002` for readability.) This closes what DESIGN.md carried as open question 5: the earlier text said `NNN` was "assigned at creation," which assumed a single allocator that a tree of concurrent agents does not have.

Task references accept a full `t-<ULID>` (or the ULID without `t-`), a slug,
a sequence with or without zero padding (`1` or `001`), and `NNN-slug`.
Because slugs and sequences are unique only within a project, every short form
also accepts a project qualifier: `<project>/<seq>`,
`<project>/<padded-seq>`, `<project>/<NNN-slug>`, or
`<project>/<slug>`. Bare forms remain valid when they identify exactly one
task across the workspace; otherwise the resolver reports ambiguity and emits
qualified suggestions. Generated mutating commands should use the task ULID,
which remains globally unique if projects or slugs change.

Four fields carry the SPM layer, and each one exists to block a specific agent failure:

- **`priority`** (MoSCoW) — `dacli next` will not recommend a `could` while a `must` is open. Agents reliably start with the tractable piece rather than the load-bearing one.
- **`estimate`** (PERT three-point) — scalar estimates are rejected. An agent asked for a number gives a confident point value; requiring `pessimistic` forces the unexamined risk to be stated.
- **`depends_on`** with a type — `SS` is what makes two tasks genuinely parallel-safe, and that is invisible in a plain `blocked_by`. It decides whether a parent may fan out.
- **`## Acceptance`** — a subagent with no acceptance criteria cannot know when to stop. This is the highest-value lint in the tool.

`blocked_by` from format v0's first draft is accepted as a synonym for `depends_on` with `type: FS`.

---

## Note — `projects/<slug>/notes/<kind>/<slug>.md`

`<kind>` is `decisions`, `findings`, `metrics`, or `refs`.

```markdown
---
id: d-sync-writes
kind: note
note_kind: decision       # decision | finding | ref
created: 2026-07-21T14:10:02Z
created_by: a-root
about: [[t-002]]          # optional: task or project this attaches to
tags: [architecture]
---

# Ledger writes stay synchronous

## Chose
Synchronous writes through the shim.

## Rejected
Async queue with eventual reconciliation.

## Because
Reconciliation cost exceeds the ~40ms latency win at current volume.
```

For `note_kind: decision`, the `## Chose` / `## Rejected` / `## Because` headings are structural — the assembler emits them into the **Constraints** section of every brief in scope. Recording what was *rejected and why* is the whole value; a decision note without `## Rejected` will be flagged by `dacli lint`.

For `note_kind: finding`, an optional `severity: major | moderate | minor` uses the review-technique definitions — major means the fix is not obvious and needs exploration, moderate means the fix is clear but needs review, minor means obvious or unnecessary. Severity lets the assembler rank findings by consequence instead of only by recency.

For `note_kind: metric`, `## Goal` / `## Question` / `## Metric` are structural, in that order:

```markdown
---
id: m-shim-correctness
kind: note
note_kind: metric
created_by: a-root
---

# Shim correctness

## Goal
Know whether the shim preserves balance integrity.

## Question
How often does a shimmed write produce a balance that reconciliation rejects?

## Metric
Rejected-reconciliation rate per 10k writes, tracked per deploy.
```

The ordering is enforced, not decorative: it is Basili's rule that you cannot pick a metric before stating the goal. An agent asked to "add some metrics" otherwise counts whatever is easiest to count.

For `ref`, the body is free-form.

---

## Risk — `projects/<slug>/risks/<slug>.md`

```markdown
---
id: r-batch-job-bypass
kind: risk
created: 2026-07-21T14:30:00Z
created_by: a-01J8F3K9
impact: high              # high | medium | low
likelihood: medium
tags: [billing]
---

# The nightly batch job bypasses the service layer

## Indicators
- Reconciliation diffs that appear only after 02:00 UTC.
- Balance rows with no corresponding service-layer audit entry.

## Action
Audit the batch job's write path before building the shim, not after.
```

`rank` is **computed, never stored**: high+high = 1 (mitigate now), high+medium = 2 (make a plan), anything with a low = 3 (monitor only), otherwise 2. Ranks 1 and 2 require an `## Action`; `dacli lint` flags a rank-1 risk without one. Rank 3 risks are deliberately allowed to have no plan — planning for everything is its own failure.

Risks are emitted into context briefs, with their indicators. A child working near a known risk is told what the early warning looks like, which is the only form in which a risk register does an agent any good.

---

## Glossary — `projects/<slug>/glossary.md`

```markdown
---
id: g-ledger
kind: note
note_kind: ref
created_by: a-root
---

# Glossary

- **balance** — the authoritative row in `balances`, not the cached figure in the API response.
- **shimmed write** — a write routed through `internal/ledger/shim.go`.
- **reconciliation** — the 02:00 UTC job comparing ledger sums to `balances`.
```

Emitted into every brief for the project. This is the direct counter to the vague-noun ambiguity category: one definition of each term that every agent in the tree sees, rather than each agent inventing its own.

---

## Queue — `queues/<slug>.md`

```markdown
---
id: q-release-checks
kind: queue
created: 2026-07-21T15:00:00Z
created_by: a-root
owner: a-root             # the only agent that may move the cursor
cursor: 2                 # index of the next step to run; 0-based
---

# Release checks

## Steps
1. `go test ./...`
2. `go vet ./...`
3. `git tag -a v0.2.0 -m "..."`
4. `goreleaser release --clean`
```

`dacli` **does not execute steps.** `dacli queue next <slug>` prints the step at `cursor`; the agent runs it and calls `dacli queue advance <slug>` (or `--fail` to halt the queue). Steps are markdown list items; a fenced code block under a step is part of that step.

Queues have an `owner` for the same reason tasks do: the cursor is mutable state, and two agents advancing it concurrently is a lost update. The first spec omitted this, which quietly violated the single-writer invariant — a queue was the one object anybody could rewrite. Only the owner advances; another agent that wants a queue stepped asks its owner, or claims the queue when it is unowned. A queue is walked by one agent at a time by design; it is a checklist, not a work-distribution mechanism.

---

## Agent — `agents/<id>.md`

```markdown
---
id: a-01J8F3K9
kind: agent
created: 2026-07-21T14:05:00Z
created_by: a-root
parent: [[a-root]]
grant: ro                 # rw | ro
role: auditor
token_hash: sha256:9f2c…  # the token itself is never stored
---

# auditor

Spawned to audit write paths into `balances`.
```

The printed token is shown **once**, at spawn. Only its hash is persisted. A lost token means spawning a new agent, not recovering the old one.

A child's `grant` may never exceed its parent's — attenuation is monotonic and enforced at spawn time.

---

## Event — `events/YYYY/MM/DD/<ULID>-<agent>-<kind>.md`

Append-only. **Never edited, never deleted** (only archived by `dacli events compact`). One file per event, so concurrent writers never contend.

```markdown
---
id: 01J8F3KA7QW3M8YRJ4V2N0XZ6P     # ULID; sorts by creation time
kind: event
event_kind: finding               # claim | release | finding | propose-status | comment | block
created: 2026-07-21T14:22:40Z
created_by: a-01J8F3K9
about: [[t-002]]
applied: false                    # set true by the owner's `dacli sync`
---

The legacy nightly batch job also writes `balances` directly, bypassing
the service layer entirely. Any shim that only wraps the service will
miss it.
```

`applied` is the one mutable field in the format, and it is written only by the owner of the referenced object during `sync`. This is the single sanctioned exception to append-only.

### Event kinds

| Kind | Meaning | Effect on `sync` |
|---|---|---|
| `claim` | Agent takes ownership of an unowned task | Sets `owner`, moves to `tasks/active/` |
| `release` | Agent gives up a task | Clears `owner`, moves to `tasks/open/` |
| `finding` | A result worth keeping | Creates a `finding` note, appends to `## Log` |
| `propose-status` | Suggests a folder move | Owner moves, or ignores |
| `comment` | Free text, no state change | Appends to `## Log` |
| `block` | Marks blocked, with `blocked_by` | Moves to `tasks/blocked/` |
| `help` | A blocking question, routed to the role owning the path | Blocks the asking task until answered |
| `answer` | Resolves a help request | Promoted to a `decision` or `finding` note |
| `run` | A shortcut invocation and its exit status | Recomputes the shortcut's derived `uses` count |

---

## Role — `.dacli/roles/<name>.md`

Full specification and rationale in [TEAM.md](TEAM.md). Frontmatter: `name`, `summary`, `skills`, `scope`, `out_of_scope`, `shortcuts`, `grant`, `escalate_to`, `wip`.

Two rules that matter for anyone writing these by hand: `out_of_scope` always beats `scope`, and an empty `scope` means no fence rather than no access. Globs support `**` for any number of path segments and `*` within one segment.

`grant` is a ceiling *request*. Capability attenuation still wins, so a role file cannot hand a child more than its parent holds.

## Shortcut — `.dacli/shortcuts/<name>.md`

Full specification in [SHORTCUTS.md](SHORTCUTS.md). Frontmatter: `name`, `summary`, `command`, `effect`, `params`, `roles`, `dir`, `uses`.

`command` is a template with `{{param}}` placeholders and `[[ ... ]]` optional groups. Every substituted value is POSIX-quoted unless its param declares `raw: true`. `effect` is `read`, `write`, or `destructive`; a shortcut with no declared effect does not run, because defaulting to `read` would let a frontmatter typo silently downgrade a deploy.

`uses` is **derived, never incremented in place**. Every invocation is a `run` event; `dacli sync` recomputes the count from the log. The first spec had runners incrementing the field directly, which would have made the shortcut file the one object every agent in the tree writes concurrently — the exact contention the event log exists to prevent. Treat `uses` as a read-only cache.

The file body should record *why the command has this exact shape* — the flag that took an hour to find is the part worth keeping.

## Runtime — `.dacli/runtimes/<name>.md`

Historical adapter documents may remain in existing workspaces. Fields such as
binary, invoke arguments, capabilities and environment names describe past
launcher configuration. Current source does not provide runtime adapters or
execute these files. Preserve them when needed to interpret retained receipts;
do not put credential values into committed workspace documents.

## Template — `.dacli/templates/<name>/template.md`

**Implemented.** Full design in [TEMPLATES.md](TEMPLATES.md).

Declares `process` (informational), `roles`, `definition_of_done`, and `stages` with exit predicates. Sibling folders `docs/`, `roles/`, and `shortcuts/` hold what the template seeds.

The predicate vocabulary is small and non-scriptable by design. The one rule to internalize when writing a gate: `sections:` checks that a section is **filled**, not that it exists — empty, placeholder-bearing (`TBD`, `{{...}}`), or major-severity-ambiguous content does not satisfy a gate.

## GitHub mapping — task frontmatter

**Implemented.** Full design in [GITHUB.md](GITHUB.md).

```yaml
github:
  issue: 42
  node_id: I_kwDO...
  project_item: PVTI_...
  synced_at: 2026-07-21T18:04:00Z
  remote_updated_at: 2026-07-21T18:02:11Z
```

Local markdown is the source of truth; GitHub is a projection that can be deleted and regenerated. The mapping lives in frontmatter rather than a side database so it is diffable and versioned with the task.

Mirrored issue bodies also carry `<!-- dacli:<task-id> ws:<workspace-id> -->`, so a lost mapping is recoverable by search instead of by creating a duplicate. Duplicate issues after a retried timeout are the characteristic failure of naive syncers.

## Skill — `.dacli/skills/<name>/skill.md`

**Implemented.** Full design in [SKILLS.md](SKILLS.md).

A skill directory: frontmattered `skill.md` plus optional resources. Deliberately the richest native target's shape with dacli extensions (`min_delivery`, `est_tokens`) as ignorable extra keys — a dacli skill is a valid native skill verbatim, `skill import` ingests existing skill trees losslessly, and compilation only ever goes outward toward poorer targets.

Compiled output lands in `.dacli/build/skills/<runtime>/<role>/` — gitignored, regenerable, never edited; the same projection doctrine as GitHub. A skill's executable resources compile to shortcuts on targets that can't carry scripts, inheriting effect gates.

## Prompt override — `.dacli/prompts/<name>.md`

A text/template file that replaces the embedded prompt of the same name — the registry and the rules are in [PROMPTS.md](PROMPTS.md). Prompt tuning is thereby a workspace commit: attributable, revertible, and auditable like every other piece of state.

## Reserved paths

`.dacli/config.yml`, `.dacli/events/`, and `.dacli/agents/` are managed by `dacli`. Everything under `projects/` and `queues/` is intended for hand-editing and third-party tooling.

## Obsidian compatibility

The layout is a valid Obsidian vault as-is. Frontmatter is standard YAML; links are standard `[[wikilinks]]`; folders nest normally. No plugin, no configuration, no export step.
