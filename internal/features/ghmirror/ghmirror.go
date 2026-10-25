// Package ghmirror is the GitHub-projection slice (docs/GITHUB.md): local
// markdown is the source of truth, GitHub is a projection that can be
// deleted and regenerated. Sync is explicit and never on the hot path.
//
// The two properties that matter, both from the spec: idempotency by marker
// (a retried sync after a timeout must converge with ZERO duplicate issues —
// the characteristic failure of naive syncers), and the disclosure gate (a
// public repository makes every mirrored artifact public; pushing there
// requires a RECORDED per-project confirmation, not a flag someone once
// passed in a script).
//
// The zero-duplicate guarantee is load-bearing, so recovery does NOT lean on
// GitHub's search index (eventually consistent — a fast retry after a
// create-then-crash would find nothing and duplicate). searchByMarker reads
// issue bodies via the strongly-consistent list endpoint and matches the
// marker by exact substring, so a just-created issue is adopted on the very
// next run. See searchByMarker for the full rationale.
package ghmirror

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/gitx"
	"github.com/mlnomadpy/dacli/internal/mdstore"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/publication"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

var Commands = []clikit.Command{
	{Path: "github doctor", Brief: "Probe gh, auth, repository visibility, and autonomous-landing branch protection", JSON: true, Usage: "dacli github doctor", Run: cmdDoctor},
	{Path: "github projection", Brief: "Show the typed public/private allowlist, withheld fields, and closure authority used by CLI and MCP publishers", JSON: true, Usage: "dacli github projection <project> [--include-internal] [--terminal]", Run: cmdProjection},
	{Path: "github link", Brief: "Bind a project to the repo (--allow-public records public-safe consent; --allow-internal separately authorizes internal evidence)", Mutates: true, Usage: "dacli github link <project> [--allow-public [--allow-internal]]", Run: cmdLink},
	{Path: "github push", Brief: "Outbound mirror under the typed public/private projection policy; public defaults to task-safe fields and needs --include-internal plus recorded authority for findings/decisions", Mutates: true, Usage: "dacli github push <project> [task-ref...] [--since <dur>] [--milestone-plan <file>] [--findings-as-issues | --closure-only] [--include-internal] [--dry-run]", Run: cmdPush},
	{Path: "github sync", Brief: "Bidirectional sync: pull then policy-governed push (--dry-run previews both halves)", JSON: true, Mutates: true, Usage: "dacli github sync <project> [task-ref...] [--since <dur>] [--findings-as-issues] [--with-tasks] [--include-internal] [--dry-run] [--limit N] [--cursor N] [--include-acceptance]", Run: cmdSync},
	{Path: "github pull", Brief: "Inbound: adopt human-authored issues as local tasks (--dry-run previews the adoptions)", JSON: true, Mutates: true, Usage: "dacli github pull <project> [--dry-run] [--limit N] [--cursor N] [--include-acceptance]", Run: cmdPull},
	{Path: "task acceptance migrate", Brief: "Preview or apply an immutable plan that imports acceptance from a task's mapped GitHub issue", Mutates: true, Usage: "dacli task acceptance migrate <ref> [--from-section \"Acceptance criteria\"] (--dry-run | --apply plan-id)", Run: cmdTaskAcceptanceMigrate},
	{Path: "github project", Brief: "Sync mirrored issues into a Project v2 board with mapped Status/Severity/Area fields (idempotent; --dry-run previews the board/items)", Mutates: true, Usage: "dacli github project <project> [--dry-run]", Run: cmdProject},
	{Path: "github release", Brief: "Cut a tagged GitHub release with generated notes on the linked repo (--notes overrides; idempotent — an existing release is reported, never duplicated; --dry-run previews the release)", Mutates: true, Usage: "dacli github release <project> <tag> [--title t] [--notes text] [--target ref] [--draft] [--prerelease] [--dry-run]", Run: cmdRelease},
	{Path: "github codeowners", Brief: "Emit .github/CODEOWNERS from role scopes (owner from the linked repo or --owner; --dry-run previews the file)", Mutates: true, Usage: "dacli github codeowners <project> [--owner org] [--stdout] [--dry-run]", Run: cmdCodeowners},
}

func cmdProjection(ctx *clikit.Ctx, args []string) error {
	w, _, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	f, err := clikit.ParseFlags(args)
	if err != nil {
		return err
	}
	if err := f.Reject("include-internal", "terminal"); err != nil {
		return err
	}
	if len(f.Pos) != 1 {
		return clikit.Usagef("usage: dacli github projection <project> [--include-internal] [--terminal]")
	}
	p, err := store.LoadProject(w, f.Pos[0])
	if err != nil {
		return err
	}
	repo, _ := p.Doc.Front.Get("github_repo")
	if repo == "" {
		return unlinkedRefusal(w.Root, p.Slug)
	}
	policy, err := publicationPolicy(w, repo, p, f.Bool("include-internal"), f.Bool("terminal"))
	if err != nil {
		return err
	}
	ctx.Result = policy
	if ctx.JSON {
		return policy.WriteJSON(ctx.Stdout)
	}
	policy.WriteText(ctx.Stdout)
	return nil
}
func cmdDoctor(ctx *clikit.Ctx, args []string) error {
	// Validate flags BEFORE probing gh: a typo should be answered
	// immediately, not after a network round-trip that fails for an
	// unrelated reason and buries it.
	if f, ferr := clikit.ParseFlags(args); ferr != nil {
		return ferr
	} else if err := f.Reject(); err != nil {
		return err
	}
	w, _, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	if _, err := exec.LookPath("gh"); err != nil {
		return fmt.Errorf("gh not on PATH — the mirror needs the GitHub CLI")
	}
	if out, err := gh(w, "auth", "status"); err != nil {
		return fmt.Errorf("gh is not authenticated: %s", out)
	}
	// doctor is a health check of the CURRENT checkout's repo (it takes no
	// project), so it deliberately resolves via cwd — hence the empty repo.
	info, err := repoView(w, "")
	if err != nil {
		return err
	}
	if info.DefaultBranch.Name == "" {
		info.DefaultBranch.Name = gitx.CurrentBranch(w.Root)
	}
	protection, err := branchProtectionView(w, info)
	if err != nil {
		// Repository/auth health is still observable when the token cannot read
		// branch rules. Preserve that distinction as an incompatible unknown
		// rather than turning a useful doctor run into an unrelated hard failure.
		protection = branchProtection{Schema: "github-branch-protection/v1", Branch: info.DefaultBranch.Name, RequiredChecks: []string{}, NextAction: "branch protection is unobservable: " + err.Error()}
	}
	result := struct {
		Schema           string           `json:"schema"`
		Authenticated    bool             `json:"authenticated"`
		Repository       string           `json:"repository"`
		Visibility       string           `json:"visibility"`
		BranchProtection branchProtection `json:"branch_protection"`
	}{Schema: "github-doctor/v1", Authenticated: true, Repository: info.NameWithOwner, Visibility: info.Visibility, BranchProtection: protection}
	ctx.Result = result
	if ctx.JSON {
		return clikit.EmitJSON(ctx, result)
	}
	fmt.Fprintf(ctx.Stdout, "gh ✓ authenticated · repo %s · visibility %s\n", info.NameWithOwner, info.Visibility)
	fmt.Fprintf(ctx.Stdout, "branch %s · checks %s · require-up-to-date %t · enforce-admins %t · reviews %d\n", protection.Branch, strings.Join(protection.RequiredChecks, ","), protection.RequireUpToDate, protection.EnforceAdmins, protection.RequiredReviewCount)
	if !protection.Compatible {
		fmt.Fprintf(ctx.Stdout, "warning: autonomous landing governance is weaker than the fresh-trunk contract — %s\n", protection.NextAction)
	}
	if strings.EqualFold(info.Visibility, "PUBLIC") {
		fmt.Fprintln(ctx.Stdout, "note: PUBLIC repo — pushing mirrors findings and reasoning to the world; `github link --allow-public` records that consent per project")
	}
	return nil
}

