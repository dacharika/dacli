package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/agentid"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/procmon"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/ulid"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

// Declaration fixtures remain readable for archived skill delivery metadata.
func seedHistoricalRuntime(t *testing.T, dir string, rt store.Runtime) {
	t.Helper()
	w, err := workspace.Find(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.CreateRuntime(w, agentid.RootID, rt, "historical fixture"); err != nil {
		t.Fatal(err)
	}
}

// These fixtures model a host-owned worker; no shipped dacli command launches it.
func writeNativeWorkerFixture(t *testing.T, dir, name, script string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name+".sh"), []byte("#!/bin/sh\nset -e\n"+script), 0o700); err != nil {
		t.Fatal(err)
	}
}

func executeNativeWorkerFixture(t *testing.T, dir, ref, name, claim string) {
	t.Helper()
	w, err := workspace.Find(dir)
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.FindTask(w, ref)
	if err != nil {
		t.Fatal(err)
	}
	run(t, dir, 0, "worktree", "add", "--task", ref)
	wt := w.WorktreePath(task.Project, task.Seq, task.Slug)
	child, token, err := agentid.Spawn(w, &agentid.Identity{ID: agentid.RootID, Grant: model.GrantRW}, "fixture-worker", model.GrantRW)
	if err != nil {
		t.Fatal(err)
	}
	runID := ulid.New()
	if err = os.MkdirAll(w.RunDir(runID), 0o700); err != nil {
		t.Fatal(err)
	}
	rec := procmon.Record{RunID: runID, Child: child, Task: task.ID, Runtime: "native-fixture", Started: time.Now(), Claims: strings.Split(claim, ",")}
	if err = procmon.WriteRecord(filepath.Join(w.RunDir(runID), "proc.txt"), rec); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(w.RunDir(runID), "invocation.txt"), []byte("run: "+runID+"\nchild: "+child+"\ntask: "+task.ID+"\nworktree: "+wt+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", filepath.Join(dir, name+".sh"))
	cmd.Dir = wt
	cmd.WaitDelay = 250 * time.Millisecond
	cmd.Env = append(os.Environ(), "DACLI_AGENT="+token)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("host fixture: %v\n%s", err, output)
	}
	if err = os.WriteFile(filepath.Join(w.RunDir(runID), "outcome.md"), []byte("outcome: finished\nexit: -\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run(t, dir, 0, "agent", "retire", child)
}
