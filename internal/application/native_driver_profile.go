package application

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"github.com/mlnomadpy/dacli/internal/governance/driver"
	"github.com/mlnomadpy/dacli/internal/governance/executor"
	"github.com/mlnomadpy/dacli/internal/governance/nativeconfig"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
)

// NativeDriverReviewDecisionSource supplies independently sourced decisions
// for the exact controller action. Returned EvidenceIDs must preserve the
// supplied action's order; the sessions authority stores persisted review
// EvidenceIDs as canonical sets.
type NativeDriverReviewDecisionSource interface {
	ReviewDecision(context.Context, sessions.ControllerAction) (NativeReviewRequest, error)
}

type nativeControllerDriverProfile struct {
	SchemaVersion        int                       `json:"schema_version"`
	WorkspaceID          string                    `json:"workspace_id"`
	InstallationID       string                    `json:"installation_id"`
	ProjectID            string                    `json:"project_id"`
	InvocationID         string                    `json:"invocation_id"`
	CheckpointID         string                    `json:"checkpoint_id"`
	ControllerConnection string                    `json:"controller_connection"`
	CapabilityFile       string                    `json:"capability_file"`
	Limits               nativeDriverProfileLimits `json:"limits"`
	Tasks                []nativeDriverTaskProfile `json:"tasks"`
}

type nativeDriverProfileLimits struct {
	MaxTransitions   uint64 `json:"max_transitions"`
	MaxEffects       uint64 `json:"max_effects"`
	MaxWork          uint64 `json:"max_work"`
	MaxNoProgress    uint64 `json:"max_no_progress"`
	MaxElapsed       int64  `json:"max_elapsed"`
	OperationTimeout int64  `json:"operation_timeout"`
	MaxSequence      uint64 `json:"max_sequence"`
}

func (l nativeDriverProfileLimits) driverLimits() driver.Limits {
	return driver.Limits{MaxTransitions: l.MaxTransitions, MaxEffects: l.MaxEffects, MaxWork: l.MaxWork, MaxNoProgress: l.MaxNoProgress, MaxElapsed: time.Duration(l.MaxElapsed), OperationTimeout: time.Duration(l.OperationTimeout), MaxSequence: l.MaxSequence}
}

type nativeDriverTaskProfile struct {
	TaskID             string                    `json:"task_id"`
	WorkerConnection   string                    `json:"worker_connection"`
	BridgeConnection   string                    `json:"bridge_connection"`
	WorkerEnrollment   executor.WorkerEnrollment `json:"worker_enrollment"`
	DispatchTemplate   executor.Dispatch         `json:"dispatch_template"`
	RunnerProfile      string                    `json:"runner_profile"`
	ReviewerConnection string                    `json:"reviewer_connection"`
	ReviewerProfile    string                    `json:"reviewer_profile"`
}

type nativeDriverTaskAccess struct {
	selection nativeDriverTaskProfile
	worker    *NativeSwarmAccess
	bridge    *NativeLifecycleAccess
	runner    *NativeVerificationProfile
	reviewer  *NativeReviewProfile
}

// NativeControllerDriver is an explicitly selected, in-process composition.
// All credentials and profile snapshots remain private to the application.
type NativeControllerDriver struct {
	profilePath       string
	profileDigest     [32]byte
	profile           nativeControllerDriverProfile
	controller        *NativeControllerAccess
	tasks             map[string]nativeDriverTaskAccess
	backend           executor.ContextBackend
	coordinator       executor.ContextCoordinator
	decisions         NativeDriverReviewDecisionSource
	workspaceRoot     string
	authorityPath     string
	pinnedFiles       map[string][32]byte
	pinnedLimits      map[string]int64
	pinnedDirectories map[string]string
}

func (NativeControllerDriver) String() string   { return "NativeControllerDriver" }
func (NativeControllerDriver) GoString() string { return "NativeControllerDriver" }

