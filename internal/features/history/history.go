// Package history reads preserved agent records; it never launches a provider.
package history

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/procmon"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/workspace"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var Commands = []clikit.Command{
	{Path: "runs list", Brief: "List preserved agent runs", Usage: "dacli runs list", Run: cmdRunsList},
	{Path: "runs show", Brief: "Read preserved invocation, outcome and transcript", Usage: "dacli runs show <run-id-prefix>", Run: cmdRunsShow},
	{Path: "agents", Brief: "Read bounded sourced worker history", JSON: true, Usage: "dacli agents [--project slug] [--active-only | --history] [--limit N] [--cursor RUN_ID]", Run: cmdAgents},
	{Path: "logs", Brief: "Read preserved transcript chunks", JSON: true, Usage: "dacli logs <run-id-prefix|child-id> [-f] [--tail N] [--cursor BYTE] [--limit BYTES] [--full]", Run: cmdLogs},
}

func cmdAgents(ctx *clikit.Ctx, args []string) error {
	w, _, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	f, err := clikit.ParseFlags(args)
	if err != nil {
		return err
	}
	if err = f.Reject("project", "active-only", "history", "limit", "cursor"); err != nil {
		return err
	}
	project := f.Get("project")
	if project != "" && !workspace.SafeSegment(project) {
		return clikit.Usagef("--project requires a valid project slug")
	}
	if f.Bool("active-only") && f.Bool("history") {
		return clikit.Usagef("--active-only and --history are mutually exclusive")
	}
	limit := 50
	if f.Bool("history") {
		limit = 0
	}
	if f.Get("limit") != "" {
		limit, err = f.Int("limit", 0)
		if err != nil || limit < 1 || limit > 200 {
			return clikit.Usagef("--limit must be an integer from 1 to 200")
		}
	}
	if err := renderAgentProgress(ctx, w, project, f.Bool("active-only"), limit, f.Get("cursor"), time.Now()); err != nil {
		return err
	}
	if !ctx.JSON && project == "" {
		for _, h := range pendingRootHandoffs(w) {
			fmt.Fprintf(ctx.Stdout, "%s task %s HANDOFF-REQUIRED\nnext: %s\n", clikit.Short(h.RunID, 10), h.TaskID, h.NextAction)
		}
	}
	return nil
}
func cmdRunsList(ctx *clikit.Ctx, args []string) error {
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
	entries, err := os.ReadDir(w.RunsDir())
	if err != nil {
		// No runs yet is normal and lists nothing. An unreadable runs directory
		// is a different fact, and reporting both as "no runs" hid the second
		// entirely. This function CAN return an error, unlike its two siblings
		// that cannot (see dacli 337).
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("cannot read the runs directory at %s: %w", w.RunsDir(), err)
	}
	names := []string{}
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names))) // ULIDs: newest first
	for _, n := range names {
		line := "(no outcome recorded)"
		if raw, err := os.ReadFile(filepath.Join(w.RunDir(n), "outcome.md")); err == nil {
			line = strings.ReplaceAll(strings.TrimSpace(string(raw)), "\n", " · ")
		}
		fmt.Fprintf(ctx.Stdout, "%s  %s\n", clikit.Short(n, 10), line)
	}
	return nil
}

func cmdRunsShow(ctx *clikit.Ctx, args []string) error {
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
		return clikit.Usagef("usage: dacli runs show <run-id-prefix>")
	}
	entries, _ := os.ReadDir(w.RunsDir())
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), f.Pos[0]) {
			continue
		}
		for _, name := range []string{"invocation.txt", "outcome.md", store.RootHandoffFile, store.ParentCommitRequestFile, store.ParentCommitReceiptFile, "brief.md", "transcript.log", "diagnostics.txt"} {
			if raw, err := os.ReadFile(filepath.Join(w.RunDir(e.Name()), name)); err == nil {
				fmt.Fprintf(ctx.Stdout, "=== %s ===\n%s\n", name, strings.TrimSpace(string(raw)))
			}
		}
		// First match wins, DELIBERATELY, and this return is a success rather
		// than a verdict — the distinction that makes it unlike the two landing
		// bugs the candidate-loop sweep found (task 363), where an early return
		// of a NEGATIVE result made every later candidate unreachable. Run ids
		// are ULIDs, so a prefix long enough to be typed is effectively unique,
		// and entries are read in sorted order, so the choice is deterministic.
		//
		// It does mean an ambiguous prefix shows one run without saying so,
		// where FindTask refuses an ambiguous task ref outright. That
		// inconsistency is recorded as a finding rather than changed here:
		// tightening it is a behaviour change to a read-only command, not part
		// of the sweep.
		return nil
	}
	return store.ErrNotFound{Ref: "run " + f.Pos[0]}
}

