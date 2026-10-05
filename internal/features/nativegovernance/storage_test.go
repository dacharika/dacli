package nativegovernance

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mlnomadpy/dacli/internal/clikit"
)

func TestNativeStorageCLIExplicitOwnerJSONAndNonAuthorityExperiment(t *testing.T) {
	f := setupVerification(t, "printf storage-private-command", false)
	run := func(kind string, args ...string) (string, error) {
		return verificationRun(t, f.w, func(c *clikit.Ctx, a []string) error { return storageCommand(c, a, kind) }, args...)
	}
	for _, kind := range []string{"observe", "experiment", "retention"} {
		args := []string{"--config", f.config}
		if kind == "experiment" {
			args = append(args, "--destination", filepath.Join(t.TempDir(), "projection"), "--mode", "projection-only")
		}
		if kind == "retention" {
			args = append(args, "--project", "p")
		}
		output, e := run(kind, args...)
		if e != nil {
			t.Fatalf("actual storage CLI %s: %v", kind, e)
		}
		var typed struct {
			Schema string         `json:"schema"`
			Result map[string]any `json:"result"`
		}
		if json.Unmarshal([]byte(output), &typed) != nil || typed.Schema != "native-storage/v1" {
			t.Fatal("storage CLI did not emit versioned JSON")
		}
		for _, private := range []string{f.owner, f.workerToken, "OwnerHash", "storage-private-command", "PrivateBytes"} {
			if strings.Contains(output, private) {
				t.Fatal("storage CLI exposed private authority")
			}
		}
		if kind == "experiment" && (typed.Result["authoritative"] != false || typed.Result["reconciled"] != true) {
			t.Fatal("storage CLI activated authoritative experiment")
		}
		if kind == "retention" && typed.Result["dry_run"] != true {
			t.Fatal("retention CLI is not dry-run")
		}
	}
	if _, e := run("experiment", "--config", f.config, "--destination", filepath.Join(t.TempDir(), "writer"), "--mode", "activate-writer"); clikit.ExitCode(e) != 3 {
		t.Fatalf("storage CLI accepted unsupported writer mode: %v", e)
	}
	before, _ := os.ReadFile(f.worker.CredentialFile)
	if _, e := run("observe", "--config", f.connection); clikit.ExitCode(e) != 3 {
		t.Fatal("worker profile opened owner storage")
	}
	after, _ := os.ReadFile(f.worker.CredentialFile)
	if string(before) != string(after) {
		t.Fatal("storage read replaced worker credential")
	}
}
