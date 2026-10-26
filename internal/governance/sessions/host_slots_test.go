package sessions

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestHostSlotHeadroomUsesExactCensusOverlap(t *testing.T) {
	tests := []struct {
		name                                                            string
		occupied, included, declared, outstanding, requested, qualified int
		want                                                            int
	}{
		{name: "included local execution removed once", occupied: 2, included: 2, declared: 4, outstanding: 2, requested: 4, qualified: 4, want: 2},
		{name: "external occupancy retained", occupied: 2, included: 0, declared: 4, outstanding: 1, requested: 4, qualified: 4, want: 1},
		{name: "one unobserved local claim", occupied: 2, included: 2, declared: 4, outstanding: 1, requested: 4, qualified: 4, want: 3},
		{name: "second unobserved local claim reduces headroom by one", occupied: 2, included: 2, declared: 4, outstanding: 2, requested: 4, qualified: 4, want: 2},
		{name: "policy cap dominates", occupied: 0, included: 0, declared: 4, outstanding: 1, requested: 2, qualified: 4, want: 1},
		{name: "qualification cap dominates", occupied: 0, included: 0, declared: 4, outstanding: 1, requested: 4, qualified: 2, want: 1},
		{name: "invalid overlap is fail closed", occupied: 1, included: 2, declared: 4, outstanding: 0, requested: 4, qualified: 4, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hostSlotHeadroom(tt.occupied, tt.included, tt.declared, tt.outstanding, tt.requested, tt.qualified); got != tt.want {
				t.Fatalf("hostSlotHeadroom(O=%d,K=%d,D=%d,L=%d,P=%d,Q=%d) = %d, want %d", tt.occupied, tt.included, tt.declared, tt.outstanding, tt.requested, tt.qualified, got, tt.want)
			}
		})
	}
}

