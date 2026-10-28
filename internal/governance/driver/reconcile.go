package driver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"time"

	"github.com/mlnomadpy/dacli/internal/governance/executor"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
)

type runState struct {
	driver                    *Driver
	request                   Request
	ctx                       context.Context
	started                   time.Time
	deadline                  time.Time
	view                      sessions.ControllerDriverView
	transitions, effects      uint64
	work, noProgress          uint64
	progressLimit             uint64
	initialHistoryEffects     uint64
	initialHistoryTransitions uint64
	initialPolicyRevision     string
}

// Run executes a bounded serial sequence of exact controller operations. It
// rereads canonical state after every callback and never trusts a port receipt
// as completion by itself.
func (d *Driver) Run(ctx context.Context, request Request) (Result, error) {
	if d == nil || d.ports == nil || ctx == nil {
		return Result{}, ErrInvalidRequest
	}
	if err := validateRequest(request); err != nil {
		return Result{}, err
	}
	started := time.Now()
	state := &runState{driver: d, request: request, ctx: ctx, started: started, progressLimit: d.limits.MaxNoProgress}
	if err := state.checkContext(); err != nil {
		return state.result("deadline", "context_expired", "stop before another operation", nil), nil
	}
	if !state.reserveWork(1) {
		return state.result("limit", "work_limit", "increase the bounded invocation profile", nil), nil
	}
	readCtx, cancel := context.WithTimeout(ctx, d.limits.OperationTimeout)
	view, err := d.ports.Read(readCtx, request)
	cancel()
	if err != nil {
		return state.result(outcomeForError(err), reasonForError(err), nextForError(err), nil), nil
	}
	if view.AuthorityVersion < 16 {
		return state.result("unsupported", "controller_schema_unsupported", "use an authority with supported controller records", &view), nil
	}
	if err := validateViewIdentity(request, view); err != nil {
		return state.result("stale", "request_identity_mismatch", "read the current controller identity and submit it explicitly", &view), nil
	}
	if !state.withinHistoricalBounds(view) {
		return state.result("limit", "historical_profile_limit", "owner selects a profile within the retained invocation history", &view), nil
	}
	if view.Status.StartedAt.IsZero() || view.Status.Deadline.IsZero() || view.Status.Sequence == 0 || view.Policy.MaxNoProgress < 1 || view.Policy.MaxElapsed <= 0 {
		return state.result("uncertain", "controller_bounds_unknown", "owner reconciles the durable invocation bounds", &view), nil
	}
	state.view = view
	state.initialHistoryEffects = view.HistoricalOperationCount
	state.initialHistoryTransitions = view.Status.Sequence - 1
	state.initialPolicyRevision = view.Policy.Revision
	state.progressLimit = minUint64(state.progressLimit, uint64(view.Policy.MaxNoProgress))
	state.deadline = view.Status.StartedAt.Add(d.limits.MaxElapsed)
	policyDeadline := view.Status.StartedAt.Add(view.Policy.MaxElapsed)
	if policyDeadline.Before(state.deadline) {
		state.deadline = policyDeadline
	}
	if view.Status.Deadline.Before(state.deadline) {
		state.deadline = view.Status.Deadline
	}
	if callerDeadline, ok := ctx.Deadline(); ok && callerDeadline.Before(state.deadline) {
		state.deadline = callerDeadline
	}
	if !time.Now().Before(state.deadline) {
		return state.result("deadline", "persisted_deadline_exhausted", "owner reviews the expired invocation; do not restart its deadline", &state.view), nil
	}
	runCtx, runCancel := context.WithDeadline(ctx, state.deadline)
	defer runCancel()
	state.ctx = runCtx
	if view.Status.Sequence != request.ExpectedSequence {
		return state.result("stale", "sequence_mismatch", "read the latest sequence and resubmit explicitly", &state.view), nil
	}
	if view.Status.Sequence >= d.limits.MaxSequence {
		return state.result("limit", "absolute_sequence_limit", "owner establishes a new bounded invocation", &state.view), nil
	}
	if request.Mode == "stop" {
		return state.stop()
	}
	if request.Mode == "cancel" {
		if outcome, reason, next, done := state.reconcileOriginals(); done {
			return state.result(outcome, reason, next, &state.view), nil
		}
		return state.cancel()
	}
	return state.drive()
}

func (r *runState) drive() (Result, error) {
	for {
		if err := r.checkContext(); err != nil {
			return r.result("deadline", reasonForError(err), nextForError(err), &r.view), nil
		}
		if outcome, reason, next, done := r.reconcileOriginals(); done {
			return r.result(outcome, reason, next, &r.view), nil
		}
		if outcome, reason, next, done := r.terminalOrHalt(); done {
			return r.result(outcome, reason, next, &r.view), nil
		}
		if r.view.Status.Sequence >= r.driver.limits.MaxSequence {
			return r.result("limit", "absolute_sequence_limit", "owner establishes a new bounded invocation", &r.view), nil
		}
		if r.view.Status.Waves >= r.view.Policy.MaxWaves && len(r.view.Status.Actions) == 0 {
			return r.result("limit", "invocation_wave_limit", "owner reviews the existing invocation limit", &r.view), nil
		}
		if err := r.checkContext(); err != nil {
			return r.result("deadline", reasonForError(err), nextForError(err), &r.view), nil
		}
		if r.view.Status.Phase == "observe" && r.view.Status.HaltClass == "lifecycle_unknown" {
			action, ok := r.observationAction()
			if !ok {
				return r.result("uncertain", "lifecycle_binding_missing", "owner reconciles the original host intent", &r.view), nil
			}
			if outcome, reason, next, done := r.observe(action); done {
				return r.result(outcome, reason, next, &r.view), nil
			}
			continue
		}

		if action, ok := r.actionForPhase(); ok {
			switch action.Kind {
			case "verification":
				if outcome, reason, next, done := r.verify(action); done {
					return r.result(outcome, reason, next, &r.view), nil
				}
			case "independent_review":
				if outcome, reason, next, done := r.review(action); done {
					return r.result(outcome, reason, next, &r.view), nil
				}
			case "integration_checkpoint", "canonical_acceptance", "accepted":
				if outcome, reason, next, done := r.ownerGate(action); done {
					return r.result(outcome, reason, next, &r.view), nil
				}
			case "correction":
				return r.result("awaiting_external", "correction_requires_worker", "wait for the assigned worker's external correction", &r.view), nil
			}
		}
		if r.revisionPendingAwaitingExternal() {
			return r.result("awaiting_external", "revision_pending", "wait for the assigned workers to publish their externally produced revisions", &r.view), nil
		}

		before := driverProgressDigest(r.view)
		if outcome, reason, next, done := r.stepOnce(); done {
			return r.result(outcome, reason, next, &r.view), nil
		}
		if r.recordProgress(before) {
			return r.result("no_progress", "durable_progress_unchanged", "owner reconciles the exact original operation before continuing", &r.view), nil
		}
	}
}

