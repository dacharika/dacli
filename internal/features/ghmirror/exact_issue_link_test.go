package ghmirror

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/mdstore"
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
