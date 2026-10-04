package sessions

import (
	"reflect"
	"time"
)

// NativeAcceptanceInput references stored independent producers; it asserts no producer success.
type NativeAcceptanceInput struct {
	SchemaVersion                                                      int
	OperationKey, WorkspaceID, InstallationID, ProjectID, TaskID       string
	ReviewID, AttemptID, LeaseID, RevisionID, Repository, Commit, Tree string
	ExpectedFence                                                      uint64
	ChecklistDigest, PolicyRevision, CandidateDigest                   string
}

// NativeAcceptanceEvidence exposes only safe exact producer digests, never commands or credentials.
type NativeAcceptanceEvidence struct{ ID, RunnerSessionID, CheckID, CommandDigest, OutputDigest string }
type NativeAcceptanceCandidate struct {
	Input                                              NativeAcceptanceInput
	ReviewerSessionID, WorkerSessionID, WorkerFamilyID string
	Evidence                                           []NativeAcceptanceEvidence
}

// NativeAcceptanceRecord is durable task-only retirement. Closing is not canonical success.
type NativeAcceptanceRecord struct {
	OperationKey, InputDigest, State, CanonicalReceiptDigest, CanonicalOutcomeDigest string
	Candidate                                                                        NativeAcceptanceCandidate
	CreatedAt, ClosedAt                                                              time.Time
}
type NativeAcceptanceResult struct {
	Applied, Duplicate   bool
	State, ReceiptDigest string
}

