package sessions

import (
	"reflect"
	"sort"
	"time"
	"unicode/utf8"
)

// ReviewObserver is installed by the trusted application owner, never a request.
// It freshly observes canonical Markdown and owner policy without mutating them.
// Neither those files nor checkout bytes are frozen by the authority lock.
type ReviewObserver interface {
	ObserveReview(workspace, project, task string) (ReviewObservation, error)
}
type RequiredCheck struct{ ID, CommandDigest string }
type ReviewObservation struct {
	RequiredSet                     *RequiredVerificationSet `json:",omitempty"`
	ChecklistDigest, PolicyRevision string
	Checks                          []RequiredCheck
}
type ReviewGrantRequest struct {
	ReviewerSession, WorkerSession, TaskID, Repository string
	ExpiresAt                                          time.Time
}
type ReviewGrant struct {
	ID, ReviewerSession, WorkerRoot, TaskID, Repository string
	Observation                                         ReviewObservation
	ExpiresAt                                           time.Time
	Revoked                                             bool
}
type ReviewRequest struct {
	OperationKey, GrantID, AttemptID, LeaseID, RevisionID string
	ExpectedFence                                         uint64
	Outcome, Rationale                                    string
	EvidenceIDs                                           []string
}

// Review is an immutable historical proposal, not acceptance or permission.
type Review struct {
	ID, IssuerSession string
	Request           ReviewRequest
	Revision          AttemptRevision
	Observation       ReviewObservation
	CreatedAt         time.Time
}
type ReviewReason string

const (
	ReviewEligible         ReviewReason = "eligible"
	ReviewChangesRequested ReviewReason = "changes-requested"
	ReviewAuthorityInvalid ReviewReason = "review-authority-invalid"
	ReviewWorkerInvalid    ReviewReason = "worker-authority-invalid"
	ReviewRevisionStale    ReviewReason = "revision-stale-or-dirty"
	ReviewPolicyChanged    ReviewReason = "checklist-or-policy-changed"
	ReviewEvidenceInvalid  ReviewReason = "evidence-ineligible"
)

// ReviewEligibility is a current cooperative observation. Consumers must repeat
// these checks when a future acceptance operation is actually consumed.
type ReviewEligibility struct {
	Eligible  bool
	Reason    ReviewReason
	Assurance string
}

// OpenReviewAuthority selects a trusted read-only observer. Plain Open remains
// usable for sessions/evidence, but cannot grant, propose or evaluate reviews.
func OpenReviewAuthority(path string, binding Binding, observer ReviewObserver, admission ...LeaseAdmission) (*Authority, error) {
	if observer == nil {
		return nil, ErrDenied
	}
	a, err := Open(path, binding, admission...)
	if err != nil {
		return nil, err
	}
	a.reviewObserver = observer
	return a, nil
}
func initializeReviews(s *state) {
	s.ReviewGrants = map[string]ReviewGrant{}
	s.Reviews = map[string]Review{}
}

// UpgradeReviews preserves schema-4 authority and immutable historical receipts.
func (a *Authority) UpgradeReviews(owner string) error {
	old := *a
	old.version = 4
	return old.transaction(false, true, func(s *state) error {
		if len(owner) != 64 || hash(owner) != s.OwnerHash {
			return ErrDenied
		}
		initializeReviews(s)
		initializePublication(s)
		s.Version = 6
		return nil
	})
}
func reviewObservation(o ReviewObservation) (ReviewObservation, error) {
	if !validDigest(o.ChecklistDigest) || !validID(o.PolicyRevision) || len(o.Checks) == 0 || len(o.Checks) > 32 {
		return ReviewObservation{}, ErrDenied
	}
	o.Checks = append([]RequiredCheck(nil), o.Checks...)
	sort.Slice(o.Checks, func(i, j int) bool { return o.Checks[i].ID < o.Checks[j].ID })
	for i, c := range o.Checks {
		if !validID(c.ID) || len(c.CommandDigest) != 64 || !validDigest("sha256:"+c.CommandDigest) || (i > 0 && c.ID == o.Checks[i-1].ID) {
			return ReviewObservation{}, ErrDenied
		}
	}
	return o, nil
}
func (a *Authority) observeReview(b TaskBinding) (ReviewObservation, error) {
	if a.reviewObserver == nil {
		return ReviewObservation{}, ErrDenied
	}
	o, err := a.reviewObserver.ObserveReview(a.binding.Workspace, b.ProjectID, b.TaskID)
	if err != nil {
		return ReviewObservation{}, ErrDenied
	}
	return reviewObservation(o)
}

