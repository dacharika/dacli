package application

import (
	"path/filepath"

	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

// NativeReviewPolicySource is a trusted owner-configured dependency, not review
// request data. Implementations must return the current owner-selected policy.
type NativeReviewPolicySource interface {
	ReviewPolicy(workspace, project, task string) (string, []sessions.RequiredCheck, error)
}
type NativeReviewObserver struct {
	workspace workspace.Workspace
	policy    NativeReviewPolicySource
}

func NewNativeReviewObserver(w *workspace.Workspace, policy NativeReviewPolicySource) (*NativeReviewObserver, error) {
	if w == nil || w.ID == "" || !filepath.IsAbs(w.Root) || policy == nil {
		return nil, sessions.ErrDenied
	}
	return &NativeReviewObserver{*w, policy}, nil
}

// ObserveReview reads the canonical task afresh. Markdown writers do not share
// the authority lock; this is an observation, not an atomic task-status model.
func (o *NativeReviewObserver) ObserveReview(workspaceID, project, task string) (sessions.ReviewObservation, error) {
	if o == nil || workspaceID != o.workspace.ID {
		return sessions.ReviewObservation{}, sessions.ErrDenied
	}
	canonical, err := store.ReadCheckpointCanonical(&o.workspace)
	if err != nil {
		return sessions.ReviewObservation{}, sessions.ErrDenied
	}
	tasks := canonical.Tasks
	var selected *store.Task
	for _, t := range tasks {
		if t.ID == task {
			if selected != nil || t.Project != project {
				return sessions.ReviewObservation{}, sessions.ErrDenied
			}
			selected = t
		}
	}
	if selected == nil || !store.HasAcceptanceCriteria(selected) {
		return sessions.ReviewObservation{}, sessions.ErrDenied
	}
	digest, err := store.AcceptanceChecklistDigest(selected)
	if err != nil {
		return sessions.ReviewObservation{}, err
	}
	revision, checks, err := o.policy.ReviewPolicy(workspaceID, project, task)
	if err != nil {
		return sessions.ReviewObservation{}, err
	}
	return sessions.ReviewObservation{ChecklistDigest: digest, PolicyRevision: revision, Checks: append([]sessions.RequiredCheck(nil), checks...)}, nil
}
