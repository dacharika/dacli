// Replay (PROPOSALS P3): reconstruct any past run as a timeline — the brief
// each agent was actually handed (frozen at spawn) interleaved with every
// event it wrote, in ULID order. The question it answers is the one that is
// undebuggable everywhere else: what did this agent KNOW at the moment it
// went wrong. Read-only, offline — no re-running, no model calls.
package history

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/eventlog"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

func init() {
	Commands = append(Commands, clikit.Command{
		Path: "replay", Brief: "Reconstruct a run as brief+events interleaved (P3, offline)", Usage: "dacli replay <run-id-prefix> | dacli replay --task <ref> [--full]", Run: cmdReplay,
	})
}

type runMeta struct {
	runID, childID, taskID, turn, briefPath string
	outcome                                 string
}

func readRunMeta(w *workspace.Workspace, runDir string) runMeta {
	m := runMeta{briefPath: filepath.Join(runDir, "brief.md")}
	if f, err := os.Open(filepath.Join(runDir, "invocation.txt")); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			k, v, ok := strings.Cut(sc.Text(), ":")
			if !ok {
				continue
			}
			v = strings.TrimSpace(v)
			switch strings.TrimSpace(k) {
			case "run":
				m.runID = v
			case "child":
				m.childID = v
			case "task":
				m.taskID = v
			case "supervise_turn":
				m.turn = v
			}
		}
		_ = f.Close()
	}
	if raw, err := os.ReadFile(filepath.Join(runDir, "outcome.md")); err == nil {
		m.outcome = strings.ReplaceAll(strings.TrimSpace(string(raw)), "\n", " · ")
	}
	return m
}

// timelineEntry is one moment, keyed by its ULID so brief-deliveries and
// events sort into one true chronological order.
type timelineEntry struct {
	ulid string
	kind string // BRIEF | event-kind
	text string
}

func cmdReplay(ctx *clikit.Ctx, args []string) error {
	w, _, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	f, _ := clikit.ParseFlags(args)
	if err := f.Reject("task", "full"); err != nil {
		return err
	}
	full := f.Bool("full") // print whole briefs rather than a summary line

	// Gather the runs in scope: one by id prefix, or every run for a task.
	// taskRef is loop-invariant, so resolve it ONCE up front rather than
	// re-reading the whole task tree per run dir.
	var metas []runMeta
	taskRef := f.Get("task")
	wantTaskID, taskResolved := "", false
	if taskRef != "" {
		if t, err := store.FindTask(w, taskRef); err == nil {
			wantTaskID, taskResolved = t.ID, true
		}
	}
	entries, _ := os.ReadDir(w.RunsDir())
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		// Gate the metadata reads (invocation.txt + outcome.md, 2 file opens)
		// behind the cheap name check so a single-run replay doesn't read
		// metadata for every dir in the runs directory.
		switch {
		case len(f.Pos) > 0 && strings.HasPrefix(e.Name(), f.Pos[0]):
			metas = append(metas, readRunMeta(w, w.RunDir(e.Name())))
		case taskResolved:
			if m := readRunMeta(w, w.RunDir(e.Name())); m.taskID == wantTaskID {
				metas = append(metas, m)
			}
		}
	}
	if len(f.Pos) == 0 && taskRef == "" {
		return clikit.Usagef("usage: dacli replay <run-id-prefix> | dacli replay --task <ref> [--full]")
	}
	if len(metas) == 0 {
		return store.ErrNotFound{Ref: "run/task for replay"}
	}

	var line []timelineEntry
	childIDs := map[string]bool{}
	taskID := ""
	for _, m := range metas {
		taskID = m.taskID
		childIDs[m.childID] = true
		// The brief is delivered at spawn time — its run id IS that ULID, so
		// it sorts ahead of everything the child then wrote.
		summary := briefSummary(m.briefPath, full)
		label := "BRIEF delivered to " + clikit.OrDash(m.childID)
		if m.turn != "" {
			label += " (supervise turn " + m.turn + ")"
		}
		line = append(line, timelineEntry{ulid: m.runID, kind: "BRIEF", text: label + "\n" + summary})
	}

	// Every event any of these children wrote — what they DID, against what
	// they KNEW. Filtered to the run's children so a task's replay is that
	// task's agents, not the whole log.
	events, _ := eventlog.List(w, eventlog.Query{})
	for _, e := range events {
		if !childIDs[e.Actor] {
			continue
		}
		body := strings.ReplaceAll(strings.TrimSpace(e.Body), "\n", " ")
		if len(body) > 100 {
			body = body[:97] + "..."
		}
		line = append(line, timelineEntry{ulid: e.ID, kind: string(e.Kind),
			text: fmt.Sprintf("%s by %s: %s", e.Kind, e.Actor, body)})
	}

	sort.Slice(line, func(i, j int) bool { return line[i].ulid < line[j].ulid })

	fmt.Fprintf(ctx.Stdout, "=== replay · task %s · %d run(s) · %d agent(s) ===\n", clikit.OrDash(taskID), len(metas), len(childIDs))
	for _, e := range line {
		if e.kind == "BRIEF" {
			fmt.Fprintf(ctx.Stdout, "\n[%s] %s\n", clikit.Short(e.ulid, 10), indent(e.text))
		} else {
			fmt.Fprintf(ctx.Stdout, "  └ [%s] %s\n", clikit.Short(e.ulid, 10), e.text)
		}
	}
	for _, m := range metas {
		if m.outcome != "" {
			fmt.Fprintf(ctx.Stdout, "\noutcome (%s): %s\n", clikit.OrDash(clikit.Short(m.runID, 10)), m.outcome)
		}
	}
	fmt.Fprintln(ctx.Stdout, "\n(offline reconstruction — the brief is what the agent knew; the events are what it did)")
	return nil
}

// briefSummary returns the brief's task line and section headers (the shape
// of what the agent knew), or the whole thing under --full.
func briefSummary(path string, full bool) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "  (brief not recorded — run predates --record)"
	}
	if full {
		return indent(string(raw))
	}
	var out []string
	for _, l := range strings.Split(string(raw), "\n") {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "## ") {
			out = append(out, "  · "+strings.TrimPrefix(t, "## "))
		} else if strings.HasPrefix(t, "## Task:") || strings.HasPrefix(t, "# Task") {
			out = append(out, "  "+t)
		}
	}
	if len(out) == 0 {
		return "  (brief had no sections)"
	}
	return strings.Join(out, "\n") + "\n  (--full for the whole brief the agent saw)"
}

func indent(s string) string {
	var b strings.Builder
	for i, l := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		if i == 0 {
			b.WriteString(l)
		} else {
			b.WriteString("\n  " + l)
		}
	}
	return b.String()
}
