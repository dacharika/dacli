// Package dashboard serves a self-contained, read-only local web UI over the
// workspace: projects, tasks by status with a burndown, the live agent swarm
// (task/role/runtime/last activity), and the pending (unsynced) event count —
// read straight from the store, the run directory's proc.txt records, and the
// event log, the same sources `dacli status` and `dacli agents` already read.
// Nothing here mutates the workspace.
package dashboard

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mlnomadpy/dacli/internal/agentstate"
	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/eventlog"
	"github.com/mlnomadpy/dacli/internal/gitx"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/procmon"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

var Commands = []clikit.Command{
	{Path: "dashboard", Brief: "Serve a local web UI: projects, burndown, and the live agent swarm", Usage: "dacli dashboard [--port N] [--native-connection FILE]", Run: cmdDashboard},
}

// legacyIndexHTML is the original self-contained dashboard: one file that polls
// /api/state. It is the dev-mode fallback served when no built SPA is embedded.
//
//go:embed static/index.html
var legacyIndexHTML []byte

// spaDist embeds the built Vue SPA bundle (ui/dist/index.html, a single
// self-contained file produced by `npm run build` — see ui/vite.config.ts).
// The `all:` prefix means the committed ui/dist/.gitkeep placeholder is
// embedded too, so `//go:embed` finds a match — and therefore `go build`
// succeeds — even on a fresh checkout that has NOT built the frontend yet. In
// that case ui/dist/index.html is absent and indexPage() falls back to the
// legacy page. CI/goreleaser run `npm run build` before `go build`, so a
// released binary always embeds the current SPA.
//
//go:embed all:ui/dist
var spaDist embed.FS

// indexPage returns the HTML served at "/": the built SPA bundle when a
// frontend build has produced ui/dist/index.html, else the legacy dashboard.
// Resolved on each call (cheap: a read from the in-memory embed FS), so there
// is no startup ordering to get wrong.
func indexPage() []byte {
	if b, err := spaDist.ReadFile("ui/dist/index.html"); err == nil {
		return b
	}
	return legacyIndexHTML
}

