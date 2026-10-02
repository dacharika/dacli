package cli

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mlnomadpy/dacli/internal/capabilities"
	"github.com/mlnomadpy/dacli/internal/mcp"
)

// capabilityRegistryGolden is the digest of the complete deterministic
// registry projection below. A command/flag/tool addition or removal must be
// reviewed as a compatibility-surface change and deliberately update it.
const capabilityRegistryGolden = "4ad35596f1497dd92a03f388bc2b8d4e6caaf02716c98f643c7bff7cc89a8dc2"

func TestCapabilityRegistryGolden(t *testing.T) {
	m := capabilityManifest()
	projection := struct {
		Schema   int                                `json:"schema"`
		State    []capabilities.VersionedCapability `json:"state"`
		Commands []capabilities.Command             `json:"commands"`
		MCP      capabilities.MCP                   `json:"mcp"`
		Prompt   capabilities.Prompt                `json:"prompt"`
		Runtime  capabilities.RuntimeAdapters       `json:"runtime"`
	}{m.SchemaVersion, m.State, m.Commands, m.MCP, m.Prompt, m.Runtime}
	raw, err := json.Marshal(projection)
	if err != nil {
		t.Fatal(err)
	}
	got := fmt.Sprintf("%x", sha256.Sum256(raw))
	if got != capabilityRegistryGolden {
		t.Fatalf("capability registry golden = %s, want %s", got, capabilityRegistryGolden)
	}
}

func TestCapabilityManifestCoversAuthoritativeCommandAndMCPRegistries(t *testing.T) {
	m := capabilityManifest()
	byPath := map[string]capabilities.Command{}
	ids := map[string]bool{}
	for _, advertised := range m.Commands {
		byPath[advertised.Path] = advertised
		if ids[advertised.ID] {
			t.Fatalf("duplicate capability id %q", advertised.ID)
		}
		ids[advertised.ID] = true
		for _, flag := range advertised.Flags {
			ids[flag.ID] = true
		}
	}
	if len(byPath) != len(commands) {
		t.Fatalf("manifest has %d commands, registry has %d", len(byPath), len(commands))
	}
	for _, registered := range commands {
		advertised, ok := byPath[registered.Path]
		if !ok {
			t.Errorf("registered command %q omitted from manifest", registered.Path)
			continue
		}
		if advertised.JSON != registered.JSON || advertised.Mutates != registered.Mutates || advertised.Usage != registered.Usage {
			t.Errorf("%s metadata drift: %#v vs %#v", registered.Path, advertised, registered)
		}
		for _, raw := range usageFlag.FindAllString(registered.Usage, -1) {
			id := advertised.ID + ".flag." + strings.TrimPrefix(raw, "--")
			if !ids[id] {
				t.Errorf("%s registered usage flag %s omitted from manifest", registered.Path, raw)
			}
		}
	}
	logs := byPath["logs"]
	if !ids[logs.ID+".flag.short-f"] {
		t.Error("registered short flag logs -f omitted from manifest")
	}
	toolIDs := map[string]bool{}
	for _, tool := range m.MCP.Tools {
		toolIDs[tool.ID] = true
	}
	for _, tool := range mcpToolCapabilitiesForTest() {
		if !toolIDs["mcp.tool."+stableSegment(tool)] {
			t.Errorf("MCP tool %q omitted from manifest", tool)
		}
	}
}

// Kept as a seam so the mutation for the MCP registry assertion is a one-line
// omission rather than a second handwritten expected list.
func mcpToolCapabilitiesForTest() []string {
	var out []string
	for _, tool := range mcp.ToolCapabilities() {
		out = append(out, tool.Name)
	}
	return out
}

func TestCapabilitiesJSONIsDeterministicAndIncludesBinaryIdentity(t *testing.T) {
	dir := t.TempDir()
	first, msg, code := executor(dir)([]string{"capabilities"}, true)
	if code != 0 {
		t.Fatalf("capabilities --json: exit %d: %s", code, msg)
	}
	second, msg, code := executor(dir)([]string{"capabilities"}, true)
	if code != 0 {
		t.Fatalf("capabilities --json: exit %d: %s", code, msg)
	}
	if first != second {
		t.Fatal("capability output changed between identical invocations")
	}
	var m capabilities.Manifest
	if err := json.Unmarshal([]byte(first), &m); err != nil {
		t.Fatal(err)
	}
	if m.SchemaVersion != 1 || m.Binary.Path == "" || m.Binary.Version == "" {
		t.Fatalf("manifest identity incomplete: %#v", m.Binary)
	}
	if len(m.Commands) == 0 || len(m.MCP.Tools) == 0 || len(m.Runtime.Capabilities) == 0 {
		t.Fatal("manifest registry sections must not be empty")
	}
}

func TestVersionCompatibilityClassifiesOptionalAndRefusesRequiredGaps(t *testing.T) {
	dir := t.TempDir()
	optional := filepath.Join(dir, "optional.json")
	if err := os.WriteFile(optional, []byte(`{"schema_version":1,"generated_by_version":"fixture-v1","optional":[{"id":"cli.command.future","schema_version":1,"fallback":"use cli escape hatch"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	out := run(t, dir, 0, "version", "--compatibility", optional)
	if !strings.Contains(out, "optional-missing: cli.command.future") || !strings.Contains(out, "fallback: use cli escape hatch") {
		t.Fatalf("optional diagnosis not actionable:\n%s", out)
	}

	required := filepath.Join(dir, "required.json")
	if err := os.WriteFile(required, []byte(`{"schema_version":1,"required":[{"id":"cli.command.future","schema_version":1}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, msg, code := executor(dir)([]string{"version", "--compatibility", required}, true)
	if code != 3 {
		t.Fatalf("required compatibility gap: exit %d, want 3: %s", code, msg)
	}
	out = stdout + msg
	if !strings.Contains(out, `"status":"required-missing"`) || !strings.Contains(out, `"compatible":false`) {
		t.Fatalf("required gap did not fail closed with JSON diagnosis:\n%s", out)
	}
}

func TestVersionCompatibilityDiscoversSkillRequirements(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".codex", "skills", "dacli", "capabilities.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"schema_version":1,"required":[{"id":"cli.command.status","schema_version":1}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	out := run(t, dir, 0, "version", "--compatibility")
	if !strings.Contains(out, path) || !strings.Contains(out, "supported: cli.command.status") {
		t.Fatalf("discovery did not select local installed skill requirements:\n%s", out)
	}
}

func TestShippedSkillRequirementsMatchCurrentManifest(t *testing.T) {
	_, here, _, _ := runtime.Caller(0)
	path := filepath.Clean(filepath.Join(filepath.Dir(here), "..", "..", "skills", "dacli", "capabilities.json"))
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var req capabilities.Requirements
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatal(err)
	}
	diagnosis, err := capabilities.Diagnose(capabilityManifest(), req, path)
	if err != nil {
		t.Fatal(err)
	}
	if !diagnosis.Compatible {
		t.Fatalf("shipped skill requirements are incompatible with the same source tree: %#v", diagnosis.Findings)
	}
	for _, finding := range diagnosis.Findings {
		if finding.Status != "supported" {
			t.Errorf("shipped skill capability %s = %s", finding.ID, finding.Status)
		}
	}
}
