package dashboard

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mlnomadpy/dacli/internal/eventlog"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/procmon"
	"github.com/mlnomadpy/dacli/internal/publication"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

// Detail surfaces (dacli 227).
//
// The dashboard could previously answer only "how much" — status counts, point
// totals, a live agent count. Every question that starts with "which one" ended
// at the terminal: which acceptance box is still open, who filed that finding,
// what has this agent actually done. These three endpoints are the "which one"
// half of the contract: one task's full record, the event log's recent history,
// and one agent's lineage and work. They are read the same way `dacli task show`,
// `dacli log` and `dacli agents` read them, from the same store — the dashboard
// never learns anything the CLI cannot also print.

// --- Error mapping ---

// apiError is a build failure that names its own HTTP status. Without it every
// failure inside writeJSON is a 500, so asking for a task that does not exist
// reads as "the server is broken" rather than "no such task" — and a dead link
// in the SPA is indistinguishable from an outage. Statuses used here: 404 for an
// object that is absent, 400 for a ref the caller got wrong (an ambiguous one).
type apiError struct {
	status int
	msg    string
}

func (e apiError) Error() string { return e.msg }

func notFound(kind, ref string) error {
	return apiError{status: http.StatusNotFound, msg: fmt.Sprintf("no such %s: %s", kind, ref)}
}

// taskError maps a store lookup failure onto an HTTP status: a missing task is
// 404, an AMBIGUOUS ref is 400 (the caller must disambiguate — the store refuses
// to guess, and so does this), and anything else is a genuine read fault (500).
func taskError(ref string, err error) error {
	var nf store.ErrNotFound
	if errors.As(err, &nf) {
		return notFound("task", ref)
	}
	if strings.Contains(err.Error(), "ambiguous") {
		return apiError{status: http.StatusBadRequest, msg: err.Error()}
	}
	return err
}

// --- Param guards ---

// validTaskRef reports whether a ?ref= value is a usable task ref. Every ref
// form the store resolves (ULID, t-<ULID>, NNN, slug, NNN-slug) is a single path
// segment, so SafeSegment is exactly the right shape test. store.FindTask
// resolves by scanning the task tree rather than by joining the ref into a path,
// so this is defense in depth — but a ref that cannot possibly name a task
// should be refused with 400, not answered with a 404 that implies it could have.
func validTaskRef(ref string) bool { return workspace.SafeSegment(ref) }

// validAgentID applies the same single-segment rule to an agent id, which DOES
// become a filename (workspace.AgentPath).
func validAgentID(id string) bool { return workspace.SafeSegment(id) }

// eventsDefaultLimit is how many events /api/events returns when the caller
// names no limit. The log is append-only and unbounded — a workspace months old
// holds tens of thousands of events — so an unbounded default would grow into a
// multi-megabyte response on a two-second poll. eventsMaxLimit is the ceiling a
// caller may raise it to.
const (
	eventsDefaultLimit = 50
	eventsMaxLimit     = 500
)

// parseLimit reads an optional ?limit=. Absent means the default; a
// non-numeric, non-positive, or over-ceiling value is the caller's mistake and
// is refused with 400 rather than silently clamped — a silently clamped limit
// makes a truncated page look complete.
func parseLimit(raw string) (int, error) {
	if raw == "" {
		return eventsDefaultLimit, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, apiError{status: http.StatusBadRequest, msg: fmt.Sprintf("limit must be a positive integer, got %q", raw)}
	}
	if n > eventsMaxLimit {
		return 0, apiError{status: http.StatusBadRequest, msg: fmt.Sprintf("limit must be <= %d, got %d", eventsMaxLimit, n)}
	}
	return n, nil
}

// --- Task detail ---

// taskDetailResponse is GET /api/task?ref=<ref>: one task's whole record. The
// summary row (taskView) is embedded verbatim rather than restated, so a client
// that has a row from /api/tasks and a detail from here binds the same field
// names to the same meanings.
type taskDetailResponse struct {
	Generated string         `json:"generated"`
	Task      taskDetailView `json:"task"`
}

