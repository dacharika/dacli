# GitHub Issues and Projects

**Status: the bidirectional Issues mirror (the G-series) is implemented** in the `ghmirror` slice (`internal/features/ghmirror/ghmirror.go`) and the PR-enrichment path in the `vcs` slice (`internal/features/vcs/lifecycle.go`). What ships today, and where it still stops:

- **Outbound push (`dacli github push <project>`)** projects local state onto Issues through the versioned `github-publication/v1` policy and is marker-idempotent (§ 4). Public or unknown visibility defaults to the public-safe task/acceptance/reference allowlist; internal evidence is withheld. `--closure-only` requires explicit mapped done-task refs and closes only those issues without publishing bodies, findings, decisions, labels, or milestones.
  - **Tasks → issues.** Each task becomes an issue whose body carries the acceptance criteria and a recovery marker; a single `status:<folder>` label (`status:open|active|blocked|done`) mirrors the task's status folder; the issue is assigned to the **project milestone** (§ 9.7); a **done** task's issue is **closed** best-effort; the issue number is written back into the task's `github:` frontmatter block as a backlink.
  - **Decisions → labeled issues.** Each decision note becomes an issue labeled `decision` whose body is the *why* — Chose / Rejected / Because — plus a backlink to the note.
  - **Findings → issue comments.** Each finding note about a task is posted as a comment on that task's issue, idempotent by a per-finding marker so a re-push never duplicates.
