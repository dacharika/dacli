package cli

import (
	"strings"
	"testing"
)

// TestMutatingCommandsRefuseReadOnlyAgents is an INVARIANT test, not a feature
// test, and it exists because of how this codebase's capability bugs have
// actually been found.
//
// Every one of them was the same shape: a rule applied to some call sites and
// silently missed at others. `RequireRW` guarded `shortcut add` but not
// `skill add` — which is strictly more dangerous, since a skill body is
// compiled into every future agent's context. `SafeSegment` guarded project
// slugs but not role, queue, or skill names, so `skill add ../../../pwned`
// wrote outside the workspace. `Flags.Reject` reached 4 handlers out of 112.
// Each was fixed individually, and each time the NEXT audit found another
// instance, because per-command tests can only assert what someone remembered
// to write a test for.
//
// This table is the memory instead. Adding a mutating command without a
// capability check fails here, whether or not its author thought about it.
// When you add a command that executes code, writes to the workspace, or
// writes to a remote, add it here — a deliberate omission needs a comment
// saying why it is safe, not silence.
func TestMutatingCommandsRefuseReadOnlyAgents(t *testing.T) {
	dir := t.TempDir()
	run(t, dir, 0, "init", "--name", "x")
	run(t, dir, 0, "project", "add", "P", "--slug", "p", "--goal", "a real goal for the project")
	run(t, dir, 0, "task", "add", "a task to act on", "--project", "p", "--accept", "it works")

	// Mint a read-only child and act as it for the rest of the test.
	out := run(t, dir, 0, "agent", "spawn", "--role", "junior", "--grant", "ro")
	token := strings.TrimSpace(strings.Split(strings.TrimSpace(out), "\n")[0])
	if len(token) != 48 {
		t.Fatalf("spawn token = %q, want 48 hex chars", token)
	}
	t.Setenv("DACLI_AGENT", token)

	// Each entry must refuse a read-only caller with exit 3 (refused by
	// policy — a caller must never retry it).
	for _, tc := range []struct {
		name string
		why  string
		args []string
	}{
		{
			"shortcut add", "defines executable code a later `run` executes as the operator",
			[]string{"shortcut", "add", "s", "--command", "echo hi", "--effect", "read"},
		},
		{
			"skill add", "a skill body is compiled into every future agent's context",
			[]string{"skill", "add", "s", "--desc", "a trigger description"},
		},
		{
			"skill fetch", "clones a third-party repo into the skill library",
			[]string{"skill", "fetch", "owner/repo"},
		},
		{
			"project add", "creates workspace state and a filesystem path",
			[]string{"project", "add", "Another", "--slug", "q"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := run(t, dir, 3, tc.args...)
			if !strings.Contains(strings.ToLower(got), "grant") {
				t.Errorf("%s must refuse a ro caller because it %s; refusal should mention the grant, got:\n%s",
					tc.name, tc.why, got)
			}
		})
	}
}

// TestUserSuppliedNamesCannotEscapeTheWorkspace is the path-traversal half of
// the same invariant. Every name below becomes a filesystem path; a value
// carrying `..` or a separator must be rejected rather than resolved. The
// guard (workspace.SafeSegment) existed for a while but was applied only to
// project slugs, so `skill add ../../../pwned` wrote outside `.dacli`
// entirely. New name→path sinks belong in this table.
func TestUserSuppliedNamesCannotEscapeTheWorkspace(t *testing.T) {
	dir := t.TempDir()
	run(t, dir, 0, "init", "--name", "x")

	for _, tc := range []struct {
		name string
		args []string
	}{
		{"project slug", []string{"project", "add", "P", "--slug", "../../escaped"}},
		{"skill name", []string{"skill", "add", "../../escaped", "--desc", "a trigger description"}},
		{"queue slug", []string{"queue", "add", "../../escaped", "--step", "do a thing"}},
		{"role name", []string{"role", "add", "../../escaped"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, errb strings.Builder
			ctx := &Ctx{Stdout: &out, Stderr: &errb, Cwd: dir}
			cmd, rest := match(tc.args)
			if cmd == nil {
				t.Fatalf("no such command: %v", tc.args)
			}
			// The contract is only that it must FAIL — usage (2) or a plain
			// error (1) are both acceptable answers to a traversing name.
			// What must never happen is success.
			if err := cmd.Run(ctx, rest); err == nil {
				t.Fatalf("%s: a traversing name was accepted; it must be rejected", tc.name)
			}
		})
	}
}

