package cli

import (
	"strings"
	"testing"
)

// Check both dispatch doors and hidden process entry points. An unadvertised
// launch command would still restore the removed provider lifecycle.
func TestRemovedProviderLaunchCommandsAreUnreachable(t *testing.T) {
	for _, argv := range [][]string{
		{"spawn"}, {"supervise"}, {"start"}, {"loop"}, {"loop", "status"},
		{"runtime", "add"}, {"runtime", "rm"}, {"runtime", "list"}, {"runtime", "doctor"},
		{"verify"}, {"kill"}, {"wait"}, {"claim", "expand"},
		{"__run-guardian", "fixture"}, {"__run-watchdog", "fixture"},
	} {
		t.Run(strings.Join(argv, "-"), func(t *testing.T) {
			if cmd, _ := match(argv); cmd != nil {
				t.Fatalf("removed provider launch command remains registered: %s", cmd.Path)
			}
			out, diagnostic, code := executor(t.TempDir())(argv, false)
			if code != 2 || out != "" || !strings.Contains(diagnostic, "unknown command") {
				t.Fatalf("legacy MCP reached removed command %v: code%d out%q diagnostic%s", argv, code, out, diagnostic)
			}
			if code = Main(argv); code != 2 {
				t.Fatalf("CLI reached removed launch entry point %v: code%d", argv, code)
			}
		})
	}
}
