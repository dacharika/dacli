package application

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/mlnomadpy/dacli/internal/governance/checkout"
	"github.com/mlnomadpy/dacli/internal/governance/nativeconfig"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
)

type ConfiguredCheck struct {
	ID          string   `json:"id"`
	Command     string   `json:"command,omitempty"`
	Argv        []string `json:"argv,omitempty"`
	Cwd         string   `json:"cwd,omitempty"`
	Environment string   `json:"environment,omitempty"`
	Deadline    string   `json:"deadline"`
	GrantID     string   `json:"grant_id,omitempty"`
}
type NativeRunnerProfile struct {
	SchemaVersion   int               `json:"schema_version"`
	Workspace       string            `json:"workspace"`
	WorkspaceID     string            `json:"workspace_id"`
	Authority       string            `json:"authority"`
	InstallationID  string            `json:"installation_id"`
	TaskID          string            `json:"task_id"`
	Repository      string            `json:"repository"`
	RunnerSessionID string            `json:"runner_session_id"`
	CredentialFile  string            `json:"credential_file"`
	Checks          []ConfiguredCheck `json:"checks"`
	RoutingPolicy   []byte            `json:"routing_policy,omitempty"`
	BaseCommit      string            `json:"base_commit,omitempty"`
}

func VerificationID(s string) bool {
	return s != "" && len(s) <= 256 && strings.TrimSpace(s) == s && !strings.ContainsAny(s, "/\\\x00\r\n")
}
func verificationPrivateJSON(path string, value any) error {
	b, e := nativeconfig.ReadPrivate(path, 65536)
	if e != nil || nativeconfig.Decode(b, value) != nil {
		return sessions.ErrDenied
	}
	return nil
}
func NativeCheckDescriptors(checks []ConfiguredCheck, grants bool) ([]NativeCheck, error) {
	return NativeProfileCheckDescriptors(NativeRunnerProfile{SchemaVersion: 1, Checks: checks}, grants)
}
func NativeProfileCheckDescriptors(profile NativeRunnerProfile, grants bool) ([]NativeCheck, error) {
	checks := profile.Checks
	routing, e := nativeVerificationRoutingProfile(profile)
	if e != nil {
		return nil, e
	}
	if len(checks) == 0 || len(checks) > 32 {
		return nil, sessions.ErrDenied
	}
	seen := map[string]bool{}
	result := make([]NativeCheck, 0, len(checks))
	for _, c := range checks {
		deadline, e := time.ParseDuration(c.Deadline)
		if e != nil || deadline <= 0 || deadline > 10*time.Minute || !VerificationID(c.ID) || seen[c.ID] || (grants && !VerificationID(c.GrantID)) {
			return nil, sessions.ErrDenied
		}
		seen[c.ID] = true
		descriptor := sessions.CheckDescriptor{ID: c.ID, Command: c.Command, Deadline: deadline}
		if profile.SchemaVersion == 2 {
			descriptor.Version = 2
			descriptor.Argv = append([]string(nil), c.Argv...)
			descriptor.Cwd = c.Cwd
			descriptor.Environment = c.Environment
			descriptor.RoutingDigest = routing.digest
			descriptor.RoutingBytesDigest = routing.bytesDigest
			descriptor.BaseCommit = profile.BaseCommit
			if !routing.matches(descriptor) {
				return nil, sessions.ErrDenied
			}
		} else if len(c.Argv) != 0 || c.Cwd != "" || c.Environment != "" {
			return nil, sessions.ErrDenied
		}
		if !sessions.ValidCheckDescriptor(descriptor) {
			return nil, sessions.ErrDenied
		}
		result = append(result, NativeCheck{Descriptor: descriptor, GrantID: c.GrantID})
	}
	return result, nil
}

// NativeVerificationProfile pins the owner's startup selection. Every operation
// rereads private files and authenticates current authority before returning it.
type NativeVerificationProfile struct {
	connectionPath, runnerPath string
	connection                 nativeconfig.Connection
	profile                    NativeRunnerProfile
}

func (NativeVerificationProfile) String() string   { return "NativeVerificationProfile" }
func (NativeVerificationProfile) GoString() string { return "NativeVerificationProfile" }

type NativeVerificationAccess struct {
	Runner      *NativeVerificationRunner
	Worker      sessions.Session
	workerToken string
}