- **Inbound pull (`dacli github pull <project>`)** adopts human-authored issues as local tasks: any issue *without* a dacli marker and *not* already mapped to a task seeds a new task (title + body → its Context), with the `github:` block written back so the next pull/push treats it as linked. Idempotent by issue-number mapping, not by editing the remote.
- **`dacli github sync <project>`** = pull then push, so a freshly adopted issue is mirrored back on the same invocation.
- **PR enrichment + verify verdicts (`dacli pr`).** Private repositories retain the explicit rich projection. Public/unknown visibility gets title, task summary, acceptance, and `Refs #<issue>`; findings and verdicts need separately recorded exact-repository authority plus `--with-verdicts`. Only an accepted terminal task emits `Fixes #<issue>`. `--dry-run` prints the exact title/body and typed withheld reasons.
- **Planning artifacts (§ 9.7).** A project maps to a **milestone** (task issues assigned on push); **`dacli github codeowners`** projects role `scope` globs into a `.github/CODEOWNERS`; **`dacli pr --draft`** opens work-in-progress PRs.
- **The disclosure gate (§ 7), in full.** `github link --allow-public` records consent only for public-safe fields. `--allow-internal` is a second exact-repository authority and must be recorded with it; a publisher must still explicitly request internals (`github push --include-internal` or `pr --with-verdicts`). Visibility is re-checked live and unknown fails closed to public-safe. Inspect the shared text/JSON/CLI/MCP policy with `dacli github projection <project> [--include-internal] [--terminal] [--json]` / `github_projection`.
- **Not yet**: Projects-v2 fields (§ 2's priority/estimate columns), status changes as `propose-status` *events* (pull adopts issues directly as tasks today rather than recording inbound events per § 3), conflict reconciliation with `remote_updated_at` (§ 5), batching/backoff (§ 6). The `gh` subcommands used are assumptions until `github doctor` grows probes beyond binary+auth+repo+visibility.

`dacli` mirrors its workspace to GitHub Issues through the `gh` CLI, so humans can see and steer agent work from where they already coordinate. Credentials are `gh`'s own — dacli never handles, stores, or prompts for a token — and every `gh` call runs under a 120 s deadline so a wedged request cannot hang the CLI or, under `dacli mcp serve`, the stdio loop.

## Collaboration boundary: shipped CLI, proposed App

### Shipped today: `gh` CLI adapter

The behavior described as shipped in this guide is operator-run CLI behavior. It executes `gh` as the current user, uses that user's existing `gh auth` session, and makes remote calls only when a `dacli github ...`, `dacli pr`, or PR-first integration command is invoked. The source tree now has a generic control-plane API/worker lifecycle skeleton, but it has no webhook receiver, GitHub App service, installation-token exchange, or continuously running GitHub worker.

No App is needed for a developer, a small team, or a self-hosted swarm that already runs in a trusted checkout and can authenticate `gh`. This is the simplest boundary: GitHub sees the operator's identity and permissions; dacli stores no GitHub secret; sync is explicit; and the local markdown record remains usable offline. Prefer this mode unless the workflow actually needs unattended, organization-managed GitHub events.

### Proposed: installation-scoped GitHub App adapter

An App becomes valuable when an organization needs unattended operation across repositories, centrally revocable installations, audit records that distinguish the automation from a human, or event-driven reactions without polling. That adapter is a proposal, not shipped behavior. It would be an additional port onto the same local projection rules, not a replacement source of truth and not permission for GitHub to mutate workspace files directly.

Its authority should be installation-scoped and least privilege: install it only on selected repositories; request issue, pull-request, metadata, and checks permissions only for operations enabled there; keep contents read-only unless a named workflow must push a branch; and do not request organization-wide administration. Installation tokens should be short-lived and exchanged outside agent prompts and transcripts. A deployment that only imports issues needs less authority than one that creates PRs or enables auto-merge, so those capabilities must remain separable.

The event trust boundary matters as much as the credential boundary. A valid webhook signature proves GitHub delivered a payload; it does **not** make the issue body, comment, actor, or linked content trusted instructions. A future receiver must verify the signature and installation/repository identity, reject replays, persist the raw delivery idempotently, and translate allowed actions into attributed dacli events. The workspace owner then applies those events under dacli's existing ownership and grant rules. It must never feed webhook text to an agent as system instructions or let a remote close/comment bypass the proposal boundary described in § 3.

### Docker is an execution envelope

Docker can package the dacli binary, `git`, an agent runtime, and `gh` into a reproducible process boundary. It can also limit mounted credentials, network reach, CPU/memory, and which checkout a worker can write. That makes it a useful **execution envelope**, not a scheduler, trust policy, GitHub identity, or replacement for dacli's grants and path claims. A container still acts with every credential and mounted path it receives.

Docker support is **not shipped**: this repository currently publishes no supported image, Dockerfile, Compose topology, or container lifecycle contract. A containerized or managed runner remains future control-plane work under [GitHub issue #446](https://github.com/mlnomadpy/dacli/issues/446); image construction, credential mounts, UID/file ownership, worktree sharing, and shutdown/recovery still need their own executable contract before dacli can claim support. Likewise, “swarm” below means dacli's coordinated agent workflow; it does not claim Docker Swarm mode support.

## GitHub-first swarm workflow

This is the end-to-end path when humans start and review work in GitHub while dacli keeps the durable execution record:

1. **Map the issue.** Link once with `dacli github link <project>` (`--allow-public` is public-safe; `--allow-internal` is a separate decision), inspect `dacli github projection <project> --json`, then run `dacli github pull <project>`.
2. **Bind work before writing.** Select ready canonical work, then use the native host to create an isolated checkout. Register the external attempt and acquire a scoped native lease. Administrative `worktree add` retains the derived task branch convention; a native external checkout must be bound explicitly rather than inferred from its branch name.
3. **Work in the worktree and record evidence.** Make only the claimed changes, add findings/decisions as they become known, run the task's checks, and mark only acceptance criteria actually proved. Commit with `dacli commit "<message>" --task <ref>` so the commit carries agent, role, and task provenance.
4. **Open the PR.** Push with `dacli push <ref>`, preview exact policy/title/body with `dacli pr --task <ref> --dry-run`, then open it. On public repos use plain `pr` for the safe projection; use `--with-verdicts` only after exact-repository internal authority. Nonterminal PRs use `Refs #...`; only accepted terminal tasks use `Fixes #...`.
5. **Wait for CI; do not infer.** Leave a pending or red PR open. Use GitHub's checks and `dacli pr status --task <ref>` to distinguish `landing`, `merged`, `orphaned`, and `unknown`; a stale local branch comparison is not proof. `dacli integrate --pr` merges only after reported checks pass, while `--auto` delegates the wait and merge to GitHub auto-merge.
6. **Integrate through dacli.** Once the task is accepted, land it with `dacli merge --task <ref>` or as part of `dacli integrate --pr` / `dacli ship --pr`. This records the landing and cleans up the worktree; a hand-written `git merge` skips that lifecycle.
7. **Close both views.** For an explicitly selected nonterminal task, `dacli ship --tasks <ref> --pr --verify "<cmd>"` merges only after checks pass, fetches and inspects the configured base, runs post-landing verification, accepts locally, and then runs `github push <project> <ref> --closure-only` so the issue cannot close early and private findings/decisions are not published as a side effect. A PR opened for an already-done task retains `Fixes #...`. If a task was integrated without PR-first closure, the next explicitly authorized GitHub projection closes the issue.
8. **Push both records deliberately.** Preview `dacli github push <project> <task-ref> --dry-run` after landing. Private repos retain the rich projection; public repos publish safe fields unless both recorded authority and `--include-internal` are present. Git push, GitHub projection, and record-branch persistence remain separate actions.

---

## 1. The one decision everything else follows from

**Local markdown is the source of truth. GitHub is a projection.**

Not a peer, not a backend, not "the real store with a local cache." A projection, synced explicitly, that can be deleted and regenerated.

This is worth being firm about because the alternative is genuinely tempting — GitHub has identity, permissions, notifications, and a UI, and it would be less code to just put tasks there. Four reasons not to:

1. **`dacli context` is the hot path and must never touch the network.** It runs on every spawn, potentially dozens of times a minute across a tree. Network latency on the core operation would make the tool feel broken, and an offline agent would be a dead agent.
2. **The concurrency model depends on local files.** Contention-freedom comes from every cross-agent write being a new ULID-named file that cannot collide. GitHub is a shared mutable store with no such property, and a tree of agents writing issues concurrently will hit secondary rate limits and interleave badly.
3. **Rate limits are a hard ceiling on fan-out.** The whole design is about running many agents at once. Coupling agent throughput to an API quota caps the thing the tool exists to do.
4. **Git already versions the workspace.** Task history, decision history, and the event log are in commits, reviewable in diffs, bisectable. Issue history is in a database you do not own.

The cost of this choice is that the two can diverge, and § 5 is about handling that honestly rather than pretending sync is atomic.

## 2. Object mapping

| `dacli` | GitHub |
|---|---|
| Project | A GitHub Project (v2), optionally a milestone |
| Task | An Issue |
| Task status folder | Project status field (`open` / `active` / `blocked` / `done`) |
| MoSCoW priority | Project single-select field |
| Three-point estimate | Project number field, carrying `Te` |
| Task dependencies | Issue body task list + `blocked-by` label |
| Risk (rank 1–2) | Issue labeled `risk`, `rank-1` |
| Finding (major) | Issue comment on the task |
| Decision | Issue comment, plus a pinned summary comment |
| Help request | Issue with `needs-answer`, assigned to a human |
| Agent | Not mirrored — agents are ephemeral, humans are not |

Decisions and findings mirror as **comments, not issues**, because they are commentary on work rather than work. Creating an issue per finding turns the tracker into noise within a day. (Findings ship exactly this way — § 9.1; **decisions ship as labeled issues** in the current build, not comments — see the top-of-file status and § 9.1.)

**Agents are deliberately not mirrored.** A tree that spawns forty children would create forty GitHub artifacts representing processes that lived for ninety seconds. The `agent tree` view stays local.

## 3. Sync direction

**Outbound (`dacli` → GitHub)** is the default and covers structure: projects, tasks, status, priority, estimates, risks.

**Inbound (GitHub → `dacli`) arrives as events, not as writes.** This is the piece that makes the whole thing fit:

| Human action on GitHub | Becomes |
|---|---|
| Comment on an issue | `comment` event |
| Closing an issue | `propose-status` event |
| Moving a card in the Project | `propose-status` event |
| Adding a `blocked` label | `block` event |
| Answering a `needs-answer` issue | `answer` event → promoted to a decision note |

A human commenting on GitHub is structurally identical to a child agent appending a finding: an outside party contributing to an object it does not own. The event log already handles exactly that, so inbound sync needs **no new concurrency machinery at all**.

It also preserves the invariant that only an object's owner rewrites it. A human closing an issue does not move a file — it proposes a status change that the owner applies on `dacli sync`. Nothing races.

> **Shipped behavior (G4).** The events model above is the design target; what `dacli github pull` implements today is the first, load-bearing half of it: **a human-authored issue is adopted as a new local task.** `pull` lists every issue (open and closed) via the strongly-consistent list endpoint and, for each issue that carries **no** dacli marker (so it is not our own projection) and is **not** already mapped to a task, calls `store.CreateTask`, seeding the task's Context with a backlink and the issue body and writing the `github:` block back so the issue is never re-imported. Idempotency here is **number-mapping**, not a body marker, precisely because `pull` never edits the remote. Inbound *comments* and remote status changes as `propose-status` events are still the planned continuation — see the top-of-file status.

## 4. Identity and idempotency

Mapping lives in task frontmatter:

```yaml
github:
  issue: 42
  node_id: I_kwDO...
  project_item: PVTI_...
  synced_at: 2026-07-21T18:04:00Z
  remote_updated_at: 2026-07-21T18:02:11Z
```

Local, diffable, and versioned with the task. No separate mapping database to lose. (The current build writes the load-bearing `issue` and `repo` keys into this block; the timestamp/node-id fields above are the fuller design target.)

Every mirrored issue body also carries a marker:

```html
<!-- dacli:t-002 ws:01J8F3K9 -->
```

so that a lost or corrupted mapping is **recoverable by search rather than by duplication**. Duplicate issues are the characteristic failure of naive syncers: a retry after a timeout that already succeeded creates a second issue, and nothing ever notices.

The create path is therefore: check frontmatter → search by marker → only then create. A sync that is interrupted at any point and re-run must converge to the same state. **Recovery reads issue bodies via the strongly-consistent list endpoint and matches the marker by exact substring — deliberately NOT `gh issue list --search`**, whose index is eventually consistent (a fast retry after a create-then-crash would find nothing and duplicate) and tokenized (it strips the angle brackets and colons in the marker). Decisions and finding comments carry distinct marker prefixes (`<!-- dacli-decision:… -->`, `<!-- dacli-finding:… -->`) so the three kinds are never confused for one another or re-adopted across kinds.

## 5. Conflicts

Divergence is normal, so the policy is per-field rather than global:

| Field class | Winner | Why |
|---|---|---|
| Structure (title, body, deps, priority, estimate) | **Local** | Authored by agents against the workspace; GitHub edits to these are overwritten and the sync says so |
| Discussion (comments) | **Remote, inbound only** | `dacli` never edits or deletes a human's comment |
| Status | **Neither — proposal** | Remote status changes become `propose-status` events for the owner to apply (§ 3) |
| Labels | Union | Cheap, and humans use labels for their own purposes |

`remote_updated_at` detects a remote structural edit; the sync reports it as an overwrite rather than silently clobbering someone's typing. (This per-field reconciliation is the design target; the current build's push is authoritative-local for structure and never rewrites a remote comment, and `pull` only *adds* tasks — it never overwrites a remote edit.)

## 6. Rate limits and batching

- **Sync is explicit** (`dacli github sync`) and authorized. It is never implicit in `context`, `status` or native evidence registration.
- Batched through `gh api --paginate`; Projects v2 needs GraphQL, so field updates go in one mutation per batch rather than one per item.
- Exponential backoff on 403/429, with the remaining quota reported.
- `--dry-run` prints exactly what the run would create, adopt or close and writes nothing — remote or local. It is the correct default habit for a first sync, and is available on every remote-mutating command: `push`, `sync`, `pull`, `project`, `release` and `codeowners` (dacli 294). The preview runs the command's REAL read-and-decide path and only elides the writes, so it can never drift from what the real run would do. Because it writes nothing, `release --dry-run` does not require the `rw` grant a real cut does; the disclosure gate still runs, so a preview that would be refused reports the refusal.
- A large first sync is chunked and resumable — creating 200 issues is a quota event.

`gh` is required and must be authenticated. `dacli github doctor` probes for the binary, auth, repo access, and visibility, and — as with runtimes — **the exact `gh` subcommands used are verified by probing rather than assumed from documentation.** (Batching and backoff are the design target; the current build issues one `gh` call per object under a per-call 120 s deadline. `--dry-run` is implemented across the remote-mutating commands, dacli 294.)

## 7. Safety

**A public repository makes every mirrored artifact public.** Findings and decisions routinely contain internal reasoning, file paths, architecture detail, and occasionally things nobody meant to publish. Mirroring a workspace to a public repo is a disclosure event.

So:

- **`dacli` checks repository visibility and refuses to push to a public repo without explicit per-project confirmation**, recorded in the project file (`github_public_confirmed: true`, written by `github link --allow-public`). Not a flag that can be passed once and forgotten in a script. The check is re-run **live at every push** (`disclosureGate`), so a repo flipped public after linking re-trips the gate, and the finding-comment path rides the same gate.
- Push is **per-project opt-in** and **operator-triggered** — `dacli init` never enables it, and neither a commit nor native evidence registration grants publication authority. `github pull` is inbound and read-only against the remote, so it is deliberately **not** gated: adopting an issue discloses nothing.
- A `private: true` note is never mirrored, in either direction.
- Credentials come from `gh`'s own auth. `dacli` never handles, stores, or prompts for a token.
- **Issue and comment bodies are untrusted input.** Anyone who can comment on a public repo can write text that a subsequent agent reads. Inbound content is data, never instruction — it is attributed to its GitHub author in every brief it reaches, and a `comment` event from an outside account is marked as such. This is the same cross-tree injection problem as [RUNTIMES.md § 12](RUNTIMES.md), with a wider door.

That last point deserves emphasis: enabling inbound sync on a public repo lets strangers put text into your agents' context. That may be acceptable, but it must be a decision rather than a side effect.

## 8. Commands

| Command | Purpose |
|---|---|
| `dacli github doctor` | Probe `gh`, auth, the repo, and its **visibility** (warns if PUBLIC) |
| `dacli github link <project> [--allow-public [--allow-internal]]` | Bind a project; public-safe consent and internal-evidence authority are separate (§ 7) |
| `dacli github projection <project> [--include-internal] [--terminal] [--json]` | Read-only `github-publication/v1` allowlist and withheld reasons, shared with MCP |
| `dacli github push <project>` | Outbound: tasks → issues (+ status label, close-on-done, backlink, **project milestone**), decisions → labeled issues, findings → issue comments |
| `dacli github pull <project>` | Inbound: adopt human-authored issues as local tasks |
| `dacli github sync <project>` | Pull then push |
| `dacli github release <project> <tag>` | Cut a tagged release with **generated notes** on the linked repo (`--notes` overrides, `--target`/`--title`/`--draft`/`--prerelease` pass through). Idempotent: an existing release for the tag is reported, never duplicated. Needs an `rw` grant; **not** disclosure-gated — generated notes are the repo's own merged-PR/commit history, not workspace findings |
| `dacli github codeowners <project> \| --owner <org>` | Emit `.github/CODEOWNERS` from role scopes: each role's `scope` globs → a pattern owned by the role's team handle (`@owner/role`), most-general pattern first so a specific owner overrides a broad one. Local file write, not disclosure-gated (§ 9.7) |
| `dacli pr [--base BRANCH] [--dry-run] [--with-verdicts] [--draft]` | Resolve the target as CLI base → project landing base → linked repository default, report the source, and open a PR whose body carries acceptance + findings + `Fixes #issue`; `--dry-run` performs no GitHub mutation; `--with-verdicts` adds a loud trust-grade summary + per-finding verdict tally to the body AND posts it (+ the verify panel's per-seat verdicts) as a PR review; `--draft` opens it as a GitHub draft (CI runs, no review requested until marked ready) |
| `dacli integrate --pr [--auto] [--no-merge] [--merge]` | PR-first: push each done branch, open or reuse an enriched PR (+verdicts). `--auto` sets GitHub auto-merge so GitHub merges after required checks/reviews; red/pending PRs stay open. Effective PR mode fails closed when GitHub is unreachable; it never falls back locally. |
| `dacli ship --pr [--auto] [--no-merge]` | The wave tail in PR-first mode: forwards the flags to `integrate` so a whole wave lands as reviewable PRs; `--auto` is hands-off (GitHub merges each when CI passes) |
| `dacli ship --push --release <tag> --project <p>` | After integrating and pushing a wave, cut a tagged release with generated notes on the project's linked repo (shells `github release`, targeting `--into`). Requires `--push` (so the released ref is on the remote) and refuses `--pr` (whose merges land asynchronously on GitHub's clock, so a release cut now could tag before the wave merges) |
| `dacli pr status <task>` | "Did this land?" — checks gh's PR state (merged/landing/orphaned) before ever falling back to a trunk fetch. **Never** conclude a done task's branch is orphaned from a bare `git merge-base --is-ancestor <branch> main` against your local checkout — see § 9.6. |
| `dacli pr diagnose --task <ref> [--json]` | Read-only typed diagnosis of the canonical task head: PR generation/topology, check-run annotations, workflow conclusions, runner queueing, approvals, GitHub account/access/service failures, and an actionable next step. |
| `dacli release train --project <slug> --source <branch> --target <branch> (--dry-run \| --apply) [--required-check <name>] [--required-artifact <name>] [--required-reviews <n>] [--merge]` | Plan or resume one durable source-to-target promotion PR. Dry-run records the exact remote SHAs, accepted/excluded task evidence, delta PRs, gates, and notes. Apply reuses its persisted PR and fails closed on unknown GitHub state; required checks and artifact digests must belong to the exact reviewed head. Merge additionally requires project `release_merge_authority: true`. It never tags or publishes. |
| `dacli release train authority --project <slug> (--allow-merge \| --revoke-merge)` | Persist or revoke the project-level authority required in addition to `release train --merge`; a transient apply flag cannot grant itself authority. |
| `dacli escalate --github` | File a help request as an issue ([TEAM.md § 3](TEAM.md)) |

`escalate --github` is the piece that was already specified as the terminal escalation hop, and it is the highest-value part of this integration: when no role in the tree owns a problem, it reaches a human where they will actually see it, with a notification, outside the session.

### External verification evidence

Acceptance and release promotion share one exact-head evidence model. A project
may configure `required_checks` and `required_artifacts`. Acceptance also reads
legacy branch protection and GitHub's evaluated branch-rules endpoint, which
already resolves every active repository or organization ruleset applicable to
the exact target branch. Configured, legacy, and ruleset requirements are
merged deterministically without losing provenance. Human output names each
source; `accept --json` and the persisted verification record expose the full
`github-required-check-policy/v1` object.

If either policy API is denied, malformed, or otherwise unobservable,
acceptance refuses before closing the task—even when project metadata lists no
checks, because an unread ruleset could be the missing requirement. The owner
may use `--allow-unobservable-check-policy` as a one-invocation exception; that
override, actor, repository, branch, and observation error are appended to the
task log. This flag never turns a red, pending, skipped, stale-head, or missing
configured check green.

Acceptance observes GitHub check runs, workflow runs, and artifact metadata for
the reviewed commit and persists provider IDs, URLs, observation time, state,
conclusion, and artifact SHA-256 digests with the local verification evidence.
Release trains use the same evidence observer and validator when their
corresponding flags are set.

Only an observed successful result for the exact reviewed head can satisfy a
requirement. Pending, skipped, superseded, expired, stale-head, or unobservable
evidence remains explicit and cannot be converted into green. A GitHub outage
may be recorded as unobservable when no external policy is configured; it fails
closed when an external check or artifact is required.

## 9. The G-series in detail

This section maps each shipped command to what it does, verified against `internal/features/ghmirror/ghmirror.go` and `internal/features/vcs/lifecycle.go`.

### 9.1 `github push <project>` — outbound projection

Runs the **disclosure gate first** (`disclosureGate`): the repo's *live* visibility is re-fetched, and a PUBLIC repo with no recorded per-project consent refuses (§ 7). Then, for every task **in the window**:

The gate returns one typed `publication.Policy`. PRIVATE preserves the rich task/finding/decision projection. PUBLIC defaults to task-safe fields and skips every decision/finding issue/comment stage; `--allow-public` alone never broadens it. Internals need exact-repository `github_internal_disclosure` plus the current `--include-internal` request. UNKNOWN visibility is public-safe and cannot consume a prior internal grant. Dry-run prints the policy, every withheld field/reason, and exact task issue title/body.

**The task window (`selectTaskWindow`, dacli 275).** A mature project's done set is hundreds of tasks; mirroring the whole backlog files (or adopts and closes) an issue for every one, so an operator cannot publish a single wave without reaching for raw `gh`. `github push <project> [task-ref...] [--since <dur>]` narrows the mirror to the requested window: the **explicit refs** after the project and/or a **`--since`** cutoff (a duration like `2h`/`90m`, matched against each task's `created` stamp). With **neither**, the full backlog mirrors exactly as before. With **both**, the window is the **union** — the named refs plus everything created since the cutoff. A ref that names **no task** in the project is a **not-found error (exit 4)**, never a silent no-op: an operator who asked to mirror a task must hear it was not found rather than watch push report success having filed nothing. A task with no parseable `created` stamp falls **outside** a `--since` window ("since" means demonstrably created after the cutoff, never "assume recent"). The `--since` cutoff is shared with the `--with-tasks` finding-issue mirror, so both scope identically.

