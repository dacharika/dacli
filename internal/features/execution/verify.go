// The verification panel (RUNTIMES.md § 10): the strongest argument for
// supporting many runtimes. A finding confirmed by two different-vendor
// models is meaningfully stronger evidence than three samples of one model,
// because the failure modes are uncorrelated.
package execution

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mlnomadpy/dacli/internal/agentid"
	"github.com/mlnomadpy/dacli/internal/brief"
	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/eventlog"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/procmon"
	"github.com/mlnomadpy/dacli/internal/prompts"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/team"
	"github.com/mlnomadpy/dacli/internal/ulid"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

func init() {
	Commands = append(Commands, clikit.Command{
		Path: "verify", Brief: "Adversarial panel: one refuter per runtime; tally derived from the log", Usage: "dacli verify --task <ref> --panel rt1,rt2[,rt3] [--claim text] [--require N] [--grant ro|rw] [--budget N] [--timeout sec] [--cooperative]", Run: cmdVerify,
	})
}

func cmdVerify(ctx *clikit.Ctx, args []string) error {
	w, id, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	f, _ := clikit.ParseFlags(args)
	if err := f.Reject("task", "panel", "claim", "require", "require-context-independent", "grant", "brief-tokens", "budget", "timeout", "cooperative", "allow-user-config"); err != nil {
		return err
	}
	taskRef := f.Get("task")
	panel := strings.Split(f.Get("panel"), ",")
	if taskRef == "" || f.Get("panel") == "" {
		return clikit.Usagef("usage: dacli verify --task <ref> --panel rt1,rt2[,rt3] [--claim text] [--require N] [--grant ro|rw] [--budget N] [--timeout sec] [--cooperative]")
	}
	t, err := store.FindTask(w, taskRef)
	if err != nil {
		return err
	}

	claim := f.Get("claim")
	if claim == "" {
		claim = latestFinding(w, t)
	}
	if claim == "" {
		return clikit.Usagef("nothing to verify: task %03d has no findings — pass --claim", t.Seq)
	}

	// Majority by default: 2-of-3, 2-of-2, 1-of-1.
	require := len(panel)/2 + 1
	if n, err := f.Int("require", 0); err != nil {
		return err
	} else if n > 0 {
		require = n
	}
	timeout := 300
	if n, err := f.Int("timeout", 0); err != nil {
		return err
	} else if n > 0 {
		timeout = n
	}
	budget, err := f.IntAliased(0, "brief-tokens", "budget")
	if err != nil {
		return err
	}
	grant := model.Grant(clikit.OrDash(f.Get("grant"), string(model.GrantRO)))
	override := f.Bool("cooperative") || f.Bool("allow-user-config")
	if f.Bool("require-context-independent") {
		seen := map[string]string{}
		for _, name := range panel {
			rt, loadErr := loadVerifyRuntime(w, strings.TrimSpace(name))
			if loadErr != nil {
				return loadErr
			}
			signature := store.ContextSummary(rt)
			if previous := seen[signature]; previous != "" {
				return clikit.Refusedf("verification runtimes %s and %s share context provenance %q", previous, rt.Name, signature)
			}
			seen[signature] = rt.Name
		}
	}

	// The diversity warning: a panel drawn from one runtime looks like
	// verification and is really repetition.
	unique := map[string]bool{}
	for _, p := range panel {
		unique[strings.TrimSpace(p)] = true
	}
	if len(unique) < 2 {
		fmt.Fprintln(ctx.Stderr, "warning: single-runtime panel — a single point of failure wearing several hats; prefer different vendors")
	}

	type seat struct {
		runtime, childID, verdict, why string
	}
	seats := make([]seat, 0, len(panel))

	for _, rtName := range panel {
		rtName = strings.TrimSpace(rtName)
		rt, err := loadVerifyRuntime(w, rtName)
		if err != nil {
			return err
		}
		contextMismatches, contextSources := contextIssues(rt, team.Role{}, false, override, ctx.Cwd, currentEnvNames())
		var contextRefused []string
		for _, issue := range contextMismatches {
			if issue.refuse {
				contextRefused = append(contextRefused, issue.message)
			} else {
				fmt.Fprintf(ctx.Stderr, "warning: allowing external context: %s\n", issue.message)
			}
		}
		if len(contextRefused) > 0 {
			return clikit.Refusedf("%s", strings.Join(contextRefused, "; "))
		}
		sandboxArgs, err := sandboxFor(ctx, rt, grant, f.Bool("cooperative"))
		if err != nil {
			return err
		}
		childID, token, err := agentid.Spawn(w, id, "verifier", grant)
		if err != nil {
			return err
		}

		b, err := brief.Assemble(w, taskRef, brief.Options{Budget: budget})
		if err != nil {
			return err
		}
		preamble, err := protocolPreamble(w, childID, grant, t)
		if err != nil {
			return err
		}
		refute, err := refutePrompt(w, t, claim)
		if err != nil {
			return err
		}
		contract, err := prompts.AutonomousContract(w.PromptsDir())
		if err != nil {
			return err
		}
		prompt := b.Render() + "\n" + contract.Text + preamble + "\n" + refute

		runID := ulid.New()
		runDir := w.RunDir(runID)
		if err := os.MkdirAll(runDir, 0o755); err != nil {
			return err
		}
		record := openRunRecord(runDir, ctx.Stderr)
		if err := record.critical("brief.md", prompt); err != nil {
			return err
		}
		// Record the seat in the SAME OrDash canonical band form cmdSpawn uses:
		// role is fixed "verifier" and verify resolves no model tier, so model is
		// the OrDash empty sentinel "-". The verify_panel_seat key is preserved so
		// runRecords can tell this is a CHECK, not an implementation actual, and
		// keep it out of the task's calibration band (see runRecords).
		invocation := fmt.Sprintf("run: %s\nverify_panel_seat: %s\ntask: %s\nchild: %s\nrole: %s\nmodel: %s\nruntime: %s\nclaim: %s\n",
			runID, rt.Name, t.ID, childID, "verifier", clikit.OrDash(""), rt.Name, claim)
		invocation += contextInvocation(team.Role{}, false, override, contextSources)
		provenance, err := promptInvocation(w.PromptsDir(), prompt)
		if err != nil {
			return err
		}
		invocation += provenance
		if err := record.critical("invocation.txt", invocation); err != nil {
			return err
		}

		fmt.Fprintf(ctx.Stderr, "panel seat %s: %s\n", rt.Name, childID)
		var procWriteErr error
		onStart := func(pid, pgid int) {
			rec := procmon.Record{
				RunID: runID, Child: childID, Task: t.ID, Runtime: rt.Name,
				PID: pid, PGID: pgid, PIDStart: pidStart(pid), Started: time.Now(),
			}
			procWriteErr = procmon.WriteRecord(filepath.Join(runDir, "proc.txt"), rec)
			if procWriteErr != nil {
				terminateRecordedTree(rec, 3*time.Second)
			}
		}
		elapsed, _, runErr := execRuntime(w.Root, filepath.Join(runDir, "transcript.log"), rt, prompt, token, sandboxArgs, timeout, false, onStart)
		if procWriteErr != nil {
			return fmt.Errorf("record critical run artifact proc.txt: %w", procWriteErr)
		}

		// The verdict is DERIVED from the log — same rule as shortcut uses:
		// the tally is recomputed from events, never stored as an integer
		// nobody can audit.
		verdict, why := verdictFor(w, childID)
		if err := record.critical("outcome.md", fmt.Sprintf("outcome: %s\nelapsed: %s\nexit: %s\n", verdict, elapsed, clikit.ErrStr(runErr))); err != nil {
			return err
		}
		// Record this seat's verdict as a queryable event ABOUT the task, not
		// only in the run's outcome file. A comment carrying the verify-verdict:
		// convention keeps the record queryable (and postable to a PR via
		// `dacli pr --with-verdicts`) without minting a durable finding note that
		// would re-enter every sibling brief. Best-effort: a failed record must
		// not abort a panel that already ran.
		if _, err := eventlog.Append(w, childID, model.EventComment, t.ID, "", VerdictRecord(rt.Name, childID, claim, verdict, why)); err != nil {
			fmt.Fprintf(ctx.Stderr, "note: verdict not recorded for %s (%v)\n", childID, err)
		}
		seats = append(seats, seat{rt.Name, childID, verdict, why})
	}

	confirmed := 0
	for _, s := range seats {
		if s.verdict == "confirmed" {
			confirmed++
		}
		line := fmt.Sprintf("%-14s %-12s %s", s.runtime, s.childID, s.verdict)
		if s.why != "" {
			line += " — " + s.why
		}
		fmt.Fprintln(ctx.Stdout, line)
	}
	fmt.Fprintf(ctx.Stdout, "confirmed %d/%d (required %d)\n", confirmed, len(seats), require)

	// D3: persist the panel's verdict onto the graded finding NOTE, so the
	// trust grade rides with the finding into every sibling brief BEFORE a
	// child acts on it — not only into this run's outcome. The claim under test
	// is the finding's title, which GradeFinding matches. A pending-event
	// finding has no note yet, so grading is best-effort: a miss is reported,
	// not fatal (the run's verdict already printed above).
	trust := "refuted"
	if confirmed >= require {
		trust = "confirmed"
	}
	if gid, err := store.GradeFinding(w, t.Project, claim, trust); err == nil {
		fmt.Fprintf(ctx.Stderr, "graded finding %s: trust=%s\n", gid, trust)
	} else {
		fmt.Fprintf(ctx.Stderr, "note: no finding note graded (%v) — verdict recorded on the run only\n", err)
	}

	if confirmed < require {
		// A killed claim is a RESULT, reported operationally (exit 1): the
		// verification worked, the claim did not survive it.
		return fmt.Errorf("claim KILLED by the panel — treat it as unestablished until re-derived")
	}
	fmt.Fprintln(ctx.Stdout, "claim SURVIVES the panel")
	return nil
}

