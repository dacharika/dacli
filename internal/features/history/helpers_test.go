package history

import (
	"bytes"
	"os"
	"testing"

	"github.com/mlnomadpy/dacli/internal/agentid"
	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

const testProject = "p"

func newCtx(cwd string) (*clikit.Ctx, *bytes.Buffer, *bytes.Buffer) {
	var out, errb bytes.Buffer
	return &clikit.Ctx{Cwd: cwd, Stdout: &out, Stderr: &errb}, &out, &errb
}
func newExecWS(t *testing.T) *workspace.Workspace {
	t.Helper()
	if v, ok := os.LookupEnv(agentid.EnvVar); ok {
		t.Setenv(agentid.EnvVar, v)
		_ = os.Unsetenv(agentid.EnvVar)
	}
	w, err := workspace.Init(t.TempDir(), "exec-test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProject(w, agentid.RootID, "Test project", testProject, "Ship it.", ""); err != nil {
		t.Fatal(err)
	}
	return w
}

func mustTask(t *testing.T, w *workspace.Workspace, title string, opts store.TaskOpts) *store.Task {
	t.Helper()
	task, err := store.CreateTask(w, agentid.RootID, testProject, title, opts)
	if err != nil {
		t.Fatal(err)
	}
	return task
}
