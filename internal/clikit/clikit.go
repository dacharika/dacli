// Package clikit is the shared kernel of the feature-sliced app layer: the
// command type, flag parsing, the exit-code contract, and workspace/identity
// resolution. Every feature slice imports this; no feature slice imports
// another — that isolation is the point of the slicing, and it is enforced
// by a test (internal/cli/arch_test.go).
package clikit

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mlnomadpy/dacli/internal/agentid"
	"github.com/mlnomadpy/dacli/internal/commandresult"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

// Ctx carries the process-wide state a command needs: where to write, and
// whether the caller wants machine-readable output.
type Ctx struct {
	Stdout io.Writer
	Stderr io.Writer
	Cwd    string
	JSON   bool
	// Result carries command facts for in-process callers. The CLI serializes
	// it through commandresult when a subprocess caller requests a result;
	// human stdout/stderr remain presentation only.
	Result any
}

// Command is one subcommand. Path is the space-separated invocation, e.g.
// "task add". Feature slices export a Commands slice; the app layer
// aggregates them into one table.
//
// JSON declares that the command honors the global --json flag — either by
// emitting a machine-readable document (context, task list) or by otherwise
// adapting its output for a machine caller. It defaults false, and the app
// layer refuses --json for any command that leaves it false rather than
// letting the flag be parsed and silently ignored (the defect task 291 fixed:
// --json was honored by a handful of commands and dropped by the rest, so an
// agent got human prose under a flag that promised structure). A new command
// therefore refuses --json until its author deliberately sets this and proves
// the flag is honored.
type Command struct {
	Path  string
	Brief string
	JSON  bool
	// Mutates declares that this command changes state a read-only agent must
	// not change — the workspace, the repository, the machine, or a remote.
	// The dispatcher enforces the rw grant from this one declaration, so a new
	// mutating command is gated by describing itself rather than by its author
	// remembering to call RequireRW. Declaring it here rather than per handler
	// is the same choice, for the same reason, as Command.JSON: every guard in
	// this codebase that was applied by convention at ~100 call sites has
	// drifted (Flags.Reject reached 4 handlers of 112; four verified grant
	// bypasses shipped next to correctly-gated siblings).
	//
	// A --dry-run invocation is exempt: by contract (dacli 294) it previews
	// from the real code path and writes nothing, so previewing a mutation is
	// a read. Handlers keep any finer-grained checks they already make.
	Mutates bool
	// Usage is the flag synopsis `--help` prints, e.g.
	// "dacli loop --project <slug> [--width N] …". Optional, but a command
	// with flags and no Usage documents none of them: `--help` printed the
	// Brief alone, so `--no-progress-halt` — which requires an integer — did
	// not appear anywhere in help output at all, and reading it as a boolean
	// was the only conclusion available (issue #421).
	//
	// A handler that also returns a usage error should build both from one
	// constant, so the two can never drift apart.
	Usage string
	Run   func(ctx *Ctx, args []string) error
}

// --- The exit-code contract (ARCHITECTURE § 4). The 1/3 distinction is the
// one that matters: retrying a refusal is the loop a supervisor must never
// enter.

type exitErr struct {
	code int
	msg  string
}

func (e exitErr) Error() string { return e.msg }

// Usagef is exit 2: the caller's mistake.
func Usagef(format string, a ...any) error { return exitErr{2, fmt.Sprintf(format, a...)} }

// Refusedf is exit 3: policy said no. An answer, never retried.
func Refusedf(format string, a ...any) error { return exitErr{3, fmt.Sprintf(format, a...)} }

// RequireRW refuses (exit 3) unless the acting identity holds a read-write
// grant. It is the single capability check that every command able to execute
// code, mutate the workspace, or write to the remote must call — so a new such
// command cannot ship without one, and a read-only agent is never one
// grant-string away from operator-level side effects. The grant model is
// cooperative (agentid § 1), so this is the enforcement point that makes it a
// boundary rather than a suggestion for the CLI's own privileged surface.
func RequireRW(id *agentid.Identity, action string) error {
	if id.Grant != model.GrantRW {
		return Refusedf("%s needs an rw grant (yours is %s)", action, OrDash(string(id.Grant)))
	}
	return nil
}

