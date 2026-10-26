package native

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/application"
	"github.com/mlnomadpy/dacli/internal/store"
)

func TestNativeReadyFrontierWorkspaceGrantMayFilterProjectAndTask(t *testing.T) {
	f := setup(t)
	other, err := store.CreateProject(mustWorkspace(t, f), "root", "Other", "other", "other goal", "")
	if err != nil {
		t.Fatal(err)
	}
	otherTask, err := store.CreateTask(mustWorkspace(t, f), "root", other.Slug, "Other task", store.TaskOpts{})
	if err != nil {
		t.Fatal(err)
	}
	workspaceScoped := scoped(t, f, "workspace:"+f.c.WorkspaceID)

	projectFrontier := call(t, workspaceScoped, "ready_frontier", frontierArgs{version{1}, "p", "", 10}).(application.NativeReadyFrontier)
	if projectFrontier.Schema != "native-ready-frontier/v1" || projectFrontier.Scope != "project" || projectFrontier.Project != "p" || !projectFrontier.DiagnosticOnly {
		t.Fatalf("workspace project filter metadata mismatch: %+v", projectFrontier)
	}
	assertNativeWireParity(t, "ready_frontier", projectFrontier)
	for _, item := range append(append([]application.NativeTaskItem{}, projectFrontier.Ready...), projectFrontier.Blocked...) {
		if item.Project != "p" || item.ID == otherTask.ID {
			t.Fatalf("workspace project filter disclosed another project: %+v", item)
		}
	}

	taskFrontier := call(t, workspaceScoped, "ready_frontier", frontierArgs{version{1}, "", f.task.ID, 10}).(application.NativeReadyFrontier)
	if taskFrontier.Scope != "task" || taskFrontier.Task != f.task.ID || len(taskFrontier.Ready) != 1 || taskFrontier.Ready[0].ID != f.task.ID {
		t.Fatalf("workspace task filter did not return only the selected task: %+v", taskFrontier)
	}
	for _, item := range append(append([]application.NativeTaskItem{}, taskFrontier.Ready...), taskFrontier.Blocked...) {
		if item.ID != f.task.ID || item.ID == otherTask.ID {
			t.Fatalf("workspace task filter disclosed another task: %+v", item)
		}
	}
}

func TestNativeReadyFrontierNarrowScopesAndFreshAuthorizationRemainEnforced(t *testing.T) {
	f := setup(t)
	_, err := store.CreateProject(mustWorkspace(t, f), "root", "Other", "other", "other goal", "")
	if err != nil {
		t.Fatal(err)
	}

	projectScoped := scoped(t, f, "project:p")
	if _, err = projectScoped.s.call("ready_frontier", mustJSON(frontierArgs{version{1}, "p", "", 10})); err != nil {
		t.Fatalf("project-scoped frontier denied its own project: %v", err)
	}
	for _, args := range []frontierArgs{
		{version{1}, "other", "", 10},
		{version{1}, "", "", 10},
	} {
		if _, err = projectScoped.s.call("ready_frontier", mustJSON(args)); err == nil {
			t.Fatalf("project-scoped session read outside its filter: %+v", args)
		}
	}

	taskFixture := setup(t)
	sibling, err := store.CreateTask(mustWorkspace(t, taskFixture), "root", "p", "Sibling task", store.TaskOpts{})
	if err != nil {
		t.Fatal(err)
	}
	taskScoped := scoped(t, taskFixture, "task:"+taskFixture.task.ID)
	if _, err = taskScoped.s.call("ready_frontier", mustJSON(frontierArgs{version{1}, "", taskFixture.task.ID, 10})); err != nil {
		t.Fatalf("task-scoped frontier denied its own task: %v", err)
	}
	for _, args := range []frontierArgs{
		{version{1}, "", sibling.ID, 10},
		{version{1}, "p", "", 10},
	} {
		if _, err = taskScoped.s.call("ready_frontier", mustJSON(args)); err == nil {
			t.Fatalf("task-scoped session read outside its filter: %+v", args)
		}
	}
	if err = taskScoped.a.Revoke(taskScoped.owner, taskScoped.c.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err = taskScoped.s.call("ready_frontier", mustJSON(frontierArgs{version{1}, "", taskFixture.task.ID, 10})); err == nil {
		t.Fatal("revoked task-scoped session read a frontier")
	}

	expired := setup(t)
	expired = scoped(t, expired, "workspace:"+expired.c.WorkspaceID)
	data, err := os.ReadFile(expired.c.Authority)
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	if err = json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	state["Sessions"].(map[string]any)[expired.c.SessionID].(map[string]any)["ExpiresAt"] = time.Now().Add(-time.Hour).Format(time.RFC3339Nano)
	data, err = json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(expired.c.Authority, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = expired.s.call("ready_frontier", mustJSON(frontierArgs{version{1}, "p", "", 10})); err == nil {
		t.Fatal("expired workspace-scoped session read a filtered frontier")
	}
	if _, err = expired.s.call("task_brief", mustJSON(briefArgs{version{1}, expired.task.ID, 1000})); err == nil {
		t.Fatal("expired workspace-scoped session read a task brief")
	}
}
