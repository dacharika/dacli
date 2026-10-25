package ghmirror

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

func writeMilestonePlan(t *testing.T, plan milestonePlan) string {
	t.Helper()
	b, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "milestone-plan.json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func mappedPlanTask(t *testing.T, w *workspace.Workspace) *store.Task {
	t.Helper()
	tk, err := store.CreateTask(w, "a-root", "core", "roadmap item", store.TaskOpts{})
	if err != nil {
		t.Fatal(err)
	}
	tk.Doc.Front.SetBlock("github", githubBlock(1183, "owner/repo"))
	if err := store.SaveTask(tk); err != nil {
		t.Fatal(err)
	}
	return tk
}

func milestonePlanFixture(t *testing.T, current string, assignments []milestoneAssignment) (*workspace.Workspace, string, *bytes.Buffer, *[][]string) {
	t.Helper()
	w := mirrorWorkspace(t)
	linkRepo(t, w, "core", "owner/repo")
	tk := mappedPlanTask(t, w)
	plan := milestonePlan{Schema: "github-milestone-plan/v1", Project: "core", Repository: "owner/repo", Assignments: assignments}
	if assignments == nil {
		plan.Assignments = []milestoneAssignment{{TaskID: tk.ID, TaskDigest: milestoneTaskDigest(tk), Issue: 1183, ExpectedMilestone: current, ExpectedLabels: []string{"area:core", "status:blocked", "type:task"}, Milestone: "M3"}}
	} else {
		for i := range plan.Assignments {
			if plan.Assignments[i].TaskID == "$task" {
				plan.Assignments[i].TaskID = tk.ID
			}
			if plan.Assignments[i].TaskDigest == "$digest" {
				plan.Assignments[i].TaskDigest = milestoneTaskDigest(tk)
			}
			if plan.Assignments[i].ExpectedLabels == nil {
				plan.Assignments[i].ExpectedLabels = []string{"area:core", "status:blocked", "type:task"}
			}
		}
	}
	path := writeMilestonePlan(t, plan)
	var calls [][]string
	orig := gh
	t.Cleanup(func() { gh = orig })
	gh = func(_ *workspace.Workspace, args ...string) (string, error) {
		calls = append(calls, append([]string(nil), args...))
		switch {
		case len(args) >= 2 && args[0] == "repo" && args[1] == "view":
			return `{"nameWithOwner":"owner/repo","visibility":"PRIVATE"}`, nil
		case len(args) >= 2 && args[0] == "issue" && args[1] == "list":
			return fmt.Sprintf(`[{"number":1183,"title":%q,"body":%q,"state":"open","labels":[{"name":"status:blocked"},{"name":"type:task"},{"name":"area:core"}],"milestone":{"title":%q}}]`, taskIssueTitle(tk), marker(w, tk), current), nil
		case len(args) >= 2 && args[0] == "api" && strings.Contains(args[1], "milestones?"):
			return "M0\nM3", nil
		case len(args) >= 2 && args[0] == "label" && args[1] == "list":
			return `[{"name":"status:open"},{"name":"status:blocked"},{"name":"type:task"},{"name":"area:core"}]`, nil
		default:
			return "", nil
		}
	}
	ctx, out := releaseCtx(t, w)
	ctx.Stdout = out
	return w, path, out, &calls
}

func TestMilestonePlanDryRunShowsExactIssueDiffAndWritesNothing(t *testing.T) {
	w, path, out, calls := milestonePlanFixture(t, "M0", nil)
	tasks, err := store.ListTasks(w, "core", "")
	if err != nil || len(tasks) != 1 {
		t.Fatalf("tasks: %v (%d)", err, len(tasks))
	}
	if err := cmdPush(releaseContext(t, w, out), []string{"core", tasks[0].ID, "--milestone-plan", path, "--dry-run"}); err != nil {
		t.Fatalf("push dry-run: %v\n%s", err, out.String())
	}
	if got := out.String(); !strings.Contains(got, `would edit issue #1183: milestone "M0" -> "M3"`) || !strings.Contains(got, `labels ["area:core", "status:blocked", "type:task"] -> ["area:core", "status:open", "type:task"]`) || !strings.Contains(got, "milestone plan: previewed for 1 explicitly selected task issue(s)") {
		t.Fatalf("dry-run omitted exact milestone diff:\n%s", got)
	}
	assertNoWrites(t, *calls)
	for _, call := range *calls {
		if len(call) > 1 && call[0] == "label" && (call[1] == "create" || call[1] == "edit" || call[1] == "delete") {
			t.Fatalf("dry-run changed shared repository label definitions: %v", call)
		}
	}
}

