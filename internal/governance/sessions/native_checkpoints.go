package sessions

import (
	"reflect"
	"sort"
	"time"
)

// CheckpointPolicy enables reconciliation as a mandatory one-wave authority gate.
// Budget is an owner-estimated cycle bound, never provider usage or money.
type CheckpointPolicy struct {
	SchemaVersion                int
	ProjectID, Revision, Harness string
	TaskIDs                      []string
	MaxCycles, MaxNoProgress     int
	ExpiresAt                    time.Time
	RequireEnforcedBudget        bool
}
type CheckpointRefs struct {
	TaskID, DispatchID, IntentID, VerificationKey, EvidenceID, ReviewID string
	IntegrationOperationID, AcceptanceOperationID                       string
	RunnerSession                                                       string
	Verification                                                        VerificationRequest
}
type CheckpointRequest struct {
	OperationKey, CheckpointID, ProjectID, InvocationID, Phase string
	ExpectedSequence                                           uint64
	Refs                                                       []CheckpointRefs
}
type CheckpointFinding struct {
	Class, Knowledge, TaskID, Reference, NextAction string
	Retryable                                       bool
}
type CheckpointFact struct {
	Kind, TaskID, ID, State, Knowledge, LeaseID, HostAgentID, Freshness string
	Fence                                                               uint64
}

type CheckpointStatus struct {
	HistoricalConditionDigest string
	ObservedTrunkRef          string
	Diagnostics               []CheckpointFinding
	PreflightReports          []PreflightReport
	ReconciliationCurrent     bool

	Facts           []CheckpointFact
	BudgetKnowledge string

	SchemaVersion                                                         int
	ID, ProjectID, InvocationID, Phase, PolicyRevision, Harness           string
	Sequence                                                              uint64
	Cycle, NoProgress, MaxCycles, MaxNoProgress                           int
	Refs                                                                  []CheckpointRefs
	Findings                                                              []CheckpointFinding
	HaltClass, NextAction, ObservedTrunk, ConditionDigest, ProgressDigest string
	Retryable, WaveAllowed                                                bool
	ObservedAt, GateExpiresAt                                             time.Time
	ConsumedWave                                                          string
}
type CheckpointReceipt struct {
	Digest string
	Status CheckpointStatus
}
type CheckpointObservation struct {
	Digest, Trunk, TrunkRef string
	Findings                []CheckpointFinding
}
type CheckpointObserver interface {
	ObserveCheckpoint(workspace, project string, tasks []string) (CheckpointObservation, error)
}

