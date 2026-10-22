// doctor's data-integrity checks are the ones with the worst failure mode in
// this tool: a health check whose entire job is finding problems reports
// "clean" when it breaks, and nothing downstream contradicts it. This repo has
// already shipped that shape — `lint --status opne` filtered to nothing and
// printed a clean bill of health for a backlog it had never looked at.
//
// So these tests do not measure coverage of cmdDoctor. Each one PLANTS a
// specific corruption and requires doctor to name it, and one requires doctor
// to stay silent on a healthy workspace, because a check that always fires is
// as useless as one that never does.
package insight

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/team"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

// doctorOut runs doctor and returns what an operator would read. doctor exits
// non-zero when it finds something, so a non-nil error is expected here and the
// output is the assertion surface.
func doctorOut(t *testing.T, ctx *clikit.Ctx) string {
	t.Helper()
	_ = cmdDoctor(ctx, nil)
	return ctx.Stdout.(*bytes.Buffer).String()
}

// openTaskPath is where a task file lives given its filename, since status is
// folder position and these tests move and copy files directly.
func openTaskPath(w *workspace.Workspace, base string) string {
	return filepath.Join(w.TasksDir("p", model.StatusOpen), base)
}

// TestDoctorIsSilentOnAHealthyWorkspace is the crying-wolf guard for every test
// below: each of them asserts a pattern is PRESENT, and all of those pass
// trivially if doctor reports everything all the time.
func TestDoctorIsSilentOnAHealthyWorkspace(t *testing.T) {
	w, ctx := doctorEnv(t)
	if _, err := store.CreateTask(w, "a-root", "p", "Ordinary healthy work", store.TaskOpts{Accept: []string{"the binary builds"}}); err != nil {
		t.Fatal(err)
	}
	out := doctorOut(t, ctx)
	if !strings.Contains(out, "no anti-patterns detected") {
		t.Fatalf("a healthy workspace must produce a clean report, got:\n%s", out)
	}
}

// TestDoctorFlagsDuplicateTaskFile: the same task living in two status folders
// is what made FindTask report the same task "ambiguous" against itself (026
// was in both open/ and done/). ListTasks dedups it away, which is precisely
// why doctor has to say it is there.
func TestDoctorFlagsDuplicateTaskFile(t *testing.T) {
	w, ctx := doctorEnv(t)
	tk, err := store.CreateTask(w, "a-root", "p", "Work that got copied", store.TaskOpts{Accept: []string{"a"}})
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(tk.Path)
	if err != nil {
		t.Fatal(err)
	}
	doneDir := w.TasksDir("p", model.StatusDone)
	if err := os.MkdirAll(doneDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(doneDir, filepath.Base(tk.Path)), body, 0o644); err != nil {
		t.Fatal(err)
	}

	out := doctorOut(t, ctx)
	if !strings.Contains(out, "duplicate-task-file") {
		t.Fatalf("expected duplicate-task-file, got:\n%s", out)
	}
	if !strings.Contains(out, tk.Slug) {
		t.Fatalf("the report must name the task, got:\n%s", out)
	}
	// Both paths, or the reader cannot tell which copy to delete.
	if !strings.Contains(out, "open") || !strings.Contains(out, "done") {
		t.Fatalf("the report must name both status folders, got:\n%s", out)
	}
}

