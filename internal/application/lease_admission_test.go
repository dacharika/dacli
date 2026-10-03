package application

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/store"
)

func TestLeaseAdmissionActiveTaskPreservesCanonicalReadiness(t *testing.T) {
	w, _ := readFixture(t)
	if _, err := store.CreateProject(w, "root", "p", "p", "goal", ""); err != nil {
		t.Fatal(err)
	}
	dep, err := store.CreateTask(w, "root", "p", "Dependency", store.TaskOpts{})
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(w, "root", "p", "Claimed candidate", store.TaskOpts{DependsOn: []string{dep.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.MoveTask(w, task, model.StatusActive); err != nil {
		t.Fatal(err)
	}
	a, err := NewLeaseAdmission(w)
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []model.Status{model.StatusOpen, model.StatusActive, model.StatusBlocked} {
		if err = store.MoveTask(w, dep, status); err != nil {
			t.Fatal(err)
		}
		if err = a.AdmitLease(w.ID, "p", task.ID); err == nil {
			t.Fatalf("active task admitted with %s dependency", status)
		}
	}
	if err = store.MoveTask(w, dep, model.StatusDone); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(task.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.AdmitLease(w.ID, "p", task.ID); err != nil {
		t.Fatal("ready active task denied", err)
	}
	after, err := os.ReadFile(task.Path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("admission changed canonical task", err)
	}
	tasks, err := store.ListTasks(w, "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range store.ReadyFrontierForProject(tasks, "p").Ready {
		if candidate.ID == task.ID {
			t.Fatal("active task leaked into scheduler frontier")
		}
	}
	for _, input := range [][3]string{{"other", "p", task.ID}, {w.ID, "other", task.ID}, {w.ID, "p", "missing"}} {
		if err = a.AdmitLease(input[0], input[1], input[2]); err == nil {
			t.Fatal("active task admitted with wrong binding")
		}
	}
	for _, status := range []model.Status{model.StatusBlocked, model.StatusDone} {
		if err = store.MoveTask(w, task, status); err != nil {
			t.Fatal(err)
		}
		if err = a.AdmitLease(w.ID, "p", task.ID); err == nil {
			t.Fatalf("%s task admitted", status)
		}
	}
}

func TestLeaseAdmissionActiveTaskRetainsExclusions(t *testing.T) {
	for _, excluded := range []string{"wont", "aggregate", "duplicate"} {
		t.Run(excluded, func(t *testing.T) {
			w, _ := readFixture(t)
			if _, err := store.CreateProject(w, "root", "p", "p", "goal", ""); err != nil {
				t.Fatal(err)
			}
			task, err := store.CreateTask(w, "root", "p", "Candidate", store.TaskOpts{})
			if err != nil {
				t.Fatal(err)
			}
			if err = store.MoveTask(w, task, model.StatusActive); err != nil {
				t.Fatal(err)
			}
			switch excluded {
			case "wont":
				task.Doc.Front.Set("priority", "wont")
			case "aggregate":
				task.Doc.Front.Set("task_kind", store.TaskKindAggregate)
			case "duplicate":
				data, readErr := os.ReadFile(task.Path)
				if readErr != nil {
					t.Fatal(readErr)
				}
				path := filepath.Join(w.TasksDir("p", model.StatusOpen), filepath.Base(task.Path))
				if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err = store.SaveTask(task); err != nil {
				t.Fatal(err)
			}
			a, err := NewLeaseAdmission(w)
			if err != nil {
				t.Fatal(err)
			}
			if err = a.AdmitLease(w.ID, "p", task.ID); err == nil {
				t.Fatalf("%s active task admitted", excluded)
			}
		})
	}
}

func TestLeaseAdmissionFreshWorkspaceGraph(t *testing.T) {
	w, _ := readFixture(t)
	for _, p := range []string{"p", "q"} {
		if _, err := store.CreateProject(w, "root", p, p, "goal", ""); err != nil {
			t.Fatal(err)
		}
	}
	dep, err := store.CreateTask(w, "root", "q", "Dependency", store.TaskOpts{})
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(w, "root", "p", "Candidate", store.TaskOpts{DependsOn: []string{dep.ID}})
	if err != nil {
		t.Fatal(err)
	}
	a, err := NewLeaseAdmission(w)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.AdmitLease(w.ID, "p", task.ID); err == nil {
		t.Fatal("unfinished cross-project dependency admitted")
	}
	if err = store.MoveTask(w, dep, model.StatusDone); err != nil {
		t.Fatal(err)
	}
	if err = a.AdmitLease(w.ID, "p", task.ID); err != nil {
		t.Fatal("complete graph denied", err)
	}
	for _, input := range [][3]string{{"other", "p", task.ID}, {w.ID, "q", task.ID}, {w.ID, "p", "missing"}} {
		if err = a.AdmitLease(input[0], input[1], input[2]); err == nil {
			t.Fatal("wrong binding admitted")
		}
	}
	copyPath := filepath.Join(w.TasksDir("p", model.StatusActive), filepath.Base(task.Path))
	if err = os.MkdirAll(filepath.Dir(copyPath), 0700); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(task.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(copyPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err = a.AdmitLease(w.ID, "p", task.ID); err == nil {
		t.Fatal("ambiguous duplicate admitted")
	}
	if err = os.Remove(copyPath); err != nil {
		t.Fatal(err)
	}
	if err = store.MoveTask(w, dep, model.StatusOpen); err != nil {
		t.Fatal(err)
	}
	if err = a.AdmitLease(w.ID, "p", task.ID); err == nil {
		t.Fatal("stale readiness admitted")
	}
	if err = os.Remove(dep.Path); err != nil {
		t.Fatal(err)
	}
	if err = a.AdmitLease(w.ID, "p", task.ID); err == nil {
		t.Fatal("missing dependency admitted")
	}
}
