package sessions_test

import (
	"errors"
	"github.com/mlnomadpy/dacli/internal/governance/executor"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"sync"
	"testing"
	"time"
)

type composedHostStore struct {
	sync.Mutex
	results                        map[string]executor.HostResult
	dispatches, registers, lookups int
	loseResponse                   bool
	lookupMiss                     bool
}
type composedControllerHost struct {
	binding executor.Binding
	store   *composedHostStore
}

func (h *composedControllerHost) Register(r executor.Registration) error {
	if e := executor.ValidateBinding(h.binding, r.Binding, time.Now()); e != nil {
		return e
	}
	h.store.Lock()
	defer h.store.Unlock()
	h.store.registers++
	return nil
}
func (h *composedControllerHost) VerifyBinding(b executor.Binding) error {
	return executor.ValidateBinding(h.binding, b, time.Now())
}
func (h *composedControllerHost) Capabilities() executor.Capabilities {
	c := executor.Capability{State: executor.Supported}
	return executor.Capabilities{Version: 1, Host: h.binding.Host, Model: c, Dispatch: c, Lookup: c, PrivateProfileHandoff: c, Observe: c, Cancel: c}
}
func (h *composedControllerHost) Dispatch(d executor.Dispatch) (executor.HostResult, error) {
	h.store.Lock()
	defer h.store.Unlock()
	h.store.dispatches++
	if prior, ok := h.store.results[d.OperationKey]; ok {
		return prior, nil
	}
	result := executor.HostResult{Version: 1, OperationKey: d.OperationKey, DispatchID: d.DispatchID, HostExecutionID: "fixture-execution-" + d.DispatchID, State: "accepted", Repository: d.Repository, Checkout: d.Checkout, Binding: d.Binding}
	h.store.results[d.OperationKey] = result
	if h.store.loseResponse {
		h.store.loseResponse = false
		return executor.HostResult{}, errors.New("fixture lost response after host effect")
	}
	return result, nil
}
func (h *composedControllerHost) Lookup(key string, b executor.Binding) (executor.HostResult, error) {
	if e := executor.ValidateBinding(h.binding, b, time.Now()); e != nil {
		return executor.HostResult{}, e
	}
	h.store.Lock()
	defer h.store.Unlock()
	h.store.lookups++
	if h.store.lookupMiss {
		return executor.HostResult{}, executor.ErrUncertain
	}
	r, ok := h.store.results[key]
	if !ok {
		return executor.HostResult{}, executor.ErrUncertain
	}
	return r, nil
}
func (h *composedControllerHost) Observe(executor.Observation) error { return nil }
func (h *composedControllerHost) Cancel(executor.CancellationRequest) (string, error) {
	return "unsupported", nil
}
func (h *composedControllerHost) counts() (int, int, int) {
	h.store.Lock()
	defer h.store.Unlock()
	return h.store.dispatches, h.store.registers, h.store.lookups
}

type composedWorker struct {
	authenticated executor.AuthenticatedBinding
	request       executor.Dispatch
	host          *composedControllerHost
}

