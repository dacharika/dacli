package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	providerv1 "github.com/mlnomadpy/dacli/contracts/provider/v1"
	"github.com/mlnomadpy/dacli/internal/application"
	"github.com/mlnomadpy/dacli/internal/clikit"
)

const providerConfigMaxBytes = 16 * 1024

func cmdProviderReadiness(ctx *Ctx, args []string) error {
	flags, err := clikit.ParseFlags(args, "config")
	if err != nil {
		return err
	}
	if err := flags.Reject("config", "auth-status"); err != nil {
		return err
	}
	if len(flags.Pos) != 0 {
		return clikit.Usagef("provider readiness accepts no positional arguments")
	}
	if len(flags.All("config")) > 1 {
		return clikit.Usagef("--config must appear once")
	}
	if len(flags.All("auth-status")) > 1 {
		return clikit.Usagef("--auth-status must appear once")
	}
	if value := strings.ToLower(strings.TrimSpace(flags.Get("auth-status"))); value != "" && value != "true" && value != "false" && value != "1" && value != "0" {
		return clikit.Usagef("--auth-status accepts only true or false")
	}
	configPath := flags.Get("config")
	inputs := application.DiscoveryInputs{
		Path:         os.Getenv("PATH"),
		Home:         os.Getenv("HOME"),
		LocalAppData: os.Getenv("LOCALAPPDATA"),
		GOOS:         runtime.GOOS,
		WorkingDir:   ctx.Cwd,
	}
	if configPath != "" {
		config, err := readProviderConfig(ctx.Cwd, configPath)
		if err != nil {
			return err
		}
		inputs.ExplicitPaths = map[application.Provider]string{}
		if config.CodexPath != "" {
			inputs.ExplicitPaths[application.ProviderCodex] = config.CodexPath
		}
		if config.ClaudeCodePath != "" {
			inputs.ExplicitPaths[application.ProviderClaudeCode] = config.ClaudeCodePath
		}
		if config.AntigravityPath != "" {
			inputs.ExplicitPaths[application.ProviderAntigravity] = config.AntigravityPath
		}
	}
	report := application.ProviderReadiness(context.Background(), application.ProviderReadinessInputs{
		Discovery:  inputs,
		AuthStatus: flags.Bool("auth-status"),
		ObservedAt: time.Now().UTC(),
	}, providerDiagnosticRunner{timeout: 2 * time.Second, maxOutput: 4096})
	if ctx.JSON {
		return json.NewEncoder(ctx.Stdout).Encode(report)
	}
	return writeProviderReadinessHuman(ctx, report)
}

func readProviderConfig(cwd, path string) (providerv1.Config, error) {
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return providerv1.Config{}, clikit.Refusedf("provider selection config is unavailable")
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > providerConfigMaxBytes {
		return providerv1.Config{}, clikit.Refusedf("provider selection config must be a bounded regular file")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return providerv1.Config{}, clikit.Refusedf("provider selection config is unavailable")
	}
	config, err := providerv1.DecodeConfig(contents, providerConfigMaxBytes)
	if err != nil {
		return providerv1.Config{}, clikit.Usagef("provider selection config is invalid")
	}
	return config, nil
}

type providerDiagnosticRunner struct {
	timeout   time.Duration
	maxOutput int
}

func (r providerDiagnosticRunner) RunProviderProbe(ctx context.Context, provider application.Provider, executable string, args []string) application.ProviderProbeResult {
	if !filepath.IsAbs(executable) || !fixedProviderDiagnostic(provider, args) {
		return application.ProviderProbeResult{ExitCode: -1}
	}
	maxOutput := r.maxOutput
	if maxOutput <= 0 || maxOutput > 4096 {
		maxOutput = 4096
	}
	timeout := r.timeout
	if timeout <= 0 || timeout > 2*time.Second {
		timeout = 2 * time.Second
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, executable, args...)
	// The provider executable may exit while a descendant still holds the
	// stdout pipe open. WaitDelay bounds os/exec's copy-goroutine wait; it does
	// not claim that arbitrary descendants were terminated.
	cmd.WaitDelay = 250 * time.Millisecond
	cmd.Stdin = nil
	stdout := &boundedDiagnosticBuffer{limit: maxOutput}
	cmd.Stdout = stdout
	cmd.Stderr = io.Discard
	err := cmd.Run()
	result := application.ProviderProbeResult{Stdout: append([]byte(nil), stdout.Bytes()...), Truncated: stdout.truncated}
	if probeCtx.Err() != nil {
		result.TimedOut = true
		return result
	}
	if err == nil {
		result.ExitCode = 0
		return result
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
		return result
	}
	result.ExitCode = -1
	return result
}

func fixedProviderDiagnostic(provider application.Provider, args []string) bool {
	if len(args) == 1 && args[0] == "--version" {
		return true
	}
	if len(args) != 2 || args[1] != "status" {
		return false
	}
	switch provider {
	case application.ProviderCodex:
		return args[0] == "login"
	case application.ProviderClaudeCode:
		return args[0] == "auth"
	default:
		return false
	}
}

type boundedDiagnosticBuffer struct {
	data      []byte
	limit     int
	truncated bool
}

func (b *boundedDiagnosticBuffer) Write(value []byte) (int, error) {
	original := len(value)
	remaining := b.limit - len(b.data)
	if remaining <= 0 {
		b.truncated = true
		return original, nil
	}
	if len(value) > remaining {
		b.truncated = true
		value = value[:remaining]
	}
	b.data = append(b.data, value...)
	return original, nil
}

func (b *boundedDiagnosticBuffer) Bytes() []byte { return b.data }

func writeProviderReadinessHuman(ctx *Ctx, report providerv1.ReadinessReport) error {
	for _, provider := range report.Providers {
		version := provider.Version
		if version == "" {
			version = provider.VersionState
		}
		_, _ = fmt.Fprintf(ctx.Stdout, "%s: install=%s source=%s version=%s auth=%s (%s)\n", provider.Provider, provider.InstallState, provider.InstallSource, version, provider.AuthState, provider.Reason)
	}
	_, _ = fmt.Fprintln(ctx.Stdout, report.Notice)
	return nil
}
