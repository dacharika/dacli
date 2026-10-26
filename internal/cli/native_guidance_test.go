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
				if (path == "native guidance refresh-apply" && (!strings.Contains(command.Usage, "--config <owner>") || !strings.Contains(command.Usage, "--plan <private-file>"))) || (path == "native guidance refresh-preview" && (!strings.Contains(command.Usage, "--config <owner>") || !strings.Contains(command.Usage, "--plan-out <private-file>"))) {
					t.Fatalf("refresh command is missing owner authentication or pinned-plan usage: %s", command.Usage)
				}
			}
		}
		if !found {
			t.Fatalf("required native guidance command is not registered: %s", path)
		}
	}
}

func TestNativeGuidanceRefreshCLIReplacesKnownLegacyAndPreservesAdjacentPrivateBytes(t *testing.T) {
	t.Setenv("HOME", canonicalCLITestTemp(t, "dacli-native-guidance-home-"))
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller path unavailable")
	}
	repo := filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
	fixture := filepath.Join(repo, "internal", "guidance", "testdata", "known-legacy")
	root := canonicalCLITestTemp(t, "dacli-native-guidance-cli-")
	var err error
	if err := copyGuidanceFixture(fixture, root); err != nil {
		t.Fatal(err)
	}
	privatePath := filepath.Join(root, "native-profile.json")
	privateBytes := []byte("preserve private selected profile\n")
	if err := os.WriteFile(privatePath, privateBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	workspaceRoot := t.TempDir()
	if _, err := workspace.Init(workspaceRoot, "guidance-refresh-fixture"); err != nil {
		t.Fatal(err)
	}
	private := canonicalCLITestTemp(t, "dacli-native-guidance-private-")
	configPath := filepath.Join(private, "owner.json")
	configBytes, err := json.Marshal(map[string]any{"schema_version": 1, "workspace": workspaceRoot, "authority": filepath.Join(private, "authority.json"), "installation_id": "guidance-refresh-fixture", "owner_credential_file": filepath.Join(private, "owner.token")})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, configBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	runGuidanceCLI(t, workspaceRoot, 0, "native", "bootstrap", "--config", configPath)
	plan := filepath.Join(private, "refresh.plan.json")
	if out := runGuidanceCLI(t, workspaceRoot, 2, "native", "guidance", "refresh-preview", "--skill-root", root, "--plan-out", plan, "--json"); !strings.Contains(out, "--config") {
		t.Fatalf("preview without explicit owner config was not rejected clearly: %s", out)
	}
	workerConfig := filepath.Join(private, "worker.json")
	workerToken := filepath.Join(private, "worker.token")
	if err := os.WriteFile(workerToken, []byte("not-an-owner-credential"), 0o600); err != nil {
		t.Fatal(err)
	}
	workerConfigBytes, err := json.Marshal(map[string]any{"schema_version": 1, "workspace": workspaceRoot, "authority": filepath.Join(private, "authority.json"), "installation_id": "guidance-refresh-fixture", "owner_credential_file": workerToken})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workerConfig, workerConfigBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if out := runGuidanceCLI(t, workspaceRoot, 3, "native", "guidance", "refresh-preview", "--config", workerConfig, "--skill-root", root, "--plan-out", plan, "--json"); !strings.Contains(out, "refused") {
		t.Fatalf("invalid worker-owner config was not refused: %s", out)
	}
	if _, err := os.Lstat(plan); !os.IsNotExist(err) {
		t.Fatalf("invalid owner config wrote plan: %v", err)
	}
	t.Setenv("DACLI_AGENT", "invalid-legacy-token-does-not-imply-owner")
	out := runGuidanceCLI(t, workspaceRoot, 0, "native", "guidance", "refresh-preview", "--config", configPath, "--skill-root", root, "--plan-out", plan, "--json")
	if !strings.Contains(out, "plan_digest") {
		t.Fatalf("preview did not return its immutable plan: %s", out)
	}
	info, err := os.Stat(plan)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("plan is not a private file: info=%v err=%v", info, err)
	}
	for _, args := range [][]string{
		{"native", "guidance", "refresh-preview", "--config", configPath, "--skill-root", root, "--plan-out", plan, "--json"},
		{"native", "guidance", "refresh-apply", "--config", configPath, "--plan", plan, "--json"},
	} {
		_, errOut, code := executor(workspaceRoot)(args, true)
		if code != 3 || !strings.Contains(errOut, "local-only") {
			t.Fatalf("MCP CLI escaped local-only owner refresh command: args=%v code=%d err=%s", args, code, errOut)
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".dacli-guidance-backups")); !os.IsNotExist(err) {
		t.Fatalf("MCP CLI refusal wrote backups: %v", err)
	}
	out = runGuidanceCLI(t, workspaceRoot, 0, "native", "guidance", "refresh-apply", "--config", configPath, "--plan", plan, "--json")
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

func canonicalCLITestTemp(t *testing.T, prefix string) string {
	t.Helper()
	dir, err := os.MkdirTemp("", prefix)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

// runGuidanceCLI mirrors Main's global --json extraction before crossing invoke.
// The older generic run helper passes raw flags and cannot prove this JSON path.
func runGuidanceCLI(t *testing.T, dir string, wantCode int, argv ...string) string {
	t.Helper()
	var out, errb bytes.Buffer
	ctx := &Ctx{Stdout: &out, Stderr: &errb, Cwd: dir}
	args := make([]string, 0, len(argv))
	for _, arg := range argv {
		if arg == "--json" {
			ctx.JSON = true
			continue
		}
		args = append(args, arg)
	}
	command, rest := match(args)
	if command == nil {
		t.Fatalf("no such command: %v", args)
	}
	err := invoke(ctx, command, rest)
	if got := exitCode(err); got != wantCode {
		t.Fatalf("%v exit %d want %d: %v; stdout=%s stderr=%s", argv, got, wantCode, err, out.String(), errb.String())
	}
	result := out.String() + errb.String()
	if err != nil {
		result += "\n" + err.Error()
	}
	return result
}
