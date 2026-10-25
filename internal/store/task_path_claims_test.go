package store

import (
	"os"
	"slices"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/workspace"
)

func TestApplyTaskPathClaimsRefusesStalePlanAndWritesCurrentClaims(t *testing.T) {
	w, err := workspace.Init(t.TempDir(), "claims-test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CreateProject(w, "a-root", "Core", "core", "goal", ""); err != nil {
		t.Fatal(err)
	}
	task, err := CreateTask(w, "a-root", "core", "Set native task claims", TaskOpts{Accept: []string{"scoped claims are recorded"}})
	if err != nil {
		t.Fatal(err)
	}
	task.Doc.Front.SetList("claims", []string{"src"})
	if err := SaveTask(task); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanTaskPathClaims(w, task, []string{"src", "tests"}, time.Unix(10, 0))
	if err != nil {
		t.Fatal(err)
	}

	staleTask, err := FindTask(w, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	staleTask.Doc.SetSection("Notes", "changed after preview")
	if err := SaveTask(staleTask); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyTaskPathClaims(w, staleTask, plan, time.Unix(11, 0)); err == nil {
		t.Fatal("stale task bytes were accepted")
	}

	fresh, err := FindTask(w, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	plan, err = PlanTaskPathClaims(w, fresh, []string{"src", "tests"}, time.Unix(12, 0))
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(fresh.Path)
	if err != nil {
		t.Fatal(err)
	}
	applied, err := ApplyTaskPathClaims(w, fresh, plan, time.Unix(13, 0))
	if err != nil {
		t.Fatal(err)
	}
	updated, err := FindTask(w, task.ID)
	if err != nil || !slices.Equal(updated.Claims(), []string{"src", "tests"}) || applied.AppliedAt.IsZero() {
		t.Fatalf("updated claims=%v plan=%+v err=%v", updated.Claims(), applied, err)
	}
	if string(before) == string(mustReadTask(t, updated.Path)) {
		t.Fatal("apply did not change task bytes")
	}
}

func mustReadTask(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestPlanTaskPathClaimsRejectsWorkspaceMetadataAndDuplicateInput(t *testing.T) {
	w, err := workspace.Init(t.TempDir(), "claims-invalid")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CreateProject(w, "a-root", "Core", "core", "goal", ""); err != nil {
		t.Fatal(err)
	}
	task, err := CreateTask(w, "a-root", "core", "Reject unsafe task claims", TaskOpts{})
	if err != nil {
		t.Fatal(err)
	}
	for _, claims := range [][]string{{"../outside"}, {".dacli/runs"}, {"src", "src"}} {
		if _, err := PlanTaskPathClaims(w, task, claims, time.Unix(20, 0)); err == nil {
			t.Errorf("PlanTaskPathClaims accepted %q", claims)
		}
	}
}
