// Package cli is the app layer of the feature-sliced design: it aggregates
// the feature slices' command tables, dispatches argv, and hosts the MCP
// front end's executor. It owns NO feature logic — a command body in this
// package is a layering bug.
//
// The FSD mapping for this repo (documented in ARCHITECTURE § 2b):
//
//	shared    ulid, mdstore, prompts, spm, shortcut, team, clikit
//	entities  model, workspace, store, eventlog, agentid, brief
//	features  internal/features/* — one slice per capability, and slices
//	          NEVER import each other (enforced by arch_test.go)
//	app       this package, and internal/mcp
package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/mlnomadpy/dacli/internal/agentid"
	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/commandresult"
	"github.com/mlnomadpy/dacli/internal/features/acceptance"
	"github.com/mlnomadpy/dacli/internal/features/briefing"
	"github.com/mlnomadpy/dacli/internal/features/catalog"
	"github.com/mlnomadpy/dacli/internal/features/cleanup"
	"github.com/mlnomadpy/dacli/internal/features/collab"
	"github.com/mlnomadpy/dacli/internal/features/dashboard"
	"github.com/mlnomadpy/dacli/internal/features/execution"
	"github.com/mlnomadpy/dacli/internal/features/ghmirror"
	"github.com/mlnomadpy/dacli/internal/features/insight"
	"github.com/mlnomadpy/dacli/internal/features/journal"
	"github.com/mlnomadpy/dacli/internal/features/knowledge"
	"github.com/mlnomadpy/dacli/internal/features/onboard"
	"github.com/mlnomadpy/dacli/internal/features/orchestration"
	"github.com/mlnomadpy/dacli/internal/features/planning"
	"github.com/mlnomadpy/dacli/internal/features/queues"
	"github.com/mlnomadpy/dacli/internal/features/reconciliation"
	"github.com/mlnomadpy/dacli/internal/features/releasetrain"
	"github.com/mlnomadpy/dacli/internal/features/selfreport"
	"github.com/mlnomadpy/dacli/internal/features/ship"
	"github.com/mlnomadpy/dacli/internal/features/shortcuts"
	"github.com/mlnomadpy/dacli/internal/features/skillforge"
	"github.com/mlnomadpy/dacli/internal/features/slices"
	"github.com/mlnomadpy/dacli/internal/features/stagegate"
	"github.com/mlnomadpy/dacli/internal/features/teamops"
	"github.com/mlnomadpy/dacli/internal/features/vcs"
	"github.com/mlnomadpy/dacli/internal/features/wscore"
	"github.com/mlnomadpy/dacli/internal/mcp"
)

// Ctx and Command are re-exported from the kernel so tests and callers keep
// one import.
type (
	Ctx     = clikit.Ctx
	Command = clikit.Command
)

// commands is the whole surface: every slice's table plus the app layer's
// own (mcp serve, which needs the dispatch loop and so cannot live in a
// slice).
var commands = aggregate(
	wscore.Commands,
	onboard.Commands,
	planning.Commands,
	briefing.Commands,
	knowledge.Commands,
	collab.Commands,
	insight.Commands,
	teamops.Commands,
	shortcuts.Commands,
	queues.Commands,
	reconciliation.Commands,
	releasetrain.Commands,
	cleanup.Commands,
	journal.Commands,
	execution.Commands,
	stagegate.Commands,
	ghmirror.Commands,
	skillforge.Commands,
	vcs.Commands,
	selfreport.Commands,
	acceptance.Commands,
	ship.Commands,
	slices.Commands,
	catalog.Commands,
	orchestration.Commands,
	dashboard.Commands,
	[]Command{
		{Path: "capabilities", Brief: "Print the generated CLI, MCP, schema, prompt, and runtime-adapter capability manifest", JSON: true, Usage: "dacli capabilities [--json]", Run: cmdCapabilities},
		{Path: "mcp serve", Brief: "Serve MCP over stdio with DACLI_AGENT identity; --operator explicitly permits local root when no token is set", Usage: "dacli mcp serve [--operator]", Run: cmdMcpServe},
	},
)

func aggregate(tables ...[]Command) []Command {
	var out []Command
	for _, t := range tables {
		out = append(out, t...)
	}
	return out
}

