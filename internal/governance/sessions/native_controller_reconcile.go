package sessions

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"time"
)

// Only admission facts enter the checkpoint condition; receipt/poll bookkeeping
// cannot invalidate a freshly observed gate or manufacture progress.
type controllerAdmission struct {
	Policy                                                   ControllerPolicy
	Controller, Project, ID, Checkpoint, TerminalClass       string
	Deadline                                                 time.Time
	Waves, Cycle, Corrections, NoProgress, TransientAttempts int
	NextRetryAt                                              time.Time
	PreflightGeneration                                      string
	Pending                                                  []ControllerAction
	Terminal                                                 bool
}

func controllerCondition(s *state) map[string]controllerAdmission {
	out := map[string]controllerAdmission{}
	for id, iv := range s.ControllerInvocations {
		st := iv.Status
		out[id] = controllerAdmission{Policy: iv.Policy, Controller: st.ControllerSessionID, Project: st.ProjectID, ID: id, Checkpoint: st.CheckpointID, Deadline: st.Deadline, Waves: st.Waves, Cycle: st.CycleOrdinal, Corrections: st.Corrections, NoProgress: st.NoProgress, TransientAttempts: st.TransientAttempts, NextRetryAt: st.NextRetryAt, PreflightGeneration: iv.PreflightGeneration, Pending: st.Actions, Terminal: st.Terminal, TerminalClass: st.HaltClass}
	}
	return out
}
func controllerCurrent(s *state, iv controllerInvocation, now time.Time) error {
	p, ok := s.ControllerPolicies[iv.Status.ProjectID]
	sw, present := s.SwarmPolicies[iv.Status.ProjectID]
	if !ok || !present || !reflect.DeepEqual(p, iv.Policy) || !now.Before(p.ExpiresAt) || !now.Before(iv.Status.Deadline) || sw.Stopped || sw.Harness != p.Harness || sw.CapabilityDigest != p.CapabilityDigest || !controllerLive(s, iv.Status.ControllerSessionID) {
		return ErrDenied
	}
	return nil
}
func controllerHalt(iv *controllerInvocation, class, next string, retry, terminal bool) {
	iv.Status.Phase = "halt"
	iv.Status.HaltClass = class
	iv.Status.NextAction = next
	iv.Status.Retryable = retry
	iv.Status.Terminal = terminal
	iv.Status.HostActionAuthorized = false
}
func (a *Authority) StepController(token, session string, r ControllerStepRequest) (ControllerStatus, error) {
	return a.advanceController(token, session, r, false)
}
func (a *Authority) ReconcileController(token, session string, r ControllerStepRequest) (ControllerStatus, error) {
	return a.advanceController(token, session, r, true)
}
func (a *Authority) advanceController(token, session string, r ControllerStepRequest, observeOnly bool) (ControllerStatus, error) {
	var out ControllerStatus
	var refusal error
	action := "controller.step"
	if observeOnly {
		action = "controller.reconcile"
	}
	err := a.transaction(false, true, func(s *state) error {
		iv, ok := s.ControllerInvocations[r.InvocationID]
		if s.Version < 16 || !ok || iv.Status.ProjectID != r.ProjectID || iv.Status.ControllerSessionID != session || !validID(r.OperationKey) {
			return ErrDenied
		}
		if e := controllerScope(s, token, session, r.ProjectID, action, iv.Policy.TaskIDs); e != nil {
			return e
		}
		digest := identity(action, session, r)
		if old, ok := s.ControllerOperations[r.OperationKey]; ok {
			if old.Digest != digest {
				return ErrDenied
			}
			out = old.Status
			out.HostActionAuthorized = false
			out.Historical = true
			if old.RefusalClass == "policy" {
				refusal = ErrDenied
			}
			if old.RefusalClass == "transient" {
				refusal = ErrPreflightTransient
			}
			return nil
		}
		if iv.Status.Sequence != r.ExpectedSequence {
			return ErrDenied
		}
		if iv.Status.Terminal && !observeOnly {
			out = iv.Status
			out.Historical = true
			refusal = ErrDenied
			return nil
		}
		// Helpers may mutate trial allocations/fences before a final observer refuses.
		// Discard the whole trial on refusal, retaining only the freshly diagnosed halt.
		trial, e := cloneAuthorityState(s)
		if e != nil {
			return e
		}
		candidate := trial.ControllerInvocations[r.InvocationID]
		candidate.Status.HostActionAuthorized = false
		candidate.Status.Historical = false
		candidate.Status.ObservedAt = time.Now().UTC()
		transitionErr := a.controllerTransition(&trial, token, session, &candidate, observeOnly)
		if transitionErr != nil {
			candidate = iv
			candidate.Status.ObservedAt = time.Now().UTC()
			if errors.Is(transitionErr, ErrPreflightTransient) {
				generation := controllerPreflightGeneration(s, candidate.Policy)
				if candidate.Status.TransientAttempts >= candidate.Policy.TransientRetries || candidate.Status.TransientAttempts > 0 && candidate.PreflightGeneration == generation {
					controllerHalt(&candidate, "transient_retry_limit", "bounded retry requires a new authenticated548 check; owner reconciles exhaustion", false, true)
					refusal = ErrDenied
				} else {
					candidate.Status.TransientAttempts++
					candidate.PreflightGeneration = generation

					controllerHalt(&candidate, "transient_preflight", "perform bounded authenticated CheckPreflight before resuming", true, false)
					candidate.Status.NextRetryAt = time.Now().Add(candidate.Policy.RetryBackoff)
					refusal = ErrPreflightTransient
				}
			} else {
				controllerHalt(&candidate, "policy_refused", "owner examines exact refusal; do not retry unchanged", false, true)
				refusal = ErrDenied
			}
		} else {
			*s = trial
		}
		candidate.Status.Sequence++
		if e := controllerScope(s, token, session, r.ProjectID, action, candidate.Policy.TaskIDs); e != nil {
			return e
		}
		// Observation may remain useful after STOP; a new effect never survives a
		// policy/deadline expiry during the last canonical/Git callback.
		if transitionErr == nil && !observeOnly && !candidate.Status.Terminal && (controllerCurrent(s, candidate, time.Now()) != nil || !controllerFreshActions(s, candidate)) {
			return ErrDenied
		}
		out = candidate.Status
		s.ControllerInvocations[out.ID] = candidate
		s.ControllerHistory[out.ID] = append(s.ControllerHistory[out.ID], out)
		class := ""
		if errors.Is(refusal, ErrDenied) {
			class = "policy"
		}
		if errors.Is(refusal, ErrPreflightTransient) {
			class = "transient"
		}
		s.ControllerOperations[r.OperationKey] = controllerReceipt{Digest: digest, Status: out, RefusalClass: class}
		return nil
	})
	if err != nil {
		return ControllerStatus{}, err
	}
	return out, refusal
}
func controllerCycleID(st ControllerStatus) string {
	return identity("controller.cycle", st.ID, st.CycleOrdinal)
}
func controllerKey(st ControllerStatus, kind, id string) string {
	return identity("controller.action", st.ID, []string{kind, id})
}
func (a *Authority) controllerTransition(s *state, token, session string, iv *controllerInvocation, observeOnly bool) error {
	st := &iv.Status
	cp, ok := s.CheckpointPolicies[st.ProjectID]
	if !ok {
		return ErrDenied
	}
	// Reconcile current scoped canonical and durable producer state on every call,
	// including reopen. Its wave blocker findings remain visible during a wave.
	snapshot, e := a.reconcileCheckpoint(s, cp, CheckpointStatus{ID: st.CheckpointID, ProjectID: st.ProjectID, InvocationID: controllerCycleID(*st), Refs: st.Refs}, token, session)
	if e != nil {
		return e
	}
	st.Checkpoint = &snapshot
	oldProgress := st.ProgressDigest
	newProgress := controllerProducerProgress(s, *iv, snapshot.ProgressDigest)
	st.RetainedConcurrency = controllerOccupancy(s, st.ProjectID)
	if observeOnly {
		st.NextAction = "inspect current scoped producer facts; no action permitted"
		return nil
	}
	if s.SwarmPolicies[st.ProjectID].Stopped {
		controllerHalt(iv, "stopped", "owner STOP prevents new effects; retain existing writers", false, true)
		return nil
	}
	if !time.Now().Before(st.Deadline) {
		controllerHalt(iv, "elapsed_limit", "fixed invocation deadline exhausted", false, true)
		return nil
	}
	if controllerCurrent(s, *iv, time.Now()) != nil {
		return ErrDenied
	}
	if st.NextRetryAt.After(time.Now()) {
		st.Phase = "wait"
		st.HaltClass = "transient_backoff"
		st.NextAction = "wait until durable retry deadline; no attempt spent"
		st.Retryable = true
		return nil
	}
	st.ProgressDigest = newProgress
	// An active wave is progressed through original identities; retained protection
	// does not grant replacement work and does not prevent its own observations.
	if len(st.Actions) > 0 && oldProgress != "" {
		if oldProgress == st.ProgressDigest {
			st.NoProgress++
		} else {
			st.NoProgress = 0
		}
		if st.NoProgress >= iv.Policy.MaxNoProgress {
			controllerHalt(iv, "no_progress", "trusted producer or canonical progress required; retain unknown work", false, true)
			return nil
		}
	}
	if len(st.Actions) > 0 {
		return a.controllerProgressWave(s, token, session, iv)
	}
	if st.Waves >= iv.Policy.MaxWaves {
		controllerHalt(iv, "wave_limit", "committed wave limit exhausted", false, true)
		return nil
	}
	if st.RetainedConcurrency >= iv.Policy.MaxConcurrency {
		controllerHalt(iv, "concurrency_limit", "owner reconciles retained writers before another wave", false, false)
		return nil
	}
	st.CycleOrdinal++
	s.ControllerInvocations[st.ID] = *iv
	h := s.Checkpoints[st.CheckpointID]
	seq := uint64(len(h))
	request := CheckpointRequest{OperationKey: controllerKey(*st, "pre-cycle", fmt.Sprint(st.CycleOrdinal)), CheckpointID: st.CheckpointID, ProjectID: st.ProjectID, InvocationID: controllerCycleID(*st), Phase: "pre-cycle", ExpectedSequence: seq, Refs: st.Refs}
	current, e := a.writeCheckpointState(s, token, session, request, true)
	if e != nil {
		return e
	}
	st.Checkpoint = &current
	st.CheckpointSequence = current.Sequence
	st.NoProgress = current.NoProgress
	if st.NoProgress >= iv.Policy.MaxNoProgress {
		controllerHalt(iv, "no_progress", "canonical or trusted producer progress required", false, true)
		return nil
	}
	limit := iv.Policy.MaxConcurrency - st.RetainedConcurrency
	plan, e := a.planSwarmLimited(s, token, session, st.ProjectID, "swarm.reserve", limit, iv.Policy.TaskIDs)
	if e != nil {
		return e
	}
	selected := 0
	for _, v := range plan.Verdicts {
		if v.Selected {
			selected++
		}
	}
	if selected == 0 {
		empty, e := a.controllerBacklogEmpty(s, st.ProjectID, iv.Policy.TaskIDs)
		if e != nil {
			return e
		}
		if empty {
			st.Phase = "completed"
			st.Terminal = true
			st.HaltClass = "empty_backlog"
			st.NextAction = "invocation ended; no task or reservation created"
			return nil
		}
		if current.Retryable && current.HaltClass == "transient_preflight" {
			return ErrPreflightTransient
		}
		controllerHalt(iv, current.HaltClass, current.NextAction, current.Retryable, !current.Retryable)
		if st.HaltClass == "" {
			st.HaltClass = "no_schedulable_work"
			st.NextAction = "resolve scoped readiness/resource or capability blockers"
		}
		return nil
	}
	if !current.WaveAllowed {
		if current.Retryable && current.HaltClass == "transient_preflight" {
			return ErrPreflightTransient
		}
		controllerHalt(iv, current.HaltClass, current.NextAction, current.Retryable, !current.Retryable)
		return nil
	}
	expiry := time.Now().Add(s.SwarmPolicies[st.ProjectID].TTL)
	if expiry.After(st.Deadline) {
		expiry = st.Deadline
	}
	wave, e := a.reserveWaveStateLimited(s, token, session, ReserveWaveRequest{OperationKey: controllerKey(*st, "reserve", fmt.Sprint(st.CycleOrdinal)), Plan: plan, ExpiresAt: expiry, CheckpointID: current.ID, CheckpointSequence: current.Sequence, InvocationID: current.InvocationID, ReconciliationDigest: current.ConditionDigest}, limit, iv.Policy.TaskIDs)
	if e != nil {
		return e
	}
	st.Waves++
	st.Phase = "dispatch"
	st.HaltClass = ""
	st.Retryable = false
	st.NextAction = "request original dispatch intent"
	for _, d := range wave.Dispatches {
		if d.Routing != nil {
			for _, id := range []string{d.Routing.Route.BridgeSession, d.Routing.Route.WorkerSession, d.Routing.Route.RunnerSession, d.Routing.Route.ReviewerSession} {
				if controllerRoot(s, id) == controllerRoot(s, session) {
					return ErrDenied
				}
			}
			iv.Routes[d.Reservation.TaskID] = d.Routing.Route
		}
		st.Actions = append(st.Actions, ControllerAction{ID: d.ID, Kind: "dispatch", TaskID: d.Reservation.TaskID, DispatchID: d.ID, Fence: d.Reservation.Fence, ReservationFence: d.Reservation.Fence})
		st.Refs = append(st.Refs, CheckpointRefs{TaskID: d.Reservation.TaskID, DispatchID: d.ID})
	}
	return nil
}
func controllerOccupancy(s *state, project string) int {
	ids := map[string]bool{}
	for _, d := range s.Dispatches {
		if d.ProjectID != project {
			continue
		}
		if d.Converted {
			for _, l := range s.Leases {
				if !l.Released && l.TaskID == d.Reservation.TaskID && l.SessionID == d.WorkerSessionID {
					ids[l.ID] = true
				}
			}
		} else if reservationRetained(s, d) {
			ids[d.ID] = true
		}
	}
	for id, l := range s.Leases {
		if !l.Released && s.Tasks[l.TaskID].ProjectID == project {
			ids[id] = true
		}
	}
	return len(ids)
}
func (a *Authority) controllerBacklogEmpty(s *state, project string, tasks []string) (bool, error) {
	observer, ok := a.admission.(SwarmAdmission)
	if !ok {
		return false, ErrDenied
	}
	snap, e := observer.ObserveSwarm(s.Binding.Workspace, project)
	if e != nil {
		return false, e
	}
	seen := map[string]bool{}
	for _, c := range snap.Candidates {
		if !subset([]string{c.TaskID}, tasks) {
			continue
		}
		seen[c.TaskID] = true
		if c.Status != "done" && c.Status != "cancelled" {
			return false, nil
		}
	}
	// Missing configured tasks are inconsistent, never an empty backlog.
	return len(seen) == len(tasks), nil
}
func controllerRef(st *ControllerStatus, ref CheckpointRefs) {
	for _, r := range st.Refs {
		if reflect.DeepEqual(r, ref) {
			return
		}
	}
	st.Refs = append(st.Refs, ref)
}
func (a *Authority) controllerProgressWave(s *state, token, session string, iv *controllerInvocation) error {
	st := &iv.Status
	for _, pending := range st.Actions {
		if _, present := s.DispatchIntents[pending.DispatchID]; !present {
			d, ok := s.Dispatches[pending.DispatchID]
			if !ok {
				return ErrDenied
			}
			route := iv.Routes[pending.TaskID]
			result, e := a.requestDispatchIntentState(s, token, session, RequestDispatchIntent{OperationKey: controllerKey(*st, "dispatch", d.ID), DispatchID: d.ID, ExpectedFence: d.Reservation.Fence, BridgeSessionID: route.BridgeSession, WorkerSessionID: route.WorkerSession, PromptSpecDigest: route.PromptSpecDigest})
			if e != nil {
				return e
			}
			for n := range st.Actions {
				if st.Actions[n].DispatchID == d.ID {
					st.Actions[n].Kind = "host_dispatch"
					st.Actions[n].ActorID = route.BridgeSession
					st.Actions[n].IntentID = result.Intent.ID
					st.Actions[n].OperationKey = result.Intent.OperationKey
				}
			}
			st.HostActionAuthorized = result.HostActionAuthorized
			st.Phase = "await_host"
			st.NextAction = "separate bridge consumes original first-use intent"
			controllerRef(st, CheckpointRefs{TaskID: pending.TaskID, DispatchID: d.ID, IntentID: result.Intent.ID})
			return nil
		}
	}
	allRetired := true
	for n, action := range st.Actions {
		d, ok := s.Dispatches[action.DispatchID]
		if !ok || d.ProjectID != st.ProjectID || d.Reservation.Fence != action.ReservationFence {
			return ErrDenied
		}
		route := iv.Routes[action.TaskID]
		i, present := s.DispatchIntents[d.ID]
		if action.Kind == "integration_checkpoint" || action.Kind == "canonical_acceptance" || action.Kind == "accepted" {
			at, ae := reviewAttempt(s, action.AttemptID)
			if ae != nil {
				return ae
			}
			var rev AttemptRevision
			for _, v := range s.Revisions[at.ID] {
				if v.ID == action.RevisionID {
					rev = v
				}
			}
			review, found := s.Reviews[action.ReviewID]
			if rev.ID == "" || !found {
				return ErrDenied
			}
			handled, e := a.controllerIntegrationProgress(s, iv, n, action, at, rev, review)
			if e != nil {
				return e
			}
			if handled {
				if !controllerActionRetired(s, d) {
					allRetired = false
				}
				continue
			}
		}
		if controllerActionRetired(s, d) {
			continue
		}
		allRetired = false
		if !present {
			return ErrDenied
		}
		if i.State != "acknowledged" || i.Lease == nil {
			st.Phase = "observe"
			st.HaltClass = "dispatch_unknown"
			st.NextAction = "bridge reconciles original host intent; never relaunch"
			continue
		}
		if !controllerLifecycleKnown(s, i) {
			st.Phase = "observe"
			st.HaltClass = "lifecycle_unknown"
			st.NextAction = "fresh authenticated host observation required; retain original writer"
			continue
		}
		if _, cancelled := s.LifecycleCancellations[d.ID]; cancelled {
			controllerHalt(iv, "withdrawn_execution", "owner reconciles retained cancelled writer", false, true)
			return nil
		}
		at, attemptErr := reviewAttempt(s, d.AttemptID)
		if attemptErr != nil || at.SessionID != route.WorkerSession || at.Request.TaskID != action.TaskID || at.ProjectID != st.ProjectID {
			return ErrDenied
		}
		history := s.Revisions[at.ID]
		if len(history) == 0 {
			st.Phase = "observe"
			st.HaltClass = "revision_pending"
			st.NextAction = "assigned worker publishes exact current revision"
			continue
		}
		rev := history[len(history)-1]
		if _, e := currentRevision(s, at, rev.ID, i.Lease.ID, i.Lease.Fence); e != nil {
			return e
		}
		if e := observeRevision(at, rev); e != nil {
			return e
		}
		required, requests, e := a.controllerRequiredVerification(s, *iv, at, rev, route)
		if e != nil {
			return e
		}
		evidenceIDs := []string{}
		pending, missing := false, false
		failed := false
		for _, request := range requests {
			controllerRef(st, CheckpointRefs{TaskID: action.TaskID, DispatchID: d.ID, IntentID: i.ID, RunnerSession: route.RunnerSession, VerificationKey: request.OperationKey, Verification: request})
			op, present := s.Operations[request.OperationKey]
			if !present {
				missing = true
				continue
			}
			if op.Pending || op.Evidence == nil {
				pending = true
				continue
			}
			ev := *op.Evidence
			if !controllerEvidenceCurrent(s, ev, request, route.RunnerSession, op) {
				return ErrDenied
			}
			controllerRef(st, CheckpointRefs{TaskID: action.TaskID, DispatchID: d.ID, EvidenceID: ev.ID})
			if ev.ExitCode != 0 {
				if e := a.controllerCorrection(s, iv, n, action, rev.ID, ev.ID); e != nil {
					return e
				}
				failed = true
				break
			}
			evidenceIDs = append(evidenceIDs, ev.ID)
		}
		if st.Terminal {
			return nil
		}
		if failed {
			continue
		}
		if missing || pending {
			staged := ControllerAction{ID: controllerKey(*st, "verify-action", rev.ID), Kind: "verification", ActorID: route.RunnerSession, TaskID: action.TaskID, DispatchID: d.ID, IntentID: i.ID, AttemptID: at.ID, LeaseID: rev.LeaseID, RevisionID: rev.ID, Fence: rev.Fence, ReservationFence: d.Reservation.Fence, OperationKey: requests[0].OperationKey, Verification: requests[0], RequiredSet: required}
			if required != nil || len(requests) > 1 {
				staged.Verifications = append([]VerificationRequest(nil), requests...)
			}
			st.Actions[n] = staged
			st.Phase = "verification"
			st.HaltClass = ""
			st.NextAction = "independent runner executes each exact required check operation; original keys survive restart"
			if pending {
				st.HaltClass = "verification_unknown"
				st.NextAction = "observe original pending required command operations; never substitute a new key"
			}
			continue
		}
		if len(evidenceIDs) != len(requests) || len(evidenceIDs) == 0 {
			return ErrDenied
		}
		ev := *s.Operations[requests[0].OperationKey].Evidence
		reviews := []string{}
		for id, review := range s.Reviews {
			if review.Request.AttemptID == at.ID && review.Request.RevisionID == rev.ID && review.IssuerSession == route.ReviewerSession && review.Request.GrantID == route.ReviewGrantID {
				reviews = append(reviews, id)
			}
		}
		sort.Slice(reviews, func(i, j int) bool {
			x, y := s.Reviews[reviews[i]], s.Reviews[reviews[j]]
			if x.CreatedAt.Equal(y.CreatedAt) {
				return x.ID < y.ID
			}
			return x.CreatedAt.Before(y.CreatedAt)
		})
		if len(reviews) == 0 {
			st.Actions[n] = ControllerAction{ID: controllerKey(*st, "review-action", rev.ID), Kind: "independent_review", ActorID: route.ReviewerSession, TaskID: action.TaskID, DispatchID: d.ID, AttemptID: at.ID, LeaseID: rev.LeaseID, RevisionID: rev.ID, Fence: rev.Fence, ReservationFence: d.Reservation.Fence, EvidenceID: ev.ID, EvidenceIDs: evidenceIDs, RequiredSet: required, OperationKey: controllerKey(*st, "review", rev.ID)}
			st.Phase = "review"
			st.HaltClass = ""
			st.NextAction = "independent reviewer proposes against exact trusted evidence"
			continue
		}
		review := s.Reviews[reviews[len(reviews)-1]]
		copy := *a
		if copy.reviewObserver == nil {
			observer, ok := a.admission.(PreflightObserver)
			if !ok {
				return ErrDenied
			}
			spec, found := routingPreflightSpec(s, st.ProjectID, action.TaskID, d.ImplementationRole)
			if !found {
				return ErrDenied
			}
			obs, e := observer.ObservePreflight(s.Binding.Workspace, st.ProjectID, action.TaskID, spec, s.SwarmPolicies[st.ProjectID].Placements[action.TaskID])
			if e != nil {
				return e
			}
			copy.reviewObserver = checkpointReviewObservation{obs.ReviewObservation}
		}
		_, _, reason := copy.reviewCurrent(s, review.IssuerSession, review.Request)
		if reason != ReviewEligible {
			return ErrDenied
		}
		controllerRef(st, CheckpointRefs{TaskID: action.TaskID, DispatchID: d.ID, ReviewID: review.ID})
		if review.Request.Outcome != "approve" {
			if e := a.controllerCorrection(s, iv, n, action, rev.ID, review.ID); e != nil {
				return e
			}
			if st.Terminal {
				return nil
			}
			continue
		}
		handled, he := a.controllerIntegrationProgress(s, iv, n, action, at, rev, review)
		if he != nil {
			return he
		}
		if handled {
			continue
		}
		// A real independent review creates a real integration checkpoint, never a
		// fabricated integration outcome. The producer remains core592.
		ref := CheckpointRefs{TaskID: action.TaskID, DispatchID: d.ID, IntentID: i.ID, EvidenceID: ev.ID, ReviewID: review.ID, IntegrationOperationID: controllerKey(*st, "integration", rev.ID)}
		controllerRef(st, ref)
		requestCP := CheckpointRequest{OperationKey: ref.IntegrationOperationID, CheckpointID: st.CheckpointID, ProjectID: st.ProjectID, InvocationID: controllerCycleID(*st), Phase: "integration", ExpectedSequence: uint64(len(s.Checkpoints[st.CheckpointID])), Refs: st.Refs}
		cp, e := a.writeCheckpointState(s, token, session, requestCP, true)
		if e != nil {
			return e
		}
		st.Checkpoint = &cp
		st.CheckpointSequence = cp.Sequence
		st.Actions[n] = ControllerAction{ID: ref.IntegrationOperationID, Kind: "integration_checkpoint", ActorID: "owner", TaskID: action.TaskID, DispatchID: d.ID, AttemptID: at.ID, LeaseID: rev.LeaseID, RevisionID: rev.ID, Fence: rev.Fence, ReservationFence: d.Reservation.Fence, EvidenceID: ev.ID, EvidenceIDs: evidenceIDs, RequiredSet: required, ReviewID: review.ID, OperationKey: ref.IntegrationOperationID}
		st.Phase = "integration"
		st.HaltClass = "pending_local_integration"
		st.NextAction = "core592 must supply actual authenticated integration producer; retain protection"
		continue
	}
	if allRetired {
		st.Actions = []ControllerAction{}
		st.Phase = "reconcile"
		st.NextAction = "fresh pre-cycle for next bounded wave"
	}
	return nil
}
func (a *Authority) controllerCorrection(s *state, iv *controllerInvocation, n int, old ControllerAction, revision, source string) error {
	st := &iv.Status
	if _, issued := iv.CorrectionSources[source]; issued {
		st.Phase = "correction"
		st.NextAction = "observe corrected revision under original lease; do not issue another round"
		return nil
	}
	if st.Corrections >= iv.Policy.MaxCorrectionRounds {
		controllerHalt(iv, "correction_limit", "owner examines rejected/failing work; correction bound exhausted", false, true)
		return nil
	}
	var route *NativeModelRoutingSelection
	if _, enabled := s.ModelRoutingPolicies[st.ProjectID]; enabled {
		previous := s.Dispatches[old.DispatchID].ImplementationRole
		selection, e := a.selectModelRouting(s, st.ProjectID, old.TaskID, map[string]int{}, st.Corrections+1, previous)
		if e != nil {
			return e
		}
		if selection.RoleID == "" {
			controllerHalt(iv, "model_route_unavailable", "owner must provision a harder capable approved route without resetting correction bounds", false, true)
			return nil
		}
		route = &selection
	}
	st.Corrections++
	iv.CorrectionSources[source] = revision
	old.Kind = "correction"
	old.ID = controllerKey(*st, "correction", source)
	old.OperationKey = old.ID
	old.ActorID = iv.Routes[old.TaskID].WorkerSession
	old.RevisionID = revision
	if route != nil {
		s.ModelRoutingSelections[old.ID] = *route
		// The descriptor is a next-turn request, not permission to borrow the old
		// worker's lease. Existing writers stay protected pending owner recovery.
		// ActorID remains the original protected writer. The separate selected
		// routing record describes a future turn and confers no old-lease rights.
	}
	st.Actions[n] = old
	st.Phase = "correction"
	st.NextAction = "original worker corrects actual failing evidence or independent rejection"
	if route != nil {
		st.NextAction = "next-turn model route recorded; owner must reconcile retained original writer before a new worker dispatch"
	}
	st.HaltClass = ""
	return nil
}

