package store

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/mlnomadpy/dacli/internal/mdstore"
	"github.com/mlnomadpy/dacli/internal/procmon"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

// Task 371 named only its documentation path literally. Its behavioral
// acceptance still required runtime persistence, execution, and CLI changes,
// but the old claim therefore fenced its implementer into docs/RUNTIMES.md and
// refused all six code files at commit time.
func TestClaimHintsInferTask371ImplementationScope(t *testing.T) {
	w, err := workspace.Init(t.TempDir(), "x")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CreateProject(w, "a-root", "Core", "core", "g", ""); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"docs/RUNTIMES.md",
		"internal/store/runtimefiles.go",
		"internal/features/execution/execution.go",
		"internal/cli/cli.go",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(w.Root, path)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(w.Root, path), []byte("fixture\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	task, err := CreateTask(w, "a-root", "core", "Add first-class Codex CLI runtime presets and structured results", TaskOpts{Accept: []string{
		"runtime add accepts Codex read-write and read-only presets",
		"The Codex adapter consumes JSONL events and records the final message, session identity, exit outcome, and token usage",
		"runtime doctor verifies Codex read-only isolation through a local sandbox helper",
		"A fake Codex fixture covers flag ordering, stdin prompts, JSONL parsing, nonzero exits, and read-only probe refusal",
		"docs/RUNTIMES.md documents Codex as shipped support",
	}})
	if err != nil {
		t.Fatal(err)
	}

	got := ClaimHints(w.Root, task)
	for _, want := range []string{"docs/RUNTIMES.md", "internal/store", "internal/features/execution", "internal/cli"} {
		if !slices.Contains(got, want) {
			t.Errorf("ClaimHints = %v, missing inferred implementation scope %q", got, want)
		}
	}
	if slices.Contains(got, "internal/features/orchestration") {
		t.Errorf("ClaimHints = %v, unrelated paths must remain outside the claim", got)
	}
}

func TestClaimHintsExplicitDeclarationIsAuthoritative(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{"internal/governance/sessions", "internal/governance/policy", "internal/features/execution"} {
		if err := os.MkdirAll(filepath.Join(root, path), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name   string
		claims []string
		want   []string
	}{
		{"existing", []string{"internal/governance/sessions", "internal/governance/policy"}, []string{"internal/governance/sessions", "internal/governance/policy"}},
		{"new", []string{"internal/governance/new", "new-package/first.go"}, []string{"internal/governance/new", "new-package/first.go"}},
		{"normalized", []string{"./internal/governance/sessions/", "internal/governance/sessions"}, []string{"internal/governance/sessions"}},
		{"legitimate-dot-path", []string{".github/workflows", "pkg/new.go"}, []string{".github/workflows", "pkg/new.go"}},
		{"invalid", []string{"/internal/governance/sessions", "../outside", ".", ".dacli", ".dacli/agents", ".git", ".git/config", "internal/../outside", "internal\\outside", "", "internal/secret\npath"}, nil},
		{"empty", []string{}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			task := &Task{Title: "Preserve sandbox execution boundaries", Doc: &mdstore.Doc{Sections: []mdstore.Section{{Title: "Context", Content: "Explain why internal/features/execution is outside this task's scope."}}}}
			task.Doc.Front.SetList("claims", tc.claims)
			if got := ClaimHints(root, task); !slices.Equal(got, tc.want) {
				t.Fatalf("explicit %s claims widened or lost: got %v want %v", tc.name, got, tc.want)
			}
		})
	}
	task := &Task{Title: "Explain sandbox execution", Doc: &mdstore.Doc{}}
	task.Doc.Front.Set("claims", "not-a-list")
	if got := ClaimHints(root, task); len(got) != 0 {
		t.Fatalf("malformed declaration fell back to inference: %v", got)
	}
	legacy := &Task{Title: "Fix sandbox execution", Doc: &mdstore.Doc{}}
	if got := ClaimHints(root, legacy); !slices.Equal(got, []string{"internal/features/execution"}) {
		t.Fatalf("legacy vocabulary inference changed: %v", got)
	}
}

