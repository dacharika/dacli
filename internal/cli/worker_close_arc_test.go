package cli

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestWorkerProposedDoneIsLandedByTheToolNotByTheTest is the integration
// regression the 2026-08-10 suite audit asked for, and the arc that had ZERO
// coverage while every guard along it was unit-correct and green.
//
// The deadlock it guards (task 312): a spawned worker CLAIMS (an event the
// owner applies on sync, so the worker is still not the owner), therefore
// cannot check its own acceptance boxes (owner-gated by design), therefore
// files `task done` as a propose-status event that sync correctly refuses for
// unmet acceptance. `accept --all` could not see that channel at all, so the
// work sat open: agents committed real code and the loop halted on "no net
// progress" with the work finished and unlandable.
//
// The load-bearing detail is what this test REFUSES to do: it never calls
// `accept --force` itself. The existing end-to-end arc (TestE2EOwnershipGrantArc)
// routes around the defect precisely there — its child uses `accept` rather
// than `task done`, and the test itself performs the reconciliation. A test
// that reconciles by hand proves the command works when a human runs it, which
// was never in doubt; it cannot prove the TOOL closes the loop.
func TestWorkerProposedDoneIsLandedByTheToolNotByTheTest(t *testing.T) {
	bin := buildDacli(t)
	dir := t.TempDir()
	gitInit(t, dir)

	run(t, dir, 0, "init", "--name", "x")
	run(t, dir, 0, "project", "add", "P", "--slug", "p", "--goal", "a real goal for the project")
	run(t, dir, 0, "task", "add", "implement the thing", "--project", "p", "--accept", "the thing works")

	// A worker that behaves exactly as the protocol preamble instructs: do the
	// work, commit it, then report through `task done` — the verb agents are
	// told to use, and the one whose proposals nothing consumed.
	writeNativeWorkerFixture(t, dir, "worker", strings.Join([]string{
		"cat > /dev/null",
		"ref=$(git rev-parse --abbrev-ref HEAD | sed 's|dacli/||; s|-.*||')",
		"echo work > worked.txt",
		"git add -A -- worked.txt",
		"git -c user.email=a@b -c user.name=w commit -q -m \"implement the thing\"",
		bin + " task claim \"$ref\"",
		bin + " task done \"$ref\"",
	}, "\n"))

	executeNativeWorkerFixture(t, dir, "001", "worker", "worked.txt")

	// The worker's own commands could not close the task — that is the design,
	// not the bug. Confirm the precondition so a future change that lets the
	// worker self-close does not make this test vacuous.
	// Status is FOLDER POSITION, not a frontmatter field, so `task show` never
	// prints it — `task list` is the signal that exists.
	if got := dacliRun(t, bin, dir, "task", "list", "--project", "p"); strings.Contains(got, "done") {
		t.Fatal("precondition lost: the worker closed its own task, so this test no longer covers the reconcile arc")
	}

	// Now the TOOL's own landing path, with no --force from the test.
	//
	// These run as SUBPROCESSES of the real binary, not through the in-process
	// dispatcher: `ship` re-invokes dacli via os.Executable(), which under
	// `go test` is the TEST binary — so an in-process ship re-enters the suite
	// instead of running its accept step. That is exactly why the existing
	// end-to-end arc reconciles by hand, and why driving the real binary is
	// the only way to prove the tool closes its own loop.
	dacliRun(t, bin, dir, "sync")
	dacliRun(t, bin, dir, "ship", "--project", "p", "--into", "main")

	if got := dacliRun(t, bin, dir, "task", "list", "--project", "p"); !strings.Contains(got, "done") {
		t.Errorf("a worker's completed, proposed-done task was not landed by the tool:\n%s", got)
	}
	// And the work itself reached trunk, not merely the record: a task marked
	// done over code that never landed is the false-completion failure the
	// landing check exists to catch.
	if out := gitOut(t, dir, "log", "--oneline", "main"); !strings.Contains(out, "implement the thing") {
		t.Errorf("the worker's commit never reached trunk:\n%s", out)
	}
}

// dacliRun executes the REAL binary in dir and fails the test on a non-zero
// exit, returning its combined output.
func dacliRun(t *testing.T, bin, dir string, args ...string) string {
	t.Helper()
	c := exec.Command(bin, args...)
	c.Dir = dir
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("dacli %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

var _ = filepath.Join
