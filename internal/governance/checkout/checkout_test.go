package checkout

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestExplicitCleanCommittedCheckout(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v", out, err)
		}
	}
	if _, err := Observe("."); !errors.Is(err, ErrInvalid) {
		t.Fatal("ambient relative path accepted")
	}
	run("init")
	if _, err := Observe(dir); !errors.Is(err, ErrInvalid) {
		t.Fatal("unborn HEAD accepted")
	}
	if err := os.WriteFile(filepath.Join(dir, "file"), []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	run("add", "file")
	run("-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-m", "initial")
	obs, err := Observe(dir)
	if err != nil || obs.Commit == "" || obs.Tree == "" || obs.ObservedAt.IsZero() {
		t.Fatalf("observation: %+v %v", obs, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "file"), []byte("modified"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = Observe(dir); !errors.Is(err, ErrInvalid) {
		t.Fatal("tracked dirty accepted")
	}
	run("add", "file")
	if _, err = Observe(dir); !errors.Is(err, ErrInvalid) {
		t.Fatal("dirty index accepted")
	}
	run("reset", "--hard", "HEAD")
	run("symbolic-ref", "HEAD", "refs/heads/missing")
	if _, err = Observe(dir); !errors.Is(err, ErrInvalid) {
		t.Fatal("unresolved HEAD accepted")
	}
}
