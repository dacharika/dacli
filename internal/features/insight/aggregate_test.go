package insight

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

func aggregateInsightFixture(t *testing.T) (*workspace.Workspace, *store.Task) {
	t.Helper()
	w, err := workspace.Init(t.TempDir(), "aggregate-insight")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProject(w, "a-root", "Core", "core", "goal", ""); err != nil {
		t.Fatal(err)
	}
	parent, err := store.CreateTask(w, "a-root", "core", "aggregate milestone", store.TaskOpts{Accept: []string{"children done"}, Estimate: "8,13,21"})
	if err != nil {
		t.Fatal(err)
	}
	for _, title := range []string{"one", "two", "three", "four"} {
		if _, err := store.CreateTask(w, "a-root", "core", title, store.TaskOpts{Parent: parent.ID, Accept: []string{title}, Estimate: "1,2,3"}); err != nil {
			t.Fatal(err)
		}
	}
	plan, _ := store.BuildAggregateRepairPlan(w, parent)
	if _, err := store.ApplyAggregateRepairPlan(w, parent, plan.ID); err != nil {
		t.Fatal(err)
	}
	return w, parent
}

func TestNextCriticalPathAndWBSAgreeOnAggregateState(t *testing.T) {
	w, parent := aggregateInsightFixture(t)
	for name, run := range map[string]func(*clikit.Ctx) error{
		"next":          func(ctx *clikit.Ctx) error { return cmdNext(ctx, []string{"--project", "core", "--parallel", "10"}) },
		"critical-path": func(ctx *clikit.Ctx) error { return cmdCriticalPath(ctx, []string{"--project", "core"}) },
		"wbs":           func(ctx *clikit.Ctx) error { return cmdWBS(ctx, []string{"--project", "core"}) },
	} {
		t.Run(name, func(t *testing.T) {
			out := &bytes.Buffer{}
			ctx := &clikit.Ctx{Cwd: w.Root, Stdout: out, Stderr: &bytes.Buffer{}}
			if err := run(ctx); err != nil {
				t.Fatal(err)
			}
			text := out.String()
			if name == "wbs" {
				if !strings.Contains(text, "aggregate 0/4") {
					t.Fatalf("WBS omitted derived aggregate progress:\n%s", text)
				}
			} else if strings.Contains(text, parent.Slug) {
				t.Fatalf("%s scheduled aggregate parent:\n%s", name, text)
			}
		})
	}
}

// scheduleTask builds only disposable canonical records; the real public view
// handlers and existing CPM engine produce every measured endpoint below.
func scheduleTask(t *testing.T, w *workspace.Workspace, title string, duration int, parent string, deps ...string) *store.Task {
	t.Helper()
	task, err := store.CreateTask(w, "a-root", "core", title, store.TaskOpts{Accept: []string{title + " works"}, Estimate: fmt.Sprintf("%d,%d,%d", duration, duration, duration), Parent: parent, DependsOn: deps})
	if err != nil {
		t.Fatal(err)
	}
	return task
}
func scheduleAggregate(t *testing.T, w *workspace.Workspace, title string, children ...*store.Task) *store.Task {
	t.Helper()
	task := scheduleTask(t, w, title, 100, "")
	ids := []string{}
	for _, child := range children {
		ids = append(ids, child.ID)
	}
	task.Doc.Front.Set("task_kind", store.TaskKindAggregate)
	task.Doc.Front.SetList("aggregate_children", ids)
	if err := store.SaveTask(task); err != nil {
		t.Fatal(err)
	}
	return task
}
func scheduleWorkspace(t *testing.T) *workspace.Workspace {
	t.Helper()
	w, err := workspace.Init(t.TempDir(), "schedule")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProject(w, "a-root", "Core", "core", "goal", ""); err != nil {
		t.Fatal(err)
	}
	return w
}

type measuredSchedule struct {
	ID       string  `json:"id"`
	Start    float64 `json:"early_start"`
	End      float64 `json:"early_finish"`
	Duration float64 `json:"duration"`
	Slack    float64 `json:"slack"`
}

