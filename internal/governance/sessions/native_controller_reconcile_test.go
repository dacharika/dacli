package sessions

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func controllerAcknowledge(t *testing.T, f controllerFixture, action ControllerAction) (Attempt, DispatchIntent) {
	t.Helper()
	return controllerAcknowledgeState(t, f, action, true)
}
func controllerAcknowledgeState(t *testing.T, f controllerFixture, action ControllerAction, terminal bool) (Attempt, DispatchIntent) {
	t.Helper()
	route := f.start.Routes[action.TaskID]
	wt := f.observer.tokens[action.TaskID]
	bt := f.bridgeTokens[route.BridgeSession]
	var intent DispatchIntent
	_ = f.a.transaction(false, false, func(s *state) error { intent = s.DispatchIntents[action.DispatchID]; return nil })
	at, e := f.a.RegisterAttempt(wt, route.WorkerSession, AttemptRequest{RequestID: "actual-" + action.TaskID, TaskID: action.TaskID, Checkout: f.p.Placements[action.TaskID].Checkout, HostLabel: "cooperative fixture"})
	if e != nil {
		t.Fatal(e)
	}
	intent, e = f.a.CorrelateDispatchIntent(bt, route.BridgeSession, correlation(intent, "correlate-"+action.TaskID, "host_correlated"))
	if e != nil {
		t.Fatal(e)
	}
	intent, e = f.a.AcknowledgeDispatchIntent(wt, route.WorkerSession, AcknowledgeDispatchIntent{OperationKey: "ack-" + action.TaskID, DispatchID: action.DispatchID, IntentID: intent.ID, AttemptID: at.ID, HostAgentID: intent.HostAgentID, ExpectedFence: action.Fence, ExpectedSequence: intent.Sequence})
	if e != nil {
		t.Fatal(e)
	}
	if terminal {
		_, e = f.a.ObserveLifecycle(bt, route.BridgeSession, ObserveLifecycleRequest{OperationKey: "completed-" + action.TaskID, Binding: lifecycleBinding(intent), ExpectedSequence: 0, HostSequence: 1, State: "completed", Source: "owner-manual-native-host", EvidenceDigest: OutputDigest([]byte("actual cooperative completion report")), SourceTime: time.Now().UTC()})
		if e != nil {
			t.Fatal(e)
		}
	}
	return at, intent
}
func controllerCompletedWave(t *testing.T) (controllerFixture, ControllerStatus) {
	t.Helper()
	return controllerCompletedWaveFrom(t, controllerSetup(t))
}
func controllerPublishedWaveFrom(t *testing.T, f controllerFixture) (controllerFixture, ControllerStatus) {
	t.Helper()
	if e := f.a.ConfigureLifecycle(f.owner, LifecyclePolicy{SchemaVersion: 1, ProjectID: "project", Revision: "life", Source: "owner-manual-native-host", Freshness: time.Minute, CancellationChannel: "unsupported"}); e != nil {
		t.Fatal(e)
	}
	st := controllerStart(t, f)
	st = controllerStep(t, f, st, "reserve")
	st = controllerStep(t, f, st, "first")
	st = controllerStep(t, f, st, "second")
	for _, action := range st.Actions {
		at, intent := controllerAcknowledge(t, f, action)
		route := f.start.Routes[action.TaskID]
		_, e := f.a.PublishAttemptRevision(f.observer.tokens[action.TaskID], route.WorkerSession, PublishRevisionRequest{OperationKey: "revision-" + action.TaskID, AttemptID: at.ID, LeaseID: intent.Lease.ID, ExpectedFence: intent.Lease.Fence})
		if e != nil {
			t.Fatal(e)
		}
	}
	return f, st
}
func controllerCompletedWaveFrom(t *testing.T, f controllerFixture) (controllerFixture, ControllerStatus) {
	f, st := controllerPublishedWaveFrom(t, f)
	st = controllerStep(t, f, st, "verification-descriptors")
	if st.Phase != "verification" || len(st.Actions) != 2 || st.Actions[0].Kind != "verification" || st.Actions[1].Kind != "verification" {
		t.Fatal("actual completed host blocked independent verification or starved second member", st)
	}
	return f, st
}
func TestNativeControllerActualCompletedProducerAndFailureCorrection(t *testing.T) {
	f, st := controllerCompletedWave(t)
	// This kernel fixture invokes the authenticated producer contract. Real command
	// execution and independent private profiles are application fixture evidence.
	action := st.Actions[0]
	route := f.start.Routes[action.TaskID]
	request := action.Verification
	var grant VerificationGrant
	_ = f.a.transaction(false, false, func(s *state) error { grant = s.Grants[request.GrantID]; return nil })
	runnerToken := f.runnerTokens[route.RunnerSession]
	rev, _, e := f.a.BeginVerification(f.observer.tokens[action.TaskID], runnerToken, route.RunnerSession, request, grant.CommandDigest)
	if e != nil {
		t.Fatal(e)
	}
	_, e = f.a.CompleteVerification(f.observer.tokens[action.TaskID], runnerToken, route.RunnerSession, request, VerificationResult{Commit: rev.Observation.Commit, Tree: rev.Observation.Tree, Directory: rev.Observation.Locator, CommandDigest: grant.CommandDigest, OutputDigest: OutputDigest([]byte("failure")), Output: []byte("failure"), ExitCode: 1, Clean: true})
	if e != nil {
		t.Fatal(e)
	}
	st = controllerStep(t, f, st, "correct-failure")
	if st.Corrections != 1 || st.Actions[0].Kind != "correction" {
		t.Fatal("authenticated failed verification could not issue one correction", st)
	}
}
func TestNativeControllerUnknownCyclesAndLateReconciliation(t *testing.T) {
	f := controllerSetup(t)
	st := controllerStart(t, f)
	st = controllerStep(t, f, st, "reserve")
	st = controllerStep(t, f, st, "intent-one")
	st = controllerStep(t, f, st, "intent-two")
	for n, key := range []string{"unknown-one", "unknown-two", "unknown-three"} {
		st = controllerStep(t, f, st, key)
		if len(st.Actions) != 2 || st.Actions[0].IntentID == "" || st.Actions[1].IntentID == "" {
			t.Fatal("unknown host erased original pending actions", st)
		}
		if n < 2 && st.Terminal {
			t.Fatal("unknown prematurely halted", st)
		}
	}
	if !st.Terminal || st.HaltClass != "no_progress" || st.Waves != 1 {
		t.Fatal("actual unknown execution escaped bounded no-progress", st)
	}
	before := st
	st, e := f.a.ReconcileController(f.token, f.s.ID, ControllerStepRequest{OperationKey: "late-observe", ProjectID: st.ProjectID, InvocationID: st.ID, ExpectedSequence: st.Sequence})
	if e != nil || !st.Terminal || st.HaltClass != before.HaltClass || st.Waves != before.Waves || st.NoProgress != before.NoProgress || st.HostActionAuthorized {
		t.Fatal("terminal invocation denied safe late reconcile or reset bounds", e, st)
	}
	next := f.start
	next.OperationKey = "new-start"
	next.InvocationID = "new-id"
	if _, e = f.a.StartController(f.owner, next); !errors.Is(e, ErrDenied) {
		t.Fatal("terminal unknown reset invocation limits", e)
	}
}
func TestNativeControllerLateReservationFailureDiscardsTrial(t *testing.T) {
	control := controllerSetup(t)
	controlStatus := controllerStart(t, control)
	lastObserver := 0
	control.observer.capabilityHook = func() { lastObserver++ }
	committed := controllerStep(t, control, controlStatus, "control-reserve")
	if committed.Waves != 1 || len(committed.Actions) != 2 || lastObserver == 0 {
		t.Fatal("control failed to locate complete reservation observer boundary")
	}
	f := controllerSetup(t)
	st := controllerStart(t, f)
	calls := 0
	f.observer.capabilityHook = func() {
		calls++
		if calls == lastObserver {
			f.observer.capabilityDenied = true
		}
	}
	t.Log("late refusal at last observer of committed reservation", lastObserver)
	// Configure a deterministic final-admission refusal without altering canonical
	// claims. The public helper may have minted trial fences; none can persist.
	var fence uint64
	_ = f.a.transaction(false, false, func(s *state) error { fence = s.Fence; return nil })
	out, e := f.a.StepController(f.token, f.s.ID, ControllerStepRequest{OperationKey: "late-failure", ProjectID: st.ProjectID, InvocationID: st.ID, ExpectedSequence: st.Sequence})
	if !errors.Is(e, ErrDenied) || !out.Terminal {
		t.Fatal("fixture did not reach late reserve failure", calls, e, out)
	}
	_ = f.a.transaction(false, false, func(s *state) error {
		if s.Fence != fence || len(s.Dispatches) != 0 || len(s.BudgetAllocations) != 0 || s.ControllerInvocations[st.ID].Status.Waves != 0 {
			t.Fatal("late reserve refusal persisted partial trial allocation or counter")
		}
		return nil
	})
	raw, _ := os.ReadFile(f.a.path)
	replay, e := f.a.StepController(f.token, f.s.ID, ControllerStepRequest{OperationKey: "late-failure", ProjectID: st.ProjectID, InvocationID: st.ID, ExpectedSequence: st.Sequence})
	after, _ := os.ReadFile(f.a.path)
	// Historical replay preserves the first refusal exit contract, without new effects.
	if !errors.Is(e, ErrDenied) || replay.HostActionAuthorized || !reflect.DeepEqual(raw, after) {
		t.Fatal("halt receipt replay changed authority", e)
	}
}

