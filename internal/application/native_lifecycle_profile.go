package application

import (
	"github.com/mlnomadpy/dacli/internal/governance/nativeconfig"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"path/filepath"
)

// NativeLifecycleAccess reloads one independently enrolled private connection.
// Host reports do not depend on dispatch capability or cancelled worker authority.
type NativeLifecycleAccess struct {
	path       string
	connection nativeconfig.Connection
}

func OpenNativeLifecycle(path string) (*NativeLifecycleAccess, error) {
	data, e := nativeconfig.ReadPrivate(path, 16384)
	if e != nil {
		return nil, sessions.ErrDenied
	}
	var c nativeconfig.Connection
	if nativeconfig.Decode(data, &c) != nil || (c.SchemaVersion != 1 && c.SchemaVersion != 2) || c.SessionID == "" || c.WorkspaceID == "" || c.InstallationID == "" || !filepath.IsAbs(c.Authority) || !filepath.IsAbs(c.CredentialFile) {
		return nil, sessions.ErrDenied
	}
	p := &NativeLifecycleAccess{path, c}
	if _, _, e = p.authenticate(); e != nil {
		return nil, e
	}
	return p, nil
}
func (p *NativeLifecycleAccess) authenticate() (*sessions.Authority, string, error) {
	data, e := nativeconfig.ReadPrivate(p.path, 16384)
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
	a, e := sessions.Open(c.Authority, sessions.Binding{Workspace: w.ID, Installation: c.InstallationID})
	if e != nil {
		return nil, "", e
	}
	t, e := nativeconfig.ReadCredential(c.CredentialFile)
	if e != nil {
		return nil, "", sessions.ErrDenied
	}
	if _, e = a.Authenticate(t, c.SessionID); e != nil {
		return nil, "", e
	}
	return a, t, nil
}
func (p *NativeLifecycleAccess) Observe(r sessions.ObserveLifecycleRequest) (sessions.LifecycleObservation, error) {
	a, t, e := p.authenticate()
	if e != nil {
		return sessions.LifecycleObservation{}, e
	}
	return a.ObserveLifecycle(t, p.connection.SessionID, r)
}
func (p *NativeLifecycleAccess) Cancel(r sessions.CancelLifecycleRequest) (sessions.LifecycleCancellationResult, error) {
	a, t, e := p.authenticate()
	if e != nil {
		return sessions.LifecycleCancellationResult{}, e
	}
	return a.CancelLifecycle(t, p.connection.SessionID, r)
}
func (p *NativeLifecycleAccess) Acknowledge(r sessions.AcknowledgeCancellationRequest) (sessions.LifecycleCancellation, error) {
	a, t, e := p.authenticate()
	if e != nil {
		return sessions.LifecycleCancellation{}, e
	}
	return a.AcknowledgeCancellation(t, p.connection.SessionID, r)
}
func (p *NativeLifecycleAccess) Status(id string) (sessions.LifecycleStatus, error) {
	a, t, e := p.authenticate()
	if e != nil {
		return sessions.LifecycleStatus{}, e
	}
	return a.LifecycleStatus(t, p.connection.SessionID, id)
}
