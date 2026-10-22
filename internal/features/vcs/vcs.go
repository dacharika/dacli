// Package vcs is the version-control slice: agents commit AS THEMSELVES with
// their role, so git blame and git log answer "who implemented this, and in
// what role" — the audit trail that lets a reviewer target findings at the
// responsible agent and lets the team improve over time, the way a human
// team uses blame.
//
// dacli owns the commit so attribution is guaranteed, not left to a prompt an
// agent might get wrong. It still runs one named command (git), consistent
// with the amended non-goal (DESIGN § 2): dacli runs agents, not work — and a
// commit is the agent recording its own work, attributed.
package vcs

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mlnomadpy/dacli/internal/agentid"
	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/commandresult"
	"github.com/mlnomadpy/dacli/internal/eventlog"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/procmon"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

const commitUsage = `dacli commit "<message>" [--task ref] [--no-add] [--force]`

var Commands = []clikit.Command{
	{Path: "commit", Brief: "Commit as yourself: author = agent (role), with dacli trailers and explicit ownership/path-scope controls", JSON: true, Mutates: true, Usage: commitUsage, Run: cmdCommit},
	{Path: "worktree reclaim", Brief: "Preview or apply an audited root recovery of a terminal agent worktree", Mutates: true, Usage: "dacli worktree reclaim --claim path,path [--apply]", Run: cmdWorktreeReclaim},
	{Path: "blame", Brief: "Who wrote each line — agent and role — for a file", Usage: "dacli blame <file>", Run: cmdBlame},
	{Path: "contrib", Brief: "Per-role / per-agent contribution rollup from commit events", Usage: "dacli contrib", Run: cmdContrib},
}

type commitControls struct {
	TaskRef           string   `json:"task_ref,omitempty"`
	TaskOwner         string   `json:"task_owner,omitempty"`
	TaskOwnedByActor  bool     `json:"task_owned_by_actor"`
	Worktree          string   `json:"worktree"`
	Branch            string   `json:"branch"`
	CanonicalWorktree bool     `json:"canonical_worktree"`
	CanonicalBranch   bool     `json:"canonical_branch"`
	PathScopePresent  bool     `json:"path_scope_present"`
	PathScope         []string `json:"path_scope"`
	PathScopeSource   string   `json:"path_scope_source,omitempty"`
}

type commitDiagnostic struct {
	Code        string         `json:"code"`
	Message     string         `json:"message"`
	Controls    commitControls `json:"controls"`
	NextAction  string         `json:"next_action"`
	Remediation string         `json:"remediation_command"`
}

type commitResult struct {
	Schema     string            `json:"schema"`
	SHA        string            `json:"sha"`
	Author     string            `json:"author"`
	Controls   commitControls    `json:"controls"`
	Diagnostic *commitDiagnostic `json:"diagnostic,omitempty"`
}

// authorName encodes the role into the git identity so plain `git blame` and
// `git log --format=%an` are already legible without any dacli tooling.
func authorName(id, role string) string {
	if role != "" && role != "root" {
		return fmt.Sprintf("%s (%s)", id, role)
	}
	return id
}

// git runs in the ACTUAL working directory (ctx.Cwd), not w.Root: an agent
// committing from an isolated worktree must commit in that worktree, on its
// own branch — while the .dacli workspace stays the shared one found by
// walking up. Using w.Root would send every worktree's commit to main and
// trip the branch guard (found by the parallel-lifecycle test).
func gitIn(dir string, args ...string) (string, error) {
	// A deadline so a git child blocked on a credential prompt cannot hang the
	// caller — critical under `dacli mcp serve`, where it would freeze the
	// stdio loop. These are all local plumbing (add/commit/blame/rev-parse).
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	operation := "git"
	if len(args) > 0 {
		operation += " " + args[0]
	}
	out, err := commandresult.Run(cmd, commandresult.RunOptions{
		Operation: operation, WorkspaceRoot: dir,
		TimedOut: func() bool { return ctx.Err() == context.DeadlineExceeded },
	})
	return strings.TrimSpace(string(out)), err
}