func (r *runState) revisionPendingAwaitingExternal() bool {
	if r.view.Status.Phase != "observe" || r.view.Status.HaltClass != "revision_pending" || len(r.view.Status.Actions) == 0 {
		return false
	}
	for _, action := range r.view.Status.Actions {
		if action.Kind == "dispatch" {
			return false
		}
		if action.Kind != "host_dispatch" && action.Kind != "observe" {
			continue
		}
		row, ok := operationByKey(r.view, "dispatch", action.OperationKey)
		if !ok || row.State != "completed" {
			return false
		}
	}
	return true
}

func (r *runState) stop() (Result, error) {
	if r.view.Status.Terminal {
		return r.result("stopped", "already_terminal", "inspect the existing terminal controller outcome", &r.view), nil
	}
	key, err := OperationKey(r.request.ProjectID, r.request.InvocationID, r.request.CheckpointID, r.view.Status.Sequence, "stop", "")
	if err != nil {
		// The driver reports expected refusal as a typed Result; Go errors are reserved for execution failures.
		//nolint:nilerr
		return r.result("refused", "stop_key_invalid", "owner inspects the controller identity", &r.view), nil
	}
	if !r.canReserveTransitions(1) || !r.canReserveEffects(1) || !r.hasWork(2) || !r.reserveWork(1) {
		return r.result("limit", "bounded_transition_limit", "owner reviews the existing invocation bounds", &r.view), nil
	}
	r.transitions++
	if err := r.ReserveEffect(); err != nil {
		// Effect exhaustion is a bounded Result outcome, not an execution error.
		//nolint:nilerr
		return r.result("limit", "effect_limit", "owner selects a profile with remaining retained effect capacity", &r.view), nil
	}
	step := sessions.ControllerStepRequest{OperationKey: key, ProjectID: r.request.ProjectID, InvocationID: r.request.InvocationID, ExpectedSequence: r.view.Status.Sequence}
	ctx, cancel := r.operationContext()
	_, err = r.driver.ports.Stop(ctx, step)
	cancel()
	if freshErr := r.refresh(); freshErr != nil {
		return r.result(outcomeForError(freshErr), reasonForError(freshErr), nextForError(freshErr), &r.view), nil
	}
	if err != nil {
		return r.result(outcomeForError(err), reasonForError(err), nextForError(err), &r.view), nil
	}
	return r.result("stopped", "stop_recorded", "reconcile retained operations; STOP authorizes no new effects", &r.view), nil
}

func (r *runState) cancel() (Result, error) {
	var action *sessions.ControllerAction
	for i := range r.view.Status.Actions {
		if r.view.Status.Actions[i].DispatchID == r.request.CancelDispatchID {
			copy := r.view.Status.Actions[i]
			action = &copy
			break
		}
	}
	if action == nil {
		return r.result("refused", "cancel_dispatch_out_of_scope", "select a dispatch in the exact current controller view", &r.view), nil
	}
	for _, operation := range r.view.Operations {
		if operation.Kind == "cancellation" && operation.DispatchID == action.DispatchID {
			if operation.State != "absent" {
				if !r.hasWork(2) || !r.reserveWork(1) {
					return r.result("limit", "work_limit", "owner reviews the bounded lookup limit", &r.view), nil
				}
				before := driverProgressDigest(r.view)
				ctx, cancel := r.operationContext()
				receipts, err := r.driver.ports.Reconcile(ctx, *action, r)
				cancel()
				var receiptErr error
				if err == nil {
					receiptErr = validateCancellationReceipts(r.view, *action, receipts, r.request)
				}
				if freshErr := r.refresh(); freshErr != nil {
					return r.result(outcomeForError(freshErr), reasonForError(freshErr), nextForError(freshErr), &r.view), nil
				}
				if err != nil {
					return r.result(outcomeForError(err), reasonForError(err), nextForError(err), &r.view), nil
				}
				if receiptErr != nil {
					// Receipt mismatch is reported as a typed uncertainty Result.
					//nolint:nilerr
					return r.result("uncertain", "cancellation_receipt_mismatch", "owner reconciles the original cancellation operation", &r.view), nil
				}
				for _, current := range r.view.Operations {
					if current.Kind == "cancellation" && current.DispatchID == action.DispatchID {
						if current.State == "completed" {
							return r.result("cancelled", "cancellation_acknowledged", "retain writer protection until an independent terminal observation", &r.view), nil
						}
						if current.State == "pending" {
							return r.result("awaiting_external", "cancellation_pending", "reconcile the original cancellation key; retain protection", &r.view), nil
						}
						return r.result("uncertain", "cancellation_outcome_unknown", "owner reconciles the original cancellation key", &r.view), nil
					}
				}
				if r.recordProgress(before) {
					return r.result("uncertain", "cancellation_record_missing", "owner reconciles the original cancellation key", &r.view), nil
				}
			}
		}
	}
	if err := r.checkContext(); err != nil {
		return r.result("deadline", reasonForError(err), nextForError(err), &r.view), nil
	}
	if !r.hasWork(2) || !r.reserveWork(1) {
		return r.result("limit", "effect_or_work_limit", "owner reviews the existing invocation bounds", &r.view), nil
	}
	before := driverProgressDigest(r.view)
	ctx, cancel := r.operationContext()
	receipts, err := r.driver.ports.Cancel(ctx, *action, r)
	cancel()
	var receiptErr error
	if err == nil {
		receiptErr = validateCancellationReceipts(r.view, *action, receipts, r.request)
	}
	if freshErr := r.refresh(); freshErr != nil {
		return r.result(outcomeForError(freshErr), reasonForError(freshErr), nextForError(freshErr), &r.view), nil
	}
	if err != nil {
		return r.result(outcomeForError(err), reasonForError(err), nextForError(err), &r.view), nil
	}
	if receiptErr != nil {
		// Receipt mismatch is reported as a typed uncertainty Result.
		//nolint:nilerr
		return r.result("uncertain", "cancellation_receipt_mismatch", "owner reconciles the original cancellation operation", &r.view), nil
	}
	if r.recordProgress(before) {
		return r.result("no_progress", "cancellation_knowledge_unchanged", "reconcile the original cancellation key and retain resources", &r.view), nil
	}
	return r.result("cancelled", "cancellation_requested", "observe the original cancellation and retain its writer until terminal proof", &r.view), nil
}

