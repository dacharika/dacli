package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mlnomadpy/dacli/internal/gitx"
	"github.com/mlnomadpy/dacli/internal/mdstore"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/procmon"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

const CleanupPlanSchema = "repository-cleanup/v2"

const cleanupPlanIDToken = "{plan-id}"

// CleanupItem is one managed worktree/branch pair. Eligible is deliberately
// derived from every proof below; absence or failure of any proof preserves it.
type CleanupItem struct {
	Worktree   string       `json:"worktree"`
	Branch     string       `json:"branch"`
	Task       string       `json:"task,omitempty"`
	TaskStatus string       `json:"task_status,omitempty"`
	Owner      string       `json:"owner,omitempty"`
	Runs       []CleanupRun `json:"runs,omitempty"`
	PRHistory  []CleanupPR  `json:"pr_history,omitempty"`
	PRState    string       `json:"pr_state"`
	Commit     string       `json:"commit,omitempty"`
	Protected  bool         `json:"protected"`
	Dirty      bool         `json:"dirty"`
	Unpushed   bool         `json:"unpushed"`
	Unknown    bool         `json:"unknown"`
	Eligible   bool         `json:"eligible"`
	Reasons    []string     `json:"reasons"`
	Operations []string     `json:"operations,omitempty"`
	Recovery   []string     `json:"recovery,omitempty"`
}

type CleanupRun struct {
	ID     string   `json:"id"`
	Agent  string   `json:"agent,omitempty"`
	State  string   `json:"state"`
	Claims []string `json:"claims,omitempty"`
}

type CleanupPR struct {
	Number int    `json:"number"`
	State  string `json:"state"`
}

type CleanupArtifact struct {
	Path           string `json:"path"`
	RunID          string `json:"run_id"`
	Task           string `json:"task,omitempty"`
	Classification string `json:"classification"`
	Pruneable      bool   `json:"pruneable"`
	Identity       string `json:"identity,omitempty"`
	Digest         string `json:"digest,omitempty"`
	Size           int64  `json:"size,omitempty"`
	Mode           uint32 `json:"mode,omitempty"`
	Quarantine     string `json:"quarantine,omitempty"`
	Operation      string `json:"operation,omitempty"`
	Recovery       string `json:"recovery,omitempty"`
	Reason         string `json:"reason"`
}

// CleanupPlan is a content-addressed, versioned observation. ObservedAt is
// informational and intentionally excluded from ID so an unchanged rerun can
// apply exactly the plan the operator reviewed.
type CleanupPlan struct {
	Schema        string            `json:"schema"`
	Version       int               `json:"version"`
	ID            string            `json:"id"`
	Project       string            `json:"project"`
	Base          string            `json:"base"`
	BaseCommit    string            `json:"base_commit"`
	CurrentBranch string            `json:"current_branch"`
	ObservedAt    time.Time         `json:"observed_at"`
	Items         []CleanupItem     `json:"items"`
	Artifacts     []CleanupArtifact `json:"artifacts"`
}

