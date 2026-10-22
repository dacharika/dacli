package application

import (
	"context"
	"github.com/mlnomadpy/dacli/internal/governance/nativeconfig"
	"reflect"

	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/store"
)

// Apply performs only separately owner-authorized local integration. Canonical
// acceptance remains the distinct 578/579 policy-consuming task transaction.
func (p *NativeIntegrationOwner) Apply(ctx context.Context, input sessions.NativeIntegrationInput) (sessions.NativeIntegrationResult, error) {
	return p.run(ctx, input, false)
}
func (p *NativeIntegrationOwner) Reconcile(ctx context.Context, input sessions.NativeIntegrationInput) (sessions.NativeIntegrationResult, error) {
	return p.run(ctx, input, true)
}
func (p *NativeIntegrationOwner) run(ctx context.Context, input sessions.NativeIntegrationInput, reconcile bool) (sessions.NativeIntegrationResult, error) {
	a, owner, _, e := p.authenticate()
	if e != nil || ctx == nil {
		return sessions.NativeIntegrationResult{}, sessions.ErrDenied
	}
	candidate := input.Candidate
	if candidate.WorkspaceID != p.acceptance.policy.WorkspaceID || candidate.ProjectID != p.policy.ProjectID || candidate.TaskID != p.policy.TaskID || candidate.Repository != p.policy.Repository || input.PolicyRevision != p.policy.Revision {
		return sessions.NativeIntegrationResult{}, sessions.ErrDenied
	}
	// Exact completed replay is historical evidence and does not inspect or mutate
	// the target. It cannot yield a new integration permission after retirement.
	if old, e := a.ReadNativeIntegration(owner, input.OperationKey); e == nil && old.State == "integrated" {
		if !reflect.DeepEqual(old.Input, input) {
			return sessions.NativeIntegrationResult{}, sessions.ErrDenied
		}
		return sessions.NativeIntegrationResult{Record: old, Duplicate: true}, nil
	}
	target := integrationTarget(p.policy, candidate)
	var result sessions.NativeIntegrationResult
	e = store.WithNativeIntegrationTarget(ctx, target, func() error {
		inspect := func(rec sessions.NativeIntegrationRecord) (sessions.NativeIntegrationObservation, error) {
			// Operation callbacks never borrow a different policy or owner selection.
			if rec.Policy.ProjectID != p.policy.ProjectID || rec.Policy.TaskID != p.policy.TaskID || rec.Policy.Revision != p.policy.Revision || !reflect.DeepEqual(rec.Input, input) {
				return sessions.NativeIntegrationObservation{}, sessions.ErrDenied
			}
			if e := p.selectionCurrent(owner); e != nil {
				return sessions.NativeIntegrationObservation{}, e
			}
			observed, e := store.ObserveNativeIntegrationTarget(ctx, integrationTarget(rec.Policy, candidate))
			if e != nil {
				return sessions.NativeIntegrationObservation{}, e
			}
			return integrationObservation(observed), nil
		}
		effect := func(rec sessions.NativeIntegrationRecord, final func() error) (sessions.NativeIntegrationObservation, error) {
			if p.testBeforeGitEffect != nil {
				p.testBeforeGitEffect()
			}
			observed, e := store.ApplyNativeIntegrationFastForwardWithRefObserver(ctx, integrationTarget(rec.Policy, candidate), func() error {
				// Owner profile changes after Git observations cannot reach the effect.
				// Reading selected private bytes does not reacquire the authority lock.
				var current sessions.NativeIntegrationPolicy
				if verificationPrivateJSON(p.policyPath, &current) != nil || !reflect.DeepEqual(current, p.policy) {
					return sessions.ErrDenied
				}
				if e := p.selectionCurrent(owner); e != nil {
					return e
				}
				return final()
			}, p.testAfterRefCAS)
			if e != nil {
				return sessions.NativeIntegrationObservation{}, e
			}
			if p.testAfterGitEffect != nil {
				p.testAfterGitEffect()
			}
			return integrationObservation(observed), nil
		}
		if reconcile {
			result, e = a.ReconcileNativeIntegration(owner, input, inspect)
		} else {
			result, e = a.ApplyNativeIntegration(owner, input, inspect, effect)
		}
		return e
	})
	if e != nil {
		return sessions.NativeIntegrationResult{}, e
	}
	return result, nil
}

// ownerSelectionCurrent is a pure private configuration comparison used while
// the integration authority transaction already holds its serialization lock.
func (p *NativeAcceptanceOwner) ownerSelectionCurrent() error {
	var config recoveryOwnerConfig
	if verificationPrivateJSON(p.owner.path, &config) != nil || config != p.owner.config {
		return sessions.ErrDenied
	}
	var review NativeReviewPolicy
	if verificationPrivateJSON(p.policyPath, &review) != nil || !reflect.DeepEqual(review, p.policy) {
		return sessions.ErrDenied
	}
	return nil
}

func (p *NativeIntegrationOwner) selectionCurrent(owner string) error {
	if e := p.acceptance.ownerSelectionCurrent(); e != nil {
		return e
	}
	var current sessions.NativeIntegrationPolicy
	if verificationPrivateJSON(p.policyPath, &current) != nil || !reflect.DeepEqual(current, p.policy) {
		return sessions.ErrDenied
	}
	token, e := nativeconfig.ReadCredential(p.acceptance.owner.config.OwnerCredentialFile)
	if e != nil || token != owner {
		return sessions.ErrDenied
	}
	return nil
}
