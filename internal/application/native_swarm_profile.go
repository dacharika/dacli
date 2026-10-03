package application

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"time"

	"github.com/mlnomadpy/dacli/internal/governance/nativeconfig"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
)

// ManualSwarmCapability is explicit owner attestation of a cooperative native
// handoff, not proof of an unattended host API or authenticated child identity.
// The bridge must still record intent/acknowledgement; no agents run here.
type ManualSwarmCapability struct {
	Codex *CodexDispatchCapability `json:"codex,omitempty"`

	SchemaVersion int       `json:"schema_version"`
	WorkspaceID   string    `json:"workspace_id"`
	ProjectID     string    `json:"project_id"`
	Harness       string    `json:"harness"`
	Source        string    `json:"source"`
	ObservedAt    time.Time `json:"observed_at"`
	ExpiresAt     time.Time `json:"expires_at"`
}
type manualSwarmCapabilityFile struct{ path string }

func (f manualSwarmCapabilityFile) AdmitSwarmCapability(w, p, h, d string) error {
	data, e := nativeconfig.ReadPrivate(f.path, 16384)
	if e != nil {
		return sessions.ErrDenied
	}
	var c ManualSwarmCapability
	if nativeconfig.Decode(data, &c) != nil {
		return sessions.ErrDenied
	}
	digest := sha256.Sum256(data)
	now := time.Now()
	if (c.SchemaVersion != 1 && c.SchemaVersion != 2) || c.Source != "owner-manual-native-handoff" || c.WorkspaceID != w || c.ProjectID != p || c.Harness != h || d != "sha256:"+hex.EncodeToString(digest[:]) || c.ObservedAt.After(now.Add(30*time.Second)) || !c.ExpiresAt.After(now) || !c.ExpiresAt.After(c.ObservedAt) || c.ExpiresAt.After(c.ObservedAt.Add(time.Hour)) || now.Sub(c.ObservedAt) > time.Hour {
		return sessions.ErrDenied
	}
	return nil
}

type NativeSwarmAccess struct {
	connectionPath, capabilityPath string
	connection                     nativeconfig.Connection
}

func OpenNativeSwarm(connectionPath, capabilityPath string) (*NativeSwarmAccess, error) {
	data, e := nativeconfig.ReadPrivate(connectionPath, 16384)
	if e != nil {
		return nil, sessions.ErrDenied
	}
	var c nativeconfig.Connection
	if nativeconfig.Decode(data, &c) != nil || (c.SchemaVersion != 1 && c.SchemaVersion != 2) || c.WorkspaceID == "" || c.SessionID == "" || c.InstallationID == "" || !filepath.IsAbs(c.Authority) || !filepath.IsAbs(c.CredentialFile) || !filepath.IsAbs(capabilityPath) {
		return nil, sessions.ErrDenied
	}
	access := &NativeSwarmAccess{connectionPath: connectionPath, capabilityPath: capabilityPath, connection: c}
	if _, _, e = access.authenticate(); e != nil {
		return nil, e
	}
	return access, nil
}
func (p *NativeSwarmAccess) authenticate() (*sessions.Authority, string, error) {
	data, e := nativeconfig.ReadPrivate(p.connectionPath, 16384)
	if e != nil {
		return nil, "", sessions.ErrDenied
	}
	var c nativeconfig.Connection
	if nativeconfig.Decode(data, &c) != nil || c != p.connection {
		return nil, "", sessions.ErrDenied
	}
	w, e := nativeconfig.ResolveWorkspace(c.Workspace, c.WorkspaceID)
	if e != nil {
		return nil, "", sessions.ErrDenied
	}
	admission, e := NewSwarmAdmission(w, manualSwarmCapabilityFile{path: p.capabilityPath})
	if e != nil {
		return nil, "", e
	}
	authority, e := sessions.OpenSwarm(c.Authority, sessions.Binding{Workspace: c.WorkspaceID, Installation: c.InstallationID}, admission)
	if e != nil {
		return nil, "", e
	}
	token, e := nativeconfig.ReadCredential(c.CredentialFile)
	if e != nil {
		return nil, "", sessions.ErrDenied
	}
	if _, e = authority.Authenticate(token, c.SessionID); e != nil {
		return nil, "", e
	}
	return authority, token, nil
}
func (p *NativeSwarmAccess) Plan(project string) (sessions.SwarmPlan, error) {
	a, t, e := p.authenticate()
	if e != nil {
		return sessions.SwarmPlan{}, e
	}
	return a.PlanSwarm(t, p.connection.SessionID, project)
}
func (p *NativeSwarmAccess) Reserve(req sessions.ReserveWaveRequest) (sessions.SwarmWave, error) {
	a, t, e := p.authenticate()
	if e != nil {
		return sessions.SwarmWave{}, e
	}
	return a.ReserveWave(t, p.connection.SessionID, req)
}
func (p *NativeSwarmAccess) Admit(req sessions.AdmitDispatchRequest) (sessions.DispatchReservation, error) {
	a, t, e := p.authenticate()
	if e != nil {
		return sessions.DispatchReservation{}, e
	}
	return a.AdmitDispatch(t, p.connection.SessionID, req)
}
func (p *NativeSwarmAccess) Handoff(req sessions.HandoffDispatchRequest) (sessions.Lease, error) {
	a, t, e := p.authenticate()
	if e != nil {
		return sessions.Lease{}, e
	}
	return a.HandoffDispatch(t, p.connection.SessionID, req)
}
