package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The architecture diagrams (docs/DIAGRAMS.md, task 316) are committed as
// Mermaid text precisely so they diff in review and cannot drift silently from
// the system they depict. These tests are that "cannot drift silently"
// promise made mechanical: the moment a feature slice is added or renamed and
// the component diagram is not updated to name it, the build fails — the same
// discipline arch_test.go applies to the import rules the diagram draws.

// TestDiagramsCoverEveryFeatureSlice fails if any internal/features/* slice is
// missing from docs/DIAGRAMS.md. The component view claims to be the current
// inventory; this makes that claim enforceable instead of a snapshot that rots.
func TestDiagramsCoverEveryFeatureSlice(t *testing.T) {
	text := readDiagrams(t)

	featuresDir := filepath.Join("..", "features")
	entries, err := os.ReadDir(featuresDir)
	if err != nil {
		t.Fatalf("features dir: %v", err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		slice := e.Name()
		// Skip a slice directory that ships no production Go (only tests):
		// the diagram documents capabilities, and a test-only package is not
		// one. Every real slice has at least one non-_test.go file.
		if !hasProductionGo(t, filepath.Join(featuresDir, slice)) {
			continue
		}
		if !mentionsWord(text, slice) {
			t.Errorf("docs/DIAGRAMS.md does not name the feature slice %q — the component diagram has drifted from internal/features/ (task 316)", slice)
		}
	}
}

// TestDiagramsHaveAllThreeViews guards the acceptance contract: the set must
// cover structure (a component graph), one end-to-end flow (a sequence), and
// the state a task moves through (a state machine). A diagram silently deleted
// or never added is caught here rather than by a reader who trusted the doc.
func TestDiagramsHaveAllThreeViews(t *testing.T) {
	text := readDiagrams(t)
	for _, kind := range []struct{ needle, view string }{
		{"```mermaid", "at least one Mermaid diagram"},
		{"graph TD", "a component graph (structure)"},
		{"sequenceDiagram", "a native task-through-landing sequence"},
		{"stateDiagram", "a task-lifecycle state machine"},
	} {
		if !strings.Contains(text, kind.needle) {
			t.Errorf("docs/DIAGRAMS.md is missing %s (looked for %q)", kind.view, kind.needle)
		}
	}
}

// TestNormativeDocsDescribeImplementedStateAndBriefAssembly keeps the claims
// injected into task briefs aligned with the code. An old lock-free/pure-brief
// description would direct agents away from the state the implementation has
// (core #467).
func TestNormativeDocsDescribeImplementedStateAndBriefAssembly(t *testing.T) {
	docs := map[string][]string{
		"DESIGN.md": {
			"serialized shared transitions",
			"recovery-critical",
			"implemented and central",
		},
		"docs/ARCHITECTURE.md": {
			"I/O assembly service",
			"pure policy engines",
			"Concurrency and persistence",
		},
		"docs/DIAGRAMS.md": {
			"serialized state transitions",
			"I/O assembly service",
		},
	}
	for name, required := range docs {
		raw, err := os.ReadFile(filepath.Join("..", "..", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		text := strings.ToLower(string(raw))
		for _, want := range required {
			if !strings.Contains(text, strings.ToLower(want)) {
				t.Errorf("%s no longer states %q", name, want)
			}
		}
		for _, stale := range []string{
			"There is no shared mutable state, therefore no race, therefore no lock.",
			"**Specification only; nothing implemented.**",
			"**Specification only.**",
		} {
			if strings.Contains(text, stale) {
				t.Errorf("%s retains stale normative claim %q", name, stale)
			}
		}
	}
	briefDoc, err := os.ReadFile(filepath.Join("..", "brief", "brief.go"))
	if err != nil {
		t.Fatalf("read brief package documentation: %v", err)
	}
	if !strings.Contains(strings.ToLower(string(briefDoc)), "i/o assembly service") {
		t.Error("internal/brief package documentation must identify Assemble as an I/O assembly service")
	}
}

func readDiagrams(t *testing.T) string {
	t.Helper()
	// internal/cli -> repo root -> docs/DIAGRAMS.md
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "DIAGRAMS.md"))
	if err != nil {
		t.Fatalf("read docs/DIAGRAMS.md: %v", err)
	}
	return string(raw)
}

func hasProductionGo(t *testing.T, dir string) bool {
	t.Helper()
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	for _, f := range files {
		if strings.HasSuffix(f.Name(), ".go") && !strings.HasSuffix(f.Name(), "_test.go") {
			return true
		}
	}
	return false
}

// mentionsWord reports whether name appears as a whole token (so "ship" is not
// matched inside "relationship"), which is how a slice name reads in the doc.
func mentionsWord(text, name string) bool {
	for _, field := range strings.FieldsFunc(text, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_')
	}) {
		if field == name {
			return true
		}
	}
	return false
}
