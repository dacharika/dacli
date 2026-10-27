package executor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/governance/sessions"
)

type contextTestBackend struct {
	base                                *fakeHost
	capsCalls, verifyCalls, lookupCalls int
	registerCalls, dispatchCalls        int
	lookupKeys                          []string
	lookupErr                           error
	capabilityErr                       error
	afterRegister                       func()
	afterVerifyBinding                  func()
}

func (b *contextTestBackend) CapabilitiesContext(ctx context.Context) (Capabilities, error) {
	b.capsCalls++
	if err := ctx.Err(); err != nil {
		return Capabilities{}, err
	}
	if b.capabilityErr != nil {
		return Capabilities{}, b.capabilityErr
	}
	return b.base.Capabilities(), nil
}
func (b *contextTestBackend) RegisterContext(ctx context.Context, r Registration) error {
	b.registerCalls++
	if err := ctx.Err(); err != nil {
		return err
	}
	err := b.base.Register(r)
	if b.afterRegister != nil {
		b.afterRegister()
	}
	return err
}
func (b *contextTestBackend) VerifyBindingContext(ctx context.Context, binding Binding) error {
	b.verifyCalls++
	if err := ctx.Err(); err != nil {
		return err
	}
	err := b.base.VerifyBinding(binding)
	if b.afterVerifyBinding != nil {
		b.afterVerifyBinding()
	}
	return err
}
func (b *contextTestBackend) DispatchContext(ctx context.Context, req Dispatch) (HostResult, error) {
	b.dispatchCalls++
	if err := ctx.Err(); err != nil {
		return HostResult{}, err
	}
	return b.base.Dispatch(req)
}
func (b *contextTestBackend) LookupContext(ctx context.Context, key string, binding Binding) (HostResult, error) {
	b.lookupCalls++
	b.lookupKeys = append(b.lookupKeys, key)
	if err := ctx.Err(); err != nil {
		return HostResult{}, err
	}
	if b.lookupErr != nil {
		return HostResult{}, b.lookupErr
	}
	return b.base.Lookup(key, binding)
}
func (b *contextTestBackend) ReadObservationContext(ctx context.Context, key string, binding Binding, after uint64) (Observation, error) {
	if err := ctx.Err(); err != nil {
		return Observation{}, err
	}
	return Observation{}, ErrUncertain
}
func (b *contextTestBackend) CancelContext(ctx context.Context, req CancellationRequest) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return "unknown", nil
}
func (b *contextTestBackend) LookupCancellationContext(ctx context.Context, req CancellationRequest) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return "unknown", nil
}

var _ ContextBackend = (*contextTestBackend)(nil)

func TestContextCoordinatorRequiresCooperativeBackendAndLiveContext(t *testing.T) {
	f, worker, req := controllerExecutorRequest(t)
	coordinator := NewContextCoordinator(nil, f.a, f.controllerToken, f.controller.ID)
	if _, err := coordinator.DispatchControllerOnce(context.Background(), sessions.ControllerHostDispatchPermit{}, req, worker, func(context.Context) error { return nil }); !errors.Is(err, ErrUnsupportedContextBackend) {
		t.Fatalf("nil cooperative host should be explicitly unsupported, got %v", err)
	}
	backend := &contextTestBackend{base: &fakeHost{store: &fakeStore{byKey: map[string]HostResult{}, digests: map[string]string{}, observed: map[string]Observation{}}, enrolled: worker.Claim()}}
	coordinator = NewContextCoordinator(backend, f.a, f.controllerToken, f.controller.ID)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := coordinator.DispatchControllerOnce(ctx, sessions.ControllerHostDispatchPermit{}, req, worker, func(context.Context) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled call should stop before backend use, got %v", err)
	}
	if backend.capsCalls != 0 || backend.registerCalls != 0 || backend.dispatchCalls != 0 || backend.lookupCalls != 0 {
		t.Fatalf("cancelled call reached backend: %+v", backend)
	}
}