func (a *Authority) UpgradeCheckpoints(owner string) error {
	old := *a
	old.version = 11
	return old.transaction(false, true, func(s *state) error {
		if s.Version != 11 || len(owner) != 64 || hash(owner) != s.OwnerHash {
			return ErrDenied
		}
		s.CheckpointPolicies = map[string]CheckpointPolicy{}
		s.CheckpointPolicyHistory = map[string]map[string]CheckpointPolicy{}
		s.Checkpoints = map[string][]CheckpointStatus{}
		s.CheckpointOperations = map[string]CheckpointReceipt{}
		s.Version = 12
		return nil
	})
}
func (a *Authority) ConfigureCheckpoints(owner string, p CheckpointPolicy) error {
	p.ExpiresAt = p.ExpiresAt.UTC()
	p.TaskIDs = append([]string(nil), p.TaskIDs...)
	sort.Strings(p.TaskIDs)
	return a.transaction(false, true, func(s *state) error {
		if s.Version < 12 || len(owner) != 64 || hash(owner) != s.OwnerHash || p.SchemaVersion != 1 || !validID(p.ProjectID) || !validID(p.Revision) || p.Harness != "codex" || p.MaxCycles < 1 || p.MaxCycles > 1000 || p.MaxNoProgress < 1 || p.MaxNoProgress > 100 || len(p.TaskIDs) == 0 || len(p.TaskIDs) > 256 || !time.Now().Before(p.ExpiresAt) || p.ExpiresAt.After(time.Now().Add(24*time.Hour)) {
			return ErrDenied
		}
		sw, ok := s.SwarmPolicies[p.ProjectID]
		if !ok || sw.Harness != p.Harness || len(p.TaskIDs) != len(sw.Placements) {
			return ErrDenied
		}
		for i, t := range p.TaskIDs {
			if !validID(t) || (i > 0 && t == p.TaskIDs[i-1]) {
				return ErrDenied
			}
			if _, ok := sw.Placements[t]; !ok {
				return ErrDenied
			}
		}
		if old, ok := s.CheckpointPolicyHistory[p.ProjectID][p.Revision]; ok {
			if !reflect.DeepEqual(old, p) {
				return ErrDenied
			}
			return nil
		}
		// Bounds cannot be reset by a policy revision while a project has checkpoints.
		for _, history := range s.Checkpoints {
			if len(history) > 0 && history[0].ProjectID == p.ProjectID {
				old := s.CheckpointPolicies[p.ProjectID]
				if old.MaxCycles != p.MaxCycles || old.MaxNoProgress != p.MaxNoProgress || old.Harness != p.Harness || !reflect.DeepEqual(old.TaskIDs, p.TaskIDs) {
					return ErrDenied
				}
			}
		}
		if s.CheckpointPolicyHistory[p.ProjectID] == nil {
			s.CheckpointPolicyHistory[p.ProjectID] = map[string]CheckpointPolicy{}
		}
		s.CheckpointPolicyHistory[p.ProjectID][p.Revision] = p
		s.CheckpointPolicies[p.ProjectID] = p
		return nil
	})
}
func checkpointAuth(s *state, token, session, project, action string) (CheckpointPolicy, error) {
	r, e := controller(s, token, session, project, action)
	if e != nil {
		return CheckpointPolicy{}, e
	}
	p, ok := s.CheckpointPolicies[project]
	if s.Version < 12 || !ok {
		return p, ErrDenied
	}
	for _, t := range p.TaskIDs {
		if !subset([]string{"task:" + t}, r.Scope.Resources) {
			return p, ErrDenied
		}
	}
	return p, nil
}
func checkpointTask(p CheckpointPolicy, t string) bool { return subset([]string{t}, p.TaskIDs) }
func (a *Authority) RecordCheckpoint(token, session string, r CheckpointRequest) (CheckpointStatus, error) {
	return a.writeCheckpoint(token, session, r, false)
}
func (a *Authority) ReconcileCheckpoint(token, session string, r CheckpointRequest) (CheckpointStatus, error) {
	return a.writeCheckpoint(token, session, r, true)
}
func (a *Authority) writeCheckpoint(token, session string, r CheckpointRequest, reconcile bool) (CheckpointStatus, error) {
	var out CheckpointStatus
	e := a.transaction(false, true, func(s *state) error {
		action := "checkpoint.record"
		if reconcile {
			action = "checkpoint.reconcile"
		}
		p, e := checkpointAuth(s, token, session, r.ProjectID, action)
		if e != nil {
			return e
		}
		if !validID(r.OperationKey) || !validID(r.CheckpointID) || !validID(r.InvocationID) || len(r.Refs) > 256 || !subset([]string{r.Phase}, []string{"pre-cycle", "dispatch", "verification", "review", "integration", "acceptance", "halt"}) {
			return ErrDenied
		}
		digest := identity(action, session, r)
		if old, ok := s.CheckpointOperations[r.OperationKey]; ok {
			if old.Digest != digest {
				return ErrDenied
			}
			out = old.Status
			return nil
		}
		for id, h := range s.Checkpoints {
			if id != r.CheckpointID && len(h) > 0 && h[0].ProjectID == r.ProjectID {
				return ErrDenied
			}
		}
		history := s.Checkpoints[r.CheckpointID]
		if reconcile && r.Phase == "pre-cycle" {
			for _, entry := range history {
				if entry.Phase == "pre-cycle" && entry.InvocationID == r.InvocationID && entry.ConditionDigest != "" {
					return ErrDenied
				}
			}
		}
		if uint64(len(history)) != r.ExpectedSequence {
			return ErrDenied
		}
		out = CheckpointStatus{SchemaVersion: 1, ID: r.CheckpointID, ProjectID: r.ProjectID, InvocationID: r.InvocationID, Phase: r.Phase, PolicyRevision: p.Revision, Harness: p.Harness, Sequence: r.ExpectedSequence + 1, Refs: append([]CheckpointRefs(nil), r.Refs...), MaxCycles: p.MaxCycles, MaxNoProgress: p.MaxNoProgress, ObservedAt: time.Now().UTC(), Findings: []CheckpointFinding{}}
		if len(history) > 0 {
			old := history[len(history)-1]
			if old.ProjectID != r.ProjectID || old.Harness != p.Harness {
				return ErrDenied
			}
			out.Cycle = old.Cycle
			out.NoProgress = old.NoProgress
			out.ProgressDigest = old.ProgressDigest
			// Previously observed producer identities cannot be dropped or replaced.
			for _, ref := range old.Refs {
				if !containsCheckpointRef(r.Refs, ref) {
					return ErrDenied
				}
			}
		}
		for _, ref := range r.Refs {
			if !checkpointTask(p, ref.TaskID) || a.validateCheckpointRef(s, p, ref) != nil {
				return ErrDenied
			}
		}
		for _, ref := range r.Refs {
			if ref.VerificationKey != "" {
				op, ok := s.Operations[ref.VerificationKey]
				if ok && op.Pending && op.CheckpointVerification == nil {
					g := s.Grants[ref.Verification.GrantID]
					op.CheckpointVerification = &CheckpointVerificationBinding{ProjectID: p.ProjectID, TaskID: ref.TaskID, RunnerSession: ref.RunnerSession, DescriptorDigest: g.CommandDigest, Request: ref.Verification}
					s.Operations[ref.VerificationKey] = op
				}
			}
		}
		if reconcile {
			current, e := a.reconcileCheckpoint(s, p, out, token, session)
			if e != nil {
				return e
			}
			out = current
			if len(history) > 0 {
				old := history[len(history)-1]
				if out.ProgressDigest == old.ProgressDigest && old.InvocationID != r.InvocationID && r.Phase == "pre-cycle" {
					out.NoProgress++
				} else if old.ProgressDigest != "" && out.ProgressDigest != old.ProgressDigest {
					out.NoProgress = 0
				}
			}
			a.checkpointBounds(&out, p, s.SwarmPolicies[p.ProjectID])
		} else {
			out.HaltClass = "reconciliation_required"
			out.NextAction = "reconcile the recorded producer references before another wave"
		}
		// Reobserve immediately before persistence; legacy canonical files aren't locked.
		if reconcile {
			fresh, e := a.reconcileCheckpoint(s, p, out, token, session)
			if e != nil || fresh.ConditionDigest != out.ConditionDigest {
				return ErrDenied
			}
		}
		if _, e := checkpointAuth(s, token, session, r.ProjectID, action); e != nil {
			return e
		}
		if reconcile && out.WaveAllowed && !time.Now().Before(out.GateExpiresAt) {
			return ErrDenied
		}
		s.Checkpoints[r.CheckpointID] = append(history, out)
		s.CheckpointOperations[r.OperationKey] = CheckpointReceipt{Digest: digest, Status: out}
		return nil
	})
	if e != nil {
		return CheckpointStatus{}, e
	}
	return out, e
}
func containsCheckpointRef(refs []CheckpointRefs, want CheckpointRefs) bool {
	for _, r := range refs {
		if r == want {
			return true
		}
	}
	return false
}
func (a *Authority) CheckpointStatus(token, session, project, id string) (CheckpointStatus, error) {
	var out CheckpointStatus
	e := a.transaction(false, false, func(s *state) error {
		p, e := checkpointAuth(s, token, session, project, "checkpoint.read")
		if e != nil {
			return e
		}
		h := s.Checkpoints[id]
		if len(h) == 0 || h[0].ProjectID != project {
			return ErrDenied
		}
		old := h[len(h)-1]
		for _, r := range old.Refs {
			if !checkpointTask(p, r.TaskID) || a.validateCheckpointRef(s, p, r) != nil {
				return ErrDenied
			}
		}
		out, e = a.reconcileCheckpoint(s, p, old, token, session)
		if e != nil {
			return e
		}
		out.HistoricalConditionDigest = old.ConditionDigest
		out.ReconciliationCurrent = old.ConditionDigest == out.ConditionDigest && time.Now().Before(old.GateExpiresAt) && old.ConsumedWave == ""
		a.checkpointBounds(&out, p, s.SwarmPolicies[project])
		out.WaveAllowed = false
		if _, e := checkpointAuth(s, token, session, project, "checkpoint.read"); e != nil {
			return e
		}
		return nil
	})
	if e != nil {
		return CheckpointStatus{}, e
	}
	return out, e
}
func (a *Authority) checkpointBounds(out *CheckpointStatus, p CheckpointPolicy, sw SwarmPolicy) {
	class, remedy := "", ""
	switch {
	case sw.Stopped:
		class = "stopped"
		remedy = "owner must explicitly change the STOP policy"
	case !time.Now().Before(p.ExpiresAt) || sw.Harness != p.Harness:
		class = "policy_refusal"
		remedy = "owner must refresh the pinned checkpoint policy"
	case p.RequireEnforcedBudget:
		class = "budget_usage_unknown"
		remedy = "enforced usage is unavailable; preserve unknown and obtain an authenticated budget producer"
	case out.Cycle >= p.MaxCycles:
		class = "budget_exhausted"
		remedy = "cycle budget exhausted; owner reconciliation required"
	case out.NoProgress >= p.MaxNoProgress:
		class = "no_progress"
		remedy = "observe a genuine producer or trunk change; bookkeeping cannot reset this counter"
	}
	// Permanent blockers dominate transient observations. Preserve stable source
	// order within each class; STOP and durable bounds are always prepended.
	sort.SliceStable(out.Findings, func(i, j int) bool { return !out.Findings[i].Retryable && out.Findings[j].Retryable })
	if class != "" {
		out.Findings = append([]CheckpointFinding{{Class: class, Knowledge: "known", NextAction: remedy}}, out.Findings...)
	}
	out.BudgetKnowledge = "unknown-provider-usage; owner-estimated-cycle-bound-only"
	out.HaltClass = "ready"
	out.NextAction = "reserve exactly one wave using this generation"
	out.Retryable = true
	out.WaveAllowed = true
	if out.Phase != "pre-cycle" {
		out.WaveAllowed = false
	}
	if len(out.Findings) > 0 {
		f := out.Findings[0]
		out.HaltClass = f.Class
		out.NextAction = f.NextAction
		out.Retryable = f.Retryable
		out.WaveAllowed = false
	}
}