// ExitCode maps an error onto the contract.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee exitErr
	if errors.As(err, &ee) {
		return ee.code
	}
	// A lost agent token (DACLI_AGENT set but empty) is a policy refusal, not a
	// transient error: retrying the same broken environment can only fail the
	// same way, so it exits 3 like every other refusal (dacli 288). Left as the
	// generic exit 1 it would teach a supervisor to retry a request that will
	// never succeed.
	if errors.Is(err, agentid.ErrEmptyToken) {
		return 3
	}
	// A policy refusal raised in the entity layer, which cannot import this
	// package to build a Refusedf. Without this it surfaced as a generic exit
	// 1 and taught supervisors to retry an answer (dacli 348).
	if errors.Is(err, store.ErrRefused) {
		return 3
	}
	var nf store.ErrNotFound
	if errors.As(err, &nf) {
		return 4
	}
	if errors.Is(err, workspace.ErrNotFound) {
		return 4
	}
	return 1
}

// ErrorDetails is the stable machine-facing error shape. CLI front ends can
// serialize it directly instead of parsing Error(), while older human paths
// continue to print the same error chain.
type ErrorDetails struct {
	Message          string                            `json:"message"`
	ExitCode         int                               `json:"exit_code"`
	Diagnostic       *commandresult.Diagnostic         `json:"diagnostic,omitempty"`
	ReviewValidation *store.ReviewValidationDiagnostic `json:"review_validation,omitempty"`
	Suggestions      []string                          `json:"suggestions"`
	NextActions      []string                          `json:"next_actions"`
}

// ErrorGuidance is implemented by presentation-layer errors that can offer
// safe recovery without changing the exit-code contract. The command graph is
// owned by the CLI app, so the kernel deliberately knows only this interface,
// not how suggestions are generated.
type ErrorGuidance interface {
	ErrorSuggestions() []string
	ErrorNextActions() []string
}

// DescribeError preserves typed subprocess facts through arbitrary %w wraps.
func DescribeError(err error) ErrorDetails {
	details := ErrorDetails{ExitCode: ExitCode(err), Suggestions: []string{}, NextActions: []string{}}
	if err == nil {
		return details
	}
	details.Message = err.Error()
	if diagnostic, ok := commandresult.AsDiagnostic(err); ok {
		details.Diagnostic = &diagnostic
	}
	var reviewValidation *store.ReviewValidationError
	if errors.As(err, &reviewValidation) {
		diagnostic := reviewValidation.Diagnostic
		details.ReviewValidation = &diagnostic
	}
	var guidance ErrorGuidance
	if errors.As(err, &guidance) {
		details.Suggestions = append([]string(nil), guidance.ErrorSuggestions()...)
		details.NextActions = append([]string(nil), guidance.ErrorNextActions()...)
	}
	return details
}

// Planned returns an honest stub: what the command is waiting on and where
// the design lives. "not implemented — see DESIGN.md" told nobody anything.
func Planned(what, doc string) func(*Ctx, []string) error {
	return func(ctx *Ctx, args []string) error {
		return fmt.Errorf("not built yet: %s. The design is in %s — implementation lands with that subsystem", what, doc)
	}
}

// --- Flags: --key value, --key=value, repeatable keys, positionals.
//
// A parser with no per-flag schema fundamentally cannot tell "--key --other"
// (bool key, then a separate bool flag other) from "--key --other" (key's
// value happens to start with --) — Go's own flag package has the same gap
// and resolves it the same way. Two escapes make a dash-leading value
// unambiguous without requiring a schema:
//   - the = form: --key=--value
//   - the -- terminator: --key -- --value (the literal "--" token forces
//     the token after it to be taken as key's value verbatim)
//
// A caller that knows some of its own flags are never boolean (they always
// take a value, e.g. runtime add's --arg/--sandbox-ro-arg/--model-flag) can
// name them via valueFlags so the space form works directly for those keys
// without either escape — see cmdRuntimeAdd. Silently defaulting such a key
// to "true" is exactly the corruption filed against run 01KY2K8N4C.

