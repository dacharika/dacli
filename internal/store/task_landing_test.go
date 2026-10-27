package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/mdstore"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

type taskLandingFixture struct {
	t          *testing.T
	w          *workspace.Workspace
	task       *Task
	root       string
	base       string
	source     string
	sourceTree string
	target     string
	merge      string
	github     string
}

func taskLandingGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	all := []string{"-C", root, "-c", "user.name=Landing Test", "-c", "user.email=landing@example.invalid"}
	all = append(all, args...)
	cmd := exec.Command("git", all...)
	cmd.Env = VerificationEnvironment()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s (%v)", args, out, err)
	}
	return strings.TrimSpace(string(out))
}

func newTaskLandingFixture(t *testing.T) *taskLandingFixture {
	t.Helper()
	root := t.TempDir()
	cmd := exec.Command("git", "-C", root, "init", "-b", "main")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %s (%v)", out, err)
	}
	taskLandingGit(t, root, "config", "core.autocrlf", "false")
	w, err := workspace.Init(root, "landing-test")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".dacli/\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src-old"), []byte("old\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src-base"), []byte("base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	taskLandingGit(t, root, "add", ".gitignore", "src-old", "src-base")
	taskLandingGit(t, root, "commit", "-m", "base")
	base := taskLandingGit(t, root, "rev-parse", "HEAD")
	project, err := CreateProject(w, "a-root", "Landing", "core", "goal", "", model.LandingPolicy{Mode: model.LandingPR, Base: "main"})
	if err != nil {
		t.Fatal(err)
	}
	project.Doc.Front.Set("github_repo", "owner/repo")
	if err := SaveProject(project); err != nil {
		t.Fatal(err)
	}
	task, err := CreateTask(w, "a-root", "core", "Deliver source", TaskOpts{Accept: []string{"land the exact source"}})
	if err != nil {
		t.Fatal(err)
	}
	return &taskLandingFixture{t: t, w: w, task: task, root: root, base: base, github: "owner/repo"}
}

func (f *taskLandingFixture) sourceCommit() {
	f.t.Helper()
	taskLandingGit(f.t, f.root, "checkout", "-b", "source")
	if err := os.Remove(filepath.Join(f.root, "src-old")); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.root, "src-new"), []byte("renamed content\n"), 0644); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.root, "src-data.bin"), []byte{0, 0xff, 0x10, 0x00, 0x80}, 0600); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.root, "src-tool"), []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.root, "src-tab\tname"), []byte("tab name\n"), 0644); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.root, "src-lines\nname"), []byte("line name\n"), 0644); err != nil {
		f.t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(f.root, "src-tool"), 0755); err != nil {
		f.t.Fatal(err)
	}
	taskLandingGit(f.t, f.root, "add", "-A")
	taskLandingGit(f.t, f.root, "commit", "-m", "source")
	f.source = taskLandingGit(f.t, f.root, "rev-parse", "HEAD")
	f.sourceTree = taskLandingGit(f.t, f.root, "rev-parse", "HEAD^{tree}")
}

func (f *taskLandingFixture) mergeSource() {
	f.t.Helper()
	taskLandingGit(f.t, f.root, "checkout", "main")
	taskLandingGit(f.t, f.root, "merge", "--no-ff", "source", "-m", "merge source")
	f.merge = taskLandingGit(f.t, f.root, "rev-parse", "HEAD")
	f.target = f.merge
}

