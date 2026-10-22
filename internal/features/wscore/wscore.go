// Package wscore is the workspace-bootstrap slice: init and identity.
package wscore

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/gates"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/team"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

var Commands = []clikit.Command{
	{Path: "init", Brief: "Create a .dacli workspace (--template seeds a default process, --roster seeds roles)", JSON: true, Mutates: true, Usage: "dacli init [--name n] [--template t] [--roster r] [--gitignore-workspace]", Run: cmdInit},
	{Path: "whoami", Brief: "Show the acting agent and its grant", JSON: true, Usage: "dacli whoami", Run: cmdWhoami},
}

func cmdInit(ctx *clikit.Ctx, args []string) error {
	f, _ := clikit.ParseFlags(args)
	if err := f.Reject("name", "template", "roster", "gitignore-workspace"); err != nil {
		return err
	}
	name := f.Get("name")
	if name == "" {
		name = filepath.Base(ctx.Cwd)
	}

	// Validate the advertised flags BEFORE creating anything: a typo in
	// --template or --roster should refuse loudly, not silently seed an empty
	// workspace (the bug this slice previously had — clikit.ParseFlags accepts
	// unknown flags, so an ignored --template exited 0 with no process seeded).
	tmpl := f.Get("template")
	roster := f.Get("roster")
	if tmpl != "" {
		if _, err := gates.Get(nil, tmpl); err != nil {
			return clikit.Usagef("unknown template %q — available: %s", tmpl, templateNames())
		}
	}
	if roster != "" {
		if _, ok := rosters[roster]; !ok {
			return clikit.Usagef("unknown roster %q — available: %s", roster, rosterNames())
		}
	}

	w, err := workspace.Init(ctx.Cwd, name)
	if err != nil {
		return err
	}
	fmt.Fprintf(ctx.Stdout, "initialized workspace %q (%s) at %s\n", w.Name, w.ID, filepath.Join(w.Root, workspace.Dir))

	// Keep the workspace off trunk, and record where its history lives. This
	// runs for EVERY entry point, not just greenfield `new`: adopting an
	// existing repo is the common case, and it was the one still tracking its
	// workspace on the branch it happened to be on. Opt out with
	// --gitignore-workspace=false.
	if untrackOptedIn(f) {
		changed, err := workspace.UntrackFromTrunk(w, ctx.Cwd)
		if err != nil {
			return err
		}
		if changed {
			fmt.Fprintf(ctx.Stdout, "gitignored %s/ — trunk stays code. Its history is not lost: `dacli ship` records it on the %s branch (override with --record-branch), and `git log %s` reads it back.\n",
				workspace.Dir, workspace.DefaultRecordBranch, workspace.DefaultRecordBranch)
		}
	}

	// --template records the workspace default process. `project add` with no
	// --template falls back to it, so a new user's first project is staged
	// exactly as WALKTHROUGH.md § 1 promises — instead of the flag being
	// silently dropped.
	if tmpl != "" {
		if err := setDefaultTemplate(w, tmpl); err != nil {
			return err
		}
		fmt.Fprintf(ctx.Stdout, "default template: %s (new projects seed this process; override with `project add --template`)\n", tmpl)
	}

	// --roster seeds a starting set of role files. Editing them is expected;
	// the roster only saves the first-run typing.
	if roster != "" {
		roles := rosters[roster]
		for _, r := range roles {
			if err := store.CreateRole(w, "a-root", r); err != nil {
				return err
			}
		}
		fmt.Fprintf(ctx.Stdout, "roster %s: seeded %d role(s): %s\n", roster, len(roles), roleNames(roles))
	}
	if !ctx.JSON {
		printGettingStarted(ctx)
	}
	return nil
}

