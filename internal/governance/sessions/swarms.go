package sessions

// Native swarm planning reserves cooperative resources, never starts host agents.
// Plans and live admission share the same predicate (core 589, 527 and 530).
import (
	"encoding/json"
	"math"
	"path/filepath"
	"reflect"
	"sort"
	"time"

	"github.com/mlnomadpy/dacli/internal/governance/checkout"
)

type SwarmCandidate struct {
	TaskID, ProjectID, Status, Reason string
	Priority                          int
	Resources                         []Resource
}
type SwarmSnapshot struct {
	Digest     string
	Candidates []SwarmCandidate
}
type SwarmAdmission interface {
	ObserveSwarm(workspace, project string) (SwarmSnapshot, error)
}
type SwarmCapabilityAdmission interface {
	AdmitSwarmCapability(workspace, project, harness, digest string) error
}

type SwarmRole struct {
	ID, Kind, Harness string
	Capacity          int
}
type SwarmPlacement struct{ Checkout, ImplementKind, ReviewKind string }

// SwarmPolicy can only be changed with owner authentication, not controller overrides.
type SwarmPolicy struct {
	ProjectID, Revision, Harness, CapabilityDigest string
	Width, WIP                                     int
	TTL                                            time.Duration
	Stopped                                        bool
	Roles                                          []SwarmRole
	Placements                                     map[string]SwarmPlacement
}
type SwarmVerdict struct {
	TaskID, Reason, ImplementationRole, ReviewRole string
	Preflight                                      *PreflightReport
	Selected                                       bool
	Resources                                      []Resource
	Checkout, Repository, BaseCommit, BaseTree     string
}
type SwarmPlan struct {
	SchemaVersion                                            int
	ID, SessionID, ProjectID, PolicyRevision, SnapshotDigest string
	Fence                                                    uint64
	Verdicts                                                 []SwarmVerdict
}
type ReserveWaveRequest struct {
	OperationKey string
	Plan         SwarmPlan
	ExpiresAt    time.Time
}
type SwarmWave struct {
	ID, PlanID string
	Dispatches []DispatchReservation
}
type DispatchReservation struct {
	ID, ControllerSessionID, ProjectID, PolicyRevision, ImplementationRole, ReviewRole, Harness, BaseCommit, BaseTree string
	Reservation                                                                                                       Lease
	PreflightExpiresAt                                                                                                time.Time
	PreflightDigest                                                                                                   string
	Converted                                                                                                         bool
	WorkerSessionID, AttemptID                                                                                        string
}
type SwarmReceipt struct {
	Digest   string
	Wave     *SwarmWave
	Dispatch *DispatchReservation
	Lease    *Lease
}
type AdmitDispatchRequest struct {
	DispatchID    string
	ExpectedFence uint64
}
type HandoffDispatchRequest struct {
	OperationKey, DispatchID, AttemptID string
	ExpectedFence                       uint64
}

func initializeSwarms(s *state) {
	s.SwarmPolicies = map[string]SwarmPolicy{}
	s.Dispatches = map[string]DispatchReservation{}
	s.SwarmOperations = map[string]SwarmReceipt{}
}

