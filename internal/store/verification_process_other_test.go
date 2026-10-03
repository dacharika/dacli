//go:build !darwin && !linux && !freebsd && !openbsd && !netbsd && !dragonfly

package store

import (
	"context"
	"os/exec"
	"testing"
)

func TestNativeVerificationUnsupportedProcessCleanupRefusesBeforeStart(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "unused-command")
	if err := runNativeVerificationCommand(cmd); !IsVerificationPolicyError(err) || cmd.Process != nil {
		t.Fatal("unsupported cleanup must refuse without starting a command", err)
	}
}
