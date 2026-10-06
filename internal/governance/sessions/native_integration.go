package sessions

import (
	"reflect"
	"time"
)

// NativeIntegrationPolicy authorizes only an exact local fast-forward target.
// ConfiguredAt is issued by the authority before new verification and review;
// repinning a changed trunk cannot reuse those earlier producer receipts.
type NativeIntegrationPolicy struct {
	SchemaVersion                                                       int
	ProjectID, TaskID, Revision                                         string
	Repository, TargetCheckout, TargetRef, ExpectedCommit, ExpectedTree string
	NativePolicyRevision                                                string
	Allow, Stopped                                                      bool
	ExpiresAt, ConfiguredAt                                             time.Time
}
type NativeIntegrationInput struct {
	OperationKey, PolicyRevision string
	Candidate                    NativeAcceptanceInput
}

// NativeIntegrationObservation contains Git facts without command, output, credential or private filesystem path.
type NativeIntegrationObservation struct {
	Commit, Tree string
	Clean        bool
	ObservedAt   time.Time
}
type NativeIntegrationRecord struct {
	OperationKey, InputDigest, State, ReceiptDigest string
	Input                                           NativeIntegrationInput
	Policy                                          NativeIntegrationPolicy
	Before, Outcome                                 NativeIntegrationObservation
	CreatedAt, CompletedAt                          time.Time
}
type NativeIntegrationResult struct {
	Record    NativeIntegrationRecord
	Duplicate bool
}

func integrationOwner(s *state, owner string) bool {
	return s.Version >= 18 && len(owner) == 64 && hash(owner) == s.OwnerHash
}
func (a *Authority) UpgradeNativeIntegration(owner string) error {
	return a.transaction(false, true, func(s *state) error {
		if s.Version != 17 || len(owner) != 64 || hash(owner) != s.OwnerHash {
			return ErrDenied
		}
		s.NativeIntegrationPolicies = map[string]NativeIntegrationPolicy{}
		s.NativeIntegrationHistory = map[string]NativeIntegrationPolicy{}
		s.NativeIntegrationOperations = map[string]NativeIntegrationRecord{}
		s.Version = 18
		return nil
	})
}
func (a *Authority) ConfigureNativeIntegration(owner string, p NativeIntegrationPolicy) (NativeIntegrationPolicy, error) {
	var out NativeIntegrationPolicy
	p.ExpiresAt = p.ExpiresAt.Round(0).UTC()
	err := a.transaction(false, true, func(s *state) error {
		if !integrationOwner(s, owner) || p.SchemaVersion != 1 || !validID(p.ProjectID) || !validID(p.TaskID) || !validID(p.Revision) || p.Repository == "" || p.TargetCheckout == "" || p.TargetRef == "" || p.ExpectedCommit == "" || p.ExpectedTree == "" || !validID(p.NativePolicyRevision) || !p.ConfiguredAt.IsZero() || !time.Now().Before(p.ExpiresAt) {
			return ErrDenied
		}
		b, ok := s.Tasks[p.TaskID]
		if !ok || b.ProjectID != p.ProjectID || b.Repository != p.Repository {
			return ErrDenied
		}
		key := p.TaskID + ":" + p.Revision
		if old, ok := s.NativeIntegrationHistory[key]; ok {
			original := old
			original.ConfiguredAt = time.Time{}
			if !reflect.DeepEqual(original, p) {
				return ErrDenied
			}
			out = old
			return nil
		}
		p.ConfiguredAt = time.Now().UTC()
		s.NativeIntegrationHistory[key] = p
		s.NativeIntegrationPolicies[p.TaskID] = p
		out = p
		return nil
	})
	if err != nil {
		return NativeIntegrationPolicy{}, err
	}
	return out, nil
}
func integrationPolicyCurrent(s *state, rec NativeIntegrationRecord, now time.Time) error {
	p, ok := s.NativeIntegrationPolicies[rec.Input.Candidate.TaskID]
	n, nok := s.NativePolicies[rec.Input.Candidate.TaskID]
	if !ok || !reflect.DeepEqual(p, rec.Policy) || !p.Allow || p.Stopped || !now.Before(p.ExpiresAt) || !nok || n.ProjectID != p.ProjectID || n.Revision != p.NativePolicyRevision || !n.Allow || n.Stopped || !now.Before(n.ExpiresAt) {
		return ErrDenied
	}
	return nil
}

