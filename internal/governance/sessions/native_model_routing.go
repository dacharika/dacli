package sessions

// Owner-selected native routing ranks cooperative role profiles. It never
// changes a running host model or manufactures provider availability/billing.
import (
	"github.com/mlnomadpy/dacli/internal/team"
	"reflect"
	"sort"
	"time"
)

type NativeModelRoutingCandidate struct {
	DifficultyTier        int
	Route                 ControllerTaskRoute
	RoleID, Model         string
	Preflight             PreflightTask
	ContextNeeded         int
	Tools                 []string
	Available             bool
	ObservedAt, ExpiresAt time.Time
}
type NativeModelRoutingTask struct {
	Candidates          []NativeModelRoutingCandidate
	MaxCorrectionRounds int
}
type NativeModelRoutingPolicy struct {
	SchemaVersion                int
	ProjectID, Revision, Harness string
	ExpiresAt                    time.Time
	Tasks                        map[string]NativeModelRoutingTask
}
type NativeModelRoutingSelection struct {
	DifficultyTier                                                                              int
	Route                                                                                       ControllerTaskRoute
	SchemaVersion                                                                               int
	ProjectID, TaskID, PolicyRevision, RoleID, Model, Harness, Reason, Assurance, BindingDigest string
	RequiredPoints                                                                              float64
	Sized                                                                                       bool
	CorrectionRound                                                                             int
	ExpiresAt                                                                                   time.Time
	Explanation                                                                                 team.Explanation
	WorkerSessionID                                                                             string
	NextTurnOnly                                                                                bool
}

