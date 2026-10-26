package cli

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mlnomadpy/dacli/internal/guidance"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

func TestNativeGuidanceCommandsDeclareCorrectCapabilities(t *testing.T) {
	want := map[string]bool{
		"native guidance diagnose":        false,
		"native guidance refresh-preview": true,
		"native guidance refresh-apply":   true,
	}
	for path, mutates := range want {
		found := false
		for _, command := range commands {
			if command.Path == path {
				found = true
				if command.Mutates != mutates || !command.JSON {
					t.Fatalf("%s capability: mutates=%v json=%v", path, command.Mutates, command.JSON)
				}
				if path == "native guidance refresh-apply" && (!strings.Contains(command.Usage, "--config <owner>") || !strings.Contains(command.Usage, "--plan <private-file>")) {
					t.Fatalf("refresh apply is missing owner authentication or pinned-plan usage: %s", command.Usage)
				}
			}
		}
		if !found {
			t.Fatalf("required native guidance command is not registered: %s", path)
		}
	}
}

func TestNativeGuidanceRefreshCLIReplacesKnownLegacyAndPreservesAdjacentPrivateBytes(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller path unavailable")
	}
	repo := filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
	fixture := filepath.Join(repo, "internal", "guidance", "testdata", "known-legacy")
	root, err := os.MkdirTemp("/private/tmp", "dacli-native-guidance-cli-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if err := copyGuidanceFixture(fixture, root); err != nil {
		t.Fatal(err)
	}
	privatePath := filepath.Join(root, "native-profile.json")
	privateBytes := []byte("preserve private selected profile\n")
	if err := os.WriteFile(privatePath, privateBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	plan := root + ".plan.json"
	out := run(t, t.TempDir(), 0, "native", "guidance", "refresh-preview", "--skill-root", root, "--plan-out", plan, "--json")
	if !strings.Contains(out, "plan_digest") {
		t.Fatalf("preview did not return its immutable plan: %s", out)
	}
	info, err := os.Stat(plan)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("plan is not a private file: info=%v err=%v", info, err)
	}
	workspaceRoot := t.TempDir()
	if _, err := workspace.Init(workspaceRoot, "guidance-refresh-fixture"); err != nil {
		t.Fatal(err)
	}
	private := t.TempDir()
	configPath := filepath.Join(private, "owner.json")
	configBytes, err := json.Marshal(map[string]any{"schema_version": 1, "workspace": workspaceRoot, "authority": filepath.Join(private, "authority.json"), "installation_id": "guidance-refresh-fixture", "owner_credential_file": filepath.Join(private, "owner.token")})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, configBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	run(t, workspaceRoot, 0, "native", "bootstrap", "--config", configPath)
	out = run(t, workspaceRoot, 0, "native", "guidance", "refresh-apply", "--config", configPath, "--plan", plan, "--json")
	if !strings.Contains(out, `"state":"completed"`) || !strings.Contains(out, `"backups"`) {
		t.Fatalf("apply did not return durable receipt: %s", out)
	}
	canonical := filepath.Join(repo, "skills", "dacli")
	for _, rel := range guidance.KnownFiles() {
		got, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		want, err := os.ReadFile(filepath.Join(canonical, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("refreshed %s differs from current checked-in guidance", rel)
		}
	}
	gotPrivate, err := os.ReadFile(privatePath)
	if err != nil || !bytes.Equal(gotPrivate, privateBytes) {
		t.Fatal("refresh changed adjacent private profile")
	}
}

func copyGuidanceFixture(source, target string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		to := filepath.Join(target, rel)
		if entry.IsDir() {
			return os.MkdirAll(to, 0o755)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(to, b, 0o644)
	})
}
