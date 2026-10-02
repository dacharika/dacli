package sessions

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestReviewLeaseRejectsReplacedCheckoutRepository(t *testing.T) {
	a, owner, _, _, base, _ := leaseSetup(t)
	s, token, at := leaseAttempt(t, a, owner, "linked-task", "linked-attempt", secondCheckout(t, base))
	// Keep the observed repository intact but move it away from the registered locator.
	locator := at.Observation.Locator
	if err := os.Rename(locator, locator+"-original"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(locator + "-original") })
	if err := os.Mkdir(locator, 0700); err != nil {
		t.Fatal(err)
	}
	gitTest(t, locator, "init")
	gitTest(t, locator, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "replacement")
	l, err := a.AcquireLease(token, s.ID, acquireReq(at, "replaced", "src"))
	if err == nil {
		t.Fatalf("lease issued for replaced checkout: receipt repository=%s actual=%s", l.Repository, filepath.Join(locator, ".git"))
	}
}

func replaceLeaseCheckout(t *testing.T, locator string) {
	t.Helper()
	if err := os.Rename(locator, locator+"-original"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(locator + "-original") })
	if err := os.Mkdir(locator, 0700); err != nil {
		t.Fatal(err)
	}
	gitTest(t, locator, "init")
	gitTest(t, locator, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "replacement")
}

func TestLeaseReplacedCheckoutRefusesChangesAndRecoversHistory(t *testing.T) {
	a, owner, _, _, base, admission := leaseSetup(t)
	s, token, at := leaseAttempt(t, a, owner, "linked-task", "linked-attempt", secondCheckout(t, base))
	req := acquireReq(at, "acquire-linked", "src")
	l := mustAcquire(t, a, s, token, req)
	renew := RenewLeaseRequest{"renew-linked", at.ID, l.ID, l.Fence, time.Now().Add(20 * time.Minute)}
	prior, err := a.RenewLease(token, s.ID, renew)
	if err != nil {
		t.Fatal(err)
	}
	replaceLeaseCheckout(t, at.Observation.Locator)
	reopened, err := Open(a.path, a.binding, admission)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.AcquireLease(token, s.ID, req)
	if err != nil || !reflect.DeepEqual(got, l) {
		t.Fatalf("historical acquire: %+v %v", got, err)
	}
	got, err = reopened.RenewLease(token, s.ID, renew)
	if err != nil || !reflect.DeepEqual(got, prior) {
		t.Fatalf("historical renew: %+v %v", got, err)
	}
	renew.OperationKey = "fresh-renew"
	_, err = reopened.RenewLease(token, s.ID, renew)
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("replaced checkout renewal: %v", err)
	}
	_, err = reopened.ReleaseLease(token, s.ID, ReleaseLeaseRequest{"fresh-release", at.ID, l.ID, l.Fence})
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("replaced checkout release: %v", err)
	}
}

func TestLeaseDirtyCheckoutAllowsRenewalAndRelease(t *testing.T) {
	a, _, s, token, at, _ := leaseSetup(t)
	l := mustAcquire(t, a, s, token, acquireReq(at, "acquire", "src"))
	if err := os.WriteFile(filepath.Join(at.Observation.Locator, "dirty"), []byte("work in progress"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, at.Observation.Locator, "add", "dirty")
	if _, err := a.RenewLease(token, s.ID, RenewLeaseRequest{"renew", at.ID, l.ID, l.Fence, time.Now().Add(20 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	gitTest(t, at.Observation.Locator, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "legitimate work")
	if _, err := a.ReleaseLease(token, s.ID, ReleaseLeaseRequest{"release", at.ID, l.ID, l.Fence}); err != nil {
		t.Fatal(err)
	}
}
