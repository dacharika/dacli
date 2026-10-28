package sessions

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"time"
)

// ErrControllerDriverUnsupported reports an authority schema that predates the
// persisted controller records consumed by the finite driver. It never upgrades
// the authority or creates replacement state.
var ErrControllerDriverUnsupported = errors.New("controller driver authority schema unsupported")

type ControllerDriverEffectBoundary string

const (
	ControllerDriverBoundaryStep          ControllerDriverEffectBoundary = "step"
	ControllerDriverBoundaryDispatch      ControllerDriverEffectBoundary = "dispatch"
	ControllerDriverBoundaryReconcile     ControllerDriverEffectBoundary = "reconcile"
	ControllerDriverBoundaryObserve       ControllerDriverEffectBoundary = "observe"
	ControllerDriverBoundaryVerify        ControllerDriverEffectBoundary = "verify"
	ControllerDriverBoundaryReview        ControllerDriverEffectBoundary = "review"
	ControllerDriverBoundaryCancelRequest ControllerDriverEffectBoundary = "cancel-request"
)

const (
	controllerDriverMaxActions    = 256
	controllerDriverMaxProjection = 4096
)

// ControllerDriverOperation is a bounded, identifier-only projection of one
// original producer operation. Digests identify inputs and outcomes without
// exposing commands, output, credentials, prompts or review rationale.
type ControllerDriverOperation struct {
	Kind, OperationKey, State                                  string
	ActionID, TaskID, DispatchID, IntentID                     string
	AttemptID, LeaseID, RevisionID, ActorID                    string
	Fence, ReservationFence                                    uint64
	Verification                                               *VerificationRequest
	EvidenceID, ReviewID, RecordID, InputDigest, OutcomeDigest string
}

// ControllerDriverView is a read-only snapshot of existing controller facts.
// It carries no dispatch authority; historical statuses always have their
// first-use host bit cleared.
type ControllerDriverView struct {
	AuthorityVersion   int
	Status             ControllerStatus
	Policy             ControllerPolicy
	CheckpointSequence uint64
	Routes             map[string]ControllerTaskRoute
	Operations         []ControllerDriverOperation
	// HistoricalOperationCount conservatively combines retained producer keys,
	// controller transitions, and pre-reserved first-use host/cancellation slots.
	HistoricalActionCount, HistoricalOperationCount uint64
}

