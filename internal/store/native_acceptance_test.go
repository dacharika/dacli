package store

import (
	"github.com/mlnomadpy/dacli/internal/governance/checkout"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func nativeLandingGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = VerificationEnvironment()
	b, e := c.CombinedOutput()
	if e != nil {
		t.Fatalf("git %v: %s %v", args, b, e)
	}
	return strings.TrimSpace(string(b))
}
func nativeLandingFixture(t *testing.T) (string, checkout.Observation) {
	t.Helper()
	dir := t.TempDir()
	nativeLandingGit(t, dir, "init", "-b", "main")
	nativeLandingGit(t, dir, "-c", "user.name=f", "-c", "user.email=f@example.invalid", "commit", "--allow-empty", "-m", "base")
	obs, e := checkout.Observe(dir)
	if e != nil {
		t.Fatal(e)
	}
	return dir, obs
}
func TestNativeAcceptanceLandingRejectsAmbientOverrideAndReplacement(t *testing.T) {
	dir, base := nativeLandingFixture(t)
	nativeLandingGit(t, dir, "checkout", "-b", "work")
	if e := os.WriteFile(filepath.Join(dir, "work"), []byte("work"), 0600); e != nil {
		t.Fatal(e)
	}
	nativeLandingGit(t, dir, "add", "work")
	nativeLandingGit(t, dir, "-c", "user.name=f", "-c", "user.email=f@example.invalid", "commit", "-m", "work")
	work, e := checkout.Observe(dir)
	if e != nil {
		t.Fatal(e)
	}
	if e = nativeAcceptanceLanded(dir, base.Repository, work.Commit, work.Tree, "main"); e == nil {
		t.Fatal("unlanded native reviewed commit authorized close")
	}
	alternate := t.TempDir()
	nativeLandingGit(t, alternate, "clone", "-b", "work", dir, ".")
	nativeLandingGit(t, alternate, "branch", "main", work.Commit)
	t.Setenv("GIT_DIR", filepath.Join(alternate, ".git"))
	if e = nativeAcceptanceLanded(dir, base.Repository, work.Commit, work.Tree, "main"); e == nil {
		t.Fatal("ambient Git override authorized unlanded native close")
	}
	t.Setenv("GIT_DIR", "")
	nativeLandingGit(t, dir, "replace", "--graft", base.Commit, work.Commit)
	if e = nativeAcceptanceLanded(dir, base.Repository, work.Commit, work.Tree, "main"); e == nil {
		t.Fatal("replacement ancestry authorized unlanded native close")
	}
	nativeLandingGit(t, dir, "replace", "-d", base.Commit)
	nativeLandingGit(t, dir, "replace", base.Commit, work.Commit)
	if e = nativeAcceptanceLanded(dir, base.Repository, base.Commit, work.Tree, "main"); e == nil {
		t.Fatal("replacement tree authorized non-genuine native landed tree")
	}
	nativeLandingGit(t, dir, "replace", "-d", base.Commit)
	if e = nativeAcceptanceLanded(dir, base.Repository, base.Commit, base.Tree, "main"); e != nil {
		t.Fatalf("genuine exact landed tree refused: %v", e)
	}
}