func OpenNativeControllerDriver(profilePath string, backend executor.ContextBackend, decisions NativeDriverReviewDecisionSource) (*NativeControllerDriver, error) {
	if contextBackendMissing(backend) {
		backend = nil
	}
	if reviewDecisionSourceMissing(decisions) {
		decisions = nil
	}
	if !filepath.IsAbs(profilePath) {
		return nil, sessions.ErrDenied
	}
	raw, err := nativeconfig.ReadPrivate(profilePath, 128*1024)
	if err != nil {
		return nil, sessions.ErrDenied
	}
	profile, err := decodeNativeControllerDriverProfile(raw)
	if err != nil || validateNativeControllerDriverProfile(profile) != nil {
		return nil, sessions.ErrDenied
	}
	controller, err := OpenNativeController(profile.ControllerConnection, profile.CapabilityFile)
	if err != nil {
		return nil, sessions.ErrDenied
	}
	if _, _, err = controller.swarm.authenticate(); err != nil {
		return nil, sessions.ErrDenied
	}
	controllerConnection := controller.swarm.connection
	if controllerConnection.WorkspaceID != profile.WorkspaceID || controllerConnection.InstallationID != profile.InstallationID {
		return nil, sessions.ErrDenied
	}
	workspace, err := nativeconfig.ResolveWorkspace(controllerConnection.Workspace, profile.WorkspaceID)
	if err != nil {
		return nil, sessions.ErrDenied
	}
	authorityPath, err := canonicalNativeAuthorityPath(controllerConnection.Authority)
	if err != nil || authorityPath == "" {
		return nil, sessions.ErrDenied
	}
	driverSelection := &NativeControllerDriver{profilePath: profilePath, profileDigest: sha256.Sum256(raw), profile: profile, controller: controller, tasks: map[string]nativeDriverTaskAccess{}, backend: backend, decisions: decisions, workspaceRoot: workspace.Root, authorityPath: authorityPath, pinnedFiles: map[string][32]byte{}, pinnedLimits: map[string]int64{}, pinnedDirectories: map[string]string{}}
	if err = driverSelection.pinDirectory(workspace.Root); err != nil {
		return nil, sessions.ErrDenied
	}
	if err = driverSelection.pinPrivate(profilePath, 128*1024); err != nil {
		return nil, sessions.ErrDenied
	}
	if err = driverSelection.pinPrivate(profile.ControllerConnection, 16*1024); err != nil {
		return nil, sessions.ErrDenied
	}
	if err = driverSelection.pinPrivate(profile.CapabilityFile, 16*1024); err != nil {
		return nil, sessions.ErrDenied
	}
	if err = driverSelection.pinPrivate(controllerConnection.CredentialFile, 4096); err != nil {
		return nil, sessions.ErrDenied
	}
	for _, selected := range profile.Tasks {
		worker, openErr := selectNativeDriverWorker(selected.WorkerConnection, profile.CapabilityFile)
		if openErr != nil {
			return nil, sessions.ErrDenied
		}
		bridge, openErr := selectNativeDriverBridge(selected.BridgeConnection)
		if openErr != nil {
			return nil, sessions.ErrDenied
		}
		runner, openErr := selectNativeDriverRunner(selected.WorkerConnection, selected.RunnerProfile)
		if openErr != nil {
			return nil, sessions.ErrDenied
		}
		reviewer, openErr := selectNativeDriverReviewer(selected.ReviewerConnection, selected.ReviewerProfile)
		if openErr != nil {
			return nil, sessions.ErrDenied
		}
		if openErr = driverSelection.validateTaskActors(selected, worker, bridge, runner, reviewer); openErr != nil {
			return nil, sessions.ErrDenied
		}
		for _, directory := range []string{selected.DispatchTemplate.Checkout, selected.DispatchTemplate.Repository, runner.profile.Repository, reviewer.profile.Repository} {
			if openErr = driverSelection.pinDirectory(directory); openErr != nil {
				return nil, sessions.ErrDenied
			}
		}
		driverSelection.tasks[selected.TaskID] = nativeDriverTaskAccess{selection: selected, worker: worker, bridge: bridge, runner: runner, reviewer: reviewer}
		for _, path := range []string{selected.WorkerConnection, selected.BridgeConnection, selected.RunnerProfile, selected.ReviewerConnection, selected.ReviewerProfile} {
			if openErr = driverSelection.pinPrivate(path, 128*1024); openErr != nil {
				return nil, sessions.ErrDenied
			}
		}
		if openErr = driverSelection.pinPrivate(reviewer.profile.PolicyFile, 64*1024); openErr != nil {
			return nil, sessions.ErrDenied
		}
		for _, credential := range []string{worker.connection.CredentialFile, bridge.connection.CredentialFile, runner.connection.CredentialFile, runner.profile.CredentialFile, reviewer.connection.CredentialFile} {
			if openErr = driverSelection.pinPrivate(credential, 4096); openErr != nil {
				return nil, sessions.ErrDenied
			}
		}
		for _, authority := range []string{worker.connection.Authority, bridge.connection.Authority, runner.connection.Authority, runner.profile.Authority, reviewer.connection.Authority, reviewer.profile.Authority} {
			physical, pathErr := canonicalNativeAuthorityPath(authority)
			if pathErr != nil || physical != authorityPath {
				return nil, sessions.ErrDenied
			}
		}
		for _, connection := range []nativeconfig.Connection{worker.connection, bridge.connection, runner.connection, reviewer.connection} {
			if connection.WorkspaceID != profile.WorkspaceID || connection.InstallationID != profile.InstallationID || connection.Workspace != controllerConnection.Workspace {
				return nil, sessions.ErrDenied
			}
		}
	}
	if err = driverSelection.reauthenticatePinned(); err != nil {
		return nil, sessions.ErrDenied
	}
	a, token, err := controller.swarm.authenticate()
	if err != nil {
		return nil, sessions.ErrDenied
	}
	driverSelection.coordinator = executor.NewContextCoordinator(backend, a, token, controller.swarm.connection.SessionID)
	return driverSelection, nil
}

