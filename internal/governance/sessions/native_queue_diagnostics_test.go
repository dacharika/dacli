package sessions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"
)

func queueDiagnosticsAuthority(t *testing.T) (*Authority, string, Session, string) {
	t.Helper()
	f := evidenceSetup(t)
	op, token, err := f.a.Enroll(f.owner, Scope{Actions: []string{"operator.read"}, Resources: []string{"workspace:" + f.a.binding.Workspace, "project:project"}}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return f.a, f.owner, op, token
}

func queueCanonical(tasks ...QueueCanonicalTask) QueueCanonicalSnapshot {
	return QueueCanonicalSnapshot{Digest: "canonical-digest", Tasks: tasks}
}

func TestQueueDiagnosticsMixedStagesReconcileAdmission(t *testing.T) {
	now := time.Now().UTC()
	f, _, _ := hostSlotSwarmFixture(t, 2)
	var snapshot state
	if err := f.a.transaction(false, false, func(s *state) error { snapshot = *s; return nil }); err != nil {
		t.Fatal(err)
	}
	s := &snapshot
	s.Tasks = make(map[string]TaskBinding, len(snapshot.Tasks)+6)
	for id, binding := range snapshot.Tasks {
		s.Tasks[id] = binding
	}
	s.Dispatches = make(map[string]DispatchReservation, len(snapshot.Dispatches)+4)
	for id, dispatch := range snapshot.Dispatches {
		s.Dispatches[id] = dispatch
	}
	s.DispatchIntents = make(map[string]DispatchIntent, len(snapshot.DispatchIntents)+1)
	for id, intent := range snapshot.DispatchIntents {
		s.DispatchIntents[id] = intent
	}
	s.LifecycleObservations = make(map[string][]LifecycleObservation, len(snapshot.LifecycleObservations)+1)
	for id, observations := range snapshot.LifecycleObservations {
		s.LifecycleObservations[id] = observations
	}
	s.ControllerInvocations = map[string]controllerInvocation{}
	s.LifecyclePolicies = map[string]LifecyclePolicy{"project": {SchemaVersion: 1, ProjectID: "project", Revision: "lifecycle-r1", Source: "bridge", Freshness: time.Minute}}
	policy := s.SwarmPolicies["project"]
	policy.Placements = make(map[string]SwarmPlacement, len(policy.Placements)+6)
	for id, placement := range s.SwarmPolicies["project"].Placements {
		policy.Placements[id] = placement
	}
	for _, id := range []string{"task-ready", "task-queued", "task-running", "task-check", "task-review", "task-integration"} {
		policy.Placements[id] = SwarmPlacement{Checkout: f.p.Placements["task-a"].Checkout, ImplementKind: "implement", ReviewKind: "review"}
		s.Tasks[id] = TaskBinding{TaskID: id, ProjectID: "project", Repository: f.p.Placements["task-a"].Checkout, AdmissionEnabled: true}
	}
	s.SwarmPolicies["project"] = policy
	dispatchFor := func(task, id string, fence uint64) DispatchReservation {
		d := DispatchReservation{ID: id, ProjectID: "project", PolicyRevision: policy.Revision, Reservation: Lease{ID: id, TaskID: task, Fence: fence, ExpiresAt: now.Add(time.Hour)}}
		s.Dispatches[id] = d
		return d
	}
	running := dispatchFor("task-running", "dispatch-running", 20)
	bridge := Session{ID: "bridge", Scope: Scope{Actions: []string{"dispatch.observe"}, Resources: []string{"project:project"}}, ExpiresAt: now.Add(time.Hour)}
	s.Sessions[bridge.ID] = record{Session: bridge, Hash: stringsOf('c', 64)}
	intent := DispatchIntent{ID: "intent-running", DispatchID: running.ID, BridgeSessionID: bridge.ID, WorkerSessionID: "worker", HostAgentID: "host-agent", State: "acknowledged", Reservation: running}
	s.DispatchIntents[running.ID] = intent
	s.LifecycleObservations[running.ID] = []LifecycleObservation{{Sequence: 1, State: "running", Source: "bridge", Assurance: "cooperative-owner-provisioned-bridge", EvidenceDigest: OutputDigest([]byte("running")), SourceTime: now, ReceivedAt: now, Binding: lifecycleBinding(intent)}}
	integration := dispatchFor("task-integration", "dispatch-integration", 21)
	review := dispatchFor("task-review", "dispatch-review", 22)
	check := dispatchFor("task-check", "dispatch-check", 23)
	s.ControllerInvocations["controller"] = controllerInvocation{Status: ControllerStatus{ProjectID: "project", Actions: []ControllerAction{
		{ID: "integration-action", OperationKey: "integration-op", Kind: "integration_checkpoint", TaskID: "task-integration", DispatchID: integration.ID, ReservationFence: integration.Reservation.Fence},
		{ID: "review-action", OperationKey: "review-op", Kind: "independent_review", TaskID: "task-review", DispatchID: review.ID, ReservationFence: review.Reservation.Fence},
		{ID: "check-action", OperationKey: "check-op", Kind: "verification", TaskID: "task-check", DispatchID: check.ID, AttemptID: "attempt-check", LeaseID: "lease-check", Fence: 24, ReservationFence: check.Reservation.Fence, Verification: VerificationRequest{OperationKey: "check-op", AttemptID: "attempt-check", LeaseID: "lease-check", ExpectedFence: 24}},
	}}}
	tasks := []QueueCanonicalTask{
		{TaskID: "task-ready", ProjectID: "project", Status: "ready", Resources: []Resource{{Kind: "path", Path: "ready/file.go"}}},
		{TaskID: "task-queued", ProjectID: "project", Status: "blocked", Reason: "dependencies_unsatisfied", Resources: []Resource{{Kind: "path", Path: "queued/file.go"}}},
		{TaskID: "task-running", ProjectID: "project", Status: "active", Resources: []Resource{{Kind: "path", Path: "running/file.go"}}},
		{TaskID: "task-check", ProjectID: "project", Status: "active", Resources: []Resource{{Kind: "path", Path: "check/file.go"}}},
		{TaskID: "task-review", ProjectID: "project", Status: "active", Resources: []Resource{{Kind: "path", Path: "review/file.go"}}},
		{TaskID: "task-integration", ProjectID: "project", Status: "active", Resources: []Resource{{Kind: "path", Path: "integration/file.go"}}},
		{TaskID: "task-terminal", ProjectID: "project", Status: "done"},
	}
	counts := QueueDiagnosticsCounts{}
	rows, terminal, err := queueProjectRows(context.Background(), s, queueCanonical(tasks...), "project", "", now, &counts)
	if err != nil {
		t.Fatal(err)
	}
	want := QueueDiagnosticsCounts{Queued: 2, Running: 1, Check: 1, Review: 1, Integration: 1}
	if counts != want || len(rows) != 6 || terminal != 1 {
		t.Fatalf("projection counts=%+v rows=%d terminal=%d; want %+v/6/1", counts, len(rows), terminal, want)
	}
	for _, row := range rows {
		if row.QueueEnteredAt != nil || row.QueueAgeSeconds != nil {
			t.Fatalf("invented queue-entry timestamp: %+v", row)
		}
	}
}

func TestQueueDiagnosticsAdmissionHoldsAndRoleUnknowns(t *testing.T) {
	f, _, _ := hostSlotSwarmFixture(t, 2)
	var snapshot state
	if err := f.a.transaction(false, false, func(s *state) error { snapshot = *s; return nil }); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	s := &snapshot
	const task = "task-probe"
	repository := f.p.Placements["task-a"].Checkout
	s.Tasks[task] = TaskBinding{TaskID: task, ProjectID: "project", Repository: repository, AdmissionEnabled: true}
	policy := s.SwarmPolicies["project"]
	policy.Placements[task] = SwarmPlacement{Checkout: repository, ImplementKind: "implement", ReviewKind: "review"}
	s.SwarmPolicies["project"] = policy
	canonical := QueueCanonicalTask{TaskID: task, ProjectID: "project", Status: "ready", Resources: []Resource{{Kind: "path", Path: "src/owned.go"}}}
	stage, reasons, _ := queueClassifyTask(s, canonical, now)
	if stage != "queued" || !queueHasReason(reasons, "preflight_unknown") {
		t.Fatalf("schema 22 task without current preflight must remain unknown: %s %+v", stage, reasons)
	}

	// A real retained reservation contributes path provenance independently of
	// canonical readiness and becomes an explicit writer conflict.
	s.Dispatches["dispatch-hold"] = DispatchReservation{ID: "dispatch-hold", ProjectID: "project", Reservation: Lease{ID: "dispatch-hold", TaskID: "task-a", Repository: repository, Resources: []Resource{{Kind: "path", Path: "src/owned.go"}}, ExpiresAt: now.Add(time.Hour)}}
	stage, reasons, _ = queueClassifyTask(s, canonical, now)
	if stage != "queued" || !queueHasReason(reasons, "path_hold") {
		t.Fatalf("overlapping retained writer was not exposed: %s %+v", stage, reasons)
	}
	sibling := canonical
	sibling.Resources = []Resource{{Kind: "path", Path: "src/other.go"}}
	stage, reasons, _ = queueClassifyTask(s, sibling, now)
	if stage != "queued" || !queueHasReason(reasons, "preflight_unknown") || queueHasReason(reasons, "path_hold") {
		t.Fatalf("sibling paths were treated as overlapping: %s %+v", stage, reasons)
	}

	ancestor := *s
	ancestor.Dispatches = make(map[string]DispatchReservation, len(s.Dispatches))
	for id, dispatch := range s.Dispatches {
		ancestor.Dispatches[id] = dispatch
	}
	ancestor.Dispatches["dispatch-ancestor"] = DispatchReservation{ID: "dispatch-ancestor", ProjectID: "project", Reservation: Lease{ID: "dispatch-ancestor", TaskID: "task-b", Repository: repository, Resources: []Resource{{Kind: "path", Path: "src"}}, ExpiresAt: now.Add(time.Hour)}}
	stage, reasons, _ = queueClassifyTask(&ancestor, canonical, now)
	if stage != "queued" || !queueHasReason(reasons, "path_hold") {
		t.Fatalf("ancestor path was not treated as overlapping: %s %+v", stage, reasons)
	}

	crossProject := *s
	crossProject.Dispatches = map[string]DispatchReservation{"foreign-dispatch-secret": {ID: "foreign-dispatch-secret", ProjectID: "foreign-project", Reservation: Lease{ID: "foreign-dispatch-secret", TaskID: "foreign-task-secret", Repository: repository, Resources: []Resource{{Kind: "path", Path: "src/owned.go"}}, ExpiresAt: now.Add(time.Hour)}}}
	stage, reasons, _ = queueClassifyTask(&crossProject, canonical, now)
	if stage != "queued" || !queueHasReason(reasons, "path_hold") {
		t.Fatalf("cross-project same-repository holder was missed: %s %+v", stage, reasons)
	}
	rows, _, err := queueProjectRows(context.Background(), &crossProject, queueCanonical(canonical), "project", "", now, &QueueDiagnosticsCounts{})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(rows)
	if err != nil || bytes.Contains(encoded, []byte("foreign-task-secret")) || bytes.Contains(encoded, []byte("foreign-dispatch-secret")) {
		t.Fatalf("cross-project writer identity escaped projection: %s err=%v", encoded, err)
	}

	checkout := canonical
	checkout.Resources = []Resource{{Kind: "checkout"}}
	s.Dispatches["dispatch-checkout-hold"] = DispatchReservation{ID: "dispatch-checkout-hold", ProjectID: "project", Reservation: Lease{ID: "dispatch-checkout-hold", TaskID: "task-b", Repository: repository, Resources: []Resource{{Kind: "checkout"}}, ExpiresAt: now.Add(time.Hour)}}
	stage, reasons, _ = queueClassifyTask(s, checkout, now)
	if stage != "queued" || !queueHasReason(reasons, "checkout_hold") {
		t.Fatalf("checkout writer was not exposed: %s %+v", stage, reasons)
	}

	sameCheckout := *s
	sameCheckout.Dispatches = map[string]DispatchReservation{"different-checkout-claim": {ID: "different-checkout-claim", ProjectID: "project", Reservation: Lease{ID: "different-checkout-claim", TaskID: "task-b", Repository: repository, Checkout: policy.Placements[task].Checkout, Resources: []Resource{{Kind: "path", Path: "elsewhere.go"}}, ExpiresAt: now.Add(time.Hour)}}}
	stage, reasons, _ = queueClassifyTask(&sameCheckout, canonical, now)
	if stage != "queued" || !queueHasReason(reasons, "checkout_hold") {
		t.Fatalf("equal checkout with disjoint paths was not blocked: %s %+v", stage, reasons)
	}

	missingBinding := *s
	missingBinding.Tasks = make(map[string]TaskBinding, len(s.Tasks))
	for id, binding := range s.Tasks {
		missingBinding.Tasks[id] = binding
	}
	delete(missingBinding.Tasks, task)
	stage, reasons, _ = queueClassifyTask(&missingBinding, canonical, now)
	if stage != "queued" || !queueHasReason(reasons, "owner_binding_missing") {
		t.Fatalf("missing owner binding was not blocked: %s %+v", stage, reasons)
	}

	missingRole := *s
	missingRolePolicy := policy
	missingRolePolicy.Placements = make(map[string]SwarmPlacement, len(policy.Placements))
	for id, placement := range policy.Placements {
		missingRolePolicy.Placements[id] = placement
	}
	delete(missingRolePolicy.Placements, task)
	missingRole.SwarmPolicies = make(map[string]SwarmPolicy, len(s.SwarmPolicies))
	for id, p := range s.SwarmPolicies {
		missingRole.SwarmPolicies[id] = p
	}
	missingRole.SwarmPolicies["project"] = missingRolePolicy
	stage, reasons, _ = queueClassifyTask(&missingRole, canonical, now)
	if stage != "queued" || !queueHasReason(reasons, "compatible_role_missing") {
		t.Fatalf("missing compatible role was not blocked: %s %+v", stage, reasons)
	}

	roleFull := *s
	roleFull.SwarmPolicies = make(map[string]SwarmPolicy, len(s.SwarmPolicies))
	for id, p := range s.SwarmPolicies {
		roleFull.SwarmPolicies[id] = p
	}
	fullPolicy := roleFull.SwarmPolicies["project"]
	fullPolicy.Roles = append([]SwarmRole(nil), policy.Roles...)
	for i := range fullPolicy.Roles {
		if fullPolicy.Roles[i].Kind == "implement" || fullPolicy.Roles[i].Kind == "review" {
			fullPolicy.Roles[i].Capacity = 1
		}
	}
	roleFull.SwarmPolicies["project"] = fullPolicy
	roleFull.Dispatches = map[string]DispatchReservation{"role-capacity-holder": {ID: "role-capacity-holder", ProjectID: "project", ImplementationRole: "codex-implement", ReviewRole: "codex-review", Reservation: Lease{ID: "role-capacity-holder", TaskID: "task-a", ExpiresAt: now.Add(time.Hour)}}}
	stage, reasons, _ = queueClassifyTask(&roleFull, canonical, now)
	if stage != "queued" || !queueHasReason(reasons, "compatible_role_missing") {
		t.Fatalf("fully occupied compatible role pair was treated as available: %s %+v", stage, reasons)
	}

	staleRouting := *s
	staleRouting.ModelRoutingPolicies = map[string]NativeModelRoutingPolicy{"project": {
		ProjectID: "project", Revision: "routing-r1", ExpiresAt: now.Add(time.Hour),
		Tasks: map[string]NativeModelRoutingTask{task: {Candidates: []NativeModelRoutingCandidate{{Model: "model-a", Available: true, ObservedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Hour)}}}},
	}}
	code, knowledge := queueRolePolicyReason(&staleRouting, "project", task, now)
	if code != "role_model_unknown" || knowledge != "unknown" {
		t.Fatalf("expired routing observation was asserted unavailable: %s/%s", code, knowledge)
	}
	knownUnavailable := staleRouting
	knownUnavailable.ModelRoutingPolicies = map[string]NativeModelRoutingPolicy{"project": {
		ProjectID: "project", Revision: "routing-r2", ExpiresAt: now.Add(time.Hour),
		Tasks: map[string]NativeModelRoutingTask{task: {Candidates: []NativeModelRoutingCandidate{{Model: "model-a", Available: false, ObservedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour)}}}},
	}}
	code, knowledge = queueRolePolicyReason(&knownUnavailable, "project", task, now)
	if code != "model_unavailable" || knowledge != "known" {
		t.Fatalf("fresh negative routing observation was not reported as unavailable: %s/%s", code, knowledge)
	}

	noSlots := *s
	noSlotsPolicy := policy
	noSlotsPolicy.HostSlots = nil
	noSlots.SwarmPolicies = make(map[string]SwarmPolicy, len(s.SwarmPolicies))
	for id, p := range s.SwarmPolicies {
		noSlots.SwarmPolicies[id] = p
	}
	noSlots.SwarmPolicies["project"] = noSlotsPolicy
	stage, reasons, freshness := queueClassifyTask(&noSlots, canonical, now)
	if stage != "queued" || freshness != "unknown" || !queueHasReason(reasons, "host_capacity_unknown") {
		t.Fatalf("missing slot policy was treated as ready: %s %s %+v", stage, freshness, reasons)
	}
}

