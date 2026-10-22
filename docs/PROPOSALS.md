# Feature proposals — ranked by value

> Historical design/evidence record: launcher behavior below describes earlier versions.
> Current source removes coding-agent wrappers; follow [the native operator playbook](OPERATOR_PLAYBOOK.md).


**Status: proposals. Nothing here is accepted until it graduates into a spec doc.** Each entry says what it exploits, what it costs, and how we'd know it worked — INVEST's *Testable* applied to our own roadmap.

---

## 1. The thesis: spend the event log twice

Everything dacli records today — attributed events, three-point estimates, run records, briefs — is collected to *coordinate* work. That data is spent once and then sits there.

The four highest-value features below are all the same move: **cash in the same asset a second time.** Calibration reads estimates against actuals. Memory distills findings into lessons. Replay reconstructs what any agent knew. Taint traces where content traveled. Four features, one substrate, zero new collection machinery — the log was already the expensive part, and it's already designed.

That suggests refining the product direction:

> **Product:** the control plane for autonomous coding-agent swarms.
> **Compounding outcome:** every governed run leaves the workspace measurably
> smarter than it found it.

"Organize" is the v0 goal. "Learn from every run" is the v1 goal. No competing tool does the second, because no competing tool has an attributed log to learn *from* — the differentiator we already have is the prerequisite for the differentiator nobody has.

## 2. Capture now, build later — the only urgent part

None of these features should be built before the v0.1 spine ([ARCHITECTURE.md § 3](ARCHITECTURE.md)). But all four consume data that is either captured from day one or **lost forever**. The features can wait; the fields cannot. Four cheap format additions that must ride in v0.1:

| Addition | Costs | Feeds |
|---|---|---|
| `runs/<id>/brief.md` — the rendered brief exactly as delivered | one file write per spawn | Replay |
| Actuals stamped into the task `## Log` at `done`: tokens, turns, runtime, wall time | one line | Calibration |
| `origin:` on every event — `agent` \| `file:<path>` \| `external:<gh-user>` | one frontmatter field | Taint |
| `scope:` on notes — `project` \| `workspace` | one frontmatter field | Memory |

An hour of format work now buys four features later. Skipping it means v1 starts its learning loops with an empty history.

---

## 3. Tier 1 — the four loops

### P1 · Cross-project memory — `dacli distill` *(highest value)*

**What:** at retro or archive, findings and decisions get distilled into **lessons** — workspace-scoped notes tagged by path patterns and topics. The brief assembler grows one section: workspace lessons matching the task's scope, capped and announced like everything else.

**Why first:** every other feature's value is linear in use; this one compounds. Today the auditor's "the batch job bypasses the service layer" dies with its project — the next project on the same codebase rediscovers it at full price. Findings are already durable *within* a project; this is the same mechanism, one scope wider. It is also the feature this repo's own development method vouches for daily: a memory that feeds future context is why these sessions don't start from zero.

**Cost:** `scope:` field, a `distill` command (agent-driven — dacli stores, the agent summarizes, per axiom 3), one brief section.
**Acceptance:** a lesson recorded in project A demonstrably appears in — and changes — a brief assembled in project B, with `--explain` showing why it matched.

> **Status 2026-07-21: shipped**, minus `--explain`. `retro --scope workspace` (and any `note add --scope workspace`) produces lessons; the brief assembler surfaces them cross-project, quote-fenced and attributed, Miller-capped with announced overflow, strictly excluding the current project (its own notes already arrive through findings/constraints). The acceptance test passes verbatim in `internal/cli/lessons_test.go`. Ranking is the crude version by decision — P5 stays available if measured misranking demands it.

### P2 · Estimate calibration — `dacli calibrate`

**What:** compare PERT expected vs. recorded actuals, grouped by role, runtime, and estimate band. Output: a multiplier table that briefs display *next to* estimates — "this role has underestimated `l` tasks 2.1× (n=14)."

**Why:** it makes the SPM layer empirical instead of aspirational — the Cone of Uncertainty with *your* cone measured, not McConnell's 2006 factors. Agents produce confidently wrong scalar estimates; forcing three points was step one, measuring how wrong is step two. No tool in this space closes the loop. It is also GQM applied to ourselves: goal (trustworthy estimates) → question (how biased, where) → metric (Te/actual ratio by band) — the discipline the tool imposes on users, imposed on the tool.

**Cost:** actuals stamping (above), pure-L4 math, one brief line. Cooperative runs self-report and get labeled estimated, per axiom 5.
**Acceptance:** with n ≥ 10 in a band, the brief shows the calibrated range beside the PERT range; the two visibly diverge where the data says they should.

> **Status 2026-07-21: shipped**, with the time-proxy caveat stated everywhere it appears. Actuals are wall-clock claim→completion spans from the Log stamps (token actuals await runtime usage reporting); the median multiplier gates at n ≥ 10 — below that, `calibrate` says "insufficient history" and briefs stay silent, because a multiplier from anecdotes is confidence theater. The acceptance test passes verbatim in `internal/cli/calibrate_test.go`. First real readout from dacli's own workspace: agent completions run ×0.01–0.02 hours/point — human-scaled points are ~50× too heavy for agent-executed work, which is itself the loop doing its job.

