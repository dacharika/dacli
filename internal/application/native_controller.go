package application

import "github.com/mlnomadpy/dacli/internal/governance/sessions"

func (p *NativeControllerAccess) Step(r sessions.ControllerStepRequest) (sessions.ControllerStatus, error) {
	a, t, e := p.swarm.authenticate()
	if e != nil {
		return sessions.ControllerStatus{}, e
	}
	return a.StepController(t, p.swarm.connection.SessionID, r)
}
func (p *NativeControllerAccess) Reconcile(r sessions.ControllerStepRequest) (sessions.ControllerStatus, error) {
	a, t, e := p.swarm.authenticate()
	if e != nil {
		return sessions.ControllerStatus{}, e
	}
	return a.ReconcileController(t, p.swarm.connection.SessionID, r)
}
func (p *NativeControllerAccess) Status(project, id string) (sessions.ControllerStatus, error) {
	a, t, e := p.swarm.authenticate()
	if e != nil {
		return sessions.ControllerStatus{}, e
	}
	return a.ControllerStatus(t, p.swarm.connection.SessionID, project, id)
}
