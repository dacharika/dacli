package vcs

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

func pruneFixture(t *testing.T, merged bool) (string, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	unsetAgentEnv(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	gitAt(t, root, "init", "-q", "-b", "main")
	gitAt(t, root, "config", "user.email", "test@example.com")
	gitAt(t, root, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(root, "base"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitAt(t, root, "add", "base")
	gitAt(t, root, "commit", "-qm", "base")
	w, err := workspace.Init(root, "prune")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.CreateProject(w, "a-root", "P", "p", "g", ""); err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(w, "a-root", "p", "Prune fixture", store.TaskOpts{Accept: []string{"proof"}})
	if err != nil {
		t.Fatal(err)
	}
	wt := w.WorktreePath(task.Project, task.Seq, task.Slug)
	if err := os.MkdirAll(filepath.Dir(wt), 0o755); err != nil {
		t.Fatal(err)
	}
	branch := store.TaskBranch(task)
	gitAt(t, root, "worktree", "add", "-q", "-b", branch, wt, "main")
	if err := os.WriteFile(filepath.Join(wt, "feature"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitAt(t, wt, "add", "feature")
	gitAt(t, wt, "commit", "-qm", "feature")
	if merged {
		gitAt(t, root, "merge", "--no-ff", "-qm", "land", branch)
	} else {
		store.CheckAllAcceptance(task)
		if err := store.SaveTask(task); err != nil {
			t.Fatal(err)
		}
		if err := store.CloseTask(w, task, "a-root"); err != nil {
			t.Fatal(err)
		}
	}
	return root, wt
}

func assertPruneRemoval(t *testing.T, root, wt string) {
	t.Helper()
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatalf("pruned directory remains: %v", err)
	}
	if got := gitAt(t, root, "worktree", "list", "--porcelain"); strings.Contains(got, wt) {
		t.Fatalf("pruned registration remains:\n%s", got)
	}
}

func TestWorktreePrunePreviewApplyExactRemoval(t *testing.T) {
	for _, merged := range []bool{true, false} {
		name := "finished"
		if merged {
			name = "merged"
		}
		t.Run(name, func(t *testing.T) {
			root, wt := pruneFixture(t, merged)
			ctx, out := prCtx(root)
			if err := cmdWorktreePrune(ctx, []string{"--dry-run"}); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), wt) || !strings.Contains(out.String(), "1 worktree(s) reclaimable") {
				t.Fatalf("preview count/path disagrees:\n%s", out.String())
			}
			if _, err := os.Stat(wt); err != nil {
				t.Fatalf("preview removed directory: %v", err)
			}
			if got := gitAt(t, root, "worktree", "list", "--porcelain"); !strings.Contains(got, wt) {
				t.Fatalf("preview removed registration:\n%s", got)
			}
			out.Reset()
			if err := cmdWorktreePrune(ctx, nil); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "reclaimed 1 worktree(s)") {
				t.Fatalf("apply disagrees with preview:\n%s", out.String())
			}
			assertPruneRemoval(t, root, wt)
		})
	}
}

// A real locked checkout was previewed as reclaimable, then silently skipped
// by apply. Both modes must preserve it instead of promising impossible work.
func TestWorktreePrunePreservesLockedCheckout(t *testing.T) {
	for _, merged := range []bool{true, false} {
		name := "finished"
		if merged {
			name = "merged"
		}
		t.Run(name, func(t *testing.T) {
			root, wt := pruneFixture(t, merged)
			gitAt(t, root, "worktree", "lock", wt)
			ctx, out := prCtx(root)
			if err := cmdWorktreePrune(ctx, []string{"--dry-run"}); err != nil {
				t.Fatal(err)
			}
			preview := out.String()
			out.Reset()
			applyErr := cmdWorktreePrune(ctx, nil)
			if strings.Contains(preview, wt) || !strings.Contains(preview, "0 worktree(s) reclaimable") {
				t.Fatalf("locked checkout reported reclaimable: preview=%q apply=%q err=%v", preview, out.String(), applyErr)
			}
			if applyErr != nil {
				t.Fatal(applyErr)
			}
			if !strings.Contains(out.String(), "reclaimed 0 worktree(s)") {
				t.Fatalf("locked checkout counted as removed:\n%s", out.String())
			}
			if _, err := os.Stat(wt); err != nil {
				t.Fatalf("locked directory removed: %v", err)
			}
			if got := gitAt(t, root, "worktree", "list", "--porcelain"); !strings.Contains(got, wt) {
				t.Fatalf("locked registration removed:\n%s", got)
			}
		})
	}
}

// Intercept only worktree removal; all classification and registration probes
// still use real Git against an isolated repository (core/439, issue #647).
func pruneGitRemoval(t *testing.T, body string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX Git failure fixture")
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	wrapper := t.TempDir()
	script := "#!/bin/sh\nif [ \"$1\" = worktree ] && [ \"$2\" = remove ]; then\n" + body + "\nfi\nexec '" + strings.ReplaceAll(realGit, "'", "'\\''") + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(wrapper, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", wrapper+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestWorktreePruneFalseSuccessMatchesPreview(t *testing.T) {
	for _, merged := range []bool{true, false} {
		name := "finished"
		if merged {
			name = "merged"
		}
		t.Run(name, func(t *testing.T) {
			root, wt := pruneFixture(t, merged)
			ctx, out := prCtx(root)
			if err := cmdWorktreePrune(ctx, []string{"--dry-run"}); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), wt) || !strings.Contains(out.String(), "1 worktree(s) reclaimable") {
				t.Fatalf("false-success preview count/path disagrees:\n%s", out.String())
			}
			pruneGitRemoval(t, "rm -rf -- \"$4\"\nexit 0")
			out.Reset()
			if err := cmdWorktreePrune(ctx, nil); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "reclaimed 1 worktree(s)") {
				t.Fatalf("false-success apply disagrees with preview:\n%s", out.String())
			}
			assertPruneRemoval(t, root, wt)
		})
	}
}

