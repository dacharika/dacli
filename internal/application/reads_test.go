package application

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/eventlog"
	"github.com/mlnomadpy/dacli/internal/governance/contracts"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

func readFixture(t *testing.T) (*workspace.Workspace, contracts.Bundle) {
	t.Helper()
	w, err := workspace.Init(t.TempDir(), "read-test")
	if err != nil {
		t.Fatal(err)
	}
	rec := func(id string) contracts.Record {
		return contracts.Record{Schema: contracts.Version, ID: id, Workspace: w.ID}
	}
	b := contracts.Bundle{Schema: contracts.Version, Workspace: w.ID,
		Humans:        []contracts.Human{{Record: rec("human"), Credential: "verified-by-adapter"}},
		Installations: []contracts.Installation{{Record: rec("install"), Human: "human"}},
		Authorities:   []contracts.Authority{{Record: rec("authority"), Installation: "install", Epoch: 1}},
		Capabilities:  []contracts.HostCapability{{Record: rec("cap"), Installation: "install", Host: "fixture", Assurance: "observed", Actions: []string{"completion"}, Enforcement: map[string]string{"completion": "cooperative"}, Cancellation: "unsupported"}},
		Sessions:      []contracts.Session{{Record: rec("session"), Kind: "host-session", Parent: "install", Human: "human", Capability: "cap", Delegation: "explicit"}},
	}
	if err := b.Validate(); err != nil {
		t.Fatal(err)
	}
	return w, b
}

func TestReadAuthorityBindings(t *testing.T) {
	w, b := readFixture(t)
	now := time.Now()
	other := *w
	other.ID = "different-workspace"
	if _, err := NewRequestContext(&other, "human", "session", b, now); clikit.ExitCode(err) != 3 {
		t.Fatalf("valid authority from another workspace: %v", err)
	}

	for _, tc := range []struct {
		name, principal, session string
		mutate                   func(*contracts.Bundle)
	}{
		{"missing principal", "", "session", nil},
		{"missing session", "human", "", nil},
		{"wrong principal", "other", "session", nil},
		{"unknown session", "human", "other", nil},
		{"missing authority", "human", "session", func(b *contracts.Bundle) { b.Authorities = nil }},
		{"wrong workspace", "human", "session", func(b *contracts.Bundle) { b.Workspace = "other" }},
		{"revoked", "human", "session", func(b *contracts.Bundle) {
			b.Revocations = []contracts.Revocation{{Record: contracts.Record{Schema: contracts.Version, ID: "revocation", Workspace: w.ID}, Session: "session", RevokedAt: now}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copy := b
			if tc.mutate != nil {
				tc.mutate(&copy)
			}
			_, err := NewRequestContext(w, tc.principal, tc.session, copy, now)
			if clikit.ExitCode(err) != 3 {
				t.Fatalf("expected refusal, got %v", err)
			}
		})
	}
	for _, op := range []func() error{
		func() error { _, err := Status(RequestContext{}, ""); return err },
		func() error { _, err := ListTasks(RequestContext{}, "", ""); return err },
		func() error { _, err := GetTask(RequestContext{}, "001"); return err },
	} {
		if err := op(); clikit.ExitCode(err) != 3 {
			t.Fatalf("unchecked context: %v", err)
		}
	}
}

func TestReadsUseExplicitContext(t *testing.T) {
	w, b := readFixture(t)
	if _, err := store.CreateProject(w, "a-root", "Project", "p", "goal", ""); err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(w, "a-root", "p", "Read explicitly", store.TaskOpts{})
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewRequestContext(w, "human", "session", b, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// A poisoned ambient identity must not influence any service operation.
	t.Setenv("DACLI_AGENT", "invalid-for-every-workspace")
	status, err := Status(r, "p")
	if err != nil || len(status.Projects) != 1 || status.Projects[0].Open != 1 {
		t.Fatalf("status: %+v, %v", status, err)
	}
	tasks, err := ListTasks(r, "p", "")
	if err != nil || len(tasks) != 1 || tasks[0].ID != task.ID {
		t.Fatalf("list: %v, %v", tasks, err)
	}
	got, err := GetTask(r, task.ID)
	if err != nil || got.ID != task.ID {
		t.Fatalf("get: %v, %v", got, err)
	}
}

func TestStatusRefusesUnreadableTaskStatusPath(t *testing.T) {
	w, b := readFixture(t)
	if _, err := store.CreateProject(w, "a-root", "Project", "p", "goal", ""); err != nil {
		t.Fatal(err)
	}
	r, err := NewRequestContext(w, "human", "session", b, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	path := w.TasksDir("p", model.StatusOpen)
	if err = os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err = os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte("existing non-directory"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ListTasks(w, "p", ""); err == nil {
		t.Fatal("fixture did not produce a task read error")
	}
	status, err := Status(r, "p")
	if err == nil {
		t.Fatalf("unreadable backlog reported successful status: %+v", status)
	}
	if status.Schema != "" {
		t.Fatalf("failed status returned a complete schema: %+v", status)
	}
}

func TestStatusRefusesPendingEventHoles(t *testing.T) {
	w, b := readFixture(t)
	r, err := NewRequestContext(w, "human", "session", b, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(w.EventsDir(), 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(w.EventsDir(), "unreadable-private-event.md")
	if err = os.Symlink(filepath.Join(t.TempDir(), "missing"), path); err != nil {
		t.Fatal(err)
	}
	_, holes, err := eventlog.ListReport(w, eventlog.Query{Pending: true})
	if err != nil || len(holes) != 1 || holes[0] != path {
		t.Fatalf("fixture did not produce exactly one event hole: %v %v", holes, err)
	}
	status, err := Status(r, "")
	if err == nil {
		t.Fatalf("incomplete event log reported successful status: %+v", status)
	}
	if status.Schema != "" {
		t.Fatalf("failed status returned a complete schema: %+v", status)
	}
}

func TestStatusSuccessfulJSONUnchanged(t *testing.T) {
	w, b := readFixture(t)
	r, err := NewRequestContext(w, "human", "session", b, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	check := func(project, want string) {
		t.Helper()
		value, err := Status(r, project)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if string(raw) != want {
			t.Fatalf("status JSON changed: %s", raw)
		}
	}
	check("", `{"schema":"workspace-status/v1","projects":null,"pending_events":0}`)
	if _, err = store.CreateProject(w, "a-root", "Project", "p", "goal", ""); err != nil {
		t.Fatal(err)
	}
	if _, err = store.CreateTask(w, "a-root", "p", "Open task", store.TaskOpts{}); err != nil {
		t.Fatal(err)
	}
	if _, err = eventlog.Append(w, "a-root", model.EventComment, "", "agent", "public fixture"); err != nil {
		t.Fatal(err)
	}
	check("p", `{"schema":"workspace-status/v1","project_filter":"p","projects":[{"slug":"p","title":"Project","open":1,"active":0,"blocked":0,"done":0,"implemented_unlanded":0}],"pending_events":1}`)
}
