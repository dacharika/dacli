package application

import (
	"context"
	"reflect"
	"time"

	"github.com/mlnomadpy/dacli/internal/governance/driver"
	"github.com/mlnomadpy/dacli/internal/governance/executor"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
)

type nativeControllerDriverPorts struct{ selection *NativeControllerDriver }

func (p *nativeControllerDriverPorts) Read(ctx context.Context, request driver.Request) (sessions.ControllerDriverView, error) {
	if err := p.ready(ctx); err != nil {
		return sessions.ControllerDriverView{}, err
	}
	a, token, err := p.selection.controller.swarm.authenticate()
	if err != nil {
		return sessions.ControllerDriverView{}, err
	}
	view, err := a.ReadControllerDriverView(ctx, token, p.selection.controller.swarm.connection.SessionID, request.ProjectID, request.InvocationID)
	if err != nil {
		return sessions.ControllerDriverView{}, err
	}
	if err = p.ready(ctx); err != nil {
		return sessions.ControllerDriverView{}, err
	}
	if view.Status.CheckpointID != request.CheckpointID {
		return sessions.ControllerDriverView{}, sessions.ErrDenied
	}
	return view, nil
}

func (p *nativeControllerDriverPorts) Step(ctx context.Context, request sessions.ControllerStepRequest) (sessions.ControllerStatus, sessions.ControllerHostDispatchPermit, error) {
	if err := p.ready(ctx); err != nil {
		return sessions.ControllerStatus{}, sessions.ControllerHostDispatchPermit{}, err
	}
	a, token, err := p.selection.controller.swarm.authenticate()
	if err != nil {
		return sessions.ControllerStatus{}, sessions.ControllerHostDispatchPermit{}, err
	}
	view, err := a.ReadControllerDriverView(ctx, token, p.selection.controller.swarm.connection.SessionID, request.ProjectID, request.InvocationID)
	if err != nil {
		return sessions.ControllerStatus{}, sessions.ControllerHostDispatchPermit{}, err
	}
	if view.Status.CheckpointID != p.selection.profile.CheckpointID || view.Status.Sequence != request.ExpectedSequence {
		return sessions.ControllerStatus{}, sessions.ControllerHostDispatchPermit{}, sessions.ErrDenied
	}
	needsNewDispatch := len(view.Status.Actions) == 0
	for _, action := range view.Status.Actions {
		if action.Kind == "dispatch" {
			needsNewDispatch = true
			break
		}
	}
	if needsNewDispatch {
		if err = p.requireDispatchCapability(ctx); err != nil {
			return sessions.ControllerStatus{}, sessions.ControllerHostDispatchPermit{}, err
		}
	}
	if err = p.ready(ctx); err != nil {
		return sessions.ControllerStatus{}, sessions.ControllerHostDispatchPermit{}, err
	}
	if len(view.Status.Actions) > 0 {
		// The empty-action path is a new-wave reservation: StepControllerForDriver
		// performs its own fresh checkpoint, wave, actor, and reservation checks.
		// Keep the extra original-operation guard for every retained action.
		if err = a.AuthorizeControllerDriverEffect(ctx, token, p.selection.controller.swarm.connection.SessionID, request.ProjectID, request.InvocationID, request.ExpectedSequence); err != nil {
			return sessions.ControllerStatus{}, sessions.ControllerHostDispatchPermit{}, err
		}
	}
	if err = p.ready(ctx); err != nil {
		return sessions.ControllerStatus{}, sessions.ControllerHostDispatchPermit{}, err
	}
	status, permit, err := a.StepControllerForDriver(token, p.selection.controller.swarm.connection.SessionID, request)
	if err == nil && ctx.Err() != nil {
		return status, sessions.ControllerHostDispatchPermit{}, executor.ErrUncertain
	}
	return status, permit, err
}

func (p *nativeControllerDriverPorts) Stop(ctx context.Context, request sessions.ControllerStepRequest) (sessions.ControllerStatus, error) {
	if err := p.ready(ctx); err != nil {
		return sessions.ControllerStatus{}, err
	}
	a, token, err := p.selection.controller.swarm.authenticate()
	if err != nil {
		return sessions.ControllerStatus{}, err
	}
	if err = p.ready(ctx); err != nil {
		return sessions.ControllerStatus{}, err
	}
	status, err := a.StopController(token, p.selection.controller.swarm.connection.SessionID, request)
	if err == nil && ctx.Err() != nil {
		return status, executor.ErrUncertain
	}
	return status, err
}