// TestEveryCommandDeclaresItsCapability is the drift guard for Command.Mutates.
//
// The 2026-08-06 audit found four grant bypasses, and every one was a mutating
// command whose author did not call RequireRW while its sibling did:
// `shortcut promote` beside `shortcut add`, six `github` verbs beside `github
// release`, `agents --reap` beside `kill`, `worktree remove` beside `merge`.
// The gate now lives on the dispatcher and reads Command.Mutates, which turns
// "did the author remember a call?" into "did the author classify the command?"
//
// This table is what makes that classification mandatory. A new command fails
// here until it appears below, so the decision is forced at review time rather
// than discovered by the next audit. Moving a command between the lists is a
// deliberate, reviewable act.
//
// The rule: Mutates covers changing state a read-only agent must not change —
// the workspace's shape, the repository, the machine, or a remote. It does NOT
// cover the propose-and-report path (`note add`, `ask`/`answer`, the `task`
// verbs), because reporting is precisely what a read-only agent exists to do:
// a non-owner's `task done` and `accept` file proposals rather than closing
// anything, and gating those would break the design rather than protect it.
func TestEveryCommandDeclaresItsCapability(t *testing.T) {
	// Commands that change state a read-only agent must not change.
	wantMutating := map[string]bool{
		"accept apply": true, "adopt": true, "agent retire": true, "agent spawn": true, "cleanup": true, "branches prune": true, "events reconcile": true,
		"catalog": true, "commit": true, "escalate": true, "github codeowners": true,
		"github link": true, "github project": true, "github pull": true,
		"github push": true, "github release": true, "github sync": true,
		"handoff consume": true, "init": true, "integrate": true, "merge": true,
		"new": true, "pr": true, "pr land": true, "project add": true, "project rm": true, "project show": true,
		"push": true, "queue add": true, "queue advance": true, "reconcile": true, "report": true,
		"role add": true, "role bump": true, "run": true,
		"ship": true, "shortcut add": true,
		"shortcut promote": true, "skill add": true, "skill bump": true,
		"skill compile": true, "skill fetch": true, "skill import": true,
		"skill promote": true, "stage advance": true,
		"sync": true, "taint": true,
		"template add": true, "worktree add": true, "worktree prune": true,
		"worktree reclaim": true,
		// Removal inverses (task 293): deleting a role, runtime, shortcut or
		// queue changes what agents can be launched with and what `dacli run`
		// will execute, so they gate exactly like their `add` counterparts.
		"role rm": true, "shortcut rm": true, "queue rm": true,
		"worktree remove": true,
		// Task lifecycle inverses (task 340). Both change the RECORD, which is
		// this tool's product: reopen unchecks acceptance boxes that claimed
		// verified work, and rm deletes a task outright. A read-only agent
		// proposes; it does not get to retract a close or erase a task.
		"task reopen": true, "task rm": true,
		// Root takeover changes durable ownership after proving the prior owner
		// has no recovery lease; it is not a child proposal path.
		"task takeover": true,
		// Delivery slices are ordinary child tasks, but creation and fresh
		// GitHub reconciliation both rewrite their canonical task records.
		"slice add": true, "slice reconcile": true,
		// Historical acceptance migration writes a content-addressed plan and
		// changes the task contract; preview remains dispatcher-exempt.
		"task acceptance migrate": true,
		// Aggregate repair and WBS decomposition both persist immutable plans
		// and rewrite the task graph; their --dry-run previews remain exempt.
		"task aggregate": true, "task decompose": true,
		// Release trains persist a resumable GitHub promotion transaction and
		// may create/merge a PR or delete its landed source branch. Authority
		// changes are themselves durable project-policy writes.
		"release train": true, "release train authority": true,
		// Native provisioning validates private owner files instead of ambient
		// legacy grants; dispatcher tests cover both authority boundaries.
		"native bootstrap": true, "native enroll": true, "native revoke": true, "native upgrade": true,
	}

	for i := range commands {
		c := &commands[i]
		path := c.Path
		want, classified := wantMutating[path]
		if !classified {
			// Not in the mutating list: it must be a read command. If you just
			// added a command and landed here, decide which list it belongs in.
			if c.Mutates {
				t.Errorf("%s declares Mutates but is not in this test's table — add it, or drop the flag", path)
			}
			continue
		}
		if c.Mutates != want {
			t.Errorf("%s: Mutates = %v, want %v — the table and the command table disagree", path, c.Mutates, want)
		}
	}

	// And the reverse direction: a name in the table that no longer exists is
	// stale, so a renamed command cannot quietly lose its gate.
	have := map[string]bool{}
	for i := range commands {
		have[commands[i].Path] = true
	}
	for path, want := range wantMutating {
		if want && !have[path] {
			t.Errorf("table lists %q as mutating, but no such command exists — renamed or removed?", path)
		}
	}
}

// TestAuditedBypassesAreClosed drives the four escalation paths the 2026-08-06
// audit reproduced, through the real dispatcher, as a real read-only agent.
// The table test above proves the classification; this proves the enforcement.
func TestAuditedBypassesAreClosed(t *testing.T) {
	dir := t.TempDir()
	run(t, dir, 0, "init", "--name", "x")
	run(t, dir, 0, "project", "add", "P", "--slug", "p", "--goal", "a real goal for the project")

	out := run(t, dir, 0, "agent", "spawn", "--role", "junior", "--grant", "ro")
	token := strings.TrimSpace(strings.Split(strings.TrimSpace(out), "\n")[0])
	t.Setenv("DACLI_AGENT", token)

	for _, tc := range []struct {
		name string
		why  string
		args []string
	}{
		{
			"shortcut promote", "re-declares the effect of a command `run` later executes as the operator",
			[]string{"shortcut", "promote", "s", "--from-event", "01ABC", "--effect", "read"},
		},
		{
			"github push", "creates issues on a remote; the disclosure gate is a no-op for a private repo",
			[]string{"github", "push", "p"},
		},
		{
			"worktree remove", "force-deletes a peer agent's checkout, uncommitted work included",
			[]string{"worktree", "remove", "--task", "001"},
		},
		{
			"catalog", "writes a file chosen by --out",
			[]string{"catalog"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := run(t, dir, 3, tc.args...)
			if !strings.Contains(strings.ToLower(got), "grant") {
				t.Errorf("%s must refuse a ro caller because it %s; got:\n%s", tc.name, tc.why, got)
			}
		})
	}

	// Historical listing stays readable; host process reaping is removed.
	run(t, dir, 0, "agents")
	run(t, dir, 2, "agents", "--reap")
}
