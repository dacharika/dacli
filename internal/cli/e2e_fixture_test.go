package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The host-owned fixture: a native worker implements a change, and the
// explicit owner delivery path lands it, and the test asserts the outcome rather than the calls
// (issue #437 item 5).
//
// Why this could not be a unit test. Every guard along this arc was already
// unit-correct and green while the arc itself was broken — that is how task
// 312's three-way deadlock survived, and how issue #382 reported done:15/21
// with the commands absent. The only thing that catches a composition is
// running the composition.
//
// It drives the REAL binary as a subprocess, not the in-process dispatcher,
// because `ship` re-invokes dacli through os.Executable() — which under
// `go test` is the test binary, so an in-process ship re-enters the suite
// instead of running its accept step.
//
// Offline and deterministic: the "agent" is a shell script, so there is no
// model, no network, and no clock dependence. What is being proven is explicit owner delivery
// coordination, and a real agent would only add variance to a question that is
// not about the agent.
func TestE2EFixtureRepoGoesFromEmptyToShipped(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("needs a POSIX shell for the stub agent")
	}
	bin := buildDacli(t)
	dir := t.TempDir()
	gitInit(t, dir)
	installRestrictedGitShim(t, filepath.Dir(bin))

	// The stub agent shells `dacli` (commit, task done), so the binary this
	// test just built has to be on the CHILD's PATH. It was not: the runtime
	// passes PATH through, and on a developer machine dacli happens to be
	// installed, so the fixture passed locally and failed on every CI runner —
	// a test that only works where the tool is already installed proves
	// nothing about a clean checkout, which is exactly what this fixture is
	// for.
	t.Setenv("PATH", filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"))

	// --- plan -------------------------------------------------------------
	run(t, dir, 0, "init", "--name", "fixture")
	run(t, dir, 0, "project", "add", "Adder", "--slug", "adder",
		"--goal", "Provide an add function with a test that proves it")
	run(t, dir, 0, "task", "add", "Implement Add and cover it with a test",
		"--project", "adder",
		"--accept", "adder.go defines Add",
		"--accept", "go test ./... passes")

	// --- the agent --------------------------------------------------------
	// A stub that does what an implementer does: write code, then report
	// through the protocol verbs. It uses `task done`, the verb agents are
	// actually told to use and the one whose proposals nothing consumed until
	// task 312.
	writeNativeWorkerFixture(t, dir, "worker", strings.Join([]string{
		`set -e`,
		`cat > adder.go <<'EOF'`,
		`package adder`,
		``,
		`// Add returns the sum of a and b.`,
		`func Add(a, b int) int { return a + b }`,
		`EOF`,
		`cat > adder_test.go <<'EOF'`,
		`package adder`,
		``,
		`import "testing"`,
		``,
		`func TestAdd(t *testing.T) {`,
		`	if Add(2, 2) != 4 { t.Fatal("Add is wrong") }`,
		`}`,
		`EOF`,
		`printf 'module adder\n\ngo 1.22\n' > go.mod`,
		`git add -A`,
		`dacli commit "Implement Add and cover it with a test" --task 001 --no-add`,
		// `task done`, NOT `task check`: a spawned agent proposes and the
		// owner applies. Checking its own boxes is refused by design, and
		// `task done` is the verb whose proposals nothing consumed until
		// task 312 — so this arc walks straight through that deadlock.
		`dacli task done 001`,
	}, "\n"))

	// --- implement --------------------------------------------------------
	// --worktree and a real --claim, because that is how the loop spawns: the
	// worktree gives the agent its task branch (dacli commit refuses to commit
	// on trunk), and the claim is the scope guard. Both refused this fixture
	// on the way to working, which is the point of driving the real path.
	executeNativeWorkerFixture(t, dir, "001", "worker", "adder.go,adder_test.go,go.mod")

	// --- land -------------------------------------------------------------
	// The TOOL closes its own loop. The test never runs `accept --force`: a
	// test that reconciles by hand proves the command works when a human runs
	// it, which was never in doubt.
	dacliRun(t, bin, dir, "sync")
	dacliRun(t, bin, dir, "ship", "--project", "adder", "--into", "main")

	// --- assert the OUTCOME, not the calls --------------------------------

	// 1. The code exists on TRUNK, not merely on a branch. A task marked done
	//    over code that never landed is issue #382's exact failure.
	if out := gitOut(t, dir, "show", "main:adder.go"); !strings.Contains(out, "func Add") {
		t.Fatalf("Add never reached trunk:\n%s", out)
	}

	// 2. The tests the agent wrote actually pass. Nothing above checks that
	//    the code WORKS — the loop is happy to land something broken.
	if out, err := goTestIn(dir); err != nil {
		t.Fatalf("the shipped code does not pass its own tests: %v\n%s", err, out)
	}

	// 3. The record agrees: the task is done and its boxes are checked.
	listing := dacliRun(t, bin, dir, "task", "list", "--project", "adder")
	if !strings.Contains(listing, "done") {
		t.Errorf("the task did not close:\n%s", listing)
	}
	if strings.Contains(listing, "[0/2]") {
		t.Errorf("the task closed with unchecked acceptance boxes:\n%s", listing)
	}

	// 4. AND IT DID NOT SILENTLY PRODUCE NOTHING. This is the assertion the
	//    whole fixture exists for: every step above can "succeed" while doing
	//    nothing, which is this repo's most expensive failure class. A trunk
	//    that never moved is the one signal that catches all of them at once.
	if out := gitOut(t, dir, "log", "--oneline", "main"); !strings.Contains(out, "Implement Add and cover it with a test") {
		t.Fatalf("trunk never advanced — every step reported success and nothing shipped:\n%s", out)
	}
}