// rootSession checks every ancestor without needing another actor's credential.
// The caller itself must authenticate separately before consulting these records.
func rootSession(s *state, id string, now time.Time) (record, string, error) {
	r, ok := s.Sessions[id]
	if !ok {
		return record{}, "", ErrDenied
	}
	seen := map[string]bool{}
	for cur := r; ; {
		if seen[cur.ID] || cur.Revoked || !now.Before(cur.ExpiresAt) {
			return record{}, "", ErrDenied
		}
		seen[cur.ID] = true
		if cur.Parent == "" {
			return r, cur.ID, nil
		}
		cur, ok = s.Sessions[cur.Parent]
		if !ok {
			return record{}, "", ErrDenied
		}
	}
}
func reviewOnly(r record, task string) bool {
	if !subset([]string{"review.propose", "review.evaluate"}, r.Scope.Actions) || !subset([]string{"task:" + task}, r.Scope.Resources) {
		return false
	}
	// Fail closed for unknown capabilities as well as today's implementation actions.
	for _, action := range r.Scope.Actions {
		if action != "review.propose" && action != "review.evaluate" && action != "read" {
			return false
		}
	}
	return true
}
func reviewIndependence(s *state, reviewer, worker, task string, now time.Time) (string, error) {
	r, root, err := rootSession(s, reviewer, now)
	if err != nil {
		return "", err
	}
	_, workerRoot, err := rootSession(s, worker, now)
	if err != nil || r.Parent != "" || root == workerRoot || !reviewOnly(r, task) {
		return "", ErrDenied
	}
	// Scope cannot widen through delegation. Also reject historical implementation
	// under this family rather than treating revoked/expired writes as independence.
	for _, at := range s.Attempts {
		if at.Request.TaskID != task {
			continue
		}
		cur, ok := s.Sessions[at.SessionID]
		seen := map[string]bool{}
		for ok && !seen[cur.ID] {
			seen[cur.ID] = true
			if cur.ID == root {
				return "", ErrDenied
			}
			if cur.Parent == "" {
				break
			}
			cur, ok = s.Sessions[cur.Parent]
		}
	}
	return workerRoot, nil
}
func (a *Authority) GrantReview(owner string, req ReviewGrantRequest) (ReviewGrant, error) {
	var result ReviewGrant
	err := a.transaction(false, true, func(s *state) error {
		if len(owner) != 64 || hash(owner) != s.OwnerHash {
			return ErrDenied
		}
		b, ok := s.Tasks[req.TaskID]
		if !ok || b.Repository != req.Repository {
			return ErrDenied
		}
		root, err := reviewIndependence(s, req.ReviewerSession, req.WorkerSession, req.TaskID, time.Now())
		if err != nil {
			return err
		}
		o, err := a.observeReview(b)
		if err != nil {
			return err
		}
		if _, err = reviewIndependence(s, req.ReviewerSession, req.WorkerSession, req.TaskID, time.Now()); err != nil {
			return err
		}
		r := s.Sessions[req.ReviewerSession]
		if !req.ExpiresAt.After(time.Now()) || req.ExpiresAt.After(r.ExpiresAt) {
			return ErrDenied
		}
		id, err := mint()
		if err != nil {
			return err
		}
		result = ReviewGrant{id, req.ReviewerSession, root, req.TaskID, req.Repository, o, req.ExpiresAt.UTC(), false}
		s.ReviewGrants[id] = result
		return nil
	})
	if err != nil {
		return ReviewGrant{}, err
	}
	return result, nil
}
func (a *Authority) RevokeReview(owner, id string) error {
	return a.transaction(false, true, func(s *state) error {
		if len(owner) != 64 || hash(owner) != s.OwnerHash {
			return ErrDenied
		}
		g, ok := s.ReviewGrants[id]
		if !ok {
			return ErrDenied
		}
		g.Revoked = true
		s.ReviewGrants[id] = g
		return nil
	})
}
func reviewAttempt(s *state, id string) (Attempt, error) {
	for _, at := range s.Attempts {
		if at.ID == id {
			return at, nil
		}
	}
	return Attempt{}, ErrDenied
}
func reviewWorker(s *state, at Attempt, req ReviewRequest) error {
	return reviewWorkerExcept(s, at, req, "")
}
func reviewWorkerExcept(s *state, at Attempt, req ReviewRequest, closing string) error {
	r, _, err := rootSession(s, at.SessionID, time.Now())
	if err != nil {
		return err
	}
	b, ok := s.Tasks[at.Request.TaskID]
	l, lok := s.Leases[req.LeaseID]
	if !ok || b.Repository != at.Observation.Repository || b.ProjectID != at.ProjectID || !b.AdmissionEnabled || !subset([]string{"verification.request"}, r.Scope.Actions) || !subset([]string{"task:" + at.Request.TaskID}, r.Scope.Resources) || !lok || l.AttemptID != at.ID || l.SessionID != at.SessionID || l.Fence != req.ExpectedFence || !currentLeaseExcept(s, l, time.Now(), closing) {
		return ErrDenied
	}
	return nil
}
func reviewGrantCurrent(s *state, session string, at Attempt, g ReviewGrant) bool {
	root, err := reviewIndependence(s, session, at.SessionID, at.Request.TaskID, time.Now())
	return err == nil && !g.Revoked && time.Now().Before(g.ExpiresAt) && g.ReviewerSession == session && g.WorkerRoot == root && g.TaskID == at.Request.TaskID && g.Repository == at.Observation.Repository
}
func reviewEvidence(s *state, at Attempt, rev AttemptRevision, ids []string, checks []RequiredCheck) bool {
	if len(ids) != len(checks) {
		return false
	}
	found := map[string]bool{}
	for _, id := range ids {
		ev, ok := s.Evidence[id]
		if !ok || ev.Provenance != "trusted-local-runner" || ev.ExitCode != 0 || !reflect.DeepEqual(ev.Revision, rev) || !validDigest(ev.OutputDigest) {
			return false
		}
		g, ok := s.Grants[ev.GrantID]
		r, root, err := rootSession(s, ev.IssuerSession, time.Now())
		_, workerRoot, werr := rootSession(s, at.SessionID, time.Now())
		if !ok || err != nil || werr != nil || root == workerRoot || r.Parent != "" || g.Revoked || !time.Now().Before(g.ExpiresAt) || g.RunnerSession != r.ID || g.TaskID != at.Request.TaskID || g.Repository != at.Observation.Repository || g.CheckID != ev.CheckID || g.CommandDigest != ev.CommandDigest || !subset([]string{"verification.run"}, r.Scope.Actions) || !subset([]string{"task:" + at.Request.TaskID}, r.Scope.Resources) {
			return false
		}
		match := false
		for _, c := range checks {
			if c.ID == ev.CheckID && c.CommandDigest == ev.CommandDigest && !found[c.ID] {
				found[c.ID] = true
				match = true
				break
			}
		}
		if !match {
			return false
		}
	}
	return len(found) == len(checks)
}
func (a *Authority) reviewCurrent(s *state, session string, req ReviewRequest) (AttemptRevision, ReviewObservation, ReviewReason) {
	return a.reviewCurrentExcept(s, session, req, "")
}
func (a *Authority) reviewCurrentExcept(s *state, session string, req ReviewRequest, closing string) (AttemptRevision, ReviewObservation, ReviewReason) {
	at, err := reviewAttempt(s, req.AttemptID)
	if err != nil {
		return AttemptRevision{}, ReviewObservation{}, ReviewWorkerInvalid
	}
	g, ok := s.ReviewGrants[req.GrantID]
	if !ok || !reviewGrantCurrent(s, session, at, g) {
		return AttemptRevision{}, ReviewObservation{}, ReviewAuthorityInvalid
	}
	if reviewWorkerExcept(s, at, req, closing) != nil {
		return AttemptRevision{}, ReviewObservation{}, ReviewWorkerInvalid
	}
	rev, err := currentRevisionExcept(s, at, req.RevisionID, req.LeaseID, req.ExpectedFence, closing)
	if err != nil {
		return AttemptRevision{}, ReviewObservation{}, ReviewRevisionStale
	}
	obs, err := a.observeReview(s.Tasks[at.Request.TaskID])
	forGrant := obs
	forGrant.RequiredSet = nil
	if err != nil || !reflect.DeepEqual(forGrant, g.Observation) {
		return AttemptRevision{}, ReviewObservation{}, ReviewPolicyChanged
	}
	if err = observeRevision(at, rev); err != nil {
		return AttemptRevision{}, ReviewObservation{}, ReviewRevisionStale
	}
	if observer, ok := a.reviewObserver.(RequiredVerificationObserver); ok {
		required, e := observer.ObserveRequiredVerification(s.Binding.Workspace, at.ProjectID, at.Request.TaskID, rev)
		if e != nil {
			return AttemptRevision{}, ReviewObservation{}, ReviewPolicyChanged
		}
		if required != nil {
			if required.RevisionID != rev.ID || required.RevisionCommit != rev.Observation.Commit || required.RevisionTree != rev.Observation.Tree || required.PlanDigest == "" || len(required.Checks) == 0 {
				return AttemptRevision{}, ReviewObservation{}, ReviewPolicyChanged
			}
			for _, selected := range required.Checks {
				matched := false
				for _, allowed := range obs.Checks {
					if selected == allowed {
						matched = true
					}
				}
				if !matched {
					return AttemptRevision{}, ReviewObservation{}, ReviewPolicyChanged
				}
			}
			obs.RequiredSet = required
			obs.Checks = append([]RequiredCheck(nil), required.Checks...)
		}
	}
	if !reviewEvidence(s, at, rev, req.EvidenceIDs, obs.Checks) {
		return AttemptRevision{}, ReviewObservation{}, ReviewEvidenceInvalid
	}
	// Observer/Git work may span expirations. All authority stays under one lock.
	if !reviewGrantCurrent(s, session, at, g) {
		return AttemptRevision{}, ReviewObservation{}, ReviewAuthorityInvalid
	}
	if reviewWorkerExcept(s, at, req, closing) != nil {
		return AttemptRevision{}, ReviewObservation{}, ReviewWorkerInvalid
	}
	return rev, obs, ReviewEligible
}
func (a *Authority) ProposeReview(token, session string, req ReviewRequest) (Review, error) {
	var result Review
	err := a.transaction(false, true, func(s *state) error {
		r, err := authenticate(s, token, session, time.Now())
		if err != nil {
			return err
		}
		if !validID(req.OperationKey) || (req.Outcome != "approve" && req.Outcome != "request-changes") || len(req.Rationale) > 4096 || !utf8.ValidString(req.Rationale) || len(req.EvidenceIDs) == 0 || len(req.EvidenceIDs) > 32 {
			return ErrDenied
		}
		req.EvidenceIDs = append([]string(nil), req.EvidenceIDs...)
		sort.Strings(req.EvidenceIDs)
		for i, id := range req.EvidenceIDs {
			if !validID(id) || (i > 0 && id == req.EvidenceIDs[i-1]) {
				return ErrDenied
			}
		}
		at, err := reviewAttempt(s, req.AttemptID)
		if err != nil || !reviewOnly(r, at.Request.TaskID) {
			return ErrDenied
		}
		digest := identity("review", session, req)
		if op, ok := s.Operations[req.OperationKey]; ok {
			if op.Digest != digest || op.Review == nil {
				return ErrDenied
			}
			result = *op.Review
			return nil
		}
		rev, obs, reason := a.reviewCurrent(s, session, req)
		if reason != ReviewEligible {
			return ErrDenied
		}
		id, err := mint()
		if err != nil {
			return err
		}
		result = Review{id, session, req, rev, obs, time.Now().UTC()}
		s.Reviews[id] = result
		s.Operations[req.OperationKey] = operation{Digest: digest, Review: &result}
		return nil
	})
	if err != nil {
		return Review{}, err
	}
	return result, nil
}
func (a *Authority) EvaluateReview(token, session, id string) (ReviewEligibility, error) {
	result := ReviewEligibility{Reason: ReviewAuthorityInvalid, Assurance: "cooperative-credential-separation"}
	err := a.transaction(false, false, func(s *state) error {
		r, err := authenticate(s, token, session, time.Now())
		if err != nil {
			return err
		}
		p, ok := s.Reviews[id]
		if !ok || p.IssuerSession != session {
			return ErrDenied
		}
		at, err := reviewAttempt(s, p.Request.AttemptID)
		if err != nil || !reviewOnly(r, at.Request.TaskID) {
			return ErrDenied
		}
		rev, obs, reason := a.reviewCurrent(s, session, p.Request)
		result.Reason = reason
		if reason != ReviewEligible {
			return nil
		}
		if !reflect.DeepEqual(rev, p.Revision) {
			result.Reason = ReviewRevisionStale
			return nil
		}
		if !reflect.DeepEqual(obs, p.Observation) {
			result.Reason = ReviewPolicyChanged
			return nil
		}
		if p.Request.Outcome != "approve" {
			result.Reason = ReviewChangesRequested
			return nil
		}
		result.Eligible = true
		return nil
	})
	return result, err
}