func TestNativeControllerSTOPMidWaveRetainsProtectionAndLateProducer(t *testing.T) {
	f := controllerSetup(t)
	if e := f.a.ConfigureLifecycle(f.owner, LifecyclePolicy{SchemaVersion: 1, ProjectID: "project", Revision: "life", Source: "owner-manual-native-host", Freshness: time.Minute, CancellationChannel: "unsupported"}); e != nil {
		t.Fatal(e)
	}
	st := controllerStart(t, f)
	st = controllerStep(t, f, st, "reserve")
	st = controllerStep(t, f, st, "first")
	before := st
	_, intent := controllerAcknowledgeState(t, f, before.Actions[0], false)
	p := f.p
	p.Stopped = true
	p.Revision = "stop"
	if e := f.a.ConfigureSwarm(f.owner, p); e != nil {
		t.Fatal(e)
	}
	next, stopErr := f.a.StepController(f.token, f.s.ID, ControllerStepRequest{OperationKey: "stop", ProjectID: st.ProjectID, InvocationID: st.ID, ExpectedSequence: st.Sequence})
	st = next
	if stopErr != nil || !st.Terminal || st.HaltClass != "stopped" || st.HostActionAuthorized || st.Waves != before.Waves {
		t.Fatal("STOP admitted effects or lost bound", st)
	}
	// A producer acknowledgement/terminal report remains independent of new controller permits.
	route := f.start.Routes[before.Actions[0].TaskID]
	_, e := f.a.ObserveLifecycle(f.bridgeTokens[route.BridgeSession], route.BridgeSession, ObserveLifecycleRequest{OperationKey: "late-completion", Binding: lifecycleBinding(intent), ExpectedSequence: 0, HostSequence: 1, State: "completed", Source: "owner-manual-native-host", EvidenceDigest: OutputDigest([]byte("late report")), SourceTime: time.Now().UTC()})
	if e != nil {
		t.Fatal("late producer denied", e)
	}
	late, e := f.a.ReconcileController(f.token, f.s.ID, ControllerStepRequest{OperationKey: "late-stop", ProjectID: st.ProjectID, InvocationID: st.ID, ExpectedSequence: st.Sequence})
	if e != nil || !late.Terminal || late.HaltClass != "stopped" || late.HostActionAuthorized || late.Waves != st.Waves || late.NoProgress != st.NoProgress {
		t.Fatal("STOP late observation reopened effects or denied knowledge", late, e)
	}
	_ = f.a.transaction(false, false, func(s *state) error {
		if len(s.Dispatches) != 2 || len(s.Leases) != 1 {
			t.Fatal("STOP released original protections")
		}
		for _, l := range s.Leases {
			if l.Released {
				t.Fatal("STOP released original lease")
			}
		}
		return nil
	})
}

