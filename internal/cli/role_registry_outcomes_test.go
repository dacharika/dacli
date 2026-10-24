package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/mlnomadpy/dacli/internal/clikit"
)

func TestRoleRegistryOutcomesDispatcherRequiresExplicitOwnerAndRejectsFilters(t *testing.T) {
	for _, ambient := range []string{"", "invalid-legacy-token"} {
		t.Setenv("DACLI_AGENT", ambient)
		command, args := dispatch([]string{"role", "registry", "outcomes", "--config", filepath.Join(t.TempDir(), "missing-owner.json"), "--project", "core"})
		if command == nil || command.Path != "role registry outcomes" {
			t.Fatalf("outcomes command not registered in dispatcher: %#v", command)
		}
		ctx := &Ctx{JSON: true, Stdout: &strings.Builder{}, Stderr: &strings.Builder{}}
		if err := invoke(ctx, command, append(args, "--task", "private-task")); clikit.ExitCode(err) != 2 || !strings.Contains(err.Error(), "--task") {
			t.Fatalf("task filter bypassed closed CLI flags: %v", err)
		}
		if _, diagnostic, code := executor(t.TempDir())([]string{"role", "registry", "outcomes"}, true); code != 3 || !strings.Contains(diagnostic, "local-only") {
			t.Fatalf("legacy MCP route exposed owner-only outcomes: exit%d %s", code, diagnostic)
		}
	}
}
