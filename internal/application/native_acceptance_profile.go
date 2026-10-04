package application

import (
	"github.com/mlnomadpy/dacli/internal/governance/nativeconfig"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"reflect"
)

// NativeAcceptanceOwner authenticates explicit installation owner configuration.
// It never borrows issuer credentials or a worker's shared connection.
type NativeAcceptanceOwner struct {
	owner      *NativeRecoveryAccess
	policyPath string
	policy     NativeReviewPolicy
}

func OpenNativeAcceptanceOwner(configPath, policyPath string) (*NativeAcceptanceOwner, error) {
	owner, e := OpenNativeRecovery(configPath)
	if e != nil {
		return nil, e
	}
	var p NativeReviewPolicy
	if verificationPrivateJSON(policyPath, &p) != nil || validateNativeReviewPolicy(p) != nil {
		return nil, sessions.ErrDenied
	}
	w, e := nativeconfig.ResolveWorkspace(owner.config.Workspace, p.WorkspaceID)
	if e != nil || w.ID != p.WorkspaceID {
		return nil, sessions.ErrDenied
	}
	return &NativeAcceptanceOwner{owner, policyPath, p}, nil
}
func (p *NativeAcceptanceOwner) Upgrade() error {
	if p == nil {
		return sessions.ErrDenied
	}
	a, o, e := p.owner.authenticate()
	if e != nil {
		return e
	}
	return a.UpgradeNativeAcceptance(o)
}
func (p *NativeAcceptanceOwner) authenticate() (*sessions.Authority, string, *nativeAcceptanceObserver, error) {
	if p == nil {
		return nil, "", nil, sessions.ErrDenied
	}
	_, owner, e := p.owner.authenticate()
	if e != nil {
		return nil, "", nil, e
	}
	var current NativeReviewPolicy
	if verificationPrivateJSON(p.policyPath, &current) != nil || !reflect.DeepEqual(current, p.policy) {
		return nil, "", nil, sessions.ErrDenied
	}
	w, e := nativeconfig.ResolveWorkspace(p.owner.config.Workspace, p.policy.WorkspaceID)
	if e != nil {
		return nil, "", nil, e
	}
	observer := &nativeAcceptanceObserver{workspace: w, policy: privateNativeReviewPolicy{p.policyPath, p.policy.WorkspaceID, p.policy.ProjectID, p.policy.TaskID}}
	admission, e := NewLeaseAdmission(w)
	if e != nil {
		return nil, "", nil, e
	}
	a, e := sessions.OpenReviewAuthority(p.owner.config.Authority, sessions.Binding{Workspace: w.ID, Installation: p.owner.config.InstallationID}, observer, admission)
	return a, owner, observer, e
}