type agentProgressView struct {
	Schema     string                `json:"schema"`
	Version    int                   `json:"version"`
	ObservedAt time.Time             `json:"observed_at"`
	Workers    []store.WorkerExplain `json:"workers"`
	Total      int                   `json:"total"`
	Limit      int                   `json:"limit"`
	Cursor     string                `json:"cursor,omitempty"`
	NextCursor string                `json:"next_cursor,omitempty"`
	ActiveOnly bool                  `json:"active_only"`
}

// renderAgentProgress consumes the same store projection as `explain`; it does
// not infer a parallel worker lifecycle from PID/RAM presentation details.
func renderAgentProgress(ctx *clikit.Ctx, w *workspace.Workspace, project string, activeOnly bool, limit int, cursor string, now time.Time) error {
	projects := []string{project}
	if project == "" {
		projects = nil
		all, err := store.ListProjects(w)
		if err != nil {
			return err
		}
		for _, item := range all {
			projects = append(projects, item.Slug)
		}
	}
	view := agentProgressView{Schema: store.ProgressExplainSchema, Version: 1, ObservedAt: now.UTC(), Workers: []store.WorkerExplain{}}
	for _, slug := range projects {
		projection, err := store.ExplainProject(w, slug, now)
		if err != nil {
			return err
		}
		view.Workers = append(view.Workers, projection.Workers...)
	}
	sort.Slice(view.Workers, func(i, j int) bool { return view.Workers[i].RunID.Value < view.Workers[j].RunID.Value })
	view.Workers, view.Total, view.NextCursor = pageWorkers(view.Workers, activeOnly, limit, cursor)
	view.Limit, view.Cursor, view.ActiveOnly = limit, cursor, activeOnly
	if ctx.JSON {
		return clikit.EmitJSON(ctx, view)
	}
	if len(view.Workers) == 0 {
		fmt.Fprintln(ctx.Stdout, "no recorded workers for project")
		return nil
	}
	for _, worker := range view.Workers {
		fmt.Fprintf(ctx.Stdout, "%s agent=%s task=%s role=%s runtime=%s state=%s (source=%s observed=%s stale=%t)\n  claims: %s\n  next: %s\n",
			clikit.Short(worker.RunID.Value, 10), clikit.OrDash(worker.AgentID.Value), clikit.OrDash(worker.TaskID.Value), clikit.OrDash(worker.Role.Value), clikit.OrDash(worker.Runtime.Value), worker.State.Value,
			worker.State.Source, worker.State.ObservedAt.Format(time.RFC3339), worker.State.Stale, clikit.OrDash(strings.Join(worker.Claims.Value, ", ")), worker.NextAction.Value)
	}
	return nil
}

func pageWorkers(workers []store.WorkerExplain, activeOnly bool, limit int, cursor string) ([]store.WorkerExplain, int, string) {
	eligible := make([]store.WorkerExplain, 0, len(workers))
	for _, worker := range workers {
		if activeOnly && worker.State.Value != "live" && worker.State.Value != "handoff-required" {
			continue
		}
		eligible = append(eligible, worker)
	}
	total := len(eligible)
	filtered := make([]store.WorkerExplain, 0, len(eligible))
	for _, worker := range eligible {
		if cursor != "" && worker.RunID.Value <= cursor {
			continue
		}
		filtered = append(filtered, worker)
	}
	next := ""
	if limit > 0 && len(filtered) > limit {
		filtered = filtered[:limit]
		next = filtered[len(filtered)-1].RunID.Value
	}
	return filtered, total, next
}

