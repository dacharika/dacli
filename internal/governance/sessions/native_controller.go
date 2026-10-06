package sessions

// The controller persists instructions, never impersonates their producer or
// starts a coding harness. External execution uncertainty retains authority.
import (
	"reflect"
	"sort"
	"time"
)

// ControllerPolicy adds invocation bounds to existing shared admission policies.
type ControllerPolicy struct {
	SchemaVersion                                                                  int
	ProjectID, Revision, Harness, CapabilityDigest                                 string
	TaskIDs                                                                        []string
	MaxWaves, MaxConcurrency, MaxCorrectionRounds, MaxNoProgress, TransientRetries int
	MaxElapsed, RetryBackoff                                                       time.Duration
	ExpiresAt                                                                      time.Time
}

// ControllerCheckRoute binds a check to its independently enrolled grant.
type ControllerCheckRoute struct{ CheckID, GrantID string }

// ControllerTaskRoute names independently enrolled actors, never private profiles.
type ControllerTaskRoute struct {
	Checks                                                                                                                      []ControllerCheckRoute `json:",omitempty"`
	BridgeSession, WorkerSession, RunnerSession, ReviewerSession, VerificationGrantID, ReviewGrantID, CheckID, PromptSpecDigest string
}
type ControllerStartRequest struct {
	OperationKey, InvocationID, ProjectID, PolicyRevision, ControllerSessionID, CheckpointID string
	Routes                                                                                   map[string]ControllerTaskRoute
}
type ControllerStepRequest struct {
	OperationKey, ProjectID, InvocationID string
	ExpectedSequence                      uint64
}

// ControllerAction binds a pending instruction to its original producer identity.
type ControllerAction struct {
	Verifications                                                                                                       []VerificationRequest    `json:",omitempty"`
	EvidenceIDs                                                                                                         []string                 `json:",omitempty"`
	RequiredSet                                                                                                         *RequiredVerificationSet `json:",omitempty"`
	ID, Kind, ActorID, TaskID, DispatchID, IntentID, LeaseID, AttemptID, RevisionID, EvidenceID, ReviewID, OperationKey string
	Fence, ReservationFence                                                                                             uint64
	Verification                                                                                                        VerificationRequest
}

// ControllerStatus exposes historical facts separately from a first-use host permit.
type ControllerStatus struct {
	SchemaVersion                                                                                                      int
	ID, ProjectID, ControllerSessionID, PolicyRevision, Harness, CheckpointID, Phase, HaltClass, NextAction, Assurance string
	Sequence, CheckpointSequence                                                                                       uint64
	Waves, CycleOrdinal, Corrections, NoProgress, RetainedConcurrency, TransientAttempts                               int
	StartedAt, Deadline, ObservedAt, NextRetryAt                                                                       time.Time
	Retryable, Terminal, Historical, HostActionAuthorized                                                              bool
	ProgressDigest                                                                                                     string
	Actions                                                                                                            []ControllerAction
	Refs                                                                                                               []CheckpointRefs
	Checkpoint                                                                                                         *CheckpointStatus
}
type controllerInvocation struct {
	Status              ControllerStatus
	Policy              ControllerPolicy
	Routes              map[string]ControllerTaskRoute
	CorrectionSources   map[string]string
	PreflightGeneration string
}
type controllerReceipt struct {
	Digest       string
	Status       ControllerStatus
	RefusalClass string
}

