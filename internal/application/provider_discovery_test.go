package application

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNormalizeProviderSupportsDocumentedProductAliases(t *testing.T) {
	for alias, want := range map[string]Provider{
		"codex": ProviderCodex, "codex-cli": ProviderCodex,
		"claude": ProviderClaudeCode, "claude-code": ProviderClaudeCode,
		"agy": ProviderAntigravity, "antigravity": ProviderAntigravity,
	} {
		got, ok := NormalizeProvider(alias)
		if !ok || got != want {
			t.Errorf("NormalizeProvider(%q) = %q, %v; want %q, true", alias, got, ok, want)
		}
	}
	if _, ok := NormalizeProvider("openai"); ok {
		t.Fatal("accepted unsupported provider alias")
	}
}

func TestDiscoverProviderExplicitConfigPrecedesPATH(t *testing.T) {
	root := t.TempDir()
	configured := executableFixture(t, filepath.Join(root, "chosen", "codex"), 0o755)
	pathInstall := executableFixture(t, filepath.Join(root, "bin", "codex"), 0o755)
	discoveries := DiscoverProvider(DiscoveryInputs{
		ExplicitPaths: map[Provider]string{ProviderCodex: configured},
		Path:          filepath.Dir(pathInstall),
		GOOS:          runtime.GOOS,
	})
	got := discoveryFor(t, discoveries, ProviderCodex)
	if got.State != "installed" || got.Source != "explicit_config" || len(got.Candidates) != 1 || got.Candidates[0].Path != configured {
		t.Fatalf("explicit config did not win: %+v", got)
	}
}

func TestDiscoverProviderReportsMultiplePATHInstallsAsAmbiguous(t *testing.T) {
	root := t.TempDir()
	first := executableFixture(t, filepath.Join(root, "one", "claude"), 0o755)
	second := executableFixture(t, filepath.Join(root, "two", "claude"), 0o755)
	got := discoveryFor(t, DiscoverProvider(DiscoveryInputs{Path: strings.Join([]string{filepath.Dir(first), filepath.Dir(second)}, string(os.PathListSeparator)), GOOS: runtime.GOOS}), ProviderClaudeCode)
	if got.State != "ambiguous" || got.Reason != "multiple_candidates" || len(got.Candidates) != 2 {
		t.Fatalf("multiple installs were not ambiguous: %+v", got)
	}
}

func TestDiscoverProviderUsesOnlyDocumentedAntigravityLocationAfterPATH(t *testing.T) {
	root := t.TempDir()
	documented := executableFixture(t, filepath.Join(root, ".local", "bin", "agy"), 0o755)
	got := discoveryFor(t, DiscoverProvider(DiscoveryInputs{Home: root, GOOS: "linux"}), ProviderAntigravity)
	if got.State != "installed" || got.Source != "documented_location" || len(got.Candidates) != 1 || got.Candidates[0].Path != documented {
		t.Fatalf("documented fallback not selected: %+v", got)
	}
}

func TestDiscoverProviderPATHPrecedesDocumentedLocation(t *testing.T) {
	root := t.TempDir()
	pathInstall := executableFixture(t, filepath.Join(root, "path", "agy"), 0o755)
	executableFixture(t, filepath.Join(root, ".local", "bin", "agy"), 0o755)
	got := discoveryFor(t, DiscoverProvider(DiscoveryInputs{Home: root, Path: filepath.Dir(pathInstall), GOOS: "linux"}), ProviderAntigravity)
	selected, err := filepath.EvalSymlinks(got.Candidates[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := filepath.EvalSymlinks(pathInstall)
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != "path" || selected != expected {
		t.Fatalf("PATH did not precede documented fallback: %+v", got)
	}
}

func TestDiscoverProviderResolvesRelativePATHAgainstSuppliedWorkingDirectory(t *testing.T) {
	root := t.TempDir()
	executableFixture(t, filepath.Join(root, "bin", "claude"), 0o755)
	got := discoveryFor(t, DiscoverProvider(DiscoveryInputs{Path: "bin", WorkingDir: root, GOOS: runtime.GOOS}), ProviderClaudeCode)
	if got.State != "installed" || got.Source != "path" {
		t.Fatalf("relative PATH component was not resolved deterministically: %+v", got)
	}
}

func TestDiscoverProviderRefusesUnboundedPATHCandidateScan(t *testing.T) {
	root := t.TempDir()
	entries := make([]string, maxProviderPathEntries+1)
	for i := range entries {
		entries[i] = filepath.Join("bin", string(rune('a'+i%26)), string(rune('A'+i/26)))
	}
	got := discoveryFor(t, DiscoverProvider(DiscoveryInputs{Path: strings.Join(entries, string(os.PathListSeparator)), WorkingDir: root, GOOS: runtime.GOOS}), ProviderCodex)
	if got.State != "denied" || got.Reason != "candidate_limit_exceeded" || len(got.Candidates) != 0 {
		t.Fatalf("unbounded PATH scan was not refused: %+v", got)
	}
}

func TestDiscoverProviderDeniedExplicitAndUnsupportedPlatform(t *testing.T) {
	root := t.TempDir()
	denied := executableFixture(t, filepath.Join(root, "codex"), 0o600)
	got := discoveryFor(t, DiscoverProvider(DiscoveryInputs{ExplicitPaths: map[Provider]string{ProviderCodex: denied}, GOOS: runtime.GOOS}), ProviderCodex)
	if got.State != "denied" || got.Reason != "not_executable" {
		t.Fatalf("non-executable explicit path was not denied: %+v", got)
	}
	unsupported := discoveryFor(t, DiscoverProvider(DiscoveryInputs{GOOS: "plan9"}), ProviderAntigravity)
	if unsupported.State != "unsupported" || len(unsupported.Candidates) != 0 {
		t.Fatalf("unsupported platform should stay unsupported: %+v", unsupported)
	}
}

func TestDiscoverProviderDoesNotSilentlyFallbackAfterInvalidExplicitSelection(t *testing.T) {
	root := t.TempDir()
	pathInstall := executableFixture(t, filepath.Join(root, "bin", "codex"), 0o755)
	got := discoveryFor(t, DiscoverProvider(DiscoveryInputs{ExplicitPaths: map[Provider]string{ProviderCodex: "relative/codex"}, Path: filepath.Dir(pathInstall), GOOS: runtime.GOOS}), ProviderCodex)
	if got.State != "denied" || got.Reason != "invalid_selection" || len(got.Candidates) != 0 {
		t.Fatalf("invalid explicit selection unexpectedly fell back: %+v", got)
	}
}

func executableFixture(t *testing.T, path string, mode os.FileMode) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("fixture"), mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func discoveryFor(t *testing.T, discoveries []ProviderDiscovery, provider Provider) ProviderDiscovery {
	t.Helper()
	for _, discovery := range discoveries {
		if discovery.Provider == provider {
			return discovery
		}
	}
	t.Fatalf("provider %q missing from discovery report", provider)
	return ProviderDiscovery{}
}
