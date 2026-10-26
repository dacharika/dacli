package ghmirror

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mlnomadpy/dacli/internal/application"
	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/mdstore"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

type exactIssueSource struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	State  string `json:"state"`
}

func exactIssueSourceDigest(repo string, issue exactIssueSource) string {
	data, _ := json.Marshal(struct {
		Repository string           `json:"repository"`
		Issue      exactIssueSource `json:"issue"`
	}{repo, issue})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// cmdExactIssueLink previews or applies an explicit bounded mapping. Applying
// an existing issue link never calls a remote write and never imports issue
// prose; acceptance migration remains a separate owner-authorized command.
func cmdExactIssueLink(ctx *clikit.Ctx, args []string) error {
	f, err := clikit.ParseFlags(args, "apply", "owner-config")
	if err != nil {
		return err
	}
	if err := f.Reject("dry-run", "apply", "owner-config"); err != nil {
		return err
	}
	applyID := f.Get("apply")
	if applyID != "" {
		if f.Bool("dry-run") || len(f.Pos) != 0 || f.Get("owner-config") == "" || len(f.All("apply")) != 1 {
			return clikit.Usagef("usage: dacli github issue link --apply <plan-id> --owner-config <private-file>")
		}
		w, _, err := clikit.OpenWorkspace(ctx)
		if err != nil {
			return err
		}
		plan, err := readExactIssueLinkPlan(w, applyID)
		if err != nil {
			return clikit.Refusedf("exact issue link plan unavailable: %v", err)
		}
		owner, err := application.OpenNativeIssueLinkOwner(f.Get("owner-config"), plan.Project)
		if err != nil {
			return clikit.Refusedf("native owner protection unavailable")
		}
		p, err := store.LoadProject(w, plan.Project)
		if err != nil {
			return err
		}
		repo, _ := p.Doc.Front.Get("github_repo")
		if repo == "" || repo != plan.Repository {
			return clikit.Refusedf("project repository changed since issue link preview")
		}
		if err := refreshExactIssueBindings(w, p, plan); err != nil {
			return clikit.Refusedf("exact issue link plan is stale: %v", err)
		}
		if err := owner.Apply(plan); err != nil {
			return clikit.Refusedf("exact issue link apply refused or needs selected-set recovery: %v", err)
		}
		result := map[string]any{"schema": "github-exact-issue-link-apply/v1", "plan_id": plan.ID, "linked": len(plan.Targets), "remote_writes": 0, "acceptance_imported": false}
		if ctx.JSON {
			return clikit.EmitJSON(ctx, result)
		}
		fmt.Fprintf(ctx.Stdout, "applied exact issue link plan %s (%d selected task mappings; no GitHub writes)\n", plan.ID, len(plan.Targets))
		return nil
	}
	if !f.Bool("dry-run") || len(f.Pos) < 2 || f.Get("owner-config") != "" {
		return clikit.Usagef("usage: dacli github issue link <project> <issue-number[=task-id]>... --dry-run")
	}
	w, _, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	project := f.Pos[0]
	p, err := store.LoadProject(w, project)
	if err != nil {
		return err
	}
	repo, _ := p.Doc.Front.Get("github_repo")
	if repo == "" {
		return unlinkedRefusal(w.Root, p.Slug)
	}
	info, err := repoView(w, repo)
	if err != nil {
		return err
	}
	if !strings.EqualFold(info.NameWithOwner, repo) {
		return clikit.Refusedf("configured repository %q resolved to foreign repository %q", repo, info.NameWithOwner)
	}
	if _, err := publicationPolicy(w, repo, p, false, false); err != nil {
		return err
	}
	graph, err := store.ReadCheckpointCanonical(w)
	if err != nil {
		return clikit.Refusedf("complete canonical task mapping inventory unavailable: %v", err)
	}
	tasks := make([]*store.Task, 0)
	for _, task := range graph.Tasks {
		if task.Project == p.Slug {
			tasks = append(tasks, task)
		}
	}
	request, err := parseExactIssueSelection(f.Pos[1:])
	if err != nil {
		return clikit.Usagef("%v", err)
	}
	if len(request) > 100 {
		return clikit.Usagef("exact issue link selection is limited to 100 issues")
	}
	selectedTaskIDs, selectedIssueIDs := map[string]bool{}, map[int]bool{}
	targets := make([]store.IssueLinkTarget, 0, len(request))
	for _, pair := range request {
		if selectedIssueIDs[pair.number] {
			return clikit.Usagef("issue #%d is selected more than once", pair.number)
		}
		selectedIssueIDs[pair.number] = true
		issue, err := fetchExactIssue(w, repo, pair.number)
		if err != nil {
			return err
		}
		if strings.EqualFold(issue.State, "closed") || strings.Contains(issue.Body, markerPrefix) {
			return clikit.Refusedf("issue #%d is closed or already carries a dacli projection marker", pair.number)
		}
		task, err := resolveExactTargetFromCanonical(tasks, pair.task, issue.Title)
		if err != nil {
			return err
		}
		if selectedTaskIDs[task.ID] {
			return clikit.Usagef("task %s is selected more than once", task.ID)
		}
		selectedTaskIDs[task.ID] = true
		block, _ := task.Doc.Front.GetBlock("github")
		alreadyLinked := block == githubBlock(pair.number, repo)
		logEntry := fmt.Sprintf("GitHub issue #%d linked by exact selection; source sha256:%s", pair.number, exactIssueSourceDigest(repo, issue))
		if alreadyLinked {
			logEntry = ""
		}
		targets = append(targets, store.IssueLinkTarget{TaskID: task.ID, Project: task.Project, IssueNumber: pair.number, Repository: repo, IssueDigest: exactIssueSourceDigest(repo, issue), MappingBlock: githubBlock(pair.number, repo), LogEntry: logEntry})
	}
	// Selected remote reads are not a lock. Refresh the project and its exact
	// repository/consent after the final read so a relink or consent withdrawal
	// during preview cannot be captured as a valid plan for the old binding.
	currentProject, err := store.LoadProject(w, p.Slug)
	if err != nil {
		return err
	}
	currentRepo, _ := currentProject.Doc.Front.Get("github_repo")
	if currentRepo != repo {
		return clikit.Refusedf("project repository changed while exact issues were being selected")
	}
	if _, err := publicationPolicy(w, repo, currentProject, false, false); err != nil {
		return err
	}
	plan, err := store.PlanIssueLinkBatch(w, p.Slug, repo, targets, time.Now().UTC())
	if err != nil {
		return err
	}
	if err := store.ValidateIssueLinkBatchBindings(w, plan); err != nil {
		return clikit.Refusedf("exact issue link mapping inventory is incomplete or conflicting: %v", err)
	}
	currentProject, err = store.LoadProject(w, p.Slug)
	if err != nil {
		return err
	}
	if _, err := publicationPolicy(w, repo, currentProject, false, false); err != nil {
		return err
	}
	if err := writeExactIssueLinkPlan(w, plan); err != nil {
		return err
	}
	result := map[string]any{"schema": "github-exact-issue-link-plan/v1", "plan_id": plan.ID, "project": plan.Project, "repository": plan.Repository, "targets": plan.Targets, "remote_writes": 0, "acceptance_imported": false, "apply": "dacli github issue link --apply " + plan.ID + " --owner-config <private-file>"}
	if ctx.JSON {
		return clikit.EmitJSON(ctx, result)
	}
	fmt.Fprintf(ctx.Stdout, "exact issue link plan %s\nproject: %s\nrepository: %s\n", plan.ID, plan.Project, plan.Repository)
	for _, target := range plan.Targets {
		fmt.Fprintf(ctx.Stdout, "  #%d -> %s (source %s, task %s)\n", target.IssueNumber, target.TaskID, target.IssueDigest, target.TaskDigest)
	}
	fmt.Fprintf(ctx.Stdout, "apply: dacli github issue link --apply %s --owner-config <private-file>\n", plan.ID)
	return nil
}

type exactIssuePair struct {
	number int
	task   string
}

func parseExactIssueSelection(raw []string) ([]exactIssuePair, error) {
	out := make([]exactIssuePair, 0, len(raw))
	for _, value := range raw {
		numberText, task, hasTask := strings.Cut(value, "=")
		n, err := strconv.Atoi(numberText)
		if err != nil || n < 1 || (hasTask && strings.TrimSpace(task) == "") {
			return nil, fmt.Errorf("expected issue-number[=exact-task-id], got %q", value)
		}
		out = append(out, exactIssuePair{n, task})
	}
	return out, nil
}

func resolveExactTarget(tasks []*store.Task, ref, title string) (*store.Task, error) {
	if len(tasks) > 0 {
		if tasks[0].Path != "" {
			w, err := workspace.Find(tasks[0].Path)
			if err != nil {
				return nil, clikit.Refusedf("complete canonical task identity unavailable: %v", err)
			}
			graph, err := store.ReadCheckpointCanonical(w)
			if err != nil {
				return nil, clikit.Refusedf("complete canonical task identity unavailable: %v", err)
			}
			project := tasks[0].Project
			tasks = make([]*store.Task, 0)
			for _, task := range graph.Tasks {
				if task.Project == project {
					tasks = append(tasks, task)
				}
			}
		}
	}
	return resolveExactTargetFromCanonical(tasks, ref, title)
}

func resolveExactTargetFromCanonical(tasks []*store.Task, ref, title string) (*store.Task, error) {
	var matches []*store.Task
	for _, task := range tasks {
		if ref != "" && (task.ID == ref || "t-"+task.ID == ref) {
			matches = append(matches, task)
		}
		if ref == "" && task.Title == title {
			matches = append(matches, task)
		}
	}
	if len(matches) == 0 {
		return nil, clikit.Refusedf("no exact existing canonical task matches issue title/target %q; exact link never creates tasks", title)
	}
	if len(matches) != 1 {
		return nil, clikit.Refusedf("issue title/target %q matches multiple canonical tasks; pass one exact task ID", title)
	}
	return matches[0], nil
}

// ensureIssueMappingUnique is retained as the repository-qualified predicate
// for focused boundary tests. Production preview/apply use the strict complete
// canonical batch validator in store.ValidateIssueLinkBatchBindings.
func ensureIssueMappingUnique(tasks []*store.Task, selected *store.Task, number int, repo string) error {
	for _, task := range tasks {
		block, exists := task.Doc.Front.GetBlock("github")
		if !exists {
			continue
		}
		mappedNumber, mappedRepo, err := store.ParseIssueLinkMapping(block)
		if err != nil {
			return clikit.Refusedf("task %s has a malformed GitHub mapping: %v", task.ID, err)
		}
		if task.ID == selected.ID {
			if mappedNumber != number || !strings.EqualFold(mappedRepo, repo) {
				return clikit.Refusedf("selected task %s already has a conflicting GitHub mapping", task.ID)
			}
		} else if mappedNumber == number && strings.EqualFold(mappedRepo, repo) {
			return clikit.Refusedf("issue #%d is already mapped to task %s in repository %q", number, task.ID, mappedRepo)
		}
	}
	return nil
}

func fetchExactIssue(w *workspace.Workspace, repo string, number int) (exactIssueSource, error) {
	var issue exactIssueSource
	out, err := ghRepo(w, repo, "issue", "view", strconv.Itoa(number), "--json", "number,title,body,state")
	if err != nil {
		return issue, fmt.Errorf("read selected issue #%d: %w (%s)", number, err, out)
	}
	if err := json.Unmarshal([]byte(out), &issue); err != nil || issue.Number != number || issue.Title == "" {
		return issue, fmt.Errorf("selected issue #%d returned invalid identity", number)
	}
	return issue, nil
}

func refreshExactIssueBindings(w *workspace.Workspace, p *store.Project, plan store.IssueLinkBatchPlan) error {
	repo, _ := p.Doc.Front.Get("github_repo")
	if repo != plan.Repository || p.Slug != plan.Project {
		return fmt.Errorf("project or repository binding changed")
	}
	info, err := repoView(w, repo)
	if err != nil {
		return err
	}
	if !strings.EqualFold(info.NameWithOwner, repo) {
		return fmt.Errorf("repository identity changed")
	}
	if _, err := publicationPolicy(w, repo, p, false, false); err != nil {
		return err
	}
	graph, err := store.ReadCheckpointCanonical(w)
	if err != nil {
		return err
	}
	byID := map[string]*store.Task{}
	for _, task := range graph.Tasks {
		if task.Project == plan.Project {
			byID[task.ID] = task
		}
	}
	for _, target := range plan.Targets {
		issue, err := fetchExactIssue(w, repo, target.IssueNumber)
		if err != nil {
			return err
		}
		if strings.EqualFold(issue.State, "closed") || strings.Contains(issue.Body, markerPrefix) || exactIssueSourceDigest(repo, issue) != target.IssueDigest {
			return fmt.Errorf("issue #%d source body/title/state or target changed", target.IssueNumber)
		}
		task := byID[target.TaskID]
		if task == nil || !store.SameIssueLinkPath(task.Path, target.TaskPath) {
			return fmt.Errorf("task %s moved or disappeared", target.TaskID)
		}
	}
	// Network reads may overlap a local relink or consent withdrawal. Re-read
	// the project and complete mapping inventory only after all selected issues
	// have been fetched; never carry the initial project object across that gap.
	currentProject, err := store.LoadProject(w, plan.Project)
	if err != nil {
		return err
	}
	if err := store.ValidateIssueLinkBatchBindings(w, plan); err != nil {
		return err
	}
	if _, err := publicationPolicy(w, repo, currentProject, false, false); err != nil {
		return err
	}
	return nil
}

func exactIssuePlanPath(w *workspace.Workspace, id string) string {
	return filepath.Join(w.Root, workspace.Dir, "plans", "issue-links", id+".json")
}

func writeExactIssueLinkPlan(w *workspace.Workspace, plan store.IssueLinkBatchPlan) error {
	path := exactIssuePlanPath(w, plan.ID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return err
	}
	if old, readErr := readExactIssuePrivateFile(path, 1<<20); readErr == nil {
		var prior store.IssueLinkBatchPlan
		if decodeExactIssueJSON(old, &prior) != nil {
			return fmt.Errorf("existing issue link plan is invalid")
		}
		priorJSON, _ := json.Marshal(prior)
		planJSON, _ := json.Marshal(plan)
		if prior.ID != plan.ID || !bytes.Equal(priorJSON, planJSON) {
			return fmt.Errorf("existing issue link plan is invalid")
		}
		return nil
	} else if !os.IsNotExist(readErr) {
		return readErr
	}
	return mdstore.WriteBytes(path, append(data, '\n'), 0o600)
}

func readExactIssueLinkPlan(w *workspace.Workspace, id string) (store.IssueLinkBatchPlan, error) {
	var plan store.IssueLinkBatchPlan
	if decoded, err := hex.DecodeString(id); err != nil || len(id) != 64 || len(decoded) != sha256.Size {
		return plan, fmt.Errorf("plan ID must be a SHA-256 digest")
	}
	data, err := readExactIssuePrivateFile(exactIssuePlanPath(w, id), 1<<20)
	if err != nil {
		return plan, err
	}
	if err := decodeExactIssueJSON(data, &plan); err != nil {
		return plan, fmt.Errorf("plan identity mismatch: %w", err)
	}
	if plan.ID != id {
		return plan, fmt.Errorf("plan identity mismatch")
	}
	return plan, nil
}

func readExactIssuePrivateFile(path string, max int64) ([]byte, error) {
	f, err := openExactIssuePrivate(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, err
		}
		return nil, fmt.Errorf("private issue-link plan must be a bounded regular file: %w", err)
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || opened.Size() < 0 || opened.Size() > max {
		return nil, fmt.Errorf("private issue-link plan must be a bounded regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil || int64(len(data)) > max {
		return nil, fmt.Errorf("private issue-link plan exceeds its size limit")
	}
	after, err := f.Stat()
	if err != nil || after.Size() != opened.Size() || !after.ModTime().Equal(opened.ModTime()) {
		return nil, fmt.Errorf("private issue-link plan changed during read")
	}
	return data, nil
}

func decodeExactIssueJSON(data []byte, into any) error {
	if err := rejectExactIssueJSONDuplicates(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
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

func rejectExactIssueJSONDuplicates(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
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
