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

type controllerFixture struct {
	swarmFixture
	observer       *checkpointFixtureAdmission
	policy         ControllerPolicy
	start          ControllerStartRequest
	bridgeTokens   map[string]string
	runnerTokens   map[string]string
	reviewerTokens map[string]string
}

func controllerSetup(t *testing.T) controllerFixture {
	t.Helper()
	f, ad, _ := budgetSetup(t, 10000)
	for _, up := range []func(string) error{f.a.UpgradeNativeAcceptance, f.a.UpgradeNativePolicy, f.a.UpgradeController} {
		if e := up(f.owner); e != nil {
			t.Fatal(e)
		}
	}
	actions := append(append([]string{}, f.s.Scope.Actions...), "read", "controller.read", "controller.step", "controller.reconcile")
	resources := append(append([]string{}, f.s.Scope.Resources...), "workspace:"+f.a.binding.Workspace)
	var e error
	f.s, f.token, e = f.a.Enroll(f.owner, Scope{Actions: actions, Resources: resources}, time.Now().Add(time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	p := ControllerPolicy{SchemaVersion: 1, ProjectID: "project", Revision: "controller-policy-one", Harness: "codex", CapabilityDigest: f.p.CapabilityDigest, TaskIDs: []string{"task-a", "task-b", "task-c"}, MaxWaves: 4, MaxConcurrency: 3, MaxCorrectionRounds: 2, MaxNoProgress: 2, TransientRetries: 1, MaxElapsed: 10 * time.Minute, RetryBackoff: time.Millisecond, ExpiresAt: time.Now().Add(20 * time.Minute)}
	if e = f.a.ConfigureController(f.owner, p); e != nil {
		t.Fatal(e)
	}
	start := ControllerStartRequest{OperationKey: "start", InvocationID: "controller-one", ProjectID: p.ProjectID, PolicyRevision: p.Revision, ControllerSessionID: f.s.ID, CheckpointID: "checkpoint-one", Routes: map[string]ControllerTaskRoute{}}
	bridgeTokens := map[string]string{}
	runnerTokens := map[string]string{}
	reviewerTokens := map[string]string{}
	for _, task := range p.TaskIDs {
		o := ad.observations[task]
		reviewer, reviewToken, e := f.a.Enroll(f.owner, Scope{Actions: []string{"read", "review.propose", "review.evaluate"}, Resources: []string{"task:" + task}}, time.Now().Add(time.Hour))
		if e != nil {
			t.Fatal(e)
		}
		reviewerTokens[reviewer.ID] = reviewToken
		o.Reviewer = PreflightActor{SessionID: reviewer.ID, CredentialHash: hash(reviewToken)}
		runner, rt, e := f.a.Enroll(f.owner, Scope{Actions: []string{"verification.run"}, Resources: []string{"task:" + task}}, time.Now().Add(time.Hour))
		if e != nil {
			t.Fatal(e)
		}
		grant, e := f.a.GrantVerification(f.owner, runner.ID, task, o.RunnerRepository, CheckDescriptor{ID: "unit", Command: "exit 1", Deadline: time.Second}, time.Now().Add(25*time.Minute))
		if e != nil {
			t.Fatal("owner verification grant", e)
		}
		runnerTokens[runner.ID] = rt
		o.Runner = PreflightActor{SessionID: runner.ID, CredentialHash: hash(rt)}
		o.VerificationGrantIDs = []string{grant.ID}
		o.Checks = []RequiredCheck{{ID: grant.CheckID, CommandDigest: grant.CommandDigest}}
		o.ReviewObservation.Checks = o.Checks
		o.ReviewObservation.ChecklistDigest = OutputDigest([]byte("owner fixture checklist"))
		f.a.reviewObserver = checkpointReviewObservation{o.ReviewObservation}
		reviewGrant, e := f.a.GrantReview(f.owner, ReviewGrantRequest{ReviewerSession: o.Reviewer.SessionID, WorkerSession: o.Worker.SessionID, TaskID: task, Repository: o.RunnerRepository, ExpiresAt: time.Now().Add(25 * time.Minute)})
		if e != nil {
			t.Fatal("owner review grant", e)
		}
		o.Reviewer.GrantID = reviewGrant.ID
		ad.observations[task] = o

		bridge, bt, e := f.a.Enroll(f.owner, Scope{Actions: []string{"read", "dispatch.correlate", "dispatch.observe"}, Resources: []string{"project:project", "task:" + task, "path:src/a", "path:other"}}, time.Now().Add(time.Hour))
		if e != nil {
			t.Fatal(e)
		}
		bridgeTokens[bridge.ID] = bt
		start.Routes[task] = ControllerTaskRoute{BridgeSession: bridge.ID, WorkerSession: o.Worker.SessionID, RunnerSession: o.Runner.SessionID, ReviewerSession: o.Reviewer.SessionID, VerificationGrantID: grant.ID, ReviewGrantID: reviewGrant.ID, CheckID: "unit", PromptSpecDigest: "sha256:" + strings.Repeat("a", 64)}
	}
	f.a.reviewObserver = nil
	return controllerFixture{f, ad, p, start, bridgeTokens, runnerTokens, reviewerTokens}
}
func controllerStart(t *testing.T, f controllerFixture) ControllerStatus {
	t.Helper()
	s, e := f.a.StartController(f.owner, f.start)
	if e != nil {
		t.Fatal("actual owner controller start", e)
	}
	return s
}
func controllerStep(t *testing.T, f controllerFixture, st ControllerStatus, key string) ControllerStatus {
	t.Helper()
	next, e := f.a.StepController(f.token, f.s.ID, ControllerStepRequest{OperationKey: key, ProjectID: st.ProjectID, InvocationID: st.ID, ExpectedSequence: st.Sequence})
	if e != nil {
		t.Fatal("actual bounded step", e, next.HaltClass)
	}
	return next
}
func TestNativeControllerAtomicWaveAndWidthTwoFirstIntentReplay(t *testing.T) {
	f := controllerSetup(t)
	st := controllerStart(t, f)
	st = controllerStep(t, f, st, "wave")
	if st.Waves != 1 || len(st.Actions) != 2 || st.Phase != "dispatch" {
		t.Fatalf("nonzero atomic wave counter/reservation parity: %+v", st)
	}
	before, _ := os.ReadFile(f.a.path)
	request := ControllerStepRequest{OperationKey: "wave", ProjectID: st.ProjectID, InvocationID: st.ID, ExpectedSequence: 1}
	replay, e := f.a.StepController(f.token, f.s.ID, request)
	after, _ := os.ReadFile(f.a.path)
	if e != nil || replay.Waves != 1 || replay.HostActionAuthorized || !reflect.DeepEqual(before, after) {
		t.Fatal("wave replay changed count or authority", e)
	}
	st = controllerStep(t, f, st, "dispatch-first")
	if !st.HostActionAuthorized {
		t.Fatal("first original intent not authorized")
	}
	st = controllerStep(t, f, st, "dispatch-second")
	if !st.HostActionAuthorized {
		t.Fatal("width2 second intent starved behind unknown first host")
	}
	count := 0
	_ = f.a.transaction(false, false, func(s *state) error { count = len(s.DispatchIntents); return nil })
	if count != 2 {
		t.Fatal("width2 did not persist exactly two original intents", count)
	}
	replay, e = f.a.StepController(f.token, f.s.ID, ControllerStepRequest{OperationKey: "dispatch-second", ProjectID: st.ProjectID, InvocationID: st.ID, ExpectedSequence: st.Sequence - 1})
	if e != nil || replay.HostActionAuthorized {
		t.Fatal("intent replay duplicated host permit", e)
	}
	st = controllerStep(t, f, st, "unknown-step")
	if st.HostActionAuthorized || st.HaltClass != "dispatch_unknown" {
		t.Fatal("unknown dispatch was treated as success or relaunch", st)
	}
}
func TestNativeControllerStartExactActorBindingsAndNoMutation(t *testing.T) {
	for _, kind := range []string{"wrong-worker-task", "wrong-grant-repository", "wrong-review-root", "delegated-controller"} {
		t.Run(kind, func(t *testing.T) {
			f := controllerSetup(t)
			switch kind {
			case "wrong-worker-task":
				r := f.start.Routes["task-a"]
				r.WorkerSession = f.start.Routes["task-b"].WorkerSession
				f.start.Routes["task-a"] = r
			case "wrong-grant-repository":
				_ = f.a.transaction(false, true, func(s *state) error {
					g := s.Grants[f.start.Routes["task-a"].VerificationGrantID]
					g.Repository = "different"
					s.Grants[g.ID] = g
					return nil
				})
			case "wrong-review-root":
				_ = f.a.transaction(false, true, func(s *state) error {
					g := s.ReviewGrants[f.start.Routes["task-a"].ReviewGrantID]
					g.WorkerRoot = f.start.Routes["task-b"].WorkerSession
					s.ReviewGrants[g.ID] = g
					return nil
				})
			case "delegated-controller":
				child, _, e := f.a.Delegate(f.token, f.s.ID, f.s.Scope, time.Now().Add(time.Minute))
				if e != nil {
					t.Fatal(e)
				}
				f.start.ControllerSessionID = child.ID
			}
			before, _ := os.ReadFile(f.a.path)
			out, e := f.a.StartController(f.owner, f.start)
			after, _ := os.ReadFile(f.a.path)
			if !errors.Is(e, ErrDenied) || out.ID != "" || !reflect.DeepEqual(before, after) {
				t.Fatal("invalid route persisted controller invocation", kind, e, out)
			}
		})
	}
}
func TestNativeControllerSchema15PreservationAndOldReaderRefusal(t *testing.T) {
	f, _, _ := budgetSetup(t, 10000)
	for _, up := range []func(string) error{f.a.UpgradeNativeAcceptance, f.a.UpgradeNativePolicy} {
		if e := up(f.owner); e != nil {
			t.Fatal(e)
		}
	}
	raw, _ := os.ReadFile(f.a.path)
	var before map[string]json.RawMessage
	_ = json.Unmarshal(raw, &before)
	if e := f.a.UpgradeController(f.owner); e != nil {
		t.Fatal(e)
	}
	raw, _ = os.ReadFile(f.a.path)
	var after map[string]json.RawMessage
	_ = json.Unmarshal(raw, &after)
	added := map[string]bool{"ControllerPolicies": true, "ControllerPolicyHistory": true, "ControllerInvocations": true, "ControllerHistory": true, "ControllerOperations": true}
	for k, v := range before {
		if k == "Version" {
			continue
		}
		if added[k] {
			if string(v) != "null" || string(after[k]) != "{}" {
				t.Fatal("new controller map shape", k)
			}
			continue
		}
		if !reflect.DeepEqual(v, after[k]) {
			t.Fatal("schema16 changed old authority history", k)
		}
	}
	old := *f.a
	old.version = 15
	if _, e := old.Authenticate(f.token, f.s.ID); !errors.Is(e, ErrStorage) {
		t.Fatal("genuine contract schema15 reader accepted16", e)
	}
}

func TestNativeControllerEnrollmentSharedResourcesAreExactSet(t *testing.T) {
	f := controllerSetup(t)
	f.policy.Revision = "sequential-overlap"
	f.policy.MaxConcurrency = 1
	if e := f.a.ConfigureController(f.owner, f.policy); e != nil {
		t.Fatal(e)
	}
	scope, e := f.a.ControllerEnrollmentScope(f.owner, f.policy.ProjectID)
	if e != nil {
		t.Fatal("owner controller scope", e)
	}
	counts := map[string]int{}
	for _, resource := range scope.Resources {
		counts[resource]++
	}
	if counts["path:src/a"] != 1 || counts["task:task-a"] != 1 || counts["task:task-b"] != 1 {
		t.Fatal("shared resource enrollment must retain exact tasks and one path", counts)
	}
	actor, _, e := f.a.Enroll(f.owner, scope, time.Now().Add(time.Hour))
	if e != nil {
		t.Fatal("actual shared-resource controller enrollment refused", e)
	}
	if actor.Parent != "" || !reflect.DeepEqual(actor.Scope, scope) {
		t.Fatal("owner enrollment changed exact scope", actor.Scope)
	}
}

func TestNativeControllerFactoryFamilyCannotMintPublicEffects(t *testing.T) {
	for _, scenario := range []string{"before-start", "after-deadline", "exhausted-wave", "delegated-without-role"} {
		t.Run(scenario, func(t *testing.T) {
			f := controllerSetup(t)
			scope, e := f.a.ControllerEnrollmentScope(f.owner, f.policy.ProjectID)
			if e != nil {
				t.Fatal(e)
			}
			for _, action := range scope.Actions {
				if action == "budget.settle" {
					t.Fatal("factory granted unused budget settlement")
				}
			}
			actor, token, e := f.a.Enroll(f.owner, scope, time.Now().Add(time.Hour))
			if e != nil {
				t.Fatal(e)
			}
			f.s, f.token, f.start.ControllerSessionID = actor, token, actor.ID
			if scenario == "after-deadline" {
				f.policy.Revision = "short-controller"
				f.policy.MaxElapsed = 100 * time.Millisecond
				if e = f.a.ConfigureController(f.owner, f.policy); e != nil {
					t.Fatal(e)
				}
				f.start.PolicyRevision = f.policy.Revision
				st := controllerStart(t, f)
				time.Sleep(time.Until(st.Deadline) + time.Millisecond)
			}
			if scenario == "exhausted-wave" {
				f.policy.Revision = "one-wave-controller"
				f.policy.MaxWaves = 1
				if e = f.a.ConfigureController(f.owner, f.policy); e != nil {
					t.Fatal(e)
				}
				f.start.PolicyRevision = f.policy.Revision
				st := controllerStart(t, f)
				st = controllerStep(t, f, st, "bounded-wave")
				if st.Waves != 1 {
					t.Fatal("atomic Step did not reserve bounded wave")
				}
			}
			if scenario == "delegated-without-role" {
				childScope := scope
				childScope.Actions = nil
				for _, action := range scope.Actions {
					if action != "controller.step" && action != "controller.reconcile" {
						childScope.Actions = append(childScope.Actions, action)
					}
				}
				actor, token, e = f.a.Delegate(token, actor.ID, childScope, time.Now().Add(20*time.Minute))
				if e != nil {
					t.Fatal(e)
				}
			}
			before, _ := os.ReadFile(f.a.path)
			request := CheckpointRequest{OperationKey: "outside", CheckpointID: "outside", ProjectID: f.policy.ProjectID, InvocationID: "outside", Phase: "pre-cycle"}
			out, e := f.a.ReconcileCheckpoint(token, actor.ID, request)
			after, _ := os.ReadFile(f.a.path)
			if !errors.Is(e, ErrDenied) || out.ID != "" || !reflect.DeepEqual(before, after) {
				t.Fatal("controller family minted generic checkpoint effect", scenario, out, e)
			}
			out, e = f.a.RecordCheckpoint(token, actor.ID, request)
			after, _ = os.ReadFile(f.a.path)
			if !errors.Is(e, ErrDenied) || out.ID != "" || !reflect.DeepEqual(before, after) {
				t.Fatal("controller family recorded generic checkpoint effect", scenario, out, e)
			}
		})
	}
}

func TestNativeControllerPublicReserveBoundaryAndGenericOperatorPositive(t *testing.T) {
	f := controllerSetup(t)
	scope, e := f.a.ControllerEnrollmentScope(f.owner, f.policy.ProjectID)
	if e != nil {
		t.Fatal(e)
	}
	actor, token, e := f.a.Enroll(f.owner, scope, time.Now().Add(time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	operatorScope := scope
	operatorScope.Actions = nil
	for _, action := range scope.Actions {
		if action != "controller.step" && action != "controller.reconcile" {
			operatorScope.Actions = append(operatorScope.Actions, action)
		}
	}
	operator, opToken, e := f.a.Enroll(f.owner, operatorScope, time.Now().Add(time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	// Produce an exact actor-bound gate through the same authenticated private
	// checkpoint helper used by Step; this isolates the public reservation gate.
	var gate CheckpointStatus
	e = f.a.transaction(false, true, func(s *state) error {
		var err error
		gate, err = f.a.writeCheckpointState(s, token, actor.ID, CheckpointRequest{OperationKey: "bound-gate", CheckpointID: "bound-gate", ProjectID: f.policy.ProjectID, InvocationID: "bound-cycle", Phase: "pre-cycle"}, true)
		return err
	})
	if e != nil || !gate.WaveAllowed {
		t.Fatal("private actor-bound gate refused", gate, e)
	}
	plan, e := f.a.PlanSwarm(token, actor.ID, f.policy.ProjectID)
	if e != nil {
		t.Fatal(e)
	}
	req := ReserveWaveRequest{OperationKey: "public-wave", Plan: plan, ExpiresAt: time.Now().Add(time.Minute), CheckpointID: gate.ID, CheckpointSequence: gate.Sequence, InvocationID: gate.InvocationID, ReconciliationDigest: gate.ConditionDigest}
	before, _ := os.ReadFile(f.a.path)
	out, e := f.a.ReserveWave(token, actor.ID, req)
	after, _ := os.ReadFile(f.a.path)
	if !errors.Is(e, ErrDenied) || out.ID != "" || !reflect.DeepEqual(before, after) {
		t.Fatal("bounded controller minted public reservation", out, e)
	}
	gate, e = f.a.ReconcileCheckpoint(opToken, operator.ID, CheckpointRequest{OperationKey: "generic-gate", CheckpointID: gate.ID, ProjectID: f.policy.ProjectID, InvocationID: "generic-cycle", Phase: "pre-cycle", ExpectedSequence: gate.Sequence})
	if e != nil || !gate.WaveAllowed {
		t.Fatal("ordinary operator fresh gate refused", gate, e)
	}
	req.CheckpointID, req.CheckpointSequence, req.InvocationID, req.ReconciliationDigest = gate.ID, gate.Sequence, gate.InvocationID, gate.ConditionDigest
	req.Plan, e = f.a.PlanSwarm(opToken, operator.ID, f.policy.ProjectID)
	if e != nil {
		t.Fatal(e)
	}
	out, e = f.a.ReserveWave(opToken, operator.ID, req)
	if e != nil || len(out.Dispatches) == 0 {
		t.Fatal("ordinary operator live reservation refused", out, e)
	}
}
func TestNativeControllerPublicIntentBoundaryKeepsAtomicStep(t *testing.T) {
	f := controllerSetup(t)
	scope, e := f.a.ControllerEnrollmentScope(f.owner, f.policy.ProjectID)
	if e != nil {
		t.Fatal(e)
	}
	actor, token, e := f.a.Enroll(f.owner, scope, time.Now().Add(time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	f.s, f.token, f.start.ControllerSessionID = actor, token, actor.ID
	st := controllerStart(t, f)
	st = controllerStep(t, f, st, "bounded-reserve")
	action := st.Actions[0]
	route := f.start.Routes[action.TaskID]
	req := RequestDispatchIntent{OperationKey: "outside-intent", DispatchID: action.DispatchID, ExpectedFence: action.ReservationFence, BridgeSessionID: route.BridgeSession, WorkerSessionID: route.WorkerSession, PromptSpecDigest: route.PromptSpecDigest}
	before, _ := os.ReadFile(f.a.path)
	out, e := f.a.RequestDispatchIntent(token, actor.ID, req)
	after, _ := os.ReadFile(f.a.path)
	if !errors.Is(e, ErrDenied) || out.HostActionAuthorized || out.Intent.ID != "" || !reflect.DeepEqual(before, after) {
		t.Fatal("bounded controller minted public host intent", out, e)
	}
	admitted, e := f.a.AdmitDispatch(token, actor.ID, AdmitDispatchRequest{DispatchID: action.DispatchID, ExpectedFence: action.ReservationFence})
	if !errors.Is(e, ErrDenied) || admitted.ID != "" {
		t.Fatal("bounded controller obtained public dispatch admission", admitted, e)
	}
	st = controllerStep(t, f, st, "bounded-intent")
	if !st.HostActionAuthorized {
		t.Fatal("atomic Step lost its original host permit")
	}
}

// Produce schema18 receipts through the real authenticated kernel APIs. The
// integration and canonical callbacks are controlled fixture observations; this
// fixture does not claim a real Git target fast-forward or external task store.
func controllerClosedRouteFixture(t *testing.T, recoverHeld bool) (controllerFixture, ControllerStatus) {
	t.Helper()
	f, previous := controllerCompletedWaveFrom(t, pluginControllerSetup(t))
	for _, upgrade := range []func(string) error{f.a.UpgradeNativeModelRouting, f.a.UpgradeNativeIntegration} {
		if err := upgrade(f.owner); err != nil {
			t.Fatal(err)
		}
	}
	for index, action := range previous.Actions {
		task, route := action.TaskID, f.start.Routes[action.TaskID]
		var at Attempt
		var grant VerificationGrant
		if err := f.a.transaction(false, false, func(s *state) error {
			var err error
			at, err = reviewAttempt(s, action.AttemptID)
			grant = s.Grants[route.VerificationGrantID]
			return err
		}); err != nil {
			t.Fatal(err)
		}
		policy := NativePolicy{SchemaVersion: 1, ProjectID: "project", TaskID: task, Revision: "closed-route-policy", Allow: true, ExpiresAt: time.Now().Add(15 * time.Minute)}
		if err := f.a.ConfigureNativePolicy(f.owner, policy); err != nil {
			t.Fatal(err)
		}
		integrationPolicy, err := f.a.ConfigureNativeIntegration(f.owner, NativeIntegrationPolicy{SchemaVersion: 1, ProjectID: "project", TaskID: task, Revision: "closed-route-target", Repository: at.Observation.Repository, TargetCheckout: at.Observation.Locator, TargetRef: "refs/heads/fixture-target", ExpectedCommit: at.Observation.Commit, ExpectedTree: at.Observation.Tree, NativePolicyRevision: policy.Revision, Allow: true, ExpiresAt: policy.ExpiresAt})
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(at.Observation.Locator, "closed-route-source"), []byte(task), 0600); err != nil {
			t.Fatal(err)
		}
		gitTest(t, at.Observation.Locator, "add", "closed-route-source")
		gitTest(t, at.Observation.Locator, "-c", "user.name=test", "-c", "user.email=test@example.invalid", "commit", "-m", "closed task source")
		revision, err := f.a.PublishAttemptRevision(f.observer.tokens[task], route.WorkerSession, PublishRevisionRequest{OperationKey: "closed-publish-" + task, AttemptID: at.ID, LeaseID: action.LeaseID, ExpectedFence: action.Fence, ExpectedRevision: 1})
		if err != nil {
			t.Fatal(err)
		}
		request := action.Verification
		request.RevisionID, request.OperationKey = revision.ID, "closed-check-"+task
		if _, _, err = f.a.BeginVerification(f.observer.tokens[task], f.runnerTokens[route.RunnerSession], route.RunnerSession, request, grant.CommandDigest); err != nil {
			t.Fatal(err)
		}
		evidence, err := f.a.CompleteVerification(f.observer.tokens[task], f.runnerTokens[route.RunnerSession], route.RunnerSession, request, VerificationResult{Commit: revision.Observation.Commit, Tree: revision.Observation.Tree, Directory: revision.Observation.Locator, CommandDigest: grant.CommandDigest, OutputDigest: OutputDigest([]byte("fixture passed")), Output: []byte("fixture passed"), ExitCode: 0, Clean: true})
		if err != nil {
			t.Fatal(err)
		}
		f.a.reviewObserver = checkpointReviewObservation{f.observer.observations[task].ReviewObservation}
		review, err := f.a.ProposeReview(f.reviewerTokens[route.ReviewerSession], route.ReviewerSession, ReviewRequest{OperationKey: "closed-review-" + task, GrantID: route.ReviewGrantID, AttemptID: at.ID, LeaseID: action.LeaseID, RevisionID: revision.ID, ExpectedFence: action.Fence, Outcome: "approve", Rationale: "exact fixture revision", EvidenceIDs: []string{evidence.ID}})
		if err != nil {
			t.Fatal(err)
		}
		candidate, err := f.a.ReadNativeAcceptanceCandidate(f.reviewerTokens[route.ReviewerSession], route.ReviewerSession, review.ID)
		if err != nil || candidate.Input.OperationKey != "" {
			t.Fatal("real acceptance candidate was not unkeyed", err)
		}
		input := candidate.Input
		input.OperationKey = "closed-accept-" + task
		integrationInput := NativeIntegrationInput{OperationKey: "closed-integrate-" + task, PolicyRevision: integrationPolicy.Revision, Candidate: input}
		integrated := false
		inspect := func(NativeIntegrationRecord) (NativeIntegrationObservation, error) {
			if integrated {
				return NativeIntegrationObservation{Commit: input.Commit, Tree: input.Tree, Clean: true, ObservedAt: time.Now().UTC()}, nil
			}
			return NativeIntegrationObservation{Commit: integrationPolicy.ExpectedCommit, Tree: integrationPolicy.ExpectedTree, Clean: true, ObservedAt: time.Now().UTC()}, nil
		}
		if _, err = f.a.ApplyNativeIntegration(f.owner, integrationInput, inspect, func(_ NativeIntegrationRecord, guard func() error) (NativeIntegrationObservation, error) {
			if err := guard(); err != nil {
				return NativeIntegrationObservation{}, err
			}
			integrated = true
			return inspect(NativeIntegrationRecord{})
		}); err != nil {
			t.Fatal(err)
		}
		decision, err := f.a.EvaluateNativePolicy(f.owner, NativePolicyEvaluationRequest{OperationKey: "closed-decision-" + task, Input: input, ExpiresAt: policy.ExpiresAt}, nativePolicyBody)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.a.BeginNativeAcceptanceWithPolicy(f.owner, input, decision.ID, nativePolicyBody); err != nil {
			t.Fatal(err)
		}
		closed := false
		if _, err = f.a.ContinueNativeAcceptance(f.owner, input, func(NativeAcceptanceRecord) (bool, error) { return closed, nil }, func(NativeAcceptanceRecord) error { closed = true; return nil }); err != nil {
			t.Fatal(err)
		}
		for i := range f.observer.snapshot.Candidates {
			if f.observer.snapshot.Candidates[i].TaskID == task {
				f.observer.snapshot.Candidates[i].Status = "done"
			}
		}
		action.Kind, action.ReviewID, action.RevisionID, action.OperationKey = "integration_checkpoint", review.ID, revision.ID, integrationInput.OperationKey
		previous.Actions[index] = action
		previous.Refs = append(previous.Refs, CheckpointRefs{TaskID: task, DispatchID: action.DispatchID, ReviewID: review.ID, IntegrationOperationID: integrationInput.OperationKey, AcceptanceOperationID: input.OperationKey})
		if recoverHeld {
			target, err := f.a.OwnerRecoveryTarget(f.owner, "lease", action.LeaseID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.a.Recover(f.owner, RecoveryRequest{OperationKey: "closed-recover-" + task, Target: target, Mode: "owner-reconciliation", Rationale: "explicit fixture protection recovery", Uncertainty: "accepted writer remains cooperative", ReleaseRisk: "owner accepts fixture release risk"}); err != nil {
				t.Fatal(err)
			}
		}
		if err = f.a.RevokeVerification(f.owner, route.VerificationGrantID); err != nil {
			t.Fatal(err)
		}
		if err = f.a.RevokeReview(f.owner, route.ReviewGrantID); err != nil {
			t.Fatal(err)
		}
		for _, actor := range []string{route.BridgeSession, route.RunnerSession, route.ReviewerSession} {
			if err = f.a.Revoke(f.owner, actor); err != nil {
				t.Fatal(err)
			}
		}
	}
	previous.Terminal, previous.Phase, previous.HaltClass = true, "halt", "policy_refused"
	if err := f.a.transaction(false, true, func(s *state) error {
		invocation := s.ControllerInvocations[previous.ID]
		invocation.Status = previous
		s.ControllerInvocations[previous.ID] = invocation
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return f, previous
}

func TestNativeControllerStartsWithExactClosedRoutesAndPreservesCheckpoint(t *testing.T) {
	f, previous := controllerClosedRouteFixture(t, true)
	before, err := f.a.ObserveStorage(f.owner)
	if err != nil {
		t.Fatal(err)
	}
	next := f.start
	next.InvocationID, next.OperationKey = "closed-continuation", "closed-start"
	out, err := f.a.StartScopedController(f.token, f.s.ID, next)
	if err != nil || out.CheckpointID != previous.CheckpointID || out.HostActionAuthorized {
		t.Fatal("exact historical DONE routes blocked startup", out, err)
	}
	if err = f.a.transaction(false, false, func(s *state) error {
		var original state
		if err := json.Unmarshal(before.PrivateBytes, &original); err != nil {
			return err
		}
		if !reflect.DeepEqual(s.Checkpoints, original.Checkpoints) || !reflect.DeepEqual(s.ControllerInvocations[previous.ID], original.ControllerInvocations[previous.ID]) || !reflect.DeepEqual(s.ControllerPolicies["project"].TaskIDs, []string{"task-a", "task-b", "task-c"}) || !reflect.DeepEqual(s.NativeAcceptanceOperations, original.NativeAcceptanceOperations) || !reflect.DeepEqual(s.Leases, original.Leases) || !reflect.DeepEqual(s.Grants, original.Grants) || !reflect.DeepEqual(s.ReviewGrants, original.ReviewGrants) {
			t.Fatal("closed route startup rewrote protection, scope, receipts, grants, or checkpoint counters")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestNativeControllerClosedRoutesRefuseWrongHistoryAndKeepOpenChecks(t *testing.T) {
	for _, change := range []string{"canonical-open", "worker", "runner", "reviewer", "bridge", "verification-grant", "review-grant", "missing-terminal", "acceptance-digest", "integration-receipt", "policy-outcome", "open-runner-revoked", "held-protection"} {
		t.Run(change, func(t *testing.T) {
			f, _ := controllerClosedRouteFixture(t, change != "held-protection")
			next := f.start
			next.InvocationID, next.OperationKey = "refused-continuation", "refused-start"
			next.Routes = map[string]ControllerTaskRoute{}
			for task, route := range f.start.Routes {
				next.Routes[task] = route
			}
			route := next.Routes["task-a"]
			switch change {
			case "worker":
				route.WorkerSession = next.Routes["task-c"].WorkerSession
			case "runner":
				route.RunnerSession = next.Routes["task-c"].RunnerSession
			case "reviewer":
				route.ReviewerSession = next.Routes["task-c"].ReviewerSession
			case "bridge":
				route.BridgeSession = next.Routes["task-c"].BridgeSession
			case "verification-grant":
				route.VerificationGrantID = next.Routes["task-c"].VerificationGrantID
			case "review-grant":
				route.ReviewGrantID = next.Routes["task-c"].ReviewGrantID
			case "canonical-open":
				for index := range f.observer.snapshot.Candidates {
					if f.observer.snapshot.Candidates[index].TaskID == "task-a" {
						f.observer.snapshot.Candidates[index].Status = "open"
					}
				}
			case "open-runner-revoked":
				if err := f.a.Revoke(f.owner, next.Routes["task-c"].RunnerSession); err != nil {
					t.Fatal(err)
				}
			}
			next.Routes["task-a"] = route
			if err := f.a.transaction(false, true, func(s *state) error {
				switch change {
				case "missing-terminal":
					delete(s.TaskTerminals, "task-a")
				case "acceptance-digest":
					record := s.TaskTerminals["task-a"]
					record.InputDigest = "other"
					s.TaskTerminals["task-a"] = record
				case "integration-receipt":
					record := s.NativeIntegrationOperations["closed-integrate-task-a"]
					record.ReceiptDigest = "other"
					s.NativeIntegrationOperations[record.OperationKey] = record
				case "policy-outcome":
					outcome := s.NativePolicyOutcomes["closed-accept-task-a"]
					outcome.State = "unknown"
					s.NativePolicyOutcomes[outcome.OperationKey] = outcome
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(f.a.path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.a.StartScopedController(f.token, f.s.ID, next); !errors.Is(err, ErrDenied) {
				t.Fatal("closed-task or open-task guard bypassed", change, err)
			}
			after, err := os.ReadFile(f.a.path)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("refused startup changed authority", change, err)
			}
		})
	}
}