func cmdLink(ctx *clikit.Ctx, args []string) error {
	w, id, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	_ = id
	f, _ := clikit.ParseFlags(args)
	if err := f.Reject("allow-public", "allow-internal"); err != nil {
		return err
	}
	if len(f.Pos) == 0 {
		return clikit.Usagef("usage: dacli github link <project> [--allow-public [--allow-internal]]")
	}
	p, err := store.LoadProject(w, f.Pos[0])
	if err != nil {
		return err
	}
	// link DISCOVERS the repo to bind from the current checkout's remote, so it
	// resolves via cwd (empty repo) — this is the one place github_repo is set.
	info, err := repoView(w, "")
	if err != nil {
		return err
	}

	public := strings.EqualFold(info.Visibility, "PUBLIC")
	if f.Bool("allow-internal") && !f.Bool("allow-public") {
		return clikit.Usagef("--allow-internal requires --allow-public so the broader authority cannot be recorded accidentally")
	}
	if public && !f.Bool("allow-public") {
		return clikit.Refusedf("repo %s is PUBLIC: mirroring is a disclosure event — findings and internal reasoning become world-readable. Re-run with --allow-public to record that consent on the project", info.NameWithOwner)
	}

	p.Doc.Front.Set("github_repo", info.NameWithOwner)
	if public {
		// The recorded confirmation: in the project file, committed, blamed —
		// not a flag that evaporates with the shell history. Scoped to THIS
		// repo (its nameWithOwner), so consent for one public repo never
		// silently authorizes a push to a different repo the project is later
		// relinked to — the disclosure gate compares this to the live repo.
		p.Doc.Front.Set("github_public_confirmed", info.NameWithOwner)
		if f.Bool("allow-internal") {
			p.Doc.Front.Set("github_internal_disclosure", info.NameWithOwner)
		}
	}
	if err := mdstore.WriteFile(p.Path, p.Doc); err != nil {
		return err
	}
	fmt.Fprintf(ctx.Stdout, "linked %s → %s (%s)\n", p.Slug, info.NameWithOwner, strings.ToLower(info.Visibility))
	if public {
		fmt.Fprintln(ctx.Stdout, "public-push consent recorded on the project")
		if f.Bool("allow-internal") {
			fmt.Fprintln(ctx.Stdout, "separate internal-evidence disclosure authority recorded on the project")
		}
	}
	return nil
}