// UpgradeNativeAcceptance explicitly preserves schema13 producer/accounting histories.
func (a *Authority) UpgradeNativeAcceptance(owner string) error {
	return a.transaction(false, true, func(s *state) error {
		if s.Version != 13 || len(owner) != 64 || hash(owner) != s.OwnerHash {
			return ErrDenied
		}
		s.TaskTerminals = map[string]NativeAcceptanceRecord{}
		s.NativeAcceptanceOperations = map[string]NativeAcceptanceRecord{}
		s.Version = 14
		return nil
	})
}
func nativeAcceptanceCandidate(s *state, binding Binding, p Review) (NativeAcceptanceCandidate, error) {
	at, e := reviewAttempt(s, p.Request.AttemptID)
	if e != nil {
		return NativeAcceptanceCandidate{}, e
	}
	b, ok := s.Tasks[at.Request.TaskID]
	if !ok || b.ProjectID != at.ProjectID || b.Repository != at.Observation.Repository {
		return NativeAcceptanceCandidate{}, ErrDenied
	}
	_, root, e := rootSession(s, at.SessionID, time.Now())
	if e != nil {
		return NativeAcceptanceCandidate{}, e
	}
	c := NativeAcceptanceCandidate{Input: NativeAcceptanceInput{SchemaVersion: 1, WorkspaceID: binding.Workspace, InstallationID: binding.Installation, ProjectID: b.ProjectID, TaskID: b.TaskID, ReviewID: p.ID, AttemptID: at.ID, LeaseID: p.Request.LeaseID, RevisionID: p.Revision.ID, Repository: b.Repository, Commit: p.Revision.Observation.Commit, Tree: p.Revision.Observation.Tree, ExpectedFence: p.Request.ExpectedFence, ChecklistDigest: p.Observation.ChecklistDigest, PolicyRevision: p.Observation.PolicyRevision}, ReviewerSessionID: p.IssuerSession, WorkerSessionID: at.SessionID, WorkerFamilyID: root, Evidence: []NativeAcceptanceEvidence{}}
	for _, id := range p.Request.EvidenceIDs {
		ev, ok := s.Evidence[id]
		if !ok {
			return NativeAcceptanceCandidate{}, ErrDenied
		}
		c.Evidence = append(c.Evidence, NativeAcceptanceEvidence{id, ev.IssuerSession, ev.CheckID, ev.CommandDigest, ev.OutputDigest})
	}
	c.Input.CandidateDigest = identity("native-acceptance-candidate", "", c)
	return c, nil
}
func (a *Authority) ReadNativeAcceptanceCandidate(token, session, id string) (NativeAcceptanceCandidate, error) {
	var out NativeAcceptanceCandidate
	e := a.transaction(false, false, func(s *state) error {
		r, e := authenticate(s, token, session, time.Now())
		if e != nil {
			return e
		}
		p, ok := s.Reviews[id]
		at, attemptErr := reviewAttempt(s, p.Request.AttemptID)
		if attemptErr != nil || !ok || p.IssuerSession != session || p.Request.Outcome != "approve" || !reviewOnly(r, at.Request.TaskID) {
			return ErrDenied
		}
		rev, obs, reason := a.reviewCurrent(s, session, p.Request)
		if reason != ReviewEligible || !reflect.DeepEqual(rev, p.Revision) || !reflect.DeepEqual(obs, p.Observation) {
			return ErrDenied
		}
		out, e = nativeAcceptanceCandidate(s, a.binding, p)
		return e
	})
	if e != nil {
		return NativeAcceptanceCandidate{}, e
	}
	return out, nil
}
func nativeAcceptanceOwner(s *state, owner string, r NativeAcceptanceInput, b Binding) error {
	if s.Version < 14 || len(owner) != 64 || hash(owner) != s.OwnerHash || r.SchemaVersion != 1 || !validID(r.OperationKey) || r.WorkspaceID != b.Workspace || r.InstallationID != b.Installation {
		return ErrDenied
	}
	return nil
}
func (a *Authority) acceptanceCurrent(s *state, r NativeAcceptanceInput, closing string) (NativeAcceptanceCandidate, error) {
	p, ok := s.Reviews[r.ReviewID]
	if !ok || p.Request.Outcome != "approve" {
		return NativeAcceptanceCandidate{}, ErrDenied
	}
	rev, obs, reason := a.reviewCurrentExcept(s, p.IssuerSession, p.Request, closing)
	if reason != ReviewEligible || !reflect.DeepEqual(rev, p.Revision) || !reflect.DeepEqual(obs, p.Observation) {
		return NativeAcceptanceCandidate{}, ErrDenied
	}
	c, e := nativeAcceptanceCandidate(s, a.binding, p)
	if e != nil {
		return NativeAcceptanceCandidate{}, e
	}
	expected := c.Input
	expected.OperationKey = r.OperationKey
	if !reflect.DeepEqual(expected, r) {
		return NativeAcceptanceCandidate{}, ErrDenied
	}
	// The final observer may span expiry of any independent producer grant.
	at, e := reviewAttempt(s, p.Request.AttemptID)
	if e != nil || !reviewEvidence(s, at, rev, p.Request.EvidenceIDs, obs.Checks) || !reviewGrantCurrent(s, p.IssuerSession, at, s.ReviewGrants[p.Request.GrantID]) || reviewWorkerExcept(s, at, p.Request, closing) != nil {
		return NativeAcceptanceCandidate{}, ErrDenied
	}
	return c, nil
}

