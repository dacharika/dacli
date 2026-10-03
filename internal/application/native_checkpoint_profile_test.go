package application

import (
	"context"
	"errors"
	"github.com/mlnomadpy/dacli/internal/eventlog"
	"github.com/mlnomadpy/dacli/internal/governance/checkout"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/procmon"
	"github.com/mlnomadpy/dacli/internal/store"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// This fixture uses a real configured local command and separate bearer roots.
// It does not claim a model host turn or an integration/acceptance producer.
type checkpointProducerAdmission struct {
	*LeaseAdmission
	trunk string
}

func (a checkpointProducerAdmission) ObserveCheckpoint(w, p string, tasks []string) (sessions.CheckpointObservation, error) {
	snapshot, e := a.ObserveSwarm(w, p)
	if e != nil {
		return sessions.CheckpointObservation{}, e
	}
	obs, e := checkout.Observe(a.trunk)
	return sessions.CheckpointObservation{Digest: safeDigest(snapshot), Trunk: obs.Commit}, e
}
func (a checkpointProducerAdmission) AdmitSwarmCapability(string, string, string, string) error {
	return nil
}
func checkpointProducerAuthority(t *testing.T, f nativeFixture, review sessions.ReviewObserver) (*sessions.Authority, sessions.Session, string) {
	t.Helper()
	ad, e := NewLeaseAdmission(f.workspace)
	if e != nil {
		t.Fatal(e)
	}
	observer := checkpointProducerAdmission{ad, f.attempt.Request.Checkout}
	a, e := sessions.OpenReviewAuthority(f.path, sessions.Binding{Workspace: f.workspace.ID, Installation: "installation"}, review, observer)
	if e != nil {
		t.Fatal(e)
	}
	for _, upgrade := range []func(string) error{a.UpgradeSwarms, a.UpgradeDispatchIntents, a.UpgradeLifecycle, a.UpgradeRecovery, a.UpgradePreflight, a.UpgradeCheckpoints} {
		if e = upgrade(f.owner); e != nil {
			t.Fatal(e)
		}
	}
	task := f.attempt.Request.TaskID
	if e = a.ConfigureSwarm(f.owner, sessions.SwarmPolicy{ProjectID: "p", Revision: "swarm", Harness: "codex", CapabilityDigest: "fixture-capability", Width: 1, WIP: 1, TTL: time.Minute, Roles: []sessions.SwarmRole{{ID: "implement", Kind: "implementation", Harness: "codex", Capacity: 1}, {ID: "review", Kind: "review", Harness: "codex", Capacity: 1}}, Placements: map[string]sessions.SwarmPlacement{task: {Checkout: f.attempt.Request.Checkout, ImplementKind: "implementation", ReviewKind: "review"}}}); e != nil {
		t.Fatal(e)
	}
	if e = a.ConfigureCheckpoints(f.owner, sessions.CheckpointPolicy{SchemaVersion: 1, ProjectID: "p", Revision: "checkpoint", Harness: "codex", TaskIDs: []string{task}, MaxCycles: 3, MaxNoProgress: 2, ExpiresAt: time.Now().Add(time.Hour)}); e != nil {
		t.Fatal(e)
	}
	controller, ct, e := a.Enroll(f.owner, sessions.Scope{Actions: []string{"checkpoint.read", "checkpoint.record", "checkpoint.reconcile", "swarm.plan", "swarm.reserve"}, Resources: []string{"project:p", "task:" + task}}, time.Now().Add(time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	return a, controller, ct
}
func TestNativeCheckpointActualCommandAndIndependentReviewCrashRestart(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "actual-command-count")
	f := nativeSetup(t, "printf executed >> '"+marker+"'; printf checked")
	task, e := store.FindTask(f.workspace, f.attempt.Request.TaskID)
	if e != nil {
		t.Fatal(e)
	}
	task.Doc.SetSection("Acceptance", "- [ ] actual command evidence")
	if e = store.SaveTask(task); e != nil {
		t.Fatal(e)
	}
	policy := &ownerReviewPolicy{revision: "review-policy", checks: []sessions.RequiredCheck{{ID: f.check.ID, CommandDigest: sessions.CheckDigest(f.check)}}}
	reviewObserver, e := NewNativeReviewObserver(f.workspace, policy)
	if e != nil {
		t.Fatal(e)
	}
	a, controller, ct := checkpointProducerAuthority(t, f, reviewObserver)
	ev, e := f.executor.Run(context.Background(), f.workerToken, f.request)
	if e != nil {
		t.Fatal(e)
	}
	// The actual producer committed an outcome; orchestrator is interrupted before
	// checkpoint write. Reopening must discover/replay that exact operation.
	restarted, e := sessions.Open(f.path, sessions.Binding{Workspace: f.workspace.ID, Installation: "installation"})
	if e != nil {
		t.Fatal(e)
	}
	runner, e := NewNativeVerificationRunner(restarted, f.runnerToken, f.runner.ID, []NativeCheck{{f.check, f.grant.ID}})
	if e != nil {
		t.Fatal(e)
	}
	replay, e := runner.Run(context.Background(), f.workerToken, f.request)
	if e != nil || !reflect.DeepEqual(replay, ev) {
		t.Fatalf("command crash replay changed authenticated receipt: %v", e)
	}
	if b, e := os.ReadFile(marker); e != nil || string(b) != "executed" {
		t.Fatalf("configured command must execute exactly once: %q %v", b, e)
	}
	v := sessions.VerificationRequest{OperationKey: f.request.OperationKey, WorkerSession: f.worker.ID, AttemptID: f.attempt.ID, LeaseID: f.lease.ID, RevisionID: f.revision.ID, CheckID: f.check.ID, GrantID: f.grant.ID, ExpectedFence: f.lease.Fence}
	request := sessions.CheckpointRequest{OperationKey: "after-verification", CheckpointID: "checkpoint", ProjectID: "p", InvocationID: "inv-one", Phase: "verification", Refs: []sessions.CheckpointRefs{{TaskID: task.ID, EvidenceID: ev.ID, VerificationKey: v.OperationKey, Verification: v, RunnerSession: f.runner.ID}}}
	request.OperationKey = "record-verification"
	recorded, err := a.RecordCheckpoint(ct, controller.ID, request)
	if err != nil {
		t.Fatal("record failed", err)
	}
	request.OperationKey = "after-verification"
	request.ExpectedSequence = recorded.Sequence
	verified, e := a.ReconcileCheckpoint(ct, controller.ID, request)
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, fact := range verified.Facts {
		if fact.Kind == "evidence" && fact.ID == ev.ID && fact.Fence == f.lease.Fence {
			found = true
		}
	}
	if !found || verified.WaveAllowed {
		t.Fatalf("actual evidence is historical fact, never a wave permit: %+v", verified)
	}
	reviewer, rt, e := a.Enroll(f.owner, sessions.Scope{Actions: []string{"review.propose", "review.evaluate"}, Resources: []string{"task:" + task.ID}}, time.Now().Add(time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	grant, e := a.GrantReview(f.owner, sessions.ReviewGrantRequest{ReviewerSession: reviewer.ID, WorkerSession: f.worker.ID, TaskID: task.ID, Repository: f.attempt.Observation.Repository, ExpiresAt: time.Now().Add(time.Minute)})
	if e != nil {
		t.Fatal(e)
	}
	rr := sessions.ReviewRequest{OperationKey: "actual-independent-review", GrantID: grant.ID, AttemptID: f.attempt.ID, LeaseID: f.lease.ID, RevisionID: f.revision.ID, ExpectedFence: f.lease.Fence, Outcome: "approve", Rationale: "Actual named command checked in separate reviewer root", EvidenceIDs: []string{ev.ID}}
	proposal, e := a.ProposeReview(rt, reviewer.ID, rr)
	if e != nil {
		t.Fatal(e)
	}
	ad, e := NewLeaseAdmission(f.workspace)
	if e != nil {
		t.Fatal(e)
	}
	a, e = sessions.OpenReviewAuthority(f.path, sessions.Binding{Workspace: f.workspace.ID, Installation: "installation"}, reviewObserver, checkpointProducerAdmission{ad, f.attempt.Request.Checkout})
	if e != nil {
		t.Fatal(e)
	}
	discovered, e := a.CheckpointStatus(ct, controller.ID, "p", "checkpoint")
	if e != nil {
		t.Fatal(e)
	}
	seenOmittedReview := false
	for _, fact := range discovered.Facts {
		if fact.Kind == "review" && fact.ID == proposal.ID {
			seenOmittedReview = true
		}
	}
	if !seenOmittedReview {
		t.Fatal("omitted actual independent review must still be discovered from durable authority")
	}
	reviewReplay, e := a.ProposeReview(rt, reviewer.ID, rr)
	if e != nil || !reflect.DeepEqual(proposal, reviewReplay) {
		t.Fatalf("independent review crash replay changed proposal: %v", e)
	}
	request.OperationKey = "after-review"
	request.InvocationID = "inv-two"
	request.Phase = "review"
	request.ExpectedSequence = verified.Sequence
	request.Refs = append(request.Refs, sessions.CheckpointRefs{TaskID: task.ID, ReviewID: proposal.ID})
	reviewed, e := a.ReconcileCheckpoint(ct, controller.ID, request)
	if e != nil {
		t.Fatal(e)
	}
	if checkpointClass(reviewed, "review_ineligible") {
		t.Fatalf("current independent review unexpectedly ineligible: %+v", reviewed)
	}
	if e = a.RevokeReview(f.owner, grant.ID); e != nil {
		t.Fatal(e)
	}
	stale, e := a.CheckpointStatus(ct, controller.ID, "p", "checkpoint")
	if e != nil || !checkpointClass(stale, "review_ineligible") {
		t.Fatalf("historical proposal cannot survive fresh grant revocation as eligibility: %+v %v", stale, e)
	}
	target, e := a.OwnerRecoveryTarget(f.owner, "lease", f.lease.ID)
	if e != nil {
		t.Fatal(e)
	}
	decision, e := a.Recover(f.owner, sessions.RecoveryRequest{OperationKey: "retire-original-worker", Target: target, Mode: "owner-reconciliation", Rationale: "owner retires completed isolated fixture attempt", Uncertainty: "all subprocess termination remains unverified", ReleaseRisk: "owner accepts old writer risk for this disposable fixture"})
	if e != nil {
		t.Fatal(e)
	}
	replacement, wt, e := a.Enroll(f.owner, f.worker.Scope, time.Now().Add(time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	replacementAttempt, e := a.RegisterAttempt(wt, replacement.ID, sessions.AttemptRequest{RequestID: "replacement-attempt", TaskID: task.ID, Checkout: f.attempt.Request.Checkout, HostLabel: "replacement-fixture"})
	if e != nil {
		t.Fatal(e)
	}
	replacementLease, e := a.AcquireLease(wt, replacement.ID, sessions.AcquireLeaseRequest{OperationKey: "replacement-lease", AttemptID: replacementAttempt.ID, Resources: []sessions.Resource{{Kind: "checkout"}}, ExpiresAt: time.Now().Add(10 * time.Minute)})
	if e != nil {
		t.Fatal(e)
	}
	replacementRevision, e := a.PublishAttemptRevision(wt, replacement.ID, sessions.PublishRevisionRequest{OperationKey: "replacement-publish", AttemptID: replacementAttempt.ID, LeaseID: replacementLease.ID, ExpectedFence: replacementLease.Fence})
	if e != nil {
		t.Fatal(e)
	}
	replacementRunner, e := NewNativeVerificationRunner(a, f.runnerToken, f.runner.ID, []NativeCheck{{f.check, f.grant.ID}})
	if e != nil {
		t.Fatal(e)
	}
	replacementRequest := NativeVerificationRequest{OperationKey: "replacement-verification", WorkerSession: replacement.ID, AttemptID: replacementAttempt.ID, LeaseID: replacementLease.ID, RevisionID: replacementRevision.ID, CheckID: f.check.ID, ExpectedFence: replacementLease.Fence}
	replacementEvidence, e := replacementRunner.Run(context.Background(), wt, replacementRequest)
	if e != nil {
		t.Fatal(e)
	}
	replacementGrant, e := a.GrantReview(f.owner, sessions.ReviewGrantRequest{ReviewerSession: reviewer.ID, WorkerSession: replacement.ID, TaskID: task.ID, Repository: replacementAttempt.Observation.Repository, ExpiresAt: time.Now().Add(time.Minute)})
	if e != nil {
		t.Fatal(e)
	}
	replacementReview, e := a.ProposeReview(rt, reviewer.ID, sessions.ReviewRequest{OperationKey: "replacement-review", GrantID: replacementGrant.ID, AttemptID: replacementAttempt.ID, LeaseID: replacementLease.ID, RevisionID: replacementRevision.ID, ExpectedFence: replacementLease.Fence, Outcome: "approve", Rationale: "separately enrolled replacement with actual command evidence", EvidenceIDs: []string{replacementEvidence.ID}})
	if e != nil {
		t.Fatal(e)
	}
	request.OperationKey = "after-owner-recovery-and-replacement"
	request.InvocationID = "inv-replacement"
	request.ExpectedSequence = reviewed.Sequence
	request.Refs = append(request.Refs, sessions.CheckpointRefs{TaskID: task.ID, EvidenceID: replacementEvidence.ID, ReviewID: replacementReview.ID})
	replacementStatus, e := a.ReconcileCheckpoint(ct, controller.ID, request)
	if e != nil {
		t.Fatal(e)
	}
	oldHistorical, newEligible := false, false
	for _, fact := range replacementStatus.Facts {
		if fact.Kind == "review" && fact.ID == proposal.ID && fact.State == string(sessions.ReviewWorkerInvalid) {
			oldHistorical = true
		}
		if fact.Kind == "review" && fact.ID == replacementReview.ID && fact.State == string(sessions.ReviewEligible) {
			newEligible = true
		}
	}
	oldRetired := false
	for _, diag := range replacementStatus.Diagnostics {
		if diag.Reference == proposal.ID && diag.Class == "review_ineligible" {
			oldRetired = true
		}
	}
	for _, finding := range replacementStatus.Findings {
		if finding.Reference == proposal.ID || finding.Reference == ev.ID {
			t.Fatalf("retired historical producer must not trap replacement after owner decision %s: %+v", decision.ID, replacementStatus)
		}
	}
	if !oldHistorical || !oldRetired || !newEligible {
		t.Fatalf("retain old ineligible history separately from actual replacement eligibility: %+v", replacementStatus)
	}
	reviewed = replacementStatus
	// Caller-authored integration/acceptance refs still have no producer outcome.
	request.OperationKey = "contract-only"
	request.InvocationID = "inv-three"
	request.Phase = "integration"
	request.ExpectedSequence = reviewed.Sequence
	request.Refs = append(request.Refs, sessions.CheckpointRefs{TaskID: task.ID, IntegrationOperationID: "592-producer-pending", AcceptanceOperationID: "594-producer-pending"})
	unknown, e := a.ReconcileCheckpoint(ct, controller.ID, request)
	if e != nil || !checkpointClass(unknown, "pending_local_integration") || !checkpointClass(unknown, "pending_canonical_acceptance") || unknown.WaveAllowed {
		t.Fatalf("contract fixture is not producer proof: %+v %v", unknown, e)
	}
}
func checkpointClass(s sessions.CheckpointStatus, class string) bool {
	for _, f := range s.Findings {
		if f.Class == class {
			return true
		}
	}
	return false
}

func TestNativeCheckpointTerminalTaskEventAndTrunkAreObservedReadOnly(t *testing.T) {
	f := nativeSetup(t, "printf checked")
	task, e := store.FindTask(f.workspace, f.attempt.Request.TaskID)
	if e != nil {
		t.Fatal(e)
	}
	if e = store.MoveTask(f.workspace, task, model.StatusDone); e != nil {
		t.Fatal(e)
	}
	event, e := eventlog.Append(f.workspace, "fixture-owner", model.EventComment, task.ID, "fixture", "terminal task pending event")
	if e != nil {
		t.Fatal(e)
	}
	nativeGit(t, f.workspace.Root, "init")
	nativeGit(t, f.workspace.Root, "add", ".")
	nativeGit(t, f.workspace.Root, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-m", "fixture trunk")
	ad, e := NewLeaseAdmission(f.workspace)
	if e != nil {
		t.Fatal(e)
	}
	before, e := os.ReadFile(event.Path)
	if e != nil {
		t.Fatal(e)
	}
	observation, e := ad.ObserveCheckpoint(f.workspace.ID, "p", []string{f.attempt.Request.TaskID})
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, finding := range observation.Findings {
		if finding.Class == "terminal_task_event" && finding.Reference == event.ID {
			found = true
		}
	}
	if !found || len(observation.Trunk) != 40 {
		t.Fatalf("exact unapplied terminal event and real trunk must be observed: %+v", observation)
	}
	after, e := os.ReadFile(event.Path)
	if e != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("checkpoint observer finalized an event without owner decision", e)
	}
}

func TestNativeCheckpointPendingActualVerificationNeverReexecutes(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "must-not-run")
	f := nativeSetup(t, "printf unexpected > '"+marker+"'")
	policy := &ownerReviewPolicy{revision: "policy", checks: []sessions.RequiredCheck{{ID: f.check.ID, CommandDigest: sessions.CheckDigest(f.check)}}}
	observer, e := NewNativeReviewObserver(f.workspace, policy)
	if e != nil {
		t.Fatal(e)
	}
	a, controller, ct := checkpointProducerAuthority(t, f, observer)
	prepared, receipt, e := f.executor.Prepare(f.workerToken, f.request)
	if e != nil || receipt != nil || prepared == nil {
		t.Fatal("actual BeginVerification did not reserve", e)
	}
	// Orchestrator loss before Execute leaves a durable pending operation. The
	// prepared object is discarded; a restart must not create another execution.
	restarted, e := sessions.Open(f.path, sessions.Binding{Workspace: f.workspace.ID, Installation: "installation"})
	if e != nil {
		t.Fatal(e)
	}
	runner, e := NewNativeVerificationRunner(restarted, f.runnerToken, f.runner.ID, []NativeCheck{{f.check, f.grant.ID}})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = runner.Run(context.Background(), f.workerToken, f.request); !errors.Is(e, sessions.ErrReconcile) {
		t.Fatalf("pending actual operation must require reconciliation, never execute: %v", e)
	}
	if _, e = os.Stat(marker); !os.IsNotExist(e) {
		t.Fatal("pending verification reran configured command", e)
	}
	v := sessions.VerificationRequest{OperationKey: f.request.OperationKey, WorkerSession: f.worker.ID, AttemptID: f.attempt.ID, LeaseID: f.lease.ID, RevisionID: f.revision.ID, CheckID: f.check.ID, GrantID: f.grant.ID, ExpectedFence: f.lease.Fence}
	r := sessions.CheckpointRequest{OperationKey: "reconcile-pending", CheckpointID: "checkpoint", ProjectID: "p", InvocationID: "inv-one", Phase: "verification", Refs: []sessions.CheckpointRefs{{TaskID: f.attempt.Request.TaskID, VerificationKey: v.OperationKey, Verification: v, RunnerSession: f.runner.ID}}}
	omitted := r
	omitted.OperationKey = "reconcile-omitted"
	omitted.Refs = nil
	omittedStatus, e := a.ReconcileCheckpoint(ct, controller.ID, omitted)
	if e != nil || !checkpointClass(omittedStatus, "verification_pending") {
		t.Fatalf("omitted actual pending verification must be discovered: %+v %v", omittedStatus, e)
	}
	r.ExpectedSequence = omittedStatus.Sequence
	status, e := a.ReconcileCheckpoint(ct, controller.ID, r)
	if e != nil || !checkpointClass(status, "verification_pending") || status.WaveAllowed {
		t.Fatalf("actual pending command must have a distinct typed halt: %+v %v", status, e)
	}
}

func TestNativeCheckpointMalformedCanonicalIsTypedUnknown(t *testing.T) {
	f := nativeSetup(t, "printf checked")
	task, e := store.FindTask(f.workspace, f.attempt.Request.TaskID)
	if e != nil {
		t.Fatal(e)
	}
	raw, e := os.ReadFile(task.Path)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(filepath.Dir(task.Path), "duplicate-fixture.md"), raw, 0600); e != nil {
		t.Fatal(e)
	}
	ad, e := NewLeaseAdmission(f.workspace)
	if e != nil {
		t.Fatal(e)
	}
	observation, e := ad.ObserveCheckpoint(f.workspace.ID, "p", []string{f.attempt.Request.TaskID})
	if e != nil || len(observation.Findings) == 0 || observation.Findings[0].Class != "inconsistent_task_record" || observation.Findings[0].Knowledge != "unknown" {
		t.Fatalf("malformed or duplicate canonical projection must produce typed unknown: %+v %v", observation, e)
	}
}

func TestNativeCheckpointConfiguredTrunkRefIgnoresFeatureHEAD(t *testing.T) {
	f := nativeSetup(t, "printf checked")
	project, e := store.LoadProject(f.workspace, "p")
	if e != nil {
		t.Fatal(e)
	}
	if e = store.ConfigureProjectLanding(project, model.LandingPolicy{Mode: model.LandingLocal, Base: "fixture-trunk"}); e != nil {
		t.Fatal(e)
	}
	if e = store.SaveProject(project); e != nil {
		t.Fatal(e)
	}
	nativeGit(t, f.workspace.Root, "init")
	nativeGit(t, f.workspace.Root, "add", ".")
	nativeGit(t, f.workspace.Root, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-m", "fixture base")
	nativeGit(t, f.workspace.Root, "branch", "-M", "fixture-trunk")
	ad, e := NewLeaseAdmission(f.workspace)
	if e != nil {
		t.Fatal(e)
	}
	before, e := ad.ObserveCheckpoint(f.workspace.ID, "p", []string{f.attempt.Request.TaskID})
	if e != nil || len(before.Trunk) != 40 || before.TrunkRef != "refs/heads/fixture-trunk" {
		t.Fatal(before, e)
	}
	nativeGit(t, f.workspace.Root, "checkout", "-b", "fixture-feature")
	if e = os.WriteFile(filepath.Join(f.workspace.Root, "feature-only"), []byte("feature"), 0600); e != nil {
		t.Fatal(e)
	}
	nativeGit(t, f.workspace.Root, "add", "feature-only")
	nativeGit(t, f.workspace.Root, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-m", "feature advance")
	feature, e := ad.ObserveCheckpoint(f.workspace.ID, "p", []string{f.attempt.Request.TaskID})
	if e != nil || feature.Trunk != before.Trunk {
		t.Fatalf("feature HEAD must not masquerade as trunk progress: %+v %v", feature, e)
	}
	nativeGit(t, f.workspace.Root, "update-ref", "refs/heads/fixture-trunk", "HEAD")
	advanced, e := ad.ObserveCheckpoint(f.workspace.ID, "p", []string{f.attempt.Request.TaskID})
	if e != nil || advanced.Trunk == before.Trunk || advanced.TrunkRef != before.TrunkRef {
		t.Fatalf("configured local trunk advancement must be observed: %+v %v", advanced, e)
	}
}
func TestNativeCheckpointPublicProjectionFiltersForeignRunAndTaskEvents(t *testing.T) {
	f := nativeSetup(t, "printf checked")
	foreignProject, e := store.CreateProject(f.workspace, "root", "Foreign", "foreign", "goal", "")
	if e != nil {
		t.Fatal(e)
	}
	foreign, e := store.CreateTask(f.workspace, "root", foreignProject.Slug, "Foreign", store.TaskOpts{})
	if e != nil {
		t.Fatal(e)
	}
	other, e := store.CreateTask(f.workspace, "root", "p", "Outside checkpoint scope", store.TaskOpts{})
	if e != nil {
		t.Fatal(e)
	}
	if e = store.MoveTask(f.workspace, other, model.StatusDone); e != nil {
		t.Fatal(e)
	}
	event, e := eventlog.Append(f.workspace, "fixture-owner", model.EventComment, other.ID, "fixture", "scoped-out terminal task event")
	if e != nil {
		t.Fatal(e)
	}
	run := "foreign-fixture-run"
	if e = os.MkdirAll(f.workspace.RunDir(run), 0700); e != nil {
		t.Fatal(e)
	}
	if e = procmon.WriteRecord(filepath.Join(f.workspace.RunDir(run), "proc.txt"), procmon.Record{PID: 99999999, Task: foreign.ID}); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(f.workspace.RunDir(run), "outcome.md"), []byte("outcome: running (detached)"), 0600); e != nil {
		t.Fatal(e)
	}
	nativeGit(t, f.workspace.Root, "init")
	nativeGit(t, f.workspace.Root, "add", ".")
	nativeGit(t, f.workspace.Root, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-m", "fixture trunk")
	raw, e := store.LocalDeliveryProjection(f.workspace, "p", time.Time{})
	if e != nil {
		t.Fatal(e)
	}
	foreignPresent := false
	eventPresent := false
	for _, finding := range raw.Findings {
		if finding.ObjectID == run {
			foreignPresent = true
		}
		if finding.ObjectID == event.ID {
			eventPresent = true
		}
	}
	if !foreignPresent || !eventPresent {
		t.Fatal("fixture must reproduce broad local projection before filtering")
	}
	ad, e := NewLeaseAdmission(f.workspace)
	if e != nil {
		t.Fatal(e)
	}
	public, e := ad.ObserveCheckpoint(f.workspace.ID, "p", []string{f.attempt.Request.TaskID})
	if e != nil {
		t.Fatal(e)
	}
	for _, finding := range public.Findings {
		if finding.Reference == run || finding.Reference == event.ID || finding.TaskID == foreign.ID || finding.TaskID == other.ID {
			t.Fatalf("exact checkpoint task scope must filter unrelated run/task event: %+v", public)
		}
	}
}
