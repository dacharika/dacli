// Package insight is the read-only-views slice: status, scheduling views
// over the SPM engines, quality checks, anti-pattern detection, and the
// standup roll-up. Nothing here mutates the workspace.
package insight

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/mlnomadpy/dacli/internal/agentid"
	"github.com/mlnomadpy/dacli/internal/application"
	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/eventlog"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/spm"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/team"
)

var Commands = []clikit.Command{
	{Path: "overview", Brief: "Bounded workspace summary: projects, task/WIP counts, activity, and ready-now work", JSON: true, Usage: "dacli overview", Run: cmdOverview},
	{Path: "status", Brief: "Tree-wide or project-scoped state in one screen", JSON: true, Usage: "dacli status [--project slug]", Run: cmdStatus},
	{Path: "metrics", Brief: "Stable scenario metrics with sample counts", JSON: true, Usage: "dacli metrics [--project slug] [--since DUR] [--name NAME]", Run: cmdMetrics},
	{Path: "lint", Brief: "Format, INVEST, requirements-quality, and ambiguity checks", Usage: "dacli lint [<task-ref>] [--project slug]", Run: cmdLint},
	{Path: "next", Brief: "What to work on now: MoSCoW, then critical path (--parallel N)", JSON: true, Usage: "dacli next [--project slug] [--parallel N] [--critical-path]", Run: cmdNext},
	{Path: "estimate", Brief: "PERT three-point estimate widened by the Cone of Uncertainty", Usage: "dacli estimate <task-ref>", Run: cmdEstimate},
	{Path: "critical-path", Brief: "CPM: full schedule with slack; star marks the critical path", JSON: true, Usage: "dacli critical-path [--project slug]", Run: cmdCriticalPath},
	{Path: "wbs", Brief: "Work breakdown tree (task add --parent builds it)", Usage: "dacli wbs [--project slug]", Run: cmdWBS},
	{Path: "burndown", Brief: "Points remaining vs done, per-day completions", Usage: "dacli burndown [--project slug]", Run: cmdBurndown},
	{Path: "velocity", Brief: "Completions per active day (time proxy until usage reporting)", Usage: "dacli velocity", Run: cmdVelocity},
	{Path: "calibrate", Brief: "Te vs actuals: the empirical multiplier by size band (P2)", Usage: "dacli calibrate", Run: cmdCalibrate},
	{Path: "taint", Brief: "Blast radius of a suspect source over event/note origins (P4)", Mutates: true, Usage: "dacli taint <origin>   (e.g. file:cron/settle.go, external:someuser, or just file: for all)", Run: cmdTaint},
	{Path: "doctor", Brief: "Detect management anti-patterns in tasks, risks, and the log", JSON: true, Usage: "dacli doctor", Run: cmdDoctor},
	{Path: "standup", Brief: "Per-agent roll-up: done, doing, impediments — derived, never filed", Usage: "dacli standup", Run: cmdStandup},
}

func cmdStatus(ctx *clikit.Ctx, args []string) error {
	f, ferr := clikit.ParseFlags(args)
	if ferr != nil {
		return ferr
	} else if err := f.Reject("project"); err != nil {
		return err
	}
	project := f.Get("project")
	w, id, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	r, err := application.NewCooperativeContext(w, id)
	if err != nil {
		return err
	}
	result, err := application.Status(r, project)
	if err != nil {
		return err
	}
	if ctx.JSON {
		return clikit.EmitJSON(ctx, result)
	}
	pal := clikit.NewPalette(ctx)
	for _, p := range result.Projects {
		slug := pal.Bold(fmt.Sprintf("%-16s", p.Slug))
		fmt.Fprintf(ctx.Stdout, "%s open:%s active:%s blocked:%s done:%s implemented-unlanded:%s  %s\n", slug, pal.Dim(fmt.Sprint(p.Open)), pal.Yellow(fmt.Sprint(p.Active)), pal.Red(fmt.Sprint(p.Blocked)), pal.Green(fmt.Sprint(p.Done)), pal.Yellow(fmt.Sprint(p.ImplementedUnlanded)), p.Title)
	}
	if result.PendingEvents > 0 {
		fmt.Fprintf(ctx.Stdout, "%s\n", pal.Yellow(fmt.Sprintf("pending events: %d (run `dacli sync` as the owner to materialize)", result.PendingEvents)))
	}

	return nil
}

// cmdLint applies the asymmetric scope policy from SPM.md: titles and
// acceptance at moderate-and-above, bodies at major only.
func cmdLint(ctx *clikit.Ctx, args []string) error {
	w, _, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	f, _ := clikit.ParseFlags(args)
	if err := f.Reject("project"); err != nil {
		return err
	}
	var tasks []*store.Task
	if len(f.Pos) > 0 {
		t, err := store.FindTask(w, f.Pos[0])
		if err != nil {
			return err
		}
		tasks = []*store.Task{t}
	} else {
		allTasks, err := store.ListTasks(w, f.Get("project"), "")
		if err != nil {
			return err
		}
		// Lint only actionable tasks (open, active, blocked), never done ones.
		for _, t := range allTasks {
			if t.Status != model.StatusDone {
				tasks = append(tasks, t)
			}
		}
	}

	total := 0
	for _, t := range tasks {
		report := func(where string, finds []spm.Finding) {
			for _, fd := range finds {
				total++
				fmt.Fprintf(ctx.Stdout, "%03d-%s %s: %s\n", t.Seq, t.Slug, where, fd)
			}
		}
		report("title", spm.Scan(t.Title, spm.Options{}))
		for _, box := range t.Acceptance() {
			report("acceptance", spm.Scan(box.Text, spm.Options{}))
		}
		for _, s := range t.Doc.Sections {
			if s.Level > 1 && !strings.EqualFold(s.Title, "Acceptance") && !strings.EqualFold(s.Title, "Log") {
				report("body", spm.Scan(s.Content, spm.Options{MinSeverity: spm.SevMajor}))
			}
		}
		if t.Status != model.StatusDone && len(t.Acceptance()) == 0 {
			total++
			fmt.Fprintf(ctx.Stdout, "%03d-%s INVEST: no acceptance criteria — the agent cannot know when to stop\n", t.Seq, t.Slug)
		}
	}
	if total == 0 {
		fmt.Fprintln(ctx.Stdout, "clean")
	}
	return nil
}