func readSchedule(t *testing.T, w *workspace.Workspace) (float64, map[string]measuredSchedule) {
	t.Helper()
	out := &bytes.Buffer{}
	ctx := &clikit.Ctx{Cwd: w.Root, Stdout: out, Stderr: &bytes.Buffer{}, JSON: true}
	if err := cmdCriticalPath(ctx, []string{"--project", "core"}); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Duration float64            `json:"duration"`
		Tasks    []measuredSchedule `json:"tasks"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	tasks := map[string]measuredSchedule{}
	for _, task := range result.Tasks {
		tasks[task.ID] = task
	}
	return result.Duration, tasks
}
func TestCore601CriticalPathPreservesAggregateChildDependencies(t *testing.T) {
	w, parent := aggregateInsightFixture(t)
	parent, err := store.FindTask(w, parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	parent.Doc.Front.SetList("depends_on", nil)
	if err := store.SaveTask(parent); err != nil {
		t.Fatal(err)
	}
	downstream := scheduleTask(t, w, "after aggregate completion", 2, "", parent.ID+":FS")
	duration, tasks := readSchedule(t, w)
	if tasks[downstream.ID].Start != 2 || duration != 4 {
		t.Fatalf("critical-path dropped aggregate required-child barrier: downstream starts %v total %v, want start2 total4", tasks[downstream.ID].Start, duration)
	}
	if _, visible := tasks[parent.ID]; visible {
		t.Fatal("aggregate appeared as implementation work")
	}
}
func TestCore601NextRanksAggregateRequiredChildByDownstreamSlack(t *testing.T) {
	w := scheduleWorkspace(t)
	unrelated := scheduleTask(t, w, "unrelated shorter chain", 5, "")
	child := scheduleTask(t, w, "required aggregate implementation", 2, "")
	parent := scheduleAggregate(t, w, "aggregate milestone", child)
	downstream := scheduleTask(t, w, "long downstream delivery", 10, "", parent.ID+":FS")
	out := &bytes.Buffer{}
	ctx := &clikit.Ctx{Cwd: w.Root, Stdout: out, Stderr: &bytes.Buffer{}, JSON: true}
	if err := cmdNext(ctx, []string{"--project", "core", "--parallel", "10"}); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Recommendations []struct {
			ID       string   `json:"id"`
			Slack    *float64 `json:"slack"`
			Critical bool     `json:"critical"`
		} `json:"recommendations"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Recommendations) != 2 || result.Recommendations[0].ID != child.ID || result.Recommendations[0].Slack == nil || *result.Recommendations[0].Slack != 0 || !result.Recommendations[0].Critical {
		t.Fatalf("next lost aggregate downstream critical chain: %+v", result)
	}
	if result.Recommendations[1].ID != unrelated.ID {
		t.Fatal("next did not retain independent implementation work")
	}
	for _, task := range result.Recommendations {
		if task.ID == parent.ID || task.ID == downstream.ID {
			t.Fatal("next assigned aggregate or unfinished aggregate dependent")
		}
	}
}
func TestCore601NestedRequiredMilestonesExcludeOptionalChildren(t *testing.T) {
	w := scheduleWorkspace(t)
	leaf := scheduleTask(t, w, "inner required implementation", 3, "")
	other := scheduleTask(t, w, "outer required implementation", 5, "")
	inner := scheduleAggregate(t, w, "inner milestone", leaf)
	outer := scheduleAggregate(t, w, "outer milestone", inner, other)
	optional := scheduleTask(t, w, "optional descriptive implementation", 10, outer.ID)
	downstream := scheduleTask(t, w, "nested downstream", 2, "", outer.ID+":FS")
	duration, tasks := readSchedule(t, w)
	if tasks[downstream.ID].Start != 5 || tasks[downstream.ID].End != 7 || duration != 10 {
		t.Fatalf("nested/optional completion barrier wrong: downstream=%+v duration=%v", tasks[downstream.ID], duration)
	}
	if tasks[optional.ID].Duration != 10 {
		t.Fatal("optional implementation work disappeared")
	}
	for _, parent := range []*store.Task{inner, outer} {
		if _, visible := tasks[parent.ID]; visible {
			t.Fatal("nested aggregate gained work duration")
		}
	}
}
func TestCore601CompletedAggregateAndChildrenHaveNoRemainingBarrierWork(t *testing.T) {
	for _, completeParent := range []bool{false, true} {
		t.Run(fmt.Sprintf("parent-%t", completeParent), func(t *testing.T) {
			w := scheduleWorkspace(t)
			child := scheduleTask(t, w, "required implementation", 5, "")
			parent := scheduleAggregate(t, w, "completed event", child)
			if completeParent {
				if err := store.MoveTask(w, parent, model.StatusDone); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := store.MoveTask(w, child, model.StatusDone); err != nil {
					t.Fatal(err)
				}
			}
			downstream := scheduleTask(t, w, "after completed work", 2, "", parent.ID+":FS")
			duration, tasks := readSchedule(t, w)
			if tasks[downstream.ID].Start != 0 {
				t.Fatalf("completed aggregate/child resurrected barrier work: %+v", tasks[downstream.ID])
			}
			expected := 2.0
			if completeParent {
				expected = 5
			}
			if duration != expected {
				t.Fatalf("completed milestone duration=%v want%v", duration, expected)
			}
		})
	}
}
func TestCore601TypedEndpointsSurviveRequiredMilestoneProjection(t *testing.T) {
	for _, incoming := range []string{"FS", "SS", "FF", "SF"} {
		for _, outgoing := range []string{"FS", "SS", "FF", "SF"} {
			t.Run(incoming+"-"+outgoing, func(t *testing.T) {
				w := scheduleWorkspace(t)
				prior := scheduleTask(t, w, "prior work", 8, "")
				child := scheduleTask(t, w, "required child", 5, "", prior.ID+":"+incoming)
				parent := scheduleAggregate(t, w, "typed milestone", child)
				downstream := scheduleTask(t, w, "typed downstream", 2, "", parent.ID+":"+outgoing)
				event := map[string]float64{"FS": 13, "SS": 5, "FF": 8, "SF": 5}[incoming]
				start := event
				if outgoing == "FF" || outgoing == "SF" {
					start -= 2
				}
				_, tasks := readSchedule(t, w)
				if tasks[child.ID].End != event || tasks[downstream.ID].Start != start {
					t.Fatalf("typed dependency endpoints were changed: %s/%s child=%+v downstream=%+v want event%v start%v", incoming, outgoing, tasks[child.ID], tasks[downstream.ID], event, start)
				}
			})
		}
	}
}
func TestCore601DescriptiveParentRemainsOrdinaryEstimatedWork(t *testing.T) {
	w := scheduleWorkspace(t)
	parent := scheduleTask(t, w, "ordinary parent", 4, "")
	child := scheduleTask(t, w, "descriptive child", 8, parent.ID)
	downstream := scheduleTask(t, w, "ordinary downstream", 2, "", parent.ID+":FS")
	_, tasks := readSchedule(t, w)
	if tasks[parent.ID].Duration != 4 || tasks[child.ID].Start != 0 || tasks[downstream.ID].Start != 4 {
		t.Fatalf("ordinary hierarchy acquired aggregate semantics: %+v", tasks)
	}
	all, err := store.ListTasks(w, "core", "")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, task := range store.ReadyFrontier(all).Ready {
		if task.ID == parent.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("ordinary parent became unassignable")
	}
}
func TestCore601UnrepresentableRequiredChildrenRefuseBothViews(t *testing.T) {
	for _, kind := range []string{"missing", "external", "empty", "unstable", "duplicate", "cycle", "blocked-cycle"} {
		t.Run(kind, func(t *testing.T) {
			w := scheduleWorkspace(t)
			child := scheduleTask(t, w, "required implementation", 2, "")
			parent := scheduleAggregate(t, w, "invalid milestone", child)
			identity := child.ID
			switch kind {
			case "missing":
				identity = "t-01M000000000000000000000000"
				parent.Doc.Front.SetList("aggregate_children", []string{identity})
			case "empty":
				identity = "no required children"
				parent.Doc.Front.SetList("aggregate_children", nil)
			case "unstable":
				identity = child.Slug
				parent.Doc.Front.SetList("aggregate_children", []string{identity})
			case "duplicate":
				parent.Doc.Front.SetList("aggregate_children", []string{child.ID, child.ID})
			case "cycle", "blocked-cycle":
				if kind == "blocked-cycle" {
					if err := store.MoveTask(w, child, model.StatusBlocked); err != nil {
						t.Fatal(err)
					}
				}
				child.Doc.Front.SetList("depends_on", []string{parent.ID + ":FS"})
				if err := store.SaveTask(child); err != nil {
					t.Fatal(err)
				}
				identity = "cycle"
			case "external":
				if _, err := store.CreateProject(w, "a-root", "Other", "other", "goal", ""); err != nil {
					t.Fatal(err)
				}
				external, err := store.CreateTask(w, "a-root", "other", "external required work", store.TaskOpts{Accept: []string{"external done"}, Estimate: "2,2,2"})
				if err != nil {
					t.Fatal(err)
				}
				identity = external.ID
				parent.Doc.Front.SetList("aggregate_children", []string{identity})
			}
			if err := store.SaveTask(parent); err != nil {
				t.Fatal(err)
			}
			for name, run := range map[string]func(*clikit.Ctx) error{"next": func(ctx *clikit.Ctx) error { return cmdNext(ctx, []string{"--project", "core"}) }, "critical-path": func(ctx *clikit.Ctx) error { return cmdCriticalPath(ctx, []string{"--project", "core"}) }} {
				ctx := &clikit.Ctx{Cwd: w.Root, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, JSON: true}
				err := run(ctx)
				if err == nil || !strings.Contains(err.Error(), identity) {
					t.Fatalf("%s invented a runnable completion schedule for %s: err=%v", name, kind, err)
				}
			}
		})
	}
}