func (f *taskLandingFixture) observation(integratedBase, integrated, prHead, merge, target string) TaskLandingObservation {
	f.t.Helper()
	targetTree := taskLandingGit(f.t, f.root, "rev-parse", target+"^{tree}")
	repo, err := TaskLandingRepository(f.w)
	if err != nil {
		f.t.Fatal(err)
	}
	if integratedBase == "" {
		integratedBase = f.base
	}
	if integrated == "" {
		integrated = f.source
	}
	if prHead == "" {
		prHead = integrated
	}
	if merge == "" {
		merge = f.merge
	}
	if target == "" {
		target = f.target
	}
	evidence := VerificationEvidence{
		Command: "go test ./...", Argv: []string{"go", "test", "./..."}, WorkingDirectory: ".", ExitCode: 0,
		ArtifactHash: "sha256:" + strings.Repeat("a", 64), Verifier: "owner-check", CommitSHA: target, TreeSHA: targetTree, Clean: true,
		RuntimeVersions: map[string]string{"go": "go1.26.10"}, ToolVersions: map[string]string{"git": "git version fixture"},
	}
	if err := AppendVerificationEvidence(f.task, evidence); err != nil {
		f.t.Fatal(err)
	}
	verificationJSON, err := json.Marshal(evidence)
	if err != nil {
		f.t.Fatal(err)
	}
	verificationSum := sha256.Sum256(verificationJSON)
	binding, err := TaskLandingBindingDigest(f.task)
	if err != nil {
		f.t.Fatal(err)
	}
	policy, err := TaskLandingPolicyDigest(f.w, f.task.Project)
	if err != nil {
		f.t.Fatal(err)
	}
	return TaskLandingObservation{
		Schema: taskLandingSchema, OperationKey: "landing-test-op", WorkspaceID: f.w.ID, InstallationID: "installation-test",
		Project: f.task.Project, TaskID: f.task.ID, Generation: f.task.Generation(), DeliveryGeneration: f.task.DeliveryGeneration(),
		TaskBindingDigest: binding, LandingPolicyDigest: policy, Repository: repo, GitHubRepository: f.github,
		Source:               TaskLandingSource{AttemptID: "attempt-test", RevisionID: "revision-test", Sequence: 1, Repository: repo, BaseCommit: f.base, Commit: f.source, Tree: f.sourceTree, Checkout: f.root},
		IntegratedBaseCommit: integratedBase, IntegratedCommit: integrated, PRNumber: 1209,
		PRURL: "https://github.com/owner/repo/pull/1209", PRState: "MERGED", PRHead: prHead, PRBase: "main", MergeCommit: merge,
		TargetRef: "refs/heads/main", TargetCommit: target, TargetTree: targetTree,
		VerificationDigest: "sha256:" + hex.EncodeToString(verificationSum[:]), ObservedAt: time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC),
	}
}

func (f *taskLandingFixture) appendAndRead(o TaskLandingObservation) TaskLandingResult {
	f.t.Helper()
	o.ID = ""
	var err error
	o.ID, err = TaskLandingID(o)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := AppendTaskLanding(f.task, o); err != nil {
		f.t.Fatal(err)
	}
	result, present := ReadTaskLanding(context.Background(), f.w, f.task, "main")
	if !present {
		f.t.Fatal("new task landing receipt was not observed")
	}
	return result
}

func (f *taskLandingFixture) withID(o TaskLandingObservation) TaskLandingObservation {
	f.t.Helper()
	var err error
	o.ID, err = TaskLandingID(o)
	if err != nil {
		f.t.Fatal(err)
	}
	return o
}

func TestTaskLandingNormalMergeWithoutLegacyBranch(t *testing.T) {
	f := newTaskLandingFixture(t)
	f.sourceCommit()
	f.mergeSource()
	o := f.observation(f.base, f.source, f.source, f.merge, f.merge)
	got := f.appendAndRead(o)
	if got.State != LandingLanded || got.Reason != "landing_verified" || got.ReceiptID == "" {
		t.Fatalf("native merge not accepted without a task branch: %+v", got)
	}
	readback, present := ReadTaskLanding(context.Background(), f.w, f.task, "main")
	if !present || readback.State != LandingLanded {
		t.Fatalf("read after append lost native result: %+v present=%v", readback, present)
	}
	without := *f.task
	without.Doc = &mdstore.Doc{}
	if result, present := ReadTaskLanding(context.Background(), f.w, &without, "main"); present || result.State != 0 {
		t.Fatalf("missing native observation should be absent for legacy fallback: result=%+v present=%v", result, present)
	}
}