func (r *runState) observationAction() (sessions.ControllerAction, bool) {
	for _, action := range r.view.Status.Actions {
		if action.Kind != "host_dispatch" {
			continue
		}
		if operation, ok := operationByKey(r.view, "dispatch", action.OperationKey); ok && operation.State == "completed" {
			return action, true
		}
	}
	return sessions.ControllerAction{}, false
}

func (r *runState) hasReservationOnlySibling() bool {
	for _, action := range r.view.Status.Actions {
		if action.Kind != "dispatch" {
			continue
		}
		for _, row := range operationsForAction(r.view, action.ID) {
			if row.Kind == "dispatch" && row.State == "absent" && row.IntentID == "" {
				return true
			}
		}
	}
	return false
}

func (r *runState) observe(action sessions.ControllerAction) (string, string, string, bool) {
	if err := r.checkContext(); err != nil {
		return "deadline", reasonForError(err), nextForError(err), true
	}
	if !r.hasWork(2) || !r.reserveWork(1) {
		return "limit", "effect_or_work_limit", "owner reviews the existing invocation bounds", true
	}
	ctx, cancel := r.operationContext()
	receipts, err := r.driver.ports.Observe(ctx, action, r)
	cancel()
	if err == nil {
		err = validateObservationReceipts(r.view, action, receipts)
	}
	if freshErr := r.refresh(); freshErr != nil {
		return outcomeForError(freshErr), reasonForError(freshErr), nextForError(freshErr), true
	}
	if err != nil {
		if errors.Is(err, ErrInvalidRequest) {
			return "uncertain", "observation_receipt_mismatch", "owner reconciles the original host observation", true
		}
		return outcomeForError(err), reasonForError(err), nextForError(err), true
	}
	state := receipts[0].Lifecycle.Observations[len(receipts[0].Lifecycle.Observations)-1].State
	switch state {
	case "running":
		if r.hasReservationOnlySibling() {
			if outcome, reason, next, done := r.stepOnce(); done {
				return outcome, reason, next, true
			}
			return "", "", "", false
		}
		return "awaiting_external", "revision_pending", "wait for the assigned worker to publish its externally produced revision", true
	case "completed", "failed", "cancelled":
		if r.view.Status.Terminal {
			return r.terminalOrHalt()
		}
		if outcome, reason, next, done := r.stepOnce(); done {
			return outcome, reason, next, true
		}
		return "", "", "", false
	default:
		return "uncertain", "host_observation_unknown", "reconcile the original host lifecycle before continuing", true
	}
}

func (r *runState) reconcileOriginals() (string, string, string, bool) {
	currentActions := make(map[string]bool, len(r.view.Status.Actions))
	for _, action := range r.view.Status.Actions {
		currentActions[action.ID] = true
		if err := r.checkContext(); err != nil {
			return "deadline", reasonForError(err), nextForError(err), true
		}
		rows := operationsForAction(r.view, action.ID)
		needsLookup := false
		for _, row := range rows {
			if !r.reserveWork(1) {
				return "limit", "work_limit", "owner reviews the bounded lookup limit", true
			}
			if r.request.Mode == "cancel" && action.DispatchID == r.request.CancelDispatchID && targetVerificationRow(action, row) && (row.State == "pending" || row.State == "unknown" || row.State == "interrupted") {
				// The exact target's pending verification must not be rerun or
				// reconciled to completion before its separately authorized cancel.
				continue
			}
			if action.Kind == "dispatch" && row.Kind == "dispatch" && row.State == "absent" && row.IntentID == "" {
				// No host intent exists yet: the next source-checked Step creates it.
				continue
			}
			if row.State == "completed" || row.State == "absent" && row.Kind != "dispatch" {
				continue
			}
			if row.State != "completed" {
				needsLookup = true
			}
		}
		if !needsLookup {
			continue
		}
		if !r.hasWork(2) || !r.reserveWork(1) {
			return "limit", "work_limit", "owner reviews the bounded lookup limit", true
		}
		before := driverProgressDigest(r.view)
		ctx, cancel := r.operationContext()
		receipts, err := r.driver.ports.Reconcile(ctx, action, r)
		cancel()
		var receiptErr error
		if err == nil {
			receiptErr = validateReceipts(r.view, action, receipts)
		}
		if freshErr := r.refresh(); freshErr != nil {
			return outcomeForError(freshErr), reasonForError(freshErr), nextForError(freshErr), true
		}
		if err != nil {
			return outcomeForError(err), reasonForError(err), nextForError(err), true
		}
		if receiptErr != nil {
			return "uncertain", "reconciliation_receipt_mismatch", "owner reconciles exact original operation keys", true
		}
		for _, row := range operationsForAction(r.view, action.ID) {
			if row.State == "pending" {
				return "awaiting_external", "original_operation_pending", "wait for the original operation outcome", true
			}
			if row.State == "unknown" || row.State == "interrupted" {
				return "uncertain", "original_operation_unknown", "owner reconciles the original operation; retain protection", true
			}
		}
		for _, row := range operationsForAction(r.view, action.ID) {
			if row.Kind == "dispatch" && row.State == "absent" && !(action.Kind == "dispatch" && row.IntentID == "") {
				return "uncertain", "original_dispatch_absent", "owner reconciles the exact committed dispatch identity; never create a replacement key", true
			}
		}
		if r.recordProgress(before) {
			return "no_progress", "reconciliation_unchanged", "wait for the original operation outcome; do not replace its key", true
		}
	}
	for _, row := range r.view.Operations {
		if (row.ActionID != "" && currentActions[row.ActionID]) || row.State == "completed" || row.State == "absent" {
			continue
		}
		if row.State == "pending" {
			return "awaiting_external", "original_operation_pending", "wait for the original operation outcome", true
		}
		if row.State == "unknown" || row.State == "interrupted" {
			return "uncertain", "original_operation_unknown", "owner reconciles the original operation; retain protection", true
		}
	}
	return "", "", "", false
}

