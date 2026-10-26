package ghmirror

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/mdstore"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

func TestExactIssueLinkRefreshRefusesThirdIssueBodyDriftBeforeEffects(t *testing.T) {
	w := mirrorWorkspace(t)
	project, err := store.LoadProject(w, "core")
	if err != nil {
		t.Fatal(err)
	}
	project.Doc.Front.Set("github_repo", "owner/repo")
	if err := mdstore.WriteFile(project.Path, project.Doc); err != nil {
		t.Fatal(err)
	}
	tasks := make([]*store.Task, 3)
	for i, title := range []string{"Audit queue capacity", "Trace retry handling", "Document token rotation"} {
		task, err := store.CreateTask(w, "a-root", "core", title, store.TaskOpts{Accept: []string{"local criterion"}})
		if err != nil {
			t.Fatal(err)
		}
		tasks[i] = task
	}
	issues := map[int]exactIssueSource{}
	for i := 0; i < 3; i++ {
		number := 801 + i
		issues[number] = exactIssueSource{Number: number, Title: tasks[i].Title, Body: fmt.Sprintf("original body %d", number), State: "OPEN"}
	}
	originalGH := gh
	t.Cleanup(func() { gh = originalGH })
	gh = func(_ *workspace.Workspace, args ...string) (string, error) {
		if len(args) >= 2 && args[0] == "repo" && args[1] == "view" {
			return `{"nameWithOwner":"owner/repo","visibility":"PRIVATE","defaultBranchRef":{"name":"main"}}`, nil
		}
		if len(args) >= 3 && args[0] == "issue" && args[1] == "view" {
			var number int
			fmt.Sscan(args[2], &number)
			issue, ok := issues[number]
			if !ok {
				return "", fmt.Errorf("unknown issue")
			}
			data, _ := json.Marshal(issue)
			return string(data), nil
		}
		return "", fmt.Errorf("unexpected remote request %v", args)
	}
	targets := make([]store.IssueLinkTarget, 3)
	for i, task := range tasks {
		issue := issues[801+i]
		digest := exactIssueSourceDigest("owner/repo", issue)
		targets[i] = store.IssueLinkTarget{TaskID: task.ID, Project: "core", IssueNumber: issue.Number, Repository: "owner/repo", IssueDigest: digest, MappingBlock: githubBlock(issue.Number, "owner/repo"), LogEntry: fmt.Sprintf("GitHub issue #%d linked by exact selection; source sha256:%s", issue.Number, digest)}
	}
	plan, err := store.PlanIssueLinkBatch(w, "core", "owner/repo", targets, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	before := make([][]byte, 3)
	for i, task := range tasks {
		before[i], err = os.ReadFile(task.Path)
		if err != nil {
			t.Fatal(err)
		}
	}
	drift := issues[803]
	drift.Body = "changed after preview"
	issues[803] = drift
	err = refreshExactIssueBindings(w, project, plan)
	if err == nil || !strings.Contains(err.Error(), "issue #803 source body/title/state") {
		t.Fatalf("third issue drift error=%v", err)
	}
	for i, task := range tasks {
		after, e := os.ReadFile(task.Path)
		if e != nil {
			t.Fatal(e)
		}
		if string(after) != string(before[i]) {
			t.Fatalf("prevalidation drift changed selected task %d", i)
		}
	}
	drift.Body = "original body 803"
	issues[803] = drift
	tasks[1].Doc.Front.SetBlock("github", githubBlock(999, "owner/repo"))
	if err := store.SaveTask(tasks[1]); err != nil {
		t.Fatal(err)
	}
	movedBefore, err := os.ReadFile(tasks[1].Path)
	if err != nil {
		t.Fatal(err)
	}
	err = refreshExactIssueBindings(w, project, plan)
	if err == nil || !strings.Contains(err.Error(), "already has a conflicting GitHub mapping") {
		t.Fatalf("moved mapping error=%v", err)
	}
	movedAfter, err := os.ReadFile(tasks[1].Path)
	if err != nil || string(movedBefore) != string(movedAfter) {
		t.Fatalf("mapping conflict changed local bytes: %v", err)
	}
}

func TestExactIssueLinkRejectsMalformedCompetingMapping(t *testing.T) {
	w := mirrorWorkspace(t)
	project, err := store.LoadProject(w, "core")
	if err != nil {
		t.Fatal(err)
	}
	project.Doc.Front.Set("github_repo", "owner/repo")
	if err := store.SaveProject(project); err != nil {
		t.Fatal(err)
	}
	selected, err := store.CreateTask(w, "a-root", "core", "Selected", store.TaskOpts{})
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.CreateTask(w, "a-root", "core", "Competing malformed row", store.TaskOpts{})
	if err != nil {
		t.Fatal(err)
	}
	other.Doc.Front.SetBlock("github", "  issue: not-a-number\n  repo: owner/repo")
	if err := store.SaveTask(other); err != nil {
		t.Fatal(err)
	}
	plan, err := store.PlanIssueLinkBatch(w, "core", "owner/repo", []store.IssueLinkTarget{{TaskID: selected.ID, Project: "core", IssueNumber: 601, Repository: "owner/repo", IssueDigest: strings.Repeat("a", 64), MappingBlock: githubBlock(601, "owner/repo")}}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ValidateIssueLinkBatchBindings(w, plan); err == nil || !strings.Contains(err.Error(), "malformed GitHub mapping") {
		t.Fatalf("malformed competing mapping accepted: %v", err)
	}
}

func TestExactIssueLinkRefusesCompetingMappingOutsideSelection(t *testing.T) {
	w := mirrorWorkspace(t)
	project, err := store.LoadProject(w, "core")
	if err != nil {
		t.Fatal(err)
	}
	project.Doc.Front.Set("github_repo", "owner/repo")
	if err := store.SaveProject(project); err != nil {
		t.Fatal(err)
	}
	selected, err := store.CreateTask(w, "a-root", "core", "Selected", store.TaskOpts{})
	if err != nil {
		t.Fatal(err)
	}
	competitor, err := store.CreateTask(w, "a-root", "core", "Outside selected set", store.TaskOpts{})
	if err != nil {
		t.Fatal(err)
	}
	competitor.Doc.Front.SetBlock("github", githubBlock(601, "owner/repo"))
	if err := store.SaveTask(competitor); err != nil {
		t.Fatal(err)
	}
	plan, err := store.PlanIssueLinkBatch(w, "core", "owner/repo", []store.IssueLinkTarget{{TaskID: selected.ID, Project: "core", IssueNumber: 601, Repository: "owner/repo", IssueDigest: strings.Repeat("a", 64), MappingBlock: githubBlock(601, "owner/repo")}}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ValidateIssueLinkBatchBindings(w, plan); err == nil || !strings.Contains(err.Error(), "already mapped to task") {
		t.Fatalf("outside selected mapping conflict accepted: %v", err)
	}
}

func TestExactIssueLinkResolverRefusesDuplicateIDAcrossStatusFolders(t *testing.T) {
	w := mirrorWorkspace(t)
	task, err := store.CreateTask(w, "a-root", "core", "Selected", store.TaskOpts{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(task.Path)
	if err != nil {
		t.Fatal(err)
	}
	done := w.TasksDir("core", model.StatusDone)
	if err := os.MkdirAll(done, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(done, "999-duplicate.md"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	tasks, err := store.ListTasks(w, "core", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolveExactTarget(tasks, task.ID, task.Title); err == nil || !strings.Contains(err.Error(), "complete canonical task identity unavailable") {
		t.Fatalf("duplicate canonical ID across status folders accepted: %v", err)
	}
}

func TestExactIssueLinkResolverRefusesMalformedCanonicalFile(t *testing.T) {
	w := mirrorWorkspace(t)
	task, err := store.CreateTask(w, "a-root", "core", "Selected", store.TaskOpts{})
	if err != nil {
		t.Fatal(err)
	}
	malformed := filepath.Join(w.TasksDir("core", model.StatusOpen), "999-malformed.md")
	if err := os.WriteFile(malformed, []byte("not valid task frontmatter\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tasks, err := store.ListTasks(w, "core", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolveExactTarget(tasks, task.ID, task.Title); err == nil || !strings.Contains(err.Error(), "complete canonical task identity unavailable") {
		t.Fatalf("malformed competing task file was hidden by list semantics: %v", err)
	}
}

func TestExactIssueLinkRepositoryQualifiedMappingCases(t *testing.T) {
	w := mirrorWorkspace(t)
	selected, err := store.CreateTask(w, "a-root", "core", "Selected", store.TaskOpts{})
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.CreateTask(w, "a-root", "core", "Other", store.TaskOpts{})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name     string
		selected string
		other    string
		wantErr  bool
	}{
		{name: "same number in another repository", other: githubBlock(601, "owner/other")},
		{name: "same number in selected repository on another task", other: githubBlock(601, "owner/repo"), wantErr: true},
		{name: "selected task already maps another repository", selected: githubBlock(602, "owner/other"), wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			selected.Doc.Front.Delete("github")
			other.Doc.Front.Delete("github")
			if tc.selected != "" {
				selected.Doc.Front.SetBlock("github", tc.selected)
			}
			if tc.other != "" {
				other.Doc.Front.SetBlock("github", tc.other)
			}
			if err := ensureIssueMappingUnique([]*store.Task{selected, other}, selected, 601, "owner/repo"); (err != nil) != tc.wantErr {
				t.Fatalf("mapping guard error=%v wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

func TestExactIssueLinkPlanReaderRejectsNonRegularAndDuplicateJSON(t *testing.T) {
	w := mirrorWorkspace(t)
	const id = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	path := exactIssuePlanPath(w, id)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "regular.json")
	if err := os.WriteFile(target, []byte(`{"schema":"bad"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := readExactIssueLinkPlan(w, id); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("symlink plan accepted: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("mkfifo", path).Run(); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	if _, err := readExactIssueLinkPlan(w, id); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("FIFO plan accepted or blocked: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"schema":"issue-link-batch/v2","schema":"issue-link-batch/v2","id":"`+id+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readExactIssueLinkPlan(w, id); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate JSON key accepted: %v", err)
	}
}
