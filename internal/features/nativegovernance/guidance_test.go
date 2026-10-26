package nativegovernance

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/guidance"
)

func TestGuidanceApplyRequiresAuthenticatedOwnerAndExactReviewedPlan(t *testing.T) {
	f := setupVerification(t, "", false)
	root, err := os.MkdirTemp("/private/tmp", "dacli-guidance-owner-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
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
	p, err := guidance.Preview(root, guidance.ReviewedSourceID)
	if err != nil {
		t.Fatal(err)
	}
	plan := filepath.Join("/private/tmp", "dacli-guidance-owner-"+p.PlanDigest+".json")
	if err := guidance.WritePlan(plan, p); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(plan) })
	var out bytes.Buffer
	ctx := &clikit.Ctx{Cwd: f.w.Root, Stdout: &out, Stderr: &out, JSON: true}
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