func controllerLifecycleKnown(s *state, i DispatchIntent) bool {
	h := s.LifecycleObservations[i.DispatchID]
	p, ok := s.LifecyclePolicies[i.Reservation.ProjectID]
	if !ok || len(h) == 0 {
		return false
	}
	last := h[len(h)-1]
	if !reflect.DeepEqual(last.Binding, lifecycleBinding(i)) || last.Source != p.Source {
		return false
	}
	if lifecycleTerminal(last.State) {
		return last.State == "completed" || last.State == "failed"
	}
	return time.Now().Before(last.SourceTime.Add(p.Freshness)) && last.State != "unknown"
}

// Failed trusted evidence is suitable only for a correction decision; the
// existing shared successful-evidence gate is deliberately unchanged.
func controllerEvidenceCurrent(s *state, ev Evidence, r VerificationRequest, runner string, op operation) bool {
	at, e := reviewAttempt(s, r.AttemptID)
	if e != nil {
		return false
	}
	g, ok := s.Grants[r.GrantID]
	actor, present := s.Sessions[runner]
	if !ok || !present || actor.Parent != "" || !controllerLive(s, runner) || g.Revoked || !time.Now().Before(g.ExpiresAt) || g.RunnerSession != runner || g.TaskID != at.Request.TaskID || g.Repository != at.Observation.Repository || g.CheckID != r.CheckID || op.Digest != identity("run:"+g.CommandDigest, runner, r) || ev.Provenance != "trusted-local-runner" || ev.IssuerSession != runner || ev.GrantID != g.ID || ev.CheckID != g.CheckID || ev.CommandDigest != g.CommandDigest || ev.Revision.AttemptID != r.AttemptID || ev.Revision.ID != r.RevisionID || ev.Revision.LeaseID != r.LeaseID || ev.Revision.Fence != r.ExpectedFence || at.SessionID != r.WorkerSession || workerTaskWithdrawn(s, at.SessionID, at.Request.TaskID) || !subset([]string{"verification.run"}, actor.Scope.Actions) || !subset([]string{"task:" + at.Request.TaskID}, actor.Scope.Resources) {
		return false
	}
	if !controllerEvidenceActorsCurrent(s, at, r, runner) {
		return false
	}
	rev, e := currentRevision(s, at, r.RevisionID, r.LeaseID, r.ExpectedFence)
	if e != nil || !reflect.DeepEqual(rev, ev.Revision) || observeRevision(at, rev) != nil {
		return false
	}
	return controllerEvidenceActorsCurrent(s, at, r, runner)

}