**The window scopes the decision and finding mirrors too (`noteInWindow`, dacli 298).** The window originally narrowed only the *tasks*; the decision mirror ran over **every** decision in the project and the standalone finding-issue mirror was scoped by `--since` alone, so a one-task `github push core 275` still published every other decision — an unbounded disclosure on a public repo, where an unintended publish cannot be taken back. A decision or finding is now **in the window** when it was created at or after the `--since` cutoff (the same temporal rule the task window uses) **or** its `about` field names one of the tasks the **explicit refs** selected. With no window (no refs, zero `--since`) every note is in, so the default whole-project mirror is unchanged. An explicit-ref window therefore publishes only the decisions/findings attached to the named tasks; the rest are reported as `out-of-window` and left unpublished.

**The blast radius is stated first (dacli 298).** Before it creates a single issue, push prints a `plan: will create N task, M decision, K finding issue(s) on <repo>` line — the count of genuinely *new* issues of each kind (a note already mapped or adoptable by marker files nothing), computed from the same in-memory issue-list snapshot the loop uses. On a public repo an operator sees the disclosure size while it can still be aborted.

1. **Resolve the issue.** Read the mapped issue number from the task's `github:` block; if absent, **search by marker** (`searchByMarker`) — a strongly-consistent list-endpoint scan matching `<!-- dacli:<task-id> ws:<ws-id> -->` by exact substring; if the marker still misses, **adopt by exact title** (`markerIndex.findByTitle`, dacli 275) — an issue an operator filed by hand carries the canonical `NNN: <title>` but **no dacli marker**, so it is adopted into the mapping rather than duplicated (the full title must match, never a prefix; the lowest-numbered issue wins a title collision, a deterministic tie-break; the issue body is never edited, so the local mapping written back is what makes the next push skip it, exactly as pull leaves an adopted issue); if still absent, **create** the issue (title `NNN: <title>`, body = marker + "So that…" + Acceptance).
2. **Write the mapping back** — *after* the remote exists, so a crash leaves an adoptable issue, never a dangling mapping.
3. **Status label** (`applyStatusLabel`): give the issue exactly one `status:<folder>` label and strip the other three, so a moved task never accumulates conflicting labels.
4. **Findings → comments** (`mirrorFindings`): each finding note whose `about` names this task is posted as an issue comment led by a per-finding marker `<!-- dacli-finding:<note-id> ws:<ws-id> -->`; a comment already carrying that marker is skipped, so a re-push never duplicates. Comments are fetched once per task, so N findings cost one extra read.
5. **Close on done**: a `done` task's issue is closed best-effort.

