package sessions

import (
	"math"
	"strings"
	"time"
)

const (
	HostSlotManual          = "manual"
	HostSlotSingleAuthority = "single_authority"
)

// HostSlotTerminalQualification is an owner-attested, expiring declaration
// that this lifecycle source can report supported terminal states for one
// exact host execution scope. It is not live host qualification.
type HostSlotTerminalQualification struct {
	SchemaVersion      int       `json:"schema_version"`
	ID                 string    `json:"id"`
	State              string    `json:"state"`
	Source             string    `json:"source"`
	EvidenceDigest     string    `json:"evidence_digest"`
	AuthorityDomainID  string    `json:"authority_domain_id"`
	HostID             string    `json:"host_id"`
	HostSessionID      string    `json:"host_session_id"`
	ExecutorInstanceID string    `json:"executor_instance_id"`
	Runtime            string    `json:"runtime"`
	Build              string    `json:"build"`
	CapabilityDigest   string    `json:"capability_digest"`
	QualifiedAt        time.Time `json:"qualified_at"`
	ExpiresAt          time.Time `json:"expires_at"`
}

// HostSlotAdmissionPolicy selects a cooperative, single-authority execution
// domain. It is not a global host lock or an OS attestation.
type HostSlotAdmissionPolicy struct {
	SchemaVersion             int                            `json:"schema_version"`
	Mode                      string                         `json:"mode"`
	Revision                  string                         `json:"revision"`
	AuthorityDomainID         string                         `json:"authority_domain_id"`
	WorkspaceID               string                         `json:"workspace_id"`
	InstallationID            string                         `json:"installation_id"`
	HostID                    string                         `json:"host_id"`
	HostSessionID             string                         `json:"host_session_id"`
	ExecutorInstanceID        string                         `json:"executor_instance_id"`
	Runtime                   string                         `json:"runtime"`
	Build                     string                         `json:"build"`
	CapabilityDigest          string                         `json:"capability_digest"`
	CapacityPolicyRevision    string                         `json:"capacity_policy_revision"`
	QualificationID           string                         `json:"qualification_id"`
	TerminalObservationSource string                         `json:"terminal_observation_source"`
	TerminalQualificationID   string                         `json:"terminal_qualification_id"`
	TerminalQualification     *HostSlotTerminalQualification `json:"terminal_qualification,omitempty"`
}

// HostSlotClaim is attached to its original dispatch; DispatchID is its only
// identity and no separate allocation lifecycle is created.
type HostSlotClaim struct {
	SchemaVersion               int    `json:"schema_version"`
	AuthorityDomainID           string `json:"authority_domain_id"`
	HostID                      string `json:"host_id"`
	HostSessionID               string `json:"host_session_id"`
	ExecutorInstanceID          string `json:"executor_instance_id"`
	CapabilityDigest            string `json:"capability_digest"`
	ReserveOperationKey         string `json:"reserve_operation_key"`
	DispatchID                  string `json:"dispatch_id"`
	DispatchFence               uint64 `json:"dispatch_fence"`
	Units                       int    `json:"units"`
	SwarmPolicyRevision         string `json:"swarm_policy_revision"`
	CapacityPolicyRevision      string `json:"capacity_policy_revision"`
	QualificationID             string `json:"qualification_id"`
	QualificationEvidenceDigest string `json:"qualification_evidence_digest"`
	ObservationOperationKey     string `json:"observation_operation_key"`
	ObservationDigest           string `json:"observation_digest"`
	ObservationSequence         uint64 `json:"observation_sequence"`
}

type HostSlotObservationBinding struct {
	DispatchID           string `json:"dispatch_id"`
	ReservationFence     uint64 `json:"reservation_fence"`
	IntentID             string `json:"intent_id"`
	OriginalOperationKey string `json:"original_operation_key"`
	HostAgentID          string `json:"host_agent_id"`
}

type HostCapacityCensus struct {
	SchemaVersion           int                          `json:"schema_version"`
	AuthorityDomainID       string                       `json:"authority_domain_id"`
	IncludedLocalExecutions []HostSlotObservationBinding `json:"included_local_executions"`
}

type HostCapacityObservationRequestV2 struct {
	HostCapacityObservationRequest
	Census HostCapacityCensus `json:"census"`
}

