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
	ExecutionConcurrency                                                                                               int                    `json:",omitempty"`
	ExecutionConcurrencyKnown                                                                                          bool                   `json:",omitempty"`
	HostSlotAdmission                                                                                                  *HostSlotAdmissionView `json:",omitempty"`
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
		closedRoutes := map[string]bool{}
		for _, task := range p.TaskIDs {
			route, present := r.Routes[task]
			placement := s.SwarmPolicies[p.ProjectID].Placements[task]
			if !present || !subset([]string{"task:" + task}, actor.Scope.Resources) || !validPromptDigest(route.PromptSpecDigest) {
				return ErrDenied
			}
			binding, bound := s.Tasks[task]
			candidate, present := candidates[task]
			if !bound || !present || binding.ProjectID != p.ProjectID || !binding.AdmissionEnabled {
				return ErrDenied
			}
			if candidate.Status == "done" {
				if !controllerClosedTaskRoute(s, r.CheckpointID, p.ProjectID, task, route, placement.Checkout) {
					return ErrDenied
				}
				closedRoutes[task] = true
				continue
			}
			if workerTaskWithdrawn(s, route.WorkerSession, task) {
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
		// Reobserve canonical DONE after any open task's slow preflight observer.
		// Closed routes are historical bookkeeping, never renewed producer grants.
		if len(closedRoutes) > 0 {
			fresh, e := observer.ObserveSwarm(s.Binding.Workspace, p.ProjectID)
			if e != nil {
				return e
			}
			seen := map[string]bool{}
			for _, candidate := range fresh.Candidates {
				if !closedRoutes[candidate.TaskID] {
					continue
				}
				if seen[candidate.TaskID] || candidate.ProjectID != p.ProjectID || candidate.Status != "done" || !controllerClosedTaskRoute(s, r.CheckpointID, p.ProjectID, candidate.TaskID, r.Routes[candidate.TaskID], s.SwarmPolicies[p.ProjectID].Placements[candidate.TaskID].Checkout) {
					return ErrDenied
				}
				seen[candidate.TaskID] = true
			}
			if len(seen) != len(closedRoutes) {
				return ErrDenied
			}
		}
		for task, route := range r.Routes {
			if closedRoutes[task] {
				continue
			}
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
		// A continuation must carry every original producer reference into its next
		// checkpoint write. Starting with empty refs would correctly trip the
		// immutable-history gate on the first step (core643). Copy only bookkeeping;
		// checkpoint bounds and prior invocation receipts remain untouched.
		if history := s.Checkpoints[r.CheckpointID]; len(history) > 0 {
			latest := history[len(history)-1]
			out.Refs = append(out.Refs, latest.Refs...)
			out.CheckpointSequence = latest.Sequence
		}
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

// A canonical DONE task may retain its original route as historical bookkeeping
// after native acceptance withdrew its producers (core641). Only an exact prior
// same-checkpoint dispatch, independent producer history and closed owner receipts
// qualify. This neither releases protection nor modifies any invocation bounds.
func controllerClosedTaskRoute(s *state, checkpoint, project, task string, route ControllerTaskRoute, checkout string) bool {
	terminal, ok := s.TaskTerminals[task]
	if !ok || terminal.State != "closed" || terminal.ClosedAt.IsZero() {
		return false
	}
	input := terminal.Candidate.Input
	input.OperationKey = terminal.OperationKey
	if input.ProjectID != project || input.TaskID != task || !acceptanceRecordMatchesInput(terminal, input) || terminal.Candidate.WorkerSessionID != route.WorkerSession || terminal.Candidate.ReviewerSessionID != route.ReviewerSession {
		return false
	}
	family, err := historicalWorkerFamily(s, route.WorkerSession)
	if err != nil || family != terminal.Candidate.WorkerFamilyID {
		return false
	}
	at, err := reviewAttempt(s, input.AttemptID)
	review, present := s.Reviews[input.ReviewID]
	binding, bound := s.Tasks[task]
	if err != nil || !present || !bound || binding.Repository != input.Repository || at.SessionID != route.WorkerSession || at.Request.TaskID != task || at.ProjectID != project || at.Observation.Locator != checkout || at.Observation.Repository != input.Repository || review.IssuerSession != route.ReviewerSession || review.Request.GrantID != route.ReviewGrantID || review.Request.Outcome != "approve" || review.Request.AttemptID != input.AttemptID || review.Request.LeaseID != input.LeaseID || review.Request.RevisionID != input.RevisionID || review.Request.ExpectedFence != input.ExpectedFence || review.Revision.ID != input.RevisionID || review.Revision.AttemptID != input.AttemptID || review.Revision.SessionID != route.WorkerSession || review.Revision.LeaseID != input.LeaseID || review.Revision.Fence != input.ExpectedFence || review.Revision.Observation.Commit != input.Commit || review.Revision.Observation.Tree != input.Tree || review.Revision.Observation.Repository != input.Repository || review.Observation.ChecklistDigest != input.ChecklistDigest || review.Observation.PolicyRevision != input.PolicyRevision {
		return false
	}
	rg, granted := s.ReviewGrants[route.ReviewGrantID]
	if !granted || rg.TaskID != task || rg.Repository != input.Repository || rg.ReviewerSession != route.ReviewerSession || rg.WorkerRoot != terminal.Candidate.WorkerFamilyID {
		return false
	}
	checks := route.Checks
	if len(checks) == 0 {
		checks = []ControllerCheckRoute{{CheckID: route.CheckID, GrantID: route.VerificationGrantID}}
	}
	if len(checks) != len(review.Request.EvidenceIDs) || len(checks) != len(terminal.Candidate.Evidence) {
		return false
	}
	seen := map[string]bool{}
	for _, id := range review.Request.EvidenceIDs {
		ev, exists := s.Evidence[id]
		g, granted := s.Grants[ev.GrantID]
		if !exists || !granted || ev.Provenance != "trusted-local-runner" || ev.ExitCode != 0 || ev.IssuerSession != route.RunnerSession || !reflect.DeepEqual(ev.Revision, review.Revision) || g.RunnerSession != route.RunnerSession || g.TaskID != task || g.Repository != input.Repository || g.CheckID != ev.CheckID || g.CommandDigest != ev.CommandDigest || seen[ev.CheckID] {
			return false
		}
		matched := false
		for _, check := range checks {
			if check.CheckID == ev.CheckID && check.GrantID == ev.GrantID {
				matched = true
			}
		}
		candidateMatched := false
		for _, evidence := range terminal.Candidate.Evidence {
			if evidence.ID == id && evidence.RunnerSessionID == ev.IssuerSession && evidence.CheckID == ev.CheckID && evidence.CommandDigest == ev.CommandDigest && evidence.OutputDigest == ev.OutputDigest {
				candidateMatched = true
			}
		}
		if !matched || !candidateMatched {
			return false
		}
		seen[ev.CheckID] = true
	}
	for _, previous := range s.ControllerInvocations {
		if previous.Status.ProjectID != project || previous.Status.CheckpointID != checkpoint || !reflect.DeepEqual(previous.Routes[task], route) {
			continue
		}
		for _, action := range previous.Status.Actions {
			if action.TaskID != task || action.AttemptID != input.AttemptID || action.LeaseID != input.LeaseID || action.RevisionID != input.RevisionID || action.Fence != input.ExpectedFence || action.ReviewID != input.ReviewID {
				continue
			}
			dispatch, exists := s.Dispatches[action.DispatchID]
			if !exists || dispatch.ProjectID != project || dispatch.Reservation.TaskID != task || dispatch.Reservation.Fence != action.ReservationFence || dispatch.Reservation.Checkout != checkout || dispatch.Reservation.Repository != input.Repository || !dispatch.Converted || dispatch.WorkerSessionID != route.WorkerSession || dispatch.AttemptID != input.AttemptID {
				continue
			}
			// The old action may have halted before recording accepted bookkeeping;
			// the exact closed terminal supplies that historical operation identity.
			action.Kind, action.OperationKey = "accepted", terminal.OperationKey
			if controllerIntegrationActionHistorical(s, previous, action) {
				return true
			}
		}
	}
	return false
}

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
			// Owner recovery preserves unknown verification operations. Only the
			// exact recovered protection makes this old producer historical;
			// ordinary release or a missing/mismatched lease does not (core633).
			request := ref.Verification
			binding := op.CheckpointVerification
			if binding == nil || binding.ProjectID != iv.Status.ProjectID || binding.TaskID != ref.TaskID || binding.RunnerSession != ref.RunnerSession || binding.Request != request || op.Digest != identity("run:"+binding.DescriptorDigest, binding.RunnerSession, request) {
				return true
			}
			lease, present := s.Leases[request.LeaseID]
			recovery, retired := checkpointRetiredLease(s, request.LeaseID)
			target := recovery.Target
			if !present || !retired || !lease.Released || lease.ID != request.LeaseID || lease.Fence != request.ExpectedFence || lease.AttemptID != request.AttemptID || lease.SessionID != request.WorkerSession || lease.TaskID != ref.TaskID || target.ExpectedFence != lease.Fence || target.AttemptID != lease.AttemptID || target.SessionID != lease.SessionID || target.TaskID != lease.TaskID || target.Repository != lease.Repository || target.Checkout != lease.Checkout {
				return true
			}
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
		refreshControllerConcurrency(s, &iv.Status)
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
