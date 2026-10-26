package executor

import (
	"bytes"
	"encoding/json"
	"io"
	"time"

	"github.com/mlnomadpy/dacli/internal/governance/sessions"
)

// CapacityReport is a credential-free host adapter payload. Authentication,
// owner enrollment, operation identity, replay protection and persistence stay
// in sessions; the adapter never receives bearer credentials.
type CapacityReport struct {
	SchemaVersion      int       `json:"schema_version"`
	OperationKey       string    `json:"operation_key"`
	HostID             string    `json:"host_id"`
	Runtime            string    `json:"runtime"`
	Build              string    `json:"build"`
	HostSessionID      string    `json:"host_session_id"`
	ExecutorInstanceID string    `json:"executor_instance_id"`
	CapabilityDigest   string    `json:"capability_digest"`
	Sequence           uint64    `json:"sequence"`
	DeclarationState   string    `json:"declaration_state"`
	DeclaredSlots      int       `json:"declared_slots"`
	ObservationState   string    `json:"observation_state"`
	OccupiedSlots      int       `json:"occupied_slots"`
	AvailableSlots     int       `json:"available_slots"`
	ObservedAt         time.Time `json:"observed_at"`
}

// DecodeCapacityReport refuses unknown fields and trailing values so a newer
// producer cannot be silently interpreted by an older adapter.
func DecodeCapacityReport(data []byte) (CapacityReport, error) {
	var report CapacityReport
	if len(data) == 0 || len(data) > 16*1024 {
		return CapacityReport{}, ErrDenied
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&report) != nil || decoder.Decode(new(any)) != io.EOF || report.SchemaVersion != 1 || report.ExecutorInstanceID == "" {
		return CapacityReport{}, ErrDenied
	}
	return report, nil
}

// CapacityFacts preserves the four independent inputs needed for an
// informational positive-evidence classification. It is not dispatch
// admission and has no fallback from one fact to another.
type CapacityFacts struct {
	SchemaVersion          int           `json:"schema_version"`
	ExecutorInstanceID     string        `json:"executor_instance_id"`
	RequestedState         string        `json:"requested_state"`
	DeclarationState       string        `json:"declaration_state"`
	ObservationState       string        `json:"observation_state"`
	QualificationState     string        `json:"qualification_state"`
	ConsistencyState       string        `json:"consistency_state"`
	RequestedSlots         int           `json:"requested_slots"`
	DeclaredSlots          int           `json:"declared_slots"`
	OccupiedSlots          int           `json:"occupied_slots"`
	AvailableSlots         int           `json:"available_slots"`
	QualifiedSlots         int           `json:"qualified_slots"`
	Freshness              time.Duration `json:"freshness"`
	ObservedAt             time.Time     `json:"observed_at"`
	ReceivedAt             time.Time     `json:"received_at"`
	QualificationExpiresAt time.Time     `json:"qualification_expires_at"`
}

func ObservationRequest(report CapacityReport) sessions.HostCapacityObservationRequest {
	return sessions.HostCapacityObservationRequest{
		SchemaVersion: report.SchemaVersion, OperationKey: report.OperationKey,
		HostID: report.HostID, Runtime: report.Runtime, Build: report.Build,
		HostSessionID: report.HostSessionID, CapabilityDigest: report.CapabilityDigest,
		ExecutorInstanceID: report.ExecutorInstanceID,
		Sequence:           report.Sequence, DeclarationState: report.DeclarationState,
		DeclaredSlots: report.DeclaredSlots, ObservedState: report.ObservationState,
		OccupiedSlots: report.OccupiedSlots, AvailableSlots: report.AvailableSlots,
		ObservedAt: report.ObservedAt,
	}
}

// PositiveCapacityEvidence is intentionally separate from any scheduler or
// dispatch authority. Unknown, unsupported, stale, contradictory and
// unqualified inputs never yield positive evidence.
func PositiveCapacityEvidence(f CapacityFacts, now time.Time) bool {
	if f.SchemaVersion != 1 || f.ExecutorInstanceID == "" || f.RequestedState != "known" || f.DeclarationState != "known" || f.ObservationState != "known" || f.QualificationState != "known" || f.ConsistencyState != "consistent" || f.RequestedSlots <= 0 || f.DeclaredSlots < 0 || f.OccupiedSlots < 0 || f.AvailableSlots < 0 || f.QualifiedSlots < 0 || f.Freshness <= 0 || f.ObservedAt.IsZero() || f.ReceivedAt.IsZero() || f.ObservedAt.After(f.ReceivedAt) || now.Before(f.ObservedAt) || now.Before(f.ReceivedAt) || now.Sub(f.ReceivedAt) > f.Freshness || now.Sub(f.ObservedAt) > f.Freshness {
		return false
	}
	if f.OccupiedSlots > int(^uint(0)>>1)-f.AvailableSlots || f.OccupiedSlots+f.AvailableSlots != f.DeclaredSlots {
		return false
	}
	if !f.QualificationExpiresAt.IsZero() && !now.Before(f.QualificationExpiresAt) {
		return false
	}
	return f.AvailableSlots >= f.RequestedSlots && f.QualifiedSlots >= f.RequestedSlots
}

// HostCapacityObservationBinding and HostCapacityCensusV2 are aliases so the
// report payload is passed to sessions without a lossy or hand-maintained copy.
type HostCapacityObservationBinding = sessions.HostSlotObservationBinding
type HostCapacityCensusV2 = sessions.HostCapacityCensus