func TestNativeControllerElapsedLimitIndependentOfNoProgress(t *testing.T) {
	f := controllerSetup(t)
	f.policy.Revision = "short-elapsed"
	f.policy.MaxElapsed = 500 * time.Millisecond
	if e := f.a.ConfigureController(f.owner, f.policy); e != nil {
		t.Fatal(e)
	}
	f.start.PolicyRevision = f.policy.Revision
	st := controllerStart(t, f)
	time.Sleep(time.Until(st.Deadline) + 10*time.Millisecond)
	next, elapsedErr := f.a.StepController(f.token, f.s.ID, ControllerStepRequest{OperationKey: "elapsed", ProjectID: st.ProjectID, InvocationID: st.ID, ExpectedSequence: st.Sequence})
	st = next
	if elapsedErr != nil || !st.Terminal || st.HaltClass != "elapsed_limit" || st.NoProgress != 0 || st.Waves != 0 || st.HostActionAuthorized {
		t.Fatal("elapsed limit bypassed before work", st)
	}
}

func TestNativeControllerExactTaskSetAndConcurrencyAdmission(t *testing.T) {
	for _, narrow := range []bool{false, true} {
		t.Run(map[bool]string{false: "concurrency-one", true: "exact-task-set"}[narrow], func(t *testing.T) {
			f := controllerSetup(t)
			f.policy.Revision = "narrow"
			if narrow {
				var cp CheckpointPolicy
				_ = f.a.transaction(false, false, func(s *state) error { cp = s.CheckpointPolicies["project"]; return nil })
				sw := f.p
				sw.Revision = "narrow"
				sw.Placements = map[string]SwarmPlacement{}
				for task, placement := range f.p.Placements {
					if task != "task-a" {
						sw.Placements[task] = placement
					}
				}
				if e := f.a.ConfigureSwarm(f.owner, sw); e != nil {
					t.Fatal(e)
				}
				cp.Revision = "narrow"
				cp.TaskIDs = []string{"task-b", "task-c"}
				if e := f.a.ConfigureCheckpoints(f.owner, cp); e != nil {
					t.Fatal(e)
				}
				f.policy.TaskIDs = cp.TaskIDs
				delete(f.start.Routes, "task-a")
			} else {
				f.policy.MaxConcurrency = 1
			}
			if e := f.a.ConfigureController(f.owner, f.policy); e != nil {
				t.Fatal(e)
			}
			f.start.PolicyRevision = f.policy.Revision
			st := controllerStart(t, f)
			if narrow {
				sw := f.p
				sw.Revision = "wide-after-start"
				if e := f.a.ConfigureSwarm(f.owner, sw); e != nil {
					t.Fatal(e)
				}
			}
			st = controllerStep(t, f, st, "reserve")
			count := 1
			first := "task-a"
			if narrow {
				count = 2
				first = "task-b"
			}
			if st.Waves != 1 || len(st.Actions) != count || st.Actions[0].TaskID != first || (narrow && st.Actions[1].TaskID != "task-c") {
				t.Fatal("exact task set or concurrency admitted extra reservation", st)
			}
			_ = f.a.transaction(false, false, func(s *state) error {
				if len(s.Dispatches) != count || len(s.BudgetAllocations) != count {
					t.Fatal("extra invocation allocation persisted")
				}
				return nil
			})
		})
	}
}
func TestNativeControllerEmptyBacklogDistinctFromBlocked(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(map[bool]string{false: "blocked", true: "empty"}[empty], func(t *testing.T) {
			f := controllerSetup(t)
			st := controllerStart(t, f)
			for i := range f.observer.snapshot.Candidates {
				if empty {
					f.observer.snapshot.Candidates[i].Status = "done"
					f.observer.snapshot.Candidates[i].Reason = "status_done"
				} else {
					f.observer.snapshot.Candidates[i].Reason = "dependencies_incomplete"
				}
			}
			next, e := f.a.StepController(f.token, f.s.ID, ControllerStepRequest{OperationKey: "no-work", ProjectID: st.ProjectID, InvocationID: st.ID, ExpectedSequence: st.Sequence})
			if e != nil || next.TransientAttempts != 0 || !next.NextRetryAt.IsZero() {
				t.Fatal("blocked readiness spent transient budget or was treated as transient", next, e)
			}
			st = next
			if st.Waves != 0 || len(st.Actions) != 0 || st.HostActionAuthorized {
				t.Fatal("empty/blocked manufactured work", st)
			}
			if empty && (!st.Terminal || st.HaltClass != "empty_backlog" || st.Phase != "completed") {
				t.Fatal("genuinely empty backlog did not end", st)
			}
			if !empty && (st.HaltClass == "empty_backlog" || st.Phase == "completed") {
				t.Fatal("blocked backlog falsely completed", st)
			}
		})
	}
}