// cmdNext: MoSCoW first, then the critical path — which tasks to spawn
// children on FIRST; fanning out onto slack tasks while the critical path
// idles is the default agent failure.
func cmdNext(ctx *clikit.Ctx, args []string) error {
	w, _, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	pal := clikit.NewPalette(ctx)
	f, _ := clikit.ParseFlags(args)
	if err := f.Reject("parallel", "project", "critical-path"); err != nil {
		return err
	}
	limit, err := f.Int("parallel", 3)
	if err != nil {
		return err
	}

	project := f.Get("project")
	allTasks, err := store.ListTasks(w, "", "")
	if err != nil {
		return err
	}
	tasks := projectTasks(allTasks, project)
	// byRef/openIDs/open build the CPM SCHEDULING set — every unfinished,
	// unblocked task, including work already in flight, because in-flight work
	// still occupies the schedule. Which of them may be RECOMMENDED is a
	// different and narrower question, answered once by store.ReadyFrontier
	// below. Done-ness no longer needs a map here: the frontier owns it.
	byRef := map[string]*store.Task{}
	openIDs := map[string]bool{}
	var open []*store.Task
	for _, t := range tasks {
		for _, ref := range []string{t.ID, strings.TrimPrefix(t.ID, "t-"), t.Slug, fmt.Sprintf("%03d", t.Seq)} {
			byRef[ref] = t
		}
		if t.Status != model.StatusDone && t.Status != model.StatusBlocked && !t.IsLoopAnchor() && !t.IsAggregate() {
			// The standing continuous-improvement task is the loop's
			// review-phase anchor, not implementer work — the readiness
			// predicate excludes it from the build frontier, so it must not
			// occupy the schedule this view is drawn from either.
			open = append(open, t)
			openIDs[t.ID] = true
		}
	}
	if len(open) == 0 {
		if ctx.JSON {
			return clikit.EmitJSON(ctx, map[string]any{"schema": "next/v1", "project": project, "ordering": "empty", "recommendations": []any{}, "blocked": []any{}, "problems": []any{}})
		}
		fmt.Fprintln(ctx.Stdout, "nothing open")
		return nil
	}

	// Readiness is store.ReadyFrontier's call, not this command's. `next` and
	// the loop's BUILD phase each used to carry their own predicate and
	// disagreed on four points, so `next` could recommend a task the loop
	// would never pick up (dacli 240). `open` above stays wider than the
	// frontier on purpose: it is the CPM SCHEDULING set, and work already in
	// flight still occupies the schedule even though it is not free to
	// recommend.
	frontier := store.ReadyFrontierForProject(allTasks, project)

	// A dep ref that resolves to nothing is a data fault, and the task is held
	// back until it is fixed — say which ref, or the task just vanishes from
	// this list for no visible reason.
	for _, line := range frontier.ProblemLines() {
		if !ctx.JSON {
			fmt.Fprintf(ctx.Stderr, "note: %s — not schedulable until the ref resolves\n", line)
		}
	}

	// CPM needs durations; degrade to MoSCoW-then-sequence when estimates
	// are missing, and SAY SO.
	slack := map[string]float64{}
	haveCPM := true
	var nodes []spm.Node
	var edges []spm.Edge
	for _, t := range open {
		est, ok := t.Estimate()
		if !ok {
			haveCPM = false
			break
		}
		nodes = append(nodes, spm.Node{ID: t.ID, Duration: est.Expected()})
		for _, d := range t.Deps() {
			// Only edge to a dep that is itself a scheduled node. A done dep
			// imposes no scheduling constraint; a BLOCKED dep is not a node
			// (both readouts exclude blocked), and edging to it would make
			// ComputeCPM fail "edge references unknown task". Readiness against
			// a blocked dep is still enforced by ready() below.
			if dep, ok := byRef[d.Ref]; ok && openIDs[dep.ID] {
				edges = append(edges, spm.Edge{From: dep.ID, To: t.ID, Type: spm.DepType(d.Type)})
			}
		}
	}
	if haveCPM {
		net, err := spm.ComputeCPM(nodes, edges)
		if err != nil {
			return fmt.Errorf("dependency graph: %w", err)
		}
		for id, s := range net.Schedules {
			slack[id] = s.Slack
		}
	} else {
		if !ctx.JSON {
			fmt.Fprintln(ctx.Stderr, "note: estimates missing — falling back to MoSCoW-then-sequence order, no critical path")
		}
	}

	cands := frontier.Ready
	if len(cands) == 0 {
		if ctx.JSON {
			blocked := make([]string, 0, len(frontier.Blocked))
			for _, task := range frontier.Blocked {
				blocked = append(blocked, task.ID)
			}
			return clikit.EmitJSON(ctx, map[string]any{"schema": "next/v1", "project": project, "ordering": map[bool]string{true: "moscow-critical-path", false: "moscow-sequence"}[haveCPM], "recommendations": []any{}, "blocked": blocked, "problems": frontier.ProblemLines()})
		}
		// Distinguish the three ways a frontier empties out: a real dependency
		// wait, a broken ref, and "everything open is already being worked on".
		// Collapsing them into one line is how a data fault reads as normal.
		switch {
		case len(frontier.Problems) > 0:
			fmt.Fprintln(ctx.Stdout, "no task is ready: see the unresolvable dependency note(s) above")
		case len(frontier.Blocked) > 0:
			fmt.Fprintln(ctx.Stdout, "no task is ready: everything open is waiting on a dependency")
		default:
			fmt.Fprintln(ctx.Stdout, "no task is ready: nothing is open and free to start")
		}
		return nil
	}
	sort.SliceStable(cands, func(i, j int) bool {
		pi, pj := model.Priority(cands[i].Priority()).Rank(), model.Priority(cands[j].Priority()).Rank()
		if pi != pj {
			return pi < pj
		}
		if haveCPM && slack[cands[i].ID] != slack[cands[j].ID] {
			return slack[cands[i].ID] < slack[cands[j].ID]
		}
		return cands[i].Seq < cands[j].Seq
	})

	// Never recommend a could while a must is ready.
	top := model.Priority(cands[0].Priority()).Rank()
	n := 0
	type recommendation struct {
		Rank       int      `json:"rank"`
		ID         string   `json:"id"`
		Seq        int      `json:"seq"`
		Slug       string   `json:"slug"`
		Project    string   `json:"project"`
		Priority   string   `json:"priority,omitempty"`
		Slack      *float64 `json:"slack,omitempty"`
		Critical   bool     `json:"critical"`
		NextAction string   `json:"next_action"`
	}
	recommendations := []recommendation{}
	for _, t := range cands {
		if model.Priority(t.Priority()).Rank() != top || n >= limit {
			break
		}
		line := fmt.Sprintf("%d. %03d-%s", n+1, t.Seq, t.Slug)
		if p := t.Priority(); p != "" {
			line += "  " + colorPriority(pal, p)
		}
		if haveCPM {
			if slack[t.ID] == 0 {
				line += "  · " + pal.Bold(pal.Cyan("critical path"))
			} else {
				line += fmt.Sprintf("  · slack %.1f", slack[t.ID])
			}
		}
		var slackValue *float64
		if haveCPM {
			value := slack[t.ID]
			slackValue = &value
		}
		recommendations = append(recommendations, recommendation{Rank: n + 1, ID: t.ID, Seq: t.Seq, Slug: t.Slug, Project: t.Project, Priority: t.Priority(), Slack: slackValue, Critical: haveCPM && slack[t.ID] == 0, NextAction: "assign an eligible implementation role"})
		if !ctx.JSON {
			fmt.Fprintln(ctx.Stdout, line)
		}
		n++
	}
	if ctx.JSON {
		blocked := make([]string, 0, len(frontier.Blocked))
		for _, task := range frontier.Blocked {
			blocked = append(blocked, task.ID)
		}
		return clikit.EmitJSON(ctx, struct {
			Schema          string           `json:"schema"`
			Project         string           `json:"project,omitempty"`
			Ordering        string           `json:"ordering"`
			Recommendations []recommendation `json:"recommendations"`
			Blocked         []string         `json:"blocked"`
			Problems        []string         `json:"problems"`
		}{"next/v1", project, map[bool]string{true: "moscow-critical-path", false: "moscow-sequence"}[haveCPM], recommendations, blocked, frontier.ProblemLines()})
	}

	// Scope-matched lessons (D2): a cross-project lesson whose topic overlaps a
	// task we just suggested, and which points at a role, is a HINT about who to
	// spawn on it — surfaced so the operator does not have to re-derive from the
	// log what a prior lesson already learned. It never assigns (axiom 3: the
	// model still chooses); it only annotates the tasks shown above.
	if n > 0 {
		if lessons := store.WorkspaceLessons(w, ""); len(lessons) > 0 {
			roles, _ := store.LoadRoles(w)
			for _, t := range cands[:n] {
				for _, l := range lessons {
					if !lessonMatchesTask(l, t) {
						continue
					}
					if role := roleForLesson(roles, l); role != "" {
						fmt.Fprintf(ctx.Stdout, "   ↳ lesson %q (%s) applies to %03d-%s — consider role %s\n",
							l.Title, l.Project, t.Seq, t.Slug, role)
					} else {
						fmt.Fprintf(ctx.Stdout, "   ↳ lesson %q (%s) applies to %03d-%s\n",
							l.Title, l.Project, t.Seq, t.Slug)
					}
				}
			}
		}
	}

	// A task that outranks everything shown but is held back still has to be
	// accounted for, or the list reads as if the must was ignored. Tasks held
	// back by a BROKEN ref were already named above with the reason that
	// actually helps — don't re-report them here as a dependency wait.
	badRef := map[string]bool{}
	for _, p := range frontier.Problems {
		badRef[p.Task.ID] = true
	}
	for _, t := range frontier.Blocked {
		if !badRef[t.ID] && model.Priority(t.Priority()).Rank() < top {
			fmt.Fprintf(ctx.Stderr, "note: %03d-%s (%s) outranks these but is waiting on a dependency\n", t.Seq, t.Slug, t.Priority())
		}
	}
	return nil
}

