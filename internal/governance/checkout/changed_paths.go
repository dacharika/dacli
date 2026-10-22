package checkout

import (
	"context"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// ChangedPathsRequest is an owner-pinned base and exact committed revision.
// Branch names, remote lookup and automatic merge-base inference are unsupported.
type ChangedPathsRequest struct{ Checkout, Repository, BaseCommit, RevisionCommit, RevisionTree string }

// FileChange preserves path bytes. Renames are represented by both D and A surfaces.
type FileChange struct{ Path, Kind string }

// ChangedPathsObservation is historical clean Git evidence, not an execution permit.
type ChangedPathsObservation struct {
	Binding    ChangedPathsRequest
	Changes    []FileChange
	ObservedAt time.Time
}

func objectID(s string, n int) bool { return len(s) == n && strings.Trim(s, "0123456789abcdef") == "" }

// ObserveChangedPaths uses one bounded context for before/diff/after observations.
// It refuses replacement objects' alternate tree/DAG and all implicit base selection.
func ObserveChangedPaths(parent context.Context, r ChangedPathsRequest) (ChangedPathsObservation, error) {
	if parent == nil {
		return ChangedPathsObservation{}, ErrInvalid
	}
	n := len(r.RevisionCommit)
	if (n != 40 && n != 64) || !objectID(r.BaseCommit, n) || !objectID(r.RevisionCommit, n) || !objectID(r.RevisionTree, n) || !filepath.IsAbs(r.Checkout) || !filepath.IsAbs(r.Repository) {
		return ChangedPathsObservation{}, ErrInvalid
	}
	root, e := filepath.EvalSymlinks(r.Checkout)
	if e != nil || root != r.Checkout {
		return ChangedPathsObservation{}, ErrInvalid
	}
	repo, e := filepath.EvalSymlinks(r.Repository)
	if e != nil || repo != r.Repository {
		return ChangedPathsObservation{}, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	before, e := observeWithContext(ctx, root, false, true)
	if e != nil || before.Repository != repo || before.Commit != r.RevisionCommit || before.Tree != r.RevisionTree {
		return ChangedPathsObservation{}, ErrInvalid
	}
	run := gitRunner(ctx, root, true)
	base, e := run("rev-parse", "--verify", r.BaseCommit+"^{commit}")
	if e != nil || strings.TrimSpace(string(base)) != r.BaseCommit {
		return ChangedPathsObservation{}, ErrInvalid
	}
	if _, e = run("merge-base", "--is-ancestor", r.BaseCommit, r.RevisionCommit); e != nil {
		return ChangedPathsObservation{}, ErrInvalid
	}
	raw, e := run("diff", "--no-ext-diff", "--no-textconv", "--no-renames", "--name-status", "-z", r.BaseCommit, r.RevisionCommit, "--")
	if e != nil {
		return ChangedPathsObservation{}, ErrInvalid
	}
	changes, e := parseChangedPaths(raw)
	if e != nil {
		return ChangedPathsObservation{}, ErrInvalid
	}
	after, e := observeWithContext(ctx, root, false, true)
	if e != nil || after.Repository != repo || after.Commit != before.Commit || after.Tree != before.Tree {
		return ChangedPathsObservation{}, ErrInvalid
	}
	if ctx.Err() != nil {
		return ChangedPathsObservation{}, ErrInvalid
	}
	return ChangedPathsObservation{r, changes, time.Now().UTC()}, nil
}
func parseChangedPaths(raw []byte) ([]FileChange, error) {
	out := []FileChange{}
	if len(raw) == 0 {
		return out, nil
	}
	if len(raw) > 1<<20 || raw[len(raw)-1] != 0 {
		return nil, ErrInvalid
	}
	fields := strings.Split(string(raw[:len(raw)-1]), "\x00")
	if len(fields)%2 != 0 || len(fields)/2 > 4096 {
		return nil, ErrInvalid
	}
	seen := map[string]bool{}
	for i := 0; i < len(fields); i += 2 {
		kind, p := fields[i], fields[i+1]
		if (kind != "A" && kind != "D" && kind != "M" && kind != "T") || p == "" || p == "." || !utf8.ValidString(p) || len(p) > 4096 || path.Clean(p) != p || strings.HasPrefix(p, "/") || p == ".." || strings.HasPrefix(p, "../") || strings.Contains(p, "\\") || seen[p] {
			return nil, ErrInvalid
		}
		for _, r := range p {
			if unicode.IsControl(r) {
				return nil, ErrInvalid
			}
		}
		seen[p] = true
		out = append(out, FileChange{p, kind})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}