func (p *nativeControllerDriverPorts) Reconcile(ctx context.Context, action sessions.ControllerAction, budget driver.EffectBudget) ([]driver.Receipt, error) {
	if err := p.ready(ctx); err != nil {
		return nil, err
	}
	task, err := p.selection.freshTask(action.TaskID)
	if err != nil {
		return nil, err
	}
	reservationOnlyDispatch := action.Kind == "dispatch" && action.IntentID == "" && action.OperationKey == ""
	if action.DispatchID != "" && !reservationOnlyDispatch {
		intent, intentErr := p.controllerIntent(action.DispatchID)
		if intentErr != nil {
			return nil, intentErr
		}
		if cancellation, present, cancelErr := p.reconcileCancellation(ctx, task, action, intent, budget); cancelErr != nil {
			return nil, cancelErr
		} else if present {
			return []driver.Receipt{cancellation}, nil
		}
	}
	switch action.Kind {
	case "dispatch", "host_dispatch", "observe":
		// The initial dispatch action reserves work but has not yet written a
		// durable host intent. Do not turn that expected absence into a controller
		// intent lookup; the next bounded Step creates the original key.
		if action.Kind == "dispatch" && action.IntentID == "" && action.OperationKey == "" {
			return nil, nil
		}
		intent, err := p.controllerIntent(action.DispatchID)
		if err != nil {
			return nil, err
		}
		if err = p.ready(ctx); err != nil {
			return nil, err
		}
		req := dispatchForIntent(task.selection, action, intent)
		if intent.BridgeSessionID != task.bridge.connection.SessionID || intent.WorkerSessionID != task.worker.connection.SessionID {
			return nil, sessions.ErrDenied
		}
		result, lookupErr := p.selection.coordinator.LookupControllerDispatch(ctx, req, task.selection.WorkerEnrollment)
		if lookupErr != nil {
			return nil, lookupErr
		}
		receipt := driver.Receipt{Kind: "dispatch", OperationKey: intent.OperationKey, State: intent.State, Host: &result, Intent: &intent}
		if (intent.State == "dispatch_requested" || intent.State == "host_correlated") && result.HostExecutionID != "" {
			intent, err = p.correlateAndAcknowledge(ctx, task, action, req, result, intent, budget)
			if err != nil {
				return []driver.Receipt{receipt}, err
			}
			receipt.Intent = &intent
			receipt.State = intent.State
		}
		receipts := []driver.Receipt{receipt}
		return receipts, nil
	case "verification":
		access, err := task.runner.Authenticate()
		if err != nil {
			return nil, err
		}
		requests := append([]sessions.VerificationRequest(nil), action.Verifications...)
		if len(requests) == 0 && action.Verification.OperationKey != "" {
			requests = append(requests, action.Verification)
		}
		out := make([]driver.Receipt, 0, len(requests))
		for _, request := range requests {
			if err := p.ready(ctx); err != nil {
				return out, err
			}
			lookup, lookupErr := access.Lookup(NativeVerificationRequest{OperationKey: request.OperationKey, WorkerSession: request.WorkerSession, AttemptID: request.AttemptID, LeaseID: request.LeaseID, RevisionID: request.RevisionID, CheckID: request.CheckID, ExpectedFence: request.ExpectedFence})
			if lookupErr != nil {
				return out, lookupErr
			}
			copy := lookup
			out = append(out, driver.Receipt{Kind: "verification", OperationKey: request.OperationKey, State: string(lookup.State), Verification: &copy})
		}
		if err := p.ready(ctx); err != nil {
			return out, err
		}
		return out, nil
	case "independent_review":
		access, err := task.reviewer.Authenticate()
		if err != nil {
			return nil, err
		}
		if action.ReviewID == "" {
			return nil, executor.ErrUncertain
		}
		eligibility, err := access.Evaluate(action.ReviewID)
		if err != nil {
			return nil, err
		}
		if err = p.ready(ctx); err != nil {
			return nil, err
		}
		state := string(eligibility.Reason)
		return []driver.Receipt{{Kind: "review", OperationKey: action.OperationKey, State: state}}, nil
	case "cancellation":
		intent, err := p.controllerIntent(action.DispatchID)
		if err != nil {
			return nil, err
		}
		status, err := task.bridge.Status(action.DispatchID)
		if err != nil {
			return nil, err
		}
		if status.Cancellation != nil && status.Cancellation.State == "requested" {
			if contextBackendMissing(p.selection.backend) {
				return nil, executor.ErrUncertain
			}
			request, requestErr := cancellationForIntent(task.selection, action, intent, p.selection.profile, status.Binding)
			if requestErr != nil {
				return nil, requestErr
			}
			state, lookupErr := p.selection.backend.LookupCancellationContext(ctx, request)
			if lookupErr != nil || !validCancellationState(state) {
				return nil, executor.ErrUncertain
			}
			if err = p.acknowledgeCancellation(ctx, task, intent, request, status.Cancellation.Sequence, state, budget); err != nil {
				return nil, err
			}
			status, err = task.bridge.Status(action.DispatchID)
			if err != nil {
				return nil, err
			}
		}
		if status.Cancellation == nil {
			return nil, executor.ErrUncertain
		}
		return []driver.Receipt{{Kind: "cancellation", OperationKey: status.Cancellation.OperationKey, State: status.Cancellation.State, Lifecycle: &status}}, nil
	default:
		return nil, driver.ErrUnsupported
	}
}

func (p *nativeControllerDriverPorts) Dispatch(ctx context.Context, permit sessions.ControllerHostDispatchPermit, action sessions.ControllerAction, budget driver.EffectBudget) ([]driver.Receipt, error) {
	if err := p.ready(ctx); err != nil {
		return nil, err
	}
	task, err := p.selection.freshTask(action.TaskID)
	if err != nil {
		return nil, err
	}
	view, _, _, err := p.readCurrentActionView(ctx, action)
	if err != nil {
		return nil, err
	}
	route := view.Routes[action.TaskID]
	if route.BridgeSession != task.bridge.connection.SessionID || route.WorkerSession != task.worker.connection.SessionID {
		return nil, sessions.ErrDenied
	}
	if err = p.authorizeCurrentActionAtBoundary(ctx, action, permit.Reference().ControllerSequence, sessions.ControllerDriverBoundaryDispatch, permit.Reference().OriginalOperationKey); err != nil {
		return nil, err
	}
	workerAuthority, workerToken, err := task.worker.authenticate()
	if err != nil {
		return nil, err
	}
	worker, err := executor.AuthenticateWorker(workerAuthority, workerToken, task.selection.WorkerEnrollment, time.Now())
	if err != nil {
		return nil, err
	}
	if worker.Claim().Attempt != task.selection.WorkerEnrollment.Binding.Attempt || (action.AttemptID != "" && action.AttemptID != worker.Claim().Attempt) {
		return nil, sessions.ErrDenied
	}
	ref := permit.Reference()
	req := dispatchForReference(task.selection, action, ref)
	req.BridgeSession = task.bridge.connection.SessionID
	req.Binding.Attempt = worker.Claim().Attempt
	if err = p.ready(ctx); err != nil {
		return nil, err
	}
	if err = p.authorizeCurrentActionAtBoundary(ctx, action, permit.Reference().ControllerSequence, sessions.ControllerDriverBoundaryDispatch, ref.OriginalOperationKey); err != nil {
		return nil, err
	}
	guard := func(ctx context.Context) error {
		if err := p.ready(ctx); err != nil {
			return err
		}
		if err := p.selection.verifyPinnedFiles(); err != nil {
			return sessions.ErrDenied
		}
		if err := p.authorizeCurrentActionAtBoundary(ctx, action, ref.ControllerSequence, sessions.ControllerDriverBoundaryDispatch, ref.OriginalOperationKey); err != nil {
			return err
		}
		return p.ready(ctx)
	}
	// Registration and dispatch are distinct host boundaries. Reserve both
	// before the coordinator can consume the one-use permit or call the host.
	if err = reserveDriverEffect(ctx, budget); err != nil {
		return nil, err
	}
	if err = reserveDriverEffect(ctx, budget); err != nil {
		return nil, err
	}
	result, err := p.selection.coordinator.DispatchControllerOnce(ctx, permit, req, worker, guard)
	if err != nil {
		return nil, err
	}
	if err = p.ready(ctx); err != nil {
		return nil, executor.ErrUncertain
	}
	intent, err := p.controllerIntent(action.DispatchID)
	if err != nil {
		return nil, err
	}
	intent, err = p.correlateAndAcknowledge(ctx, task, action, req, result, intent, budget)
	if err != nil {
		return []driver.Receipt{{Kind: "dispatch", OperationKey: req.OperationKey, State: intent.State, Host: &result, Intent: &intent}}, err
	}
	return []driver.Receipt{{Kind: "dispatch", OperationKey: req.OperationKey, State: intent.State, Host: &result, Intent: &intent}}, nil
}

