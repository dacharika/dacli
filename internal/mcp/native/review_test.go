package native

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/mlnomadpy/dacli/internal/application"
	"github.com/mlnomadpy/dacli/internal/governance/checkout"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/store"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type reviewFixture struct {
	worker                                  fixture
	server                                  *Server
	policyPath, profilePath, connectionPath string
	profile                                 application.NativeReviewerProfile
	policy                                  application.NativeReviewPolicy
	request                                 application.NativeReviewRequest
	authority                               *sessions.Authority
	token                                   string
	evidence                                sessions.Evidence
}

func setupNativeReview(t *testing.T) reviewFixture {
	t.Helper()
	f := setup(t)
	f.task.Doc.SetSection("Acceptance", "- [ ] Owner check passes")
	if e := store.SaveTask(f.task); e != nil {
		t.Fatal(e)
	}
	git(t, f.c.Workspace, "add", ".")
	git(t, f.c.Workspace, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "acceptance")
	old, e := f.a.Authenticate(f.token, f.c.SessionID)
	if e != nil {
		t.Fatal(e)
	}
	scope := old.Scope
	scope.Actions = append(scope.Actions, "verification.request")
	worker, token, e := f.a.Enroll(f.owner, scope, time.Now().Add(time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	f.token = token
	f.c.SessionID = worker.ID
	if e = os.WriteFile(f.c.CredentialFile, []byte(token), 0600); e != nil {
		t.Fatal(e)
	}
	save(t, f.s.path, f.c)
	f.s, e = Open(f.s.path)
	if e != nil {
		t.Fatal(e)
	}
	attempt := call(t, f, "register_attempt", registerArgs{version{1}, sessions.AttemptRequest{RequestID: "review-attempt", TaskID: f.task.ID, Checkout: f.c.Workspace}}).(sessions.Attempt)
	lease := call(t, f, "acquire_lease", acquireArgs{version{1}, sessions.AcquireLeaseRequest{OperationKey: "review-lease", AttemptID: attempt.ID, Resources: []sessions.Resource{{Kind: "path", Path: "src"}}, ExpiresAt: time.Now().Add(5 * time.Minute)}}).(sessions.Lease)
	rev := call(t, f, "publish_revision", publishArgs{version{1}, sessions.PublishRevisionRequest{OperationKey: "review-revision", AttemptID: attempt.ID, LeaseID: lease.ID, ExpectedFence: lease.Fence}}).(sessions.AttemptRevision)
	obs, e := checkout.Observe(f.c.Workspace)
	if e != nil {
		t.Fatal(e)
	}
	runner, runnerToken, e := f.a.Enroll(f.owner, sessions.Scope{Actions: []string{"verification.run"}, Resources: []string{"task:" + f.task.ID}}, time.Now().Add(time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	marker := filepath.Join(t.TempDir(), "checked")
	check := sessions.CheckDescriptor{ID: "unit", Command: "printf checked > '" + marker + "'; printf verified", Deadline: 4 * time.Second}
	grant, e := f.a.GrantVerification(f.owner, runner.ID, f.task.ID, obs.Repository, check, time.Now().Add(time.Minute))
	if e != nil {
		t.Fatal(e)
	}
	executor, e := application.NewNativeVerificationRunner(f.a, runnerToken, runner.ID, []application.NativeCheck{{Descriptor: check, GrantID: grant.ID}})
	if e != nil {
		t.Fatal(e)
	}
	evidence, e := executor.Run(context.Background(), f.token, application.NativeVerificationRequest{OperationKey: "review-check", WorkerSession: worker.ID, AttemptID: attempt.ID, LeaseID: lease.ID, RevisionID: rev.ID, CheckID: "unit", ExpectedFence: lease.Fence})
	if e != nil {
		t.Fatal(e)
	}
	if b, e := os.ReadFile(marker); e != nil || string(b) != "checked" {
		t.Fatal("trusted check did not run", e, string(b))
	}
	private := t.TempDir()
	policyPath := filepath.Join(private, "policy.json")
	profilePath := filepath.Join(private, "review.json")
	connectionPath := filepath.Join(private, "connection.json")
	credential := filepath.Join(private, "reviewer.token")
	policy, e := application.NativeReviewPolicyFromChecks(f.c.WorkspaceID, f.task.Project, f.task.ID, "policy-v1", []application.ConfiguredCheck{{ID: "unit", Command: check.Command, Deadline: "4s", GrantID: grant.ID}})
	if e != nil {
		t.Fatal(e)
	}
	save(t, policyPath, policy)
	authority, _, e := application.OpenNativeReviewAuthority(f.c.Workspace, f.c.WorkspaceID, f.c.Authority, f.c.InstallationID, policyPath)
	if e != nil {
		t.Fatal(e)
	}
	reviewer, reviewerToken, e := authority.Enroll(f.owner, sessions.Scope{Actions: []string{"read", "review.propose", "review.evaluate"}, Resources: []string{"workspace:" + f.c.WorkspaceID, "task:" + f.task.ID}}, time.Now().Add(time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	reviewGrant, e := authority.GrantReview(f.owner, sessions.ReviewGrantRequest{ReviewerSession: reviewer.ID, WorkerSession: worker.ID, TaskID: f.task.ID, Repository: obs.Repository, ExpiresAt: time.Now().Add(time.Minute)})
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(credential, []byte(reviewerToken), 0600); e != nil {
		t.Fatal(e)
	}
	connection := f.c
	connection.SessionID = reviewer.ID
	connection.CredentialFile = credential
	save(t, connectionPath, connection)
	profile := application.NativeReviewerProfile{SchemaVersion: 1, Workspace: f.c.Workspace, WorkspaceID: f.c.WorkspaceID, Authority: f.c.Authority, InstallationID: f.c.InstallationID, ProjectID: f.task.Project, TaskID: f.task.ID, Repository: obs.Repository, ReviewerSessionID: reviewer.ID, WorkerSessionID: worker.ID, GrantID: reviewGrant.ID, CredentialFile: credential, PolicyFile: policyPath}
	save(t, profilePath, profile)
	server, e := OpenWithProfiles(connectionPath, "", profilePath)
	if e != nil {
		t.Fatal(e)
	}
	request := application.NativeReviewRequest{OperationKey: "review-proposal", AttemptID: attempt.ID, LeaseID: lease.ID, RevisionID: rev.ID, ExpectedFence: lease.Fence, Outcome: "approve", Rationale: "Inspected committed implementation and independent check", EvidenceIDs: []string{evidence.ID}}
	return reviewFixture{f, server, policyPath, profilePath, connectionPath, profile, policy, request, authority, reviewerToken, evidence}
}
func (f reviewFixture) propose(t *testing.T) sessions.Review {
	t.Helper()
	b, _ := json.Marshal(reviewArgs{version{1}, f.request})
	v, e := f.server.call("propose_review", b)
	if e != nil {
		t.Fatal(e)
	}
	encoded, _ := json.Marshal(result("propose_review", v, nil))
	if !strings.Contains(string(encoded), `"isError":false`) {
		t.Fatal("review response invalid schema", string(encoded))
	}
	return v.(sessions.Review)
}
func (f reviewFixture) evaluate(t *testing.T, id string) sessions.ReviewEligibility {
	t.Helper()
	b, _ := json.Marshal(evaluateReviewArgs{version{1}, id})
	v, e := f.server.call("evaluate_review", b)
	if e != nil {
		t.Fatal(e)
	}
	return v.(sessions.ReviewEligibility)
}
func TestNativeReviewerModeCatalogueAndProtocol(t *testing.T) {
	f := setupNativeReview(t)
	before, e := os.ReadFile(f.worker.task.Path)
	if e != nil {
		t.Fatal(e)
	}
	got := []string{}
	for _, tool := range f.server.catalog() {
		got = append(got, tool.Name)
	}
	want := []string{"identity", "workspace_status", "task_list", "task_get", "propose_review", "evaluate_review"}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("review mode widened", got)
	}
	for _, name := range []string{"register_attempt", "acquire_lease", "publish_revision", "submit_evidence", "request_verification", "get_verification_status", "cancel_verification", "accept", "grant", "shell"} {
		if _, e := f.server.call(name, []byte(`{"schema_version":1}`)); e == nil {
			t.Fatal("reviewer exposed writer", name)
		}
	}
	if _, e := OpenWithProfiles(f.connectionPath, f.profilePath, f.profilePath); e == nil {
		t.Fatal("mixed owner profiles accepted")
	}
	for _, name := range []string{"propose_review", "evaluate_review"} {
		if _, e := f.worker.s.call(name, []byte(`{"schema_version":1}`)); e == nil {
			t.Fatal("default worker exposed review", name)
		}
	}
	review := f.propose(t)
	if review.IssuerSession != f.profile.ReviewerSessionID || review.Request.GrantID != f.profile.GrantID || review.Revision.ID != f.request.RevisionID {
		t.Fatal("caller forged review identity", review)
	}
	if eligibility := f.evaluate(t, review.ID); !eligibility.Eligible || eligibility.Assurance != "cooperative-credential-separation" {
		t.Fatal(eligibility)
	}
	request, _ := json.Marshal(reviewArgs{version{1}, f.request})
	evaluate, _ := json.Marshal(evaluateReviewArgs{version{1}, review.ID})
	input := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{}}}` + "\n"
	for i, item := range []struct {
		name string
		args []byte
	}{{"propose_review", request}, {"evaluate_review", evaluate}} {
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": i + 2, "method": "tools/call", "params": map[string]any{"name": item.name, "arguments": json.RawMessage(item.args)}})
		input += string(b) + "\n"
	}
	var out bytes.Buffer
	if e = f.server.Serve(strings.NewReader(input), &out); e != nil {
		t.Fatal(e)
	}
	decoder := json.NewDecoder(&out)
	responses := 0
	for decoder.More() {
		var response struct {
			Result struct {
				IsError    bool            `json:"isError"`
				Structured json.RawMessage `json:"structuredContent"`
			} `json:"result"`
		}
		if e = decoder.Decode(&response); e != nil {
			t.Fatal(e)
		}
		if response.Result.IsError {
			t.Fatal("protocol refused typed review")
		}
		responses++
	}
	if responses != 3 {
		t.Fatal("missing independent responses", responses)
	}
	after, e := os.ReadFile(f.worker.task.Path)
	if e != nil || !bytes.Equal(before, after) {
		t.Fatal("proposal accepted or changed task")
	}
	runs, e := os.ReadDir(filepath.Join(f.worker.c.Workspace, ".dacli", "runs"))
	if !os.IsNotExist(e) && (e != nil || len(runs) != 0) {
		t.Fatal("review created provider record", runs, e)
	}
}
func TestNativeReviewToolOverridesAreRejected(t *testing.T) {
	f := setupNativeReview(t)
	for _, key := range []string{"grant_id", "worker_session", "reviewer_session", "policy_file", "command", "credential", "provenance"} {
		b, _ := json.Marshal(reviewArgs{version{1}, f.request})
		var arguments map[string]any
		_ = json.Unmarshal(b, &arguments)
		arguments["request"].(map[string]any)[key] = "caller"
		raw, _ := json.Marshal(arguments)
		if _, e := f.server.call("propose_review", raw); e == nil {
			t.Fatal("tool override accepted", key)
		}
	}
	for _, key := range []string{"credential", "grant_id", "policy_file", "session_id"} {
		raw, _ := json.Marshal(map[string]any{"schema_version": 1, "review_id": "unknown", "ignored": key})
		if _, e := f.server.call("evaluate_review", raw); e == nil {
			t.Fatal("evaluate override accepted", key)
		}
	}
}
func TestNativeReviewHistoricalReplayAndFreshEligibility(t *testing.T) {
	for _, kind := range []string{"policy", "checklist", "dirty", "released-lease", "expired-lease", "grant-revoked", "worker-revoked", "runner-revoked", "new-revision"} {
		t.Run(kind, func(t *testing.T) {
			f := setupNativeReview(t)
			review := f.propose(t)
			switch kind {
			case "policy":
				f.policy.Revision = "policy-v2"
				save(t, f.policyPath, f.policy)
			case "checklist":
				f.worker.task.Doc.SetSection("Acceptance", "- [ ] Changed criterion")
				if e := store.SaveTask(f.worker.task); e != nil {
					t.Fatal(e)
				}
			case "dirty":
				if e := os.WriteFile(filepath.Join(f.worker.c.Workspace, "dirty"), []byte("changed"), 0600); e != nil {
					t.Fatal(e)
				}
			case "released-lease":
				call(t, f.worker, "release_lease", releaseArgs{version{1}, sessions.ReleaseLeaseRequest{OperationKey: "release-after-review", AttemptID: f.request.AttemptID, LeaseID: f.request.LeaseID, ExpectedFence: f.request.ExpectedFence}})
			case "expired-lease":
				renewed := call(t, f.worker, "renew_lease", renewArgs{version{1}, sessions.RenewLeaseRequest{OperationKey: "expire-after-review", AttemptID: f.request.AttemptID, LeaseID: f.request.LeaseID, ExpectedFence: f.request.ExpectedFence, ExpiresAt: time.Now().Add(2 * time.Second)}}).(sessions.Lease)
				time.Sleep(time.Until(renewed.ExpiresAt) + 10*time.Millisecond)
			case "worker-revoked":
				if e := f.authority.Revoke(f.worker.owner, f.worker.c.SessionID); e != nil {
					t.Fatal(e)
				}
			case "runner-revoked":
				if e := f.authority.Revoke(f.worker.owner, f.evidence.IssuerSession); e != nil {
					t.Fatal(e)
				}
			case "new-revision":
				git(t, f.worker.c.Workspace, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "new revision")
				call(t, f.worker, "publish_revision", publishArgs{version{1}, sessions.PublishRevisionRequest{OperationKey: "new-review-revision", AttemptID: f.request.AttemptID, LeaseID: f.request.LeaseID, ExpectedFence: f.request.ExpectedFence, ExpectedRevision: 1}})
			case "grant-revoked":
				if e := f.authority.RevokeReview(f.worker.owner, f.profile.GrantID); e != nil {
					t.Fatal(e)
				}
			}
			if again := f.propose(t); again.ID != review.ID {
				t.Fatal("immutable historical proposal lost")
			}
			eligibility := f.evaluate(t, review.ID)
			// Grant independence requires a current worker root and is checked before
			// worker lease validity, so worker revocation invalidates review authority.
			expected := map[string]sessions.ReviewReason{"policy": sessions.ReviewPolicyChanged, "checklist": sessions.ReviewPolicyChanged, "dirty": sessions.ReviewRevisionStale, "released-lease": sessions.ReviewWorkerInvalid, "expired-lease": sessions.ReviewWorkerInvalid, "grant-revoked": sessions.ReviewAuthorityInvalid, "worker-revoked": sessions.ReviewAuthorityInvalid, "runner-revoked": sessions.ReviewEvidenceInvalid, "new-revision": sessions.ReviewRevisionStale}[kind]
			if eligibility.Eligible || eligibility.Reason != expected {
				t.Fatal("stale review eligibility/reason incorrect", kind, eligibility, expected)
			}
		})
	}
}
func TestNativeReviewRejectsUntrustedEvidenceAndRevokedReviewer(t *testing.T) {
	for _, kind := range []string{"self-report", "stale-fence", "wrong-worker", "reviewer-revoked"} {
		t.Run(kind, func(t *testing.T) {
			f := setupNativeReview(t)
			switch kind {
			case "self-report":
				evidence := call(t, f.worker, "submit_evidence", evidenceArgs{version{1}, sessions.EvidenceRequest{OperationKey: "self-report", AttemptID: f.request.AttemptID, LeaseID: f.request.LeaseID, RevisionID: f.request.RevisionID, ExpectedFence: f.request.ExpectedFence, CheckID: "unit", OutputDigest: sessions.OutputDigest([]byte("claimed"))}}).(sessions.Evidence)
				f.request.EvidenceIDs = []string{evidence.ID}
			case "stale-fence":
				f.request.ExpectedFence++
			case "wrong-worker":
				f.request.AttemptID = "other-attempt"
			case "reviewer-revoked":
				if e := f.authority.Revoke(f.worker.owner, f.profile.ReviewerSessionID); e != nil {
					t.Fatal(e)
				}
			}
			b, _ := json.Marshal(reviewArgs{version{1}, f.request})
			if _, e := f.server.call("propose_review", b); e == nil {
				t.Fatal("ineligible review proposal accepted", kind)
			}
		})
	}
}

func TestNativeReviewerProfileCannotReuseWorkerIdentity(t *testing.T) {
	f := setupNativeReview(t)
	var c Connection
	if b, e := ReadPrivate(f.connectionPath, 16384); e != nil || Decode(b, &c) != nil {
		t.Fatal("connection unreadable", e)
	}
	c.SessionID = f.worker.c.SessionID
	c.CredentialFile = f.worker.c.CredentialFile
	f.profile.ReviewerSessionID = c.SessionID
	f.profile.CredentialFile = c.CredentialFile
	save(t, f.connectionPath, c)
	save(t, f.profilePath, f.profile)
	if _, e := OpenWithProfiles(f.connectionPath, "", f.profilePath); e == nil {
		t.Fatal("shared worker/reviewer credential accepted")
	}
}
