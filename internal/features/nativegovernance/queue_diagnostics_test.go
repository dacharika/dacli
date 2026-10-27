package nativegovernance

import (
	"strings"
	"testing"

	"github.com/mlnomadpy/dacli/internal/clikit"
)

func TestNativeQueueDiagnosticsCLIReadOnlyAndFlagContract(t *testing.T) {
	var command *clikit.Command
	for i := range QueueDiagnosticsCommands {
		if QueueDiagnosticsCommands[i].Path == "native queue diagnostics" {
			command = &QueueDiagnosticsCommands[i]
			break
		}
	}
	if command == nil || !command.JSON || command.Mutates || !strings.Contains(command.Usage, "--connection") || !strings.Contains(command.Usage, "--project") {
		t.Fatalf("queue diagnostics command contract is incomplete: %+v", command)
	}
	if err := command.Run(&clikit.Ctx{}, []string{"--bogus"}); err == nil || !strings.Contains(err.Error(), "--bogus") {
		t.Fatalf("unknown flag refusal did not name the flag: %v", err)
	}
	if err := command.Run(&clikit.Ctx{}, nil); clikit.ExitCode(err) != 2 || !strings.Contains(err.Error(), "--connection") {
		t.Fatalf("missing connection did not produce named usage exit 2: %v", err)
	}
	if err := command.Run(&clikit.Ctx{}, []string{"--connection", "/private/reader.json", "--project", "p", "--limit", "101"}); clikit.ExitCode(err) != 2 || !strings.Contains(err.Error(), "--limit") {
		t.Fatalf("out-of-range limit did not produce usage exit 2: %v", err)
	}
}
