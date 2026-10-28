package sessions

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func TestReadControllerDriverViewIsScopedBoundedAndReadOnly(t *testing.T) {
	f := controllerSetup(t)
	status := controllerStart(t, f)
	before, err := os.ReadFile(f.a.path)
	if err != nil {
		t.Fatal(err)
	}
	view, err := f.a.ReadControllerDriverView(context.Background(), f.token, f.s.ID, status.ProjectID, status.ID)
	if err != nil {
		t.Fatal("read current controller view", err)
	}
	after, err := os.ReadFile(f.a.path)
	if err != nil || string(before) != string(after) {
		t.Fatalf("read projection changed authority bytes: %v", err)
	}
	if view.AuthorityVersion != 16 || view.Status.ID != status.ID || view.CheckpointSequence != 0 || view.Status.HostActionAuthorized || len(view.Routes) != len(f.policy.TaskIDs) || len(view.Operations) != 0 {
		t.Fatalf("unexpected scoped starting projection: %+v", view)
	}

	status = controllerStep(t, f, status, "driver-read-wave")
	view, err = f.a.ReadControllerDriverView(context.Background(), f.token, f.s.ID, status.ProjectID, status.ID)
	if err != nil {
		t.Fatal("read reserved wave", err)
	}
	if len(view.Status.Actions) != 2 || len(view.Operations) != 2 || view.HistoricalActionCount != 2 || view.HistoricalOperationCount != 1 {
		t.Fatalf("width-two wave did not expose bounded original operation keys: %+v", view)
	}
	for _, operation := range view.Operations {
		if operation.Kind != "dispatch" || operation.State != "absent" || operation.DispatchID == "" || operation.ReservationFence == 0 {
			t.Fatalf("unrequested dispatch was not represented as exact absence: %+v", operation)
		}
	}

	status = controllerStep(t, f, status, "driver-read-first-intent")
	view, err = f.a.ReadControllerDriverView(context.Background(), f.token, f.s.ID, status.ProjectID, status.ID)
	if err != nil {
		t.Fatal("read original first-use intent", err)
	}
	requested := 0
	for _, operation := range view.Operations {
		if operation.DispatchID == status.Actions[0].DispatchID && operation.State == "pending" && operation.IntentID != "" && operation.OperationKey == status.Actions[0].OperationKey {
			requested++
		}
	}
	if requested != 1 || view.Status.HostActionAuthorized {
		t.Fatalf("requested host intent was not reported without first-use authority: %+v", view)
	}

	if _, err := f.a.ReadControllerDriverView(context.Background(), f.runnerTokens[view.Routes["task-a"].RunnerSession], f.s.ID, status.ProjectID, status.ID); !errors.Is(err, ErrDenied) {
		t.Fatalf("foreign runner read controller view: %v", err)
	}
	if _, err := f.a.ReadControllerDriverView(context.Background(), f.token, f.s.ID, status.ProjectID, "another-invocation"); !errors.Is(err, ErrDenied) {
		t.Fatalf("foreign invocation was not refused: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.a.ReadControllerDriverView(ctx, f.token, f.s.ID, status.ProjectID, status.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled read context was ignored: %v", err)
	}
}

func TestReadControllerDriverViewReopensCorrelatedUnacknowledgedDispatch(t *testing.T) {
	f := controllerSetup(t)
	status := controllerStart(t, f)
	status = controllerStep(t, f, status, "correlated-wave")
	status = controllerStep(t, f, status, "correlated-first-intent")
	if !status.HostActionAuthorized || len(status.Actions) == 0 {
		t.Fatalf("first-use host intent was not created: %+v", status)
	}
	action := status.Actions[0]
	var intent DispatchIntent
	if err := f.a.transaction(false, false, func(s *state) error {
		intent = s.DispatchIntents[action.DispatchID]
		return nil
	}); err != nil || intent.ID == "" {
		t.Fatalf("read retained original intent: %v %+v", err, intent)
	}
	bridgeSession := f.start.Routes[action.TaskID].BridgeSession
	bridgeToken := f.bridgeTokens[bridgeSession]
	correlated, err := f.a.CorrelateDispatchIntent(bridgeToken, bridgeSession, correlation(intent, "correlated-before-ack", "host_correlated"))
	if err != nil || correlated.State != "host_correlated" {
		t.Fatalf("correlate without worker acknowledgement: %v %+v", err, correlated)
	}
	for attempt := 0; attempt < 2; attempt++ { // model a process reopen before worker acknowledgement
		view, err := f.a.ReadControllerDriverView(context.Background(), f.token, f.s.ID, status.ProjectID, status.ID)
		if err != nil {
			t.Fatalf("reopen correlated-but-unacknowledged dispatch: %v", err)
		}
		found := 0
		for _, operation := range view.Operations {
			if operation.DispatchID == action.DispatchID && operation.OperationKey == action.OperationKey && operation.State == "pending" {
				found++
			}
		}
		if found != 1 {
			t.Fatalf("correlated original dispatch did not remain one pending resumable operation: %+v", view.Operations)
		}
	}
}

