//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func verificationChildRunning(t *testing.T, pid int) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ps", "-o", "stat=", "-p", strconv.Itoa(pid))
	cmd.WaitDelay = 250 * time.Millisecond
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatal("process-state observation timed out")
	}
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 && strings.TrimSpace(string(out)) == "" {
			return false
		}
		t.Fatalf("process-state observation unavailable: %v %s", err, out)
	}
	state := strings.TrimSpace(string(out))
	// An orphan zombie has terminated; only its new parent can reap it. The
	// direct shell is reaped by Cmd.Run, while this assertion checks execution.
	return state != "" && !strings.HasPrefix(state, "Z")
}

func TestNativeVerificationOwnsChildCleanup(t *testing.T) {
	for _, mode := range []string{"cancel", "deadline", "normal"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			for _, args := range [][]string{{"init"}, {"-c", "user.name=test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "base"}} {
				cmd := exec.Command("git", args...)
				cmd.Dir = dir
				cmd.Env = VerificationEnvironment()
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("git: %v %s", err, out)
				}
			}
			observation, err := readVerificationTree(dir)
			if err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(t.TempDir(), "processes")
			quoted := "'" + strings.ReplaceAll(marker, "'", "'\"'\"'") + "'"
			// The real child survives killing only its shell. Keeping stdout inherited
			// in cancellation cases also exercises the bounded pipe cleanup path.
			command := "sleep 30 & child=$!; printf '%s %s' $$ $child > " + quoted + "; wait"
			if mode == "normal" {
				command = "sleep 30 >/dev/null 2>&1 & child=$!; printf '%s %s' $$ $child > " + quoted + "; printf done"
			}
			timeout := 8 * time.Second
			if mode == "deadline" {
				timeout = 2 * time.Second
			}
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			type result struct {
				evidence VerificationEvidence
				output   []byte
				err      error
			}
			done := make(chan result, 1)
			go func() {
				ev, out, err := RunAcceptanceVerificationRevisionContext(ctx, dir, "runner", command, observation.Commit, observation.Tree)
				done <- result{ev, out, err}
			}()
			var shell, child int
			until := time.Now().Add(1500 * time.Millisecond)
			for time.Now().Before(until) {
				b, readErr := os.ReadFile(marker)
				if readErr == nil {
					if n, scanErr := fmt.Sscanf(string(b), "%d %d", &shell, &child); n == 2 && scanErr == nil {
						break
					}
				}
				time.Sleep(10 * time.Millisecond)
			}
			if shell <= 0 || child <= 0 {
				t.Fatal("real verification child did not start")
			}
			t.Cleanup(func() {
				// Also clean up the intentionally surviving child in a mutation-red run.
				if group, e := syscall.Getpgid(child); e == nil && group == shell {
					_ = syscall.Kill(-group, syscall.SIGKILL)
				}
			})
			if mode != "normal" && !verificationChildRunning(t, child) {
				t.Fatal("child was not alive before cancellation")
			}
			started := time.Now()
			if mode == "cancel" {
				cancel()
			}
			var got result
			select {
			case got = <-done:
			case <-time.After(4 * time.Second):
				t.Fatal("verification did not return within cleanup bound")
			}
			if time.Since(started) > 4*time.Second {
				t.Fatal("cleanup exceeded bound")
			}
			if mode == "normal" {
				if got.err != nil || got.evidence.ExitCode != 0 || string(got.output) != "done" {
					t.Fatalf("normal result changed: %v %#v %q", got.err, got.evidence, got.output)
				}
			} else if got.err == nil || ctx.Err() == nil {
				t.Fatal("cancelled verification reported success", got.err)
			}
			until = time.Now().Add(2 * time.Second)
			for verificationChildRunning(t, child) && time.Now().Before(until) {
				time.Sleep(10 * time.Millisecond)
			}
			if verificationChildRunning(t, child) {
				t.Fatal("owned child remained alive after verification returned")
			}
			if verificationChildRunning(t, shell) {
				t.Fatal("verification shell remained alive after return")
			}
		})
	}
}