// ReadControllerDriverView projects existing controller and producer records
// for the current original controller. It performs no admission observations
// and writes no authority state.
func (a *Authority) ReadControllerDriverView(ctx context.Context, token, session, project, invocation string) (ControllerDriverView, error) {
	if ctx == nil {
		return ControllerDriverView{}, ErrDenied
	}
	if err := ctx.Err(); err != nil {
		return ControllerDriverView{}, err
	}
	if !validID(project) || !validID(invocation) || token == "" || session == "" {
		return ControllerDriverView{}, ErrDenied
	}
	var out ControllerDriverView
	err := a.transaction(false, false, func(s *state) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if s.Version < 16 {
			actor, err := authenticate(s, token, session, time.Now())
			if err != nil || !subset([]string{"controller.read"}, actor.Scope.Actions) || !subset([]string{"workspace:" + s.Binding.Workspace, "project:" + project}, actor.Scope.Resources) {
				return ErrDenied
			}
			return ErrControllerDriverUnsupported
		}
		iv, ok := s.ControllerInvocations[invocation]
		if !ok || iv.Status.ID != invocation || iv.Status.ProjectID != project || iv.Status.ControllerSessionID != session || iv.Policy.ProjectID != project || iv.Policy.Revision != iv.Status.PolicyRevision || len(iv.Policy.TaskIDs) == 0 || len(iv.Policy.TaskIDs) > controllerDriverMaxActions {
			return ErrDenied
		}
		if err := controllerScope(s, token, session, project, "controller.read", iv.Policy.TaskIDs); err != nil {
			return err
		}
		if !controllerLive(s, session) {
			return ErrDenied
		}
		status := cloneControllerDriverStatus(iv.Status)
		status.HostActionAuthorized = false
		status.Checkpoint = nil
		if len(status.Actions) > controllerDriverMaxActions || len(status.Refs) > controllerDriverMaxProjection {
			return ErrDenied
		}
		if status.Actions == nil {
			status.Actions = []ControllerAction{}
		}
		if status.Refs == nil {
			status.Refs = []CheckpointRefs{}
		}
		history := s.ControllerHistory[invocation]
		if len(history) == 0 || len(history) > controllerDriverMaxProjection || !reflect.DeepEqual(history[len(history)-1], iv.Status) {
			return ErrDenied
		}
		latest := s.Checkpoints[status.CheckpointID]
		if len(latest) > controllerDriverMaxProjection {
			return ErrDenied
		}
		checkpointSequence := uint64(0)
		if len(latest) > 0 {
			checkpoint := latest[len(latest)-1]
			if checkpoint.ID != status.CheckpointID || checkpoint.ProjectID != project || checkpoint.Sequence == 0 || checkpoint.Sequence < status.CheckpointSequence {
				return ErrDenied
			}
			checkpointSequence = checkpoint.Sequence
		} else if status.CheckpointSequence != 0 {
			return ErrDenied
		}

		policy := iv.Policy
		policy.TaskIDs = append([]string(nil), iv.Policy.TaskIDs...)
		projectionWork := uint64(0)
		consumeProjectionWork := func(amount uint64) bool {
			if projectionWork > controllerDriverMaxProjection || amount > controllerDriverMaxProjection-projectionWork {
				return false
			}
			projectionWork += amount
			return true
		}
		routes := make(map[string]ControllerTaskRoute, len(iv.Routes))
		for task, route := range iv.Routes {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !consumeProjectionWork(1) {
				return ErrDenied
			}
			if !subset([]string{task}, policy.TaskIDs) {
				return ErrDenied
			}
			route.Checks = append([]ControllerCheckRoute(nil), route.Checks...)
			routes[task] = route
		}
		for _, action := range status.Actions {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !consumeProjectionWork(1) {
				return ErrDenied
			}
			if !controllerDriverActionCurrent(s, project, policy.TaskIDs, action) {
				return ErrDenied
			}
		}
		for _, ref := range status.Refs {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !consumeProjectionWork(1) {
				return ErrDenied
			}
			if err := a.validateCheckpointRef(s, CheckpointPolicy{ProjectID: project, TaskIDs: policy.TaskIDs}, ref); err != nil {
				return ErrDenied
			}
		}

		operations := make([]ControllerDriverOperation, 0, len(status.Actions)+len(status.Refs))
		byKey := make(map[string]int, len(status.Actions)+len(status.Refs))
		add := func(operation ControllerDriverOperation) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !consumeProjectionWork(1) || len(operations) >= controllerDriverMaxProjection || !validID(operation.OperationKey) || !validID(operation.TaskID) || !subset([]string{operation.TaskID}, policy.TaskIDs) {
				return ErrDenied
			}
			if idx, exists := byKey[operation.Kind+"\x00"+operation.OperationKey]; exists {
				merged, ok := mergeControllerDriverOperation(operations[idx], operation)
				if !ok {
					return ErrDenied
				}
				operations[idx] = merged
				return nil
			}
			byKey[operation.Kind+"\x00"+operation.OperationKey] = len(operations)
			operations = append(operations, operation)
			return nil
		}
		for _, action := range status.Actions {
			if err := ctx.Err(); err != nil {
				return err
			}
			if uint64(len(action.Verifications)) > controllerDriverMaxProjection-projectionWork {
				return ErrDenied
			}
			for _, operation := range controllerDriverActionOperations(s, iv, action) {
				if err := add(operation); err != nil {
					return err
				}
			}
		}
		for _, ref := range status.Refs {
			if err := ctx.Err(); err != nil {
				return err
			}
			for _, operation := range controllerDriverRefOperations(s, project, status, ref) {
				if err := add(operation); err != nil {
					return err
				}
			}
		}
		if len(operations) > controllerDriverMaxProjection {
			return ErrDenied
		}
		sort.Slice(operations, func(i, j int) bool {
			if operations[i].TaskID != operations[j].TaskID {
				return operations[i].TaskID < operations[j].TaskID
			}
			if operations[i].Kind != operations[j].Kind {
				return operations[i].Kind < operations[j].Kind
			}
			return operations[i].OperationKey < operations[j].OperationKey
		})

		actionsSeen, operationKeys, dispatchIDs := map[string]bool{}, map[string]bool{}, map[string]bool{}
		for _, old := range history {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !consumeProjectionWork(1) {
				return ErrDenied
			}
			if len(old.Actions) > controllerDriverMaxActions || len(old.Refs) > controllerDriverMaxProjection {
				return ErrDenied
			}
			for _, action := range old.Actions {
				if err := ctx.Err(); err != nil {
					return err
				}
				if !consumeProjectionWork(1) {
					return ErrDenied
				}
				if !validID(action.ID) || !validID(action.TaskID) || !subset([]string{action.TaskID}, policy.TaskIDs) {
					return ErrDenied
				}
				actionsSeen[action.ID] = true
				if action.DispatchID != "" {
					dispatchIDs[action.DispatchID] = true
				}
				keyCount := controllerDriverActionKeyCount(old, action)
				if !consumeProjectionWork(keyCount) {
					return ErrDenied
				}
				oldInvocation := iv
				oldInvocation.Status = old
				for _, operation := range controllerDriverActionOperations(s, oldInvocation, action) {
					if err := ctx.Err(); err != nil {
						return err
					}
					if !consumeProjectionWork(1) {
						return ErrDenied
					}
					if operation.State != "absent" {
						operationKeys[operation.OperationKey] = true
					}
				}
			}
			for _, ref := range old.Refs {
				if err := ctx.Err(); err != nil {
					return err
				}
				if !consumeProjectionWork(1) {
					return ErrDenied
				}
				if !subset([]string{ref.TaskID}, policy.TaskIDs) {
					return ErrDenied
				}
				if ref.DispatchID != "" {
					dispatchIDs[ref.DispatchID] = true
				}
				for _, operation := range controllerDriverRefOperations(s, project, old, ref) {
					if err := ctx.Err(); err != nil {
						return err
					}
					if !consumeProjectionWork(1) {
						return ErrDenied
					}
					if operation.State != "absent" {
						operationKeys[operation.OperationKey] = true
					}
				}
			}
			if uint64(len(actionsSeen)) > controllerDriverMaxProjection || uint64(len(operationKeys)) > controllerDriverMaxProjection {
				return ErrDenied
			}
		}
		for _, action := range status.Actions {
			if err := ctx.Err(); err != nil {
				return err
			}
			actionsSeen[action.ID] = true
			if action.DispatchID != "" {
				dispatchIDs[action.DispatchID] = true
			}
			keyCount := controllerDriverActionKeyCount(status, action)
			if !consumeProjectionWork(keyCount) {
				return ErrDenied
			}
		}
		for _, ref := range status.Refs {
			if err := ctx.Err(); err != nil {
				return err
			}
			if ref.DispatchID != "" {
				dispatchIDs[ref.DispatchID] = true
			}
			if !consumeProjectionWork(uint64(len(controllerDriverRefKeys(ref)))) {
				return ErrDenied
			}
		}
		for _, operation := range operations {
			if operation.State != "absent" {
				operationKeys[operation.OperationKey] = true
			}
		}
		// Controller sequences start at one, so the durable sequence carries the
		// historical Step/Stop count without a second transition ledger.
		transitionUnits := uint64(0)
		if status.Sequence > 1 {
			transitionUnits = status.Sequence - 1
		}
		intentOperationKeys := make([]string, 0, len(s.DispatchIntentOperations))
		for key := range s.DispatchIntentOperations {
			intentOperationKeys = append(intentOperationKeys, key)
		}
		sort.Strings(intentOperationKeys)
		retainedIntents := map[string]bool{}
		for _, key := range intentOperationKeys {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !consumeProjectionWork(1) {
				return ErrDenied
			}
			if dispatchIDs[s.DispatchIntentOperations[key].Intent.DispatchID] {
				operationKeys[key] = true
				retainedIntents[s.DispatchIntentOperations[key].Intent.DispatchID] = true
			}
		}
		lifecycleOperationKeys := make([]string, 0, len(s.LifecycleOperations))
		for key := range s.LifecycleOperations {
			lifecycleOperationKeys = append(lifecycleOperationKeys, key)
		}
		sort.Strings(lifecycleOperationKeys)
		for _, key := range lifecycleOperationKeys {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !consumeProjectionWork(1) {
				return ErrDenied
			}
			receipt := s.LifecycleOperations[key]
			dispatchID := ""
			if receipt.Observation != nil {
				dispatchID = receipt.Observation.Binding.DispatchID
			}
			if receipt.Cancellation != nil {
				dispatchID = receipt.Cancellation.Binding.DispatchID
			}
			if dispatchIDs[dispatchID] {
				operationKeys[key] = true
			}
		}
		historicalEffectUnits := uint64(len(operationKeys)) + transitionUnits + uint64(len(retainedIntents))
		// A retained first-use host intent records two pre-reserved host-effect
		// slots (worker registration and dispatch), regardless of where its reply
		// was lost. The original host key accounts for one; this supplies the
		// second without creating another authority record.
		cancellationDispatchIDs := make([]string, 0, len(s.LifecycleCancellations))
		for dispatchID := range s.LifecycleCancellations {
			cancellationDispatchIDs = append(cancellationDispatchIDs, dispatchID)
		}
		sort.Strings(cancellationDispatchIDs)
		for _, dispatchID := range cancellationDispatchIDs {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !consumeProjectionWork(1) {
				return ErrDenied
			}
			if dispatchIDs[dispatchID] && s.LifecycleCancellations[dispatchID].State != "" {
				historicalEffectUnits++ // one pre-reserved first-use host cancellation slot
			}
		}
		if uint64(len(actionsSeen)) > controllerDriverMaxProjection || historicalEffectUnits > controllerDriverMaxProjection {
			return ErrDenied
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		out = ControllerDriverView{
			AuthorityVersion: s.Version, Status: status, Policy: policy,
			CheckpointSequence: checkpointSequence, Routes: routes, Operations: operations,
			HistoricalActionCount: uint64(len(actionsSeen)), HistoricalOperationCount: historicalEffectUnits,
		}
		return nil
	})
	if err != nil {
		return ControllerDriverView{}, err
	}
	if err := ctx.Err(); err != nil {
		return ControllerDriverView{}, err
	}
	return out, nil
}

// AuthorizeControllerDriverEffect is a fresh, read-only gate for a producer or
// host boundary. It repeats the current controller, checkpoint, swarm-policy,
// deadline, source-binding and actor checks immediately before that boundary.
// It grants no authority and remains non-atomic with a later remote callback.
func (a *Authority) AuthorizeControllerDriverEffect(ctx context.Context, token, session, project, invocation string, expectedSequence uint64) error {
	return a.authorizeControllerDriverEffect(ctx, token, session, project, invocation, expectedSequence, ControllerDriverBoundaryStep, nil, "")
}

// AuthorizeControllerDriverActionEffect allows one exact current action at one
// named producer boundary. The original key is the stored dispatch key for
// dispatch/reconcile/observe/cancel-request and the exact stored producer key
// for verification/review. It grants no permission and is safe to repeat.
func (a *Authority) AuthorizeControllerDriverActionEffect(ctx context.Context, token, session, project, invocation string, expectedSequence uint64, action ControllerAction, boundary ControllerDriverEffectBoundary, originalOperationKey string) error {
	return a.authorizeControllerDriverEffect(ctx, token, session, project, invocation, expectedSequence, boundary, &action, originalOperationKey)
}