func targetVerificationRow(action sessions.ControllerAction, row sessions.ControllerDriverOperation) bool {
	if action.Kind != "verification" || row.Kind != "verification" || row.ActionID != action.ID || row.TaskID != action.TaskID || row.Verification == nil || row.OperationKey != row.Verification.OperationKey {
		return false
	}
	for _, request := range verificationRequests(action) {
		if request.OperationKey == row.OperationKey && reflect.DeepEqual(request, *row.Verification) && request.AttemptID == action.AttemptID && request.LeaseID == action.LeaseID && request.RevisionID == action.RevisionID && request.ExpectedFence == action.Fence {
			return true
		}
	}
	return false
}

func (r *runState) verify(action sessions.ControllerAction) (string, string, string, bool) {
	requests := verificationRequests(action)
	if len(requests) == 0 {
		return "uncertain", "verification_request_missing", "owner reconciles the exact controller verification request", true
	}
	for _, request := range requests {
		row, ok := operationByKey(r.view, "verification", request.OperationKey)
		if !ok || row.Verification == nil || !reflect.DeepEqual(*row.Verification, request) {
			return "uncertain", "verification_binding_missing", "owner reconciles the exact stored verification binding", true
		}
		if row.State == "completed" {
			continue
		}
		if row.State == "pending" || row.State == "interrupted" || row.State == "unknown" {
			return "uncertain", "verification_not_rerunnable", "reconcile the original required-check key; never rerun pending work", true
		}
		if row.State != "absent" {
			return "uncertain", "verification_state_unknown", "owner reconciles the exact original verification key", true
		}
		if err := r.checkContext(); err != nil {
			return "deadline", reasonForError(err), nextForError(err), true
		}
		if !r.hasWork(2) || !r.reserveWork(1) {
			return "limit", "effect_or_work_limit", "owner reviews the existing invocation bounds", true
		}
		before := driverProgressDigest(r.view)
		ctx, cancel := r.operationContext()
		receipts, err := r.driver.ports.Verify(ctx, action, request, r)
		cancel()
		var receiptErr error
		if err == nil {
			receiptErr = validateVerificationReceipts(action, request, receipts)
		}
		if freshErr := r.refresh(); freshErr != nil {
			return outcomeForError(freshErr), reasonForError(freshErr), nextForError(freshErr), true
		}
		if err != nil {
			return outcomeForError(err), reasonForError(err), nextForError(err), true
		}
		if receiptErr != nil {
			return "uncertain", "verification_receipt_mismatch", "owner reconciles the original verification key", true
		}
		if r.recordProgress(before) {
			return "no_progress", "verification_result_unchanged", "reconcile the original required-check result", true
		}
		if row, ok = operationByKey(r.view, "verification", request.OperationKey); !ok || row.State != "completed" {
			if ok && row.State == "pending" {
				return "awaiting_external", "verification_pending", "wait for the original runner operation", true
			}
			return "uncertain", "verification_outcome_unknown", "owner reconciles the original runner operation", true
		}
	}
	return "", "", "", false
}

func verificationRequests(action sessions.ControllerAction) []sessions.VerificationRequest {
	requests := append([]sessions.VerificationRequest(nil), action.Verifications...)
	if len(requests) == 0 && action.Verification.OperationKey != "" {
		requests = append(requests, action.Verification)
	}
	return requests
}

func (r *runState) review(action sessions.ControllerAction) (string, string, string, bool) {
	row, ok := operationByKey(r.view, "review", action.OperationKey)
	if !ok {
		return "uncertain", "review_request_missing", "owner reconciles the exact stored review request", true
	}
	if row.State == "completed" {
		return "", "", "", false
	}
	if row.State == "pending" || row.State == "unknown" || row.State == "interrupted" {
		return "uncertain", "review_operation_unknown", "reconcile the original independent review operation", true
	}
	if err := r.checkContext(); err != nil {
		return "deadline", reasonForError(err), nextForError(err), true
	}
	if !r.hasWork(2) || !r.reserveWork(1) {
		return "limit", "effect_or_work_limit", "owner reviews the existing invocation bounds", true
	}
	before := driverProgressDigest(r.view)
	ctx, cancel := r.operationContext()
	receipts, err := r.driver.ports.Review(ctx, action, r)
	cancel()
	var receiptErr error
	if err == nil {
		receiptErr = validateReviewReceipts(action, receipts)
	}
	if freshErr := r.refresh(); freshErr != nil {
		return outcomeForError(freshErr), reasonForError(freshErr), nextForError(freshErr), true
	}
	if errors.Is(err, ErrUnsupported) {
		return "awaiting_external", "independent_review_decision_required", "provide an independent decision for the same original review key", true
	}
	if err != nil {
		return outcomeForError(err), reasonForError(err), nextForError(err), true
	}
	if receiptErr != nil {
		return "uncertain", "review_receipt_mismatch", "owner reconciles the exact original review key", true
	}
	if r.recordProgress(before) {
		return "no_progress", "review_result_unchanged", "reconcile the original independent review request", true
	}
	row, ok = operationByKey(r.view, "review", action.OperationKey)
	if !ok || row.State != "completed" {
		return "awaiting_external", "independent_review_pending", "await an independently supplied review decision", true
	}
	return "", "", "", false
}

func (r *runState) ownerGate(action sessions.ControllerAction) (string, string, string, bool) {
	kind, key := "integration", action.OperationKey
	if action.Kind == "canonical_acceptance" || action.Kind == "accepted" {
		kind = "acceptance"
	}
	row, ok := operationByKey(r.view, kind, key)
	if !ok {
		return "uncertain", "owner_operation_binding_missing", "owner reconciles the exact original producer identity", true
	}
	switch row.State {
	case "completed":
		return "", "", "", false
	case "pending", "unknown", "interrupted":
		return "uncertain", "owner_operation_unknown", "owner reconciles the exact original producer operation", true
	default:
		return "owner_required", "owner_authority_required", "owner applies the separately gated producer operation", true
	}
}

