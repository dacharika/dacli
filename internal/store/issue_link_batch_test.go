package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/mdstore"
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

func TestIssueLinkBatchAllowsUnselectedLegacyMappingSeparator(t *testing.T) {
	w, err := workspace.Init(t.TempDir(), "issue-link-legacy-separator")
	if err != nil {
		t.Fatal(err)
	}
	project, err := CreateProject(w, "a-root", "Core", "core", "goal", "")
	if err != nil {
		t.Fatal(err)
	}
	project.Doc.Front.Set("github_repo", "dacharika/dacli")
	if err := SaveProject(project); err != nil {
		t.Fatal(err)
	}
	var tasks []*Task
	for _, title := range []string{"Selected issue one", "Selected issue two", "Selected issue three", "Unselected legacy issue task"} {
		task, err := CreateTask(w, "a-root", "core", title, TaskOpts{Accept: []string{"preserve this criterion"}})
		if err != nil {
			t.Fatal(err)
		}
		tasks = append(tasks, task)
	}
	tasks[3].Doc.Front.SetBlock("github", "  issue: 1192\n  repo: dacharika/dacli\n\n")
	if err := SaveTask(tasks[3]); err != nil {
		t.Fatal(err)
	}
	unselectedBefore, err := os.ReadFile(tasks[3].Path)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := mdstore.Parse(string(unselectedBefore))
	if err != nil {
		t.Fatal(err)
	}
	legacyBlock, ok := parsed.Front.GetBlock("github")
	if !ok || legacyBlock != "  issue: 1192\n  repo: dacharika/dacli\n\n" {
		t.Fatalf("mdstore did not preserve the legacy trailing separator: found=%v block=%q", ok, legacyBlock)
	}
	targets := make([]IssueLinkTarget, 0, 3)
	for i, task := range tasks[:3] {
		digest := strings.Repeat(string(rune('a'+i)), 64)
		number := 1301 + i
		targets = append(targets, IssueLinkTarget{TaskID: task.ID, Project: "core", IssueNumber: number, Repository: "dacharika/dacli", IssueDigest: digest, MappingBlock: fmt.Sprintf("  issue: %d\n  repo: dacharika/dacli", number)})
	}
	plan, err := PlanIssueLinkBatch(w, "core", "dacharika/dacli", targets, time.Unix(400, 0))
	if err != nil {
		t.Fatalf("plan exact three-issue selection: %v", err)
	}
	if err := ValidateIssueLinkBatchBindings(w, plan); err != nil {
		t.Fatalf("unrelated legacy mapping prevented exact selection: %v", err)
	}
	commits := 0
	validate := func() error { return ValidateIssueLinkBatchBindings(w, plan) }
	apply := func() error {
		return ApplyIssueLinkBatchWithValidation(w, plan, func(_ string, commit func() error) error {
			commits++
			return commit()
		}, validate)
	}
	if err := apply(); err != nil {
		t.Fatalf("apply exact three-issue selection: %v", err)
	}
	if commits != 3 {
		t.Fatalf("selected task commit count = %d, want exactly 3", commits)
	}
	for i, task := range tasks[:3] {
		linked, err := FindTask(w, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		block, ok := linked.Doc.Front.GetBlock("github")
		if !ok || block != targets[i].MappingBlock {
			t.Fatalf("selected task %s mapping = (%v) %q, want %q", task.ID, ok, block, targets[i].MappingBlock)
		}
		if got := linked.Acceptance(); len(got) != 1 || got[0].Text != "preserve this criterion" {
			t.Fatalf("selected task %s acceptance changed: %+v", task.ID, got)
		}
	}
	if after, err := os.ReadFile(tasks[3].Path); err != nil || !bytesEqual(after, unselectedBefore) {
		t.Fatalf("unselected task bytes changed: err=%v", err)
	}
	commits = 0
	if err := apply(); err != nil {
		t.Fatalf("idempotent exact selection replay: %v", err)
	}
	if commits != 0 {
		t.Fatalf("idempotent replay committed %d selected files", commits)
	}
	if after, err := os.ReadFile(tasks[3].Path); err != nil || !bytesEqual(after, unselectedBefore) {
		t.Fatalf("unselected legacy task bytes changed after replay: err=%v", err)
	}
}

func TestParseExactIssueMappingOnlySkipsBlankLines(t *testing.T) {
	valid := []string{
		"  issue: 1192\n  repo: dacharika/dacli\n\n",
		"\n \t\nissue: 1192\nrepo: dacharika/dacli\n  \n",
	}
	for _, block := range valid {
		issue, repo, err := ParseIssueLinkMapping(block)
		if err != nil || issue != 1192 || repo != "dacharika/dacli" {
			t.Errorf("ParseIssueLinkMapping(%q) = (%d, %q, %v), want (1192, dacharika/dacli, nil)", block, issue, repo, err)
		}
	}
	invalid := []struct {
		name  string
		block string
	}{
		{name: "empty", block: ""},
		{name: "whitespace only", block: " \t\n \t "},
		{name: "malformed nonblank", block: "issue: 1192\nrepo: dacharika/dacli\nnot-a-field"},
		{name: "unknown field", block: "issue: 1192\nrepo: dacharika/dacli\nextra: value"},
		{name: "duplicate key", block: "issue: 1192\nissue: 1192\nrepo: dacharika/dacli"},
		{name: "duplicate repository", block: "issue: 1192\nrepo: dacharika/dacli\nrepo: dacharika/other"},
		{name: "invalid number", block: "issue: 0\nrepo: dacharika/dacli"},
		{name: "invalid repository", block: "issue: 1192\nrepo: dacharika"},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := ParseIssueLinkMapping(test.block); err == nil {
				t.Fatalf("ParseIssueLinkMapping accepted invalid mapping %q", test.block)
			}
		})
	}
}