func cmdCommit(ctx *clikit.Ctx, args []string) error {
	w, id, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	f, err := clikit.ParseFlags(args, "task")
	if err != nil {
		return err
	}
	if err := f.Reject("task", "no-add", "force"); err != nil {
		return err
	}
	if len(f.Pos) != 1 || strings.HasPrefix(f.Pos[0], "-") {
		return clikit.Usagef("usage: " + commitUsage)
	}
	if id.Grant != model.GrantRW {
		return clikit.Refusedf("committing writes to the repo; that needs an rw grant (yours is %s)", id.Grant)
	}
	if _, err := exec.LookPath("git"); err != nil {
		return fmt.Errorf("git not on PATH")
	}
	// Commit in the agent's ACTUAL directory (its worktree, if isolated),
	// not the shared workspace root.
	gitDir := ctx.Cwd

	// Never commit to the default branch — the same rule the git_workflow
	// prompt gives agents, enforced here so it cannot be skipped.
	// branch --show-current names an unborn branch too, where rev-parse
	// --abbrev-ref returns a useless "HEAD".
	branch, _ := gitIn(gitDir, "branch", "--show-current")
	if branch == "main" || branch == "master" {
		return clikit.Refusedf("refusing to commit on %s — branch first (git checkout -b dacli/<task>-<slug>)", branch)
	}

	// A worktree created for a spawned agent belongs to that child even when a
	// later dacli process loses DACLI_AGENT and falls back to a-root. Check this
	// before git add: tasks 422/423 showed that a malformed owner invocation can
	// otherwise stage and commit the child's verified diff as `-m`, leaving the
	// documented child commit to misleadingly report "nothing staged".
	worktreeRoot, topErr := gitIn(gitDir, "rev-parse", "--show-toplevel")
	if topErr != nil {
		return fmt.Errorf("resolve git worktree: %w", topErr)
	}
	if owner, ok := agentWorktreeOwner(w, worktreeRoot); ok && owner != id.ID {
		return clikit.Refusedf("refusing to commit worktree owned by %s as %s — restore that child's DACLI_AGENT token, or have root preview `dacli worktree reclaim --claim path,path`; staged work was preserved", owner, id.ID)
	}

	if !f.Bool("no-add") {
		if _, err := gitIn(gitDir, "add", "-A"); err != nil {
			return fmt.Errorf("git add: %w", err)
		}
	}
	staged, _ := gitIn(gitDir, "diff", "--cached", "--name-only")
	if staged == "" {
		return clikit.Usagef("nothing staged to commit")
	}

	// Claim-scoped commit (E2): the spawn recorded this agent's --claim scope
	// in the run record. Refuse CODE files staged outside it — this is what
	// kills the "do NOT git add -A, stage ONLY these files" boilerplate every
	// brief needed (agents still slipped and staged their own agent file).
	// Files under .dacli/ (the task's own record, workspace crumbs) are always
	// allowed: we do not fight the workspace record. --force overrides with a
	// loud note. An agent with no recorded claim is warned once, not blocked.
	taskRef := f.Get("task")
	var task *store.Task
	if t, findErr := store.FindTask(w, taskRef); taskRef != "" && findErr == nil {
		taskRef, task = t.ID, t
	}
	controls := commitControls{TaskRef: taskRef, Worktree: worktreeRoot, Branch: branch, PathScope: []string{}}
	if task != nil {
		controls.TaskOwner = task.Owner()
		controls.TaskOwnedByActor = task.Owner() == id.ID
		controls.CanonicalBranch = branch == BranchFor(task)
		controls.CanonicalWorktree = sameWorktree(w.WorktreePath(task.Project, task.Seq, task.Slug), worktreeRoot)
	}
	claimSet, ok := agentClaims(w, id.ID, worktreeRoot, branch, taskRef)
	controls.PathScopePresent = ok
	if ok {
		controls.PathScope = append([]string(nil), claimSet.paths...)
		controls.PathScopeSource = claimSet.provenance
	}
	var diagnostic *commitDiagnostic
	if ok {
		claims := claimSet.paths
		var outside []string
		for _, p := range strings.Split(staged, "\n") {
			if p = strings.TrimSpace(p); p == "" || inClaimScope(p, claims) {
				continue
			}
			outside = append(outside, p)
		}
		if len(outside) > 0 {
			if !f.Bool("force") {
				return clikit.Refusedf("refusing to commit %d file(s) outside your claim [%s]: %s — %s; stage only the declared paths (plus .dacli/ records), or record a corrected audited recovery transfer",
					len(outside), strings.Join(claims, ", "), strings.Join(outside, ", "), claimSet.provenance)
			}
			fmt.Fprintf(ctx.Stderr, "warning: --force committing %d file(s) OUTSIDE your claim [%s]: %s\n",
				len(outside), strings.Join(claims, ", "), strings.Join(outside, ", "))
		}
	} else {
		remediation := "dacli context " + clikit.OrDash(taskRef) + " --json"
		diagnostic = &commitDiagnostic{Code: "path_scope_unavailable", Controls: controls, Message: "no path-scope claim governs this commit", NextAction: "inspect task/worktree association, then establish a narrow spawn claim or preview an audited worktree recovery transfer", Remediation: remediation}
		if !ctx.JSON {
			fmt.Fprintf(ctx.Stderr, "warning[path_scope_unavailable]: no path-scope claim for actor %s; task=%s owner=%s owned-by-actor=%t worktree=%s canonical-worktree=%t branch=%s canonical-branch=%t — committing without file-scope enforcement; inspect with `%s`\n",
				id.ID, clikit.OrDash(taskRef), clikit.OrDash(controls.TaskOwner), controls.TaskOwnedByActor, worktreeRoot, controls.CanonicalWorktree, branch, controls.CanonicalBranch, remediation)
		}
	}

	domain, tp := w.Attribution()
	name := authorName(id.ID, id.Role)
	email := id.ID + domain
	msg := f.Pos[0]

	// Attribution must not degrade silently. A non-root agent with no resolved
	// role produces a bare-id commit with no Dacli-Role trailer — which looks
	// fine until someone inspects it. Say so loudly (the usual cause is the
	// agent's identity file not being visible from here — e.g. a worktree
	// checkout that predates the spawn).
	if id.Role == "" && id.ID != "a-root" {
		fmt.Fprintf(ctx.Stderr, "warning: committing as %s with no resolved role — commit will lack a %s-Role trailer (is %s.md present in this checkout's .dacli/agents/?)\n",
			id.ID, tp, id.ID)
	}

	// Trailers: machine-parseable provenance alongside the human author. The
	// prefix is workspace-configurable (dacli 196).
	trailers := fmt.Sprintf("\n\n%s-Agent: %s", tp, id.ID)
	if id.Role != "" {
		trailers += fmt.Sprintf("\n%s-Role: %s", tp, id.Role)
	}
	if taskRef != "" {
		if t, err := store.FindTask(w, taskRef); err == nil {
			trailers += fmt.Sprintf("\n%s-Task: %03d-%s", tp, t.Seq, t.Slug)
			taskRef = t.ID
		}
	}

	out, err := gitIn(gitDir,
		"-c", "user.name="+name, "-c", "user.email="+email,
		"commit", "--author", fmt.Sprintf("%s <%s>", name, email),
		"-m", msg+trailers)
	if err != nil {
		return fmt.Errorf("git commit failed: %s", out)
	}
	sha, _ := gitIn(gitDir, "rev-parse", "--short", "HEAD")

	// The commit becomes a first-class workspace event — attributed, so the
	// team's whole read surface (standup, replay, contrib) sees it.
	body := fmt.Sprintf("%s %s\nrole: %s", sha, msg, clikit.OrDash(id.Role))
	if _, evErr := eventlog.Append(w, id.ID, model.EventCommit, taskRef, "", body); evErr != nil {
		return evErr
	}
	result := commitResult{Schema: "commit-result/v1", SHA: sha, Author: name, Controls: controls, Diagnostic: diagnostic}
	ctx.Result = result
	if ctx.JSON {
		return clikit.EmitJSON(ctx, result)
	}
	fmt.Fprintf(ctx.Stdout, "committed %s as %s\n", sha, name)
	return nil
}

