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

	"github.com/mlnomadpy/dacli/internal/mdstore"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

const issueLinkBatchSchema = "issue-link-batch/v1"

// IssueLinkTarget freezes one existing canonical task and the exact mapping
// fields to add. Issue prose and acceptance are deliberately absent.
type IssueLinkTarget struct {
	TaskID       string `json:"task_id"`
	Project      string `json:"project"`
	TaskPath     string `json:"task_path"`
	TaskDigest   string `json:"task_digest"`
	IssueNumber  int    `json:"issue_number"`
	Repository   string `json:"repository"`
	IssueDigest  string `json:"issue_digest"`
	MappingBlock string `json:"mapping_block"`
	LogEntry     string `json:"log_entry"`
}

// IssueLinkBatchPlan is an immutable digest of a bounded exact selection.
type IssueLinkBatchPlan struct {
	Schema     string            `json:"schema"`
	ID         string            `json:"id"`
	Project    string            `json:"project"`
	Repository string            `json:"repository"`
	CreatedAt  time.Time         `json:"created_at"`
	Targets    []IssueLinkTarget `json:"targets"`
}

type issueLinkRecoveryItem struct {
	TaskID  string `json:"task_id"`
	Path    string `json:"path"`
	Before  []byte `json:"before"`
	After   []byte `json:"after"`
	Applied bool   `json:"applied"`
}

type issueLinkRecovery struct {
	Schema string                  `json:"schema"`
	PlanID string                  `json:"plan_id"`
	Items  []issueLinkRecoveryItem `json:"items"`
}

// PlanIssueLinkBatch binds every selected task to its full current bytes.
func PlanIssueLinkBatch(w *workspace.Workspace, project, repository string, targets []IssueLinkTarget, now time.Time) (IssueLinkBatchPlan, error) {
	plan := IssueLinkBatchPlan{Schema: issueLinkBatchSchema, Project: project, Repository: repository, CreatedAt: now.UTC(), Targets: append([]IssueLinkTarget(nil), targets...)}
	if project == "" || repository == "" || len(targets) == 0 || len(targets) > 100 || now.IsZero() {
		return IssueLinkBatchPlan{}, fmt.Errorf("exact issue link selection must contain 1 to 100 targets and a project/repository")
	}
	sort.Slice(plan.Targets, func(i, j int) bool { return plan.Targets[i].TaskID < plan.Targets[j].TaskID })
	seenTasks, seenIssues := map[string]bool{}, map[int]bool{}
	for i := range plan.Targets {
		target := &plan.Targets[i]
		if !validIssueLinkTarget(project, repository, *target) || seenTasks[target.TaskID] || seenIssues[target.IssueNumber] {
			return IssueLinkBatchPlan{}, fmt.Errorf("duplicate or invalid issue-to-task selection")
		}
		seenTasks[target.TaskID], seenIssues[target.IssueNumber] = true, true
		task, err := FindTask(w, project+"/"+target.TaskID)
		if err != nil || task.ID != target.TaskID || task.Project != project || task.Path == "" {
			return IssueLinkBatchPlan{}, fmt.Errorf("selected canonical task %s is missing or ambiguous", target.TaskID)
		}
		raw, err := os.ReadFile(task.Path)
		if err != nil {
			return IssueLinkBatchPlan{}, fmt.Errorf("read selected task %s: %w", target.TaskID, err)
		}
		target.TaskPath = task.Path
		target.TaskDigest = issueLinkDigest(raw)
	}
	plan.ID, _ = issueLinkPlanID(plan)
	return plan, nil
}

func issueLinkDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func issueLinkPlanID(plan IssueLinkBatchPlan) (string, error) {
	plan.ID = ""
	data, err := json.Marshal(plan)
	if err != nil {
		return "", err
	}
	return issueLinkDigest(data), nil
}

