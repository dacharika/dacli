// Package ship is the one-command wave tail: `dacli ship` closes the loop the
// operator still runs by hand after a wave of agents finish — accept the done
// tasks, integrate their branches, commit the workspace record SAFELY, and
// (optionally) push. Every step the operator used to type is now one command
// that stops honestly at the first failure and never half-ships.
//
// It is a feature slice (ARCHITECTURE § 2b) and imports NO other slice. Because
// slices cannot call each other, ship ORCHESTRATES by shelling out to its own
// binary (os.Executable) — `dacli accept`, `dacli integrate` — exactly as the
// prompt templates tell agents to invoke dacli. The record commit and push are
// git operations done directly through the shared gitx layer (an entity
// package, so importing it is allowed): the record commit lands on the
// integration branch, where `dacli commit` refuses to run.
//
// The pipeline, stopping at the first non-zero step so nothing is left
// half-shipped:
//
//  1. accept   — shell `dacli accept --all --force --defer-landing [--verify
//     "cmd"]`: verify-then-close every task an agent proposed for acceptance.
//     --force is always passed — `accept` only honors it for root, so it
//     reconciles a wave's tasks left owned by an agent that already finished
//     (and will never sync to apply its own proposal) instead of stalling on
//     the orphan. --defer-landing skips accept's own "did the branch reach
//     trunk?" check: accept necessarily runs BEFORE integrate here (integrate
//     refuses a non-done task), so checking now would always see "not yet"
//     and durably record that on every task this run is about to land —
//     recordWaveLanding (below, step 2b) records the real verdict instead,
//     once integrate has had its chance (dacli 329).
//  2. integrate— shell `dacli integrate --tasks <done seqs> --into <branch>`:
//     merge each done task's branch. A conflict blocks that task; ship
//     detects the block and stops before committing or pushing.
//     2b. landing — recordWaveLanding re-checks and stamps the true landing
//     verdict for every task accept closed this run, now that integrate has
//     run (or been skipped). Runs even when integrate stops on a conflict or
//     a hard error, so a genuinely unlanded close still reads as unlanded.
//  3. record   — stage ONLY .dacli (NEVER `git add -A`, the footgun that once
//     tracked a worktree gitlink) and commit the workspace state.
//  4. push     — with --push, push the integration branch; without it, print the
//     push command so the operator stays in control.
//
// `--dry-run` prints each step it would run and executes nothing.
package ship

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/mlnomadpy/dacli/internal/agentid"
	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/commandresult"
	"github.com/mlnomadpy/dacli/internal/eventlog"
	"github.com/mlnomadpy/dacli/internal/gitx"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

// Commands is this slice's table, aggregated by the app layer (cli.go).
var Commands = []clikit.Command{
	{Path: "ship", Brief: "Wave owner composes accept + integrate + durable record (and optional push/release); it is the wave boundary, not a worker completion command", Mutates: true, Usage: "dacli ship [--project slug] [--tasks refs] [--pr | --landing-mode local] [--into BRANCH | --landing-base BRANCH] [--verify \"cmd\"] [--no-accept] [--no-integrate] [--auto] [--merge] [--no-merge] [--record-branch BRANCH] [--push] [--release TAG] [--dry-run]", Run: cmdShip},
}

// shellDacli runs a dacli subcommand by shelling this binary, so ship
// orchestrates the accept/integrate steps without importing sibling slices. It
// runs from the workspace root (where the integration branch is checked out and
// .dacli lives) and inherits the environment, so the child resolves the same
// workspace and carries the operator's DACLI_AGENT for attribution. It is a
// package variable so a test can substitute an in-process runner.
var shellDacli = func(ctx *clikit.Ctx, w *workspace.Workspace, args ...string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		exe = "dacli"
	}
	c := exec.Command(exe, args...)
	c.Dir = w.Root
	var out []byte
	if len(args) > 0 && args[0] == "integrate" {
		var result commandresult.Integration
		out, err = commandresult.Capture(c, &result)
		ctx.Result = result
	} else {
		operation := "dacli ship child"
		if len(args) > 0 {
			operation = "dacli " + args[0]
		}
		out, err = commandresult.Run(c, commandresult.RunOptions{
			Operation:     operation,
			WorkspaceRoot: w.Root,
		})
	}
	// Stream the child's output so the operator sees each step's per-task result.
	fmt.Fprint(ctx.Stdout, string(out))
	return string(out), err
}

