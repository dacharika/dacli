// Package orchestration is the autonomous-team slice: it runs the whole
// software process as a governed, bounded loop. A cycle walks the same phases
// a real team walks each sprint — review, plan, implement, test, land, retro —
// and then goes around again, without a human in the loop.
//
// It owns NO agent-spawning or integration logic of its own: every phase is a
// real `dacli` subcommand invocation (spawn, wait, ship, retro), sequenced by
// this driver and gated by a pure Governor. That keeps the slice inside the
// feature-sliced boundary (it imports no sibling feature) and makes every phase
// a first-class, logged run rather than hidden in-process magic.
package orchestration

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mlnomadpy/dacli/internal/agentid"
	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/commandresult"
	"github.com/mlnomadpy/dacli/internal/gates"
	"github.com/mlnomadpy/dacli/internal/gitx"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/prompts"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/team"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

var Commands = []clikit.Command{
	{Path: "start", Usage: startUsage, Brief: "Resolve, persist, preview, or execute a bounded operating profile (inspect, task, wave, loop, service)", JSON: true, Mutates: true, Run: cmdStart},
	{Path: "loop", Usage: loopUsage, Brief: "Run the whole team process as a governed perpetual loop: review→plan→implement→test→land→retro, then repeat (--dry-run to preview, --max-cycles to bound). Token vocabulary: --max-tokens caps ONE cycle's spend, --window-tokens caps a rolling window, --token-window sets that window's duration (alias: --budget-window); --brief-tokens is the brief's SIZE, not spend", Mutates: true, Run: cmdLoop},
	{Path: "loop status", Brief: "Show the running/last loop checkpoint, typed halt diagnosis, exact affected refs, and preserved governor counters", Usage: "dacli loop status --project <slug> [--json]", JSON: true, Run: cmdLoopStatus},
	{Path: "review record", Brief: "Append one identity-bound independent-review-result/v1 verdict without granting the reviewer write authority", Usage: "dacli review record --task <ref> --result <JSON>", Run: cmdReviewRecord},
	{Path: "review projection", Brief: "Render the public-safe GitHub verdict and line-comment projection from a recorded structured review", Usage: "dacli review projection --task <ref> [--json]", JSON: true, Run: cmdReviewProjection},
}

// runner executes a dacli subcommand. Real runs shell out to this very binary
// so each phase is a logged, attributable run; tests inject a fake.
type runner interface {
	run(label string, args ...string) (string, error)
}

type resultRunner interface {
	runResult(label string, result any, args ...string) (string, error)
}

// execRunner invokes os.Executable() with the given args, inheriting the
// environment (so DACLI_AGENT identity flows into children).
type execRunner struct {
	cwd string
}

func (execRunner) requiresLaunchContract() bool { return true }

func (r execRunner) run(label string, args ...string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		exe = "dacli"
	}
	cmd := exec.Command(exe, args...)
	cmd.Dir = r.cwd
	out, err := commandresult.Run(cmd, commandresult.RunOptions{
		Operation:     "dacli " + label,
		WorkspaceRoot: r.cwd,
	})
	return string(out), err
}

func (r execRunner) runResult(label string, result any, args ...string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		exe = "dacli"
	}
	cmd := exec.Command(exe, args...)
	cmd.Dir = r.cwd
	out, err := commandresult.Capture(cmd, result)
	return string(out), err
}

func (r execRunner) runPreflight(label string, args ...string) (string, error) {
	return r.run(label, args...)
}

// loopStack reads the stack `dacli new` recorded on the project the loop is
// about to drive (dacli 192). A missing or stackless project yields the zero
// Stack, which pins every role default to the constant it was before — the
// backwards-compatibility contract for every workspace that predates stacks.
func loopStack(w *workspace.Workspace, project string) prompts.Stack {
	p, err := store.LoadProject(w, project)
	if err != nil {
		return prompts.Stack{}
	}
	return prompts.StackFromProject(p.Doc)
}

// dryRunner logs the intended command and does nothing.
type dryRunner struct{ log func(string) }

func (r dryRunner) run(label string, args ...string) (string, error) {
	r.log(fmt.Sprintf("  would run: dacli %s", strings.Join(args, " ")))
	return "", nil
}

func (r dryRunner) runPreflight(label string, args ...string) (string, error) {
	return r.run(label, args...)
}

// loopCfg is the resolved policy for one `dacli loop` invocation.
type loopCfg struct {
	project  string
	implRole string
	// implRoleExplicit distinguishes an operator's --impl-role decision from
	// the project-stack fallback. Only the latter may be replaced by automatic
	// cheapest-capable routing (task 373).
	implRoleExplicit    bool
	reviewRoleExplicit  bool
	reviewRole          string
	harnessMode         string
	allowedHarnesses    []string
	width               int   // implementers spawned per cycle
	perCycleTok         int64 // --max-tokens passed to each spawn (0 = unset)
	allowUserConfig     bool  // explicit external-context exception, recorded by execution
	allowAdvisoryTokens bool  // explicit policy: account for tokens even when the runtime cannot enforce the ceiling
	workerTimeout       int   // explicit --worker-timeout seconds (0 = derive from task estimate)
	dryRun              bool
	yolo                bool   // no between-cycle checkpoint pause
	pr                  bool   // land through PRs; autoMerge separately controls unattended landing
	autoMerge           bool   // queue GitHub auto-merge; false leaves the verified PR for explicit landing
	into                string // --into: the branch ship/integrate land onto ("" = resolve)
	landing             model.LandingPolicy
	landingExplicit     bool
	capacityOverride    *capacityOverride
}

const (
	minimumWorkerTimeout    = 5 * time.Minute
	workerTimeoutPerTePoint = 5 * time.Minute
)

// workerTimeout returns the wall-clock allowance for one loop worker. An
// explicit loop flag is a policy override. Otherwise each expected estimate
// point buys five minutes, with the historical five-minute allowance retained
// as the floor for unestimated and sub-point tasks.
func (d *driver) workerTimeout(t *store.Task) int {
	if d.cfg.workerTimeout > 0 {
		return d.cfg.workerTimeout
	}
	timeout := minimumWorkerTimeout
	if t != nil {
		if tp, ok := t.Estimate(); ok {
			timeout = time.Duration(math.Ceil(tp.Expected()*workerTimeoutPerTePoint.Seconds())) * time.Second
			if timeout < minimumWorkerTimeout {
				timeout = minimumWorkerTimeout
			}
		}
	}
	return int(timeout / time.Second)
}

// loopUsage is the single source of truth for loop's flag synopsis: `--help`
// prints it and the missing-project usage error quotes it, so the two cannot
// drift. Every flag that TAKES A VALUE shows its value here — that is the
// whole point. `--no-progress-halt` requires an integer, appeared nowhere in
// help output, and reading it as a boolean was the only conclusion a user
// could reach (issue #421).
const loopUsage = "dacli loop --project <slug> [--width N] [--impl-role R|--impl-role-fallback R] [--review-role R] " +
	"[--harness FAMILY]... [--hybrid] " +
	"[--capacity-override-reason TEXT --capacity-override-expires RFC3339] " +
	"[--max-cycles N] [--window-tokens N --token-window DUR] [--max-tokens N [--allow-advisory-tokens]] [--allow-user-config] [--worker-timeout SEC] [--brief-tokens N] " +
	"[--idle DUR] [--halt-after-idle N] [--into BRANCH] [--stop-file PATH] [--no-pr] [--auto-merge|--no-auto-merge] [--yolo] [--dry-run] [--advise]"

