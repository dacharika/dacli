package native

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/application"
	"github.com/mlnomadpy/dacli/internal/eventlog"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

func scoped(t *testing.T, f fixture, resources ...string) fixture {
	t.Helper()
	principal, token, err := f.a.Enroll(f.owner, sessions.Scope{Actions: []string{"read"}, Resources: resources}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	f.c.SessionID = principal.ID
	save(t, f.s.path, f.c)
	if err := os.WriteFile(f.c.CredentialFile, []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	f.s, err = Open(f.s.path)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestNativeTaskBriefWorkspaceAndNarrowScopeOmissions(t *testing.T) {
	f := setup(t)
	if _, err := store.CreateNote(mustWorkspace(t, f), "root", "p", model.NoteDecision, "decision source", store.NoteOpts{Rejected: "discarded option", Body: "Chosen option"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateNote(mustWorkspace(t, f), "root", "p", model.NoteFinding, "finding source", store.NoteOpts{Severity: "major", Body: "verified source"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GradeFinding(mustWorkspace(t, f), "p", "finding source", "confirmed"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		if _, err := store.CreateNote(mustWorkspace(t, f), "root", "p", model.NoteFinding, fmt.Sprintf("finding %02d", i), store.NoteOpts{Severity: "minor", Body: fmt.Sprintf("finding body %02d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	other, err := store.CreateProject(mustWorkspace(t, f), "root", "Other", "other", "other goal", "")
	if err != nil {
		t.Fatal(err)
	}
	otherTask, err := store.CreateTask(mustWorkspace(t, f), "root", other.Slug, "Other task", store.TaskOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eventlog.Append(mustWorkspace(t, f), "root", model.EventFinding, otherTask.ID, "", "CROSS_PROJECT_SECRET"); err != nil {
		t.Fatal(err)
	}

	full := call(t, f, "task_brief", briefArgs{version{1}, f.task.ID, 10000}).(application.NativeTaskBrief)
	if full.Schema != "native-task-brief/v1" || full.TaskID != f.task.ID || full.ObservedAt.IsZero() || full.TrustNotice == "" {
		t.Fatalf("incomplete brief metadata: %+v", full)
	}
	decision, finding := false, false
	for _, section := range full.Sections {
		if strings.Contains(section.Content, "CROSS_PROJECT_SECRET") {
			t.Fatal("cross-project event entered the task brief")
		}
	}
	for _, source := range full.SourceRefs {
		decision = decision || source.Kind == "decision" && source.Trust == "recorded-decision"
		if source.Kind == "finding" && source.Trust == "confirmed" {
			finding = true
			if info, err := os.Stat(filepath.Join(mustWorkspace(t, f).Root, filepath.FromSlash(source.Path))); err != nil || info.IsDir() {
				t.Fatalf("source ref is not an exact note file: %q %v", source.Path, err)
			}
		}
	}
	if !decision || !finding || len(full.SourceRefs) > 9 {
		t.Fatalf("brief source trust or project boundary failed: decision=%v finding=%v", decision, finding)
	}
	if len(full.Omissions) == 0 || !strings.Contains(strings.Join(full.Omissions, " "), "findings beyond") {
		t.Fatalf("brief omitted findings without explicit budget accounting: %v", full.Omissions)
	}
	assertNativeWireParity(t, "task_brief", full)

	narrow := scoped(t, f, "task:"+f.task.ID)
	short := call(t, narrow, "task_brief", briefArgs{version{1}, f.task.ID, 10000}).(application.NativeTaskBrief)
	if len(short.Sections) != 1 || !strings.Contains(short.Sections[0].Title, f.task.Title) || len(short.Omissions) != 1 || !strings.Contains(strings.Join(short.Omissions, " "), "broader") {
		t.Fatalf("narrow brief did not retain exact-task context and report broader omission: %+v", short)
	}
	encoded, _ := json.Marshal(short)
	if strings.Contains(string(encoded), "CROSS_PROJECT_SECRET") {
		t.Fatal("narrow task scope disclosed cross-project event data")
	}
}

func TestNativeReadyFrontierIsScopedDiagnosticAndReportsOmissions(t *testing.T) {
	f := setup(t)
	second, err := store.CreateTask(mustWorkspace(t, f), "root", "p", "Second ready", store.TaskOpts{})
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.CreateProject(mustWorkspace(t, f), "root", "Other", "other", "other goal", "")
	if err != nil {
		t.Fatal(err)
	}
	otherTask, err := store.CreateTask(mustWorkspace(t, f), "root", other.Slug, "Other ready", store.TaskOpts{})
	if err != nil {
		t.Fatal(err)
	}

	projectScoped := scoped(t, f, "project:p")
	frontier := call(t, projectScoped, "ready_frontier", frontierArgs{version{1}, "p", "", 1}).(application.NativeReadyFrontier)
	if frontier.Schema != "native-ready-frontier/v1" || frontier.Scope != "project" || !frontier.DiagnosticOnly || len(frontier.Ready) != 1 || frontier.OmittedCount != 1 || len(frontier.OmittedTaskIDs) != 1 {
		t.Fatalf("frontier omitted/diagnostic contract failed: %+v", frontier)
	}
	assertNativeWireParity(t, "ready_frontier", frontier)
	for _, item := range frontier.Ready {
		if item.ID == otherTask.ID {
			t.Fatal("project frontier disclosed another project's task")
		}
	}
	if _, err := projectScoped.s.call("ready_frontier", mustJSON(frontierArgs{version{1}, "", "", 1})); err == nil {
		t.Fatal("project scope read workspace frontier")
	}

	taskScoped := scoped(t, f, "task:"+second.ID)
	taskFrontier := call(t, taskScoped, "ready_frontier", frontierArgs{version{1}, "", second.ID, 10}).(application.NativeReadyFrontier)
	if taskFrontier.Scope != "task" || len(taskFrontier.Ready) != 1 || taskFrontier.Ready[0].ID != second.ID {
		t.Fatalf("task scope escaped exact task: %+v", taskFrontier)
	}
	if _, err := taskScoped.s.call("ready_frontier", mustJSON(frontierArgs{version{1}, "", f.task.ID, 10})); err == nil {
		t.Fatal("task scope read a sibling task")
	}
}

func TestNativeContextReadInputSchemasAreStrictAndVersioned(t *testing.T) {
	f := setup(t)
	for _, tc := range []struct{ name, tool, raw string }{
		{"brief unknown field", "task_brief", `{"schema_version":1,"task":"` + f.task.ID + `","token":"private"}`},
		{"brief wrong version", "task_brief", `{"schema_version":2,"task":"` + f.task.ID + `"}`},
		{"brief negative budget", "task_brief", `{"schema_version":1,"task":"` + f.task.ID + `","budget_tokens":-1}`},
		{"frontier unknown field", "ready_frontier", `{"schema_version":1,"project":"p","limit":1,"reserve":true}`},
		{"frontier wrong version", "ready_frontier", `{"schema_version":2,"project":"p","task":"","limit":1}`},
		{"frontier dual scope", "ready_frontier", `{"schema_version":1,"project":"p","task":"` + f.task.ID + `","limit":1}`},
		{"frontier invalid limit", "ready_frontier", `{"schema_version":1,"project":"p","task":"","limit":101}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := f.s.call(tc.tool, []byte(tc.raw)); err == nil {
				t.Fatal("malformed or unversioned input accepted")
			}
		})
	}
}

func mustWorkspace(t *testing.T, f fixture) *workspace.Workspace {
	t.Helper()
	w, err := workspace.Find(f.c.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func assertNativeWireParity(t *testing.T, name string, data any) {
	t.Helper()
	r := result(name, data, nil).(map[string]any)
	structured, err := json.Marshal(r["structuredContent"])
	if err != nil {
		t.Fatal(err)
	}
	textFallback := r["content"].([]any)[0].(map[string]any)["text"].(string)
	if string(structured) != textFallback || !validJSON(outputSchema(name), structured) {
		t.Fatalf("%s text/structured output parity failed", name)
	}
}
