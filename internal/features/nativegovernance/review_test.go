package nativegovernance

import (
	"bytes"
	"encoding/json"
	"github.com/mlnomadpy/dacli/internal/application"
	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/mcp/native"
	"github.com/mlnomadpy/dacli/internal/store"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func reviewSetupWorker(t *testing.T) verificationFixture {
	t.Helper()
	f := setupVerification(t, "", true)
	f.task.Doc.SetSection("Acceptance", "- [ ] Named check passes")
	if e := store.SaveTask(f.task); e != nil {
		t.Fatal(e)
	}
	verificationGit(t, f.w.Root, "add", ".")
	verificationGit(t, f.w.Root, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "review checklist")
	rev, e := f.authority.PublishAttemptRevision(f.workerToken, f.worker.SessionID, sessions.PublishRevisionRequest{OperationKey: "review-revision", AttemptID: f.request.AttemptID, LeaseID: f.request.LeaseID, ExpectedFence: f.request.ExpectedFence, ExpectedRevision: 1})
	if e != nil {
		t.Fatal(e)
	}
	f.request.RevisionID = rev.ID
	verificationJSON(t, f.requestFile, f.request)
	return f
}
func reviewerArgs(f verificationFixture, d string) []string {
	return []string{"--config", f.config, "--task", f.task.ID, "--worker-session", f.worker.SessionID, "--runner-config", f.runnerProfile, "--policy-revision", "owner-policy-v1", "--policy-out", filepath.Join(d, "policy.json"), "--review-config", filepath.Join(d, "review.json"), "--connection", filepath.Join(d, "reviewer.json"), "--credential", filepath.Join(d, "reviewer.token")}
}
func TestNativeReviewerProvisioningUsesIndependentRootAndDerivedPolicy(t *testing.T) {
	f := reviewSetupWorker(t)
	before, e := os.ReadFile(f.task.Path)
	if e != nil {
		t.Fatal(e)
	}
	out, e := f.run(t)
	if e != nil {
		t.Fatal(e, out)
	}
	var evidence struct {
		Evidence sessions.Evidence `json:"evidence"`
	}
	if e = json.Unmarshal([]byte(out), &evidence); e != nil {
		t.Fatal(e)
	}
	d := t.TempDir()
	args := reviewerArgs(f, d)
	out, e = verificationRun(t, f.w, reviewerAdd, args...)
	if e != nil {
		t.Fatal(e, out)
	}
	var p application.NativeReviewerProfile
	var c native.Connection
	var policy application.NativeReviewPolicy
	if privateJSON(filepath.Join(d, "review.json"), &p) != nil || privateJSON(filepath.Join(d, "reviewer.json"), &c) != nil || privateJSON(filepath.Join(d, "policy.json"), &policy) != nil {
		t.Fatal("private provisioning output unreadable")
	}
	token, e := native.ReadCredential(c.CredentialFile)
	if e != nil {
		t.Fatal(e)
	}
	reviewer, e := f.authority.Authenticate(token, c.SessionID)
	if e != nil || reviewer.Parent != "" || reviewer.ID == f.worker.SessionID || reviewer.ID == f.runner.RunnerSessionID || strings.Join(reviewer.Scope.Actions, ",") != "read,review.propose,review.evaluate" {
		t.Fatal("reviewer identity or scope unsafe", reviewer, e)
	}
	expected, e := application.NativeReviewPolicyFromChecks(f.w.ID, f.task.Project, f.task.ID, "owner-policy-v1", f.runner.Checks)
	if e != nil {
		t.Fatal(e)
	}
	actual, _ := json.Marshal(policy)
	want, _ := json.Marshal(expected)
	if !bytes.Equal(actual, want) {
		t.Fatal("policy not derived from owner runner", string(actual))
	}
	selected, e := application.OpenNativeReviewProfile(filepath.Join(d, "reviewer.json"), filepath.Join(d, "review.json"))
	if e != nil {
		t.Fatal(e)
	}
	access, e := selected.Authenticate()
	if e != nil {
		t.Fatal(e)
	}
	request := application.NativeReviewRequest{OperationKey: "propose", AttemptID: f.request.AttemptID, LeaseID: f.request.LeaseID, RevisionID: f.request.RevisionID, ExpectedFence: f.request.ExpectedFence, Outcome: "approve", Rationale: "Reviewed committed implementation and configured check", EvidenceIDs: []string{evidence.Evidence.ID}}
	review, e := access.Propose(request)
	if e != nil {
		t.Fatal(e)
	}
	eligibility, e := access.Evaluate(review.ID)
	if e != nil || !eligibility.Eligible || eligibility.Assurance != "cooperative-credential-separation" {
		t.Fatal(eligibility, e)
	}
	again, e := access.Propose(request)
	if e != nil || again.ID != review.ID {
		t.Fatal("proposal replay failed", again, e)
	}
	after, e := os.ReadFile(f.task.Path)
	if e != nil || !bytes.Equal(before, after) {
		t.Fatal("review changed canonical task")
	}
	entries, e := os.ReadDir(f.w.RunsDir())
	if !os.IsNotExist(e) && (e != nil || len(entries) != 0) {
		t.Fatal("review fabricated agent process", entries, e)
	}
	for _, name := range []string{"policy.json", "review.json", "reviewer.json", "reviewer.token"} {
		info, e := os.Stat(filepath.Join(d, name))
		if e != nil || info.Mode().Perm() != 0600 {
			t.Fatal("review output not private", name, e)
		}
	}
	for _, secret := range []string{token, f.owner, f.workerToken, f.runner.Checks[0].Command} {
		if strings.Contains(out, secret) {
			t.Fatal("provisioning output leaked private material")
		}
	}
}
func TestNativeReviewerMalformedInputsAndOccupiedOutputsPreserveAuthority(t *testing.T) {
	for _, kind := range []string{"owner", "runner-unknown", "runner-command", "runner-repository", "policy-revision", "no-checklist", "occupied-policy", "occupied-profile", "occupied-connection", "occupied-credential"} {
		t.Run(kind, func(t *testing.T) {
			f := reviewSetupWorker(t)
			d := t.TempDir()
			args := reviewerArgs(f, d)
			switch kind {
			case "owner":
				c, _ := config(f.config)
				_ = os.WriteFile(c.OwnerCredentialFile, []byte("forged"), 0600)
			case "runner-unknown":
				b, _ := os.ReadFile(f.runnerProfile)
				b = append(b[:len(b)-1], []byte(`,"issuer":"caller"}`)...)
				_ = os.WriteFile(f.runnerProfile, b, 0600)
			case "runner-command":
				f.runner.Checks[0].Command = ""
				verificationJSON(t, f.runnerProfile, f.runner)
			case "runner-repository":
				f.runner.Repository = d
				verificationJSON(t, f.runnerProfile, f.runner)
			case "policy-revision":
				for i := range args {
					if args[i] == "--policy-revision" {
						args[i+1] = "../policy"
					}
				}
			case "no-checklist":
				f.task.Doc.SetSection("Acceptance", "")
				if e := store.SaveTask(f.task); e != nil {
					t.Fatal(e)
				}
			default:
				name := map[string]string{"occupied-policy": "policy.json", "occupied-profile": "review.json", "occupied-connection": "reviewer.json", "occupied-credential": "reviewer.token"}[kind]
				if e := os.WriteFile(filepath.Join(d, name), []byte("preserve"), 0600); e != nil {
					t.Fatal(e)
				}
			}
			before, e := os.ReadFile(f.worker.Authority)
			if e != nil {
				t.Fatal(e)
			}
			out, e := verificationRun(t, f.w, reviewerAdd, args...)
			if clikit.ExitCode(e) != 3 {
				t.Fatal("owner setup did not refuse", e, out)
			}
			after, e := os.ReadFile(f.worker.Authority)
			if e != nil || !bytes.Equal(before, after) {
				t.Fatal("preissuance refusal changed authority")
			}
			entries, e := os.ReadDir(d)
			if e != nil {
				t.Fatal(e)
			}
			if strings.HasPrefix(kind, "occupied-") {
				if len(entries) != 1 {
					t.Fatal("refusal left new outputs", entries)
				}
				b, _ := os.ReadFile(filepath.Join(d, entries[0].Name()))
				if string(b) != "preserve" {
					t.Fatal("existing private output changed")
				}
			} else if len(entries) != 0 {
				t.Fatal("preissuance refusal left files", entries)
			}
		})
	}
}
func TestNativeReviewerFailedGrantPreservesIssuedRecovery(t *testing.T) {
	f := reviewSetupWorker(t)
	d := t.TempDir()
	args := reviewerArgs(f, d)
	for i := range args {
		if args[i] == "--worker-session" {
			args[i+1] = "nonexistent-worker"
		}
	}
	before, e := os.ReadFile(f.worker.Authority)
	if e != nil {
		t.Fatal(e)
	}
	out, e := verificationRun(t, f.w, reviewerAdd, args...)
	if clikit.ExitCode(e) != 3 || !strings.Contains(out, "issued; preserve private files") {
		t.Fatal("partial issuance recovery missing", out, e)
	}
	after, e := os.ReadFile(f.worker.Authority)
	if e != nil || bytes.Equal(before, after) {
		t.Fatal("issued authority was reset or missing")
	}
	var p application.NativeReviewerProfile
	if privateJSON(filepath.Join(d, "review.json"), &p) != nil || p.ReviewerSessionID == "" || p.GrantID != "" {
		t.Fatal("incomplete recovery profile missing", p)
	}
	if _, e := application.OpenNativeReviewProfile(filepath.Join(d, "reviewer.json"), filepath.Join(d, "review.json")); e == nil {
		t.Fatal("incomplete profile admitted")
	}
	for _, name := range []string{"policy.json", "review.json", "reviewer.json", "reviewer.token"} {
		if _, e := os.Stat(filepath.Join(d, name)); e != nil {
			t.Fatal("issued recovery artifact removed", name, e)
		}
	}
	if e = f.authority.Revoke(f.owner, p.ReviewerSessionID); e != nil {
		t.Fatal("owner recovery revoke failed", e)
	}
}
