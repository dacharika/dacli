// Package acceptance is the supervisor-native close: `dacli accept` verifies an
// agent's completion and closes the task in one owner-set-policy step, replacing
// the per-spawn manual `task check --all` + `task done` ritual.
//
// It is a feature slice (ARCHITECTURE § 2b) and imports NO other slice — the
// close is expressed purely over store primitives (CheckAllAcceptance, SaveTask,
// MoveTask) and the event log, so the arch_test's isolation rule holds.
//
// Two paths, chosen by grant exactly as `task done` does:
//
//   - The OWNER (rw) runs `dacli accept <ref> [--verify "cmd"]`: an optional
//     verification command gates the close (a non-zero exit REFUSES the accept,
//     exit 1 — never close a task whose checks fail), then every acceptance box
//     is checked and the task moves to done. `--all` accepts, in one pass, every
//     task an agent has proposed for acceptance.
//   - A read-only AGENT runs the same command and, unable to rewrite the task,
//     records a box-check PROPOSAL as an event. The owner's accept applies it —
//     the child proposes, the owner decides.
package acceptance

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/mlnomadpy/dacli/internal/agentid"
	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/eventlog"
	"github.com/mlnomadpy/dacli/internal/gitx"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

const acceptUsage = "dacli accept <ref> [--verify \"cmd\"] [--full-output] [--final-commit SHA --final-tree SHA] [--require-verify] [--require-independent] [--allow-unverified] [--allow-unlanded] [--allow-unobservable-check-policy] [--defer-landing] [--force] [--into BRANCH] | dacli accept --all [--verify \"cmd\"] [--full-output] [--final-commit SHA --final-tree SHA] [--require-verify] [--require-independent] [--allow-unverified] [--allow-unlanded] [--allow-unobservable-check-policy] [--defer-landing] [--force] [--into BRANCH]"
const acceptProposeUsage = "dacli accept propose <task> --verify \"cmd\" [--full-output]"
const acceptApplyUsage = "dacli accept apply <task> --proposal <id> [--allow-unlanded | --defer-landing] [--into BRANCH]"

// Commands is this slice's table, aggregated by the app layer (cli.go).
var Commands = []clikit.Command{
	{Path: "accept apply-native", Brief: "Explicit native owner consumes independently reviewed exact landed evidence and closes the canonical task", JSON: true, Mutates: true, Usage: nativeApplyUsage, Run: cmdAcceptApplyNative},
	{Path: "accept", Brief: "Workspace owner validates a worker proposal against fixed acceptance and reviewed-head evidence, then closes it; this does not merge the branch", JSON: true, Usage: acceptUsage, Run: cmdAccept},
	{Path: "accept propose", Brief: "Independent read-only reviewer records a verified, exact-tree acceptance handoff", JSON: true, Usage: acceptProposeUsage, Run: cmdAcceptPropose},
	{Path: "accept apply", Brief: "Task owner atomically validates and applies one independent acceptance handoff", JSON: true, Mutates: true, Usage: acceptApplyUsage, Run: cmdAcceptApply},
}

type acceptanceProposalResult struct {
	Schema     string                   `json:"schema"`
	Proposal   store.AcceptanceProposal `json:"proposal"`
	NextAction string                   `json:"next_action"`
	Applied    bool                     `json:"applied"`
	Duplicate  bool                     `json:"duplicate,omitempty"`
	Task       *acceptedTaskResult      `json:"task,omitempty"`
}

func cmdAcceptPropose(ctx *clikit.Ctx, args []string) error {
	w, id, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	f, _ := clikit.ParseFlags(args)
	if err := f.Reject("verify", "full-output"); err != nil {
		return err
	}
	if len(f.Pos) != 1 || f.Get("verify") == "" {
		return clikit.Usagef("usage: %s", acceptProposeUsage)
	}
	if id.Grant != model.GrantRO {
		return clikit.Refusedf("accept propose requires an independent read-only reviewer identity; spawn a reviewer with grant ro")
	}
	t, err := store.FindTask(w, f.Pos[0])
	if err != nil {
		return err
	}
	if !store.HasAcceptanceCriteria(t) {
		return emptyAcceptanceRefusal(t.Seq)
	}
	evidence, err := runVerify(ctx, w, id.ID, f.Get("verify"), f.Bool("full-output"))
	if err != nil {
		return fmt.Errorf("verification failed — acceptance NOT proposed for task %03d: %w", t.Seq, err)
	}
	proposal, err := store.NewAcceptanceProposal(w, t, id, evidence, time.Now())
	if err != nil {
		return clikit.Refusedf("cannot create independent acceptance proposal: %v", err)
	}
	if err := store.WriteAcceptanceProposal(w, proposal); err != nil {
		return err
	}
	next := fmt.Sprintf("dacli accept apply %s --proposal %s", t.ID, proposal.ID)
	result := acceptanceProposalResult{Schema: "acceptance-proposal-result/v1", Proposal: proposal, NextAction: next}
	ctx.Result = result
	if ctx.JSON {
		return clikit.EmitJSON(ctx, result)
	}
	fmt.Fprintf(ctx.Stdout, "acceptance proposed: %s by %s (%s/%s)\nevidence: %s · commit %s · tree %s\nnext: %s\n", proposal.ID, proposal.Proposer, proposal.Runtime, proposal.Grant, proposal.EvidenceDigest, proposal.CommitSHA, proposal.TreeSHA, next)
	return nil
}

func cmdAcceptApply(ctx *clikit.Ctx, args []string) error {
	w, id, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	f, _ := clikit.ParseFlags(args)
	if err := f.Reject("proposal", "allow-unlanded", "defer-landing", "into"); err != nil {
		return err
	}
	if len(f.Pos) != 1 || f.Get("proposal") == "" {
		return clikit.Usagef("usage: %s", acceptApplyUsage)
	}
	if f.Bool("allow-unlanded") && f.Bool("defer-landing") {
		return clikit.Usagef("--allow-unlanded and --defer-landing are alternatives")
	}
	t, err := store.FindTask(w, f.Pos[0])
	if err != nil {
		return err
	}
	if !id.CanMutate(t.Owner()) {
		return clikit.Refusedf("only task owner %s can apply acceptance proposal %s (%s)", clikit.OrDash(t.Owner()), f.Get("proposal"), id.MutateRefusal())
	}
	p, err := store.ReadAcceptanceProposal(w, t.Project, t.ID, f.Get("proposal"))
	if err != nil {
		return clikit.Refusedf("cannot load acceptance proposal %s for task %s: %v", f.Get("proposal"), t.ID, err)
	}
	result, err := applyAcceptanceProposal(ctx, w, id, t, p, f.Bool("allow-unlanded"), f.Bool("defer-landing"), f.Get("into"))
	if err != nil {
		return err
	}
	ctx.Result = result
	if ctx.JSON {
		return clikit.EmitJSON(ctx, result)
	}
	if result.Duplicate {
		fmt.Fprintf(ctx.Stdout, "acceptance proposal %s was already applied; receipt repaired if needed\n", p.ID)
		return nil
	}
	writeAcceptedTask(ctx.Stdout, *result.Task)
	fmt.Fprintf(ctx.Stdout, "applied independent proposal %s from %s; evidence %s\n", p.ID, p.Proposer, p.EvidenceDigest)
	return nil
}