// PlanRepositoryCleanup reuses ReclaimableWorktrees as the canonical local
// worktree classifier, then narrows it with remote and terminal evidence.
// Cleanup is never allowed to be more permissive than worktree prune.
func PlanRepositoryCleanup(w *workspace.Workspace, project string, now time.Time, protect ...string) (CleanupPlan, error) {
	p := CleanupPlan{Schema: CleanupPlanSchema, Version: 2, Project: project, ObservedAt: now.UTC(), Items: []CleanupItem{}, Artifacts: []CleanupArtifact{}}
	prj, err := LoadProject(w, project)
	if err != nil {
		return p, err
	}
	p.Base = prj.Landing.Base
	if p.Base == "" {
		p.Base = "main"
	}
	baseCommit, err := gitx.Run(w.Root, "rev-parse", p.Base)
	if err != nil {
		return p, fmt.Errorf("observe configured base %s: %w", p.Base, err)
	}
	p.BaseCommit = strings.TrimSpace(baseCommit)
	prs, ghErr := ObserveDeliveryPRs(w.Root)
	wts, wtErr := gitx.ListWorktrees(w.Root)
	if wtErr != nil {
		return p, fmt.Errorf("observe worktrees: %w", wtErr)
	}
	reclaimable, reclaimErr := ReclaimableWorktrees(w, p.Base)
	reclaim := map[string]ReclaimableWorktree{}
	if reclaimErr == nil {
		for _, c := range reclaimable {
			reclaim[cleanPath(c.Path)] = c
		}
	}
	tasks, taskErr := ListTasks(w, project, "")
	byBranch := map[string]*Task{}
	byCommit := map[string][]*Task{}
	taskByID := map[string]*Task{}
	if taskErr == nil {
		for _, t := range tasks {
			byBranch[TaskBranch(t)] = t
			taskByID[t.ID] = t
			if tip, tipErr := gitx.Run(w.Root, "rev-parse", "--verify", TaskBranch(t)); tipErr == nil {
				commit := strings.TrimSpace(tip)
				byCommit[commit] = append(byCommit[commit], t)
			}
		}
	}
	runsByTask, artifacts, runsErr := observeCleanupRuns(w, project, taskByID)
	p.Artifacts = artifacts
	if runsErr != nil {
		for i := range p.Artifacts {
			if p.Artifacts[i].Pruneable {
				p.Artifacts[i].Pruneable = false
				p.Artifacts[i].Reason = "generated artifact preserved because run evidence is unreadable: " + runsErr.Error()
				p.Artifacts[i].Quarantine, p.Artifacts[i].Operation, p.Artifacts[i].Recovery = "", "", ""
			}
		}
	}
	current := gitx.CurrentBranch(w.Root)
	p.CurrentBranch = current
	protectedPaths := map[string]bool{cleanPath(w.Root): true}
	for _, path := range protect {
		if path != "" {
			protectedPaths[cleanPath(path)] = true
		}
	}
	managedRoot := cleanPath(w.WorktreesDir())
	for _, wt := range wts {
		path := cleanPath(wt.Path)
		if path != managedRoot && !strings.HasPrefix(path, managedRoot+string(filepath.Separator)) {
			continue
		}
		detached := wt.Detached && wt.Branch == ""
		item := CleanupItem{Worktree: wt.Path, Branch: wt.Branch, PRState: "missing", Reasons: []string{}}
		if t := byBranch[wt.Branch]; t != nil {
			item.Task, item.TaskStatus = t.ID, string(t.Status)
			item.Owner, item.Runs = t.Owner(), runsByTask[t.ID]
		}
		if detached && wt.Head != "" {
			for _, t := range byCommit[wt.Head] {
				// A non-terminal task at this exact commit is an ownership record.
				// Prefer it over a historical done task so cleanup fails closed.
				if item.Task == "" || t.Status != model.StatusDone {
					item.Task, item.TaskStatus = t.ID, string(t.Status)
					item.Owner, item.Runs = t.Owner(), runsByTask[t.ID]
				}
			}
		}
		if runsErr != nil {
			item.Unknown = true
			item.Reasons = append(item.Reasons, "run/claim evidence is unobservable: "+runsErr.Error())
		}
		item.Protected = protectedPaths[path] || wt.Branch == current || wt.Branch == p.Base || wt.Locked
		if item.Protected {
			item.Reasons = append(item.Reasons, "protected current/base worktree")
		}
		dirty, dirtyErr := gitx.DirtyPaths(wt.Path)
		item.Dirty = dirtyErr != nil || len(dirty) > 0
		if item.Dirty {
			item.Reasons = append(item.Reasons, "dirty or untracked worktree")
		}
		commit, commitErr := wt.Head, error(nil)
		if commit == "" {
			commit, commitErr = gitx.Run(w.Root, "rev-parse", wt.Branch)
		}
		item.Commit = strings.TrimSpace(commit)
		if detached {
			contained, containedErr := gitx.IsAncestor(w.Root, item.Commit, p.BaseCommit)
			if containedErr != nil || !contained {
				item.Unknown = containedErr != nil
				item.Unpushed = containedErr == nil
				item.Reasons = append(item.Reasons, "detached HEAD is not proven contained in configured base")
			}
			if item.Task != "" && item.TaskStatus != string(model.StatusDone) {
				item.Reasons = append(item.Reasons, "detached HEAD has non-terminal task ownership")
			}
			if containedErr == nil && contained {
				item.PRState = "contained"
			}
		}
		remoteHead := ""
		if detached {
			remoteHead = item.Commit
		} else if ghErr != nil {
			item.Unknown = true
			item.PRState = "unknown"
			item.Reasons = append(item.Reasons, "GitHub PR state is unobservable")
		} else {
			var matches []DeliveryPR
			for _, pr := range prs {
				if pr.HeadRefName == wt.Branch {
					matches = append(matches, pr)
				}
			}
			if len(matches) > 0 {
				sort.Slice(matches, func(i, j int) bool { return matches[i].Number < matches[j].Number })
				canonicalPR := matches[len(matches)-1]
				for i, pr := range matches {
					state := cleanupPRState(pr)
					if i < len(matches)-1 {
						state = "superseded"
					}
					item.PRHistory = append(item.PRHistory, CleanupPR{Number: pr.Number, State: state})
				}
				if len(matches) > 1 && matches[len(matches)-2].Number == canonicalPR.Number {
					item.Unknown, item.PRState = true, "ambiguous"
					item.Reasons = append(item.Reasons, "multiple canonical PR observations share the latest number")
				} else {
					item.PRState = cleanupPRState(canonicalPR)
					remoteHead = canonicalPR.HeadRefOid
				}
			}
		}
		// The PR head OID is the canonical pushed snapshot and remains useful
		// after GitHub deletes the merged remote branch. Fall back to the local
		// remote-tracking ref only when GitHub omitted it; any failure preserves.
		if !detached && remoteHead == "" {
			remoteCommit, remoteErr := gitx.Run(w.Root, "rev-parse", "refs/remotes/origin/"+wt.Branch)
			if remoteErr != nil {
				item.Unknown = true
				item.Reasons = append(item.Reasons, "remote branch is unobservable")
			} else {
				remoteHead = strings.TrimSpace(remoteCommit)
			}
		}
		item.Unpushed = item.Unpushed || (commitErr == nil && remoteHead != "" && remoteHead != item.Commit)
		if item.Unpushed {
			item.Reasons = append(item.Reasons, "branch contains unpushed commits")
		}
		if !detached && item.PRState != "merged" {
			item.Reasons = append(item.Reasons, "PR state is "+item.PRState)
		}
		if !detached && (item.Task == "" || item.TaskStatus != string(model.StatusDone)) {
			item.Reasons = append(item.Reasons, "task is missing or non-terminal")
		}
		runsTerminal := true
		for _, run := range item.Runs {
			if run.State != "terminal" || len(run.Claims) > 0 {
				runsTerminal = false
				item.Unknown = item.Unknown || run.State == "unknown"
				item.Reasons = append(item.Reasons, "run "+run.ID+" is "+run.State+" or still owns claims")
			}
		}
		canonical := reclaim[path]
		if !detached && !canonical.Merged {
			item.Reasons = append(item.Reasons, "canonical worktree classifier has not proven a merge")
		}
		if commitErr != nil || reclaimErr != nil || taskErr != nil {
			item.Unknown = true
			item.Reasons = append(item.Reasons, "local cleanup evidence is incomplete")
		}
		item.Reasons = uniqueStrings(item.Reasons)
		detachedOwnedSafely := item.Task == "" || item.TaskStatus == string(model.StatusDone)
		item.Eligible = !item.Protected && !item.Dirty && !item.Unpushed && !item.Unknown && runsTerminal && ((detached && item.PRState == "contained" && detachedOwnedSafely) || (!detached && item.PRState == "merged" && item.TaskStatus == string(model.StatusDone) && canonical.Merged))
		if item.Eligible {
			if detached {
				item.Reasons = []string{"detached HEAD is contained in base, clean, non-protected, and has no live ownership"}
				item.Operations = []string{"git worktree remove -- " + wt.Path}
				item.Recovery = []string{"git worktree add --detach <path> " + item.Commit}
			} else {
				item.Reasons = []string{"merged, clean, pushed, non-protected, terminal task"}
				item.Operations = []string{"git worktree remove -- " + wt.Path, "git branch -d -- " + wt.Branch}
				item.Recovery = []string{"git branch recovered/" + safeRecoveryRef(wt.Branch) + " " + item.Commit, "git worktree add <path> " + item.Commit}
			}
		}
		p.Items = append(p.Items, item)
	}
	sort.Slice(p.Items, func(i, j int) bool { return p.Items[i].Worktree < p.Items[j].Worktree })
	sort.Slice(p.Artifacts, func(i, j int) bool { return p.Artifacts[i].Path < p.Artifacts[j].Path })
	p.ID = cleanupPlanID(p)
	materializeCleanupArtifactTargets(&p)
	return p, nil
}

