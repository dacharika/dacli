package sessions

import "testing"

// ControllerExecutorFixture exposes only isolated test plumbing to the external
// executor integration tests. It is never part of the production package API.
type ControllerExecutorFixture struct {
	f                                  controllerFixture
	Authority                          *Authority
	ControllerToken, ControllerSession string
}
type ControllerExecutorExpectedSlot struct{ DomainID, HostSessionID, CapabilityDigest, HostID, InstanceID string }
type ControllerExecutorWorker struct {
	Session              Session
	Token                string
	Request              AttemptRequest
	Repository, Checkout string
	ExpectedSlot         *ControllerExecutorExpectedSlot
}

func NewControllerExecutorFixture(t *testing.T) *ControllerExecutorFixture {
	t.Helper()
	f := pluginControllerSetup(t)
	return &ControllerExecutorFixture{f: f, Authority: f.a, ControllerToken: f.token, ControllerSession: f.s.ID}
}
func NewControllerExecutorHostSlotFixture(t *testing.T) *ControllerExecutorFixture {
	t.Helper()
	f := controllerHostSlotSetup(t)
	return &ControllerExecutorFixture{f: f, Authority: f.a, ControllerToken: f.token, ControllerSession: f.s.ID}
}
func (f *ControllerExecutorFixture) Worker(t *testing.T, task string) ControllerExecutorWorker {
	t.Helper()
	route := f.f.start.Routes[task]
	token := f.f.observer.tokens[task]
	worker, e := f.Authority.Authenticate(token, route.WorkerSession)
	if e != nil {
		t.Fatal(e)
	}
	binding, e := f.Authority.OwnerTaskBinding(f.f.owner, task)
	if e != nil {
		t.Fatal(e)
	}
	checkout := f.f.p.Placements[task].Checkout
	result := ControllerExecutorWorker{Session: worker, Token: token, Request: AttemptRequest{RequestID: "composed-controller-executor-" + task, TaskID: task, Checkout: checkout}, Repository: binding.Repository, Checkout: checkout}
	if scope := f.f.p.HostSlots; scope != nil {
		result.ExpectedSlot = &ControllerExecutorExpectedSlot{DomainID: scope.AuthorityDomainID, HostSessionID: scope.HostSessionID, CapabilityDigest: scope.CapabilityDigest, HostID: scope.HostID, InstanceID: scope.ExecutorInstanceID}
	}
	return result
}
func (f *ControllerExecutorFixture) SetPromptDigest(task, digest string) {
	route := f.f.start.Routes[task]
	route.PromptSpecDigest = digest
	f.f.start.Routes[task] = route
}
func (f *ControllerExecutorFixture) Start(t *testing.T) ControllerStatus {
	t.Helper()
	return controllerStart(t, f.f)
}
func (f *ControllerExecutorFixture) Step(t *testing.T, status ControllerStatus, key string) ControllerStatus {
	t.Helper()
	return controllerStep(t, f.f, status, key)
}
func (f *ControllerExecutorFixture) DriverStep(t *testing.T, status ControllerStatus, key string) (ControllerStatus, ControllerHostDispatchPermit) {
	t.Helper()
	next, permit, e := f.Authority.StepControllerForDriver(f.ControllerToken, f.ControllerSession, ControllerStepRequest{OperationKey: key, ProjectID: status.ProjectID, InvocationID: status.ID, ExpectedSequence: status.Sequence})
	if e != nil {
		t.Fatal("actual typed driver step", e)
	}
	return next, permit
}
func (f *ControllerExecutorFixture) Stop(t *testing.T, status ControllerStatus) {
	t.Helper()
	next, e := f.Authority.StopController(f.ControllerToken, f.ControllerSession, ControllerStepRequest{OperationKey: "composed-stop", ProjectID: status.ProjectID, InvocationID: status.ID, ExpectedSequence: status.Sequence})
	if e != nil || !next.Terminal {
		t.Fatal("actual stop failed", e)
	}
}
func (f *ControllerExecutorFixture) Restart(t *testing.T) *Authority {
	t.Helper()
	a, e := OpenCurrent(f.f.a.path, f.f.a.binding)
	if e != nil {
		t.Fatal(e)
	}
	a.admission = f.f.observer
	return a
}
func (f *ControllerExecutorFixture) ProtectedHolds(t *testing.T) int {
	t.Helper()
	s, e := f.Authority.ObserveStorage(f.f.owner)
	if e != nil {
		t.Fatal(e)
	}
	count := 0
	for _, hold := range s.Holds {
		if hold.Reason == "retained native reservation" || hold.Reason == "retained native lease" {
			count++
		}
	}
	return count
}