func TestTaskLandingCherryPickedDisjointBatch(t *testing.T) {
	f := newTaskLandingFixture(t)
	f.sourceCommit()
	// Map the complete source delta into a different one-commit range while
	// adding a disjoint batch change in that same commit.
	taskLandingGit(t, f.root, "checkout", "-b", "integration", f.base)
	taskLandingGit(t, f.root, "cherry-pick", "--no-commit", f.source)
	if err := os.WriteFile(filepath.Join(f.root, "batch-unrelated"), []byte("other task\n"), 0644); err != nil {
		t.Fatal(err)
	}
	taskLandingGit(t, f.root, "add", "batch-unrelated")
	taskLandingGit(t, f.root, "commit", "-m", "batch source and unrelated task")
	integrated := taskLandingGit(t, f.root, "rev-parse", "HEAD")
	taskLandingGit(t, f.root, "checkout", "main")
	taskLandingGit(t, f.root, "merge", "--no-ff", "integration", "-m", "merge batch")
	f.merge = taskLandingGit(t, f.root, "rev-parse", "HEAD")
	f.target = f.merge
	o := f.observation(f.base, integrated, integrated, f.merge, f.merge)
	got := f.appendAndRead(o)
	if got.State != LandingLanded {
		t.Fatalf("disjoint cherry-pick batch not accepted: %+v", got)
	}
	if integrated == f.source {
		t.Fatal("fixture did not distinguish source and integrated commit")
	}
}