func TestWorktreePruneReportsRemovalFailure(t *testing.T) {
	root, wt := pruneFixture(t, true)
	ctx, out := prCtx(root)
	if err := cmdWorktreePrune(ctx, []string{"--dry-run"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), wt) || !strings.Contains(out.String(), "1 worktree(s) reclaimable") {
		t.Fatalf("failure fixture not reclaimable:\n%s", out.String())
	}
	pruneGitRemoval(t, "echo 'fixture removal failure' >&2\nexit 1")
	out.Reset()
	err := cmdWorktreePrune(ctx, nil)
	if err == nil || clikit.ExitCode(err) != 1 || !strings.Contains(err.Error(), wt) || !strings.Contains(err.Error(), "inspect") {
		t.Fatalf("removal failure silently dropped or misclassified: output=%q err=%v", out.String(), err)
	}
	if !strings.Contains(out.String(), "reclaimed 0 worktree(s)") {
		t.Fatalf("failure lost actual removal count:\n%s", out.String())
	}
	if _, err := os.Stat(wt); err != nil {
		t.Fatalf("failed removal deleted directory: %v", err)
	}
	if got := gitAt(t, root, "worktree", "list", "--porcelain"); !strings.Contains(got, wt) {
		t.Fatalf("failed removal deleted registration:\n%s", got)
	}
}

func TestWorktreePruneContinuesAfterRemovalFailure(t *testing.T) {
	root, failed := pruneFixture(t, true)
	w, err := workspace.Find(root)
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(w, "a-root", "p", "Second finished", store.TaskOpts{Accept: []string{"proof"}})
	if err != nil {
		t.Fatal(err)
	}
	wt := w.WorktreePath(task.Project, task.Seq, task.Slug)
	gitAt(t, root, "worktree", "add", "-q", "-b", store.TaskBranch(task), wt, "main")
	store.CheckAllAcceptance(task)
	if err := store.SaveTask(task); err != nil {
		t.Fatal(err)
	}
	if err := store.CloseTask(w, task, "a-root"); err != nil {
		t.Fatal(err)
	}
	ctx, out := prCtx(root)
	if err := cmdWorktreePrune(ctx, []string{"--dry-run"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "2 worktree(s) reclaimable") {
		t.Fatalf("partial fixture preview not exact:\n%s", out.String())
	}
	pruneGitRemoval(t, "if [ \"$4\" = '"+strings.ReplaceAll(failed, "'", "'\\''")+"' ]; then\necho 'fixture failure' >&2\nexit 1\nfi")
	out.Reset()
	err = cmdWorktreePrune(ctx, nil)
	if err == nil || clikit.ExitCode(err) != 1 || !strings.Contains(err.Error(), failed) {
		t.Fatalf("partial failure not reported: %v", err)
	}
	if !strings.Contains(out.String(), "pruned "+wt) || !strings.Contains(out.String(), "reclaimed 1 worktree(s)") {
		t.Fatalf("partial success lost actual removal count:\n%s", out.String())
	}
	assertPruneRemoval(t, root, wt)
	if _, err := os.Stat(failed); err != nil {
		t.Fatalf("failed directory removed: %v", err)
	}
	if got := gitAt(t, root, "worktree", "list", "--porcelain"); !strings.Contains(got, failed) {
		t.Fatalf("failed registration removed:\n%s", got)
	}
}

func TestWorktreePruneInterruptedFinishedMatchesPreview(t *testing.T) {
	root, wt := pruneFixture(t, false)
	if err := os.Remove(filepath.Join(wt, ".git")); err != nil {
		t.Fatal(err)
	}
	if got := gitAt(t, root, "worktree", "list", "--porcelain"); !strings.Contains(got, wt) {
		t.Fatalf("interrupted fixture lacks registration:\n%s", got)
	}
	ctx, out := prCtx(root)
	if err := cmdWorktreePrune(ctx, []string{"--dry-run"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), wt) || !strings.Contains(out.String(), "1 worktree(s) reclaimable") {
		t.Fatalf("interrupted preview count/path disagrees:\n%s", out.String())
	}
	out.Reset()
	if err := cmdWorktreePrune(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "reclaimed 1 worktree(s)") {
		t.Fatalf("interrupted apply disagrees with preview:\n%s", out.String())
	}
	assertPruneRemoval(t, root, wt)
}
