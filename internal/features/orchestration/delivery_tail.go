package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/commandresult"
	"github.com/mlnomadpy/dacli/internal/gitx"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/procmon"
	"github.com/mlnomadpy/dacli/internal/store"
)

func (d *driver) recordSelfPR() {
	d.pendingLand = d.stillPending(d.pendingLand)

	args := d.shipArgs("--no-accept", "--no-integrate", "--project", d.cfg.project)
	// ship distinguishes the effective PR policy from selecting its PR-capable
	// execution path. A configured policy is intentionally not forwarded as a
	// CLI override by shipArgs, so the record tail must select that path itself
	// or ship refuses before writing the collaboration record (issue #663).
	if d.cfg.landing.Mode == model.LandingPR {
		args = append(args, "--pr")
	}
	if len(d.pendingLand) == 0 {
		args = append(args, "--push")
	} else {
		d.logf("  record: holding the push — %d PR(s) still in flight (%s); pushes once they land", len(d.pendingLand), strings.Join(d.pendingLand, ", "))
	}
	if out, err := d.run.run("record", args...); err != nil {
		d.logf("  record: ship failed: %s", clikit.FirstLine(out))
	}
}

// stillPending refreshes remote-tracking refs (best-effort — a wedged network
// must never block the loop) and drops any branch GitHub has already merged
// (or closed) and deleted from the returned set. `dacli pr --auto` and
// `dacli integrate --pr` both pass --delete-branch, so a branch's remote-
// tracking ref disappearing after a pruning fetch is the same landed signal
// trunkMarker/branchExists already lean on elsewhere in this file.
func (d *driver) stillPending(branches []string) []string {
	if len(branches) == 0 {
		return branches
	}
	if !d.cfg.dryRun {
		_, _ = gitx.RunNetwork(d.w.Root, "fetch", "-q", "--prune", "origin")
	}
	still := branches[:0]
	for _, b := range branches {
		if _, err := d.git("rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+b); err != nil {
			continue // no remote ref at all: nothing can be in flight
		}
		// A remote ref is NOT a PR. A leftover origin/dacli/NNN — from an
		// attempt that never opened one, which nothing deletes — used to read
		// as "still in flight" forever, so the record push was held
		// indefinitely and every later cycle repeated the same conclusion.
		// Ask what the PR is actually doing; only an open one holds the push.
		switch d.prLandStatus(b) {
		case "landing", "stranded":
			still = append(still, b)
		case "merged", "orphaned":
			// Landed or dead — either way it is not blocking the record.
		default:
			// "unknown": gh unreachable or no PR found. Treat a bare ref with
			// no discoverable PR as NOT in flight, because the alternative is
			// the wedge this fixes — an indefinite hold on evidence that is
			// only the ref's existence.
			d.logf("    %s: a remote branch exists but no PR was found — not holding the record for it", b)
		}
	}
	return still
}

