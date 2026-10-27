package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/mlnomadpy/dacli/internal/gitx"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

// LandingState is what we could learn about whether a task's work reached
// trunk. It is deliberately three-valued: "no branch" and "cannot tell" are
// different from "landed", and neither should be reported as success.
//
// This lives in the entity layer (not a feature slice) because both
// `accept` (features/acceptance) and `ship` (features/ship) need the exact
// same truthful check — accept to certify a close, ship to CORRECT that
// certification once its own later integrate step actually lands the
// branch accept could not yet see (dacli 329).
type LandingState int

const (
	LandingUnknown  LandingState = iota // no git, no trunk, or the check failed
	LandingNoBranch                     // the task never had a branch
	LandingLanded                       // the branch is an ancestor of trunk
	LandingUnlanded                     // the branch exists and is NOT in trunk
)

// CheckLanded asks whether the task's branch has reached trunk.
//
// This is the gap issue #382 called its most serious finding: `accept --verify
// "go build && go test"` passes whether or not the task's deliverable landed.
// On that reporter's run, PR merges had failed (DIRTY), the code never merged,
// and accept still marked the tasks done — the CLI reported done:15/21 while
// the vector-search commands it claimed to have built did not exist at all.
// A build passing proves the TREE compiles, not that this task's work is in it.
//
// The check is intentionally cheap and local: does a branch named for this
// task exist, and is it an ancestor of trunk? That answers the exact question
// a coarse verify cannot.
func CheckLanded(w *workspace.Workspace, t *Task, trunk string) (LandingState, string) {
	if observation, present := ReadTaskLanding(context.Background(), w, t, trunk); present {
		return observation.State, "native landing observation " + observation.ReceiptID
	}
	if !gitx.Available() {
		return LandingUnknown, ""
	}
	branch, shas := ResolveBranchRefs(w, t)
	if len(shas) == 0 {
		// No branch at all. Common and legitimate: work committed straight to
		// trunk, a docs-only task, or a record task. Nothing to contradict.
		return LandingNoBranch, branch
	}
	if trunk == "" {
		return LandingUnknown, branch
	}
	return LandingOfRefs(w, shas, trunk), branch
}

func configuredTaskLandingBase(w *workspace.Workspace, t *Task) string {
	if project, err := LoadProject(w, t.Project); err == nil && project.Landing.Base != "" {
		return project.Landing.Base
	}
	return "main"
}

func requireSelectedTaskLanding(w *workspace.Workspace, t *Task, base string) error {
	if observation, present := ReadTaskLanding(context.Background(), w, t, base); present && observation.State != LandingLanded {
		return Refusedf("selected native landing is %s; owner must record or reconcile exact landing evidence before closing", taskLandingStateName(observation.State))
	}
	return nil
}

// LandingOfRefs answers the landing question about EVERY commit a task's
// branch names, and is landed only when all of them are.
//
// Asking about one ref was a false LANDED, which is the failure #382 exists
// for. origin/<branch> and refs/heads/<branch> can point at different commits —
// a branch pushed once and then advanced locally is the ordinary case — and the
// resolver returned the remote one. So a task whose FIRST commit had merged
// long ago, with the deliverable sitting in later local commits that never
// merged, was certified landed: the stale sha genuinely is an ancestor of
// trunk. accept then closed it with a record saying the work was in trunk when
// it was not.
//
// Neither ref is authoritative — the --pr path lands what origin has, the
// local-merge path lands what refs/heads has — so the only sound answer is the
// conservative one. Unknown outranks Unlanded outranks Landed: a view we could
// not get must never read as a verdict, and one commit provably outside trunk
// settles the question however many others are inside it.
func LandingOfRefs(w *workspace.Workspace, shas []string, trunk string) LandingState {
	worst := LandingLanded
	for _, sha := range shas {
		switch LandingOfRef(w, sha, trunk) {
		case LandingUnknown:
			return LandingUnknown
		case LandingUnlanded:
			worst = LandingUnlanded
		}
	}
	return worst
}

