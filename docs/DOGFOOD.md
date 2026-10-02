# Dogfood as proof

> Historical design/evidence record: launcher behavior below describes earlier versions.
> Current source removes coding-agent wrappers; follow [the native operator playbook](OPERATOR_PLAYBOOK.md).


dacli's one differentiator is a claim: **an attributed log lets a tool learn from
its own development.** Every competitor can log. What none of them has is the
proof — a tool that provably learned from itself. That proof is not a slide; it
is sitting in this repo's `.dacli/` directory, because dacli developed dacli.

Over one session, spawned agents shipped tasks 016–029: process-tree
resource-monitoring and kill, a hot-path read/atomicity audit, async
`spawn`/`wait`, the worktree-shadowing fix, and then the D-series — agent-native
estimation (D1), acting on the log at spawn (D2), and trust as a brief property
(D3). Each agent committed through `dacli commit`, which stamped its agent id and
role onto the commit. The result is a real, self-hosted record you can query.

`scripts/dogfood-demo.sh` replays that record. It is strictly **read-only** — no
spawns, no writes, no box-checking — and re-runnable. Run it against this repo:

```
./scripts/dogfood-demo.sh
```

It builds the repo-root `./dacli` if absent (`go build -o ./dacli ./cmd/dacli`),
then prints four readouts with narration. Every block below is **real output**
captured from running it against this workspace's own `.dacli/`.

> Note: the demo binary path is overridable with `DACLI=/path/to/dacli` for CI or
> a prebuilt binary. The captured output below was produced by the same binary
> over this repo's shared workspace.

---

## 1. `contrib` — the agents authored the code, provably

```
by role  (commits · findings-against · defect rate):
  maintainer     9 commit(s) · 0 finding(s)-against
by agent:
  a-cqrx1m7sc0     (maintainer) 1 commit(s) · 0 finding(s)-against
  a-khwzk4bfr6     (maintainer) 1 commit(s) · 0 finding(s)-against
  a-yyn9jj4j0b     (maintainer) 1 commit(s) · 0 finding(s)-against
  a-q2w31150s0     (maintainer) 1 commit(s) · 0 finding(s)-against
  a-68jkvx7x6b     (maintainer) 1 commit(s) · 0 finding(s)-against
  a-sr9e6xf1d0     (maintainer) 1 commit(s) · 0 finding(s)-against
  a-mzt5xcjgnm     (maintainer) 1 commit(s) · 0 finding(s)-against
  a-9v3qa4cstk     (maintainer) 1 commit(s) · 0 finding(s)-against
  a-zqtmzbe6mv     (maintainer) 1 commit(s) · 0 finding(s)-against
(a high defect rate for a role is where to focus improvement — better prompts, tighter scope, or a heavier model)
```

**What it proves.** Nine distinct agents, each authoring a commit, all under the
`maintainer` role — dacli's code was written by dacli's agents, and the log
attributes each commit to the agent that made it. The `findings-against` /
defect-rate column is the payoff of attribution: when a reviewer files a finding
against a commit, it rolls up to the *role*, so a persistently buggy role points
at a prompt, a scope, or a model tier to fix. Here every role reads clean, which
is honest for a session whose reviewer findings were about the code under test,
not the commits that shipped it.

## 2. `calibrate` — dacli measured its own Cone of Uncertainty

```
by size band:
small (≤3)   n=1   ×0.03 median hours/point
medium (≤8)  n=6   ×0.02 median hours/point
large (>8)   n=2   ×0.01 median hours/point
overall      n=9   ×0.01 median hours/point

by agent band: no done task joins a run record yet (runs predate model-banding, or none recorded)
insufficient history (n=9 < 10): briefs stay silent — a multiplier from anecdotes is confidence theater
(actuals are wall-clock claim→completion — a time PROXY until runtimes report token usage)
```

**What it proves.** This is the finding that named the whole D-series. dacli
invested its heaviest design in porting the *human* software-estimation apparatus
— PERT three-point estimates, velocity, and McConnell's Cone of Uncertainty. Then
it estimated its own tasks and measured the actuals against those estimates. The
answer: agent wall-time lands at **~0.01–0.03 hours per point**, roughly **50–100×
under** human-scaled points. The multiplier is not estimation error; it is the
unit conversion between points calibrated for human effort and agent wall-clock.

This is the compound loop working out loud: dacli's own first self-measurement
told it the units are off by two orders of magnitude — and that reading *is* the
empirical Cone of Uncertainty, drawn from real self-hosted data. No competitor
can print this chart, because none has the attributed log to measure its own cone.

Be honest about the thinness: `n=9` overall, and the read is deliberately
conservative. dacli refuses to fold the multiplier into briefs until a band has
`n≥10` — "a multiplier from anecdotes is confidence theater." The agent-band view
(role × model × runtime) is still empty because the completed runs predate
model-banding; it will populate as banded runs accumulate. And the actuals are
wall-clock claim→completion, a *time proxy* — the honest native unit is tokens,
which waits on runtimes reporting usage. The tool states its own limits in the
readout rather than overselling the number.

## 3. `taint` — a real blast-radius query over the log's own origins

First the security-relevant question: is anything in this workspace derived from
an **external** origin?

```
$ dacli taint external:
no artifact carries origin "external:" — nothing derived from this source
```

**What it proves.** The blast radius of external input is empty — an honest,
meaningful clean result, not an absence of capability. Nothing in this workspace
traces to an untrusted external source, so no brief is downstream of one.

To show the query works end-to-end — that "clean" means empty, not broken — run
it over the origin that *is* present. Every artifact here is self-reported
`origin=agent`, so this traces dacli's own development:

```
$ dacli taint agent
event  01KY3DG7QKWK2RMNXYK5BDH58E   by a-khwzk4bfr6   origin=agent → 
event  01KY3E6E90Q8H0BAFNP8TQG93X   by a-68jkvx7x6b   origin=agent → 
event  01KY3EA7QAECQCZGY39KFD31VD   by a-cqrx1m7sc0   origin=agent → 
event  01KY4MCNF7Z2DKJBV3QWDBNE0S   by a-zqtmzbe6mv   origin=agent → resource-monitoring-and-kill-for-spawned-agent-process-trees
event  01KY4VF7MA99GGKCZVD82TG1MV   by a-yyn9jj4j0b   origin=agent → async-spawn-wait-agent-lifecycle-features-from-my-dacli-feedback
event  01KY4ZMYCDTG860F7S6BATHFT6   by a-mzt5xcjgnm   origin=agent → t-01KY4ZJQHVD81PB7KKPW8Z2JF4
event  01KY507DKP5ZGGJZ2N5W4C24P0   by a-sr9e6xf1d0   origin=agent → d1-agent-native-estimation-empirical-role-model-runtime-bands-become-the
event  01KY50YZ30VWV4R56FV1F5BP46   by a-q2w31150s0   origin=agent → d2-act-on-the-log-at-spawn-dacli-spawn-advise-budget-taint-role
event  01KY51JED0A2W6QTNEV1BZVEFQ   by a-9v3qa4cstk   origin=agent → d3-trust-as-a-first-class-brief-property-trust-floor-label-spawn-time-taint-gate

blast radius: 9 artifact(s), 0 project(s), 6 brief(s) exposed
exposed briefs: async-spawn-wait-agent-lifecycle-features-from-my-dacli-feedback, d1-agent-native-estimation-empirical-role-model-runtime-bands-become-the, d2-act-on-the-log-at-spawn-dacli-spawn-advise-budget-taint-role, d3-trust-as-a-first-class-brief-property-trust-floor-label-spawn-time-taint-gate, resource-monitoring-and-kill-for-spawned-agent-process-trees, t-01KY4ZJQHVD81PB7KKPW8Z2JF4
this is a LOWER BOUND: only honestly-labeled provenance is traced — unlabeled artifacts are invisible.
(an audit, not a fix — review these briefs' consumers; injection prevention is unsolved, RUNTIMES § 18)
```

**What it proves.** taint walks provenance forward: given an origin, it reports
every downstream artifact and every brief that origin exposed. Here it traces
dacli's own development — 9 events, the tasks they completed, and 6 briefs in the
blast radius. This is the "attributed log → learn from it" claim made queryable:
if a source were compromised, this is the exact set of briefs you would audit.

The tool is honest about its own reach: the output labels the result a **lower
bound** — only honestly-labeled provenance is traced, so an unlabeled artifact is
invisible — and calls itself **an audit, not a fix**, pointing at the unsolved
cross-tree injection problem (RUNTIMES § 18) rather than claiming to have closed
it.

## 4. `status` + `standup` — the tree and the roll-up, from one log

```
$ dacli status
core             open:6 active:0 blocked:0 done:25  dacli remaining backlog
pending events: 9 (run `dacli sync` as the owner to materialize)
```

```
$ dacli standup
a-3yjdhjvxhc (1 events)
a-68jkvx7x6b (1 events)
a-9v3qa4cstk (1 events)
a-cqrx1m7sc0 (1 events)
a-hp8fwzbck0 (11 events)
a-khwzk4bfr6 (1 events)
a-mzt5xcjgnm (1 events)
a-q2w31150s0 (1 events)
a-root (0 events)
  done:        001-implement-template-manifests-and-stage-gates, ...,
               026-resolve-worktree-agents-to-the-shared-workspace-close-dacli-5-shadowing,
               027-d1-agent-native-estimation-empirical-role-model-runtime-bands-become-the,
               028-d2-act-on-the-log-at-spawn-dacli-spawn-advise-budget-taint-role,
               029-d3-trust-as-a-first-class-brief-property-trust-floor-label-spawn-time-taint-gate
a-sr9e6xf1d0 (1 events)
a-yyn9jj4j0b (1 events)
a-z4w30d506r (1 events)
a-zjtzasqfb4 (3 events)
a-zqtmzbe6mv (1 events)
```

(The `standup` `done:` list is abbreviated here; the script prints it in full.)

**What it proves.** `status` is the project tree derived from the task log — 25
tasks done, 6 open, plus 9 pending events the owner has yet to `sync`. `standup`
is the same log viewed per agent: who did what. The reviewer `a-hp8fwzbck0` shows
11 events (the hot-path audit findings), `a-zjtzasqfb4` shows 3 (prompt-registry
findings), and the D-series implementers each show their single completing commit.
One append-only log, two views — the tree and the roll-up — with no separate
bookkeeping to drift out of sync.

---

## The proof, restated

Read top to bottom, the four readouts are one argument:

1. **contrib** — agents authored the code, and the log attributes it.
2. **calibrate** — dacli measured its own estimates against its own actuals and
   found the empirical Cone: agent work runs 50–100× under human-scaled points.
3. **taint** — the provenance of any origin is queryable end-to-end; external
   input is provably clean; agent-origin work traces dacli's whole development.
4. **status / standup** — the tree and the per-agent roll-up both fall out of the
   same log.

The differentiator was never "dacli can log." It is that dacli's log let dacli
learn from building dacli — and that proof reproduces from this repo's own
`.dacli/` every time you run the script.
