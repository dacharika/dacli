package sessions

import (
	"errors"
	"math"
	"time"

	"github.com/mlnomadpy/dacli/internal/governance/checkout"
)

// ErrReconcile means a one-shot execution was reserved but has no committed
// outcome. The owner must investigate; retry must never execute it again.
var ErrReconcile = errors.New("verification outcome unknown; owner reconciliation required")

type PublishRevisionRequest struct {
	OperationKey, AttemptID, LeaseID string
	ExpectedFence, ExpectedRevision  uint64
}
type AttemptRevision struct {
	ID, AttemptID, SessionID, LeaseID string
	Sequence, Fence                   uint64
	Observation                       checkout.Observation
	PublishedAt                       time.Time
}

// CheckDescriptor binds both command bytes and deadline, selected by the owner.
type CheckDescriptor struct {
	ID, Command string
	Deadline    time.Duration
}

func CheckDigest(c CheckDescriptor) string { return identity("check", "", c) }

type VerificationGrant struct {
	ID, RunnerSession, TaskID, Repository, CheckID, CommandDigest string
	ExpiresAt                                                     time.Time
	Revoked                                                       bool
}
type EvidenceRequest struct {
	OperationKey, AttemptID, LeaseID, RevisionID, CheckID string
	ExpectedFence                                         uint64
	ExitCode                                              int
	OutputDigest                                          string
}
type Evidence struct {
	ID, IssuerSession, Provenance, GrantID string
	Revision                               AttemptRevision
	CheckID, CommandDigest, OutputDigest   string
	ExitCode                               int
	RecordedAt                             time.Time
}
type VerificationRequest struct {
	OperationKey, WorkerSession, AttemptID, LeaseID, RevisionID, CheckID, GrantID string
	ExpectedFence                                                                 uint64
}

// VerificationResult contains only facts from the trusted configured executor.
// Output is checked and discarded; it is never written to the authority file.
type VerificationResult struct {
	Commit, Tree, Directory, CommandDigest, OutputDigest string
	Output                                               []byte
	ExitCode                                             int
	Clean                                                bool
}

func initializeEvidence(s *state) {
	s.Revisions = map[string][]AttemptRevision{}
	s.Grants = map[string]VerificationGrant{}
	s.Evidence = map[string]Evidence{}
}

