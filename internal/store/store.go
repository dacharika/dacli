// Package store is L2: object CRUD over mdstore, enforcing ownership.
//
// Status is folder position, ids are ULIDs, and the NNN filename prefix is a
// display alias assigned by the single allocator (the creating owner), per
// docs/FORMAT.md. Nothing here touches the event log — that is L3's job.
package store

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mlnomadpy/dacli/internal/gitx"
	"github.com/mlnomadpy/dacli/internal/mdstore"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/procmon"
	"github.com/mlnomadpy/dacli/internal/spm"
	"github.com/mlnomadpy/dacli/internal/ulid"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

// ErrNotFound distinguishes "no such object" from operational failures, so
// the CLI can map it to exit code 4.
type ErrNotFound struct{ Ref string }

func (e ErrNotFound) Error() string { return fmt.Sprintf("not found: %s", e.Ref) }

func now() string { return time.Now().UTC().Format(time.RFC3339) }

// Slugify turns a title into a filename-safe slug.
func Slugify(s string) string {
	orig := s
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	s = strings.TrimSuffix(b.String(), "-")
	// Bound the slug so it is always a legal filename. A note whose "title" is
	// a whole paragraph (dacli note add "<long text>") otherwise produced a
	// slug longer than the OS filename limit and the write failed outright.
	// Trim at the last dash within the cap so a word isn't cut mid-token.
	const maxSlug = 80
	if len(s) > maxSlug {
		s = s[:maxSlug]
		if i := strings.LastIndexByte(s, '-'); i > 0 {
			s = s[:i]
		}
	}
	if s == "" {
		// A punctuation-only ("???") or non-ASCII (CJK, Arabic — i18n titles
		// are realistic) title keeps no [a-z0-9] rune, so the slug collapses to
		// "". Left bare, CreateNote writes a hidden ".md" file with a bare "f-"
		// id and CreateTask writes "NNN-.md"; two such objects then collide.
		// Fall back to a stable, deterministic token derived from the original
		// title so every object keeps a legible, unique filename and id.
		h := fnv.New32a()
		_, _ = h.Write([]byte(orig))
		s = fmt.Sprintf("u%08x", h.Sum32())
	}
	return s
}

// --- Projects ---

// Project is the parsed view store works with; heavier typing waits for the
// brief assembler, which is the real consumer.
type Project struct {
	Slug    string
	Doc     *mdstore.Doc
	Path    string
	Title   string
	Stage   string
	Landing model.LandingPolicy
}

// CreateProject writes projects/<slug>/project.md with the structural
// sections the brief assembler reads by heading.
func CreateProject(w *workspace.Workspace, actor, title, slug, goal, stage string, landing ...model.LandingPolicy) (*Project, error) {
	if slug == "" {
		slug = Slugify(title)
	}
	// An explicit --slug bypasses Slugify, so validate it here: it must be a
	// safe single path segment, or it could escape projects/ and write a
	// project file anywhere the user can write (and later be RemoveAll'd by
	// `project rm`). ProjectDir also contains this defensively; this is the
	// clean, early error on the common path.
	if !workspace.SafeSegment(slug) {
		return nil, fmt.Errorf("invalid project slug %q: must be a single path segment without '/' or '..'", slug)
	}
	path := w.ProjectPath(slug)
	if _, err := os.Stat(path); err == nil {
		return nil, fmt.Errorf("project %q already exists", slug)
	}
	if stage == "" {
		// New projects start at the widest point of the Cone: claiming more
		// certainty than "we just defined this" would be a lie in a field.
		stage = "definition"
	}
	var policy model.LandingPolicy
	if len(landing) > 0 {
		policy = landing[0]
		if err := model.ValidateLandingPolicy(policy); err != nil {
			return nil, err
		}
	}

	d := &mdstore.Doc{}
	d.Front.Set("id", "p-"+slug)
	d.Front.Set("kind", string(model.KindProject))
	d.Front.Set("created", now())
	d.Front.Set("created_by", actor)
	d.Front.Set("status", "active")
	d.Front.Set("stage", stage)
	if len(landing) > 0 {
		d.Front.Set("landing.mode", string(policy.Mode))
		if policy.Base != "" {
			d.Front.Set("landing.base", policy.Base)
		}
	}
	d.Sections = []mdstore.Section{
		{Level: 1, Title: title, Content: ""},
		{Level: 2, Title: "Goal", Content: goal + "\n"},
		{Level: 2, Title: "Constraints", Content: ""},
		{Level: 2, Title: "Out of scope", Content: ""},
		{Level: 2, Title: "Success criteria", Content: ""},
	}
	if err := mdstore.WriteFile(path, d); err != nil {
		return nil, err
	}
	return &Project{Slug: slug, Doc: d, Path: path, Title: title, Stage: stage, Landing: policy}, nil
}

// SaveProject rewrites a project in place.
func SaveProject(p *Project) error { return mdstore.WriteFile(p.Path, p.Doc) }

// ConfigureProjectLanding validates before touching the document so a usage
// error can never leave a partially configured project record behind.
func ConfigureProjectLanding(p *Project, policy model.LandingPolicy) error {
	if err := model.ValidateLandingPolicy(policy); err != nil {
		return err
	}
	p.Doc.Front.Set("landing.mode", string(policy.Mode))
	if policy.Base == "" {
		p.Doc.Front.Delete("landing.base")
	} else {
		p.Doc.Front.Set("landing.base", policy.Base)
	}
	p.Landing = policy
	return nil
}

// UpdateProjectLanding serializes the whole load/resolve/save transaction.
// Atomic file replacement alone is insufficient: two one-flag project-show
// updates can both read the same policy, both report success, and silently
// overwrite one another (issue #762 review). The lock must therefore surround
// the read as well as the write.
func UpdateProjectLanding(w *workspace.Workspace, slug string, override model.LandingOverride) (*Project, error) {
	var updated *Project
	err := WithFileLock(filepath.Join(w.ProjectDir(slug), ".project.lock"), func() error {
		p, err := LoadProject(w, slug)
		if err != nil {
			return err
		}
		policy, explicit, err := model.ResolveLanding(p.Landing, override)
		if err != nil {
			return err
		}
		if !explicit {
			updated = p
			return nil
		}
		if err := ConfigureProjectLanding(p, policy); err != nil {
			return err
		}
		if err := SaveProject(p); err != nil {
			return err
		}
		updated, err = LoadProject(w, slug)
		return err
	})
	return updated, err
}

func LoadProject(w *workspace.Workspace, slug string) (*Project, error) {
	path := w.ProjectPath(slug)
	d, err := mdstore.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound{Ref: "project/" + slug}
		}
		return nil, err
	}
	p := &Project{Slug: slug, Doc: d, Path: path}
	p.Stage, _ = d.Front.Get("stage")
	mode, _ := d.Front.Get("landing.mode")
	base, baseSet := d.Front.Get("landing.base")
	if baseSet && strings.TrimSpace(base) == "" {
		return nil, fmt.Errorf("invalid project %q: landing base must be a non-empty branch when configured", slug)
	}
	p.Landing = model.LandingPolicy{Mode: model.LandingMode(mode), Base: base}
	_, _, err = model.ResolveLanding(p.Landing, model.LandingOverride{})
	if err != nil {
		return nil, fmt.Errorf("invalid project %q: %w", slug, err)
	}
	for _, s := range d.Sections {
		if s.Level == 1 {
			p.Title = s.Title
			break
		}
	}
	return p, nil
}

// DeleteProject removes a project directory and everything filed under it —
// tasks, notes, risks, glossary. Irreversible; callers must get explicit
// confirmation before calling this (see cmdProjectRm's --force gate) — this
// exists specifically to recover from a project created by mistake (e.g. an
// `adopt` that guessed the wrong slug).
func DeleteProject(w *workspace.Workspace, slug string) error {
	dir := w.ProjectDir(slug)
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound{Ref: "project/" + slug}
		}
		return err
	}
	return os.RemoveAll(dir)
}

func ListProjects(w *workspace.Workspace) ([]*Project, error) {
	entries, err := os.ReadDir(w.ProjectsDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []*Project
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), "_") {
			continue
		}
		p, err := LoadProject(w, e.Name())
		if err != nil {
			continue // a broken project dir shouldn't hide the others
		}
		out = append(out, p)
	}
	return out, nil
}

// --- Tasks ---

type Task struct {
	ID      string
	Seq     int
	Slug    string
	Project string
	Status  model.Status
	Title   string
	Doc     *mdstore.Doc
	Path    string
}

// TaskBranch is the branch a task's work lands on. It lives here, in the
// entity layer, because three different slices need it and slices never
// import each other: vcs creates and merges it, acceptance checks whether it
// reached trunk before certifying a close, and the git_workflow prompt tells
// agents the same convention.
func TaskBranch(t *Task) string {
	if t == nil {
		return ""
	}
	if t.IsDeliverySlice() {
		return deliverySliceBranch(t)
	}
	return fmt.Sprintf("dacli/%03d-%s", t.Seq, t.Slug)
}

func (t *Task) Owner() string    { v, _ := t.Doc.Front.Get("owner"); return v }
func (t *Task) Priority() string { v, _ := t.Doc.Front.Get("priority"); return v }

// CompletionState distinguishes finished implementation from canonical done.
// PR-mode close requests remain in the nonterminal task folder until a landing
// owner records exact landing/verification evidence through acceptance.
func (t *Task) CompletionState() string {
	v, _ := t.Doc.Front.Get("completion_state")
	return v
}

