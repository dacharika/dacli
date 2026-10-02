package reviewing

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/agentid"
	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/gitx"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/team"
)

func TestReviewRecordBindsROIdentityAndExactTreeThenProjectsSafely(t *testing.T) {
	w := loopEnv(t)
	task, err := store.CreateTask(w, "a-root", "p", "Reviewed branch", store.TaskOpts{Accept: []string{"done"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateRole(w, "a-root", team.Role{Name: "reviewer", Kind: "reviewer", Grant: "ro", Runtime: "codex", Model: "gpt"}); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-q", "-m", "base"}, {"branch", taskBranch(task)}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = w.Root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	commit, err := gitx.Run(w.Root, "rev-parse", "--verify", taskBranch(task))
	if err != nil {
		t.Fatal(err)
	}
	commit = strings.TrimSpace(commit)
	tree, err := gitx.Run(w.Root, "rev-parse", "--verify", commit+"^{tree}")
	tree = strings.TrimSpace(tree)
	if err != nil {
		t.Fatal(err)
	}
	child, token, err := agentid.Spawn(w, &agentid.Identity{ID: agentid.RootID, Grant: model.GrantRW, Role: "root"}, "reviewer", model.GrantRO)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(agentid.EnvVar, token)
	result := store.IndependentReviewResult{Schema: store.ReviewResultSchema, Verdict: store.ReviewRequestChanges, ReviewerID: child, ReviewerRole: "reviewer", Runtime: "codex", Model: "gpt", Grant: "ro", IndependentOf: []string{"a-builder-1"}, CommitSHA: commit, TreeSHA: tree, ObservedAt: time.Now(), Findings: []store.ReviewFinding{{ID: "REV-100", Severity: "major", File: "main.go", Line: 1, Evidence: "/private/secret evidence", AffectedInvariant: "current tree fails its contract", SuggestedVerification: "go test ./..."}}}
	raw, _ := json.Marshal(result)
	ctx := &clikit.Ctx{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, Cwd: w.Root}
	if err := cmdReviewRecord(ctx, []string{"--task", task.ID, "--result", string(raw)}); err != nil {
		t.Fatal(err)
	}

	out := &bytes.Buffer{}
	ctx.Stdout = out
	if err := cmdReviewProjection(ctx, []string{"--task", task.ID}); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, forbidden := range []string{"/private/secret", child, "a-builder", "codex", "gpt"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("projection leaked %q: %s", forbidden, got)
		}
	}
	for _, want := range []string{"REV-100", "main.go", "current tree fails", "go test"} {
		if !strings.Contains(got, want) {
			t.Fatalf("projection omitted %q: %s", want, got)
		}
	}
}

func TestReviewRecordRefusesStaleTreeAndRWReviewer(t *testing.T) {
	w := loopEnv(t)
	task, err := store.CreateTask(w, "a-root", "p", "Stale review", store.TaskOpts{Accept: []string{"done"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateRole(w, "a-root", team.Role{Name: "reviewer", Kind: "reviewer", Grant: "ro", Runtime: "codex"}); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "add", ".")
	cmd.Dir = w.Root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v %s", err, out)
	}
	cmd = exec.Command("git", "commit", "-q", "-m", "base")
	cmd.Dir = w.Root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v %s", err, out)
	}
	cmd = exec.Command("git", "branch", taskBranch(task))
	cmd.Dir = w.Root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git branch: %v %s", err, out)
	}
	result := store.IndependentReviewResult{Schema: store.ReviewResultSchema, Verdict: store.ReviewApprove, ReviewerID: "a-root", ReviewerRole: "root", Runtime: "codex", Grant: "rw", IndependentOf: []string{"a-builder"}, CommitSHA: "stale", TreeSHA: "stale", ObservedAt: time.Now()}
	raw, _ := json.Marshal(result)
	ctx := &clikit.Ctx{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, Cwd: w.Root}
	if err := cmdReviewRecord(ctx, []string{"--task", task.ID, "--result", string(raw)}); clikit.ExitCode(err) != 3 {
		t.Fatalf("rw/stale review exit=%d err=%v, want refusal", clikit.ExitCode(err), err)
	}
}
