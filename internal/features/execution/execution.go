// Package execution is the runtime slice: dacli launching agents. Adapter
// management, single spawns, the supervision loop, and run records. This is
// the one slice that runs processes — and where the permission model stops
// being cooperative for spawned children (RUNTIMES.md § 8).
package execution

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mlnomadpy/dacli/internal/agentid"
	"github.com/mlnomadpy/dacli/internal/brief"
	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/eventlog"
	"github.com/mlnomadpy/dacli/internal/gates"
	"github.com/mlnomadpy/dacli/internal/gitx"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/procmon"
	"github.com/mlnomadpy/dacli/internal/prompts"
	"github.com/mlnomadpy/dacli/internal/providerpolicy"
	"github.com/mlnomadpy/dacli/internal/spm"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/team"
	"github.com/mlnomadpy/dacli/internal/ulid"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

var Commands = []clikit.Command{
	{Path: "runtime add", Brief: "Add a coding-agent CLI adapter (--preset claude-code|claude-code-rw|codex|codex-rw|gemini|gemini-rw|copilot|copilot-rw|generic-exec)", Mutates: true, Usage: "dacli runtime add <name> [--preset claude-code|claude-code-rw|codex|codex-rw|gemini|gemini-rw|copilot|copilot-rw|generic-exec] [--harness family] [--binary b] [--mode stdin|arg] [--flag -p] [--arg a]... [--sandbox-ro-arg a]... [--env NAME]... [--model-flag f] [--token-limit-flag f] [--execution-capability local-coordination-socket]... [--behavioral-preflight codex-exec-json-v1|codex-exec-json-v2|claude-print-v1]", Run: cmdRuntimeAdd},
	{Path: "runtime rm", Brief: "Remove a runtime adapter (refuses while a role routes to it)", Mutates: true, Usage: "dacli runtime rm <name>", Run: cmdRuntimeRm},
	{Path: "runtime list", Brief: "Configured runtimes and their declared capabilities", Usage: "dacli runtime list", Run: cmdRuntimeList},
	{Path: "runtime doctor", Brief: "Probe binary/version and exact behavioral launch compatibility", JSON: true, Usage: "dacli runtime doctor [--runtime name] [--grant ro|rw]", Run: cmdRuntimeDoctor},
	{Path: "claim expand", Brief: "Owner-authorize a reasoned path-scope expansion for a later relaunch", JSON: true, Mutates: true, Usage: "dacli claim expand --task <ref> --run <id> --add <path>... --reason <text> [--json]", Run: cmdClaimExpand},
	{Path: "spawn", Brief: "Launch a child agent on a runtime: identity, brief, sandbox, run record (--detach to background)", Mutates: true, Usage: "dacli spawn --task <ref> [--runtime name] [--role r] [--grant ro|rw] [--model m] [--harness family]... [--worktree] [--detach] [--claim path,path] [--pr] [--review [--structured-review-result] [--preflight-fingerprint sha256:...] [--pr-number N]] [--budget N] [--max-tokens N [--allow-advisory-tokens]] [--timeout sec] [--cooperative|--allow-user-config] [--advise] [--force]", Run: cmdSpawn},
	{Path: "wait", Brief: "Block until detached run(s) finish, then finalize their outcome (default: all live)", Mutates: true, Usage: "dacli wait [<run-id>...] [--interval DUR] [--timeout DUR]", Run: cmdWait},
	{Path: "supervise", Brief: "Spawn-evaluate-correct loop until accepted or --max-turns", Mutates: true, Usage: "dacli supervise --task <ref> [--runtime name] [--role r] [--max-turns N] [--grant ro|rw] [--model m] [--claim path,path] [--pr] [--review [--pr-number N]] [--budget N] [--max-tokens N [--allow-advisory-tokens]] [--timeout sec] [--cooperative|--allow-user-config] [--advise] [--force]", Run: cmdSupervise},
	{Path: "runs list", Brief: "Recorded agent runs, newest first", Usage: "dacli runs list", Run: cmdRunsList},
	{Path: "runs show", Brief: "Invocation, outcome, brief, and transcript for one run", Usage: "dacli runs show <run-id-prefix>", Run: cmdRunsShow},
	{Path: "runs prune", Brief: "Bound transcript growth (--keep N, default 20)", Mutates: true, Usage: "dacli runs prune [--keep N]", Run: cmdRunsPrune},
	{Path: "agents", Brief: "Live agents or bounded sourced worker history", JSON: true, Usage: "dacli agents [--project slug] [--active-only | --history] [--limit N] [--cursor RUN_ID] [--max-rss MB] [--max-runtime DUR] [--reap] [--tail]", Run: cmdAgents},
	{Path: "logs", Brief: "Read a bounded transcript chunk or follow it as it streams", JSON: true, Usage: "dacli logs <run-id-prefix|child-id> [-f] [--tail N] [--cursor BYTE] [--limit BYTES] [--full]", Run: cmdLogs},
	{Path: "kill", Brief: "Terminate an agent and its ENTIRE process tree (SIGTERM→SIGKILL); reaps runaways", Mutates: true, Usage: "dacli kill <run-id-prefix | child-id> [--grace sec]  |  dacli kill --all", Run: cmdKill},
}

func contextContract(user, repo, skills, plugins, mcp, env store.ContextCapability) map[store.ContextClass]store.ContextCapability {
	return map[store.ContextClass]store.ContextCapability{
		store.ContextUserConfig: user, store.ContextRepoInstructions: repo,
		store.ContextGlobalSkills: skills, store.ContextPlugins: plugins,
		store.ContextMCP: mcp, store.ContextEnvironment: env,
	}
}

// presets are shipped adapters. Their flags are ASSUMPTIONS, recorded as
// such in the adapter body, to be corrected by `runtime doctor` on a machine
// where the binary exists.
var presets = map[string]store.Runtime{
	"claude-code": {
		Name: "claude-code", Harness: "claude", Binary: "claude", Mode: "arg", Flag: "-p",
		// Read-only means read tools plus Bash scoped to the dacli binary —
		// plan mode would mute the child entirely (no Bash = no reporting).
		SandboxRO: []string{"--allowedTools", "Read,Grep,Glob,LS,Bash(dacli:*)"},
		// Deliberately NO ANTHROPIC_API_KEY: children run as the user's own
		// Claude Code login (keychain via HOME/USER), never API billing. If
		// that variable leaked through, billing would silently flip.
		Env:             []string{"HOME", "PATH", "USER", "LOGNAME", "TMPDIR"},
		ModelFlag:       "--model", // role-level cost routing: reviewer=opus, junior=haiku
		SkillsNativeDir: ".claude/skills",
		// Defaults ON: without it, `agents --tail` reads a transcript that
		// stays empty until the child exits (claude buffers stdout under
		// --print), and calibration gets no usage actuals at all (§ 23).
		UsageFormat:         "stream-json",
		BehavioralPreflight: store.BehavioralPreflightClaudePrintV1,
		Context:             contextContract(store.ContextEnumerated, store.ContextEnumerated, store.ContextEnumerated, store.ContextEnumerated, store.ContextEnumerated, store.ContextIsolated),
	},
	// claude-code-rw is the write-capable counterpart. The stock claude-code
	// preset only declares SandboxRO, so an rw spawn on it was refused with
	// "grants no write tool" and no obvious remedy — the operator had to
	// hand-build a second runtime with the right --allowedTools list to do the
	// most ordinary thing dacli does, which is run an implementer (task 309,
	// issue #382 item 7).
	//
	// The allowlist is the read set plus Edit/Write and the two commands an
	// implementer cannot work without: git for its own branch and commits, and
	// the dacli binary for claiming, checking and reporting. Everything else
	// stays out; widen it per-runtime, deliberately, rather than here.
	"claude-code-rw": {
		Name: "claude-code-rw", Harness: "claude", Binary: "claude", Mode: "arg", Flag: "-p",
		Args: []string{"--allowedTools", "Read,Grep,Glob,LS,Edit,Write,Bash(git:*),Bash(dacli:*)"},
		// Same read-only list as claude-code, so one runtime can serve both
		// grants: a ro spawn on this adapter is still sandboxed to reads.
		SandboxRO:           []string{"--allowedTools", "Read,Grep,Glob,LS,Bash(dacli:*)"},
		Env:                 []string{"HOME", "PATH", "USER", "LOGNAME", "TMPDIR"},
		ModelFlag:           "--model",
		SkillsNativeDir:     ".claude/skills",
		UsageFormat:         "stream-json",
		BehavioralPreflight: store.BehavioralPreflightClaudePrintV1,
		Context:             contextContract(store.ContextEnumerated, store.ContextEnumerated, store.ContextEnumerated, store.ContextEnumerated, store.ContextEnumerated, store.ContextIsolated),
	},
	"generic-exec": {
		Name: "generic-exec", Harness: "generic", Binary: "", Mode: "stdin",
		Env:     []string{"HOME", "PATH"},
		Context: contextContract(store.ContextUnsupported, store.ContextEnumerated, store.ContextUnsupported, store.ContextUnsupported, store.ContextUnsupported, store.ContextUnsupported),
	},
	"codex": {
		Name: "codex", Harness: "codex", Binary: "codex", Mode: "stdin",
		GlobalArgs: []string{"--ask-for-approval", "never"},
		Args:       []string{"exec", "--json", "--ephemeral", "--color", "never", "--sandbox", "read-only"},
		SandboxRO:  []string{"--sandbox", "read-only"},
		Env:        []string{"HOME", "PATH", "USER", "LOGNAME", "TMPDIR", "CODEX_HOME"},
		ModelFlag:  "--model", UsageFormat: "codex-jsonl",
		BehavioralPreflight: store.BehavioralPreflightCodexExecJSONV2,
		Context:             contextContract(store.ContextEnumerated, store.ContextEnumerated, store.ContextEnumerated, store.ContextEnumerated, store.ContextEnumerated, store.ContextIsolated),
	},
	"codex-rw": {
		Name: "codex-rw", Harness: "codex", Binary: "codex", Mode: "stdin",
		GlobalArgs: []string{"--ask-for-approval", "never"},
		Args:       []string{"exec", "--json", "--ephemeral", "--color", "never", "--sandbox", "workspace-write"},
		SandboxRO:  []string{"--sandbox", "read-only"},
		Env:        []string{"HOME", "PATH", "USER", "LOGNAME", "TMPDIR", "CODEX_HOME"},
		ModelFlag:  "--model", UsageFormat: "codex-jsonl",
		BehavioralPreflight: store.BehavioralPreflightCodexExecJSONV2,
		Context:             contextContract(store.ContextEnumerated, store.ContextEnumerated, store.ContextEnumerated, store.ContextEnumerated, store.ContextEnumerated, store.ContextIsolated),
	},
	"gemini": {
		Name: "gemini", Harness: "gemini", Binary: "gemini", Mode: "arg", Flag: "-p",
		SandboxRO:       []string{"--approval-mode", "plan"},
		Env:             []string{"HOME", "PATH", "USER", "LOGNAME", "TMPDIR"},
		ModelFlag:       "--model",
		SkillsNativeDir: ".gemini/skills",
		UsageFormat:     "gemini-stream-json",
		Context:         contextContract(store.ContextEnumerated, store.ContextEnumerated, store.ContextEnumerated, store.ContextEnumerated, store.ContextEnumerated, store.ContextIsolated),
	},
	"gemini-rw": {
		Name: "gemini-rw", Harness: "gemini", Binary: "gemini", Mode: "arg", Flag: "-p",
		// auto_edit permits workspace edits but does not silently approve every
		// shell command or MCP call. That boundary is why this is not --yolo.
		Args:            []string{"--approval-mode", "auto_edit"},
		SandboxRO:       []string{"--approval-mode", "plan"},
		Env:             []string{"HOME", "PATH", "USER", "LOGNAME", "TMPDIR"},
		ModelFlag:       "--model",
		SkillsNativeDir: ".gemini/skills",
		UsageFormat:     "gemini-stream-json",
		Context:         contextContract(store.ContextEnumerated, store.ContextEnumerated, store.ContextEnumerated, store.ContextEnumerated, store.ContextEnumerated, store.ContextIsolated),
	},
	"copilot": {
		Name: "copilot", Harness: "copilot", Binary: "copilot", Mode: "arg", Flag: "-p",
		SandboxRO:   []string{"--deny-tool", "write", "--deny-tool", "shell"},
		Env:         []string{"HOME", "PATH", "USER", "LOGNAME", "TMPDIR"},
		ModelFlag:   "--model",
		UsageFormat: "copilot-json",
		Context:     contextContract(store.ContextEnumerated, store.ContextEnumerated, store.ContextUnsupported, store.ContextEnumerated, store.ContextEnumerated, store.ContextIsolated),
	},
	"copilot-rw": {
		Name: "copilot-rw", Harness: "copilot", Binary: "copilot", Mode: "arg", Flag: "-p",
		// There is deliberately no --allow-all-tools. The child may edit and
		// run only the two command families needed by the dacli work loop.
		Args:        []string{"--allow-tool", "write", "--allow-tool", "shell(git:*)", "--allow-tool", "shell(dacli:*)"},
		SandboxRO:   []string{"--deny-tool", "write", "--deny-tool", "shell"},
		Env:         []string{"HOME", "PATH", "USER", "LOGNAME", "TMPDIR"},
		ModelFlag:   "--model",
		UsageFormat: "copilot-json",
		Context:     contextContract(store.ContextEnumerated, store.ContextEnumerated, store.ContextUnsupported, store.ContextEnumerated, store.ContextEnumerated, store.ContextIsolated),
	},
}

func claimTask(ctx *clikit.Ctx, w *workspace.Workspace, t *store.Task, childID string) {
	err := store.WithTask(w, t, func(fresh *store.Task) error {
		if store.ClaimedBy(fresh) == childID {
			return nil
		}
		store.AppendLog(fresh, "claimed by "+childID)
		return store.SaveTask(fresh)
	})
	if err != nil {
		fmt.Fprintf(ctx.Stderr, "warning: could not stamp claim on task %03d-%s: %v\n", t.Seq, t.Slug, err)
	}
}