func queueHasReason(reasons []QueueDiagnosticReason, code string) bool {
	for _, reason := range reasons {
		if reason.Code == code {
			return true
		}
	}
	return false
}

func TestQueueDiagnosticsAdmissionCapPlusOneReservationIsAtomic(t *testing.T) {
	f, _, _ := hostSlotSwarmFixture(t, 1)
	plan := swarmPlan(t, f)
	beforeFence := uint64(0)
	if err := f.a.transaction(false, false, func(s *state) error { beforeFence = s.Fence; return nil }); err != nil {
		t.Fatal(err)
	}
	_, reserveErr := f.a.ReserveWave(f.token, f.s.ID, ReserveWaveRequest{OperationKey: "queue-diagnostics-cap-plus-one", Plan: plan, ExpiresAt: time.Now().Add(10 * time.Minute)})
	if !errors.Is(reserveErr, ErrDenied) {
		t.Fatalf("cap+1 reservation should be refused: %v", reserveErr)
	}
	if err := f.a.transaction(false, false, func(s *state) error {
		view := hostSlotAdmissionView(s, "project", time.Now())
		if !view.Known || view.RemainingAdmissibleSlots != 1 || len(s.Dispatches) != 0 || s.Fence != beforeFence || s.SwarmOperations["queue-diagnostics-cap-plus-one"].Wave != nil {
			t.Fatalf("failed cap+1 write affected observed state: view=%+v dispatches=%d fence=%d->%d receipt=%+v", view, len(s.Dispatches), beforeFence, s.Fence, s.SwarmOperations["queue-diagnostics-cap-plus-one"])
		}
		hosts := queueHostDiagnostics(s, "project", time.Now())
		if len(hosts) != 1 || hosts[0].State != "current" || hosts[0].RemainingAdmissibleSlots == nil || *hosts[0].RemainingAdmissibleSlots != 1 {
			t.Fatalf("diagnostics disagree with post-refusal admission: %+v", hosts)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestQueueDiagnosticsCanonicalActiveOccupiesRoleCapacity(t *testing.T) {
	f, _, _ := hostSlotSwarmFixture(t, 2)
	var s state
	if err := f.a.transaction(false, false, func(st *state) error { s = *st; return nil }); err != nil {
		t.Fatal(err)
	}
	const activeTask, candidateTask = "unbound-active-task", "ready-role-candidate"
	repository := f.p.Placements["task-a"].Checkout
	s.Tasks[candidateTask] = TaskBinding{TaskID: candidateTask, ProjectID: "project", Repository: repository, AdmissionEnabled: true}
	policy := s.SwarmPolicies["project"]
	policy.Roles = append([]SwarmRole(nil), policy.Roles...)
	for i := range policy.Roles {
		if policy.Roles[i].Kind == "implement" || policy.Roles[i].Kind == "review" {
			policy.Roles[i].Capacity = 1
		}
	}
	policy.Placements = make(map[string]SwarmPlacement, len(policy.Placements)+1)
	for id, placement := range s.SwarmPolicies["project"].Placements {
		policy.Placements[id] = placement
	}
	policy.Placements[candidateTask] = SwarmPlacement{Checkout: repository, ImplementKind: "implement", ReviewKind: "review"}
	s.SwarmPolicies["project"] = policy
	canonical := queueCanonical(
		QueueCanonicalTask{TaskID: activeTask, ProjectID: "project", Status: "active"},
		QueueCanonicalTask{TaskID: candidateTask, ProjectID: "project", Status: "ready", Resources: []Resource{{Kind: "path", Path: "candidate.go"}}},
	)
	used, occupied, _ := swarmOccupancy(&s, policy, SwarmSnapshot{Candidates: []SwarmCandidate{{TaskID: activeTask, Status: "active"}}})
	if used["codex-implement"] != 1 || used["codex-review"] != 1 || !occupied[activeTask] {
		t.Fatalf("fixture canonical active occupancy is not admission-representative: used=%v occupied=%v", used, occupied)
	}
	counts := QueueDiagnosticsCounts{}
	rows, _, err := queueProjectRows(context.Background(), &s, canonical, "project", "", time.Now(), &counts)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.TaskID == candidateTask {
			if row.Stage != "queued" || !queueHasReason(row.Reasons, "compatible_role_missing") {
				t.Fatalf("diagnostic omitted canonical active role occupancy: %+v", row)
			}
			return
		}
	}
	t.Fatalf("canonical candidate row missing: %+v", rows)
}

func TestQueueDiagnosticsMissingMandatoryPreflightIsUnknown(t *testing.T) {
	f, _, _ := hostSlotSwarmFixture(t, 2)
	var s state
	if err := f.a.transaction(false, false, func(st *state) error { s = *st; return nil }); err != nil {
		t.Fatal(err)
	}
	const task = "missing-preflight-task"
	repository := f.p.Placements["task-a"].Checkout
	s.Tasks[task] = TaskBinding{TaskID: task, ProjectID: "project", Repository: repository, AdmissionEnabled: true}
	policy := s.SwarmPolicies["project"]
	policy.Placements = make(map[string]SwarmPlacement, len(policy.Placements)+1)
	for id, placement := range s.SwarmPolicies["project"].Placements {
		policy.Placements[id] = placement
	}
	policy.Placements[task] = SwarmPlacement{Checkout: repository, ImplementKind: "implement", ReviewKind: "review"}
	s.SwarmPolicies["project"] = policy
	delete(s.PreflightPolicies["project"].Tasks, task)
	stage, reasons, _ := queueClassifyTask(&s, QueueCanonicalTask{TaskID: task, ProjectID: "project", Status: "ready", Resources: []Resource{{Kind: "path", Path: "safe.go"}}}, time.Now())
	if stage != "queued" || !queueHasReason(reasons, "preflight_unknown") {
		t.Fatalf("missing mandatory preflight was reported ready: stage=%s reasons=%+v", stage, reasons)
	}
	expired := s.PreflightPolicies["project"]
	expired.ExpiresAt = time.Now().Add(-time.Minute)
	s.PreflightPolicies["project"] = expired
	stage, reasons, _ = queueClassifyTask(&s, QueueCanonicalTask{TaskID: task, ProjectID: "project", Status: "ready", Resources: []Resource{{Kind: "path", Path: "safe.go"}}}, time.Now())
	if stage != "queued" || !queueHasReason(reasons, "preflight_unknown") {
		t.Fatalf("expired mandatory preflight was reported ready: stage=%s reasons=%+v", stage, reasons)
	}
}

func TestQueueDiagnosticsMixedStaleModelCandidatesRemainUnknown(t *testing.T) {
	f, _, _ := hostSlotSwarmFixture(t, 2)
	var s state
	if err := f.a.transaction(false, false, func(st *state) error { s = *st; return nil }); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	s.ModelRoutingPolicies = map[string]NativeModelRoutingPolicy{"project": {
		ProjectID: "project", Revision: "routing-mixed", ExpiresAt: now.Add(time.Hour),
		Tasks: map[string]NativeModelRoutingTask{"task-a": {Candidates: []NativeModelRoutingCandidate{
			{Model: "model-negative", Available: false, ObservedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour)},
			{Model: "model-unknown", Available: true, ObservedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Hour)},
		}}},
	}}
	code, knowledge := queueRolePolicyReason(&s, "project", "task-a", now)
	if code != "role_model_unknown" || knowledge != "unknown" {
		t.Fatalf("fresh negative masked an unknown routing candidate: %s/%s", code, knowledge)
	}
}

func TestQueueDiagnosticsRetiredPendingVerificationIsHistorical(t *testing.T) {
	now := time.Now().UTC()
	request := VerificationRequest{OperationKey: "verification-op", WorkerSession: "worker", AttemptID: "attempt-v", LeaseID: "lease-v", RevisionID: "revision-v", CheckID: "check-v", GrantID: "grant-v", ExpectedFence: 7}
	binding := &CheckpointVerificationBinding{ProjectID: "project", TaskID: "task-v", RunnerSession: "runner", DescriptorDigest: "descriptor-v", Request: request}
	lease := Lease{ID: request.LeaseID, AttemptID: request.AttemptID, SessionID: request.WorkerSession, TaskID: "task-v", Fence: request.ExpectedFence, Repository: "/repo", Checkout: "/repo/task-v", Released: false, ExpiresAt: now.Add(time.Hour)}
	attempt := Attempt{ID: request.AttemptID, SessionID: request.WorkerSession, ProjectID: "project", Request: AttemptRequest{TaskID: "task-v"}}
	op := operation{Pending: true, Digest: identity("run:"+binding.DescriptorDigest, binding.RunnerSession, request), CheckpointVerification: binding}
	s := &state{Version: 22, Binding: Binding{Workspace: "workspace", Installation: "installation"}, Tasks: map[string]TaskBinding{"task-v": {TaskID: "task-v", ProjectID: "project", Repository: "/repo", AdmissionEnabled: true}}, Attempts: map[string]Attempt{attempt.ID: attempt}, Leases: map[string]Lease{lease.ID: lease}, Operations: map[string]operation{request.OperationKey: op}, Recoveries: map[string]RecoveryReceipt{}}
	canonical := queueCanonical(QueueCanonicalTask{TaskID: "task-v", ProjectID: "project", Status: "ready", Resources: []Resource{{Kind: "path", Path: "src/task-v.go"}}})
	rows, _, err := queueProjectRows(context.Background(), s, canonical, "project", "", now, &QueueDiagnosticsCounts{})
	if err != nil || len(rows) != 1 || rows[0].Stage != "check" {
		t.Fatalf("current exact pending verification should be a check stage: rows=%+v err=%v", rows, err)
	}
	lease.Released = true
	s.Leases[lease.ID] = lease
	target := RecoveryTarget{Workspace: "workspace", Kind: "lease", TaskID: lease.TaskID, AttemptID: lease.AttemptID, ProtectionID: lease.ID, SessionID: lease.SessionID, Repository: lease.Repository, Checkout: lease.Checkout, ExpectedFence: lease.Fence}
	s.Recoveries["lease:"+lease.ID] = RecoveryReceipt{ID: "recovery-v", Target: target}
	rows, _, err = queueProjectRows(context.Background(), s, canonical, "project", "", now, &QueueDiagnosticsCounts{})
	if err != nil || len(rows) != 1 || rows[0].Stage == "check" || !queueHasReason(rows[0].Reasons, "verification_unknown") {
		t.Fatalf("exactly retired pending verifier remained a current check: rows=%+v err=%v", rows, err)
	}
}

func TestQueueDiagnosticsProductionReadSurvivesCapRefusalAndReopen(t *testing.T) {
	f, _, _ := hostSlotSwarmFixture(t, 1)
	plan := swarmPlan(t, f)
	_, reserveErr := f.a.ReserveWave(f.token, f.s.ID, ReserveWaveRequest{OperationKey: "queue-diagnostics-reopen-cap", Plan: plan, ExpiresAt: time.Now().Add(10 * time.Minute)})
	if !errors.Is(reserveErr, ErrDenied) {
		t.Fatalf("cap+1 reservation should be refused: %v", reserveErr)
	}
	operator, token, err := f.a.Enroll(f.owner, Scope{Actions: []string{"operator.read"}, Resources: []string{"workspace:" + f.a.binding.Workspace, "project:project"}}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	canonical := queueCanonical(
		QueueCanonicalTask{TaskID: "task-a", ProjectID: "project", Status: "ready", Resources: []Resource{{Kind: "path", Path: "task-a.go"}}},
		QueueCanonicalTask{TaskID: "task-b", ProjectID: "project", Status: "ready", Resources: []Resource{{Kind: "path", Path: "task-b.go"}}},
	)
	read := func(authority *Authority) QueueDiagnosticsSnapshot {
		t.Helper()
		out, readErr := authority.ReadQueueDiagnostics(context.Background(), token, operator.ID, "project", "", 50, func(context.Context) (QueueCanonicalSnapshot, error) { return canonical, nil })
		if readErr != nil {
			t.Fatal(readErr)
		}
		return out
	}
	first := read(f.a)
	if first.TotalCount != 2 || len(first.Hosts) != 1 || first.Hosts[0].State != "current" || first.Hosts[0].RemainingAdmissibleSlots == nil || *first.Hosts[0].RemainingAdmissibleSlots != 1 {
		t.Fatalf("production read disagrees with cap refusal: %+v", first)
	}
	reopened, err := OpenQueueDiagnosticsAuthority(f.a.path, f.a.binding)
	if err != nil {
		t.Fatal(err)
	}
	second := read(reopened)
	first.ObservedAt, second.ObservedAt = time.Time{}, time.Time{}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("reopened authority changed queue projection: first=%+v second=%+v", first, second)
	}
}

func TestQueueDiagnosticsReadDoesNotCreateLockOrInvokeProducers(t *testing.T) {
	a, _, operator, token := queueDiagnosticsAuthority(t)
	reader, err := OpenQueueDiagnosticsAuthority(a.path, a.binding)
	if err != nil {
		t.Fatal(err)
	}
	lock := a.path + ".lock"
	if err := os.WriteFile(lock, []byte("preexisting"), 0o600); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Remove(lock); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Errorf("remove preexisting lock fixture: %v", err)
		}
	}()
	beforeAuthority, err := os.ReadFile(a.path)
	if err != nil {
		t.Fatal(err)
	}
	beforeLock, err := os.ReadFile(lock)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	got, err := reader.ReadQueueDiagnostics(context.Background(), token, operator.ID, "project", "", 50, func(context.Context) (QueueCanonicalSnapshot, error) {
		calls++
		return queueCanonical(QueueCanonicalTask{TaskID: "task-a", ProjectID: "project", Status: "ready"}), nil
	})
	if err != nil || calls != 1 || got.TotalCount != 1 || got.Rows[0].TaskID != "task-a" {
		t.Fatalf("read result=%+v calls=%d err=%v", got, calls, err)
	}
	afterAuthority, err := os.ReadFile(a.path)
	if err != nil || !bytes.Equal(beforeAuthority, afterAuthority) {
		t.Fatal("queue read changed authority bytes", err)
	}
	afterLock, err := os.ReadFile(lock)
	if err != nil || !bytes.Equal(beforeLock, afterLock) {
		t.Fatal("queue read changed or waited on the existing lock", err)
	}
}