// Test seams: the suite drives handlers through the same entry path users
// take, and needs the kernel's plumbing under the old package-local names.
var (
	openWorkspace = clikit.OpenWorkspace
	exitCode      = clikit.ExitCode
)

// Main dispatches argv and returns a process exit code.
func Main(argv []string) int {
	if len(argv) >= 2 && argv[0] == "__run-guardian" {
		return execution.RunGuardian(argv[1:])
	}
	if len(argv) == 2 && argv[0] == "__run-watchdog" {
		return execution.RunWatchdog(strings.Join(argv[1:], ""))
	}
	cwd, _ := os.Getwd()
	ctx := &Ctx{Stdout: os.Stdout, Stderr: os.Stderr, Cwd: cwd}

	args := make([]string, 0, len(argv))
	for _, a := range argv {
		if a == "--json" {
			ctx.JSON = true
			continue
		}
		args = append(args, a)
	}

	if len(args) == 0 {
		fmt.Fprint(ctx.Stdout, clikit.Banner())
		usage(ctx.Stdout)
		return 0
	}
	if args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		return help(ctx, args[1:])
	}
	// Parents are navigation nodes, not executable commands. Treat their help
	// exactly like leaf help so `dacli task --help` cannot fall through to the
	// unknown-command path just because the table stores only leaf commands.
	if len(args) == 2 && hasHelp(args[1:]) {
		if exact, _ := match(args[:1]); exact == nil && printParentHelp(ctx.Stdout, args[0]) {
			return 0
		}
	}

	cmd, rest := match(args)
	if cmd == nil {
		err := unknownCommandError(args)
		emitError(ctx, err)
		if !ctx.JSON {
			fmt.Fprintln(ctx.Stderr)
			if guidance := commandGuidanceFor(args); guidance.family != "" {
				printParentHelp(ctx.Stderr, guidance.family)
				fmt.Fprintln(ctx.Stderr)
			}
			usage(ctx.Stderr)
		}
		return exitCode(err)
	}

	err := invoke(ctx, cmd, rest)
	if resultErr := commandresult.Flush(ctx.Result); resultErr != nil && err == nil {
		err = resultErr
	}
	if err != nil {
		emitError(ctx, err)
		// The exit-code contract (ARCHITECTURE § 4): 2 usage, 3 refused by
		// policy, 4 not found, 1 everything else. Agents branch on these
		// without parsing stderr — and must never retry a 3.
		return exitCode(err)
	}
	return 0
}

// emitError is the one presentation boundary for CLI failures. JSON callers
// receive stable typed fields; human callers retain concise actionable prose.
func emitError(ctx *Ctx, err error) {
	details := clikit.DescribeError(err)
	if ctx.JSON {
		if encodeErr := json.NewEncoder(ctx.Stderr).Encode(details); encodeErr == nil {
			return
		}
	}
	fmt.Fprintf(ctx.Stderr, "dacli: %v\n", err)
	if len(details.Suggestions) > 0 {
		fmt.Fprintln(ctx.Stderr, "Suggestions:")
		for _, suggestion := range details.Suggestions {
			fmt.Fprintf(ctx.Stderr, "  %s\n", suggestion)
		}
	}
	if len(details.NextActions) > 0 {
		fmt.Fprintln(ctx.Stderr, "Next actions:")
		for _, action := range details.NextActions {
			fmt.Fprintf(ctx.Stderr, "  %s\n", action)
		}
	}
}

// invoke is the single path from a matched command to its handler: the
// dispatcher's gates, then the handler. Both front ends (argv and MCP) and the
// test suite go through it, so a gate cannot hold for users while being
// invisible to tests — which is exactly what happened before it existed: the
// suite called cmd.Run directly, so nothing covered Main's checks.
func invoke(ctx *Ctx, cmd *Command, rest []string) error {
	// --help BEFORE anything runs. It was not a flag anywhere: on a command
	// that rejects unknown flags it was a usage error, and on one that does
	// not it was silently dropped and THE COMMAND RAN — `dacli task claim 001
	// --help` claimed the task. Asking what a command does must never be the
	// thing that does it.
	if hasHelp(rest) {
		printCommandHelp(ctx, cmd)
		return nil
	}
	if err := refuseUnsupportedJSON(cmd, ctx.JSON); err != nil {
		return err
	}
	if err := refuseUngrantedMutation(ctx, cmd, rest); err != nil {
		return err
	}
	return withUsage(cmd, cmd.Run(ctx, rest))
}