// reconcilePendingAccepts checks every task whose accept is still deferred
// (built by a --pr cycle, parked in pendingAccept) against its PR's real
// fate: a confirmed merge closes the task record now — the only point at
// which the backlog is allowed to claim it done, per task 115/issue #74. A PR
// that closed without merging drops the task from tracking so the
// (still-open) task re-enters the ready pool for a fresh attempt instead of
// being stuck behind a rejected PR forever. Anything still open or
// unanswerable is left pending for the next check.
//
// The returned cycleRollup is this call's contribution to the cycle's rollup
// (dacli 299): a merge confirmed here is real trunk-landed work even though
// the task may have been BUILT a cycle or more ago, so it belongs in the
// rollup of the cycle that actually observed the landing, not silently
// dropped because runCycle's own batch never touched it this time around.
func (d *driver) reconcilePendingAccepts() cycleRollup {
	var r cycleRollup
	if len(d.pendingAccept) == 0 {
		return r
	}
	remaining := d.pendingAccept[:0]
	snapshot, snapshotErr := store.LoadTaskSnapshot(d.w)
	for _, p := range d.pendingAccept {
		var task *store.Task
		taskErr := snapshotErr
		if taskErr == nil {
			task, taskErr = snapshot.Find(fmt.Sprintf("%s/%03d", d.cfg.project, p.Seq))
		}
		if taskErr == nil && ((!p.GenerationSet && task.Generation() > 0) || (p.GenerationSet && task.Generation() != p.Generation)) {
			// A reopen deliberately reuses the same sequence and branch for new
			// corrective work. Its earlier merged PR is not evidence that this
			// generation landed, and must neither exclude it nor trigger GC.
			d.logf("    %03d: task was reopened after this recovery entry — invalidating prior-generation pending accept", p.Seq)
			continue
		}
		switch d.prLandStatus(p.Branch) {
		case "merged":
			if taskErr != nil {
				d.logf("    %03d: PR merged but task state could not be resolved — keeping recovery entry: %s", p.Seq, taskErr)
				remaining = append(remaining, p)
				continue
			}
			if task.Status == model.StatusDone && acceptanceComplete(task) {
				// An owner may finish verification between bounded loop invocations.
				// Re-running accept here duplicated evidence and, for command criteria,
				// retried a policy refusal forever even though the canonical record was
				// already truthful (issue #661).
				d.logf("    %03d: PR merged and task already fully accepted — clearing stale recovery entry", p.Seq)
				if !d.checkpointTaskPhase(task, phaseAccepted) {
					remaining = append(remaining, p)
					continue
				}
				d.gcBranch(p.Branch)
				continue
			}
			if !d.checkpointTaskPhase(task, phaseMerged) {
				remaining = append(remaining, p)
				continue
			}
			if taskRequiresVerifierEvidence(task) {
				if !p.VerifyRequired {
					d.logf("    %03d: PR merged but command acceptance requires verifier evidence — run `dacli accept %03d --verify \"<command>\"`; keeping the recovery entry", p.Seq, p.Seq)
				}
				p.VerifyRequired = true
				remaining = append(remaining, p)
				continue
			}
			d.logf("    %03d: PR merged — closing the task record", p.Seq)
			// The close must SUCCEED before this counts as landed. Discarding
			// the error meant a failed accept still incremented Landed and
			// still deleted the branch: the rollup reported the task as landed
			// while it sat open, and the branch that was the evidence was gone.
			// Record-disagrees-with-reality, plus the recovery path destroyed
			// in the same breath (found by errcheck during the dacli 336 review).
			// accept mutates the task tree. End this phase before invoking it and
			// reload even on failure: a failed command may have made a partial
			// durable transition, which must not leave later refs reading stale
			// state from this cycle's old index (issue #686).
			snapshot.Invalidate()
			out, acceptErr := d.run.run("accept", "accept", fmt.Sprintf("%03d", p.Seq), "--force")
			snapshotErr = snapshot.Refresh()
			if acceptErr != nil {
				d.logf("    %03d: PR merged but accept FAILED — task left open and its branch kept for recovery: %s",
					p.Seq, clikit.FirstLine(out))
				remaining = append(remaining, p)
				continue
			}
			if !d.checkpointTaskPhase(task, phaseAccepted) {
				remaining = append(remaining, p)
				continue
			}
			d.gcBranch(p.Branch)
			r.Landed++
		case "orphaned":
			// "Fresh retry" was not fresh. Nothing removed the branch, and
			// AddWorktree reuses an existing one AT ITS OLD TIP, so the next
			// cycle rebuilt on the abandoned base, hit the same non-fast-
			// forward push, and reached the same conclusion — forever. Clear
			// the local branch and worktree, and the stale remote ref that
			// stillPending would otherwise keep reading.
			d.logf("    %03d: PR closed without merging — clearing the branch so the retry starts from trunk", p.Seq)
			d.gcBranch(p.Branch)
			d.dropRemoteBranch(p.Branch)
			if taskErr == nil && !d.clearTaskPhase(task) {
				remaining = append(remaining, p)
				continue
			}
			r.ProducedNothing++
		case "awaiting-pr":
			// A successful empty PR query is not a closed PR. The agent may
			// have committed and pushed before PR creation failed (task 366
			// after run 01KZVR1TQH); keep both refs and tell the operator the
			// missing lifecycle step instead of destroying verified work.
			d.logf("    %03d: branch built and awaiting PR creation — keeping the branch for recovery", p.Seq)
			remaining = append(remaining, p)
			r.Stalled++
		case "stranded":
			// Open, but auto-merge never queued — it will NOT self-land. Say so
			// loudly instead of silently treating it like a queued PR: without this
			// a stranded PR sits open forever, counted as "still landing", holding
			// the record push back and never surfacing that no one is going to
			// merge it (task 290). Kept pending so the loop keeps watching (a human
			// may queue or merge it) rather than dropped, which would re-rank the
			// task and open a duplicate PR against the still-open one.
			d.logf("    %03d: PR open but NOT queued for auto-merge — it will NOT self-land; queue it (`gh pr merge %s --auto`) or merge it by hand (task 290)", p.Seq, p.Branch)
			remaining = append(remaining, p)
			r.Stalled++
		default: // "landing" (queued, PR still open) or "unknown" (gh/network unreachable)
			remaining = append(remaining, p)
			r.Stalled++
		}
	}
	d.pendingAccept = remaining
	return r
}

