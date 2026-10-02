package checkout

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func reviewGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if b, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("git %v: %v %s", args, e, b)
	}
}
func reviewRepo(t *testing.T) string {
	t.Helper()
	r := t.TempDir()
	reviewGit(t, r, "init")
	reviewGit(t, r, "config", "user.name", "Fixture")
	reviewGit(t, r, "config", "user.email", "fixture@example.invalid")
	if e := os.WriteFile(filepath.Join(r, "public.txt"), []byte("public fixture\n"), 0600); e != nil {
		t.Fatal(e)
	}
	reviewGit(t, r, "add", ".")
	reviewGit(t, r, "commit", "-m", "fixture")
	return r
}
func TestReviewObserverDoesNotRunFSMonitor(t *testing.T) {
	r := reviewRepo(t)
	d := t.TempDir()
	marker := filepath.Join(d, "public-marker")
	script := filepath.Join(d, "monitor")
	if e := os.WriteFile(script, []byte("#!/bin/sh\nprintf public > '"+marker+"'\nexit 1\n"), 0700); e != nil {
		t.Fatal(e)
	}
	reviewGit(t, r, "config", "core.fsmonitor", script)
	_, err := Observe(r)
	t.Logf("observation error=%v", err)
	if _, e := os.Stat(marker); e == nil {
		t.Fatalf("observational Git ran local fsmonitor hook; observation error=%v", err)
	}
}
func TestReviewObserverBoundsInheritedOutputPipe(t *testing.T) {
	r := reviewRepo(t)
	realGit, e := exec.LookPath("git")
	if e != nil {
		t.Fatal(e)
	}
	d := t.TempDir()
	script := filepath.Join(d, "git")
	body := "#!/bin/sh\nfor arg do\nif [ \"$arg\" = status ]; then\n(sleep 7) &\nfi\ndone\nexec '" + realGit + "' \"$@\"\n"
	if e := os.WriteFile(script, []byte(body), 0700); e != nil {
		t.Fatal(e)
	}
	t.Setenv("PATH", d+string(os.PathListSeparator)+os.Getenv("PATH"))
	start := time.Now()
	_, err := Observe(r)
	elapsed := time.Since(start)
	if elapsed > 6*time.Second {
		t.Fatalf("five-second observation waited %v for descendant stdout; error=%v", elapsed, err)
	}
}
func TestReviewObserverDoesNotRunCleanFilter(t *testing.T) {
	r := reviewRepo(t)
	d := t.TempDir()
	marker := filepath.Join(d, "public-marker")
	script := filepath.Join(d, "clean")
	if e := os.WriteFile(script, []byte("#!/bin/sh\nprintf public > '"+marker+"'\ncat\n"), 0700); e != nil {
		t.Fatal(e)
	}
	reviewGit(t, r, "config", "filter.fixture.clean", script)
	if e := os.WriteFile(filepath.Join(r, ".gitattributes"), []byte("public.txt filter=fixture\n"), 0600); e != nil {
		t.Fatal(e)
	}
	reviewGit(t, r, "add", ".")
	reviewGit(t, r, "commit", "-m", "filter fixture")
	_ = os.Remove(marker)
	if e := os.WriteFile(filepath.Join(r, "public.txt"), []byte("public fixture\n"), 0600); e != nil {
		t.Fatal(e)
	}
	_, err := Observe(r)
	if _, e := os.Stat(marker); e == nil {
		t.Fatalf("observational Git ran local clean filter; observation error=%v", err)
	}
}

func TestObserverRefusesIncludedProcessFilter(t *testing.T) {
	r := reviewRepo(t)
	config := filepath.Join(t.TempDir(), "included-config")
	if err := os.WriteFile(config, []byte("[filter \"fixture\"]\nprocess = public-unavailable-command\n"), 0600); err != nil {
		t.Fatal(err)
	}
	reviewGit(t, r, "config", "include.path", config)
	if _, err := Observe(r); err == nil {
		t.Fatal("included executable filter accepted")
	}
}
func TestObserverRefusesIndexedSubmodule(t *testing.T) {
	r := reviewRepo(t)
	nested := reviewRepo(t)
	reviewGit(t, r, "-c", "protocol.file.allow=always", "submodule", "add", nested, "nested")
	reviewGit(t, r, "commit", "-m", "clean initialized gitlink")
	if _, err := Observe(r); err == nil {
		t.Fatal("indexed submodule accepted")
	}
}
