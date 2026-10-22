package cli

import (
	"os/exec"
	"strings"
	"testing"
)

// forwardsUnknownFlags names the commands that legitimately accept flags this
// table cannot enumerate, because forwarding them IS the feature.
var forwardsUnknownFlags = map[string]bool{
	// `run` passes unknown flags to the shortcut as parameters.
	"run": true,
	// pull/sync forward their flags to the push/pull halves they drive.
	"github pull": true,
	"github sync": true,
}

// TestEveryCommandRejectsAnUnknownFlag drives the WHOLE surface with a flag
// that exists nowhere and asserts it is refused.
//
// This is the drift guard the flag kernel never had. `Flags.Reject` is opt-in
// per handler, and opt-in guards in this codebase drift without exception: it
// reached 4 handlers out of 112 once, was fixed, and by the 2026-08-06 audit
// had drifted back to ~25 handlers with none — including mutating ones. The
// live demo was `dacli task block 001 --whyy "the real reason"`, which exited
// 0, blocked the task, and recorded an EMPTY reason. The caller's intent was
// lost with a success exit.
//
// A per-handler test can only assert what someone remembered to write. This
// asserts it for every command that exists, so a new handler that forgets
// Reject fails here rather than in someone's workspace.
func TestEveryCommandRejectsAnUnknownFlag(t *testing.T) {
	// Every invocation must reject before reading or mutating command state, so
	// one initialized fixture is both the stronger contract and dramatically
	// cheaper than creating a repository and five git processes for every
	// command. A handler that accidentally runs still fails below on nil error.
	dir := t.TempDir()
	gitInit(t, dir)
	run(t, dir, 0, "init", "--name", "x")

	for i := range commands {
		cmd := &commands[i]
		if forwardsUnknownFlags[cmd.Path] {
			continue
		}
		t.Run(cmd.Path, func(t *testing.T) {
			var out, errb strings.Builder
			ctx := &Ctx{Stdout: &out, Stderr: &errb, Cwd: dir}
			err := invoke(ctx, cmd, []string{"--zz-no-such-flag", "value"})
			if err == nil {
				t.Fatalf("%s accepted --zz-no-such-flag and ran anyway; a typo'd flag must never be silently dropped", cmd.Path)
			}
			// The error must NAME the flag. Accepting any error would let a
			// command pass by failing on a missing positional first, which is
			// exactly the false confidence this test exists to remove: the
			// flag would still be dropped once the positional was supplied.
			if !strings.Contains(err.Error(), "zz-no-such-flag") {
				t.Fatalf("%s failed for some other reason (%v) — it never rejected the unknown flag itself", cmd.Path, err)
			}
			if code := exitCode(err); code != 2 {
				t.Errorf("%s: exit %d for an unknown flag, want 2 (usage)", cmd.Path, code)
			}
		})
	}
}

// Asking what a command does must never be the thing that does it. Before the
// dispatcher handled it, `--help` was silently dropped by any handler without
// Reject and the command RAN: `dacli task claim 001 --help` claimed the task.
func TestHelpNeverExecutesTheCommand(t *testing.T) {
	dir := t.TempDir()
	run(t, dir, 0, "init", "--name", "x")
	run(t, dir, 0, "project", "add", "P", "--slug", "p", "--goal", "a real goal for the project")
	run(t, dir, 0, "task", "add", "a task", "--project", "p", "--accept", "it works")

	out := run(t, dir, 0, "task", "claim", "001", "--help")
	if !strings.Contains(out, "task claim") {
		t.Errorf("--help must describe the command, got:\n%s", out)
	}
	// The task must be untouched: help is a read.
	if got := run(t, dir, 0, "task", "show", "001"); strings.Contains(got, "claimed") {
		t.Errorf("--help claimed the task instead of describing it:\n%s", got)
	}

	// -h is the same promise.
	if out := run(t, dir, 0, "task", "claim", "001", "-h"); !strings.Contains(out, "task claim") {
		t.Errorf("-h must describe the command, got:\n%s", out)
	}
}

// gitInit makes dir a git repository with one commit, so commands that shell
// to git get far enough for their flag handling to be what fails.
func gitInit(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "x@x"},
		{"config", "user.name", "x"},
		// Force the trunk name. git's default branch is configurable and
		// differs between machines — CI runners still default to `master` —
		// so a fixture that assumes `main` fails there for a reason that has
		// nothing to do with what it is testing. (dacli's own ship refusal
		// caught exactly this: "there is no main branch to integrate into".)
		{"checkout", "-q", "-b", "main"},
		{"commit", "-q", "--allow-empty", "-m", "base"},
	} {
		c := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
}

// `--help` printed the Brief and nothing else, so a command's flags were
// undocumented — `dacli loop --help` did not mention --no-progress-halt at
// all, and since it requires an integer, "it reads like a boolean" was the
// only conclusion available from help output (issue #421).
//
// Asserted over the VALUE SHAPE, not the prose: a flag that takes a value must
// show it, because that is the specific thing whose absence caused the bug.
func TestHelpShowsTheFlagSynopsisWithValueShapes(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	run(t, dir, 0, "init", "--name", "h")

	out := run(t, dir, 0, "task", "check", "--help")
	for _, want := range []string{
		"--n N",
		"--verify command",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("task check --help must document %q:\n%s", want, out)
		}
	}
}

