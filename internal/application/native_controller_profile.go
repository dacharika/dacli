package application

import (
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"time"
)

// NativeControllerOwner authenticates explicit owner configuration on each call.
type NativeControllerOwner struct{ owner *NativePreflightOwner }

func OpenNativeControllerOwner(path string) (*NativeControllerOwner, error) {
	o, e := OpenNativePreflightOwner(path)
	if e != nil {
		return nil, e
	}
	return &NativeControllerOwner{owner: o}, nil
}
func (p *NativeControllerOwner) Upgrade() error {
	a, o, e := p.owner.authenticate()
	if e != nil {
		return e
	}
	return a.UpgradeController(o)
}
func (p *NativeControllerOwner) Configure(r sessions.ControllerPolicy) error {
	a, o, e := p.owner.authenticate()
	if e != nil {
		return e
	}
	return a.ConfigureController(o, r)
}
func (p *NativeControllerOwner) Start(r sessions.ControllerStartRequest) (sessions.ControllerStatus, error) {
	a, o, e := p.owner.authenticate()
	if e != nil {
		return sessions.ControllerStatus{}, e
	}
	return a.StartController(o, r)
}
func (p *NativeControllerOwner) Enroll(project string, expires time.Time) (sessions.Session, string, error) {
	a, o, e := p.owner.authenticate()
	if e != nil {
		return sessions.Session{}, "", e
	}
	scope, e := a.ControllerEnrollmentScope(o, project)
	if e != nil {
		return sessions.Session{}, "", e
	}
	return a.Enroll(o, scope, expires)
}

// NativeControllerAccess contains the controller credential only. Independent
// worker/runner/reviewer actions are emitted as descriptors, never executed here.
type NativeControllerAccess struct{ swarm *NativeSwarmAccess }

func OpenNativeController(connection, capability string) (*NativeControllerAccess, error) {
	s, e := OpenNativeSwarm(connection, capability)
	if e != nil {
		return nil, e
	}
	return &NativeControllerAccess{swarm: s}, nil
}