// BeginNativeAcceptance persists retirement before any canonical side effect.
func (a *Authority) BeginNativeAcceptance(owner string, r NativeAcceptanceInput, prepare func(NativeAcceptanceRecord) (string, error)) (NativeAcceptanceRecord, error) {
	var out NativeAcceptanceRecord
	e := a.transaction(false, true, func(s *state) error {
		if e := nativeAcceptanceOwner(s, owner, r, a.binding); e != nil {
			return e
		}
		digest := identity("native-acceptance-operation", "", r)
		if old, ok := s.NativeAcceptanceOperations[r.OperationKey]; ok {
			if old.InputDigest != digest {
				return ErrDenied
			}
			out = old
			return nil
		}
		if _, ok := s.TaskTerminals[r.TaskID]; ok {
			return ErrDenied
		}
		c, e := a.acceptanceCurrent(s, r, "")
		if e != nil {
			return e
		}
		out = NativeAcceptanceRecord{OperationKey: r.OperationKey, InputDigest: digest, State: "closing", Candidate: c, CreatedAt: time.Now().UTC()}
		out.CanonicalReceiptDigest = identity("native-acceptance-receipt", "", out)
		if prepare == nil {
			return ErrDenied
		}
		out.CanonicalOutcomeDigest, e = prepare(out)
		if e != nil || !validDigest(out.CanonicalOutcomeDigest) {
			return ErrDenied
		}
		if _, e = a.acceptanceCurrent(s, r, ""); e != nil {
			return e
		}
		s.TaskTerminals[r.TaskID] = out
		s.NativeAcceptanceOperations[r.OperationKey] = out
		return nil
	})
	if e != nil {
		return NativeAcceptanceRecord{}, e
	}
	return out, nil
}

// ContinueNativeAcceptance holds authority while the trusted application observes and closes
// the already locked canonical task. Exact closed observation repairs failed finalization.
func (a *Authority) ContinueNativeAcceptance(owner string, r NativeAcceptanceInput, inspect func(NativeAcceptanceRecord) (bool, error), closeTask func(NativeAcceptanceRecord) error) (NativeAcceptanceResult, error) {
	var out NativeAcceptanceResult
	e := a.transaction(false, true, func(s *state) error {
		if e := nativeAcceptanceOwner(s, owner, r, a.binding); e != nil {
			return e
		}
		rec, ok := s.NativeAcceptanceOperations[r.OperationKey]
		terminal, tok := s.TaskTerminals[r.TaskID]
		if !ok || !tok || rec.InputDigest != identity("native-acceptance-operation", "", r) || !reflect.DeepEqual(rec, terminal) || inspect == nil || closeTask == nil {
			return ErrDenied
		}
		closed, e := inspect(rec)
		if e != nil {
			return e
		}
		if rec.State == "closed" {
			if !closed {
				return ErrDenied
			}
			out = NativeAcceptanceResult{true, true, "closed", rec.CanonicalReceiptDigest}
			return nil
		}
		if rec.State != "closing" {
			return ErrDenied
		}
		if !closed {
			if _, e = a.acceptanceCurrent(s, r, r.OperationKey); e != nil {
				return e
			}
			closed, e = inspect(rec)
			if e != nil {
				return e
			}
			if closed {
				return ErrReconcile
			}
			p := s.Reviews[r.ReviewID]
			at, e := reviewAttempt(s, r.AttemptID)
			if e != nil || observeRevision(at, p.Revision) != nil {
				return ErrDenied
			}
			if e != nil || reviewWorkerExcept(s, at, p.Request, r.OperationKey) != nil || !reviewGrantCurrent(s, p.IssuerSession, at, s.ReviewGrants[p.Request.GrantID]) || !reviewEvidence(s, at, p.Revision, p.Request.EvidenceIDs, p.Observation.Checks) {
				return ErrDenied
			}
			current, e := currentRevisionExcept(s, at, r.RevisionID, r.LeaseID, r.ExpectedFence, r.OperationKey)
			if e != nil || !reflect.DeepEqual(current, p.Revision) {
				return ErrDenied
			}
			if e = closeTask(rec); e != nil {
				return e
			}
			closed, e = inspect(rec)
			if e != nil || !closed {
				return ErrReconcile
			}
		}
		rec.State = "closed"
		rec.ClosedAt = time.Now().UTC()
		s.TaskTerminals[r.TaskID] = rec
		s.NativeAcceptanceOperations[r.OperationKey] = rec
		out = NativeAcceptanceResult{true, false, "closed", rec.CanonicalReceiptDigest}
		return nil
	})
	if e != nil {
		return NativeAcceptanceResult{}, e
	}
	return out, nil
}