func (a *Authority) UpgradeController(owner string) error {
	return a.transaction(false, true, func(s *state) error {
		if s.Version != 15 || len(owner) != 64 || hash(owner) != s.OwnerHash {
			return ErrDenied
		}
		s.ControllerPolicies = map[string]ControllerPolicy{}
		s.ControllerPolicyHistory = map[string]ControllerPolicy{}
		s.ControllerInvocations = map[string]controllerInvocation{}
		s.ControllerHistory = map[string][]ControllerStatus{}
		s.ControllerOperations = map[string]controllerReceipt{}
		s.Version = 16
		return nil
	})
}
func (a *Authority) ConfigureController(owner string, p ControllerPolicy) error {
	p.ExpiresAt = p.ExpiresAt.Round(0).UTC()
	p.TaskIDs = append([]string(nil), p.TaskIDs...)
	sort.Strings(p.TaskIDs)
	return a.transaction(false, true, func(s *state) error {
		if s.Version < 16 || len(owner) != 64 || hash(owner) != s.OwnerHash || p.SchemaVersion != 1 || !validID(p.ProjectID) || !validID(p.Revision) || !validID(p.Harness) || !validID(p.CapabilityDigest) || p.MaxWaves < 1 || p.MaxWaves > 1000 || p.MaxConcurrency < 1 || p.MaxConcurrency > 256 || p.MaxCorrectionRounds < 0 || p.MaxCorrectionRounds > 100 || p.MaxNoProgress < 1 || p.MaxNoProgress > 100 || p.TransientRetries < 0 || p.TransientRetries > 100 || p.MaxElapsed <= 0 || p.MaxElapsed > 24*time.Hour || p.RetryBackoff <= 0 || p.RetryBackoff > time.Hour || !time.Now().Before(p.ExpiresAt) || len(p.TaskIDs) == 0 || len(p.TaskIDs) > 256 {
			return ErrDenied
		}
		sw, ok := s.SwarmPolicies[p.ProjectID]
		cp, present := s.CheckpointPolicies[p.ProjectID]
		if !ok || !present || sw.Harness != p.Harness || sw.CapabilityDigest != p.CapabilityDigest || !reflect.DeepEqual(cp.TaskIDs, p.TaskIDs) || p.MaxWaves > cp.MaxCycles || p.MaxNoProgress > cp.MaxNoProgress {
			return ErrDenied
		}
		for i, t := range p.TaskIDs {
			if !validID(t) || (i > 0 && t == p.TaskIDs[i-1]) {
				return ErrDenied
			}
		}
		key := p.ProjectID + ":" + p.Revision
		if old, ok := s.ControllerPolicyHistory[key]; ok {
			if !reflect.DeepEqual(old, p) {
				return ErrDenied
			}
			return nil
		}
		s.ControllerPolicyHistory[key] = p
		s.ControllerPolicies[p.ProjectID] = p
		return nil
	})
}
func controllerScope(s *state, token, session, project, action string, tasks []string) error {
	r, e := authenticate(s, token, session, time.Now())
	if e != nil {
		return e
	}
	if !subset([]string{action}, r.Scope.Actions) || !subset([]string{"workspace:" + s.Binding.Workspace, "project:" + project}, r.Scope.Resources) {
		return ErrDenied
	}
	for _, task := range tasks {
		if !subset([]string{"task:" + task}, r.Scope.Resources) {
			return ErrDenied
		}
	}
	return nil
}
func (a *Authority) StartController(owner string, r ControllerStartRequest) (ControllerStatus, error) {
	return a.startController(owner, "", "", r)
}