func (r *runState) stepOnce() (string, string, string, bool) {
	if err := r.checkContext(); err != nil {
		return "deadline", reasonForError(err), nextForError(err), true
	}
	if r.view.HistoricalOperationCount >= r.driver.limits.MaxEffects {
		return "limit", "historical_profile_limit", "owner selects a profile with remaining retained operation capacity", true
	}
	if !r.canReserveTransitions(1) {
		return "limit", "transition_limit", "owner reviews the bounded invocation profile", true
	}
	// Keep headroom for a possible first-use dispatch and the mandatory fresh
	// durable read after each callback before committing the Step transition.
	neededEffects := uint64(1)
	if r.stepMayCreateDispatch() {
		neededEffects = 5 // Step, first-use worker registration, host dispatch, correlation, and acknowledgement.
	}
	if !r.canReserveEffects(neededEffects) || !r.hasWork(3) || !r.reserveWork(1) {
		return "limit", "effect_or_work_limit", "owner reviews the bounded invocation profile", true
	}
	key, err := OperationKey(r.request.ProjectID, r.request.InvocationID, r.request.CheckpointID, r.view.Status.Sequence, "step", "")
	if err != nil {
		return "refused", "step_key_invalid", "owner reconciles the controller identity", true
	}
	r.transitions++
	if err := r.ReserveEffect(); err != nil {
		return "limit", "effect_limit", "owner selects a profile with remaining retained effect capacity", true
	}
	before := driverProgressDigest(r.view)
	request := sessions.ControllerStepRequest{OperationKey: key, ProjectID: r.request.ProjectID, InvocationID: r.request.InvocationID, ExpectedSequence: r.view.Status.Sequence}
	ctx, cancel := r.operationContext()
	status, permit, err := r.driver.ports.Step(ctx, request)
	cancel()
	if err != nil {
		if freshErr := r.refresh(); freshErr != nil {
			return outcomeForError(freshErr), reasonForError(freshErr), nextForError(freshErr), true
		}
		return outcomeForError(err), reasonForError(err), nextForError(err), true
	}
	if ref := permit.Reference(); ref.ActionID != "" {
		action, ok := actionByID(status.Actions, ref.ActionID)
		if !ok || action.Kind != "host_dispatch" || action.DispatchID != ref.DispatchID || action.IntentID != ref.IntentID || action.OperationKey != ref.OriginalOperationKey || ref.ReservationFence == 0 || ref.ReservationFence != action.ReservationFence || ref.CheckpointSequence == 0 || ref.ProjectID != r.request.ProjectID || ref.InvocationID != r.request.InvocationID || ref.CheckpointID != r.request.CheckpointID || ref.StepOperationKey != key || ref.ControllerSequence != status.Sequence || status.Sequence != r.view.Status.Sequence+1 {
			if freshErr := r.refresh(); freshErr != nil {
				return outcomeForError(freshErr), reasonForError(freshErr), nextForError(freshErr), true
			}
			return "uncertain", "new_permit_binding_mismatch", "owner reconciles the committed original host intent", true
		}
		if err := r.checkContext(); err != nil {
			if freshErr := r.refresh(); freshErr != nil {
				return outcomeForError(freshErr), reasonForError(freshErr), nextForError(freshErr), true
			}
			return "deadline", reasonForError(err), nextForError(err), true
		}
		if !r.hasWork(2) || !r.reserveWork(1) {
			if freshErr := r.refresh(); freshErr != nil {
				return outcomeForError(freshErr), reasonForError(freshErr), nextForError(freshErr), true
			}
			return "limit", "effect_or_work_limit", "reconcile the original host intent; do not replace its key", true
		}
		dispatchBefore := driverProgressDigest(r.view)
		ctx, cancel = r.operationContext()
		receipts, dispatchErr := r.driver.ports.Dispatch(ctx, permit, action, r)
		cancel()
		if dispatchErr == nil {
			dispatchErr = validateDispatchReceipts(r.request, action, receipts)
		}
		if freshErr := r.refresh(); freshErr != nil {
			return outcomeForError(freshErr), reasonForError(freshErr), nextForError(freshErr), true
		}
		if dispatchErr != nil {
			if errors.Is(dispatchErr, ErrInvalidRequest) {
				return "uncertain", "dispatch_receipt_mismatch", "owner reconciles the original host operation", true
			}
			return outcomeForError(dispatchErr), reasonForError(dispatchErr), nextForError(dispatchErr), true
		}
		if r.recordProgress(dispatchBefore) {
			return "no_progress", "dispatch_knowledge_unchanged", "reconcile the original host dispatch key; never relaunch", true
		}
		return "", "", "", false
	}
	if status.HostActionAuthorized {
		if freshErr := r.refresh(); freshErr != nil {
			return outcomeForError(freshErr), reasonForError(freshErr), nextForError(freshErr), true
		}
		return "uncertain", "new_permit_missing", "owner reconciles the committed original host intent", true
	}
	if err := r.refresh(); err != nil {
		return outcomeForError(err), reasonForError(err), nextForError(err), true
	}
	if !sameControllerSequence(status, r.view.Status) && r.view.Status.Sequence < status.Sequence {
		return "stale", "controller_sequence_regressed", "owner reconciles the current invocation history", true
	}
	if r.recordProgress(before) {
		return "no_progress", "controller_progress_unchanged", "stop after repeated unchanged durable progress", true
	}
	return "", "", "", false
}

func (r *runState) stepMayCreateDispatch() bool {
	if len(r.view.Status.Actions) == 0 {
		return true
	}
	for _, action := range r.view.Status.Actions {
		if action.Kind == "dispatch" {
			return true
		}
	}
	return false
}

func (r *runState) actionForPhase() (sessions.ControllerAction, bool) {
	for _, action := range r.view.Status.Actions {
		if r.view.Status.Phase == "verification" && action.Kind == "verification" || r.view.Status.Phase == "review" && action.Kind == "independent_review" || r.view.Status.Phase == "integration" && (action.Kind == "integration_checkpoint" || action.Kind == "canonical_acceptance" || action.Kind == "accepted") || action.Kind == "correction" {
			return action, true
		}
	}
	return sessions.ControllerAction{}, false
}