// CapacityReportV2 is the strict schema-2 observer payload. It deliberately
// has a separate DTO from CapacityReport so an older report cannot absorb
// census fields or silently reinterpret a newer schema.
type CapacityReportV2 struct {
	SchemaVersion      int                   `json:"schema_version"`
	OperationKey       string                `json:"operation_key"`
	HostID             string                `json:"host_id"`
	Runtime            string                `json:"runtime"`
	Build              string                `json:"build"`
	HostSessionID      string                `json:"host_session_id"`
	ExecutorInstanceID string                `json:"executor_instance_id"`
	CapabilityDigest   string                `json:"capability_digest"`
	Sequence           uint64                `json:"sequence"`
	DeclarationState   string                `json:"declaration_state"`
	DeclaredSlots      int                   `json:"declared_slots"`
	ObservationState   string                `json:"observation_state"`
	OccupiedSlots      int                   `json:"occupied_slots"`
	AvailableSlots     int                   `json:"available_slots"`
	ObservedAt         time.Time             `json:"observed_at"`
	Census             *HostCapacityCensusV2 `json:"census"`
}

// DecodeCapacityReportV2 refuses unknown fields, trailing values, absent
// census and noncanonical inclusion lists. Exact authority/dispatch binding
// checks occur after authentication in the sessions adapter.
func DecodeCapacityReportV2(data []byte) (CapacityReportV2, error) {
	var report CapacityReportV2
	if len(data) == 0 || len(data) > 16*1024 || hasDuplicateJSONKeys(data) {
		return CapacityReportV2{}, ErrDenied
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&report) != nil || decoder.Decode(new(any)) != io.EOF || report.SchemaVersion != 2 || report.ExecutorInstanceID == "" || !validCapacityCounts(report.DeclaredSlots, report.OccupiedSlots, report.AvailableSlots) || report.Census == nil || report.Census.SchemaVersion != 1 || report.Census.AuthorityDomainID == "" || report.Census.IncludedLocalExecutions == nil {
		return CapacityReportV2{}, ErrDenied
	}
	seenDispatches := make(map[string]struct{}, len(report.Census.IncludedLocalExecutions))
	seenIntents := make(map[string]struct{}, len(report.Census.IncludedLocalExecutions))
	seenKeys := make(map[string]struct{}, len(report.Census.IncludedLocalExecutions))
	for _, binding := range report.Census.IncludedLocalExecutions {
		if binding.DispatchID == "" || binding.ReservationFence == 0 || binding.IntentID == "" || binding.OriginalOperationKey == "" || binding.HostAgentID == "" {
			return CapacityReportV2{}, ErrDenied
		}
		if _, ok := seenDispatches[binding.DispatchID]; ok {
			return CapacityReportV2{}, ErrDenied
		}
		if _, ok := seenIntents[binding.IntentID]; ok {
			return CapacityReportV2{}, ErrDenied
		}
		if _, ok := seenKeys[binding.OriginalOperationKey]; ok {
			return CapacityReportV2{}, ErrDenied
		}
		seenDispatches[binding.DispatchID] = struct{}{}
		seenIntents[binding.IntentID] = struct{}{}
		seenKeys[binding.OriginalOperationKey] = struct{}{}
	}
	return report, nil
}

// ObservationRequestV2 preserves every census identity for the authority's
// separate schema-2 digest and authenticated binding checks. Callers must use a
// report returned by DecodeCapacityReportV2.
func ObservationRequestV2(report CapacityReportV2) sessions.HostCapacityObservationRequestV2 {
	census := sessions.HostCapacityCensus{}
	if report.Census != nil {
		census = *report.Census
	}
	return sessions.HostCapacityObservationRequestV2{
		HostCapacityObservationRequest: sessions.HostCapacityObservationRequest{
			SchemaVersion: report.SchemaVersion, OperationKey: report.OperationKey,
			HostID: report.HostID, Runtime: report.Runtime, Build: report.Build,
			HostSessionID: report.HostSessionID, CapabilityDigest: report.CapabilityDigest,
			ExecutorInstanceID: report.ExecutorInstanceID, Sequence: report.Sequence,
			DeclarationState: report.DeclarationState, DeclaredSlots: report.DeclaredSlots,
			ObservedState: report.ObservationState, OccupiedSlots: report.OccupiedSlots,
			AvailableSlots: report.AvailableSlots, ObservedAt: report.ObservedAt,
		},
		Census: census,
	}
}

// hasDuplicateJSONKeys prevents a duplicate schema_version or census field
// from being interpreted differently by independent JSON readers.
func hasDuplicateJSONKeys(data []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if !scanJSONValue(decoder) {
		return true
	}
	_, err := decoder.Token()
	return err != io.EOF
}

func scanJSONValue(decoder *json.Decoder) bool {
	token, err := decoder.Token()
	if err != nil {
		return false
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return true
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			key, ok := keyToken.(string)
			if err != nil || !ok {
				return false
			}
			if _, exists := seen[key]; exists {
				return false
			}
			seen[key] = struct{}{}
			if !scanJSONValue(decoder) {
				return false
			}
		}
		closing, err := decoder.Token()
		return err == nil && closing == json.Delim('}')
	case '[':
		for decoder.More() {
			if !scanJSONValue(decoder) {
				return false
			}
		}
		closing, err := decoder.Token()
		return err == nil && closing == json.Delim(']')
	default:
		return false
	}
}

func validCapacityCounts(declared, occupied, available int) bool {
	if declared < 0 || occupied < 0 || available < 0 || occupied > int(^uint(0)>>1)-available {
		return false
	}
	return occupied+available == declared
}