func controllerFailEvidence(t *testing.T, f controllerFixture, action ControllerAction) {
	t.Helper()
	route := f.start.Routes[action.TaskID]
	request := action.Verification
	var grant VerificationGrant
	_ = f.a.transaction(false, false, func(s *state) error { grant = s.Grants[request.GrantID]; return nil })
	token := f.runnerTokens[route.RunnerSession]
	rev, _, e := f.a.BeginVerification(f.observer.tokens[action.TaskID], token, route.RunnerSession, request, grant.CommandDigest)
	if e != nil {
		t.Fatal(e)
	}
	_, e = f.a.CompleteVerification(f.observer.tokens[action.TaskID], token, route.RunnerSession, request, VerificationResult{Commit: rev.Observation.Commit, Tree: rev.Observation.Tree, Directory: rev.Observation.Locator, CommandDigest: grant.CommandDigest, OutputDigest: OutputDigest([]byte("failed")), Output: []byte("failed"), ExitCode: 1, Clean: true})
	if e != nil {
		t.Fatal(e)
	}
}
func TestNativeControllerRetiredMemberDoesNotBlockLiveMember(t *testing.T) {
	f, st := controllerCompletedWave(t)
	old := st.Actions[0]
	live := st.Actions[1]
	target, e := f.a.OwnerRecoveryTarget(f.owner, "lease", old.LeaseID)
	if e != nil {
		t.Fatal(e)
	}
	receipt, e := f.a.Recover(f.owner, RecoveryRequest{OperationKey: "retire-a", Target: target, Mode: "owner-reconciliation", Rationale: "owner inspected isolated fixture", Uncertainty: "all writers stopped remains unverified", ReleaseRisk: "owner accepts retained writer risk"})
	if e != nil {
		t.Fatal(e)
	}
	controllerFailEvidence(t, f, live)
	st = controllerStep(t, f, st, "live-b-after-recovery")
	if st.Actions[1].Kind != "correction" || st.Corrections != 1 || st.Actions[0].DispatchID != old.DispatchID {
		t.Fatal("exact retired member blocked healthy live producer", st)
	}
	_ = f.a.transaction(false, false, func(s *state) error {
		if !s.Leases[old.LeaseID].Released || s.Leases[live.LeaseID].Released || len(s.Dispatches) != 2 || len(s.Recoveries) != 1 {
			t.Fatal("recovery history or unrelated protection lost", receipt)
		}
		return nil
	})
}
func TestNativeControllerCorrectionLimitStopsWidthTwoEffects(t *testing.T) {
	f := controllerSetup(t)
	f.policy.Revision = "no-corrections"
	f.policy.MaxCorrectionRounds = 0
	if e := f.a.ConfigureController(f.owner, f.policy); e != nil {
		t.Fatal(e)
	}
	f.start.PolicyRevision = f.policy.Revision
	if e := f.a.ConfigureLifecycle(f.owner, LifecyclePolicy{SchemaVersion: 1, ProjectID: "project", Revision: "life", Source: "owner-manual-native-host", Freshness: time.Minute, CancellationChannel: "unsupported"}); e != nil {
		t.Fatal(e)
	}
	st := controllerStart(t, f)
	st = controllerStep(t, f, st, "reserve")
	st = controllerStep(t, f, st, "first")
	st = controllerStep(t, f, st, "second")
	for _, action := range st.Actions {
		at, intent := controllerAcknowledge(t, f, action)
		route := f.start.Routes[action.TaskID]
		if _, e := f.a.PublishAttemptRevision(f.observer.tokens[action.TaskID], route.WorkerSession, PublishRevisionRequest{OperationKey: "revision-" + action.TaskID, AttemptID: at.ID, LeaseID: intent.Lease.ID, ExpectedFence: intent.Lease.Fence}); e != nil {
			t.Fatal(e)
		}
	}
	st = controllerStep(t, f, st, "verification")
	second := st.Actions[1]
	controllerFailEvidence(t, f, st.Actions[0])
	st = controllerStep(t, f, st, "exhausted")
	if !st.Terminal || st.HaltClass != "correction_limit" || st.Corrections != 0 || !reflect.DeepEqual(st.Actions[1], second) || st.HostActionAuthorized {
		t.Fatal("correction exhaustion staged later width2 effects", st)
	}
}

