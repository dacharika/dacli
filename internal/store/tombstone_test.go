package store

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mlnomadpy/dacli/internal/procmon"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

func tombstoneWS(t *testing.T) *workspace.Workspace {
	t.Helper()
	w, err := workspace.Init(t.TempDir(), "x")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CreateProject(w, "a-root", "P", "p", "g", ""); err != nil {
		t.Fatal(err)
	}
	return w
}

// Seq allocation promises "monotonic-never-reuse". The git ceiling delivers it
// for any seq ever COMMITTED, but a workspace that records to its own branch
// has .dacli gitignored, so a task created AND removed between two ships was
// never committed — the seq came back, and a live agent's ref resolved to a
// DIFFERENT task with different content (issue #433, reported by the agent it
// happened to).
func TestARemovedSeqIsNeverHandedOutAgain(t *testing.T) {
	w := tombstoneWS(t)

	first, err := CreateTask(w, "a-root", "p", "First", TaskOpts{Accept: []string{"x"}})
	if err != nil {
		t.Fatal(err)
	}
	if first.Seq != 1 {
		t.Fatalf("fixture seq = %d, want 1", first.Seq)
	}
	if err := RemoveTask(w, first); err != nil {
		t.Fatal(err)
	}

	second, err := CreateTask(w, "a-root", "p", "Second", TaskOpts{Accept: []string{"x"}})
	if err != nil {
		t.Fatal(err)
	}
	if second.Seq == first.Seq {
		t.Fatalf("seq %d was handed out twice; a stale ref now resolves to a different task", second.Seq)
	}

	// And the stale ref resolves to NOTHING rather than to the new task —
	// which is the failure mode: an agent asked for its own task and got
	// someone else's.
	if got, err := FindTask(w, "001"); err == nil {
		t.Errorf("the removed ref 001 still resolves, to %q", got.Title)
	}
}

