package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/governance/checkout"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

type nativeFixture struct {
	workspace                             *workspace.Workspace
	authority                             *sessions.Authority
	path, owner, workerToken, runnerToken string
	worker, runner                        sessions.Session
	attempt                               sessions.Attempt
	lease                                 sessions.Lease
	revision                              sessions.AttemptRevision
	check                                 sessions.CheckDescriptor
	grant                                 sessions.VerificationGrant
	request                               NativeVerificationRequest
	executor                              *NativeVerificationRunner
}

func nativeGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = store.VerificationEnvironment()
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
}
func nativeSetup(t *testing.T, command string) nativeFixture {
	t.Helper()
	w, _ := readFixture(t)
	if _, err := store.CreateProject(w, "root", "Project", "p", "goal", ""); err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(w, "root", "p", "Verify", store.TaskOpts{})
	if err != nil {
		t.Fatal(err)
	}
	admission, err := NewLeaseAdmission(w)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "authority.json")
	a, err := sessions.Open(path, sessions.Binding{Workspace: w.ID, Installation: "installation"}, admission)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := a.Bootstrap()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	nativeGit(t, dir, "init")
	if err = os.WriteFile(filepath.Join(dir, "tracked"), []byte("base"), 0600); err != nil {
		t.Fatal(err)
	}
	nativeGit(t, dir, "add", "tracked")
	nativeGit(t, dir, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-m", "base")
	obs, err := checkout.Observe(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.BindTask(owner, sessions.TaskBinding{TaskID: task.ID, ProjectID: "p", Repository: obs.Repository, AdmissionEnabled: true}); err != nil {
		t.Fatal(err)
	}
	worker, workerToken, err := a.Enroll(owner, sessions.Scope{Actions: []string{"attempt.register", "attempt.publish", "lease.acquire", "lease.renew", "evidence.submit", "verification.request"}, Resources: []string{"task:" + task.ID, "checkout:"}}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	at, err := a.RegisterAttempt(workerToken, worker.ID, sessions.AttemptRequest{RequestID: "external", TaskID: task.ID, Checkout: dir})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := a.AcquireLease(workerToken, worker.ID, sessions.AcquireLeaseRequest{OperationKey: "lease", AttemptID: at.ID, Resources: []sessions.Resource{{Kind: "checkout"}}, ExpiresAt: time.Now().Add(10 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "tracked"), []byte("final"), 0600); err != nil {
		t.Fatal(err)
	}
	nativeGit(t, dir, "add", "tracked")
	nativeGit(t, dir, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-m", "final")
	revision, err := a.PublishAttemptRevision(workerToken, worker.ID, sessions.PublishRevisionRequest{OperationKey: "publish", AttemptID: at.ID, LeaseID: lease.ID, ExpectedFence: lease.Fence})
	if err != nil {
		t.Fatal(err)
	}
	runner, runnerToken, err := a.Enroll(owner, sessions.Scope{Actions: []string{"verification.run"}, Resources: []string{"task:" + task.ID}}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	check := sessions.CheckDescriptor{ID: "unit", Command: command, Deadline: 4 * time.Second}
	grant, err := a.GrantVerification(owner, runner.ID, task.ID, obs.Repository, check, time.Now().Add(10*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	executor, err := NewNativeVerificationRunner(a, runnerToken, runner.ID, []NativeCheck{{check, grant.ID}})
	if err != nil {
		t.Fatal(err)
	}
	req := NativeVerificationRequest{OperationKey: "verify", WorkerSession: worker.ID, AttemptID: at.ID, LeaseID: lease.ID, RevisionID: revision.ID, CheckID: check.ID, ExpectedFence: lease.Fence}
	return nativeFixture{w, a, path, owner, workerToken, runnerToken, worker, runner, at, lease, revision, check, grant, req, executor}
}
func TestNativeVerificationExecutesConfiguredCheckAndRestartRetry(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "count")
	f := nativeSetup(t, "printf ran >> '"+marker+"'; printf harmless")
	ev, err := f.executor.Run(context.Background(), f.workerToken, f.request)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Provenance != "trusted-local-runner" || ev.IssuerSession != f.runner.ID || ev.OutputDigest != sessions.OutputDigest([]byte("harmless")) || ev.Revision.ID != f.revision.ID || ev.ExitCode != 0 {
		t.Fatalf("bad evidence %+v", ev)
	}
	eligible, err := f.authority.EligibleVerification(f.workerToken, f.worker.ID, ev.ID)
	if err != nil || !eligible {
		t.Fatal("not eligible", err)
	}
	a, err := sessions.Open(f.path, sessions.Binding{Workspace: f.authorityBinding(), Installation: "installation"})
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := NewNativeVerificationRunner(a, f.runnerToken, f.runner.ID, []NativeCheck{{f.check, f.grant.ID}})
	if err != nil {
		t.Fatal(err)
	}
	retry, err := restarted.Run(context.Background(), f.workerToken, f.request)
	if err != nil || !reflect.DeepEqual(ev, retry) {
		t.Fatal("retry", err)
	}
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != "ran" {
		t.Fatalf("reexecuted %q %v", data, err)
	}
	req := f.request
	req.CheckID = "unknown"
	_, err = f.executor.Run(context.Background(), f.workerToken, req)
	if !errors.Is(err, sessions.ErrDenied) {
		t.Fatal(err)
	}
	data, err = os.ReadFile(f.path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{f.workerToken, f.runnerToken, f.owner, "harmless"} {
		if strings.Contains(string(data), secret) {
			t.Fatal("secret/raw output persisted")
		}
	}
}
func (f nativeFixture) authorityBinding() string {
	// The immutable binding is recovered from the file only in this test; production
	// owners keep their explicit workspace binding when reconstructing the runner.
	var value struct{ Binding sessions.Binding }
	data, _ := os.ReadFile(f.path)
	_ = json.Unmarshal(data, &value)
	return value.Binding.Workspace
}
func TestNativeVerificationChangedTreeAndFailedOutcome(t *testing.T) {
	for _, command := range []string{"printf changed > tracked", "git -c user.name=test -c user.email=test@example.com commit --allow-empty -m changed", "printf diagnostic; exit 7"} {
		t.Run(command, func(t *testing.T) {
			f := nativeSetup(t, command)
			ev, err := f.executor.Run(context.Background(), f.workerToken, f.request)
			if strings.Contains(command, "exit 7") {
				if err != nil || ev.ExitCode != 7 {
					t.Fatal("failed outcome", ev, err)
				}
				ok, err := f.authority.EligibleVerification(f.workerToken, f.worker.ID, ev.ID)
				if err != nil || ok {
					t.Fatal("failed eligible", err)
				}
			} else {
				if !errors.Is(err, sessions.ErrDenied) || ev.ID != "" {
					t.Fatal("changed tree accepted", ev, err)
				}
				_, err = f.executor.Run(context.Background(), f.workerToken, f.request)
				if !errors.Is(err, sessions.ErrReconcile) {
					t.Fatal("uncertain run replay", err)
				}
			}
		})
	}
}
func TestNativeVerificationScrubsSecretsAndBoundsDeadline(t *testing.T) {
	t.Setenv("DACLI_AGENT", "secret-agent")
	t.Setenv("DACLI_SESSION_TOKEN", "secret-session")
	t.Setenv("MCP_SESSION", "secret-mcp")
	t.Setenv("GIT_WORK_TREE", "/wrong")
	t.Setenv("SERVICE_TOKEN", "secret-other")
	f := nativeSetup(t, "test -z \"$DACLI_AGENT$DACLI_SESSION_TOKEN$MCP_SESSION$SERVICE_TOKEN\" && test -z \"$GIT_WORK_TREE\" && printf safe")
	ev, err := f.executor.Run(context.Background(), f.workerToken, f.request)
	if err != nil || ev.ExitCode != 0 || ev.OutputDigest != sessions.OutputDigest([]byte("safe")) {
		t.Fatal("unsafe environment", ev, err)
	}
	f = nativeSetup(t, "sleep 5 & wait")
	short := f.check
	short.Deadline = 150 * time.Millisecond
	grant, err := f.authority.GrantVerification(f.owner, f.runner.ID, f.attempt.Request.TaskID, f.attempt.Observation.Repository, short, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	executor, err := NewNativeVerificationRunner(f.authority, f.runnerToken, f.runner.ID, []NativeCheck{{short, grant.ID}})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	_, err = executor.Run(context.Background(), f.workerToken, f.request)
	if !errors.Is(err, sessions.ErrDenied) || time.Since(started) > 2*time.Second {
		t.Fatal("unbounded deadline", time.Since(started), err)
	}
	_, err = executor.Run(context.Background(), f.workerToken, f.request)
	if !errors.Is(err, sessions.ErrReconcile) {
		t.Fatal("timeout replay", err)
	}
}

type nativeRaceInput struct {
	Path, Workspace, Owner, WorkerToken, WorkerSession, Target, Action string
	Publish                                                            sessions.PublishRevisionRequest
}

func TestNativeVerificationSeparateProcessFinalAuthority(t *testing.T) {
	if raw := os.Getenv("NATIVE_RACE_INPUT"); raw != "" {
		var in nativeRaceInput
		if err := json.Unmarshal([]byte(raw), &in); err != nil {
			t.Fatal(err)
		}
		a, err := sessions.Open(in.Path, sessions.Binding{Workspace: in.Workspace, Installation: "installation"})
		if err != nil {
			t.Fatal(err)
		}
		switch in.Action {
		case "revision":
			_, err = a.PublishAttemptRevision(in.WorkerToken, in.WorkerSession, in.Publish)
		case "grant":
			err = a.RevokeVerification(in.Owner, in.Target)
		default:
			err = a.Revoke(in.Owner, in.Target)
		}
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	for _, action := range []string{"revision", "grant", "worker", "runner", "lease-expiry", "grant-expiry"} {
		t.Run(action, func(t *testing.T) {
			signals := t.TempDir()
			started := filepath.Join(signals, "started")
			release := filepath.Join(signals, "release")
			f := nativeSetup(t, "printf started > '"+started+"'; while test ! -f '"+release+"'; do sleep 0.01; done; printf safe")
			var expiry time.Time
			if action == "lease-expiry" {
				expiry = time.Now().Add(time.Second)
				var err error
				f.lease, err = f.authority.RenewLease(f.workerToken, f.worker.ID, sessions.RenewLeaseRequest{OperationKey: "short-lease", AttemptID: f.attempt.ID, LeaseID: f.lease.ID, ExpectedFence: f.lease.Fence, ExpiresAt: expiry})
				if err != nil {
					t.Fatal(err)
				}
			}
			if action == "grant-expiry" {
				expiry = time.Now().Add(time.Second)
				grant, err := f.authority.GrantVerification(f.owner, f.runner.ID, f.attempt.Request.TaskID, f.attempt.Observation.Repository, f.check, expiry)
				if err != nil {
					t.Fatal(err)
				}
				f.executor, err = NewNativeVerificationRunner(f.authority, f.runnerToken, f.runner.ID, []NativeCheck{{f.check, grant.ID}})
				if err != nil {
					t.Fatal(err)
				}
			}
			type outcome struct {
				ev  sessions.Evidence
				err error
			}
			done := make(chan outcome, 1)
			go func() {
				ev, err := f.executor.Run(context.Background(), f.workerToken, f.request)
				done <- outcome{ev, err}
			}()
			deadline := time.Now().Add(3 * time.Second)
			for {
				if _, err := os.Stat(started); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("command did not start")
				}
				time.Sleep(5 * time.Millisecond)
			}
			if !expiry.IsZero() {
				time.Sleep(time.Until(expiry.Add(20 * time.Millisecond)))
			} else {
				target := f.worker.ID
				if action == "runner" {
					target = f.runner.ID
				}
				if action == "grant" {
					target = f.grant.ID
				}
				input := nativeRaceInput{Path: f.path, Workspace: f.authorityBinding(), Owner: f.owner, WorkerToken: f.workerToken, WorkerSession: f.worker.ID, Action: action, Target: target, Publish: sessions.PublishRevisionRequest{OperationKey: "advance", AttemptID: f.attempt.ID, LeaseID: f.lease.ID, ExpectedFence: f.lease.Fence, ExpectedRevision: 1}}
				data, err := json.Marshal(input)
				if err != nil {
					t.Fatal(err)
				}
				cmd := exec.Command(os.Args[0], "-test.run=^TestNativeVerificationSeparateProcessFinalAuthority$")
				for _, e := range os.Environ() {
					if !strings.HasPrefix(e, "DACLI_AGENT=") && !strings.HasPrefix(e, "NATIVE_RACE_INPUT=") {
						cmd.Env = append(cmd.Env, e)
					}
				}
				cmd.Env = append(cmd.Env, "NATIVE_RACE_INPUT="+string(data))
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("authority child: %v %s", err, out)
				}
			}
			if err := os.WriteFile(release, nil, 0600); err != nil {
				t.Fatal(err)
			}
			result := <-done
			if !errors.Is(result.err, sessions.ErrDenied) || result.ev.ID != "" {
				t.Fatalf("post-execution %s accepted: %+v %v", action, result.ev, result.err)
			}
			data, err := os.ReadFile(f.path)
			if err != nil {
				t.Fatal(err)
			}
			var persisted struct{ Evidence map[string]sessions.Evidence }
			if err = json.Unmarshal(data, &persisted); err != nil {
				t.Fatal(err)
			}
			if len(persisted.Evidence) != 0 {
				t.Fatal("eligible evidence persisted after invalidation")
			}
		})
	}
}
func TestNativeVerificationConstructorConfigMismatch(t *testing.T) {
	f := nativeSetup(t, "printf harmless")
	changed := f.check
	changed.Command = "printf substituted"
	executor, err := NewNativeVerificationRunner(f.authority, f.runnerToken, f.runner.ID, []NativeCheck{{changed, f.grant.ID}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = executor.Run(context.Background(), f.workerToken, f.request)
	if !errors.Is(err, sessions.ErrDenied) {
		t.Fatal("command substitution", err)
	}
	formatted := fmt.Sprintf("%+v %#v %+v %#v", f.executor, f.executor, *f.executor, *f.executor)
	if strings.Contains(formatted, f.runnerToken) || strings.Contains(formatted, f.workerToken) {
		t.Fatal("formatted runner exposes credential")
	}
	// Errors and receipts must not expose the configured runner credential.
	if strings.Contains(fmt.Sprint(err), f.runnerToken) {
		t.Fatal("runner credential exposed")
	}
}