func controllerPreflightGeneration(s *state, p ControllerPolicy) string {
	receipts := map[string]PreflightReceipt{}
	for id, r := range s.PreflightOperations {
		if r.Report.ProjectID == p.ProjectID && subset([]string{r.Report.TaskID}, p.TaskIDs) {
			receipts[id] = r
		}
	}
	return identity("controller.preflight-retry", "", receipts)
}

func controllerEvidenceActorsCurrent(s *state, at Attempt, r VerificationRequest, runner string) bool {
	l, present := s.Leases[r.LeaseID]
	worker, ok := s.Sessions[at.SessionID]
	g, gok := s.Grants[r.GrantID]
	return present && ok && gok && currentLease(s, l, time.Now()) && l.Fence == r.ExpectedFence && l.AttemptID == at.ID && l.SessionID == at.SessionID && swarmLineageCurrent(s, at.SessionID, time.Now()) && subset([]string{"verification.request"}, worker.Scope.Actions) && !workerTaskWithdrawn(s, at.SessionID, at.Request.TaskID) && controllerLive(s, runner) && !g.Revoked && time.Now().Before(g.ExpiresAt)
}

func controllerProducerProgress(s *state, iv controllerInvocation, canonical string) string {
	revisions := map[string]string{}
	for _, action := range iv.Status.Actions {
		if d, ok := s.Dispatches[action.DispatchID]; ok {
			if h := s.Revisions[d.AttemptID]; len(h) > 0 {
				revisions[d.ID] = h[len(h)-1].ID
			}
		}
	}
	return identity("controller.progress", "", struct {
		Canonical string
		Revisions map[string]string
	}{canonical, revisions})
}