// withUsage appends the command's synopsis to a USAGE error, so a refusal names
// what is right and not only what is wrong.
//
// "unknown flag(s): --kind, --title" tells a caller their invocation is
// malformed and nothing else; the agent who reported this observed that
// "--project is required" was the most useful of its four failed attempts
// precisely BECAUSE it named the correct thing (issue #436). With every command
// now carrying a Usage (dacli 347), the answer is already on hand at the moment
// of refusal.
//
// At the dispatcher rather than in Flags.Reject, for the same reason the grant
// and --json gates live here: Reject is in the kernel and cannot see the
// Command, and a rule applied per handler in this codebase has drifted every
// time. This way one place covers every command and every usage error, not just
// the unknown-flag one.
//
// Only exit 2. A policy refusal (3) has already said what to do instead, and a
// not-found (4) is not a malformed call — appending a synopsis to either would
// be noise.
func withUsage(cmd *Command, err error) error {
	if err == nil || cmd.Usage == "" || clikit.ExitCode(err) != 2 {
		return err
	}
	if strings.Contains(err.Error(), cmd.Usage) {
		return err // the handler already printed it; do not say it twice
	}
	return clikit.Usagef("%v\nusage: %s", err, cmd.Usage)
}

// refuseUnsupportedJSON returns a usage error (exit 2) when --json was
// requested for a command that does not honor it. This one dispatch-layer
// check is the whole fix for task 291: --json is parsed globally (Main strips
// it, the MCP front end passes it as jsonMode), so before this the ~110
// commands that never read ctx.JSON accepted the flag and silently emitted
// human prose — a machine caller got unparseable text under a flag that
// promised structure. Enforcing it here rather than per-command is deliberate:
// the same class of drift (a rule applied at some call sites and missed at
// others) is how Flags.Reject reached 4 handlers out of 112. A command now
// either declares Command.JSON and adapts, or the flag is refused loudly.
func refuseUnsupportedJSON(cmd *Command, jsonMode bool) error {
	if !jsonMode || cmd.JSON {
		return nil
	}
	return guidedUsageError{
		err:         clikit.Usagef("%s does not support --json", cmd.Path),
		suggestions: jsonAlternatives(cmd.Path),
		nextActions: []string{"inspect `dacli capabilities --json` before selecting a machine-readable command"},
	}
}

// jsonAlternatives returns only nearby, executable machine-readable commands.
// The old refusal printed the entire JSON catalog, which buried the useful
// replacement in dozens of unrelated paths (issue #1029).
func jsonAlternatives(path string) []string {
	type candidate struct {
		path  string
		score int
	}
	family := strings.Fields(path)[0]
	var candidates []candidate
	for _, other := range jsonCmdList() {
		if strings.Fields(other)[0] != family {
			continue
		}
		candidates = append(candidates, candidate{path: other, score: editDistance(path, other)})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score < candidates[j].score
		}
		return candidates[i].path < candidates[j].path
	})
	if len(candidates) > 3 {
		candidates = candidates[:3]
	}
	out := make([]string, 0, max(1, len(candidates)))
	for _, item := range candidates {
		out = append(out, "dacli "+item.path+" --json")
	}
	if len(out) == 0 {
		out = append(out, "dacli capabilities --json")
	}
	return out
}

