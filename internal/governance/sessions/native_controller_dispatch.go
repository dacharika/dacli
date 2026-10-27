package sessions

import (
	"reflect"
	"sync/atomic"
	"time"
)

// ControllerHostDispatchReference identifies one stored instruction. It is
// descriptive data only; it carries no permission to launch a host action.
type ControllerHostDispatchReference struct {
	ProjectID, InvocationID, CheckpointID, StepOperationKey  string
	ActionID, DispatchID, IntentID, OriginalOperationKey     string
	ControllerSequence, CheckpointSequence, ReservationFence uint64
}

// ControllerHostDispatchPermit is an opaque, in-process, one-use handoff.
// Copies share the same private atomic state and cannot be serialized.
type ControllerHostDispatchPermit struct {
	state *controllerHostDispatchPermitState
}

type controllerHostDispatchPermitState struct {
	spent             atomic.Bool
	reference         ControllerHostDispatchReference
	controllerSession string
	authorityPath     string
	binding           Binding
	sourceDigest      string
}

func (p ControllerHostDispatchPermit) Reference() ControllerHostDispatchReference {
	if p.state == nil {
		return ControllerHostDispatchReference{}
	}
	return p.state.reference
}

func (ControllerHostDispatchPermit) String() string   { return "<controller-host-dispatch-permit>" }
func (ControllerHostDispatchPermit) GoString() string { return "<controller-host-dispatch-permit>" }

func (ControllerHostDispatchPermit) MarshalJSON() ([]byte, error) {
	return nil, ErrDenied
}

func (p *ControllerHostDispatchPermit) UnmarshalJSON([]byte) error {
	if p != nil {
		p.state = nil
	}
	return ErrDenied
}

// StepControllerForDriver advances the normal controller transition machine
// and returns a permit only for the one new host intent committed by this call.
func (a *Authority) StepControllerForDriver(token, session string, request ControllerStepRequest) (ControllerStatus, ControllerHostDispatchPermit, error) {
	status, created, err := a.advanceControllerForDriver(token, session, request, false)
	if err != nil || created == nil || status.Historical || status.Terminal {
		return status, ControllerHostDispatchPermit{}, err
	}
	permit, err := a.mintControllerHostDispatchPermit(token, session, request, status, created)
	return status, permit, err
}

func (a *Authority) mintControllerHostDispatchPermit(token, session string, request ControllerStepRequest, status ControllerStatus, created *controllerDispatchCandidate) (ControllerHostDispatchPermit, error) {
	if created == nil || created.CheckpointSequence == 0 {
		return ControllerHostDispatchPermit{}, ErrDenied
	}
	ref := ControllerHostDispatchReference{
		ProjectID: status.ProjectID, InvocationID: status.ID, CheckpointID: status.CheckpointID,
		StepOperationKey: request.OperationKey, ActionID: created.Action.ID, DispatchID: created.Intent.DispatchID,
		IntentID: created.Intent.ID, OriginalOperationKey: created.Intent.OperationKey,
		ControllerSequence: status.Sequence, CheckpointSequence: created.CheckpointSequence,
		ReservationFence: created.Intent.Reservation.Reservation.Fence,
	}
	var sourceDigest string
	err := a.transaction(false, false, func(s *state) error {
		iv, ok := s.ControllerInvocations[ref.InvocationID]
		if !ok || !controllerDispatchReferenceCurrent(s, iv, ref, created.Action, created.Intent) || !reflect.DeepEqual(iv.Routes[created.Action.TaskID], created.Route) || !iv.Status.HostActionAuthorized || iv.Status.Phase != "await_host" {
			return ErrDenied
		}
		if e := controllerScope(s, token, session, ref.ProjectID, "controller.step", iv.Policy.TaskIDs); e != nil {
			return e
		}
		if controllerCurrent(s, iv, time.Now()) != nil || !controllerFreshActions(s, iv) {
			return ErrDenied
		}
		sourceDigest = controllerDispatchSourceDigest(s, iv, created.Action, created.Route, created.Intent)
		if sourceDigest == "" {
			return ErrDenied
		}
		return nil
	})
	if err != nil {
		return ControllerHostDispatchPermit{}, err
	}
	return ControllerHostDispatchPermit{state: &controllerHostDispatchPermitState{
		reference: ref, controllerSession: session, authorityPath: a.path, binding: a.binding, sourceDigest: sourceDigest,
	}}, nil
}

