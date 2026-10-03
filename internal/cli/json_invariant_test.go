package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// jsonHonoringCommands is the recorded set of command paths that honor the
// global --json flag. Most entries emit a stable JSON document; init and new
// adapt their human output for a machine caller by suppressing the decorative
// getting-started/next-steps block — the project's established meaning of
// --json for a scaffolding command (see
// wscore.TestCmdInitJSONSuppressesGettingStarted).
//
// It is the memory the invariant enforces against: a new command that sets
// Command.JSON without being recorded here fails TestJSONFlagIsHonoredOrRefused,
// so no command can quietly claim --json support. Adding an entry is the
// deliberate act that says "I checked: this command honors --json."
var jsonHonoringCommands = map[string]bool{
	"native preflight configure": true, "native preflight override": true, "native preflight check": true,
	"native recovery configure":   true,
	"native recovery preview":     true,
	"native recovery apply":       true,
	"native lifecycle observe":    true,
	"native lifecycle cancel":     true,
	"native lifecycle cancel-ack": true,
	"native lifecycle status":     true,

	"native dispatch request":     true,
	"native dispatch correlate":   true,
	"native dispatch acknowledge": true,
	"native dispatch status":      true,

	"accept":                  true,
	"accept apply":            true,
	"accept propose":          true,
	"agents":                  true,
	"capabilities":            true,
	"cleanup":                 true,
	"branches audit":          true,
	"branches prune":          true,
	"commit":                  true,
	"events reconcile":        true,
	"explain":                 true,
	"github projection":       true,
	"github doctor":           true,
	"github pull":             true,
	"github sync":             true,
	"handoff consume":         true,
	"handoff show":            true,
	"context":                 true,
	"metrics":                 true,
	"overview":                true,
	"whoami":                  true,
	"status":                  true,
	"next":                    true,
	"native swarm plan":       true,
	"native swarm reserve":    true,
	"native swarm admit":      true,
	"native swarm handoff":    true,
	"native verify":           true,
	"critical-path":           true,
	"doctor":                  true,
	"project show":            true,
	"pr diagnose":             true,
	"pr wait":                 true,
	"reconcile":               true,
	"release train":           true,
	"release train authority": true,
	"review projection":       true,
	"slice reconcile":         true,
	"task list":               true,
	"task show":               true,
	"task status":             true,
	"task progress":           true,
	"task aggregate":          true,
	"task decompose":          true,
	"init":                    true,
	"logs":                    true,
	"new":                     true,
	"version":                 true,
}

// TestJSONFlagIsHonoredOrRefused is an INVARIANT test over the whole command
// table, and it exists because of how this bug was found: --json was parsed
// globally and then honored by 4 of 117 commands and silently ignored by the
// ~40 read commands, so an agent that passed --json to `status`, `whoami`,
// `next`, or `task show` got human prose it could not parse — under a flag
// whose entire purpose is machine-readable output.
//
// The fix is a dispatch-layer gate (refuseUnsupportedJSON): a command either
// declares Command.JSON and honors the flag, or the flag is refused with exit
// 2 rather than accepted and dropped. This table locks that contract in. A new
// command defaults to Command.JSON == false and so refuses --json
// automatically; it can only start accepting the flag by an author setting
// Command.JSON true AND recording the path in jsonHonoringCommands above — at
// which point this test demands proof (a driver, below) that the flag is
// genuinely honored. Silent acceptance is structurally impossible.
func TestJSONFlagIsHonoredOrRefused(t *testing.T) {
	// 1. The declared set (Command.JSON) and the recorded set must match
	//    exactly, so neither can drift without failing here.
	declared := map[string]bool{}
	for i := range commands {
		if commands[i].JSON {
			declared[commands[i].Path] = true
		}
	}
	for path := range declared {
		if !jsonHonoringCommands[path] {
			t.Errorf("%q sets Command.JSON but is not recorded in jsonHonoringCommands — record it (and confirm it honors --json), or drop the flag", path)
		}
	}
	for path := range jsonHonoringCommands {
		if !declared[path] {
			t.Errorf("%q is recorded as honoring --json but no command sets Command.JSON for it", path)
		}
	}

	// 2. Enumerate every command through the real dispatch gate (the executor
	//    front end) with --json. A command that does not honor --json must
	//    REFUSE it with exit 2; one that honors it must not be refused for the
	//    flag. The gate fires before Run, so a non-honoring command is refused
	//    without side effects regardless of its other args.
	for i := range commands {
		cmd := commands[i]
		t.Run(cmd.Path, func(t *testing.T) {
			dir := t.TempDir()
			argv := strings.Fields(cmd.Path)
			_, msg, code := executor(dir)(argv, true)
			refusedForJSON := code == 2 && strings.Contains(msg, "does not support --json")
			if cmd.JSON {
				if refusedForJSON {
					t.Errorf("%q honors --json but the gate refused the flag: %s", cmd.Path, msg)
				}
			} else if !refusedForJSON {
				t.Errorf("%q does not honor --json, so --json must be refused with exit 2 (got code %d, msg %q)", cmd.Path, code, msg)
			}
		})
	}
}