// refuseUngrantedMutation enforces Command.Mutates at the dispatcher: a
// read-only agent invoking a state-changing command is refused (exit 3) before
// the handler runs.
//
// This is the grant-side twin of refuseUnsupportedJSON, and it exists because
// per-handler enforcement demonstrably does not hold. The 2026-08-06 audit
// found four live bypasses, each sitting beside a correctly-gated sibling:
// `shortcut promote` beside `shortcut add`, six `github` verbs beside `github
// release`, `agents --reap` beside `kill`, `worktree remove` beside `merge`.
// Every one of those is closed by the table declaring what it does.
//
// Command-specific read shapes remain declared here rather than hidden in a
// handler. `start --show` and an explicitly selected inspect profile are reads;
// `project show` is a read unless either landing-policy flag is present. Both
// commands keep their command-level Mutates declaration because their other
// invocation shapes persist state. A --dry-run is also a read (see
// Command.Mutates). A workspace that cannot be opened
// yields no identity to judge, so the gate
// defers to the handler, which reports the real problem (no workspace, bad
// token) instead of a misleading grant refusal.
func refuseUngrantedMutation(ctx *Ctx, cmd *Command, args []string) error {
	if !cmd.Mutates || hasDryRun(args) || mutationInvocationIsReadOnly(cmd, args) {
		return nil
	}
	_, id, err := openWorkspace(ctx)
	if err != nil {
		//nolint:nilerr // deliberate: no workspace means no identity to judge, so
		// the gate defers to the handler, which reports the REAL problem (no
		// workspace, bad token) instead of a misleading grant refusal.
		return nil
	}
	return clikit.RequireRW(id, cmd.Path)
}

func mutationInvocationIsReadOnly(cmd *Command, args []string) bool {
	switch cmd.Path {
	case "start":
		for i, arg := range args {
			if arg == "--show" || arg == "--show=true" {
				return true
			}
			if arg == "--profile=inspect" || arg == "--profile" && i+1 < len(args) && args[i+1] == "inspect" {
				return true
			}
		}
	case "project show":
		return !hasNamedFlag(args, "landing-mode") && !hasNamedFlag(args, "landing-base")
	case "reconcile":
		return !hasNamedFlag(args, "apply-safe")
	}
	return false
}

func hasNamedFlag(args []string, name string) bool {
	flag := "--" + name
	for _, arg := range args {
		if arg == flag || strings.HasPrefix(arg, flag+"=") {
			return true
		}
	}
	return false
}

// hasDryRun reports whether --dry-run was passed. It scans argv directly
// rather than using ParseFlags because the dispatcher must not consume or
// validate a command's flags — that stays the handler's job.
func hasDryRun(args []string) bool {
	for _, a := range args {
		if a == "--dry-run" || a == "--dry-run=true" {
			return true
		}
	}
	return false
}

// hasHelp reports whether the caller asked for help rather than execution.
// Scanned from argv directly: the dispatcher must not consume or validate a
// command's flags, which stays the handler's job.
func hasHelp(args []string) bool {
	for _, a := range args {
		if a == "--help" || a == "-h" {
			return true
		}
	}
	return false
}

// printCommandHelp prints what one command is for. The Brief is the whole
// documentation a command carries, so this is deliberately thin — the point is
// that asking is SAFE and answers something, not that it answers everything.
func printCommandHelp(ctx *Ctx, cmd *Command) {
	fmt.Fprintf(ctx.Stdout, "dacli %s\n\n%s\n", cmd.Path, cmd.Brief)
	if cmd.Usage != "" {
		fmt.Fprintf(ctx.Stdout, "\n%s\n", cmd.Usage)
	}
	if cmd.JSON {
		fmt.Fprintln(ctx.Stdout, "\nSupports --json.")
	}
	if cmd.Mutates {
		fmt.Fprintln(ctx.Stdout, "Needs an rw grant (a --dry-run, where supported, is a read).")
	}
}

// jsonCmdList indirects jsonCommands so that refuseUnsupportedJSON does not
// reference the `commands` table through the static initializer graph — the
// same cycle-breaking trick as dispatch (both are assigned in init below).
var jsonCmdList func() []string

// cmdDescription breaks the static initialization cycle: commands includes
// cmdMcpServe, which supplies the table-derived MCP discovery description.
var cmdDescription func() string

// jsonCommands lists, sorted, the command paths that honor --json. It is built
// from the table so the refusal hint can never drift from the set of commands
// that actually implement the flag.
func jsonCommands() []string {
	var out []string
	for i := range commands {
		if commands[i].JSON {
			out = append(out, commands[i].Path)
		}
	}
	sort.Strings(out)
	return out
}

// commandDescription keeps MCP discovery tied to the aggregate table without
// copying its entire catalog into every tools/list response. Exact signatures
// stay lazy through the same --help dispatch used by the CLI (issue #692).
func commandDescription() string {
	return mcp.CommandDescription(len(commands))
}

