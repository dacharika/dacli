package dashboard

import (
	"reflect"
	"testing"

	"github.com/mlnomadpy/dacli/internal/store"
)

func TestOperatorProjectionReusesCanonicalDashboardProducers(t *testing.T) {
	w := dashboardEnv(t)
	canonicalTasks, err := buildTasks(w, "core")
	if err != nil {
		t.Fatal(err)
	}
	projected, err := OperatorProjection(w, "tasks.list", "core", "", "", 50, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	gotTasks, ok := projected.Data.(tasksResponse)
	if !ok {
		t.Fatalf("operator task result has type %T", projected.Data)
	}
	gotTasks.Generated, canonicalTasks.Generated = "", ""
	if !reflect.DeepEqual(gotTasks, canonicalTasks) {
		t.Fatal("operator task rows diverged from the dashboard's canonical builder")
	}
	if len(canonicalTasks.Tasks) == 0 {
		t.Fatal("dashboard fixture has no canonical task")
	}
	canonicalTask, err := store.FindTask(w, canonicalTasks.Tasks[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	wantDetail, err := buildTaskDetail(w, canonicalTask.ID)
	if err != nil {
		t.Fatal(err)
	}
	gotDetail, err := OperatorProjection(w, "tasks.get", "core", canonicalTask.ID, "", 50, "", "", "")
	gotTaskDetail, ok := gotDetail.Data.(taskDetailResponse)
	if err == nil && ok {
		gotTaskDetail.Generated, wantDetail.Generated = "", ""
	}
	if err != nil || !ok || !reflect.DeepEqual(gotTaskDetail, wantDetail) {
		t.Fatalf("operator task detail diverged from canonical builder: err=%v", err)
	}
	filters, err := parseEventFilters(eventFilters{Project: "core", Kind: "comment", State: "all", Range: "all", Limit: eventsDefaultLimit})
	if err != nil {
		t.Fatal(err)
	}
	wantEvents, err := buildEvents(w, filters)
	if err != nil {
		t.Fatal(err)
	}
	gotEvents, err := OperatorProjection(w, "activity.list", "core", "", "", 0, "comment", "all", "all")
	gotActivity, ok := gotEvents.Data.(eventsResponse)
	if err == nil && ok {
		gotActivity.Generated, wantEvents.Generated = "", ""
	}
	if err != nil || !ok || !reflect.DeepEqual(gotActivity, wantEvents) {
		t.Fatalf("operator activity diverged from canonical builder: err=%v", err)
	}
	if _, err := OperatorProjection(w, "shell.exec", "core", "", "", 0, "", "", ""); err == nil {
		t.Fatal("operator projection accepted an unsupported command operation")
	}
}

func TestOperatorTaskDetailDoesNotEnrichDependencyOutsideProject(t *testing.T) {
	w := dashboardEnv(t)
	for _, project := range []string{"p", "q"} {
		if _, err := store.CreateProject(w, "a-root", project, project, "goal", ""); err != nil {
			t.Fatal(err)
		}
	}
	foreign, err := store.CreateTask(w, "a-root", "q", "Q-only dependency title", store.TaskOpts{})
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(w, "a-root", "p", "P-scoped task", store.TaskOpts{DependsOn: []string{foreign.ID + ":SS"}})
	if err != nil {
		t.Fatal(err)
	}

	canonical, err := buildTaskDetail(w, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(canonical.Task.Deps) != 1 || !canonical.Task.Deps[0].Resolved || canonical.Task.Deps[0].Title != foreign.Title {
		t.Fatalf("fixture did not reproduce the global dependency enrichment: %+v", canonical.Task.Deps)
	}

	projected, err := OperatorProjection(w, "tasks.get", "p", task.ID, "", 50, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	got, ok := projected.Data.(taskDetailResponse)
	if !ok {
		t.Fatalf("operator task result has type %T", projected.Data)
	}
	if len(got.Task.Deps) != 1 {
		t.Fatalf("operator dependency list = %+v, want the canonical edge retained", got.Task.Deps)
	}
	dep := got.Task.Deps[0]
	if dep.Ref != foreign.ID || dep.Type != "SS" || dep.ID != "" || dep.Title != "" || dep.Status != "" || dep.Resolved {
		t.Fatalf("operator leaked out-of-project dependency metadata: %+v", dep)
	}
}
