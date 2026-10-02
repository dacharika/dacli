package insight

import (
	"strings"
	"testing"

	"github.com/mlnomadpy/dacli/internal/mdstore"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/team"
)

func TestDoctorFlagsUnroutableAndTaskSpecificRoles(t *testing.T) {
	w, ctx := doctorEnv(t)
	if err := store.CreateRole(w, "a-root", team.Role{
		Name: "task-375-codex-architect", Summary: "one-off role for task 375",
		Runtime: "no-model-runtime", Model: "frontier",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateRuntime(w, "a-root", store.Runtime{
		Name: "no-model-runtime", Binary: "true", Mode: "stdin",
	}, ""); err != nil {
		t.Fatal(err)
	}
	doc, err := mdstore.ReadFile(w.RolePath("task-375-codex-architect"))
	if err != nil {
		t.Fatal(err)
	}
	doc.Front.Delete("version")
	if err := mdstore.WriteFile(w.RolePath("task-375-codex-architect"), doc); err != nil {
		t.Fatal(err)
	}

	out := doctorOut(t, ctx)
	for _, want := range []string{"role-metadata", "task-specific-role", "provider-specific-role"} {
		if !strings.Contains(out, want) {
			t.Fatalf("doctor missed %s:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "missing version") {
		t.Fatalf("doctor missed absent role version:\n%s", out)
	}
}

func TestDoctorFlagsTaskSpecificSummary(t *testing.T) {
	w, ctx := doctorEnv(t)
	if err := store.CreateRole(w, "a-root", team.Role{
		Name: "architect", Summary: "architecture owner for issue #375",
	}); err != nil {
		t.Fatal(err)
	}
	if out := doctorOut(t, ctx); !strings.Contains(out, "task-specific-role") {
		t.Fatalf("doctor missed task-specific role summary:\n%s", out)
	}
}

func TestDoctorFlagsExcessiveModelConcentration(t *testing.T) {
	w, ctx := doctorEnv(t)
	for _, name := range []string{"one", "two", "three", "four", "five"} {
		if err := store.CreateRole(w, "a-root", team.Role{
			Name: name, Summary: "complete role", Kind: "reviewer", Grant: "ro",
			Runtime: "review", Model: "expensive", Scope: []string{"**"},
			OutOfScope: []string{"generated/**"}, EscalateTo: []string{"human"},
			Skills:  []string{"evidence"},
			Profile: team.ModelProfile{ID: "expensive", CostTier: 3, MaxTaskPoints: 5, ContextLimit: 100000, CapabilityTags: []string{"review"}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	out := doctorOut(t, ctx)
	if !strings.Contains(out, "model-concentration") || !strings.Contains(out, "5/5") {
		t.Fatalf("doctor missed concentrated default model:\n%s", out)
	}
}
