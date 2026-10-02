package prompts

import (
	"strings"
	"testing"

	"github.com/mlnomadpy/dacli/internal/mdstore"
)

// The exact prose `dacli new` writes into a project, reproduced verbatim so
// this test fails the day that command's wording drifts away from the parser.
const pythonProject = `---
stage: build
---

# Recipes

## Constraints
Stack: Python. Build with ` + "`python -m build`" + `; test with ` + "`pytest`" + `. A task in this project is done only when both exit 0.

## Architecture
**Stack:** Python — scaffold with ` + "`python -m venv .venv`" + `, build with ` + "`python -m build`" + `, test with ` + "`pytest`" + `.
`

// A project as it existed before stacks were recorded: real sections, no
// Stack: line anywhere.
const legacyProject = `---
stage: build
---

# Core

## Constraints
Ship behind a flag. Keep the CLI's exit-code contract stable.

## Architecture
One package per bounded concern, no import cycles between concerns.
`

func parse(t *testing.T, raw string) Stack {
	t.Helper()
	d, err := mdstore.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return StackFromProject(d)
}

func TestStackFromProjectReadsRecordedStack(t *testing.T) {
	s := parse(t, pythonProject)
	if !s.Recorded() {
		t.Fatalf("stack not recognized: %+v", s)
	}
	if s.Label != "Python" || s.Key != "python" {
		t.Errorf("label/key = %q/%q, want Python/python", s.Label, s.Key)
	}
	if s.Build != "python -m build" || s.Test != "pytest" {
		t.Errorf("build/test = %q/%q", s.Build, s.Test)
	}
	if s.Format != "ruff format" {
		t.Errorf("format = %q, want ruff format", s.Format)
	}
	if s.IsGo() {
		t.Error("a Python project must not read as Go")
	}
}

// No recorded stack must be indistinguishable from pre-192: zero value, and
// every consumer's fallback path.
func TestStackAbsentOnLegacyProject(t *testing.T) {
	s := parse(t, legacyProject)
	if s.Recorded() || s != (Stack{}) {
		t.Errorf("legacy project produced a stack: %+v", s)
	}
	if s := (Stack{}); s.IsGo() {
		t.Error("the zero Stack must not claim to be Go")
	}
}

// A project doc that states its own formatter beats the per-key table — the
// table is a default, not an override.
func TestExplicitFormatCommandWins(t *testing.T) {
	s := ParseStack("Stack: Python. Format with `black .`; test with `pytest`.")
	if s.Format != "black ." {
		t.Errorf("format = %q, want black .", s.Format)
	}
}

// Half a record is not a record: without a Stack: line there is nothing to
// brand the commands with, so Recorded() must stay false.
func TestCommandsWithoutStackLineAreNotAStack(t *testing.T) {
	if s := ParseStack("Build with `make`; test with `make test`."); s.Recorded() {
		t.Errorf("bare commands became a stack: %+v", s)
	}
}

