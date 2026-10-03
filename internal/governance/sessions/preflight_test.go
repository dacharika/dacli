package sessions

import (
	"encoding/json"
	"errors"
	"github.com/mlnomadpy/dacli/internal/team"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

type preflightFixtureAdmission struct {
	*swarmFixtureAdmission
	observations map[string]PreflightObservation
	hook         func()
	dispatchHook func()
}

func (p *preflightFixtureAdmission) ObservePreflight(w, project, task string, spec PreflightTask, placement SwarmPlacement) (PreflightObservation, error) {
	if p.hook != nil {
		p.hook()
	}
	return p.observations[task], nil
}
func (p *preflightFixtureAdmission) AdmitDispatchCapability(w, project, h, d string) error {
	if p.dispatchHook != nil {
		p.dispatchHook()
	}
	return p.AdmitSwarmCapability(w, project, h, d)
}
func preflightSetup(t *testing.T) (swarmFixture, *preflightFixtureAdmission, PreflightPolicy, map[string]Session, map[string]string) {
	t.Helper()
	f := swarmSetup(t)
	for _, upgrade := range []func(string) error{f.a.UpgradeDispatchIntents, f.a.UpgradeLifecycle, f.a.UpgradeRecovery, f.a.UpgradePreflight} {
		if e := upgrade(f.owner); e != nil {
			t.Fatal(e)
		}
	}
	ad := &preflightFixtureAdmission{swarmFixtureAdmission: f.ad, observations: map[string]PreflightObservation{}}
	f.a.admission = ad
	p := PreflightPolicy{SchemaVersion: 1, ProjectID: "project", Revision: "preflight-v1", Harness: "codex", Rationale: "owner accepts explicit cooperative models and phase contracts", AcceptManualAssurance: true, ExpiresAt: time.Now().Add(30 * time.Minute), TransientRetries: 1, Tasks: map[string]PreflightTask{}}
	workers := map[string]Session{}
	tokens := map[string]string{}
	for _, id := range []string{"task-a", "task-b", "task-c"} {
		worker, token, e := f.a.Enroll(f.owner, Scope{Actions: []string{"attempt.register", "attempt.publish", "lease.acquire", "lease.renew", "lease.release", "verification.request"}, Resources: []string{"task:" + id, "path:src/a", "path:other", "checkout:" + f.p.Placements[id].Checkout}}, time.Now().Add(time.Hour))
		if e != nil {
			t.Fatal(e)
		}
		workers[id] = worker
		tokens[id] = token
		reviewer, rt, e := f.a.Enroll(f.owner, Scope{Actions: []string{"read", "review.propose", "review.evaluate"}, Resources: []string{"task:" + id}}, time.Now().Add(time.Hour))
		if e != nil {
			t.Fatal(e)
		}
		o := PreflightObservation{AuthorityPath: f.a.path, InstallationID: f.a.binding.Installation, TaskDigest: strings.Repeat("a", 64), RosterDigest: strings.Repeat("b", 64), ClaimsDigest: strings.Repeat("c", 64), ProfileDigest: strings.Repeat("d", 64), Harness: "codex", Required: 8, Sized: true, Implementation: team.Role{Name: "codex-implement", Kind: "implement", Runtime: "codex", Model: "model", Grant: "rw", MaxPoints: 13}, Review: team.Role{Name: "codex-review", Kind: "review", Runtime: "codex", Model: "model", Grant: "ro", MaxPoints: 13}, Recovery: team.Role{Name: "codex-implement", Kind: "implement", Runtime: "codex", Model: "model", MaxPoints: 13}, Worker: PreflightActor{SessionID: worker.ID, CredentialHash: hash(token)}, Reviewer: PreflightActor{SessionID: reviewer.ID, CredentialHash: hash(rt)}, ReviewObservation: ReviewObservation{ChecklistDigest: "checklist", PolicyRevision: "review-policy", Checks: []RequiredCheck{{ID: "unit", CommandDigest: strings.Repeat("e", 64)}}}, ExpiresAt: p.ExpiresAt, Phases: []PreflightPhase{}}
		bindingRepo := ""
		if e = f.a.transaction(false, true, func(s *state) error {
			bindingRepo = s.Tasks[id].Repository
			g := ReviewGrant{ID: "review-" + id, ReviewerSession: reviewer.ID, WorkerRoot: worker.ID, TaskID: id, Repository: bindingRepo, Observation: o.ReviewObservation, ExpiresAt: p.ExpiresAt}
			s.ReviewGrants[g.ID] = g
			return nil
		}); e != nil {
			t.Fatal(e)
		}
		runner, runnerToken, e := f.a.Enroll(f.owner, Scope{Actions: []string{"verification.run"}, Resources: []string{"task:" + id}}, time.Now().Add(time.Hour))
		if e != nil {
			t.Fatal(e)
		}
		o.Runner = PreflightActor{SessionID: runner.ID, CredentialHash: hash(runnerToken)}
		o.RunnerRepository = bindingRepo
		o.Checks = o.ReviewObservation.Checks
		o.VerificationGrantIDs = []string{"check-" + id}
		if e = f.a.transaction(false, true, func(s *state) error {
			s.Grants["check-"+id] = VerificationGrant{ID: "check-" + id, RunnerSession: runner.ID, TaskID: id, Repository: bindingRepo, CheckID: o.Checks[0].ID, CommandDigest: o.Checks[0].CommandDigest, ExpiresAt: p.ExpiresAt}
			return nil
		}); e != nil {
			t.Fatal(e)
		}
		o.Reviewer.GrantID = "review-" + id
		o.ReviewerRepository = bindingRepo
		for _, phase := range []string{"implementation", "review", "verification", "integration", "recovery", "fallback", "tokens", "budget"} {
			o.Phases = append(o.Phases, PreflightPhase{Phase: phase, Verdict: "ready", Assurance: "owner-manual", Required: phase != "integration" && phase != "tokens" && phase != "budget"})
		}
		if id == "task-c" {
			o.ClaimResources = []string{"path:other"}
		} else {
			o.ClaimResources = []string{"path:src/a"}
		}
		ad.observations[id] = o
		p.Tasks[id] = PreflightTask{ImplementationRole: "codex-implement", ReviewRole: "codex-review", RecoveryRole: "codex-implement", RequireReview: true, RequireVerification: true, RunnerProfile: "/private/runner", WorkerConnection: "/private/worker", ReviewerProfile: "/private/reviewer", ContractProfile: "/private/contract"}
	}
	if e := f.a.ConfigurePreflight(f.owner, p); e != nil {
		t.Fatal(e)
	}
	return f, ad, p, workers, tokens
}
func TestNativePreflightExplicitCapacityMatchesTeamAndOwnerOverride(t *testing.T) {
	f, ad, p, _, _ := preflightSetup(t)
	o := ad.observations["task-a"]
	o.Implementation.MaxPoints = 3
	ad.observations["task-a"] = o
	expected := team.TaskCapacity(o.Implementation, o.Required, o.Sized)
	plan := swarmPlan(t, f)
	if len(plan.Verdicts) != 3 || plan.Verdicts[0].Selected || plan.Verdicts[0].Preflight == nil || !reflect.DeepEqual(plan.Verdicts[0].Preflight.Capacity, expected) {
		t.Fatalf("explicit undersized native role bypassed shared team capacity: %+v", plan)
	}
	if _, e := f.a.OverrideCapacity(f.token, CapacityOverrideRequest{ID: "override", ProjectID: "project", TaskID: "task-a", Reason: "reviewed risk", ExpiresAt: p.ExpiresAt}); !errors.Is(e, ErrDenied) {
		t.Fatalf("controller issued owner override: %v", e)
	}
	req := CapacityOverrideRequest{ID: "override", ProjectID: "project", TaskID: "task-a", Reason: "owner accepts task-size risk", ExpiresAt: p.ExpiresAt.Add(-time.Minute)}
	override, e := f.a.OverrideCapacity(f.owner, req)
	if e != nil || override.Delta != -5 || override.Actor != "owner:"+f.a.binding.Installation {
		t.Fatalf("exact owner override: %+v %v", override, e)
	}
	plan = swarmPlan(t, f)
	if !plan.Verdicts[0].Selected || plan.Verdicts[0].Preflight.OverrideID != override.ID {
		t.Fatalf("exact size override did not admit: %+v", plan)
	}
	wave := swarmReserve(t, f, "after-override", plan)
	if len(wave.Dispatches) != 2 || wave.Dispatches[0].PreflightDigest == "" {
		t.Fatalf("complete preflight reserve: %+v", wave)
	}
	// Changing canonical size invalidates exact old override and already-reserved admission.
	o.Required = 9
	ad.observations["task-a"] = o
	if _, e = f.a.AdmitDispatch(f.token, f.s.ID, AdmitDispatchRequest{DispatchID: wave.Dispatches[0].ID, ExpectedFence: wave.Dispatches[0].Reservation.Fence}); !errors.Is(e, ErrDenied) {
		t.Fatalf("stale estimate/override live dispatch authorized: %v", e)
	}
}
func TestNativePreflightRequiredReviewerBeforeImplementation(t *testing.T) {
	for _, mutation := range []string{"missing", "same-root", "wrong-credential", "revoked", "expired", "grant-policy"} {
		t.Run(mutation, func(t *testing.T) {
			f, ad, _, workers, _ := preflightSetup(t)
			o := ad.observations["task-a"]
			switch mutation {
			case "missing":
				o.Reviewer = PreflightActor{}
			case "same-root":
				o.Reviewer = o.Worker
			case "wrong-credential":
				o.Reviewer.CredentialHash = strings.Repeat("0", 64)
			case "grant-policy":
				o.ReviewObservation.PolicyRevision = "changed"
			case "revoked", "expired":
				if e := f.a.transaction(false, true, func(s *state) error {
					r := s.Sessions[o.Reviewer.SessionID]
					r.Revoked = mutation == "revoked"
					if mutation == "expired" {
						r.ExpiresAt = time.Now().Add(-time.Second)
					}
					s.Sessions[r.ID] = r
					return nil
				}); e != nil {
					t.Fatal(e)
				}
			}
			ad.observations["task-a"] = o
			plan := swarmPlan(t, f)
			if len(plan.Verdicts) != 3 || plan.Verdicts[0].Selected || plan.Verdicts[0].Preflight == nil || plan.Verdicts[0].Preflight.FailureClass != "permanent" {
				t.Fatalf("required reviewer deferred until implementation: %+v worker%s", plan, workers["task-a"].ID)
			}
		})
	}
}
func TestNativePreflightStablePreviewLiveFreshnessAndPinnedWorker(t *testing.T) {
	f, ad, p, workers, tokens := preflightSetup(t)
	plan := swarmPlan(t, f)
	again := swarmPlan(t, f)
	if plan.ID != again.ID || plan.Verdicts[0].Preflight.ObservedAt.Equal(again.Verdicts[0].Preflight.ObservedAt) {
		t.Fatalf("audit timing contaminated deterministic plan identity: %+v %+v", plan, again)
	}
	wave := swarmReserve(t, f, "wave", plan)
	d := wave.Dispatches[0]
	if _, e := f.a.AdmitDispatch(f.token, f.s.ID, AdmitDispatchRequest{DispatchID: d.ID, ExpectedFence: d.Reservation.Fence}); e != nil {
		t.Fatal(e)
	}
	worker := workers["task-a"]
	at, e := f.a.RegisterAttempt(tokens["task-a"], worker.ID, AttemptRequest{RequestID: "preflight-attempt", TaskID: "task-a", Checkout: f.dirs[0]})
	if e != nil {
		t.Fatal(e)
	}
	o := ad.observations["task-a"]
	originalReviewerHash := o.Reviewer.CredentialHash
	o.Reviewer.CredentialHash = "withdrawn"
	ad.observations["task-a"] = o
	if _, e = f.a.HandoffDispatch(tokens["task-a"], worker.ID, HandoffDispatchRequest{OperationKey: "handoff", DispatchID: d.ID, AttemptID: at.ID, ExpectedFence: d.Reservation.Fence}); !errors.Is(e, ErrDenied) {
		t.Fatalf("fresh reviewer withdrawal ignored at handoff: %v", e)
	}
	o.Reviewer.CredentialHash = originalReviewerHash
	ad.observations["task-a"] = o
	// Wrong authority identity and unsupported phase remain permanent before work.
	o.AuthorityPath = "/wrong/authority"
	ad.observations["task-a"] = o
	check, e := f.a.CheckPreflight(f.token, f.s.ID, "project", "task-a", "check")
	if e != nil || check.Report.Allowed || check.Report.FailureClass != "permanent" || check.Report.PolicyRevision != p.Revision {
		t.Fatalf("wrong private-profile authority accepted: %+v %v", check, e)
	}
}
func TestNativePreflightTransientBudgetAndHistoricalReplay(t *testing.T) {
	f, ad, _, _, _ := preflightSetup(t)
	o := ad.observations["task-a"]
	o.Phases = append(o.Phases, PreflightPhase{Phase: "integration", Verdict: "transient", Required: true, Assurance: "owner-manual"})
	ad.observations["task-a"] = o
	first, e := f.a.CheckPreflight(f.token, f.s.ID, "project", "task-a", "first")
	if e != nil || first.Report.FailureClass != "transient" || first.TransientAttempts != 1 {
		t.Fatalf("bounded transient classification: %+v %v", first, e)
	}
	replay, e := f.a.CheckPreflight(f.token, f.s.ID, "project", "task-a", "first")
	if e != nil || !reflect.DeepEqual(first, replay) {
		t.Fatalf("transient replay spent new budget: %+v %v", replay, e)
	}
	last, e := f.a.CheckPreflight(f.token, f.s.ID, "project", "task-a", "second")
	if e != nil || last.Report.FailureClass != "permanent" || last.Report.ProofDigest != preflightDigest(last.Report) {
		t.Fatalf("exhausted transient budget not durable/bound: %+v %v", last, e)
	}
	// Even repaired external state cannot bypass exhausted owner-policy budget.
	o.Phases = o.Phases[:len(o.Phases)-1]
	ad.observations["task-a"] = o
	plan := swarmPlan(t, f)
	if plan.Verdicts[0].Selected || plan.Verdicts[0].Preflight.FailureClass != "permanent" {
		t.Fatalf("planner bypassed exhausted retry budget: %+v", plan)
	}
}
func TestNativePreflightMigrationPreservesHistoryAndStrictShape(t *testing.T) {
	f, i := recoverySetup(t, false)
	before, e := os.ReadFile(f.a.path)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.a.UpgradePreflight(f.owner); e != nil {
		t.Fatal(e)
	}
	after, e := os.ReadFile(f.a.path)
	if e != nil {
		t.Fatal(e)
	}
	var old, current map[string]json.RawMessage
	if json.Unmarshal(before, &old) != nil || json.Unmarshal(after, &current) != nil {
		t.Fatal("decode")
	}
	for key, value := range old {
		if key != "Version" && key != "PreflightPolicies" && key != "PreflightPolicyHistory" && key != "CapacityOverrides" && key != "PreflightOperations" && key != "PreflightRetryBudgets" && !reflect.DeepEqual(value, current[key]) {
			t.Fatalf("schema10 history changed: %s", key)
		}
	}
	if e = f.a.AuthenticateOwner(f.owner); e != nil {
		t.Fatalf("current factory cannot reopen11: %v", e)
	}
	if e = f.a.transaction(false, false, func(s *state) error {
		if s.DispatchIntents[i.DispatchID].ID != i.ID {
			return ErrDenied
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	delete(current, "CapacityOverrides")
	bad, _ := json.Marshal(current)
	if e = os.WriteFile(f.a.path, bad, 0600); e != nil {
		t.Fatal(e)
	}
	if e = f.a.AuthenticateOwner(f.owner); !errors.Is(e, ErrStorage) {
		t.Fatalf("missing required11 map normalized: %v", e)
	}
}

func TestNativePreflightPinnedHarnessManualEnforcementAndFiniteCapacity(t *testing.T) {
	for _, mutation := range []string{"implementation-host", "review-host", "recovery-host", "fallback-host", "fallback-charter", "role-charter", "negative-limit", "nan-size", "manual-disallowed", "unsupported-required"} {
		t.Run(mutation, func(t *testing.T) {
			f, ad, p, _, _ := preflightSetup(t)
			o := ad.observations["task-a"]
			switch mutation {
			case "implementation-host":
				o.Implementation.Runtime = "antigravity"
			case "review-host":
				o.Review.Runtime = "other"
			case "recovery-host":
				o.Recovery.Runtime = "other"
			case "fallback-host":
				spec := p.Tasks["task-a"]
				spec.FallbackRoles = []string{"other"}
				p.Tasks["task-a"] = spec
				p.Revision = "fallback-policy"
				if e := f.a.ConfigurePreflight(f.owner, p); e != nil {
					t.Fatal(e)
				}
				o.Fallback = []team.Role{{Name: "other", Runtime: "other", Kind: "implement", Model: "model"}}
			case "fallback-charter":
				spec := p.Tasks["task-a"]
				spec.FallbackRoles = []string{"other"}
				p.Tasks["task-a"] = spec
				p.Revision = "fallback-charter-policy"
				if e := f.a.ConfigurePreflight(f.owner, p); e != nil {
					t.Fatal(e)
				}
				o.Fallback = []team.Role{{Name: "other", Kind: "reviewer", Runtime: "codex", Model: "model"}}
			case "role-charter":
				o.Implementation.Kind = "reviewer"
			case "negative-limit":
				o.Implementation.MaxPoints = -1
			case "nan-size":
				o.Required = math.NaN()
			case "manual-disallowed":
				p.Revision = "strict"
				p.AcceptManualAssurance = false
				if e := f.a.ConfigurePreflight(f.owner, p); e != nil {
					t.Fatal(e)
				}
			case "unsupported-required":
				o.Phases = append(o.Phases, PreflightPhase{Phase: "tokens", Required: true, Verdict: "unsupported", Assurance: "advisory"})
			}
			ad.observations["task-a"] = o
			plan := swarmPlan(t, f)
			if len(plan.Verdicts) != 3 || plan.Verdicts[0].Selected || plan.Verdicts[0].Preflight == nil || plan.Verdicts[0].Preflight.Allowed {
				t.Fatalf("complete-loop pinned role/control preflight bypass %s: %+v", mutation, plan)
			}
		})
	}
}
func TestNativePreflightExactRunnerGrantAndIndependentRoot(t *testing.T) {
	for _, mutation := range []string{"none", "same-worker", "wrong-grant", "wrong-repository", "revoked-grant"} {
		t.Run(mutation, func(t *testing.T) {
			f, ad, p, _, _ := preflightSetup(t)
			o := ad.observations["task-a"]
			runner, token, e := f.a.Enroll(f.owner, Scope{Actions: []string{"verification.run"}, Resources: []string{"task:task-a"}}, time.Now().Add(time.Hour))
			if e != nil {
				t.Fatal(e)
			}
			o.Runner = PreflightActor{SessionID: runner.ID, CredentialHash: hash(token)}
			o.RunnerRepository = o.ReviewerRepository
			o.Checks = o.ReviewObservation.Checks
			o.VerificationGrantIDs = []string{"exact-check-grant"}
			if e = f.a.transaction(false, true, func(s *state) error {
				s.Grants["exact-check-grant"] = VerificationGrant{ID: "exact-check-grant", RunnerSession: runner.ID, TaskID: "task-a", Repository: o.RunnerRepository, CheckID: o.Checks[0].ID, CommandDigest: o.Checks[0].CommandDigest, ExpiresAt: p.ExpiresAt}
				return nil
			}); e != nil {
				t.Fatal(e)
			}
			spec := p.Tasks["task-a"]
			spec.RequireVerification = true
			spec.RunnerProfile = "/private/runner"
			p.Tasks["task-a"] = spec
			p.Revision = "verify"
			if e = f.a.ConfigurePreflight(f.owner, p); e != nil {
				t.Fatal(e)
			}
			switch mutation {
			case "same-worker":
				o.Runner = o.Worker
				if e = f.a.transaction(false, true, func(s *state) error {
					r := s.Sessions[o.Worker.SessionID]
					r.Scope.Actions = append(r.Scope.Actions, "verification.run")
					s.Sessions[r.ID] = r
					g := s.Grants["exact-check-grant"]
					g.RunnerSession = r.ID
					s.Grants[g.ID] = g
					return nil
				}); e != nil {
					t.Fatal(e)
				}
			case "wrong-grant":
				o.VerificationGrantIDs[0] = "not-selected"
			case "wrong-repository":
				o.RunnerRepository = "other"
			case "revoked-grant":
				if e = f.a.transaction(false, true, func(s *state) error {
					g := s.Grants["exact-check-grant"]
					g.Revoked = true
					s.Grants[g.ID] = g
					return nil
				}); e != nil {
					t.Fatal(e)
				}
			}
			ad.observations["task-a"] = o
			plan := swarmPlan(t, f)
			if len(plan.Verdicts) != 3 || plan.Verdicts[0].Selected != (mutation == "none") {
				t.Fatalf("exact independent runner preflight %s: %+v", mutation, plan)
			}
		})
	}
}
func TestNativePreflightStorageFailureNoReceiptAndCurrentReplayAuth(t *testing.T) {
	f, _, _, _, _ := preflightSetup(t)
	f.a.persistOverride = func([]byte) error { return ErrStorage }
	report, e := f.a.CheckPreflight(f.token, f.s.ID, "project", "task-a", "failed-write")
	if !errors.Is(e, ErrStorage) || !reflect.DeepEqual(report, PreflightReceipt{}) {
		t.Fatalf("failed persistence leaked completed preflight: %+v %v", report, e)
	}
	f.a.persistOverride = nil
	report, e = f.a.CheckPreflight(f.token, f.s.ID, "project", "task-a", "first")
	if e != nil || !report.Report.Allowed {
		t.Fatalf("fresh preflight %+v %v", report, e)
	}
	if _, e = f.a.CheckPreflight(strings.Repeat("0", 64), f.s.ID, "project", "task-a", "first"); !errors.Is(e, ErrDenied) {
		t.Fatalf("forged historical report replay: %v", e)
	}
	if _, e = f.a.CheckPreflight(f.token, f.s.ID, "project", "task-b", "first"); !errors.Is(e, ErrDenied) {
		t.Fatalf("altered task historical report replay: %v", e)
	}
}
func TestNativePreflightOldDispatchAndMissingPolicyAllPhases(t *testing.T) {
	f, i := recoverySetup(t, false)
	if e := f.a.UpgradePreflight(f.owner); e != nil {
		t.Fatal(e)
	}
	if _, e := f.a.AdmitDispatch(f.token, f.s.ID, AdmitDispatchRequest{DispatchID: i.DispatchID, ExpectedFence: i.Reservation.Reservation.Fence}); !errors.Is(e, ErrDenied) {
		t.Fatalf("old dispatch grandfathered into schema11: %v", e)
	}
	report, e := f.a.CheckPreflight(f.token, f.s.ID, "project", "task-a", "missing-policy")
	if e != nil || report.Report.Allowed {
		t.Fatalf("missing owner policy %+v %v", report, e)
	}
	for _, phase := range []string{"implementation", "review", "verification", "integration", "recovery", "fallback", "tokens", "budget"} {
		found := false
		for _, p := range report.Report.Phases {
			if p.Phase == phase && p.Verdict == "not_evaluated" {
				found = true
			}
		}
		if !found {
			t.Fatalf("blocked report omitted complete phase %s: %+v", phase, report)
		}
	}
}
func TestNativePreflightReviewerCapacityRequiresSeparateExactOverride(t *testing.T) {
	f, ad, p, _, _ := preflightSetup(t)
	o := ad.observations["task-a"]
	o.Review.MaxPoints = 1
	ad.observations["task-a"] = o
	plan := swarmPlan(t, f)
	expected := team.TaskCapacity(o.Review, o.Required, o.Sized)
	if plan.Verdicts[0].Selected || len(plan.Verdicts[0].Preflight.RoleCapacities) != 3 || !reflect.DeepEqual(plan.Verdicts[0].Preflight.RoleCapacities[1].Capacity, expected) {
		t.Fatalf("required reviewer tasksize bypassed shared team assignment verdict: %+v", plan)
	}
	req := CapacityOverrideRequest{ID: "review-size", ProjectID: "project", TaskID: "task-a", RoleID: o.Review.Name, Reason: "owner accepts exact reviewer tasksize risk", ExpiresAt: p.ExpiresAt.Add(-time.Minute)}
	override, e := f.a.OverrideCapacity(f.owner, req)
	if e != nil || override.RoleID != o.Review.Name || override.Delta != -7 {
		t.Fatalf("exact readable reviewer override: %+v %v", override, e)
	}
	plan = swarmPlan(t, f)
	if !plan.Verdicts[0].Selected || plan.Verdicts[0].Preflight.RoleCapacities[1].OverrideID != override.ID || plan.Verdicts[0].Preflight.OverrideID != "" {
		t.Fatalf("review override widened implementation gate: %+v", plan)
	}
	o.Reviewer.CredentialHash = "revoked-profile"
	ad.observations["task-a"] = o
	if swarmPlan(t, f).Verdicts[0].Selected {
		t.Fatal("review size override waived independent reviewer authority")
	}
}
func TestNativePreflightMissingPhaseCannotAuthorizeAndOverrideExpiry(t *testing.T) {
	f, ad, p, _, _ := preflightSetup(t)
	o := ad.observations["task-a"]
	o.Phases = o.Phases[1:]
	ad.observations["task-a"] = o
	if swarmPlan(t, f).Verdicts[0].Selected {
		t.Fatal("missing required implementation phase added after Allowed calculation")
	}
	o = ad.observations["task-b"]
	o.Implementation.MaxPoints = 3
	ad.observations["task-b"] = o
	req := CapacityOverrideRequest{ID: "short", ProjectID: "project", TaskID: "task-b", Reason: "short test owner assurance", ExpiresAt: time.Now().Add(150 * time.Millisecond)}
	if _, e := f.a.OverrideCapacity(f.owner, req); e != nil {
		t.Fatal(e)
	}
	ad.hook = func() { time.Sleep(200 * time.Millisecond) }
	report, e := f.a.CheckPreflight(f.token, f.s.ID, "project", "task-b", "expired")
	if e != nil || report.Report.Allowed || report.Report.PolicyRevision != p.Revision {
		t.Fatalf("expired override refreshed during slow observations: %+v %v", report, e)
	}
}
func TestNativePreflightBeforeUpgradeRefusesWithoutAuthorityChanges(t *testing.T) {
	f := swarmSetup(t)
	before, e := os.ReadFile(f.a.path)
	if e != nil {
		t.Fatal(e)
	}
	report, e := f.a.CheckPreflight(f.token, f.s.ID, "project", "task-a", "not-upgraded")
	if !errors.Is(e, ErrDenied) || !reflect.DeepEqual(report, PreflightReceipt{}) {
		t.Fatalf("pre-upgrade check wrote schema11 fields: %+v %v", report, e)
	}
	after, e := os.ReadFile(f.a.path)
	if e != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("pre-upgrade check mutated authority", e)
	}
}
func TestNativePreflightFirstIntentRechecksAfterSlowHostCapability(t *testing.T) {
	f, ad, _, workers, _ := preflightSetup(t)
	wave := swarmReserve(t, f, "wave", swarmPlan(t, f))
	d := wave.Dispatches[0]
	resources := []string{"project:project"}
	for _, r := range d.Reservation.Resources {
		resources = append(resources, r.Kind+":"+r.Path)
	}
	bridge, _, e := f.a.Enroll(f.owner, Scope{Actions: []string{"read", "dispatch.correlate"}, Resources: resources}, time.Now().Add(time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	calls := 0
	ad.dispatchHook = func() {
		calls++
		if calls == 2 {
			o := ad.observations["task-a"]
			o.Reviewer.CredentialHash = "withdrawn-during-capability"
			ad.observations["task-a"] = o
		}
	}
	req := RequestDispatchIntent{OperationKey: "first-launch", DispatchID: d.ID, BridgeSessionID: bridge.ID, WorkerSessionID: workers["task-a"].ID, PromptSpecDigest: "sha256:" + strings.Repeat("a", 64), ExpectedFence: d.Reservation.Fence}
	result, e := f.a.RequestDispatchIntent(f.token, f.s.ID, req)
	if !errors.Is(e, ErrDenied) || result.HostActionAuthorized || calls != 2 {
		t.Fatalf("review profile withdrew after admission but first host launch authorized: %+v %v calls%d", result, e, calls)
	}
	if e = f.a.transaction(false, false, func(s *state) error {
		if _, ok := s.DispatchIntents[d.ID]; ok {
			return ErrDenied
		}
		return nil
	}); e != nil {
		t.Fatal("failed fresh preflight persisted host intent", e)
	}
}
func TestNativePreflightFinalObserverCannotOutliveController(t *testing.T) {
	f, ad, _, _, _ := preflightSetup(t)
	expires := time.Now().Add(2 * time.Second)
	if e := f.a.transaction(false, true, func(s *state) error { r := s.Sessions[f.s.ID]; r.ExpiresAt = expires; s.Sessions[r.ID] = r; return nil }); e != nil {
		t.Fatal(e)
	}
	calls := 0
	ad.hook = func() {
		calls++
		if calls == 3 {
			time.Sleep(time.Until(expires) + 50*time.Millisecond)
		}
	}
	plan, e := f.a.PlanSwarm(f.token, f.s.ID, "project")
	if calls < 3 {
		t.Fatalf("fixture never reached final slow preflight: calls%d %v", calls, e)
	}
	if !errors.Is(e, ErrDenied) || plan.ID != "" {
		t.Fatalf("controller expired during final preflight but plan authorized: id%s err%v calls%d", plan.ID, e, calls)
	}
}
func TestNativePreflightFinalMutationBoundaryExpiry(t *testing.T) {
	for _, phase := range []string{"reserve", "admit", "intent", "handoff"} {
		t.Run(phase, func(t *testing.T) {
			f, ad, _, workers, tokens := preflightSetup(t)
			plan := swarmPlan(t, f)
			var d DispatchReservation
			var at Attempt
			var bridge Session
			if phase != "reserve" {
				wave := swarmReserve(t, f, "wave", plan)
				d = wave.Dispatches[0]
			}
			if phase == "handoff" {
				var e error
				at, e = f.a.RegisterAttempt(tokens["task-a"], workers["task-a"].ID, AttemptRequest{RequestID: "final-attempt", TaskID: "task-a", Checkout: f.dirs[0]})
				if e != nil {
					t.Fatal(e)
				}
			}
			expires := time.Now().Add(2 * time.Second)
			if phase == "intent" {
				resources := []string{"project:project"}
				for _, r := range d.Reservation.Resources {
					resources = append(resources, r.Kind+":"+r.Path)
				}
				var e error
				bridge, _, e = f.a.Enroll(f.owner, Scope{Actions: []string{"read", "dispatch.correlate"}, Resources: resources}, expires)
				if e != nil {
					t.Fatal(e)
				}
			}
			if phase == "admit" || phase == "intent" {
				if e := f.a.transaction(false, true, func(s *state) error {
					updated := s.Dispatches[d.ID]
					updated.Reservation.ExpiresAt = expires
					s.Dispatches[d.ID] = updated
					return nil
				}); e != nil {
					t.Fatal(e)
				}
			}
			if phase == "reserve" || phase == "handoff" {
				if e := f.a.transaction(false, true, func(s *state) error { r := s.Sessions[f.s.ID]; r.ExpiresAt = expires; s.Sessions[r.ID] = r; return nil }); e != nil {
					t.Fatal(e)
				}
			}
			expected := map[string]int{"reserve": 5, "admit": 1, "intent": 2, "handoff": 1}[phase]
			calls := 0
			ad.hook = func() {
				calls++
				if calls == expected {
					time.Sleep(time.Until(expires) + 50*time.Millisecond)
				}
			}
			switch phase {
			case "reserve":
				wave, e := f.a.ReserveWave(f.token, f.s.ID, ReserveWaveRequest{OperationKey: "late-wave", Plan: plan, ExpiresAt: expires.Add(-50 * time.Millisecond)})
				if !errors.Is(e, ErrDenied) || wave.ID != "" {
					t.Fatalf("late reserve controller authorized after final observer: %+v %v", wave, e)
				}
			case "admit":
				result, e := f.a.AdmitDispatch(f.token, f.s.ID, AdmitDispatchRequest{DispatchID: d.ID, ExpectedFence: d.Reservation.Fence})
				if !errors.Is(e, ErrDenied) || result.ID != "" {
					t.Fatalf("late reservation admission authorized after final observer: %+v %v", result, e)
				}
			case "intent":
				result, e := f.a.RequestDispatchIntent(f.token, f.s.ID, RequestDispatchIntent{OperationKey: "late-intent", DispatchID: d.ID, BridgeSessionID: bridge.ID, WorkerSessionID: workers["task-a"].ID, PromptSpecDigest: "sha256:" + strings.Repeat("a", 64), ExpectedFence: d.Reservation.Fence})
				if !errors.Is(e, ErrDenied) || result.HostActionAuthorized {
					t.Fatalf("late bridge launch authorized after final observer: %+v %v", result, e)
				}
			case "handoff":
				lease, e := f.a.HandoffDispatch(tokens["task-a"], workers["task-a"].ID, HandoffDispatchRequest{OperationKey: "late-handoff", DispatchID: d.ID, AttemptID: at.ID, ExpectedFence: d.Reservation.Fence})
				if !errors.Is(e, ErrDenied) || lease.ID != "" {
					t.Fatalf("late controller lease handoff authorized after final observer: %+v %v", lease, e)
				}
			}
			if calls < expected {
				t.Fatalf("%s fixture did not reach finalobserver: calls%d expected%d", phase, calls, expected)
			}
		})
	}
}
func TestNativePreflightEarlierWaveOverrideExpiryDuringLaterObservation(t *testing.T) {
	for _, phase := range []string{"plan", "reserve"} {
		t.Run(phase, func(t *testing.T) {
			f, ad, _, _, _ := preflightSetup(t)
			o := ad.observations["task-a"]
			o.Implementation.MaxPoints = 3
			ad.observations["task-a"] = o
			expires := time.Now().Add(2 * time.Second)
			if _, e := f.a.OverrideCapacity(f.owner, CapacityOverrideRequest{ID: "wave-size", ProjectID: "project", TaskID: "task-a", Reason: "bounded owner size assurance", ExpiresAt: expires}); e != nil {
				t.Fatal(e)
			}
			var preview SwarmPlan
			if phase == "reserve" {
				preview = swarmPlan(t, f)
			}
			expected := map[string]int{"plan": 4, "reserve": 6}[phase]
			calls := 0
			ad.hook = func() {
				calls++
				if calls == expected {
					time.Sleep(time.Until(expires) + 50*time.Millisecond)
				}
			}
			if phase == "plan" {
				plan, e := f.a.PlanSwarm(f.token, f.s.ID, "project")
				if !errors.Is(e, ErrDenied) || plan.ID != "" {
					t.Fatalf("earlier wave override expired during later task read but plan authorized: %+v %v", plan, e)
				}
			} else {
				wave, e := f.a.ReserveWave(f.token, f.s.ID, ReserveWaveRequest{OperationKey: "late-size-wave", Plan: preview, ExpiresAt: time.Now().Add(5 * time.Minute)})
				if !errors.Is(e, ErrDenied) || wave.ID != "" {
					t.Fatalf("earlier wave override expired during later task read but reservation allocated: %+v %v", wave, e)
				}
			}
			if calls != expected {
				t.Fatalf("did not reach later task final observer: calls%d expected%d", calls, expected)
			}
			if e := f.a.transaction(false, false, func(s *state) error {
				if len(s.Dispatches) != 0 || s.Fence != 0 {
					return ErrDenied
				}
				return nil
			}); e != nil {
				t.Fatal("expired whole-wave preflight persisted protection", e)
			}
		})
	}
}
func TestNativePreflightScopedCheckCannotSpendAnotherTaskBudget(t *testing.T) {
	for _, scope := range []string{"project-only", "other-task"} {
		t.Run(scope, func(t *testing.T) {
			f, ad, _, _, _ := preflightSetup(t)
			o := ad.observations["task-a"]
			o.Phases = append(o.Phases, PreflightPhase{Phase: "integration", Required: true, Verdict: "transient", Assurance: "owner-manual"})
			ad.observations["task-a"] = o
			resources := []string{"project:project"}
			if scope == "other-task" {
				resources = append(resources, "task:task-b")
			}
			actor, token, e := f.a.Enroll(f.owner, Scope{Actions: []string{"swarm.plan"}, Resources: resources}, time.Now().Add(time.Hour))
			if e != nil {
				t.Fatal(e)
			}
			before, e := os.ReadFile(f.a.path)
			if e != nil {
				t.Fatal(e)
			}
			calls := 0
			ad.hook = func() { calls++ }
			result, e := f.a.CheckPreflight(token, actor.ID, "project", "task-a", "foreign-budget")
			if !errors.Is(e, ErrDenied) || !reflect.DeepEqual(result, PreflightReceipt{}) {
				t.Fatalf("ungranted task preflight spent shared retry authority: %+v %v", result, e)
			}
			after, e := os.ReadFile(f.a.path)
			if e != nil || !reflect.DeepEqual(before, after) || calls != 0 {
				t.Fatalf("foreign task refusal mutated budget/receipt or observed private profiles: calls%d %v", calls, e)
			}
		})
	}
}
func TestNativePreflightTypedTransientReachesEveryLiveAdapter(t *testing.T) {
	f, ad, _, workers, tokens := preflightSetup(t)
	plan := swarmPlan(t, f)
	wave := swarmReserve(t, f, "wave", plan)
	d := wave.Dispatches[0]
	at, e := f.a.RegisterAttempt(tokens["task-a"], workers["task-a"].ID, AttemptRequest{RequestID: "typed-attempt", TaskID: "task-a", Checkout: f.dirs[0]})
	if e != nil {
		t.Fatal(e)
	}
	resources := []string{"project:project"}
	for _, r := range d.Reservation.Resources {
		resources = append(resources, r.Kind+":"+r.Path)
	}
	bridge, _, e := f.a.Enroll(f.owner, Scope{Actions: []string{"read", "dispatch.correlate"}, Resources: resources}, time.Now().Add(time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	o := ad.observations["task-a"]
	o.Phases = append(o.Phases, PreflightPhase{Phase: "integration", Required: true, Verdict: "transient", Assurance: "owner-manual"})
	ad.observations["task-a"] = o
	before, e := os.ReadFile(f.a.path)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.a.AdmitDispatch(f.token, f.s.ID, AdmitDispatchRequest{DispatchID: d.ID, ExpectedFence: d.Reservation.Fence}); !errors.Is(e, ErrPreflightTransient) {
		t.Fatalf("live admission erased typed transient: %v", e)
	}
	if _, e = f.a.HandoffDispatch(tokens["task-a"], workers["task-a"].ID, HandoffDispatchRequest{OperationKey: "typed-handoff", DispatchID: d.ID, AttemptID: at.ID, ExpectedFence: d.Reservation.Fence}); !errors.Is(e, ErrPreflightTransient) {
		t.Fatalf("live handoff erased typed transient: %v", e)
	}
	if _, e = f.a.RequestDispatchIntent(f.token, f.s.ID, RequestDispatchIntent{OperationKey: "typed-intent", DispatchID: d.ID, BridgeSessionID: bridge.ID, WorkerSessionID: workers["task-a"].ID, PromptSpecDigest: "sha256:" + strings.Repeat("a", 64), ExpectedFence: d.Reservation.Fence}); !errors.Is(e, ErrPreflightTransient) {
		t.Fatalf("first intent erased typed transient: %v", e)
	}
	after, e := os.ReadFile(f.a.path)
	if e != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("blocked transient live adapters mutated retained authority", e)
	}
	// A fresh planned task that becomes transient also retains the typed refusal at reserve.
	f2, ad2, _, _, _ := preflightSetup(t)
	p2 := swarmPlan(t, f2)
	o2 := ad2.observations["task-a"]
	o2.Phases = append(o2.Phases, PreflightPhase{Phase: "integration", Required: true, Verdict: "transient", Assurance: "owner-manual"})
	ad2.observations["task-a"] = o2
	if _, e = f2.a.ReserveWave(f2.token, f2.s.ID, ReserveWaveRequest{OperationKey: "typed-wave", Plan: p2, ExpiresAt: time.Now().Add(5 * time.Minute)}); !errors.Is(e, ErrPreflightTransient) {
		t.Fatalf("wave reservation erased typed transient: %v", e)
	}
}

func TestNativePreflightMixedWavePermanentDominatesTransient(t *testing.T) {
	old := SwarmPlan{Verdicts: []SwarmVerdict{{TaskID: "task-a", Selected: true}, {TaskID: "task-b", Selected: true}}}
	transient := SwarmVerdict{TaskID: "task-a", Preflight: &PreflightReport{FailureClass: "transient"}}
	for _, permanent := range []SwarmVerdict{{TaskID: "task-b", Preflight: &PreflightReport{FailureClass: "permanent"}}, {TaskID: "task-b", Reason: "scope_denied"}} {
		for _, rows := range [][]SwarmVerdict{{transient, permanent}, {permanent, transient}} {
			if e := preflightPlanError(SwarmPlan{Verdicts: rows}, old); !errors.Is(e, ErrDenied) {
				t.Fatalf("atomic wave permanent refusal promoted to retryable transient: %v", e)
			}
		}
	}
	if e := preflightPlanError(SwarmPlan{Verdicts: []SwarmVerdict{transient, {TaskID: "task-b", Selected: true}}}, old); !errors.Is(e, ErrPreflightTransient) {
		t.Fatalf("sole transient wave refusal lost retryable class: %v", e)
	}
}
