package application

import (
	"errors"
	"fmt"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/workspace"
	"reflect"
)

type nativeAcceptanceObserver struct {
	workspace       *workspace.Workspace
	policy          NativeReviewPolicySource
	receipt, before string
}

func (o *nativeAcceptanceObserver) ObserveReview(workspaceID, project, task string) (sessions.ReviewObservation, error) {
	if o == nil || workspaceID != o.workspace.ID {
		return sessions.ReviewObservation{}, sessions.ErrDenied
	}
	t, e := store.NativeAcceptanceTask(o.workspace, task, project)
	if e != nil {
		return sessions.ReviewObservation{}, e
	}
	digest, e := store.NativeAcceptanceOriginalChecklist(t, o.receipt, o.before)
	if e != nil {
		return sessions.ReviewObservation{}, e
	}
	revision, checks, e := o.policy.ReviewPolicy(workspaceID, project, task)
	if e != nil {
		return sessions.ReviewObservation{}, e
	}
	return sessions.ReviewObservation{ChecklistDigest: digest, PolicyRevision: revision, Checks: checks}, nil
}

// Apply performs task lock -> durable closing transaction -> current owner close
// transaction. Canonical and authority stores are not crash-atomic; exact readback
// repairs only this operation's canonical success and never restores task writers.
func (p *NativeAcceptanceOwner) Apply(input sessions.NativeAcceptanceInput, base string) (sessions.NativeAcceptanceResult, error) {
	return p.ApplyWithPolicy(input, base, "")
}
func (p *NativeAcceptanceOwner) ApplyWithPolicy(input sessions.NativeAcceptanceInput, base, decisionID string) (sessions.NativeAcceptanceResult, error) {
	a, owner, observer, e := p.authenticate()
	if e != nil {
		return sessions.NativeAcceptanceResult{}, e
	}
	if decisionID != "" {
		status, e := a.ReadNativePolicyStatus(owner, decisionID)
		if e != nil || !reflect.DeepEqual(status.Decision.Input, input) {
			return sessions.NativeAcceptanceResult{}, sessions.ErrDenied
		}
	}

	if input.ProjectID != p.policy.ProjectID || input.TaskID != p.policy.TaskID || input.WorkspaceID != p.policy.WorkspaceID {
		return sessions.NativeAcceptanceResult{}, sessions.ErrDenied
	}
	var out sessions.NativeAcceptanceResult
	e = store.WithNativeAcceptanceTask(observer.workspace, input.TaskID, input.ProjectID, func(t *store.Task) error {
		if t.Status != model.StatusDone {
			// No retirement or canonical write on fresh delivery/landing refusal.
			if e := store.ValidateNativeAcceptanceClose(observer.workspace, t, input.Repository, input.Commit, input.Tree, base); e != nil {
				return e
			}
		}
		var prepared string
		prepare := func(rec sessions.NativeAcceptanceRecord) (string, error) {
			if t.Status == model.StatusDone {
				return "", sessions.ErrDenied
			}
			if marker, _ := t.Doc.Front.Get("native_acceptance.receipt"); marker != "" {
				return "", sessions.ErrDenied
			}
			c := rec.Candidate
			summary := nativeAcceptanceSummary(input, c)
			body, digest, e := store.PrepareNativeAcceptanceTask(t, rec.CanonicalReceiptDigest, c.ReviewerSessionID, summary, rec.CreatedAt)
			prepared = body
			return digest, e
		}
		rec, e := a.BeginNativeAcceptanceWithPolicy(owner, input, decisionID, prepare)
		if e != nil {
			return e
		}
		observer.receipt, observer.before = rec.CanonicalReceiptDigest, input.ChecklistDigest
		if prepared == "" {
			marker, _ := t.Doc.Front.Get("native_acceptance.receipt")
			if marker == "" {
				digest, e := prepare(rec)
				if e != nil || digest != rec.CanonicalOutcomeDigest {
					return sessions.ErrReconcile
				}
			}
		}
		exactPartial := false
		inspect := func(rec sessions.NativeAcceptanceRecord) (bool, error) {
			fresh, e := store.NativeAcceptanceTask(observer.workspace, input.TaskID, input.ProjectID)
			if e != nil {
				return false, e
			}
			t = fresh
			digest, e := store.NativeAcceptanceOriginalChecklist(t, rec.CanonicalReceiptDigest, input.ChecklistDigest)
			if e != nil || digest != input.ChecklistDigest {
				return false, sessions.ErrDenied
			}
			marker, _ := t.Doc.Front.Get("native_acceptance.receipt")
			if marker != "" {
				outcome, e := store.ReadNativeAcceptanceOutcomeDigest(observer.workspace, input.TaskID, input.ProjectID)
				if e != nil || outcome != rec.CanonicalOutcomeDigest {
					return false, sessions.ErrDenied
				}
			}
			if t.Status == model.StatusDone {
				return marker == rec.CanonicalReceiptDigest, nil
			}
			exactPartial = marker == rec.CanonicalReceiptDigest && rec.State == "closing"
			if e = store.ValidateNativeAcceptanceClose(observer.workspace, t, input.Repository, input.Commit, input.Tree, base); e != nil {
				return false, e
			}
			// Rebind private policy after all landing/canonical observations.
			revision, checks, e := observer.policy.ReviewPolicy(input.WorkspaceID, input.ProjectID, input.TaskID)
			if e != nil || revision != input.PolicyRevision || !reflect.DeepEqual(checks, p.policy.Checks) {
				return false, sessions.ErrDenied
			}
			if marker == "" {
				body, digest, e := store.PrepareNativeAcceptanceTask(t, rec.CanonicalReceiptDigest, rec.Candidate.ReviewerSessionID, nativeAcceptanceSummary(input, rec.Candidate), rec.CreatedAt)
				if e != nil || digest != rec.CanonicalOutcomeDigest {
					return false, sessions.ErrDenied
				}
				prepared = body
			}
			return false, nil
		}
		closeTask := func(rec sessions.NativeAcceptanceRecord) error {
			return store.CloseTaskWithNativeAcceptance(observer.workspace, t, rec.CanonicalReceiptDigest, prepared, rec.CanonicalOutcomeDigest)
		}
		out, e = a.ContinueNativeAcceptance(owner, input, inspect, closeTask)
		if exactPartial && errors.Is(e, sessions.ErrDenied) {
			return fmt.Errorf("native acceptance exact partial canonical effect requires owner reconciliation; closing retirement and resources retained: %w", errors.Join(sessions.ErrDenied, sessions.ErrReconcile))
		}
		return e
	})
	if e != nil {
		return sessions.NativeAcceptanceResult{}, e
	}
	return out, nil
}

func nativeAcceptanceSummary(input sessions.NativeAcceptanceInput, c sessions.NativeAcceptanceCandidate) string {
	return fmt.Sprintf("review %s; worker %s; original lease %s fence %d; revision %s commit %s tree %s; trusted evidence %v", input.ReviewID, c.WorkerSessionID, input.LeaseID, input.ExpectedFence, input.RevisionID, input.Commit, input.Tree, c.Evidence)
}