// A fresh reconciliation is consumed once, in the same authority transaction as
// the wave. A historical operation replay returns its wave but grants no new one.
func (a *Authority) consumeCheckpoint(s *state, token, session string, r ReserveWaveRequest, wave string) error {
	p, enabled := s.CheckpointPolicies[r.Plan.ProjectID]
	if !enabled {
		return nil
	}
	if _, e := checkpointAuth(s, token, session, r.Plan.ProjectID, "checkpoint.reconcile"); e != nil {
		return e
	}
	h := s.Checkpoints[r.CheckpointID]
	if len(h) == 0 {
		return ErrDenied
	}
	old := h[len(h)-1]
	if old.ProjectID != p.ProjectID || old.Sequence != r.CheckpointSequence || old.InvocationID != r.InvocationID || old.ConditionDigest != r.ReconciliationDigest || old.ConsumedWave != "" || !old.WaveAllowed || !time.Now().Before(old.GateExpiresAt) {
		return ErrDenied
	}
	fresh, e := a.reconcileCheckpoint(s, p, old, token, session)
	if e != nil {
		return e
	}
	a.checkpointBounds(&fresh, p, s.SwarmPolicies[p.ProjectID])
	if !fresh.WaveAllowed || fresh.ConditionDigest != old.ConditionDigest {
		return ErrDenied
	}
	if _, e := checkpointAuth(s, token, session, r.Plan.ProjectID, "checkpoint.reconcile"); e != nil || !time.Now().Before(old.GateExpiresAt) {
		return ErrDenied
	}
	if wave != "" {
		old.ConsumedWave = wave
		old.Cycle++
		old.WaveAllowed = false
		old.Sequence++
		s.Checkpoints[old.ID] = append(h, old)
	}
	return nil
}