type taskDetailView struct {
	Native *sessions.DashboardSnapshot `json:"native,omitempty"`
	taskView
	// Estimate is the three points behind taskView.Points; null when the task is
	// unestimated. The spread is the risk statement a scalar hides, so it is
	// served whole rather than collapsed to its expected value.
	Estimate *estimateView `json:"estimate"`
	// SoThat and Context are the task's narrative sections, "" when absent.
	SoThat  string `json:"so_that"`
	Context string `json:"context"`
	// Acceptance is the contract, in file order, each box with its checked state:
	// the difference between "active" and "actually finished".
	Acceptance []acceptanceBox `json:"acceptance"`
	// AcceptanceDone / AcceptanceTotal are the counts a progress meter needs
	// without the client re-deriving them from the list.
	AcceptanceDone  int `json:"acceptance_done"`
	AcceptanceTotal int `json:"acceptance_total"`
	// Deps are the typed dependency edges, each resolved to the task it names so
	// the UI can show a title and a status instead of a bare ref.
	Deps []depView `json:"deps"`
	// Parent is the WBS edge (the frontmatter parent wikilink, brackets stripped);
	// "" when the task is a root.
	Parent string `json:"parent"`
	// Log is the task's history, newest LAST (file order) — the only record of
	// who claimed it, who blocked it, and when it completed.
	Log []logEntry `json:"log"`
}

type estimateView struct {
	Optimistic  float64 `json:"optimistic"`
	Probable    float64 `json:"probable"`
	Pessimistic float64 `json:"pessimistic"`
	// Expected is the PERT expected value, echoed here so the estimate object is
	// self-contained (it equals taskView.Points).
	Expected float64 `json:"expected"`
}

type acceptanceBox struct {
	Text string `json:"text"`
	Done bool   `json:"done"`
}

// depView is one dependency edge. Resolved is false when the ref names no task
// in the workspace (a dangling edge — worth SEEING rather than dropping, because
// a dependency that silently vanishes is a schedule that silently lies), in
// which case ID/Title/Status are empty.
type depView struct {
	Ref      string `json:"ref"`
	Type     string `json:"type"` // FS | SS | FF | SF (FS when unspecified)
	ID       string `json:"id"`
	Title    string `json:"title"`
	Status   string `json:"status"`
	Resolved bool   `json:"resolved"`
}

// logEntry is one "- <RFC3339> <text>" line from the task's Log section, split
// into its stamp and its text. A line that does not carry a parseable stamp
// (hand-written notes exist) keeps At empty and its whole content in Text —
// never dropped, because the Log is the task's only audit trail.
type logEntry struct {
	At   string `json:"at"`
	Text string `json:"text"`
}

func buildTaskDetail(w *workspace.Workspace, ref string) (taskDetailResponse, error) {
	resp := taskDetailResponse{Generated: nowStamp()}
	// One index build resolves the task AND every one of its dependency refs;
	// FindTask-per-ref would re-read the whole task tree once per edge.
	idx, err := store.BuildTaskIndex(w)
	if err != nil {
		return resp, err
	}
	t, err := idx.Find(ref)
	if err != nil {
		return resp, taskError(ref, err)
	}

	tv := taskDetailView{taskView: summarizeTask(t), Acceptance: []acceptanceBox{}, Deps: []depView{}, Log: []logEntry{}}
	if tp, ok := t.Estimate(); ok {
		tv.Estimate = &estimateView{
			Optimistic: tp.Optimistic, Probable: tp.Probable,
			Pessimistic: tp.Pessimistic, Expected: tp.Expected(),
		}
	}
	tv.SoThat = sectionText(t, "So that")
	tv.Context = sectionText(t, "Context")
	for _, box := range t.Acceptance() {
		tv.Acceptance = append(tv.Acceptance, acceptanceBox{Text: box.Text, Done: box.Done})
		if box.Done {
			tv.AcceptanceDone++
		}
	}
	tv.AcceptanceTotal = len(tv.Acceptance)
	for _, d := range t.Deps() {
		dv := depView{Ref: d.Ref, Type: d.Type}
		if dep, err := idx.Find(d.Ref); err == nil {
			dv.ID, dv.Title, dv.Status, dv.Resolved = dep.ID, dep.Title, string(dep.Status), true
		}
		tv.Deps = append(tv.Deps, dv)
	}
	if p, ok := t.Doc.Front.Get("parent"); ok {
		tv.Parent = strings.TrimSuffix(strings.TrimPrefix(p, "[["), "]]")
	}
	tv.Log = parseLog(t)

	resp.Task = tv
	return resp, nil
}

