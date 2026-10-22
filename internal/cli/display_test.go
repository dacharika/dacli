package cli

import (
	"strings"
	"testing"
)

func TestDisplayCommands(t *testing.T) {
	dir := t.TempDir()
	run(t, dir, 0, "init", "--name", "x")
	run(t, dir, 0, "project", "add", "P", "--slug", "p", "--goal", "g", "--stage", "elicitation")
	run(t, dir, 0, "task", "add", "Parent epic task", "--project", "p", "--priority", "must",
		"--estimate", "2,5,14", "--accept", "a")
	run(t, dir, 0, "task", "add", "Child work item", "--project", "p", "--priority", "must",
		"--estimate", "1,2,3", "--accept", "a", "--parent", "001", "--depends-on", "001")

	// estimate: PERT + the cone for the project's stage (6 at elicitation → 3–12).
	out := run(t, dir, 0, "estimate", "001")
	for _, want := range []string{"Te 6.0", "cone (elicitation): 3.0–12.0"} {
		if !strings.Contains(out, want) {
			t.Errorf("estimate missing %q:\n%s", want, out)
		}
	}
	run(t, dir, 4, "estimate", "no-such")

	// critical-path: both on the chain, star-marked, duration = 6+2.
	out = run(t, dir, 0, "critical-path", "p")
	// Two starred rows plus the legend's star.
	if !strings.Contains(out, "project duration: 8.0") || strings.Count(out, "★") != 3 {
		t.Errorf("critical path wrong:\n%s", out)
	}

	// wbs: the child nests under the parent.
	out = run(t, dir, 0, "wbs", "p")
	if !strings.Contains(out, "\n  002-child-work-item") {
		t.Errorf("wbs tree not nested:\n%s", out)
	}

	// burndown before/after completion; velocity from the same stamps.
	out = run(t, dir, 0, "burndown")
	if !strings.Contains(out, "remaining: 8.0") {
		t.Errorf("burndown remaining wrong:\n%s", out)
	}
	run(t, dir, 0, "task", "claim", "001")
	run(t, dir, 0, "task", "check", "001", "--all")
	run(t, dir, 0, "task", "done", "001")
	out = run(t, dir, 0, "burndown")
	if !strings.Contains(out, "remaining: 2.0") || !strings.Contains(out, "done: 6.0") {
		t.Errorf("burndown after completion wrong:\n%s", out)
	}
	out = run(t, dir, 0, "velocity")
	if !strings.Contains(out, "1 task(s)") || !strings.Contains(out, "proxy") {
		t.Errorf("velocity wrong or not labeled a proxy:\n%s", out)
	}

	// threads: open, then answered.
	run(t, dir, 0, "ask", "Is the schema frozen?", "--about", "002")
	out = run(t, dir, 0, "threads")
	if !strings.Contains(out, "[OPEN]") || !strings.Contains(out, "schema frozen") {
		t.Errorf("threads open state wrong:\n%s", out)
	}
	qid := strings.Fields(out)[0]
	run(t, dir, 0, "answer", qid, "Yes, until Q3.")
	out = run(t, dir, 0, "threads")
	if !strings.Contains(out, "answered by a-root") {
		t.Errorf("threads answered state wrong:\n%s", out)
	}

	// escalate (event path only — no gh in CI): records and reports.
	out = run(t, dir, 0, "escalate", "Nobody owns the infra directory", "--about", "002")
	if !strings.Contains(out, "a human does now") {
		t.Errorf("escalate wrong:\n%s", out)
	}

	// shortcut promote is now real (no more planned stubs left): with no args
	// it is a usage error (exit 2), not the old "not built yet" message.
	out = run(t, dir, 2, "shortcut", "promote")
	if !strings.Contains(out, "shortcut promote") {
		t.Errorf("shortcut promote usage message unclear:\n%s", out)
	}
	// github sync is now a real command: with no project it is a usage error
	// (exit 2), not a planned-stub exit-1 message.
	out = run(t, dir, 2, "github", "sync")
	if !strings.Contains(out, "github sync <project>") {
		t.Errorf("github sync should now require a project, not stub:\n%s", out)
	}
}