func reviewDecisionSourceMissing(source NativeDriverReviewDecisionSource) bool {
	if source == nil {
		return true
	}
	v := reflect.ValueOf(source)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

func decodeNativeControllerDriverProfile(raw []byte) (nativeControllerDriverProfile, error) {
	var p nativeControllerDriverProfile
	if nativeconfig.Decode(raw, &p) != nil {
		return nativeControllerDriverProfile{}, sessions.ErrDenied
	}
	var top map[string]json.RawMessage
	if json.Unmarshal(raw, &top) != nil {
		return nativeControllerDriverProfile{}, sessions.ErrDenied
	}
	var limits map[string]json.RawMessage
	if json.Unmarshal(top["limits"], &limits) != nil || len(limits) != 7 {
		return nativeControllerDriverProfile{}, sessions.ErrDenied
	}
	for _, key := range []string{"max_transitions", "max_effects", "max_work", "max_no_progress", "max_elapsed", "operation_timeout", "max_sequence"} {
		value, ok := limits[key]
		if !ok || len(value) == 0 || string(value) == "null" {
			return nativeControllerDriverProfile{}, sessions.ErrDenied
		}
	}
	return p, nil
}

func validateNativeControllerDriverProfile(p nativeControllerDriverProfile) error {
	if p.SchemaVersion != 1 || !VerificationID(p.WorkspaceID) || !VerificationID(p.InstallationID) || !VerificationID(p.ProjectID) || !VerificationID(p.InvocationID) || !VerificationID(p.CheckpointID) || !filepath.IsAbs(p.ControllerConnection) || !filepath.IsAbs(p.CapabilityFile) || len(p.Tasks) == 0 || len(p.Tasks) > 256 || !validNativeDriverLimits(p.Limits) {
		return sessions.ErrDenied
	}
	seen := map[string]bool{}
	for _, task := range p.Tasks {
		if !VerificationID(task.TaskID) || seen[task.TaskID] || !filepath.IsAbs(task.WorkerConnection) || !filepath.IsAbs(task.BridgeConnection) || !filepath.IsAbs(task.RunnerProfile) || !filepath.IsAbs(task.ReviewerConnection) || !filepath.IsAbs(task.ReviewerProfile) || task.WorkerEnrollment.Binding.Workspace != p.WorkspaceID || task.WorkerEnrollment.Binding.Project != p.ProjectID || task.WorkerEnrollment.Binding.Task != task.TaskID || !VerificationID(task.WorkerEnrollment.Binding.Attempt) || task.WorkerEnrollment.AttemptRequest.TaskID != task.TaskID || task.WorkerEnrollment.AttemptRequest.RequestID == "" || task.DispatchTemplate.Version != executor.ContractVersion || task.DispatchTemplate.Binding.Workspace != p.WorkspaceID || task.DispatchTemplate.Binding.Task != task.TaskID || task.DispatchTemplate.Binding.Project != p.ProjectID || task.DispatchTemplate.Prompt == "" || task.DispatchTemplate.Repository == "" || !filepath.IsAbs(task.DispatchTemplate.Checkout) || task.DispatchTemplate.PromptSpecDigest != executor.DispatchSpecDigest(task.DispatchTemplate) {
			return sessions.ErrDenied
		}
		seen[task.TaskID] = true
	}
	return nil
}

func validNativeDriverLimits(limits nativeDriverProfileLimits) bool {
	return limits.MaxTransitions > 0 && limits.MaxTransitions <= 256 &&
		limits.MaxEffects > 0 && limits.MaxEffects <= 256 &&
		limits.MaxWork > 0 && limits.MaxWork <= 4096 &&
		limits.MaxNoProgress > 0 && limits.MaxNoProgress <= 100 &&
		limits.MaxElapsed > 0 && time.Duration(limits.MaxElapsed) <= 15*time.Minute &&
		limits.OperationTimeout > 0 && time.Duration(limits.OperationTimeout) <= 30*time.Second &&
		limits.MaxSequence > 0
}

func (p *NativeControllerDriver) validateTaskActors(selected nativeDriverTaskProfile, worker *NativeSwarmAccess, bridge *NativeLifecycleAccess, runner *NativeVerificationProfile, reviewer *NativeReviewProfile) error {
	if selected.WorkerEnrollment.Binding.Workspace != p.profile.WorkspaceID || selected.WorkerEnrollment.Binding.Project != p.profile.ProjectID || selected.WorkerEnrollment.Binding.Session != worker.connection.SessionID || selected.WorkerEnrollment.Binding.Task != selected.TaskID || selected.WorkerEnrollment.Binding.CredentialID != selected.WorkerEnrollment.Profile.ID || selected.WorkerEnrollment.Profile.WorkerSession != worker.connection.SessionID || selected.WorkerEnrollment.AttemptRequest.Checkout != selected.DispatchTemplate.Checkout || selected.WorkerEnrollment.Profile != selected.DispatchTemplate.Profile || !sameNativeBindingExceptAttempt(selected.WorkerEnrollment.Binding, selected.DispatchTemplate.Binding) || selected.DispatchTemplate.Binding.Session != worker.connection.SessionID || selected.DispatchTemplate.Binding.Project != p.profile.ProjectID || selected.DispatchTemplate.Binding.Task != selected.TaskID || selected.DispatchTemplate.Repository != runner.profile.Repository || selected.DispatchTemplate.Checkout == "" || selected.DispatchTemplate.Prompt == "" {
		return sessions.ErrDenied
	}
	if bridge.connection.WorkspaceID != p.profile.WorkspaceID || runner.connection.WorkspaceID != p.profile.WorkspaceID || reviewer.connection.WorkspaceID != p.profile.WorkspaceID || runner.profile.WorkspaceID != p.profile.WorkspaceID || runner.profile.InstallationID != p.profile.InstallationID || runner.profile.TaskID != selected.TaskID || !filepath.IsAbs(runner.profile.Repository) || !filepath.IsAbs(runner.profile.CredentialFile) || reviewer.profile.WorkspaceID != p.profile.WorkspaceID || reviewer.profile.InstallationID != p.profile.InstallationID || runner.profile.TaskID != selected.TaskID || reviewer.profile.TaskID != selected.TaskID || reviewer.profile.ProjectID != p.profile.ProjectID || reviewer.profile.Repository != selected.DispatchTemplate.Repository || reviewer.profile.WorkerSessionID != worker.connection.SessionID || reviewer.profile.ReviewerSessionID == worker.connection.SessionID || !filepath.IsAbs(reviewer.profile.Repository) || !filepath.IsAbs(reviewer.profile.CredentialFile) || !filepath.IsAbs(reviewer.profile.PolicyFile) {
		return sessions.ErrDenied
	}
	if worker.connection.SessionID == p.controller.swarm.connection.SessionID || bridge.connection.SessionID == worker.connection.SessionID || bridge.connection.SessionID == p.controller.swarm.connection.SessionID || runner.profile.RunnerSessionID == worker.connection.SessionID || runner.profile.RunnerSessionID == p.controller.swarm.connection.SessionID || runner.profile.RunnerSessionID == bridge.connection.SessionID || reviewer.profile.ReviewerSessionID == runner.profile.RunnerSessionID || reviewer.profile.ReviewerSessionID == bridge.connection.SessionID || reviewer.profile.ReviewerSessionID == p.controller.swarm.connection.SessionID {
		return sessions.ErrDenied
	}
	return nil
}

func sameNativeBindingExceptAttempt(a, b executor.Binding) bool {
	return a.Workspace == b.Workspace && a.Project == b.Project && a.Task == b.Task && a.Host == b.Host && a.Session == b.Session && a.Instance == b.Instance && a.CredentialID == b.CredentialID && a.ExpiresAt.Equal(b.ExpiresAt) && a.Revoked == b.Revoked && (b.Attempt == "" || a.Attempt == b.Attempt)
}

func canonicalNativeAuthorityPath(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", sessions.ErrDenied
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", sessions.ErrDenied
	}
	return filepath.EvalSymlinks(path)
}

