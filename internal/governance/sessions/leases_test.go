package sessions

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/governance/checkout"
)

type testAdmission struct {
	err        error
	calls      int
	delayUntil time.Time
}

func (a *testAdmission) AdmitLease(w, p, t string) error {
	a.calls++
	if !a.delayUntil.IsZero() {
		time.Sleep(time.Until(a.delayUntil))
	}
	if w != "workspace" || p != "project" || t == "" {
		return ErrDenied
	}
	return a.err
}
func leaseAttempt(t *testing.T, a *Authority, owner, task, request, dir string) (Session, string, Attempt) {
	t.Helper()
	obs, err := checkout.Observe(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.BindTask(owner, TaskBinding{task, "project", obs.Repository, true}); err != nil {
		t.Fatal(err)
	}
	s, token, err := a.Enroll(owner, Scope{[]string{"attempt.register", "lease.acquire", "lease.renew", "lease.release"}, []string{"task:" + task, "path:src", "path:src/a", "path:src/b", "path:other", "checkout:"}}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	at, err := a.RegisterAttempt(token, s.ID, AttemptRequest{RequestID: request, TaskID: task, Checkout: dir})
	if err != nil {
		t.Fatal(err)
	}
	return s, token, at
}
func leaseSetup(t *testing.T) (*Authority, string, Session, string, Attempt, *testAdmission) {
	t.Helper()
	a, owner := setup(t)
	admission := &testAdmission{}
	a.admission = admission
	dir := t.TempDir()
	gitTest(t, dir, "init")
	gitTest(t, dir, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "initial")
	s, token, at := leaseAttempt(t, a, owner, "task-a", "attempt-a", dir)
	return a, owner, s, token, at, admission
}
func acquireReq(at Attempt, key, path string) AcquireLeaseRequest {
	return AcquireLeaseRequest{key, at.ID, []Resource{{"path", path}}, time.Now().Add(10 * time.Minute)}
}
func mustAcquire(t *testing.T, a *Authority, s Session, token string, req AcquireLeaseRequest) Lease {
	t.Helper()
	l, err := a.AcquireLease(token, s.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	return l
}
func secondCheckout(t *testing.T, at Attempt) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "checkout")
	gitTest(t, at.Observation.Locator, "worktree", "add", "--detach", dir, "HEAD")
	return dir
}
func TestLeaseRestartCanonicalRetryAndFences(t *testing.T) {
	a, _, s, token, at, admission := leaseSetup(t)
	req := acquireReq(at, "acquire", "src/a")
	req.Resources = append(req.Resources, Resource{"path", "src/a"}, Resource{"task", "task-a"})
	first := mustAcquire(t, a, s, token, req)
	if first.Fence != 1 || len(first.Resources) != 2 {
		t.Fatalf("bad receipt %+v", first)
	}
	reopened, err := Open(a.path, a.binding, admission)
	if err != nil {
		t.Fatal(err)
	}
	req.Resources = []Resource{{"task", "task-a"}, {"path", "src/a"}}
	retry, err := reopened.AcquireLease(token, s.ID, req)
	if err != nil || !reflect.DeepEqual(first, retry) {
		t.Fatalf("retry %v %+v", err, retry)
	}
	changed := req
	changed.Resources = []Resource{{"path", "src/b"}}
	_, err = a.AcquireLease(token, s.ID, changed)
	denied(t, err)
	renew := RenewLeaseRequest{"renew", at.ID, first.ID, first.Fence, time.Now().Add(20 * time.Minute)}
	renewed, err := a.RenewLease(token, s.ID, renew)
	if err != nil || renewed.Fence != first.Fence {
		t.Fatal(err)
	}
	again, err := reopened.RenewLease(token, s.ID, renew)
	if err != nil || !reflect.DeepEqual(renewed, again) {
		t.Fatal(err)
	}
	stale := renew
	stale.OperationKey = "stale"
	stale.ExpectedFence++
	_, err = a.RenewLease(token, s.ID, stale)
	denied(t, err)
	release := ReleaseLeaseRequest{"release", at.ID, first.ID, first.Fence}
	released, err := a.ReleaseLease(token, s.ID, release)
	if err != nil || !released.Released {
		t.Fatal(err)
	}
	again, err = reopened.ReleaseLease(token, s.ID, release)
	if err != nil || !reflect.DeepEqual(released, again) {
		t.Fatal(err)
	}
	second := mustAcquire(t, a, s, token, acquireReq(at, "second", "src/a"))
	if second.Fence != 2 {
		t.Fatal("fence did not increase")
	}
	_, err = a.ReleaseLease(token, s.ID, ReleaseLeaseRequest{"stale-release", at.ID, second.ID, first.Fence})
	denied(t, err)
	// An acquire retry is historical even after release; it never reserves anew.
	retry, err = a.AcquireLease(token, s.ID, req)
	if err != nil || !reflect.DeepEqual(first, retry) {
		t.Fatal(err)
	}
}
func TestLeaseOverlapAtomicSetAndTaskReservation(t *testing.T) {
	a, owner, s, token, at, _ := leaseSetup(t)
	first := mustAcquire(t, a, s, token, acquireReq(at, "first", "src/a"))
	dir := secondCheckout(t, at)
	other, otherToken, otherAt := leaseAttempt(t, a, owner, "task-b", "attempt-b", dir)
	req := acquireReq(otherAt, "overlap", "other")
	req.Resources = append(req.Resources, Resource{"path", "src"})
	_, err := a.AcquireLease(otherToken, other.ID, req)
	denied(t, err)
	if err := a.transaction(false, false, func(st *state) error {
		if st.Fence != 1 || len(st.Leases) != 1 || len(st.Operations) != 1 {
			t.Fatal("partial reservation")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Prefix segment boundary: src/b does not overlap src/a.
	disjoint := mustAcquire(t, a, other, otherToken, acquireReq(otherAt, "disjoint", "src/b"))
	if disjoint.Fence != 2 {
		t.Fatal("disjoint fence")
	}
	same, sameToken, sameAt := leaseAttempt(t, a, owner, "task-a", "attempt-same", secondCheckout(t, at))
	_, err = a.AcquireLease(sameToken, same.ID, acquireReq(sameAt, "split-task", "other"))
	denied(t, err)
	_, err = a.ReleaseLease(token, s.ID, ReleaseLeaseRequest{"release", at.ID, first.ID, first.Fence})
	if err != nil {
		t.Fatal(err)
	}
	mustAcquire(t, a, same, sameToken, acquireReq(sameAt, "after-release", "other"))
}
func TestLeaseRevocationAdmissionOwnershipAndOverflow(t *testing.T) {
	a, owner, s, token, at, admission := leaseSetup(t)
	req := acquireReq(at, "first", "src")
	a.admission = nil
	_, err := a.AcquireLease(token, s.ID, req)
	denied(t, err)
	a.admission = admission
	admission.err = errors.New("unready")
	_, err = a.AcquireLease(token, s.ID, req)
	denied(t, err)
	admission.err = nil
	_, err = a.AcquireLease("bad", s.ID, req)
	denied(t, err)
	wrong, wrongToken := enroll(t, a, owner)
	_, err = a.AcquireLease(wrongToken, wrong.ID, req)
	denied(t, err)
	bad := req
	bad.AttemptID = "missing"
	_, err = a.AcquireLease(token, s.ID, bad)
	denied(t, err)
	l := mustAcquire(t, a, s, token, req)
	admission.err = errors.New("active task is no longer open")
	_, err = a.RenewLease(token, s.ID, RenewLeaseRequest{"renew", at.ID, l.ID, l.Fence, time.Now().Add(15 * time.Minute)})
	if err != nil {
		t.Fatal("renew reapplied admission", err)
	}
	if err := a.Revoke(owner, s.ID); err != nil {
		t.Fatal(err)
	}
	_, err = a.AcquireLease(token, s.ID, req)
	denied(t, err)
	_, err = a.ReleaseLease(token, s.ID, ReleaseLeaseRequest{"release", at.ID, l.ID, l.Fence})
	denied(t, err)
	other, otherToken, otherAt := leaseAttempt(t, a, owner, "task-b", "other", at.Observation.Locator)
	admission.err = nil
	_, err = a.AcquireLease(otherToken, other.ID, acquireReq(otherAt, "takeover", "other"))
	denied(t, err)
	a2, _, s2, tok2, at2, _ := leaseSetup(t)
	if err := a2.transaction(false, true, func(st *state) error { st.Fence = math.MaxUint64; return nil }); err != nil {
		t.Fatal(err)
	}
	_, err = a2.AcquireLease(tok2, s2.ID, acquireReq(at2, "overflow", "src"))
	denied(t, err)
}
func TestLeaseExpiryEqualityAndUncertainCheckout(t *testing.T) {
	a, owner, s, token, at, _ := leaseSetup(t)
	l := mustAcquire(t, a, s, token, acquireReq(at, "first", "src"))
	now := time.Now()
	if currentLease(&state{}, Lease{ExpiresAt: now}, now) {
		t.Fatal("expiry equality accepted")
	}
	if err := a.transaction(false, true, func(st *state) error { old := st.Leases[l.ID]; old.ExpiresAt = now; st.Leases[l.ID] = old; return nil }); err != nil {
		t.Fatal(err)
	}
	_, err := a.RenewLease(token, s.ID, RenewLeaseRequest{"renew", at.ID, l.ID, l.Fence, time.Now().Add(time.Minute)})
	denied(t, err)
	_, err = a.ReleaseLease(token, s.ID, ReleaseLeaseRequest{"release", at.ID, l.ID, l.Fence})
	denied(t, err)
	other, otherToken, otherAt := leaseAttempt(t, a, owner, "task-b", "other", at.Observation.Locator)
	_, err = a.AcquireLease(otherToken, other.ID, acquireReq(otherAt, "takeover", "other"))
	denied(t, err)
	// Authentication uses strictly-before at the exact boundary, including ancestors.
	if err := a.transaction(false, true, func(st *state) error {
		r := st.Sessions[s.ID]
		r.ExpiresAt = now
		st.Sessions[s.ID] = r
		_, e := authenticate(st, token, s.ID, now)
		denied(t, e)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
func TestLeaseResourceAliases(t *testing.T) {
	root := t.TempDir()
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"../x", "src/../x", "/absolute", "src//x", "src/./x", "Src", "src/*", "src/**", "src\\x", "link/x", ".git/config", "..", "."} {
		t.Run(path, func(t *testing.T) {
			_, err := canonicalResources(root, "task", []Resource{{"path", path}})
			denied(t, err)
		})
	}
	if prefix("src/a", "src/ab") || !prefix("src/a", "src/a/child") {
		t.Fatal("segment prefix")
	}
}
func TestLeaseSchemaUpgradePreservesAttempts(t *testing.T) {
	a, owner, s, token, at, _ := leaseSetup(t)
	data, err := os.ReadFile(a.path)
	if err != nil {
		t.Fatal(err)
	}
	var st state
	if err = json.Unmarshal(data, &st); err != nil {
		t.Fatal(err)
	}
	st.Version = 2
	st.Leases = nil
	st.Operations = nil
	data, err = json.Marshal(st)
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
	denied(t, a.UpgradeLeases(token))
	if err = a.UpgradeLeases(owner); err != nil {
		t.Fatal(err)
	}
	if err = a.transaction(false, false, func(new *state) error {
		if !reflect.DeepEqual(st.Sessions, new.Sessions) || !reflect.DeepEqual(st.Attempts, new.Attempts) || !reflect.DeepEqual(st.Tasks, new.Tasks) || st.OwnerHash != new.OwnerHash {
			t.Fatal("migration lost records")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	mustAcquire(t, a, s, token, acquireReq(at, "after-upgrade", "src"))
}
func childEnvironment() []string {
	var out []string
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "DACLI_AGENT=") && !strings.HasPrefix(e, "LEASE_TEST_") {
			out = append(out, e)
		}
	}
	return out
}
func TestLeaseProcessRace(t *testing.T) {
	if data := os.Getenv("LEASE_TEST_INPUT"); data != "" {
		var input struct {
			Path, Token, Session string
			Request              AcquireLeaseRequest
		}
		if err := json.Unmarshal([]byte(data), &input); err != nil {
			t.Fatal(err)
		}
		a, err := Open(input.Path, Binding{"workspace", "installation"}, &testAdmission{})
		if err != nil {
			t.Fatal(err)
		}
		_, err = a.AcquireLease(input.Token, input.Session, input.Request)
		if err != nil && !errors.Is(err, ErrDenied) {
			t.Fatal(err)
		}
		return
	}
	for _, conflict := range []bool{true, false} {
		t.Run(fmt.Sprint(conflict), func(t *testing.T) {
			a, owner, s, token, at, _ := leaseSetup(t)
			other, otherToken, otherAt := leaseAttempt(t, a, owner, "task-b", "other", secondCheckout(t, at))
			path := "src/b"
			if conflict {
				path = "src/a"
			}
			inputs := []struct {
				Path, Token, Session string
				Request              AcquireLeaseRequest
			}{{a.path, token, s.ID, acquireReq(at, "one", "src/a")}, {a.path, otherToken, other.ID, acquireReq(otherAt, "two", path)}}
			var wg sync.WaitGroup
			for _, input := range inputs {
				data, err := json.Marshal(input)
				if err != nil {
					t.Fatal(err)
				}
				wg.Add(1)
				go func(data []byte) {
					defer wg.Done()
					cmd := exec.Command(os.Args[0], "-test.run=^TestLeaseProcessRace$")
					cmd.Env = append(childEnvironment(), "LEASE_TEST_INPUT="+string(data))
					if out, err := cmd.CombinedOutput(); err != nil {
						t.Errorf("child %s %v", out, err)
					}
				}(data)
			}
			wg.Wait()
			if err := a.transaction(false, false, func(st *state) error {
				want := 2
				if conflict {
					want = 1
				}
				if len(st.Leases) != want || len(st.Operations) != want || st.Fence != uint64(want) {
					t.Errorf("winners=%d fences=%d want %d", len(st.Leases), st.Fence, want)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLeaseAdmissionCannotOutliveExpiry(t *testing.T) {
	a, _, s, token, at, admission := leaseSetup(t)
	req := acquireReq(at, "slow-admission", "src")
	req.ExpiresAt = time.Now().Add(50 * time.Millisecond)
	admission.delayUntil = req.ExpiresAt.Add(10 * time.Millisecond)
	_, err := a.AcquireLease(token, s.ID, req)
	denied(t, err)
	if err := a.transaction(false, false, func(st *state) error {
		if st.Fence != 0 || len(st.Leases) != 0 {
			t.Fatal("expired admission reserved")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
func TestLeaseAncestorRevocation(t *testing.T) {
	a, owner, parent, token, at, _ := leaseSetup(t)
	child, childToken, err := a.Delegate(token, parent.ID, parent.Scope, parent.ExpiresAt)
	if err != nil {
		t.Fatal(err)
	}
	req := at.Request
	req.RequestID = "child-attempt"
	childAt, err := a.RegisterAttempt(childToken, child.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	l := mustAcquire(t, a, child, childToken, acquireReq(childAt, "child-lease", "src"))
	if err = a.Revoke(owner, parent.ID); err != nil {
		t.Fatal(err)
	}
	_, err = a.RenewLease(childToken, child.ID, RenewLeaseRequest{"child-renew", childAt.ID, l.ID, l.Fence, time.Now().Add(time.Minute)})
	denied(t, err)
	_, err = a.ReleaseLease(childToken, child.ID, ReleaseLeaseRequest{"child-release", childAt.ID, l.ID, l.Fence})
	denied(t, err)
}
