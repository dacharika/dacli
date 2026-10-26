package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mlnomadpy/dacli/internal/mdstore"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

const issueLinkBatchSchema = "issue-link-batch/v2"
const issueLinkRecoveryMaxBytes int64 = 80 << 20

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
	Schema        string            `json:"schema"`
	ID            string            `json:"id"`
	Project       string            `json:"project"`
	Repository    string            `json:"repository"`
	ProjectDigest string            `json:"project_digest"`
	CreatedAt     time.Time         `json:"created_at"`
	Targets       []IssueLinkTarget `json:"targets"`
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
	graph, err := ReadCheckpointCanonical(w)
	if err != nil {
		return IssueLinkBatchPlan{}, fmt.Errorf("complete canonical task mapping inventory unavailable: %w", err)
	}
	if graph.Projects[project] == nil {
		return IssueLinkBatchPlan{}, fmt.Errorf("exact issue link project is missing")
	}
	projectBytes, err := issueLinkProjectBytes(w, project)
	if err != nil {
		return IssueLinkBatchPlan{}, err
	}
	if _, err := mdstore.Parse(string(projectBytes)); err != nil {
		return IssueLinkBatchPlan{}, fmt.Errorf("project binding is invalid")
	}
	plan.ProjectDigest = issueLinkDigest(projectBytes)
	sort.Slice(plan.Targets, func(i, j int) bool { return plan.Targets[i].TaskID < plan.Targets[j].TaskID })
	seenTasks, seenIssues := map[string]bool{}, map[int]bool{}
	for i := range plan.Targets {
		target := &plan.Targets[i]
		if !validIssueLinkTarget(project, repository, *target) || seenTasks[target.TaskID] || seenIssues[target.IssueNumber] {
			return IssueLinkBatchPlan{}, fmt.Errorf("duplicate or invalid issue-to-task selection")
		}
		seenTasks[target.TaskID], seenIssues[target.IssueNumber] = true, true
		var task *Task
		for _, candidate := range graph.Tasks {
			if candidate.ID == target.TaskID && candidate.Project == project {
				task = candidate
				break
			}
		}
		if task == nil || task.Path == "" {
			return IssueLinkBatchPlan{}, fmt.Errorf("selected canonical task %s is missing or ambiguous", target.TaskID)
		}
		raw, err := issueLinkTaskBytes(w, task)
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
	return ApplyIssueLinkBatchWithValidation(w, plan, protect, nil)
}