> **Status 2026-07-21: shipped.** `dacli replay <run-id>` / `--task <ref>` interleaves each run's frozen brief with the child's events in ULID order, offline. Capture (briefs frozen at every spawn/supervise turn) had been live since v0.5; this is the renderer over it. Proved itself on real data on first use: replaying the opus reviewer's run showed a complete brief delivered against zero events written, localizing the Bash-gating bug to the reporting channel rather than comprehension.

### P3 · Replay — `dacli replay <task>`

**What:** reconstruct any past task as a timeline: the brief each agent was actually handed (recorded at spawn), interleaved with every event it wrote, in ULID order — *what did this agent know at the moment it went wrong?*

**Why:** multi-agent failures are currently undebuggable everywhere — transcripts show what an agent said, never what it was told relative to what existed. dacli is uniquely positioned because the log is already ordered and attributed; the only missing piece is freezing the brief. This is also the honest answer to "why did the tree do something stupid" that doesn't involve guessing.

**Cost:** nearly zero — `runs/<id>/brief.md` plus a read-only formatter over existing data. No re-running, no model calls.
**Acceptance:** for any completed task, `replay` renders the full knew-vs-wrote timeline offline.

### P4 · Taint tracing — `dacli taint <path|actor>`

**What:** every event records provenance (`origin:`). The query: given a suspect source — a hostile file, a compromised GitHub commenter — walk forward: which findings derived from it → which briefs included those findings → which agents consumed those briefs → which tasks they shipped.

**Why:** cross-tree injection is the design's worst open problem and every doc says so. This does not fix it — nothing proposed anywhere fixes it — but it converts "attribution helps a human audit afterward" from a sentence into a command. Blast-radius-in-seconds is the difference between an incident and an unbounded suspicion, and it is the only injection posture that doesn't require trusting a sanitizer.

**Cost:** one field, one graph walk over existing links.
**Acceptance:** seed a workspace with one marked-hostile file; `taint` returns exactly the tasks whose briefs transitively consumed it, and nothing else.

---

## 4. Tier 2 — worth doing, not worth leading with

| | Proposal | One-line case | Note |
|---|---|---|---|
| P5 | **Graph-proximity relevance** for brief findings: rank by wikilink-graph distance from the task, then severity, then recency | Closes DESIGN open question 2 with zero dependencies — the link graph already exists and embeddings would break the no-infrastructure story | Pure L4; measure against the dumb ranking before keeping it |
| P6 | **`dacli handoff`** — structured pause note (tried / state / next / warning), surfaced as a "Resumption" brief section | Session death mid-task currently scatters state across events; this is context compaction as a first-class object | The user-visible half of what long-session agents already need |
| P7 | **`dacli fleet`** — `~/.dacli/registry` of workspaces; one portfolio view: stalled projects, open help requests, budget burn across every repo | Resolves DESIGN open question 3 (cross-repo) as a *read-only view* rather than a federation — no shared state, no sync, just N workspaces read | For a person running agent trees across many repos, this becomes the morning screen |
| P8 | **`dacli dashboard`** — shipped loopback Vue projection: operator pulse, projects, task board, critical-path graph, burn, live agents, and role capacity | Read-only and derived from the durable workspace; one embedded bundle, no second database | Shipped; see [DASHBOARD.md](DASHBOARD.md) |

## 5. Considered and rejected

On record so they aren't re-litigated by the next enthusiastic session (including mine):

- **A deterministic auto-planner** (`dacli plan` inventing a WBS without a
  model) — planning is the orchestrator agent's job; dacli stores, validates,
  prices, and schedules the recorded plan. A planning agent may be launched by
  a governed loop, but dacli policy code must not masquerade as engineering
  judgment.
- **Agent chat** — rejected in TEAM.md § 3, re-rejected here. Nothing in the four loops changed the arithmetic: no completion criterion, no information gain, no durability.
- **An arbitrary job DAG / pipeline runner** — axiom 2 permits lifecycle
  orchestration for coding agents, not arbitrary user-authored jobs. CI remains
  responsible for build pipelines; dacli consumes its evidence.
- **Embeddings for relevance** — a model dependency and an index to maintain, against P5's zero-cost graph ranking. Revisit only if P5 measurably fails, with the measurement in hand.
- **Golden-task regression / eval platform** — a real product, a *different* product. The run records make it possible for whoever wants to build it on top.
- **Notification daemon / watch mode** — polling `events tail` is adequate until a demonstrated need; a daemon is the first step toward the workflow engine axiom 2 forbids.
- **Template/shortcut marketplace** — premature by at least two version numbers.

## 6. Sequencing against the wedges

| Wedge | Feature work |
|---|---|
| **v0.1** | **The four capture fields — non-negotiable.** P1 usable immediately (distill at retro); P4 queryable immediately; P3 partial via `context --record` |
| v0.2–0.3 | P5 relevance, P6 handoff (both pure or near-pure L4) |
| v0.4 | P3 complete (spawn records briefs automatically); P2 begins accruing real actuals |
| Current | P2 calibrated ranges, P7 fleet foundations, and the P8 read-only dashboard are shipped |

The discipline holds: nothing above jumps the spine, and nothing requires new collection — only the four fields that make the existing collection worth keeping.
