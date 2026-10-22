package cleanup

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

func emptyCleanupWorkspace(t *testing.T) *workspace.Workspace {
	t.Helper()
	w, err := workspace.Init(t.TempDir(), "cleanup")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "init", "-q", "-b", "main")
	cmd.Dir = w.Root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	for _, args := range [][]string{{"config", "user.email", "test@example.test"}, {"config", "user.name", "test"}, {"commit", "--allow-empty", "-qm", "base"}} {
		cmd = exec.Command("git", args...)
		cmd.Dir = w.Root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if _, err := store.CreateProject(w, "a-root", "Core", "core", "goal", ""); err != nil {
		t.Fatal(err)
	}
	old := store.ObserveDeliveryPRs
	store.ObserveDeliveryPRs = func(string) ([]store.DeliveryPR, error) { return nil, nil }
	t.Cleanup(func() { store.ObserveDeliveryPRs = old })
	return w
}

func TestCleanupDryRunRendersSameVersionedPlanInTextAndJSON(t *testing.T) {
	w := emptyCleanupWorkspace(t)
	text := &bytes.Buffer{}
	if err := cmdCleanup(&clikit.Ctx{Cwd: w.Root, Stdout: text, Stderr: &bytes.Buffer{}}, []string{"--project", "core", "--dry-run"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text.String(), store.CleanupPlanSchema) || !strings.Contains(text.String(), "nothing was written") || !strings.Contains(text.String(), "--apply-safe") {
		t.Fatalf("text plan omitted immutable-plan contract:\n%s", text)
	}
	out := &bytes.Buffer{}
	if err := cmdCleanup(&clikit.Ctx{Cwd: w.Root, Stdout: out, Stderr: &bytes.Buffer{}, JSON: true}, []string{"--project", "core", "--dry-run"}); err != nil {
		t.Fatal(err)
	}
	var plan store.CleanupPlan
	if err := json.Unmarshal(out.Bytes(), &plan); err != nil {
		t.Fatalf("JSON plan: %v\n%s", err, out)
	}
	if plan.Schema != store.CleanupPlanSchema || plan.Version != 3 || !strings.Contains(text.String(), plan.ID) {
		t.Fatalf("text/JSON plan identity differs: text=%s json=%+v", text, plan)
	}
}

func TestBranchesAuditDelegatesToCanonicalCleanupPlan(t *testing.T) {
	w := emptyCleanupWorkspace(t)
	out := &bytes.Buffer{}
	if err := cmdBranchesAudit(&clikit.Ctx{Cwd: w.Root, Stdout: out, Stderr: &bytes.Buffer{}, JSON: true}, []string{"--project", "core"}); err != nil {
		t.Fatal(err)
	}
	var plan store.CleanupPlan
	if err := json.Unmarshal(out.Bytes(), &plan); err != nil || plan.Schema != store.CleanupPlanSchema || plan.ID == "" {
		t.Fatalf("branch audit did not return canonical plan: err=%v plan=%+v", err, plan)
	}
}

func TestCleanupRefusesUnknownApplyIdentity(t *testing.T) {
	w := emptyCleanupWorkspace(t)
	err := cmdCleanup(&clikit.Ctx{Cwd: w.Root, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}, []string{"--project", "core", "--apply-safe", strings.Repeat("0", 64)})
	if err == nil || clikit.ExitCode(err) != 3 || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("unknown plan = %v (exit %d), want policy refusal", err, clikit.ExitCode(err))
	}
}

func TestCleanupEveryClassificationFixtureRendersInTextAndVersionedJSON(t *testing.T) {
	plan := store.CleanupPlan{Schema: store.CleanupPlanSchema, Version: 2, ID: strings.Repeat("a", 64), Project: "core", Base: "main", Items: []store.CleanupItem{
		{Worktree: "/managed/protected", Branch: "main", Protected: true, PRState: "missing", Reasons: []string{"protected current/base worktree"}},
		{Worktree: "/managed/dirty", Branch: "dirty", Dirty: true, PRState: "open", Reasons: []string{"dirty or untracked worktree"}},
		{Worktree: "/managed/unpushed", Branch: "unpushed", Unpushed: true, PRState: "closed", Reasons: []string{"branch contains unpushed commits"}},
		{Worktree: "/managed/unknown", Branch: "unknown", Unknown: true, PRState: "unknown", Reasons: []string{"GitHub PR state is unobservable"}},
		{Worktree: "/managed/safe", Branch: "landed", Task: "t-1", TaskStatus: "done", Owner: "a-owner", PRState: "merged", PRHistory: []store.CleanupPR{{Number: 1, State: "superseded"}, {Number: 2, State: "merged"}}, Runs: []store.CleanupRun{{ID: "run-1", Agent: "a-owner", State: "terminal"}}, Eligible: true, Reasons: []string{"safe"}},
		{Worktree: "/managed/claimed", Branch: "claimed", Task: "t-2", TaskStatus: "active", Owner: "a-live", PRState: "ambiguous", Runs: []store.CleanupRun{{ID: "run-2", Agent: "a-live", State: "live", Claims: []string{"src"}}}, Reasons: []string{"live claim"}},
	}, Artifacts: []store.CleanupArtifact{
		{Path: "/runs/run-1/transcript.log", RunID: "run-1", Task: "t-1", Classification: "durable-evidence", Reason: "retain"},
		{Path: "/runs/run-1/capture.tmp", RunID: "run-1", Task: "t-1", Classification: "generated-run-artifact", Pruneable: true, Identity: strings.Repeat("b", 64), Digest: "sha256:123", Quarantine: "/quarantine/capture.tmp", Operation: "move", Recovery: "restore", Reason: "generated"},
	}}
	textOut := &bytes.Buffer{}
	if err := renderPlan(&clikit.Ctx{Stdout: textOut}, plan); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"protected", "dirty", "unpushed", "open", "closed", "merged", "missing", "unknown", "ambiguous", "superseded", "owner=a-owner", "state=terminal", "claims=[src]", "durable-evidence", "generated-run-artifact", "sha256:123", "/quarantine/capture.tmp", "recovery: restore"} {
		if !strings.Contains(textOut.String(), want) {
			t.Errorf("text fixture omitted %q:\n%s", want, textOut)
		}
	}
	jsonOut := &bytes.Buffer{}
	if err := renderPlan(&clikit.Ctx{Stdout: jsonOut, JSON: true}, plan); err != nil {
		t.Fatal(err)
	}
	var decoded store.CleanupPlan
	if err := json.Unmarshal(jsonOut.Bytes(), &decoded); err != nil || decoded.Schema != store.CleanupPlanSchema || decoded.ID != plan.ID || len(decoded.Items) != len(plan.Items) || len(decoded.Artifacts) != len(plan.Artifacts) {
		t.Fatalf("versioned JSON did not preserve fixture: err=%v plan=%+v", err, decoded)
	}
}