func cmdPush(ctx *clikit.Ctx, args []string) error {
	w, _, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	f, parseErr := clikit.ParseFlags(args, "milestone-plan")
	if parseErr != nil {
		return parseErr
	}
	if err := f.Reject("findings-as-issues", "with-tasks", "since", "milestone-plan", "closure-only", "include-internal", "dry-run"); err != nil {
		return err
	}
	if plans := f.All("milestone-plan"); len(plans) > 1 || len(plans) == 1 && strings.TrimSpace(plans[0]) == "" {
		return clikit.Usagef("--milestone-plan requires exactly one non-empty plan file")
	}
	if len(f.Pos) == 0 {
		return clikit.Usagef("usage: dacli github push <project> [task-ref...] [--since <dur>] [--milestone-plan <file>] [--findings-as-issues | --closure-only] [--include-internal] [--dry-run]")
	}
	// --dry-run: run the real read + decision path below but ELIDE every write —
	// each remote mutation and local mapping write is swapped for a "would ..."
	// line, so the preview is the same code path a real push runs, never a
	// parallel re-description of it (task 294).
	dry := f.Bool("dry-run")
	p, err := store.LoadProject(w, f.Pos[0])
	if err != nil {
		return err
	}
	repo, _ := p.Doc.Front.Get("github_repo")
	if repo == "" {
		return unlinkedRefusal(w.Root, p.Slug)
	}
	// G5: --findings-as-issues files each finding note as its OWN standalone,
	// severity-labeled, triageable issue (distinct from G4, which posts findings
	// as COMMENTS on a task's issue). The flag selects the standalone-issue mode;
	// without it, the default finding-comment path is unchanged.
	findingsAsIssues := f.Bool("findings-as-issues")
	closureOnly := f.Bool("closure-only")
	if f.Get("milestone-plan") != "" && (findingsAsIssues || f.Bool("with-tasks") || f.Bool("include-internal")) {
		return clikit.Usagef("--milestone-plan is an exact task taxonomy operation and cannot be combined with finding or internal projection flags")
	}
	if closureOnly {
		if len(f.Pos) < 2 {
			return clikit.Usagef("--closure-only requires one or more explicit task refs")
		}
		if findingsAsIssues || f.Bool("with-tasks") || f.Bool("include-internal") || f.Get("since") != "" {
			return clikit.Usagef("--closure-only cannot be combined with finding, task-expansion, or --since projection flags")
		}
	}
	if f.Get("milestone-plan") != "" && closureOnly {
		return clikit.Usagef("--milestone-plan cannot be combined with --closure-only")
	}

	// Visibility is re-checked LIVE at every push: a repo flipped public
	// after linking must re-trip the disclosure gate. Findings ride this same
	// gate below — a finding comment on a public issue is the risk-rank-2 leak.
	policy, err := publicationPolicy(w, repo, p, f.Bool("include-internal"), closureOnly)
	if err != nil {
		return err
	}
	if f.Bool("include-internal") && !policy.Allows(publication.FieldFindings) {
		return clikit.Refusedf("--include-internal is not authorized for %s: %s; record exact-repository authority with `dacli github link %s --allow-public --allow-internal`", repo, policy.Reason(publication.FieldFindings), p.Slug)
	}
	if findingsAsIssues && !policy.Allows(publication.FieldFindings) {
		return clikit.Refusedf("--findings-as-issues is internal evidence and is not authorized for %s: %s", repo, policy.Reason(publication.FieldFindings))
	}
	if dry {
		policy.WriteText(ctx.Stdout)
	}
	if closureOnly {
		apply := func() error { return closeMappedDoneTasks(ctx, w, p, repo, f.Pos[1:], dry) }
		if dry {
			return apply()
		}
		lockPath := githubPushLockPath(w, repo)
		if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
			return fmt.Errorf("prepare github push lock: %w", err)
		}
		return store.WithFileLock(lockPath, apply)
	}
	if dry {
		return pushProject(ctx, w, p, repo, f, true, findingsAsIssues, policy)
	}
	lockPath := githubPushLockPath(w, repo)
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return fmt.Errorf("prepare github push lock: %w", err)
	}
	return store.WithFileLock(lockPath, func() error {
		return pushProject(ctx, w, p, repo, f, false, findingsAsIssues, policy)
	})
}

// deliveryProjectionParents prevents typed slices from creating unrelated
// GitHub issues. A whole-project window drops duplicate slice rows; an
// explicit slice window projects its one canonical parent issue instead.
func deliveryProjectionParents(w *workspace.Workspace, tasks []*store.Task) ([]*store.Task, error) {
	out := make([]*store.Task, 0, len(tasks))
	seen := map[string]bool{}
	for _, task := range tasks {
		projected := task
		if task.IsDeliverySlice() {
			parent, err := store.FindTask(w, task.ParentID())
			if err != nil {
				return nil, fmt.Errorf("delivery slice %s parent: %w", task.ID, err)
			}
			projected = parent
		}
		if !seen[projected.ID] {
			seen[projected.ID] = true
			out = append(out, projected)
		}
	}
	return out, nil
}

// closeMappedDoneTasks is the least-disclosure completion projection used by
// the PR land-then-accept transaction. Unlike ordinary github push it cannot
// create/adopt/edit issues, publish findings or decisions, or enumerate an
// implicit project window. Every target must already be mapped and locally
// done, so the only remote mutation is closing that exact issue.
func closeMappedDoneTasks(ctx *clikit.Ctx, w *workspace.Workspace, p *store.Project, repo string, refs []string, dry bool) error {
	tasks, err := store.ListTasks(w, p.Slug, "")
	if err != nil {
		return err
	}
	tasks, err = selectTaskWindow(tasks, refs, time.Time{})
	if err != nil {
		return err
	}
	tasks, err = deliveryProjectionParents(w, tasks)
	if err != nil {
		return err
	}
	for _, t := range tasks {
		if t.Status != model.StatusDone {
			return clikit.Refusedf("--closure-only refuses %03d-%s: local status is %s, not done", t.Seq, t.Slug, t.Status)
		}
		num := mappedIssue(t)
		if num == 0 {
			return clikit.Refusedf("--closure-only refuses %03d-%s: no mapped GitHub issue exists", t.Seq, t.Slug)
		}
		if dry {
			fmt.Fprintf(ctx.Stdout, "would close mapped issue #%d for %03d-%s; no body, finding, decision, label, or milestone projection\n", num, t.Seq, t.Slug)
			continue
		}
		if out, err := ghRepo(w, repo, "issue", "close", strconv.Itoa(num), "--reason", "completed"); err != nil {
			return fmt.Errorf("close mapped issue #%d for %03d-%s: %w (%s)", num, t.Seq, t.Slug, err, out)
		}
		fmt.Fprintf(ctx.Stdout, "closed mapped issue #%d for %03d-%s (closure-only; no findings or decisions published)\n", num, t.Seq, t.Slug)
	}
	return nil
}

// githubPushLockPath keys the mutating lease by workspace and linked repo, not
// project: two projects may project into one repository and must not take
// independent marker snapshots before either creates. Hashing the persisted
// remote identity keeps repository names from becoming filesystem paths.
func githubPushLockPath(w *workspace.Workspace, repo string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(repo)))
	return filepath.Join(w.Root, workspace.Dir, "locks", fmt.Sprintf("github-push-%x.lock", sum[:16]))
}

