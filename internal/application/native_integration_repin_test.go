package application

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/store"
)

func TestNativeIntegrationRepinRequiresFreshVerificationAndReview(t *testing.T) {
	useCanonicalTestTempParent(t)
	f, originalOwner, originalInput, _ := nativeIntegrationProducerFixture(t)
	if filepath.Clean(f.access.policyPath) == filepath.Clean(originalOwner.policyPath) {
		t.Fatal("review policy and integration policy must be separately selected")
	}
	if f.access.policy.Revision == originalOwner.policy.Revision {
		t.Fatal("review-policy and integration-policy revisions must remain distinct")
	}
	originalPersisted, err := originalOwner.InspectPolicy()
	if err != nil || originalPersisted.ConfiguredAt.IsZero() {
		t.Fatalf("inspect original persisted integration epoch: %+v %v", originalPersisted, err)
	}

	// Repin a new integration-policy epoch to the same exact local target. Keeping
	// the target fixed isolates the ConfiguredAt freshness boundary from ref/CAS
	// failures and from a stale outer policy-revision refusal.
	repinnedOwner, repinned := repinNativeIntegrationOwner(t, f, originalOwner, "integration-repin", originalOwner.policy.ExpectedCommit, originalOwner.policy.ExpectedTree)
	if repinned.Revision == originalOwner.policy.Revision || repinned.TargetCheckout != originalOwner.policy.TargetCheckout || repinned.TargetRef != originalOwner.policy.TargetRef || repinned.ExpectedCommit != originalOwner.policy.ExpectedCommit || repinned.ExpectedTree != originalOwner.policy.ExpectedTree {
		t.Fatal("repin did not preserve the exact source/target pin while changing policy epoch")
	}
	if !repinned.ConfiguredAt.After(originalPersisted.ConfiguredAt) {
		t.Fatal("owner API did not establish a newer integration epoch")
	}

	oldCandidate, err := f.a.ReadNativeAcceptanceCandidate(f.reviewerToken, f.reviewer.ID, originalInput.Candidate.ReviewID)
	if err != nil || len(oldCandidate.Evidence) != 1 {
		t.Fatalf("cannot observe original independent producer pair: %+v %v", oldCandidate, err)
	}
	oldEvidenceID := oldCandidate.Evidence[0].ID

	oldPair := repinIntegrationInput(originalInput, "repin-old-pair", repinned.Revision)
	repinAssertApplyDeniedNoEffects(t, f, repinnedOwner, oldPair, "pre-repin verification and review")

	// A real new trusted check is not retroactively attached to the old review.
	// The immutable old candidate continues to reference its old evidence and must
	// remain ineligible for the new integration epoch.
	newCheckForOldReview := repinRunVerification(t, f, "repin-check-old-review", originalInput.Candidate.RevisionID)
	if newCheckForOldReview.ID == oldEvidenceID || !newCheckForOldReview.RecordedAt.After(repinned.ConfiguredAt) {
		t.Fatal("fresh verification was not a distinct post-repin producer")
	}
	if oldCandidate.Evidence[0].ID != oldEvidenceID {
		t.Fatal("old review candidate was rewritten by the later verification")
	}
	repinAssertApplyDeniedNoEffects(t, f, repinnedOwner, repinIntegrationInput(originalInput, "repin-old-review-new-check", repinned.Revision), "old review despite a later unrelated check")

	// Produce a genuinely fresh, independent review over the still-valid but
	// pre-repin verification. The review is new enough; only the evidence epoch is
	// stale, so Apply must reject in its integration freshness predicate.
	freshReviewOldCheck := repinReviewWithEvidence(t, f, oldEvidenceID, originalInput.Candidate.RevisionID, "repin-review-old-check")
	if !freshReviewOldCheck.review.CreatedAt.After(repinned.ConfiguredAt) || freshReviewOldCheck.candidate.Input.ReviewID == originalInput.Candidate.ReviewID {
		t.Fatal("review producer was not newly and independently issued after repin")
	}
	repinAssertApplyDeniedNoEffects(t, f, repinnedOwner, repinCandidateIntegrationInput(freshReviewOldCheck.candidate, "repin-new-review-old-check", repinned.Revision), "fresh review over pre-repin verification")

	// Both real producers are now issued after the repin for the same immutable
	// committed tree. This positive control must pass admission and perform the
	// exact local integration.
	freshEvidence := repinRunVerification(t, f, "repin-check-fresh-pair", originalInput.Candidate.RevisionID)
	if freshEvidence.ID == oldEvidenceID || !freshEvidence.RecordedAt.After(repinned.ConfiguredAt) {
		t.Fatal("fresh trusted verification did not postdate repin")
	}
	freshPair := repinReviewWithEvidence(t, f, freshEvidence.ID, originalInput.Candidate.RevisionID, "repin-review-fresh-pair")
	if !freshPair.review.CreatedAt.After(repinned.ConfiguredAt) || freshPair.review.Revision.Observation.Commit != originalInput.Candidate.Commit || freshPair.review.Revision.Observation.Tree != originalInput.Candidate.Tree {
		t.Fatal("fresh independent review did not bind the exact original committed tree")
	}
	input := repinCandidateIntegrationInput(freshPair.candidate, "repin-fresh-pair", repinned.Revision)
	result, err := repinnedOwner.Apply(context.Background(), input)
	if err != nil || result.Record.State != "integrated" || result.Record.Outcome.Commit != input.Candidate.Commit || result.Record.Outcome.Tree != input.Candidate.Tree || result.Duplicate {
		t.Fatalf("fresh post-repin producer pair did not integrate exact candidate: %+v %v", result, err)
	}
	observed, err := store.ObserveNativeIntegrationTarget(context.Background(), integrationTarget(repinnedOwner.policy, input.Candidate))
	if err != nil || !observed.Clean || observed.Commit != input.Candidate.Commit || observed.Tree != input.Candidate.Tree {
		t.Fatalf("target does not match fresh integration receipt: %+v %v", observed, err)
	}
}

