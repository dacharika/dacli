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
