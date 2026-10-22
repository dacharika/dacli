package sessions

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func setup(t *testing.T) (*Authority, string) {
	t.Helper()
	a, err := Open(filepath.Join(t.TempDir(), "authority.json"), Binding{"workspace", "installation"})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := a.Bootstrap()
	if err != nil {
		t.Fatal(err)
	}
	return a, owner
}
func enroll(t *testing.T, a *Authority, owner string) (Session, string) {
	t.Helper()
	s, token, err := a.Enroll(owner, Scope{[]string{"read", "write"}, []string{"task-a", "task-b"}}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return s, token
}
func denied(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("want denied, got %v", err)
	}
}
func TestAuthenticationBindingAndRestart(t *testing.T) {
	a, owner := setup(t)
	s, token := enroll(t, a, owner)
	independent, other := enroll(t, a, owner)
	if _, err := a.Authenticate(token, s.ID); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{owner, other, "codex", strings.Repeat("f", 64), ""} {
		_, err := a.Authenticate(bad, s.ID)
		denied(t, err)
		if strings.Contains(err.Error(), bad) && bad != "" {
			t.Fatal("credential in error")
		}
	}
	_, err := a.Authenticate(token, independent.ID)
	denied(t, err)
	_, err = a.Bootstrap()
	denied(t, err)
	for _, b := range []Binding{{"other", "installation"}, {"workspace", "other"}} {
		wrong, err := Open(a.path, b)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = wrong.Authenticate(token, s.ID); err == nil {
			t.Fatal("binding bypass")
		}
	}
	restarted, err := Open(a.path, a.binding)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = restarted.Authenticate(token, s.ID); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(a.path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{owner, token, other} {
		if strings.Contains(string(data), secret) {
			t.Fatal("secret persisted")
		}
	}
	info, err := os.Stat(a.path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("permissions: %v %v", info, err)
	}
	if err = a.Revoke(owner, s.ID); err != nil {
		t.Fatal(err)
	}
	_, err = restarted.Authenticate(token, s.ID)
	denied(t, err)
	if _, err = restarted.Authenticate(other, independent.ID); err != nil {
		t.Fatal(err)
	}
}
func TestDelegationGuardsAndAncestorRevocation(t *testing.T) {
	a, owner := setup(t)
	parent, token := enroll(t, a, owner)
	narrow := Scope{[]string{"read"}, []string{"task-a"}}
	for _, scope := range []Scope{{[]string{"deploy"}, []string{"task-a"}}, {[]string{"read"}, []string{"other"}}, {nil, []string{"task-a"}}} {
		_, _, err := a.Delegate(token, parent.ID, scope, parent.ExpiresAt)
		denied(t, err)
	}
	_, _, err := a.Delegate(token, parent.ID, narrow, parent.ExpiresAt.Add(time.Second))
	denied(t, err)
	_, _, err = a.Delegate(owner, parent.ID, narrow, parent.ExpiresAt)
	denied(t, err)
	child, childToken, err := a.Delegate(token, parent.ID, narrow, parent.ExpiresAt)
	if err != nil {
		t.Fatal(err)
	}
	grand, grandToken, err := a.Delegate(childToken, child.ID, narrow, parent.ExpiresAt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.Authenticate(grandToken, grand.ID); err != nil {
		t.Fatal(err)
	}
	denied(t, a.Revoke(token, parent.ID))
	if err = a.Revoke(owner, parent.ID); err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{{token, parent.ID}, {childToken, child.ID}, {grandToken, grand.ID}} {
		_, err = a.Authenticate(pair[0], pair[1])
		denied(t, err)
	}
	_, _, err = a.Delegate(childToken, child.ID, narrow, parent.ExpiresAt)
	denied(t, err)
}
func TestExpiry(t *testing.T) {
	a, owner := setup(t)
	scope := Scope{[]string{"read"}, []string{"task"}}
	_, _, err := a.Enroll(owner, scope, time.Now().Add(-time.Second))
	denied(t, err)
	s, token, err := a.Enroll(owner, scope, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	// Test the exact expiry boundary without depending on scheduler sleeps.
	err = a.transaction(false, false, func(state *state) error {
		_, err := authenticate(state, token, s.ID, s.ExpiresAt)
		denied(t, err)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
func TestConcurrentAuthorities(t *testing.T) {
	a, owner := setup(t)
	var wg sync.WaitGroup
	errs := make(chan error, 24)
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reopened, err := Open(a.path, a.binding)
			if err == nil {
				_, _, err = reopened.Enroll(owner, Scope{[]string{"read"}, []string{"task"}}, time.Now().Add(time.Hour))
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	err := a.transaction(false, false, func(s *state) error {
		if len(s.Sessions) != 24 {
			t.Fatalf("lost update: %d", len(s.Sessions))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
func TestStorageFailsClosed(t *testing.T) {
	a, owner := setup(t)
	if err := os.WriteFile(a.path, []byte("credential-secret malformed"), 0600); err != nil {
		t.Fatal(err)
	}
	_, _, err := a.Enroll(owner, Scope{[]string{"read"}, []string{"task"}}, time.Now().Add(time.Hour))
	if !errors.Is(err, ErrStorage) || strings.Contains(err.Error(), "secret") {
		t.Fatal(err)
	}
	if _, err := Open("relative", Binding{"w", "i"}); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
}

// Independent processes exercise the filesystem lock rather than a Go mutex.
func TestProcessUpdates(t *testing.T) {
	if path := os.Getenv("SESSIONS_TEST_AUTHORITY"); path != "" {
		a, err := Open(path, Binding{"workspace", "installation"})
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 8; i++ {
			_, _, err = a.Enroll(os.Getenv("SESSIONS_TEST_OWNER"), Scope{[]string{"read"}, []string{"task"}}, time.Now().Add(time.Hour))
			if err != nil {
				t.Fatal(err)
			}
		}
		return
	}
	a, owner := setup(t)
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.Command(os.Args[0], "-test.run=^TestProcessUpdates$")
			cmd.Env = append(os.Environ(), "SESSIONS_TEST_AUTHORITY="+a.path, "SESSIONS_TEST_OWNER="+owner)
			data, err := cmd.CombinedOutput()
			if err != nil {
				t.Errorf("child: %s", data)
			}
			errs <- err
		}()
	}
	// Readers must always observe a complete authority, even during replacements.
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
reading:
	for {
		select {
		case <-done:
			break reading
		default:
		}
		data, err := os.ReadFile(a.path)
		if err != nil {
			t.Fatal(err)
		}
		var s state
		if err = json.Unmarshal(data, &s); err != nil {
			t.Fatal("non-atomic authority update", err)
		}
	}
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	err := a.transaction(false, false, func(s *state) error {
		if len(s.Sessions) != 32 {
			t.Fatalf("lost process update: %d", len(s.Sessions))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
