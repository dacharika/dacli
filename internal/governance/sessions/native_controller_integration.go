package sessions

import "reflect"

// Native acceptance candidates are deliberately operation-independent. Bind the
// record's exact operation and digest before keying a copy for comparison; never
// rewrite the stored candidate or treat a different key as equivalent (core639).
func acceptanceRecordMatchesInput(record NativeAcceptanceRecord, input NativeAcceptanceInput) bool {
	if !validID(input.OperationKey) || record.OperationKey != input.OperationKey || record.InputDigest != identity("native-acceptance-operation", "", input) {
		return false
	}
	candidate := record.Candidate.Input
	if candidate.OperationKey != "" && candidate.OperationKey != input.OperationKey {
		return false
	}
	candidate.OperationKey = input.OperationKey
	return reflect.DeepEqual(candidate, input)
}

// controllerIntegrationProgress consumes recorded owner effects only. It never
// invokes Git, closes a task, releases resources or restores a retired worker.
// Current producer checks apply before owner effects; exact closed history can
// advance bookkeeping after task retirement, only with fresh canonical DONE.
func (a *Authority) controllerIntegrationProgress(s *state, iv *controllerInvocation, n int, action ControllerAction, at Attempt, rev AttemptRevision, review Review) (bool, error) {
	if s.Version < 18 {
		return false, nil
	}
	st := &iv.Status
	operation := ""
	reviewID := action.ReviewID
	if reviewID == "" {
		reviewID = review.ID
	}
	for _, ref := range st.Refs {
		if ref.TaskID == action.TaskID && (ref.DispatchID == "" || ref.DispatchID == action.DispatchID) && (ref.ReviewID == "" || reviewID == "" || ref.ReviewID == reviewID) && ref.IntegrationOperationID != "" {
			if operation != "" && operation != ref.IntegrationOperationID {
				return true, ErrDenied
			}
			operation = ref.IntegrationOperationID
		}
	}
	if operation == "" && action.Kind == "integration_checkpoint" {
		operation = action.OperationKey
	}
	if operation == "" {
		return false, nil
	}
	rec, exists := s.NativeIntegrationOperations[operation]
	if !exists {
		if action.Kind != "integration_checkpoint" && action.Kind != "canonical_acceptance" {
			return false, nil
		}
		st.Phase = "integration"
		st.HaltClass = "pending_local_integration"
		st.NextAction = "owner applies exact592 integration under pinned target epoch; retain protection"
		return true, nil
	}
	input := rec.Input.Candidate
	historicalReview, ok := s.Reviews[input.ReviewID]
	historicalAttempt, e := reviewAttempt(s, input.AttemptID)
	if !ok || e != nil || input.ProjectID != st.ProjectID || input.TaskID != action.TaskID || historicalAttempt.ID != action.AttemptID || historicalAttempt.Request.TaskID != action.TaskID || historicalAttempt.ProjectID != st.ProjectID || input.AttemptID != action.AttemptID || input.LeaseID != action.LeaseID || input.RevisionID != action.RevisionID || input.ExpectedFence != action.Fence || historicalReview.Request.AttemptID != input.AttemptID || historicalReview.Request.LeaseID != input.LeaseID || historicalReview.Request.RevisionID != input.RevisionID || historicalReview.Request.ExpectedFence != input.ExpectedFence || historicalReview.Request.Outcome != "approve" || historicalReview.Revision.ID != input.RevisionID || historicalReview.Revision.Fence != input.ExpectedFence || historicalReview.Revision.Observation.Commit != input.Commit || historicalReview.Revision.Observation.Tree != input.Tree || historicalReview.Revision.Observation.Repository != input.Repository || historicalReview.Observation.ChecklistDigest != input.ChecklistDigest || historicalReview.Observation.PolicyRevision != input.PolicyRevision || rec.OperationKey != operation || rec.Input.OperationKey != operation {
		return true, ErrDenied
	}
	if review.ID != "" && !reflect.DeepEqual(review, historicalReview) {
		return true, ErrDenied
	}
	if at.ID != "" && !reflect.DeepEqual(at, historicalAttempt) {
		return true, ErrDenied
	}
	if rev.ID != "" && !reflect.DeepEqual(rev, historicalReview.Revision) {
		return true, ErrDenied
	}
	if action.ReviewID != "" && action.ReviewID != input.ReviewID {
		return true, ErrDenied
	}
	if rec.State != "integrated" {
		st.Phase = "integration"
		st.HaltClass = "pending_local_integration"
		st.NextAction = "owner freshly reconciles exact durable592 intent; unknown Git effects cannot rerun and resources remain retained"
		return true, nil
	}
	if !validNativeIntegrationOutcome(rec) || !reflect.DeepEqual(s.NativeIntegrationHistory[rec.Policy.TaskID+":"+rec.Policy.Revision], rec.Policy) {
		return true, ErrDenied
	}
	// External delivery remains optional unless separately required by owner.
	// The fresh diagnostic gate advances no counters and never substitutes the
	// existing integration and canonical acceptance receipts below.
	if class, next, ready := nativeExternalGate(s, action.TaskID, input); !ready {
		st.Phase = "integration"
		st.HaltClass = class
		st.NextAction = next
		return true, nil
	}
	acceptanceKey := input.OperationKey
	if !validID(acceptanceKey) || acceptanceKey == operation {
		return true, ErrDenied
	}
	controllerRef(st, CheckpointRefs{TaskID: action.TaskID, DispatchID: action.DispatchID, ReviewID: input.ReviewID, IntegrationOperationID: operation, AcceptanceOperationID: acceptanceKey})
	acceptance, closed := s.NativeAcceptanceOperations[acceptanceKey]
	if !closed || acceptance.State != "closed" {
		if closed && !acceptanceRecordMatchesInput(acceptance, input) {
			return true, ErrDenied
		}
		next := action
		next.ID = acceptanceKey
		next.OperationKey = acceptanceKey
		next.Kind = "canonical_acceptance"
		next.ActorID = "owner"
		next.ReviewID = input.ReviewID
		st.Actions[n] = next
		st.Phase = "acceptance"
		st.HaltClass = "pending_canonical_acceptance"
		st.NextAction = "separately scoped owner evaluates/approves/consumes578 policy and applies579 canonical outcome; integration is not task acceptance"
		return true, nil
	}
	terminal, present := s.TaskTerminals[action.TaskID]
	outcome, observed := s.NativePolicyOutcomes[acceptanceKey]
	decision, consumed := nativePolicyForOperation(s, acceptanceKey)
	if !acceptanceRecordMatchesInput(acceptance, input) || !present || !reflect.DeepEqual(terminal, acceptance) || acceptance.ClosedAt.IsZero() || !validControllerCanonicalReceipt(acceptance) || !validDigest(acceptance.CanonicalOutcomeDigest) || !observed || outcome.OperationKey != acceptanceKey || outcome.State != "closed" || outcome.ExpectedOutcomeDigest != acceptance.CanonicalOutcomeDigest || outcome.ObservedOutcomeDigest != acceptance.CanonicalOutcomeDigest || outcome.ObservedAt.IsZero() || !consumed || outcome.DecisionID != decision.ID || !reflect.DeepEqual(decision.Input, input) || decision.Prepared.CanonicalOutcomeDigest != acceptance.CanonicalOutcomeDigest {
		return true, ErrDenied
	}
	observer, available := a.admission.(SwarmAdmission)
	if !available {
		return true, ErrDenied
	}
	canonical, e := observer.ObserveSwarm(s.Binding.Workspace, st.ProjectID)
	if e != nil {
		return true, e
	}
	found := false
	for _, candidate := range canonical.Candidates {
		if candidate.TaskID == action.TaskID {
			if found || candidate.ProjectID != st.ProjectID || candidate.Status != "done" {
				return true, ErrDenied
			}
			found = true
		}
	}
	if !found {
		return true, ErrDenied
	}
	// Reading canonical DONE is a historical receipt check, not a fresh producer
	// grant. No live lease, Git effect, acceptance or release is manufactured here.
	next := action
	next.ID = acceptanceKey
	next.OperationKey = acceptanceKey
	next.Kind = "accepted"
	next.ActorID = "owner"
	next.ReviewID = input.ReviewID
	st.Actions[n] = next
	st.Phase = "reconcile"
	st.HaltClass = ""
	st.NextAction = "exact owner integration and canonical acceptance observed; retain resources until explicit release/recovery"
	return true, nil
}