func acceptanceComplete(t *store.Task) bool {
	boxes := t.Acceptance()
	if len(boxes) == 0 {
		return false
	}
	for _, box := range boxes {
		if !box.Done {
			return false
		}
	}
	return true
}

func taskRequiresVerifierEvidence(t *store.Task) bool {
	for i := range t.Acceptance() {
		if store.AcceptanceRequiresCommandVerification(t, i+1) {
			return true
		}
	}
	return false
}

// cycleRollup is the per-cycle outcome tally `dacli loop status` surfaces
// (dacli 299): how many of the tasks the loop touched this cycle actually
// reached trunk, produced no work at all, are still in flight, or ended the
// cycle blocked — so an unattended run's health is legible from the
// persisted state file alone, without replaying its stdout log.
type cycleRollup struct {
	Landed          int // work reached trunk (a confirmed PR merge, or a local integrate)
	ProducedNothing int // spawn refused/failed, a branch with no commits, or a PR closed unmerged
	Stalled         int // built (or previously built) but not yet confirmed landed
	Blocked         int // the task ended the cycle in status blocked
}

const cycleOutcomeSchema = "loop-cycle-outcome/v1"

type cyclePhaseFailure struct {
	Phase     string `json:"phase"`
	Class     string `json:"class"`
	Retryable bool   `json:"retryable"`
	Detail    string `json:"detail"`
}

type cycleOutcome struct {
	Schema          string              `json:"schema"`
	Selected        int                 `json:"selected"`
	Landed          int                 `json:"landed"`
	ProducedNothing int                 `json:"produced_nothing"`
	Stalled         int                 `json:"stalled"`
	Blocked         int                 `json:"blocked"`
	Classification  string              `json:"classification"`
	Failures        []cyclePhaseFailure `json:"phase_failures"`
}

func (d *driver) recordCycleFailure(phase string, err error, detail string) {
	class, retryable := "operational-degradation", true
	if policyLaunchRefusal(err) {
		class, retryable = "policy-refusal", false
	}
	detail = strings.TrimSpace(detail)
	if detail == "" && err != nil {
		detail = err.Error()
	}
	d.cycleFailures = append(d.cycleFailures, cyclePhaseFailure{Phase: phase, Class: class, Retryable: retryable, Detail: detail})
}

func (d *driver) finishCycleOutcome(selected int, rollup cycleRollup) cycleOutcome {
	out := cycleOutcome{Schema: cycleOutcomeSchema, Selected: selected, Landed: rollup.Landed, ProducedNothing: rollup.ProducedNothing, Stalled: rollup.Stalled, Blocked: rollup.Blocked, Classification: "healthy", Failures: append([]cyclePhaseFailure(nil), d.cycleFailures...)}
	if selected == 0 {
		out.Classification = "healthy-idle"
	} else if rollup.Landed == 0 && len(out.Failures) > 0 {
		out.Classification = "degraded-zero-output"
	}
	return out
}

func (o cycleOutcome) err() error {
	if o.Classification != "degraded-zero-output" {
		return nil
	}
	for _, failure := range o.Failures {
		if failure.Class == "policy-refusal" {
			return clikit.Refusedf("cycle selected %d task(s), landed none, and %s was refused: %s", o.Selected, failure.Phase, failure.Detail)
		}
	}
	return fmt.Errorf("cycle selected %d task(s), landed none, and delivery degraded: %s", o.Selected, o.Failures[0].Detail)
}

// add returns the element-wise sum of r and o — combining reconcile's
// this-pass classification of PRIOR cycles' pending work with THIS cycle's
// own batch classification into the one rollup a checkpoint persists.
func (r cycleRollup) add(o cycleRollup) cycleRollup {
	return cycleRollup{
		Landed:          r.Landed + o.Landed,
		ProducedNothing: r.ProducedNothing + o.ProducedNothing,
		Stalled:         r.Stalled + o.Stalled,
		Blocked:         r.Blocked + o.Blocked,
	}
}

func (r cycleRollup) String() string {
	return fmt.Sprintf("landed %d · produced nothing %d · stalled %d · blocked %d",
		r.Landed, r.ProducedNothing, r.Stalled, r.Blocked)
}

