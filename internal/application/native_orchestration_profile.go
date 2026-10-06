package application

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"reflect"

	"github.com/mlnomadpy/dacli/internal/governance/nativeconfig"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/store"
)

// NativeOrchestrationConfig is owner-selected wiring, not enrollment. It contains
// no credentials or commands. Routes cannot replace the actors' independent
// scope, grants, or fresh admission checks in the shared controller service.
type NativeOrchestrationConfig struct {
	SchemaVersion    int                                     `json:"schema_version"`
	WorkspaceID      string                                  `json:"workspace_id"`
	ProjectID        string                                  `json:"project_id"`
	SessionID        string                                  `json:"session_id"`
	Role             string                                  `json:"role"`
	CapabilityFile   string                                  `json:"capability_file"`
	CapabilitySHA256 string                                  `json:"capability_sha256"`
	PolicyRevision   string                                  `json:"policy_revision,omitempty"`
	Routes           map[string]sessions.ControllerTaskRoute `json:"routes,omitempty"`
}
type NativeOrchestrationProfile struct {
	path      string
	config    NativeOrchestrationConfig
	digest    [32]byte
	swarm     *NativeSwarmAccess
	lifecycle *NativeLifecycleAccess
}

func OpenNativeOrchestrationProfile(connection, config string) (*NativeOrchestrationProfile, error) {
	data, e := nativeconfig.ReadPrivate(config, 16384)
	var c NativeOrchestrationConfig
	if e != nil || nativeconfig.Decode(data, &c) != nil || c.SchemaVersion != 1 || c.WorkspaceID == "" || c.ProjectID == "" || c.SessionID == "" || !filepath.IsAbs(c.CapabilityFile) || !probeHash(c.CapabilitySHA256) {
		return nil, sessions.ErrDenied
	}
	if (c.Role != "controller" && c.Role != "bridge" && c.Role != "worker") || (c.Role == "controller" && (c.PolicyRevision == "" || len(c.Routes) == 0)) || (c.Role != "controller" && (c.PolicyRevision != "" || len(c.Routes) != 0)) {
		return nil, sessions.ErrDenied
	}
	swarm, e := OpenNativeSwarm(connection, c.CapabilityFile)
	if e != nil {
		return nil, sessions.ErrDenied
	}
	lifecycle, e := OpenNativeLifecycle(connection)
	if e != nil {
		return nil, sessions.ErrDenied
	}
	p := &NativeOrchestrationProfile{path: config, config: c, digest: sha256.Sum256(data), swarm: swarm, lifecycle: lifecycle}
	if _, e = p.Authenticate(); e != nil {
		return nil, e
	}
	return p, nil
}
func (p *NativeOrchestrationProfile) Role() string    { return p.config.Role }
func (p *NativeOrchestrationProfile) Project() string { return p.config.ProjectID }
func (p *NativeOrchestrationProfile) Authenticate() (sessions.Session, error) {
	a, t, e := p.authenticate()
	if e != nil {
		return sessions.Session{}, e
	}
	return a.Authenticate(t, p.config.SessionID)
}
func (p *NativeOrchestrationProfile) authenticate() (*sessions.Authority, string, error) {
	data, e := nativeconfig.ReadPrivate(p.path, 16384)
	var c NativeOrchestrationConfig
	if e != nil || sha256.Sum256(data) != p.digest || nativeconfig.Decode(data, &c) != nil || !reflect.DeepEqual(c, p.config) {
		return nil, "", sessions.ErrDenied
	}
	capability, e := nativeconfig.ReadPrivate(c.CapabilityFile, 16384)
	digest := sha256.Sum256(capability)
	var host ManualSwarmCapability
	if e != nil || hex.EncodeToString(digest[:]) != c.CapabilitySHA256 || nativeconfig.Decode(capability, &host) != nil || host.WorkspaceID != c.WorkspaceID || host.ProjectID != c.ProjectID || host.Harness != "codex" || host.SchemaVersion != 2 || host.Codex == nil || host.Source != "owner-manual-native-handoff" || host.Codex.SchemaVersion != 1 || host.Codex.NativeHostAttestation != "owner-observed-native-collaboration" || host.Codex.ChildProfileChannel != "owner-manual-separate-process" {
		return nil, "", sessions.ErrDenied
	}
	a, t, e := p.swarm.authenticate()
	if e != nil {
		return nil, "", e
	}
	connection := p.swarm.connection
	if connection.WorkspaceID != c.WorkspaceID || connection.SessionID != c.SessionID {
		return nil, "", sessions.ErrDenied
	}
	if c.Role == "worker" {
		// Owner native enrollment is task-scoped (core624); a project grant
		// would widen worker authority merely to initialize transport wiring.
		if _, e = a.Authorize(t, c.SessionID, c.WorkspaceID, "read", "workspace:"+c.WorkspaceID); e != nil {
			return nil, "", sessions.ErrDenied
		}
		w, e := nativeconfig.ResolveWorkspace(connection.Workspace, c.WorkspaceID)
		if e != nil {
			return nil, "", sessions.ErrDenied
		}
		canonical, e := store.ReadCheckpointCanonical(w)
		if e != nil {
			return nil, "", sessions.ErrDenied
		}
		for _, task := range canonical.Tasks {
			if task.Project != c.ProjectID {
				continue
			}
			if _, e = a.Authorize(t, c.SessionID, c.WorkspaceID, "read", "task:"+task.ID); e != nil {
				continue
			}
			if _, e = a.Authorize(t, c.SessionID, c.WorkspaceID, "lease.acquire", "task:"+task.ID); e == nil {
				// Acknowledgement still checks the exact dispatch project,
				// assigned actor, task, checkout and fresh handshake below.
				return a, t, nil
			}
		}
		return nil, "", sessions.ErrDenied
	}
	actions := map[string][]string{"controller": {"read", "controller.start", "controller.stop", "controller.read", "controller.step", "controller.reconcile", "swarm.plan", "dispatch.cancel"}, "bridge": {"read", "dispatch.correlate", "dispatch.observe"}, "worker": {"read", "lease.acquire"}}[c.Role]
	for _, action := range actions {
		if _, e = a.Authorize(t, c.SessionID, c.WorkspaceID, action, "project:"+c.ProjectID); e != nil {
			return nil, "", sessions.ErrDenied
		}
	}
	return a, t, nil
}
func (p *NativeOrchestrationProfile) Plan(project string) (sessions.SwarmPlan, error) {
	if p.config.Role != "controller" || project != p.config.ProjectID {
		return sessions.SwarmPlan{}, sessions.ErrDenied
	}
	if _, _, e := p.authenticate(); e != nil {
		return sessions.SwarmPlan{}, e
	}
	return p.swarm.Plan(project)
}
func (p *NativeOrchestrationProfile) Start(operation, invocation, checkpoint string) (sessions.ControllerStatus, error) {
	if p.config.Role != "controller" {
		return sessions.ControllerStatus{}, sessions.ErrDenied
	}
	a, t, e := p.authenticate()
	if e != nil {
		return sessions.ControllerStatus{}, e
	}
	if e = (manualSwarmCapabilityFile{p.config.CapabilityFile}).AdmitDispatchCapability(p.config.WorkspaceID, p.config.ProjectID, "codex", "sha256:"+p.config.CapabilitySHA256); e != nil {
		return sessions.ControllerStatus{}, e
	}
	return a.StartScopedController(t, p.config.SessionID, sessions.ControllerStartRequest{OperationKey: operation, InvocationID: invocation, CheckpointID: checkpoint, ProjectID: p.config.ProjectID, PolicyRevision: p.config.PolicyRevision, ControllerSessionID: p.config.SessionID, Routes: p.config.Routes})
}
func (p *NativeOrchestrationProfile) Advance(r sessions.ControllerStepRequest, mode string) (sessions.ControllerStatus, error) {
	if p.config.Role != "controller" || r.ProjectID != p.config.ProjectID {
		return sessions.ControllerStatus{}, sessions.ErrDenied
	}
	a, t, e := p.authenticate()
	if e != nil {
		return sessions.ControllerStatus{}, e
	}
	switch mode {
	case "step":
		return a.StepController(t, p.config.SessionID, r)
	case "reconcile":
		return a.ReconcileController(t, p.config.SessionID, r)
	case "stop":
		return a.StopController(t, p.config.SessionID, r)
	}
	return sessions.ControllerStatus{}, sessions.ErrDenied
}
func (p *NativeOrchestrationProfile) Status(project, invocation string) (sessions.ControllerStatus, error) {
	if p.config.Role != "controller" || project != p.config.ProjectID {
		return sessions.ControllerStatus{}, sessions.ErrDenied
	}
	a, t, e := p.authenticate()
	if e != nil {
		return sessions.ControllerStatus{}, e
	}
	return a.ControllerStatus(t, p.config.SessionID, project, invocation)
}
func (p *NativeOrchestrationProfile) dispatchInProject(id string) error {
	d, e := p.swarm.DispatchStatus(id)
	if e != nil || d.Reservation.ProjectID != p.config.ProjectID {
		return sessions.ErrDenied
	}
	return nil
}
func (p *NativeOrchestrationProfile) Correlate(r sessions.CorrelateDispatchIntent) (sessions.DispatchIntent, error) {
	if p.config.Role != "bridge" {
		return sessions.DispatchIntent{}, sessions.ErrDenied
	}
	if _, _, e := p.authenticate(); e != nil {
		return sessions.DispatchIntent{}, e
	}
	if e := p.dispatchInProject(r.DispatchID); e != nil {
		return sessions.DispatchIntent{}, e
	}
	return p.swarm.CorrelateDispatch(r)
}
func (p *NativeOrchestrationProfile) Acknowledge(r sessions.AcknowledgeDispatchIntent) (sessions.DispatchIntent, error) {
	if p.config.Role != "worker" {
		return sessions.DispatchIntent{}, sessions.ErrDenied
	}
	if _, _, e := p.authenticate(); e != nil {
		return sessions.DispatchIntent{}, e
	}
	if e := p.dispatchInProject(r.DispatchID); e != nil {
		return sessions.DispatchIntent{}, e
	}
	return p.swarm.AcknowledgeDispatch(r)
}
func (p *NativeOrchestrationProfile) DispatchStatus(id string) (sessions.DispatchIntent, error) {
	if _, _, e := p.authenticate(); e != nil {
		return sessions.DispatchIntent{}, e
	}
	if e := p.dispatchInProject(id); e != nil {
		return sessions.DispatchIntent{}, e
	}
	return p.swarm.DispatchStatus(id)
}
func (p *NativeOrchestrationProfile) Observe(r sessions.ObserveLifecycleRequest) (sessions.LifecycleObservation, error) {
	if p.config.Role != "bridge" {
		return sessions.LifecycleObservation{}, sessions.ErrDenied
	}
	if _, _, e := p.authenticate(); e != nil {
		return sessions.LifecycleObservation{}, e
	}
	if e := p.dispatchInProject(r.Binding.DispatchID); e != nil {
		return sessions.LifecycleObservation{}, e
	}
	return p.lifecycle.Observe(r)
}
func (p *NativeOrchestrationProfile) Cancel(r sessions.CancelLifecycleRequest) (sessions.LifecycleCancellationResult, error) {
	if p.config.Role != "controller" {
		return sessions.LifecycleCancellationResult{}, sessions.ErrDenied
	}
	if _, _, e := p.authenticate(); e != nil {
		return sessions.LifecycleCancellationResult{}, e
	}
	if e := p.dispatchInProject(r.Binding.DispatchID); e != nil {
		return sessions.LifecycleCancellationResult{}, e
	}
	return p.lifecycle.Cancel(r)
}
func (p *NativeOrchestrationProfile) AcknowledgeCancellation(r sessions.AcknowledgeCancellationRequest) (sessions.LifecycleCancellation, error) {
	if p.config.Role != "bridge" {
		return sessions.LifecycleCancellation{}, sessions.ErrDenied
	}
	if _, _, e := p.authenticate(); e != nil {
		return sessions.LifecycleCancellation{}, e
	}
	if e := p.dispatchInProject(r.Binding.DispatchID); e != nil {
		return sessions.LifecycleCancellation{}, e
	}
	return p.lifecycle.Acknowledge(r)
}
func (p *NativeOrchestrationProfile) LifecycleStatus(id string) (sessions.LifecycleStatus, error) {
	if _, _, e := p.authenticate(); e != nil {
		return sessions.LifecycleStatus{}, e
	}
	if e := p.dispatchInProject(id); e != nil {
		return sessions.LifecycleStatus{}, e
	}
	return p.lifecycle.Status(id)
}