func (r *runState) terminalOrHalt() (string, string, string, bool) {
	status := r.view.Status
	if status.Terminal {
		if status.HaltClass == "policy_refused" || status.HaltClass == "policy_refusal" {
			return "refused", safeReason(status.HaltClass), "owner reviews the exact policy refusal", true
		}
		if status.Phase == "completed" && status.HaltClass == "empty_backlog" {
			return "completed", "canonical_controller_completed", "no additional effect is authorized", true
		}
		if status.HaltClass == "stopped" || status.HaltClass == "owner_stop" {
			return "stopped", safeReason(status.HaltClass), "owner STOP prevents new effects; retain existing writers", true
		}
		return "owner_required", safeReason(status.HaltClass), "owner reviews the terminal controller outcome", true
	}
	switch status.HaltClass {
	case "stopped", "owner_stop":
		return "stopped", safeReason(status.HaltClass), "owner STOP prevents new effects; retain existing writers", true
	case "policy_refused", "policy_refusal":
		return "refused", safeReason(status.HaltClass), "owner reviews the exact policy refusal", true
	case "pending_local_integration", "pending_canonical_acceptance":
		return "owner_required", safeReason(status.HaltClass), "owner applies the separately gated producer operation", true
	case "revision_pending":
		// The canonical controller remains the scheduler. A known-running
		// dispatch retains its writer, but must not block a ready sibling from
		// being selected by the next bounded step.
		if status.Phase == "observe" {
			return "", "", "", false
		}
		return "awaiting_external", safeReason(status.HaltClass), "wait for the assigned worker's externally published revision", true
	case "verification_pending":
		return "awaiting_external", safeReason(status.HaltClass), "wait for the assigned worker or original producer result", true
	case "dispatch_unknown", "verification_unknown", "verification_interrupted":
		return "uncertain", safeReason(status.HaltClass), "reconcile original operation keys and retain protection", true
	case "unsupported_host", "unsupported_stage", "unsupported_schema":
		return "unsupported", safeReason(status.HaltClass), "select an explicitly supported host or schema", true
	case "transient_backoff":
		return "awaiting_external", safeReason(status.HaltClass), "wait for the existing bounded retry time", true
	}
	return "", "", "", false
}

func (r *runState) refresh() error {
	if err := r.checkContext(); err != nil {
		return err
	}
	if !r.reserveWork(1) {
		return errors.New("driver work limit")
	}
	ctx, cancel := r.operationContext()
	view, err := r.driver.ports.Read(ctx, r.request)
	cancel()
	if err != nil {
		return err
	}
	if view.AuthorityVersion < 16 {
		return sessions.ErrControllerDriverUnsupported
	}
	if view.Status.ID != r.request.InvocationID || view.Status.ProjectID != r.request.ProjectID || view.Status.CheckpointID != r.request.CheckpointID || view.Policy.Revision != r.initialPolicyRevision || view.Status.Sequence < r.view.Status.Sequence || view.CheckpointSequence < r.view.CheckpointSequence {
		return ErrInvalidRequest
	}
	if !r.withinHistoricalBounds(view) {
		return ErrWorkLimit
	}
	r.view = view
	return r.checkContext()
}

func (r *runState) operationContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.ctx, r.driver.limits.OperationTimeout)
}

func (r *runState) checkContext() error {
	if err := r.ctx.Err(); err != nil {
		return err
	}
	if !r.deadline.IsZero() && !time.Now().Before(r.deadline) {
		return context.DeadlineExceeded
	}
	return nil
}

func (r *runState) reserveWork(amount uint64) bool {
	if !r.hasWork(amount) {
		return false
	}
	r.work += amount
	return true
}

func (r *runState) hasWork(amount uint64) bool {
	return r.work <= r.driver.limits.MaxWork && amount <= r.driver.limits.MaxWork-r.work
}

func (r *runState) reserveEffect() bool {
	if !r.canReserveEffects(1) {
		return false
	}
	r.effects++
	return true
}

// ReserveEffect implements EffectBudget. Each call counts one actual
// write-capable boundary, even when the producer's durable outcome is unknown.
func (r *runState) ReserveEffect() error {
	if !r.reserveEffect() {
		return ErrEffectLimit
	}
	return nil
}

func (r *runState) canReserveEffects(amount uint64) bool {
	max := r.driver.limits.MaxEffects
	if r.view.HistoricalOperationCount > max || r.effects > max || amount > max-r.effects || r.initialHistoryEffects > max {
		return false
	}
	local := r.effects + amount
	if local > max-r.initialHistoryEffects {
		return false
	}
	return true // both retained H and projected H0+L+n fit the profile
}

func (r *runState) canReserveTransitions(amount uint64) bool {
	max := r.driver.limits.MaxTransitions
	if r.transitions > max || amount > max-r.transitions || r.view.Status.Sequence == 0 || r.initialHistoryTransitions > max {
		return false
	}
	retained := r.view.Status.Sequence - 1
	local := r.initialHistoryTransitions + r.transitions + amount
	if retained > max || local > max {
		return false
	}
	return true // retained H and projected entry history plus local transitions fit
}

func (r *runState) withinHistoricalBounds(view sessions.ControllerDriverView) bool {
	return view.Status.Sequence > 0 && view.Status.Sequence-1 <= r.driver.limits.MaxTransitions && view.HistoricalOperationCount <= r.driver.limits.MaxEffects
}

func (r *runState) recordProgress(before string) bool {
	after := driverProgressDigest(r.view)
	if before == after {
		r.noProgress++
	} else {
		r.noProgress = 0
	}
	return r.noProgress >= r.progressLimit
}