// Recovery renders the NEXT STEP for each non-landing outcome, one line each,
// and nothing at all when everything landed.
//
// A count alone tells an operator that something went wrong and leaves them to
// open six transcripts to find out what to do about it — which is the work
// this rollup exists to replace (task 271). Each line names the command that
// answers "and now what?", so the rollup is a starting point rather than a
// verdict.
func (r cycleRollup) Recovery() []string {
	var out []string
	if r.ProducedNothing > 0 {
		out = append(out, fmt.Sprintf("%d produced nothing — the spawn failed or committed nothing: `dacli runs list` for the outcome, `dacli logs <run>` for why. The task stayed open and will be re-picked.", r.ProducedNothing))
	}
	if r.Stalled > 0 {
		out = append(out, fmt.Sprintf("%d stalled — built but not confirmed landed: `dacli pr status --task <ref>` says whether the PR is merging, behind base, or conflicted.", r.Stalled))
	}
	if r.Blocked > 0 {
		out = append(out, fmt.Sprintf("%d blocked — an agent asked a question it could not answer: `dacli threads` lists them, `dacli answer <id> \"...\"` unblocks the task.", r.Blocked))
	}
	return out
}

// classifyBatch tallies how each task in this cycle's build batch resolved,
// for the rollup (dacli 299). Called after LAND and after the SYNC step has
// applied any status a read-only build agent could only propose, so a task
// the wave blocked on a question (`dacli task block`/`ask`, applied by sync)
// reports blocked rather than merely in flight.
//
//   - ProducedNothing — the spawn never produced a commit at all: refused/
//     failed synchronously, or wait finished with an empty branch (built[t.Seq]
//     was cleared by the post-wait branchHasWork check above).
//   - Blocked — the task's CURRENT status is blocked.
//   - Landed — --no-pr only: ship's integrate step reached trunk and closed
//     the task this same cycle. Under --pr a task never lands within its own
//     build cycle (GitHub merges asynchronously); its landing is observed and
//     rolled up later by reconcilePendingAccepts.
//   - Stalled — everything else: a --pr build parked in pendingAccept awaiting
//     merge confirmation, or a --no-pr integrate that hit a conflict (blocked,
//     per docs/vcs, "never half-merges") and left the task open.
//
// A task that cannot even be reloaded is counted stalled, never landed or
// blocked — the same honest-degrade rule an unmeasurable trunk gets (dacli
// 212): absence of a signal must never be spelled as a stronger one.
func (d *driver) classifyBatch(batch []*store.Task, built map[int]bool) cycleRollup {
	var r cycleRollup
	snapshot, snapshotErr := store.LoadTaskSnapshot(d.w)
	for _, t := range batch {
		var cur *store.Task
		err := snapshotErr
		if err == nil {
			cur, err = snapshot.Find(fmt.Sprintf("%03d", t.Seq))
		}
		if !built[t.Seq] {
			if err == nil && cur.Status == model.StatusBlocked {
				r.Blocked++
			} else {
				r.ProducedNothing++
			}
			continue
		}
		switch {
		case err != nil:
			r.Stalled++
		case cur.Status == model.StatusBlocked:
			r.Blocked++
		case !d.cfg.pr && cur.Status == model.StatusDone:
			r.Landed++
		default:
			r.Stalled++
		}
	}
	return r
}

func (d *driver) policyRefusedSince(taskID, since string) bool {
	entries, _ := os.ReadDir(d.w.RunsDir())
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() <= since {
			continue
		}
		dir := d.w.RunDir(entry.Name())
		rec, err := procmon.ReadRecord(filepath.Join(dir, "proc.txt"))
		if err != nil || rec.Task != taskID {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, "outcome.md"))
		if err == nil && strings.Contains(string(raw), "exit: exit status 3") {
			return true
		}
	}
	return false
}

// gcBranch removes a task's worktree and local branch once its work has landed
// on trunk (a confirmed PR merge). Without this the workspace accumulates a
// worktree and a stale branch per completed task indefinitely — a live
// workspace reached 72 worktrees / 71 merged local branches (dacli 182). Only
// called on a CONFIRMED merge, never a blanket sweep: a zero-commit branch of a
// still-running agent is trivially an ancestor of trunk, so sweeping by
// ancestry would risk deleting a live worktree mid-work. Best-effort — a GC
// failure never blocks the loop.
func (d *driver) gcBranch(branch string) {
	if wts, err := gitx.ListWorktrees(d.w.Root); err == nil {
		for _, wt := range wts {
			if wt.Branch == branch {
				_ = gitx.RemoveWorktree(d.w.Root, wt.Path)
				break
			}
		}
	}
	_, _ = d.git("branch", "-D", branch)
}