// colorPriority tints a MoSCoW priority string for a human terminal: must
// red (the thing everything else waits on), should yellow, could/won't dim.
// Off-terminal this is a no-op (clikit.Palette), so `next`'s plain-text
// contract for agents and tests is unchanged.
func colorPriority(pal clikit.Palette, p string) string {
	switch p {
	case "must":
		return pal.Red(p)
	case "should":
		return pal.Yellow(p)
	default:
		return pal.Dim(p)
	}
}

// minLessonOverlap is how many DISTINCT significant words a lesson and a task
// must share before the lesson is hinted against the task. One is not overlap:
// every lesson body is a paragraph, so a single common word (task 248's bug)
// lands somewhere in nearly every lesson and painted every lesson onto every
// task. Two keeps the hint honest without demanding a full topic model — a
// spurious hint still costs only one ignorable line, a missed one a
// re-derivation, so the bar stays low, just not zero.
const minLessonOverlap = 2

// lessonMatchesTask reports MEANINGFUL topical overlap between a cross-project
// lesson and a task: at least minLessonOverlap distinct significant words shared
// between the lesson's title/body and the task's title/slug. The match is on
// the lesson's significant-WORD set, not a substring scan of its raw text, so a
// task word "port" no longer matches "report"/"import"/"export" buried in a
// lesson — the old strings.Contains(hay, w) form did, which is half of why the
// old single-word rule matched everything (task 248).
func lessonMatchesTask(l store.Lesson, t *store.Task) bool {
	lessonWords := significantWords(l.Title + " " + l.Body)
	shared := 0
	for w := range significantWords(t.Title + " " + strings.ReplaceAll(t.Slug, "-", " ")) {
		if lessonWords[w] {
			shared++
			if shared >= minLessonOverlap {
				return true
			}
		}
	}
	return false
}

// roleForLesson maps a lesson to the role it points at: first a SCOPE match —
// a path the lesson cites that falls inside a role's declared boundary — then a
// name match if the lesson names a role outright. Empty when the lesson maps to
// no role, in which case the hint still fires without a suggestion.
func roleForLesson(roles []team.Role, l store.Lesson) string {
	for _, tok := range pathTokens(l.Body + " " + l.Title) {
		for _, r := range roles {
			if len(r.Scope) > 0 && r.InScope(tok) {
				return r.Name
			}
		}
	}
	text := strings.ToLower(l.Title + " " + l.Body)
	for _, r := range roles {
		if r.Name != "" && strings.Contains(text, strings.ToLower(r.Name)) {
			return r.Name
		}
	}
	return ""
}

// pathTokens pulls path-like tokens out of free text. It delegates to
// store.PathTokens so this hinter and the routing tie-break (Task.PathHints)
// share one definition of "a path" — a silent divergence between two copies is
// a bug this codebase has already been bitten by (dacli 238).
func pathTokens(s string) []string {
	return store.PathTokens(s)
}

// significantWords lowercases s and returns its content words (length ≥ 4, not
// a stopword) as a set — the crude topical fingerprint lessonMatchesTask uses.
func significantWords(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if len(w) >= 4 && !lessonStopWords[w] {
			out[w] = true
		}
	}
	return out
}

// lessonStopWords are common tokens that would match almost any lesson and so
// carry no topical signal.
var lessonStopWords = map[string]bool{
	"task": true, "with": true, "this": true, "that": true, "from": true,
	"when": true, "then": true, "than": true, "code": true, "test": true,
	"tests": true, "into": true, "over": true, "your": true, "have": true,
	"here": true, "does": true, "must": true, "only": true, "also": true,
	"they": true, "them": true, "will": true, "each": true, "same": true,
}

func cmdEstimate(ctx *clikit.Ctx, args []string) error {
	w, _, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	f, _ := clikit.ParseFlags(args)
	// Reject unknown flags: a typo used to be dropped silently and the
	// command ran as if the caller had meant the default.
	if err := f.Reject(); err != nil {
		return err
	}
	if len(f.Pos) == 0 {
		return clikit.Usagef("usage: dacli estimate <task-ref>")
	}
	t, err := store.FindTask(w, f.Pos[0])
	if err != nil {
		return err
	}
	tp, ok := t.Estimate()
	if !ok {
		return fmt.Errorf("%03d-%s has no three-point estimate; add one with --estimate o,m,p — a scalar hides the risk", t.Seq, t.Slug)
	}
	stage := spm.StageElicitation
	if p, err := store.LoadProject(w, t.Project); err == nil && p.Stage != "" {
		stage = spm.Stage(p.Stage)
	}
	e, err := spm.Evaluate(tp, stage)
	if err != nil {
		return err
	}
	fmt.Fprintf(ctx.Stdout, "%03d-%s\n  three-point: %g / %g / %g\n  Te %.1f · σ %.1f · 1σ range %.1f–%.1f\n  cone (%s): %.1f–%.1f\n",
		t.Seq, t.Slug, tp.Optimistic, tp.Probable, tp.Pessimistic,
		e.Expected, e.Sigma, e.Sigma1Low, e.Sigma1High, stage, e.ConeLow, e.ConeHigh)

	// D1 inversion: if this task's agent band (role×model×runtime) has enough
	// history, the empirical distribution IS the estimate and the human PERT
	// above is demoted to the prior. The band comes from the task's own run
	// record; a task never spawned has no band and this stays silent. One walk
	// of RunsDir backs both the band lookup and the samples.
	cal := store.LoadCalibration(w)
	if band, ok := cal.TaskBand(t.ID); ok {
		te := tp.Expected()
		// F1: tokens are the real unit. Collect the band's wall-clock ratios and,
		// separately, the ratios of samples that carry real usage. When the band
		// has enough token history, the token-per-point distribution IS the
		// estimate and wall-clock is demoted to the fallback — the SAME preference
		// `dacli calibrate` prints, so the two readouts never disagree on a band.
		var wallRs, tokRs []float64
		for _, s := range cal.Samples {
			if s.Band != band {
				continue
			}
			wallRs = append(wallRs, s.Ratio())
			if s.HasTokens() {
				tokRs = append(tokRs, s.TokenRatio())
			}
		}
		switch {
		case len(tokRs) >= 10:
			med, p10, p90 := spm.Median(tokRs), percentile(tokRs, 10), percentile(tokRs, 90)
			fmt.Fprintf(ctx.Stdout,
				"  empirical band %s (n=%d, tokens): %.0f median tok/point · p10–p90 %.0f–%.0f\n"+
					"    → estimate %.0f output tokens (p10–p90 %.0f–%.0f) — THIS is the estimate; the PERT above is the prior\n"+
					"    (tokens/point is the real unit from runtime usage; wall-clock is the fallback)\n",
				band.String(), len(tokRs), med, p10, p90, med*te, p10*te, p90*te)
		case len(wallRs) >= 10:
			med, p10, p90 := spm.Median(wallRs), percentile(wallRs, 10), percentile(wallRs, 90)
			fmt.Fprintf(ctx.Stdout,
				"  empirical band %s (n=%d): ×%.2f median · p10–p90 ×%.2f–×%.2f hours/point\n"+
					"    → estimate %.1f h (p10–p90 %.1f–%.1f h) — THIS is the estimate; the PERT above is the prior\n"+
					"    (actuals are wall-clock claim→completion, a time proxy; opt a runtime into usage_format: stream-json for tokens)\n",
				band.String(), len(wallRs), med, p10, p90, med*te, p10*te, p90*te)
		}
	}
	return nil
}