func (p *nativeControllerDriverPorts) Observe(ctx context.Context, action sessions.ControllerAction, budget driver.EffectBudget) ([]driver.Receipt, error) {
	if err := p.ready(ctx); err != nil {
		return nil, err
	}
	if contextBackendMissing(p.selection.backend) {
		return nil, executor.ErrUnsupportedContextBackend
	}
	caps, err := p.selection.backend.CapabilitiesContext(ctx)
	if err != nil || ctx.Err() != nil || caps.Version != executor.ContractVersion || caps.Observe.State != executor.Supported {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, executor.ErrUnsupportedContextBackend
	}
	if err = p.ready(ctx); err != nil {
		return nil, err
	}
	task, err := p.selection.freshTask(action.TaskID)
	if err != nil {
		return nil, err
	}
	intent, err := p.controllerIntent(action.DispatchID)
	if err != nil {
		return nil, err
	}
	req := dispatchForIntent(task.selection, action, intent)
	host, err := p.selection.coordinator.LookupControllerDispatch(ctx, req, task.selection.WorkerEnrollment)
	if err != nil {
		return nil, err
	}
	if host.HostExecutionID == "" {
		return nil, executor.ErrUncertain
	}
	status, err := task.bridge.Status(action.DispatchID)
	if err != nil {
		return nil, err
	}
	if receipt, cached, cacheErr := cachedTerminalLifecycleReceipt(p.selection.profile, action, req, intent, status, host); cacheErr != nil {
		return nil, cacheErr
	} else if cached {
		if err = p.ready(ctx); err != nil {
			return nil, err
		}
		freshIntent, freshIntentErr := p.controllerIntent(action.DispatchID)
		freshStatus, freshStatusErr := task.bridge.Status(action.DispatchID)
		if freshIntentErr != nil || !reflect.DeepEqual(freshIntent, intent) || freshStatusErr != nil || !sameLifecycleDurableSnapshot(freshStatus, status) {
			return nil, executor.ErrUncertain
		}
		// Reuse the exact terminal lifecycle receipt after the controller's
		// separately authorized Step has not yet consumed it. Do not poll a host
		// again after terminal evidence has already been durably ingested.
		if _, _, _, err = p.readCurrentActionView(ctx, action); err != nil {
			return nil, err
		}
		return []driver.Receipt{receipt}, nil
	}
	previousSequence := uint64(0)
	previousState := ""
	expectedLifecycleSequence := uint64(0)
	if len(status.Observations) > 0 {
		previous := status.Observations[len(status.Observations)-1]
		previousSequence = previous.HostSequence
		previousState = previous.State
		expectedLifecycleSequence = previous.Sequence
	}
	observation, err := p.selection.backend.ReadObservationContext(ctx, intent.OperationKey, req.Binding, previousSequence)
	if err != nil {
		return nil, err
	}
	if err = p.ready(ctx); err != nil {
		return nil, executor.ErrUncertain
	}
	if observation.Binding != req.Binding || observation.OperationKey != intent.OperationKey || executor.ValidateObservation(previousState, previousSequence, observation) != nil || observation.SourceTime.IsZero() || observation.SourceTime.After(time.Now().Add(time.Minute)) {
		return nil, executor.ErrUncertain
	}
	binding := status.Binding
	if !nativeDriverObservationMatches(action, req, intent, status, host, observation) {
		return nil, executor.ErrDenied
	}
	if err = p.ready(ctx); err != nil {
		return nil, err
	}
	// Polling is a slow callback. Refresh the exact durable host intent and
	// lifecycle compare-and-swap snapshot immediately before ingestion.
	freshIntent, err := p.controllerIntent(action.DispatchID)
	if err != nil || !reflect.DeepEqual(freshIntent, intent) {
		return nil, executor.ErrUncertain
	}
	freshStatus, err := task.bridge.Status(action.DispatchID)
	if err != nil || !sameLifecycleDurableSnapshot(freshStatus, status) {
		return nil, executor.ErrUncertain
	}
	if terminalHostObservation(observation.State) {
		// STOP forbids new work but does not erase independent terminal evidence
		// for the original retained writer. Keep the exact current action, route,
		// pinned profile, intent and lifecycle checks above; bridge.Observe still
		// authenticates its own principal and expected lifecycle sequence.
		if _, _, _, err = p.readCurrentActionView(ctx, action); err != nil {
			return nil, err
		}
	} else if err = p.authorizeCurrentActionAtBoundary(ctx, action, 0, sessions.ControllerDriverBoundaryObserve, intent.OperationKey); err != nil {
		return nil, err
	}
	key, err := driver.OperationKey(p.selection.profile.ProjectID, p.selection.profile.InvocationID, p.selection.profile.CheckpointID, observation.Sequence, "observe", intent.OperationKey)
	if err != nil {
		return nil, err
	}
	if err = reserveDriverEffect(ctx, budget); err != nil {
		return nil, err
	}
	_, err = task.bridge.Observe(sessions.ObserveLifecycleRequest{OperationKey: key, Binding: binding, ExpectedSequence: expectedLifecycleSequence, HostSequence: observation.Sequence, State: observation.State, Source: "owner-manual-native-host", EvidenceDigest: observation.EvidenceDigest, SourceTime: observation.SourceTime})
	if err != nil {
		return nil, err
	}
	lifecycle, err := task.bridge.Status(action.DispatchID)
	if err != nil {
		return nil, err
	}
	return []driver.Receipt{{Kind: "dispatch", OperationKey: key, State: lifecycle.State, Lifecycle: &lifecycle}}, nil
}

func terminalHostObservation(state string) bool {
	return state == "completed" || state == "failed" || state == "cancelled"
}

