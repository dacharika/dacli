package application

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	providerv1 "github.com/mlnomadpy/dacli/contracts/provider/v1"
)

type providerProbeFixture struct {
	results map[string]ProviderProbeResult
	calls   []providerProbeCall
}

type providerProbeCall struct {
	path string
	args []string
}

func (f *providerProbeFixture) RunProviderProbe(ctx context.Context, provider Provider, path string, args []string) ProviderProbeResult {
	f.calls = append(f.calls, providerProbeCall{path: path, args: append([]string(nil), args...)})
	key := filepath.Base(path) + " " + strings.Join(args, " ")
	if err := ctx.Err(); err != nil {
		return ProviderProbeResult{TimedOut: true}
	}
	return f.results[key]
}

func TestProviderReadinessOnlyRunsFixedVersionCommandsByDefault(t *testing.T) {
	root := t.TempDir()
	codex := executableFixture(t, filepath.Join(root, "codex", "codex"), 0o755)
	claude := executableFixture(t, filepath.Join(root, "claude", "claude"), 0o755)
	agy := executableFixture(t, filepath.Join(root, "agy", "agy"), 0o755)
	runner := &providerProbeFixture{results: map[string]ProviderProbeResult{
		"codex --version":  {Stdout: []byte("codex-cli 1.2.3\n"), ExitCode: 0},
		"claude --version": {Stdout: []byte("2.4.5 (Claude Code)\n"), ExitCode: 0},
		"agy --version":    {Stdout: []byte("agy 3.4.5\n"), ExitCode: 0},
	}}
	report := ProviderReadiness(context.Background(), ProviderReadinessInputs{
		Discovery:  DiscoveryInputs{Path: strings.Join([]string{filepath.Dir(codex), filepath.Dir(claude), filepath.Dir(agy)}, string(os.PathListSeparator)), GOOS: runtime.GOOS},
		ObservedAt: time.Date(2026, 10, 27, 19, 0, 0, 0, time.UTC),
	}, runner)
	if len(runner.calls) != 3 {
		t.Fatalf("got %d probes, want one bounded version probe per provider", len(runner.calls))
	}
	for _, call := range runner.calls {
		if len(call.args) != 1 || call.args[0] != "--version" {
			t.Fatalf("default diagnostics ran an unexpected command: %+v", call)
		}
	}
	for _, provider := range report.Providers {
		if provider.VersionState != "known" || provider.AuthState != "unknown" || provider.Reason != "diagnostic_not_requested" {
			t.Errorf("unexpected default state: %+v", provider)
		}
	}
}

func TestProviderReadinessAuthDiagnosticsAreOptInAndAntigravityStaysUnknown(t *testing.T) {
	root := t.TempDir()
	codex := executableFixture(t, filepath.Join(root, "codex", "codex"), 0o755)
	claude := executableFixture(t, filepath.Join(root, "claude", "claude"), 0o755)
	agy := executableFixture(t, filepath.Join(root, "agy", "agy"), 0o755)
	runner := &providerProbeFixture{results: map[string]ProviderProbeResult{
		"codex --version":    {Stdout: []byte("codex-cli 1.2.3"), ExitCode: 0},
		"codex login status": {Stdout: []byte("credential-text-secret"), ExitCode: 0},
		"claude --version":   {Stdout: []byte("2.4.5 (Claude Code)"), ExitCode: 0},
		"claude auth status": {Stdout: []byte(`{"authMethod":"none","configDirectory":"/private/path"}`), ExitCode: 1},
		"agy --version":      {Stdout: []byte("agy 3.4.5"), ExitCode: 0},
		"agy auth status":    {Stdout: []byte("must not run"), ExitCode: 0},
		"agy login status":   {Stdout: []byte("must not run"), ExitCode: 0},
	}}
	report := ProviderReadiness(context.Background(), ProviderReadinessInputs{
		Discovery:  DiscoveryInputs{Path: strings.Join([]string{filepath.Dir(codex), filepath.Dir(claude), filepath.Dir(agy)}, string(os.PathListSeparator)), GOOS: runtime.GOOS},
		AuthStatus: true,
	}, runner)
	if len(runner.calls) != 5 {
		t.Fatalf("got %d probes, want three versions and two auth statuses", len(runner.calls))
	}
	codexReport := providerReportFor(t, report.Providers, ProviderCodex)
	claudeReport := providerReportFor(t, report.Providers, ProviderClaudeCode)
	agyReport := providerReportFor(t, report.Providers, ProviderAntigravity)
	if codexReport.AuthState != "authenticated" || claudeReport.AuthState != "unauthenticated" {
		t.Fatalf("documented statuses were misclassified: codex=%+v claude=%+v", codexReport, claudeReport)
	}
	if agyReport.AuthState != "unknown" || agyReport.Reason != "auth_diagnostic_unsupported" {
		t.Fatalf("Antigravity authentication was guessed: %+v", agyReport)
	}
	serialized, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"credential-text-secret", "/private/path", "loggedIn", "authMethod"} {
		if strings.Contains(string(serialized), secret) {
			t.Fatalf("provider output escaped into report: %s", secret)
		}
	}
}