func cmdLogs(ctx *clikit.Ctx, args []string) error {
	w, _, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	f, err := clikit.ParseFlags(args, "tail")
	if err != nil {
		return err
	}
	if err := f.Reject("f", "follow", "tail", "cursor", "limit", "full"); err != nil {
		return err
	}
	if len(f.Pos) == 0 {
		return clikit.Usagef("usage: dacli logs <run-id-prefix|child-id> [-f] [--tail N] [--cursor BYTE] [--limit BYTES] [--full]")
	}
	ref := f.Pos[0]
	rec, haveRec := readProcByRef(w, ref)
	runID := rec.RunID
	if !haveRec {
		entries, _ := os.ReadDir(w.RunsDir())
		for _, e := range entries {
			if e.IsDir() && strings.HasPrefix(e.Name(), ref) {
				runID = e.Name()
				break
			}
		}
	}
	if runID == "" {
		return store.ErrNotFound{Ref: "run " + ref}
	}
	path := filepath.Join(w.RunDir(runID), "transcript.log")

	limit := 64 << 10
	if f.Bool("full") {
		limit = 0
	}
	if f.Get("limit") != "" {
		parsed, parseErr := f.Int("limit", 0)
		if parseErr != nil || parsed < 1 || parsed > 1<<20 {
			return clikit.Usagef("--limit must be an integer from 1 to 1048576")
		}
		limit = parsed
	}
	fileSize := 0
	if info, statErr := os.Stat(path); statErr == nil {
		fileSize = int(info.Size())
	}
	start := 0
	if f.Get("cursor") != "" {
		parsed, parseErr := f.Int("cursor", -1)
		if parseErr != nil || parsed < 0 {
			return clikit.Usagef("--cursor must be a non-negative byte offset")
		}
		start = min(parsed, fileSize)
	}
	if start > 0 && len(f.All("tail")) > 0 {
		return clikit.Usagef("--cursor and --tail are mutually exclusive")
	}
	var data []byte
	if n, err := f.Int("tail", 0); err != nil {
		return err
	} else if n <= 0 && len(f.All("tail")) > 0 {
		return clikit.Usagef("--tail must be a positive integer, got %d", n)
	} else if n > 0 {
		data, _ = os.ReadFile(path)
		tailed := lastLines(data, n)
		start = fileSize - len(tailed)
		data = tailed
	}
	if len(f.All("tail")) == 0 {
		data = readTranscriptRange(path, start, limit, fileSize)
	}
	chunk, consumed := transcriptChunk(data, limit)
	if ctx.JSON {
		if f.Bool("f") || f.Bool("follow") {
			return clikit.Usagef("--json uses repeatable --cursor polling and cannot be combined with --follow")
		}
		var rendered bytes.Buffer
		renderTranscriptTo(&rendered, chunk)
		return clikit.EmitJSON(ctx, struct {
			Schema     string    `json:"schema"`
			RunID      string    `json:"run_id"`
			ObservedAt time.Time `json:"observed_at"`
			Cursor     int       `json:"cursor"`
			NextCursor int       `json:"next_cursor"`
			EOF        bool      `json:"eof"`
			Output     string    `json:"output"`
		}{"transcript-chunk/v1", runID, time.Now().UTC(), start, start + consumed, start+consumed >= fileSize, rendered.String()})
	}
	// Detached stream-json runs write RAW JSON events to the transcript (the tee
	// only runs on the foreground path), so render each line to readable text on
	// read — logs and -f show the same legible output as a text runtime.
	renderTranscriptTo(ctx.Stdout, chunk)
	var offset int64
	offset = int64(start + consumed)
	if !(f.Bool("f") || f.Bool("follow")) {
		return nil
	}
	// Follow: drain appended bytes until the agent's process is gone (one final
	// drain after it exits), so the tail ends when the work does. Advance the
	// offset only to the last newline so a JSON event line is never split across
	// two renders.
	drain := func(final bool) {
		fi, e := os.Stat(path)
		if e != nil || fi.Size() <= offset {
			return
		}
		chunk := make([]byte, fi.Size()-offset)
		fh, e2 := os.Open(path)
		if e2 != nil {
			return
		}
		n, _ := fh.ReadAt(chunk, offset)
		_ = fh.Close()
		chunk = chunk[:n]
		if !final {
			nl := bytes.LastIndexByte(chunk, '\n')
			if nl < 0 {
				return // no complete line yet; wait for the rest
			}
			chunk = chunk[:nl+1]
		}
		renderTranscriptTo(ctx.Stdout, chunk)
		offset += int64(len(chunk))
	}
	for {
		time.Sleep(700 * time.Millisecond)
		drain(false)
		if !(haveRec && procmon.AliveRecord(rec)) {
			drain(true) // flush any trailing partial line once the work is done
			return nil
		}
	}
}

func transcriptChunk(data []byte, limit int) ([]byte, int) {
	if limit <= 0 || len(data) <= limit {
		return data, len(data)
	}
	end := limit
	if nl := bytes.LastIndexByte(data[:limit], '\n'); nl >= 0 {
		end = nl + 1
	}
	return data[:end], end
}

func readTranscriptRange(path string, start, limit, size int) []byte {
	if start >= size {
		return nil
	}
	length := size - start
	if limit > 0 && length > limit {
		length = limit
	}
	data := make([]byte, length)
	fh, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = fh.Close() }()
	n, _ := fh.ReadAt(data, int64(start))
	return data[:n]
}

