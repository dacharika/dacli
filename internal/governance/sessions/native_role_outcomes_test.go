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

	"github.com/mlnomadpy/dacli/internal/governance/checkout"
	"github.com/mlnomadpy/dacli/internal/team"
)

type outcomePreflightAdmission struct {
	*swarmFixtureAdmission
	observations map[string]PreflightObservation
}

func (a *outcomePreflightAdmission) ObservePreflight(_, _, task string, _ PreflightTask, _ SwarmPlacement) (PreflightObservation, error) {
	return a.observations[task], nil
}

type outcomeDispatchAdmission struct {
	*dispatchAdmission
	preflight *outcomePreflightAdmission
}

func (a *outcomeDispatchAdmission) ObservePreflight(workspace, project, task string, spec PreflightTask, placement SwarmPlacement) (PreflightObservation, error) {
	return a.preflight.ObservePreflight(workspace, project, task, spec, placement)
}

func nativeRoleOutcomeFixture(t *testing.T) (*Authority, string, string) {
	t.Helper()
	f := swarmSetup(t)
	if err := f.a.UpgradeDispatchIntents(f.owner); err != nil {
		t.Fatal(err)
	}
	if err := f.a.UpgradeLifecycle(f.owner); err != nil {
		t.Fatal(err)
	}
	return f.a, f.owner, f.s.ID
}