// HostSlotAdmissionView reports the execution cap separately from writer
// occupancy. Known is false whenever the observation/census cannot establish
// an exact headroom value.
type HostSlotAdmissionView struct {
	SchemaVersion              int    `json:"schema_version"`
	Known                      bool   `json:"known"`
	Mode                       string `json:"mode"`
	Reason                     string `json:"reason"`
	AuthorityDomainID          string `json:"authority_domain_id,omitempty"`
	HostID                     string `json:"host_id,omitempty"`
	HostSessionID              string `json:"host_session_id,omitempty"`
	ExecutorInstanceID         string `json:"executor_instance_id,omitempty"`
	ObservationOperationKey    string `json:"observation_operation_key,omitempty"`
	ObservationDigest          string `json:"observation_digest,omitempty"`
	ObservationSequence        uint64 `json:"observation_sequence,omitempty"`
	ObservedOccupied           int    `json:"observed_occupied,omitempty"`
	IncludedLocalExecutions    int    `json:"included_local_executions,omitempty"`
	ExternalOccupied           int    `json:"external_occupied,omitempty"`
	OutstandingExecutionClaims int    `json:"outstanding_execution_claims,omitempty"`
	RemainingAdmissibleSlots   int    `json:"remaining_admissible_slots,omitempty"`
	RetainedWriterCount        int    `json:"retained_writer_count"`
}

func validHostCapacityCensus(c HostCapacityCensus) bool {
	if c.SchemaVersion != 1 || !validID(c.AuthorityDomainID) || c.IncludedLocalExecutions == nil || len(c.IncludedLocalExecutions) > 4096 {
		return false
	}
	seen := map[string]bool{}
	seenHost := map[string]bool{}
	for _, binding := range c.IncludedLocalExecutions {
		if !validID(binding.DispatchID) || binding.ReservationFence == 0 || !validID(binding.IntentID) || !validID(binding.OriginalOperationKey) || !validHostAgentID(binding.HostAgentID) || seen[binding.DispatchID] || seenHost[binding.HostAgentID] {
			return false
		}
		seen[binding.DispatchID] = true
		seenHost[binding.HostAgentID] = true
	}
	return true
}

func hostSlotPolicyShapeValid(s *state, p HostSlotAdmissionPolicy) bool {
	if p.SchemaVersion != 1 || !validID(p.Revision) || !validID(p.AuthorityDomainID) || p.WorkspaceID != s.Binding.Workspace || p.InstallationID != s.Binding.Installation || !validID(p.HostID) || !validID(p.HostSessionID) || !validID(p.ExecutorInstanceID) || !validID(p.Runtime) || !validID(p.Build) || !validCapabilityDigest(p.CapabilityDigest) || !validID(p.CapacityPolicyRevision) || !validID(p.QualificationID) {
		return false
	}
	if p.Mode == HostSlotManual {
		return p.TerminalObservationSource == "" && p.TerminalQualificationID == "" && p.TerminalQualification == nil
	}
	if p.Mode != HostSlotSingleAuthority || p.TerminalQualification == nil {
		return false
	}
	qualification := p.TerminalQualification
	host, hostOK := s.Sessions[p.HostSessionID]
	return hostOK && qualification.SchemaVersion == 1 && qualification.State == "supported" && validID(qualification.ID) && validID(qualification.Source) && qualification.Source == p.TerminalObservationSource && qualification.ID == p.TerminalQualificationID && len(qualification.EvidenceDigest) == 64 && strings.Trim(qualification.EvidenceDigest, "0123456789abcdef") == "" && qualification.AuthorityDomainID == p.AuthorityDomainID && qualification.HostID == p.HostID && qualification.HostSessionID == p.HostSessionID && qualification.ExecutorInstanceID == p.ExecutorInstanceID && qualification.Runtime == p.Runtime && qualification.Build == p.Build && rawDigest(qualification.CapabilityDigest) == rawDigest(p.CapabilityDigest) && !qualification.QualifiedAt.IsZero() && qualification.ExpiresAt.After(qualification.QualifiedAt) && (qualification.ExpiresAt.Before(host.ExpiresAt) || qualification.ExpiresAt.Equal(host.ExpiresAt))
}