// The tombstone is a record, not a bare marker. Removal is the one operation
// that destroys history, so what it destroyed is written down — a reader who
// finds a gap in the numbering gets an answer instead of a mystery.
func TestTheTombstoneRecordsWhatWasRemoved(t *testing.T) {
	w := tombstoneWS(t)
	task, err := CreateTask(w, "a-root", "p", "A task that should never have existed", TaskOpts{Accept: []string{"x"}})
	if err != nil {
		t.Fatal(err)
	}
	id := task.ID
	if err := RemoveTask(w, task); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(w.TombstonesDir("p"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected exactly one tombstone, got %v (%v)", entries, err)
	}
	raw, err := os.ReadFile(filepath.Join(w.TombstonesDir("p"), entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, want := range []string{id, "A task that should never have existed", "001"} {
		if !strings.Contains(body, want) {
			t.Errorf("the tombstone must record %q:\n%s", want, body)
		}
	}
}

// A tombstone is NOT a task. listTasksRaw iterates model.AllStatuses
// explicitly, so the removed/ sibling must never be read back as backlog — a
// removed task reappearing in `task list` would be worse than the reuse this
// fixes.
func TestTombstonesAreNotListedAsTasks(t *testing.T) {
	w := tombstoneWS(t)
	task, err := CreateTask(w, "a-root", "p", "Removed", TaskOpts{Accept: []string{"x"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := RemoveTask(w, task); err != nil {
		t.Fatal(err)
	}
	got, err := ListTasks(w, "p", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("a tombstone was listed as a task: %v", got)
	}
}

// Unknown retained evidence must block even when the caller owns the task;
// the root orphan override is not the only destructive consumer (core499).
func TestRemoveTaskUnknownRunEvidenceHasNoEffects(t *testing.T) {
	for _, mode := range []string{"runs-file", "malformed", "proc-directory", "malformed-pid", "active-zero"} {
		t.Run(mode, func(t *testing.T) {
			w := tombstoneWS(t)
			task, err := CreateTask(w, "a-root", "p", "Keep unknown evidence", TaskOpts{Accept: []string{"x"}})
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(task.Path)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "runs-file" {
				if err := os.WriteFile(w.RunsDir(), []byte("not a directory"), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				dir := w.RunDir("retained")
				if err := os.MkdirAll(dir, 0755); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(dir, "proc.txt")
				if mode == "proc-directory" {
					if err := os.Mkdir(path, 0755); err != nil {
						t.Fatal(err)
					}
				} else {
					body := "not a process record\n"
					if mode == "malformed-pid" {
						body = "child: a-root\ntask: " + task.ID + "\npid: not-a-pid\n"
					}
					if mode == "active-zero" {
						body = "child: a-root\ntask: " + task.ID + "\npid: 0\n"
					}
					if err := os.WriteFile(path, []byte(body), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			err = RemoveTask(w, task)
			var refused ErrReferenced
			if !errors.As(err, &refused) || !strings.Contains(err.Error(), "evidence is unreadable") {
				t.Fatalf("unknown run evidence must refuse removal before effects: %v", err)
			}
			after, err := os.ReadFile(task.Path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("unknown evidence changed original task: %v", err)
			}
			entries, err := os.ReadDir(w.TombstonesDir("p"))
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("unknown evidence wrote tombstone: %v", entries)
			}
		})
	}
}

func TestRemoveTaskReadableRunEvidencePreservesLiveDeadAndOptionalCases(t *testing.T) {
	for _, mode := range []string{"missing-runs", "missing-proc", "dead", "live"} {
		t.Run(mode, func(t *testing.T) {
			w := tombstoneWS(t)
			task, err := CreateTask(w, "a-root", "p", "Readable removal", TaskOpts{Accept: []string{"x"}})
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(task.Path)
			if err != nil {
				t.Fatal(err)
			}
			if mode != "missing-runs" {
				dir := w.RunDir("retained")
				if err := os.MkdirAll(dir, 0755); err != nil {
					t.Fatal(err)
				}
				if mode != "missing-proc" {
					pid := os.Getpid()
					if mode == "dead" {
						cmd := exec.Command("true")
						if err := cmd.Run(); err != nil {
							t.Fatal(err)
						}
						pid = cmd.Process.Pid
					}
					rec := procmon.Record{RunID: "retained", Child: "a-root", Task: task.ID, PID: pid, PGID: pid}
					if err := procmon.WriteRecord(filepath.Join(dir, "proc.txt"), rec); err != nil {
						t.Fatal(err)
					}
				}
			}
			err = RemoveTask(w, task)
			entries, readErr := os.ReadDir(w.TombstonesDir("p"))
			if readErr != nil && !os.IsNotExist(readErr) {
				t.Fatal(readErr)
			}
			if mode == "live" {
				var refused ErrReferenced
				if !errors.As(err, &refused) || !strings.Contains(err.Error(), "live agent a-root") {
					t.Fatalf("readable live evidence must refuse: %v", err)
				}
				after, readErr := os.ReadFile(task.Path)
				if readErr != nil || !bytes.Equal(before, after) || len(entries) != 0 {
					t.Fatalf("live refusal changed task/tombstone: %v %v", readErr, entries)
				}
			} else if err != nil || len(entries) != 1 {
				t.Fatalf("readable dead or absent evidence should allow one removal: %v %v", err, entries)
			} else if _, err := os.Stat(task.Path); !os.IsNotExist(err) {
				t.Fatalf("successful removal kept task: %v", err)
			}
		})
	}
}

func TestRemoveTaskValidatesEveryRetainedRecordBeforeRemoval(t *testing.T) {
	w := tombstoneWS(t)
	task, err := CreateTask(w, "a-root", "p", "Keep whole scan", TaskOpts{Accept: []string{"x"}})
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(task.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := procmon.WriteRecord(filepath.Join(w.RunDir("a-terminal-other"), "proc.txt"), procmon.Record{Child: "a-other", Task: "other-task", Outcome: "completed"}); err != nil {
		t.Fatal(err)
	}
	dir := w.RunDir("z-malformed-other")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "proc.txt"), []byte("child: a-other\ntask: other-task\npid: malformed\noutcome: completed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	err = RemoveTask(w, task)
	if err == nil || !strings.Contains(err.Error(), "evidence is unreadable") {
		t.Fatalf("later nonmatching malformed evidence must refuse before removal: %v", err)
	}
	after, err := os.ReadFile(task.Path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("whole scan refusal changed task: %v", err)
	}
	entries, err := os.ReadDir(w.TombstonesDir("p"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("whole scan refusal wrote tombstone: %v", entries)
	}
}

func TestRemoveTaskPreservesSameAgentMultiRunLiveClaim(t *testing.T) {
	w := tombstoneWS(t)
	task, err := CreateTask(w, "a-root", "p", "Keep agent live claim", TaskOpts{Accept: []string{"x"}})
	if err != nil {
		t.Fatal(err)
	}
	for name, rec := range map[string]procmon.Record{
		"old-task-run":   {RunID: "old-task-run", Child: "a-root", Task: task.ID, PID: 0, Outcome: "completed"},
		"live-other-run": {RunID: "live-other-run", Child: "a-root", Task: "other-task", PID: os.Getpid()},
	} {
		if err := procmon.WriteRecord(filepath.Join(w.RunDir(name), "proc.txt"), rec); err != nil {
			t.Fatal(err)
		}
	}
	err = RemoveTask(w, task)
	if err == nil || !strings.Contains(err.Error(), "live agent a-root") {
		t.Fatalf("same agent live claim across runs must remain protected: %v", err)
	}
	if _, err := os.Stat(task.Path); err != nil {
		t.Fatalf("live multi-run claim lost task: %v", err)
	}
}