const worktreeTransferFile = "worktree-transfer.txt"

type worktreeOwnership struct {
	runDir string
	runID  string
	owner  string
}

type worktreeTransfer struct {
	Owner    string
	Claims   []string
	Worktree string
	Branch   string
	PriorRun string
}

// cmdWorktreeReclaim is deliberately a two-invocation operation. Issue #694
// was caused by recovery pressure after runtimes failed before reading their
// prompts; printing the exact checkout state on a no-write preview keeps that
// pressure from turning into a blind break-glass mutation.
func cmdWorktreeReclaim(ctx *clikit.Ctx, args []string) error {
	w, id, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	f, err := clikit.ParseFlags(args, "claim")
	if err != nil {
		return err
	}
	if err := f.Reject("claim", "apply"); err != nil {
		return err
	}
	if len(f.Pos) != 0 || f.Get("claim") == "" {
		return clikit.Usagef("usage: dacli worktree reclaim --claim path,path [--apply]")
	}
	if id.ID != agentid.RootID || id.Grant != model.GrantRW {
		return clikit.Refusedf("worktree recovery is root-only and requires an rw grant (you are %s with %s)", id.ID, id.Grant)
	}
	claims, err := recoveryClaims(f.Get("claim"))
	if err != nil {
		return err
	}
	worktreeRoot, err := gitIn(ctx.Cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		return fmt.Errorf("resolve git worktree: %w", err)
	}

	apply := func() error {
		ownerships, err := worktreeOwnerships(w, worktreeRoot)
		if err != nil {
			return clikit.Refusedf("cannot prove every worktree owner is terminal: %v", err)
		}
		if len(ownerships) == 0 {
			return clikit.Refusedf("no spawned-agent ownership record names worktree %s", worktreeRoot)
		}
		latest := ownerships[0]
		if latest.owner == id.ID {
			return clikit.Refusedf("worktree is already owned by %s through run %s; no recovery mutation is needed", id.ID, latest.runID)
		}
		branch, branchErr := gitIn(worktreeRoot, "branch", "--show-current")
		if branchErr != nil || branch == "" {
			return clikit.Refusedf("cannot resolve the worktree branch: %v", branchErr)
		}
		dirty, dirtyErr := gitIn(worktreeRoot, "status", "--short")
		if dirtyErr != nil {
			return clikit.Refusedf("cannot inspect dirty paths: %v", dirtyErr)
		}
		fmt.Fprintf(ctx.Stdout, "worktree: %s\nbranch: %s\nprior owner: %s\nprior run: %s\ndirty paths:\n%s\nclaims: %s\nnew owner: %s\n",
			worktreeRoot, branch, latest.owner, latest.runID, indentOrNone(dirty), strings.Join(claims, ","), id.ID)
		if !f.Bool("apply") {
			fmt.Fprintln(ctx.Stdout, "preview only; rerun with --apply to record this transfer")
			return nil
		}
		body := fmt.Sprintf("version: 1\nworktree: %s\nbranch: %s\nprior_run: %s\nprior_owner: %s\nnew_owner: %s\nclaims: %s\ntransferred_at: %s\n",
			worktreeRoot, branch, latest.runID, latest.owner, id.ID, strings.Join(claims, ","), time.Now().UTC().Format(time.RFC3339Nano))
		for _, line := range strings.Split(dirty, "\n") {
			if line != "" {
				body += "dirty: " + line + "\n"
			}
		}
		if err := writeAtomic(filepath.Join(latest.runDir, worktreeTransferFile), []byte(body)); err != nil {
			return fmt.Errorf("record worktree transfer: %w", err)
		}
		owner, ok := agentWorktreeOwner(w, worktreeRoot)
		if !ok || owner != id.ID {
			return fmt.Errorf("transfer was recorded but a newer ownership record won the race; preview and reclaim again")
		}
		fmt.Fprintf(ctx.Stdout, "reclaimed worktree for %s; audit: %s\n", id.ID, filepath.Join(latest.runDir, worktreeTransferFile))
		return nil
	}
	if !f.Bool("apply") {
		return apply()
	}
	return store.WithFileLock(filepath.Join(w.RunsDir(), ".worktree-reclaim.lock"), apply)
}