// Pure predicates run after the last slow observer and immediately before Git.
func integrationFinalCurrent(s *state, rec NativeIntegrationRecord) error {
	if integrationPolicyCurrent(s, rec, time.Now()) != nil {
		return ErrDenied
	}
	input := rec.Input.Candidate
	review, ok := s.Reviews[input.ReviewID]
	if !ok || review.Request.Outcome != "approve" || !review.CreatedAt.After(rec.Policy.ConfiguredAt) {
		return ErrDenied
	}
	at, e := reviewAttempt(s, input.AttemptID)
	if e != nil || at.ProjectID != rec.Policy.ProjectID || at.Request.TaskID != rec.Policy.TaskID {
		return ErrDenied
	}
	rev, e := currentRevision(s, at, input.RevisionID, input.LeaseID, input.ExpectedFence)
	if e != nil || !reflect.DeepEqual(rev, review.Revision) {
		return ErrDenied
	}
	if !reviewGrantCurrent(s, review.IssuerSession, at, s.ReviewGrants[review.Request.GrantID]) || reviewWorkerExcept(s, at, review.Request, "") != nil || !reviewEvidence(s, at, rev, review.Request.EvidenceIDs, review.Observation.Checks) {
		return ErrDenied
	}
	for _, id := range review.Request.EvidenceIDs {
		ev, ok := s.Evidence[id]
		if !ok || !ev.RecordedAt.After(rec.Policy.ConfiguredAt) {
			return ErrDenied
		}
	}
	return nil
}
func (a *Authority) integrationCurrent(s *state, rec NativeIntegrationRecord) error {
	if integrationPolicyCurrent(s, rec, time.Now()) != nil {
		return ErrDenied
	}
	if _, e := a.acceptanceCurrent(s, rec.Input.Candidate, ""); e != nil {
		return e
	}
	return integrationFinalCurrent(s, rec)
}
func integrationDone(rec NativeIntegrationRecord, obs NativeIntegrationObservation) bool {
	return obs.Clean && obs.Commit == rec.Input.Candidate.Commit && obs.Tree == rec.Input.Candidate.Tree && !obs.ObservedAt.IsZero()
}
func (a *Authority) beginNativeIntegration(owner string, r NativeIntegrationInput, inspect func(NativeIntegrationRecord) (NativeIntegrationObservation, error)) (NativeIntegrationRecord, bool, error) {
	var out NativeIntegrationRecord
	fresh := false
	err := a.transaction(false, true, func(s *state) error {
		if !integrationOwner(s, owner) || !validID(r.OperationKey) || !validID(r.PolicyRevision) || nativeAcceptanceOwner(s, owner, r.Candidate, a.binding) != nil || inspect == nil {
			return ErrDenied
		}
		digest := identity("native-integration-input", "", r)
		if old, ok := s.NativeIntegrationOperations[r.OperationKey]; ok {
			if old.InputDigest != digest || !reflect.DeepEqual(old.Input, r) || (old.State == "integrated" && !validNativeIntegrationOutcome(old)) {
				return ErrDenied
			}
			out = old
			return nil
		}
		p, ok := s.NativeIntegrationPolicies[r.Candidate.TaskID]
		if !ok || p.Revision != r.PolicyRevision || p.ProjectID != r.Candidate.ProjectID || p.Repository != r.Candidate.Repository {
			return ErrDenied
		}
		for _, other := range s.NativeIntegrationOperations {
			if other.Input.Candidate.TaskID == r.Candidate.TaskID && other.Policy.Revision == p.Revision {
				return ErrDenied
			}
			if other.State != "integrated" && other.Policy.Repository == p.Repository && other.Policy.TargetRef == p.TargetRef {
				return ErrReconcile
			}
		}
		out = NativeIntegrationRecord{OperationKey: r.OperationKey, InputDigest: digest, State: "pending", Input: r, Policy: p, CreatedAt: time.Now().UTC()}
		if e := a.integrationCurrent(s, out); e != nil {
			return e
		}
		before, e := inspect(out)
		if e != nil {
			return e
		}
		if !before.Clean || before.Commit != p.ExpectedCommit || before.Tree != p.ExpectedTree || before.Commit == r.Candidate.Commit || before.ObservedAt.IsZero() {
			return ErrDenied
		}
		if e = integrationFinalCurrent(s, out); e != nil {
			return e
		}
		out.Before = before
		s.NativeIntegrationOperations[r.OperationKey] = out
		fresh = true
		return nil
	})
	if err != nil {
		return NativeIntegrationRecord{}, false, err
	}
	return out, fresh, nil
}

