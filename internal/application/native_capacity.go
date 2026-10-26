package application

import (
	"path/filepath"

	"github.com/mlnomadpy/dacli/internal/governance/executor"
	"github.com/mlnomadpy/dacli/internal/governance/nativeconfig"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

// NativeCapacityAccess holds a private observer connection. It revalidates
// the selected connection, credential, workspace binding and enrollment at
// each observation and read boundary.
type NativeCapacityAccess struct {
	path          string
	connection    nativeconfig.Connection
	workspaceRoot string
}

func OpenNativeCapacity(path string, w *workspace.Workspace) (*NativeCapacityAccess, error) {
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
	p := &NativeCapacityAccess{path: path, connection: c, workspaceRoot: resolved.Root}
	if _, _, err := p.authenticate(); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *NativeCapacityAccess) authenticate() (*sessions.Authority, string, error) {
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
	a, err := sessions.OpenHostCapacity(c.Authority, sessions.Binding{Workspace: w.ID, Installation: c.InstallationID})
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

func (p *NativeCapacityAccess) Observe(report executor.CapacityReport) (sessions.HostCapacityObservation, bool, error) {
	a, token, err := p.authenticate()
	if err != nil {
		return sessions.HostCapacityObservation{}, false, err
	}
	return a.RecordHostCapacityObservation(token, p.connection.SessionID, executor.ObservationRequest(report))
}

func (p *NativeCapacityAccess) Read(hostSessionID string) (executor.CapacityFacts, error) {
	a, token, err := p.authenticate()
	if err != nil {
		return executor.CapacityFacts{}, err
	}
	view, err := a.ReadHostCapacity(token, p.connection.SessionID, hostSessionID)
	if err != nil {
		return executor.CapacityFacts{}, err
	}
	return executor.CapacityFacts{
		SchemaVersion: 1, RequestedState: view.Policy.State, DeclarationState: view.DeclarationState,
		ObservationState: view.ObservationState, QualificationState: view.QualificationState, ConsistencyState: view.ConsistencyState,
		RequestedSlots: view.Policy.RequestedSlots, DeclaredSlots: view.DeclaredSlots,
		OccupiedSlots: view.OccupiedSlots, AvailableSlots: view.AvailableSlots,
		QualifiedSlots: view.QualifiedSlots, Freshness: view.Policy.Freshness, ObservedAt: view.ObservedAt,
		ReceivedAt: view.ReceivedAt, QualificationExpiresAt: view.QualificationExpiresAt,
	}, nil
}

// NativeCapacityOwner uses the existing private recovery-owner profile. The
// owner performs explicit schema migration, observer enrollment and
// qualification; executor reports cannot perform any of those operations.
type NativeCapacityOwner struct{ owner *NativeRecoveryAccess }

func OpenNativeCapacityOwner(path string) (*NativeCapacityOwner, error) {
	p, err := OpenNativeRecovery(path)
	if err != nil {
		return nil, sessions.ErrDenied
	}
	return &NativeCapacityOwner{owner: p}, nil
}

func (p *NativeCapacityOwner) Upgrade() error {
	if p == nil || p.owner == nil {
		return sessions.ErrDenied
	}
	a, owner, err := p.owner.authenticate()
	if err != nil {
		return err
	}
	return a.UpgradeHostCapacity(owner)
}

func (p *NativeCapacityOwner) EnrollObserver(observer sessions.HostCapacityObserver) (sessions.HostCapacityObserver, string, error) {
	if p == nil || p.owner == nil {
		return sessions.HostCapacityObserver{}, "", sessions.ErrDenied
	}
	a, owner, err := p.owner.authenticate()
	if err != nil {
		return sessions.HostCapacityObserver{}, "", err
	}
	return a.EnrollHostCapacityObserver(owner, observer)
}

func (p *NativeCapacityOwner) Configure(policy sessions.HostCapacityPolicy) (sessions.HostCapacityPolicy, error) {
	if p == nil || p.owner == nil {
		return sessions.HostCapacityPolicy{}, sessions.ErrDenied
	}
	a, owner, err := p.owner.authenticate()
	if err != nil {
		return sessions.HostCapacityPolicy{}, err
	}
	return a.ConfigureHostCapacityPolicy(owner, policy)
}

func (p *NativeCapacityOwner) Qualify(qualification sessions.HostCapacityQualification) (sessions.HostCapacityQualification, error) {
	if p == nil || p.owner == nil {
		return sessions.HostCapacityQualification{}, sessions.ErrDenied
	}
	a, owner, err := p.owner.authenticate()
	if err != nil {
		return sessions.HostCapacityQualification{}, err
	}
	return a.QualifyHostCapacity(owner, qualification)
}
