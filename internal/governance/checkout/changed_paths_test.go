package checkout

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func changeGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, e := cmd.CombinedOutput()
	if e != nil {
		t.Fatalf("git %v: %v %s", args, e, out)
	}
	return strings.TrimSpace(string(out))
}
func changeWrite(t *testing.T, dir, name, body string) {
	t.Helper()
	if e := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); e != nil {
		t.Fatal(e)
	}
}
func changeCommit(t *testing.T, dir string) {
	t.Helper()
	changeGit(t, dir, "add", ".")
	changeGit(t, dir, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-m", "fixture")
}
func changeFixture(t *testing.T) (string, ChangedPathsRequest) {
	t.Helper()
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	changeGit(t, dir, "init")
	changeWrite(t, dir, "old name.go", "original")
	changeWrite(t, dir, "deleted.py", "old")
	changeWrite(t, dir, "same.go", "before")
	changeCommit(t, dir)
	base := changeGit(t, dir, "rev-parse", "HEAD")
	changeGit(t, dir, "mv", "old name.go", "new name.go")
	changeGit(t, dir, "rm", "deleted.py")
	changeWrite(t, dir, "same.go", "after")
	changeWrite(t, dir, "nested web/new file.vue", "web")
	changeCommit(t, dir)
	obs, e := Observe(dir)
	if e != nil {
		t.Fatal(e)
	}
	return dir, ChangedPathsRequest{dir, obs.Repository, base, obs.Commit, obs.Tree}
}
func TestPinnedChangedPathsRenameDeletionSpaces(t *testing.T) {
	_, req := changeFixture(t)
	obs, e := ObserveChangedPaths(context.Background(), req)
	want := []FileChange{{"deleted.py", "D"}, {"nested web/new file.vue", "A"}, {"new name.go", "A"}, {"old name.go", "D"}, {"same.go", "M"}}
	if e != nil || !reflect.DeepEqual(obs.Changes, want) || obs.Binding != req || obs.ObservedAt.IsZero() {
		t.Fatalf("pinned full changed surfaces mismatch: %+v %v", obs, e)
	}
	req.BaseCommit = req.RevisionCommit
	empty, e := ObserveChangedPaths(context.Background(), req)
	if e != nil || len(empty.Changes) != 0 {
		t.Fatal("empty committed diff invented changed paths")
	}
}
func TestChangedPathsScrubsGitOverrides(t *testing.T) {
	_, req := changeFixture(t)
	other := t.TempDir()
	changeGit(t, other, "init")
	changeWrite(t, other, "unrelated", "other")
	changeCommit(t, other)
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_WORK_TREE", other)
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.worktree")
	t.Setenv("GIT_CONFIG_VALUE_0", other)
	obs, e := ObserveChangedPaths(context.Background(), req)
	if e != nil || obs.Binding.Repository != req.Repository || len(obs.Changes) != 5 {
		t.Fatalf("ambient Git override changed pinned observation: %+v %v", obs, e)
	}
}
func TestChangedPathsBindingAndCleanlinessRefuse(t *testing.T) {
	dir, req := changeFixture(t)
	for _, name := range []string{"base-name", "unknown-base", "repository", "commit", "tree", "relative"} {
		t.Run(name, func(t *testing.T) {
			r := req
			switch name {
			case "base-name":
				r.BaseCommit = "main"
			case "unknown-base":
				r.BaseCommit = strings.Repeat("f", 40)
			case "repository":
				r.Repository = dir
			case "commit":
				r.RevisionCommit = r.BaseCommit
			case "tree":
				r.RevisionTree = strings.Repeat("1", 40)
			case "relative":
				r.Checkout = "."
			}
			got, e := ObserveChangedPaths(context.Background(), r)
			if !errors.Is(e, ErrInvalid) || !reflect.DeepEqual(got, ChangedPathsObservation{}) {
				t.Fatalf("incorrect binding returned changed evidence: %+v %v", got, e)
			}
		})
	}
	changeWrite(t, dir, "same.go", "dirty")
	if _, e := ObserveChangedPaths(context.Background(), req); !errors.Is(e, ErrInvalid) {
		t.Fatal("dirty checkout yielded routed paths")
	}
}
func TestChangedPathsGenuineTreeRejectsReplacement(t *testing.T) {
	dir, req := changeFixture(t)
	changeWrite(t, dir, "same.go", "replacement tree")
	changeCommit(t, dir)
	replacementTree := changeGit(t, dir, "rev-parse", "HEAD^{tree}")
	// A sibling replacement preserves genuine base ancestry without a replacement cycle.
	replacement := changeGit(t, dir, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit-tree", replacementTree, "-p", req.BaseCommit, "-m", "sibling replacement")
	changeGit(t, dir, "reset", "--hard", req.RevisionCommit)
	changeGit(t, dir, "replace", req.RevisionCommit, replacement)
	changeGit(t, dir, "reset", "--hard", req.RevisionCommit)
	alternate := changeGit(t, dir, "rev-parse", req.RevisionCommit+"^{tree}")
	if alternate == req.RevisionTree {
		t.Fatal("replacement premise did not alter tree")
	}
	req.RevisionTree = alternate
	if got, e := ObserveChangedPaths(context.Background(), req); !errors.Is(e, ErrInvalid) || got.Binding.Checkout != "" {
		t.Fatalf("replacement tree accepted as genuine pinned revision: %+v %v", got, e)
	}
}
func TestChangedPathsUnrelatedBaseAndCancellation(t *testing.T) {
	dir, req := changeFixture(t)
	changeGit(t, dir, "checkout", "--orphan", "unrelated")
	changeGit(t, dir, "rm", "-rf", ".")
	changeWrite(t, dir, "unrelated", "root")
	changeCommit(t, dir)
	unrelated := changeGit(t, dir, "rev-parse", "HEAD")
	changeGit(t, dir, "checkout", "--detach", req.RevisionCommit)
	req.BaseCommit = unrelated
	if _, e := ObserveChangedPaths(context.Background(), req); !errors.Is(e, ErrInvalid) {
		t.Fatal("unrelated pinned base accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if _, e := ObserveChangedPaths(ctx, req); !errors.Is(e, ErrInvalid) || time.Since(start) > time.Second {
		t.Fatal("cancelled observation executed or exceeded bound")
	}
}
func TestChangedPathsParserRejectsIncompleteOrUnsafeEvidence(t *testing.T) {
	for _, raw := range [][]byte{[]byte("M\x00no terminator"), []byte("R100\x00a\x00b\x00"), []byte("A\x00../x\x00"), []byte("A\x00x\x00D\x00x\x00"), []byte("M\x00line\nfile\x00"), []byte(strings.Repeat("x", (1<<20)+1))} {
		if _, e := parseChangedPaths(raw); !errors.Is(e, ErrInvalid) {
			t.Fatalf("malformed changed path evidence accepted: %q", raw[:min(len(raw), 40)])
		}
	}
}
