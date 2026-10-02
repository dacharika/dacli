package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mlnomadpy/dacli/internal/mdstore"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

// Runtime is archived coding-agent adapter metadata retained for history,
// diagnostics, and skill export. It does not authorize or launch a provider.
type Runtime struct {
	Name string
	// Harness groups interchangeable read/write adapters for the same coding
	// agent CLI. Routing treats this as an opaque policy label: adapter code
	// declares it; orchestration never infers provider identity from names.
	Harness    string
	Binary     string
	Mode       string   // stdin | arg — how the prompt is delivered
	Flag       string   // arg mode: the flag preceding the prompt (e.g. -p)
	GlobalArgs []string // flags that must precede a runtime subcommand
	Args       []string

	// SandboxRO are the args that put this runtime in a read-only mode. An
	// EMPTY list means the runtime cannot enforce read-only — and per
	// RUNTIMES.md § 8 that is a refusal to spawn ro children, never a silent
	// downgrade.
	SandboxRO []string

	// ROProbe is the local installed binary's verified sandbox state. It is
	// hydrated from .dacli/build (gitignored) rather than the adapter document:
	// a declaration committed by one machine is not evidence about another.
	ROProbe RuntimeROProbe

	// Env lists variable NAMES passed through from the parent environment.
	// Values never enter the workspace; the workspace is committed to git.
	Env []string

	// ModelFlag is the flag that selects a model tier on this CLI (e.g.
	// --model). Empty means the runtime has no model selection — and then
	// role-level model routing is announced as inoperative, not ignored.
	ModelFlag string

	// TokenLimitFlag transmits a hard per-run token ceiling to this CLI. Empty
	// means the runtime cannot enforce --max-tokens; calibrated history remains
	// useful for planning but is never represented as a provider-side cap.
	TokenLimitFlag string

	// ExecutionCapabilities describe build-environment facilities exposed to a
	// child after startup. They are deliberately separate from behavioral
	// launch compatibility: issue #799 showed that a CLI can start and write
	// files while its sandbox still forbids Gradle's coordination socket.
	ExecutionCapabilities []ExecutionCapability

	// Skill delivery (SKILLS.md § 3): where this runtime loads native skills
	// from, and/or which startup context file it reads. Both empty = the
	// inline floor.
	SkillsNativeDir   string
	SkillsContextFile string

	// UsageFormat OPTS this runtime into token-usage capture (F1). Empty is the
	// default and preserves today's behavior byte-for-byte: a plain-text
	// transcript and wall-clock actuals. Set to "stream-json" and dacli asks the
	// child for `--output-format stream-json`, parses the stream into a readable
	// transcript, and records the final `usage` (output tokens, turns, cost) so
	// calibration can measure in tokens instead of a wall-clock proxy.
	UsageFormat string

	// BehavioralPreflight selects the versioned adapter-owned launch handshake.
	// It is independent of UsageFormat: safe execution must not disappear when
	// an operator changes how provider output is parsed (issue #763).
	BehavioralPreflight           string
	BehavioralPreflightProvenance CapabilityProvenance

	// Context declares the provider-specific discovery contract using a
	// provider-neutral vocabulary. The map key is a ContextClass and the value
	// says whether that class is isolated, enumerable, deliberately allowed, or
	// outside dacli's control. Empty is intentionally not treated as isolated.
	Context map[ContextClass]ContextCapability

	Path string
}

// ContextClass is the external state capable of changing an agent's prompt or
// tools. Keep this vocabulary provider-neutral: adapters map vendor discovery
// rules onto it instead of teaching spawn five incompatible flag languages.
type ContextClass string

type ExecutionCapability string

const ExecutionCapabilityLocalCoordinationSocket ExecutionCapability = "local-coordination-socket"

const (
	ContextUserConfig       ContextClass = "user-config"
	ContextRepoInstructions ContextClass = "repository-instructions"
	ContextGlobalSkills     ContextClass = "global-skills"
	ContextPlugins          ContextClass = "plugins-extensions"
	ContextMCP              ContextClass = "mcp-servers"
	ContextEnvironment      ContextClass = "environment-config"
)

var ContextClasses = []ContextClass{ContextUserConfig, ContextRepoInstructions, ContextGlobalSkills, ContextPlugins, ContextMCP, ContextEnvironment}

type ContextCapability string

const (
	ContextIsolated    ContextCapability = "isolated"
	ContextEnumerated  ContextCapability = "enumerated"
	ContextAllowed     ContextCapability = "allowed"
	ContextUnsupported ContextCapability = "unsupported"
)

