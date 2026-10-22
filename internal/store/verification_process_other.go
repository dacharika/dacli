//go:build !darwin && !linux && !freebsd && !openbsd && !netbsd && !dragonfly

package store

import "os/exec"

// Do not silently fall back to killing only a shell on platforms without the
// owned process-group cleanup implementation.
func runNativeVerificationCommand(_ *exec.Cmd) (NativeVerificationTermination, error) {
	return NativeVerificationTermination{}, &VerificationPolicyError{Reason: "native verification process cleanup unsupported on this platform"}
}
