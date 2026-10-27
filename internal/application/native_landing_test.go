package application

import (
	"context"
	"os"
	"reflect"
	"testing"

	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/store"
)

func nativeLandingSetup(t *testing.T) (*NativeLandingOwner, acceptanceFixture, NativeLandingRequest) {
	t.Helper()
	f := acceptanceSetup(t)
	project, err := store.LoadProject(f.w, "p")
	if err != nil {
		t.Fatal(err)
	}
	project.Doc.Front.Set("github_repo", "owner/repo")
	if err := store.SaveProject(project); err != nil {
		t.Fatal(err)
	}
	if err := store.ConfigureProjectLanding(project, model.LandingPolicy{Mode: model.LandingPR, Base: "main"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveProject(project); err != nil {
		t.Fatal(err)
	}
	if _, err := store.TaskLandingPolicyDigest(f.w, "p"); err != nil {
		t.Fatalf("policy digest preflight: %v", err)
	}
	task, err := store.NativeAcceptanceTask(f.w, f.task.ID, "p")
	if err != nil {
		t.Fatal(err)
	}
	evidence := store.VerificationEvidence{
		Command: "go test ./...", Argv: []string{"go", "test", "./..."}, WorkingDirectory: ".",
		ExitCode: 0, ArtifactHash: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Verifier: "trusted-runner", Branch: "main", CommitSHA: f.revision.Observation.Commit,
		TreeSHA: f.revision.Observation.Tree, Clean: true,
		RuntimeVersions: map[string]string{"go": "1.26"}, ToolVersions: map[string]string{"git": "2.0"},
	}
	if err := store.AppendVerificationEvidence(task, evidence); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveTask(task); err != nil {
		t.Fatal(err)
	}
	owner, err := OpenNativeLandingOwner(f.access.owner.path, "p")
	if err != nil {
		t.Fatal(err)
	}
	owner.observePR = func(_ context.Context, repository string, number int) (nativeLandingPRObservation, error) {
		if repository != "owner/repo" || number != 123 {
			t.Fatalf("observer got unexpected PR selector: %s #%d", repository, number)
		}
		var pr nativeLandingPRObservation
		pr.Number = 123
		pr.URL = "https://github.com/owner/repo/pull/123"
		pr.State = "MERGED"
		pr.HeadRefOID = f.revision.Observation.Commit
		pr.BaseRefName = "main"
		pr.MergeCommit.OID = f.revision.Observation.Commit
		return pr, nil
	}
	request := NativeLandingRequest{
		OperationKey: "landing-op-1", TaskID: f.task.ID, AttemptID: f.at.ID, RevisionID: f.revision.ID,
		GitHubRepository: "owner/repo", PRNumber: 123, ExpectedPRHead: f.revision.Observation.Commit,
		IntegratedBaseCommit: f.at.Observation.Commit, IntegratedCommit: f.revision.Observation.Commit,
		TargetCommit: f.revision.Observation.Commit, TargetTree: f.revision.Observation.Tree,
	}
	return owner, f, request
}

func TestNativeLandingRecordExactPublishedSourceAndReplay(t *testing.T) {
	owner, f, request := nativeLandingSetup(t)
	// Historical source remains valid after its execution lease is released and
	// worker session is revoked.
	if _, err := f.a.ReleaseLease(f.workerToken, f.worker.ID, sessions.ReleaseLeaseRequest{OperationKey: "release-for-landing", AttemptID: f.at.ID, LeaseID: f.lease.ID, ExpectedFence: f.lease.Fence}); err != nil {
		t.Fatal(err)
	}
	if err := f.a.Revoke(f.owner, f.worker.ID); err != nil {
		t.Fatal(err)
	}
	authorityBefore, err := os.ReadFile(f.access.owner.config.Authority)
	if err != nil {
		t.Fatal(err)
	}
	got, err := owner.Record(context.Background(), request)
	if err != nil || got.ID == "" || got.Source.RevisionID != f.revision.ID || got.IntegratedBaseCommit != f.at.Observation.Commit {
		t.Fatalf("record valid landing: %+v %v", got, err)
	}
	task, err := store.NativeAcceptanceTask(f.w, f.task.ID, "p")
	if err != nil {
		t.Fatal(err)
	}
	result, present := store.ReadTaskLanding(context.Background(), f.w, task, "main")
	if !present || result.State != store.LandingLanded {
		t.Fatalf("record not consumed as landed: %+v present=%v", result, present)
	}
	before, err := os.ReadFile(task.Path)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := owner.Record(context.Background(), request)
	if err != nil || !reflect.DeepEqual(got, replayed) {
		t.Fatalf("exact operation replay changed its receipt: equal=%v err=%v", reflect.DeepEqual(got, replayed), err)
	}
	after, err := os.ReadFile(task.Path)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("exact replay rewrote canonical task: %v", err)
	}
	changed := request
	changed.TargetCommit = f.at.Observation.Commit
	if _, err := owner.Record(context.Background(), changed); err == nil {
		t.Fatal("changed request reused existing operation key")
	}
	if authorityAfter, err := os.ReadFile(f.access.owner.config.Authority); err != nil || !reflect.DeepEqual(authorityBefore, authorityAfter) {
		t.Fatalf("read/record changed authority bytes: %v", err)
	}
}

func TestNativeLandingRefusalsDoNotChangeTaskOrOwnerSelection(t *testing.T) {
	for _, kind := range []string{"unmerged", "wrong-head", "wrong-base", "credential-substitution", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			owner, f, request := nativeLandingSetup(t)
			before, err := os.ReadFile(f.task.Path)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "unmerged":
				owner.observePR = func(context.Context, string, int) (nativeLandingPRObservation, error) {
					pr := nativeLandingPRObservation{Number: 123, URL: "https://github.com/owner/repo/pull/123", State: "OPEN", HeadRefOID: request.ExpectedPRHead, BaseRefName: "main"}
					pr.MergeCommit.OID = f.revision.Observation.Commit
					return pr, nil
				}
			case "wrong-head":
				owner.observePR = func(context.Context, string, int) (nativeLandingPRObservation, error) {
					pr := nativeLandingPRObservation{Number: 123, URL: "https://github.com/owner/repo/pull/123", State: "MERGED", HeadRefOID: f.at.Observation.Commit, BaseRefName: "main"}
					pr.MergeCommit.OID = f.revision.Observation.Commit
					return pr, nil
				}
			case "wrong-base":
				owner.observePR = func(context.Context, string, int) (nativeLandingPRObservation, error) {
					pr := nativeLandingPRObservation{Number: 123, URL: "https://github.com/owner/repo/pull/123", State: "MERGED", HeadRefOID: request.ExpectedPRHead, BaseRefName: "other"}
					pr.MergeCommit.OID = f.revision.Observation.Commit
					return pr, nil
				}
			case "credential-substitution":
				owner.observePR = func(context.Context, string, int) (nativeLandingPRObservation, error) {
					if err := os.WriteFile(f.access.owner.config.OwnerCredentialFile, []byte("substituted-owner"), 0600); err != nil {
						t.Fatal(err)
					}
					pr := nativeLandingPRObservation{Number: 123, URL: "https://github.com/owner/repo/pull/123", State: "MERGED", HeadRefOID: request.ExpectedPRHead, BaseRefName: "main"}
					pr.MergeCommit.OID = f.revision.Observation.Commit
					return pr, nil
				}
			case "canceled":
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				if _, err := owner.Record(ctx, request); err == nil {
					t.Fatal("canceled operation accepted")
				}
			}
			if kind != "canceled" {
				if _, err := owner.Record(context.Background(), request); err == nil {
					t.Fatal("invalid landing observation accepted")
				}
			}
			after, err := os.ReadFile(f.task.Path)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("refusal changed canonical task: %v", err)
			}
		})
	}
}