// Generation identifies the current corrective-work generation of a task.
// Legacy tasks have generation zero. ReopenTask increments it so recovery
// ledgers created before a reopen cannot mistake the earlier landing for the
// newly-open work (issue #679).
func (t *Task) Generation() int {
	v, _ := t.Doc.Front.Get("generation")
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// ContinuousImprovementMarker is the title prefix of the loop's standing
// review-phase anchor task (see orchestration.ensureImproveTask): an auditor
// is spawned against it every cycle to file new work, but it is never itself
// implementer work.
const ContinuousImprovementMarker = "Continuous improvement"

// IsLoopAnchor reports whether t is the standing continuous-improvement
// anchor task, so callers can exclude it from "what's actionable" views
// (dacli next, the loop's own readyTasks) — the single source of truth both
// planning and execution defer to.
func (t *Task) IsLoopAnchor() bool {
	return strings.HasPrefix(t.Title, ContinuousImprovementMarker)
}

// PathHints pulls the path-like tokens a task mentions — in its title and in
// every section body (So that, Acceptance, Context, notes) — so routing can ask
// which role's declared scope covers the work. A task carries no explicit file
// list, so this is the best available signal for "the task's files"; it is
// deliberately crude (a spurious token costs one weak tie-break vote, a missed
// one just falls back to name). See PathTokens for the extraction (dacli 238).
func (t *Task) PathHints() []string {
	if claims := t.Claims(); len(claims) > 0 {
		return claims
	}
	var b strings.Builder
	b.WriteString(t.Title)
	if t.Doc != nil {
		for _, s := range t.Doc.Sections {
			b.WriteByte('\n')
			b.WriteString(s.Content)
		}
	}
	return PathTokens(b.String())
}

// Claims returns explicit minimal write paths produced by a reviewed WBS
// decomposition. Legacy tasks have none and continue to use PathHints' free
// text extraction; a planned child must not lose its narrower contract to a
// title token (issue #866).
func (t *Task) Claims() []string { return t.Doc.Front.GetList("claims") }

// PathTokens pulls path-like tokens (a slash, or a .go suffix) out of free
// text, stripping the file: prefix and :line suffix that findings use, so they
// can be tested against a role's scope globs. Shared so the routing tie-break
// (Task.PathHints) and the lesson/role hinter (insight) cannot diverge on what
// counts as a path.
func PathTokens(s string) []string {
	var out []string
	for _, f := range strings.Fields(s) {
		f = strings.Trim(f, "`.,:;()[]{}\"'")
		f = strings.TrimPrefix(f, "file:")
		if i := strings.IndexByte(f, ':'); i >= 0 {
			f = f[:i] // drop a :line suffix
		}
		if strings.Contains(f, "/") || strings.HasSuffix(f, ".go") {
			out = append(out, f)
		}
	}
	return out
}

// Acceptance returns the task's acceptance checkboxes.
func (t *Task) Acceptance() []mdstore.Checkbox {
	s, ok := t.Doc.Section("Acceptance")
	if !ok {
		return nil
	}
	return mdstore.Checkboxes(s.Content)
}

// HasAcceptanceCriteria reports whether the task states at least one acceptance
// checkbox. A task with none has nothing to verify, so closing it makes "done"
// mean "nothing was ever asked for" rather than "the work was checked": the
// unmet-box scan in `task done` finds an empty list and passes, and
// CheckAllAcceptance checks zero boxes and reports success — zero boxes read as
// all boxes. Every close path (task done, accept, and the propose→sync route)
// consults this so the empty-acceptance rule is enforced identically on each
// and no path can silently close an unverifiable task (dacli 289).
func HasAcceptanceCriteria(t *Task) bool {
	return len(t.Acceptance()) > 0
}

// CheckAllAcceptance marks every acceptance checkbox done in place and returns
// the number that were newly checked. It mutates only the in-memory doc — the
// caller SaveTask()s to persist. This is the "check --all" primitive factored
// out so the acceptance slice can close a task without importing the planning
// slice (the no-cross-import rule). A task with no Acceptance section returns 0.
func CheckAllAcceptance(t *Task) int {
	sec, ok := t.Doc.Section("Acceptance")
	if !ok {
		return 0
	}

	// Flip the boxes IN PLACE rather than re-rendering the section from a
	// parsed checkbox list. SetSection(RenderCheckboxes(...)) rebuilt the
	// section from the boxes alone, so every line that was not a checkbox —
	// leading prose, blank lines, plain bullets — was silently dropped, and
	// nested items lost their indentation (dacli 335).
	//
	// The prefix is "- [ ]" WITHOUT a trailing space: a criterion with no text
	// after the box is degenerate, but skipping it would leave an unchecked
	// box behind on a task reported as fully accepted, which is the one
	// outcome this function must never produce.
	lines := strings.Split(sec.Content, "\n")
	newly := 0
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "- [ ]") {
			continue // already checked, prose, or not a box: preserved verbatim
		}
		indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		lines[i] = indent + "- [x]" + trimmed[len("- [ ]"):]
		newly++
	}

	t.Doc.SetSection("Acceptance", strings.Join(lines, "\n"))
	return newly
}

// TaskOpts carries creation options; zero values are simply omitted.
type TaskOpts struct {
	Priority  string
	Estimate  string // "o,m,p"
	Accept    []string
	SoThat    string
	Context   string
	DependsOn []string // "ref" or "ref:SS" etc.
	Parent    string   // parent task ref — the WBS edge
	Claims    []string // minimal paths this task is allowed to change
	StableID  string   // content-addressed proposal ID; empty for normal creation
	Delivery  *DeliverySliceOpts
}

// Dep is one typed dependency. SS is what makes two tasks genuinely
// parallel-safe; everything else blocks.
type Dep struct {
	Ref  string
	Type string // FS | SS | FF | SF; FS when unspecified
}

// Deps parses the task's depends_on list. :blocks and :hard were pre-typed
// spellings for the default finish-to-start edge; accept them when reading old
// records so dependency edits and scheduling can repair or consume them.
func (t *Task) Deps() []Dep {
	var out []Dep
	for _, raw := range t.Doc.Front.GetList("depends_on") {
		d := Dep{Ref: raw, Type: "FS"}
		if i := strings.Index(raw, ":"); i > 0 {
			d.Ref, d.Type = raw[:i], strings.ToUpper(raw[i+1:])
			if d.Type == "BLOCKS" || d.Type == "HARD" {
				d.Type = "FS"
			}
		}
		out = append(out, d)
	}
	return out
}

// NormalizeDependency ensures new records use only the dependency types the
// CPM understands. :blocks and :hard are retained solely as migration aliases
// for FS.
func NormalizeDependency(raw string) (string, error) {
	ref, typ := raw, "FS"
	if i := strings.LastIndex(raw, ":"); i > 0 {
		ref, typ = raw[:i], strings.ToUpper(raw[i+1:])
	}
	if typ == "BLOCKS" || typ == "HARD" {
		typ = "FS"
	}
	switch typ {
	case "FS", "SS", "FF", "SF":
	default:
		return "", fmt.Errorf("dependency %q has unsupported type %q (want FS, SS, FF, or SF)", raw, typ)
	}
	if typ == "FS" {
		return ref, nil
	}
	return ref + ":" + typ, nil
}

// setEstimateFront writes a three-point estimate into frontmatter. Shared by
// creation and by SetEstimate so the two cannot drift on the format or on the
// refusal — a scalar estimate hides the very risk the third point exists to
// state.
func parseEstimate(est string) (spm.ThreePoint, error) {
	parts := strings.Split(est, ",")
	if len(parts) != 3 {
		return spm.ThreePoint{}, fmt.Errorf("estimate must be three-point o,m,p — a scalar hides the risk (got %q)", est)
	}
	values := make([]float64, len(parts))
	for i, p := range parts {
		if strings.TrimSpace(p) == "" {
			return spm.ThreePoint{}, fmt.Errorf("estimate must be three-point o,m,p — a missing point is not an estimate (got %q)", est)
		}
		value, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil {
			return spm.ThreePoint{}, fmt.Errorf("estimate point %q is not numeric", strings.TrimSpace(p))
		}
		values[i] = value
	}
	tp := spm.ThreePoint{Optimistic: values[0], Probable: values[1], Pessimistic: values[2]}
	if err := tp.Valid(); err != nil {
		return spm.ThreePoint{}, fmt.Errorf("invalid estimate: %w", err)
	}
	return tp, nil
}

// ValidateEstimate checks the CLI o,m,p representation before a command
// performs any mutation.
func ValidateEstimate(est string) error {
	_, err := parseEstimate(est)
	return err
}

func setEstimateFront(f *mdstore.Front, est string) error {
	tp, err := parseEstimate(est)
	if err != nil {
		return err
	}
	f.Set("estimate", fmt.Sprintf("{optimistic: %g, probable: %g, pessimistic: %g}",
		tp.Optimistic, tp.Probable, tp.Pessimistic))
	return nil
}

// SetEstimate sizes an EXISTING task. Estimates could previously only be set at
// creation, so a backlog filed without them could never be sized — and
// `critical-path`, `next --parallel` and the whole CPM schedule silently
// degraded to MoSCoW-then-sequence order, which is the one ordering that
// ignores what can run concurrently (dacli 228).
func SetEstimate(w *workspace.Workspace, t *Task, est string) error {
	if err := setEstimateFront(&t.Doc.Front, est); err != nil {
		return err
	}
	return SaveTask(t)
}

// Estimate returns the three-point estimate if present and valid.
func (t *Task) Estimate() (spm.ThreePoint, bool) {
	m := t.Doc.Front.GetMap("estimate")
	if m == nil {
		return spm.ThreePoint{}, false
	}
	var tp spm.ThreePoint
	_, _ = fmt.Sscanf(m["optimistic"], "%g", &tp.Optimistic)
	_, _ = fmt.Sscanf(m["probable"], "%g", &tp.Probable)
	_, _ = fmt.Sscanf(m["pessimistic"], "%g", &tp.Pessimistic)
	return tp, tp.Valid() == nil && tp.Pessimistic > 0
}