func addNativeRoleOutcomeDispatch(t *testing.T, a *Authority, id, task, version string, policy []RolePolicyReference, stateName string) {
	t.Helper()
	provenance := RoleRegistryProvenance{RoleID: "builder", Version: version, ContentDigest: "sha256:" + strings.Repeat("a", 64), PolicyReferences: policy}
	reservation := Lease{ID: id, TaskID: task, Fence: 7, Repository: "fixture/repo", Checkout: "/checkout/" + task, Resources: []Resource{{Kind: "path", Path: "src"}}}
	dispatch := DispatchReservation{ID: id, ProjectID: "project", PolicyRevision: "swarm-policy", ImplementationRole: "builder", ReviewRole: "reviewer", Harness: "codex", Reservation: reservation, RoleRegistry: []RoleRegistryProvenance{provenance}}
	lease := Lease{ID: id, AttemptID: "attempt-" + task, SessionID: "worker-" + task, TaskID: task, Repository: reservation.Repository, Checkout: reservation.Checkout, Resources: reservation.Resources, Fence: 11}
	intent := DispatchIntent{Sequence: 2, ID: "intent-" + id, DispatchID: id, BridgeSessionID: "bridge", WorkerSessionID: lease.SessionID, State: "acknowledged", Reservation: dispatch, Lease: &lease}
	currentDispatch := dispatch
	currentDispatch.Converted, currentDispatch.WorkerSessionID, currentDispatch.AttemptID = true, lease.SessionID, lease.AttemptID
	if err := a.transaction(false, true, func(s *state) error {
		s.Dispatches[id] = currentDispatch
		s.DispatchIntents[id] = intent
		s.Leases[id] = lease
		s.Attempts[lease.AttemptID] = Attempt{ID: lease.AttemptID, SessionID: lease.SessionID, ProjectID: "project", Request: AttemptRequest{TaskID: task, Checkout: reservation.Checkout}, Observation: checkout.Observation{Locator: reservation.Checkout, Repository: reservation.Repository}}
		if stateName != "" {
			now := time.Now().UTC()
			observation := LifecycleObservation{Sequence: 1, HostSequence: 1, Binding: lifecycleBinding(intent), State: stateName, Assurance: "cooperative-owner-provisioned-bridge", EvidenceDigest: OutputDigest([]byte(id)), SourceTime: now.Add(-24 * time.Hour), ReceivedAt: now}
			s.LifecycleObservations[id] = []LifecycleObservation{observation}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func nativeRoleOutcomeSnapshot(dispatch DispatchReservation) DispatchReservation {
	dispatch.Converted, dispatch.WorkerSessionID, dispatch.AttemptID = false, "", ""
	return dispatch
}

func TestOwnerNativeRoleOutcomesCountsAcknowledgedDispatchesOnceAndKeepsExactGrouping(t *testing.T) {
	a, owner, _ := nativeRoleOutcomeFixture(t)
	refsA := []RolePolicyReference{{Kind: "native-swarm", Version: "v1", Digest: "sha256:" + strings.Repeat("b", 64)}}
	refsB := []RolePolicyReference{{Kind: "native-budget", Version: "v2", Digest: "sha256:" + strings.Repeat("c", 64)}}
	for i := range 5 {
		addNativeRoleOutcomeDispatch(t, a, "dispatch-a-"+string(rune('a'+i)), "task-a-"+string(rune('a'+i)), "1.0.0", refsA, "completed")
	}
	for i := range 5 {
		addNativeRoleOutcomeDispatch(t, a, "dispatch-b-"+string(rune('a'+i)), "task-b-"+string(rune('a'+i)), "1.0.0", refsB, "failed")
	}
	out, err := a.OwnerNativeRoleOutcomes(owner, "project")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("exact role/policy cohorts = %d, want 2: %+v", len(out), out)
	}
	if out[0].Version != "1.0.0" || out[0].AdoptionTaskSamples != 5 || out[0].DispatchSamples != 5 || out[0].OutcomeCounts == nil || out[0].OutcomeCounts.Completed != 5 || out[0].OutcomeCounts.Failed != 0 {
		t.Fatalf("first aggregate lost exact tuple or dispatch counts: %+v", out[0])
	}
	if out[1].Version != "1.0.0" || out[1].AdoptionTaskSamples != 5 || out[1].OutcomeCounts == nil || out[1].OutcomeCounts.Failed != 5 {
		t.Fatalf("second aggregate lost exact tuple or policy grouping: %+v", out[1])
	}
	if out[0].PolicySetDigest == out[1].PolicySetDigest {
		t.Fatal("different stable policy sets shared a digest")
	}
	encoded, err := json.Marshal(out[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"task-a-", "dispatch-a-", "intent-dispatch", "worker-task"} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("aggregate leaked private identifier %q: %s", private, encoded)
		}
	}
}

func TestOwnerNativeRoleOutcomesRefusesAmbiguousImplementationProvenance(t *testing.T) {
	a, owner, _ := nativeRoleOutcomeFixture(t)
	for i := range 5 {
		addNativeRoleOutcomeDispatch(t, a, "ambiguous-"+string(rune('a'+i)), "ambiguous-task-"+string(rune('a'+i)), "1.0.0", nil, "completed")
	}
	if err := a.transaction(false, true, func(s *state) error {
		d := s.Dispatches["ambiguous-a"]
		d.RoleRegistry = append(d.RoleRegistry, d.RoleRegistry[0])
		s.Dispatches[d.ID] = d
		i := s.DispatchIntents[d.ID]
		i.Reservation = nativeRoleOutcomeSnapshot(d)
		s.DispatchIntents[d.ID] = i
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	out, err := a.OwnerNativeRoleOutcomes(owner, "project")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Fatalf("ambiguous implementation provenance contributed to a four-task cohort: %+v", out)
	}
}

func TestOwnerNativeRoleOutcomesCreditsOnlyProvenanceForImplementationRole(t *testing.T) {
	a, owner, _ := nativeRoleOutcomeFixture(t)
	for i := range 5 {
		addNativeRoleOutcomeDispatch(t, a, "placed-"+string(rune('a'+i)), "placed-task-"+string(rune('a'+i)), "1.0.0", nil, "completed")
	}
	addNativeRoleOutcomeDispatch(t, a, "placed-pending", "placed-pending-task", "1.0.0", nil, "completed")
	if err := a.transaction(false, true, func(s *state) error {
		for id, d := range s.Dispatches {
			if !strings.HasPrefix(id, "placed-") {
				continue
			}
			provenance := RoleRegistryProvenance{RoleID: "reviewer", Version: "9.0.0", ContentDigest: "sha256:" + strings.Repeat("d", 64)}
			d.RoleRegistry = append(d.RoleRegistry, provenance)
			s.Dispatches[id] = d
			i := s.DispatchIntents[id]
			i.Reservation = nativeRoleOutcomeSnapshot(d)
			s.DispatchIntents[id] = i
		}
		pending := s.DispatchIntents["placed-pending"]
		pending.State = "pending"
		s.DispatchIntents["placed-pending"] = pending
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	out, err := a.OwnerNativeRoleOutcomes(owner, "project")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].RoleID != "builder" || out[0].AdoptionTaskSamples != 5 || out[0].DispatchSamples != 5 {
		t.Fatalf("reviewer or unrelated pin was attributed as executed role: %+v", out)
	}
}

func TestOwnerNativeRoleOutcomesSuppressesSmallCohortsAndMinorityOutcomeCells(t *testing.T) {
	a, owner, _ := nativeRoleOutcomeFixture(t)
	for i := range 4 {
		addNativeRoleOutcomeDispatch(t, a, "small-"+string(rune('a'+i)), "small-task-"+string(rune('a'+i)), "1.0.0", nil, "completed")
	}
	for i := range 5 {
		stateName := "completed"
		if i == 4 {
			stateName = "failed"
		}
		addNativeRoleOutcomeDispatch(t, a, "mixed-"+string(rune('a'+i)), "mixed-task-"+string(rune('a'+i)), "2.0.0", nil, stateName)
	}
	out, err := a.OwnerNativeRoleOutcomes(owner, "project")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Version != "2.0.0" {
		t.Fatalf("small cohort was exposed or mixed cohort omitted: %+v", out)
	}
	if out[0].OutcomeCounts != nil || out[0].Suppression == "" || out[0].AdoptionTaskSamples != 5 || out[0].DispatchSamples != 5 {
		t.Fatalf("minority cell did not suppress the entire vector: %+v", out[0])
	}
}

func TestOwnerNativeRoleOutcomesUnknownObservationsNeverBecomeSuccess(t *testing.T) {
	a, owner, _ := nativeRoleOutcomeFixture(t)
	for i := range 5 {
		stateName := ""
		if i == 0 {
			stateName = "cancelled"
		}
		if i == 1 {
			stateName = "running"
		}
		if i == 4 {
			stateName = "completed"
		}
		addNativeRoleOutcomeDispatch(t, a, "unknown-"+string(rune('a'+i)), "unknown-task-"+string(rune('a'+i)), "1.0.0", nil, stateName)
	}
	if err := a.transaction(false, true, func(s *state) error {
		id := "unknown-a"
		i := s.DispatchIntents[id]
		s.LifecycleCancellations[id] = LifecycleCancellation{Binding: lifecycleBinding(i), State: "acknowledged"}
		invalidTime := s.LifecycleObservations["unknown-e"]
		invalidTime[0].SourceTime = time.Now().Add(time.Hour)
		invalidTime[0].ReceivedAt = time.Now().Add(time.Hour)
		invalidTime[0].HostSequence = 0
		s.LifecycleObservations["unknown-e"] = invalidTime
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	out, err := a.OwnerNativeRoleOutcomes(owner, "project")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].OutcomeCounts == nil || out[0].OutcomeCounts.Completed != 0 || out[0].OutcomeCounts.Failed != 0 || out[0].OutcomeCounts.Unknown != 5 {
		t.Fatalf("pending, missing, running and cancelled observations became success: %+v", out)
	}
}

func TestOwnerNativeRoleOutcomesRetainsHistoricalRunsAfterWorkerRetirementAndPolicyChange(t *testing.T) {
	a, owner, sessionID := nativeRoleOutcomeFixture(t)
	for i := range 5 {
		addNativeRoleOutcomeDispatch(t, a, "historical-"+string(rune('a'+i)), "historical-task-"+string(rune('a'+i)), "1.0.0", nil, "completed")
	}
	if err := a.transaction(false, true, func(s *state) error {
		// Current policy drift and a revoked worker record do not rewrite
		// durable historical provenance.
		policy := s.SwarmPolicies["project"]
		policy.Revision = "new-policy"
		s.SwarmPolicies["project"] = policy
		for id, intent := range s.DispatchIntents {
			if !strings.HasPrefix(id, "historical-") {
				continue
			}
			intent.WorkerSessionID = sessionID
			intent.Lease.SessionID = sessionID
			s.DispatchIntents[id] = intent
			dispatch := s.Dispatches[id]
			dispatch.WorkerSessionID = sessionID
			s.Dispatches[id] = dispatch
			lease := s.Leases[id]
			lease.SessionID = sessionID
			s.Leases[id] = lease
			for requestID, attempt := range s.Attempts {
				if attempt.ID == intent.Lease.AttemptID {
					attempt.SessionID = sessionID
					s.Attempts[requestID] = attempt
				}
			}
			history := s.LifecycleObservations[id]
			history[0].Binding = lifecycleBinding(intent)
			s.LifecycleObservations[id] = history
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := a.Revoke(owner, sessionID); err != nil {
		t.Fatal(err)
	}
	out, err := a.OwnerNativeRoleOutcomes(owner, "project")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Version != "1.0.0" || out[0].OutcomeCounts == nil || out[0].OutcomeCounts.Completed != 5 {
		t.Fatalf("historical aggregate disappeared after current authority changed: %+v", out)
	}
}

func TestOwnerNativeRoleOutcomesRejectsWrongOwnerAndContradictoryBinding(t *testing.T) {
	a, owner, _ := nativeRoleOutcomeFixture(t)
	for i := range 5 {
		addNativeRoleOutcomeDispatch(t, a, "bound-"+string(rune('a'+i)), "bound-task-"+string(rune('a'+i)), "1.0.0", nil, "completed")
	}
	if _, err := a.OwnerNativeRoleOutcomes(strings.Repeat("x", 64), "project"); !errors.Is(err, ErrDenied) {
		t.Fatalf("wrong owner accepted: %v", err)
	}
	if err := a.transaction(false, true, func(s *state) error {
		i := s.DispatchIntents["bound-a"]
		history := s.LifecycleObservations["bound-a"]
		history[0].Binding.TaskID = "different-task"
		s.LifecycleObservations["bound-a"] = history
		s.DispatchIntents["bound-a"] = i
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	out, err := a.OwnerNativeRoleOutcomes(owner, "project")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].OutcomeCounts != nil || out[0].Suppression == "" || out[0].AdoptionTaskSamples != 5 {
		t.Fatalf("contradictory binding or pending intent was credited as success: %+v", out)
	}
}

func TestOwnerNativeRoleOutcomesCountsEachDispatchOnceAcrossLifecycleReplay(t *testing.T) {
	a, owner, _ := nativeRoleOutcomeFixture(t)
	for i := range 5 {
		addNativeRoleOutcomeDispatch(t, a, "replay-"+string(rune('a'+i)), "replay-task-"+string(rune('a'+i)), "1.0.0", nil, "completed")
	}
	if err := a.transaction(false, true, func(s *state) error {
		history := s.LifecycleObservations["replay-a"]
		history = append(history, history[0])
		s.LifecycleObservations["replay-a"] = history
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	out, err := a.OwnerNativeRoleOutcomes(owner, "project")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].DispatchSamples != 5 || out[0].AdoptionTaskSamples != 5 || out[0].OutcomeCounts != nil {
		t.Fatalf("replayed lifecycle record changed dispatch count or escaped complementary suppression: %+v", out)
	}
}

func TestOwnerNativeRoleOutcomesRefusesIntentReservationProvenanceDrift(t *testing.T) {
	a, owner, _ := nativeRoleOutcomeFixture(t)
	for i := range 5 {
		addNativeRoleOutcomeDispatch(t, a, "drift-"+string(rune('a'+i)), "drift-task-"+string(rune('a'+i)), "1.0.0", nil, "completed")
	}
	if err := a.transaction(false, true, func(s *state) error {
		intent := s.DispatchIntents["drift-a"]
		intent.Reservation.RoleRegistry[0].ContentDigest = "sha256:" + strings.Repeat("f", 64)
		s.DispatchIntents["drift-a"] = intent
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	out, err := a.OwnerNativeRoleOutcomes(owner, "project")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Fatalf("contradictory intent reservation contributed to a four-task group: %+v", out)
	}
}

func TestNativeRolePolicySetDigestIsOrderIndependentAndRejectsDuplicateKinds(t *testing.T) {
	a := []RolePolicyReference{{Kind: "native-swarm", Version: "v1", Digest: "sha256:" + strings.Repeat("a", 64)}, {Kind: "native-budget", Version: "v2", Digest: "sha256:" + strings.Repeat("b", 64)}}
	b := []RolePolicyReference{a[1], a[0]}
	first, ok := nativeRolePolicySetDigest(a)
	second, ok2 := nativeRolePolicySetDigest(b)
	if !ok || !ok2 || first != second {
		t.Fatalf("unordered policy set digest differs: %s / %s", first, second)
	}
	if _, ok := nativeRolePolicySetDigest(append(a, a[0])); ok {
		t.Fatal("duplicate policy kind accepted")
	}
}

func TestOwnerNativeRoleOutcomesActualAcknowledgedFiveTaskCohort(t *testing.T) {
	f := swarmSetup(t)
	const taskCount = 5
	tasks := make([]string, taskCount)
	resources := make([]string, taskCount)
	checkouts := make([]string, taskCount)
	candidates := make([]SwarmCandidate, taskCount)
	placements := make(map[string]SwarmPlacement, taskCount)
	repository, err := checkout.Observe(f.dirs[0])
	if err != nil {
		t.Fatal(err)
	}
	var baseAttempt Attempt
	if err := f.a.transaction(false, false, func(s *state) error {
		for _, attempt := range s.Attempts {
			baseAttempt = attempt
			break
		}
		return nil
	}); err != nil || baseAttempt.ID == "" {
		t.Fatalf("read base checkout attempt: %v", err)
	}
	for index := range taskCount {
		taskID := "outcome-task-" + string(rune('a'+index))
		resource := "src/outcome-" + string(rune('a'+index))
		checkoutPath := f.dirs[0]
		if index > 0 {
			checkoutPath = secondCheckout(t, baseAttempt)
		}
		observedCheckout, err := checkout.Observe(checkoutPath)
		if err != nil {
			t.Fatal(err)
		}
		checkoutPath = observedCheckout.Locator
		if err := os.MkdirAll(filepath.Join(checkoutPath, resource), 0700); err != nil {
			t.Fatal(err)
		}
		tasks[index], resources[index] = taskID, resource
		checkouts[index] = checkoutPath
		if err := f.a.BindTask(f.owner, TaskBinding{TaskID: taskID, ProjectID: "project", Repository: repository.Repository, AdmissionEnabled: true}); err != nil {
			t.Fatal(err)
		}
		candidates[index] = SwarmCandidate{TaskID: taskID, ProjectID: "project", Status: "open", Reason: "ready", Resources: []Resource{{Kind: "path", Path: resource}}}
		placements[taskID] = SwarmPlacement{Checkout: checkoutPath, ImplementKind: "implement", ReviewKind: "review"}
	}
	provenance := []RoleRegistryProvenance{
		{RoleID: "codex-implement", Version: "1.2.3", ContentDigest: "sha256:" + strings.Repeat("a", 64), LifecycleDigest: "sha256:" + strings.Repeat("b", 64), LifecycleExpiresAt: time.Now().UTC().Add(time.Hour)},
		{RoleID: "codex-review", Version: "2.3.4", ContentDigest: "sha256:" + strings.Repeat("c", 64), LifecycleDigest: "sha256:" + strings.Repeat("d", 64), LifecycleExpiresAt: time.Now().UTC().Add(time.Hour)},
	}
	// The test admission supplies fixture provenance through the observer before
	// planning. It exercises durable native provenance binding, not cryptographic
	// verification of a signed role release.
	f.ad.snapshot = SwarmSnapshot{Digest: "actual-role-outcomes-snapshot", Candidates: candidates, RoleRegistry: provenance}
	f.p.Revision, f.p.Width, f.p.WIP, f.p.Placements = "outcomes-five-task-wave", taskCount, taskCount, placements
	for index := range f.p.Roles {
		f.p.Roles[index].Capacity = taskCount
	}
	if err := f.a.ConfigureSwarm(f.owner, f.p); err != nil {
		t.Fatal(err)
	}
	for version := 7; version < 20; version++ {
		current, err := OpenCurrent(f.a.path, f.a.binding, f.ad)
		if err != nil {
			t.Fatalf("reopen authority before schema upgrade %d: %v", version, err)
		}
		var upgrade func(string) error
		switch version {
		case 7:
			upgrade = current.UpgradeDispatchIntents
		case 8:
			upgrade = current.UpgradeLifecycle
		case 9:
			upgrade = current.UpgradeRecovery
		case 10:
			upgrade = current.UpgradePreflight
		case 11:
			upgrade = current.UpgradeCheckpoints
		case 12:
			upgrade = current.UpgradeBudgets
		case 13:
			upgrade = current.UpgradeNativeAcceptance
		case 14:
			upgrade = current.UpgradeNativePolicy
		case 15:
			upgrade = current.UpgradeController
		case 16:
			upgrade = current.UpgradeNativeModelRouting
		case 17:
			upgrade = current.UpgradeNativeIntegration
		case 18:
			upgrade = current.UpgradeNativeExternal
		case 19:
			upgrade = current.UpgradeNativeRoleRegistry
		}
		if err := upgrade(f.owner); err != nil {
			t.Fatalf("upgrade disposable authority from schema %d: %v", version, err)
		}
	}
	f.a, err = OpenNativeRoleRegistry(f.a.path, f.a.binding, f.ad)
	if err != nil {
		t.Fatalf("open schema-20 authority: %v", err)
	}
	preflightAdmission := &outcomePreflightAdmission{swarmFixtureAdmission: f.ad, observations: make(map[string]PreflightObservation, taskCount)}
	f.a.admission = preflightAdmission
	if err := f.a.ConfigureLifecycle(f.owner, LifecyclePolicy{SchemaVersion: 1, ProjectID: "project", Revision: "outcomes-lifecycle", Source: "owner-manual-native-host", Freshness: time.Minute, CancellationEnabled: true, CancellationChannel: "owner-manual-native-host"}); err != nil {
		t.Fatal(err)
	}
	workers := make(map[string]Session, taskCount)
	workerTokens := make(map[string]string, taskCount)
	preflight := PreflightPolicy{SchemaVersion: 1, ProjectID: "project", Revision: "outcomes-preflight", Harness: "codex", Rationale: "fixture provides bounded preflight observations for the lifecycle outcome test", AcceptManualAssurance: true, ExpiresAt: time.Now().Add(30 * time.Minute), Tasks: make(map[string]PreflightTask, taskCount)}
	for index, taskID := range tasks {
		resource, checkoutPath := resources[index], checkouts[index]
		worker, token, err := f.a.Enroll(f.owner, Scope{Actions: []string{"attempt.register", "lease.acquire", "lease.renew", "lease.release"}, Resources: []string{"task:" + taskID, "path:" + resource, "checkout:" + checkoutPath}}, time.Now().Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		workers[taskID], workerTokens[taskID] = worker, token
		preflight.Tasks[taskID] = PreflightTask{ImplementationRole: "codex-implement", ReviewRole: "codex-review", RecoveryRole: "codex-implement", WorkerConnection: filepath.Join(t.TempDir(), "worker"), ContractProfile: filepath.Join(t.TempDir(), "contract")}
		role := func(name, kind string) team.Role {
			return team.Role{Name: name, Kind: kind, Runtime: "codex", Model: "fixture-model", Grant: "rw", MaxPoints: 13, Profile: team.ModelProfile{ID: "fixture-model", MaxTaskPoints: 13, ContextLimit: 1000}}
		}
		phases := []PreflightPhase{}
		for _, phase := range []string{"implementation", "review", "verification", "integration", "recovery", "fallback", "tokens", "budget"} {
			phases = append(phases, PreflightPhase{Phase: phase, Verdict: "ready", Assurance: "owner-manual", Required: true})
		}
		preflightAdmission.observations[taskID] = PreflightObservation{AuthorityPath: f.a.path, InstallationID: f.a.binding.Installation, TaskDigest: strings.Repeat("a", 64), RosterDigest: strings.Repeat("b", 64), ClaimsDigest: strings.Repeat("c", 64), ProfileDigest: strings.Repeat("d", 64), Harness: "codex", ClaimResources: []string{"path:" + resource}, Required: 1, Sized: true, Implementation: role("codex-implement", "implement"), Review: role("codex-review", "review"), Recovery: role("codex-implement", "implement"), Worker: PreflightActor{SessionID: worker.ID, CredentialHash: hash(token)}, RoleRegistry: provenance, ExpiresAt: preflight.ExpiresAt, Phases: phases}
	}
	if err := f.a.ConfigurePreflight(f.owner, preflight); err != nil {
		t.Fatalf("configure fixture preflight policy: %v", err)
	}
	controllerResources := []string{"project:project"}
	for index, taskID := range tasks {
		controllerResources = append(controllerResources, "task:"+taskID, "path:"+resources[index])
	}
	controllerSession, controllerToken, err := f.a.Enroll(f.owner, Scope{Actions: []string{"swarm.plan", "swarm.reserve", "swarm.dispatch"}, Resources: controllerResources}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	f.s, f.token = controllerSession, controllerToken
	plan, err := f.a.PlanSwarm(f.token, f.s.ID, "project")
	if err != nil {
		t.Fatal(err)
	}
	wave, err := f.a.ReserveWave(f.token, f.s.ID, ReserveWaveRequest{OperationKey: "outcomes-five-reserve", Plan: plan, ExpiresAt: time.Now().Add(10 * time.Minute)})
	if err != nil || len(wave.Dispatches) != taskCount {
		t.Fatalf("reserve five distinct task placements: %d %v verdicts=%+v", len(wave.Dispatches), err, plan.Verdicts)
	}
	dispatchAdmission := &outcomeDispatchAdmission{dispatchAdmission: &dispatchAdmission{swarmFixtureAdmission: f.ad}, preflight: preflightAdmission}
	f.a.admission = dispatchAdmission
	bridgeResources := append([]string(nil), controllerResources...)
	bridge, bridgeToken, err := f.a.Enroll(f.owner, Scope{Actions: []string{"read", "dispatch.correlate", "dispatch.observe"}, Resources: bridgeResources}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for index, dispatch := range wave.Dispatches {
		taskID := tasks[index]
		worker, workerToken := workers[taskID], workerTokens[taskID]
		attempt, err := f.a.RegisterAttempt(workerToken, worker.ID, AttemptRequest{RequestID: "outcomes-attempt-" + taskID, TaskID: taskID, Checkout: checkouts[index]})
		if err != nil {
			t.Fatal(err)
		}
		intentResult, err := f.a.RequestDispatchIntent(f.token, f.s.ID, RequestDispatchIntent{OperationKey: "outcomes-intent-" + taskID, DispatchID: dispatch.ID, BridgeSessionID: bridge.ID, WorkerSessionID: worker.ID, PromptSpecDigest: OutputDigest([]byte(taskID)), ExpectedFence: dispatch.Reservation.Fence})
		if err != nil || !intentResult.HostActionAuthorized {
			t.Fatalf("record durable dispatch intent for %s: %+v %v", taskID, intentResult, err)
		}
		intent, err := f.a.CorrelateDispatchIntent(bridgeToken, bridge.ID, CorrelateDispatchIntent{OperationKey: "outcomes-correlate-" + taskID, DispatchID: dispatch.ID, IntentID: intentResult.Intent.ID, HostAgentID: "/root/native_dispatch_worker", State: "host_correlated", ExpectedFence: dispatch.Reservation.Fence, ExpectedSequence: intentResult.Intent.Sequence})
		if err != nil {
			t.Fatalf("correlate dispatch for %s: %v", taskID, err)
		}
		intent, err = f.a.AcknowledgeDispatchIntent(workerToken, worker.ID, AcknowledgeDispatchIntent{OperationKey: "outcomes-ack-" + taskID, DispatchID: dispatch.ID, IntentID: intent.ID, AttemptID: attempt.ID, HostAgentID: intent.HostAgentID, ExpectedFence: dispatch.Reservation.Fence, ExpectedSequence: intent.Sequence})
		if err != nil {
			t.Fatalf("acknowledge dispatch for %s: %v", taskID, err)
		}
		if err := f.a.transaction(false, false, func(s *state) error {
			current := s.Dispatches[dispatch.ID]
			dispatchBound := nativeRoleOutcomeDispatchBound(current.ID, current, intent)
			leaseBound := nativeRoleOutcomeDurableLeaseBound(s, current.ID, current, intent)
			if !dispatchBound || !leaseBound {
				attempt := s.Attempts[intent.Lease.AttemptID]
				t.Fatalf("actual authenticated acknowledgement rejected: dispatch=%t lease=%t snapshot=%t worker=%q/%q attempt=%q/%q attemptRecord=%+v durable=%#v", dispatchBound, leaseBound, reflect.DeepEqual(intent.Reservation, nativeRoleOutcomeSnapshot(current)), current.WorkerSessionID, intent.WorkerSessionID, current.AttemptID, intent.Lease.AttemptID, attempt, s.Leases[current.ID])
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := f.a.ObserveLifecycle(bridgeToken, bridge.ID, ObserveLifecycleRequest{OperationKey: "outcomes-observe-" + taskID, Binding: lifecycleBinding(intent), ExpectedSequence: 0, HostSequence: 1, State: "completed", Source: "owner-manual-native-host", EvidenceDigest: OutputDigest([]byte("completed-" + taskID)), SourceTime: time.Now().UTC()}); err != nil {
			t.Fatalf("record lifecycle outcome for %s: %v", taskID, err)
		}
	}
	out, err := f.a.OwnerNativeRoleOutcomes(f.owner, "project")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].RoleID != "codex-implement" || out[0].Version != "1.2.3" || out[0].AdoptionTaskSamples != taskCount || out[0].DispatchSamples != taskCount || out[0].OutcomeCounts == nil || out[0].OutcomeCounts.Completed != taskCount {
		t.Fatalf("actual acknowledged five-task cohort was not reported: %+v", out)
	}
}