// installRestrictedGitShim keeps a macOS sandbox diagnostic out of git's
// machine-readable stdout. In the restricted agent sandbox git starts and
// succeeds, but prints a confstr warning; dacli's commit claim gate consumes
// combined git output and otherwise mistakes that warning for a staged path.
// The shim preserves git's status and all other stderr, so this fixture still
// exercises the real claim enforcement instead of bypassing it with --force.
func installRestrictedGitShim(t *testing.T, binDir string) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("needs git for the self-hosting fixture")
	}
	shim := filepath.Join(binDir, "git")
	script := fmt.Sprintf(`#!/bin/sh
stderr_file="${TMPDIR:-/tmp}/dacli-fixture-git-stderr-$$"
%q "$@" 2>"$stderr_file"
status=$?
sed '/^git: warning: confstr() failed with code 5: couldn.t get path of DARWIN_USER_TEMP_DIR; using \/tmp instead$/d' "$stderr_file" >&2
rm -f "$stderr_file"
exit "$status"
`, realGit)
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func fixtureWorkerDiagnostic(spawnOutput string) string {
	const marker = "transcript: "
	for _, line := range strings.Split(spawnOutput, "\n") {
		path, ok := strings.CutPrefix(strings.TrimSpace(line), marker)
		if !ok {
			continue
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return fmt.Sprintf("child transcript unavailable: %v", err)
		}
		if len(body) == 0 {
			return "child transcript: <empty>"
		}
		return "child transcript:\n" + string(body)
	}
	return "child transcript path was not reported"
}

func TestFixtureWorkerDiagnosticReadsTranscriptBeforeCleanup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.log")
	if err := os.WriteFile(path, []byte("sandbox: worker startup denied\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	diagnostic := fixtureWorkerDiagnostic("run failed\ntranscript: " + path + "\n")
	if !strings.Contains(diagnostic, "sandbox: worker startup denied") {
		t.Fatalf("worker stderr was discarded:\n%s", diagnostic)
	}
}

// goTestIn runs the fixture's own suite, proving the shipped code WORKS rather
// than merely existing. Nothing else in the arc checks that: the loop is
// perfectly happy to land something broken.
func goTestIn(dir string) (string, error) {
	c := exec.Command("go", "test", "./...")
	c.Dir = dir
	b, err := c.CombinedOutput()
	return string(b), err
}