func cleanupPRState(pr DeliveryPR) string {
	state := strings.ToLower(pr.DeliveryConfidence)
	if state == "closed" && pr.MergeCommit != nil {
		return "merged"
	}
	return state
}

func observeCleanupRuns(w *workspace.Workspace, project string, tasks map[string]*Task) (map[string][]CleanupRun, []CleanupArtifact, error) {
	byTask := map[string][]CleanupRun{}
	var artifacts []CleanupArtifact
	entries, err := os.ReadDir(w.RunsDir())
	if err != nil {
		if os.IsNotExist(err) {
			return byTask, artifacts, nil
		}
		return byTask, artifacts, fmt.Errorf("read runs directory: %w", err)
	}
	var observeErr error
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		// PRPublicationPath owns this reserved directory for restart checkpoints,
		// not worker processes. Requiring its nonexistent proc.txt vetoed every
		// otherwise terminal cleanup candidate. Keep its contents as metadata;
		// arbitrary unknown run directories still require process evidence.
		if entry.Name() == "pr-publications" {
			metadataDir := filepath.Join(w.RunsDir(), entry.Name())
			files, metadataErr := os.ReadDir(metadataDir)
			if metadataErr != nil {
				observeErr = fmt.Errorf("read publication checkpoint metadata: %w", metadataErr)
				continue
			}
			for _, file := range files {
				artifacts = append(artifacts, CleanupArtifact{
					Path:           filepath.Join(metadataDir, file.Name()),
					Classification: "durable-publication-metadata",
					Reason:         "reserved PR publication checkpoints are retained as durable metadata",
				})
			}
			continue
		}
		runID, runDir := entry.Name(), w.RunDir(entry.Name())
		rec, recErr := procmon.ReadRecord(filepath.Join(runDir, "proc.txt"))
		if recErr == nil && (rec.RunID != runID || rec.Task == "") {
			recErr = fmt.Errorf("missing or mismatched run/task identity")
		}
		run := CleanupRun{ID: runID, State: "unknown"}
		if recErr == nil {
			run.Agent, run.Claims = rec.Child, append([]string(nil), rec.Claims...)
			switch {
			case rec.Outcome != "" && len(rec.Claims) == 0:
				run.State = "terminal"
			case procmon.AliveIdentity(rec.PID, rec.PIDStart) || len(rec.Claims) > 0:
				run.State = "live"
			}
			byTask[rec.Task] = append(byTask[rec.Task], run)
		} else {
			observeErr = fmt.Errorf("read run %s process evidence: %w", runID, recErr)
		}
		files, fileErr := os.ReadDir(runDir)
		if fileErr != nil {
			observeErr = fmt.Errorf("read run %s artifacts: %w", runID, fileErr)
			continue
		}
		for _, file := range files {
			if file.IsDir() {
				continue
			}
			a := CleanupArtifact{Path: filepath.Join(runDir, file.Name()), RunID: runID, Task: rec.Task, Classification: "durable-evidence", Reason: "run records and transcripts are retained as durable evidence"}
			if isGeneratedCleanupArtifact(file.Name()) && recErr == nil && run.State == "terminal" && file.Type().IsRegular() {
				if t := tasks[rec.Task]; t != nil && t.Status == model.StatusDone {
					info, infoErr := file.Info()
					digest, digestErr := cleanupArtifactDigest(a.Path)
					if infoErr != nil || digestErr != nil {
						if infoErr != nil {
							observeErr = fmt.Errorf("read run %s artifact identity: %w", runID, infoErr)
						} else {
							observeErr = fmt.Errorf("read run %s artifact identity: %w", runID, digestErr)
						}
						a.Reason = "generated artifact preserved because its identity is unreadable"
					} else {
						a.Classification, a.Pruneable = "generated-run-artifact", true
						a.Digest, a.Size, a.Mode = digest, info.Size(), uint32(info.Mode().Perm())
						a.Identity = cleanupArtifactIdentity(a.Path, runID, digest, a.Size, a.Mode)
						a.Quarantine = filepath.Join(w.Root, workspace.Dir, "quarantine", "cleanup", cleanupPlanIDToken, runID, a.Identity+"-"+file.Name())
						a.Operation = "move -- " + a.Path + " -> " + a.Quarantine
						a.Recovery = "dacli cleanup --project " + project + " --restore " + cleanupPlanIDToken + " --artifact " + a.Identity
						a.Reason = "terminal task and run with released claims; quarantine is recoverable and no durable evidence is removed"
					}
				}
			}
			artifacts = append(artifacts, a)
		}
	}
	return byTask, artifacts, observeErr
}