func hostSlotTerminalQualificationDigest(qualification HostSlotTerminalQualification) string {
	return identity("host-slot-terminal-qualification/v1", "", qualification)
}

func hostSlotTerminalQualificationCurrent(s *state, policy HostSlotAdmissionPolicy, now time.Time) bool {
	if !hostSlotPolicyShapeValid(s, policy) || policy.Mode != HostSlotSingleAuthority {
		return false
	}
	qualification := policy.TerminalQualification
	return !qualification.QualifiedAt.After(now) && now.Before(qualification.ExpiresAt)
}

func hostSlotEnrollmentCurrent(s *state, policy HostSlotAdmissionPolicy, now time.Time) bool {
	if _, ok := s.Sessions[policy.HostSessionID]; !ok {
		return false
	}
	return authenticateRecord(s, policy.HostSessionID, now) == nil
}

func hostSlotScopeEqual(a, b HostSlotAdmissionPolicy) bool {
	return a.WorkspaceID == b.WorkspaceID && a.InstallationID == b.InstallationID && a.AuthorityDomainID == b.AuthorityDomainID && a.HostID == b.HostID && a.HostSessionID == b.HostSessionID && a.ExecutorInstanceID == b.ExecutorInstanceID && a.Runtime == b.Runtime && a.Build == b.Build && rawDigest(a.CapabilityDigest) == rawDigest(b.CapabilityDigest)
}

func hostSlotClaimMatchesPolicy(claim *HostSlotClaim, policy HostSlotAdmissionPolicy) bool {
	return claim != nil && claim.AuthorityDomainID == policy.AuthorityDomainID && claim.HostID == policy.HostID && claim.HostSessionID == policy.HostSessionID && claim.ExecutorInstanceID == policy.ExecutorInstanceID && claim.CapabilityDigest == rawDigest(policy.CapabilityDigest)
}

func hostSlotPolicyValid(s *state, project string, p HostSlotAdmissionPolicy) bool {
	if !hostSlotPolicyShapeValid(s, p) {
		return false
	}
	if p.Mode == HostSlotManual {
		return true
	}
	if !hostSlotTerminalQualificationCurrent(s, p, time.Now()) {
		return false
	}
	capacity, ok := s.CapacityPolicies[capacityFactKey(p.HostSessionID, p.ExecutorInstanceID)]
	qualification, qok := s.CapacityQualifications[capacityFactKey(p.HostSessionID, p.ExecutorInstanceID)]
	if !ok || !qok || capacity.Revision != p.CapacityPolicyRevision || capacity.State != CapacityKnown || capacity.RequestedSlots < 1 || capacity.HostID != p.HostID || capacity.Runtime != p.Runtime || capacity.Build != p.Build || capacity.CapabilityDigest != rawDigest(p.CapabilityDigest) || qualification.QualificationID != p.QualificationID || qualification.State != CapacityKnown || qualification.QualifiedSlots < 1 || qualification.HostID != p.HostID || qualification.Runtime != p.Runtime || qualification.Build != p.Build || qualification.CapabilityDigest != rawDigest(p.CapabilityDigest) {
		return false
	}
	// A domain label cannot make two independently configured aliases exclusive.
	for projectID, policy := range s.SwarmPolicies {
		if strings.Contains(projectID, "\x00") || projectID == project || policy.HostSlots == nil || policy.HostSlots.Mode != HostSlotSingleAuthority {
			continue
		}
		other := policy.HostSlots
		if other.AuthorityDomainID == p.AuthorityDomainID && (other.HostID != p.HostID || other.HostSessionID != p.HostSessionID || other.ExecutorInstanceID != p.ExecutorInstanceID || other.CapabilityDigest != p.CapabilityDigest || other.Runtime != p.Runtime || other.Build != p.Build) {
			return false
		}
		if other.AuthorityDomainID != p.AuthorityDomainID && other.HostID == p.HostID {
			return false
		}
	}
	return true
}

func rawDigest(value string) string { return strings.TrimPrefix(value, "sha256:") }

func validCapabilityDigest(value string) bool {
	if strings.HasPrefix(value, "sha256:") {
		value = strings.TrimPrefix(value, "sha256:")
	}
	return len(value) == 64 && strings.Trim(value, "0123456789abcdef") == ""
}