// CreateTask allocates the next NNN in the project (we are the owner at
// creation, so we are the single allocator the format requires) and writes
// the task into tasks/open/.
func CreateTask(w *workspace.Workspace, actor, project, title string, opts TaskOpts) (*Task, error) {
	if _, err := LoadProject(w, project); err != nil {
		return nil, err
	}

	// Two agents filing concurrently both scan the same on-disk seqs before
	// either file lands, so without a lock they can compute and write the
	// same NNN (dacli 209): both files exist with a shared seq, and FindTask
	// reports the ref ambiguous. acquireSeqLock serializes the scan-then-write
	// across processes so the seq each caller settles on is truly next.
	unlock, err := acquireSeqLock(w, project)
	if err != nil {
		return nil, err
	}
	defer unlock()

	seq, err := nextTaskSequence(w, project)
	if err != nil {
		return nil, err
	}

	id := opts.StableID
	if id == "" {
		id = "t-" + ulid.New()
	} else if !strings.HasPrefix(id, "t-") || !workspace.SafeSegment(id) {
		return nil, fmt.Errorf("stable task id %q is invalid", id)
	} else if existing, err := ListTasks(w, "", ""); err != nil {
		return nil, err
	} else {
		for _, task := range existing {
			if task.ID == id {
				return nil, fmt.Errorf("stable task id %s already exists", id)
			}
		}
	}
	slug := Slugify(title)

	d := &mdstore.Doc{}
	d.Front.Set("id", id)
	d.Front.Set("kind", string(model.KindTask))
	d.Front.Set("created", now())
	d.Front.Set("created_by", actor)
	d.Front.Set("owner", actor)
	if opts.Priority != "" {
		d.Front.Set("priority", opts.Priority)
	}
	if opts.Estimate != "" {
		if err := setEstimateFront(&d.Front, opts.Estimate); err != nil {
			return nil, err
		}
	}
	if len(opts.DependsOn) > 0 {
		deps := make([]string, 0, len(opts.DependsOn))
		for _, raw := range opts.DependsOn {
			dep, err := NormalizeDependency(raw)
			if err != nil {
				return nil, err
			}
			deps = append(deps, dep)
		}
		d.Front.SetList("depends_on", deps)
	}
	if opts.Parent != "" {
		// Resolve at the write site — the same lesson the about-filter bug
		// taught: a raw ref stored today is a broken link tomorrow.
		p, err := FindTask(w, opts.Parent)
		if err != nil {
			return nil, fmt.Errorf("parent: %w", err)
		}
		d.Front.Set("parent", "[["+p.ID+"]]")
	}
	if len(opts.Claims) > 0 {
		d.Front.SetList("claims", opts.Claims)
	}
	if opts.Delivery != nil {
		d.Front.Set("delivery_slice", "true")
		d.Front.Set("delivery_generation", strconv.Itoa(opts.Delivery.Generation))
		d.Front.Set("parent_generation", strconv.Itoa(opts.Delivery.ParentGeneration))
		d.Front.Set("delivery_required", strconv.FormatBool(opts.Delivery.Required))
		d.Front.Set("delivery_terminal", strconv.FormatBool(opts.Delivery.Terminal))
	}

	d.Sections = []mdstore.Section{{Level: 1, Title: title, Content: ""}}
	if opts.SoThat != "" {
		d.Sections = append(d.Sections, mdstore.Section{Level: 2, Title: "So that", Content: opts.SoThat + "\n"})
	}
	if opts.Context != "" {
		d.Sections = append(d.Sections, mdstore.Section{Level: 2, Title: "Context", Content: opts.Context + "\n"})
	}
	var boxes []mdstore.Checkbox
	for _, a := range opts.Accept {
		boxes = append(boxes, mdstore.Checkbox{Text: a})
	}
	d.Sections = append(d.Sections,
		mdstore.Section{Level: 2, Title: "Acceptance", Content: mdstore.RenderCheckboxes(boxes)},
		mdstore.Section{Level: 2, Title: "Log", Content: ""},
	)

	path := filepath.Join(w.TasksDir(project, model.StatusOpen), fmt.Sprintf("%03d-%s.md", seq, slug))
	if err := mdstore.WriteFile(path, d); err != nil {
		return nil, err
	}
	recordNextTaskSequence(w, project, seq+1)
	return &Task{ID: id, Seq: seq, Slug: slug, Project: project, Status: model.StatusOpen, Title: title, Doc: d, Path: path}, nil
}

// onDiskSeqCeiling returns the highest seq visible in the FILENAMES under the
// project's status folders, parsed or not. It exists for the file the listing
// cannot return: an unparseable task is dropped from ListTasks, so allocation
// scanning parsed tasks alone would hand its number out again.
//
// Best-effort like the git ceiling: an unreadable folder contributes 0 and
// allocation falls back to the other ceilings. It can only raise the seq, never
// lower it, so a failure here cannot make allocation worse than it was.
func onDiskSeqCeiling(w *workspace.Workspace, project string) int {
	if !workspace.SafeSegment(project) {
		return 0
	}
	max := 0
	for _, st := range model.AllStatuses {
		entries, err := os.ReadDir(w.TasksDir(project, st))
		if err != nil {
			continue // a missing status folder is normal
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			if seq := seqFromTaskFilename(e.Name()); seq > max {
				max = seq
			}
		}
	}
	return max
}

// gitTaskSeqCeiling returns the highest task seq that appears in the git
// history of project across EVERY ref, or 0 when the workspace is not in a git
// repo (a bare .dacli, most unit tests) or git is unavailable. It walks
// `git log --all` so a seq committed on an unmerged sibling branch — invisible
// to the working-tree scan — still bars reuse. Best-effort by design: a git
// failure returns 0 and allocation falls back to the working-tree scan, which
// is exactly today's (weaker) behavior, so this can only tighten allocation,
// never break it.
func gitTaskSeqCeiling(w *workspace.Workspace, project string) int {
	if !workspace.SafeSegment(project) || !gitx.Available() {
		return 0
	}
	// NOT memoized, deliberately. Caching this per process is tempting — it is
	// ~200ms and runs under the seq lock, so a batch creator serializes it —
	// but it is not sound: a commit can land between two CreateTask calls in
	// one process, and a stale ceiling means two tasks share a seq, which is
	// silent data corruption rather than a slow command. The cross-branch seq
	// tests encode exactly this and correctly reject the cache.
	//
	// Pathspec relative to the repo root. `--` keeps a project value from ever
	// being read as a git option. Every path a task file has ever occupied in
	// any commit reachable from any ref is listed (a status-folder rename lists
	// both old and new, each carrying the same NNN), so no committed seq escapes.
	pathspec := workspace.Dir + "/projects/" + project + "/tasks"
	out, err := gitx.Run(w.Root, "log", "--all", "--pretty=format:", "--name-only", "--", pathspec)
	if err != nil {
		return 0
	}
	max := 0
	for _, line := range strings.Split(out, "\n") {
		if seq := seqFromTaskFilename(path.Base(strings.TrimSpace(line))); seq > max {
			max = seq
		}
	}
	return max
}

// seqFromTaskFilename extracts the NNN prefix from a task filename
// (NNN-slug.md), or 0 if it has none — the same "digits before the first dash"
// rule loadTaskFile applies to on-disk names, here reused for git-listed paths.
func seqFromTaskFilename(base string) int {
	base = strings.TrimSuffix(base, ".md")
	i := strings.IndexByte(base, '-')
	if i <= 0 {
		return 0
	}
	seq, err := strconv.Atoi(base[:i])
	if err != nil {
		return 0
	}
	return seq
}

// seqLockTimeout bounds how long a caller waits for a held seq lock before
// giving up. Waiting out the timeout does NOT entitle the waiter to the lock:
// a slow holder is still a holder (dacli 247).
const seqLockTimeout = 5 * time.Second

// seqLockStaleAfter is the age past which a lock we cannot disprove is live —
// one recording another machine's pid, or a file we cannot read as a complete
// record — is treated as abandoned. Long, because guessing wrong here costs a
// duplicate seq while guessing conservatively costs a wait.
const seqLockStaleAfter = 60 * time.Second

const (
	seqLockBackoffMin = 2 * time.Millisecond
	seqLockBackoffMax = 100 * time.Millisecond
)

// seqLockClock keeps lock-age decisions and waiter deadlines on one clock.
// Production uses the wall clock; tests inject a monotonic fake so a delayed
// scheduler cannot age a fresh, partially written lock past the stale horizon
// while the assertion is waiting (issue #946).
type seqLockClock struct {
	now   func() time.Time
	sleep func(time.Duration)
}

var wallSeqLockClock = seqLockClock{now: time.Now, sleep: time.Sleep}

// seqLockOwner is what a holder writes into .seq.lock so that anyone who finds
// the file can answer two questions the old empty marker could not: is this
// lock MINE (token), and is its holder still alive (host + pid + pid start)?
type seqLockOwner struct {
	PID      int    `json:"pid"`
	PIDStart string `json:"pid_start"` // ps lstart, defeats pid recycling
	Host     string `json:"host"`
	Token    string `json:"token"`
	TS       string `json:"ts"` // RFC3339Nano, the holder's clock
}

var (
	selfHost     = sync.OnceValue(func() string { h, _ := os.Hostname(); return h })
	selfPIDStart = sync.OnceValue(func() string { s, _ := procmon.ProcStart(os.Getpid()); return s })
)