After the task loop, **decisions → labeled issues** (`mirrorDecisions`): each **in-window** decision note (see the window note above) becomes an issue labeled `decision`, keyed by `<!-- dacli-decision:<note-id> ws:<ws-id> -->` (a distinct prefix so a decision issue is never adopted as a task mirror), with the same frontmatter → search-by-marker → create idempotency. The body is the **why**: Chose / Rejected / Because + a backlink. Decisions ride the same explicit push and the same already-tripped disclosure gate — never a separate auto-run — and are scoped to the same window as the tasks, so a scoped push never files a decision about a task it did not name.

### 9.2 `github pull <project>` — inbound adoption

Adopts human-authored issues as local tasks (§ 3, *Shipped behavior*). It is **read-only against the remote** — it never edits an issue — so it is **not** gated on public visibility: importing an issue discloses nothing. Idempotent by issue-number mapping.

`github pull <project> --dry-run --json` emits `github-adoption-plan/v1`.
It reports total counts and bounded arrays for `create`, `link`,
`already_mapped`, `refused`, and `skipped`. Pages default to 100 entries, accept
`--limit 1..200`, and expose an offset `next_cursor` for the next `--cursor`.
Acceptance criteria and parsing diagnostics are withheld by default; add
`--include-acceptance` when they are required. JSON without `--dry-run` is
refused, preserving an explicit boundary between inspecting and applying a
local mutation. GitHub read failures return an error before a partial plan is
emitted.

