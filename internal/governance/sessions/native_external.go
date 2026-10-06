package sessions

import (
	"github.com/mlnomadpy/dacli/internal/prci"
	"reflect"
	"strings"
	"time"
)

// External authority is separately owner-selected and optional. Generation,
// source, target and required checks are immutable within a policy revision.
// Private repository/profile selections are never exported through status.
type NativeExternalPolicy struct {
	SchemaVersion                                                      int
	ProjectID, TaskID, Revision, NativePolicyRevision                  string
	Repository, Checkout, GitHubRepository, SourceBranch, TargetBranch string
	Commit, Tree, ExpectedTrunk, ExpectedRemoteHead                    string
	Generation, SliceGeneration                                        int
	RequiredChecks                                                     []string
	AllowPush, AllowCreatePR, Required, Stopped                        bool
	ExpiresAt, ConfiguredAt                                            time.Time
}
type NativeExternalInput struct {
	OperationKey, PolicyRevision, Action string
	Candidate                            NativeAcceptanceInput
}
type NativeExternalObservation struct {
	Generation, SliceGeneration                   int
	Branch, Commit, Tree, Trunk, RemoteHead       string
	Clean                                         bool
	PullRequestNumber                             int
	PullRequestURL, PullRequestState, MergeCommit string
	ChecksSatisfied, APIComplete                  bool
	Diagnosis                                     prci.Result
	ObservedAt                                    time.Time
}
type NativeExternalRecord struct {
	OperationKey, InputDigest, State, ReceiptDigest string
	Input                                           NativeExternalInput
	Policy                                          NativeExternalPolicy
	Before, Outcome                                 NativeExternalObservation
	CreatedAt, UpdatedAt                            time.Time
}
type NativeExternalResult struct {
	Record    NativeExternalRecord
	Duplicate bool
}

