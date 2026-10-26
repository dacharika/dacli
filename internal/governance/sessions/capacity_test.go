package sessions

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
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
	observer, observerToken, err := current.EnrollHostCapacityObserver(owner, HostCapacityObserver{SchemaVersion: 1, HostSessionID: host.ID, ExecutorInstanceID: "executor-1", HostID: "host-a", Runtime: "codex", Build: "build-1", CapabilityDigest: stringsOf('a', 64), ExpiresAt: time.Now().Add(30 * time.Minute)})
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
	return HostCapacityObservationRequest{SchemaVersion: 1, OperationKey: key, HostID: "host-a", Runtime: "codex", Build: "build-1", HostSessionID: host.ID, ExecutorInstanceID: "executor-1", CapabilityDigest: stringsOf('a', 64), Sequence: seq, DeclarationState: CapacityKnown, DeclaredSlots: 2, ObservedState: CapacityKnown, OccupiedSlots: 1, AvailableSlots: 1, ObservedAt: time.Now().Add(-time.Second).UTC()}
}

func fillFixtureValue(v reflect.Value, depth int) {
	if !v.CanSet() || depth > 6 {
		return
	}
	if v.Type() == reflect.TypeOf(time.Time{}) {
		v.Set(reflect.ValueOf(time.Now().UTC()))
		return
	}
	if v.Type() == reflect.TypeOf(json.RawMessage{}) {
		v.SetBytes([]byte(`{"fixture":true}`))
		return
	}
	switch v.Kind() {
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			fillFixtureValue(v.Field(i), depth+1)
		}
	case reflect.Map:
		m := reflect.MakeMap(v.Type())
		key := reflect.New(v.Type().Key()).Elem()
		fillFixtureValue(key, depth+1)
		val := reflect.New(v.Type().Elem()).Elem()
		fillFixtureValue(val, depth+1)
		m.SetMapIndex(key, val)
		v.Set(m)
	case reflect.Slice:
		s := reflect.MakeSlice(v.Type(), 1, 1)
		fillFixtureValue(s.Index(0), depth+1)
		v.Set(s)
	case reflect.Pointer:
		p := reflect.New(v.Type().Elem())
		fillFixtureValue(p.Elem(), depth+1)
		v.Set(p)
	case reflect.Interface:
	case reflect.String:
		v.SetString("fixture-record")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(1)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(1)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(1)
	}
}