func composedWorkerFor(t *testing.T, f *sessions.ControllerExecutorFixture, task string) composedWorker {
	t.Helper()
	return composedWorkerForScope(t, f, task, false)
}
func composedWorkerForScope(t *testing.T, f *sessions.ControllerExecutorFixture, task string, wrongExpectedSlot bool) composedWorker {
	t.Helper()
	w := f.Worker(t, task)
	p := executor.ProfileRef{ID: "private-fixture-profile-" + task, WorkerSession: w.Session.ID, ScopeDigest: "sha256:fixture-scope", ExpiresAt: w.Session.ExpiresAt}
	b := executor.Binding{Workspace: "workspace", Project: "project", Task: task, Host: "codex", Session: w.Session.ID, Instance: "fixture-instance-" + task, CredentialID: p.ID, ExpiresAt: w.Session.ExpiresAt}
	enrollment := executor.WorkerEnrollment{Binding: b, Profile: p, AttemptRequest: w.Request}
	if scope := w.ExpectedSlot; scope != nil {
		b.Host = scope.HostID
		b.Instance = scope.InstanceID
		enrollment.Binding = b
		enrollment.ExpectedHostSlotScope = &executor.ExpectedHostSlotScope{SchemaVersion: 1, AuthorityDomainID: scope.DomainID, HostSessionID: scope.HostSessionID, CapabilityDigest: scope.CapabilityDigest}
		if wrongExpectedSlot {
			enrollment.ExpectedHostSlotScope.AuthorityDomainID = "different-owner-domain"
		}
	}
	auth, e := executor.AuthenticateWorker(f.Authority, w.Token, enrollment, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	b = auth.Claim()
	d := executor.Dispatch{Version: 1, Binding: b, Profile: p, Repository: w.Repository, Checkout: w.Checkout, Prompt: "Implement the isolated exact task " + task}
	d.PromptSpecDigest = executor.DispatchSpecDigest(d)
	f.SetPromptDigest(task, d.PromptSpecDigest)
	return composedWorker{authenticated: auth, request: d, host: &composedControllerHost{binding: b, store: &composedHostStore{results: map[string]executor.HostResult{}}}}
}
func composeRequest(t *testing.T, w composedWorker, status sessions.ControllerStatus, permit sessions.ControllerHostDispatchPermit) executor.Dispatch {
	t.Helper()
	ref := permit.Reference()
	var action sessions.ControllerAction
	for _, a := range status.Actions {
		if a.DispatchID == ref.DispatchID {
			action = a
		}
	}
	if action.Kind != "host_dispatch" || action.TaskID != w.request.Binding.Task || action.OperationKey != ref.OriginalOperationKey {
		t.Fatal("permit did not identify exact newly authorized action")
	}
	d := w.request
	d.OperationKey = action.OperationKey
	d.DispatchID = action.DispatchID
	d.BridgeSession = action.ActorID
	d.ExpectedFence = action.ReservationFence
	return d
}
func controllerCoordinator(f *sessions.ControllerExecutorFixture, w composedWorker) executor.Coordinator {
	return executor.NewCoordinator(w.host, f.Authority, f.ControllerToken, f.ControllerSession)
}

func TestControllerExecutorFirstUseHandoff(t *testing.T) {
	f := sessions.NewControllerExecutorFixture(t)
	a := composedWorkerFor(t, f, "task-a")
	b := composedWorkerFor(t, f, "task-c")
	st := f.Step(t, f.Start(t), "composed-reserve")
	st, first := f.DriverStep(t, st, "composed-first")
	da := composeRequest(t, a, st, first)
	// The public standalone path must continue to refuse a bounded controller.
	negative := &composedControllerHost{binding: a.host.binding, store: &composedHostStore{results: map[string]executor.HostResult{}}}
	old := executor.NewCoordinator(negative, f.Authority, f.ControllerToken, f.ControllerSession)
	if _, e := old.DispatchOnce(da, a.authenticated, time.Now()); !errors.Is(e, executor.ErrDenied) {
		t.Fatalf("standalone controller boundary changed: %v", e)
	}
	if d, _, l := negative.counts(); d != 0 || l != 0 {
		t.Fatal("standalone path reached host execution")
	}
	got, e := controllerCoordinator(f, a).DispatchControllerOnce(first, da, a.authenticated, time.Now())
	if e != nil || got.OperationKey != da.OperationKey || got.DispatchID != da.DispatchID {
		t.Fatalf("first-use typed handoff failed: %+v %v", got, e)
	}
	st, second := f.DriverStep(t, st, "composed-second")
	db := composeRequest(t, b, st, second)
	if second.Reference().DispatchID == first.Reference().DispatchID {
		t.Fatal("width-two status reissued earlier sibling permit")
	}
	got, e = controllerCoordinator(f, b).DispatchControllerOnce(second, db, b.authenticated, time.Now())
	if e != nil || got.OperationKey != db.OperationKey || got.DispatchID != db.DispatchID {
		t.Fatalf("second typed handoff failed: %+v %v", got, e)
	}
	for _, w := range []composedWorker{a, b} {
		if d, r, l := w.host.counts(); d != 1 || r != 1 || l != 0 {
			t.Fatalf("not exactly one original effect: dispatch=%d registration=%d lookup=%d", d, r, l)
		}
	}
	if f.ProtectedHolds(t) != 2 {
		t.Fatal("host result released retained protection")
	}
}

func TestControllerExecutorCopiedPermitCannotDuplicateDispatch(t *testing.T) {
	f := sessions.NewControllerExecutorFixture(t)
	w := composedWorkerFor(t, f, "task-a")
	st := f.Step(t, f.Start(t), "copy-reserve")
	st, p := f.DriverStep(t, st, "copy-first")
	d := composeRequest(t, w, st, p)
	c := controllerCoordinator(f, w)
	var wg sync.WaitGroup
	success := make(chan bool, 8)
	for n := 0; n < 8; n++ {
		copy := p
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := c.DispatchControllerOnce(copy, d, w.authenticated, time.Now())
			success <- e == nil
		}()
	}
	wg.Wait()
	close(success)
	passed := 0
	for x := range success {
		if x {
			passed++
		}
	}
	if calls, regs, _ := w.host.counts(); passed != 1 || calls != 1 || regs != 1 {
		t.Fatalf("copied capabilities duplicated effect: successes=%d dispatch=%d registration=%d", passed, calls, regs)
	}
}