func TestHostSlotsSchema22MigrationPreservesCapacityAndOldExactReaderRefuses(t *testing.T) {
	a, owner, host, _, observer, observerToken := capacity21Fixture(t)
	if _, err := a.ConfigureHostCapacityPolicy(owner, HostCapacityPolicy{SchemaVersion: 1, WorkspaceID: a.binding.Workspace, InstallationID: a.binding.Installation, HostID: observer.HostID, Runtime: observer.Runtime, Build: observer.Build, HostSessionID: host.ID, ExecutorInstanceID: observer.ExecutorInstanceID, CapabilityDigest: observer.CapabilityDigest, State: CapacityKnown, RequestedSlots: 2, Freshness: time.Minute, Revision: "capacity-rev-1"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.RecordHostCapacityObservation(observerToken, observer.ObserverSessionID, capacityRequest(host, 1, "schema22-preserve-observation")); err != nil {
		t.Fatal(err)
	}
	old, err := OpenHostCapacity(a.path, a.binding)
	if err != nil {
		t.Fatal(err)
	}
	var before state
	if err := a.transaction(false, false, func(s *state) error { before = *s; return nil }); err != nil {
		t.Fatal(err)
	}
	before.Version = 22
	if err := a.UpgradeHostSlots("wrong-owner"); !errors.Is(err, ErrDenied) {
		t.Fatalf("bad owner migration error = %v, want ErrDenied", err)
	}
	if err := a.UpgradeHostSlots(owner); err != nil {
		t.Fatal(err)
	}
	var after state
	if err := a.transaction(false, false, func(s *state) error { after = *s; return nil }); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("21→22 migration changed schema-21 capacity facts or existing records")
	}
	if err := old.transaction(false, false, func(*state) error { return nil }); !errors.Is(err, ErrStorage) {
		t.Fatalf("already-open exact-21 reader accepted schema 22: %v", err)
	}
	if err := old.transaction(false, true, func(*state) error { return nil }); !errors.Is(err, ErrStorage) {
		t.Fatalf("already-open exact-21 writer accepted schema 22: %v", err)
	}
	if _, err := OpenHostCapacity(a.path, a.binding); !errors.Is(err, ErrStorage) {
		t.Fatalf("new exact-21 opener accepted schema 22: %v", err)
	}
	if _, err := OpenHostSlots(a.path, a.binding); err != nil {
		t.Fatalf("exact-22 opener rejected migrated schema: %v", err)
	}
	current, err := OpenCapacityCurrent(a.path, a.binding)
	if err != nil {
		t.Fatalf("current capacity opener rejected schema 22: %v", err)
	}
	if err := current.transaction(false, false, func(s *state) error {
		if s.Version != 22 {
			return ErrStorage
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestHostSlotsUpgradeRefusesInvalid21WithoutWriting(t *testing.T) {
	a, owner, _, _, _, _ := capacity21Fixture(t)
	if err := os.WriteFile(a.path, []byte(`{"Version":21,"unknown":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(a.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.UpgradeHostSlots(owner); !errors.Is(err, ErrStorage) {
		t.Fatalf("malformed schema-21 migration error = %v, want ErrStorage", err)
	}
	after, err := os.ReadFile(a.path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("refused schema migration modified the authority file")
	}
}

func TestHostSlotsRejectsUnknownSchema22Fields(t *testing.T) {
	a, owner, _, _, _, _ := capacity21Fixture(t)
	if err := a.UpgradeHostSlots(owner); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(a.path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	raw["UnexpectedHostSlotField"] = true
	data, err = json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenHostSlots(a.path, a.binding); !errors.Is(err, ErrStorage) {
		t.Fatalf("schema-22 reader accepted unknown field: %v", err)
	}
}

func TestPre22ReaderRejectsExplicitSchema22FieldsOnlyAtTypedPaths(t *testing.T) {
	fixtures := map[string]string{
		"current policy null pointer":             `{"SwarmPolicies":{"project":{"HostSlots":null}}}`,
		"dispatch null pointer":                   `{"Dispatches":{"dispatch":{"HostSlot":null}}}`,
		"capacity census null pointer":            `{"CapacityObservations":{"observation":{"Census":null}}}`,
		"lifecycle observation null pointer":      `{"LifecycleObservations":{"dispatch":[{"host_slot_terminal":null}]}}`,
		"lifecycle receipt nested null pointer":   `{"LifecycleOperations":{"receipt":{"Observation":{"host_slot_terminal":null}}}}`,
		"swarm receipt nested reservation":        `{"SwarmOperations":{"receipt":{"Wave":{"Dispatches":[{"hostslot":null}]}}}}`,
		"intent current nested reservation":       `{"DispatchIntents":{"dispatch":{"Reservation":{"hostslot":null}}}}`,
		"intent receipt nested reservation":       `{"DispatchIntentOperations":{"receipt":{"Intent":{"Reservation":{"HostSlot":null}}}}}`,
		"controller invocation zero integer":      `{"ControllerInvocations":{"invocation":{"Status":{"ExecutionConcurrency":0}}}}`,
		"controller history false boolean":        `{"ControllerHistory":{"invocation":[{"ExecutionConcurrencyKnown":false}]}}`,
		"controller receipt null pointer":         `{"ControllerOperations":{"receipt":{"Status":{"HostSlotAdmission":null}}}}`,
		"ambiguous root field casing":             `{"SwarmPolicies":{},"swarmPolicies":{"project":{"HostSlots":null}}}`,
		"ambiguous typed structural field casing": `{"SwarmOperations":{"receipt":{"Wave":{},"wave":{"Dispatches":[{"HostSlot":null}]}}}}`,
	}
	for name, fixture := range fixtures {
		t.Run(name, func(t *testing.T) {
			if !authorityHasSchema22Fields([]byte(fixture)) {
				t.Fatalf("typed schema22 field presence was not detected in %s", fixture)
			}
		})
	}
	if authorityHasSchema22Fields([]byte(`{"Context":{"HostSlot":null,"host_slots":null,"ExecutionConcurrency":0}}`)) {
		t.Fatal("arbitrary context payload triggered schema22 field presence detection")
	}
}

func TestPre22DecoderRejectsExplicitSchema22Presence(t *testing.T) {
	a, _, _, _, _, _ := capacity21Fixture(t)
	var current state
	if err := a.transaction(false, false, func(s *state) error { current = *s; return nil }); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(current)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatal(err)
	}
	root["SwarmPolicies"] = json.RawMessage(`{"project":{"HostSlots":null}}`)
	raw, err = json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeAuthorityStateMode(raw, a.binding, 21, true); !errors.Is(err, ErrStorage) {
		t.Fatalf("schema-21 decoder accepted explicit schema-22 null field: %v", err)
	}
}

func TestHostSlotCensusRequiresExactUniqueExecutionBindings(t *testing.T) {
	binding := HostSlotObservationBinding{DispatchID: "dispatch-1", ReservationFence: 1, IntentID: "intent-1", OriginalOperationKey: "original-op", HostAgentID: "host-worker"}
	if !validHostCapacityCensus(HostCapacityCensus{SchemaVersion: 1, AuthorityDomainID: "domain-1", IncludedLocalExecutions: []HostSlotObservationBinding{binding}}) {
		t.Fatal("valid exact local binding refused")
	}
	for name, entries := range map[string][]HostSlotObservationBinding{
		"duplicate dispatch":       {binding, binding},
		"duplicate host execution": {binding, {DispatchID: "dispatch-2", ReservationFence: 2, IntentID: "intent-2", OriginalOperationKey: "original-op-2", HostAgentID: binding.HostAgentID}},
		"missing host execution":   {{DispatchID: "dispatch-2", ReservationFence: 2, IntentID: "intent-2", OriginalOperationKey: "original-op-2"}},
	} {
		t.Run(name, func(t *testing.T) {
			if validHostCapacityCensus(HostCapacityCensus{SchemaVersion: 1, AuthorityDomainID: "domain-1", IncludedLocalExecutions: entries}) {
				t.Fatal("invalid census binding set accepted")
			}
		})
	}
}

func TestHostSlotCensusBindingRequiresOriginalIntentIdentity(t *testing.T) {
	qualification := HostSlotTerminalQualification{SchemaVersion: 1, ID: "terminal-qualification", State: "supported", Source: "bridge-terminal", EvidenceDigest: stringsOf('d', 64), AuthorityDomainID: "domain-1", HostID: "host-1", HostSessionID: "host-session", ExecutorInstanceID: "executor-1", Runtime: "codex", Build: "build-1", CapabilityDigest: stringsOf('a', 64), QualifiedAt: time.Now().Add(-time.Minute), ExpiresAt: time.Now().Add(time.Hour)}
	policy := HostSlotAdmissionPolicy{SchemaVersion: 1, Mode: HostSlotSingleAuthority, Revision: "slot-rev", WorkspaceID: "workspace", InstallationID: "installation", AuthorityDomainID: "domain-1", HostID: "host-1", HostSessionID: "host-session", ExecutorInstanceID: "executor-1", Runtime: "codex", Build: "build-1", CapabilityDigest: stringsOf('a', 64), CapacityPolicyRevision: "capacity-rev", QualificationID: "capacity-qual", TerminalObservationSource: "bridge-terminal", TerminalQualificationID: "terminal-qualification", TerminalQualification: &qualification}
	dispatch := DispatchReservation{ID: "dispatch-1", ProjectID: "project-1", PolicyRevision: "swarm-rev-1", Reservation: Lease{Fence: 7}, HostSlot: &HostSlotClaim{AuthorityDomainID: policy.AuthorityDomainID, HostID: policy.HostID, HostSessionID: policy.HostSessionID, ExecutorInstanceID: policy.ExecutorInstanceID, CapabilityDigest: policy.CapabilityDigest}}
	intent := DispatchIntent{ID: "intent-1", OperationKey: "original-intent-op", HostAgentID: "host-agent-1", WorkerSessionID: "worker-1", State: "acknowledged", Reservation: dispatch}
	policyRecord := SwarmPolicy{ProjectID: "project-1", Revision: "swarm-rev-1", HostSlots: &policy}
	s := &state{Binding: Binding{Workspace: "workspace", Installation: "installation"}, Sessions: map[string]record{"host-session": {Session: Session{ID: "host-session", ExpiresAt: time.Now().Add(2 * time.Hour)}}}, Dispatches: map[string]DispatchReservation{dispatch.ID: dispatch}, DispatchIntents: map[string]DispatchIntent{dispatch.ID: intent}, SwarmPolicies: map[string]SwarmPolicy{"project-1\x00swarm-rev-1": policyRecord}, LifecycleObservations: map[string][]LifecycleObservation{}}
	binding := HostSlotObservationBinding{DispatchID: dispatch.ID, ReservationFence: 7, IntentID: intent.ID, OriginalOperationKey: intent.OperationKey, HostAgentID: intent.HostAgentID}
	if !censusBindingMatches(s, policy, binding) {
		t.Fatal("exact original intent binding refused")
	}
	terminal := &HostSlotTerminalProof{AuthorityDomainID: policy.AuthorityDomainID, DispatchID: dispatch.ID, ReservationFence: 7, IntentID: intent.ID, HostAgentID: intent.HostAgentID, WorkerSessionID: intent.WorkerSessionID, Source: policy.TerminalObservationSource, QualificationID: policy.TerminalQualificationID, QualificationDigest: hostSlotTerminalQualificationDigest(qualification), QualificationPolicyRevision: policyRecord.Revision}
	lifecycle := LifecycleObservation{Sequence: 1, Binding: LifecycleBinding{DispatchID: dispatch.ID, IntentID: intent.ID, HostAgentID: intent.HostAgentID, WorkerSessionID: intent.WorkerSessionID, ReservationFence: 7}, State: "completed", Source: terminal.Source, ReceivedAt: time.Now(), HostSlotTerminal: terminal}
	s.LifecycleObservations[dispatch.ID] = []LifecycleObservation{lifecycle}
	if !hostSlotTerminalProof(s, dispatch) || !censusBindingMatches(s, policy, binding) {
		t.Fatal("later terminal proof invalidated the historical census binding")
	}
	changedOperation := binding
	changedOperation.OriginalOperationKey = "replacement-intent-op"
	if censusBindingMatches(s, policy, changedOperation) {
		t.Fatal("census accepted a substituted original operation key")
	}
	changedWorker := binding
	changedWorker.HostAgentID = "replacement-host-agent"
	if censusBindingMatches(s, policy, changedWorker) {
		t.Fatal("census accepted a substituted host execution identity")
	}
}

func TestHostSlotTerminalProofFreesExecutionClaimButRetainsWriterLease(t *testing.T) {
	const dispatchID, projectID, domainID = "dispatch-1", "project-1", "domain-1"
	qualification := HostSlotTerminalQualification{SchemaVersion: 1, ID: "terminal-qual-1", State: "supported", Source: "owner-bridge-terminal", EvidenceDigest: stringsOf('d', 64), AuthorityDomainID: domainID, HostID: "host-1", HostSessionID: "host-session", ExecutorInstanceID: "executor-1", Runtime: "codex", Build: "build-1", CapabilityDigest: stringsOf('a', 64), QualifiedAt: time.Now().Add(-90 * time.Minute), ExpiresAt: time.Now().Add(-30 * time.Minute)}
	dispatch := DispatchReservation{ID: dispatchID, ProjectID: projectID, PolicyRevision: "swarm-rev-1", Converted: true, WorkerSessionID: "worker-1", Reservation: Lease{ID: dispatchID, TaskID: "task-1", Fence: 9}, HostSlot: &HostSlotClaim{SchemaVersion: 1, AuthorityDomainID: domainID, HostID: qualification.HostID, HostSessionID: qualification.HostSessionID, ExecutorInstanceID: qualification.ExecutorInstanceID, CapabilityDigest: qualification.CapabilityDigest, DispatchID: dispatchID, DispatchFence: 9, Units: 1}}
	intent := DispatchIntent{ID: "intent-1", DispatchID: dispatchID, HostAgentID: "agent-1", WorkerSessionID: "worker-1"}
	policySlots := HostSlotAdmissionPolicy{SchemaVersion: 1, Mode: HostSlotSingleAuthority, Revision: "slots-rev-1", WorkspaceID: "workspace", InstallationID: "installation", AuthorityDomainID: domainID, HostID: qualification.HostID, HostSessionID: qualification.HostSessionID, ExecutorInstanceID: qualification.ExecutorInstanceID, Runtime: qualification.Runtime, Build: qualification.Build, CapabilityDigest: qualification.CapabilityDigest, CapacityPolicyRevision: "capacity-rev-1", QualificationID: "capacity-qual-1", TerminalObservationSource: qualification.Source, TerminalQualificationID: qualification.ID, TerminalQualification: &qualification}
	policy := SwarmPolicy{ProjectID: projectID, Revision: "swarm-rev-1", HostSlots: &policySlots}
	binding := LifecycleBinding{DispatchID: dispatchID, IntentID: intent.ID, BridgeSessionID: "bridge-1", WorkerSessionID: intent.WorkerSessionID, HostAgentID: intent.HostAgentID, ReservationFence: 9, AttemptID: "attempt-1", LeaseID: "lease-1", LeaseFence: 10}
	s := &state{
		Binding:                Binding{Workspace: "workspace", Installation: "installation"},
		Sessions:               map[string]record{"host-session": {Session: Session{ID: "host-session", Revoked: true, ExpiresAt: time.Now().Add(-20 * time.Minute)}}},
		Dispatches:             map[string]DispatchReservation{dispatchID: dispatch},
		DispatchIntents:        map[string]DispatchIntent{dispatchID: intent},
		SwarmPolicies:          map[string]SwarmPolicy{projectID + "\x00swarm-rev-1": policy},
		LifecycleObservations:  map[string][]LifecycleObservation{},
		LifecycleCancellations: map[string]LifecycleCancellation{dispatchID: {State: "acknowledged"}},
		Tasks:                  map[string]TaskBinding{"task-1": {ProjectID: projectID}},
		Leases:                 map[string]Lease{"lease-1": {ID: "lease-1", TaskID: "task-1", SessionID: "worker-1", Fence: 10}},
	}
	if got := hostSlotClaimCount(s, domainID); got != 1 {
		t.Fatalf("cancellation acknowledgement freed execution claim: got %d want 1", got)
	}
	if got := controllerOccupancy(s, projectID); got != 1 {
		t.Fatalf("test lease did not retain writer protection: got %d want 1", got)
	}
	proof := &HostSlotTerminalProof{AuthorityDomainID: domainID, DispatchID: dispatchID, ReservationFence: 9, IntentID: intent.ID, HostAgentID: intent.HostAgentID, WorkerSessionID: intent.WorkerSessionID, AttemptID: binding.AttemptID, LeaseID: binding.LeaseID, LeaseFence: binding.LeaseFence, BridgeSessionID: binding.BridgeSessionID, Source: "owner-bridge-terminal", QualificationID: "terminal-qual-1", QualificationDigest: hostSlotTerminalQualificationDigest(qualification), QualificationPolicyRevision: policy.Revision}
	s.LifecycleObservations[dispatchID] = []LifecycleObservation{{Sequence: 1, Binding: binding, State: "completed", Source: proof.Source, ReceivedAt: time.Now().Add(-60 * time.Minute), HostSlotTerminal: proof}}
	if hostSlotTerminalQualificationCurrent(s, policySlots, time.Now()) {
		t.Fatal("expired terminal qualification remained current for new ingestion")
	}
	if validHostSlotTerminalObservation(s, LifecycleObservation{Sequence: 1, Binding: LifecycleBinding{DispatchID: dispatchID, IntentID: intent.ID, BridgeSessionID: binding.BridgeSessionID, WorkerSessionID: binding.WorkerSessionID, HostAgentID: binding.HostAgentID, ReservationFence: 10, AttemptID: binding.AttemptID, LeaseID: binding.LeaseID, LeaseFence: binding.LeaseFence}, State: "completed", Source: proof.Source, HostSlotTerminal: proof}) {
		t.Fatal("terminal proof accepted a substituted reservation fence")
	}
	if got := hostSlotClaimCount(s, domainID); got != 0 {
		t.Fatalf("accepted exact terminal proof did not free one execution claim: got %d want 0", got)
	}
	if got := controllerOccupancy(s, projectID); got != 1 {
		t.Fatalf("terminal proof incorrectly cleared retained writer lease: got %d want 1", got)
	}
}

func TestHostSlotQualifiedTerminalIngestionSurvivesBridgeRevocationAndReopen(t *testing.T) {
	f, workers, tokens := hostSlotSwarmFixture(t, 2)
	if err := f.a.ConfigureLifecycle(f.owner, LifecyclePolicy{SchemaVersion: 1, ProjectID: "project", Revision: "lifecycle-terminal-v1", Source: "owner-manual-native-host", Freshness: time.Minute, CancellationEnabled: true, CancellationChannel: "owner-manual-native-host"}); err != nil {
		t.Fatal(err)
	}
	plan := swarmPlan(t, f)
	wave, err := f.a.ReserveWave(f.token, f.s.ID, ReserveWaveRequest{OperationKey: "reserve-terminal-proof", Plan: plan, ExpiresAt: time.Now().Add(10 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	var dispatch DispatchReservation
	for _, candidate := range wave.Dispatches {
		if candidate.Reservation.TaskID == "task-a" {
			dispatch = candidate
		}
	}
	if dispatch.ID == "" {
		t.Fatal("terminal fixture did not include task-a")
	}
	resources := []string{"project:project"}
	for _, resource := range dispatch.Reservation.Resources {
		resources = append(resources, resource.Kind+":"+resource.Path)
	}
	bridge, bridgeToken, err := f.a.Enroll(f.owner, Scope{Actions: []string{"read", "dispatch.correlate", "dispatch.observe"}, Resources: resources}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	worker := workers["task-a"]
	if _, err := f.a.RegisterAttempt(tokens["task-a"], worker.ID, AttemptRequest{RequestID: "terminal-proof-worker", TaskID: "task-a", Checkout: f.dirs[0]}); err != nil {
		t.Fatal(err)
	}
	request := RequestDispatchIntent{OperationKey: "intent-terminal-proof", DispatchID: dispatch.ID, BridgeSessionID: bridge.ID, WorkerSessionID: worker.ID, PromptSpecDigest: "sha256:" + stringsOf('e', 64), ExpectedFence: dispatch.Reservation.Fence}
	permit, err := f.a.RequestDispatchIntent(f.token, f.s.ID, request)
	if err != nil || !permit.HostActionAuthorized {
		t.Fatalf("intent permit: %+v %v", permit, err)
	}
	intent, err := f.a.CorrelateDispatchIntent(bridgeToken, bridge.ID, CorrelateDispatchIntent{OperationKey: "correlate-terminal-proof", DispatchID: dispatch.ID, IntentID: permit.Intent.ID, HostAgentID: "host-agent-terminal", State: "host_correlated", ExpectedFence: dispatch.Reservation.Fence, ExpectedSequence: permit.Intent.Sequence})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.a.transaction(false, false, func(s *state) error {
		if got := hostSlotClaimCount(s, "domain-slots-1"); got != 2 {
			t.Fatalf("unexpected pre-terminal claim count: %d", got)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	observation, err := f.a.ObserveLifecycle(bridgeToken, bridge.ID, ObserveLifecycleRequest{OperationKey: "terminal-proof-observation", Binding: lifecycleBinding(intent), ExpectedSequence: 0, HostSequence: 1, State: "completed", Source: "owner-manual-native-host", EvidenceDigest: OutputDigest([]byte("terminal evidence")), SourceTime: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if observation.HostSlotTerminal == nil || observation.HostSlotTerminal.QualificationID != "terminal-qualification-v1" || observation.HostSlotTerminal.QualificationDigest == "" || observation.HostSlotTerminal.QualificationPolicyRevision != f.p.Revision {
		t.Fatalf("accepted terminal observation did not freeze qualified evidence: %+v", observation.HostSlotTerminal)
	}
	if err := f.a.transaction(false, true, func(s *state) error {
		record := s.Sessions[bridge.ID]
		record.Revoked = true
		record.ExpiresAt = time.Now().Add(-time.Minute)
		s.Sessions[bridge.ID] = record
		return nil
	}); err != nil {
		t.Fatalf("revoke bridge after accepted observation: %v", err)
	}
	reopened, err := OpenHostSlots(f.a.path, f.a.binding)
	if err != nil {
		t.Fatalf("reopen after bridge revocation: %v", err)
	}
	if err := reopened.transaction(false, false, func(s *state) error {
		if got := hostSlotClaimCount(s, "domain-slots-1"); got != 1 {
			t.Fatalf("accepted terminal proof did not persist across bridge revoke/reopen: remaining claims=%d", got)
		}
		if !hostSlotTerminalProof(s, s.Dispatches[dispatch.ID]) {
			t.Fatal("reopened accepted terminal proof no longer validates")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func hostSlotSwarmFixture(t *testing.T, requestedSlots int) (swarmFixture, map[string]Session, map[string]string) {
	t.Helper()
	f, _, _, workers, tokens := preflightSetup(t)
	for _, upgrade := range []func(string) error{f.a.UpgradeCheckpoints, f.a.UpgradeBudgets, f.a.UpgradeNativeAcceptance, f.a.UpgradeNativePolicy, f.a.UpgradeController, f.a.UpgradeNativeModelRouting, f.a.UpgradeNativeIntegration, f.a.UpgradeNativeExternal, f.a.UpgradeNativeRoleRegistry, f.a.UpgradeHostCapacity} {
		if err := upgrade(f.owner); err != nil {
			t.Fatal(err)
		}
	}
	host, _, err := f.a.Enroll(f.owner, Scope{Actions: []string{"host.observe"}, Resources: []string{"workspace:" + f.a.binding.Workspace}}, time.Now().Add(30*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	observer, observerToken, err := f.a.EnrollHostCapacityObserver(f.owner, HostCapacityObserver{SchemaVersion: 1, HostSessionID: host.ID, ExecutorInstanceID: "executor-slots", HostID: "host-slots", Runtime: "codex", Build: "build-slots", CapabilityDigest: stringsOf('a', 64), ExpiresAt: time.Now().Add(20 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	capacityPolicy := HostCapacityPolicy{SchemaVersion: 1, WorkspaceID: f.a.binding.Workspace, InstallationID: f.a.binding.Installation, HostID: observer.HostID, Runtime: observer.Runtime, Build: observer.Build, HostSessionID: observer.HostSessionID, ExecutorInstanceID: observer.ExecutorInstanceID, CapabilityDigest: observer.CapabilityDigest, State: CapacityKnown, RequestedSlots: requestedSlots, Freshness: 10 * time.Minute, Revision: "capacity-slots-v1"}
	if _, err := f.a.ConfigureHostCapacityPolicy(f.owner, capacityPolicy); err != nil {
		t.Fatal(err)
	}
	if _, err := f.a.QualifyHostCapacity(f.owner, HostCapacityQualification{SchemaVersion: 1, HostSessionID: observer.HostSessionID, ExecutorInstanceID: observer.ExecutorInstanceID, HostID: observer.HostID, Runtime: observer.Runtime, Build: observer.Build, CapabilityDigest: observer.CapabilityDigest, State: CapacityKnown, EvidenceDigest: stringsOf('b', 64), QualificationID: "qualified-slots-v1", QualifiedSlots: 2, ExpiresAt: time.Now().Add(15 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err := f.a.UpgradeHostSlots(f.owner); err != nil {
		t.Fatal(err)
	}
	f.p.Revision = "policy-host-slots"
	terminalQualification := &HostSlotTerminalQualification{SchemaVersion: 1, ID: "terminal-qualification-v1", State: "supported", Source: "owner-manual-native-host", EvidenceDigest: stringsOf('d', 64), AuthorityDomainID: "domain-slots-1", HostID: observer.HostID, HostSessionID: observer.HostSessionID, ExecutorInstanceID: observer.ExecutorInstanceID, Runtime: observer.Runtime, Build: observer.Build, CapabilityDigest: observer.CapabilityDigest, QualifiedAt: time.Now().UTC(), ExpiresAt: time.Now().Add(10 * time.Minute)}
	f.p.HostSlots = &HostSlotAdmissionPolicy{SchemaVersion: 1, Mode: HostSlotSingleAuthority, Revision: "host-slots-rev1", AuthorityDomainID: "domain-slots-1", WorkspaceID: f.a.binding.Workspace, InstallationID: f.a.binding.Installation, HostID: observer.HostID, HostSessionID: observer.HostSessionID, ExecutorInstanceID: observer.ExecutorInstanceID, Runtime: observer.Runtime, Build: observer.Build, CapabilityDigest: observer.CapabilityDigest, CapacityPolicyRevision: capacityPolicy.Revision, QualificationID: "qualified-slots-v1", TerminalObservationSource: "owner-manual-native-host", TerminalQualificationID: "terminal-qualification-v1", TerminalQualification: terminalQualification}
	if err := f.a.ConfigureSwarm(f.owner, f.p); err != nil {
		t.Fatal(err)
	}
	_, _, err = f.a.RecordHostCapacityObservationV2(observerToken, observer.ObserverSessionID, HostCapacityObservationRequestV2{HostCapacityObservationRequest: HostCapacityObservationRequest{SchemaVersion: 2, OperationKey: "capacity-slots-empty", HostID: observer.HostID, Runtime: observer.Runtime, Build: observer.Build, HostSessionID: observer.HostSessionID, ExecutorInstanceID: observer.ExecutorInstanceID, CapabilityDigest: observer.CapabilityDigest, Sequence: 1, DeclarationState: CapacityKnown, DeclaredSlots: 2, ObservedState: CapacityKnown, OccupiedSlots: 0, AvailableSlots: 2, ObservedAt: time.Now().Add(-time.Second).UTC()}, Census: HostCapacityCensus{SchemaVersion: 1, AuthorityDomainID: "domain-slots-1", IncludedLocalExecutions: []HostSlotObservationBinding{}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.a.transaction(false, false, func(*state) error { return nil }); err != nil {
		t.Fatalf("schema22 fixture state invalid before planning: %v", err)
	}
	return f, workers, tokens
}

func TestHostSlotReserveCommitsExactClaimsAtCapAndRefusesCapPlusOneAtomically(t *testing.T) {
	for _, slots := range []int{1, 2} {
		t.Run(map[int]string{1: "cap_plus_one_refused", 2: "cap_exactly_full_commits"}[slots], func(t *testing.T) {
			f, _, _ := hostSlotSwarmFixture(t, slots)
			plan := swarmPlan(t, f)
			selected := 0
			for _, verdict := range plan.Verdicts {
				if verdict.Selected {
					selected++
				}
			}
			if selected != 2 {
				t.Fatalf("fixture did not produce two disjoint reservations: selected=%d", selected)
			}
			beforeFence := uint64(0)
			if err := f.a.transaction(false, false, func(s *state) error { beforeFence = s.Fence; return nil }); err != nil {
				t.Fatal(err)
			}
			wave, err := f.a.ReserveWave(f.token, f.s.ID, ReserveWaveRequest{OperationKey: "reserve-slots-" + map[int]string{1: "one", 2: "two"}[slots], Plan: plan, ExpiresAt: time.Now().Add(10 * time.Minute)})
			if slots == 1 {
				denied(t, err)
				if err := f.a.transaction(false, false, func(s *state) error {
					if len(s.Dispatches) != 0 || s.Fence != beforeFence || s.SwarmOperations["reserve-slots-one"].Wave != nil {
						t.Fatalf("cap+1 refusal left partial reservations/fence/receipt: dispatches=%d fence=%d before=%d", len(s.Dispatches), s.Fence, beforeFence)
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				return
			}
			if err != nil {
				t.Fatalf("exact cap wave refused: %v", err)
			}
			if len(wave.Dispatches) != slots {
				t.Fatalf("committed %d dispatches at cap %d", len(wave.Dispatches), slots)
			}
			if err := f.a.transaction(false, false, func(s *state) error {
				receipt := s.SwarmOperations["reserve-slots-two"]
				if receipt.Wave == nil || len(receipt.Wave.Dispatches) != slots {
					t.Fatalf("atomic reservation receipt missing exact wave: %+v", receipt)
				}
				for _, dispatch := range wave.Dispatches {
					stored := s.Dispatches[dispatch.ID]
					if stored.HostSlot == nil || stored.HostSlot.DispatchID != dispatch.ID || stored.HostSlot.ReserveOperationKey != "reserve-slots-two" || stored.HostSlot.DispatchFence != stored.Reservation.Fence {
						t.Fatalf("original dispatch lacks exact slot claim: %+v", stored)
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestHostSlotFinalIntentGuardRejectsCapacityDriftDuringRoleCallback(t *testing.T) {
	f, workers, tokens := hostSlotSwarmFixture(t, 2)
	plan := swarmPlan(t, f)
	wave, err := f.a.ReserveWave(f.token, f.s.ID, ReserveWaveRequest{OperationKey: "reserve-intent-drift", Plan: plan, ExpiresAt: time.Now().Add(10 * time.Minute)})
	if err != nil {
		t.Fatalf("reserve fixture wave: %v", err)
	}
	var dispatch DispatchReservation
	for _, candidate := range wave.Dispatches {
		if candidate.Reservation.TaskID == "task-a" {
			dispatch = candidate
		}
	}
	if dispatch.ID == "" {
		t.Fatal("reserve fixture did not include task-a")
	}
	resources := []string{"project:project"}
	for _, resource := range dispatch.Reservation.Resources {
		resources = append(resources, resource.Kind+":"+resource.Path)
	}
	bridge, _, err := f.a.Enroll(f.owner, Scope{Actions: []string{"read", "dispatch.correlate"}, Resources: resources}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	worker := workers["task-a"]
	attempt, err := f.a.RegisterAttempt(tokens["task-a"], worker.ID, AttemptRequest{RequestID: "slot-drift-worker", TaskID: "task-a", Checkout: f.dirs[0]})
	if err != nil {
		t.Fatal(err)
	}
	var current state
	if err := f.a.transaction(false, false, func(s *state) error { current = *s; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := f.a.dispatchPreflight(&current, dispatch, ""); err != nil {
		t.Fatalf("intent preflight fixture: %v", err)
	}
	baseAdmission, ok := f.a.admission.(*preflightFixtureAdmission)
	if !ok {
		t.Fatalf("fixture admission type = %T", f.a.admission)
	}
	admission := &hostSlotRoleDriftAdmission{preflightFixtureAdmission: baseAdmission, state: &current}
	f.a.admission = admission
	req := RequestDispatchIntent{OperationKey: "intent-capacity-drift", DispatchID: dispatch.ID, BridgeSessionID: bridge.ID, WorkerSessionID: worker.ID, PromptSpecDigest: "sha256:" + stringsOf('c', 64), ExpectedFence: dispatch.Reservation.Fence}
	result, err := f.a.requestDispatchIntentState(&current, f.token, f.s.ID, req)
	if !errors.Is(err, ErrDenied) || result.HostActionAuthorized || result.Intent.ID != "" {
		t.Fatalf("final intent guard issued permit after callback capacity drift: callbacks=%d result=%+v err=%v", admission.observeCalls, result, err)
	}
	if admission.observeCalls != 2 {
		t.Fatalf("role callback ran %d times, want admit+final role recheck", admission.observeCalls)
	}
	if _, exists := current.DispatchIntents[dispatch.ID]; exists {
		t.Fatal("capacity drift left a durable intent")
	}
	if _, exists := current.DispatchIntentOperations[req.OperationKey]; exists {
		t.Fatal("capacity drift left an intent receipt")
	}
	_ = attempt
}

func TestHostSlotPolicyTransitionsCannotRebindOrHideOutstandingClaims(t *testing.T) {
	f, _, _ := hostSlotSwarmFixture(t, 2)
	plan := swarmPlan(t, f)
	wave, err := f.a.ReserveWave(f.token, f.s.ID, ReserveWaveRequest{OperationKey: "reserve-policy-transition", Plan: plan, ExpiresAt: time.Now().Add(10 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	claimCount := len(wave.Dispatches)
	replacement := f.p
	replacement.Revision = "host-slot-new-domain"
	replacement.HostSlots = func() *HostSlotAdmissionPolicy {
		policy := *f.p.HostSlots
		policy.Revision = "host-slots-rev2"
		policy.AuthorityDomainID = "domain-slots-replacement"
		return &policy
	}()
	if err := f.a.ConfigureSwarm(f.owner, replacement); !errors.Is(err, ErrDenied) {
		t.Fatalf("domain replacement hid outstanding claims: %v", err)
	}
	manual := f.p
	manual.Revision = "host-slot-manual-mode"
	manual.HostSlots = &HostSlotAdmissionPolicy{SchemaVersion: 1, Mode: HostSlotManual, Revision: "host-slots-manual-v1", AuthorityDomainID: "domain-slots-1", WorkspaceID: f.a.binding.Workspace, InstallationID: f.a.binding.Installation, HostID: f.p.HostSlots.HostID, HostSessionID: f.p.HostSlots.HostSessionID, ExecutorInstanceID: f.p.HostSlots.ExecutorInstanceID, Runtime: f.p.HostSlots.Runtime, Build: f.p.HostSlots.Build, CapabilityDigest: f.p.HostSlots.CapabilityDigest, CapacityPolicyRevision: f.p.HostSlots.CapacityPolicyRevision, QualificationID: f.p.HostSlots.QualificationID}
	if err := f.a.ConfigureSwarm(f.owner, manual); err != nil {
		t.Fatalf("explicit manual-mode configuration refused: %v", err)
	}
	if err := f.a.transaction(false, false, func(s *state) error {
		if got := hostSlotClaimCount(s, "domain-slots-1"); got != claimCount {
			t.Fatalf("manual mode hid outstanding claims: got %d want %d", got, claimCount)
		}
		for _, dispatch := range s.Dispatches {
			if dispatch.HostSlot == nil {
				t.Fatalf("manual transition erased historical claim on %s", dispatch.ID)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestHostSlotTerminalQualificationRequiresExactCurrentOwnerEvidence(t *testing.T) {
	f, _, _ := hostSlotSwarmFixture(t, 2)
	mutations := map[string]func(*HostSlotTerminalQualification){
		"unsupported state":        func(q *HostSlotTerminalQualification) { q.State = "unknown" },
		"invalid evidence digest":  func(q *HostSlotTerminalQualification) { q.EvidenceDigest = "not-a-digest" },
		"foreign authority domain": func(q *HostSlotTerminalQualification) { q.AuthorityDomainID = "other-domain" },
		"future qualified time": func(q *HostSlotTerminalQualification) {
			q.QualifiedAt = time.Now().Add(time.Minute)
			q.ExpiresAt = time.Now().Add(2 * time.Minute)
		},
		"source mismatch": func(q *HostSlotTerminalQualification) { q.Source = "another-source" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			policy := f.p
			policy.Revision = "host-slot-invalid-" + strings.ReplaceAll(name, " ", "-")
			slots := *f.p.HostSlots
			qualification := *f.p.HostSlots.TerminalQualification
			mutate(&qualification)
			slots.TerminalQualification = &qualification
			policy.HostSlots = &slots
			if err := f.a.ConfigureSwarm(f.owner, policy); !errors.Is(err, ErrDenied) {
				t.Fatalf("invalid terminal source qualification was accepted: %v", err)
			}
		})
	}
}

func TestHostSlotSerializedConcurrentReservationsCannotExceedCap(t *testing.T) {
	f, _, _ := hostSlotSwarmFixture(t, 1)
	tasks := []string{"task-a", "task-c"}
	start := make(chan struct{})
	type outcome struct {
		task string
		wave SwarmWave
		err  error
	}
	results := make(chan outcome, len(tasks))
	for _, task := range tasks {
		task := task
		go func() {
			<-start
			req := ReserveWaveRequest{OperationKey: "concurrent-slot-" + task, ExpiresAt: time.Now().Add(10 * time.Minute)}
			var wave SwarmWave
			err := f.a.transaction(false, true, func(s *state) error {
				plan, err := f.a.planSwarmLimited(s, f.token, f.s.ID, "project", "swarm.reserve", 0, []string{task})
				if err != nil {
					return err
				}
				selected := 0
				for _, verdict := range plan.Verdicts {
					if verdict.Selected {
						selected++
					}
				}
				if selected != 1 {
					return ErrDenied
				}
				req.Plan = plan
				wave, err = f.a.reserveWaveStateLimited(s, f.token, f.s.ID, req, 0, []string{task})
				return err
			})
			results <- outcome{task: task, wave: wave, err: err}
		}()
	}
	close(start)
	succeeded, refused := 0, 0
	for range tasks {
		result := <-results
		if result.err == nil {
			succeeded++
			if len(result.wave.Dispatches) != 1 || result.wave.Dispatches[0].HostSlot == nil {
				t.Fatalf("successful %s reservation lacks one durable claim: %+v", result.task, result.wave)
			}
		} else if errors.Is(result.err, ErrDenied) {
			refused++
		} else {
			t.Fatalf("concurrent %s reservation failed unexpectedly: %v", result.task, result.err)
		}
	}
	if succeeded != 1 || refused != 1 {
		t.Fatalf("serialized cap-1 race committed %d and refused %d; want one each", succeeded, refused)
	}
	if err := f.a.transaction(false, false, func(s *state) error {
		if len(s.Dispatches) != 1 || hostSlotClaimCount(s, "domain-slots-1") != 1 {
			t.Fatalf("concurrent reservation exceeded cap or partially committed: dispatches=%d claims=%d", len(s.Dispatches), hostSlotClaimCount(s, "domain-slots-1"))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestHostSlotConcurrentPublicPlanAndReserveRefuseFreshCapPlusOne(t *testing.T) {
	f, _, _ := hostSlotSwarmFixture(t, 1)
	second, secondToken, err := f.a.Enroll(f.owner, Scope{Actions: []string{"swarm.plan", "swarm.reserve", "swarm.dispatch"}, Resources: []string{"project:project", "task:task-a", "task:task-c", "path:src/a", "path:other"}}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	base, ok := f.a.admission.(*preflightFixtureAdmission)
	if !ok {
		t.Fatalf("fixture admission type = %T", f.a.admission)
	}
	cloneFor := func(task string) *Authority {
		baseCopy := *base
		admissionCopy := *base.swarmFixtureAdmission
		admissionCopy.snapshot.Candidates = nil
		for _, candidate := range base.swarmFixtureAdmission.snapshot.Candidates {
			if candidate.TaskID == task {
				admissionCopy.snapshot.Candidates = append(admissionCopy.snapshot.Candidates, candidate)
			}
		}
		admissionCopy.snapshot.Digest = "snapshot-only-" + task
		baseCopy.swarmFixtureAdmission = &admissionCopy
		authority := *f.a
		authority.admission = &baseCopy
		return &authority
	}
	type proposal struct {
		name, token string
		session     Session
		authority   *Authority
		task        string
	}
	proposals := []proposal{
		{name: "first", token: f.token, session: f.s, authority: cloneFor("task-a"), task: "task-a"},
		{name: "second", token: secondToken, session: second, authority: cloneFor("task-c"), task: "task-c"},
	}
	startPlans := make(chan struct{})
	startReserves := make(chan struct{})
	type planned struct {
		proposal proposal
		plan     SwarmPlan
		err      error
	}
	plans := make(chan planned, len(proposals))
	type result struct {
		planned
		wave SwarmWave
	}
	results := make(chan result, len(proposals))
	for _, proposal := range proposals {
		proposal := proposal
		go func() {
			<-startPlans
			plan, planErr := proposal.authority.PlanSwarm(proposal.token, proposal.session.ID, "project")
			plans <- planned{proposal: proposal, plan: plan, err: planErr}
			<-startReserves
			if planErr != nil {
				results <- result{planned: planned{proposal: proposal, err: planErr}}
				return
			}
			wave, reserveErr := proposal.authority.ReserveWave(proposal.token, proposal.session.ID, ReserveWaveRequest{OperationKey: "public-concurrent-" + proposal.name, Plan: plan, ExpiresAt: time.Now().Add(10 * time.Minute)})
			results <- result{planned: planned{proposal: proposal, plan: plan, err: reserveErr}, wave: wave}
		}()
	}
	close(startPlans)
	firstPlan, secondPlan := <-plans, <-plans
	for _, item := range []planned{firstPlan, secondPlan} {
		if item.err != nil {
			t.Fatalf("public PlanSwarm for %s failed: %v", item.proposal.task, item.err)
		}
		selected := 0
		for _, verdict := range item.plan.Verdicts {
			if verdict.Selected {
				selected++
			}
		}
		if selected != 1 {
			t.Fatalf("public proposal for %s selected %d tasks", item.proposal.task, selected)
		}
	}
	close(startReserves)
	reserveResults := []result{<-results, <-results}
	var winner, loser result
	for _, item := range reserveResults {
		if item.err == nil {
			winner = item
		} else if errors.Is(item.err, ErrDenied) {
			loser = item
		} else {
			t.Fatalf("public ReserveWave for %s failed unexpectedly: %v", item.proposal.task, item.err)
		}
	}
	if winner.proposal.name == "" || loser.proposal.name == "" || len(winner.wave.Dispatches) != 1 || winner.wave.Dispatches[0].HostSlot == nil {
		t.Fatalf("public race did not produce exactly one bound reservation: winner=%+v loser=%+v", winner, loser)
	}
	// The loser may have observed the changed fence. Re-plan once under that new
	// premise, then prove the still-unoccupied task is refused by the full cap.
	freshPlan, err := loser.proposal.authority.PlanSwarm(loser.proposal.token, loser.proposal.session.ID, "project")
	if err != nil {
		t.Fatalf("bounded changed-premise re-plan for %s: %v", loser.proposal.task, err)
	}
	selected := 0
	for _, verdict := range freshPlan.Verdicts {
		if verdict.Selected {
			selected++
		}
	}
	if selected != 1 {
		t.Fatalf("loser's still-free task %s was blocked by an unrelated WIP/role limit: selected=%d", loser.proposal.task, selected)
	}
	if _, err := loser.proposal.authority.ReserveWave(loser.proposal.token, loser.proposal.session.ID, ReserveWaveRequest{OperationKey: "public-fresh-cap-plus-one", Plan: freshPlan, ExpiresAt: time.Now().Add(10 * time.Minute)}); !errors.Is(err, ErrDenied) {
		t.Fatalf("fresh public cap+1 reserve returned %v, want host-slot refusal", err)
	}
	if err := f.a.transaction(false, false, func(s *state) error {
		if len(s.Dispatches) != 1 || hostSlotClaimCount(s, "domain-slots-1") != 1 || s.SwarmOperations["public-fresh-cap-plus-one"].Wave != nil {
			t.Fatalf("public cap+1 attempted extra dispatch/fence/receipt: dispatches=%d claims=%d", len(s.Dispatches), hostSlotClaimCount(s, "domain-slots-1"))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestReadDispatchReservationIsExactScopedAndNeverLaunches(t *testing.T) {
	f, workers, tokens := hostSlotSwarmFixture(t, 2)
	plan := swarmPlan(t, f)
	wave, err := f.a.ReserveWave(f.token, f.s.ID, ReserveWaveRequest{OperationKey: "reserve-scoped-read", Plan: plan, ExpiresAt: time.Now().Add(10 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	var dispatch DispatchReservation
	for _, candidate := range wave.Dispatches {
		if candidate.Reservation.TaskID == "task-a" {
			dispatch = candidate
		}
	}
	if dispatch.ID == "" {
		t.Fatal("reservation fixture did not include task-a")
	}
	if admitted, err := f.a.AdmitDispatch(f.token, f.s.ID, AdmitDispatchRequest{DispatchID: dispatch.ID, ExpectedFence: dispatch.Reservation.Fence}); err != nil || admitted.ID != dispatch.ID {
		t.Fatalf("fresh bound dispatch admission failed: %+v %v", admitted, err)
	}
	bridgeResources := []string{"project:project"}
	for _, resource := range dispatch.Reservation.Resources {
		bridgeResources = append(bridgeResources, resource.Kind+":"+resource.Path)
	}
	bridge, _, err := f.a.Enroll(f.owner, Scope{Actions: []string{"read", "dispatch.correlate"}, Resources: bridgeResources}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	preIntentRead, err := f.a.ReadDispatchReservation(tokens["task-a"], workers["task-a"].ID, dispatch.ID, dispatch.Reservation.Fence)
	if err != nil || preIntentRead.ID != dispatch.ID || preIntentRead.HostSlot == nil {
		t.Fatalf("exact scoped reservation read failed: %+v %v", preIntentRead, err)
	}
	if err := f.a.transaction(false, false, func(s *state) error {
		if len(s.DispatchIntents) != 0 || len(s.DispatchIntentOperations) != 0 {
			t.Fatal("read-only reservation lookup created a host action intent")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	intent, err := f.a.RequestDispatchIntent(f.token, f.s.ID, RequestDispatchIntent{OperationKey: "intent-scoped-read", DispatchID: dispatch.ID, BridgeSessionID: bridge.ID, WorkerSessionID: workers["task-a"].ID, PromptSpecDigest: "sha256:" + stringsOf('c', 64), ExpectedFence: dispatch.Reservation.Fence})
	if err != nil || !intent.HostActionAuthorized {
		t.Fatalf("fresh bound intent admission failed: %+v %v", intent, err)
	}
	read, err := f.a.ReadDispatchReservation(tokens["task-a"], workers["task-a"].ID, dispatch.ID, dispatch.Reservation.Fence)
	if err != nil || read.ID != dispatch.ID || read.HostSlot == nil || read.HostSlot.DispatchFence != dispatch.Reservation.Fence {
		t.Fatalf("exact scoped reservation read failed: %+v %v", read, err)
	}
	if _, err := f.a.ReadDispatchReservation(tokens["task-c"], workers["task-c"].ID, dispatch.ID, dispatch.Reservation.Fence); !errors.Is(err, ErrDenied) {
		t.Fatalf("foreign task worker read dispatch: %v", err)
	}
	if _, err := f.a.ReadDispatchReservation(tokens["task-a"], workers["task-a"].ID, dispatch.ID, dispatch.Reservation.Fence+1); !errors.Is(err, ErrDenied) {
		t.Fatalf("stale or substituted fence read dispatch: %v", err)
	}
	if err := f.a.transaction(false, true, func(s *state) error {
		key := capacityFactKey(dispatch.HostSlot.HostSessionID, dispatch.HostSlot.ExecutorInstanceID)
		policy := s.CapacityPolicies[key]
		policy.Freshness = time.Nanosecond
		s.CapacityPolicies[key] = policy
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	read, err = f.a.ReadDispatchReservation(tokens["task-a"], workers["task-a"].ID, dispatch.ID, dispatch.Reservation.Fence)
	if err != nil || read.ID != dispatch.ID {
		t.Fatalf("held reservation lookup incorrectly required fresh launch admission: %+v %v", read, err)
	}
	if err := f.a.transaction(false, false, func(s *state) error {
		if len(s.DispatchIntents) != 1 || len(s.DispatchIntentOperations) != 1 || s.DispatchIntents[dispatch.ID].ID != intent.Intent.ID {
			t.Fatal("read-only reservation lookup altered the durable intent")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

type hostSlotRoleDriftAdmission struct {
	*preflightFixtureAdmission
	state        *state
	observeCalls int
}

func (a *hostSlotRoleDriftAdmission) ObserveSwarm(workspace, project string) (SwarmSnapshot, error) {
	a.observeCalls++
	snapshot, err := a.preflightFixtureAdmission.ObserveSwarm(workspace, project)
	if err == nil && a.observeCalls == 2 {
		for key, observation := range a.state.CapacityObservations {
			observation.ReceivedAt = time.Now().Add(-time.Hour)
			a.state.CapacityObservations[key] = observation
		}
	}
	return snapshot, err
}