// alwaysBool names flags that never take a value anywhere in the CLI. The
// parser has no per-command schema, so a bare flag followed by a positional
// otherwise eats it — and for these keys that is a correctness bug, not a
// nuisance: `--dry-run 001` disabled the preview AND dropped task 001 from the
// window, turning a rehearsal into a real remote write.
//
// Keep this list to flags that are unambiguously boolean in EVERY command that
// accepts them; anything command-specific belongs in that command's
// ParseFlags(valueFlags...) call instead.
var alwaysBool = map[string]bool{
	"dry-run": true, "force": true, "yolo": true, "advise": true,
	"detach": true, "worktree": true, "reap": true,
	"all": true, "json": true, "allow-unobservable-check-policy": true, "operator": true,
}

type Flags struct {
	Pos  []string
	vals map[string][]string
}

func ParseFlags(args []string, valueFlags ...string) (*Flags, error) {
	valueOnly := make(map[string]bool, len(valueFlags))
	for _, k := range valueFlags {
		valueOnly[k] = true
	}
	f := &Flags{vals: map[string][]string{}}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "--") {
			f.Pos = append(f.Pos, a)
			continue
		}
		key := a[2:]
		// A BARE "--" outside the documented escape position. The escape form
		// (`--key -- --value`) is consumed by the --key iteration above, so
		// reaching here means this "--" leads nothing.
		//
		// It used to fall straight through with key == "" and swallow the next
		// token into a flag with an EMPTY NAME: `["--", "x"]` parsed as
		// {"": ["x"]}, and `["--", "--", "--dry-run", ""]` as
		// {"": ["--dry-run"]} — the --dry-run silently gone. That is the
		// silently-dropped-flag class this parser has been bitten by twice
		// already, hiding in the escape hatch built to prevent it. Found by
		// FuzzParseFlags in six seconds (dacli 350).
		//
		// POSIX semantics instead: everything after a bare "--" is positional.
		// It is what a caller expects, and it cannot lose a flag — the worst
		// case is an argument the command then rejects by name.
		if key == "" {
			f.Pos = append(f.Pos, args[i+1:]...)
			break
		}
		if eq := strings.Index(key, "="); eq >= 0 {
			f.vals[key[:eq]] = append(f.vals[key[:eq]], key[eq+1:])
			continue
		}
		if alwaysBool[key] {
			// Never consume the next token for a flag that has no value form.
			// Without this, `github push p --dry-run 001 002` swallowed `001`
			// as dry-run's value: the preview flag read false AND the task
			// silently left the push window. These keys are safety- or
			// scope-bearing everywhere they appear, so the fix is global
			// rather than per-command. The `--key=false` form is handled
			// above and still turns them off.
			f.vals[key] = append(f.vals[key], "true")
			continue
		}
		// The documented escape (`--key -- VALUE`) comes AFTER the boolean
		// check, because a flag with no value form cannot be given one. It
		// used to come first, so `--dry-run -- ""` set dry-run to "" and
		// walked straight past the guard above — the escape hatch built to
		// disambiguate values quietly re-opened the hole it sits next to.
		// Found by FuzzParseFlags (dacli 350).
		if i+2 < len(args) && args[i+1] == "--" {
			i += 2
			f.vals[key] = append(f.vals[key], args[i])
			continue
		}
		if valueOnly[key] {
			if i+1 >= len(args) {
				return f, Usagef("--%s requires a value (use --%s=VALUE or --%s -- VALUE)", key, key, key)
			}
			i++
			f.vals[key] = append(f.vals[key], args[i])
			continue
		}
		if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
			f.vals[key] = append(f.vals[key], "true") // bare flag
			continue
		}
		i++
		f.vals[key] = append(f.vals[key], args[i])
	}
	return f, nil
}

