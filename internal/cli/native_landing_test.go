package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/agentid"
	"github.com/mlnomadpy/dacli/internal/application"
	"github.com/mlnomadpy/dacli/internal/governance/checkout"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

func TestNativeLandingThroughMainRecordsHistoricalSchema10Source(t *testing.T) {
	root := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid"}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s %v", args, out, err)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-b", "main")
	git("config", "gc.auto", "0")
	git("config", "maintenance.auto", "false")
	w, err := workspace.Init(root, "landing-main")
	if err != nil {
		t.Fatal(err)
	}
	write := func(path string, data []byte, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(path, data, mode); err != nil {
			t.Fatal(err)
		}
	}
	privateJSON := func(path string, value any) {
		t.Helper()
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		write(path, raw, 0600)
	}
	write(filepath.Join(root, ".git", "info", "exclude"), []byte(".dacli/\n"), 0600)
	project, err := store.CreateProject(w, "a-root", "Fixture", "p", "goal", "", model.LandingPolicy{Mode: model.LandingPR, Base: "main"})
	if err != nil {
		t.Fatal(err)
	}
	project.Doc.Front.Set("github_repo", "owner/repo")
	if err := store.SaveProject(project); err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(w, "a-root", "p", "Land the native task", store.TaskOpts{Accept: []string{"deliver exact source"}})
	if err != nil {
		t.Fatal(err)
	}
	task.Doc.Front.SetList("claims", []string{"src"})
	if err := store.SaveTask(task); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(root, "src"), []byte("before"), 0600)
	git("add", "src")
	git("commit", "-m", "base")
	observed, err := checkout.Observe(root)
	if err != nil {
		t.Fatal(err)
	}
	private := t.TempDir()
	authorityPath := filepath.Join(private, "authority.json")
	admission, err := application.NewLeaseAdmission(w)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := sessions.Open(authorityPath, sessions.Binding{Workspace: w.ID, Installation: "landing-main"}, admission)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := authority.Bootstrap()
	if err != nil {
		t.Fatal(err)
	}
	for _, upgrade := range []func(string) error{authority.UpgradeSwarms, authority.UpgradeDispatchIntents, authority.UpgradeLifecycle, authority.UpgradeRecovery} {
		if err := upgrade(owner); err != nil {
			t.Fatal(err)
		}
	}
	if err := authority.BindTask(owner, sessions.TaskBinding{TaskID: task.ID, ProjectID: "p", Repository: observed.Repository, AdmissionEnabled: true}); err != nil {
		t.Fatal(err)
	}
	worker, token, err := authority.Enroll(owner, sessions.Scope{Actions: []string{"attempt.register", "lease.acquire", "lease.release", "attempt.publish"}, Resources: []string{"task:" + task.ID, "checkout:" + root, "path:src"}}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := authority.RegisterAttempt(token, worker.ID, sessions.AttemptRequest{RequestID: "landing-main-attempt", TaskID: task.ID, Checkout: root})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := authority.AcquireLease(token, worker.ID, sessions.AcquireLeaseRequest{OperationKey: "lease", AttemptID: attempt.ID, Resources: []sessions.Resource{{Kind: "path", Path: "src"}}, ExpiresAt: time.Now().Add(30 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(root, "src"), []byte("delivered"), 0600)
	git("add", "src")
	git("commit", "-m", "source")
	revision, err := authority.PublishAttemptRevision(token, worker.ID, sessions.PublishRevisionRequest{OperationKey: "publish", AttemptID: attempt.ID, LeaseID: lease.ID, ExpectedFence: lease.Fence})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.ReleaseLease(token, worker.ID, sessions.ReleaseLeaseRequest{OperationKey: "release", AttemptID: attempt.ID, LeaseID: lease.ID, ExpectedFence: lease.Fence}); err != nil {
		t.Fatal(err)
	}
	if err := authority.Revoke(owner, worker.ID); err != nil {
		t.Fatal(err)
	}
	// Structured verification and the service reply are fixture facts; this test
	// proves the actual CLI/owner/source/store path, not a real GitHub merge.
	evidence := store.VerificationEvidence{Command: "fixture validation", Argv: []string{"fixture"}, WorkingDirectory: root, ExitCode: 0, ArtifactHash: "sha256:" + strings.Repeat("a", 64), Verifier: "owner-fixture", CommitSHA: revision.Observation.Commit, TreeSHA: revision.Observation.Tree, Clean: true, RuntimeVersions: map[string]string{"go": "fixture"}, ToolVersions: map[string]string{"git": "fixture"}}
	if err := store.ValidateFinalTreeVerification(evidence, revision.Observation.Commit, revision.Observation.Tree); err != nil {
		t.Fatalf("fixture final-tree evidence is invalid: %v", err)
	}
	if err := store.AppendVerificationEvidence(task, evidence); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveTask(task); err != nil {
		t.Fatal(err)
	}
	credential := filepath.Join(private, "owner.token")
	write(credential, []byte(owner+"\n"), 0600)
	config := filepath.Join(private, "owner.json")
	privateJSON(config, map[string]any{"schema_version": 1, "workspace": root, "authority": authorityPath, "installation_id": "landing-main", "owner_credential_file": credential})
	request := application.NativeLandingRequest{OperationKey: "record-main", TaskID: task.ID, AttemptID: attempt.ID, RevisionID: revision.ID, GitHubRepository: "owner/repo", PRNumber: 1209, ExpectedPRHead: revision.Observation.Commit, IntegratedBaseCommit: attempt.Observation.Commit, IntegratedCommit: revision.Observation.Commit, TargetCommit: revision.Observation.Commit, TargetTree: revision.Observation.Tree}
	requestFile := filepath.Join(private, "request.json")
	privateJSON(requestFile, request)
	pr, err := json.Marshal(map[string]any{"number": 1209, "url": "https://github.com/owner/repo/pull/1209", "state": "MERGED", "headRefOid": revision.Observation.Commit, "baseRefName": "main", "mergeCommit": map[string]string{"oid": revision.Observation.Commit}})
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	write(filepath.Join(bin, "gh"), []byte("#!/bin/sh\ncat <<'DACLI_FIXTURE_JSON'\n"+string(pr)+"\nDACLI_FIXTURE_JSON\n"), 0755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(agentid.EnvVar, "invalid-ambient-legacy-token")
	before, err := os.ReadFile(authorityPath)
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"native", "landing", "record", "--config", config, "--project", "p", "--request", requestFile}
	for i := 0; i < 2; i++ {
		out, code := runMainWithJSON(t, args...)
		if code != 0 {
			t.Fatalf("record/replay through Main: code=%d %s", code, out)
		}
		var receipt store.TaskLandingObservation
		if err := json.Unmarshal([]byte(out), &receipt); err != nil || receipt.TaskID != task.ID || receipt.Source.RevisionID != revision.ID {
			t.Fatalf("record JSON did not bind exact source: %s %v", out, err)
		}
	}
	after, err := os.ReadFile(authorityPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("owner recording changed live authority bytes: %v", err)
	}
	var header struct{ Version int }
	if err := json.Unmarshal(after, &header); err != nil || header.Version != 10 {
		t.Fatalf("fixture did not retain schema10: version=%d %v", header.Version, err)
	}
	current, err := store.FindTask(w, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result, present := store.ReadTaskLanding(context.Background(), w, current, "main"); !present || result.State != store.LandingLanded {
		t.Fatalf("actual CLI result not consumed as landed: %#v present=%t", result, present)
	}
	if out, code := runMainWithJSON(t, "native", "landing", "record", "--unknown-landing-flag"); code != 2 || !strings.Contains(out, "--unknown-landing-flag") {
		t.Fatalf("unknown flag was not rejected by name: code=%d %s", code, out)
	}
	if out, code := runMainWithJSON(t, "native", "landing", "record"); code != 2 || !strings.Contains(out, "--config") {
		t.Fatalf("missing owner flags: code=%d %s", code, out)
	}
	_, message, code := executor(root)(args, true)
	if code != 3 || !strings.Contains(message, "local-only") {
		t.Fatalf("legacy MCP CLI exposed owner recording: code=%d %s", code, message)
	}
	malformed := filepath.Join(private, "unknown-request.json")
	privateJSON(malformed, map[string]any{"operation_key": "unknown-request", "unexpected_owner_override": "must-not-be-printed"})
	if out, code := runMainWithJSON(t, "native", "landing", "record", "--config", config, "--project", "p", "--request", malformed); code != 3 || strings.Contains(out, "must-not-be-printed") {
		t.Fatalf("unknown request field did not fail closed and redacted: code=%d %s", code, out)
	}
}