func loadVerifyRuntime(w *workspace.Workspace, name string) (store.Runtime, error) {
	rt, err := store.LoadRuntime(w, name)
	if err != nil {
		return store.Runtime{}, err
	}
	path, err := exec.LookPath(rt.Binary)
	if err != nil {
		return store.Runtime{}, fmt.Errorf("runtime %s: binary %q not on PATH", rt.Name, rt.Binary)
	}
	return store.HydrateRuntimeROProbe(w, rt, path), nil
}

// VerdictMarker prefixes a comment event that records a verify-panel verdict,
// so the record is queryable and postable to a PR distinctly from ordinary
// task comments. The vcs slice mirrors this string when it renders the verdicts
// into a PR review (they are separate packages, so the convention — not an
// import — is the contract).
const VerdictMarker = "verify-verdict:"

// VerdictRecord formats one panel seat's verdict as a comment-event body. The
// shape is stable so `dacli pr --with-verdicts` can render it back into a PR
// review; it is exported (and pure) so the body assembly is unit-testable
// without a live gh call.
func VerdictRecord(runtime, childID, claim, verdict, why string) string {
	line := fmt.Sprintf("%s %s — %s (%s) on claim: %s", VerdictMarker, verdict, runtime, childID, claim)
	if strings.TrimSpace(why) != "" {
		line += " — " + strings.TrimSpace(why)
	}
	return line
}

