package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

// `dacli init` now prints a short getting-started section right after the
// workspace facts — the first-run onboarding a human sees once, with no
// effect on the machine-readable facts init already printed.
func TestInitPrintsGettingStarted(t *testing.T) {
	dir := t.TempDir()
	out := run(t, dir, 0, "init", "--name", "x")
	if !strings.Contains(out, "initialized workspace") {
		t.Fatalf("init lost its workspace-facts line:\n%s", out)
	}
	if !strings.Contains(out, "Getting started") {
		t.Errorf("init did not print a getting-started section:\n%s", out)
	}
	for _, want := range []string{
		"dacli whoami", "dacli project add", "dacli task add", "dacli next",
		"dacli native enroll --help", "dacli context <ref>", "dacli overview",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("getting-started missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "dacli spawn") || strings.Contains(out, "dacli runtime add") {
		t.Fatalf("onboarding recommends removed provider launchers: %s", out)
	}

}

// --json is the machine caller's contract: it must not gain decorative text
// that wasn't there before.
func TestInitJSONSkipsGettingStarted(t *testing.T) {
	dir := t.TempDir()
	out := runJSON(t, dir, "init", "--name", "x")
	if strings.Contains(out, "Getting started") {
		t.Errorf("init --json printed the human-only getting-started section:\n%s", out)
	}
}

// overview on a brand-new workspace (no projects yet) tells the human what
// to do next instead of printing an empty PROJECTS table.
func TestOverviewEmptyWorkspace(t *testing.T) {
	dir := t.TempDir()
	run(t, dir, 0, "init", "--name", "x")
	out := run(t, dir, 0, "overview")
	if !strings.Contains(out, "no projects yet") || !strings.Contains(out, "dacli project add") {
		t.Errorf("overview on an empty workspace should point at project add:\n%s", out)
	}
}

// overview reports who's acting, per-project status counts, pending events,
// and a taste of what's ready — the human-first counterpart to `status`.
func TestOverviewSummarizesWorkspace(t *testing.T) {
	dir := t.TempDir()
	run(t, dir, 0, "init", "--name", "x")
	run(t, dir, 0, "project", "add", "Core", "--slug", "core", "--goal", "g")
	run(t, dir, 0, "task", "add", "Ship the thing", "--project", "core", "--accept", "a", "--priority", "must")
	run(t, dir, 0, "task", "add", "Done already", "--project", "core", "--accept", "a")
	doneID := findTask(t, dir, "done-already")
	run(t, dir, 0, "task", "claim", doneID)
	run(t, dir, 0, "task", "check", doneID, "--all")
	run(t, dir, 0, "task", "done", doneID)

	out := run(t, dir, 0, "overview")
	for _, want := range []string{
		"you are ", "PROJECTS", "core", "ACTIVITY", "pending events", "live agents",
		"READY NOW", "ship-the-thing", "must", "USEFUL NEXT", "dacli next",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("overview missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "1 done") {
		t.Errorf("overview did not roll up the done task:\n%s", out)
	}
}

func TestOverviewEmitsBoundedJSON(t *testing.T) {
	dir := t.TempDir()
	run(t, dir, 0, "init", "--name", "x")
	out, msg, code := executor(dir)([]string{"overview"}, true)
	if code != 0 {
		t.Fatalf("overview --json: exit %d: %s", code, msg)
	}
	var view struct {
		Schema     string `json:"schema"`
		ReadyLimit int    `json:"ready_limit"`
	}
	if err := json.Unmarshal([]byte(out), &view); err != nil {
		t.Fatalf("overview --json invalid: %v\n%s", err, out)
	}
	if view.Schema != "workspace-overview/v1" || view.ReadyLimit != 3 {
		t.Fatalf("overview schema/limit = %q/%d", view.Schema, view.ReadyLimit)
	}
}
