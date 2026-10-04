package sessions

import (
	"errors"
	"sort"
	"time"
)

// References carry identities only. They never confer producer credentials,
// host permission, current review eligibility, acceptance or release authority.
func (a *Authority) validateCheckpointRef(s *state, p CheckpointPolicy, r CheckpointRefs) error {
	for _, id := range []string{r.TaskID, r.DispatchID, r.IntentID, r.VerificationKey, r.EvidenceID, r.ReviewID, r.IntegrationOperationID, r.AcceptanceOperationID, r.RunnerSession} {
		if id != "" && !validID(id) {
			return ErrDenied
		}
	}
	if r.DispatchID != "" {
		d, ok := s.Dispatches[r.DispatchID]
		if !ok || d.ProjectID != p.ProjectID || d.Reservation.TaskID != r.TaskID {
			return ErrDenied
		}
	}
	if r.IntentID != "" {
		i, ok := s.DispatchIntents[r.DispatchID]
		if !ok || i.ID != r.IntentID || i.DispatchID != r.DispatchID || i.Reservation.ProjectID != p.ProjectID || i.Reservation.Reservation.TaskID != r.TaskID {
			return ErrDenied
		}
	}
	if r.EvidenceID != "" {
		ev, ok := s.Evidence[r.EvidenceID]
		at, atErr := reviewAttempt(s, ev.Revision.AttemptID)
		present := atErr == nil
		if !ok || !present || at.ProjectID != p.ProjectID || at.Request.TaskID != r.TaskID {
			return ErrDenied
		}
	}
	if r.ReviewID != "" {
		rev, ok := s.Reviews[r.ReviewID]
		at, atErr := reviewAttempt(s, rev.Request.AttemptID)
		present := atErr == nil
		if !ok || !present || at.ProjectID != p.ProjectID || at.Request.TaskID != r.TaskID {
			return ErrDenied
		}
	}
	if r.VerificationKey != "" {
		v := r.Verification
		at, atErr := reviewAttempt(s, v.AttemptID)
		ok := atErr == nil
		g, present := s.Grants[v.GrantID]
		if !ok || !present || at.ProjectID != p.ProjectID || at.Request.TaskID != r.TaskID || v.WorkerSession != at.SessionID || v.OperationKey != r.VerificationKey || g.TaskID != r.TaskID || g.RunnerSession != r.RunnerSession || g.CheckID != v.CheckID {
			return ErrDenied
		}
		if op, ok := s.Operations[r.VerificationKey]; ok && op.Digest != identity("run:"+g.CommandDigest, r.RunnerSession, v) {
			return ErrDenied
		}
	}
	return nil
}
func (a *Authority) reconcileCheckpoint(s *state, p CheckpointPolicy, out CheckpointStatus, token, session string) (CheckpointStatus, error) {
	observer, ok := a.admission.(CheckpointObserver)
	if !ok {
		return out, ErrDenied
	}
	obs, e := observer.ObserveCheckpoint(s.Binding.Workspace, p.ProjectID, p.TaskIDs)
	if e != nil {
		obs = CheckpointObservation{Digest: identity("checkpoint.observation-unavailable", "", p.ProjectID), Findings: []CheckpointFinding{{Class: "transient_infrastructure", Knowledge: "unknown", Reference: p.ProjectID, NextAction: "restore trusted read-only checkpoint observations", Retryable: true}}}
	}
	out.ObservedAt = time.Now().UTC()
	out.ObservedTrunk = obs.Trunk
	out.ObservedTrunkRef = obs.TrunkRef
	out.Diagnostics = []CheckpointFinding{}
	out.PreflightReports = []PreflightReport{}
	out.GateExpiresAt = out.ObservedAt.Add(30 * time.Second)
	out.Findings = append([]CheckpointFinding{}, obs.Findings...)
	out.Facts = []CheckpointFact{}
	add := func(class, knowledge, task, ref, action string, retry bool) {
		out.Findings = append(out.Findings, CheckpointFinding{Class: class, Knowledge: knowledge, TaskID: task, Reference: ref, NextAction: action, Retryable: retry})
	}
	swarmObserver, available := a.admission.(SwarmAdmission)
	if !available {
		return out, ErrDenied
	}
	snapshot, e := swarmObserver.ObserveSwarm(s.Binding.Workspace, p.ProjectID)
	if e != nil {
		add("inconsistent_task_record", "unknown", "", p.ProjectID, "repair canonical duplicate, malformed, missing or inconsistent records", false)
	}
	candidates := map[string]SwarmCandidate{}
	for _, c := range snapshot.Candidates {
		candidates[c.TaskID] = c
	}
	// Every configured task is examined, including durable producers omitted by a
	// caller. Missing refs cannot conceal an outstanding reservation or host action.
	progress := []string{obs.Trunk}
	diagnosticForLease := func(lease, task, class, knowledge, ref, action string, retry bool) {
		if decision, ok := checkpointRetiredLease(s, lease); ok {
			out.Diagnostics = append(out.Diagnostics, CheckpointFinding{Class: class, Knowledge: knowledge, TaskID: task, Reference: ref, NextAction: "historical outcome retained after explicit owner recovery " + decision.ID + "; never reexecute the original operation"})
			progress = append(progress, decision.ID)
		} else {
			add(class, knowledge, task, ref, action, retry)
		}
	}

	ids := []string{}
	for id, d := range s.Dispatches {
		if d.ProjectID == p.ProjectID && checkpointTask(p, d.Reservation.TaskID) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		d := s.Dispatches[id]
		task := d.Reservation.TaskID
		retained := reservationRetained(s, d)
		var intent *DispatchIntent
		for _, i := range s.DispatchIntents {
			if i.DispatchID == id {
				copy := i
				intent = &copy
				break
			}
		}
		if intent != nil && intent.Lease != nil {
			l, present := s.Leases[intent.Lease.ID]
			retained = present && !l.Released
		}
		lease := d.Reservation
		if intent != nil && intent.Lease != nil {
			lease = s.Leases[intent.Lease.ID]
		}
		out.Facts = append(out.Facts, CheckpointFact{Kind: "dispatch", TaskID: task, ID: id, State: "reserved", Knowledge: "known", LeaseID: lease.ID, Fence: lease.Fence})
		if retained {
			add("retained_resources", "known", task, id, "owner reconciles exact reservation/lease/fence through schema10 recovery; checkpoint never releases it", false)
		}
		if intent == nil {
			if retained {
				add("dispatch_intent_pending", "pending", task, id, "observe original dispatch operation; never create a replacement key for uncertain work", false)
			}
			continue
		}
		progress = append(progress, identity("checkpoint.intent", "", []string{intent.ID, intent.State, intent.HostAgentID}))
		out.Facts = append(out.Facts, CheckpointFact{Kind: "intent", TaskID: task, ID: intent.ID, State: intent.State, Knowledge: "known", HostAgentID: intent.HostAgentID, LeaseID: lease.ID, Fence: lease.Fence})
		if decision, retired := checkpointRetiredDispatch(s, d, lease); retired {
			out.Facts = append(out.Facts, CheckpointFact{Kind: "owner-recovery", TaskID: task, ID: decision.ID, State: decision.Mode, Knowledge: "known", LeaseID: lease.ID, Fence: decision.Target.ExpectedFence})
			progress = append(progress, decision.ID)
			out.Diagnostics = append(out.Diagnostics, CheckpointFinding{Class: "retired_native_execution", Knowledge: "unknown", TaskID: task, Reference: intent.ID, NextAction: "owner accepted recorded uncertainty/release risk; old execution is not proven successful or terminated"})
			continue
		}
		if intent.State != "acknowledged" {
			add("unknown_dispatch_ack", "unknown", task, intent.ID, "reconcile original intent and host correlation without another launch", false)
		}
		observations := s.LifecycleObservations[id]
		if len(observations) == 0 {
			add("stale_host_observation", "unknown", task, intent.ID, "bridge must supply a fresh observation of the original host execution", true)
		} else {
			last := observations[len(observations)-1]
			policy := s.LifecyclePolicies[p.ProjectID]
			until := last.SourceTime.Add(policy.Freshness)
			progress = append(progress, identity("checkpoint.host", "", []string{intent.ID, last.State}))
			freshness := "fresh"
			if !out.ObservedAt.Before(until) {
				freshness = "stale"
			}
			out.Facts = append(out.Facts, CheckpointFact{Kind: "host", TaskID: task, ID: id, State: last.State, Knowledge: "known", HostAgentID: intent.HostAgentID, Freshness: freshness})
			if lifecycleTerminal(last.State) {
				if retained {
					add("finished_dispatch_reconciliation", "known", task, id, "terminal host observation does not prove all writers stopped; obtain explicit owner recovery decision", false)
				}
			} else if !out.ObservedAt.Before(until) {
				add("stale_host_observation", "unknown", task, id, "observe original host execution; staleness never proves success", true)
			} else {
				add("host_execution_pending", "pending", task, id, "wait for the original host outcome; do not redispatch", true)
			}
		}
	}
	leaseIDs := []string{}
	for id, l := range s.Leases {
		if !l.Released && checkpointTask(p, l.TaskID) {
			leaseIDs = append(leaseIDs, id)
		}
	}
	sort.Strings(leaseIDs)
	for _, id := range leaseIDs {
		l := s.Leases[id]
		already := false
		for _, f := range out.Facts {
			if f.Kind == "dispatch" && f.LeaseID == id {
				already = true
			}
		}
		if already {
			continue
		}
		out.Facts = append(out.Facts, CheckpointFact{Kind: "lease", TaskID: l.TaskID, ID: id, LeaseID: id, Fence: l.Fence, State: "retained", Knowledge: "known"})
		add("retained_resources", "known", l.TaskID, id, "owner must reconcile this exact lease/fence through schema10 recovery; expiry does not release claims", false)
	}
	for _, task := range p.TaskIDs {
		c, present := candidates[task]
		if !present {
			add("inconsistent_task_record", "unknown", task, task, "restore the unique canonical task record", false)
			continue
		}
		if c.Status == "done" || c.Status == "cancelled" {
			continue
		}
		if c.Reason != "ready" {
			out.Facts = append(out.Facts, CheckpointFact{Kind: "scheduling", TaskID: task, ID: task, State: c.Reason, Knowledge: "known"})
		}
	}
	refs := append([]CheckpointRefs{}, out.Refs...)
	hasRef := func(kind, id string) bool {
		for _, r := range refs {
			if kind == "verification" && r.VerificationKey == id || kind == "review" && r.ReviewID == id || kind == "evidence" && r.EvidenceID == id {
				return true
			}
		}
		return false
	}
	operationIDs := []string{}
	for id := range s.Operations {
		operationIDs = append(operationIDs, id)
	}
	sort.Strings(operationIDs)
	for _, id := range operationIDs {
		op := s.Operations[id]
		if op.Pending {
			b := op.CheckpointVerification
			if b == nil {
				if !hasRef("verification", id) {
					add("verification_binding_unknown", "unknown", "", p.ProjectID, "owner must bind the original legacy pending verification digest to its exact scoped request; never reexecute", false)
				}
				continue
			}
			if b.ProjectID == p.ProjectID && checkpointTask(p, b.TaskID) && !hasRef("verification", id) {
				refs = append(refs, CheckpointRefs{TaskID: b.TaskID, VerificationKey: id, Verification: b.Request, RunnerSession: b.RunnerSession})
			}
		}
		if op.Evidence != nil {
			ev := *op.Evidence
			at, err := reviewAttempt(s, ev.Revision.AttemptID)
			if err == nil && at.ProjectID == p.ProjectID && checkpointTask(p, at.Request.TaskID) && ev.Provenance == "trusted-local-runner" && !hasRef("evidence", ev.ID) {
				refs = append(refs, CheckpointRefs{TaskID: at.Request.TaskID, EvidenceID: ev.ID})
			}
		}
	}
	reviewIDs := []string{}
	for id := range s.Reviews {
		reviewIDs = append(reviewIDs, id)
	}
	sort.Strings(reviewIDs)
	for _, id := range reviewIDs {
		rev := s.Reviews[id]
		at, err := reviewAttempt(s, rev.Request.AttemptID)
		if err == nil && at.ProjectID == p.ProjectID && checkpointTask(p, at.Request.TaskID) && !hasRef("review", id) {
			refs = append(refs, CheckpointRefs{TaskID: at.Request.TaskID, ReviewID: id})
		}
	}
	for _, r := range refs {
		if r.VerificationKey != "" {
			op, present := s.Operations[r.VerificationKey]
			switch {
			case !present:
				add("verification_unknown", "unknown", r.TaskID, r.VerificationKey, "observe original verification operation; absence is not completion", false)
			case op.Pending:
				diagnosticForLease(r.Verification.LeaseID, r.TaskID, "verification_pending", "pending", r.VerificationKey, "reserved outcome unknown; owner reconciliation, never reexecute", false)
			case op.Evidence == nil:
				add("verification_unknown", "unknown", r.TaskID, r.VerificationKey, "no authenticated result exists", false)
			case !checkpointEvidenceCurrent(s, *op.Evidence):
				diagnosticForLease(op.Evidence.Revision.LeaseID, r.TaskID, "verification_ineligible", "known", op.Evidence.ID, "require actual configured runner evidence", false)
			case op.Evidence.ExitCode != 0:
				add("verification_failed", "known", r.TaskID, op.Evidence.ID, "repair the observed check failure", false)
			default:
				progress = append(progress, op.Evidence.ID)
			}
		}
		if r.EvidenceID != "" {
			ev := s.Evidence[r.EvidenceID]
			out.Facts = append(out.Facts, CheckpointFact{Kind: "evidence", TaskID: r.TaskID, ID: ev.ID, State: ev.Provenance, Knowledge: "known", LeaseID: ev.Revision.LeaseID, Fence: ev.Revision.Fence})
			if !checkpointEvidenceCurrent(s, ev) {
				diagnosticForLease(ev.Revision.LeaseID, r.TaskID, "verification_ineligible", "known", r.EvidenceID, "require successful configured runner evidence", false)
			} else {
				progress = append(progress, ev.ID)
			}
		}
		if r.ReviewID != "" {
			rev := s.Reviews[r.ReviewID]
			copy := *a
			// Reuse the owner-selected preflight observer, never the controller's claim
			// about review policy. Missing trusted observer is explicitly ineligible.
			if o, available := a.admission.(PreflightObserver); available && copy.reviewObserver == nil {
				spec := s.PreflightPolicies[p.ProjectID].Tasks[r.TaskID]
				placement := s.SwarmPolicies[p.ProjectID].Placements[r.TaskID]
				observed, err := o.ObservePreflight(s.Binding.Workspace, p.ProjectID, r.TaskID, spec, placement)
				if err == nil {
					copy.reviewObserver = checkpointReviewObservation{observed.ReviewObservation}
				}
			}
			_, _, reason := copy.reviewCurrent(s, rev.IssuerSession, rev.Request)
			at, atErr := reviewAttempt(s, rev.Request.AttemptID)
			if atErr != nil || workerTaskWithdrawn(s, at.SessionID, r.TaskID) {
				reason = ReviewWorkerInvalid
			}
			out.Facts = append(out.Facts, CheckpointFact{Kind: "review", TaskID: r.TaskID, ID: rev.ID, State: string(reason), Knowledge: "known", LeaseID: rev.Request.LeaseID, Fence: rev.Request.ExpectedFence})
			if reason != ReviewEligible || rev.Request.Outcome != "approve" {
				diagnosticForLease(rev.Request.LeaseID, r.TaskID, "review_ineligible", "known", r.ReviewID, "fresh independent reviewer authority, policy, tree and evidence must be eligible", false)
			} else {
				progress = append(progress, rev.ID)
			}
		}
		if r.IntegrationOperationID != "" {
			add("pending_local_integration", "unknown", r.TaskID, r.IntegrationOperationID, "core592 must supply authenticated local integration producer outcome; contract reference is not execution", false)
		}
		if r.AcceptanceOperationID != "" {
			add("pending_canonical_acceptance", "unknown", r.TaskID, r.AcceptanceOperationID, "core594 must supply authenticated canonical acceptance outcome", false)
		}
	}
	planID := ""
	if !s.SwarmPolicies[p.ProjectID].Stopped {
		plan, err := a.planSwarm(s, token, session, p.ProjectID, "swarm.plan")
		if err != nil {
			if errors.Is(err, ErrPreflightTransient) {
				add("transient_preflight", "unknown", "", p.ProjectID, "use bounded CheckPreflight retries with the original policy and durable retry budget", true)
			} else {
				add("policy_refusal", "known", "", p.ProjectID, "fresh shared scheduler/preflight refuses scheduling", false)
			}
		} else {
			planID = plan.ID
			selected := false
			resourceConflict := false
			transientPreflight, permanentPreflight := false, false
			budgetBlocked := false
			for _, v := range plan.Verdicts {
				if !checkpointTask(p, v.TaskID) {
					continue
				}
				if v.Preflight != nil {
					out.PreflightReports = append(out.PreflightReports, *v.Preflight)
					if v.Preflight.FailureClass != "" {
						transient := v.Preflight.FailureClass == "transient"
						transientPreflight = transientPreflight || transient
						permanentPreflight = permanentPreflight || !transient
						out.Diagnostics = append(out.Diagnostics, CheckpointFinding{Class: "candidate_preflight", Knowledge: "known", TaskID: v.TaskID, Reference: v.TaskID, Retryable: transient, NextAction: "inspect this excluded candidate's typed preflight report; healthy selected backfill remains eligible"})
					}
				}
				if v.Budget != nil && !v.Budget.Allowed {
					budgetBlocked = true
				}
				if v.Selected {
					selected = true
				}
				if v.Reason == "resource_conflict" || v.Reason == "checkout_conflict" {
					resourceConflict = true
					out.Facts = append(out.Facts, CheckpointFact{Kind: "scheduling", TaskID: v.TaskID, ID: v.TaskID, State: v.Reason, Knowledge: "known"})
				}
			}
			if !selected && len(out.Findings) == 0 {
				if budgetBlocked {
					add("budget_admission", "known", "", p.ProjectID, "owner-estimated complete-cycle headroom exhausted, cooldown active, or model estimate binding unavailable; provider enforcement remains unsupported", false)
				} else if permanentPreflight {
					add("policy_refusal", "known", "", p.ProjectID, "owner must repair permanent selected-candidate preflight failures before a bounded retry", false)
				} else if resourceConflict {
					add("resource_conflict", "known", "", p.ProjectID, "preserve occupied protections; no safe task can be selected", false)
				} else if transientPreflight {
					add("transient_preflight", "unknown", "", p.ProjectID, "use bounded CheckPreflight retries with the original policy and durable retry budget", true)
				} else {
					add("no_schedulable_work", "known", "", p.ProjectID, "wait for a genuine canonical readiness or resource change", true)
				}
			}
		}
	}
	// All authority mutations, not only fence increments, invalidate a gate.
	// The hash is opaque; no credentials, commands or private paths are returned.
	source := *s
	source.Checkpoints = nil
	source.CheckpointOperations = nil
	for _, c := range snapshot.Candidates {
		if checkpointTask(p, c.TaskID) {
			progress = append(progress, identity("checkpoint.task-condition", "", []string{c.TaskID, c.Status, c.Reason}))
		}
	}
	for _, f := range out.Findings {
		if f.Class == "budget_admission" {
			continue
		}
		progress = append(progress, identity("checkpoint.blocker", "", []string{f.Class, f.Knowledge, f.TaskID}))
	}
	uniqueProgress := map[string]bool{}
	for _, fact := range progress {
		uniqueProgress[fact] = true
	}
	progress = nil
	for fact := range uniqueProgress {
		progress = append(progress, fact)
	}
	sort.Strings(progress)
	out.ProgressDigest = identity("checkpoint.progress", "", progress)
	out.ConditionDigest = identity("checkpoint.condition", "", struct {
		Authority state
		Canonical string
		Trunk     string
		Findings  []CheckpointFinding
	}{source, identity("checkpoint.observed-plan", "", []string{obs.Digest, planID}), obs.Trunk, out.Findings})
	return out, nil
}

