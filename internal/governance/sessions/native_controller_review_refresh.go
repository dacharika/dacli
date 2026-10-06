package sessions

// Checklist progress can change after independent checks. Only an explicit owner
// operation may refresh that observation; it cannot change the actors or checks.
import (
	"reflect"
	"time"
)

type ControllerReviewRefreshRequest struct {
	OperationKey, ProjectID, InvocationID, TaskID                   string
	ExpectedSequence                                                uint64
	ExpectedGrantID, ReviewerSessionID, WorkerSessionID, Repository string
	AttemptID, RevisionID, LeaseID                                  string
	ExpectedFence                                                   uint64
	ExpectedChecklistDigest                                         string
}

type ControllerReviewRefreshResult struct {
	PreviousGrantID, GrantID string
	Status                   ControllerStatus
}

func (a *Authority) RefreshControllerReview(owner string, r ControllerReviewRefreshRequest) (ControllerReviewRefreshResult, error) {
	var out ControllerReviewRefreshResult
	err := a.transaction(false, true, func(s *state) error {
		if s.Version < 16 || len(owner) != 64 || hash(owner) != s.OwnerHash || !validID(r.OperationKey) || !validDigest(r.ExpectedChecklistDigest) {
			return ErrDenied
		}
		digest := identity("controller.review-refresh", "", r)
		grantID := identity("controller.review-refresh-grant", "", r)
		if old, ok := s.ControllerOperations[r.OperationKey]; ok {
			if old.Digest != digest {
				return ErrDenied
			}
			g, ok := s.ReviewGrants[grantID]
			if !ok || g.TaskID != r.TaskID || g.ReviewerSession != r.ReviewerSessionID {
				return ErrDenied
			}
			out = ControllerReviewRefreshResult{r.ExpectedGrantID, grantID, old.Status}
			out.Status.Historical = true
			out.Status.HostActionAuthorized = false
			return nil
		}
		iv, ok := s.ControllerInvocations[r.InvocationID]
		if !ok || iv.Status.ProjectID != r.ProjectID || iv.Status.Sequence != r.ExpectedSequence || iv.Status.Terminal || controllerCurrent(s, iv, time.Now()) != nil {
			return ErrDenied
		}
		route, ok := iv.Routes[r.TaskID]
		if !ok || route.ReviewGrantID != r.ExpectedGrantID || route.ReviewerSession != r.ReviewerSessionID || route.WorkerSession != r.WorkerSessionID {
			return ErrDenied
		}
		if _, exists := s.TaskTerminals[r.TaskID]; exists {
			return ErrDenied
		}
		observer, ok := a.admission.(SwarmAdmission)
		if !ok {
			return ErrDenied
		}
		canonicalOpen := func() bool {
			snapshot, err := observer.ObserveSwarm(s.Binding.Workspace, r.ProjectID)
			if err != nil {
				return false
			}
			matches := 0
			for _, candidate := range snapshot.Candidates {
				if candidate.TaskID == r.TaskID {
					matches++
					if candidate.ProjectID != r.ProjectID || (candidate.Status != "open" && candidate.Status != "active") {
						return false
					}
				}
			}
			return matches == 1
		}
		if !canonicalOpen() {
			return ErrDenied
		}
		var action ControllerAction
		matches := 0
		for _, candidate := range iv.Status.Actions {
			if candidate.TaskID == r.TaskID {
				action = candidate
				matches++
			}
		}
		if matches != 1 || action.Kind != "independent_review" || action.ActorID != route.ReviewerSession || action.ReviewID != "" || action.AttemptID != r.AttemptID || action.RevisionID != r.RevisionID || action.LeaseID != r.LeaseID || action.Fence != r.ExpectedFence {
			return ErrDenied
		}
		dispatch, present := s.Dispatches[action.DispatchID]
		if !present || !dispatch.Converted || dispatch.ProjectID != r.ProjectID || dispatch.Reservation.TaskID != r.TaskID || dispatch.Reservation.Fence != action.ReservationFence || dispatch.WorkerSessionID != route.WorkerSession || dispatch.AttemptID != r.AttemptID || dispatch.Reservation.Repository != r.Repository {
			return ErrDenied
		}
		at, e := reviewAttempt(s, r.AttemptID)
		if e != nil || at.ProjectID != r.ProjectID || at.Request.TaskID != r.TaskID || at.SessionID != route.WorkerSession || at.Observation.Repository != r.Repository {
			return ErrDenied
		}
		old, ok := s.ReviewGrants[r.ExpectedGrantID]
		if !ok || !reviewGrantCurrent(s, route.ReviewerSession, at, old) {
			return ErrDenied
		}
		req := ReviewRequest{GrantID: old.ID, AttemptID: r.AttemptID, RevisionID: r.RevisionID, LeaseID: r.LeaseID, ExpectedFence: r.ExpectedFence}
		if reviewWorker(s, at, req) != nil {
			return ErrDenied
		}
		rev, e := currentRevision(s, at, r.RevisionID, r.LeaseID, r.ExpectedFence)
		if e != nil || observeRevision(at, rev) != nil {
			return ErrDenied
		}
		observation, e := a.observeReview(s.Tasks[r.TaskID])
		observation.RequiredSet = nil
		if e != nil || observation.ChecklistDigest != r.ExpectedChecklistDigest || observation.ChecklistDigest == old.Observation.ChecklistDigest || observation.PolicyRevision != old.Observation.PolicyRevision || !reflect.DeepEqual(observation.Checks, old.Observation.Checks) {
			return ErrDenied
		}
		checks := observation.Checks
		if action.RequiredSet != nil {
			if action.RequiredSet.RevisionID != rev.ID || action.RequiredSet.RevisionCommit != rev.Observation.Commit || action.RequiredSet.RevisionTree != rev.Observation.Tree {
				return ErrDenied
			}
			checks = action.RequiredSet.Checks
			for _, c := range checks {
				found := false
				for _, allowed := range observation.Checks {
					if c == allowed {
						found = true
					}
				}
				if !found {
					return ErrDenied
				}
			}
		}
		if !reviewEvidence(s, at, rev, action.EvidenceIDs, checks) {
			return ErrDenied
		}
		// Existing approved proposals cannot be silently superseded by a new grant.
		for _, review := range s.Reviews {
			if review.Request.AttemptID == at.ID && review.Request.RevisionID == rev.ID {
				return ErrDenied
			}
		}
		fresh, e := a.observeReview(s.Tasks[r.TaskID])
		fresh.RequiredSet = nil
		if e != nil || !reflect.DeepEqual(fresh, observation) || !reviewGrantCurrent(s, route.ReviewerSession, at, old) || reviewWorker(s, at, req) != nil || controllerCurrent(s, iv, time.Now()) != nil {
			return ErrDenied
		}
		if !canonicalOpen() || observeRevision(at, rev) != nil || !reviewEvidence(s, at, rev, action.EvidenceIDs, checks) {
			return ErrDenied
		}
		if _, exists := s.ReviewGrants[grantID]; exists {
			return ErrDenied
		}
		replacement := old
		replacement.ID = grantID
		replacement.Observation = observation
		s.ReviewGrants[grantID] = replacement
		// Routes are mutable invocation state; original history, refs and allocations
		// remain untouched. A refresh does not grant a new host execution permit.
		route.ReviewGrantID = grantID
		iv.Routes[r.TaskID] = route
		iv.Status.Sequence++
		iv.Status.ObservedAt = time.Now().UTC()
		iv.Status.HostActionAuthorized = false
		iv.Status.Historical = false
		s.ControllerInvocations[r.InvocationID] = iv
		s.ControllerHistory[r.InvocationID] = append(s.ControllerHistory[r.InvocationID], iv.Status)
		s.ControllerOperations[r.OperationKey] = controllerReceipt{Digest: digest, Status: iv.Status}
		out = ControllerReviewRefreshResult{old.ID, grantID, iv.Status}
		return nil
	})
	return out, err
}
