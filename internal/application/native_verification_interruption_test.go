package application

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/governance/nativeconfig"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
)

func nativeInterruptionProfile(t *testing.T, f nativeFixture) *NativeVerificationProfile {
	t.Helper()
	private := t.TempDir()
	worker, runner := filepath.Join(private, "worker.token"), filepath.Join(private, "runner.token")
	for path, token := range map[string]string{worker: f.workerToken, runner: f.runnerToken} {
		if err := os.WriteFile(path, []byte(token), 0600); err != nil {
			t.Fatal(err)
		}
	}
	connection, profile := filepath.Join(private, "connection.json"), filepath.Join(private, "runner.json")
	for path, value := range map[string]any{
		connection: nativeconfig.Connection{SchemaVersion: 1, Workspace: f.workspace.Root, Authority: f.path, WorkspaceID: f.workspace.ID, InstallationID: "installation", SessionID: f.worker.ID, CredentialFile: worker},
		profile:    NativeRunnerProfile{SchemaVersion: 1, Workspace: f.workspace.Root, WorkspaceID: f.workspace.ID, Authority: f.path, InstallationID: "installation", TaskID: f.attempt.Request.TaskID, Repository: f.attempt.Observation.Repository, RunnerSessionID: f.runner.ID, CredentialFile: runner, Checks: []ConfiguredCheck{{ID: f.check.ID, Command: f.check.Command, Deadline: f.check.Deadline.String(), GrantID: f.grant.ID}}},
	} {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	selected, err := OpenNativeVerificationProfile(connection, profile)
	if err != nil {
		t.Fatal(err)
	}
	return selected
}

func TestNativeVerificationKnownPreStartCancellationSurvivesJobRestart(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "must-not-start")
	f := nativeSetup(t, "printf ran > '"+marker+"'")
	prepared, receipt, err := f.executor.Prepare(f.workerToken, f.request)
	if err != nil || receipt != nil || prepared == nil {
		t.Fatal("prepare failed", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if ev, err := prepared.Execute(ctx); !errors.Is(err, sessions.ErrDenied) || ev.ID != "" {
		t.Fatal("cancelled preparation inferred evidence", ev, err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cancelled preparation launched command", err)
	}
	selected := nativeInterruptionProfile(t, f)
	jobs, err := NewNativeVerificationJobs(selected)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = jobs.Close() }()
	for _, method := range []func(NativeVerificationRequest) (VerificationStatus, error){jobs.Status, jobs.Cancel, jobs.Request} {
		got, err := method(f.request)
		if err != nil || got.State != "interrupted" || got.Evidence != nil || got.Interruption == nil || got.Interruption.Reason != "cancelled_before_start" {
			t.Fatal("fresh job process lost durable interruption", got, err)
		}
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("restart reran interrupted command", err)
	}
}

func TestNativeVerificationUnknownReservationRemainsProtectedAcrossRestart(t *testing.T) {
	f := nativeSetup(t, "printf must-not-run")
	if prepared, receipt, err := f.executor.Prepare(f.workerToken, f.request); err != nil || prepared == nil || receipt != nil {
		t.Fatal("prepare failed", err)
	}
	jobs, err := NewNativeVerificationJobs(nativeInterruptionProfile(t, f))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = jobs.Close() }()
	before, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []func(NativeVerificationRequest) (VerificationStatus, error){jobs.Status, jobs.Cancel, jobs.Request} {
		got, err := method(f.request)
		if err != nil || got.State != "reconciliation_required" || got.Evidence != nil || got.Interruption != nil {
			t.Fatal("restart/cancel inferred an unobserved terminal outcome", got, err)
		}
	}
	after, err := os.ReadFile(f.path)
	if err != nil || string(before) != string(after) {
		t.Fatal("unknown restart changed original authority", err)
	}
}

func TestNativeVerificationRunningCancellationRecordsOnlyObservedOwnedCleanup(t *testing.T) {
	switch runtime.GOOS {
	case "darwin", "linux", "freebsd", "openbsd", "netbsd", "dragonfly":
	default:
		t.Skip("owned process cleanup unavailable")
	}
	marker := filepath.Join(t.TempDir(), "started")
	f := nativeSetup(t, "printf started > '"+marker+"'; sleep 30")
	prepared, receipt, err := f.executor.Prepare(f.workerToken, f.request)
	if err != nil || prepared == nil || receipt != nil {
		t.Fatal("prepare failed", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := prepared.Execute(ctx); done <- err }()
	until := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(until) {
			t.Fatal("actual configured command did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err = <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled executor did not return")
	}
	if !errors.Is(err, sessions.ErrDenied) {
		t.Fatal("cancelled executor returned success", err)
	}
	access, err := nativeInterruptionProfile(t, f).Authenticate()
	if err != nil {
		t.Fatal(err)
	}
	got, err := access.Lookup(f.request)
	if err != nil || got.State != sessions.VerificationInterrupted || got.Evidence != nil || got.Interruption == nil || got.Interruption.Reason != "cancelled_owned_group_cleanup" {
		t.Fatal("known cancellation remained unknown or fabricated evidence", got, err)
	}
	if strings.Contains(got.Interruption.Assurance, "all") {
		t.Fatal("interruption claimed all-writer termination")
	}
}