// OpenSwarm refuses schema6 until an explicit owner migration. Old schema6
// binaries in turn refuse schema7 rather than silently erasing new reservations.
func OpenSwarm(path string, binding Binding, admission LeaseAdmission) (*Authority, error) {
	a, e := Open(path, binding, admission)
	if e == nil {
		a.version = 7
	}
	return a, e
}
func (a *Authority) UpgradeSwarms(owner string) error {
	old := *a
	old.version = 6
	return old.transaction(false, true, func(s *state) error {
		if s.Version != 6 || len(owner) != 64 || hash(owner) != s.OwnerHash {
			return ErrDenied
		}
		if s.SwarmPolicies != nil || s.Dispatches != nil || s.SwarmOperations != nil {
			return ErrStorage
		}
		initializeSwarms(s)
		s.Version = 7
		return nil
	})
}
func (a *Authority) ConfigureSwarm(owner string, p SwarmPolicy) error {
	return a.ConfigureSwarmWithBindings(owner, p, nil)
}
func (a *Authority) ConfigureSwarmWithBindings(owner string, p SwarmPolicy, bindings []TaskBinding) error {
	if !validID(p.ProjectID) || !validID(p.Revision) || !validID(p.Harness) || !validID(p.CapabilityDigest) || p.Width < 1 || p.WIP < p.Width || p.TTL <= 0 || p.TTL > 24*time.Hour || len(p.Placements) == 0 {
		return ErrDenied
	}
	ids := map[string]bool{}
	for _, r := range p.Roles {
		if !validID(r.ID) || !validID(r.Kind) || !validID(r.Harness) || r.Capacity < 1 || ids[r.ID] {
			return ErrDenied
		}
		ids[r.ID] = true
	}
	for task, v := range p.Placements {
		if !validID(task) || !filepath.IsAbs(v.Checkout) || !validID(v.ImplementKind) || !validID(v.ReviewKind) {
			return ErrDenied
		}
	}
	return a.transaction(false, true, func(s *state) error {
		if s.Version < 7 || len(owner) != 64 || hash(owner) != s.OwnerHash {
			return ErrDenied
		}
		if old, ok := s.SwarmPolicies[p.ProjectID]; ok && old.Revision == p.Revision && !reflect.DeepEqual(old, p) {
			return ErrDenied
		}
		for _, b := range bindings {
			if !validID(b.TaskID) || b.ProjectID != p.ProjectID || !b.AdmissionEnabled || !filepath.IsAbs(b.Repository) {
				return ErrDenied
			}
			if _, ok := p.Placements[b.TaskID]; !ok {
				return ErrDenied
			}
			if old, ok := s.Tasks[b.TaskID]; ok && old != b {
				return ErrDenied
			}
			s.Tasks[b.TaskID] = b
		}

		for task := range p.Placements {
			b, ok := s.Tasks[task]
			if !ok || b.ProjectID != p.ProjectID || !b.AdmissionEnabled {
				return ErrDenied
			}
		}
		if prior, ok := s.SwarmPolicies[p.ProjectID+"\x00"+p.Revision]; ok {
			if current, ok := s.SwarmPolicies[p.ProjectID]; !ok || current.Revision != prior.Revision || !reflect.DeepEqual(prior, p) {
				return ErrDenied
			}
		}
		s.SwarmPolicies[p.ProjectID+"\x00"+p.Revision] = p
		s.SwarmPolicies[p.ProjectID] = p
		return nil
	})
}
func controller(s *state, token, id, project, action string) (record, error) {
	r, e := authenticate(s, token, id, time.Now())
	if e != nil {
		return r, e
	}
	if !subset([]string{action}, r.Scope.Actions) || !subset([]string{"project:" + project}, r.Scope.Resources) {
		return r, ErrDenied
	}
	return r, nil
}
func planDigest(p SwarmPlan) string {
	p.ID = ""
	p.Verdicts = append([]SwarmVerdict(nil), p.Verdicts...)
	for i := range p.Verdicts {
		if p.Verdicts[i].Preflight != nil {
			r := *p.Verdicts[i].Preflight
			r.ObservedAt = time.Time{}
			p.Verdicts[i].Preflight = &r
		}
	}
	b, _ := json.Marshal(p)
	return hash(string(b))
}
func (a *Authority) planSwarm(s *state, token, session, project, action string) (SwarmPlan, error) {
	r, e := controller(s, token, session, project, action)
	if e != nil {
		return SwarmPlan{}, e
	}
	policy, ok := s.SwarmPolicies[project]
	if s.Version < 7 || !ok || policy.Stopped {
		return SwarmPlan{}, ErrDenied
	}
	capability, ok := a.admission.(SwarmCapabilityAdmission)
	if !ok || capability.AdmitSwarmCapability(s.Binding.Workspace, project, policy.Harness, policy.CapabilityDigest) != nil {
		return SwarmPlan{}, ErrDenied
	}
	adapter, ok := a.admission.(SwarmAdmission)
	if !ok {
		return SwarmPlan{}, ErrDenied
	}
	snap, e := adapter.ObserveSwarm(s.Binding.Workspace, project)
	if e != nil || snap.Digest == "" {
		return SwarmPlan{}, ErrDenied
	}
	candidates := append([]SwarmCandidate(nil), snap.Candidates...)
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Priority != candidates[j].Priority {
			return candidates[i].Priority < candidates[j].Priority
		}
		return candidates[i].TaskID < candidates[j].TaskID
	})
	plan := SwarmPlan{SchemaVersion: 1, SessionID: session, ProjectID: project, PolicyRevision: policy.Revision, SnapshotDigest: snap.Digest, Fence: s.Fence, Verdicts: []SwarmVerdict{}}
	used, occupied, excluded := swarmOccupancy(s, policy, snap)
	roles := append([]SwarmRole(nil), policy.Roles...)
	sort.Slice(roles, func(i, j int) bool { return roles[i].ID < roles[j].ID })
	choose := func(kind string, counts map[string]int) string {
		for _, role := range roles {
			if role.Kind == kind && role.Harness == policy.Harness && counts[role.ID] < role.Capacity {
				return role.ID
			}
		}
		return ""
	}
	seen := map[string]bool{}
	selected := 0
	for _, c := range candidates {
		if !validID(c.TaskID) || seen[c.TaskID] || c.ProjectID != project {
			return SwarmPlan{}, ErrDenied
		}
		seen[c.TaskID] = true
		v := SwarmVerdict{TaskID: c.TaskID, Reason: c.Reason}
		if c.Reason != "ready" {
			plan.Verdicts = append(plan.Verdicts, v)
			continue
		}
		placement, present := policy.Placements[c.TaskID]
		binding, bound := s.Tasks[c.TaskID]
		if !present || !bound || !binding.AdmissionEnabled || binding.ProjectID != project {
			v.Reason = "owner_binding_missing"
			plan.Verdicts = append(plan.Verdicts, v)
			continue
		}
		if !subset([]string{"task:" + c.TaskID}, r.Scope.Resources) {
			v.Reason = "scope_denied"
			plan.Verdicts = append(plan.Verdicts, v)
			continue
		}
		if selected >= policy.Width || len(occupied) >= policy.WIP {
			v.Reason = "capacity_exhausted"
			plan.Verdicts = append(plan.Verdicts, v)
			continue
		}
		if len(c.Resources) == 0 {
			v.Reason = "claims_missing"
			plan.Verdicts = append(plan.Verdicts, v)
			continue
		}
		resources, e := canonicalResources(placement.Checkout, c.TaskID, c.Resources)
		if e != nil {
			v.Reason = "claims_invalid"
			plan.Verdicts = append(plan.Verdicts, v)
			continue
		}
		allowed := true
		for _, x := range resources {
			if !subset([]string{x.Kind + ":" + x.Path}, r.Scope.Resources) {
				allowed = false
			}
		}
		if !allowed {
			v.Reason = "scope_denied"
			plan.Verdicts = append(plan.Verdicts, v)
			continue
		}
		if canonical, e := filepath.EvalSymlinks(placement.Checkout); e != nil || canonical != placement.Checkout {
			v.Reason = "checkout_unavailable"
			plan.Verdicts = append(plan.Verdicts, v)
			continue
		}
		proposed := Lease{TaskID: c.TaskID, Repository: binding.Repository, Checkout: placement.Checkout, Resources: resources}
		collision := false
		for _, old := range excluded {
			if overlaps(proposed, old) {
				collision = true
				break
			}
		}
		if collision {
			v.Reason = "resource_conflict"
			plan.Verdicts = append(plan.Verdicts, v)
			continue
		}
		obs, e := checkout.Observe(placement.Checkout)
		if e != nil || obs.Repository != binding.Repository || obs.Locator != placement.Checkout {
			v.Reason = "checkout_unavailable"
			plan.Verdicts = append(plan.Verdicts, v)
			continue
		}
		pick := func(kind, id string) string {
			for _, role := range roles {
				if role.ID == id && role.Kind == kind && role.Harness == policy.Harness && used[id] < role.Capacity {
					return id
				}
			}
			return ""
		}
		impl := choose(placement.ImplementKind, used)
		if s.Version >= 11 {
			impl = pick(placement.ImplementKind, s.PreflightPolicies[project].Tasks[c.TaskID].ImplementationRole)
		}
		if impl != "" {
			used[impl]++
		}
		review := choose(placement.ReviewKind, used)
		if s.Version >= 11 {
			review = pick(placement.ReviewKind, s.PreflightPolicies[project].Tasks[c.TaskID].ReviewRole)
		}
		if impl == "" || review == "" {
			if impl != "" {
				used[impl]--
			}
			v.Reason = "compatible_role_missing"
			plan.Verdicts = append(plan.Verdicts, v)
			continue
		}
		if s.Version >= 11 {
			report, e := a.preflight(s, project, c.TaskID, impl, review, "")
			if e != nil {
				return SwarmPlan{}, e
			}
			v.Preflight = &report
			if !report.Allowed {
				used[impl]--
				v.Reason = "preflight_" + report.FailureClass
				plan.Verdicts = append(plan.Verdicts, v)
				continue
			}
		}
		used[review]++
		selected++
		occupied[c.TaskID] = true
		excluded = append(excluded, proposed)
		v.Selected = true
		v.Reason = "selected"
		v.ImplementationRole = impl
		v.ReviewRole = review
		v.Checkout = placement.Checkout
		v.Repository = binding.Repository
		v.BaseCommit = obs.Commit
		v.BaseTree = obs.Tree
		v.Resources = resources
		plan.Verdicts = append(plan.Verdicts, v)
	}
	// Canonical reads/Git can consume time; a session that expired during planning
	// cannot obtain a fresh authority decision.
	if _, e := controller(s, token, session, project, action); e != nil {
		return SwarmPlan{}, e
	}
	if capability.AdmitSwarmCapability(s.Binding.Workspace, project, policy.Harness, policy.CapabilityDigest) != nil {
		return SwarmPlan{}, ErrDenied
	}
	if s.Version >= 11 {
		for _, v := range plan.Verdicts {
			if v.Selected {
				fresh, e := a.preflight(s, project, v.TaskID, v.ImplementationRole, v.ReviewRole, "")
				if e != nil {
					return SwarmPlan{}, e
				}
				if !fresh.Allowed {
					return SwarmPlan{}, preflightReportError(fresh)
				}
				if v.Preflight == nil || fresh.ProofDigest != v.Preflight.ProofDigest {
					return SwarmPlan{}, ErrDenied
				}
			}
		}
	}
	if s.Version >= 11 && capability.AdmitSwarmCapability(s.Binding.Workspace, project, policy.Harness, policy.CapabilityDigest) != nil {
		return SwarmPlan{}, ErrDenied
	}
	if _, e := controller(s, token, session, project, action); e != nil {
		return SwarmPlan{}, e
	}
	if s.Version >= 11 {
		for _, v := range plan.Verdicts {
			if v.Selected && (v.Preflight == nil || !time.Now().Before(v.Preflight.ExpiresAt)) {
				return SwarmPlan{}, ErrDenied
			}
		}
	}
	plan.ID = planDigest(plan)
	return plan, nil
}
func (a *Authority) PlanSwarm(token, session, project string) (SwarmPlan, error) {
	var p SwarmPlan
	e := a.transaction(false, false, func(s *state) error {
		var e error
		p, e = a.planSwarm(s, token, session, project, "swarm.plan")
		return e
	})
	return p, e
}
func (a *Authority) ReserveWave(token, session string, req ReserveWaveRequest) (SwarmWave, error) {
	var wave SwarmWave
	e := a.transaction(false, true, func(s *state) error {
		r, e := controller(s, token, session, req.Plan.ProjectID, "swarm.reserve")
		if e != nil {
			return e
		}
		if !validID(req.OperationKey) || req.Plan.SessionID != session || req.Plan.SchemaVersion != 1 || req.Plan.ID != planDigest(req.Plan) {
			return ErrDenied
		}
		req.ExpiresAt = req.ExpiresAt.UTC()
		digest := identity("swarm.reserve", session, req)
		if old, ok := s.SwarmOperations[req.OperationKey]; ok {
			if old.Digest != digest || old.Wave == nil {
				return ErrDenied
			}
			wave = *old.Wave
			return nil
		}
		fresh, e := a.planSwarm(s, token, session, req.Plan.ProjectID, "swarm.reserve")
		if e != nil {
			return e
		}
		if fresh.ID != req.Plan.ID {
			return preflightPlanError(fresh, req.Plan)
		}
		p := s.SwarmPolicies[req.Plan.ProjectID]
		now := time.Now()
		if !req.ExpiresAt.After(now) || req.ExpiresAt.After(now.Add(p.TTL)) || req.ExpiresAt.After(r.ExpiresAt) {
			return ErrDenied
		}
		id, e := mint()
		if e != nil {
			return e
		}
		wave = SwarmWave{ID: id, PlanID: fresh.ID, Dispatches: []DispatchReservation{}}
		for _, v := range fresh.Verdicts {
			if !v.Selected {
				continue
			}
			if s.Fence == math.MaxUint64 {
				return ErrDenied
			}
			id, e := mint()
			if e != nil {
				return e
			}
			s.Fence++
			d := DispatchReservation{ID: id, ControllerSessionID: session, ProjectID: p.ProjectID, PolicyRevision: p.Revision, ImplementationRole: v.ImplementationRole, ReviewRole: v.ReviewRole, Harness: p.Harness, BaseCommit: v.BaseCommit, BaseTree: v.BaseTree, Reservation: Lease{ID: id, SessionID: session, TaskID: v.TaskID, Repository: v.Repository, Checkout: v.Checkout, Resources: v.Resources, Fence: s.Fence, ExpiresAt: req.ExpiresAt}}
			if v.Preflight != nil {
				d.PreflightDigest = v.Preflight.ProofDigest
				d.PreflightExpiresAt = v.Preflight.ExpiresAt
			}
			s.Dispatches[id] = d
			wave.Dispatches = append(wave.Dispatches, d)
		}
		if len(wave.Dispatches) == 0 {
			return preflightPlanError(fresh, req.Plan)
		}
		capability, ok := a.admission.(SwarmCapabilityAdmission)
		if !ok || capability.AdmitSwarmCapability(s.Binding.Workspace, p.ProjectID, p.Harness, p.CapabilityDigest) != nil {
			return ErrDenied
		}
		if _, e := controller(s, token, session, req.Plan.ProjectID, "swarm.reserve"); e != nil || !req.ExpiresAt.After(time.Now()) {
			return ErrDenied
		}
		for _, d := range wave.Dispatches {
			if e := a.dispatchPreflight(s, d, ""); e != nil {
				return e
			}
		}
		if s.Version >= 11 && capability.AdmitSwarmCapability(s.Binding.Workspace, p.ProjectID, p.Harness, p.CapabilityDigest) != nil {
			return ErrDenied
		}
		if _, e := controller(s, token, session, req.Plan.ProjectID, "swarm.reserve"); e != nil || !time.Now().Before(req.ExpiresAt) {
			return ErrDenied
		}
		for _, d := range wave.Dispatches {
			if !preflightBoundCurrent(s, d) {
				return ErrDenied
			}
		}
		s.SwarmOperations[req.OperationKey] = SwarmReceipt{Digest: digest, Wave: &wave}
		return nil
	})
	if e != nil {
		return SwarmWave{}, e
	}
	return wave, nil
}
func (a *Authority) admitDispatch(s *state, token, session string, req AdmitDispatchRequest) (DispatchReservation, error) {
	if !lifecycleDispatchEligible(s, req.DispatchID) {
		return DispatchReservation{}, ErrDenied
	}
	if _, cancelled := s.LifecycleCancellations[req.DispatchID]; cancelled {
		return DispatchReservation{}, ErrDenied
	}
	d, ok := s.Dispatches[req.DispatchID]
	if !ok || d.ControllerSessionID != session || d.Reservation.Fence != req.ExpectedFence || !reservationRetained(s, d) || !time.Now().Before(d.Reservation.ExpiresAt) {
		return DispatchReservation{}, ErrDenied
	}
	r, e := controller(s, token, session, d.ProjectID, "swarm.dispatch")
	if e != nil {
		return DispatchReservation{}, e
	}
	p, ok := s.SwarmPolicies[d.ProjectID]
	if !ok || p.Stopped || p.Revision != d.PolicyRevision || p.Harness != d.Harness {
		return DispatchReservation{}, ErrDenied
	}
	capability, ok := a.admission.(SwarmCapabilityAdmission)
	if !ok || capability.AdmitSwarmCapability(s.Binding.Workspace, d.ProjectID, p.Harness, p.CapabilityDigest) != nil {
		return DispatchReservation{}, ErrDenied
	}
	b, ok := s.Tasks[d.Reservation.TaskID]
	if !ok || !b.AdmissionEnabled || b.Repository != d.Reservation.Repository || b.ProjectID != d.ProjectID {
		return DispatchReservation{}, ErrDenied
	}
	for _, res := range d.Reservation.Resources {
		if !subset([]string{res.Kind + ":" + res.Path}, r.Scope.Resources) {
			return DispatchReservation{}, ErrDenied
		}
	}
	if a.admission == nil || a.admission.AdmitLease(s.Binding.Workspace, d.ProjectID, d.Reservation.TaskID) != nil {
		return DispatchReservation{}, ErrDenied
	}
	adapter, ok := a.admission.(SwarmAdmission)
	if !ok {
		return DispatchReservation{}, ErrDenied
	}
	snap, e := adapter.ObserveSwarm(s.Binding.Workspace, d.ProjectID)
	if e != nil {
		return DispatchReservation{}, ErrDenied
	}
	matched := false
	for _, c := range snap.Candidates {
		if c.TaskID == d.Reservation.TaskID {
			resources, e := canonicalResources(d.Reservation.Checkout, c.TaskID, c.Resources)
			if e != nil || c.Reason != "ready" || !reflect.DeepEqual(resources, d.Reservation.Resources) {
				return DispatchReservation{}, ErrDenied
			}
			matched = true
		}
	}
	if !matched {
		return DispatchReservation{}, ErrDenied
	}
	if !swarmCapacityAvailable(s, p, snap, d) {
		return DispatchReservation{}, ErrDenied
	}
	placement, ok := p.Placements[d.Reservation.TaskID]
	if !ok || placement.Checkout != d.Reservation.Checkout {
		return DispatchReservation{}, ErrDenied
	}
	impl, review := false, false
	for _, role := range p.Roles {
		if role.Harness != d.Harness {
			continue
		}
		if role.ID == d.ImplementationRole && role.Kind == placement.ImplementKind {
			impl = true
		}
		if role.ID == d.ReviewRole && role.Kind == placement.ReviewKind {
			review = true
		}
	}
	if !impl || !review {
		return DispatchReservation{}, ErrDenied
	}
	obs, e := checkout.Observe(d.Reservation.Checkout)
	if e != nil || obs.Repository != b.Repository || obs.Locator != d.Reservation.Checkout || obs.Commit != d.BaseCommit || obs.Tree != d.BaseTree {
		return DispatchReservation{}, ErrDenied
	}
	if capability.AdmitSwarmCapability(s.Binding.Workspace, d.ProjectID, p.Harness, p.CapabilityDigest) != nil {
		return DispatchReservation{}, ErrDenied
	}
	if _, e := controller(s, token, session, d.ProjectID, "swarm.dispatch"); e != nil || !time.Now().Before(d.Reservation.ExpiresAt) {
		return DispatchReservation{}, ErrDenied
	}
	if e := a.dispatchPreflight(s, d, ""); e != nil {
		return DispatchReservation{}, e
	}
	if s.Version >= 11 && capability.AdmitSwarmCapability(s.Binding.Workspace, d.ProjectID, p.Harness, p.CapabilityDigest) != nil {
		return DispatchReservation{}, ErrDenied
	}
	if _, e := controller(s, token, session, d.ProjectID, "swarm.dispatch"); e != nil || !time.Now().Before(d.Reservation.ExpiresAt) || !preflightBoundCurrent(s, d) {
		return DispatchReservation{}, ErrDenied
	}
	return d, nil
}