// writeSeqLockOwner stamps ownership into a freshly created lock file in one
// write, terminated by a newline that readers use as the completeness sentinel.
func writeSeqLockOwner(f *os.File, token string) error {
	b, err := json.Marshal(seqLockOwner{
		PID:      os.Getpid(),
		PIDStart: selfPIDStart(),
		Host:     selfHost(),
		Token:    token,
		TS:       time.Now().UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	return err
}

// readSeqLockOwner parses a lock file. ok=false means we did NOT read a
// complete record — a holder caught between the O_EXCL create and its write, a
// truncated file, or a lock from before this format. Every caller must treat
// that as a live holder: a half-written lock is the youngest lock there is.
func readSeqLockOwner(path string) (seqLockOwner, bool) {
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 || b[len(b)-1] != '\n' {
		return seqLockOwner{}, false
	}
	var o seqLockOwner
	if err := json.Unmarshal(b, &o); err != nil {
		return seqLockOwner{}, false
	}
	if o.Token == "" || o.TS == "" {
		return seqLockOwner{}, false
	}
	if _, err := time.Parse(time.RFC3339Nano, o.TS); err != nil {
		return seqLockOwner{}, false
	}
	return o, true
}

// stale reports whether this lock's holder is demonstrably gone. On our own
// host that is a real answer: the pid either exists with the recorded start
// time or it does not, so a LIVE holder is never stale no matter how long it
// holds. A lock from another host is unprovable — pids are meaningless across
// machines — so it falls back to age alone.
func (o seqLockOwner) stale(now time.Time) bool {
	ts, err := time.Parse(time.RFC3339Nano, o.TS)
	if err != nil {
		return false
	}
	age := now.Sub(ts)
	if o.Host == selfHost() {
		if procmon.AliveIdentity(o.PID, o.PIDStart) {
			return false
		}
		return age >= seqLockTimeout
	}
	return age >= seqLockStaleAfter
}

// seqLockOlderThanAt reports whether the lock FILE (not its content) has gone
// untouched for at least d at the supplied observation time. Used only where
// the content is unreadable.
func seqLockOlderThanAt(path string, d time.Duration, now time.Time) bool {
	fi, err := os.Stat(path)
	if err != nil {
		return false
	}
	return now.Sub(fi.ModTime()) >= d
}

// removeSeqLockIf deletes the lock at path only if the file it actually got
// hold of satisfies want. It renames first so the decision and the deletion
// apply to the same file: two callers cannot both rename the same lock aside,
// so at most one can conclude it removed it. A file that turns out not to be
// the one we judged is put back with a hard link, which fails rather than
// overwrites if a new holder has already claimed the path.
func removeSeqLockIf(path string, want func(victim string) bool) bool {
	victim := path + ".gone-" + ulid.New()
	if err := os.Rename(path, victim); err != nil {
		return false
	}
	if want(victim) {
		_ = os.Remove(victim)
		return true
	}
	if err := os.Link(victim, path); err == nil {
		_ = os.Remove(victim)
	}
	return false
}

// seqLockBreakableAt reports whether the lock at path may be broken at now,
// together with the record that was judged (zero Token = the file could not be
// read, and is breakable only on file age).
func seqLockBreakableAt(path string, now time.Time) (seqLockOwner, bool) {
	if o, ok := readSeqLockOwner(path); ok {
		return o, o.stale(now)
	}
	// Unreadable: assume a holder mid-write until the file itself has been
	// untouched for the full recovery horizon. This age and the caller's wait
	// deadline must be evaluated on the same clock: otherwise scheduler delay
	// can turn a fresh partial write into an abandoned lock (issue #946).
	return seqLockOwner{}, seqLockOlderThanAt(path, seqLockStaleAfter, now)
}

// stealSeqLockWithClock breaks the lock at path if and only if its holder is
// demonstrably gone, and reports whether THIS caller is the one that broke it.
//
// Stealers are serialized by their own O_EXCL guard file. Without it, twenty
// waiters read the same dead lock, all judge it stale, and the ones that lose
// the race go on to move aside whatever file the winner's successor has since
// created — a live lock, displaced by a decision made about a file that is
// already gone. The guard means the "is it stale" read and the removal are not
// interleaved with another steal. It is held for a few syscalls, so a guard
// older than seqLockStaleAfter is garbage from a crash, and clearing one costs
// at most the serialization it was providing.
func stealSeqLockWithClock(path string, clock seqLockClock) bool {
	if _, ok := seqLockBreakableAt(path, clock.now()); !ok {
		return false
	}
	guard := path + ".steal"
	f, err := os.OpenFile(guard, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if os.IsExist(err) && seqLockOlderThanAt(guard, seqLockStaleAfter, clock.now()) {
			_ = os.Remove(guard)
		}
		return false
	}
	_ = f.Close()
	defer func() { _ = os.Remove(guard) }()

	// Re-read under the guard: the lock may have been broken and retaken while
	// we were queueing for it.
	o, ok := seqLockBreakableAt(path, clock.now())
	if !ok {
		return false
	}
	return removeSeqLockIf(path, func(victim string) bool {
		got, parsed := readSeqLockOwner(victim)
		if o.Token == "" {
			return !parsed && seqLockOlderThanAt(victim, seqLockStaleAfter, clock.now())
		}
		return parsed && got.Token == o.Token && got.stale(clock.now())
	})
}

// acquireSeqLock claims an exclusive, cross-process lock on seq allocation for
// project, via O_EXCL on a marker file: creation is atomic even across
// processes sharing the workspace directory (worktrees redirect to the one
// shared .dacli, per workspace.Find), which a plain in-memory mutex would not
// cover. The returned func releases the lock; callers must defer it.
//
// The lock is owned, not merely present. The holder writes its pid, its pid
// start time, its host and a random token into the file; release removes only
// a file still carrying that token, and a waiter breaks a lock only when it
// can show the holder is gone — a dead pid on this host, or an hour-old file
// from somewhere else. Waiting out seqLockTimeout buys an error, not the lock:
// a wedged lock is something a human can delete, whereas two callers holding
// it at once silently gives two tasks the same NNN, which is the corruption
// this lock exists to prevent (dacli 209, 247).
//
// Assumptions: O_EXCL create, rename and link are atomic on the filesystem
// holding the workspace (local filesystems, and NFSv3+ where Go maps these to
// the exclusive-create and rename RPCs). Pid liveness is consulted ONLY for a
// lock recording this host — across a network filesystem another machine's pid
// says nothing about our process table, so those locks are broken on age
// alone, and clock skew between the two machines is charged against that age.
func acquireSeqLock(w *workspace.Workspace, project string) (func(), error) {
	return acquireFileLock(filepath.Join(w.ProjectDir(project), ".seq.lock"))
}

// acquireFileLock is the generic half of acquireSeqLock: the O_EXCL marker,
// the owned-lock discipline, the steal-when-provably-dead rule and the
// wait-then-error timeout, for any lock path. Seq allocation was the first
// thing to need it; per-task read-modify-write is the second (see WithTask).
func acquireFileLock(path string) (func(), error) {
	return acquireFileLockWithClock(path, wallSeqLockClock)
}

func acquireFileLockWithClock(path string, clock seqLockClock) (func(), error) {
	token := ulid.New()
	deadline := clock.now().Add(seqLockTimeout)
	backoff := seqLockBackoffMin
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			if err := writeSeqLockOwner(f, token); err != nil {
				_ = f.Close()
				_ = os.Remove(path)
				return nil, fmt.Errorf("seq lock: %w", err)
			}
			_ = f.Close()
			return func() {
				removeSeqLockIf(path, func(victim string) bool {
					got, ok := readSeqLockOwner(victim)
					return ok && got.Token == token
				})
			}, nil
		}
		if !os.IsExist(err) {
			return nil, err
		}
		// A steal frees the path for everyone, so re-race for it rather than
		// assuming it is ours.
		if stealSeqLockWithClock(path, clock) {
			continue
		}
		if !clock.now().Before(deadline) {
			return nil, fmt.Errorf("seq lock %s is held by %s after waiting %s; if the holder is gone, delete the file", path, describeSeqLockHolder(path), seqLockTimeout)
		}
		clock.sleep(backoff)
		if backoff < seqLockBackoffMax {
			backoff *= 2
		}
	}
}

// WithFileLock serializes fn across processes using the store's owned,
// stale-aware lock discipline. Feature slices use this narrow wrapper when a
// multi-file state transition must not race with another dacli process.
func WithFileLock(path string, fn func() error) error {
	release, err := acquireFileLock(path)
	if err != nil {
		return err
	}
	defer release()
	return fn()
}

// describeSeqLockHolder renders the current holder for an error message.
func describeSeqLockHolder(path string) string {
	o, ok := readSeqLockOwner(path)
	if !ok {
		return "an unreadable lock file (a holder mid-write, or a lock from an older dacli)"
	}
	return fmt.Sprintf("pid %d on %s since %s", o.PID, o.Host, o.TS)
}

// ListTasks returns tasks for a project (or all projects if project == ""),
// optionally filtered by status. Status comes from the folder, never from
// frontmatter.
//
// A task whose file exists in two status folders (the duplicate-task drift that
// once let a stale open/ copy shadow the authoritative done/ copy and made
// FindTask report the task as "ambiguous" with itself) is yielded ONCE here:
// dedupeTasks keeps the most-terminal copy. The raw, still-duplicated view is
// available via DuplicateTaskFiles for the doctor check that surfaces the drift.
func ListTasks(w *workspace.Workspace, project string, status model.Status) ([]*Task, error) {
	all, err := listTasksRaw(w, project, status)
	if err != nil {
		return nil, err
	}
	out := dedupeTasks(all)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Project != out[j].Project {
			return out[i].Project < out[j].Project
		}
		return out[i].Seq < out[j].Seq
	})
	return out, nil
}

// listTasksRaw walks every requested status folder and loads every task file
// WITHOUT deduping — a task present in two folders appears twice. Callers that
// want the resolved, one-per-task view use ListTasks; only the duplicate-drift
// detector (DuplicateTaskFiles) needs the raw, still-duplicated list.
func listTasksRaw(w *workspace.Workspace, project string, status model.Status) ([]*Task, error) {
	var projects []string
	if project != "" {
		projects = []string{project}
	} else {
		ps, err := ListProjects(w)
		if err != nil {
			return nil, err
		}
		for _, p := range ps {
			projects = append(projects, p.Slug)
		}
	}

	var out []*Task
	for _, proj := range projects {
		for _, st := range model.AllStatuses {
			if status != "" && st != status {
				continue
			}
			dir := w.TasksDir(proj, st)
			entries, err := os.ReadDir(dir)
			if err != nil {
				// Only a MISSING status folder is normal (a project with
				// nothing blocked has no blocked/). Any other failure —
				// EACCES, EMFILE under a wide agent wave — must not read as
				// "this folder is empty": an unreadable backlog is not
				// evidence of an empty one, and the seq allocator scans this
				// same list, so a partial view can reissue a live number.
				if os.IsNotExist(err) {
					continue
				}
				return nil, fmt.Errorf("reading %s: %w", dir, err)
			}
			for _, e := range entries {
				if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
					continue
				}
				path := filepath.Join(dir, e.Name())
				t, err := loadTaskFile(path, proj, st)
				if err != nil {
					// A task file that does not parse used to vanish from every
					// list — including doctor's, whose corrupt-object check
					// iterates this very output, so nothing could ever report
					// it. Its seq also became invisible to the allocator, so
					// the next `task add` could reissue it. Record it instead;
					// callers surface it (doctor) and the list stays usable.
					noteBrokenTaskFile(path, err)
					continue
				}
				// It parsed, so whatever was wrong with it is fixed. Clearing
				// here is what keeps the record a statement about the CURRENT
				// workspace rather than a permanent accusation: the record is
				// process-global, so in any process that outlives one command
				// — the MCP server, the dashboard, a test binary — a file
				// repaired between two listings would otherwise be reported
				// broken forever, and doctor would name a problem the owner
				// had already fixed.
				forgetBrokenTaskFile(path)
				out = append(out, t)
			}
		}
	}
	return out, nil
}

// taskIdentity is the key that decides "same task" for dedup and duplicate
// detection: the ULID id when present (globally unique), else project+seq (the
// NNN prefix is unique within a project). Two files sharing this key ARE the
// same task, even when they sit in different status folders.
func taskIdentity(t *Task) string {
	if t.ID != "" {
		return "id:" + t.ID
	}
	return fmt.Sprintf("seq:%s/%d", t.Project, t.Seq)
}

