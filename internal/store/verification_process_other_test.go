//go:build !darwin && !linux && !freebsd && !openbsd && !netbsd && !dragonfly

package store

import (
	"context"
	"os/exec"
	"testing"
)

func TestNativeVerificationUnsupportedProcessCleanupRefusesBeforeStart(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "unused-command")
	if terminal, err := runNativeVerificationCommand(cmd); !IsVerificationPolicyError(err) || cmd.Process != nil || terminal.State != "" {
		t.Fatal("unsupported cleanup must refuse without starting a command", err)
	}
}
