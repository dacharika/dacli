package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// core/597: a command that never started must not look successful when native
// Git observation loses its deadline. Keep this separate from process cleanup.
func TestNativeVerificationPreflightFailureStatus(t *testing.T) {
	for _, kind := range []string{"nil-context", "cancelled", "deadline", "git-unavailable", "dirty", "revision-mismatch", "missing-revision"} {
		t.Run(kind, func(t *testing.T) {
			dir := verificationRepo(t)
			marker := filepath.Join(t.TempDir(), "command-started")
			command := "printf executed > '" + strings.ReplaceAll(marker, "'", "'\\''") + "'"
			ctx := context.Background()
			want := ""
			switch kind {
			case "nil-context":
				ctx = nil
				want = "context required"
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				want = "git version observation unavailable"
			case "deadline":
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer cancel()
				want = "git version observation unavailable"
			case "git-unavailable":
				t.Setenv("PATH", t.TempDir())
				want = "git version observation unavailable"
			case "dirty":
				if err := os.WriteFile(filepath.Join(dir, "uncommitted"), []byte("dirty"), 0o600); err != nil {
					t.Fatal(err)
				}
				want = "working tree is dirty"
			case "revision-mismatch":
				want = "verification revision changed before execution"
			case "missing-revision":
				want = "context and exact revision required"
			}
			var ev VerificationEvidence
			var out []byte
			var err error
			if kind == "revision-mismatch" {
				ev, out, err = RunAcceptanceVerificationRevisionContext(ctx, dir, "runner", command, strings.Repeat("0", 40), strings.Repeat("0", 40))
			} else if kind == "missing-revision" {
				ev, out, err = RunAcceptanceVerificationRevisionContext(ctx, dir, "runner", command, "", "")
			} else {
				ev, out, err = RunAcceptanceVerificationContext(ctx, dir, "runner", command)
			}
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("unexpected preflight outcome: %+v %v", ev, err)
			}
			if ev.ExitCode != 1 {
				t.Fatalf("failed preflight must report exit one: %+v %v", ev, err)
			}
			if len(out) != 0 || ev.ArtifactHash != "" {
				t.Fatalf("preflight fabricated command artifact: %+v %q", ev, out)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("command ran before preflight refusal: %v", err)
			}
		})
	}
}

func TestNativeVerificationCommandStatusAfterPreflight(t *testing.T) {
	for _, kind := range []string{"success", "failure"} {
		t.Run(kind, func(t *testing.T) {
			dir := verificationRepo(t)
			command := "printf executed"
			want := 0
			if kind == "failure" {
				command += "; exit 17"
				want = 17
			}
			ev, out, err := RunAcceptanceVerificationContext(context.Background(), dir, "runner", command)
			if ev.ExitCode != want || string(out) != "executed" || ev.ArtifactHash == "" || (want == 0) != (err == nil) {
				t.Fatalf("completed command status changed: %+v %q %v", ev, out, err)
			}
		})
	}
}

// A postexecution policy error invalidates acceptance but retains the command's
// actual status and artifact; it must not be relabelled as a preflight failure.
func TestNativeVerificationPostexecutionPolicyPreservesCommandStatus(t *testing.T) {
	for _, status := range []int{0, 17} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			dir := verificationRepo(t)
			command := fmt.Sprintf("printf executed; printf dirty > changed; exit %d", status)
			ev, out, err := RunAcceptanceVerificationContext(context.Background(), dir, "runner", command)
			if !IsVerificationPolicyError(err) || ev.ExitCode != status || string(out) != "executed" || ev.ArtifactHash == "" {
				t.Fatalf("postexecution policy changed actual command status: %+v %q %v", ev, out, err)
			}
		})
	}
}