func TestNativeControllerMultiwaveRecoveryReopenAndMaxWaves(t *testing.T) {
	for _, maximum := range []int{1, 4} {
		t.Run(map[int]string{1: "wave-bound", 4: "two-waves"}[maximum], func(t *testing.T) {
			f := controllerSetup(t)
			f.policy.Revision = "wave-policy"
			f.policy.MaxWaves = maximum
			if e := f.a.ConfigureController(f.owner, f.policy); e != nil {
				t.Fatal(e)
			}
			f.start.PolicyRevision = f.policy.Revision
			f, st := controllerCompletedWaveFrom(t, f)
			oldIDs := []string{}
			remaining := map[string]bool{"task-a": true, "task-b": true, "task-c": true}
			for _, action := range st.Actions {
				oldIDs = append(oldIDs, action.DispatchID)
				delete(remaining, action.TaskID)
				target, e := f.a.OwnerRecoveryTarget(f.owner, "lease", action.LeaseID)
				if e != nil {
					t.Fatal(e)
				}
				if _, e = f.a.Recover(f.owner, RecoveryRequest{OperationKey: "retire-" + action.TaskID, Target: target, Mode: "owner-reconciliation", Rationale: "owner inspected isolated fixture", Uncertainty: "all writers stopped remains unverified", ReleaseRisk: "owner accepts retained writer risk"}); e != nil {
					t.Fatal(e)
				}
				for i := range f.observer.snapshot.Candidates {
					if f.observer.snapshot.Candidates[i].TaskID == action.TaskID {
						f.observer.snapshot.Candidates[i].Status = "done"
						f.observer.snapshot.Candidates[i].Reason = "status_done"
					}
				}
			}
			reopened, e := Open(f.a.path, f.a.binding, f.observer)
			if e != nil {
				t.Fatal(e)
			}
			f.a = reopened
			st = controllerStep(t, f, st, "retired-wave")
			if len(st.Actions) != 0 || st.Waves != 1 {
				t.Fatal("reopen lost committed wave or retirement", st)
			}
			st = controllerStep(t, f, st, "next-cycle")
			if maximum == 1 {
				if !st.Terminal || st.HaltClass != "wave_limit" || st.Waves != 1 || len(st.Actions) != 0 {
					t.Fatal("committed wave bound bypassed", st)
				}
			} else {
				if st.Waves != 2 || len(st.Actions) != 1 || !remaining[st.Actions[0].TaskID] || st.CycleOrdinal != 2 {
					t.Fatal("nonzero second wave failed after actual owner recovery", st)
				}
			}
			_ = f.a.transaction(false, false, func(s *state) error {
				for _, id := range oldIDs {
					if _, ok := s.Dispatches[id]; !ok {
						t.Fatal("restart lost original dispatch history")
					}
				}
				if len(s.Recoveries) != 2 {
					t.Fatal("restart lost recovery authority")
				}
				return nil
			})
		})
	}
}

