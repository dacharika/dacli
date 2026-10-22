package application

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"

	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/store"
)

// NativeVerificationRunner is constructed only by the trusted application
// owner. Credentials and configured commands are private and never returned.
// It grants cooperative local execution provenance, not cryptographic host proof.
type NativeVerificationRunner struct {
	authority      *sessions.Authority
	token, session string
	checks         map[string]NativeCheck
	repository     string
	routing        *nativeVerificationRouting
}

// String prevents formatted executor values from revealing private credentials.
func (NativeVerificationRunner) String() string { return "NativeVerificationRunner" }

// GoString redacts private credentials from Go-syntax formatting.
func (NativeVerificationRunner) GoString() string { return "NativeVerificationRunner" }

type NativeCheck struct {
	Descriptor sessions.CheckDescriptor
	GrantID    string
}
type NativeVerificationRequest struct {
	OperationKey, WorkerSession, AttemptID, LeaseID, RevisionID, CheckID string
	ExpectedFence                                                        uint64
}

func NewNativeVerificationRunner(authority *sessions.Authority, token, session string, checks []NativeCheck) (*NativeVerificationRunner, error) {
	return newNativeVerificationRunner(authority, token, session, checks, nil)
}
func newNativeVerificationRunner(authority *sessions.Authority, token, session string, checks []NativeCheck, routing *nativeVerificationRouting) (*NativeVerificationRunner, error) {
	if authority == nil || len(checks) == 0 {
		return nil, sessions.ErrDenied
	}
	if _, err := authority.Authenticate(token, session); err != nil {
		return nil, err
	}
	r := &NativeVerificationRunner{authority: authority, token: token, session: session, checks: map[string]NativeCheck{}, routing: routing}
	for _, c := range checks {
		if !sessions.ValidCheckDescriptor(c.Descriptor) || c.GrantID == "" || (c.Descriptor.Version == 2 && (routing == nil || !routing.matches(c.Descriptor))) {
			return nil, sessions.ErrDenied
		}
		if _, ok := r.checks[c.Descriptor.ID]; ok {
			return nil, sessions.ErrDenied
		}
		c.Descriptor.Argv = append([]string(nil), c.Descriptor.Argv...)
		r.checks[c.Descriptor.ID] = c
	}
	return r, nil
}
func (r *NativeVerificationRunner) Run(ctx context.Context, workerToken string, req NativeVerificationRequest) (sessions.Evidence, error) {
	if ctx == nil {
		return sessions.Evidence{}, sessions.ErrDenied
	}
	prepared, receipt, e := r.prepareContext(ctx, workerToken, req)
	if e != nil {
		return sessions.Evidence{}, e
	}
	if receipt != nil {
		return *receipt, nil
	}
	return prepared.Execute(ctx)
}

// Prepare reserves exactly one durable operation before asynchronous execution.
func (r *NativeVerificationRunner) Prepare(workerToken string, req NativeVerificationRequest) (*nativePreparedVerification, *sessions.Evidence, error) {
	return r.prepareContext(context.Background(), workerToken, req)
}
func (r *NativeVerificationRunner) prepareContext(ctx context.Context, workerToken string, req NativeVerificationRequest) (*nativePreparedVerification, *sessions.Evidence, error) {
	if r == nil || ctx == nil || ctx.Err() != nil || !ValidNativeVerificationRequest(req) {
		return nil, nil, sessions.ErrDenied
	}
	c, ok := r.checks[req.CheckID]
	if !ok {
		return nil, nil, sessions.ErrDenied
	}
	request := sessions.VerificationRequest{OperationKey: req.OperationKey, WorkerSession: req.WorkerSession, AttemptID: req.AttemptID, LeaseID: req.LeaseID, RevisionID: req.RevisionID, CheckID: req.CheckID, GrantID: c.GrantID, ExpectedFence: req.ExpectedFence}
	digest := sessions.CheckDigest(c.Descriptor)
	if r.routing != nil {
		lookup, e := r.authority.LookupVerification(workerToken, r.token, r.session, request, digest)
		if e != nil {
			return nil, nil, e
		}
		if lookup.State == sessions.VerificationAbsent {
			current, e := r.authority.ObserveVerificationRevision(workerToken, r.token, r.session, request, digest)
			if e != nil {
				return nil, nil, e
			}
			if r.repository != "" && current.Observation.Repository != r.repository {
				return nil, nil, sessions.ErrDenied
			}
			if e = r.routing.selected(ctx, current, req.CheckID); e != nil {
				return nil, nil, e
			}
		}
	}
	rev, receipt, e := r.authority.BeginVerification(workerToken, r.token, r.session, request, digest)
	if e != nil {
		return nil, nil, e
	}
	observed := rev
	if receipt != nil {
		observed = receipt.Revision
	}
	if r.repository != "" && observed.Observation.Repository != r.repository {
		return nil, nil, sessions.ErrDenied
	}
	if receipt != nil {
		return nil, receipt, nil
	}
	return &nativePreparedVerification{runner: r, workerToken: workerToken, request: request, check: c, revision: rev, digest: digest, consumed: &atomic.Bool{}}, nil, nil
}

type nativePreparedVerification struct {
	runner      *NativeVerificationRunner
	workerToken string
	request     sessions.VerificationRequest
	check       NativeCheck
	revision    sessions.AttemptRevision
	digest      string
	consumed    *atomic.Bool
}

