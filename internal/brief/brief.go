// Package brief is the I/O assembly service that builds the context document
// handed to a subagent from workspace entities and append-only records.
//
// This is the product. Everything else in dacli exists so that this function
// has something to slice. Sections are emitted in fixed priority order and
// trimmed from the BOTTOM under a budget, so the highest-value content is
// never what gets cut; every omission is announced inline, because an agent
// can only ask for what it knows is missing.
package brief

import (
	"fmt"
	"sort"
	"strings"

	"github.com/mlnomadpy/dacli/internal/eventlog"
	"github.com/mlnomadpy/dacli/internal/mdstore"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/prompts"
	"github.com/mlnomadpy/dacli/internal/shortcut"
	"github.com/mlnomadpy/dacli/internal/spm"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/team"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

// Options controls assembly.
type Options struct {
	Budget int // approximate token ceiling; 0 = unlimited

	// Role names the role the brief is being assembled for. When set, the
	// role's standing instructions (the body of its file) lead the brief —
	// HOW this agent works, before WHAT it is working on. Empty means an
	// unroled brief, which is the `dacli context <ref>` case.
	Role string
}

// Section is one emitted block. Order in the slice is priority order.
type Section struct {
	Title     string
	Content   string
	Droppable bool     // the task itself is never droppable
	Sources   []Source `json:"-"` // identities selected while rendering this retained section
}

// Source identifies a record selected for a rendered section. It is metadata,
// not parsed back out of rendered prose, which is untrusted contextual data.
type Source struct {
	Kind  string
	ID    string
	Path  string
	Trust string
}

// Brief is an assembled context document.
type Brief struct {
	TaskID   string
	Sections []Section
	Omitted  []string // announced omissions

	promptsDir string // workspace prompt-override dir, set by Assemble
}

// MillerCap bounds constraints and risks per brief. An agent handed 40
// constraints silently drops most of them, exactly as a human would; a brief
// is a working-memory budget, not an archive.
const MillerCap = 7

// View is one command-scoped, immutable read of every workspace input used by
// brief selection and rendering. It is intentionally not cached: a caller
// starts a new documented snapshot boundary by calling LoadView again after a
// mutation or before the next command/cycle. This makes freshness explicit and
// lets AssembleView remain filesystem-free.
type View struct {
	Task        *store.Task
	Project     *store.Project
	Tasks       []*store.Task
	Decisions   []*mdstore.Doc
	Findings    []*mdstore.Doc
	Risks       []*store.Risk
	Glossary    *mdstore.Doc
	Lessons     []store.Lesson
	Events      []*eventlog.Event
	Roles       []team.Role
	Shortcuts   []shortcut.Shortcut
	Calibration []store.CalibSample
	promptsDir  string
}

// LoadView establishes a fresh command/cycle boundary. Every collection is
// loaded once; the returned value never observes later sibling writes.
func LoadView(w *workspace.Workspace, ref string) (*View, error) {
	tasks, err := store.ListTasks(w, "", "")
	if err != nil {
		return nil, err
	}
	t, err := store.NewTaskIndex(tasks).Find(ref)
	if err != nil {
		return nil, err
	}
	p, err := store.LoadProject(w, t.Project)
	if err != nil {
		return nil, err
	}
	v := &View{Task: t, Project: p, Tasks: tasks, promptsDir: w.PromptsDir()}
	v.Calibration = store.CalibrationSamplesFrom(w, tasks)
	v.Decisions, _ = store.ListNotes(w, p.Slug, model.NoteDecision)
	v.Findings, _ = store.ListNotes(w, p.Slug, model.NoteFinding)
	v.Risks, _ = store.ListRisks(w, p.Slug)
	v.Glossary, _ = mdstore.ReadFile(w.GlossaryPath(p.Slug))
	v.Lessons = store.WorkspaceLessons(w, p.Slug)
	v.Events, _ = eventlog.List(w, eventlog.Query{})
	v.Roles, _ = store.LoadRoles(w)
	v.Shortcuts, _ = store.LoadShortcuts(w)
	return v, nil
}

// EstimateTokens approximates token count. chars/4 is wrong per-model and
// every trim is announced anyway — the agent can see the estimate bit, which
// is most of the value a precise count would provide.
func EstimateTokens(s string) int { return len(s) / 4 }

// Assemble builds the brief for a task ref. Reads fold in pending events, so
// a sibling's finding is visible here the instant it is appended.
func Assemble(w *workspace.Workspace, ref string, opt Options) (*Brief, error) {
	v, err := LoadView(w, ref)
	if err != nil {
		return nil, err
	}
	return AssembleView(v, opt)
}

// AssembleTaskView renders only the selected task record. It is used when a
// caller has task-level authority and must not load project or workspace
// context. The task section remains non-droppable under the usual budget rule.
func AssembleTaskView(t *store.Task, opt Options) (*Brief, error) {
	if t == nil {
		return nil, fmt.Errorf("task is required")
	}
	b := &Brief{TaskID: t.ID}
	b.addWithSources("Task: "+t.Title, taskSection(t, ""), false, []Source{{Kind: "task", ID: t.ID, Path: t.Path}})
	return b, b.trim(opt.Budget)
}

// AssembleView performs pure selection and rendering over a loaded view.
func AssembleView(v *View, opt Options) (*Brief, error) {
	t, p, allTasks := v.Task, v.Project, v.Tasks
	b := &Brief{TaskID: t.ID, promptsDir: v.promptsDir}

	// 1. The task itself — never trimmed. If it alone exceeds the budget,
	// assembly fails rather than truncating the one thing the agent needs.
	// The calibration line (PROPOSALS P2) rides here once history earns it:
	// n >= 10 completed estimate+actual pairs, never sooner — a multiplier
	// from three anecdotes is confidence theater.
	calib := ""
	if tp, ok := t.Estimate(); ok {
		// Reuse the task list this function already walked: CalibrationSamples
		// would re-list and re-parse every done task, which measured at more
		// than half of Assemble's total cost — paid per spawn, and again per
		// supervise turn.
		if samples := v.Calibration; len(samples) >= 10 {
			ratios := make([]float64, len(samples))
			for i, s := range samples {
				ratios[i] = s.Ratio()
			}
			med := spm.Median(ratios)
			lo, hi := tp.ConfidenceRange(1)
			calib = fmt.Sprintf("calibrated: ~%.1f–%.1fh wall (×%.1f median, n=%d — time proxy, not tokens)",
				lo*med, hi*med, med, len(samples))
		}
	}
	b.addWithSources("Task: "+t.Title, taskSection(t, calib), false, []Source{{Kind: "task", ID: t.ID, Path: t.Path}})

	// 1b. WHO the agent is. A role's standing instructions — its method, what
	// it looks for, what it refuses — are what make a roster a team rather
	// than a directory of path globs. Not droppable: an agent trimmed down to
	// the task alone would work like every other role, which is the failure
	// this section exists to prevent (dacli 202). Roles whose file carries no
	// instructions beyond metadata emit nothing.
	if opt.Role != "" {
		for _, r := range v.Roles {
			if !strings.EqualFold(r.Name, opt.Role) {
				continue
			}
			if p := strings.TrimSpace(r.Prompt); p != "" {
				b.add("Your role: "+r.Name, p+"\n", false)
			}
			break
		}
	}

	// 2. Why — project goal chain, with the current lifecycle phase so the
	// agent knows what kind of work is appropriate NOW (don't implement in
	// discovery).
	var why strings.Builder
	fmt.Fprintf(&why, "Project **%s** — *%s*\n", p.Slug, p.Title)
	if phase, ok := p.Doc.Front.Get("phase"); ok && phase != "" {
		fmt.Fprintf(&why, "Phase: **%s**", phase)
		if allows := p.Doc.Front.GetList("phase_allows"); len(allows) > 0 {
			fmt.Fprintf(&why, " (work appropriate now: %s)", strings.Join(allows, ", "))
		}
		why.WriteString("\n")
	}
	if s, ok := p.Doc.Section("Goal"); ok && strings.TrimSpace(s.Content) != "" {
		why.WriteString("Goal: " + strings.TrimSpace(s.Content) + "\n")
	}
	if s, ok := p.Doc.Section("Success criteria"); ok && strings.TrimSpace(s.Content) != "" {
		why.WriteString("Success criteria:\n" + s.Content)
	}
	b.add("Why", why.String(), true)

	// 2b. Spec and Architecture — what the product IS and how it is built.
	// `dacli new` seeds both at project birth, and on a greenfield repo they
	// are the only description of the thing being built: there is no codebase
	// to read yet, so an agent without them is guessing at the shape of the
	// system. Placed above the scope boundary because they are the positive
	// statement the boundary constrains (dacli 191).
	for _, name := range []string{"Spec", "Architecture"} {
		if s, ok := p.Doc.Section(name); ok && strings.TrimSpace(s.Content) != "" {
			b.add(name, s.Content, true)
		}
	}

	// 3. Scope boundary — cheap, and the only scope-creep intervention that
	// lands before the tokens are spent.
	if s, ok := p.Doc.Section("Out of scope"); ok && strings.TrimSpace(s.Content) != "" {
		b.add("Out of scope", s.Content, true)
	}

	// 3b. Codebase map — for adopted projects, the real structure of the
	// existing repo, so an agent onboards from context rather than a blank.
	if s, ok := p.Doc.Section("Codebase map"); ok && strings.TrimSpace(s.Content) != "" {
		b.add("Codebase map", s.Content, true)
	}

	// 4. Constraints — project constraints plus decision notes, capped.
	var cons strings.Builder
	if s, ok := p.Doc.Section("Constraints"); ok && strings.TrimSpace(s.Content) != "" {
		cons.WriteString(s.Content)
	}
	decisions := v.Decisions
	// Rank by recency, newest first, before the cap (dacli 286). Decisions carry
	// no severity or trust, so recency is the only signal — and the only one that
	// matters: the decisions a new agent must not undo are the ones just made, not
	// the ones whose filename slug sorts earliest. os.ReadDir order used to show
	// the oldest `NNN-*` slugs and silently drop every recent decision.
	sortByRecency(decisions)
	shown := 0
	decisionSources := make([]Source, 0, min(len(decisions), MillerCap))
	for _, d := range decisions {
		if shown >= MillerCap {
			break
		}
		id, _ := d.Front.Get("id")
		if id != "" {
			decisionSources = append(decisionSources, Source{Kind: "decision", ID: id, Path: d.SourcePath, Trust: "recorded-decision"})
		}
		fmt.Fprintf(&cons, "**[[%s]]**", id)
		if s, ok := d.Section("Chose"); ok {
			fmt.Fprintf(&cons, " — Chose: %s", strings.TrimSpace(s.Content))
		}
		if s, ok := d.Section("Rejected"); ok {
			fmt.Fprintf(&cons, " Rejected: %s.", strings.TrimSpace(s.Content))
		}
		if s, ok := d.Section("Because"); ok {
			fmt.Fprintf(&cons, " Because: %s", strings.TrimSpace(s.Content))
		}
		cons.WriteString("\n")
		shown++
	}
	if len(decisions) > shown {
		var dropped []string
		for _, d := range decisions[shown:] {
			id, _ := d.Front.Get("id")
			dropped = append(dropped, "[["+id+"]]")
		}
		b.Omitted = append(b.Omitted, namedOmission("decisions", len(decisions)-shown, dropped))
	}
	if strings.TrimSpace(cons.String()) != "" {
		b.addWithSources("Constraints", cons.String(), true, decisionSources)
	}

	// 5. Risks — rank 1 and 2 only, WITH their indicators. A risk register
	// helps an agent only in this form: what is likely to go wrong, and what
	// the early warning looks like.
	risks := v.Risks
	var rk strings.Builder
	shownRisks := 0
	for _, r := range risks {
		if r.Rank() > 2 {
			continue // rank 3 is monitored, not briefed
		}
		if shownRisks >= MillerCap {
			b.Omitted = append(b.Omitted, "risks beyond the working-memory cap")
			break
		}
		fmt.Fprintf(&rk, "**%s** (rank %d)", r.Title, r.Rank())
		if len(r.Indicators) > 0 {
			fmt.Fprintf(&rk, " — watch for: %s", strings.Join(r.Indicators, "; "))
		}
		rk.WriteString("\n")
		shownRisks++
	}
	if rk.Len() > 0 {
		b.add("Risks", rk.String(), true)
	}

	// 6. Glossary — one definition per term for every agent in the tree.
	if g := v.Glossary; g != nil {
		var body strings.Builder
		for _, s := range g.Sections {
			body.WriteString(s.Content)
		}
		if strings.TrimSpace(body.String()) != "" {
			b.add("Glossary", body.String(), true)
		}
	}

	// 7. Lessons — workspace-scoped notes from OTHER projects (PROPOSALS
	// P1): the compounding loop. Rendered quote-fenced like all third-party
	// content — lessons are data in briefs; only skills are instructions,
	// and the boundary between those is a security boundary (SKILLS.md § 6).
	lessons := v.Lessons
	if len(lessons) > 0 {
		var ls strings.Builder
		shown := 0
		for _, l := range lessons {
			if shown >= MillerCap {
				b.Omitted = append(b.Omitted, fmt.Sprintf("%d workspace lessons beyond the cap", len(lessons)-shown))
				break
			}
			writeQuoted(&ls, l.Actor+" · from "+l.Project, "", "[["+l.ID+"]] "+l.Title+" — "+l.Body)
			shown++
		}
		b.add("Lessons from other projects", ls.String(), true)
	}

	// 8. What siblings found — finding notes plus PENDING finding events, so
	// a report is visible tree-wide the instant it is written, no sync
	// needed. Third-party content is quote-fenced and attributed: data, not
	// instructions.
	var finds strings.Builder
	// The trust-floor (D3): the WORST verify grade among the findings surfaced
	// below, ordered refuted < unverified < confirmed. An ungraded finding — one
	// verify has not judged — drops the floor to "unverified", so an unchecked
	// claim is visible AS unchecked before a child acts on it. floorRank starts
	// above every grade; -1 once any finding is seen.
	floorRank := -1
	noteFloor := func(trust string) {
		if r := TrustRank(trust); floorRank < 0 || r < floorRank {
			floorRank = r
		}
	}
	// Findings honor MillerCap like every other section — a brief is a
	// working-memory budget, not an archive. Notes come first (graded, durable),
	// then pending events; the cap spans both, and the remainder is announced as
	// an omission. The trust-floor reflects only the findings actually shown.
	// Scope both feeds to THIS PROJECT. store.ListNotes is already per-project;
	// the pending finding events must match that scope, or a finding's brief
	// visibility flips across the sync boundary — project-wide as a materialized
	// note, but task-scoped while it is still a pending event (issue #21). Both
	// are now project-wide: a sibling's finding about ANY task in this project is
	// visible the instant it is written and stays visible once the owner syncs it
	// into a note. An event with no `about` target carries no task to place, so it
	// surfaces in every project's brief as before.
	notes := v.Findings
	// Rank findings by severity, then trust, then recency before the cap (dacli
	// 286) — os.ReadDir handed them back in alphabetical filename order, so a
	// `major`/`confirmed` finding whose slug sorted late was silently dropped
	// while a `minor` one that sorted early survived. Now the cap keeps the most
	// severe, best-verified, newest findings; the least severe are what gets cut.
	sortFindings(notes)
	// ONE event-log walk serves all THREE of the brief's event queries (dacli
	// 246): the pending findings here, the recent activity in §9, and the run
	// counts that rank shortcuts in §10. Every eventlog.List is a full walk and
	// parse of every event file — ~3.5ms each on a 344-event log, so three
	// calls cost ~10.5ms of the 26.7ms Assemble for one log's worth of data.
	// Limit does NOT save the third call: it is checked against MATCHING
	// events, so §9's Limit: 5 still parsed all 344 files whenever the task had
	// fewer than 5 events. Filter this slice in memory; do not add a fourth
	// List. Events come back newest-first, and each in-memory filter below
	// preserves that order, so section content and ordering are unchanged.
	allEvents := v.Events
	var events []*eventlog.Event
	for _, e := range allEvents {
		// e.Pending is the literal `applied: false` test Query{Pending: true}
		// applies — not !e.Applied — so this selects the identical set.
		if e.Kind == model.EventFinding && e.Pending {
			events = append(events, e)
		}
	}
	inProject := map[string]bool{}
	for _, pt := range allTasks {
		if pt.Project != p.Slug {
			continue
		}
		inProject[pt.ID] = true
		inProject[strings.TrimPrefix(pt.ID, "t-")] = true
	}
	var pending []*eventlog.Event
	for _, e := range events {
		if e.About != "" && !inProject[e.About] {
			continue
		}
		pending = append(pending, e)
	}
	total := len(notes) + len(pending)
	// Notes are shown first (graded, durable), then pending events fill any
	// remaining slots; the cap spans both. Track how many of each survived so the
	// omission can NAME what was dropped rather than report a bare count (dacli
	// 286) — and because notes are severity-ranked above, the dropped notes are
	// the least severe, so the named ones are exactly the borderline cases an
	// agent would want to ask about.
	shownNotes := 0
	findingSources := make([]Source, 0, MillerCap)
	for _, n := range notes {
		if shownNotes >= MillerCap {
			break
		}
		id, _ := n.Front.Get("id")
		by, _ := n.Front.Get("created_by")
		sev, _ := n.Front.Get("severity")
		trust, _ := n.Front.Get("trust")
		if id != "" {
			findingSources = append(findingSources, Source{Kind: "finding", ID: id, Path: n.SourcePath, Trust: TrustLabel(trust)})
		}
		noteFloor(trust)
		// On disk the note's body lives inside the level-1 title section
		// (content extends to the next heading), so collect every section's
		// content — filtering by level here silently dropped finding bodies,
		// which the dogfood test caught on its first run.
		var body strings.Builder
		for _, s := range n.Sections {
			body.WriteString(s.Content)
		}
		writeQuoted(&finds, by, sev, "[trust: "+TrustLabel(trust)+"] [["+id+"]] "+strings.TrimSpace(body.String()))
		shownNotes++
	}
	shownPending := 0
	for _, e := range pending {
		if shownNotes+shownPending >= MillerCap {
			break
		}
		// A pending finding event is not yet a graded note — ungraded, so it
		// pulls the floor to unverified like any other unchecked claim.
		noteFloor("")
		findingSources = append(findingSources, Source{Kind: "event", ID: e.ID, Path: e.Path, Trust: TrustLabel("")})
		writeQuoted(&finds, e.Actor, "", "[trust: "+TrustLabel("")+"] "+e.Body)
		shownPending++
	}
	shown = shownNotes + shownPending
	if total > shown {
		var dropped []string
		for _, n := range notes[shownNotes:] {
			id, _ := n.Front.Get("id")
			sev, _ := n.Front.Get("severity")
			label := "[[" + id + "]]"
			if sev != "" {
				label += " (" + sev + ")"
			}
			dropped = append(dropped, label)
		}
		for _, e := range pending[shownPending:] {
			dropped = append(dropped, "pending finding by "+e.Actor)
		}
		b.Omitted = append(b.Omitted, namedOmission("findings", total-shown, dropped))
	}
	if strings.TrimSpace(finds.String()) != "" {
		floor := fmt.Sprintf("**trust-floor: %s** — worst verify grade among the findings below (refuted < unverified < confirmed); an unverified claim has not been checked, treat it as a lead, not a fact.\n\n",
			TrustLabel(RankTrust(floorRank)))
		b.addWithSources("What siblings found", floor+finds.String(), true, findingSources)
	}

	// 9. Recent activity on this task.
	var act strings.Builder
	// The newest 5 events about this task, taken from the single walk above
	// (dacli 246). eventlog.List's Limit stops at 5 MATCHING events, so this
	// query used to re-walk and re-parse the entire log whenever the task had
	// fewer than 5 — the limit never short-circuited.
	recent := make([]*eventlog.Event, 0, 5)
	for _, e := range allEvents {
		if len(recent) >= 5 {
			break
		}
		if e.About == t.ID {
			recent = append(recent, e)
		}
	}
	for _, e := range recent {
		// Guarded, not e.ID[:10]: event ids come from filenames, which are
		// hand-editable, and a short one would panic brief assembly (dacli 207).
		short := e.ID
		if len(short) > 10 {
			short = short[:10]
		}
		fmt.Fprintf(&act, "- %s %s by %s\n", short, e.Kind, e.Actor)
	}
	if act.Len() > 0 {
		b.add("Recent activity", act.String(), true)
	}

	// 10. Shortcuts — ranked by derived use count, truncated with the
	// omission announced. An unadvertised shortcut still runs; it just
	// stops taxing every brief.
	if scs := append([]shortcut.Shortcut(nil), v.Shortcuts...); len(scs) > 0 {
		// Run counts come from the same single walk (dacli 246) — this was the
		// third full re-parse of the log inside one Assemble.
		counts := map[string]int{}
		for _, e := range allEvents {
			if e.Kind == model.EventRun {
				counts[e.About]++
			}
		}
		for i := range scs {
			scs[i].Uses = counts[scs[i].Name]
		}
		if cat := shortcut.Catalog(scs, "", 8); strings.TrimSpace(cat) != "" {
			b.add("Shortcuts", cat, true)
		}
	}

	return b, b.trim(opt.Budget)
}

func (b *Brief) add(title, content string, droppable bool) {
	b.Sections = append(b.Sections, Section{Title: title, Content: content, Droppable: droppable})
}

func (b *Brief) addWithSources(title, content string, droppable bool, sources []Source) {
	b.Sections = append(b.Sections, Section{Title: title, Content: content, Droppable: droppable, Sources: sources})
}

// TrustLabel renders a finding note's `trust:` frontmatter for a brief. An
// empty grade means verify has not judged the finding — it is surfaced as
// "unverified" so an unchecked claim reads as such, which is the point.
// Exported so `dacli pr --with-verdicts` (internal/features/vcs) renders the
// same trust vocabulary into a PR rather than keeping a second, driftable copy.
func TrustLabel(trust string) string {
	switch trust {
	case "confirmed":
		return "confirmed"
	case "refuted":
		return "refuted"
	default:
		return "unverified"
	}
}

// TrustRank orders grades for the trust-floor: refuted < unverified < confirmed.
// The floor is the WORST (lowest) grade among the surfaced findings.
func TrustRank(trust string) int {
	switch trust {
	case "refuted":
		return 0
	case "confirmed":
		return 2
	default:
		return 1 // ungraded / unverified
	}
}

// RankTrust is the inverse of TrustRank, mapping the accumulated floor rank
// back to a canonical grade string for TrustLabel.
func RankTrust(rank int) string {
	switch rank {
	case 0:
		return "refuted"
	case 2:
		return "confirmed"
	default:
		return "" // unverified (also the no-findings case, which is never rendered)
	}
}

// noteCreated reads a note's `created` frontmatter for recency ranking. The
// value is an RFC3339 timestamp, which sorts identically lexically and
// chronologically, so a plain string compare orders newest-first. A missing
// field sorts as the empty string — oldest — which is the right default: an
// unstamped note should not out-rank a stamped one.
func noteCreated(d *mdstore.Doc) string {
	c, _ := d.Front.Get("created")
	return c
}

// sortByRecency orders notes newest-first, in place. Used for decisions, which
// carry no severity or trust — recency is the only cap-selection signal they
// have (dacli 286).
func sortByRecency(notes []*mdstore.Doc) {
	sort.SliceStable(notes, func(i, j int) bool {
		return noteCreated(notes[i]) > noteCreated(notes[j])
	})
}

// severityRank orders a finding's `severity` for the working-memory cap: the
// most severe survives (lower rank wins). An unknown or empty severity ranks
// last, below every graded level, so a finding a reporter bothered to grade
// out-ranks one left ungraded.
func severityRank(sev string) int {
	switch strings.ToLower(strings.TrimSpace(sev)) {
	case "major":
		return 0
	case "moderate":
		return 1
	case "minor":
		return 2
	default:
		return 3
	}
}

// sortFindings orders finding notes for the cap by severity, then trust, then
// recency (dacli 286). Severity is the primary signal — a critical finding must
// never be dropped for a filename that sorts earlier. Ties break to the
// better-verified finding (confirmed > unverified > refuted, via TrustRank), then
// to the newest. Stable so equal-key findings keep their prior order.
func sortFindings(notes []*mdstore.Doc) {
	sort.SliceStable(notes, func(i, j int) bool {
		si, _ := notes[i].Front.Get("severity")
		sj, _ := notes[j].Front.Get("severity")
		if ri, rj := severityRank(si), severityRank(sj); ri != rj {
			return ri < rj
		}
		ti, _ := notes[i].Front.Get("trust")
		tj, _ := notes[j].Front.Get("trust")
		if ri, rj := TrustRank(ti), TrustRank(tj); ri != rj {
			return ri > rj // confirmed (2) beats unverified (1) beats refuted (0)
		}
		return noteCreated(notes[i]) > noteCreated(notes[j])
	})
}

// omissionNameCap bounds how many dropped items an omission enumerates by name.
// Naming the FULL tail would defeat the working-memory budget the cap exists to
// enforce — core already drops hundreds of findings per brief. Because the
// dropped items are ranked worst-last, the first few are the borderline cases an
// agent is most likely to want to ask about; the rest collapse to "+N more".
const omissionNameCap = MillerCap

// namedOmission renders an omission that NAMES what was dropped rather than
// reporting a bare count (dacli 286). It lists up to omissionNameCap names and
// summarizes any remainder, so a dropped critical item is visible by name while
// a long low-severity tail does not blow the budget.
func namedOmission(kind string, total int, names []string) string {
	base := fmt.Sprintf("%d %s beyond the working-memory cap", total, kind)
	if len(names) == 0 {
		return base
	}
	if len(names) <= omissionNameCap {
		return base + ": " + strings.Join(names, ", ")
	}
	return fmt.Sprintf("%s: %s, +%d more", base, strings.Join(names[:omissionNameCap], ", "), len(names)-omissionNameCap)
}

// writeQuoted renders third-party content as an attributed blockquote — the
// cheap injection mitigation: it makes the provenance visible, not the
// attack impossible.
func writeQuoted(w *strings.Builder, by, severity, text string) {
	tag := by
	if severity != "" {
		tag += ", " + severity
	}
	fmt.Fprintf(w, "> **%s**:\n", tag)
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		fmt.Fprintf(w, "> %s\n", line)
	}
}