// statusRank orders statuses by how terminal they are, using the canonical
// AllStatuses order (open < active < blocked < done). A done copy outranks an
// open one, so a stale open duplicate never wins over the authoritative done
// file.
func statusRank(s model.Status) int {
	for i, st := range model.AllStatuses {
		if st == s {
			return i
		}
	}
	return -1
}

// taskModTime is the file's mtime, zero on any stat error — used only to
// tie-break two copies in the same status.
func taskModTime(t *Task) time.Time {
	if fi, err := os.Stat(t.Path); err == nil {
		return fi.ModTime()
	}
	return time.Time{}
}

// preferTask reports whether candidate a should replace incumbent b as the
// surviving copy of a duplicated task: the more terminal status wins, ties
// broken by the most-recently-modified file.
func preferTask(a, b *Task) bool {
	if ra, rb := statusRank(a.Status), statusRank(b.Status); ra != rb {
		return ra > rb
	}
	return taskModTime(a).After(taskModTime(b))
}

// dedupeTasks collapses tasks that resolve to the same identity but were found
// in more than one status folder, keeping the preferred (most terminal) copy.
// Input order is otherwise preserved; ListTasks re-sorts afterward.
func dedupeTasks(tasks []*Task) []*Task {
	best := make(map[string]*Task, len(tasks))
	var order []string
	for _, t := range tasks {
		key := taskIdentity(t)
		cur, ok := best[key]
		if !ok {
			best[key] = t
			order = append(order, key)
			continue
		}
		if preferTask(t, cur) {
			best[key] = t
		}
	}
	out := make([]*Task, 0, len(order))
	for _, k := range order {
		out = append(out, best[k])
	}
	return out
}

// DuplicateTask names a task whose file exists in more than one status folder.
type DuplicateTask struct {
	Project string
	Seq     int
	Slug    string
	Paths   []string // every status-folder path holding a copy, sorted
}

// DuplicateTaskFiles reports every task identity (id/seq) whose file appears in
// more than one status folder — the drift that ListTasks now hides by deduping.
// The doctor check surfaces it so a stale duplicate stays visible instead of
// being silently resolved.
func DuplicateTaskFiles(w *workspace.Workspace) ([]DuplicateTask, error) {
	all, err := listTasksRaw(w, "", "")
	if err != nil {
		return nil, err
	}
	groups := map[string][]*Task{}
	var order []string
	for _, t := range all {
		key := taskIdentity(t)
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], t)
	}
	var dups []DuplicateTask
	for _, k := range order {
		g := groups[k]
		if len(g) < 2 {
			continue
		}
		d := DuplicateTask{Project: g[0].Project, Seq: g[0].Seq, Slug: g[0].Slug}
		for _, t := range g {
			d.Paths = append(d.Paths, t.Path)
		}
		sort.Strings(d.Paths)
		dups = append(dups, d)
	}
	return dups, nil
}

// CollidedSeq names a single project/seq that DIFFERENT tasks claim — the
// wreckage a cross-branch seq collision (dacli 251) leaves once both branches
// merge: two files with distinct ids (and usually distinct slugs) sharing one
// NNN, which makes `dacli <NNN>` ambiguous and unaddressable.
type CollidedSeq struct {
	Project string
	Seq     int
	Slugs   []string // one per colliding task, sorted, id-annotated
}

// CollidedSeqs reports every project/seq owned by more than one DISTINCT task.
// It is deliberately distinct from DuplicateTaskFiles: that groups by identity
// to catch one task spread across status folders, whereas this groups by
// project+seq to catch the opposite — different tasks that ended up with the
// same number. Surfacing it is the reconciliation the format cannot do
// silently: a seq is a reference (branch names, worktree paths, PR titles all
// derive from it), so renumbering is an owner decision, and doctor's job is to
// make the collision loud rather than leave it buried behind an "ambiguous ref"
// error at the point of use.
func CollidedSeqs(w *workspace.Workspace) ([]CollidedSeq, error) {
	all, err := ListTasks(w, "", "")
	if err != nil {
		return nil, err
	}
	type key struct {
		project string
		seq     int
	}
	groups := map[key][]*Task{}
	var order []key
	for _, t := range all {
		k := key{t.Project, t.Seq}
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], t)
	}
	var out []CollidedSeq
	for _, k := range order {
		g := groups[k]
		if len(g) < 2 {
			continue
		}
		c := CollidedSeq{Project: k.project, Seq: k.seq}
		for _, t := range g {
			c.Slugs = append(c.Slugs, fmt.Sprintf("%s (%s)", t.Slug, clikitOrDash(t.ID)))
		}
		sort.Strings(c.Slugs)
		out = append(out, c)
	}
	return out, nil
}

// clikitOrDash renders an empty id as a dash so a hollow (frontmatter-less)
// colliding file still reads cleanly in the doctor line.
func clikitOrDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// OwnerHasLiveRun scans every recorded run's proc.txt for one whose Child is
// ownerID and whose process is still alive (probed live, per procmon's
// contract — never trusted from the file). No live run means the owner
// finished (or never ran as a spawn at all); the doctor's orphaned-task check
// uses this to tell "not me" (accept's ordinary grant gate) apart from
// "provably dead" (worth suggesting --force for).
func OwnerHasLiveRun(w *workspace.Workspace, ownerID string) bool {
	entries, err := os.ReadDir(w.RunsDir())
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		rec, err := procmon.ReadRecord(filepath.Join(w.RunDir(e.Name()), "proc.txt"))
		if err != nil {
			continue
		}
		if rec.Child == ownerID && procmon.AliveRecord(rec) {
			return true
		}
	}
	return false
}

// OwnerTaskHasRecoveryLease reports whether ownership must remain with ownerID.
// A live process for that owner always wins, even when it is working another
// task; a fresh transcript only fences recovery of the task it names. The
// latter preserves work whose launcher disappeared while its child kept the
// inherited transcript descriptor open (issue #672).
func OwnerTaskHasRecoveryLease(w *workspace.Workspace, ownerID, taskID string) (bool, error) {
	entries, err := os.ReadDir(w.RunsDir())
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("read recovery runs: %w", err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		runDir := w.RunDir(e.Name())
		rec, err := procmon.ReadRecord(filepath.Join(runDir, "proc.txt"))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return false, fmt.Errorf("read recovery run %s: %w", e.Name(), err)
		}
		if rec.Child == "" {
			return false, fmt.Errorf("read recovery run %s: proc.txt has no child", e.Name())
		}
		if rec.Child != ownerID || rec.Outcome != "" {
			continue
		}
		if procmon.AliveRecord(rec) {
			return true, nil
		}
		if rec.Task != taskID {
			continue
		}
		if _, err := os.Stat(filepath.Join(runDir, "runtime-exit.txt")); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return false, fmt.Errorf("inspect recovery run %s exit marker: %w", e.Name(), err)
		}
		if info, err := os.Stat(filepath.Join(runDir, "transcript.log")); err == nil && info.Size() > 0 {
			age := time.Since(info.ModTime())
			if age >= 0 && age < 15*time.Second {
				return true, nil
			}
		} else if err != nil && !os.IsNotExist(err) {
			return false, fmt.Errorf("inspect recovery run %s transcript: %w", e.Name(), err)
		}
	}
	return false, nil
}

func loadTaskFile(path, project string, st model.Status) (*Task, error) {
	d, err := mdstore.ReadFile(path)
	if err != nil {
		return nil, err
	}
	t := &Task{Project: project, Status: st, Doc: d, Path: path}
	t.ID, _ = d.Front.Get("id")
	base := strings.TrimSuffix(filepath.Base(path), ".md")
	if i := strings.Index(base, "-"); i > 0 {
		t.Seq, _ = strconv.Atoi(base[:i])
		t.Slug = base[i+1:]
	}
	for _, s := range d.Sections {
		if s.Level == 1 {
			t.Title = s.Title
			break
		}
	}
	return t, nil
}

// matchesRef reports whether ref names task t.
func matchesRef(t *Task, ref string) bool {
	for _, key := range taskRefKeys(t) {
		if key == ref {
			return true
		}
	}
	return false
}

// splitQualifiedTaskRef recognizes the one slash in <project>/<task-ref>.
// Project slugs are already constrained to one safe path segment, so refs
// with extra slashes are not partially interpreted as a different project.
func splitQualifiedTaskRef(ref string) (project, taskRef string, ok bool) {
	project, taskRef, ok = strings.Cut(ref, "/")
	return project, taskRef, ok && project != "" && taskRef != "" && !strings.Contains(taskRef, "/")
}

func taskRefKeys(t *Task) []string {
	seq3 := fmt.Sprintf("%03d", t.Seq)
	return []string{
		t.ID,
		strings.TrimPrefix(t.ID, "t-"),
		t.Slug,
		strconv.Itoa(t.Seq),
		seq3,
		seq3 + "-" + t.Slug,
	}
}

// resolveRef applies FindTask's ambiguity contract to a pre-loaded task set —
// exactly one hit or an error. Shared by FindTask (one-shot) and TaskIndex
// (the hot-loop path) so both spell "ambiguity is an error, not a guess" the
// same way.
func resolveRef(hits []*Task, ref string) (*Task, error) {
	switch len(hits) {
	case 0:
		return nil, ErrNotFound{Ref: ref}
	case 1:
		return hits[0], nil
	default:
		var names []string
		for _, h := range hits {
			names = append(names, fmt.Sprintf("%s/%03d-%s", h.Project, h.Seq, h.Slug))
		}
		return nil, fmt.Errorf("ref %q is ambiguous: %s", ref, strings.Join(names, ", "))
	}
}

// FindTask resolves a ref — ULID id, t-<ULID>, NNN, or slug — searching all
// projects. Ambiguity is an error, not a guess: acting on the wrong task
// because a slug matched twice is the silent version of a collision.
//
// This reads the whole task tree once. Callers that resolve many refs in a
// loop (Sync, Taint) must build a TaskIndex once and reuse it instead — see
// BuildTaskIndex — or they re-read every task file per ref, O(refs×tasks).
func FindTask(w *workspace.Workspace, ref string) (*Task, error) {
	project, taskRef, qualified := splitQualifiedTaskRef(ref)
	if qualified {
		if _, err := LoadProject(w, project); err != nil {
			return nil, err
		}
		ref = taskRef
	} else {
		project = ""
	}
	all, err := ListTasks(w, project, "")
	if err != nil {
		return nil, err
	}
	var hits []*Task
	for _, t := range all {
		if matchesRef(t, ref) {
			hits = append(hits, t)
		}
	}
	t, err := resolveRef(hits, ref)
	if qualified && isNotFound(err) {
		return nil, ErrNotFound{Ref: "project/" + project + "/task/" + taskRef}
	}
	return t, err
}

