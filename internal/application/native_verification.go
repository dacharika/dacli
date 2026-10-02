package application

import (
	"context"
	"time"

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
	if authority == nil || len(checks) == 0 {
		return nil, sessions.ErrDenied
	}
	if _, err := authority.Authenticate(token, session); err != nil {
		return nil, err
	}
	r := &NativeVerificationRunner{authority: authority, token: token, session: session, checks: map[string]NativeCheck{}}
	for _, c := range checks {
		if c.Descriptor.ID == "" || c.Descriptor.Command == "" || c.Descriptor.Deadline <= 0 || c.Descriptor.Deadline > 10*time.Minute || c.GrantID == "" {
			return nil, sessions.ErrDenied
		}
		if _, ok := r.checks[c.Descriptor.ID]; ok {
			return nil, sessions.ErrDenied
		}
		r.checks[c.Descriptor.ID] = c
	}
	return r, nil
}
func (r *NativeVerificationRunner) Run(ctx context.Context, workerToken string, req NativeVerificationRequest) (sessions.Evidence, error) {
	if r == nil || ctx == nil {
		return sessions.Evidence{}, sessions.ErrDenied
	}
	c, ok := r.checks[req.CheckID]
	if !ok {
		return sessions.Evidence{}, sessions.ErrDenied
	}
	request := sessions.VerificationRequest{OperationKey: req.OperationKey, WorkerSession: req.WorkerSession, AttemptID: req.AttemptID, LeaseID: req.LeaseID, RevisionID: req.RevisionID, CheckID: req.CheckID, GrantID: c.GrantID, ExpectedFence: req.ExpectedFence}
	digest := sessions.CheckDigest(c.Descriptor)
	rev, receipt, err := r.authority.BeginVerification(workerToken, r.token, r.session, request, digest)
	if err != nil {
		return sessions.Evidence{}, err
	}
	if receipt != nil {
		return *receipt, nil
	}
	bounded, cancel := context.WithTimeout(ctx, c.Descriptor.Deadline)
	defer cancel()
	ev, output, runErr := store.RunAcceptanceVerificationRevisionContext(bounded, rev.Observation.Locator, r.session, c.Descriptor.Command, rev.Observation.Commit, rev.Observation.Tree)
	if store.IsVerificationPolicyError(runErr) || bounded.Err() != nil {
		return sessions.Evidence{}, sessions.ErrDenied
	}
	if ev.Command != c.Descriptor.Command || ev.Verifier != r.session || ev.ArtifactHash != sessions.OutputDigest(output) {
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