func TestNativeIntegrationRepinRejectsGenuineDivergentCandidate(t *testing.T) {
	useCanonicalTestTempParent(t)
	f, originalOwner, _, originalBase := nativeIntegrationProducerFixture(t)
	originalPersisted, err := originalOwner.InspectPolicy()
	if err != nil || originalPersisted.ConfiguredAt.IsZero() {
		t.Fatalf("inspect original persisted integration epoch: %+v %v", originalPersisted, err)
	}

	// Advance the actual target checkout/ref with a real commit from the original
	// base. This is a fixture Git effect, followed by a fresh owner pin.
	targetOnly := filepath.Join(originalOwner.policy.TargetCheckout, "target-only.txt")
	if err := os.WriteFile(targetOnly, []byte("target advanced\n"), 0600); err != nil {
		t.Fatal(err)
	}
	nativeGit(t, originalOwner.policy.TargetCheckout, "add", "target-only.txt")
	nativeGit(t, originalOwner.policy.TargetCheckout, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "advance integration target")
	newTargetCommit := nativeAcceptanceGitOutput(t, originalOwner.policy.TargetCheckout, "rev-parse", originalOwner.policy.TargetRef)
	newTargetTree := nativeAcceptanceGitOutput(t, originalOwner.policy.TargetCheckout, "rev-parse", originalOwner.policy.TargetRef+"^{tree}")
	if newTargetCommit == originalBase || newTargetTree == originalOwner.policy.ExpectedTree {
		t.Fatal("fixture failed to create a genuine changed target commit/tree")
	}
	repinnedOwner, repinned := repinNativeIntegrationOwner(t, f, originalOwner, "integration-divergent", newTargetCommit, newTargetTree)
	if !repinned.ConfiguredAt.After(originalPersisted.ConfiguredAt) || repinned.ExpectedCommit != newTargetCommit || repinned.ExpectedTree != newTargetTree {
		t.Fatal("new owner policy did not pin the observed target epoch")
	}

	// Construct a separate genuine candidate commit from the old base. It changes
	// the same source branch independently and is therefore not a descendant of
	// the newly pinned target commit.
	nativeGit(t, f.w.Root, "checkout", "-b", "repin-divergent-worker", originalBase)
	if err := os.WriteFile(filepath.Join(f.w.Root, "src"), []byte("divergent candidate\n"), 0600); err != nil {
		t.Fatal(err)
	}
	nativeGit(t, f.w.Root, "add", "src")
	nativeGit(t, f.w.Root, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "create divergent candidate")
	divergentCommit := nativeAcceptanceGitOutput(t, f.w.Root, "rev-parse", "HEAD")
	if divergentCommit == newTargetCommit {
		t.Fatal("candidate commit unexpectedly equals pinned target")
	}
	mergeBase := exec.Command("git", "merge-base", "--is-ancestor", newTargetCommit, divergentCommit)
	mergeBase.Dir = f.w.Root
	mergeBase.Env = store.VerificationEnvironment()
	if output, err := mergeBase.CombinedOutput(); err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
			t.Fatalf("could not establish genuine divergence (expected merge-base exit 1): %s %v", output, err)
		}
	} else {
		t.Fatalf("candidate unexpectedly descends from target: %s", output)
	}

	revision, err := f.a.PublishAttemptRevision(f.workerToken, f.worker.ID, sessions.PublishRevisionRequest{OperationKey: "repin-divergent-revision", AttemptID: f.at.ID, LeaseID: f.lease.ID, ExpectedFence: f.lease.Fence, ExpectedRevision: 2})
	if err != nil || revision.Observation.Commit != divergentCommit {
		t.Fatalf("failed to publish genuine divergent candidate: %+v %v", revision, err)
	}
	evidence := repinRunVerification(t, f, "repin-divergent-check", revision.ID)
	review := repinReviewWithEvidence(t, f, evidence.ID, revision.ID, "repin-divergent-review")
	input := repinCandidateIntegrationInput(review.candidate, "repin-divergent-apply", repinned.Revision)
	if input.Candidate.Commit != divergentCommit || !evidence.RecordedAt.After(repinned.ConfiguredAt) || !review.review.CreatedAt.After(repinned.ConfiguredAt) {
		t.Fatal("divergent producer inputs were not genuine and fresh under the new policy")
	}

	beforeTarget := repinReadTargetState(t, repinnedOwner.policy, input.Candidate)
	beforeCandidate := repinReadCandidateState(t, f.w.Root)
	result, applyErr := repinnedOwner.Apply(context.Background(), input)
	if !errors.Is(applyErr, sessions.ErrReconcile) || result.Record.OperationKey != "" || result.Record.State != "" {
		t.Fatalf("divergent actual Git producer must remain pending/reconcile, never fabricate success: %+v %v", result, applyErr)
	}
	record, inspectErr := repinnedOwner.Inspect(input.OperationKey)
	if inspectErr != nil || record.State != "pending" || record.Outcome.Commit != "" || record.Outcome.Tree != "" || record.ReceiptDigest != "" {
		t.Fatalf("divergent refusal did not preserve honest pending intent: %+v %v", record, inspectErr)
	}
	afterTarget := repinReadTargetState(t, repinnedOwner.policy, input.Candidate)
	afterCandidate := repinReadCandidateState(t, f.w.Root)
	if !repinSameTargetState(beforeTarget, afterTarget) || !repinSameCandidateState(beforeCandidate, afterCandidate) {
		t.Fatalf("divergent candidate changed source/target despite refusal: beforeTarget=%+v afterTarget=%+v beforeCandidate=%+v afterCandidate=%+v", beforeTarget, afterTarget, beforeCandidate, afterCandidate)
	}
}