// Task 381's acceptance named shared estimate validation and three command
// surfaces, but its old claim inferred only internal/store. The finished
// implementation consequently reached four additional package boundaries and
// was refused at commit time.
func TestClaimHintsInferTask381ImplementationScope(t *testing.T) {
	w, err := workspace.Init(t.TempDir(), "x")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CreateProject(w, "a-root", "Core", "core", "g", ""); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"internal/store/store.go",
		"internal/spm/estimate.go",
		"internal/features/planning/planning.go",
		"internal/features/insight/insight.go",
		"internal/features/orchestration/orchestration.go",
		"internal/cli/cli.go",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(w.Root, path)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(w.Root, path), []byte("fixture\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	task, err := CreateTask(w, "a-root", "core", "Reject non-finite task estimates before they corrupt scheduling and timeout policy", TaskOpts{Accept: []string{
		"task add --estimate Inf,Inf,Inf and task estimate <ref> --estimate Inf,Inf,Inf both fail with a usage error and leave task frontmatter unchanged",
		"spm.ThreePoint.Valid rejects NaN and positive or negative infinity in every estimate point, and store estimate parsing rejects non-numeric values before persisting them",
		"finite ordered three-point estimates continue to round-trip through task creation and resizing, and critical-path output contains neither Inf nor NaN for accepted estimates",
		"regression tests cover both creation and resizing command paths plus the shared ThreePoint validation",
	}})
	if err != nil {
		t.Fatal(err)
	}

	got := ClaimHints(w.Root, task)
	for _, want := range []string{"internal/store", "internal/spm", "internal/features/planning", "internal/features/insight", "internal/cli"} {
		if !slices.Contains(got, want) {
			t.Errorf("ClaimHints = %v, missing inferred task 381 scope %q", got, want)
		}
	}
	if slices.Contains(got, "internal/features/orchestration") {
		t.Errorf("ClaimHints = %v, unrelated feature slices must remain outside the claim", got)
	}
	for _, changed := range []string{
		"internal/cli/dogfood_test.go",
		"internal/features/insight/criticalpath_test.go",
		"internal/features/planning/estimate_test.go",
		"internal/features/planning/planning.go",
		"internal/features/planning/planning_test.go",
		"internal/spm/estimate.go",
		"internal/spm/spm_test.go",
		"internal/store/store.go",
	} {
		if _, _, covered := procmon.PathsOverlap([]string{changed}, got); !covered {
			t.Errorf("ClaimHints %v would refuse recorded task 381 file %q", got, changed)
		}
	}
	if _, _, covered := procmon.PathsOverlap([]string{"internal/features/orchestration/orchestration.go"}, got); covered {
		t.Errorf("ClaimHints %v would allow an unrelated feature slice", got)
	}
}