func validateHostSlotClaim(d DispatchReservation) bool {
	c := d.HostSlot
	return c == nil || (c.SchemaVersion == 1 && c.AuthorityDomainID != "" && c.HostID != "" && c.HostSessionID != "" && c.ExecutorInstanceID != "" && validID(c.CapabilityDigest) && c.ReserveOperationKey != "" && c.DispatchID == d.ID && c.DispatchFence != 0 && c.DispatchFence == d.Reservation.Fence && c.Units == 1 && c.SwarmPolicyRevision == d.PolicyRevision && c.CapacityPolicyRevision != "" && c.QualificationID != "" && c.ObservationOperationKey != "" && c.ObservationDigest != "" && c.ObservationSequence != 0)
}

func censusBindingMatches(s *state, policy HostSlotAdmissionPolicy, binding HostSlotObservationBinding) bool {
	dispatch, ok := s.Dispatches[binding.DispatchID]
	if !ok || dispatch.HostSlot == nil || dispatch.HostSlot.AuthorityDomainID != policy.AuthorityDomainID || dispatch.HostSlot.HostID != policy.HostID || dispatch.HostSlot.HostSessionID != policy.HostSessionID || dispatch.HostSlot.ExecutorInstanceID != policy.ExecutorInstanceID || dispatch.Reservation.Fence != binding.ReservationFence {
		return false
	}
	intent, ok := s.DispatchIntents[binding.DispatchID]
	if !ok || intent.ID != binding.IntentID || intent.OperationKey != binding.OriginalOperationKey || intent.HostAgentID != binding.HostAgentID || intent.Reservation.Reservation.Fence != binding.ReservationFence || (intent.State != "host_correlated" && intent.State != "acknowledged") {
		return false
	}
	return true
}

func latestHostSlotObservation(s *state, policy HostSlotAdmissionPolicy) (HostCapacityObservation, bool) {
	var latest HostCapacityObservation
	for _, observation := range s.CapacityObservations {
		if observation.HostSessionID == policy.HostSessionID && observation.ExecutorInstanceID == policy.ExecutorInstanceID && observation.Sequence > latest.Sequence {
			latest = observation
		}
	}
	if latest.OperationKey == "" || latest.Census == nil || latest.Census.AuthorityDomainID != policy.AuthorityDomainID {
		return HostCapacityObservation{}, false
	}
	if latest.HostID != policy.HostID || latest.Runtime != policy.Runtime || latest.Build != policy.Build || latest.HostSessionID != policy.HostSessionID || latest.ExecutorInstanceID != policy.ExecutorInstanceID || latest.CapabilityDigest != rawDigest(policy.CapabilityDigest) {
		return HostCapacityObservation{}, false
	}
	for _, binding := range latest.Census.IncludedLocalExecutions {
		if !censusBindingMatches(s, policy, binding) {
			return HostCapacityObservation{}, false
		}
	}
	return latest, true
}

func hostSlotWithinLimit(s *state, project string, now time.Time) bool {
	policy, ok := s.SwarmPolicies[project]
	if !ok || policy.HostSlots == nil || policy.HostSlots.Mode != HostSlotSingleAuthority {
		return true
	}
	view := hostSlotAdmissionView(s, project, now)
	if !view.Known {
		return false
	}
	var latest HostCapacityObservation
	latest, ok = latestHostSlotObservation(s, *policy.HostSlots)
	if !ok {
		return false
	}
	capacity := s.CapacityPolicies[capacityFactKey(policy.HostSlots.HostSessionID, policy.HostSlots.ExecutorInstanceID)]
	qualification := s.CapacityQualifications[capacityFactKey(policy.HostSlots.HostSessionID, policy.HostSlots.ExecutorInstanceID)]
	claims := hostSlotClaimCount(s, policy.HostSlots.AuthorityDomainID)
	external := latest.OccupiedSlots - len(latest.Census.IncludedLocalExecutions)
	if claims < 0 || claims > capacity.RequestedSlots || external < 0 {
		return false
	}
	ceiling := qualification.QualifiedSlots
	if latest.DeclaredSlots < ceiling {
		ceiling = latest.DeclaredSlots
	}
	return external <= ceiling && claims <= ceiling-external
}