func TestMilestonePlanApplyTouchesOnlyExplicitTaxonomy(t *testing.T) {
	w, path, out, calls := milestonePlanFixture(t, "M0", nil)
	tasks, err := store.ListTasks(w, "core", "")
	if err != nil || len(tasks) != 1 {
		t.Fatalf("tasks: %v (%d)", err, len(tasks))
	}
	if err := cmdPush(releaseContext(t, w, out), []string{"core", tasks[0].ID, "--milestone-plan", path}); err != nil {
		t.Fatalf("push apply: %v\n%s", err, out.String())
	}
	edit := findCall(*calls, "issue", "edit", "1183")
	if edit == nil || !argPairPresent(edit, "--milestone", "M3") {
		t.Fatalf("apply did not assign the selected milestone to the mapped issue: %v", *calls)
	}
	for _, call := range *calls {
		if len(call) > 1 && call[0] == "label" && (call[1] == "create" || call[1] == "edit" || call[1] == "delete") {
			t.Fatalf("explicit plan changed shared repository label definitions: %v", call)
		}
		if len(call) > 1 && call[0] == "issue" && (call[1] == "create" || call[1] == "close" || call[1] == "comment") {
			t.Fatalf("exact milestone plan performed unrelated task projection: %v", call)
		}
	}
}

func TestMilestonePlanRequiresExistingLabelsBeforeAnyIssueEdit(t *testing.T) {
	for _, dry := range []bool{false, true} {
		t.Run(map[bool]string{false: "apply", true: "dry-run"}[dry], func(t *testing.T) {
			w, path, out, calls := milestonePlanFixture(t, "M0", nil)
			fixtureGH := gh
			gh = func(w *workspace.Workspace, args ...string) (string, error) {
				if len(args) >= 2 && args[0] == "label" && args[1] == "list" {
					*calls = append(*calls, append([]string(nil), args...))
					return `[{"name":"status:blocked"},{"name":"type:task"},{"name":"area:core"}]`, nil
				}
				return fixtureGH(w, args...)
			}
			tasks, err := store.ListTasks(w, "core", "")
			if err != nil || len(tasks) != 1 {
				t.Fatalf("tasks: %v (%d)", err, len(tasks))
			}
			args := []string{"core", tasks[0].ID, "--milestone-plan", path}
			if dry {
				args = append(args, "--dry-run")
			}
			err = cmdPush(releaseContext(t, w, out), args)
			if err == nil || !strings.Contains(err.Error(), `requires label "status:open", which does not exist`) || !strings.Contains(err.Error(), "create required labels explicitly") {
				t.Fatalf("push error = %v, want missing-label setup refusal", err)
			}
			for _, call := range *calls {
				if len(call) > 1 && call[0] == "issue" && call[1] == "edit" || len(call) > 1 && call[0] == "label" && (call[1] == "create" || call[1] == "edit" || call[1] == "delete") {
					t.Fatalf("missing-label plan performed remote write: %v", call)
				}
			}
		})
	}
}

func TestMilestonePlanApplyReportsIssueEditFailure(t *testing.T) {
	w, path, out, _ := milestonePlanFixture(t, "M0", nil)
	tasks, err := store.ListTasks(w, "core", "")
	if err != nil || len(tasks) != 1 {
		t.Fatalf("tasks: %v (%d)", err, len(tasks))
	}
	fixtureGH := gh
	gh = func(w *workspace.Workspace, args ...string) (string, error) {
		if len(args) >= 2 && args[0] == "issue" && args[1] == "edit" {
			return "permission denied", fmt.Errorf("gh issue edit failed")
		}
		return fixtureGH(w, args...)
	}
	err = cmdPush(releaseContext(t, w, out), []string{"core", tasks[0].ID, "--milestone-plan", path})
	if err == nil || !strings.Contains(err.Error(), "milestone plan apply stopped") || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("planned edit failure was not reported: %v", err)
	}
}

