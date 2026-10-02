package history

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/procmon"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/workspace"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func runID(i int) string { return fmt.Sprintf("01RUN%04dZZZZZZZZZZZZZZZZZ", i) }

// mkRun creates a run directory with an outcome line.

func mkRun(t *testing.T, w *workspace.Workspace, id, outcome string) string {
	t.Helper()
	dir := w.RunDir(id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if outcome != "" {
		if err := os.WriteFile(filepath.Join(dir, "outcome.md"), []byte(outcome), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestRuntimeDoctorSeparatesDeclaredVerifiedAndFailedRO is the regression for
// dacli 365: a sandbox flag in a committed adapter is an assumption, not proof
// that this installed CLI accepts it. The fixtures are local shell scripts;
// the probe must never need a model or a network service.

func TestRunsListToleratesAShortRunDirName(t *testing.T) {
	w := newExecWS(t)
	mkRun(t, w, "short", "outcome: ok\n")
	mkRun(t, w, runID(1), "outcome: failed\n")

	var out *bytes.Buffer
	panicked := func() (p bool) {
		defer func() { p = recover() != nil }()
		var ctx *clikit.Ctx
		ctx, out, _ = newCtx(w.Root)
		if err := cmdRunsList(ctx, nil); err != nil {
			t.Errorf("cmdRunsList: %v", err)
		}
		return
	}()
	if panicked {
		t.Fatal("cmdRunsList panicked on a run-dir name shorter than the display width")
	}
	if !strings.Contains(out.String(), "short") {
		t.Errorf("the short-named run was not listed:\n%s", out)
	}
	if !strings.Contains(out.String(), runID(1)[:10]) {
		t.Errorf("the normal-length run was not listed:\n%s", out)
	}
}

func TestRunsListNewestFirst(t *testing.T) {
	w := newExecWS(t)
	mkRun(t, w, runID(1), "outcome: ok\nexit: 0\n")
	mkRun(t, w, runID(2), "outcome: failed\nexit: 1\n")
	mkRun(t, w, runID(3), "")

	ctx, out, _ := newCtx(w.Root)
	if err := cmdRunsList(ctx, nil); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d:\n%s", len(lines), out)
	}
	if !strings.HasPrefix(lines[0], runID(3)[:10]) {
		t.Errorf("newest run must be first; got %q", lines[0])
	}
	if !strings.Contains(lines[0], "no outcome recorded") {
		t.Errorf("a run with no outcome must still be listed and labelled: %q", lines[0])
	}
	if !strings.HasPrefix(lines[2], runID(1)[:10]) {
		t.Errorf("oldest run must be last; got %q", lines[2])
	}
	if !strings.Contains(lines[1], "outcome: failed · exit: 1") {
		t.Errorf("multi-line outcome must flatten with ' · '; got %q", lines[1])
	}
}

// `runs show` resolves a run-id PREFIX (nobody types a whole ULID) and prints
// the records in a fixed order, including durable optional-artifact warnings.

func TestRunsShowByPrefix(t *testing.T) {
	w := newExecWS(t)
	dir := mkRun(t, w, "01RUN0001", "outcome: ok\n")
	for name, body := range map[string]string{
		"invocation.txt":  "run: 01RUN0001\nchild: a-1\n",
		"brief.md":        "## Task: do the thing\n",
		"transcript.log":  "child said hello\n",
		"diagnostics.txt": "could not record optional usage.txt\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	ctx, out, _ := newCtx(w.Root)
	if err := cmdRunsShow(ctx, []string{"01RUN"}); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	order := []string{"=== invocation.txt ===", "=== outcome.md ===", "=== brief.md ===", "=== transcript.log ===", "=== diagnostics.txt ==="}
	at := -1
	for _, want := range order {
		i := strings.Index(got, want)
		if i < 0 {
			t.Fatalf("missing section %q in:\n%s", want, got)
		}
		if i < at {
			t.Errorf("section %q out of order", want)
		}
		at = i
	}
	if !strings.Contains(got, "do the thing") || !strings.Contains(got, "child said hello") {
		t.Errorf("run record bodies not printed:\n%s", got)
	}
	if !strings.Contains(got, "could not record optional usage.txt") {
		t.Errorf("durable warning not surfaced:\n%s", got)
	}

	ctx2, _, _ := newCtx(w.Root)
	if code := clikit.ExitCode(cmdRunsShow(ctx2, []string{"01NOPE"})); code != 4 {
		t.Errorf("unknown run prefix exit %d, want 4 (not found)", code)
	}
	ctx3, _, _ := newCtx(w.Root)
	if code := clikit.ExitCode(cmdRunsShow(ctx3, nil)); code != 2 {
		t.Errorf("missing argument exit %d, want 2", code)
	}
}

// liveAgents must be runaways-included, ghosts-excluded: liveness is PROBED,
// never trusted from the file. A run whose recorded pid is long gone must not
// resurface in `agents` / `kill` / `wait`.

func TestAgentsJSONUsesSourcedProgressWorkerProjection(t *testing.T) {
	w := newExecWS(t)
	task := mustTask(t, w, "explain worker", store.TaskOpts{Accept: []string{"reported"}, Estimate: "1,2,3"})
	id := runID(8)
	if err := os.MkdirAll(w.RunDir(id), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := procmon.WriteRecord(filepath.Join(w.RunDir(id), "proc.txt"), procmon.Record{RunID: id, Child: "a-json", Task: task.ID, Role: "builder", Runtime: "codex", PID: 1 << 30, PGID: 1 << 30, Started: time.Now(), Claims: []string{"internal/store"}}); err != nil {
		t.Fatal(err)
	}
	oldObserver := store.ObserveDeliveryPRs
	store.ObserveDeliveryPRs = func(string) ([]store.DeliveryPR, error) { return nil, nil }
	t.Cleanup(func() { store.ObserveDeliveryPRs = oldObserver })
	oldCache := store.SharedProgressExplainCache
	store.SharedProgressExplainCache = store.NewExplainCache(4, time.Second, time.Minute)
	t.Cleanup(func() { store.SharedProgressExplainCache = oldCache })

	ctx, out, _ := newCtx(w.Root)
	ctx.JSON = true
	if err := cmdAgents(ctx, []string{"--project", testProject}); err != nil {
		t.Fatal(err)
	}
	var got agentProgressView
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("decode agents JSON: %v\n%s", err, out)
	}
	if got.Schema != store.ProgressExplainSchema || len(got.Workers) != 1 {
		t.Fatalf("worker projection = %+v", got)
	}
	worker := got.Workers[0]
	if worker.State.Value != "finished-unfinalized" || worker.State.Source == "" || worker.State.ObservedAt.IsZero() || worker.State.Stale {
		t.Fatalf("worker state lacks canonical lifecycle/source/freshness: %+v", worker.State)
	}
	if worker.Role.Value != "builder" || len(worker.Claims.Value) != 1 || worker.NextAction.Value == "" {
		t.Fatalf("worker routing/claims/next action incomplete: %+v", worker)
	}
}

func TestPageWorkersBoundsLargeHistoryAndUsesStableCursor(t *testing.T) {
	workers := make([]store.WorkerExplain, 10_000)
	for i := range workers {
		workers[i].RunID.Value = fmt.Sprintf("%026d", i)
		workers[i].State.Value = "succeeded"
		if i%100 == 0 {
			workers[i].State.Value = "live"
		}
	}
	first, total, next := pageWorkers(workers, false, 50, "")
	if len(first) != 50 || total != 10_000 || next != first[49].RunID.Value {
		t.Fatalf("first page = len %d total %d next %q", len(first), total, next)
	}
	second, total, _ := pageWorkers(workers, false, 50, next)
	if len(second) != 50 || total != 10_000 || second[0].RunID.Value <= next {
		t.Fatalf("cursor page duplicated or skipped boundary: len %d total %d first %q cursor %q", len(second), total, second[0].RunID.Value, next)
	}
	active, total, _ := pageWorkers(workers, true, 200, "")
	if len(active) != 100 || total != 100 {
		t.Fatalf("active page = len %d total %d, want 100", len(active), total)
	}
}

func TestReadTranscriptRangeBoundsLargeHistoricalArtifact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.log")
	large := bytes.Repeat([]byte("0123456789abcdef\n"), 1<<19)
	if err := os.WriteFile(path, large, 0o600); err != nil {
		t.Fatal(err)
	}
	page := readTranscriptRange(path, 1024, 4096, len(large))
	if len(page) != 4096 || !bytes.Equal(page, large[1024:1024+4096]) {
		t.Fatalf("bounded range = %d bytes, want the exact 4096-byte page", len(page))
	}
}

// `dacli agents` must show whether a live agent is actually moving — RAM/CPU
// and uptime alone can't tell a reasoning agent from a wedged one (dacli 270).
// This drives cmdAgents' real output (not agentstate.Derive directly) to prove
// the printed line carries the state, and that a stalled agent reads visually
// distinct from a busy one without passing --tail.
