package cli

import (
	"github.com/mlnomadpy/dacli/internal/store"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Acceptance 1: import ingests a native skill tree LOSSLESSLY — byte-equal
// copies, SKILL.md never renamed, resources and scripts intact.
func TestSkillImportLossless(t *testing.T) {
	dir := t.TempDir()
	run(t, dir, 0, "init", "--name", "x")

	// A fake ~/.claude/skills tree: native SKILL.md casing, a resource, an
	// executable script, and a nested directory.
	src := filepath.Join(dir, "native-skills")
	skillDir := filepath.Join(src, "tikz-figures")
	if err := os.MkdirAll(filepath.Join(skillDir, "refs"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"SKILL.md":     "---\nname: tikz-figures\ndescription: TikZ figures for ML papers.\n---\n# tikz-figures\n\nPalette and layout rules.\n",
		"palette.md":   "sage on warm-dark\n",
		"refs/deep.md": "nested reference\n",
		"compile.sh":   "#!/bin/sh\necho compile\n",
	}
	for name, content := range files {
		mode := os.FileMode(0o644)
		if strings.HasSuffix(name, ".sh") {
			mode = 0o755
		}
		if err := os.WriteFile(filepath.Join(skillDir, name), []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}

	out := run(t, dir, 0, "skill", "import", src)
	if !strings.Contains(out, "imported 1 skill(s) losslessly: tikz-figures") {
		t.Fatalf("import wrong:\n%s", out)
	}
	// Byte-for-byte, original names kept.
	for name, want := range files {
		got, err := os.ReadFile(filepath.Join(dir, ".dacli", "skills", "tikz-figures", name))
		if err != nil {
			t.Fatalf("file %s not copied: %v", name, err)
		}
		if string(got) != want {
			t.Errorf("file %s not byte-identical", name)
		}
	}
	// And the reader understands the native casing.
	list := run(t, dir, 0, "skill", "list")
	if !strings.Contains(list, "tikz-figures") || !strings.Contains(list, "scripts:1") {
		t.Errorf("imported skill not readable:\n%s", list)
	}
	// Re-import refuses rather than clobbering.
	run(t, dir, 1, "skill", "import", src)
}

// Acceptance 2: the context-file target announces the per-turn token tax —
// plus the floor semantics: min_delivery unmet is omitted AND announced, and
// scripts on a non-native target are named as undeliverable.
func TestSkillCompileFidelityLadder(t *testing.T) {
	dir := t.TempDir()
	run(t, dir, 0, "init", "--name", "x")

	run(t, dir, 0, "skill", "add", "prose-only", "--desc", "writing guidance",
		"--body", strings.Repeat("Write plainly. ", 50))
	run(t, dir, 0, "skill", "add", "native-only", "--desc", "needs lazy loading",
		"--body", "big body", "--min-delivery", "native")
	// A skill with a script, authored then given a resource by hand.
	run(t, dir, 0, "skill", "add", "with-script", "--desc", "has a tool", "--body", "use the tool")
	if err := os.WriteFile(filepath.Join(dir, ".dacli", "skills", "with-script", "tool.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Runtime with a native dir: everything rides native, no tax at all.
	seedHistoricalRuntime(t, dir, store.Runtime{Name: "nat", Binary: "sh", SkillsNativeDir: ".claude/skills"})
	out := run(t, dir, 0, "skill", "compile", "--runtime", "nat")
	if strings.Contains(out, "per-turn tax") {
		t.Errorf("native target should announce no tax:\n%s", out)
	}
	if !strings.Contains(out, "compiled to") {
		t.Errorf("native compile failed:\n%s", out)
	}
	// Native output is the skill dir, copied.
	if _, err := os.Stat(filepath.Join(dir, ".dacli", "build", "skills", "nat", "_all", "with-script", "tool.sh")); err != nil {
		t.Errorf("native copy missing script: %v", err)
	}

	// Runtime with only a context file: tax announced per skill and in
	// total; the native-only skill is OMITTED with its floor named; the
	// script is called out as undeliverable.
	seedHistoricalRuntime(t, dir, store.Runtime{Name: "ctxrt", Binary: "sh", SkillsContextFile: "AGENTS.md"})
	out = run(t, dir, 0, "skill", "compile", "--runtime", "ctxrt")
	for _, want := range []string{
		"per-turn tax ~", "full body, every turn",
		"total per-turn tax on ctxrt",
		"progressive disclosure is gone",
		"→ omitted",
		"min_delivery native, but ctxrt only offers context",
		"script(s) cannot ride a context target",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("context compile missing %q:\n%s", want, out)
		}
	}
	// The managed context file exists with markers; the omitted skill is
	// genuinely absent from it.
	raw, err := os.ReadFile(filepath.Join(dir, ".dacli", "build", "skills", "ctxrt", "_all", "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "<!-- dacli:skill:prose-only begin -->") {
		t.Error("context file missing managed markers")
	}
	if strings.Contains(string(raw), "native-only") {
		t.Error("omitted skill leaked into the context file")
	}

	// Role-scoped compile: only the role's skills, and dry-run writes nothing.
	run(t, dir, 0, "role", "add", "writer", "--skill", "prose-only", "--grant", "rw")
	out = run(t, dir, 0, "skill", "compile", "--runtime", "ctxrt", "--role", "writer", "--dry-run")
	if !strings.Contains(out, "prose-only") || strings.Contains(out, "with-script") {
		t.Errorf("role scoping wrong:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(dir, ".dacli", "build", "skills", "ctxrt", "writer")); !os.IsNotExist(err) {
		t.Error("dry-run wrote output")
	}
}

// SKILLS.md § 6: a lesson never auto-promotes to a skill — promotion is an
// explicit act by the workspace owner (a-root), gated against every spawned
// agent regardless of grant, and the produced skill is versioned and carries
// the lesson's provenance forward.
func TestSkillPromoteOwnerGated(t *testing.T) {
	dir := t.TempDir()
	run(t, dir, 0, "init", "--name", "x")
	run(t, dir, 0, "project", "add", "Alpha", "--slug", "alpha", "--goal", "a")

	// A project-scoped finding is not a lesson: not promotable (not found,
	// same as any other unresolvable ref).
	run(t, dir, 0, "note", "add", "finding", "Local detail only",
		"--project", "alpha", "--body", "not workspace-scoped")
	run(t, dir, 4, "skill", "promote", "f-local-detail-only")

	// A workspace-scoped lesson, carrying a suspect origin, IS promotable —
	// but not by a spawned agent, however wide its grant.
	run(t, dir, 0, "note", "add", "finding", "Always audit write paths first",
		"--project", "alpha", "--body", "audit before estimating ledger work",
		"--scope", "workspace", "--origin", "file:cron/settle.go")

	tok := strings.TrimSpace(strings.Split(run(t, dir, 0, "agent", "spawn", "--grant", "rw"), "\n")[0])
	t.Setenv("DACLI_AGENT", tok)
	run(t, dir, 3, "skill", "promote", "f-always-audit-write-paths-first")
	_ = os.Unsetenv("DACLI_AGENT")

	// The owner (root) promotes it: a versioned skill lands in the library,
	// inheriting the lesson's origin.
	out := run(t, dir, 0, "skill", "promote", "f-always-audit-write-paths-first")
	if !strings.Contains(out, "promoted lesson f-always-audit-write-paths-first") {
		t.Fatalf("promote output wrong:\n%s", out)
	}
	raw, err := os.ReadFile(filepath.Join(dir, ".dacli", "skills", "always-audit-write-paths-first", "skill.md"))
	if err != nil {
		t.Fatalf("skill not written: %v", err)
	}
	got := string(raw)
	for _, want := range []string{"version: v1", "origin: file:cron/settle.go", "promoted_from: f-always-audit-write-paths-first", "audit before estimating ledger work"} {
		if !strings.Contains(got, want) {
			t.Errorf("skill.md missing %q:\n%s", want, got)
		}
	}
	list := run(t, dir, 0, "skill", "list")
	if !strings.Contains(list, "always-audit-write-paths-first") {
		t.Errorf("promoted skill not visible to skill list:\n%s", list)
	}

	// Re-promoting the same lesson under the same name refuses rather than
	// clobbering — same discipline as `skill add`/`skill import`.
	run(t, dir, 1, "skill", "promote", "f-always-audit-write-paths-first")

	// An unknown ref is a clean not-found, not a crash.
	run(t, dir, 4, "skill", "promote", "no-such-lesson")
}
