// A worktree created for a NEW branch is cut from HEAD and starts current. One
// created for an EXISTING branch is checked out at whatever commit that branch
// was left at, and nothing advanced it.
//
// That is invisible for a one-shot task, and compounding for a recurring one.
// The standing continuous-improvement anchor keeps its branch across every
// cycle, so an auditor was handed a tree arbitrarily far behind trunk and asked
// to find bugs in code where the fixes had not been applied — reported at 34
// commits behind, re-reporting seven already-closed defects (issue #441). Those
// phantom findings then spawn fixer agents that rebuild work that exists.
package gitx

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func wtGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	c := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func wtCommit(t *testing.T, dir, name string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644); err != nil {
		t.Fatal(err)
	}
	wtGit(t, dir, "add", "-A")
	wtGit(t, dir, "commit", "-q", "-m", name)
	return revParse(dir, "HEAD")
}

// staleBranchRepo: a recurring task's branch left behind while trunk moved on.
func staleBranchRepo(t *testing.T) (root, branch, trunkTip string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root = t.TempDir()
	wtGit(t, root, "init", "-q")
	wtGit(t, root, "config", "user.email", "x@x")
	wtGit(t, root, "config", "user.name", "x")
	wtGit(t, root, "checkout", "-q", "-b", "main")
	wtCommit(t, root, "base.txt")

	branch = "dacli/006-continuous-improvement"
	wtGit(t, root, "branch", branch) // the recurring branch, at base
	wtCommit(t, root, "fix-one.txt") // trunk moves on without it
	trunkTip = wtCommit(t, root, "fix-two.txt")
	return root, branch, trunkTip
}

// TestAddWorktreeFastForwardsAStaleRecurringBranch is the regression.
func TestAddWorktreeFastForwardsAStaleRecurringBranch(t *testing.T) {
	root, branch, trunkTip := staleBranchRepo(t)
	path := filepath.Join(t.TempDir(), "wt")

	freshened, err := AddWorktree(root, path, branch, "main")
	if err != nil {
		t.Fatal(err)
	}
	if !freshened {
		t.Fatal("a branch two commits behind trunk with no work of its own must be fast-forwarded")
	}
	if got := revParse(path, "HEAD"); got != trunkTip {
		t.Fatalf("the worktree is still behind trunk: HEAD %s, trunk %s", got, trunkTip)
	}
	// The point of the fix: the agent can actually SEE the fixes that landed.
	if _, err := os.Stat(filepath.Join(path, "fix-two.txt")); err != nil {
		t.Fatalf("the agent's tree is missing work that is in trunk: %v", err)
	}
}

// TestAddWorktreeLeavesADivergedBranchAlone: a branch with real unmerged
// commits must come through untouched — rewriting or auto-merging an agent's
// work at spawn time would be a far worse bug than the staleness this fixes.
//
// Note what actually enforces it. Deleting the explicit IsAncestor pre-filter
// does NOT make this test fail, because `merge --ff-only` refuses a diverged
// branch on its own — verified by mutation. The safety rests on git, not on a
// check we could forget, and this comment exists so nobody later "restores"
// the pre-filter believing it was the protection.
func TestAddWorktreeLeavesADivergedBranchAlone(t *testing.T) {
	root, branch, trunkTip := staleBranchRepo(t)

	// Give the branch a commit of its own, so it is no longer an ancestor.
	wtGit(t, root, "checkout", "-q", branch)
	own := wtCommit(t, root, "agents-own-work.txt")
	wtGit(t, root, "checkout", "-q", "main")

	path := filepath.Join(t.TempDir(), "wt")
	freshened, err := AddWorktree(root, path, branch, "main")
	if err != nil {
		t.Fatal(err)
	}
	if freshened {
		t.Fatal("a branch carrying its own commits must never be silently fast-forwarded")
	}
	if got := revParse(path, "HEAD"); got != own {
		t.Fatalf("the branch tip moved: HEAD %s, want the branch's own commit %s", got, own)
	}
	if got := revParse(path, "HEAD"); got == trunkTip {
		t.Fatal("the branch was reset onto trunk, destroying its unmerged work")
	}
	if _, err := os.Stat(filepath.Join(path, "agents-own-work.txt")); err != nil {
		t.Fatalf("the agent's unmerged work is gone: %v", err)
	}
}

// TestAddWorktreeReportsNoFresheningWhenAlreadyCurrent: a branch already at
// trunk's tip is trivially an ancestor and `merge --ff-only` succeeds there as
// a no-op, so reporting on the command's exit status would announce a
// freshening that never happened — and every spawn would print it.
func TestAddWorktreeReportsNoFresheningWhenAlreadyCurrent(t *testing.T) {
	root, branch, _ := staleBranchRepo(t)
	wtGit(t, root, "branch", "-f", branch, "main") // already current

	freshened, err := AddWorktree(root, filepath.Join(t.TempDir(), "wt"), branch, "main")
	if err != nil {
		t.Fatal(err)
	}
	if freshened {
		t.Fatal("a branch already at trunk must not be reported as freshened")
	}
}

// TestAddWorktreeOnANewBranchIsNeverFreshened: it is cut from the resolved
// trunk ref, so it starts current and there is nothing to report.
func TestAddWorktreeOnANewBranchIsNeverFreshened(t *testing.T) {
	root, _, trunkTip := staleBranchRepo(t)
	path := filepath.Join(t.TempDir(), "wt")

	freshened, err := AddWorktree(root, path, "dacli/007-brand-new", "main")
	if err != nil {
		t.Fatal(err)
	}
	if freshened {
		t.Fatal("a newly created branch cannot have been behind anything")
	}
	if got := revParse(path, "HEAD"); got != trunkTip {
		t.Fatalf("a new branch must start at HEAD: got %s, want %s", got, trunkTip)
	}
}