func TestQueueDiagnosticsScopeAndRedaction(t *testing.T) {
	a, owner, operator, token := queueDiagnosticsAuthority(t)
	reader, err := OpenQueueDiagnosticsAuthority(a.path, a.binding)
	if err != nil {
		t.Fatal(err)
	}
	plain, plainToken, err := a.Enroll(owner, Scope{Actions: []string{"read"}, Resources: []string{"workspace:" + a.binding.Workspace, "project:project"}}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	taskOnly, taskToken, err := a.Enroll(owner, Scope{Actions: []string{"operator.read"}, Resources: []string{"workspace:" + a.binding.Workspace, "task:task-a"}}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	foreign, foreignToken, err := a.Enroll(owner, Scope{Actions: []string{"operator.read"}, Resources: []string{"workspace:" + a.binding.Workspace, "project:foreign"}}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	callbackCalls := 0
	callback := func(context.Context) (QueueCanonicalSnapshot, error) {
		callbackCalls++
		return queueCanonical(
			QueueCanonicalTask{TaskID: "task-a", ProjectID: "project", Status: "ready"},
			QueueCanonicalTask{TaskID: "foreign-secret-task", ProjectID: "foreign", Status: "ready", Reason: "SECRET_DISPATCH_SESSION_PROFILE"},
		), nil
	}
	for _, identity := range []struct{ session, token string }{{plain.ID, plainToken}, {taskOnly.ID, taskToken}, {foreign.ID, foreignToken}} {
		_, err := reader.ReadQueueDiagnostics(context.Background(), identity.token, identity.session, "project", "", 50, callback)
		if !errors.Is(err, ErrDenied) {
			t.Fatalf("insufficient scope accepted: %v", err)
		}
	}
	if callbackCalls != 0 {
		t.Fatalf("canonical source invoked before operator project authorization: %d", callbackCalls)
	}
	out, err := reader.ReadQueueDiagnostics(context.Background(), token, operator.ID, "project", "", 50, callback)
	if err != nil || out.TotalCount != 1 {
		t.Fatalf("authorized projection failed: %+v %v", out, err)
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"foreign-secret-task", "SECRET_DISPATCH_SESSION_PROFILE", token, foreignToken} {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatalf("foreign or secret metadata escaped: %q", secret)
		}
	}
	_, err = reader.ReadQueueDiagnostics(context.Background(), token, operator.ID, "project", "foreign-secret-task", 50, callback)
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("foreign exact task filter was not refused: %v", err)
	}
}

func TestQueueDiagnosticsRejectsAuthorityChangeAndRevocation(t *testing.T) {
	a, owner, operator, token := queueDiagnosticsAuthority(t)
	reader, err := OpenQueueDiagnosticsAuthority(a.path, a.binding)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	_, err = reader.ReadQueueDiagnostics(context.Background(), token, operator.ID, "project", "", 50, func(context.Context) (QueueCanonicalSnapshot, error) {
		called = true
		if revokeErr := a.Revoke(owner, operator.ID); revokeErr != nil {
			t.Fatal(revokeErr)
		}
		return queueCanonical(QueueCanonicalTask{TaskID: "task-a", ProjectID: "project", Status: "ready"}), nil
	})
	if !called || !errors.Is(err, ErrDenied) {
		t.Fatalf("revocation during callback produced data: called=%v err=%v", called, err)
	}
}

func TestQueueDiagnosticsRejectsExpiryAndDeadlineAfterCanonicalRead(t *testing.T) {
	a, owner, operator, token := queueDiagnosticsAuthority(t)
	reader, err := OpenQueueDiagnosticsAuthority(a.path, a.binding)
	if err != nil {
		t.Fatal(err)
	}
	short, shortToken, err := a.Enroll(owner, operator.Scope, time.Now().Add(35*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	_, err = reader.ReadQueueDiagnostics(context.Background(), shortToken, short.ID, "project", "", 50, func(context.Context) (QueueCanonicalSnapshot, error) {
		time.Sleep(50 * time.Millisecond)
		return queueCanonical(QueueCanonicalTask{TaskID: "task-a", ProjectID: "project", Status: "ready"}), nil
	})
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("expired principal produced a snapshot: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	called := false
	_, err = reader.ReadQueueDiagnostics(ctx, token, operator.ID, "project", "", 50, func(context.Context) (QueueCanonicalSnapshot, error) {
		called = true
		time.Sleep(20 * time.Millisecond)
		return queueCanonical(QueueCanonicalTask{TaskID: "task-a", ProjectID: "project", Status: "ready"}), nil
	})
	if !called || !errors.Is(err, ErrDenied) {
		t.Fatalf("deadline did not refuse completed callback result: called=%v err=%v", called, err)
	}
}

func TestQueueDiagnosticsStaleCensusCannotBeCurrent(t *testing.T) {
	f, _, _ := hostSlotSwarmFixture(t, 2)
	if err := f.a.transaction(false, false, func(s *state) error {
		copy := *s
		copy.CapacityObservations = make(map[string]HostCapacityObservation, len(s.CapacityObservations))
		for k, v := range s.CapacityObservations {
			copy.CapacityObservations[k] = v
		}
		for k, v := range copy.CapacityObservations {
			v.ReceivedAt = time.Now().Add(-time.Hour)
			copy.CapacityObservations[k] = v
		}
		hosts := queueHostDiagnostics(&copy, "project", time.Now())
		if len(hosts) != 1 || hosts[0].Freshness != "stale" || hosts[0].State == "current" || hosts[0].RemainingAdmissibleSlots != nil || hosts[0].ObservedOccupied != nil {
			t.Fatalf("stale census presented as current capacity: %+v", hosts)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestQueueDiagnosticsTerminalProofRetainsExecutionClaims(t *testing.T) {
	const project, domain, dispatchID = "project-1", "domain-1", "dispatch-1"
	qualified := HostSlotTerminalQualification{SchemaVersion: 1, ID: "terminal-qual-1", State: "supported", Source: "owner-bridge", EvidenceDigest: stringsOf('d', 64), AuthorityDomainID: domain, HostID: "host-1", HostSessionID: "host-session", ExecutorInstanceID: "executor-1", Runtime: "codex", Build: "build-1", CapabilityDigest: stringsOf('a', 64), QualifiedAt: time.Now().Add(-time.Minute), ExpiresAt: time.Now().Add(time.Hour)}
	slots := HostSlotAdmissionPolicy{SchemaVersion: 1, Mode: HostSlotSingleAuthority, Revision: "slots-r1", AuthorityDomainID: domain, WorkspaceID: "workspace", InstallationID: "installation", HostID: qualified.HostID, HostSessionID: qualified.HostSessionID, ExecutorInstanceID: qualified.ExecutorInstanceID, Runtime: qualified.Runtime, Build: qualified.Build, CapabilityDigest: qualified.CapabilityDigest, CapacityPolicyRevision: "capacity-r1", QualificationID: "capacity-qual", TerminalObservationSource: qualified.Source, TerminalQualificationID: qualified.ID, TerminalQualification: &qualified}
	dispatch := DispatchReservation{ID: dispatchID, ProjectID: project, PolicyRevision: "swarm-r1", Converted: true, WorkerSessionID: "worker", Reservation: Lease{ID: dispatchID, TaskID: "task-1", Fence: 9}, HostSlot: &HostSlotClaim{SchemaVersion: 1, AuthorityDomainID: domain, HostID: qualified.HostID, HostSessionID: qualified.HostSessionID, ExecutorInstanceID: qualified.ExecutorInstanceID, CapabilityDigest: qualified.CapabilityDigest, DispatchID: dispatchID, DispatchFence: 9, Units: 1, SwarmPolicyRevision: "swarm-r1", QualificationID: "capacity-qual"}}
	intent := DispatchIntent{ID: "intent-1", DispatchID: dispatchID, OperationKey: "intent-op", BridgeSessionID: "bridge", HostAgentID: "agent-1", WorkerSessionID: "worker", Reservation: dispatch}
	policy := SwarmPolicy{ProjectID: project, Revision: "swarm-r1", HostSlots: &slots}
	proof := &HostSlotTerminalProof{AuthorityDomainID: domain, DispatchID: dispatchID, ReservationFence: 9, IntentID: intent.ID, HostAgentID: intent.HostAgentID, WorkerSessionID: intent.WorkerSessionID, BridgeSessionID: "bridge", Source: qualified.Source, QualificationID: qualified.ID, QualificationDigest: hostSlotTerminalQualificationDigest(qualified), QualificationPolicyRevision: policy.Revision}
	observed := LifecycleObservation{Binding: lifecycleBinding(intent), State: "completed", Source: qualified.Source, ReceivedAt: time.Now(), HostSlotTerminal: proof}
	// A second completed lifecycle row has no qualified terminal proof. Its
	// slot claim must remain, while both reservation records remain protected.
	unqualified := dispatch
	unqualified.ID = "dispatch-unqualified"
	unqualified.Converted = false
	unqualified.Reservation.ID = unqualified.ID
	unqualified.Reservation.TaskID = "task-2"
	unqualified.Reservation.Fence = 10
	unqualified.HostSlot = func() *HostSlotClaim {
		claim := *dispatch.HostSlot
		claim.DispatchID = unqualified.ID
		claim.DispatchFence = 10
		return &claim
	}()
	unqualifiedIntent := intent
	unqualifiedIntent.ID = "intent-unqualified"
	unqualifiedIntent.DispatchID = unqualified.ID
	unqualifiedIntent.Reservation = unqualified
	unqualifiedObservation := LifecycleObservation{Binding: lifecycleBinding(unqualifiedIntent), State: "completed", Source: qualified.Source, ReceivedAt: time.Now()}
	s := &state{Binding: Binding{Workspace: "workspace", Installation: "installation"}, Sessions: map[string]record{"host-session": {Session: Session{ID: "host-session", ExpiresAt: time.Now().Add(2 * time.Hour)}}}, Dispatches: map[string]DispatchReservation{dispatchID: dispatch, unqualified.ID: unqualified}, DispatchIntents: map[string]DispatchIntent{dispatchID: intent, unqualified.ID: unqualifiedIntent}, SwarmPolicies: map[string]SwarmPolicy{project + "\x00" + policy.Revision: policy}, LifecycleObservations: map[string][]LifecycleObservation{dispatchID: {observed}, unqualified.ID: {unqualifiedObservation}}, Recoveries: map[string]RecoveryReceipt{}, Leases: map[string]Lease{"lease-1": {ID: "lease-1", TaskID: "task-1", SessionID: "worker", Fence: 11}}, Tasks: map[string]TaskBinding{"task-1": {ProjectID: project}, "task-2": {ProjectID: project}}}
	if !hostSlotTerminalProof(s, dispatch) {
		t.Fatalf("fixture lacks exact qualified terminal proof: valid=%v policyshape=%v claimmatch=%v scopesame=%v digest=%q/%q times=%v", validHostSlotTerminalObservation(s, observed), hostSlotPolicyShapeValid(s, slots), hostSlotClaimMatchesPolicy(dispatch.HostSlot, slots), hostSlotScopeEqual(slots, slots), proof.QualificationDigest, hostSlotTerminalQualificationDigest(qualified), !observed.ReceivedAt.Before(qualified.QualifiedAt) && observed.ReceivedAt.Before(qualified.ExpiresAt))
	}
	if got := queueOutstandingExecutionClaims(s, domain); got != 1 {
		t.Fatalf("qualified terminal proof or unqualified terminal state released the wrong execution claims: %d", got)
	}
	if got := controllerOccupancy(s, project); got != 2 || !queueTaskRetained(s, project, "task-1") || !queueTaskRetained(s, project, "task-2") {
		t.Fatalf("terminal proof incorrectly released writer protection: occupancy=%d", got)
	}
}

func TestQueueDiagnosticsBoundsAndUnknowns(t *testing.T) {
	a, _, operator, token := queueDiagnosticsAuthority(t)
	reader, err := OpenQueueDiagnosticsAuthority(a.path, a.binding)
	if err != nil {
		t.Fatal(err)
	}
	tasks := make([]QueueCanonicalTask, 120)
	for i := range tasks {
		tasks[i] = QueueCanonicalTask{ProjectID: "project", Status: "ready"}
	}
	// Use unique simple IDs independent of the deterministic display order.
	for i := range tasks {
		tasks[i].TaskID = "task-" + string(rune('a'+i/26)) + string(rune('a'+i%26))
	}
	out, err := reader.ReadQueueDiagnostics(context.Background(), token, operator.ID, "project", "", 3, func(context.Context) (QueueCanonicalSnapshot, error) { return queueCanonical(tasks...), nil })
	if err != nil || out.TotalCount != 120 || out.Counts.Queued != 120 || len(out.Rows) != 3 || out.OmittedCount != 117 || !out.Limited {
		t.Fatalf("bounded projection counts=%+v rows=%d omitted=%d err=%v", out.Counts, len(out.Rows), out.OmittedCount, err)
	}
	encoded, err := json.Marshal(out)
	if err != nil || len(encoded) > queueDiagnosticsBytes {
		t.Fatalf("response size=%d err=%v", len(encoded), err)
	}
	if out.ProviderQuotaState != "unknown" || out.ProviderUsage != nil || out.ProviderCostUSD != nil || out.Rows[0].QueueEnteredAt != nil || out.Rows[0].QueueAgeSeconds != nil {
		t.Fatal("unknown usage/cost/queue age was reported as measured")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	_, err = reader.ReadQueueDiagnostics(canceled, token, operator.ID, "project", "", 50, func(context.Context) (QueueCanonicalSnapshot, error) { called = true; return queueCanonical(), nil })
	if !errors.Is(err, ErrDenied) || called {
		t.Fatalf("canceled read invoked callback: called=%v err=%v", called, err)
	}
}

func TestQueueDiagnosticsActualSchemaVersionBoundaries(t *testing.T) {
	t.Run("schema 6", func(t *testing.T) {
		a, owner := setup(t)
		queueDiagnosticsCheckSchema(t, a, owner, 6)
	})
	t.Run("schema 19", func(t *testing.T) {
		a, owner, _ := authorityGrowthFixture(t, 0)
		queueDiagnosticsCheckSchema(t, a, owner, 19)
	})
	t.Run("schema 21", func(t *testing.T) {
		a, owner, _, _, _, _ := capacity21Fixture(t)
		queueDiagnosticsCheckSchema(t, a, owner, 21)
	})
	t.Run("schema 22", func(t *testing.T) {
		f, _, _ := hostSlotSwarmFixture(t, 2)
		op, token, err := f.a.Enroll(f.owner, Scope{Actions: []string{"operator.read"}, Resources: []string{"workspace:" + f.a.binding.Workspace, "project:project"}}, time.Now().Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		reader, err := OpenQueueDiagnosticsAuthority(f.a.path, f.a.binding)
		if err != nil {
			t.Fatal(err)
		}
		out, err := reader.ReadQueueDiagnostics(context.Background(), token, op.ID, "project", "", 50, func(context.Context) (QueueCanonicalSnapshot, error) {
			return queueCanonical(QueueCanonicalTask{TaskID: "task-a", ProjectID: "project", Status: "ready"}), nil
		})
		if err != nil || out.AuthorityVersion != 22 || len(out.Hosts) != 1 || out.Hosts[0].RemainingAdmissibleSlots == nil {
			t.Fatalf("schema-22 exact slot summary missing: version=%d hosts=%+v err=%v", out.AuthorityVersion, out.Hosts, err)
		}
	})
}

func queueDiagnosticsCheckSchema(t *testing.T, a *Authority, owner string, want int) {
	t.Helper()
	project := "queue-schema-project"
	op, token, err := a.Enroll(owner, Scope{Actions: []string{"operator.read"}, Resources: []string{"workspace:" + a.binding.Workspace, "project:" + project}}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	reader, err := OpenQueueDiagnosticsAuthority(a.path, a.binding)
	if err != nil {
		t.Fatal(err)
	}
	out, err := reader.ReadQueueDiagnostics(context.Background(), token, op.ID, project, "", 50, func(context.Context) (QueueCanonicalSnapshot, error) {
		return queueCanonical(QueueCanonicalTask{TaskID: "task-schema", ProjectID: project, Status: "ready"}), nil
	})
	if err != nil || out.AuthorityVersion != want || out.Counts.Queued != 1 || len(out.Hosts) != 0 || out.Rows[0].Freshness != "unsupported" {
		t.Fatalf("schema %d support incorrect: %+v err=%v", want, out, err)
	}
}