// ConsumeControllerHostDispatch spends the permit before checking any state.
// A stale or refused attempt is intentionally not recoverable by replay.
func (a *Authority) ConsumeControllerHostDispatch(token, session string, permit ControllerHostDispatchPermit) (DispatchIntent, error) {
	if permit.state == nil || !permit.state.spent.CompareAndSwap(false, true) {
		return DispatchIntent{}, ErrDenied
	}
	sealed := permit.state
	ref := sealed.reference
	if a.path != sealed.authorityPath || a.binding != sealed.binding || session == "" || session != sealed.controllerSession {
		return DispatchIntent{}, ErrDenied
	}
	var result DispatchIntent
	err := a.transaction(false, false, func(s *state) error {
		iv, ok := s.ControllerInvocations[ref.InvocationID]
		if !ok || iv.Status.ControllerSessionID != session || !controllerDispatchReferenceShape(iv, ref) {
			return ErrDenied
		}
		if !controllerDispatchCommitCurrent(s, iv, ref) {
			return ErrDenied
		}
		if e := controllerScope(s, token, session, ref.ProjectID, "controller.step", iv.Policy.TaskIDs); e != nil {
			return e
		}
		if controllerCurrent(s, iv, time.Now()) != nil || !controllerFreshActions(s, iv) || iv.Status.Terminal || iv.Status.Historical || !iv.Status.HostActionAuthorized || iv.Status.Phase != "await_host" {
			return ErrDenied
		}
		action, ok := controllerDispatchAction(iv.Status, ref)
		if !ok {
			return ErrDenied
		}
		intent, ok := s.DispatchIntents[ref.DispatchID]
		if !ok || intent.State != "dispatch_requested" || intent.Sequence != 1 || intent.Lease != nil || intent.ID != ref.IntentID || intent.OperationKey != ref.OriginalOperationKey || intent.ControllerSessionID != session || intent.DispatchID != ref.DispatchID || intent.Reservation.Reservation.Fence != ref.ReservationFence {
			return ErrDenied
		}
		if receipt, ok := s.DispatchIntentOperations[ref.OriginalOperationKey]; !ok || receipt.Intent.ID != intent.ID || receipt.Intent.OperationKey != intent.OperationKey {
			return ErrDenied
		}
		route := iv.Routes[action.TaskID]
		dispatch, ok := s.Dispatches[ref.DispatchID]
		if !ok || dispatch.Converted || dispatch.ControllerSessionID != session || dispatch.ProjectID != ref.ProjectID || dispatch.Reservation.Fence != ref.ReservationFence || route.BridgeSession != intent.BridgeSessionID || route.WorkerSession != intent.WorkerSessionID || route.PromptSpecDigest != intent.PromptSpecDigest {
			return ErrDenied
		}
		if source := controllerDispatchSourceDigest(s, iv, action, route, intent); source == "" || source != sealed.sourceDigest {
			return ErrDenied
		}
		admitted, e := a.admitDispatch(s, token, session, AdmitDispatchRequest{DispatchID: ref.DispatchID, ExpectedFence: ref.ReservationFence})
		if e != nil || !reflect.DeepEqual(admitted, dispatch) || !reflect.DeepEqual(intent.Reservation, dispatch) {
			return ErrDenied
		}
		if a.currentRoleRegistryMatches(s, dispatch) != nil {
			return ErrDenied
		}
		// Final checks follow admission observers. They may refresh role or host
		// capacity facts, but cannot move execution under the authority lock.
		if e = controllerScope(s, token, session, ref.ProjectID, "controller.step", iv.Policy.TaskIDs); e != nil || controllerCurrent(s, iv, time.Now()) != nil || !controllerFreshActions(s, iv) || !time.Now().Before(iv.Status.Deadline) || !preflightBoundCurrent(s, dispatch) || !budgetDispatchCurrent(s, dispatch) || !validateHostSlotDispatchState(s, dispatch, time.Now()) {
			return ErrDenied
		}
		if !controllerDispatchCommitCurrent(s, iv, ref) || controllerDispatchSourceDigest(s, iv, action, route, intent) != sealed.sourceDigest {
			return ErrDenied
		}
		result = intent
		return nil
	})
	if err != nil {
		return DispatchIntent{}, ErrDenied
	}
	return result, nil
}