func (r *runState) result(outcome, reason, next string, view *sessions.ControllerDriverView) Result {
	result := Result{Schema: ResultSchema, ProjectID: r.request.ProjectID, InvocationID: r.request.InvocationID, CheckpointID: r.request.CheckpointID, Outcome: outcome, Reason: reason, NextAction: next, Transitions: r.transitions, Effects: r.effects, Work: r.work, NoProgress: r.noProgress, ElapsedMS: time.Since(r.started).Milliseconds(), OriginalOperations: []OriginalOperation{}}
	if view == nil {
		return result
	}
	result.Sequence = view.Status.Sequence
	result.CheckpointSequence = view.CheckpointSequence
	if view.Status.RetainedConcurrency >= 0 {
		retained := view.Status.RetainedConcurrency > 0
		result.ResourcesRetained = &retained
	}
	result.OriginalOperations = make([]OriginalOperation, 0, len(view.Operations))
	for _, operation := range view.Operations {
		result.OriginalOperations = append(result.OriginalOperations, OriginalOperation{Kind: operation.Kind, OperationKey: operation.OperationKey, State: operation.State})
	}
	sort.Slice(result.OriginalOperations, func(i, j int) bool {
		if result.OriginalOperations[i].Kind != result.OriginalOperations[j].Kind {
			return result.OriginalOperations[i].Kind < result.OriginalOperations[j].Kind
		}
		return result.OriginalOperations[i].OperationKey < result.OriginalOperations[j].OperationKey
	})
	return result
}

func validateViewIdentity(request Request, view sessions.ControllerDriverView) error {
	if view.Status.ID != request.InvocationID || view.Status.ProjectID != request.ProjectID || view.Status.CheckpointID != request.CheckpointID || view.Status.Sequence == 0 || len(view.Status.Actions) > 256 || len(view.Operations) > 4096 || view.HistoricalActionCount > 4096 || view.HistoricalOperationCount > 4096 {
		return ErrInvalidRequest
	}
	for _, operation := range view.Operations {
		if !driverID(operation.Kind) || !driverID(operation.OperationKey) || !validOperationState(operation.State) {
			return ErrInvalidRequest
		}
	}
	return nil
}

func validOperationState(state string) bool {
	switch state {
	case "absent", "pending", "completed", "interrupted", "unknown":
		return true
	default:
		return false
	}
}

func operationsForAction(view sessions.ControllerDriverView, actionID string) []sessions.ControllerDriverOperation {
	rows := make([]sessions.ControllerDriverOperation, 0)
	for _, row := range view.Operations {
		if row.ActionID == actionID {
			rows = append(rows, row)
		}
	}
	return rows
}

func operationByKey(view sessions.ControllerDriverView, kind, key string) (sessions.ControllerDriverOperation, bool) {
	for _, operation := range view.Operations {
		if operation.Kind == kind && operation.OperationKey == key {
			return operation, true
		}
	}
	return sessions.ControllerDriverOperation{}, false
}

func actionByID(actions []sessions.ControllerAction, id string) (sessions.ControllerAction, bool) {
	for _, action := range actions {
		if action.ID == id {
			return action, true
		}
	}
	return sessions.ControllerAction{}, false
}

func validateReceipts(view sessions.ControllerDriverView, action sessions.ControllerAction, receipts []Receipt) error {
	allowed := map[string]bool{}
	for _, row := range operationsForAction(view, action.ID) {
		allowed[row.OperationKey] = true
	}
	seen := map[string]bool{}
	for _, receipt := range receipts {
		if !allowed[receipt.OperationKey] || receipt.Kind == "" || receipt.State == "" || seen[receipt.OperationKey] {
			return ErrInvalidRequest
		}
		seen[receipt.OperationKey] = true
	}
	return nil
}

func validateDispatchReceipts(request Request, action sessions.ControllerAction, receipts []Receipt) error {
	allowed := map[string]bool{action.OperationKey: true}
	for _, purpose := range []string{"correlate", "ack"} {
		key, err := OperationKey(request.ProjectID, request.InvocationID, request.CheckpointID, 0, purpose, action.OperationKey)
		if err != nil {
			return err
		}
		allowed[key] = true
	}
	for _, receipt := range receipts {
		if !allowed[receipt.OperationKey] || receipt.Kind != "dispatch" || receipt.State == "" {
			return ErrInvalidRequest
		}
		if receipt.Host != nil && (receipt.Host.OperationKey != action.OperationKey || receipt.Host.DispatchID != action.DispatchID) {
			return ErrInvalidRequest
		}
		if receipt.Intent != nil && (receipt.Intent.OperationKey != action.OperationKey || receipt.Intent.DispatchID != action.DispatchID || receipt.Intent.ID != action.IntentID) {
			return ErrInvalidRequest
		}
		if receipt.Lifecycle != nil && receipt.Lifecycle.Binding.DispatchID != action.DispatchID {
			return ErrInvalidRequest
		}
	}
	return nil
}

func validateVerificationReceipts(action sessions.ControllerAction, request sessions.VerificationRequest, receipts []Receipt) error {
	if len(receipts) != 1 {
		return ErrInvalidRequest
	}
	receipt := receipts[0]
	if receipt.Kind != "verification" || receipt.OperationKey != request.OperationKey || receipt.State == "" || receipt.Verification == nil {
		return ErrInvalidRequest
	}
	lookup := receipt.Verification
	switch lookup.State {
	case sessions.VerificationCompleted:
		if lookup.Evidence == nil || lookup.Evidence.Provenance != "trusted-local-runner" || lookup.Evidence.GrantID != request.GrantID || lookup.Evidence.CheckID != request.CheckID || lookup.Evidence.CommandDigest == "" || lookup.Evidence.OutputDigest == "" || lookup.Evidence.Revision.ID != request.RevisionID || lookup.Evidence.Revision.AttemptID != request.AttemptID || lookup.Evidence.Revision.SessionID != request.WorkerSession || lookup.Evidence.Revision.LeaseID != request.LeaseID || lookup.Evidence.Revision.Fence != request.ExpectedFence || lookup.Evidence.IssuerSession != action.ActorID {
			return ErrInvalidRequest
		}
	case sessions.VerificationPending:
		if lookup.Evidence != nil || lookup.Interruption != nil {
			return ErrInvalidRequest
		}
	case sessions.VerificationInterrupted:
		if lookup.Interruption == nil || !reflect.DeepEqual(lookup.Interruption.Request, request) || lookup.Evidence != nil {
			return ErrInvalidRequest
		}
	default:
		return ErrInvalidRequest
	}
	return nil
}

