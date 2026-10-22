package sessions

import (
	"os"
	"reflect"
	"testing"
	"time"
)

func pluginControllerSetup(t *testing.T) controllerFixture {
	t.Helper()
	f := controllerSetup(t)
	scope := f.s.Scope
	scope.Actions = append(scope.Actions, "controller.start", "controller.stop")
	session, token, e := f.a.Enroll(f.owner, scope, time.Now().Add(time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	f.s = session
	f.token = token
	f.start.ControllerSessionID = session.ID
	return f
}
func TestPluginScopedControllerStartStopAndReplay(t *testing.T) {
	f := pluginControllerSetup(t)
	start, e := f.a.StartScopedController(f.token, f.s.ID, f.start)
	if e != nil || start.ControllerSessionID != f.s.ID || start.HostActionAuthorized {
		t.Fatalf("scoped start lost exact actor or inferred host effect: %+v %v", start, e)
	}
	replay, e := f.a.StartScopedController(f.token, f.s.ID, f.start)
	if e != nil || !replay.Historical || replay.HostActionAuthorized || replay.Sequence != start.Sequence {
		t.Fatal("start replay authorized fresh work", e)
	}
	active := controllerStep(t, f, start, "plugin-step")
	if active.Waves != 1 || len(active.Actions) != 2 || active.RetainedConcurrency != 2 {
		t.Fatalf("reservation step did not report committed occupancy: waves=%d actions=%d retained=%d", active.Waves, len(active.Actions), active.RetainedConcurrency)
	}
	// Read through a new authority so a correct response cannot hide stale durable
	// invocation, history, or idempotent-operation records (core623).
	reopened, e := Open(f.a.path, f.a.binding)
	if e != nil {
		t.Fatal(e)
	}
	if e = reopened.transaction(false, false, func(s *state) error {
		if len(s.Dispatches) != 2 || controllerOccupancy(s, active.ProjectID) != 2 {
			t.Fatal("reservation step did not commit exactly two retained reservations")
		}
		history := s.ControllerHistory[active.ID]
		if s.ControllerInvocations[active.ID].Status.RetainedConcurrency != 2 || history[len(history)-1].RetainedConcurrency != 2 || s.ControllerOperations["plugin-step"].Status.RetainedConcurrency != 2 {
			t.Fatal("reservation step persisted stale occupancy")
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	persisted, e := reopened.ControllerStatus(f.token, f.s.ID, active.ProjectID, active.ID)
	if e != nil || persisted.RetainedConcurrency != 2 {
		t.Fatal("reopened status lost committed occupancy", persisted.RetainedConcurrency, e)
	}
	replay, e = reopened.StepController(f.token, f.s.ID, ControllerStepRequest{OperationKey: "plugin-step", ProjectID: active.ProjectID, InvocationID: active.ID, ExpectedSequence: start.Sequence})
	if e != nil || !replay.Historical || replay.HostActionAuthorized || replay.RetainedConcurrency != 2 || replay.Sequence != active.Sequence {
		t.Fatal("reservation replay lost committed occupancy", replay.RetainedConcurrency, e)
	}
	stopRequest := ControllerStepRequest{OperationKey: "plugin-stop", ProjectID: active.ProjectID, InvocationID: active.ID, ExpectedSequence: active.Sequence}
	stopped, e := f.a.StopController(f.token, f.s.ID, stopRequest)
	if e != nil || !stopped.Terminal || stopped.HaltClass != "stopped" || stopped.HostActionAuthorized || !reflect.DeepEqual(stopped.Actions, active.Actions) || stopped.RetainedConcurrency != active.RetainedConcurrency {
		t.Fatalf("stop lost retained work or claimed host termination: %+v %v", stopped, e)
	}
	replay, e = f.a.StopController(f.token, f.s.ID, stopRequest)
	if e != nil || !replay.Historical || replay.HostActionAuthorized || replay.Sequence != stopped.Sequence {
		t.Fatal("stop replay changed durable identity", e)
	}
	before, _ := os.ReadFile(f.a.path)
	if _, e = f.a.StepController(f.token, f.s.ID, ControllerStepRequest{OperationKey: "after-stop", ProjectID: active.ProjectID, InvocationID: active.ID, ExpectedSequence: stopped.Sequence}); e == nil {
		t.Fatal("stopped invocation admitted future effect")
	}
	after, _ := os.ReadFile(f.a.path)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("refused step changed authority")
	}
	stopRequest.ExpectedSequence++
	if _, e = f.a.StopController(f.token, f.s.ID, stopRequest); e == nil {
		t.Fatal("stop key accepted changed request")
	}
}
func TestPluginScopedControllerCannotBorrowOwnerOrOtherSession(t *testing.T) {
	f := pluginControllerSetup(t)
	before, _ := os.ReadFile(f.a.path)
	wrong := f.start
	wrong.ControllerSessionID = f.start.Routes["task-a"].WorkerSession
	if _, e := f.a.StartScopedController(f.token, f.s.ID, wrong); e == nil {
		t.Fatal("caller label widened enrolled controller")
	}
	refusedBytes, e := os.ReadFile(f.a.path)
	if e != nil || !reflect.DeepEqual(before, refusedBytes) {
		t.Fatal("refused other-session start changed authority", e)
	}
	scope := f.s.Scope
	scope.Actions = []string{"read", "controller.read", "controller.step", "controller.reconcile", "swarm.plan"}
	actor, token, e := f.a.Enroll(f.owner, scope, time.Now().Add(time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	before, _ = os.ReadFile(f.a.path)
	wrong = f.start
	wrong.ControllerSessionID = actor.ID
	if _, e = f.a.StartScopedController(token, actor.ID, wrong); e == nil {
		t.Fatal("missing controller.start minted invocation")
	}
	after, _ := os.ReadFile(f.a.path)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("refused scoped start changed authority")
	}
}
