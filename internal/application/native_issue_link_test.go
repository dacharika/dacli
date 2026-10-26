package application

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/governance/checkout"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

func TestNativeIssueLinkApplyRechecksRetainedTaskLeaseAfterPreview(t *testing.T) {
	w, err := workspace.Init(t.TempDir(), "issue-link-hold")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProject(w, "a-root", "Project", "p", "goal", ""); err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(w, "a-root", "p", "Task protected after preview", store.TaskOpts{Accept: []string{"keep"}})
	if err != nil {
		t.Fatal(err)
	}
	nativeGit(t, w.Root, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(w.Root, ".git", "info", "exclude"), []byte(".dacli/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.Root, "tracked"), []byte("base"), 0o600); err != nil {
		t.Fatal(err)
	}
	nativeGit(t, w.Root, "add", "tracked")
	nativeGit(t, w.Root, "-c", "user.name=test", "-c", "user.email=test@example.invalid", "commit", "-m", "base")
	observation, err := checkout.Observe(w.Root)
	if err != nil {
		t.Fatal(err)
	}
	admission, err := NewLeaseAdmission(w)
	if err != nil {
		t.Fatal(err)
	}
	private := t.TempDir()
	authorityPath := filepath.Join(private, "authority.json")
	authority, err := sessions.Open(authorityPath, sessions.Binding{Workspace: w.ID, Installation: "installation"}, admission)
	if err != nil {
		t.Fatal(err)
	}
	ownerToken, err := authority.Bootstrap()
	if err != nil {
		t.Fatal(err)
	}
	if err := authority.BindTask(ownerToken, sessions.TaskBinding{TaskID: task.ID, ProjectID: "p", Repository: observation.Repository, AdmissionEnabled: true}); err != nil {
		t.Fatal(err)
	}
	plan, err := store.PlanIssueLinkBatch(w, "p", "owner/repo", []store.IssueLinkTarget{{TaskID: task.ID, Project: "p", IssueNumber: 88, Repository: "owner/repo", IssueDigest: strings.Repeat("a", 64), MappingBlock: "  issue: 88\n  repo: owner/repo", LogEntry: "GitHub issue #88 linked by exact selection; source sha256:" + strings.Repeat("a", 64)}}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	credential := filepath.Join(private, "owner.token")
	if err := os.WriteFile(credential, []byte(ownerToken), 0o600); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(private, "owner.json")
	saveReviewPrivate(t, config, recoveryOwnerConfig{SchemaVersion: 1, Workspace: w.Root, Authority: authorityPath, InstallationID: "installation", OwnerCredentialFile: credential})
	owner, err := OpenNativeIssueLinkOwner(config, "p")
	if err != nil {
		t.Fatal(err)
	}
	// The task has no lease at preview; a native worker acquires one afterward.
	worker, token, err := authority.Enroll(ownerToken, sessions.Scope{Actions: []string{"attempt.register", "lease.acquire"}, Resources: []string{"task:" + task.ID, "checkout:"}}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := authority.RegisterAttempt(token, worker.ID, sessions.AttemptRequest{RequestID: "after-preview", TaskID: task.ID, Checkout: w.Root})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.AcquireLease(token, worker.ID, sessions.AcquireLeaseRequest{OperationKey: "after-preview-hold", AttemptID: attempt.ID, Resources: []sessions.Resource{{Kind: "checkout"}}, ExpiresAt: time.Now().Add(10 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(task.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.Apply(plan); err == nil || !strings.Contains(err.Error(), "native task protection") {
		t.Fatalf("retained lease did not protect task mapping: %v", err)
	}
	after, err := os.ReadFile(task.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("protected task changed")
	}
}

func TestNativeIssueLinkApplyPreservesAcceptanceAndReplays(t *testing.T) {
	w, err := workspace.Init(t.TempDir(), "issue-link-clean")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProject(w, "a-root", "Project", "p", "goal", ""); err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(w, "a-root", "p", "Existing task", store.TaskOpts{Accept: []string{"local criterion"}})
	if err != nil {
		t.Fatal(err)
	}
	task.Doc.SetSection("Acceptance", "- [ ] local criterion\n")
	if err := store.SaveTask(task); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "authority.json")
	authority, err := sessions.Open(path, sessions.Binding{Workspace: w.ID, Installation: "installation"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ownerCredential, err := authority.Bootstrap()
	if err != nil {
		t.Fatal(err)
	}
	private := t.TempDir()
	credential := filepath.Join(private, "owner.token")
	config := filepath.Join(private, "owner.json")
	if err := os.WriteFile(credential, []byte(ownerCredential), 0o600); err != nil {
		t.Fatal(err)
	}
	saveReviewPrivate(t, config, recoveryOwnerConfig{SchemaVersion: 1, Workspace: w.Root, Authority: path, InstallationID: "installation", OwnerCredentialFile: credential})
	owner, err := OpenNativeIssueLinkOwner(config, "p")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := store.PlanIssueLinkBatch(w, "p", "owner/repo", []store.IssueLinkTarget{{TaskID: task.ID, Project: "p", IssueNumber: 88, Repository: "owner/repo", IssueDigest: strings.Repeat("a", 64), MappingBlock: "  issue: 88\n  repo: owner/repo", LogEntry: "GitHub issue #88 linked by exact selection; source sha256:" + strings.Repeat("a", 64)}}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.Apply(plan); err != nil {
		t.Fatalf("unprotected exact task apply: %v", err)
	}
	after, err := store.FindTask(w, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	block, ok := after.Doc.Front.GetBlock("github")
	if !ok || !strings.Contains(block, "issue: 88") || !strings.Contains(block, "repo: owner/repo") {
		t.Fatalf("mapping missing: %q", block)
	}
	if boxes := after.Acceptance(); len(boxes) != 1 || boxes[0].Text != "local criterion" || boxes[0].Done {
		t.Fatalf("issue linking changed local acceptance: %+v", boxes)
	}
	first, err := os.ReadFile(after.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.Apply(plan); err != nil {
		t.Fatalf("replay exact link plan: %v", err)
	}
	second, err := os.ReadFile(after.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) || strings.Count(string(second), "source sha256:"+strings.Repeat("a", 64)) != 1 {
		t.Fatal("idempotent replay changed task bytes or duplicated history")
	}
}
