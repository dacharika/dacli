package sessions

import (
	"encoding/json"
	"github.com/mlnomadpy/dacli/internal/team"
	"math"
	"reflect"
	"sort"
	"strings"
	"time"
)

// BudgetEstimate numbers are owner estimates in tokens, NOT provider usage or host
// spending limits. No accounting transition confers release or execution rights.
type BudgetEstimate struct {
	RoleID, Model, Provenance string
	Tokens                    int64
	Samples                   int
	ObservedAt                time.Time
}
type BudgetTask struct {
	Phases                                        map[string]BudgetEstimate
	Alternatives                                  map[string]BudgetEstimate
	IntegrationOperationID, AcceptanceOperationID string
}
type BudgetPolicy struct {
	SchemaVersion                                    int
	ProjectID, Revision, Harness, CycleID, Rationale string
	CycleLimit, WindowLimit, RecoveryFloor           int64
	Window                                           time.Duration
	WindowOrigin, ExpiresAt, CooldownUntil           time.Time
	Tasks                                            map[string]BudgetTask
}
type BudgetPhase struct {
	Phase                      string
	Estimate                   BudgetEstimate
	State, ProducerID, Outcome string
	SettledAt                  time.Time
	// ProviderUsage remains nil: current native producers do not measure billing.
	ProviderUsage *int64
}
type BudgetAllocation struct {
	AvailableProducers                                                                       []BudgetProducer `json:",omitempty"`
	ID, DispatchID, WaveID, ProjectID, TaskID, PolicyRevision, CycleID, Harness, QuoteDigest string
	ReservationFence                                                                         uint64
	AllocatedAt                                                                              time.Time
	Phases                                                                                   []BudgetPhase
}
type BudgetReceipt struct {
	Digest     string
	Allocation BudgetAllocation
}
type BudgetTotals struct{ Limit, Spent, Reserved, Remaining int64 }
type BudgetStatus struct {
	SchemaVersion                                                                             int
	ProjectID, PolicyRevision, CycleID, Units, Provenance, Enforcement, UsageKnowledge        string
	ObservedAt, CooldownUntil                                                                 time.Time
	WindowReset                                                                               *time.Time
	Window, Cycle                                                                             BudgetTotals
	ReviewReserved, IntegrationReserved, RecoveryReserved, RecoveryFloor, UnallocatedHeadroom int64
	ProviderUsage                                                                             *int64
	STOP                                                                                      bool
	Allocations                                                                               []BudgetAllocation
	LedgerDigest                                                                              string
}
type BudgetAlternative struct {
	RoleID, Model, Reason                  string
	MarginalEstimate                       *int64
	CostTier                               int
	EstimateKnowledge, CapabilityKnowledge string
	Capable, Selected                      bool
}
type BudgetQuote struct {
	SchemaVersion                                                                     int
	ProjectID, TaskID, PolicyRevision, CycleID, LedgerDigest, Digest, Reason, Harness string
	Allowed                                                                           bool
	Total, Implementation, Review, Correction, Integration, Recovery                  int64
	Phases                                                                            []BudgetPhase
	Alternatives                                                                      []BudgetAlternative
	// Only local allocation admission is enforced; host/provider spend is unknown.
	Enforcement, UsageKnowledge string
}
type BudgetSettleRequest struct {
	OperationKey, DispatchID, Phase, ProducerID string
	ExpectedFence                               uint64
}

func (a *Authority) UpgradeBudgets(owner string) error {
	old := *a
	old.version = 12
	return old.transaction(false, true, func(s *state) error {
		if s.Version != 12 || len(owner) != 64 || hash(owner) != s.OwnerHash {
			return ErrDenied
		}
		s.BudgetPolicies = map[string]BudgetPolicy{}
		s.BudgetPolicyHistory = map[string]map[string]BudgetPolicy{}
		s.BudgetAllocations = map[string]BudgetAllocation{}
		s.BudgetOperations = map[string]BudgetReceipt{}
		s.Version = 13
		return nil
	})
}
func budgetPhaseName(name string) bool {
	switch name {
	case "implementation", "review", "correction", "re-review", "verification", "integration", "recovery":
		return true
	}
	return false
}
func budgetAdd(x, y int64) (int64, error) {
	if x < 0 || y < 0 || x > math.MaxInt64-y {
		return 0, ErrDenied
	}
	return x + y, nil
}