### 9.3 `github sync <project>`

`cmdPull` then `cmdPush` — each half carries its own linkage and (for push) disclosure checks; running pull first means a freshly adopted issue is mirrored back on the same invocation.

`github sync <project> --dry-run --json` emits `github-sync-plan/v1`: the typed
inbound adoption plan plus a bounded, content-digested outbound preview. This
is a preview contract only; non-dry-run JSON refuses rather than combining a
machine plan request with local or remote writes.

### 9.4 `dacli pr [--with-verdicts]` — PR enrichment and verify verdicts

Before mutation, `dacli pr` resolves and reports one target branch: explicit `--base` wins, then the task project's configured landing base, then `gh repo view` supplies the linked repository default. `--dry-run` prints that decision without opening, reviewing, or queueing a PR. An unavailable or invalid repository default is a fail-closed result with two recovery paths: pass `--base`, or persist `--landing-base` with `project show`; dacli never substitutes `main`.

Task worktrees use that project boundary before implementation begins. `worktree add` resolves the project's configured landing base, freshly fetch `origin/<base>` when `origin` exists, and cut a new `dacli/NNN-slug` branch at that exact observed commit. They never inherit the operator checkout's incidental `HEAD`. A missing or unreadable configured remote base refuses before branch creation; repositories with no `origin` use the exact local base ref. Native external attempts record their own observed checkout/revision binding. Historical `worktree-base.json` records remain evidence of older launcher starting points.

