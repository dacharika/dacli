package nativegovernance

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mlnomadpy/dacli/internal/clikit"
)

func TestGuidanceApplyRequiresAuthenticatedOwnerAndExactReviewedPlan(t *testing.T) {
	t.Setenv("HOME", canonicalNativeTestTemp(t, "dacli-guidance-owner-home-"))
	f := setupVerification(t, "", false)
	root := canonicalNativeTestTemp(t, "dacli-guidance-owner-")
	fixture := filepath.Join("..", "..", "guidance", "testdata", "known-legacy")
	if err := filepath.WalkDir(fixture, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(fixture, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(filepath.Join(root, rel), 0o755)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err = os.MkdirAll(filepath.Dir(filepath.Join(root, rel)), 0o755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(root, rel), b, 0o644)
	}); err != nil {
		t.Fatal(err)
	}
	plan := filepath.Join(canonicalNativeTestTemp(t, "dacli-guidance-plan-"), "owner-plan.json")
	t.Cleanup(func() { _ = os.Remove(plan) })
	var out bytes.Buffer
	ctx := &clikit.Ctx{Cwd: f.w.Root, Stdout: &out, Stderr: &out, JSON: true}
	previewArgs := []string{"--skill-root", root, "--plan-out", plan}
	if err := nativeGuidanceRefreshPreview(ctx, previewArgs); clikit.ExitCode(err) != 2 {
		t.Fatalf("preview owner config omission was not a usage refusal: %v", err)
	}
	if err := nativeGuidanceRefreshPreview(ctx, []string{"--config", f.connection, "--skill-root", root, "--plan-out", plan}); clikit.ExitCode(err) != 3 {
		t.Fatalf("worker profile authorized preview: %v", err)
	}
	if _, err := os.Lstat(plan); !os.IsNotExist(err) {
		t.Fatalf("unauthorized preview wrote plan: %v", err)
	}
	if err := nativeGuidanceRefreshPreview(ctx, []string{"--config", f.config, "--skill-root", root, "--plan-out", plan}); err != nil {
		t.Fatalf("authenticated owner preview failed: %v", err)
	}
	if err := nativeGuidanceRefreshApply(ctx, []string{"--plan", plan}); clikit.ExitCode(err) != 2 {
		t.Fatalf("owner config omission was not a usage refusal: %v", err)
	}
	if err := nativeGuidanceRefreshApply(ctx, []string{"--config", f.connection, "--plan", plan}); clikit.ExitCode(err) != 3 {
		t.Fatalf("worker connection authorized owner apply: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".dacli-guidance-backups")); !os.IsNotExist(err) {
		t.Fatalf("unauthorized apply wrote backups: %v", err)
	}
	if err := nativeGuidanceRefreshApply(ctx, []string{"--config", f.config, "--plan", plan}); err != nil {
		t.Fatalf("authenticated owner apply failed: %v; output=%s", err, out.String())
	}
	if !strings.Contains(out.String(), `"state": "completed"`) {
		t.Fatalf("owner apply receipt missing: %s", out.String())
	}
}

func TestGuidancePreviewRefusesConflictingDiscoverableCopy(t *testing.T) {
	f := setupVerification(t, "", false)
	home := canonicalNativeTestTemp(t, "dacli-guidance-conflicting-home-")
	t.Setenv("HOME", home)
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller path unavailable")
	}
	repo := filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", ".."))
	currentSource := filepath.Join(repo, "internal", "guidance", "assets", "current")
	currentTarget := filepath.Join(home, ".codex", "skills", "dacli")
	if err := filepath.WalkDir(currentSource, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(currentSource, path)
		if err != nil {
			return err
		}
		destination := filepath.Join(currentTarget, rel)
		if entry.IsDir() {
			return os.MkdirAll(destination, 0o755)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return err
		}
		return os.WriteFile(destination, b, 0o644)
	}); err != nil {
		t.Fatal(err)
	}
	legacyRoot := canonicalNativeTestTemp(t, "dacli-guidance-conflicting-legacy-")
	fixture := filepath.Join(repo, "internal", "guidance", "testdata", "known-legacy")
	if err := filepath.WalkDir(fixture, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(fixture, path)
		if err != nil {
			return err
		}
		destination := filepath.Join(legacyRoot, rel)
		if entry.IsDir() {
			return os.MkdirAll(destination, 0o755)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return err
		}
		return os.WriteFile(destination, b, 0o644)
	}); err != nil {
		t.Fatal(err)
	}
	plan := filepath.Join(canonicalNativeTestTemp(t, "dacli-guidance-conflicting-plan-"), "plan.json")
	ctx := &clikit.Ctx{Cwd: f.w.Root, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, JSON: true}
	err := nativeGuidanceRefreshPreview(ctx, []string{"--config", f.config, "--skill-root", legacyRoot, "--plan-out", plan})
	if err == nil || clikit.ExitCode(err) != 3 {
		t.Fatalf("conflicting discoverable copies were not refused: %v", err)
	}
	if _, err := os.Lstat(plan); !os.IsNotExist(err) {
		t.Fatalf("conflicting copies produced a plan: %v", err)
	}
}

func canonicalNativeTestTemp(t *testing.T, prefix string) string {
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