// UpgradeEvidence explicitly upgrades schema 3, preserving all prior records,
// operation receipts and the global fence. It initializes the current review
// schema too; old writers refuse the upgraded authority.
func (a *Authority) UpgradeEvidence(owner string) error {
	old := *a
	old.version = 3
	return old.transaction(false, true, func(s *state) error {
		if len(owner) != 64 || hash(owner) != s.OwnerHash {
			return ErrDenied
		}
		initializeEvidence(s)
		initializeReviews(s)
		initializePublication(s)
		s.Version = 6
		return nil
	})
}
func activeAttempt(s *state, token, session, attempt, lease string, fence uint64, action string) (Attempt, error) {
	_, at, err := leaseAuth(s, token, session, attempt, action, time.Now())
	if err != nil {
		return Attempt{}, err
	}
	l, ok := s.Leases[lease]
	if !ok || l.AttemptID != attempt || l.SessionID != session || l.Fence != fence || !currentLease(s, l, time.Now()) {
		return Attempt{}, ErrDenied
	}
	return at, nil
}
func sameCheckout(at Attempt, obs checkout.Observation) bool {
	return obs.Locator == at.Observation.Locator && obs.Repository == at.Observation.Repository
}
func (a *Authority) PublishAttemptRevision(token, session string, req PublishRevisionRequest) (AttemptRevision, error) {
	var result AttemptRevision
	err := a.transaction(false, true, func(s *state) error {
		// Authenticate even historical retries, but never turn a receipt into a lease.
		_, at, err := leaseAuth(s, token, session, req.AttemptID, "attempt.publish", time.Now())
		if err != nil {
			return err
		}
		if !validID(req.OperationKey) {
			return ErrDenied
		}
		digest := identity("revision", session, req)
		if op, ok := s.Operations[req.OperationKey]; ok {
			if op.Digest != digest || op.Revision == nil {
				return ErrDenied
			}
			result = *op.Revision
			return nil
		}
		if _, err = activeAttempt(s, token, session, req.AttemptID, req.LeaseID, req.ExpectedFence, "attempt.publish"); err != nil {
			return err
		}
		history := s.Revisions[at.ID]
		if uint64(len(history)) != req.ExpectedRevision || req.ExpectedRevision == math.MaxUint64 {
			return ErrDenied
		}
		obs, err := checkout.Observe(at.Observation.Locator)
		if err != nil || !sameCheckout(at, obs) {
			return ErrDenied
		}
		if _, err = activeAttempt(s, token, session, req.AttemptID, req.LeaseID, req.ExpectedFence, "attempt.publish"); err != nil {
			return err
		}
		id, err := mint()
		if err != nil {
			return err
		}
		result = AttemptRevision{id, at.ID, session, req.LeaseID, req.ExpectedRevision + 1, req.ExpectedFence, obs, time.Now().UTC()}
		s.Revisions[at.ID] = append(history, result)
		s.Operations[req.OperationKey] = operation{Digest: digest, Revision: &result}
		return nil
	})
	if err != nil {
		return AttemptRevision{}, err
	}
	return result, nil
}
func currentRevision(s *state, at Attempt, id, lease string, fence uint64) (AttemptRevision, error) {
	return currentRevisionExcept(s, at, id, lease, fence, "")
}
func currentRevisionExcept(s *state, at Attempt, id, lease string, fence uint64, closing string) (AttemptRevision, error) {
	if workerTaskWithdrawnExcept(s, at.SessionID, at.Request.TaskID, closing) {
		return AttemptRevision{}, ErrDenied
	}
	history := s.Revisions[at.ID]
	if len(history) == 0 {
		return AttemptRevision{}, ErrDenied
	}
	rev := history[len(history)-1]
	if rev.ID != id || rev.LeaseID != lease || rev.Fence != fence {
		return AttemptRevision{}, ErrDenied
	}
	return rev, nil
}
func observeRevision(at Attempt, rev AttemptRevision) error {
	obs, err := checkout.Observe(at.Observation.Locator)
	if err != nil || !sameCheckout(at, obs) || obs.Commit != rev.Observation.Commit || obs.Tree != rev.Observation.Tree {
		return ErrDenied
	}
	return nil
}

// GrantVerification requires an independently owner-enrolled session. Delegation
// cannot inherit this capability, even if its ordinary scopes match exactly.
func (a *Authority) GrantVerification(owner, runner, task, repository string, check CheckDescriptor, expires time.Time) (VerificationGrant, error) {
	var result VerificationGrant
	err := a.transaction(false, true, func(s *state) error {
		if len(owner) != 64 || hash(owner) != s.OwnerHash {
			return ErrDenied
		}
		r, ok := s.Sessions[runner]
		b, bok := s.Tasks[task]
		if !ok || !bok || r.Parent != "" || r.Revoked || !time.Now().Before(r.ExpiresAt) || !subset([]string{"verification.run"}, r.Scope.Actions) || !subset([]string{"task:" + task}, r.Scope.Resources) || b.Repository != repository || !validID(check.ID) || check.Command == "" || check.Deadline <= 0 || check.Deadline > 10*time.Minute || !expires.After(time.Now()) || expires.After(r.ExpiresAt) {
			return ErrDenied
		}
		id, err := mint()
		if err != nil {
			return err
		}
		result = VerificationGrant{id, runner, task, repository, check.ID, CheckDigest(check), expires.UTC(), false}
		s.Grants[id] = result
		return nil
	})
	return result, err
}
func (a *Authority) RevokeVerification(owner, id string) error {
	return a.transaction(false, true, func(s *state) error {
		if len(owner) != 64 || hash(owner) != s.OwnerHash {
			return ErrDenied
		}
		g, ok := s.Grants[id]
		if !ok {
			return ErrDenied
		}
		g.Revoked = true
		s.Grants[id] = g
		return nil
	})
}
func runnerAuth(s *state, token, session string, req VerificationRequest, at Attempt, descriptor string) (VerificationGrant, error) {
	r, err := authenticate(s, token, session, time.Now())
	if err != nil {
		return VerificationGrant{}, err
	}
	g, ok := s.Grants[req.GrantID]
	if !ok || r.Parent != "" || g.Revoked || !time.Now().Before(g.ExpiresAt) || g.RunnerSession != r.ID || r.ID == req.WorkerSession || g.TaskID != at.Request.TaskID || g.Repository != at.Observation.Repository || g.CheckID != req.CheckID || g.CommandDigest != descriptor || !subset([]string{"verification.run"}, r.Scope.Actions) || !subset([]string{"task:" + g.TaskID}, r.Scope.Resources) {
		return VerificationGrant{}, ErrDenied
	}
	return g, nil
}

