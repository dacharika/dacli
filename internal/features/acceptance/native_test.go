package acceptance

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
)

func TestNativeAcceptanceExplicitFlagsAndPartialPolicyExit(t *testing.T) {
	for _, args := range [][]string{{}, {"task-a", "--config", "owner", "--review-policy", "policy", "--request", "request"}, {"--force"}, {"--config", "owner", "--review-policy", "policy", "--request", "request", "--base", "main"}} {
		ctx := &clikit.Ctx{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
		if e := cmdAcceptApplyNative(ctx, args); clikit.ExitCode(e) != 2 {
			t.Fatalf("native acceptance accepted unsupported/missing explicit flags: %v %v", args, e)
		}
		if ctx.Result != nil || ctx.Stdout.(*bytes.Buffer).Len() != 0 {
			t.Fatal("invalid request published acceptance result")
		}
	}
	e := nativeAcceptanceError(errors.Join(sessions.ErrDenied, sessions.ErrReconcile))
	if clikit.ExitCode(e) != 3 || !strings.Contains(e.Error(), "reconciliation") {
		t.Fatalf("partial exact-effect diagnosis lost policy exit3: %v", e)
	}
	if clikit.ExitCode(nativeAcceptanceError(sessions.ErrDenied)) != 3 {
		t.Fatal("authority refusal exit changed")
	}
	if clikit.ExitCode(nativeAcceptanceError(sessions.ErrStorage)) != 1 {
		t.Fatal("storage failure falsely reported policy result")
	}
}