func populateLegacyCollectionFixture(t *testing.T, a *Authority) {
	t.Helper()
	if err := a.transaction(false, true, func(s *state) error {
		v := reflect.ValueOf(s).Elem()
		for i := 0; i < v.NumField(); i++ {
			field := v.Field(i)
			name := v.Type().Field(i).Name
			if field.Kind() != reflect.Map || strings.HasPrefix(name, "Capacity") {
				continue
			}
			if field.Len() == 0 {
				key := reflect.New(field.Type().Key()).Elem()
				fillFixtureValue(key, 0)
				value := reflect.New(field.Type().Elem()).Elem()
				fillFixtureValue(value, 0)
				field.SetMapIndex(key, value)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func assertLegacyCollectionCoverage(t *testing.T, s state) {
	t.Helper()
	v := reflect.ValueOf(s)
	for i := 0; i < v.NumField(); i++ {
		field := v.Field(i)
		name := v.Type().Field(i).Name
		if field.Kind() == reflect.Map && !strings.HasPrefix(name, "Capacity") && field.Len() == 0 {
			t.Errorf("schema-20 preservation fixture left legacy collection %s empty", name)
		}
	}
	roleWithNestedHistory := false
	for _, dispatch := range s.Dispatches {
		for _, role := range dispatch.RoleRegistry {
			if role.RoleID != "" && role.Version != "" && len(role.PolicyReferences) > 0 && role.PolicyReferences[0].Kind != "" {
				roleWithNestedHistory = true
				break
			}
		}
	}
	if !roleWithNestedHistory {
		t.Error("schema-20 fixture lacks nested role-registry provenance in retained dispatch history")
	}
}

func TestHostCapacitySchema21UpgradePreservesSchema20AndOldReaderRefuses(t *testing.T) {
	a, owner, _ := authorityGrowthFixture(t, 2)
	if err := a.UpgradeNativeRoleRegistry(owner); err != nil {
		t.Fatal(err)
	}
	populateLegacyCollectionFixture(t, a)
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
	assertLegacyCollectionCoverage(t, before)
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
		"host":     func(r *HostCapacityObservationRequest) { r.HostID = "host-b" },
		"instance": func(r *HostCapacityObservationRequest) { r.ExecutorInstanceID = "executor-replacement" },
		"runtime":  func(r *HostCapacityObservationRequest) { r.Runtime = "other-runtime" },
		"build":    func(r *HostCapacityObservationRequest) { r.Build = "other-build" },
		"session":  func(r *HostCapacityObservationRequest) { r.HostSessionID = "other-session" },
		"digest":   func(r *HostCapacityObservationRequest) { r.CapabilityDigest = stringsOf('b', 64) },
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

func TestHostCapacityReplacementInstanceDoesNotInheritOldFacts(t *testing.T) {
	a, owner, host, _, original, originalToken := capacity21Fixture(t)
	policy := HostCapacityPolicy{SchemaVersion: 1, WorkspaceID: a.binding.Workspace, InstallationID: a.binding.Installation, HostID: "host-a", Runtime: "codex", Build: "build-1", HostSessionID: host.ID, ExecutorInstanceID: "executor-1", CapabilityDigest: stringsOf('a', 64), State: CapacityKnown, RequestedSlots: 1, Freshness: time.Minute, Revision: "replacement-test"}
	if _, err := a.ConfigureHostCapacityPolicy(owner, policy); err != nil {
		t.Fatal(err)
	}
	qualification := HostCapacityQualification{SchemaVersion: 1, HostSessionID: host.ID, ExecutorInstanceID: "executor-1", HostID: "host-a", Runtime: "codex", Build: "build-1", CapabilityDigest: stringsOf('a', 64), State: CapacityKnown, QualifiedSlots: 1, EvidenceDigest: stringsOf('b', 64), QualificationID: "old-instance-qualification", ExpiresAt: time.Now().Add(time.Minute)}
	if _, err := a.QualifyHostCapacity(owner, qualification); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.RecordHostCapacityObservation(originalToken, original.ObserverSessionID, capacityRequest(host, 1, "old-instance-observation")); err != nil {
		t.Fatal(err)
	}
	replacement, replacementToken, err := a.EnrollHostCapacityObserver(owner, HostCapacityObserver{SchemaVersion: 1, HostSessionID: host.ID, ExecutorInstanceID: "executor-2", HostID: "host-a", Runtime: "codex", Build: "build-1", CapabilityDigest: stringsOf('a', 64), ExpiresAt: time.Now().Add(20 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	view, err := a.ReadHostCapacity(replacementToken, replacement.ObserverSessionID, host.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.ExecutorInstanceID != "executor-2" || view.Policy.State != CapacityUnknown || view.QualificationState != CapacityUnknown || view.ObservationState != CapacityUnknown || view.Eligible {
		t.Fatalf("replacement inherited old instance facts: %+v", view)
	}
	// The old observer remains bound to its exact process identity and retains
	// its own coherent history; that history is not visible to the replacement.
	oldView, err := a.ReadHostCapacity(originalToken, original.ObserverSessionID, host.ID)
	if err != nil || oldView.ExecutorInstanceID != "executor-1" || !oldView.Eligible {
		t.Fatalf("original instance facts changed unexpectedly: view=%+v err=%v", oldView, err)
	}
}

func TestHostCapacityFutureOwnerVersionsRefusedWithoutStateChange(t *testing.T) {
	a, owner, host, _, _, _ := capacity21Fixture(t)
	before, err := os.ReadFile(a.path)
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []int{0, 2} {
		observer := HostCapacityObserver{SchemaVersion: version, HostSessionID: host.ID, ExecutorInstanceID: "executor-1", HostID: "host-a", Runtime: "codex", Build: "build-1", CapabilityDigest: stringsOf('a', 64), ExpiresAt: time.Now().Add(10 * time.Minute)}
		if _, _, err := a.EnrollHostCapacityObserver(owner, observer); !errors.Is(err, ErrDenied) {
			t.Fatalf("observer schema_version=%d contract accepted: %v", version, err)
		}
		policy := HostCapacityPolicy{SchemaVersion: version, WorkspaceID: a.binding.Workspace, InstallationID: a.binding.Installation, HostID: "host-a", Runtime: "codex", Build: "build-1", HostSessionID: host.ID, ExecutorInstanceID: "executor-1", CapabilityDigest: stringsOf('a', 64), State: CapacityKnown, RequestedSlots: 1, Freshness: time.Minute, Revision: "future-policy"}
		if _, err := a.ConfigureHostCapacityPolicy(owner, policy); !errors.Is(err, ErrDenied) {
			t.Fatalf("policy schema_version=%d contract accepted: %v", version, err)
		}
		qualification := HostCapacityQualification{SchemaVersion: version, HostSessionID: host.ID, ExecutorInstanceID: "executor-1", HostID: "host-a", Runtime: "codex", Build: "build-1", CapabilityDigest: stringsOf('a', 64), State: CapacityKnown, QualifiedSlots: 1, EvidenceDigest: stringsOf('b', 64), QualificationID: "future-qualification"}
		if _, err := a.QualifyHostCapacity(owner, qualification); !errors.Is(err, ErrDenied) {
			t.Fatalf("qualification schema_version=%d contract accepted: %v", version, err)
		}
	}
	after, err := os.ReadFile(a.path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("future owner contracts changed persisted authority bytes")
	}
}

func TestHostCapacityMissingPolicyIsTypedUnknown(t *testing.T) {
	a, _, host, _, observer, observerToken := capacity21Fixture(t)
	view, err := a.ReadHostCapacity(observerToken, observer.ObserverSessionID, host.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Policy.SchemaVersion != 1 || view.Policy.State != CapacityUnknown || view.Policy.RequestedSlots != 0 || view.Eligible {
		t.Fatalf("missing requested policy lacks typed unknown: %+v", view)
	}
}

func TestHostCapacityExpiredQualificationIsStaleAndNotEligible(t *testing.T) {
	a, owner, host, _, observer, observerToken := capacity21Fixture(t)
	if _, err := a.ConfigureHostCapacityPolicy(owner, HostCapacityPolicy{SchemaVersion: 1, WorkspaceID: a.binding.Workspace, InstallationID: a.binding.Installation, HostID: "host-a", Runtime: "codex", Build: "build-1", HostSessionID: host.ID, ExecutorInstanceID: "executor-1", CapabilityDigest: stringsOf('a', 64), State: CapacityKnown, RequestedSlots: 1, Freshness: time.Minute, Revision: "expiry-policy"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.QualifyHostCapacity(owner, HostCapacityQualification{SchemaVersion: 1, HostSessionID: host.ID, ExecutorInstanceID: "executor-1", HostID: "host-a", Runtime: "codex", Build: "build-1", CapabilityDigest: stringsOf('a', 64), State: CapacityKnown, QualifiedSlots: 1, EvidenceDigest: stringsOf('b', 64), QualificationID: "expiry-qualification", ExpiresAt: time.Now().Add(10 * time.Millisecond)}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.RecordHostCapacityObservation(observerToken, observer.ObserverSessionID, capacityRequest(host, 1, "expiry-observation")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	view, err := a.ReadHostCapacity(observerToken, observer.ObserverSessionID, host.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.QualificationState != CapacityStale || view.Eligible {
		t.Fatalf("expired qualification remained current: %+v", view)
	}
}

func TestHostCapacitySchema21StrictFieldsAndMigrationSourceShape(t *testing.T) {
	t.Run("schema20 nested future field refuses migration without rewrite", func(t *testing.T) {
		a, owner, _ := authorityGrowthFixture(t, 1)
		if err := a.UpgradeNativeRoleRegistry(owner); err != nil {
			t.Fatal(err)
		}
		before, err := os.ReadFile(a.path)
		if err != nil {
			t.Fatal(err)
		}
		var top map[string]json.RawMessage
		if err := json.Unmarshal(before, &top); err != nil {
			t.Fatal(err)
		}
		var tasks map[string]json.RawMessage
		if err := json.Unmarshal(top["Tasks"], &tasks); err != nil || len(tasks) == 0 {
			t.Fatalf("fixture task bindings missing: err=%v count=%d", err, len(tasks))
		}
		for id, raw := range tasks {
			var task map[string]json.RawMessage
			if err := json.Unmarshal(raw, &task); err != nil {
				t.Fatal(err)
			}
			task["future_security_guard"] = json.RawMessage(`true`)
			tasks[id], _ = json.Marshal(task)
			break
		}
		top["Tasks"], _ = json.Marshal(tasks)
		unknown, _ := json.Marshal(top)
		if err := os.WriteFile(a.path, unknown, 0600); err != nil {
			t.Fatal(err)
		}
		if err := a.UpgradeHostCapacity(owner); !errors.Is(err, ErrStorage) {
			t.Fatalf("schema-20 migration accepted nested unknown source field: %v", err)
		}
		after, err := os.ReadFile(a.path)
		if err != nil || !reflect.DeepEqual(unknown, after) {
			t.Fatalf("refused migration changed source bytes: err=%v", err)
		}
	})
	for _, nested := range []bool{false, true} {
		name := "top-level"
		if nested {
			name = "observer-nested"
		}
		t.Run("schema21 "+name+" future field refuses owner write without rewrite", func(t *testing.T) {
			a, owner, host, _, observer, _ := capacity21Fixture(t)
			_ = host
			before, err := os.ReadFile(a.path)
			if err != nil {
				t.Fatal(err)
			}
			var top map[string]json.RawMessage
			if err := json.Unmarshal(before, &top); err != nil {
				t.Fatal(err)
			}
			if nested {
				var observers map[string]json.RawMessage
				if err := json.Unmarshal(top["CapacityObservers"], &observers); err != nil {
					t.Fatal(err)
				}
				raw := observers[observer.ObserverSessionID]
				var record map[string]json.RawMessage
				if err := json.Unmarshal(raw, &record); err != nil {
					t.Fatal(err)
				}
				record["future_security_guard"] = json.RawMessage(`true`)
				observers[observer.ObserverSessionID], _ = json.Marshal(record)
				top["CapacityObservers"], _ = json.Marshal(observers)
			} else {
				top["FutureCapacityGuard"] = json.RawMessage(`true`)
			}
			unknown, _ := json.Marshal(top)
			if err := os.WriteFile(a.path, unknown, 0600); err != nil {
				t.Fatal(err)
			}
			if err := a.Revoke(owner, observer.ObserverSessionID); !errors.Is(err, ErrStorage) {
				t.Fatalf("schema-21 owner write accepted unknown %s field: %v", name, err)
			}
			after, err := os.ReadFile(a.path)
			if err != nil || !reflect.DeepEqual(unknown, after) {
				t.Fatalf("refused owner write changed unknown state bytes: err=%v", err)
			}
		})
	}
}

func TestHostCapacityCurrentReaderRejectsFutureVersionAndMissingCollection(t *testing.T) {
	t.Run("future version", func(t *testing.T) {
		a, _, _, _, _, _ := capacity21Fixture(t)
		raw, err := os.ReadFile(a.path)
		if err != nil {
			t.Fatal(err)
		}
		var top map[string]json.RawMessage
		_ = json.Unmarshal(raw, &top)
		top["Version"] = json.RawMessage(`22`)
		future, _ := json.Marshal(top)
		if err := os.WriteFile(a.path, future, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenCurrent(a.path, a.binding); !errors.Is(err, ErrStorage) {
			t.Fatalf("current reader accepted future schema: %v", err)
		}
	})
	t.Run("missing required capacity collection", func(t *testing.T) {
		a, _, _, _, _, _ := capacity21Fixture(t)
		raw, err := os.ReadFile(a.path)
		if err != nil {
			t.Fatal(err)
		}
		var top map[string]json.RawMessage
		_ = json.Unmarshal(raw, &top)
		delete(top, "CapacityQualifications")
		missing, _ := json.Marshal(top)
		if err := os.WriteFile(a.path, missing, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenHostCapacity(a.path, a.binding); !errors.Is(err, ErrStorage) {
			t.Fatalf("current reader accepted missing required schema-21 collection: %v", err)
		}
	})
}

func TestHostCapacityFactsRequireAllIndependentPositiveEvidence(t *testing.T) {
	a, owner, host, _, observer, observerToken := capacity21Fixture(t)
	policy := HostCapacityPolicy{SchemaVersion: 1, WorkspaceID: a.binding.Workspace, InstallationID: a.binding.Installation, HostID: "host-a", Runtime: "codex", Build: "build-1", HostSessionID: host.ID, ExecutorInstanceID: "executor-1", CapabilityDigest: stringsOf('a', 64), State: CapacityKnown, RequestedSlots: 1, Freshness: time.Minute, Revision: "request-v1"}
	if _, err := a.ConfigureHostCapacityPolicy(owner, policy); err != nil {
		t.Fatal(err)
	}
	if _, err := a.QualifyHostCapacity(owner, HostCapacityQualification{SchemaVersion: 1, HostSessionID: host.ID, ExecutorInstanceID: "executor-1", HostID: "host-a", Runtime: "codex", Build: "build-1", CapabilityDigest: stringsOf('a', 64), State: CapacityKnown, QualifiedSlots: 1, EvidenceDigest: stringsOf('b', 64), QualificationID: "qual-v1", ExpiresAt: time.Now().Add(time.Minute)}); err != nil {
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
	if _, err := a.QualifyHostCapacity(owner, HostCapacityQualification{SchemaVersion: 1, HostSessionID: host.ID, ExecutorInstanceID: "executor-1", HostID: "host-a", Runtime: "codex", Build: "build-1", CapabilityDigest: stringsOf('a', 64), State: CapacityUnknown, QualificationID: "qualification-unknown"}); err != nil {
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
	_, err := a.ConfigureHostCapacityPolicy(owner, HostCapacityPolicy{SchemaVersion: 1, WorkspaceID: a.binding.Workspace, InstallationID: a.binding.Installation, HostID: "host-a", Runtime: "codex", Build: "build-1", HostSessionID: host.ID, ExecutorInstanceID: "executor-1", CapabilityDigest: stringsOf('a', 64), State: CapacityKnown, RequestedSlots: 1, Freshness: time.Minute, Revision: "policy-v1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.QualifyHostCapacity(owner, HostCapacityQualification{SchemaVersion: 1, HostSessionID: host.ID, ExecutorInstanceID: "executor-1", HostID: "host-a", Runtime: "codex", Build: "build-1", CapabilityDigest: stringsOf('a', 64), State: CapacityKnown, QualifiedSlots: 1, EvidenceDigest: stringsOf('b', 64), QualificationID: "qual-v1"}); err != nil {
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
	observer, observerToken, err := a.EnrollHostCapacityObserver(owner, HostCapacityObserver{SchemaVersion: 1, HostSessionID: child.ID, ExecutorInstanceID: "executor-1", HostID: "host-a", Runtime: "codex", Build: "build-1", CapabilityDigest: stringsOf('a', 64), ExpiresAt: time.Now().Add(15 * time.Minute)})
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