// summarizeTask builds the shared summary row. buildTasks' loop body was lifted
// here so the list endpoint and the detail endpoint cannot drift on what a task
// row means.
func summarizeTask(t *store.Task) taskView {
	tv := taskView{
		ID: t.ID, Project: t.Project, Seq: t.Seq, Slug: t.Slug,
		Title: t.Title, Status: string(t.Status),
		Priority: t.Priority(), Owner: t.Owner(),
	}
	if tp, ok := t.Estimate(); ok {
		tv.Points = tp.Expected()
		tv.Estimated = true
	}
	return tv
}

// sectionText returns a task section's trimmed body, "" when the section is
// absent — the two are the same thing to a reader.
func sectionText(t *store.Task, title string) string {
	s, ok := t.Doc.Section(title)
	if !ok {
		return ""
	}
	return strings.TrimSpace(s.Content)
}

// parseLog splits the Log section's "- <stamp> <text>" lines. store.AppendLog
// writes that shape; anything else in the section is kept verbatim with no stamp
// rather than discarded.
func parseLog(t *store.Task) []logEntry {
	out := []logEntry{}
	s, ok := t.Doc.Section("Log")
	if !ok {
		return out
	}
	for _, raw := range strings.Split(s.Content, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		line = strings.TrimPrefix(line, "- ")
		e := logEntry{Text: line}
		if stamp, rest, found := strings.Cut(line, " "); found {
			if _, err := time.Parse(time.RFC3339, stamp); err == nil {
				e.At, e.Text = stamp, rest
			}
		}
		out = append(out, e)
	}
	return out
}

// --- Event history ---

// eventsResponse is GET /api/events[?task=<ref>][&limit=N]: the append-only
// cross-agent log, newest first (eventlog.List's order — ULID filenames sort by
// creation time, which is the property the whole log design rests on).
type eventsResponse struct {
	Generated string `json:"generated"`
	// Task is the RESOLVED task id the filter applied ("" for the whole log), so
	// a client can see which task a ref actually named.
	Task string `json:"task"`
	// Limit is the ceiling applied, and Truncated says whether it bit — a page
	// that stops short must say so rather than look like the end of history.
	Limit             int          `json:"limit"`
	Cursor            string       `json:"cursor,omitempty"`
	NextCursor        string       `json:"next_cursor,omitempty"`
	Truncated         bool         `json:"truncated"`
	Partial           bool         `json:"partial"`
	UnreadableRecords int          `json:"unreadable_records"`
	Filters           eventFilters `json:"filters"`
	Events            []eventView  `json:"events"`
}

type eventFilters struct {
	Task    string `json:"task,omitempty"`
	Project string `json:"project,omitempty"`
	Kind    string `json:"kind,omitempty"`
	Actor   string `json:"actor,omitempty"`
	State   string `json:"state"`
	Range   string `json:"range"`
	Cursor  string `json:"-"`
	Limit   int    `json:"-"`
}

// eventView is one log entry. Body is public-safe sanitized and bounded before
// transfer; the Vue client interpolates it as inert text rather than HTML.
type eventView struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	// Actor is the agent that wrote the event (the file's created_by).
	Actor string `json:"actor"`
	// About is the object the event concerns, wikilink brackets already stripped.
	About string `json:"about"`
	// Origin is the taint field: agent | file:<path> | external:<who>.
	Origin string `json:"origin"`
	// Against is the agent id a review finding concerns; "" for everything else.
	Against string `json:"against"`
	// Applied is whether the owner has synced this event onto its object —
	// false is the "pending" the header's unsynced count is made of.
	Applied bool `json:"applied"`
	// At is the event's creation time, RFC3339 UTC, decoded from its ULID (see
	// eventTime); "" if the id is not a ULID.
	At   string `json:"at"`
	Body string `json:"body"`
	// Label is a stable human description of the event class. Color is only a
	// secondary visual cue in the client.
	Label        string `json:"label"`
	Category     string `json:"category"`
	RelatedTask  string `json:"related_task,omitempty"`
	RelatedAgent string `json:"related_agent,omitempty"`
}