`prBody` assembles the PR description with no network access (so it is unit-testable): a `Fixes #<issue>` line parsed from the task's own `github:` block (skipped cleanly when unlinked), the task's **Acceptance** section, and a **Findings** section listing every finding note about the task (with severity and trust tags). The opened PR's URL is recorded as an operational comment so it enters every future brief without lowering the task's finding trust grade.

With `--with-verdicts`, the PR projection can include recorded trust grades and
historical `verify-verdict:` events under the explicit disclosure policy. The
provider-driven panel writer has been removed; reading an old verdict does not
run verification again or establish native reviewer independence. Posting remains
operator-triggered. Native self-reports, trusted runner observations and review
grants have their separate revision-bound contracts.

### 9.5 `dacli integrate` — the merge path, and the branch key it lands

**`dacli integrate` is how a done task's branch reaches trunk — not a hand-run `git merge`.** It merges every done branch, in `dacli next` order, one at a time, so a conflict blocks exactly that task (with a finding naming the conflicted files) instead of piling up; `dacli merge --task <ref>` lands a single task's branch and `dacli ship` tails a whole wave. Never `git checkout main && git merge` a task branch by hand: `integrate` is what records the landing, tears down the worktree, and keeps the sequence honest.

**The branch key is `dacli/<seq>-<slug>`** — the task's zero-padded 3-digit sequence number and its slug (`fmt.Sprintf("dacli/%03d-%s", t.Seq, t.Slug)`, `internal/features/vcs/lifecycle.go`). That single derivation is the contract: `dacli worktree add` creates the branch under this name, `integrate`/`merge`/`pr status` find a task's branch by re-deriving it, and the worktree that holds it is keyed on **project + seq + slug** so two same-titled tasks in different projects never collide on one path (dacli 288/215). A branch named anything else is not a task branch `integrate` will find — the key is derived from the task, never typed.

