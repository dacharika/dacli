package sessions

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func capacity21Fixture(t *testing.T) (*Authority, string, Session, string, HostCapacityObserver, string) {
	t.Helper()
	a, owner, _ := authorityGrowthFixture(t, 1)
	if err := a.UpgradeNativeRoleRegistry(owner); err != nil {
		t.Fatal(err)
	}
	old20, err := OpenNativeRoleRegistry(a.path, a.binding)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.UpgradeHostCapacity(owner); err != nil {
		t.Fatal(err)
	}
	if err := old20.transaction(false, false, func(*state) error { return nil }); !errors.Is(err, ErrStorage) {
		t.Fatalf("already-open schema-20 reader accepted schema 21: %v", err)
	}
	if err := old20.transaction(false, true, func(*state) error { return nil }); !errors.Is(err, ErrStorage) {
		t.Fatalf("already-open schema-20 writer accepted schema 21: %v", err)
	}
	current, err := OpenCurrent(a.path, a.binding)
	if err != nil {
		t.Fatal(err)
	}
	host, hostToken, err := current.Enroll(owner, Scope{Actions: []string{"host.observe"}, Resources: []string{"workspace:" + a.binding.Workspace}}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	observer, observerToken, err := current.EnrollHostCapacityObserver(owner, HostCapacityObserver{HostSessionID: host.ID, HostID: "host-a", Runtime: "codex", Build: "build-1", CapabilityDigest: stringsOf('a', 64), ExpiresAt: time.Now().Add(30 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	return current, owner, host, hostToken, observer, observerToken
}

func stringsOf(r rune, n int) string {
	b := make([]rune, n)
	for i := range b {
		b[i] = r
	}
	return string(b)
}

func capacityRequest(host Session, seq uint64, key string) HostCapacityObservationRequest {
	return HostCapacityObservationRequest{SchemaVersion: 1, OperationKey: key, HostID: "host-a", Runtime: "codex", Build: "build-1", HostSessionID: host.ID, CapabilityDigest: stringsOf('a', 64), Sequence: seq, DeclarationState: CapacityKnown, DeclaredSlots: 2, ObservedState: CapacityKnown, OccupiedSlots: 1, AvailableSlots: 1, ObservedAt: time.Now().Add(-time.Second).UTC()}
}

func TestHostCapacitySchema21UpgradePreservesSchema20AndOldReaderRefuses(t *testing.T) {
	a, owner, _ := authorityGrowthFixture(t, 2)
	if err := a.UpgradeNativeRoleRegistry(owner); err != nil {
		t.Fatal(err)
	}
	old, err := OpenNativeRoleRegistry(a.path, a.binding)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.UpgradeHostCapacity("not-the-owner"); !errors.Is(err, ErrDenied) {
		t.Fatalf("schema migration accepted a non-owner: %v", err)
	}
	var before state
	if err := a.transaction(false, false, func(s *state) error { before = *s; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := a.UpgradeHostCapacity(owner); err != nil {
		t.Fatal(err)
	}
	var after state
	if err := a.transaction(false, false, func(s *state) error { after = *s; return nil }); err != nil {
		t.Fatal(err)
	}
	before.Version = 21
	before.CapacityPolicies, before.CapacityObservers = map[string]HostCapacityPolicy{}, map[string]HostCapacityObserver{}
	before.CapacityObservations, before.CapacityQualifications = map[string]HostCapacityObservation{}, map[string]HostCapacityQualification{}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("20→21 migration changed existing semantic records")
	}
	current, err := OpenCurrent(a.path, a.binding)
	if err != nil {
		t.Fatalf("current reader rejected schema 21: %v", err)
	}
	for name, open := range map[string]func() (*Authority, error){
		"swarm":     func() (*Authority, error) { return OpenSwarm(a.path, a.binding, nil) },
		"review":    func() (*Authority, error) { return OpenReviewAuthority(a.path, a.binding, &testReviewObserver{}) },
		"dashboard": func() (*Authority, error) { return OpenDashboardAuthority(a.path, a.binding) },
	} {
		t.Run("current-reader-"+name, func(t *testing.T) {
			reader, err := open()
			if err != nil {
				t.Fatalf("%s refused schema 21: %v", name, err)
			}
			if err = reader.transaction(false, false, func(s *state) error {
				if s.Version != 21 {
					return ErrStorage
				}
				return nil
			}); err != nil {
				t.Fatalf("%s transaction refused schema 21: %v", name, err)
			}
		})
	}
	if err := current.transaction(false, true, func(s *state) error {
		if s.Version != 21 || s.CapacityPolicies == nil || s.CapacityObservers == nil || s.CapacityObservations == nil || s.CapacityQualifications == nil {
			t.Fatal("current reader did not initialize schema-21 collections")
		}
		return nil
	}); err != nil {
		t.Fatalf("current schema-21 writer refused: %v", err)
	}
	if err := old.transaction(false, false, func(*state) error { return nil }); !errors.Is(err, ErrStorage) {
		t.Fatalf("schema-20 reader accepted post-upgrade state: %v", err)
	}
}

func TestHostCapacityRejectsUnenrolledOrSubstitutedObserver(t *testing.T) {
	a, _, host, _, observer, observerToken := capacity21Fixture(t)
	var before state
	if err := a.transaction(false, false, func(s *state) error { before = *s; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.RecordHostCapacityObservation(observerToken, "un-enrolled-observer", capacityRequest(host, 1, "capacity-op-1")); !errors.Is(err, ErrDenied) {
		t.Fatalf("un-enrolled observer accepted: %v", err)
	}
	// The observer credential and every field except its enrolled host scope are
	// valid; substituting that one bound identity must refuse.
	for name, substitute := range map[string]func(*HostCapacityObservationRequest){
		"host":    func(r *HostCapacityObservationRequest) { r.HostID = "host-b" },
		"runtime": func(r *HostCapacityObservationRequest) { r.Runtime = "other-runtime" },
		"build":   func(r *HostCapacityObservationRequest) { r.Build = "other-build" },
		"session": func(r *HostCapacityObservationRequest) { r.HostSessionID = "other-session" },
		"digest":  func(r *HostCapacityObservationRequest) { r.CapabilityDigest = stringsOf('b', 64) },
	} {
		t.Run(name, func(t *testing.T) {
			substituted := capacityRequest(host, 1, "capacity-op-1")
			substitute(&substituted)
			if _, _, err := a.RecordHostCapacityObservation(observerToken, observer.ObserverSessionID, substituted); !errors.Is(err, ErrDenied) {
				t.Fatalf("substituted observer binding accepted: %v", err)
			}
		})
	}
	var afterRefusals state
	if err := a.transaction(false, false, func(s *state) error { afterRefusals = *s; return nil }); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, afterRefusals) {
		t.Fatal("refused observer substitutions changed authority state")
	}
	if _, _, err := a.RecordHostCapacityObservation(observerToken, observer.ObserverSessionID, capacityRequest(host, 1, "capacity-op-1")); err != nil {
		t.Fatalf("valid enrolled observer refused: %v", err)
	}
}

func TestHostCapacityFactsRequireAllIndependentPositiveEvidence(t *testing.T) {
	a, owner, host, _, observer, observerToken := capacity21Fixture(t)
	policy := HostCapacityPolicy{WorkspaceID: a.binding.Workspace, InstallationID: a.binding.Installation, HostID: "host-a", Runtime: "codex", Build: "build-1", HostSessionID: host.ID, CapabilityDigest: stringsOf('a', 64), State: CapacityKnown, RequestedSlots: 1, Freshness: time.Minute, Revision: "request-v1"}
	if _, err := a.ConfigureHostCapacityPolicy(owner, policy); err != nil {
		t.Fatal(err)
	}
	if _, err := a.QualifyHostCapacity(owner, HostCapacityQualification{HostSessionID: host.ID, HostID: "host-a", Runtime: "codex", Build: "build-1", CapabilityDigest: stringsOf('a', 64), State: CapacityKnown, QualifiedSlots: 1, EvidenceDigest: stringsOf('b', 64), QualificationID: "qual-v1", ExpiresAt: time.Now().Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	request := capacityRequest(host, 1, "capacity-op-1")
	if _, _, err := a.RecordHostCapacityObservation(observerToken, observer.ObserverSessionID, request); err != nil {
		t.Fatal(err)
	}
	view, err := a.ReadHostCapacity(observerToken, observer.ObserverSessionID, host.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !view.Eligible {
		t.Fatalf("four fresh independently known facts not eligible: %+v", view)
	}
	unknown := request
	unknown.OperationKey = "capacity-op-unknown"
	unknown.Sequence = 2
	unknown.ObservedState = CapacityUnknown
	unknown.OccupiedSlots, unknown.AvailableSlots = 0, 0
	if _, _, err := a.RecordHostCapacityObservation(observerToken, observer.ObserverSessionID, unknown); err != nil {
		t.Fatal(err)
	}
	view, err = a.ReadHostCapacity(observerToken, observer.ObserverSessionID, host.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.ObservationState != CapacityUnknown || view.Eligible {
		t.Fatalf("unknown observation became positive eligibility: %+v", view)
	}
	if _, err := a.QualifyHostCapacity(owner, HostCapacityQualification{HostSessionID: host.ID, HostID: "host-a", Runtime: "codex", Build: "build-1", CapabilityDigest: stringsOf('a', 64), State: CapacityUnknown, QualificationID: "qualification-unknown"}); err != nil {
		t.Fatal(err)
	}
	knownUnqualified := request
	knownUnqualified.OperationKey = "capacity-op-unqualified"
	knownUnqualified.Sequence = 3
	if _, _, err := a.RecordHostCapacityObservation(observerToken, observer.ObserverSessionID, knownUnqualified); err != nil {
		t.Fatal(err)
	}
	view, err = a.ReadHostCapacity(observerToken, observer.ObserverSessionID, host.ID)
	if err != nil || view.QualificationState != CapacityUnknown || view.Eligible {
		t.Fatalf("unknown qualification became positive eligibility: %+v err=%v", view, err)
	}
	unsupported := unknown
	unsupported.OperationKey = "capacity-op-unsupported"
	unsupported.Sequence = 4
	unsupported.DeclarationState = CapacityUnsupported
	unsupported.DeclaredSlots = 0
	unsupported.ObservedState = CapacityUnsupported
	if _, _, err := a.RecordHostCapacityObservation(observerToken, observer.ObserverSessionID, unsupported); err != nil {
		t.Fatal(err)
	}
	view, err = a.ReadHostCapacity(observerToken, observer.ObserverSessionID, host.ID)
	if err != nil || view.ObservationState != CapacityUnsupported || view.Eligible {
		t.Fatalf("unsupported fact lost distinction or became eligible: %+v err=%v", view, err)
	}
}

func TestHostCapacityObservationRejectsChangedReplayStaleSequenceFutureAndInvalidArithmetic(t *testing.T) {
	a, _, host, _, observer, observerToken := capacity21Fixture(t)
	r := capacityRequest(host, 2, "capacity-op-2")
	if _, _, err := a.RecordHostCapacityObservation(observerToken, observer.ObserverSessionID, r); err != nil {
		t.Fatal(err)
	}
	changed := r
	changed.AvailableSlots = 0
	if _, _, err := a.RecordHostCapacityObservation(observerToken, observer.ObserverSessionID, changed); !errors.Is(err, ErrDenied) {
		t.Fatalf("changed operation replay accepted: %v", err)
	}
	stale := r
	stale.OperationKey = "capacity-op-stale"
	stale.Sequence = 1
	if _, _, err := a.RecordHostCapacityObservation(observerToken, observer.ObserverSessionID, stale); !errors.Is(err, ErrDenied) {
		t.Fatalf("stale sequence accepted: %v", err)
	}
	future := r
	future.OperationKey = "capacity-op-future"
	future.Sequence = 3
	future.ObservedAt = time.Now().Add(time.Minute)
	if _, _, err := a.RecordHostCapacityObservation(observerToken, observer.ObserverSessionID, future); !errors.Is(err, ErrDenied) {
		t.Fatalf("future observation time accepted: %v", err)
	}
	invalid := r
	invalid.OperationKey = "capacity-op-invalid"
	invalid.Sequence = 3
	invalid.OccupiedSlots = int(^uint(0) >> 1)
	invalid.AvailableSlots = 1
	if _, _, err := a.RecordHostCapacityObservation(observerToken, observer.ObserverSessionID, invalid); !errors.Is(err, ErrDenied) {
		t.Fatalf("overflowing capacity arithmetic accepted: %v", err)
	}
	negative := r
	negative.OperationKey = "capacity-op-negative"
	negative.Sequence = 3
	negative.AvailableSlots = -1
	if _, _, err := a.RecordHostCapacityObservation(observerToken, observer.ObserverSessionID, negative); !errors.Is(err, ErrDenied) {
		t.Fatalf("negative observed capacity accepted: %v", err)
	}
	duplicate, replay, err := a.RecordHostCapacityObservation(observerToken, observer.ObserverSessionID, r)
	if err != nil || !replay || duplicate.ReceivedAt != r.ObservedAt && duplicate.Sequence != r.Sequence {
		t.Fatalf("identical replay did not return historical receipt: %+v duplicate=%v err=%v", duplicate, replay, err)
	}
}

func TestHostCapacityExpiredObserverCannotRecord(t *testing.T) {
	a, _, host, _, observer, observerToken := capacity21Fixture(t)
	if err := a.transaction(false, true, func(s *state) error {
		expiredEnrollment := time.Now().Add(-2 * time.Second)
		r := s.Sessions[observer.ObserverSessionID]
		r.ExpiresAt = expiredEnrollment.Add(time.Second)
		s.Sessions[observer.ObserverSessionID] = r
		stored := s.CapacityObservers[observer.ObserverSessionID]
		stored.EnrolledAt = expiredEnrollment
		stored.ExpiresAt = r.ExpiresAt
		s.CapacityObservers[observer.ObserverSessionID] = stored
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.RecordHostCapacityObservation(observerToken, observer.ObserverSessionID, capacityRequest(host, 1, "capacity-expired-observer")); !errors.Is(err, ErrDenied) {
		t.Fatalf("expired observer credential recorded capacity: %v", err)
	}
}

func TestHostCapacityViewMarksStaleAndContradictoryFactsNonEligible(t *testing.T) {
	a, owner, host, _, observer, observerToken := capacity21Fixture(t)
	_, err := a.ConfigureHostCapacityPolicy(owner, HostCapacityPolicy{WorkspaceID: a.binding.Workspace, InstallationID: a.binding.Installation, HostID: "host-a", Runtime: "codex", Build: "build-1", HostSessionID: host.ID, CapabilityDigest: stringsOf('a', 64), State: CapacityKnown, RequestedSlots: 1, Freshness: time.Minute, Revision: "policy-v1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.QualifyHostCapacity(owner, HostCapacityQualification{HostSessionID: host.ID, HostID: "host-a", Runtime: "codex", Build: "build-1", CapabilityDigest: stringsOf('a', 64), State: CapacityKnown, QualifiedSlots: 1, EvidenceDigest: stringsOf('b', 64), QualificationID: "qual-v1"}); err != nil {
		t.Fatal(err)
	}
	r := capacityRequest(host, 1, "capacity-op-1")
	r.DeclaredSlots = 3
	if _, _, err = a.RecordHostCapacityObservation(observerToken, observer.ObserverSessionID, r); err != nil {
		t.Fatal(err)
	}
	view, err := a.ReadHostCapacity(observerToken, observer.ObserverSessionID, host.ID)
	if err != nil || view.ConsistencyState != CapacityContradictory || view.Eligible {
		t.Fatalf("contradictory facts not classified: %+v err=%v", view, err)
	}
	// Use a later coherent sequence and a deterministic old source timestamp.
	r.OperationKey = "capacity-op-2"
	r.Sequence = 2
	r.DeclaredSlots = 2
	r.ObservedAt = time.Now().Add(-2 * time.Minute)
	if _, _, err = a.RecordHostCapacityObservation(observerToken, observer.ObserverSessionID, r); err != nil {
		t.Fatal(err)
	}
	view, err = a.ReadHostCapacity(observerToken, observer.ObserverSessionID, host.ID)
	if err != nil || view.ObservationState != CapacityStale || view.ConsistencyState != CapacityConsistent || view.Eligible {
		t.Fatalf("stale facts became eligible: %+v err=%v", view, err)
	}
	firstReceived := view.ReceivedAt
	receipt, duplicate, err := a.RecordHostCapacityObservation(observerToken, observer.ObserverSessionID, r)
	if err != nil || !duplicate || !receipt.ReceivedAt.Equal(firstReceived) {
		t.Fatalf("identical replay changed historical receipt freshness: receipt=%+v duplicate=%v err=%v", receipt, duplicate, err)
	}
	view, err = a.ReadHostCapacity(observerToken, observer.ObserverSessionID, host.ID)
	if err != nil || view.ObservationState != CapacityStale || view.Eligible {
		t.Fatalf("identical historical replay refreshed stale capacity: %+v err=%v", view, err)
	}
}

func TestHostCapacityObserverAndObservedSessionRevocationAreFinal(t *testing.T) {
	a, owner, host, hostToken, _, _ := capacity21Fixture(t)
	child, _, err := a.Delegate(hostToken, host.ID, host.Scope, time.Now().Add(20*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	observer, observerToken, err := a.EnrollHostCapacityObserver(owner, HostCapacityObserver{HostSessionID: child.ID, HostID: "host-a", Runtime: "codex", Build: "build-1", CapabilityDigest: stringsOf('a', 64), ExpiresAt: time.Now().Add(15 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Revoke(owner, host.ID); err != nil {
		t.Fatal(err)
	}
	request := capacityRequest(child, 1, "capacity-op-parent-revoked")
	if _, _, err := a.RecordHostCapacityObservation(observerToken, observer.ObserverSessionID, request); !errors.Is(err, ErrDenied) {
		t.Fatalf("revoked parent of observed host session remained usable: %v", err)
	}
}