func taskSection(t *store.Task, calibLine string) string {
	var s strings.Builder
	meta := []string{}
	if p := t.Priority(); p != "" {
		meta = append(meta, "priority: "+p)
	}
	if tp, ok := t.Estimate(); ok {
		meta = append(meta, fmt.Sprintf("estimate: %g/%g/%g (Te %.1f)",
			tp.Optimistic, tp.Probable, tp.Pessimistic, tp.Expected()))
	}
	if o := t.Owner(); o != "" {
		meta = append(meta, "owner: "+o)
	}
	if len(meta) > 0 {
		s.WriteString(strings.Join(meta, " · ") + "\n")
	}
	if calibLine != "" {
		s.WriteString(calibLine + "\n")
	}
	if len(meta) > 0 || calibLine != "" {
		s.WriteString("\n")
	}
	for _, sec := range t.Doc.Sections {
		switch {
		case sec.Level == 1:
			// title already in the section header
		case strings.EqualFold(sec.Title, "Log"):
			// history, not context
		default:
			if strings.TrimSpace(sec.Content) == "" {
				continue
			}
			if sec.Title != "" {
				s.WriteString("**" + sec.Title + "**\n")
			}
			s.WriteString(sec.Content)
		}
	}
	return s.String()
}

// trim drops droppable sections from the bottom until the budget fits,
// announcing each drop. The task itself is never dropped: if it alone
// exceeds the budget, that is an error, not a truncation.
func (b *Brief) trim(budget int) error {
	if budget <= 0 {
		return nil
	}
	// render() concatenates every section verbatim, and EstimateTokens is
	// len/4, so the whole brief's token estimate is a pure sum over its
	// sections. Keep a running byte total and subtract a dropped section's
	// bytes instead of re-rendering + re-tokenizing the entire document on
	// every pass. total stays byte-identical to len(b.render()), so the drop
	// decisions (which sections, in what order) are unchanged.
	total := 0
	for _, sec := range b.Sections {
		total += sectionLen(sec)
	}
	for total/4 > budget {
		dropped := false
		for i := len(b.Sections) - 1; i >= 0; i-- {
			if b.Sections[i].Droppable {
				b.Omitted = append(b.Omitted, fmt.Sprintf("section %q (budget)", b.Sections[i].Title))
				total -= sectionLen(b.Sections[i])
				b.Sections = append(b.Sections[:i], b.Sections[i+1:]...)
				dropped = true
				break
			}
		}
		if !dropped {
			return fmt.Errorf("task alone exceeds the %d-token budget; raise it — truncating the task would hand the agent half its instructions", budget)
		}
	}
	return nil
}