func cmdCriticalPath(ctx *clikit.Ctx, args []string) error {
	w, _, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	f, _ := clikit.ParseFlags(args)
	if err := f.Reject("project"); err != nil {
		return err
	}
	project := f.Get("project")
	if project == "" && len(f.Pos) > 0 {
		project = f.Pos[0]
	}
	allTasks, err := store.ListTasks(w, "", "")
	if err != nil {
		return err
	}
	tasks := projectTasks(allTasks, project)
	if project != "" {
		idx := store.NewTaskIndex(allTasks)
		var externalBlockers []string
		for _, task := range tasks {
			for _, depRef := range task.Deps() {
				if !store.DepBlocksStart(depRef) {
					continue
				}
				dep, findErr := idx.Find(depRef.Ref)
				if findErr != nil {
					return fmt.Errorf("task %s dependency %q cannot be resolved for critical-path scheduling: %w", task.ID, depRef.Ref, findErr)
				}
				if dep.Project != project && dep.Status != model.StatusDone {
					externalBlockers = append(externalBlockers, fmt.Sprintf("%s waits on %s/%s (%s)", task.ID, dep.Project, dep.ID, dep.Status))
				}
			}
		}
		if len(externalBlockers) > 0 {
			return fmt.Errorf("project critical path has unfinished external prerequisites whose duration is outside this schedule: %s", strings.Join(externalBlockers, "; "))
		}
	}
	byRef := map[string]*store.Task{}
	done := map[string]bool{}
	openIDs := map[string]bool{}
	var open []*store.Task
	for _, t := range tasks {
		for _, ref := range []string{t.ID, strings.TrimPrefix(t.ID, "t-"), t.Slug, fmt.Sprintf("%03d", t.Seq)} {
			byRef[ref] = t
		}
		// Exclude blocked from the schedule, exactly as `dacli next` does, so
		// the two readouts agree on what is runnable and neither stars a
		// blocked task as the thing to spawn on first.
		if t.Status == model.StatusDone {
			done[t.ID] = true
		} else if t.Status != model.StatusBlocked && !t.IsAggregate() {
			open = append(open, t)
			openIDs[t.ID] = true
		}
	}
	var nodes []spm.Node
	var edges []spm.Edge
	labels := map[string]string{}
	// Collect EVERY unsized task before refusing. Returning on the first one
	// made acting on the refusal an N-round-trip loop — fix one, re-run, learn
	// about the next — when the whole list was already in hand. A refusal is
	// only useful if one reading of it is enough to act.
	var unsized []string
	for _, t := range open {
		if _, ok := t.Estimate(); !ok {
			unsized = append(unsized, fmt.Sprintf("%03d-%s", t.Seq, t.Slug))
		}
	}
	if len(unsized) > 0 {
		// Name the remedy, not just the fault — the standard doctor's findings
		// already hold (`dacli accept --force` is spelled out there). CPM
		// refuses rather than degrading because a schedule built on fabricated
		// durations reads as authoritative while being invented; `dacli next`
		// is the honest fallback and is named so the caller has somewhere to go.
		return fmt.Errorf("%d open task(s) have no estimate — CPM needs durations, so this command refuses rather than invent them: %s\nsize them with `dacli task estimate <ref> --estimate o,m,p`, or use `dacli next` which degrades to MoSCoW-then-sequence order",
			len(unsized), strings.Join(unsized, ", "))
	}
	for _, t := range open {
		est, _ := t.Estimate()
		nodes = append(nodes, spm.Node{ID: t.ID, Duration: est.Expected()})
		labels[t.ID] = fmt.Sprintf("%03d-%s", t.Seq, t.Slug)
		for _, d := range t.Deps() {
			// Edge only to a scheduled node — skip done and blocked deps so a
			// blocked predecessor never triggers "edge references unknown task".
			if dep, ok := byRef[d.Ref]; ok && openIDs[dep.ID] {
				edges = append(edges, spm.Edge{From: dep.ID, To: t.ID, Type: spm.DepType(d.Type)})
			}
		}
	}
	if len(nodes) == 0 {
		if ctx.JSON {
			return clikit.EmitJSON(ctx, map[string]any{"schema": "critical-path/v1", "project": project, "duration": 0, "tasks": []any{}})
		}
		fmt.Fprintln(ctx.Stdout, "nothing open")
		return nil
	}
	net, err := spm.ComputeCPM(nodes, edges)
	if err != nil {
		return err
	}
	if !ctx.JSON {
		fmt.Fprintf(ctx.Stdout, "project duration: %.1f (Te units)\n", net.Duration)
	}
	ordered := make([]spm.Schedule, 0, len(net.Schedules))
	for _, s := range net.Schedules {
		ordered = append(ordered, s)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].EarlyStart < ordered[j].EarlyStart })
	for _, s := range ordered {
		if ctx.JSON {
			continue
		}
		mark := " "
		if s.Critical {
			mark = "★"
		}
		fmt.Fprintf(ctx.Stdout, "%s %-30s Te %5.1f  ES %5.1f  EF %5.1f  slack %5.1f\n",
			mark, labels[s.ID], s.Duration, s.EarlyStart, s.EarlyFinish, s.Slack)
	}
	if ctx.JSON {
		type schedule struct {
			ID          string  `json:"id"`
			Label       string  `json:"label"`
			Duration    float64 `json:"duration"`
			EarlyStart  float64 `json:"early_start"`
			EarlyFinish float64 `json:"early_finish"`
			LateStart   float64 `json:"late_start"`
			LateFinish  float64 `json:"late_finish"`
			Slack       float64 `json:"slack"`
			Critical    bool    `json:"critical"`
		}
		out := make([]schedule, 0, len(ordered))
		for _, s := range ordered {
			out = append(out, schedule{ID: s.ID, Label: labels[s.ID], Duration: s.Duration, EarlyStart: s.EarlyStart, EarlyFinish: s.EarlyFinish, LateStart: s.LateStart, LateFinish: s.LateFinish, Slack: s.Slack, Critical: s.Critical})
		}
		return clikit.EmitJSON(ctx, struct {
			Schema   string     `json:"schema"`
			Project  string     `json:"project,omitempty"`
			Duration float64    `json:"duration"`
			Tasks    []schedule `json:"tasks"`
		}{"critical-path/v1", project, net.Duration, out})
	}
	fmt.Fprintln(ctx.Stdout, "★ = critical path: spawn children here first — slack tasks can wait")
	return nil
}

func projectTasks(tasks []*store.Task, project string) []*store.Task {
	if project == "" {
		return tasks
	}
	out := make([]*store.Task, 0, len(tasks))
	for _, task := range tasks {
		if task.Project == project {
			out = append(out, task)
		}
	}
	return out
}