// match finds the longest command path first, so "task acceptance migrate"
// beats any shorter prefix. The old hard-coded two-word ceiling made every
// three-word command visible in help but unreachable through CLI and MCP.
func match(args []string) (*Command, []string) {
	for n := len(args); n >= 1; n-- {
		if len(args) < n {
			continue
		}
		path := strings.Join(args[:n], " ")
		for i := range commands {
			if commands[i].Path == path {
				return &commands[i], args[n:]
			}
		}
	}
	return nil, nil
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "dacli — context management for hierarchies of coding agents")
	fmt.Fprintln(w, "\nUsage: dacli <command> [args] [--json]")
	fmt.Fprintln(w, "\nPrimary bounded workflow:")
	fmt.Fprintln(w, "  inspect → plan → claim → implement → verify → review → PR → CI → merge")
	fmt.Fprintln(w, "  start --profile inspect|task|wave|loop   choose the operating scope")
	fmt.Fprintln(w, "  overview / next / task                  inspect and select bounded work")
	fmt.Fprintln(w, "  context / spawn / wait                  brief, run, and collect workers")
	fmt.Fprintln(w, "  verify / accept / pr / pr diagnose      prove work and observe GitHub")
	fmt.Fprintln(w, "  pr wait / pr land / integrate / ship    wait, land one task, or finish one wave")
	fmt.Fprintln(w, "  branches audit / branches prune         inspect or safely apply cleanup plans")
	fmt.Fprintln(w, "\nCommand families:")
	parents := commandParents()
	for _, parent := range parents {
		fmt.Fprintf(w, "  %-12s  dacli %s --help\n", parent, parent)
	}
	fmt.Fprintln(w, "\nAdvanced and recovery tools remain available: dacli help --all")
	fmt.Fprintln(w, "Leaf help: dacli <command> --help")
	fmt.Fprintln(w, "\nEnvironment:")
	fmt.Fprintln(w, "  DACLI_AGENT  agent token; unset means the root agent")
}

func help(ctx *Ctx, args []string) int {
	if len(args) == 0 {
		usage(ctx.Stdout)
		return 0
	}
	if len(args) == 1 && args[0] == "--all" {
		usageAll(ctx.Stdout)
		return 0
	}
	if cmd, rest := match(args); cmd != nil && len(rest) == 0 {
		printCommandHelp(ctx, cmd)
		return 0
	}
	if printParentHelp(ctx.Stdout, strings.Join(args, " ")) {
		return 0
	}
	err := clikit.Usagef("unknown command family %q", strings.Join(args, " "))
	emitError(ctx, err)
	return exitCode(err)
}

func usageAll(w io.Writer) {
	fmt.Fprintln(w, "dacli — complete command catalog (advanced and recovery tools included)")
	fmt.Fprintln(w, "\nUsage: dacli <command> [args] [--json]")
	fmt.Fprintln(w, "\nCommands:")

	paths := make([]string, 0, len(commands))
	byPath := map[string]string{}
	width := 0
	for _, c := range commands {
		paths = append(paths, c.Path)
		byPath[c.Path] = c.Brief
		if len(c.Path) > width {
			width = len(c.Path)
		}
	}
	sort.Strings(paths)
	for _, p := range paths {
		fmt.Fprintf(w, "  %-*s  %s\n", width, p, byPath[p])
	}
	fmt.Fprintln(w, "\nUse `dacli <parent> --help` for one family or `dacli <command> --help` for a leaf.")
}

func commandParents() []string {
	seen := map[string]bool{}
	for _, c := range commands {
		parts := strings.Fields(c.Path)
		if len(parts) > 1 {
			seen[parts[0]] = true
		}
	}
	parents := make([]string, 0, len(seen))
	for parent := range seen {
		parents = append(parents, parent)
	}
	sort.Strings(parents)
	return parents
}

