package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"reflect"

	"github.com/mlnomadpy/dacli/internal/governance/checkout"
	"github.com/mlnomadpy/dacli/internal/governance/nativeconfig"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/verificationrouting"
)

// Routing is private owner input. Its original bytes, not a re-rendered policy,
// are pinned in each grant alongside the normalized semantic policy digest.
type nativeVerificationRouting struct {
	policy                    verificationrouting.Policy
	digest, bytesDigest, base string
}

func nativeVerificationRoutingProfile(p NativeRunnerProfile) (*nativeVerificationRouting, error) {
	if p.SchemaVersion == 1 {
		if len(p.RoutingPolicy) != 0 || p.BaseCommit != "" {
			return nil, sessions.ErrDenied
		}
		return nil, nil
	}
	if p.SchemaVersion != 2 || len(p.RoutingPolicy) == 0 || len(p.RoutingPolicy) > 65536 || (len(p.BaseCommit) != 40 && len(p.BaseCommit) != 64) {
		return nil, sessions.ErrDenied
	}
	for _, c := range p.BaseCommit {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return nil, sessions.ErrDenied
		}
	}
	var policy verificationrouting.Policy
	if nativeconfig.Decode(p.RoutingPolicy, &policy) != nil || verificationrouting.ValidatePolicy(policy) != nil || len(policy.Gates) != len(p.Checks) {
		return nil, sessions.ErrDenied
	}
	digest, e := verificationrouting.PolicyDigest(policy)
	if e != nil {
		return nil, sessions.ErrDenied
	}
	raw := sha256.Sum256(p.RoutingPolicy)
	return &nativeVerificationRouting{policy: policy, digest: digest, bytesDigest: "sha256:" + hex.EncodeToString(raw[:]), base: p.BaseCommit}, nil
}
func (r *nativeVerificationRouting) matches(c sessions.CheckDescriptor) bool {
	if r == nil {
		return false
	}
	for _, g := range r.policy.Gates {
		if g.ID == c.ID {
			return c.Command == "" && g.Cwd == c.Cwd && g.Environment == verificationrouting.EnvironmentPolicy(c.Environment) && g.Deadline == c.Deadline && reflect.DeepEqual(g.Argv, c.Argv)
		}
	}
	return false
}
func (r *nativeVerificationRouting) plan(ctx context.Context, rev sessions.AttemptRevision) (verificationrouting.Plan, error) {
	obs := rev.Observation
	changed, e := checkout.ObserveChangedPaths(ctx, checkout.ChangedPathsRequest{Checkout: obs.Locator, Repository: obs.Repository, BaseCommit: r.base, RevisionCommit: obs.Commit, RevisionTree: obs.Tree})
	if e != nil {
		return verificationrouting.Plan{}, sessions.ErrDenied
	}
	changes := make([]verificationrouting.Change, 0, len(changed.Changes))
	for _, c := range changed.Changes {
		changes = append(changes, verificationrouting.Change{Path: c.Path, Kind: c.Kind})
	}
	plan, e := verificationrouting.Select(r.policy, verificationrouting.Binding{Repository: obs.Repository, Checkout: obs.Locator, BaseCommit: r.base, RevisionCommit: obs.Commit, RevisionTree: obs.Tree, PolicyDigest: r.digest, PolicyBytesDigest: r.bytesDigest}, changes)
	if e != nil {
		return verificationrouting.Plan{}, sessions.ErrDenied
	}
	return plan, nil
}
func (r *nativeVerificationRouting) selected(ctx context.Context, rev sessions.AttemptRevision, id string) error {
	plan, e := r.plan(ctx, rev)
	if e != nil {
		return e
	}
	for _, s := range plan.Selected {
		if s.Gate.ID == id {
			return nil
		}
	}
	return sessions.ErrDenied
}

// InspectRouting returns an observational plan for a freshly authenticated,
// exact revision. It cannot reserve execution or satisfy required-set completion.
func (a *NativeVerificationAccess) InspectRouting(ctx context.Context, req NativeVerificationRequest) (verificationrouting.Plan, error) {
	if a == nil || a.Runner == nil || a.Runner.routing == nil || ctx == nil || !ValidNativeVerificationRequest(req) {
		return verificationrouting.Plan{}, sessions.ErrDenied
	}
	req.WorkerSession = a.Worker.ID
	c, ok := a.Runner.checks[req.CheckID]
	if !ok {
		return verificationrouting.Plan{}, sessions.ErrDenied
	}
	request := sessions.VerificationRequest{OperationKey: req.OperationKey, WorkerSession: req.WorkerSession, AttemptID: req.AttemptID, LeaseID: req.LeaseID, RevisionID: req.RevisionID, CheckID: req.CheckID, GrantID: c.GrantID, ExpectedFence: req.ExpectedFence}
	rev, e := a.Runner.authority.ObserveVerificationRevision(a.workerToken, a.Runner.token, a.Runner.session, request, sessions.CheckDigest(c.Descriptor))
	if e != nil {
		return verificationrouting.Plan{}, e
	}
	return a.Runner.routing.plan(ctx, rev)
}