// ApplyIssueLinkBatch validates and stages the complete selected set before
// effects, then commits selected files in stable task-ID order. Each commit is
// wrapped by the caller's live native-protection check before taking the task
// lock (the established authority->task lock order). The durable journal makes
// a partial selected-set write explicitly recoverable by replaying this plan.
func ApplyIssueLinkBatch(w *workspace.Workspace, plan IssueLinkBatchPlan, protect func(string, func() error) error) error {
	want, err := issueLinkPlanID(plan)
	if err != nil || plan.Schema != issueLinkBatchSchema || plan.ID == "" || plan.ID != want || plan.Project == "" || plan.Repository == "" || plan.CreatedAt.IsZero() || protect == nil || len(plan.Targets) == 0 || len(plan.Targets) > 100 {
		return fmt.Errorf("invalid exact issue link plan")
	}
	seenTasks, seenIssues := map[string]bool{}, map[int]bool{}
	for _, target := range plan.Targets {
		taskDigest := target.TaskDigest
		if _, digestErr := hex.DecodeString(taskDigest); digestErr != nil || len(taskDigest) != sha256.Size*2 || !validIssueLinkTarget(plan.Project, plan.Repository, target) || seenTasks[target.TaskID] || seenIssues[target.IssueNumber] || target.TaskPath == "" {
			return fmt.Errorf("duplicate or invalid exact issue link target")
		}
		seenTasks[target.TaskID], seenIssues[target.IssueNumber] = true, true
	}
	items := append([]IssueLinkTarget(nil), plan.Targets...)
	sort.Slice(items, func(i, j int) bool { return items[i].TaskID < items[j].TaskID })
	journalPath := filepath.Join(w.Root, workspace.Dir, "recovery", "issue-links", plan.ID+".json")
	if err := os.MkdirAll(filepath.Dir(journalPath), 0o700); err != nil {
		return err
	}
	var recovery issueLinkRecovery
	if raw, readErr := os.ReadFile(journalPath); readErr == nil {
		if json.Unmarshal(raw, &recovery) != nil || recovery.Schema != "issue-link-recovery/v1" || recovery.PlanID != plan.ID || len(recovery.Items) != len(items) {
			return fmt.Errorf("issue link recovery journal is invalid; preserve it for owner review")
		}
		for i := range items {
			if recovery.Items[i].TaskID != items[i].TaskID || recovery.Items[i].Path != items[i].TaskPath {
				return fmt.Errorf("issue link recovery selection changed")
			}
			if issueLinkDigest(recovery.Items[i].Before) != items[i].TaskDigest {
				return fmt.Errorf("issue link recovery source bytes do not match the immutable plan")
			}
			expected, buildErr := issueLinkUpdatedBytes(w, plan.CreatedAt, items[i], recovery.Items[i].Before)
			if buildErr != nil || !bytesEqual(expected, recovery.Items[i].After) {
				return fmt.Errorf("issue link recovery target bytes do not match the immutable plan")
			}
		}
	} else if !os.IsNotExist(readErr) {
		return readErr
	} else {
		recovery = issueLinkRecovery{Schema: "issue-link-recovery/v1", PlanID: plan.ID, Items: make([]issueLinkRecoveryItem, len(items))}
		for i, target := range items {
			task, loadErr := loadTaskFile(target.TaskPath, target.Project, taskStatusFromPath(w, target.Project, target.TaskPath))
			if loadErr != nil || task.ID != target.TaskID || task.Project != target.Project {
				return fmt.Errorf("selected task %s moved or is unavailable", target.TaskID)
			}
			before, readErr := os.ReadFile(target.TaskPath)
			if readErr != nil || issueLinkDigest(before) != target.TaskDigest {
				return fmt.Errorf("selected task %s changed since preview", target.TaskID)
			}
			after, buildErr := issueLinkUpdatedBytes(w, plan.CreatedAt, target, before)
			if buildErr != nil {
				return fmt.Errorf("prepare selected task %s: %w", target.TaskID, buildErr)
			}
			recovery.Items[i] = issueLinkRecoveryItem{TaskID: target.TaskID, Path: target.TaskPath, Before: before, After: after}
		}
		if err := writeIssueLinkRecovery(journalPath, recovery); err != nil {
			return fmt.Errorf("stage issue link recovery: %w", err)
		}
	}

	// Revalidate the entire selected set before the first write. A replay may
	// contain only exact before/after bytes; any third state needs owner review.
	for _, item := range recovery.Items {
		current, readErr := os.ReadFile(item.Path)
		if readErr != nil {
			return fmt.Errorf("selected task %s moved or is unavailable", item.TaskID)
		}
		if !bytesEqual(current, item.Before) && !bytesEqual(current, item.After) {
			return fmt.Errorf("selected task %s changed after preview; recovery preserved", item.TaskID)
		}
	}
	for i := range recovery.Items {
		item := &recovery.Items[i]
		if item.Applied || bytesEqual(readBytes(item.Path), item.After) {
			item.Applied = true
			continue
		}
		commit := func() error {
			release, lockErr := acquireFileLock(taskLockPath(w, &Task{ID: item.TaskID, Project: plan.Project}))
			if lockErr != nil {
				return lockErr
			}
			defer release()
			current, readErr := os.ReadFile(item.Path)
			if readErr != nil || !bytesEqual(current, item.Before) {
				return fmt.Errorf("selected task %s changed before commit; recovery preserved", item.TaskID)
			}
			return mdstore.WriteBytes(item.Path, item.After, 0o600)
		}
		if err := protect(item.TaskID, commit); err != nil {
			return fmt.Errorf("issue link batch partial/recovery required for plan %s: %w", plan.ID, err)
		}
		item.Applied = true
		if err := writeIssueLinkRecovery(journalPath, recovery); err != nil {
			return fmt.Errorf("issue link batch committed selected task %s; recovery journal update failed: %w", item.TaskID, err)
		}
	}
	return nil
}

