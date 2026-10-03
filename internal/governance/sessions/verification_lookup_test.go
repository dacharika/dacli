package sessions

import (
	"bytes"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"
)

func lookupFixture(t *testing.T, f evidenceFixture, req VerificationRequest) VerificationLookup {
	t.Helper()
	before, err := os.ReadFile(f.a.path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.a.LookupVerification(f.workerToken, f.runnerToken, f.runner.ID, req, CheckDigest(f.check))
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(f.a.path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("lookup rewrote authority", err)
	}
	return got
}

func TestVerificationLookupReadOnlyRestartAndHistoricalReceipt(t *testing.T) {
	f := evidenceSetup(t)
	if got := lookupFixture(t, f, f.request); got.State != VerificationAbsent || got.Evidence != nil {
		t.Fatalf("absence: %+v", got)
	}
	// A lookup did not reserve the operation: the actual begin must still work.
	f.begin(t)
	reopened, err := Open(f.a.path, f.a.binding)
	if err != nil {
		t.Fatal(err)
	}
	f.a = reopened
	if got := lookupFixture(t, f, f.request); got.State != VerificationPending || got.Evidence != nil {
		t.Fatalf("restart pending: %+v", got)
	}
	ev, err := f.a.CompleteVerification(f.workerToken, f.runnerToken, f.runner.ID, f.request, f.result())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.a.ReleaseLease(f.workerToken, f.worker.ID, ReleaseLeaseRequest{OperationKey: "release", AttemptID: f.at.ID, LeaseID: f.lease.ID, ExpectedFence: f.lease.Fence}); err != nil {
		t.Fatal(err)
	}
	reopened, err = Open(f.a.path, f.a.binding)
	if err != nil {
		t.Fatal(err)
	}
	f.a = reopened
	got := lookupFixture(t, f, f.request)
	if got.State != VerificationCompleted || got.Evidence == nil || !reflect.DeepEqual(*got.Evidence, ev) {
		t.Fatalf("historical receipt: %+v", got)
	}
	got.Evidence.Revision.Observation.Tree = "changed returned copy"
	got.Evidence.GrantID = "changed"
	if again := lookupFixture(t, f, f.request); again.Evidence == nil || !reflect.DeepEqual(*again.Evidence, ev) {
		t.Fatal("caller changed durable receipt")
	}
}

func TestVerificationLookupRejectsChangedOperationBinding(t *testing.T) {
	f := evidenceSetup(t)
	f.begin(t)
	other, token, err := f.a.Enroll(f.owner, f.worker.Scope, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	mutations := map[string]func(*VerificationRequest){
		"lease":    func(r *VerificationRequest) { r.LeaseID = "other" },
		"revision": func(r *VerificationRequest) { r.RevisionID = "other" },
		"fence":    func(r *VerificationRequest) { r.ExpectedFence++ },
		"attempt":  func(r *VerificationRequest) { r.AttemptID = "other" },
		"check":    func(r *VerificationRequest) { r.CheckID = "other" },
		"grant":    func(r *VerificationRequest) { r.GrantID = "other" },
		"worker":   func(r *VerificationRequest) { r.WorkerSession = other.ID },
	}
	before, err := os.ReadFile(f.a.path)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			req := f.request
			mutate(&req)
			wt := f.workerToken
			if name == "worker" {
				wt = token
			}
			got, e := f.a.LookupVerification(wt, f.runnerToken, f.runner.ID, req, CheckDigest(f.check))
			if !errors.Is(e, ErrDenied) || got.State != "" || got.Evidence != nil {
				t.Fatalf("changed %s binding accepted: %+v %v", name, got, e)
			}
		})
	}
	after, err := os.ReadFile(f.a.path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("refused lookup rewrote authority", err)
	}
}

func TestVerificationLookupRequiresCurrentAuthority(t *testing.T) {
	for _, mode := range []string{"worker-revoked", "runner-revoked", "grant-revoked", "descriptor", "worker-token", "runner-token"} {
		t.Run(mode, func(t *testing.T) {
			f := evidenceSetup(t)
			f.begin(t)
			if _, err := f.a.CompleteVerification(f.workerToken, f.runnerToken, f.runner.ID, f.request, f.result()); err != nil {
				t.Fatal(err)
			}
			digest := CheckDigest(f.check)
			switch mode {
			case "worker-revoked":
				if err := f.a.Revoke(f.owner, f.worker.ID); err != nil {
					t.Fatal(err)
				}
			case "runner-revoked":
				if err := f.a.Revoke(f.owner, f.runner.ID); err != nil {
					t.Fatal(err)
				}
			case "grant-revoked":
				if err := f.a.RevokeVerification(f.owner, f.grant.ID); err != nil {
					t.Fatal(err)
				}
			case "descriptor":
				changed := f.check
				changed.Command = "different"
				digest = CheckDigest(changed)
			case "worker-token":
				f.workerToken = "invalid"
			case "runner-token":
				f.runnerToken = "invalid"
			}
			before, err := os.ReadFile(f.a.path)
			if err != nil {
				t.Fatal(err)
			}
			got, err := f.a.LookupVerification(f.workerToken, f.runnerToken, f.runner.ID, f.request, digest)
			if !errors.Is(err, ErrDenied) || got.State != "" || got.Evidence != nil {
				t.Fatalf("%s disclosed receipt: %+v %v", mode, got, err)
			}
			after, err := os.ReadFile(f.a.path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("refused lookup changed authority", err)
			}
		})
	}
}
