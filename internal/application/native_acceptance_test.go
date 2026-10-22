package application

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/mlnomadpy/dacli/internal/governance/checkout"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/workspace"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type acceptanceFixture struct {
	w                                          *workspace.Workspace
	a                                          *sessions.Authority
	owner, workerToken, reviewerToken          string
	worker, reviewer                           sessions.Session
	at                                         sessions.Attempt
	lease                                      sessions.Lease
	revision                                   sessions.AttemptRevision
	grant                                      sessions.ReviewGrant
	input                                      sessions.NativeAcceptanceInput
	access                                     *NativeAcceptanceOwner
	task                                       *store.Task
	runnerToken, runnerID, verificationGrantID string
	check                                      sessions.CheckDescriptor
	marker                                     string
}

func acceptanceSetup(t *testing.T) acceptanceFixture { return acceptanceSetupMode(t, false) }
func acceptanceSetupMode(t *testing.T, tracked bool) acceptanceFixture {
	t.Helper()
	w, _ := readFixture(t)
	nativeGit(t, w.Root, "init", "-b", "main")
	if e := os.WriteFile(filepath.Join(w.Root, ".git", "info", "exclude"), []byte(".dacli/\n"), 0600); e != nil {
		t.Fatalf("setup/read line 38: %v", e)
	}
	if _, e := store.CreateProject(w, "a-root", "Fixture", "p", "goal", ""); e != nil {
		t.Fatalf("setup/read line 41: %v", e)
	}
	task, e := store.CreateTask(w, "a-root", "p", "Implement native acceptance", store.TaskOpts{Accept: []string{"exact command passes", "independent review approves"}})
	if e != nil {
		t.Fatalf("setup/read line 45: %v", e)
	}
	if e = os.WriteFile(filepath.Join(w.Root, "src"), []byte("base"), 0600); e != nil {
		t.Fatalf("setup/read line 48: %v", e)
	}
	nativeGit(t, w.Root, "add", "src")
	if tracked {
		nativeGit(t, w.Root, "add", "-f", task.Path)
	}
	nativeGit(t, w.Root, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "base")
	admission, e := NewLeaseAdmission(w)
	if e != nil {
		t.Fatalf("setup/read line 54: %v", e)
	}
	private := t.TempDir()
	path := filepath.Join(private, "authority.json")
	a, e := sessions.Open(path, sessions.Binding{Workspace: w.ID, Installation: "installation"}, admission)
	if e != nil {
		t.Fatalf("setup/read line 60: %v", e)
	}
	owner, e := a.Bootstrap()
	if e != nil {
		t.Fatalf("setup/read line 64: %v", e)
	}
	obs, e := checkout.Observe(w.Root)
	if e != nil {
		t.Fatalf("setup/read line 68: %v", e)
	}
	if e = a.BindTask(owner, sessions.TaskBinding{TaskID: task.ID, ProjectID: "p", Repository: obs.Repository, AdmissionEnabled: true}); e != nil {
		t.Fatalf("setup/read line 71: %v", e)
	}
	worker, wt, e := a.Enroll(owner, sessions.Scope{Actions: []string{"attempt.register", "lease.acquire", "lease.renew", "lease.release", "attempt.publish", "verification.request", "evidence.submit"}, Resources: []string{"task:" + task.ID, "checkout:" + w.Root, "path:src"}}, time.Now().Add(time.Hour))
	if e != nil {
		t.Fatalf("setup/read line 75: %v", e)
	}
	at, e := a.RegisterAttempt(wt, worker.ID, sessions.AttemptRequest{RequestID: "implementation", TaskID: task.ID, Checkout: w.Root})
	if e != nil {
		t.Fatalf("setup/read line 79: %v", e)
	}
	lease, e := a.AcquireLease(wt, worker.ID, sessions.AcquireLeaseRequest{OperationKey: "lease", AttemptID: at.ID, Resources: []sessions.Resource{{Kind: "path", Path: "src"}}, ExpiresAt: time.Now().Add(time.Hour / 2)})
	if e != nil {
		t.Fatalf("setup/read line 83: %v", e)
	}
	if e = os.WriteFile(filepath.Join(w.Root, "src"), []byte("finished"), 0600); e != nil {
		t.Fatalf("setup/read line 86: %v", e)
	}
	nativeGit(t, w.Root, "add", "src")
	nativeGit(t, w.Root, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "finished")
	rev, e := a.PublishAttemptRevision(wt, worker.ID, sessions.PublishRevisionRequest{OperationKey: "publish", AttemptID: at.ID, LeaseID: lease.ID, ExpectedFence: lease.Fence})
	if e != nil {
		t.Fatalf("setup/read line 92: %v", e)
	}
	runner, rt, e := a.Enroll(owner, sessions.Scope{Actions: []string{"verification.run"}, Resources: []string{"task:" + task.ID}}, time.Now().Add(time.Hour))
	if e != nil {
		t.Fatalf("setup/read line 96: %v", e)
	}
	marker := filepath.Join(private, "actual-command-count")
	check := sessions.CheckDescriptor{ID: "unit", Command: "printf ran >> '" + marker + "'", Deadline: 4 * time.Second}
	vg, e := a.GrantVerification(owner, runner.ID, task.ID, obs.Repository, check, time.Now().Add(time.Hour/2))
	if e != nil {
		t.Fatalf("setup/read line 102: %v", e)
	}
	executor, e := NewNativeVerificationRunner(a, rt, runner.ID, []NativeCheck{{Descriptor: check, GrantID: vg.ID}})
	if e != nil {
		t.Fatalf("setup/read line 106: %v", e)
	}
	ev, e := executor.Run(context.Background(), wt, NativeVerificationRequest{OperationKey: "trusted-check", AttemptID: at.ID, LeaseID: lease.ID, RevisionID: rev.ID, CheckID: check.ID, ExpectedFence: lease.Fence, WorkerSession: worker.ID})
	if e != nil {
		t.Fatalf("setup/read line 110: %v", e)
	}
	if b, e := os.ReadFile(marker); e != nil || string(b) != "ran" {
		t.Fatalf("actual trusted command not executed exactly once: %q %v", b, e)
	}
	policy := NativeReviewPolicy{SchemaVersion: 1, WorkspaceID: w.ID, ProjectID: "p", TaskID: task.ID, Revision: "policy-one", Checks: []sessions.RequiredCheck{{ID: check.ID, CommandDigest: sessions.CheckDigest(check)}}}
	policyPath := filepath.Join(private, "policy.json")
	acceptanceJSON(t, policyPath, policy)
	a, _, e = OpenNativeReviewAuthority(w.Root, w.ID, path, "installation", policyPath)
	if e != nil {
		t.Fatalf("setup/read line 120: %v", e)
	}
	reviewer, rvt, e := a.Enroll(owner, sessions.Scope{Actions: []string{"review.propose", "review.evaluate"}, Resources: []string{"task:" + task.ID}}, time.Now().Add(time.Hour))
	if e != nil {
		t.Fatalf("setup/read line 124: %v", e)
	}
	rg, e := a.GrantReview(owner, sessions.ReviewGrantRequest{ReviewerSession: reviewer.ID, WorkerSession: worker.ID, TaskID: task.ID, Repository: obs.Repository, ExpiresAt: time.Now().Add(time.Hour / 2)})
	if e != nil {
		t.Fatalf("setup/read line 128: %v", e)
	}
	review, e := a.ProposeReview(rvt, reviewer.ID, sessions.ReviewRequest{OperationKey: "independent-review", GrantID: rg.ID, AttemptID: at.ID, LeaseID: lease.ID, RevisionID: rev.ID, ExpectedFence: lease.Fence, Outcome: "approve", Rationale: "actual trusted check and reviewed code", EvidenceIDs: []string{ev.ID}})
	if e != nil {
		t.Fatalf("setup/read line 132: %v", e)
	}
	for _, upgrade := range []func(string) error{a.UpgradeSwarms, a.UpgradeDispatchIntents, a.UpgradeLifecycle, a.UpgradeRecovery, a.UpgradePreflight, a.UpgradeCheckpoints, a.UpgradeBudgets, a.UpgradeNativeAcceptance} {
		if e = upgrade(owner); e != nil {
			t.Fatalf("setup/read line 136: %v", e)
		}
	}
	candidate, e := a.ReadNativeAcceptanceCandidate(rvt, reviewer.ID, review.ID)
	if e != nil {
		t.Fatalf("setup/read line 141: %v", e)
	}
	if candidate.ReviewerSessionID == candidate.WorkerSessionID || len(candidate.Evidence) != 1 || candidate.Evidence[0].RunnerSessionID == candidate.WorkerSessionID {
		t.Fatal("independent producer credentials collapsed")
	}
	input := candidate.Input
	input.OperationKey = "owner-accept"
	ownerPath := filepath.Join(private, "owner.token")
	if e = os.WriteFile(ownerPath, []byte(owner), 0600); e != nil {
		t.Fatalf("setup/read line 150: %v", e)
	}
	configPath := filepath.Join(private, "owner.json")
	acceptanceJSON(t, configPath, recoveryOwnerConfig{SchemaVersion: 1, Workspace: w.Root, Authority: path, InstallationID: "installation", OwnerCredentialFile: ownerPath})
	access, e := OpenNativeAcceptanceOwner(configPath, policyPath)
	if e != nil {
		t.Fatalf("setup/read line 156: %v", e)
	}
	return acceptanceFixture{w: w, a: a, owner: owner, workerToken: wt, reviewerToken: rvt, worker: worker, reviewer: reviewer, at: at, lease: lease, revision: rev, grant: rg, input: input, access: access, task: task, runnerToken: rt, runnerID: runner.ID, verificationGrantID: vg.ID, check: check, marker: marker}
}
func acceptanceJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, e := json.Marshal(v)
	if e != nil {
		t.Fatalf("setup/read line 164: %v", e)
	}
	if e = os.WriteFile(path, b, 0600); e != nil {
		t.Fatalf("setup/read line 167: %v", e)
	}
}
func TestNativeAcceptanceActualCommandIndependentCanonicalClose(t *testing.T) {
	f := acceptanceSetup(t)
	result, e := f.access.Apply(f.input, "")
	if e != nil || !result.Applied || result.State != "closed" {
		t.Fatalf("actual native owner consumer must close canonical task: %+v %v", result, e)
	}
	task, e := store.NativeAcceptanceTask(f.w, f.task.ID, "p")
	if e != nil || task.Status != model.StatusDone || !store.LogHasStamp(task, "completed by ") {
		t.Fatalf("native close lacks actual DONE/completion stamp: %+v %v", task, e)
	}
	for _, criterion := range task.Acceptance() {
		if !criterion.Done {
			t.Fatal("native close left unchecked criteria")
		}
	}
	before, _ := os.ReadFile(task.Path)
	again, e := f.access.Apply(f.input, "")
	if e != nil || !again.Applied || !again.Duplicate {
		t.Fatalf("exact closed replay must be historical duplicate: %+v %v", again, e)
	}
	after, _ := os.ReadFile(task.Path)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("historical replay reexecuted canonical close")
	}
	changed := f.input
	changed.Tree = "0000000000000000000000000000000000000000"
	bad, e := f.access.Apply(changed, "")
	if e == nil || bad.Applied {
		t.Fatal("altered closed replay authorized acceptance")
	}
	runs, e := os.ReadDir(f.w.RunsDir())
	if e != nil && !os.IsNotExist(e) {
		t.Fatal(e)
	}
	if len(runs) != 0 {
		t.Fatal("native acceptance invented launcher provenance")
	}
}
func TestNativeAcceptanceConsumerRefusalsLeaveCanonicalUnchanged(t *testing.T) {
	for _, kind := range []string{"dirty", "changed-commit", "changed-tree", "digest", "reviewer-revoked", "worker-revoked", "runner-revoked", "grant-revoked", "released", "checklist", "claimed-review", "forged-repository", "forged-fence", "forged-revision", "forged-policy"} {
		t.Run(kind, func(t *testing.T) {
			f := acceptanceSetup(t)
			before, _ := os.ReadFile(f.task.Path)
			input := f.input
			switch kind {
			case "dirty":
				if e := os.WriteFile(filepath.Join(f.w.Root, "src"), []byte("dirty"), 0600); e != nil {
					t.Fatal(e)
				}
			case "changed-commit":
				nativeGit(t, f.w.Root, "-c", "user.name=f", "-c", "user.email=f@example.invalid", "commit", "--allow-empty", "-m", "later")
			case "changed-tree":
				if e := os.WriteFile(filepath.Join(f.w.Root, "src"), []byte("different tree"), 0600); e != nil {
					t.Fatal(e)
				}
				nativeGit(t, f.w.Root, "add", "src")
				nativeGit(t, f.w.Root, "-c", "user.name=f", "-c", "user.email=f@example.invalid", "commit", "-m", "changed tree")
			case "claimed-review":
				input.ReviewID = "caller-claimed-independent-review"
			case "forged-repository":
				input.Repository = "caller-claimed-repository"
			case "forged-fence":
				input.ExpectedFence++
			case "forged-revision":
				input.RevisionID = "caller-claimed-revision"
			case "forged-policy":
				input.PolicyRevision = "caller-claimed-policy"
			case "digest":
				input.CandidateDigest = "forged"
			case "worker-revoked":
				if e := f.a.Revoke(f.owner, f.worker.ID); e != nil {
					t.Fatal(e)
				}
			case "runner-revoked":
				c, e := f.a.ReadNativeAcceptanceCandidate(f.reviewerToken, f.reviewer.ID, f.input.ReviewID)
				if e != nil || len(c.Evidence) != 1 {
					t.Fatal("cannot select authentic runner producer", e)
				}
				if e = f.a.Revoke(f.owner, c.Evidence[0].RunnerSessionID); e != nil {
					t.Fatal(e)
				}
			case "reviewer-revoked":
				if e := f.a.Revoke(f.owner, f.reviewer.ID); e != nil {
					t.Fatal(e)
				}
			case "grant-revoked":
				if e := f.a.RevokeReview(f.owner, f.grant.ID); e != nil {
					t.Fatal(e)
				}
			case "released":
				if _, e := f.a.ReleaseLease(f.workerToken, f.worker.ID, sessions.ReleaseLeaseRequest{OperationKey: "release", AttemptID: f.at.ID, LeaseID: f.lease.ID, ExpectedFence: f.lease.Fence}); e != nil {
					t.Fatal(e)
				}
			case "checklist":
				f.task.Doc.SetSection("Acceptance", "- [ ] different criterion")
				if e := store.SaveTask(f.task); e != nil {
					t.Fatal(e)
				}
				before, _ = os.ReadFile(f.task.Path)
			}
			result, e := f.access.Apply(input, "")
			if e == nil || result.Applied {
				t.Fatalf("native consumer %s authorized invalid producer: %+v %v", kind, result, e)
			}
			after, _ := os.ReadFile(f.task.Path)
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("native consumer %s refusal changed canonical task", kind)
			}
		})
	}
}
func TestNativeAcceptanceClosingRetryUsesDeterministicPreparedOutcome(t *testing.T) {
	f := acceptanceSetup(t)
	a, o, observer, e := f.access.authenticate()
	if e != nil {
		t.Fatal(e)
	}
	_ = observer
	var digest string
	rec, e := a.BeginNativeAcceptance(o, f.input, func(rec sessions.NativeAcceptanceRecord) (string, error) {
		_, d, e := store.PrepareNativeAcceptanceTask(f.task, rec.CanonicalReceiptDigest, rec.Candidate.ReviewerSessionID, nativeAcceptanceSummary(f.input, rec.Candidate), rec.CreatedAt)
		digest = d
		return d, e
	})
	if e != nil {
		t.Fatal(e)
	}
	if rec.CanonicalOutcomeDigest != digest || rec.State != "closing" {
		t.Fatal("closing retirement not durably bound")
	}
	time.Sleep(20 * time.Millisecond)
	out, e := f.access.Apply(f.input, "")
	if e != nil || !out.Applied {
		t.Fatalf("closing-before-save crash must reuse deterministic prepared outcome: %+v %v", out, e)
	}
	if _, e = f.a.PublishAttemptRevision(f.workerToken, f.worker.ID, sessions.PublishRevisionRequest{OperationKey: "new-after-close", AttemptID: f.at.ID, LeaseID: f.lease.ID, ExpectedFence: f.lease.Fence, ExpectedRevision: 1}); !errors.Is(e, sessions.ErrDenied) {
		t.Fatalf("closed task writer resurrected: %v", e)
	}
}