func (p *NativeControllerDriver) pinPrivate(path string, limit int64) error {
	if p == nil || path == "" || !filepath.IsAbs(path) {
		return sessions.ErrDenied
	}
	raw, err := nativeconfig.ReadPrivate(path, limit)
	if err != nil {
		return sessions.ErrDenied
	}
	digest := sha256.Sum256(raw)
	if pinned, exists := p.pinnedFiles[path]; exists {
		if pinned != digest {
			return sessions.ErrDenied
		}
		if currentLimit := p.pinnedLimits[path]; currentLimit < limit {
			return nil
		}
	}
	p.pinnedFiles[path] = digest
	if currentLimit, exists := p.pinnedLimits[path]; !exists || limit < currentLimit {
		p.pinnedLimits[path] = limit
	}
	return nil
}

func (p *NativeControllerDriver) pinDirectory(path string) error {
	if p == nil || !filepath.IsAbs(path) {
		return sessions.ErrDenied
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return sessions.ErrDenied
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return sessions.ErrDenied
	}
	if pinned, exists := p.pinnedDirectories[path]; exists && pinned != resolved {
		return sessions.ErrDenied
	}
	p.pinnedDirectories[path] = resolved
	return nil
}

func (p *NativeControllerDriver) verifyPinnedFiles() error {
	if p == nil || p.profilePath == "" {
		return sessions.ErrDenied
	}
	raw, err := nativeconfig.ReadPrivate(p.profilePath, 128*1024)
	if err != nil || sha256.Sum256(raw) != p.profileDigest {
		return sessions.ErrDenied
	}
	for path, digest := range p.pinnedFiles {
		current, readErr := nativeconfig.ReadPrivate(path, p.pinnedLimits[path])
		if readErr != nil || sha256.Sum256(current) != digest {
			return sessions.ErrDenied
		}
	}
	for path, pinned := range p.pinnedDirectories {
		current, resolveErr := filepath.EvalSymlinks(path)
		if resolveErr != nil || current != pinned {
			return sessions.ErrDenied
		}
	}
	if err = p.verifySelectedRoots(); err != nil {
		return sessions.ErrDenied
	}
	return nil
}

