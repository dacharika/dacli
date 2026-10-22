package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"time"
)

// NativeIntegrationTarget is owner-selected local Git state, never a remote.
type NativeIntegrationTarget struct {
	Repository, Checkout, Ref, ExpectedCommit, ExpectedTree, Commit, Tree string
}
type NativeIntegrationGitObservation struct {
	Commit, Tree string
	Clean        bool
	ObservedAt   time.Time
}

func nativeIntegrationObject(s string) bool {
	return (len(s) == 40 || len(s) == 64) && strings.Trim(s, "0123456789abcdef") == ""
}
func nativeIntegrationTarget(t NativeIntegrationTarget) error {
	if !filepath.IsAbs(t.Repository) || !filepath.IsAbs(t.Checkout) || !strings.HasPrefix(t.Ref, "refs/heads/") || strings.TrimSpace(t.Ref) != t.Ref || strings.ContainsAny(t.Ref, "\\\x00\r\n") || !nativeIntegrationObject(t.ExpectedCommit) || !nativeIntegrationObject(t.ExpectedTree) {
		return Refusedf("exact native integration target required")
	}
	for _, p := range []string{t.Repository, t.Checkout} {
		real, e := filepath.EvalSymlinks(p)
		if e != nil || real != p || filepath.Clean(p) != p {
			return Refusedf("native integration target alias refused")
		}
	}
	return nil
}
func nativeIntegrationGit(ctx context.Context, dir string, args ...string) (string, error) {
	return runVerificationGit(ctx, dir, append([]string{"--no-replace-objects", "-c", "core.hooksPath=/dev/null"}, args...)...)
}

// WithNativeIntegrationTarget serializes all DACLI local integrations for one
// common repository across linked checkouts and target refs. It grants no writer
// permission and does not claim to control unrelated operating-system processes.
func WithNativeIntegrationTarget(ctx context.Context, t NativeIntegrationTarget, fn func() error) error {
	if ctx == nil || ctx.Err() != nil || fn == nil {
		return Refusedf("native integration context required")
	}
	if e := nativeIntegrationTarget(t); e != nil {
		return e
	}
	_, e := ObserveNativeIntegrationTarget(ctx, t)
	if e != nil {
		return e
	}
	release, e := acquireFileLock(filepath.Join(t.Repository, "dacli-native-integration.lock"))
	if e != nil {
		return e
	}
	defer release()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return fn()
}