func controllerActionRetired(s *state, d DispatchReservation) bool {
	if _, recovered := s.Recoveries["reservation:"+d.ID]; recovered {
		return true
	}
	i, present := s.DispatchIntents[d.ID]
	return !reservationRetained(s, d) && (!present || i.Lease == nil || s.Leases[i.Lease.ID].Released)
}

// Every emitted action remains current after the last other task's observer.
// This is a cooperative snapshot, not containment of arbitrary filesystem writers.
func controllerFreshActions(s *state, iv controllerInvocation) bool {
	now := time.Now()
	for _, action := range iv.Status.Actions {
		d, ok := s.Dispatches[action.DispatchID]
		if !ok || d.ProjectID != iv.Status.ProjectID || d.Reservation.TaskID != action.TaskID || d.Reservation.Fence != action.ReservationFence {
			return false
		}
		if controllerIntegrationActionHistorical(s, iv, action) {
			continue
		}
		if controllerActionRetired(s, d) {
			continue
		}
		route := iv.Routes[action.TaskID]
		if action.Kind == "dispatch" || action.Kind == "host_dispatch" {
			if !d.Converted && (!now.Before(d.Reservation.ExpiresAt) || !preflightBoundCurrent(s, d) || !budgetDispatchCurrent(s, d) || !dispatchBoundActor(s, route.BridgeSession, d.ProjectID, "dispatch.correlate", d) || !dispatchBoundActor(s, route.WorkerSession, d.ProjectID, "lease.acquire", d)) {
				return false
			}
			continue
		}
		at, e := reviewAttempt(s, action.AttemptID)
		if e != nil || at.SessionID != route.WorkerSession {
			return false
		}
		rev, e := currentRevision(s, at, action.RevisionID, action.LeaseID, action.Fence)
		if e != nil || rev.ID != action.RevisionID {
			return false
		}
		req := VerificationRequest{GrantID: route.VerificationGrantID, LeaseID: action.LeaseID, ExpectedFence: action.Fence}
		if !controllerEvidenceActorsCurrent(s, at, req, route.RunnerSession) {
			return false
		}
		for _, request := range action.Verifications {
			if request.AttemptID != at.ID || request.RevisionID != rev.ID || request.LeaseID != rev.LeaseID || request.ExpectedFence != rev.Fence || !controllerEvidenceActorsCurrent(s, at, request, route.RunnerSession) {
				return false
			}
			g := s.Grants[request.GrantID]
			if g.RunnerSession != route.RunnerSession || g.CheckID != request.CheckID || g.TaskID != action.TaskID || g.Repository != at.Observation.Repository {
				return false
			}
		}
		vg := s.Grants[route.VerificationGrantID]
		if vg.RunnerSession != route.RunnerSession || vg.TaskID != action.TaskID || vg.Repository != at.Observation.Repository || vg.CheckID != route.CheckID {
			return false
		}
		if action.Kind == "independent_review" || action.Kind == "integration_checkpoint" || action.Kind == "correction" {
			rg, ok := s.ReviewGrants[route.ReviewGrantID]
			if !ok || !reviewGrantCurrent(s, route.ReviewerSession, at, rg) {
				return false
			}
		}
	}
	return true
}

