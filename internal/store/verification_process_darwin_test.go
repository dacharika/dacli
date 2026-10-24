//go:build darwin

package store

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

const darwinTestZombieState = 5 // SZOMB from the Darwin SDK's sys/proc.h.

func TestVerificationGroupTerminalOnlyGuardsSnapshot(t *testing.T) {
	const pgid = 41001
	zombie := func(pid int) verificationProcessMember {
		return verificationProcessMember{PID: pid, PGID: pgid, State: darwinZombieProcessState}
	}
	validLargeSnapshot := func() []verificationProcessMember {
		members := make([]verificationProcessMember, maxVerificationGroupMembers+1)
		for i := range members {
			members[i] = zombie(pgid + i + 1)
		}
		return members
	}

	cases := []struct {
		name    string
		members []verificationProcessMember
		want    bool
	}{
		{name: "absent group", members: nil, want: true},
		{name: "single zombie", members: []verificationProcessMember{zombie(pgid + 1)}, want: true},
		{name: "multiple zombies", members: []verificationProcessMember{zombie(pgid + 1), zombie(pgid + 2)}, want: true},
		{name: "live member", members: []verificationProcessMember{{PID: pgid + 1, PGID: pgid, State: 2}}},
		{name: "mixed zombie and live", members: []verificationProcessMember{zombie(pgid + 1), {PID: pgid + 2, PGID: pgid, State: 2}}},
		{name: "unknown state", members: []verificationProcessMember{{PID: pgid + 1, PGID: pgid, State: 99}}},
		{name: "leader reused in group", members: []verificationProcessMember{zombie(pgid)}},
		{name: "invalid member pid", members: []verificationProcessMember{{PID: 0, PGID: pgid, State: darwinZombieProcessState}}},
		{name: "mismatched group", members: []verificationProcessMember{{PID: pgid + 1, PGID: pgid + 1, State: darwinZombieProcessState}}},
		{name: "duplicate member pid", members: []verificationProcessMember{zombie(pgid + 1), zombie(pgid + 1)}},
		{name: "oversized snapshot", members: validLargeSnapshot()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := verificationGroupTerminalOnly(pgid, pgid, tc.members); got != tc.want {
				t.Fatalf("verificationGroupTerminalOnly() = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestVerificationGroupCleanupSnapshotFailsClosed(t *testing.T) {
	const pgid = 42001
	zombie := []verificationProcessMember{{PID: pgid + 1, PGID: pgid, State: darwinZombieProcessState}}
	called := false
	if !verificationGroupCleanupSafeAfterEPERMSnapshot(pgid, pgid, func(gotPGID int) ([]verificationProcessMember, error) {
		called = true
		if gotPGID != pgid {
			t.Fatalf("snapshot group id = %d, want %d", gotPGID, pgid)
		}
		return zombie, nil
	}) || !called {
		t.Fatal("valid exact zombie snapshot was refused or query was skipped")
	}

	cases := []struct {
		name       string
		groupID    int
		leaderID   int
		members    []verificationProcessMember
		err        error
		queryCalls int
	}{
		{name: "query error with zombie rows", groupID: pgid, leaderID: pgid, members: zombie, err: errors.New("kernel snapshot unavailable"), queryCalls: 1},
		{name: "oversized successful snapshot", groupID: pgid, leaderID: pgid, members: func() []verificationProcessMember {
			members := make([]verificationProcessMember, maxVerificationGroupMembers+1)
			for i := range members {
				members[i] = verificationProcessMember{PID: pgid + i + 1, PGID: pgid, State: darwinZombieProcessState}
			}
			return members
		}(), queryCalls: 1},
		{name: "invalid group identity", groupID: pgid + 1, leaderID: pgid, members: zombie, queryCalls: 0},
		{name: "nonpositive group identity", groupID: 0, leaderID: 0, members: zombie, queryCalls: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			got := verificationGroupCleanupSafeAfterEPERMSnapshot(tc.groupID, tc.leaderID, func(int) ([]verificationProcessMember, error) {
				calls++
				return tc.members, tc.err
			})
			if got {
				t.Fatal("verificationGroupCleanupSafeAfterEPERMSnapshot() = true, want false")
			}
			if calls != tc.queryCalls {
				t.Fatalf("snapshot callback called %d times, want %d", calls, tc.queryCalls)
			}
		})
	}
	if verificationGroupCleanupSafeAfterEPERMSnapshot(pgid, pgid, nil) {
		t.Fatal("nil snapshot callback was accepted")
	}
}

func waitForDarwinZombieMember(t *testing.T, pgid, pid int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		members, err := unix.SysctlKinfoProcSlice("kern.proc.pgrp", pgid)
		if err != nil {
			t.Fatalf("observe owned process group: %v", err)
		}
		for _, member := range members {
			if int(member.Proc.P_pid) == pid && int(member.Eproc.Pgid) == pgid {
				if member.Proc.P_stat == darwinTestZombieState {
					return
				}
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("follower pid %d did not become an observable zombie in process group %d", pid, pgid)
}

func TestNativeVerificationCleanupConfirmsZombieOnlyGroup(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "leader-started")
	temporaryMarker := marker + ".tmp"
	quotedMarker := "'" + strings.ReplaceAll(marker, "'", "'\"'\"'") + "'"
	quotedTemporaryMarker := "'" + strings.ReplaceAll(temporaryMarker, "'", "'\"'\"'") + "'"
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "printf '%s' \"$$\" > "+quotedTemporaryMarker+"; mv "+quotedTemporaryMarker+" "+quotedMarker+"; exec /bin/sleep 30")
	type result struct {
		termination NativeVerificationTermination
		err         error
	}
	done := make(chan result, 1)
	go func() {
		termination, err := runNativeVerificationCommand(cmd)
		done <- result{termination: termination, err: err}
	}()

	var runResult result
	gotRunResult := false
	var follower *exec.Cmd
	followerWaited := false
	t.Cleanup(func() {
		cancel()
		if !gotRunResult {
			select {
			case runResult = <-done:
				gotRunResult = true
			case <-time.After(5 * time.Second):
				t.Error("native verification did not stop during cleanup")
			}
		}
		if follower != nil && !followerWaited {
			if follower.Process != nil {
				// This is an individual unreaped child handle, not a stale group ID.
				_ = follower.Process.Kill()
			}
			_ = follower.Wait()
		}
	})

	var leaderPID int
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if markerBytes, readErr := os.ReadFile(marker); readErr == nil {
			if parsedPID, parseErr := strconv.Atoi(string(markerBytes)); parseErr == nil && parsedPID > 0 {
				leaderPID = parsedPID
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if leaderPID <= 0 {
		t.Fatalf("leader command did not write a valid pid marker: %q", marker)
	}

	// This second direct child joins the real leader's process group and exits
	// naturally. Deliberately leave it unreaped so the kernel keeps its zombie
	// entry after the cancellation callback kills the live leader.
	follower = exec.Command("/bin/sh", "-c", "exit 0")
	follower.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: leaderPID}
	if err := follower.Start(); err != nil {
		t.Fatalf("start same-group follower: %v", err)
	}
	waitForDarwinZombieMember(t, leaderPID, follower.Process.Pid)

	// The callback kills the live leader and zombie follower; Cmd.Run reaps only
	// the leader. The post-Run group kill then sees the confirmed zombie-only
	// group and can return EPERM even though no member can execute.
	cancel()
	select {
	case runResult = <-done:
		gotRunResult = true
	case <-time.After(5 * time.Second):
		t.Fatal("native verification did not return after cancellation")
	}
	if runResult.err == nil {
		t.Fatal("canceled native command returned no error")
	}
	if runResult.termination.State != "owned_group_cleanup" {
		t.Fatalf("confirmed zombie-only group termination = %q, want owned_group_cleanup", runResult.termination.State)
	}

	followerErr := follower.Wait()
	followerWaited = true
	if followerErr != nil {
		var exit *exec.ExitError
		if !errors.As(followerErr, &exit) || exit.ExitCode() != 0 {
			t.Fatalf("reap naturally exited follower: %v", followerErr)
		}
	}
	if cmd.ProcessState == nil {
		t.Fatal("Cmd.Run did not record the direct leader process state")
	}
	waitStatus, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
	if !ok || !waitStatus.Signaled() || waitStatus.Signal() != syscall.SIGKILL {
		t.Fatalf("direct leader wait status = %v, want reaped SIGKILL", cmd.ProcessState.Sys())
	}
}