func (a *Authority) UpgradeNativeModelRouting(owner string) error {
	return a.transaction(false, true, func(s *state) error {
		if s.Version != 16 || len(owner) != 64 || hash(owner) != s.OwnerHash {
			return ErrDenied
		}
		s.ModelRoutingPolicies = map[string]NativeModelRoutingPolicy{}
		s.ModelRoutingHistory = map[string]NativeModelRoutingPolicy{}
		s.ModelRoutingSelections = map[string]NativeModelRoutingSelection{}
		s.Version = 17
		return nil
	})
}
func (a *Authority) ConfigureNativeModelRouting(owner string, p NativeModelRoutingPolicy) error {
	p.ExpiresAt = p.ExpiresAt.Round(0).UTC()
	// Copy caller-owned containers before storing an immutable policy revision.
	tasks := map[string]NativeModelRoutingTask{}
	for task, t := range p.Tasks {
		t.Candidates = append([]NativeModelRoutingCandidate(nil), t.Candidates...)
		for i := range t.Candidates {
			c := &t.Candidates[i]
			c.Tools = append([]string(nil), c.Tools...)
			sort.Strings(c.Tools)
			c.Preflight.FallbackRoles = append([]string(nil), c.Preflight.FallbackRoles...)
			c.ObservedAt = c.ObservedAt.Round(0).UTC()
			c.ExpiresAt = c.ExpiresAt.Round(0).UTC()
		}
		sort.Slice(t.Candidates, func(i, j int) bool { return t.Candidates[i].RoleID < t.Candidates[j].RoleID })
		tasks[task] = t
	}
	p.Tasks = tasks
	return a.transaction(false, true, func(s *state) error {
		if s.Version < 17 || len(owner) != 64 || hash(owner) != s.OwnerHash || p.SchemaVersion != 1 || !validID(p.ProjectID) || !validID(p.Revision) || p.Harness != s.SwarmPolicies[p.ProjectID].Harness || !time.Now().Before(p.ExpiresAt) || len(p.Tasks) == 0 || len(p.Tasks) > 256 {
			return ErrDenied
		}
		for task, t := range p.Tasks {
			if _, ok := s.PreflightPolicies[p.ProjectID].Tasks[task]; !ok || len(t.Candidates) == 0 || len(t.Candidates) > 32 || t.MaxCorrectionRounds < 0 || t.MaxCorrectionRounds > 100 {
				return ErrDenied
			}
			for i, c := range t.Candidates {
				if !validID(c.RoleID) || c.DifficultyTier < 1 || c.DifficultyTier > 1000 || c.Model == "" || c.Preflight.ImplementationRole != c.RoleID || c.Preflight.WorkerConnection == "" || c.ContextNeeded < 0 || len(c.Tools) > 64 || !time.Now().Before(c.ExpiresAt) || c.ExpiresAt.After(p.ExpiresAt) || c.ExpiresAt.After(c.ObservedAt.Add(time.Hour)) || c.ObservedAt.After(time.Now()) || c.ObservedAt.Before(time.Now().Add(-time.Hour)) || i > 0 && t.Candidates[i-1].RoleID == c.RoleID {
					return ErrDenied
				}
				if i > 0 && (c.ContextNeeded != t.Candidates[0].ContextNeeded || !reflect.DeepEqual(c.Tools, t.Candidates[0].Tools)) {
					return ErrDenied
				}
				fixed := s.PreflightPolicies[p.ProjectID].Tasks[task]
				if c.Preflight.ReviewRole != fixed.ReviewRole || c.Preflight.RequireReview != fixed.RequireReview || c.Preflight.RequireVerification != fixed.RequireVerification || c.Preflight.RequireIntegration != fixed.RequireIntegration || c.Preflight.RequireEnforcedTokens != fixed.RequireEnforcedTokens || c.Preflight.RequireEnforcedBudget != fixed.RequireEnforcedBudget {
					return ErrDenied
				}
			}
		}
		key := p.ProjectID + ":" + p.Revision
		if old, ok := s.ModelRoutingHistory[key]; ok {
			if !reflect.DeepEqual(old, p) {
				return ErrDenied
			}
			return nil
		}
		s.ModelRoutingHistory[key] = p
		s.ModelRoutingPolicies[p.ProjectID] = p
		return nil
	})
}
func routingPreflightSpec(s *state, project, task, role string) (PreflightTask, bool) {
	fixed, ok := s.PreflightPolicies[project].Tasks[task]
	if p, enabled := s.ModelRoutingPolicies[project]; enabled {
		if !time.Now().Before(p.ExpiresAt) {
			return PreflightTask{}, false
		}
		for _, c := range p.Tasks[task].Candidates {
			if c.RoleID == role && c.Available && time.Now().Before(c.ExpiresAt) {
				return c.Preflight, true
			}
		}
		return PreflightTask{}, false
	}
	return fixed, ok
}
func (a *Authority) selectModelRouting(s *state, project, task string, used map[string]int, round int, previous string) (NativeModelRoutingSelection, error) {
	p, ok := s.ModelRoutingPolicies[project]
	t, present := p.Tasks[task]
	out := NativeModelRoutingSelection{SchemaVersion: 1, ProjectID: project, TaskID: task, PolicyRevision: p.Revision, Harness: p.Harness, CorrectionRound: round, NextTurnOnly: round > 0, Assurance: "owner-manual availability; cooperative native grant; estimated admission only", ExpiresAt: p.ExpiresAt}
	if !ok || !present || !time.Now().Before(p.ExpiresAt) || round > t.MaxCorrectionRounds {
		return out, ErrDenied
	}
	observer, ok := a.admission.(PreflightObserver)
	if !ok {
		return out, ErrDenied
	}
	candidates := []team.RouteCandidate{}
	reports := map[string]PreflightReport{}
	observations := map[string]PreflightObservation{}
	previousTier := -1
	minimumTier := 0
	// A durable harder-route decision carries into the next safe wave; polling,
	// restart or a new caller operation key cannot silently choose the cheap role
	// again. Only an explicit new owner policy revision changes this binding.
	for _, old := range s.ModelRoutingSelections {
		if old.ProjectID == project && old.TaskID == task && old.PolicyRevision == p.Revision && old.CorrectionRound > 0 && old.DifficultyTier > minimumTier {
			minimumTier = old.DifficultyTier
		}
	}
	for _, c := range t.Candidates {
		if c.RoleID == previous {
			previousTier = c.DifficultyTier
		}
	}
	for _, c := range t.Candidates {
		obs, e := observer.ObservePreflight(s.Binding.Workspace, project, task, c.Preflight, s.SwarmPolicies[project].Placements[task])
		if e != nil {
			return out, e
		}
		observations[c.RoleID] = obs
	}
	for _, c := range t.Candidates {
		obs := observations[c.RoleID]
		report, e := a.preflight(s, project, task, c.RoleID, c.Preflight.ReviewRole, "")
		if e != nil {
			return out, e
		}
		capacity := 0
		for _, r := range s.SwarmPolicies[project].Roles {
			if r.ID == c.RoleID && r.Harness == p.Harness {
				capacity = r.Capacity - used[c.RoleID]
			}
		}
		eligible := report.Allowed && report.RoleDigest == roleDigest(obs.Implementation) && report.TaskDigest == obs.TaskDigest && report.RosterDigest == obs.RosterDigest && report.ClaimsDigest == obs.ClaimsDigest && report.ProfileDigest == obs.ProfileDigest && obs.Sized && obs.Implementation.ModelID() == c.Model && c.Available && time.Now().Before(c.ExpiresAt) && time.Now().Before(report.ExpiresAt)
		if c.DifficultyTier < minimumTier || round > 0 && (previousTier < 0 || c.RoleID == previous || c.DifficultyTier <= previousTier) {
			eligible = false
		}
		if eligible {
			q, e := a.quoteBudget(s, project, task, &report, 0)
			if e != nil {
				return out, e
			}
			if q != nil && !q.Allowed {
				eligible = false
			}
		}
		if eligible && !routingProducerRouteCurrent(s, project, task, c.Route, obs) {
			eligible = false
		}
		candidate := team.RouteCandidate{Role: obs.Implementation, GrantEnforced: eligible, ContextLimit: obs.Implementation.Profile.ContextLimit, CapacityRemaining: capacity, ProviderPaused: !eligible}
		// Native authority enforces scoped writes; availability remains owner-manual.
		req := team.RouteRequirements{Kind: obs.Implementation.Kind, Grant: "rw", Paths: obs.ClaimResources, TaskPoints: obs.Required, Tools: c.Tools, ContextNeeded: c.ContextNeeded}
		req.Paths = nil
		for _, r := range obs.ClaimResources {
			if len(r) > 5 && r[:5] == "path:" {
				req.Paths = append(req.Paths, r[5:])
			}
		}
		individual := (team.Strategy{}).Select(req, []team.RouteCandidate{candidate})
		if individual.Selected.Role == "" {
			candidate.GrantEnforced = false
			candidate.ProviderPaused = true
		}
		candidates = append(candidates, candidate)
		reports[c.RoleID] = report
	}
	// Task requirements are evaluated per candidate above; ranking retains all
	// candidates and the shared Strategy cost/capacity ordering.
	first := observations[t.Candidates[0].RoleID]
	out.Explanation = (team.Strategy{}).Select(team.RouteRequirements{Kind: first.Implementation.Kind, Grant: "rw", TaskPoints: first.Required}, candidates)
	selected := out.Explanation.Selected.Role
	if selected == "" {
		out.Reason = "no_owner_approved_capable_model"
		return out, nil
	}
	obs := observations[selected]
	report := reports[selected]
	out.RoleID = selected
	out.Model = obs.Implementation.ModelID()
	out.RequiredPoints = obs.Required
	out.Sized = obs.Sized
	out.WorkerSessionID = obs.Worker.SessionID
	for _, c := range t.Candidates {
		if c.RoleID == selected {
			out.Route = c.Route
			out.DifficultyTier = c.DifficultyTier
		}
	}
	out.Reason = "cheapest eligible owner-approved difficulty route"
	if minimumTier > 0 {
		out.Reason = "cheapest eligible role at or above durable harder correction tier"
	}
	if report.ExpiresAt.Before(out.ExpiresAt) {
		out.ExpiresAt = report.ExpiresAt
	}
	for _, c := range t.Candidates {
		if c.RoleID == selected && c.ExpiresAt.Before(out.ExpiresAt) {
			out.ExpiresAt = c.ExpiresAt
		}
	}
	out.BindingDigest = identity("native.model.routing", "", []any{p, report.ProofDigest, out.Explanation, round, previous})
	return out, nil
}