func printParentHelp(w io.Writer, parent string) bool {
	prefix := parent + " "
	var children []Command
	for _, c := range commands {
		if strings.HasPrefix(c.Path, prefix) {
			children = append(children, c)
		}
	}
	if len(children) == 0 {
		return false
	}
	sort.Slice(children, func(i, j int) bool { return children[i].Path < children[j].Path })
	fmt.Fprintf(w, "dacli %s — command family\n\n", parent)
	width := 0
	for _, c := range children {
		if len(c.Path) > width {
			width = len(c.Path)
		}
	}
	for _, c := range children {
		fmt.Fprintf(w, "  %-*s  %s\n", width, c.Path, c.Brief)
	}
	fmt.Fprintln(w, "\nUse `dacli <command> --help` for flags and policy requirements.")
	return true
}

// dispatch indirects the command lookup so that the commands table can
// reference cmdMcpServe without a static initialization cycle.
var dispatch func(args []string) (*Command, []string)

func init() {
	dispatch = match
	jsonCmdList = jsonCommands
	cmdDescription = commandDescription
	manifestCommandTable = func() []Command { return commands }
	guidanceCommandTable = func() []Command { return commands }
	selfreport.Compatibility = cmdCompatibility
}

// executor adapts the command table for the MCP server: same dispatch, same
// exit-code contract, buffered output. This closure is the entire coupling
// between the two front ends — mcp never imports cli.
func executor(cwd string) mcp.Executor {
	return func(argv []string, jsonMode bool) (string, string, int) {
		var out, errb bytes.Buffer
		c := &Ctx{Stdout: &out, Stderr: &errb, Cwd: cwd, JSON: jsonMode}
		cmd, rest := dispatch(argv)
		if cmd == nil {
			err := unknownCommandError(argv)
			return "", marshalErrorDetails(clikit.DescribeError(err)), exitCode(err)
		}
		// The MCP front end is a second door to the same table, so it goes
		// through the same gates — otherwise every bypass they close reopens
		// over MCP.
		err := invoke(c, cmd, rest)
		msg := errb.String()
		if err != nil {
			details := clikit.DescribeError(err)
			if note := strings.TrimSpace(msg); note != "" {
				details.Message = note + "\n" + details.Message
			}
			msg = marshalErrorDetails(details)
		}
		return out.String(), msg, exitCode(err)
	}
}

func marshalErrorDetails(details clikit.ErrorDetails) string {
	b, err := json.Marshal(details)
	if err != nil {
		return details.Message
	}
	return string(b)
}

func cmdMcpServe(ctx *Ctx, args []string) error {
	f, ferr := clikit.ParseFlags(args)
	if ferr != nil {
		return ferr
	}
	if err := f.Reject("operator"); err != nil {
		return err
	}
	if len(f.Pos) != 0 {
		return clikit.Usagef("mcp serve accepts no positional arguments")
	}
	// Flags.Bool deliberately treats arbitrary values as enabled in some
	// legacy commands. An authority opt-in must accept only explicit booleans.
	for _, value := range f.All("operator") {
		if value != "true" && value != "false" {
			return clikit.Usagef("--operator accepts only true or false")
		}
	}
	_, tokenPresent := os.LookupEnv("DACLI_AGENT")
	if f.Bool("operator") && tokenPresent {
		return clikit.Refusedf("--operator conflicts with DACLI_AGENT; it is only available when no agent token is set")
	}
	if !tokenPresent && !f.Bool("operator") {
		return clikit.Refusedf("MCP requires DACLI_AGENT; provide an agent token through the environment, or explicitly use --operator for local root access")
	}
	// Validate the real workspace and identity before reading any requests.
	// Keep ordinary CLI root convenience, but never inherit it implicitly at
	// the agent-facing server boundary (issue #1069). Resolution errors do not
	// contain the credential, including for empty or unrecognized tokens.
	if _, _, err := openWorkspace(ctx); err != nil {
		if errors.Is(err, agentid.ErrBadToken) || errors.Is(err, agentid.ErrEmptyToken) {
			return clikit.Refusedf("cannot serve MCP: %v", err)
		}
		return err
	}
	mode := "identity from DACLI_AGENT"
	if f.Bool("operator") {
		mode = "explicit local operator root identity"
	}
	fmt.Fprintf(ctx.Stderr, "dacli mcp: serving on stdio (%s)\n", mode)
	return mcp.Serve(os.Stdin, ctx.Stdout, executor(ctx.Cwd), cmdDescription())
}
