package executor

import (
	"context"
	"errors"
	"reflect"
	"time"

	"github.com/mlnomadpy/dacli/internal/governance/checkout"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
)

var ErrUnsupportedContextBackend = errors.New("context-aware host backend is unsupported")

// ContextBackend is a separate cooperative host contract. It intentionally
// does not embed Backend: a synchronous legacy method cannot be made
// cancellable by wrapping it in a goroutine or timer.
type ContextBackend interface {
	CapabilitiesContext(context.Context) (Capabilities, error)
	RegisterContext(context.Context, Registration) error
	VerifyBindingContext(context.Context, Binding) error
	DispatchContext(context.Context, Dispatch) (HostResult, error)
	LookupContext(context.Context, string, Binding) (HostResult, error)
	ReadObservationContext(context.Context, string, Binding, uint64) (Observation, error)
	CancelContext(context.Context, CancellationRequest) (string, error)
	LookupCancellationContext(context.Context, CancellationRequest) (string, error)
}

// ContextCoordinator is an explicit adapter over a cooperative ContextBackend
// and one already provisioned controller identity. It owns no durable state.
type ContextCoordinator struct {
	backend                            ContextBackend
	authority                          *sessions.Authority
	controllerToken, controllerSession string
}

func NewContextCoordinator(backend ContextBackend, authority *sessions.Authority, controllerToken, controllerSession string) ContextCoordinator {
	return ContextCoordinator{backend: backend, authority: authority, controllerToken: controllerToken, controllerSession: controllerSession}
}

func (c ContextCoordinator) DispatchControllerOnce(ctx context.Context, permit sessions.ControllerHostDispatchPermit, req Dispatch, worker AuthenticatedBinding, beforeEffect func(context.Context) error) (HostResult, error) {
	if err := contextReady(ctx); err != nil {
		return HostResult{}, err
	}
	if c.backend == nil {
		return HostResult{}, ErrUnsupportedContextBackend
	}
	if beforeEffect == nil {
		return HostResult{}, ErrDenied
	}
	now := time.Now()
	if c.authority == nil || c.controllerToken == "" || c.controllerSession == "" || !controllerDispatchRequestValid(req, worker, now) {
		return HostResult{}, ErrDenied
	}
	if err := contextReady(ctx); err != nil {
		return HostResult{}, err
	}
	currentWorker, err := c.authority.Authenticate(worker.token, worker.binding.Session)
	if err != nil || currentWorker.Revoked || !now.Before(currentWorker.ExpiresAt) || currentWorker.ID != worker.attempt.SessionID {
		return HostResult{}, ErrDenied
	}
	if err = contextReady(ctx); err != nil {
		return HostResult{}, err
	}
	if _, err = c.authority.Authorize(worker.token, worker.binding.Session, worker.binding.Workspace, "lease.acquire", "task:"+worker.binding.Task); err != nil {
		return HostResult{}, ErrDenied
	}
	if err = contextReady(ctx); err != nil {
		return HostResult{}, err
	}
	reservation, err := c.authority.ReadDispatchReservation(worker.token, worker.binding.Session, req.DispatchID, req.ExpectedFence)
	if err != nil || !dispatchReservationMatchesWorker(reservation, req, worker) {
		return HostResult{}, ErrDenied
	}
	if err = contextReady(ctx); err != nil {
		return HostResult{}, err
	}
	ref := permit.Reference()
	if !controllerReferenceMatchesRequest(ref, req, worker) {
		return HostResult{}, ErrDenied
	}
	caps, err := c.backend.CapabilitiesContext(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return HostResult{}, ctx.Err()
		}
		return HostResult{}, ErrUnsupportedContextBackend
	}
	if err = contextReady(ctx); err != nil {
		return HostResult{}, err
	}
	if caps.Version != ContractVersion || caps.Host != req.Binding.Host || caps.Dispatch.State != Supported || caps.Lookup.State != Supported || caps.PrivateProfileHandoff.State != Supported {
		return HostResult{}, ErrUnsupportedContextBackend
	}
	registration := Registration{Version: ContractVersion, Binding: worker.binding, Capabilities: caps}
	if ValidateRegistration(c.authority, worker, registration, now) != nil {
		return HostResult{}, ErrDenied
	}
	if err = beforeEffect(ctx); err != nil {
		return HostResult{}, err
	}
	if err = contextReady(ctx); err != nil {
		return HostResult{}, err
	}
	intent, err := c.authority.ConsumeControllerHostDispatch(c.controllerToken, c.controllerSession, permit)
	if err != nil || intent.ControllerSessionID != c.controllerSession || !reflect.DeepEqual(intent.Reservation, reservation) || !controllerIntentMatchesRequest(intent, ref, req, worker) {
		return HostResult{}, ErrDenied
	}
	// The permit is an in-process one-use grant; the intent is already durable.
	// From this point a lost grant is never recreated or turned into a new key.
	if contextReady(ctx) != nil {
		return HostResult{}, ErrUncertain
	}
	if caps, err = c.validateDispatchBoundary(ctx, ref, req, worker, beforeEffect); err != nil {
		return HostResult{}, err
	}
	registration.Capabilities = caps
	if err = c.backend.RegisterContext(ctx, registration); err != nil {
		if contextReady(ctx) != nil {
			return HostResult{}, ErrUncertain
		}
		return c.lookupAfterUncertainDispatch(ctx, req)
	}
	if _, err = c.validateDispatchBoundary(ctx, ref, req, worker, beforeEffect); err != nil {
		return HostResult{}, err
	}
	if err = c.backend.VerifyBindingContext(ctx, worker.binding); err != nil {
		if ctx.Err() != nil {
			return HostResult{}, ErrUncertain
		}
		return HostResult{}, ErrUncertain
	}
	if _, err = c.validateDispatchBoundary(ctx, ref, req, worker, beforeEffect); err != nil {
		return HostResult{}, err
	}
	result, dispatchErr := c.backend.DispatchContext(ctx, req)
	if dispatchErr == nil && hostResultMatches(result, req) {
		return result, nil
	}
	return c.lookupAfterUncertainDispatch(ctx, req)
}

