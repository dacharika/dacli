package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The full identity loop: spawn a read-only child, act as it via the token,
// verify its writes become events, and that attenuation holds.
func TestSpawnedChildIdentity(t *testing.T) {
	dir := t.TempDir()
	run(t, dir, 0, "init", "--name", "x")
	run(t, dir, 0, "project", "add", "P", "--slug", "p", "--goal", "g")
	run(t, dir, 0, "task", "add", "Audit the batch path", "--project", "p", "--accept", "a")

	// Spawn: token on stdout alone, so $(...) captures it.
	out := run(t, dir, 0, "agent", "spawn", "--role", "auditor", "--grant", "ro")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	token := strings.TrimSpace(lines[0])
	if len(token) != 48 {
		t.Fatalf("token line = %q, want 48 hex chars", token)
	}

	// Act as the child.
	t.Setenv("DACLI_AGENT", token)

	who := run(t, dir, 0, "whoami")
	if !strings.Contains(who, "grant: ro") || !strings.Contains(who, "role: auditor") {
		t.Errorf("child identity not resolved: %s", who)
	}

	// A read-only child finishing a task produces a proposal, not a move.
	done := run(t, dir, 0, "task", "done", "001")
	if !strings.Contains(done, "proposed as event") {
		t.Errorf("ro done should be an event: %s", done)
	}

	// Regression (found by the live mock-child demo): an ro finding filed
	// against a seq ref like "001" must resolve to the task id at the write
	// site, or the brief's about-filter never matches it.
	run(t, dir, 0, "note", "add", "finding", "Batch bypasses the service layer",
		"--project", "p", "--about", "001", "--body", "settle.go:112 writes directly")
	briefOut := run(t, dir, 0, "context", "001")
	if !strings.Contains(briefOut, "settle.go:112") {
		t.Errorf("ro child finding (seq-ref about) missing from brief:\n%s", briefOut)
	}

	// A read-only parent cannot mint a read-write child: attenuation, exit 3.
	run(t, dir, 3, "agent", "spawn", "--grant", "rw")

	// Back to root: the tree shows lineage and the child's writes.
	_ = os.Unsetenv("DACLI_AGENT")
	tree := run(t, dir, 0, "agent", "tree")
	if !strings.Contains(tree, "a-root (rw") || !strings.Contains(tree, "auditor") {
		t.Errorf("tree missing lineage:\n%s", tree)
	}
	if !strings.Contains(tree, "2 events") {
		t.Errorf("child's events (proposal + finding) not attributed in tree:\n%s", tree)
	}

	// The owner's sync applies the child's finding but NOT its propose:done:
	// the task still has an unchecked acceptance box, so the close is verified
	// and refused exactly like the owner's direct `task done` (task 284). A
	// propose:done no longer slips an unmet task into done/ via a bare move —
	// the proposal is left pending for a human, so only the finding applies.
	syncOut := run(t, dir, 0, "sync")
	if !strings.Contains(syncOut, "1 applied, 1 left pending") {
		t.Errorf("unmet propose:done should stay pending, only the finding applies:\n%s", syncOut)
	}
	// And the task must not have landed in done/ with no check to support it.
	notDone := run(t, dir, 0, "task", "list", "--status", "done")
	if strings.Contains(notDone, "audit-the-batch-path") {
		t.Errorf("task with an unchecked acceptance box was closed by propose:done:\n%s", notDone)
	}
}

func TestBadTokenRejected(t *testing.T) {
	dir := t.TempDir()
	run(t, dir, 0, "init", "--name", "x")
	t.Setenv("DACLI_AGENT", "deadbeef")
	out := run(t, dir, 1, "whoami")
	if !strings.Contains(out, "not recognized") {
		t.Errorf("bad token error unclear: %s", out)
	}
}