func (nativePreparedVerification) String() string   { return "nativePreparedVerification" }
func (nativePreparedVerification) GoString() string { return "nativePreparedVerification" }
func (p *nativePreparedVerification) Execute(ctx context.Context) (sessions.Evidence, error) {
	if p == nil || p.consumed == nil || !p.consumed.CompareAndSwap(false, true) || ctx == nil {
		return sessions.Evidence{}, sessions.ErrDenied
	}
	r, c, rev, request, digest, workerToken := p.runner, p.check, p.revision, p.request, p.digest, p.workerToken
	if ctx.Err() != nil {
		return p.interrupted(ctx.Err(), "not_started")
	}
	// Asynchronous preparation is not a lasting launch permission. Reauthenticate
	// the exact worker, lease, runner grant and revision before the executor.
	current, e := r.authority.ObserveVerificationRevision(workerToken, r.token, r.session, request, digest)
	if e != nil || current.ID != rev.ID || current.Observation != rev.Observation {
		return sessions.Evidence{}, sessions.ErrDenied
	}
	bounded, cancel := context.WithTimeout(ctx, c.Descriptor.Deadline)
	defer cancel()
	var ev store.VerificationEvidence
	var output []byte
	var runErr error
	if c.Descriptor.Version == 2 {
		ev, output, runErr = store.RunAcceptanceVerificationArgvRevisionContext(bounded, rev.Observation.Locator, r.session, c.Descriptor.Argv, c.Descriptor.Cwd, rev.Observation.Commit, rev.Observation.Tree)
	} else {
		ev, output, runErr = store.RunAcceptanceVerificationRevisionContext(bounded, rev.Observation.Locator, r.session, c.Descriptor.Command, rev.Observation.Commit, rev.Observation.Tree)
	}
	if bounded.Err() != nil && ev.NativeTermination.State != "" {
		return p.interrupted(bounded.Err(), ev.NativeTermination.State)
	}
	if store.IsVerificationPolicyError(runErr) || bounded.Err() != nil {
		return sessions.Evidence{}, sessions.ErrDenied
	}
	if ev.Command != c.Descriptor.Command || (c.Descriptor.Version == 2 && !reflect.DeepEqual(ev.Argv, c.Descriptor.Argv)) || ev.Verifier != r.session || ev.ArtifactHash != sessions.OutputDigest(output) {
		return sessions.Evidence{}, sessions.ErrDenied
	}
	if ev.ExitCode == 0 {
		if err := store.ValidateFinalTreeVerification(ev, rev.Observation.Commit, rev.Observation.Tree); err != nil {
			return sessions.Evidence{}, sessions.ErrDenied
		}
	}
	result := sessions.VerificationResult{Commit: ev.CommitSHA, Tree: ev.TreeSHA, Directory: ev.WorkingDirectory, CommandDigest: digest, OutputDigest: ev.ArtifactHash, Output: output, ExitCode: ev.ExitCode, Clean: ev.Clean}
	return r.authority.CompleteVerification(workerToken, r.token, r.session, request, result)
}

// Persist only knowledge supplied by this prepared executor, after owned process
// cleanup or before any launch. An unobserved restart retains Pending (core627).
func (p *nativePreparedVerification) interrupted(cause error, termination string) (sessions.Evidence, error) {
	reason := "cancelled"
	if errors.Is(cause, context.DeadlineExceeded) {
		reason = "deadline"
	} else if !errors.Is(cause, context.Canceled) {
		return sessions.Evidence{}, sessions.ErrDenied
	}
	switch termination {
	case "not_started":
		reason += "_before_start"
	case "owned_group_cleanup":
		reason += "_owned_group_cleanup"
	default:
		return sessions.Evidence{}, sessions.ErrDenied
	}
	if _, err := p.runner.authority.InterruptVerification(p.workerToken, p.runner.token, p.runner.session, p.request, p.digest, reason); err != nil {
		return sessions.Evidence{}, err
	}
	return sessions.Evidence{}, sessions.ErrDenied
}

func (a *NativeVerificationAccess) Run(ctx context.Context, req NativeVerificationRequest) (sessions.Evidence, error) {
	req.WorkerSession = a.Worker.ID
	return a.Runner.Run(ctx, a.workerToken, req)
}
func (a *NativeVerificationAccess) Lookup(req NativeVerificationRequest) (sessions.VerificationLookup, error) {
	req.WorkerSession = a.Worker.ID
	if !ValidNativeVerificationRequest(req) {
		return sessions.VerificationLookup{}, sessions.ErrDenied
	}
	c, ok := a.Runner.checks[req.CheckID]
	if !ok {
		return sessions.VerificationLookup{}, sessions.ErrDenied
	}
	lookup, e := a.Runner.authority.LookupVerification(a.workerToken, a.Runner.token, a.Runner.session, sessions.VerificationRequest{OperationKey: req.OperationKey, WorkerSession: req.WorkerSession, AttemptID: req.AttemptID, LeaseID: req.LeaseID, RevisionID: req.RevisionID, CheckID: req.CheckID, GrantID: c.GrantID, ExpectedFence: req.ExpectedFence}, sessions.CheckDigest(c.Descriptor))
	if e == nil && lookup.Evidence != nil && a.Runner.repository != "" && lookup.Evidence.Revision.Observation.Repository != a.Runner.repository {
		return sessions.VerificationLookup{}, sessions.ErrDenied
	}
	return lookup, e
}
