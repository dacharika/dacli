package sessions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func reviewAuthority(t *testing.T) (*Authority, string, Session, string, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "authority.json")
	a, err := Open(p, Binding{Workspace: "fixture-workspace", Installation: "fixture-installation"})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := a.Bootstrap()
	if err != nil {
		t.Fatal(err)
	}
	s, token, err := a.Enroll(owner, Scope{Actions: []string{"read"}, Resources: []string{"workspace:fixture-workspace"}}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return a, owner, s, token, p
}

func reviewChangeState(t *testing.T, p string, change func(map[string]any)) {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	if err = json.Unmarshal(b, &state); err != nil {
		t.Fatal(err)
	}
	change(state)
	b, err = json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestUnsupportedSchemaWithInvalidCredentialStaysUnknown(t *testing.T) {
	a, _, s, _, p := reviewAuthority(t)
	reviewChangeState(t, p, func(state map[string]any) { state["Version"] = 20 })
	got := a.InspectNativeCompatibility(strings.Repeat("x", 64), s.ID)
	if got.Status != "unknown" {
		t.Fatalf("unauthenticated authority observation is not unknown: status=%s schema=%d", got.Status, got.AuthoritySchema)
	}
}

func TestWrongBindingWithInvalidCredentialStaysUnknown(t *testing.T) {
	a, _, s, _, p := reviewAuthority(t)
	reviewChangeState(t, p, func(state map[string]any) {
		state["Binding"] = map[string]any{"Workspace": "foreign-workspace", "Installation": "foreign-installation"}
	})
	got := a.InspectNativeCompatibility(strings.Repeat("x", 64), s.ID)
	if got.Status != "unknown" {
		t.Fatalf("unauthenticated authority binding observation is not unknown: status=%s schema=%d", got.Status, got.AuthoritySchema)
	}
}

func TestAncestorRevocationClassified(t *testing.T) {
	a, owner, parent, token, _ := reviewAuthority(t)
	child, childToken, err := a.Delegate(token, parent.ID, parent.Scope, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err = a.Revoke(owner, parent.ID); err != nil {
		t.Fatal(err)
	}
	got := a.InspectNativeCompatibility(childToken, child.ID)
	if got.Status != "revoked_actor" {
		t.Fatalf("revoked ancestor: %+v", got)
	}
}

func TestAncestorExpiryClassified(t *testing.T) {
	a, _, parent, token, p := reviewAuthority(t)
	child, childToken, err := a.Delegate(token, parent.ID, parent.Scope, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	reviewChangeState(t, p, func(state map[string]any) {
		actor := state["Sessions"].(map[string]any)[parent.ID].(map[string]any)
		actor["ExpiresAt"] = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	})
	got := a.InspectNativeCompatibility(childToken, child.ID)
	if got.Status != "expired_enrollment" {
		t.Fatalf("expired ancestor: %+v", got)
	}
}