func cmdShip(ctx *clikit.Ctx, args []string) error {
	w, id, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	f, _ := clikit.ParseFlags(args)
	if err := f.Reject("into", "dry-run", "no-accept", "verify", "project", "no-integrate", "push", "pr", "auto", "no-merge", "merge", "record-branch", "tasks", "release", "release-title", "release-notes", "landing-mode", "landing-base"); err != nil {
		return err
	}
	policy, explicit, err := effectiveShipPolicy(w, f)
	if err != nil {
		return err
	}
	if policy.Mode == model.LandingPR && !f.Bool("pr") {
		return clikit.Refusedf("project landing policy requires the PR path; re-run with --pr, or explicitly override it with --landing-mode local")
	}
	into := clikit.OrDash(policy.Base, "main")
	// A ship into a branch that does not exist cannot integrate anything, and
	// used to report "integrated 0 task(s)" — indistinguishable from "there was
	// nothing to do". That is how a repo whose trunk was never established
	// (a no-worktree spawn branched the main checkout and left it there)
	// looked healthy while landing nothing at all (issue #382 item 3). Say
	// which branch is missing, and name the two ways forward.
	if f.Get("into") == "" && gitx.Available() && !gitx.BranchExists(w.Root, into) {
		return clikit.Refusedf("there is no %s branch to integrate into, so nothing can land. Create it (git checkout -b %s && git commit), or name the real trunk with --into <branch>", into, into)
	}
	dry := f.Bool("dry-run")
	release := f.Get("release")

	if !gitx.Available() {
		return fmt.Errorf("git not on PATH")
	}
	if dry {
		return printPlan(ctx, w, id, f, policy, explicit, into)
	}
	// The real pipeline writes to the repo (integrate/commit/push).
	if id.Grant != model.GrantRW {
		return clikit.Refusedf("ship integrates, commits and pushes; that needs an rw grant (yours is %s)", id.Grant)
	}
	// Guard the branch UP FRONT: shipping onto a branch other than `into` is
	// never intended, and catching it here means accept never runs against a
	// pipeline that integrate would only refuse a step later (never half-ship).
	if cur := gitx.CurrentBranch(w.Root); cur != into {
		return clikit.Refusedf("checkout %s before shipping (currently on %s) — ship integrates and records onto --into", into, cur)
	}

	// Snapshot the done set BEFORE accept so integrate is scoped to the WAVE this
	// run closes — the tasks done afterward but not before — never the full
	// history. Skipped when --tasks names an explicit window: the operator has
	// already said exactly what to integrate (task 261).
	window := f.Get("tasks")
	// PR shipping an explicit window is a landing transaction, not the legacy
	// wave tail. Keep each selected task nonterminal while GitHub checks and the
	// merge can still fail; only the observed landed state may enter acceptance
	// (issue #841). The legacy accept-first order remains for local waves, where
	// integrate's done-only scan is the concurrency boundary.
	transactionWave, landThenAccept, err := prepareLandThenAccept(w, window, policy.Mode == model.LandingPR && window != "" && !f.Bool("no-accept") && !f.Bool("no-integrate"))
	if err != nil {
		return err
	}
	if landThenAccept {
		if f.Get("verify") == "" {
			return clikit.Usagef("the PR land-then-accept transaction requires --verify <command> so acceptance is proved on fresh trunk before the task and issue close")
		}
		if f.Bool("no-merge") {
			return clikit.Usagef("--no-merge cannot complete a land-then-accept transaction; add --no-accept to open the PR without finalizing the task, then rerun ship after review")
		}
	}
	var preDone map[string]bool
	if window == "" {
		preDone, err = doneKeys(w, f.Get("project"))
		if err != nil {
			return err
		}
	}

	// --release cuts a tagged GitHub release AFTER the wave lands (step 5). Its
	// preconditions are validated UP FRONT — before accept runs — so ship never
	// integrates and records a wave and only THEN refuses the release it was
	// asked to cut (never half-ship). Three conditions make a truthful release:
	//   - not --pr: PR-first mode merges to the target on GitHub's clock,
	//     asynchronously (an --auto PR merges only when CI goes green), so a
	//     release cut now would tag a target that has not merged the wave yet.
	//   - --push: the release tags the state on the REMOTE, so the merged work
	//     must have reached origin — the local-merge path only lands it locally
	//     until --push. A release of un-pushed commits is a record that lies.
	//   - --project: the release resolves the repo from the linked project.
	if release != "" {
		if f.Bool("pr") {
			return clikit.Refusedf("--release cannot be combined with --pr: PR-first integration merges to %s on GitHub's clock (asynchronously), so a release cut now could tag %s before the wave's PRs merge — cut the release in a separate step once they land", into, into)
		}
		if !f.Bool("push") {
			return clikit.Refusedf("--release %s cuts a release of pushed work, but --push was not passed; a release of un-pushed commits would tag a state the remote does not have — re-run with --push", release)
		}
		if f.Get("project") == "" {
			return clikit.Usagef("--release needs --project <slug> to resolve the linked repo to release")
		}
	}

	// 1. accept — verify-then-close every proposed task. A failed verify (or any
	//    non-zero exit) stops the pipeline here: nothing has been integrated,
	//    committed or pushed yet.
	//
	// --defer-landing: accept necessarily runs BEFORE integrate here (integrate
	// refuses a non-done task), so accept's own "did the branch reach trunk?"
	// check would always see "not yet" and durably record that on every task
	// ship is about to land seconds later — a false record stamped on every
	// successful ship (dacli 329). --defer-landing skips that check inside
	// accept; recordWaveLanding below records the REAL verdict once integrate
	// has actually had its chance to run.
	ranAccept := !f.Bool("no-accept")
	if landThenAccept {
		ranAccept = false
	}
	if ranAccept {
		// --force is always forwarded: `dacli accept` only honors it for the
		// root identity, so this is a no-op unless ship itself is running as
		// root — but when it is, a wave's orphaned tasks (owned by a spawned
		// agent that has since finished and will never sync) get reconciled
		// and closed instead of sitting as a pending proposal forever.
		acceptArgs := []string{"accept", "--all", "--force", "--defer-landing"}
		if v := f.Get("verify"); v != "" {
			acceptArgs = append(acceptArgs, "--verify", v)
		}
		if _, err := shellDacli(ctx, w, acceptArgs...); err != nil {
			return fmt.Errorf("ship stopped at accept (nothing integrated, committed or pushed): %w", err)
		}
	}

	// 2. integrate — merge each WAVE task's branch into `into`. The wave is the
	//    tasks THIS run closed (done now, but not before accept ran) or the
	//    explicit --tasks window — NOT every task ever closed. Integrating the
	//    full done set makes ship strictly more dangerous the longer a project
	//    runs: on a long backlog it re-drives hundreds of long-settled tasks
	//    through integrate and, in --pr mode, re-pushes their branches and
	//    reopens their PRs (task 261).
	var wave []*store.Task
	if landThenAccept {
		wave = transactionWave
	} else {
		wave, err = shipWave(w, f.Get("project"), preDone, window)
	}
	if err != nil {
		return err
	}
	// Snapshot each wave task's branch ref BEFORE integrate runs: a clean local
	// merge (vcs.mergeTask) DELETES the branch once it lands, so a landing check
	// made only afterward can no longer find it and would misread a merged task
	// as "no branch — nothing to check" instead of "landed". Capturing the
	// commit now lets recordWaveLanding ask "was THIS commit merged?" instead of
	// "does this branch still exist?" once integrate is done with it.
	waveRefs := captureWaveRefs(w, wave)
	// merged counts the branches integrate ACTUALLY merged, so the record commit
	// message reports what really landed — not the raw wave-task count, which
	// overstates it whenever a task has no branch (skipped) or a merge fails.
	merged := 0
	if !f.Bool("no-integrate") {
		if len(wave) == 0 {
			fmt.Fprintln(ctx.Stdout, "integrate: no tasks in this wave to integrate")
		} else {
			iargs := []string{"integrate", "--tasks", strings.Join(doneRefs(wave), ","), "--into", into}
			if landThenAccept {
				iargs = append(iargs, "--force")
				// The transaction certifies the exact reviewed head on fresh trunk.
				// A squash rewrites that identity, so use a merge commit even though
				// standalone integrate defaults to squash (issue #841).
				if !f.Bool("merge") {
					iargs = append(iargs, "--merge")
				}
			}
			if p := f.Get("project"); p != "" {
				iargs = append(iargs, "--project", p)
			}
			// Pass the already-resolved policy to integrate. The child resolves the
			// same typed values and records any explicit override durably. PR mode
			// pushes each branch, opens an enriched PR, and merges via gh. --no-merge
			// opens the PRs and stops for human review; --merge picks a merge commit
			// over the default squash. Default (no --pr) keeps the local-merge path.
			iargs = append(iargs, landingFlags(f, policy, explicit)...)
			_, err := shellDacli(ctx, w, iargs...)
			if err != nil {
				// integrate now propagates a genuine (non-conflict) merge failure
				// as a non-zero exit — a dirty code tree, a missing branch,
				// unrelated histories. Stop here: nothing has been recorded or
				// pushed, so a hard integrate failure can never half-ship. Some of
				// the wave may still have merged before the error, so the landing
				// verdict recorded below must reflect exactly what git shows now —
				// a genuinely unlanded task must stay visibly unlanded.
				if ranAccept {
					recordWaveLanding(w, into, wave, waveRefs)
				}
				return fmt.Errorf("ship stopped at integrate (workspace record not committed, nothing pushed): %w", err)
			}
			// integrate exits 0 even on a conflict (it prints the conflict and
			// blocks that one task). Detect the block SEMANTICALLY — a task from
			// our done set now sitting in blocked — and stop before recording or
			// pushing, so a conflict never half-ships.
			if b := blockedAmong(w, wave); len(b) > 0 {
				if ranAccept {
					recordWaveLanding(w, into, wave, waveRefs)
				}
				return clikit.Refusedf("ship stopped: task(s) %s blocked on a merge conflict — resolve on the branch, then re-run ship (nothing committed or pushed)", strings.Join(b, ", "))
			}
			result, ok := ctx.Result.(commandresult.Integration)
			if !ok {
				return fmt.Errorf("integrate returned no structured command result")
			}
			merged = result.Merged
			if landThenAccept && result.Open > 0 {
				return clikit.Refusedf("ship stopped: %d selected PR(s) remain open because checks or merge gates did not pass; tasks remain nonterminal with their branches, worktrees, and evidence preserved — fix the reported gate and re-run the same ship command", result.Open)
			}
		}
	}
	if landThenAccept {
		if err := requireFreshLanding(w, into, wave, waveRefs); err != nil {
			return err
		}
		finalCommit, finalTree, err := freshLandingArtifact(w, into)
		if err != nil {
			return err
		}
		for _, t := range wave {
			acceptArgs := []string{"accept", taskRef(t), "--force", "--into", into, "--final-commit", finalCommit, "--final-tree", finalTree}
			if v := f.Get("verify"); v != "" {
				acceptArgs = append(acceptArgs, "--verify", v)
			}
			if _, err := shellDacli(ctx, w, acceptArgs...); err != nil {
				return fmt.Errorf("ship landed the PR but stopped at post-landing acceptance for %03d-%s; the task remains nonterminal and the merged PR is recorded: %w", t.Seq, t.Slug, err)
			}
		}
		ranAccept = true
		project := f.Get("project")
		if project == "" && len(wave) > 0 {
			project = wave[0].Project
		}
		if project != "" {
			args := append([]string{"github", "push", project}, doneRefs(wave)...)
			args = append(args, "--closure-only")
			if _, err := shellDacli(ctx, w, args...); err != nil {
				return fmt.Errorf("ship accepted landed work but scoped GitHub issue closure failed: %w", err)
			}
		}
	}
	// The wave's landing verdict is only settled now — integrate has had its
	// one chance to land each branch. Recording it here (not inside accept,
	// which ran first) is the fix for dacli 329: covers a clean integrate, a
	// skipped one (--no-integrate), and an empty wave (a no-op).
	if ranAccept {
		recordWaveLanding(w, into, wave, waveRefs)
	}

	// 3. record — commit the .dacli workspace state, staging ONLY .dacli. The
	//    message reports branches ACTUALLY merged, never the done-task count.
	// An explicit --record-branch wins; otherwise the workspace's configured
	// branch, which `dacli new` sets whenever it gitignores .dacli. Without
	// this fallback the default workspace would be ignored AND unrecorded —
	// the exact history loss that kept the ignore opt-in.
	recordBranch := f.Get("record-branch")
	if recordBranch == "" {
		recordBranch = w.RecordBranch
	}
	if err := commitRecord(ctx, w, id, merged, recordBranch); err != nil {
		return err
	}

	// 4. push — opt-in, so the operator decides when work leaves the machine.
	//
	// BOTH refs, when a record branch is configured. Step 3 puts the record on
	// its own ref precisely so trunk history stays code-only, which means the
	// record is NOT an ancestor of the current branch and pushing that branch
	// alone leaves it on the machine. The output said "pushed main to origin"
	// and every commit of the trajectory stayed local — a silent history loss
	// that reads as a completed push, which is worse than not pushing at all
	// (dacli 323).
	branch := gitx.CurrentBranch(w.Root)
	refs := []string{branch}
	if recordBranch != "" && recordBranch != branch {
		refs = append(refs, recordBranch)
	}
	if f.Bool("push") {
		// PushSync retries a non-fast-forward rejection with a fetch+rebase —
		// the record commit just made above can land on top of a fixer PR that
		// merged asynchronously via `gh pr merge --auto` since this checkout
		// last synced, instead of failing outright and stranding the record.
		var pushed []string
		for _, ref := range refs {
			out, err := gitx.PushSync(w.Root, ref)
			if err != nil {
				// Name what DID reach the remote before failing: a partial push
				// is the state the operator has to reason about.
				if len(pushed) > 0 {
					return fmt.Errorf("pushed %s, then push of %s failed: %s",
						strings.Join(pushed, " and "), ref, out)
				}
				return fmt.Errorf("push failed: %s", out)
			}
			pushed = append(pushed, ref)
		}
		fmt.Fprintf(ctx.Stdout, "pushed %s to origin\n", strings.Join(pushed, " and "))
	} else {
		fmt.Fprintf(ctx.Stdout, "not pushed (no --push). To push: git push -u origin %s\n",
			strings.Join(refs, " && git push -u origin "))
	}

	// 5. release — opt-in: cut a tagged GitHub release with generated notes on
	//    the project's linked repo, AFTER the push above put the merged wave on
	//    the remote (the preconditions above guarantee --push ran and it is not
	//    the async --pr path). Shelled to `dacli github release` because ship
	//    imports no sibling slice, targeting --into so the release tags the
	//    branch just pushed. A failure here is surfaced honestly, but the wave
	//    already landed and pushed, so it is a post-ship release failure, not a
	//    half-shipped pipeline.
	if release != "" {
		relArgs := []string{"github", "release", f.Get("project"), release, "--target", into}
		if t := f.Get("release-title"); t != "" {
			relArgs = append(relArgs, "--title", t)
		}
		if n := f.Get("release-notes"); n != "" {
			relArgs = append(relArgs, "--notes", n)
		}
		if _, err := shellDacli(ctx, w, relArgs...); err != nil {
			return fmt.Errorf("wave shipped and pushed, but cutting release %s failed: %w", release, err)
		}
	}
	return nil
}

