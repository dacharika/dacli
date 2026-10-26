package application

import (
	"context"
	"errors"
	"path/filepath"

	operatorv1 "github.com/mlnomadpy/dacli/contracts/operator/v1"
	"github.com/mlnomadpy/dacli/internal/governance/nativeconfig"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/operatorread"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

// NativeOperatorRead selects one explicitly provisioned project reader. It
// retains only the immutable connection selection; credentials and authority
// are reopened and authenticated for every request.
type NativeOperatorRead struct {
	dashboard *NativeDashboard
	workspace *workspace.Workspace
}

func OpenOperatorRead(connectionPath string) (*NativeOperatorRead, error) {
	if !filepath.IsAbs(connectionPath) {
		return nil, nativeconfig.ErrDenied
	}
	raw, err := nativeconfig.ReadPrivate(connectionPath, 16*1024)
	if err != nil {
		return nil, nativeconfig.ErrDenied
	}
	var connection nativeconfig.Connection
	if nativeconfig.Decode(raw, &connection) != nil || (connection.SchemaVersion != 1 && connection.SchemaVersion != 2) || connection.WorkspaceID == "" || connection.SessionID == "" || connection.InstallationID == "" || !filepath.IsAbs(connection.Authority) || !filepath.IsAbs(connection.CredentialFile) {
		return nil, nativeconfig.ErrDenied
	}
	w, err := nativeconfig.ResolveWorkspace(connection.Workspace, connection.WorkspaceID)
	if err != nil {
		return nil, nativeconfig.ErrDenied
	}
	reader, err := OpenNativeDashboard(connectionPath, w)
	if err != nil {
		return nil, nativeconfig.ErrDenied
	}
	return &NativeOperatorRead{dashboard: reader, workspace: w}, nil
}

func (p *NativeOperatorRead) Workspace() *workspace.Workspace {
	if p == nil {
		return nil
	}
	return p.workspace
}

// Authorize validates the selected workspace and reauthenticates the exact
// requested project/task at the native authority before any projection runs.
func (p *NativeOperatorRead) Authorize(ctx context.Context, req operatorv1.Request) (operatorv1.Scope, error) {
	if p == nil || p.dashboard == nil || p.workspace == nil || req.WorkspaceID != p.workspace.ID {
		return operatorv1.Scope{}, operatorread.ErrDenied
	}
	if err := ctx.Err(); err != nil {
		return operatorv1.Scope{}, operatorread.ErrStale
	}
	if _, err := p.dashboard.ReadOperator(req.ProjectID, ""); err != nil {
		return operatorv1.Scope{}, mapOperatorReadError(err)
	}
	if req.TaskID != "" {
		tasks, err := store.ListTasks(p.workspace, req.ProjectID, "")
		if err != nil {
			return operatorv1.Scope{}, operatorread.ErrUnavailable
		}
		found := false
		for _, task := range tasks {
			if task.ID == req.TaskID && task.Project == req.ProjectID {
				found = true
				break
			}
		}
		if !found {
			return operatorv1.Scope{}, operatorread.ErrDenied
		}
	}
	return operatorv1.Scope{WorkspaceID: p.workspace.ID, ProjectID: req.ProjectID, TaskID: req.TaskID}, nil
}

func (p *NativeOperatorRead) ReadOperator(project, task string) (sessions.DashboardSnapshot, error) {
	if p == nil || p.dashboard == nil || p.workspace == nil {
		return sessions.DashboardSnapshot{}, operatorread.ErrDenied
	}
	return p.dashboard.ReadOperator(project, task)
}

// ReadOperator reopens every private input and uses the separately provisioned
// operator.read action. NativeDashboard.Read stays compatible with existing
// dashboard readers and their generic read grants.
func (p *NativeDashboard) ReadOperator(project, task string) (sessions.DashboardSnapshot, error) {
	if p == nil {
		return sessions.DashboardSnapshot{}, nativeconfig.ErrDenied
	}
	raw, err := nativeconfig.ReadPrivate(p.path, 16*1024)
	if err != nil {
		return sessions.DashboardSnapshot{}, nativeconfig.ErrDenied
	}
	var connection nativeconfig.Connection
	if nativeconfig.Decode(raw, &connection) != nil || connection != p.connection {
		return sessions.DashboardSnapshot{}, nativeconfig.ErrDenied
	}
	w, err := nativeconfig.ResolveWorkspace(connection.Workspace, connection.WorkspaceID)
	if err != nil || w.Root != p.workspace.Root || w.ID != p.workspace.ID {
		return sessions.DashboardSnapshot{}, nativeconfig.ErrDenied
	}
	token, err := nativeconfig.ReadCredential(connection.CredentialFile)
	if err != nil {
		return sessions.DashboardSnapshot{}, nativeconfig.ErrDenied
	}
	authority, err := sessions.OpenDashboardAuthority(connection.Authority, sessions.Binding{Workspace: connection.WorkspaceID, Installation: connection.InstallationID})
	if err != nil {
		return sessions.DashboardSnapshot{}, err
	}
	return authority.ReadOperatorDashboard(token, connection.SessionID, project, task)
}

func mapOperatorReadError(err error) error {
	switch {
	case errors.Is(err, sessions.ErrDenied), errors.Is(err, nativeconfig.ErrDenied):
		return operatorread.ErrDenied
	case errors.Is(err, sessions.ErrStorage):
		return operatorread.ErrUnavailable
	default:
		return err
	}
}