func recoveryClaims(raw string) ([]string, error) {
	var claims []string
	for _, value := range strings.Split(raw, ",") {
		value = strings.TrimSpace(value)
		clean := filepath.ToSlash(filepath.Clean(value))
		if value == "" || filepath.IsAbs(value) || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
			return nil, clikit.Usagef("invalid recovery claim %q: name a repository-relative path", value)
		}
		claims = append(claims, clean)
	}
	return claims, nil
}

func indentOrNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "  (none)"
	}
	return "  " + strings.ReplaceAll(s, "\n", "\n  ")
}

// worktreeOwnerships is fail-closed because a skipped or partially parsed run
// could be the live owner root is about to displace. Only proc.txt's durable
// terminal outcome plus a fresh OS liveness probe authorizes recovery.
func worktreeOwnerships(w *workspace.Workspace, dir string) ([]worktreeOwnership, error) {
	entries, err := os.ReadDir(w.RunsDir())
	if err != nil {
		return nil, err
	}
	wantInfo, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	wantPath, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	var out []worktreeOwnership
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		runDir := w.RunDir(entry.Name())
		raw, err := os.ReadFile(filepath.Join(runDir, "worktree.txt"))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read run %s worktree state: %w", entry.Name(), err)
		}
		candidate := filepath.Clean(strings.TrimSpace(string(raw)))
		candidatePath, absErr := filepath.Abs(candidate)
		if absErr != nil {
			return nil, fmt.Errorf("resolve run %s worktree %q: %w", entry.Name(), candidate, absErr)
		}
		candidateInfo, err := os.Stat(candidate)
		if err != nil {
			// Pruned worktrees are normal historical records. They cannot own the
			// current checkout when their normalized path differs; the matching
			// path remains fail-closed because its unreadable state is relevant.
			if candidatePath != wantPath {
				continue
			}
			return nil, fmt.Errorf("inspect run %s worktree %q: %w", entry.Name(), candidate, err)
		}
		if !os.SameFile(wantInfo, candidateInfo) {
			continue
		}
		rec, err := procmon.ReadRecord(filepath.Join(runDir, "proc.txt"))
		if err != nil || rec.RunID == "" || rec.Child == "" {
			return nil, fmt.Errorf("read run %s process state: %w", entry.Name(), err)
		}
		if procmon.AliveRecord(rec) {
			return nil, fmt.Errorf("run %s owner %s still has a live process", rec.RunID, rec.Child)
		}
		if rec.Outcome == "" {
			return nil, fmt.Errorf("run %s owner %s has no terminal outcome; inspect the native host outcome and use audited owner recovery before reclaiming ownership", rec.RunID, rec.Child)
		}
		owner := rec.Child
		if transfer, err := readWorktreeTransfer(filepath.Join(runDir, worktreeTransferFile)); err == nil {
			owner = transfer.Owner
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("read run %s transfer state: %w", rec.RunID, err)
		}
		out = append(out, worktreeOwnership{runDir: runDir, runID: rec.RunID, owner: owner})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].runID > out[j].runID })
	return out, nil
}

