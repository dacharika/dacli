package application

import (
	"context"
	"reflect"

	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/store"
)

// NativeIntegrationOwner selects separate private owner, review and integration
// files. A worker/reviewer connection is not an integration credential.
type NativeIntegrationOwner struct {
	acceptance *NativeAcceptanceOwner
	policyPath string
	policy     sessions.NativeIntegrationPolicy
}

func (NativeIntegrationOwner) String() string   { return "NativeIntegrationOwner" }
func (NativeIntegrationOwner) GoString() string { return "NativeIntegrationOwner" }
func OpenNativeIntegrationOwner(configPath, reviewPolicyPath, integrationPolicyPath string) (*NativeIntegrationOwner, error) {
	acceptance, e := OpenNativeAcceptanceOwner(configPath, reviewPolicyPath)
	if e != nil {
		return nil, e
	}
	var policy sessions.NativeIntegrationPolicy
	if verificationPrivateJSON(integrationPolicyPath, &policy) != nil || policy.SchemaVersion != 1 || policy.ProjectID != acceptance.policy.ProjectID || policy.TaskID != acceptance.policy.TaskID || !policy.ConfiguredAt.IsZero() {
		return nil, sessions.ErrDenied
	}
	p := &NativeIntegrationOwner{acceptance: acceptance, policyPath: integrationPolicyPath, policy: policy}
	if _, _, _, e = p.authenticate(); e != nil {
		return nil, e
	}
	return p, nil
}
func (p *NativeIntegrationOwner) authenticate() (*sessions.Authority, string, *nativeAcceptanceObserver, error) {
	if p == nil || p.acceptance == nil {
		return nil, "", nil, sessions.ErrDenied
	}
	var current sessions.NativeIntegrationPolicy
	if verificationPrivateJSON(p.policyPath, &current) != nil || !reflect.DeepEqual(current, p.policy) {
		return nil, "", nil, sessions.ErrDenied
	}
	return p.acceptance.authenticate()
}
func integrationTarget(p sessions.NativeIntegrationPolicy, input sessions.NativeAcceptanceInput) store.NativeIntegrationTarget {
	return store.NativeIntegrationTarget{Repository: p.Repository, Checkout: p.TargetCheckout, Ref: p.TargetRef, ExpectedCommit: p.ExpectedCommit, ExpectedTree: p.ExpectedTree, Commit: input.Commit, Tree: input.Tree}
}
func integrationObservation(o store.NativeIntegrationGitObservation) sessions.NativeIntegrationObservation {
	return sessions.NativeIntegrationObservation{Commit: o.Commit, Tree: o.Tree, Clean: o.Clean, ObservedAt: o.ObservedAt}
}
func (p *NativeIntegrationOwner) Configure(ctx context.Context) (sessions.NativeIntegrationPolicy, error) {
	a, owner, _, e := p.authenticate()
	if e != nil {
		return sessions.NativeIntegrationPolicy{}, e
	}
	var result sessions.NativeIntegrationPolicy
	target := integrationTarget(p.policy, sessions.NativeAcceptanceInput{})
	e = store.WithNativeIntegrationTarget(ctx, target, func() error {
		observed, e := store.ObserveNativeIntegrationTarget(ctx, target)
		if e != nil || !observed.Clean || observed.Commit != p.policy.ExpectedCommit || observed.Tree != p.policy.ExpectedTree {
			return sessions.ErrDenied
		}
		// Re-read the private owner selections after slow Git work.
		if _, _, _, e = p.authenticate(); e != nil {
			return e
		}
		result, e = a.ConfigureNativeIntegration(owner, p.policy)
		return e
	})
	if e != nil {
		return sessions.NativeIntegrationPolicy{}, e
	}
	return result, nil
}
func (p *NativeIntegrationOwner) Inspect(key string) (sessions.NativeIntegrationRecord, error) {
	a, owner, _, e := p.authenticate()
	if e != nil {
		return sessions.NativeIntegrationRecord{}, e
	}
	rec, e := a.ReadNativeIntegration(owner, key)
	if e != nil || rec.Input.Candidate.TaskID != p.policy.TaskID || rec.Input.Candidate.ProjectID != p.policy.ProjectID {
		return sessions.NativeIntegrationRecord{}, sessions.ErrDenied
	}
	return rec, nil
}

func (p *NativeRecoveryAccess) UpgradeIntegration() error {
	a, owner, e := p.authenticate()
	if e != nil {
		return e
	}
	return a.UpgradeNativeIntegration(owner)
}

func (p *NativeIntegrationOwner) InspectPolicy() (sessions.NativeIntegrationPolicy, error) {
	a, owner, _, e := p.authenticate()
	if e != nil {
		return sessions.NativeIntegrationPolicy{}, e
	}
	current, e := a.ReadNativeIntegrationPolicy(owner, p.policy.TaskID)
	if e != nil || current.ProjectID != p.policy.ProjectID {
		return sessions.NativeIntegrationPolicy{}, sessions.ErrDenied
	}
	return current, nil
}
