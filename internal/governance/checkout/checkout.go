// Package checkout observes explicit local Git checkouts without granting writes.
package checkout

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

var ErrInvalid = errors.New("checkout observation refused")

type Observation struct {
	Locator    string
	Repository string
	Commit     string
	Tree       string
	ObservedAt time.Time
}

// Observe supports clean committed worktrees only. Repository identity is the
// canonical local common Git directory, not a remote URL or host identity.
// This historical observation cannot freeze bytes against concurrent writers.
func Observe(locator string) (Observation, error) {
	if !filepath.IsAbs(locator) {
		return Observation{}, ErrInvalid
	}
	root, err := filepath.EvalSymlinks(locator)
	if err != nil {
		return Observation{}, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	run := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", append([]string{"--no-optional-locks", "-c", "core.fsmonitor=false", "-C", root}, args...)...)
		cmd.WaitDelay = 250 * time.Millisecond
		for _, e := range os.Environ() {
			if !strings.HasPrefix(e, "GIT_") {
				cmd.Env = append(cmd.Env, e)
			}
		}
		cmd.Env = append(cmd.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0")
		var out boundedBuffer
		cmd.Stdout = &out
		if cmd.Run() != nil {
			return "", ErrInvalid
		}
		return strings.TrimSpace(out.String()), nil
	}
	top, err := run("rev-parse", "--show-toplevel")
	if err != nil {
		return Observation{}, ErrInvalid
	}
	top, err = filepath.EvalSymlinks(top)
	if err != nil || top != root {
		return Observation{}, ErrInvalid
	}
	common, err := run("rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return Observation{}, ErrInvalid
	}
	common, err = filepath.EvalSymlinks(common)
	if err != nil {
		return Observation{}, ErrInvalid
	}
	commit, err := run("rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return Observation{}, ErrInvalid
	}
	tree, err := run("rev-parse", "--verify", "HEAD^{tree}")
	if err != nil {
		return Observation{}, ErrInvalid
	}
	// Status may invoke clean/process filters, including included and worktree
	// configuration. Refuse these repositories before inspecting worktree bytes.
	keys, err := run("config", "--null", "--name-only", "--list")
	if err != nil {
		return Observation{}, ErrInvalid
	}
	for _, key := range strings.Split(keys, "\x00") {
		key = strings.ToLower(key)
		if strings.HasPrefix(key, "filter.") && (strings.HasSuffix(key, ".clean") || strings.HasSuffix(key, ".process")) {
			return Observation{}, ErrInvalid
		}
	}
	// A nested repository has its own executable configuration. Initial support
	// excludes gitlinks rather than recursing into an uninspected submodule.
	entries, err := run("ls-files", "--stage")
	if err != nil {
		return Observation{}, ErrInvalid
	}
	for _, entry := range strings.Split(entries, "\n") {
		if strings.HasPrefix(entry, "160000 ") {
			return Observation{}, ErrInvalid
		}
	}
	dirty, err := run("status", "--porcelain=v1", "--untracked-files=all", "--ignore-submodules=none")
	if err != nil || dirty != "" {
		return Observation{}, ErrInvalid
	}
	again, err := run("rev-parse", "--verify", "HEAD^{commit}")
	if err != nil || again != commit {
		return Observation{}, ErrInvalid
	}
	return Observation{root, common, commit, tree, time.Now().UTC()}, nil
}

type boundedBuffer struct{ bytes.Buffer }

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 1024*1024 {
		return 0, ErrInvalid
	}
	return b.Buffer.Write(p)
}