func TestControllerExecutorLostGrantAndResponseUseOriginalKey(t *testing.T) {
	for _, lostGrant := range []bool{false, true} {
		name := "lost-response"
		if lostGrant {
			name = "lost-grant"
		}
		t.Run(name, func(t *testing.T) {
			f := sessions.NewControllerExecutorFixture(t)
			w := composedWorkerFor(t, f, "task-a")
			st := f.Step(t, f.Start(t), "lost-reserve")
			st, p := f.DriverStep(t, st, "lost-first")
			d := composeRequest(t, w, st, p)
			if !lostGrant {
				w.host.store.loseResponse = true
				if _, e := controllerCoordinator(f, w).DispatchControllerOnce(p, d, w.authenticated, time.Now()); e != nil {
					t.Fatal("original-key lost response was not reconciled", e)
				}
			}
			reopened := executor.NewCoordinator(w.host, f.Restart(t), f.ControllerToken, f.ControllerSession)
			got, e := reopened.LookupControllerDispatch(d, w.authenticated)
			if lostGrant {
				if !errors.Is(e, executor.ErrUncertain) {
					t.Fatal("missing original execution invented success", e)
				}
			} else if e != nil || got.OperationKey != d.OperationKey {
				t.Fatal("restart lost original execution", e)
			}
			dispatches, registrations, lookups := w.host.counts()
			want := 1
			if lostGrant {
				want = 0
			}
			if dispatches != want || registrations != want || lookups < 1 {
				t.Fatalf("restart relaunched or registered: %d %d %d", dispatches, registrations, lookups)
			}
			if f.ProtectedHolds(t) != 2 {
				t.Fatal("uncertainty lost retained protection")
			}
		})
	}
}

func TestControllerExecutorSTOPBetweenHandoffAndConsumption(t *testing.T) {
	f := sessions.NewControllerExecutorFixture(t)
	w := composedWorkerFor(t, f, "task-a")
	st := f.Step(t, f.Start(t), "stop-reserve")
	st, p := f.DriverStep(t, st, "stop-first")
	d := composeRequest(t, w, st, p)
	f.Stop(t, st)
	if _, e := controllerCoordinator(f, w).DispatchControllerOnce(p, d, w.authenticated, time.Now()); e == nil {
		t.Fatal("STOP allowed a future host effect")
	}
	if a, b, c := w.host.counts(); a != 0 || b != 0 || c != 0 {
		t.Fatalf("STOP reached host: %d %d %d", a, b, c)
	}
	if f.ProtectedHolds(t) != 2 {
		t.Fatal("STOP lost uncertain protection")
	}
}

func TestControllerExecutorBoundHostSlotUsesIndependentExpectedScope(t *testing.T) {
	for _, wrong := range []bool{false, true} {
		name := "matching-owner-scope"
		if wrong {
			name = "substituted-owner-scope"
		}
		t.Run(name, func(t *testing.T) {
			f := sessions.NewControllerExecutorHostSlotFixture(t)
			w := composedWorkerForScope(t, f, "task-a", wrong)
			st := f.Step(t, f.Start(t), "slot-reserve")
			st, p := f.DriverStep(t, st, "slot-first")
			d := composeRequest(t, w, st, p)
			_, err := controllerCoordinator(f, w).DispatchControllerOnce(p, d, w.authenticated, time.Now())
			calls, regs, _ := w.host.counts()
			if wrong {
				if err == nil || calls != 0 || regs != 0 {
					t.Fatalf("returned host claim replaced independent scope: error=%v dispatch=%d registration=%d", err, calls, regs)
				}
			} else if err != nil || calls != 1 || regs != 1 {
				t.Fatalf("valid bound-slot controller handoff failed: error=%v dispatch=%d registration=%d", err, calls, regs)
			}
		})
	}
}
