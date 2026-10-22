package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// Exercise the actual command entrypoint with queued requests: a refused
// bootstrap must emit no JSON-RPC response, not merely fail a later tool call.
func mcpStartup(t *testing.T, dir string, args ...string) (string, string, int) {
	t.Helper()
	return mcpStartupScript(t, dir, "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\"}\n{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"whoami\",\"arguments\":{\"schema_version\":1}}}\n", args...)
}

func mcpStartupScript(t *testing.T, dir, script string, args ...string) (string, string, int) {
	t.Helper()
	in, err := os.CreateTemp(t.TempDir(), "mcp-input")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = in.Close() }() // Temporary input; all writes are checked below.
	_, err = in.WriteString(script)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := in.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	previous := os.Stdin
	os.Stdin = in
	defer func() { os.Stdin = previous }()
	var out, diagnostics bytes.Buffer
	ctx := &Ctx{Cwd: dir, Stdout: &out, Stderr: &diagnostics}
	cmd, _ := match([]string{"mcp", "serve"})
	err = invoke(ctx, cmd, args)
	if err != nil {
		diagnostics.WriteString(err.Error())
	}
	return out.String(), diagnostics.String(), exitCode(err)
}

func TestMCPStartupPreservesAgentMutationGate(t *testing.T) {
	dir := t.TempDir()
	run(t, dir, 0, "init", "--name", "mcp-ro")
	minted := run(t, dir, 0, "agent", "spawn", "--role", "reviewer", "--grant", "ro")
	token := strings.TrimSpace(strings.Split(minted, "\n")[0])
	t.Setenv("DACLI_AGENT", token)
	script := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"cli","arguments":{"schema_version":1,"argv":["project","add","Forbidden","--slug","forbidden","--goal","Must not be created."]}}}` + "\n"
	out, diagnostics, code := mcpStartupScript(t, dir, script)
	if code != 0 || !strings.Contains(out, "refused") || !strings.Contains(out, "rw grant") {
		t.Fatalf("RO bootstrap lost mutation gate: exit %d, stdout=%q diagnostics=%q", code, out, diagnostics)
	}
	if strings.Contains(out+diagnostics, token) {
		t.Fatal("mutation refusal disclosed credential")
	}
	if got := run(t, dir, 0, "project", "list"); strings.Contains(got, "Forbidden") {
		t.Fatalf("RO session created forbidden project: %s", got)
	}
}

func TestMCPStartupIdentity(t *testing.T) {
	dir := t.TempDir()
	run(t, dir, 0, "init", "--name", "mcp-startup")
	minted := run(t, dir, 0, "agent", "spawn", "--role", "reviewer", "--grant", "ro")
	token := strings.TrimSpace(strings.Split(minted, "\n")[0])
	for _, tc := range []struct {
		name, token string
		present     bool
		args        []string
		code        int
		want        string
	}{
		{name: "absent", code: 3, want: "DACLI_AGENT"},
		{name: "empty", present: true, code: 3, want: "empty"},
		{name: "unknown", present: true, token: "do-not-disclose-invalid-credential", code: 3, want: "not recognized"},
		{name: "operator", args: []string{"--operator"}, want: "a-root (grant: rw, role: root)"},
		{name: "operator-true", args: []string{"--operator=true"}, want: "a-root (grant: rw, role: root)"},
		{name: "operator-false", args: []string{"--operator=false"}, code: 3, want: "DACLI_AGENT"},
		{name: "operator-repeated-disabled", args: []string{"--operator", "--operator=false"}, code: 3, want: "DACLI_AGENT"},
		{name: "valid-agent", present: true, token: token, want: "grant: ro, role: reviewer"},
		{name: "operator-empty", present: true, args: []string{"--operator"}, code: 3, want: "DACLI_AGENT"},
		{name: "operator-invalid", present: true, token: "do-not-disclose-invalid-credential", args: []string{"--operator"}, code: 3, want: "DACLI_AGENT"},
		{name: "operator-agent", present: true, token: token, args: []string{"--operator"}, code: 3, want: "DACLI_AGENT"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DACLI_AGENT", tc.token)
			if !tc.present {
				if err := os.Unsetenv("DACLI_AGENT"); err != nil {
					t.Fatal(err)
				}
			}
			out, diagnostics, code := mcpStartup(t, dir, tc.args...)
			if code != tc.code {
				t.Fatalf("exit %d, want %d; stdout=%q diagnostics=%q", code, tc.code, out, diagnostics)
			}
			if tc.code != 0 && out != "" {
				t.Fatalf("refused startup processed requests: %q", out)
			}
			if !strings.Contains(out+diagnostics, tc.want) {
				t.Fatalf("missing %q in stdout=%q diagnostics=%q", tc.want, out, diagnostics)
			}
			if tc.token != "" && strings.Contains(out+diagnostics, tc.token) {
				t.Fatal("startup disclosed credential")
			}
		})
	}
	if got := run(t, dir, 0, "whoami"); !strings.Contains(got, "a-root (grant: rw, role: root)") {
		t.Fatalf("ordinary CLI identity changed: %s", got)
	}
}

func TestMCPStartupArgumentsAndWorkspace(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{{"stray"}, {"--operator", "stray"}, {"--unknown"}, {"--operator=garbage"}, {"--operator="}, {"--operator=0"}, {"--operator=garbage", "--operator"}, {"--operator", "--operator=garbage"}} {
		out, diagnostics, code := mcpStartup(t, dir, args...)
		if code != 2 || out != "" {
			t.Fatalf("%v: exit %d, stdout=%q diagnostics=%q", args, code, out, diagnostics)
		}
	}
	out, diagnostics, code := mcpStartup(t, dir, "--operator")
	if code != 4 || out != "" || !strings.Contains(diagnostics, "no dacli workspace") {
		t.Fatalf("operator skipped workspace validation: exit %d, stdout=%q diagnostics=%q", code, out, diagnostics)
	}
}

func TestMCPStartupDiscovery(t *testing.T) {
	got := run(t, t.TempDir(), 0, "mcp", "serve", "--help")
	if !strings.Contains(got, "--operator") || !strings.Contains(got, "DACLI_AGENT") {
		t.Fatalf("help omits bootstrap modes: %s", got)
	}
	for _, command := range capabilityManifest().Commands {
		if command.Path == "mcp serve" {
			if !strings.Contains(command.Usage, "--operator") {
				t.Fatalf("capability discovery omits operator mode: %#v", command)
			}
			return
		}
	}
	t.Fatal("MCP command missing from discovery")
}
