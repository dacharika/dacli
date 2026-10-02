package cli

import (
	"fmt"
	"os"
	"testing"

	"github.com/mlnomadpy/dacli/internal/agentid"
)

// TestMain strips DACLI_AGENT from the environment before any test runs.
// The commands resolve identity from the process env, so when the suite is
// executed *by a spawned dacli agent* (the tool developing itself), that
// child's token would leak in and make root-only operations like `project
// add` fail with "agent token not recognized". Clearing it once here makes
// the cli tests hermetic regardless of who launches them.
func TestMain(m *testing.M) {
	_ = os.Unsetenv(agentid.EnvVar)
	code := m.Run()
	// The shared binary must survive individual tests, then release its own
	// temporary directory after the complete suite has finished.
	if dacliBin.dir != "" {
		if err := os.RemoveAll(dacliBin.dir); err != nil {
			fmt.Fprintf(os.Stderr, "clean test binary: %v\n", err)
			code = 1
		}
	}
	os.Exit(code)
}