// latestFinding returns the newest finding about the task, from events or
// notes, as the default claim under test.
func latestFinding(w *workspace.Workspace, t *store.Task) string {
	events, _ := eventlog.List(w, eventlog.Query{About: t.ID, Kinds: []model.EventKind{model.EventFinding}, Limit: 1})
	if len(events) > 0 {
		return strings.SplitN(events[0].Body, "\n", 2)[0]
	}
	notes, _ := store.ListNotes(w, t.Project, model.NoteFinding)
	for i := len(notes) - 1; i >= 0; i-- {
		if about, _ := notes[i].Front.Get("about"); strings.Contains(about, t.ID) || strings.Contains(about, fmt.Sprintf("%03d", t.Seq)) {
			for _, s := range notes[i].Sections {
				if s.Level == 1 {
					return s.Title
				}
			}
		}
	}
	return ""
}

// verdictFor reads a panelist's verdict from its finding events. No verdict
// is counted as no confirmation — a silent verifier confirms nothing.
func verdictFor(w *workspace.Workspace, childID string) (verdict, why string) {
	events, _ := eventlog.List(w, eventlog.Query{Actor: childID, Kinds: []model.EventKind{model.EventFinding}})
	for _, e := range events {
		first := strings.ToLower(strings.SplitN(e.Body, "\n", 2)[0])
		if i := strings.Index(first, "verdict: confirmed"); i >= 0 {
			return "confirmed", strings.TrimSpace(strings.TrimPrefix(first[i:], "verdict: confirmed —"))
		}
		if i := strings.Index(first, "verdict: refuted"); i >= 0 {
			return "refuted", strings.TrimSpace(strings.TrimPrefix(first[i:], "verdict: refuted —"))
		}
	}
	return "no-verdict", "panelist reported nothing — counts as unconfirmed"
}

func refutePrompt(w *workspace.Workspace, t *store.Task, claim string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		exe = "dacli"
	}
	return prompts.Render(w.PromptsDir(), "verify_refute", map[string]any{
		"Claim":   claim,
		"Exe":     exe,
		"Project": t.Project,
		"Ref":     fmt.Sprintf("%03d", t.Seq),
	})
}
