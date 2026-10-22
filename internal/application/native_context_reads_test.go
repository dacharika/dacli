package application

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/eventlog"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/mdstore"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

type nativeContextReadFixture struct {
	w     *workspace.Workspace
	a     *sessions.Authority
	owner string
	task  *store.Task
}

func newNativeContextReadFixture(t *testing.T) nativeContextReadFixture {
	t.Helper()
	w, err := workspace.Init(t.TempDir(), "native-context-read-test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.CreateProject(w, "root", "Project", "p", "goal", ""); err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(w, "root", "p", "Implement own task", store.TaskOpts{Accept: []string{"OWN_ACCEPTANCE_MARKER"}, Context: "OWN_CONTEXT_MARKER"})
	if err != nil {
		t.Fatal(err)
	}
	a, err := sessions.Open(filepath.Join(t.TempDir(), "authority.json"), sessions.Binding{Workspace: w.ID, Installation: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := a.Bootstrap()
	if err != nil {
		t.Fatal(err)
	}
	return nativeContextReadFixture{w: w, a: a, owner: owner, task: task}
}

func (f nativeContextReadFixture) context(t *testing.T, resource string) RequestContext {
	t.Helper()
	session, token, err := f.a.Enroll(f.owner, sessions.Scope{Actions: []string{"read"}, Resources: []string{resource}}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewNativeReadContext(f.w, f.a, token, session.ID, resource)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func nativeBriefText(b NativeTaskBrief) string {
	var out strings.Builder
	for _, section := range b.Sections {
		out.WriteString(section.Content)
	}
	return out.String()
}

func TestNativeTaskBriefNarrowScopeRetainsOwnAcceptanceAndContext(t *testing.T) {
	f := newNativeContextReadFixture(t)
	b, err := TaskBrief(f.context(t, "task:"+f.task.ID), f.task.ID, 4000)
	if err != nil {
		t.Fatal(err)
	}
	text := nativeBriefText(b)
	for _, want := range []string{"OWN_ACCEPTANCE_MARKER", "OWN_CONTEXT_MARKER"} {
		if !strings.Contains(text, want) {
			t.Fatalf("authorized task context missing %q: sections=%d", want, len(b.Sections))
		}
	}
	if len(b.SourceRefs) != 1 || b.SourceRefs[0].ID != f.task.ID || b.SourceRefs[0].Kind != "task" {
		t.Fatalf("task-only source refs: %+v", b.SourceRefs)
	}
	if len(b.Omissions) != 1 || !strings.Contains(b.Omissions[0], "broader") {
		t.Fatalf("task-only context omission: %q", b.Omissions)
	}
}

func TestNativeTaskBriefSourcesFollowSelectedRecords(t *testing.T) {
	f := newNativeContextReadFixture(t)
	event, err := eventlog.Append(f.w, "reviewer", model.EventFinding, f.task.ID, "", "MULTILINE_FIRST\nMULTILINE_SECOND")
	if err != nil {
		t.Fatal(err)
	}
	b, err := TaskBrief(f.context(t, "workspace:"+f.w.ID), f.task.ID, 10000)
	if err != nil {
		t.Fatal(err)
	}
	text := nativeBriefText(b)
	if !strings.Contains(text, "MULTILINE_FIRST") || !strings.Contains(text, "MULTILINE_SECOND") {
		t.Fatal("fixture did not retain multiline finding")
	}
	if !hasNativeSource(b.SourceRefs, "event", event.ID) {
		t.Fatalf("retained multiline finding has no source ref: %+v", b.SourceRefs)
	}

	f = newNativeContextReadFixture(t)
	for i := 0; i < 8; i++ {
		if _, err = eventlog.Append(f.w, "same-reviewer", model.EventFinding, f.task.ID, "", "REPEATED_FINDING"); err != nil {
			t.Fatal(err)
		}
	}
	b, err = TaskBrief(f.context(t, "workspace:"+f.w.ID), f.task.ID, 10000)
	if err != nil {
		t.Fatal(err)
	}
	eventRefs := 0
	for _, source := range b.SourceRefs {
		if source.Kind == "event" {
			eventRefs++
		}
	}
	if eventRefs != 7 {
		t.Fatalf("source refs for finding cap: got %d, want 7; omissions=%q", eventRefs, b.Omissions)
	}

	f = newNativeContextReadFixture(t)
	largeEvent, err := eventlog.Append(f.w, "reviewer", model.EventFinding, f.task.ID, "", strings.Repeat("BUDGET_DROPPED_FINDING ", 500))
	if err != nil {
		t.Fatal(err)
	}
	b, err = TaskBrief(f.context(t, "workspace:"+f.w.ID), f.task.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	if hasNativeSource(b.SourceRefs, "event", largeEvent.ID) {
		t.Fatalf("budget-dropped event retained a source ref: omissions=%q", b.Omissions)
	}
}

func TestNativeTaskBriefOmittedMarkerIsNotProvenance(t *testing.T) {
	f := newNativeContextReadFixture(t)
	for i := 0; i < 7; i++ {
		if _, err := store.CreateNote(f.w, "root", "p", model.NoteDecision, fmt.Sprintf("keep %d", i), store.NoteOpts{Rejected: "other", Body: "Chosen"}); err != nil {
			t.Fatal(err)
		}
	}
	path, err := store.CreateNote(f.w, "root", "p", model.NoteDecision, "old omitted source", store.NoteOpts{Rejected: "other", Body: "OMITTED_DECISION_BODY"})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := mdstore.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := doc.Front.Get("id")
	doc.Front.Set("created", "2000-01-01T00:00:00Z")
	if err := mdstore.WriteFile(path, doc); err != nil {
		t.Fatal(err)
	}
	selected, err := store.CreateTask(f.w, "root", "p", "Task linking old decision", store.TaskOpts{Context: "Mention [[" + id + "]] without including its decision"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := TaskBrief(f.context(t, "workspace:"+f.w.ID), selected.ID, 10000)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(nativeBriefText(b), "OMITTED_DECISION_BODY") {
		t.Fatal("fixture retained supposedly omitted decision")
	}
	if hasNativeSource(b.SourceRefs, "decision", id) {
		t.Fatalf("omitted decision received a source ref from task prose: %+v; omissions=%q", b.SourceRefs, b.Omissions)
	}
}

func TestNarrowGetTaskErrorsDoNotLeakForeignNames(t *testing.T) {
	f := newNativeContextReadFixture(t)
	if _, err := store.CreateProject(f.w, "root", "Foreign", "secret-project", "goal", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateTask(f.w, "root", "secret-project", "SECRET_TASK_TITLE", store.TaskOpts{}); err != nil {
		t.Fatal(err)
	}
	got, err := GetTask(f.context(t, "project:p"), "001")
	if err != nil && (strings.Contains(err.Error(), "secret-project") || strings.Contains(err.Error(), "SECRET_TASK_TITLE")) {
		t.Fatalf("narrow application error leaked foreign names: %v", err)
	}
	if err == nil && got.ID != f.task.ID {
		t.Fatalf("project scope resolved outside its own task set: %+v", got)
	}
}

func TestTaskAliasGrantCannotReadCanonicalFrontier(t *testing.T) {
	f := newNativeContextReadFixture(t)
	_, err := ReadyFrontier(f.context(t, "task:001"), "", "001", 10)
	if err == nil {
		t.Fatal("noncanonical task grant authorized canonical task frontier")
	}
}

func TestWorkspaceStatusMissingProjectStillRefuses(t *testing.T) {
	f := newNativeContextReadFixture(t)
	if _, err := Status(f.context(t, "workspace:"+f.w.ID), "missing-project"); err == nil {
		t.Fatal("workspace status succeeded for a nonexistent project")
	}
}

func TestNarrowReadFailuresDoNotLeakForeignPaths(t *testing.T) {
	f := newNativeContextReadFixture(t)
	if _, err := store.CreateProject(f.w, "root", "Secret project", "secret-project", "goal", ""); err != nil {
		t.Fatal(err)
	}
	foreignOpen := f.w.TasksDir("secret-project", model.StatusOpen)
	if err := os.RemoveAll(foreignOpen); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(foreignOpen), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(foreignOpen, []byte("broken foreign task directory"), 0600); err != nil {
		t.Fatal(err)
	}

	projectRead := f.context(t, "project:p")
	frontier, err := ReadyFrontier(projectRead, "p", "", 10)
	if clikit.ExitCode(err) != 3 || strings.Contains(err.Error(), "secret-project") || strings.Contains(err.Error(), f.w.Root) {
		t.Fatalf("narrow readiness error leaked foreign path: frontier=%+v err=%v", frontier, err)
	}
	if status, err := Status(projectRead, "p"); err != nil || len(status.Projects) != 1 || status.Projects[0].Slug != "p" {
		t.Fatalf("project status scanned or exposed foreign failure: %+v %v", status, err)
	}
	if tasks, err := ListTasks(projectRead, "p", ""); err != nil || len(tasks) != 1 || tasks[0].ID != f.task.ID {
		t.Fatalf("project task list scanned or exposed foreign failure: %v %v", tasks, err)
	}
	event, err := eventlog.Append(f.w, "reviewer", model.EventFinding, f.task.ID, "", "corrupt event fixture")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(event.Path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(f.w.Root, "missing-event-target"), event.Path); err != nil {
		t.Fatal(err)
	}
	if _, err := Status(projectRead, "p"); err == nil || strings.Contains(err.Error(), event.Path) || strings.Contains(err.Error(), f.w.Root) {
		t.Fatalf("project status event-log error leaked a workspace path: %v", err)
	}

	workspaceRead := f.context(t, "workspace:"+f.w.ID)
	_, err = ReadyFrontier(workspaceRead, "", "", 10)
	if err == nil || !strings.Contains(err.Error(), foreignOpen) {
		t.Fatalf("workspace read should preserve its diagnostic context: %v", err)
	}

	ownOpen := f.w.TasksDir("p", model.StatusOpen)
	if err := os.RemoveAll(ownOpen); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(ownOpen), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ownOpen, []byte("broken authorized task directory"), 0600); err != nil {
		t.Fatal(err)
	}
	_, statusErr := Status(projectRead, "p")
	if clikit.ExitCode(statusErr) != 3 || strings.Contains(statusErr.Error(), f.w.Root) {
		t.Fatalf("project status error leaked storage path: %v", statusErr)
	}
	if _, err := ListTasks(projectRead, "p", ""); clikit.ExitCode(err) != 3 || strings.Contains(err.Error(), f.w.Root) {
		t.Fatalf("project task list error leaked storage path: %v", err)
	}
}

func hasNativeSource(sources []NativeSourceRef, kind, id string) bool {
	for _, source := range sources {
		if source.Kind == kind && source.ID == id {
			return true
		}
	}
	return false
}