// controllerIntegrationActionHistorical exempts only already-consumed owner
// bookkeeping from live worker predicates. Pending unknown integration never
// qualifies; canonical closing only permits observation of its original marker.
func controllerIntegrationActionHistorical(s *state, iv controllerInvocation, action ControllerAction) bool {
	if s.Version < 18 || (action.Kind != "accepted" && action.Kind != "canonical_acceptance") {
		return false
	}
	var rec NativeIntegrationRecord
	found := false
	for _, ref := range iv.Status.Refs {
		if ref.TaskID != action.TaskID || ref.DispatchID != action.DispatchID || ref.ReviewID != action.ReviewID || ref.IntegrationOperationID == "" {
			continue
		}
		candidate, ok := s.NativeIntegrationOperations[ref.IntegrationOperationID]
		if !ok || !validNativeIntegrationOutcome(candidate) {
			return false
		}
		if found && rec.OperationKey != candidate.OperationKey {
			return false
		}
		rec = candidate
		found = true
	}
	if !found {
		return false
	}
	input := rec.Input.Candidate
	if input.ProjectID != iv.Status.ProjectID || input.TaskID != action.TaskID || input.ReviewID != action.ReviewID || input.AttemptID != action.AttemptID || input.LeaseID != action.LeaseID || input.RevisionID != action.RevisionID || input.ExpectedFence != action.Fence || input.OperationKey != action.OperationKey || input.OperationKey == rec.OperationKey {
		return false
	}
	if !reflect.DeepEqual(s.NativeIntegrationHistory[rec.Policy.TaskID+":"+rec.Policy.Revision], rec.Policy) {
		return false
	}
	acceptance, ok := s.NativeAcceptanceOperations[input.OperationKey]
	terminal, present := s.TaskTerminals[input.TaskID]
	decision, consumed := nativePolicyForOperation(s, input.OperationKey)
	if !ok || !present || !acceptanceRecordMatchesInput(acceptance, input) || !reflect.DeepEqual(terminal, acceptance) || !validControllerCanonicalReceipt(acceptance) || !validDigest(acceptance.CanonicalOutcomeDigest) || !consumed || !reflect.DeepEqual(decision.Input, input) || decision.Prepared.CanonicalOutcomeDigest != acceptance.CanonicalOutcomeDigest {
		return false
	}
	if action.Kind == "canonical_acceptance" && acceptance.State == "closing" {
		return true
	}
	if acceptance.State != "closed" || acceptance.ClosedAt.IsZero() {
		return false
	}
	outcome, observed := s.NativePolicyOutcomes[input.OperationKey]
	return observed && outcome.OperationKey == input.OperationKey && outcome.DecisionID == decision.ID && outcome.State == "closed" && outcome.ExpectedOutcomeDigest == acceptance.CanonicalOutcomeDigest && outcome.ObservedOutcomeDigest == acceptance.CanonicalOutcomeDigest && !outcome.ObservedAt.IsZero()
}

// Canonical receipts use identity's bare hash, while command outcome digests use
// the sha256: wire format. Recompute the original closing receipt so historical
// reconciliation checks its contents instead of applying the wrong format gate
// (core642). ClosedAt and the final state are recorded after that receipt exists.
func validControllerCanonicalReceipt(record NativeAcceptanceRecord) bool {
	if record.CreatedAt.IsZero() || record.CanonicalReceiptDigest == "" {
		return false
	}
	closing := NativeAcceptanceRecord{
		OperationKey: record.OperationKey,
		InputDigest:  record.InputDigest,
		State:        "closing",
		Candidate:    record.Candidate,
		CreatedAt:    record.CreatedAt,
	}
	return record.CanonicalReceiptDigest == identity("native-acceptance-receipt", "", closing)
}