func contextInvocation(role team.Role, hasRole, override bool, sources []store.ContextSource) string {
	skills := "-"
	if hasRole && len(role.Skills) > 0 {
		skills = strings.Join(role.Skills, ",")
	}
	lines := []string{"declared_role_skills: " + skills, fmt.Sprintf("external_context_override: %t", override)}
	if len(sources) == 0 {
		lines = append(lines, "external_context_sources: -")
	}
	for _, source := range sources {
		lines = append(lines, fmt.Sprintf("external_context_source: %s=%s", source.Class, source.Path))
	}
	return strings.Join(lines, "\n") + "\n"
}

// --- The shared pre-launch path ---
//
// Every command that puts a brief in front of a runtime and starts a child —
// `spawn` and `supervise` — takes the SAME prologue: resolve role/runtime/
// grant/band, run every governance gate, build the sandbox. It is deliberately
// ONE function over ONE gate list, not a convention two commands are trusted to
// follow: `supervise` used to reimplement the prologue by hand and silently
// dropped four gates (role WIP, taint blast-radius, --max-tokens, --claim
// overlap), so a brief that `spawn` refused could be pushed through the same
// runtime for up to --max-turns turns just by typing the other verb.
//
// Adding a gate therefore means adding an entry to launchGates, which is the
// only place a gate CAN be added — there is no per-command prologue left to add
// it to, and no per-command gate to forget.
//
// What genuinely stays per-command is the part AFTER the gates: `spawn` owns
// --detach and --worktree (a single run, optionally backgrounded or isolated in
// its own git worktree); `supervise` owns --max-turns and the evaluate-correct
// loop. Those are execution shapes, not policy decisions, so they are not gates
// and are not shared.

// launchPlan is everything decided before an identity is minted or a process
// starts. Both commands build it via resolveLaunch and read their settings from
// it rather than re-deriving any of them.
type launchPlan struct {
	Task                 *store.Task
	TaskRef              string
	RoleName             string
	Role                 team.Role
	HasRole              bool
	Grant                model.Grant
	Model                string
	Runtime              store.Runtime
	Band                 store.Band
	Claims               []string
	Sandbox              []string
	Budget               int
	Timeout              int
	ContextSources       []store.ContextSource
	ContextOverride      bool
	MutationCapabilities []mutationCapabilityResult
	PlannedHandoffs      []string
	ProbeWorkDir         string
	LaunchContract       store.RuntimeLaunchContract

	w *workspace.Workspace
	f *clikit.Flags
}

// launchGate is one pre-launch policy decision: it reads the resolved plan and
// either passes or REFUSES (exit 3). A gate never mutates the plan and never
// starts anything, so every refusal below lands before any process exists.
type launchGate struct {
	Name  string
	Check func(ctx *clikit.Ctx, p *launchPlan) error
}

// launchGates is THE gate list. resolveLaunch runs it in order for every
// spawning command; nothing else enforces a spawn-time policy.
var launchGates = []launchGate{
	{"role-wip", gateRoleWIP},
	{"seniority", gateSeniority},
	{"phase", gatePhase},
	{"token-budget", gateTokenBudget},
	{"taint", gateTaint},
	{"claim-overlap", gateClaimOverlap},
}

// launchFlags are the flags the shared prologue itself reads. EVERY spawning
// command must accept them: a command whose flag set omits --claim or
// --max-tokens cannot be gated by them at all, and one that omits --force
// cannot be un-gated deliberately, so a missing flag is a missing gate by
// another route. Commands add their own flags on top via launchFlagsWith.
var launchFlags = []string{
	"task", "runtime", "role", "grant", "model", "harness",
	"claim", "budget", "max-tokens", "timeout",
	"cooperative", "allow-user-config", "advise", "force", "allow-advisory-tokens",
}

// launchFlagsWith returns the shared flag set plus a command's own flags,
// copying so no caller can append into the package-level slice.
func launchFlagsWith(extra ...string) []string {
	return append(append([]string{}, launchFlags...), extra...)
}

// errAdviseOnly is the sentinel resolveLaunch returns when `--advise` fired:
// the advisory was printed and the launch must NOT proceed. Callers unwrap it to
// a clean exit-0 preview (dacli 232) — `--advise` previews on `spawn`/`supervise`
// exactly as it does on `loop`, so a flag named "advise" never launches a run.
var errAdviseOnly = errors.New("advise-only: previewed, nothing launched")

// resolveLaunch turns flags into a fully-gated launchPlan: role defaults,
// runtime resolution, the advisory readout, every gate in launchGates, and the
// sandbox. It returns only plans that are cleared to launch. A `--advise` preview
// short-circuits with errAdviseOnly before any gate runs — nothing is minted.
func resolveLaunch(ctx *clikit.Ctx, w *workspace.Workspace, f *clikit.Flags, taskRef string) (*launchPlan, error) {
	t, err := store.FindTask(w, taskRef)
	if err != nil {
		return nil, err
	}
	// Role defaults: grant, runtime routing, model tier. The role's WIP and
	// seniority/phase caps are enforced below as gates, not here.
	p := &launchPlan{
		Task: t, TaskRef: taskRef, w: w, f: f,
		RoleName: f.Get("role"),
		Grant:    model.Grant(f.Get("grant")),
		Model:    f.Get("model"),
	}
	rtName := f.Get("runtime")
	if role, ok := store.LoadRole(w, p.RoleName); ok {
		p.Role, p.HasRole = role, true
		if p.Grant == "" && role.Grant != "" {
			p.Grant = model.Grant(role.Grant)
		}
		if rtName == "" {
			rtName = role.Runtime
		}
		if p.Model == "" {
			p.Model = role.Model
		}
	}
	if p.Grant == "" {
		p.Grant = model.GrantRO
	}
	if rtName == "" {
		return nil, clikit.Usagef("no runtime: pass --runtime or set `runtime:` on the role")
	}
	// An explicit --runtime is an operator choice and is never substituted.
	// Role routing may use only that role's named, ordered fallback chain.
	limits := store.LoadRuntimeLimits(w)
	if cooldown, open, limitErr := limits.Open(rtName); limitErr != nil {
		return nil, fmt.Errorf("runtime %s cooldown: %w", rtName, limitErr)
	} else if open {
		remaining := time.Until(cooldown.Until)
		if remaining < 0 {
			remaining = 0
		}
		destination := ""
		if f.Get("runtime") == "" && p.HasRole && (providerpolicy.Outcome{Kind: cooldown.Kind}).Fallbackable() {
			roles, loadErr := store.LoadRoles(w)
			if loadErr != nil {
				return nil, loadErr
			}
			source := p.Role
			source.Grant = string(p.Grant)
			allowedHarnesses := f.All("harness")
			allowedFallback := func(role team.Role) bool {
				if len(allowedHarnesses) == 0 {
					return true
				}
				rt, loadErr := store.LoadRuntime(w, role.Runtime)
				return loadErr == nil && slices.Contains(allowedHarnesses, rt.Harness)
			}
			if fallback, _, ok, selectErr := store.SelectFallbackMatching(source, roles, limits, allowedFallback); selectErr != nil {
				return nil, selectErr
			} else if ok {
				destination = fallback.Runtime
				p.Role, p.HasRole, p.RoleName = fallback, true, fallback.Name
				rtName = fallback.Runtime
				if f.Get("grant") == "" && fallback.Grant != "" {
					p.Grant = model.Grant(fallback.Grant)
				}
				if f.Get("model") == "" {
					p.Model = fallback.Model
				}
			}
		}
		transition := providerpolicy.Transition{Source: cooldown.Runtime, Destination: destination, Reason: cooldown.Reason, Cooldown: remaining}
		if reportErr := limits.Report(ctx.Stdout, transition); reportErr != nil {
			return nil, reportErr
		}
		if destination == "" {
			return nil, clikit.Refusedf("runtime %s is paused for %s (%s)", cooldown.Runtime, remaining, cooldown.Reason)
		}
	}
	rt, err := store.LoadRuntime(w, rtName)
	if err != nil {
		return nil, err
	}
	path, err := exec.LookPath(rt.Binary)
	if err != nil {
		return nil, fmt.Errorf("runtime %s: binary %q not on PATH — `dacli runtime doctor`", rt.Name, rt.Binary)
	}
	rt = store.HydrateRuntimeROProbe(w, rt, path)
	p.Runtime = rt
	if allowed := f.All("harness"); len(allowed) > 0 && !slices.Contains(allowed, rt.Harness) {
		return nil, clikit.Refusedf("runtime %s belongs to harness %s, outside the allowed harness policy %s; choose a compatible role/runtime or explicitly authorize a hybrid harness set", rt.Name, rt.Harness, strings.Join(allowed, ","))
	}
	// The band is built in the SAME recorded form invocation.txt uses (OrDash
	// for an empty role/model, rt.Name for runtime) so it matches the bands
	// store.CalibrationSamples joins back from the run records.
	p.Band = store.Band{Role: clikit.OrDash(p.RoleName), Model: clikit.OrDash(p.Model), Runtime: rt.Name}
	// Issue #794: Get returns only the final occurrence. Claims are repeatable
	// scope declarations, so losing an earlier value makes the run record lie
	// and causes dacli commit to refuse work the operator explicitly claimed.
	p.Claims = splitClaimValues(f.All("claim"))
	if p.Grant == model.GrantRW && len(p.Claims) == 0 {
		p.Claims = taskWriteClaims(w, t)
	}
	if p.Budget, err = f.IntAliased(0, "brief-tokens", "budget"); err != nil {
		return nil, err
	}
	p.Timeout = 300
	if n, err := f.Int("timeout", 0); err != nil {
		return nil, err
	} else if n > 0 {
		p.Timeout = n
	}

	// --advise: with role/model/runtime/task resolved but BEFORE any identity is
	// minted or process launched, surface what the log already knows at the launch
	// decision — a calibrated sizing for this agent band and this task's taint
	// status — then STOP. `--advise` previews and never acts, identically on
	// `spawn`, `supervise` and `loop` (dacli 232): a flag whose name promises a
	// preview must not silently launch a real run and bill the operator for a
	// spawn they only meant to price. The gates below and the launch never run;
	// the caller unwraps errAdviseOnly to a clean exit-0 preview.
	if f.Bool("advise") {
		printAdvisory(ctx, w, t, p.Band, p.Claims)
		if err := printTokenCeiling(ctx, p, true); err != nil {
			return nil, err
		}
		return nil, errAdviseOnly
	}

	for _, g := range launchGates {
		if err := g.Check(ctx, p); err != nil {
			return nil, err
		}
	}

	// dacli 272: report every preflight mismatch (binary-allowlist,
	// prompt-tools; grant-write is reported here too but sandboxFor below
	// still owns the actual refusal, unchanged) BEFORE sandboxFor can refuse
	// and return early — otherwise a grant-write refusal would hide a
	// binary-allowlist or prompt-tools warning nobody would ever see.
	exe, _ := os.Executable() // "" on error; preflightIssues skips class 2 then, same as the old warnExeAllowlist
	override := f.Bool("cooperative") || f.Bool("allow-user-config")
	for _, iss := range preflightIssues(rt, p.Role, p.HasRole, p.Grant, f.Bool("cooperative"), exe) {
		if !iss.refuse {
			fmt.Fprintf(ctx.Stderr, "warning: %s\n", iss.message)
		}
	}
	contextMismatches, sources := contextIssues(rt, p.Role, p.HasRole, override, ctx.Cwd, currentEnvNames())
	var refused []string
	for _, iss := range contextMismatches {
		if iss.refuse {
			refused = append(refused, iss.message)
		} else {
			fmt.Fprintf(ctx.Stderr, "warning: allowing external context: %s\n", iss.message)
		}
	}
	if len(refused) > 0 {
		return nil, clikit.Refusedf("%s; pass --allow-user-config (or --cooperative) to record and allow the exception", strings.Join(refused, "; "))
	}
	p.ContextSources, p.ContextOverride = sources, override

	sandbox, err := sandboxFor(ctx, rt, p.Grant, f.Bool("cooperative"))
	if err != nil {
		return nil, err
	}
	p.Sandbox = sandbox
	// Issue #746: this remains inside resolveLaunch, before identity minting,
	// task claims, run records, and worktrees. A local sandbox flag probe alone
	// did not prove Codex could initialize the exact app-server transport.
	resultChannel := store.CommandResultChannel
	if f.Bool("structured-review-result") {
		if p.Grant != model.GrantRO {
			return nil, clikit.Refusedf("independent structured review requires an enforced read-only grant, got %s", p.Grant)
		}
		if f.Bool("cooperative") {
			return nil, clikit.Refusedf("independent structured review cannot use cooperative read-only; use a runtime with a verified read-only sandbox")
		}
		if !store.RuntimeEnforcesRO(rt) {
			return nil, clikit.Refusedf("independent structured review requires a verified read-only sandbox on runtime %s", rt.Name)
		}
		resultChannel = store.IndependentReviewChannel
	}
	p.LaunchContract, err = requireLaunchCompatibility(ctx, w, rt, path, p.Grant, p.Model, override, sandbox, resultChannel, f.Get("preflight-fingerprint"))
	if err != nil {
		return nil, err
	}
	// Resolve an already-existing assignment checkout for concrete mutation
	// probes. A requested new --worktree does not exist yet; its creation is
	// still proven later by git, so preflight conservatively probes the main
	// checkout rather than inventing success for a path that cannot be opened.
	p.ProbeWorkDir = w.Root
	if candidate, _, resolveErr := resolveSpawnWorkDir(w, t, ctx.Cwd, f.Bool("worktree")); resolveErr != nil {
		return nil, resolveErr
	} else if info, statErr := os.Stat(candidate); statErr == nil && info.IsDir() {
		p.ProbeWorkDir = candidate
	}
	// Issue #874: prove the resolved assignment can perform its required
	// mutations before an identity, worktree, or paid worker exists. Publication
	// capabilities may be intentionally delegated to root; source writes and
	// declared verification tools may not.
	mutationResults, err := mutationPreflight(p)
	if err != nil {
		return nil, err
	}
	p.MutationCapabilities = mutationResults
	p.PlannedHandoffs = plannedHandoffCapabilities(mutationResults)
	for _, result := range mutationResults {
		if result.Disposition == "planned_handoff" {
			fmt.Fprintf(ctx.Stderr, "planned root handoff: %s (%s): %s\n", result.Capability, result.Class, result.Detail)
		}
	}
	return p, nil
}

