package nativegovernance

import (
	"bytes"
	"github.com/mlnomadpy/dacli/internal/clikit"
	"testing"
)

func TestNativePolicyCommandsExplicitAuthority(t *testing.T) {
	for _, h := range []func(*clikit.Ctx, []string) error{nativePolicyUpgrade, nativePolicyConfigure, nativePolicyEvaluate, nativePolicyApprove, nativePolicyStatus} {
		for _, args := range [][]string{nil, {"--connection", "worker"}, {"--config", "owner", "--force"}} {
			ctx := &clikit.Ctx{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
			if e := h(ctx, args); clikit.ExitCode(e) != 2 {
				t.Fatal("native policy accepted implicit/unsupported authority", e)
			}
			if ctx.Result != nil || ctx.Stdout.(*bytes.Buffer).Len() != 0 {
				t.Fatal("invalid policy request exported authority")
			}
		}
	}
}
