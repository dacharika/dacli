package workspace_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/mlnomadpy/dacli/internal/workspace"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func realpath(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p) // macOS /var -> /private/var
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// A dacli command run from inside a LINKED git worktree must resolve to the
// MAIN worktree's .dacli, not the worktree's stale tracked copy — otherwise a
// spawned agent gets a shadow workspace and can't see its own freshly-minted
// identity or an uncommitted task (self-commit attribution + `task check`
// break). This is the fix for the worktree-shadowing class (issue #1 / #5).
func TestFindRedirectsFromLinkedWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	git(t, root, "init", "-q")
	git(t, root, "config", "user.email", "t@t")
	git(t, root, "config", "user.name", "t")
	if _, err := workspace.Init(root, "x"); err != nil {
		t.Fatal(err)
	}
	git(t, root, "add", "-A")
	git(t, root, "commit", "-qm", "init")

	wt := filepath.Join(root, ".dacli", "worktrees", "w1")
	git(t, root, "worktree", "add", "-q", wt, "-b", "feat", "HEAD")

	// From the main root: resolves to itself, no redirect.
	if w, err := workspace.Find(root); err != nil {
		t.Fatalf("Find(root): %v", err)
	} else if realpath(t, w.Root) != realpath(t, root) {
		t.Fatalf("Find(root).Root = %s; want %s", w.Root, root)
	}

	// From inside the linked worktree: redirects to the SHARED root.
	w, err := workspace.Find(wt)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := realpath(t, w.Root), realpath(t, root); got != want {
		t.Fatalf("Find(worktree).Root = %s; want shared root %s (shadow not redirected)", got, want)
	}
}

// The redirect from a worktree to its shared root must be DETERMINISTIC and not
// hinge on a git subprocess: dacli places every worktree at
// <root>/.dacli/worktrees/<name>, so the shared root is derivable from the path
// alone. Before this, resolution leaned solely on `git rev-parse
// --path-format=absolute`, which silently returns "" on an old git or a sandbox
// that withholds git — dropping the agent onto the stale worktree snapshot and
// surfacing as a cryptic "agent token not recognized" (task 296). Here there is
// NO git repo at all, yet a command from inside the worktree must still resolve
// to the shared root, not the shadow it physically sits in.
func TestFindRedirectsFromWorktreePathWithoutGit(t *testing.T) {
	root := t.TempDir()
	if _, err := workspace.Init(root, "shared"); err != nil {
		t.Fatal(err)
	}
	// A shadow workspace physically nested where dacli puts worktrees, with its
	// OWN distinct identity so a wrong resolution is unambiguous. No git anywhere.
	shadow := filepath.Join(root, ".dacli", "worktrees", "core-001-x")
	if _, err := workspace.Init(shadow, "shadow"); err != nil {
		t.Fatal(err)
	}
	w, err := workspace.Find(shadow)
	if err != nil {
		t.Fatal(err)
	}
	if w.Name != "shared" {
		t.Fatalf("Find(worktree) resolved the %q shadow, not the shared root — redirect is not git-free/deterministic", w.Name)
	}
	if got, want := realpath(t, w.Root), realpath(t, root); got != want {
		t.Fatalf("Find(worktree).Root = %s; want shared root %s", got, want)
	}
}

// Two tasks with the same title share a slug, so a slug-only worktree key made
// them collide onto one directory and commit onto the wrong branch (dacli 215).
// WorktreePath must disambiguate by project + seq so same-titled tasks — within
// a project (different seq) and across projects (repeated seq) — never share a
// worktree.
func TestWorktreePathDisambiguatesSameTitle(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	git(t, root, "init", "-q")
	git(t, root, "config", "user.email", "t@t")
	git(t, root, "config", "user.name", "t")
	w, err := workspace.Init(root, "x")
	if err != nil {
		t.Fatal(err)
	}

	// Same slug, same project, different seq — must not collide.
	a := w.WorktreePath("core", 1, "fix-thing")
	b := w.WorktreePath("core", 2, "fix-thing")
	if a == b {
		t.Fatalf("same-titled tasks share a worktree: %s", a)
	}

	// Same slug, same seq, different project — must not collide.
	c := w.WorktreePath("web", 1, "fix-thing")
	if a == c {
		t.Fatalf("cross-project same-seq tasks share a worktree: %s", a)
	}

	// The name carries project and seq so the layout stays greppable.
	if got, want := filepath.Base(a), "core-001-fix-thing"; got != want {
		t.Fatalf("WorktreePath base = %s; want %s", got, want)
	}
}

// A project slug is a single path segment. SafeSegment is the guard that keeps
// an explicit --slug or a forged --project from escaping projects/ — the
// path-traversal write/delete class (dacli 163).
func TestSafeSegment(t *testing.T) {
	ok := []string{"core", "my-project", "p1", "a-b-c-9"}
	for _, s := range ok {
		if !workspace.SafeSegment(s) {
			t.Errorf("SafeSegment(%q) = false; want true (well-formed slug)", s)
		}
	}
	bad := []string{"", ".", "..", "../x", "../../../../etc", "a/b", "/abs", "x/..", "..\\y", "foo/../bar"}
	for _, s := range bad {
		if workspace.SafeSegment(s) {
			t.Errorf("SafeSegment(%q) = true; want false (traversal/separator)", s)
		}
	}
}

