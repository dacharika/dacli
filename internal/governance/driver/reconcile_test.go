package driver

import (
	"context"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/governance/sessions"
)

type observePumpPorts struct {
	view         sessions.ControllerDriverView
	reads        int
	observes     int
	steps        int
	verifies     int
	reconciles   int
	observeErr   error
	observeState string
	terminalHalt string
}

type missingReviewDecisionPorts struct {
	*observePumpPorts
	reviews int
}

func (p *missingReviewDecisionPorts) Review(context.Context, sessions.ControllerAction, EffectBudget) ([]Receipt, error) {
	p.reviews++
	return nil, ErrUnsupported
}

func (p *observePumpPorts) Read(context.Context, Request) (sessions.ControllerDriverView, error) {
	p.reads++
	return p.view, nil
}

func (p *observePumpPorts) Step(_ context.Context, _ sessions.ControllerStepRequest) (sessions.ControllerStatus, sessions.ControllerHostDispatchPermit, error) {
	p.steps++
	p.view.Status.Sequence++
	return p.view.Status, sessions.ControllerHostDispatchPermit{}, nil
}

func (p *observePumpPorts) Stop(context.Context, sessions.ControllerStepRequest) (sessions.ControllerStatus, error) {
	panic("unexpected Stop")
}

func (p *observePumpPorts) Reconcile(context.Context, sessions.ControllerAction, EffectBudget) ([]Receipt, error) {
	p.reconciles++
	return nil, nil
}

func (p *observePumpPorts) Dispatch(context.Context, sessions.ControllerHostDispatchPermit, sessions.ControllerAction, EffectBudget) ([]Receipt, error) {
	panic("unexpected Dispatch")
}

func (p *observePumpPorts) Observe(_ context.Context, action sessions.ControllerAction, budget EffectBudget) ([]Receipt, error) {
	p.observes++
	if p.observeErr != nil {
		return nil, p.observeErr
	}
	if err := budget.ReserveEffect(); err != nil {
		return nil, err
	}
	p.view.Status.HaltClass = "revision_pending"
	if p.terminalHalt != "" {
		p.view.Status.Terminal = true
		p.view.Status.HaltClass = p.terminalHalt
	}
	state := p.observeState
	if state == "" {
		state = "running"
	}
	key, err := OperationKey(p.view.Status.ProjectID, p.view.Status.ID, p.view.Status.CheckpointID, 3, "observe", action.OperationKey)
	if err != nil {
		return nil, err
	}
	binding := sessions.LifecycleBinding{DispatchID: action.DispatchID, IntentID: action.IntentID, TaskID: action.TaskID, ReservationFence: action.ReservationFence}
	observation := &sessions.LifecycleStatus{
		Binding: binding, State: state, Freshness: "fresh",
		Observations: []sessions.LifecycleObservation{{HostSequence: 3, Binding: binding, State: state, Source: "cooperative-test", EvidenceDigest: "digest", SourceTime: time.Now()}},
	}
	return []Receipt{{Kind: "dispatch", OperationKey: key, State: "recorded", Lifecycle: observation}}, nil
}

func (p *observePumpPorts) Verify(_ context.Context, action sessions.ControllerAction, request sessions.VerificationRequest, budget EffectBudget) ([]Receipt, error) {
	p.verifies++
	if err := budget.ReserveEffect(); err != nil {
		return nil, err
	}
	for i := range p.view.Operations {
		if p.view.Operations[i].Kind == "verification" && p.view.Operations[i].OperationKey == request.OperationKey {
			p.view.Operations[i].State = "completed"
		}
	}
	lookup := &sessions.VerificationLookup{State: sessions.VerificationCompleted, Evidence: &sessions.Evidence{
		IssuerSession: action.ActorID, Provenance: "trusted-local-runner", GrantID: request.GrantID, CheckID: request.CheckID,
		CommandDigest: "command", OutputDigest: "output", Revision: sessions.AttemptRevision{ID: request.RevisionID, AttemptID: request.AttemptID, SessionID: request.WorkerSession, LeaseID: request.LeaseID, Fence: request.ExpectedFence},
	}}
	return []Receipt{{Kind: "verification", OperationKey: request.OperationKey, State: "completed", Verification: lookup}}, nil
}

func (p *observePumpPorts) Review(context.Context, sessions.ControllerAction, EffectBudget) ([]Receipt, error) {
	panic("unexpected Review")
}