type repinReviewResult struct {
	review    sessions.Review
	candidate sessions.NativeAcceptanceCandidate
}

func repinNativeIntegrationOwner(t *testing.T, f acceptanceFixture, original *NativeIntegrationOwner, revision, expectedCommit, expectedTree string) (*NativeIntegrationOwner, sessions.NativeIntegrationPolicy) {
	t.Helper()
	policy := original.policy
	policy.Revision = revision
	policy.ExpectedCommit = expectedCommit
	policy.ExpectedTree = expectedTree
	policy.ConfiguredAt = time.Time{}
	policy.ExpiresAt = time.Now().Add(time.Hour)
	path := filepath.Join(filepath.Dir(original.policyPath), revision+".json")
	acceptanceJSON(t, path, policy)
	owner, err := OpenNativeIntegrationOwner(f.access.owner.path, f.access.policyPath, path)
	if err != nil {
		t.Fatalf("open separately selected integration policy: %v", err)
	}
	if filepath.Clean(owner.policyPath) == filepath.Clean(owner.acceptance.policyPath) {
		t.Fatal("review-policy and integration-policy selections were conflated")
	}
	configured, err := owner.Configure(context.Background())
	if err != nil {
		t.Fatalf("configure integration repin through owner API: %v", err)
	}
	if configured.Revision != revision || configured.ConfiguredAt.IsZero() || configured.ExpectedCommit != expectedCommit || configured.ExpectedTree != expectedTree {
		t.Fatalf("owner API returned unexpected integration epoch: %+v", configured)
	}
	return owner, configured
}

