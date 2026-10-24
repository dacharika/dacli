package application

import (
	"github.com/mlnomadpy/dacli/internal/governance/nativeconfig"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
)

// NativePreflightOwner selects the same immutable private owner configuration
// as recovery. It installs pure canonical/profile observers, never worker tokens.
type NativePreflightOwner struct{ owner *NativeRecoveryAccess }

func OpenNativePreflightOwner(path string) (*NativePreflightOwner, error) {
	p, e := OpenNativeRecovery(path)
	if e != nil {
		return nil, e
	}
	return &NativePreflightOwner{owner: p}, nil
}
func (p *NativePreflightOwner) authenticate() (*sessions.Authority, string, error) {
	_, o, e := p.owner.authenticate()
	if e != nil {
		return nil, "", e
	}
	c := p.owner.config
	w, e := nativeconfig.ResolveWorkspace(c.Workspace, "")
	if e != nil {
		return nil, "", e
	}
	observer, e := NewLeaseAdmission(w)
	if e != nil {
		return nil, "", e
	}
	a, e := sessions.OpenCurrent(c.Authority, sessions.Binding{Workspace: w.ID, Installation: c.InstallationID}, observer)
	return a, o, e
}
func (p *NativePreflightOwner) Upgrade() error {
	a, o, e := p.authenticate()
	if e != nil {
		return e
	}
	return a.UpgradePreflight(o)
}
func (p *NativePreflightOwner) Configure(policy sessions.PreflightPolicy) error {
	a, o, e := p.authenticate()
	if e != nil {
		return e
	}
	return a.ConfigurePreflight(o, policy)
}
func (p *NativePreflightOwner) Override(req sessions.CapacityOverrideRequest) (sessions.CapacityOverride, error) {
	a, o, e := p.authenticate()
	if e != nil {
		return sessions.CapacityOverride{}, e
	}
	return a.OverrideCapacity(o, req)
}
func (p *NativeSwarmAccess) Preflight(project, task, key string) (sessions.PreflightReceipt, error) {
	a, token, e := p.authenticate()
	if e != nil {
		return sessions.PreflightReceipt{}, e
	}
	return a.CheckPreflight(token, p.connection.SessionID, project, task, key)
}