// reapWorktrees is the blanket, safety-checked counterpart to gcBranch: once a
// cycle it reclaims EVERY worktree whose branch has landed on trunk or whose
// run has finished, not just the ones this loop is tracking a PR for. gcBranch's
// caution against a blanket ancestry sweep is honored inside
// store.ReclaimableWorktrees — a bare-tipped live spawn (zero commits, trivially
// an ancestor of trunk) is never reclaimed, and a merged worktree is touched
// only when its tree is clean. Best-effort: a reap failure never blocks the loop.
func (d *driver) reapWorktrees() {
	trunk := d.trunkBranch
	if trunk == "" {
		trunk = "main"
	}
	removed, err := store.PruneWorktrees(d.w, trunk)
	if err != nil {
		return
	}
	for _, c := range removed {
		d.logf("    reclaimed worktree %s (%s — %s)", filepath.Base(c.Path), c.Branch, c.Reason)
	}
}

// runGH runs the GitHub CLI for the loop's own merge-confirmation checks. A
// package variable so a test can stub it, mirroring features/vcs's identical
// runGH — duplicated rather than imported because the feature-slice isolation
// rule (arch_test's TestFeatureSlicesAreIsolated) forbids orchestration
// importing vcs (see taskBranch above for the same reasoning).
var runGH = func(dir string, args ...string) (string, error) {
	pctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	c := exec.CommandContext(pctx, "gh", args...)
	c.Dir = dir
	out, err := commandresult.Run(c, commandresult.RunOptions{
		Operation:     "gh pr status",
		WorkspaceRoot: dir,
		TimedOut: func() bool {
			return pctx.Err() == context.DeadlineExceeded
		},
	})
	return strings.TrimSpace(string(out)), err
}

// prLandStatus classifies whether branch has actually reached trunk:
//   - "merged"   — the branch's work is on trunk now.
//   - "landing"  — a PR is open WITH GitHub auto-merge queued; it lands itself
//     the instant CI passes. The healthy --pr --auto path.
//   - "stranded" — a PR is open but NO auto-merge is queued: the fixer's
//     `dacli pr --auto` failed to queue it (repo has "Allow auto-merge" off, or
//     GitHub was unreachable) and reported that non-zero. It will NOT self-land,
//     so the loop must not keep counting it as still-landing forever (task 290).
//   - "awaiting-pr" — the branch has not landed and no PR exists yet.
//   - "orphaned" — a PR closed without merging: safe to rebuild from trunk.
//   - "unknown"  — gh and a trunk fetch both failed to answer.
//
// This mirrors features/vcs's checkLanded (gh state first, a fresh-fetch
// ancestor check only when gh finds no PR) but is duplicated, not imported —
// same feature-slice isolation reasoning as runGH above. It goes one step
// further than checkLanded (which the operator reads at `dacli pr status`) by
// splitting an open PR into landing vs. stranded: an unattended loop has no
// human to notice a stranded PR sitting open, so it must tell the two apart
// itself.
func (d *driver) prLandStatus(branch string) string {
	noPR := false
	if out, err := runGH(d.w.Root, "pr", "list", "--head", branch, "--state", "all", "--json", "state,autoMergeRequest", "--limit", "1"); err == nil {
		var prs []struct {
			State            string `json:"state"`
			AutoMergeRequest *struct {
				EnabledAt string `json:"enabledAt"`
			} `json:"autoMergeRequest"`
		}
		if jerr := json.Unmarshal([]byte(out), &prs); jerr == nil && len(prs) == 0 {
			noPR = true
		} else if jerr == nil && len(prs) > 0 {
			switch strings.ToUpper(prs[0].State) {
			case "MERGED":
				return "merged"
			case "OPEN":
				if prs[0].AutoMergeRequest != nil {
					return "landing"
				}
				return "stranded"
			case "CLOSED":
				return "orphaned"
			}
		}
	}
	if d.cfg.dryRun {
		return "unknown"
	}
	hasOrigin := d.hasOrigin()
	b := d.trunkBranch
	if b == "" {
		b = "main"
	}
	// Which trunk ref answers "did this land?" — origin's when there is a
	// remote, the LOCAL branch when there is not.
	//
	// A workspace with no origin used to dead-end here: the fetch failed, every
	// branch reported "unknown", pendingAccept never resolved, and so no task
	// EVER closed. `next` then re-picked the same work every cycle, forever —
	// issue #382's first and worst symptom, on a repo that had merged its
	// branches into trunk perfectly well, just locally.
	trunkRef := "origin/" + b
	if hasOrigin {
		if _, err := gitx.RunNetwork(d.w.Root, "fetch", "-q", "origin", "--", b); err != nil {
			return "unknown"
		}
	} else {
		trunkRef = b
		if _, err := d.git("rev-parse", "--verify", "--quiet", "refs/heads/"+b); err != nil {
			// No remote AND no local trunk: there is nothing to have landed
			// into. Saying "unknown" here is honest — and the loop's no-trunk
			// warning (see runCycle) tells the operator why.
			return "unknown"
		}
	}
	// A branch with no commits beyond trunk is trivially an ancestor of it. Two
	// very different situations produce that, and they must not be conflated:
	//
	//   - a spawn that died before committing — the branch never carried work,
	//     and reporting it "merged" force-accepts an empty task (dacli 168);
	//   - a branch whose commits ARE in trunk because it was merged — which is
	//     precisely a landing.
	//
	// Ancestry alone cannot tell them apart after the merge, so compare tips:
	// an unstarted branch still points at the commit trunk was on when it was
	// cut, while a merged branch's tip is a commit that was made ON it and
	// trunk has moved past (to the merge commit).
	if n, err := d.git("rev-list", "--count", trunkRef+".."+branch); err == nil && strings.TrimSpace(n) == "0" {
		branchTip, e1 := d.git("rev-parse", branch)
		trunkTip, e2 := d.git("rev-parse", trunkRef)
		if e1 != nil || e2 != nil || strings.TrimSpace(branchTip) == strings.TrimSpace(trunkTip) {
			if noPR {
				return "awaiting-pr"
			}
			if !hasOrigin {
				return "orphaned"
			}
			return "unknown"
		}
		return "merged"
	}
	ok, err := gitx.IsAncestor(d.w.Root, branch, trunkRef)
	if err != nil {
		return "unknown"
	}
	if ok {
		return "merged"
	}
	if noPR {
		return "awaiting-pr"
	}
	if !hasOrigin {
		return "orphaned"
	}
	return "unknown"
}

