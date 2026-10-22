package sessions

import (
	"encoding/json"
	"errors"
	"github.com/mlnomadpy/dacli/internal/team"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func budgetSetup(t *testing.T, limit int64) (swarmFixture, *checkpointFixtureAdmission, BudgetPolicy) {
	t.Helper()
	f, ad, _ := checkpointSetup(t)
	if e := f.a.UpgradeBudgets(f.owner); e != nil {
		t.Fatal(e)
	}
	actions := append(append([]string{}, f.s.Scope.Actions...), "budget.read", "budget.quote", "budget.settle", "dispatch.cancel")
	var e error
	f.s, f.token, e = f.a.Enroll(f.owner, Scope{Actions: actions, Resources: f.s.Scope.Resources}, time.Now().Add(time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	now := time.Now().UTC()
	p := BudgetPolicy{SchemaVersion: 1, ProjectID: "project", Revision: "budget-one", CycleID: "cycle-one", Harness: "codex", Rationale: "owner estimated fixture units; no provider measurement", CycleLimit: limit, WindowLimit: limit, RecoveryFloor: 5, Window: time.Hour, WindowOrigin: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour), Tasks: map[string]BudgetTask{}}
	for task, spec := range ad.observations {
		_ = spec
		p.Tasks[task] = BudgetTask{Phases: map[string]BudgetEstimate{}}
		for phase, tokens := range map[string]int64{"implementation": 20, "review": 10, "correction": 5, "re-review": 5, "verification": 0, "integration": 10, "recovery": 5} {
			role := "codex-implement"
			if phase == "review" || phase == "re-review" {
				role = "codex-review"
			}
			p.Tasks[task].Phases[phase] = BudgetEstimate{RoleID: role, Model: "model", Tokens: tokens, Provenance: "owner-estimate", Samples: 0, ObservedAt: now}
		}
	}
	if e = f.a.ConfigureBudget(f.owner, p); e != nil {
		t.Fatal(e)
	}
	return f, ad, p
}
func budgetReserve(t *testing.T, f swarmFixture, key string) SwarmWave {
	t.Helper()
	st, e := f.a.ReconcileCheckpoint(f.token, f.s.ID, checkpointRequest("checkpoint-"+key, "inv-"+key, 0))
	if e != nil || !st.WaveAllowed {
		t.Fatalf("fresh budget precycle: %+v %v", st, e)
	}
	plan, e := f.a.PlanSwarm(f.token, f.s.ID, "project")
	if e != nil {
		t.Fatal(e)
	}
	wave, e := f.a.ReserveWave(f.token, f.s.ID, ReserveWaveRequest{OperationKey: key, Plan: plan, ExpiresAt: time.Now().Add(time.Minute), CheckpointID: st.ID, CheckpointSequence: st.Sequence, InvocationID: st.InvocationID, ReconciliationDigest: st.ConditionDigest})
	if e != nil {
		t.Fatal(e)
	}
	return wave
}
func TestNativeBudgetPreviewLiveCompleteCycleAndRecoveryFloor(t *testing.T) {
	f, _, _ := budgetSetup(t, 70)
	quote, e := f.a.QuoteBudget(f.token, f.s.ID, "project", "task-a")
	if e != nil || !quote.Allowed || quote.Total != 55 || quote.Review != 15 || quote.Integration != 10 || quote.Recovery != 5 {
		t.Fatalf("full-phase quote differs from protected estimates: %+v %v", quote, e)
	}
	plan, e := f.a.PlanSwarm(f.token, f.s.ID, "project")
	if e != nil {
		t.Fatal(e)
	}
	selected := 0
	for _, v := range plan.Verdicts {
		if v.Selected {
			selected++
			if v.Budget == nil || v.Budget.Digest != quote.Digest {
				t.Fatalf("preview/live quote divergence: %+v", v)
			}
		}
	}
	if selected != 1 {
		t.Fatalf("budget must shrink to one complete cycle before reservation: %d", selected)
	}
	wave := budgetReserve(t, f, "wave-one")
	if len(wave.Dispatches) != 1 {
		t.Fatalf("live wave must match shrunken preview: %+v", wave)
	}
	status, e := f.a.BudgetStatus(f.token, f.s.ID, "project")
	if e != nil || status.Cycle.Reserved != 55 || status.Cycle.Remaining != 15 || status.UnallocatedHeadroom != 10 || status.ReviewReserved != 15 || status.ProviderUsage != nil || status.Enforcement != "local-allocation-only; provider/host spend unsupported" {
		t.Fatalf("reserved units are not free/enforceable provider remaining: %+v %v", status, e)
	}
	next, e := f.a.QuoteBudget(f.token, f.s.ID, "project", "task-c")
	if e != nil || next.Allowed || next.Reason != "insufficient_complete_cycle_headroom" {
		t.Fatalf("reserved units must block another full cycle: %+v %v", next, e)
	}
	// Old plan cannot bypass updated allocation generation. No new fence/allocation.
	before := status.LedgerDigest
	if _, e = f.a.ReserveWave(f.token, f.s.ID, ReserveWaveRequest{OperationKey: "stale-wave", Plan: plan, ExpiresAt: time.Now().Add(time.Minute)}); !errors.Is(e, ErrDenied) {
		t.Fatalf("stale preview cannot allocate twice: %v", e)
	}
	after, _ := f.a.BudgetStatus(f.token, f.s.ID, "project")
	if after.LedgerDigest != before {
		t.Fatal("failed reserve mutated budget")
	}
}
func TestNativeBudgetInsufficientRecoveryAndMissingPhaseRefuse(t *testing.T) {
	f, _, p := budgetSetup(t, 59)
	q, e := f.a.QuoteBudget(f.token, f.s.ID, "project", "task-a")
	if e != nil || q.Allowed || q.Total != 55 {
		t.Fatalf("recovery floor must refuse before dispatch: %+v %v", q, e)
	}
	plan, e := f.a.PlanSwarm(f.token, f.s.ID, "project")
	if e != nil {
		t.Fatal(e)
	}
	for _, v := range plan.Verdicts {
		if v.Selected {
			t.Fatal("underfunded recovery selected a worker")
		}
	}
	p.Revision = "missing-review"
	delete(p.Tasks["task-a"].Phases, "review")
	if e = f.a.ConfigureBudget(f.owner, p); !errors.Is(e, ErrDenied) {
		t.Fatalf("missing required phase must not become zero: %v", e)
	}
}
func TestNativeBudgetRestartWindowUnknownCarryAndSTOP(t *testing.T) {
	f, ad, p := budgetSetup(t, 130)
	wave := budgetReserve(t, f, "wave-one")
	if len(wave.Dispatches) < 1 {
		t.Fatal("no dispatch")
	}
	a, e := Open(f.a.path, f.a.binding, ad)
	if e != nil {
		t.Fatal(e)
	}
	f.a = a
	first, e := a.BudgetStatus(f.token, f.s.ID, "project")
	if e != nil {
		t.Fatal(e)
	}
	// Pure calculator at the exact next-window boundary: unknown/live reservations
	// carry even if their original allocation and native TTL are older.
	if e = a.transaction(false, false, func(s *state) error {
		later, err := budgetStatus(s, p, p.WindowOrigin.Add(2*p.Window))
		if err != nil {
			return err
		}
		if later.Window.Reserved != first.Window.Reserved || later.Window.Spent != 0 || later.ProviderUsage != nil {
			t.Fatalf("window reset freed unknown liability: %+v", later)
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	f.p.Revision = "stopped-policy"
	f.p.Stopped = true
	if e = a.ConfigureSwarm(f.owner, f.p); e != nil {
		t.Fatal(e)
	}
	stopped, e := a.BudgetStatus(f.token, f.s.ID, "project")
	if e != nil || !stopped.STOP || stopped.Window.Reserved != first.Window.Reserved {
		t.Fatalf("STOP erased durable reservations: %+v %v", stopped, e)
	}
	if _, e = a.PlanSwarm(f.token, f.s.ID, "project"); !errors.Is(e, ErrDenied) {
		t.Fatalf("STOP cannot permit model routing: %v", e)
	}
	p.Revision = "cooldown"
	p.CooldownUntil = time.Now().Add(time.Minute)
	if e = a.ConfigureBudget(f.owner, p); e != nil {
		t.Fatal(e)
	}
	q, e := a.QuoteBudget(f.token, f.s.ID, "project", "task-a")
	if e != nil || q.Allowed {
		t.Fatalf("cooldown must persist refusal: %+v %v", q, e)
	}
}
func TestNativeBudgetMigrationPreservesAllMapsAndServices(t *testing.T) {
	f, ad, _ := checkpointSetup(t)
	before, err := os.ReadFile(f.a.path)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.a.UpgradeBudgets(f.owner); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(f.a.path)
	if err != nil {
		t.Fatal(err)
	}
	var old, new map[string]json.RawMessage
	_ = json.Unmarshal(before, &old)
	_ = json.Unmarshal(after, &new)
	for key, value := range old {
		if key == "Version" || key == "BudgetPolicies" || key == "BudgetPolicyHistory" || key == "BudgetAllocations" || key == "BudgetOperations" {
			continue
		}
		if !reflect.DeepEqual(value, new[key]) {
			t.Fatalf("migration dropped prior map %s", key)
		}
	}
	if _, err = f.a.PlanSwarm(f.token, f.s.ID, "project"); err != nil {
		t.Fatalf("schema13 preflight/planning service unusable: %v", err)
	}
	st, err := f.a.ReconcileCheckpoint(f.token, f.s.ID, checkpointRequest("new", "inv-one", 0))
	if err != nil || !st.WaveAllowed {
		t.Fatalf("schema13 checkpoints unusable: %+v %v", st, err)
	}
	a, err := Open(f.a.path, f.a.binding, ad)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.Authenticate(f.token, f.s.ID); err != nil {
		t.Fatal(err)
	}
	if err = f.a.UpgradeBudgets(f.owner); !errors.Is(err, ErrDenied) {
		t.Fatalf("repeat migration must refuse upgraded schema, not reset maps: %v", err)
	}
}
func TestNativeBudgetGateSourceDeepCloneAndTrueProgress(t *testing.T) {
	f, _, _ := budgetSetup(t, 70)
	if e := f.a.transaction(false, false, func(s *state) error {
		copy, e := cloneAuthorityState(s)
		if e != nil {
			return e
		}
		copy.BudgetAllocations["sentinel"] = BudgetAllocation{ID: "sentinel"}
		copy.Checkpoints["sentinel"] = []CheckpointStatus{{Sequence: 9}}
		if _, ok := s.BudgetAllocations["sentinel"]; ok {
			t.Fatal("allocation clone aliases live authority")
		}
		if _, ok := s.Checkpoints["sentinel"]; ok {
			t.Fatal("checkpoint clone aliases live authority")
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	st, e := f.a.ReconcileCheckpoint(f.token, f.s.ID, checkpointRequest("before", "inv-one", 0))
	if e != nil {
		t.Fatal(e)
	}
	if e = f.a.transaction(false, true, func(s *state) error { s.BudgetOperations["bookkeeping"] = BudgetReceipt{Digest: "opaque"}; return nil }); e != nil {
		t.Fatal(e)
	}
	current, e := f.a.CheckpointStatus(f.token, f.s.ID, "project", st.ID)
	if e != nil || current.ConditionDigest == st.ConditionDigest || current.ProgressDigest != st.ProgressDigest {
		t.Fatalf("accounting must invalidate condition without progress reset: %+v %v", current, e)
	}
}

func budgetHost(t *testing.T, f swarmFixture, ad *checkpointFixtureAdmission, wave SwarmWave) (Session, string, DispatchIntent) {
	t.Helper()
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
	at, e := f.a.RegisterAttempt(wt, worker.ID, AttemptRequest{RequestID: "budget-attempt-" + d.ID, TaskID: d.Reservation.TaskID, Checkout: d.Reservation.Checkout, HostLabel: "fixture"})
	if e != nil {
		t.Fatal(e)
	}
	initial, e := f.a.RequestDispatchIntent(f.token, f.s.ID, RequestDispatchIntent{OperationKey: "budget-intent-" + d.ID, DispatchID: d.ID, BridgeSessionID: bridge.ID, WorkerSessionID: worker.ID, PromptSpecDigest: OutputDigest([]byte("fixture")), ExpectedFence: d.Reservation.Fence})
	if e != nil || !initial.HostActionAuthorized {
		t.Fatalf("actual first intent budget permit: %+v %v", initial, e)
	}
	correlated, e := f.a.CorrelateDispatchIntent(bt, bridge.ID, correlation(initial.Intent, "budget-correlate-"+d.ID, "host_correlated"))
	if e != nil {
		t.Fatal(e)
	}
	intent, e := f.a.AcknowledgeDispatchIntent(wt, worker.ID, AcknowledgeDispatchIntent{OperationKey: "budget-ack-" + d.ID, DispatchID: d.ID, IntentID: correlated.ID, AttemptID: at.ID, HostAgentID: correlated.HostAgentID, ExpectedFence: d.Reservation.Fence, ExpectedSequence: correlated.Sequence})
	if e != nil {
		t.Fatal(e)
	}
	if e = f.a.ConfigureLifecycle(f.owner, LifecyclePolicy{SchemaVersion: 1, ProjectID: "project", Revision: "budget-lifecycle", Source: "owner-manual-native-host", Freshness: time.Minute, CancellationEnabled: true, CancellationChannel: "owner-manual-native-host"}); e != nil {
		t.Fatal(e)
	}
	return bridge, bt, intent
}
func TestNativeBudgetActualProducerSettlementCrashIdempotencyAndProtection(t *testing.T) {
	for _, stateName := range []string{"completed", "failed", "unknown", "cancel-ack"} {
		t.Run(stateName, func(t *testing.T) {
			f, ad, p := budgetSetup(t, 70)
			wave := budgetReserve(t, f, "wave-one")
			bridge, bt, intent := budgetHost(t, f, ad, wave)
			var producer string
			if stateName == "cancel-ack" {
				result, e := f.a.CancelLifecycle(f.token, f.s.ID, CancelLifecycleRequest{OperationKey: "budget-cancel", Binding: lifecycleBinding(intent)})
				if e != nil {
					t.Fatal(e)
				}
				ack, e := f.a.AcknowledgeCancellation(bt, bridge.ID, AcknowledgeCancellationRequest{OperationKey: "budget-cancel-ack", Binding: lifecycleBinding(intent), ExpectedSequence: result.Cancellation.Sequence, State: "acknowledged", EvidenceDigest: OutputDigest([]byte("cooperative acknowledgement"))})
				if e != nil {
					t.Fatal(e)
				}
				producer = ack.OperationKey
			} else {
				observation, e := f.a.ObserveLifecycle(bt, bridge.ID, lifecycleObserve(intent, "budget-observation", stateName, 1))
				if e != nil {
					t.Fatal(e)
				}
				producer = BudgetLifecycleProducerID(observation)
			}
			a, e := Open(f.a.path, f.a.binding, ad)
			if e != nil {
				t.Fatal(e)
			}
			f.a = a
			publicStatus, e := a.BudgetStatus(f.token, f.s.ID, "project")
			if e != nil {
				t.Fatal(e)
			}
			found := false
			for _, allocation := range publicStatus.Allocations {
				if allocation.DispatchID != intent.DispatchID {
					continue
				}
				for _, candidate := range allocation.AvailableProducers {
					if candidate.ID == producer && candidate.Phase == "implementation" {
						found = true
						producer = candidate.ID
					}
				}
			}
			if !found {
				t.Fatal("actual CLI status cannot discover safe producer ID for settlement")
			}
			request := BudgetSettleRequest{OperationKey: "settlement-one", DispatchID: intent.DispatchID, Phase: "implementation", ProducerID: producer, ExpectedFence: intent.Reservation.Reservation.Fence}
			settled, e := a.SettleBudget(f.token, f.s.ID, request)
			if e != nil {
				t.Fatal(e)
			}
			original, err := os.ReadFile(a.path)
			if err != nil {
				t.Fatal(err)
			}
			replay, e := a.SettleBudget(f.token, f.s.ID, request)
			if e != nil || !reflect.DeepEqual(settled, replay) {
				t.Fatalf("settlement replay changed outcome: %v", e)
			}
			status, e := a.BudgetStatus(f.token, f.s.ID, "project")
			if e != nil {
				t.Fatal(e)
			}
			charged := stateName != "unknown" && stateName != "cancel-ack"
			if charged && (status.Cycle.Spent != 20 || status.Cycle.Reserved != 35) || !charged && (status.Cycle.Spent != 0 || status.Cycle.Reserved != 55) {
				t.Fatalf("actual terminal/unknown producer allocation settled incorrectly: %+v", status)
			}
			if e = a.transaction(false, false, func(s *state) error {
				lease := s.Leases[intent.Lease.ID]
				if lease.Released || s.Fence != intent.Lease.Fence {
					t.Fatal("accounting released protection or advanced fence")
				}
				later, e := budgetStatus(s, p, time.Now().Add(p.Window))
				if e != nil {
					return e
				}
				if charged && later.Window.Spent != 0 || later.Window.Reserved != status.Window.Reserved {
					t.Fatalf("rolling expiry freed pending phase: %+v", later)
				}
				return nil
			}); e != nil {
				t.Fatal(e)
			}
			var before map[string]json.RawMessage
			_ = json.Unmarshal(original, &before)
			request.OperationKey = "forged-integration"
			request.Phase = "integration"
			request.ProducerID = "caller-says-done"
			if _, e = a.SettleBudget(f.token, f.s.ID, request); !errors.Is(e, ErrDenied) {
				t.Fatalf("contract/caller completion cannot settle native integration: %v", e)
			}
		})
	}
}
func TestNativeBudgetTrueRollingIntervalAndOldCycleUnknownCarry(t *testing.T) {
	f, _, p := budgetSetup(t, 70)
	now := time.Now().UTC()
	if e := f.a.transaction(false, false, func(s *state) error {
		s.BudgetAllocations["old"] = BudgetAllocation{ID: "old", ProjectID: "project", TaskID: "task-a", CycleID: "old-cycle", AllocatedAt: now.Add(-2 * time.Hour), Phases: []BudgetPhase{{Phase: "implementation", Estimate: BudgetEstimate{Tokens: 20}, State: "charged", SettledAt: now.Add(-30 * time.Minute)}, {Phase: "integration", Estimate: BudgetEstimate{Tokens: 10}, State: "unknown-retained"}}}
		p.WindowOrigin = now.Add(-90 * time.Minute)
		status, e := budgetStatus(s, p, now)
		if e != nil {
			return e
		}
		if status.Window.Spent != 20 || status.Window.Reserved != 10 || status.Cycle.Reserved != 10 || !status.WindowReset.Equal(now.Add(30*time.Minute)) {
			t.Fatalf("true rolling interval and old-cycle carry differ: %+v", status)
		}
		atBoundary, e := budgetStatus(s, p, now.Add(30*time.Minute))
		if e != nil {
			return e
		}
		if atBoundary.Window.Spent != 0 || atBoundary.Window.Reserved != 10 {
			t.Fatalf("exact charge expiry freed outstanding uncertainty: %+v", atBoundary)
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
}

func TestNativeBudgetQuoteFinalPreflightExpiryRefuses(t *testing.T) {
	f, ad, _ := budgetSetup(t, 70)
	observation := ad.observations["task-a"]
	observation.ExpiresAt = time.Now().Add(150 * time.Millisecond)
	ad.observations["task-a"] = observation
	calls := 0
	ad.hook = func() {
		calls++
		if calls == 2 {
			time.Sleep(200 * time.Millisecond)
			fresh := ad.observations["task-a"]
			fresh.ExpiresAt = time.Now().Add(time.Hour)
			ad.observations["task-a"] = fresh
		}
	}
	q, e := f.a.QuoteBudget(f.token, f.s.ID, "project", "task-a")
	if !errors.Is(e, ErrDenied) || q.Allowed {
		t.Fatalf("quote cannot return Allowed after original preflight grant/profile expiry: %+v %v", q, e)
	}
}

func TestNativeBudgetSameModelDifferentRoleRequiresOwnerRebind(t *testing.T) {
	f, ad, _ := budgetSetup(t, 70)
	var p PreflightPolicy
	if e := f.a.transaction(false, false, func(s *state) error { p = s.PreflightPolicies["project"]; return nil }); e != nil {
		t.Fatal(e)
	}
	p.Revision = "replacement-roles"
	spec := p.Tasks["task-a"]
	spec.ImplementationRole = "replacement-implementation"
	p.Tasks["task-a"] = spec
	observation := ad.observations["task-a"]
	observation.Implementation.Name = spec.ImplementationRole
	ad.observations["task-a"] = observation
	if e := f.a.ConfigurePreflight(f.owner, p); e != nil {
		t.Fatal(e)
	}
	quote, e := f.a.QuoteBudget(f.token, f.s.ID, "project", "task-a")
	if e != nil || quote.Allowed || quote.Reason != "owner_estimate_model_binding_changed" {
		t.Fatalf("same model must not bypass exact owner role estimate: %+v %v", quote, e)
	}
}
func TestNativeBudgetConcurrentWaveDoesNotDuplicateAllocation(t *testing.T) {
	f, _, _ := budgetSetup(t, 130)
	st, e := f.a.ReconcileCheckpoint(f.token, f.s.ID, checkpointRequest("precycle", "inv-one", 0))
	if e != nil {
		t.Fatal(e)
	}
	plan, e := f.a.PlanSwarm(f.token, f.s.ID, "project")
	if e != nil {
		t.Fatal(e)
	}
	req := ReserveWaveRequest{OperationKey: "wave", Plan: plan, ExpiresAt: time.Now().Add(time.Minute), CheckpointID: st.ID, CheckpointSequence: st.Sequence, InvocationID: st.InvocationID, ReconciliationDigest: st.ConditionDigest}
	type outcome struct {
		wave SwarmWave
		err  error
	}
	results := make(chan outcome, 2)
	for i := 0; i < 2; i++ {
		go func() { wave, e := f.a.ReserveWave(f.token, f.s.ID, req); results <- outcome{wave, e} }()
	}
	first, second := <-results, <-results
	if first.err != nil || second.err != nil || !reflect.DeepEqual(first.wave, second.wave) || len(first.wave.Dispatches) != 2 {
		t.Fatalf("concurrent original operation must preserve two disjoint dispatches once: %+v %+v", first, second)
	}
	status, e := f.a.BudgetStatus(f.token, f.s.ID, "project")
	if e != nil || len(status.Allocations) != 2 || status.Cycle.Reserved != 110 || status.ReviewReserved != 30 {
		t.Fatalf("concurrent replay duplicated/free'd estimated reservations: %+v %v", status, e)
	}
	req.OperationKey = "new-key"
	if _, e = f.a.ReserveWave(f.token, f.s.ID, req); !errors.Is(e, ErrDenied) {
		t.Fatalf("new key cannot consume original wave gate twice: %v", e)
	}
}
func TestNativeBudgetKnownZeroStopsBeforeRoleRouting(t *testing.T) {
	f, ad, _ := budgetSetup(t, 60)
	_ = budgetReserve(t, f, "wave-one")
	called := false
	ad.hook = func() { called = true }
	plan, e := f.a.PlanSwarm(f.token, f.s.ID, "project")
	if e != nil {
		t.Fatal(e)
	}
	for _, v := range plan.Verdicts {
		if v.Selected {
			t.Fatal("known-zero headroom selected another worker")
		}
	}
	if called {
		t.Fatal("known-zero admission reached role/model preflight routing")
	}
}

func TestNativeBudgetPublicProducerBindingRejectsSecondDispatchDecoy(t *testing.T) {
	f, ad, _ := budgetSetup(t, 130)
	wave := budgetReserve(t, f, "wave-one")
	if len(wave.Dispatches) != 2 {
		t.Fatal("fixture requires two disjoint dispatches")
	}
	producers := map[string]string{}
	for _, d := range wave.Dispatches {
		bridge, bt, intent := budgetHost(t, f, ad, SwarmWave{Dispatches: []DispatchReservation{d}})
		if _, e := f.a.ObserveLifecycle(bt, bridge.ID, lifecycleObserve(intent, "terminal-"+d.ID, "completed", 1)); e != nil {
			t.Fatal(e)
		}
	}
	public, e := f.a.BudgetStatus(f.token, f.s.ID, "project")
	if e != nil {
		t.Fatal(e)
	}
	for _, allocation := range public.Allocations {
		for _, candidate := range allocation.AvailableProducers {
			if candidate.Phase == "implementation" {
				producers[allocation.DispatchID] = candidate.ID
			}
		}
	}
	first, decoy := wave.Dispatches[0], wave.Dispatches[1]
	if producers[first.ID] == "" || producers[decoy.ID] == "" {
		t.Fatal("public projection omitted exact producers")
	}
	req := BudgetSettleRequest{OperationKey: "decoy", DispatchID: first.ID, Phase: "implementation", ProducerID: producers[decoy.ID], ExpectedFence: first.Reservation.Fence}
	if _, e = f.a.SettleBudget(f.token, f.s.ID, req); !errors.Is(e, ErrDenied) {
		t.Fatalf("second dispatch producer cannot settle first allocation: %v", e)
	}
	unchanged, e := f.a.BudgetStatus(f.token, f.s.ID, "project")
	if e != nil || unchanged.LedgerDigest != public.LedgerDigest {
		t.Fatal("decoy refusal changed accounting", e)
	}
	req.OperationKey = "correct-first"
	req.ProducerID = producers[first.ID]
	if _, e = f.a.SettleBudget(f.token, f.s.ID, req); e != nil {
		t.Fatal(e)
	}
	after, e := f.a.BudgetStatus(f.token, f.s.ID, "project")
	if e != nil || after.Window.Spent != 20 || after.Window.Reserved != 90 {
		t.Fatalf("public first settlement changed decoy liability: %+v %v", after, e)
	}
}

func TestNativeBudgetPendingVerificationCrashRetainsOriginalOperation(t *testing.T) {
	f, ad, _ := budgetSetup(t, 70)
	wave := budgetReserve(t, f, "wave-one")
	_, _, intent := budgetHost(t, f, ad, wave)
	worker := ad.workers[intent.Reservation.Reservation.TaskID]
	wt := ad.tokens[intent.Reservation.Reservation.TaskID]
	revision, e := f.a.PublishAttemptRevision(wt, worker.ID, PublishRevisionRequest{OperationKey: "pending-revision", AttemptID: intent.Lease.AttemptID, LeaseID: intent.Lease.ID, ExpectedFence: intent.Lease.Fence})
	if e != nil {
		t.Fatal(e)
	}
	runner, rt, e := f.a.Enroll(f.owner, Scope{Actions: []string{"verification.run"}, Resources: []string{"task:" + intent.Reservation.Reservation.TaskID}}, time.Now().Add(time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	check := CheckDescriptor{ID: "pending-check", Command: "fixture command not started after crash", Deadline: time.Second}
	grant, e := f.a.GrantVerification(f.owner, runner.ID, intent.Reservation.Reservation.TaskID, intent.Lease.Repository, check, time.Now().Add(time.Minute))
	if e != nil {
		t.Fatal(e)
	}
	req := VerificationRequest{OperationKey: "pending-execution", WorkerSession: worker.ID, AttemptID: intent.Lease.AttemptID, LeaseID: intent.Lease.ID, RevisionID: revision.ID, ExpectedFence: intent.Lease.Fence, CheckID: check.ID, GrantID: grant.ID}
	if _, receipt, e := f.a.BeginVerification(wt, rt, runner.ID, req, CheckDigest(check)); e != nil || receipt != nil {
		t.Fatal("pending producer begin", e)
	}
	a, e := Open(f.a.path, f.a.binding, ad)
	if e != nil {
		t.Fatal(e)
	}
	f.a = a
	if _, _, e = a.BeginVerification(wt, rt, runner.ID, req, CheckDigest(check)); !errors.Is(e, ErrReconcile) {
		t.Fatalf("unknown actual verification cannot reexecute after crash: %v", e)
	}
	public, e := a.BudgetStatus(f.token, f.s.ID, "project")
	if e != nil {
		t.Fatal(e)
	}
	id := ""
	for _, alloc := range public.Allocations {
		if alloc.DispatchID == intent.DispatchID {
			for _, producer := range alloc.AvailableProducers {
				if producer.Phase == "verification" && producer.Knowledge == "pending" {
					id = producer.ID
				}
			}
		}
	}
	if id == "" {
		t.Fatal("public pending command producer unavailable")
	}
	allocation, e := a.SettleBudget(f.token, f.s.ID, BudgetSettleRequest{OperationKey: "settle-pending", DispatchID: intent.DispatchID, Phase: "verification", ProducerID: id, ExpectedFence: intent.Reservation.Reservation.Fence})
	if e != nil {
		t.Fatal(e)
	}
	pending := false
	for _, phase := range allocation.Phases {
		if phase.Phase == "verification" {
			pending = phase.State == "unknown-retained" && phase.ProducerID == req.OperationKey
		}
	}
	if !pending {
		t.Fatalf("pending/timed-out command uncertainty became successful/free: %+v", allocation)
	}
	after, e := a.BudgetStatus(f.token, f.s.ID, "project")
	if e != nil || after.Window.Reserved != 55 || after.Window.Spent != 0 {
		t.Fatalf("pending execution freed implementation/downstream reserve: %+v %v", after, e)
	}
}

func TestNativeBudgetCooldownWithoutSTOPReopenAndLiveAdmission(t *testing.T) {
	f, ad, p := budgetSetup(t, 130)
	p.Revision = "cooldown-only"
	p.CooldownUntil = time.Now().Add(time.Minute)
	if e := f.a.ConfigureBudget(f.owner, p); e != nil {
		t.Fatal(e)
	}
	a, e := Open(f.a.path, f.a.binding, ad)
	if e != nil {
		t.Fatal(e)
	}
	f.a = a
	status, e := a.BudgetStatus(f.token, f.s.ID, "project")
	if e != nil || status.STOP || !status.CooldownUntil.Equal(p.CooldownUntil) || status.Cycle.Remaining != 130 {
		t.Fatalf("cooldown-only persisted state differs: %+v %v", status, e)
	}
	quote, e := a.QuoteBudget(f.token, f.s.ID, "project", "task-a")
	if e != nil || quote.Allowed || quote.Reason != "budget_cooldown" {
		t.Fatalf("isolated cooldown must block quote with ample headroom and STOP=false: %+v %v", quote, e)
	}
	plan, e := a.PlanSwarm(f.token, f.s.ID, "project")
	if e != nil {
		t.Fatal(e)
	}
	for _, v := range plan.Verdicts {
		if v.Selected {
			t.Fatal("isolated cooldown admitted live worker")
		}
	}
}
func TestNativeBudgetCheaperCapableAlternativePinnedAndNullableEstimate(t *testing.T) {
	f, ad, p := budgetSetup(t, 130)
	var preflight PreflightPolicy
	if e := f.a.transaction(false, false, func(s *state) error { preflight = s.PreflightPolicies["project"]; return nil }); e != nil {
		t.Fatal(e)
	}
	preflight.Revision = "with-cheaper-fallbacks"
	spec := preflight.Tasks["task-a"]
	spec.FallbackRoles = []string{"cheaper", "unestimated"}
	preflight.Tasks["task-a"] = spec
	observation := ad.observations["task-a"]
	observation.Implementation.Profile.CostTier = 2
	observation.Implementation.Profile.ContextLimit = 256
	observation.Implementation.Profile.CapabilityTags = []string{"go", "code"}
	for _, name := range spec.FallbackRoles {
		observation.Fallback = append(observation.Fallback, team.Role{Name: name, Kind: "implementer", Runtime: "codex", Model: "cheap-model", Grant: "rw", Scope: []string{"src/**"}, MaxPoints: 13, Profile: team.ModelProfile{CostTier: 1, ContextLimit: 256, CapabilityTags: []string{"go", "code"}}})
	}
	ad.observations["task-a"] = observation
	if e := f.a.ConfigurePreflight(f.owner, preflight); e != nil {
		t.Fatal(e)
	}
	p.Revision = "with-alternative-estimate"
	task := p.Tasks["task-a"]
	task.Alternatives = map[string]BudgetEstimate{"cheaper": {RoleID: "cheaper", Model: "cheap-model", Tokens: 5, Provenance: "owner-estimate", ObservedAt: time.Now()}}
	p.Tasks["task-a"] = task
	if e := f.a.ConfigureBudget(f.owner, p); e != nil {
		t.Fatal(e)
	}
	quote, e := f.a.QuoteBudget(f.token, f.s.ID, "project", "task-a")
	if e != nil || !quote.Allowed || quote.Harness != "codex" {
		t.Fatalf("pinned canonical alternative quote: %+v %v", quote, e)
	}
	known, unknown, chosen := false, false, false
	for _, alternative := range quote.Alternatives {
		switch alternative.RoleID {
		case "codex-implement":
			chosen = alternative.Selected && alternative.Model == "model" && alternative.CostTier == 2 && alternative.MarginalEstimate != nil && *alternative.MarginalEstimate == 55
		case "cheaper":
			known = alternative.Capable && !alternative.Selected && alternative.CostTier == 1 && alternative.MarginalEstimate != nil && *alternative.MarginalEstimate == 40 && alternative.EstimateKnowledge == "owner-estimate" && strings.Contains(alternative.Reason, "cheaper cost tier rejected") && strings.Contains(alternative.CapabilityKnowledge, "native model availability unobserved")
		case "unestimated":
			unknown = alternative.Capable && !alternative.Selected && alternative.MarginalEstimate == nil && alternative.EstimateKnowledge == "unknown"
		}
	}
	if !known || !unknown || !chosen {
		t.Fatalf("cheaper-capable rejection/full marginal/null provenance or owner model pin missing: %+v", quote.Alternatives)
	}
}
func TestNativeBudgetExpiredHostFreshnessRetainsWindowLiability(t *testing.T) {
	f, ad, p := budgetSetup(t, 70)
	wave := budgetReserve(t, f, "wave-one")
	bridge, bt, intent := budgetHost(t, f, ad, wave)
	request := lifecycleObserve(intent, "expired-host-running", "running", 1)
	request.SourceTime = time.Now().Add(-2 * time.Minute)
	if _, e := f.a.ObserveLifecycle(bt, bridge.ID, request); e != nil {
		t.Fatal(e)
	}
	expired, e := f.a.LifecycleStatus(bt, bridge.ID, intent.DispatchID)
	if e != nil || expired.State != "unknown" || expired.Freshness != "stale" || !expired.ResourcesRetained {
		t.Fatalf("actual expired freshness must remain unknown: %+v %v", expired, e)
	}
	status, e := f.a.BudgetStatus(f.token, f.s.ID, "project")
	if e != nil {
		t.Fatal(e)
	}
	var allocation BudgetAllocation
	id := ""
	for _, record := range status.Allocations {
		if record.DispatchID == intent.DispatchID {
			allocation = record
			for _, producer := range record.AvailableProducers {
				if producer.Phase == "implementation" {
					id = producer.ID
				}
			}
		}
	}
	if id == "" {
		t.Fatal("public expired observation missing")
	}
	if _, e = f.a.SettleBudget(f.token, f.s.ID, BudgetSettleRequest{OperationKey: "expired-settle", DispatchID: allocation.DispatchID, Phase: "implementation", ProducerID: id, ExpectedFence: allocation.ReservationFence}); e != nil {
		t.Fatal(e)
	}
	if e = f.a.transaction(false, false, func(s *state) error {
		later, e := budgetStatus(s, p, time.Now().Add(2*p.Window))
		if e != nil {
			return e
		}
		if later.Window.Reserved != 55 || later.Window.Spent != 0 || later.WindowReset != nil {
			t.Fatalf("expired host/elapsed window freed unknown liability: %+v", later)
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
}

func TestNativeBudgetTimestampCanonicalReplayAndNewCycle(t *testing.T) {
	f, ad, p := budgetSetup(t, 70)
	// Go inputs carry a monotonic clock; JSON offsets carry a different Location.
	now := time.Now()
	p.Revision = "time-canonical"
	p.WindowOrigin = now.Add(-time.Minute)
	p.ExpiresAt = now.Add(time.Hour)
	p.CooldownUntil = now.Add(-time.Second)
	for id, task := range p.Tasks {
		for phase, estimate := range task.Phases {
			estimate.ObservedAt = now
			task.Phases[phase] = estimate
		}
		p.Tasks[id] = task
	}
	// Preserve the already configured accounting origin, which is immutable.
	var stored BudgetPolicy
	if e := f.a.transaction(false, false, func(s *state) error { stored = s.BudgetPolicies[p.ProjectID]; return nil }); e != nil {
		t.Fatal(e)
	}
	p.WindowOrigin = stored.WindowOrigin.In(time.FixedZone("offset", 3600))
	if e := f.a.ConfigureBudget(f.owner, p); e != nil {
		t.Fatal(e)
	}
	if e := f.a.ConfigureBudget(f.owner, p); e != nil {
		t.Fatalf("monotonic Go policy replay must preserve immutable instant history: %v", e)
	}
	raw, e := json.Marshal(p)
	if e != nil {
		t.Fatal(e)
	}
	var offset BudgetPolicy
	if e = json.Unmarshal(raw, &offset); e != nil {
		t.Fatal(e)
	}
	offset.WindowOrigin = offset.WindowOrigin.In(time.FixedZone("zero-offset", 0))
	offset.ExpiresAt = offset.ExpiresAt.In(time.FixedZone("offset", -7200))
	offset.CooldownUntil = offset.CooldownUntil.In(time.FixedZone("offset", 3600))
	for id, task := range offset.Tasks {
		for phase, estimate := range task.Phases {
			estimate.ObservedAt = estimate.ObservedAt.In(time.FixedZone("offset", 3600))
			task.Phases[phase] = estimate
		}
		offset.Tasks[id] = task
	}
	before, e := os.ReadFile(f.a.path)
	if e != nil {
		t.Fatal(e)
	}
	a, e := Open(f.a.path, f.a.binding, ad)
	if e != nil {
		t.Fatal(e)
	}
	if e = a.ConfigureBudget(f.owner, offset); e != nil {
		t.Fatalf("offset policy replay after restart must preserve immutable instant history: %v", e)
	}
	after, e := os.ReadFile(f.a.path)
	if e != nil {
		t.Fatal(e)
	}
	if string(before) != string(after) {
		t.Fatal("canonical replay changed authority history")
	}
	offset.Revision = "time-next"
	offset.CycleID = "cycle-next"
	offset.CycleLimit = 1000
	if e = a.ConfigureBudget(f.owner, offset); e != nil {
		t.Fatalf("same instant origin must admit legitimate new owner cycle: %v", e)
	}
	changed := canonicalBudgetPolicy(offset)
	changed.WindowOrigin = changed.WindowOrigin.Add(time.Second)
	changed.Revision = "changed-origin"
	if e = a.ConfigureBudget(f.owner, changed); !errors.Is(e, ErrDenied) {
		t.Fatalf("genuine origin reset must remain refused: %v", e)
	}
	changed = canonicalBudgetPolicy(offset)
	changed.Window += time.Second
	changed.Revision = "changed-window"
	if e = a.ConfigureBudget(f.owner, changed); !errors.Is(e, ErrDenied) {
		t.Fatalf("genuine window reset must remain refused: %v", e)
	}
	changed = canonicalBudgetPolicy(offset)
	changed.CycleLimit++
	if e = a.ConfigureBudget(f.owner, changed); !errors.Is(e, ErrDenied) {
		t.Fatalf("immutable policy revision mutation must remain refused: %v", e)
	}
	// Alternative observations use the same canonical copy path, without aliasing maps.
	input := canonicalBudgetPolicy(p)
	task := input.Tasks["task-a"]
	task.Alternatives = map[string]BudgetEstimate{"cheap": {ObservedAt: now}}
	input.Tasks["task-a"] = task
	normalized := canonicalBudgetPolicy(input)
	if normalized.Tasks["task-a"].Alternatives["cheap"].ObservedAt != now.Round(0).UTC() || input.Tasks["task-a"].Alternatives["cheap"].ObservedAt != now {
		t.Fatal("alternative timestamp canonicalization must strip location/monotonic without caller mutation")
	}
}