// PathHints is deliberately crude — a slash or a .go suffix is enough — because
// for routing a spurious token costs one weak tie-break vote. The loop then
// reused it as `spawn --claim`, where a claim REFUSES every staged file outside
// it. Task 338's acceptance criteria mention the gosec rule list
// "G104/G301/G302/G306"; that became the agent's entire claim and its commit of
// eighteen legitimate files was refused (issue #427).
func TestClaimHintsRejectsProseThatMerelyContainsASlash(t *testing.T) {
	w, err := workspace.Init(t.TempDir(), "x")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CreateProject(w, "a-root", "Core", "core", "g", ""); err != nil {
		t.Fatal(err)
	}
	// A real file the task legitimately names.
	real := filepath.Join("internal", "store", "store.go")
	if err := os.MkdirAll(filepath.Join(w.Root, "internal", "store"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.Root, real), []byte("package store\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	task, err := CreateTask(w, "a-root", "core", "Stage gosec with a curated rule list", TaskOpts{
		Accept: []string{
			"gosec runs with G104/G301/G302/G306 excluded",
			"the and/or cases in internal/store/store.go are covered",
			"a 50/50 split is handled",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// The crude hinter still yields the junk — that is its documented contract
	// and routing tolerates it. If this stops being true the test below is
	// measuring nothing, so assert it.
	hints := task.PathHints()
	sawJunk := false
	for _, h := range hints {
		if h == "G104/G301/G302/G306" || h == "and/or" || h == "50/50" {
			sawJunk = true
		}
	}
	if !sawJunk {
		t.Fatalf("PathHints no longer yields the junk tokens this test guards against: %v", hints)
	}

	got := ClaimHints(w.Root, task)
	for _, bad := range []string{"G104/G301/G302/G306", "and/or", "50/50"} {
		for _, g := range got {
			if g == bad {
				t.Errorf("%q became a claim; prose containing a slash is not a path", bad)
			}
		}
	}
	// The real path survives, or the filter has thrown away the signal too.
	found := false
	for _, g := range got {
		if g == real || g == "internal/store/store.go" {
			found = true
		}
	}
	if !found {
		t.Errorf("the real path %q was filtered out along with the junk: %v", real, got)
	}
}

// A bare filename is plausible and still wrong: claims match by exact path or
// path-prefix (procmon.PathsOverlap), so a claim of "acceptance.go" overlaps NO
// staged file and blocks the whole commit just as thoroughly as junk does.
//
// Yielding nothing is the safe outcome: an agent with no recorded claim is
// warned once and proceeds, while a wrong claim is a lockout only --force gets
// past.
func TestClaimHintsDropsABareFilenameThatIsNotARepoPath(t *testing.T) {
	w, err := workspace.Init(t.TempDir(), "x")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CreateProject(w, "a-root", "Core", "core", "g", ""); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(w.Root, "internal", "features", "acceptance"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.Root, "internal", "features", "acceptance", "acceptance.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	task, err := CreateTask(w, "a-root", "core", "Cover the unlanded-close refusal", TaskOpts{
		Accept: []string{"acceptance.go:148 refuses the close"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := ClaimHints(w.Root, task); len(got) != 0 {
		t.Errorf("ClaimHints = %v; a bare filename is not a repo path and would match nothing staged", got)
	}
}

// Task 408 named a directory it had to create. Requiring the leaf to exist
// discarded that explicit boundary, then the prose phrase "agent execution"
// inferred an unrelated existing feature slice and fenced every required file
// out of the implementer's claim (issue #580).
func TestClaimHintsPreservesExplicitNewTask408Boundary(t *testing.T) {
	w, err := workspace.Init(t.TempDir(), "x")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CreateProject(w, "a-root", "Core", "core", "g", ""); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"contracts", "internal/features/execution"} {
		if err := os.MkdirAll(filepath.Join(w.Root, filepath.FromSlash(path)), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	task, err := CreateTask(w, "a-root", "core", "Define the signed control-plane event protocol", TaskOpts{
		Context: "Use contracts/controlplane/v1 as the source of truth for agent execution.",
		Accept: []string{
			"Create seven closed schemas under contracts/controlplane/v1",
			"contracts/controlplane/v1/contract_test.go verifies the golden outcomes",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	got := ClaimHints(w.Root, task)
	if !slices.Contains(got, "contracts/controlplane/v1") {
		t.Fatalf("ClaimHints = %v, missing explicit new implementation boundary", got)
	}
	if slices.Contains(got, "internal/features/execution") {
		t.Fatalf("ClaimHints = %v, generic prose overrode the explicit implementation boundary", got)
	}
	for _, required := range []string{
		"contracts/controlplane/v1/envelope.schema.json",
		"contracts/controlplane/v1/contract_test.go",
	} {
		if _, _, covered := procmon.PathsOverlap([]string{required}, got); !covered {
			t.Errorf("ClaimHints %v would refuse required new file %q", got, required)
		}
	}
}