func externalOID(v string) bool {
	return (len(v) == 40 || len(v) == 64) && strings.Trim(v, "0123456789abcdef") == ""
}
func externalOwner(s *state, owner string) bool {
	return s.Version >= 19 && len(owner) == 64 && hash(owner) == s.OwnerHash
}
func (a *Authority) UpgradeNativeExternal(owner string) error {
	return a.transaction(false, true, func(s *state) error {
		if s.Version != 18 || len(owner) != 64 || hash(owner) != s.OwnerHash {
			return ErrDenied
		}
		s.NativeExternalPolicies = map[string]NativeExternalPolicy{}
		s.NativeExternalHistory = map[string]NativeExternalPolicy{}
		s.NativeExternalOperations = map[string]NativeExternalRecord{}
		s.Version = 19
		return nil
	})
}
func (a *Authority) ConfigureNativeExternal(owner string, p NativeExternalPolicy) (NativeExternalPolicy, error) {
	var out NativeExternalPolicy
	p.ExpiresAt = p.ExpiresAt.Round(0).UTC()
	p.RequiredChecks = append([]string(nil), p.RequiredChecks...)
	e := a.transaction(false, true, func(s *state) error {
		if !externalOwner(s, owner) || p.SchemaVersion != 1 || !validID(p.ProjectID) || !validID(p.TaskID) || !validID(p.Revision) || !validID(p.NativePolicyRevision) || p.Repository == "" || p.Checkout == "" || p.GitHubRepository == "" || p.SourceBranch == "" || p.TargetBranch == "" || !externalOID(p.Commit) || !externalOID(p.Tree) || !externalOID(p.ExpectedTrunk) || (p.ExpectedRemoteHead != "" && !externalOID(p.ExpectedRemoteHead)) || p.Generation < 0 || p.SliceGeneration < 0 || len(p.RequiredChecks) == 0 || !p.ConfiguredAt.IsZero() || !time.Now().Before(p.ExpiresAt) {
			return ErrDenied
		}
		names := map[string]bool{}
		for _, c := range p.RequiredChecks {
			if c == "" || names[c] {
				return ErrDenied
			}
			names[c] = true
		}
		b, ok := s.Tasks[p.TaskID]
		if !ok || b.ProjectID != p.ProjectID || b.Repository != p.Repository {
			return ErrDenied
		}
		key := p.TaskID + ":" + p.Revision
		if old, ok := s.NativeExternalHistory[key]; ok {
			copy := old
			copy.ConfiguredAt = time.Time{}
			if !reflect.DeepEqual(copy, p) {
				return ErrDenied
			}
			out = old
			return nil
		}
		p.ConfiguredAt = time.Now().UTC()
		s.NativeExternalHistory[key] = p
		s.NativeExternalPolicies[p.TaskID] = p
		out = p
		return nil
	})
	if e != nil {
		return NativeExternalPolicy{}, e
	}
	return out, nil
}
func externalBound(r NativeExternalRecord, o NativeExternalObservation) bool {
	p := r.Policy
	return o.Generation == p.Generation && o.SliceGeneration == p.SliceGeneration && o.Branch == p.SourceBranch && o.Commit == p.Commit && o.Tree == p.Tree && o.Clean && !o.ObservedAt.IsZero()
}
func externalInputBound(p NativeExternalPolicy, r NativeExternalInput) bool {
	c := r.Candidate
	return p.Revision == r.PolicyRevision && p.ProjectID == c.ProjectID && p.TaskID == c.TaskID && p.Repository == c.Repository && p.Commit == c.Commit && p.Tree == c.Tree
}
func externalPolicyCurrent(s *state, r NativeExternalRecord) error {
	p, ok := s.NativeExternalPolicies[r.Policy.TaskID]
	n, nok := s.NativePolicies[r.Policy.TaskID]
	if !ok || !reflect.DeepEqual(p, r.Policy) || p.Stopped || !time.Now().Before(p.ExpiresAt) || !nok || n.Revision != p.NativePolicyRevision || !n.Allow || n.Stopped || !time.Now().Before(n.ExpiresAt) {
		return ErrDenied
	}
	return nil
}
func externalFinalCurrent(s *state, r NativeExternalRecord) error {
	if externalPolicyCurrent(s, r) != nil {
		return ErrDenied
	}
	// Reuse the exact trusted verification/review and current native authority
	// predicate from local integration. External authority cannot replace it.

	// integrationFinalCurrent also checks integration policy; perform its pure
	// producer checks here without inventing an integration policy association.
	input := r.Input.Candidate
	review, ok := s.Reviews[input.ReviewID]
	if !ok || review.Request.Outcome != "approve" || review.Observation.ChecklistDigest != input.ChecklistDigest || review.Observation.PolicyRevision != input.PolicyRevision || !review.CreatedAt.After(r.Policy.ConfiguredAt) {
		return ErrDenied
	}
	at, e := reviewAttempt(s, input.AttemptID)
	if e != nil || at.ProjectID != r.Policy.ProjectID || at.Request.TaskID != r.Policy.TaskID {
		return ErrDenied
	}
	rev, e := currentRevision(s, at, input.RevisionID, input.LeaseID, input.ExpectedFence)
	if e != nil || rev.Observation.Commit != input.Commit || rev.Observation.Tree != input.Tree || rev.Observation.Repository != input.Repository || !reflect.DeepEqual(rev, review.Revision) || !reviewGrantCurrent(s, review.IssuerSession, at, s.ReviewGrants[review.Request.GrantID]) || reviewWorkerExcept(s, at, review.Request, "") != nil || !reviewEvidence(s, at, rev, review.Request.EvidenceIDs, review.Observation.Checks) {
		return ErrDenied
	}
	for _, id := range review.Request.EvidenceIDs {
		ev, ok := s.Evidence[id]
		if !ok || !ev.RecordedAt.After(r.Policy.ConfiguredAt) {
			return ErrDenied
		}
	}
	return nil
}
func (a *Authority) externalCurrent(s *state, r NativeExternalRecord) error {
	if externalPolicyCurrent(s, r) != nil {
		return ErrDenied
	}
	if _, e := a.acceptanceCurrent(s, r.Input.Candidate, ""); e != nil {
		return e
	}
	return externalFinalCurrent(s, r)
}
func externalEffectDone(r NativeExternalRecord, o NativeExternalObservation) bool {
	if !externalBound(r, o) || !o.APIComplete {
		return false
	}
	switch r.Input.Action {
	case "push":
		return o.RemoteHead == r.Policy.Commit
	case "create_pr":
		return o.RemoteHead == r.Policy.Commit && o.PullRequestNumber > 0 && o.PullRequestURL != "" && (o.PullRequestState == "OPEN" || o.PullRequestState == "MERGED")
	}
	return false
}
func sealExternal(r NativeExternalRecord) NativeExternalRecord {
	r.ReceiptDigest = ""
	r.ReceiptDigest = identity("native-external-receipt", "", r)
	return r
}
func validExternalRecord(r NativeExternalRecord) bool {
	d := r.ReceiptDigest
	r.ReceiptDigest = ""
	return d != "" && d == identity("native-external-receipt", "", r) && r.InputDigest == identity("native-external-input", "", r.Input) && r.OperationKey == r.Input.OperationKey && externalInputBound(r.Policy, r.Input)
}
func (a *Authority) ApplyNativeExternal(owner string, input NativeExternalInput, inspect func(NativeExternalRecord) (NativeExternalObservation, error), effect func(NativeExternalRecord, func() error) (NativeExternalObservation, error)) (NativeExternalResult, error) {
	fresh := false
	e := a.transaction(false, true, func(s *state) error {
		if !externalOwner(s, owner) || inspect == nil || effect == nil || !validID(input.OperationKey) || !validID(input.PolicyRevision) || (input.Action != "push" && input.Action != "create_pr") || nativeAcceptanceOwner(s, owner, input.Candidate, a.binding) != nil {
			return ErrDenied
		}
		if old, ok := s.NativeExternalOperations[input.OperationKey]; ok {
			if !validExternalRecord(old) || !reflect.DeepEqual(old.Input, input) {
				return ErrDenied
			}
			return nil
		}
		p, ok := s.NativeExternalPolicies[input.Candidate.TaskID]
		if !ok || !externalInputBound(p, input) || (input.Action == "push" && !p.AllowPush) || (input.Action == "create_pr" && !p.AllowCreatePR) {
			return ErrDenied
		}
		for _, old := range s.NativeExternalOperations {
			if old.Policy.TaskID == p.TaskID && old.Policy.Revision == p.Revision && old.Input.Action == input.Action {
				return ErrReconcile
			}
			if old.State == "pending" && old.Policy.GitHubRepository == p.GitHubRepository && old.Policy.SourceBranch == p.SourceBranch {
				return ErrReconcile
			}
		}
		r := NativeExternalRecord{OperationKey: input.OperationKey, InputDigest: identity("native-external-input", "", input), State: "pending", Input: input, Policy: p, CreatedAt: time.Now().UTC()}
		if e := a.externalCurrent(s, r); e != nil {
			return e
		}
		obs, e := inspect(r)
		if e != nil {
			return e
		}
		if !externalBound(r, obs) || !obs.APIComplete || obs.Trunk != p.ExpectedTrunk || (input.Action == "push" && obs.RemoteHead != p.ExpectedRemoteHead) || (input.Action == "create_pr" && (obs.RemoteHead != p.Commit || obs.PullRequestNumber != 0)) {
			return ErrDenied
		}
		if externalFinalCurrent(s, r) != nil {
			return ErrDenied
		}
		r.Before = obs
		r.UpdatedAt = r.CreatedAt
		s.NativeExternalOperations[input.OperationKey] = sealExternal(r)
		fresh = true
		return nil
	})
	if e != nil {
		return NativeExternalResult{}, e
	}
	return a.continueNativeExternal(owner, input, inspect, effect, fresh)
}
func (a *Authority) ReconcileNativeExternal(owner string, input NativeExternalInput, inspect func(NativeExternalRecord) (NativeExternalObservation, error)) (NativeExternalResult, error) {
	return a.continueNativeExternal(owner, input, inspect, nil, false)
}
func (a *Authority) continueNativeExternal(owner string, input NativeExternalInput, inspect func(NativeExternalRecord) (NativeExternalObservation, error), effect func(NativeExternalRecord, func() error) (NativeExternalObservation, error), fresh bool) (NativeExternalResult, error) {
	var out NativeExternalResult
	e := a.transaction(false, true, func(s *state) error {
		r, ok := s.NativeExternalOperations[input.OperationKey]
		if !externalOwner(s, owner) || !ok || inspect == nil || !validExternalRecord(r) || !reflect.DeepEqual(r.Input, input) {
			return ErrDenied
		}
		// Reconciliation is read-only remote recovery even for a known effect: CI,
		// trunk and auto-merge may have changed. It never grants another effect.
		obs, e := inspect(r)
		if e != nil {
			return e
		}
		if !externalBound(r, obs) {
			return ErrDenied
		}
		if r.State == "pending" && !externalEffectDone(r, obs) && fresh {
			if effect == nil || !obs.APIComplete || obs.Trunk != r.Before.Trunk || obs.RemoteHead != r.Before.RemoteHead || obs.PullRequestNumber != r.Before.PullRequestNumber {
				return ErrReconcile
			}
			if e = a.externalCurrent(s, r); e != nil {
				return e
			}
			obs, e = effect(r, func() error { return externalFinalCurrent(s, r) })
			if e != nil {
				return ErrReconcile
			}
			if !externalBound(r, obs) {
				return ErrReconcile
			}
		}
		r.Outcome = obs
		r.UpdatedAt = time.Now().UTC()
		if externalEffectDone(r, obs) {
			r.State = "observed"
		}
		r = sealExternal(r)
		s.NativeExternalOperations[input.OperationKey] = r
		out = NativeExternalResult{r, !fresh}
		return nil
	})
	if e != nil {
		return NativeExternalResult{}, e
	}
	return out, nil
}
func (a *Authority) ReadNativeExternal(owner, key string) (NativeExternalRecord, error) {
	var out NativeExternalRecord
	e := a.transaction(false, false, func(s *state) error {
		r, ok := s.NativeExternalOperations[key]
		if !externalOwner(s, owner) || !ok || !validExternalRecord(r) {
			return ErrDenied
		}
		out = r
		return nil
	})
	if e != nil {
		return NativeExternalRecord{}, e
	}
	return out, nil
}
func (a *Authority) ReadNativeExternalPolicy(owner, task string) (NativeExternalPolicy, error) {
	var out NativeExternalPolicy
	e := a.transaction(false, false, func(s *state) error {
		if !externalOwner(s, owner) {
			return ErrDenied
		}
		p, ok := s.NativeExternalPolicies[task]
		if !ok {
			return ErrDenied
		}
		out = p
		return nil
	})
	if e != nil {
		return NativeExternalPolicy{}, e
	}
	return out, nil
}

