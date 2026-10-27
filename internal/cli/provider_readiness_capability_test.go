package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	providerv1 "github.com/mlnomadpy/dacli/contracts/provider/v1"
)

// Provider diagnostics execute caller-selected code. Fixed arguments do not
// establish that the selected executable is read-only, so the rw boundary
// must run before probing even when generic dispatcher exemptions apply.
func TestProviderReadinessRefusesReadOnlyExecutableSelection(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix shell diagnostic fixture")
	}
	workspaceDir := t.TempDir()
	run(t, workspaceDir, 0, "init", "--name", "provider-capability")
	spawned := run(t, workspaceDir, 0, "agent", "spawn", "--role", "auditor", "--grant", "ro")
	token := strings.TrimSpace(strings.Split(strings.TrimSpace(spawned), "\n")[0])
	if len(token) != 48 {
		t.Fatal("fixture did not mint a legacy agent token")
	}
	t.Setenv("DACLI_AGENT", token)
	if who := run(t, workspaceDir, 0, "whoami"); !strings.Contains(who, "grant: ro") {
		t.Fatal("fixture did not resolve an actual read-only identity")
	}
	for _, mode := range []string{"explicit-config", "path", "config-named-dry-run", "outside-workspace"} {
		t.Run(mode, func(t *testing.T) {
			providerDir := t.TempDir()
			marker := filepath.Join(providerDir, "executed")
			t.Setenv("PROVIDER_READINESS_MARKER", marker)
			binary := providerScript(t, providerDir, "codex", "printf executed > \"$PROVIDER_READINESS_MARKER\"\nprintf 'codex-cli 1.2.3\\n'\n")
			t.Setenv("PATH", providerDir)
			t.Setenv("HOME", providerDir)
			t.Setenv("LOCALAPPDATA", providerDir)
			t.Setenv("USERPROFILE", providerDir)
			cwd := workspaceDir
			var args []string
			if mode != "path" {
				raw, err := json.Marshal(providerv1.Config{SchemaVersion: 1, CodexPath: binary})
				if err != nil {
					t.Fatal(err)
				}
				configPath := filepath.Join(providerDir, "selection.json")
				if mode == "config-named-dry-run" {
					configPath = filepath.Join(workspaceDir, "--dry-run")
					args = []string{"--config", "--dry-run"}
				} else {
					args = []string{"--config", configPath}
				}
				if err := os.WriteFile(configPath, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "outside-workspace" {
				cwd = providerDir
			}
			cmd, rest := match([]string{"provider", "readiness"})
			if cmd == nil {
				t.Fatal("provider readiness is not registered")
			}
			var stdout, stderr bytes.Buffer
			err := invoke(&Ctx{Cwd: cwd, Stdout: &stdout, Stderr: &stderr, JSON: true}, cmd, append(rest, args...))
			_, markerErr := os.Stat(marker)
			t.Logf("Mutates=%v exit=%d marker_written=%v", cmd.Mutates, exitCode(err), markerErr == nil)
			if err == nil {
				t.Error("read-only diagnostic request succeeded instead of refusing execution")
			}
			if mode != "outside-workspace" && exitCode(err) != 3 {
				t.Errorf("read-only diagnostic exit=%d, want policy refusal 3", exitCode(err))
			}
			if markerErr == nil {
				t.Error("provider fixture ran and wrote a marker for a read-only caller")
			} else if !os.IsNotExist(markerErr) {
				t.Fatal(markerErr)
			}
		})
	}
}