func (a *Authority) authorizeControllerDriverEffect(ctx context.Context, token, session, project, invocation string, expectedSequence uint64, boundary ControllerDriverEffectBoundary, action *ControllerAction, originalOperationKey string) error {
	if ctx == nil {
		return ErrDenied
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validID(project) || !validID(invocation) || token == "" || session == "" || expectedSequence == 0 || !controllerDriverBoundaryValid(boundary, action, originalOperationKey) {
		return ErrDenied
	}
	err := a.transaction(false, false, func(s *state) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		iv, ok := s.ControllerInvocations[invocation]
		if s.Version < 16 || !ok || iv.Status.ID != invocation || iv.Status.ProjectID != project || iv.Status.ControllerSessionID != session || iv.Status.Sequence != expectedSequence || iv.Policy.ProjectID != project {
			return ErrDenied
		}
		if err := controllerScope(s, token, session, project, "controller.step", iv.Policy.TaskIDs); err != nil {
			return err
		}
		if !controllerLive(s, session) || controllerCurrent(s, iv, time.Now()) != nil || iv.Status.Terminal || iv.Status.Historical || !controllerFreshActions(s, iv) {
			return ErrDenied
		}
		if action != nil {
			found := false
			for _, current := range iv.Status.Actions {
				if current.ID == action.ID && reflect.DeepEqual(current, *action) {
					found = true
					break
				}
			}
			if !found {
				return ErrDenied
			}
		}
		checkpointPolicy, ok := s.CheckpointPolicies[project]
		if !ok {
			return ErrDenied
		}
		checkpoint, err := a.reconcileCheckpoint(s, checkpointPolicy, CheckpointStatus{
			ID: iv.Status.CheckpointID, ProjectID: project,
			InvocationID: controllerCycleID(iv.Status), Refs: iv.Status.Refs,
		}, token, session)
		if err != nil {
			return err
		}
		currentCheckpointPolicy, checkpointCurrent := s.CheckpointPolicies[project]
		if !checkpointCurrent || !reflect.DeepEqual(currentCheckpointPolicy, checkpointPolicy) || controllerCurrent(s, iv, time.Now()) != nil || !controllerFreshActions(s, iv) || !a.controllerDriverCheckpointSafeForBoundary(s, checkpoint, iv, action, boundary, originalOperationKey) {
			return ErrDenied
		}
		if action != nil && !a.controllerDriverActionBoundaryCurrent(s, iv, *action, boundary, originalOperationKey) {
			return ErrDenied
		}
		// The action boundary above performs the last slow role/source observer.
		// Repeat pure authorization guards after it, immediately before return.
		currentCheckpointPolicy, checkpointCurrent = s.CheckpointPolicies[project]
		if !checkpointCurrent || !reflect.DeepEqual(currentCheckpointPolicy, checkpointPolicy) || controllerScope(s, token, session, project, "controller.step", iv.Policy.TaskIDs) != nil || !controllerLive(s, session) || controllerCurrent(s, iv, time.Now()) != nil || !controllerFreshActions(s, iv) || !time.Now().Before(iv.Status.Deadline) {
			return ErrDenied
		}
		return ctx.Err()
	})
	if err != nil {
		return err
	}
	return ctx.Err()
}

func controllerDriverBoundaryValid(boundary ControllerDriverEffectBoundary, action *ControllerAction, key string) bool {
	if boundary == ControllerDriverBoundaryStep {
		return action == nil && key == ""
	}
	if action == nil || !validID(action.ID) || !validID(key) {
		return false
	}
	switch boundary {
	case ControllerDriverBoundaryDispatch, ControllerDriverBoundaryReconcile, ControllerDriverBoundaryObserve, ControllerDriverBoundaryCancelRequest:
		return validID(action.DispatchID)
	case ControllerDriverBoundaryVerify:
		return action.Kind == "verification"
	case ControllerDriverBoundaryReview:
		return action.Kind == "independent_review"
	default:
		return false
	}
}

func (a *Authority) controllerDriverActionBoundaryCurrent(s *state, iv controllerInvocation, action ControllerAction, boundary ControllerDriverEffectBoundary, key string) bool {
	if boundary == ControllerDriverBoundaryVerify {
		for _, request := range action.Verifications {
			if request.OperationKey == key && controllerDriverVerificationState(s, action, request, &ControllerDriverOperation{}) != "unknown" {
				return request.OperationKey == key
			}
		}
		if len(action.Verifications) == 0 && action.Verification.OperationKey == key {
			return controllerDriverVerificationState(s, action, action.Verification, &ControllerDriverOperation{}) != "unknown"
		}
		return false
	}
	if boundary == ControllerDriverBoundaryReview {
		return action.OperationKey == key && action.Kind == "independent_review"
	}
	intent, present := s.DispatchIntents[action.DispatchID]
	if boundary == ControllerDriverBoundaryDispatch {
		return action.Kind == "host_dispatch" && intentCurrentForDriver(s, iv, action, intent, present) && a.controllerDriverCurrentDispatchSource(s, iv, action) && action.OperationKey == key && intent.OperationKey == key
	}
	if boundary == ControllerDriverBoundaryObserve {
		return (action.Kind == "host_dispatch" || action.Kind == "observe") && intentCurrentForDriver(s, iv, action, intent, present) && a.controllerDriverCurrentDispatchSource(s, iv, action) && intent.OperationKey == key
	}
	if boundary == ControllerDriverBoundaryReconcile || boundary == ControllerDriverBoundaryCancelRequest {
		return intentCurrentForDriver(s, iv, action, intent, present) && a.controllerDriverCurrentDispatchSource(s, iv, action) && intent.OperationKey == key
	}
	return false
}

func (a *Authority) controllerDriverCurrentDispatchSource(s *state, iv controllerInvocation, action ControllerAction) bool {
	d, ok := s.Dispatches[action.DispatchID]
	if !ok || d.ProjectID != iv.Status.ProjectID || d.Reservation.TaskID != action.TaskID || d.Reservation.Fence != action.ReservationFence {
		return false
	}
	if a.currentRoleRegistryMatches(s, d) != nil {
		return false
	}
	if !preflightBoundCurrent(s, d) || !budgetDispatchCurrent(s, d) {
		return false
	}
	route := iv.Routes[action.TaskID]
	if !dispatchBoundActor(s, route.BridgeSession, d.ProjectID, "dispatch.correlate", d) || !dispatchBoundActor(s, route.WorkerSession, d.ProjectID, "lease.acquire", d) {
		return false
	}
	return d.HostSlot == nil || validateHostSlotDispatchState(s, d, time.Now())
}

func intentCurrentForDriver(s *state, iv controllerInvocation, action ControllerAction, intent DispatchIntent, present bool) bool {
	if !present || intent.DispatchID != action.DispatchID || intent.OperationKey == "" || intent.ControllerSessionID != iv.Status.ControllerSessionID || intent.Reservation.Reservation.TaskID != action.TaskID || intent.Reservation.Reservation.Fence != action.ReservationFence {
		return false
	}
	if action.IntentID != "" && action.IntentID != intent.ID || action.OperationKey != "" && action.Kind == "host_dispatch" && action.OperationKey != intent.OperationKey {
		return false
	}
	return true
}

func (a *Authority) controllerDriverCheckpointSafeForBoundary(s *state, status CheckpointStatus, iv controllerInvocation, action *ControllerAction, boundary ControllerDriverEffectBoundary, key string) bool {
	if boundary == ControllerDriverBoundaryVerify && (action == nil || !controllerDriverActionHasVerificationKey(*action, key)) {
		return false
	}
	for _, finding := range status.Findings {
		if finding.Class == "policy_refusal" {
			return false
		}
		if finding.Class == "verification_failed" {
			if boundary == ControllerDriverBoundaryStep && a.controllerDriverStepExpectedFinding(s, iv, finding) {
				continue
			}
			if boundary == ControllerDriverBoundaryStep && iv.Status.Phase == "correction" {
				matched := false
				for _, current := range iv.Status.Actions {
					if current.Kind == "correction" && finding.TaskID == current.TaskID && containsString(current.EvidenceIDs, finding.Reference) {
						matched = true
						break
					}
				}
				if matched {
					continue
				}
			}
			return false
		}
		if finding.Knowledge != "unknown" {
			continue
		}
		if action == nil {
			if boundary == ControllerDriverBoundaryStep && a.controllerDriverStepExpectedFinding(s, iv, finding) {
				continue
			}
			return false
		}
		switch {
		case (boundary == ControllerDriverBoundaryDispatch || boundary == ControllerDriverBoundaryReconcile || boundary == ControllerDriverBoundaryObserve) && (finding.Class == "unknown_dispatch_ack" || finding.Class == "stale_host_observation"):
			if finding.TaskID == action.TaskID && action.Kind == "host_dispatch" && finding.Reference == action.IntentID && (boundary != ControllerDriverBoundaryDispatch || key == action.OperationKey) || a.controllerDriverExpectedHostFinding(s, iv, finding) {
				continue
			}
			return false
		case boundary == ControllerDriverBoundaryReconcile && action.Kind == "verification" && finding.Class == "verification_unknown" && finding.TaskID == action.TaskID && controllerDriverActionHasVerificationKey(*action, finding.Reference):
			continue
		case boundary == ControllerDriverBoundaryReconcile && action.Kind == "independent_review" && finding.Class == "review_unknown" && finding.TaskID == action.TaskID && finding.Reference == action.OperationKey:
			continue
		case boundary == ControllerDriverBoundaryVerify && action.Kind == "verification" && finding.Class == "verification_unknown" && a.controllerDriverExpectedVerificationFinding(s, iv, finding.TaskID, finding.Reference):
			continue
		case boundary == ControllerDriverBoundaryReview && action.Kind == "independent_review" && finding.Class == "review_unknown" && a.controllerDriverExpectedReviewFinding(s, iv, finding.TaskID, finding.Reference):
			continue
		case boundary == ControllerDriverBoundaryReview && finding.TaskID != action.TaskID && a.controllerDriverExpectedUnstartedOwnerGate(s, iv, finding):
			continue
		default:
			return false
		}
	}
	return true
}

