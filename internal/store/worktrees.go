package store

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mlnomadpy/dacli/internal/gitx"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

// ReclaimableWorktree is one isolated task worktree that PruneWorktrees may
// safely remove, with the reason it is dead weight.
type ReclaimableWorktree struct {
	Path   string
	Branch string
	Reason string // "branch gone" | "merged into <trunk>" | "run finished"
	// Merged reports that the branch has landed on trunk, so its local branch
	// is dead too — PruneWorktrees deletes it, the same teardown gcBranch does
	// on a confirmed merge. A finished-but-unmerged run keeps its branch so an
	// accepted-yet-unlanded fix is never lost (see dacli 154, 157).
	Merged bool
}

// ReclaimableWorktrees reports the isolated task worktrees (those under the
// workspace's WorktreesDir) that are safe to reclaim. Without this a workspace
// accumulates one live checkout per task ever spawned with --worktree — a real
// run reached 86 worktrees / 2.2 GB (dacli 252). A worktree is returned only
// when we can PROVE it is dead, never on a failed probe:
//
//   - "branch gone" — the branch was already deleted (e.g. gcBranch removed it
//     but the worktree teardown failed): a dangling admin entry, always safe.
//   - "merged into <trunk>" — the branch is an ancestor of trunk AND entered
//     via a merge (its tip is NOT on trunk's first-parent mainline). The
//     bare-tip guard is essential: a just-spawned agent's branch carries zero
//     commits of its own and is trivially an ancestor of trunk, so ancestry
//     alone would delete a LIVE worktree mid-work (dacli 168, 241). A merged
//     worktree is reclaimed only when its tree is clean — uncommitted scratch
//     in it is unmerged work we must not force-remove.
//   - "run finished" — the task file for the branch sits in done/. The run is
//     over, so its scratch checkout is dead weight; the branch is kept.
//
// trunk is the local trunk branch (e.g. "main"); pass "" to skip the merged
// check. The workspace's own main worktree and every path in protect (e.g. the
// caller's cwd), and locked worktrees, are never returned.
func ReclaimableWorktrees(w *workspace.Workspace, trunk string, protect ...string) ([]ReclaimableWorktree, error) {
	wts, err := gitx.ListWorktrees(w.Root)
	if err != nil {
		return nil, err
	}

	// Branches whose task has landed in done/ — the run is finished.
	done := map[string]bool{}
	if tasks, err := ListTasks(w, "", model.StatusDone); err == nil {
		for _, t := range tasks {
			done[fmt.Sprintf("dacli/%03d-%s", t.Seq, t.Slug)] = true
		}
	}
	// A reopened task may intentionally reuse the exact branch whose earlier
	// generation merged. Ancestry therefore proves only the old landing, not
	// that the corrective checkout is dead (issue #679).
	reopened := map[string]bool{}
	for _, status := range []model.Status{model.StatusOpen, model.StatusActive} {
		if tasks, err := ListTasks(w, "", status); err == nil {
			for _, t := range tasks {
				if t.Generation() > 0 {
					reopened[TaskBranch(t)] = true
				}
			}
		}
	}

	skip := map[string]bool{cleanPath(w.Root): true}
	for _, p := range protect {
		if p != "" {
			skip[cleanPath(p)] = true
		}
	}
	wtDir := cleanPath(w.WorktreesDir())

	var out []ReclaimableWorktree
	for _, wt := range wts {
		p := cleanPath(wt.Path)
		if skip[p] || wt.Locked {
			continue
		}
		// Only ever touch worktrees under the workspace's own worktrees dir —
		// never an external worktree an operator added for their own reasons.
		if p != wtDir && !strings.HasPrefix(p, wtDir+string(filepath.Separator)) {
			continue
		}

		switch {
		case wt.Branch != "" && !gitx.BranchExists(w.Root, wt.Branch):
			out = append(out, ReclaimableWorktree{Path: wt.Path, Branch: wt.Branch, Reason: "branch gone"})
		case wt.Branch != "" && !reopened[wt.Branch] && mergedIntoTrunk(w.Root, wt.Branch, trunk) && gitx.IsClean(wt.Path):
			out = append(out, ReclaimableWorktree{Path: wt.Path, Branch: wt.Branch, Reason: "merged into " + trunk, Merged: true})
		case wt.Branch != "" && done[wt.Branch]:
			out = append(out, ReclaimableWorktree{Path: wt.Path, Branch: wt.Branch, Reason: "run finished"})
		}
	}
	return out, nil
}

// mergedIntoTrunk reports whether branch has landed on trunk: an ancestor of
// trunk that entered via a merge, not a bare-tipped live spawn. Any probe
// failure returns false — a worktree is reclaimed only on proof it is safe.
func mergedIntoTrunk(root, branch, trunk string) bool {
	if trunk == "" || !gitx.BranchExists(root, trunk) {
		return false
	}
	// A branch whose tip sits on trunk's first-parent mainline carries no work
	// of its own — a spawn that never committed. Trunk is trivially its own
	// ancestor, so without this guard IsAncestor would call that live spawn
	// "merged" and reclaim its worktree mid-work (dacli 168, 241).
	if bare, err := gitx.TipOnFirstParentMainline(root, branch, trunk); err != nil || bare {
		return false
	}
	anc, err := gitx.IsAncestor(root, branch, trunk)
	return err == nil && anc
}

// PruneWorktrees reclaims every ReclaimableWorktrees entry: it removes the
// worktree, and for a landed branch deletes the local branch too. A per-worktree
// failure does not stop other candidates, but is returned with its path and
// recovery action: silent drops made apply disagree with preview (core/439).
// Returns the entries actually removed even when another removal failed.
func PruneWorktrees(w *workspace.Workspace, trunk string, protect ...string) ([]ReclaimableWorktree, error) {
	cand, err := ReclaimableWorktrees(w, trunk, protect...)
	if err != nil {
		return nil, err
	}
	var removed []ReclaimableWorktree
	var failures []error
	for _, c := range cand {
		if err := gitx.RemoveWorktree(w.Root, c.Path); err != nil {
			failures = append(failures, fmt.Errorf("prune %s: %w; inspect the worktree and Git registration, resolve the removal failure, then preview again", c.Path, err))
			continue
		}
		if c.Merged && c.Branch != "" {
			_, _ = gitx.Run(w.Root, "branch", "-D", c.Branch)
		}
		removed = append(removed, c)
	}
	return removed, errors.Join(failures...)
}

// cleanPath canonicalizes a path for identity comparison, resolving symlinks so
// the main root (which macOS may hand back as /var/... ) and a `git worktree
// list` entry (/private/var/...) compare equal. Falls back to a lexical clean
// when the path cannot be resolved.
func cleanPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}