// landingFlags forwards the resolved landing policy and PR integration flags to the
// `dacli integrate` child, so `dacli ship --pr [--auto] [--no-merge] [--merge]`
// behaves exactly like the same flags on integrate. --auto sets GitHub's native
// auto-merge (merge on CI green, hands-off); absent --pr, it returns nothing and
// the local-merge path is unchanged.
func landingFlags(f *clikit.Flags, policy model.LandingPolicy, explicit bool) []string {
	var out []string
	if policy.Mode == model.LandingPR {
		out = append(out, "--pr")
	}
	if explicit && len(f.All("landing-mode")) > 0 {
		out = append(out, "--landing-mode", string(policy.Mode))
	}
	if len(f.All("landing-base")) > 0 {
		out = append(out, "--landing-base", policy.Base)
	}
	if f.Bool("auto") {
		out = append(out, "--auto")
	}
	if f.Bool("no-merge") {
		out = append(out, "--no-merge")
	}
	if f.Bool("merge") {
		out = append(out, "--merge")
	}
	return out
}

func effectiveShipPolicy(w *workspace.Workspace, f *clikit.Flags) (model.LandingPolicy, bool, error) {
	var configured model.LandingPolicy
	project := f.Get("project")
	if project == "" && f.Get("tasks") != "" {
		for _, ref := range strings.Split(f.Get("tasks"), ",") {
			if strings.TrimSpace(ref) == "" {
				continue
			}
			t, err := store.FindTask(w, strings.TrimSpace(ref))
			if err != nil {
				return model.LandingPolicy{}, false, err
			}
			if project != "" && project != t.Project {
				return model.LandingPolicy{}, false, clikit.Usagef("--tasks spans projects %s and %s; landing policy must resolve from one project", project, t.Project)
			}
			project = t.Project
		}
	}
	if project != "" {
		p, err := store.LoadProject(w, project)
		if err != nil {
			return model.LandingPolicy{}, false, err
		}
		configured = p.Landing
	}
	var override model.LandingOverride
	if len(f.All("landing-mode")) > 0 {
		mode := model.LandingMode(f.Get("landing-mode"))
		override.Mode = &mode
	}
	if f.Bool("pr") {
		if override.Mode != nil && *override.Mode != model.LandingPR {
			return model.LandingPolicy{}, false, clikit.Usagef("--pr conflicts with --landing-mode %s", *override.Mode)
		}
		mode := model.LandingPR
		override.Mode = &mode
	}
	if len(f.All("landing-base")) > 0 && len(f.All("into")) > 0 {
		return model.LandingPolicy{}, false, clikit.Usagef("use either --into or --landing-base, not both")
	}
	if len(f.All("landing-base")) > 0 {
		base := f.Get("landing-base")
		override.Base = &base
	} else if len(f.All("into")) > 0 {
		base := f.Get("into")
		override.Base = &base
	}
	effective, explicit, err := model.ResolveLanding(configured, override)
	if err != nil {
		return model.LandingPolicy{}, explicit, clikit.Usagef("%v", err)
	}
	return effective, explicit, nil
}

