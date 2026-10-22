package application

import (
	"github.com/mlnomadpy/dacli/internal/governance/nativeconfig"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"path/filepath"
)

// NativeRecoveryAccess requires explicit private owner configuration; no session grant is sufficient.
type NativeRecoveryAccess struct {
	path   string
	config recoveryOwnerConfig
}
type recoveryOwnerConfig struct {
	SchemaVersion       int    `json:"schema_version"`
	Workspace           string `json:"workspace"`
	Authority           string `json:"authority"`
	InstallationID      string `json:"installation_id"`
	OwnerCredentialFile string `json:"owner_credential_file"`
}

func OpenNativeRecovery(path string) (*NativeRecoveryAccess, error) {
	data, e := nativeconfig.ReadPrivate(path, 16384)
	var c recoveryOwnerConfig
	if e != nil || nativeconfig.Decode(data, &c) != nil || c.SchemaVersion != 1 || c.InstallationID == "" || !filepath.IsAbs(c.Authority) || !filepath.IsAbs(c.OwnerCredentialFile) {
		return nil, sessions.ErrDenied
	}
	p := &NativeRecoveryAccess{path, c}
	if _, _, e = p.authenticate(); e != nil {
		return nil, e
	}
	return p, nil
}
func (p *NativeRecoveryAccess) authenticate() (*sessions.Authority, string, error) {
	data, e := nativeconfig.ReadPrivate(p.path, 16384)
	var c recoveryOwnerConfig
	if e != nil || nativeconfig.Decode(data, &c) != nil || c != p.config {
		return nil, "", sessions.ErrDenied
	}
	w, e := nativeconfig.ResolveWorkspace(c.Workspace, "")
	if e != nil {
		return nil, "", sessions.ErrDenied
	}
	a, e := sessions.Open(c.Authority, sessions.Binding{Workspace: w.ID, Installation: c.InstallationID})
	if e != nil {
		return nil, "", e
	}
	owner, e := nativeconfig.ReadCredential(c.OwnerCredentialFile)
	if e != nil || a.AuthenticateOwner(owner) != nil {
		return nil, "", sessions.ErrDenied
	}
	return a, owner, nil
}
func (p *NativeRecoveryAccess) Upgrade() error {
	a, o, e := p.authenticate()
	if e != nil {
		return e
	}
	return a.UpgradeRecovery(o)
}
func (p *NativeRecoveryAccess) Configure(r sessions.RecoveryPolicy) error {
	a, o, e := p.authenticate()
	if e != nil {
		return e
	}
	return a.ConfigureRecovery(o, r)
}
func (p *NativeRecoveryAccess) Preview(kind, id string) (sessions.RecoveryTarget, error) {
	a, o, e := p.authenticate()
	if e != nil {
		return sessions.RecoveryTarget{}, e
	}
	return a.OwnerRecoveryTarget(o, kind, id)
}
func (p *NativeRecoveryAccess) Apply(r sessions.RecoveryRequest) (sessions.RecoveryReceipt, error) {
	a, o, e := p.authenticate()
	if e != nil {
		return sessions.RecoveryReceipt{}, e
	}
	return a.Recover(o, r)
}
