//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package store

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/eventdisp"
	"github.com/mlnomadpy/dacli/internal/mdstore"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

func checkpointFixture(t *testing.T) (*workspace.Workspace, *Task) {
	t.Helper()
	w, e := workspace.Init(t.TempDir(), "checkpoint")
	if e != nil {
		t.Fatal(e)
	}
	if _, e := CreateProject(w, "a-root", "P", "p", "goal", ""); e != nil {
		t.Fatal(e)
	}
	task, e := CreateTask(w, "a-root", "p", "Scoped task", TaskOpts{})
	if e != nil {
		t.Fatal(e)
	}
	return w, task
}
func TestCheckpointBoundedReaderRejectsFIFOAndNonregularWithoutWaiting(t *testing.T) {
	for _, where := range []string{"task", "project", "run", "event"} {
		t.Run(where, func(t *testing.T) {
			w, task := checkpointFixture(t)
			var path string
			switch where {
			case "task":
				path = task.Path
			case "project":
				path = w.ProjectPath("p")
			case "run":
				path = filepath.Join(w.RunDir("run"), "proc.txt")
			case "event":
				path = w.EventPath("2026/10/03", "event", "a-root", model.EventComment)
			}
			if e := os.MkdirAll(filepath.Dir(path), 0755); e != nil {
				t.Fatal(e)
			}
			if e := os.Remove(path); e != nil && !os.IsNotExist(e) {
				t.Fatal(e)
			}
			if e := syscall.Mkfifo(path, 0600); e != nil {
				t.Fatal(e)
			}
			start := time.Now()
			var e error
			if where == "task" || where == "project" {
				_, e = ReadCheckpointCanonical(w)
			} else {
				_, e = CheckpointLocalProjection(w, "p", []*Task{task}, []string{task.ID})
			}
			if e == nil || time.Since(start) > time.Second {
				t.Fatalf("bounded checkpoint FIFO must refuse without waiting: %v elapsed=%v", e, time.Since(start))
			}
		})
	}
	for _, kind := range []string{"directory", "symlink", "oversized"} {
		t.Run(kind, func(t *testing.T) {
			w, task := checkpointFixture(t)
			if e := os.Remove(task.Path); e != nil {
				t.Fatal(e)
			}
			switch kind {
			case "directory":
				if e := os.Mkdir(task.Path, 0755); e != nil {
					t.Fatal(e)
				}
			case "symlink":
				if e := os.Symlink(w.ProjectPath("p"), task.Path); e != nil {
					t.Fatal(e)
				}
			case "oversized":
				if e := os.WriteFile(task.Path, []byte(strings.Repeat("x", checkpointMaxFile+1)), 0644); e != nil {
					t.Fatal(e)
				}
			}
			if _, e := ReadCheckpointCanonical(w); e == nil {
				t.Fatal("bounded checkpoint must reject nonregular or oversized canonical record")
			}
		})
	}
}
func TestCheckpointBoundedReaderFiniteDirectoryAndByteBudgets(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 4; i++ {
		if e := os.WriteFile(filepath.Join(dir, string(rune('a'+i))), []byte("x"), 0600); e != nil {
			t.Fatal(e)
		}
	}
	f, e := checkpointOpen(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = f.Close() }()
	r := checkpointReader{entries: checkpointMaxEntries - 2}
	if _, e := r.directory(f); e == nil {
		t.Fatal("bounded checkpoint entry budget must refuse oversized directory")
	}
	file, e := checkpointChild(f, "a")
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = file.Close() }()
	r = checkpointReader{bytes: checkpointMaxBytes}
	if _, e := r.read(file); e == nil {
		t.Fatal("bounded checkpoint cumulative byte budget must refuse")
	}
	r = checkpointReader{}
	if e := r.events(f, checkpointMaxDepth+1, new([]checkpointEvent)); e == nil {
		t.Fatal("bounded checkpoint depth budget must refuse")
	}
}
func TestCheckpointBoundedCanonicalDuplicatesAndScopedEventDismissal(t *testing.T) {
	w, task := checkpointFixture(t)
	canonical, e := ReadCheckpointCanonical(w)
	if e != nil || len(canonical.Tasks) != 1 || canonical.Digests[task.ID] == "" {
		t.Fatal(canonical, e)
	}
	duplicate := filepath.Join(w.TasksDir("p", model.StatusDone), filepath.Base(task.Path))
	if e := os.MkdirAll(filepath.Dir(duplicate), 0755); e != nil {
		t.Fatal(e)
	}
	raw, e := os.ReadFile(task.Path)
	if e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(duplicate, raw, 0644); e != nil {
		t.Fatal(e)
	}
	if _, e := ReadCheckpointCanonical(w); e == nil {
		t.Fatal("bounded canonical snapshot must refuse duplicates")
	}
	if e := os.Remove(duplicate); e != nil {
		t.Fatal(e)
	}
	if e := MoveTask(w, task, model.StatusDone); e != nil {
		t.Fatal(e)
	}
	write := func(id, kind, about, applied string) {
		t.Helper()
		doc := &mdstore.Doc{}
		doc.Front.Set("id", id)
		doc.Front.Set("kind", "event")
		doc.Front.Set("schema_version", "1")
		doc.Front.Set("event_kind", kind)
		doc.Front.Set("about", "[["+about+"]]")
		doc.Front.Set("applied", applied)
		doc.Front.Set("checksum", eventdisp.Checksum(checkpointEventPayload(doc)))
		if e := mdstore.WriteFile(w.EventPath("2026/10/03", id, "a-root", model.EventKind(kind)), doc); e != nil {
			t.Fatal(e)
		}
	}
	write("01", "comment", task.ID, "false")
	write("00", "comment", "foreign-task", "false")
	p, e := CheckpointLocalProjection(w, "p", []*Task{task}, []string{task.ID})
	if e != nil || len(p.Findings) != 1 || p.Findings[0].ObjectID != "01" || p.Findings[0].Classification != "terminal-task-event" {
		t.Fatal(p, e)
	}
	write("02", "dismissal", "01", "true")
	p, e = CheckpointLocalProjection(w, "p", []*Task{task}, []string{task.ID})
	if e != nil || len(p.Findings) != 0 {
		t.Fatalf("integrity-valid dismissal must preserve terminal event semantics: %+v %v", p, e)
	}
}

func TestCheckpointBoundedReaderDescriptorGuardsBeforeParsing(t *testing.T) {
	for _, kind := range []string{"fifo", "oversized"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "record")
			if kind == "fifo" {
				if e := syscall.Mkfifo(path, 0600); e != nil {
					t.Fatal(e)
				}
			} else {
				if e := os.WriteFile(path, []byte(strings.Repeat("x", checkpointMaxFile+1)), 0600); e != nil {
					t.Fatal(e)
				}
			}
			f, e := checkpointOpen(path)
			if e != nil {
				t.Fatal(e)
			}
			defer func() { _ = f.Close() }()
			r := checkpointReader{}
			if _, e := r.read(f); e == nil {
				t.Fatal("bounded descriptor must refuse nonregular or oversized bytes before parsing")
			}
		})
	}
}