// pushProject is the complete snapshot/reconcile/mutate transaction. Real
// pushes enter only under githubPushLockPath; dry-run deliberately does not
// acquire a mutating lease because it writes nothing (issue #682).
func pushProject(ctx *clikit.Ctx, w *workspace.Workspace, p *store.Project, repo string, f *clikit.Flags, dry, findingsAsIssues bool, policy publication.Policy) error {

	taskArea := areaLabel(p.Slug)

	// One issue-list snapshot for the ENTIRE push: adoption scans it in memory
	// instead of a full `gh issue list` per task/decision/finding (the old
	// behaviour cost one list call for every unmapped note in the loop). Scoped
	// to the linked repo so adoption reads the repo push writes to (dacli 221).
	idx := newMarkerIndex(w, repo)

	// Refuse a truncated index BEFORE the first create, not after the last one.
	// Both other cap-readers (pull's listIssues, project's itemSnapshot) stop on
	// a partial page; push writes to a live repository and so has the most to
	// lose by continuing (dacli 205).
	if err := idx.preflight(); err != nil {
		return err
	}

	// --findings-as-issues is a FINDINGS-ONLY push: skip the task/decision mirror
	// entirely so a run can publish just an audit's findings without also filing
	// an issue for every task in the project. (Pass --with-tasks to do both.)
	if findingsAsIssues && !f.Bool("with-tasks") {
		return mirrorFindingsOnly(w, p, repo, f, idx, dry, ctx.Stdout)
	}

	tasks, err := store.ListTasks(w, p.Slug, "")
	if err != nil {
		return err
	}

	// Task window (task 275): a mature project's done set can be hundreds of
	// tasks, and mirroring the whole backlog files (or adopts and closes) an
	// issue for every one — the operator cannot publish a single wave without
	// reaching for raw gh. Narrow the mirror to the requested window: the
	// explicit refs after the project and/or a --since cutoff. With neither, the
	// full backlog mirrors exactly as before. Computed once here and reused by
	// the --with-tasks finding-issue mirror below so both scope identically.
	since, err := sinceWindow(f)
	if err != nil {
		return err
	}
	tasks, err = selectTaskWindow(tasks, f.Pos[1:], since)
	if err != nil {
		return err
	}
	tasks, err = deliveryProjectionParents(w, tasks)
	if err != nil {
		return err
	}
	planByTask := map[string]string(nil)
	if planPath := f.Get("milestone-plan"); planPath != "" {
		if len(f.Pos) < 2 || f.Get("since") != "" {
			return clikit.Usagef("--milestone-plan requires an exact task-ref window and cannot be combined with --since")
		}
		plan, err := readMilestonePlan(planPath)
		if err != nil {
			return err
		}
		if len(f.Pos[1:]) != len(tasks) {
			return clikit.Refusedf("milestone plan requires each canonical task_id exactly once; the selected task window contains %d task(s) from %d ref(s)", len(tasks), len(f.Pos[1:]))
		}
		selectedIDs := make(map[string]bool, len(tasks))
		for _, task := range tasks {
			selectedIDs[task.ID] = true
		}
		seenRefs := map[string]bool{}
		for _, ref := range f.Pos[1:] {
			if !selectedIDs[ref] || seenRefs[ref] {
				return clikit.Refusedf("milestone plan requires each canonical task_id exactly once; %q is not a unique canonical task_id", ref)
			}
			seenRefs[ref] = true
		}
		planByTask, err = validateMilestonePlan(plan, p.Slug, repo, tasks, idx)
		if err != nil {
			return err
		}
	}

	defaultMilestone := milestoneTitle(p)
	defaultHave := false
	if planByTask == nil {
		if dry {
			defaultHave, _ = milestoneExists(w, repo, defaultMilestone)
		} else {
			defaultHave = ensureMilestone(w, repo, defaultMilestone)
		}
	}
	plannedMilestones := map[string]bool{}
	for _, title := range planByTask {
		plannedMilestones[title] = false
	}
	for title := range plannedMilestones {
		found, err := milestoneExists(w, repo, title)
		if err != nil {
			return fmt.Errorf("verify planned milestone %q: %w", title, err)
		}
		if !found {
			return clikit.Refusedf("milestone plan target %q does not exist in %s; create it explicitly, then refresh the plan", title, repo)
		}
		plannedMilestones[title] = true
	}
	if planByTask != nil {
		// Explicit plans only edit mapped issues. Creating labels here would
		// mutate repository-wide definitions outside the plan and beyond the
		// issue-level diff printed by --dry-run, so require all needed labels to
		// exist before any plan apply can edit an issue.
		repoLabels, err := listLabels(w, repo)
		if err != nil {
			return fmt.Errorf("verify labels required by milestone plan: %w", err)
		}
		needed := map[string]bool{}
		for _, task := range tasks {
			issueLabels := idx.labelsFor(mappedIssue(task))
			for _, label := range []string{statusLabel(task.Status), "type:task", taskArea} {
				if label != "" && !issueLabels[label] {
					needed[label] = true
				}
			}
		}
		labelsToCheck := append([]string(nil), "type:task", taskArea)
		for _, status := range model.AllStatuses {
			labelsToCheck = append(labelsToCheck, statusLabel(status))
		}
		for _, label := range labelsToCheck {
			if needed[label] && !repoLabels[label] {
				return clikit.Refusedf("milestone plan requires label %q, which does not exist in %s; create required labels explicitly, then refresh the plan", label, repo)
			}
			delete(needed, label)
		}
		// Keep a future status label from silently passing if the status model
		// grows without updating the deterministic list above.
		for label := range needed {
			if !repoLabels[label] {
				return clikit.Refusedf("milestone plan requires label %q, which does not exist in %s; create required labels explicitly, then refresh the plan", label, repo)
			}
		}
	}
	// G6 labels are pre-created only after every explicit milestone plan has
	// passed its stale/conflict and target-existence checks, so refusal cannot
	// leave remote changes.
	if !dry && planByTask == nil {
		precreateLabels(w, repo, taskArea)
	}
	// task 298: the window must scope the decision and finding-issue mirrors too,
	// not just the tasks — a decision rode along on EVERY push regardless of the
	// window, so `github push core 275` published task 275's issue AND every other
	// decision in the project, an unbounded disclosure on a public repo. refTasks
	// is the ref axis of the window (the tasks the explicit refs named); the
	// decision/finding mirrors scope their `about`-match against it so a windowed
	// push publishes only the notes attached to the named tasks. Empty when no refs
	// were given, so a whole-project or --since-only push is unchanged.
	refTasks := refMatchedTasks(tasks, f.Pos[1:])

	// The project's finding notes are read ONCE for the whole push (dacli 245).
	// store.ListNotes used to live INSIDE the task loop below, which made the
	// finding mirror O(tasks × notes): measured on this workspace (238 tasks)
	// the per-task shape costs 579,551,265 ns/op, 341 MB, 1,007,954 allocs
	// against 2,433,990 ns/op, 1.4 MB, 4,235 allocs hoisted — 0.58s of pure
	// local file I/O and 341 MB of garbage per push, ~10s at 1000 tasks. The
	// only per-task work is the about-match, which needs no re-read; do not put
	// the read back in the loop. Skipped in --findings-as-issues mode, where
	// mirrorFindings is not called at all.
	var findingCommentNotes []*mdstore.Doc
	if !findingsAsIssues && policy.Allows(publication.FieldFindings) {
		// Errors are swallowed exactly as before: an unreadable notes dir meant
		// "no findings to mirror", never a failed push.
		findingCommentNotes, _ = store.ListNotes(w, p.Slug, model.NoteFinding)
		findingCommentNotes = canonicalFindingDocs(findingCommentNotes)
	}

	// The decision notes (and, in --findings-as-issues mode, the finding notes) are
	// read ONCE here so the blast-radius plan below and the mirrors that follow
	// share one traversal — the same read-once discipline the finding-comment path
	// uses (dacli 245).
	var decNotes []noteFile
	if policy.Allows(publication.FieldDecisions) {
		decNotes, err = decisionNotes(w, p.Slug)
		if err != nil {
			return err
		}
	}
	var findIssueNotes []noteFile
	if findingsAsIssues {
		if findIssueNotes, err = findingNotes(w, p.Slug); err != nil {
			return err
		}
	}
	decNotes = canonicalNoteFiles(decNotes)
	findIssueNotes = canonicalNoteFiles(findIssueNotes)

	// task 298: state the blast radius — how many NEW issues of each kind this push
	// will create — BEFORE creating any, so an operator sees the disclosure size on
	// a public repo while it can still be aborted. Counts only genuine creates (a
	// note already mapped or adoptable by marker files no new issue), and only
	// notes inside the window, so the number matches what the mirrors below do.
	taskCreates := plannedTaskCreates(w, tasks, idx)
	decCreates := plannedNoteCreates(w, decNotes, refTasks, since, idx, decisionMarker, false)
	findCreates := 0
	if findingsAsIssues {
		findCreates = plannedNoteCreates(w, findIssueNotes, refTasks, since, idx, findingIssueMarker, true)
	}
	fmt.Fprintf(ctx.Stdout, "plan: will create %d task, %d decision, %d finding issue(s) on %s\n",
		taskCreates, decCreates, findCreates, repo)

	created, adopted, closed, kept, commented := 0, 0, 0, 0, 0
	for _, t := range tasks {
		milestone, haveMilestone := defaultMilestone, defaultHave
		if planByTask != nil {
			milestone = planByTask[t.ID]
			haveMilestone = plannedMilestones[milestone]
		}
		num := mappedIssue(t)

		// The idempotent create path, per GITHUB.md § 4: frontmatter first,
		// then SEARCH BY MARKER, and only then create. A crash between the
		// remote create and the local mapping write must converge on re-run
		// by adoption, never by a duplicate.
		if num == 0 {
			if matches := idx.findAll(marker(w, t)); len(matches) > 0 {
				num = matches[0]
				adopted++
				if len(matches) > 1 {
					fmt.Fprintf(ctx.Stdout, "duplicate marker for task %03d-%s matches issues %s; canonical mapping is #%d\n", t.Seq, t.Slug, issueNumbers(matches), num)
				}
				if dry {
					fmt.Fprintf(ctx.Stdout, "would adopt issue #%d by marker for task %03d-%s\n", num, t.Seq, t.Slug)
				}
			} else if found := idx.findByTitle(taskIssueTitle(t)); found > 0 {
				// task 275: an issue an operator filed by hand carries the
				// canonical `NNN: <title>` but no dacli marker, so the marker
				// search above misses it. Adopt it by exact title rather than
				// filing a duplicate. The mapping written back below binds it
				// locally, so the next push skips it via mappedIssue — the issue
				// body is never edited, exactly as pull leaves an adopted issue.
				num = found
				adopted++
				if dry {
					fmt.Fprintf(ctx.Stdout, "would adopt issue #%d by title for task %03d-%s\n", num, t.Seq, t.Slug)
				}
			}
		}
		// wouldCreate marks a task whose issue does not exist yet: in a dry-run it
		// leaves num == 0 (there is no real issue number), so the per-issue writes
		// below — mapping, labels, close, comments — are skipped for it and folded
		// into the single "would create" line instead.
		wouldCreate := false
		if num == 0 {
			if dry {
				fmt.Fprintf(ctx.Stdout, "would create issue %q\nexact title: %s\nexact body:\n%s\n", policy.Sanitize(taskIssueTitle(t)), policy.Sanitize(taskIssueTitle(t)), projectedIssueBody(w, t, policy))
				created++
				wouldCreate = true
			} else {
				body := projectedIssueBody(w, t, policy)
				createArgs := []string{"issue", "create", "--title", policy.Sanitize(taskIssueTitle(t)), "--body", body, "--label", "type:task"}
				if taskArea != "" {
					createArgs = append(createArgs, "--label", taskArea)
				}
				// Only pass --milestone when it is CONFIRMED to exist — an unknown
				// milestone would hard-fail this create and abort the whole push.
				if haveMilestone {
					createArgs = append(createArgs, "--milestone", milestone)
				}
				out, err := ghRepo(w, repo, createArgs...)
				if err != nil {
					return fmt.Errorf("issue create for %03d-%s: %w (%s)", t.Seq, t.Slug, err, out)
				}
				num = trailingInt(out)
				if num == 0 {
					return fmt.Errorf("could not parse issue number from gh output %q", out)
				}
				created++
			}
		} else if mappedIssue(t) != 0 {
			kept++
		}

		// A dry-run writes nothing — remote or local — for a would-be-created
		// issue: it has no real number to map, label, comment on or close.
		if wouldCreate {
			// Findings on a to-be-created issue ride the create; count nothing
			// here (the issue does not exist yet), and close is folded into the
			// create line below.
			if t.Status == model.StatusDone {
				closed++
			}
			continue
		}

		// Write the mapping back — after the remote exists, so the failure
		// window leaves an adoptable issue, not a dangling mapping. Skipped when
		// the mapping is already current, so an idempotent re-push does not
		// rewrite every task file (churning mtimes and git blame for a no-op).
		// A dry-run performs no local write, so the mapping is left untouched.
		if desired := githubBlock(num, repo); !dry && mappedBlockChanged(t.Doc, desired) {
			if err := store.WithTask(w, t, func(fresh *store.Task) error {
				if mappedBlockChanged(fresh.Doc, desired) {
					fresh.Doc.Front.SetBlock("github", desired)
				}
				return store.SaveTask(fresh)
			}); err != nil {
				return err
			}
		}
		if dry {
			if num != 0 {
				if planByTask != nil {
					beforeMilestone, _ := idx.issueMilestone(num)
					beforeLabels, _ := idx.issueLabels(num)
					afterLabels := projectedTaxonomyLabels(beforeLabels, t.Status, taskArea)
					fmt.Fprintf(ctx.Stdout, "would edit issue #%d: milestone %q -> %q; labels %s -> %s\n", num, beforeMilestone, milestone, formatLabelSet(beforeLabels), formatLabelSet(afterLabels))
				} else if diff := issueTaxonomyArgs(idx, num, t.Status, taskArea, milestone, haveMilestone); len(diff) > 0 {
					fmt.Fprintf(ctx.Stdout, "would edit issue #%d taxonomy: %s\n", num, strings.Join(diff, " "))
				} else {
					fmt.Fprintf(ctx.Stdout, "issue #%d taxonomy unchanged\n", num)
				}
			}
		} else {
			// One diffed edit per issue, covering the status label (G1), the
			// type:/area: taxonomy (G6) and the milestone (dacli 224).
			//
			// These were three unconditional calls that issued five gh
			// invocations between them — add, three separate removes, plus the
			// taxonomy — for EVERY mapped issue on every push, whether or not
			// anything had changed. At this repo's ~300 mirrored tasks an
			// idempotent re-push spent ~2,100 network round-trips relabelling
			// issues to the values they already had. syncIssueTaxonomy diffs
			// against the snapshot the marker index has already fetched and
			// makes zero calls when the issue is current.
			if planByTask != nil {
				if err := syncIssueTaxonomyStrict(w, repo, idx, num, t.Status, taskArea, milestone, haveMilestone); err != nil {
					return fmt.Errorf("milestone plan apply stopped at task %s: %w", t.ID, err)
				}
			} else {
				syncIssueTaxonomy(w, repo, idx, num, t.Status, taskArea, milestone, haveMilestone)
			}
		}

		// Findings backlink to the issue a human sees: each finding note about
		// this task becomes an issue comment, idempotent by a per-finding marker
		// so a re-push never duplicates. Behind the disclosure gate tripped above.
		// Skipped in --findings-as-issues mode, where findings become standalone
		// issues instead (mirrored once, after the task loop).
		if planByTask == nil && !findingsAsIssues && policy.Allows(publication.FieldFindings) {
			if dry {
				// findingsToPost is the SHARED decision both the real mirror and
				// this preview run, so a would-comment can never drift from what a
				// real push comments (task 294).
				if todo, terr := findingsToPost(w, repo, num, t, findingCommentNotes); terr == nil {
					for _, n := range todo {
						id, _ := n.Front.Get("id")
						fmt.Fprintf(ctx.Stdout, "would comment on issue #%d: finding %s\nexact comment body:\n%s\n", num, id, policy.Sanitize(findingCommentBody(w, n)))
						commented++
					}
				}
			} else {
				posted, ferr := mirrorFindings(w, repo, num, t, findingCommentNotes)
				commented += posted
				if ferr != nil {
					return fmt.Errorf("github push incomplete: task stage stopped at %03d-%s; closure stage and decision stage were not completed: %w", t.Seq, t.Slug, ferr)
				}
			}
		}

		if planByTask == nil && t.Status == model.StatusDone {
			if dry {
				fmt.Fprintf(ctx.Stdout, "would close issue #%d (%s)\n", num, taskIssueTitle(t))
				closed++
			} else if closeOut, err := ghRepo(w, repo, "issue", "close", strconv.Itoa(num)); err != nil {
				// A failed close is a partial apply, not cosmetic drift. Returning
				// here leaves later decisions untouched; the next marker-idempotent
				// push retries this closure before entering that stage (task 394).
				return fmt.Errorf("github push incomplete: task stage stopped at %03d-%s; closure stage failed and decision stage was not completed: %w (%s)", t.Seq, t.Slug, err, closeOut)
			} else {
				closed++
			}
		}
	}
	if dry {
		fmt.Fprintf(ctx.Stdout, "dry-run: push would create %d, adopt %d, leave %d unchanged, close %d, add %d finding comment(s) (of %d tasks); nothing was written\n",
			created, adopted, kept, closed, commented, len(tasks))
	} else {
		fmt.Fprintf(ctx.Stdout, "push: %d created, %d adopted-by-marker, %d unchanged, %d closed, %d finding comment(s) (of %d tasks)\n",
			created, adopted, kept, closed, commented, len(tasks))
	}
	if planByTask != nil {
		fmt.Fprintf(ctx.Stdout, "milestone plan: %s for %d explicitly selected task issue(s)\n", map[bool]string{true: "previewed", false: "applied"}[dry], len(planByTask))
	} else if defaultHave {
		if dry {
			fmt.Fprintf(ctx.Stdout, "milestone: %s (task issues would be assigned; legacy project-title default)\n", defaultMilestone)
		} else {
			fmt.Fprintf(ctx.Stdout, "milestone: %s (task issues assigned; legacy project-title default)\n", defaultMilestone)
		}
	}
	if planByTask != nil {
		return nil // explicit plan mode changes only selected issue labels/milestones
	}

	// G2: decisions ride the SAME explicit push and the SAME disclosure gate
	// (already tripped above), never auto-run on ship. They share the one
	// issue-list snapshot, so decision adoption costs no extra list call. task 298:
	// scoped to the SAME window as the tasks — refTasks + since — so a windowed
	// push publishes only the decisions attached to the named tasks, never every
	// project decision unscoped.
	if policy.Allows(publication.FieldDecisions) {
		if err := mirrorDecisions(w, repo, decNotes, refTasks, since, idx, dry, ctx.Stdout); err != nil {
			return err
		}
	}

	// With --with-tasks, findings-as-issues runs AFTER the task mirror above.
	// (The findings-ONLY path returned earlier before the task loop.) It shares
	// the same window — refTasks + since — computed for the task window above, so
	// the standalone finding issues scope identically to the tasks (task 298).
	if findingsAsIssues {
		if err := mirrorFindingIssues(w, repo, findIssueNotes, refTasks, since, idx, dry, ctx.Stdout); err != nil {
			return err
		}
	}
	if !dry {
		fmt.Fprintf(ctx.Stdout, "applied: github push completed task, closure, decision, and finding stages on %s\n", repo)
	}
	return nil
}