// exeAllowlistWarning is dacli 267's binary-allowlist check (class 2 of dacli
// 272's preflight, via preflightIssues): it builds the args the child
// actually runs with (invoke args plus the ro sandbox that was applied) and
// reports the warning text, if any. sandbox is empty for an rw grant, so an
// rw child on cc-rw is checked against its invoke_args allowlist; an ro child
// is checked against that plus its sandbox_ro_args.
func exeAllowlistWarning(rt store.Runtime, sandbox []string, exe string) (string, bool) {
	args := append(append([]string{}, rt.Args...), sandbox...)
	ok, allowlisted := store.RuntimeAllowsDacli(args, exe)
	if ok {
		return "", false
	}
	return fmt.Sprintf("warning: runtime %s allowlists the dacli binary at %s, but this dacli is %s — a headless child cannot run the dacli path its own preamble names (dacli 267); re-run `go install ./cmd/dacli` so ~/go/bin/dacli matches, or update the runtime's allowlist\n",
		rt.Name, strings.Join(allowlisted, ", "), exe), true
}

// gateRoleWIP enforces the role's work-in-progress cap. WIP counts LIVE agents
// holding the role, so it binds anything that mints one — a supervised run
// occupies a slot for its whole loop exactly as a spawn does.
func gateRoleWIP(_ *clikit.Ctx, p *launchPlan) error {
	if !p.HasRole || p.Role.WIP <= 0 {
		return nil
	}
	active, err := store.ActiveInRole(p.w, p.RoleName)
	if err != nil {
		// Cannot rule out the role already being at its cap, so this must not
		// read as "nobody holds the role" and wave the spawn through (dacli
		// 341): fail closed, the same rule gateClaimOverlap already holds
		// itself to for the runs dir (dacli 337, "a gate must never certify
		// what it could not read").
		return fmt.Errorf("cannot check role %s WIP: %w", p.RoleName, err)
	}
	if active >= p.Role.WIP {
		// Name the way out. A bare "at its WIP limit" left an operator staring
		// at `dacli agents` reporting nobody live, with no stated path from
		// refusal to running — its sibling refusals all name theirs (task 295).
		return clikit.Refusedf("role %s is at its WIP limit (%d/%d) — `dacli agents` shows the live runs consuming capacity; stop or finish one, or raise the cap: `dacli role bump %s --wip %d`",
			p.RoleName, active, p.Role.WIP, p.RoleName, p.Role.WIP+1)
	}
	return nil
}

func gateSeniority(_ *clikit.Ctx, p *launchPlan) error {
	if !p.HasRole {
		return nil
	}
	return seniorityGate(p.Role, p.Task)
}

func gatePhase(_ *clikit.Ctx, p *launchPlan) error {
	if !p.HasRole {
		return nil
	}
	return phaseGate(p.w, p.Task, p.Role)
}

// gateTokenBudget makes --max-tokens a runtime-enforced launch contract.
// Calibration predicts whether the task is likely to fit; only an adapter's
// declared TokenLimitFlag can impose the actual ceiling. Unsupported runtimes
// fail closed unless the operator explicitly chooses advisory-only accounting.
//
// For `supervise` the figure is the cost of ONE turn, because the band's
// measured cost is per-run and the loop re-sends the brief once per turn: a
// plan that already busts the budget on a single turn cannot possibly fit the
// loop. Scaling it by --max-turns would refuse on a cost that is never incurred
// when the task is accepted on turn 1, so the single-turn figure is the honest
// bound to gate on.
func gateTokenBudget(ctx *clikit.Ctx, p *launchPlan) error {
	return printTokenCeiling(ctx, p, false)
}

func printTokenCeiling(ctx *clikit.Ctx, p *launchPlan, preview bool) error {
	maxTok, set, err := requestedTokenLimit(p)
	if err != nil || !set {
		return err
	}
	enforced := p.Runtime.TokenLimitFlag != ""
	if !enforced && !p.f.Bool("allow-advisory-tokens") && !preview {
		return clikit.Refusedf("runtime %s cannot enforce --max-tokens %d: its adapter declares no token-limit flag — choose a capable runtime or pass --allow-advisory-tokens to launch with accounting that is explicitly advisory only", p.Runtime.Name, maxTok)
	}
	if enforced {
		fmt.Fprintf(ctx.Stderr, "token ceiling: ENFORCED by runtime %s via %s %d\n", p.Runtime.Name, p.Runtime.TokenLimitFlag, maxTok)
	} else if preview && !p.f.Bool("allow-advisory-tokens") {
		fmt.Fprintf(ctx.Stderr, "token ceiling: UNSUPPORTED by runtime %s — launch with --max-tokens %d would refuse; --allow-advisory-tokens explicitly selects advisory-only accounting\n", p.Runtime.Name, maxTok)
	} else {
		fmt.Fprintf(ctx.Stderr, "warning: token ceiling: ADVISORY ONLY — runtime %s cannot enforce %d tokens; launch allowed by explicit --allow-advisory-tokens\n", p.Runtime.Name, maxTok)
	}
	expected, n, ok := bandTokenBudget(p.w, p.Task, p.Band)
	switch {
	case !ok:
		fmt.Fprintf(ctx.Stderr, "calibrated estimate: unavailable for band %s (no measured token cost yet or task unestimated)\n", p.Band)
	case expected <= float64(maxTok):
		fmt.Fprintf(ctx.Stderr, "calibrated estimate: ~%.0f tokens within ceiling %d (n=%d)\n", expected, maxTok, n)
	case n < 10:
		fmt.Fprintf(ctx.Stderr, "calibrated estimate: PROVISIONAL ~%.0f tokens exceeds ceiling %d (n=%d < 10); the task may not fit\n", expected, maxTok, n)
	default:
		fmt.Fprintf(ctx.Stderr, "warning: calibrated estimate ~%.0f tokens exceeds ceiling %d (n=%d); the task may not fit\n", expected, maxTok, n)
	}
	return nil
}

func requestedTokenLimit(p *launchPlan) (int, bool, error) {
	maxStr := p.f.Get("max-tokens")
	if maxStr == "" {
		return 0, false, nil
	}
	maxTok, err := strconv.Atoi(maxStr)
	if err != nil || maxTok <= 0 {
		return 0, false, clikit.Usagef("--max-tokens takes a positive integer")
	}
	return maxTok, true, nil
}

func tokenLimitArgs(p *launchPlan) []string {
	maxTok, set, err := requestedTokenLimit(p)
	if err != nil || !set || p.Runtime.TokenLimitFlag == "" {
		return nil
	}
	return []string{p.Runtime.TokenLimitFlag, strconv.Itoa(maxTok)}
}

func tokenLimitMode(p *launchPlan) string {
	if p.f.Get("max-tokens") == "" {
		return "unset"
	}
	if p.Runtime.TokenLimitFlag != "" {
		return "runtime-enforced"
	}
	return "advisory-only"
}

// gateTaint is the launch-time taint gate (D3): --advise DISPLAYS taint status;
// here it BLOCKS. If this task's brief sits in an external source's blast
// radius, refuse (exit 3) rather than feed a possibly-injected brief to a fresh
// child — taint stops being an audit query you run after the fact and becomes a
// gate at the point of consumption (RUNTIMES §18, cross-tree injection).
// --force (or --cooperative) is the explicit, loud override: the operator has
// read the origins and accepts the risk. A supervised run consumes that same
// brief once per turn, so it is if anything the more exposed of the two.
func gateTaint(_ *clikit.Ctx, p *launchPlan) error {
	origins, inRadius, _ := externalRadius(p.w, p.Task)
	if inRadius && !(p.f.Bool("force") || p.f.Bool("cooperative")) {
		return clikit.Refusedf("task %03d-%s is in the blast radius of %s — its brief may carry injected content (RUNTIMES §18); audit the origins, then re-run with --force to spawn anyway",
			p.Task.Seq, p.Task.Slug, strings.Join(origins, ", "))
	}
	return nil
}

// gateClaimOverlap enforces the --claim disjointness that keeps parallel
// branches merge-clean: if a live agent already claims an overlapping tree,
// refuse instead of hoping the two never collide.
func gateClaimOverlap(_ *clikit.Ctx, p *launchPlan) error {
	if len(p.Claims) == 0 {
		return nil
	}
	live, err := liveAgents(p.w)
	if err != nil {
		// Cannot rule out a clash, so this must not read as "nobody is
		// working" and wave the spawn through (dacli 337): fail closed, the
		// same rule internal/gates already holds live agents to for its own
		// quantifier gates ("a gate must never certify what it could not
		// read").
		return fmt.Errorf("cannot check for a claim overlap: %w", err)
	}
	for _, other := range live {
		if mine, theirs, clash := procmon.PathsOverlap(p.Claims, other.Claims); clash {
			return clikit.Refusedf("path-claim conflict: live agent %s already claims %q and you claim %q — narrow your scope, or `dacli wait %s` first",
				other.Child, theirs, mine, other.RunID[:min(10, len(other.RunID))])
		}
	}
	return nil
}

