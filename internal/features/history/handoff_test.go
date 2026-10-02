package history

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/agentid"
	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/gitx"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

func executionHandoffFixture(t *testing.T) (*store.Task, string, string) {
	t.Helper()
	w := newExecWS(t)
	for _, args := range [][]string{{"init"}, {"config", "user.name", "Test"}, {"config", "user.email", "test@example.invalid"}} {
		if _, err := gitx.Run(w.Root, args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(w.Root, ".gitignore"), []byte(".dacli/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.Root, "source.txt"), []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := gitx.Run(w.Root, "add", ".gitignore", "source.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitx.Run(w.Root, "commit", "-m", "fixture"); err != nil {
		t.Fatal(err)
	}
	task := mustTask(t, w, "handoff work", store.TaskOpts{Accept: []string{"work is preserved"}})
	runID := "01HANDOFFEXECUTIONTEST00001"
	if err := os.MkdirAll(w.RunDir(runID), 0o755); err != nil {
		t.Fatal(err)
	}
	request := store.RootHandoffRequest{
		Schema: store.RootHandoffSchema, Verification: []store.RootHandoffVerification{{Command: "go test ./...", ExitCode: 0, Result: "pass"}},
		FailedOperation: "git commit", FailureClass: "filesystem_sandbox_refusal", Stderr: "index.lock: permission denied",
		NextAction: "owner commits after re-observation",
	}
	raw, _ := json.Marshal(request)
	if err := os.WriteFile(filepath.Join(w.RunDir(runID), store.RootHandoffRequestFile), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.Root, "source.txt"), []byte("after\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.CaptureRootHandoff(w, runID, task.ID, "a-worker", w.Root, store.RootHandoffRequest{}, time.Now()); err != nil || !ok {
		t.Fatalf("capture = %t, %v", ok, err)
	}
	return task, runID, w.Root
}

func TestHandoffConsumeReobservesAndIsRootOnly(t *testing.T) {
	_, runID, root := executionHandoffFixture(t)
	ctx, out, _ := newCtx(root)
	if err := cmdHandoffConsume(ctx, []string{runID}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "after re-observing 1 path") {
		t.Fatalf("consume did not report re-observation: %s", out.String())
	}
	w, err := workspace.Find(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(w.RunDir(runID), store.RootHandoffConsumedFile)); err != nil {
		t.Fatalf("consumption receipt missing: %v", err)
	}
}

func TestHandoffConsumeDoesNotBroadenWorkerAuthority(t *testing.T) {
	_, runID, root := executionHandoffFixture(t)
	w, err := workspace.Find(root)
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := agentid.Spawn(w, &agentid.Identity{ID: agentid.RootID, Grant: model.GrantRW}, "worker", model.GrantRW)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(agentid.EnvVar, token)
	ctx, _, _ := newCtx(root)
	if err := cmdHandoffConsume(ctx, []string{runID}); clikit.ExitCode(err) != 3 || !strings.Contains(err.Error(), "owner-only") {
		t.Fatalf("worker consume = %v, want owner-only refusal", err)
	}
}

func TestAgentsReportsTerminalHandoffRequired(t *testing.T) {
	_, runID, root := executionHandoffFixture(t)
	ctx, out, _ := newCtx(root)
	if err := cmdAgents(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "HANDOFF-REQUIRED") || !strings.Contains(got, runID[:10]) {
		t.Fatalf("agents hid pending root handoff:\n%s", got)
	}
}