// canonicalBudgetPolicy compares instants independently of JSON offsets and Go monotonic clocks.
// Copy the estimate maps so normalization cannot mutate the caller or historical authority.
func canonicalBudgetPolicy(p BudgetPolicy) BudgetPolicy {
	p.WindowOrigin = p.WindowOrigin.Round(0).UTC()
	p.ExpiresAt = p.ExpiresAt.Round(0).UTC()
	p.CooldownUntil = p.CooldownUntil.Round(0).UTC()
	tasks := make(map[string]BudgetTask, len(p.Tasks))
	for id, task := range p.Tasks {
		phases := make(map[string]BudgetEstimate, len(task.Phases))
		for phase, estimate := range task.Phases {
			estimate.ObservedAt = estimate.ObservedAt.Round(0).UTC()
			phases[phase] = estimate
		}
		task.Phases = phases
		if task.Alternatives != nil {
			alternatives := make(map[string]BudgetEstimate, len(task.Alternatives))
			for role, estimate := range task.Alternatives {
				estimate.ObservedAt = estimate.ObservedAt.Round(0).UTC()
				alternatives[role] = estimate
			}
			task.Alternatives = alternatives
		}
		tasks[id] = task
	}
	p.Tasks = tasks
	return p
}
func (a *Authority) ConfigureBudget(owner string, p BudgetPolicy) error {
	p = canonicalBudgetPolicy(p)
	return a.transaction(false, true, func(s *state) error {
		now := time.Now()
		if s.Version < 13 || len(owner) != 64 || hash(owner) != s.OwnerHash || p.SchemaVersion != 1 || !validID(p.ProjectID) || !validID(p.Revision) || !validID(p.CycleID) || p.Harness != "codex" || strings.TrimSpace(p.Rationale) == "" || len(p.Rationale) > 8192 || p.CycleLimit <= 0 || p.WindowLimit <= 0 || p.RecoveryFloor < 0 || p.RecoveryFloor >= p.CycleLimit || p.RecoveryFloor >= p.WindowLimit || p.Window <= 0 || p.Window > 30*24*time.Hour || p.WindowOrigin.IsZero() || p.WindowOrigin.After(now) || !now.Before(p.ExpiresAt) || p.ExpiresAt.After(now.Add(24*time.Hour)) || p.CooldownUntil.After(p.ExpiresAt) || len(p.Tasks) == 0 || len(p.Tasks) > 256 {
			return ErrDenied
		}
		swarm, ok := s.SwarmPolicies[p.ProjectID]
		if !ok || swarm.Harness != p.Harness {
			return ErrDenied
		}
		for task, t := range p.Tasks {
			spec, ok := s.PreflightPolicies[p.ProjectID].Tasks[task]
			if !ok || len(t.Phases) < 3 || len(t.Phases) > 7 {
				return ErrDenied
			}
			for _, phase := range []string{"implementation", "correction", "recovery"} {
				if _, ok := t.Phases[phase]; !ok {
					return ErrDenied
				}
			}
			if spec.RequireReview {
				for _, phase := range []string{"review", "re-review"} {
					if _, ok := t.Phases[phase]; !ok {
						return ErrDenied
					}
				}
			}
			if spec.RequireIntegration {
				if _, ok := t.Phases["integration"]; !ok {
					return ErrDenied
				}
			}
			if spec.RequireVerification {
				if _, ok := t.Phases["verification"]; !ok {
					return ErrDenied
				}
			}
			if t.IntegrationOperationID != "" && !validID(t.IntegrationOperationID) || t.AcceptanceOperationID != "" && !validID(t.AcceptanceOperationID) || len(t.Alternatives) > 32 {
				return ErrDenied
			}
			for role, v := range t.Alternatives {
				if !subset([]string{role}, spec.FallbackRoles) || v.RoleID != role || !validID(v.Model) || v.Tokens <= 0 || v.Provenance != "owner-estimate" || v.Samples < 0 || v.ObservedAt.IsZero() || v.ObservedAt.After(now) || v.ObservedAt.Before(now.Add(-30*24*time.Hour)) {
					return ErrDenied
				}
			}
			total := int64(0)
			for phase, v := range t.Phases {
				if !budgetPhaseName(phase) || v.Tokens < 0 || v.Tokens == 0 && phase != "verification" || v.Samples < 0 || v.Provenance != "owner-estimate" || v.ObservedAt.IsZero() || v.ObservedAt.After(now) || v.ObservedAt.Before(now.Add(-30*24*time.Hour)) {
					return ErrDenied
				}
				role := spec.ImplementationRole
				if phase == "review" || phase == "re-review" {
					role = spec.ReviewRole
				}
				if phase == "recovery" {
					role = spec.RecoveryRole
				}
				if phase != "verification" && (v.RoleID != role || !validID(v.Model)) {
					return ErrDenied
				}
				var e error
				total, e = budgetAdd(total, v.Tokens)
				if e != nil {
					return e
				}
			}
		}
		if old, ok := s.BudgetPolicyHistory[p.ProjectID][p.Revision]; ok {
			if !reflect.DeepEqual(canonicalBudgetPolicy(old), p) {
				return ErrDenied
			}
			return nil
		}
		// Accounting windows and cycle limits cannot be reset while reusing their IDs.
		if old, ok := s.BudgetPolicies[p.ProjectID]; ok {
			if !old.WindowOrigin.Equal(p.WindowOrigin) || old.Window != p.Window {
				return ErrDenied
			}
			if old.CycleID == p.CycleID && old.CycleLimit != p.CycleLimit {
				return ErrDenied
			}
		}
		if s.BudgetPolicyHistory[p.ProjectID] == nil {
			s.BudgetPolicyHistory[p.ProjectID] = map[string]BudgetPolicy{}
		}
		s.BudgetPolicyHistory[p.ProjectID][p.Revision] = p
		s.BudgetPolicies[p.ProjectID] = p
		return nil
	})
}
func budgetAuth(s *state, token, session, project, action string) (BudgetPolicy, error) {
	if _, e := controller(s, token, session, project, action); e != nil {
		return BudgetPolicy{}, e
	}
	p, ok := s.BudgetPolicies[project]
	if s.Version < 13 || !ok {
		return BudgetPolicy{}, ErrDenied
	}
	for task := range p.Tasks {
		if !subset([]string{"task:" + task}, s.Sessions[session].Scope.Resources) {
			return BudgetPolicy{}, ErrDenied
		}
	}
	for _, alloc := range s.BudgetAllocations {
		if alloc.ProjectID == project && !subset([]string{"task:" + alloc.TaskID}, s.Sessions[session].Scope.Resources) {
			return BudgetPolicy{}, ErrDenied
		}
	}
	return p, nil
}
func budgetStatus(s *state, p BudgetPolicy, now time.Time) (BudgetStatus, error) {
	out := BudgetStatus{SchemaVersion: 1, ProjectID: p.ProjectID, PolicyRevision: p.Revision, CycleID: p.CycleID, Units: "owner-estimated-tokens", Provenance: "owner-estimate", Enforcement: "local-allocation-only; provider/host spend unsupported", UsageKnowledge: "unknown", ObservedAt: now, RecoveryFloor: p.RecoveryFloor, CooldownUntil: p.CooldownUntil, STOP: s.SwarmPolicies[p.ProjectID].Stopped, Allocations: []BudgetAllocation{}}
	if now.Before(p.WindowOrigin) {
		return BudgetStatus{}, ErrDenied
	}
	start := now.Add(-p.Window)
	activeCharges := []string{}
	out.Window.Limit = p.WindowLimit
	out.Cycle.Limit = p.CycleLimit
	ids := []string{}
	for id, alloc := range s.BudgetAllocations {
		if alloc.ProjectID == p.ProjectID {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	add := func(target *int64, value int64) error { v, e := budgetAdd(*target, value); *target = v; return e }
	for _, id := range ids {
		alloc := s.BudgetAllocations[id]
		if alloc.AllocatedAt.After(now) {
			return BudgetStatus{}, ErrDenied
		}
		publicAllocation := alloc
		publicAllocation.AvailableProducers = budgetProducers(s, alloc)
		out.Allocations = append(out.Allocations, publicAllocation)
		for _, phase := range alloc.Phases {
			settled := phase.State == "charged"
			if settled && (phase.SettledAt.IsZero() || phase.SettledAt.After(now)) {
				return BudgetStatus{}, ErrDenied
			}
			if !settled {
				if e := add(&out.Window.Reserved, phase.Estimate.Tokens); e != nil {
					return BudgetStatus{}, e
				}
				// Old-cycle outstanding liabilities remain protected in the new cycle too.
				if e := add(&out.Cycle.Reserved, phase.Estimate.Tokens); e != nil {
					return BudgetStatus{}, e
				}
				var field *int64
				switch phase.Phase {
				case "review", "re-review":
					field = &out.ReviewReserved
				case "integration":
					field = &out.IntegrationReserved
				case "recovery":
					field = &out.RecoveryReserved
				}
				if field != nil {
					if e := add(field, phase.Estimate.Tokens); e != nil {
						return BudgetStatus{}, e
					}
				}
			} else {
				if phase.SettledAt.After(start) {
					activeCharges = append(activeCharges, alloc.ID+":"+phase.Phase)
					relief := phase.SettledAt.Add(p.Window)
					if out.WindowReset == nil || relief.Before(*out.WindowReset) {
						out.WindowReset = &relief
					}
					if e := add(&out.Window.Spent, phase.Estimate.Tokens); e != nil {
						return BudgetStatus{}, e
					}
				}
				if alloc.CycleID == p.CycleID {
					if e := add(&out.Cycle.Spent, phase.Estimate.Tokens); e != nil {
						return BudgetStatus{}, e
					}
				}
			}
		}
	}
	for _, t := range []*BudgetTotals{&out.Window, &out.Cycle} {
		used, e := budgetAdd(t.Spent, t.Reserved)
		if e != nil {
			return BudgetStatus{}, e
		}
		t.Remaining = t.Limit - used
		if t.Remaining < 0 {
			t.Remaining = 0
		}
	}
	out.UnallocatedHeadroom = out.Cycle.Remaining
	if out.Window.Remaining < out.UnallocatedHeadroom {
		out.UnallocatedHeadroom = out.Window.Remaining
	}
	out.UnallocatedHeadroom -= p.RecoveryFloor
	if out.UnallocatedHeadroom < 0 {
		out.UnallocatedHeadroom = 0
	}
	out.LedgerDigest = identity("budget.ledger", "", struct {
		Policy        BudgetPolicy
		Allocations   []BudgetAllocation
		ActiveCharges []string
	}{p, out.Allocations, activeCharges})
	return out, nil
}
func (a *Authority) BudgetStatus(token, session, project string) (BudgetStatus, error) {
	var out BudgetStatus
	e := a.transaction(false, false, func(s *state) error {
		p, e := budgetAuth(s, token, session, project, "budget.read")
		if e != nil {
			return e
		}
		out, e = budgetStatus(s, p, time.Now().UTC())
		return e
	})
	if e != nil {
		return BudgetStatus{}, e
	}
	return out, nil
}

// quoteBudget resolves trusted inputs for the single pure calculator used by preview, reserve and live
// admission. already is the cost of earlier selected tasks in this same wave.
func (a *Authority) quoteBudget(s *state, project, task string, report *PreflightReport, already int64) (*BudgetQuote, error) {
	p, enabled := s.BudgetPolicies[project]
	if !enabled {
		return nil, nil
	}
	status, e := budgetStatus(s, p, time.Now().UTC())
	if e != nil {
		return nil, e
	}
	q, e := calculateBudgetQuote(p, task, status, report, already, time.Now().UTC())
	if e != nil {
		return nil, e
	}
	spec, ok := p.Tasks[task]
	if !ok {
		return finishBudgetQuote(q, report), nil
	}

	// Validate the selected model against fresh canonical preflight observations.
	observer, ok := a.admission.(PreflightObserver)
	if !ok {
		return nil, ErrDenied
	}
	preflight := s.PreflightPolicies[project].Tasks[task]
	obs, e := observer.ObservePreflight(s.Binding.Workspace, project, task, preflight, s.SwarmPolicies[project].Placements[task])
	if e != nil {
		return nil, e
	}
	roles := map[string][]team.Role{"implementation": {obs.Implementation}, "review": {obs.Review}, "recovery": {obs.Recovery}, "fallback": obs.Fallback}
	for phase, roleSet := range roles {
		for _, role := range roleSet {
			for _, capacity := range report.RoleCapacities {
				if capacity.Phase == phase && capacity.RoleID == role.Name && capacity.RoleDigest != roleDigest(role) {
					return nil, ErrDenied
				}
			}
		}
	}

	if obs.TaskDigest != report.TaskDigest || obs.RosterDigest != report.RosterDigest || obs.ClaimsDigest != report.ClaimsDigest || obs.ProfileDigest != report.ProfileDigest || !time.Now().Before(obs.ExpiresAt) {
		return nil, ErrDenied
	}
	for name, estimate := range spec.Phases {
		model := obs.Implementation.ModelID()
		if name == "review" || name == "re-review" {
			model = obs.Review.ModelID()
		}
		if name == "recovery" {
			model = obs.Recovery.ModelID()
		}
		role := obs.Implementation.Name
		if name == "review" || name == "re-review" {
			role = obs.Review.Name
		}
		if name == "recovery" {
			role = obs.Recovery.Name
		}
		if name != "verification" && (estimate.Model != model || estimate.RoleID != role) {
			q.Allowed = false
			q.Reason = "owner_estimate_model_binding_changed"
		}
	}
	chosen := spec.Phases["implementation"]
	selectedEstimate := q.Total
	q.Alternatives = append(q.Alternatives, BudgetAlternative{RoleID: chosen.RoleID, Model: chosen.Model, MarginalEstimate: &selectedEstimate, CostTier: obs.Implementation.Profile.CostTier, EstimateKnowledge: "owner-estimate", CapabilityKnowledge: "fresh canonical/private-profile preflight; native model availability unobserved", Selected: q.Allowed, Capable: report.Allowed, Reason: q.Reason})
	for _, role := range obs.Fallback {
		capable := report.Allowed && role.Runtime == p.Harness && implementationCharter(role.Kind) && role.Grant == "rw" && role.Profile.ContextLimit >= obs.Implementation.Profile.ContextLimit
		for _, capacity := range report.RoleCapacities {
			if capacity.Phase == "fallback" && capacity.RoleID == role.Name && !capacity.Capacity.Fits && capacity.OverrideID == "" {
				capable = false
			}
		}
		for _, resource := range obs.ClaimResources {
			if strings.HasPrefix(resource, "path:") && !role.InScope(strings.TrimPrefix(resource, "path:")) {
				capable = false
			}
		}
		for _, tag := range obs.Implementation.Profile.CapabilityTags {
			if !subset([]string{tag}, role.Profile.CapabilityTags) {
				capable = false
			}
		}
		alternative := BudgetAlternative{RoleID: role.Name, Model: role.ModelID(), CostTier: role.Profile.CostTier, Capable: capable, EstimateKnowledge: "unknown", CapabilityKnowledge: "canonical charter/grant/capacity/scope/context/tags and current phase preflight only; native model availability unobserved"}
		alternative.Reason = "owner credential/profile pins selected implementation role; fallback requires explicit owner rebind"
		if role.Profile.CostTier > 0 && role.Profile.CostTier < 99 && obs.Implementation.Profile.CostTier > 0 && role.Profile.CostTier < obs.Implementation.Profile.CostTier {
			alternative.Reason = "cheaper cost tier rejected: " + alternative.Reason
		}
		if !capable {
			alternative.Reason = "fallback lacks complete current canonical capability/scope/grant/phase preflight"
		}
		if estimate, ok := spec.Alternatives[role.Name]; ok && estimate.Model == role.ModelID() {
			marginal, err := budgetAdd(q.Total-q.Implementation, estimate.Tokens)
			if err != nil {
				return nil, err
			}
			alternative.MarginalEstimate = &marginal
			alternative.EstimateKnowledge = "owner-estimate"
		}
		q.Alternatives = append(q.Alternatives, alternative)
	}

	return finishBudgetQuote(q, report), nil
}

// calculateBudgetQuote is deterministic and performs no observations, writes or
// routing. Preview and admission pass the same immutable inputs and liabilities.
func calculateBudgetQuote(p BudgetPolicy, task string, status BudgetStatus, report *PreflightReport, already int64, now time.Time) (*BudgetQuote, error) {
	q := &BudgetQuote{SchemaVersion: 1, ProjectID: p.ProjectID, TaskID: task, PolicyRevision: p.Revision, CycleID: p.CycleID, Harness: p.Harness, LedgerDigest: status.LedgerDigest, Phases: []BudgetPhase{}, Alternatives: []BudgetAlternative{}, Enforcement: status.Enforcement, UsageKnowledge: "unknown"}
	spec, ok := p.Tasks[task]
	var e error
	if !ok {
		q.Reason = "missing_complete_phase_estimate"
		return q, nil
	}
	names := []string{}
	for name := range spec.Phases {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		estimate := spec.Phases[name]
		q.Phases = append(q.Phases, BudgetPhase{Phase: name, Estimate: estimate, State: "reserved"})
		q.Total, e = budgetAdd(q.Total, estimate.Tokens)
		if e != nil {
			return nil, e
		}
		switch name {
		case "implementation":
			q.Implementation = estimate.Tokens
		case "review", "re-review":
			q.Review += estimate.Tokens
		case "correction":
			q.Correction = estimate.Tokens
		case "integration":
			q.Integration = estimate.Tokens
		case "recovery":
			q.Recovery = estimate.Tokens
		}
	}
	q.Reason = "selected_owner_pinned_model"
	q.Allowed = report != nil && report.Allowed && now.Before(report.ExpiresAt) && !status.STOP && now.Before(p.ExpiresAt) && !now.Before(p.CooldownUntil)
	if !q.Allowed {
		switch {
		case status.STOP:
			q.Reason = "budget_STOP"
		case !now.Before(p.ExpiresAt):
			q.Reason = "budget_policy_expired"
		case now.Before(p.CooldownUntil):
			q.Reason = "budget_cooldown"
		default:
			q.Reason = "budget_preflight_unavailable"
		}
	}
	cost, e := budgetAdd(already, q.Total)
	if e != nil {
		return nil, e
	}
	cost, e = budgetAdd(cost, p.RecoveryFloor)
	if e != nil {
		return nil, e
	}
	if cost > status.Cycle.Remaining || cost > status.Window.Remaining {
		q.Allowed = false
		q.Reason = "insufficient_complete_cycle_headroom"
	}
	return q, nil
}
func finishBudgetQuote(q *BudgetQuote, report *PreflightReport) *BudgetQuote {
	proof := ""
	if report != nil {
		proof = report.ProofDigest
	}
	q.Digest = identity("budget.quote", "", struct {
		Quote     BudgetQuote
		Preflight string
	}{*q, proof})
	return q
}
func (a *Authority) QuoteBudget(token, session, project, task string) (BudgetQuote, error) {
	var out BudgetQuote
	e := a.transaction(false, false, func(s *state) error {
		p, e := budgetAuth(s, token, session, project, "budget.quote")
		if e != nil {
			return e
		}
		if _, ok := p.Tasks[task]; !ok {
			return ErrDenied
		}
		spec := s.PreflightPolicies[project].Tasks[task]
		report, e := a.preflight(s, project, task, spec.ImplementationRole, spec.ReviewRole, "")
		if e != nil {
			return e
		}
		q, e := a.quoteBudget(s, project, task, &report, 0)
		if e != nil {
			return e
		}
		if _, e := budgetAuth(s, token, session, project, "budget.quote"); e != nil {
			return e
		}
		if q.Allowed && (!budgetQuoteCurrent(s, q) || !time.Now().Before(report.ExpiresAt)) {
			return ErrDenied
		}
		out = *q
		return nil
	})
	if e != nil {
		return BudgetQuote{}, e
	}
	return out, nil
}
func allocateBudget(s *state, d DispatchReservation, wave string, q *BudgetQuote) error {
	if _, enabled := s.BudgetPolicies[d.ProjectID]; !enabled {
		return nil
	}
	if q == nil || !q.Allowed {
		return ErrDenied
	}
	if _, exists := s.BudgetAllocations[d.ID]; exists {
		return ErrDenied
	}
	s.BudgetAllocations[d.ID] = BudgetAllocation{ID: d.ID, DispatchID: d.ID, WaveID: wave, ProjectID: d.ProjectID, TaskID: d.Reservation.TaskID, PolicyRevision: q.PolicyRevision, CycleID: q.CycleID, Harness: d.Harness, QuoteDigest: q.Digest, ReservationFence: d.Reservation.Fence, AllocatedAt: time.Now().UTC(), Phases: append([]BudgetPhase{}, q.Phases...)}
	return nil
}
func budgetDispatchCurrent(s *state, d DispatchReservation) bool {
	p, enabled := s.BudgetPolicies[d.ProjectID]
	if !enabled {
		return true
	}
	alloc, ok := s.BudgetAllocations[d.ID]
	if !ok || alloc.PolicyRevision != p.Revision || alloc.DispatchID != d.ID || alloc.TaskID != d.Reservation.TaskID || alloc.ReservationFence != d.Reservation.Fence || alloc.Harness != d.Harness || s.SwarmPolicies[d.ProjectID].Stopped || !time.Now().Before(p.ExpiresAt) || time.Now().Before(p.CooldownUntil) {
		return false
	}
	status, e := budgetStatus(s, p, time.Now())
	if e != nil || status.Window.Remaining < p.RecoveryFloor || status.Cycle.Remaining < p.RecoveryFloor || status.Window.Spent+status.Window.Reserved > p.WindowLimit || status.Cycle.Spent+status.Cycle.Reserved > p.CycleLimit {
		return false
	}
	for _, phase := range alloc.Phases {
		if phase.Phase == "implementation" {
			return phase.State == "reserved"
		}
	}
	return false
}

// cloneAuthorityState prevents allocation/dispatch writes aliasing the immutable
// pre-allocation generation used for checkpoint reconciliation.
func cloneAuthorityState(s *state) (state, error) {
	data, e := json.Marshal(s)
	if e != nil {
		return state{}, ErrStorage
	}
	var copy state
	if json.Unmarshal(data, &copy) != nil {
		return state{}, ErrStorage
	}
	return copy, nil
}

func budgetQuoteCurrent(s *state, q *BudgetQuote) bool {
	if q == nil {
		return true
	}
	p, ok := s.BudgetPolicies[q.ProjectID]
	if !ok || p.Revision != q.PolicyRevision || !time.Now().Before(p.ExpiresAt) || time.Now().Before(p.CooldownUntil) || s.SwarmPolicies[p.ProjectID].Stopped {
		return false
	}
	status, e := budgetStatus(s, p, time.Now().UTC())
	return e == nil && status.LedgerDigest == q.LedgerDigest
}