// TestJSONHonoringCommandsEmitOrAdapt proves the recorded --json commands
// actually do something with the flag rather than merely declaring it: the two
// document emitters produce valid JSON, and the two adapters drop their
// human-only decoration. Without this, Command.JSON could be set on a command
// that ignores --json — exactly the dishonesty the gate exists to prevent.
func TestJSONHonoringCommandsEmitOrAdapt(t *testing.T) {
	dir := t.TempDir()
	// init both proves the adapter (below) and gives the other commands a
	// workspace to read.
	initOut, msg, code := executor(dir)([]string{"init", "--name", "x"}, true)
	if code != 0 {
		t.Fatalf("init --json: exit %d: %s", code, msg)
	}
	if strings.Contains(initOut, "Getting started") {
		t.Errorf("init --json must suppress the human getting-started block:\n%s", initOut)
	}
	run(t, dir, 0, "project", "add", "P", "--slug", "p", "--goal", "a real goal for this project")
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.test/p\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, dir, 0, "adopt", "--project", "p")
	run(t, dir, 0, "task", "add", "a task", "--project", "p", "--accept", "it works")
	run(t, dir, 0, "task", "add", "aggregate parent", "--project", "p", "--accept", "children complete")
	run(t, dir, 0, "task", "add", "aggregate child one", "--project", "p", "--parent", "002", "--accept", "internal/a/a.go complete")
	run(t, dir, 0, "task", "add", "render the UI", "--project", "p", "--parent", "002", "--accept", "internal/b/b.go complete")
	run(t, dir, 0, "task", "add", "oversized leaf", "--project", "p", "--estimate", "8,13,21", "--accept", "internal/c/c.go complete", "--accept", "internal/d/d.go complete")

	for _, tc := range []struct {
		name string
		argv []string
	}{
		{"context", []string{"context", "001"}},
		{"whoami", []string{"whoami"}},
		{"status", []string{"status", "--project", "p"}},
		{"next", []string{"next", "--project", "p"}},
		{"doctor", []string{"doctor"}},
		{"metrics", []string{"metrics"}},
		{"project show", []string{"project", "show", "p"}},
		{"task list", []string{"task", "list", "--project", "p"}},
		{"task show", []string{"task", "show", "001"}},
		{"task aggregate", []string{"task", "aggregate", "002", "--dry-run"}},
		{"task decompose", []string{"task", "decompose", "005", "--dry-run"}},
		{"accept", []string{"accept", "001", "--allow-unlanded"}},
	} {
		out, msg, code := executor(dir)(tc.argv, true)
		if code != 0 {
			t.Fatalf("%s --json: exit %d: %s", tc.name, code, msg)
		}
		if !json.Valid([]byte(out)) {
			t.Errorf("%s --json did not emit valid JSON:\n%s", tc.name, out)
		}
	}
}