// commitRecord stages ONLY the .dacli record and commits it, attributed to the
// acting agent. `git add -- .dacli` is the whole safety property: .dacli/.gitignore
// already excludes runs/build/worktrees, so nothing regenerable or code is
// swept — and we NEVER `git add -A`, the operator footgun that tracked a
// worktree gitlink this session. A belt-and-suspenders check refuses if anything
// outside .dacli somehow landed staged.
func commitRecord(ctx *clikit.Ctx, w *workspace.Workspace, id *agentid.Identity, integrated int, recordBranch string) error {
	domain, tp := w.Attribution()
	name := authorName(id.ID, id.Role)
	email := id.ID + domain

	// --record-branch routes the workspace record to its own ref instead of
	// trunk. Committing it to trunk is what turned 58% of this repo's own
	// history into bookkeeping — including one message repeated verbatim 61
	// times — so a reader looking for engineering history mostly finds the loop
	// narrating itself. The trajectory still ships with the repository; it just
	// stops being interleaved with the code's history (dacli 193).
	if recordBranch != "" {
		msg := fmt.Sprintf("record: workspace after integrating %d task(s)", integrated)
		msg += fmt.Sprintf("\n\n%s-Agent: %s", tp, id.ID)
		if id.Role != "" {
			msg += fmt.Sprintf("\n%s-Role: %s", tp, id.Role)
		}
		sha, err := gitx.CommitPathToBranch(w.Root, recordBranch, workspace.Dir, msg, name, email)
		if err != nil {
			return fmt.Errorf("record commit failed: %w", err)
		}
		if sha == "" {
			fmt.Fprintf(ctx.Stdout, "workspace record: nothing to commit (%s unchanged)\n", workspace.Dir)
			return nil
		}
		fmt.Fprintf(ctx.Stdout, "workspace record committed %s on %s — trunk history stays code-only\n", sha[:min(7, len(sha))], recordBranch)
		return nil
	}

	if out, err := gitx.Run(w.Root, "add", "--", ".dacli"); err != nil {
		return fmt.Errorf("git add .dacli: %s", out)
	}
	staged, _ := gitx.Run(w.Root, "diff", "--cached", "--name-only")
	staged = strings.TrimSpace(staged)
	if staged == "" {
		fmt.Fprintln(ctx.Stdout, "workspace record: nothing to commit (.dacli unchanged)")
		return nil
	}
	for _, p := range strings.Split(staged, "\n") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if p != workspace.Dir && !strings.HasPrefix(p, workspace.Dir+"/") {
			return fmt.Errorf("refusing to commit: %s is staged outside %s — ship records the workspace only, never code/worktrees/build", p, workspace.Dir)
		}
	}

	msg := fmt.Sprintf("ship: record workspace after integrating %d task(s)", integrated)
	trailers := fmt.Sprintf("\n\n%s-Agent: %s", tp, id.ID)
	if id.Role != "" {
		trailers += fmt.Sprintf("\n%s-Role: %s", tp, id.Role)
	}
	out, err := gitx.Run(w.Root,
		"-c", "user.name="+name, "-c", "user.email="+email,
		"commit", "--author", fmt.Sprintf("%s <%s>", name, email), "-m", msg+trailers)
	if err != nil {
		return fmt.Errorf("record commit failed: %s", out)
	}
	sha, _ := gitx.Run(w.Root, "rev-parse", "--short", "HEAD")
	fmt.Fprintf(ctx.Stdout, "workspace record committed %s (staged only %s)\n", sha, workspace.Dir)
	return nil
}

