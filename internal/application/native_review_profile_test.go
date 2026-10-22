package application

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/governance/nativeconfig"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/store"
)

type reviewProfileFixture struct {
	nativeFixture
	selected                                *NativeReviewProfile
	profile                                 NativeReviewerProfile
	policy                                  NativeReviewPolicy
	connection                              nativeconfig.Connection
	connectionPath, profilePath, policyPath string
	reviewer                                sessions.Session
	token                                   string
	request                                 NativeReviewRequest
}

func saveReviewPrivate(t *testing.T, path string, v any) {
	t.Helper()
	b, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(path, b, 0600); e != nil {
		t.Fatal(e)
	}
}
func reviewProfileSetup(t *testing.T) reviewProfileFixture {
	t.Helper()
	f := nativeSetup(t, "printf checked")
	// Place canonical workspace beside the actual external checkout, matching the
	// owner provisioning contract without inventing process evidence or authority.
	dir := f.attempt.Observation.Locator
	if e := os.Rename(filepath.Join(f.workspace.Root, ".dacli"), filepath.Join(dir, ".dacli")); e != nil {
		t.Fatal("move canonical workspace", e)
	}
	f.workspace.Root = dir
	if e := os.WriteFile(filepath.Join(dir, ".git", "info", "exclude"), []byte(".dacli/\n"), 0600); e != nil {
		t.Fatal("exclude canonical metadata", e)
	}
	task, e := store.FindTask(f.workspace, f.attempt.Request.TaskID)
	if e != nil {
		t.Fatal("find canonical task", e)
	}
	task.Doc.SetSection("Acceptance", "- [ ] Named check passes")
	if e = store.SaveTask(task); e != nil {
		t.Fatal("save acceptance checklist", e)
	}
	private := t.TempDir()
	pp := filepath.Join(private, "policy.json")
	policy, e := NativeReviewPolicyFromChecks(f.workspace.ID, "p", task.ID, "policy-v1", []ConfiguredCheck{{ID: f.check.ID, Command: f.check.Command, Deadline: f.check.Deadline.String(), GrantID: f.grant.ID}})
	if e != nil {
		t.Fatal("derive owner policy", e)
	}
	saveReviewPrivate(t, pp, policy)
	a, _, e := OpenNativeReviewAuthority(dir, f.workspace.ID, f.path, "installation", pp)
	if e != nil {
		t.Fatal("open review authority", e)
	}
	reviewer, token, e := a.Enroll(f.owner, sessions.Scope{Actions: []string{"read", "review.propose", "review.evaluate"}, Resources: []string{"workspace:" + f.workspace.ID, "task:" + task.ID}}, time.Now().Add(time.Hour))
	if e != nil {
		t.Fatal("enroll reviewer", e)
	}
	grant, e := a.GrantReview(f.owner, sessions.ReviewGrantRequest{ReviewerSession: reviewer.ID, WorkerSession: f.worker.ID, TaskID: task.ID, Repository: f.attempt.Observation.Repository, ExpiresAt: reviewer.ExpiresAt})
	if e != nil {
		t.Fatal("grant review", e)
	}
	credential := filepath.Join(private, "reviewer.token")
	if e = os.WriteFile(credential, []byte(token), 0600); e != nil {
		t.Fatal("write reviewer credential", e)
	}
	c := nativeconfig.Connection{SchemaVersion: 1, Workspace: dir, WorkspaceID: f.workspace.ID, Authority: f.path, InstallationID: "installation", SessionID: reviewer.ID, CredentialFile: credential}
	p := NativeReviewerProfile{SchemaVersion: 1, Workspace: dir, WorkspaceID: f.workspace.ID, Authority: f.path, InstallationID: "installation", ProjectID: "p", TaskID: task.ID, Repository: f.attempt.Observation.Repository, ReviewerSessionID: reviewer.ID, WorkerSessionID: f.worker.ID, GrantID: grant.ID, CredentialFile: credential, PolicyFile: pp}
	cp := filepath.Join(private, "connection.json")
	rp := filepath.Join(private, "review.json")
	saveReviewPrivate(t, cp, c)
	saveReviewPrivate(t, rp, p)
	selected, e := OpenNativeReviewProfile(cp, rp)
	if e != nil {
		t.Fatal("open private review profile", e)
	}
	ev, e := f.executor.Run(context.Background(), f.workerToken, f.request)
	if e != nil {
		t.Fatal("execute configured verification", e)
	}
	req := NativeReviewRequest{OperationKey: "review", AttemptID: f.attempt.ID, LeaseID: f.lease.ID, RevisionID: f.revision.ID, ExpectedFence: f.lease.Fence, Outcome: "approve", Rationale: "Named check and exact revision reviewed", EvidenceIDs: []string{ev.ID}}
	return reviewProfileFixture{f, selected, p, policy, c, cp, rp, pp, reviewer, token, req}
}
func TestNativeReviewProfileFreshPolicyAndHistoricalReplay(t *testing.T) {
	f := reviewProfileSetup(t)
	access, e := f.selected.Authenticate()
	if e != nil {
		t.Fatal(e)
	}
	task, e := store.FindTask(f.workspace, f.profile.TaskID)
	if e != nil {
		t.Fatal(e)
	}
	before, e := os.ReadFile(task.Path)
	if e != nil {
		t.Fatal(e)
	}
	review, e := access.Propose(f.request)
	if e != nil {
		t.Fatal(e)
	}
	eligible, e := access.Evaluate(review.ID)
	if e != nil || !eligible.Eligible {
		t.Fatal(eligible, e)
	}
	f.policy.Revision = "policy-v2"
	saveReviewPrivate(t, f.policyPath, f.policy)
	// The same access must observe changed policy; a loader-only reread is not enough.
	eligible, e = access.Evaluate(review.ID)
	if e != nil || eligible.Eligible || eligible.Reason != sessions.ReviewPolicyChanged {
		t.Fatalf("fresh policy not observed: %+v %v", eligible, e)
	}
	fresh, e := f.selected.Authenticate()
	if e != nil {
		t.Fatal("valid changed owner policy refused at loader", e)
	}
	replay, e := fresh.Propose(f.request)
	if e != nil || replay.ID != review.ID {
		t.Fatal("historical replay became current-policy check", e)
	}
	after, e := os.ReadFile(task.Path)
	if e != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("review changed canonical task", e)
	}
	if e = f.authority.Revoke(f.owner, f.reviewer.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = f.selected.Authenticate(); !errors.Is(e, sessions.ErrDenied) {
		t.Fatal("revoked reviewer authenticated", e)
	}
	if _, e = access.Evaluate(review.ID); !errors.Is(e, sessions.ErrDenied) {
		t.Fatal("cached access retained revoked authority", e)
	}
}
func TestNativeReviewProfileRejectsSelectionAndIdentitySubstitution(t *testing.T) {
	f := reviewProfileSetup(t)
	for _, field := range []string{"grant", "worker", "policy", "repository"} {
		t.Run(field, func(t *testing.T) {
			p := f.profile
			switch field {
			case "grant":
				p.GrantID = "other"
			case "worker":
				p.WorkerSessionID = "other"
			case "policy":
				p.PolicyFile = f.profilePath
			case "repository":
				p.Repository = t.TempDir()
			}
			saveReviewPrivate(t, f.profilePath, p)
			if _, e := f.selected.Authenticate(); !errors.Is(e, sessions.ErrDenied) {
				t.Fatalf("changed %s profile accepted: %v", field, e)
			}
			saveReviewPrivate(t, f.profilePath, f.profile)
		})
	}
	t.Run("forged-token", func(t *testing.T) {
		if e := os.WriteFile(f.connection.CredentialFile, []byte("forged"), 0600); e != nil {
			t.Fatal(e)
		}
		if _, e := f.selected.Authenticate(); !errors.Is(e, sessions.ErrDenied) {
			t.Fatal("forged reviewer accepted", e)
		}
		if e := os.WriteFile(f.connection.CredentialFile, []byte(f.token), 0600); e != nil {
			t.Fatal(e)
		}
	})
	t.Run("worker-as-reviewer", func(t *testing.T) {
		c := f.connection
		p := f.profile
		c.SessionID = f.worker.ID
		p.ReviewerSessionID = f.worker.ID
		p.WorkerSessionID = "different-label"
		saveReviewPrivate(t, f.connectionPath, c)
		saveReviewPrivate(t, f.profilePath, p)
		if e := os.WriteFile(c.CredentialFile, []byte(f.workerToken), 0600); e != nil {
			t.Fatal(e)
		}
		if _, e := OpenNativeReviewProfile(f.connectionPath, f.profilePath); !errors.Is(e, sessions.ErrDenied) {
			t.Fatal("implementation authority accepted as reviewer", e)
		}
	})
}
func TestNativeReviewPolicyBindsCommandAndDeadline(t *testing.T) {
	check := ConfiguredCheck{ID: "unit", Command: "printf check", Deadline: "2s", GrantID: "grant"}
	base, e := NativeReviewPolicyFromChecks("workspace", "project", "task", "policy", []ConfiguredCheck{check})
	if e != nil {
		t.Fatal(e)
	}
	for _, mode := range []string{"command", "deadline"} {
		changed := check
		if mode == "command" {
			changed.Command = "printf changed"
		} else {
			changed.Deadline = "3s"
		}
		p, e := NativeReviewPolicyFromChecks("workspace", "project", "task", "policy", []ConfiguredCheck{changed})
		if e != nil || p.Checks[0].CommandDigest == base.Checks[0].CommandDigest {
			t.Fatalf("%s omitted from policy digest", mode)
		}
	}
	if _, e = NativeReviewPolicyFromChecks("workspace", "project", "task", "policy", []ConfiguredCheck{check, check}); !errors.Is(e, sessions.ErrDenied) {
		t.Fatal("duplicate checks accepted", e)
	}
	if _, _, e = base.ReviewPolicy("wrong", "project", "task"); !errors.Is(e, sessions.ErrDenied) {
		t.Fatal("policy tuple substitution accepted", e)
	}
}