func (p *NativeControllerDriver) verifySelectedRoots() error {
	if p != nil && p.authorityPath == "" {
		return nil
	}
	if p == nil || p.controller == nil || p.controller.swarm == nil {
		return sessions.ErrDenied
	}
	authorities := []string{p.controller.swarm.connection.Authority}
	connections := []nativeconfig.Connection{p.controller.swarm.connection}
	for _, task := range p.tasks {
		authorities = append(authorities, task.worker.connection.Authority, task.bridge.connection.Authority, task.runner.connection.Authority, task.runner.profile.Authority, task.reviewer.connection.Authority, task.reviewer.profile.Authority)
		connections = append(connections, task.worker.connection, task.bridge.connection, task.runner.connection, task.reviewer.connection)
		for _, workspacePath := range []string{task.runner.profile.Workspace, task.reviewer.profile.Workspace} {
			resolved, resolveErr := filepath.EvalSymlinks(workspacePath)
			if resolveErr != nil || resolved != p.workspaceRoot {
				return sessions.ErrDenied
			}
		}
	}
	for _, authority := range authorities {
		resolved, err := canonicalNativeAuthorityPath(authority)
		if err != nil || resolved != p.authorityPath {
			return sessions.ErrDenied
		}
	}
	for _, connection := range connections {
		workspace, err := nativeconfig.ResolveWorkspace(connection.Workspace, p.profile.WorkspaceID)
		if err != nil || workspace.Root != p.workspaceRoot || connection.WorkspaceID != p.profile.WorkspaceID || connection.InstallationID != p.profile.InstallationID {
			return sessions.ErrDenied
		}
	}
	return nil
}