func readWorktreeTransfer(path string) (worktreeTransfer, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return worktreeTransfer{}, err
	}
	var transfer worktreeTransfer
	for _, line := range strings.Split(string(raw), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "new_owner":
			transfer.Owner = strings.TrimSpace(value)
		case "worktree":
			transfer.Worktree = filepath.Clean(strings.TrimSpace(value))
		case "branch":
			transfer.Branch = strings.TrimSpace(value)
		case "prior_run":
			transfer.PriorRun = strings.TrimSpace(value)
		case "claims":
			transfer.Claims, err = recoveryClaims(strings.TrimSpace(value))
			if err != nil {
				return worktreeTransfer{}, err
			}
		}
	}
	if transfer.Owner == "" || len(transfer.Claims) == 0 || transfer.Worktree == "" || transfer.Branch == "" {
		return worktreeTransfer{}, fmt.Errorf("missing new_owner, claims, worktree, or branch")
	}
	return transfer, nil
}

func writeAtomic(path string, raw []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".worktree-transfer-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if err := f.Chmod(0o644); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(raw); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// agentWorktreeOwner returns the child recorded for the newest run using dir
// as its isolated worktree. worktree.txt is written before the runtime starts,
// and unlike process liveness it remains useful through the child's final
// commit, so attribution does not race the guardian's terminal transition.
func agentWorktreeOwner(w *workspace.Workspace, dir string) (string, bool) {
	entries, err := os.ReadDir(w.RunsDir())
	if err != nil {
		return "", false
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	wantInfo, wantErr := os.Stat(dir)
	for _, name := range names {
		runDir := w.RunDir(name)
		raw, err := os.ReadFile(filepath.Join(runDir, "worktree.txt"))
		if err != nil {
			continue
		}
		candidate := filepath.Clean(strings.TrimSpace(string(raw)))
		candidateInfo, candidateErr := os.Stat(candidate)
		if wantErr != nil || candidateErr != nil || !os.SameFile(wantInfo, candidateInfo) {
			continue
		}
		rec, err := procmon.ReadRecord(filepath.Join(runDir, "proc.txt"))
		owner := rec.Child
		if err != nil || owner == "" {
			// invocation.txt is committed before the provider process starts,
			// while proc.txt can only gain its PID after Start returns. A very
			// fast child can invoke `dacli commit` in that interval. Treat the
			// exact run/child launch intent as ownership so it cannot fall back
			// to an older root-recovery transfer (PR #903 clean-checkout).
			owner, _ = invocationRunOwner(runDir, name)
		}
		if owner != "" {
			if transfer, transferErr := readWorktreeTransfer(filepath.Join(runDir, worktreeTransferFile)); transferErr == nil {
				return transfer.Owner, true
			}
			return owner, true
		}
	}
	return "", false
}

func invocationRunOwner(runDir, wantRunID string) (string, bool) {
	raw, err := os.ReadFile(filepath.Join(runDir, "invocation.txt"))
	if err != nil {
		return "", false
	}
	var runID, child string
	for _, line := range strings.Split(string(raw), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "run":
			runID = strings.TrimSpace(value)
		case "child":
			child = strings.TrimSpace(value)
		}
	}
	return child, runID == wantRunID && child != ""
}

type commitClaim struct {
	paths      []string
	provenance string
}

// agentClaims returns the claim that governs this commit context. Recovery
// transfers are durable attribution records, so owner alone cannot make every
// historical root transfer a permanent global fence (task 494): its recorded
// checkout must match, and its branch or originating task must still describe
// the commit. This preserves restart recovery while excluding pruned and later
// unrelated worktrees from authorization.
func agentClaims(w *workspace.Workspace, child, worktree, branch, task string) (commitClaim, bool) {
	entries, err := os.ReadDir(w.RunsDir())
	if err != nil {
		return commitClaim{}, false
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names))) // ULIDs: newest first
	for _, n := range names {
		rec, err := procmon.ReadRecord(filepath.Join(w.RunDir(n), "proc.txt"))
		if err != nil {
			continue
		}
		if transfer, transferErr := readWorktreeTransfer(filepath.Join(w.RunDir(n), worktreeTransferFile)); transferErr == nil && transfer.Owner == child &&
			sameWorktree(transfer.Worktree, worktree) && (transfer.Branch == branch || (task != "" && rec.Task == task)) {
			return commitClaim{
				paths: transfer.Claims,
				provenance: fmt.Sprintf("current recovery transfer run %s for worktree %s on branch %s (task %s)",
					n, transfer.Worktree, transfer.Branch, clikit.OrDash(rec.Task)),
			}, true
		}
		// A child identity can outlive one task, and root necessarily does. A
		// claim from an earlier correction turn is therefore not authority over
		// every later checkout used by the same identity (issue #811). Bind a
		// spawn claim to its recorded task and, when the run has an isolated
		// worktree, to that checkout as well.
		if rec.Child == child && len(rec.Claims) > 0 && runClaimMatchesCommit(w.RunDir(n), rec, worktree, task) {
			return commitClaim{paths: rec.Claims, provenance: "spawn claim from run " + n}, true
		}
	}

	// A root-owned task worktree does not necessarily have a spawn record: an
	// operator can claim a task and create its checkout directly. The task is
	// already the durable, reviewable source from which loop spawns infer their
	// claim. Use that same inference only when both the canonical task branch
	// and canonical task worktree prove this commit belongs to the task. This
	// replaces stale historical authority without silently widening it.
	if task != "" {
		if t, findErr := store.FindTask(w, task); findErr == nil && branch == BranchFor(t) && sameWorktree(w.WorktreePath(t.Project, t.Seq, t.Slug), worktree) {
			if claims := store.ClaimHints(w.Root, t); len(claims) > 0 {
				return commitClaim{paths: claims, provenance: fmt.Sprintf("inferred claim from task %03d-%s in its canonical worktree", t.Seq, t.Slug)}, true
			}
		}
	}
	return commitClaim{}, false
}