// Issue #684 happened through the public commands: a child filed a duplicate,
// was retired, and root still could not remove the orphan. Keep this at the CLI
// boundary so retirement is resolved from the durable agent file written by
// `agent retire`, not from a planning-package fixture or in-memory identity.
func TestRootRemovesTaskOwnedByRetiredChild(t *testing.T) {
	dir := t.TempDir()
	run(t, dir, 0, "init", "--name", "retired-child-removal")
	run(t, dir, 0, "project", "add", "P", "--slug", "p", "--goal", "g")
	run(t, dir, 0, "task", "add", "Remove the duplicate", "--project", "p", "--accept", "x")

	spawned := run(t, dir, 0, "agent", "spawn", "--role", "worker", "--grant", "rw")
	lines := strings.Split(strings.TrimSpace(spawned), "\n")
	token := strings.TrimSpace(lines[0])
	var childID string
	for _, line := range lines[1:] {
		if fields := strings.Fields(line); len(fields) >= 2 && fields[0] == "spawned" {
			childID = fields[1]
			break
		}
	}
	if childID == "" {
		t.Fatalf("spawn output did not name the child:\n%s", spawned)
	}

	t.Setenv("DACLI_AGENT", token)
	run(t, dir, 0, "task", "add", "Remove the duplicate", "--project", "p", "--accept", "x", "--force")
	_ = os.Unsetenv("DACLI_AGENT")

	run(t, dir, 0, "agent", "retire", childID)
	removed := run(t, dir, 0, "task", "rm", "002")
	if !strings.Contains(removed, "removed 002-remove-the-duplicate") {
		t.Fatalf("root did not remove retired child's duplicate:\n%s", removed)
	}
	remaining := run(t, dir, 0, "task", "list", "--status", "open")
	if strings.Count(remaining, "remove-the-duplicate") != 1 {
		t.Fatalf("duplicate reconciliation left the wrong task set:\n%s", remaining)
	}
}

// The shortcut loop: define, dry-run, guarded execution, injection safety,
// and the run event feeding the catalog.
func TestShortcutRun(t *testing.T) {
	dir := t.TempDir()
	run(t, dir, 0, "init", "--name", "x")
	run(t, dir, 0, "project", "add", "P", "--slug", "p")
	run(t, dir, 0, "task", "add", "T one", "--project", "p", "--accept", "a")

	// No effect → refused at creation, not silently defaulted.
	run(t, dir, 1, "shortcut", "add", "bad", "--command", "true")

	run(t, dir, 0, "shortcut", "add", "greet",
		"--command", "echo hello {{name}}",
		"--effect", "read",
		"--param", "name=world",
		"--summary", "echo test",
		"--why", "exists for the test suite")

	// Dry-run shows the expansion without executing.
	dry := run(t, dir, 0, "run", "greet", "--dry-run")
	if strings.TrimSpace(dry) != "echo hello world" {
		t.Errorf("dry-run = %q", dry)
	}

	// Injection attempt arrives quoted: the shell echoes it as a literal.
	out := run(t, dir, 0, "run", "greet", "--name", "world; touch /tmp/pwned")
	if !strings.Contains(out, "hello world; touch /tmp/pwned") {
		t.Errorf("param not passed as a literal: %q", out)
	}

	// Destructive requires rw AND --confirm.
	run(t, dir, 0, "shortcut", "add", "nuke", "--command", "echo boom", "--effect", "destructive")
	run(t, dir, 3, "run", "nuke")
	if got := run(t, dir, 0, "run", "nuke", "--confirm"); !strings.Contains(got, "boom") {
		t.Errorf("confirmed destructive did not run: %q", got)
	}

	// Uses are derived from run events; the catalog ranks by them.
	list := run(t, dir, 0, "run", "--list")
	if !strings.HasPrefix(strings.TrimSpace(list), "- `dacli run greet`") {
		t.Errorf("catalog should lead with the used shortcut:\n%s", list)
	}

	// And the catalog reaches the brief.
	briefOut := run(t, dir, 0, "context", "001")
	if !strings.Contains(briefOut, "## Shortcuts") || !strings.Contains(briefOut, "dacli run greet") {
		t.Errorf("shortcut catalog missing from brief:\n%s", briefOut)
	}
}