// integrateMode names the merge strategy ship's integrate step will use, for the
// dry-run plan.
func integrateMode(f *clikit.Flags) string {
	if !f.Bool("pr") {
		return "local merge"
	}
	switch {
	case f.Bool("auto"):
		return "open PRs + set GitHub auto-merge on CI green (--auto)"
	case f.Bool("no-merge"):
		return "open PRs, stop for review (--no-merge)"
	default:
		return "PR-first via gh, merge only checks-passing PRs"
	}
}

// printPlan renders every step ship WOULD run, executing nothing (--dry-run).
func printPlan(ctx *clikit.Ctx, w *workspace.Workspace, id *agentid.Identity, f *clikit.Flags, policy model.LandingPolicy, explicit bool, into string) error {
	fmt.Fprintln(ctx.Stdout, "dry-run: dacli ship would run these steps (nothing executed)")
	fmt.Fprintf(ctx.Stdout, "  landing: mode=%s base=%s override=%t; PR action=%s; gates=required checks and reviews\n", policy.Mode, into, explicit, integrateMode(f))

	window := f.Get("tasks")
	transactionWave, landThenAccept, err := prepareLandThenAccept(w, window, policy.Mode == model.LandingPR && window != "" && !f.Bool("no-accept") && !f.Bool("no-integrate"))
	if err != nil {
		return err
	}
	if landThenAccept {
		if f.Get("verify") == "" {
			return clikit.Usagef("the PR land-then-accept transaction requires --verify <command> so acceptance is proved on fresh trunk before the task and issue close")
		}
		if f.Bool("no-merge") {
			return clikit.Usagef("--no-merge cannot complete a land-then-accept transaction; add --no-accept to open the PR without finalizing the task, then rerun ship after review")
		}
	}
	switch {
	case landThenAccept:
		transactionFlags := landingFlags(f, policy, explicit)
		if !f.Bool("merge") {
			transactionFlags = append(transactionFlags, "--merge")
		}
		fmt.Fprintf(ctx.Stdout, "  1. integrate: dacli integrate --tasks %s --into %s --force %s  (tasks remain nonterminal until the checks-gated PR merge and fresh-base inspection succeed)\n",
			strings.Join(doneRefs(transactionWave), ","), into, strings.Join(transactionFlags, " "))
	case f.Bool("no-accept"):
		fmt.Fprintln(ctx.Stdout, "  1. accept:    (skipped: --no-accept)")
	default:
		line := "dacli accept --all --force --defer-landing"
		if v := f.Get("verify"); v != "" {
			line += fmt.Sprintf(" --verify %q", v)
		}
		fmt.Fprintf(ctx.Stdout, "  1. accept:    %s   (landing verdict recorded after integrate, once it has actually run)\n", line)
	}

	switch {
	case landThenAccept:
		verify := ""
		if v := f.Get("verify"); v != "" {
			verify = fmt.Sprintf(" --verify %q", v)
		}
		fmt.Fprintf(ctx.Stdout, "  2. accept:    fresh origin/%s must contain each reviewed head; then dacli accept <%s> --force --into %s%s, followed by scoped GitHub issue closure\n",
			into, strings.Join(doneRefs(transactionWave), ","), into, verify)
	case f.Bool("no-integrate"):
		fmt.Fprintln(ctx.Stdout, "  2. integrate: (skipped: --no-integrate)")
	case window != "":
		// Explicit window: the operator named the tasks, so the plan can resolve
		// and show exactly what would integrate.
		wave, err := previewExplicitWave(w, id, f, window)
		if err != nil {
			return err
		}
		fmt.Fprintf(ctx.Stdout, "  2. integrate: dacli integrate --tasks %s --into %s %s  (explicit --tasks window: %d task(s): %s) [%s]\n",
			strings.Join(doneRefs(wave), ","), into, strings.Join(landingFlags(f, policy, explicit), " "), len(wave), doneLabels(wave), integrateMode(f))
	default:
		// No window, and accept has not run — so the wave (the tasks accept will
		// close THIS run) is not yet known. Say so honestly, and report how many
		// already-done tasks are DELIBERATELY skipped, never re-integrated (261).
		done, _ := store.ListTasks(w, f.Get("project"), model.StatusDone)
		fmt.Fprintf(ctx.Stdout, "  2. integrate: dacli integrate --tasks <the wave accept closes this run> --into %s %s  [%s; %d already-done task(s) are not re-integrated]\n",
			into, strings.Join(landingFlags(f, policy, explicit), " "), integrateMode(f), len(done))
	}

	// The preview must describe the branch the record ACTUALLY lands on. A plan
	// that says "git add .dacli" while the run commits to a separate ref is a
	// preview of a different command, and --dry-run exists to be trusted.
	recordBranch := f.Get("record-branch")
	if recordBranch == "" {
		recordBranch = w.RecordBranch
	}
	if recordBranch != "" {
		fmt.Fprintf(ctx.Stdout, "  3. record:    commit %s onto %s   (its own ref — trunk history stays code-only)\n", workspace.Dir, recordBranch)
	} else {
		fmt.Fprintf(ctx.Stdout, "  3. record:    git add %s && git commit   (stages ONLY %s — never git add -A)\n", workspace.Dir, workspace.Dir)
	}

	branch := gitx.CurrentBranch(w.Root)
	refs := []string{branch}
	if recordBranch != "" && recordBranch != branch {
		refs = append(refs, recordBranch)
	}
	if f.Bool("push") {
		fmt.Fprintf(ctx.Stdout, "  4. push:      git push -u origin %s\n",
			strings.Join(refs, " && git push -u origin "))
	} else {
		fmt.Fprintln(ctx.Stdout, "  4. push:      (skipped: pass --push to push, else run git push yourself)")
	}

	if release := f.Get("release"); release != "" {
		notes := "generated notes"
		if f.Get("release-notes") != "" {
			notes = "explicit --notes"
		}
		fmt.Fprintf(ctx.Stdout, "  5. release:   dacli github release %s %s --target %s   (%s)\n", f.Get("project"), release, into, notes)
	} else {
		fmt.Fprintln(ctx.Stdout, "  5. release:   (skipped: pass --release <tag> to cut a tagged release with notes)")
	}
	return nil
}