func (a *Authority) controllerDriverExpectedHostFinding(s *state, iv controllerInvocation, finding CheckpointFinding) bool {
	if s == nil {
		return false
	}
	for _, current := range iv.Status.Actions {
		if current.Kind != "host_dispatch" || current.TaskID != finding.TaskID || current.IntentID == "" || current.IntentID != finding.Reference || current.OperationKey == "" {
			continue
		}
		intent, ok := s.DispatchIntents[current.DispatchID]
		if !ok || (intent.State != "dispatch_requested" && intent.State != "host_correlated" && intent.State != "acknowledged") {
			continue
		}
		if a.controllerDriverActionBoundaryCurrent(s, iv, current, ControllerDriverBoundaryReconcile, current.OperationKey) {
			return true
		}
	}
	return false
}

func (a *Authority) controllerDriverExpectedVerificationFinding(s *state, iv controllerInvocation, taskID, key string) bool {
	if s == nil || taskID == "" || key == "" {
		return false
	}
	for _, current := range iv.Status.Actions {
		if current.Kind != "verification" || current.TaskID != taskID {
			continue
		}
		for _, request := range controllerDriverVerificationRequests(current) {
			if request.OperationKey == key && controllerDriverVerificationState(s, current, request, &ControllerDriverOperation{}) == "absent" && a.controllerDriverActionBoundaryCurrent(s, iv, current, ControllerDriverBoundaryVerify, key) {
				return true
			}
		}
	}
	return false
}

func (a *Authority) controllerDriverExpectedReviewFinding(s *state, iv controllerInvocation, taskID, key string) bool {
	if s == nil || taskID == "" || key == "" {
		return false
	}
	for _, current := range iv.Status.Actions {
		if current.Kind == "independent_review" && current.TaskID == taskID && current.OperationKey == key {
			if _, exists := s.Reviews[key]; !exists && a.controllerDriverActionBoundaryCurrent(s, iv, current, ControllerDriverBoundaryReview, key) {
				return true
			}
		}
	}
	return false
}

// controllerDriverExpectedUnstartedOwnerGate permits an independent action to
// proceed while a sibling is waiting at an exact owner-only gate that has not
// started. Any existing operation record, including pending or unknown Git or
// canonical effects, remains a blocker.
func (a *Authority) controllerDriverExpectedUnstartedOwnerGate(s *state, iv controllerInvocation, finding CheckpointFinding) bool {
	if s == nil || finding.TaskID == "" || finding.Reference == "" {
		return false
	}
	ref, ok := controllerDriverUnstartedOwnerGateRef(iv, finding)
	if !ok {
		return false
	}
	for _, action := range iv.Status.Actions {
		if action.ID != finding.Reference || action.TaskID != finding.TaskID || action.DispatchID != ref.DispatchID || action.ReviewID != ref.ReviewID || action.EvidenceID != "" && action.EvidenceID != ref.EvidenceID || action.IntentID != "" && action.IntentID != ref.IntentID || action.OperationKey != finding.Reference || action.AttemptID == "" || action.LeaseID == "" || action.RevisionID == "" || action.Fence == 0 {
			continue
		}
		if finding.Class == "pending_local_integration" && action.Kind == "integration_checkpoint" && ref.IntegrationOperationID == finding.Reference {
			if _, exists := s.NativeIntegrationOperations[finding.Reference]; exists {
				return false
			}
			return a.controllerDriverOwnerActionCurrent(s, iv, action, ref)
		}
		if finding.Class == "pending_canonical_acceptance" && action.Kind == "canonical_acceptance" && ref.AcceptanceOperationID == finding.Reference {
			if _, exists := s.NativeAcceptanceOperations[finding.Reference]; exists {
				return false
			}
			integration, exists := s.NativeIntegrationOperations[ref.IntegrationOperationID]
			if !exists || !validNativeIntegrationOutcome(integration) || !reflect.DeepEqual(s.NativeIntegrationHistory[integration.Policy.TaskID+":"+integration.Policy.Revision], integration.Policy) {
				return false
			}
			candidate := integration.Input.Candidate
			if integration.Input.OperationKey != ref.IntegrationOperationID || candidate.ProjectID != iv.Status.ProjectID || candidate.TaskID != action.TaskID || candidate.ReviewID != action.ReviewID || candidate.AttemptID != action.AttemptID || candidate.LeaseID != action.LeaseID || candidate.RevisionID != action.RevisionID || candidate.ExpectedFence != action.Fence {
				return false
			}
			return a.controllerDriverOwnerActionCurrent(s, iv, action, ref)
		}
	}
	return false
}

// controllerDriverUnstartedOwnerGateRef joins the current acceptance reference
// to its exact earlier integration reference. Acceptance refs intentionally
// carry only operation keys, so retained host intent and review evidence must
// come from the same invocation's already-bound integration lineage.
func controllerDriverUnstartedOwnerGateRef(iv controllerInvocation, finding CheckpointFinding) (CheckpointRefs, bool) {
	var target *CheckpointRefs
	for i := range iv.Status.Refs {
		candidate := &iv.Status.Refs[i]
		matches := finding.Class == "pending_local_integration" && candidate.IntegrationOperationID == finding.Reference || finding.Class == "pending_canonical_acceptance" && candidate.AcceptanceOperationID == finding.Reference
		if candidate.TaskID == finding.TaskID && matches {
			if target != nil {
				return CheckpointRefs{}, false
			}
			target = candidate
		}
	}
	if target == nil || target.DispatchID == "" || target.ReviewID == "" || target.IntegrationOperationID == "" {
		return CheckpointRefs{}, false
	}
	if finding.Class == "pending_local_integration" {
		if target.IntegrationOperationID != finding.Reference || target.IntentID == "" || target.EvidenceID == "" {
			return CheckpointRefs{}, false
		}
		return *target, true
	}
	if finding.Class != "pending_canonical_acceptance" || target.AcceptanceOperationID != finding.Reference {
		return CheckpointRefs{}, false
	}
	var lineage *CheckpointRefs
	for i := range iv.Status.Refs {
		candidate := &iv.Status.Refs[i]
		if candidate.TaskID != target.TaskID || candidate.DispatchID != target.DispatchID || candidate.ReviewID != target.ReviewID || candidate.IntegrationOperationID != target.IntegrationOperationID || candidate.IntentID == "" || candidate.EvidenceID == "" {
			continue
		}
		if lineage != nil {
			return CheckpointRefs{}, false
		}
		lineage = candidate
	}
	if lineage == nil || target.IntentID != "" && target.IntentID != lineage.IntentID || target.EvidenceID != "" && target.EvidenceID != lineage.EvidenceID {
		return CheckpointRefs{}, false
	}
	joined := *target
	joined.IntentID = lineage.IntentID
	joined.EvidenceID = lineage.EvidenceID
	return joined, true
}

