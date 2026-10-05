package sessions

import (
	"github.com/mlnomadpy/dacli/internal/governance/policy"
	"reflect"
	"time"
)

// NativePolicy is explicit owner policy for one canonical local task action.
type NativePolicy struct {
	SchemaVersion                   int
	ProjectID, TaskID, Revision     string
	Allow, RequireApproval, Stopped bool
	ExpiresAt                       time.Time
}

// NativePolicyDecision is immutable permission history, never a bearer permit.
type NativePolicyDecision struct {
	ID, ActorID, PolicyDigest, ActionDigest string
	Input                                   NativeAcceptanceInput
	Policy                                  NativePolicy
	Assessment                              policy.NativeAcceptanceAssessment
	Prepared                                NativeAcceptanceRecord
	CreatedAt, ExpiresAt                    time.Time
}
type NativePolicyEvaluationRequest struct {
	OperationKey string
	Input        NativeAcceptanceInput
	ExpiresAt    time.Time
}
type NativePolicyApprovalRequest struct {
	OperationKey, DecisionID string
	ExpiresAt                time.Time
}
type NativePolicyApproval struct {
	DecisionID, OperationKey, ActionDigest string
	CreatedAt, ExpiresAt                   time.Time
}
type NativePolicyConsumption struct {
	DecisionID, OperationKey, ActionDigest, CanonicalOutcomeDigest string
	ConsumedAt                                                     time.Time
}

// NativePolicyOutcome records observation separately from permission consumption.
type NativePolicyOutcome struct {
	OperationKey, DecisionID, State, ExpectedOutcomeDigest, ObservedOutcomeDigest string
	ObservedAt                                                                    time.Time
}
type NativePolicyStatus struct {
	Decision    NativePolicyDecision
	Approval    *NativePolicyApproval
	Consumption *NativePolicyConsumption
	Outcome     *NativePolicyOutcome
	Historical  bool
}