func FormatContextContract(m map[ContextClass]ContextCapability) []string {
	var out []string
	for _, class := range ContextClasses {
		if capability := m[class]; capability != "" {
			out = append(out, string(class)+"="+string(capability))
		}
	}
	return out
}

func ParseContextContract(lines []string) map[ContextClass]ContextCapability {
	out := map[ContextClass]ContextCapability{}
	for _, line := range lines {
		class, capability, ok := strings.Cut(line, "=")
		if ok {
			out[ContextClass(strings.TrimSpace(class))] = ContextCapability(strings.TrimSpace(capability))
		}
	}
	return out
}

func ContextSummary(rt Runtime) string {
	parts := FormatContextContract(rt.Context)
	if len(parts) == 0 {
		return "context provenance undeclared"
	}
	return "context " + strings.Join(parts, ",")
}

func ValidateContextContract(rt Runtime) error {
	for _, class := range ContextClasses {
		capability := rt.Context[class]
		switch capability {
		case ContextIsolated, ContextEnumerated, ContextAllowed, ContextUnsupported:
		default:
			return fmt.Errorf("runtime %s context class %s has invalid or missing capability %q", rt.Name, class, capability)
		}
	}
	return nil
}

// RuntimeROProbe keeps declaration and observation separate. Unknown is the
// zero value on purpose: an adapter that has not been checked on this install
// must never accidentally become eligible for an ro spawn.
type RuntimeROProbe string

const (
	RuntimeROUnknown  RuntimeROProbe = "unknown"
	RuntimeROVerified RuntimeROProbe = "verified"
	RuntimeROFailed   RuntimeROProbe = "failed"
)

type runtimeProbeCache struct {
	Fingerprint string                     `json:"fingerprint"`
	ReadOnly    RuntimeROProbe             `json:"read_only"`
	Detail      string                     `json:"detail,omitempty"`
	Launch      map[string]json.RawMessage `json:"launch,omitempty"`
}

type CapabilityProvenance string

const (
	ProvenanceDeclared CapabilityProvenance = "declared"
	ProvenanceInferred CapabilityProvenance = "inferred"
	ProvenanceProbed   CapabilityProvenance = "probed"
)

const (
	BehavioralPreflightCodexExecJSONV1 = "codex-exec-json-v1"
	// V2 treats the first valid Codex lifecycle readiness event as the end of
	// the transport probe. V1 waited for a complete model turn, so its fixed
	// deadline measured inference latency rather than startup compatibility.
	BehavioralPreflightCodexExecJSONV2 = "codex-exec-json-v2"
	// ClaudePrintV1 reaches Claude Code's stream-json initialization event. It
	// proves the installed CLI can authenticate before dacli spends a governed
	// run; the adapter owns its invocation and failure prose classification.
	BehavioralPreflightClaudePrintV1 = "claude-print-v1"
)