func cmdSpawn(ctx *clikit.Ctx, args []string) (retErr error) {
	w, id, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	f, _ := clikit.ParseFlags(args)
	if err := f.Reject(launchFlagsWith("worktree", "detach", "pr", "review", "structured-review-result", "preflight-fingerprint", "pr-number")...); err != nil {
		return err
	}
	if f.Bool("structured-review-result") && !f.Bool("review") {
		return clikit.Usagef("--structured-review-result requires --review")
	}
	taskRef := f.Get("task")
	if taskRef == "" {
		return clikit.Usagef("usage: dacli spawn --task <ref> [--runtime name] [--role r] [--grant ro|rw] [--model m] [--harness family]... [--worktree] [--detach] [--claim path,path] [--pr] [--review [--structured-review-result] [--preflight-fingerprint sha256:...] [--pr-number N]] [--budget N] [--max-tokens N [--allow-advisory-tokens]] [--timeout sec] [--cooperative|--allow-user-config] [--advise] [--force]")
	}
	plan, err := resolveLaunch(ctx, w, f, taskRef)
	if errors.Is(err, errAdviseOnly) {
		return nil // --advise previewed and stopped; nothing spawned (dacli 232)
	}
	if err != nil {
		return err
	}
	t, rt := plan.Task, plan.Runtime
	if err := validateReviewTarget(w, t, f); err != nil {
		return err
	}
	if _, willIsolate, resolveErr := resolveSpawnWorkDir(w, t, ctx.Cwd, f.Bool("worktree")); resolveErr != nil {
		return resolveErr
	} else if plan.Grant == model.GrantRW && willIsolate && len(plan.Claims) == 0 {
		return clikit.Refusedf("isolated writable task %s has no exact path claim; record a concrete task claim before launching", t.ID)
	}
	roleName, modelName, grant := plan.RoleName, plan.Model, plan.Grant
	claims, sandboxArgs := plan.Claims, plan.Sandbox
	budget, timeout := plan.Budget, plan.Timeout

	childID, token, err := agentid.Spawn(w, id, roleName, grant)
	if err != nil {
		return err
	}
	prelaunch, err := beginPrelaunchRun(ctx, w, t, childID, roleName, rt.Name, timeout, claims)
	if err != nil {
		return err
	}
	defer prelaunch.finalize(&retErr)
	runID, runDir, record := prelaunch.runID, prelaunch.runDir, prelaunch.record
	// Stamp the claim now that the child id is minted: this is the span start
	// calibrate joins run actuals against (D1). Idempotent — a re-spawn respects
	// the existing claim.
	claimTask(ctx, w, t, childID)

	b, err := brief.Assemble(w, taskRef, brief.Options{Budget: budget, Role: roleName})
	if err != nil {
		return err
	}
	suffix, err := promptSuffix(w, f, t, childID, grant)
	if err != nil {
		return err
	}
	prompt := b.Render() + suffix

	// The run record: what was this agent told, exactly (PROPOSALS P3).
	if raw, marshalErr := json.MarshalIndent(plan.LaunchContract, "", "  "); marshalErr == nil {
		record.bestEffort("launch-contract.json", string(raw)+"\n")
	}
	if len(plan.PlannedHandoffs) > 0 {
		if err := record.critical("planned-handoffs.txt", strings.Join(plan.PlannedHandoffs, "\n")+"\n"); err != nil {
			return err
		}
	}
	if err := record.critical("brief.md", prompt); err != nil {
		return err
	}

	invocation := fmt.Sprintf("run: %s\ntask: %s\nchild: %s\nrole: %s\nmodel: %s\ngrant: %s\nruntime: %s\nbinary: %s\nenv_names: %s\nbudget: %d (recorded, not enforced: runtime reports no usage)\nmax_tokens: %s\nmax_tokens_mode: %s\ntimeout_s: %d\n",
		runID, t.ID, childID, clikit.OrDash(roleName), clikit.OrDash(modelName), grant, rt.Name, rt.Binary,
		strings.Join(append([]string{agentid.EnvVar}, rt.Env...), ","), budget, f.Get("max-tokens"), tokenLimitMode(plan), timeout)
	invocation += contextInvocation(plan.Role, plan.HasRole, plan.ContextOverride, plan.ContextSources)
	invocation += mutationPreflightInvocation(plan.MutationCapabilities)

	// --worktree isolates this child in its own git worktree + branch, so
	// several children spawned in parallel never clobber each other's working
	// tree. The child edits CODE there; workspace.Find redirects its dacli
	// state (identity, tasks, events) to the shared root, so the child sees its
	// own freshly-minted identity and can self-commit, self-check, self-report
	// — no shadow .dacli. Its events therefore land in the shared root, which
	// is exactly where we read them back from below.
	workDir, isolatedWorktree, err := resolveSpawnWorkDir(w, t, ctx.Cwd, f.Bool("worktree"))
	if err != nil {
		return err
	}
	if !isolatedWorktree {
		// Without --worktree the child works in the MAIN checkout, and its
		// protocol tells it to branch — so the main tree's HEAD moves off
		// trunk and stays there. On a fresh repo that also means trunk is
		// never established at all, and `ship` later reports "integrated 0
		// task(s)" as if nothing needed doing, with nowhere to integrate INTO
		// (issue #382 item 3). Say it here, where it is still cheap to fix,
		// rather than leaving the operator to discover a trunkless repo.
		warnMainCheckoutSpawn(ctx, w)
	}
	if f.Bool("worktree") {
		if !gitx.Available() {
			return fmt.Errorf("--worktree needs git on PATH")
		}
		wtPath := w.WorktreePath(t.Project, t.Seq, t.Slug)
		// Pass trunk so a REUSED branch is fast-forwarded before the child sees
		// it. A recurring task keeps its branch across every run, so without
		// this the agent audits a tree as far behind trunk as the task is old
		// and re-reports defects that were fixed long ago (issue #441).
		base, err := store.ResolveTaskWorktreeBase(w, t)
		if err != nil {
			return err
		}
		freshened, err := gitx.AddWorktreeFrom(w.Root, wtPath, fmt.Sprintf("dacli/%03d-%s", t.Seq, t.Slug), base.Commit)
		if err != nil {
			return err
		}
		if freshened {
			fmt.Fprintf(ctx.Stderr, "note: fast-forwarded %s to %s at %s before spawning — it was behind\n",
				fmt.Sprintf("dacli/%03d-%s", t.Seq, t.Slug), base.Branch, base.Commit)
		}
		workDir = wtPath
		record.bestEffort("worktree.txt", wtPath+"\n")
		if raw, marshalErr := json.MarshalIndent(base, "", "  "); marshalErr == nil {
			record.bestEffort("worktree-base.json", string(raw)+"\n")
		}
		// The worktree-sandbox fix: the runtime sandbox allowlists the `dacli`
		// binary at the MAIN checkout's absolute path, so a worktree agent can
		// mistake main for its repo and edit code there — clobbering the main
		// tree and sibling agents. A generic "edit relative to cwd" preamble did
		// not override that signal. State the ACTUAL working directory explicitly
		// and forbid editing outside it; this is what keeps the edits in the
		// worktree. Re-freeze brief.md so the run record matches what was sent.
		prompt += worktreePreamble(wtPath)
		if err := record.critical("brief.md", prompt); err != nil {
			return err
		}
		fmt.Fprintf(ctx.Stderr, "isolated worktree: %s\n", wtPath)
	} else if isolatedWorktree {
		// A follow-up launched from the task's existing checkout must carry the
		// same isolation signals as the run that originally created it. Merely
		// fixing cmd.Dir would leave the prompt pointing at the main checkout and
		// disable the escape check below, recreating issue #673 through a
		// different signal.
		record.bestEffort("worktree.txt", workDir+"\n")
		prompt += worktreePreamble(workDir)
		if err := record.critical("brief.md", prompt); err != nil {
			return err
		}
		fmt.Fprintf(ctx.Stderr, "resuming task worktree: %s\n", workDir)
	}

	providerWorkDir := workDir
	claimSandboxed := false
	if grant == model.GrantRW && isolatedWorktree {
		if len(claims) == 0 {
			return clikit.Refusedf("isolated writable task %s has no exact path claim; record a concrete task claim before launching", t.ID)
		}
		claimPlan, sandboxErr := prepareClaimSandbox(w, runID, workDir, claims, time.Now())
		if sandboxErr != nil {
			return clikit.Refusedf("claim-enforced writable launch refused: %v", sandboxErr)
		}
		providerWorkDir = claimPlan.SandboxDir
		claimSandboxed = true
		claims = claimPlan.Claims
		plan.Claims = append([]string(nil), claims...)
		prompt += fmt.Sprintf("\nCLAIM-ENFORCED ASSIGNMENT CHECKOUT: Work only in %s. This is an independent disposable checkout. The parent will project only exact claimed regular-file additions/modifications into %s; any out-of-claim write, rename, delete, symlink, generated-path escape, or stale base refuses the entire projection. Do not edit the canonical checkout directly.\n", providerWorkDir, workDir)
		if err := record.critical("brief.md", prompt); err != nil {
			return err
		}
		fmt.Fprintf(ctx.Stderr, "claim-enforced assignment checkout: %s\n", providerWorkDir)
	}

	// The break-glass BLOCKED channel (task 269): a plain file the child writes
	// with no dacli, read back by `agents`/`wait`. Appended LAST — after any
	// worktree preamble — so its one carve-out from the worktree rule (this path
	// is the shared record store, not the code tree) is stated last. Re-freeze
	// brief.md so the run record matches exactly what the child was sent.
	prompt += blockedChannelPreamble(filepath.Join(runDir, blockedFileName))
	if grant == model.GrantRW {
		// Keep the structured publication-failure channel after the general
		// worktree and BLOCKED rules so its exact second shared-record path is
		// never contradicted by an earlier "outside the worktree" instruction.
		prompt += handoffChannelPreamble(runDir, plan.PlannedHandoffs)
	}
	if err := record.critical("brief.md", prompt); err != nil {
		return err
	}
	provenance, err := promptInvocation(w.PromptsDir(), prompt)
	if err != nil {
		return err
	}
	if err := record.critical("invocation.txt", invocation+provenance); err != nil {
		return err
	}

	extraArgs := append(append([]string{}, sandboxArgs...), modelArgs(ctx, rt, modelName)...)
	extraArgs = append(extraArgs, tokenLimitArgs(plan)...)
	// A child launched with ungated outward reach is recorded, not merely
	// warned about. The warning at `runtime add` reaches an operator who is
	// present; a loop spawns unattended, so the durable record is the only
	// thing that can answer "who could have done this?" afterwards — which is
	// the question the repo-creation incident left unanswerable (task 308).
	if entry, why := store.UngatedOutwardGrant(append(append([]string{}, rt.Args...), extraArgs...)); entry != "" {
		fmt.Fprintf(ctx.Stderr, "warning: %s runs with %s — %s\n", childID, entry, why)
		_, _ = eventlog.Append(w, id.ID, model.EventFinding, t.Slug, "agent",
			fmt.Sprintf("spawned %s on runtime %s, whose allowlist includes %s — that child can write outward without passing dacli's consent gates", childID, rt.Name, entry))
	}
	fmt.Fprintf(ctx.Stderr, "spawning %s on %s for %03d-%s (run %s)\n", childID, rt.Name, t.Seq, t.Slug, clikit.Short(runID, 10))
	// Register the live process tree so `dacli agents`/`dacli kill` (a separate
	// invocation) can find and reap it while this spawn blocks here.
	var procWriteErr error
	var startedRec procmon.Record
	onStart := func(pid, pgid int) {
		startedRec = procmon.Record{
			RunID: runID, Child: childID, Task: t.ID, Role: roleName, Runtime: rt.Name,
			PID: pid, PGID: pgid, PIDStart: pidStart(pid), Started: time.Now(), Timeout: time.Duration(timeout) * time.Second, Claims: claims,
		}
		procWriteErr = procmon.WriteRecord(filepath.Join(runDir, "proc.txt"), startedRec)
		if procWriteErr != nil {
			terminateRecordedTree(startedRec, 3*time.Second)
		} else {
			prelaunch.pending = false
		}
	}
	transcriptPath := filepath.Join(runDir, "transcript.log")

	// --detach starts the child and returns immediately with a run-id, so an
	// orchestrator can launch many at once and block on them later with
	// `dacli wait` instead of hand-rolling shell backgrounding. The detached
	// child runs in its own process group (still visible to `dacli agents`,
	// killable by `dacli kill`); its outcome is finalized by `dacli wait`.
	if f.Bool("detach") {
		if _, _, derr := execRuntime(providerWorkDir, transcriptPath, rt, prompt, token, extraArgs, timeout, true, onStart); derr != nil {
			return fmt.Errorf("detached spawn failed to start: %w", derr)
		}
		if procWriteErr != nil {
			terminateRecordedTree(startedRec, 3*time.Second)
			return fmt.Errorf("record critical run artifact proc.txt: %w", procWriteErr)
		}
		if err := record.critical("outcome.md", fmt.Sprintf("outcome: running (detached)\nchild: %s\ntask: %s\n", childID, t.ID)); err != nil {
			terminateRecordedTree(startedRec, 3*time.Second)
			return err
		}
		if err := startRunWatchdog(w.Root, runID); err != nil {
			if rec, ok := readProcByRef(w, runID); ok {
				terminateRecordedTree(rec, 3*time.Second)
			}
			return fmt.Errorf("detached spawn watchdog: %w", err)
		}
		fmt.Fprintf(ctx.Stdout, "detached %s on %s for %03d-%s (run %s)\ntrack: dacli agents · block: dacli wait %s · transcript: %s\n",
			childID, rt.Name, t.Seq, t.Slug, clikit.Short(runID, 10), clikit.Short(runID, 10), transcriptPath)
		return nil
	}

	var preSpawnDirty map[string]bool
	if isolatedWorktree {
		if before, derr := gitx.DirtyPaths(w.Root, ".dacli"); derr == nil {
			preSpawnDirty = make(map[string]bool, len(before))
			for _, p := range before {
				preSpawnDirty[p] = true
			}
		}
	}

	elapsed, timedOut, runErr := execRuntime(providerWorkDir, transcriptPath, rt, prompt, token, extraArgs, timeout, false, onStart)
	if procWriteErr != nil {
		return fmt.Errorf("record critical run artifact proc.txt: %w", procWriteErr)
	}
	if runErr != nil && !timedOut {
		if policyErr := recordProviderFailure(ctx, w, rt.Name, transcriptPath, runErr, record.bestEffort); policyErr != nil {
			return policyErr
		}
	}
	if claimSandboxed && runErr == nil && !timedOut {
		if projected, projectionErr := projectAndCommitClaimSandbox(w, runID, t, childID, workDir, time.Now()); projectionErr != nil {
			runErr = errors.Join(runErr, fmt.Errorf("claim sandbox projection: %w", projectionErr))
		} else if len(projected) > 0 {
			fmt.Fprintf(ctx.Stdout, "claim sandbox projected %d path(s): %s\n", len(projected), strings.Join(projected, ", "))
		}
	}

	if isolatedWorktree {
		if leaked, rerr := reclaimMainCheckoutEscape(w.Root, preSpawnDirty); rerr != nil {
			fmt.Fprintf(ctx.Stderr, "warning: could not check main checkout for a worktree escape: %v\n", rerr)
		} else if len(leaked) > 0 {
			fmt.Fprintf(ctx.Stderr, "worktree escape: child %s wrote outside %s into the main checkout — reverted %d path(s): %s\n",
				childID, workDir, len(leaked), strings.Join(leaked, ", "))
			runErr = fmt.Errorf("child wrote outside its worktree into the main checkout (reverted): %s", strings.Join(leaked, ", "))
		}
	}
	if f.Bool("structured-review-result") && runErr == nil {
		if grant != model.GrantRO {
			runErr = clikit.Refusedf("independent review requires a read-only reviewer grant, got %s", grant)
		} else if err := materializeReviewOutput(w, t, childID, plan.Role, rt.Name, modelName, transcriptPath); err != nil {
			runErr = fmt.Errorf("structured review output: %w", err)
		}
	}

	// Evaluate against the fixed criterion: acceptance boxes, plus what the
	// child actually wrote to the workspace. Partial work survives a dead
	// child — that is the whole point of the workspace return channel.
	t2, _ := store.FindTask(w, taskRef)
	done, total := 0, 0
	if t2 != nil {
		for _, box := range t2.Acceptance() {
			total++
			if box.Done {
				done++
			}
		}
	}
	// Read the child's events from the shared root — where a worktree child now
	// writes them too (workspace.Find redirects), so the outcome reflects real
	// work instead of always reading 0.
	childEvents, _ := eventlog.List(w, eventlog.Query{Actor: childID})

	outcome := "ok"
	var handoff store.RootHandoff
	handoffRequired := false
	if store.RootHandoffRequested(w, runID) || (isolatedWorktree && len(plan.PlannedHandoffs) > 0) {
		failureClass := "filesystem_sandbox_refusal"
		if runErr != nil {
			failureClass = mutationFailureClass(runErr)
		}
		var handoffErr error
		handoff, handoffRequired, handoffErr = store.CaptureRootHandoff(w, runID, t.ID, childID, workDir, store.RootHandoffRequest{
			Schema: store.RootHandoffSchema, FailedOperation: "worker lifecycle publication",
			FailureClass: failureClass, Stderr: clikit.ErrStr(runErr),
			NextAction: "owner consumes the handoff after hash re-observation, reruns verification, then commits and publishes without changing the worker harness or grant",
		}, time.Now())
		if handoffErr != nil {
			return fmt.Errorf("capture root handoff: %w", handoffErr)
		}
	}
	if handoffRequired {
		if receipt, resolved, commitErr := applyParentCommitIfPlanned(w, handoff, plan.PlannedHandoffs, time.Now()); resolved {
			handoffRequired = false
			runErr = nil
			fmt.Fprintf(ctx.Stdout, "parent-mediated commit %s applied from immutable run request %s\n", clikit.Short(receipt.Commit, 12), clikit.Short(receipt.RequestID, 19))
			if commitErr != nil {
				fmt.Fprintf(ctx.Stderr, "warning: %v; the immutable request and receipt remain authoritative\n", commitErr)
			}
		} else if commitErr != nil {
			fmt.Fprintf(ctx.Stderr, "parent-mediated commit refused: %v\n", commitErr)
		}
	}
	switch {
	case handoffRequired:
		outcome = "handoff-required"
	case timedOut:
		outcome = "stalled"
	case runErr != nil && claimSandboxed:
		outcome = "failed"
	case runErr != nil && len(childEvents) > 0:
		outcome = "partial"
	case runErr != nil:
		outcome = "failed"
	}
	if err := record.critical("outcome.md", fmt.Sprintf("outcome: %s\nexit: %v\nelapsed: %s\nacceptance: %d/%d\nevents_by_child: %d\n",
		outcome, clikit.ErrStr(runErr), elapsed, done, total, len(childEvents))); err != nil {
		return err
	}
	if rec, ok := readProcByRef(w, runID); ok {
		if err := procmon.CompleteRecord(filepath.Join(runDir, "proc.txt"), rec, outcome); err != nil {
			return fmt.Errorf("record critical run artifact proc.txt: %w", err)
		}
	}
	fmt.Fprintf(ctx.Stdout, "run %s: %s in %s · child wrote %d event(s) · acceptance %d/%d\ntranscript: %s\n",
		clikit.Short(runID, 10), outcome, elapsed, len(childEvents), done, total, filepath.Join(runDir, "transcript.log"))
	if outcome == "failed" || outcome == "stalled" {
		if runErr != nil {
			return fmt.Errorf("child %s: %s (see %s): %w", childID, outcome, runDir, runErr)
		}
		return fmt.Errorf("child %s: %s (see %s)", childID, outcome, runDir)
	}
	if outcome == "handoff-required" {
		return clikit.Refusedf("handoff-required for run %s: %s; next: %s", clikit.Short(runID, 10), handoff.FailedOperation, handoff.NextAction)
	}
	return nil
}

