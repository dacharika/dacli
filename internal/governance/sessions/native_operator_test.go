package sessions

import (
	"bytes"
	"os"
	"testing"
	"time"
)

func TestNativeOperatorRequiresDistinctActionAndProjectScope(t *testing.T) {
	f := nativeReviewSetup(t)
	actor, token := dashboardReader(t, f.a, f.owner, "task-a")
	_, err := f.a.ReadOperatorDashboard(token, actor.ID, "project", "task-a")
	denied(t, err)
	operator, secret, err := f.a.Enroll(f.owner, Scope{Actions: []string{"read", "operator.read"}, Resources: []string{"workspace:" + f.a.binding.Workspace, "project:project"}}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(f.a.path)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range []string{"", "task-a"} {
		snapshot, err := f.a.ReadOperatorDashboard(secret, operator.ID, "project", task)
		if err != nil || snapshot.Project != "project" || len(snapshot.Records) == 0 {
			t.Fatalf("project operator without per-task grants could not read: %v %+v", err, snapshot)
		}
	}
	_, err = f.a.ReadOperatorDashboard(secret, operator.ID, "foreign", "task-a")
	denied(t, err)
	// Absence from native bindings is not absence from the canonical project.
	// The operator app resolves canonical task scope before projecting; this
	// filtered authority snapshot must reveal no foreign native records.
	empty, err := f.a.ReadOperatorDashboard(secret, operator.ID, "project", "unbound-task")
	if err != nil || len(empty.Records) != 0 {
		t.Fatal("unbound task filter disclosed records", err, empty)
	}
	after, err := os.ReadFile(f.a.path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("operator reads mutated authority", err)
	}
	if err := f.a.Revoke(f.owner, operator.ID); err != nil {
		t.Fatal(err)
	}
	_, err = f.a.ReadOperatorDashboard(secret, operator.ID, "project", "")
	denied(t, err)
}

func TestNativeOperatorEmptyProjectAndExpiry(t *testing.T) {
	f := nativeReviewSetup(t)
	actor, token, err := f.a.Enroll(f.owner, Scope{Actions: []string{"read", "operator.read"}, Resources: []string{"workspace:" + f.a.binding.Workspace, "project:empty"}}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := f.a.ReadOperatorDashboard(token, actor.ID, "empty", "")
	if err != nil || snapshot.State != "ready" || len(snapshot.Records) != 0 {
		t.Fatalf("authorized empty project was not empty: %v %+v", err, snapshot)
	}
	// Use an already valid principal and a real bounded expiry, without editing
	// fixture authority internals or bypassing authentication.
	expires := time.Now().Add(80 * time.Millisecond)
	short, shortToken, err := f.a.Enroll(f.owner, actor.Scope, expires)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Until(expires) + time.Millisecond)
	_, err = f.a.ReadOperatorDashboard(shortToken, short.ID, "empty", "")
	denied(t, err)
}