// validateDispatchBoundary rereads the cooperative host capability and the
// original controller, worker, reservation and intent after every slow host
// callback. The caller guard additionally pins the application profile and
// validates the exact current controller action. It never consumes a permit.
func (c ContextCoordinator) validateDispatchBoundary(ctx context.Context, ref sessions.ControllerHostDispatchReference, req Dispatch, worker AuthenticatedBinding, beforeEffect func(context.Context) error) (Capabilities, error) {
	if err := contextReady(ctx); err != nil {
		return Capabilities{}, err
	}
	caps, err := c.backend.CapabilitiesContext(ctx)
	if err != nil {
		if contextReady(ctx) != nil {
			return Capabilities{}, contextReady(ctx)
		}
		return Capabilities{}, ErrUnsupportedContextBackend
	}
	if err = contextReady(ctx); err != nil {
		return Capabilities{}, err
	}
	if caps.Version != ContractVersion || caps.Host != req.Binding.Host || caps.Dispatch.State != Supported || caps.Lookup.State != Supported || caps.PrivateProfileHandoff.State != Supported {
		return Capabilities{}, ErrUnsupportedContextBackend
	}
	controller, err := c.authority.Authenticate(c.controllerToken, c.controllerSession)
	if err != nil || controller.Revoked || !time.Now().Before(controller.ExpiresAt) {
		return Capabilities{}, ErrDenied
	}
	currentWorker, err := c.authority.Authenticate(worker.token, worker.binding.Session)
	if err != nil || currentWorker.Revoked || !time.Now().Before(currentWorker.ExpiresAt) || currentWorker.ID != worker.attempt.SessionID {
		return Capabilities{}, ErrDenied
	}
	if _, err = c.authority.Authorize(worker.token, worker.binding.Session, worker.binding.Workspace, "lease.acquire", "task:"+worker.binding.Task); err != nil {
		return Capabilities{}, ErrDenied
	}
	reservation, err := c.authority.ReadDispatchReservation(worker.token, worker.binding.Session, req.DispatchID, req.ExpectedFence)
	if err != nil || !dispatchReservationMatchesWorker(reservation, req, worker) {
		return Capabilities{}, ErrDenied
	}
	intent, err := c.authority.DispatchIntentStatus(c.controllerToken, c.controllerSession, req.DispatchID)
	if err != nil || intent.ControllerSessionID != c.controllerSession || !reflect.DeepEqual(intent.Reservation, reservation) || !controllerIntentMatchesRequest(intent, ref, req, worker) {
		return Capabilities{}, ErrDenied
	}
	registration := Registration{Version: ContractVersion, Binding: worker.binding, Capabilities: caps}
	if ValidateRegistration(c.authority, worker, registration, time.Now()) != nil {
		return Capabilities{}, ErrDenied
	}
	if err = contextReady(ctx); err != nil {
		return Capabilities{}, err
	}
	if err = beforeEffect(ctx); err != nil {
		return Capabilities{}, err
	}
	if err = contextReady(ctx); err != nil {
		return Capabilities{}, err
	}
	return caps, nil
}