// mirrorFindingsOnly is the FINDINGS-ONLY push (--findings-as-issues without
// --with-tasks): the disclosure gate has already tripped; project just the
// finding notes as issues, scoped by --since. No task/decision issues are
// touched — so an audit can publish its findings without filing an issue per
// task in the project.
func mirrorFindingsOnly(w *workspace.Workspace, p *store.Project, repo string, f *clikit.Flags, idx *markerIndex, dry bool, out io.Writer) error {
	since, err := sinceWindow(f)
	if err != nil {
		return err
	}
	// task 298: honor an explicit task-ref window here too — scope the finding
	// issues to those about the named tasks — rather than silently ignoring the
	// refs an operator typed (the invisible-drop failure mode). The refs are
	// validated against the project's tasks via selectTaskWindow (zero since, so it
	// returns exactly the ref-matched tasks), so an unknown ref is a not-found
	// error, never a silent empty scope.
	var refTasks []*store.Task
	if refs := f.Pos[1:]; len(refs) > 0 {
		tasks, err := store.ListTasks(w, p.Slug, "")
		if err != nil {
			return err
		}
		if refTasks, err = selectTaskWindow(tasks, refs, time.Time{}); err != nil {
			return err
		}
	}
	notes, err := findingNotes(w, p.Slug)
	if err != nil {
		return err
	}
	notes = canonicalNoteFiles(notes)
	// State the blast radius before creating any issue (task 298).
	fmt.Fprintf(out, "plan: will create %d finding issue(s) on %s\n",
		plannedNoteCreates(w, notes, refTasks, since, idx, findingIssueMarker, true), repo)
	if err := mirrorFindingIssues(w, repo, notes, refTasks, since, idx, dry, out); err != nil {
		return err
	}
	if !dry {
		fmt.Fprintf(out, "applied: github push completed finding stages on %s\n", repo)
	}
	return nil
}

