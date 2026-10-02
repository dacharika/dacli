package application

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/store"
)

func TestNativeReadContexts(t *testing.T) {
	w, _ := readFixture(t)
	path := filepath.Join(t.TempDir(), "authority.json")
	a, err := sessions.Open(path, sessions.Binding{Workspace: w.ID, Installation: "installation"})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := a.Bootstrap()
	if err != nil {
		t.Fatal(err)
	}
	issue := func(scope sessions.Scope) (sessions.Session, string) {
		t.Helper()
		s, token, err := a.Enroll(owner, scope, time.Now().Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		return s, token
	}
	scope := sessions.Scope{Actions: []string{"read"}, Resources: []string{"workspace:" + w.ID}}
	s, token := issue(scope)
	independent, other := issue(scope)
	write, writeToken := issue(sessions.Scope{Actions: []string{"write"}, Resources: scope.Resources})
	narrow, narrowToken := issue(sessions.Scope{Actions: scope.Actions, Resources: []string{"task:one"}})
	wildcard, wildcardToken := issue(sessions.Scope{Actions: scope.Actions, Resources: []string{"*"}})
	wrong := *w
	wrong.ID = "another-workspace"
	foreign, err := sessions.Open(filepath.Join(t.TempDir(), "foreign.json"), sessions.Binding{Workspace: "foreign", Installation: "installation"})
	if err != nil {
		t.Fatal(err)
	}
	foreignOwner, err := foreign.Bootstrap()
	if err != nil {
		t.Fatal(err)
	}
	foreignSession, foreignToken, err := foreign.Enroll(foreignOwner, scope, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		run  func() (RequestContext, error)
	}{
		{"missing", func() (RequestContext, error) { return NewNativeContext(w, a, "", s.ID) }},
		{"invalid", func() (RequestContext, error) { return NewNativeContext(w, a, strings.Repeat("x", 64), s.ID) }},
		{"self asserted", func() (RequestContext, error) { return NewNativeContext(w, a, "codex", "human") }},
		{"independent credential", func() (RequestContext, error) { return NewNativeContext(w, a, other, s.ID) }},
		{"owner credential", func() (RequestContext, error) { return NewNativeContext(w, a, owner, s.ID) }},
		{"workspace", func() (RequestContext, error) { return NewNativeContext(&wrong, a, token, s.ID) }},
		{"foreign authority", func() (RequestContext, error) { return NewNativeContext(w, foreign, foreignToken, foreignSession.ID) }},
		{"action", func() (RequestContext, error) { return NewNativeContext(w, a, writeToken, write.ID) }},
		{"resource", func() (RequestContext, error) { return NewNativeContext(w, a, narrowToken, narrow.ID) }},
		{"wildcard", func() (RequestContext, error) { return NewNativeContext(w, a, wildcardToken, wildcard.ID) }},
		{"nil authority", func() (RequestContext, error) { return NewNativeContext(w, nil, token, s.ID) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := tc.run()
			if clikit.ExitCode(err) != 3 || r != (RequestContext{}) {
				t.Fatalf("expected empty refused context: %+v %v", r, err)
			}
			if err.Error() != "native read authority denied" {
				t.Fatalf("unredacted diagnostic: %v", err)
			}
		})
	}
	if _, err = store.CreateProject(w, "a-root", "Project", "p", "goal", ""); err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(w, "a-root", "p", "Read", store.TaskOpts{})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DACLI_AGENT", "poisoned-ambient-identity")
	for _, pair := range [][2]string{{s.ID, token}, {independent.ID, other}} {
		r, err := NewNativeContext(w, a, pair[1], pair[0])
		if err != nil {
			t.Fatal(err)
		}
		if r.principal != pair[0] || r.session != pair[0] || strings.Contains(fmt.Sprintf("%+v", r), pair[1]) {
			t.Fatal("incorrect or secret-bearing binding")
		}
		status, err := Status(r, "p")
		if err != nil || len(status.Projects) != 1 || status.Projects[0].Open != 1 {
			t.Fatalf("status: %+v %v", status, err)
		}
		tasks, err := ListTasks(r, "p", "")
		if err != nil || len(tasks) != 1 || tasks[0].ID != task.ID {
			t.Fatalf("list: %v %v", tasks, err)
		}
		got, err := GetTask(r, task.ID)
		if err != nil || got.ID != task.ID {
			t.Fatalf("get: %v %v", got, err)
		}
	}
	if err = a.Revoke(owner, s.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = NewNativeContext(w, a, token, s.ID); clikit.ExitCode(err) != 3 {
		t.Fatalf("revocation: %v", err)
	}
	if _, err = NewNativeContext(w, a, other, independent.ID); err != nil {
		t.Fatal(err)
	}
	// Change durable expiry without a scheduler-dependent sleep; the next request
	// must reload authority rather than reuse a previously authenticated snapshot.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	if err = json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	state["Sessions"].(map[string]any)[independent.ID].(map[string]any)["ExpiresAt"] = time.Now().Add(-time.Hour).Format(time.RFC3339Nano)
	data, err = json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = NewNativeContext(w, a, other, independent.ID); clikit.ExitCode(err) != 3 {
		t.Fatalf("expiry: %v", err)
	}
	if err = os.WriteFile(path, []byte("secret malformed authority"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = NewNativeContext(w, a, other, independent.ID); clikit.ExitCode(err) != 3 || err.Error() != "native read authority denied" {
		t.Fatalf("storage refusal: %v", err)
	}
}
