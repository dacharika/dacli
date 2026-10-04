package sessions

import (
	"errors"
	"github.com/mlnomadpy/dacli/internal/governance/checkout"
	"reflect"
	"testing"
	"time"
)

func acceptanceKernelSetup(t *testing.T) (nativeReviewFixture, NativeAcceptanceInput) {
	t.Helper()
	f := nativeReviewSetup(t)
	// Enroll the implementing root with all intended exact task scopes before
	// registering its reviewed attempt. No stored grant is widened.
	if _, e := f.a.ReleaseLease(f.workerToken, f.worker.ID, ReleaseLeaseRequest{"setup-release", f.at.ID, f.lease.ID, f.lease.Fence}); e != nil {
		t.Fatal(e)
	}
	scope := f.worker.Scope
	scope.Resources = append(append([]string(nil), scope.Resources...), "task:unrelated", "task:independent", "path:other")
	worker, token, e := f.a.Enroll(f.owner, scope, time.Now().Add(time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	at, e := f.a.RegisterAttempt(token, worker.ID, AttemptRequest{RequestID: "accepted-broad-root", TaskID: "task-a", Checkout: f.at.Observation.Locator})
	if e != nil {
		t.Fatal(e)
	}
	lease, e := f.a.AcquireLease(token, worker.ID, AcquireLeaseRequest{"accepted-broad-lease", at.ID, []Resource{{"checkout", ""}}, time.Now().Add(10 * time.Minute)})
	if e != nil {
		t.Fatal(e)
	}
	revision, e := f.a.PublishAttemptRevision(token, worker.ID, PublishRevisionRequest{"accepted-broad-publish", at.ID, lease.ID, lease.Fence, 0})
	if e != nil {
		t.Fatal(e)
	}
	request := VerificationRequest{"accepted-broad-check", worker.ID, at.ID, lease.ID, revision.ID, f.check.ID, f.grant.ID, lease.Fence}
	if _, _, e = f.a.BeginVerification(token, f.runnerToken, f.runner.ID, request, CheckDigest(f.check)); e != nil {
		t.Fatal(e)
	}
	f.worker, f.workerToken, f.at, f.lease, f.revision, f.request = worker, token, at, lease, revision, request
	evidence, e := f.a.CompleteVerification(token, f.runnerToken, f.runner.ID, request, f.result())
	if e != nil {
		t.Fatal(e)
	}
	grant, e := f.a.GrantReview(f.owner, ReviewGrantRequest{f.reviewer.ID, worker.ID, "task-a", at.Observation.Repository, time.Now().Add(10 * time.Minute)})
	if e != nil {
		t.Fatal(e)
	}
	f.reviewGrant, f.evidence = grant, evidence
	f.req = ReviewRequest{OperationKey: "accepted-broad-review", GrantID: grant.ID, AttemptID: at.ID, LeaseID: lease.ID, RevisionID: revision.ID, ExpectedFence: lease.Fence, Outcome: "approve", Rationale: "exact credential-separated producer contract", EvidenceIDs: []string{evidence.ID}}
	p := f.propose(t)
	for _, upgrade := range []func(string) error{f.a.UpgradeSwarms, f.a.UpgradeDispatchIntents, f.a.UpgradeLifecycle, f.a.UpgradeRecovery, f.a.UpgradePreflight, f.a.UpgradeCheckpoints, f.a.UpgradeBudgets, f.a.UpgradeNativeAcceptance} {
		if e := upgrade(f.owner); e != nil {
			t.Fatal(e)
		}
	}
	c, e := f.a.ReadNativeAcceptanceCandidate(f.token, f.reviewer.ID, p.ID)
	if e != nil {
		t.Fatal(e)
	}
	r := c.Input
	r.OperationKey = "owner-close"
	return f, r
}
func TestNativeAcceptanceClosingTaskAuthorityAndRetainedProtection(t *testing.T) {
	f, r := acceptanceKernelSetup(t)
	// This owner-enrolled root has both exact task scopes before retirement.
	otherDir := secondCheckout(t, f.at)
	if e := f.a.BindTask(f.owner, TaskBinding{"unrelated", "project", f.at.Observation.Repository, true}); e != nil {
		t.Fatal(e)
	}
	scope := f.worker.Scope

	broad, broadToken, e := f.a.Enroll(f.owner, scope, time.Now().Add(time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	child, childToken, e := f.a.Delegate(broadToken, broad.ID, scope, time.Now().Add(30*time.Minute))
	if e != nil {
		t.Fatal(e)
	}
	var before state
	if e := f.a.transaction(false, false, func(s *state) error { before = *s; return nil }); e != nil {
		t.Fatal(e)
	}
	pending := f.request
	pending.OperationKey = "pending-command"
	if _, _, e := f.a.BeginVerification(f.workerToken, f.runnerToken, f.runner.ID, pending, CheckDigest(f.check)); e != nil {
		t.Fatal(e)
	}
	rec, e := f.a.BeginNativeAcceptance(f.owner, r, func(NativeAcceptanceRecord) (string, error) {
		return OutputDigest([]byte("canonical outcome contract")), nil
	})
	if e != nil || rec.State != "closing" {
		t.Fatal(rec, e)
	}
	if _, e = f.a.RegisterAttempt(f.workerToken, f.worker.ID, AttemptRequest{RequestID: "after-retirement", TaskID: "task-a", Checkout: f.at.Observation.Locator}); !errors.Is(e, ErrDenied) {
		t.Fatalf("closing task registration remained executable: %v", e)
	}
	if _, e = f.a.RenewLease(f.workerToken, f.worker.ID, RenewLeaseRequest{OperationKey: "renew-after", AttemptID: f.at.ID, LeaseID: f.lease.ID, ExpectedFence: f.lease.Fence, ExpiresAt: time.Now().Add(time.Minute)}); !errors.Is(e, ErrDenied) {
		t.Fatalf("closing task renewal remained executable: %v", e)
	}
	if _, e = f.a.PublishAttemptRevision(f.workerToken, f.worker.ID, PublishRevisionRequest{OperationKey: "publish-after", AttemptID: f.at.ID, LeaseID: f.lease.ID, ExpectedFence: f.lease.Fence, ExpectedRevision: 1}); !errors.Is(e, ErrDenied) {
		t.Fatalf("closing task publication remained executable: %v", e)
	}
	if _, e = f.a.CompleteVerification(f.workerToken, f.runnerToken, f.runner.ID, pending, f.result()); !errors.Is(e, ErrDenied) {
		t.Fatalf("pending runner produced fresh evidence after task retirement: %v", e)
	}
	eligible, e := f.a.EvaluateReview(f.token, f.reviewer.ID, r.ReviewID)
	if e != nil || eligible.Eligible {
		t.Fatalf("closing task eligible review resurrected: %+v %v", eligible, e)
	}
	// A separately enrolled root and an admission toggle cannot revive this task.
	replacement, token, e := f.a.Enroll(f.owner, f.worker.Scope, time.Now().Add(time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	if e = f.a.BindTask(f.owner, TaskBinding{"task-a", "project", f.at.Observation.Repository, true}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.a.RegisterAttempt(token, replacement.ID, AttemptRequest{RequestID: "replacement", TaskID: "task-a", Checkout: f.at.Observation.Locator}); !errors.Is(e, ErrDenied) {
		t.Fatalf("new family bypassed task retirement: %v", e)
	}
	for _, principal := range []struct {
		s          Session
		token, key string
	}{{broad, broadToken, "sibling"}, {child, childToken, "child"}} {
		if _, e := f.a.RegisterAttempt(principal.token, principal.s.ID, AttemptRequest{RequestID: principal.key + "-retired", TaskID: "task-a", Checkout: otherDir}); !errors.Is(e, ErrDenied) {
			t.Fatalf("%s bypassed task retirement: %v", principal.key, e)
		}
	}
	other, e := f.a.RegisterAttempt(broadToken, broad.ID, AttemptRequest{RequestID: "unrelated-positive", TaskID: "unrelated", Checkout: otherDir})
	if e != nil {
		t.Fatal("task-only retirement blocked actual unrelated registration", e)
	}
	if _, e = f.a.AcquireLease(broadToken, broad.ID, acquireReq(other, "retained-overlap", "other")); !errors.Is(e, ErrDenied) {
		t.Fatal("retained whole-repository lease lost protection", e)
	}
	// A genuinely unrelated repository avoids the retained whole-repository protection.
	independentDir := t.TempDir()
	gitTest(t, independentDir, "init")
	gitTest(t, independentDir, "-c", "user.name=f", "-c", "user.email=f@example.invalid", "commit", "--allow-empty", "-m", "independent")
	obs, e := checkout.Observe(independentDir)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.a.BindTask(f.owner, TaskBinding{"independent", "project", obs.Repository, true}); e != nil {
		t.Fatal(e)
	}
	independent, e := f.a.RegisterAttempt(f.workerToken, f.worker.ID, AttemptRequest{RequestID: "unrelated-independent", TaskID: "independent", Checkout: independentDir})
	if e != nil {
		t.Fatal("task-only retirement blocked independent registration", e)
	}
	if _, e = f.a.AcquireLease(f.workerToken, f.worker.ID, acquireReq(independent, "unrelated-positive-lease", "other")); e != nil {
		t.Fatal("task-only retirement blocked actual unrelated lease", e)
	}
	// Retirement keys only the task: the exact implementation root keeps unrelated scope.
	if e = f.a.transaction(false, false, func(s *state) error {
		if workerTaskWithdrawn(s, f.worker.ID, "unrelated") {
			t.Fatal("acceptance globally withdrew unrelated task")
		}
		if !reflect.DeepEqual(before.Leases[f.lease.ID], s.Leases[f.lease.ID]) || before.Fence+1 != s.Fence {
			t.Fatal("acceptance released protection or rewrote original fence")
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
}
func TestNativeAcceptanceFinalObservationAndCanonicalWriteFailure(t *testing.T) {
	f, r := acceptanceKernelSetup(t)
	if _, e := f.a.BeginNativeAcceptance(f.owner, r, func(NativeAcceptanceRecord) (string, error) { return OutputDigest([]byte("bound canonical")), nil }); e != nil {
		t.Fatal(e)
	}
	calls := 0
	wrote := false
	out, e := f.a.ContinueNativeAcceptance(f.owner, r, func(NativeAcceptanceRecord) (bool, error) {
		calls++
		if calls == 2 {
			// The lock is held; emulate expiry during the actual final external observer.
			time.Sleep(30 * time.Millisecond)
		}
		return false, nil
	}, func(NativeAcceptanceRecord) error { wrote = true; return errors.New("injected canonical write") })
	if e == nil || out.Applied || !wrote {
		t.Fatalf("canonical write failure reported success: %+v %v", out, e)
	}
	// Real slow observer changes reviewed worktree; ledger revision alone cannot vouch for it.
	calls = 0
	wrote = false
	out, e = f.a.ContinueNativeAcceptance(f.owner, r, func(NativeAcceptanceRecord) (bool, error) {
		calls++
		if calls == 2 {
			gitTest(t, f.at.Observation.Locator, "-c", "user.name=f", "-c", "user.email=f@example.invalid", "commit", "--allow-empty", "-m", "changed during final inspect")
		}
		return false, nil
	}, func(NativeAcceptanceRecord) error { wrote = true; return nil })
	if !errors.Is(e, ErrDenied) || out.Applied || wrote {
		t.Fatalf("late final-inspect changed revision authorized canonical close: %+v wrote=%v %v", out, wrote, e)
	}
}

// This fixture drives real reserve/intent/correlation/acknowledgement APIs. Its
// verification result is a kernel contract fixture; actual commands live in the
// application close-consumer tests.
func TestNativeAcceptanceControllerConsumersClosingAndClosed(t *testing.T) {
	f := dispatchSetup(t)
	scope := f.worker.Scope
	scope.Actions = append(append([]string(nil), scope.Actions...), "attempt.publish", "verification.request")
	worker, token, e := f.a.Enroll(f.owner, scope, time.Now().Add(time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	at, e := f.a.RegisterAttempt(token, worker.ID, AttemptRequest{RequestID: "published-dispatch-worker", TaskID: "task-a", Checkout: f.dirs[0]})
	if e != nil {
		t.Fatal(e)
	}
	f.worker, f.workerToken, f.at = worker, token, at
	f.req.WorkerSessionID = worker.ID
	intent := requestIntent(t, f).Intent
	correlated, e := f.a.CorrelateDispatchIntent(f.bridgeToken, f.bridge.ID, correlation(intent, "correlate", "host_correlated"))
	if e != nil {
		t.Fatal(e)
	}
	ack, e := f.a.AcknowledgeDispatchIntent(f.workerToken, f.worker.ID, acknowledgement(f, correlated))
	if e != nil || ack.Lease == nil {
		t.Fatal("actual initial acknowledgement", e)
	}
	lease := *ack.Lease
	revision, e := f.a.PublishAttemptRevision(f.workerToken, f.worker.ID, PublishRevisionRequest{"controller-publish", f.at.ID, lease.ID, lease.Fence, 0})
	if e != nil {
		t.Fatal(e)
	}
	observer := &testReviewObserver{ReviewObservation{OutputDigest([]byte("checklist")), "review-policy", []RequiredCheck{{"unit", CheckDigest(CheckDescriptor{"unit", "printf contract", time.Second})}}}}
	f.a.reviewObserver = observer
	runner, rt, e := f.a.Enroll(f.owner, Scope{[]string{"verification.run"}, []string{"task:task-a"}}, time.Now().Add(time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	check := CheckDescriptor{"unit", "printf contract", time.Second}
	grant, e := f.a.GrantVerification(f.owner, runner.ID, "task-a", f.at.Observation.Repository, check, time.Now().Add(10*time.Minute))
	if e != nil {
		t.Fatal(e)
	}
	request := VerificationRequest{"controller-command", f.worker.ID, f.at.ID, lease.ID, revision.ID, check.ID, grant.ID, lease.Fence}
	if _, _, e = f.a.BeginVerification(f.workerToken, rt, runner.ID, request, CheckDigest(check)); e != nil {
		t.Fatal(e)
	}
	evidence, e := f.a.CompleteVerification(f.workerToken, rt, runner.ID, request, VerificationResult{Commit: revision.Observation.Commit, Tree: revision.Observation.Tree, Directory: revision.Observation.Locator, CommandDigest: CheckDigest(check), OutputDigest: OutputDigest([]byte("contract")), Output: []byte("contract"), Clean: true})
	if e != nil {
		t.Fatal(e)
	}
	reviewer, rvt, e := f.a.Enroll(f.owner, Scope{[]string{"review.propose", "review.evaluate"}, []string{"task:task-a"}}, time.Now().Add(time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	rg, e := f.a.GrantReview(f.owner, ReviewGrantRequest{reviewer.ID, f.worker.ID, "task-a", f.at.Observation.Repository, time.Now().Add(10 * time.Minute)})
	if e != nil {
		t.Fatal(e)
	}
	review, e := f.a.ProposeReview(rvt, reviewer.ID, ReviewRequest{OperationKey: "controller-review", GrantID: rg.ID, AttemptID: f.at.ID, LeaseID: lease.ID, RevisionID: revision.ID, ExpectedFence: lease.Fence, Outcome: "approve", Rationale: "contract independent review", EvidenceIDs: []string{evidence.ID}})
	if e != nil {
		t.Fatal(e)
	}
	for _, upgrade := range []func(string) error{f.a.UpgradeLifecycle, f.a.UpgradeRecovery, f.a.UpgradePreflight, f.a.UpgradeCheckpoints, f.a.UpgradeBudgets, f.a.UpgradeNativeAcceptance} {
		if e = upgrade(f.owner); e != nil {
			t.Fatal(e)
		}
	}
	candidate, e := f.a.ReadNativeAcceptanceCandidate(rvt, reviewer.ID, review.ID)
	if e != nil {
		t.Fatal(e)
	}
	input := candidate.Input
	input.OperationKey = "controller-owner-close"
	var originalDispatch DispatchReservation
	if e = f.a.transaction(false, false, func(s *state) error { originalDispatch = s.Dispatches[f.d.ID]; return nil }); e != nil {
		t.Fatal(e)
	}
	if _, e = f.a.BeginNativeAcceptance(f.owner, input, func(NativeAcceptanceRecord) (string, error) { return OutputDigest([]byte("contract canonical")), nil }); e != nil {
		t.Fatal(e)
	}
	for _, phaseState := range []string{"closing", "closed"} {
		plan, e := f.a.PlanSwarm(f.token, f.s.ID, "project")
		if e != nil {
			t.Fatal(e)
		}
		found := false
		for _, v := range plan.Verdicts {
			if v.TaskID == "task-a" {
				found = true
				if v.Selected || v.Reason != "task_execution_retired" {
					t.Fatalf("%s controller plan resurrected retired task: %+v", phaseState, v)
				}
			}
		}
		if !found {
			t.Fatal("missing retired task verdict")
		}
		if _, e = f.a.ReserveWave(f.token, f.s.ID, ReserveWaveRequest{OperationKey: "retired-wave-" + phaseState, Plan: plan, ExpiresAt: time.Now().Add(time.Minute)}); e == nil {
			t.Fatal("retired controller reserved fresh wave")
		}
		if _, e = f.a.AdmitDispatch(f.token, f.s.ID, AdmitDispatchRequest{DispatchID: f.d.ID, ExpectedFence: f.d.Reservation.Fence}); !errors.Is(e, ErrDenied) {
			t.Fatal("retired controller dispatch admitted", e)
		}
		req := f.req
		req.OperationKey = "retired-intent-" + phaseState
		if out, e := f.a.RequestDispatchIntent(f.token, f.s.ID, req); e == nil || out.HostActionAuthorized {
			t.Fatal("retired task received fresh host permit", e)
		}
		ar := acknowledgement(f, correlated)
		ar.OperationKey = "retired-ack-" + phaseState
		if _, e = f.a.AcknowledgeDispatchIntent(f.workerToken, f.worker.ID, ar); !errors.Is(e, ErrDenied) {
			t.Fatal("retired worker acknowledgement acquired authority", e)
		}
		if _, e = f.a.HandoffDispatch(f.workerToken, f.worker.ID, HandoffDispatchRequest{OperationKey: "retired-handoff-" + phaseState, DispatchID: f.d.ID, AttemptID: f.at.ID, ExpectedFence: f.d.Reservation.Fence}); !errors.Is(e, ErrDenied) {
			t.Fatal("retired task handoff acquired authority", e)
		}
		if e = f.a.transaction(false, false, func(s *state) error {
			if s.Leases[lease.ID].Released || !reflect.DeepEqual(originalDispatch, s.Dispatches[f.d.ID]) {
				t.Fatal("acceptance released dispatch protection")
			}
			return nil
		}); e != nil {
			t.Fatal(e)
		}
		if phaseState == "closing" {
			if out, e := f.a.ContinueNativeAcceptance(f.owner, input, func(NativeAcceptanceRecord) (bool, error) { return true, nil }, func(NativeAcceptanceRecord) error { t.Fatal("historical completion reexecuted"); return nil }); e != nil || !out.Applied {
				t.Fatal(out, e)
			}
		}
	}
}
