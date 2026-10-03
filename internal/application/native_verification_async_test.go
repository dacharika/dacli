package application

import (
	"context"
	"encoding/json"
	"github.com/mlnomadpy/dacli/internal/governance/checkout"
	"github.com/mlnomadpy/dacli/internal/governance/nativeconfig"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/store"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func completedJobFixture(t *testing.T) (*NativeVerificationJobs, NativeVerificationRequest, sessions.Evidence) {
	t.Helper()
	w, _ := readFixture(t)
	_, e := store.CreateProject(w, "root", "Project", "p", "goal", "")
	if e != nil {
		t.Fatal(e)
	}
	task, e := store.CreateTask(w, "root", "p", "Check", store.TaskOpts{})
	if e != nil {
		t.Fatal(e)
	}
	nativeGit(t, w.Root, "init")
	nativeGit(t, w.Root, "add", ".")
	nativeGit(t, w.Root, "-c", "user.name=test", "-c", "user.email=test@example.invalid", "commit", "-m", "base")
	obs, e := checkout.Observe(w.Root)
	if e != nil {
		t.Fatal(e)
	}
	private := t.TempDir()
	authorityPath := filepath.Join(private, "authority.json")
	admission, e := NewLeaseAdmission(w)
	if e != nil {
		t.Fatal(e)
	}
	a, e := sessions.Open(authorityPath, sessions.Binding{Workspace: w.ID, Installation: "installation"}, admission)
	if e != nil {
		t.Fatal(e)
	}
	owner, e := a.Bootstrap()
	if e != nil {
		t.Fatal(e)
	}
	if e = a.BindTask(owner, sessions.TaskBinding{TaskID: task.ID, ProjectID: "p", Repository: obs.Repository, AdmissionEnabled: true}); e != nil {
		t.Fatal(e)
	}
	worker, token, e := a.Enroll(owner, sessions.Scope{Actions: []string{"attempt.register", "attempt.publish", "lease.acquire", "verification.request"}, Resources: []string{"task:" + task.ID, "checkout:"}}, time.Now().Add(time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	at, e := a.RegisterAttempt(token, worker.ID, sessions.AttemptRequest{RequestID: "at", TaskID: task.ID, Checkout: w.Root})
	if e != nil {
		t.Fatal(e)
	}
	lease, e := a.AcquireLease(token, worker.ID, sessions.AcquireLeaseRequest{OperationKey: "lease", AttemptID: at.ID, Resources: []sessions.Resource{{Kind: "checkout"}}, ExpiresAt: time.Now().Add(time.Minute)})
	if e != nil {
		t.Fatal(e)
	}
	rev, e := a.PublishAttemptRevision(token, worker.ID, sessions.PublishRevisionRequest{OperationKey: "publish", AttemptID: at.ID, LeaseID: lease.ID, ExpectedFence: lease.Fence})
	if e != nil {
		t.Fatal(e)
	}
	runner, runnerToken, e := a.Enroll(owner, sessions.Scope{Actions: []string{"verification.run"}, Resources: []string{"task:" + task.ID}}, time.Now().Add(time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	check := sessions.CheckDescriptor{ID: "unit", Command: "printf harmless", Deadline: time.Second}
	grant, e := a.GrantVerification(owner, runner.ID, task.ID, obs.Repository, check, time.Now().Add(time.Minute))
	if e != nil {
		t.Fatal(e)
	}
	req := NativeVerificationRequest{OperationKey: "verify", WorkerSession: worker.ID, AttemptID: at.ID, LeaseID: lease.ID, RevisionID: rev.ID, CheckID: check.ID, ExpectedFence: lease.Fence}
	executor, e := NewNativeVerificationRunner(a, runnerToken, runner.ID, []NativeCheck{{check, grant.ID}})
	if e != nil {
		t.Fatal(e)
	}
	evidence, e := executor.Run(context.Background(), token, req)
	if e != nil {
		t.Fatal(e)
	}
	save := func(path string, value any) {
		t.Helper()
		b, e := json.Marshal(value)
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(path, b, 0600); e != nil {
			t.Fatal(e)
		}
	}
	workerFile := filepath.Join(private, "worker.token")
	runnerFile := filepath.Join(private, "runner.token")
	if e = os.WriteFile(workerFile, []byte(token), 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(runnerFile, []byte(runnerToken), 0600); e != nil {
		t.Fatal(e)
	}
	connection := filepath.Join(private, "connection.json")
	profile := filepath.Join(private, "runner.json")
	save(connection, nativeconfig.Connection{SchemaVersion: 1, Workspace: w.Root, Authority: authorityPath, WorkspaceID: w.ID, InstallationID: "installation", SessionID: worker.ID, CredentialFile: workerFile})
	save(profile, NativeRunnerProfile{SchemaVersion: 1, Workspace: w.Root, WorkspaceID: w.ID, Authority: authorityPath, InstallationID: "installation", TaskID: task.ID, Repository: obs.Repository, RunnerSessionID: runner.ID, CredentialFile: runnerFile, Checks: []ConfiguredCheck{{ID: "unit", Command: check.Command, Deadline: "1s", GrantID: grant.ID}}})
	selected, e := OpenNativeVerificationProfile(connection, profile)
	if e != nil {
		t.Fatal(e)
	}
	m, e := NewNativeVerificationJobs(selected)
	if e != nil {
		t.Fatal(e)
	}
	return m, req, evidence
}
func TestCompletedVerificationPrecedesLiveJob(t *testing.T) {
	m, r, evidence := completedJobFixture(t)
	// Deterministically reproduce the interval after receipt commit and before
	// execute goroutine deletion, without another check or timing assumption.
	m.jobs[jobKey(r)] = &verificationJob{request: r, state: "reserved", cancel: func() { t.Error("completed job was cancelled") }}
	for _, call := range []func(NativeVerificationRequest) (VerificationStatus, error){m.Request, m.Status, m.Cancel} {
		s, e := call(r)
		if e != nil || s.State != "completed" || s.Evidence == nil || s.Evidence.ID != evidence.ID {
			t.Fatal("committed receipt masked by live job", s, e)
		}
	}
}