// StartScopedController starts only the authenticated, independently enrolled
// controller. Its adapter supplies owner-pinned routes, never owner credentials.
func (a *Authority) StartScopedController(token, session string, r ControllerStartRequest) (ControllerStatus, error) {
	if session == "" || r.ControllerSessionID != session {
		return ControllerStatus{}, ErrDenied
	}
	return a.startController("", token, session, r)
}
func (a *Authority) startController(owner, token, session string, r ControllerStartRequest) (ControllerStatus, error) {
	var out ControllerStatus
	err := a.transaction(false, true, func(s *state) error {
		if s.Version < 16 || !validID(r.OperationKey) || !validID(r.InvocationID) || !validID(r.CheckpointID) {
			return ErrDenied
		}
		if session == "" {
			if len(owner) != 64 || hash(owner) != s.OwnerHash {
				return ErrDenied
			}
		} else {
			p, ok := s.ControllerPolicies[r.ProjectID]
			if !ok || controllerScope(s, token, session, r.ProjectID, "controller.start", p.TaskIDs) != nil {
				return ErrDenied
			}
		}
		digest := identity("controller.start", session, r)
		if old, ok := s.ControllerOperations[r.OperationKey]; ok {
			if old.Digest != digest {
				return ErrDenied
			}
			out = old.Status
			out.Historical = true
			out.HostActionAuthorized = false
			return nil
		}
		p, ok := s.ControllerPolicies[r.ProjectID]
		actor, exists := s.Sessions[r.ControllerSessionID]
		if !ok || !exists || actor.Parent != "" || p.Revision != r.PolicyRevision || !time.Now().Before(p.ExpiresAt) || !controllerLive(s, actor.ID) || !subset([]string{"controller.read", "controller.step", "controller.reconcile", "swarm.plan", "swarm.reserve", "swarm.dispatch", "checkpoint.record", "checkpoint.reconcile", "checkpoint.read"}, actor.Scope.Actions) {
			return ErrDenied
		}
		if !subset([]string{"workspace:" + s.Binding.Workspace, "project:" + p.ProjectID}, actor.Scope.Resources) || len(r.Routes) != len(p.TaskIDs) {
			return ErrDenied
		}
		observer, available := a.admission.(SwarmAdmission)
		if !available {
			return ErrDenied
		}
		snapshot, e := observer.ObserveSwarm(s.Binding.Workspace, p.ProjectID)
		if e != nil {
			return e
		}
		candidates := map[string]SwarmCandidate{}
		for _, c := range snapshot.Candidates {
			if _, dup := candidates[c.TaskID]; dup {
				return ErrDenied
			}
			candidates[c.TaskID] = c
		}
		for _, task := range p.TaskIDs {
			route, present := r.Routes[task]
			placement := s.SwarmPolicies[p.ProjectID].Placements[task]
			if !present || !subset([]string{"task:" + task}, actor.Scope.Resources) || !validPromptDigest(route.PromptSpecDigest) {
				return ErrDenied
			}
			binding, bound := s.Tasks[task]
			candidate, present := candidates[task]
			if !bound || !present || binding.ProjectID != p.ProjectID || !binding.AdmissionEnabled || workerTaskWithdrawn(s, route.WorkerSession, task) {
				return ErrDenied
			}
			resources, e := canonicalResources(placement.Checkout, task, candidate.Resources)
			if e != nil || len(resources) == 0 {
				return ErrDenied
			}
			d := DispatchReservation{ProjectID: p.ProjectID, Reservation: Lease{TaskID: task, Checkout: placement.Checkout, Repository: binding.Repository, Resources: resources}}
			for _, actorID := range []string{route.WorkerSession} {
				actor, ok := s.Sessions[actorID]
				if !ok || !subset([]string{"task:" + task, "checkout:" + placement.Checkout}, actor.Scope.Resources) {
					return ErrDenied
				}
			}

			if route.BridgeSession == route.WorkerSession || route.BridgeSession == actor.ID || route.WorkerSession == actor.ID || !dispatchBoundActor(s, route.BridgeSession, p.ProjectID, "dispatch.correlate", d) || !dispatchBoundActor(s, route.WorkerSession, p.ProjectID, "lease.acquire", d) {
				return ErrDenied
			}
			if len(route.Checks) > 0 {
				seen := map[string]bool{}
				for _, check := range route.Checks {
					g, ok := s.Grants[check.GrantID]
					if seen[check.CheckID] || !ok || g.Revoked || g.TaskID != task || g.Repository != binding.Repository || g.RunnerSession != route.RunnerSession || g.CheckID != check.CheckID || !time.Now().Before(g.ExpiresAt) {
						return ErrDenied
					}
					seen[check.CheckID] = true
				}
			}
			if observer, ok := a.admission.(PreflightObserver); ok {
				spec := s.PreflightPolicies[p.ProjectID].Tasks[task]
				obs, e := observer.ObservePreflight(s.Binding.Workspace, p.ProjectID, task, spec, placement)
				if e != nil {
					return e
				}
				checks, grants := obs.Checks, obs.VerificationGrantIDs
				if obs.RequiredSet != nil {
					checks, grants = obs.AvailableChecks, obs.AvailableGrantIDs
				}
				if len(checks) > 1 || obs.RequiredSet != nil {
					if len(route.Checks) != len(checks) {
						return ErrDenied
					}
					for i, c := range checks {
						found := false
						for _, r := range route.Checks {
							if r.CheckID == c.ID && i < len(grants) && r.GrantID == grants[i] {
								found = true
							}
						}
						if !found {
							return ErrDenied
						}
					}
				}
			}
			vg, vok := s.Grants[route.VerificationGrantID]
			rg, rok := s.ReviewGrants[route.ReviewGrantID]
			if !vok || !rok || vg.Repository != binding.Repository || rg.Repository != binding.Repository || rg.WorkerRoot != controllerRoot(s, route.WorkerSession) || vg.TaskID != task || vg.RunnerSession != route.RunnerSession || vg.CheckID != route.CheckID || vg.Revoked || !time.Now().Before(vg.ExpiresAt) || rg.TaskID != task || rg.ReviewerSession != route.ReviewerSession || rg.Revoked || !time.Now().Before(rg.ExpiresAt) {
				return ErrDenied
			}
			runner, runnerOK := s.Sessions[route.RunnerSession]
			reviewer, reviewerOK := s.Sessions[route.ReviewerSession]
			roots := map[string]bool{}
			for _, id := range []string{actor.ID, route.BridgeSession, route.WorkerSession, route.RunnerSession, route.ReviewerSession} {
				root := controllerRoot(s, id)
				if root == "" || roots[root] {
					return ErrDenied
				}
				roots[root] = true
			}
			if !runnerOK || runner.Parent != "" || !reviewerOK || !reviewOnly(reviewer, task) || !subset([]string{"verification.run"}, runner.Scope.Actions) || !subset([]string{"task:" + task}, runner.Scope.Resources) {
				return ErrDenied
			}
			if route.RunnerSession == actor.ID || route.ReviewerSession == actor.ID || controllerRoot(s, route.WorkerSession) == controllerRoot(s, route.ReviewerSession) || !controllerLive(s, route.RunnerSession) || !controllerLive(s, route.ReviewerSession) {
				return ErrDenied
			}
		}
		for _, iv := range s.ControllerInvocations {
			if iv.Status.ProjectID == p.ProjectID && (!iv.Status.Terminal || controllerUnresolved(s, iv)) {
				return ErrDenied
			}
		}
		if _, ok := s.ControllerInvocations[r.InvocationID]; ok {
			return ErrDenied
		}
		// All start-time pure actor/grant predicates survive earlier slow observers.
		if !controllerLive(s, actor.ID) || !time.Now().Before(p.ExpiresAt) {
			return ErrDenied
		}
		for _, route := range r.Routes {
			if !controllerLive(s, route.BridgeSession) || !controllerLive(s, route.WorkerSession) || !controllerLive(s, route.RunnerSession) || !controllerLive(s, route.ReviewerSession) || !time.Now().Before(s.Grants[route.VerificationGrantID].ExpiresAt) || !time.Now().Before(s.ReviewGrants[route.ReviewGrantID].ExpiresAt) {
				return ErrDenied
			}
		}
		if session != "" && controllerScope(s, token, session, r.ProjectID, "controller.start", p.TaskIDs) != nil {
			return ErrDenied
		}
		now := time.Now().UTC()
		deadline := now.Add(p.MaxElapsed)
		if deadline.After(p.ExpiresAt) {
			deadline = p.ExpiresAt
		}
		if deadline.After(actor.ExpiresAt) {
			deadline = actor.ExpiresAt
		}
		out = ControllerStatus{SchemaVersion: 1, ID: r.InvocationID, ProjectID: p.ProjectID, ControllerSessionID: actor.ID, PolicyRevision: p.Revision, Harness: p.Harness, CheckpointID: r.CheckpointID, Sequence: 1, Phase: "reconcile", StartedAt: now, Deadline: deadline, Assurance: "cooperative-native", Actions: []ControllerAction{}, Refs: []CheckpointRefs{}}
		s.ControllerInvocations[out.ID] = controllerInvocation{Status: out, Policy: p, Routes: r.Routes, CorrectionSources: map[string]string{}}
		s.ControllerHistory[out.ID] = []ControllerStatus{out}
		s.ControllerOperations[r.OperationKey] = controllerReceipt{Digest: digest, Status: out}
		return nil
	})
	if err != nil {
		return ControllerStatus{}, err
	}
	return out, nil
}
func (a *Authority) ControllerStatus(token, session, project, id string) (ControllerStatus, error) {
	var out ControllerStatus
	e := a.transaction(false, false, func(s *state) error {
		iv, ok := s.ControllerInvocations[id]
		if s.Version < 16 || !ok || iv.Status.ProjectID != project || iv.Status.ControllerSessionID != session {
			return ErrDenied
		}
		if e := controllerScope(s, token, session, project, "controller.read", iv.Policy.TaskIDs); e != nil {
			return e
		}
		out = iv.Status
		out.Historical = true
		out.HostActionAuthorized = false
		return nil
	})
	if e != nil {
		return ControllerStatus{}, e
	}
	return out, nil
}

