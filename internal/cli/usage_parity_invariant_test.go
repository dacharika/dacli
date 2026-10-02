package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/mlnomadpy/dacli/internal/agentid"
	"github.com/mlnomadpy/dacli/internal/mcp"
)

// handlerUsageVariants are intentional table extensions: a missing positional
// cannot exercise their optional flags or explanatory continuation. Keep each
// exception here rather than weakening the invariant for every command.
var handlerUsageVariants = map[string]string{
	"new":               "dacli new \"<product name>\" --goal \"<what it is>\" [--slug s] [--stack go|python|typescript|rust|auto] [--template solo|standard|product] [--out-of-scope x]... [--success \"criterion\"]... [--no-ci] [--gitignore-workspace]",
	"project add":       "dacli project add <title> [--slug s] [--goal g] [--stage definition|elicitation|approach|design]",
	"project show":      "dacli project show <slug>",
	"ask":               "dacli ask \"question\" --about <task-ref> [--need path]",
	"escalate":          "dacli escalate \"summary\" [--about task] [--github]",
	"role add":          "dacli role add <name> [--summary s] [--kind researcher|planner|designer|implementer|reviewer] [--skill s]... [--scope glob]... [--shortcut n]... [--escalate-to role]... [--grant ro|rw] [--wip N] [--runtime rt] [--model m] [--max-points N]",
	"shortcut add":      "dacli shortcut add <name> --command 'tmpl {{p}}' --effect read|write|destructive [--summary s] [--param name=default]... [--role r]... [--why text]",
	"template show":     "dacli template show <name>",
	"github sync":       "dacli github pull <project> [--dry-run]",
	"github codeowners": "dacli github codeowners <project> | dacli github codeowners --owner <org>",
	"report":            "dacli report \"<what went wrong>\" [--body detail] [--run <run-id>] [--repo owner/name] [--disclose]\n(files an issue on the dacli tool's own tracker — an explicit action, never automatic; --disclose opts in to attaching the workspace name + run transcript, withheld by default since the upstream is public)",
}

// handlerUsageProbes names required-argument handlers whose stale table usage
// could otherwise make the generic positional detector skip the handler. Push
// shipped exactly that failure mode by advertising the unrelated github push.
var handlerUsageProbes = map[string]bool{
	"push": true,
}

// exactUsageExtensions preserve complete signatures when a composite command
// delegates its missing-argument check to one constituent handler. The handler
// only knows its own flags, while the command table must still advertise every
// flag forwarded by the composite command.
var exactUsageExtensions = map[string]string{
	"github sync": "dacli github sync <project> [task-ref...] [--since <dur>] [--findings-as-issues] [--with-tasks] [--include-internal] [--dry-run] [--limit N] [--cursor N] [--include-acceptance]",
}

// TestCommandUsageMatchesHandlerUsage keeps the command table — the shared
// CLI/MCP contract — from advertising a copied synopsis. A handler that
// reports a missing argument supplies the executable contract; commands whose
// omission is valid or whose error is not a synopsis are intentionally skipped.
func TestCommandUsageMatchesHandlerUsage(t *testing.T) {
	originalToken, hadToken := os.LookupEnv(agentid.EnvVar)
	if err := os.Unsetenv(agentid.EnvVar); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if hadToken {
			_ = os.Setenv(agentid.EnvVar, originalToken)
		}
	})
	dir := t.TempDir()
	gitInit(t, dir)
	run(t, dir, 0, "init", "--name", "usage-parity")
	mcpDescription := mcp.CommandDescription(len(commands))
	if len(mcpDescription) > 4096 {
		t.Fatalf("MCP cli description is %d bytes; command discovery must stay lazy", len(mcpDescription))
	}
	if !strings.Contains(mcpDescription, `followed by "--help"`) {
		t.Fatal("MCP cli description does not direct callers to table-derived help")
	}

	checked := 0
	for i := range commands {
		cmd := &commands[i]
		if want, ok := exactUsageExtensions[cmd.Path]; ok && cmd.Usage != want {
			t.Errorf("%s Usage = %q, want complete documented signature %q", cmd.Path, cmd.Usage, want)
		}
		if !strings.HasPrefix(cmd.Usage, "dacli "+cmd.Path) {
			t.Errorf("%s Usage = %q, want synopsis to begin %q", cmd.Path, cmd.Usage, "dacli "+cmd.Path)
		}
		help := run(t, dir, 0, append(strings.Fields(cmd.Path), "--help")...)
		if !strings.Contains(help, cmd.Usage) {
			t.Errorf("%s --help omitted declared Usage %q", cmd.Path, cmd.Usage)
		}
		mcpOut, mcpMsg, mcpCode := executor(dir)(append(strings.Fields(cmd.Path), "--help"), false)
		if mcpCode != 0 || mcpMsg != "" || !strings.Contains(mcpOut, cmd.Usage) {
			t.Errorf("MCP cli help for %s = (%q, %q, %d), want declared Usage %q", cmd.Path, mcpOut, mcpMsg, mcpCode, cmd.Usage)
		}
		// Only probe handlers whose synopsis declares a required first
		// positional, plus the explicit flag-first variants above. Calling a
		// no-argument command to learn whether omission is valid can perform
		// real work (ship is the load-bearing example from issue #692).
		tail := strings.TrimSpace(strings.TrimPrefix(cmd.Usage, "dacli "+cmd.Path))
		if !strings.HasPrefix(tail, "<") && !strings.HasPrefix(tail, `"<`) {
			_, variant := handlerUsageVariants[cmd.Path]
			if !variant && !handlerUsageProbes[cmd.Path] {
				continue
			}
		}
		ctx := &Ctx{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, Cwd: dir}
		err := cmd.Run(ctx, nil)
		if err == nil || exitCode(err) != 2 {
			continue
		}
		got, ok := strings.CutPrefix(err.Error(), "usage: ")
		if !ok {
			continue
		}
		checked++
		if got != cmd.Usage && handlerUsageVariants[cmd.Path] != got {
			t.Errorf("%s Usage = %q, handler missing-argument usage = %q", cmd.Path, cmd.Usage, got)
		}
	}
	if checked == 0 {
		t.Fatal("no command emitted a handler usage error — this invariant measured nothing")
	}
}