func applyAcceptanceProposal(ctx *clikit.Ctx, w *workspace.Workspace, id *agentid.Identity, t *store.Task, p store.AcceptanceProposal, allowUnlanded, deferLanding bool, into string) (acceptanceProposalResult, error) {
	result := acceptanceProposalResult{Schema: "acceptance-application-result/v1", Proposal: p}
	marker := "applied acceptance proposal " + p.ID
	if strings.Contains(acceptanceTaskLog(t), marker) {
		if p.State != "applied" {
			if err := store.MarkAcceptanceProposalApplied(w, p, id.ID, time.Now()); err != nil {
				return result, err
			}
		}
		result.Applied, result.Duplicate = true, true
		return result, nil
	}
	if p.State != "pending" {
		return result, clikit.Refusedf("acceptance proposal %s is already %s", p.ID, p.State)
	}
	if p.Proposer == id.ID || p.Proposer == store.ClaimedBy(t) || p.Proposer == t.Owner() || p.Grant != "ro" {
		return result, clikit.Refusedf("acceptance proposal %s is not independent of task owner/claimant", p.ID)
	}
	checklist, err := store.AcceptanceChecklistDigest(t)
	if err != nil {
		return result, err
	}
	if checklist != p.ChecklistDigest {
		return result, clikit.Refusedf("acceptance proposal %s is stale: acceptance checklist changed; run a new independent review", p.ID)
	}
	commit, tree, clean, err := currentAcceptanceTree(ctx.Cwd)
	if err != nil {
		return result, clikit.Refusedf("cannot observe proposal application tree: %v", err)
	}
	if !clean || commit != p.CommitSHA || tree != p.TreeSHA {
		return result, clikit.Refusedf("acceptance proposal %s is stale: proposed commit/tree %s/%s, current %s/%s (clean=%t); apply from the exact reviewed tree or request a new proposal", p.ID, p.CommitSHA, p.TreeSHA, commit, tree, clean)
	}
	landing, branch, target := landingLanded, "", ""
	if !deferLanding {
		target, err = landingTarget(w, t, into)
		if err != nil {
			return result, err
		}
		landing, branch = checkLanded(w, t, target)
		if landing == landingUnlanded && !allowUnlanded {
			return result, unlandedRefusal(t.Seq, branch, target)
		}
		if landing == landingUnknown {
			return result, unknownLandingRefusal(t.Seq, target)
		}
	}
	var newly, satisfied, total int
	if err := store.WithTask(w, t, func(fresh *store.Task) error {
		freshDigest, digestErr := store.AcceptanceChecklistDigest(fresh)
		if digestErr != nil {
			return digestErr
		}
		if freshDigest != p.ChecklistDigest {
			return clikit.Refusedf("acceptance proposal %s became stale while applying: acceptance checklist changed", p.ID)
		}
		newly = store.CheckAllAcceptance(fresh)
		satisfied, total = acceptanceSatisfaction(fresh)
		store.AppendLog(fresh, fmt.Sprintf("%s from %s (%s/%s, run %s)", marker, p.Proposer, p.Runtime, p.Grant, p.RunID))
		store.AppendLog(fresh, fmt.Sprintf("independent verification evidence %s for commit %s tree %s", p.EvidenceDigest, p.CommitSHA, p.TreeSHA))
		if err := store.AppendVerificationEvidence(fresh, p.Evidence); err != nil {
			return clikit.Refusedf("proposal verification evidence is incomplete: %v", err)
		}
		if !deferLanding {
			store.AppendLog(fresh, landingEvidence(landing, branch, target))
		}
		return store.CloseTaskAfterLandingDecision(w, fresh, id.ID)
	}); err != nil {
		return result, err
	}
	if err := store.MarkAcceptanceProposalApplied(w, p, id.ID, time.Now()); err != nil {
		return result, fmt.Errorf("task closed but proposal receipt remains pending; rerun the same apply command to repair it: %w", err)
	}
	result.Proposal.State, result.Applied = "applied", true
	taskResult := acceptedTaskResult{ID: t.ID, Seq: t.Seq, Slug: t.Slug, NewlyChecked: newly, Satisfied: satisfied, Total: total}
	result.Task = &taskResult
	return result, nil
}

func currentAcceptanceTree(dir string) (commit, tree string, clean bool, err error) {
	commit, err = gitx.Run(dir, "rev-parse", "HEAD")
	if err != nil {
		return "", "", false, err
	}
	tree, err = gitx.Run(dir, "rev-parse", "HEAD^{tree}")
	if err != nil {
		return "", "", false, err
	}
	status, err := gitx.Run(dir, "status", "--porcelain", "--untracked-files=normal")
	if err != nil {
		return "", "", false, err
	}
	return strings.TrimSpace(commit), strings.TrimSpace(tree), strings.TrimSpace(status) == "", nil
}

func acceptanceTaskLog(t *store.Task) string {
	section, ok := t.Doc.Section("Log")
	if !ok {
		return ""
	}
	return section.Content
}

// proposePrefix is the body convention that marks an EventComment as a
// box-check proposal. A comment carrying this prefix is the minimal-but-real
// event an agent emits and `accept` applies. It is NOT a finding: a proposed
// close is an intention, not a discovered fact, so it must not create a durable
// finding note. The convention is defined in eventlog (eventlog.ProposePrefix)
// because eventlog.Sync must recognize it too — Sync leaves proposals pending
// instead of consuming them as generic comments, so this consumer and Sync do
// not race on the same event.
const proposePrefix = eventlog.ProposePrefix

func cmdAccept(ctx *clikit.Ctx, args []string) error {
	w, id, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	f, _ := clikit.ParseFlags(args)
	if err := f.Reject("all", "verify", "full-output", "final-commit", "final-tree", "force", "require-verify", "require-independent", "allow-unverified", "allow-unlanded", "allow-unobservable-check-policy", "defer-landing", "into"); err != nil {
		return err
	}
	finalCommit, finalTree := f.Get("final-commit"), f.Get("final-tree")
	if (finalCommit == "") != (finalTree == "") {
		return clikit.Usagef("--final-commit and --final-tree must be passed together")
	}
	if finalCommit != "" && f.Get("verify") == "" {
		return clikit.Usagef("--final-commit/--final-tree require --verify so evidence can be compared to the reviewed landing head")
	}
	requireVerify := f.Bool("require-verify")
	requireIndependent := f.Bool("require-independent")
	allowUnlanded := f.Bool("allow-unlanded")
	allowUnverified := f.Bool("allow-unverified")
	allowPolicyUnknown := f.Bool("allow-unobservable-check-policy")
	deferLanding := f.Bool("defer-landing")
	// --into names the branch this work is being integrated INTO. Without it
	// the landing check always resolved the repository's trunk, so during a
	// sprint — where a batch lands on sprint/N and takes one PR to main at the
	// end — every accept warned that work was "NOT in trunk" while it had in
	// fact landed exactly where it was meant to. A warning that is wrong on
	// every run is one nobody reads when it is right (dacli 342).
	into := f.Get("into")
	// --defer-landing is `ship`'s escape hatch, not an operator flag: ship must
	// run accept BEFORE integrate (integrate refuses a non-done task), which
	// means the landing check below would ALWAYS see the branch as not yet in
	// trunk and durably record so on every task ship is about to land seconds
	// later (dacli 329). It says "skip the check here, a later step owns it" —
	// the opposite of --require-verify's "prove it landed before you close",
	// so the two cannot be combined.
	if deferLanding && requireVerify {
		return clikit.Usagef("--defer-landing and --require-verify conflict: --require-verify demands proof of landing before closing, but --defer-landing defers that proof to a later step")
	}

	// --all: accept every task an agent has proposed for acceptance, in one
	// pass. This is the "owner sets policy instead of hand-closing every spawn"
	// surface — the verify command, if given, now runs PER TASK (dacli 185).
	if f.Bool("all") {
		return acceptAllForTreePolicy(ctx, w, id, f.Get("verify"), f.Bool("force"), requireVerify, requireIndependent, allowUnverified, allowUnlanded, allowPolicyUnknown, deferLanding, f.Bool("full-output"), into, finalCommit, finalTree)
	}

	if len(f.Pos) == 0 {
		return clikit.Usagef("usage: %s", acceptUsage)
	}
	t, err := store.FindTask(w, f.Pos[0])
	if err != nil {
		return err
	}

	// The grant decides the path, exactly as `task done` does: a read-only
	// agent cannot rewrite the task, so it proposes the close as an event.
	if !id.CanMutate(t.Owner()) {
		// Operator override: root can reconcile a task whose owner is a spawned
		// agent that has since finished — that owner will never run `sync` again,
		// so its proposed close would sit pending forever, orphan-locking the
		// backlog. --force makes the override explicit; without it the close stays
		// a proposal for a live owner to apply, preserving peer concurrency safety.
		if id.ID == agentid.RootID && f.Bool("force") {
			prev := t.Owner()
			t.Doc.Front.Set("owner", id.ID)
			store.AppendLog(t, fmt.Sprintf("adopted by %s (owner %s orphaned)", id.ID, clikit.OrDash(prev)))
			return acceptOneForTreePolicy(ctx, w, id, t, f.Get("verify"), requireVerify, requireIndependent, allowUnverified, allowUnlanded, allowPolicyUnknown, deferLanding, f.Bool("full-output"), into, finalCommit, finalTree)
		}
		return propose(ctx, w, id, t)
	}

	return acceptOneForTreePolicy(ctx, w, id, t, f.Get("verify"), requireVerify, requireIndependent, allowUnverified, allowUnlanded, allowPolicyUnknown, deferLanding, f.Bool("full-output"), into, finalCommit, finalTree)
}