// refMatchedTasks returns the subset of tasks that the explicit window refs name —
// the ref axis of the push window (task 298), as distinct from the --since axis.
// It is the set the decision and finding-issue mirrors scope their `about`-match
// against, so a windowed push publishes only the notes attached to the named
// tasks. Empty when no refs were given, so a whole-project or --since-only push
// leaves the note mirrors unscoped by refs (the since axis still applies).
func refMatchedTasks(tasks []*store.Task, refs []string) []*store.Task {
	if len(refs) == 0 {
		return nil
	}
	var out []*store.Task
	for _, t := range tasks {
		for _, ref := range refs {
			if taskMatchesRef(t, ref) {
				out = append(out, t)
				break
			}
		}
	}
	return out
}

// noteInWindow reports whether a decision or finding note falls inside the push
// window (task 298). With no window — no refs (empty refTasks) and a zero since —
// every note is in, the default whole-project mirror. Otherwise a note is in the
// window when it was created at or after the --since cutoff (the temporal axis,
// the rule the task window and the finding-issue --since filter both apply), OR
// its `about` field names one of the tasks the explicit refs selected (the ref
// axis) — so `github push core 275` mirrors the decision about 275 and leaves
// every other project decision unpublished, the scoping the task mirror already
// had and the decision mirror lacked.
func noteInWindow(doc *mdstore.Doc, refTasks []*store.Task, since time.Time) bool {
	if len(refTasks) == 0 && since.IsZero() {
		return true
	}
	if !since.IsZero() {
		if cs, ok := doc.Front.Get("created"); ok {
			if ct, err := time.Parse(time.RFC3339, cs); err == nil && !ct.Before(since) {
				return true
			}
		}
	}
	for _, rt := range refTasks {
		if findingAboutTask(doc, rt) {
			return true
		}
	}
	return false
}