func hostSlotClaimCount(s *state, domain string) int {
	count := 0
	for _, dispatch := range s.Dispatches {
		if dispatch.HostSlot != nil && dispatch.HostSlot.AuthorityDomainID == domain && !hostSlotTerminalProof(s, dispatch) {
			count += dispatch.HostSlot.Units
		}
	}
	return count
}

func validateHostSlotDispatchState(s *state, dispatch DispatchReservation, now time.Time) bool {
	policy, ok := s.SwarmPolicies[dispatch.ProjectID]
	if !ok || policy.HostSlots == nil || policy.HostSlots.Mode == HostSlotManual {
		return dispatch.HostSlot == nil
	}
	if policy.HostSlots.Mode != HostSlotSingleAuthority || dispatch.HostSlot == nil || !validateHostSlotClaim(dispatch) || hostSlotTerminalProof(s, dispatch) {
		return false
	}
	claim := dispatch.HostSlot
	p := policy.HostSlots
	if claim.AuthorityDomainID != p.AuthorityDomainID || claim.HostID != p.HostID || claim.HostSessionID != p.HostSessionID || claim.ExecutorInstanceID != p.ExecutorInstanceID || claim.CapabilityDigest != rawDigest(p.CapabilityDigest) || claim.SwarmPolicyRevision != policy.Revision || claim.CapacityPolicyRevision != p.CapacityPolicyRevision || claim.QualificationID != p.QualificationID || claim.DispatchID != dispatch.ID || claim.DispatchFence != dispatch.Reservation.Fence {
		return false
	}
	key := capacityFactKey(p.HostSessionID, p.ExecutorInstanceID)
	capacity := s.CapacityPolicies[key]
	qualification := s.CapacityQualifications[key]
	if !hostSlotEnrollmentCurrent(s, *p, now) || !hostSlotPolicyValid(s, dispatch.ProjectID, *p) || claim.QualificationEvidenceDigest != qualification.EvidenceDigest || (!qualification.ExpiresAt.IsZero() && !now.Before(qualification.ExpiresAt)) {
		return false
	}
	source, ok := s.CapacityObservations[claim.ObservationOperationKey]
	if !ok || source.InputDigest != claim.ObservationDigest || source.Sequence != claim.ObservationSequence || source.Census == nil || source.Census.AuthorityDomainID != p.AuthorityDomainID {
		return false
	}
	if _, ok := latestHostSlotObservation(s, *p); !ok || !now.Before(source.ReceivedAt.Add(capacity.Freshness)) {
		return false
	}
	return hostSlotWithinLimit(s, dispatch.ProjectID, now)
}

func claimHostSlotState(s *state, dispatch DispatchReservation, policy HostSlotAdmissionPolicy, observation HostCapacityObservation, reserveKey string) (*HostSlotClaim, error) {
	if policy.Mode != HostSlotSingleAuthority || !hostSlotEnrollmentCurrent(s, policy, time.Now()) || !hostSlotPolicyValid(s, dispatch.ProjectID, policy) || observation.Census == nil || observation.Census.AuthorityDomainID != policy.AuthorityDomainID || !validateHostSlotClaim(dispatch) {
		return nil, ErrDenied
	}
	return &HostSlotClaim{SchemaVersion: 1, AuthorityDomainID: policy.AuthorityDomainID, HostID: policy.HostID, HostSessionID: policy.HostSessionID, ExecutorInstanceID: policy.ExecutorInstanceID, CapabilityDigest: rawDigest(policy.CapabilityDigest), ReserveOperationKey: reserveKey, DispatchID: dispatch.ID, DispatchFence: dispatch.Reservation.Fence, Units: 1, SwarmPolicyRevision: dispatch.PolicyRevision, CapacityPolicyRevision: policy.CapacityPolicyRevision, QualificationID: policy.QualificationID, QualificationEvidenceDigest: s.CapacityQualifications[capacityFactKey(policy.HostSessionID, policy.ExecutorInstanceID)].EvidenceDigest, ObservationOperationKey: observation.OperationKey, ObservationDigest: observation.InputDigest, ObservationSequence: observation.Sequence}, nil
}