// cmdDashboard binds a localhost listener (an ephemeral port unless --port
// pins one) and serves the dashboard until the process is killed. The page
// polls the typed API (the legacy fallback polls /api/state), so a loop's agents
// appear live without a restart.
func cmdDashboard(ctx *clikit.Ctx, args []string) error {
	w, _, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	f, err := clikit.ParseFlags(args)
	if err != nil {
		return err
	}
	if err := f.Reject("port", "native-connection"); err != nil {
		return err
	}
	port := 0
	if p := f.Get("port"); p != "" {
		port, err = strconv.Atoi(p)
		if err != nil {
			return clikit.Usagef("--port must be a number, got %q", p)
		}
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return fmt.Errorf("dashboard: %w", err)
	}
	fmt.Fprintf(ctx.Stdout, "dacli dashboard: http://%s (Ctrl+C to stop)\n", ln.Addr().String())
	// A bare http.Serve never times out a slow or hung client — even on a
	// localhost-only listener, another local process could hold the socket
	// open indefinitely (gosec G114). The deadlines are generous because a
	// large transcript.log served over loopback should never approach them.
	srv := &http.Server{
		Handler:           newHandlerNative(w, openNativeReader(f.Get("native-connection"), w)),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	return srv.Serve(ln)
}

// newHandler builds the whole server: the embedded page at "/", the combined
// JSON snapshot the legacy page polls at "/api/state", and the typed
// per-surface endpoints the Vue SPA reads. Every JSON handler reads the workspace fresh on
// each request — no cache — so a poll always reflects the live store and event
// log (the same honesty rule buildState follows). Factored out so tests can
// drive it through httptest without binding a real port.
func newHandler(w *workspace.Workspace) http.Handler { return newHandlerNative(w, nil) }
func newHandlerNative(w *workspace.Workspace, native nativeDashboardReader) http.Handler {
	mux := http.NewServeMux()
	outcomes := newOutcomeCache()
	mux.HandleFunc("/", func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(rw, r)
			return
		}
		rw.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = rw.Write(indexPage())
	})
	// Legacy combined snapshot — the self-contained static/index.html polls
	// this. Preserved verbatim so the existing dashboard keeps working until
	// the SPA (which reads the typed endpoints below) replaces it. Every /api/*
	// handler is wrapped in apiGuard: a Host-header allowlist (DNS-rebinding
	// defense, 403) plus a GET-only gate (the whole API is read-only).
	mux.HandleFunc("/api/state", apiGuard(func(rw http.ResponseWriter, r *http.Request) {
		writeJSON(rw, func() (any, error) { return buildState(w) })
	}))
	// Typed per-surface endpoints for the SPA. Each is an envelope carrying its
	// own `generated` stamp so a surface can be polled independently and still
	// reason about freshness. Shared facts use the same builders as /api/state;
	// /api/projects deliberately omits graphs, which live at /api/graph.
	mux.HandleFunc("/api/overview", apiGuard(func(rw http.ResponseWriter, r *http.Request) {
		writeJSON(rw, func() (any, error) { return buildOverview(w) })
	}))
	mux.HandleFunc("/api/projects", apiGuard(func(rw http.ResponseWriter, r *http.Request) {
		writeJSON(rw, func() (any, error) { return buildProjects(w) })
	}))
	mux.HandleFunc("/api/tasks", apiGuard(func(rw http.ResponseWriter, r *http.Request) {
		project := r.URL.Query().Get("project")
		if !validProject(project) {
			http.Error(rw, "invalid project parameter", http.StatusBadRequest)
			return
		}
		writeJSON(rw, func() (any, error) { return buildTasks(w, project) })
	}))
	mux.HandleFunc("/api/agents", apiGuard(func(rw http.ResponseWriter, r *http.Request) {
		writeJSON(rw, func() (any, error) { return buildAgents(w) })
	}))
	// Per-run detail the swarm links to, both read-only and both served as
	// text/plain so a browser renders them inline. /transcript renders the run's
	// transcript.log (raw stream-json events decoded to readable text); /diff
	// shows the run's uncommitted changes (git diff HEAD in its worktree, else
	// the main checkout). A missing/invalid run id is a 404; the run never leaves
	// the runs dir (validated against path traversal), and neither handler writes.
	mux.HandleFunc("/api/agents/transcript", apiGuard(func(rw http.ResponseWriter, r *http.Request) {
		serveRunTranscript(rw, w, r.URL.Query().Get("run"))
	}))
	mux.HandleFunc("/api/agents/diff", apiGuard(func(rw http.ResponseWriter, r *http.Request) {
		serveRunDiff(rw, w, r.URL.Query().Get("run"))
	}))
	// Burn: token/cost burn-rate over time against the calibrated ceiling. The
	// envelope wraps the same buildBurn payload embedded in /api/state, so the
	// standalone surface and the combined snapshot can never disagree.
	mux.HandleFunc("/api/burn", apiGuard(func(rw http.ResponseWriter, r *http.Request) {
		writeJSON(rw, func() (any, error) { return buildNativeBurn(w, native) })
	}))
	// Outcome analytics compares adjacent bounded windows from one indexed scan.
	// Missing historical timestamps, usage, or provider cost remain explicitly
	// unknown; this projection never upgrades them to zero or current success.
	mux.HandleFunc("/api/outcomes", apiGuard(func(rw http.ResponseWriter, r *http.Request) {
		project := r.URL.Query().Get("project")
		if project == "" || !validProject(project) {
			http.Error(rw, "a valid project parameter is required", http.StatusBadRequest)
			return
		}
		if _, err := store.LoadProject(w, project); err != nil {
			http.Error(rw, "no such project", http.StatusNotFound)
			return
		}
		days := 30
		if raw := strings.TrimSuffix(r.URL.Query().Get("range"), "d"); raw != "" {
			var err error
			days, err = strconv.Atoi(raw)
			if err != nil || days != 7 && days != 30 && days != 90 {
				http.Error(rw, "range must be 7d, 30d, or 90d", http.StatusBadRequest)
				return
			}
		}
		writeJSON(rw, func() (any, error) { return outcomes.build(w, project, days, time.Now()) })
	}))
	// Loop operations: one selected-project supervision projection assembled
	// from durable loop, recovery, phase, preflight, reservation, profile, run,
	// and routing evidence. The browser never parses workspace files and this
	// GET does not launch providers or refresh GitHub (issue #942).
	mux.HandleFunc("/api/loop-operation", apiGuard(func(rw http.ResponseWriter, r *http.Request) {
		project := r.URL.Query().Get("project")
		if project == "" || !validProject(project) {
			http.Error(rw, "a valid project parameter is required", http.StatusBadRequest)
			return
		}
		if _, err := store.LoadProject(w, project); err != nil {
			http.Error(rw, "no such project", http.StatusNotFound)
			return
		}
		writeJSON(rw, func() (any, error) { return buildLoopOperation(w, project, time.Now()), nil })
	}))
	// Canonical task/worker progress is the same progress-explain/v1 entity used
	// by `task status`, `explain`, `agents --json`, and the generic MCP command.
	// The dashboard does not restate or reinterpret these freshness-marked facts.
	mux.HandleFunc("/api/progress", apiGuard(func(rw http.ResponseWriter, r *http.Request) {
		project, taskRef := r.URL.Query().Get("project"), r.URL.Query().Get("task")
		if project == "" || !validProject(project) {
			http.Error(rw, "a valid project parameter is required", http.StatusBadRequest)
			return
		}
		if taskRef != "" && !validTaskRef(taskRef) {
			http.Error(rw, "invalid task parameter: a task ref is a single path segment", http.StatusBadRequest)
			return
		}
		writeJSON(rw, func() (any, error) { return buildProgressProjection(w, project, taskRef, time.Now()) })
	}))
	// Graph: the task dependency DAG + CPM critical path (internal/spm computes
	// the chain — this exposes and draws it). Optional ?project=<slug> scopes it;
	// the same graphView is embedded per-project in /api/state, so the standalone
	// surface and the combined snapshot can never disagree.
	mux.HandleFunc("/api/graph", apiGuard(func(rw http.ResponseWriter, r *http.Request) {
		project := r.URL.Query().Get("project")
		if !validProject(project) {
			http.Error(rw, "invalid project parameter", http.StatusBadRequest)
			return
		}
		opts, err := parseGraphOptions(r.URL.Query().Get("mode"), r.URL.Query().Get("focus"), r.URL.Query().Get("status"), r.URL.Query().Get("page"))
		if err != nil {
			http.Error(rw, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(rw, func() (any, error) { return buildGraphResponseWithOptions(w, project, opts) })
	}))
	// Roles: the workspace roster — what each role may touch, what it costs, and
	// how much of its WIP budget is spent (dacli 226). The same []roleView is
	// embedded in /api/state for legacy compatibility.
	mux.HandleFunc("/api/roles", apiGuard(func(rw http.ResponseWriter, r *http.Request) {
		writeJSON(rw, func() (any, error) { return buildRoles(w) })
	}))
	// Detail surfaces (dacli 227): one task's whole record, the recent event log
	// (optionally scoped to a task), and one agent's lineage and work. Each ref
	// parameter is validated as a single path segment before it reaches the store,
	// the same rule validProject and validRunID apply.
	mux.HandleFunc("/api/task", apiGuard(func(rw http.ResponseWriter, r *http.Request) {
		ref := r.URL.Query().Get("ref")
		if !validTaskRef(ref) {
			http.Error(rw, "invalid ref parameter: a task ref is a single path segment", http.StatusBadRequest)
			return
		}
		writeJSON(rw, func() (any, error) { return buildNativeTaskDetail(w, native, ref) })
	}))
	mux.HandleFunc("/api/delivery-timeline", apiGuard(func(rw http.ResponseWriter, r *http.Request) {
		ref := r.URL.Query().Get("task")
		if !validTaskRef(ref) {
			http.Error(rw, "invalid task parameter: a task ref is a single path segment", http.StatusBadRequest)
			return
		}
		writeJSON(rw, func() (any, error) { return buildDeliveryTimeline(w, ref) })
	}))
	mux.HandleFunc("/api/delivery-attention", apiGuard(func(rw http.ResponseWriter, r *http.Request) {
		writeJSON(rw, func() (any, error) { return buildDeliveryAttention(w) })
	}))
	// Operator attention is a workspace-wide or project-scoped read model over
	// canonical policy and delivery records. It is deliberately not dismissible:
	// resolution follows the underlying state change (issue #951).
	mux.HandleFunc("/api/attention", apiGuard(func(rw http.ResponseWriter, r *http.Request) {
		project := r.URL.Query().Get("project")
		if !validProject(project) {
			http.Error(rw, "invalid project parameter", http.StatusBadRequest)
			return
		}
		writeJSON(rw, func() (any, error) { return buildNativeAttention(w, native, project) })
	}))
	mux.HandleFunc("/api/events", apiGuard(func(rw http.ResponseWriter, r *http.Request) {
		// An absent ?task= is the whole log — a legitimate query, not a bad param.
		task := r.URL.Query().Get("task")
		if task != "" && !validTaskRef(task) {
			http.Error(rw, "invalid task parameter: a task ref is a single path segment", http.StatusBadRequest)
			return
		}
		limit, err := parseLimit(r.URL.Query().Get("limit"))
		if err != nil {
			writeJSON(rw, func() (any, error) { return nil, err })
			return
		}
		filters, err := parseEventFilters(eventFilters{
			Task: task, Project: r.URL.Query().Get("project"), Kind: r.URL.Query().Get("kind"),
			Actor: r.URL.Query().Get("actor"), State: r.URL.Query().Get("state"),
			Range: r.URL.Query().Get("range"), Cursor: r.URL.Query().Get("cursor"), Limit: limit,
		})
		if err != nil {
			writeJSON(rw, func() (any, error) { return nil, err })
			return
		}
		writeJSON(rw, func() (any, error) { return buildEvents(w, filters) })
	}))
	mux.HandleFunc("/api/agent", apiGuard(func(rw http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		if !validAgentID(id) {
			http.Error(rw, "invalid id parameter: an agent id is a single path segment", http.StatusBadRequest)
			return
		}
		writeJSON(rw, func() (any, error) { return buildAgentDetail(w, id) })
	}))
	return mux
}

// apiGuard wraps an /api/* handler with the two dashboard hardening checks:
// a Host-header allowlist and a GET-only method gate. The server binds
// 127.0.0.1 only, but a browser aimed at an attacker-controlled DNS name that
// resolves to loopback (DNS rebinding) would still reach it same-origin;
// rejecting any Host that is not localhost/127.0.0.1/[::1] closes that gap
// without adding an auth token that would break the local `dacli dashboard`
// UX. The whole API is read-only, so anything but GET is refused too.
func apiGuard(next http.HandlerFunc) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		if !allowedHost(r.Host) {
			http.Error(rw, "forbidden: Host header is not a recognized loopback name", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet {
			http.Error(rw, "method not allowed: the dashboard API is read-only (GET)", http.StatusMethodNotAllowed)
			return
		}
		next(rw, r)
	}
}

// allowedHost reports whether the request's Host header names the loopback
// interface the dashboard binds to — localhost, 127.0.0.1, or ::1, with or
// without a port. An empty or foreign Host (a rebinding attacker's domain) is
// rejected. This is the same-origin gate for the loopback-only server.
func allowedHost(host string) bool {
	if host == "" {
		return false
	}
	h := host
	if hostOnly, _, err := net.SplitHostPort(host); err == nil {
		h = hostOnly
	}
	h = strings.TrimPrefix(strings.TrimSuffix(h, "]"), "[")
	switch h {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}

// validProject reports whether a ?project= filter is usable. An empty value
// means "all projects" (a legitimate whole-board query) and is allowed; a
// non-empty value must be a safe single path segment — the same guard
// workspace.ProjectDir applies before the slug reaches filepath.Join, mirroring
// how validRunID gates the run param. An unsafe slug (`../other-workspace`) is
// rejected with 400 rather than silently returning an empty result.
func validProject(project string) bool {
	return project == "" || workspace.SafeSegment(project)
}

// writeJSON runs build (a fresh workspace read), then encodes the result as
// indented JSON with the dashboard's standard content type. A build error
// becomes a 500 with the error text, mirroring the original /api/state handler —
// UNLESS it is an apiError, which names its own status (dacli 227): a request
// for a task that does not exist is a 404, not a report that the server broke,
// and an ambiguous ref is the caller's 400. Without that distinction a dead link
// in the SPA is indistinguishable from an outage.
func writeJSON(rw http.ResponseWriter, build func() (any, error)) {
	v, err := build()
	if err != nil {
		var ae apiError
		if errors.As(err, &ae) {
			http.Error(rw, ae.msg, ae.status)
			return
		}
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	rw.Header().Set("Content-Type", "application/json; charset=utf-8")
	enc := json.NewEncoder(rw)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

// --- Snapshot assembly ---

type dashboardState struct {
	Generated     string        `json:"generated"`
	Projects      []projectView `json:"projects"`
	Agents        []agentView   `json:"agents"`
	PendingEvents int           `json:"pending_events"` // unsynced child events (eventlog), as `dacli status` reports
	Burn          burnView      `json:"burn"`           // token/cost burn-rate over time vs the calibrated ceiling
	// Roles is the team roster (dacli 226), embedded so the SPA's single poll
	// carries it — the same reason burn and the per-project graph live here. It
	// is the identical []roleView /api/roles serves.
	Roles []roleView `json:"roles"`
}

type projectView struct {
	projectSummaryView
	Graph graphView `json:"graph"` // dependency DAG + CPM critical path for this project
}

type projectSummaryView struct {
	Slug     string         `json:"slug"`
	Title    string         `json:"title"`
	Stage    string         `json:"stage"`
	Total    int            `json:"total"`
	Counts   map[string]int `json:"counts"` // status -> task count
	Burndown burndownView   `json:"burndown"`
}

type burndownView struct {
	DonePoints      float64       `json:"done_points"`
	RemainingPoints float64       `json:"remaining_points"`
	Unestimated     int           `json:"unestimated"`
	PerDay          []burndownDay `json:"per_day"`
	HiddenDays      int           `json:"hidden_days"`
}

type burndownDay struct {
	Day     string   `json:"day"`
	Points  float64  `json:"points"`
	TaskIDs []string `json:"task_ids"`
}

type agentView struct {
	RunID   string `json:"run_id"`
	Child   string `json:"child"`
	Task    string `json:"task"`
	Role    string `json:"role"`
	Runtime string `json:"runtime"`
	PID     int    `json:"pid"`
	Started string `json:"started"`
	// State is the honest per-agent activity: thinking | acting | waiting |
	// stalled | blocked | silent, derived by agentstate.Derive — the same
	// function `dacli agents` calls, so the two surfaces can never disagree. It
	// answers the operator's daily "reasoning or hung?" question without
	// trusting proc RAM/CPU.
	State        string `json:"state"`
	RuntimeSecs  int64  `json:"runtime_secs"`
	LastActivity string `json:"last_activity"`
	// TranscriptURL and DiffURL are same-origin read-only links to the two agent
	// detail endpoints — the rendered transcript and this run's uncommitted diff —
	// so the swarm can offer "view transcript / see the diff" without any mutation.
	TranscriptURL string `json:"transcript_url"`
	DiffURL       string `json:"diff_url"`
}

// buildState reads the workspace fresh on every call — the dashboard has no
// cache of its own, so a page poll always reflects the current store and run
// directory (the same honesty rule `dacli agents` follows: liveness is never
// trusted from a stale read).
func buildState(w *workspace.Workspace) (dashboardState, error) {
	st := dashboardState{Generated: time.Now().UTC().Format(time.RFC3339)}

	projects, err := store.ListProjects(w)
	if err != nil {
		return st, err
	}
	for _, p := range projects {
		st.Projects = append(st.Projects, buildProjectView(w, p))
	}

	// One task-tree scan for every live agent's blocked check, not one per
	// agent (store.BuildTaskIndex — the same discipline eventlog.Sync and
	// acceptance.go follow). A failed build degrades to nil: agentstate.Derive
	// then just never reports "blocked", never an error.
	tasks, _ := store.BuildTaskIndex(w)
	for _, rec := range liveAgents(w) {
		st.Agents = append(st.Agents, buildAgentView(w, rec, tasks))
	}

	pending, _ := eventlog.List(w, eventlog.Query{Pending: true})
	st.PendingEvents = len(pending)

	if burn, err := buildBurn(w); err == nil {
		st.Burn = burn
	}
	// An unreadable roles dir degrades to an empty roster rather than failing the
	// whole snapshot: the board, the swarm and the burn are all still true.
	if roster, err := buildRoles(w); err == nil {
		st.Roles = roster.Roles
	}
	return st, nil
}

// --- Typed per-surface endpoints (SPA contract) ---
//
// Each endpoint returns an envelope whose `generated` field is an RFC3339 UTC
// stamp of when the snapshot was built, exactly like dashboardState.Generated,
// so a client polling one surface can still show freshness. Payloads reuse the
// buildProjectView / buildAgentView builders that /api/state uses, so the typed
// endpoints and the combined snapshot can never disagree about a shared field.

// overviewResponse is GET /api/overview: the workspace-health signals the
// Overview surface reads at a glance — project/task totals, aggregate task
// counts across every project, the unsynced-event count, and how many agents
// are live right now. It carries no per-project or per-task detail; those live
// on /api/projects and /api/tasks.
type overviewResponse struct {
	Generated     string         `json:"generated"`
	ProjectCount  int            `json:"project_count"`
	TaskCount     int            `json:"task_count"`     // total tasks across all projects
	Counts        map[string]int `json:"counts"`         // status -> task count, summed across projects
	PendingEvents int            `json:"pending_events"` // unsynced child events, as `dacli status` reports
	LiveAgents    int            `json:"live_agents"`    // count of liveness-probed running agents
}

// projectsResponse is GET /api/projects: project summaries without dependency
// graphs. The Vue application fetches only the selected project's graph through
// /api/graph, so a large history is not rebuilt for every unselected project
// (issue #932). /api/state retains full projectView entries for the legacy page.
type projectsResponse struct {
	Generated string               `json:"generated"`
	Projects  []projectSummaryView `json:"projects"`
}

// tasksResponse is GET /api/tasks: individual task rows, the per-task detail
// /api/state deliberately omits (it carries only per-status counts). This is
// the "future snapshot [that] adds a task list" DESIGN.md §7.2 anticipated, so
// the board can render real task identities instead of magnitude-only chips.
// An optional ?project=<slug> query filters to one project; absent, every
// project's tasks are returned, sorted by project then sequence (ListTasks'
// order).
type tasksResponse struct {
	Generated string     `json:"generated"`
	Tasks     []taskView `json:"tasks"`
}

// taskView is one task row. Points is the PERT expected value of the task's
// three-point estimate; when the task has no valid estimate, Estimated is false
// and Points is 0 (it contributes to counts/totals but not to burndown points,
// the same rule buildProjectView applies).
type taskView struct {
	ID        string  `json:"id"`
	Project   string  `json:"project"`
	Seq       int     `json:"seq"`
	Slug      string  `json:"slug"`
	Title     string  `json:"title"`
	Status    string  `json:"status"`
	Priority  string  `json:"priority"`
	Owner     string  `json:"owner"`
	Points    float64 `json:"points"`    // PERT expected; 0 when unestimated
	Estimated bool    `json:"estimated"` // whether a valid three-point estimate exists
}

// agentsResponse is GET /api/agents: the live agent swarm, identical to the
// `agents` array inside /api/state — newest-first and already liveness-filtered
// (never trust proc.txt alone; AliveRecord re-probes each PID).
type agentsResponse struct {
	Generated string      `json:"generated"`
	Agents    []agentView `json:"agents"`
}

// burnResponse is GET /api/burn: the burnView with its own `generated` stamp,
// so the burn surface can be polled independently and still reason about
// freshness. Its payload is the same burnView embedded in /api/state.
type burnResponse struct {
	Generated string `json:"generated"`
	burnView
}

func nowStamp() string { return time.Now().UTC().Format(time.RFC3339) }

func buildProgressProjection(w *workspace.Workspace, project, taskRef string, now time.Time) (store.ProgressExplain, error) {
	if _, err := store.LoadProject(w, project); err != nil {
		return store.ProgressExplain{}, apiError{status: http.StatusNotFound, msg: "no such project"}
	}
	projection, err := store.ExplainProject(w, project, now)
	if taskRef == "" || err != nil {
		return projection, err
	}
	task, findErr := store.FindTask(w, taskRef)
	if findErr != nil || task.Project != project {
		return store.ProgressExplain{}, apiError{status: http.StatusNotFound, msg: "no such task in project"}
	}
	tasks := projection.Tasks[:0]
	for _, item := range projection.Tasks {
		if item.ID.Value == task.ID {
			tasks = append(tasks, item)
		}
	}
	workers := projection.Workers[:0]
	for _, worker := range projection.Workers {
		if worker.TaskID.Value == task.ID {
			workers = append(workers, worker)
		}
	}
	projection.Tasks, projection.Workers = tasks, workers
	return projection, nil
}

func buildBurnResponse(w *workspace.Workspace) (burnResponse, error) {
	burn, err := buildBurn(w)
	if err != nil {
		return burnResponse{}, err
	}
	return burnResponse{Generated: nowStamp(), burnView: burn}, nil
}

func buildOverview(w *workspace.Workspace) (overviewResponse, error) {
	resp := overviewResponse{Generated: nowStamp(), Counts: map[string]int{}}
	projects, err := store.ListProjects(w)
	if err != nil {
		return resp, err
	}
	resp.ProjectCount = len(projects)
	for _, p := range projects {
		tasks, _ := store.ListTasks(w, p.Slug, "")
		resp.TaskCount += len(tasks)
		for _, t := range tasks {
			resp.Counts[string(t.Status)]++
		}
	}
	pending, _ := eventlog.List(w, eventlog.Query{Pending: true})
	resp.PendingEvents = len(pending)
	resp.LiveAgents = len(liveAgents(w))
	return resp, nil
}

func buildProjects(w *workspace.Workspace) (projectsResponse, error) {
	resp := projectsResponse{Generated: nowStamp()}
	projects, err := store.ListProjects(w)
	if err != nil {
		return resp, err
	}
	for _, p := range projects {
		resp.Projects = append(resp.Projects, buildProjectSummaryView(w, p))
	}
	return resp, nil
}

// buildTasks lists task rows, optionally filtered to one project. An empty
// project filter yields every project's tasks (ListTasks with project == ""),
// so a single request can drive the whole board.
func buildTasks(w *workspace.Workspace, project string) (tasksResponse, error) {
	resp := tasksResponse{Generated: nowStamp()}
	tasks, err := store.ListTasks(w, project, "")
	if err != nil {
		return resp, err
	}
	for _, t := range tasks {
		// summarizeTask (detail.go) is the single definition of a task row, so the
		// list endpoint and /api/task's embedded summary cannot drift (dacli 227).
		resp.Tasks = append(resp.Tasks, summarizeTask(t))
	}
	return resp, nil
}

func buildAgents(w *workspace.Workspace) (agentsResponse, error) {
	resp := agentsResponse{Generated: nowStamp()}
	tasks, _ := store.BuildTaskIndex(w)
	for _, rec := range liveAgents(w) {
		resp.Agents = append(resp.Agents, buildAgentView(w, rec, tasks))
	}
	return resp, nil
}

// validRunID reports whether s is a bare run directory name — the ULID form
// dacli mints (uppercase alphanumeric, no separators). This is the guard that
// keeps the run query parameter from escaping the runs dir via "../" or an
// absolute path: an id with any other character is rejected as not-found.
func validRunID(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9') && !(r >= 'A' && r <= 'Z') && !(r >= 'a' && r <= 'z') {
			return false
		}
	}
	return true
}

// serveRunTranscript writes the run's transcript.log as readable text/plain,
// decoding stream-json events the same way the swarm's state derivation and
// `dacli agents --tail` do. Read-only; a bad id or a run with no transcript is a
// 404 so a dead link never renders as an empty 200.
func serveRunTranscript(rw http.ResponseWriter, w *workspace.Workspace, runID string) {
	if !validRunID(runID) {
		http.NotFound(rw, nil)
		return
	}
	data, err := os.ReadFile(filepath.Join(w.RunDir(runID), "transcript.log"))
	if err != nil {
		http.NotFound(rw, nil)
		return
	}
	rw.Header().Set("Content-Type", "text/plain; charset=utf-8")
	for _, ln := range bytes.Split(data, []byte("\n")) {
		if text := agentstate.RenderTranscriptLine(ln); text != "" {
			fmt.Fprintln(rw, text)
		}
	}
}

// serveRunDiff writes the run's uncommitted changes as text/plain — `git diff
// HEAD` in the run's isolated worktree (worktree.txt) when it has one, else the
// main checkout. A live agent is mid-task, so this is the honest "what is it
// changing right now" view. Read-only (diff mutates nothing). A bad id is a 404;
// a clean tree or a non-repo yields a 200 with a plain note, never a fake diff.
func serveRunDiff(rw http.ResponseWriter, w *workspace.Workspace, runID string) {
	if !validRunID(runID) {
		http.NotFound(rw, nil)
		return
	}
	runDir := w.RunDir(runID)
	if _, err := os.Stat(runDir); err != nil {
		http.NotFound(rw, nil)
		return
	}
	dir := w.Root
	if raw, err := os.ReadFile(filepath.Join(runDir, "worktree.txt")); err == nil {
		if wt := strings.TrimSpace(string(raw)); wt != "" {
			dir = wt
		}
	}
	rw.Header().Set("Content-Type", "text/plain; charset=utf-8")
	out, err := gitx.Run(dir, "diff", "HEAD")
	switch {
	case err != nil:
		fmt.Fprintf(rw, "(diff unavailable for %s: %v)\n", runID, err)
	case strings.TrimSpace(out) == "":
		fmt.Fprintf(rw, "(no uncommitted changes in %s)\n", dir)
	default:
		_, _ = io.WriteString(rw, out)
	}
}

func buildProjectView(w *workspace.Workspace, p *store.Project) projectView {
	summary := buildProjectSummaryView(w, p)
	graph, _ := buildGraph(w, p.Slug)
	return projectView{projectSummaryView: summary, Graph: graph}
}

func buildProjectSummaryView(w *workspace.Workspace, p *store.Project) projectSummaryView {
	tasks, _ := store.ListTasks(w, p.Slug, "")
	counts := map[string]int{}
	var doneP, remP float64
	var unestimated int
	perDay := map[string]float64{}
	perDayTasks := map[string][]string{}
	for _, t := range tasks {
		counts[string(t.Status)]++
		tp, ok := t.Estimate()
		if !ok {
			unestimated++
			continue
		}
		if t.Status == model.StatusDone {
			doneP += tp.Expected()
			if day, ok := completionDay(t); ok {
				perDay[day] += tp.Expected()
				if len(perDayTasks[day]) < analyticsEvidenceLimit {
					perDayTasks[day] = append(perDayTasks[day], t.ID)
				}
			}
		} else {
			remP += tp.Expected()
		}
	}
	days := make([]string, 0, len(perDay))
	for d := range perDay {
		days = append(days, d)
	}
	sort.Strings(days)
	perDaySlice := make([]burndownDay, 0, len(days))
	for _, d := range days {
		perDaySlice = append(perDaySlice, burndownDay{Day: d, Points: perDay[d], TaskIDs: perDayTasks[d]})
	}
	perDaySlice, hiddenDays := downsampleBurndown(perDaySlice, 90)
	return projectSummaryView{
		Slug: p.Slug, Title: p.Title, Stage: p.Stage,
		Total: len(tasks), Counts: counts,
		Burndown: burndownView{
			DonePoints: doneP, RemainingPoints: remP,
			Unestimated: unestimated, PerDay: perDaySlice, HiddenDays: hiddenDays,
		},
	}
}

func downsampleBurndown(points []burndownDay, limit int) ([]burndownDay, int) {
	if limit < 4 || len(points) <= limit {
		return points, 0
	}
	keep := map[int]bool{0: true, len(points) - 1: true}
	minIndex, maxIndex := 0, 0
	for i := range points {
		if points[i].Points < points[minIndex].Points {
			minIndex = i
		}
		if points[i].Points > points[maxIndex].Points {
			maxIndex = i
		}
	}
	keep[minIndex], keep[maxIndex] = true, true
	for step := 1; step < limit-1 && len(keep) < limit; step++ {
		keep[int(float64(step)*float64(len(points)-1)/float64(limit-1))] = true
	}
	for index := 1; index < len(points)-1 && len(keep) < limit; index++ {
		keep[index] = true
	}
	indexes := make([]int, 0, len(keep))
	for index := range keep {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	out := make([]burndownDay, 0, len(indexes))
	for _, index := range indexes {
		out = append(out, points[index])
	}
	return out, len(points) - len(out)
}

// completionDay mirrors insight.completionDay: the task's Log section records
// "<date> ... completed by <actor>" on close, and that date is the only
// record of when a done task's points actually landed. Duplicated rather than
// imported — feature slices never import each other (arch_test.go).
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

func buildAgentView(w *workspace.Workspace, rec procmon.Record, tasks *store.TaskIndex) agentView {
	transcriptPath := filepath.Join(w.RunDir(rec.RunID), "transcript.log")
	last := rec.Started
	if fi, err := os.Stat(transcriptPath); err == nil {
		last = fi.ModTime()
	}
	return agentView{
		RunID: rec.RunID, Child: rec.Child, Task: rec.Task, Role: rec.Role,
		Runtime: rec.Runtime, PID: rec.PID,
		Started:       rec.Started.UTC().Format(time.RFC3339),
		State:         agentstate.Derive(w, rec, tasks),
		RuntimeSecs:   int64(time.Since(rec.Started).Seconds()),
		LastActivity:  last.UTC().Format(time.RFC3339),
		TranscriptURL: "/api/agents/transcript?run=" + rec.RunID,
		DiffURL:       "/api/agents/diff?run=" + rec.RunID,
	}
}

// liveAgents mirrors execution.liveAgents: read every run's proc.txt, keep
// the ones whose leader process is still alive (never trust the file alone —
// AliveRecord re-probes the PID/start-time pair), newest first. Duplicated
// for the same no-cross-slice-import reason as completionDay.
func liveAgents(w *workspace.Workspace) []procmon.Record {
	entries, err := os.ReadDir(w.RunsDir())
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	var out []procmon.Record
	for _, n := range names {
		rec, err := procmon.ReadRecord(filepath.Join(w.RunDir(n), "proc.txt"))
		if err != nil {
			continue
		}
		if procmon.AliveRecord(rec) {
			out = append(out, rec)
		}
	}
	return out
}
