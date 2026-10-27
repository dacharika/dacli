package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	providerv1 "github.com/mlnomadpy/dacli/contracts/provider/v1"
	"github.com/mlnomadpy/dacli/internal/application"
)

func TestProviderReadinessJSONUsesExplicitConfigAndDoesNotRunAuthByDefault(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script provider fixtures require a Unix host")
	}
	root := t.TempDir()
	initializeProviderReadinessWorkspace(t, root)
	codex := providerScript(t, root, "codex", "printf 'codex-cli 1.2.3\\n'\n")
	claude := providerScript(t, root, "claude", "printf '2.4.5 (Claude Code)\\n'\n")
	agy := providerScript(t, root, "agy", "printf 'agy 3.4.5\\n'\n")
	configPath := filepath.Join(root, "provider.json")
	config := providerv1.Config{SchemaVersion: 1, CodexPath: codex, ClaudeCodePath: claude, AntigravityPath: agy}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Join(root, "empty-path"))
	t.Setenv("HOME", root)
	var stdout, stderr bytes.Buffer
	ctx := &Ctx{Stdout: &stdout, Stderr: &stderr, Cwd: root, JSON: true}
	if err := invokeProviderReadiness(ctx, []string{"--config", configPath}); err != nil {
		t.Fatalf("handler: %v (stderr=%s)", err, stderr.String())
	}
	report, err := providerv1.DecodeReport(bytes.TrimSpace(stdout.Bytes()), 16*1024)
	if err != nil {
		t.Fatalf("invalid JSON report %q: %v", stdout.String(), err)
	}
	if len(report.Providers) != 3 || report.AuthStatusRequested {
		t.Fatalf("unexpected report: %+v", report)
	}
	for _, provider := range report.Providers {
		if provider.InstallState != "installed" || provider.InstallSource != "explicit_config" || provider.AuthState != "unknown" || provider.VersionState != "known" {
			t.Errorf("unexpected provider state: %+v", provider)
		}
	}
}

func TestProviderDiagnosticRunnerAllowsOnlyFixedProviderCommandsAndBoundsOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script provider fixtures require a Unix host")
	}
	root := t.TempDir()
	binary := providerScript(t, root, "codex", "head -c 6000 /dev/zero | tr '\\000' x\n")
	runner := providerDiagnosticRunner{maxOutput: 10}
	result := runner.RunProviderProbe(context.Background(), application.ProviderCodex, binary, []string{"--version"})
	if len(result.Stdout) != 10 || !result.Truncated {
		t.Fatalf("output was not bounded: bytes=%d truncated=%v", len(result.Stdout), result.Truncated)
	}
	if got := runner.RunProviderProbe(context.Background(), application.ProviderAntigravity, binary, []string{"login", "status"}); got.ExitCode != -1 || got.Stdout != nil {
		t.Fatalf("unsupported auth command was accepted: %+v", got)
	}
}

func TestProviderDiagnosticRunnerBoundsInheritedPipeWait(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script inherited-pipe fixture requires a Unix host")
	}
	root := t.TempDir()
	binary := providerScript(t, root, "codex", "(sleep 3) &\nexit 0\n")
	ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
	defer cancel()
	started := time.Now()
	(providerDiagnosticRunner{timeout: 750 * time.Millisecond, maxOutput: 4096}).RunProviderProbe(ctx, application.ProviderCodex, binary, []string{"--version"})
	elapsed := time.Since(started)
	if elapsed > 1600*time.Millisecond {
		t.Fatalf("inherited pipe delayed return: elapsed=%s", elapsed)
	}
}

func TestProviderReadinessRejectsUnsafeOrMalformedConfig(t *testing.T) {
	root := t.TempDir()
	initializeProviderReadinessWorkspace(t, root)
	configPath := filepath.Join(root, "provider.json")
	if err := os.WriteFile(configPath, []byte(`{"schema_version":1,"token":"secret"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	ctx := &Ctx{Stdout: &stdout, Stderr: &stderr, Cwd: root, JSON: true}
	err := invokeProviderReadiness(ctx, []string{"--config", configPath})
	if err == nil || !strings.Contains(err.Error(), "config is invalid") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("malformed config did not fail safely: %v", err)
	}
	link := filepath.Join(root, "provider-link.json")
	if err := os.Symlink(configPath, link); err != nil {
		t.Fatal(err)
	}
	if err := invokeProviderReadiness(ctx, []string{"--config", link}); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("symlink config was not refused: %v", err)
	}
}

func initializeProviderReadinessWorkspace(t *testing.T, root string) {
	t.Helper()
	clearProviderReadinessAgent(t)
	run(t, root, 0, "init", "--name", "provider-readiness-test")
}

func clearProviderReadinessAgent(t *testing.T) {
	t.Helper()
	prior, hadPrior := os.LookupEnv("DACLI_AGENT")
	if err := os.Unsetenv("DACLI_AGENT"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		var err error
		if hadPrior {
			err = os.Setenv("DACLI_AGENT", prior)
		} else {
			err = os.Unsetenv("DACLI_AGENT")
		}
		if err != nil {
			t.Errorf("restore DACLI_AGENT: %v", err)
		}
	})
}

func invokeProviderReadiness(ctx *Ctx, args []string) error {
	cmd, rest := match([]string{"provider", "readiness"})
	if cmd == nil {
		return errors.New("provider readiness is not registered")
	}
	return invoke(ctx, cmd, append(rest, args...))
}

func providerScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	content := "#!/bin/sh\n" + body
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}
