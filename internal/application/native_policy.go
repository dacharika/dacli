package application

import (
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/store"
)

// EvaluatePolicy prepares an exact deterministic canonical outcome without saving
// it. The private authority stores permission history, not a canonical effect.
func (p *NativeAcceptanceOwner) EvaluatePolicy(r sessions.NativePolicyEvaluationRequest) (sessions.NativePolicyDecision, error) {
	a, owner, observer, e := p.authenticate()
	if e != nil {
		return sessions.NativePolicyDecision{}, e
	}
	input := r.Input
	if input.ProjectID != p.policy.ProjectID || input.TaskID != p.policy.TaskID || input.WorkspaceID != p.policy.WorkspaceID {
		return sessions.NativePolicyDecision{}, sessions.ErrDenied
	}
	var out sessions.NativePolicyDecision
	e = store.WithNativeAcceptanceTask(observer.workspace, input.TaskID, input.ProjectID, func(t *store.Task) error {
		if t.Status == model.StatusDone {
			return sessions.ErrDenied
		}
		if marker, _ := t.Doc.Front.Get("native_acceptance.receipt"); marker != "" {
			return sessions.ErrDenied
		}
		if e := store.ValidateNativeAcceptanceClose(observer.workspace, t, input.Repository, input.Commit, input.Tree, ""); e != nil {
			return e
		}
		var e error
		out, e = a.EvaluateNativePolicy(owner, r, func(rec sessions.NativeAcceptanceRecord) (string, error) {
			_, digest, e := store.PrepareNativeAcceptanceTask(t, rec.CanonicalReceiptDigest, rec.Candidate.ReviewerSessionID, nativeAcceptanceSummary(input, rec.Candidate), rec.CreatedAt)
			return digest, e
		})
		return e
	})
	if e != nil {
		return sessions.NativePolicyDecision{}, e
	}
	return out, nil
}