func hostSlotTerminalProof(s *state, dispatch DispatchReservation) bool {
	if dispatch.HostSlot == nil {
		return false
	}
	for _, observed := range s.LifecycleObservations[dispatch.ID] {
		proof := observed.HostSlotTerminal
		if validHostSlotTerminalObservation(s, observed) && proof.DispatchID == dispatch.ID && proof.ReservationFence == dispatch.Reservation.Fence && proof.AuthorityDomainID == dispatch.HostSlot.AuthorityDomainID {
			return true
		}
	}
	return false
}

func validHostSlotTerminalObservation(s *state, observed LifecycleObservation) bool {
	proof := observed.HostSlotTerminal
	if proof == nil || !lifecycleTerminal(observed.State) {
		return false
	}
	dispatch, ok := s.Dispatches[proof.DispatchID]
	if !ok || dispatch.HostSlot == nil || dispatch.HostSlot.AuthorityDomainID != proof.AuthorityDomainID || dispatch.Reservation.Fence != proof.ReservationFence {
		return false
	}
	intent, ok := s.DispatchIntents[proof.DispatchID]
	if !ok || intent.ID != proof.IntentID || intent.HostAgentID != proof.HostAgentID || intent.WorkerSessionID != proof.WorkerSessionID {
		return false
	}
	binding := observed.Binding
	if binding.DispatchID != proof.DispatchID || binding.IntentID != proof.IntentID || binding.ReservationFence != proof.ReservationFence || binding.HostAgentID != proof.HostAgentID || binding.WorkerSessionID != proof.WorkerSessionID || binding.AttemptID != proof.AttemptID || binding.LeaseID != proof.LeaseID || binding.LeaseFence != proof.LeaseFence || binding.BridgeSessionID != proof.BridgeSessionID || observed.Source != proof.Source || !validID(proof.QualificationID) {
		return false
	}
	policy, ok := s.SwarmPolicies[dispatch.ProjectID+"\x00"+proof.QualificationPolicyRevision]
	if !ok || policy.HostSlots == nil || policy.HostSlots.Mode != HostSlotSingleAuthority || policy.HostSlots.AuthorityDomainID != proof.AuthorityDomainID || !hostSlotPolicyShapeValid(s, *policy.HostSlots) {
		return false
	}
	claimPolicy, ok := s.SwarmPolicies[dispatch.ProjectID+"\x00"+dispatch.HostSlot.SwarmPolicyRevision]
	if !ok || claimPolicy.HostSlots == nil || !hostSlotScopeEqual(*claimPolicy.HostSlots, *policy.HostSlots) || !hostSlotClaimMatchesPolicy(dispatch.HostSlot, *claimPolicy.HostSlots) {
		return false
	}
	qualification := policy.HostSlots.TerminalQualification
	return qualification != nil && qualification.ID == proof.QualificationID && qualification.Source == proof.Source && hostSlotScopeMatchesQualification(*policy.HostSlots, *qualification) && hostSlotTerminalQualificationDigest(*qualification) == proof.QualificationDigest && !observed.ReceivedAt.Before(qualification.QualifiedAt) && observed.ReceivedAt.Before(qualification.ExpiresAt) && qualification.AuthorityDomainID == proof.AuthorityDomainID && hostSlotClaimMatchesPolicy(dispatch.HostSlot, *policy.HostSlots)
}

func hostSlotScopeMatchesQualification(policy HostSlotAdmissionPolicy, qualification HostSlotTerminalQualification) bool {
	return qualification.AuthorityDomainID == policy.AuthorityDomainID && qualification.HostID == policy.HostID && qualification.HostSessionID == policy.HostSessionID && qualification.ExecutorInstanceID == policy.ExecutorInstanceID && qualification.Runtime == policy.Runtime && qualification.Build == policy.Build && rawDigest(qualification.CapabilityDigest) == rawDigest(policy.CapabilityDigest)
}