func (a *Authority) controllerDriverOwnerActionCurrent(s *state, iv controllerInvocation, action ControllerAction, ref CheckpointRefs) bool {
	dispatch, exists := s.Dispatches[action.DispatchID]
	intent, hasIntent := s.DispatchIntents[action.DispatchID]
	if !exists || !hasIntent || dispatch.ProjectID != iv.Status.ProjectID || dispatch.Reservation.TaskID != action.TaskID || dispatch.Reservation.Fence != action.ReservationFence || intent.ID != ref.IntentID || action.IntentID != "" && action.IntentID != intent.ID || intent.DispatchID != action.DispatchID || intent.State != "acknowledged" || intent.Lease == nil || intent.Lease.ID != action.LeaseID || intent.Lease.Fence != action.Fence || intent.ControllerSessionID != iv.Status.ControllerSessionID {
		return false
	}
	attempt, err := reviewAttempt(s, action.AttemptID)
	if err != nil || attempt.ProjectID != iv.Status.ProjectID || attempt.Request.TaskID != action.TaskID || attempt.SessionID != intent.WorkerSessionID {
		return false
	}
	var revision *AttemptRevision
	for i := range s.Revisions[attempt.ID] {
		candidate := &s.Revisions[attempt.ID][i]
		if candidate.ID == action.RevisionID {
			if revision != nil {
				return false
			}
			revision = candidate
		}
	}
	review, hasReview := s.Reviews[action.ReviewID]
	if revision == nil || !hasReview || review.ID != ref.ReviewID || review.Request.AttemptID != attempt.ID || review.Request.LeaseID != action.LeaseID || review.Request.RevisionID != action.RevisionID || review.Request.ExpectedFence != action.Fence || review.Request.Outcome != "approve" || review.Revision.ID != action.RevisionID || review.Revision.Fence != action.Fence {
		return false
	}
	if revision.LeaseID != action.LeaseID || revision.Fence != action.Fence || !controllerDriverEvidenceIDsEqual(review.Request.EvidenceIDs, action.EvidenceIDs) {
		return false
	}
	boundEvidence := false
	for _, evidenceID := range action.EvidenceIDs {
		if evidenceID == ref.EvidenceID {
			boundEvidence = true
			break
		}
	}
	if !boundEvidence {
		return false
	}
	currentRevision, err := currentRevision(s, attempt, action.RevisionID, action.LeaseID, action.Fence)
	if err != nil || !reflect.DeepEqual(currentRevision, *revision) || a.currentRoleRegistryMatches(s, dispatch) != nil {
		return false
	}
	if !preflightBoundCurrent(s, dispatch) || !budgetDispatchCurrent(s, dispatch) {
		return false
	}
	route := iv.Routes[action.TaskID]
	if route.RunnerSession != action.ActorID && action.Kind != "integration_checkpoint" && action.Kind != "canonical_acceptance" {
		return false
	}
	if !dispatchBoundActor(s, route.BridgeSession, dispatch.ProjectID, "dispatch.correlate", dispatch) || !dispatchBoundActor(s, route.WorkerSession, dispatch.ProjectID, "lease.acquire", dispatch) {
		return false
	}
	return dispatch.HostSlot == nil || validateHostSlotDispatchState(s, dispatch, time.Now())
}

// controllerDriverEvidenceIDsEqual compares the review's canonical sorted set
// with the action's producer order while rejecting malformed or duplicate IDs.
func controllerDriverEvidenceIDsEqual(left, right []string) bool {
	if len(left) == 0 || len(left) != len(right) {
		return false
	}
	leftSet := make(map[string]struct{}, len(left))
	for _, id := range left {
		if !validID(id) {
			return false
		}
		if _, duplicate := leftSet[id]; duplicate {
			return false
		}
		leftSet[id] = struct{}{}
	}
	rightSet := make(map[string]struct{}, len(right))
	for _, id := range right {
		if !validID(id) {
			return false
		}
		if _, duplicate := rightSet[id]; duplicate {
			return false
		}
		rightSet[id] = struct{}{}
	}
	for id := range leftSet {
		if _, ok := rightSet[id]; !ok {
			return false
		}
	}
	return true
}

func (a *Authority) controllerDriverStepExpectedFinding(s *state, iv controllerInvocation, finding CheckpointFinding) bool {
	for _, action := range iv.Status.Actions {
		if finding.TaskID != action.TaskID {
			continue
		}
		switch finding.Class {
		case "stale_host_observation", "unknown_dispatch_ack":
			if action.Kind == "host_dispatch" && finding.Reference == action.IntentID && a.controllerDriverExpectedHostFinding(s, iv, finding) {
				return true
			}
		case "verification_unknown", "verification_failed":
			if action.Kind == "verification" {
				if finding.Class == "verification_unknown" && a.controllerDriverExpectedVerificationFinding(s, iv, finding.TaskID, finding.Reference) {
					return true
				}
				for _, request := range controllerDriverVerificationRequests(action) {
					if finding.Class == "verification_failed" && controllerDriverFailedEvidenceMatches(s, action, request, finding.Reference) {
						return true
					}
				}
			}
		case "review_unknown":
			if action.Kind == "independent_review" && finding.Reference == action.OperationKey && a.controllerDriverExpectedReviewFinding(s, iv, finding.TaskID, finding.Reference) {
				return true
			}
		case "pending_local_integration", "pending_canonical_acceptance":
			if a.controllerDriverExpectedUnstartedOwnerGate(s, iv, finding) {
				return true
			}
		}
	}
	return false
}

func controllerDriverVerificationRequests(action ControllerAction) []VerificationRequest {
	requests := append([]VerificationRequest(nil), action.Verifications...)
	if len(requests) == 0 && action.Verification.OperationKey != "" {
		requests = append(requests, action.Verification)
	}
	return requests
}

func controllerDriverFailedEvidenceMatches(s *state, action ControllerAction, request VerificationRequest, evidenceID string) bool {
	if s == nil || evidenceID == "" || request.OperationKey == "" {
		return false
	}
	op, ok := s.Operations[request.OperationKey]
	if !ok || op.Pending || op.Evidence == nil || op.Evidence.ID != evidenceID || op.Evidence.ExitCode == 0 {
		return false
	}
	grant, ok := s.Grants[request.GrantID]
	if !ok || op.Digest != identity("run:"+grant.CommandDigest, action.ActorID, request) {
		return false
	}
	return controllerEvidenceCurrent(s, *op.Evidence, request, action.ActorID, op)
}