func validateReviewReceipts(action sessions.ControllerAction, receipts []Receipt) error {
	if len(receipts) != 1 {
		return ErrInvalidRequest
	}
	receipt := receipts[0]
	if receipt.Kind != "review" || receipt.OperationKey != action.OperationKey || receipt.State == "" || receipt.Review == nil {
		return ErrInvalidRequest
	}
	review := receipt.Review
	if review.Request.OperationKey != action.OperationKey || review.Request.AttemptID != action.AttemptID || review.Request.LeaseID != action.LeaseID || review.Request.RevisionID != action.RevisionID || review.Request.ExpectedFence != action.Fence || review.IssuerSession != action.ActorID {
		return ErrInvalidRequest
	}
	return nil
}

func validateObservationReceipts(view sessions.ControllerDriverView, action sessions.ControllerAction, receipts []Receipt) error {
	if len(receipts) != 1 {
		return ErrInvalidRequest
	}
	for _, receipt := range receipts {
		if receipt.Kind != "dispatch" || receipt.State == "" {
			return ErrInvalidRequest
		}
		if receipt.Lifecycle == nil {
			return ErrInvalidRequest
		}
		observation := receipt.Lifecycle
		binding := observation.Binding
		if binding.DispatchID != action.DispatchID || binding.IntentID != action.IntentID || binding.TaskID != action.TaskID || binding.ReservationFence != action.ReservationFence {
			return ErrInvalidRequest
		}
		if observation.State == "" || observation.Freshness == "" {
			return ErrInvalidRequest
		}
		if len(observation.Observations) == 0 {
			return ErrInvalidRequest
		}
		last := observation.Observations[len(observation.Observations)-1]
		if last.HostSequence == 0 || last.Binding.DispatchID != action.DispatchID || last.Binding.IntentID != action.IntentID || last.Binding.TaskID != action.TaskID || last.Binding.ReservationFence != action.ReservationFence || last.SourceTime.IsZero() || last.SourceTime.After(time.Now().Add(time.Minute)) || last.EvidenceDigest == "" || !(last.State == "running" || last.State == "unknown" || last.State == "completed" || last.State == "failed" || last.State == "cancelled") {
			return ErrInvalidRequest
		}
		key, err := OperationKey(view.Status.ProjectID, view.Status.ID, view.Status.CheckpointID, last.HostSequence, "observe", action.OperationKey)
		if err != nil || receipt.OperationKey != key {
			return ErrInvalidRequest
		}
	}
	return nil
}

func validateCancellationReceipts(view sessions.ControllerDriverView, action sessions.ControllerAction, receipts []Receipt, request Request) error {
	original := ""
	for _, row := range view.Operations {
		if row.Kind == "dispatch" && row.DispatchID == action.DispatchID {
			if original != "" && original != row.OperationKey {
				return ErrInvalidRequest
			}
			original = row.OperationKey
		}
	}
	if original == "" {
		return ErrInvalidRequest
	}
	allowed := map[string]bool{}
	for _, purpose := range []string{"cancel", "cancel-ack"} {
		key, err := OperationKey(request.ProjectID, request.InvocationID, request.CheckpointID, 0, purpose, original)
		if err != nil {
			return err
		}
		allowed[key] = true
	}
	for _, receipt := range receipts {
		if !allowed[receipt.OperationKey] || receipt.Kind != "cancellation" || receipt.State == "" {
			return ErrInvalidRequest
		}
		if receipt.Lifecycle != nil && receipt.Lifecycle.Binding.DispatchID != action.DispatchID {
			return ErrInvalidRequest
		}
	}
	return nil
}

func sameControllerSequence(a, b sessions.ControllerStatus) bool {
	return a.ID == b.ID && a.ProjectID == b.ProjectID && a.CheckpointID == b.CheckpointID && a.Sequence == b.Sequence
}

func driverProgressDigest(view sessions.ControllerDriverView) string {
	status := view.Status
	status.Sequence = 0
	status.CheckpointSequence = 0
	status.ObservedAt = time.Time{}
	status.NextRetryAt = time.Time{}
	status.Historical = false
	status.HostActionAuthorized = false
	status.NoProgress = 0
	data, _ := json.Marshal(struct {
		Status     sessions.ControllerStatus
		Operations []sessions.ControllerDriverOperation
	}{status, view.Operations})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func minUint64(left, right uint64) uint64 {
	if left < right {
		return left
	}
	return right
}

func safeReason(reason string) string {
	if !driverID(reason) {
		return "controller_state_unknown"
	}
	return reason
}

func outcomeForError(err error) string {
	switch {
	case errors.Is(err, ErrWorkLimit):
		return "limit"
	case errors.Is(err, ErrEffectLimit):
		return "limit"
	case errors.Is(err, sessions.ErrControllerDriverUnsupported), errors.Is(err, ErrUnsupported), errors.Is(err, executor.ErrUnsupportedContextBackend):
		return "unsupported"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, sessions.ErrDenied), errors.Is(err, executor.ErrDenied):
		return "refused"
	default:
		return "uncertain"
	}
}

func reasonForError(err error) string {
	switch {
	case errors.Is(err, ErrWorkLimit):
		return "work_limit"
	case errors.Is(err, ErrEffectLimit):
		return "effect_limit"
	case errors.Is(err, sessions.ErrControllerDriverUnsupported):
		return "controller_schema_unsupported"
	case errors.Is(err, executor.ErrUnsupportedContextBackend):
		return "unsupported_context_backend"
	case errors.Is(err, ErrUnsupported):
		return "unsupported_operation"
	case errors.Is(err, context.DeadlineExceeded):
		return "operation_deadline"
	case errors.Is(err, context.Canceled):
		return "caller_cancelled"
	case errors.Is(err, sessions.ErrDenied), errors.Is(err, executor.ErrDenied):
		return "authority_refused"
	case errors.Is(err, ErrInvalidRequest):
		return "controller_identity_changed"
	default:
		return "operation_outcome_unknown"
	}
}

func nextForError(err error) string {
	if errors.Is(err, sessions.ErrControllerDriverUnsupported) {
		return "use an authority with supported controller records; do not upgrade this schema in place"
	}
	if errors.Is(err, executor.ErrUnsupportedContextBackend) || errors.Is(err, ErrUnsupported) {
		return "select an explicitly supported context backend"
	}
	if errors.Is(err, sessions.ErrDenied) || errors.Is(err, executor.ErrDenied) {
		return "owner reviews the exact authority refusal; do not retry unchanged"
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "reconcile original operation keys before any retry"
	}
	return "owner reconciles the exact original operation and retains protection"
}