func (p *observePumpPorts) Cancel(context.Context, sessions.ControllerAction, EffectBudget) ([]Receipt, error) {
	panic("unexpected Cancel")
}

type terminalObservationPumpPorts struct{ *observePumpPorts }

func (p *terminalObservationPumpPorts) Step(_ context.Context, _ sessions.ControllerStepRequest) (sessions.ControllerStatus, sessions.ControllerHostDispatchPermit, error) {
	p.steps++
	p.view.Status.Sequence++
	p.view.Status.Terminal = true
	p.view.Status.Phase = "completed"
	p.view.Status.HaltClass = "empty_backlog"
	return p.view.Status, sessions.ControllerHostDispatchPermit{}, nil
}

func TestRunStepsOnceAfterRunningObservationForReservedSibling(t *testing.T) {
	now := time.Now()
	action := sessions.ControllerAction{
		ID: "action-a", Kind: "host_dispatch", TaskID: "task-a", DispatchID: "dispatch-a",
		IntentID: "intent-a", OperationKey: "host-key-a", ReservationFence: 7,
	}
	ports := &observePumpPorts{view: sessions.ControllerDriverView{
		AuthorityVersion: 16,
		Status: sessions.ControllerStatus{
			ID: "invocation-a", ProjectID: "project-a", CheckpointID: "checkpoint-a", Sequence: 1,
			Phase: "observe", HaltClass: "lifecycle_unknown", StartedAt: now.Add(-time.Second),
			Deadline: now.Add(time.Minute), Actions: []sessions.ControllerAction{action, {ID: "sibling", Kind: "dispatch", TaskID: "task-b", DispatchID: "dispatch-b", ReservationFence: 8}},
		},
		Policy: sessions.ControllerPolicy{ProjectID: "project-a", Revision: "policy-a", MaxNoProgress: 1, MaxWaves: 4, MaxElapsed: time.Minute},
		Operations: []sessions.ControllerDriverOperation{
			{Kind: "dispatch", OperationKey: action.OperationKey, State: "completed", ActionID: action.ID, TaskID: action.TaskID, DispatchID: action.DispatchID, IntentID: action.IntentID, ReservationFence: action.ReservationFence},
			{Kind: "dispatch", OperationKey: "sibling-key", State: "absent", ActionID: "sibling", TaskID: "task-b", DispatchID: "dispatch-b", ReservationFence: 8},
		},
	}}
	d, err := New(ports, Limits{MaxTransitions: 8, MaxEffects: 8, MaxWork: 64, MaxNoProgress: 1, MaxElapsed: time.Minute, OperationTimeout: time.Second, MaxSequence: 32})
	if err != nil {
		t.Fatal(err)
	}
	result, err := d.Run(context.Background(), Request{SchemaVersion: 1, ProjectID: "project-a", InvocationID: "invocation-a", CheckpointID: "checkpoint-a", ExpectedSequence: 1, Mode: "drive"})
	if err != nil {
		t.Fatal(err)
	}
	if ports.observes != 1 {
		t.Fatalf("Observe calls = %d, want 1", ports.observes)
	}
	if ports.reads < 3 {
		t.Fatalf("Read calls = %d, want initial, post-observation, and post-Step reads", ports.reads)
	}
	if ports.steps != 1 || result.Outcome != "no_progress" {
		t.Fatalf("running observation should Step once for its reserved sibling: steps=%d result=%+v", ports.steps, result)
	}
	if result.Sequence != ports.view.Status.Sequence || result.Work == 0 {
		t.Fatalf("result did not reflect refreshed durable view/work: %+v", result)
	}
}