// propose records a box-check proposal as an event. The owner applies it on the
// next `dacli accept` for this task — the decision stays with the owner.
func propose(ctx *clikit.Ctx, w *workspace.Workspace, id *agentid.Identity, t *store.Task) error {
	body := fmt.Sprintf("%s %s completed; proposing all acceptance boxes checked", proposePrefix, id.ID)
	if _, err := eventlog.Append(w, id.ID, model.EventComment, t.ID, "", body); err != nil {
		return err
	}
	reason := id.MutateRefusal()
	if id.Grant == model.GrantRW {
		reason = "not the owner — root can `accept --force` to reconcile"
	}
	// The owner may have several projects with this sequence. A completion
	// message is machine guidance, so use the stable identity rather than the
	// human shorthand that triggered issue #636.
	fmt.Fprintf(ctx.Stdout, "acceptance proposed as event (%s); the owner applies it with `dacli accept %s`\n", reason, t.ID)
	return nil
}

// acceptOne runs the optional verification gate, then checks every acceptance
// box and moves the task to done. Any pending proposals for the task are
// acknowledged (marked applied) as part of the close.
func acceptOne(ctx *clikit.Ctx, w *workspace.Workspace, id *agentid.Identity, t *store.Task, verify string, requireVerify, requireIndependent, allowUnverified, allowUnlanded, deferLanding bool, into string) error {
	return acceptOneForTree(ctx, w, id, t, verify, requireVerify, requireIndependent, allowUnverified, allowUnlanded, deferLanding, into, "", "")
}

func acceptOneForTree(ctx *clikit.Ctx, w *workspace.Workspace, id *agentid.Identity, t *store.Task, verify string, requireVerify, requireIndependent, allowUnverified, allowUnlanded, deferLanding bool, into, finalCommit, finalTree string) error {
	return acceptOneForTreePolicy(ctx, w, id, t, verify, requireVerify, requireIndependent, allowUnverified, allowUnlanded, false, deferLanding, false, into, finalCommit, finalTree)
}

// acceptanceArtifact separates the task's reviewed PR head from the explicit
// post-landing artifact supplied by ship. Neither is inferred from the checkout
// running the verifier: a clean stale checkout otherwise closed work (core541).
type acceptanceArtifact struct {
	Commit string
	Tree   string
}

var observeAcceptanceArtifact = canonicalAcceptanceArtifact

