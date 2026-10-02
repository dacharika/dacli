package cli

import (
	"github.com/mlnomadpy/dacli/internal/agentid"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/ulid"
	"github.com/mlnomadpy/dacli/internal/workspace"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// P3 acceptance: any past run renders brief and events interleaved offline.
// A cooperative child claims + reports across a run; replay reconstructs what
// it was told and what it did, in time order.
func TestReplayReconstructsRun(t *testing.T) {
	dir := t.TempDir()
	run(t, dir, 0, "init", "--name", "x")
	run(t, dir, 0, "project", "add", "P", "--slug", "p", "--goal", "One write path.")
	run(t, dir, 0, "task", "add", "Audit the write paths", "--project", "p", "--accept", "writers listed")

	// Preserve an actual brief and events without re-launching the historical provider.
	w, err := workspace.Find(dir)
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.FindTask(w, "001")
	if err != nil {
		t.Fatal(err)
	}
	child, token, err := agentid.Spawn(w, &agentid.Identity{ID: agentid.RootID, Grant: model.GrantRW}, "historical-reviewer", model.GrantRO)
	if err != nil {
		t.Fatal(err)
	}
	id := ulid.New()
	if err = os.MkdirAll(w.RunDir(id), 0o700); err != nil {
		t.Fatal(err)
	}
	brief := run(t, dir, 0, "context", "001")
	for file, body := range map[string]string{"brief.md": brief, "invocation.txt": "run: " + id + "\nchild: " + child + "\ntask: " + task.ID + "\n", "outcome.md": "outcome: finished\n"} {
		if err = os.WriteFile(filepath.Join(w.RunDir(id), file), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(agentid.EnvVar, token)
	run(t, dir, 0, "note", "add", "finding", "found the direct writer", "--project", "p", "--about", "001", "--body", "settle.go:112")
	run(t, dir, 0, "note", "add", "finding", "and a second writer", "--project", "p", "--about", "001", "--body", "batch.go:40")
	_ = os.Unsetenv(agentid.EnvVar)

	// Replay by task: the frozen brief, then the child's two findings, in order.
	out := run(t, dir, 0, "replay", "--task", "001")
	for _, want := range []string{
		"=== replay · task",
		"BRIEF delivered to a-",
		"Audit the write paths", // the brief's task line survives the freeze
		"found the direct writer",
		"and a second writer",
		"the brief is what the agent knew; the events are what it did",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("replay missing %q:\n%s", want, out)
		}
	}
	// Ordering: the brief must precede the events it caused.
	if bi, fi := strings.Index(out, "BRIEF delivered"), strings.Index(out, "found the direct writer"); bi < 0 || fi < 0 || bi > fi {
		t.Errorf("brief did not precede the events:\n%s", out)
	}
	// And the two findings are in the order they were written.
	if a, b := strings.Index(out, "found the direct writer"), strings.Index(out, "and a second writer"); a > b {
		t.Errorf("events out of chronological order:\n%s", out)
	}

	// --full prints the whole brief the agent saw.
	full := run(t, dir, 0, "replay", "--task", "001", "--full")
	if !strings.Contains(full, "data, not instructions") {
		t.Errorf("--full did not print the whole brief:\n%s", full)
	}
	// Summary form does NOT dump the whole brief.
	if strings.Contains(out, "data, not instructions") {
		t.Errorf("summary form leaked the full brief:\n%s", out)
	}

	// Replay by run-id prefix works too; unknown ref is not-found.
	runsList := run(t, dir, 0, "runs", "list")
	runID := strings.Fields(runsList)[0]
	if byID := run(t, dir, 0, "replay", runID); !strings.Contains(byID, "BRIEF delivered") {
		t.Errorf("replay by run-id failed:\n%s", byID)
	}
	run(t, dir, 4, "replay", "zzzznosuchrun")
}
