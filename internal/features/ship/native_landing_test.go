package ship

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/store"
)

func TestNativeBatchLandingShipRetainsUnknownSelectedEvidence(t *testing.T) {
	root, w := shipEnv(t)
	tasks, err := store.ListTasks(w, "p", model.StatusOpen)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("fixture task selection: %d %v", len(tasks), err)
	}
	task := tasks[0]
	branch := store.TaskBranch(task)
	gitAt(t, root, "checkout", "-b", branch)
	if err := os.WriteFile(filepath.Join(root, "delivered.txt"), []byte("delivered"), 0600); err != nil {
		t.Fatal(err)
	}
	gitAt(t, root, "add", "delivered.txt")
	gitAt(t, root, "commit", "-m", "source")
	head := gitAt(t, root, "rev-parse", "HEAD")
	gitAt(t, root, "checkout", "main")
	gitAt(t, root, "merge", "--no-ff", branch, "-m", "land captured source")
	remote := filepath.Join(t.TempDir(), "origin.git")
	gitAt(t, root, "init", "--bare", remote)
	gitAt(t, root, "remote", "add", "origin", remote)
	gitAt(t, root, "push", "-q", "origin", "main")
	task.Doc.Front.Set("native_landing.current", "malformed")
	if err := store.SaveTask(task); err != nil {
		t.Fatal(err)
	}
	wave := []*store.Task{task}
	refs := map[string]waveRef{taskRef(task): {branch: branch, shas: []string{head}}}
	if err := requireFreshLanding(w, "main", wave, refs); err == nil || !strings.Contains(err.Error(), "selected native landing observation") {
		t.Fatalf("fresh merged legacy head overrode selected native evidence: %v", err)
	}
	recordWaveLanding(w, "main", wave, refs)
	current, err := store.FindTask(w, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	log, _ := current.Doc.Section("Log")
	if !strings.Contains(log.Content, "could not be checked against main") || strings.Contains(log.Content, " is merged into main") {
		t.Fatalf("wave recorder replaced unknown native truth with legacy success: %s", log.Content)
	}
}
