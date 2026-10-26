package store

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/workspace"
)

func TestIssueLinkBatchPrevalidatesAndRecoversOnlySelectedTasks(t *testing.T) {
	w, err := workspace.Init(t.TempDir(), "issue-links")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CreateProject(w, "a-root", "Core", "core", "goal", ""); err != nil {
		t.Fatal(err)
	}
	var tasks []*Task
	for _, title := range []string{"Human task one", "Human task two", "Human task three", "Unselected fourth task"} {
		task, err := CreateTask(w, "a-root", "core", title, TaskOpts{Accept: []string{"keep this acceptance"}})
		if err != nil {
			t.Fatal(err)
		}
		tasks = append(tasks, task)
	}
	targets := make([]IssueLinkTarget, 0, 3)
	for i, task := range tasks[:3] {
		targets = append(targets, IssueLinkTarget{TaskID: task.ID, Project: "core", IssueNumber: 601 + i, Repository: "owner/repo", IssueDigest: strings.Repeat("a", 64), MappingBlock: fmt.Sprintf("  issue: %d\n  repo: owner/repo", 601+i), LogEntry: fmt.Sprintf("GitHub issue #%d linked by exact selection; source sha256:%s", 601+i, strings.Repeat("a", 64))})
	}
	plan, err := PlanIssueLinkBatch(w, "core", "owner/repo", targets, time.Unix(100, 0))
	if err != nil {
		t.Fatalf("preview plan: %v", err)
	}
	unselectedBefore, _ := os.ReadFile(tasks[3].Path)
	calls := 0
	err = ApplyIssueLinkBatch(w, plan, func(_ string, commit func() error) error {
		calls++
		if calls == 2 {
			return errors.New("injected second selected write failure")
		}
		return commit()
	})
	if err == nil || !strings.Contains(err.Error(), "recovery required") {
		t.Fatalf("injected partial apply = %v", err)
	}
	first, _ := os.ReadFile(tasks[0].Path)
	second, _ := os.ReadFile(tasks[1].Path)
	third, _ := os.ReadFile(tasks[2].Path)
	if !strings.Contains(string(first), "issue: 601") || string(second) == "" || string(third) == "" {
		t.Fatalf("expected only first selected target to commit")
	}
	if strings.Contains(string(second), "repo: owner/repo") || strings.Contains(string(third), "repo: owner/repo") {
		t.Fatal("failure wrote a task after the injected second-write boundary")
	}
	if after, _ := os.ReadFile(tasks[3].Path); string(after) != string(unselectedBefore) {
		t.Fatal("unselected task bytes changed during partial apply")
	}
	if err := ApplyIssueLinkBatch(w, plan, func(_ string, commit func() error) error { return commit() }); err != nil {
		t.Fatalf("selected-only recovery replay: %v", err)
	}
	for i, task := range tasks[:3] {
		after, err := os.ReadFile(task.Path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(after), fmt.Sprintf("issue: %d", 601+i)) || !strings.Contains(string(after), "repo: owner/repo") || strings.Count(string(after), "source sha256:"+strings.Repeat("a", 64)) != 1 {
			t.Fatalf("selected task %d mapping/history after replay: %s", i, after)
		}
	}
	if after, _ := os.ReadFile(tasks[3].Path); string(after) != string(unselectedBefore) {
		t.Fatal("unselected task bytes changed during recovery")
	}
	if err := ApplyIssueLinkBatch(w, plan, func(_ string, commit func() error) error { return commit() }); err != nil {
		t.Fatalf("idempotent replay: %v", err)
	}
	for i, task := range tasks[:3] {
		after, _ := os.ReadFile(task.Path)
		if strings.Count(string(after), "source sha256:"+strings.Repeat("a", 64)) != 1 {
			t.Fatalf("task %d duplicate link history after replay", i)
		}
	}
}

func TestIssueLinkBatchRejectsChangedThirdTargetBeforeEffects(t *testing.T) {
	w, err := workspace.Init(t.TempDir(), "issue-links-drift")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CreateProject(w, "a-root", "Core", "core", "goal", ""); err != nil {
		t.Fatal(err)
	}
	var targets []IssueLinkTarget
	var tasks []*Task
	for i := 0; i < 3; i++ {
		task, err := CreateTask(w, "a-root", "core", "Task "+string(rune('1'+i)), TaskOpts{})
		if err != nil {
			t.Fatal(err)
		}
		tasks = append(tasks, task)
		targets = append(targets, IssueLinkTarget{TaskID: task.ID, Project: "core", IssueNumber: 700 + i, Repository: "owner/repo", IssueDigest: strings.Repeat("b", 64), MappingBlock: fmt.Sprintf("  issue: %d\n  repo: owner/repo", 700+i), LogEntry: fmt.Sprintf("GitHub issue #%d linked by exact selection; source sha256:%s", 700+i, strings.Repeat("b", 64))})
	}
	plan, err := PlanIssueLinkBatch(w, "core", "owner/repo", targets, time.Unix(200, 0))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tasks[2].Path, []byte("moved after preview"), 0o600); err != nil {
		t.Fatal(err)
	}
	var commits int
	err = ApplyIssueLinkBatch(w, plan, func(_ string, commit func() error) error { commits++; return commit() })
	if err == nil || commits != 0 {
		t.Fatalf("changed third task must refuse before selected effects: err=%v protected calls=%d", err, commits)
	}
}
