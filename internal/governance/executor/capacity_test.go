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

func TestDecodeCapacityReportV2RequiresExplicitBoundCensus(t *testing.T) {
	base := `{"schema_version":2,"operation_key":"op-2","host_id":"host-a","runtime":"codex","build":"b1","host_session_id":"session-a","executor_instance_id":"executor-1","capability_digest":"` + strings.Repeat("a", 64) + `","sequence":2,"declaration_state":"known","declared_slots":2,"observation_state":"known","occupied_slots":1,"available_slots":1,"observed_at":"2026-10-26T19:00:00Z","census":{"schema_version":1,"authority_domain_id":"domain-a","included_local_executions":[]}}`
	if report, err := DecodeCapacityReportV2([]byte(base)); err != nil || report.Census == nil || report.Census.IncludedLocalExecutions == nil {
		t.Fatalf("explicit empty census refused or collapsed to absent: report=%+v err=%v", report, err)
	}
	knownZero := strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(base, `"declared_slots":2`, `"declared_slots":0`), `"occupied_slots":1`, `"occupied_slots":0`), `"available_slots":1`, `"available_slots":0`)
	if _, err := DecodeCapacityReportV2([]byte(knownZero)); err != nil {
		t.Fatalf("explicit zero measurements were refused: %v", err)
	}
	cases := map[string]string{
		"schema1":               strings.Replace(base, `"schema_version":2`, `"schema_version":1`, 1),
		"unknown-census-schema": strings.Replace(base, `"schema_version":1,"authority_domain_id"`, `"schema_version":2,"authority_domain_id"`, 1),
		"missing-census":        strings.Replace(base, `,"census":{"schema_version":1,"authority_domain_id":"domain-a","included_local_executions":[]}`, "", 1),
		"null-census-list":      strings.Replace(base, `"included_local_executions":[]`, `"included_local_executions":null`, 1),
		"bad-counts":            strings.Replace(base, `"available_slots":1`, `"available_slots":2`, 1),
		"negative-count":        strings.Replace(base, `"occupied_slots":1`, `"occupied_slots":-1`, 1),
		"negative-declaration":  strings.Replace(base, `"declared_slots":2`, `"declared_slots":-2`, 1),
		"missing-declaration":   strings.Replace(strings.Replace(strings.Replace(base, `,"declared_slots":2`, "", 1), `"occupied_slots":1`, `"occupied_slots":0`, 1), `"available_slots":1`, `"available_slots":0`, 1),
		"null-declaration":      strings.Replace(strings.Replace(strings.Replace(base, `"declared_slots":2`, `"declared_slots":null`, 1), `"occupied_slots":1`, `"occupied_slots":0`, 1), `"available_slots":1`, `"available_slots":0`, 1),
		"missing-occupied":      strings.Replace(strings.Replace(base, `,"occupied_slots":1`, "", 1), `"declared_slots":2`, `"declared_slots":1`, 1),
		"null-occupied":         strings.Replace(strings.Replace(base, `"occupied_slots":1`, `"occupied_slots":null`, 1), `"declared_slots":2`, `"declared_slots":1`, 1),
		"missing-available":     strings.Replace(strings.Replace(base, `,"available_slots":1`, "", 1), `"declared_slots":2`, `"declared_slots":1`, 1),
		"null-available":        strings.Replace(strings.Replace(base, `"available_slots":1`, `"available_slots":null`, 1), `"declared_slots":2`, `"declared_slots":1`, 1),
		"overflow-count":        strings.Replace(base, `"occupied_slots":1`, `"occupied_slots":9223372036854775807`, 1),
		"unknown-field":         strings.Replace(base, `,"census":`, `,"future":true,"census":`, 1),
		"duplicate-version":     strings.Replace(base, `"schema_version":2`, `"schema_version":2,"schema_version":1`, 1),
		"duplicate-census":      strings.Replace(base, `"authority_domain_id":"domain-a"`, `"authority_domain_id":"domain-a","authority_domain_id":"domain-b"`, 1),
		"trailing-value":        base + `{}`,
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeCapacityReportV2([]byte(input)); err == nil {
				t.Fatalf("invalid schema-2 report accepted: %s", name)
			}
		})
	}
}

func TestDecodeCapacityReportV2RejectsDuplicateCensusIdentity(t *testing.T) {
	binding := `{"dispatch_id":"dispatch-a","reservation_fence":7,"intent_id":"intent-a","original_operation_key":"operation-a","host_agent_id":"host-agent-a"}`
	base := `{"schema_version":2,"operation_key":"op-2","host_id":"host-a","runtime":"codex","build":"b1","host_session_id":"session-a","executor_instance_id":"executor-1","capability_digest":"` + strings.Repeat("a", 64) + `","sequence":2,"declaration_state":"known","declared_slots":2,"observation_state":"known","occupied_slots":1,"available_slots":1,"observed_at":"2026-10-26T19:00:00Z","census":{"schema_version":1,"authority_domain_id":"domain-a","included_local_executions":[` + binding + `]}}`
	if _, err := DecodeCapacityReportV2([]byte(base)); err != nil {
		t.Fatalf("single exact census binding refused: %v", err)
	}
	duplicate := strings.Replace(base, `[`+binding+`]`, `[`+binding+`,`+binding+`]`, 1)
	if _, err := DecodeCapacityReportV2([]byte(duplicate)); err == nil {
		t.Fatal("duplicate local execution binding accepted")
	}
	missingIdentity := strings.Replace(base, `"host_agent_id":"host-agent-a"`, `"host_agent_id":""`, 1)
	if _, err := DecodeCapacityReportV2([]byte(missingIdentity)); err == nil {
		t.Fatal("incomplete local execution binding accepted")
	}
}

func TestObservationRequestV2CarriesAllCensusBindings(t *testing.T) {
	report := CapacityReportV2{
		SchemaVersion: 2, OperationKey: "capacity-op", HostID: "host-a", Runtime: "codex", Build: "b1",
		HostSessionID: "host-session", ExecutorInstanceID: "executor-1", CapabilityDigest: strings.Repeat("a", 64),
		Sequence: 4, DeclarationState: "known", DeclaredSlots: 3, ObservationState: "known", OccupiedSlots: 2,
		AvailableSlots: 1, ObservedAt: time.Date(2026, 10, 26, 19, 0, 0, 0, time.UTC),
		Census: &HostCapacityCensusV2{SchemaVersion: 1, AuthorityDomainID: "domain-a", IncludedLocalExecutions: []HostCapacityObservationBinding{{DispatchID: "dispatch-a", ReservationFence: 8, IntentID: "intent-a", OriginalOperationKey: "original-op-a", HostAgentID: "host-agent-a"}}},
	}
	req := ObservationRequestV2(report)
	if req.SchemaVersion != 2 || req.OperationKey != report.OperationKey || req.HostSessionID != report.HostSessionID || req.ExecutorInstanceID != report.ExecutorInstanceID || req.Sequence != report.Sequence || req.Census.SchemaVersion != 1 || req.Census.AuthorityDomainID != "domain-a" || len(req.Census.IncludedLocalExecutions) != 1 {
		t.Fatalf("schema-2 observation request lost report or census identity: %+v", req)
	}
	if req.Census.IncludedLocalExecutions[0] != report.Census.IncludedLocalExecutions[0] {
		t.Fatalf("schema-2 observation request altered binding: got %+v want %+v", req.Census.IncludedLocalExecutions[0], report.Census.IncludedLocalExecutions[0])
	}
}