func controllerRoot(s *state, id string) string {
	_, root, e := rootSession(s, id, time.Now())
	if e != nil {
		return ""
	}
	return root
}
func controllerLive(s *state, id string) bool { return controllerRoot(s, id) != "" }

func controllerUnresolved(s *state, iv controllerInvocation) bool {
	for _, action := range iv.Status.Actions {
		d, ok := s.Dispatches[action.DispatchID]
		if !ok {
			return true
		}
		i, present := s.DispatchIntents[d.ID]
		if reservationRetained(s, d) {
			return true
		}
		if present && i.Lease != nil && !s.Leases[i.Lease.ID].Released {
			return true
		}
	}
	for _, ref := range iv.Status.Refs {
		if op, ok := s.Operations[ref.VerificationKey]; ok && op.Pending {
			return true
		}
	}
	return false
}

// ControllerEnrollmentScope derives exact declared tasks and current claims;
// enrollment grants controller actions, never owner or producer authority.
func (a *Authority) ControllerEnrollmentScope(owner, project string) (Scope, error) {
	var scope Scope
	e := a.transaction(false, false, func(s *state) error {
		if s.Version < 16 || len(owner) != 64 || hash(owner) != s.OwnerHash {
			return ErrDenied
		}
		p, ok := s.ControllerPolicies[project]
		observer, available := a.admission.(SwarmAdmission)
		if !ok || !available || !time.Now().Before(p.ExpiresAt) {
			return ErrDenied
		}
		snap, e := observer.ObserveSwarm(s.Binding.Workspace, project)
		if e != nil {
			return e
		}
		scope = Scope{Actions: []string{"read", "controller.start", "controller.stop", "dispatch.cancel", "controller.read", "controller.step", "controller.reconcile", "swarm.plan", "swarm.reserve", "swarm.dispatch", "checkpoint.read", "checkpoint.record", "checkpoint.reconcile", "budget.read", "budget.quote"}, Resources: []string{"workspace:" + s.Binding.Workspace, "project:" + project}}
		seen := map[string]bool{}
		for _, c := range snap.Candidates {
			if !subset([]string{c.TaskID}, p.TaskIDs) {
				continue
			}
			if seen[c.TaskID] {
				return ErrDenied
			}
			seen[c.TaskID] = true
			scope.Resources = append(scope.Resources, "task:"+c.TaskID)
			for _, r := range c.Resources {
				scope.Resources = append(scope.Resources, r.Kind+":"+r.Path)
			}
		}
		// Multiple declared tasks can legitimately claim the same resource.
		// Enrollment requires a set, while task identity remains independently unique.
		resources := make([]string, 0, len(scope.Resources))
		resourceSet := map[string]bool{}
		for _, resource := range scope.Resources {
			if !resourceSet[resource] {
				resourceSet[resource] = true
				resources = append(resources, resource)
			}
		}
		scope.Resources = resources
		if len(seen) != len(p.TaskIDs) || !time.Now().Before(p.ExpiresAt) {
			return ErrDenied
		}
		return nil
	})
	if e != nil {
		return Scope{}, e
	}
	return scope, nil
}

