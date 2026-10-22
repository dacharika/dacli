package cli

import (
	"strings"
	"testing"
)

// Canonical context reports the current project phase and appropriate role kinds.
func TestPhaseContext(t *testing.T) {
	dir := t.TempDir()
	run(t, dir, 0, "init", "--name", "x")

	// A phase-gated project starts in discovery.
	run(t, dir, 0, "project", "add", "Widget", "--slug", "w",
		"--goal", "One clear thing, verified by the acceptance suite.", "--template", "product")
	run(t, dir, 0, "task", "add", "Prototype the widget", "--project", "w", "--accept", "a")

	// Roles with kinds, and a runtime to spawn onto.
	run(t, dir, 0, "role", "add", "researcher", "--kind", "researcher", "--grant", "ro")
	run(t, dir, 0, "role", "add", "builder", "--kind", "implementer", "--grant", "rw")

	// stage shows the phase and its allowed roles.
	st := run(t, dir, 0, "stage", "w")
	if !strings.Contains(st, "phase discovery") || !strings.Contains(st, "researcher") {
		t.Errorf("stage does not show the phase:\n%s", st)
	}

	// The brief tells the agent the phase.
	brief := run(t, dir, 0, "context", "prototype-the-widget")
	if !strings.Contains(brief, "Phase: **discovery**") || !strings.Contains(brief, "work appropriate now: researcher") {
		t.Errorf("brief missing the phase:\n%s", brief)
	}

	// Reading stage and context does not advance the project phase.
	if strings.Contains(run(t, dir, 0, "stage", "w"), "phase implementation") {
		t.Error("project should still be pre-implementation")
	}
}

// Untemplated projects have no phase restriction in the canonical context.
func TestSoloProjectContextHasNoPhase(t *testing.T) {
	dir := t.TempDir()
	run(t, dir, 0, "init", "--name", "x")
	run(t, dir, 0, "project", "add", "Free", "--slug", "f", "--goal", "g")
	run(t, dir, 0, "task", "add", "Do the thing", "--project", "f", "--accept", "a")
	out := run(t, dir, 0, "context", "do-the-thing")
	if strings.Contains(out, "Phase: **") {
		t.Fatalf("solo context unexpectedly phase-gated: %s", out)
	}
}
