package cli

import (
	"strings"
	"testing"
)

func TestOperatorServeIsReadOnlyAndUnavailableThroughMCP(t *testing.T) {
	cmd, _ := match([]string{"operator", "serve"})
	if cmd == nil || cmd.Mutates {
		t.Fatal("operator serve must remain read-only")
	}
	out, errOut, code := executor(t.TempDir())([]string{"operator", "serve", "--connection", "/private/operator.json"}, true)
	if code != 3 || out != "" || !strings.Contains(errOut, "local-only") {
		t.Fatalf("MCP reached app-owned operator stdio transport: stdout=%q stderr=%q code=%d", out, errOut, code)
	}
}