func controllerDriverActionHasVerificationKey(action ControllerAction, key string) bool {
	if key == "" {
		return false
	}
	for _, request := range action.Verifications {
		if request.OperationKey == key {
			return true
		}
	}
	return len(action.Verifications) == 0 && action.Verification.OperationKey == key
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

// AuthorizeControllerDriverCancellation authorizes only the single host
// cancellation whose withdrawal is already durably recorded. It is narrower
// than the general effect gate because withdrawal retires the target worker;
// all other current actions still have to pass fresh source checks.
func (a *Authority) AuthorizeControllerDriverCancellation(ctx context.Context, token, session, project, invocation string, expectedSequence uint64, request CancelLifecycleRequest) error {
	if ctx == nil {
		return ErrDenied
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validID(project) || !validID(invocation) || token == "" || session == "" || expectedSequence == 0 || !validID(request.OperationKey) {
		return ErrDenied
	}
	err := a.transaction(false, false, func(s *state) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		iv, ok := s.ControllerInvocations[invocation]
		if s.Version < 16 || !ok || iv.Status.ID != invocation || iv.Status.ProjectID != project || iv.Status.ControllerSessionID != session || iv.Status.Sequence != expectedSequence || iv.Status.Terminal || iv.Status.Historical || !time.Now().Before(iv.Status.Deadline) {
			return ErrDenied
		}
		if err := controllerScope(s, token, session, project, "controller.step", iv.Policy.TaskIDs); err != nil {
			return err
		}
		policy, current := s.ControllerPolicies[project]
		swarm, swarmCurrent := s.SwarmPolicies[project]
		if !current || !swarmCurrent || !reflect.DeepEqual(policy, iv.Policy) || !time.Now().Before(policy.ExpiresAt) || swarm.Harness != policy.Harness || swarm.CapabilityDigest != policy.CapabilityDigest || !controllerLive(s, session) || controllerCurrent(s, iv, time.Now()) != nil {
			return ErrDenied
		}
		var target *ControllerAction
		remaining := iv
		remaining.Status.Actions = make([]ControllerAction, 0, len(iv.Status.Actions))
		for i := range iv.Status.Actions {
			action := iv.Status.Actions[i]
			if action.DispatchID == request.Binding.DispatchID {
				if target != nil {
					return ErrDenied
				}
				copy := action
				target = &copy
				continue
			}
			remaining.Status.Actions = append(remaining.Status.Actions, action)
		}
		if target == nil || target.DispatchID != request.Binding.DispatchID || target.TaskID != request.Binding.TaskID || target.IntentID != "" && target.IntentID != request.Binding.IntentID || target.ReservationFence != 0 && target.ReservationFence != request.Binding.ReservationFence || target.LeaseID != "" && target.LeaseID != request.Binding.LeaseID || target.AttemptID != "" && target.AttemptID != request.Binding.AttemptID || !controllerFreshActions(s, remaining) {
			return ErrDenied
		}
		intent, ok := s.DispatchIntents[request.Binding.DispatchID]
		if !ok || intent.ID != request.Binding.IntentID || (intent.State != "acknowledged" && intent.State != "host_correlated" && intent.State != "dispatch_requested") || (intent.State == "acknowledged" && intent.Lease == nil) || intent.HostAgentID == "" || intent.Reservation.Reservation.TaskID != request.Binding.TaskID || intent.Reservation.Reservation.Fence != request.Binding.ReservationFence || intent.ControllerSessionID != session {
			return ErrDenied
		}
		if _, err := lifecycleIntent(s, request.Binding); err != nil {
			return err
		}
		cancellation, ok := s.LifecycleCancellations[request.Binding.DispatchID]
		lifecyclePolicy, policyOK := s.LifecyclePolicies[project]
		if !ok || !policyOK || !lifecyclePolicy.CancellationEnabled || cancellation.OperationKey != request.OperationKey || !reflect.DeepEqual(cancellation.Binding, request.Binding) || cancellation.RequestedBy != session || cancellation.Sequence != 1 || cancellation.State != "requested" || cancellation.PolicyRevision != lifecyclePolicy.Revision || cancellation.Channel != "owner-manual-native-host" {
			return ErrDenied
		}
		receipt, ok := s.LifecycleOperations[request.OperationKey]
		if !ok || receipt.Cancellation == nil || !reflect.DeepEqual(*receipt.Cancellation, cancellation) || receipt.Digest != identity("lifecycle.cancel", session, request) {
			return ErrDenied
		}
		checkpointPolicy, ok := s.CheckpointPolicies[project]
		if !ok {
			return ErrDenied
		}
		checkpoint, err := a.reconcileCheckpoint(s, checkpointPolicy, CheckpointStatus{ID: iv.Status.CheckpointID, ProjectID: project, InvocationID: controllerCycleID(iv.Status), Refs: iv.Status.Refs}, token, session)
		if err != nil {
			return err
		}
		currentCheckpointPolicy, checkpointCurrent := s.CheckpointPolicies[project]
		if !checkpointCurrent || !reflect.DeepEqual(currentCheckpointPolicy, checkpointPolicy) || controllerCurrent(s, iv, time.Now()) != nil || !controllerFreshActions(s, remaining) || !controllerDriverCheckpointSafeForCancellation(s, iv, *target, checkpoint, request.Binding.DispatchID, request.Binding.IntentID) {
			return ErrDenied
		}
		return ctx.Err()
	})
	if err != nil {
		return err
	}
	return ctx.Err()
}

func controllerDriverCheckpointSafeForCancellation(s *state, iv controllerInvocation, target ControllerAction, status CheckpointStatus, dispatchID, intentID string) bool {
	for _, finding := range status.Findings {
		if finding.Knowledge != "unknown" && finding.Class != "policy_refusal" && finding.Class != "verification_failed" {
			continue
		}
		if finding.Class == "policy_refusal" || finding.Class == "verification_failed" {
			return false
		}
		if finding.Reference == dispatchID || finding.Reference == intentID || controllerDriverCancellationVerificationFinding(s, iv, target, finding, dispatchID) {
			continue
		}
		return false
	}
	return true
}

func controllerDriverCancellationVerificationFinding(s *state, iv controllerInvocation, target ControllerAction, finding CheckpointFinding, dispatchID string) bool {
	if s == nil || target.Kind != "verification" || target.DispatchID != dispatchID || target.TaskID != finding.TaskID || finding.Class != "verification_unknown" {
		return false
	}
	route, ok := iv.Routes[target.TaskID]
	if !ok || route.RunnerSession != target.ActorID {
		return false
	}
	for _, request := range controllerDriverVerificationRequests(target) {
		if request.OperationKey != finding.Reference || request.AttemptID != target.AttemptID || request.LeaseID != target.LeaseID || request.RevisionID != target.RevisionID || request.ExpectedFence != target.Fence || request.WorkerSession != route.WorkerSession {
			continue
		}
		grantMatches := request.GrantID == route.VerificationGrantID
		for _, check := range route.Checks {
			if check.CheckID == request.CheckID && check.GrantID == request.GrantID {
				grantMatches = true
				break
			}
		}
		if !grantMatches {
			continue
		}
		op, present := s.Operations[request.OperationKey]
		grant, grantPresent := s.Grants[request.GrantID]
		if !present || !grantPresent || op.Digest != identity("run:"+grant.CommandDigest, target.ActorID, request) {
			continue
		}
		state := controllerDriverVerificationState(s, target, request, &ControllerDriverOperation{})
		if state == "pending" || state == "unknown" {
			return true
		}
	}
	return false
}

func mergeControllerDriverOperation(left, right ControllerDriverOperation) (ControllerDriverOperation, bool) {
	if left.Kind != right.Kind || left.OperationKey != right.OperationKey || left.TaskID != right.TaskID || left.DispatchID != right.DispatchID || left.State != right.State {
		return ControllerDriverOperation{}, false
	}
	for _, pair := range [][2]string{{left.ActionID, right.ActionID}, {left.IntentID, right.IntentID}, {left.AttemptID, right.AttemptID}, {left.LeaseID, right.LeaseID}, {left.RevisionID, right.RevisionID}, {left.ActorID, right.ActorID}, {left.EvidenceID, right.EvidenceID}, {left.ReviewID, right.ReviewID}, {left.RecordID, right.RecordID}, {left.InputDigest, right.InputDigest}, {left.OutcomeDigest, right.OutcomeDigest}} {
		if pair[0] != "" && pair[1] != "" && pair[0] != pair[1] {
			return ControllerDriverOperation{}, false
		}
	}
	for _, pair := range [][2]uint64{{left.Fence, right.Fence}, {left.ReservationFence, right.ReservationFence}} {
		if pair[0] != 0 && pair[1] != 0 && pair[0] != pair[1] {
			return ControllerDriverOperation{}, false
		}
	}
	if left.Verification != nil && right.Verification != nil && !reflect.DeepEqual(*left.Verification, *right.Verification) {
		return ControllerDriverOperation{}, false
	}
	if left.ActionID == "" {
		left.ActionID = right.ActionID
	}
	if left.IntentID == "" {
		left.IntentID = right.IntentID
	}
	if left.AttemptID == "" {
		left.AttemptID = right.AttemptID
	}
	if left.LeaseID == "" {
		left.LeaseID = right.LeaseID
	}
	if left.RevisionID == "" {
		left.RevisionID = right.RevisionID
	}
	if left.ActorID == "" {
		left.ActorID = right.ActorID
	}
	if left.EvidenceID == "" {
		left.EvidenceID = right.EvidenceID
	}
	if left.ReviewID == "" {
		left.ReviewID = right.ReviewID
	}
	if left.RecordID == "" {
		left.RecordID = right.RecordID
	}
	if left.InputDigest == "" {
		left.InputDigest = right.InputDigest
	}
	if left.OutcomeDigest == "" {
		left.OutcomeDigest = right.OutcomeDigest
	}
	if left.Fence == 0 {
		left.Fence = right.Fence
	}
	if left.ReservationFence == 0 {
		left.ReservationFence = right.ReservationFence
	}
	if left.Verification == nil {
		left.Verification = right.Verification
	}
	return left, true
}

func cloneControllerDriverStatus(in ControllerStatus) ControllerStatus {
	out := in
	out.Actions = append([]ControllerAction(nil), in.Actions...)
	for i := range out.Actions {
		action := &out.Actions[i]
		action.Verifications = append([]VerificationRequest(nil), action.Verifications...)
		action.EvidenceIDs = append([]string(nil), action.EvidenceIDs...)
		if action.RequiredSet != nil {
			required := *action.RequiredSet
			required.Checks = append([]RequiredCheck(nil), action.RequiredSet.Checks...)
			action.RequiredSet = &required
		}
	}
	out.Refs = append([]CheckpointRefs(nil), in.Refs...)
	return out
}

func controllerDriverActionCurrent(s *state, project string, tasks []string, action ControllerAction) bool {
	if !validID(action.ID) || !validID(action.TaskID) || !subset([]string{action.TaskID}, tasks) || action.DispatchID == "" {
		return false
	}
	d, ok := s.Dispatches[action.DispatchID]
	return ok && d.ProjectID == project && d.Reservation.TaskID == action.TaskID && d.Reservation.Fence == action.ReservationFence && action.ReservationFence > 0
}

func controllerDriverActionKeyCount(status ControllerStatus, action ControllerAction) uint64 {
	count := uint64(0)
	if action.OperationKey != "" {
		count++
	}
	count += uint64(len(action.Verifications))
	if action.Verification.OperationKey != "" {
		count++
	}
	if action.Kind == "dispatch" && action.DispatchID != "" {
		count++
	}
	return count
}

func controllerDriverRefKeys(ref CheckpointRefs) []string {
	keys := []string{}
	for _, key := range []string{ref.VerificationKey, ref.IntegrationOperationID, ref.AcceptanceOperationID} {
		if key != "" {
			keys = append(keys, key)
		}
	}
	return keys
}

func controllerDriverActionOperations(s *state, iv controllerInvocation, action ControllerAction) []ControllerDriverOperation {
	base := ControllerDriverOperation{ActionID: action.ID, TaskID: action.TaskID, DispatchID: action.DispatchID, IntentID: action.IntentID, AttemptID: action.AttemptID, LeaseID: action.LeaseID, RevisionID: action.RevisionID, ActorID: action.ActorID, Fence: action.Fence, ReservationFence: action.ReservationFence}
	out := []ControllerDriverOperation{}
	switch action.Kind {
	case "dispatch", "host_dispatch", "correction":
		d, ok := s.Dispatches[action.DispatchID]
		if !ok {
			return nil
		}
		intent, present := s.DispatchIntents[action.DispatchID]
		key := action.OperationKey
		if present {
			key = intent.OperationKey
		}
		if key == "" {
			key = controllerKey(iv.Status, "dispatch", action.DispatchID)
		}
		base.Kind, base.OperationKey, base.State = "dispatch", key, "absent"
		if !present && action.Kind == "host_dispatch" {
			base.State = "unknown"
		}
		if present {
			base.IntentID = intent.ID
			base.ActorID = intent.BridgeSessionID
			switch {
			case intent.State == "acknowledged" && intent.Lease != nil:
				base.State = "completed"
			case intent.State == "dispatch_requested" || intent.State == "host_correlated":
				base.State = "pending"
			default:
				base.State = "unknown"
			}
			if intent.ID != action.IntentID && action.IntentID != "" || intent.OperationKey != key || intent.DispatchID != action.DispatchID || intent.Reservation.Reservation.Fence != d.Reservation.Fence || intent.Reservation.Reservation.TaskID != action.TaskID {
				base.State = "unknown"
			}
		}
		out = append(out, base)
	case "verification":
		requests := append([]VerificationRequest(nil), action.Verifications...)
		if len(requests) == 0 && action.Verification.OperationKey != "" {
			requests = append(requests, action.Verification)
		}
		for _, request := range requests {
			operation := base
			operation.Kind, operation.OperationKey, operation.Verification = "verification", request.OperationKey, cloneVerificationRequest(request)
			operation.State = controllerDriverVerificationState(s, action, request, &operation)
			out = append(out, operation)
		}
	case "independent_review":
		operation := base
		operation.Kind, operation.OperationKey, operation.State = "review", action.OperationKey, "absent"
		if op, present := s.Operations[action.OperationKey]; present {
			if op.Review == nil {
				operation.State = "unknown"
			} else {
				review := *op.Review
				if review.Request.OperationKey != action.OperationKey || review.Request.AttemptID != action.AttemptID || review.Request.LeaseID != action.LeaseID || review.Request.RevisionID != action.RevisionID || review.Request.ExpectedFence != action.Fence || review.IssuerSession != action.ActorID {
					operation.State = "unknown"
				} else {
					operation.State = "completed"
					operation.ReviewID, operation.RecordID = review.ID, review.ID
					operation.InputDigest = op.Digest
					operation.OutcomeDigest = identity("controller-driver-review", "", review)
				}
			}
		}
		out = append(out, operation)
	case "integration_checkpoint", "canonical_acceptance", "accepted":
		if action.Kind == "integration_checkpoint" {
			out = append(out, controllerDriverIntegrationOperation(s, iv.Status.ProjectID, base, action.OperationKey)...)
		} else {
			out = append(out, controllerDriverAcceptanceOperation(s, iv.Status.ProjectID, base, action.OperationKey)...)
		}
	}
	if cancellation, ok := s.LifecycleCancellations[action.DispatchID]; ok {
		operation := base
		operation.Kind, operation.OperationKey = "cancellation", cancellation.OperationKey
		operation.IntentID = cancellation.Binding.IntentID
		operation.State = "pending"
		if cancellation.State == "acknowledged" || cancellation.State == "cancelled" || cancellation.State == "completed" {
			operation.State = "completed"
		} else if cancellation.State != "requested" {
			operation.State = "unknown"
		}
		operation.InputDigest = s.LifecycleOperations[cancellation.OperationKey].Digest
		operation.OutcomeDigest = cancellation.EvidenceDigest
		out = append(out, operation)
	}
	return out
}

func cloneVerificationRequest(in VerificationRequest) *VerificationRequest {
	copy := in
	return &copy
}

func controllerDriverVerificationState(s *state, action ControllerAction, request VerificationRequest, operation *ControllerDriverOperation) string {
	if request.OperationKey == "" || request.OperationKey != action.OperationKey && len(action.Verifications) == 0 {
		return "unknown"
	}
	if request.AttemptID != action.AttemptID || request.LeaseID != action.LeaseID || request.RevisionID != action.RevisionID || request.ExpectedFence != action.Fence || request.WorkerSession == "" || request.CheckID == "" || request.GrantID == "" {
		return "unknown"
	}
	op, present := s.Operations[request.OperationKey]
	if !present {
		return "absent"
	}
	grant, ok := s.Grants[request.GrantID]
	if !ok || op.Digest != identity("run:"+grant.CommandDigest, action.ActorID, request) {
		return "unknown"
	}
	if op.Pending {
		return "pending"
	}
	if interrupted := verificationInterruption(op, action.ActorID, request); interrupted != nil {
		operation.InputDigest = op.Digest
		operation.OutcomeDigest = interrupted.ResultDigest
		return "interrupted"
	}
	if op.Evidence == nil || !controllerEvidenceCurrent(s, *op.Evidence, request, action.ActorID, op) {
		return "unknown"
	}
	operation.EvidenceID = op.Evidence.ID
	operation.RecordID = op.Evidence.ID
	operation.InputDigest = op.Digest
	operation.OutcomeDigest = op.Evidence.OutputDigest
	return "completed"
}

func controllerDriverIntegrationOperation(s *state, project string, base ControllerDriverOperation, key string) []ControllerDriverOperation {
	if key == "" {
		return nil
	}
	base.Kind, base.OperationKey, base.State = "integration", key, "absent"
	if rec, ok := s.NativeIntegrationOperations[key]; ok {
		input := rec.Input.Candidate
		base, ok = controllerDriverBindCandidate(base, input)
		if !ok || rec.OperationKey != key || rec.Input.OperationKey != key || rec.InputDigest != identity("native-integration-input", "", rec.Input) || rec.Input.PolicyRevision != rec.Policy.Revision || input.ProjectID != project {
			base.State = "unknown"
		} else {
			base.RecordID, base.InputDigest, base.OutcomeDigest = rec.OperationKey, rec.InputDigest, rec.ReceiptDigest
			switch rec.State {
			case "integrated":
				if validNativeIntegrationOutcome(rec) {
					base.State = "completed"
				} else {
					base.State = "unknown"
				}
			case "pending", "prepared", "integrating":
				base.State = "pending"
			default:
				base.State = "unknown"
			}
		}
	}
	return []ControllerDriverOperation{base}
}

func controllerDriverAcceptanceOperation(s *state, project string, base ControllerDriverOperation, key string) []ControllerDriverOperation {
	if key == "" {
		return nil
	}
	base.Kind, base.OperationKey, base.State = "acceptance", key, "absent"
	rec, present := s.NativeAcceptanceOperations[key]
	if !present {
		return []ControllerDriverOperation{base}
	}
	input := rec.Candidate.Input
	base, bound := controllerDriverBindCandidate(base, input)
	candidateInput := input
	candidateInput.OperationKey = key
	terminal, retired := s.TaskTerminals[base.TaskID]
	outcome, observed := s.NativePolicyOutcomes[key]
	if !bound || !acceptanceRecordMatchesInput(rec, candidateInput) || input.ProjectID != project || input.WorkspaceID != s.Binding.Workspace || input.InstallationID != s.Binding.Installation {
		base.State = "unknown"
	} else {
		base.RecordID, base.InputDigest, base.OutcomeDigest = rec.OperationKey, rec.InputDigest, rec.CanonicalOutcomeDigest
		if rec.State == "pending" || rec.State == "closing" {
			base.State = "pending"
		} else if rec.State == "closed" && !rec.ClosedAt.IsZero() && retired && reflect.DeepEqual(terminal, rec) && validControllerCanonicalReceipt(rec) && validDigest(rec.CanonicalOutcomeDigest) && observed && outcome.OperationKey == key && outcome.State == "closed" && outcome.ExpectedOutcomeDigest == rec.CanonicalOutcomeDigest && outcome.ObservedOutcomeDigest == rec.CanonicalOutcomeDigest && !outcome.ObservedAt.IsZero() {
			base.State = "completed"
		} else {
			base.State = "unknown"
		}
	}
	return []ControllerDriverOperation{base}
}

func controllerDriverBindCandidate(base ControllerDriverOperation, input NativeAcceptanceInput) (ControllerDriverOperation, bool) {
	if base.TaskID != input.TaskID || (base.AttemptID != "" && base.AttemptID != input.AttemptID) || (base.LeaseID != "" && base.LeaseID != input.LeaseID) || (base.RevisionID != "" && base.RevisionID != input.RevisionID) || (base.Fence != 0 && base.Fence != input.ExpectedFence) {
		return base, false
	}
	base.AttemptID, base.LeaseID, base.RevisionID, base.Fence = input.AttemptID, input.LeaseID, input.RevisionID, input.ExpectedFence
	return base, input.AttemptID != "" && input.LeaseID != "" && input.RevisionID != "" && input.ExpectedFence > 0
}

func controllerDriverRefOperations(s *state, project string, status ControllerStatus, ref CheckpointRefs) []ControllerDriverOperation {
	base := ControllerDriverOperation{TaskID: ref.TaskID, DispatchID: ref.DispatchID, IntentID: ref.IntentID, Fence: ref.Verification.ExpectedFence}
	out := []ControllerDriverOperation{}
	if ref.DispatchID != "" {
		intent, present := s.DispatchIntents[ref.DispatchID]
		op := base
		op.Kind, op.OperationKey, op.State = "dispatch", controllerKey(status, "dispatch", ref.DispatchID), "unknown"
		if present {
			op.OperationKey = intent.OperationKey
			op.IntentID, op.ActorID, op.ReservationFence = intent.ID, intent.BridgeSessionID, intent.Reservation.Reservation.Fence
			if intent.Reservation.ProjectID != project || intent.Reservation.Reservation.TaskID != ref.TaskID || intent.DispatchID != ref.DispatchID || (ref.IntentID != "" && ref.IntentID != intent.ID) {
				op.State = "unknown"
			} else if intent.State == "acknowledged" && intent.Lease != nil {
				op.State = "completed"
			} else if intent.State == "dispatch_requested" || intent.State == "host_correlated" {
				op.State = "pending"
			}
		} else {
			for _, action := range status.Actions {
				if action.DispatchID == ref.DispatchID && action.Kind == "dispatch" {
					op.State, op.ActionID, op.ReservationFence = "absent", action.ID, action.ReservationFence
					break
				}
			}
		}
		out = append(out, op)
	}
	if ref.VerificationKey != "" {
		op := base
		op.Kind, op.OperationKey, op.Verification = "verification", ref.VerificationKey, cloneVerificationRequest(ref.Verification)
		op.AttemptID, op.LeaseID, op.RevisionID, op.Fence, op.ActorID = ref.Verification.AttemptID, ref.Verification.LeaseID, ref.Verification.RevisionID, ref.Verification.ExpectedFence, ref.RunnerSession
		op.State = "unknown"
		if !refMatchesVerification(ref) {
			op.State = "unknown"
		} else if raw, ok := s.Operations[ref.VerificationKey]; !ok {
			op.State = "absent"
		} else {
			grant := s.Grants[ref.Verification.GrantID]
			if raw.Digest != identity("run:"+grant.CommandDigest, ref.RunnerSession, ref.Verification) {
				op.State = "unknown"
			} else if raw.Pending {
				op.State = "pending"
			} else if interruption := verificationInterruption(raw, ref.RunnerSession, ref.Verification); interruption != nil {
				op.State, op.InputDigest, op.OutcomeDigest = "interrupted", raw.Digest, interruption.ResultDigest
			} else if raw.Evidence != nil && controllerEvidenceCurrent(s, *raw.Evidence, ref.Verification, ref.RunnerSession, raw) {
				op.State, op.RecordID, op.EvidenceID, op.InputDigest, op.OutcomeDigest = "completed", raw.Evidence.ID, raw.Evidence.ID, raw.Digest, raw.Evidence.OutputDigest
			}
		}
		out = append(out, op)
	}
	if ref.ReviewID != "" {
		if review, ok := s.Reviews[ref.ReviewID]; ok {
			op := base
			op.Kind, op.OperationKey, op.State = "review", review.Request.OperationKey, "unknown"
			op.AttemptID, op.LeaseID, op.RevisionID, op.Fence = review.Request.AttemptID, review.Request.LeaseID, review.Request.RevisionID, review.Request.ExpectedFence
			op.ActorID, op.ReviewID, op.RecordID = review.IssuerSession, review.ID, review.ID
			at, attemptErr := reviewAttempt(s, review.Request.AttemptID)
			if attemptErr == nil && at.ProjectID == project && at.Request.TaskID == ref.TaskID && review.Request.OperationKey != "" && review.IssuerSession != "" {
				op.State, op.OutcomeDigest = "completed", identity("controller-driver-review", "", review)
			}
			if raw, exists := s.Operations[op.OperationKey]; !exists || raw.Review == nil || raw.Review.ID != review.ID {
				op.State = "unknown"
			} else {
				op.InputDigest = raw.Digest
			}
			out = append(out, op)
		}
	}
	if ref.IntegrationOperationID != "" {
		out = append(out, controllerDriverIntegrationOperation(s, project, base, ref.IntegrationOperationID)...)
	}
	if ref.AcceptanceOperationID != "" {
		out = append(out, controllerDriverAcceptanceOperation(s, project, base, ref.AcceptanceOperationID)...)
	}
	return out
}

func refMatchesVerification(ref CheckpointRefs) bool {
	return ref.VerificationKey == ref.Verification.OperationKey && ref.RunnerSession != "" && ref.Verification.WorkerSession != ""
}