By default `dacli integrate` (and `dacli ship`, which shells it) lands each done task's branch with a **local `git merge`**. `--pr` switches to **PR-first integration**: for every done task with a branch, `prIntegrateTask` (`internal/features/vcs/lifecycle.go`)

1. **pushes** the branch to origin (`dacli push`'s primitive),
2. **opens an enriched PR** — the same `prBody` (acceptance + findings + `Fixes #issue`) as `dacli pr`, and it always posts the **verify-panel verdicts** as a review comment, and
3. **lands** it via `gh pr merge`. Three sub-modes decide *how*:
   - **`--auto`** — `gh pr merge <branch> --auto --merge --delete-branch` sets GitHub's native auto-merge, so GitHub merges the PR the instant its required checks go green and deletes the branch. Nothing merges locally now; the worktree/branch stay put because GitHub owns the pending merge. This is the **hands-off** path: the operator never waits on CI or merges by hand.
   - **default (no flag)** — the **check gate**: `prChecksPass` runs `gh pr checks <branch>` and merges (`--squash`, or `--merge` for a merge commit) **only if every check already passes** (exit 0). A red or pending check leaves the PR **open** and reports it, rather than blindly merging over a failing gate — and so does a repo with **no checks configured at all** ("no checks reported"): an absent gate is reported distinctly from a passed one, not treated as green, so a repo with no CI can't merge everything by default (dacli 216). On a clean merge dacli tears down the local worktree/branch and fast-forwards the local target to the merged remote state.
   - **`--no-merge`** — stops after step 2: the PRs are **left open for human review** and nothing lands.

`--auto` and `--no-merge` both hand the merge to GitHub, so an *offline* failure is **surfaced** rather than silently local-merged behind the operator's back; the default (check-gate) path still falls back to a local merge when GitHub is unreachable so a wave lands offline. Because a merge closes the mirrored issue via the body's `Fixes #<issue>` line, PR-first integration keeps GitHub the source of truth for review while dacli still assembles the body.

**Fail-closed recovery.** In effective `pr` mode, failures during push, PR creation, checks, or merge leave the canonical task branch and existing PR recoverable; they never authorize a local merge that bypasses required checks or reviews. Run `dacli github doctor`, repair `gh auth` or connectivity, inspect `dacli pr status --task <ref>`, and rerun the same command. A deliberate local exception must be explicit (`--landing-mode local`, or loop `--no-pr`) and is recorded as an override.

### 9.6 `dacli pr status <task>` — "did this land?" without false positives

A `--auto` PR merges on **GitHub's clock**, not the reviewer's: it queues auto-merge and returns immediately, then lands whenever CI goes green, possibly minutes later and always asynchronously to whatever the reviewer's checkout is doing. A reviewer or backlog-auditor who checks "did this accepted task's branch actually land?" by running a bare `git merge-base --is-ancestor <branch> main` against **their own local `main`** is comparing against a snapshot from whenever they last fetched — not against GitHub's current state. Two false positives shipped from exactly this mistake: task 157 (task 154's CI change) and task 160 (task 159's fetch/fast-forward fix) were both filed as "accepted+closed but branch orphaned" when in fact the branch's PR was still landing — 159's own branch merged shortly after (PR #268), proving the "orphan" was never real.

**The rule going forward: a branch not (yet) an ancestor of local main is not evidence of anything.** Before calling a done task's branch orphaned, run `dacli pr status <task>` (`checkLanded` in `internal/features/vcs/lifecycle.go`). It answers with one of:

- **`merged`** — GitHub reports the PR MERGED, or (no PR on record at all, e.g. a local `dacli integrate`) the branch is an ancestor of a **freshly fetched** `origin/main`.
- **`landing`** — the PR is **OPEN** on GitHub right now, whether or not `--auto`'s auto-merge is queued yet. This is the state 157 and 160 were wrongly filed under — it is not a defect, and re-checking a few minutes later (or after CI finishes) is the only correct next step, not a re-integration task.
- **`orphaned`** — no PR was ever opened (or it was **closed without merging**) and the branch is still not an ancestor of a fresh `origin/main`. This is the only state that justifies a re-integration task.
- **`unknown`** — gh and a trunk fetch both failed to answer; treat as "can't tell", not as "orphaned".

`checkLanded` always asks `gh pr list --head <branch> --state all` first — GitHub's own PR state is authoritative — and only falls back to a git comparison when no PR is on record, and even then it fetches `origin` first rather than trusting whatever the local checkout already had on disk.

### 9.6a `dacli pr diagnose --task <ref>` — why it is blocked

`pr status` answers whether work landed; `pr diagnose` answers why the current canonical PR cannot progress. It resolves the task's derived branch and current commit before selecting a PR, so an older closed or failing PR for the same branch cannot override a newer generation. It then inspects check runs, their annotations, and workflow-run conclusions rather than treating the aggregate `gh pr checks` exit code as a diagnosis.

The dashboard does not call either command on its polling heartbeat. Delivery
and Overview project only durable local PR/check/review/verification
observations, keep superseded generations visibly separate, and report missing
remote truth as **external/API unknown**. Use the bounded `pr diagnose` command
when a fresh GitHub observation is needed; its result can then enter the durable
record through the governed delivery workflow.

The JSON result is the canonical contract: `code`, `summary`, `retryable`, `next`, `evidence[]`, the selected `pull_request`, and any `superseded_prs`. Codes distinguish `test_failure`, `workflow_configuration_failure`, `runner_unavailable`, `billing_restriction`, `github_authentication`, `github_authorization`, `github_rate_limited`, `github_outage`, `approval_pending`, `merge_conflict`, `stale_base`, `closed_unmerged`, `missing_canonical_pr`, `superseded_pr_generation`, `ci_pending`, `ready`, and `unknown`. A queued job becomes `runner_unavailable` only after the documented 30-minute threshold; before that it is `ci_pending`. Neither state authorizes dacli to retry, override, merge, or repair the account automatically—the `next` field names the bounded operator action.

### 9.7 Milestones, draft PRs, and CODEOWNERS — the planning artifacts a real project carries (dacli 224)

Three additions that give a mirrored repo the planning scaffolding a hand-run project maintains by hand:

- **Projects → milestones (`github push`).** A project maps to ONE milestone titled by the project (its human title, falling back to the slug), and every task issue is assigned to it — so the tracker groups a project's work the way GitHub milestones are meant to. `ensureMilestone` (`internal/features/ghmirror/ghmirror.go`) is the load-bearing piece: `gh issue create --milestone <title>` **hard-fails on an unknown milestone**, which would abort the whole push, so it passes `--milestone` ONLY once the milestone is *positively confirmed* present. gh has no `milestone create` verb, so creation is a POST to the REST milestones endpoint followed by a re-list to confirm — a milestone that could not be confirmed (a flaky gh, a create that did not land) is simply skipped, exactly like the best-effort labels, never passed as a poison flag. Assignment to adopted/already-mapped issues is best-effort and idempotent.
- **Draft PRs (`dacli pr --draft`).** Opens the PR as a GitHub draft — CI runs but it is not mergeable and requests no review until marked ready, the work-in-progress PR a real project opens before review. It threads through `openPR` as a single flag; **integration never drafts** (it opens PRs to *land*, `prIntegrateTask` passes `draft=false`), so only the operator-run `dacli pr` sets it.
- **Roles → CODEOWNERS (`github codeowners`).** A real project routes review by mapping code areas to owning teams; dacli already carries that mapping in its roles — each role's `scope` globs are the paths it owns. `github codeowners` projects those scopes into `.github/CODEOWNERS`: each glob → a CODEOWNERS pattern (a trailing `**` is dropped since a directory prefix already matches recursively), owned by the role's team handle `@owner/role` (owner from the linked repo, or `--owner`). Roles sharing a pattern share one line (CODEOWNERS's multi-owner form), and patterns are emitted **most-general-first** so a specific owner overrides a broad one under CODEOWNERS's last-match-wins rule. It writes a **local file** — not an outbound GitHub write — so, like template generation, it is not disclosure-gated; and it **refuses to write a hollow header-only file** when no role declares a scope, rather than reporting success over an empty projection. The mapping (`codeownersEntries`/`codeownersPattern`/`codeownersDoc` in `internal/features/ghmirror/codeowners.go`) is pure and unit-tested without gh or the filesystem.

---

# Obsidian

Unchanged from [DESIGN.md § 9](https://github.com/mlnomadpy/dacli/blob/main/DESIGN.md): **conform to the conventions, ship no plugin.**

The workspace is already a valid vault — YAML frontmatter, `[[wikilinks]]`, folders. Opening the project root works today, and graph view renders the decision and finding link structure for free.

Templates ([TEMPLATES.md](TEMPLATES.md)) generate docs into that layout, so documents created by an agent open as ordinary vault notes with no export step.

Two additions worth making, both zero-integration:

- **Index notes.** Templates generate a `_index.md` per project linking its docs, tasks, and decisions — a map-of-content note, which is how vaults are navigated. Cheap, and it makes the vault browsable rather than just present.
- **Inline field syntax** on tasks (`priority:: must`) alongside the frontmatter, so that anyone running the Dataview community plugin gets live task boards and status queries in Obsidian with no work from us. Optional, additive, and invisible to anyone not using it.

What stays out of scope: a plugin, Canvas file generation, and anything requiring Obsidian to be installed. The vault must remain a side benefit of the format rather than a dependency of it.

## The three surfaces

The clean way to state the whole picture:

- **Obsidian** is where humans read and write documents.
- **GitHub** is where humans coordinate and where work becomes visible outside the session.
- **`dacli`** (CLI and MCP) is where agents work.

One markdown store underneath all three. None of them owns it.


## Exact existing issue links

Use `dacli github issue link <project> <issue-number[=task-id]>... --dry-run` to preview only the selected existing issues/tasks, then inspect its immutable plan and apply with `dacli github issue link --apply <plan-id> --owner-config <private-file>`. This exact local-owner operation is refused through the generic MCP CLI escape hatch. Apply authenticates the explicit native owner rather than unrelated legacy ambient identity. It refreshes exact remote/source and local repository/consent/mapping bindings and retains native protection through each durable task write. No public issue is created or edited and no acceptance is imported.

Selected file commits have an explicit recoverable local journal, not a distributed GitHub/filesystem transaction. Replay observes current bytes and refuses rollback/third states; partial selected progress retains its recovery identity and never broadens selection. Unselected bytes/history stay unchanged. Acceptance migration is separate. Do not use a broad pull or edit issue titles to force adoption. CI remains manual only.