func hostSlotAdmissionView(s *state, project string, now time.Time) HostSlotAdmissionView {
	view := HostSlotAdmissionView{SchemaVersion: 1, Mode: "legacy_manual", Reason: "legacy_manual"}
	view.RetainedWriterCount = controllerOccupancy(s, project)
	policy, ok := s.SwarmPolicies[project]
	if !ok || policy.HostSlots == nil {
		return view
	}
	view.Mode = policy.HostSlots.Mode
	view.AuthorityDomainID = policy.HostSlots.AuthorityDomainID
	view.HostID = policy.HostSlots.HostID
	view.HostSessionID = policy.HostSlots.HostSessionID
	view.ExecutorInstanceID = policy.HostSlots.ExecutorInstanceID
	if policy.HostSlots.Mode == HostSlotManual {
		view.Reason = "manual"
		return view
	}
	if !hostSlotEnrollmentCurrent(s, *policy.HostSlots, now) {
		view.Reason = "host_enrollment_unavailable"
		return view
	}
	if !hostSlotPolicyValid(s, project, *policy.HostSlots) {
		view.Reason = "policy_or_qualification_unknown"
		return view
	}
	key := capacityFactKey(policy.HostSlots.HostSessionID, policy.HostSlots.ExecutorInstanceID)
	capacity := s.CapacityPolicies[key]
	qualification := s.CapacityQualifications[key]
	latest, censusKnown := latestHostSlotObservation(s, *policy.HostSlots)
	observer, observerOK := s.CapacityObservers[latest.ObserverSessionID]
	if !censusKnown || latest.ObservedState != CapacityKnown || latest.DeclarationState != CapacityKnown || !now.Before(latest.ReceivedAt.Add(capacity.Freshness)) || !now.Before(latest.ObservedAt.Add(capacity.Freshness)) || !observerOK || !now.Before(observer.ExpiresAt) || authenticateRecord(s, latest.ObserverSessionID, now) != nil || (!qualification.ExpiresAt.IsZero() && !now.Before(qualification.ExpiresAt)) {
		view.Reason = "fresh_census_required"
		return view
	}
	local := map[string]bool{}
	for _, included := range latest.Census.IncludedLocalExecutions {
		local[included.DispatchID] = true
	}
	claims := hostSlotClaimCount(s, policy.HostSlots.AuthorityDomainID)
	if latest.OccupiedSlots < 0 || latest.AvailableSlots < 0 || latest.OccupiedSlots > math.MaxInt-latest.AvailableSlots || latest.OccupiedSlots+latest.AvailableSlots != latest.DeclaredSlots || len(local) > latest.OccupiedSlots {
		view.Reason = "contradictory_census_or_capacity"
		return view
	}
	external := latest.OccupiedSlots - len(local)
	remaining := hostSlotHeadroom(latest.OccupiedSlots, len(local), latest.DeclaredSlots, claims, capacity.RequestedSlots, qualification.QualifiedSlots)
	view.Known = true
	view.Reason = "admissible"
	view.ObservationOperationKey = latest.OperationKey
	view.ObservationDigest = latest.InputDigest
	view.ObservationSequence = latest.Sequence
	view.ObservedOccupied = latest.OccupiedSlots
	view.IncludedLocalExecutions = len(local)
	view.ExternalOccupied = external
	view.OutstandingExecutionClaims = claims
	view.RemainingAdmissibleSlots = remaining
	return view
}

// hostSlotHeadroom computes min(P-L, min(D,Q)-(O-K)-L) with checked
// subtraction. Inputs have already passed the observed-count consistency gate.
func hostSlotHeadroom(occupied, included, declared, outstanding, requested, qualified int) int {
	if occupied < 0 || included < 0 || included > occupied || declared < 0 || outstanding < 0 || requested < 0 || qualified < 0 {
		return 0
	}
	external := occupied - included
	ceiling := qualified
	if declared < ceiling {
		ceiling = declared
	}
	if outstanding >= requested || external >= ceiling || outstanding >= ceiling-external {
		return 0
	}
	byLocal := requested - outstanding
	byDomain := ceiling - external - outstanding
	if byLocal < byDomain {
		return byLocal
	}
	return byDomain
}