// reclaimMainCheckoutEscape diffs the main checkout's dirty paths against
// preSpawnDirty (captured right before the child ran) and reverts whatever is
// new — a tracked file is restored with `git checkout --`, an untracked one
// (the common case: a brand-new file the child created by absolute path) is
// removed outright, since there is nothing to restore it to. Only the DELTA
// is touched, so a human's own pre-existing uncommitted work in the main
// checkout is never reverted. Returns the leaked paths (already undone) so
// the caller can name them.
//
// This is a backstop, not prevention: it cannot stop the write from
// happening, only make sure it does not survive the spawn that caused it. A
// child that commits directly into main's history (rather than dirtying its
// working tree) is not caught here — that needs the child to run `git
// commit` against a different repo path, a materially more deliberate
// action than the stray-file-write incident this guards against.
func reclaimMainCheckoutEscape(root string, preSpawnDirty map[string]bool) ([]string, error) {
	after, err := gitx.DirtyPaths(root, ".dacli")
	if err != nil {
		return nil, err
	}
	var leaked []string
	for _, p := range after {
		if !preSpawnDirty[p] {
			leaked = append(leaked, p)
		}
	}
	for _, p := range leaked {
		if _, err := gitx.Run(root, "checkout", "--", p); err != nil {
			// Not a tracked path checkout can restore (a brand-new file) —
			// remove it directly so the tree returns to its pre-spawn state.
			_ = os.Remove(filepath.Join(root, p))
		}
	}
	return leaked, nil
}

// worktreePreamble is the ISOLATED-WORKTREE section appended to a --worktree
// child's brief. It resolves two directions the agent would otherwise conflate:
//
//   - CODE and git live in the worktree, so edits land on THIS branch. That is
//     the sandbox-signal fix (the `dacli` binary is allowlisted at the main
//     checkout's path, so an agent can mistake main for its repo).
//   - dacli WORKSPACE STATE — identity, `task check`, notes, findings, and the
//     event crumb `dacli commit` writes — deliberately resolves to the shared
//     MAIN workspace, not this worktree's `.dacli` snapshot (workspace.Find
//     redirects a linked worktree via git's common dir; the snapshot is stale
//     the moment the branch was cut). Task 260: spawn must SAY this, because an
//     agent that assumes its `task check` lands on its branch is surprised when
//     the record shows up in the shared store — and may "fix" it by cd-ing to
//     main, which corrupts the shared tree.
func worktreePreamble(wtPath string) string {
	return fmt.Sprintf("\n\n## Your working directory (ISOLATED WORKTREE)\nYou are running in an isolated git worktree at:\n\n    %s\n\nEVERY file you read, create, or edit lives UNDER this directory — use paths relative to it. Do NOT edit any file by an absolute path outside it. In particular, the `dacli` binary may live in a DIFFERENT checkout (the main tree); editing code there clobbers the main tree and other agents. Your code edits, `git`, `go build`, and the commit `dacli commit` records all operate HERE, on THIS branch.\n\nBut dacli's WORKSPACE STATE is deliberately shared, not per-branch: your agent identity, `task check`, `note add`, findings, and the event crumb every `dacli commit` writes resolve to the MAIN workspace at the repo root — NOT this worktree's `.dacli` (a git snapshot that went stale the moment your branch was cut). That is intended and correct: it is how you see your own freshly-minted identity and how your reports reach the owner in one shared, append-safe store. So your CODE lands on your branch while your RECORD of the work lands in the shared store. Never `cd` to the main checkout to 'fix' this — that would commit code onto main's tree.\n", wtPath)
}

// blockedFileName is the break-glass channel (task 269). Every way a child
// reports — notes, findings, ask, task done — is a `dacli` invocation, so the
// ONE failure that breaks dacli itself (a broken binary, a rejected sandbox, a
// lost token) would silence every report and make the run look merely empty
// instead of blocked. A child writes this plain file into its run directory with
// a single ordinary file write and NO dacli, and `agents`/`wait` read it back to
// report the run as a distinct BLOCKED state. It lives in the run directory
// under the SHARED workspace (not the child's worktree), so it is read back the
// same way whether or not the child ran isolated, and it never lands on a branch.
const blockedFileName = "blocked.txt"

// readBlocked returns the child's blocked reason for a run, trimmed of trailing
// whitespace, or "" when the child never raised the channel. A read error
// (file absent — the common case) is deliberately reported as "not blocked".
func readBlocked(w *workspace.Workspace, runID string) string {
	raw, err := os.ReadFile(filepath.Join(w.RunDir(runID), blockedFileName))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// firstLine returns s up to its first newline, trimmed — the one-line summary
// for a possibly-multiline blocked reason (the full text is kept whole in the
// file and in outcome.md; only the display is shortened).
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}

// blockedChannelPreamble is the last section of a spawned child's brief: it
// names the exact path of the break-glass BLOCKED channel and says when to use
// it. It is appended AFTER the worktree preamble so its single carve-out from
// the "never write an absolute path outside your worktree" rule — this one path
// is the shared record store, not a code tree — is the final word (task 269).
func blockedChannelPreamble(path string) string {
	return fmt.Sprintf("\n\n## If you cannot run dacli at all (BLOCKED)\nEvery channel above — `note add`, findings, `ask`, `task done` — is a `dacli` invocation. So the one failure that breaks `dacli` ITSELF (a broken binary, a sandbox that rejects it, a lost token) would silence every report you could otherwise make, and your run would read as merely empty rather than blocked.\n\nThere is one escape hatch that is NOT dacli: with a single ordinary file write and no command, write a plain text file to exactly this path:\n\n    %s\n\nFirst line: that you are blocked. Below it: WHY — what you ran and what it said. `dacli agents` and `dacli wait` read this file and surface your run as BLOCKED, a distinct state and not a normal completion, so a human is told. This is the ONE absolute path outside your working directory you may write — it is the shared record store, never a code tree, so it never touches your branch. Use it only as a last resort, when dacli genuinely will not run; if dacli works, report through it as usual.\n", path)
}

// externalRadius reports whether task t's brief sits in the blast radius of any
// external-origin artifact (RUNTIMES § 18), returning the distinct origins when
// it does. A single broad "external:" needle unions every external source's
// radius; ExposedBriefs answers whether this task is exposed. hasExternal
// distinguishes "clean, nothing external recorded" from "clean, not in radius".
// Shared by --advise (display) and the spawn-time taint gate (refusal) so the
// two never diverge.
func externalRadius(w *workspace.Workspace, t *store.Task) (origins []string, inRadius, hasExternal bool) {
	res, err := store.Taint(w, "external:")
	if err != nil || len(res.Hits) == 0 {
		return nil, false, false
	}
	for _, ref := range res.ExposedBriefs(w) {
		if ref == t.Slug {
			inRadius = true
			break
		}
	}
	if !inRadius {
		return nil, false, true
	}
	seen := map[string]bool{}
	for _, h := range res.Hits {
		if !seen[h.Origin] {
			seen[h.Origin] = true
			origins = append(origins, h.Origin)
		}
	}
	sort.Strings(origins)
	return origins, true, true
}

// printAdvisory is the body of `spawn --advise`: it reports what the log
// already knows at the spawn decision and returns, changing nothing. Two
// readouts, both reusing the existing machinery:
//
//   - Budget/sizing from the calibrated band. TOKENS are the F1 unit and are
//     PREFERRED whenever this band has any token-bearing sample: the suggested
//     token budget = median output-tokens/point × the task's Te once the band
//     has enough history (n≥10, D1's threshold); below that the figure is
//     PROVISIONAL and no firm number is printed. A band whose runs never
//     reported usage falls back to the honest wall-clock proxy (same n≥10 gate).
//   - Taint status: whether this task's brief sits in an external source's
//     blast radius, via store.Taint / TaintResult.ExposedBriefs.
func printAdvisory(ctx *clikit.Ctx, w *workspace.Workspace, t *store.Task, band store.Band, claims []string) {
	fmt.Fprintf(ctx.Stdout, "── advise · %03d-%s · band %s ──\n", t.Seq, t.Slug, band.String())
	if len(claims) == 0 {
		fmt.Fprintln(ctx.Stdout, "  claims: (none)")
	} else {
		fmt.Fprintf(ctx.Stdout, "  claims: %s\n", strings.Join(claims, ", "))
	}

	// One walk of the calibration samples backs both the token readout (preferred)
	// and the wall-clock fallback below, so --advise never re-walks RunsDir twice.
	samples := store.CalibrationSamples(w)

	if tokRatio, tn := store.MedianTokenRatio(samples, band); tn > 0 {
		// F1's measured token cost is the real unit — prefer it over wall-clock.
		if tp, ok := t.Estimate(); ok && tn >= 10 {
			fmt.Fprintf(ctx.Stdout,
				"  tokens: ~%.0f suggested (band ×%.0f median output-tokens/point on Te %.1f, n=%d)\n",
				tokRatio*tp.Expected(), tokRatio, tp.Expected(), tn)
			fmt.Fprintln(ctx.Stdout, "  (measured token cost, F1; cap this spawn with --max-tokens N)")
		} else if tn >= 10 {
			fmt.Fprintf(ctx.Stdout,
				"  tokens: band ×%.0f median output-tokens/point (n=%d) — estimate the task for a token figure\n",
				tokRatio, tn)
		} else {
			// Thin data: mark PROVISIONAL, print no firm suggested budget.
			fmt.Fprintf(ctx.Stdout,
				"  tokens: PROVISIONAL — band has token history but n=%d < 10 (median ×%.0f output-tokens/point); no calibrated number yet\n",
				tn, tokRatio)
		}
	} else {
		// Honest fallback: this band's runs never reported tokens, so the
		// wall-clock proxy is the best calibrated sizing available.
		var rs []float64
		for _, s := range samples {
			if s.Band == band {
				rs = append(rs, s.Ratio())
			}
		}
		if len(rs) >= 10 {
			med, p10, p90 := spm.Median(rs), percentile(rs, 10), percentile(rs, 90)
			if tp, ok := t.Estimate(); ok {
				te := tp.Expected()
				fmt.Fprintf(ctx.Stdout,
					"  budget: ~%.1f h suggested (p10–p90 %.1f–%.1f h) — band ×%.2f median hours/point on Te %.1f\n",
					med*te, p10*te, p90*te, med, te)
			} else {
				fmt.Fprintf(ctx.Stdout,
					"  budget: band ×%.2f median hours/point (p10–p90 ×%.2f–×%.2f) — estimate the task for an hour figure\n",
					med, p10, p90)
			}
			fmt.Fprintln(ctx.Stdout, "  (wall-clock proxy — this band's runtime reports no tokens; advisory — you still pass --budget)")
		} else {
			fmt.Fprintf(ctx.Stdout, "  budget: no band history yet (n=%d < 10) — no calibrated suggestion\n", len(rs))
		}
	}

	// External origins are the untrusted class (RUNTIMES § 18 cross-tree
	// injection); externalRadius unions every external source's radius and
	// answers whether this task's brief is in it. The same helper backs the
	// spawn-time gate, so --advise and the refusal never disagree.
	if origins, inRadius, hasExternal := externalRadius(w, t); inRadius {
		fmt.Fprintf(ctx.Stdout, "  taint: task %03d is in the blast radius of %s — audit before trusting this brief\n",
			t.Seq, strings.Join(origins, ", "))
	} else if hasExternal {
		fmt.Fprintln(ctx.Stdout, "  taint: clean (no external-origin artifact reaches this brief)")
	} else {
		fmt.Fprintln(ctx.Stdout, "  taint: clean (no external-origin artifacts recorded)")
	}
	fmt.Fprintln(ctx.Stdout, "── (preview only — no agent spawned; re-run without --advise to launch) ──")
}