func TestRunWaitsAfterRunningObservationWithoutSibling(t *testing.T) {
	now := time.Now()
	action := sessions.ControllerAction{ID: "action-running", Kind: "host_dispatch", TaskID: "task-a", DispatchID: "dispatch-a", IntentID: "intent-a", OperationKey: "host-key-a", ReservationFence: 7}
	ports := &observePumpPorts{view: sessions.ControllerDriverView{
		AuthorityVersion: 16,
		Status:           sessions.ControllerStatus{ID: "invocation-running", ProjectID: "project-running", CheckpointID: "checkpoint-running", Sequence: 1, Phase: "observe", HaltClass: "lifecycle_unknown", StartedAt: now.Add(-time.Second), Deadline: now.Add(time.Minute), Actions: []sessions.ControllerAction{action}},
		Policy:           sessions.ControllerPolicy{ProjectID: "project-running", Revision: "policy-running", MaxNoProgress: 1, MaxWaves: 4, MaxElapsed: time.Minute},
		Operations:       []sessions.ControllerDriverOperation{{Kind: "dispatch", OperationKey: action.OperationKey, State: "completed", ActionID: action.ID, TaskID: action.TaskID, DispatchID: action.DispatchID, IntentID: action.IntentID, ReservationFence: action.ReservationFence}},
	}}
	d, err := New(ports, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	result, err := d.Run(context.Background(), Request{SchemaVersion: 1, ProjectID: "project-running", InvocationID: "invocation-running", CheckpointID: "checkpoint-running", ExpectedSequence: 1, Mode: "drive"})
	if err != nil {
		t.Fatal(err)
	}
	if ports.observes != 1 || ports.steps != 0 || result.Outcome != "awaiting_external" || result.Reason != "revision_pending" {
		t.Fatalf("running observation without a sibling should wait immediately: observes=%d steps=%d result=%+v", ports.observes, ports.steps, result)
	}
}

func TestRunStepsOnceAfterTerminalHostObservation(t *testing.T) {
	now := time.Now()
	action := sessions.ControllerAction{ID: "action-terminal", Kind: "host_dispatch", TaskID: "task-a", DispatchID: "dispatch-a", IntentID: "intent-a", OperationKey: "host-key-a", ReservationFence: 7}
	base := &observePumpPorts{view: sessions.ControllerDriverView{
		AuthorityVersion: 16,
		Status:           sessions.ControllerStatus{ID: "invocation-terminal", ProjectID: "project-terminal", CheckpointID: "checkpoint-terminal", Sequence: 1, Phase: "observe", HaltClass: "lifecycle_unknown", StartedAt: now.Add(-time.Second), Deadline: now.Add(time.Minute), Actions: []sessions.ControllerAction{action}},
		Policy:           sessions.ControllerPolicy{ProjectID: "project-terminal", Revision: "policy-terminal", MaxNoProgress: 1, MaxWaves: 4, MaxElapsed: time.Minute},
		Operations:       []sessions.ControllerDriverOperation{{Kind: "dispatch", OperationKey: action.OperationKey, State: "completed", ActionID: action.ID, TaskID: action.TaskID, DispatchID: action.DispatchID, IntentID: action.IntentID, ReservationFence: action.ReservationFence}},
	}, observeState: "completed"}
	ports := &terminalObservationPumpPorts{observePumpPorts: base}
	d, err := New(ports, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	result, err := d.Run(context.Background(), Request{SchemaVersion: 1, ProjectID: "project-terminal", InvocationID: "invocation-terminal", CheckpointID: "checkpoint-terminal", ExpectedSequence: 1, Mode: "drive"})
	if err != nil {
		t.Fatal(err)
	}
	if ports.observes != 1 || ports.steps != 1 || result.Outcome != "completed" || result.Sequence != 2 {
		t.Fatalf("terminal observation did not advance the canonical controller once: observes=%d steps=%d result=%+v", ports.observes, ports.steps, result)
	}
}

func TestRunDoesNotStepAfterTerminalObservationConcurrentStop(t *testing.T) {
	now := time.Now()
	action := sessions.ControllerAction{ID: "action-stopped", Kind: "host_dispatch", TaskID: "task-a", DispatchID: "dispatch-a", IntentID: "intent-a", OperationKey: "host-key-a", ReservationFence: 7}
	base := &observePumpPorts{view: sessions.ControllerDriverView{
		AuthorityVersion: 16,
		Status:           sessions.ControllerStatus{ID: "invocation-stopped", ProjectID: "project-stopped", CheckpointID: "checkpoint-stopped", Sequence: 1, Phase: "observe", HaltClass: "lifecycle_unknown", StartedAt: now.Add(-time.Second), Deadline: now.Add(time.Minute), Actions: []sessions.ControllerAction{action}},
		Policy:           sessions.ControllerPolicy{ProjectID: "project-stopped", Revision: "policy-stopped", MaxNoProgress: 1, MaxWaves: 4, MaxElapsed: time.Minute},
		Operations:       []sessions.ControllerDriverOperation{{Kind: "dispatch", OperationKey: action.OperationKey, State: "completed", ActionID: action.ID, TaskID: action.TaskID, DispatchID: action.DispatchID, IntentID: action.IntentID, ReservationFence: action.ReservationFence}},
	}, observeState: "completed", terminalHalt: "stopped"}
	ports := &terminalObservationPumpPorts{observePumpPorts: base}
	d, err := New(ports, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	result, err := d.Run(context.Background(), Request{SchemaVersion: 1, ProjectID: "project-stopped", InvocationID: "invocation-stopped", CheckpointID: "checkpoint-stopped", ExpectedSequence: 1, Mode: "drive"})
	if err != nil {
		t.Fatal(err)
	}
	if ports.observes != 1 || ports.steps != 0 || result.Outcome != "stopped" || result.Sequence != 1 {
		t.Fatalf("terminal observation racing STOP authorized another Step: observes=%d steps=%d result=%+v", ports.observes, ports.steps, result)
	}
}

func TestReviewWithoutIndependentDecisionWaitsOnOriginalKey(t *testing.T) {
	now := time.Now()
	view := sessions.ControllerDriverView{
		AuthorityVersion: 16,
		Status: sessions.ControllerStatus{
			ID: "invocation-review", ProjectID: "project-review", CheckpointID: "checkpoint-review",
			Sequence: 1, Phase: "review", StartedAt: now.Add(-time.Second), Deadline: now.Add(time.Minute),
		},
		Policy:     sessions.ControllerPolicy{ProjectID: "project-review", Revision: "policy-review", MaxNoProgress: 1, MaxWaves: 4, MaxElapsed: time.Minute},
		Operations: []sessions.ControllerDriverOperation{{Kind: "review", OperationKey: "original-review-key", State: "absent", TaskID: "task-review"}},
	}
	base := &observePumpPorts{view: view}
	ports := &missingReviewDecisionPorts{observePumpPorts: base}
	d, err := New(ports, Limits{MaxTransitions: 8, MaxEffects: 8, MaxWork: 64, MaxNoProgress: 1, MaxElapsed: time.Minute, OperationTimeout: time.Second, MaxSequence: 32})
	if err != nil {
		t.Fatal(err)
	}
	run := &runState{
		driver: d, request: Request{SchemaVersion: 1, ProjectID: view.Status.ProjectID, InvocationID: view.Status.ID, CheckpointID: view.Status.CheckpointID, ExpectedSequence: 1},
		ctx: context.Background(), started: now, view: view, progressLimit: 1, initialPolicyRevision: view.Policy.Revision,
	}
	action := sessions.ControllerAction{ID: "review-action", Kind: "independent_review", TaskID: "task-review", OperationKey: "original-review-key"}
	outcome, reason, next, terminal := run.review(action)
	if !terminal || outcome != "awaiting_external" || reason != "independent_review_decision_required" || next != "provide an independent decision for the same original review key" {
		t.Fatalf("missing decision was not resumable on the original request: %q %q %q terminal=%t", outcome, reason, next, terminal)
	}
	if ports.reviews != 1 || run.effects != 0 {
		t.Fatalf("empty independent response caused repeat work or spent an effect: reviews=%d effects=%d", ports.reviews, run.effects)
	}
}

func TestTerminalPolicyRefusalRemainsRefusedAfterResume(t *testing.T) {
	run := &runState{view: sessions.ControllerDriverView{Status: sessions.ControllerStatus{Terminal: true, HaltClass: "policy_refusal"}}}
	outcome, reason, next, done := run.terminalOrHalt()
	if !done || outcome != "refused" || reason != "policy_refusal" || next != "owner reviews the exact policy refusal" {
		t.Fatalf("retained terminal policy refusal lost its exit contract: %q %q %q done=%t", outcome, reason, next, done)
	}
}

func TestCancelReconciliationSkipsOnlyExactTargetPendingVerification(t *testing.T) {
	now := time.Now()
	request := sessions.VerificationRequest{OperationKey: "verify-target", WorkerSession: "worker", AttemptID: "attempt", LeaseID: "lease", RevisionID: "revision", CheckID: "check", GrantID: "grant", ExpectedFence: 9}
	action := sessions.ControllerAction{ID: "verify-action", Kind: "verification", ActorID: "runner", TaskID: "task", DispatchID: "dispatch-target", AttemptID: "attempt", LeaseID: "lease", RevisionID: "revision", Fence: 9, OperationKey: request.OperationKey, Verification: request}
	view := sessions.ControllerDriverView{
		AuthorityVersion: 16,
		Status:           sessions.ControllerStatus{ID: "invocation-cancel", ProjectID: "project-cancel", CheckpointID: "checkpoint-cancel", Sequence: 2, Phase: "verification", StartedAt: now.Add(-time.Second), Deadline: now.Add(time.Minute), Actions: []sessions.ControllerAction{action}},
		Policy:           sessions.ControllerPolicy{ProjectID: "project-cancel", Revision: "policy-cancel", MaxNoProgress: 1, MaxWaves: 4, MaxElapsed: time.Minute},
		Operations:       []sessions.ControllerDriverOperation{{Kind: "verification", OperationKey: request.OperationKey, State: "pending", ActionID: action.ID, TaskID: action.TaskID, DispatchID: action.DispatchID, Verification: &request}},
	}
	ports := &observePumpPorts{view: view}
	d, err := New(ports, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	run := &runState{
		driver: d, request: Request{SchemaVersion: 1, ProjectID: view.Status.ProjectID, InvocationID: view.Status.ID, CheckpointID: view.Status.CheckpointID, ExpectedSequence: view.Status.Sequence, Mode: "cancel", CancelDispatchID: action.DispatchID},
		ctx: context.Background(), started: now, view: view, progressLimit: 1,
	}
	outcome, _, _, done := run.reconcileOriginals()
	if done || outcome != "" || ports.reconciles != 0 {
		t.Fatalf("cancel must bypass only the exact pending target verification lookup: outcome=%q done=%t reconciles=%d", outcome, done, ports.reconciles)
	}
	unrelated := sessions.ControllerAction{ID: "verify-other", Kind: "verification", TaskID: "task-other", DispatchID: "dispatch-other", OperationKey: "verify-other"}
	run.view.Status.Actions = []sessions.ControllerAction{action, unrelated}
	run.view.Operations = append(run.view.Operations, sessions.ControllerDriverOperation{Kind: "verification", OperationKey: "verify-other", State: "pending", ActionID: unrelated.ID, TaskID: unrelated.TaskID, DispatchID: unrelated.DispatchID})
	outcome, _, _, done = run.reconcileOriginals()
	if !done || (outcome != "awaiting_external" && outcome != "uncertain") || ports.reconciles != 1 {
		t.Fatalf("unrelated pending verification did not block cancellation: outcome=%q done=%t reconciles=%d", outcome, done, ports.reconciles)
	}
}

func TestRunReturnsAwaitingExternalWhenAllDispatchesAreRunning(t *testing.T) {
	now := time.Now()
	action := sessions.ControllerAction{ID: "action-a", Kind: "host_dispatch", TaskID: "task-a", DispatchID: "dispatch-a", IntentID: "intent-a", OperationKey: "host-key-a", ReservationFence: 7}
	ports := &observePumpPorts{view: sessions.ControllerDriverView{
		AuthorityVersion: 16,
		Status:           sessions.ControllerStatus{ID: "invocation", ProjectID: "project", CheckpointID: "checkpoint", Sequence: 2, Phase: "observe", HaltClass: "revision_pending", StartedAt: now.Add(-time.Second), Deadline: now.Add(time.Minute), Actions: []sessions.ControllerAction{action}},
		Policy:           sessions.ControllerPolicy{ProjectID: "project", Revision: "policy", MaxNoProgress: 1, MaxWaves: 4, MaxElapsed: time.Minute},
		Operations:       []sessions.ControllerDriverOperation{{Kind: "dispatch", OperationKey: action.OperationKey, State: "completed", ActionID: action.ID, TaskID: action.TaskID, DispatchID: action.DispatchID, IntentID: action.IntentID, ReservationFence: action.ReservationFence}},
	}}
	d, err := New(ports, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	result, err := d.Run(context.Background(), Request{SchemaVersion: 1, ProjectID: "project", InvocationID: "invocation", CheckpointID: "checkpoint", ExpectedSequence: 2, Mode: "drive"})
	if err != nil {
		t.Fatal(err)
	}
	if ports.steps != 0 || result.Outcome != "awaiting_external" || result.Reason != "revision_pending" {
		t.Fatalf("all-running invocation should wait without another Step: steps=%d result=%+v", ports.steps, result)
	}
}

func TestReservationOnlyDispatchCanAdvanceToFirstIntentStep(t *testing.T) {
	now := time.Now()
	action := sessions.ControllerAction{ID: "reserved", Kind: "dispatch", TaskID: "task", DispatchID: "dispatch", ReservationFence: 7}
	ports := &observePumpPorts{view: sessions.ControllerDriverView{
		AuthorityVersion: 16,
		Status:           sessions.ControllerStatus{ID: "invocation", ProjectID: "project", CheckpointID: "checkpoint", Sequence: 1, Phase: "await_host", StartedAt: now.Add(-time.Second), Deadline: now.Add(time.Minute), Actions: []sessions.ControllerAction{action}},
		Policy:           sessions.ControllerPolicy{ProjectID: "project", Revision: "policy", MaxNoProgress: 1, MaxWaves: 4, MaxElapsed: time.Minute},
		Operations:       []sessions.ControllerDriverOperation{{Kind: "dispatch", OperationKey: "first-host-key", State: "absent", ActionID: action.ID, TaskID: action.TaskID, DispatchID: action.DispatchID, ReservationFence: action.ReservationFence}},
	}}
	d, err := New(ports, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	result, err := d.Run(context.Background(), Request{SchemaVersion: 1, ProjectID: "project", InvocationID: "invocation", CheckpointID: "checkpoint", ExpectedSequence: 1, Mode: "drive"})
	if err != nil {
		t.Fatal(err)
	}
	if ports.reconciles != 0 || ports.steps != 1 || result.Outcome == "uncertain" {
		t.Fatalf("reservation-only absence must reach the next original Step without host lookup: reconciles=%d steps=%d result=%+v", ports.reconciles, ports.steps, result)
	}
}

func TestRunAdvancesAfterCompletedVerification(t *testing.T) {
	now := time.Now()
	verification := sessions.VerificationRequest{OperationKey: "verify-key", WorkerSession: "worker", AttemptID: "attempt", LeaseID: "lease", RevisionID: "revision", CheckID: "check", GrantID: "grant", ExpectedFence: 9}
	action := sessions.ControllerAction{ID: "verify-action", Kind: "verification", ActorID: "runner", TaskID: "task", DispatchID: "dispatch", AttemptID: verification.AttemptID, LeaseID: verification.LeaseID, RevisionID: verification.RevisionID, Fence: verification.ExpectedFence, OperationKey: verification.OperationKey, Verification: verification}
	ports := &observePumpPorts{view: sessions.ControllerDriverView{
		AuthorityVersion: 16,
		Status:           sessions.ControllerStatus{ID: "invocation", ProjectID: "project", CheckpointID: "checkpoint", Sequence: 1, Phase: "verification", StartedAt: now.Add(-time.Second), Deadline: now.Add(time.Minute), Actions: []sessions.ControllerAction{action}},
		Policy:           sessions.ControllerPolicy{ProjectID: "project", Revision: "policy", MaxNoProgress: 1, MaxWaves: 4, MaxElapsed: time.Minute},
		Operations:       []sessions.ControllerDriverOperation{{Kind: "verification", OperationKey: verification.OperationKey, State: "completed", ActionID: action.ID, TaskID: action.TaskID, Verification: &verification}},
	}}
	d, err := New(ports, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	result, err := d.Run(context.Background(), Request{SchemaVersion: 1, ProjectID: "project", InvocationID: "invocation", CheckpointID: "checkpoint", ExpectedSequence: 1, Mode: "drive"})
	if err != nil {
		t.Fatal(err)
	}
	if ports.steps != 1 || result.Outcome != "no_progress" {
		t.Fatalf("completed verification did not advance through canonical Step: steps=%d result=%+v", ports.steps, result)
	}
}

func TestRunAllowsFirstVerificationAfterExactAbsence(t *testing.T) {
	now := time.Now()
	verification := sessions.VerificationRequest{OperationKey: "verify-key", WorkerSession: "worker", AttemptID: "attempt", LeaseID: "lease", RevisionID: "revision", CheckID: "check", GrantID: "grant", ExpectedFence: 9}
	action := sessions.ControllerAction{ID: "verify-action", Kind: "verification", ActorID: "runner", TaskID: "task", DispatchID: "dispatch", AttemptID: verification.AttemptID, LeaseID: verification.LeaseID, RevisionID: verification.RevisionID, Fence: verification.ExpectedFence, OperationKey: verification.OperationKey, Verification: verification}
	ports := &observePumpPorts{view: sessions.ControllerDriverView{
		AuthorityVersion: 16,
		Status:           sessions.ControllerStatus{ID: "invocation", ProjectID: "project", CheckpointID: "checkpoint", Sequence: 1, Phase: "verification", StartedAt: now.Add(-time.Second), Deadline: now.Add(time.Minute), Actions: []sessions.ControllerAction{action}},
		Policy:           sessions.ControllerPolicy{ProjectID: "project", Revision: "policy", MaxNoProgress: 1, MaxWaves: 4, MaxElapsed: time.Minute},
		Operations:       []sessions.ControllerDriverOperation{{Kind: "verification", OperationKey: verification.OperationKey, State: "absent", ActionID: action.ID, TaskID: action.TaskID, Verification: &verification}},
	}}
	d, err := New(ports, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	result, err := d.Run(context.Background(), Request{SchemaVersion: 1, ProjectID: "project", InvocationID: "invocation", CheckpointID: "checkpoint", ExpectedSequence: 1, Mode: "drive"})
	if err != nil {
		t.Fatal(err)
	}
	if ports.verifies != 1 || ports.steps != 1 || ports.reconciles != 0 || result.Outcome != "no_progress" {
		t.Fatalf("safe absence did not permit the original verification once then advance: verifies=%d reconciles=%d steps=%d result=%+v", ports.verifies, ports.reconciles, ports.steps, result)
	}
}

func TestRunBlocksPendingRefOnlyOperationBeforeStep(t *testing.T) {
	now := time.Now()
	ports := &observePumpPorts{view: sessions.ControllerDriverView{
		AuthorityVersion: 16,
		Status:           sessions.ControllerStatus{ID: "invocation", ProjectID: "project", CheckpointID: "checkpoint", Sequence: 1, Phase: "await_host", StartedAt: now.Add(-time.Second), Deadline: now.Add(time.Minute)},
		Policy:           sessions.ControllerPolicy{ProjectID: "project", Revision: "policy", MaxNoProgress: 1, MaxWaves: 4, MaxElapsed: time.Minute},
		Operations:       []sessions.ControllerDriverOperation{{Kind: "integration", OperationKey: "owner-key", State: "pending", TaskID: "task"}},
	}}
	d, err := New(ports, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	result, err := d.Run(context.Background(), Request{SchemaVersion: 1, ProjectID: "project", InvocationID: "invocation", CheckpointID: "checkpoint", ExpectedSequence: 1, Mode: "drive"})
	if err != nil {
		t.Fatal(err)
	}
	if ports.steps != 0 || result.Outcome != "awaiting_external" || result.Reason != "original_operation_pending" {
		t.Fatalf("ref-only pending operation did not block new work: steps=%d result=%+v", ports.steps, result)
	}
}

func TestCancellationReceiptUsesRetainedHostOperationFromAdvancedAction(t *testing.T) {
	action := sessions.ControllerAction{ID: "review", Kind: "independent_review", TaskID: "task", DispatchID: "dispatch", OperationKey: "review-key"}
	view := sessions.ControllerDriverView{
		Status:     sessions.ControllerStatus{ID: "invocation", ProjectID: "project", CheckpointID: "checkpoint"},
		Operations: []sessions.ControllerDriverOperation{{Kind: "dispatch", OperationKey: "original-host-key", State: "completed", DispatchID: action.DispatchID, TaskID: action.TaskID}},
	}
	request := Request{ProjectID: "project", InvocationID: "invocation", CheckpointID: "checkpoint"}
	key, err := OperationKey(request.ProjectID, request.InvocationID, request.CheckpointID, 0, "cancel-ack", "original-host-key")
	if err != nil {
		t.Fatal(err)
	}
	if err := validateCancellationReceipts(view, action, []Receipt{{Kind: "cancellation", OperationKey: key, State: "acknowledged"}}, request); err != nil {
		t.Fatalf("advanced action must bind cancellation to retained host key: %v", err)
	}
	wrong, err := OperationKey(request.ProjectID, request.InvocationID, request.CheckpointID, 0, "cancel-ack", action.OperationKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateCancellationReceipts(view, action, []Receipt{{Kind: "cancellation", OperationKey: wrong, State: "acknowledged"}}, request); err == nil {
		t.Fatal("review operation key was accepted as the original host key")
	}
}

func TestRunEnforcesRetainedTransitionsAgainstProfileCeilings(t *testing.T) {
	now := time.Now()
	ports := &observePumpPorts{view: sessions.ControllerDriverView{
		AuthorityVersion:      16,
		Status:                sessions.ControllerStatus{ID: "invocation", ProjectID: "project", CheckpointID: "checkpoint", Sequence: 3, Phase: "await_host", StartedAt: now.Add(-time.Second), Deadline: now.Add(time.Minute)},
		Policy:                sessions.ControllerPolicy{ProjectID: "project", Revision: "policy", MaxNoProgress: 1, MaxWaves: 4, MaxElapsed: time.Minute},
		HistoricalActionCount: 2,
	}}
	limits := testLimits()
	limits.MaxTransitions = 1
	d, err := New(ports, limits)
	if err != nil {
		t.Fatal(err)
	}
	result, err := d.Run(context.Background(), Request{SchemaVersion: 1, ProjectID: "project", InvocationID: "invocation", CheckpointID: "checkpoint", ExpectedSequence: 1, Mode: "drive"})
	if err != nil {
		t.Fatal(err)
	}
	if ports.steps != 0 || result.Outcome != "limit" || result.Reason != "historical_profile_limit" {
		t.Fatalf("retained transition ceiling reset across resume: steps=%d result=%+v", ports.steps, result)
	}
}

func TestRunRefusesNewStepAtRetainedHistoryCeiling(t *testing.T) {
	now := time.Now()
	ports := &observePumpPorts{view: sessions.ControllerDriverView{
		AuthorityVersion:         16,
		Status:                   sessions.ControllerStatus{ID: "invocation", ProjectID: "project", CheckpointID: "checkpoint", Sequence: 1, Phase: "await_host", StartedAt: now.Add(-time.Second), Deadline: now.Add(time.Minute)},
		Policy:                   sessions.ControllerPolicy{ProjectID: "project", Revision: "policy", MaxNoProgress: 1, MaxWaves: 4, MaxElapsed: time.Minute},
		HistoricalActionCount:    1,
		HistoricalOperationCount: 1,
	}}
	limits := testLimits()
	limits.MaxTransitions = 1
	limits.MaxEffects = 1
	d, err := New(ports, limits)
	if err != nil {
		t.Fatal(err)
	}
	result, err := d.Run(context.Background(), Request{SchemaVersion: 1, ProjectID: "project", InvocationID: "invocation", CheckpointID: "checkpoint", ExpectedSequence: 1, Mode: "drive"})
	if err != nil {
		t.Fatal(err)
	}
	if ports.steps != 0 || result.Outcome != "limit" || result.Reason != "historical_profile_limit" {
		t.Fatalf("new durable step crossed retained history ceiling: steps=%d result=%+v", ports.steps, result)
	}
}

func TestProgressDigestIgnoresCheckpointAndNoProgressCounters(t *testing.T) {
	view := sessions.ControllerDriverView{Status: sessions.ControllerStatus{ID: "invocation", ProjectID: "project", CheckpointID: "checkpoint", Sequence: 4, CheckpointSequence: 2, NoProgress: 1}, CheckpointSequence: 3}
	changed := view
	changed.Status.Sequence++
	changed.Status.CheckpointSequence++
	changed.Status.NoProgress++
	changed.CheckpointSequence++
	if driverProgressDigest(view) != driverProgressDigest(changed) {
		t.Fatal("sequence/checkpoint/no-progress churn counted as producer progress")
	}
}

func TestVerificationReceiptBindsRunnerAndWorkerRevision(t *testing.T) {
	request := sessions.VerificationRequest{OperationKey: "verify-key", WorkerSession: "worker", AttemptID: "attempt", LeaseID: "lease", RevisionID: "revision", CheckID: "check", GrantID: "grant", ExpectedFence: 9}
	action := sessions.ControllerAction{ActorID: "runner"}
	lookup := &sessions.VerificationLookup{State: sessions.VerificationCompleted, Evidence: &sessions.Evidence{IssuerSession: "runner", Provenance: "trusted-local-runner", GrantID: "grant", CheckID: "check", CommandDigest: "command", OutputDigest: "output", Revision: sessions.AttemptRevision{ID: "revision", AttemptID: "attempt", SessionID: "worker", LeaseID: "lease", Fence: 9}}}
	receipts := []Receipt{{Kind: "verification", OperationKey: request.OperationKey, State: "completed", Verification: lookup}}
	if err := validateVerificationReceipts(action, request, receipts); err != nil {
		t.Fatalf("valid runner-issued evidence refused: %v", err)
	}
	lookupCopy := *lookup
	evidenceCopy := *lookup.Evidence
	evidenceCopy.IssuerSession = request.WorkerSession
	lookupCopy.Evidence = &evidenceCopy
	receipts[0].Verification = &lookupCopy
	if err := validateVerificationReceipts(action, request, receipts); err == nil {
		t.Fatal("worker identity was accepted as verifier issuer")
	}
}

func testLimits() Limits {
	return Limits{MaxTransitions: 8, MaxEffects: 8, MaxWork: 64, MaxNoProgress: 1, MaxElapsed: time.Minute, OperationTimeout: time.Second, MaxSequence: 32}
}