// RecordHostCapacityObservationV2 stores an authenticated census with its
// versioned digest. V1 observations remain diagnostic and never imply an
// empty census.
func (a *Authority) RecordHostCapacityObservationV2(token, observerSessionID string, req HostCapacityObservationRequestV2) (HostCapacityObservation, bool, error) {
	var out HostCapacityObservation
	r := req.HostCapacityObservationRequest
	duplicate := false
	if r.SchemaVersion != 2 || req.Census.SchemaVersion != 1 || !validHostCapacityCensus(req.Census) {
		return out, false, ErrDenied
	}
	digest := identity("dacli-host-capacity-observation/v2", observerSessionID, struct {
		Request HostCapacityObservationRequest
		Census  HostCapacityCensus
	}{r, req.Census})
	err := a.transaction(false, true, func(s *state) error {
		base := r
		base.SchemaVersion = 1
		if s.Version != 22 || !validateCapacityObservation(base) {
			return ErrDenied
		}
		observer, ok := s.CapacityObservers[observerSessionID]
		if !ok || observer.ObserverSessionID != observerSessionID || observer.HostID != r.HostID || observer.Runtime != r.Runtime || observer.Build != r.Build || observer.HostSessionID != r.HostSessionID || observer.ExecutorInstanceID != r.ExecutorInstanceID || observer.CapabilityDigest != r.CapabilityDigest || !time.Now().Before(observer.ExpiresAt) || !validObserverCredentialScope(s, token, observerSessionID, observer) || authenticateRecord(s, observer.HostSessionID, time.Now()) != nil || !r.ObservedAt.Before(time.Now()) {
			return ErrDenied
		}
		if old, exists := s.CapacityObservations[r.OperationKey]; exists {
			if old.InputDigest != digest {
				return ErrDenied
			}
			out, duplicate = old, true
			return nil
		}
		var policy *HostSlotAdmissionPolicy
		for _, candidate := range s.SwarmPolicies {
			if candidate.HostSlots != nil && candidate.HostSlots.Mode == HostSlotSingleAuthority && candidate.HostSlots.AuthorityDomainID == req.Census.AuthorityDomainID && candidate.HostSlots.HostSessionID == r.HostSessionID && candidate.HostSlots.ExecutorInstanceID == r.ExecutorInstanceID {
				copy := *candidate.HostSlots
				policy = &copy
				break
			}
		}
		if policy == nil || policy.HostID != r.HostID || policy.Runtime != r.Runtime || policy.Build != r.Build || rawDigest(policy.CapabilityDigest) != r.CapabilityDigest {
			return ErrDenied
		}
		if r.OccupiedSlots < 0 || r.AvailableSlots < 0 || r.OccupiedSlots > math.MaxInt-r.AvailableSlots || r.DeclaredSlots != r.OccupiedSlots+r.AvailableSlots || len(req.Census.IncludedLocalExecutions) > r.OccupiedSlots {
			return ErrDenied
		}
		for _, binding := range req.Census.IncludedLocalExecutions {
			dispatch := s.Dispatches[binding.DispatchID]
			if !censusBindingMatches(s, *policy, binding) || hostSlotTerminalProof(s, dispatch) {
				return ErrDenied
			}
		}
		for _, old := range s.CapacityObservations {
			if old.HostSessionID == r.HostSessionID && old.ExecutorInstanceID == r.ExecutorInstanceID && r.Sequence <= old.Sequence {
				return ErrDenied
			}
		}
		census := req.Census
		census.IncludedLocalExecutions = make([]HostSlotObservationBinding, len(req.Census.IncludedLocalExecutions))
		copy(census.IncludedLocalExecutions, req.Census.IncludedLocalExecutions)
		out = HostCapacityObservation{HostCapacityObservationRequest: r, ObserverSessionID: observerSessionID, InputDigest: digest, ReceivedAt: time.Now().UTC(), Census: &census}
		s.CapacityObservations[r.OperationKey] = out
		return nil
	})
	return out, duplicate, err
}

// ReadHostSlotAdmission is a project-scoped diagnostic read; it never grants
// a dispatch allocation or host action.
func (a *Authority) ReadHostSlotAdmission(token, session, project string) (HostSlotAdmissionView, error) {
	var out HostSlotAdmissionView
	err := a.transaction(false, false, func(s *state) error {
		if s.Version != 22 {
			return ErrStorage
		}
		if _, err := controller(s, token, session, project, "read"); err != nil {
			return err
		}
		out = hostSlotAdmissionView(s, project, time.Now())
		return nil
	})
	return out, err
}