func TestMilestonePlanRejectsEmptyStaleAndConflictingPlansBeforeWrites(t *testing.T) {
	for _, tc := range []struct {
		name        string
		current     string
		assignments []milestoneAssignment
		want        string
	}{
		{name: "empty", current: "M0", assignments: []milestoneAssignment{}, want: "at least one assignment"},
		{name: "stale current", current: "M1", assignments: []milestoneAssignment{{TaskID: "$task", TaskDigest: "$digest", Issue: 1183, ExpectedMilestone: "M0", Milestone: "M3"}}, want: "stale for issue #1183"},
		{name: "stale labels", current: "M0", assignments: []milestoneAssignment{{TaskID: "$task", TaskDigest: "$digest", Issue: 1183, ExpectedMilestone: "M0", ExpectedLabels: []string{"type:task"}, Milestone: "M3"}}, want: "current labels differ"},
		{name: "stale task metadata", current: "M0", assignments: []milestoneAssignment{{TaskID: "$task", TaskDigest: "sha256:old", Issue: 1183, ExpectedMilestone: "M0", Milestone: "M3"}}, want: "local task content or metadata changed"},
		{name: "conflicting duplicate", current: "M0", assignments: []milestoneAssignment{{TaskID: "$task", TaskDigest: "$digest", Issue: 1183, ExpectedMilestone: "M0", Milestone: "M1"}, {TaskID: "$task", TaskDigest: "$digest", Issue: 1183, ExpectedMilestone: "M0", Milestone: "M2"}}, want: "exact non-empty selected task window"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, path, out, calls := milestonePlanFixture(t, tc.current, tc.assignments)
			tasks, err := store.ListTasks(w, "core", "")
			if err != nil || len(tasks) != 1 {
				t.Fatalf("tasks: %v (%d)", err, len(tasks))
			}
			beforeCalls := len(*calls)
			err = cmdPush(releaseContext(t, w, out), []string{"core", tasks[0].ID, "--milestone-plan", path})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("push error = %v, want %q", err, tc.want)
			}
			for _, c := range (*calls)[beforeCalls:] {
				if len(c) > 1 && c[0] == "issue" && c[1] == "edit" || len(c) > 2 && c[0] == "api" && c[1] == "--method" && c[2] == "POST" {
					t.Fatalf("refused plan caused a remote write: %v", c)
				}
			}
		})
	}
}