func TestTaskLandingMultiCommitCherryPickedRange(t *testing.T) {
	f := newTaskLandingFixture(t)
	taskLandingGit(t, f.root, "checkout", "-b", "source")
	if err := os.WriteFile(filepath.Join(f.root, "src-a"), []byte("a1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	taskLandingGit(t, f.root, "add", "src-a")
	taskLandingGit(t, f.root, "commit", "-m", "source one")
	sourceOne := taskLandingGit(t, f.root, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(f.root, "src-b"), []byte("b1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	taskLandingGit(t, f.root, "add", "src-b")
	taskLandingGit(t, f.root, "commit", "-m", "source two")
	if err := os.WriteFile(filepath.Join(f.root, "src-a"), []byte("a2\n"), 0644); err != nil {
		t.Fatal(err)
	}
	taskLandingGit(t, f.root, "add", "src-a")
	taskLandingGit(t, f.root, "commit", "-m", "source correction")
	f.source = taskLandingGit(t, f.root, "rev-parse", "HEAD")
	f.sourceTree = taskLandingGit(t, f.root, "rev-parse", "HEAD^{tree}")
	taskLandingGit(t, f.root, "checkout", "-b", "integration", f.base)
	taskLandingGit(t, f.root, "cherry-pick", sourceOne)
	if err := os.WriteFile(filepath.Join(f.root, "batch-between"), []byte("interleaved unrelated commit\n"), 0644); err != nil {
		t.Fatal(err)
	}
	taskLandingGit(t, f.root, "add", "batch-between")
	taskLandingGit(t, f.root, "commit", "-m", "unrelated batch commit")
	taskLandingGit(t, f.root, "cherry-pick", sourceOne+".."+f.source)
	integrated := taskLandingGit(t, f.root, "rev-parse", "HEAD")
	taskLandingGit(t, f.root, "checkout", "main")
	taskLandingGit(t, f.root, "merge", "--no-ff", "integration", "-m", "merge integrated range")
	f.merge = taskLandingGit(t, f.root, "rev-parse", "HEAD")
	f.target = f.merge
	o := f.observation(f.base, integrated, integrated, f.merge, f.merge)
	got := f.appendAndRead(o)
	if got.State != LandingLanded {
		t.Fatalf("multi-commit cherry-pick range not accepted: %+v", got)
	}
	bad := o
	bad.ID = ""
	bad.OperationKey = "landing-final-commit-only"
	bad.IntegratedBaseCommit = taskLandingGit(t, f.root, "rev-parse", integrated+"^")
	bad.IntegratedCommit = integrated
	bad.PreviousID = o.ID
	bad.ID, _ = TaskLandingID(bad)
	if err := ValidateTaskLanding(context.Background(), f.w, f.task, bad, "main"); err == nil {
		t.Fatal("final-commit-only range unexpectedly validated")
	}
}

func TestTaskLandingReplayAndSourceCAS(t *testing.T) {
	f := newTaskLandingFixture(t)
	f.sourceCommit()
	f.mergeSource()
	o := f.withID(f.observation(f.base, f.source, f.source, f.merge, f.merge))
	got := f.appendAndRead(o)
	stored, found, err := ReadTaskLandingOperation(f.task, o.OperationKey)
	if err != nil || !found || stored.ID != o.ID || stored.ObservedAt != o.ObservedAt {
		t.Fatalf("current operation lookup did not return its exact observation: found=%v stored=%+v err=%v", found, stored, err)
	}
	first, err := json.Marshal(f.task.Doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := AppendTaskLanding(f.task, o); err != nil {
		t.Fatalf("identical idempotent replay refused: %v", err)
	}
	second, err := json.Marshal(f.task.Doc)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("identical retry changed task bytes")
	}
	changed := o
	changed.ObservedAt = changed.ObservedAt.Add(time.Second)
	changed.ID, _ = TaskLandingID(changed)
	if err := AppendTaskLanding(f.task, changed); err == nil {
		t.Fatal("changed payload under same operation key was accepted")
	}
	oldPrevious := o
	oldPrevious.OperationKey = "landing-old-previous"
	oldPrevious.PreviousID = "sha256:" + strings.Repeat("0", 64)
	oldPrevious.ID, _ = TaskLandingID(oldPrevious)
	if err := AppendTaskLanding(f.task, oldPrevious); err == nil {
		t.Fatal("old previous receipt was accepted")
	}
	if got.ReceiptID != o.ID {
		t.Fatalf("receipt ID = %q, want %q", got.ReceiptID, o.ID)
	}
}

func TestTaskLandingRejectsChangedTaskAndInvalidPresentNoFallback(t *testing.T) {
	f := newTaskLandingFixture(t)
	f.sourceCommit()
	f.mergeSource()
	o := f.observation(f.base, f.source, f.source, f.merge, f.merge)
	if result := f.appendAndRead(o); result.State != LandingLanded {
		t.Fatalf("valid baseline refused: %+v", result)
	}
	f.task.Doc.SetSection("Acceptance", "- [ ] different criterion\n")
	result, present := ReadTaskLanding(context.Background(), f.w, f.task, "main")
	if !present || result.State != LandingUnknown || result.Reason != "landing_task_changed" {
		t.Fatalf("changed task binding did not fail closed: %+v present=%v", result, present)
	}
	// Even a malformed marker is present new-format state and must not look
	// absent to callers that retain legacy branch resolution.
	f.task.Doc.Front.Set("native_landing.current", "sha256:broken")
	result, present = ReadTaskLanding(context.Background(), f.w, f.task, "main")
	if !present || result.State != LandingUnknown || result.Reason != "landing_receipt_invalid" {
		t.Fatalf("malformed marker fell through as legacy absence: %+v present=%v", result, present)
	}
}

func TestTaskLandingMalformedMarkerNeverLooksAbsent(t *testing.T) {
	f := newTaskLandingFixture(t)
	f.task.Doc.Front.Set("native_landing.current", "sha256:broken")
	result, present := ReadTaskLanding(context.Background(), f.w, f.task, "main")
	if !present || result.State != LandingUnknown || result.Reason != "landing_receipt_invalid" {
		t.Fatalf("malformed current marker looked absent: %+v present=%v", result, present)
	}
}

func TestTaskLandingRejectsPolicyDrift(t *testing.T) {
	f := newTaskLandingFixture(t)
	f.sourceCommit()
	f.mergeSource()
	o := f.observation(f.base, f.source, f.source, f.merge, f.merge)
	if result := f.appendAndRead(o); result.State != LandingLanded {
		t.Fatalf("valid baseline refused: %+v", result)
	}
	project, err := LoadProject(f.w, f.task.Project)
	if err != nil {
		t.Fatal(err)
	}
	project.Doc.Front.Set("github_repo", "other/repo")
	if err := SaveProject(project); err != nil {
		t.Fatal(err)
	}
	result, present := ReadTaskLanding(context.Background(), f.w, f.task, "main")
	if !present || result.State != LandingUnknown || result.Reason != "landing_policy_changed" {
		t.Fatalf("project repository drift did not invalidate the receipt: %+v present=%v", result, present)
	}
}

func TestTaskLandingSourceChangesMustFitDeclaredClaims(t *testing.T) {
	f := newTaskLandingFixture(t)
	f.task.Doc.Front.SetList("claims", []string{"allowed-path"})
	f.sourceCommit()
	f.mergeSource()
	o := f.withID(f.observation(f.base, f.source, f.source, f.merge, f.merge))
	if err := ValidateTaskLanding(context.Background(), f.w, f.task, o, "main"); err == nil || !strings.Contains(err.Error(), "outside task claims") {
		t.Fatalf("source changes outside declared claims were accepted: %v", err)
	}
}

func TestTaskLandingBindingDigestIgnoresChecklistStateAndRejectsMalformedGeneration(t *testing.T) {
	f := newTaskLandingFixture(t)
	before, err := TaskLandingBindingDigest(f.task)
	if err != nil {
		t.Fatal(err)
	}
	CheckAllAcceptance(f.task)
	after, err := TaskLandingBindingDigest(f.task)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatal("checking acceptance boxes changed source binding")
	}
	f.task.Doc.Front.Set("parent_generation", "not-a-number")
	if _, err := TaskLandingBindingDigest(f.task); err == nil {
		t.Fatal("malformed parent generation was silently treated as zero")
	}
}

func TestTaskLandingForwardTargetAndRewrite(t *testing.T) {
	f := newTaskLandingFixture(t)
	f.sourceCommit()
	f.mergeSource()
	o := f.observation(f.base, f.source, f.source, f.merge, f.merge)
	if result := f.appendAndRead(o); result.State != LandingLanded {
		t.Fatalf("valid landing refused: %+v", result)
	}
	if err := os.WriteFile(filepath.Join(f.root, "src-new"), []byte("later normal edit\n"), 0644); err != nil {
		t.Fatal(err)
	}
	taskLandingGit(t, f.root, "add", "src-new")
	taskLandingGit(t, f.root, "commit", "-m", "later change")
	advanced := taskLandingGit(t, f.root, "rev-parse", "HEAD")
	if advanced == f.merge {
		t.Fatal("target did not advance")
	}
	if result, present := ReadTaskLanding(context.Background(), f.w, f.task, "main"); !present || result.State != LandingLanded {
		t.Fatalf("ordinary forward target stopped being landed: %+v present=%v", result, present)
	}
	// Move the configured target to a genuinely unrelated history.
	taskLandingGit(t, f.root, "checkout", "--orphan", "rewritten")
	taskLandingGit(t, f.root, "rm", "-rf", ".")
	if err := os.WriteFile(filepath.Join(f.root, "unrelated"), []byte("new root\n"), 0644); err != nil {
		t.Fatal(err)
	}
	taskLandingGit(t, f.root, "add", "unrelated")
	taskLandingGit(t, f.root, "commit", "-m", "rewritten target")
	rewritten := taskLandingGit(t, f.root, "rev-parse", "HEAD")
	taskLandingGit(t, f.root, "branch", "-f", "main", rewritten)
	taskLandingGit(t, f.root, "checkout", "main")
	result, present := ReadTaskLanding(context.Background(), f.w, f.task, "main")
	if !present || result.State != LandingUnlanded {
		t.Fatalf("rewritten target not reported unlanded: %+v present=%v", result, present)
	}
}

func TestTaskLandingRevertedInsidePullRequest(t *testing.T) {
	f := newTaskLandingFixture(t)
	f.sourceCommit()
	taskLandingGit(t, f.root, "checkout", "-b", "integration", f.base)
	taskLandingGit(t, f.root, "cherry-pick", f.source)
	integrated := taskLandingGit(t, f.root, "rev-parse", "HEAD")
	if err := os.Remove(filepath.Join(f.root, "src-new")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.root, "src-old"), []byte("old again\n"), 0644); err != nil {
		t.Fatal(err)
	}
	taskLandingGit(t, f.root, "add", "-A")
	taskLandingGit(t, f.root, "commit", "-m", "revert source inside PR")
	prHead := taskLandingGit(t, f.root, "rev-parse", "HEAD")
	taskLandingGit(t, f.root, "checkout", "main")
	taskLandingGit(t, f.root, "merge", "--no-ff", "integration", "-m", "merge reverted PR")
	merge := taskLandingGit(t, f.root, "rev-parse", "HEAD")
	f.merge, f.target = merge, merge
	o := f.observation(f.base, integrated, prHead, merge, merge)
	o.ID, _ = TaskLandingID(o)
	if err := ValidateTaskLanding(context.Background(), f.w, f.task, o, "main"); err == nil {
		t.Fatal("ancestor-only PR evidence accepted despite source paths reverted in PR")
	}
}

func TestTaskLandingAppendRejectsCorruptEarlierReceipt(t *testing.T) {
	f := newTaskLandingFixture(t)
	f.sourceCommit()
	f.mergeSource()
	first := f.withID(f.observation(f.base, f.source, f.source, f.merge, f.merge))
	if err := AppendTaskLanding(f.task, first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.ID = ""
	second.OperationKey = "landing-second-op"
	second.PreviousID = first.ID
	second.ObservedAt = second.ObservedAt.Add(time.Second)
	second.ID, _ = TaskLandingID(second)
	if err := AppendTaskLanding(f.task, second); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := ReadTaskLandingOperation(f.task, "landing-test-op"); err == nil || ok {
		t.Fatal("operation lookup reselected an older receipt")
	}
	section, _ := f.task.Doc.Section(taskLandingSection)
	lines := strings.Split(strings.TrimSuffix(section.Content, "\n"), "\n")
	var earlier map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &earlier); err != nil {
		t.Fatal(err)
	}
	earlier["id"] = "sha256:" + strings.Repeat("0", 64)
	corrupt, err := json.Marshal(earlier)
	if err != nil {
		t.Fatal(err)
	}
	lines[0] = string(corrupt)
	f.task.Doc.SetSection(taskLandingSection, strings.Join(lines, "\n")+"\n")
	before := mdstore.Render(f.task.Doc)
	third := second
	third.ID = ""
	third.OperationKey = "landing-third-op"
	third.PreviousID = second.ID
	third.ObservedAt = third.ObservedAt.Add(time.Second)
	third.ID, _ = TaskLandingID(third)
	if err := AppendTaskLanding(f.task, third); err == nil {
		t.Fatal("append accepted a corrupt earlier record")
	}
	if after := mdstore.Render(f.task.Doc); after != before {
		t.Fatal("refused corrupt-history append changed task document bytes")
	}
	if _, ok, err := ReadTaskLandingOperation(f.task, "landing-second-op"); err == nil || ok {
		t.Fatal("operation lookup accepted a malformed earlier receipt")
	}
}

func TestTaskLandingCancellationAndMissingAncestryRemainUnknown(t *testing.T) {
	f := newTaskLandingFixture(t)
	f.sourceCommit()
	f.mergeSource()
	o := f.withID(f.observation(f.base, f.source, f.source, f.merge, f.merge))
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := TaskLandingRepositoryContext(canceled, f.w); err == nil {
		t.Fatal("repository identity ignored canceled operation context")
	}
	if _, err := TaskLandingPolicyDigestContext(canceled, f.w, f.task.Project); err == nil {
		t.Fatal("policy digest ignored canceled operation context")
	}
	if err := ValidateTaskLanding(canceled, f.w, f.task, o, "main"); err == nil || errors.Is(err, errTaskLandingUnlanded) {
		t.Fatalf("cancellation was not an unknown refusal: %v", err)
	}
	missing := o
	missing.ID = ""
	missing.IntegratedBaseCommit = strings.Repeat("f", len(o.IntegratedBaseCommit))
	missing.ID, _ = TaskLandingID(missing)
	if err := ValidateTaskLanding(context.Background(), f.w, f.task, missing, "main"); err == nil || errors.Is(err, errTaskLandingUnlanded) {
		t.Fatalf("missing integration object was reported as proven unlanded: %v", err)
	}
}

func TestTaskLandingRequiresExactFinalVerification(t *testing.T) {
	f := newTaskLandingFixture(t)
	f.sourceCommit()
	f.mergeSource()
	o := f.observation(f.base, f.source, f.source, f.merge, f.merge)
	wrong := VerificationEvidence{
		Command: "go test ./...", Argv: []string{"go", "test", "./..."}, WorkingDirectory: ".", ExitCode: 0,
		ArtifactHash: "sha256:" + strings.Repeat("b", 64), Verifier: "owner-check", CommitSHA: f.source, TreeSHA: f.sourceTree, Clean: true,
		RuntimeVersions: map[string]string{"go": "go1.26.10"}, ToolVersions: map[string]string{"git": "git version fixture"},
	}
	if err := AppendVerificationEvidence(f.task, wrong); err != nil {
		t.Fatal(err)
	}
	wrongJSON, err := json.Marshal(wrong)
	if err != nil {
		t.Fatal(err)
	}
	wrongSum := sha256.Sum256(wrongJSON)
	o.VerificationDigest = "sha256:" + hex.EncodeToString(wrongSum[:])
	o = f.withID(o)
	if err := ValidateTaskLanding(context.Background(), f.w, f.task, o, "main"); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("verification for the wrong commit/tree was accepted: %v", err)
	}
}

func TestTaskLandingRejectsWrongSourceTaskRepositoryBaseTree(t *testing.T) {
	f := newTaskLandingFixture(t)
	f.sourceCommit()
	f.mergeSource()
	good := f.withID(f.observation(f.base, f.source, f.source, f.merge, f.merge))
	cases := []struct {
		name   string
		mutate func(*TaskLandingObservation)
	}{
		{"wrong-task", func(o *TaskLandingObservation) { o.TaskID = "another-task" }},
		{"invalid-source-repository", func(o *TaskLandingObservation) { o.Source.Repository = "relative/source/repository" }},
		{"wrong-source-base", func(o *TaskLandingObservation) { o.Source.BaseCommit = o.Source.Commit }},
		{"wrong-source-tree", func(o *TaskLandingObservation) { o.Source.Tree = strings.Repeat("a", len(o.Source.Tree)) }},
		{"wrong-target-repository", func(o *TaskLandingObservation) { o.Repository = filepath.Join(f.root, ".git", "other") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := good
			o.ID = ""
			tc.mutate(&o)
			o.ID, _ = TaskLandingID(o)
			if err := ValidateTaskLanding(context.Background(), f.w, f.task, o, "main"); err == nil {
				t.Fatal("invalid source/task/repository binding was accepted")
			}
		})
	}
}
