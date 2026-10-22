package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mlnomadpy/dacli/internal/agentid"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/procmon"
	"github.com/mlnomadpy/dacli/internal/ulid"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

func TestRoleRemovalPreservesHistoricalAttribution(t *testing.T) {
	dir := t.TempDir()
	run(t, dir, 0, "init", "--name", "x")
	run(t, dir, 0, "project", "add", "P", "--slug", "p")
	run(t, dir, 0, "task", "add", "Run one terminal agent", "--project", "p", "--accept", "agent exits")

	run(t, dir, 0, "role", "add", "ephemeral", "--grant", "rw", "--runtime", "terminal", "--scope", "internal/**")

	w, err := workspace.Find(dir)
	if err != nil {
		t.Fatal(err)
	}
	childID, _, err := agentid.Spawn(w, &agentid.Identity{ID: agentid.RootID, Grant: model.GrantRW}, "ephemeral", model.GrantRW)
	if err != nil {
		t.Fatal(err)
	}
	historicalID := ulid.New()
	if err = os.MkdirAll(w.RunDir(historicalID), 0o700); err != nil {
		t.Fatal(err)
	}
	for filename, body := range map[string]string{"invocation.txt": "run: " + historicalID + "\nchild: " + childID + "\nrole: ephemeral\n", "outcome.md": "outcome: finished\nexit: -\n"} {
		if err = os.WriteFile(filepath.Join(w.RunDir(historicalID), filename), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err = procmon.WriteRecord(filepath.Join(w.RunDir(historicalID), "proc.txt"), procmon.Record{RunID: historicalID, Child: childID, Role: "ephemeral", Runtime: "terminal"}); err != nil {
		t.Fatal(err)
	}
	run(t, dir, 0, "agent", "retire", childID)
	runs := run(t, dir, 0, "runs", "list")
	runID := strings.Fields(runs)[0]
	detail := run(t, dir, 0, "runs", "show", runID)
	var child string
	for _, line := range strings.Split(detail, "\n") {
		if strings.HasPrefix(line, "child: ") {
			child = strings.TrimSpace(strings.TrimPrefix(line, "child: "))
			break
		}
	}
	if child == "" {
		t.Fatalf("runs show did not expose the spawned child:\n%s", detail)
	}

	run(t, dir, 0, "role", "rm", "ephemeral")
	agent := run(t, dir, 0, "agent", "show", child)
	if !strings.Contains(agent, "role:     ephemeral") || !strings.Contains(agent, runID) {
		t.Fatalf("agent attribution did not survive role removal:\n%s", agent)
	}
	after := run(t, dir, 0, "runs", "show", runID)
	if !strings.Contains(after, "child: "+child) || !strings.Contains(after, "role: ephemeral") {
		t.Fatalf("run attribution did not survive role removal:\n%s", after)
	}
}
