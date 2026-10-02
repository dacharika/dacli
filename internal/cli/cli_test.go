package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// Main intercepts "help" for usage before dispatch ever runs, so any command
// path beginning with "help" is silently unreachable. This shipped once —
// "help ask", "help answer", and "help escalate" were all dead on arrival,
// and the duplicate-path test could not see it.
func TestNoCommandShadowedByReservedWords(t *testing.T) {
	for _, c := range commands {
		first := strings.SplitN(c.Path, " ", 2)[0]
		switch first {
		case "help", "-h", "--help":
			t.Errorf("command %q is unreachable: %q is intercepted before dispatch", c.Path, first)
		}
	}
}

// A duplicate command silently shadows the later registration and prints
// twice in help. This shipped once already; it should not ship again.
func TestNoDuplicateCommandPaths(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range commands {
		if seen[c.Path] {
			t.Errorf("duplicate command path %q", c.Path)
		}
		seen[c.Path] = true
	}
}

func TestEveryCommandHasABrief(t *testing.T) {
	for _, c := range commands {
		if c.Path == "" {
			t.Error("command with an empty path")
		}
		if c.Brief == "" {
			t.Errorf("command %q has no description", c.Path)
		}
		if c.Run == nil {
			t.Errorf("command %q has no Run function", c.Path)
		}
	}
}

// Longest-path matching must prefer "task add" over any bare "task", so that
// adding a bare parent command later cannot hijack its subcommands.
func TestMatchPrefersLongestPath(t *testing.T) {
	cmd, rest := match([]string{"task", "add", "Audit the write paths"})
	if cmd == nil {
		t.Fatal("no match for \"task add\"")
	}
	if cmd.Path != "task add" {
		t.Errorf("matched %q, want \"task add\"", cmd.Path)
	}
	if len(rest) != 1 || rest[0] != "Audit the write paths" {
		t.Errorf("rest = %v, want the trailing argument only", rest)
	}
}

func TestMatchReturnsNilForUnknown(t *testing.T) {
	if cmd, _ := match([]string{"frobnicate"}); cmd != nil {
		t.Errorf("matched %q for an unknown command", cmd.Path)
	}
}

func TestUnknownCommandExitsNonZero(t *testing.T) {
	if code := Main([]string{"frobnicate"}); code == 0 {
		t.Error("unknown command should exit non-zero")
	}
}

func TestHelpExitsZero(t *testing.T) {
	if code := Main([]string{"help"}); code != 0 {
		t.Errorf("help exit code = %d, want 0", code)
	}
}

func TestTopLevelHelpLeadsWithBoundedWorkflowAndKeepsFullCatalogDiscoverable(t *testing.T) {
	var out bytes.Buffer
	usage(&out)
	got := out.String()
	for _, want := range []string{
		"inspect → plan → claim → implement → verify → review → PR → CI → merge",
		"native / overview / next / task",
		"dacli help --all",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("top-level help missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "task acceptance migrate") {
		t.Errorf("top-level help regressed to the flat expert catalog:\n%s", got)
	}

	var all bytes.Buffer
	usageAll(&all)
	if !strings.Contains(all.String(), "task acceptance migrate") {
		t.Errorf("advanced command disappeared from --all catalog:\n%s", all.String())
	}
}

func TestParentHelpListsLeavesWithoutExecutingThem(t *testing.T) {
	var out bytes.Buffer
	if !printParentHelp(&out, "task") {
		t.Fatal("task should be a command family")
	}
	got := out.String()
	for _, want := range []string{"task add", "task claim", "task done", "task acceptance migrate"} {
		if !strings.Contains(got, want) {
			t.Errorf("task parent help missing %q:\n%s", want, got)
		}
	}
	if printParentHelp(&out, "not-a-family") {
		t.Error("unknown parent reported as a family")
	}
	if exact, _ := match([]string{"task"}); exact != nil {
		t.Fatalf("task parent unexpectedly resolves to leaf %s; task --help would execute leaf-help routing", exact.Path)
	}
	if exact, _ := match([]string{"pr"}); exact == nil {
		t.Fatal("pr leaf disappeared; pr --help must keep the leaf synopsis even though pr status also exists")
	}

	parentOut := captureMainStdout(t, []string{"task", "--help"})
	if !strings.Contains(parentOut, "task acceptance migrate") {
		t.Errorf("task --help did not reach parent help:\n%s", parentOut)
	}
	leafOut := captureMainStdout(t, []string{"pr", "--help"})
	if !strings.Contains(leafOut, "--task") {
		t.Errorf("pr --help hid the pr leaf behind parent navigation:\n%s", leafOut)
	}
}

func captureMainStdout(t *testing.T, args []string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stdout
	os.Stdout = f
	t.Cleanup(func() { os.Stdout = previous })
	if code := Main(args); code != 0 {
		t.Fatalf("Main(%v) exit %d", args, code)
	}
	os.Stdout = previous
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
