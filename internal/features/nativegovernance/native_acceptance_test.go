package nativegovernance

import (
	"bytes"
	"github.com/mlnomadpy/dacli/internal/clikit"
	"testing"
)

func TestNativeAcceptanceCommandsRequireExplicitOwnerAndReviewer(t *testing.T) {
	for _, test := range []struct {
		handler func(*clikit.Ctx, []string) error
		args    []string
	}{{nativeAcceptanceUpgrade, nil}, {nativeAcceptanceUpgrade, []string{"--connection", "inherited"}}, {nativeAcceptanceCandidate, nil}, {nativeAcceptanceCandidate, []string{"--connection", "worker", "--review", "claimed"}}, {nativeAcceptanceCandidate, []string{"--connection", "worker", "--review-config", "reviewer", "--review", "claimed", "--force"}}} {
		ctx := &clikit.Ctx{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
		if e := test.handler(ctx, test.args); clikit.ExitCode(e) != 2 {
			t.Fatalf("native acceptance accepted unsupported/missing private binding: %v %v", test.args, e)
		}
		if ctx.Result != nil || ctx.Stdout.(*bytes.Buffer).Len() != 0 {
			t.Fatal("invalid private binding exported producer")
		}
	}
}