func TestCore601NextRetainsIndependentReadyWorkWhenRequiredChildBlocked(t *testing.T) {
	w := scheduleWorkspace(t)
	independent := scheduleTask(t, w, "independent ready work", 4, "")
	child := scheduleTask(t, w, "blocked required implementation", 2, "")
	parent := scheduleAggregate(t, w, "incomplete aggregate milestone", child)
	dependent := scheduleTask(t, w, "waits for aggregate", 1, "", parent.ID+":FS")
	if err := store.MoveTask(w, child, model.StatusBlocked); err != nil {
		t.Fatal(err)
	}

	out := &bytes.Buffer{}
	ctx := &clikit.Ctx{Cwd: w.Root, Stdout: out, Stderr: &bytes.Buffer{}, JSON: true}
	if err := cmdNext(ctx, []string{"--project", "core", "--parallel", "12", "--critical-path"}); err != nil {
		t.Fatalf("next must retain independent ready work without claiming an incomplete critical path: %v", err)
	}
	var got struct {
		Ordering        string `json:"ordering"`
		Recommendations []struct {
			ID       string   `json:"id"`
			Slack    *float64 `json:"slack"`
			Critical bool     `json:"critical"`
		} `json:"recommendations"`
		Blocked             []string `json:"blocked"`
		ScheduleDiagnostics []string `json:"schedule_diagnostics"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("next JSON: %v\n%s", err, out)
	}
	if got.Ordering != "moscow-sequence" {
		t.Fatalf("ordering = %q, want explicit sequence fallback", got.Ordering)
	}
	if len(got.Recommendations) != 1 || got.Recommendations[0].ID != independent.ID {
		t.Fatalf("recommendations = %+v, want only independent ready work %s", got.Recommendations, independent.ID)
	}
	if got.Recommendations[0].Slack != nil || got.Recommendations[0].Critical {
		t.Fatalf("fallback recommendation claimed CPM evidence: %+v", got.Recommendations[0])
	}
	if len(got.Blocked) != 1 || got.Blocked[0] != dependent.ID {
		t.Fatalf("blocked = %v, want aggregate-dependent task %s", got.Blocked, dependent.ID)
	}
	if len(got.ScheduleDiagnostics) != 1 || !strings.Contains(got.ScheduleDiagnostics[0], parent.ID) || !strings.Contains(got.ScheduleDiagnostics[0], child.ID) {
		t.Fatalf("schedule diagnostics = %v, want aggregate %s and blocked child %s", got.ScheduleDiagnostics, parent.ID, child.ID)
	}

	criticalOut := &bytes.Buffer{}
	criticalCtx := &clikit.Ctx{Cwd: w.Root, Stdout: criticalOut, Stderr: &bytes.Buffer{}, JSON: true}
	if err := cmdCriticalPath(criticalCtx, []string{"--project", "core"}); err == nil || !strings.Contains(err.Error(), parent.ID) || !strings.Contains(err.Error(), child.ID) {
		t.Fatalf("critical-path must continue refusing the incomplete aggregate schedule with both IDs: %v", err)
	}
}

func TestCore601RequiredChildrenStayInCanonicalProject(t *testing.T) {
	for _, done := range []bool{false, true} {
		t.Run(fmt.Sprintf("done-%t", done), func(t *testing.T) {
			w := scheduleWorkspace(t)
			if _, err := store.CreateProject(w, "a-root", "Other", "other", "goal", ""); err != nil {
				t.Fatal(err)
			}
			child, err := store.CreateTask(w, "a-root", "other", "external child", store.TaskOpts{Accept: []string{"works"}, Estimate: "3,3,3"})
			if err != nil {
				t.Fatal(err)
			}
			parent := scheduleAggregate(t, w, "cross project event", child)
			scheduleTask(t, w, "after event", 2, "", parent.ID+":FS")
			if done {
				store.CheckAllAcceptance(child)
				if err := store.SaveTask(child); err != nil {
					t.Fatal(err)
				}
				if err := store.MoveTask(w, child, model.StatusDone); err != nil {
					t.Fatal(err)
				}
			}
			progress, err := store.AggregateProgressFor(w, parent)
			if err != nil || progress.ReadyToClose {
				t.Fatalf("shared progress unexpectedly accepts external required child: %+v %v", progress, err)
			}
			for _, projectArgs := range [][]string{nil, {"--project", "core"}} {
				for _, run := range []func(*clikit.Ctx, []string) error{cmdNext, cmdCriticalPath} {
					ctx := &clikit.Ctx{Cwd: w.Root, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, JSON: true}
					err := run(ctx, projectArgs)
					if err == nil || !strings.Contains(err.Error(), child.ID) {
						t.Fatalf("scheduling accepted external required child rejected by canonical aggregate progress: done=%t project=%v err=%v", done, projectArgs, err)
					}
				}
			}
		})
	}
}
func TestCore601TypedExternalDoneDependencyRemainsSatisfied(t *testing.T) {
	w := scheduleWorkspace(t)
	if _, err := store.CreateProject(w, "a-root", "Other", "other", "goal", ""); err != nil {
		t.Fatal(err)
	}
	external, err := store.CreateTask(w, "a-root", "other", "external prerequisite", store.TaskOpts{Accept: []string{"works"}, Estimate: "20,20,20"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MoveTask(w, external, model.StatusDone); err != nil {
		t.Fatal(err)
	}
	child := scheduleTask(t, w, "local required implementation", 3, "", "other/"+external.ID+":FS")
	parent := scheduleAggregate(t, w, "local event", child)
	downstream := scheduleTask(t, w, "after local event", 2, "", parent.ID+":FS")
	duration, tasks := readSchedule(t, w)
	if tasks[downstream.ID].Start != 3 || duration != 5 {
		t.Fatalf("typed completed external prerequisite changed local barrier: %+v duration=%v", tasks, duration)
	}
	ctx := &clikit.Ctx{Cwd: w.Root, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, JSON: true}
	if err := cmdNext(ctx, []string{"--project", "core"}); err != nil {
		t.Fatal(err)
	}
}
func TestCore601BlockedAggregateDependencyRefusesBothViews(t *testing.T) {
	for _, typ := range []string{"FS", "SS", "FF", "SF"} {
		t.Run(typ, func(t *testing.T) {
			w := scheduleWorkspace(t)
			leaf := scheduleTask(t, w, "required work", 3, "")
			blocked := scheduleAggregate(t, w, "blocked prerequisite event", leaf)
			if err := store.MoveTask(w, blocked, model.StatusBlocked); err != nil {
				t.Fatal(err)
			}
			ownChild := scheduleTask(t, w, "other required work", 2, "")
			aggregate := scheduleAggregate(t, w, "scheduled event", ownChild)
			aggregate.Doc.Front.SetList("depends_on", []string{blocked.ID + ":" + typ})
			if err := store.SaveTask(aggregate); err != nil {
				t.Fatal(err)
			}
			scheduleTask(t, w, "after scheduled event", 1, "", aggregate.ID+":FS")
			for _, run := range []func(*clikit.Ctx, []string) error{cmdNext, cmdCriticalPath} {
				ctx := &clikit.Ctx{Cwd: w.Root, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, JSON: true}
				err := run(ctx, []string{"--project", "core"})
				if err == nil || !strings.Contains(err.Error(), blocked.ID) {
					t.Fatalf("blocked aggregate prerequisite silently disappeared from %s schedule: %v", typ, err)
				}
			}
		})
	}
}

func TestCore601AggregateNeedsNoEstimateAndQualifiedRefsKeepBarrier(t *testing.T) {
	w := scheduleWorkspace(t)
	child := scheduleTask(t, w, "required sized work", 3, "")
	parent := scheduleAggregate(t, w, "unsized zero work milestone", child)
	parent.Doc.Front.Delete("estimate")
	if err := store.SaveTask(parent); err != nil {
		t.Fatal(err)
	}
	downstream := scheduleTask(t, w, "qualified dependent", 2, "", "core/"+parent.ID+":FS")
	duration, tasks := readSchedule(t, w)
	if tasks[downstream.ID].Start != 3 || duration != 5 {
		t.Fatalf("unsized aggregate or qualified ref lost barrier: downstream=%+v duration=%v", tasks[downstream.ID], duration)
	}
	ctx := &clikit.Ctx{Cwd: w.Root, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, JSON: true}
	if err := cmdNext(ctx, []string{"--project", "core"}); err != nil {
		t.Fatal(err)
	}
	if text := ctx.Stdout.(*bytes.Buffer).String(); !strings.Contains(text, `"slack": 0`) {
		t.Fatalf("aggregate estimate incorrectly disabled next CPM: %s", text)
	}
}
