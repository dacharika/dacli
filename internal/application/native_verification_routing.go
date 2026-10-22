package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"sort"

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

// requiredSet projects every required gate from the complete owner policy,
// preserving the plan digest without exporting command bytes/private profiles.
func requiredRoutingSet(r *nativeVerificationRouting, p NativeRunnerProfile, rev sessions.AttemptRevision) (*sessions.RequiredVerificationSet, error) {
	if r == nil {
		return nil, nil
	}
	plan, e := r.plan(context.Background(), rev)
	if e != nil {
		return nil, e
	}
	checks, e := NativeProfileCheckDescriptors(p, true)
	if e != nil {
		return nil, e
	}
	out := &sessions.RequiredVerificationSet{PolicyDigest: plan.Binding.PolicyDigest, PolicyBytesDigest: plan.Binding.PolicyBytesDigest, BaseCommit: plan.Binding.BaseCommit, RevisionID: rev.ID, RevisionCommit: plan.Binding.RevisionCommit, RevisionTree: plan.Binding.RevisionTree, PlanDigest: plan.Digest}
	for _, id := range plan.RequiredGateIDs {
		found := false
		for _, check := range checks {
			if check.Descriptor.ID == id {
				out.Checks = append(out.Checks, sessions.RequiredCheck{ID: id, CommandDigest: sessions.CheckDigest(check.Descriptor)})
				out.GrantIDs = append(out.GrantIDs, check.GrantID)
				found = true
			}
		}
		if !found {
			return nil, sessions.ErrDenied
		}
	}
	if len(out.Checks) == 0 {
		return nil, sessions.ErrDenied
	}
	return out, nil
}
func readRoutedReviewProfile(p NativeReviewPolicy) (NativeRunnerProfile, *nativeVerificationRouting, error) {
	raw, e := nativeconfig.ReadPrivate(p.RunnerProfile, 65536)
	if e != nil {
		return NativeRunnerProfile{}, nil, sessions.ErrDenied
	}
	digest := sha256.Sum256(raw)
	if hex.EncodeToString(digest[:]) != p.RunnerProfileDigest {
		return NativeRunnerProfile{}, nil, sessions.ErrDenied
	}
	var runner NativeRunnerProfile
	if nativeconfig.Decode(raw, &runner) != nil || runner.SchemaVersion != 2 || runner.WorkspaceID != p.WorkspaceID || runner.TaskID != p.TaskID {
		return runner, nil, sessions.ErrDenied
	}
	route, e := nativeVerificationRoutingProfile(runner)
	if e != nil {
		return runner, nil, e
	}
	checks, e := NativeProfileCheckDescriptors(runner, true)
	if e != nil || len(checks) != len(p.Checks) {
		return runner, nil, sessions.ErrDenied
	}
	for _, check := range checks {
		found := false
		for _, configured := range p.Checks {
			if configured.ID == check.Descriptor.ID && configured.CommandDigest == sessions.CheckDigest(check.Descriptor) {
				found = true
			}
		}
		if !found {
			return runner, nil, sessions.ErrDenied
		}
	}
	return runner, route, nil
}
func nativeReviewRoutingRevision(p NativeReviewPolicy) (string, error) {
	if p.SchemaVersion == 1 {
		return p.Revision, nil
	}
	_, r, e := readRoutedReviewProfile(p)
	if e != nil {
		return "", e
	}
	return safeDigest(struct{ Revision, Profile, Policy, Raw, Base string }{p.Revision, p.RunnerProfileDigest, r.digest, r.bytesDigest, r.base}), nil
}

// NativeRoutedReviewPolicyFromProfile prepares private schema2 owner input;
// only explicit owner handlers may materialize it, never a worker request.
func NativeRoutedReviewPolicyFromProfile(workspace, project, task, revision, path string) (NativeReviewPolicy, error) {
	raw, e := nativeconfig.ReadPrivate(path, 65536)
	if e != nil {
		return NativeReviewPolicy{}, sessions.ErrDenied
	}
	var runner NativeRunnerProfile
	if nativeconfig.Decode(raw, &runner) != nil || runner.SchemaVersion != 2 || runner.WorkspaceID != workspace || runner.TaskID != task {
		return NativeReviewPolicy{}, sessions.ErrDenied
	}
	checks, e := NativeProfileCheckDescriptors(runner, true)
	if e != nil {
		return NativeReviewPolicy{}, e
	}
	digest := sha256.Sum256(raw)
	p := NativeReviewPolicy{SchemaVersion: 2, WorkspaceID: workspace, ProjectID: project, TaskID: task, Revision: revision, RunnerProfile: path, RunnerProfileDigest: hex.EncodeToString(digest[:])}
	for _, c := range checks {
		p.Checks = append(p.Checks, sessions.RequiredCheck{ID: c.Descriptor.ID, CommandDigest: sessions.CheckDigest(c.Descriptor)})
	}
	sort.Slice(p.Checks, func(i, j int) bool { return p.Checks[i].ID < p.Checks[j].ID })
	if validateNativeReviewPolicy(p) != nil {
		return NativeReviewPolicy{}, sessions.ErrDenied
	}
	return p, nil
}

// ObserveRequiredVerification rereads explicit private policy/profile pins at
// the exact revision. Schema1 keeps its original static grant contract.
func (o *NativeReviewObserver) ObserveRequiredVerification(workspace, project, task string, rev sessions.AttemptRevision) (*sessions.RequiredVerificationSet, error) {
	if o == nil || o.workspace.ID != workspace {
		return nil, sessions.ErrDenied
	}
	var policy NativeReviewPolicy
	switch p := o.policy.(type) {
	case NativeReviewPolicy:
		policy = p
	case privateNativeReviewPolicy:
		if verificationPrivateJSON(p.path, &policy) != nil {
			return nil, sessions.ErrDenied
		}
	default:
		return nil, nil
	}
	if validateNativeReviewPolicy(policy) != nil || policy.WorkspaceID != workspace || policy.ProjectID != project || policy.TaskID != task {
		return nil, sessions.ErrDenied
	}
	if policy.SchemaVersion == 1 {
		return nil, nil
	}
	runner, r, e := readRoutedReviewProfile(policy)
	if e != nil {
		return nil, e
	}
	if runner.Repository != rev.Observation.Repository {
		return nil, sessions.ErrDenied
	}
	return requiredRoutingSet(r, runner, rev)
}
