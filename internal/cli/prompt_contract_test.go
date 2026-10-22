package cli

import (
	"regexp"
	"strings"
	"testing"

	"github.com/mlnomadpy/dacli/internal/prompts"
)

func TestAutonomousPromptCommandsMatchRegistry(t *testing.T) {
	contract, err := prompts.AutonomousContract("")
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]Command{}
	for _, cmd := range commands {
		byPath[cmd.Path] = cmd
	}
	// Examples are deliberately provider-neutral dacli invocations. Resolve
	// each to the longest registered command path and require the command's
	// canonical Usage to start with that same path. This catches renamed or
	// removed commands without importing feature slices into prompts.
	re := regexp.MustCompile("`dacli ([^`]+)`")
	for _, match := range re.FindAllStringSubmatch(contract.Text, -1) {
		fields := strings.Fields(match[1])
		var found Command
		for n := len(fields); n > 0; n-- {
			if cmd, ok := byPath[strings.Join(fields[:n], " ")]; ok {
				found = cmd
				break
			}
		}
		if found.Path == "" {
			t.Errorf("prompt example names no registered command: dacli %s", match[1])
			continue
		}
		if !strings.HasPrefix(found.Usage, "dacli "+found.Path) {
			t.Errorf("%s has non-canonical Usage %q", found.Path, found.Usage)
		}
	}
	for _, exit := range []string{"0 success", "2 usage", "3 policy refusal (never retry)", "4 not found", "1 other failure"} {
		if !strings.Contains(contract.Text, exit) {
			t.Errorf("prompt omits exit-code contract %q", exit)
		}
	}
}

func TestNativePromptStatesAuthorityAndHostBoundaries(t *testing.T) {
	contract, err := prompts.AutonomousContract("")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"native host runs agents", "does not launch a coding", "shared MCP connection shares one session identity", "Self-reports remain assertions", "owner-issued", "does not select fallbacks"} {
		if !strings.Contains(contract.Text, want) {
			t.Errorf("native contract omits boundary %q", want)
		}
	}
	for _, forbidden := range []string{"`dacli spawn", "`dacli supervise", "`dacli loop", "`dacli start", "`dacli verify --task"} {
		if strings.Contains(contract.Text, forbidden) {
			t.Errorf("native contract recommends removed launcher %q", forbidden)
		}
	}
}
