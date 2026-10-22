package application

import "github.com/mlnomadpy/dacli/internal/governance/sessions"

// NativeModelRoutingOwner reuses explicit private owner authentication; neither
// a public routing explanation nor an MCP caller can configure model authority.
type NativeModelRoutingOwner struct{ owner *NativePreflightOwner }

func OpenNativeModelRoutingOwner(path string) (*NativeModelRoutingOwner, error) {
	owner, e := OpenNativePreflightOwner(path)
	if e != nil {
		return nil, e
	}
	return &NativeModelRoutingOwner{owner: owner}, nil
}
func (p *NativeModelRoutingOwner) Upgrade() error {
	a, owner, e := p.owner.authenticate()
	if e != nil {
		return e
	}
	return a.UpgradeNativeModelRouting(owner)
}
func (p *NativeModelRoutingOwner) Configure(policy sessions.NativeModelRoutingPolicy) error {
	a, owner, e := p.owner.authenticate()
	if e != nil {
		return e
	}
	return a.ConfigureNativeModelRouting(owner, policy)
}

// Routing is an authenticated historical explanation, never a host permit.
func (p *NativeSwarmAccess) Routing(project, id string) (sessions.NativeModelRoutingSelection, error) {
	a, token, e := p.authenticate()
	if e != nil {
		return sessions.NativeModelRoutingSelection{}, e
	}
	return a.ReadDispatchRouting(token, p.connection.SessionID, project, id)
}
