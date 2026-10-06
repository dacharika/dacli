package application

import (
	"context"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/store"
	"reflect"
)

func externalObservation(o store.NativeExternalFacts) sessions.NativeExternalObservation {
	return sessions.NativeExternalObservation{Generation: o.Generation, SliceGeneration: o.SliceGeneration, Branch: o.Branch, Commit: o.Commit, Tree: o.Tree, Trunk: o.Trunk, RemoteHead: o.RemoteHead, Clean: o.Clean, PullRequestNumber: o.PullRequestNumber, PullRequestURL: o.PullRequestURL, PullRequestState: o.PullRequestState, MergeCommit: o.MergeCommit, ChecksSatisfied: o.ChecksSatisfied, APIComplete: o.APIComplete, Diagnosis: o.Diagnosis, ObservedAt: o.ObservedAt}
}
func (p *NativeExternalOwner) Apply(ctx context.Context, input sessions.NativeExternalInput) (sessions.NativeExternalResult, error) {
	return p.run(ctx, input, false)
}
func (p *NativeExternalOwner) Reconcile(ctx context.Context, input sessions.NativeExternalInput) (sessions.NativeExternalResult, error) {
	return p.run(ctx, input, true)
}
func (p *NativeExternalOwner) run(ctx context.Context, input sessions.NativeExternalInput, reconcile bool) (sessions.NativeExternalResult, error) {
	a, owner, e := p.authenticate()
	if e != nil || ctx == nil {
		return sessions.NativeExternalResult{}, sessions.ErrDenied
	}
	c := input.Candidate
	q := p.profile.Policy
	if c.ProjectID != q.ProjectID || c.TaskID != q.TaskID || c.Repository != q.Repository || c.Commit != q.Commit || c.Tree != q.Tree || input.PolicyRevision != q.Revision {
		return sessions.NativeExternalResult{}, sessions.ErrDenied
	}
	credential, e := p.selectionCurrent(owner)
	if e != nil {
		return sessions.NativeExternalResult{}, e
	}
	target, e := p.target(credential)
	if e != nil {
		return sessions.NativeExternalResult{}, e
	}
	var out sessions.NativeExternalResult
	e = store.WithNativeExternalTarget(ctx, target, func() error {
		inspect := func(rec sessions.NativeExternalRecord) (sessions.NativeExternalObservation, error) {
			if !reflect.DeepEqual(rec.Input, input) || rec.Policy.TaskID != q.TaskID || rec.Policy.ProjectID != q.ProjectID || rec.Policy.Revision != q.Revision {
				return sessions.NativeExternalObservation{}, sessions.ErrDenied
			}
			credential, e := p.selectionCurrent(owner)
			if e != nil {
				return sessions.NativeExternalObservation{}, e
			}
			target, e := p.target(credential)
			if e != nil {
				return sessions.NativeExternalObservation{}, e
			}
			facts, e := store.ObserveNativeExternal(ctx, target)
			if e != nil {
				return sessions.NativeExternalObservation{}, e
			}
			if _, e = p.selectionCurrent(owner); e != nil {
				return sessions.NativeExternalObservation{}, e
			}
			return externalObservation(facts), nil
		}
		effect := func(rec sessions.NativeExternalRecord, final func() error) (sessions.NativeExternalObservation, error) {
			facts, e := store.ApplyNativeExternalEffect(ctx, target, rec.Input.Action, func() error {
				if _, e := p.selectionCurrent(owner); e != nil {
					return e
				}
				return final()
			})
			if e != nil {
				return sessions.NativeExternalObservation{}, e
			}
			return externalObservation(facts), nil
		}
		if reconcile {
			out, e = a.ReconcileNativeExternal(owner, input, inspect)
		} else {
			out, e = a.ApplyNativeExternal(owner, input, inspect, effect)
		}
		return e
	})
	if e != nil {
		return sessions.NativeExternalResult{}, e
	}
	return out, nil
}
