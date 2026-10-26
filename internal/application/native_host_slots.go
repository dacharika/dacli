package application

import (
	"path/filepath"

	"github.com/mlnomadpy/dacli/internal/governance/nativeconfig"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

// NativeHostSlotsAccess is a private authenticated controller reader for
// schema-22 slot diagnostics. It grants no reservation or host-action permit.
type NativeHostSlotsAccess struct {
	path          string
	connection    nativeconfig.Connection
	workspaceRoot string
}

func OpenNativeHostSlots(path string, w *workspace.Workspace) (*NativeHostSlotsAccess, error) {
	raw, err := nativeconfig.ReadPrivate(path, 16384)
	if err != nil {
		return nil, sessions.ErrDenied
	}
	var c nativeconfig.Connection
	if nativeconfig.Decode(raw, &c) != nil || (c.SchemaVersion != 1 && c.SchemaVersion != 2) || w == nil || c.SessionID == "" || c.InstallationID == "" || !filepath.IsAbs(c.Authority) || !filepath.IsAbs(c.CredentialFile) {
		return nil, sessions.ErrDenied
	}
	resolved, err := nativeconfig.ResolveWorkspace(c.Workspace, c.WorkspaceID)
	if err != nil || resolved.ID != w.ID || !sameWorkspaceRoot(resolved.Root, w.Root) {
		return nil, sessions.ErrDenied
	}
	p := &NativeHostSlotsAccess{path: path, connection: c, workspaceRoot: resolved.Root}
	if _, _, err := p.authenticate(); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *NativeHostSlotsAccess) authenticate() (*sessions.Authority, string, error) {
	if p == nil {
		return nil, "", sessions.ErrDenied
	}
	raw, err := nativeconfig.ReadPrivate(p.path, 16384)
	if err != nil {
		return nil, "", sessions.ErrDenied
	}
	var c nativeconfig.Connection
	if nativeconfig.Decode(raw, &c) != nil || c != p.connection {
		return nil, "", sessions.ErrDenied
	}
	w, err := nativeconfig.ResolveWorkspace(c.Workspace, c.WorkspaceID)
	if err != nil || w.Root != p.workspaceRoot {
		return nil, "", sessions.ErrDenied
	}
	a, err := sessions.OpenHostSlots(c.Authority, sessions.Binding{Workspace: w.ID, Installation: c.InstallationID})
	if err != nil {
		return nil, "", err
	}
	token, err := nativeconfig.ReadCredential(c.CredentialFile)
	if err != nil {
		return nil, "", sessions.ErrDenied
	}
	if _, err = a.Authenticate(token, c.SessionID); err != nil {
		return nil, "", err
	}
	return a, token, nil
}

func (p *NativeHostSlotsAccess) Read(project string) (sessions.HostSlotAdmissionView, error) {
	a, token, err := p.authenticate()
	if err != nil {
		return sessions.HostSlotAdmissionView{}, err
	}
	return a.ReadHostSlotAdmission(token, p.connection.SessionID, project)
}