func cmdLoop(ctx *clikit.Ctx, args []string) error {
	w, id, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	f, _ := clikit.ParseFlags(args)
	if err := f.Reject("project", "impl-role", "impl-role-fallback", "review-role", "harness", "hybrid", "width", "max-tokens",
		"worker-timeout", "allow-advisory-tokens", "allow-user-config",
		"capacity-override-reason", "capacity-override-expires",
		"dry-run", "yolo", "pr", "no-pr", "auto-merge", "no-auto-merge", "advise", "budget-window", "window-tokens",
		"idle", "max-cycles", "no-progress-halt", "halt-after-idle", "into", "stop-file",
		"token-window", "brief-tokens"); err != nil {
		return err
	}

	project := f.Get("project")
	if project == "" {
		// Default to the sole project if there is exactly one.
		ps, _ := store.ListProjects(w)
		if len(ps) == 1 {
			project = ps[0].Slug
		} else {
			return clikit.Usagef("usage: %s", loopUsage)
		}
	}

	// Role defaults follow the project's recorded stack (dacli 192). They used
	// to be the constants "fixer" and "go-auditor", so a Python project was
	// audited by a role named for a language it does not use — observed live.
	// An explicit --impl-role/--review-role still wins over everything.
	stack := loopStack(w, project)
	inRoster := func(name string) bool { _, ok := store.LoadRole(w, name); return ok }
	if f.Get("impl-role") != "" && f.Get("impl-role-fallback") != "" {
		return clikit.Usagef("use either --impl-role for a fixed override or --impl-role-fallback for automatic cost routing, not both")
	}
	implementationRole := f.Get("impl-role")
	if implementationRole == "" {
		implementationRole = f.Get("impl-role-fallback")
	}

	// Every numeric/duration knob is read through the refusing clikit readers.
	// The hand-rolled helpers these replaced returned the DEFAULT on a parse
	// error, which meant `--window-tokens garbage` became 0 — read downstream as
	// "unlimited" — so an operator who asked for a cap ran with none, and
	// `--window-tokens 50k` silently became a 50-token ceiling.
	width, err := f.Int("width", 2)
	if err != nil {
		return err
	}
	perCycleTok, err := f.Int64("max-tokens", 0)
	if err != nil {
		return err
	}
	workerTimeout, err := f.Int("worker-timeout", 0)
	if err != nil {
		return err
	}
	if workerTimeout < 0 {
		return clikit.Usagef("--worker-timeout must be zero or a positive number of seconds (got %d)", workerTimeout)
	}
	// token-window is canonical; budget-window is the accepted old spelling.
	// They name a DURATION, which is why "budget" was a bad word for it: the
	// same root meant the brief's size elsewhere (task 292).
	windowFlag := "token-window"
	if n, ok := f.Alias("token-window", "budget-window"); ok {
		windowFlag = n
	}
	windowDur, err := f.Duration(windowFlag, 24*time.Hour)
	if err != nil {
		return err
	}
	windowTokens, err := f.Int64("window-tokens", 0)
	if err != nil {
		return err
	}
	idle, err := f.Duration("idle", 30*time.Minute)
	if err != nil {
		return err
	}
	maxCycles, err := f.Int("max-cycles", 0)
	if err != nil {
		return err
	}
	// --halt-after-idle is the canonical spelling. The original name asks the
	// reader to hold a double negative — "--no-progress-halt 2" reads as "do
	// not halt", so supplying a number feels wrong even though it is required
	// (issue #421). The old name keeps working: scripts already pass it.
	noProgressHalt, err := f.IntAliased(3, "halt-after-idle", "no-progress-halt")
	if err != nil {
		return err
	}

	cfg := loopCfg{
		project:             project,
		implRole:            orDefault(implementationRole, prompts.RoleFor(stack, "fixer", "fixer", inRoster)),
		implRoleExplicit:    f.Get("impl-role") != "",
		reviewRole:          orDefault(f.Get("review-role"), stackReviewRole(w, stack, prompts.RoleFor(stack, "auditor", "go-auditor", inRoster))),
		reviewRoleExplicit:  f.Get("review-role") != "",
		width:               width,
		perCycleTok:         perCycleTok,
		allowUserConfig:     f.Bool("allow-user-config"),
		allowAdvisoryTokens: f.Bool("allow-advisory-tokens"),
		workerTimeout:       workerTimeout,
		dryRun:              f.Bool("dry-run"),
		yolo:                f.Bool("yolo"),
	}
	overrideReason := strings.TrimSpace(f.Get("capacity-override-reason"))
	overrideExpiry := strings.TrimSpace(f.Get("capacity-override-expires"))
	if overrideReason != "" || overrideExpiry != "" {
		if overrideReason == "" || overrideExpiry == "" {
			return clikit.Usagef("capacity override requires both --capacity-override-reason and --capacity-override-expires")
		}
		if id.ID != agentid.RootID {
			return clikit.Refusedf("only the workspace owner may override an under-capacity role (actor %s)", id.ID)
		}
		expires, parseErr := time.Parse(time.RFC3339, overrideExpiry)
		if parseErr != nil || !expires.After(time.Now().UTC()) {
			return clikit.Usagef("--capacity-override-expires must be a future RFC3339 timestamp")
		}
		cfg.capacityOverride = &capacityOverride{Actor: id.ID, Reason: overrideReason, ExpiresAt: expires.UTC()}
	}
	if err := resolveLoopHarnessPolicy(w, &cfg, f.All("harness"), f.Bool("hybrid")); err != nil {
		return err
	}
	journal, journalWarn := readCycleJournal(w, project)
	phases, phaseErr := readPhaseJournal(w, project)
	if phaseErr != nil {
		return clikit.Refusedf("%v — refusing to resume without exact operation checkpoints; inspect %s", phaseErr, phaseJournalFile(w, project))
	}
	landing, landingExplicit, err := resolveLoopLanding(w, project, f, journal)
	if err != nil {
		return err
	}
	cfg.landing, cfg.landingExplicit = landing, landingExplicit
	cfg.pr = landing.Mode == model.LandingPR
	cfg.into = landing.Base
	if f.Bool("auto-merge") && f.Bool("no-auto-merge") {
		return clikit.Usagef("use either --auto-merge or --no-auto-merge, not both")
	}
	// Preserve legacy direct-loop behavior when no project operating profile
	// exists. Once start has persisted a profile, its safer auto-merge decision
	// is the base and only an explicit loop flag may override it.
	cfg.autoMerge = true
	if profile, profileErr := loadProfile(w, project); profileErr == nil {
		cfg.autoMerge = profile.Landing.AutoMerge
	}
	if f.Bool("auto-merge") || f.Bool("no-auto-merge") {
		cfg.autoMerge = f.Bool("auto-merge")
	}

	// Validate --into UP FRONT. The branch is threaded into every ship and
	// integrate call, so a typo would otherwise surface deep inside a cycle
	// that has already spawned agents and spent tokens — and `ship --into` on a
	// branch that does not exist fails in a way that reads as a git problem
	// rather than a flag problem.
	if cfg.into != "" {
		if _, err := gitx.Run(w.Root, "rev-parse", "--verify", "--quiet", "refs/heads/"+cfg.into); err != nil {
			return clikit.Usagef("--into %s: no such local branch — create it first (git branch %s), or drop --into to land on the resolved trunk", cfg.into, cfg.into)
		}
	}

	// An explicit --pr with no remote cannot work: every landing check would
	// dead-end on gh, and nothing would ever close. Refuse rather than run a
	// loop that can only thrash (issue #382).
	if cfg.pr && !hasOriginRemote(w.Root) {
		return clikit.Refusedf("effective landing policy is pr, but this repo has no `origin` remote; add one and authenticate `gh`, or explicitly override with --no-pr (the task remains open)")
	}

	// --advise (mirrors `spawn --advise`): report the calibrated per-cycle
	// token cost band for this width/role config and return — no agents
	// spawned, no grant needed, the unbounded-loop stop-condition refusal
	// below never even runs.
	if f.Bool("advise") {
		printLoopAdvisory(ctx, w, cfg)
		return nil
	}

	if cfg.allowAdvisoryTokens && cfg.perCycleTok <= 0 {
		return clikit.Usagef("--allow-advisory-tokens requires a positive --max-tokens ceiling")
	}
	if cfg.perCycleTok > 0 {
		if cfg.allowAdvisoryTokens {
			fmt.Fprintf(ctx.Stderr, "warning: loop token ceilings are ADVISORY ONLY where a selected runtime declares no token-limit flag (%s); rolling-window, timeout, cycle, and idle guards remain enforced\n", advisoryTokenRoles(w, cfg))
		} else if err := validateHardTokenRoles(w, cfg); err != nil {
			return err
		}
	}

	gov := &Governor{
		WindowDur:      windowDur,
		WindowTokens:   windowTokens,
		Idle:           idle,
		MaxCycles:      maxCycles,
		NoProgressHalt: noProgressHalt,
		StopFile:       resolveStopFile(w, f.Get("stop-file")),
	}
	// A token ceiling with a zero-length window is not a ceiling: the window
	// rolls on every check and the spend resets before it is ever compared, so
	// the budget silently disables itself — and `--budget-window 0` parses
	// cleanly, so this is a plain flag combination, not an exotic one. Refuse
	// it rather than run an operator who asked to be capped uncapped (dacli
	// 218). With no --window-tokens the window is meaningless and this is fine.
	if gov.WindowTokens > 0 && gov.WindowDur <= 0 {
		return clikit.Usagef("--window-tokens %d needs a positive --budget-window (got %q): a zero-length window resets the spend before it is ever compared, which silently disables the budget",
			gov.WindowTokens, f.Get("budget-window"))
	}

	// A perpetual loop runs as a fresh process every checkpoint (the default,
	// non-yolo path returns after each cycle for the operator to re-run) — so
	// without this reload every restart would silently forget tokens already
	// spent this window and cycles/thrash-streak already accumulated, and a
	// --window-tokens or --no-progress-halt guard would never actually trip.
	// A snapshot that exists but does not parse is a REFUSAL, never a fresh
	// start: resuming from zeroes is exactly the state that clears the token
	// ceiling and the thrash streak, and the file sits in a repo the loop's own
	// children can write (dacli 207). The operator inspects and removes it.
	var restored governorState
	var restoredOK bool
	recovery := ""
	switch st, err := readGovernorState(w, project); {
	case err == nil:
		effectiveWindowTokens := windowTokens
		if effectiveWindowTokens == 0 {
			effectiveWindowTokens = journal.WindowTokens
		}
		if journal.WindowTokens == 0 && effectiveWindowTokens > 0 && st.WindowStart.IsZero() && st.WindowSpent > 0 {
			// Task 513: uncapped loops still report spend, but those tokens have
			// no rolling-window provenance. When a ceiling is introduced for the
			// first time, start accounting from this invocation. The journal is
			// the durable proof that no prior ceiling existed; an active or
			// previously capped window never receives this reset.
			recovery = fmt.Sprintf("new token window ignores %d historical uncapped tokens", st.WindowSpent)
			st.WindowSpent = 0
		}
		gov.Restore(st)
		// Governor snapshots written before task 379 do not carry a trunk
		// marker. The companion loop snapshot does, so use it to migrate a
		// persisted thrash halt instead of forcing an operator to discard the
		// otherwise-valid cycle and token-window accounting.
		if !st.TrunkMarkerKnown {
			if prior, priorErr := readLoopState(w, project); priorErr == nil && prior.Status == Halt.String() && strings.Contains(prior.Reason, "thrash guard tripped") {
				st.TrunkMarker = prior.TrunkMarker
				st.TrunkMarkerKnown = true
			}
		}
		restored, restoredOK = st, true
	case errors.Is(err, errCorruptState):
		return clikit.Refusedf("%v — refusing to resume with reset guards; inspect it, then delete %s to start a fresh window", err, governorStateFile(w, project))
	case errors.Is(err, os.ErrNotExist):
		if prior, priorErr := readLoopState(w, project); priorErr == nil && prior.Status == Halt.String() && strings.Contains(prior.Reason, "thrash guard tripped") {
			recovery = "explicit operator reset (governor state removed)"
		}
	}

	// A perpetual loop with no bound and no kill switch is a footgun. Require
	// one explicit termination affordance unless the operator opts into --yolo.
	if gov.MaxCycles == 0 && gov.NoProgressHalt == 0 && !cfg.yolo {
		return clikit.Usagef("refusing an unbounded loop with no stop condition: set --max-cycles N, keep --halt-after-idle > 0, or pass --yolo to accept a truly perpetual run (kill it with the stop file: %s)", gov.StopFile)
	}

	var run runner
	if cfg.dryRun {
		run = dryRunner{log: func(s string) { fmt.Fprintln(ctx.Stdout, s) }}
	} else {
		if id.Grant != model.GrantRW {
			return clikit.Refusedf("dacli loop spawns agents and lands PRs — that needs an rw grant (you are %s)", id.Grant)
		}
		run = execRunner{cwd: ctx.Cwd}
	}

	d := &driver{ctx: ctx, w: w, cfg: cfg, gov: gov, run: run, sleep: time.Sleep, now: time.Now, recovery: recovery, phases: phases}
	if restoredOK {
		d.restoredTrunkMarker = restored.TrunkMarker
		d.restoredTrunkMarkerKnown = restored.TrunkMarkerKnown
	}

	// Resume the landing ledger the previous invocation checkpointed. Without
	// this the loop's three landing guarantees hold only in --yolo: the default
	// mode returns at every checkpoint, so in-memory pending state never
	// survived to the invocation that needed it (see journal.go).
	j := journal
	for _, msg := range journalWarn {
		// Say what was dropped. A silently shortened ledger looks exactly like
		// "nothing was outstanding", which is the failure this file prevents.
		fmt.Fprintf(ctx.Stderr, "warning: cycle journal: %s — that entry is not being reconciled this cycle\n", msg)
	}
	d.pendingAccept, d.pendingLand = j.PendingAccept, j.PendingLand
	// A crash can occur after the remote operation checkpoint but before the
	// older landing journal is rewritten. Reconstruct that derived state from
	// the finer-grained authoritative ledger so restart observes the existing
	// PR instead of spawning or opening another one.
	d.restoreLandingPhases()
	if len(d.pendingAccept) > 0 || len(d.pendingLand) > 0 {
		d.logf("resuming: %d task(s) awaiting merge confirmation, %d record branch(es) in flight",
			len(d.pendingAccept), len(d.pendingLand))
	}
	// The ceiling, unlike the spend, was never persisted: a restart that
	// omitted --window-tokens restored the spend and then ran UNCAPPED. An
	// explicit flag still wins, so an operator can raise or lower it.
	if gov.WindowTokens == 0 && j.WindowTokens > 0 {
		gov.WindowTokens = j.WindowTokens
		d.logf("resuming token ceiling %d from the journal (pass --window-tokens to change it)", j.WindowTokens)
	}

	return d.loop()
}

