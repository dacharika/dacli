package sessions

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

func controllerReviewRefreshFixture(t *testing.T) (controllerFixture, ControllerReviewRefreshRequest, ControllerStatus) {
	t.Helper()
	f, st := controllerCompletedWave(t)
	for _, action := range st.Actions {
		route := f.start.Routes[action.TaskID]
		var grant VerificationGrant
		if err := f.a.transaction(false, false, func(s *state) error { grant = s.Grants[action.Verification.GrantID]; return nil }); err != nil {
			t.Fatal(err)
		}
		rev, _, err := f.a.BeginVerification(f.observer.tokens[action.TaskID], f.runnerTokens[route.RunnerSession], route.RunnerSession, action.Verification, grant.CommandDigest)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.a.CompleteVerification(f.observer.tokens[action.TaskID], f.runnerTokens[route.RunnerSession], route.RunnerSession, action.Verification, VerificationResult{Commit: rev.Observation.Commit, Tree: rev.Observation.Tree, Directory: rev.Observation.Locator, CommandDigest: grant.CommandDigest, OutputDigest: OutputDigest([]byte("ok")), Output: []byte("ok"), ExitCode: 0, Clean: true}); err != nil {
			t.Fatal(err)
		}
	}
	st = controllerStep(t, f, st, "refresh-review-descriptors")
	action := st.Actions[0]
	if action.Kind != "independent_review" {
		t.Fatal("fixture did not reach actual independent review", action.Kind)
	}
	route := f.start.Routes[action.TaskID]
	observation := f.observer.observations[action.TaskID].ReviewObservation
	observation.ChecklistDigest = OutputDigest([]byte("checked canonical acceptance"))
	f.a.reviewObserver = &testReviewObserver{observation}
	var at Attempt
	if err := f.a.transaction(false, false, func(s *state) error { var e error; at, e = reviewAttempt(s, action.AttemptID); return e }); err != nil {
		t.Fatal(err)
	}
	request := ControllerReviewRefreshRequest{OperationKey: "owner-refresh-review", ProjectID: st.ProjectID, InvocationID: st.ID, TaskID: action.TaskID, ExpectedSequence: st.Sequence, ExpectedGrantID: route.ReviewGrantID, ReviewerSessionID: route.ReviewerSession, WorkerSessionID: route.WorkerSession, Repository: at.Observation.Repository, AttemptID: action.AttemptID, RevisionID: action.RevisionID, LeaseID: action.LeaseID, ExpectedFence: action.Fence, ExpectedChecklistDigest: observation.ChecklistDigest}
	return f, request, st
}

