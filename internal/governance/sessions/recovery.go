package sessions

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"time"
)

// RecoveryPolicy records the owner's acceptance of a cooperative assurance.
// It never asserts that a bridge report proves every OS writer stopped.
type RecoveryPolicy struct {
	SchemaVersion                  int
	ProjectID, Revision, Rationale string
	AcceptCooperativeTerminal      bool
}
type RecoveryTarget struct {
	Workspace, Kind, TaskID, DispatchID, IntentID, AttemptID, ProtectionID, SessionID, WorkerFamilyID, Repository, Checkout string
	ExpectedFence                                                                                                           uint64
}
type RecoveryRequest struct {
	OperationKey                              string
	Target                                    RecoveryTarget
	Mode, Rationale, Uncertainty, ReleaseRisk string
	PolicyRevision                            string
	ObservationSequence                       uint64
	EvidenceDigest                            string
}
type RecoveryReceipt struct {
	ID, OperationKey, RequestDigest, Actor, Mode, Rationale, Uncertainty, ReleaseRisk, PolicyRevision string
	Target                                                                                            RecoveryTarget
	Observation                                                                                       *LifecycleObservation
	ReleasedResources                                                                                 []Resource
	PreviousGlobalFence, GlobalFence                                                                  uint64
	RecordedAt                                                                                        time.Time
}

func (a *Authority) UpgradeRecovery(owner string) error {
	old := *a
	old.version = 9
	return old.transaction(false, true, func(s *state) error {
		if s.Version != 9 || len(owner) != 64 || hash(owner) != s.OwnerHash {
			return ErrDenied
		}
		s.RecoveryPolicies = map[string]RecoveryPolicy{}
		s.RecoveryPolicyHistory = map[string]map[string]RecoveryPolicy{}
		s.Recoveries = map[string]RecoveryReceipt{}
		s.RecoveryOperations = map[string]RecoveryReceipt{}
		s.Version = 10
		return nil
	})
}
func (a *Authority) ConfigureRecovery(owner string, p RecoveryPolicy) error {
	return a.transaction(false, true, func(s *state) error {
		if s.Version != 10 || len(owner) != 64 || hash(owner) != s.OwnerHash || p.SchemaVersion != 1 || !validID(p.ProjectID) || !validID(p.Revision) || strings.TrimSpace(p.Rationale) == "" || len(p.Rationale) > 8192 {
			return ErrDenied
		}
		if _, ok := s.SwarmPolicies[p.ProjectID]; !ok {
			return ErrDenied
		}
		if old, ok := s.RecoveryPolicyHistory[p.ProjectID][p.Revision]; ok {
			if !reflect.DeepEqual(old, p) {
				return ErrDenied
			}
			return nil
		}
		if s.RecoveryPolicyHistory[p.ProjectID] == nil {
			s.RecoveryPolicyHistory[p.ProjectID] = map[string]RecoveryPolicy{}
		}
		s.RecoveryPolicyHistory[p.ProjectID][p.Revision] = p
		s.RecoveryPolicies[p.ProjectID] = p
		return nil
	})
}
func reservationRetained(s *state, d DispatchReservation) bool {
	_, recovered := s.Recoveries["reservation:"+d.ID]
	return !d.Converted && !recovered
}
func recoveryTarget(s *state, kind, id string) (RecoveryTarget, Lease, error) {
	t := RecoveryTarget{Workspace: s.Binding.Workspace, Kind: kind, ProtectionID: id}
	var l Lease
	switch kind {
	case "reservation":
		d, ok := s.Dispatches[id]
		if !ok || !reservationRetained(s, d) {
			return t, l, ErrDenied
		}
		l = d.Reservation
		t.DispatchID = d.ID
		if i, ok := s.DispatchIntents[d.ID]; ok {
			t.IntentID = i.ID
			t.SessionID = i.WorkerSessionID
		}
	case "lease":
		var ok bool
		l, ok = s.Leases[id]
		if !ok || l.Released {
			return t, l, ErrDenied
		}
		t.SessionID = l.SessionID
		for _, d := range s.Dispatches {
			if d.Converted && d.Reservation.ID == id {
				if t.DispatchID != "" {
					return t, l, ErrDenied
				}
				t.DispatchID = d.ID
				if i, ok := s.DispatchIntents[d.ID]; ok {
					t.IntentID = i.ID
				}
			}
		}
	default:
		return t, l, ErrDenied
	}
	if t.SessionID != "" {
		family, e := historicalWorkerFamily(s, t.SessionID)
		if e != nil {
			return t, l, e
		}
		t.WorkerFamilyID = family
	}
	t.TaskID = l.TaskID
	t.AttemptID = l.AttemptID
	t.Repository = l.Repository
	t.Checkout = l.Checkout
	t.ExpectedFence = l.Fence
	return t, l, nil
}