// Each check operation is derived from the immutable invocation/revision/check,
// never supplied by a caller choosing a convenient successful subset.
func (a *Authority) controllerRequiredVerification(s *state, iv controllerInvocation, at Attempt, rev AttemptRevision, route ControllerTaskRoute) (*RequiredVerificationSet, []VerificationRequest, error) {
	checks := []ControllerCheckRoute{{CheckID: route.CheckID, GrantID: route.VerificationGrantID}}
	var required *RequiredVerificationSet
	if observer, ok := a.admission.(PreflightObserver); ok {
		d := s.Dispatches[iv.Status.Actions[0].DispatchID]
		// Resolve this task's actual selected role rather than another width member.
		for _, action := range iv.Status.Actions {
			if action.TaskID == at.Request.TaskID {
				d = s.Dispatches[action.DispatchID]
			}
		}
		spec, found := routingPreflightSpec(s, iv.Status.ProjectID, at.Request.TaskID, d.ImplementationRole)
		if !found {
			return nil, nil, ErrDenied
		}
		obs, e := observer.ObservePreflight(s.Binding.Workspace, iv.Status.ProjectID, at.Request.TaskID, spec, s.SwarmPolicies[iv.Status.ProjectID].Placements[at.Request.TaskID])
		if e != nil {
			return nil, nil, e
		}
		required = obs.RequiredSet
		available, grants := obs.Checks, obs.VerificationGrantIDs
		if required != nil {
			available, grants = obs.AvailableChecks, obs.AvailableGrantIDs
		}
		if len(available) > 1 || required != nil {
			if len(route.Checks) != len(available) {
				return nil, nil, ErrDenied
			}
			for i, c := range available {
				found := false
				for _, r := range route.Checks {
					if r.CheckID == c.ID && i < len(grants) && r.GrantID == grants[i] {
						found = true
					}
				}
				if !found {
					return nil, nil, ErrDenied
				}
			}
		}
		if required != nil {
			if required.RevisionCommit != rev.Observation.Commit || required.RevisionTree != rev.Observation.Tree || len(required.Checks) == 0 || len(required.Checks) != len(required.GrantIDs) {
				return nil, nil, ErrDenied
			}
			requiredCopy := *required
			requiredCopy.RevisionID = rev.ID
			required = &requiredCopy
			checks = nil
			for i, c := range required.Checks {
				checks = append(checks, ControllerCheckRoute{CheckID: c.ID, GrantID: required.GrantIDs[i]})
			}
		} else if len(route.Checks) > 0 {
			checks = route.Checks
		}
	}
	requests := []VerificationRequest{}
	for _, check := range checks {
		grant, ok := s.Grants[check.GrantID]
		if !ok || grant.TaskID != at.Request.TaskID || grant.Repository != at.Observation.Repository || grant.RunnerSession != route.RunnerSession || grant.CheckID != check.CheckID {
			return nil, nil, ErrDenied
		}
		if required != nil {
			matched := false
			for _, c := range required.Checks {
				if c.ID == check.CheckID && c.CommandDigest == grant.CommandDigest {
					matched = true
				}
			}
			if !matched {
				return nil, nil, ErrDenied
			}
		}
		key := controllerKey(iv.Status, "verification", rev.ID)
		if required != nil || len(checks) > 1 {
			key = controllerKey(iv.Status, "verification", rev.ID+":"+check.CheckID)
		}
		requests = append(requests, VerificationRequest{OperationKey: key, WorkerSession: at.SessionID, AttemptID: at.ID, LeaseID: rev.LeaseID, RevisionID: rev.ID, CheckID: check.CheckID, GrantID: check.GrantID, ExpectedFence: rev.Fence})
	}
	if len(requests) == 0 {
		return nil, nil, ErrDenied
	}
	return required, requests, nil
}