// bandTokenBudget computes the expected token cost of spawning THIS band on task
// t from the band's measured output-tokens/point (F1): expected = median
// TokenRatio × the task's Te. It reads the SAME calibration samples the advisory
// displays via store.MedianTokenRatio, so the launch and `--advise` can never
// disagree on the estimate. n is the count of token-bearing samples in the
// band. ok is false when the band has no token history (a text runtime) or the
// task carries no three-point estimate. This helper predicts cost; enforcement
// is solely the adapter's TokenLimitFlag passed by tokenLimitArgs.
func bandTokenBudget(w *workspace.Workspace, t *store.Task, band store.Band) (expected float64, n int, ok bool) {
	ratio, n := store.MedianTokenRatio(store.CalibrationSamples(w), band)
	if n == 0 {
		return 0, 0, false
	}
	tp, est := t.Estimate()
	if !est {
		return 0, n, false
	}
	return ratio * tp.Expected(), n, true
}

// percentile returns the p-th (0..100) percentile of xs by linear
// interpolation. It is a DELIBERATE local copy of insight.percentile: the
// feature-slice isolation rule (cli/arch_test.go — slices never import each
// other) forbids execution importing insight, and this task's STRICT scope
// forbids hoisting the helper into spm/store — so a small in-slice copy is the
// only honest option. The math is identical; keep the two in sync.
func percentile(xs []float64, p float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	if len(s) == 1 {
		return s[0]
	}
	rank := p / 100 * float64(len(s)-1)
	lo := int(rank)
	if lo >= len(s)-1 {
		return s[len(s)-1]
	}
	return s[lo] + (rank-float64(lo))*(s[lo+1]-s[lo])
}

// cmdSupervise runs the RUNTIMES § 7 loop: spawn, evaluate against the
// acceptance criteria written before the work started, correct, repeat. It
// terminates because the criterion is external and turns are capped.
func cmdSupervise(ctx *clikit.Ctx, args []string) error {
	w, id, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	f, _ := clikit.ParseFlags(args)
	if err := f.Reject(launchFlagsWith("max-turns", "pr", "review", "pr-number")...); err != nil {
		return err
	}
	taskRef := f.Get("task")
	if taskRef == "" {
		return clikit.Usagef("usage: dacli supervise --task <ref> [--runtime name] [--role r] [--max-turns N] [--grant ro|rw] [--model m] [--claim path,path] [--pr] [--review [--pr-number N]] [--budget N] [--max-tokens N [--allow-advisory-tokens]] [--timeout sec] [--cooperative|--allow-user-config] [--advise] [--force]")
	}
	// The SAME gated prologue `spawn` takes (launchGates). supervise sends this
	// brief to this runtime once per turn, so every gate that decides whether a
	// child may be launched at all decides it here too — see the shared-path
	// comment above resolveLaunch.
	plan, err := resolveLaunch(ctx, w, f, taskRef)
	if errors.Is(err, errAdviseOnly) {
		return nil // --advise previewed and stopped; nothing spawned (dacli 232)
	}
	if err != nil {
		return err
	}
	t, rt := plan.Task, plan.Runtime
	if err := validateReviewTarget(w, t, f); err != nil {
		return err
	}
	// workspace.Find redirects calls from a linked task checkout to the shared
	// root. Preserve the caller's registered task worktree nevertheless: a
	// correction launched from root after an audited recovery transfer has no
	// current worktree ownership record, so its governed commit is refused.
	workDir, isolatedWorktree, err := resolveSuperviseWorkDir(w, t, ctx.Cwd)
	if err != nil {
		return err
	}
	if plan.Grant == model.GrantRW && isolatedWorktree && len(plan.Claims) == 0 {
		return clikit.Refusedf("isolated writable task %s has no exact path claim; record a concrete task claim before launching", t.ID)
	}
	roleName, modelName, grant := plan.RoleName, plan.Model, plan.Grant
	claims, sandboxArgs := plan.Claims, plan.Sandbox
	budget, timeout := plan.Budget, plan.Timeout

	maxTurns := 3
	if n, err := f.Int("max-turns", 0); err != nil {
		return err
	} else if n > 0 {
		maxTurns = n
	}

	// ONE child identity across turns: ownership continuity is what lets the
	// child claim on turn 1 and check its own boxes on turn 2.
	childID, token, err := agentid.Spawn(w, id, roleName, grant)
	if err != nil {
		return err
	}
	// One child owns this task across turns; claim once (idempotent) so a
	// claim->completed span exists for calibrate to join (D1).
	claimTask(ctx, w, t, childID)

	unmetList := func() []string {
		cur, err := store.FindTask(w, taskRef)
		if err != nil {
			return nil
		}
		var unmet []string
		for _, box := range cur.Acceptance() {
			if !box.Done {
				unmet = append(unmet, box.Text)
			}
		}
		return unmet
	}

	for turn := 1; turn <= maxTurns; turn++ {
		b, err := brief.Assemble(w, taskRef, brief.Options{Budget: budget, Role: roleName})
		if err != nil {
			return err
		}
		suffix, perr := promptSuffix(w, f, t, childID, grant)
		if perr != nil {
			return perr
		}
		prompt := b.Render() + suffix
		if turn > 1 {
			// No session resume: each turn re-sends the brief plus the
			// correction (templated). Turn 3 is a mis-sizing signal, not
			// normal operation.
			correction, cerr := prompts.Render(w.PromptsDir(), "supervise_correction", map[string]any{
				"Turn": turn, "MaxTurns": maxTurns, "Unmet": unmetList(),
			})
			if cerr != nil {
				return cerr
			}
			prompt += "\n" + correction
		}
		if turn == 3 {
			fmt.Fprintf(ctx.Stderr, "note: turn 3 — under the small-task doctrine this usually means the task should be decomposed, not retried\n")
		}

		runID := ulid.New()
		runDir := w.RunDir(runID)
		if err := os.MkdirAll(runDir, 0o755); err != nil {
			return err
		}
		record := openRunRecord(runDir, ctx.Stderr)
		if len(plan.PlannedHandoffs) > 0 {
			if err := record.critical("planned-handoffs.txt", strings.Join(plan.PlannedHandoffs, "\n")+"\n"); err != nil {
				return err
			}
		}
		if isolatedWorktree {
			record.bestEffort("worktree.txt", workDir+"\n")
			prompt += worktreePreamble(workDir)
		}
		providerWorkDir := workDir
		if grant == model.GrantRW && isolatedWorktree {
			claimPlan, sandboxErr := prepareClaimSandbox(w, runID, workDir, claims, time.Now())
			if sandboxErr != nil {
				return clikit.Refusedf("claim-enforced writable supervise turn refused: %v", sandboxErr)
			}
			providerWorkDir = claimPlan.SandboxDir
			claims = claimPlan.Claims
			prompt += fmt.Sprintf("\nCLAIM-ENFORCED ASSIGNMENT CHECKOUT: Work only in %s. The parent projects only exact claimed regular-file additions/modifications into %s; all other writes refuse the turn.\n", providerWorkDir, workDir)
		}
		if grant == model.GrantRW {
			prompt += handoffChannelPreamble(runDir, plan.PlannedHandoffs)
		}
		if err := record.critical("brief.md", prompt); err != nil {
			return err
		}
		// Record role/model in the SAME OrDash canonical form cmdSpawn uses
		// (execution.go:373-374), so a supervise-completed task's run band
		// {role,model,rt} matches the {OrDash(role),OrDash(model),rt} band the
		// calibrate gate/advise compares against. Without this the band was
		// {"","",rt} and never equalled the gate's {"-","-",rt} sentinel form,
		// making every supervise actual dead weight for by-agent-band calibration.
		invocation := fmt.Sprintf("run: %s\nsupervise_turn: %d/%d\ntask: %s\nchild: %s\nrole: %s\nmodel: %s\nruntime: %s\nmax_tokens: %s\nmax_tokens_mode: %s\n",
			runID, turn, maxTurns, t.ID, childID, clikit.OrDash(roleName), clikit.OrDash(modelName), rt.Name, f.Get("max-tokens"), tokenLimitMode(plan))
		invocation += contextInvocation(plan.Role, plan.HasRole, plan.ContextOverride, plan.ContextSources)
		invocation += mutationPreflightInvocation(plan.MutationCapabilities)
		provenance, err := promptInvocation(w.PromptsDir(), prompt)
		if err != nil {
			return err
		}
		invocation += provenance
		if err := record.critical("invocation.txt", invocation); err != nil {
			return err
		}

		fmt.Fprintf(ctx.Stderr, "turn %d/%d: %s on %s\n", turn, maxTurns, childID, rt.Name)
		extraArgs := append(append([]string{}, sandboxArgs...), modelArgs(ctx, rt, modelName)...)
		extraArgs = append(extraArgs, tokenLimitArgs(plan)...)
		// Claims are PUBLISHED here, not merely checked in the prologue: the
		// overlap gate reads other agents' proc records, so a supervised run that
		// checked --claim without recording its own would take the mutual
		// exclusion one-way — every other agent would still be free to claim the
		// tree this one is editing.
		var procWriteErr error
		onStart := func(pid, pgid int) {
			rec := procmon.Record{
				RunID: runID, Child: childID, Task: t.ID, Role: roleName, Runtime: rt.Name,
				PID: pid, PGID: pgid, PIDStart: pidStart(pid), Started: time.Now(), Timeout: time.Duration(timeout) * time.Second, Claims: claims,
			}
			procWriteErr = procmon.WriteRecord(filepath.Join(runDir, "proc.txt"), rec)
			if procWriteErr != nil {
				terminateRecordedTree(rec, 3*time.Second)
			}
		}
		elapsed, timedOut, runErr := execRuntime(providerWorkDir, filepath.Join(runDir, "transcript.log"), rt, prompt, token, extraArgs, timeout, false, onStart)
		if procWriteErr != nil {
			return fmt.Errorf("record critical run artifact proc.txt: %w", procWriteErr)
		}
		if runErr != nil && !timedOut {
			if policyErr := recordProviderFailure(ctx, w, rt.Name, filepath.Join(runDir, "transcript.log"), runErr, record.bestEffort); policyErr != nil {
				return policyErr
			}
		}
		if grant == model.GrantRW && runErr == nil && !timedOut {
			projected, projectionErr := projectAndCommitClaimSandbox(w, runID, t, childID, workDir, time.Now())
			if projectionErr != nil {
				runErr = fmt.Errorf("claim sandbox projection: %w", projectionErr)
			} else if len(projected) > 0 {
				fmt.Fprintf(ctx.Stderr, "  projected and parent-committed %d claimed path(s)\n", len(projected))
			}
		}

		// The supervisor owns the objects, so it applies the child's events
		// between turns — claims become ownership, findings become notes.
		if res, err := eventlog.Sync(w, id.ID, id.CanMutate); err == nil && res.Applied > 0 {
			fmt.Fprintf(ctx.Stderr, "  applied %d child event(s)\n", res.Applied)
		}

		unmet := unmetList()
		cur, _ := store.FindTask(w, taskRef)
		outcome := fmt.Sprintf("turn %d: %d unmet, elapsed %s", turn, len(unmet), elapsed)
		if err := record.critical("outcome.md", outcome+"\n"); err != nil {
			return err
		}
		if rec, ok := readProcByRef(w, runID); ok {
			if err := procmon.CompleteRecord(filepath.Join(runDir, "proc.txt"), rec, outcome); err != nil {
				return fmt.Errorf("record critical run artifact proc.txt: %w", err)
			}
		}

		if len(unmet) == 0 && cur != nil && cur.Status == model.StatusDone {
			fmt.Fprintf(ctx.Stdout, "accepted after %d turn(s): all acceptance criteria met and task done\n", turn)
			return nil
		}
		if timedOut {
			if runErr != nil {
				return fmt.Errorf("stalled: turn %d timed out after %ds (run %s): %w", turn, timeout, clikit.Short(runID, 10), runErr)
			}
			return fmt.Errorf("stalled: turn %d timed out after %ds (run %s)", turn, timeout, clikit.Short(runID, 10))
		}
		if runErr != nil {
			fmt.Fprintf(ctx.Stderr, "  turn %d exited non-zero (%v) — child events still count\n", turn, runErr)
		}
	}
	return fmt.Errorf("stalled after %d turns; unmet:\n  - %s\ndecompose the task or fix the criteria — do not simply re-run", maxTurns, strings.Join(unmetList(), "\n  - "))
}

// phaseGate is the answer to "don't start implementation while still in
// discovery": if the task's project is in a gated phase, a role whose kind
// the phase disallows is refused — you can't spawn an implementer into a
// research phase. Roles with no kind opt out; solo/untemplated projects are
// never gated.
func phaseGate(w *workspace.Workspace, t *store.Task, role team.Role) error {
	if role.Kind == "" {
		return nil
	}
	// A phase we could not READ is not a phase that permits everything. The
	// stage gates had this exact shape and were fixed to fail closed; this
	// preflight kept the old one, so a broken or unreadable project file let a
	// role of any kind be spawned into any phase — the gate reporting success
	// from a path that never ran the check (found by nilerr).
	ph, err := gates.PhaseFor(w, t.Project)
	if err != nil {
		return clikit.Refusedf("cannot read project %s's stage to check whether a %s role may act: %v — fix the project file, or pass --force if you have accounted for it", t.Project, role.Kind, err)
	}
	if !ph.Gated {
		return nil
	}
	if !ph.AllowsKind(role.Kind) {
		return clikit.Refusedf("project %s is in the %s phase; a %s role has no work here (allowed: %s). Advance the stage first (dacli stage advance %s), or use an allowed role",
			t.Project, ph.Name, role.Kind, strings.Join(ph.Allows, ", "), t.Project)
	}
	return nil
}