// SubmitEvidence always records self-report provenance. No request field can
// assert a runner, grant, provider or trusted status.
func (a *Authority) SubmitEvidence(token, session string, req EvidenceRequest) (Evidence, error) {
	var result Evidence
	err := a.transaction(false, true, func(s *state) error {
		_, at, err := leaseAuth(s, token, session, req.AttemptID, "evidence.submit", time.Now())
		if err != nil {
			return err
		}
		if !validID(req.OperationKey) || !validID(req.CheckID) || !validDigest(req.OutputDigest) {
			return ErrDenied
		}
		digest := identity("self-report", session, req)
		if op, ok := s.Operations[req.OperationKey]; ok {
			if op.Digest != digest || op.Evidence == nil {
				return ErrDenied
			}
			result = *op.Evidence
			return nil
		}
		if _, err = activeAttempt(s, token, session, req.AttemptID, req.LeaseID, req.ExpectedFence, "evidence.submit"); err != nil {
			return err
		}
		rev, err := currentRevision(s, at, req.RevisionID, req.LeaseID, req.ExpectedFence)
		if err != nil {
			return err
		}
		if err = observeRevision(at, rev); err != nil {
			return err
		}
		if _, err = activeAttempt(s, token, session, req.AttemptID, req.LeaseID, req.ExpectedFence, "evidence.submit"); err != nil {
			return err
		}
		id, err := mint()
		if err != nil {
			return err
		}
		result = Evidence{ID: id, IssuerSession: session, Provenance: "self-reported", Revision: rev, CheckID: req.CheckID, OutputDigest: req.OutputDigest, ExitCode: req.ExitCode, RecordedAt: time.Now().UTC()}
		s.Evidence[id] = result
		s.Operations[req.OperationKey] = operation{Digest: digest, Evidence: &result}
		return nil
	})
	if err != nil {
		return Evidence{}, err
	}
	return result, nil
}
func validDigest(s string) bool {
	if len(s) != 71 || s[:7] != "sha256:" {
		return false
	}
	for _, c := range s[7:] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func OutputDigest(output []byte) string { return "sha256:" + hash(string(output)) }

// VerificationOperationState describes durable evidence, not process liveness.
type VerificationOperationState string

const (
	VerificationAbsent    VerificationOperationState = "absent"
	VerificationPending   VerificationOperationState = "pending"
	VerificationCompleted VerificationOperationState = "completed"
)

type VerificationLookup struct {
	State    VerificationOperationState
	Evidence *Evidence
}

// LookupVerification reads an exact operation without reserving or executing it.
// Historical receipts require current worker and runner authority, as Begin's
// replay path does; they do not renew a lease or establish current eligibility.
// Pending means reserved with no committed result, never proof of a live process.
func (a *Authority) LookupVerification(workerToken, runnerToken, runnerSession string, req VerificationRequest, descriptor string) (VerificationLookup, error) {
	var result VerificationLookup
	err := a.transaction(false, false, func(s *state) error {
		_, at, err := leaseAuth(s, workerToken, req.WorkerSession, req.AttemptID, "verification.request", time.Now())
		if err != nil {
			return err
		}
		if !validID(req.OperationKey) {
			return ErrDenied
		}
		if _, err = runnerAuth(s, runnerToken, runnerSession, req, at, descriptor); err != nil {
			return err
		}
		op, ok := s.Operations[req.OperationKey]
		if !ok {
			result.State = VerificationAbsent
			return nil
		}
		if op.Digest != identity("run:"+descriptor, runnerSession, req) {
			return ErrDenied
		}
		if op.Evidence != nil {
			copy := *op.Evidence
			result = VerificationLookup{State: VerificationCompleted, Evidence: &copy}
			return nil
		}
		if !op.Pending {
			return ErrReconcile
		}
		result.State = VerificationPending
		return nil
	})
	if err != nil {
		return VerificationLookup{}, err
	}
	return result, nil
}

// BeginVerification durably reserves an execution before external effects. An
// exact committed retry recovers the receipt; an uncommitted retry refuses.
func (a *Authority) BeginVerification(workerToken, runnerToken, runnerSession string, req VerificationRequest, descriptor string) (AttemptRevision, *Evidence, error) {
	var rev AttemptRevision
	var receipt *Evidence
	err := a.transaction(false, true, func(s *state) error {
		_, at, err := leaseAuth(s, workerToken, req.WorkerSession, req.AttemptID, "verification.request", time.Now())
		if err != nil {
			return err
		}
		if !validID(req.OperationKey) {
			return ErrDenied
		}
		if _, err = runnerAuth(s, runnerToken, runnerSession, req, at, descriptor); err != nil {
			return err
		}
		digest := identity("run:"+descriptor, runnerSession, req)
		if op, ok := s.Operations[req.OperationKey]; ok {
			if op.Digest != digest {
				return ErrDenied
			}
			if op.Evidence != nil {
				copy := *op.Evidence
				receipt = &copy
				return nil
			}
			return ErrReconcile
		}
		if _, err = activeAttempt(s, workerToken, req.WorkerSession, req.AttemptID, req.LeaseID, req.ExpectedFence, "verification.request"); err != nil {
			return err
		}
		rev, err = currentRevision(s, at, req.RevisionID, req.LeaseID, req.ExpectedFence)
		if err != nil {
			return err
		}
		if err = observeRevision(at, rev); err != nil {
			return err
		}
		if _, err = activeAttempt(s, workerToken, req.WorkerSession, req.AttemptID, req.LeaseID, req.ExpectedFence, "verification.request"); err != nil {
			return err
		}
		if _, err = runnerAuth(s, runnerToken, runnerSession, req, at, descriptor); err != nil {
			return err
		}
		op := operation{Digest: digest, Pending: true}
		if s.Version >= 12 {
			op.CheckpointVerification = &CheckpointVerificationBinding{ProjectID: at.ProjectID, TaskID: at.Request.TaskID, RunnerSession: runnerSession, DescriptorDigest: descriptor, Request: req}
		}
		s.Operations[req.OperationKey] = op
		return nil
	})
	return rev, receipt, err
}

// CompleteVerification rechecks all current authority after execution. Holding a
// runner credential is a cooperative issuer capability, not host execution proof.
func (a *Authority) CompleteVerification(workerToken, runnerToken, runnerSession string, req VerificationRequest, res VerificationResult) (Evidence, error) {
	var result Evidence
	err := a.transaction(false, true, func(s *state) error {
		at, err := activeAttempt(s, workerToken, req.WorkerSession, req.AttemptID, req.LeaseID, req.ExpectedFence, "verification.request")
		if err != nil {
			return err
		}
		g, err := runnerAuth(s, runnerToken, runnerSession, req, at, res.CommandDigest)
		if err != nil {
			return err
		}
		digest := identity("run:"+res.CommandDigest, runnerSession, req)
		op, ok := s.Operations[req.OperationKey]
		if !ok || op.Digest != digest {
			return ErrDenied
		}
		if op.Evidence != nil {
			if op.ResultDigest != identity("result", runnerSession, res) {
				return ErrDenied
			}
			result = *op.Evidence
			return nil
		}
		if !op.Pending {
			return ErrDenied
		}
		rev, err := currentRevision(s, at, req.RevisionID, req.LeaseID, req.ExpectedFence)
		if err != nil {
			return err
		}
		if !res.Clean || res.Commit != rev.Observation.Commit || res.Tree != rev.Observation.Tree || res.Directory != rev.Observation.Locator || len(res.Output) > 64<<10 || res.OutputDigest != OutputDigest(res.Output) {
			return ErrDenied
		}
		if err = observeRevision(at, rev); err != nil {
			return err
		}
		if _, err = activeAttempt(s, workerToken, req.WorkerSession, req.AttemptID, req.LeaseID, req.ExpectedFence, "verification.request"); err != nil {
			return err
		}
		if _, err = runnerAuth(s, runnerToken, runnerSession, req, at, res.CommandDigest); err != nil {
			return err
		}
		id, err := mint()
		if err != nil {
			return err
		}
		result = Evidence{ID: id, IssuerSession: runnerSession, Provenance: "trusted-local-runner", GrantID: g.ID, Revision: rev, CheckID: g.CheckID, CommandDigest: g.CommandDigest, OutputDigest: res.OutputDigest, ExitCode: res.ExitCode, RecordedAt: time.Now().UTC()}
		s.Evidence[id] = result
		s.Operations[req.OperationKey] = operation{Digest: digest, Evidence: &result, ResultDigest: identity("result", runnerSession, res), CheckpointVerification: s.Operations[req.OperationKey].CheckpointVerification}
		return nil
	})
	if err != nil {
		return Evidence{}, err
	}
	return result, nil
}

// EligibleVerification is observation only, not review or task acceptance.
func (a *Authority) EligibleVerification(token, session, id string) (bool, error) {
	eligible := false
	err := a.transaction(false, false, func(s *state) error {
		ev, ok := s.Evidence[id]
		if !ok {
			return ErrDenied
		}
		at, err := activeAttempt(s, token, session, ev.Revision.AttemptID, ev.Revision.LeaseID, ev.Revision.Fence, "verification.request")
		if err != nil {
			return err
		}
		if ev.Provenance != "trusted-local-runner" || ev.ExitCode != 0 {
			return nil
		}
		if _, err = currentRevision(s, at, ev.Revision.ID, ev.Revision.LeaseID, ev.Revision.Fence); err != nil {
			return nil //nolint:nilerr // A stale revision is an observed ineligible result.
		}
		g, ok := s.Grants[ev.GrantID]
		r, rok := s.Sessions[ev.IssuerSession]
		if !ok || !rok || g.Revoked || r.Revoked || r.Parent != "" || g.RunnerSession != r.ID || g.TaskID != at.Request.TaskID || g.Repository != at.Observation.Repository || g.CheckID != ev.CheckID || g.CommandDigest != ev.CommandDigest || !subset([]string{"verification.run"}, r.Scope.Actions) || !subset([]string{"task:" + g.TaskID}, r.Scope.Resources) || !time.Now().Before(g.ExpiresAt) || !time.Now().Before(r.ExpiresAt) {
			return nil
		}
		if err = observeRevision(at, ev.Revision); err != nil {
			return nil //nolint:nilerr // A dirty or replaced checkout is an observed ineligible result.
		}
		if _, err = activeAttempt(s, token, session, ev.Revision.AttemptID, ev.Revision.LeaseID, ev.Revision.Fence, "verification.request"); err != nil {
			return err
		}
		if !time.Now().Before(g.ExpiresAt) || !time.Now().Before(r.ExpiresAt) {
			return nil
		}
		eligible = true
		return nil
	})
	return eligible, err
}