func cmdWBS(ctx *clikit.Ctx, args []string) error {
	w, _, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	f, _ := clikit.ParseFlags(args)
	if err := f.Reject("project"); err != nil {
		return err
	}
	project := f.Get("project")
	if project == "" && len(f.Pos) > 0 {
		project = f.Pos[0]
	}
	tasks, err := store.ListTasks(w, project, "")
	if err != nil {
		return err
	}
	children := map[string][]*store.Task{}
	byID := map[string]*store.Task{}
	for _, t := range tasks {
		byID[t.ID] = t
	}
	for _, t := range tasks {
		parent := ""
		if p, ok := t.Doc.Front.Get("parent"); ok {
			parent = strings.TrimSuffix(strings.TrimPrefix(p, "[["), "]]")
		}
		if _, ok := byID[parent]; !ok {
			parent = "" // orphan parents render at root rather than vanishing
		}
		children[parent] = append(children[parent], t)
	}
	var render func(parent string, depth int)
	render = func(parent string, depth int) {
		for _, t := range children[parent] {
			est := ""
			if tp, ok := t.Estimate(); ok {
				est = fmt.Sprintf("  Te %.1f", tp.Expected())
			}
			progress := ""
			if t.IsAggregate() {
				p, progressErr := store.AggregateProgressFor(w, t)
				if progressErr != nil {
					progress = "  aggregate progress unavailable: " + progressErr.Error()
				} else {
					progress = fmt.Sprintf("  aggregate %d/%d", p.RequiredDone, p.Required)
				}
			}
			fmt.Fprintf(ctx.Stdout, "%s%03d-%s [%s]%s%s\n", strings.Repeat("  ", depth), t.Seq, t.Slug, t.Status, est, progress)
			render(t.ID, depth+1)
		}
	}
	render("", 0)
	return nil
}

func cmdBurndown(ctx *clikit.Ctx, args []string) error {
	w, _, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	f, _ := clikit.ParseFlags(args)
	if err := f.Reject("project"); err != nil {
		return err
	}
	tasks, err := store.ListTasks(w, f.Get("project"), "")
	if err != nil {
		return err
	}
	var doneP, remP float64
	unestimated := 0
	perDay := map[string]float64{}
	for _, t := range tasks {
		tp, ok := t.Estimate()
		if !ok {
			unestimated++
			continue
		}
		if t.Status == model.StatusDone {
			doneP += tp.Expected()
			if day, ok := completionDay(t); ok {
				perDay[day] += tp.Expected()
			}
		} else {
			remP += tp.Expected()
		}
	}
	fmt.Fprintf(ctx.Stdout, "remaining: %.1f points · done: %.1f points\n", remP, doneP)
	days := make([]string, 0, len(perDay))
	for d := range perDay {
		days = append(days, d)
	}
	sort.Strings(days)
	for _, d := range days {
		fmt.Fprintf(ctx.Stdout, "  %s  %5.1f done\n", d, perDay[d])
	}
	if unestimated > 0 {
		fmt.Fprintf(ctx.Stdout, "(%d task(s) without estimates are invisible here — that is a hole in the chart, not zero work)\n", unestimated)
	}
	return nil
}

func cmdVelocity(ctx *clikit.Ctx, args []string) error {
	// This command takes no flags, so ANY flag is a typo. An empty allowlist
	// rejects every one — without it a mistyped flag was dropped and the
	// command ran as if nothing were wrong.
	if f, ferr := clikit.ParseFlags(args); ferr != nil {
		return ferr
	} else if err := f.Reject(); err != nil {
		return err
	}
	w, _, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	tasks, err := store.ListTasks(w, "", "")
	if err != nil {
		return err
	}
	perDay := map[string]int{}
	for _, t := range tasks {
		if t.Status != model.StatusDone {
			continue
		}
		if day, ok := completionDay(t); ok {
			perDay[day]++
		}
	}
	if len(perDay) == 0 {
		fmt.Fprintln(ctx.Stdout, "no completions recorded yet")
		return nil
	}
	total := 0
	days := make([]string, 0, len(perDay))
	for d, n := range perDay {
		days = append(days, d)
		total += n
	}
	sort.Strings(days)
	for _, d := range days {
		fmt.Fprintf(ctx.Stdout, "%s  %d task(s)\n", d, perDay[d])
	}
	fmt.Fprintf(ctx.Stdout, "mean %.1f task(s)/active day over %d day(s)\n(time is a proxy — per-token velocity needs runtime usage reporting, which nothing here provides yet)\n",
		float64(total)/float64(len(days)), len(days))
	return nil
}

// completionDay extracts YYYY-MM-DD from the "completed by" Log stamp — the
// capture field paying rent.
func completionDay(t *store.Task) (string, bool) {
	s, ok := t.Doc.Section("Log")
	if !ok {
		return "", false
	}
	for _, line := range strings.Split(s.Content, "\n") {
		if strings.Contains(line, "completed by") {
			fields := strings.Fields(strings.TrimPrefix(strings.TrimSpace(line), "- "))
			if len(fields) > 0 && len(fields[0]) >= 10 {
				return fields[0][:10], true
			}
		}
	}
	return "", false
}