// cmdLoopStatus reports the last persisted snapshot of a loop run for a
// project — the running loop's own writes if one is mid-flight, or the final
// snapshot of the last completed run otherwise.
func cmdLoopStatus(ctx *clikit.Ctx, args []string) error {
	w, _, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	f, _ := clikit.ParseFlags(args)
	if err := f.Reject("project"); err != nil {
		return err
	}

	project := f.Get("project")
	if project == "" {
		ps, _ := store.ListProjects(w)
		if len(ps) == 1 {
			project = ps[0].Slug
		} else {
			return clikit.Usagef("usage: dacli loop status --project <slug>")
		}
	}

	st, err := readLoopState(w, project)
	if err != nil {
		return fmt.Errorf("no persisted loop state for project %s — run `dacli loop --project %s` at least once", project, project)
	}
	cp, cpErr := readLoopRecovery(w, project)
	budget, budgetErr := readTokenBudget(w, project)
	if budgetErr == nil {
		budget = reconcileReservations(w, budget, time.Now())
	}
	if ctx.JSON {
		if cpErr != nil {
			return fmt.Errorf("no typed loop recovery checkpoint for project %s: %w", project, cpErr)
		}
		out := struct {
			loopRecoveryCheckpoint
			WindowTokens int64                `json:"window_tokens"`
			Backlog      int                  `json:"backlog"`
			Rollup       cycleRollup          `json:"rollup"`
			Outcome      cycleOutcome         `json:"cycle_outcome"`
			Recovery     string               `json:"recovery,omitempty"`
			TokenBudget  *tokenBudgetSnapshot `json:"token_budget,omitempty"`
		}{loopRecoveryCheckpoint: cp, WindowTokens: st.WindowTokens, Backlog: st.Backlog, Rollup: st.Rollup, Outcome: st.Outcome, Recovery: st.Recovery}
		if budgetErr == nil {
			out.TokenBudget = &budget
		}
		enc := json.NewEncoder(ctx.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	fmt.Fprintf(ctx.Stdout, "project %s — cycle %d · trunk marker %d · tokens this window %d · ready backlog %d\n",
		st.Project, st.Cycle, st.TrunkMarker, st.WindowTokens, st.Backlog)
	fmt.Fprintf(ctx.Stdout, "rollup: %s\n", st.Rollup)
	if st.Outcome.Schema != "" {
		fmt.Fprintf(ctx.Stdout, "cycle outcome: %s · selected %d · failures %d\n", st.Outcome.Classification, st.Outcome.Selected, len(st.Outcome.Failures))
	}
	for _, line := range st.Rollup.Recovery() {
		fmt.Fprintf(ctx.Stdout, "  → %s\n", line)
	}
	fmt.Fprintf(ctx.Stdout, "last: %s", st.Status)
	if st.Reason != "" {
		fmt.Fprintf(ctx.Stdout, " (%s)", st.Reason)
	}
	if !st.UpdatedAt.IsZero() {
		fmt.Fprintf(ctx.Stdout, " · updated %s", st.UpdatedAt.Format(time.RFC3339))
	}
	fmt.Fprintln(ctx.Stdout)
	if st.Recovery != "" {
		fmt.Fprintf(ctx.Stdout, "recovery: %s\n", st.Recovery)
	}
	if budgetErr == nil {
		fmt.Fprintf(ctx.Stdout, "tokens: %s · workers %d/%d · review reserve %d · integration/recovery reserve %d",
			budget.Mode, budget.AllocatedWidth, budget.RequestedWidth, budget.ReviewReservation, budget.RecoveryReserve)
		if budget.Unallocated != nil {
			fmt.Fprintf(ctx.Stdout, " · unallocated %d", *budget.Unallocated)
		} else {
			fmt.Fprint(ctx.Stdout, " · enforceable remaining unknown")
		}
		fmt.Fprintln(ctx.Stdout)
	}
	if cpErr == nil {
		fmt.Fprintf(ctx.Stdout, "checkpoint: %s · halt class %s · retryable %t\n", cp.Checkpoint, cp.HaltClass, cp.Retryable)
		for _, ref := range cp.AffectedRefs {
			fmt.Fprintf(ctx.Stdout, "  affected %s: %s\n", ref.Kind, ref.ID)
		}
		if cp.NextAction != "" {
			fmt.Fprintf(ctx.Stdout, "next: %s\n", cp.NextAction)
		}
	}
	return nil
}

// printLoopAdvisory is the body of `loop --advise`: the expected token cost
// of ONE cycle at this width, from measured calibration bands — the P2 loop's
// budgeting sibling to `spawn --advise`'s per-task figure. It changes nothing.
//
// A cycle spends tokens on `width` build spawns (role implRole) plus one
// review spawn (role reviewRole); `wait`/`accept`/`ship`/`retro` run
// in-process and spend none. Bands here group by ROLE ALONE, not the full
// role×model×runtime triple `dacli calibrate` reports — the loop does not pin
// a model or runtime ahead of a spawn, so role is the coarsest grouping this
// projection can honestly commit to (store.TokensPerRun).
func printLoopAdvisory(ctx *clikit.Ctx, w *workspace.Workspace, cfg loopCfg) {
	samples := store.CalibrationSamples(w)
	fmt.Fprintf(ctx.Stdout, "── loop advise · width %d · impl=%s · review=%s · harness=%s:%s ──\n", cfg.width, cfg.implRole, cfg.reviewRole, cfg.harnessMode, strings.Join(cfg.allowedHarnesses, ","))
	if cfg.implRoleExplicit {
		fmt.Fprintln(ctx.Stdout, "  build role source: explicit --impl-role override (phase routing may replace it only when the phase refuses its kind)")
	} else {
		fmt.Fprintf(ctx.Stdout, "  build role source: automatic cost routing per task (project-stack fallback %s; phase routing takes precedence when gated)\n", cfg.implRole)
	}

	implMed, implP10, implP90, implN := store.TokensPerRun(samples, cfg.implRole)
	reviewMed, reviewP10, reviewP90, reviewN := store.TokensPerRun(samples, cfg.reviewRole)

	report := func(label, role string, med, p10, p90 float64, n int) {
		switch {
		case n >= 10:
			fmt.Fprintf(ctx.Stdout, "  %-6s role %-14s ~%.0f median output-tokens/run  p10–p90 %.0f–%.0f  (n=%d) ← AUTHORITATIVE\n",
				label, role, med, p10, p90, n)
		case n > 0:
			fmt.Fprintf(ctx.Stdout, "  %-6s role %-14s ~%.0f median output-tokens/run  (n=%d, PROVISIONAL — n<10)\n",
				label, role, med, n)
		default:
			fmt.Fprintf(ctx.Stdout, "  %-6s role %-14s no token history yet\n", label, role)
		}
	}
	report("build", cfg.implRole, implMed, implP10, implP90, implN)
	report("review", cfg.reviewRole, reviewMed, reviewP10, reviewP90, reviewN)

	switch {
	case implN > 0 && reviewN > 0:
		expected := float64(cfg.width)*implMed + reviewMed
		low := float64(cfg.width)*implP10 + reviewP10
		high := float64(cfg.width)*implP90 + reviewP90
		conf := "AUTHORITATIVE"
		if implN < 10 || reviewN < 10 {
			conf = "PROVISIONAL — a band above has n<10"
		}
		fmt.Fprintf(ctx.Stdout, "  expected cycle cost at width %d: ~%.0f output tokens  (band %.0f–%.0f)  %s\n",
			cfg.width, expected, low, high, conf)
	case implN > 0 || reviewN > 0:
		fmt.Fprintln(ctx.Stdout, "  expected cycle cost: partial — one role above has no token history yet, so no combined figure")
	default:
		fmt.Fprintln(ctx.Stdout, "  expected cycle cost: no measured band history yet — run some cycles first, then `dacli calibrate`")
	}
	fmt.Fprintln(ctx.Stdout, "── (advice only; no agents spawned) ──")
}

type driver struct {
	ctx                      *clikit.Ctx
	w                        *workspace.Workspace
	cfg                      loopCfg
	gov                      *Governor
	run                      runner
	sleep                    func(time.Duration)
	now                      func() time.Time
	trunkBranch              string // the branch ship/integrate lands into; resolved once
	lastTrunkMarker          int    // most recently observed trunkMarker(), for status snapshots
	lastTrunkKnown           bool
	restoredTrunkMarker      int
	restoredTrunkMarkerKnown bool
	recovery                 string
	lastRollup               cycleRollup // most recently computed cycle rollup, for status snapshots (dacli 299)
	lastOutcome              cycleOutcome
	cycleFailures            []cyclePhaseFailure
	pendingLand              []string        // self-PR branches opened this run not yet confirmed merged (see recordSelfPR)
	pendingAccept            []pendingAccept // built tasks whose `accept --force` awaits PR-merge confirmation (see reconcilePendingAccepts)
	phases                   cyclePhaseJournal
	phaseErr                 error
	plannedWidth             int
	tokenBudget              tokenBudgetSnapshot
	reviewLaunchFingerprint  string
	// afterPhase is an in-process crash-injection seam used by the composed
	// restart fixture. Production drivers leave it nil. It runs only after the
	// checkpoint has been atomically written and re-read, so an injected stop
	// models process loss at the exact recovery boundary rather than a failed
	// persistence operation.
	afterPhase func(cyclePhase) error
}

func (d *driver) cycleWidth() int {
	if d.plannedWidth > 0 {
		return d.plannedWidth
	}
	return d.cfg.width
}

func (d *driver) prepareTokenBudget(ready []*store.Task) error {
	requested := len(selectClaimCompatibleWave(d.w.Root, ready, d.cfg.width).Tasks)
	cycleLimit := int64(0)
	if profile, err := loadProfile(d.w, d.cfg.project); err == nil {
		cycleLimit = profile.Budgets.PerCycleTokens
	}
	var priorRuns []runReservation
	var priorReserved int64
	if prior, err := readTokenBudget(d.w, d.cfg.project); err == nil {
		prior = reconcileReservations(d.w, prior, d.now())
		for _, reservation := range prior.Runs {
			if reservation.State == "live" || reservation.State == "planned" {
				priorRuns = append(priorRuns, reservation)
				priorReserved += reservation.Tokens
			}
		}
	}
	d.tokenBudget = reservationPlan(d.cfg.project, d.gov.Cycle()+1, requested, d.cfg.perCycleTok, cycleLimit,
		d.gov.WindowTokens, d.gov.WindowSpent(), priorReserved, d.gov.WindowStart(), d.gov.windowDur(), !d.cfg.allowAdvisoryTokens, d.now())
	d.tokenBudget.Runs = priorRuns
	d.plannedWidth = d.tokenBudget.AllocatedWidth
	d.logf("token plan: mode=%s · workers=%d/%d · implementation=%d · review=%d · integration/recovery=%d",
		d.tokenBudget.Mode, d.tokenBudget.AllocatedWidth, d.tokenBudget.RequestedWidth,
		int64(d.tokenBudget.AllocatedWidth)*d.cfg.perCycleTok, d.tokenBudget.ReviewReservation, d.tokenBudget.RecoveryReserve)
	if requested > 0 && d.plannedWidth == 0 && d.cfg.perCycleTok > 0 {
		return clikit.Refusedf("cycle token reservation cannot fit one worker plus review and integration/recovery reserve (cycle remaining %s, rolling remaining %s)",
			clikit.OrDash(valueString(d.tokenBudget.CycleBudget.Remaining)), clikit.OrDash(valueString(d.tokenBudget.RollingBudget.Remaining)))
	}
	if requested > d.plannedWidth {
		d.logf("  shrinking wave from %d to %d before spawn so completion reserves remain funded", requested, d.plannedWidth)
	}
	if !d.cfg.dryRun {
		return writeTokenBudget(d.w, d.tokenBudget)
	}
	return nil
}

func valueString(value *int64) string {
	if value == nil {
		return ""
	}
	return fmt.Sprint(*value)
}

func hasPendingAccept(entries []pendingAccept, seq, generation int) bool {
	for _, entry := range entries {
		if entry.Seq == seq && entry.GenerationSet && entry.Generation == generation {
			return true
		}
	}
	return false
}

// pendingAccept is a self-PR task built this run whose task record is held
// open (not `accept --force`d) until its PR's fate is confirmed. Closing the
// record the moment the PR merely OPENS — the old behavior — marked the task
// done before GitHub's async auto-merge (or a later CI failure) ever
// happened, so the backlog could claim "done" work the trunk never received
// (issue #74 / task 115). Holding it here also excludes the task from the
// ready frontier (see excludePending) so a still-in-flight PR is never
// rebuilt by a subsequent cycle.
type pendingAccept struct {
	Seq            int
	Branch         string
	Generation     int  // task generation whose landing this entry recovers (issue #679)
	GenerationSet  bool // false only for backward-compatible legacy journal entries
	VerifyRequired bool // recovery already surfaced the owner-only --verify action (issue #661)
}

func (d *driver) logf(format string, a ...any) {
	fmt.Fprintf(d.ctx.Stdout, format+"\n", a...)
}

// saveState commits the recovery ledger at every governor checkpoint. The
// loop-status and governor snapshots remain advisory, but failure to persist
// the landing journal must stop the loop before it reports the checkpoint.
func (d *driver) saveState(status, reason string, backlog int) error {
	// A preview must not become the last production outcome or consume any of
	// the governor/journal state it is describing (task 370). Keeping the guard
	// here makes every present and future checkpoint dry-run safe.
	if d.cfg.dryRun {
		return nil
	}
	// The landing ledger rides every checkpoint, because the default mode
	// RETURNS at each one: without this, `pendingAccept`/`pendingLand` die with
	// the process and the next invocation re-picks tasks whose PRs merged and
	// pushes the record out from under PRs still in flight (see journal.go).
	if err := writeCycleJournal(d.w, d.cfg.project, cycleJournal{
		PendingAccept:   d.pendingAccept,
		PendingLand:     d.pendingLand,
		WindowTokens:    d.gov.WindowTokens,
		Landing:         d.cfg.landing,
		LandingExplicit: d.cfg.landingExplicit,
	}); err != nil {
		return fmt.Errorf("persist loop recovery ledger: %w", err)
	}
	if err := writeLoopState(d.w, loopState{
		Project:      d.cfg.project,
		Cycle:        d.gov.Cycle(),
		TrunkMarker:  d.lastTrunkMarker,
		WindowTokens: d.gov.WindowSpent(),
		Backlog:      backlog,
		Status:       status,
		Reason:       reason,
		Recovery:     d.recovery,
		Rollup:       d.lastRollup,
		Outcome:      d.lastOutcome,
		UpdatedAt:    d.now(),
	}); err != nil {
		return fmt.Errorf("persist versioned cycle outcome: %w", err)
	}
	govState := d.gov.State()
	govState.TrunkMarker = d.lastTrunkMarker
	govState.TrunkMarkerKnown = d.lastTrunkKnown
	writeGovernorState(d.w, d.cfg.project, govState)
	if err := writeLoopRecovery(d.w, decisionRecovery(d, status, reason)); err != nil {
		return fmt.Errorf("persist typed loop checkpoint: %w", err)
	}
	return nil
}

func (d *driver) loop() error {
	d.logf("dacli loop — project %s · impl=%s · review=%s · harness=%s:%s · width=%d%s",
		d.cfg.project, d.cfg.implRole, d.cfg.reviewRole, d.cfg.harnessMode, strings.Join(d.cfg.allowedHarnesses, ","), d.cfg.width, dryTag(d.cfg.dryRun))
	action, gates := "local merge", "task acceptance and configured verification"
	if d.cfg.pr {
		action, gates = "open/reuse PR for explicit landing", "GitHub required checks and reviews"
		if d.cfg.autoMerge {
			action = "open/reuse PR and queue auto-merge"
		}
	}
	d.logf("landing policy: mode=%s · base=%s · override=%t · PR action=%s · required gates=%s",
		d.cfg.landing.Mode, clikit.OrDash(d.trunkBase(), "repository default"), d.cfg.landingExplicit, action, gates)
	if d.gov.MaxCycles > 0 {
		d.logf("bounded to %d cycle(s); stop file: %s", d.gov.MaxCycles, d.gov.StopFile)
	} else {
		d.logf("perpetual; stop file: %s · thrash-halt after %d cycles with no trunk advance", d.gov.StopFile, d.gov.NoProgressHalt)
	}
	if d.recovery != "" {
		d.logf("recovery: %s", d.recovery)
	}

	d.trunkBranch = d.resolveTrunkBranch()
	if d.trunkBranch == "" {
		d.logf("note: no trunk branch could be resolved (detached HEAD with no main/master?) — trunk measurement degrades to a best-effort default")
	}
	// prevTrunkKnown carries "we have a real baseline to subtract from". Until
	// one measurement succeeds there is nothing to compare against, and a
	// missing baseline must never be spelled 0 — see trunkMarker (dacli 212).
	prevTrunk, prevTrunkKnown := d.trunkMarker()
	if prevTrunkKnown {
		d.lastTrunkMarker = prevTrunk
		d.lastTrunkKnown = true
		if d.restoredTrunkMarkerKnown && prevTrunk > d.restoredTrunkMarker {
			d.gov.ResetZeroStreak()
			trunkRecovery := fmt.Sprintf("observed trunk advanced between invocations (%d → %d)", d.restoredTrunkMarker, prevTrunk)
			if d.recovery == "" {
				d.recovery = trunkRecovery
			} else {
				d.recovery += "; " + trunkRecovery
			}
			d.logf("recovery: %s", trunkRecovery)
		}
	}

	for {
		// Reconcile the local trunk checkout with origin BEFORE doing anything
		// else this cycle: under --pr --auto, GitHub merges a fixer's PR (and
		// deletes its branch) asynchronously, on its own schedule — not
		// synchronously inside any dacli command this loop runs. Without this,
		// local main only ever falls further behind origin/main across cycles,
		// and the record commit recordSelfPR makes later in this same cycle
		// would sit behind trunk, risking the non-fast-forward push
		// PushSync exists to retry. Best-effort like every other network touch
		// here: it only ever fast-forwards (never discards local work), and a
		// diverged local, missing remote, or wedged network just leaves a note.
		d.syncTrunk()

		// Reconcile the durable prior cycle before acceptance, cleanup, stage
		// advancement, or task selection. Unknown GitHub evidence is not green;
		// the next bounded invocation re-observes it and resumes only after the
		// external condition is visibly resolved (issue #859).
		if !d.cfg.dryRun {
			checkpoint, err := d.reconcileBeforeCycle()
			if err != nil {
				return fmt.Errorf("pre-cycle reconciliation: %w", err)
			}
			if checkpoint != nil {
				if err := d.saveState(Halt.String(), checkpoint.Reason, 0); err != nil {
					return err
				}
				// saveState persists the journal/governor and a generic decision;
				// this evidence-rich record is the authoritative final checkpoint.
				if err := writeLoopRecovery(d.w, *checkpoint); err != nil {
					return fmt.Errorf("persist pre-cycle recovery halt: %w", err)
				}
				raw, _ := json.Marshal(checkpoint)
				d.logf("● halt: %s", checkpoint.Reason)
				d.logf("  recovery checkpoint JSON: %s", raw)
				return nil
			}
		}

		// Reconcile every task whose accept is still deferred (built by a prior
		// cycle in --pr mode, awaiting its PR's fate): a confirmed merge closes it
		// now, a PR that closed unmerged drops it from tracking so it re-enters
		// the ready pool for a fresh attempt instead of staying stuck forever —
		// see pendingAccept and reconcilePendingAccepts. Its rollup contribution (a
		// merge or an orphan observed THIS pass, however long ago the task was
		// built) seeds this cycle's tally; runCycle's own batch classification
		// below is added to it once — and if — a cycle actually runs (dacli 299).
		var reconcileRollup cycleRollup
		if !d.cfg.dryRun {
			reconcileRollup = d.reconcilePendingAccepts()
			if d.phaseErr != nil {
				return d.phaseErr
			}
		}
		d.lastRollup = reconcileRollup

		// Reclaim worktrees whose branch has already landed or whose run is
		// finished. reconcilePendingAccepts/gcBranch only reap the branches THIS
		// loop is tracking a PR for; a task closed by an operator, a prior loop,
		// or a --worktree spawn outside the accept flow leaves its checkout
		// behind forever — one live checkout per task ever spawned, until a real
		// run hit 86 worktrees / 2.2 GB (dacli 252). This blanket sweep is the
		// safety-checked catch-all (see store.ReclaimableWorktrees).
		if !d.cfg.dryRun {
			d.reapWorktrees()
		}

		// Walk the stage gates as far as they open before choosing this
		// cycle's work — a project sitting in a phase whose gates have all
		// passed is deadlock, not process (see advanceStages, dacli 189).
		if !d.cfg.dryRun {
			d.advanceStages()
		}

		ready, err := d.readyTasks()
		if err != nil {
			return err
		}
		ready = excludePending(ready, d.pendingAccept)
		rankByPriority(d.w, d.cfg.project, ready)
		dec, why := d.gov.Before(len(ready), d.now())
		if dec == Proceed {
			// Issue #867: sizing is part of resolving the cycle, not an
			// implementation phase. Run it before preflight, then reload and rank
			// so capacity, routing, claims, and the live wave all see the same
			// estimate rather than certifying stale unsized pointers.
			d.sizeUnestimated(selectClaimCompatibleWave(d.w.Root, ready, d.cfg.width).Tasks)
			ready, err = d.readyTasks()
			if err != nil {
				return err
			}
			ready = excludePending(ready, d.pendingAccept)
			rankByPriority(d.w, d.cfg.project, ready)
			if err := d.prepareTokenBudget(ready); err != nil {
				return err
			}
		}
		if err := d.saveState(dec.String(), why, len(ready)); err != nil {
			return err
		}
		if dec == Proceed || dec == Idle {
			if err := d.preflightCycle(ready); err != nil {
				return err
			}
		}
		switch dec {
		case Halt:
			d.logf("● halt: %s", why)
			return nil
		case SleepWindow:
			rem := d.gov.WindowRemaining(d.now())
			d.logf("● %s (resets in %s)", why, rem.Round(time.Second))
			if d.cfg.dryRun {
				return nil
			}
			d.sleep(rem)
			continue
		case Idle:
			d.logf("● cycle %d: %s", d.gov.Cycle()+1, why)
			d.cycleFailures = nil
			d.lastRollup = cycleRollup{}
			d.lastOutcome = d.finishCycleOutcome(0, d.lastRollup)
			// Even with an empty backlog, run a review pass to regenerate work —
			// that is what makes the machine self-feeding rather than stalling.
			// Its spend is charged to the SAME window a runCycle charges — an
			// idle tick is not a sprint (no cycle-counter/thrash-streak bump),
			// but its tokens are real and must still count against
			// --window-tokens, the loop's steady-state cost guard.
			since := store.LatestRunID(d.w)
			d.reviewPhase()
			if d.cfg.dryRun {
				return nil
			}
			d.gov.ChargeIdleTokens(store.RunsTokensSince(d.w, since))
			if err := d.saveState(dec.String(), why, len(ready)); err != nil {
				return err
			}
			// An UNPRODUCTIVE idle — review regenerated no ready work — counts
			// toward --max-cycles, so a bounded run on a permanently empty
			// backlog terminates instead of idling forever (dacli 172). A
			// PRODUCTIVE idle (review filed work) is not charged: the build it
			// enables is the cycle the budget is for, and the next iteration
			// proceeds straight to it.
			if after, _ := readyTasks(d.w, d.cfg.project); len(after) == 0 {
				d.gov.CountIdleCycle()
				if d.gov.MaxCyclesReached() {
					d.logf("● reached --max-cycles %d (backlog still empty)", d.gov.MaxCycles)
					return nil
				}
			}
			d.sleep(d.gov.Idle)
			continue
		}

		tokens, batchRollup := d.runCycle(ready)
		if d.phaseErr != nil {
			d.recordCycleFailure("checkpoint", d.phaseErr, d.phaseErr.Error())
			d.lastRollup = reconcileRollup.add(batchRollup)
			d.lastOutcome = d.finishCycleOutcome(d.lastOutcome.Selected, d.lastRollup)
			if err := d.saveState("degraded", d.phaseErr.Error(), len(ready)); err != nil {
				return err
			}
			return d.phaseErr
		}
		d.lastRollup = reconcileRollup.add(batchRollup)
		d.logf("  cycle rollup: %s", d.lastRollup)
		for _, line := range d.lastRollup.Recovery() {
			d.logf("    → %s", line)
		}
		// The preview has now printed the complete action plan. Do not measure it
		// as a production cycle: AfterCycle would increment cycle/streak/window
		// state and can falsely trip a thrash halt one step below the threshold.
		if d.cfg.dryRun {
			d.logf("(dry-run: one cycle previewed; stopping)")
			return nil
		}

		// PROGRESS — the thrash guard's signal is REAL trunk advancement, not a
		// task-status delta. Under the default --pr --auto path, merges land on
		// origin ASYNCHRONOUSLY (GitHub merges each PR after its CI passes), so a
		// task that `accept --all` closes this cycle may not have merged yet — or
		// may fail CI and never merge. `landed` is therefore the count of commits
		// that actually reached trunk (local OR origin) since the last cycle. A
		// PR queued this cycle that merges a cycle or two later resets the streak
		// then; only trunk that never moves across NoProgressHalt cycles halts —
		// which is exactly the runaway (PRs that never land) and stall (agents
		// producing nothing) the guard exists to catch.
		//
		// A cycle whose marker could not be READ is charged as unmeasured: the
		// thrash streak is left exactly as it was and prevTrunk keeps its last
		// good value, so the next successful measurement computes a real delta
		// spanning both cycles instead of one fabricated zero followed by one
		// fabricated repo-sized jump (dacli 212).
		landed := 0
		measured := false
		if curTrunk, ok := d.trunkMarker(); ok {
			d.lastTrunkMarker = curTrunk
			d.lastTrunkKnown = true
			if prevTrunkKnown {
				landed = curTrunk - prevTrunk
				if landed < 0 {
					landed = 0
				}
				measured = true
			}
			prevTrunk, prevTrunkKnown = curTrunk, true
		} else {
			d.logf("  note: could not measure trunk on %s — this cycle counts as unmeasured, not as zero progress", orDefault(d.trunkBranch, "main"))
		}

		if measured {
			dec, why = d.gov.AfterCycle(landed, tokens)
		} else {
			dec, why = d.gov.AfterCycleUnmeasured(tokens)
		}
		remaining, _ := readyTasks(d.w, d.cfg.project)
		if err := d.saveState(dec.String(), why, len(remaining)); err != nil {
			return err
		}
		if err := d.lastOutcome.err(); err != nil {
			return err
		}
		if dec == Halt {
			d.logf("● halt: %s", why)
			return nil
		}
		// The stop file is re-checked here as well as in Before(): a whole wave
		// of child agents ran since the last check, and any of them (or the
		// operator watching them) may have asked the loop to stop (dacli 207).
		if d.gov.StopRequested() {
			if err := d.saveState(Halt.String(), d.gov.StopReason(), len(remaining)); err != nil {
				return err
			}
			d.logf("● halt: %s", d.gov.StopReason())
			return nil
		}
		if !d.cfg.yolo {
			progress := fmt.Sprintf("trunk advanced by %d", landed)
			if !measured {
				progress = "trunk advance not measurable this cycle"
			}
			d.logf("— cycle %d done (%s). Checkpoint: re-run to continue, or touch %s to stop —",
				d.gov.Cycle(), progress, d.gov.StopFile)
			return nil
		}
	}
}

// runCycle executes one full sprint: build → test → sync → land → review →
// retro. It returns the tokens charged and a rollup of how the batch
// resolved (landed / produced nothing / stalled / blocked — see
// classifyBatch); trunk-advancement (the thrash-guard signal) is measured by
// the caller across the cycle, not derived from a task-status delta here —
// see loop().
func (d *driver) runCycle(ready []*store.Task) (tokens int64, rollup cycleRollup) {
	d.cycleFailures = nil
	since := store.LatestRunID(d.w)
	defer func() {
		tokens = store.RunsTokensSince(d.w, since)
		d.settleCycleTokenBudget(tokens)
	}()
	cycle := d.gov.Cycle() + 1
	wave := selectClaimCompatibleWave(d.w.Root, ready, d.cycleWidth())
	batch := wave.Tasks
	defer func() { d.lastOutcome = d.finishCycleOutcome(len(batch), rollup) }()
	d.logf("● cycle %d — building %d task(s):", cycle, len(batch))
	for _, collision := range wave.Collisions {
		d.logf("  → %03d: planned claim collision (%s overlaps %s) — leaving open without spawning", collision.Task.Seq, collision.Mine, collision.Theirs)
	}

	// BUILD — one detached implementer per task, each opening its own PR. A
	// task only counts as built if BOTH the spawn command itself did not
	// error (a synchronous refusal — taint, budget, malformed flags) AND, once
	// the wave finishes, its dacli/<seq>-slug branch actually exists (catching
	// an async failure: the child crashed or was killed after a clean launch
	// and never committed). A batch task that fails either check must not be
	// force-closed below — the next cycle has to re-pick it, not silently lose it.
	built := make(map[int]bool, len(batch))
	// The fallback role is resolved per cycle, not read straight off the
	// config: on a phase-gated project the configured implementer may have no
	// work in the current phase, and spawning it anyway is a guaranteed
	// refusal (dacli 189). On an untemplated project buildRole returns
	// cfg.implRole unchanged.
	fallbackRole := d.buildRole()
	fallbackSource := "automatic cost routing"
	if d.cfg.implRoleExplicit {
		fallbackSource = "explicit override"
	}
	if fallbackRole != d.cfg.implRole {
		fallbackSource = "phase routing"
	}
	// Within the fallback's kind, each task then routes to the cheapest role
	// whose capacity covers ITS OWN Te — the loop used to spawn every task in
	// the batch with the one fallback role regardless of size, so a one-line
	// typo fix and a subsystem rewrite landed on the same (often the most
	// expensive) model. team.CheapestCapable already existed for `dacli team
	// assign`/`spawn --advise` (230, 231); this wires the loop's own batching
	// through it. A task with no estimate, or a roster with nothing else of
	// that kind, still spawns with the fallback exactly as before (dacli 233).
	// Sizing is best-effort and ran before the complete-cycle preflight, so
	// re-read the batch and say plainly when it did
	// not take. A task that is STILL unsized will be refused by every capped
	// role, so this cycle is structurally unable to build it — and the thrash
	// guard would otherwise report only "no net progress", leaving the cause
	// buried in a per-spawn refusal above it (issue #430, suggestion 3).
	d.reportStillUnsized(batch)
	// Resolve the whole wave's enforcement boundaries before launching any of
	// it. selectClaimCompatibleWave applied this same predicate while filling
	// the width-limited batch, so a collision cannot consume a worker slot and
	// dry-run/profile preview operate on identical input (issue #838).
	plannedClaims := wave.Claims

	roles, _ := store.LoadRoles(d.w)
	calibration := store.LoadCalibration(d.w)
	outcomes := store.FirstPassOutcomes(d.w)
	fallbackKind := ""
	if role, ok := store.LoadRole(d.w, fallbackRole); ok {
		fallbackKind = role.Kind
	}
	for _, t := range batch {
		claims, planned := plannedClaims[t.Seq]
		if !planned {
			continue
		}
		if checkpoint, ok := d.taskPhase(t); ok && phaseAtLeast(checkpoint.Phase, phaseSpawned) {
			d.logf("  → %s: resuming after durable %s checkpoint (run %s); not spawning a duplicate", t.ID, checkpoint.Phase, clikit.OrDash(checkpoint.RunID))
			built[t.Seq] = true
			continue
		}
		// The stop file is re-checked before EVERY spawn, not once per cycle in
		// Before(): a wave is the longest stretch of the loop, it is where all
		// the tokens go, and an operator who touches STOP while agents are
		// running means "launch no more", not "one more full width of them"
		// (dacli 207).
		if d.gov.StopRequested() {
			d.logf("  ● %s — launching no further agents this wave", d.gov.StopReason())
			break
		}
		buildRole := fallbackRole
		var routing team.Explanation
		automaticRouted := false
		if fallbackSource == "automatic cost routing" && fallbackKind != "" {
			te := 0.0
			if tp, sized := t.Estimate(); sized {
				te = tp.Expected()
			}
			automaticRouted = true
			candidates := d.routeCandidates(roles, calibration.Samples, outcomes, fallbackRole, fallbackKind, te)
			routing = (team.Strategy{}).Select(team.RouteRequirements{Kind: fallbackKind, Grant: "rw", Title: t.Title, Body: orchestrationTaskBody(t), Paths: store.ClaimHints(d.w.Root, t), TaskPoints: te, RequireTokenCeiling: d.cfg.perCycleTok > 0 && !d.cfg.allowAdvisoryTokens}, candidates)
			for _, candidate := range routing.Candidates {
				for _, exclusion := range candidate.Exclusions {
					if strings.Contains(exclusion, "token ceiling") {
						d.logf("    excluded role %s (runtime %s): %s", candidate.Role, clikit.OrDash(candidate.Runtime), exclusion)
					}
				}
			}
			if routing.Selected.Role != "" {
				buildRole = routing.Selected.Role
			}
		}
		if automaticRouted && routing.Selected.Role == "" {
			d.logf("  → %s: automatic routing found no eligible %s role — leaving open without bypassing scope, capacity, grant, or provider gates", t.ID, fallbackKind)
			continue
		}
		if routing.Selected.Role == "" {
			if role, ok := store.LoadRole(d.w, buildRole); ok {
				routing.Selected = team.RouteSelection{Role: role.Name, Runtime: role.Runtime, Model: role.ModelID()}
			}
		}
		if !d.cfg.dryRun {
			writeRoutingExplanation(d.w, cycle, t.Seq, routing)
		}
		ref := t.ID
		spawn := []string{"spawn", "--task", ref, "--role", buildRole, "--detach", "--worktree"}
		for _, harness := range d.cfg.allowedHarnesses {
			spawn = append(spawn, "--harness", harness)
		}
		// Claim the task's own path hints (dacli 299): the loop used to spawn
		// every wave with no --claim at all, so `gateClaimOverlap` had nothing to
		// arbitrate and two tasks touching the same tree could run in parallel and
		// merge-conflict each other — the operator did that arbitration by hand
		// (see the a-root finding this task was filed from). A claim carrying no
		// paths (a task whose text names nothing path-like) omits the flag
		// entirely, matching splitClaims's own "no claim" behavior.
		// ClaimHints, not PathHints: a claim is an ENFORCEMENT boundary, and
		// PathHints is documented as crude because for routing a spurious
		// token costs one weak tie-break vote. Here it cost an agent its whole
		// commit — task 338's "G104/G301/G302/G306" became its claim and
		// eighteen legitimate files were refused (issue #427). Only tokens
		// that resolve to a real path in the repo become a claim.
		if claim := strings.Join(claims, ","); claim != "" {
			spawn = append(spawn, "--claim", claim)
		}
		if d.cfg.pr {
			spawn = append(spawn, "--pr")
		}
		if d.cfg.perCycleTok > 0 {
			spawn = append(spawn, "--max-tokens", fmt.Sprint(d.cfg.perCycleTok))
			if d.cfg.allowAdvisoryTokens {
				spawn = append(spawn, "--allow-advisory-tokens")
			}
		}
		spawn = d.contextExceptionArgs(spawn)
		spawn = append(spawn, "--timeout", fmt.Sprint(d.workerTimeout(t)))
		decisionSource := routing.Source
		if fallbackSource != "automatic cost routing" {
			decisionSource = fallbackSource
		}
		d.logf("  → %s: %s — role %s · runtime %s · model %s · source %s", ref, t.Title, buildRole, clikit.OrDash(routing.Selected.Runtime), clikit.OrDash(routing.Selected.Model), decisionSource)
		if selected := routing.Candidate(buildRole); selected != nil {
			if selected.Score.TokenSamples > 0 {
				d.logf("    marginal estimate: %.0f output tokens/completed task (n=%d) · cost tier %d", selected.Score.TokensPerCompleted, selected.Score.TokenSamples, selected.Score.CostTier)
			} else {
				d.logf("    marginal estimate: unknown provider usage · cost tier %d", selected.Score.CostTier)
			}
			for _, candidate := range routing.Candidates {
				if candidate.Role == selected.Role || (candidate.Score.CostTier >= selected.Score.CostTier && candidate.Score.TokensPerCompleted >= selected.Score.TokensPerCompleted) {
					continue
				}
				reason := "ranked behind the selected cheapest capable route"
				if len(candidate.Exclusions) > 0 {
					reason = strings.Join(candidate.Exclusions, "; ")
				}
				d.logf("    cheaper candidate %s rejected: %s", candidate.Role, reason)
			}
		}
		if routing.Uplift != "" {
			d.logf("    consequence uplift: %s", routing.Uplift)
		}
		if out, err := d.run.run("spawn", spawn...); err != nil {
			d.logf("    spawn refused/failed: %s", strings.TrimSpace(out))
			d.recordCycleFailure("spawn", err, out)
			if policyLaunchRefusal(err) {
				d.phaseErr = clikit.Refusedf("spawn policy refusal: %s", strings.TrimSpace(out))
				return
			}
			continue
		}
		built[t.Seq] = true
		if !d.checkpointTaskPhase(t, phaseSpawned) {
			return
		}
		if !d.cfg.dryRun && d.cfg.perCycleTok > 0 {
			runID := latestTaskRunID(d.w, t.ID)
			d.tokenBudget.Runs = append(d.tokenBudget.Runs, runReservation{Task: t.ID, RunID: runID, Tokens: d.cfg.perCycleTok, State: "live", ObservedAt: d.now().UTC()})
			if err := writeTokenBudget(d.w, d.tokenBudget); err != nil {
				d.phaseErr = fmt.Errorf("persist token reservation for %s: %w", t.ID, err)
				return
			}
		}
	}

	// TEST — block until the detached wave finishes and finalizes.
	d.logf("  waiting on the wave…")
	// A failed wait is not cosmetic: everything after this point — sync,
	// accept, integrate — assumes the wave FINISHED. Proceeding silently on a
	// failure means acting on half-written work, so say so. It is reported
	// rather than fatal because the steps below re-derive state from disk and
	// will simply find less to do.
	waitSucceeded := true
	if out, err := d.run.run("wait", "wait"); err != nil {
		waitSucceeded = false
		d.logf("    wait failed (%v) — the steps below assume the wave finished, so treat this cycle's results as partial: %s",
			err, clikit.FirstLine(out))
		d.recordCycleFailure("wait", err, clikit.FirstLine(out))
	}
	if !d.cfg.dryRun && d.tokenBudget.Project != "" {
		d.tokenBudget = reconcileReservations(d.w, d.tokenBudget, d.now())
		if err := writeTokenBudget(d.w, d.tokenBudget); err != nil {
			d.phaseErr = fmt.Errorf("settle token reservations after wait: %w", err)
			return
		}
	}
	for _, t := range batch {
		if built[t.Seq] && !d.checkpointTaskPhase(t, phaseWaited) {
			return
		}
		if built[t.Seq] && d.policyRefusedSince(t.ID, since) {
			d.logf("    %03d: child ended in exit-3 policy refusal — blocking instead of retrying unchanged", t.Seq)
			if cur, err := store.FindTask(d.w, fmt.Sprintf("%03d", t.Seq)); err == nil {
				store.AppendLog(cur, "blocked after an exit-3 policy refusal; change the claim or policy before retrying")
				_ = store.SaveTask(cur)
				_ = store.MoveTask(d.w, cur, model.StatusBlocked)
			}
			built[t.Seq] = false
		}
	}

	// SYNC — apply every pending proposal a read-only agent in the wave filed
	// as an event (a status change via `task block`/`task done`, a finding) so
	// it lands in the objects it references BEFORE this cycle's LAND step and
	// before the caller judges whether the cycle produced anything (dacli
	// 299). A read-only grant is the DEFAULT for a spawned agent — it cannot
	// mutate a task it does not own, so its "done" or "blocked" only ever
	// reaches the event log until the owner applies it; without this the loop
	// itself never called `sync`, so that work sat pending forever and the
	// task it touched read as untouched. Run before LAND so any file changes
	// sync makes ride in the SAME cycle's record commit, not an uncommitted
	// tree left for the next cycle to trip over.
	if out, err := d.run.run("sync", "sync"); err != nil {
		d.logf("  sync: %s", clikit.FirstLine(out))
		d.recordCycleFailure("sync", err, clikit.FirstLine(out))
	}

	// Re-check every spawn that launched cleanly: did its branch actually
	// land? A run that started fine can still die mid-flight.
	for _, t := range batch {
		if !built[t.Seq] {
			continue
		}
		branch := taskBranch(t)
		if !d.branchHasWork(branch) {
			d.logf("    %03d: %s has no commits after wait — treating spawn as failed", t.Seq, branch)
			built[t.Seq] = false
			if latestTaskRunID(d.w, t.ID) != "" {
				d.recordCycleFailure("commit", fmt.Errorf("no task commit"), branch+" has no commits after the worker completed")
			}
			if waitSucceeded && !d.clearTaskPhase(t) {
				return
			}
			continue
		}
		if !d.checkpointTaskPhase(t, phaseCommitted) {
			return
		}
		if !d.cfg.dryRun {
			if err := d.verifyTaskChange(t); err != nil {
				d.logf("    %03d: configured verification failed — leaving open: %v", t.Seq, err)
				built[t.Seq] = false
				d.recordCycleFailure("verification", err, err.Error())
				if !d.clearTaskPhase(t) {
					return
				}
				continue
			}
		}
		if !d.checkpointTaskPhase(t, phaseVerified) {
			return
		}
	}

	// DELIVERY REVIEW — when the operating profile requires review, landing is
	// gated by one structured, identity-bound verdict on the exact branch tree.
	// Request-changes enters the bounded correction/re-review transaction; every
	// non-approval remains open and therefore cannot be published as success.
	if d.structuredReviewRequired() {
		d.logf("  independently reviewing built tasks before landing…")
		for _, t := range batch {
			if built[t.Seq] && !d.reviewDeliveryTask(t) {
				built[t.Seq] = false
				d.recordCycleFailure("review", clikit.Refusedf("structured review did not approve"), "structured review did not approve the current tree")
				d.logf("    %03d: structured review did not approve the current tree — leaving open and unlanded", t.Seq)
			}
		}
	}

	// LAND — two models, chosen by --pr:
	if d.cfg.pr {
		// The child prompt asks for a self-PR, but landing is the controller's
		// transaction. Issue #792 proved a worker can commit and exit before that
		// prompt step, leaving verified work stranded in its worktree. Push and
		// run the idempotent PR command here as well: it creates the missing PR or
		// reuses the child's existing head PR and queues the same required checks.
		// The task record is NOT closed here — accept --force must wait until the
		// PR actually MERGES, or a task marked done here could still fail CI and
		// never land, leaving the backlog claiming work the trunk never received
		// (issue #74 / task 115). Instead every actually-built task is parked in
		// pendingAccept; reconcilePendingAccepts (called at the top of loop())
		// closes it once merged, or drops it (leaving it open for a fresh retry)
		// once its PR closes unmerged. A task whose spawn was refused/failed is
		// left open (not tracked, not box-checked) so the next cycle re-picks it.
		d.logf("  built tasks' accept is deferred until each PR's merge is confirmed…")
		for _, t := range batch {
			if !built[t.Seq] {
				d.logf("    %03d: spawn refused/failed — leaving open for retry", t.Seq)
				continue
			}
			if !d.queueTaskPR(t) {
				d.logf("    %03d: committed branch preserved for the actionable landing retry above", t.Seq)
				built[t.Seq] = false
				d.recordCycleFailure("pull-request", fmt.Errorf("PR publication failed"), "canonical branch or PR publication failed; inspect the phase log")
				continue
			}
			branch := taskBranch(t)
			d.pendingAccept = append(d.pendingAccept, pendingAccept{Seq: t.Seq, Branch: branch, Generation: t.Generation(), GenerationSet: true})
			d.pendingLand = append(d.pendingLand, branch)
		}
		d.recordSelfPR()
	} else {
		// Local model: fixers committed to their branches without opening PRs, so
		// the loop integrates them into trunk itself.
		d.logf("  integrating done branches…")
		// The local-landing path. A silent failure here is precisely issue
		// #419's shape: branches never reach trunk, tasks never close, and the
		// cycle reports "trunk advanced by 0" with no cause attached — the one
		// number the reporter said was their only symptom.
		if out, err := d.run.run("ship", d.shipArgs("--project", d.cfg.project)...); err != nil {
			d.logf("    integrate failed — NOTHING landed on trunk this cycle: %s", clikit.FirstLine(out))
			d.recordCycleFailure("ship", err, clikit.FirstLine(out))
		}
	}

	// ROLLUP — classify how this cycle's batch resolved (dacli 299): landed,
	// produced nothing, still in flight (stalled), or blocked. Computed here,
	// after LAND and after the SYNC step above has folded in anything a
	// read-only build agent proposed, so a task a wave blocked on a question
	// reports as blocked rather than as an ordinary in-flight task.
	rollup = d.classifyBatch(batch, built)

	// REVIEW — regenerate the backlog: an auditor files the next
	// evidence-based improvement(s) as fresh tasks. Skipped once STOP is
	// present: landing what the wave already produced is finishing started
	// work, but the review spawn is a NEW agent, and the stop file's promise is
	// that no new agent starts after it appears (dacli 207).
	if d.gov.StopRequested() {
		d.logf("  ● %s — skipping the review spawn", d.gov.StopReason())
		return
	}
	d.reviewPhase(batch...)

	// RETRO — harvest the cycle for the record. cmdRetro requires a ref and at
	// least one bullet; the loop passes the project as the ref and a factual
	// per-cycle bullet, so this records a note instead of exiting 2 unnoticed
	// every cycle (dacli 173).
	builtCount := 0
	for _, t := range batch {
		if built[t.Seq] {
			builtCount++
		}
	}
	// Best-effort: the retro note is a record, and losing one must never stop a
	// cycle that has already landed work. Explicitly discarded so the next
	// reader knows it is a decision rather than an oversight.
	_, _ = d.run.run("retro", "retro", d.cfg.project, "--improve",
		fmt.Sprintf("cycle: %d of %d spawned task(s) produced work; follow-ups are filed as tasks by the review phase", builtCount, len(batch)))

	// Workspace health, once per cycle. The loop never ran doctor, so duplicate
	// tasks, orphaned records and unparseable task files stayed invisible on
	// the ONE path nobody is watching — the inversion the audit for task 300
	// named: the unattended run should be the best-covered, not the least.
	//
	// Reported, never fatal: a corrupt workspace is something an operator must
	// see, but halting a governed loop on it would trade a visible problem for
	// a stalled machine.
	d.reportWorkspaceHealth()

	// The deferred token charge above sums every run this cycle produced
	// (build spawns + the review spawn) from their usage.txt actuals — 0 for
	// any run whose runtime never reported usage, the same honest degrade
	// calibration applies elsewhere.
	return
}

func (d *driver) settleCycleTokenBudget(tokens int64) {
	if d.cfg.dryRun || d.tokenBudget.Project == "" {
		return
	}
	d.tokenBudget.CycleBudget.Spent = tokens
	d.tokenBudget.RollingBudget.Spent = d.gov.WindowSpent() + tokens
	// Review is no longer future work once runCycle returns. Landing/recovery
	// capacity remains held while a PR is still in flight; releasing it at the
	// implementation wait would present money needed for reconciliation as free.
	d.tokenBudget.ReviewReservation = 0
	if len(d.pendingLand) == 0 && len(d.pendingAccept) == 0 {
		d.tokenBudget.RecoveryReserve = 0
	}
	d.tokenBudget = reconcileReservations(d.w, d.tokenBudget, d.now())
	if err := writeTokenBudget(d.w, d.tokenBudget); err != nil && d.phaseErr == nil {
		d.phaseErr = fmt.Errorf("settle completed cycle token budget: %w", err)
	}
}

// queueTaskPR completes the controller-owned remote half of PR landing. Both
// commands are deliberately task-addressed and idempotent: push fast-forwards
// the canonical branch, while pr reuses an already-open head PR and records its
// URL on the task. --auto delegates the merge to GitHub's required checks; this
// method never interprets a successful PR creation as a successful merge.
func (d *driver) queueTaskPR(t *store.Task) bool {
	if d.cfg.dryRun {
		d.logf("    %03d: would run `dacli push --task %s`, then create/reuse the PR against %s with required checks", t.Seq, t.ID, d.trunkBase())
		return true
	}
	checkpoint, resumed := d.taskPhase(t)
	if !resumed || !phaseAtLeast(checkpoint.Phase, phasePushed) {
		if out, err := d.run.run("push", "push", "--task", t.ID); err != nil {
			d.logf("    %03d: branch push failed: %s — retry `dacli push --task %s`; no PR was attempted", t.Seq, clikit.FirstLine(out), t.ID)
			return false
		}
		if !d.checkpointTaskPhase(t, phasePushed) {
			return false
		}
	}
	if resumed && phaseAtLeast(checkpoint.Phase, phasePRCreated) {
		d.logf("    %03d: resuming after durable %s checkpoint; not creating a duplicate PR", t.Seq, checkpoint.Phase)
		return true
	}
	args := []string{"pr", "--task", t.ID}
	if base := d.trunkBase(); base != "" {
		args = append(args, "--base", base)
	}
	if d.cfg.autoMerge {
		args = append(args, "--auto")
	}
	if out, err := d.run.run("pr", args...); err != nil {
		retry := fmt.Sprintf("dacli pr --task %s --base %s", t.ID, d.trunkBase())
		if d.cfg.autoMerge {
			retry += " --auto"
		}
		d.logf("    %03d: PR create/reuse failed after push: %s — retry `%s`; the committed branch is preserved", t.Seq, clikit.FirstLine(out), retry)
		return false
	}
	if !d.checkpointTaskPhase(t, phasePRCreated) {
		return false
	}
	d.logf("    %03d: canonical branch pushed; PR created or reused against %s (auto-merge=%t; required checks remain authoritative)", t.Seq, d.trunkBase(), d.cfg.autoMerge)
	return true
}

func (d *driver) routeCandidates(roles []team.Role, samples []store.CalibSample, outcomes map[store.Band]store.FirstPassOutcome, sourceName, kind string, te float64) []team.RouteCandidate {
	limits := store.LoadRuntimeLimits(d.w)
	allowed := map[string]bool{}
	if source, ok := store.LoadRole(d.w, sourceName); ok && source.Runtime != "" {
		if _, paused, _ := limits.Open(source.Runtime); paused {
			allowed[source.Name] = true
			for _, name := range source.FallbackTo {
				allowed[name] = true
			}
		}
	}
	out := make([]team.RouteCandidate, 0, len(roles))
	for _, role := range roles {
		if !strings.EqualFold(role.Kind, kind) || (len(allowed) > 0 && !allowed[role.Name]) {
			continue
		}
		if !d.roleAllowedByHarness(role) {
			continue
		}
		band := store.Band{Role: role.Name, Model: role.ModelID(), Runtime: role.Runtime}
		ratio, tokenN := store.MedianTokenRatio(samples, band)
		stat := outcomes[band]
		paused := false
		if role.Runtime != "" {
			_, paused, _ = limits.Open(role.Runtime)
		}
		// An undeclared grant retains the legacy role behavior; a declared ro
		// role is ineligible for the loop's implementation wave.
		grantEnforced := role.Grant != "ro"
		if role.Grant == "" {
			role.Grant = "rw"
		}
		tokenCeilingEnforced := false
		if rt, err := store.LoadRuntime(d.w, role.Runtime); err == nil {
			grantEnforced = grantEnforced && store.RuntimeWritable(rt)
			tokenCeilingEnforced = rt.TokenLimitFlag != ""
		}
		capacity := 1
		if role.WIP > 0 {
			if active, err := store.ActiveInRole(d.w, role.Name); err != nil {
				capacity = 0
			} else {
				capacity = role.WIP - active
			}
		}
		out = append(out, team.RouteCandidate{
			Role: role, GrantEnforced: grantEnforced, ContextLimit: role.Profile.ContextLimit,
			// max-tokens is an enforceable per-worker ceiling, not a scheduler
			// allocation. Using it as RemainingBudget made calibrated historical
			// cost reject a role that start's profile preview had selected, even
			// though the spawned worker would be bounded by that same ceiling
			// (issue #837). The rolling governor owns aggregate budget admission.
			CapacityRemaining: capacity, ProviderPaused: paused, TokenCeilingEnforced: tokenCeilingEnforced,
			Metrics: team.RouteMetrics{TokensPerCompleted: ratio * te, TokenSamples: tokenN, FirstPassSuccess: stat.Rate, SuccessSamples: stat.Samples, LatencySeconds: medianBandHours(samples, band) * 3600},
		})
	}
	return out
}

func validateHardTokenRoles(w *workspace.Workspace, cfg loopCfg) error {
	capable := func(roleName string) (string, bool) {
		role, ok := store.LoadRole(w, roleName)
		if !ok || role.Runtime == "" {
			return "no runtime configured", false
		}
		rt, err := store.LoadRuntime(w, role.Runtime)
		if err != nil {
			return fmt.Sprintf("runtime %s is unavailable", role.Runtime), false
		}
		if rt.TokenLimitFlag == "" {
			return fmt.Sprintf("runtime %s declares no token-limit flag", role.Runtime), false
		}
		return "", true
	}
	if cfg.implRoleExplicit {
		if reason, ok := capable(cfg.implRole); !ok {
			return clikit.Refusedf("hard token policy excludes explicitly selected implementation role %s: %s; choose a capable role, configure its runtime token_limit_flag, or explicitly pass --allow-advisory-tokens", cfg.implRole, reason)
		}
	} else {
		source, ok := store.LoadRole(w, cfg.implRole)
		if !ok {
			return clikit.Refusedf("hard token policy cannot resolve implementation role %s; configure a role whose runtime declares token_limit_flag, explicitly select a capable --impl-role, or explicitly pass --allow-advisory-tokens", cfg.implRole)
		}
		roles, _ := store.LoadRoles(w)
		var exclusions []string
		found := false
		for _, role := range roles {
			if strings.EqualFold(role.Kind, source.Kind) {
				if reason, ok := capable(role.Name); ok {
					found = true
				} else {
					exclusions = append(exclusions, fmt.Sprintf("%s (%s)", role.Name, reason))
				}
			}
		}
		if !found {
			return clikit.Refusedf("hard token policy found no %s implementation role with an enforceable runtime ceiling; excluded: %s; configure token_limit_flag on a runtime, explicitly select a capable role, or explicitly pass --allow-advisory-tokens", source.Kind, strings.Join(exclusions, "; "))
		}
	}
	if reason, ok := capable(cfg.reviewRole); !ok {
		return clikit.Refusedf("hard token policy excludes review role %s: %s; configure a review role whose runtime declares token_limit_flag, or explicitly pass --allow-advisory-tokens", cfg.reviewRole, reason)
	}
	return nil
}

func advisoryTokenRoles(w *workspace.Workspace, cfg loopCfg) string {
	names := []string{cfg.implRole, cfg.reviewRole}
	var unsupported []string
	for _, name := range names {
		role, ok := store.LoadRole(w, name)
		if !ok || role.Runtime == "" {
			unsupported = append(unsupported, name+" has no runtime")
			continue
		}
		rt, err := store.LoadRuntime(w, role.Runtime)
		if err != nil || rt.TokenLimitFlag == "" {
			unsupported = append(unsupported, name+"/"+role.Runtime)
		}
	}
	if len(unsupported) == 0 {
		return "all configured roles currently enforce the ceiling"
	}
	return "unsupported roles: " + strings.Join(unsupported, ", ")
}

func orchestrationTaskBody(t *store.Task) string {
	var b strings.Builder
	for _, section := range t.Doc.Sections {
		if !strings.EqualFold(section.Title, "Log") {
			b.WriteString(" ")
			b.WriteString(section.Content)
		}
	}
	return strings.TrimSpace(b.String())
}

// stackReviewRole handles composite recorded stacks (for example
// Vue/Supabase), for which the historical exact-name lookup could only invent
// "vue/supabase-auditor" and then fall back to go-auditor. A declared match
// in a reviewer's name, summary, skills, or scope is stronger evidence than a
// language-specific legacy fallback (task 495).
func stackReviewRole(w *workspace.Workspace, stack prompts.Stack, fallback string) string {
	if !stack.Recorded() || stack.IsGo() {
		return fallback
	}
	parts := strings.FieldsFunc(strings.ToLower(stack.Label), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
	roles, _ := store.LoadRoles(w)
	var matches []team.Role
	for _, role := range roles {
		if !strings.EqualFold(role.Kind, "reviewer") {
			continue
		}
		declared := strings.ToLower(role.Name + " " + role.Summary + " " + strings.Join(role.Skills, " ") + " " + strings.Join(role.Scope, " "))
		for _, part := range parts {
			if len(part) > 1 && strings.Contains(declared, part) {
				matches = append(matches, role)
				break
			}
		}
	}
	if pick, ok := team.CheapestCapableForTitled(matches, "reviewer", 0, nil, "Review "+stack.Label+" repository", ""); ok {
		return pick.Name
	}
	return fallback
}

func medianBandHours(samples []store.CalibSample, band store.Band) float64 {
	var values []float64
	for _, sample := range samples {
		if sample.Band == band {
			values = append(values, sample.Hours)
		}
	}
	if len(values) == 0 {
		return 0
	}
	sort.Float64s(values)
	return values[len(values)/2]
}

func routingExplanationFile(w *workspace.Workspace, cycle, seq int) string {
	return filepath.Join(w.Root, workspace.Dir, "loop", "routing", fmt.Sprintf("cycle-%03d-task-%03d.json", cycle, seq))
}

func writeRoutingExplanation(w *workspace.Workspace, cycle, seq int, explanation team.Explanation) {
	raw, err := json.MarshalIndent(explanation, "", "  ")
	if err != nil {
		return
	}
	path := routingExplanationFile(w, cycle, seq)
	if os.MkdirAll(filepath.Dir(path), 0o755) != nil {
		return
	}
	_ = writeStateFile(path, string(append(raw, '\n')))
}

// recordSelfPR commits the .dacli workspace record every cycle but only PUSHES
// it once none of the self-PR branches this loop opened are still awaiting
// GitHub's auto-merge. Pushing while one is in flight advances `main` out from
// under its queued PR, and under strict branch protection (require branches
// up to date) GitHub reads that as "behind" and never merges it — stranding
// every fixer PR, even at --width 1 (task 114 / issue #75). The record commit
// is data-only bookkeeping, so holding its push back never blocks a task from
// closing or a branch from landing; it just keeps `main` stable while a PR is
// pending, and catches the push up the first cycle nothing is left in flight.
func (d *driver) templateStage() string {
	p, err := store.LoadProject(d.w, d.cfg.project)
	if err != nil {
		return ""
	}
	s, _ := p.Doc.Front.Get("template_stage")
	return s
}

// advanceStages walks the project's stage gates as far as they will open, once
// per cycle.
//
// The loop used to be stage-BLIND — this package imported `gates` nowhere — so
// a project on a phase-gated template deadlocked on cycle one. `dacli init
// --template product` starts a project in the DISCOVERY phase, which admits
// only researcher and reviewer kinds, and spawn's phaseGate refuses an
// implementer there ("advance the stage first"). Nothing in an autonomous run
// ever advanced it: `dacli stage advance` is a command a human types. So every
// build spawn was refused, forever, until the thrash guard killed the run
// (dacli 189).
//
// The gate's purpose is to stop work moving on before its preconditions hold.
// Once every check for the current stage passes, that purpose is served and
// keeping the project there is pure deadlock — so the loop advances it itself
// and says so in the log, exactly as `stage advance` would. A closed gate is
// still an answer: the loop reports what is unmet and works the current phase.
//
// Untemplated (solo) projects have no stages and return on the first line, so
// their behavior is identical to before. Dry runs report and mutate nothing.
func (d *driver) advanceStages() {
	for i := 0; i < maxStageAdvancesPerCycle; i++ {
		stage := d.templateStage()
		if stage == "" || stage == "complete" {
			return // untemplated (solo), or already through every gate
		}
		if d.cfg.dryRun {
			// Status is read-only; Advance rewrites the project file.
			st, err := gates.Status(d.w, d.cfg.project)
			if err != nil || st.Complete {
				return
			}
			if unmet := unmetChecks(st.Checks); len(unmet) > 0 {
				d.logf("  stage: %s holds at %q — %d gate check(s) unmet (first: %s)", d.cfg.project, stage, len(unmet), unmet[0])
			} else {
				d.logf("  stage: every gate check at %q passes — would advance", stage)
			}
			return
		}
		newStage, unmet, err := gates.Advance(d.w, d.cfg.project)
		if err != nil {
			d.logf("  stage: gates unreadable (%v) — leaving %s at %q", err, d.cfg.project, stage)
			return
		}
		if len(unmet) > 0 {
			why := unmet[0].Desc
			if unmet[0].Why != "" {
				why += " — " + unmet[0].Why
			}
			d.logf("  stage: %s holds at %q — %d gate check(s) unmet (first: %s)", d.cfg.project, stage, len(unmet), why)
			return
		}
		d.logf("  stage: every gate check at %q passed — advanced %s to %q", stage, d.cfg.project, newStage)
	}
}

// unmetChecks returns the descriptions of the failing checks, for the log.
func unmetChecks(checks []gates.Check) []string {
	var out []string
	for _, c := range checks {
		if !c.OK {
			out = append(out, c.Desc)
		}
	}
	return out
}

// buildRole resolves the role this cycle's BUILD phase spawns with. On an
// untemplated project — the common solo case — it is cfg.implRole verbatim and
// nothing else here runs, so the ungated loop is unchanged.
//
// On a phase-gated project the phase decides which role KINDS may act, and
// spawning a kind the phase does not admit is a guaranteed refusal, cycle after
// cycle (dacli 189). So the loop asks the same question spawn's phaseGate asks,
// and when the configured implementer has no work in this phase it looks for a
// roster role that does. A role with no declared kind is exempt from phase
// gating (phaseGate's own rule), and so is one this workspace does not define —
// both pass through untouched, and any refusal they earn is the same refusal
// they would have earned before.
func (d *driver) buildRole() string {
	ph, err := gates.PhaseFor(d.w, d.cfg.project)
	if err != nil || !ph.Gated {
		return d.cfg.implRole
	}
	role, ok := store.LoadRole(d.w, d.cfg.implRole)
	if !ok || role.Kind == "" || ph.AllowsKind(role.Kind) {
		return d.cfg.implRole
	}
	roles, err := store.LoadRoles(d.w)
	if err != nil {
		return d.cfg.implRole
	}
	if pick := pickRoleForPhase(roles, ph); pick != "" {
		d.logf("  phase %s has no work for %s (kind %s) — building with %s instead",
			ph.Name, d.cfg.implRole, role.Kind, pick)
		return pick
	}
	// Nothing on the roster fits. Falling through to the configured role keeps
	// the pre-existing behavior (a logged refusal) rather than inventing a
	// role; the log says why, because "spawn refused" alone does not.
	d.logf("  phase %s admits only %s and no role on the roster is one — the build spawn will be refused; add a role or advance the stage",
		ph.Name, strings.Join(ph.Allows, ", "))
	return d.cfg.implRole
}

// pickRoleForPhase chooses a roster role whose kind the phase admits, honoring
// the manifest's own `allow:` ORDER as the preference: a template that writes
// "allow: implementer, reviewer" is stating that the implementer is the one who
// should be doing the work there, with the reviewer as the fallback. Ties
// within a kind break by name, so the same roster always yields the same
// choice — a loop that picked a different builder each cycle would be
// unauditable.
func pickRoleForPhase(roles []team.Role, ph gates.Phase) string {
	for _, kind := range ph.Allows {
		var names []string
		for _, r := range roles {
			if r.Name != "" && r.Kind == kind {
				names = append(names, r.Name)
			}
		}
		if len(names) > 0 {
			sort.Strings(names)
			return names[0]
		}
	}
	return ""
}

// reviewPhase spawns a reviewer against the project's standing
// continuous-improvement task, whose charter is to file the single
// highest-value, evidence-based change as new work — never to implement it.
func (d *driver) reviewPhase(wave ...*store.Task) {
	ref, err := d.ensureImproveTask()
	if err != nil {
		d.logf("  review: could not seed the improvement task: %v", err)
		return
	}
	if len(wave) > 0 && !d.cfg.dryRun {
		if err := d.attachWaveReviewBrief(ref, wave); err != nil {
			d.logf("  review: could not attach the just-completed wave brief: %v", err)
			return
		}
	}
	reviewRuntime, reviewModel := "", ""
	if role, ok := store.LoadRole(d.w, d.cfg.reviewRole); ok {
		reviewRuntime, reviewModel = role.Runtime, role.ModelID()
	}
	reviewSource := "project stack"
	if d.cfg.reviewRoleExplicit {
		reviewSource = "explicit override"
	}
	d.logf("  review: %s audits and files the next improvement — runtime %s · model %s · source %s", d.cfg.reviewRole, clikit.OrDash(reviewRuntime), clikit.OrDash(reviewModel), reviewSource)
	spawn := []string{"spawn", "--task", ref, "--role", d.cfg.reviewRole}
	for _, harness := range d.cfg.allowedHarnesses {
		spawn = append(spawn, "--harness", harness)
	}
	if d.cfg.perCycleTok > 0 {
		spawn = append(spawn, "--max-tokens", fmt.Sprint(d.cfg.perCycleTok))
		if d.cfg.allowAdvisoryTokens {
			spawn = append(spawn, "--allow-advisory-tokens")
		}
	}
	spawn = d.contextExceptionArgs(spawn)
	if anchor, findErr := store.FindTask(d.w, ref); findErr == nil {
		spawn = append(spawn, "--timeout", fmt.Sprint(d.workerTimeout(anchor)))
	} else {
		spawn = append(spawn, "--timeout", fmt.Sprint(d.workerTimeout(nil)))
	}
	var result commandresult.Spawn
	var out string
	var runErr error
	if rr, ok := d.run.(resultRunner); ok {
		out, runErr = rr.runResult("review", &result, spawn...)
	} else {
		out, runErr = d.run.run("review", spawn...)
	}
	if runErr != nil {
		detail := reviewFailure(out, runErr, result)
		d.logf("    %s", detail)
		d.recordCycleFailure("review", runErr, detail)
		if policyLaunchRefusal(runErr) {
			d.phaseErr = clikit.Refusedf("%s", detail)
		}
		d.logf("    review produced NO new work this cycle — the backlog grows only here")
		return
	}
	if result.RunID != "" {
		d.logf("    review run: %s", result.RunID)
	}

	// The review phase's whole output is NEW TASKS, and the next cycle spawns
	// an implementer straight onto them. lint is the check that catches a
	// vague acceptance criterion before tokens are spent proving it was vague
	// — which is precisely what it was written for — and the loop had never
	// run it, so the unattended path was the one place its own quality gate
	// did not apply.
	d.lintFiledWork()
}

// attachWaveReviewBrief makes queued work visible to a reviewer whose checkout
// still points at trunk. Cycle 95 re-filed the wave's own fix because the
// branch had not landed yet; task status alone could not show the commit or
// linked issue that proved it was the same work (issue #668).
func (d *driver) attachWaveReviewBrief(anchorRef string, wave []*store.Task) error {
	snapshot, err := store.LoadTaskSnapshot(d.w)
	if err != nil {
		return err
	}
	anchor, err := snapshot.Find(anchorRef)
	if err != nil {
		return err
	}
	_, base, _ := d.anchorCharter()
	var b strings.Builder
	b.WriteString(base)
	b.WriteString("\n\nJust-completed wave (treat this as queued work when checking duplicates):\n")
	for _, prior := range wave {
		current, findErr := snapshot.Find(prior.ID)
		if findErr != nil {
			current = prior
		}
		branch := taskBranch(current)
		commit := "none"
		if tip, gitErr := d.git("rev-parse", "--verify", "--quiet", branch); gitErr == nil && strings.TrimSpace(tip) != "" {
			commit = strings.TrimSpace(tip)
		}
		pending := false
		for _, pendingBranch := range d.pendingLand {
			if pendingBranch == branch {
				pending = true
				break
			}
		}
		fmt.Fprintf(&b, "- task %s (%03d-%s); status=%s; branch=%s; commit=%s; linked_issue=%s; pending_pr_landing=%t\n",
			current.ID, current.Seq, current.Slug, current.Status, branch, commit, linkedIssue(current), pending)
	}
	anchor.Doc.SetSection("Context", b.String())
	return store.SaveTask(anchor)
}

func linkedIssue(t *store.Task) string {
	block, ok := t.Doc.Front.GetBlock("github")
	if !ok {
		return "none"
	}
	for _, line := range strings.Split(block, "\n") {
		key, value, found := strings.Cut(strings.TrimSpace(line), ":")
		if found && key == "issue" && strings.TrimSpace(value) != "" {
			return "#" + strings.TrimSpace(value)
		}
	}
	return "none"
}

// lintFiledWork reports ambiguity in the tasks the review phase just filed.
//
// Reported, never fatal. lint flags language, and language it flags is
// sometimes correct — refusing a cycle over a "should" would trade a wave of
// real work for a wording argument. What must not happen is an implementer
// discovering the ambiguity by burning a run on it.
func (d *driver) lintFiledWork() {
	out, err := d.run.run("lint", "lint", "--project", d.cfg.project)
	body := strings.TrimSpace(out)
	if body == "" {
		return
	}
	// lint exits non-zero when it finds something, which is the case worth
	// surfacing rather than swallowing as a failed phase.
	major := 0
	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(line, "major") {
			major++
		}
	}
	if err == nil && major == 0 {
		return
	}
	d.logf("  lint: %d major ambiguity finding(s) in the backlog — an implementer spawned onto one of these will discover it the expensive way", major)
	for i, line := range strings.Split(body, "\n") {
		if i >= 5 {
			d.logf("    … `dacli lint --project %s` for the rest", d.cfg.project)
			break
		}
		if strings.TrimSpace(line) != "" {
			d.logf("    %s", line)
		}
	}
}

// The two review-phase anchor charters. Both carry
// store.ContinuousImprovementMarker so IsLoopAnchor keeps excluding them from
// the ready frontier — an anchor is a standing prompt, never implementer work —
// and they differ only in the suffix, which is how ensureImproveTask tells them
// apart and reuses the right one across cycles.
const (
	evidenceAnchorTitle = store.ContinuousImprovementMarker + ": file the single highest-value evidence-based change"
	specAnchorTitle     = store.ContinuousImprovementMarker + ": decompose the goal into a dependency-ordered backlog"
)

// ensureImproveTask returns the ref of the standing improvement task for the
// project, creating it (open) if absent. The task is the review phase's anchor:
// an auditor is spawned against it every cycle and files fresh work. Which
// anchor is right depends on whether the project has anything to reason FROM —
// see anchorCharter.
func (d *driver) ensureImproveTask() (string, error) {
	title, context, accept := d.anchorCharter()
	for _, st := range []model.Status{model.StatusOpen, model.StatusActive} {
		ts, _ := store.ListTasks(d.w, d.cfg.project, st)
		for _, t := range ts {
			if t.IsLoopAnchor() && t.Title == title {
				return t.ID, nil
			}
		}
	}
	if d.cfg.dryRun {
		return "IMPROVE", nil // placeholder ref for the preview
	}
	t, err := store.CreateTask(d.w, "loop", d.cfg.project, title, store.TaskOpts{
		Priority: "should",
		Context:  context,
		Accept:   accept,
		Estimate: "1,2,3",
	})
	if err != nil {
		return "", err
	}
	return t.ID, nil
}

// anchorCharter picks which standing anchor this cycle's review phase runs
// against.
//
// The evidence-based anchor is the original and stays the default: find the ONE
// highest-value improvement grounded in a failing test, a reviewer finding, or a
// real defect, and do NOT invent speculative work. That charter is exactly right
// for a live codebase — and a dead end for a greenfield repo. With no code and
// no backlog there is no evidence to point at, so the auditor correctly files
// nothing, and the loop prints "backlog empty — no evidence-based work; idling
// rather than inventing work" forever. A project with a Goal and zero tasks
// never starts (dacli 190).
//
// So a project that has not been decomposed yet gets the opposite charter: turn
// the stated intent — Goal, Constraints, Out of scope, Success criteria — into a
// dependency-ordered backlog. That anchor IS licensed to invent the work,
// because writing down what a stated goal requires is not speculation; it is the
// planning step the evidence-based anchor presupposes somebody already did.
func (d *driver) anchorCharter() (title, context string, accept []string) {
	if d.preImplementation() {
		return specAnchorTitle,
			fmt.Sprintf("Standing anchor for the autonomous review phase on a project that has NO backlog and NO code yet. There is nothing to survey, so — unlike the evidence-based anchor — you ARE licensed to invent the work here, from the project's own stated intent and from nothing else. Read `dacli project show %s` and decompose its Goal, Constraints, Out of scope, and Success criteria into the smallest set of concrete, buildable tasks that would actually satisfy them. File each with `dacli task add <title> --project %s --accept <criterion>` and REAL acceptance criteria — a task whose acceptance is \"it works\" is as empty as TBD. Order them: `--depends-on <ref>` for work that genuinely cannot start until another task is done, `--parent <ref>` for a step that is part of a larger one. Every task must trace to a line of the Goal or the Success criteria; anything you cannot trace is scope you invented, and belongs in Out of scope instead. Do NOT implement anything here.", d.cfg.project, d.cfg.project),
			[]string{
				"Filed a dependency-ordered set of tasks derived from the project's Goal and Success criteria",
				"Every filed task carries concrete acceptance criteria",
				"Did not implement any change in this task",
			}
	}
	return evidenceAnchorTitle,
		fmt.Sprintf("Standing anchor for the autonomous review phase. Survey the code, tests, CI, and open findings; identify the ONE highest-value improvement grounded in evidence (a failing test, a reviewer finding, a real defect). Before filing, run `dacli task list --project %s --status open` and `dacli task list --project %s --status active` to check whether the backlog already queues it — a prior cycle may have filed the same issue under different wording. `dacli task add` refuses (exit 3) a title that scores as a near-duplicate of existing work, so pick real, distinct scope rather than re-filing and re-running with --force. If the audit finds no distinct task after those duplicate checks, that is an honest result: record a finding naming what you audited and the open/active work that already covers it, then finish this anchor without filing placeholder work. Otherwise file the distinct task with concrete acceptance criteria. Do NOT implement anything here, and do NOT invent speculative work.", d.cfg.project, d.cfg.project),
		[]string{
			"Evidenced exactly one outcome: filed a distinct task grounded in an observed defect, finding, or failing check; or recorded a reviewer finding that the audit found no distinct task after checking open and active work for duplicates",
			"Did not implement any change in this task",
		}
}

// preImplementation reports whether the project has nothing an evidence-based
// reviewer could stand on. Both halves matter:
//
//   - No filed work at all. The anchors themselves do not count — they are
//     standing prompts, not backlog. One real task means somebody already
//     decomposed the goal, and re-decomposing it would duplicate their work.
//   - No source in the repository. An empty backlog over a REAL codebase still
//     has evidence to survey (tests, defects, findings), and that project keeps
//     the original charter — this must not change what a working repo does.
func (d *driver) preImplementation() bool {
	tasks, err := store.ListTasks(d.w, d.cfg.project, "")
	if err != nil {
		return false // an unreadable backlog is not evidence of an empty one
	}
	for _, t := range tasks {
		if !t.IsLoopAnchor() {
			return false
		}
	}
	return !d.repoHasCode()
}

// repoHasCode reports whether the repository carries anything an evidence-based
// reviewer could actually audit. Tracked files only: an untracked scratch file
// is not the project. The workspace record (.dacli) is excluded because it is
// the loop's own bookkeeping, and prose is excluded because a repo holding a
// README and a licence has a description of software, not software. On any git
// error this answers TRUE — an unmeasurable repo keeps the pre-existing
// evidence-based anchor rather than being handed a licence to invent work.
func (d *driver) repoHasCode() bool {
	out, err := d.git("ls-files")
	if err != nil {
		return true
	}
	for _, f := range strings.Split(out, "\n") {
		f = strings.TrimSpace(f)
		if f == "" || strings.HasPrefix(f, workspace.Dir+"/") {
			continue
		}
		switch strings.ToLower(filepath.Ext(f)) {
		case ".md", ".txt", "":
			continue
		}
		return true
	}
	return false
}

// readyFrontier evaluates the ONE readiness predicate (store.ReadyFrontier)
// over the project. The rule used to be reimplemented here, with a comment
// claiming it mirrored `dacli next` while differing from it on dep types, ref
// resolution, unresolvable refs and candidate status — so the loop could
// silently refuse work `next` was recommending (dacli 240). The rule and its
// four judgement calls now live in store; this is just the workspace read.
func resolveStopFile(w *workspace.Workspace, v string) string {
	if v == "" {
		return filepath.Join(w.Root, ".dacli", "STOP")
	}
	if filepath.IsAbs(v) {
		return v
	}
	return filepath.Join(w.Root, v)
}

func dryTag(dry bool) string {
	if dry {
		return " · DRY-RUN"
	}
	return ""
}

// resolveLoopLanding uses the same entity-layer precedence as integrate/ship,
// while a journaled policy keeps one bounded run stable across process-level
// checkpoints. Feature isolation prevents importing either landing slice.
func resolveLoopLanding(w *workspace.Workspace, project string, f *clikit.Flags, journal cycleJournal) (model.LandingPolicy, bool, error) {
	p, err := store.LoadProject(w, project)
	if err != nil {
		return model.LandingPolicy{}, false, err
	}
	configured := p.Landing
	// Issue #801: a recovery journal is durable state, but it is not timeless
	// authority. When `start` has persisted a newer project operating policy, a
	// bare loop must not silently resurrect contradictory landing settings from
	// an older run. An explicit loop override is the audited recovery path.
	if journal.Landing.Mode != "" && !f.Bool("pr") && !f.Bool("no-pr") && len(f.All("into")) == 0 {
		if profile, profileErr := loadProfile(w, project); profileErr == nil {
			expected := p.Landing
			switch profile.Landing.Mode {
			case "project", "":
			case "pr":
				expected.Mode = model.LandingPR
			case "local":
				expected.Mode = model.LandingLocal
			}
			if profile.Landing.ProtectedBranch != "" {
				expected.Base = profile.Landing.ProtectedBranch
			}
			if expected != journal.Landing {
				return model.LandingPolicy{}, false, clikit.Refusedf("stale loop journal landing policy %+v contradicts persisted project profile %+v — inspect %s, then pass an explicit --pr/--no-pr and --into override or remove the stale journal after confirming no PR is in flight", journal.Landing, expected, journalFile(w, project))
			}
		}
	}
	if journal.Landing.Mode != "" {
		configured = journal.Landing
	}
	var override model.LandingOverride
	if f.Bool("pr") && f.Bool("no-pr") {
		return model.LandingPolicy{}, false, clikit.Usagef("use either --pr or --no-pr, not both")
	}
	if f.Bool("pr") || f.Bool("no-pr") {
		mode := model.LandingPR
		if f.Bool("no-pr") {
			mode = model.LandingLocal
		}
		override.Mode = &mode
	}
	if len(f.All("into")) > 0 {
		base := f.Get("into")
		override.Base = &base
	}
	effective, explicit, err := model.ResolveLanding(configured, override)
	if err != nil {
		return model.LandingPolicy{}, explicit, clikit.Usagef("%v", err)
	}
	if override.Mode == nil && override.Base == nil && journal.Landing.Mode != "" {
		explicit = journal.LandingExplicit
	}
	return effective, explicit, nil
}

// hasOriginRemote is the package-level twin of driver.hasOrigin, for the
// config path that runs before a driver exists.
func hasOriginRemote(root string) bool {
	out, err := gitx.Run(root, "remote")
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

// reportWorkspaceHealth runs doctor's checks once per cycle and surfaces
// anything it finds. It shells to the same binary every other phase uses, so
// the loop sees exactly what an operator running `dacli doctor` would.
func (d *driver) reportWorkspaceHealth() {
	out, err := d.run.run("doctor", "doctor")
	if err != nil {
		// doctor exits non-zero when it FINDS something, which is the case we
		// care about — surface its report rather than swallowing it as a
		// failed phase.
		if s := strings.TrimSpace(out); s != "" {
			d.logf("  workspace health:")
			for _, line := range strings.Split(s, "\n") {
				d.logf("    %s", line)
			}
		}
		return
	}
	if s := strings.TrimSpace(out); s != "" && !strings.Contains(s, "no anti-patterns") {
		d.logf("  workspace health: %s", clikit.FirstLine(s))
	}
}

// sizeUnestimated spawns the roster's estimator on any task in the batch that
// carries no three-point estimate, and says so when it cannot.
//
// This is the gap both wrong halves of the loop audit were groping at. The
// loop already routes by capacity and already ranks by critical path — but
// both read t.Estimate(), and both fall back silently when it is missing: the
// task lands on the fallback role regardless of size, and the whole wave
// reverts to MoSCoW order while still printing as if the critical path were
// in play. Sizing is the input those features were built to consume.
//
// It uses the roster's own estimator role rather than inventing numbers here:
// a three-point estimate is a judgment about the codebase, which is exactly
// what that role exists to make. With no estimator in the roster there is
// nothing honest to do but name the degradation.
func (d *driver) sizeUnestimated(batch []*store.Task) {
	var unsized []*store.Task
	for _, t := range batch {
		if _, ok := t.Estimate(); !ok {
			unsized = append(unsized, t)
		}
	}
	if len(unsized) == 0 {
		return
	}
	if _, ok := store.LoadRole(d.w, estimatorRole); !ok {
		d.logf("  %d task(s) have no estimate — capacity routing and critical-path order both degrade; add an `%s` role, or size them with `dacli task estimate <ref> --estimate o,m,p`",
			len(unsized), estimatorRole)
		return
	}
	for _, t := range unsized {
		ref := t.ID
		d.logf("  sizing %s with the %s role (unsized tasks lose routing and critical-path order)", ref, estimatorRole)
		if out, err := d.run.run("estimate", "spawn", "--task", ref, "--role", estimatorRole); err != nil {
			// Never fatal: an unsized task still builds, just on the fallback
			// role and in MoSCoW order — the pre-existing behavior.
			d.logf("    could not size %s: %s", ref, clikit.FirstLine(out))
		}
	}
}

// estimatorRole is the roster role that sizes an open task. Named once so the
// lookup and the advice can never disagree about what to add.
const estimatorRole = "estimator"

// dropRemoteBranch deletes a task branch from origin after its PR closed
// unmerged.
//
// Left behind, the ref is indistinguishable from work in flight to anything
// that only asks whether it exists — which is exactly what stillPending used
// to do, holding the record push indefinitely on the evidence of a branch
// nobody was going to merge. Deleting it is safe precisely because the PR is
// closed: the work is either abandoned or already elsewhere.
//
// Best-effort. A protected branch, a revoked token or an offline remote must
// never fail a cycle over cleanup; the local clear above is what makes the
// retry fresh, and this only stops the stale ref from misleading a later read.
func (d *driver) dropRemoteBranch(branch string) {
	if !d.hasOrigin() || d.cfg.dryRun {
		return
	}
	if out, err := gitx.RunNetwork(d.w.Root, "push", "origin", "--delete", "--", branch); err != nil {
		d.logf("    could not delete origin/%s (left in place): %s", branch, clikit.FirstLine(out))
	}
}

// reviewFailure turns a failed review spawn into a line that names what
// ACTUALLY went wrong.
//
// The original reported `spawn refused/failed: <FirstLine(out)>`, and the
// first line of a spawn's output is its SUCCESS banner — so a review agent
// that ran for five minutes and was killed on a timeout was reported as
//
//	spawn refused/failed: spawning a-go-auditor-yn0a9b on cc for 303-… (run 01KZPAKYFN)
//
// which names the spawn that worked and says nothing about the failure. A
// policy refusal and a timeout kill were indistinguishable from the loop's
// output, and only `dacli runs list` showed the truth: "outcome: stalled ·
// exit: signal: killed · elapsed: 5m0.022s" (dacli 333).
//
// Deliberately NOT reported: a derived "timeout" figure. The loop knows the
// token budget it passed, not the wall-clock limit the runtime applied, and
// printing the former labelled as the latter trades one misleading message
// for another. The run id is printed instead, because the run record is the
// thing that actually knows.
func reviewFailure(out string, err error, result commandresult.Spawn) string {
	if result.RunID != "" {
		where := fmt.Sprintf(" — `dacli runs list` and `dacli logs %s` for the outcome", clikit.Short(result.RunID, 10))
		// It ran and did not finish. That is a different operator problem from
		// a spawn the gate never let through, and the error — not the banner —
		// is the half that says which.
		return fmt.Sprintf("review spawn started but did not complete: %v%s", err, where)
	}
	// No typed run result: the spawn never got far enough to mint a run, so the
	// output is the refusal and is the more actionable of the two.
	if msg := strings.TrimSpace(out); msg != "" {
		return fmt.Sprintf("review spawn refused: %s", msg)
	}
	return fmt.Sprintf("review spawn failed: %v", err)
}

// reportStillUnsized names the structural mismatch that the thrash guard's
// "no net progress" hides: a task nothing sized, about to meet a role that
// refuses unsized work.
//
// The reporter watched fourteen consecutive no-progress cycles before the
// guard tripped, and had to notice a per-spawn refusal buried above the rollup
// to learn that every task in the backlog was unspawnable. That is one line's
// worth of difference between a five-minute fix and reading a redirected log
// (issue #430).
func (d *driver) reportStillUnsized(batch []*store.Task) {
	var stuck []string
	snapshot, snapshotErr := store.LoadTaskSnapshot(d.w)
	for _, t := range batch {
		if snapshotErr != nil {
			break
		}
		fresh, err := snapshot.Find(fmt.Sprintf("%03d", t.Seq))
		if err != nil {
			continue
		}
		if _, ok := fresh.Estimate(); !ok {
			stuck = append(stuck, fmt.Sprintf("%03d", fresh.Seq))
		}
	}
	if len(stuck) == 0 {
		return
	}
	d.logf("  %d task(s) are STILL unsized after the sizing step (%s) — every capacity-capped role refuses unsized work, so this cycle cannot build them. Size them by hand (`dacli task estimate <ref> --estimate o,m,p --force`) or add an `%s` role.",
		len(stuck), strings.Join(stuck, ", "), estimatorRole)
}

// contextExceptionArgs forwards only an explicitly selected policy exception.
// Execution records the sources and override; sandbox verification still runs.
func (d *driver) contextExceptionArgs(args []string) []string {
	if d.cfg.allowUserConfig {
		return append(args, "--allow-user-config")
	}
	return args
}
