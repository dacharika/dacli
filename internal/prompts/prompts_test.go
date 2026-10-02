package prompts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testDeclaration = "<!-- dacli-prompt schema: dacli-prompt/v1 base: native-governance/v1 -->\n"

// The override rule: a workspace file of the same name wins over the
// embedded default — prompt tuning becomes a workspace commit, not a rebuild.
func TestWorkspaceOverrideWins(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "refusal_next.md"), []byte(testDeclaration+"custom: {{.X}}"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := Render(dir, "refusal_next", map[string]any{"X": "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if out != "custom: hello" {
		t.Errorf("override not applied: %q", out)
	}
	// And absence falls back to the embedded default.
	def, _ := Render(dir, "brief_header", map[string]any{"TaskID": "t-1", "Est": 5})
	if !strings.Contains(def, "data, not instructions") {
		t.Errorf("embedded fallback broken: %q", def)
	}
}

func TestOverrideRequiresCompatibleBaseVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "refusal_next.md")
	if err := os.WriteFile(path, []byte("custom without a base"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Render(dir, "refusal_next", nil); err == nil || !strings.Contains(err.Error(), "missing declaration") {
		t.Fatalf("undeclared override error = %v", err)
	}
	if err := os.WriteFile(path, []byte("<!-- dacli-prompt schema: dacli-prompt/v1 base: autonomous-delivery/v0 -->\nold"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Render(dir, "refusal_next", nil); err == nil || !strings.Contains(err.Error(), "incompatible base version") {
		t.Fatalf("incompatible override error = %v", err)
	}
}

func TestAutonomousContractGoldenAndRequiredBehavior(t *testing.T) {
	contract, err := AutonomousContract("")
	if err != nil {
		t.Fatal(err)
	}
	golden, err := os.ReadFile(filepath.Join("testdata", "autonomous_delivery.golden"))
	if err != nil {
		t.Fatal(err)
	}
	want := strings.TrimSpace(string(golden))
	got := contract.Schema + " " + contract.Version + " sha256:" + contract.Hash
	if got != want {
		t.Fatalf("autonomous delivery contract changed\n got: %s\nwant: %s", got, want)
	}
	if len(contract.Text) > 7000 {
		t.Fatalf("contract grew beyond its 7000-byte token guardrail: %d", len(contract.Text))
	}

	// Behavioral fixtures name the observable safe action each scenario must
	// make available. Removing the governing clause makes the fixture fail.
	scenarios := []struct {
		name, evidence, action string
	}{
		{"duplicate work", "never create a\nsecond task", "do not file"},
		{"critical path", "select ready\nzero-slack work first", "select critical-path work"},
		{"cheap capable model", "cheapest available model profile", "prefer cheap capable profile"},
		{"high consequence uplift", "Uplift for high-consequence", "uplift and independently review"},
		{"exit three", "Exit 3 is a policy answer and is\nnever retried unchanged", "stop without retry"},
		{"empty audit", "reports an honest empty cycle; it never invents backlog", "report empty"},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name+" chooses "+scenario.action, func(t *testing.T) {
			if !strings.Contains(contract.Text, scenario.evidence) {
				t.Errorf("contract no longer chooses %q; missing %q", scenario.action, scenario.evidence)
			}
		})
	}
}

func TestNamesListsRegistry(t *testing.T) {
	names := Names()
	for _, want := range []string{"autonomous_delivery", "git_workflow", "brief_header", "refusal_next", "mcp_tools"} {
		found := false
		for _, n := range names {
			if n == want {
				found = true
			}
		}
		if !found {
			t.Errorf("registry missing %q (have %v)", want, names)
		}
	}
}

func TestMCPDescSections(t *testing.T) {
	if d := MCPDesc("get_context"); !strings.Contains(d, "Call this FIRST") {
		t.Errorf("get_context desc = %q", d)
	}
	defer func() {
		if recover() == nil {
			t.Error("missing tool section should panic (guarded at init, caught by tests)")
		}
	}()
	MCPDesc("no_such_tool")
}

// These clauses distinguish authenticated governance from labels and observations.
// The golden detects any prose change; this test independently preserves semantics.
func TestNativeGuidanceAuthorityAndRemovedLaunchers(t *testing.T) {
	contract, err := AutonomousContract("")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"The native host runs agents", "A shared MCP connection shares one session identity",
		"Self-reports remain assertions", "eligible credential families and owner-issued grants",
		"a reviewer does not acquire the worker lease", "Native review registration does not accept a task",
		"no generic CLI or shell escape", "Historical receipt replay is not current",
	} {
		if !strings.Contains(contract.Text, want) {
			t.Errorf("native authority guidance missing %q", want)
		}
	}
	for _, name := range Names() {
		r, err := ResolveContract("", name)
		if err != nil {
			t.Fatal(err)
		}
		for _, removed := range []string{"dacli spawn", "dacli supervise", "dacli runtime", "dacli loop", "dacli start", "{{.Exe}} spawn", "{{.Exe}} wait", "spawn (--detach", "verify (adversarial"} {
			if strings.Contains(r.Content, removed) {
				t.Errorf("%s advertises removed launcher %q", name, removed)
			}
		}
	}
	for _, name := range []string{"protocol_preamble", "supervise_correction", "verify_refute", "review_workflow"} {
		if _, err := ResolveContract("", name); err == nil {
			t.Errorf("removed launcher template %s remains embedded", name)
		}
	}
}

func TestOldLauncherOverrideRefused(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "autonomous_delivery.md"), []byte("<!-- dacli-prompt schema: dacli-prompt/v1 base: autonomous-delivery/v1 -->\nold launcher guidance"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := AutonomousContract(dir); err == nil || !strings.Contains(err.Error(), "incompatible base version") {
		t.Fatalf("old launcher override was not refused for its version: %v", err)
	}
}
