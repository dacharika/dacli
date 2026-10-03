package sessions

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func lifecycleSetup(t *testing.T, ack bool) (dispatchFixture, DispatchIntent) {
	return lifecycleSetupWithCorrelation(t, ack, true)
}
func lifecycleSetupWithCorrelation(t *testing.T, ack, correlate bool) (dispatchFixture, DispatchIntent) {
	t.Helper()
	f := swarmSetup(t)
	scope := f.s.Scope
	scope.Actions = append(append([]string{}, scope.Actions...), "dispatch.cancel")
	s, token, e := f.a.Enroll(f.owner, scope, time.Now().Add(time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	f.s = s
	f.token = token
	w := swarmReserve(t, f, "wave", swarmPlan(t, f))
	d := w.Dispatches[0]
	if e = f.a.UpgradeDispatchIntents(f.owner); e != nil {
		t.Fatal(e)
	}
	observer := &dispatchAdmission{swarmFixtureAdmission: f.ad}
	f.a.admission = observer
	resources := []string{"project:project", "task:task-b"}
	for _, r := range d.Reservation.Resources {
		resources = append(resources, r.Kind+":"+r.Path)
	}
	bridge, bt, e := f.a.Enroll(f.owner, Scope{Actions: []string{"read", "dispatch.correlate", "dispatch.observe"}, Resources: resources}, time.Now().Add(time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	worker, wt, e := f.a.Enroll(f.owner, Scope{Actions: []string{"read", "attempt.register", "lease.acquire", "lease.renew", "lease.release", "attempt.publish", "evidence.submit", "verification.request", "publication.evaluate"}, Resources: append(resources, "checkout:", "repository:fixture/repo")}, time.Now().Add(time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	at, e := f.a.RegisterAttempt(wt, worker.ID, AttemptRequest{RequestID: "expected-worker", TaskID: "task-a", Checkout: f.dirs[0]})
	if e != nil {
		t.Fatal(e)
	}
	if e = f.a.UpgradeLifecycle(f.owner); e != nil {
		t.Fatal(e)
	}
	if e = f.a.ConfigureLifecycle(f.owner, LifecyclePolicy{SchemaVersion: 1, ProjectID: "project", Revision: "lifecycle-1", Source: "owner-manual-native-host", Freshness: time.Minute, CancellationEnabled: true, CancellationChannel: "owner-manual-native-host"}); e != nil {
		t.Fatal(e)
	}
	req := RequestDispatchIntent{OperationKey: "intent", DispatchID: d.ID, BridgeSessionID: bridge.ID, WorkerSessionID: worker.ID, PromptSpecDigest: "sha256:" + strings.Repeat("a", 64), ExpectedFence: d.Reservation.Fence}
	df := dispatchFixture{f, bridge, worker, bt, wt, at, d, req, observer}
	i := requestIntent(t, df).Intent
	if !correlate {
		return df, i
	}
	i, e = f.a.CorrelateDispatchIntent(bt, bridge.ID, correlation(i, "correlate", "host_correlated"))
	if e != nil {
		t.Fatal(e)
	}
	if ack {
		i, e = f.a.AcknowledgeDispatchIntent(wt, worker.ID, acknowledgement(df, i))
		if e != nil {
			t.Fatal(e)
		}
	}
	return df, i
}
func lifecycleObserve(i DispatchIntent, key, state string, seq uint64) ObserveLifecycleRequest {
	return ObserveLifecycleRequest{OperationKey: key, Binding: lifecycleBinding(i), ExpectedSequence: seq - 1, HostSequence: seq, State: state, Source: "owner-manual-native-host", EvidenceDigest: OutputDigest([]byte(key)), SourceTime: time.Now().UTC()}
}
func lifecycleCancel(t *testing.T, f dispatchFixture, i DispatchIntent) LifecycleCancellationResult {
	t.Helper()
	r, e := f.a.CancelLifecycle(f.token, f.s.ID, CancelLifecycleRequest{OperationKey: "cancel", Binding: lifecycleBinding(i)})
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func TestNativeLifecycleLedgerFreshnessRestartAndTerminalMonotonicity(t *testing.T) {
	f, i := lifecycleSetup(t, true)
	req := lifecycleObserve(i, "running", "running", 1)
	req.SourceTime = time.Now().Add(-2 * time.Minute)
	o, e := f.a.ObserveLifecycle(f.bridgeToken, f.bridge.ID, req)
	if e != nil || o.Sequence != 1 || o.ReceivedAt.Before(req.SourceTime) || o.Assurance != "cooperative-owner-provisioned-bridge" {
		t.Fatalf("bound received observation: %+v %v", o, e)
	}
	status, e := f.a.LifecycleStatus(f.bridgeToken, f.bridge.ID, i.DispatchID)
	if e != nil || status.State != "unknown" || status.Freshness != "stale" || !status.ResourcesRetained {
		t.Fatalf("lost heartbeat invented success: %+v %v", status, e)
	}
	reopened, e := Open(f.a.path, f.a.binding)
	if e != nil {
		t.Fatal(e)
	}
	f.a = reopened
	replay, e := f.a.ObserveLifecycle(f.bridgeToken, f.bridge.ID, req)
	if e != nil || !reflect.DeepEqual(replay, o) {
		t.Fatalf("restart replay diverged: %+v %v", replay, e)
	}
	changed := req
	changed.State = "completed"
	_, e = f.a.ObserveLifecycle(f.bridgeToken, f.bridge.ID, changed)
	denied(t, e)
	old := req
	old.OperationKey = "out-of-order"
	old.ExpectedSequence = 1
	_, e = f.a.ObserveLifecycle(f.bridgeToken, f.bridge.ID, old)
	if !errors.Is(e, ErrDenied) {
		t.Fatalf("out-of-order lifecycle observation accepted: %v", e)
	}
	earlier := req
	earlier.OperationKey = "earlier-source-time"
	earlier.ExpectedSequence = 1
	earlier.HostSequence = 2
	earlier.SourceTime = req.SourceTime.Add(-time.Second)
	if _, e = f.a.ObserveLifecycle(f.bridgeToken, f.bridge.ID, earlier); !errors.Is(e, ErrDenied) {
		t.Fatalf("earlier host source timestamp accepted: %v", e)
	}
	bad := lifecycleObserve(i, "bad-binding", "completed", 2)
	bad.Binding.WorkerSessionID = f.s.ID
	_, e = f.a.ObserveLifecycle(f.bridgeToken, f.bridge.ID, bad)
	denied(t, e)
	terminal := lifecycleObserve(i, "terminal", "completed", 2)
	o, e = f.a.ObserveLifecycle(f.bridgeToken, f.bridge.ID, terminal)
	if e != nil || o.Sequence != 2 {
		t.Fatalf("terminal observation failed: %+v %v", o, e)
	}
	regression := lifecycleObserve(i, "terminal-regression", "running", 3)
	_, e = f.a.ObserveLifecycle(f.bridgeToken, f.bridge.ID, regression)
	if !errors.Is(e, ErrDenied) {
		t.Fatalf("terminal regressed: %v", e)
	}
	status, e = f.a.LifecycleStatus(f.bridgeToken, f.bridge.ID, i.DispatchID)
	if e != nil || status.State != "completed" || len(status.Observations) != 2 {
		t.Fatalf("terminal history missing: %+v %v", status, e)
	}
}
func TestNativeLifecycleCancellationWithdrawsBeforePermitAndRetainsResources(t *testing.T) {
	for _, ack := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-ack", true: "after-ack"}[ack], func(t *testing.T) {
			f, i := lifecycleSetup(t, ack)
			r := lifecycleCancel(t, f, i)
			if !r.HostActionAuthorized || r.Cancellation.State != "requested" {
				t.Fatalf("first durable cancel permit: %+v", r)
			}
			replay, e := f.a.CancelLifecycle(f.token, f.s.ID, CancelLifecycleRequest{OperationKey: "cancel", Binding: lifecycleBinding(i)})
			if e != nil || replay.HostActionAuthorized {
				t.Fatalf("cancel replay reauthorized host: %+v %v", replay, e)
			}
			if e = f.a.transaction(false, false, func(s *state) error {
				if !workerTaskWithdrawn(s, f.worker.ID, "task-a") || workerTaskWithdrawn(s, f.worker.ID, "task-b") || s.Sessions[f.worker.ID].Revoked {
					t.Fatal("withdrawal was missing or revoked unrelated worker authority")
				}
				if ack {
					if s.Leases[i.Lease.ID].Released || currentLease(s, s.Leases[i.Lease.ID], time.Now()) {
						t.Fatal("cancelled writer authority remained current or resources freed")
					}
				} else if s.Dispatches[i.DispatchID].Converted {
					t.Fatal("pre-ack cancellation freed reservation")
				}
				if _, _, e := leaseAuth(s, f.workerToken, f.worker.ID, f.at.ID, "lease.acquire", time.Now()); !errors.Is(e, ErrDenied) {
					t.Fatalf("withdrawn leaseAuth remained valid: %v", e)
				}
				return nil
			}); e != nil {
				t.Fatal(e)
			}
			_, e = f.a.RegisterAttempt(f.workerToken, f.worker.ID, AttemptRequest{RequestID: "cancelled-new", TaskID: "task-a", Checkout: secondCheckout(t, f.at)})
			if !errors.Is(e, ErrDenied) {
				t.Fatalf("cancelled worker registered fresh checkout: %v", e)
			}
			child, ct, e := f.a.Delegate(f.workerToken, f.worker.ID, f.worker.Scope, time.Now().Add(10*time.Minute))
			if e != nil {
				t.Fatal(e)
			}
			_, e = f.a.RegisterAttempt(ct, child.ID, AttemptRequest{RequestID: "child-bypass", TaskID: "task-a", Checkout: f.dirs[0]})
			if !errors.Is(e, ErrDenied) {
				t.Fatalf("cancelled descendant registered task: %v", e)
			}
			_, e = f.a.RegisterAttempt(f.workerToken, f.worker.ID, AttemptRequest{RequestID: "unrelated", TaskID: "task-b", Checkout: f.dirs[1]})
			if e != nil {
				t.Fatalf("unrelated task was revoked: %v", e)
			}
			_, e = f.a.AcquireLease(f.workerToken, f.worker.ID, acquireReq(f.at, "cancelled-acquire", "src/a"))
			denied(t, e)
			if ack {
				_, e = f.a.RenewLease(f.workerToken, f.worker.ID, RenewLeaseRequest{OperationKey: "cancelled-renew", AttemptID: f.at.ID, LeaseID: i.Lease.ID, ExpectedFence: i.Lease.Fence, ExpiresAt: time.Now().Add(10 * time.Minute)})
				denied(t, e)
				_, e = f.a.ReleaseLease(f.workerToken, f.worker.ID, ReleaseLeaseRequest{OperationKey: "cancelled-release", AttemptID: f.at.ID, LeaseID: i.Lease.ID, ExpectedFence: i.Lease.Fence})
				denied(t, e)
			} else {
				_, e = f.a.AcknowledgeDispatchIntent(f.workerToken, f.worker.ID, acknowledgement(f, i))
				if !errors.Is(e, ErrDenied) {
					t.Fatalf("cancelled intent acquired worker authority: %v", e)
				}
			}
			if e = f.a.Revoke(f.owner, f.worker.ID); e != nil {
				t.Fatal(e)
			}
			if e = f.a.Revoke(f.owner, f.s.ID); e != nil {
				t.Fatal(e)
			}
			c, e := f.a.AcknowledgeCancellation(f.bridgeToken, f.bridge.ID, AcknowledgeCancellationRequest{ExpectedSequence: 1, OperationKey: "cancel-ack", Binding: lifecycleBinding(i), State: "acknowledged", EvidenceDigest: OutputDigest([]byte("host acknowledged"))})
			if e != nil || c.State != "acknowledged" {
				t.Fatalf("late cancel acknowledgement refused: %+v %v", c, e)
			}
			req := lifecycleObserve(i, "late-completion", "completed", 1)
			_, e = f.a.ObserveLifecycle(f.bridgeToken, f.bridge.ID, req)
			if e != nil {
				t.Fatalf("late assigned bridge completion refused: %v", e)
			}
			status, e := f.a.LifecycleStatus(f.bridgeToken, f.bridge.ID, i.DispatchID)
			if e != nil || status.State != "completed" || status.Cancellation.State != "acknowledged" || !status.ResourcesRetained {
				t.Fatalf("late completion restored authority or dropped cancellation: %+v %v", status, e)
			}
			if e = f.a.Revoke(f.owner, f.bridge.ID); e != nil {
				t.Fatal(e)
			}
			_, e = f.a.ObserveLifecycle(f.bridgeToken, f.bridge.ID, req)
			if !errors.Is(e, ErrDenied) {
				t.Fatalf("revoked bridge report admitted: %v", e)
			}
		})
	}
}
func TestNativeLifecycleUnsupportedCancellationAndBindingRefusal(t *testing.T) {
	f, i := lifecycleSetup(t, false)
	p, e := f.a.OwnerLifecyclePolicy(f.owner, "project")
	if e != nil {
		t.Fatal(e)
	}
	p.Revision = "unsupported"
	p.CancellationChannel = "unsupported"
	if e = f.a.ConfigureLifecycle(f.owner, p); e != nil {
		t.Fatal(e)
	}
	bad := lifecycleBinding(i)
	bad.ReservationFence++
	_, e = f.a.CancelLifecycle(f.token, f.s.ID, CancelLifecycleRequest{OperationKey: "bad", Binding: bad})
	denied(t, e)
	r := lifecycleCancel(t, f, i)
	if r.HostActionAuthorized || r.Cancellation.State != "unsupported" {
		t.Fatalf("unsupported cancellation claimed host action: %+v", r)
	}
	_, e = f.a.AcknowledgeCancellation(f.bridgeToken, f.bridge.ID, AcknowledgeCancellationRequest{ExpectedSequence: 1, OperationKey: "false-ack", Binding: lifecycleBinding(i), State: "acknowledged", EvidenceDigest: OutputDigest([]byte("unproved"))})
	denied(t, e)
}
func TestNativeLifecycleSchemaUpgradePreservesHistoryAndOldWritersRefuse(t *testing.T) {
	f := dispatchSetup(t)
	i := requestIntent(t, f).Intent
	before, _ := os.ReadFile(f.a.path)
	var old state
	if e := json.Unmarshal(before, &old); e != nil {
		t.Fatal(e)
	}
	denied(t, f.a.UpgradeLifecycle("forged"))
	if e := f.a.UpgradeLifecycle(f.owner); e != nil {
		t.Fatal(e)
	}
	for _, version := range []int{6, 7, 8} {
		a := *f.a
		a.version = version
		if _, e := a.Authenticate(f.workerToken, f.worker.ID); e != nil {
			t.Fatalf("current schema%d reader failed: %v", version, e)
		}
	}
	if e := f.a.transaction(false, false, func(s *state) error {
		current := *s
		current.Version = 8
		current.LifecyclePolicyHistory = nil
		current.LifecyclePolicies = nil
		current.LifecycleObservations = nil
		current.LifecycleCancellations = nil
		current.LifecycleOperations = nil
		if !reflect.DeepEqual(current, old) {
			t.Fatal("schema upgrade altered historical authority")
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	if replay, e := f.a.RequestDispatchIntent(f.token, f.s.ID, f.req); e != nil || replay.HostActionAuthorized || replay.Intent.ID != i.ID {
		t.Fatalf("schema9 dispatch history incompatible: %+v %v", replay, e)
	}
	data := mustReadLifecycle(t, f.a.path)
	var fields map[string]json.RawMessage
	if e := json.Unmarshal(data, &fields); e != nil {
		t.Fatal(e)
	}
	delete(fields, "LifecycleOperations")
	broken, _ := json.Marshal(fields)
	if e := os.WriteFile(f.a.path, broken, 0600); e != nil {
		t.Fatal(e)
	}
	_, e := f.a.Authenticate(f.workerToken, f.worker.ID)
	if !errors.Is(e, ErrStorage) {
		t.Fatalf("missing schema9 map normalized: %v", e)
	}
}
func mustReadLifecycle(t *testing.T, path string) []byte {
	t.Helper()
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	return b
}

// Isolated fixtures inject the same durable withdrawal fact generated by the
// end-to-end cancellation tests, so each existing consumer is tested directly.
func lifecycleWithdrawFixture(t *testing.T, a *Authority, owner, worker, task string) {
	t.Helper()
	if e := a.UpgradeSwarms(owner); e != nil {
		t.Fatal(e)
	}
	if e := a.UpgradeDispatchIntents(owner); e != nil {
		t.Fatal(e)
	}
	if e := a.UpgradeLifecycle(owner); e != nil {
		t.Fatal(e)
	}
	if e := a.transaction(false, true, func(s *state) error {
		s.LifecycleCancellations["fixture-dispatch"] = LifecycleCancellation{Binding: LifecycleBinding{DispatchID: "fixture-dispatch", WorkerSessionID: worker, TaskID: task}, State: "requested"}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
}
func TestNativeLifecycleWithdrawalReachesEvidenceVerificationReviewAndPublication(t *testing.T) {
	t.Run("verification", func(t *testing.T) {
		f := evidenceSetup(t)
		f.begin(t)
		lifecycleWithdrawFixture(t, f.a, f.owner, f.worker.ID, "task-a")
		_, e := f.a.PublishAttemptRevision(f.workerToken, f.worker.ID, PublishRevisionRequest{OperationKey: "late-revision", AttemptID: f.at.ID, LeaseID: f.lease.ID, ExpectedFence: f.lease.Fence, ExpectedRevision: 1})
		denied(t, e)
		_, e = f.a.SubmitEvidence(f.workerToken, f.worker.ID, EvidenceRequest{OperationKey: "late-self-report", AttemptID: f.at.ID, LeaseID: f.lease.ID, RevisionID: f.revision.ID, ExpectedFence: f.lease.Fence, CheckID: "check", OutputDigest: OutputDigest([]byte("late"))})
		denied(t, e)
		late := f.request
		late.OperationKey = "late-run"
		_, _, e = f.a.BeginVerification(f.workerToken, f.runnerToken, f.runner.ID, late, CheckDigest(f.check))
		if !errors.Is(e, ErrDenied) {
			t.Fatalf("withdrawn verification began: %v", e)
		}
		_, e = f.a.CompleteVerification(f.workerToken, f.runnerToken, f.runner.ID, f.request, f.result())
		if !errors.Is(e, ErrDenied) {
			t.Fatalf("withdrawn verification completed: %v", e)
		}
	})
	t.Run("review-and-eligibility", func(t *testing.T) {
		f := nativeReviewSetup(t)
		r := f.propose(t)
		lifecycleWithdrawFixture(t, f.a, f.owner, f.worker.ID, "task-a")
		eligible, e := f.a.EligibleVerification(f.workerToken, f.worker.ID, f.evidence.ID)
		if e == nil || eligible {
			t.Fatalf("withdrawn trusted evidence eligible: %v %v", eligible, e)
		}
		verdict, e := f.a.EvaluateReview(f.token, f.reviewer.ID, r.ID)
		if e != nil || verdict.Eligible {
			t.Fatalf("withdrawn native review eligible: %+v %v", verdict, e)
		}
		req := f.req
		req.OperationKey = "late-review"
		_, e = f.a.ProposeReview(f.token, f.reviewer.ID, req)
		if !errors.Is(e, ErrDenied) {
			t.Fatalf("withdrawn worker review proposed: %v", e)
		}
		if e = f.a.transaction(false, false, func(s *state) error {
			if _, e := currentRevision(s, f.at, f.revision.ID, f.lease.ID, f.lease.Fence); !errors.Is(e, ErrDenied) {
				t.Fatalf("withdrawn revision remained current: %v", e)
			}
			if e := reviewWorker(s, f.at, f.req); !errors.Is(e, ErrDenied) {
				t.Fatalf("withdrawn reviewWorker remained current: %v", e)
			}
			return nil
		}); e != nil {
			t.Fatal(e)
		}
	})
	t.Run("publication", func(t *testing.T) {
		f := publicationSetup(t, false)
		d := f.decision(t)
		lifecycleWithdrawFixture(t, f.a, f.owner, f.worker.ID, "task-a")
		req := f.req
		req.OperationKey = "late-decision"
		_, e := f.a.EvaluatePublication(f.token, f.worker.ID, req)
		if !errors.Is(e, ErrDenied) {
			t.Fatalf("withdrawn publication decision admitted: %v", e)
		}
		_, e = f.a.ConsumePublication(f.owner, consumption(d))
		if !errors.Is(e, ErrDenied) {
			t.Fatalf("withdrawn owner publication consumed: %v", e)
		}
		if e = f.a.transaction(false, false, func(s *state) error {
			if _, _, _, e := publicationCurrent(s, f.worker.ID, f.req); !errors.Is(e, ErrDenied) {
				t.Fatalf("withdrawn publicationCurrent remained valid: %v", e)
			}
			return nil
		}); e != nil {
			t.Fatal(e)
		}
	})
}

func TestNativeLifecycleFailedPersistenceReturnsNoCancelPermitOrObservation(t *testing.T) {
	f, i := lifecycleSetup(t, true)
	before := mustReadLifecycle(t, f.a.path)
	dir := filepath.Dir(f.a.path)
	f.a.persistOverride = func(data []byte) error {
		if e := os.Chmod(dir, 0500); e != nil {
			t.Fatal(e)
		}
		defer func() { _ = os.Chmod(dir, 0700) }()
		return f.a.persist(data) // actual CreateTemp failure after callback built result
	}
	result, e := f.a.CancelLifecycle(f.token, f.s.ID, CancelLifecycleRequest{OperationKey: "failed-cancel", Binding: lifecycleBinding(i)})
	if e == nil {
		t.Fatal("cancel storage failure fixture unexpectedly writable")
	}
	if result.HostActionAuthorized || result.Cancellation.OperationKey != "" {
		t.Fatalf("uncommitted cancellation permit leaked: %+v %v", result, e)
	}
	observation, e := f.a.ObserveLifecycle(f.bridgeToken, f.bridge.ID, lifecycleObserve(i, "failed-observation", "running", 1))
	if e == nil {
		t.Fatal("observation storage failure fixture unexpectedly writable")
	}
	if observation.Sequence != 0 {
		t.Fatalf("uncommitted observation leaked: %+v %v", observation, e)
	}
	if !reflect.DeepEqual(before, mustReadLifecycle(t, f.a.path)) {
		t.Fatal("failed lifecycle storage changed authority")
	}
	f.a.persistOverride = nil
}

func TestNativeLifecycleUpgradePreservesVerificationReviewPublicationHistory(t *testing.T) {
	f := nativeReviewSetup(t)
	review := f.propose(t)
	if _, e := f.a.SetPublicationPolicy(f.owner, SetPublicationPolicyRequest{TaskID: "task-a", Repository: "fixture/repo", Visibility: "public"}); e != nil {
		t.Fatal(e)
	}
	if e := f.a.UpgradeSwarms(f.owner); e != nil {
		t.Fatal(e)
	}
	if e := f.a.UpgradeDispatchIntents(f.owner); e != nil {
		t.Fatal(e)
	}
	data := mustReadLifecycle(t, f.a.path)
	var before state
	if e := json.Unmarshal(data, &before); e != nil {
		t.Fatal(e)
	}
	if len(before.Evidence) != 1 || len(before.Reviews) != 1 || len(before.PublicationPolicies) != 1 || len(before.Revisions) != 1 {
		t.Fatal("upgrade preservation fixture lacks history")
	}
	var fields map[string]json.RawMessage
	if e := json.Unmarshal(data, &fields); e != nil {
		t.Fatal(e)
	}
	fields["LifecyclePolicies"] = json.RawMessage(`{}`)
	malformed, _ := json.Marshal(fields)
	if e := os.WriteFile(f.a.path, malformed, 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := f.a.Authenticate(f.workerToken, f.worker.ID); !errors.Is(e, ErrStorage) {
		t.Fatalf("schema8 accepted unversioned lifecycle records: %v", e)
	}
	if e := os.WriteFile(f.a.path, data, 0600); e != nil {
		t.Fatal(e)
	}
	if e := f.a.UpgradeLifecycle(f.owner); e != nil {
		t.Fatal(e)
	}
	if e := f.a.transaction(false, false, func(s *state) error {
		old := *s
		old.Version = 8
		old.LifecyclePolicyHistory = nil
		old.LifecyclePolicies = nil
		old.LifecycleObservations = nil
		old.LifecycleCancellations = nil
		old.LifecycleOperations = nil
		if !reflect.DeepEqual(before, old) {
			t.Fatal("lifecycle migration altered verification, review or publication history")
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	reader, e := OpenReviewAuthority(f.a.path, f.a.binding, f.observer)
	if e != nil {
		t.Fatal(e)
	}
	eligibility, e := reader.EvaluateReview(f.token, f.reviewer.ID, review.ID)
	if e != nil || !eligibility.Eligible {
		t.Fatalf("schema9 current reviewer lost unchanged authority: %+v %v", eligibility, e)
	}
}

func TestNativeLifecyclePreHostCancellationCannotReopenAdmission(t *testing.T) {
	f, i := lifecycleSetupWithCorrelation(t, false, false)
	r := lifecycleCancel(t, f, i)
	if r.HostActionAuthorized || r.Cancellation.State != "manual_handoff_required" {
		t.Fatalf("missing host received cancel action: %+v", r)
	}
	_, e := f.a.CorrelateDispatchIntent(f.bridgeToken, f.bridge.ID, correlation(i, "after-cancel", "host_correlated"))
	if !errors.Is(e, ErrDenied) {
		t.Fatalf("cancelled intent correlated new host: %v", e)
	}
	_, e = f.a.AdmitDispatch(f.token, f.s.ID, AdmitDispatchRequest{DispatchID: i.DispatchID, ExpectedFence: i.Reservation.Reservation.Fence})
	if !errors.Is(e, ErrDenied) {
		t.Fatalf("cancelled reservation re-admitted: %v", e)
	}
	_, e = f.a.HandoffDispatch(f.workerToken, f.worker.ID, HandoffDispatchRequest{OperationKey: "raw-cancelled-handoff", DispatchID: i.DispatchID, AttemptID: f.at.ID, ExpectedFence: i.Reservation.Reservation.Fence})
	denied(t, e)
}
func TestNativeLifecycleCancellationAcknowledgementSequenceAndReplay(t *testing.T) {
	f, i := lifecycleSetup(t, true)
	lifecycleCancel(t, f, i)
	req := AcknowledgeCancellationRequest{ExpectedSequence: 1, OperationKey: "unknown-cancel", Binding: lifecycleBinding(i), State: "dispatch_unknown", EvidenceDigest: OutputDigest([]byte("lost acknowledgement"))}
	c, e := f.a.AcknowledgeCancellation(f.bridgeToken, f.bridge.ID, req)
	if e != nil || c.Sequence != 2 || c.State != "dispatch_unknown" {
		t.Fatalf("cancel observation: %+v %v", c, e)
	}
	old := req
	old.OperationKey = "old-cancel-ack"
	old.State = "acknowledged"
	_, e = f.a.AcknowledgeCancellation(f.bridgeToken, f.bridge.ID, old)
	if !errors.Is(e, ErrDenied) {
		t.Fatalf("older cancellation sequence accepted: %v", e)
	}
	next := req
	next.OperationKey = "confirmed-cancel"
	next.ExpectedSequence = 2
	next.State = "acknowledged"
	c, e = f.a.AcknowledgeCancellation(f.bridgeToken, f.bridge.ID, next)
	if e != nil || c.Sequence != 3 {
		t.Fatal(c, e)
	}
	replay, e := f.a.AcknowledgeCancellation(f.bridgeToken, f.bridge.ID, req)
	if e != nil || replay.Sequence != 2 || replay.State != "dispatch_unknown" {
		t.Fatalf("historical cancel receipt changed: %+v %v", replay, e)
	}
	changed := req
	changed.EvidenceDigest = OutputDigest([]byte("changed"))
	_, e = f.a.AcknowledgeCancellation(f.bridgeToken, f.bridge.ID, changed)
	denied(t, e)
	next.OperationKey = "regressed-ack"
	next.ExpectedSequence = 3
	next.State = "dispatch_unknown"
	_, e = f.a.AcknowledgeCancellation(f.bridgeToken, f.bridge.ID, next)
	denied(t, e)
	status, e := f.a.LifecycleStatus(f.bridgeToken, f.bridge.ID, i.DispatchID)
	if e != nil || status.State != "unknown" || status.Freshness != "missing" || status.Cancellation.State != "acknowledged" {
		t.Fatalf("acknowledgement invented terminal result: %+v %v", status, e)
	}
}

func TestNativeLifecyclePreAckObservationReplayAfterWorkerAcknowledgement(t *testing.T) {
	f, i := lifecycleSetup(t, false)
	req := lifecycleObserve(i, "pre-ack-running", "running", 1)
	observed, e := f.a.ObserveLifecycle(f.bridgeToken, f.bridge.ID, req)
	if e != nil {
		t.Fatal(e)
	}
	acknowledged, e := f.a.AcknowledgeDispatchIntent(f.workerToken, f.worker.ID, acknowledgement(f, i))
	if e != nil || acknowledged.Lease == nil {
		t.Fatal(acknowledged, e)
	}
	reopened, e := Open(f.a.path, f.a.binding)
	if e != nil {
		t.Fatal(e)
	}
	replay, e := reopened.ObserveLifecycle(f.bridgeToken, f.bridge.ID, req)
	if e != nil || !reflect.DeepEqual(replay, observed) {
		t.Fatalf("pre-ack historical observation replay refused after worker acknowledgement: %+v %v", replay, e)
	}
	changed := req
	changed.Binding.LeaseID = acknowledged.Lease.ID
	_, e = reopened.ObserveLifecycle(f.bridgeToken, f.bridge.ID, changed)
	denied(t, e)
	fresh := lifecycleObserve(acknowledged, "post-ack-running", "running", 2)
	_, e = reopened.ObserveLifecycle(f.bridgeToken, f.bridge.ID, fresh)
	if e != nil {
		t.Fatalf("fresh post-ack exact binding refused: %v", e)
	}
}

func TestNativeLifecycleTerminalBeforeAckRefusesFreshHandoff(t *testing.T) {
	f, i := lifecycleSetup(t, false)
	if _, e := f.a.ObserveLifecycle(f.bridgeToken, f.bridge.ID, lifecycleObserve(i, "pre-ack-terminal", "completed", 1)); e != nil {
		t.Fatal(e)
	}
	_, e := f.a.AcknowledgeDispatchIntent(f.workerToken, f.worker.ID, acknowledgement(f, i))
	if !errors.Is(e, ErrDenied) {
		t.Fatalf("terminal host execution received new worker lease: %v", e)
	}
	_, e = f.a.AdmitDispatch(f.token, f.s.ID, AdmitDispatchRequest{DispatchID: i.DispatchID, ExpectedFence: i.Reservation.Reservation.Fence})
	if !errors.Is(e, ErrDenied) {
		t.Fatalf("terminal host reservation re-admitted: %v", e)
	}
	if e = f.a.transaction(false, false, func(s *state) error {
		if s.Dispatches[i.DispatchID].Converted || len(s.Leases) != 0 {
			t.Fatal("terminal refusal did not retain reservation")
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
}
func TestNativeLifecyclePolicyRevisionHistoryIsImmutable(t *testing.T) {
	f, _ := lifecycleSetup(t, false)
	original, e := f.a.OwnerLifecyclePolicy(f.owner, "project")
	if e != nil {
		t.Fatal(e)
	}
	next := original
	next.Revision = "lifecycle-v2"
	next.Freshness = 2 * time.Minute
	if e = f.a.ConfigureLifecycle(f.owner, next); e != nil {
		t.Fatal(e)
	}
	changed := original
	changed.Freshness = 3 * time.Minute
	if e = f.a.ConfigureLifecycle(f.owner, changed); !errors.Is(e, ErrDenied) {
		t.Fatalf("historical lifecycle revision reused with altered policy: %v", e)
	}
	if e = f.a.ConfigureLifecycle(f.owner, original); e != nil {
		t.Fatal(e)
	}
	current, e := f.a.OwnerLifecyclePolicy(f.owner, "project")
	if e != nil || !reflect.DeepEqual(current, next) {
		t.Fatalf("historical policy replay silently reset current policy: %+v %v", current, e)
	}
}
