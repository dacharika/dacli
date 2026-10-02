package sessions

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

type testReviewObserver struct{ observation ReviewObservation }

func (o *testReviewObserver) ObserveReview(string, string, string) (ReviewObservation, error) {
	return o.observation, nil
}

type nativeReviewFixture struct {
	evidenceFixture
	reviewer    Session
	token       string
	observer    *testReviewObserver
	reviewGrant ReviewGrant
	evidence    Evidence
	req         ReviewRequest
}

func nativeReviewSetup(t *testing.T) nativeReviewFixture {
	t.Helper()
	f := evidenceSetup(t)
	f.begin(t)
	ev, err := f.a.CompleteVerification(f.workerToken, f.runnerToken, f.runner.ID, f.request, f.result())
	if err != nil {
		t.Fatal(err)
	}
	observer := &testReviewObserver{ReviewObservation{OutputDigest([]byte("checklist")), "policy-1", []RequiredCheck{{f.check.ID, CheckDigest(f.check)}}}}
	f.a.reviewObserver = observer
	reviewer, token, err := f.a.Enroll(f.owner, Scope{[]string{"review.propose", "review.evaluate"}, []string{"task:task-a"}}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	g, err := f.a.GrantReview(f.owner, ReviewGrantRequest{reviewer.ID, f.worker.ID, "task-a", f.at.Observation.Repository, time.Now().Add(10 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	req := ReviewRequest{OperationKey: "review", GrantID: g.ID, AttemptID: f.at.ID, LeaseID: f.lease.ID, RevisionID: f.revision.ID, ExpectedFence: f.lease.Fence, Outcome: "approve", Rationale: "checked exact revision", EvidenceIDs: []string{ev.ID}}
	return nativeReviewFixture{f, reviewer, token, observer, g, ev, req}
}
func (f nativeReviewFixture) propose(t *testing.T) Review {
	t.Helper()
	p, e := f.a.ProposeReview(f.token, f.reviewer.ID, f.req)
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func (f nativeReviewFixture) eligible(t *testing.T, id string) ReviewEligibility {
	t.Helper()
	e, err := f.a.EvaluateReview(f.token, f.reviewer.ID, id)
	if err != nil {
		t.Fatal(err)
	}
	return e
}
func TestNativeReviewRoundtripRetryAndChanges(t *testing.T) {
	f := nativeReviewSetup(t)
	p := f.propose(t)
	if e := f.eligible(t, p.ID); !e.Eligible || e.Reason != ReviewEligible || e.Assurance != "cooperative-credential-separation" {
		t.Fatal(e)
	}
	a, err := OpenReviewAuthority(f.a.path, f.a.binding, f.observer)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := a.ProposeReview(f.token, f.reviewer.ID, f.req)
	if err != nil || !reflect.DeepEqual(retry, p) {
		t.Fatalf("retry: %+v %v", retry, err)
	}
	changed := f.req
	changed.Rationale = "changed"
	_, err = a.ProposeReview(f.token, f.reviewer.ID, changed)
	denied(t, err)
	changed.OperationKey = "changes"
	changed.Outcome = "request-changes"
	changes, err := a.ProposeReview(f.token, f.reviewer.ID, changed)
	if err != nil {
		t.Fatal(err)
	}
	if e := f.eligible(t, changes.ID); e.Eligible || e.Reason != ReviewChangesRequested {
		t.Fatal(e)
	}
	if err = f.a.RevokeReview(f.owner, f.reviewGrant.ID); err != nil {
		t.Fatal(err)
	}
	retry, err = a.ProposeReview(f.token, f.reviewer.ID, f.req)
	if err != nil || !reflect.DeepEqual(retry, p) {
		t.Fatal("historical receipt", err)
	}
	if e := f.eligible(t, p.ID); e.Eligible {
		t.Fatal("receipt promoted to permission")
	}
}
func TestNativeReviewIndependenceGuard(t *testing.T) {
	for _, kind := range []string{"same-worker", "delegated-reviewer", "implementation-scope", "forged-owner", "sibling-family"} {
		t.Run(kind, func(t *testing.T) {
			f := nativeReviewSetup(t)
			req := ReviewGrantRequest{f.reviewer.ID, f.worker.ID, "task-a", f.at.Observation.Repository, time.Now().Add(time.Minute)}
			owner := f.owner
			switch kind {
			case "same-worker":
				req.ReviewerSession = f.worker.ID
			case "delegated-reviewer":
				child, _, err := f.a.Delegate(f.token, f.reviewer.ID, f.reviewer.Scope, time.Now().Add(time.Minute))
				if err != nil {
					t.Fatal(err)
				}
				req.ReviewerSession = child.ID
			case "implementation-scope":
				scope := f.reviewer.Scope
				scope.Actions = append(append([]string(nil), scope.Actions...), "attempt.register")
				r, _, err := f.a.Enroll(f.owner, scope, time.Now().Add(time.Hour))
				if err != nil {
					t.Fatal(err)
				}
				req.ReviewerSession = r.ID
			case "forged-owner":
				owner = f.workerToken
			case "sibling-family":
				// Reviewer root equals the delegated worker's family, despite distinct IDs.
				child, _, err := f.a.Delegate(f.token, f.reviewer.ID, f.reviewer.Scope, time.Now().Add(time.Minute))
				if err != nil {
					t.Fatal(err)
				}
				req.WorkerSession = child.ID
			}
			_, err := f.a.GrantReview(owner, req)
			denied(t, err)
		})
	}
}
func TestNativeReviewRevisionGuard(t *testing.T) {
	for _, kind := range []string{"new-publication", "advanced-head", "dirty", "replaced", "fence"} {
		t.Run(kind, func(t *testing.T) {
			f := nativeReviewSetup(t)
			p := f.propose(t)
			switch kind {
			case "new-publication":
				req := f.publish
				req.OperationKey = "next"
				req.ExpectedRevision = 1
				if _, err := f.a.PublishAttemptRevision(f.workerToken, f.worker.ID, req); err != nil {
					t.Fatal(err)
				}
			case "advanced-head":
				gitTest(t, f.at.Observation.Locator, "-c", "user.name=t", "-c", "user.email=t@t.test", "commit", "--allow-empty", "-m", "next")
			case "dirty":
				if err := os.WriteFile(filepath.Join(f.at.Observation.Locator, "dirty"), []byte("new"), 0600); err != nil {
					t.Fatal(err)
				}
			case "replaced":
				if err := os.Rename(f.at.Observation.Locator, f.at.Observation.Locator+"-old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(f.at.Observation.Locator, 0700); err != nil {
					t.Fatal(err)
				}
			case "fence":
				if err := f.a.transaction(false, true, func(s *state) error { l := s.Leases[f.lease.ID]; l.Fence++; s.Leases[l.ID] = l; return nil }); err != nil {
					t.Fatal(err)
				}
			}
			if e := f.eligible(t, p.ID); e.Eligible {
				t.Fatal("stale/dirty revision remained eligible")
			}
			req := f.req
			req.OperationKey = "new-proposal"
			_, err := f.a.ProposeReview(f.token, f.reviewer.ID, req)
			denied(t, err)
		})
	}
}
func TestNativeReviewProvenanceGuard(t *testing.T) {
	for _, kind := range []string{"self-report", "failed", "wrong-command", "wrong-check", "missing", "wrong-revision", "runner-revoked", "runner-grant-revoked"} {
		t.Run(kind, func(t *testing.T) {
			f := nativeReviewSetup(t)
			if err := f.a.transaction(false, true, func(s *state) error {
				ev := s.Evidence[f.evidence.ID]
				switch kind {
				case "self-report":
					ev.Provenance = "self-reported" // Isolates the provenance guard even with all other fields supplied.
				case "failed":
					ev.ExitCode = 1
				case "wrong-command":
					ev.CommandDigest = hash("different")
				case "wrong-check":
					ev.CheckID = "another"
				case "missing":
					delete(s.Evidence, ev.ID)
					return nil
				case "wrong-revision":
					ev.Revision.ID = "another"
				case "runner-revoked":
					r := s.Sessions[f.runner.ID]
					r.Revoked = true
					s.Sessions[r.ID] = r
				case "runner-grant-revoked":
					g := s.Grants[f.grant.ID]
					g.Revoked = true
					s.Grants[g.ID] = g
				}
				s.Evidence[ev.ID] = ev
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			_, err := f.a.ProposeReview(f.token, f.reviewer.ID, f.req)
			denied(t, err)
		})
	}
}
func TestNativeReviewFreshAuthorityAndPolicy(t *testing.T) {
	for _, kind := range []string{"reviewer-revoked", "worker-revoked", "runner-revoked", "grant-revoked", "reviewer-expired", "worker-expired", "runner-expired", "grant-expired", "runner-grant-expired", "lease-expired", "lease-released", "checklist", "policy", "requirements", "missing-observer", "implementation-authority"} {
		t.Run(kind, func(t *testing.T) {
			f := nativeReviewSetup(t)
			p := f.propose(t)
			switch kind {
			case "checklist":
				f.observer.observation.ChecklistDigest = OutputDigest([]byte("changed"))
			case "policy":
				f.observer.observation.PolicyRevision = "policy-2"
			case "requirements":
				f.observer.observation.Checks[0].CommandDigest = hash("other")
			case "missing-observer":
				f.a.reviewObserver = nil
			default:
				if err := f.a.transaction(false, true, func(s *state) error {
					switch kind {
					case "reviewer-revoked", "worker-revoked", "runner-revoked", "reviewer-expired", "worker-expired", "runner-expired":
						id := f.reviewer.ID
						if kind == "worker-revoked" || kind == "worker-expired" {
							id = f.worker.ID
						}
						if kind == "runner-revoked" || kind == "runner-expired" {
							id = f.runner.ID
						}
						r := s.Sessions[id]
						if kind == "reviewer-expired" || kind == "worker-expired" || kind == "runner-expired" {
							r.ExpiresAt = time.Now().Add(-time.Second)
						} else {
							r.Revoked = true
						}
						s.Sessions[id] = r
					case "grant-revoked", "grant-expired":
						g := s.ReviewGrants[f.reviewGrant.ID]
						g.Revoked = kind == "grant-revoked"
						if kind == "grant-expired" {
							g.ExpiresAt = time.Now().Add(-time.Second)
						}
						s.ReviewGrants[g.ID] = g
					case "runner-grant-expired":
						g := s.Grants[f.grant.ID]
						g.ExpiresAt = time.Now().Add(-time.Second)
						s.Grants[g.ID] = g
					case "lease-expired", "lease-released":
						l := s.Leases[f.lease.ID]
						if kind == "lease-expired" {
							l.ExpiresAt = time.Now().Add(-time.Second)
						} else {
							l.Released = true
						}
						s.Leases[l.ID] = l
					case "implementation-authority":
						r := s.Sessions[f.reviewer.ID]
						r.Scope.Actions = append(r.Scope.Actions, "attempt.register")
						s.Sessions[r.ID] = r
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			e, err := f.a.EvaluateReview(f.token, f.reviewer.ID, p.ID)
			if e.Eligible {
				t.Fatalf("%s remained eligible: %v", kind, err)
			}
			if err != nil && !errors.Is(err, ErrDenied) {
				t.Fatal(err)
			}
		})
	}
}
func TestNativeReviewMigrationPreservesV4AndRefusesOldWriter(t *testing.T) {
	f := nativeReviewSetup(t)
	data, err := os.ReadFile(f.a.path)
	if err != nil {
		t.Fatal(err)
	}
	var old state
	if err = json.Unmarshal(data, &old); err != nil {
		t.Fatal(err)
	}
	old.Version = 4
	old.ReviewGrants = nil
	old.Reviews = nil
	data, err = json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(f.a.path, data, 0600); err != nil {
		t.Fatal(err)
	}
	denied(t, f.a.UpgradeReviews(f.workerToken))
	if err = f.a.UpgradeReviews(f.owner); err != nil {
		t.Fatal(err)
	}
	if err = f.a.transaction(false, false, func(s *state) error {
		copy := *s
		copy.Version = 4
		copy.ReviewGrants = nil
		copy.Reviews = nil
		if !reflect.DeepEqual(copy, old) {
			t.Fatal("migration changed v4 history")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	legacy := *f.a
	legacy.version = 4
	if err = legacy.Revoke(f.owner, f.worker.ID); !errors.Is(err, ErrStorage) {
		t.Fatal("old writer", err)
	}
}

type blockedReviewObserver struct {
	*testReviewObserver
	entered chan struct{}
	release chan struct{}
}

func (o *blockedReviewObserver) ObserveReview(w, p, t string) (ReviewObservation, error) {
	close(o.entered)
	<-o.release
	return o.testReviewObserver.ObserveReview(w, p, t)
}
func TestNativeReviewProcessSerialization(t *testing.T) {
	if path := os.Getenv("NATIVE_REVIEW_PROCESS_PATH"); path != "" {
		a, err := Open(path, Binding{"workspace", "installation"})
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(os.Getenv("NATIVE_REVIEW_PROCESS_STARTED"), []byte("started"), 0600); err != nil {
			t.Fatal(err)
		}
		if os.Getenv("NATIVE_REVIEW_PROCESS_MODE") == "revoke" {
			err = a.Revoke(os.Getenv("NATIVE_REVIEW_PROCESS_TOKEN"), os.Getenv("NATIVE_REVIEW_PROCESS_SESSION"))
		} else {
			var req PublishRevisionRequest
			if err = json.Unmarshal([]byte(os.Getenv("NATIVE_REVIEW_PROCESS_REQUEST")), &req); err != nil {
				t.Fatal(err)
			}
			_, err = a.PublishAttemptRevision(os.Getenv("NATIVE_REVIEW_PROCESS_TOKEN"), os.Getenv("NATIVE_REVIEW_PROCESS_SESSION"), req)
		}
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	for _, mode := range []string{"revoke", "revision"} {
		t.Run(mode, func(t *testing.T) {
			f := nativeReviewSetup(t)
			block := &blockedReviewObserver{f.observer, make(chan struct{}), make(chan struct{})}
			f.a.reviewObserver = block
			type proposalResult struct {
				p   Review
				err error
			}
			done := make(chan proposalResult, 1)
			go func() { p, err := f.a.ProposeReview(f.token, f.reviewer.ID, f.req); done <- proposalResult{p, err} }()
			<-block.entered
			started := filepath.Join(t.TempDir(), "started")
			token, session := f.owner, f.worker.ID
			req := f.publish
			req.OperationKey = "process-revision"
			req.ExpectedRevision = 1
			raw, err := json.Marshal(req)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "revision" {
				token = f.workerToken
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestNativeReviewProcessSerialization$")
			cmd.Env = append(os.Environ(), "NATIVE_REVIEW_PROCESS_PATH="+f.a.path, "NATIVE_REVIEW_PROCESS_STARTED="+started, "NATIVE_REVIEW_PROCESS_MODE="+mode, "NATIVE_REVIEW_PROCESS_TOKEN="+token, "NATIVE_REVIEW_PROCESS_SESSION="+session, "NATIVE_REVIEW_PROCESS_REQUEST="+string(raw))
			if err = cmd.Start(); err != nil {
				close(block.release)
				t.Fatal(err)
			}
			deadline := time.Now().Add(3 * time.Second)
			for {
				if _, err = os.Stat(started); err == nil {
					break
				}
				if time.Now().After(deadline) {
					close(block.release)
					_ = cmd.Wait()
					t.Fatal("child did not start")
				}
				time.Sleep(5 * time.Millisecond)
			}
			close(block.release)
			result := <-done
			if result.err != nil {
				t.Fatal(result.err)
			}
			if err = cmd.Wait(); err != nil {
				t.Fatal(err)
			}
			f.a.reviewObserver = f.observer
			if e := f.eligible(t, result.p.ID); e.Eligible {
				t.Fatal("cross-process update failed to invalidate review")
			}
			retry := f.req
			retry.OperationKey = "after-process"
			_, err = f.a.ProposeReview(f.token, f.reviewer.ID, retry)
			denied(t, err)
		})
	}
}