// plannedTaskCreates counts the windowed tasks that would file a BRAND-NEW issue —
// neither already mapped nor adoptable by marker or by canonical title — using the
// in-memory issue-list snapshot, so the blast-radius plan (task 298) matches what
// the task loop actually creates without a second network call.
func plannedTaskCreates(w *workspace.Workspace, tasks []*store.Task, idx *markerIndex) int {
	n := 0
	for _, t := range tasks {
		if mappedIssue(t) != 0 {
			continue
		}
		if idx.find(marker(w, t)) > 0 {
			continue
		}
		if idx.findByTitle(taskIssueTitle(t)) > 0 {
			continue
		}
		n++
	}
	return n
}

// plannedNoteCreates counts the in-window notes that would file a brand-new issue —
// keyed on the same conditions the decision/finding-issue mirrors use (has an id,
// non-empty text when requireText, in the window, not already mapped, not adoptable
// by marker) — so the blast-radius plan (task 298) never over- or under-states the
// creates. mk is the note's marker function (decisionMarker or findingIssueMarker).
func plannedNoteCreates(w *workspace.Workspace, notes []noteFile, refTasks []*store.Task, since time.Time, idx *markerIndex, mk func(*workspace.Workspace, string) string, requireText bool) int {
	n := 0
	for _, dn := range notes {
		if dn.id == "" {
			continue
		}
		if requireText && findingText(dn.doc) == "" {
			continue
		}
		if !noteFileInWindow(dn, refTasks, since) {
			continue
		}
		if mappedIssueDoc(dn.doc) != 0 {
			continue
		}
		if findNoteMarker(w, idx, dn, mk) > 0 {
			continue
		}
		n++
	}
	return n
}

// sinceWindow parses --since <dur> (e.g. 2h, 90m) into a cutoff time; the zero
// time means "no window — every finding". Shared so the findings-only and
// --with-tasks paths scope identically.
func sinceWindow(f *clikit.Flags) (time.Time, error) {
	v := f.Get("since")
	if v == "" {
		return time.Time{}, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return time.Time{}, clikit.Usagef("--since wants a duration like 2h or 90m: %v", err)
	}
	return time.Now().Add(-d), nil
}

