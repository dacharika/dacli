package sessions

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/governance/checkout"
)

func gitTest(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git: %s %v", out, err)
	}
}
func attemptSetup(t *testing.T) (*Authority, string, Session, string, AttemptRequest) {
	t.Helper()
	a, owner := setup(t)
	dir := t.TempDir()
	gitTest(t, dir, "init")
	gitTest(t, dir, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "initial")
	obs, err := checkout.Observe(dir)
	if err != nil {
		t.Fatal(err)
	}
	b := TaskBinding{"canonical-task", "project", obs.Repository, true}
	denied(t, a.BindTask("agent", b))
	if err = a.BindTask(owner, b); err != nil {
		t.Fatal(err)
	}
	s, token, err := a.Enroll(owner, Scope{[]string{"attempt.register"}, []string{"task:canonical-task"}}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return a, owner, s, token, AttemptRequest{"request", "canonical-task", dir, "host"}
}
func TestAttemptsRestartAndRefusals(t *testing.T) {
	a, owner, s, token, req := attemptSetup(t)
	first, err := a.RegisterAttempt(token, s.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(a.path, a.binding)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := reopened.RegisterAttempt(token, s.ID, req)
	if err != nil || !reflect.DeepEqual(first, retry) {
		t.Fatalf("retry: %v", err)
	}
	changed := req
	changed.HostLabel = "changed"
	_, err = a.RegisterAttempt(token, s.ID, changed)
	denied(t, err)
	wrong := req
	wrong.TaskID = "other"
	_, err = a.RegisterAttempt(token, s.ID, wrong)
	denied(t, err)
	wrong = req
	wrong.Checkout = t.TempDir()
	gitTest(t, wrong.Checkout, "init")
	gitTest(t, wrong.Checkout, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "other")
	_, err = a.RegisterAttempt(token, s.ID, wrong)
	denied(t, err)
	read, readToken, err := a.Enroll(owner, Scope{[]string{"read"}, []string{"task:canonical-task"}}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	wrongScope, wrongToken, err := a.Enroll(owner, Scope{[]string{"attempt.register"}, []string{"task:other"}}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	fresh := req
	fresh.RequestID = "wrong-resource"
	_, err = a.RegisterAttempt(wrongToken, wrongScope.ID, fresh)
	denied(t, err)
	if err = a.transaction(false, true, func(st *state) error {
		r := st.Sessions[wrongScope.ID]
		r.ExpiresAt = time.Now().Add(-time.Second)
		st.Sessions[r.ID] = r
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_, err = a.RegisterAttempt(wrongToken, wrongScope.ID, fresh)
	denied(t, err)
	obs, err := checkout.Observe(req.Checkout)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.BindTask(owner, TaskBinding{req.TaskID, "project", obs.Repository, false}); err != nil {
		t.Fatal(err)
	}
	fresh.RequestID = "not-ready"
	_, err = a.RegisterAttempt(token, s.ID, fresh)
	denied(t, err)
	if err = a.BindTask(owner, TaskBinding{req.TaskID, "project", obs.Repository, true}); err != nil {
		t.Fatal(err)
	}
	readReq := req
	readReq.RequestID = "read-only-request"
	_, err = a.RegisterAttempt(readToken, read.ID, readReq)
	denied(t, err)
	if err = os.WriteFile(filepath.Join(req.Checkout, "dirty"), []byte("dirty"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = a.RegisterAttempt(token, s.ID, req)
	denied(t, err)
	if err = os.Remove(filepath.Join(req.Checkout, "dirty")); err != nil {
		t.Fatal(err)
	}
	gitTest(t, req.Checkout, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "changed")
	_, err = a.RegisterAttempt(token, s.ID, req)
	denied(t, err)
	child, childToken, err := a.Delegate(token, s.ID, s.Scope, s.ExpiresAt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.Authenticate(childToken, child.ID); err != nil {
		t.Fatal(err)
	}
	if err = a.Revoke(owner, s.ID); err != nil {
		t.Fatal(err)
	}
	req.RequestID = "after-revoke"
	_, err = a.RegisterAttempt(childToken, child.ID, req)
	denied(t, err)
}
func TestAttemptSchemaUpgrade(t *testing.T) {
	a, owner := setup(t)
	s, token := enroll(t, a, owner)
	if err := a.Revoke(owner, s.ID); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(a.path)
	if err != nil {
		t.Fatal(err)
	}
	var old state
	if err = json.Unmarshal(data, &old); err != nil {
		t.Fatal(err)
	}
	old.Version = 1
	old.Tasks = nil
	old.Attempts = nil
	data, err = json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(a.path, data, 0600); err != nil {
		t.Fatal(err)
	}
	_, err = a.Authenticate(token, s.ID)
	if !errors.Is(err, ErrStorage) {
		t.Fatal(err)
	}
	denied(t, a.Upgrade(token))
	if err = a.Upgrade(owner); err != nil {
		t.Fatal(err)
	}
	var upgraded state
	data, err = os.ReadFile(a.path)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &upgraded); err != nil {
		t.Fatal(err)
	}
	if old.OwnerHash != upgraded.OwnerHash || !reflect.DeepEqual(old.Sessions, upgraded.Sessions) {
		t.Fatal("identity changed")
	}
	denied(t, func() error { _, err := a.Authenticate(token, s.ID); return err }())
	legacy := *a
	legacy.version = 1
	if err = legacy.Revoke(owner, s.ID); !errors.Is(err, ErrStorage) {
		t.Fatal("legacy writer accepted", err)
	}
}
func TestAttemptProcessRace(t *testing.T) {
	if path := os.Getenv("ATTEMPT_TEST_PATH"); path != "" {
		a, err := Open(path, Binding{"workspace", "installation"})
		if err != nil {
			t.Fatal(err)
		}
		requestID := os.Getenv("ATTEMPT_TEST_REQUEST")
		if requestID == "" {
			requestID = "request"
		}
		req := AttemptRequest{requestID, "canonical-task", os.Getenv("ATTEMPT_TEST_CHECKOUT"), "host"}
		_, err = a.RegisterAttempt(os.Getenv("ATTEMPT_TEST_TOKEN"), os.Getenv("ATTEMPT_TEST_SESSION"), req)
		if err != nil && !errors.Is(err, ErrDenied) {
			t.Fatal(err)
		}
		return
	}
	a, owner, s, token, req := attemptSetup(t)
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.Command(os.Args[0], "-test.run=^TestAttemptProcessRace$")
			cmd.Env = append(os.Environ(), "ATTEMPT_TEST_PATH="+a.path, "ATTEMPT_TEST_CHECKOUT="+req.Checkout, "ATTEMPT_TEST_TOKEN="+token, "ATTEMPT_TEST_SESSION="+s.ID)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("child: %s %v", out, err)
			}
		}()
	}
	wg.Wait()
	if err := a.transaction(false, false, func(st *state) error {
		if len(st.Attempts) != 1 {
			t.Errorf("attempt count: %d", len(st.Attempts))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Race fresh registration against revocation from a separate process.
	cmd := exec.Command(os.Args[0], "-test.run=^TestAttemptProcessRace$")
	cmd.Env = append(os.Environ(), "ATTEMPT_TEST_PATH="+a.path, "ATTEMPT_TEST_CHECKOUT="+req.Checkout, "ATTEMPT_TEST_TOKEN="+token, "ATTEMPT_TEST_SESSION="+s.ID, "ATTEMPT_TEST_REQUEST=racing-request")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := a.Revoke(owner, s.ID); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	_, err := a.RegisterAttempt(token, s.ID, req)
	denied(t, err)
}