// genericControllerMutation rejects a bounded controller at public effect APIs.
// Atomic controller transitions use the shared private state helpers instead.
func genericControllerMutation(s *state, token, session string) error {
	actor, e := authenticate(s, token, session, time.Now())
	if e != nil {
		return e
	}
	if s.Version >= 16 {
		// Delegation cannot shed the bounded-controller family's effect boundary.
		for depth := 0; depth <= len(s.Sessions); depth++ {
			if subset([]string{"controller.step"}, actor.Scope.Actions) || subset([]string{"controller.reconcile"}, actor.Scope.Actions) {
				return ErrDenied
			}
			if actor.Parent == "" {
				return nil
			}
			parent, ok := s.Sessions[actor.Parent]
			if !ok {
				return ErrDenied
			}
			actor = parent
		}
		return ErrDenied
	}
	return nil
}

// StopController halts future invocation effects. Existing actions and occupied
// resources remain protected; this is not a host termination acknowledgement.
func (a *Authority) StopController(token, session string, r ControllerStepRequest) (ControllerStatus, error) {
	var out ControllerStatus
	e := a.transaction(false, true, func(s *state) error {
		iv, ok := s.ControllerInvocations[r.InvocationID]
		if s.Version < 16 || !ok || iv.Status.ProjectID != r.ProjectID || iv.Status.ControllerSessionID != session || !validID(r.OperationKey) {
			return ErrDenied
		}
		if e := controllerScope(s, token, session, r.ProjectID, "controller.stop", iv.Policy.TaskIDs); e != nil {
			return e
		}
		digest := identity("controller.stop", session, r)
		if old, ok := s.ControllerOperations[r.OperationKey]; ok {
			if old.Digest != digest {
				return ErrDenied
			}
			out = old.Status
			out.Historical = true
			out.HostActionAuthorized = false
			return nil
		}
		if iv.Status.Sequence != r.ExpectedSequence {
			return ErrDenied
		}
		controllerHalt(&iv, "stopped", "controller stopped; retain dispatches until authenticated host completion or owner reconciliation", false, true)
		iv.Status.Sequence++
		iv.Status.Historical = false
		iv.Status.ObservedAt = time.Now().UTC()
		iv.Status.RetainedConcurrency = controllerOccupancy(s, r.ProjectID)
		out = iv.Status
		s.ControllerInvocations[out.ID] = iv
		s.ControllerHistory[out.ID] = append(s.ControllerHistory[out.ID], out)
		s.ControllerOperations[r.OperationKey] = controllerReceipt{Digest: digest, Status: out}
		return nil
	})
	if e != nil {
		return ControllerStatus{}, e
	}
	return out, nil
}