// ApplyIssueLinkBatchWithValidation runs validate at each selected target's
// local effect boundary, inside the authority callback and stable task lock.
// It intentionally does not hold locks across remote reads or promise a
// workspace-wide atomic transaction.
func ApplyIssueLinkBatchWithValidation(w *workspace.Workspace, plan IssueLinkBatchPlan, protect func(string, func() error) error, validate func() error) error {
	want, err := issueLinkPlanID(plan)
	if err != nil || plan.Schema != issueLinkBatchSchema || plan.ID == "" || plan.ID != want || plan.Project == "" || plan.Repository == "" || !validIssueLinkDigest(plan.ProjectDigest) || plan.CreatedAt.IsZero() || protect == nil || len(plan.Targets) == 0 || len(plan.Targets) > 100 {
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
	graph, err := ReadCheckpointCanonical(w)
	if err != nil {
		return fmt.Errorf("complete canonical task mapping inventory unavailable: %w", err)
	}
	if raw, readErr := readIssueLinkPrivateFile(journalPath, issueLinkRecoveryMaxBytes); readErr == nil {
		if decodeIssueLinkJSON(raw, &recovery) != nil || recovery.Schema != "issue-link-recovery/v1" || recovery.PlanID != plan.ID || len(recovery.Items) != len(items) {
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
			var task *Task
			for _, candidate := range graph.Tasks {
				if candidate.ID == target.TaskID && candidate.Project == target.Project {
					task = candidate
					break
				}
			}
			if task == nil || !sameIssueLinkPath(task.Path, target.TaskPath) {
				return fmt.Errorf("selected task %s moved or is unavailable", target.TaskID)
			}
			before, readErr := issueLinkTaskBytes(w, task)
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
		current, readErr := readIssueLinkTaskPath(w, plan.Project, item.Path)
		if readErr != nil {
			return fmt.Errorf("selected task %s moved or is unavailable", item.TaskID)
		}
		if !bytesEqual(current, item.Before) && !bytesEqual(current, item.After) {
			return fmt.Errorf("selected task %s changed after preview; recovery preserved", item.TaskID)
		}
	}
	if validate != nil {
		if err := validate(); err != nil {
			return fmt.Errorf("issue link local binding changed before apply: %w", err)
		}
	}
	for i := range recovery.Items {
		item := &recovery.Items[i]
		verifyRelease, lockErr := acquireFileLock(taskLockPath(w, &Task{ID: item.TaskID, Project: plan.Project}))
		if lockErr != nil {
			return lockErr
		}
		current, readErr := readIssueLinkTaskPath(w, plan.Project, item.Path)
		if readErr != nil {
			verifyRelease()
			return fmt.Errorf("selected task %s moved or is unavailable", item.TaskID)
		}
		if bytesEqual(current, item.After) {
			item.Applied = true
			if err := writeIssueLinkRecovery(journalPath, recovery); err != nil {
				verifyRelease()
				return fmt.Errorf("selected task %s is committed but recovery journal update failed: %w", item.TaskID, err)
			}
			verifyRelease()
			continue
		}
		if item.Applied {
			verifyRelease()
			return fmt.Errorf("selected task %s was restored to its preview bytes after an applied journal entry; recovery preserved for owner review", item.TaskID)
		}
		verifyRelease()
		commit := func() error {
			release, lockErr := acquireFileLock(taskLockPath(w, &Task{ID: item.TaskID, Project: plan.Project}))
			if lockErr != nil {
				return lockErr
			}
			defer release()
			if validate != nil {
				if err := validate(); err != nil {
					return err
				}
			}
			current, readErr := readIssueLinkTaskPath(w, plan.Project, item.Path)
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

// ValidateIssueLinkBatchBindings verifies the exact local project and mapping
// inventory against a preview. It is called again at every local effect
// boundary; ReadCheckpointCanonical refuses incomplete, malformed or duplicate
// task identity instead of accepting ListTasks' display-oriented deduplication.
func ValidateIssueLinkBatchBindings(w *workspace.Workspace, plan IssueLinkBatchPlan) error {
	graph, err := ReadCheckpointCanonical(w)
	if err != nil {
		return fmt.Errorf("complete canonical task mapping inventory unavailable: %w", err)
	}
	project := graph.Projects[plan.Project]
	if project == nil {
		return fmt.Errorf("issue-link project is missing")
	}
	projectBytes, err := issueLinkProjectBytes(w, plan.Project)
	if err != nil || issueLinkDigest(projectBytes) != plan.ProjectDigest {
		return fmt.Errorf("project binding or task-only consent changed since issue-link preview")
	}
	projectDoc, err := mdstore.Parse(string(projectBytes))
	if err != nil {
		return fmt.Errorf("project binding is invalid")
	}
	repo, _ := projectDoc.Front.Get("github_repo")
	if repo != plan.Repository {
		return fmt.Errorf("project repository changed since issue-link preview")
	}
	selected := make(map[string]IssueLinkTarget, len(plan.Targets))
	for _, target := range plan.Targets {
		selected[target.TaskID] = target
		var found *Task
		for _, task := range graph.Tasks {
			if task.ID == target.TaskID && task.Project == target.Project {
				found = task
				break
			}
		}
		if found == nil || !sameIssueLinkPath(found.Path, target.TaskPath) {
			return fmt.Errorf("selected task %s moved or disappeared", target.TaskID)
		}
	}
	for _, task := range graph.Tasks {
		block, exists := task.Doc.Front.GetBlock("github")
		if !exists {
			continue
		}
		issue, mappedRepo, err := parseExactIssueMapping(block)
		if err != nil {
			return fmt.Errorf("task %s has a malformed GitHub mapping: %w", task.ID, err)
		}
		if target, isSelected := selected[task.ID]; isSelected {
			if issue == target.IssueNumber && strings.EqualFold(mappedRepo, target.Repository) {
				continue
			}
			return fmt.Errorf("selected task %s already has a conflicting GitHub mapping", task.ID)
		}
		for _, target := range plan.Targets {
			if issue == target.IssueNumber && strings.EqualFold(mappedRepo, target.Repository) {
				return fmt.Errorf("issue #%d is already mapped to task %s in repository %q", target.IssueNumber, task.ID, mappedRepo)
			}
		}
	}
	return nil
}

func parseExactIssueMapping(block string) (int, string, error) {
	var issue int
	var repo string
	seenIssue, seenRepo := false, false
	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return 0, "", fmt.Errorf("invalid mapping line")
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		switch key {
		case "issue":
			if seenIssue {
				return 0, "", fmt.Errorf("duplicate issue field")
			}
			seenIssue = true
			n, err := strconv.Atoi(value)
			if err != nil || n < 1 {
				return 0, "", fmt.Errorf("invalid issue number")
			}
			issue = n
		case "repo":
			if seenRepo || !validIssueLinkRepository(value) {
				return 0, "", fmt.Errorf("invalid repository identity")
			}
			seenRepo = true
			repo = value
		default:
			return 0, "", fmt.Errorf("unknown mapping field %q", key)
		}
	}
	parts := strings.Split(repo, "/")
	if issue == 0 || repo == "" || len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return 0, "", fmt.Errorf("issue and repository fields are required")
	}
	return issue, repo, nil
}

// ParseIssueLinkMapping strictly reads the exact issue/repository identity in a
// task's private GitHub mapping block.
func ParseIssueLinkMapping(block string) (int, string, error) {
	return parseExactIssueMapping(block)
}

func validIssueLinkTarget(project, repository string, target IssueLinkTarget) bool {
	digest := strings.TrimPrefix(target.IssueDigest, "sha256:")
	decoded, err := hex.DecodeString(digest)
	return project != "" && target.TaskID != "" && target.Project == project && target.IssueNumber > 0 && validIssueLinkRepository(repository) && target.Repository == repository && err == nil && len(decoded) == sha256.Size && target.MappingBlock == fmt.Sprintf("  issue: %d\n  repo: %s", target.IssueNumber, repository) && (target.LogEntry == "" || target.LogEntry == fmt.Sprintf("GitHub issue #%d linked by exact selection; source sha256:%s", target.IssueNumber, digest))
}

func validIssueLinkRepository(repository string) bool {
	if strings.ContainsAny(repository, "\r\n\t ") {
		return false
	}
	parts := strings.Split(repository, "/")
	return len(parts) == 2 && parts[0] != "" && parts[1] != ""
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
	if int64(len(data)) > issueLinkRecoveryMaxBytes {
		return fmt.Errorf("issue link recovery journal exceeds its size limit")
	}
	return mdstore.WriteBytes(path, data, 0o600)
}

func validIssueLinkDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func issueLinkProjectBytes(w *workspace.Workspace, project string) ([]byte, error) {
	root, err := checkpointOpen(w.ProjectsDir())
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	dir, err := checkpointDirectory(root, project)
	if err != nil {
		return nil, err
	}
	defer func() { _ = dir.Close() }()
	return (&checkpointReader{}).childBytes(dir, "project.md")
}

func issueLinkTaskBytes(w *workspace.Workspace, task *Task) ([]byte, error) {
	return readIssueLinkTaskPath(w, task.Project, task.Path)
}

func sameIssueLinkPath(left, right string) bool {
	leftResolved, leftErr := filepath.EvalSymlinks(left)
	rightResolved, rightErr := filepath.EvalSymlinks(right)
	if leftErr == nil && rightErr == nil {
		return filepath.Clean(leftResolved) == filepath.Clean(rightResolved)
	}
	return filepath.Clean(left) == filepath.Clean(right)
}

// SameIssueLinkPath treats a workspace path and its filesystem-resolved alias
// as the same selected file while still refusing a moved task.
func SameIssueLinkPath(left, right string) bool { return sameIssueLinkPath(left, right) }

func readIssueLinkTaskPath(w *workspace.Workspace, project, path string) ([]byte, error) {
	status := taskStatusFromPath(w, project, path)
	if status == "" {
		return nil, fmt.Errorf("task path is outside canonical project status folders")
	}
	dir, err := checkpointOpen(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer func() { _ = dir.Close() }()
	return (&checkpointReader{}).childBytes(dir, filepath.Base(path))
}

func readIssueLinkPrivateFile(path string, max int64) ([]byte, error) {
	f, err := checkpointOpen(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	before, err := f.Stat()
	if err != nil || !before.Mode().IsRegular() || before.Size() < 0 || before.Size() > max {
		return nil, fmt.Errorf("private issue-link file must be a bounded regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil || int64(len(data)) > max {
		return nil, fmt.Errorf("private issue-link file exceeds its size limit")
	}
	after, err := f.Stat()
	if err != nil || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return nil, fmt.Errorf("private issue-link file changed during observation")
	}
	return data, nil
}

func decodeIssueLinkJSON(data []byte, into any) error {
	if err := rejectIssueLinkJSONDuplicates(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}

func rejectIssueLinkJSONDuplicates(data []byte) error {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	var value func(json.Token) error
	value = func(token json.Token) error {
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok || seen[key] {
					return fmt.Errorf("duplicate or invalid JSON object key")
				}
				seen[key] = true
				child, err := decoder.Token()
				if err != nil {
					return err
				}
				if err := value(child); err != nil {
					return err
				}
			}
			_, err := decoder.Token()
			return err
		case '[':
			for decoder.More() {
				child, err := decoder.Token()
				if err != nil {
					return err
				}
				if err := value(child); err != nil {
					return err
				}
			}
			_, err := decoder.Token()
			return err
		default:
			return fmt.Errorf("invalid JSON delimiter")
		}
	}
	root, err := decoder.Token()
	if err != nil {
		return err
	}
	if err := value(root); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}

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