func TestReadControllerDriverViewRejectsLegacySchemaWithoutMutation(t *testing.T) {
	f := swarmSetup(t)
	session, token, err := f.a.Enroll(f.owner, Scope{
		Actions:   []string{"controller.read"},
		Resources: []string{"workspace:" + f.a.binding.Workspace, "project:project"},
	}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(f.a.path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.a.ReadControllerDriverView(context.Background(), token, session.ID, "project", "controller-one")
	if !errors.Is(err, ErrControllerDriverUnsupported) {
		t.Fatalf("legacy schema was not reported as explicitly unsupported: %v", err)
	}
	after, err := os.ReadFile(f.a.path)
	if err != nil || string(before) != string(after) {
		t.Fatalf("unsupported legacy read changed authority bytes: %v", err)
	}
}

func TestAuthorizeControllerDriverEffectRejectsCurrentPolicyDriftWithoutWriting(t *testing.T) {
	f := controllerSetup(t)
	status := controllerStart(t, f)
	if err := f.a.AuthorizeControllerDriverEffect(context.Background(), f.token, f.s.ID, status.ProjectID, status.ID, status.Sequence); err != nil {
		t.Fatalf("current invocation effect authorization refused: %v", err)
	}
	if err := f.a.transaction(false, true, func(s *state) error {
		policy := s.ControllerPolicies[status.ProjectID]
		policy.MaxWaves++
		s.ControllerPolicies[status.ProjectID] = policy
		return nil
	}); err != nil {
		t.Fatal("prepare current policy drift", err)
	}
	before, err := os.ReadFile(f.a.path)
	if err != nil {
		t.Fatal(err)
	}
	err = f.a.AuthorizeControllerDriverEffect(context.Background(), f.token, f.s.ID, status.ProjectID, status.ID, status.Sequence)
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("stale invocation policy authorized a new effect: %v", err)
	}
	after, err := os.ReadFile(f.a.path)
	if err != nil || string(before) != string(after) {
		t.Fatalf("effect authorization wrote authority state: %v", err)
	}
}

func TestControllerDriverCheckpointAllowsOnlyStageAndKeyMatchedExpectedUnknowns(t *testing.T) {
	authority := &Authority{}
	state := &state{}
	invocation := controllerInvocation{Status: ControllerStatus{Phase: "await_host"}}
	workerAction := ControllerAction{ID: "host-action", Kind: "host_dispatch", TaskID: "task", DispatchID: "dispatch", IntentID: "intent", OperationKey: "host-key"}
	unknownAck := CheckpointStatus{Findings: []CheckpointFinding{{Class: "unknown_dispatch_ack", Knowledge: "unknown", TaskID: "task", Reference: "intent"}}}
	if !authority.controllerDriverCheckpointSafeForBoundary(state, unknownAck, invocation, &workerAction, ControllerDriverBoundaryDispatch, "host-key") {
		t.Fatal("the exact host action's own first-use acknowledgement blocker was not allowed")
	}
	if authority.controllerDriverCheckpointSafeForBoundary(state, unknownAck, invocation, &workerAction, ControllerDriverBoundaryDispatch, "other-host-key") {
		t.Fatal("a different original host key was allowed through the checkpoint guard")
	}
	if authority.controllerDriverCheckpointSafeForBoundary(state, unknownAck, invocation, nil, ControllerDriverBoundaryStep, "") {
		t.Fatal("a controller Step bypassed an unrelated unknown host action")
	}
	verifyRequest := VerificationRequest{OperationKey: "verify-key", WorkerSession: "worker", AttemptID: "attempt", LeaseID: "lease", RevisionID: "revision", CheckID: "check", GrantID: "grant", ExpectedFence: 9}
	verifyAction := ControllerAction{ID: "verify-action", Kind: "verification", ActorID: "runner", TaskID: "task", AttemptID: "attempt", LeaseID: "lease", RevisionID: "revision", Fence: 9, OperationKey: "verify-key", Verifications: []VerificationRequest{verifyRequest}}
	invocation.Status.Actions = []ControllerAction{verifyAction}
	missingCheck := CheckpointStatus{Findings: []CheckpointFinding{{Class: "verification_unknown", Knowledge: "unknown", TaskID: "task", Reference: "verify-key"}}}
	if !authority.controllerDriverCheckpointSafeForBoundary(state, missingCheck, invocation, &verifyAction, ControllerDriverBoundaryVerify, "verify-key") {
		t.Fatal("the exact absent required-check key was not allowed to reach its runner")
	}
	if authority.controllerDriverCheckpointSafeForBoundary(state, missingCheck, invocation, &verifyAction, ControllerDriverBoundaryVerify, "other-key") {
		t.Fatal("a mismatched required-check key was allowed through the checkpoint guard")
	}
	verifyRequestA := verifyRequest
	verifyRequestA.OperationKey = "verify-key-a"
	verifyRequestB := verifyRequest
	verifyRequestB.OperationKey = "verify-key-b"
	multiCheckAction := ControllerAction{ID: "verify-action", Kind: "verification", ActorID: "runner", TaskID: "task", AttemptID: "attempt", LeaseID: "lease", RevisionID: "revision", Fence: 9, Verifications: []VerificationRequest{verifyRequestA, verifyRequestB}}
	invocation.Status.Actions = []ControllerAction{multiCheckAction}
	multiCheckUnknowns := CheckpointStatus{Findings: []CheckpointFinding{
		{Class: "verification_unknown", Knowledge: "unknown", TaskID: "task", Reference: "verify-key-a"},
		{Class: "verification_unknown", Knowledge: "unknown", TaskID: "task", Reference: "verify-key-b"},
	}}
	if !authority.controllerDriverCheckpointSafeForBoundary(state, multiCheckUnknowns, invocation, &multiCheckAction, ControllerDriverBoundaryVerify, "verify-key-a") {
		t.Fatal("an absent sibling required check blocked the exact selected verification")
	}
	failedCheck := CheckpointStatus{Findings: []CheckpointFinding{{Class: "verification_failed", Knowledge: "known", TaskID: "task", Reference: "verify-key"}}}
	invocation.Status.Actions = []ControllerAction{verifyAction}
	if authority.controllerDriverCheckpointSafeForBoundary(state, failedCheck, invocation, nil, ControllerDriverBoundaryStep, "") {
		t.Fatal("an evidence ID without a bound current failed runner result bypassed the Step gate")
	}
	refusal := CheckpointStatus{Findings: []CheckpointFinding{{Class: "policy_refusal", Knowledge: "known", TaskID: "task", Reference: "project"}}}
	if authority.controllerDriverCheckpointSafeForBoundary(state, refusal, invocation, &workerAction, ControllerDriverBoundaryDispatch, "host-key") {
		t.Fatal("an expected own-stage exception bypassed a real policy refusal")
	}
}

func TestControllerDriverRefOnlyMissingIntentIsUnknownExceptReservationOnly(t *testing.T) {
	status := ControllerStatus{ID: "invocation", ProjectID: "project", CheckpointID: "checkpoint"}
	ref := CheckpointRefs{TaskID: "task", DispatchID: "dispatch", IntentID: "intent"}
	rows := controllerDriverRefOperations(&state{}, "project", status, ref)
	if len(rows) != 1 || rows[0].Kind != "dispatch" || rows[0].State != "unknown" || rows[0].OperationKey == "" {
		t.Fatalf("ref-only missing original dispatch intent was omitted or treated safe: %+v", rows)
	}
	status.Actions = []ControllerAction{{ID: "reservation", Kind: "dispatch", TaskID: "task", DispatchID: "dispatch", ReservationFence: 7}}
	rows = controllerDriverRefOperations(&state{}, "project", status, ref)
	if len(rows) != 1 || rows[0].State != "absent" || rows[0].ActionID != "reservation" || rows[0].ReservationFence != 7 {
		t.Fatalf("reservation-only initial dispatch was not distinguished from lost host intent: %+v", rows)
	}
}

func TestControllerDriverCancellationAllowsOnlyExactPendingVerificationTarget(t *testing.T) {
	request := VerificationRequest{OperationKey: "verify-cancel", WorkerSession: "worker", AttemptID: "attempt", LeaseID: "lease", RevisionID: "revision", CheckID: "check", GrantID: "grant", ExpectedFence: 9}
	action := ControllerAction{ID: "verify-action", Kind: "verification", ActorID: "runner", TaskID: "task", DispatchID: "dispatch", IntentID: "intent", AttemptID: "attempt", LeaseID: "lease", RevisionID: "revision", Fence: 9, OperationKey: request.OperationKey, Verification: request}
	grant := VerificationGrant{ID: request.GrantID, CommandDigest: "command-digest", RunnerSession: action.ActorID, TaskID: action.TaskID, CheckID: request.CheckID}
	opRecord := operation{Pending: true, Digest: identity("run:"+grant.CommandDigest, action.ActorID, request)}
	s := &state{Grants: map[string]VerificationGrant{request.GrantID: grant}, Operations: map[string]operation{request.OperationKey: opRecord}}
	iv := controllerInvocation{Routes: map[string]ControllerTaskRoute{action.TaskID: {RunnerSession: action.ActorID, WorkerSession: request.WorkerSession, VerificationGrantID: request.GrantID}}}
	expected := CheckpointStatus{Findings: []CheckpointFinding{{Class: "verification_unknown", Knowledge: "unknown", TaskID: action.TaskID, Reference: request.OperationKey}}}
	if !controllerDriverCheckpointSafeForCancellation(s, iv, action, expected, action.DispatchID, action.IntentID) {
		t.Fatal("exact pending target verification blocked its separately authorized cancellation")
	}
	foreign := expected
	foreign.Findings = []CheckpointFinding{{Class: "verification_unknown", Knowledge: "unknown", TaskID: action.TaskID, Reference: "other-verification"}}
	if controllerDriverCheckpointSafeForCancellation(s, iv, action, foreign, action.DispatchID, action.IntentID) {
		t.Fatal("unrelated verification unknown bypassed cancellation checkpoint guard")
	}
	refusal := CheckpointStatus{Findings: []CheckpointFinding{{Class: "policy_refusal", Knowledge: "known", TaskID: action.TaskID, Reference: action.DispatchID}}}
	if controllerDriverCheckpointSafeForCancellation(s, iv, action, refusal, action.DispatchID, action.IntentID) {
		t.Fatal("policy refusal for target bypassed cancellation checkpoint guard")
	}
}

func TestControllerDriverEvidenceIDsEqualIsOrderIndependentAndExact(t *testing.T) {
	if !controllerDriverEvidenceIDsEqual([]string{"evidence-a", "evidence-b"}, []string{"evidence-b", "evidence-a"}) {
		t.Fatal("the same evidence set in a different producer order was rejected")
	}
	for name, pair := range map[string]struct{ left, right []string }{
		"nil set":          {nil, []string{"evidence-a"}},
		"empty set":        {[]string{}, []string{}},
		"missing member":   {[]string{"evidence-a", "evidence-b"}, []string{"evidence-a"}},
		"extra member":     {[]string{"evidence-a"}, []string{"evidence-a", "evidence-b"}},
		"duplicate left":   {[]string{"evidence-a", "evidence-a"}, []string{"evidence-a", "evidence-a"}},
		"duplicate right":  {[]string{"evidence-a", "evidence-b"}, []string{"evidence-a", "evidence-a"}},
		"invalid left ID":  {[]string{"evidence/a"}, []string{"evidence/a"}},
		"invalid right ID": {[]string{"evidence-a"}, []string{"evidence\nb"}},
	} {
		t.Run(name, func(t *testing.T) {
			if controllerDriverEvidenceIDsEqual(pair.left, pair.right) {
				t.Fatal("non-exact or invalid evidence set was accepted")
			}
		})
	}
}

func TestControllerDriverOwnerRowsBindRefCandidatesAndPendingState(t *testing.T) {
	candidate := NativeAcceptanceInput{SchemaVersion: 1, WorkspaceID: "workspace", InstallationID: "installation", ProjectID: "project", TaskID: "task", ReviewID: "review", AttemptID: "attempt", LeaseID: "lease", RevisionID: "revision", Repository: "repo", Commit: "commit", Tree: "tree", ExpectedFence: 17}
	acceptanceKey := "acceptance-key"
	acceptanceInput := candidate
	acceptanceInput.OperationKey = acceptanceKey
	acceptance := NativeAcceptanceRecord{OperationKey: acceptanceKey, InputDigest: identity("native-acceptance-operation", "", acceptanceInput), State: "pending", Candidate: NativeAcceptanceCandidate{Input: candidate}}
	integrationKey := "integration-key"
	integration := NativeIntegrationRecord{OperationKey: integrationKey, InputDigest: "", State: "pending", Input: NativeIntegrationInput{OperationKey: integrationKey, PolicyRevision: "integration-policy", Candidate: acceptanceInput}, Policy: NativeIntegrationPolicy{Revision: "integration-policy"}}
	integration.InputDigest = identity("native-integration-input", "", integration.Input)
	s := &state{Binding: Binding{Workspace: "workspace", Installation: "installation"}, NativeAcceptanceOperations: map[string]NativeAcceptanceRecord{acceptanceKey: acceptance}, NativeIntegrationOperations: map[string]NativeIntegrationRecord{integrationKey: integration}}
	if _, ok := s.NativeAcceptanceOperations[acceptanceKey]; !ok {
		t.Fatal("acceptance fixture key missing")
	}
	base := ControllerDriverOperation{TaskID: "task"}
	acceptanceRows := controllerDriverAcceptanceOperation(s, "project", base, acceptanceKey)
	if len(acceptanceRows) != 1 || acceptanceRows[0].State != "pending" || acceptanceRows[0].AttemptID != candidate.AttemptID || acceptanceRows[0].LeaseID != candidate.LeaseID || acceptanceRows[0].RevisionID != candidate.RevisionID || acceptanceRows[0].Fence != candidate.ExpectedFence {
		t.Fatalf("pending operation-independent acceptance candidate was not bound: %+v", acceptanceRows)
	}
	integrationRows := controllerDriverIntegrationOperation(s, "project", base, integrationKey)
	if len(integrationRows) != 1 || integrationRows[0].State != "pending" || integrationRows[0].AttemptID != candidate.AttemptID || integrationRows[0].LeaseID != candidate.LeaseID || integrationRows[0].RevisionID != candidate.RevisionID || integrationRows[0].Fence != candidate.ExpectedFence {
		t.Fatalf("pending integration candidate was not bound: %+v", integrationRows)
	}
}

func TestControllerDriverUnstartedAcceptanceGateJoinsExactIntegrationLineage(t *testing.T) {
	finding := CheckpointFinding{Class: "pending_canonical_acceptance", Knowledge: "unknown", TaskID: "task", Reference: "acceptance-key"}
	integration := CheckpointRefs{TaskID: "task", DispatchID: "dispatch", IntentID: "intent", EvidenceID: "evidence", ReviewID: "review", IntegrationOperationID: "integration-key"}
	acceptance := CheckpointRefs{TaskID: "task", DispatchID: "dispatch", ReviewID: "review", IntegrationOperationID: "integration-key", AcceptanceOperationID: "acceptance-key"}
	iv := controllerInvocation{Status: ControllerStatus{Refs: []CheckpointRefs{integration, acceptance}}}
	joined, ok := controllerDriverUnstartedOwnerGateRef(iv, finding)
	if !ok || joined.IntentID != integration.IntentID || joined.EvidenceID != integration.EvidenceID || joined.DispatchID != acceptance.DispatchID || joined.ReviewID != acceptance.ReviewID || joined.IntegrationOperationID != acceptance.IntegrationOperationID {
		t.Fatalf("acceptance ref did not join its exact retained integration lineage: ok=%v joined=%+v", ok, joined)
	}

	for name, refs := range map[string][]CheckpointRefs{
		"mismatched populated identity": {integration, func() CheckpointRefs { ref := acceptance; ref.IntentID = "other-intent"; return ref }()},
		"different dispatch":            {integration, func() CheckpointRefs { ref := acceptance; ref.DispatchID = "other-dispatch"; return ref }()},
		"ambiguous lineage":             {integration, integration, acceptance},
	} {
		t.Run(name, func(t *testing.T) {
			iv.Status.Refs = refs
			if joined, ok := controllerDriverUnstartedOwnerGateRef(iv, finding); ok {
				t.Fatalf("unsafe acceptance reference lineage was accepted: %+v", joined)
			}
		})
	}
}
