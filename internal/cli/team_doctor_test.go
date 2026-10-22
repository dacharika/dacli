package cli

import (
	"os"
	"strings"
	"testing"
)

// Roles change what an agent can do. Identity history is deliberately separate
// from live WIP occupancy: token-only `agent spawn` has no running process.
func TestRoleWiredIntoSpawn(t *testing.T) {
	dir := t.TempDir()
	run(t, dir, 0, "init", "--name", "x")

	// A name-only role draws the cosplay warning.
	out := run(t, dir, 0, "role", "add", "vibes")
	if !strings.Contains(out, "costume, not a role") {
		t.Errorf("cosplay role not warned:\n%s", out)
	}

	run(t, dir, 0, "role", "add", "auditor",
		"--summary", "read-only audit work",
		"--grant", "ro", "--wip", "3",
		"--skill", "math-paper-audit",
		"--scope", "internal/**",
		"--escalate-to", "architect")

	// Spawn takes the role's default grant and surfaces its skills.
	out = run(t, dir, 0, "agent", "spawn", "--role", "auditor")
	if !strings.Contains(out, "grant: ro") {
		t.Errorf("role default grant not applied:\n%s", out)
	}
	if !strings.Contains(out, "math-paper-audit") {
		t.Errorf("role skills not surfaced at spawn:\n%s", out)
	}

	// A second historical identity is not a second live run and therefore does
	// not consume the role's live-process WIP capacity.
	run(t, dir, 0, "agent", "spawn", "--role", "auditor")
	if agents := run(t, dir, 0, "agents"); !strings.Contains(agents, "no recorded workers for project") {
		t.Errorf("agents liveness wrong:\n%s", agents)
	}
	team := run(t, dir, 0, "team")
	if !strings.Contains(team, "auditor") || !strings.Contains(team, "occupancy:0 headroom:3") {
		t.Errorf("team disagrees with agents about zero live occupancy:\n%s", team)
	}

	// Retire the first; the slot frees.
	tree := run(t, dir, 0, "agent", "tree")
	var childID string
	for _, line := range strings.Split(tree, "\n") {
		if strings.Contains(line, "auditor") {
			childID = strings.Fields(strings.TrimSpace(line))[0]
		}
	}
	if childID == "" {
		t.Fatalf("no auditor in tree:\n%s", tree)
	}
	run(t, dir, 0, "agent", "retire", childID)
	run(t, dir, 0, "agent", "spawn", "--role", "auditor")

	// Retirement changes identity history but not live occupancy.
	team = run(t, dir, 0, "team")
	if !strings.Contains(team, "auditor") || !strings.Contains(team, "occupancy:0 headroom:3") {
		t.Errorf("team roster wrong:\n%s", team)
	}
}

func TestTeamRoute(t *testing.T) {
	dir := t.TempDir()
	run(t, dir, 0, "init", "--name", "x")
	run(t, dir, 0, "role", "add", "frontend", "--scope", "web/**", "--escalate-to", "backend")
	run(t, dir, 0, "role", "add", "backend", "--scope", "internal/**", "--escalate-to", "human")

	out := run(t, dir, 0, "team", "route", "internal/spm/x.go", "--from", "frontend")
	if !strings.Contains(out, "owners (most specific first): backend") {
		t.Errorf("owner wrong:\n%s", out)
	}
	if !strings.Contains(out, "frontend → backend") {
		t.Errorf("chain wrong:\n%s", out)
	}

	// Owner exists but no edge reaches it: the message names the gap (G8).
	run(t, dir, 0, "role", "add", "sre", "--scope", "infra/**")
	run(t, dir, 0, "role", "add", "docs", "--scope", "docs/**", "--escalate-to", "frontend")
	broken := run(t, dir, 1, "team", "route", "infra/main.tf", "--from", "docs")
	if !strings.Contains(broken, "sre owns this") || !strings.Contains(broken, "escalate_to") {
		t.Errorf("missing-edge message wrong:\n%s", broken)
	}
}

func TestDoctorDetectors(t *testing.T) {
	dir := t.TempDir()
	run(t, dir, 0, "init", "--name", "x")
	run(t, dir, 0, "project", "add", "P", "--slug", "p", "--goal", "g")

	// Clean workspace: no findings.
	if out := run(t, dir, 0, "doctor"); !strings.Contains(out, "no anti-patterns") {
		t.Errorf("clean workspace flagged:\n%s", out)
	}

	// Cart Before the Horse: a could active while a must sits open.
	run(t, dir, 0, "task", "add", "The load-bearing migration", "--project", "p", "--priority", "must", "--accept", "a")
	run(t, dir, 0, "task", "add", "Nice-to-have cleanup pass", "--project", "p", "--priority", "could", "--accept", "a")
	run(t, dir, 0, "task", "claim", "002")
	out := run(t, dir, 0, "doctor")
	if !strings.Contains(out, "cart-before-the-horse") {
		t.Errorf("cart not detected:\n%s", out)
	}

	// Unmanaged rank-1 risk.
	run(t, dir, 0, "risk", "add", "Data loss on migrate", "--project", "p", "--impact", "high", "--likelihood", "high")
	out = run(t, dir, 0, "doctor")
	if !strings.Contains(out, "unmanaged-risk") {
		t.Errorf("rank-1 risk without action not detected:\n%s", out)
	}

	// Unanswered question.
	run(t, dir, 0, "ask", "Is the schema frozen?", "--about", "002")
	out = run(t, dir, 0, "doctor")
	if !strings.Contains(out, "unanswered-questions") {
		t.Errorf("open question not detected:\n%s", out)
	}
}

func TestStandupAndRetro(t *testing.T) {
	dir := t.TempDir()
	run(t, dir, 0, "init", "--name", "x")
	run(t, dir, 0, "project", "add", "P", "--slug", "p", "--goal", "g")
	run(t, dir, 0, "task", "add", "Ship the thing", "--project", "p", "--accept", "a")
	run(t, dir, 0, "task", "claim", "001")
	run(t, dir, 0, "task", "check", "001", "--all")
	run(t, dir, 0, "task", "done", "001")

	out := run(t, dir, 0, "standup")
	if !strings.Contains(out, "a-root") || !strings.Contains(out, "done:") || !strings.Contains(out, "001-ship-the-thing") {
		t.Errorf("standup roll-up wrong:\n%s", out)
	}

	// Retro requires content, records a note, and --scope workspace marks it
	// as a cross-project lesson (the P1 capture field).
	run(t, dir, 2, "retro", "001")
	out = run(t, dir, 0, "retro", "001",
		"--well", "acceptance criteria kept the scope tight",
		"--bad", "estimate was 2x off",
		"--improve", "audit the batch path before estimating",
		"--scope", "workspace")
	if !strings.Contains(out, "retro recorded") {
		t.Errorf("retro failed:\n%s", out)
	}
	// The retro is a ref note (briefs deliberately don't pull refs); verify
	// the durable artifact directly, including the workspace scope field.
	path := strings.TrimSpace(strings.TrimPrefix(out, "retro recorded:"))
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("retro note missing at %q: %v", path, err)
	}
	for _, want := range []string{"estimate was 2x off", "Went well", "scope: workspace"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("retro note missing %q:\n%s", want, raw)
		}
	}
}