// CreateRuntime serializes archived runtime metadata to .dacli/runtimes/<name>.md.
// Retained fixture builders use it; no shipped command launches this declaration.
func CreateRuntime(w *workspace.Workspace, actor string, rt Runtime, note string) error {
	if rt.Name == "" || rt.Binary == "" {
		return fmt.Errorf("a runtime needs at least --name and --binary")
	}
	if rt.Mode == "" {
		rt.Mode = "stdin"
	}
	for _, capability := range rt.ExecutionCapabilities {
		if capability != ExecutionCapabilityLocalCoordinationSocket {
			return fmt.Errorf("runtime %s has unknown execution capability %q", rt.Name, capability)
		}
	}
	// Same containment guard its siblings carry: the name becomes a filename,
	// and a runtime file describes what a spawned child is allowed to do, so
	// one written outside .dacli is both an escape and a policy document in a
	// place nothing audits.
	if !workspace.SafeSegment(rt.Name) {
		return fmt.Errorf("invalid runtime name %q: must be a single path segment without '/' or '..'", rt.Name)
	}
	if rt.Harness != "" && !workspace.SafeSegment(rt.Harness) {
		return fmt.Errorf("invalid runtime harness %q: must be one identifier without '/' or '..'", rt.Harness)
	}
	path := w.RuntimePath(rt.Name)
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("runtime %q already exists", rt.Name)
	}

	d := &mdstore.Doc{}
	d.Front.Set("id", "rt-"+rt.Name)
	d.Front.Set("kind", string(model.KindRuntime))
	d.Front.Set("created", now())
	d.Front.Set("created_by", actor)
	d.Front.Set("name", rt.Name)
	if rt.Harness != "" {
		d.Front.Set("harness", rt.Harness)
	}
	d.Front.Set("binary", rt.Binary)
	d.Front.Set("invoke_mode", rt.Mode)
	if rt.Flag != "" {
		d.Front.Set("invoke_flag", rt.Flag)
	}
	setInline := func(k string, v []string) {
		if len(v) > 0 {
			d.Front.SetList(k, v)
		}
	}
	setInline("invoke_args", rt.Args)
	setInline("global_args", rt.GlobalArgs)
	setInline("sandbox_ro_args", rt.SandboxRO)
	setInline("env_passthrough", rt.Env)
	if len(rt.ExecutionCapabilities) > 0 {
		values := make([]string, len(rt.ExecutionCapabilities))
		for i, capability := range rt.ExecutionCapabilities {
			values[i] = string(capability)
		}
		setInline("execution_capabilities", values)
	}
	if rt.ModelFlag != "" {
		d.Front.Set("model_flag", rt.ModelFlag)
	}
	if rt.TokenLimitFlag != "" {
		d.Front.Set("token_limit_flag", rt.TokenLimitFlag)
	}
	if rt.SkillsNativeDir != "" {
		d.Front.Set("skills_native_dir", rt.SkillsNativeDir)
	}
	if rt.SkillsContextFile != "" {
		d.Front.Set("skills_context_file", rt.SkillsContextFile)
	}
	if rt.UsageFormat != "" {
		d.Front.Set("usage_format", rt.UsageFormat)
	}
	if rt.BehavioralPreflight != "" {
		d.Front.Set("behavioral_preflight", rt.BehavioralPreflight)
	}
	if len(rt.Context) > 0 {
		d.Front.SetList("context_provenance", FormatContextContract(rt.Context))
	}
	if note == "" {
		note = "Historical runtime declaration; these flags and cached observations do not authorize a native session."
	}
	d.Sections = []mdstore.Section{{Level: 1, Title: rt.Name, Content: note + "\n"}}
	return mdstore.WriteFile(path, d)
}

// parseRuntime builds a Runtime from a parsed adapter doc at path. It returns
// ok=false for a malformed adapter (no name or no binary), matching the filter
// LoadRuntimes has always applied.
func parseRuntime(d *mdstore.Doc, path string) (Runtime, bool) {
	rt := Runtime{Path: path, ROProbe: RuntimeROUnknown}
	rt.Name, _ = d.Front.Get("name")
	rt.Harness, _ = d.Front.Get("harness")
	rt.Binary, _ = d.Front.Get("binary")
	rt.Mode, _ = d.Front.Get("invoke_mode")
	rt.Flag, _ = d.Front.Get("invoke_flag")
	rt.Args = d.Front.GetList("invoke_args")
	rt.GlobalArgs = d.Front.GetList("global_args")
	rt.SandboxRO = d.Front.GetList("sandbox_ro_args")
	rt.Env = d.Front.GetList("env_passthrough")
	for _, capability := range d.Front.GetList("execution_capabilities") {
		rt.ExecutionCapabilities = append(rt.ExecutionCapabilities, ExecutionCapability(capability))
	}
	rt.ModelFlag, _ = d.Front.Get("model_flag")
	rt.TokenLimitFlag, _ = d.Front.Get("token_limit_flag")
	rt.SkillsNativeDir, _ = d.Front.Get("skills_native_dir")
	rt.SkillsContextFile, _ = d.Front.Get("skills_context_file")
	rt.UsageFormat, _ = d.Front.Get("usage_format")
	if rt.Mode == "" {
		rt.Mode = "stdin"
	}
	rt.BehavioralPreflight, _ = d.Front.Get("behavioral_preflight")
	if rt.BehavioralPreflight != "" {
		rt.BehavioralPreflightProvenance = ProvenanceDeclared
	} else if legacyCodexBehavioralPreflight(rt) {
		// Mature workspaces predate the capability field. Infer only the two
		// exact shipped exec contracts; a binary or runtime name alone is not
		// authority to execute provider-specific probing (issue #763).
		rt.BehavioralPreflight = BehavioralPreflightCodexExecJSONV2
		rt.BehavioralPreflightProvenance = ProvenanceInferred
	} else if legacyClaudeBehavioralPreflight(rt) {
		// The shipped Claude presets also predate the capability field. Infer
		// only their exact -p/stream-json transport contract; a binary or
		// runtime name by itself is not authority for provider-specific probing.
		rt.BehavioralPreflight = BehavioralPreflightClaudePrintV1
		rt.BehavioralPreflightProvenance = ProvenanceInferred
	}
	// Existing adapter documents predate harness policy. Infer only from exact
	// adapter-owned launch contracts; otherwise use the runtime name as a safe
	// single-adapter family until the operator recreates it with --harness.
	if rt.Harness == "" {
		switch rt.BehavioralPreflight {
		case BehavioralPreflightCodexExecJSONV1, BehavioralPreflightCodexExecJSONV2:
			rt.Harness = "codex"
		case BehavioralPreflightClaudePrintV1:
			rt.Harness = "claude"
		default:
			rt.Harness = legacyStructuredHarness(rt)
			if rt.Harness == "" {
				rt.Harness = rt.Name
			}
		}
	}
	rt.Context = ParseContextContract(d.Front.GetList("context_provenance"))
	return rt, rt.Name != "" && rt.Binary != ""
}