func TestNativeControllerTransientBackoffDoesNotSpendPollBudget(t *testing.T) {
	f := controllerSetup(t)
	f.policy.Revision = "retry-policy"
	f.policy.RetryBackoff = 2 * time.Second
	if e := f.a.ConfigureController(f.owner, f.policy); e != nil {
		t.Fatal(e)
	}
	f.start.PolicyRevision = f.policy.Revision
	st := controllerStart(t, f)
	for task, o := range f.observer.observations {
		o.Phases = append(o.Phases, PreflightPhase{Phase: "integration", Verdict: "transient", Required: true, Assurance: "owner-manual"})
		f.observer.observations[task] = o
	}
	request := ControllerStepRequest{OperationKey: "transient", ProjectID: st.ProjectID, InvocationID: st.ID, ExpectedSequence: st.Sequence}
	first, e := f.a.StepController(f.token, f.s.ID, request)
	if !errors.Is(e, ErrPreflightTransient) || first.TransientAttempts != 1 || first.Terminal || first.Waves != 0 || first.HaltClass != "transient_preflight" {
		t.Fatal("actual transient failure not bounded or typed", first, e)
	}
	raw, _ := os.ReadFile(f.a.path)
	replay, e := f.a.StepController(f.token, f.s.ID, request)
	after, _ := os.ReadFile(f.a.path)
	if !errors.Is(e, ErrPreflightTransient) || !replay.Historical || replay.TransientAttempts != 1 || !reflect.DeepEqual(raw, after) {
		t.Fatal("transient replay changed exit1 or consumed budget", replay, e)
	}
	if !time.Now().Before(first.NextRetryAt) {
		t.Fatal("fixture early poll missed recorded deadline")
	}
	poll, pollErr := f.a.StepController(f.token, f.s.ID, ControllerStepRequest{OperationKey: "early-poll", ProjectID: first.ProjectID, InvocationID: first.ID, ExpectedSequence: first.Sequence})
	if pollErr != nil || poll.Terminal || poll.TransientAttempts != 1 || poll.HaltClass != "transient_backoff" || poll.ProgressDigest != first.ProgressDigest || poll.NoProgress != first.NoProgress || poll.Waves != 0 {
		t.Fatal("early backoff poll spent attempt or consumed progress", poll)
	}
	time.Sleep(time.Until(first.NextRetryAt) + 10*time.Millisecond)
	exhausted, e := f.a.StepController(f.token, f.s.ID, ControllerStepRequest{OperationKey: "exhausted", ProjectID: poll.ProjectID, InvocationID: poll.ID, ExpectedSequence: poll.Sequence})
	if !errors.Is(e, ErrDenied) || !exhausted.Terminal || exhausted.HaltClass != "transient_retry_limit" || exhausted.TransientAttempts != 1 || exhausted.Waves != 0 {
		t.Fatal("unchanged transient escaped independent retry bound", exhausted, e)
	}
}

func TestNativeControllerFinalWholeWaveExpiryDiscardsEarlierAction(t *testing.T) {
	f, st := controllerCompletedWave(t)
	controllerFailEvidence(t, f, st.Actions[0])
	first := st.Actions[0]
	second := st.Actions[1]
	realGit, e := exec.LookPath("git")
	if e != nil {
		t.Fatal(e)
	}
	shim := t.TempDir()
	marker := filepath.Join(shim, "observed")
	// Delay only the second task's real checkout observation, after the first
	// task has staged its correction. The real Git command remains unchanged.
	script := `#!/bin/sh
if [ "$5" = "$DACLI_DELAY_CHECKOUT" ] && [ ! -e "$DACLI_DELAY_MARKER" ]; then
 date -u +%s > "$DACLI_DELAY_MARKER"
 sleep 4
fi
exec "$DACLI_REAL_GIT" "$@"
`
	if e = os.WriteFile(filepath.Join(shim, "git"), []byte(script), 0700); e != nil {
		t.Fatal(e)
	}
	t.Setenv("DACLI_REAL_GIT", realGit)
	t.Setenv("DACLI_DELAY_CHECKOUT", f.p.Placements[second.TaskID].Checkout)
	t.Setenv("DACLI_DELAY_MARKER", marker)
	t.Setenv("PATH", shim+string(os.PathListSeparator)+os.Getenv("PATH"))
	var expires time.Time
	_ = f.a.transaction(false, true, func(s *state) error {
		g := s.Grants[f.start.Routes[first.TaskID].VerificationGrantID]
		g.ExpiresAt = time.Now().Add(3 * time.Second)
		expires = g.ExpiresAt
		s.Grants[g.ID] = g
		return nil
	})
	before, _ := os.ReadFile(f.a.path)
	out, e := f.a.StepController(f.token, f.s.ID, ControllerStepRequest{OperationKey: "late-second", ProjectID: st.ProjectID, InvocationID: st.ID, ExpectedSequence: st.Sequence})
	after, _ := os.ReadFile(f.a.path)
	stamp, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal("fixture did not reach late second observer", err)
	}
	seconds, err := strconv.ParseInt(strings.TrimSpace(string(stamp)), 10, 64)
	if err != nil || !time.Unix(seconds, 0).Before(expires) || !time.Now().After(expires) {
		t.Fatal("fixture expiry did not occur during second observer", string(stamp), expires, err)
	}
	t.Log("second observer started before first grant expiry; final observation ended after expiry")
	if !errors.Is(e, ErrDenied) || out.ID != "" || !reflect.DeepEqual(before, after) {
		t.Fatal("late second observer expiry persisted earlier action or partial counter", out, e)
	}
}