func parseEventFilters(filters eventFilters) (eventFilters, error) {
	if filters.Project != "" && !validProject(filters.Project) {
		return filters, apiError{status: http.StatusBadRequest, msg: "invalid project parameter"}
	}
	if filters.Actor != "" && !validAgentID(filters.Actor) {
		return filters, apiError{status: http.StatusBadRequest, msg: "invalid actor parameter"}
	}
	if filters.Cursor != "" && !workspace.SafeSegment(filters.Cursor) {
		return filters, apiError{status: http.StatusBadRequest, msg: "invalid event cursor"}
	}
	if filters.State == "" {
		filters.State = "all"
	}
	if filters.State != "all" && filters.State != "pending" && filters.State != "applied" {
		return filters, apiError{status: http.StatusBadRequest, msg: fmt.Sprintf("invalid event state %q", filters.State)}
	}
	if filters.Range == "" {
		// Preserve the established task-inspector API contract. The Activity SPA
		// sends its explicit 7d default; callers that omit range still observe the
		// bounded newest page across the whole durable journal.
		filters.Range = "all"
	}
	if filters.Range != "24h" && filters.Range != "7d" && filters.Range != "30d" && filters.Range != "all" {
		return filters, apiError{status: http.StatusBadRequest, msg: fmt.Sprintf("invalid event range %q", filters.Range)}
	}
	if filters.Kind != "" {
		valid := false
		for _, kind := range []model.EventKind{
			model.EventClaim, model.EventRelease, model.EventFinding, model.EventProposeStatus,
			model.EventComment, model.EventBlock, model.EventDependency, model.EventDismissal,
			model.EventHelp, model.EventAnswer, model.EventRun, model.EventCommit, model.EventExit,
			model.EventReview,
		} {
			if filters.Kind == string(kind) {
				valid = true
				break
			}
		}
		if !valid {
			return filters, apiError{status: http.StatusBadRequest, msg: fmt.Sprintf("invalid event kind %q", filters.Kind)}
		}
	}
	return filters, nil
}

func buildEvents(w *workspace.Workspace, filters eventFilters) (eventsResponse, error) {
	resp := eventsResponse{Generated: nowStamp(), Limit: filters.Limit, Cursor: filters.Cursor, Filters: filters, Events: []eventView{}}
	q := eventlog.Query{}
	if filters.Task != "" {
		// Resolve the ref to the task's id before filtering. Events record `about`
		// as the resolved id, never the ref the operator typed, so filtering on the
		// raw ref would silently return nothing for "226" — the about-filter bug
		// store.CreateTask's parent handling already learned once.
		t, err := store.FindTask(w, filters.Task)
		if err != nil {
			return resp, taskError(filters.Task, err)
		}
		resp.Task = t.ID
		q.About = t.ID
		filters.Task = t.ID
		resp.Filters.Task = t.ID
	}
	if filters.Kind != "" {
		q.Kinds = []model.EventKind{model.EventKind(filters.Kind)}
	}
	q.Actor = filters.Actor
	q.Pending = filters.State == "pending"
	events, holes, err := eventlog.ListReport(w, q)
	if err != nil {
		return resp, err
	}
	resp.UnreadableRecords = len(holes)
	resp.Partial = len(holes) > 0

	projectTasks := map[string]bool{}
	allTaskProject := map[string]string{}
	tasks, err := store.ListTasks(w, "", "")
	if err != nil {
		return resp, err
	}
	for _, task := range tasks {
		allTaskProject[task.ID] = task.Project
		if filters.Project != "" && task.Project == filters.Project {
			projectTasks[task.ID] = true
		}
	}

	cutoff := time.Time{}
	switch filters.Range {
	case "24h":
		cutoff = time.Now().UTC().Add(-24 * time.Hour)
	case "7d":
		cutoff = time.Now().UTC().Add(-7 * 24 * time.Hour)
	case "30d":
		cutoff = time.Now().UTC().Add(-30 * 24 * time.Hour)
	}
	filtered := make([]*eventlog.Event, 0, len(events))
	for _, e := range events {
		if filters.Cursor != "" && e.ID >= filters.Cursor {
			continue
		}
		if filters.State == "applied" && !e.Applied {
			continue
		}
		if filters.Project != "" && e.About != filters.Project && !projectTasks[e.About] {
			continue
		}
		if !cutoff.IsZero() {
			at, ok := ulidTime(e.ID)
			if !ok || at.Before(cutoff) {
				continue
			}
		}
		filtered = append(filtered, e)
	}
	if len(filtered) > filters.Limit {
		resp.Truncated = true
		filtered = filtered[:filters.Limit]
	}
	if resp.Truncated && len(filtered) > 0 {
		resp.NextCursor = filtered[len(filtered)-1].ID
	}
	for _, e := range filtered {
		label, category := eventPresentation(e)
		relatedTask := ""
		if _, ok := allTaskProject[e.About]; ok {
			relatedTask = e.About
		}
		relatedAgent := e.Actor
		resp.Events = append(resp.Events, eventView{
			ID: e.ID, Kind: string(e.Kind), Actor: e.Actor, About: e.About,
			Origin: safeEventOrigin(e.Origin), Against: e.Against, Applied: e.Applied,
			At: eventTime(e.ID), Body: safeEventBody(e.Body), Label: label, Category: category,
			RelatedTask: relatedTask, RelatedAgent: relatedAgent,
		})
	}
	return resp, nil
}