func legacyStructuredHarness(rt Runtime) string {
	base := strings.TrimSuffix(filepath.Base(rt.Binary), ".exe")
	switch {
	case base == "gemini" && rt.Mode == "arg" && rt.Flag == "-p" && rt.UsageFormat == "gemini-stream-json" && rt.ModelFlag == "--model":
		return "gemini"
	case base == "copilot" && rt.Mode == "arg" && rt.Flag == "-p" && rt.UsageFormat == "copilot-json" && rt.ModelFlag == "--model":
		return "copilot"
	default:
		return ""
	}
}

func legacyClaudeBehavioralPreflight(rt Runtime) bool {
	if filepath.Base(rt.Binary) != "claude" || rt.Mode != "arg" || rt.Flag != "-p" || rt.UsageFormat != "stream-json" || len(rt.GlobalArgs) != 0 {
		return false
	}
	if rt.ModelFlag != "" && rt.ModelFlag != "--model" {
		return false
	}
	return validClaudeToolArgs(rt.Args) && validClaudeToolArgs(rt.SandboxRO)
}

func validClaudeToolArgs(args []string) bool {
	if len(args) == 0 {
		return true
	}
	if len(args) < 2 || args[0] != "--allowedTools" {
		return false
	}
	for _, tool := range args[1:] {
		if tool == "" || strings.HasPrefix(tool, "--") {
			return false
		}
	}
	return true
}

func legacyCodexBehavioralPreflight(rt Runtime) bool {
	if filepath.Base(rt.Binary) != "codex" || rt.Mode != "stdin" || rt.Flag != "" || (len(rt.SandboxRO) > 0 && !equalStrings(rt.SandboxRO, []string{"--sandbox", "read-only"})) {
		return false
	}
	// Bootstrap adapters written before GlobalArgs existed persisted the same
	// supported Codex contract entirely in invoke_args, with optional context
	// isolation and the two shared worktree directories. Parse that narrow
	// grammar instead of requiring the newer storage layout. Unknown flags fail
	// closed, so a provider-shaped custom adapter is never inferred by name.
	args := append(append([]string{}, rt.GlobalArgs...), rt.Args...)
	seen := map[string]bool{}
	disableAllowed := map[string]bool{"plugins": true, "plugin_sharing": true, "remote_plugin": true}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "exec":
			seen["exec"] = true
		case "--json", "--ephemeral", "--ignore-user-config":
			seen[args[i]] = true
		case "--ask-for-approval":
			i++
			if i >= len(args) || args[i] != "never" {
				return false
			}
			seen["approval"] = true
		case "--color":
			i++
			if i >= len(args) || args[i] != "never" {
				return false
			}
			seen["color"] = true
		case "--sandbox":
			i++
			if i >= len(args) || (args[i] != "read-only" && args[i] != "workspace-write") {
				return false
			}
			seen["sandbox"] = true
		case "--disable":
			i++
			if i >= len(args) || !disableAllowed[args[i]] {
				return false
			}
		case "--add-dir":
			i++
			if i >= len(args) || strings.TrimSpace(args[i]) == "" {
				return false
			}
		default:
			return false
		}
	}
	if !seen["sandbox"] && equalStrings(rt.SandboxRO, []string{"--sandbox", "read-only"}) {
		seen["sandbox"] = true
	}
	return seen["exec"] && seen["--json"] && seen["--ephemeral"] && seen["approval"] && seen["color"] && seen["sandbox"]
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func runtimeProbePath(w *workspace.Workspace, name string) string {
	// Runtime documents can be hand-edited, so do not turn their frontmatter
	// name back into a path without the same containment guarantee CreateRuntime
	// applies. Hashing only malformed legacy names keeps even those caches under
	// runtime-probes while preserving readable filenames for valid adapters.
	cacheName := name
	if !workspace.SafeSegment(cacheName) {
		sum := sha256.Sum256([]byte(cacheName))
		cacheName = "invalid-" + hex.EncodeToString(sum[:])
	}
	return filepath.Join(w.Root, workspace.Dir, "build", "runtime-probes", cacheName+".json")
}