func TestNativeControllerBeforePersistTransitionFailuresRemainUnknown(t *testing.T) {
	for _, phase := range []string{"reserve", "dispatch", "verification", "independent-review", "correction", "integration", "halt", "reconcile"} {
		t.Run(phase, func(t *testing.T) {
			f := controllerSetup(t)
			var st ControllerStatus
			if phase == "correction" || phase == "integration" || phase == "independent-review" {
				f, st = controllerCompletedWaveFrom(t, f)
			} else {
				st = controllerStart(t, f)
			}
			if phase == "dispatch" {
				st = controllerStep(t, f, st, "reserve")
			}
			if phase == "verification" {
				f, st = controllerPublishedWaveFrom(t, f)
			}
			if phase == "correction" {
				controllerFailEvidence(t, f, st.Actions[0])
			}
			if phase == "integration" || phase == "independent-review" {
				for _, action := range st.Actions {
					route := f.start.Routes[action.TaskID]
					r := action.Verification
					var g VerificationGrant
					_ = f.a.transaction(false, false, func(s *state) error { g = s.Grants[r.GrantID]; return nil })
					rev, _, e := f.a.BeginVerification(f.observer.tokens[action.TaskID], f.runnerTokens[route.RunnerSession], route.RunnerSession, r, g.CommandDigest)
					if e != nil {
						t.Fatal(e)
					}
					ev, e := f.a.CompleteVerification(f.observer.tokens[action.TaskID], f.runnerTokens[route.RunnerSession], route.RunnerSession, r, VerificationResult{Commit: rev.Observation.Commit, Tree: rev.Observation.Tree, Directory: rev.Observation.Locator, CommandDigest: g.CommandDigest, OutputDigest: OutputDigest([]byte("ok")), Output: []byte("ok"), ExitCode: 0, Clean: true})
					if e != nil {
						t.Fatal(e)
					}
					f.a.reviewObserver = checkpointReviewObservation{f.observer.observations[action.TaskID].ReviewObservation}
					if phase == "independent-review" {
						continue
					}
					if _, e = f.a.ProposeReview(f.reviewerTokens[route.ReviewerSession], route.ReviewerSession, ReviewRequest{OperationKey: "review-" + action.TaskID, GrantID: route.ReviewGrantID, AttemptID: action.AttemptID, LeaseID: action.LeaseID, RevisionID: action.RevisionID, ExpectedFence: action.Fence, Outcome: "approve", Rationale: "independent contract fixture", EvidenceIDs: []string{ev.ID}}); e != nil {
						t.Fatal(e)
					}
				}
			}
			if phase == "halt" {
				p := f.p
				p.Revision = "stop"
				p.Stopped = true
				if e := f.a.ConfigureSwarm(f.owner, p); e != nil {
					t.Fatal(e)
				}
			}
			request := ControllerStepRequest{OperationKey: "persist-failure", ProjectID: st.ProjectID, InvocationID: st.ID, ExpectedSequence: st.Sequence}
			before, _ := os.ReadFile(f.a.path)
			f.a.persistOverride = func([]byte) error { return ErrStorage }
			var out ControllerStatus
			var e error
			if phase == "reconcile" {
				out, e = f.a.ReconcileController(f.token, f.s.ID, request)
			} else {
				out, e = f.a.StepController(f.token, f.s.ID, request)
			}
			after, _ := os.ReadFile(f.a.path)
			if !errors.Is(e, ErrStorage) || out.ID != "" || !reflect.DeepEqual(before, after) {
				t.Fatal("prepersist failure exposed status or persisted partial transition", phase, out, e)
			}
			f.a.persistOverride = nil
			// No successful receipt exists: identical original request may commit once.
			if phase == "reconcile" {
				out, e = f.a.ReconcileController(f.token, f.s.ID, request)
			} else {
				out, e = f.a.StepController(f.token, f.s.ID, request)
			}
			if e != nil || out.Sequence != st.Sequence+1 {
				t.Fatal("original uncertain request failed fresh reconciliation", phase, out, e)
			}
			if phase == "verification" && out.Phase != "verification" {
				t.Fatal("initial verification descriptor staging absent", out)
			}
			if phase == "independent-review" && out.Phase != "review" {
				t.Fatal("initial independent review descriptor staging absent", out)
			}
			if phase == "integration" && (out.Phase != "integration" || out.HaltClass != "pending_local_integration") {
				t.Fatal("actual contract producer integration checkpoint absent", out)
			}
		})
	}
}