// AdmitDispatch is a fresh permission observation, not a durable host launch or
// an exactly-once execution claim. A bridge must record intent before its call.
func (a *Authority) AdmitDispatch(token, session string, req AdmitDispatchRequest) (DispatchReservation, error) {
	var d DispatchReservation
	e := a.transaction(false, false, func(s *state) error { var e error; d, e = a.admitDispatch(s, token, session, req); return e })
	return d, e
}

// HandoffDispatch is called by the independently enrolled worker. It atomically
// replaces one retained reservation with its lease: no controller worker token,
// unreserved gap, or overlap. Host acknowledgement is a separate bridge concern.
func (a *Authority) HandoffDispatch(token, worker string, req HandoffDispatchRequest) (Lease, error) {
	var result Lease
	e := a.transaction(false, true, func(s *state) error {
		if _, exists := s.DispatchIntents[req.DispatchID]; exists {
			return ErrDenied
		}
		return a.handoffDispatch(s, token, worker, req, &result)
	})
	if e != nil {
		return Lease{}, e
	}
	return result, nil
}

func (a *Authority) handoffDispatch(s *state, token, worker string, req HandoffDispatchRequest, result *Lease) error {
	r, at, e := leaseAuth(s, token, worker, req.AttemptID, "lease.acquire", time.Now())
	if e != nil {
		return e
	}
	if !validID(req.OperationKey) {
		return ErrDenied
	}
	digest := identity("swarm.handoff", worker, req)
	if old, ok := s.SwarmOperations[req.OperationKey]; ok {
		if old.Digest != digest || old.Lease == nil {
			return ErrDenied
		}
		*result = *old.Lease
		return nil
	}
	d, ok := s.Dispatches[req.DispatchID]
	if !ok || !reservationRetained(s, d) || d.ControllerSessionID == worker || d.Reservation.Fence != req.ExpectedFence || !time.Now().Before(d.Reservation.ExpiresAt) {
		return ErrDenied
	}
	cr, ok := s.Sessions[d.ControllerSessionID]
	if !ok {
		return ErrDenied
	}
	// Authenticate the controller's ancestry without recovering its bearer token.
	seen := map[string]bool{}
	for cur := cr; ; {
		if seen[cur.ID] {
			return ErrDenied
		}
		seen[cur.ID] = true
		if cur.Revoked || !time.Now().Before(cur.ExpiresAt) {
			return ErrDenied
		}
		if cur.Parent == "" {
			break
		}
		next, ok := s.Sessions[cur.Parent]
		if !ok {
			return ErrDenied
		}
		cur = next
	}
	p, ok := s.SwarmPolicies[d.ProjectID]
	if !ok || p.Stopped || p.Revision != d.PolicyRevision {
		return ErrDenied
	}
	capability, ok := a.admission.(SwarmCapabilityAdmission)
	if !ok || capability.AdmitSwarmCapability(s.Binding.Workspace, d.ProjectID, p.Harness, p.CapabilityDigest) != nil {
		return ErrDenied
	}
	if at.Request.TaskID != d.Reservation.TaskID || at.Observation.Locator != d.Reservation.Checkout || at.Observation.Repository != d.Reservation.Repository {
		return ErrDenied
	}
	for _, res := range d.Reservation.Resources {
		if !subset([]string{res.Kind + ":" + res.Path}, r.Scope.Resources) || !subset([]string{res.Kind + ":" + res.Path}, cr.Scope.Resources) {
			return ErrDenied
		}
	}
	if !subset([]string{"swarm.dispatch"}, cr.Scope.Actions) || a.admission == nil || a.admission.AdmitLease(s.Binding.Workspace, d.ProjectID, at.Request.TaskID) != nil {
		return ErrDenied
	}
	if !subset([]string{"project:" + d.ProjectID}, cr.Scope.Resources) {
		return ErrDenied
	}
	binding, ok := s.Tasks[d.Reservation.TaskID]
	if !ok || !binding.AdmissionEnabled || binding.ProjectID != d.ProjectID || binding.Repository != d.Reservation.Repository {
		return ErrDenied
	}
	adapter, ok := a.admission.(SwarmAdmission)
	if !ok {
		return ErrDenied
	}
	snapshot, e := adapter.ObserveSwarm(s.Binding.Workspace, d.ProjectID)
	if e != nil {
		return ErrDenied
	}
	fresh := false
	for _, candidate := range snapshot.Candidates {
		if candidate.TaskID == d.Reservation.TaskID {
			resources, e := canonicalResources(d.Reservation.Checkout, candidate.TaskID, candidate.Resources)
			if e != nil || candidate.Reason != "ready" || !reflect.DeepEqual(resources, d.Reservation.Resources) {
				return ErrDenied
			}
			fresh = true
		}
	}
	if !fresh {
		return ErrDenied
	}
	if !swarmCapacityAvailable(s, p, snapshot, d) {
		return ErrDenied
	}
	placement, ok := p.Placements[d.Reservation.TaskID]
	if !ok || placement.Checkout != d.Reservation.Checkout || p.Harness != d.Harness {
		return ErrDenied
	}
	impl, review := false, false
	for _, role := range p.Roles {
		if role.Harness != d.Harness {
			continue
		}
		if role.ID == d.ImplementationRole && role.Kind == placement.ImplementKind {
			impl = true
		}
		if role.ID == d.ReviewRole && role.Kind == placement.ReviewKind {
			review = true
		}
	}
	if !impl || !review {
		return ErrDenied
	}

	if e := checkLeaseCheckout(at); e != nil {
		return e
	}
	obs, e := checkout.Observe(at.Observation.Locator)
	if e != nil || obs.Commit != at.Observation.Commit || obs.Tree != at.Observation.Tree || obs.Commit != d.BaseCommit || obs.Tree != d.BaseTree {
		return ErrDenied
	}
	*result = d.Reservation
	result.AttemptID = at.ID
	result.SessionID = worker
	result.ExpiresAt = d.Reservation.ExpiresAt
	if result.ExpiresAt.After(r.ExpiresAt) {
		return ErrDenied
	}
	for _, old := range s.Leases {
		if !old.Released && overlaps(*result, old) {
			return ErrDenied
		}
	}
	for _, other := range s.Dispatches {
		if other.ID != d.ID && reservationRetained(s, other) && overlaps(*result, other.Reservation) {
			return ErrDenied
		}
	}
	if capability.AdmitSwarmCapability(s.Binding.Workspace, d.ProjectID, p.Harness, p.CapabilityDigest) != nil {
		return ErrDenied
	}
	if _, _, e := leaseAuth(s, token, worker, req.AttemptID, "lease.acquire", time.Now()); e != nil || !time.Now().Before(d.Reservation.ExpiresAt) || !swarmLineageCurrent(s, d.ControllerSessionID, time.Now()) {
		return ErrDenied
	}
	if e := a.dispatchPreflight(s, d, worker); e != nil {
		return e
	}
	if s.Version >= 11 && capability.AdmitSwarmCapability(s.Binding.Workspace, d.ProjectID, p.Harness, p.CapabilityDigest) != nil {
		return ErrDenied
	}
	if _, _, e := leaseAuth(s, token, worker, req.AttemptID, "lease.acquire", time.Now()); e != nil || !swarmLineageCurrent(s, d.ControllerSessionID, time.Now()) || !time.Now().Before(d.Reservation.ExpiresAt) || !preflightBoundCurrent(s, d) {
		return ErrDenied
	}
	if s.Fence == math.MaxUint64 {
		return ErrDenied
	}
	s.Fence++
	result.Fence = s.Fence
	d.Converted = true
	d.WorkerSessionID = worker
	d.AttemptID = at.ID
	s.Dispatches[d.ID] = d
	s.Leases[result.ID] = *result
	s.SwarmOperations[req.OperationKey] = SwarmReceipt{Digest: digest, Lease: result}
	return nil
}

