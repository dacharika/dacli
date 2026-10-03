package sessions

import (
	"context"
	"github.com/mlnomadpy/dacli/internal/governance/checkout"
	"github.com/mlnomadpy/dacli/internal/procmon"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ObserveCheckpointTrunk is bounded local read plumbing. It never invokes a
// coding harness, network adapter, alias, filter or hook. Git overrides and
// authority secrets are stripped, and the explicit root is canonically bound.
func ObserveCheckpointTrunk(root, base string) (string, string, error) {
	repo, e := checkout.ObserveRepositoryIdentity(root)
	if e != nil {
		return "", "", ErrDenied
	}
	read := func(args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = repo.Locator
		cmd.WaitDelay = time.Second
		for _, entry := range os.Environ() {
			key, _, _ := strings.Cut(entry, "=")
			upper := strings.ToUpper(key)
			if strings.HasPrefix(upper, "GIT_") || strings.HasPrefix(upper, "DACLI_") || strings.HasPrefix(upper, "MCP_") || strings.Contains(upper, "TOKEN") || strings.Contains(upper, "SECRET") || strings.Contains(upper, "CREDENTIAL") {
				continue
			}
			cmd.Env = append(cmd.Env, entry)
		}
		cmd.Env = append(cmd.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0")
		var output checkpointGitOutput
		cmd.Stdout = &output
		if cmd.Run() != nil || output.overflow {
			return "", ErrStorage
		}
		return strings.TrimSpace(string(output.data)), nil
	}
	if base == "" {
		if head, e := read("symbolic-ref", "--short", "refs/remotes/origin/HEAD"); e == nil {
			base = strings.TrimPrefix(head, "origin/")
		} else {
			for _, candidate := range []string{"main", "master"} {
				if _, e := read("show-ref", "--verify", "refs/heads/"+candidate); e == nil {
					base = candidate
					break
				}
			}
		}
	}
	if base == "" {
		return "", "", ErrDenied
	}
	if _, e := read("check-ref-format", "--branch", base); e != nil {
		return "", "", e
	}
	ref := "refs/heads/" + base
	commit, e := read("rev-parse", "--verify", ref+"^{commit}")
	if e != nil || len(commit) != 40 {
		return "", "", ErrStorage
	}
	return ref, commit, nil
}

type checkpointGitOutput struct {
	data     []byte
	overflow bool
}

func (b *checkpointGitOutput) Write(p []byte) (int, error) {
	n := len(p)
	if len(b.data)+n > 4096 {
		b.overflow = true
		return n, nil
	}
	b.data = append(b.data, p...)
	return n, nil
}

// CheckpointRunTasks reads correlation only; it neither checks process liveness
// nor claims native host authentication. Public projection filters by task IDs.
func CheckpointRunTasks(runs string) (map[string]string, error) {
	result := map[string]string{}
	entries, e := os.ReadDir(runs)
	if os.IsNotExist(e) {
		return result, nil
	}
	if e != nil {
		return nil, ErrStorage
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		record, e := procmon.ReadRecord(filepath.Join(runs, entry.Name(), "proc.txt"))
		if e == nil {
			result[entry.Name()] = record.Task
		}
	}
	return result, nil
}
