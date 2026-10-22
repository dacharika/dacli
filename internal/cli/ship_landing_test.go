package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestShipDoesNotStampAFalseUnlandedRecord is the dacli 329 regression.
//
// `dacli ship` runs `accept --all --force` BEFORE `integrate` — forced,
// because `integrate` refuses a task that is not yet done. That ordering
// means a landing check made DURING accept can only ever see "the branch is
// not yet in trunk", even for a task ship's own next step is seconds away
// from merging. Before the fix, every successful ship durably recorded
// "... exists but is NOT in trunk — closed anyway" on every task it closed
// and then landed, permanently misstating where the work ended up.
//
// This drives the real accept-then-integrate order through the actual
// binary (not a stub, unlike ship's own unit tests, which mock the shelled
// subcommands and so never exercise the landing check at all) and asserts
// the FINAL task record — after the whole ship pipeline has run — states the
// truth: merged, never "closed anyway".
func TestShipDoesNotStampAFalseUnlandedRecord(t *testing.T) {
	bin := buildDacli(t)
	dir := t.TempDir()
	gitInit(t, dir)

	run(t, dir, 0, "init", "--name", "x")
	run(t, dir, 0, "project", "add", "P", "--slug", "p", "--goal", "a real goal for the project")
	run(t, dir, 0, "task", "add", "implement the thing", "--project", "p", "--accept", "the thing works")

	// A worker that does real work on its own branch, then reports through
	// `task done` — the same arc TestWorkerProposedDoneIsLandedByTheToolNotByTheTest
	// drives, reused here because it is exactly the shape ship's accept step
	// force-closes: a task proposed by a now-finished agent.
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
	dacliRun(t, bin, dir, "sync")
	dacliRun(t, bin, dir, "ship", "--project", "p", "--into", "main")

	// The task must actually be closed AND landed — the precondition for the
	// assertion below to mean anything.
	if got := dacliRun(t, bin, dir, "task", "list", "--project", "p"); !strings.Contains(got, "done") {
		t.Fatalf("setup: ship did not close the task:\n%s", got)
	}
	if out := gitOut(t, dir, "log", "--oneline", "main"); !strings.Contains(out, "implement the thing") {
		t.Fatalf("setup: ship did not land the branch on trunk:\n%s", out)
	}

	record := dacliRun(t, bin, dir, "task", "show", "001")
	// The evidence names the branch it checked against — "main" here, or
	// "sprint/N" during a sprint — rather than the word "trunk" (dacli 342).
	if strings.Contains(record, "is NOT in main") {
		t.Errorf("ship recorded a false unlanded verdict on a task it went on to land:\n%s", record)
	}
	if !strings.Contains(record, "is merged into main") {
		t.Errorf("ship never recorded the true landing verdict once integrate actually ran:\n%s", record)
	}
}

// TestShipRecordsUnlandedTruthfullyOnConflict is acceptance criterion 2 for
// dacli 329: deferring the landing check into ship must not turn into
// silence when integrate genuinely fails to land the work. A real merge
// conflict blocks the task before it reaches trunk, and the task's record
// must still say so plainly — the failure issue #382 exists to catch.
func TestShipRecordsUnlandedTruthfullyOnConflict(t *testing.T) {
	bin := buildDacli(t)
	dir := t.TempDir()
	gitInit(t, dir)

	run(t, dir, 0, "init", "--name", "x")
	run(t, dir, 0, "project", "add", "P", "--slug", "p", "--goal", "a real goal for the project")
	run(t, dir, 0, "task", "add", "edit shared", "--project", "p", "--accept", "a")

	writeNativeWorkerFixture(t, dir, "worker", strings.Join([]string{
		"cat > /dev/null",
		"ref=$(git rev-parse --abbrev-ref HEAD | sed 's|dacli/||; s|-.*||')",
		"echo branch line > shared.txt",
		"git add -A -- shared.txt",
		"git -c user.email=a@b -c user.name=w commit -q -m \"$ref: branch edit\"",
		bin + " task claim \"$ref\"",
		bin + " task done \"$ref\"",
	}, "\n"))

	executeNativeWorkerFixture(t, dir, "001", "worker", "shared.txt")

	// main edits the SAME file differently, so integrate hits a real conflict
	// instead of a clean merge.
	if err := os.WriteFile(filepath.Join(dir, "shared.txt"), []byte("main line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitAt(t, dir, "add", "-A")
	gitAt(t, dir, "commit", "-q", "-m", "main edits shared")

	dacliRun(t, bin, dir, "sync")

	c := exec.Command(bin, "ship", "--project", "p", "--into", "main")
	c.Dir = dir
	if out, err := c.CombinedOutput(); err == nil {
		t.Fatalf("expected ship to refuse on the merge conflict:\n%s", out)
	}

	record := dacliRun(t, bin, dir, "task", "show", "001")
	if !strings.Contains(record, "is NOT in main") {
		t.Errorf("a genuinely unlanded close must still record that plainly:\n%s", record)
	}
	if strings.Contains(record, "is merged into main") {
		t.Errorf("a task blocked on conflict must not be recorded as merged:\n%s", record)
	}
}