func acceptanceSHA(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, c := range value {
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func acceptanceAPIArtifact(w *workspace.Workspace, repo, ref string, landing bool) (acceptanceArtifact, error) {
	endpoint := "repos/" + repo + "/git/commits/" + ref
	if landing {
		endpoint = "repos/" + repo + "/commits/" + url.PathEscape(ref)
	}
	out, err := runLandingGH(w.Root, "api", endpoint)
	if err != nil {
		return acceptanceArtifact{}, fmt.Errorf("canonical GitHub artifact is unobservable: %w", err)
	}
	var commit struct {
		SHA  string `json:"sha"`
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
		Commit struct {
			Tree struct {
				SHA string `json:"sha"`
			} `json:"tree"`
		} `json:"commit"`
	}
	if err := json.Unmarshal([]byte(out), &commit); err != nil {
		return acceptanceArtifact{}, fmt.Errorf("canonical GitHub artifact is malformed: %w", err)
	}
	tree := commit.Tree.SHA
	if landing {
		tree = commit.Commit.Tree.SHA
	}
	if !acceptanceSHA(commit.SHA) || !acceptanceSHA(tree) || (!landing && commit.SHA != ref) {
		return acceptanceArtifact{}, fmt.Errorf("canonical GitHub artifact has no exact matching commit/tree")
	}
	return acceptanceArtifact{Commit: commit.SHA, Tree: tree}, nil
}

func localAcceptanceArtifact(w *workspace.Workspace, ref string) (acceptanceArtifact, error) {
	commit, err := gitx.Run(w.Root, "--no-replace-objects", "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return acceptanceArtifact{}, err
	}
	commit = strings.TrimSpace(commit)
	tree, err := gitx.Run(w.Root, "--no-replace-objects", "rev-parse", "--verify", commit+"^{tree}")
	if err != nil {
		return acceptanceArtifact{}, err
	}
	tree = strings.TrimSpace(tree)
	if !acceptanceSHA(commit) || !acceptanceSHA(tree) {
		return acceptanceArtifact{}, fmt.Errorf("canonical local artifact has no exact commit/tree")
	}
	return acceptanceArtifact{Commit: commit, Tree: tree}, nil
}

func canonicalAcceptanceArtifact(w *workspace.Workspace, task *store.Task, into string, landing bool) (acceptanceArtifact, error) {
	project, err := store.LoadProject(w, task.Project)
	if err != nil {
		var missing store.ErrNotFound
		if !errors.As(err, &missing) {
			return acceptanceArtifact{}, err
		}
	}
	repo := ""
	if project != nil {
		repo, _ = project.Doc.Front.Get("github_repo")
		repo = strings.TrimSpace(repo)
	}
	target, err := landingTarget(w, task, into)
	if err != nil {
		if project != nil || repo != "" || landing {
			return acceptanceArtifact{}, err
		}
		target = store.TrunkBranch(w)
	}
	if landing {
		if repo != "" {
			return acceptanceAPIArtifact(w, repo, target, true)
		}
		return localAcceptanceArtifact(w, "refs/heads/"+target)
	}
	branch := store.TaskBranch(task)
	current, currentErr := localAcceptanceArtifact(w, "refs/heads/"+branch)
	recorded := store.RecordedPRURL(task)
	if repo == "" && recorded == "" {
		// Historical branchless local records have no reviewed-PR contract.
		// Preserve their clean diagnostic verification; task-check stays advisory.
		if !gitx.BranchExists(w.Root, branch) {
			return acceptanceArtifact{}, nil
		}
		return current, currentErr
	}
	type observedPR struct {
		Number int    `json:"number"`
		URL    string `json:"url"`
		State  string `json:"state"`
		Base   string `json:"baseRefName"`
		Branch string `json:"headRefName"`
		Head   string `json:"headRefOid"`
	}
	var selected observedPR
	if recorded != "" {
		out, err := runLandingGH(w.Root, "pr", "view", recorded, "--json", "number,url,state,baseRefName,headRefName,headRefOid")
		if err != nil {
			return acceptanceArtifact{}, fmt.Errorf("recorded reviewed PR is unobservable: %w", err)
		}
		if err := json.Unmarshal([]byte(out), &selected); err != nil {
			return acceptanceArtifact{}, fmt.Errorf("recorded reviewed PR is malformed: %w", err)
		}
		if selected.URL != recorded {
			return acceptanceArtifact{}, fmt.Errorf("reviewed PR does not match its canonical record")
		}
	} else {
		if currentErr != nil {
			return acceptanceArtifact{}, fmt.Errorf("current task head is unobservable; record the canonical PR or restore the task branch")
		}
		out, err := runLandingGH(w.Root, "pr", "list", "--head", branch, "--base", target, "--state", "all", "--json", "number,url,state,baseRefName,headRefName,headRefOid", "--limit", "100")
		if err != nil {
			return acceptanceArtifact{}, fmt.Errorf("reviewed task PR is unobservable: %w", err)
		}
		var prs []observedPR
		if err := json.Unmarshal([]byte(out), &prs); err != nil {
			return acceptanceArtifact{}, fmt.Errorf("reviewed task PR is malformed: %w", err)
		}
		for _, pr := range prs {
			if pr.Branch == branch && pr.Base == target && pr.Head == current.Commit {
				if pr.Number == selected.Number && selected.Number != 0 {
					return acceptanceArtifact{}, fmt.Errorf("reviewed task PR observations are ambiguous")
				}
				if pr.Number > selected.Number {
					selected = pr
				}
			}
		}
	}
	if selected.Number <= 0 || selected.Branch == "" || selected.Base != target || !acceptanceSHA(selected.Head) || (selected.State != "MERGED" && selected.State != "OPEN") {
		return acceptanceArtifact{}, fmt.Errorf("canonical reviewed PR has no usable exact task head/base/state")
	}
	prURL, err := url.Parse(selected.URL)
	if err != nil || prURL.Scheme != "https" || prURL.Host == "" {
		return acceptanceArtifact{}, fmt.Errorf("canonical reviewed PR URL is malformed")
	}
	parts := strings.Split(strings.Trim(prURL.Path, "/"), "/")
	if len(parts) != 4 || parts[2] != "pull" || parts[3] != fmt.Sprintf("%d", selected.Number) || (repo != "" && parts[0]+"/"+parts[1] != repo) {
		return acceptanceArtifact{}, fmt.Errorf("canonical reviewed PR belongs to another repository")
	}
	if selected.Branch != branch && recorded == "" {
		return acceptanceArtifact{}, fmt.Errorf("canonical reviewed PR belongs to another task branch")
	}
	if currentErr == nil && selected.Head != current.Commit {
		return acceptanceArtifact{}, fmt.Errorf("recorded PR head differs from the current canonical task branch; inspect the current generation")
	}
	artifact, err := localAcceptanceArtifact(w, selected.Head)
	if err == nil {
		return artifact, nil
	}
	if repo == "" {
		return acceptanceArtifact{}, fmt.Errorf("reviewed PR tree is unobservable locally")
	}
	return acceptanceAPIArtifact(w, repo, selected.Head, false)
}

// An explicit local unlanded close predates reviewed-PR verification. Preserve
// that escape only when neither configured GitHub nor a recorded PR defines a
// reviewed artifact; it cannot relax any remote acceptance contract.
func localUnlandedAcceptance(w *workspace.Workspace, task *store.Task, allowUnlanded bool) bool {
	if !allowUnlanded || store.RecordedPRURL(task) != "" {
		return false
	}
	project, err := store.LoadProject(w, task.Project)
	if err != nil {
		return false
	}
	repo, _ := project.Doc.Front.Get("github_repo")
	return strings.TrimSpace(repo) == ""
}

func pinAcceptanceArtifact(ctx *clikit.Ctx, w *workspace.Workspace, task *store.Task, into, finalCommit, finalTree string) (acceptanceArtifact, error) {
	expected, err := observeAcceptanceArtifact(w, task, into, finalCommit != "")
	if err != nil {
		return expected, clikit.Refusedf("task %03d canonical verification artifact is unobservable: %v; inspect the recorded PR/current task head before verifying", task.Seq, err)
	}
	if finalCommit != "" && (finalCommit != expected.Commit || finalTree != expected.Tree) {
		return expected, clikit.Refusedf("task %03d supplied final commit/tree %s/%s differs from canonical landing head %s/%s; rerun verification on the reviewed head", task.Seq, finalCommit, finalTree, expected.Commit, expected.Tree)
	}
	if expected.Commit == "" {
		return expected, nil
	}
	commit, tree, clean, err := currentAcceptanceTree(ctx.Cwd)
	if err != nil || !clean {
		return expected, clikit.Refusedf("task %03d verification working tree is dirty or unreadable; commit or discard changes before verifying", task.Seq)
	}
	if commit != expected.Commit || tree != expected.Tree {
		return expected, clikit.Refusedf("task %03d verification checkout %s/%s differs from canonical reviewed head %s/%s; rerun verification on the reviewed head", task.Seq, commit, tree, expected.Commit, expected.Tree)
	}
	return expected, nil
}

func recheckAcceptanceArtifact(w *workspace.Workspace, task *store.Task, into string, landing bool, expected acceptanceArtifact, evidence store.VerificationEvidence) error {
	fresh, err := observeAcceptanceArtifact(w, task, into, landing)
	if err != nil || fresh != expected {
		return clikit.Refusedf("task %03d canonical verification artifact changed or became unobservable during verification; before %s/%s after %s/%s: %v; rerun verification on the reviewed head", task.Seq, expected.Commit, expected.Tree, fresh.Commit, fresh.Tree, err)
	}
	if expected.Commit != "" {
		if err := store.ValidateFinalTreeVerification(evidence, expected.Commit, expected.Tree); err != nil {
			return clikit.Refusedf("task %03d verification does not certify the reviewed head: %v", task.Seq, err)
		}
	}
	return nil
}

func acceptOneForTreePolicy(ctx *clikit.Ctx, w *workspace.Workspace, id *agentid.Identity, t *store.Task, verify string, requireVerify, requireIndependent, allowUnverified, allowUnlanded, allowPolicyUnknown, deferLanding, fullOutput bool, into, finalCommit, finalTree string) error {
	// A task with no acceptance criteria checks zero boxes and reports success,
	// so zero boxes read as all boxes and the close certifies nothing (dacli
	// 289). Refuse unless the owner explicitly opts into an unverified close —
	// the same rule task done and the propose→sync path enforce.
	if !store.HasAcceptanceCriteria(t) && !allowUnverified {
		return emptyAcceptanceRefusal(t.Seq)
	}
	if verify == "" && requireVerify {
		return requireVerifyRefusal(t.Seq)
	}
	if verify == "" && taskRequiresCommandVerification(t) {
		return clikit.Refusedf("task %03d has a command acceptance criterion; pass --verify so artifact hash and verifier identity can be recorded", t.Seq)
	}
	if err := independenceCheck(id, t, requireIndependent); err != nil {
		return err
	}
	var verifyRecord store.VerificationEvidence
	var expectedArtifact acceptanceArtifact
	canonicalLanding := finalCommit != ""
	bindCanonical := canonicalLanding || (!deferLanding && !localUnlandedAcceptance(w, t, allowUnlanded))
	if verify != "" {
		var err error
		if bindCanonical {
			expectedArtifact, err = pinAcceptanceArtifact(ctx, w, t, into, finalCommit, finalTree)
		}
		if err != nil {
			return err
		}
		verifyRecord, err = runVerify(ctx, w, id.ID, verify, fullOutput)
		if err != nil {
			// A failed check is a RESULT, reported operationally (exit 1): the
			// verification ran and the task did not pass it, so it stays open.
			return fmt.Errorf("verification failed — task %03d NOT accepted: %w", t.Seq, err)
		}
		if bindCanonical {
			if err := recheckAcceptanceArtifact(w, t, into, canonicalLanding, expectedArtifact, verifyRecord); err != nil {
				return err
			}
			finalCommit = expectedArtifact.Commit
		}
		if !ctx.JSON {
			fmt.Fprintf(ctx.Stderr, "verification passed: %s\n", verify)
		}
	}

	// A passing verify proves the TREE is healthy, not that THIS task's work is
	// in it — the gap issue #382 called its most serious finding (done:15/21
	// reported while the commands did not exist, because the PRs had failed to
	// merge). Ask the question a build cannot: did this task's branch reach
	// trunk?
	//
	// A REFUSAL by default (issue #443). This was a warning unless
	// --require-verify was passed, and a warning was not enough: a task was
	// closed four seconds after its PR opened, with its boxes checked and a
	// passing verify recorded, against work that six days later was still not
	// in main and whose PR had become unmergeable. Nothing downstream ever
	// rechecked, because a task in done/ is taken to mean the work is in trunk.
	//
	// The warning was written where the operator was watching. The close it was
	// meant to stop happens inside a loop where nobody is. --allow-unlanded is
	// the deliberate escape and its name says what it grants.
	//
	// --defer-landing skips this check and its Log line entirely: the caller
	// (ship) is about to integrate the branch itself and will record the real
	// verdict once that has actually happened (dacli 329) — checking now would
	// only ever see "not yet landed" and stamp that as a permanent record.
	var landing landingState
	var branch, target string
	if !deferLanding {
		var err error
		target, err = landingTarget(w, t, into)
		if err != nil {
			return err
		}
		landing, branch = checkLanded(w, t, target)
		if landing == landingUnlanded && !allowUnlanded {
			return unlandedRefusal(t.Seq, branch, target)
		}
		if landing == landingUnknown {
			return unknownLandingRefusal(t.Seq, target)
		}
	}
	var checkPolicy *store.GitHubRequiredCheckPolicy
	if verify != "" {
		var err error
		checkPolicy, err = observeAcceptancePolicy(ctx, w, t, into, finalCommit, allowPolicyUnknown, &verifyRecord)
		if err != nil {
			return err
		}
	}

	// Read the pending proposals now but do NOT consume them yet: they are the
	// owner's acknowledgement of this close, and marking them applied before the
	// close is durable would orphan the work if CloseTask fails (dacli 210).
	var proposals []*eventlog.Event
	var newly, satisfied, total int
	if err := store.WithTask(w, t, func(fresh *store.Task) error {
		if verify != "" && bindCanonical {
			if err := recheckAcceptanceArtifact(w, fresh, into, canonicalLanding, expectedArtifact, verifyRecord); err != nil {
				return err
			}
		}
		if t.Owner() == id.ID && fresh.Owner() != id.ID {
			prev := fresh.Owner()
			fresh.Doc.Front.Set("owner", id.ID)
			store.AppendLog(fresh, fmt.Sprintf("adopted by %s (owner %s orphaned)", id.ID, clikit.OrDash(prev)))
		}
		proposals = pendingProposals(w, fresh)
		newly = store.CheckAllAcceptance(fresh)
		satisfied, total = acceptanceSatisfaction(fresh)
		line := fmt.Sprintf("accepted by %s", id.ID)
		if len(proposals) > 0 {
			line += fmt.Sprintf(" (applied %d proposal(s))", len(proposals))
		}
		store.AppendLog(fresh, line)
		store.AppendLog(fresh, verificationEvidence(verify, verifyWhere(ctx.Cwd)))
		if verify != "" {
			if err := store.AppendVerificationEvidence(fresh, verifyRecord); err != nil {
				return clikit.Refusedf("command verification evidence is incomplete: %v", err)
			}
		}
		if checkPolicy != nil && checkPolicy.Override {
			store.AppendLog(fresh, fmt.Sprintf("required-check policy override by %s for %s/%s: %s", id.ID, checkPolicy.Repository, checkPolicy.Branch, checkPolicy.Error))
		}
		if !deferLanding {
			store.AppendLog(fresh, landingEvidence(landing, branch, target))
		}
		if !store.HasAcceptanceCriteria(fresh) {
			store.AppendLog(fresh, emptyAcceptanceEvidence)
		}
		return store.CloseTaskAfterLandingDecision(w, fresh, id.ID)
	}); err != nil {
		return err
	}
	// The close is durable — only now consume the proposals. If CloseTask failed
	// above, they stay pending so a retry re-finds the task (dacli 210).
	markProposalsApplied(proposals)

	result := acceptanceResult{Schema: "acceptance-result/v1", Tasks: []acceptedTaskResult{}}
	result.add(acceptedTaskResult{ID: t.ID, Seq: t.Seq, Slug: t.Slug, NewlyChecked: newly, Satisfied: satisfied, Total: total, Unverified: total == 0, ExternalPolicy: checkPolicy})
	ctx.Result = result
	if ctx.JSON {
		return clikit.EmitJSON(ctx, result)
	}
	writeAcceptedTask(ctx.Stdout, result.Tasks[0])
	return nil
}

type acceptanceResult struct {
	Schema          string               `json:"schema"`
	Accepted        int                  `json:"accepted"`
	NewlyChecked    int                  `json:"newly_checked"`
	Satisfied       int                  `json:"satisfied"`
	Total           int                  `json:"total"`
	UnverifiedTasks int                  `json:"unverified_tasks"`
	Tasks           []acceptedTaskResult `json:"tasks"`
}

type acceptedTaskResult struct {
	ID             string                           `json:"id"`
	Seq            int                              `json:"seq"`
	Slug           string                           `json:"slug"`
	NewlyChecked   int                              `json:"newly_checked"`
	Satisfied      int                              `json:"satisfied"`
	Total          int                              `json:"total"`
	Unverified     bool                             `json:"unverified"`
	ExternalPolicy *store.GitHubRequiredCheckPolicy `json:"external_policy,omitempty"`
}

func (r *acceptanceResult) add(task acceptedTaskResult) {
	r.Tasks = append(r.Tasks, task)
	r.Accepted++
	r.NewlyChecked += task.NewlyChecked
	r.Satisfied += task.Satisfied
	r.Total += task.Total
	if task.Unverified {
		r.UnverifiedTasks++
	}
}

func acceptanceSatisfaction(t *store.Task) (satisfied, total int) {
	boxes := t.Acceptance()
	for _, box := range boxes {
		if box.Done {
			satisfied++
		}
	}
	return satisfied, len(boxes)
}

func writeAcceptedTask(out interface{ Write([]byte) (int, error) }, task acceptedTaskResult) {
	if task.Unverified {
		_, _ = fmt.Fprintf(out, "accepted: %03d-%s — NO acceptance criteria; explicitly UNVERIFIED; moved to done\n", task.Seq, task.Slug)
		return
	}
	_, _ = fmt.Fprintf(out, "accepted: %03d-%s — %d newly checked; %d/%d acceptance criteria satisfied; moved to done\n", task.Seq, task.Slug, task.NewlyChecked, task.Satisfied, task.Total)
}

func writeRequiredCheckPolicy(out interface{ Write([]byte) (int, error) }, policy store.GitHubRequiredCheckPolicy) {
	for _, requirement := range policy.Requirements {
		sources := make([]string, 0, len(requirement.Sources))
		for _, source := range requirement.Sources {
			label := strings.ReplaceAll(source.Kind, "_", " ")
			if source.Kind == "ruleset" && source.RulesetID != 0 {
				label = fmt.Sprintf("ruleset %d", source.RulesetID)
			}
			sources = append(sources, label)
		}
		_, _ = fmt.Fprintf(out, "required check %s · source %s\n", requirement.Name, strings.Join(sources, ", "))
	}
}

func observeAcceptancePolicy(ctx *clikit.Ctx, w *workspace.Workspace, t *store.Task, into, finalCommit string, allowUnknown bool, evidence *store.VerificationEvidence) (*store.GitHubRequiredCheckPolicy, error) {
	project, err := store.LoadProject(w, t.Project)
	if err != nil {
		var notFound store.ErrNotFound
		if errors.As(err, &notFound) {
			return nil, nil // Preserve acceptance for legacy tasks with no project record.
		}
		return nil, err
	}
	repo, ok := project.Doc.Front.Get("github_repo")
	repo = strings.TrimSpace(repo)
	if !ok || repo == "" {
		return nil, nil
	}
	branch, err := landingTarget(w, t, into)
	if err != nil {
		return nil, err
	}
	observed, policyErr := store.ObserveGitHubRequiredCheckPolicy(w.Root, repo, branch, project.Doc.Front.GetList("required_checks"), time.Now())
	if policyErr != nil && !allowUnknown {
		return nil, clikit.Refusedf("required-check policy is unobservable for %s/%s: %v; inspect GitHub access or pass --allow-unobservable-check-policy to record an audited override", repo, branch, policyErr)
	}
	if policyErr != nil {
		observed.Override = true
	}
	evidence.ExternalPolicy = &observed
	commit := evidence.CommitSHA
	if finalCommit != "" {
		commit = finalCommit
	}
	policy := store.ExternalVerificationPolicy{HeadSHA: commit, RequiredChecks: observed.Names(), RequiredArtifacts: project.Doc.Front.GetList("required_artifacts")}
	external, observeErr := store.ObserveGitHubExternalVerification(w.Root, repo, commit, time.Now())
	if observeErr != nil {
		_ = store.AttachExternalVerification(evidence, store.ExternalVerificationEvidence{Provider: "github", HeadSHA: commit, ObservedAt: time.Now().UTC(), State: "unobservable", SkipReason: observeErr.Error()})
		if len(policy.RequiredChecks) > 0 || len(policy.RequiredArtifacts) > 0 {
			return nil, clikit.Refusedf("configured external verification is unobservable for exact head %s: %v", commit, observeErr)
		}
		return &observed, nil
	}
	for _, attachment := range external {
		if attachment.HeadSHA != "" && attachment.HeadSHA != commit {
			return nil, clikit.Refusedf("external verification belongs to head %s, not reviewed head %s", attachment.HeadSHA, commit)
		}
		if err := store.AttachExternalVerification(evidence, attachment); err != nil {
			return nil, clikit.Refusedf("external verification evidence is invalid: %v", err)
		}
	}
	if err := store.ValidateExternalVerification(*evidence, policy); err != nil {
		return nil, clikit.Refusedf("configured external verification is not satisfied: %v", err)
	}
	if !ctx.JSON {
		fmt.Fprintf(ctx.Stderr, "external verification observed: %d exact-head check/workflow record(s), required checks=%d artifacts=%d\n", len(external), len(policy.RequiredChecks), len(policy.RequiredArtifacts))
		writeRequiredCheckPolicy(ctx.Stderr, observed)
	}
	return &observed, nil
}

// acceptAll accepts every task carrying at least one pending proposal. The
// verify command, if given, runs PER TASK: gating the whole batch once meant a
// single passing build closed every proposed task, including ones whose work
// was unrelated or absent (dacli 185). A task whose verification fails is
// skipped and left open. force mirrors the
// single-ref override (cmdAccept): when the acting identity is root, a task
// owned by another (finished, orphaning) agent is adopted and reconciled
// instead of skipped — so a wave-ending `ship` can auto-close every task a
// now-dead spawned agent proposed, not just the ones root itself owns.
func acceptAll(ctx *clikit.Ctx, w *workspace.Workspace, id *agentid.Identity, verify string, force, requireVerify, requireIndependent, allowUnverified, allowUnlanded, deferLanding bool, into string) error {
	return acceptAllForTree(ctx, w, id, verify, force, requireVerify, requireIndependent, allowUnverified, allowUnlanded, deferLanding, into, "", "")
}

func acceptAllForTree(ctx *clikit.Ctx, w *workspace.Workspace, id *agentid.Identity, verify string, force, requireVerify, requireIndependent, allowUnverified, allowUnlanded, deferLanding bool, into, finalCommit, finalTree string) error {
	return acceptAllForTreePolicy(ctx, w, id, verify, force, requireVerify, requireIndependent, allowUnverified, allowUnlanded, false, deferLanding, false, into, finalCommit, finalTree)
}

func acceptAllForTreePolicy(ctx *clikit.Ctx, w *workspace.Workspace, id *agentid.Identity, verify string, force, requireVerify, requireIndependent, allowUnverified, allowUnlanded, allowPolicyUnknown, deferLanding, fullOutput bool, into, finalCommit, finalTree string) error {
	proposed, err := proposedTasks(w)
	if err != nil {
		return err
	}
	if len(proposed) == 0 {
		if ctx.JSON {
			result := acceptanceResult{Schema: "acceptance-result/v1", Tasks: []acceptedTaskResult{}}
			ctx.Result = result
			return clikit.EmitJSON(ctx, result)
		}
		fmt.Fprintln(ctx.Stdout, "no tasks proposed for acceptance")
		return nil
	}
	if verify == "" && requireVerify {
		return clikit.Refusedf("--require-verify is set and no --verify command was given: %d task(s) cannot be closed on unverified assertions", len(proposed))
	}

	result := acceptanceResult{Schema: "acceptance-result/v1", Tasks: []acceptedTaskResult{}}
	for _, t := range proposed {
		if !id.CanMutate(t.Owner()) {
			if id.ID != agentid.RootID || !force {
				fmt.Fprintf(ctx.Stderr, "skipped %03d-%s: owned by %s\n", t.Seq, t.Slug, clikit.OrDash(t.Owner()))
				continue
			}
			prev := t.Owner()
			t.Doc.Front.Set("owner", id.ID)
			store.AppendLog(t, fmt.Sprintf("adopted by %s (owner %s orphaned)", id.ID, clikit.OrDash(prev)))
		}
		// A task with no acceptance criteria certifies nothing when closed
		// (dacli 289): skip it and leave it open unless the owner opted into an
		// unverified batch close, mirroring the single-ref refusal.
		if !store.HasAcceptanceCriteria(t) && !allowUnverified {
			fmt.Fprintf(ctx.Stderr, "skipped %03d-%s: no acceptance criteria — nothing to verify (pass --allow-unverified to close it explicitly UNVERIFIED)\n", t.Seq, t.Slug)
			continue
		}
		if verify == "" && taskRequiresCommandVerification(t) {
			fmt.Fprintf(ctx.Stderr, "skipped %03d-%s: command acceptance criterion requires --verify evidence\n", t.Seq, t.Slug)
			continue
		}
		if err := independenceCheck(id, t, requireIndependent); err != nil {
			fmt.Fprintf(ctx.Stderr, "skipped %03d-%s: %v\n", t.Seq, t.Slug, err)
			continue
		}
		// Verify PER TASK. A single workspace-wide command run once told you
		// the tree was healthy after all the work — it never established that
		// task N specifically was done, yet it closed every proposed task
		// (dacli 185). Re-running costs more; a true record is worth it.
		var verifyRecord store.VerificationEvidence
		var expectedArtifact acceptanceArtifact
		canonicalLanding := finalCommit != ""
		bindCanonical := canonicalLanding || (!deferLanding && !localUnlandedAcceptance(w, t, allowUnlanded))
		var checkPolicy *store.GitHubRequiredCheckPolicy
		if verify != "" {
			var err error
			if bindCanonical {
				expectedArtifact, err = pinAcceptanceArtifact(ctx, w, t, into, finalCommit, finalTree)
			}
			if err != nil {
				return err
			}
			verifyRecord, err = runVerify(ctx, w, id.ID, verify, fullOutput)
			if err != nil {
				if clikit.ExitCode(err) == 3 {
					return err
				}
				fmt.Fprintf(ctx.Stderr, "skipped %03d-%s: verification failed — %v\n", t.Seq, t.Slug, err)
				continue
			}
			if bindCanonical {
				if err := recheckAcceptanceArtifact(w, t, into, canonicalLanding, expectedArtifact, verifyRecord); err != nil {
					return err
				}
			}
			checkPolicy, err = observeAcceptancePolicy(ctx, w, t, into, expectedArtifact.Commit, allowPolicyUnknown, &verifyRecord)
			if err != nil {
				return err
			}
		}
		// Read but do not consume the proposals until the close is durable
		// (dacli 210): a CloseTask failure below returns before the mark, so the
		// proposals stay pending and the task is re-found on the next accept.
		var proposals []*eventlog.Event
		var newly, satisfied, total int
		err := store.WithTask(w, t, func(fresh *store.Task) error {
			if verify != "" && bindCanonical {
				if err := recheckAcceptanceArtifact(w, fresh, into, canonicalLanding, expectedArtifact, verifyRecord); err != nil {
					return err
				}
			}
			if t.Owner() == id.ID && fresh.Owner() != id.ID {
				prev := fresh.Owner()
				fresh.Doc.Front.Set("owner", id.ID)
				store.AppendLog(fresh, fmt.Sprintf("adopted by %s (owner %s orphaned)", id.ID, clikit.OrDash(prev)))
			}
			proposals = pendingProposals(w, fresh)
			newly = store.CheckAllAcceptance(fresh)
			satisfied, total = acceptanceSatisfaction(fresh)
			store.AppendLog(fresh, fmt.Sprintf("accepted by %s (applied %d proposal(s))", id.ID, len(proposals)))
			store.AppendLog(fresh, verificationEvidence(verify, verifyWhere(ctx.Cwd)))
			if verify != "" {
				if err := store.AppendVerificationEvidence(fresh, verifyRecord); err != nil {
					return clikit.Refusedf("command verification evidence is incomplete: %v", err)
				}
			}
			if checkPolicy != nil && checkPolicy.Override {
				store.AppendLog(fresh, fmt.Sprintf("required-check policy override by %s for %s/%s: %s", id.ID, checkPolicy.Repository, checkPolicy.Branch, checkPolicy.Error))
			}
			// Same deliverable question the single-task path asks: did THIS task's
			// work reach trunk? --all is the batch path ship and the loop use, so a
			// silent close here is the one most likely to go unnoticed.
			//
			// --defer-landing skips this entirely: ship, which always runs accept
			// before its own integrate step (integrate refuses a non-done task),
			// passes it so this check — which would only ever see "not yet landed"
			// this early — never stamps that as a permanent record. ship records the
			// real verdict itself once integrate has actually run (dacli 329).
			if !deferLanding {
				trunk, err := landingTarget(w, fresh, into)
				if err != nil {
					return err
				}
				landing, branch := checkLanded(w, fresh, trunk)
				// Refuses by default, like the single-task path (issue #443). --all
				// is what ship and the loop use, so a silent close here is the one
				// least likely to be seen by anyone.
				if landing == landingUnlanded && !allowUnlanded {
					return unlandedRefusal(fresh.Seq, branch, trunk)
				}
				if landing == landingUnknown {
					return unknownLandingRefusal(fresh.Seq, trunk)
				}
				store.AppendLog(fresh, landingEvidence(landing, branch, trunk))
			}
			if !store.HasAcceptanceCriteria(fresh) {
				store.AppendLog(fresh, emptyAcceptanceEvidence)
			}
			// CloseTask stamps "completed by" (the actuals capture field) and moves to
			// done — calibration pairs it with the spawn-time "claimed by" (E3) to size
			// the run. One canonical close for every path; no task closes without it.
			return store.CloseTaskAfterLandingDecision(w, fresh, id.ID)
		})
		if err != nil {
			return err
		}
		markProposalsApplied(proposals)
		taskResult := acceptedTaskResult{ID: t.ID, Seq: t.Seq, Slug: t.Slug, NewlyChecked: newly, Satisfied: satisfied, Total: total, Unverified: total == 0, ExternalPolicy: checkPolicy}
		if !ctx.JSON {
			writeAcceptedTask(ctx.Stdout, taskResult)
		}
		result.add(taskResult)
	}
	ctx.Result = result
	if ctx.JSON {
		return clikit.EmitJSON(ctx, result)
	}
	fmt.Fprintf(ctx.Stdout, "accepted %d task(s) — %d newly checked; %d/%d acceptance criteria satisfied", result.Accepted, result.NewlyChecked, result.Satisfied, result.Total)
	if result.UnverifiedTasks > 0 {
		fmt.Fprintf(ctx.Stdout, "; %d explicitly UNVERIFIED task(s) with no criteria", result.UnverifiedTasks)
	}
	fmt.Fprintln(ctx.Stdout)
	return nil
}

func taskRequiresCommandVerification(t *store.Task) bool {
	for i := range t.Acceptance() {
		if store.AcceptanceRequiresCommandVerification(t, i+1) {
			return true
		}
	}
	return false
}

// pendingProposals returns every unconsumed box-check proposal event for this
// task. It only READS: consuming a proposal (MarkApplied) is deferred to
// markProposalsApplied, which runs after the close is durable — see dacli 210.
func pendingProposals(w *workspace.Workspace, t *store.Task) []*eventlog.Event {
	events, err := eventlog.List(w, eventlog.Query{About: t.ID, Pending: true})
	if err != nil {
		return nil
	}
	var out []*eventlog.Event
	for _, e := range events {
		if isCloseRequest(e) {
			out = append(out, e)
		}
	}
	return out
}

// isCloseRequest reports whether an event is an agent asking the owner to
// close this task — through EITHER channel.
//
// There are two, and an agent cannot be expected to know the difference:
// `dacli accept` files an accept-propose COMMENT, while `dacli task done`
// files a propose-status EVENT. Only the comment channel was ever consumed
// here, so an agent that used `task done` — which is what the protocol
// preamble tells it to do — produced a request nothing acted on.
//
// That was one leg of a three-way deadlock (task 312): the agent's claim is
// not applied until sync, so it cannot check its own acceptance boxes; sync
// then refuses its propose:done because those boxes are unmet; and
// `accept --all` could not see the request at all. Both channels mean the
// same thing — "I believe this is finished, please verify and close" — so
// both are answered here, by the owner, who is the only one allowed to check
// the boxes.
func isCloseRequest(e *eventlog.Event) bool {
	switch e.Kind {
	case model.EventComment:
		return isProposal(e)
	case model.EventProposeStatus:
		return strings.TrimSpace(strings.TrimPrefix(e.Body, "propose:")) == string(model.StatusDone)
	}
	return false
}

// markProposalsApplied marks each proposal event as applied, returning how many
// were acknowledged. Only the owner reaches here, so marking applied is the
// owner's decision recorded — mirroring eventlog.Sync's contract. It MUST be
// called only after store.CloseTask succeeds: a proposal consumed before the
// close is durable would vanish from pending while the task stayed open, so the
// next accept could no longer re-find the task and the completed work would be
// permanently invisible (dacli 210).
func markProposalsApplied(proposals []*eventlog.Event) int {
	n := 0
	for _, e := range proposals {
		if err := eventlog.MarkApplied(e.Path); err == nil {
			n++
		}
	}
	return n
}

// proposedTasks returns every task with at least one pending acceptance
// proposal, resolved via a single task-index build (FindTask per event would be
// O(events×tasks)).
func proposedTasks(w *workspace.Workspace) ([]*store.Task, error) {
	events, err := eventlog.List(w, eventlog.Query{Pending: true})
	if err != nil {
		return nil, err
	}
	idx, err := store.BuildTaskIndex(w)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []*store.Task
	for _, e := range events {
		if !isCloseRequest(e) {
			continue
		}
		t, err := idx.Find(e.About)
		if err != nil || seen[t.ID] {
			continue
		}
		seen[t.ID] = true
		out = append(out, t)
	}
	return out, nil
}

func isProposal(e *eventlog.Event) bool {
	return strings.HasPrefix(strings.TrimSpace(e.Body), proposePrefix)
}

// runVerify executes the verification command from the caller's working tree and
// returns its error (with combined output) on a non-zero exit.
func runVerify(ctx *clikit.Ctx, w *workspace.Workspace, verifier, cmd string, fullOutput bool) (store.VerificationEvidence, error) {
	fmt.Fprintf(ctx.Stderr, "verifying: %s\n", cmd)
	ev, out, err := store.RunAcceptanceVerification(ctx.Cwd, verifier, cmd)
	if ev.ArtifactHash != "" {
		if persistErr := store.PersistVerificationArtifact(w, ev, out); persistErr != nil {
			return ev, clikit.Refusedf("retain verification artifact: %v", persistErr)
		}
	}
	if err != nil {
		fmt.Fprintln(ctx.Stderr, store.VerificationSummary(ev))
		if detail := store.VerificationOutput(out, fullOutput); detail != "" {
			fmt.Fprintln(ctx.Stderr, detail)
		} else if len(out) > 0 {
			fmt.Fprintln(ctx.Stderr, "verification output retained; rerun with --full-output to inspect the bounded artifact")
		}
		if store.IsVerificationPolicyError(err) {
			return ev, clikit.Refusedf("%v", err)
		}
		if !fullOutput {
			return ev, fmt.Errorf("`%s` exited non-zero (exit %d; artifact %s)", cmd, ev.ExitCode, ev.ArtifactHash)
		}
		return ev, fmt.Errorf("`%s` exited non-zero: %w", cmd, err)
	}
	fmt.Fprintln(ctx.Stderr, store.VerificationSummary(ev))
	return ev, nil
}

// verificationEvidence renders what actually certified a close, for the task
// log. The record must distinguish "a command ran and passed" from "nobody
// checked" — a close whose evidence is absent used to be indistinguishable
// from a verified one, which is what made every `done` label an unverified
// assertion (dacli 184).
func verificationEvidence(cmd string, where verifyContext) string {
	if cmd == "" {
		return "closed WITHOUT verification — no --verify command was given"
	}
	// State WHERE it ran. `verified by <cmd> (exit 0)` reads as a claim about
	// the deliverable, and it is not one: a build-and-test proves the tree it
	// ran in compiles, which is a different sentence from "this work is in
	// trunk" — and if it ran in the branch that just wrote the code, the two
	// are not even close. That gap closed a task four seconds after its PR
	// opened, over work that never merged (issue #443).
	//
	// The landing check below is what actually answers the trunk question, and
	// it now refuses by default. This line's job is narrower and just as
	// important: never let the record imply a verification broader than the one
	// that happened.
	return fmt.Sprintf("verified by `%s` (exit 0) in %s", cmd, where)
}

// verifyContext is the tree a verification ran against, rendered for the record.
type verifyContext struct {
	Branch string
	Head   string // short sha
}

func (v verifyContext) String() string {
	switch {
	case v.Branch == "" && v.Head == "":
		return "an unidentified working tree — proves that tree builds, nothing about trunk"
	case v.Head == "":
		return fmt.Sprintf("branch %s — proves that tree builds, not that the work is in trunk", v.Branch)
	case v.Branch == "":
		return fmt.Sprintf("commit %s — proves that tree builds, not that the work is in trunk", v.Head)
	}
	return fmt.Sprintf("branch %s at %s — proves that tree builds, not that the work is in trunk", v.Branch, v.Head)
}

// verifyWhere reads the tree runVerify actually executed in. Best-effort: an
// unreadable or non-git tree yields an empty context, which renders as the
// honest "unidentified working tree" rather than a guess.
func verifyWhere(dir string) verifyContext {
	if !gitx.Available() {
		return verifyContext{}
	}
	var vc verifyContext
	if out, err := gitx.Run(dir, "branch", "--show-current"); err == nil {
		vc.Branch = out
	}
	if out, err := gitx.Run(dir, "rev-parse", "--short", "HEAD"); err == nil {
		vc.Head = strings.TrimSpace(out)
	}
	return vc
}

// independenceCheck refuses a close where the certifier is the same agent that
// did the work. The implementer marking its own work complete is the oldest
// failure in the review literature and the one dacli's whole role split exists
// to prevent — yet nothing enforced it: the task owner (the claimant) accepted
// its own task. Opt-in, because the common operator flow (root closes work its
// children did) is already independent and must keep working (dacli 188).
func independenceCheck(id *agentid.Identity, t *store.Task, required bool) error {
	if !required {
		return nil
	}
	claimant := store.ClaimedBy(t)
	if claimant != "" && claimant == id.ID {
		return clikit.Refusedf("--require-independent is set: %s claimed task %03d and cannot also certify it; a different agent (or root) must accept", id.ID, t.Seq)
	}
	return nil
}

// emptyAcceptanceEvidence is the Log stamp for an --allow-unverified close of a
// task that stated no acceptance criteria — the record must say plainly that
// nothing was checked, never let the "done" imply otherwise (dacli 289).
const emptyAcceptanceEvidence = "closed with NO acceptance criteria — UNVERIFIED (--allow-unverified)"

// emptyAcceptanceRefusal is the empty-acceptance refusal (exit 3): a task with
// no acceptance boxes checks zero boxes and reports success, so closing it
// certifies nothing. Refuse rather than silently close, the same rule every
// close path enforces (dacli 289).
func emptyAcceptanceRefusal(seq int) error {
	return clikit.Refusedf("task %03d has no acceptance criteria: closing it would verify nothing. Add at least one criterion, or pass --allow-unverified to close it as explicitly UNVERIFIED — do not retry", seq)
}

// requireVerifyRefusal is the strict-mode refusal. Generating repositories
// whose trajectory is the product needs every close backed by a command that
// actually ran; --require-verify makes an unverified close impossible rather
// than merely visible.
func requireVerifyRefusal(seq int) error {
	return clikit.Refusedf("--require-verify is set and no --verify command was given: task %03d cannot be closed on an unverified assertion", seq)
}
