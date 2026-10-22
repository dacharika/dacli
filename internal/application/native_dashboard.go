package application

import (
	"github.com/mlnomadpy/dacli/internal/governance/nativeconfig"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/workspace"
	"path/filepath"
)

// NativeDashboard holds an explicit reader connection, not owner authority.
// Reads revalidate the immutable profile and authenticate in the bounded snapshot.
type NativeDashboard struct {
	path       string
	connection nativeconfig.Connection
	workspace  *workspace.Workspace
}

func OpenNativeDashboard(path string, w *workspace.Workspace) (*NativeDashboard, error) {
	raw, e := nativeconfig.ReadPrivate(path, 16384)
	if e != nil {
		return nil, nativeconfig.ErrDenied
	}
	var c nativeconfig.Connection
	if nativeconfig.Decode(raw, &c) != nil || (c.SchemaVersion != 1 && c.SchemaVersion != 2) || w == nil || c.SessionID == "" || c.InstallationID == "" || !filepath.IsAbs(c.Authority) || !filepath.IsAbs(c.CredentialFile) {
		return nil, nativeconfig.ErrDenied
	}
	resolved, e := nativeconfig.ResolveWorkspace(c.Workspace, c.WorkspaceID)
	if e != nil || resolved.ID != w.ID || resolved.Root != w.Root {
		return nil, nativeconfig.ErrDenied
	}
	return &NativeDashboard{path, c, resolved}, nil
}
func (p *NativeDashboard) Read(project, task string) (sessions.DashboardSnapshot, error) {
	raw, e := nativeconfig.ReadPrivate(p.path, 16384)
	if e != nil {
		return sessions.DashboardSnapshot{}, nativeconfig.ErrDenied
	}
	var c nativeconfig.Connection
	if nativeconfig.Decode(raw, &c) != nil || c != p.connection {
		return sessions.DashboardSnapshot{}, nativeconfig.ErrDenied
	}
	w, e := nativeconfig.ResolveWorkspace(c.Workspace, c.WorkspaceID)
	if e != nil || w.Root != p.workspace.Root {
		return sessions.DashboardSnapshot{}, nativeconfig.ErrDenied
	}
	token, e := nativeconfig.ReadCredential(c.CredentialFile)
	if e != nil {
		return sessions.DashboardSnapshot{}, nativeconfig.ErrDenied
	}
	a, e := sessions.OpenDashboardAuthority(c.Authority, sessions.Binding{Workspace: c.WorkspaceID, Installation: c.InstallationID})
	if e != nil {
		return sessions.DashboardSnapshot{}, e
	}
	return a.ReadDashboard(token, c.SessionID, project, task)
}
