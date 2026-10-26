package application

import (
	"fmt"
	"path/filepath"

	"github.com/mlnomadpy/dacli/internal/governance/nativeconfig"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

// NativeIssueLinkOwner is an explicit private owner profile for applying an
// exact issue-to-task plan. It does not import issue prose or grant publication
// consent; it only protects mapping changes against retained native holds.
type NativeIssueLinkOwner struct {
	owner   *NativeRecoveryAccess
	project string
	root    *workspace.Workspace
}

func OpenNativeIssueLinkOwner(configPath, project string) (*NativeIssueLinkOwner, error) {
	if !workspace.SafeSegment(project) {
		return nil, sessions.ErrDenied
	}
	owner, err := OpenNativeRecovery(configPath)
	if err != nil {
		return nil, sessions.ErrDenied
	}
	w, err := nativeconfig.ResolveWorkspace(owner.config.Workspace, "")
	if err != nil || !filepath.IsAbs(w.ProjectPath(project)) {
		return nil, sessions.ErrDenied
	}
	if _, err = store.LoadProject(w, project); err != nil {
		return nil, sessions.ErrDenied
	}
	if _, _, err = owner.authenticate(); err != nil {
		return nil, sessions.ErrDenied
	}
	return &NativeIssueLinkOwner{owner: owner, project: project, root: w}, nil
}

// Apply refreshes owner authentication and holds the native authority lock
// before the store acquires each stable task lock, matching the existing
// authority->task ordering used by task claims and avoiding lock inversion.
func (p *NativeIssueLinkOwner) Apply(plan store.IssueLinkBatchPlan) error {
	if p == nil || p.owner == nil || plan.Project != p.project {
		return sessions.ErrDenied
	}
	authority, owner, err := p.owner.authenticate()
	if err != nil {
		return sessions.ErrDenied
	}
	return store.ApplyIssueLinkBatchWithValidation(p.root, plan, func(taskID string, commit func() error) error {
		return authority.WithOwnerTaskClaimsProtection(owner, taskID, func(protection sessions.TaskClaimsProtection) error {
			if protection.TaskID != taskID || len(protection.Holds) != 0 {
				return fmt.Errorf("native task protection unavailable or retained for %s: %v", taskID, protection.Holds)
			}
			return commit()
		})
	}, func() error {
		return store.ValidateIssueLinkBatchBindings(p.root, plan)
	})
}