func (f *Flags) Get(k string) string {
	if v := f.vals[k]; len(v) > 0 {
		return v[len(v)-1]
	}
	return ""
}
func (f *Flags) All(k string) []string { return f.vals[k] }

// Bool reports whether a bare boolean flag was set.
//
// The subtlety is what happens when a bool flag is followed by a positional:
// the schema-less parser consumes that positional as the flag's VALUE, so
// `github push p --dry-run 001 002` parsed as dry-run="001". Read as
// `Get(k) == "true"` that yielded FALSE — the safety flag silently turned
// itself off and the command mutated the remote for real, while `001` also
// dropped out of the positional window.
//
// Treating any non-"false" value as set is the safe reading: the caller wrote
// the flag, so the flag is on, and the swallowed token is recovered as a
// positional by Pos. `--flag=false` still turns it off explicitly.
func (f *Flags) Bool(k string) bool {
	v, ok := f.vals[k]
	if !ok || len(v) == 0 {
		return false
	}
	last := strings.TrimSpace(strings.ToLower(v[len(v)-1]))
	return last != "false" && last != "0"
}

// Int reads flag k as a base-10 integer. An absent or empty flag returns def;
// a present but non-numeric value is a usage error (exit 2), never a silent
// fallback to def. This is the single integer-flag reader: before it, callers
// hand-rolled the parse four different ways — strconv.Atoi with the error
// dropped, fmt.Sscanf with the result ignored, an Atoi guarded by `n > 0` — and
// every one of them turned garbage into zero. That is exactly how
// `spawn --timeout 30s` ran unbounded on the default instead of refusing: "30s"
// failed to parse and the discarded error let the default stand (dacli 243).
func (f *Flags) Int(k string, def int) (int, error) {
	v := f.Get(k)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def, Usagef("--%s must be an integer, got %q", k, v)
	}
	return n, nil
}

// Duration reads flag k as a time span, accepting either a Go duration
// ("30m", "90s") or a bare integer count of SECONDS ("90") — both spellings
// are already in the CLI's surface, so one reader accepts both rather than
// forcing a flag day. An absent or empty flag returns def; anything else that
// does not parse is a usage error (exit 2), never a silent fallback.
//
// It is the duration twin of Int, and exists for the same reason: the loop's
// own knobs re-created the exact defect Int was written to kill. `--idle 5min`
// (not a Go unit) silently became the 30m default, and `--budget-window`
// garbage silently became 24h, because the helper they used returned the
// default on a parse error. A caller who typed a bound and got the default is
// strictly worse off than one who was told the flag was wrong.
func (f *Flags) Duration(k string, def time.Duration) (time.Duration, error) {
	v := strings.TrimSpace(f.Get(k))
	if v == "" {
		return def, nil
	}
	if d, err := time.ParseDuration(v); err == nil {
		return d, nil
	}
	if n, err := strconv.Atoi(v); err == nil {
		return time.Duration(n) * time.Second, nil
	}
	return def, Usagef("--%s must be a duration (30m, 90s) or a whole number of seconds, got %q", k, v)
}

// Int64 is Int for values that can exceed an int on 32-bit platforms — token
// ceilings, most of all. Same contract: garbage is a usage error, never a
// silent default. `--window-tokens 50k` used to parse as 50 (Sscanf stops at
// the 'k' and reports no error), and `--window-tokens garbage` became 0, which
// the ceiling check reads as UNLIMITED — so an operator who asked for a cap
// ran with none.
func (f *Flags) Int64(k string, def int64) (int64, error) {
	v := f.Get(k)
	if v == "" {
		return def, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return def, Usagef("--%s must be an integer, got %q", k, v)
	}
	return n, nil
}

