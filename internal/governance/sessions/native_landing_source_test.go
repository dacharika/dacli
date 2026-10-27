package sessions

import (
	"os"
	"reflect"
	"testing"
)

func TestOwnerReadNativeLandingSourceRequiresLatestPublishedSource(t *testing.T) {
	f := evidenceSetup(t)
	first, err := f.a.OwnerReadNativeLandingSource(f.owner, "project", "task-a", f.at.ID, f.revision.ID)
	if err != nil || first.Attempt.ID != f.at.ID || first.Revision.ID != f.revision.ID || first.Binding.Repository != f.at.Observation.Repository {
		t.Fatalf("owner could not read exact published source: %+v %v", first, err)
	}

	gitTest(t, f.at.Observation.Locator, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "second published source")
	secondRequest := f.publish
	secondRequest.OperationKey = "publish-second"
	secondRequest.ExpectedRevision = 1
	second, err := f.a.PublishAttemptRevision(f.workerToken, f.worker.ID, secondRequest)
	if err != nil || second.Sequence != 2 {
		t.Fatalf("publish newer source: %+v %v", second, err)
	}
	if _, err := f.a.OwnerReadNativeLandingSource(f.owner, "project", "task-a", f.at.ID, f.revision.ID); err != ErrDenied {
		t.Fatalf("older publication remained selectable after a newer revision: %v", err)
	}

	if _, err := f.a.ReleaseLease(f.workerToken, f.worker.ID, ReleaseLeaseRequest{OperationKey: "retire-landing-worker", AttemptID: f.at.ID, LeaseID: f.lease.ID, ExpectedFence: f.lease.Fence}); err != nil {
		t.Fatal("release worker lease", err)
	}
	if err := f.a.Revoke(f.owner, f.worker.ID); err != nil {
		t.Fatal("revoke worker session", err)
	}
	before, err := os.ReadFile(f.a.path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.a.OwnerReadNativeLandingSource(f.owner, "project", "task-a", f.at.ID, second.ID)
	if err != nil || !reflect.DeepEqual(got.Revision, second) {
		t.Fatalf("retired worker's historical published source was lost: %+v %v", got, err)
	}
	called := false
	if err := f.a.WithOwnerNativeLandingSource(f.owner, "project", "task-a", f.at.ID, second.ID, func(source NativeLandingSource) error {
		called = true
		if !reflect.DeepEqual(source.Revision, second) {
			t.Fatalf("final source callback received a different publication: %+v", source.Revision)
		}
		return nil
	}); err != nil || !called {
		t.Fatalf("owner final-source callback did not run: called=%v err=%v", called, err)
	}
	after, err := os.ReadFile(f.a.path)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("historical source reads changed authority bytes: %v", err)
	}
	if _, err := f.a.OwnerReadNativeLandingSource(f.workerToken, "project", "task-a", f.at.ID, second.ID); err != ErrDenied {
		t.Fatalf("ordinary revoked worker authenticated an owner source read: %v", err)
	}
}

func TestWithOwnerNativeLandingSourceRejectsNilCallbackAndChangedSourceSelection(t *testing.T) {
	f := evidenceSetup(t)
	if err := f.a.WithOwnerNativeLandingSource(f.owner, "project", "task-a", f.at.ID, f.revision.ID, nil); err != ErrDenied {
		t.Fatalf("nil final-source callback was accepted: %v", err)
	}
	for _, bad := range []struct{ project, task, attempt, revision string }{
		{"other-project", "task-a", f.at.ID, f.revision.ID},
		{"project", "other-task", f.at.ID, f.revision.ID},
		{"project", "task-a", "other-attempt", f.revision.ID},
		{"project", "task-a", f.at.ID, "other-revision"},
	} {
		if _, err := f.a.OwnerReadNativeLandingSource(f.owner, bad.project, bad.task, bad.attempt, bad.revision); err != ErrDenied {
			t.Fatalf("mismatched source selection %+v was accepted: %v", bad, err)
		}
	}
}