// Required external delivery is a fresh diagnostic gate, not acceptance. The
// caller must still consume the independent local integration and close receipts.
func nativeExternalGate(s *state, task string, candidate NativeAcceptanceInput) (string, string, bool) {
	p, ok := s.NativeExternalPolicies[task]
	if !ok || !p.Required {
		return "", "", true
	}
	if p.Stopped || !time.Now().Before(p.ExpiresAt) {
		return "external_policy_refusal", "owner restores separately configured external authority; preserve loop bounds", false
	}
	latest := nativeExternalLatest(s, task, candidate)
	if latest.OperationKey == "" || !externalBound(latest, latest.Outcome) || NativeExternalFreshness(latest.Outcome) != "fresh" {
		return "external_observation_unknown", "owner observes the original external operation with exact generation/head/trunk; never relaunch unknown effect", false
	}
	o := latest.Outcome
	if o.Trunk != p.ExpectedTrunk && !(o.PullRequestState == "MERGED" && o.MergeCommit == p.Commit && o.Trunk == p.Commit) {
		return "stale_pending_landing", "trunk advanced: repin resulting tree and obtain fresh verification/review without resetting controller counters", false
	}
	if o.PullRequestState == "MERGED" && o.MergeCommit == p.Commit && o.Trunk == p.Commit && o.ChecksSatisfied {
		return "", "", true
	}
	if o.Diagnosis.Code == "ready" {
		return "pending_external_merge", "wait for separately authorized external merge; readiness is not a merge receipt", false
	}
	if o.Diagnosis.Code == "" {
		return "external_observation_unknown", "owner reconciles exact original external observation", false
	}
	return o.Diagnosis.Code, o.Diagnosis.Next, false
}

