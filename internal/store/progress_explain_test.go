package store

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/procmon"
	"github.com/mlnomadpy/dacli/internal/team"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

func explainFixture(t *testing.T) (*workspace.Workspace, *Task) {
	t.Helper()
	w, err := workspace.Init(t.TempDir(), "explain")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "init", "-q", "-b", "main")
	cmd.Dir = w.Root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	if _, err := CreateProject(w, "a-root", "Core", "core", "goal", ""); err != nil {
		t.Fatal(err)
	}
	first, err := CreateTask(w, "a-root", "core", "foundation", TaskOpts{Accept: []string{"done"}, Estimate: "1,2,3", Claims: []string{"internal/store"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CreateTask(w, "a-root", "core", "dependent", TaskOpts{Accept: []string{"done"}, Estimate: "1,2,3", DependsOn: []string{first.ID}}); err != nil {
		t.Fatal(err)
	}
	for _, role := range []team.Role{
		{Name: "builder", Kind: "implementer", Grant: "rw", Runtime: "codex-rw", Scope: []string{"internal/**"}, Profile: team.ModelProfile{ID: "small", CostTier: 1}},
		{Name: "review-only", Kind: "reviewer", Grant: "ro", Runtime: "codex", Scope: []string{"**"}, Profile: team.ModelProfile{ID: "large", CostTier: 3}},
	} {
		if err := CreateRole(w, "a-root", role); err != nil {
			t.Fatal(err)
		}
	}
	return w, first
}

func TestWorkerExplainReportsDurableProgressAndExplicitAvailability(t *testing.T) {
	w, task := explainFixture(t)
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = w.Root
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	if err := os.WriteFile(filepath.Join(w.Root, "worker.go"), []byte("package worker\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "worker.go")
	git("-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-q", "-m", "fixture")
	commit := git("rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(w.Root, "worker.go"), []byte("package worker\n// changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	runID := "01KEXPLAINWORKER0000000001"
	runDir := w.RunDir(runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"worktree.txt":   w.Root + "\n",
		"transcript.log": "[tool: Edit]\nchanged worker.go\ngo test ./...\n",
		"usage.txt":      "input_tokens: 12\noutput_tokens: 7\nnum_turns: 2\ncost_usd: 0.125\n",
	} {
		if err := os.WriteFile(filepath.Join(runDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := procmon.WriteRecord(filepath.Join(runDir, "proc.txt"), procmon.Record{RunID: runID, Child: "a-worker", Task: task.ID, Role: "builder", Runtime: "codex", PID: os.Getpid(), PGID: os.Getpid(), Started: at.Add(-time.Minute), Claims: []string{"worker.go"}}); err != nil {
		t.Fatal(err)
	}
	delivery := DeliveryProjection{Schema: DeliverySchemaVersion, Version: 1, Project: "core", ObservedAt: at, Reconciled: true, Findings: []DeliveryFinding{{ID: "canonical-pr:" + task.ID, Classification: "canonical-pr", ObjectID: task.ID, Source: "github/checks", ObservedAt: at, Confidence: DeliveryKnown, Detail: "pr=#9 checks=pending", NextAction: "wait for checks"}}}
	p, err := BuildProgressExplain(w, "core", delivery, at)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Workers) != 1 {
		t.Fatalf("workers = %+v", p.Workers)
	}
	got := p.Workers[0]
	if got.Phase.Value != "verifying" || got.CurrentCommandCategory.Value != "test" || got.ElapsedMS.Value < 60_000 {
		t.Fatalf("phase/category/elapsed = %+v/%+v/%+v", got.Phase, got.CurrentCommandCategory, got.ElapsedMS)
	}
	if got.LastDurableActivity.Value == nil || len(got.ChangedPaths.Value) != 1 || got.ChangedPaths.Value[0] != "worker.go" || got.LastCommit.Value != commit {
		t.Fatalf("durable/git progress = activity:%+v paths:%+v commit:%+v", got.LastDurableActivity, got.ChangedPaths, got.LastCommit)
	}
	if !got.Usage.Value.Available || got.Usage.Value.OutputTokens != 7 || got.Usage.Value.CostUSD != 0.125 {
		t.Fatalf("usage = %+v", got.Usage)
	}
	if got.PullRequestChecks.Value.Classification != "canonical-pr" || got.NextTransition.Value == "" || got.RequiredOperatorAction.Value == "" {
		t.Fatalf("delivery transition = pr:%+v next:%+v operator:%+v", got.PullRequestChecks, got.NextTransition, got.RequiredOperatorAction)
	}
	for _, fact := range []struct {
		source string
		at     time.Time
	}{{got.Phase.Source, got.Phase.ObservedAt}, {got.CurrentCommandCategory.Source, got.CurrentCommandCategory.ObservedAt}, {got.ChangedPaths.Source, got.ChangedPaths.ObservedAt}, {got.Usage.Source, got.Usage.ObservedAt}, {got.PullRequestChecks.Source, got.PullRequestChecks.ObservedAt}} {
		if fact.source == "" || fact.at.IsZero() {
			t.Fatalf("unsourced worker fact: %+v", got)
		}
	}
}

func TestWorkerProgressFixturesCoverAgentLifecycleStates(t *testing.T) {
	for _, tc := range []struct {
		name, transcript, state, phase, category string
	}{
		{"editing worker", "[tool: edit]", "live", "implementing", "edit"},
		{"long-running test", "go test ./...", "live", "verifying", "test"},
		{"silent dead worker", "", "finished-unfinalized", "finished-unfinalized", "unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.transcript != "" {
				if err := os.WriteFile(filepath.Join(dir, "transcript.log"), []byte(tc.transcript), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			phase, category := workerPhase(dir, tc.state)
			if phase != tc.phase || category != tc.category {
				t.Fatalf("phase/category = %s/%s, want %s/%s", phase, category, tc.phase, tc.category)
			}
		})
	}
	for _, tc := range []struct {
		name, state        string
		landing            LandingExplain
		transition, action string
	}{
		{"awaiting owner", "handoff-required", LandingExplain{}, "awaiting-owner", "root action"},
		{"awaiting merge", "completed", LandingExplain{Classification: "canonical-pr", Confidence: DeliveryKnown}, "required-checks-or-merge", "none unless the diagnosis requests intervention"},
		{"external unknown", "completed", LandingExplain{Classification: "github-state-unknown", Confidence: DeliveryUnknown}, "awaiting-external-state", "restore external observability; do not infer success"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transition, action := workerTransition(tc.state, tc.landing, "root action")
			if transition != tc.transition || action != tc.action {
				t.Fatalf("transition/action = %q/%q, want %q/%q", transition, action, tc.transition, tc.action)
			}
		})
	}
}

func TestBuildProgressExplainRetainsRejectedRolesAndCanonicalLanding(t *testing.T) {
	w, first := explainFixture(t)
	at := time.Unix(100, 0).UTC()
	delivery := DeliveryProjection{Schema: DeliverySchemaVersion, Version: 1, Project: "core", ObservedAt: at, Reconciled: true, Findings: []DeliveryFinding{{
		ID: "canonical-pr:" + first.ID, Classification: "canonical-pr", ObjectID: first.ID, Source: "github", ObservedAt: at,
		Confidence: DeliveryKnown, Detail: "pr=#9", NextAction: "wait for required checks", RelatedRefs: []DeliveryRef{{Kind: "pull_request", ID: "#9"}},
	}}}
	p, err := BuildProgressExplain(w, "core", delivery, at)
	if err != nil {
		t.Fatal(err)
	}
	if p.Schema != ProgressExplainSchema || len(p.Tasks) != 2 {
		t.Fatalf("projection envelope/tasks = %+v", p)
	}
	var got *TaskExplain
	for i := range p.Tasks {
		if p.Tasks[i].ID.Value == first.ID {
			got = &p.Tasks[i]
		}
	}
	if got == nil {
		t.Fatal("foundation task absent")
	}
	if got.Landing.Value.Classification != "canonical-pr" || got.Landing.Source != "github" || got.NextAction.Value == "" {
		t.Fatalf("canonical landing/next action lost: %+v", got)
	}
	if got.Rank.Value != 1 || got.Slack.Value == nil || len(got.Claims.Value) != 1 {
		t.Fatalf("rank/slack/claims incomplete: %+v", got)
	}
	rejected := got.RoleRouting.Value.Candidate("review-only")
	if rejected == nil || rejected.Eligible || len(rejected.Exclusions) == 0 || !strings.Contains(strings.Join(rejected.Exclusions, " "), "kind") {
		t.Fatalf("rejected reviewer role was dropped or unexplained: %+v", got.RoleRouting.Value)
	}
	if selected := got.RoleRouting.Value.Selected.Role; selected != "builder" {
		t.Fatalf("selected role = %q, want builder", selected)
	}
	for _, fact := range []struct {
		name   string
		source string
		at     time.Time
	}{
		{"id", got.ID.Source, got.ID.ObservedAt}, {"title", got.Title.Source, got.Title.ObservedAt}, {"status", got.Status.Source, got.Status.ObservedAt},
		{"rank", got.Rank.Source, got.Rank.ObservedAt}, {"slack", got.Slack.Source, got.Slack.ObservedAt}, {"blockers", got.Blockers.Source, got.Blockers.ObservedAt},
		{"claims", got.Claims.Source, got.Claims.ObservedAt}, {"parent", got.Parent.Source, got.Parent.ObservedAt}, {"aggregate", got.Aggregate.Source, got.Aggregate.ObservedAt},
		{"landing", got.Landing.Source, got.Landing.ObservedAt}, {"routing", got.RoleRouting.Source, got.RoleRouting.ObservedAt}, {"next", got.NextAction.Source, got.NextAction.ObservedAt},
	} {
		if fact.source == "" || fact.at.IsZero() {
			t.Errorf("%s is missing source/observed time", fact.name)
		}
	}
}

func TestBuildProgressExplainResolvesCompletedCrossProjectDependencies(t *testing.T) {
	w, prerequisite := explainFixture(t)
	if err := MoveTask(w, prerequisite, model.StatusDone); err != nil {
		t.Fatalf("mark core prerequisite done: %v", err)
	}
	if _, err := CreateProject(w, "a-root", "Operator", "operator", "goal", ""); err != nil {
		t.Fatalf("create operator project: %v", err)
	}
	qualified, err := CreateTask(w, "a-root", "operator", "qualified dependency", TaskOpts{
		Accept: []string{"explain cross-project progress"}, Estimate: "1,2,3", Claims: []string{"internal/store"},
		DependsOn: []string{"core/" + prerequisite.ID},
	})
	if err != nil {
		t.Fatalf("create qualified dependent task: %v", err)
	}
	stableID, err := CreateTask(w, "a-root", "operator", "stable ID dependency", TaskOpts{
		Accept: []string{"explain cross-project progress"}, Estimate: "1,2,3", Claims: []string{"internal/store"},
		DependsOn: []string{prerequisite.ID},
	})
	if err != nil {
		t.Fatalf("create stable-ID dependent task: %v", err)
	}
	at := time.Now().UTC()
	delivery := DeliveryProjection{Schema: DeliverySchemaVersion, Version: 1, Project: "operator", ObservedAt: at}
	p, err := BuildProgressExplain(w, "operator", delivery, at)
	if err != nil {
		t.Fatalf("build progress explain: %v", err)
	}
	if p.Project != "operator" || len(p.Tasks) != 2 {
		t.Fatalf("project-scoped task rows = %+v", p.Tasks)
	}
	byID := make(map[string]TaskExplain, len(p.Tasks))
	for _, task := range p.Tasks {
		byID[task.ID.Value] = task
		if task.ID.Value != qualified.ID && task.ID.Value != stableID.ID {
			t.Errorf("foreign task row leaked into operator explain: %s", task.ID.Value)
		}
	}
	for _, taskID := range []string{qualified.ID, stableID.ID} {
		task, ok := byID[taskID]
		if !ok {
			t.Fatalf("dependent operator task %s missing from projection", taskID)
		}
		if len(task.Blockers.Value) != 0 || strings.HasPrefix(task.NextAction.Value, "resolve:") || strings.Contains(task.NextAction.Value, "dependency") {
			t.Errorf("completed cross-project dependency was reported as a blocker: task=%s blockers=%v next=%q", taskID, task.Blockers.Value, task.NextAction.Value)
		}
		if strings.HasPrefix(task.NextAction.Value, "repair role eligibility") {
			t.Errorf("task with a satisfied dependency was not recognized as ready: task=%s next=%q", taskID, task.NextAction.Value)
		}
		if task.Landing.Value.Classification != "unobserved" || task.Landing.Value.Confidence != DeliveryUnknown {
			t.Errorf("local dependency resolution implied remote landing success: %+v", task.Landing.Value)
		}
	}
}

func TestBuildProgressExplainKeepsMissingAndAmbiguousDependenciesExplicit(t *testing.T) {
	w, _ := explainFixture(t)
	if _, err := CreateProject(w, "a-root", "Operator", "operator", "goal", ""); err != nil {
		t.Fatalf("create operator project: %v", err)
	}
	if _, err := CreateProject(w, "a-root", "Other", "other", "goal", ""); err != nil {
		t.Fatalf("create other project: %v", err)
	}
	for _, project := range []string{"core", "other"} {
		if _, err := CreateTask(w, "a-root", project, "shared reference", TaskOpts{Accept: []string{"exists"}}); err != nil {
			t.Fatalf("create ambiguous prerequisite in %s: %v", project, err)
		}
	}
	absent, err := CreateTask(w, "a-root", "operator", "missing prerequisite", TaskOpts{
		Accept: []string{"surface missing dependency"}, Estimate: "1,2,3", DependsOn: []string{"missing-dep-1210"},
	})
	if err != nil {
		t.Fatalf("create missing dependency task: %v", err)
	}
	ambiguous, err := CreateTask(w, "a-root", "operator", "ambiguous prerequisite", TaskOpts{
		Accept: []string{"surface ambiguous dependency"}, Estimate: "1,2,3", DependsOn: []string{"shared-reference"},
	})
	if err != nil {
		t.Fatalf("create ambiguous dependency task: %v", err)
	}
	at := time.Now().UTC()
	p, err := BuildProgressExplain(w, "operator", DeliveryProjection{Schema: DeliverySchemaVersion, Version: 1, Project: "operator", ObservedAt: at}, at)
	if err != nil {
		t.Fatalf("build progress explain: %v", err)
	}
	if len(p.Tasks) != 2 {
		t.Fatalf("project-scoped task rows = %+v", p.Tasks)
	}
	byID := make(map[string]TaskExplain, len(p.Tasks))
	for _, task := range p.Tasks {
		byID[task.ID.Value] = task
		if task.ID.Value != absent.ID && task.ID.Value != ambiguous.ID {
			t.Errorf("foreign task row leaked into operator explain: %s", task.ID.Value)
		}
	}
	for _, tc := range []struct {
		id, ref, reason string
	}{{absent.ID, "missing-dep-1210", "not found"}, {ambiguous.ID, "shared-reference", "ambiguous"}} {
		task := byID[tc.id]
		blockers := strings.Join(task.Blockers.Value, "\n")
		if !strings.Contains(blockers, tc.ref) || !strings.Contains(blockers, tc.reason) {
			t.Errorf("dependency problem was not explicit for %s: blockers=%q", tc.ref, blockers)
		}
		if !strings.HasPrefix(task.NextAction.Value, "resolve:") {
			t.Errorf("dependency problem did not guide the next action for %s: %q", tc.ref, task.NextAction.Value)
		}
	}
}

func TestExplainCacheMarksEveryFallbackFactStaleAndIsBounded(t *testing.T) {
	at := time.Unix(100, 0).UTC()
	fact := func(value string) Observed[string] { return observed(value, "fixture", at, false) }
	projection := ProgressExplain{Schema: ProgressExplainSchema, Version: 1, Project: "core", ObservedAt: at, Tasks: []TaskExplain{{
		ID: fact("t-1"), Title: fact("title"), Status: fact("open"), Completion: fact(""), Rank: observed(1, "fixture", at, false), Slack: observed[*float64](nil, "fixture", at, false),
		Blockers: observed([]string{}, "fixture", at, false), Claims: observed([]string{}, "fixture", at, false), Parent: fact(""), Aggregate: observed(AggregateExplain{}, "fixture", at, false),
		Landing: observed(LandingExplain{}, "fixture", at, false), RoleRouting: observed(team.Explanation{Candidates: []team.CandidateExplanation{{Role: "rejected", Eligible: false, Exclusions: []string{"wrong kind"}}}}, "fixture", at, false), NextAction: fact("assign"),
	}}, Workers: []WorkerExplain{{RunID: fact("run"), AgentID: fact("agent"), TaskID: fact("t-1"), Role: fact("builder"), Runtime: fact("codex"), State: fact("live"), Claims: observed([]string{"internal"}, "fixture", at, false), NextAction: fact("observe")}}}
	cache := NewExplainCache(1, time.Second, time.Minute)
	if _, err := cache.Get("core", at, func() (ProgressExplain, error) { return projection, nil }); err != nil {
		t.Fatal(err)
	}
	got, err := cache.Get("core", at.Add(2*time.Second), func() (ProgressExplain, error) { return ProgressExplain{}, errors.New("fixture outage") })
	if err != nil {
		t.Fatal(err)
	}
	if !got.Stale || got.CacheState != "stale-fallback" || !strings.Contains(got.Warning, "fixture outage") {
		t.Fatalf("fallback freshness = stale:%t cache:%s warning:%q", got.Stale, got.CacheState, got.Warning)
	}
	task, worker := got.Tasks[0], got.Workers[0]
	if !task.ID.Stale || !task.Status.Stale || !task.Completion.Stale || !task.RoleRouting.Stale || !task.Landing.Stale || !task.NextAction.Stale || !worker.Role.Stale || !worker.State.Stale || !worker.Phase.Stale || !worker.CurrentCommandCategory.Stale || !worker.LastDurableActivity.Stale || !worker.ChangedPaths.Stale || !worker.LastCommit.Stale || !worker.Usage.Stale || !worker.PullRequestChecks.Stale || !worker.NextTransition.Stale || !worker.RequiredOperatorAction.Stale || !worker.NextAction.Stale {
		t.Fatalf("stale cache escaped as current: task=%+v worker=%+v", task, worker)
	}
	if candidate := task.RoleRouting.Value.Candidate("rejected"); candidate == nil || candidate.Eligible {
		t.Fatalf("stale fallback lost rejected role evidence: %+v", task.RoleRouting.Value)
	}
	if _, err := cache.Get("core", at.Add(2*time.Minute), func() (ProgressExplain, error) { return ProgressExplain{}, errors.New("still unavailable") }); err == nil {
		t.Fatal("cache served evidence beyond its maximum stale age")
	}
	if _, err := cache.Get("other", at.Add(3*time.Second), func() (ProgressExplain, error) { p := projection; p.Project = "other"; return p, nil }); err != nil {
		t.Fatal(err)
	}
	if len(cache.entries) != 1 || cache.entries["other"].projection.Project != "other" {
		t.Fatalf("bounded cache did not evict LRU entry: %+v", cache.entries)
	}
}