// TestDoctorFlagsCollidedSeq: two DIFFERENT tasks under one NNN is the scar a
// cross-branch collision leaves after both branches merge. Allocation bars new
// ones; a pre-existing pair is invisible until `dacli NNN` fails at the point
// of use, which is the worst possible moment to discover it.
func TestDoctorFlagsCollidedSeq(t *testing.T) {
	w, ctx := doctorEnv(t)
	first, err := store.CreateTask(w, "a-root", "p", "The task that holds the number", store.TaskOpts{Accept: []string{"a"}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.CreateTask(w, "a-root", "p", "A different task entirely", store.TaskOpts{Accept: []string{"a"}})
	if err != nil {
		t.Fatal(err)
	}
	// Seq comes from the filename, so renaming is how the collision is made —
	// the ids stay distinct, so this is two tasks sharing one number rather
	// than one task duplicated.
	collided := openTaskPath(w, strings.Replace(filepath.Base(second.Path), "002-", "001-", 1))
	if err := os.Rename(second.Path, collided); err != nil {
		t.Fatal(err)
	}

	out := doctorOut(t, ctx)
	if !strings.Contains(out, "collided-seq") {
		t.Fatalf("expected collided-seq, got:\n%s", out)
	}
	if !strings.Contains(out, first.Slug) || !strings.Contains(out, second.Slug) {
		t.Fatalf("both colliding tasks must be named or neither can be renumbered, got:\n%s", out)
	}
	if strings.Contains(out, "duplicate-task-file") {
		t.Fatalf("two different tasks sharing a seq is not one task duplicated, got:\n%s", out)
	}
}

// TestDoctorFlagsHollowTask: a task whose frontmatter is gone still LISTS —
// status comes from its folder and seq/slug from its filename — so it shows up
// as a row with no id, no title and no acceptance criteria while every list
// path carries on as if the workspace were healthy (dacli 204).
func TestDoctorFlagsHollowTask(t *testing.T) {
	w, ctx := doctorEnv(t)
	hollow := openTaskPath(w, "007-hollowed-out.md")
	if err := os.MkdirAll(filepath.Dir(hollow), 0o755); err != nil {
		t.Fatal(err)
	}
	// Parses cleanly; says nothing. That is the whole danger.
	if err := os.WriteFile(hollow, []byte("---\nkind: task\n---\n\nsome body text\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out := doctorOut(t, ctx)
	if !strings.Contains(out, "corrupt-object") {
		t.Fatalf("expected corrupt-object, got:\n%s", out)
	}
	if !strings.Contains(out, "hollowed-out") {
		t.Fatalf("the report must name the file, got:\n%s", out)
	}
}

// TestDoctorFlagsUnparseableTask covers the file the hollow check above can
// never see: the hollow check iterates the LISTING, and a file with a conflict
// marker in its frontmatter never reaches the listing at all. Before it was
// recorded, it was invisible to every reader including the one whose job is
// finding it.
func TestDoctorFlagsUnparseableTask(t *testing.T) {
	w, ctx := doctorEnv(t)
	broken := openTaskPath(w, "009-conflicted.md")
	if err := os.MkdirAll(filepath.Dir(broken), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(broken, []byte("---\n<<<<<<< HEAD\ntitle: ours\n=======\ntitle: theirs\n>>>>>>> other\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out := doctorOut(t, ctx)
	if !strings.Contains(out, "unparseable-task") {
		t.Fatalf("expected unparseable-task, got:\n%s", out)
	}
	if !strings.Contains(out, "009-conflicted.md") {
		t.Fatalf("the report must name the file, got:\n%s", out)
	}
	if strings.Contains(out, "corrupt-object") {
		t.Fatalf("a file that never parsed cannot be reported by the listing-based check, got:\n%s", out)
	}
}

// TestDoctorFlagsUnresolvableDependency: the readiness predicate holds a task
// with a bad ref back rather than running work whose prerequisite may not
// exist. That is the safe call, and it starves the task forever unless
// something says why — doctor is where the why lives (dacli 240).
func TestDoctorFlagsUnresolvableDependency(t *testing.T) {
	w, ctx := doctorEnv(t)
	tk, err := store.CreateTask(w, "a-root", "p", "Waits on a task that does not exist",
		store.TaskOpts{Accept: []string{"a"}, DependsOn: []string{"404"}})
	if err != nil {
		t.Fatal(err)
	}

	out := doctorOut(t, ctx)
	if !strings.Contains(out, "unresolvable-dependency") {
		t.Fatalf("expected unresolvable-dependency, got:\n%s", out)
	}
	if !strings.Contains(out, tk.Slug) || !strings.Contains(out, "404") {
		t.Fatalf("the report must name the task AND the ref — either alone is unfixable, got:\n%s", out)
	}
}

// TestDoctorFlagsBrokenCalibrationSpan: a done task that was claimed but never
// stamped "completed by" can never produce a calibration sample, so the
// estimator silently sizes future work from a shrinking population.
func TestDoctorFlagsBrokenCalibrationSpan(t *testing.T) {
	w, ctx := doctorEnv(t)
	tk, err := store.CreateTask(w, "a-root", "p", "Claimed but never completed", store.TaskOpts{Accept: []string{"a"}})
	if err != nil {
		t.Fatal(err)
	}
	store.AppendLog(tk, "claimed by a-worker")
	if err := store.SaveTask(tk); err != nil {
		t.Fatal(err)
	}
	if err := store.MoveTask(w, tk, model.StatusDone); err != nil {
		t.Fatal(err)
	}

	out := doctorOut(t, ctx)
	if !strings.Contains(out, "broken-calibration-span") {
		t.Fatalf("expected broken-calibration-span, got:\n%s", out)
	}
	if !strings.Contains(out, tk.Slug) {
		t.Fatalf("the report must name the task, got:\n%s", out)
	}
}

// Historical adapter declarations and probe caches cannot establish native
// session authority, and must not grade the current governance health report.
func TestDoctorDoesNotGradeHistoricalLauncherReadiness(t *testing.T) {
	for _, grant := range []model.Grant{model.GrantRO, model.GrantRW} {
		t.Run(string(grant), func(t *testing.T) {
			w, ctx := doctorEnv(t)
			rt := store.Runtime{Name: "archived", Binary: filepath.Join(t.TempDir(), "missing-provider-cli")}
			if err := store.CreateRuntime(w, "a-root", rt, "historical declaration"); err != nil {
				t.Fatal(err)
			}
			if err := store.CreateRole(w, "a-root", team.Role{Name: "reviewer", Summary: "review code", Kind: "reviewer", Scope: []string{"internal/**"}, Grant: string(grant), Runtime: rt.Name,
				OutOfScope: []string{"generated/**"}, EscalateTo: []string{"human"}, Skills: []string{"evidence"},
				Profile: team.ModelProfile{ID: "planned-model", CostTier: 1, MaxTaskPoints: 5, ContextLimit: 100000, CapabilityTags: []string{"review"}}}); err != nil {
				t.Fatal(err)
			}
			out := doctorOut(t, ctx)
			if !strings.Contains(out, "no anti-patterns detected") {
				t.Fatalf("historical launcher readiness changed current health:\n%s", out)
			}
			if strings.Contains(out, "spawn") || strings.Contains(out, "grant-runtime-mismatch") {
				t.Fatalf("obsolete launcher authority leaked into health:\n%s", out)
			}
			got, err := store.LoadRuntime(w, rt.Name)
			if err != nil || got.Binary != rt.Binary {
				t.Fatalf("historical metadata was not preserved: %#v, %v", got, err)
			}
		})
	}
}