// cmdCalibrate is the P2 loop's readout: how wrong the estimates actually
// are, measured, not assumed. McConnell's cone becomes YOUR cone.
func cmdCalibrate(ctx *clikit.Ctx, args []string) error {
	// This command takes no flags, so ANY flag is a typo. An empty allowlist
	// rejects every one — without it a mistyped flag was dropped and the
	// command ran as if nothing were wrong.
	if f, ferr := clikit.ParseFlags(args); ferr != nil {
		return ferr
	} else if err := f.Reject(); err != nil {
		return err
	}
	w, _, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	samples := store.CalibrationSamples(w)
	if len(samples) == 0 {
		fmt.Fprintln(ctx.Stdout, "no history: calibration needs done tasks with both a three-point estimate and claim→completion stamps")
		return nil
	}

	band := func(te float64) string {
		switch {
		case te <= 3:
			return "small (≤3)"
		case te <= 8:
			return "medium (≤8)"
		default:
			return "large (>8)"
		}
	}
	byBand := map[string][]float64{}
	var all []float64
	for _, s := range samples {
		byBand[band(s.Te)] = append(byBand[band(s.Te)], s.Ratio())
		all = append(all, s.Ratio())
	}
	fmt.Fprintln(ctx.Stdout, "by size band:")
	for _, name := range []string{"small (≤3)", "medium (≤8)", "large (>8)"} {
		rs := byBand[name]
		if len(rs) == 0 {
			continue
		}
		// Per-band n-gate: only a band with n>=10 shows a calibrated p10–p90
		// range. A thinner band prints its median marked "provisional" and NO
		// range — a percentile spread over a handful of samples is confidence
		// theater, the exact failure the overall size gate below warns against.
		if len(rs) >= 10 {
			fmt.Fprintf(ctx.Stdout, "%-12s n=%-3d ×%.2f median  p10–p90 ×%.2f–×%.2f hours/point\n",
				name, len(rs), spm.Median(rs), percentile(rs, 10), percentile(rs, 90))
		} else {
			fmt.Fprintf(ctx.Stdout, "%-12s n=%-3d ×%.2f median hours/point  (provisional, n<10 — no calibrated range)\n",
				name, len(rs), spm.Median(rs))
		}
	}
	fmt.Fprintf(ctx.Stdout, "%-12s n=%-3d ×%.2f median hours/point\n", "overall", len(all), spm.Median(all))

	// Token bands, computed FIRST so the wall-clock agent-band section below can
	// see which bands the tokens already speak for. A band with n>=10 token
	// samples is authoritative in tokens (the real unit), so its wall-clock line
	// must NOT also claim to be the estimate — one authoritative unit per band,
	// never two contradictory ones (F1/criterion 2).
	tokenByAgent := map[string][]float64{}
	tokenSamples := 0
	for _, s := range samples {
		if !s.HasTokens() || s.Band.Empty() {
			continue
		}
		tokenByAgent[s.Band.String()] = append(tokenByAgent[s.Band.String()], s.TokenRatio())
		tokenSamples++
	}
	tokenAuthoritative := map[string]bool{}
	for name, rs := range tokenByAgent {
		if len(rs) >= 10 {
			tokenAuthoritative[name] = true
		}
	}

	// Agent bands — role × model × runtime. This is the D1 inversion: once a
	// band has n>=10 samples its empirical distribution is the authoritative
	// estimate, not a multiplier beside PERT. Samples with no run record joined
	// (Band.Empty) cannot be attributed to an agent, so they are size-band only.
	byAgent := map[string][]float64{}
	for _, s := range samples {
		if s.Band.Empty() {
			continue
		}
		byAgent[s.Band.String()] = append(byAgent[s.Band.String()], s.Ratio())
	}
	if len(byAgent) > 0 {
		fmt.Fprintln(ctx.Stdout, "\nby agent band (role/model/runtime):")
		names := make([]string, 0, len(byAgent))
		for name := range byAgent {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			rs := byAgent[name]
			// Per-band n-gate: a band with n<10 is provisional and prints its
			// median only — NO p10–p90 range. Printing "×0.03–×0.03" over n=1 as
			// if calibrated is confidence theater; only a band that clears the
			// n>=10 floor earns the range (and the AUTHORITATIVE claim).
			switch {
			case len(rs) >= 10 && tokenAuthoritative[name]:
				// Tokens already speak for this band below; wall-clock is the
				// fallback here, NOT a second "IS the estimate" line.
				fmt.Fprintf(ctx.Stdout, "%-28s n=%-3d ×%.2f median  p10–p90 ×%.2f–×%.2f hours/point  (fallback — tokens/point below IS the estimate)\n",
					name, len(rs), spm.Median(rs), percentile(rs, 10), percentile(rs, 90))
			case len(rs) >= 10:
				fmt.Fprintf(ctx.Stdout, "%-28s n=%-3d ×%.2f median  p10–p90 ×%.2f–×%.2f hours/point  ← AUTHORITATIVE (n≥10: this distribution IS the estimate)\n",
					name, len(rs), spm.Median(rs), percentile(rs, 10), percentile(rs, 90))
			default:
				fmt.Fprintf(ctx.Stdout, "%-28s n=%-3d ×%.2f median  (provisional, n<10 — no calibrated range)\n",
					name, len(rs), spm.Median(rs))
			}
		}
	} else {
		fmt.Fprintln(ctx.Stdout, "\nby agent band: no done task joins a run record yet (runs predate model-banding, or none recorded)")
	}

	// F1: token-per-point bands. When a band's completing runs used a
	// usage-reporting runtime, output tokens are the REAL unit and wall-clock is
	// demoted to the fallback for runs without usage. This is the caveat every
	// readout above has printed finally coming true: tokens, not a time proxy.
	// (tokenByAgent/tokenSamples were gathered above so the wall-clock section
	// could defer to them for the single authoritative line per band.)
	if len(tokenByAgent) > 0 {
		fmt.Fprintln(ctx.Stdout, "\nby agent band (tokens/point) — PREFERRED (real unit; wall-clock above is the fallback):")
		names := make([]string, 0, len(tokenByAgent))
		for name := range tokenByAgent {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			rs := tokenByAgent[name]
			if len(rs) >= 10 {
				fmt.Fprintf(ctx.Stdout, "%-28s n=%-3d %.0f median tok/point  p10–p90 %.0f–%.0f  ← AUTHORITATIVE (n≥10: tokens ARE the estimate)\n",
					name, len(rs), spm.Median(rs), percentile(rs, 10), percentile(rs, 90))
			} else {
				fmt.Fprintf(ctx.Stdout, "%-28s n=%-3d %.0f median tok/point  (provisional, n<10 — no calibrated range)\n",
					name, len(rs), spm.Median(rs))
			}
		}
	}

	if len(all) < 10 {
		fmt.Fprintf(ctx.Stdout, "insufficient history (n=%d < 10): briefs stay silent — a multiplier from anecdotes is confidence theater\n", len(all))
	} else {
		fmt.Fprintln(ctx.Stdout, "briefs now show the calibrated range beside PERT")
	}
	if tokenSamples > 0 {
		fmt.Fprintf(ctx.Stdout, "(tokens/point is the real unit, from runtime usage on %d sample(s); wall-clock claim→completion is the fallback for runs without usage)\n", tokenSamples)
	} else {
		fmt.Fprintln(ctx.Stdout, "(actuals are wall-clock claim→completion — a time PROXY until runtimes report token usage; opt a runtime in with usage_format: stream-json)")
	}
	return nil
}

// percentile returns the p-th (0..100) percentile of xs by linear
// interpolation on the sorted copy — the p10/p90 spread the calibration
// readout reports so a band's distribution, not just its median, is visible.
// Zero-length returns 0.
func percentile(xs []float64, p float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	if len(s) == 1 {
		return s[0]
	}
	rank := p / 100 * float64(len(s)-1)
	lo := int(rank)
	if lo >= len(s)-1 {
		return s[len(s)-1]
	}
	return s[lo] + (rank-float64(lo))*(s[lo+1]-s[lo])
}

// cmdTaint is the P4 blast-radius query: given a hostile source, which
// briefs consumed it. It does not fix injection — nothing does — but it
// makes the propagation auditable in seconds instead of an unbounded
// suspicion, which is the only honest posture the design claims.
func cmdTaint(ctx *clikit.Ctx, args []string) error {
	w, _, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	f, _ := clikit.ParseFlags(args)
	if err := f.Reject("origin"); err != nil {
		return err
	}
	source := f.Get("origin")
	if source == "" && len(f.Pos) > 0 {
		source = f.Pos[0]
	}
	if source == "" {
		return clikit.Usagef("usage: dacli taint <origin>   (e.g. file:cron/settle.go, external:someuser, or just file: for all)")
	}
	res, err := store.Taint(w, source)
	if err != nil {
		return err
	}
	if len(res.Hits) == 0 {
		fmt.Fprintf(ctx.Stdout, "no artifact carries origin %q — nothing derived from this source\n", source)
		return nil
	}
	for _, h := range res.Hits {
		loc := h.About
		if h.Project != "" {
			loc = h.Project + "/" + h.About
		}
		fmt.Fprintf(ctx.Stdout, "%-6s %-28s by %-14s origin=%s → %s\n", h.Kind, h.ID, h.Actor, h.Origin, loc)
	}
	exposed := res.ExposedBriefs(w)
	sort.Strings(exposed)
	scope := fmt.Sprintf("%d project(s)", len(res.Projects))
	if res.TreeWide {
		scope = "TREE-WIDE (a workspace-scoped hit reaches every project's briefs)"
	}
	fmt.Fprintf(ctx.Stdout, "\nblast radius: %d artifact(s), %s, %d brief(s) exposed\n",
		len(res.Hits), scope, len(exposed))
	if len(exposed) > 0 {
		fmt.Fprintf(ctx.Stdout, "exposed briefs: %s\n", strings.Join(exposed, ", "))
	}
	// Reviewer F4: origin is self-reported, so this is a floor. An artifact
	// whose author omitted --origin carries "agent" and is invisible here.
	fmt.Fprintln(ctx.Stdout, "this is a LOWER BOUND: only honestly-labeled provenance is traced — unlabeled artifacts are invisible.")
	fmt.Fprintln(ctx.Stdout, "(an audit, not a fix — review these briefs' consumers; injection prevention is unsolved, RUNTIMES § 18)")
	return nil
}