func sameLifecycleDurableSnapshot(left, right sessions.LifecycleStatus) bool {
	return reflect.DeepEqual(left.Binding, right.Binding) &&
		reflect.DeepEqual(left.Observations, right.Observations) &&
		reflect.DeepEqual(left.Cancellation, right.Cancellation) &&
		left.ResourcesRetained == right.ResourcesRetained
}

func cachedTerminalLifecycleReceipt(profile nativeControllerDriverProfile, action sessions.ControllerAction, request executor.Dispatch, intent sessions.DispatchIntent, lifecycle sessions.LifecycleStatus, host executor.HostResult) (driver.Receipt, bool, error) {
	if !terminalHostObservation(lifecycle.State) {
		return driver.Receipt{}, false, nil
	}
	if len(lifecycle.Observations) == 0 {
		return driver.Receipt{}, false, executor.ErrUncertain
	}
	last := lifecycle.Observations[len(lifecycle.Observations)-1]
	if last.State != lifecycle.State || last.HostSequence == 0 || last.EvidenceDigest == "" || last.SourceTime.IsZero() || last.SourceTime.After(time.Now().Add(time.Minute)) {
		return driver.Receipt{}, false, executor.ErrUncertain
	}
	observation := executor.Observation{Version: executor.ContractVersion, OperationKey: intent.OperationKey, Binding: request.Binding, Sequence: last.HostSequence, State: last.State, EvidenceDigest: last.EvidenceDigest, SourceTime: last.SourceTime}
	if !nativeDriverObservationMatches(action, request, intent, lifecycle, host, observation) {
		return driver.Receipt{}, false, executor.ErrUncertain
	}
	key, err := driver.OperationKey(profile.ProjectID, profile.InvocationID, profile.CheckpointID, last.HostSequence, "observe", intent.OperationKey)
	if err != nil {
		return driver.Receipt{}, false, err
	}
	return driver.Receipt{Kind: "dispatch", OperationKey: key, State: lifecycle.State, Lifecycle: &lifecycle}, true, nil
}

func nativeDriverObservationMatches(action sessions.ControllerAction, request executor.Dispatch, intent sessions.DispatchIntent, lifecycle sessions.LifecycleStatus, host executor.HostResult, observation executor.Observation) bool {
	if intent.Lease == nil || intent.State != "acknowledged" || (action.Kind != "host_dispatch" && action.Kind != "observe") || action.DispatchID != intent.DispatchID || action.TaskID != intent.Reservation.Reservation.TaskID || (action.ActorID != "" && action.ActorID != intent.BridgeSessionID) || (action.IntentID != "" && action.IntentID != intent.ID) || (action.OperationKey != "" && action.OperationKey != intent.OperationKey) {
		return false
	}
	binding := lifecycle.Binding
	if binding.DispatchID != intent.DispatchID || binding.IntentID != intent.ID || binding.BridgeSessionID != intent.BridgeSessionID || binding.WorkerSessionID != intent.WorkerSessionID || binding.TaskID != action.TaskID || binding.Checkout != request.Checkout || binding.BaseCommit != intent.Reservation.BaseCommit || binding.HostAgentID != host.HostExecutionID || binding.AttemptID != intent.Lease.AttemptID || binding.LeaseID != intent.Lease.ID || binding.ReservationFence != intent.Reservation.Reservation.Fence || binding.LeaseFence != intent.Lease.Fence {
		return false
	}
	if intent.Lease.SessionID != intent.WorkerSessionID || intent.Lease.TaskID != action.TaskID || intent.Lease.Repository != request.Repository || intent.Lease.Checkout != request.Checkout || request.Binding.Session != intent.WorkerSessionID || request.Binding.Task != action.TaskID || request.Binding.Attempt != intent.Lease.AttemptID || (action.AttemptID != "" && action.AttemptID != binding.AttemptID) || (action.LeaseID != "" && action.LeaseID != binding.LeaseID) || (action.Fence != 0 && action.Fence != controllerActionFenceForLifecycle(action, binding)) || (action.ReservationFence != 0 && action.ReservationFence != binding.ReservationFence) {
		return false
	}
	return host.OperationKey == intent.OperationKey && host.DispatchID == intent.DispatchID && host.Repository == request.Repository && host.Checkout == request.Checkout && reflect.DeepEqual(host.Binding, request.Binding) && observation.Binding == request.Binding && observation.OperationKey == intent.OperationKey
}

func controllerActionFenceForLifecycle(action sessions.ControllerAction, binding sessions.LifecycleBinding) uint64 {
	if action.LeaseID != "" {
		return binding.LeaseFence
	}
	return binding.ReservationFence
}