// OwnerRecoveryTarget is a preview, not permission to remove protection later.
func (a *Authority) OwnerRecoveryTarget(owner, kind, id string) (RecoveryTarget, error) {
	var t RecoveryTarget
	e := a.transaction(false, false, func(s *state) error {
		if s.Version != 10 || len(owner) != 64 || hash(owner) != s.OwnerHash {
			return ErrDenied
		}
		var e error
		t, _, e = recoveryTarget(s, kind, id)
		return e
	})
	if e != nil {
		return RecoveryTarget{}, e
	}
	return t, nil
}

// Structural identity survives expiry/revocation; missing or cyclic lineage cannot be reconciled implicitly.
func historicalWorkerFamily(s *state, id string) (string, error) {
	seen := map[string]bool{}
	for id != "" && !seen[id] {
		seen[id] = true
		r, ok := s.Sessions[id]
		if !ok {
			return "", ErrDenied
		}
		if r.Parent == "" {
			return id, nil
		}
		id = r.Parent
	}
	return "", ErrDenied
}
func recoveryTaskWithdrawn(s *state, session, task string) bool {
	seen := map[string]bool{}
	for session != "" && !seen[session] {
		for _, r := range s.Recoveries {
			if r.Target.WorkerFamilyID == session && r.Target.TaskID == task {
				return true
			}
		}
		seen[session] = true
		record, ok := s.Sessions[session]
		if !ok {
			break
		}
		session = record.Parent
	}
	return false
}
func (a *Authority) Recover(owner string, req RecoveryRequest) (RecoveryReceipt, error) {
	var result RecoveryReceipt
	e := a.transaction(false, true, func(s *state) error {
		// Authenticate even historical receipt reads; session/controller grants never substitute.
		if s.Version != 10 || len(owner) != 64 || hash(owner) != s.OwnerHash || !validID(req.OperationKey) {
			return ErrDenied
		}
		b, _ := json.Marshal(req)
		digest := hash(string(b))
		if r, ok := s.RecoveryOperations[req.OperationKey]; ok {
			if r.RequestDigest != digest {
				return ErrDenied
			}
			result = r
			return nil
		}
		if _, ok := s.Recoveries[req.Target.Kind+":"+req.Target.ProtectionID]; ok {
			return ErrDenied
		}
		target, l, e := recoveryTarget(s, req.Target.Kind, req.Target.ProtectionID)
		if e != nil {
			return e
		}
		if !reflect.DeepEqual(target, req.Target) || target.ExpectedFence == 0 || s.Fence == math.MaxUint64 || strings.TrimSpace(req.Rationale) == "" || len(req.Rationale) > 8192 {
			return ErrDenied
		}
		var observation *LifecycleObservation
		switch req.Mode {
		case "owner-reconciliation":
			if strings.TrimSpace(req.Uncertainty) == "" || strings.TrimSpace(req.ReleaseRisk) == "" || len(req.Uncertainty) > 8192 || len(req.ReleaseRisk) > 8192 || req.PolicyRevision != "" || req.ObservationSequence != 0 || req.EvidenceDigest != "" {
				return ErrDenied
			}
		case "authenticated-terminal":
			binding, ok := s.Tasks[target.TaskID]
			if !ok {
				return ErrDenied
			}
			p, ok := s.RecoveryPolicies[binding.ProjectID]
			if !ok || !p.AcceptCooperativeTerminal || p.Revision != req.PolicyRevision || target.DispatchID == "" || target.IntentID == "" {
				return ErrDenied
			}
			history := s.LifecycleObservations[target.DispatchID]
			if len(history) == 0 {
				return ErrDenied
			}
			last := history[len(history)-1]
			i, ok := s.DispatchIntents[target.DispatchID]
			if !ok || !lifecycleTerminal(last.State) || last.Sequence != req.ObservationSequence || last.EvidenceDigest != req.EvidenceDigest || last.Assurance != "cooperative-owner-provisioned-bridge" || !reflect.DeepEqual(last.Binding, lifecycleBinding(i)) {
				return ErrDenied
			}
			observation = &last
		default:
			return ErrDenied
		}
		id, e := mint()
		if e != nil {
			return e
		}
		previous := s.Fence
		s.Fence++
		result = RecoveryReceipt{ID: id, OperationKey: req.OperationKey, RequestDigest: digest, Actor: "owner:" + s.OwnerHash, Mode: req.Mode, Rationale: req.Rationale, Uncertainty: req.Uncertainty, ReleaseRisk: req.ReleaseRisk, PolicyRevision: req.PolicyRevision, Target: target, Observation: observation, ReleasedResources: append([]Resource{}, l.Resources...), PreviousGlobalFence: previous, GlobalFence: s.Fence, RecordedAt: time.Now().UTC()}
		// Tombstone withdraws old worker+descendant task authority; original dispatch identity stays immutable.
		s.Recoveries[target.Kind+":"+target.ProtectionID] = result
		s.RecoveryOperations[req.OperationKey] = result
		if target.Kind == "lease" {
			l.Released = true
			s.Leases[l.ID] = l
		}
		return nil
	})
	if e != nil {
		return RecoveryReceipt{}, e
	}
	return result, nil
}