func TestIssueLinkBatchAppliedJournalRefusesRestoredBeforeBytes(t *testing.T) {
	w, err := workspace.Init(t.TempDir(), "issue-link-rollback")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CreateProject(w, "a-root", "Core", "core", "goal", ""); err != nil {
		t.Fatal(err)
	}
	task, err := CreateTask(w, "a-root", "core", "Selected", TaskOpts{})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := PlanIssueLinkBatch(w, "core", "owner/repo", []IssueLinkTarget{{TaskID: task.ID, Project: "core", IssueNumber: 601, Repository: "owner/repo", IssueDigest: strings.Repeat("a", 64), MappingBlock: "  issue: 601\n  repo: owner/repo"}}, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(task.Path)
	if err != nil {
		t.Fatal(err)
	}
	apply := func() error {
		return ApplyIssueLinkBatch(w, plan, func(_ string, commit func() error) error { return commit() })
	}
	if err := apply(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(task.Path, before, 0o600); err != nil {
		t.Fatal(err)
	}
	err = apply()
	if err == nil || !strings.Contains(err.Error(), "restored to its preview bytes") {
		t.Fatalf("replay after journaled write rollback = %v, want explicit refusal", err)
	}
	got, err := os.ReadFile(task.Path)
	if err != nil || !bytesEqual(got, before) {
		t.Fatalf("refusal changed rolled-back task bytes: err=%v", err)
	}
}

func TestIssueLinkRecoveryRejectsRecomputedAcceptanceImport(t *testing.T) {
	w, err := workspace.Init(t.TempDir(), "issue-link-no-acceptance-import")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CreateProject(w, "a-root", "Core", "core", "goal", ""); err != nil {
		t.Fatal(err)
	}
	task, err := CreateTask(w, "a-root", "core", "Human issue task", TaskOpts{Accept: []string{"keep this acceptance"}})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := PlanIssueLinkBatch(w, "core", "owner/repo", []IssueLinkTarget{{TaskID: task.ID, Project: "core", IssueNumber: 601, Repository: "owner/repo", IssueDigest: strings.Repeat("a", 64), MappingBlock: "  issue: 601\n  repo: owner/repo", LogEntry: "GitHub issue #601 linked by exact selection; source sha256:" + strings.Repeat("a", 64)}}, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	err = ApplyIssueLinkBatch(w, plan, func(_ string, _ func() error) error {
		return errors.New("injected interruption before first task write")
	})
	if err == nil || !strings.Contains(err.Error(), "recovery required") {
		t.Fatalf("staging interruption = %v, want recoverable selected-set interruption", err)
	}
	journalPath := filepath.Join(w.Root, workspace.Dir, "recovery", "issue-links", plan.ID+".json")
	data, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	var recovery issueLinkRecovery
	if err := json.Unmarshal(data, &recovery); err != nil {
		t.Fatal(err)
	}
	if len(recovery.Items) != 1 || !strings.Contains(string(recovery.Items[0].After), "keep this acceptance") {
		t.Fatalf("fixture recovery snapshot missing expected acceptance: %+v", recovery.Items)
	}
	recovery.Items[0].After = []byte(strings.ReplaceAll(string(recovery.Items[0].After), "keep this acceptance", "issue supplied acceptance"))
	data, err = json.Marshal(recovery)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(journalPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	err = ApplyIssueLinkBatch(w, plan, func(_ string, commit func() error) error { return commit() })
	if err == nil || !strings.Contains(err.Error(), "target bytes do not match the immutable plan") {
		t.Fatalf("replayed journal accepted acceptance import: %v", err)
	}
	got, err := os.ReadFile(task.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "keep this acceptance") || strings.Contains(string(got), "issue supplied acceptance") {
		t.Fatalf("journal replay changed acceptance: %s", got)
	}
}

func TestIssueLinkBatchRejectsNonRegularRecoveryJournal(t *testing.T) {
	w, err := workspace.Init(t.TempDir(), "issue-link-journal-file")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CreateProject(w, "a-root", "Core", "core", "goal", ""); err != nil {
		t.Fatal(err)
	}
	task, err := CreateTask(w, "a-root", "core", "Selected", TaskOpts{})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := PlanIssueLinkBatch(w, "core", "owner/repo", []IssueLinkTarget{{TaskID: task.ID, Project: "core", IssueNumber: 601, Repository: "owner/repo", IssueDigest: strings.Repeat("a", 64), MappingBlock: "  issue: 601\n  repo: owner/repo"}}, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(w.Root, workspace.Dir, "recovery", "issue-links", plan.ID+".json")
	if err := os.MkdirAll(filepath.Dir(journal), 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "regular.json")
	if err := os.WriteFile(target, []byte(`{"schema":"issue-link-recovery/v1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, journal); err != nil {
		t.Fatal(err)
	}
	if err := ApplyIssueLinkBatch(w, plan, func(_ string, commit func() error) error { return commit() }); err == nil {
		t.Fatal("symlink recovery journal was accepted")
	}
	if err := os.Remove(journal); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("mkfifo", journal).Run(); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	if err := ApplyIssueLinkBatch(w, plan, func(_ string, commit func() error) error { return commit() }); err == nil || !strings.Contains(err.Error(), "regular") {
		t.Fatalf("nonregular recovery journal was not rejected promptly: %v", err)
	}
}

func TestIssueLinkBatchRecoveryReaderRejectsDuplicateJSONKeys(t *testing.T) {
	w, err := workspace.Init(t.TempDir(), "issue-link-journal-json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CreateProject(w, "a-root", "Core", "core", "goal", ""); err != nil {
		t.Fatal(err)
	}
	task, err := CreateTask(w, "a-root", "core", "Selected", TaskOpts{})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := PlanIssueLinkBatch(w, "core", "owner/repo", []IssueLinkTarget{{TaskID: task.ID, Project: "core", IssueNumber: 601, Repository: "owner/repo", IssueDigest: strings.Repeat("a", 64), MappingBlock: "  issue: 601\n  repo: owner/repo"}}, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	apply := func() error {
		return ApplyIssueLinkBatch(w, plan, func(_ string, commit func() error) error { return commit() })
	}
	if err := apply(); err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(w.Root, workspace.Dir, "recovery", "issue-links", plan.ID+".json")
	raw, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(journal, append([]byte(`{"schema":"duplicate",`), raw[1:]...), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := apply(); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("duplicate-key recovery journal was accepted: %v", err)
	}
}

func TestIssueLinkBatchRechecksOutsideMappingAtEffectBoundary(t *testing.T) {
	w, err := workspace.Init(t.TempDir(), "issue-link-final-map")
	if err != nil {
		t.Fatal(err)
	}
	project, err := CreateProject(w, "a-root", "Core", "core", "goal", "")
	if err != nil {
		t.Fatal(err)
	}
	project.Doc.Front.Set("github_repo", "owner/repo")
	if err := SaveProject(project); err != nil {
		t.Fatal(err)
	}
	selected, err := CreateTask(w, "a-root", "core", "Selected", TaskOpts{})
	if err != nil {
		t.Fatal(err)
	}
	competitor, err := CreateTask(w, "a-root", "core", "Competitor", TaskOpts{})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := PlanIssueLinkBatch(w, "core", "owner/repo", []IssueLinkTarget{{TaskID: selected.ID, Project: "core", IssueNumber: 601, Repository: "owner/repo", IssueDigest: strings.Repeat("a", 64), MappingBlock: "  issue: 601\n  repo: owner/repo"}}, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	selectedBefore, err := os.ReadFile(selected.Path)
	if err != nil {
		t.Fatal(err)
	}
	checks := 0
	validate := func() error {
		checks++
		if checks == 2 { // The first check is preflight; this is inside the selected task lock.
			fresh, err := FindTask(w, competitor.ID)
			if err != nil {
				return err
			}
			fresh.Doc.Front.SetBlock("github", "  issue: 601\n  repo: owner/repo")
			if err := SaveTask(fresh); err != nil {
				return err
			}
		}
		return ValidateIssueLinkBatchBindings(w, plan)
	}
	err = ApplyIssueLinkBatchWithValidation(w, plan, func(_ string, commit func() error) error { return commit() }, validate)
	if err == nil || !strings.Contains(err.Error(), "already mapped to task") {
		t.Fatalf("late outside mapping conflict = %v, want boundary refusal", err)
	}
	got, err := os.ReadFile(selected.Path)
	if err != nil || !bytesEqual(got, selectedBefore) {
		t.Fatalf("selected task changed despite boundary conflict: %v", err)
	}
}

func TestIssueLinkBatchRechecksProjectBindingAtEffectBoundary(t *testing.T) {
	w, err := workspace.Init(t.TempDir(), "issue-link-final-project")
	if err != nil {
		t.Fatal(err)
	}
	project, err := CreateProject(w, "a-root", "Core", "core", "goal", "")
	if err != nil {
		t.Fatal(err)
	}
	project.Doc.Front.Set("github_repo", "owner/repo")
	project.Doc.Front.Set("github_public_confirmed", "owner/repo")
	if err := SaveProject(project); err != nil {
		t.Fatal(err)
	}
	selected, err := CreateTask(w, "a-root", "core", "Selected", TaskOpts{})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := PlanIssueLinkBatch(w, "core", "owner/repo", []IssueLinkTarget{{TaskID: selected.ID, Project: "core", IssueNumber: 602, Repository: "owner/repo", IssueDigest: strings.Repeat("b", 64), MappingBlock: "  issue: 602\n  repo: owner/repo"}}, time.Unix(101, 0))
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(selected.Path)
	if err != nil {
		t.Fatal(err)
	}
	checks := 0
	validate := func() error {
		checks++
		if checks == 2 { // The final check runs under authority -> selected task lock.
			fresh, err := LoadProject(w, "core")
			if err != nil {
				return err
			}
			fresh.Doc.Front.Delete("github_public_confirmed")
			if err := SaveProject(fresh); err != nil {
				return err
			}
		}
		return ValidateIssueLinkBatchBindings(w, plan)
	}
	err = ApplyIssueLinkBatchWithValidation(w, plan, func(_ string, commit func() error) error { return commit() }, validate)
	if err == nil || !strings.Contains(err.Error(), "consent changed") {
		t.Fatalf("late public-consent removal = %v, want effect-boundary refusal", err)
	}
	got, err := os.ReadFile(selected.Path)
	if err != nil || !bytesEqual(got, before) {
		t.Fatalf("selected task changed despite consent withdrawal: %v", err)
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