func controllerDispatchReferenceCurrent(s *state, iv controllerInvocation, ref ControllerHostDispatchReference, action ControllerAction, intent DispatchIntent) bool {
	if !controllerDispatchReferenceShape(iv, ref) || !controllerDispatchCommitCurrent(s, iv, ref) || action.ID != ref.ActionID || action.Kind != "host_dispatch" || action.DispatchID != ref.DispatchID || action.IntentID != ref.IntentID || action.OperationKey != ref.OriginalOperationKey || action.ReservationFence != ref.ReservationFence || intent.ID != ref.IntentID || intent.DispatchID != ref.DispatchID || intent.OperationKey != ref.OriginalOperationKey || intent.Reservation.Reservation.Fence != ref.ReservationFence {
		return false
	}
	current, ok := s.DispatchIntents[ref.DispatchID]
	return ok && reflect.DeepEqual(current, intent)
}

func controllerDispatchCommitCurrent(s *state, iv controllerInvocation, ref ControllerHostDispatchReference) bool {
	step, ok := s.ControllerOperations[ref.StepOperationKey]
	if !ok || step.Status.ID != ref.InvocationID || step.Status.Sequence != ref.ControllerSequence || step.Status.Historical || step.Status.Terminal || !step.Status.HostActionAuthorized || step.Status.Phase != "await_host" {
		return false
	}
	if _, ok := controllerDispatchAction(step.Status, ref); !ok {
		return false
	}
	history := s.Checkpoints[ref.CheckpointID]
	if len(history) == 0 {
		return false
	}
	checkpoint := history[len(history)-1]
	checkpointPolicy, ok := s.CheckpointPolicies[ref.ProjectID]
	return ok && checkpoint.ID == ref.CheckpointID && checkpoint.ProjectID == ref.ProjectID && checkpoint.Sequence == ref.CheckpointSequence && checkpoint.InvocationID == controllerCycleID(iv.Status) && checkpoint.PolicyRevision == checkpointPolicy.Revision
}

func controllerDispatchReferenceShape(iv controllerInvocation, ref ControllerHostDispatchReference) bool {
	st := iv.Status
	return st.ProjectID == ref.ProjectID && st.ID == ref.InvocationID && st.ControllerSessionID != "" && st.CheckpointID == ref.CheckpointID && st.Sequence == ref.ControllerSequence && st.CheckpointSequence > 0 && ref.CheckpointSequence > 0 && ref.ReservationFence > 0
}

func controllerDispatchAction(st ControllerStatus, ref ControllerHostDispatchReference) (ControllerAction, bool) {
	for _, action := range st.Actions {
		if action.ID == ref.ActionID && action.DispatchID == ref.DispatchID && action.IntentID == ref.IntentID && action.OperationKey == ref.OriginalOperationKey && action.ReservationFence == ref.ReservationFence && action.Kind == "host_dispatch" {
			return action, true
		}
	}
	return ControllerAction{}, false
}

func controllerDispatchSourceDigest(s *state, iv controllerInvocation, action ControllerAction, route ControllerTaskRoute, intent DispatchIntent) string {
	dispatch, ok := s.Dispatches[intent.DispatchID]
	if !ok {
		return ""
	}
	history := s.Checkpoints[iv.Status.CheckpointID]
	if len(history) == 0 {
		return ""
	}
	type source struct {
		Invocation controllerInvocation
		Action     ControllerAction
		Route      ControllerTaskRoute
		Dispatch   DispatchReservation
		Intent     DispatchIntent
		Checkpoint CheckpointStatus
	}
	return identity("controller.host-dispatch.source", iv.Status.ControllerSessionID, source{Invocation: iv, Action: action, Route: route, Dispatch: dispatch, Intent: intent, Checkpoint: history[len(history)-1]})
}