// sectionLen is the byte length one section contributes to render():
// "## " + title + "\n" + content + "\n". Kept next to render() so the two
// stay in lockstep — total/4 must equal EstimateTokens(render()) exactly.
func sectionLen(sec Section) int {
	return len("## ") + len(sec.Title) + len("\n") + len(sec.Content) + len("\n")
}

func (b *Brief) render() string {
	var s strings.Builder
	for _, sec := range b.Sections {
		s.WriteString("## " + sec.Title + "\n")
		s.WriteString(sec.Content)
		s.WriteString("\n")
	}
	return s.String()
}

// Render produces the final markdown document. The header prose is
// templated (prompts/tpl/brief_header.md, workspace-overridable) — the
// data-not-instructions line is a security posture and deserves review as a
// file, not as a string constant.
func (b *Brief) Render() string {
	var s strings.Builder
	header, err := prompts.Render(b.promptsDir, "brief_header", map[string]any{
		"TaskID": b.TaskID, "Est": EstimateTokens(b.render()),
	})
	if err != nil {
		// A broken header override degrades to the embedded default rather
		// than shipping a brief without the untrusted-content warning.
		header, _ = prompts.Render("", "brief_header", map[string]any{
			"TaskID": b.TaskID, "Est": EstimateTokens(b.render()),
		})
	}
	s.WriteString(strings.TrimRight(header, "\n") + "\n\n")
	s.WriteString(b.render())
	for _, o := range b.Omitted {
		fmt.Fprintf(&s, "<!-- dacli: omitted %s -->\n", o)
	}
	return s.String()
}
