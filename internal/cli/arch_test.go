package cli

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Host agents own execution. Reintroducing the removed launcher slices would
// restore a second agent lifecycle even if no public command advertised it.
func TestLegacyLaunchSlicesRemainAbsent(t *testing.T) {
	for _, name := range []string{"execution", "orchestration"} {
		_, err := os.Stat(filepath.Join("..", "features", name))
		if !errors.Is(err, os.ErrNotExist) {
			t.Errorf("removed provider launcher slice %s exists or cannot be observed: %v", name, err)
		}
	}
}

// The feature-sliced design's load-bearing rule: SLICES NEVER IMPORT EACH
// OTHER. Shared behavior belongs in clikit or the entity/engine layers;
// a feature→feature import is coupling that will calcify. This test is the
// rule's enforcement — without it, the isolation is a comment.
func TestFeatureSlicesAreIsolated(t *testing.T) {
	featuresDir := filepath.Join("..", "features")
	slices, err := os.ReadDir(featuresDir)
	if err != nil {
		t.Fatalf("features dir: %v", err)
	}
	importRe := regexp.MustCompile(`"github\.com/mlnomadpy/dacli/internal/features/([a-z]+)`)

	for _, slice := range slices {
		if !slice.IsDir() {
			continue
		}
		files, _ := os.ReadDir(filepath.Join(featuresDir, slice.Name()))
		for _, file := range files {
			if !strings.HasSuffix(file.Name(), ".go") {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(featuresDir, slice.Name(), file.Name()))
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range importRe.FindAllStringSubmatch(string(raw), -1) {
				if m[1] != slice.Name() {
					t.Errorf("slice %s imports slice %s (%s) — shared behavior belongs in clikit or an entity package",
						slice.Name(), m[1], file.Name())
				}
			}
		}
	}
}

// The app layer owns aggregation, not behavior: cli must not reach past the
// kernel into entities directly (the executor and mcp serve are the two
// sanctioned exceptions, and they need only clikit + mcp).
func TestAppLayerStaysThin(t *testing.T) {
	raw, err := os.ReadFile("cli.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"internal/store\"", "internal/eventlog\"", "internal/brief\"", "internal/spm\"",
	} {
		if strings.Contains(string(raw), forbidden) {
			t.Errorf("cli.go imports %s — feature logic is leaking back into the app layer", forbidden)
		}
	}
}

// The three oldest feature slices accumulated unrelated lifecycle concerns in
// single coordinator files. Keep the extracted boundaries load-bearing: a
// future change must extend the focused component instead of silently growing
// the old collision hotspot back past the reviewed ceiling (issue #901).
func TestLargeFeatureCoordinatorsStayDecomposed(t *testing.T) {
	featuresDir := filepath.Join("..", "features")
	components := map[string][]string{
		"history":   {"history.go", "replay.go", "handoff.go"},
		"reviewing": {"reviewing.go"},
		"ghmirror":  {"transport.go", "adoption.go", "publication.go", "project.go"},
	}
	coordinators := map[string]int{
		"ghmirror/ghmirror.go": 1100,
		// The next shared collision points after #901. These ceilings are close
		// to the reviewed trunk size on purpose: new persistence policy belongs
		// in a focused store component, and new GitHub/Git effects belong beside
		// lifecycle rather than growing its command coordinator (issue #913).
		"vcs/lifecycle.go": 2150,
	}
	for rel, ceiling := range coordinators {
		raw, err := os.ReadFile(filepath.Join(featuresDir, rel))
		if err != nil {
			t.Fatal(err)
		}
		if lines := strings.Count(string(raw), "\n") + 1; lines > ceiling {
			t.Errorf("%s grew to %d lines (ceiling %d); put the responsibility in its focused component", rel, lines, ceiling)
		}
	}
	for slice, files := range components {
		for _, file := range files {
			if _, err := os.Stat(filepath.Join(featuresDir, slice, file)); err != nil {
				t.Errorf("%s component %s is missing: %v", slice, file, err)
			}
		}
	}
	storeDir := filepath.Join("..", "store")
	if raw, err := os.ReadFile(filepath.Join(storeDir, "store.go")); err != nil {
		t.Fatal(err)
	} else if lines := strings.Count(string(raw), "\n") + 1; lines > 2400 {
		t.Errorf("store/store.go grew to %d lines (ceiling 2400); place the domain in a focused store component", lines)
	}
	for _, component := range []string{"readiness.go", "verification.go", "cleanup.go", "reconciliation.go", "review.go", "delivery_slices.go", "aggregate.go", "release_train.go", "root_handoff.go"} {
		if _, err := os.Stat(filepath.Join(storeDir, component)); err != nil {
			t.Errorf("store component %s is missing: %v", component, err)
		}
	}
}
