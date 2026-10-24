//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package application

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/governance/sessions"
)

func TestNativeVerificationShutdownReportsUnresolvedInterruption(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "started")
	f := nativeSetupLinkedWorkspace(t, "printf started > '"+marker+"'; sleep 30")
	jobs, err := NewNativeVerificationJobs(nativeInterruptionProfile(t, f))
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = jobs.Close()
		}
		_ = os.Chmod(f.path, 0600)
	}()
	if status, err := jobs.Request(f.request); err != nil || status.State != "reserved" {
		t.Fatal("verification was not admitted", status, err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if info, err := os.Stat(marker); err == nil && info.Size() > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("configured verification command did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Public fixture corruption models a runner whose owned command was
	// cleaned up but whose authenticated interruption write now refuses.
	if err := os.Chmod(f.path, 0644); err != nil {
		t.Fatal(err)
	}
	closeErr := jobs.Close()
	closed = true
	if !errors.Is(closeErr, sessions.ErrReconcile) {
		t.Fatalf("shutdown reported success without an authenticated terminal outcome: %v", closeErr)
	}
	if err := os.Chmod(f.path, 0600); err != nil {
		t.Fatal(err)
	}
	selected := nativeInterruptionProfile(t, f)
	access, err := selected.Authenticate()
	if err != nil {
		t.Fatal(err)
	}
	lookup, err := access.Lookup(f.request)
	if err != nil || lookup.State != sessions.VerificationPending || lookup.Evidence != nil || lookup.Interruption != nil {
		t.Fatalf("unresolved cancellation changed its durable reservation: %+v, %v", lookup, err)
	}
	if err := jobs.Close(); !errors.Is(err, sessions.ErrReconcile) {
		t.Fatalf("repeated shutdown forgot unresolved outcome: %v", err)
	}
}

func TestNativeVerificationShutdownKeepsCompletedFailedCheck(t *testing.T) {
	f := nativeSetupLinkedWorkspace(t, "printf failed-check; exit 7")
	jobs, err := NewNativeVerificationJobs(nativeInterruptionProfile(t, f))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = jobs.Close() }()
	if status, err := jobs.Request(f.request); err != nil || status.State != "reserved" {
		t.Fatal("verification was not admitted", status, err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		status, err := jobs.Status(f.request)
		if err != nil {
			t.Fatal(err)
		}
		if status.State == "completed" {
			if status.Evidence == nil || status.Evidence.ExitCode != 7 {
				t.Fatalf("failed check evidence was not retained: %+v", status)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("failed check did not reach a durable completed result: %+v", status)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := jobs.Close(); err != nil {
		t.Fatalf("durable failed-check evidence made shutdown appear unresolved: %v", err)
	}
}
