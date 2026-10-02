package application

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/store"
)

type ownerReviewPolicy struct {
	revision string
	checks   []sessions.RequiredCheck
	err      error
}

func (p *ownerReviewPolicy) ReviewPolicy(string, string, string) (string, []sessions.RequiredCheck, error) {
	return p.revision, p.checks, p.err
}
func TestNativeReviewActualRunnerAndCanonicalChecklist(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	f := nativeSetup(t, "printf checked > '"+marker+"'; printf verification-passed")
	task, err := store.FindTask(f.workspace, f.attempt.Request.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	task.Doc.SetSection("Acceptance", "- [ ] Named verification passes")
	if err = store.SaveTask(task); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(task.Path)
	if err != nil {
		t.Fatal(err)
	}
	policy := &ownerReviewPolicy{revision: "owner-policy-v1", checks: []sessions.RequiredCheck{{ID: f.check.ID, CommandDigest: sessions.CheckDigest(f.check)}}}
	observer, err := NewNativeReviewObserver(f.workspace, policy)
	if err != nil {
		t.Fatal(err)
	}
	a, err := sessions.OpenReviewAuthority(f.path, sessions.Binding{Workspace: f.workspace.ID, Installation: "installation"}, observer)
	if err != nil {
		t.Fatal(err)
	}
	reviewer, token, err := a.Enroll(f.owner, sessions.Scope{Actions: []string{"review.propose", "review.evaluate"}, Resources: []string{"task:" + task.ID}}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	grant, err := a.GrantReview(f.owner, sessions.ReviewGrantRequest{ReviewerSession: reviewer.ID, WorkerSession: f.worker.ID, TaskID: task.ID, Repository: f.attempt.Observation.Repository, ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	ev, err := f.executor.Run(context.Background(), f.workerToken, f.request)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := os.ReadFile(marker); err != nil || string(out) != "checked" {
		t.Fatalf("named check did not actually execute: %q %v", out, err)
	}
	req := sessions.ReviewRequest{OperationKey: "review", GrantID: grant.ID, AttemptID: f.attempt.ID, LeaseID: f.lease.ID, RevisionID: f.revision.ID, ExpectedFence: f.lease.Fence, Outcome: "approve", Rationale: "Exact committed revision passed named check", EvidenceIDs: []string{ev.ID}}
	review, err := a.ProposeReview(token, reviewer.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	e, err := a.EvaluateReview(token, reviewer.ID, review.ID)
	if err != nil || !e.Eligible {
		t.Fatalf("independent review %+v %v", e, err)
	}
	after, err := os.ReadFile(task.Path)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("review changed canonical task")
	}
	runs, err := os.ReadDir(f.workspace.RunsDir())
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Fatal("review unexpectedly created process evidence")
	}
	t.Run("fresh-policy", func(t *testing.T) {
		policy.revision = "owner-policy-v2"
		e, err := a.EvaluateReview(token, reviewer.ID, review.ID)
		if err != nil || e.Eligible || e.Reason != sessions.ReviewPolicyChanged {
			t.Fatalf("policy %+v %v", e, err)
		}
		policy.revision = "owner-policy-v1"
	})
	t.Run("fresh-checklist", func(t *testing.T) {
		task.Doc.SetSection("Acceptance", "- [ ] Changed acceptance criterion")
		if err := store.SaveTask(task); err != nil {
			t.Fatal(err)
		}
		e, err := a.EvaluateReview(token, reviewer.ID, review.ID)
		if err != nil || e.Eligible || e.Reason != sessions.ReviewPolicyChanged {
			t.Fatalf("checklist %+v %v", e, err)
		}
	})
	t.Run("new-revision", func(t *testing.T) {
		task.Doc.SetSection("Acceptance", "- [ ] Named verification passes")
		if err := store.SaveTask(task); err != nil {
			t.Fatal(err)
		}
		nativeGit(t, f.attempt.Observation.Locator, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "next revision")
		_, err := a.PublishAttemptRevision(f.workerToken, f.worker.ID, sessions.PublishRevisionRequest{OperationKey: "publish-next", AttemptID: f.attempt.ID, LeaseID: f.lease.ID, ExpectedFence: f.lease.Fence, ExpectedRevision: 1})
		if err != nil {
			t.Fatal(err)
		}
		e, err := a.EvaluateReview(token, reviewer.ID, review.ID)
		if err != nil || e.Eligible || e.Reason != sessions.ReviewRevisionStale {
			t.Fatalf("revision %+v %v", e, err)
		}
	})
}
func TestNativeReviewObserverRefusesMissingWrongAndBrokenTask(t *testing.T) {
	f := nativeSetup(t, "printf ok")
	policy := &ownerReviewPolicy{revision: "p", checks: []sessions.RequiredCheck{{ID: f.check.ID, CommandDigest: sessions.CheckDigest(f.check)}}}
	observer, err := NewNativeReviewObserver(f.workspace, policy)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][3]string{{"wrong", "p", f.attempt.Request.TaskID}, {f.workspace.ID, "wrong", f.attempt.Request.TaskID}, {f.workspace.ID, "p", "missing"}, {f.workspace.ID, "p", f.attempt.Request.TaskID}} {
		if _, err := observer.ObserveReview(args[0], args[1], args[2]); !errors.Is(err, sessions.ErrDenied) {
			t.Fatalf("unchecked task accepted: %v", err)
		}
	}
	if _, err := NewNativeReviewObserver(f.workspace, nil); !errors.Is(err, sessions.ErrDenied) {
		t.Fatal("missing policy accepted")
	}
	task, err := store.FindTask(f.workspace, f.attempt.Request.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	task.Doc.SetSection("Acceptance", "- [ ] test")
	if err = store.SaveTask(task); err != nil {
		t.Fatal(err)
	}
	policy.err = errors.New("unavailable")
	if _, err = observer.ObserveReview(f.workspace.ID, "p", task.ID); err == nil {
		t.Fatal("unobservable policy accepted")
	}
}
