package application

import "github.com/mlnomadpy/dacli/internal/governance/sessions"

type NativeBudgetOwner struct{ owner *NativePreflightOwner }

func OpenNativeBudgetOwner(path string) (*NativeBudgetOwner, error) {
	p, e := OpenNativePreflightOwner(path)
	if e != nil {
		return nil, e
	}
	return &NativeBudgetOwner{p}, nil
}
func (p *NativeBudgetOwner) Upgrade() error {
	a, o, e := p.owner.authenticate()
	if e != nil {
		return e
	}
	return a.UpgradeBudgets(o)
}
func (p *NativeBudgetOwner) Configure(r sessions.BudgetPolicy) error {
	a, o, e := p.owner.authenticate()
	if e != nil {
		return e
	}
	return a.ConfigureBudget(o, r)
}
func (p *NativeSwarmAccess) BudgetStatus(project string) (sessions.BudgetStatus, error) {
	a, t, e := p.authenticate()
	if e != nil {
		return sessions.BudgetStatus{}, e
	}
	return a.BudgetStatus(t, p.connection.SessionID, project)
}
func (p *NativeSwarmAccess) QuoteBudget(project, task string) (sessions.BudgetQuote, error) {
	a, t, e := p.authenticate()
	if e != nil {
		return sessions.BudgetQuote{}, e
	}
	return a.QuoteBudget(t, p.connection.SessionID, project, task)
}
func (p *NativeSwarmAccess) SettleBudget(r sessions.BudgetSettleRequest) (sessions.BudgetAllocation, error) {
	a, t, e := p.authenticate()
	if e != nil {
		return sessions.BudgetAllocation{}, e
	}
	return a.SettleBudget(t, p.connection.SessionID, r)
}
