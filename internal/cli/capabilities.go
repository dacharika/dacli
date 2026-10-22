package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"github.com/mlnomadpy/dacli/internal/buildinfo"
	"github.com/mlnomadpy/dacli/internal/capabilities"
	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/eventlog"
	"github.com/mlnomadpy/dacli/internal/mcp"
	"github.com/mlnomadpy/dacli/internal/prompts"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

var usageFlag = regexp.MustCompile(`--[a-z][a-z0-9-]*`)

// manifestCommandTable breaks the static initializer cycle: the command table
// contains cmdCapabilities, while the handler must inspect that whole table.
var manifestCommandTable func() []Command

func stableSegment(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.NewReplacer(" ", ".", "_", "-", "/", ".").Replace(s)
	return s
}

func executableIdentity() string {
	path, err := os.Executable()
	if err != nil {
		return "unknown"
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	if absolute, err := filepath.Abs(path); err == nil {
		path = absolute
	}
	return filepath.Clean(path)
}

func commandCapability(c Command) capabilities.Command {
	prefix := "cli.command." + stableSegment(c.Path)
	names := map[string]string{}
	for _, raw := range usageFlag.FindAllString(c.Usage, -1) {
		names[strings.TrimPrefix(raw, "--")] = raw
	}
	for _, token := range strings.Fields(c.Usage) {
		raw := strings.Trim(token, "[]()|,")
		if len(raw) == 2 && raw[0] == '-' && raw[1] != '-' {
			names["short-"+raw[1:]] = raw
		}
	}
	flags := make([]capabilities.Flag, 0, len(names))
	for id, name := range names {
		flags = append(flags, capabilities.Flag{ID: prefix + ".flag." + id, Name: name, SchemaVersion: 1})
	}
	return capabilities.Command{ID: prefix, Path: c.Path, Aliases: []string{}, Flags: flags, JSON: c.JSON, Mutates: c.Mutates, Usage: c.Usage, SchemaVersion: 1}
}

func capabilityManifest() capabilities.Manifest {
	m := capabilities.Manifest{
		SchemaVersion: capabilities.ManifestSchema,
		Binary:        capabilities.Binary{Name: "dacli", Version: buildinfo.Version, Path: executableIdentity(), GOOS: runtime.GOOS, GOARCH: runtime.GOARCH},
		State: []capabilities.VersionedCapability{
			{ID: "state.workspace", SchemaVersion: workspace.FormatVersion},
			{ID: "state.event", SchemaVersion: eventlog.EventSchemaVersion},
			{ID: "state.role-skill", SchemaVersion: 1},
			{ID: "state.root-handoff", SchemaVersion: 1},
		},
		MCP:     capabilities.MCP{ProtocolVersion: mcp.ProtocolVersion(), ServerVersion: mcp.ServerVersion(), ToolSchema: mcp.ToolSchemaVersion(), Tools: []capabilities.VersionedCapability{}},
		Prompt:  capabilities.Prompt{Schema: prompts.Schema, Version: prompts.Version},
		Runtime: capabilities.RuntimeAdapters{SchemaVersion: 1, Capabilities: []capabilities.VersionedCapability{}},
	}
	for _, c := range manifestCommandTable() {
		m.Commands = append(m.Commands, commandCapability(c))
	}
	for _, tool := range mcp.ToolCapabilities() {
		m.MCP.Tools = append(m.MCP.Tools, capabilities.VersionedCapability{ID: "mcp.tool." + stableSegment(tool.Name), SchemaVersion: tool.SchemaVersion})
	}
	m.Normalize()
	return m
}

func cmdCapabilities(ctx *Ctx, args []string) error {
	if f, err := clikit.ParseFlags(args); err != nil {
		return err
	} else if err := f.Reject(); err != nil {
		return err
	}
	m := capabilityManifest()
	if ctx.JSON {
		return json.NewEncoder(ctx.Stdout).Encode(m)
	}
	fmt.Fprintf(ctx.Stdout, "dacli %s capability manifest v%d: %d commands, %d MCP tools\n", m.Binary.Version, m.SchemaVersion, len(m.Commands), len(m.MCP.Tools))
	fmt.Fprintln(ctx.Stdout, "Use `dacli capabilities --json` for the stable machine-readable contract.")
	return nil
}

func compatibilityRequirementsPath(cwd, requested string) (string, error) {
	if requested != "" && requested != "true" {
		if !filepath.IsAbs(requested) {
			requested = filepath.Join(cwd, requested)
		}
		return filepath.Clean(requested), nil
	}
	var candidates []string
	if env := os.Getenv("DACLI_SKILL_REQUIREMENTS"); env != "" {
		candidates = append(candidates, env)
	}
	for _, rel := range []string{"skills/dacli/capabilities.json", ".codex/skills/dacli/capabilities.json", ".agents/skills/dacli/capabilities.json"} {
		candidates = append(candidates, filepath.Join(cwd, rel))
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".codex/skills/dacli/capabilities.json"), filepath.Join(home, ".agents/skills/dacli/capabilities.json"))
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			return filepath.Clean(candidate), nil
		}
	}
	return "", clikit.Usagef("no skill capability requirement document found; pass `dacli version --compatibility <path>` or set DACLI_SKILL_REQUIREMENTS")
}

func cmdCompatibility(ctx *Ctx, requested string) error {
	path, err := compatibilityRequirementsPath(ctx.Cwd, requested)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read skill capability requirements %s: %w", path, err)
	}
	var req capabilities.Requirements
	if err := json.Unmarshal(raw, &req); err != nil {
		return clikit.Usagef("invalid skill capability requirements %s: %v", path, err)
	}
	diagnosis, err := capabilities.Diagnose(capabilityManifest(), req, path)
	if err != nil {
		return clikit.Refusedf("skill compatibility refused: %v", err)
	}
	if ctx.JSON {
		if err := json.NewEncoder(ctx.Stdout).Encode(diagnosis); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(ctx.Stdout, "skill requirements: %s (generated by %s)\nbinary: %s %s at %s\n", path, diagnosis.GeneratedByVersion, diagnosis.Binary.Name, diagnosis.Binary.Version, diagnosis.Binary.Path)
		for _, finding := range diagnosis.Findings {
			line := fmt.Sprintf("%s: %s", finding.Status, finding.ID)
			if finding.Fallback != "" {
				line += " — fallback: " + finding.Fallback
			}
			fmt.Fprintln(ctx.Stdout, line)
		}
	}
	if !diagnosis.Compatible {
		var missing []string
		for _, finding := range diagnosis.Findings {
			if finding.Kind == "required" && finding.Status != "supported" {
				missing = append(missing, finding.ID)
			}
		}
		sort.Strings(missing)
		return clikit.Refusedf("installed dacli is incompatible with required skill capabilities: %s; update the binary or use guidance matching this manifest", strings.Join(missing, ", "))
	}
	return nil
}
