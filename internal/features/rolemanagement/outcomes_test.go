package rolemanagement

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/mlnomadpy/dacli/internal/clikit"
)

func outcomeCommand(t *testing.T) clikit.Command {
	t.Helper()
	for _, command := range OutcomeCommands() {
		if command.Path == outcomeCommandPath {
			return command
		}
	}
	t.Fatal("outcomes command was not declared")
	return clikit.Command{}
}

func TestRoleRegistryOutcomesRejectsIdentityAndArbitraryFilters(t *testing.T) {
	command := outcomeCommand(t)
	for _, flag := range []string{"--user", "--agent", "--task", "--role", "--group-by", "--from", "--minimum-tasks"} {
		err := command.Run(roleRegistryContext(), []string{"--config", "owner.json", "--project", "core", flag, "value"})
		if clikit.ExitCode(err) != 2 || err == nil || !strings.Contains(err.Error(), flag) {
			t.Errorf("filter %s was not rejected as usage: %v", flag, err)
		}
	}
	if !AuthenticatesOutcomeExplicitly(outcomeCommandPath) || AuthenticatesOutcomeExplicitly("role registry verify") {
		t.Fatal("explicit-authentication classifier does not isolate outcomes command")
	}
}

func TestRoleRegistryOutcomesRequiresPrivateOwnerAndExactProject(t *testing.T) {
	command := outcomeCommand(t)
	missing := filepath.Join(t.TempDir(), "missing-owner.json")
	err := command.Run(roleRegistryContext(), []string{"--config", missing, "--project", "core"})
	if clikit.ExitCode(err) != 3 || err == nil || !strings.Contains(err.Error(), "explicit private owner") {
		t.Fatalf("missing owner should be refused after exact argument parsing: %v", err)
	}
	err = command.Run(roleRegistryContext(), []string{"--config", missing})
	if clikit.ExitCode(err) != 2 || err == nil || !strings.Contains(err.Error(), "--project is required") {
		t.Fatalf("missing project should be usage error: %v", err)
	}
}