func TestNativeControllerStartPersistenceFailureAndReopen(t *testing.T) {
	f := controllerSetup(t)
	before, _ := os.ReadFile(f.a.path)
	f.a.persistOverride = func([]byte) error { return ErrStorage }
	out, e := f.a.StartController(f.owner, f.start)
	after, _ := os.ReadFile(f.a.path)
	if !errors.Is(e, ErrStorage) || out.ID != "" || !reflect.DeepEqual(before, after) {
		t.Fatal("failed Start exposed or persisted invocation", out, e)
	}
	f.a.persistOverride = nil
	out, e = f.a.StartController(f.owner, f.start)
	if e != nil {
		t.Fatal(e)
	}
	reopened, e := Open(f.a.path, f.a.binding, f.observer)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := os.ReadFile(f.a.path)
	again, e := reopened.StartController(f.owner, f.start)
	after, _ = os.ReadFile(f.a.path)
	if e != nil || !again.Historical || again.ID != out.ID || again.Deadline != out.Deadline || !reflect.DeepEqual(raw, after) {
		t.Fatal("Start reopen reset limits or duplicated invocation", again, e)
	}
}

func TestNativeControllerAdmissionCountersBindGateNotProgress(t *testing.T) {
	for _, field := range []string{"no-progress", "transient-attempts", "backoff", "preflight-generation"} {
		t.Run(field, func(t *testing.T) {
			f := controllerSetup(t)
			st := controllerStart(t, f)
			scope := f.s.Scope
			scope.Actions = nil
			for _, action := range f.s.Scope.Actions {
				if action != "controller.step" && action != "controller.reconcile" {
					scope.Actions = append(scope.Actions, action)
				}
			}
			operator, operatorToken, e := f.a.Enroll(f.owner, scope, time.Now().Add(time.Hour))
			if e != nil {
				t.Fatal(e)
			}
			f.s, f.token = operator, operatorToken
			gate, e := f.a.ReconcileCheckpoint(f.token, f.s.ID, CheckpointRequest{OperationKey: "gate", CheckpointID: st.CheckpointID, ProjectID: st.ProjectID, InvocationID: "independent-gate", Phase: "pre-cycle", ExpectedSequence: 0})
			if e != nil || !gate.WaveAllowed {
				t.Fatal("counter gate fixture not eligible", gate, e)
			}
			plan, e := f.a.PlanSwarm(f.token, f.s.ID, st.ProjectID)
			if e != nil {
				t.Fatal(e)
			}
			_ = f.a.transaction(false, true, func(s *state) error {
				iv := s.ControllerInvocations[st.ID]
				switch field {
				case "no-progress":
					iv.Status.NoProgress++
				case "transient-attempts":
					iv.Status.TransientAttempts++
				case "backoff":
					iv.Status.NextRetryAt = time.Now().Add(time.Minute)
				case "preflight-generation":
					iv.PreflightGeneration = "new-authenticated-generation"
				}
				s.ControllerInvocations[st.ID] = iv
				return nil
			})
			var observed CheckpointStatus
			_ = f.a.transaction(false, false, func(s *state) error {
				p := s.CheckpointPolicies[st.ProjectID]
				var err error
				observed, err = f.a.reconcileCheckpoint(s, p, gate, f.token, f.s.ID)
				return err
			})
			if observed.ConditionDigest == gate.ConditionDigest || observed.ProgressDigest != gate.ProgressDigest {
				t.Fatal("admission counters failed condition binding or manufactured progress", field, observed)
			}
			request := ReserveWaveRequest{OperationKey: "stale-counter-wave", Plan: plan, ExpiresAt: time.Now().Add(time.Minute), CheckpointID: gate.ID, CheckpointSequence: gate.Sequence, InvocationID: gate.InvocationID, ReconciliationDigest: gate.ConditionDigest}
			before, _ := os.ReadFile(f.a.path)
			out, e := f.a.ReserveWave(f.token, f.s.ID, request)
			after, _ := os.ReadFile(f.a.path)
			if !errors.Is(e, ErrDenied) || out.ID != "" || !reflect.DeepEqual(before, after) {
				t.Fatal("changed admission counters consumed stale wave gate", field, out, e)
			}
		})
	}
}