// seniorityGate enforces a role's MaxPoints: a junior role mechanically
// cannot take the hard migration. Unestimated tasks are refused too — a
// capped role takes only work whose size somebody stated.
//
// EXCEPT for a planner, whose OUTPUT is the size. Refusing one for a missing
// estimate asks a malformed question ("can you hold work of this size?" of the
// role that determines the size), and it deadlocked the loop: the review phase
// files unestimated tasks, sizeUnestimated spawns the capped `estimator` role
// to size them, this gate refused THAT spawn, and the capped implementer then
// refused the still-unsized task — every cycle, forever. The reporter watched
// fourteen consecutive no-progress cycles and their backlog grow 6 → 8 while
// done stayed at 27 (issue #430).
//
// Reproduced verbatim before fixing:
//
//	$ dacli spawn --task 344 --role estimator
//	dacli: role estimator takes only estimated tasks (max 2 points) — estimate 344-… first
//
// The over-cap refusal below still applies to a planner once an estimate
// exists, so the cap keeps meaning what it says.
func seniorityGate(role team.Role, t *store.Task) error {
	tp, ok := t.Estimate()
	te := 0.0
	if ok {
		te = tp.Expected()
	}
	capacity := team.TaskCapacity(role, te, ok)
	if capacity.Fits {
		return nil
	}
	if !ok {
		return clikit.Refusedf("role %s takes only estimated tasks (max %g points) — estimate %03d-%s first (a planner-kind role is exempt: sizing is its output)", role.Name, capacity.Limit, t.Seq, t.Slug)
	}
	return clikit.Refusedf("task %03d-%s is Te %.1f, above role %s's cap of %g — assign a heavier role, or decompose the task", t.Seq, t.Slug, te, role.Name, capacity.Limit)
}

// modelArgs routes a model tier onto the runtime. A runtime with no model
// flag makes role-level routing inoperative — announced, never ignored.
func modelArgs(ctx *clikit.Ctx, rt store.Runtime, modelName string) []string {
	if modelName == "" {
		return nil
	}
	if rt.ModelFlag == "" {
		fmt.Fprintf(ctx.Stderr, "warning: model %q requested but runtime %s declares no model_flag — running on the runtime's default\n", modelName, rt.Name)
		return nil
	}
	return []string{rt.ModelFlag, modelName}
}

// sandboxFor applies the § 8 rule: a read-only child needs a runtime that
// can enforce it. --cooperative downgrades EXPLICITLY and loudly.
func sandboxFor(ctx *clikit.Ctx, rt store.Runtime, grant model.Grant, cooperative bool) ([]string, error) {
	if grant != model.GrantRO {
		// The rw half of the same rule. An rw grant is a promise the child can
		// modify the workspace; on a runtime whose allowlist grants no write tool
		// (junior's cc: Read/Grep/Glob/LS + the dacli binary) that promise is a
		// lie — the child reads its brief, fails its first edit, and the run is
		// spent discovering it (dacli 250). Refuse at spawn, symmetric to the ro
		// refusal below, unless --cooperative accepts it out loud.
		if !store.RuntimeWritable(rt) && !cooperative {
			return nil, clikit.Refusedf("runtime %s grants no write tool (its --allowedTools list has no Edit/Write), so an rw child cannot modify the workspace and would burn the run finding out. Add a write-capable adapter — `dacli runtime add %s-rw --preset claude-code-rw` — and point the role at it, or pass --cooperative to spawn anyway", rt.Name, rt.Name)
		}
		return nil, nil
	}
	if store.RuntimeEnforcesRO(rt) {
		return rt.SandboxRO, nil
	}
	if !cooperative {
		state := rt.ROProbe
		if state == "" {
			state = store.RuntimeROUnknown
		}
		return nil, clikit.Refusedf("runtime %s cannot enforce read-only (sandbox probe: %s); spawning an unverified process labeled ro would be a lie. Run `dacli runtime doctor`, pass --cooperative to accept convention-only permissions, or use an rw grant", rt.Name, state)
	}
	if rt.ROProbe == store.RuntimeROFailed {
		fmt.Fprintf(ctx.Stderr, "warning: read-only is COOPERATIVE on %s — its sandbox probe failed, so no sandbox arguments are applied; the child can bypass dacli and you accepted this with --cooperative\n", rt.Name)
		return nil, nil
	}
	fmt.Fprintf(ctx.Stderr, "warning: read-only is COOPERATIVE on %s — declared sandbox arguments are unverified and applied best-effort; the child can bypass dacli and you accepted this with --cooperative\n", rt.Name)
	return rt.SandboxRO, nil
}

// recordProviderFailure is the adapter-neutral failure seam shared by spawn
// and supervise. Permanent, authentication, and policy outcomes are recorded
// as typed diagnostics but never open a fallback circuit.
func recordProviderFailure(ctx *clikit.Ctx, w *workspace.Workspace, runtimeName, transcriptPath string, runErr error, writeRun func(string, string)) error {
	exitCode := 1
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		exitCode = exitErr.ExitCode()
	}
	return recordProviderOutcome(ctx.Stdout, w, runtimeName, transcriptPath, exitCode, writeRun)
}

func recordProviderOutcome(out io.Writer, w *workspace.Workspace, runtimeName, transcriptPath string, exitCode int, writeRun func(string, string)) error {
	raw, _ := os.ReadFile(transcriptPath)
	outcome := providerpolicy.Classify(exitCode, string(raw))
	writeRun("provider-outcome.txt", fmt.Sprintf("kind: %s\nreason: %s\nreset_after: %s\n", outcome.Kind, outcome.Reason, outcome.ResetAfter))
	if !outcome.Fallbackable() {
		return nil
	}
	cooldown := outcome.ResetAfter
	if cooldown <= 0 {
		if delay, ok := (providerpolicy.RetryPolicy{Base: time.Second, Max: time.Minute, Jitter: .2}).Delay(0, outcome); ok {
			cooldown = delay
		} else {
			// Hard quota exhaustion has no useful immediate retry.
			cooldown = time.Hour
		}
	}
	limits := store.LoadRuntimeLimits(w)
	if _, err := limits.Record(runtimeName, outcome, cooldown); err != nil {
		return err
	}
	return limits.Report(out, providerpolicy.Transition{Source: runtimeName, Reason: outcome.Reason, Cooldown: cooldown})
}

// execRuntime launches one child turn. Env is allowlisted by NAME — the
// child gets the token plus exactly what the adapter declares, never the
// parent's full environment.
//
// A private guardian is placed in its own process GROUP (Setpgid), then starts
// the runtime in that group and remains its authenticated leader until every
// descendant drains. This preserves task 177's dead-runtime descendants without
// trusting a recycled numeric PGID (task 369). onStart records the guardian's
// durable pid/start identity for separate dacli invocations.
//
// The child's stdout+stderr stream to transcriptPath (a real file), so a
// DETACHED child's output persists after this parent process exits: the child
// keeps its own inherited fd and the parent closes its copy. detach=true starts
// the child in its own process group, releases it, and returns immediately with
// no deadline (enforce timeouts via `dacli kill` or a watchdog); the foreground
// path keeps the context deadline and group-kill-on-timeout.
func promptSuffix(w *workspace.Workspace, f *clikit.Flags, t *store.Task, childID string, grant model.Grant) (string, error) {
	contract, err := prompts.AutonomousContract(w.PromptsDir())
	if err != nil {
		return "", fmt.Errorf("autonomous delivery contract: %w", err)
	}
	out, err := protocolPreamble(w, childID, grant, t)
	if err != nil {
		return "", err
	}
	if grant == model.GrantRW {
		exe, exeErr := os.Executable()
		if exeErr != nil {
			exe = "dacli"
		}
		git, err := prompts.Render(w.PromptsDir(), "git_workflow", map[string]any{
			// Commands that mutate task state must use the globally stable ID.
			// A sequence is only project-local, so emitting it here stranded
			// workers as soon as another project allocated the same number
			// (issue #636).
			"Ref":    t.ID,
			"Title":  t.Title,
			"Branch": fmt.Sprintf("dacli/%03d-%s", t.Seq, t.Slug),
			"PR":     f.Bool("pr"),
			"Exe":    exe,
			// The child's format/build/test advice comes from the project's
			// own recorded stack (dacli 192) — this prompt used to order every
			// writer to gofmt its .go files, which is nonsense in a Python
			// project. A project with nothing recorded yields the zero Stack,
			// which the template treats as "unchanged from before".
			"Stack": projectStack(w, t.Project),
		})
		if err != nil {
			return "", err
		}
		out += "\n" + git
	}
	if f.Bool("review") {
		exe, exeErr := os.Executable()
		if exeErr != nil {
			exe = "dacli"
		}
		review, err := prompts.Render(w.PromptsDir(), "review_workflow", map[string]any{
			"Task":             t.ID,
			"Search":           fmt.Sprintf("dacli/%03d-%s", t.Seq, t.Slug),
			"Base":             reviewBase(w, t),
			"PRRef":            f.Get("pr-number"),
			"PR":               f.Bool("pr"),
			"Exe":              exe,
			"StructuredReview": f.Bool("structured-review-result"),
		})
		if err != nil {
			return "", err
		}
		out += "\n" + review
	}
	return "\n" + contract.Text + out, nil
}

// reviewBase matches local review instructions to the task project's landing
// target. An explicitly configured base wins; legacy projects use the
// repository's actual trunk (including master or a renamed origin HEAD).
func reviewBase(w *workspace.Workspace, t *store.Task) string {
	if p, err := store.LoadProject(w, t.Project); err == nil && p.Landing.Base != "" {
		return p.Landing.Base
	}
	if trunk := store.TrunkBranch(w); trunk != "" {
		return trunk
	}
	return "main"
}

// promptInvocation records both levels of prompt identity. The contract hash
// changes when shared semantics change; prompt_hash also captures the task
// brief, role prose, worktree path, and last-mile recovery channel actually
// delivered to this invocation (issue #707).
func promptInvocation(overrideDir, prompt string) (string, error) {
	contract, err := prompts.AutonomousContract(overrideDir)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("prompt_schema: %s\nprompt_version: %s\ncontract_hash: sha256:%s\nprompt_hash: sha256:%s\n",
		contract.Schema, contract.Version, contract.Hash, prompts.DeliveredHash(prompt)), nil
}

// projectStack loads a task's project and reads back the stack `dacli new`
// recorded on it (dacli 192). Every failure — no project, unreadable doc, a
// project written before stacks were recorded — collapses to the zero Stack on
// purpose: a spawn must never fail because a prompt wanted to know the language,
// and the zero value is exactly the pre-192 behavior.
func projectStack(w *workspace.Workspace, slug string) prompts.Stack {
	if slug == "" {
		return prompts.Stack{}
	}
	p, err := store.LoadProject(w, slug)
	if err != nil {
		return prompts.Stack{}
	}
	return prompts.StackFromProject(p.Doc)
}

// protocolPreamble tells a spawned child HOW to report. Without it, a real
// headless child does the work and prints text into the void — work not
// written to the workspace does not exist.
func protocolPreamble(w *workspace.Workspace, childID string, grant model.Grant, t *store.Task) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		exe = "dacli"
	}
	out, err := prompts.Render(w.PromptsDir(), "protocol_preamble", map[string]any{
		"ChildID": childID,
		"Grant":   string(grant),
		"Ref":     t.ID,
		"Slug":    t.Slug,
		"Project": t.Project,
		"Exe":     exe,
		"RW":      grant == model.GrantRW,
	})
	if err != nil {
		// A broken override must not silently mute the protocol — the child
		// would work into the void. Fail the spawn instead.
		return "", fmt.Errorf("protocol_preamble template: %w (fix or remove the override in .dacli/prompts/)", err)
	}
	return "\n" + out, nil
}

func cmdKill(ctx *clikit.Ctx, args []string) error {
	w, id, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	// Terminating process groups is a privileged, irreversible side effect (and
	// the target pgid comes from an on-disk record any rw child can forge) —
	// keep it off the read-only surface.
	if err := clikit.RequireRW(id, "killing an agent"); err != nil {
		return err
	}
	f, _ := clikit.ParseFlags(args)
	if err := f.Reject("all", "grace", "run", "child"); err != nil {
		return err
	}
	grace := time.Duration(3) * time.Second
	if n, err := f.Int("grace", 0); err != nil {
		return err
	} else if n > 0 {
		grace = time.Duration(n) * time.Second
	}

	if f.Bool("all") {
		live, err := liveAgents(w)
		if err != nil {
			return err
		}
		if len(live) == 0 {
			fmt.Fprintln(ctx.Stdout, "no live agents to kill")
			return nil
		}
		for _, rec := range live {
			killOne(ctx, w, rec, grace)
		}
		return nil
	}

	ref := ""
	if len(f.Pos) > 0 {
		ref = f.Pos[0]
	} else if r := f.Get("run"); r != "" {
		ref = r
	} else if c := f.Get("child"); c != "" {
		ref = c
	}
	if ref == "" {
		return clikit.Usagef("usage: dacli kill <run-id-prefix | child-id> [--grace sec]  |  dacli kill --all")
	}
	live, err := liveAgents(w)
	if err != nil {
		return err
	}
	for _, rec := range live {
		if strings.HasPrefix(rec.RunID, ref) || rec.Child == ref {
			killOne(ctx, w, rec, grace)
			return nil
		}
	}
	return store.ErrNotFound{Ref: "live agent " + ref}
}

// pidStart captures a freshly-started child's OS start time for its proc.txt,
// so a later reader can tell the real agent from a process that recycled its
// PID. Best-effort: an empty string just falls back to a bare liveness probe.
func pidStart(pid int) string { s, _ := procmon.ProcStart(pid); return s }