// ApplyNativeIntegration owns the first-only effect across two durable authority
// transactions. The freshness flag never crosses the public API as a permit.
// A recovered pending intent can inspect/settle but can never launch Git again.
func (a *Authority) ApplyNativeIntegration(owner string, r NativeIntegrationInput, inspect func(NativeIntegrationRecord) (NativeIntegrationObservation, error), effect func(NativeIntegrationRecord, func() error) (NativeIntegrationObservation, error)) (NativeIntegrationResult, error) {
	_, fresh, e := a.beginNativeIntegration(owner, r, inspect)
	if e != nil {
		return NativeIntegrationResult{}, e
	}
	return a.continueNativeIntegration(owner, r, inspect, effect, fresh)
}
func (a *Authority) ReconcileNativeIntegration(owner string, r NativeIntegrationInput, inspect func(NativeIntegrationRecord) (NativeIntegrationObservation, error)) (NativeIntegrationResult, error) {
	return a.continueNativeIntegration(owner, r, inspect, nil, false)
}
func (a *Authority) continueNativeIntegration(owner string, r NativeIntegrationInput, inspect func(NativeIntegrationRecord) (NativeIntegrationObservation, error), effect func(NativeIntegrationRecord, func() error) (NativeIntegrationObservation, error), fresh bool) (NativeIntegrationResult, error) {
	var out NativeIntegrationResult
	err := a.transaction(false, true, func(s *state) error {
		rec, ok := s.NativeIntegrationOperations[r.OperationKey]
		if !integrationOwner(s, owner) || !ok || inspect == nil || !reflect.DeepEqual(rec.Input, r) || rec.InputDigest != identity("native-integration-input", "", r) {
			return ErrDenied
		}
		if rec.State == "integrated" {
			if !validNativeIntegrationOutcome(rec) {
				return ErrDenied
			}
			out = NativeIntegrationResult{rec, true}
			return nil
		}
		if rec.State != "pending" {
			return ErrReconcile
		}
		obs, e := inspect(rec)
		if e != nil {
			return e
		}
		if !integrationDone(rec, obs) {
			if !fresh || effect == nil {
				return ErrReconcile
			}
			if !obs.Clean || obs.Commit != rec.Before.Commit || obs.Tree != rec.Before.Tree {
				return ErrReconcile
			}
			if e = a.integrationCurrent(s, rec); e != nil {
				return e
			}
			obs, e = effect(rec, func() error { return integrationFinalCurrent(s, rec) })
			if e != nil {
				return ErrReconcile
			}
			if !integrationDone(rec, obs) {
				return ErrReconcile
			}
		}
		// Outcome observation is historical effect evidence, never renewed authority.
		rec.State = "integrated"
		rec.Outcome = obs
		rec.CompletedAt = time.Now().UTC()
		rec.ReceiptDigest = identity("native-integration-receipt", "", rec)
		s.NativeIntegrationOperations[r.OperationKey] = rec
		out = NativeIntegrationResult{rec, !fresh}
		return nil
	})
	if err != nil {
		return NativeIntegrationResult{}, err
	}
	return out, nil
}
func (a *Authority) ReadNativeIntegration(owner string, key string) (NativeIntegrationRecord, error) {
	var out NativeIntegrationRecord
	e := a.transaction(false, false, func(s *state) error {
		if !integrationOwner(s, owner) {
			return ErrDenied
		}
		v, ok := s.NativeIntegrationOperations[key]
		if !ok || v.OperationKey != key || v.Input.OperationKey != key || v.InputDigest != identity("native-integration-input", "", v.Input) || (v.State == "integrated" && !validNativeIntegrationOutcome(v)) {
			return ErrDenied
		}
		out = v
		return nil
	})
	if e != nil {
		return NativeIntegrationRecord{}, e
	}
	return out, nil
}

// ValidNativeIntegrationOutcome exposes only a pure receipt predicate for the
// controller's eventual schema18 continuation. It never authorizes a new effect.
func ValidNativeIntegrationOutcome(rec NativeIntegrationRecord) bool {
	return validNativeIntegrationOutcome(rec)
}
func validNativeIntegrationOutcome(rec NativeIntegrationRecord) bool {
	if rec.Policy.TaskID != rec.Input.Candidate.TaskID || rec.Policy.ProjectID != rec.Input.Candidate.ProjectID || rec.Policy.Repository != rec.Input.Candidate.Repository || rec.Policy.Revision != rec.Input.PolicyRevision || rec.Before.Commit != rec.Policy.ExpectedCommit || rec.Before.Tree != rec.Policy.ExpectedTree || !rec.Before.Clean {
		return false
	}
	if rec.State != "integrated" || rec.OperationKey != rec.Input.OperationKey || rec.InputDigest != identity("native-integration-input", "", rec.Input) || !integrationDone(rec, rec.Outcome) || rec.CompletedAt.IsZero() {
		return false
	}
	digest := rec.ReceiptDigest
	rec.ReceiptDigest = ""
	return digest == identity("native-integration-receipt", "", rec)
}
func (a *Authority) ReadNativeIntegrationPolicy(owner, task string) (NativeIntegrationPolicy, error) {
	var out NativeIntegrationPolicy
	e := a.transaction(false, false, func(s *state) error {
		if !integrationOwner(s, owner) {
			return ErrDenied
		}
		p, ok := s.NativeIntegrationPolicies[task]
		if !ok {
			return ErrDenied
		}
		out = p
		return nil
	})
	if e != nil {
		return NativeIntegrationPolicy{}, e
	}
	return out, nil
}