// previewExplicitWave projects the accept step that precedes ship's explicit
// window resolution. The real pipeline accepts every pending proposal before
// calling explicitWave, so validating the pre-transition status here made a
// dry-run refuse commands that ship itself could run (issue #651).
func previewExplicitWave(w *workspace.Workspace, id *agentid.Identity, f *clikit.Flags, window string) ([]*store.Task, error) {
	var wave []*store.Task
	for _, ref := range strings.Split(window, ",") {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		t, err := store.FindTask(w, ref)
		if err != nil {
			return nil, err
		}
		if t.Status == model.StatusDone {
			wave = append(wave, t)
			continue
		}
		if f.Bool("no-accept") {
			return nil, clikit.Usagef("--tasks: %03d-%s is %s, not done — ship integrates done tasks' branches", t.Seq, t.Slug, t.Status)
		}
		if err := previewAcceptsTask(w, id, f, t); err != nil {
			return nil, err
		}
		wave = append(wave, t)
	}
	return wave, nil
}

// previewAcceptsTask checks the static conditions that accept --all --force
// applies to a pending proposal. It deliberately does not run --verify: a
// preview must not execute commands, while a supplied verifier is the same
// runtime gate the real accept step will execute.
func previewAcceptsTask(w *workspace.Workspace, id *agentid.Identity, f *clikit.Flags, t *store.Task) error {
	events, err := eventlog.List(w, eventlog.Query{About: t.ID, Pending: true})
	if err != nil {
		return err
	}
	proposed := false
	for _, e := range events {
		if e.Kind == model.EventComment && strings.HasPrefix(strings.TrimSpace(e.Body), eventlog.ProposePrefix) ||
			e.Kind == model.EventProposeStatus && strings.TrimSpace(strings.TrimPrefix(e.Body, "propose:")) == string(model.StatusDone) {
			proposed = true
			break
		}
	}
	if !proposed {
		return clikit.Usagef("--tasks: %03d-%s is %s, not done — ship integrates done tasks' branches", t.Seq, t.Slug, t.Status)
	}
	if !id.CanMutate(t.Owner()) && id.ID != agentid.RootID {
		return clikit.Refusedf("skipped %03d-%s: owned by %s", t.Seq, t.Slug, clikit.OrDash(t.Owner()))
	}
	if !store.HasAcceptanceCriteria(t) {
		return clikit.Refusedf("skipped %03d-%s: no acceptance criteria — nothing to verify (pass --allow-unverified to close it explicitly UNVERIFIED)", t.Seq, t.Slug)
	}
	if f.Get("verify") == "" {
		for i := range t.Acceptance() {
			if store.AcceptanceRequiresCommandVerification(t, i+1) {
				return clikit.Refusedf("skipped %03d-%s: command acceptance criterion requires --verify evidence", t.Seq, t.Slug)
			}
		}
	}
	return nil
}

// blockedAmong returns the labels of tasks from the given set that are now in
// blocked status — the signal that integrate hit a merge conflict on one.
func blockedAmong(w *workspace.Workspace, set []*store.Task) []string {
	blocked, err := store.ListTasks(w, "", model.StatusBlocked)
	if err != nil {
		return nil
	}
	inSet := map[string]bool{}
	for _, t := range set {
		inSet[t.ID] = true
	}
	var out []string
	for _, t := range blocked {
		if inSet[t.ID] {
			out = append(out, fmt.Sprintf("%03d-%s", t.Seq, t.Slug))
		}
	}
	return out
}