func repinIntegrationInput(input sessions.NativeIntegrationInput, operation, policyRevision string) sessions.NativeIntegrationInput {
	input.OperationKey = operation
	input.PolicyRevision = policyRevision
	input.Candidate.OperationKey = operation
	return input
}

func repinRunVerification(t *testing.T, f acceptanceFixture, operation, revision string) sessions.Evidence {
	t.Helper()
	runner, err := NewNativeVerificationRunner(f.a, f.runnerToken, f.runnerID, []NativeCheck{{Descriptor: f.check, GrantID: f.verificationGrantID}})
	if err != nil {
		t.Fatalf("construct actual trusted verifier: %v", err)
	}
	evidence, err := runner.Run(context.Background(), f.workerToken, NativeVerificationRequest{OperationKey: operation, AttemptID: f.at.ID, LeaseID: f.lease.ID, RevisionID: revision, CheckID: f.check.ID, ExpectedFence: f.lease.Fence, WorkerSession: f.worker.ID})
	if err != nil || evidence.Provenance != "trusted-local-runner" || evidence.ExitCode != 0 || evidence.Revision.ID != revision {
		t.Fatalf("actual trusted verification failed: %+v %v", evidence, err)
	}
	return evidence
}

func repinReviewWithEvidence(t *testing.T, f acceptanceFixture, evidenceID, revision, operation string) repinReviewResult {
	t.Helper()
	reviewer, token, err := f.a.Enroll(f.owner, sessions.Scope{Actions: []string{"review.propose", "review.evaluate"}, Resources: []string{"task:" + f.task.ID}}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("enroll separate independent reviewer: %v", err)
	}
	grant, err := f.a.GrantReview(f.owner, sessions.ReviewGrantRequest{ReviewerSession: reviewer.ID, WorkerSession: f.worker.ID, TaskID: f.task.ID, Repository: f.input.Repository, ExpiresAt: time.Now().Add(time.Hour / 2)})
	if err != nil {
		t.Fatalf("issue separate reviewer grant: %v", err)
	}
	review, err := f.a.ProposeReview(token, reviewer.ID, sessions.ReviewRequest{OperationKey: operation, GrantID: grant.ID, AttemptID: f.at.ID, LeaseID: f.lease.ID, RevisionID: revision, ExpectedFence: f.lease.Fence, Outcome: "approve", Rationale: "repin epoch producer check", EvidenceIDs: []string{evidenceID}})
	if err != nil {
		t.Fatalf("actual independent review refused: %v", err)
	}
	candidate, err := f.a.ReadNativeAcceptanceCandidate(token, reviewer.ID, review.ID)
	if err != nil {
		t.Fatalf("read exact accepted review candidate: %v", err)
	}
	return repinReviewResult{review: review, candidate: candidate}
}