// TaskIndex resolves task refs against a task set read once, turning the
// O(refs×tasks) blowup of calling FindTask in a loop into O(tasks) up front
// plus O(1) per lookup. Build it once outside the loop and Find inside.
type TaskIndex struct {
	byRef         map[string][]*Task
	knownProjects map[string]bool
}

// BuildTaskIndex reads the whole task tree once and indexes every ref form
// (ULID, t-<ULID>, NNN, seq, slug, NNN-slug) to its task.
func BuildTaskIndex(w *workspace.Workspace) (*TaskIndex, error) {
	all, err := ListTasks(w, "", "")
	if err != nil {
		return nil, err
	}
	projects, err := ListProjects(w)
	if err != nil {
		return nil, err
	}
	known := make(map[string]bool, len(projects))
	for _, project := range projects {
		known[project.Slug] = true
	}
	return newTaskIndex(all, known), nil
}

// NewTaskIndex indexes an already-loaded task set.
func NewTaskIndex(tasks []*Task) *TaskIndex {
	known := make(map[string]bool)
	for _, task := range tasks {
		known[task.Project] = true
	}
	return newTaskIndex(tasks, known)
}

func newTaskIndex(tasks []*Task, knownProjects map[string]bool) *TaskIndex {
	idx := &TaskIndex{
		byRef:         make(map[string][]*Task, len(tasks)*12),
		knownProjects: knownProjects,
	}
	add := func(key string, t *Task) {
		if key == "" {
			return
		}
		// A task must not appear twice for one ref even if two of its key
		// forms collide (e.g. slug that looks like a seq) — dedupe on identity.
		for _, existing := range idx.byRef[key] {
			if existing == t {
				return
			}
		}
		idx.byRef[key] = append(idx.byRef[key], t)
	}
	for _, t := range tasks {
		for _, key := range taskRefKeys(t) {
			add(key, t)
			add(t.Project+"/"+key, t)
		}
	}
	return idx
}

// Find resolves one ref with the same ambiguity contract as FindTask.
func (idx *TaskIndex) Find(ref string) (*Task, error) {
	project, taskRef, qualified := splitQualifiedTaskRef(ref)
	if !qualified {
		return resolveRef(idx.byRef[ref], ref)
	}
	if !idx.knownProjects[project] {
		return nil, ErrNotFound{Ref: "project/" + project}
	}
	t, err := resolveRef(idx.byRef[ref], ref)
	if isNotFound(err) {
		return nil, ErrNotFound{Ref: "project/" + project + "/task/" + taskRef}
	}
	return t, err
}

// SaveTask rewrites a task in place.
func SaveTask(t *Task) error { return mdstore.WriteFile(t.Path, t.Doc) }

// MoveTask changes status by moving the file — the folder is the single
// source of truth, so this is the only way status ever changes.
//
// A move must leave the task in EXACTLY ONE status folder. os.Rename already
// removes the source, but a pre-existing stale copy in a THIRD folder (the
// root cause of the 026 duplicate: the file lived in both open/ and done/)
// would survive it. So after the move we sweep every other status folder for a
// same-named copy and remove it — a leftover source copy can never outlast a
// move.
func MoveTask(w *workspace.Workspace, t *Task, to model.Status) error {
	base := filepath.Base(t.Path)
	src := t.Path
	dst := filepath.Join(w.TasksDir(t.Project, to), base)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := os.Rename(t.Path, dst); err != nil {
		return err
	}
	t.Path, t.Status = dst, to
	for _, st := range model.AllStatuses {
		if st == to {
			continue
		}
		stale := filepath.Join(w.TasksDir(t.Project, st), base)
		if stale == dst {
			continue
		}
		if _, err := os.Stat(stale); err == nil {
			_ = os.Remove(stale)
		}
	}
	stageTaskMove(w, src, dst)
	return nil
}

// AppendLog adds a timestamped line to the task's ## Log.
func AppendLog(t *Task, line string) {
	s, _ := t.Doc.Section("Log")
	t.Doc.SetSection("Log", s.Content+fmt.Sprintf("- %s %s\n", now(), line))
}

// RecordedPRURL returns the newest pull-request URL materialized into a task's
// log by `dacli pr`. Looking the PR up by this durable identity still works
// after GitHub deletes the merged head branch; a head-only lookup does not.
func RecordedPRURL(t *Task) string {
	s, ok := t.Doc.Section("Log")
	if !ok {
		return ""
	}
	lines := strings.Split(s.Content, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		for _, field := range strings.Fields(lines[i]) {
			field = strings.TrimRight(field, ").,;]")
			if strings.HasPrefix(field, "https://") && strings.Contains(field, "/pull/") {
				return field
			}
		}
	}
	return ""
}

// CloseTask is the ONE canonical way a task is closed: it stamps the
// actuals-capture field "completed by <actor>" onto the Log and moves the task
// to done. Both `task done` and `accept` route through it so no path can close a
// task without the stamp that calibration's claim→completion span reads — the
// drift that once let `accept` close a task with no actuals (E1) cannot recur.
// Callers that persist other Log lines (e.g. accept's "accepted by") append
// them before calling; this SaveTask flushes them together.
func CloseTask(w *workspace.Workspace, t *Task, actor string) error {
	return closeTask(w, t, actor, false)
}

// CloseTaskAfterLandingDecision is the only close entry point for a caller
// that has already made and recorded the explicit landing decision. accept
// owns that decision (including its named --allow-unlanded escape); task done
// and proposal sync deliberately do not (issue #900).
func CloseTaskAfterLandingDecision(w *workspace.Workspace, t *Task, actor string) error {
	return closeTask(w, t, actor, true)
}

func closeTask(w *workspace.Workspace, t *Task, actor string, landingDecided bool) error {
	if err := RefuseIncompleteDelivery(w, t); err != nil {
		return err
	}
	if err := RefuseIncompleteAggregate(w, t); err != nil {
		return err
	}
	project, err := LoadProject(w, t.Project)
	if err != nil {
		return err
	}
	landing, _, err := model.ResolveLanding(project.Landing, model.LandingOverride{})
	if err != nil {
		return err
	}
	if landing.Mode == model.LandingPR && !landingDecided {
		base := landing.Base
		if base == "" {
			base = "main"
		}
		state, branch := CheckLanded(w, t, base)
		stateName := map[LandingState]string{LandingUnknown: "unknown", LandingNoBranch: "no branch", LandingLanded: "landed-awaiting-acceptance", LandingUnlanded: "unlanded"}[state]
		t.Doc.Front.Set("completion_state", "implemented-unlanded")
		t.Doc.Front.Set("completion_requested_by", actor)
		t.Doc.Front.Set("completion_requested_at", now())
		AppendLog(t, fmt.Sprintf("completion requested by %s; PR landing state %s on %s", actor, stateName, base))
		if err := SaveTask(t); err != nil {
			return err
		}
		return Refusedf("PR-mode task %03d is implemented but not canonically accepted on %s (branch %s; landing state %s): durable state is implemented-unlanded; use dacli ship --tasks %d --pr --verify \"<cmd>\" or dacli accept after the exact head lands", t.Seq, base, branch, stateName, t.Seq)
	}
	t.Doc.Front.Delete("completion_state")
	t.Doc.Front.Delete("completion_requested_by")
	t.Doc.Front.Delete("completion_requested_at")
	AppendLog(t, "completed by "+actor)
	if err := SaveTask(t); err != nil {
		return err
	}
	return MoveTask(w, t, model.StatusDone)
}

// LogHasStamp reports whether the task's Log has a line whose text (after the
// RFC3339 timestamp) begins with prefix — e.g. "claimed by", "completed by".
// The doctor data-integrity check uses it to find broken calibration spans (a
// claim with no completion), mirroring calibration.logSpan's stamp parsing.
func LogHasStamp(t *Task, prefix string) bool {
	s, ok := t.Doc.Section("Log")
	if !ok {
		return false
	}
	for _, line := range strings.Split(s.Content, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "- "))
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if strings.HasPrefix(strings.Join(fields[1:], " "), prefix) {
			return true
		}
	}
	return false
}

// ClaimedBy returns the agent id from the task's most recent "claimed by"
// stamp — the agent performing the current/final work cycle — or "" when the
// task was never claimed. A respawn mints a new identity; returning the first
// historical claimant would attribute later work to a retired agent and make
// the independence gate inspect the wrong implementer (issue #725).
func ClaimedBy(t *Task) string {
	s, ok := t.Doc.Section("Log")
	if !ok {
		return ""
	}
	var claimant string
	for _, line := range strings.Split(s.Content, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "- "))
		fields := strings.Fields(line)
		// "<timestamp> claimed by <agent-id>"
		if len(fields) >= 4 && fields[1] == "claimed" && fields[2] == "by" {
			claimant = fields[3]
		}
	}
	return claimant
}

// --- Notes ---

type NoteOpts struct {
	About    string
	Rejected string
	Because  string
	Severity string
	Scope    string // project | workspace — the P1 capture field
	Origin   string // agent | file:<path> | external:<who> — the P4 taint field
	Against  string // an agent id whose work this finding concerns — the review field
	Body     string

	// SourceEvent is the id of the event this note materializes from, stamped
	// into frontmatter so eventlog.Sync is idempotent: a mid-apply failure
	// re-runs from the top, and without this a second CreateNote would write a
	// duplicate finding note under a fresh ULID suffix. When set, CreateNote
	// returns the existing note instead of writing a second one.
	SourceEvent string
}

