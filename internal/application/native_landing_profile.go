package application

import (
	"context"
	"strings"

	"github.com/mlnomadpy/dacli/internal/governance/nativeconfig"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

// NativeLandingOwner binds explicit private owner credentials to one canonical
// workspace and project. Worker and reader connection profiles are never used.
type NativeLandingOwner struct {
	owner           *NativeRecoveryAccess
	project         string
	workspace       *workspace.Workspace
	observePR       func(context.Context, string, int) (nativeLandingPRObservation, error)
	validateLanding func(context.Context, *workspace.Workspace, *store.Task, store.TaskLandingObservation, string) error
}

func OpenNativeLandingOwner(configPath, project string) (*NativeLandingOwner, error) {
	if project == "" || !workspace.SafeSegment(project) {
		return nil, sessions.ErrDenied
	}
	owner, err := OpenNativeRecovery(configPath)
	if err != nil {
		return nil, sessions.ErrDenied
	}
	_, _, err = owner.authenticate()
	if err != nil {
		return nil, sessions.ErrDenied
	}
	w, err := workspace.Find(owner.config.Workspace)
	if err != nil {
		return nil, sessions.ErrDenied
	}
	if _, err = store.LoadProject(w, project); err != nil {
		return nil, sessions.ErrDenied
	}
	return &NativeLandingOwner{owner: owner, project: project, workspace: w}, nil
}

func (p *NativeLandingOwner) authenticate() (*sessions.Authority, string, error) {
	if p == nil || p.owner == nil || p.workspace == nil || p.project == "" {
		return nil, "", sessions.ErrDenied
	}
	authority, owner, err := p.owner.authenticate()
	if err != nil {
		return nil, "", sessions.ErrDenied
	}
	w, err := workspace.Find(p.owner.config.Workspace)
	if err != nil || w.ID != p.workspace.ID || w.Root != p.workspace.Root {
		return nil, "", sessions.ErrDenied
	}
	if _, err := store.LoadProject(w, p.project); err != nil {
		return nil, "", sessions.ErrDenied
	}
	return authority, owner, nil
}

func (p *NativeLandingOwner) selectedOwnerCurrent(owner string) error {
	if p == nil || p.owner == nil || p.workspace == nil {
		return sessions.ErrDenied
	}
	// This check is safe inside WithOwnerNativeLandingSource: it reads only the
	// already pinned private selection and credential and never reacquires the
	// authority lock.
	var current recoveryOwnerConfig
	if verificationPrivateJSON(p.owner.path, &current) != nil || current != p.owner.config {
		return sessions.ErrDenied
	}
	credential, err := nativeconfig.ReadCredential(p.owner.config.OwnerCredentialFile)
	if err != nil || credential != owner {
		return sessions.ErrDenied
	}
	w, err := workspace.Find(p.owner.config.Workspace)
	if err != nil || w.ID != p.workspace.ID || w.Root != p.workspace.Root {
		return sessions.ErrDenied
	}
	return nil
}

func (p *NativeLandingOwner) validateTaskLanding(ctx context.Context, task *store.Task, observation store.TaskLandingObservation, base string) error {
	if p != nil && p.validateLanding != nil {
		return p.validateLanding(ctx, p.workspace, task, observation, base)
	}
	return store.ValidateTaskLanding(ctx, p.workspace, task, observation, base)
}

func (p *NativeLandingOwner) selectedLandingPolicyCurrent(ctx context.Context, expected model.LandingPolicy, repository, digest string) error {
	if p == nil || p.workspace == nil || ctx == nil || ctx.Err() != nil {
		return sessions.ErrDenied
	}
	project, err := store.LoadProject(p.workspace, p.project)
	if err != nil || project.Landing != expected {
		return sessions.ErrDenied
	}
	githubRepository, _ := project.Doc.Front.Get("github_repo")
	if strings.TrimSpace(githubRepository) != repository {
		return sessions.ErrDenied
	}
	currentDigest, err := store.TaskLandingPolicyDigestContext(ctx, p.workspace, p.project)
	if err != nil || currentDigest != digest || ctx.Err() != nil {
		return sessions.ErrDenied
	}
	return nil
}