// hasOrigin reports whether the repo has an `origin` remote at all. Cheap,
// local, and the difference between "the PR has not merged yet" and "there is
// no GitHub in this picture" — two states the loop used to conflate into
// "unknown", which is the state that never resolves.
func (d *driver) hasOrigin() bool {
	out, err := d.git("remote")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "origin" {
			return true
		}
	}
	return false
}

// excludePending drops every task whose Seq is parked in pending from the
// ready frontier — a task built this loop's --pr cycle stays open (never
// accepted) until its PR's merge is confirmed, so without this it would be
// picked up and rebuilt by the very next cycle while its first PR is still
// in flight.
func excludePending(tasks []*store.Task, pending []pendingAccept) []*store.Task {
	if len(pending) == 0 {
		return tasks
	}
	skip := make(map[int]bool, len(pending))
	for _, p := range pending {
		skip[p.Seq] = true
	}
	out := tasks[:0]
	for _, t := range tasks {
		if !skip[t.Seq] {
			out = append(out, t)
		}
	}
	return out
}

// resolveTrunkBranch finds the branch ship/integrate lands into — the repo's
// default branch — so trunk advancement is measured against the right ref.
//
// The answer is always a branch that exists, or nothing. Two ways it used to
// be neither (dacli 211): on a detached HEAD the last resort was
// `rev-parse --abbrev-ref HEAD`, which returns the literal string "HEAD" — so
// trunkMarker went on to count `origin HEAD` and syncTrunk merged whatever
// arbitrary ref that named; and with origin/HEAD unset (the norm in CI and in
// shallow clones) it fell straight through to a local `main`, which on a repo
// whose work lands on origin/master is a branch nothing ever reaches, making
// every progress measurement a measurement of the wrong thing.
//
// Order of preference, most authoritative first: what origin says its default
// is; then a remote-tracking branch, because trunk is where work LANDS and
// that is a property of the remote, not of this checkout; then a local branch,
// which is all an ordinary offline repo has; then the checked-out branch via
// symbolic-ref, which — unlike rev-parse --abbrev-ref — fails on a detached
// HEAD instead of inventing a name. Nothing resolvable returns "", and the
// callers (trunkMarker, syncTrunk, shipArgs) each already degrade honestly on
// an empty trunk rather than guessing.
func (d *driver) resolveTrunkBranch() string {
	// --into wins outright. A sprint integrates a batch of related work onto
	// its own branch and takes ONE pull request to main at the end, instead of
	// one PR per fix; without this the loop always resolved main and refused
	// the moment the checkout was on the sprint branch ("refusing to operate
	// on the wrong branch"), which made the whole sprint workflow unusable
	// (dacli 332). Validated in cmdLoop before the driver is built, so an
	// unknown branch is a usage error rather than a mid-cycle surprise.
	if d.cfg.into != "" {
		return d.cfg.into
	}
	if out, err := d.git("rev-parse", "--abbrev-ref", "origin/HEAD"); err == nil {
		s := strings.TrimSpace(out) // "origin/main"
		if i := strings.LastIndex(s, "/"); i >= 0 {
			s = s[i+1:]
		}
		if s != "" && s != "HEAD" {
			return s
		}
	}
	for _, b := range []string{"main", "master"} {
		if _, err := d.git("rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+b); err == nil {
			return b
		}
	}
	for _, b := range []string{"main", "master"} {
		if _, err := d.git("rev-parse", "--verify", "--quiet", "refs/heads/"+b); err == nil {
			return b
		}
	}
	// symbolic-ref reports the branch HEAD points AT — it errors on a detached
	// HEAD (and still answers on an unborn branch in a fresh repo), which is
	// exactly the distinction rev-parse --abbrev-ref throws away.
	if out, err := d.git("symbolic-ref", "--quiet", "--short", "HEAD"); err == nil {
		if s := strings.TrimSpace(out); s != "" && s != "HEAD" {
			return s
		}
	}
	return ""
}

