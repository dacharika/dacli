package application

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mlnomadpy/dacli/internal/governance/sessions"
)

func TestNativeStorageProfileActualExperimentAndFreshOwnerBoundary(t *testing.T) {
	f := nativeSetup(t, "printf native-storage-private-command")
	dir := t.TempDir()
	config, credential := filepath.Join(dir, "owner.json"), filepath.Join(dir, "owner.token")
	if e := os.WriteFile(credential, []byte(f.owner), 0600); e != nil {
		t.Fatal(e)
	}
	c := recoveryOwnerConfig{SchemaVersion: 1, Workspace: f.workspace.Root, Authority: f.path, InstallationID: "installation", OwnerCredentialFile: credential}
	saveReviewPrivate(t, config, c)
	owner, e := OpenNativeStorageOwner(config)
	if e != nil {
		t.Fatal(e)
	}
	before, _ := os.ReadFile(f.path)
	observation, e := owner.Observe()
	if e != nil {
		t.Fatal(e)
	}
	public, _ := json.Marshal(observation)
	for _, secret := range []string{f.owner, f.workerToken, "OwnerHash", "native-storage-private-command"} {
		if strings.Contains(string(public), secret) {
			t.Fatal("owner storage leaked private state")
		}
	}
	out, e := owner.Experiment(filepath.Join(t.TempDir(), "projection"), "projection-only")
	if e != nil || !out.Reconciled || out.Authoritative || out.TaskCount != 1 {
		t.Fatalf("actual storage owner experiment failed: %v", e)
	}
	plan, e := owner.Retention("p")
	if e != nil || !plan.DryRun {
		t.Fatalf("owner retention dry-run failed: %v", e)
	}
	if e = os.WriteFile(credential, []byte(f.workerToken), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = owner.Observe(); !errors.Is(e, sessions.ErrDenied) {
		t.Fatal("storage owner reused worker credential")
	}
	if _, e = owner.Experiment(filepath.Join(t.TempDir(), "denied"), "projection-only"); !errors.Is(e, sessions.ErrDenied) {
		t.Fatal("storage experiment bypassed fresh owner")
	}
	after, _ := os.ReadFile(f.path)
	if !bytes.Equal(before, after) {
		t.Fatal("storage profile changed live authority")
	}
}