func repinCandidateIntegrationInput(candidate sessions.NativeAcceptanceCandidate, operation, policyRevision string) sessions.NativeIntegrationInput {
	input := candidate.Input
	input.OperationKey = operation
	return sessions.NativeIntegrationInput{OperationKey: operation, PolicyRevision: policyRevision, Candidate: input}
}

func repinAssertApplyDeniedNoEffects(t *testing.T, f acceptanceFixture, owner *NativeIntegrationOwner, input sessions.NativeIntegrationInput, label string) {
	t.Helper()
	before := repinReadTargetState(t, owner.policy, input.Candidate)
	beforeAuthority, err := os.ReadFile(f.access.owner.config.Authority)
	if err != nil {
		t.Fatal(err)
	}
	beforeCandidate := repinReadCandidateState(t, f.w.Root)
	result, applyErr := owner.Apply(context.Background(), input)
	if !errors.Is(applyErr, sessions.ErrDenied) || result.Record.OperationKey != "" || result.Record.State != "" {
		t.Fatalf("%s must be denied before any durable integration record: %+v %v", label, result, applyErr)
	}
	if _, recordErr := f.a.ReadNativeIntegration(f.owner, input.OperationKey); !errors.Is(recordErr, sessions.ErrDenied) {
		t.Fatalf("%s fabricated a durable integration operation: %v", label, recordErr)
	}
	afterAuthority, err := os.ReadFile(f.access.owner.config.Authority)
	if err != nil {
		t.Fatal(err)
	}
	after := repinReadTargetState(t, owner.policy, input.Candidate)
	afterCandidate := repinReadCandidateState(t, f.w.Root)
	if !bytes.Equal(beforeAuthority, afterAuthority) || !repinSameTargetState(before, after) || !repinSameCandidateState(beforeCandidate, afterCandidate) {
		t.Fatalf("%s refusal changed durable authority or Git state: beforeTarget=%+v afterTarget=%+v beforeCandidate=%+v afterCandidate=%+v", label, before, after, beforeCandidate, afterCandidate)
	}
}

type repinTargetState struct {
	observation store.NativeIntegrationGitObservation
	ref         string
	bytes       []byte
}

type repinCandidateState struct {
	branch, commit, tree string
	bytes                []byte
}

func repinReadCandidateState(t *testing.T, root string) repinCandidateState {
	t.Helper()
	branch := nativeAcceptanceGitOutput(t, root, "symbolic-ref", "--short", "HEAD")
	commit := nativeAcceptanceGitOutput(t, root, "rev-parse", "HEAD")
	tree := nativeAcceptanceGitOutput(t, root, "rev-parse", "HEAD^{tree}")
	data, err := os.ReadFile(filepath.Join(root, "src"))
	if err != nil {
		t.Fatalf("read candidate source bytes: %v", err)
	}
	return repinCandidateState{branch: branch, commit: commit, tree: tree, bytes: data}
}

func repinSameCandidateState(before, after repinCandidateState) bool {
	return before.branch == after.branch && before.commit == after.commit && before.tree == after.tree && bytes.Equal(before.bytes, after.bytes)
}

func repinReadTargetState(t *testing.T, policy sessions.NativeIntegrationPolicy, candidate sessions.NativeAcceptanceInput) repinTargetState {
	t.Helper()
	target := integrationTarget(policy, candidate)
	observation, err := store.ObserveNativeIntegrationTarget(context.Background(), target)
	if err != nil {
		t.Fatalf("observe exact integration target: %v", err)
	}
	ref := nativeAcceptanceGitOutput(t, policy.TargetCheckout, "rev-parse", policy.TargetRef)
	data, err := os.ReadFile(filepath.Join(policy.TargetCheckout, "src"))
	if err != nil {
		t.Fatalf("read target source bytes: %v", err)
	}
	return repinTargetState{observation: observation, ref: ref, bytes: data}
}

func repinSameTargetState(before, after repinTargetState) bool {
	return before.ref == after.ref && before.observation.Commit == after.observation.Commit && before.observation.Tree == after.observation.Tree && before.observation.Clean == after.observation.Clean && bytes.Equal(before.bytes, after.bytes)
}