func runClaimMatchesCommit(runDir string, rec procmon.Record, worktree, task string) bool {
	if task != "" && rec.Task != task {
		return false
	}
	raw, err := os.ReadFile(filepath.Join(runDir, "worktree.txt"))
	if errors.Is(err, fs.ErrNotExist) {
		// Non-isolated runs have no worktree record. Their task identity is the
		// remaining scope proof; with no --task, preserve the legacy child-token
		// behavior for callers that have no stronger commit context.
		return task == "" || rec.Task == task
	}
	if err != nil {
		return false
	}
	return sameWorktree(strings.TrimSpace(string(raw)), worktree)
}

func sameWorktree(recorded, current string) bool {
	recordedAbs, recordedErr := filepath.Abs(recorded)
	currentAbs, currentErr := filepath.Abs(current)
	if recordedErr != nil || currentErr != nil {
		return false
	}
	recordedInfo, recordedErr := os.Stat(recordedAbs)
	currentInfo, currentErr := os.Stat(currentAbs)
	return recordedErr == nil && currentErr == nil && os.SameFile(recordedInfo, currentInfo)
}

// inClaimScope reports whether a staged path is within the agent's declared
// scope. Anything under .dacli/ (the task's own file plus workspace crumbs) is
// always in scope — we do not fight the workspace record. Every other path is
// a code file, in scope only when it overlaps a claimed path (claim is the
// file, or a path-segment prefix of it — the same rule two live agents use to
// detect a clash).
func inClaimScope(p string, claims []string) bool {
	clean := strings.Trim(strings.TrimSpace(p), "/")
	if clean == ".dacli" || strings.HasPrefix(clean, ".dacli/") {
		return true
	}
	_, _, overlap := procmon.PathsOverlap([]string{clean}, claims)
	return overlap
}