const eventBodyLimit = 2000

func safeEventBody(body string) string {
	policy := publication.New("", "unknown", false, false, false)
	body = policy.Sanitize(strings.TrimSpace(body))
	body = strings.ToValidUTF8(body, "�")
	if len(body) > eventBodyLimit {
		cut := eventBodyLimit
		for cut > 0 && !utf8.ValidString(body[:cut]) {
			cut--
		}
		body = body[:cut] + "\n[body truncated]"
	}
	return body
}

func safeEventOrigin(origin string) string {
	if strings.HasPrefix(origin, "file:") {
		return "file:<withheld-local-path>"
	}
	return safeEventBody(origin)
}

func eventPresentation(e *eventlog.Event) (string, string) {
	body := strings.ToLower(e.Body)
	switch {
	case strings.Contains(body, "handoff"):
		return "Owner handoff", "handoff"
	case strings.Contains(body, "refus") || strings.Contains(body, "policy denial"):
		return "Policy refusal", "refusal"
	case strings.Contains(body, "reconcil") || e.Kind == model.EventDismissal:
		return "Reconciliation", "reconciliation"
	}
	switch e.Kind {
	case model.EventFinding:
		return "Review finding", "finding"
	case model.EventHelp:
		return "Owner ask", "ask"
	case model.EventAnswer:
		return "Owner answer", "ask"
	case model.EventReview:
		return "Review verdict", "review"
	case model.EventBlock:
		return "Blocked work", "refusal"
	case model.EventCommit, model.EventRun, model.EventExit:
		return "Delivery event", "delivery"
	case model.EventClaim, model.EventRelease:
		return "Ownership event", "ownership"
	case model.EventProposeStatus, model.EventDependency:
		return "Change proposal", "proposal"
	default:
		return "Activity note", "activity"
	}
}

// eventTime recovers an event's creation time from its ULID's leading 48-bit
// millisecond timestamp, via the same ulidTime the burn series already uses to
// bucket runs by day. eventlog.Event carries no `created` field — the id IS the
// timestamp, which is exactly why the log's directory listing is its ordering —
// so decoding is cheaper and more honest than re-reading every event file for a
// frontmatter field the parser already threw away. Returns "" for anything that
// is not a ULID (an id from a foreign writer), so a client renders "unknown"
// rather than the epoch.
func eventTime(id string) string {
	t, ok := ulidTime(id)
	if !ok {
		return ""
	}
	return t.Format(time.RFC3339)
}

// --- Agent detail ---