func (p *NativeControllerDriver) reauthenticatePinned() error {
	if err := p.verifyPinnedFiles(); err != nil {
		return err
	}
	controllerAuthority, _, err := p.controller.swarm.authenticate()
	if err != nil || controllerAuthority == nil {
		return sessions.ErrDenied
	}
	return nil
}

func (p *NativeControllerDriver) freshTask(taskID string) (nativeDriverTaskAccess, error) {
	if err := p.reauthenticatePinned(); err != nil {
		return nativeDriverTaskAccess{}, err
	}
	task, ok := p.tasks[taskID]
	if !ok {
		return nativeDriverTaskAccess{}, sessions.ErrDenied
	}
	return task, nil
}

func selectNativeDriverWorker(connectionPath, capabilityPath string) (*NativeSwarmAccess, error) {
	connection, err := readNativeDriverConnection(connectionPath)
	if err != nil || !filepath.IsAbs(capabilityPath) {
		return nil, sessions.ErrDenied
	}
	return &NativeSwarmAccess{connectionPath: connectionPath, capabilityPath: capabilityPath, connection: connection}, nil
}

func selectNativeDriverBridge(connectionPath string) (*NativeLifecycleAccess, error) {
	connection, err := readNativeDriverConnection(connectionPath)
	if err != nil {
		return nil, err
	}
	return &NativeLifecycleAccess{path: connectionPath, connection: connection}, nil
}

func selectNativeDriverRunner(connectionPath, runnerPath string) (*NativeVerificationProfile, error) {
	connection, err := readNativeDriverConnection(connectionPath)
	if err != nil {
		return nil, err
	}
	raw, err := nativeconfig.ReadPrivate(runnerPath, 65536)
	if err != nil {
		return nil, sessions.ErrDenied
	}
	var profile NativeRunnerProfile
	if nativeconfig.Decode(raw, &profile) != nil {
		return nil, sessions.ErrDenied
	}
	return &NativeVerificationProfile{connectionPath: connectionPath, runnerPath: runnerPath, connection: connection, profile: profile}, nil
}

func selectNativeDriverReviewer(connectionPath, profilePath string) (*NativeReviewProfile, error) {
	connection, err := readNativeDriverConnection(connectionPath)
	if err != nil {
		return nil, err
	}
	raw, err := nativeconfig.ReadPrivate(profilePath, 65536)
	if err != nil {
		return nil, sessions.ErrDenied
	}
	var profile NativeReviewerProfile
	if nativeconfig.Decode(raw, &profile) != nil {
		return nil, sessions.ErrDenied
	}
	return &NativeReviewProfile{connectionPath: connectionPath, profilePath: profilePath, connection: connection, profile: profile}, nil
}

func readNativeDriverConnection(path string) (nativeconfig.Connection, error) {
	raw, err := nativeconfig.ReadPrivate(path, 16384)
	if err != nil {
		return nativeconfig.Connection{}, sessions.ErrDenied
	}
	var connection nativeconfig.Connection
	if nativeconfig.Decode(raw, &connection) != nil || (connection.SchemaVersion != 1 && connection.SchemaVersion != 2) || connection.WorkspaceID == "" || connection.InstallationID == "" || connection.SessionID == "" || !filepath.IsAbs(connection.Workspace) || !filepath.IsAbs(connection.Authority) || !filepath.IsAbs(connection.CredentialFile) {
		return nativeconfig.Connection{}, sessions.ErrDenied
	}
	return connection, nil
}