func TestMilestonePlanRequiresExistingTargetAndReadableMilestoneSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name string
		out  string
		err  error
		want string
	}{
		{name: "missing target", out: "M0", want: `target "M3" does not exist`},
		{name: "unreadable snapshot", err: fmt.Errorf("gh offline"), want: `verify planned milestone "M3"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, path, out, calls := milestonePlanFixture(t, "M0", nil)
			tasks, err := store.ListTasks(w, "core", "")
			if err != nil || len(tasks) != 1 {
				t.Fatalf("tasks: %v (%d)", err, len(tasks))
			}
			fixtureGH := gh
			gh = func(w *workspace.Workspace, args ...string) (string, error) {
				if len(args) >= 2 && args[0] == "api" && strings.Contains(args[1], "milestones?") {
					*calls = append(*calls, append([]string(nil), args...))
					return tc.out, tc.err
				}
				return fixtureGH(w, args...)
			}
			if err := cmdPush(releaseContext(t, w, out), []string{"core", tasks[0].ID, "--milestone-plan", path}); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("push error = %v, want %q", err, tc.want)
			}
			for _, c := range *calls {
				if len(c) > 1 && c[0] == "issue" && c[1] == "edit" || len(c) > 2 && c[0] == "api" && c[1] == "--method" && c[2] == "POST" || len(c) > 1 && c[0] == "label" && c[1] == "create" {
					t.Fatalf("invalid plan target caused a remote write: %v", c)
				}
			}
		})
	}
}

func TestPushDryRunDocumentsLegacyProjectMilestoneDefault(t *testing.T) {
	w := mirrorWorkspace(t)
	linkRepo(t, w, "core", "owner/repo")
	tk := mappedPlanTask(t, w)
	orig := gh
	t.Cleanup(func() { gh = orig })
	gh = func(_ *workspace.Workspace, args ...string) (string, error) {
		switch {
		case len(args) >= 2 && args[0] == "repo" && args[1] == "view":
			return `{"nameWithOwner":"owner/repo","visibility":"PRIVATE"}`, nil
		case len(args) >= 2 && args[0] == "issue" && args[1] == "list":
			return fmt.Sprintf(`[{"number":1183,"title":%q,"body":%q,"state":"open","labels":[{"name":"status:open"},{"name":"type:task"},{"name":"area:core"}],"milestone":{"title":"Core"}}]`, taskIssueTitle(tk), marker(w, tk)), nil
		case len(args) >= 2 && args[0] == "api" && strings.Contains(args[1], "milestones?"):
			return "Core", nil
		default:
			return "", nil
		}
	}
	ctx, out := releaseCtx(t, w)
	if err := cmdPush(ctx, []string{"core", tk.ID, "--dry-run"}); err != nil {
		t.Fatalf("legacy push dry-run: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "milestone: Core (task issues would be assigned; legacy project-title default)") {
		t.Fatalf("omitting the explicit plan must retain and identify the legacy project-title default:\n%s", out.String())
	}
}

func TestMilestonePlanReadRejectsUnknownFieldsAndTrailingJSON(t *testing.T) {
	for _, raw := range []string{
		`{"schema":"github-milestone-plan/v1","project":"core","repository":"owner/repo","assignments":[{"task_id":"t","task_digest":"sha256:x","issue":1,"expected_milestone":"M0","expected_labels":[],"milestone":"M1","fallback":"Core"}]}`,
		`{"schema":"github-milestone-plan/v1","project":"core","repository":"owner/repo","assignments":[{"task_id":"t","task_digest":"sha256:x","issue":1,"expected_milestone":"M0","expected_labels":[],"milestone":"M1"}]} {}`,
	} {
		path := filepath.Join(t.TempDir(), "plan.json")
		if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := readMilestonePlan(path); err == nil {
			t.Fatalf("accepted unsafe plan JSON %s", raw)
		}
	}
}

func TestMilestonePlanReadRejectsDuplicateJSONKeysAtEveryObjectLevel(t *testing.T) {
	for _, raw := range []string{
		`{"schema":"github-milestone-plan/v1","schema":"github-milestone-plan/v1","project":"core","repository":"owner/repo","assignments":[{"task_id":"t","task_digest":"sha256:x","issue":1,"expected_milestone":"M0","expected_labels":[],"milestone":"M1"}]}`,
		`{"schema":"github-milestone-plan/v1","project":"core","repository":"owner/repo","assignments":[{"task_id":"t","task_digest":"sha256:x","issue":1,"expected_milestone":"M0","expected_labels":[],"milestone":"M1","milestone":"M2"}]}`,
		`{"schema":"github-milestone-plan/v1","project":"core","repository":"owner/repo","assignments":[{"task_id":"t","task_digest":"sha256:x","issue":1,"expected_milestone":"M0","expected_labels":[],"milestone":"M1","nested":{"a":1,"a":2}}]}`,
	} {
		t.Run(raw, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "plan.json")
			if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := readMilestonePlan(path); err == nil || !strings.Contains(err.Error(), "duplicate JSON key") {
				t.Fatalf("readMilestonePlan error = %v, want duplicate-key rejection", err)
			}
		})
	}
}

func TestMilestonePlanDuplicateKeysRefuseBeforeWrites(t *testing.T) {
	w, path, out, calls := milestonePlanFixture(t, "M0", nil)
	if err := os.WriteFile(path, []byte(`{"schema":"github-milestone-plan/v1","schema":"github-milestone-plan/v1","project":"core","repository":"owner/repo","assignments":[{"task_id":"x","task_digest":"sha256:x","issue":1183,"expected_milestone":"M0","expected_labels":[],"milestone":"M3"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	tasks, err := store.ListTasks(w, "core", "")
	if err != nil || len(tasks) != 1 {
		t.Fatalf("tasks: %v (%d)", err, len(tasks))
	}
	err = cmdPush(releaseContext(t, w, out), []string{"core", tasks[0].ID, "--milestone-plan", path})
	if err == nil || !strings.Contains(err.Error(), "duplicate JSON key") {
		t.Fatalf("push error = %v, want duplicate-key refusal", err)
	}
	for _, call := range *calls {
		if len(call) > 1 && call[0] == "issue" && call[1] == "edit" || len(call) > 1 && call[0] == "label" && (call[1] == "create" || call[1] == "edit" || call[1] == "delete") || len(call) > 2 && call[0] == "api" && call[1] == "--method" && call[2] == "POST" {
			t.Fatalf("duplicate-key plan performed remote write: %v", call)
		}
	}
}

func TestMilestonePlanReadBoundsInputAndAssignmentCount(t *testing.T) {
	tooLarge := strings.Repeat(" ", (1<<20)+1)
	path := filepath.Join(t.TempDir(), "large.json")
	if err := os.WriteFile(path, []byte(tooLarge), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readMilestonePlan(path); err == nil || !strings.Contains(err.Error(), "exceeds 1 MiB") {
		t.Fatalf("oversize plan error = %v, want bounded-input refusal", err)
	}

	assignments := make([]milestoneAssignment, 1001)
	for i := range assignments {
		assignments[i] = milestoneAssignment{TaskID: fmt.Sprintf("task-%d", i), TaskDigest: "sha256:x", Issue: i + 1, ExpectedMilestone: "M0", ExpectedLabels: []string{}, Milestone: "M1"}
	}
	plan := milestonePlan{Schema: "github-milestone-plan/v1", Project: "core", Repository: "owner/repo", Assignments: assignments}
	path = writeMilestonePlan(t, plan)
	if _, err := readMilestonePlan(path); err == nil || !strings.Contains(err.Error(), "at most 1000 assignments") {
		t.Fatalf("assignment count error = %v, want bounded-count refusal", err)
	}
}

func TestEmptyMilestonePlanFlagCannotFallBackToProjectDefault(t *testing.T) {
	w, _, _, calls := milestonePlanFixture(t, "M0", nil)
	tasks, err := store.ListTasks(w, "core", "")
	if err != nil || len(tasks) != 1 {
		t.Fatalf("tasks: %v (%d)", err, len(tasks))
	}
	err = cmdPush(releaseContext(t, w, &bytes.Buffer{}), []string{"core", tasks[0].ID, "--milestone-plan="})
	if err == nil || clikit.ExitCode(err) != 2 || !strings.Contains(err.Error(), "exactly one non-empty plan file") {
		t.Fatalf("empty plan flag was not a usage error: %v", err)
	}
	if len(*calls) != 0 {
		t.Fatalf("empty plan flag reached GitHub and could fall back to project-wide behavior: %v", *calls)
	}
}

func TestMilestonePlanFlagRequiresAValue(t *testing.T) {
	w, _, _, calls := milestonePlanFixture(t, "M0", nil)
	tasks, err := store.ListTasks(w, "core", "")
	if err != nil || len(tasks) != 1 {
		t.Fatalf("tasks: %v (%d)", err, len(tasks))
	}
	err = cmdPush(releaseContext(t, w, &bytes.Buffer{}), []string{"core", tasks[0].ID, "--milestone-plan"})
	if err == nil || clikit.ExitCode(err) != 2 || !strings.Contains(err.Error(), "--milestone-plan requires a value") {
		t.Fatalf("missing plan path was not a usage error: %v", err)
	}
	if len(*calls) != 0 {
		t.Fatalf("missing plan path reached GitHub: %v", *calls)
	}
}

func releaseContext(t *testing.T, w *workspace.Workspace, out *bytes.Buffer) *clikit.Ctx {
	t.Helper()
	ctx, _ := releaseCtx(t, w)
	ctx.Stdout = out
	return ctx
}