func TestNativeAcceptanceActualPartialMoveAndHistoricalFinalization(t *testing.T) {
	for _, kind := range []string{"ignored-partial", "tracked-partial", "closed-before-finalization", "raw-replay-change"} {
		t.Run(kind, func(t *testing.T) {
			f := acceptanceSetupMode(t, kind == "tracked-partial")
			a, o, observer, e := f.access.authenticate()
			if e != nil {
				t.Fatal(e)
			}
			var body string
			rec, e := a.BeginNativeAcceptance(o, f.input, func(rec sessions.NativeAcceptanceRecord) (string, error) {
				b, d, e := store.PrepareNativeAcceptanceTask(f.task, rec.CanonicalReceiptDigest, rec.Candidate.ReviewerSessionID, nativeAcceptanceSummary(f.input, rec.Candidate), rec.CreatedAt)
				body = b
				return d, e
			})
			if e != nil {
				t.Fatal(e)
			}
			observer.receipt, observer.before = rec.CanonicalReceiptDigest, f.input.ChecklistDigest
			destination := filepath.Join(f.w.TasksDir("p", model.StatusDone), filepath.Base(f.task.Path))
			out, e := a.ContinueNativeAcceptance(o, f.input, func(sessions.NativeAcceptanceRecord) (bool, error) { return false, nil }, func(rec sessions.NativeAcceptanceRecord) error {
				if kind == "ignored-partial" || kind == "tracked-partial" {
					if e := os.MkdirAll(destination, 0700); e != nil {
						return e
					}
				}
				e := store.CloseTaskWithNativeAcceptance(f.w, f.task, rec.CanonicalReceiptDigest, body, rec.CanonicalOutcomeDigest)
				if e != nil {
					return e
				}
				return errors.New("injected failure after actual canonical close before finalization")
			})
			if e == nil || out.Applied {
				t.Fatalf("partial canonical close falsely Applied: %+v %v", out, e)
			}
			if kind == "ignored-partial" || kind == "tracked-partial" {
				if e = os.Remove(destination); e != nil {
					t.Fatal(e)
				}
			}
			if kind == "raw-replay-change" {
				raw, e := os.ReadFile(f.task.Path)
				if e != nil {
					t.Fatal(e)
				}
				raw = []byte(strings.ReplaceAll(string(raw), "\n", "\r\n"))
				if e = os.WriteFile(f.task.Path, raw, 0600); e != nil {
					t.Fatal(e)
				}
				out, e = f.access.Apply(f.input, "")
				if e == nil || out.Applied {
					t.Fatalf("raw canonical byte change authorized native historical finalization: %+v %v", out, e)
				}
				return
			}
			out, e = f.access.Apply(f.input, "")
			if kind == "tracked-partial" {
				if !errors.Is(e, sessions.ErrDenied) || !errors.Is(e, sessions.ErrReconcile) || out.Applied {
					t.Fatalf("tracked partial dirtiness must retain retirement and require reconciliation: %+v %v", out, e)
				}
				var durable struct {
					TaskTerminals map[string]sessions.NativeAcceptanceRecord
					Leases        map[string]sessions.Lease
				}
				raw, readErr := os.ReadFile(f.access.owner.config.Authority)
				if readErr != nil || json.Unmarshal(raw, &durable) != nil {
					t.Fatal("cannot inspect partial fixture state", readErr)
				}
				if durable.TaskTerminals[f.task.ID].State != "closing" || durable.Leases[f.lease.ID].Released {
					t.Fatal("partial refusal lost closing retirement or original protection")
				}
				return
			}
			if e != nil || !out.Applied {
				t.Fatalf("exact canonical partial/recovery failed: %+v %v", out, e)
			}
			task, e := store.NativeAcceptanceTask(f.w, f.task.ID, "p")
			if e != nil || task.Status != model.StatusDone {
				t.Fatal("partial outcome did not actually close", e)
			}
		})
	}
}

