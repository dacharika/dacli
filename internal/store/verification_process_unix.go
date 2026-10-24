//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package store

import (
	"errors"
	"os/exec"
	"syscall"
)

// Native checks own their process group, including cooperative descendants. A
// context cancellation kills that group, not merely the shell holding its PID.
// Normal completion also removes background children left by a check. A child
// that deliberately leaves the group is outside this local cooperation boundary.
// WaitDelay on the caller bounds pipe draining; Cmd.Run reaps the direct child.
func runNativeVerificationCommand(cmd *exec.Cmd) (NativeVerificationTermination, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	killGroup := func() error {
		if cmd.Process == nil {
			return nil
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
	cmd.Cancel = killGroup
	err := cmd.Run()
	cleanupErr := killGroup()
	if cleanupErr != nil {
		knownTerminalGroup := cmd.Process != nil && cmd.ProcessState != nil &&
			errors.Is(cleanupErr, syscall.EPERM) &&
			verificationGroupCleanupSafeAfterEPERM(cmd.Process.Pid, cmd.Process.Pid)
		if !knownTerminalGroup {
			// Never certify cleanup from EPERM alone. Darwin may return EPERM for
			// a group containing only zombies; its platform-specific observer must
			// prove that state with a successful exact-group snapshot.
			return NativeVerificationTermination{}, &VerificationPolicyError{Reason: "native verification process cleanup unavailable"}
		}
	}
	state := "not_started"
	if cmd.Process != nil {
		if cmd.ProcessState == nil {
			return NativeVerificationTermination{}, err
		}
		// Cmd.Run reaped the direct child; group SIGKILL succeeded or ESRCH
		// was observed. This does not prove escaped descendants terminated.
		state = "owned_group_cleanup"
	}
	return NativeVerificationTermination{State: state}, err
}