func TestNativeControllerReviewRefreshExactOwnerAndReplayPreserveProducers(t *testing.T) {
	f, request, beforeStatus := controllerReviewRefreshFixture(t)
	var original state
	if err := f.a.transaction(false, false, func(s *state) error { var e error; original, e = cloneAuthorityState(s); return e }); err != nil {
		t.Fatal(err)
	}
	action := beforeStatus.Actions[0]
	reviewRequest := ReviewRequest{OperationKey: "original-refused-proposal", GrantID: request.ExpectedGrantID, AttemptID: request.AttemptID, LeaseID: request.LeaseID, RevisionID: request.RevisionID, ExpectedFence: request.ExpectedFence, Outcome: "approve", Rationale: "separate reviewer", EvidenceIDs: action.EvidenceIDs}
	if _, err := f.a.ProposeReview(f.reviewerTokens[request.ReviewerSessionID], request.ReviewerSessionID, reviewRequest); !errors.Is(err, ErrDenied) {
		t.Fatal("old observation accepted changed checklist", err)
	}
	out, err := f.a.RefreshControllerReview(f.owner, request)
	if err != nil || out.GrantID == request.ExpectedGrantID || out.PreviousGrantID != request.ExpectedGrantID || out.Status.Sequence != beforeStatus.Sequence+1 || out.Status.HostActionAuthorized || out.Status.Historical {
		t.Fatal("exact owner refresh failed", out, err)
	}
	if err = f.a.transaction(false, false, func(s *state) error {
		if !reflect.DeepEqual(s.ReviewGrants[request.ExpectedGrantID], original.ReviewGrants[request.ExpectedGrantID]) || !reflect.DeepEqual(s.Evidence, original.Evidence) || !reflect.DeepEqual(s.Revisions, original.Revisions) || !reflect.DeepEqual(s.Leases, original.Leases) || !reflect.DeepEqual(s.BudgetAllocations, original.BudgetAllocations) || !reflect.DeepEqual(s.Checkpoints, original.Checkpoints) || !reflect.DeepEqual(s.ControllerInvocations[request.InvocationID].Policy, original.ControllerInvocations[request.InvocationID].Policy) || !reflect.DeepEqual(out.Status.Actions, beforeStatus.Actions) || !reflect.DeepEqual(out.Status.Refs, beforeStatus.Refs) {
			t.Fatal("refresh rewrote immutable producers or bounds")
		}
		if s.ControllerInvocations[request.InvocationID].Routes[request.TaskID].ReviewGrantID != out.GrantID || len(s.ControllerHistory[request.InvocationID]) != len(original.ControllerHistory[request.InvocationID])+1 || len(s.ReviewGrants) != len(original.ReviewGrants)+1 {
			t.Fatal("refresh did not persist exact route and history")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(f.a.path)
	replay, err := f.a.RefreshControllerReview(f.owner, request)
	after, _ := os.ReadFile(f.a.path)
	if err != nil || replay.GrantID != out.GrantID || !replay.Status.Historical || replay.Status.HostActionAuthorized || !reflect.DeepEqual(before, after) {
		t.Fatal("refresh replay reissued grant or advanced controller", replay, err)
	}
	reviewRequest.OperationKey = "fresh-proposal"
	reviewRequest.GrantID = out.GrantID
	if _, err = f.a.ProposeReview(f.reviewerTokens[request.ReviewerSessionID], request.ReviewerSessionID, reviewRequest); err != nil {
		t.Fatal("fresh grant cannot propose against same original evidence", err)
	}
}

func TestNativeControllerReviewRefreshRefusesChangedBindingsAtomically(t *testing.T) {
	for _, name := range []string{"owner", "sequence", "grant", "reviewer", "worker", "repository", "attempt", "revision", "lease", "fence", "digest", "policy", "checks", "unchanged-checklist", "closed", "terminal", "revoked-grant", "failed-evidence", "wrong-action"} {
		t.Run(name, func(t *testing.T) {
			f, request, _ := controllerReviewRefreshFixture(t)
			owner := f.owner
			switch name {
			case "owner":
				owner = strings.Repeat("0", 64)
			case "sequence":
				request.ExpectedSequence++
			case "grant":
				request.ExpectedGrantID = "wrong-grant"
			case "reviewer":
				request.ReviewerSessionID = f.s.ID
			case "worker":
				request.WorkerSessionID = f.s.ID
			case "repository":
				request.Repository = "/wrong"
			case "attempt":
				request.AttemptID = "wrong-attempt"
			case "revision":
				request.RevisionID = "wrong-revision"
			case "lease":
				request.LeaseID = "wrong-lease"
			case "fence":
				request.ExpectedFence++
			case "digest":
				request.ExpectedChecklistDigest = OutputDigest([]byte("wrong"))
			case "policy":
				f.a.reviewObserver.(*testReviewObserver).observation.PolicyRevision = "different-policy"
			case "checks":
				f.a.reviewObserver.(*testReviewObserver).observation.Checks[0].CommandDigest = strings.Repeat("0", 64)
			case "unchanged-checklist":
				f.a.reviewObserver.(*testReviewObserver).observation.ChecklistDigest = f.observer.observations[request.TaskID].ReviewObservation.ChecklistDigest
			case "closed":
				for i := range f.observer.snapshot.Candidates {
					if f.observer.snapshot.Candidates[i].TaskID == request.TaskID {
						f.observer.snapshot.Candidates[i].Status = "done"
					}
				}
			default:
				if err := f.a.transaction(false, true, func(s *state) error {
					iv := s.ControllerInvocations[request.InvocationID]
					switch name {
					case "terminal":
						iv.Status.Terminal = true
					case "wrong-action":
						iv.Status.Actions[0].Kind = "integration_checkpoint"
					case "revoked-grant":
						grant := s.ReviewGrants[request.ExpectedGrantID]
						grant.Revoked = true
						s.ReviewGrants[grant.ID] = grant
					case "failed-evidence":
						evidence := s.Evidence[iv.Status.Actions[0].EvidenceIDs[0]]
						evidence.ExitCode = 1
						s.Evidence[evidence.ID] = evidence
					}
					s.ControllerInvocations[request.InvocationID] = iv
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := os.ReadFile(f.a.path)
			_, err := f.a.RefreshControllerReview(owner, request)
			after, _ := os.ReadFile(f.a.path)
			if !errors.Is(err, ErrDenied) || !reflect.DeepEqual(before, after) {
				t.Fatal("unsafe refresh mutated authority or was not policy refusal", err)
			}
		})
	}
}