func TestNativeAcceptanceConsumerRejectsNonGenuineLanding(t *testing.T) {
	for _, kind := range []string{"ambient-override", "replacement-ancestry", "replacement-tree"} {
		t.Run(kind, func(t *testing.T) {
			f := acceptanceSetup(t)
			before, e := os.ReadFile(f.task.Path)
			if e != nil {
				t.Fatal(e)
			}
			if kind != "replacement-tree" {
				nativeGit(t, f.w.Root, "branch", "reviewed")
				nativeGit(t, f.w.Root, "checkout", "--orphan", "landing")
				nativeGit(t, f.w.Root, "rm", "-rf", "src")
				nativeGit(t, f.w.Root, "-c", "user.name=f", "-c", "user.email=f@example.invalid", "commit", "--allow-empty", "-m", "unrelated landing")
				target := nativeAcceptanceGitOutput(t, f.w.Root, "rev-parse", "HEAD")
				nativeGit(t, f.w.Root, "branch", "-f", "main", target)
				nativeGit(t, f.w.Root, "checkout", "reviewed")
				if kind == "ambient-override" {
					alternate := t.TempDir()
					nativeGit(t, alternate, "clone", "-b", "reviewed", f.w.Root, ".")
					nativeGit(t, alternate, "branch", "-f", "main", f.input.Commit)
					t.Setenv("GIT_DIR", filepath.Join(alternate, ".git"))
				} else {
					nativeGit(t, f.w.Root, "replace", "--graft", target, f.input.Commit)
				}
			}
			// Inject only after genuine verification and independent review exist.
			if kind == "replacement-tree" {
				landed := nativeAcceptanceGitOutput(t, f.w.Root, "rev-parse", "HEAD")
				if e := os.WriteFile(filepath.Join(f.w.Root, "src"), []byte("replacement tree"), 0600); e != nil {
					t.Fatal(e)
				}
				nativeGit(t, f.w.Root, "add", "src")
				nativeGit(t, f.w.Root, "-c", "user.name=f", "-c", "user.email=f@example.invalid", "commit", "-m", "replacement object")
				replacement := nativeAcceptanceGitOutput(t, f.w.Root, "rev-parse", "HEAD")
				nativeGit(t, f.w.Root, "update-ref", "refs/heads/main", landed)
				nativeGit(t, f.w.Root, "replace", landed, replacement)
			}
			out, e := f.access.Apply(f.input, "")
			if e == nil || out.Applied {
				t.Fatalf("non-genuine %s landing authorized actual native canonical close: %+v %v", kind, out, e)
			}
			after, e := os.ReadFile(f.task.Path)
			if e != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("invalid landing refusal changed canonical task", e)
			}
		})
	}
}

func nativeAcceptanceGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = store.VerificationEnvironment()
	b, e := c.CombinedOutput()
	if e != nil {
		t.Fatalf("fixture git %v: %s %v", args, b, e)
	}
	return strings.TrimSpace(string(b))
}