// trunkMarker is a monotonic count of commits that have reached trunk — local
// OR origin — so it captures both in-cycle local integrations and the async
// GitHub auto-merges the default --pr --auto path produces. It refreshes the
// remote-tracking ref first (so async auto-merges become visible) and degrades
// to the local count when there is no remote.
//
// The bool is the whole point: it reports whether the count could be MEASURED
// at all. Returning a bare 0 when every rev-list variant failed — an index
// lock, a timeout, git briefly unavailable — was indistinguishable from a
// genuinely empty trunk, and the consequences compounded: that cycle computed
// `landed = 0 - prevTrunk`, clamped it to 0 and bumped the thrash streak
// toward a false halt, then set prevTrunk = 0, so the NEXT cycle read the whole
// repository history as this cycle's progress and reset the streak. The thrash
// guard's input must never be a fabricated number (dacli 212).
func (d *driver) trunkMarker() (int, bool) {
	b := d.trunkBranch
	if b == "" {
		b = "main"
	}
	if !d.cfg.dryRun {
		// Network-bound: a hung fetch (wedged network, a credential prompt) must
		// not block the loop — it gets the longer network leash and, on timeout,
		// this degrades to the local-only rev-list count below, the existing
		// best-effort fallback.
		_, _ = gitx.RunNetwork(d.w.Root, "fetch", "-q", "origin", "--", b)
	}
	for _, refs := range [][]string{{b, "origin/" + b}, {b}, {"origin/" + b}} {
		args := append([]string{"rev-list", "--count"}, refs...)
		// Exclude the loop's OWN bookkeeping: recordSelfPR commits a .dacli-only
		// record onto trunk every cycle, so counting all commits would make
		// `landed` >= 1 unconditionally and the thrash guard (NoProgressHalt)
		// could never fire. Progress is CODE reaching trunk, not the loop
		// narrating itself (dacli 171).
		args = append(args, "--", ":(exclude).dacli")
		if out, err := d.git(args...); err == nil {
			var n int
			if _, e := fmt.Sscanf(strings.TrimSpace(out), "%d", &n); e == nil {
				return n, true
			}
		}
	}
	return 0, false
}

// syncTrunk fast-forwards the local trunk checkout up to origin's latest —
// the reconciliation step for a `gh pr merge --auto` landing that happened
// between cycles. It only ever fast-forwards: gitx.FastForward refuses (and
// this just logs, never fails the loop) the moment local has a commit origin
// lacks, e.g. a record commit made but not yet pushed in a prior cycle — that
// case is left for recordSelfPR's own push (via gitx.PushSync) to reconcile
// with a rebase.
func (d *driver) syncTrunk() {
	if d.cfg.dryRun {
		return
	}
	b := d.trunkBranch
	if b == "" {
		b = "main"
	}
	if out, err := gitx.FastForward(d.w.Root, b); err != nil {
		d.logf("  note: local %s not fast-forwarded to origin: %s", b, clikit.FirstLine(out))
	}
}