// cmdDoctor runs anti-pattern detectors over tasks, risks, and the event
// log. Informational: the point is visibility while the pattern is cheap.
func cmdDoctor(ctx *clikit.Ctx, args []string) error {
	// This command takes no flags, so ANY flag is a typo. An empty allowlist
	// rejects every one — without it a mistyped flag was dropped and the
	// command ran as if nothing were wrong.
	if f, ferr := clikit.ParseFlags(args); ferr != nil {
		return ferr
	} else if err := f.Reject(); err != nil {
		return err
	}
	w, _, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	pal := clikit.NewPalette(ctx)
	found := 0
	type doctorFinding struct {
		Pattern string `json:"pattern"`
		Detail  string `json:"detail"`
	}
	findingsJSON := []doctorFinding{}
	report := func(pattern, detail string) {
		found++
		findingsJSON = append(findingsJSON, doctorFinding{Pattern: pattern, Detail: detail})
		if ctx.JSON {
			return
		}
		label := pal.Yellow(fmt.Sprintf("%-22s", pattern+":")) // pad THEN color: see cmdStatus
		fmt.Fprintf(ctx.Stdout, "%s %s\n", label, detail)
	}

	tasks, _ := store.ListTasks(w, "", "")
	var mustsOpen, done, active int
	var lowerActive []string
	var brokenSpans []string
	var orphaned []string
	var recoveryEvidenceErrors []string
	for _, t := range tasks {
		switch t.Status {
		case model.StatusDone:
			done++
			// Data-integrity: a done task claimed (E3 spawn stamp) but never
			// stamped "completed by" has a broken calibration span — it can never
			// produce a sample. Name it so the drift that E1/E7 fixed can't hide.
			if store.LogHasStamp(t, "claimed by") && !store.LogHasStamp(t, "completed by") {
				brokenSpans = append(brokenSpans, fmt.Sprintf("%03d-%s", t.Seq, t.Slug))
			}
		case model.StatusActive:
			active++
			if model.Priority(t.Priority()).Rank() > 0 {
				lowerActive = append(lowerActive, fmt.Sprintf("%03d-%s(%s)", t.Seq, t.Slug, clikit.OrDash(t.Priority())))
			}
		case model.StatusOpen:
			if model.Priority(t.Priority()).Rank() == 0 && t.Priority() != "" {
				mustsOpen++
			}
		}
		// Orphan check: open/active work owned by a non-root agent that has no
		// live process is stuck — that agent will never run `sync` or `accept`
		// again, so a proposed close (or any further progress) sits pending
		// forever. `task takeover --force --reason` is root's audited recovery
		// path; name it here so the backlog doesn't silently rot behind a
		// finished agent.
		//
		// The loop anchor is exempt: it is a STANDING task owned by "loop"
		// (ensureImproveTask, orchestration.go), re-surveyed by a fresh auditor
		// every cycle rather than driven by one agent to completion. "loop" is
		// never a live process, so without this guard the anchor is flagged
		// orphaned on every run — noise that trains people to ignore doctor
		// (dacli 254). Reuse the shared IsLoopAnchor predicate (decision 112).
		if owner := t.Owner(); (t.Status == model.StatusOpen || t.Status == model.StatusActive) &&
			owner != "" && owner != agentid.RootID && !t.IsLoopAnchor() {
			leased, err := store.OwnerTaskHasRecoveryLease(w, owner, t.ID)
			if err != nil {
				recoveryEvidenceErrors = append(recoveryEvidenceErrors, fmt.Sprintf("%03d-%s: %v", t.Seq, t.Slug, err))
			} else if !leased {
				orphaned = append(orphaned, fmt.Sprintf("%03d-%s(owner %s)", t.Seq, t.Slug, owner))
			}
		}
	}

	if mustsOpen > 0 && len(lowerActive) > 0 {
		report("cart-before-the-horse", fmt.Sprintf("%d must task(s) sit open while lower-priority work is active: %s",
			mustsOpen, strings.Join(lowerActive, ", ")))
	}
	if active >= 3 && done == 0 {
		report("burning-across", fmt.Sprintf("%d tasks active, 0 done — finish before starting; redirect free agents to help", active))
	}
	if len(brokenSpans) > 0 {
		report("broken-calibration-span", fmt.Sprintf("%d done task(s) claimed but never stamped 'completed by' — calibration cannot size them: %s",
			len(brokenSpans), strings.Join(brokenSpans, ", ")))
	}
	if len(orphaned) > 0 {
		report("orphaned-task", fmt.Sprintf("%d task(s) have no owner process or transcript-active run — `dacli task takeover <ref> --force --reason \"owner exited; recovery reviewed\"` to recover: %s",
			len(orphaned), strings.Join(orphaned, ", ")))
	}
	if len(recoveryEvidenceErrors) > 0 {
		report("recovery-evidence", fmt.Sprintf("%d task(s) could not be classified safely; takeover will refuse until run evidence is readable: %s",
			len(recoveryEvidenceErrors), strings.Join(recoveryEvidenceErrors, ", ")))
	}
	// Data-integrity: a task file living in more than one status folder is the
	// duplicate-task drift that made FindTask fail with "ambiguous" on the same
	// task twice (026 lived in both open/ and done/). ListTasks now dedups it
	// away; name the paths so the drift stays visible instead of silent.
	if dups, _ := store.DuplicateTaskFiles(w); len(dups) > 0 {
		for _, d := range dups {
			report("duplicate-task-file", fmt.Sprintf("%03d-%s exists in %d status folders: %s",
				d.Seq, d.Slug, len(d.Paths), strings.Join(d.Paths, ", ")))
		}
	}
	// Data-integrity: two DIFFERENT tasks holding the same NNN is the scar a
	// cross-branch seq collision leaves once both branches merge (dacli 251) —
	// allocation now bars new ones, but a pre-existing pair is invisible until
	// `dacli <NNN>` fails "ambiguous" at the point of use. Name both so the owner
	// can renumber one, rather than leaving the reference silently broken.
	if cols, _ := store.CollidedSeqs(w); len(cols) > 0 {
		for _, c := range cols {
			report("collided-seq", fmt.Sprintf("seq %03d in project %s is claimed by %d different tasks: %s — renumber one so the ref resolves",
				c.Seq, c.Project, len(c.Slugs), strings.Join(c.Slugs, ", ")))
		}
	}
	// Data-integrity: a depends_on ref that resolves to no task (or to more
	// than one) is a typo with a scheduling consequence. The readiness
	// predicate holds such a task back rather than running work whose
	// prerequisite may not exist — the safe call, but one that would starve
	// the task forever if nothing ever said why. This is where "why" lives:
	// doctor already owns the workspace's data-integrity readout, and it is
	// the place a stalled backlog gets diagnosed (dacli 240).
	for _, p := range store.ReadyFrontier(tasks).Problems {
		report("unresolvable-dependency", fmt.Sprintf("%s — it can never become ready until the ref is corrected", p))
	}
	for _, candidate := range store.AggregateRepairCandidates(tasks) {
		report("ambiguous-aggregate-parent", fmt.Sprintf("%03d-%s is independently schedulable while %d direct children remain open; inspect immutable repair with `dacli task aggregate %s --dry-run` (ordinary hierarchy remains descriptive until explicit apply)", candidate.Parent.Seq, candidate.Parent.Slug, len(candidate.Children), candidate.Parent.ID))
	}
	// Data-integrity: a task whose frontmatter is gone still LISTS, because
	// status comes from its folder and seq/slug from its filename — so it
	// appears as a hollow row with no id, no title and no acceptance criteria,
	// and every list path carries on as if the workspace were healthy. That is
	// exactly how the CRLF and newline-injection bugs destroyed tasks in
	// silence: the damage was invisible until someone looked at the file. A
	// tool whose job is workspace integrity must not call this clean
	// (dacli 204).
	var hollow []string
	for _, t := range tasks {
		if t.ID == "" || strings.TrimSpace(t.Title) == "" {
			hollow = append(hollow, fmt.Sprintf("%03d-%s", t.Seq, t.Slug))
		}
	}
	if len(hollow) > 0 {
		report("corrupt-object", fmt.Sprintf("%d task file(s) lost their frontmatter — no id or title, so ownership, acceptance and event correlation are gone: %s",
			len(hollow), strings.Join(hollow, ", ")))
	}

	// The hollow check above can only see tasks that PARSED — it iterates the
	// listing. A file whose frontmatter is malformed outright (git conflict
	// markers are the realistic case in a tracked, agent-written .dacli) never
	// reaches the listing at all, so it was invisible to every reader including
	// this one, while its seq stayed invisible to the allocator that must not
	// reissue it. store records those; drain them here.
	if broken := store.BrokenTaskFiles(); len(broken) > 0 {
		var lines []string
		for _, b := range broken {
			lines = append(lines, fmt.Sprintf("%s (%v)", filepath.Base(b.Path), b.Err))
		}
		report("unparseable-task", fmt.Sprintf("%d task file(s) exist but do not parse, so they are missing from every list and their seq can be reissued — fix the frontmatter (a conflict marker is the usual cause): %s",
			len(broken), strings.Join(lines, ", ")))
	}

	// Count a finding while it lives SOLELY as a pending event; once the owner
	// syncs it (applied) it also exists as a NoteFinding counted below, so
	// counting applied events too would double-count every read-only reviewer's
	// finding (event now, synced note later). Gate on Pending so each finding is
	// counted exactly once — the same dedup contrib uses (see the contrib
	// decision note gating on !e.Applied).
	findings, _ := eventlog.List(w, eventlog.Query{Kinds: []model.EventKind{model.EventFinding}, Pending: true})
	noteFindings := 0
	if ps, _ := store.ListProjects(w); ps != nil {
		for _, p := range ps {
			ns, _ := store.ListNotes(w, p.Slug, model.NoteFinding)
			noteFindings += len(ns)
			risks, _ := store.ListRisks(w, p.Slug)
			for _, r := range risks {
				if r.Rank() == 1 && strings.TrimSpace(r.Action) == "" {
					report("unmanaged-risk", fmt.Sprintf("%s/%s is rank 1 with no action plan", p.Slug, r.Slug))
				}
			}
		}
	}
	if len(findings)+noteFindings >= 5 && done == 0 {
		report("analysis-paralysis", fmt.Sprintf("%d findings recorded, 0 tasks done — deliver something", len(findings)+noteFindings))
	}
	if qs, _ := eventlog.List(w, eventlog.Query{Kinds: []model.EventKind{model.EventHelp}, Pending: true}); len(qs) > 0 {
		report("unanswered-questions", fmt.Sprintf("%d question(s) open — the asking tasks are blocked until someone answers", len(qs)))
	}
	roles, _ := store.LoadRoles(w)
	for _, problem := range rosterProblems(w, roles) {
		report(problem.Kind, problem.Detail)
	}
	for _, r := range roles {
		if r.WIP > 0 {
			if n, _ := store.ActiveInRole(w, r.Name); n > r.WIP {
				report("wip-exceeded", fmt.Sprintf("role %s has %d active agents against a limit of %d", r.Name, n, r.WIP))
			}
		}
		// A role couples a grant (what the workspace permits) with a runtime
		// (what the process can actually do), and nothing else cross-checks them:
		// the role file reads fine on its own. When they disagree the spawn is
		// wasted before it starts — an rw grant on a runtime whose allowlist has
		// no write tool burns a run when the child cannot edit (dacli 250), and a
		// ro grant on a runtime with no read-only sandbox is refused outright
		// (§ 8). Name every such role so the coupling is visible without opening
		// two files by hand. A runtime the role names but the workspace has not
		// defined makes no checkable claim, so it is skipped rather than guessed.
		if r.Runtime != "" {
			if rt, err := store.LoadRuntime(w, r.Runtime); err == nil {
				// runtime doctor records its verdict against the exact adapter and
				// installed binary. LoadRuntime deliberately returns declaration-only
				// state, so hydrate that local evidence here just as spawn does before
				// deciding whether an ro grant is enforceable. A missing binary leaves
				// the verdict unknown and therefore safely reports the mismatch.
				if path, lookErr := exec.LookPath(rt.Binary); lookErr == nil {
					rt = store.HydrateRuntimeROProbe(w, rt, path)
				}
				grant := model.Grant(r.Grant)
				if grant == "" {
					grant = model.GrantRO // spawn's default when a role sets none
				}
				switch {
				case grant == model.GrantRW && !store.RuntimeWritable(rt):
					report("grant-runtime-mismatch", fmt.Sprintf("role %s declares grant rw but runtime %s grants no write tool — a spawn here burns a run when the child cannot edit; give it a write-capable runtime or correct the grant", r.Name, rt.Name))
				case grant == model.GrantRO && !store.RuntimeEnforcesRO(rt):
					report("grant-runtime-mismatch", fmt.Sprintf("role %s declares grant ro but runtime %s cannot enforce read-only — the spawn is refused unless --cooperative; give it a runtime with a read-only sandbox or correct the grant", r.Name, rt.Name))
				}
			}
		}
	}

	if ctx.JSON {
		return clikit.EmitJSON(ctx, struct {
			Schema   string          `json:"schema"`
			Healthy  bool            `json:"healthy"`
			Findings []doctorFinding `json:"findings"`
		}{Schema: "doctor/v1", Healthy: found == 0, Findings: findingsJSON})
	}
	if found == 0 {
		fmt.Fprintln(ctx.Stdout, pal.Green("no anti-patterns detected"))
	}
	return nil
}