// The ad-hoc promotion loop: dacli tracks `run --cmd` invocations, a single
// run is not enough to promote, a repeated one is, and the resulting
// shortcut runs like any other.
func TestShortcutPromote(t *testing.T) {
	dir := t.TempDir()
	run(t, dir, 0, "init", "--name", "x")

	// Ad-hoc commands need an rw grant — there is no declared effect to gate
	// on, so a read-only agent is refused rather than defaulted into "safe".
	out := run(t, dir, 0, "agent", "spawn", "--role", "auditor", "--grant", "ro")
	token := strings.TrimSpace(strings.Split(strings.TrimSpace(out), "\n")[0])
	t.Setenv("DACLI_AGENT", token)
	run(t, dir, 3, "run", "--cmd", "echo hi")
	_ = os.Unsetenv("DACLI_AGENT")

	// dry-run just prints the literal command, no execution or tracking.
	dry := run(t, dir, 0, "run", "--cmd", "echo hi", "--dry-run")
	if strings.TrimSpace(dry) != "echo hi" {
		t.Errorf("adhoc dry-run = %q", dry)
	}

	// A single run is not "repeated": promotion refuses it.
	first := run(t, dir, 0, "run", "--cmd", "echo hi")
	if !strings.Contains(first, "hi") {
		t.Errorf("adhoc run did not execute:\n%s", first)
	}
	id1 := firstEventID(t, dir)
	run(t, dir, 3, "shortcut", "promote", "greet-adhoc", "--from-event", id1, "--effect", "read")

	// A second, identical invocation makes it repeated.
	run(t, dir, 0, "run", "--cmd", "echo hi")
	id2 := firstEventID(t, dir)

	promoted := run(t, dir, 0, "shortcut", "promote", "greet-adhoc", "--from-event", id2, "--effect", "read")
	if !strings.Contains(promoted, "2 runs") || !strings.Contains(promoted, "greet-adhoc") {
		t.Errorf("promote confirmation wrong:\n%s", promoted)
	}

	// The promoted shortcut runs like any hand-authored one.
	ran := run(t, dir, 0, "run", "greet-adhoc")
	if !strings.Contains(ran, "hi") {
		t.Errorf("promoted shortcut did not run:\n%s", ran)
	}
}

// firstEventID scrapes the newest run event's ULID off the on-disk log —
// eventlog has no "list IDs" CLI surface, so the test reaches for the one
// thing that is stable: the filename, which is <ULID>-<agent>-<kind>.md.
func firstEventID(t *testing.T, dir string) string {
	t.Helper()
	var newest string
	err := filepath.WalkDir(filepath.Join(dir, ".dacli", "events"), func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, "-run.md") {
			//nolint:nilerr // fs.WalkDirFunc: nil skips this entry and keeps walking
			return nil
		}
		base := filepath.Base(path)
		if base > newest {
			newest = base
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking events dir: %v", err)
	}
	if newest == "" {
		t.Fatalf("no run event found under %s", dir)
	}
	return strings.SplitN(newest, "-", 2)[0]
}

// ask blocks the task; answer unblocks it and leaves a durable note that
// reaches future briefs. The question is transient; the answer is permanent.
func TestAskAnswerLoop(t *testing.T) {
	dir := t.TempDir()
	run(t, dir, 0, "init", "--name", "x")
	run(t, dir, 0, "project", "add", "P", "--slug", "p", "--goal", "g")
	run(t, dir, 0, "task", "add", "Build the shim", "--project", "p", "--accept", "a")
	run(t, dir, 0, "task", "claim", "001")

	out := run(t, dir, 0, "ask", "Does the batch job write balances directly?", "--about", "001")
	if !strings.Contains(out, "blocked until answered") {
		t.Errorf("ask did not block: %s", out)
	}
	qid := strings.Fields(strings.TrimPrefix(out, "asked "))[0]

	st := run(t, dir, 0, "task", "list", "--status", "blocked")
	if !strings.Contains(st, "build-the-shim") {
		t.Errorf("task not in blocked:\n%s", st)
	}

	run(t, dir, 0, "answer", qid, "Yes — it bypasses the service layer entirely.", "--as", "finding")

	// Unblocked, and the answer is in the next brief.
	st = run(t, dir, 0, "task", "list", "--status", "active")
	if !strings.Contains(st, "build-the-shim") {
		t.Errorf("task not unblocked after answer:\n%s", st)
	}
	briefOut := run(t, dir, 0, "context", "001")
	if !strings.Contains(briefOut, "bypasses the service layer") {
		t.Errorf("answer not in the brief:\n%s", briefOut)
	}
}
