package application

import (
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/workspace"
	"path/filepath"
	"strings"
)

// LeaseAdmission reads only the explicitly owner-selected workspace. Markdown
// writers do not share the authority lock: this is a fresh readiness observation,
// not atomic readiness against concurrent legacy edits. Shared-writer gating
// requires a later persistence cutover. No task status is stored in authority.
type LeaseAdmission struct {
	workspace       workspace.Workspace
	swarmCapability sessions.SwarmCapabilityAdmission
}

func NewLeaseAdmission(w *workspace.Workspace) (*LeaseAdmission, error) {
	if w == nil || w.ID == "" || !filepath.IsAbs(w.Root) {
		return nil, sessions.ErrDenied
	}
	return &LeaseAdmission{workspace: *w}, nil
}
func (a *LeaseAdmission) AdmitLease(workspaceID, project, task string) error {
	if a == nil || workspaceID != a.workspace.ID {
		return sessions.ErrDenied
	}
	tasks, err := store.ListTasks(&a.workspace, "", "")
	if err != nil {
		return err
	}
	dups, err := store.DuplicateTaskFiles(&a.workspace)
	if err != nil {
		return err
	}
	if len(dups) != 0 {
		return sessions.ErrDenied
	}
	for _, broken := range store.BrokenTaskFiles() {
		if strings.HasPrefix(broken.Path, a.workspace.Root+string(filepath.Separator)) {
			return sessions.ErrDenied
		}
	}
	count := 0
	index := -1
	for i, t := range tasks {
		if t.ID == task {
			if t.Project != project {
				return sessions.ErrDenied
			}
			count++
			index = i
		}
	}
	if count != 1 {
		return sessions.ErrDenied
	}
	// The scheduler offers only unclaimed work; a claimed native task still
	// needs leases and fresh dependency checks (governance/031). Evaluate a
	// copy of only this active candidate without changing canonical status or
	// making another active task available to the scheduler.
	if tasks[index].Status == model.StatusActive {
		candidate := *tasks[index]
		candidate.Status = model.StatusOpen
		tasks[index] = &candidate
	}
	frontier := store.ReadyFrontierForProject(tasks, project)
	for _, t := range frontier.Ready {
		if t.ID == task {
			return nil
		}
	}
	return sessions.ErrDenied
}
