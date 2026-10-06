package sessions

import (
	"encoding/json"
	"errors"
	"github.com/mlnomadpy/dacli/internal/governance/checkout"
	"os"
	"reflect"
	"testing"
	"time"
)

type evidenceFixture struct {
	a                               *Authority
	owner, workerToken, runnerToken string
	worker, runner                  Session
	at                              Attempt
	lease                           Lease
	revision                        AttemptRevision
	publish                         PublishRevisionRequest
	check                           CheckDescriptor
	grant                           VerificationGrant
	request                         VerificationRequest
}

func evidenceSetup(t *testing.T) evidenceFixture { return evidenceSetupDelegated(t, false) }
func evidenceSetupDelegated(t *testing.T, delegated bool) evidenceFixture {
	t.Helper()
	a, owner := setup(t)
	a.admission = &testAdmission{}
	dir := t.TempDir()
	gitTest(t, dir, "init")
	gitTest(t, dir, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "initial")
	scope := Scope{[]string{"attempt.register", "lease.acquire", "lease.renew", "lease.release", "attempt.publish", "evidence.submit", "verification.request"}, []string{"task:task-a", "checkout:"}}
	worker, token, err := a.Enroll(owner, scope, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if delegated {
		parent, parentToken, err := a.Enroll(owner, scope, time.Now().Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		worker, token, err = a.Delegate(parentToken, parent.ID, scope, time.Now().Add(30*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
	}
	// The registration base remains untouched when final HEAD is published.
	obs, err := checkout.Observe(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.BindTask(owner, TaskBinding{"task-a", "project", obs.Repository, true}); err != nil {
		t.Fatal(err)
	}
	at, err := a.RegisterAttempt(token, worker.ID, AttemptRequest{RequestID: "attempt", TaskID: "task-a", Checkout: dir})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := a.AcquireLease(token, worker.ID, AcquireLeaseRequest{"lease", at.ID, []Resource{{"checkout", ""}}, time.Now().Add(10 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	gitTest(t, dir, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "final")
	publish := PublishRevisionRequest{"publish", at.ID, lease.ID, lease.Fence, 0}
	revision, err := a.PublishAttemptRevision(token, worker.ID, publish)
	if err != nil {
		t.Fatal(err)
	}
	runner, runnerToken, err := a.Enroll(owner, Scope{[]string{"verification.run"}, []string{"task:task-a"}}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	check := CheckDescriptor{ID: "test", Command: "printf harmless", Deadline: time.Second}
	grant, err := a.GrantVerification(owner, runner.ID, "task-a", at.Observation.Repository, check, time.Now().Add(10*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	req := VerificationRequest{"run", worker.ID, at.ID, lease.ID, revision.ID, check.ID, grant.ID, lease.Fence}
	return evidenceFixture{a, owner, token, runnerToken, worker, runner, at, lease, revision, publish, check, grant, req}
}
func (f evidenceFixture) begin(t *testing.T) {
	t.Helper()
	rev, receipt, err := f.a.BeginVerification(f.workerToken, f.runnerToken, f.runner.ID, f.request, CheckDigest(f.check))
	if err != nil || receipt != nil || rev.ID != f.revision.ID {
		t.Fatalf("begin %+v %+v %v", rev, receipt, err)
	}
}
func (f evidenceFixture) result() VerificationResult {
	return VerificationResult{Commit: f.revision.Observation.Commit, Tree: f.revision.Observation.Tree, Directory: f.revision.Observation.Locator, CommandDigest: CheckDigest(f.check), OutputDigest: OutputDigest([]byte("harmless")), Output: []byte("harmless"), Clean: true}
}
func TestPublishRevisionHistoryRetryAndRefusals(t *testing.T) {
	f := evidenceSetup(t)
	if f.revision.Observation.Commit == f.at.Observation.Commit || f.revision.Sequence != 1 {
		t.Fatal("did not publish final head")
	}
	reopened, err := Open(f.a.path, f.a.binding)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := reopened.PublishAttemptRevision(f.workerToken, f.worker.ID, f.publish)
	if err != nil || !reflect.DeepEqual(retry, f.revision) {
		t.Fatalf("retry %v", err)
	}
	if err = f.a.transaction(false, false, func(s *state) error {
		if !reflect.DeepEqual(s.Attempts[f.at.Request.RequestID], f.at) {
			t.Fatal("registration rewritten")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"changed-key-payload", "stale-prior", "fence", "owner", "dirty", "replaced", "revoked", "expired"} {
		t.Run(kind, func(t *testing.T) {
			f := evidenceSetup(t)
			req := f.publish
			req.OperationKey = "new"
			req.ExpectedRevision = 1
			token := f.workerToken
			switch kind {
			case "changed-key-payload":
				req.OperationKey = f.publish.OperationKey
			case "stale-prior":
				req.ExpectedRevision = 0
			case "fence":
				req.ExpectedFence++
			case "owner":
				token = f.owner
			case "dirty":
				if err := os.WriteFile(f.at.Observation.Locator+"/dirty", []byte("dirty"), 0600); err != nil {
					t.Fatal(err)
				}
			case "replaced":
				// Replace the checkout Git locator with a different repository.
				other := t.TempDir()
				gitTest(t, other, "init")
				gitTest(t, other, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "other")
				original := f.at.Observation.Locator + "/.git"
				if err := os.Rename(original, original+"-historical"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(original, []byte("gitdir: "+other+"/.git\n"), 0600); err != nil {
					t.Fatal(err)
				}
				// Ignore the archived directory so refusal measures identity, not dirt.
				if err := os.WriteFile(other+"/.git/info/exclude", []byte(".git-historical/\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "revoked":
				if err := f.a.Revoke(f.owner, f.worker.ID); err != nil {
					t.Fatal(err)
				}
			case "expired":
				if err := f.a.transaction(false, true, func(s *state) error {
					l := s.Leases[f.lease.ID]
					l.ExpiresAt = time.Now().Add(-time.Second)
					s.Leases[l.ID] = l
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			_, err := f.a.PublishAttemptRevision(token, f.worker.ID, req)
			denied(t, err)
		})
	}
}
func TestEvidenceSelfReportCannotPromote(t *testing.T) {
	f := evidenceSetup(t)
	req := EvidenceRequest{"self", f.at.ID, f.lease.ID, f.revision.ID, "test", f.lease.Fence, 0, OutputDigest(nil)}
	ev, err := f.a.SubmitEvidence(f.workerToken, f.worker.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Provenance != "self-reported" || ev.GrantID != "" || ev.IssuerSession != f.worker.ID {
		t.Fatalf("promoted %+v", ev)
	}
	eligible, err := f.a.EligibleVerification(f.workerToken, f.worker.ID, ev.ID)
	if err != nil || eligible {
		t.Fatal("self-report eligible", err)
	}
	retry, err := f.a.SubmitEvidence(f.workerToken, f.worker.ID, req)
	if err != nil || !reflect.DeepEqual(ev, retry) {
		t.Fatal("self retry", err)
	}
	req.ExitCode = 1
	_, err = f.a.SubmitEvidence(f.workerToken, f.worker.ID, req)
	denied(t, err)
}
func TestVerificationReceiptRestartAndUnknownOutcome(t *testing.T) {
	f := evidenceSetup(t)
	f.begin(t)
	_, _, err := f.a.BeginVerification(f.workerToken, f.runnerToken, f.runner.ID, f.request, CheckDigest(f.check))
	if !errors.Is(err, ErrReconcile) {
		t.Fatal(err)
	}
	ev, err := f.a.CompleteVerification(f.workerToken, f.runnerToken, f.runner.ID, f.request, f.result())
	if err != nil {
		t.Fatal(err)
	}
	eligible, err := f.a.EligibleVerification(f.workerToken, f.worker.ID, ev.ID)
	if err != nil || !eligible {
		t.Fatal("eligible", err)
	}
	f.a, err = Open(f.a.path, f.a.binding)
	if err != nil {
		t.Fatal(err)
	}
	_, receipt, err := f.a.BeginVerification(f.workerToken, f.runnerToken, f.runner.ID, f.request, CheckDigest(f.check))
	if err != nil || receipt == nil || !reflect.DeepEqual(ev, *receipt) {
		t.Fatal("restart receipt", err)
	}
	changed := f.result()
	changed.ExitCode = 1
	_, err = f.a.CompleteVerification(f.workerToken, f.runnerToken, f.runner.ID, f.request, changed)
	denied(t, err)
	f.request.CheckID = "other"
	_, _, err = f.a.BeginVerification(f.workerToken, f.runnerToken, f.runner.ID, f.request, CheckDigest(f.check))
	denied(t, err)
}
func TestVerificationFinalRevisionFenceAndProvenanceGuards(t *testing.T) {
	for _, kind := range []string{"revision-advance", "stale-revision", "fence", "fence-state", "ancestor-revoked", "commit", "tree", "directory", "output-digest", "command-digest", "check", "grant", "forged-token", "issuer", "worker-revoked", "runner-revoked", "grant-revoked", "grant-task", "grant-repository", "issuer-substitution", "lease-expired", "grant-expired", "runner-expired", "scope-action", "scope-resource", "dirty"} {
		t.Run(kind, func(t *testing.T) {
			f := evidenceSetup(t)
			if kind == "ancestor-revoked" {
				f = evidenceSetupDelegated(t, true)
			}
			f.begin(t)
			res := f.result()
			req := f.request
			runnerToken, runnerID := f.runnerToken, f.runner.ID
			change := func(fn func(*state)) {
				t.Helper()
				if err := f.a.transaction(false, true, func(s *state) error { fn(s); return nil }); err != nil {
					t.Fatal(err)
				}
			}
			switch kind {
			case "revision-advance":
				p := f.publish
				p.OperationKey = "advance"
				p.ExpectedRevision = 1
				if _, err := f.a.PublishAttemptRevision(f.workerToken, f.worker.ID, p); err != nil {
					t.Fatal(err)
				}
			case "stale-revision":
				req.RevisionID = f.at.ID
			case "fence":
				req.ExpectedFence++
			case "fence-state":
				change(func(s *state) { l := s.Leases[f.lease.ID]; l.Fence++; s.Leases[l.ID] = l })
			case "ancestor-revoked":
				if err := f.a.Revoke(f.owner, f.worker.Parent); err != nil {
					t.Fatal(err)
				}
			case "commit":
				res.Commit = f.at.Observation.Commit
			case "tree":
				res.Tree = "wrong"
			case "directory":
				res.Directory = t.TempDir()
			case "output-digest":
				res.OutputDigest = OutputDigest(nil)
			case "command-digest":
				res.CommandDigest = "wrong"
			case "check":
				req.CheckID = "other"
			case "grant":
				req.GrantID = "forged"
			case "forged-token":
				runnerToken = f.workerToken
			case "issuer":
				runnerID = f.worker.ID
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
			case "grant-task":
				change(func(s *state) { g := s.Grants[f.grant.ID]; g.TaskID = "other"; s.Grants[g.ID] = g })
			case "grant-repository":
				change(func(s *state) { g := s.Grants[f.grant.ID]; g.Repository = t.TempDir(); s.Grants[g.ID] = g })
			case "issuer-substitution":
				other, token, err := f.a.Enroll(f.owner, f.runner.Scope, time.Now().Add(time.Hour))
				if err != nil {
					t.Fatal(err)
				}
				runnerToken, runnerID = token, other.ID
			case "lease-expired":
				change(func(s *state) {
					l := s.Leases[f.lease.ID]
					l.ExpiresAt = time.Now().Add(-time.Second)
					s.Leases[l.ID] = l
				})
			case "grant-expired":
				change(func(s *state) {
					g := s.Grants[f.grant.ID]
					g.ExpiresAt = time.Now().Add(-time.Second)
					s.Grants[g.ID] = g
				})
			case "runner-expired":
				change(func(s *state) {
					r := s.Sessions[f.runner.ID]
					r.ExpiresAt = time.Now().Add(-time.Second)
					s.Sessions[r.ID] = r
				})
			case "scope-action":
				change(func(s *state) { r := s.Sessions[f.runner.ID]; r.Scope.Actions = []string{"read"}; s.Sessions[r.ID] = r })
			case "scope-resource":
				change(func(s *state) {
					r := s.Sessions[f.worker.ID]
					r.Scope.Resources = []string{"task:other"}
					s.Sessions[r.ID] = r
				})
			case "dirty":
				if err := os.WriteFile(f.at.Observation.Locator+"/dirty", nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			_, err := f.a.CompleteVerification(f.workerToken, runnerToken, runnerID, req, res)
			denied(t, err)
			if err = f.a.transaction(false, false, func(s *state) error {
				if len(s.Evidence) != 0 {
					t.Fatal("refused completion persisted evidence")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestVerificationGrantCannotDelegateOrSubstitute(t *testing.T) {
	f := evidenceSetup(t)
	child, token, err := f.a.Delegate(f.runnerToken, f.runner.ID, f.runner.Scope, time.Now().Add(5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.a.GrantVerification(f.owner, child.ID, "task-a", f.at.Observation.Repository, f.check, time.Now().Add(time.Minute))
	denied(t, err)
	_, _, err = f.a.BeginVerification(f.workerToken, token, child.ID, f.request, CheckDigest(f.check))
	denied(t, err)
	_, err = f.a.GrantVerification(f.workerToken, f.runner.ID, "task-a", f.at.Observation.Repository, f.check, time.Now().Add(time.Minute))
	denied(t, err)
	changed := f.check
	changed.Deadline *= 2
	_, _, err = f.a.BeginVerification(f.workerToken, f.runnerToken, f.runner.ID, f.request, CheckDigest(changed))
	denied(t, err)
}
func TestEvidenceMigrationPreservesAuthorityAndOldWritersRefuse(t *testing.T) {
	f := evidenceSetup(t)
	data, err := os.ReadFile(f.a.path)
	if err != nil {
		t.Fatal(err)
	}
	var old state
	if err = json.Unmarshal(data, &old); err != nil {
		t.Fatal(err)
	}
	old.Version = 3
	old.Revisions = nil
	old.Grants = nil
	old.Evidence = nil
	data, err = json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(f.a.path, data, 0600); err != nil {
		t.Fatal(err)
	}
	_, err = f.a.Authenticate(f.workerToken, f.worker.ID)
	if !errors.Is(err, ErrStorage) {
		t.Fatal(err)
	}
	denied(t, f.a.UpgradeEvidence(f.workerToken))
	if err = f.a.UpgradeEvidence(f.owner); err != nil {
		t.Fatal(err)
	}
	if err = f.a.transaction(false, false, func(s *state) error {
		if !reflect.DeepEqual(s.Sessions, old.Sessions) || !reflect.DeepEqual(s.Attempts, old.Attempts) || !reflect.DeepEqual(s.Leases, old.Leases) || !reflect.DeepEqual(s.Operations, old.Operations) || !reflect.DeepEqual(s.Tasks, old.Tasks) || s.Fence != old.Fence || s.OwnerHash != old.OwnerHash {
			t.Fatal("migration lost authority")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	legacy := *f.a
	legacy.version = 3
	_, err = legacy.Authenticate(f.workerToken, f.worker.ID)
	if !errors.Is(err, ErrStorage) {
		t.Fatal("old writer", err)
	}
	if err = legacy.Revoke(f.owner, f.worker.ID); !errors.Is(err, ErrStorage) {
		t.Fatal("old writer mutation", err)
	}
	if _, err = f.a.Authenticate(f.workerToken, f.worker.ID); err != nil {
		t.Fatal("old writer changed upgraded authority", err)
	}
}

func TestEvidenceNewRevisionSupersedesEligibility(t *testing.T) {
	f := evidenceSetup(t)
	f.begin(t)
	ev, err := f.a.CompleteVerification(f.workerToken, f.runnerToken, f.runner.ID, f.request, f.result())
	if err != nil {
		t.Fatal(err)
	}
	req := f.publish
	req.OperationKey = "next"
	req.ExpectedRevision = 1
	next, err := f.a.PublishAttemptRevision(f.workerToken, f.worker.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	if next.Sequence != 2 || next.ID == ev.Revision.ID {
		t.Fatal("did not advance immutable revision")
	}
	eligible, err := f.a.EligibleVerification(f.workerToken, f.worker.ID, ev.ID)
	if err != nil || eligible {
		t.Fatal("superseded evidence eligible", err)
	}
	if err = f.a.transaction(false, false, func(s *state) error {
		if !reflect.DeepEqual(s.Evidence[ev.ID], ev) {
			t.Fatal("historical evidence rewritten")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