// git runs a local (non-network) git op under gitx's short deadline, so a
// wedged git child (an index lock, a credential-helper prompt) can never
// block the loop indefinitely.
func (d *driver) git(args ...string) (string, error) {
	return gitx.Run(d.w.Root, args...)
}

// shipArgs prepends "ship" and appends --into <trunk> to a ship invocation.
// ship defaults --into to "main" and refuses up front when the checkout is not
// that branch, so on a repo whose trunk is master/renamed the loop's LAND and
// record-ship steps would fail every cycle without forwarding the resolved
// trunk (dacli 174). Omitted when the trunk could not be resolved, letting ship
// keep its own default.
func (d *driver) shipArgs(rest ...string) []string {
	args := append([]string{"ship"}, rest...)
	if d.cfg.landingExplicit {
		mode := d.cfg.landing.Mode
		if mode == "" {
			mode = model.LandingLocal
		}
		args = append(args, "--landing-mode", string(mode))
		if d.trunkBranch != "" {
			args = append(args, "--landing-base", d.trunkBranch)
		}
		return args
	}
	// A configured base is resolved again by ship from the same project. Only
	// forward the repository-derived fallback, which ship cannot otherwise
	// infer consistently on renamed trunks (dacli 174).
	if d.cfg.landing.Base == "" && d.trunkBranch != "" {
		args = append(args, "--into", d.trunkBranch)
	}
	return args
}

func (d *driver) trunkBase() string {
	if d.trunkBranch != "" {
		return d.trunkBranch
	}
	return d.cfg.landing.Base
}

// taskBranch is the task-branch naming convention, duplicated (not imported)
// from features/vcs.BranchFor: the feature-slice isolation rule (arch_test's
// TestFeatureSlicesAreIsolated) forbids orchestration importing vcs, and this
// is the one fact of that convention the loop needs to verify a spawn actually
// produced a branch.
func taskBranch(t *store.Task) string {
	return fmt.Sprintf("dacli/%03d-%s", t.Seq, t.Slug)
}

// branchExists reports whether branch exists either as a local ref or as an
// already-fetched remote-tracking ref — a worktree spawn commits locally, a
// --pr spawn additionally pushes, and trunkMarker's fetch may or may not have
// run yet, so both are checked.
func (d *driver) branchExists(branch string) bool {
	if _, err := d.git("rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
		return true
	}
	if _, err := d.git("rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+branch); err == nil {
		return true
	}
	return false
}

// branchHasWork reports whether branch carries at least one commit beyond the
// trunk it was forked from. The worktree+branch is created at SPAWN time
// (gitx.AddWorktree), so the branch exists at trunk's tip before the child has
// done anything — existence alone is NOT evidence of work. A child that OOMs,
// is killed, or is refused by its runtime right after launch leaves a
// zero-commit branch, which is trivially an ancestor of trunk and would
// otherwise be misread as "merged" and force-accepted as done with no work
// (dacli 168). Compares against a ref that exists (local trunk, else
// origin/trunk); on any git error it returns true, so an unmeasurable branch
// is never destroyed on a false negative — the PR/ancestor checks still apply.
func (d *driver) branchHasWork(branch string) bool {
	if !d.branchExists(branch) {
		return false
	}
	base := d.trunkBranch
	if base == "" {
		base = "main"
	}
	for _, ref := range []string{base, "origin/" + base} {
		if out, err := d.git("rev-list", "--count", ref+".."+branch); err == nil {
			return strings.TrimSpace(out) != "0"
		}
	}
	return true
}

// maxStageAdvancesPerCycle bounds advanceStages. A manifest is a handful of
// stages, and a project whose gates ALL open at once should reach its
// implementation phase in one cycle rather than crawling one stage per cycle;
// the cap only guards against a pathological manifest looping forever.
const maxStageAdvancesPerCycle = 8

// templateStage reads the project's current stage straight off its
// frontmatter. Deliberately not gates.Status: Status evaluates every predicate
// for the stage, and the `command:`/`coverage:` predicates shell out (each with
// a ten-minute leash). This is the cheap "is this project gated at all?"
// question, and for the overwhelmingly common untemplated/solo project it is
// the ONLY gates-related work the loop does. Returns "" when the project has no
// template, and "complete" once every gate has been passed.

// Subprocess diagnostics wrap exec.ExitError, which clikit's in-process
// error classifier does not interpret. Exit 3 remains a policy answer.
func policyLaunchRefusal(err error) bool {
	var exit interface{ ExitCode() int }
	return clikit.ExitCode(err) == 3 || (errors.As(err, &exit) && exit.ExitCode() == 3)
}
