package executor

import (
	"reflect"
	"strings"
	"time"

	"github.com/mlnomadpy/dacli/internal/governance/sessions"
)

// DispatchControllerOnce spends a controller step's opaque first-use grant
// before it registers or dispatches anything at the native host.
func (c Coordinator) DispatchControllerOnce(permit sessions.ControllerHostDispatchPermit, req Dispatch, worker AuthenticatedBinding, now time.Time) (HostResult, error) {
	if !controllerDispatchRequestValid(req, worker, now) || c.Backend == nil || c.Authority == nil || c.controllerToken == "" || c.controllerSession == "" {
		return HostResult{}, ErrDenied
	}
	currentWorker, err := c.Authority.Authenticate(worker.token, worker.binding.Session)
	if err != nil || currentWorker.Revoked || !now.Before(currentWorker.ExpiresAt) || worker.attempt.SessionID != currentWorker.ID {
		return HostResult{}, ErrDenied
	}
	if _, err := c.Authority.Authorize(worker.token, worker.binding.Session, worker.binding.Workspace, "lease.acquire", "task:"+worker.binding.Task); err != nil {
		return HostResult{}, ErrDenied
	}
	reservation, err := c.Authority.ReadDispatchReservation(worker.token, worker.binding.Session, req.DispatchID, req.ExpectedFence)
	if err != nil || !dispatchReservationMatchesWorker(reservation, req, worker) {
		return HostResult{}, ErrDenied
	}
	ref := permit.Reference()
	if !controllerReferenceMatchesRequest(ref, req, worker) {
		return HostResult{}, ErrDenied
	}
	caps := c.Backend.Capabilities()
	if caps.Version != ContractVersion || caps.Host != req.Binding.Host || caps.Dispatch.State != Supported || caps.Lookup.State != Supported || caps.PrivateProfileHandoff.State != Supported {
		return HostResult{}, ErrDenied
	}
	registration := Registration{Version: ContractVersion, Binding: worker.binding, Capabilities: caps}
	if ValidateRegistration(c.Authority, worker, registration, now) != nil {
		return HostResult{}, ErrDenied
	}
	intent, err := c.Authority.ConsumeControllerHostDispatch(c.controllerToken, c.controllerSession, permit)
	if err != nil || intent.ControllerSessionID != c.controllerSession || !reflect.DeepEqual(intent.Reservation, reservation) || !controllerIntentMatchesRequest(intent, ref, req, worker) {
		return HostResult{}, ErrDenied
	}
	// One-use consumption is the authorization boundary; the intent was already
	// durable, and backend work happens after the authority lock is released.
	if c.Backend.Register(registration) != nil {
		return HostResult{}, ErrDenied
	}
	result, dispatchErr := c.Backend.Dispatch(req)
	if dispatchErr == nil && hostResultMatches(result, req) {
		return result, nil
	}
	lookup, lookupErr := c.Backend.Lookup(req.OperationKey, req.Binding)
	if lookupErr == nil && hostResultMatches(lookup, req) {
		return lookup, nil
	}
	return HostResult{}, ErrUncertain
}

// LookupControllerDispatch is recovery-only: it authenticates the current
// controller against the retained original intent, verifies the existing host
// binding, and looks up only the original operation key.
func (c Coordinator) LookupControllerDispatch(req Dispatch, worker AuthenticatedBinding) (HostResult, error) {
	now := time.Now()
	if !controllerDispatchRequestBound(req, worker) || c.Backend == nil || c.Authority == nil || c.controllerToken == "" || c.controllerSession == "" {
		return HostResult{}, ErrDenied
	}
	current, err := c.Authority.Authenticate(c.controllerToken, c.controllerSession)
	if err != nil || current.Revoked || !now.Before(current.ExpiresAt) {
		return HostResult{}, ErrDenied
	}
	intent, err := c.Authority.DispatchIntentStatus(c.controllerToken, c.controllerSession, req.DispatchID)
	if err != nil || intent.ControllerSessionID != c.controllerSession || !controllerIntentMatchesRequest(intent, sessions.ControllerHostDispatchReference{ProjectID: worker.binding.Project, DispatchID: req.DispatchID, OriginalOperationKey: req.OperationKey, ReservationFence: req.ExpectedFence}, req, worker) {
		return HostResult{}, ErrDenied
	}
	caps := c.Backend.Capabilities()
	if caps.Version != ContractVersion || caps.Host != req.Binding.Host || caps.Lookup.State != Supported {
		return HostResult{}, ErrDenied
	}
	if c.Backend.VerifyBinding(worker.binding) != nil {
		return HostResult{}, ErrUncertain
	}
	result, err := c.Backend.Lookup(req.OperationKey, req.Binding)
	if err != nil || !hostResultMatches(result, req) {
		return HostResult{}, ErrUncertain
	}
	return result, nil
}

func controllerDispatchRequestValid(req Dispatch, worker AuthenticatedBinding, now time.Time) bool {
	return dispatchRequestMatchesWorker(req, worker, now) && worker.token != "" && worker.attempt.SessionID == worker.binding.Session && worker.attempt.ProjectID == worker.binding.Project && worker.attempt.Request.TaskID == worker.binding.Task && worker.attempt.ID == worker.binding.Attempt
}

func controllerDispatchRequestBound(req Dispatch, worker AuthenticatedBinding) bool {
	return req.Version == ContractVersion && req.OperationKey != "" && req.DispatchID != "" && req.BridgeSession != "" && req.ExpectedFence != 0 && req.PromptSpecDigest != "" && req.PromptSpecDigest == DispatchSpecDigest(req) && strings.TrimSpace(req.Prompt) != "" && req.Repository == worker.attempt.Observation.Repository && req.Checkout == worker.attempt.Observation.Locator && worker.binding.same(req.Binding) && req.Profile == worker.profile && worker.attempt.SessionID == worker.binding.Session && worker.attempt.ProjectID == worker.binding.Project && worker.attempt.Request.TaskID == worker.binding.Task && worker.attempt.ID == worker.binding.Attempt
}

func controllerReferenceMatchesRequest(ref sessions.ControllerHostDispatchReference, req Dispatch, worker AuthenticatedBinding) bool {
	return ref.ProjectID == worker.binding.Project && ref.InvocationID != "" && ref.CheckpointID != "" && ref.StepOperationKey != "" && ref.ActionID != "" && ref.DispatchID == req.DispatchID && ref.IntentID != "" && ref.OriginalOperationKey == req.OperationKey && ref.ControllerSequence > 0 && ref.CheckpointSequence > 0 && ref.ReservationFence == req.ExpectedFence
}

func controllerIntentMatchesRequest(intent sessions.DispatchIntent, ref sessions.ControllerHostDispatchReference, req Dispatch, worker AuthenticatedBinding) bool {
	return intent.OperationKey == req.OperationKey && intent.DispatchID == req.DispatchID && intent.ID != "" && (ref.IntentID == "" || intent.ID == ref.IntentID) && intent.BridgeSessionID == req.BridgeSession && intent.WorkerSessionID == worker.binding.Session && intent.PromptSpecDigest == req.PromptSpecDigest && intent.Reservation.Reservation.Fence == req.ExpectedFence && intent.Reservation.ProjectID == worker.binding.Project && intent.Reservation.Reservation.TaskID == worker.binding.Task && intent.Reservation.Reservation.Checkout == worker.attempt.Observation.Locator && intent.Reservation.Reservation.Repository == worker.attempt.Observation.Repository && dispatchReservationMatchesWorker(intent.Reservation, req, worker)
}