func validIssueLinkTarget(project, repository string, target IssueLinkTarget) bool {
	digest := strings.TrimPrefix(target.IssueDigest, "sha256:")
	decoded, err := hex.DecodeString(digest)
	return project != "" && target.TaskID != "" && target.Project == project && target.IssueNumber > 0 && strings.Count(repository, "/") == 1 && !strings.ContainsAny(repository, "\r\n\t ") && target.Repository == repository && err == nil && len(decoded) == sha256.Size && target.MappingBlock == fmt.Sprintf("  issue: %d\n  repo: %s", target.IssueNumber, repository) && (target.LogEntry == "" || target.LogEntry == fmt.Sprintf("GitHub issue #%d linked by exact selection; source sha256:%s", target.IssueNumber, digest))
}

func issueLinkUpdatedBytes(w *workspace.Workspace, at time.Time, target IssueLinkTarget, before []byte) ([]byte, error) {
	status := taskStatusFromPath(w, target.Project, target.TaskPath)
	if status == "" {
		return nil, fmt.Errorf("task path is outside its project status folders")
	}
	doc, err := mdstore.Parse(string(before))
	if err != nil {
		return nil, err
	}
	task := &Task{ID: target.TaskID, Project: target.Project, Status: status, Path: target.TaskPath, Doc: doc}
	task.Doc.Front.SetBlock("github", target.MappingBlock)
	if target.LogEntry != "" {
		section, _ := task.Doc.Section("Log")
		task.Doc.SetSection("Log", section.Content+"- "+at.UTC().Format(time.RFC3339)+" "+target.LogEntry+"\n")
	}
	return []byte(mdstore.Render(task.Doc)), nil
}

func taskStatusFromPath(w *workspace.Workspace, project, path string) model.Status {
	actualDir, actualErr := filepath.EvalSymlinks(filepath.Dir(path))
	if actualErr != nil {
		return ""
	}
	for _, status := range model.AllStatuses {
		expectedDir, expectedErr := filepath.EvalSymlinks(w.TasksDir(project, status))
		if expectedErr == nil && actualDir == expectedDir {
			return status
		}
	}
	return ""
}

func writeIssueLinkRecovery(path string, recovery issueLinkRecovery) error {
	data, err := json.Marshal(recovery)
	if err != nil {
		return err
	}
	return mdstore.WriteBytes(path, data, 0o600)
}

func readBytes(path string) []byte { data, _ := os.ReadFile(path); return data }

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