func TestClaudeAuthStatusRequiresDocumentedJSONMethodAndConsistentExit(t *testing.T) {
	for _, tc := range []struct {
		name  string
		probe ProviderProbeResult
		want  string
	}{
		{name: "logged-in", probe: ProviderProbeResult{Stdout: []byte(`{"authMethod":"claude.ai","configDirectory":"/private/path"}`), ExitCode: 0}, want: "authenticated"},
		{name: "logged-out", probe: ProviderProbeResult{Stdout: []byte(`{"authMethod":"none"}`), ExitCode: 1}, want: "unauthenticated"},
		{name: "empty-json", probe: ProviderProbeResult{ExitCode: 0}, want: "unknown"},
		{name: "malformed-json", probe: ProviderProbeResult{Stdout: []byte("not-json"), ExitCode: 0}, want: "unknown"},
		{name: "missing-method", probe: ProviderProbeResult{Stdout: []byte(`{"loggedIn":true}`), ExitCode: 0}, want: "unknown"},
		{name: "unknown-method", probe: ProviderProbeResult{Stdout: []byte(`{"authMethod":"personal-token"}`), ExitCode: 0}, want: "unknown"},
		{name: "inconsistent-login", probe: ProviderProbeResult{Stdout: []byte(`{"authMethod":"claude.ai"}`), ExitCode: 1}, want: "unknown"},
		{name: "inconsistent-logout", probe: ProviderProbeResult{Stdout: []byte(`{"authMethod":"none"}`), ExitCode: 0}, want: "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyAuth(ProviderClaudeCode, tc.probe); got != tc.want {
				t.Fatalf("classifyAuth() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCodexAuthStatusUsesDocumentedExitCodeWithEmptyStdout(t *testing.T) {
	if got := classifyAuth(ProviderCodex, ProviderProbeResult{ExitCode: 0}); got != "authenticated" {
		t.Fatalf("Codex empty-stdout success = %q, want authenticated", got)
	}
	if got := classifyAuth(ProviderCodex, ProviderProbeResult{ExitCode: 1}); got != "unknown" {
		t.Fatalf("Codex nonzero exit = %q, want unknown", got)
	}
	if got := classifyAuth(ProviderClaudeCode, ProviderProbeResult{ExitCode: 0}); got != "unknown" {
		t.Fatalf("Claude empty-stdout success = %q, want unknown", got)
	}
}

func TestProviderReadinessUnknownOnAmbiguousUnavailableExpiredContextAndBadVersion(t *testing.T) {
	root := t.TempDir()
	first := executableFixture(t, filepath.Join(root, "one", "codex"), 0o755)
	second := executableFixture(t, filepath.Join(root, "two", "codex"), 0o755)
	claude := executableFixture(t, filepath.Join(root, "claude", "claude"), 0o755)
	runner := &providerProbeFixture{results: map[string]ProviderProbeResult{
		"claude --version":   {Stdout: []byte("not-a-version"), ExitCode: 0},
		"claude auth status": {ExitCode: 1},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	report := ProviderReadiness(ctx, ProviderReadinessInputs{
		Discovery:  DiscoveryInputs{Path: strings.Join([]string{filepath.Dir(first), filepath.Dir(second), filepath.Dir(claude)}, string(os.PathListSeparator)), GOOS: runtime.GOOS},
		AuthStatus: true,
	}, runner)
	codexReport := providerReportFor(t, report.Providers, ProviderCodex)
	claudeReport := providerReportFor(t, report.Providers, ProviderClaudeCode)
	if codexReport.InstallState != "ambiguous" || codexReport.VersionState != "ambiguous" || codexReport.AuthState != "unknown" {
		t.Fatalf("multiple installations did not fail truthfully: %+v", codexReport)
	}
	if claudeReport.VersionState != "unknown" || claudeReport.AuthState != "unknown" {
		t.Fatalf("expired context or invalid version output produced a claim: %+v", claudeReport)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("ambiguous provider was probed or unexpected command ran: %+v", runner.calls)
	}
}

func TestProviderReadinessOfflineAndProbeFailuresRemainUnknown(t *testing.T) {
	root := t.TempDir()
	codex := executableFixture(t, filepath.Join(root, "codex"), 0o755)
	runner := &providerProbeFixture{results: map[string]ProviderProbeResult{
		"codex --version":    {TimedOut: true},
		"codex login status": {ExitCode: 2},
	}}
	report := ProviderReadiness(context.Background(), ProviderReadinessInputs{
		Discovery:  DiscoveryInputs{ExplicitPaths: map[Provider]string{ProviderCodex: codex}, GOOS: runtime.GOOS},
		AuthStatus: true,
	}, runner)
	got := providerReportFor(t, report.Providers, ProviderCodex)
	if got.VersionState != "unknown" || got.Version != "" || got.AuthState != "unknown" || got.Reason != "probe_failed" {
		t.Fatalf("probe failure was reported as ready: %+v", got)
	}
	missingRunner := &providerProbeFixture{}
	missing := ProviderReadiness(context.Background(), ProviderReadinessInputs{Discovery: DiscoveryInputs{GOOS: "linux"}}, missingRunner)
	for _, provider := range missing.Providers {
		if provider.InstallState != "missing" || provider.AuthState != "unknown" || provider.VersionState != "unknown" {
			t.Errorf("offline absence not represented as unknown: %+v", provider)
		}
	}
	if len(missingRunner.calls) != 0 {
		t.Fatalf("ran probes without installed providers: %+v", missingRunner.calls)
	}
}

func TestProviderReadinessReportAlwaysSatisfiesItsWireContract(t *testing.T) {
	root := t.TempDir()
	codex := executableFixture(t, filepath.Join(root, "codex", "codex"), 0o755)
	claude := executableFixture(t, filepath.Join(root, "claude", "claude"), 0o755)
	agy := executableFixture(t, filepath.Join(root, "agy", "agy"), 0o755)
	longVersion := "1.2.3-" + strings.Repeat("a", 100)
	runner := &providerProbeFixture{results: map[string]ProviderProbeResult{
		"codex --version":  {Stdout: []byte("codex-cli " + longVersion), ExitCode: 0},
		"claude --version": {Stdout: []byte("2.4.5 (Claude Code)"), ExitCode: 0},
		"agy --version":    {Stdout: []byte("agy 3.4.5"), ExitCode: 0},
	}}
	report := ProviderReadiness(context.Background(), ProviderReadinessInputs{
		Discovery: DiscoveryInputs{Path: strings.Join([]string{filepath.Dir(codex), filepath.Dir(claude), filepath.Dir(agy)}, string(os.PathListSeparator)), GOOS: runtime.GOOS},
	}, runner)
	if codexReport := providerReportFor(t, report.Providers, ProviderCodex); codexReport.VersionState != "unknown" || codexReport.Version != "" {
		t.Fatalf("oversized version was trusted: %+v", codexReport)
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := providerv1.DecodeReport(raw, 16*1024); err != nil {
		t.Fatalf("producer emitted an invalid provider-readiness/v1 report: %v", err)
	}
}

func providerReportFor(t *testing.T, reports []providerv1.ProviderReport, provider Provider) providerv1.ProviderReport {
	t.Helper()
	for _, report := range reports {
		if report.Provider == string(provider) {
			return report
		}
	}
	t.Fatalf("missing report for %s", provider)
	return providerv1.ProviderReport{}
}