// selectTaskWindow narrows the tasks a push mirrors to the requested window
// (task 275): the explicit refs after the project and/or a --since cutoff. With
// no refs and a zero since it returns tasks unchanged, so the default push still
// mirrors the whole backlog.
//
// When both are given the window is the UNION — the named refs PLUS everything
// created since the cutoff — so `push core 275 --since 2h` mirrors task 275 even
// if it predates the window. A task selected by neither is left out.
//
// A ref that names no task in the project is a NOT-FOUND error (exit 4), never a
// silent no-op: an operator who asked to mirror a specific task must hear that it
// was not found, not watch the push report success having filed nothing for it —
// the invisible-success failure mode this tool guards against hardest.
func selectTaskWindow(tasks []*store.Task, refs []string, since time.Time) ([]*store.Task, error) {
	if len(refs) == 0 && since.IsZero() {
		return tasks, nil
	}
	matched := make(map[string]bool, len(refs))
	var out []*store.Task
	for _, t := range tasks {
		include := false
		for _, ref := range refs {
			if taskMatchesRef(t, ref) {
				include = true
				matched[ref] = true
			}
		}
		if !include && !since.IsZero() && taskCreatedSince(t, since) {
			include = true
		}
		if include {
			out = append(out, t)
		}
	}
	var missing []string
	for _, ref := range refs {
		if !matched[ref] {
			missing = append(missing, ref)
		}
	}
	if len(missing) > 0 {
		return nil, store.ErrNotFound{Ref: strings.Join(missing, ", ")}
	}
	return out, nil
}

// taskCreatedSince reports whether task t was created at or after the cutoff. A
// task with no parseable `created` stamp is treated as OUTSIDE the window — a
// --since window means "demonstrably created since", never "assume recent" — the
// same rule the finding-issue --since filter applies (mirrorFindingIssues).
func taskCreatedSince(t *store.Task, since time.Time) bool {
	cs, ok := t.Doc.Front.Get("created")
	if !ok {
		return false
	}
	ct, err := time.Parse(time.RFC3339, cs)
	if err != nil {
		return false
	}
	return !ct.Before(since)
}

// taskIssueTitle is the canonical GitHub issue title a task mirrors to,
// `NNN: <title>`. One definition so the create path and the title-adoption path
// (task 275, idx.findByTitle) compare against the identical string and never
// drift — a drift would either duplicate (adopt misses, create fires) or adopt
// the wrong issue.
func taskIssueTitle(t *store.Task) string {
	return fmt.Sprintf("%03d: %s", t.Seq, t.Title)
}

// publicationPolicy re-checks live visibility and returns the one typed policy
// used by every outbound issue projection. Public-safe consent and internal-
// evidence authority are deliberately separate, exact-repository grants:
// --allow-public alone never publishes decisions or findings (issue #873).
func publicationPolicy(w *workspace.Workspace, repo string, p *store.Project, includeInternal, terminal bool) (publication.Policy, error) {
	// Judge the LINKED repo's visibility (the repo push actually writes to),
	// not the cwd remote's — a public linked repo must trip the gate even from a
	// private checkout, and a private linked repo must not be blocked by a public
	// cwd (dacli 221).
	info, err := repoView(w, repo)
	if err != nil {
		return publication.Policy{}, err
	}
	if strings.EqualFold(info.Visibility, "PUBLIC") {
		confirmed, _ := p.Doc.Front.Get("github_public_confirmed")
		if !consentCoversRepo(confirmed, info.NameWithOwner) {
			return publication.Policy{}, clikit.Refusedf("repo %s is PUBLIC and project %s has no recorded consent for its public-safe projection — `dacli github link %s --allow-public`", info.NameWithOwner, p.Slug, p.Slug)
		}
	}
	internal, _ := p.Doc.Front.Get("github_internal_disclosure")
	return publication.New(info.NameWithOwner, info.Visibility, includeInternal, consentCoversRepo(internal, info.NameWithOwner), terminal), nil
}

// disclosureGate remains the shared board/push guard. Those callers only need
// the authorization result; github push consumes the returned typed policy.
func disclosureGate(w *workspace.Workspace, repo string, p *store.Project) error {
	_, err := publicationPolicy(w, repo, p, false, false)
	return err
}

// consentCoversRepo reports whether the recorded public-push consent applies to
// the repo currently being pushed to. Consent is SCOPED to the exact repo it was
// granted for (stored as nameWithOwner, not a bare boolean), so a project
// relinked to a DIFFERENT public repo must consent afresh — a "true" flag would
// have silently leaked to any repo the project was later pointed at. A legacy
// bare-boolean "true" no longer matches any repo, so it fails closed (re-link).
func consentCoversRepo(confirmed, repo string) bool {
	return confirmed != "" && strings.EqualFold(confirmed, repo)
}

// --- inbound: github pull (G4) ---

// ghIssue is the subset of a remote issue that pull reads to seed a task.
func originRepo(root string) string {
	out, err := gitx.Run(root, "remote", "get-url", "origin")
	if err != nil {
		return ""
	}
	u := strings.TrimSpace(out)
	u = strings.TrimSuffix(u, ".git")
	if i := strings.LastIndex(u, ":"); i >= 0 && !strings.Contains(u[i+1:], "/") {
		// SSH scp-style: git@github.com:owner/name — but only when the part
		// after the colon is not itself a path (which would be a port or a
		// URL scheme).
		u = u[i+1:]
	}
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
		if j := strings.Index(u, "/"); j >= 0 {
			u = u[j+1:]
		}
	}
	parts := strings.Split(strings.Trim(u, "/"), "/")
	if len(parts) < 2 {
		return ""
	}
	return parts[len(parts)-2] + "/" + parts[len(parts)-1]
}

// unlinkedRefusal names the fix, filling in the repo when `origin` reveals it.
func unlinkedRefusal(root, slug string) error {
	if repo := originRepo(root); repo != "" {
		return clikit.Usagef("project %s is not linked to a GitHub repo, so nothing can be mirrored — this repo's origin is %s: run `dacli github link %s --repo %s` (linking is a consent step, so it is never inferred for you)", slug, repo, slug, repo)
	}
	return clikit.Usagef("project %s is not linked — `dacli github link %s` first (and this repo has no `origin` remote to infer one from)", slug, slug)
}
