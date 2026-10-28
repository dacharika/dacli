package nativegovernance

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/mlnomadpy/dacli/internal/clikit"
)

func TestGuidancePredecessorOwnerHandlers(t *testing.T) {
	t.Setenv("HOME", canonicalNativeTestTemp(t, "guidance-predecessor-home-"))
	t.Setenv("DACLI_AGENT", "poisoned-worker-identity")
	f := setupVerification(t, "", false)
	fixture := filepath.Join("..", "..", "guidance", "testdata", "previous-current-ce282fcb.json")
	var data struct {
		Files map[string]string `json:"files"`
	}
	raw, e := os.ReadFile(fixture)
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(raw, &data); e != nil {
		t.Fatal(e)
	}
	root := canonicalNativeTestTemp(t, "guidance-predecessor-root-")
	for name, body := range data.Files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if e = os.MkdirAll(filepath.Dir(path), 0755); e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(path, []byte(body), 0644); e != nil {
			t.Fatal(e)
		}
	}
	plan := filepath.Join(canonicalNativeTestTemp(t, "guidance-predecessor-plan-"), "plan.json")
	configBytes, e := os.ReadFile(f.config)
	if e != nil {
		t.Fatal(e)
	}
	var cfg ownerConfig
	if e = json.Unmarshal(configBytes, &cfg); e != nil {
		t.Fatal(e)
	}
	before, e := os.ReadFile(cfg.Authority)
	if e != nil {
		t.Fatal(e)
	}
	authorityDigest := sha256.Sum256(before)
	ctx := &clikit.Ctx{Cwd: f.w.Root, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, JSON: true}
	workerPlan := filepath.Join(canonicalNativeTestTemp(t, "guidance-predecessor-worker-plan-"), "plan.json")
	if e = nativeGuidanceRefreshPreview(ctx, []string{"--config", f.connection, "--skill-root", root, "--plan-out", workerPlan}); clikit.ExitCode(e) != 3 {
		t.Fatalf("worker profile authorized predecessor preview: %v", e)
	}
	if _, e = os.Lstat(workerPlan); !os.IsNotExist(e) {
		t.Fatalf("worker profile wrote plan: %v", e)
	}
	if e = nativeGuidanceRefreshPreview(ctx, []string{"--config", f.config, "--skill-root", root, "--plan-out", plan}); e != nil {
		t.Fatalf("owner preview: %v", e)
	}
	var p struct {
		Schema string `json:"schema"`
		Before struct {
			Identity string `json:"identity"`
		} `json:"before_bundle"`
	}
	raw, e = os.ReadFile(plan)
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(raw, &p); e != nil {
		t.Fatal(e)
	}
	if p.Schema != "dacli-guidance-plan/v2" || p.Before.Identity != "embedded-reviewed-dacli-skill/ce282fcb" {
		t.Fatalf("unexpected plan binding %+v", p)
	}
	if e = nativeGuidanceRefreshApply(ctx, []string{"--config", f.config, "--plan", plan}); e != nil {
		t.Fatalf("owner apply: %v", e)
	}
	after, e := os.ReadFile(cfg.Authority)
	if e != nil || sha256.Sum256(after) != authorityDigest {
		t.Fatalf("owner authority changed: %v", e)
	}
}

func TestGuidancePlanOwnerRefusesAuthorityParentAliasRetarget(t *testing.T) {
	f := setupVerification(t, "", false)
	var cfg ownerConfig
	raw, e := os.ReadFile(f.config)
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(raw, &cfg); e != nil {
		t.Fatal(e)
	}
	realParent := filepath.Dir(cfg.Authority)
	aliasBase := canonicalNativeTestTemp(t, "guidance-authority-alias-")
	alias := filepath.Join(aliasBase, "authority-dir")
	if e = os.Symlink(realParent, alias); e != nil {
		t.Fatal(e)
	}
	cfg.Authority = filepath.Join(alias, filepath.Base(cfg.Authority))
	verificationJSON(t, f.config, cfg)
	owner, e := guidancePlanOwner(f.config)
	if e != nil {
		t.Fatal(e)
	}
	canonicalParent, e := filepath.EvalSymlinks(realParent)
	if e != nil {
		t.Fatal(e)
	}
	if owner.Binding.AuthorityPath != filepath.Join(canonicalParent, filepath.Base(cfg.Authority)) {
		t.Fatalf("authority path was not canonicalized: %s", owner.Binding.AuthorityPath)
	}
	original, e := os.ReadFile(cfg.Authority)
	if e != nil {
		t.Fatal(e)
	}
	copyDir := canonicalNativeTestTemp(t, "guidance-authority-copy-")
	copyPath := filepath.Join(copyDir, filepath.Base(cfg.Authority))
	if e = os.WriteFile(copyPath, original, 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.Remove(alias); e != nil {
		t.Fatal(e)
	}
	if e = os.Symlink(copyDir, alias); e != nil {
		t.Fatal(e)
	}
	if e = owner.Authorize(); e == nil {
		t.Fatal("owner authorization accepted a retargeted authority parent alias")
	}
}