func TestNativeLandingSourcePublicationChangeDuringPRObservationIsDenied(t *testing.T) {
	owner, f, request := nativeLandingSetup(t)
	owner.observePR = func(context.Context, string, int) (nativeLandingPRObservation, error) {
		// A newer publication during the remote observation supersedes the
		// selected historical source before the final source/task CAS.
		nativeGit(t, f.w.Root, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "newer source")
		newer := sessions.PublishRevisionRequest{OperationKey: "publish-newer-source", AttemptID: f.at.ID, LeaseID: f.lease.ID, ExpectedFence: f.lease.Fence, ExpectedRevision: 1}
		if _, err := f.a.PublishAttemptRevision(f.workerToken, f.worker.ID, newer); err != nil {
			t.Fatalf("publish newer source during observation: %v", err)
		}
		pr := nativeLandingPRObservation{Number: 123, URL: "https://github.com/owner/repo/pull/123", State: "MERGED", HeadRefOID: request.ExpectedPRHead, BaseRefName: "main"}
		pr.MergeCommit.OID = f.revision.Observation.Commit
		return pr, nil
	}
	before, err := os.ReadFile(f.task.Path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Record(context.Background(), request); err == nil {
		t.Fatal("superseded source publication accepted")
	}
	after, err := os.ReadFile(f.task.Path)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("owner substitution changed canonical task: %v", err)
	}
}
