package history

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mlnomadpy/dacli/internal/agentid"
	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

func init() {
	Commands = append(Commands,
		clikit.Command{Path: "handoff show", Brief: "Show a structured worker-to-root lifecycle handoff", JSON: true, Usage: "dacli handoff show <run-id>", Run: cmdHandoffShow},
		clikit.Command{Path: "handoff consume", Brief: "Root re-observes exact worktree hashes and acknowledges a lifecycle handoff", JSON: true, Mutates: true, Usage: "dacli handoff consume <run-id>", Run: cmdHandoffConsume},
	)
}

func cmdHandoffShow(ctx *clikit.Ctx, args []string) error {
	w, _, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	f, err := clikit.ParseFlags(args)
	if err != nil {
		return err
	}
	if err := f.Reject(); err != nil {
		return err
	}
	if len(f.Pos) != 1 {
		return clikit.Usagef("usage: dacli handoff show <run-id>")
	}
	runID := f.Pos[0]
	if rec, ok := readProcByRef(w, f.Pos[0]); ok {
		runID = rec.RunID
	}
	h, err := store.LoadRootHandoff(w, runID)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return store.ErrNotFound{Ref: "root handoff " + f.Pos[0]}
		}
		return err
	}
	if ctx.JSON {
		enc := json.NewEncoder(ctx.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(h)
	}
	printRootHandoff(ctx, h)
	return nil
}

func cmdHandoffConsume(ctx *clikit.Ctx, args []string) error {
	w, id, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	if id.ID != agentid.RootID || id.Grant != model.GrantRW {
		return clikit.Refusedf("handoff consumption is an owner-only re-observation step; current identity is %s (%s)", id.ID, id.Grant)
	}
	f, err := clikit.ParseFlags(args)
	if err != nil {
		return err
	}
	if err := f.Reject(); err != nil {
		return err
	}
	if len(f.Pos) != 1 {
		return clikit.Usagef("usage: dacli handoff consume <run-id>")
	}
	runID := f.Pos[0]
	if rec, ok := readProcByRef(w, runID); ok {
		runID = rec.RunID
	}
	h, err := store.LoadRootHandoff(w, runID)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return store.ErrNotFound{Ref: "root handoff " + f.Pos[0]}
		}
		return err
	}
	if err := store.MarkRootHandoffConsumed(w, h, id.ID, time.Now()); err != nil {
		return clikit.Refusedf("cannot consume handoff: %v", err)
	}
	if ctx.JSON {
		return clikit.EmitJSON(ctx, struct {
			Schema     string `json:"schema"`
			RunID      string `json:"run_id"`
			TreeSHA256 string `json:"tree_sha256"`
			NextAction string `json:"safe_owner_next_action"`
		}{"root-handoff-consumption/v1", h.RunID, h.TreeSHA256, h.NextAction})
	}
	fmt.Fprintf(ctx.Stdout, "consumed handoff %s after re-observing %d path(s) and tree %s\nnext: %s\n", h.RunID, len(h.ChangedPaths), h.TreeSHA256, h.NextAction)
	return nil
}

func printRootHandoff(ctx *clikit.Ctx, h store.RootHandoff) {
	fmt.Fprintf(ctx.Stdout, "handoff-required · run %s · task %s · child %s\n", h.RunID, h.TaskID, h.ChildID)
	fmt.Fprintf(ctx.Stdout, "failure: %s (%s)\n", h.FailedOperation, h.FailureClass)
	for _, path := range h.ChangedPaths {
		fmt.Fprintf(ctx.Stdout, "  %s  %s\n", path.SHA256, path.Path)
	}
	for _, check := range h.Verification {
		fmt.Fprintf(ctx.Stdout, "verify exit %d: %s", check.ExitCode, check.Command)
		if strings.TrimSpace(check.Result) != "" {
			fmt.Fprintf(ctx.Stdout, " — %s", strings.TrimSpace(check.Result))
		}
		fmt.Fprintln(ctx.Stdout)
	}
	fmt.Fprintf(ctx.Stdout, "next: %s\n", h.NextAction)
}

func pendingRootHandoffs(w *workspace.Workspace) []store.RootHandoff {
	entries, err := os.ReadDir(w.RunsDir())
	if err != nil {
		return nil
	}
	var out []store.RootHandoff
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		h, err := store.LoadRootHandoff(w, entry.Name())
		if err != nil {
			continue
		}
		if _, err := os.Stat(filepath.Join(w.RunDir(entry.Name()), store.RootHandoffConsumedFile)); err == nil {
			continue
		}
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RunID < out[j].RunID })
	return out
}