// ResolveBranchRefs finds EVERY commit a task's branch currently names — the
// remote ref, the local ref, or both when they disagree — deduplicated.
//
// It returns all of them rather than picking one because picking one was
// wrong in both directions and there is no ordering that fixes it: the --pr
// path merges what origin/<branch> has, the local-merge path merges what
// refs/heads/<branch> has, and a branch pushed once then advanced locally has
// its deliverable only in the latter. Returning the remote ref alone certified
// tasks as landed whose deliverable was never merged (see LandingOfRefs).
//
// Exposed separately so a caller can snapshot the commits BEFORE an operation
// that might delete the branch (a clean local merge does exactly that once it
// lands — see vcs.mergeTask), and still answer "was THIS work merged?"
// afterward via LandingOfRefs, when the branch name alone can no longer say.
func ResolveBranchRefs(w *workspace.Workspace, t *Task) (branch string, shas []string) {
	branch = TaskBranch(t)
	if !gitx.Available() {
		return branch, nil
	}
	seen := map[string]bool{}
	for _, cand := range []string{"refs/remotes/origin/" + branch, "refs/heads/" + branch} {
		out, err := gitx.Run(w.Root, "rev-parse", "--verify", "--quiet", cand)
		if err != nil {
			continue
		}
		// The common case is both refs at the SAME commit, which is one
		// question, not two.
		if sha := strings.TrimSpace(out); sha != "" && !seen[sha] {
			seen[sha] = true
			shas = append(shas, sha)
		}
	}
	return branch, shas
}

// LandingOfRef answers whether an already-resolved commit (sha) has reached
// trunk. Separated from CheckLanded's branch lookup so a caller holding a
// commit captured before the branch was deleted can still ask the question.
func LandingOfRef(w *workspace.Workspace, sha, trunk string) LandingState {
	if !gitx.Available() || trunk == "" || sha == "" {
		return LandingUnknown
	}
	// EVERY existing trunk ref is consulted, and only a ref that answers
	// "landed" ends the search. Returning the first EXISTING ref's verdict
	// instead reads as thorough while making the second ref unreachable
	// whenever the first exists — and the first is origin/<trunk>, which is
	// stale by construction on the path that matters: `ship` merges each task
	// branch into trunk locally, records the verdict, and pushes afterward. So
	// every task shipped through the default path on a repo with a remote got a
	// permanent, committed "NOT in <trunk> — closed anyway" stamped on work
	// that had just been merged into trunk. The record is the product, so a
	// false line in it is the most expensive bug this tool has.
	//
	// The two refs disagree in both directions and neither is authoritative: a
	// pre-push tree has the merge only locally, and a fetched-but-not-merged
	// checkout has it only in origin. Present in either IS landed.
	sawTrunk := false
	for _, trunkRef := range []string{"refs/remotes/origin/" + trunk, "refs/heads/" + trunk} {
		if _, err := gitx.Run(w.Root, "rev-parse", "--verify", "--quiet", trunkRef); err != nil {
			continue
		}
		sawTrunk = true
		in, err := gitx.IsAncestor(w.Root, sha, trunkRef)
		if err != nil {
			// A failed query is not evidence of absence. Reporting "unlanded"
			// here would refuse work that may well have landed, and reporting
			// "landed" would certify a close from a view we never got.
			return LandingUnknown
		}
		if in {
			return LandingLanded
		}
	}
	if sawTrunk {
		return LandingUnlanded
	}
	return LandingUnknown
}

// LandingEvidence renders the one Log line recorded on the task, so the
// trajectory states what was known about the deliverable at close time rather
// than implying more than was checked.
// The TARGET is named, never called "trunk" generically. During a sprint the
// work lands on sprint/N and takes one pull request to main at the end, so a
// record saying "merged into trunk" would be false about where it actually
// went — and the record is the product (dacli 342).
func LandingEvidence(st LandingState, branch, target string) string {
	if target == "" {
		target = "trunk"
	}
	switch st {
	case LandingLanded:
		return fmt.Sprintf("deliverable: %s is merged into %s", branch, target)
	case LandingUnlanded:
		return fmt.Sprintf("deliverable: %s exists but is NOT in %s — closed anyway", branch, target)
	case LandingNoBranch:
		return fmt.Sprintf("deliverable: no %s branch — nothing to check against %s", branch, target)
	default:
		return fmt.Sprintf("deliverable: could not be checked against %s", target)
	}
}

// TrunkBranch resolves the branch a task's work is expected to land on. It
// prefers the repo's actual HEAD-of-origin, falling back to the conventional
// names, so a repo using `master` or a renamed default is not silently
// reported as "cannot tell".
func TrunkBranch(w *workspace.Workspace) string {
	if out, err := gitx.Run(w.Root, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
		if s := strings.TrimSpace(out); s != "" {
			return strings.TrimPrefix(s, "origin/")
		}
	}
	for _, cand := range []string{"main", "master"} {
		if gitx.BranchExists(w.Root, cand) {
			return cand
		}
	}
	return ""
}
