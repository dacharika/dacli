package sessions

import (
	"errors"
	"os"
	"reflect"
	"testing"
	"time"
)

type checkpointFixtureAdmission struct {
	*preflightFixtureAdmission
	trunk, digest  string
	workers        map[string]Session
	tokens         map[string]string
	findings       []CheckpointFinding
	capabilityHook func()
	checkpointHook func()
}

func (a *checkpointFixtureAdmission) AdmitSwarmCapability(w, p, h, d string) error {
	if a.capabilityHook != nil {
		a.capabilityHook()
	}
	return a.swarmFixtureAdmission.AdmitSwarmCapability(w, p, h, d)
}
func (a *checkpointFixtureAdmission) ObserveCheckpoint(string, string, []string) (CheckpointObservation, error) {
	if a.checkpointHook != nil {
		a.checkpointHook()
	}
	return CheckpointObservation{Trunk: a.trunk, Digest: a.digest, Findings: a.findings}, nil
}
func checkpointSetup(t *testing.T) (swarmFixture, *checkpointFixtureAdmission, CheckpointPolicy) {
	t.Helper()
	f, ad, _, workers, tokens := preflightSetup(t)
	if e := f.a.UpgradeCheckpoints(f.owner); e != nil {
		t.Fatal(e)
	}
	ca := &checkpointFixtureAdmission{preflightFixtureAdmission: ad, trunk: "trunk-one", digest: "canonical-one", workers: workers, tokens: tokens}
	f.a.admission = ca
	actions := append(append([]string{}, f.s.Scope.Actions...), "checkpoint.read", "checkpoint.record", "checkpoint.reconcile")
	var e error
	f.s, f.token, e = f.a.Enroll(f.owner, Scope{Actions: actions, Resources: f.s.Scope.Resources}, time.Now().Add(time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	p := CheckpointPolicy{SchemaVersion: 1, ProjectID: "project", Revision: "checkpoint-policy-one", Harness: "codex", TaskIDs: []string{"task-a", "task-b", "task-c"}, MaxCycles: 5, MaxNoProgress: 2, ExpiresAt: time.Now().Add(time.Hour)}
	if e = f.a.ConfigureCheckpoints(f.owner, p); e != nil {
		t.Fatal(e)
	}
	return f, ca, p
}
func checkpointRequest(key, inv string, n uint64) CheckpointRequest {
	return CheckpointRequest{OperationKey: key, CheckpointID: "checkpoint-one", ProjectID: "project", InvocationID: inv, Phase: "pre-cycle", ExpectedSequence: n}
}
func TestNativeCheckpointMandatoryPrecycleOneWaveAndHistoricalReplay(t *testing.T) {
	f, _, _ := checkpointSetup(t)
	plan, e := f.a.PlanSwarm(f.token, f.s.ID, "project")
	if e != nil {
		t.Fatal(e)
	}
	request := ReserveWaveRequest{OperationKey: "wave-one", Plan: plan, ExpiresAt: time.Now().Add(time.Minute)}
	if _, e = f.a.ReserveWave(f.token, f.s.ID, request); !errors.Is(e, ErrDenied) {
		t.Fatalf("precycle bypass must refuse before any reservation: %v", e)
	}
	status, e := f.a.ReconcileCheckpoint(f.token, f.s.ID, checkpointRequest("reconcile-one", "invocation-one", 0))
	if e != nil || !status.WaveAllowed {
		t.Fatalf("fresh reconciliation should permit one wave: %+v %v", status, e)
	}
	request.CheckpointID = status.ID
	request.CheckpointSequence = status.Sequence
	request.InvocationID = status.InvocationID
	request.ReconciliationDigest = status.ConditionDigest
	wave, e := f.a.ReserveWave(f.token, f.s.ID, request)
	if e != nil {
		t.Fatal(e)
	}
	replay, e := f.a.ReserveWave(f.token, f.s.ID, request)
	if e != nil || !reflect.DeepEqual(wave, replay) {
		t.Fatalf("historical replay must preserve wave: %v", e)
	}
	request.OperationKey = "wave-two"
	if _, e = f.a.ReserveWave(f.token, f.s.ID, request); !errors.Is(e, ErrDenied) {
		t.Fatalf("consumed reconciliation cannot authorize another wave: %v", e)
	}
	current, e := f.a.CheckpointStatus(f.token, f.s.ID, "project", status.ID)
	if e != nil || current.Cycle != 1 || current.WaveAllowed {
		t.Fatalf("status must show durable cycle and no live permit: %+v %v", current, e)
	}
}
func TestNativeCheckpointFreshAuthorityAndCanonicalGate(t *testing.T) {
	for _, change := range []string{"canonical", "authority", "STOP"} {
		t.Run(change, func(t *testing.T) {
			f, ad, _ := checkpointSetup(t)
			status, e := f.a.ReconcileCheckpoint(f.token, f.s.ID, checkpointRequest("reconcile-one", "inv-one", 0))
			if e != nil {
				t.Fatal(e)
			}
			plan, e := f.a.PlanSwarm(f.token, f.s.ID, "project")
			if e != nil {
				t.Fatal(e)
			}
			switch change {
			case "canonical":
				ad.digest = "canonical-two"
			case "authority":
				if _, _, e = f.a.Enroll(f.owner, Scope{Actions: []string{"read"}, Resources: []string{"task:task-a"}}, time.Now().Add(time.Hour)); e != nil {
					t.Fatal(e)
				}
			case "STOP":
				p := f.p
				p.Revision = "stopped-policy"
				p.Stopped = true
				if e = f.a.ConfigureSwarm(f.owner, p); e != nil {
					t.Fatal(e)
				}
			}
			req := ReserveWaveRequest{OperationKey: "wave-one", Plan: plan, ExpiresAt: time.Now().Add(time.Minute), CheckpointID: status.ID, CheckpointSequence: status.Sequence, InvocationID: status.InvocationID, ReconciliationDigest: status.ConditionDigest}
			if _, e = f.a.ReserveWave(f.token, f.s.ID, req); !errors.Is(e, ErrDenied) {
				t.Fatalf("fresh %s change must invalidate generation without fence reliance: %v", change, e)
			}
		})
	}
}
func TestNativeCheckpointRestartTrunkProgressAndNoCounterReset(t *testing.T) {
	f, ad, _ := checkpointSetup(t)
	one, e := f.a.ReconcileCheckpoint(f.token, f.s.ID, checkpointRequest("one", "inv-one", 0))
	if e != nil {
		t.Fatal(e)
	}
	two, e := f.a.ReconcileCheckpoint(f.token, f.s.ID, checkpointRequest("two", "inv-two", one.Sequence))
	if e != nil || two.NoProgress != 1 {
		t.Fatalf("unchanged invocation must retain no-progress: %+v %v", two, e)
	}
	three, e := f.a.ReconcileCheckpoint(f.token, f.s.ID, checkpointRequest("three", "inv-three", two.Sequence))
	if e != nil || three.HaltClass != "no_progress" {
		t.Fatalf("bounded no-progress halt: %+v %v", three, e)
	}
	a, e := Open(f.a.path, f.a.binding, ad)
	if e != nil {
		t.Fatal(e)
	}
	f.a = a
	ad.trunk = "actual-new-trunk"
	four, e := f.a.ReconcileCheckpoint(f.token, f.s.ID, checkpointRequest("four", "inv-four", three.Sequence))
	if e != nil || four.NoProgress != 0 || four.HaltClass != "ready" {
		t.Fatalf("fresh trunk progress resumes original checkpoint: %+v %v", four, e)
	}
	newID := checkpointRequest("five", "inv-five", 0)
	newID.CheckpointID = "replacement-counter"
	if _, e = f.a.ReconcileCheckpoint(f.token, f.s.ID, newID); !errors.Is(e, ErrDenied) {
		t.Fatalf("new checkpoint cannot reset project counters: %v", e)
	}
}
func TestNativeCheckpointContractUnknownNeverSuccessAndScope(t *testing.T) {
	f, _, _ := checkpointSetup(t)
	r := checkpointRequest("one", "inv-one", 0)
	r.Refs = []CheckpointRefs{{TaskID: "task-a", IntegrationOperationID: "future-integration", AcceptanceOperationID: "future-acceptance"}}
	status, e := f.a.ReconcileCheckpoint(f.token, f.s.ID, r)
	if e != nil {
		t.Fatal(e)
	}
	if status.WaveAllowed || status.HaltClass != "pending_local_integration" || len(status.Findings) != 2 || status.Findings[0].Knowledge != "unknown" {
		t.Fatalf("unknown native producer outcome must never be success: %+v", status)
	}
	r.ExpectedSequence = status.Sequence
	r.OperationKey = "two"
	r.Refs = nil
	if _, e = f.a.RecordCheckpoint(f.token, f.s.ID, r); !errors.Is(e, ErrDenied) {
		t.Fatalf("uncertain operation identities cannot be dropped: %v", e)
	}
	s, token, e := f.a.Enroll(f.owner, Scope{Actions: []string{"checkpoint.read"}, Resources: []string{"project:project", "task:task-a"}}, time.Now().Add(time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.a.CheckpointStatus(token, s.ID, "project", status.ID); !errors.Is(e, ErrDenied) {
		t.Fatalf("partial task scope must not read linked records: %v", e)
	}
}
func TestNativeCheckpointSchema11PreservationAndPreflightUsable(t *testing.T) {
	f, _, _, _, _ := preflightSetup(t)
	before, e := os.ReadFile(f.a.path)
	if e != nil {
		t.Fatal(e)
	}
	old, e := f.a.CheckPreflight(f.token, f.s.ID, "project", "task-a", "before-upgrade")
	if e != nil {
		t.Fatal(e)
	}
	if e = f.a.UpgradeCheckpoints(f.owner); e != nil {
		t.Fatal(e)
	}
	replay, e := f.a.CheckPreflight(f.token, f.s.ID, "project", "task-a", "before-upgrade")
	if e != nil || !reflect.DeepEqual(old, replay) {
		t.Fatalf("schema11 receipt replay must remain usable after12: %v", e)
	}
	report, e := f.a.CheckPreflight(f.token, f.s.ID, "project", "task-a", "after-upgrade")
	if e != nil || !report.Report.Allowed {
		t.Fatalf("live schema11 preflight must remain usable after12: %+v %v", report, e)
	}
	if len(before) == 0 {
		t.Fatal("missing original authority")
	}
}

func TestNativeCheckpointCrashAfterActualIntentAckAndObservation(t *testing.T) {
	f, ad, _ := checkpointSetup(t)
	if e := f.a.ConfigureLifecycle(f.owner, LifecyclePolicy{SchemaVersion: 1, ProjectID: "project", Revision: "life", Source: "owner-manual-native-host", Freshness: time.Minute, CancellationChannel: "unsupported"}); e != nil {
		t.Fatal(e)
	}
	st, e := f.a.ReconcileCheckpoint(f.token, f.s.ID, checkpointRequest("precycle", "inv-one", 0))
	if e != nil {
		t.Fatal(e)
	}
	plan, e := f.a.PlanSwarm(f.token, f.s.ID, "project")
	if e != nil {
		t.Fatal(e)
	}
	wave, e := f.a.ReserveWave(f.token, f.s.ID, ReserveWaveRequest{OperationKey: "wave", Plan: plan, ExpiresAt: time.Now().Add(time.Minute), CheckpointID: st.ID, CheckpointSequence: st.Sequence, InvocationID: st.InvocationID, ReconciliationDigest: st.ConditionDigest})
	if e != nil {
		t.Fatal(e)
	}
	d := wave.Dispatches[0]
	resources := []string{"project:project"}
	for _, r := range d.Reservation.Resources {
		resources = append(resources, r.Kind+":"+r.Path)
	}
	bridge, bt, e := f.a.Enroll(f.owner, Scope{Actions: []string{"read", "dispatch.correlate", "dispatch.observe"}, Resources: resources}, time.Now().Add(time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	worker := ad.workers[d.Reservation.TaskID]
	wt := ad.tokens[d.Reservation.TaskID]
	at, e := f.a.RegisterAttempt(wt, worker.ID, AttemptRequest{RequestID: "actual-attempt", TaskID: d.Reservation.TaskID, Checkout: d.Reservation.Checkout, HostLabel: "fixture"})
	if e != nil {
		t.Fatal(e)
	}
	req := RequestDispatchIntent{OperationKey: "actual-intent", DispatchID: d.ID, BridgeSessionID: bridge.ID, WorkerSessionID: worker.ID, PromptSpecDigest: OutputDigest([]byte("bounded fixture")), ExpectedFence: d.Reservation.Fence}

	initial, e := f.a.RequestDispatchIntent(f.token, f.s.ID, req)
	if e != nil || !initial.HostActionAuthorized {
		t.Fatal(initial, e)
	}
	// Simulate orchestrator loss immediately after the real durable producer, before
	// recording its outcome in the checkpoint. No model host was launched here.
	reopen := func() {
		a, err := Open(f.a.path, f.a.binding, ad)
		if err != nil {
			t.Fatal(err)
		}
		f.a = a
	}
	reopen()
	replay, e := f.a.RequestDispatchIntent(f.token, f.s.ID, req)
	if e != nil || replay.HostActionAuthorized || replay.Intent.ID != initial.Intent.ID {
		t.Fatalf("crashed intent replay must not launch again: %+v %v", replay, e)
	}
	r := checkpointRequest("after-intent", "inv-two", st.Sequence+1)
	r.Refs = []CheckpointRefs{{TaskID: d.Reservation.TaskID, DispatchID: d.ID, IntentID: initial.Intent.ID}}
	after, e := f.a.ReconcileCheckpoint(f.token, f.s.ID, r)
	if e != nil || after.WaveAllowed || !hasCheckpointFinding(after, "unknown_dispatch_ack") {
		t.Fatalf("unknown actual intent cannot imply successful execution: %+v %v", after, e)
	}
	corr := correlation(initial.Intent, "actual-correlation", "host_correlated")
	i, e := f.a.CorrelateDispatchIntent(bt, bridge.ID, corr)
	if e != nil {
		t.Fatal(e)
	}
	ack := AcknowledgeDispatchIntent{OperationKey: "actual-ack", DispatchID: d.ID, IntentID: i.ID, AttemptID: at.ID, HostAgentID: i.HostAgentID, ExpectedFence: d.Reservation.Fence, ExpectedSequence: i.Sequence}
	acknowledged, e := f.a.AcknowledgeDispatchIntent(wt, worker.ID, ack)
	if e != nil {
		t.Fatal(e)
	}
	reopen()
	ackReplay, e := f.a.AcknowledgeDispatchIntent(wt, worker.ID, ack)
	if e != nil || !reflect.DeepEqual(acknowledged, ackReplay) {
		t.Fatalf("ack crash replay must preserve exact lease and fence: %+v %v", ackReplay, e)
	}
	binding := lifecycleBinding(acknowledged)
	staleObservation := ObserveLifecycleRequest{OperationKey: "actual-stale-running", Binding: binding, ExpectedSequence: 0, HostSequence: 1, State: "running", Source: "owner-manual-native-host", EvidenceDigest: OutputDigest([]byte("old running observation")), SourceTime: time.Now().Add(-2 * time.Minute).UTC()}
	if _, e := f.a.ObserveLifecycle(bt, bridge.ID, staleObservation); e != nil {
		t.Fatal(e)
	}
	stale, e := f.a.CheckpointStatus(f.token, f.s.ID, "project", st.ID)
	if e != nil || !hasCheckpointFinding(stale, "stale_host_observation") || stale.WaveAllowed {
		t.Fatalf("stale running host must remain distinct unknown: %+v %v", stale, e)
	}
	observation := ObserveLifecycleRequest{OperationKey: "actual-observation", Binding: binding, ExpectedSequence: 1, HostSequence: 2, State: "completed", Source: "owner-manual-native-host", EvidenceDigest: OutputDigest([]byte("observed cooperative completion")), SourceTime: time.Now().UTC()}
	observed, e := f.a.ObserveLifecycle(bt, bridge.ID, observation)
	if e != nil {
		t.Fatal(e)
	}
	reopen()
	observedReplay, e := f.a.ObserveLifecycle(bt, bridge.ID, observation)
	if e != nil || !reflect.DeepEqual(observed, observedReplay) {
		t.Fatalf("observation crash replay must remain immutable: %v", e)
	}
	r.OperationKey = "after-completion"
	r.InvocationID = "inv-three"
	r.ExpectedSequence = after.Sequence
	final, e := f.a.ReconcileCheckpoint(f.token, f.s.ID, r)
	if e != nil || final.WaveAllowed || !hasCheckpointFinding(final, "finished_dispatch_reconciliation") || !hasCheckpointFinding(final, "retained_resources") {
		t.Fatalf("terminal host does not release protection or authorize next wave: %+v %v", final, e)
	}
}
func hasCheckpointFinding(s CheckpointStatus, class string) bool {
	for _, f := range s.Findings {
		if f.Class == class {
			return true
		}
	}
	return false
}

func TestNativeCheckpointCanonicalChangeAtWaveCommitRefuses(t *testing.T) {
	f, ad, _ := checkpointSetup(t)
	status, e := f.a.ReconcileCheckpoint(f.token, f.s.ID, checkpointRequest("one", "inv-one", 0))
	if e != nil {
		t.Fatal(e)
	}
	plan, e := f.a.PlanSwarm(f.token, f.s.ID, "project")
	if e != nil {
		t.Fatal(e)
	}
	// Capability observation occurs after proposed reservation mutation. A change
	// in a nonselected canonical record still invalidates the whole cycle gate.
	calls := 0
	ad.capabilityHook = func() {
		calls++
		if calls == 3 {
			ad.digest = "changed-at-final-write"
		}
	}
	req := ReserveWaveRequest{OperationKey: "wave", Plan: plan, ExpiresAt: time.Now().Add(time.Minute), CheckpointID: status.ID, CheckpointSequence: status.Sequence, InvocationID: status.InvocationID, ReconciliationDigest: status.ConditionDigest}
	if _, e = f.a.ReserveWave(f.token, f.s.ID, req); !errors.Is(e, ErrDenied) {
		t.Fatalf("precycle condition must revalidate at final wave commit: %v", e)
	}
	if e = f.a.transaction(false, false, func(s *state) error {
		if len(s.Dispatches) != 0 || s.Checkpoints[status.ID][0].Cycle != 0 {
			t.Fatal("failed final gate must leave no reservation or spent cycle")
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
}

func TestNativeCheckpointBoundsCannotBeResetByInvocationOrPolicy(t *testing.T) {
	f, _, p := checkpointSetup(t)
	one, e := f.a.ReconcileCheckpoint(f.token, f.s.ID, checkpointRequest("one", "same-invocation", 0))
	if e != nil {
		t.Fatal(e)
	}
	duplicate := checkpointRequest("changed-key", "same-invocation", one.Sequence)
	if _, e = f.a.ReconcileCheckpoint(f.token, f.s.ID, duplicate); !errors.Is(e, ErrDenied) {
		t.Fatalf("new operation cannot reuse invocation to avoid no-progress counter: %v", e)
	}
	p.Revision = "reset-bounds"
	p.MaxCycles++
	if e = f.a.ConfigureCheckpoints(f.owner, p); !errors.Is(e, ErrDenied) {
		t.Fatalf("policy revision cannot reset persisted initial budget: %v", e)
	}
	f2, _, p2 := checkpointSetup(t)
	p2.Revision = "enforced-unknown"
	p2.RequireEnforcedBudget = true
	if e = f2.a.ConfigureCheckpoints(f2.owner, p2); e != nil {
		t.Fatal(e)
	}
	unknown, e := f2.a.ReconcileCheckpoint(f2.token, f2.s.ID, checkpointRequest("one", "inv-one", 0))
	if e != nil || unknown.HaltClass != "budget_usage_unknown" || unknown.WaveAllowed {
		t.Fatalf("unknown provider usage is not enforceable remaining: %+v %v", unknown, e)
	}
}

func TestNativeCheckpointPersistenceFailureReturnsNoStatusOrPermit(t *testing.T) {
	f, _, _ := checkpointSetup(t)
	f.a.persistOverride = func([]byte) error { return ErrStorage }
	out, e := f.a.ReconcileCheckpoint(f.token, f.s.ID, checkpointRequest("one", "inv-one", 0))
	if !errors.Is(e, ErrStorage) || !reflect.DeepEqual(out, CheckpointStatus{}) {
		t.Fatalf("failed persistence must return zero status and no permit: %+v %v", out, e)
	}
	f.a.persistOverride = nil
	if e = f.a.transaction(false, false, func(s *state) error {
		if len(s.Checkpoints) != 0 || len(s.CheckpointOperations) != 0 {
			t.Fatal("failed persistence recorded a checkpoint")
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
}
func TestNativeCheckpointOwner595RecoveryIsRealProgressAfterNoProgress(t *testing.T) {
	f, _, _ := checkpointSetup(t)
	one, e := f.a.ReconcileCheckpoint(f.token, f.s.ID, checkpointRequest("one", "inv-one", 0))
	if e != nil {
		t.Fatal(e)
	}
	plan, e := f.a.PlanSwarm(f.token, f.s.ID, "project")
	if e != nil {
		t.Fatal(e)
	}
	wave, e := f.a.ReserveWave(f.token, f.s.ID, ReserveWaveRequest{OperationKey: "wave", Plan: plan, ExpiresAt: time.Now().Add(time.Minute), CheckpointID: one.ID, CheckpointSequence: one.Sequence, InvocationID: one.InvocationID, ReconciliationDigest: one.ConditionDigest})
	if e != nil {
		t.Fatal(e)
	}
	status := one
	status.Sequence++
	for _, id := range []string{"two", "three", "four"} {
		status, e = f.a.ReconcileCheckpoint(f.token, f.s.ID, checkpointRequest(id, "inv-"+id, status.Sequence))
		if e != nil {
			t.Fatal(e)
		}
	}
	if status.HaltClass != "no_progress" || status.NoProgress != 2 {
		t.Fatalf("fixture must reach real durable no-progress before recovery: %+v", status)
	}
	for n, d := range wave.Dispatches {
		target, e := f.a.OwnerRecoveryTarget(f.owner, "reservation", d.ID)
		if e != nil {
			t.Fatal(e)
		}
		key := []string{"recover-one", "recover-two"}[n]
		if _, e = f.a.Recover(f.owner, RecoveryRequest{OperationKey: key, Target: target, Mode: "owner-reconciliation", Rationale: "owner retires unused isolated reservation", Uncertainty: "no host execution or allwriter termination proof", ReleaseRisk: "owner accepts disposable oldwriter risk"}); e != nil {
			t.Fatal(e)
		}
	}
	resumed, e := f.a.ReconcileCheckpoint(f.token, f.s.ID, checkpointRequest("five", "inv-five", status.Sequence))
	if e != nil || resumed.HaltClass != "ready" || resumed.NoProgress != 0 || resumed.Cycle != 1 || !resumed.WaveAllowed {
		t.Fatalf("actual fenced owner decisions must resume original checkpoint without counter reset: %+v %v", resumed, e)
	}
}
func TestNativeCheckpointBlockedTaskDoesNotPreventSafeBackfill(t *testing.T) {
	f, ad, _ := checkpointSetup(t)
	ad.snapshot.Candidates[0].Reason = "dependencies_unsatisfied"
	o := ad.observations["task-a"]
	o.Implementation.MaxPoints = 0
	ad.observations["task-a"] = o
	status, e := f.a.ReconcileCheckpoint(f.token, f.s.ID, checkpointRequest("one", "inv-one", 0))
	if e != nil || !status.WaveAllowed {
		t.Fatalf("blocked undersized task must not prevent safe disjoint shared backfill: %+v %v", status, e)
	}
	plan, e := f.a.PlanSwarm(f.token, f.s.ID, "project")
	if e != nil {
		t.Fatal(e)
	}
	for _, v := range plan.Verdicts {
		if v.TaskID == "task-a" && v.Selected {
			t.Fatal("blocked task was selected")
		}
	}
}

func TestNativeCheckpointFinalObserverCannotPersistAfterControllerExpiry(t *testing.T) {
	f, ad, _ := checkpointSetup(t)
	expires := time.Now().Add(3 * time.Second)
	s, token, e := f.a.Enroll(f.owner, f.s.Scope, expires)
	if e != nil {
		t.Fatal(e)
	}
	observed := false
	ad.checkpointHook = func() {
		if !observed {
			observed = true
			time.Sleep(time.Until(expires.Add(10 * time.Millisecond)))
		}
	}
	out, e := f.a.ReconcileCheckpoint(token, s.ID, checkpointRequest("expiry", "inv-expiry", 0))
	if !observed || !errors.Is(e, ErrDenied) || !reflect.DeepEqual(out, CheckpointStatus{}) {
		t.Fatalf("expired controller after slow observer must not persist checkpoint/permit: %+v %v observed=%v", out, e, observed)
	}
	if e = f.a.transaction(false, false, func(s *state) error {
		if len(s.Checkpoints) != 0 {
			t.Fatal("expired final writer persisted checkpoint")
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
}

func TestNativeCheckpointTransientPreflightTypedAndPermanentDominance(t *testing.T) {
	f, ad, _ := checkpointSetup(t)
	for task, o := range ad.observations {
		o.Phases = append(o.Phases, PreflightPhase{Phase: "integration", Verdict: "transient", Required: true, Assurance: "owner-manual", Remediation: "refresh transient infrastructure"})
		ad.observations[task] = o
	}
	transient, e := f.a.ReconcileCheckpoint(f.token, f.s.ID, checkpointRequest("transient-one", "inv-transient", 0))
	if e != nil || transient.HaltClass != "transient_preflight" || !transient.Retryable || transient.WaveAllowed {
		t.Fatalf("transient-only preflight must retain bounded retry diagnosis: %+v %v", transient, e)
	}
	o := ad.observations["task-c"]
	o.Implementation.MaxPoints = 3
	ad.observations["task-c"] = o
	permanent, e := f.a.ReconcileCheckpoint(f.token, f.s.ID, checkpointRequest("permanent-two", "inv-permanent", transient.Sequence))
	if e != nil || permanent.HaltClass != "policy_refusal" || permanent.Retryable || permanent.WaveAllowed {
		t.Fatalf("permanent candidate preflight must dominate mixed transient: %+v %v", permanent, e)
	}
}

func TestNativeCheckpointPermanentRetainedProtectionDominatesTransientObserver(t *testing.T) {
	f, ad, _ := checkpointSetup(t)
	status, e := f.a.ReconcileCheckpoint(f.token, f.s.ID, checkpointRequest("ready", "inv-ready", 0))
	if e != nil {
		t.Fatal(e)
	}
	plan, e := f.a.PlanSwarm(f.token, f.s.ID, "project")
	if e != nil {
		t.Fatal(e)
	}
	_, e = f.a.ReserveWave(f.token, f.s.ID, ReserveWaveRequest{OperationKey: "retained-wave", Plan: plan, ExpiresAt: time.Now().Add(time.Minute), CheckpointID: status.ID, CheckpointSequence: status.Sequence, InvocationID: status.InvocationID, ReconciliationDigest: status.ConditionDigest})
	if e != nil {
		t.Fatal(e)
	}
	ad.findings = []CheckpointFinding{{Class: "transient_infrastructure", Knowledge: "unknown", Reference: "project", Retryable: true, NextAction: "restore trunk observation"}}
	halted, e := f.a.ReconcileCheckpoint(f.token, f.s.ID, checkpointRequest("mixed", "inv-mixed", status.Sequence+1))
	if e != nil || halted.Retryable || halted.WaveAllowed || halted.HaltClass != "retained_resources" {
		t.Fatalf("permanent retained protection must dominate transient observer failure: %+v %v", halted, e)
	}
	if !hasCheckpointFinding(halted, "transient_infrastructure") {
		t.Fatal("transient observation diagnostics must remain visible")
	}
}
