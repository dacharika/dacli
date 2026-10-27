package sessions

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func controllerDriverPermit(t *testing.T) (controllerFixture, ControllerStatus, ControllerHostDispatchPermit) {
	t.Helper()
	f := controllerSetup(t)
	status := controllerStart(t, f)
	status = controllerStep(t, f, status, "driver-wave")
	if len(status.Actions) != 2 || status.Phase != "dispatch" {
		t.Fatalf("expected actual width-two reservation: %+v", status)
	}
	request := ControllerStepRequest{OperationKey: "driver-first", ProjectID: status.ProjectID, InvocationID: status.ID, ExpectedSequence: status.Sequence}
	status, permit, err := f.a.StepControllerForDriver(f.token, f.s.ID, request)
	if err != nil || len(status.Actions) == 0 || permit.Reference().DispatchID != status.Actions[0].DispatchID || permit.Reference().ControllerSequence != status.Sequence || status.Phase != "await_host" {
		t.Fatalf("fresh controller step did not return the exact newly persisted intent permit: status=%+v ref=%+v err=%v", status, permit.Reference(), err)
	}
	return f, status, permit
}

func controllerHostSlotSetup(t *testing.T) controllerFixture {
	t.Helper()
	f := controllerSetup(t)
	for _, upgrade := range []func(string) error{f.a.UpgradeNativeModelRouting, f.a.UpgradeNativeIntegration, f.a.UpgradeNativeExternal, f.a.UpgradeNativeRoleRegistry, f.a.UpgradeHostCapacity} {
		if err := upgrade(f.owner); err != nil {
			t.Fatal(err)
		}
	}
	host, _, err := f.a.Enroll(f.owner, Scope{Actions: []string{"host.observe"}, Resources: []string{"workspace:" + f.a.binding.Workspace}}, time.Now().Add(30*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	observer, observerToken, err := f.a.EnrollHostCapacityObserver(f.owner, HostCapacityObserver{SchemaVersion: 1, HostSessionID: host.ID, ExecutorInstanceID: "executor-controller-slots", HostID: "host-controller-slots", Runtime: "codex", Build: "build-controller-slots", CapabilityDigest: strings.Repeat("a", 64), ExpiresAt: time.Now().Add(20 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	capacityPolicy := HostCapacityPolicy{SchemaVersion: 1, WorkspaceID: f.a.binding.Workspace, InstallationID: f.a.binding.Installation, HostID: observer.HostID, Runtime: observer.Runtime, Build: observer.Build, HostSessionID: observer.HostSessionID, ExecutorInstanceID: observer.ExecutorInstanceID, CapabilityDigest: observer.CapabilityDigest, State: CapacityKnown, RequestedSlots: 3, Freshness: 10 * time.Minute, Revision: "controller-capacity-slots"}
	if _, err := f.a.ConfigureHostCapacityPolicy(f.owner, capacityPolicy); err != nil {
		t.Fatal(err)
	}
	if _, err := f.a.QualifyHostCapacity(f.owner, HostCapacityQualification{SchemaVersion: 1, HostSessionID: observer.HostSessionID, ExecutorInstanceID: observer.ExecutorInstanceID, HostID: observer.HostID, Runtime: observer.Runtime, Build: observer.Build, CapabilityDigest: observer.CapabilityDigest, State: CapacityKnown, EvidenceDigest: strings.Repeat("b", 64), QualificationID: "controller-qualified-slots", QualifiedSlots: 3, ExpiresAt: time.Now().Add(15 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err := f.a.UpgradeHostSlots(f.owner); err != nil {
		t.Fatal(err)
	}
	terminal := &HostSlotTerminalQualification{SchemaVersion: 1, ID: "controller-terminal-qualification", State: "supported", Source: "owner-manual-native-host", EvidenceDigest: strings.Repeat("d", 64), AuthorityDomainID: "controller-slot-domain", HostID: observer.HostID, HostSessionID: observer.HostSessionID, ExecutorInstanceID: observer.ExecutorInstanceID, Runtime: observer.Runtime, Build: observer.Build, CapabilityDigest: observer.CapabilityDigest, QualifiedAt: time.Now().UTC(), ExpiresAt: time.Now().Add(10 * time.Minute)}
	f.p.Revision = "controller-host-slot-policy"
	f.p.HostSlots = &HostSlotAdmissionPolicy{SchemaVersion: 1, Mode: HostSlotSingleAuthority, Revision: "controller-host-slot-rev", AuthorityDomainID: "controller-slot-domain", WorkspaceID: f.a.binding.Workspace, InstallationID: f.a.binding.Installation, HostID: observer.HostID, HostSessionID: observer.HostSessionID, ExecutorInstanceID: observer.ExecutorInstanceID, Runtime: observer.Runtime, Build: observer.Build, CapabilityDigest: observer.CapabilityDigest, CapacityPolicyRevision: capacityPolicy.Revision, QualificationID: "controller-qualified-slots", TerminalObservationSource: terminal.Source, TerminalQualificationID: terminal.ID, TerminalQualification: terminal}
	if err := f.a.ConfigureSwarm(f.owner, f.p); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.a.RecordHostCapacityObservationV2(observerToken, observer.ObserverSessionID, HostCapacityObservationRequestV2{HostCapacityObservationRequest: HostCapacityObservationRequest{SchemaVersion: 2, OperationKey: "controller-capacity-empty", HostID: observer.HostID, Runtime: observer.Runtime, Build: observer.Build, HostSessionID: observer.HostSessionID, ExecutorInstanceID: observer.ExecutorInstanceID, CapabilityDigest: observer.CapabilityDigest, Sequence: 1, DeclarationState: CapacityKnown, DeclaredSlots: 3, ObservedState: CapacityKnown, OccupiedSlots: 0, AvailableSlots: 3, ObservedAt: time.Now().Add(-time.Second).UTC()}, Census: HostCapacityCensus{SchemaVersion: 1, AuthorityDomainID: "controller-slot-domain", IncludedLocalExecutions: []HostSlotObservationBinding{}}}); err != nil {
		t.Fatal(err)
	}
	if err := f.a.transaction(false, false, func(*state) error { return nil }); err != nil {
		t.Fatalf("schema22 controller host-slot fixture invalid: %v", err)
	}
	return f
}

func TestControllerDispatchPermitMintAfterCommit(t *testing.T) {
	f := controllerSetup(t)
	status := controllerStart(t, f)
	status = controllerStep(t, f, status, "commit-wave")
	request := ControllerStepRequest{OperationKey: "commit-intent", ProjectID: status.ProjectID, InvocationID: status.ID, ExpectedSequence: status.Sequence}
	before, err := os.ReadFile(f.a.path)
	if err != nil {
		t.Fatal(err)
	}
	f.a.persistOverride = func([]byte) error { return ErrStorage }
	failedStatus, failedPermit, err := f.a.StepControllerForDriver(f.token, f.s.ID, request)
	f.a.persistOverride = nil
	after, readErr := os.ReadFile(f.a.path)
	if !errors.Is(err, ErrStorage) || failedStatus.ID != "" || failedPermit.Reference() != (ControllerHostDispatchReference{}) || readErr != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("failed persistence leaked a grant or state: status=%+v ref=%+v err=%v readErr=%v unchanged=%v", failedStatus, failedPermit.Reference(), err, readErr, reflect.DeepEqual(before, after))
	}
	committed, permit, err := f.a.StepControllerForDriver(f.token, f.s.ID, request)
	if err != nil || permit.Reference().IntentID == "" || permit.Reference().ControllerSequence != committed.Sequence {
		t.Fatalf("committed original step did not mint its permit: status=%+v ref=%+v err=%v", committed, permit.Reference(), err)
	}

	// A regular StepController still consumes the first-use transition without
	// creating a permit that a later replay can manufacture.
	g := controllerSetup(t)
	manual := controllerStart(t, g)
	manual = controllerStep(t, g, manual, "manual-wave")
	manualRequest := ControllerStepRequest{OperationKey: "manual-intent", ProjectID: manual.ProjectID, InvocationID: manual.ID, ExpectedSequence: manual.Sequence}
	manual, err = g.a.StepController(g.token, g.s.ID, manualRequest)
	if err != nil || !manual.HostActionAuthorized {
		t.Fatalf("manual path failed to create its established intent: status=%+v err=%v", manual, err)
	}
	replay, replayPermit, err := g.a.StepControllerForDriver(g.token, g.s.ID, manualRequest)
	if err != nil || !replay.Historical || replayPermit.Reference() != (ControllerHostDispatchReference{}) {
		t.Fatalf("historical status replay manufactured a handoff permit: status=%+v ref=%+v err=%v", replay, replayPermit.Reference(), err)
	}
}

func TestControllerDispatchPermitWidthTwoNewIntent(t *testing.T) {
	f := controllerSetup(t)
	status := controllerStart(t, f)
	status = controllerStep(t, f, status, "width-two-wave")
	firstRequest := ControllerStepRequest{OperationKey: "width-two-first", ProjectID: status.ProjectID, InvocationID: status.ID, ExpectedSequence: status.Sequence}
	firstStatus, firstPermit, err := f.a.StepControllerForDriver(f.token, f.s.ID, firstRequest)
	if err != nil || firstPermit.Reference().DispatchID != firstStatus.Actions[0].DispatchID || firstPermit.Reference().ActionID != firstStatus.Actions[0].ID {
		t.Fatalf("first action permit selected wrong sibling: %+v %v", firstPermit.Reference(), err)
	}
	first, err := f.a.ConsumeControllerHostDispatch(f.token, f.s.ID, firstPermit)
	if err != nil || first.DispatchID != firstPermit.Reference().DispatchID {
		t.Fatalf("first exact intent did not consume: intent=%+v err=%v", first, err)
	}
	secondRequest := ControllerStepRequest{OperationKey: "width-two-second", ProjectID: firstStatus.ProjectID, InvocationID: firstStatus.ID, ExpectedSequence: firstStatus.Sequence}
	secondStatus, secondPermit, err := f.a.StepControllerForDriver(f.token, f.s.ID, secondRequest)
	if err != nil || len(secondStatus.Actions) != 2 || secondPermit.Reference().DispatchID != secondStatus.Actions[1].DispatchID || secondPermit.Reference().ActionID != secondStatus.Actions[1].ID || secondPermit.Reference().DispatchID == first.DispatchID {
		t.Fatalf("width-two second step selected the earlier sibling or failed to mint: status=%+v ref=%+v err=%v", secondStatus, secondPermit.Reference(), err)
	}
	second, err := f.a.ConsumeControllerHostDispatch(f.token, f.s.ID, secondPermit)
	if err != nil || second.DispatchID != secondPermit.Reference().DispatchID || second.DispatchID == first.DispatchID {
		t.Fatalf("second exact intent did not consume: intent=%+v first=%+v err=%v", second, first, err)
	}
}

func TestControllerDispatchPermitCopiesAreOneUse(t *testing.T) {
	f, _, permit := controllerDriverPermit(t)
	const contenders = 12
	var wg sync.WaitGroup
	var mu sync.Mutex
	successes := 0
	for range contenders {
		wg.Add(1)
		copy := permit
		go func() {
			defer wg.Done()
			if _, err := f.a.ConsumeControllerHostDispatch(f.token, f.s.ID, copy); err == nil {
				mu.Lock()
				successes++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if successes != 1 {
		t.Fatalf("copied concurrent permit yielded %d successful consumptions, want exactly one", successes)
	}
	if _, err := f.a.ConsumeControllerHostDispatch(f.token, f.s.ID, permit); !errors.Is(err, ErrDenied) {
		t.Fatalf("spent permit was usable again: %v", err)
	}
}

func TestControllerDispatchPermitStaleStoppedAndSubstituted(t *testing.T) {
	t.Run("stop", func(t *testing.T) {
		f, _, permit := controllerDriverPermit(t)
		p := f.p
		p.Revision = "controller-stop-before-dispatch"
		p.Stopped = true
		if err := f.a.ConfigureSwarm(f.owner, p); err != nil {
			t.Fatal(err)
		}
		if _, err := f.a.ConsumeControllerHostDispatch(f.token, f.s.ID, permit); !errors.Is(err, ErrDenied) {
			t.Fatalf("STOP did not refuse first-use consumption: %v", err)
		}
		if _, err := f.a.ConsumeControllerHostDispatch(f.token, f.s.ID, permit); !errors.Is(err, ErrDenied) {
			t.Fatalf("failed STOP attempt did not burn the copied one-use permit: %v", err)
		}
	})
	t.Run("route-substitution", func(t *testing.T) {
		f, _, permit := controllerDriverPermit(t)
		ref := permit.Reference()
		if err := f.a.transaction(false, true, func(s *state) error {
			iv := s.ControllerInvocations[ref.InvocationID]
			route := iv.Routes[s.Dispatches[ref.DispatchID].Reservation.TaskID]
			route.PromptSpecDigest = "sha256:" + strings.Repeat("b", 64)
			iv.Routes[s.Dispatches[ref.DispatchID].Reservation.TaskID] = route
			s.ControllerInvocations[ref.InvocationID] = iv
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := f.a.ConsumeControllerHostDispatch(f.token, f.s.ID, permit); !errors.Is(err, ErrDenied) {
			t.Fatalf("changed controller route or prompt binding was accepted: %v", err)
		}
	})
	t.Run("checkpoint-substitution", func(t *testing.T) {
		f, _, permit := controllerDriverPermit(t)
		ref := permit.Reference()
		if err := f.a.transaction(false, true, func(s *state) error {
			iv := s.ControllerInvocations[ref.InvocationID]
			iv.Status.CheckpointID = "different-checkpoint"
			s.ControllerInvocations[ref.InvocationID] = iv
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := f.a.ConsumeControllerHostDispatch(f.token, f.s.ID, permit); !errors.Is(err, ErrDenied) {
			t.Fatalf("changed current checkpoint identity was accepted: %v", err)
		}
	})
	t.Run("stale-sequence", func(t *testing.T) {
		f, status, permit := controllerDriverPermit(t)
		request := ControllerStepRequest{OperationKey: "stale-followup", ProjectID: status.ProjectID, InvocationID: status.ID, ExpectedSequence: status.Sequence}
		if _, _, err := f.a.StepControllerForDriver(f.token, f.s.ID, request); err != nil {
			t.Fatal(err)
		}
		if _, err := f.a.ConsumeControllerHostDispatch(f.token, f.s.ID, permit); !errors.Is(err, ErrDenied) {
			t.Fatalf("old sequence permit remained usable after a newer controller step: %v", err)
		}
	})
}

func TestControllerDispatchPermitCannotSerialize(t *testing.T) {
	f, _, permit := controllerDriverPermit(t)
	encoded, err := json.Marshal(permit)
	if err == nil || len(encoded) != 0 || strings.Contains(permit.String(), permit.Reference().IntentID) || strings.Contains(permit.GoString(), permit.Reference().OriginalOperationKey) {
		t.Fatalf("opaque permit leaked serialized or string identity: json=%q string=%q go=%q err=%v", encoded, permit.String(), permit.GoString(), err)
	}
	var decoded ControllerHostDispatchPermit
	if err := json.Unmarshal([]byte(`{}`), &decoded); !errors.Is(err, ErrDenied) {
		t.Fatalf("JSON decoder created a permit: %v", err)
	}
	if _, err := f.a.ConsumeControllerHostDispatch(f.token, f.s.ID, decoded); !errors.Is(err, ErrDenied) {
		t.Fatalf("zero/decoded permit authorized consumption: %v", err)
	}
	if _, err := f.a.ConsumeControllerHostDispatch(f.token, f.s.ID, ControllerHostDispatchPermit{}); !errors.Is(err, ErrDenied) {
		t.Fatalf("zero permit authorized consumption: %v", err)
	}
}

func TestControllerDispatchPermitRevocationAndExpiry(t *testing.T) {
	t.Run("revocation", func(t *testing.T) {
		f, _, permit := controllerDriverPermit(t)
		if err := f.a.Revoke(f.owner, f.s.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := f.a.ConsumeControllerHostDispatch(f.token, f.s.ID, permit); !errors.Is(err, ErrDenied) {
			t.Fatalf("revoked controller consumed a permit: %v", err)
		}
	})
	t.Run("deadline", func(t *testing.T) {
		f, _, permit := controllerDriverPermit(t)
		if err := f.a.transaction(false, true, func(s *state) error {
			iv := s.ControllerInvocations[permit.Reference().InvocationID]
			iv.Status.Deadline = time.Now().Add(-time.Second)
			s.ControllerInvocations[iv.Status.ID] = iv
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := f.a.ConsumeControllerHostDispatch(f.token, f.s.ID, permit); !errors.Is(err, ErrDenied) {
			t.Fatalf("expired invocation consumed a permit: %v", err)
		}
	})
	t.Run("preflight-expiry", func(t *testing.T) {
		f, _, permit := controllerDriverPermit(t)
		if err := f.a.transaction(false, true, func(s *state) error {
			d := s.Dispatches[permit.Reference().DispatchID]
			d.PreflightExpiresAt = time.Now().Add(-time.Second)
			s.Dispatches[d.ID] = d
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := f.a.ConsumeControllerHostDispatch(f.token, f.s.ID, permit); !errors.Is(err, ErrDenied) {
			t.Fatalf("expired durable preflight did not refuse handoff: %v", err)
		}
	})
	t.Run("budget-revision", func(t *testing.T) {
		f, _, permit := controllerDriverPermit(t)
		var policy BudgetPolicy
		if err := f.a.transaction(false, false, func(s *state) error {
			policy = s.BudgetPolicies[permit.Reference().ProjectID]
			return nil
		}); err != nil || policy.Revision == "" {
			t.Fatalf("fixture lacks an active durable budget policy: policy=%+v err=%v", policy, err)
		}
		policy.Revision += "-changed-before-handoff"
		if err := f.a.ConfigureBudget(f.owner, policy); err != nil {
			t.Fatal(err)
		}
		if _, err := f.a.ConsumeControllerHostDispatch(f.token, f.s.ID, permit); !errors.Is(err, ErrDenied) {
			t.Fatalf("changed budget revision did not refuse handoff: %v", err)
		}
	})
}

type controllerDispatchRoleDriftAdmission struct {
	*checkpointFixtureAdmission
	observeCalls int
}

func (a *controllerDispatchRoleDriftAdmission) ObserveSwarm(workspace, project string) (SwarmSnapshot, error) {
	a.observeCalls++
	snapshot, err := a.swarmFixtureAdmission.ObserveSwarm(workspace, project)
	if err == nil && a.observeCalls == 2 {
		snapshot.RoleRegistry = append(snapshot.RoleRegistry, RoleRegistryProvenance{RoleID: "changed-after-admission"})
	}
	return snapshot, err
}

func TestControllerDispatchPermitPostObserverRoleDrift(t *testing.T) {
	f, _, permit := controllerDriverPermit(t)
	drift := &controllerDispatchRoleDriftAdmission{checkpointFixtureAdmission: f.observer}
	f.a.admission = drift
	if _, err := f.a.ConsumeControllerHostDispatch(f.token, f.s.ID, permit); !errors.Is(err, ErrDenied) {
		t.Fatalf("post-admission role-registry change did not refuse handoff: calls=%d err=%v", drift.observeCalls, err)
	}
	if drift.observeCalls != 2 {
		t.Fatalf("handoff did not perform its final role-registry observation after admission: got %d calls, want 2", drift.observeCalls)
	}
}

func TestControllerDispatchPermitCheckpointAdvanceBeforeMint(t *testing.T) {
	f := controllerSetup(t)
	status := controllerStart(t, f)
	status = controllerStep(t, f, status, "checkpoint-wave")
	request := ControllerStepRequest{OperationKey: "checkpoint-intent", ProjectID: status.ProjectID, InvocationID: status.ID, ExpectedSequence: status.Sequence}
	committed, created, err := f.a.advanceControllerForDriver(f.token, f.s.ID, request, false)
	if err != nil || created == nil || created.CheckpointSequence == 0 {
		t.Fatalf("actual durable step did not capture its checkpoint generation: status=%+v candidate=%+v err=%v", committed, created, err)
	}
	if err := f.a.transaction(false, true, func(s *state) error {
		history := s.Checkpoints[committed.CheckpointID]
		if len(history) == 0 || history[len(history)-1].Sequence != created.CheckpointSequence {
			return ErrDenied
		}
		advanced := history[len(history)-1]
		advanced.Sequence++
		advanced.ObservedAt = time.Now().UTC()
		s.Checkpoints[committed.CheckpointID] = append(history, advanced)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	permit, err := f.a.mintControllerHostDispatchPermit(f.token, f.s.ID, request, committed, created)
	if !errors.Is(err, ErrDenied) || permit.Reference() != (ControllerHostDispatchReference{}) {
		t.Fatalf("mint rebased the just-committed step onto a later checkpoint: ref=%+v err=%v", permit.Reference(), err)
	}
}

func TestControllerDispatchPermitSchema22HostSlotAdmission(t *testing.T) {
	t.Run("valid-claim-consumes", func(t *testing.T) {
		f := controllerHostSlotSetup(t)
		status := controllerStart(t, f)
		status = controllerStep(t, f, status, "slot-wave")
		request := ControllerStepRequest{OperationKey: "slot-intent", ProjectID: status.ProjectID, InvocationID: status.ID, ExpectedSequence: status.Sequence}
		status, permit, err := f.a.StepControllerForDriver(f.token, f.s.ID, request)
		if err != nil || status.Phase != "await_host" || permit.Reference().IntentID == "" {
			t.Fatalf("valid schema22 host slot did not produce a first-use permit: status=%+v ref=%+v err=%v", status, permit.Reference(), err)
		}
		intent, err := f.a.ConsumeControllerHostDispatch(f.token, f.s.ID, permit)
		if err != nil || intent.Reservation.HostSlot == nil || intent.Reservation.HostSlot.AuthorityDomainID != f.p.HostSlots.AuthorityDomainID || intent.Reservation.HostSlot.HostID != f.p.HostSlots.HostID || intent.Reservation.HostSlot.HostSessionID != f.p.HostSlots.HostSessionID || intent.Reservation.HostSlot.ExecutorInstanceID != f.p.HostSlots.ExecutorInstanceID {
			t.Fatalf("valid isolated22 host-slot admission was not returned exactly: claim=%+v err=%v", intent.Reservation.HostSlot, err)
		}
	})
	t.Run("changed-slot-claim-refuses", func(t *testing.T) {
		f := controllerHostSlotSetup(t)
		status := controllerStart(t, f)
		status = controllerStep(t, f, status, "slot-wave")
		request := ControllerStepRequest{OperationKey: "slot-intent", ProjectID: status.ProjectID, InvocationID: status.ID, ExpectedSequence: status.Sequence}
		_, permit, err := f.a.StepControllerForDriver(f.token, f.s.ID, request)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.a.transaction(false, true, func(s *state) error {
			d := s.Dispatches[permit.Reference().DispatchID]
			d.HostSlot.HostID = "substituted-host"
			s.Dispatches[d.ID] = d
			i := s.DispatchIntents[d.ID]
			i.Reservation.HostSlot.HostID = "substituted-host"
			s.DispatchIntents[d.ID] = i
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := f.a.ConsumeControllerHostDispatch(f.token, f.s.ID, permit); !errors.Is(err, ErrDenied) {
			t.Fatalf("changed schema22 host-slot claim was accepted: %v", err)
		}
	})
	t.Run("stale-current-census-refuses", func(t *testing.T) {
		f := controllerHostSlotSetup(t)
		status := controllerStart(t, f)
		status = controllerStep(t, f, status, "slot-wave")
		request := ControllerStepRequest{OperationKey: "slot-intent", ProjectID: status.ProjectID, InvocationID: status.ID, ExpectedSequence: status.Sequence}
		_, permit, err := f.a.StepControllerForDriver(f.token, f.s.ID, request)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.a.transaction(false, true, func(s *state) error {
			found := false
			for key, observation := range s.CapacityObservations {
				if observation.HostSessionID == f.p.HostSlots.HostSessionID && observation.ExecutorInstanceID == f.p.HostSlots.ExecutorInstanceID {
					observation.ReceivedAt = time.Now().Add(-2 * time.Hour)
					s.CapacityObservations[key] = observation
					found = true
				}
			}
			if !found {
				return ErrDenied
			}
			return nil
		}); err != nil {
			t.Fatalf("could not age existing valid capacity census: %v", err)
		}
		if _, err := f.a.ConsumeControllerHostDispatch(f.token, f.s.ID, permit); !errors.Is(err, ErrDenied) {
			t.Fatalf("stale current schema22 census did not refuse handoff: %v", err)
		}
	})
}
