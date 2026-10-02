package sessions

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"reflect"
	"testing"
	"time"
)

type leaseCrashInput struct {
	Path, Token, Session string
	Request              AcquireLeaseRequest
}

// Only tests set this mode; credentials travel over private stdin, not argv.
func TestLeaseCrashChild(t *testing.T) {
	mode := os.Getenv("LEASE_TEST_CRASH_MODE")
	if mode == "" {
		return
	}
	var in leaseCrashInput
	if err := json.NewDecoder(io.LimitReader(os.Stdin, 1<<20)).Decode(&in); err != nil {
		t.Fatal(err)
	}
	a, err := Open(in.Path, Binding{"workspace", "installation"}, &testAdmission{})
	if err != nil {
		t.Fatal(err)
	}
	if mode == "hold-lock" {
		err = a.transaction(false, true, func(*state) error {
			if _, err := io.WriteString(os.Stdout, "lock-held\n"); err != nil {
				return err
			}
			// The parent terminates this exact child after observing the callback.
			time.Sleep(time.Minute)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	l, err := a.AcquireLease(in.Token, in.Session, in.Request)
	if err != nil {
		t.Fatal(err)
	}
	if mode == "lose-reply" {
		return
	}
	if mode != "replay" {
		t.Fatal("unknown child mode")
	}
	if err := json.NewEncoder(os.Stdout).Encode(l); err != nil {
		t.Fatal(err)
	}
}

func leaseCrashCommand(t *testing.T, mode string, in leaseCrashInput) *exec.Cmd {
	t.Helper()
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLeaseCrashChild$")
	cmd.Env = append(childEnvironment(), "LEASE_TEST_CRASH_MODE="+mode)
	cmd.Stdin = bytes.NewReader(raw)
	return cmd
}

func TestLeaseCrashPreservesLockAndAuthority(t *testing.T) {
	a, _, s, token, at, _ := leaseSetup(t)
	in := leaseCrashInput{a.path, token, s.ID, acquireReq(at, "after-crash", "src/a")}
	before, err := os.ReadFile(a.path)
	if err != nil {
		t.Fatal(err)
	}
	cmd := leaseCrashCommand(t, "hold-lock", in)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	t.Cleanup(func() {
		if !waited {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	ready := make(chan string, 1)
	go func() { line, _ := bufio.NewReader(out).ReadString('\n'); ready <- line }()
	select {
	case line := <-ready:
		if line != "lock-held\n" {
			t.Fatalf("transaction did not acquire lock: %q", line)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("lock callback was not reached")
	}
	lockBefore, err := os.Stat(a.path + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	err = cmd.Wait()
	waited = true
	var exit *exec.ExitError
	if !errors.As(err, &exit) || cmd.ProcessState == nil || cmd.ProcessState.Success() {
		t.Fatalf("child crash/reap not proven: %v", err)
	}
	start := time.Now()
	_, err = a.AcquireLease(token, s.ID, in.Request)
	if !errors.Is(err, ErrStorage) {
		t.Fatalf("crashed lock must refuse, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 8*time.Second {
		t.Fatalf("unbounded lock refusal: %s", elapsed)
	}
	after, err := os.ReadFile(a.path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("crashed lock changed authority")
	}
	lockAfter, err := os.Stat(a.path + ".lock")
	if err != nil || !os.SameFile(lockBefore, lockAfter) {
		t.Fatal("crashed lock was removed or replaced")
	}
	// Explicit fixture-owner reconciliation, after the exact child is reaped.
	if err := os.Remove(a.path + ".lock"); err != nil {
		t.Fatal(err)
	}
	l := mustAcquire(t, a, s, token, in.Request)
	if l.Fence != 1 {
		t.Fatalf("refused crash attempt allocated fence %d", l.Fence)
	}
}

func TestLeaseLostReplyReplayAcrossProcesses(t *testing.T) {
	a, _, s, token, at, _ := leaseSetup(t)
	req := acquireReq(at, "lost-reply", "src/a")
	in := leaseCrashInput{a.path, token, s.ID, req}
	if out, err := leaseCrashCommand(t, "lose-reply", in).CombinedOutput(); err != nil {
		t.Fatalf("commit child failed: %s %v", out, err)
	}
	var first Lease
	if err := a.transaction(false, false, func(st *state) error {
		if st.Fence != 1 || len(st.Leases) != 1 || len(st.Operations) != 1 {
			t.Fatal("lost-reply commit missing")
		}
		first = st.Operations[req.OperationKey].Receipt
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_, err := a.ReleaseLease(token, s.ID, ReleaseLeaseRequest{"release", at.ID, first.ID, first.Fence})
	if err != nil {
		t.Fatal(err)
	}
	second := mustAcquire(t, a, s, token, acquireReq(at, "new-acquisition", "src/a"))
	if second.Fence != 2 {
		t.Fatal("fixture did not advance fence")
	}
	before, err := os.ReadFile(a.path)
	if err != nil {
		t.Fatal(err)
	}
	out, err := leaseCrashCommand(t, "replay", in).CombinedOutput()
	if err != nil {
		t.Fatalf("lost reply replay failed: %s %v", out, err)
	}
	var replay Lease
	if err := json.NewDecoder(bytes.NewReader(out)).Decode(&replay); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, replay) {
		t.Fatal("lost reply did not recover exact historical receipt")
	}
	after, err := os.ReadFile(a.path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("historical replay changed authority or fence")
	}
	changed := req
	changed.Resources = []Resource{{"path", "src/b"}}
	_, err = a.AcquireLease(token, s.ID, changed)
	denied(t, err)
	_, err = a.RenewLease(token, s.ID, RenewLeaseRequest{"stale-renew", at.ID, replay.ID, replay.Fence, time.Now().Add(time.Minute)})
	denied(t, err)
}