// NativeOrchestrationHostStatus describes the cooperative adapter boundary, never an effect permit.
type NativeOrchestrationHostStatus struct {
	Harness              string `json:"harness"`
	DispatchChannel      string `json:"dispatch_channel"`
	AutomaticDispatch    string `json:"automatic_dispatch"`
	PrivateChildDelivery string `json:"private_child_delivery"`
	CancellationChannel  string `json:"cancellation_channel"`
	AutomaticTermination string `json:"automatic_termination"`
	EffectAuthorized     bool   `json:"effect_authorized"`
}

func (p *NativeOrchestrationProfile) HostStatus() (NativeOrchestrationHostStatus, error) {
	if _, _, e := p.authenticate(); e != nil {
		return NativeOrchestrationHostStatus{}, e
	}
	return NativeOrchestrationHostStatus{Harness: "codex", DispatchChannel: "owner-manual-separate-process", AutomaticDispatch: "unsupported", PrivateChildDelivery: "owner-provisioned", CancellationChannel: "owner-manual-native-host", AutomaticTermination: "unverified", EffectAuthorized: false}, nil
}

// Routing reads a historical dispatch/correction selection for the configured
// controller and project. A future-turn route is not a host execution permit or
// a change to the current writer's identity/model.
func (p *NativeOrchestrationProfile) Routing(project, id string) (sessions.NativeModelRoutingSelection, error) {
	if p.config.Role != "controller" || project != p.config.ProjectID {
		return sessions.NativeModelRoutingSelection{}, sessions.ErrDenied
	}
	if _, _, e := p.authenticate(); e != nil {
		return sessions.NativeModelRoutingSelection{}, e
	}
	return p.swarm.Routing(project, id)
}