// Retained reservations and unreleased leases occupy capacity even after expiry.
// Known native handoff keeps its recorded role provenance; unknown writers
// conservatively consume each role. Canonical active tasks count independently.
func swarmOccupancy(s *state, p SwarmPolicy, snap SwarmSnapshot) (map[string]int, map[string]bool, []Lease) {
	used := map[string]int{}
	occupied := map[string]bool{}
	excluded := []Lease{}
	for _, d := range s.Dispatches {
		if reservationRetained(s, d) {
			excluded = append(excluded, d.Reservation)
			if d.ProjectID == p.ProjectID {
				occupied[d.Reservation.TaskID] = true
				used[d.ImplementationRole]++
				used[d.ReviewRole]++
			}
		}
	}
	for _, l := range s.Leases {
		if l.Released {
			continue
		}
		excluded = append(excluded, l)
		if b, ok := s.Tasks[l.TaskID]; !ok || b.ProjectID != p.ProjectID {
			continue
		}
		occupied[l.TaskID] = true
		known := false
		for _, d := range s.Dispatches {
			if d.Converted && d.Reservation.ID == l.ID && d.WorkerSessionID == l.SessionID {
				used[d.ImplementationRole]++
				used[d.ReviewRole]++
				known = true
				break
			}
		}
		if !known {
			for _, r := range p.Roles {
				used[r.ID]++
			}
		}
	}
	for _, c := range snap.Candidates {
		if c.Status == "active" && !occupied[c.TaskID] {
			occupied[c.TaskID] = true
			for _, r := range p.Roles {
				used[r.ID]++
			}
		}
	}
	return used, occupied, excluded
}
func swarmCapacityAvailable(s *state, p SwarmPolicy, snap SwarmSnapshot, d DispatchReservation) bool {
	used, occupied, _ := swarmOccupancy(s, p, snap)
	if len(occupied) > p.WIP {
		return false
	}
	for _, r := range p.Roles {
		if (r.ID == d.ImplementationRole || r.ID == d.ReviewRole) && used[r.ID] > r.Capacity {
			return false
		}
	}
	return true
}

func swarmLineageCurrent(s *state, id string, now time.Time) bool {
	seen := map[string]bool{}
	for id != "" {
		r, ok := s.Sessions[id]
		if !ok || seen[id] || r.Revoked || !now.Before(r.ExpiresAt) {
			return false
		}
		seen[id] = true
		id = r.Parent
	}
	return true
}

func (a *Authority) OwnerSwarmPolicy(owner, project string) (SwarmPolicy, error) {
	var p SwarmPolicy
	e := a.transaction(false, false, func(s *state) error {
		if len(owner) != 64 || hash(owner) != s.OwnerHash {
			return ErrDenied
		}
		var ok bool
		p, ok = s.SwarmPolicies[project]
		if !ok {
			return ErrDenied
		}
		return nil
	})
	return p, e
}

// AuthenticateOwner grants no worker capability; it supports bounded owner-only
// local provisioning before observing caller-selected repositories.
func (a *Authority) AuthenticateOwner(owner string) error {
	return a.transaction(false, false, func(s *state) error {
		if len(owner) != 64 || hash(owner) != s.OwnerHash {
			return ErrDenied
		}
		return nil
	})
}
