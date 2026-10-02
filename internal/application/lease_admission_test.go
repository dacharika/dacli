package application

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/store"
)

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
