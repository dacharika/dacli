package executor

import (
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/governance/sessions"
)

type countedControllerHost struct {
	*fakeHost
	lookups       []string
	verifyCalls   int
	registerCalls int
	dispatchCalls int
}

func (h *countedControllerHost) VerifyBinding(binding Binding) error {
	h.verifyCalls++
	return h.fakeHost.VerifyBinding(binding)
}

func (h *countedControllerHost) Register(registration Registration) error {
	h.registerCalls++
	return h.fakeHost.Register(registration)
}

func (h *countedControllerHost) Dispatch(req Dispatch) (HostResult, error) {
	h.dispatchCalls++
	return h.fakeHost.Dispatch(req)
}

func (h *countedControllerHost) Lookup(key string, binding Binding) (HostResult, error) {
	h.lookups = append(h.lookups, key)
	return h.fakeHost.Lookup(key, binding)
}

func controllerExecutorRequest(t *testing.T) (authorityFixture, AuthenticatedBinding, Dispatch) {
	t.Helper()
	f := makeAuthorityFixture(t)
	p := ProfileRef{ID: "worker-profile-2", WorkerSession: f.worker.ID, ScopeDigest: "sha256:scope", ExpiresAt: f.worker.ExpiresAt}
	b := Binding{Workspace: "workspace", Project: "project", Task: "task-a", Host: "codex", Session: f.worker.ID, Instance: "owner-configured-process", CredentialID: p.ID, ExpiresAt: f.worker.ExpiresAt}
	worker, err := AuthenticateWorker(f.a, f.workerToken, WorkerEnrollment{Binding: b, Profile: p, AttemptRequest: f.attempt.Request}, time.Now())
	if err != nil {
		t.Fatalf("authenticate worker: %v", err)
	}
	b = worker.Claim()
	req := Dispatch{Version: ContractVersion, OperationKey: "controller-original", DispatchID: f.dispatch.ID, BridgeSession: f.bridge.ID, ExpectedFence: f.dispatch.Reservation.Fence, Binding: b, Profile: p, Prompt: "Implement the assigned task.", Repository: f.attempt.Observation.Repository, Checkout: f.attempt.Observation.Locator}
	req.PromptSpecDigest = DispatchSpecDigest(req)
	return f, worker, req
}

func TestControllerExecutorFirstUseRequiresOpaquePermit(t *testing.T) {
	f, worker, req := controllerExecutorRequest(t)
	store := &fakeStore{byKey: map[string]HostResult{}, digests: map[string]string{}, observed: map[string]Observation{}}
	host := &fakeHost{store: store, enrolled: worker.Claim()}
	coordinator := NewCoordinator(host, f.a, f.controllerToken, f.controller.ID)
	if _, err := coordinator.DispatchControllerOnce(sessions.ControllerHostDispatchPermit{}, req, worker, time.Now()); err == nil {
		t.Fatal("zero permit authorized a controller dispatch")
	}
	if store.registerCalls != 0 || store.dispatchCalls != 0 || store.dispatches != 0 {
		t.Fatalf("zero permit reached backend effects: register=%d dispatch=%d launches=%d", store.registerCalls, store.dispatchCalls, store.dispatches)
	}
	if host.capabilityCalls != 0 {
		t.Fatalf("zero permit reached the backend before authorization: capability reads=%d", host.capabilityCalls)
	}
}

func TestControllerExecutorRecoveryLooksUpOriginalKeyOnly(t *testing.T) {
	f, worker, req := controllerExecutorRequest(t)
	requested, err := f.a.RequestDispatchIntent(f.controllerToken, f.controller.ID, sessions.RequestDispatchIntent{OperationKey: req.OperationKey, DispatchID: req.DispatchID, BridgeSessionID: req.BridgeSession, WorkerSessionID: req.Binding.Session, PromptSpecDigest: req.PromptSpecDigest, ExpectedFence: req.ExpectedFence})
	if err != nil {
		t.Fatalf("record original intent: %v", err)
	}
	want := HostResult{Version: ContractVersion, OperationKey: req.OperationKey, DispatchID: req.DispatchID, HostExecutionID: "original-execution", State: "accepted", Repository: req.Repository, Checkout: req.Checkout, Binding: req.Binding}
	store := &fakeStore{byKey: map[string]HostResult{req.OperationKey: want}, digests: map[string]string{}, observed: map[string]Observation{}}
	host := &countedControllerHost{fakeHost: &fakeHost{store: store, enrolled: worker.Claim()}}
	coordinator := NewCoordinator(host, f.a, f.controllerToken, f.controller.ID)
	got, err := coordinator.LookupControllerDispatch(req, worker)
	if err != nil || got.HostExecutionID != want.HostExecutionID || requested.Intent.OperationKey != req.OperationKey {
		t.Fatalf("original-key lookup: got=%+v err=%v intent=%+v", got, err, requested.Intent)
	}
	if store.registerCalls != 0 || store.dispatchCalls != 0 || store.dispatches != 0 || host.registerCalls != 0 || host.dispatchCalls != 0 || host.verifyCalls != 1 || len(host.lookups) != 1 || host.lookups[0] != req.OperationKey {
		t.Fatalf("recovery performed a write, wrong lookup, or relaunch: register=%d dispatch=%d launches=%d verify=%d lookups=%v", store.registerCalls, store.dispatchCalls, store.dispatches, host.verifyCalls, host.lookups)
	}
	delete(store.byKey, req.OperationKey)
	if _, err := coordinator.LookupControllerDispatch(req, worker); err != ErrUncertain {
		t.Fatalf("missing original execution should remain uncertain, got %v", err)
	}
	if len(host.lookups) != 2 || host.lookups[1] != req.OperationKey || store.registerCalls != 0 || store.dispatchCalls != 0 || store.dispatches != 0 || host.registerCalls != 0 || host.dispatchCalls != 0 || host.verifyCalls != 2 {
		t.Fatalf("missing result retried or changed effects: register=%d dispatch=%d launches=%d verify=%d lookups=%v", store.registerCalls, store.dispatchCalls, store.dispatches, host.verifyCalls, host.lookups)
	}
	capabilityCalls := host.capabilityCalls
	changed := req
	changed.OperationKey = "replacement-key"
	changed.PromptSpecDigest = DispatchSpecDigest(changed)
	if _, err := coordinator.LookupControllerDispatch(changed, worker); err == nil {
		t.Fatal("recovery accepted a replacement operation key")
	}
	if host.capabilityCalls != capabilityCalls {
		t.Fatalf("replacement operation key reached backend capability checks: before=%d after=%d", capabilityCalls, host.capabilityCalls)
	}
	if store.registerCalls != 0 || store.dispatchCalls != 0 || store.dispatches != 0 || host.registerCalls != 0 || host.dispatchCalls != 0 || host.verifyCalls != 2 || len(host.lookups) != 2 {
		t.Fatalf("replacement key reached backend: register=%d dispatch=%d launches=%d verify=%d lookups=%v", store.registerCalls, store.dispatchCalls, store.dispatches, host.verifyCalls, host.lookups)
	}
}