// printGettingStarted is the first-run onboarding a human sees exactly once,
// right after `dacli init` creates the workspace: the smallest next-step
// path from an empty workspace to a briefed agent. It never runs under
// --json — a machine caller of init wants the workspace facts above, not a
// decorative reading list — and is colorized only on a real terminal (see
// clikit.Palette), so it never leaks ANSI into a captured or piped log.
func printGettingStarted(ctx *clikit.Ctx) {
	pal := clikit.NewPalette(ctx)
	steps := [][2]string{
		{"dacli whoami", "see your identity and grant"},
		{`dacli project add "<title>"`, "create your first project"},
		{`dacli task add "<title>" --project <slug> --accept <criterion>`, "add a task with acceptance criteria"},
		{"dacli next", "see what's ready to work on"},
		{"dacli native enroll --help", "inspect explicit owner enrollment for the native host plugin"},
		{"dacli context <ref> --budget 4000", "prepare canonical context for your native host agent"},
		{"dacli overview", "a human-first summary, any time"},
	}
	width := 0
	for _, s := range steps {
		if len(s[0]) > width {
			width = len(s[0])
		}
	}
	fmt.Fprintf(ctx.Stdout, "\n%s\n", pal.Bold("Getting started"))
	for _, s := range steps {
		fmt.Fprintf(ctx.Stdout, "  %s  %s\n", pal.Cyan(fmt.Sprintf("%-*s", width, s[0])), s[1])
	}
	fmt.Fprintln(ctx.Stdout, pal.Dim("Docs: README.md · docs/WALKTHROUGH.md · docs/DOGFOOD.md"))
}

// setDefaultTemplate appends the chosen template to config.yml. open() parses
// it back into Workspace.DefaultTemplate, and planning's `project add` reads
// that as the fallback when no --template is passed.
func setDefaultTemplate(w *workspace.Workspace, tmpl string) error {
	raw, err := os.ReadFile(w.ConfigPath())
	if err != nil {
		return err
	}
	updated := strings.TrimRight(string(raw), "\n") + fmt.Sprintf("\ndefault_template: %s\n", tmpl)
	return os.WriteFile(w.ConfigPath(), []byte(updated), 0o644)
}

func templateNames() string {
	ts, _ := gates.Load(nil)
	var names []string
	for _, t := range ts {
		names = append(names, t.Name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func rosterNames() string {
	var names []string
	for n := range rosters {
		names = append(names, n)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func roleNames(roles []team.Role) string {
	var names []string
	for _, r := range roles {
		names = append(names, r.Name)
	}
	return strings.Join(names, ", ")
}

func cmdWhoami(ctx *clikit.Ctx, args []string) error {
	// This command takes no flags, so ANY flag is a typo. An empty allowlist
	// rejects every one — without it a mistyped flag was dropped and the
	// command ran as if nothing were wrong.
	if f, ferr := clikit.ParseFlags(args); ferr != nil {
		return ferr
	} else if err := f.Reject(); err != nil {
		return err
	}
	_, id, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	if ctx.JSON {
		return clikit.EmitJSON(ctx, struct {
			Schema string `json:"schema"`
			ID     string `json:"id"`
			Grant  string `json:"grant"`
			Role   string `json:"role,omitempty"`
		}{Schema: "identity/v1", ID: id.ID, Grant: string(id.Grant), Role: id.Role})
	}
	if id.Role != "" {
		fmt.Fprintf(ctx.Stdout, "%s (grant: %s, role: %s)\n", id.ID, id.Grant, id.Role)
	} else {
		fmt.Fprintf(ctx.Stdout, "%s (grant: %s)\n", id.ID, id.Grant)
	}
	return nil
}

// untrackOptedIn reads --gitignore-workspace, which now DEFAULTS ON: absence
// means yes. Only an explicit false opts out, so a caller who wants the old
// tracked-on-trunk arrangement has to ask for it. Mirrors the same reading
// onboard applies to its own copy of this flag.
func untrackOptedIn(f *clikit.Flags) bool {
	vals := f.All("gitignore-workspace")
	if len(vals) == 0 {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(vals[len(vals)-1])) {
	case "false", "0", "no":
		return false
	}
	return true
}
