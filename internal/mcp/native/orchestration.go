package native

import (
	"reflect"

	"github.com/mlnomadpy/dacli/internal/application"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
)

type swarmPlanArgs struct {
	version
	Project string `json:"project"`
}
type loopStartRequest struct {
	OperationKey string `json:"operation_key"`
	InvocationID string `json:"invocation_id"`
	CheckpointID string `json:"checkpoint_id"`
}
type loopStartArgs struct {
	version
	Request loopStartRequest `json:"request"`
}
type loopAdvanceArgs struct {
	version
	Request sessions.ControllerStepRequest `json:"request"`
}
type loopStatusArgs struct {
	version
	Project      string `json:"project"`
	InvocationID string `json:"invocation_id"`
}
type dispatchStatusArgs struct {
	version
	DispatchID string `json:"dispatch_id"`
}
type dispatchCorrelateArgs struct {
	version
	Request sessions.CorrelateDispatchIntent `json:"request"`
}
type dispatchAcknowledgeArgs struct {
	version
	Request sessions.AcknowledgeDispatchIntent `json:"request"`
}
type lifecycleObserveArgs struct {
	version
	Request sessions.ObserveLifecycleRequest `json:"request"`
}
type lifecycleCancelArgs struct {
	version
	Request sessions.CancelLifecycleRequest `json:"request"`
}
type lifecycleAcknowledgeArgs struct {
	version
	Request sessions.AcknowledgeCancellationRequest `json:"request"`
}

// These catalogs express the selected connection's role, never caller labels.
// Shared services still authenticate the exact actor, task, operation and fence.
var orchestrationRoles = map[string][]string{
	"controller": {"plan_swarm", "start_loop", "get_loop_status", "step_loop", "reconcile_loop", "stop_loop", "cancel_dispatch", "get_dispatch_status", "get_dispatch_lifecycle"},
	"bridge":     {"correlate_dispatch", "observe_dispatch", "acknowledge_cancellation", "get_dispatch_status", "get_dispatch_lifecycle"},
	"worker":     {"acknowledge_dispatch", "get_dispatch_status", "get_dispatch_lifecycle"},
}
var orchestrationReads = []string{"identity", "workspace_status", "task_list", "task_get", "get_policy_status", "get_orchestration_host_status"}

func init() {
	for n, v := range map[string]any{
		"get_orchestration_host_status": version{}, "plan_swarm": swarmPlanArgs{}, "start_loop": loopStartArgs{}, "get_loop_status": loopStatusArgs{}, "step_loop": loopAdvanceArgs{}, "reconcile_loop": loopAdvanceArgs{}, "stop_loop": loopAdvanceArgs{}, "cancel_dispatch": lifecycleCancelArgs{}, "get_dispatch_status": dispatchStatusArgs{}, "get_dispatch_lifecycle": dispatchStatusArgs{}, "correlate_dispatch": dispatchCorrelateArgs{}, "observe_dispatch": lifecycleObserveArgs{}, "acknowledge_dispatch": dispatchAcknowledgeArgs{}, "acknowledge_cancellation": lifecycleAcknowledgeArgs{},
	} {
		arguments[n] = v
	}
	for n, v := range map[string]any{
		"get_orchestration_host_status": application.NativeOrchestrationHostStatus{}, "plan_swarm": sessions.SwarmPlan{}, "start_loop": sessions.ControllerStatus{}, "get_loop_status": sessions.ControllerStatus{}, "step_loop": sessions.ControllerStatus{}, "reconcile_loop": sessions.ControllerStatus{}, "stop_loop": sessions.ControllerStatus{}, "cancel_dispatch": sessions.LifecycleCancellationResult{}, "get_dispatch_status": sessions.DispatchIntent{}, "get_dispatch_lifecycle": sessions.LifecycleStatus{}, "correlate_dispatch": sessions.DispatchIntent{}, "observe_dispatch": sessions.LifecycleObservation{}, "acknowledge_dispatch": sessions.DispatchIntent{}, "acknowledge_cancellation": sessions.LifecycleCancellation{},
	} {
		outputs[n] = v
	}
}
func (s *Server) orchestrationToolAllowed(name string) bool {
	for _, n := range orchestrationReads {
		if name == n {
			return true
		}
	}
	for _, n := range orchestrationRoles[s.orchestration.Role()] {
		if name == n {
			return true
		}
	}
	return false
}
func (s *Server) orchestrationCatalog() []tool {
	names := append(append([]string{}, orchestrationReads...), orchestrationRoles[s.orchestration.Role()]...)
	out := []tool{}
	for _, n := range names {
		out = append(out, tool{n, "Scoped owner-pinned cooperative orchestration; host actions require first-use permission and independently enrolled child connections; no commands, credentials or grant overrides.", schema(reflect.TypeOf(arguments[n])), outputSchema(n)})
	}
	return out
}
func (s *Server) orchestrationCall(name string, arg any) (any, error, bool) {
	p := s.orchestration
	if name == "get_orchestration_host_status" {
		d, e := p.HostStatus()
		return d, e, true
	}
	switch x := arg.(type) {
	case *swarmPlanArgs:
		d, e := p.Plan(x.Project)
		return d, e, true
	case *loopStartArgs:
		d, e := p.Start(x.Request.OperationKey, x.Request.InvocationID, x.Request.CheckpointID)
		return d, e, true
	case *loopStatusArgs:
		d, e := p.Status(x.Project, x.InvocationID)
		return d, e, true
	case *loopAdvanceArgs:
		mode := map[string]string{"step_loop": "step", "reconcile_loop": "reconcile", "stop_loop": "stop"}[name]
		d, e := p.Advance(x.Request, mode)
		return d, e, true
	case *dispatchStatusArgs:
		if name == "get_dispatch_lifecycle" {
			d, e := p.LifecycleStatus(x.DispatchID)
			return d, e, true
		}
		d, e := p.DispatchStatus(x.DispatchID)
		return d, e, true
	case *dispatchCorrelateArgs:
		d, e := p.Correlate(x.Request)
		return d, e, true
	case *dispatchAcknowledgeArgs:
		d, e := p.Acknowledge(x.Request)
		return d, e, true
	case *lifecycleObserveArgs:
		d, e := p.Observe(x.Request)
		return d, e, true
	case *lifecycleCancelArgs:
		d, e := p.Cancel(x.Request)
		return d, e, true
	case *lifecycleAcknowledgeArgs:
		d, e := p.AcknowledgeCancellation(x.Request)
		return d, e, true
	}
	return nil, nil, false
}