// CreateNote writes a decision, finding, metric, or ref note.
func CreateNote(w *workspace.Workspace, actor, project string, kind model.NoteKind, title string, opts NoteOpts) (string, error) {
	if _, err := LoadProject(w, project); err != nil {
		return "", err
	}
	if kind == model.NoteDecision && opts.Rejected == "" {
		// A decision without a rejection cannot be safely revisited; refusing
		// here is cheaper than dacli lint flagging it later.
		//
		// Wrapped in ErrRefused so it exits 3, not 1. This is a POLICY answer —
		// retrying it unchanged can never succeed — and the 1/3 distinction is
		// the one a supervisor acts on. store cannot import clikit (clikit
		// imports store), so the sentinel is the seam, exactly as
		// agentid.ErrEmptyToken already is.
		return "", Refusedf("a decision must record what was rejected (--rejected)")
	}

	// Idempotency: if this event already materialized a note here, return it
	// rather than writing a duplicate. This is what lets Sync re-run a
	// partially-applied event safely.
	if opts.SourceEvent != "" {
		if path, ok := noteBySourceEvent(w, project, kind, opts.SourceEvent); ok {
			return path, nil
		}
	}

	slug := Slugify(title)
	prefix := map[model.NoteKind]string{
		model.NoteDecision: "d-", model.NoteFinding: "f-", model.NoteMetric: "m-", model.NoteRef: "r-",
	}[kind]

	d := &mdstore.Doc{}
	d.Front.Set("id", prefix+slug)
	d.Front.Set("kind", string(model.KindNote))
	d.Front.Set("note_kind", string(kind))
	d.Front.Set("created", now())
	d.Front.Set("created_by", actor)
	if opts.About != "" {
		d.Front.Set("about", "[["+opts.About+"]]")
	}
	if opts.Severity != "" {
		d.Front.Set("severity", opts.Severity)
	}
	if opts.Scope != "" {
		d.Front.Set("scope", opts.Scope)
	}
	if opts.Origin != "" && opts.Origin != "agent" {
		// Provenance must survive event→note, or `dacli taint` dead-ends at
		// sync: a finding derived from a hostile file loses the file the
		// moment the owner materializes it. This is the P4 chain's weld.
		d.Front.Set("origin", opts.Origin)
	}
	if opts.Against != "" {
		// The reviewed agent: `dacli contrib` joins on this to show which
		// role drew which findings — the improve-the-team signal.
		d.Front.Set("against", opts.Against)
	}
	if opts.SourceEvent != "" {
		// The materializing event, so a re-synced event finds its own note
		// instead of writing a second one (see NoteOpts.SourceEvent).
		d.Front.Set("source_event", opts.SourceEvent)
	}

	d.Sections = []mdstore.Section{{Level: 1, Title: title, Content: ""}}
	if kind == model.NoteDecision {
		d.Sections = append(d.Sections,
			mdstore.Section{Level: 2, Title: "Chose", Content: firstNonEmpty(opts.Body, title) + "\n"},
			mdstore.Section{Level: 2, Title: "Rejected", Content: opts.Rejected + "\n"},
			mdstore.Section{Level: 2, Title: "Because", Content: opts.Because + "\n"},
		)
	} else if opts.Body != "" {
		d.Sections = append(d.Sections, mdstore.Section{Level: 0, Content: opts.Body + "\n"})
	}

	path := filepath.Join(w.NotesDir(project, kind), slug+".md")
	// Same-titled notes must not clobber each other — sync materializes
	// findings from events, and two agents finding the same thing is normal.
	if _, err := os.Stat(path); err == nil {
		suffix := strings.ToLower(ulid.New())
		path = filepath.Join(w.NotesDir(project, kind), slug+"-"+suffix[len(suffix)-6:]+".md")
		d.Front.Set("id", prefix+slug+"-"+suffix[len(suffix)-6:])
	}
	if err := mdstore.WriteFile(path, d); err != nil {
		return "", err
	}
	return path, nil
}

// GradeFinding stamps a verify verdict onto a finding note's frontmatter as a
// `trust:` key ("confirmed" | "refuted"), so the grade rides with the finding
// into every sibling brief BEFORE a child acts on it (D3). ref matches the
// note's id or its level-1 title — verify identifies the judged finding by its
// claim text, which is that title. A finding note with no `trust:` key is
// ungraded, which the trust-floor reads as "unverified". Returns the graded
// note's id, or an error if no finding note in the project matches ref.
func GradeFinding(w *workspace.Workspace, project, ref, trust string) (string, error) {
	dir := w.NotesDir(project, model.NoteFinding)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	type cand struct {
		path string
		doc  *mdstore.Doc
		id   string
	}
	// Prefer an exact id match — an id is unique, so it targets exactly the
	// intended note regardless of os.ReadDir order. Only if no id matches do we
	// fall back to the level-1 title (the claim text verify passes). Two notes
	// can legitimately share a title (CreateNote's own collision path exists
	// because "two agents finding the same thing is normal"), so a title match
	// is graded across ALL twins rather than stamping whichever the filesystem
	// happened to list first: the verdict is about the claim, and every note
	// asserting it earns the same grade. This makes the outcome deterministic.
	var idMatch *cand
	var titleMatches []*cand
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		d, err := mdstore.ReadFile(path)
		if err != nil {
			continue
		}
		id, _ := d.Front.Get("id")
		title := ""
		for _, s := range d.Sections {
			if s.Level == 1 {
				title = s.Title
				break
			}
		}
		if id == ref {
			idMatch = &cand{path: path, doc: d, id: id}
			break
		}
		if title != "" && title == ref {
			titleMatches = append(titleMatches, &cand{path: path, doc: d, id: id})
		}
	}
	grade := func(c *cand) error {
		c.doc.Front.Set("trust", trust)
		return mdstore.WriteFile(c.path, c.doc)
	}
	if idMatch != nil {
		if err := grade(idMatch); err != nil {
			return "", err
		}
		return idMatch.id, nil
	}
	if len(titleMatches) == 0 {
		return "", fmt.Errorf("no finding note in project %s matches %q", project, ref)
	}
	ids := make([]string, 0, len(titleMatches))
	for _, c := range titleMatches {
		if err := grade(c); err != nil {
			return "", err
		}
		ids = append(ids, c.id)
	}
	sort.Strings(ids)
	return strings.Join(ids, ","), nil
}

// ListNotes returns parsed notes of one kind for a project.
func ListNotes(w *workspace.Workspace, project string, kind model.NoteKind) ([]*mdstore.Doc, error) {
	dir := w.NotesDir(project, kind)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // no notes dir yet is not an error
		}
		return nil, err // a real I/O/permission failure must not read as "empty"
	}
	var out []*mdstore.Doc
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		if d, err := mdstore.ReadFile(filepath.Join(dir, e.Name())); err == nil {
			out = append(out, d)
		}
	}
	return out, nil
}

// noteBySourceEvent returns the path of an existing note of this kind that was
// materialized from the given event id, if any. It reads the notes dir
// directly (the exact frontmatter field), not through ListNotes, so a real
// read error surfaces rather than masquerading as "no match".
func noteBySourceEvent(w *workspace.Workspace, project string, kind model.NoteKind, eventID string) (string, bool) {
	dir := w.NotesDir(project, kind)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false // dir-absent or unreadable: nothing to dedupe against
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		src, ok := sourceEventOf(path, e)
		if !ok {
			continue
		}
		if src == eventID {
			return path, true
		}
	}
	return "", false
}

// sourceEventOf reads one note's source_event, memoized on (path, mtime, size).
//
// The directory is still listed on every call, so a note another process just
// wrote is seen — the dedup this backs must not go stale. What the cache skips
// is the PARSE, which is the actual cost: eventlog.Sync calls this once per
// finding event, and each call used to parse every finding note in the project.
// With 345 notes that is O(events x notes) — a wave syncing 50 findings did
// ~17k file parses to answer a question whose answer had not changed. Notes are
// effectively immutable once written, so the cache hits almost always, and a
// changed mtime or size re-parses.
func sourceEventOf(path string, e os.DirEntry) (string, bool) {
	fi, err := e.Info()
	if err != nil {
		return "", false
	}
	key := path
	stamp := noteStamp{mod: fi.ModTime().UnixNano(), size: fi.Size()}

	noteSrcMu.RLock()
	c, hit := noteSrcCache[key]
	noteSrcMu.RUnlock()
	if hit && c.stamp == stamp {
		return c.src, true
	}

	d, err := mdstore.ReadFile(path)
	if err != nil {
		return "", false
	}
	src, _ := d.Front.Get("source_event")

	noteSrcMu.Lock()
	noteSrcCache[key] = noteSrcEntry{stamp: stamp, src: src}
	noteSrcMu.Unlock()
	return src, true
}

type noteStamp struct {
	mod  int64
	size int64
}

type noteSrcEntry struct {
	stamp noteStamp
	src   string
}

var (
	noteSrcMu    sync.RWMutex
	noteSrcCache = map[string]noteSrcEntry{}
)

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// --- Unparseable task files.
//
// mdstore.Parse hard-errors on malformed frontmatter, deliberately: "a file
// half-understood is a file about to be corrupted". But listTasksRaw used to
// swallow that error with a bare `continue`, which made a corrupt task
// invisible to EVERY reader — `next` skipped it, `status` under-counted, and
// doctor could not report it because doctor's own check iterates ListTasks
// output. Its seq was invisible to the allocator too, so `task add` could
// reissue a live number.
//
// Conflict markers inside frontmatter are the realistic trigger: `.dacli` is a
// git-tracked, agent-written tree, so merge conflicts on task files are a
// live hazard rather than a hypothetical one.
//
// The record is process-global and additive because listTasksRaw is called
// from everywhere and threading a second return value through every caller
// would be a far larger change than the bug warrants. Readers that care
// (doctor) drain it; everyone else is unaffected.

// BrokenTaskFile is one task file that exists but could not be parsed.
type BrokenTaskFile struct {
	Path string
	Err  error
}

var (
	brokenMu    sync.Mutex
	brokenTasks = map[string]BrokenTaskFile{}
)

func noteBrokenTaskFile(path string, err error) {
	brokenMu.Lock()
	defer brokenMu.Unlock()
	brokenTasks[path] = BrokenTaskFile{Path: path, Err: err}
}

func forgetBrokenTaskFile(path string) {
	brokenMu.Lock()
	defer brokenMu.Unlock()
	delete(brokenTasks, path)
}