// liveAgents reads every run's proc.txt and returns those whose leader process
// is still alive AND still identifies as the spawned agent (PID not recycled),
// newest first.
//
// "No runs yet" (the directory does not exist) is normal and returns an empty
// result with no error. A directory that exists but cannot be read is a
// different fact — reporting both as "no live agents" hid the second entirely
// and read as "nobody is working" to every caller, including the WIP-facing
// callers (`agents`, `kill --all`, `wait`) that decide from this result
// whether anyone is currently active (dacli 337; mirrors cmdRunsList, which
// could already tell the two apart).
func liveAgents(w *workspace.Workspace) ([]procmon.Record, error) {
	entries, err := os.ReadDir(w.RunsDir())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("cannot read the runs directory at %s: %w", w.RunsDir(), err)
	}
	names := []string{}
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names))) // ULIDs: newest first
	var out []procmon.Record
	for _, n := range names {
		procPath := filepath.Join(w.RunDir(n), "proc.txt")
		rec, err := procmon.ReadRecord(procPath)
		if err != nil {
			continue
		}
		if live, _ := runLifecycleLive(w, rec, lifecycleNow()); live {
			out = append(out, rec)
		} else if rec.Outcome == "" && len(rec.Claims) > 0 {
			// Crash recovery may release a stale claim only after the shared
			// lifecycle classifier has rejected both the recorded process
			// identity and the bounded activity grace. The terminal transition
			// is atomic, so a concurrent claim check never sees half a release.
			if err := procmon.CompleteRecord(procPath, rec, recoveredExitOutcome); err != nil {
				return nil, fmt.Errorf("record critical run artifact proc.txt: %w", err)
			}
		}
	}
	return out, nil
}

func killOne(ctx *clikit.Ctx, w *workspace.Workspace, rec procmon.Record, grace time.Duration) {
	// Reconcile again immediately before the irreversible action. Callers
	// normally supply liveAgents output, but a stale record must never make a
	// recycled numeric process group a signal target (task 369).
	if !procmon.ReconcileRun(rec) {
		fmt.Fprintf(ctx.Stdout, "%s: nothing to signal (already gone)\n", clikit.OrDash(rec.Child))
		return
	}
	before := procmon.SampleGroup(rec.PGID).Procs
	if !terminateRecordedTree(rec, grace) {
		fmt.Fprintf(ctx.Stdout, "%s: nothing to signal (already gone)\n", clikit.OrDash(rec.Child))
		return
	}
	verb := "SIGTERM→SIGKILL if needed"
	// Audit crumb next to the run record: what was reaped and how.
	openRunRecord(w.RunDir(rec.RunID), ctx.Stderr).bestEffort("killed.txt",
		fmt.Sprintf("killed %s (pgid %d, ~%d proc) via %s at %s\n",
			rec.Child, rec.PGID, before, verb, time.Now().UTC().Format(time.RFC3339)))
	fmt.Fprintf(ctx.Stdout, "killed %s — process group %d (~%d proc) reaped via %s\n",
		clikit.OrDash(rec.Child), rec.PGID, before, verb)
}

// cmdWait blocks until the named detached run(s) finish — or all live agents if
// none are named — then finalizes each one's outcome from the workspace effects
// it left behind. This is the block half of async orchestration: `spawn
// --detach` many, then `wait` on them, instead of hand-rolling shell polling.
// runStillLive reports whether the recorded leader still identifies the live
// process tree. Descendants remain covered while that leader is alive. After
// it exits, however, the bare numeric PGID can be reused and can no longer be
// safely attributed or signalled; task 369 closes that residual from task 285.
func runStillLive(rec procmon.Record) bool {
	return procmon.ReconcileRun(rec)
}

const (
	runStartupGrace       = 30 * time.Second
	transcriptActiveGrace = 15 * time.Second
	recoveredExitOutcome  = "process exited (recovered)"
)

// runLifecycleLive is the shared startup/completion view for agents and wait.
// The recorded guardian is normally authoritative, but Codex runs 01KZVSF64J
// and 01KZVSFBXR showed that its CLI can disappear during registration while a
// worker continues writing the inherited transcript descriptor. A short,
// explicit startup grace prevents that ordering window from becoming a false
// completion; recent transcript writes are durable cross-process evidence that
// work continues after it. Both bounds are deliberately finite, so a launch
// with neither a process nor advancing output still becomes finalizable.
func warnMainCheckoutSpawn(ctx *clikit.Ctx, w *workspace.Workspace) {
	if !gitx.Available() {
		return
	}
	if !hasAnyTrunk(w.Root) {
		fmt.Fprintf(ctx.Stderr, "warning: this repo has no trunk branch (main/master), and without --worktree the child branches in the MAIN checkout — `dacli ship` will then have nowhere to integrate into. Create a trunk first (git checkout -b main && git commit), or spawn with --worktree.\n")
		return
	}
	head, err := gitx.Run(w.Root, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return
	}
	if h := strings.TrimSpace(head); strings.HasPrefix(h, "dacli/") {
		fmt.Fprintf(ctx.Stderr, "warning: the main checkout is on task branch %s, not trunk — work spawned here lands on that branch and `dacli ship` integrates from trunk. Switch back (git checkout main) or use --worktree so each agent gets its own branch.\n", h)
	}
}

func taskBranch(t *store.Task) string {
	return fmt.Sprintf("dacli/%03d-%s", t.Seq, t.Slug)
}

// validateReviewTarget keeps local reviews local: unlike PR-first reviews,
// their target is the task's canonical branch and must exist before a child is
// minted. A missing branch used to surface only as a failed gh lookup inside
// the child, leaving a detached run with no visible result (issue #714).
func validateReviewTarget(w *workspace.Workspace, t *store.Task, f *clikit.Flags) error {
	if !f.Bool("review") || f.Bool("pr") {
		return nil
	}
	branch := taskBranch(t)
	if !gitx.Available() {
		return clikit.Refusedf("local review target %s needs git to inspect task branch %s — install git or re-run with --pr", t.ID, branch)
	}
	if !gitx.BranchExists(w.Root, branch) {
		return clikit.Refusedf("local review target %s has no task branch %s — create or restore it, or re-run with --pr", t.ID, branch)
	}
	return nil
}

// resolveSpawnWorkDir preserves a task checkout when spawn is invoked from
// inside it without --worktree. workspace.Find intentionally redirects that
// caller to the shared workspace root, so using w.Root as the runtime cwd loses
// the code checkout that owns the task branch (issue #673).
//
// Only Git's registered worktree/branch pair is trusted. Directory names are
// human-readable conveniences and can be renamed or copied; treating one as
// attribution would let a same-looking but unrelated tree receive the run.
func resolveSpawnWorkDir(w *workspace.Workspace, t *store.Task, cwd string, explicit bool) (string, bool, error) {
	if explicit {
		return w.WorktreePath(t.Project, t.Seq, t.Slug), true, nil
	}
	caller, root, wtRoot := resolvedPath(cwd), resolvedPath(w.Root), resolvedPath(w.WorktreesDir())
	if (caller == root || strings.HasPrefix(caller, root+string(filepath.Separator))) &&
		caller != wtRoot && !strings.HasPrefix(caller, wtRoot+string(filepath.Separator)) {
		// Preserve the historical no-git path for the main checkout. A plain
		// workspace (including unit-test fixtures) has no worktree registry to
		// consult, and an unrelated task worktree elsewhere must not pull a main
		// checkout spawn away from its caller.
		return w.Root, false, nil
	}
	wts, err := gitx.ListWorktrees(w.Root)
	if err != nil {
		return "", false, fmt.Errorf("cannot resolve spawn worktree: %w", err)
	}
	return resolveSpawnWorkDirFrom(w, t, cwd, false, wts)
}

// resolveSuperviseWorkDir preserves the main-checkout default except when root
// has durably reclaimed this task's checkout. In that recovery state the
// transfer identifies the canonical task worktree that correction turns must
// resume in; launching from main would lose governed commit attribution.
func resolveSuperviseWorkDir(w *workspace.Workspace, t *store.Task, cwd string) (string, bool, error) {
	workDir, isolated, err := resolveSpawnWorkDir(w, t, cwd, false)
	if err != nil || isolated || resolvedPath(cwd) != resolvedPath(w.Root) {
		return workDir, isolated, err
	}

	entries, err := os.ReadDir(w.RunsDir())
	if errors.Is(err, fs.ErrNotExist) {
		return workDir, isolated, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read recovery runs: %w", err)
	}
	var transferred string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		runDir := w.RunDir(entry.Name())
		rec, err := procmon.ReadRecord(filepath.Join(runDir, "proc.txt"))
		if err != nil || rec.Task != t.ID {
			continue
		}
		path, found, err := rootTransferredWorktree(filepath.Join(runDir, "worktree-transfer.txt"), taskBranch(t))
		if err != nil {
			return "", false, fmt.Errorf("read task recovery transfer in run %s: %w", rec.RunID, err)
		}
		if !found {
			continue
		}
		if transferred != "" && resolvedPath(transferred) != resolvedPath(path) {
			return "", false, clikit.Refusedf("task %s has multiple root-reclaimed worktrees; inspect recovery transfers before supervising", t.ID)
		}
		transferred = path
	}
	if transferred == "" {
		return workDir, isolated, nil
	}

	wts, err := gitx.ListWorktrees(w.Root)
	if err != nil {
		return "", false, fmt.Errorf("cannot resolve recovered task worktree: %w", err)
	}
	wantBranch := taskBranch(t)
	var candidates []gitx.Worktree
	for _, wt := range wts {
		if wt.Branch == wantBranch {
			candidates = append(candidates, wt)
		}
	}
	if len(candidates) != 1 || resolvedPath(candidates[0].Path) != resolvedPath(transferred) {
		// Reuse the established ambiguity refusal when it applies; otherwise a
		// transfer that no longer names the registered canonical branch is not
		// sufficient authority to redirect a root-owned correction.
		if len(candidates) > 1 {
			return resolveSpawnWorkDirFrom(w, t, transferred, false, wts)
		}
		return "", false, clikit.Refusedf("root recovery transfer for task %s does not name its registered canonical branch %s", t.ID, wantBranch)
	}
	return filepath.Clean(candidates[0].Path), true, nil
}

// rootTransferredWorktree reads only the binding needed to resume a root-owned
// recovery. The vcs slice owns writing this small text record; execution keeps
// its reader local to preserve feature-slice isolation.
func rootTransferredWorktree(path, wantBranch string) (string, bool, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if ok {
			values[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	if values["new_owner"] != agentid.RootID {
		return "", false, nil
	}
	if values["worktree"] == "" || values["branch"] == "" {
		return "", false, fmt.Errorf("missing worktree or branch")
	}
	if values["branch"] != wantBranch {
		return "", false, clikit.Refusedf("recovery transfer branch %s does not match task branch %s", values["branch"], wantBranch)
	}
	return filepath.Clean(values["worktree"]), true, nil
}

func resolveSpawnWorkDirFrom(w *workspace.Workspace, t *store.Task, cwd string, explicit bool, wts []gitx.Worktree) (string, bool, error) {
	if explicit {
		return w.WorktreePath(t.Project, t.Seq, t.Slug), true, nil
	}
	wantBranch := taskBranch(t)
	var candidates []gitx.Worktree
	for _, wt := range wts {
		if wt.Branch == wantBranch {
			candidates = append(candidates, wt)
		}
	}
	if len(candidates) > 1 {
		return "", false, clikit.Refusedf("task %s has multiple registered worktrees on %s; refusing to choose between them — inspect `git worktree list`, then run `git worktree remove <duplicate-path>` and retry", t.ID, wantBranch)
	}

	caller := resolvedPath(cwd)
	var current *gitx.Worktree
	for i := range wts {
		path := resolvedPath(wts[i].Path)
		if caller == path || strings.HasPrefix(caller, path+string(filepath.Separator)) {
			// Prefer the most specific registration if a repository is nested in
			// another checkout. The main checkout must not mask its linked child.
			if current == nil || len(path) > len(resolvedPath(current.Path)) {
				current = &wts[i]
			}
		}
	}
	if current == nil || resolvedPath(current.Path) == resolvedPath(w.Root) {
		return w.Root, false, nil
	}
	if current.Branch != wantBranch {
		return "", false, clikit.Refusedf("current worktree is on %s, not task %s branch %s — run `dacli spawn --task %s --worktree` from the main checkout", current.Branch, t.ID, wantBranch, t.ID)
	}
	return filepath.Clean(current.Path), true, nil
}

// Git canonicalizes macOS's /tmp symlink to /private/tmp in porcelain output.
// Resolve both sides before containment checks or a real linked worktree can
// look unrelated solely because the caller used the shorter spelling.
func resolvedPath(path string) string {
	clean := filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(clean); err == nil {
		return filepath.Clean(resolved)
	}
	return clean
}

// hasAnyTrunk reports whether a conventional trunk branch exists at all.
func hasAnyTrunk(root string) bool {
	for _, b := range []string{"main", "master"} {
		if gitx.BranchExists(root, b) {
			return true
		}
	}
	return false
}

// cmdRuntimeRm is the removal inverse of the corresponding add. Every creatable object
// used to need a text editor to undo, which made a mistake a command made into
// a mistake only a human could fix (task 293).
func cmdRuntimeRm(ctx *clikit.Ctx, args []string) error {
	w, _, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	f, _ := clikit.ParseFlags(args)
	if err := f.Reject(); err != nil {
		return err
	}
	if len(f.Pos) < 1 {
		return clikit.Usagef("usage: dacli runtime rm <name>")
	}
	name := f.Pos[0]
	if err := store.RemoveRuntime(w, name); err != nil {
		var ref store.ErrReferenced
		if errors.As(err, &ref) {
			// A dangling reference is worse than the mistake being removed, so
			// name what still points here rather than deleting and letting it
			// fail later at spawn time.
			return clikit.Refusedf("%v — retire or repoint them first", ref)
		}
		return err
	}
	fmt.Fprintf(ctx.Stdout, "removed runtime %s\n", name)
	return nil
}
