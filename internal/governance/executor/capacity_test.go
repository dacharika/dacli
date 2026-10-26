package executor

import (
	"strings"
	"testing"
	"time"
)

func TestDecodeCapacityReportRejectsUnknownFields(t *testing.T) {
	good := `{"schema_version":1,"operation_key":"op-1","host_id":"host-a","runtime":"codex","build":"b1","host_session_id":"session-a","executor_instance_id":"executor-1","capability_digest":"` + strings.Repeat("a", 64) + `","sequence":1,"declaration_state":"known","declared_slots":2,"observation_state":"known","occupied_slots":1,"available_slots":1,"observed_at":"2026-10-26T19:00:00Z"}`
	if _, err := DecodeCapacityReport([]byte(good)); err != nil {
		t.Fatalf("valid report refused: %v", err)
	}
	unknown := strings.TrimSuffix(good, "}") + `,"qualified":true}`
	if _, err := DecodeCapacityReport([]byte(unknown)); err == nil {
		t.Fatal("unknown qualification field was silently accepted")
	}
	withoutInstance := strings.Replace(good, `,"executor_instance_id":"executor-1"`, "", 1)
	if _, err := DecodeCapacityReport([]byte(withoutInstance)); err == nil {
		t.Fatal("report without an observed executor instance was accepted")
	}
}

func TestPositiveCapacityEvidenceRequiresFourFreshKnownFacts(t *testing.T) {
	now := time.Now().UTC()
	facts := CapacityFacts{SchemaVersion: 1, ExecutorInstanceID: "executor-1", RequestedState: "known", DeclarationState: "known", ObservationState: "known", QualificationState: "known", ConsistencyState: "consistent", RequestedSlots: 1, DeclaredSlots: 2, OccupiedSlots: 1, AvailableSlots: 1, QualifiedSlots: 1, Freshness: time.Minute, ObservedAt: now.Add(-time.Second), ReceivedAt: now.Add(-time.Second)}
	if !PositiveCapacityEvidence(facts, now) {
		t.Fatal("four fresh known facts did not produce positive informational evidence")
	}
	for name, mutate := range map[string]func(*CapacityFacts){
		"source-after-receipt": func(f *CapacityFacts) { f.ObservedAt = f.ReceivedAt.Add(time.Second) },
		"unknown":              func(f *CapacityFacts) { f.ObservationState = "unknown" },
		"unsupported":          func(f *CapacityFacts) { f.DeclarationState = "unsupported" },
		"unqualified":          func(f *CapacityFacts) { f.QualificationState = "unknown" },
		"stale":                func(f *CapacityFacts) { f.ReceivedAt = now.Add(-time.Hour) },
		"contradictory":        func(f *CapacityFacts) { f.DeclaredSlots = 3 },
	} {
		t.Run(name, func(t *testing.T) {
			changed := facts
			mutate(&changed)
			if PositiveCapacityEvidence(changed, now) {
				t.Fatalf("%s facts produced positive evidence", name)
			}
		})
	}
}