func NativeExternalFreshness(o NativeExternalObservation) string {
	if o.ObservedAt.IsZero() || !o.APIComplete || o.ObservedAt.After(time.Now()) {
		return "unknown"
	}
	if time.Since(o.ObservedAt) > 30*time.Second {
		return "stale"
	}
	return "fresh"
}
func nativeExternalLatest(s *state, task string, candidate NativeAcceptanceInput) NativeExternalRecord {
	p := s.NativeExternalPolicies[task]
	var latest NativeExternalRecord
	for _, r := range s.NativeExternalOperations {
		if r.Policy.TaskID == task && reflect.DeepEqual(r.Policy, p) && reflect.DeepEqual(r.Input.Candidate, candidate) && validExternalRecord(r) && (r.UpdatedAt.After(latest.UpdatedAt) || (r.UpdatedAt.Equal(latest.UpdatedAt) && r.OperationKey > latest.OperationKey)) {
			latest = r
		}
	}
	return latest
}
func nativeExternalCheckpointFact(s *state, task string, candidate NativeAcceptanceInput) (CheckpointFact, string) {
	r := nativeExternalLatest(s, task, candidate)
	if r.OperationKey == "" {
		return CheckpointFact{}, ""
	}
	return CheckpointFact{Kind: "external", TaskID: task, ID: r.OperationKey, State: r.State + ":" + r.Outcome.Diagnosis.Code, Knowledge: "known", LeaseID: candidate.LeaseID, Fence: candidate.ExpectedFence, Freshness: NativeExternalFreshness(r.Outcome)}, r.OperationKey
}