func runtimeProbeFingerprint(rt Runtime, binaryPath string) (string, error) {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\x00", rt.Binary, binaryPath, strings.Join(rt.GlobalArgs, "\x00"), strings.Join(rt.SandboxRO, "\x00"))
	b, err := os.ReadFile(binaryPath)
	if err != nil {
		return "", err
	}
	_, _ = h.Write(b)
	return hex.EncodeToString(h.Sum(nil)), nil
}

// HydrateRuntimeROProbe applies a cached verdict only when it describes this
// exact adapter declaration and installed binary. Missing, corrupt, or stale
// cache data safely leaves the state unknown.
func HydrateRuntimeROProbe(w *workspace.Workspace, rt Runtime, binaryPath string) Runtime {
	rt.ROProbe = RuntimeROUnknown
	if len(rt.SandboxRO) == 0 {
		rt.ROProbe = RuntimeROFailed
		return rt
	}
	b, err := os.ReadFile(runtimeProbePath(w, rt.Name))
	if err != nil {
		return rt
	}
	var c runtimeProbeCache
	fingerprint, err := runtimeProbeFingerprint(rt, binaryPath)
	if err != nil || json.Unmarshal(b, &c) != nil || c.Fingerprint != fingerprint {
		return rt
	}
	if c.ReadOnly == RuntimeROVerified || c.ReadOnly == RuntimeROFailed {
		rt.ROProbe = c.ReadOnly
	}
	return rt
}

// SaveRuntimeROProbe records a local probe outside the committed adapter.
func SaveRuntimeROProbe(w *workspace.Workspace, rt Runtime, binaryPath string, state RuntimeROProbe, detail string) error {
	fingerprint, err := runtimeProbeFingerprint(rt, binaryPath)
	if err != nil {
		return err
	}
	dir := filepath.Dir(runtimeProbePath(w, rt.Name))
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	cache := runtimeProbeCache{Fingerprint: fingerprint, ReadOnly: state, Detail: detail}
	if old, readErr := os.ReadFile(runtimeProbePath(w, rt.Name)); readErr == nil {
		var existing runtimeProbeCache
		if json.Unmarshal(old, &existing) == nil && existing.Fingerprint == fingerprint {
			cache.Launch = existing.Launch
		}
	}
	b, err := json.Marshal(cache)
	if err != nil {
		return err
	}
	return mdstore.WriteBytes(runtimeProbePath(w, rt.Name), append(b, '\n'), 0o600)
}

// LoadRuntimes parses every adapter.
func LoadRuntimes(w *workspace.Workspace) ([]Runtime, error) {
	entries, err := os.ReadDir(w.RuntimesDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // no runtimes dir yet is not an error
		}
		return nil, err // a real I/O/permission failure must not read as "empty"
	}
	var out []Runtime
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		path := w.RuntimePath(strings.TrimSuffix(e.Name(), ".md"))
		d, err := mdstore.ReadFile(path)
		if err != nil {
			continue
		}
		if rt, ok := parseRuntime(d, path); ok {
			out = append(out, rt)
		}
	}
	return out, nil
}

// LoadRuntime reads one adapter by name from its exact file, rather than
// scanning the whole directory through LoadRuntimes.
func LoadRuntime(w *workspace.Workspace, name string) (Runtime, error) {
	path := w.RuntimePath(name)
	d, err := mdstore.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Runtime{}, ErrNotFound{Ref: "runtime/" + name}
		}
		return Runtime{}, err
	}
	if rt, ok := parseRuntime(d, path); ok {
		return rt, nil
	}
	return Runtime{}, ErrNotFound{Ref: "runtime/" + name}
}