// BrokenTaskFiles returns every task file that failed to parse during this
// process's task listings, sorted by path. doctor reports them; a caller that
// sees a shorter backlog than it expected can ask why.
//
// A recorded file that no longer EXISTS is dropped rather than returned: the
// listing loop can only clear an entry by parsing the file successfully, so a
// broken task that was deleted (or whose whole workspace was) has no other way
// out of the record, and reporting it would send the reader looking for a path
// that is gone.
func BrokenTaskFiles() []BrokenTaskFile {
	brokenMu.Lock()
	defer brokenMu.Unlock()
	out := make([]BrokenTaskFile, 0, len(brokenTasks))
	for _, b := range brokenTasks {
		if _, err := os.Stat(b.Path); err != nil {
			delete(brokenTasks, b.Path)
			continue
		}
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// WithTask serializes a read-modify-write of ONE task across processes, and
// hands fn a copy re-read from disk under the lock.
//
// The bug it closes: every task mutation is a whole-file rewrite of a doc read
// earlier, and until now nothing serialized that. The seq lock covers seq
// allocation only. Two processes acting as the SAME identity — the loop's
// post-run auto-sync and an operator's `dacli sync`, which is a routine
// pairing — could interleave as: A appends a Log line and saves, A marks its
// event applied, then B (holding the task as it was BEFORE A's write) saves
// its own copy. A's line is gone, and because A's event is already applied,
// logOnce never gets another chance to re-add it. The lost line can be a
// claim, a finding, or the `completed by` stamp that calibration and doctor
// read — a durable record erased with no error anywhere.
//
// Re-reading inside the lock is the load-bearing half. Locking alone would
// still let a caller write back a stale in-memory doc; fn must therefore use
// the task it is GIVEN, not the one it captured.
//
// Note this is a cross-PROCESS race, which is why it needs a file lock and why
// `go test -race` cannot observe it: the detector instruments goroutines in
// one process, and these are separate dacli invocations.
func WithTask(w *workspace.Workspace, t *Task, fn func(fresh *Task) error) error {
	release, err := acquireFileLock(taskLockPath(w, t))
	if err != nil {
		return err
	}
	defer release()

	// Re-read under the lock: whatever another process committed while we
	// waited is now visible, and fn builds on it instead of clobbering it.
	path, status := currentTaskLocation(w, t)
	fresh, err := loadTaskFile(path, t.Project, status)
	if err != nil {
		// The task was removed entirely. Fall back to the caller's copy rather
		// than failing: the mutation is idempotent, and a missing file is
		// reported by the write that follows.
		fresh = t
	}
	if err := fn(fresh); err != nil {
		return err
	}
	// Publish the result back into the caller's task. Callers commonly hold a
	// task from a shared index and apply several mutations in sequence (the
	// sync loop does exactly this: a claim moves the file, then a propose-done
	// reads it back); leaving them pointing at a pre-mutation doc and a
	// pre-rename path would make the NEXT mutation build on stale state.
	if fresh != t {
		t.Doc, t.Path, t.Status = fresh.Doc, fresh.Path, fresh.Status
	}
	return nil
}

// currentTaskLocation resolves where a task's file is NOW. The caller's path
// can be stale: a status change renames the file between folders, and that may
// have happened in another process — or in an earlier iteration of the
// caller's own loop — since the task was read.
func currentTaskLocation(w *workspace.Workspace, t *Task) (string, model.Status) {
	if _, err := os.Stat(t.Path); err == nil {
		return t.Path, t.Status
	}
	base := filepath.Base(t.Path)
	for _, st := range model.AllStatuses {
		p := filepath.Join(w.TasksDir(t.Project, st), base)
		if _, err := os.Stat(p); err == nil {
			return p, st
		}
	}
	return t.Path, t.Status
}

// taskLockPath keys the lock on the task's stable id rather than its path, so
// a concurrent status-folder rename cannot make two holders think they hold
// different locks. It lives beside the project's seq lock, in a directory that
// already exists.
func taskLockPath(w *workspace.Workspace, t *Task) string {
	key := t.ID
	if key == "" {
		key = fmt.Sprintf("%03d-%s", t.Seq, t.Slug)
	}
	return filepath.Join(w.ProjectDir(t.Project), ".task-"+key+".lock")
}

// stageTaskMove tells git about a status change, so a closed task is not
// reported as existing in two folders at once.
//
// Status is folder position, so closing a task is a RENAME. Left unstaged, git
// sees a deletion in open/ and an untracked file in done/, and `dacli doctor`
// reports the task twice — a close that appeared not to have happened unless
// the operator remembered a `git add` nobody told them about (task 273).
//
// Scope is deliberately narrow: only the two task paths, never `git add -A`,
// which is the footgun that once swept an agent's unrelated work into a record
// commit. Best-effort throughout — a workspace with no git, or one that is
// gitignored (the default since the workspace moved off trunk), simply has
// nothing to stage, and a staging failure must never fail the close itself.
func stageTaskMove(w *workspace.Workspace, src, dst string) {
	if !gitx.Available() {
		return
	}
	// A gitignored workspace has no index entry to update. Asking git to stage
	// it would either error or, with force, start tracking the very files the
	// untracking default removed.
	if _, err := gitx.Run(w.Root, "check-ignore", "-q", "--", dst); err == nil {
		return
	}
	_, _ = gitx.Run(w.Root, "add", "--", src, dst)
}

// ClaimHints returns validated explicit claims without adding inferred scope.
// A declared empty or malformed list grants no inferred paths. Explicit claims
// may name new repository paths, including new top-level directories; unlike
// prose, a reviewed declaration does not need an existing path to prove intent.
// Legacy tasks without a declaration use PathHints narrowed to an existing repository
// path or an unambiguous new descendant of one, for the one caller that turns
// hints into an enforcement boundary rather than a ranking signal.
//
// PathHints is deliberately crude — a slash or a .go suffix is enough — because
// a spurious token costs routing one weak tie-break vote. The loop then reused
// it as `spawn --claim`, where crude is not affordable: a claim REFUSES every
// staged file outside it. Task 338's acceptance criteria mention the gosec rule
// list "G104/G301/G302/G306"; that became the agent's entire claim, and its
// commit of eighteen legitimate files was refused (issue #427).
//
// Two failure shapes, both closed by requiring the path to resolve:
//
//   - prose that merely contains a slash — "and/or", "50/50",
//     "G104/G301/G302/G306" — is not a path and now yields nothing
//   - a bare filename like "acceptance.go" IS plausible but still wrong:
//     claims match by exact path or path-prefix (procmon.PathsOverlap), so a
//     claim of "acceptance.go" overlaps NO staged file and blocks the whole
//     commit just as thoroughly
//
// Yielding nothing is the safe outcome, not a gap: an agent with no recorded
// claim is warned once and allowed to proceed, whereas a wrong claim is a
// lockout that --force is the only way past.
func ClaimHints(root string, t *Task) []string {
	seen := map[string]bool{}
	var out []string
	if t.Doc != nil {
		if _, declared := t.Doc.Front.GetText("claims"); declared {
			for _, raw := range t.Claims() {
				p := strings.TrimSpace(raw)
				if filepath.IsAbs(p) || strings.Contains(p, "\\") || strings.Contains("/"+p+"/", "/../") || strings.IndexFunc(raw, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
					continue
				}
				paths, err := normalizeClaimList([]string{p})
				if err != nil {
					continue
				}
				p = paths[0]
				if p == ".git" || strings.HasPrefix(p, ".git/") || seen[p] {
					continue
				}
				seen[p] = true
				out = append(out, p)
			}
			return out
		}
	}
	explicitNew := false
	add := func(p string, allowNew bool) {
		clean := strings.Trim(strings.TrimSpace(p), "/")
		if clean == "" || seen[clean] || strings.Contains(clean, "..") {
			return
		}
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(clean))); err != nil {
			if !allowNew || !hasExistingPathBoundary(root, clean) {
				return
			}
			explicitNew = true
		}
		seen[clean] = true
		out = append(out, clean)
	}
	for _, p := range t.PathHints() {
		add(p, true)
	}

	// Literal paths alone understate implementation work described in
	// behavioral acceptance. Task 371, for example, named docs/RUNTIMES.md but
	// required persisted runtime presets, an execution adapter/doctor, and CLI
	// commands; its docs-only claim then refused all six required code files.
	// Keep this vocabulary architectural and conservative: each signal names a
	// stable package boundary, and a missing directory never becomes a claim.
	// A literal new boundary is stronger evidence than architectural vocabulary.
	// In task 408, "contracts/controlplane/v1" was the required destination;
	// allowing the generic phrase "agent execution" to add a second hard claim
	// fenced a sibling task out of its legitimate feature slice (issue #580).
	if explicitNew {
		return out
	}
	text := strings.ToLower(taskClaimText(t))
	for _, inferred := range []struct {
		path    string
		signals []string
	}{
		{"internal/store", []string{"persist", "runtime preset"}},
		{"internal/features/execution", []string{" adapter", "runtime doctor", "sandbox", "execution"}},
		// Explicit shared types and command names infer the package boundaries
		// task 381 had to change. Generic words such as "validation" or
		// "creation" stay out: they occur across unrelated feature slices and
		// would turn a useful claim into an over-broad lock (task 393).
		{"internal/spm", []string{"spm.", "threepoint"}},
		{"internal/features/planning", []string{"task add", "task estimate"}},
		{"internal/features/insight", []string{"critical-path"}},
		{"internal/cli", []string{" cli", "command-line", "runtime add", "task add", "task estimate"}},
	} {
		for _, signal := range inferred.signals {
			if strings.Contains(text, signal) {
				add(inferred.path, false)
				break
			}
		}
	}
	return out
}

// hasExistingPathBoundary accepts a not-yet-created path only when its first
// repository component already exists. That keeps a task free to name a new
// package below a known boundary (contracts/controlplane/v1), without reviving
// prose tokens such as G104/G301/G302/G306 as hard claims.
func hasExistingPathBoundary(root, clean string) bool {
	parts := strings.Split(filepath.ToSlash(clean), "/")
	if len(parts) < 2 || parts[0] == "" || parts[0] == "." {
		return false
	}
	info, err := os.Stat(filepath.Join(root, filepath.FromSlash(parts[0])))
	return err == nil && info.IsDir()
}

func taskClaimText(t *Task) string {
	var b strings.Builder
	b.WriteString(t.Title)
	if t.Doc != nil {
		for _, s := range t.Doc.Sections {
			b.WriteByte('\n')
			b.WriteString(s.Content)
		}
	}
	return b.String()
}