// isAgentAuthor reports whether a git author line names a dacli agent. Keying
// on the ID rather than on the "(role)" suffix (dacli 225) is what makes a
// ROLELESS agent's lines still count as agent-authored — with readable ids the
// role is usually in the id itself, and the old suffix-only heuristic silently
// attributed those lines to a human.
func isAgentAuthor(who string) bool {
	id, rest, _ := strings.Cut(who, " ")
	if !agentid.IsID(id) {
		return false
	}
	return rest == "" || strings.HasPrefix(rest, "(")
}

// cmdBlame answers "who wrote each line, in what role" — the reviewer's tool.
// Author names already carry the role, so a summary over `git blame` is
// enough; no trailer parsing needed for the common case.
func cmdBlame(ctx *clikit.Ctx, args []string) error {
	if _, _, err := clikit.OpenWorkspace(ctx); err != nil {
		return err
	}
	f, _ := clikit.ParseFlags(args)
	// Reject unknown flags: a typo used to be dropped silently and the
	// command ran as if the caller had meant the default.
	if err := f.Reject(); err != nil {
		return err
	}
	if len(f.Pos) == 0 {
		return clikit.Usagef("usage: dacli blame <file>")
	}
	out, err := gitIn(ctx.Cwd, "blame", "--line-porcelain", f.Pos[0])
	if err != nil {
		return fmt.Errorf("git blame: %s", out)
	}
	lines := map[string]int{}
	agents := map[string]bool{}
	total := 0
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "author ") {
			who := strings.TrimPrefix(l, "author ")
			lines[who]++
			total++
			if isAgentAuthor(who) {
				agents[who] = true
			}
		}
	}
	type row struct {
		who   string
		count int
	}
	var rows []row
	for who, n := range lines {
		rows = append(rows, row{who, n})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].count > rows[j].count })
	for _, r := range rows {
		mark := " "
		if isAgentAuthor(r.who) {
			mark = "*"
		}
		fmt.Fprintf(ctx.Stdout, "%s %5d lines (%4.1f%%)  %s\n", mark, r.count, 100*float64(r.count)/float64(total), r.who)
	}
	fmt.Fprintf(ctx.Stdout, "%d lines · %d dacli agent(s) touched this file (* = agent-authored)\n", total, len(agents))
	return nil
}