type checkpointReviewObservation struct{ observation ReviewObservation }

func (o checkpointReviewObservation) ObserveReview(string, string, string) (ReviewObservation, error) {
	return o.observation, nil
}

// This read predicate intentionally needs no producer bearer credential. It
// validates the persisted actor lineage and exact current lease/grant/revision;
// the caller has already authenticated separate project/task read authority.
func checkpointEvidenceCurrent(s *state, ev Evidence) bool {
	at, atErr := reviewAttempt(s, ev.Revision.AttemptID)
	ok := atErr == nil
	l, present := s.Leases[ev.Revision.LeaseID]
	w, workerPresent := s.Sessions[at.SessionID]
	if !ok || !present || !workerPresent || !currentLease(s, l, time.Now()) || l.Fence != ev.Revision.Fence || l.AttemptID != at.ID || workerTaskWithdrawn(s, at.SessionID, at.Request.TaskID) || !swarmLineageCurrent(s, at.SessionID, time.Now()) || !subset([]string{"verification.request"}, w.Scope.Actions) || ev.Provenance != "trusted-local-runner" || ev.ExitCode != 0 {
		return false
	}
	if _, e := currentRevision(s, at, ev.Revision.ID, l.ID, l.Fence); e != nil {
		return false
	}
	g, ok := s.Grants[ev.GrantID]
	r, present := s.Sessions[ev.IssuerSession]
	if !ok || !present || g.Revoked || r.Parent != "" || !swarmLineageCurrent(s, r.ID, time.Now()) || g.RunnerSession != r.ID || g.TaskID != at.Request.TaskID || g.Repository != at.Observation.Repository || g.CheckID != ev.CheckID || g.CommandDigest != ev.CommandDigest || !subset([]string{"verification.run"}, r.Scope.Actions) || !subset([]string{"task:" + g.TaskID}, r.Scope.Resources) || !time.Now().Before(g.ExpiresAt) {
		return false
	}
	return observeRevision(at, ev.Revision) == nil
}

func checkpointRetiredLease(s *state, id string) (RecoveryReceipt, bool) {
	r, ok := s.Recoveries["lease:"+id]
	return r, ok && r.Target.Kind == "lease" && r.Target.ProtectionID == id
}
func checkpointRetiredDispatch(s *state, d DispatchReservation, l Lease) (RecoveryReceipt, bool) {
	if r, ok := s.Recoveries["reservation:"+d.ID]; ok && r.Target.ProtectionID == d.ID {
		return r, true
	}
	return checkpointRetiredLease(s, l.ID)
}