func TestContextCoordinatorRecoveryUsesOnlyOriginalKeyLookup(t *testing.T) {
	f, worker, req := controllerExecutorRequest(t)
	intent, err := f.a.RequestDispatchIntent(f.controllerToken, f.controller.ID, sessions.RequestDispatchIntent{OperationKey: req.OperationKey, DispatchID: req.DispatchID, BridgeSessionID: req.BridgeSession, WorkerSessionID: req.Binding.Session, PromptSpecDigest: req.PromptSpecDigest, ExpectedFence: req.ExpectedFence})
	if err != nil {
		t.Fatalf("record original intent: %v", err)
	}
	want := HostResult{Version: ContractVersion, OperationKey: req.OperationKey, DispatchID: req.DispatchID, HostExecutionID: "context-original-execution", State: "accepted", Repository: req.Repository, Checkout: req.Checkout, Binding: req.Binding}
	store := &fakeStore{byKey: map[string]HostResult{req.OperationKey: want}, digests: map[string]string{}, observed: map[string]Observation{}}
	backend := &contextTestBackend{base: &fakeHost{store: store, enrolled: worker.Claim()}}
	enrollment := WorkerEnrollment{Binding: worker.binding, Profile: worker.profile, AttemptRequest: worker.attempt.Request}
	coordinator := NewContextCoordinator(backend, f.a, f.controllerToken, f.controller.ID)
	got, err := coordinator.LookupControllerDispatch(context.Background(), req, enrollment)
	if err != nil || got.HostExecutionID != want.HostExecutionID || intent.Intent.OperationKey != req.OperationKey {
		t.Fatalf("context recovery: got=%+v err=%v intent=%+v", got, err, intent.Intent)
	}
	if backend.capsCalls != 1 || backend.verifyCalls != 1 || backend.lookupCalls != 1 || len(backend.lookupKeys) != 1 || backend.lookupKeys[0] != req.OperationKey || backend.registerCalls != 0 || backend.dispatchCalls != 0 || store.registerCalls != 0 || store.dispatchCalls != 0 || store.dispatches != 0 {
		t.Fatalf("recovery used unexpected calls: caps=%d verify=%d lookup=%v register=%d dispatch=%d launches=%d", backend.capsCalls, backend.verifyCalls, backend.lookupKeys, backend.registerCalls, backend.dispatchCalls, store.dispatches)
	}
	delete(store.byKey, req.OperationKey)
	if _, err := coordinator.LookupControllerDispatch(context.Background(), req, enrollment); !errors.Is(err, ErrUncertain) {
		t.Fatalf("context lookup miss should stay uncertain, got %v", err)
	}
	if backend.lookupCalls != 2 || backend.lookupKeys[1] != req.OperationKey || backend.registerCalls != 0 || backend.dispatchCalls != 0 {
		t.Fatalf("lookup miss retried or changed key: lookups=%v register=%d dispatch=%d", backend.lookupKeys, backend.registerCalls, backend.dispatchCalls)
	}
}

func TestContextCoordinatorDoesNotCallHostAfterAuthorityReadCancellation(t *testing.T) {
	f, worker, req := controllerExecutorRequest(t)
	backend := &contextTestBackend{base: &fakeHost{store: &fakeStore{byKey: map[string]HostResult{}, digests: map[string]string{}, observed: map[string]Observation{}}, enrolled: worker.Claim()}}
	coordinator := NewContextCoordinator(backend, f.a, f.controllerToken, f.controller.ID)
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if _, err := coordinator.LookupControllerDispatch(ctx, req, WorkerEnrollment{Binding: worker.binding, Profile: worker.profile, AttemptRequest: worker.attempt.Request}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired context should stop before authority/backend reads, got %v", err)
	}
	if backend.capsCalls != 0 || backend.verifyCalls != 0 || backend.lookupCalls != 0 {
		t.Fatalf("expired lookup reached host backend: %+v", backend)
	}
}