func TestAddWorktreeFromIgnoresOperatorFeatureHead(t *testing.T) {
	root, _, base := staleBranchRepo(t)
	wtGit(t, root, "checkout", "-q", "-b", "operator-feature")
	feature := wtCommit(t, root, "unrelated-feature.txt")
	if feature == base {
		t.Fatal("fixture feature did not advance")
	}

	path := filepath.Join(t.TempDir(), "wt")
	if _, err := AddWorktreeFrom(root, path, "dacli/008-exact-base", "refs/heads/main"); err != nil {
		t.Fatal(err)
	}
	if got := revParse(path, "HEAD"); got != base {
		t.Fatalf("task branch started at operator HEAD %s instead of base %s: got %s", feature, base, got)
	}
	if _, err := os.Stat(filepath.Join(path, "unrelated-feature.txt")); !os.IsNotExist(err) {
		t.Fatalf("task worktree inherited unrelated feature content: %v", err)
	}
}

// TestAddWorktreeWithoutATrunkStillWorks: a repo with no trunk (and most unit
// tests) must behave exactly as before rather than failing the spawn.
func TestAddWorktreeWithoutATrunkStillWorks(t *testing.T) {
	root, branch, _ := staleBranchRepo(t)
	path := filepath.Join(t.TempDir(), "wt")

	freshened, err := AddWorktree(root, path, branch, "")
	if err != nil {
		t.Fatalf("an empty trunk must not fail the spawn: %v", err)
	}
	if freshened {
		t.Fatal("with no trunk there is nothing to freshen to")
	}
	if strings.TrimSpace(revParse(path, "HEAD")) == "" {
		t.Fatal("the worktree is unusable")
	}
}

func TestAddWorktreeFromReusesRegisteredCheckoutAtExactBase(t *testing.T) {
	root, branch, base := staleBranchRepo(t)
	path := filepath.Join(t.TempDir(), "wt")
	wtGit(t, root, "worktree", "add", path, branch)
	wtGit(t, root, "checkout", "-q", "-b", "operator-feature")
	wtCommit(t, root, "operator-only.txt")
	freshened, err := AddWorktreeFrom(root, path, branch, base)
	if err != nil || !freshened || revParse(path, "HEAD") != base {
		t.Fatalf("registered ancestor not refreshed to exact base: freshened=%t err=%v HEAD=%s want=%s", freshened, err, revParse(path, "HEAD"), base)
	}
	if _, err := os.Stat(filepath.Join(path, "operator-only.txt")); !os.IsNotExist(err) {
		t.Fatalf("reused tree inherited operator HEAD: %v", err)
	}
	if freshened, err := AddWorktreeFrom(root, path, branch, base); err != nil || freshened {
		t.Fatalf("current reuse must succeed without freshening: %t %v", freshened, err)
	}
}

func TestAddWorktreeFromPreservesDirtyAndDivergentRegisteredCheckout(t *testing.T) {
	for _, state := range []string{"tracked", "untracked", "divergent"} {
		t.Run(state, func(t *testing.T) {
			root, branch, base := staleBranchRepo(t)
			path := filepath.Join(t.TempDir(), "wt")
			wtGit(t, root, "worktree", "add", path, branch)
			name := "agent-work.txt"
			if state == "tracked" {
				name = "base.txt"
			}
			if err := os.WriteFile(filepath.Join(path, name), []byte("agent work"), 0o644); err != nil {
				t.Fatal(err)
			}
			if state == "divergent" {
				wtGit(t, path, "add", name)
				wtGit(t, path, "commit", "-qm", "agent work")
			}
			before := revParse(path, "HEAD")
			if freshened, err := AddWorktreeFrom(root, path, branch, base); err != nil || freshened || revParse(path, "HEAD") != before {
				t.Fatalf("%s tree changed or reuse failed: freshened=%t err=%v", state, freshened, err)
			}
			if data, err := os.ReadFile(filepath.Join(path, name)); err != nil || string(data) != "agent work" {
				t.Fatalf("agent data changed: %q %v", data, err)
			}
		})
	}
}

func TestAddWorktreeFromRefusesUnrelatedExistingPaths(t *testing.T) {
	for _, state := range []string{"unregistered", "foreign", "wrong-branch", "branch-elsewhere", "replaced-registration"} {
		t.Run(state, func(t *testing.T) {
			root, branch, base := staleBranchRepo(t)
			path := filepath.Join(t.TempDir(), "wt")
			switch state {
			case "wrong-branch":
				wtGit(t, root, "worktree", "add", "-b", "other", path, "main")
			case "branch-elsewhere":
				wtGit(t, root, "worktree", "add", filepath.Join(t.TempDir(), "other"), branch)
			case "replaced-registration":
				wtGit(t, root, "worktree", "add", path, branch)
				if err := os.Remove(filepath.Join(path, ".git")); err != nil {
					t.Fatal(err)
				}
				wtGit(t, path, "init", "-q")
				wtGit(t, path, "symbolic-ref", "HEAD", "refs/heads/"+branch)
			case "foreign":
				wtGit(t, root, "init", "-q", path)
			case "unregistered":
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			before := revParse(root, branch)
			if _, err := AddWorktreeFrom(root, path, branch, base); err == nil {
				t.Fatalf("accepted %s path as task checkout", state)
			}
			if after := revParse(root, branch); after != before {
				t.Fatalf("rejected reuse moved branch: %s -> %s", before, after)
			}
		})
	}
}