// cmdContrib is the self-evolving-team surface: which roles and agents did
// how much, read from commit events (no git needed). Pair it with the
// findings each agent's work drew to see which role needs improving.
func cmdContrib(ctx *clikit.Ctx, args []string) error {
	// This command takes no flags, so ANY flag is a typo. An empty allowlist
	// rejects every one — without it a mistyped flag was dropped and the
	// command ran as if nothing were wrong.
	if f, ferr := clikit.ParseFlags(args); ferr != nil {
		return ferr
	} else if err := f.Reject(); err != nil {
		return err
	}
	w, _, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	// One kind-filtered walk yields both commits and findings; splitting them
	// in memory halves the event-tree I/O versus two full List scans.
	events, err := eventlog.List(w, eventlog.Query{Kinds: []model.EventKind{model.EventCommit, model.EventFinding}})
	if err != nil {
		return err
	}
	commitsBy := map[string]int{}
	// The improvement join, now real: a reviewer files a finding --against
	// <agent-id> (from dacli blame). Count those per agent whose work drew
	// them — the signal for which role produces which class of defect.
	againstBy := map[string]int{}
	// Role of every agent — from the agent files, so an agent with findings
	// against it but no commits still resolves to a role.
	roleOf := map[string]string{}
	for _, a := range mustAgents(w) {
		roleOf[a.ID] = a.Role
	}

	commitCount := 0
	for _, e := range events {
		switch e.Kind {
		case model.EventCommit:
			commitCount++
			commitsBy[e.Actor]++
			for _, l := range strings.Split(e.Body, "\n") {
				if strings.HasPrefix(l, "role: ") && roleOf[e.Actor] == "" {
					roleOf[e.Actor] = strings.TrimPrefix(l, "role: ")
				}
			}
		case model.EventFinding:
			// Count an against only while the finding lives SOLELY as an event.
			// Once the owner syncs it (applied), it also exists as a NoteFinding
			// counted in the notes loop below — counting both double-counts a
			// read-only reviewer's finding (event now, synced note later) while an
			// rw reviewer's finding (a direct note, no event) counts once. Gate on
			// !e.Applied so every finding is counted exactly once, synced or not.
			if e.Against != "" && !e.Applied {
				againstBy[e.Against]++
			}
		}
	}
	if commitCount == 0 {
		fmt.Fprintln(ctx.Stdout, "no attributed commits yet — agents commit with `dacli commit`")
		return nil
	}
	if ps, _ := store.ListProjects(w); ps != nil {
		for _, p := range ps {
			notes, _ := store.ListNotes(w, p.Slug, model.NoteFinding)
			for _, n := range notes {
				if ag, _ := n.Front.Get("against"); ag != "" {
					againstBy[ag]++
				}
			}
		}
	}

	// Roll up to roles.
	roleCommits := map[string]int{}
	roleAgainst := map[string]int{}
	everyone := map[string]bool{}
	for a := range commitsBy {
		everyone[a] = true
	}
	for a := range againstBy {
		everyone[a] = true
	}
	for a := range everyone {
		r := clikit.OrDash(roleOf[a])
		roleCommits[r] += commitsBy[a]
		roleAgainst[r] += againstBy[a]
	}

	fmt.Fprintln(ctx.Stdout, "by role  (commits · findings-against · defect rate):")
	for _, r := range sortedKeys(roleCommits) {
		fmt.Fprintf(ctx.Stdout, "  %-14s %d commit(s) · %d finding(s)-against%s\n",
			r, roleCommits[r], roleAgainst[r], rate(roleAgainst[r], roleCommits[r]))
	}
	fmt.Fprintln(ctx.Stdout, "by agent:")
	agents := make([]string, 0, len(everyone))
	for a := range everyone {
		agents = append(agents, a)
	}
	sort.Slice(agents, func(i, j int) bool { return commitsBy[agents[i]] > commitsBy[agents[j]] })
	for _, a := range agents {
		fmt.Fprintf(ctx.Stdout, "  %-16s %-12s %d commit(s) · %d finding(s)-against\n",
			a, "("+clikit.OrDash(roleOf[a])+")", commitsBy[a], againstBy[a])
	}
	fmt.Fprintln(ctx.Stdout, "(a high defect rate for a role is where to focus improvement — better prompts, tighter scope, or a heavier model)")
	return nil
}

// rate renders a defect rate only when there is enough to mean anything.
func rate(against, commits int) string {
	if commits == 0 || against == 0 {
		return ""
	}
	return fmt.Sprintf(" · %.1f per commit", float64(against)/float64(commits))
}

func mustAgents(w *workspace.Workspace) []store.AgentInfo {
	a, _ := store.ListAgents(w)
	return a
}

func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		if m[out[i]] != m[out[j]] {
			return m[out[i]] > m[out[j]]
		}
		return out[i] < out[j]
	})
	return out
}