func isGeneratedCleanupArtifact(name string) bool {
	lower := strings.ToLower(name)
	if !strings.HasSuffix(lower, ".tmp") {
		return false
	}
	for _, durable := range []string{"proc", "outcome", "transcript", "verification", "verify", "invocation", "usage", "brief", "worktree", "runtime-exit", "killed", "timeout", "blocked", "provider-outcome"} {
		if lower == durable+".tmp" || strings.HasPrefix(lower, durable+"-") || strings.HasPrefix(lower, durable+".") {
			return false
		}
	}
	return true
}

func cleanupPlanID(p CleanupPlan) string {
	planID := p.ID
	p.ID = ""
	p.ObservedAt = time.Time{}
	p.Artifacts = append([]CleanupArtifact(nil), p.Artifacts...)
	for i := range p.Artifacts {
		for _, target := range []*string{&p.Artifacts[i].Quarantine, &p.Artifacts[i].Operation, &p.Artifacts[i].Recovery} {
			if planID != "" {
				*target = strings.ReplaceAll(*target, planID, cleanupPlanIDToken)
			}
		}
	}
	raw, _ := json.Marshal(p)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func materializeCleanupArtifactTargets(p *CleanupPlan) {
	for i := range p.Artifacts {
		for _, target := range []*string{&p.Artifacts[i].Quarantine, &p.Artifacts[i].Operation, &p.Artifacts[i].Recovery} {
			*target = strings.ReplaceAll(*target, cleanupPlanIDToken, p.ID)
		}
	}
}

func cleanupArtifactDigest(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func cleanupArtifactIdentity(path, runID, digest string, size int64, mode uint32) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s\x00%d\x00%d", filepath.Clean(path), runID, digest, size, mode)))
	return hex.EncodeToString(sum[:])
}

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func safeRecoveryRef(branch string) string {
	r := strings.NewReplacer("/", "-", "\\", "-", " ", "-").Replace(branch)
	return strings.Trim(r, "-")
}

type CleanupAudit struct {
	Schema           string                  `json:"schema"`
	PlanID           string                  `json:"plan_id"`
	Project          string                  `json:"project"`
	AppliedAt        time.Time               `json:"applied_at"`
	Planned          []CleanupItem           `json:"planned"`
	PlannedArtifacts []CleanupArtifact       `json:"planned_artifacts"`
	Removed          []CleanupItem           `json:"removed"`
	Quarantined      []CleanupArtifact       `json:"quarantined_artifacts"`
	Restored         []CleanupArtifact       `json:"restored_artifacts,omitempty"`
	Operations       []CleanupOperationAudit `json:"completed_operations"`
}

type CleanupOperationAudit struct {
	Operation   string    `json:"operation"`
	Target      string    `json:"target"`
	Source      string    `json:"source,omitempty"`
	Identity    string    `json:"identity,omitempty"`
	Digest      string    `json:"digest,omitempty"`
	Recovery    string    `json:"recovery,omitempty"`
	CompletedAt time.Time `json:"completed_at"`
}

var (
	removeCleanupWorktree  = gitx.RemoveCleanWorktree
	removeCleanupBranch    = func(root, branch string) error { _, err := gitx.Run(root, "branch", "-d", "--", branch); return err }
	moveCleanupArtifact    = os.Rename
	restoreCleanupArtifact = func(source, target string) error {
		// Link creates target with O_EXCL-like no-overwrite semantics. Both paths
		// are workspace-owned .dacli entries on the same filesystem.
		if err := os.Link(source, target); err != nil {
			return err
		}
		if err := os.Remove(source); err != nil {
			_ = os.Remove(target)
			return err
		}
		return nil
	}
)

// ApplyRepositoryCleanup executes only the operations enumerated by a fresh,
// matching plan. Worktrees are removed through git, never recursive deletion;
// branches use non-forcing -d and retain their exact recovery commit in audit.
func ApplyRepositoryCleanup(w *workspace.Workspace, project, requestedID string, now time.Time, protect ...string) (CleanupAudit, error) {
	var audit CleanupAudit
	err := WithFileLock(filepath.Join(w.Root, workspace.Dir, ".cleanup.lock"), func() error {
		p, err := PlanRepositoryCleanup(w, project, now, protect...)
		if err != nil {
			return err
		}
		if requestedID == "" || requestedID != p.ID || cleanupPlanID(p) != p.ID {
			return fmt.Errorf("cleanup plan is stale or unknown; review a new dry-run")
		}
		for _, item := range p.Items {
			if item.Eligible {
				audit.Planned = append(audit.Planned, item)
			}
		}
		for _, artifact := range p.Artifacts {
			if artifact.Pruneable {
				audit.PlannedArtifacts = append(audit.PlannedArtifacts, artifact)
			}
		}
		audit.Schema, audit.PlanID, audit.Project, audit.AppliedAt = "repository-cleanup-audit/v2", p.ID, p.Project, now.UTC()
		audit.Removed = []CleanupItem{}
		audit.Quarantined = []CleanupArtifact{}
		auditPath, err := writeCleanupAudit(w, audit)
		if err != nil {
			return err // prove the recovery ledger is writable before mutation
		}
		for _, artifact := range audit.PlannedArtifacts {
			if err := validateCleanupArtifactPaths(w, artifact, false); err != nil {
				return fmt.Errorf("quarantine artifact %s (audit %s): %w", artifact.Path, auditPath, err)
			}
			if err := verifyCleanupArtifactSource(artifact); err != nil {
				return fmt.Errorf("quarantine artifact %s (audit %s): %w", artifact.Path, auditPath, err)
			}
			if err := os.MkdirAll(filepath.Dir(artifact.Quarantine), 0o700); err != nil {
				return fmt.Errorf("prepare artifact quarantine %s (audit %s): %w", artifact.Quarantine, auditPath, err)
			}
			if err := validateCleanupArtifactPaths(w, artifact, false); err != nil {
				return fmt.Errorf("revalidate artifact quarantine %s (audit %s): %w", artifact.Quarantine, auditPath, err)
			}
			if _, err := os.Lstat(artifact.Quarantine); err == nil {
				return fmt.Errorf("quarantine target already exists %s (audit %s)", artifact.Quarantine, auditPath)
			} else if !os.IsNotExist(err) {
				return fmt.Errorf("inspect quarantine target %s (audit %s): %w", artifact.Quarantine, auditPath, err)
			}
			if err := moveCleanupArtifact(artifact.Path, artifact.Quarantine); err != nil {
				return fmt.Errorf("quarantine artifact %s (audit %s): %w", artifact.Path, auditPath, err)
			}
			audit.Quarantined = append(audit.Quarantined, artifact)
			audit.Operations = append(audit.Operations, CleanupOperationAudit{
				Operation: "artifact-quarantine", Source: artifact.Path, Target: artifact.Quarantine,
				Identity: artifact.Identity, Digest: artifact.Digest, Recovery: artifact.Recovery, CompletedAt: time.Now().UTC(),
			})
			if _, err := writeCleanupAudit(w, audit); err != nil {
				return fmt.Errorf("record quarantined artifact in %s: %w", auditPath, err)
			}
		}
		for _, item := range audit.Planned {
			if err := removeCleanupWorktree(w.Root, item.Worktree); err != nil {
				return fmt.Errorf("remove worktree %s (audit %s): %w", item.Worktree, auditPath, err)
			}
			audit.Operations = append(audit.Operations, CleanupOperationAudit{Operation: "worktree-remove", Target: item.Worktree, CompletedAt: time.Now().UTC()})
			if _, err := writeCleanupAudit(w, audit); err != nil {
				return fmt.Errorf("record removed worktree in %s: %w", auditPath, err)
			}
			if item.Branch != "" {
				if err := removeCleanupBranch(w.Root, item.Branch); err != nil {
					return fmt.Errorf("remove merged branch %s (worktree recovery recorded in %s): %w", item.Branch, auditPath, err)
				}
				audit.Operations = append(audit.Operations, CleanupOperationAudit{Operation: "branch-delete", Target: item.Branch, CompletedAt: time.Now().UTC()})
			}
			audit.Removed = append(audit.Removed, item)
			if _, err := writeCleanupAudit(w, audit); err != nil {
				return fmt.Errorf("record completed cleanup in %s: %w", auditPath, err)
			}
		}
		_, err = writeCleanupAudit(w, audit)
		return err
	})
	return audit, err
}

// RestoreRepositoryCleanupArtifact reverses one recorded quarantine move. It
// trusts neither the caller's paths nor the current filesystem: the exact
// source/target and digest come from the durable audit, and an existing source
// always wins rather than being overwritten.
func RestoreRepositoryCleanupArtifact(w *workspace.Workspace, project, planID, identity string, now time.Time) (CleanupAudit, error) {
	var audit CleanupAudit
	err := WithFileLock(filepath.Join(w.Root, workspace.Dir, ".cleanup.lock"), func() error {
		if len(planID) != 64 || len(identity) != 64 || !isLowerHex(planID) || !isLowerHex(identity) {
			return fmt.Errorf("cleanup plan or artifact identity is invalid")
		}
		auditPath := filepath.Join(w.Root, workspace.Dir, "audit", "cleanup", planID+".json")
		raw, err := os.ReadFile(auditPath)
		if err != nil {
			return fmt.Errorf("read cleanup audit: %w", err)
		}
		if err := json.Unmarshal(raw, &audit); err != nil {
			return fmt.Errorf("read cleanup audit: %w", err)
		}
		if audit.Schema != "repository-cleanup-audit/v2" || audit.PlanID != planID || audit.Project != project {
			return fmt.Errorf("cleanup audit identity is stale or unknown")
		}
		var artifact *CleanupArtifact
		for i := range audit.Quarantined {
			if audit.Quarantined[i].Identity == identity {
				artifact = &audit.Quarantined[i]
				break
			}
		}
		if artifact == nil {
			return fmt.Errorf("artifact identity is not recorded as quarantined")
		}
		for _, restored := range audit.Restored {
			if restored.Identity == identity {
				return fmt.Errorf("artifact identity is already restored")
			}
		}
		if err := validateCleanupArtifactPaths(w, *artifact, true); err != nil {
			return err
		}
		if _, err := os.Lstat(artifact.Path); err == nil {
			return fmt.Errorf("restore source already exists; refusing to overwrite %s", artifact.Path)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect restore source: %w", err)
		}
		info, err := os.Lstat(artifact.Quarantine)
		if err != nil {
			return fmt.Errorf("inspect quarantined artifact: %w", err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("quarantined artifact is not a regular file")
		}
		digest, err := cleanupArtifactDigest(artifact.Quarantine)
		if err != nil {
			return fmt.Errorf("verify quarantined artifact: %w", err)
		}
		observedIdentity := cleanupArtifactIdentity(artifact.Path, artifact.RunID, digest, info.Size(), uint32(info.Mode().Perm()))
		if digest != artifact.Digest || observedIdentity != artifact.Identity {
			return fmt.Errorf("quarantined artifact identity changed; refusing restore")
		}
		if err := os.MkdirAll(filepath.Dir(artifact.Path), 0o755); err != nil {
			return fmt.Errorf("prepare restore source: %w", err)
		}
		if err := restoreCleanupArtifact(artifact.Quarantine, artifact.Path); err != nil {
			return fmt.Errorf("restore quarantined artifact: %w", err)
		}
		audit.Restored = append(audit.Restored, *artifact)
		audit.Operations = append(audit.Operations, CleanupOperationAudit{
			Operation: "artifact-restore", Source: artifact.Quarantine, Target: artifact.Path,
			Identity: artifact.Identity, Digest: artifact.Digest, CompletedAt: now.UTC(),
		})
		_, err = writeCleanupAudit(w, audit)
		return err
	})
	return audit, err
}

func validateCleanupArtifactPaths(w *workspace.Workspace, artifact CleanupArtifact, sourceParentMayBeMissing bool) error {
	if !pathWithin(w.RunsDir(), artifact.Path) {
		return fmt.Errorf("artifact source is outside the managed runs directory")
	}
	quarantineRoot := filepath.Join(w.Root, workspace.Dir, "quarantine", "cleanup")
	if !pathWithin(quarantineRoot, artifact.Quarantine) {
		return fmt.Errorf("artifact target is outside the managed cleanup quarantine")
	}
	if err := rejectCleanupSymlinkParents(w.Root, artifact.Path, sourceParentMayBeMissing); err != nil {
		return fmt.Errorf("artifact source parent is not canonical: %w", err)
	}
	if err := rejectCleanupSymlinkParents(w.Root, artifact.Quarantine, true); err != nil {
		return fmt.Errorf("artifact quarantine parent is not canonical: %w", err)
	}
	return nil
}

// rejectCleanupSymlinkParents walks from the workspace root rather than using
// EvalSymlinks on the final path. The quarantine tail may not exist yet, but
// every existing parent must be a real directory: otherwise a lexical path
// beneath .dacli could resolve to an operator-controlled path outside it.
func rejectCleanupSymlinkParents(root, path string, allowMissing bool) error {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Dir(filepath.Clean(path)))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("parent escapes workspace")
	}
	current := filepath.Clean(root)
	if rel == "." {
		return nil
	}
	for _, component := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) && allowMissing {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlinked parent %s", current)
		}
		if !info.IsDir() {
			return fmt.Errorf("non-directory parent %s", current)
		}
	}
	return nil
}

func verifyCleanupArtifactSource(artifact CleanupArtifact) error {
	info, err := os.Lstat(artifact.Path)
	if err != nil {
		return fmt.Errorf("inspect artifact source: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("artifact source is not a regular file")
	}
	digest, err := cleanupArtifactDigest(artifact.Path)
	if err != nil {
		return fmt.Errorf("digest artifact source: %w", err)
	}
	identity := cleanupArtifactIdentity(artifact.Path, artifact.RunID, digest, info.Size(), uint32(info.Mode().Perm()))
	if digest != artifact.Digest || identity != artifact.Identity {
		return fmt.Errorf("artifact identity changed after planning")
	}
	return nil
}

func pathWithin(root, path string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func isLowerHex(s string) bool {
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func writeCleanupAudit(w *workspace.Workspace, audit CleanupAudit) (string, error) {
	raw, err := json.MarshalIndent(audit, "", "  ")
	if err != nil {
		return "", err
	}
	dir := filepath.Join(w.Root, workspace.Dir, "audit", "cleanup")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, audit.PlanID+".json")
	return path, mdstore.WriteBytes(path, append(raw, '\n'), 0o644)
}