func (p *nativeControllerDriverPorts) Verify(ctx context.Context, action sessions.ControllerAction, request sessions.VerificationRequest, budget driver.EffectBudget) ([]driver.Receipt, error) {
	if err := p.ready(ctx); err != nil {
		return nil, err
	}
	view, _, _, err := p.readCurrentActionView(ctx, action)
	if err != nil {
		return nil, err
	}
	task, err := p.selection.freshTask(action.TaskID)
	if err != nil {
		return nil, err
	}
	if !nativeDriverVerificationRouteMatches(view.Routes[action.TaskID], action, task.runner.profile, request) {
		return nil, sessions.ErrDenied
	}
	access, err := task.runner.Authenticate()
	if err != nil {
		return nil, err
	}
	if err = p.ready(ctx); err != nil {
		return nil, err
	}
	nativeRequest := NativeVerificationRequest{OperationKey: request.OperationKey, WorkerSession: request.WorkerSession, AttemptID: request.AttemptID, LeaseID: request.LeaseID, RevisionID: request.RevisionID, CheckID: request.CheckID, ExpectedFence: request.ExpectedFence}
	lookup, err := access.Lookup(nativeRequest)
	if err != nil {
		return nil, err
	}
	if lookup.State == sessions.VerificationAbsent {
		if err = p.ready(ctx); err != nil {
			return nil, err
		}
		if err = p.authorizeCurrentActionAtBoundary(ctx, action, 0, sessions.ControllerDriverBoundaryVerify, request.OperationKey); err != nil {
			return nil, err
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err = reserveDriverEffect(ctx, budget); err != nil {
			return nil, err
		}
		if _, err = access.Run(ctx, nativeRequest); err != nil {
			return nil, err
		}
		if err = p.ready(ctx); err != nil {
			return nil, executor.ErrUncertain
		}
		lookup, err = access.Lookup(nativeRequest)
		if err != nil {
			return nil, err
		}
	}
	if err = p.ready(ctx); err != nil {
		return nil, err
	}
	copy := lookup
	return []driver.Receipt{{Kind: "verification", OperationKey: request.OperationKey, State: string(lookup.State), Verification: &copy}}, nil
}

func (p *nativeControllerDriverPorts) Review(ctx context.Context, action sessions.ControllerAction, budget driver.EffectBudget) ([]driver.Receipt, error) {
	if err := p.ready(ctx); err != nil {
		return nil, err
	}
	task, err := p.selection.freshTask(action.TaskID)
	if err != nil {
		return nil, err
	}
	view, _, _, err := p.readCurrentActionView(ctx, action)
	if err != nil {
		return nil, err
	}
	if !nativeDriverReviewRouteMatches(view.Routes[action.TaskID], action, task.reviewer.profile) {
		return nil, sessions.ErrDenied
	}
	access, err := task.reviewer.Authenticate()
	if err != nil {
		return nil, err
	}
	if err = p.ready(ctx); err != nil {
		return nil, err
	}
	if action.ReviewID != "" {
		eligibility, e := access.Evaluate(action.ReviewID)
		if e != nil {
			return nil, e
		}
		return []driver.Receipt{{Kind: "review", OperationKey: action.OperationKey, State: string(eligibility.Reason)}}, nil
	}
	if p.selection.decisions == nil {
		return nil, driver.ErrUnsupported
	}
	decision, err := p.selection.decisions.ReviewDecision(ctx, action)
	if err != nil {
		return nil, err
	}
	if err = p.ready(ctx); err != nil {
		return nil, err
	}
	if err = p.authorizeCurrentActionAtBoundary(ctx, action, 0, sessions.ControllerDriverBoundaryReview, action.OperationKey); err != nil {
		return nil, err
	}
	if decision.OperationKey != action.OperationKey || decision.AttemptID != action.AttemptID || decision.LeaseID != action.LeaseID || decision.RevisionID != action.RevisionID || decision.ExpectedFence != action.Fence || !reflect.DeepEqual(decision.EvidenceIDs, action.EvidenceIDs) || decision.Outcome == "" || decision.Rationale == "" {
		return nil, sessions.ErrDenied
	}
	if err = reserveDriverEffect(ctx, budget); err != nil {
		return nil, err
	}
	review, err := access.Propose(decision)
	if err != nil {
		return nil, err
	}
	if err = p.ready(ctx); err != nil {
		return nil, executor.ErrUncertain
	}
	_, err = access.Evaluate(review.ID)
	if err != nil {
		return nil, err
	}
	return []driver.Receipt{{Kind: "review", OperationKey: decision.OperationKey, State: review.Request.Outcome, Review: &review}}, nil
}

func (p *nativeControllerDriverPorts) Cancel(ctx context.Context, action sessions.ControllerAction, budget driver.EffectBudget) ([]driver.Receipt, error) {
	if err := p.ready(ctx); err != nil {
		return nil, err
	}
	task, err := p.selection.freshTask(action.TaskID)
	if err != nil {
		return nil, err
	}
	intent, err := p.controllerIntent(action.DispatchID)
	if err != nil {
		return nil, err
	}
	prior, err := task.bridge.Status(action.DispatchID)
	if err != nil {
		return nil, err
	}
	a, token, err := p.selection.controller.swarm.authenticate()
	if err != nil {
		return nil, err
	}
	key, err := driver.OperationKey(p.selection.profile.ProjectID, p.selection.profile.InvocationID, p.selection.profile.CheckpointID, 0, "cancel", intent.OperationKey)
	if err != nil {
		return nil, err
	}
	request, err := cancellationForIntent(task.selection, action, intent, p.selection.profile, prior.Binding)
	if err != nil {
		return nil, err
	}
	controllerSequence, err := p.authorizeCurrentActionAtBoundarySequence(ctx, action, 0, sessions.ControllerDriverBoundaryCancelRequest, intent.OperationKey)
	if err != nil {
		return nil, err
	}
	if err = reserveDriverEffect(ctx, budget); err != nil {
		return nil, err
	}
	withdrawal, err := a.CancelLifecycle(token, p.selection.controller.swarm.connection.SessionID, sessions.CancelLifecycleRequest{OperationKey: key, Binding: request.Lifecycle})
	if err != nil {
		return nil, err
	}
	if err = p.ready(ctx); err != nil {
		return nil, executor.ErrUncertain
	}
	var state string
	if prior.Cancellation == nil && withdrawal.HostActionAuthorized {
		if contextBackendMissing(p.selection.backend) {
			return nil, executor.ErrUnsupportedContextBackend
		}
		caps, capErr := p.selection.backend.CapabilitiesContext(ctx)
		if capErr != nil || ctx.Err() != nil || caps.Version != executor.ContractVersion || caps.Host != request.Worker.Host || caps.Cancel.State != executor.Supported {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, executor.ErrUnsupportedContextBackend
		}
		if err = p.selection.backend.VerifyBindingContext(ctx, request.Worker); err != nil {
			return nil, executor.ErrUncertain
		}
		if err = p.ready(ctx); err != nil {
			return nil, executor.ErrUncertain
		}
		if err = p.authorizeCurrentCancellation(ctx, controllerSequence, sessions.CancelLifecycleRequest{OperationKey: key, Binding: request.Lifecycle}); err != nil {
			return nil, err
		}
		if err = reserveDriverEffect(ctx, budget); err != nil {
			return nil, err
		}
		cancel, cancelErr := p.selection.backend.CancelContext(ctx, request)
		if cancelErr != nil {
			if ctx.Err() != nil {
				return nil, executor.ErrUncertain
			}
			cancel, cancelErr = p.selection.backend.LookupCancellationContext(ctx, request)
		}
		if cancelErr != nil || !validCancellationState(cancel) {
			return nil, executor.ErrUncertain
		}
		state = cancel
		if err = p.acknowledgeCancellation(ctx, task, intent, request, withdrawal.Cancellation.Sequence, state, budget); err != nil {
			return nil, err
		}
	} else if prior.Cancellation != nil && prior.Cancellation.State == "requested" {
		if contextBackendMissing(p.selection.backend) {
			return nil, executor.ErrUncertain
		}
		caps, capErr := p.selection.backend.CapabilitiesContext(ctx)
		if capErr != nil || ctx.Err() != nil || caps.Version != executor.ContractVersion || caps.Cancel.State != executor.Supported {
			return nil, executor.ErrUncertain
		}
		if err = p.ready(ctx); err != nil {
			return nil, executor.ErrUncertain
		}
		state, err = p.selection.backend.LookupCancellationContext(ctx, request)
		if err != nil || !validCancellationState(state) {
			return nil, executor.ErrUncertain
		}
		if err = p.acknowledgeCancellation(ctx, task, intent, request, prior.Cancellation.Sequence, state, budget); err != nil {
			return nil, err
		}
	}
	status, err := task.bridge.Status(action.DispatchID)
	if err != nil {
		return nil, err
	}
	receiptKey := request.OperationKey
	if status.Cancellation != nil && status.Cancellation.State != "requested" {
		ackKey, keyErr := driver.OperationKey(p.selection.profile.ProjectID, p.selection.profile.InvocationID, p.selection.profile.CheckpointID, 0, "cancel-ack", intent.OperationKey)
		if keyErr != nil {
			return nil, keyErr
		}
		receiptKey = ackKey
	}
	if status.Cancellation == nil {
		return nil, executor.ErrUncertain
	}
	return []driver.Receipt{{Kind: "cancellation", OperationKey: receiptKey, State: status.Cancellation.State, Lifecycle: &status}}, nil
}

func cancellationForIntent(selection nativeDriverTaskProfile, action sessions.ControllerAction, intent sessions.DispatchIntent, profile nativeControllerDriverProfile, lifecycle sessions.LifecycleBinding) (executor.CancellationRequest, error) {
	request := selection.DispatchTemplate
	request.Binding.Attempt = selection.WorkerEnrollment.Binding.Attempt
	if action.AttemptID != "" && action.AttemptID != request.Binding.Attempt {
		request.Binding.Attempt = ""
	}
	if lifecycle.DispatchID != intent.DispatchID || lifecycle.IntentID != intent.ID || lifecycle.BridgeSessionID != intent.BridgeSessionID || lifecycle.WorkerSessionID != intent.WorkerSessionID || lifecycle.TaskID != action.TaskID || lifecycle.Checkout != request.Checkout || lifecycle.BaseCommit != intent.Reservation.BaseCommit || lifecycle.ReservationFence != intent.Reservation.Reservation.Fence || (action.LeaseID != "" && action.LeaseID != lifecycle.LeaseID) || (action.AttemptID != "" && action.AttemptID != lifecycle.AttemptID) || (action.LeaseID != "" && action.Fence != 0 && action.Fence != lifecycle.LeaseFence) || (action.ReservationFence != 0 && action.ReservationFence != lifecycle.ReservationFence) {
		return executor.CancellationRequest{}, sessions.ErrDenied
	}
	key, err := driver.OperationKey(profile.ProjectID, profile.InvocationID, profile.CheckpointID, 0, "cancel", intent.OperationKey)
	if err != nil {
		return executor.CancellationRequest{}, err
	}
	return executor.CancellationRequest{Version: executor.ContractVersion, OperationKey: key, OriginalOperationKey: intent.OperationKey, Lifecycle: lifecycle, Worker: request.Binding, Repository: request.Repository, Checkout: request.Checkout}, nil
}

func validCancellationState(state string) bool {
	return state == "acknowledged" || state == "unsupported" || state == "dispatch_unknown"
}

func (p *nativeControllerDriverPorts) acknowledgeCancellation(ctx context.Context, task nativeDriverTaskAccess, intent sessions.DispatchIntent, request executor.CancellationRequest, expected uint64, state string, budget driver.EffectBudget) error {
	if err := p.ready(ctx); err != nil {
		return executor.ErrUncertain
	}
	key, err := driver.OperationKey(p.selection.profile.ProjectID, p.selection.profile.InvocationID, p.selection.profile.CheckpointID, 0, "cancel-ack", intent.OperationKey)
	if err != nil {
		return err
	}
	if err := reserveDriverEffect(ctx, budget); err != nil {
		return err
	}
	_, err = task.bridge.Acknowledge(sessions.AcknowledgeCancellationRequest{ExpectedSequence: expected, OperationKey: key, Binding: request.Lifecycle, State: state, EvidenceDigest: sessions.OutputDigest([]byte(request.OperationKey + ":" + state))})
	return err
}

func (p *nativeControllerDriverPorts) reconcileCancellation(ctx context.Context, task nativeDriverTaskAccess, action sessions.ControllerAction, intent sessions.DispatchIntent, budget driver.EffectBudget) (driver.Receipt, bool, error) {
	status, err := task.bridge.Status(action.DispatchID)
	if err != nil || status.Cancellation == nil {
		return driver.Receipt{}, false, err
	}
	cancellation := status.Cancellation
	if cancellation.State == "requested" {
		if contextBackendMissing(p.selection.backend) {
			return driver.Receipt{}, false, executor.ErrUncertain
		}
		caps, capErr := p.selection.backend.CapabilitiesContext(ctx)
		if capErr != nil || ctx.Err() != nil || caps.Version != executor.ContractVersion || caps.Cancel.State != executor.Supported {
			return driver.Receipt{}, false, executor.ErrUncertain
		}
		request, requestErr := cancellationForIntent(task.selection, action, intent, p.selection.profile, status.Binding)
		if requestErr != nil {
			return driver.Receipt{}, false, requestErr
		}
		state, lookupErr := p.selection.backend.LookupCancellationContext(ctx, request)
		if lookupErr != nil || !validCancellationState(state) {
			return driver.Receipt{}, false, executor.ErrUncertain
		}
		if err = p.acknowledgeCancellation(ctx, task, intent, request, cancellation.Sequence, state, budget); err != nil {
			return driver.Receipt{}, false, err
		}
		status, err = task.bridge.Status(action.DispatchID)
		if err != nil || status.Cancellation == nil {
			return driver.Receipt{}, false, executor.ErrUncertain
		}
		cancellation = status.Cancellation
	}
	return driver.Receipt{Kind: "cancellation", OperationKey: cancellation.OperationKey, State: cancellation.State, Lifecycle: &status}, true, nil
}

func (p *nativeControllerDriverPorts) ready(ctx context.Context) error {
	if p == nil || p.selection == nil || ctx == nil || ctx.Err() != nil {
		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		return sessions.ErrDenied
	}
	if err := p.selection.verifyPinnedFiles(); err != nil {
		return sessions.ErrDenied
	}
	return ctx.Err()
}

func contextBackendMissing(backend executor.ContextBackend) bool {
	if backend == nil {
		return true
	}
	v := reflect.ValueOf(backend)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

func reserveDriverEffect(ctx context.Context, budget driver.EffectBudget) error {
	if ctx == nil {
		return sessions.ErrDenied
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if budget == nil {
		return driver.ErrEffectLimit
	}
	v := reflect.ValueOf(budget)
	if (v.Kind() == reflect.Chan || v.Kind() == reflect.Func || v.Kind() == reflect.Interface || v.Kind() == reflect.Map || v.Kind() == reflect.Pointer || v.Kind() == reflect.Slice) && v.IsNil() {
		return driver.ErrEffectLimit
	}
	if err := budget.ReserveEffect(); err != nil {
		return err
	}
	return ctx.Err()
}

func (p *nativeControllerDriverPorts) requireDispatchCapability(ctx context.Context) error {
	if err := p.ready(ctx); err != nil {
		return err
	}
	if contextBackendMissing(p.selection.backend) {
		return executor.ErrUnsupportedContextBackend
	}
	caps, err := p.selection.backend.CapabilitiesContext(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return executor.ErrUnsupportedContextBackend
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if caps.Version != executor.ContractVersion || caps.Dispatch.State != executor.Supported || caps.Lookup.State != executor.Supported || caps.PrivateProfileHandoff.State != executor.Supported {
		return executor.ErrUnsupportedContextBackend
	}
	for _, task := range p.selection.tasks {
		if task.selection.WorkerEnrollment.Binding.Host != caps.Host {
			return executor.ErrUnsupportedContextBackend
		}
	}
	return nil
}

func (p *nativeControllerDriverPorts) controllerIntent(dispatchID string) (sessions.DispatchIntent, error) {
	a, token, err := p.selection.controller.swarm.authenticate()
	if err != nil {
		return sessions.DispatchIntent{}, err
	}
	return a.DispatchIntentStatus(token, p.selection.controller.swarm.connection.SessionID, dispatchID)
}

func dispatchForIntent(selection nativeDriverTaskProfile, action sessions.ControllerAction, intent sessions.DispatchIntent) executor.Dispatch {
	request := selection.DispatchTemplate
	request.OperationKey = intent.OperationKey
	request.DispatchID = intent.DispatchID
	request.ExpectedFence = intent.Reservation.Reservation.Fence
	request.BridgeSession = intent.BridgeSessionID
	request.Binding.Attempt = selection.WorkerEnrollment.Binding.Attempt
	if action.AttemptID != "" && action.AttemptID != request.Binding.Attempt {
		request.Binding.Attempt = ""
	}
	return request
}

func dispatchForReference(selection nativeDriverTaskProfile, action sessions.ControllerAction, reference sessions.ControllerHostDispatchReference) executor.Dispatch {
	request := selection.DispatchTemplate
	request.OperationKey = reference.OriginalOperationKey
	request.DispatchID = reference.DispatchID
	request.ExpectedFence = reference.ReservationFence
	request.Binding.Attempt = selection.WorkerEnrollment.Binding.Attempt
	if action.AttemptID != "" && action.AttemptID != request.Binding.Attempt {
		request.Binding.Attempt = ""
	}
	return request
}

func (p *nativeControllerDriverPorts) correlateAndAcknowledge(ctx context.Context, task nativeDriverTaskAccess, action sessions.ControllerAction, request executor.Dispatch, result executor.HostResult, intent sessions.DispatchIntent, budget driver.EffectBudget) (sessions.DispatchIntent, error) {
	if err := p.ready(ctx); err != nil {
		return intent, err
	}
	if result.OperationKey != intent.OperationKey || result.DispatchID != intent.DispatchID || result.HostExecutionID == "" || result.Repository != request.Repository || result.Checkout != request.Checkout || !reflect.DeepEqual(result.Binding, request.Binding) {
		return intent, executor.ErrUncertain
	}
	if intent.State == "dispatch_requested" {
		// Correlation is a dispatch-capability effect. Lifecycle status and
		// observation use the narrower independent lifecycle connection, but a
		// dispatch write must reopen the same bridge identity through the pinned
		// capability-bearing swarm constructor.
		bridge, err := p.dispatchBridge(task)
		if err != nil {
			return intent, err
		}
		if err = p.authorizeCurrentActionAtBoundary(ctx, action, 0, sessions.ControllerDriverBoundaryReconcile, intent.OperationKey); err != nil {
			return intent, err
		}
		key, err := driver.OperationKey(p.selection.profile.ProjectID, p.selection.profile.InvocationID, p.selection.profile.CheckpointID, 0, "correlate", intent.OperationKey)
		if err != nil {
			return intent, err
		}
		if err = reserveDriverEffect(ctx, budget); err != nil {
			return intent, err
		}
		intent, err = bridge.CorrelateDispatch(sessions.CorrelateDispatchIntent{OperationKey: key, DispatchID: intent.DispatchID, IntentID: intent.ID, HostAgentID: result.HostExecutionID, State: "host_correlated", ExpectedFence: intent.Reservation.Reservation.Fence, ExpectedSequence: intent.Sequence})
		if err != nil {
			return intent, err
		}
	}
	if err := p.ready(ctx); err != nil {
		return intent, err
	}
	if intent.State == "host_correlated" {
		a, token, err := task.worker.authenticate()
		if err != nil {
			return intent, err
		}
		worker, err := executor.AuthenticateWorker(a, token, task.selection.WorkerEnrollment, time.Now())
		if err != nil {
			return intent, err
		}
		if err = p.authorizeCurrentActionAtBoundary(ctx, action, 0, sessions.ControllerDriverBoundaryReconcile, intent.OperationKey); err != nil {
			return intent, err
		}
		attemptID := worker.Claim().Attempt
		key, err := driver.OperationKey(p.selection.profile.ProjectID, p.selection.profile.InvocationID, p.selection.profile.CheckpointID, 0, "ack", intent.OperationKey)
		if err != nil {
			return intent, err
		}
		if err = reserveDriverEffect(ctx, budget); err != nil {
			return intent, err
		}
		intent, err = a.AcknowledgeDispatchIntent(token, task.worker.connection.SessionID, sessions.AcknowledgeDispatchIntent{OperationKey: key, DispatchID: intent.DispatchID, IntentID: intent.ID, AttemptID: attemptID, HostAgentID: result.HostExecutionID, ExpectedFence: intent.Reservation.Reservation.Fence, ExpectedSequence: intent.Sequence})
		if err != nil {
			return intent, err
		}
	}
	if intent.State != "acknowledged" || intent.Lease == nil || intent.OperationKey != request.OperationKey || intent.DispatchID != request.DispatchID || intent.HostAgentID != result.HostExecutionID || intent.Reservation.Reservation.Fence != request.ExpectedFence {
		return intent, executor.ErrUncertain
	}
	return intent, nil
}

func (p *nativeControllerDriverPorts) dispatchBridge(task nativeDriverTaskAccess) (*NativeSwarmAccess, error) {
	if task.bridge == nil || p.selection == nil {
		return nil, sessions.ErrDenied
	}
	return OpenNativeSwarm(task.bridge.path, p.selection.profile.CapabilityFile)
}

func (p *nativeControllerDriverPorts) authorizeCurrentActionAtBoundary(ctx context.Context, action sessions.ControllerAction, expectedSequence uint64, boundary sessions.ControllerDriverEffectBoundary, originalOperationKey string) error {
	_, err := p.authorizeCurrentActionAtBoundarySequence(ctx, action, expectedSequence, boundary, originalOperationKey)
	return err
}

func (p *nativeControllerDriverPorts) authorizeCurrentActionAtBoundarySequence(ctx context.Context, action sessions.ControllerAction, expectedSequence uint64, boundary sessions.ControllerDriverEffectBoundary, originalOperationKey string) (uint64, error) {
	view, a, token, err := p.readCurrentActionView(ctx, action)
	if err != nil {
		return 0, err
	}
	if expectedSequence != 0 && view.Status.Sequence != expectedSequence {
		return 0, sessions.ErrDenied
	}
	if err = a.AuthorizeControllerDriverActionEffect(ctx, token, p.selection.controller.swarm.connection.SessionID, p.selection.profile.ProjectID, p.selection.profile.InvocationID, view.Status.Sequence, action, boundary, originalOperationKey); err != nil {
		return 0, err
	}
	if err = p.ready(ctx); err != nil {
		return 0, err
	}
	return view.Status.Sequence, nil
}

func (p *nativeControllerDriverPorts) authorizeCurrentCancellation(ctx context.Context, expectedSequence uint64, request sessions.CancelLifecycleRequest) error {
	if err := p.ready(ctx); err != nil {
		return err
	}
	a, token, err := p.selection.controller.swarm.authenticate()
	if err != nil {
		return err
	}
	if err = a.AuthorizeControllerDriverCancellation(ctx, token, p.selection.controller.swarm.connection.SessionID, p.selection.profile.ProjectID, p.selection.profile.InvocationID, expectedSequence, request); err != nil {
		return err
	}
	return p.ready(ctx)
}

func (p *nativeControllerDriverPorts) readCurrentActionView(ctx context.Context, action sessions.ControllerAction) (sessions.ControllerDriverView, *sessions.Authority, string, error) {
	if err := p.ready(ctx); err != nil {
		return sessions.ControllerDriverView{}, nil, "", err
	}
	a, token, err := p.selection.controller.swarm.authenticate()
	if err != nil {
		return sessions.ControllerDriverView{}, nil, "", err
	}
	view, err := a.ReadControllerDriverView(ctx, token, p.selection.controller.swarm.connection.SessionID, p.selection.profile.ProjectID, p.selection.profile.InvocationID)
	if err != nil {
		return sessions.ControllerDriverView{}, nil, "", err
	}
	found := false
	for _, current := range view.Status.Actions {
		if reflect.DeepEqual(current, action) {
			found = true
			break
		}
	}
	route, ok := view.Routes[action.TaskID]
	if !found || !ok || !nativeDriverActionActorMatches(action, route) {
		return sessions.ControllerDriverView{}, nil, "", sessions.ErrDenied
	}
	if err = p.ready(ctx); err != nil {
		return sessions.ControllerDriverView{}, nil, "", err
	}
	return view, a, token, nil
}

func nativeDriverVerificationRouteMatches(route sessions.ControllerTaskRoute, action sessions.ControllerAction, profile NativeRunnerProfile, request sessions.VerificationRequest) bool {
	if action.Kind != "verification" || action.ActorID != route.RunnerSession || profile.RunnerSessionID != route.RunnerSession || profile.TaskID != action.TaskID || request.OperationKey == "" {
		return false
	}
	requested := false
	for _, candidate := range action.Verifications {
		if reflect.DeepEqual(candidate, request) {
			requested = true
			break
		}
	}
	if !requested && reflect.DeepEqual(action.Verification, request) {
		requested = true
	}
	if !requested {
		return false
	}
	configuredGrant := ""
	for _, check := range profile.Checks {
		if check.ID == request.CheckID {
			configuredGrant = check.GrantID
			break
		}
	}
	if configuredGrant == "" || request.GrantID != configuredGrant {
		return false
	}
	for _, check := range route.Checks {
		if check.CheckID == request.CheckID && check.GrantID == configuredGrant {
			return true
		}
	}
	return len(route.Checks) == 0 && route.CheckID == request.CheckID && route.VerificationGrantID == configuredGrant
}

func nativeDriverReviewRouteMatches(route sessions.ControllerTaskRoute, action sessions.ControllerAction, profile NativeReviewerProfile) bool {
	return action.Kind == "independent_review" && action.ActorID == route.ReviewerSession && profile.ReviewerSessionID == route.ReviewerSession && profile.WorkerSessionID == route.WorkerSession && profile.TaskID == action.TaskID && profile.GrantID == route.ReviewGrantID
}

func nativeDriverActionActorMatches(action sessions.ControllerAction, route sessions.ControllerTaskRoute) bool {
	switch action.Kind {
	case "dispatch", "host_dispatch", "observe", "cancellation":
		return action.ActorID != "" && action.ActorID == route.BridgeSession
	case "verification":
		return action.ActorID != "" && action.ActorID == route.RunnerSession
	case "independent_review":
		return action.ActorID != "" && action.ActorID == route.ReviewerSession
	default:
		return false
	}
}

var _ driver.Ports = (*nativeControllerDriverPorts)(nil)