// cmdStandup is derived entirely from the log and the tasks — no agent ever
// files a status report.
func cmdStandup(ctx *clikit.Ctx, args []string) error {
	// This command takes no flags, so ANY flag is a typo. An empty allowlist
	// rejects every one — without it a mistyped flag was dropped and the
	// command ran as if nothing were wrong.
	if f, ferr := clikit.ParseFlags(args); ferr != nil {
		return ferr
	} else if err := f.Reject(); err != nil {
		return err
	}
	w, _, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	tasks, _ := store.ListTasks(w, "", "")
	events, _ := eventlog.List(w, eventlog.Query{})

	type roll struct {
		doing, doneT, blocked []string
		events                int
	}
	rolls := map[string]*roll{}
	get := func(id string) *roll {
		if rolls[id] == nil {
			rolls[id] = &roll{}
		}
		return rolls[id]
	}
	for _, t := range tasks {
		if t.Owner() == "" {
			continue
		}
		label := fmt.Sprintf("%03d-%s", t.Seq, t.Slug)
		switch t.Status {
		case model.StatusActive:
			get(t.Owner()).doing = append(get(t.Owner()).doing, label)
		case model.StatusDone:
			get(t.Owner()).doneT = append(get(t.Owner()).doneT, label)
		case model.StatusBlocked:
			get(t.Owner()).blocked = append(get(t.Owner()).blocked, label)
		}
	}
	for _, e := range events {
		get(e.Actor).events++
	}

	ids := make([]string, 0, len(rolls))
	for id := range rolls {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		r := rolls[id]
		fmt.Fprintf(ctx.Stdout, "%s (%d events)\n", id, r.events)
		if len(r.doneT) > 0 {
			fmt.Fprintf(ctx.Stdout, "  done:        %s\n", strings.Join(r.doneT, ", "))
		}
		if len(r.doing) > 0 {
			fmt.Fprintf(ctx.Stdout, "  doing:       %s\n", strings.Join(r.doing, ", "))
		}
		if len(r.blocked) > 0 {
			fmt.Fprintf(ctx.Stdout, "  impediments: %s\n", strings.Join(r.blocked, ", "))
		}
	}
	return nil
}