// ProjectDir must never resolve a traversing slug to a path outside projects/.
// A well-formed slug resolves normally; anything that would escape is redirected
// to an in-workspace sentinel, so the operation fails safely inside .dacli.
func TestProjectDirContainment(t *testing.T) {
	root := t.TempDir()
	w, err := workspace.Init(root, "x")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := w.ProjectDir("core"), filepath.Join(w.ProjectsDir(), "core"); got != want {
		t.Fatalf("ProjectDir(core) = %s; want %s", got, want)
	}
	for _, evil := range []string{"../../../../escape", "..", "a/../../b"} {
		got := w.ProjectDir(evil)
		rel, err := filepath.Rel(w.ProjectsDir(), got)
		if err != nil || rel == ".." || filepath.IsAbs(rel) ||
			len(rel) >= 2 && rel[0] == '.' && rel[1] == '.' {
			t.Fatalf("ProjectDir(%q) = %s escaped projects/ (rel=%q)", evil, got, rel)
		}
	}
}

// An ignored canonical workspace is absent from an external worktree checkout.
// Discovery must share the main record without relying on a local snapshot.
func TestFindExternalLinkedWorktreeWithoutLocalWorkspace(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	parent := t.TempDir()
	root := filepath.Join(parent, "main")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, root, "init", "-q")
	git(t, root, "config", "user.email", "t@t")
	git(t, root, "config", "user.name", "t")
	canonical, err := workspace.Init(root, "canonical")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".dacli/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, root, "add", ".gitignore")
	git(t, root, "commit", "-qm", "ignore workspace record")
	wt := filepath.Join(parent, "external-worker")
	git(t, root, "worktree", "add", "-q", wt, "-b", "worker", "HEAD")
	nested := filepath.Join(wt, "src", "nested")
	if err = os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, start := range []string{wt, nested} {
		found, err := workspace.Find(start)
		if err != nil {
			t.Fatalf("Find(%s): %v", start, err)
		}
		if found.ID != canonical.ID || realpath(t, found.Root) != realpath(t, root) {
			t.Fatalf("Find(%s) = %+v; want canonical workspace %+v", start, found, canonical)
		}
	}
	if _, err = os.Stat(filepath.Join(wt, workspace.Dir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("external checkout unexpectedly contains a workspace: %v", err)
	}

	t.Run("nearest workspace precedes Git fallback", func(t *testing.T) {
		local, err := workspace.Init(nested, "local")
		if err != nil {
			t.Fatal(err)
		}
		found, err := workspace.Find(nested)
		if err != nil || found.ID != local.ID {
			t.Fatalf("nearest workspace lost precedence: %+v %v", found, err)
		}
	})
	t.Run("ancestor workspace precedes Git fallback", func(t *testing.T) {
		outer, err := workspace.Init(parent, "outer")
		if err != nil {
			t.Fatal(err)
		}
		found, err := workspace.Find(wt)
		if err != nil || found.ID != outer.ID {
			t.Fatalf("ordinary ancestor workspace lost precedence: %+v %v", found, err)
		}
		if err = os.RemoveAll(filepath.Join(parent, workspace.Dir)); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("absent canonical workspace is not created", func(t *testing.T) {
		if err := os.RemoveAll(filepath.Join(root, workspace.Dir)); err != nil {
			t.Fatal(err)
		}
		if _, err := workspace.Find(wt); !errors.Is(err, workspace.ErrNotFound) {
			t.Fatalf("Find without canonical workspace = %v; want ErrNotFound", err)
		}
		if _, err := os.Stat(filepath.Join(wt, workspace.Dir)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("discovery created an external workspace: %v", err)
		}
	})
}

// A submodule's gitdir pointer does not make the parent of its common dir a
// canonical workspace. A decoy there catches both fallback and local redirect.
func TestFindSubmoduleDoesNotBorrowCommonDirParentWorkspace(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	origin, root := t.TempDir(), t.TempDir()
	for _, dir := range []string{origin, root} {
		git(t, dir, "init", "-q")
		git(t, dir, "config", "user.email", "t@t")
		git(t, dir, "config", "user.name", "t")
		git(t, dir, "commit", "--allow-empty", "-qm", "init")
	}
	git(t, root, "-c", "protocol.file.allow=always", "submodule", "add", "-q", origin, "sub")
	sub := filepath.Join(root, "sub")
	if _, err := workspace.Init(filepath.Join(root, ".git", "modules"), "unrelated"); err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.Find(sub); !errors.Is(err, workspace.ErrNotFound) {
		t.Fatalf("submodule borrowed unrelated workspace: %v", err)
	}
	local, err := workspace.Init(sub, "submodule")
	if err != nil {
		t.Fatal(err)
	}
	found, err := workspace.Find(sub)
	if err != nil || found.ID != local.ID || realpath(t, found.Root) != realpath(t, sub) {
		t.Fatalf("submodule local workspace redirected to unrelated record: %+v %v", found, err)
	}
}