// renderTranscriptTo writes b to out with each complete line rendered from
// stream-json to readable text (assistant text / [tool: X] markers); a
// plain-text line passes through unchanged and blank lines are dropped. This is
// the read-side counterpart of teeStreamJSON: it makes a detached run's raw
// stream-json transcript as legible as a foreground run's already-teed one.
func renderTranscriptTo(out io.Writer, b []byte) {
	for _, ln := range bytes.Split(b, []byte("\n")) {
		if len(bytes.TrimSpace(ln)) == 0 {
			continue
		}
		text, _ := renderStreamLine(ln)
		if text == "" {
			text, _ = renderCodexLine(ln, streamUsage{})
		}
		if text != "" {
			fmt.Fprintln(out, text)
		}
	}
}

func lastLines(b []byte, n int) []byte {
	count := 0
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] == '\n' {
			count++
			if count > n {
				return b[i+1:]
			}
		}
	}
	return b
}

// splitClaims parses one comma-separated --claim value into cleaned paths.
func readProcByRef(w *workspace.Workspace, ref string) (procmon.Record, bool) {
	entries, err := os.ReadDir(w.RunsDir())
	if err != nil {
		return procmon.Record{}, false
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		rec, err := procmon.ReadRecord(filepath.Join(w.RunDir(e.Name()), "proc.txt"))
		if err != nil {
			continue
		}
		if strings.HasPrefix(rec.RunID, ref) || rec.Child == ref {
			return rec, true
		}
	}
	return procmon.Record{}, false
}

type streamUsage struct {
	InputTokens  int
	OutputTokens int
	NumTurns     int
	CostUSD      float64
	SessionID    string
	FinalMessage string
	ExitOutcome  string
	found        bool
}

type codexEvent struct {
	Type     string `json:"type"`
	ThreadID string `json:"thread_id"`
	Item     struct {
		Type string `json:"type"`
		Text string `json:"text"`
		Name string `json:"name"`
	} `json:"item"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

func renderCodexLine(line []byte, prior streamUsage) (string, streamUsage) {
	var ev codexEvent
	if json.Unmarshal(bytes.TrimSpace(line), &ev) != nil {
		return string(bytes.TrimSpace(line)), prior
	}
	switch ev.Type {
	case "thread.started":
		prior.SessionID = ev.ThreadID
	case "item.completed":
		if ev.Item.Type == "agent_message" {
			prior.FinalMessage = strings.TrimSpace(ev.Item.Text)
			return prior.FinalMessage, prior
		}
		if ev.Item.Type != "" {
			return "[item: " + ev.Item.Type + "]", prior
		}
	case "turn.completed":
		prior.InputTokens, prior.OutputTokens, prior.ExitOutcome, prior.found = ev.Usage.InputTokens, ev.Usage.OutputTokens, "completed", true
	case "turn.failed":
		prior.ExitOutcome, prior.found = "failed", true
	}
	return "", prior
}

type streamEvent struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
	Result    string `json:"result"`
	IsError   bool   `json:"is_error"`
	Message   struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
			Name string `json:"name"`
		} `json:"content"`
	} `json:"message"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	NumTurns int     `json:"num_turns"`
	CostUSD  float64 `json:"total_cost_usd"`
}

// renderStreamLine turns one stream-json line into its human-readable rendering
// (assistant text and [tool: X] markers) and, when it is the terminating
// `result` event, its usage. A line that is not a JSON event is returned
// verbatim so nothing the child emits is ever dropped. text is "" for events
// with no human-facing content (system/result/empty), letting callers skip
// them. This is the single shared decoder for both the live tee and the
// render-on-read transcript readers, so foreground and detached runs render
// identically.
func renderStreamLine(line []byte) (text string, usage streamUsage) {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return "", streamUsage{}
	}
	if trimmed[0] != '{' { // fast path: not an event object — pass through
		return string(trimmed), streamUsage{}
	}
	var ev streamEvent
	if err := json.Unmarshal(trimmed, &ev); err != nil {
		return string(trimmed), streamUsage{} // not an event — verbatim
	}
	switch ev.Type {
	case "assistant":
		var b strings.Builder
		for _, c := range ev.Message.Content {
			switch c.Type {
			case "text":
				if s := strings.TrimSpace(c.Text); s != "" {
					b.WriteString(s)
					b.WriteByte('\n')
				}
			case "tool_use":
				fmt.Fprintf(&b, "[tool: %s]\n", c.Name)
			}
		}
		return strings.TrimRight(b.String(), "\n"), streamUsage{}
	case "result":
		return "", streamUsage{
			InputTokens:  ev.Usage.InputTokens,
			OutputTokens: ev.Usage.OutputTokens,
			NumTurns:     ev.NumTurns,
			CostUSD:      ev.CostUSD,
			SessionID:    ev.SessionID,
			FinalMessage: strings.TrimSpace(ev.Result),
			ExitOutcome:  map[bool]string{false: "completed", true: "failed"}[ev.IsError],
			found:        true,
		}
	}
	return "", streamUsage{}
}