// The regression this task exists to fix: a Python project must not be told to
// run gofmt on .go files.
func TestGitWorkflowNoGofmtForPythonStack(t *testing.T) {
	out, err := Render("", "git_workflow", map[string]any{
		"Ref": "008", "Title": "t", "Branch": "b", "PR": true, "Exe": "dacli",
		"Stack": parse(t, pythonProject),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{"gofmt", "`.go`", "Go code"} {
		if strings.Contains(out, banned) {
			t.Errorf("python git_workflow leaks %q:\n%s", banned, out)
		}
	}
	for _, want := range []string{"ruff format", "python -m build", "pytest", "Python project"} {
		if !strings.Contains(out, want) {
			t.Errorf("python git_workflow missing %q:\n%s", want, out)
		}
	}
}

// Backwards compatibility, asserted rather than assumed: with no recorded
// stack — and with no Stack key at all, the shape any un-updated caller or
// workspace override passes — the prompt is byte-identical to the current native workflow text.
func TestGitWorkflowUnchangedWithoutStack(t *testing.T) {
	const legacyLine = "- Before you commit Go code, format it: run `gofmt -w` on every `.go` file you touched (test files included). CI runs `gofmt -l .` and REJECTS an unformatted file — an un-gofmt'd test is the most common way a green-locally change fails CI."
	base := map[string]any{"Ref": "008", "Title": "t", "Branch": "b", "PR": true, "Exe": "dacli"}

	for _, tc := range []struct {
		name string
		data map[string]any
	}{
		{"key absent", base},
		{"zero stack", map[string]any{"Ref": "008", "Title": "t", "Branch": "b", "PR": true, "Exe": "dacli", "Stack": Stack{}}},
		{"legacy project", map[string]any{"Ref": "008", "Title": "t", "Branch": "b", "PR": true, "Exe": "dacli", "Stack": parse(t, legacyProject)}},
	} {
		out, err := Render("", "git_workflow", tc.data)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if !strings.Contains(out, legacyLine) {
			t.Errorf("%s: pre-192 gofmt line lost:\n%s", tc.name, out)
		}
		// And the un-suffixed test-suite line, likewise untouched.
		if !strings.Contains(out, "- Run the project's test suite before declaring") {
			t.Errorf("%s: test-suite line changed:\n%s", tc.name, out)
		}
		// The strongest form of the contract: byte-identical to what the
		// template rendered before this task. testdata/git_workflow.md is a
		// frozen copy of the native workflow file, fed through the same override path
		// a workspace would use, so any drift in the stackless branch — a
		// stray newline from a template action included — fails here.
		if golden := renderGolden(t, tc.data); out != golden {
			t.Errorf("%s: stackless render drifted from the native workflow\n--- got ---\n%s\n--- want ---\n%s", tc.name, out, golden)
		}
	}
}

func renderGolden(t *testing.T, data map[string]any) string {
	t.Helper()
	out, err := Render("testdata", "git_workflow", data)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// A Go project that DOES record its stack keeps Go advice — the fix is about
// asking the project, not about deleting Go support.
func TestGoStackKeepsGofmt(t *testing.T) {
	s := ParseStack("Stack: Go. Build with `go build ./...`; test with `go test ./...`.")
	if !s.IsGo() || s.Format != "gofmt -w" {
		t.Fatalf("go stack misread: %+v", s)
	}
	out, err := Render("", "git_workflow", map[string]any{
		"Ref": "008", "Title": "t", "Branch": "b", "PR": false, "Exe": "dacli", "Stack": s,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "gofmt -w") || !strings.Contains(out, "go test ./...") {
		t.Errorf("go git_workflow lost its own toolchain:\n%s", out)
	}
}

func TestRoleForPrefersRosterOverInvention(t *testing.T) {
	py := ParseStack("Stack: Python. Build with `python -m build`; test with `pytest`.")

	// Nothing in the roster: today's default survives rather than a spawn
	// failing on a role that does not exist.
	none := func(string) bool { return false }
	if got := RoleFor(py, "auditor", "go-auditor", none); got != "go-auditor" {
		t.Errorf("empty roster: got %q, want go-auditor", got)
	}
	// A stack-specific role exists: take it.
	has := func(n string) bool { return n == "python-auditor" }
	if got := RoleFor(py, "auditor", "go-auditor", has); got != "python-auditor" {
		t.Errorf("got %q, want python-auditor", got)
	}
	// Only a generic one exists: still better than go-auditor.
	generic := func(n string) bool { return n == "auditor" }
	if got := RoleFor(py, "auditor", "go-auditor", generic); got != "auditor" {
		t.Errorf("got %q, want auditor", got)
	}
	// No recorded stack: the default, whatever the roster holds.
	if got := RoleFor(Stack{}, "auditor", "go-auditor", has); got != "go-auditor" {
		t.Errorf("stackless: got %q, want go-auditor", got)
	}
	// A recorded Go stack is already what the default names.
	goStack := ParseStack("Stack: Go. Build with `go build ./...`; test with `go test ./...`.")
	if got := RoleFor(goStack, "auditor", "go-auditor", has); got != "go-auditor" {
		t.Errorf("go stack: got %q, want go-auditor", got)
	}
	// impl phase: the default IS the generic name, so only a stack-specific
	// role can displace it.
	if got := RoleFor(py, "fixer", "fixer", func(n string) bool { return n == "fixer" }); got != "fixer" {
		t.Errorf("impl: got %q, want fixer", got)
	}
	if got := RoleFor(py, "fixer", "fixer", func(n string) bool { return n == "python-fixer" }); got != "python-fixer" {
		t.Errorf("impl: got %q, want python-fixer", got)
	}
}