func (NativeVerificationAccess) String() string   { return "NativeVerificationAccess" }
func (NativeVerificationAccess) GoString() string { return "NativeVerificationAccess" }
func OpenNativeVerificationProfile(connectionPath, runnerPath string) (*NativeVerificationProfile, error) {
	var c nativeconfig.Connection
	var p NativeRunnerProfile
	if verificationPrivateJSON(connectionPath, &c) != nil || verificationPrivateJSON(runnerPath, &p) != nil {
		return nil, sessions.ErrDenied
	}
	selected := &NativeVerificationProfile{connectionPath, runnerPath, c, p}
	if _, e := selected.Authenticate(); e != nil {
		return nil, e
	}
	return selected, nil
}
func (selected *NativeVerificationProfile) Authenticate() (*NativeVerificationAccess, error) {
	if selected == nil {
		return nil, sessions.ErrDenied
	}
	var c nativeconfig.Connection
	var p NativeRunnerProfile
	if verificationPrivateJSON(selected.connectionPath, &c) != nil || verificationPrivateJSON(selected.runnerPath, &p) != nil || c != selected.connection || !reflect.DeepEqual(p, selected.profile) {
		return nil, sessions.ErrDenied
	}
	if c.SchemaVersion != 1 || (p.SchemaVersion != 1 && p.SchemaVersion != 2) || c.WorkspaceID == "" || c.InstallationID == "" || !VerificationID(c.SessionID) || c.WorkspaceID != p.WorkspaceID || c.InstallationID != p.InstallationID || !VerificationID(p.RunnerSessionID) || !VerificationID(p.TaskID) || !filepath.IsAbs(p.Repository) {
		return nil, sessions.ErrDenied
	}
	// Compare physical authority paths as well as installation/workspace binding;
	// a profile from another local authority cannot be substituted by its IDs.
	for _, path := range []string{c.Authority, p.Authority} {
		info, statErr := os.Lstat(path)
		if statErr != nil || !info.Mode().IsRegular() {
			return nil, sessions.ErrDenied
		}
	}
	authority, e := filepath.EvalSymlinks(c.Authority)
	runnerAuthority, re := filepath.EvalSymlinks(p.Authority)
	if e != nil || re != nil || !filepath.IsAbs(authority) || !filepath.IsAbs(c.Authority) || !filepath.IsAbs(p.Authority) || authority != runnerAuthority {
		return nil, sessions.ErrDenied
	}
	w, e := nativeconfig.ResolveWorkspace(c.Workspace, c.WorkspaceID)
	if e != nil {
		return nil, sessions.ErrDenied
	}
	runnerWorkspace, e := nativeconfig.ResolveWorkspace(p.Workspace, p.WorkspaceID)
	if e != nil || runnerWorkspace.Root != w.Root {
		return nil, sessions.ErrDenied
	}
	trusted, e := NativeProfileCheckDescriptors(p, true)
	if e != nil {
		return nil, e
	}
	workerToken, e := nativeconfig.ReadCredential(c.CredentialFile)
	if e != nil {
		return nil, sessions.ErrDenied
	}
	admission, e := NewLeaseAdmission(w)
	if e != nil {
		return nil, sessions.ErrDenied
	}
	a, e := sessions.Open(authority, sessions.Binding{Workspace: w.ID, Installation: c.InstallationID}, admission)
	if e != nil {
		return nil, sessions.ErrDenied
	}
	worker, e := a.Authenticate(workerToken, c.SessionID)
	if e != nil {
		return nil, sessions.ErrDenied
	}
	runnerToken, e := nativeconfig.ReadCredential(p.CredentialFile)
	if e != nil {
		return nil, sessions.ErrDenied
	}
	runnerSession, e := a.Authenticate(runnerToken, p.RunnerSessionID)
	if e != nil || runnerSession.Parent != "" || len(runnerSession.Scope.Actions) != 1 || runnerSession.Scope.Actions[0] != "verification.run" || len(runnerSession.Scope.Resources) != 1 || runnerSession.Scope.Resources[0] != "task:"+p.TaskID {
		return nil, sessions.ErrDenied
	}
	if _, e = a.Authorize(workerToken, worker.ID, w.ID, "verification.request", "task:"+p.TaskID); e != nil {
		return nil, sessions.ErrDenied
	}
	repository, e := checkout.ObserveRepositoryIdentity(w.Root)
	if e != nil || repository.Repository != p.Repository {
		return nil, sessions.ErrDenied
	}
	routing, e := nativeVerificationRoutingProfile(p)
	if e != nil {
		return nil, e
	}
	runner, e := newNativeVerificationRunner(a, runnerToken, p.RunnerSessionID, trusted, routing)
	if e != nil {
		return nil, sessions.ErrDenied
	}

	runner.repository = p.Repository
	return &NativeVerificationAccess{runner, worker, workerToken}, nil
}
func ValidNativeVerificationRequest(r NativeVerificationRequest) bool {
	return VerificationID(r.OperationKey) && VerificationID(r.AttemptID) && VerificationID(r.LeaseID) && VerificationID(r.RevisionID) && VerificationID(r.CheckID) && r.ExpectedFence != 0
}