func nativePolicyOwner(s *state, owner string) bool {
	return s.Version >= 15 && len(owner) == 64 && hash(owner) == s.OwnerHash
}
func (a *Authority) UpgradeNativePolicy(owner string) error {
	return a.transaction(false, true, func(s *state) error {
		if s.Version != 14 || len(owner) != 64 || hash(owner) != s.OwnerHash {
			return ErrDenied
		}
		s.NativePolicies = map[string]NativePolicy{}
		s.NativePolicyHistory = map[string]NativePolicy{}
		s.NativePolicyDecisions = map[string]NativePolicyDecision{}
		s.NativePolicyApprovals = map[string]NativePolicyApproval{}
		s.NativePolicyConsumptions = map[string]NativePolicyConsumption{}
		s.NativePolicyOutcomes = map[string]NativePolicyOutcome{}
		s.Version = 15
		return nil
	})
}
func (a *Authority) ConfigureNativePolicy(owner string, p NativePolicy) error {
	p.ExpiresAt = p.ExpiresAt.Round(0).UTC()
	return a.transaction(false, true, func(s *state) error {
		if !nativePolicyOwner(s, owner) || p.SchemaVersion != 1 || !validID(p.TaskID) || !validID(p.ProjectID) || !validID(p.Revision) || !p.ExpiresAt.After(time.Now()) {
			return ErrDenied
		}
		key := p.TaskID + ":" + p.Revision
		if old, ok := s.NativePolicyHistory[key]; ok {
			if !reflect.DeepEqual(old, p) {
				return ErrDenied
			}
			return nil
		}
		s.NativePolicyHistory[key] = p
		s.NativePolicies[p.TaskID] = p
		return nil
	})
}
func nativePolicyCurrent(s *state, d NativePolicyDecision, now time.Time) error {
	p, ok := s.NativePolicies[d.Input.TaskID]
	if !ok || !reflect.DeepEqual(p, d.Policy) || d.PolicyDigest != identity("native-policy", "", p) || !p.Allow || p.Stopped || !now.Before(p.ExpiresAt) || !now.Before(d.ExpiresAt) {
		return ErrDenied
	}
	if p.RequireApproval {
		ap, ok := s.NativePolicyApprovals[d.ID]
		if !ok || ap.DecisionID != d.ID || ap.ActionDigest != d.ActionDigest || !now.Before(ap.ExpiresAt) {
			return ErrDenied
		}
	}
	return nil
}
func (a *Authority) EvaluateNativePolicy(owner string, r NativePolicyEvaluationRequest, prepare func(NativeAcceptanceRecord) (string, error)) (NativePolicyDecision, error) {
	var out NativePolicyDecision
	r.ExpiresAt = r.ExpiresAt.Round(0).UTC()
	e := a.transaction(false, true, func(s *state) error {
		if !nativePolicyOwner(s, owner) || !validID(r.OperationKey) || nativeAcceptanceOwner(s, owner, r.Input, a.binding) != nil || prepare == nil {
			return ErrDenied
		}
		if old, ok := s.NativePolicyDecisions[r.OperationKey]; ok {
			if !reflect.DeepEqual(old.Input, r.Input) || !old.ExpiresAt.Equal(r.ExpiresAt) {
				return ErrDenied
			}
			out = old
			return nil
		}
		if _, ok := s.TaskTerminals[r.Input.TaskID]; ok {
			return ErrDenied
		}
		p, ok := s.NativePolicies[r.Input.TaskID]
		now := time.Now().UTC()
		if !ok || p.ProjectID != r.Input.ProjectID || !now.Before(p.ExpiresAt) || !r.ExpiresAt.After(now) || r.ExpiresAt.After(p.ExpiresAt) {
			return ErrDenied
		}
		c, e := a.acceptanceCurrent(s, r.Input, "")
		if e != nil {
			return e
		}
		rec := NativeAcceptanceRecord{OperationKey: r.Input.OperationKey, InputDigest: identity("native-acceptance-operation", "", r.Input), State: "closing", Candidate: c, CreatedAt: now}
		rec.CanonicalReceiptDigest = identity("native-acceptance-receipt", "", rec)
		rec.CanonicalOutcomeDigest, e = prepare(rec)
		if e != nil || !validDigest(rec.CanonicalOutcomeDigest) {
			return ErrDenied
		}
		if _, e = a.acceptanceCurrent(s, r.Input, ""); e != nil {
			return e
		}
		if !time.Now().Before(p.ExpiresAt) || !time.Now().Before(r.ExpiresAt) {
			return ErrDenied
		}
		actor := "installation-owner:" + a.binding.Installation
		action := identity("native-policy-action", "", struct {
			Input NativeAcceptanceInput
			Body  string
		}{r.Input, rec.CanonicalOutcomeDigest})
		assessment := policy.EvaluateNativeAcceptance(policy.NativeAcceptanceObservation{ActorID: actor, Action: "native-task-accept", TaskID: r.Input.TaskID, ActionDigest: action, PolicyRevision: p.Revision, Allowed: p.Allow, ApprovalRequired: p.RequireApproval, Stopped: p.Stopped})
		assessment.Assurance = "cooperative-session"
		out = NativePolicyDecision{ID: r.OperationKey, ActorID: actor, PolicyDigest: identity("native-policy", "", p), ActionDigest: action, Input: r.Input, Policy: p, Assessment: assessment, Prepared: rec, CreatedAt: now, ExpiresAt: r.ExpiresAt}
		s.NativePolicyDecisions[out.ID] = out
		return nil
	})
	if e != nil {
		return NativePolicyDecision{}, e
	}
	return out, nil
}
func (a *Authority) ApproveNativePolicy(owner string, r NativePolicyApprovalRequest) (NativePolicyApproval, error) {
	var out NativePolicyApproval
	r.ExpiresAt = r.ExpiresAt.Round(0).UTC()
	e := a.transaction(false, true, func(s *state) error {
		d, ok := s.NativePolicyDecisions[r.DecisionID]
		if !nativePolicyOwner(s, owner) || !ok || !validID(r.OperationKey) || !d.Policy.Allow || d.Policy.Stopped || !d.Policy.RequireApproval || !r.ExpiresAt.After(time.Now()) || r.ExpiresAt.After(d.ExpiresAt) || !reflect.DeepEqual(s.NativePolicies[d.Input.TaskID], d.Policy) {
			return ErrDenied
		}
		if old, ok := s.NativePolicyApprovals[d.ID]; ok {
			if old.OperationKey != r.OperationKey || !old.ExpiresAt.Equal(r.ExpiresAt) {
				return ErrDenied
			}
			out = old
			return nil
		}
		if _, used := s.NativePolicyConsumptions[d.ID]; used {
			return ErrDenied
		}
		out = NativePolicyApproval{d.ID, r.OperationKey, d.ActionDigest, time.Now().UTC(), r.ExpiresAt}
		s.NativePolicyApprovals[d.ID] = out
		return nil
	})
	if e != nil {
		return NativePolicyApproval{}, e
	}
	return out, nil
}
func nativePolicyForOperation(s *state, op string) (NativePolicyDecision, bool) {
	for id, c := range s.NativePolicyConsumptions {
		if c.OperationKey == op {
			d, ok := s.NativePolicyDecisions[id]
			return d, ok
		}
	}
	return NativePolicyDecision{}, false
}
func (a *Authority) ReadNativePolicyStatus(owner, id string) (NativePolicyStatus, error) {
	var out NativePolicyStatus
	e := a.transaction(false, false, func(s *state) error {
		d, ok := s.NativePolicyDecisions[id]
		if !nativePolicyOwner(s, owner) || !ok {
			return ErrDenied
		}
		out = NativePolicyStatus{Decision: d, Historical: true}
		if v, ok := s.NativePolicyApprovals[id]; ok {
			out.Approval = &v
		}
		if v, ok := s.NativePolicyConsumptions[id]; ok {
			out.Consumption = &v
		}
		if v, ok := s.NativePolicyOutcomes[d.Input.OperationKey]; ok && v.DecisionID == d.ID {
			out.Outcome = &v
		}
		return nil
	})
	if e != nil {
		return NativePolicyStatus{}, e
	}
	return out, nil
}