// ReadDispatchRouting authenticates scope and returns no private profile paths.
func (a *Authority) ReadDispatchRouting(token, session, project, id string) (NativeModelRoutingSelection, error) {
	var out NativeModelRoutingSelection
	e := a.transaction(false, false, func(s *state) error {
		actor, e := controller(s, token, session, project, "swarm.plan")
		if e != nil {
			return e
		}
		value, ok := s.ModelRoutingSelections[id]
		if !ok || value.ProjectID != project || !subset([]string{"task:" + value.TaskID}, actor.Scope.Resources) {
			return ErrDenied
		}
		out = value
		return nil
	})
	if e != nil {
		return NativeModelRoutingSelection{}, e
	}
	return out, nil
}

func routingProducerRouteCurrent(s *state, project, task string, route ControllerTaskRoute, obs PreflightObservation) bool {
	if route.WorkerSession != obs.Worker.SessionID || route.RunnerSession != obs.Runner.SessionID || route.ReviewerSession != obs.Reviewer.SessionID || route.ReviewGrantID != obs.Reviewer.GrantID || !validPromptDigest(route.PromptSpecDigest) {
		return false
	}
	bound, ok := s.Tasks[task]
	if !ok || bound.ProjectID != project {
		return false
	}
	placement := s.SwarmPolicies[project].Placements[task]
	resources := []Resource{}
	for _, r := range obs.ClaimResources {
		if len(r) > 5 && r[:5] == "path:" {
			resources = append(resources, Resource{Kind: "path", Path: r[5:]})
		}
	}
	resources, e := canonicalResources(placement.Checkout, task, resources)
	if e != nil {
		return false
	}
	d := DispatchReservation{ProjectID: project, Reservation: Lease{TaskID: task, Repository: bound.Repository, Checkout: placement.Checkout, Resources: resources}}
	if !dispatchBoundActor(s, route.BridgeSession, project, "dispatch.correlate", d) || !dispatchBoundActor(s, route.WorkerSession, project, "lease.acquire", d) {
		return false
	}
	roots := map[string]bool{}
	for _, id := range []string{route.BridgeSession, route.WorkerSession, route.RunnerSession, route.ReviewerSession} {
		root := controllerRoot(s, id)
		if root == "" || roots[root] || !controllerLive(s, id) {
			return false
		}
		roots[root] = true
	}
	vg, ok := s.Grants[route.VerificationGrantID]
	if !ok || vg.Revoked || !time.Now().Before(vg.ExpiresAt) || vg.TaskID != task || vg.Repository != bound.Repository || vg.RunnerSession != route.RunnerSession || vg.CheckID != route.CheckID {
		return false
	}
	return subset([]string{route.VerificationGrantID}, obs.VerificationGrantIDs)
}
func modelRoutingCurrent(s *state, d DispatchReservation) bool {
	if d.Routing == nil {
		_, enabled := s.ModelRoutingPolicies[d.ProjectID]
		return !enabled
	}
	r := d.Routing
	p, ok := s.ModelRoutingPolicies[d.ProjectID]
	if !ok || p.Revision != r.PolicyRevision || r.TaskID != d.Reservation.TaskID || r.RoleID != d.ImplementationRole || r.Harness != d.Harness || !time.Now().Before(r.ExpiresAt) || !time.Now().Before(p.ExpiresAt) {
		return false
	}
	for _, c := range p.Tasks[r.TaskID].Candidates {
		if c.RoleID == r.RoleID && c.Model == r.Model && c.Available && time.Now().Before(c.ExpiresAt) {
			return true
		}
	}
	return false
}