// taskKey is a stable per-task identity for wave membership: the globally-unique
// ULID when a task has one, else the project-qualified seq-slug (a pre-ULID
// task). Keying on a bare seq would collide across projects, so two projects'
// task 5 would wrongly count as the same wave member.
func taskKey(t *store.Task) string {
	if t.ID != "" {
		return t.ID
	}
	return fmt.Sprintf("%s/%03d-%s", t.Project, t.Seq, t.Slug)
}

// doneKeys is the set of task keys already done — captured BEFORE accept runs so
// the wave (done-after-accept minus done-before) excludes every task a prior run
// closed. Without this snapshot ship integrates the full done history each run.
func doneKeys(w *workspace.Workspace, project string) (map[string]bool, error) {
	done, err := store.ListTasks(w, project, model.StatusDone)
	if err != nil {
		return nil, err
	}
	keys := make(map[string]bool, len(done))
	for _, t := range done {
		keys[taskKey(t)] = true
	}
	return keys, nil
}

// shipWave resolves the tasks THIS ship run integrates. With an explicit --tasks
// window it is exactly those refs (each must be done — a not-done ref has no
// landable branch state). Otherwise it is the wave accept just closed: every
// done task whose key was NOT in preDone, the snapshot taken before accept ran.
func shipWave(w *workspace.Workspace, project string, preDone map[string]bool, window string) ([]*store.Task, error) {
	if window != "" {
		return explicitWave(w, window)
	}
	done, err := store.ListTasks(w, project, model.StatusDone)
	if err != nil {
		return nil, err
	}
	wave := make([]*store.Task, 0, len(done))
	for _, t := range done {
		if !preDone[taskKey(t)] {
			wave = append(wave, t)
		}
	}
	return wave, nil
}

// explicitWave resolves a comma-separated --tasks window to its tasks. A ref
// that does not resolve propagates its original (not-found) error so the exit
// code stays honest; a ref that resolves but is not done is a usage error —
// ship integrates a done task's branch, and a not-done ref has none to land.
func explicitWave(w *workspace.Workspace, window string) ([]*store.Task, error) {
	var wave []*store.Task
	for _, ref := range strings.Split(window, ",") {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		t, err := store.FindTask(w, ref)
		if err != nil {
			return nil, err
		}
		if t.Status != model.StatusDone {
			return nil, clikit.Usagef("--tasks: %03d-%s is %s, not done — ship integrates done tasks' branches", t.Seq, t.Slug, t.Status)
		}
		wave = append(wave, t)
	}
	return wave, nil
}

// selectedWave resolves an explicit transaction window without pretending its
// tasks are already accepted. integrate receives --force only inside the
// enclosing ship transaction; direct integrate retains its done-only policy.
func selectedWave(w *workspace.Workspace, window string) ([]*store.Task, error) {
	var wave []*store.Task
	for _, ref := range strings.Split(window, ",") {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		t, err := store.FindTask(w, ref)
		if err != nil {
			return nil, err
		}
		wave = append(wave, t)
	}
	if len(wave) == 0 {
		return nil, clikit.Usagef("--tasks was empty; give a comma-separated list of task refs")
	}
	return wave, nil
}

// prepareLandThenAccept validates the irreversible half of the PR transaction
// before integrate can merge anything. A selected open task must already carry
// a complete owner-reviewed acceptance contract; post-landing --verify proves
// that accepted work on fresh trunk, it does not invent requirements after the
// merge. Done-only windows retain the legacy path, and mixed windows are split
// explicitly instead of re-accepting history as a side effect.
func prepareLandThenAccept(w *workspace.Workspace, window string, candidate bool) ([]*store.Task, bool, error) {
	if !candidate {
		return nil, false, nil
	}
	wave, err := selectedWave(w, window)
	if err != nil {
		return nil, false, err
	}
	done := 0
	for _, t := range wave {
		if t.Status == model.StatusDone {
			done++
		}
	}
	if done == len(wave) {
		return wave, false, nil
	}
	if done > 0 {
		return nil, false, clikit.Usagef("--tasks mixes done and nonterminal work; ship them in separate commands so the PR transaction cannot re-accept historical tasks")
	}
	for _, t := range wave {
		if t.Status != model.StatusOpen && t.Status != model.StatusActive {
			return nil, false, clikit.Refusedf("%03d-%s is %s; only open or active tasks can enter the PR land-then-accept transaction", t.Seq, t.Slug, t.Status)
		}
		if !store.HasAcceptanceCriteria(t) {
			return nil, false, clikit.Refusedf("%03d-%s has no acceptance criteria; define or migrate a checkable contract before merging its PR", t.Seq, t.Slug)
		}
		var unmet []int
		for i, criterion := range t.Acceptance() {
			if !criterion.Done {
				unmet = append(unmet, i+1)
			}
		}
		if len(unmet) > 0 {
			return nil, false, clikit.Refusedf("%03d-%s has unchecked acceptance criteria %v; the owner must verify and check them before the PR can merge", t.Seq, t.Slug, unmet)
		}
	}
	return wave, true, nil
}