func (c ContextCoordinator) lookupAfterUncertainDispatch(ctx context.Context, req Dispatch) (HostResult, error) {
	if contextReady(ctx) != nil {
		return HostResult{}, ErrUncertain
	}
	lookup, lookupErr := c.backend.LookupContext(ctx, req.OperationKey, req.Binding)
	if lookupErr == nil && hostResultMatches(lookup, req) {
		return lookup, nil
	}
	return HostResult{}, ErrUncertain
}

// LookupControllerDispatch reconstructs only read authority from a pinned
// enrollment. It never authenticates the worker for launch, registers an
// attempt, registers a host, or dispatches.
func (c ContextCoordinator) LookupControllerDispatch(ctx context.Context, req Dispatch, worker WorkerEnrollment) (HostResult, error) {
	if err := contextReady(ctx); err != nil {
		return HostResult{}, err
	}
	if c.backend == nil {
		return HostResult{}, ErrUnsupportedContextBackend
	}
	if c.authority == nil || c.controllerToken == "" || c.controllerSession == "" || !dispatchRequestMatchesEnrollment(req, worker) {
		return HostResult{}, ErrDenied
	}
	controller, err := c.authority.Authenticate(c.controllerToken, c.controllerSession)
	if err != nil || controller.Revoked || !time.Now().Before(controller.ExpiresAt) {
		return HostResult{}, ErrDenied
	}
	if err = contextReady(ctx); err != nil {
		return HostResult{}, err
	}
	intent, err := c.authority.DispatchIntentStatus(c.controllerToken, c.controllerSession, req.DispatchID)
	if err != nil || intent.ControllerSessionID != c.controllerSession || !intentMatchesEnrollment(intent, req, worker) {
		return HostResult{}, ErrDenied
	}
	if err = contextReady(ctx); err != nil {
		return HostResult{}, err
	}
	caps, err := c.backend.CapabilitiesContext(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return HostResult{}, ctx.Err()
		}
		return HostResult{}, ErrUnsupportedContextBackend
	}
	if caps.Version != ContractVersion || caps.Host != req.Binding.Host || caps.Lookup.State != Supported {
		return HostResult{}, ErrUnsupportedContextBackend
	}
	if err = c.backend.VerifyBindingContext(ctx, req.Binding); err != nil {
		return HostResult{}, ErrUncertain
	}
	if err = contextReady(ctx); err != nil {
		return HostResult{}, err
	}
	result, err := c.backend.LookupContext(ctx, req.OperationKey, req.Binding)
	if err != nil || !hostResultMatches(result, req) {
		return HostResult{}, ErrUncertain
	}
	return result, nil
}

func dispatchRequestMatchesEnrollment(req Dispatch, worker WorkerEnrollment) bool {
	return req.Version == ContractVersion && req.OperationKey != "" && req.DispatchID != "" && req.BridgeSession != "" && req.ExpectedFence != 0 && req.PromptSpecDigest == DispatchSpecDigest(req) && req.PromptSpecDigest != "" && req.Repository != "" && req.Checkout == worker.AttemptRequest.Checkout && worker.AttemptRequest.RequestID != "" && worker.AttemptRequest.TaskID == worker.Binding.Task && worker.Binding.Attempt != "" && worker.Binding.Project != "" && worker.Binding.Workspace != "" && worker.Profile.ID == worker.Binding.CredentialID && worker.Profile.WorkerSession == worker.Binding.Session && worker.Binding.same(req.Binding) && worker.Profile == req.Profile && worker.AttemptRequest.Checkout == req.Checkout && worker.Binding.Task == req.Binding.Task && worker.Binding.Session == req.Binding.Session
}

func intentMatchesEnrollment(intent sessions.DispatchIntent, req Dispatch, worker WorkerEnrollment) bool {
	historical := AuthenticatedBinding{
		binding: worker.Binding,
		profile: worker.Profile,
		attempt: sessions.Attempt{SessionID: worker.Binding.Session, ProjectID: worker.Binding.Project, Request: worker.AttemptRequest, Observation: checkout.Observation{Repository: req.Repository, Locator: req.Checkout}},
	}
	if worker.ExpectedHostSlotScope != nil {
		historical.expectedHostSlot = *worker.ExpectedHostSlotScope
		historical.hasHostSlotScope = true
	}
	return controllerIntentMatchesRequest(intent, sessions.ControllerHostDispatchReference{ProjectID: worker.Binding.Project, DispatchID: req.DispatchID, OriginalOperationKey: req.OperationKey, ReservationFence: req.ExpectedFence}, req, historical)
}

func contextReady(ctx context.Context) error {
	if ctx == nil {
		return ErrDenied
	}
	return ctx.Err()
}