// agentDetailResponse is GET /api/agent?id=<agent id>: one agent's identity,
// lineage and work.
//
// SECRETS: an agent file carries a token_hash. It is deliberately unreachable
// from here — store.AgentInfo, the only shape this reads, has no field for it,
// so there is no path by which a hash could reach the wire even by accident.
// Nothing below re-reads the raw agent doc.
type agentDetailResponse struct {
	Generated string          `json:"generated"`
	Agent     agentDetailView `json:"agent"`
}

type agentDetailView struct {
	ID   string `json:"id"`
	Role string `json:"role"`
	// Parent is the spawning agent's id (the lineage edge), "" for the root.
	Parent string `json:"parent"`
	// Grant is the capability this agent was spawned with.
	Grant string `json:"grant"`
	// Retired is whether the agent has released its WIP slot. The file outlives
	// the agent — lineage and attribution have to.
	Retired bool `json:"retired"`
	// Children are the agents this one spawned, by id.
	Children []string `json:"children"`
	// Tasks are the tasks this agent owns right now (frontmatter owner).
	Tasks []taskView `json:"tasks"`
	// Runs are the run records attributable to this agent, newest first, dead ones
	// included: a finished run is the evidence of what the agent DID, and dropping
	// it would leave a retired agent looking like it never worked.
	Runs []agentRunView `json:"runs"`
}

// agentRunView is one run this agent led. Live is the honest liveness probe
// (procmon.AliveRecord re-checks the PID/start-time pair), never proc.txt alone.
type agentRunView struct {
	RunID   string `json:"run_id"`
	Task    string `json:"task"`
	Role    string `json:"role"`
	Runtime string `json:"runtime"`
	PID     int    `json:"pid"`
	Started string `json:"started"`
	Live    bool   `json:"live"`
	// TranscriptURL and DiffURL are the same read-only per-run links the swarm
	// exposes, so agent detail and the swarm reach identical evidence.
	TranscriptURL string `json:"transcript_url"`
	DiffURL       string `json:"diff_url"`
}

func buildAgentDetail(w *workspace.Workspace, id string) (agentDetailResponse, error) {
	resp := agentDetailResponse{Generated: nowStamp()}
	agents, err := store.ListAgents(w)
	if err != nil {
		// No agents dir at all: the id names nothing, which is a 404, not a 500.
		return resp, notFound("agent", id)
	}
	view := agentDetailView{Children: []string{}, Tasks: []taskView{}, Runs: []agentRunView{}}
	found := false
	for _, a := range agents {
		if a.ID == id {
			view.ID, view.Role, view.Grant, view.Parent, view.Retired = a.ID, a.Role, a.Grant, a.Parent, a.Retired
			found = true
		}
		if a.Parent == id && a.ID != id {
			view.Children = append(view.Children, a.ID)
		}
	}
	if !found {
		return resp, notFound("agent", id)
	}
	sort.Strings(view.Children)

	tasks, err := store.ListTasks(w, "", "")
	if err != nil {
		return resp, err
	}
	for _, t := range tasks {
		if t.Owner() == id {
			view.Tasks = append(view.Tasks, summarizeTask(t))
		}
	}
	view.Runs = agentRuns(w, id)

	resp.Agent = view
	return resp, nil
}

// agentRuns reads every run's proc.txt and keeps the ones this agent led,
// newest first (run ids are ULIDs, so reverse-lexical IS reverse-chronological).
// Unlike liveAgents this keeps the dead ones and marks them, because agent
// detail is a work history, not a liveness board.
func agentRuns(w *workspace.Workspace, id string) []agentRunView {
	out := []agentRunView{}
	entries, err := os.ReadDir(w.RunsDir())
	if err != nil {
		return out
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	for _, n := range names {
		rec, err := procmon.ReadRecord(filepath.Join(w.RunDir(n), "proc.txt"))
		if err != nil || rec.Child != id {
			continue
		}
		out = append(out, agentRunView{
			RunID: rec.RunID, Task: rec.Task, Role: rec.Role,
			Runtime: rec.Runtime, PID: rec.PID,
			Started:       rec.Started.UTC().Format(time.RFC3339),
			Live:          procmon.AliveRecord(rec),
			TranscriptURL: "/api/agents/transcript?run=" + rec.RunID,
			DiffURL:       "/api/agents/diff?run=" + rec.RunID,
		})
	}
	return out
}