// requireFreshLanding fetches the configured base after GitHub reports the PR
// merged and proves every reviewed branch ref is present there. This check runs
// before verification and acceptance, so a stale local checkout or an async
// merge report cannot finalize the task (issue #841).
func requireFreshLanding(w *workspace.Workspace, into string, wave []*store.Task, refs map[string]waveRef) error {
	if _, err := gitx.RunNetwork(w.Root, "fetch", "-q", "origin", "--", into); err != nil {
		return fmt.Errorf("ship merged the PR but could not fetch fresh origin/%s; tasks remain nonterminal until landing can be inspected: %w", into, err)
	}
	for _, t := range wave {
		if observation, present := store.ReadTaskLanding(context.Background(), w, t, into); present {
			if observation.State != store.LandingLanded {
				return clikit.Refusedf("ship cannot certify %03d-%s: selected native landing observation is not validated; reconcile it before accepting", t.Seq, t.Slug)
			}
			continue
		}
		shas := refs[taskRef(t)].shas
		if len(shas) == 0 {
			return clikit.Refusedf("ship cannot certify %03d-%s: no reviewed branch head was captured before merge; task remains nonterminal", t.Seq, t.Slug)
		}
		if store.LandingOfRefs(w, shas, into) != store.LandingLanded {
			return clikit.Refusedf("ship cannot certify %03d-%s: the exact reviewed head is not present on fresh origin/%s; task remains nonterminal with evidence preserved", t.Seq, t.Slug, into)
		}
	}
	return nil
}

// freshLandingArtifact resolves the exact immutable remote head fetched by
// requireFreshLanding. Post-landing acceptance verifies the local checkout and
// compares its evidence to both values, so a stale main checkout, a later push,
// or verification of the parent tree cannot certify the reviewed landing.
func freshLandingArtifact(w *workspace.Workspace, into string) (commitSHA, treeSHA string, err error) {
	ref := "refs/remotes/origin/" + into
	commitSHA, err = gitx.Run(w.Root, "rev-parse", "--verify", ref)
	if err != nil {
		return "", "", clikit.Refusedf("ship cannot certify the reviewed landing head: fresh origin/%s has no resolvable commit", into)
	}
	commitSHA = strings.TrimSpace(commitSHA)
	treeSHA, err = gitx.Run(w.Root, "rev-parse", "--verify", commitSHA+"^{tree}")
	if err != nil {
		return "", "", clikit.Refusedf("ship cannot certify the reviewed landing head %s: its immutable tree cannot be resolved", commitSHA)
	}
	return commitSHA, strings.TrimSpace(treeSHA), nil
}

// taskRef renders a task as a ref store.FindTask resolves — the task's ULID
// id, which is GLOBALLY unique (a bare seq is only unique within a project,
// so across a multi-project done list two projects' task 5 both resolve as
// "5" and lookups become ambiguous). A task predating ULID ids falls back to
// the qualified %03d-slug form — still not a bare seq.
func taskRef(t *store.Task) string {
	if t.ID != "" {
		return t.ID
	}
	return fmt.Sprintf("%03d-%s", t.Seq, t.Slug)
}

// doneRefs renders each task as a ref integrate resolves via store.FindTask.
func doneRefs(tasks []*store.Task) []string {
	refs := make([]string, 0, len(tasks))
	for _, t := range tasks {
		refs = append(refs, taskRef(t))
	}
	return refs
}

// waveRef is a wave task's branch ref, captured BEFORE integrate runs.
type waveRef struct {
	branch string
	// EVERY commit the branch names, not one. origin/<branch> and
	// refs/heads/<branch> disagree whenever a branch was pushed and then
	// advanced locally, and integrate merges different ones on the --pr and
	// local-merge paths — so a snapshot of a single ref asks the landing
	// question about a commit that may not be the deliverable.
	shas []string
}

// captureWaveRefs snapshots each wave task's branch commit before integrate
// gets a chance to run — see recordWaveLanding for why the snapshot has to
// happen this early.
func captureWaveRefs(w *workspace.Workspace, wave []*store.Task) map[string]waveRef {
	refs := make(map[string]waveRef, len(wave))
	for _, t := range wave {
		branch, shas := store.ResolveBranchRefs(w, t)
		refs[taskRef(t)] = waveRef{branch: branch, shas: shas}
	}
	return refs
}

// recordWaveLanding writes the truthful landing verdict — merged, or still
// not in trunk — for every task accept closed THIS run, now that integrate
// has had its one chance to land them.
//
// accept's own landing check necessarily runs BEFORE integrate here
// (integrate refuses a non-done task), so checking at accept time would
// always see "not yet landed" and durably record that on every task ship
// goes on to land seconds later. accept is told to skip its check
// (--defer-landing); this is the one place the real verdict gets stamped
// (dacli 329).
//
// The landing check itself uses the COMMIT captured in refs, not a live
// branch-name lookup: a clean local merge (vcs.mergeTask) deletes the branch
// once it lands, so checking the branch name now would misread a landed task
// as branchless. Each task is also RE-READ from disk immediately before
// writing: integrate may already have moved a blocked task to a different
// status/path (mergeTask's conflict handling runs in a separate process), so
// the copy captured before integrate ran can be stale. A task that no longer
// resolves is skipped — nothing to correct.
func recordWaveLanding(w *workspace.Workspace, trunk string, wave []*store.Task, refs map[string]waveRef) {
	for _, t := range wave {
		ref := taskRef(t)
		fresh, err := store.FindTask(w, ref)
		if err != nil {
			continue
		}
		r := refs[ref]
		landing := store.LandingNoBranch
		label := r.branch
		if observation, present := store.ReadTaskLanding(context.Background(), w, fresh, trunk); present {
			landing, label = observation.State, "native landing observation "+observation.ReceiptID
		} else if len(r.shas) > 0 {
			landing = store.LandingOfRefs(w, r.shas, trunk)
		}
		_ = store.WithTask(w, fresh, func(current *store.Task) error {
			store.AppendLog(current, store.LandingEvidence(landing, label, trunk))
			return store.SaveTask(current)
		})
	}
}

func doneLabels(tasks []*store.Task) string {
	labels := make([]string, 0, len(tasks))
	for _, t := range tasks {
		labels = append(labels, fmt.Sprintf("%03d-%s", t.Seq, t.Slug))
	}
	return strings.Join(labels, ", ")
}

// authorName encodes the role into the git identity so plain `git blame` and
// `git log` stay legible — a local copy of the vcs slice's rule (slices cannot
// import each other, and the record commit is attributed the same way).
func authorName(id, role string) string {
	if role != "" && role != "root" {
		return fmt.Sprintf("%s (%s)", id, role)
	}
	return id
}