// ObserveNativeIntegrationTarget observes genuine objects and the entire clean
// checked-out target with bounded Git output/process lifetimes.
func ObserveNativeIntegrationTarget(parent context.Context, t NativeIntegrationTarget) (NativeIntegrationGitObservation, error) {
	if parent == nil || nativeIntegrationTarget(t) != nil {
		return NativeIntegrationGitObservation{}, Refusedf("native integration target invalid")
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	run := func(args ...string) (string, error) { return nativeIntegrationGit(ctx, t.Checkout, args...) }
	top, e := run("rev-parse", "--show-toplevel")
	if e != nil || strings.TrimSpace(top) != t.Checkout {
		return NativeIntegrationGitObservation{}, Refusedf("native integration whole checkout required")
	}
	common, e := run("rev-parse", "--path-format=absolute", "--git-common-dir")
	if e != nil {
		return NativeIntegrationGitObservation{}, Refusedf("native integration repository unavailable")
	}
	actual, e := filepath.EvalSymlinks(strings.TrimSpace(common))
	if e != nil || actual != t.Repository {
		return NativeIntegrationGitObservation{}, Refusedf("native integration repository mismatch")
	}
	if _, e = run("check-ref-format", t.Ref); e != nil {
		return NativeIntegrationGitObservation{}, Refusedf("native integration target ref invalid")
	}
	ref, e := run("symbolic-ref", "--quiet", "HEAD")
	if e != nil || strings.TrimSpace(ref) != t.Ref {
		return NativeIntegrationGitObservation{}, Refusedf("native integration target branch mismatch")
	}
	keys, e := run("config", "--null", "--name-only", "--list")
	if e != nil {
		return NativeIntegrationGitObservation{}, Refusedf("native integration Git config unavailable")
	}
	for _, key := range strings.Split(keys, "\x00") {
		key = strings.ToLower(key)
		if strings.HasPrefix(key, "filter.") && (strings.HasSuffix(key, ".clean") || strings.HasSuffix(key, ".process")) {
			return NativeIntegrationGitObservation{}, Refusedf("native integration executable filter refused")
		}
	}
	entries, e := run("ls-files", "--stage")
	if e != nil {
		return NativeIntegrationGitObservation{}, Refusedf("native integration index unavailable")
	}
	for _, line := range strings.Split(entries, "\n") {
		if strings.HasPrefix(line, "160000 ") {
			return NativeIntegrationGitObservation{}, Refusedf("native integration submodule unsupported")
		}
	}
	commit, e := run("rev-parse", "--verify", t.Ref+"^{commit}")
	if e != nil {
		return NativeIntegrationGitObservation{}, Refusedf("native integration target commit unavailable")
	}
	tree, e := run("rev-parse", "--verify", t.Ref+"^{tree}")
	if e != nil {
		return NativeIntegrationGitObservation{}, Refusedf("native integration target tree unavailable")
	}
	status, e := run("status", "--porcelain=v1", "--untracked-files=all", "--ignore-submodules=none")
	if e != nil {
		return NativeIntegrationGitObservation{}, Refusedf("native integration target status unavailable")
	}
	again, e := run("rev-parse", "--verify", t.Ref+"^{commit}")
	if e != nil || again != commit {
		return NativeIntegrationGitObservation{}, Refusedf("native integration target changed during observation")
	}
	head, e := run("symbolic-ref", "--quiet", "HEAD")
	if e != nil || head != ref {
		return NativeIntegrationGitObservation{}, Refusedf("native integration target branch changed during observation")
	}
	return NativeIntegrationGitObservation{Commit: strings.TrimSpace(commit), Tree: strings.TrimSpace(tree), Clean: strings.TrimSpace(status) == "", ObservedAt: time.Now().UTC()}, nil
}

// ApplyNativeIntegrationFastForward uses an explicit ref compare-and-swap, then
// a safe two-tree index/worktree transition. A crash between them is unknown;
// callers must not rerun it. No commit, remote, hook, deployment or release runs.
func ApplyNativeIntegrationFastForward(parent context.Context, t NativeIntegrationTarget, finalCurrent func() error) (NativeIntegrationGitObservation, error) {
	return ApplyNativeIntegrationFastForwardWithRefObserver(parent, t, finalCurrent, nil)
}

// ApplyNativeIntegrationFastForwardWithRefObserver is the producer variant used
// to observe the boundary after the durable ref CAS and before the worktree
// update. The callback cannot authorize or alter the Git command; production
// callers pass nil. It exists so process-death recovery tests can exercise the
// real split side effect instead of simulating an Authority callback.
func ApplyNativeIntegrationFastForwardWithRefObserver(parent context.Context, t NativeIntegrationTarget, finalCurrent func() error, afterRefCAS func()) (NativeIntegrationGitObservation, error) {
	if parent == nil || finalCurrent == nil || !nativeIntegrationObject(t.Commit) || !nativeIntegrationObject(t.Tree) {
		return NativeIntegrationGitObservation{}, Refusedf("reviewed native integration revision required")
	}
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	before, e := ObserveNativeIntegrationTarget(ctx, t)
	if e != nil || !before.Clean || before.Commit != t.ExpectedCommit || before.Tree != t.ExpectedTree {
		return NativeIntegrationGitObservation{}, Refusedf("native integration trunk changed; repin and verify/review resulting tree")
	}
	run := func(args ...string) (string, error) { return nativeIntegrationGit(ctx, t.Checkout, args...) }
	commit, e := run("rev-parse", "--verify", t.Commit+"^{commit}")
	if e != nil || strings.TrimSpace(commit) != t.Commit {
		return NativeIntegrationGitObservation{}, Refusedf("native integration genuine commit mismatch")
	}
	tree, e := run("rev-parse", "--verify", t.Commit+"^{tree}")
	if e != nil || strings.TrimSpace(tree) != t.Tree {
		return NativeIntegrationGitObservation{}, Refusedf("native integration genuine tree mismatch")
	}
	if _, e = run("merge-base", "--is-ancestor", t.ExpectedCommit, t.Commit); e != nil {
		return NativeIntegrationGitObservation{}, Refusedf("native integration diverged; resulting tree needs fresh verification and review")
	}
	if e = finalCurrent(); e != nil {
		return NativeIntegrationGitObservation{}, e
	}
	if _, e = run("update-ref", "--no-deref", t.Ref, t.Commit, t.ExpectedCommit); e != nil {
		return NativeIntegrationGitObservation{}, errors.Join(Refusedf("native integration ref CAS refused"), e)
	}
	if afterRefCAS != nil {
		afterRefCAS()
	}
	if _, e = run("read-tree", "-u", "-m", t.ExpectedCommit, t.Commit); e != nil {
		return NativeIntegrationGitObservation{}, errors.Join(Refusedf("native integration partial effect; owner reconciliation required"), e)
	}
	after, e := ObserveNativeIntegrationTarget(ctx, t)
	if e != nil || !after.Clean || after.Commit != t.Commit || after.Tree != t.Tree {
		return NativeIntegrationGitObservation{}, Refusedf("native integration outcome unknown; owner reconciliation required")
	}
	return after, nil
}