// Every command that declares a Usage must actually print it, or the field is
// decorative and the next command to add one gets no help either.
func TestDeclaredUsageIsAlwaysPrinted(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	run(t, dir, 0, "init", "--name", "h")

	checked := 0
	for i := range commands {
		c := &commands[i]
		if c.Usage == "" {
			continue
		}
		checked++
		out := run(t, dir, 0, append(strings.Fields(c.Path), "--help")...)
		if !strings.Contains(out, c.Usage) {
			t.Errorf("%s declares a Usage that --help does not print:\n%s", c.Path, out)
		}
	}
	if checked == 0 {
		t.Fatal("no command declares a Usage — this test measured nothing")
	}
}

// A command that TAKES flags must document them, or `--help` sends the caller
// off to brute-force the shape.
//
// An agent reported burning four failed invocations on `dacli note add` — the
// whole of `--help` was the command name and one line of prose. Its framing is
// the reason this test exists: a human hits --help, sees nothing and shrugs
// into the README; an agent has no README reflex, so it guesses, and each guess
// is a full turn plus tokens (issue #436).
//
// This is the drift guard for the Usage field added in dacli 339. That change
// built the mechanism and filled it in for exactly ONE command, which is how
// the gap survived to be reported.
//
// Enumerated, not sampled: a curated list would drift the same way the flags
// themselves did. Commands genuinely taking no flags are exempt — they are
// discovered by asking each handler whether it rejects any.
func TestFlagTakingCommandsDocumentTheirFlags(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	run(t, dir, 0, "init", "--name", "h")

	var undocumented []string
	checked := 0
	for i := range commands {
		c := &commands[i]
		// Probe: a command that rejects unknown flags TAKES flags (or takes
		// none and rejects everything). Distinguish by offering a flag that no
		// command could legitimately accept and seeing whether the refusal
		// names an allowlist.
		out := run(t, dir, 2, append(strings.Fields(c.Path), "--zz-no-such-flag")...)
		if !strings.Contains(out, "zz-no-such-flag") {
			continue // does not reach flag rejection (missing positional, etc.)
		}
		checked++
		if c.Usage == "" {
			undocumented = append(undocumented, c.Path)
		}
	}
	if checked == 0 {
		t.Fatal("no command reached flag rejection — this test measured nothing")
	}
	if len(undocumented) > 0 {
		t.Errorf("%d of %d flag-taking commands have no Usage, so --help documents none of their flags:\n  %s",
			len(undocumented), checked, strings.Join(undocumented, "\n  "))
	}
}

// A usage error must name what is RIGHT, not only what is wrong.
//
// "unknown flag(s): --kind, --title" tells a caller their invocation is
// malformed and nothing else. The agent who reported this observed that
// "--project is required" was the most useful of its four failed attempts
// precisely BECAUSE it named the correct thing (issue #436). Every command now
// carries a synopsis, so the answer is on hand at the moment of refusal
// (dacli 348).
func TestAUsageErrorPrintsTheCommandsSynopsis(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	run(t, dir, 0, "init", "--name", "h")
	run(t, dir, 0, "project", "add", "P", "--slug", "p", "--goal", "g")

	// The reporter's own first attempt.
	out := run(t, dir, 2, "note", "add", "--kind", "finding", "--title", "x")
	if !strings.Contains(out, "kind") {
		t.Errorf("the refusal must still name the offending flag:\n%s", out)
	}
	if !strings.Contains(out, "usage: dacli note add") {
		t.Errorf("the refusal must also name the correct signature:\n%s", out)
	}
	// The synopsis has to be the REAL one, not a placeholder.
	if !strings.Contains(out, "--project") {
		t.Errorf("the synopsis must carry the command's actual flags:\n%s", out)
	}
}

// Only exit 2. A policy refusal has already said what to do instead, and a
// not-found is not a malformed call — appending a synopsis to either is noise.
func TestOnlyUsageErrorsGainASynopsis(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	run(t, dir, 0, "init", "--name", "h")
	run(t, dir, 0, "project", "add", "P", "--slug", "p", "--goal", "g")

	// Exit 3: a decision without --rejected is refused by policy.
	refusal := run(t, dir, 3, "note", "add", "decision", "d", "--project", "p")
	if strings.Contains(refusal, "usage: dacli note add") {
		t.Errorf("a policy refusal must not be padded with a synopsis:\n%s", refusal)
	}

	// Exit 4: a ref that names nothing.
	notFound := run(t, dir, 4, "task", "show", "999")
	if strings.Contains(notFound, "usage: dacli task show") {
		t.Errorf("a not-found must not be padded with a synopsis:\n%s", notFound)
	}
}