// Alias resolves the first of names that was actually passed, so a flag can be
// renamed without breaking every caller and script that used the old spelling.
// Returns the name found and whether any was.
//
// It exists because one vocabulary was doing three jobs: `--budget` meant the
// BRIEF's size, `--budget-window` meant a time period, and `--max-tokens` /
// `--window-tokens` meant spend ceilings — so an agent could not predict the
// flag for a command it had not used from the ones it had (task 292). Each
// concept now has one canonical name and keeps its old spelling as an alias.
func (f *Flags) Alias(names ...string) (string, bool) {
	for _, n := range names {
		if _, ok := f.vals[n]; ok {
			return n, true
		}
	}
	return "", false
}

// IntAliased is Int over a canonical name plus aliases, refusing garbage the
// same way. The FIRST name is canonical; the rest are accepted spellings.
func (f *Flags) IntAliased(def int, names ...string) (int, error) {
	if n, ok := f.Alias(names...); ok {
		return f.Int(n, def)
	}
	return def, nil
}

// Raw exposes every parsed flag, for commands (like `run`) that forward
// unknown flags as parameters.
func (f *Flags) Raw() map[string][]string { return f.vals }

// Reject enforces an allowlist: any parsed flag not named in known is
// reported as a usage error (exit 2) instead of being silently dropped. Opt
// in per-command — a global check inside ParseFlags would break `run`, which
// forwards unknown flags via Raw() by design (dacli 143).
func (f *Flags) Reject(known ...string) error {
	allowed := make(map[string]bool, len(known))
	for _, k := range known {
		allowed[k] = true
	}
	var bad []string
	for k := range f.vals {
		if !allowed[k] {
			bad = append(bad, k)
		}
	}
	if len(bad) == 0 {
		return nil
	}
	sort.Strings(bad)
	return Usagef("unknown flag(s): --%s", strings.Join(bad, ", --"))
}

// --- Shared plumbing ---

// OpenWorkspace resolves the workspace from cwd and the acting identity from
// the environment.
func OpenWorkspace(ctx *Ctx) (*workspace.Workspace, *agentid.Identity, error) {
	w, err := workspace.Find(ctx.Cwd)
	if err != nil {
		return nil, nil, err
	}
	id, err := agentid.Resolve(w)
	if err != nil {
		return nil, nil, err
	}
	return w, id, nil
}

// EmitJSON writes v as indented JSON to stdout.
func EmitJSON(ctx *Ctx, v any) error {
	enc := json.NewEncoder(ctx.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// Short truncates s to at most n bytes, for the abbreviated ids that appear
// throughout the CLI's output (run ids, event ids, agent ids).
//
// It exists because `s[:n]` does not: run and event ids come from directory
// and file names, which are hand-editable by design, so a name shorter than
// the assumed width panicked the command outright — `dacli runs list` crashed
// on a 9-character run directory. A display helper must never be able to take
// down the process it is decorating (dacli 207).
func Short(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// FirstLine returns the first non-blank line of s, trimmed of surrounding
// whitespace. It trims BEFORE locating the newline so that a body which opens
// with a blank line (or is entirely leading whitespace before its content)
// still yields its real first line rather than "".
//
// It replaces three subtly incompatible copies that had diverged: one that did
// no trimming at all (so a refusal body starting with '\n' logged a BLANK
// reason — dacli 242), one that trimmed only after the cut (same blank-reason
// bug), and this, the correct behaviour, which is now the single contract used
// everywhere a body is summarised to one line.
func FirstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// OrDash returns s, or a default, or "-".
func OrDash(s string, def ...string) string {
	if s != "" {
		return s
	}
	if len(def) > 0 {
		return def[0]
	}
	return "-"
}

// ErrStr renders an error for run records; nil is "0" (a clean exit).
func ErrStr(err error) string {
	if err == nil {
		return "0"
	}
	return err.Error()
}
