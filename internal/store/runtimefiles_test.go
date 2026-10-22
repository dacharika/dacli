package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/mlnomadpy/dacli/internal/workspace"
)

func runtimeWorkspace(t *testing.T) *workspace.Workspace {
	t.Helper()
	w, err := workspace.Init(t.TempDir(), "test")
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	return w
}

func TestLegacyRuntimeHarnessInferenceIsExactAndSafe(t *testing.T) {
	w := runtimeWorkspace(t)
	legacy := Runtime{Name: "old-gemini-writer", Binary: "gemini", Mode: "arg", Flag: "-p", ModelFlag: "--model", UsageFormat: "gemini-stream-json"}
	if err := CreateRuntime(w, "a-root", legacy, ""); err != nil {
		t.Fatal(err)
	}
	got, err := LoadRuntime(w, legacy.Name)
	if err != nil || got.Harness != "gemini" {
		t.Fatalf("legacy shipped contract harness = %q, err=%v", got.Harness, err)
	}
	custom := Runtime{Name: "custom", Binary: "gemini", Mode: "stdin"}
	if err := CreateRuntime(w, "a-root", custom, ""); err != nil {
		t.Fatal(err)
	}
	got, err = LoadRuntime(w, custom.Name)
	if err != nil || got.Harness != "custom" {
		t.Fatalf("ambiguous custom adapter harness = %q, err=%v; want safe runtime-name family", got.Harness, err)
	}
}

func TestRuntimeROProbeCacheIsLocalAndInvalidatesOnAdapterChange(t *testing.T) {
	w := runtimeWorkspace(t)
	bin := filepath.Join(t.TempDir(), "agent")
	if err := os.WriteFile(bin, []byte("fixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	rt := Runtime{Name: "fixture", Binary: bin, SandboxRO: []string{"--allowedTools", "Read"}}
	if err := SaveRuntimeROProbe(w, rt, bin, RuntimeROVerified, "test"); err != nil {
		t.Fatal(err)
	}
	if got := HydrateRuntimeROProbe(w, rt, bin); got.ROProbe != RuntimeROVerified {
		t.Fatalf("matching cached probe = %q, want verified", got.ROProbe)
	}
	changed := rt
	changed.SandboxRO = []string{"--allowedTools", "Read,Grep"}
	if got := HydrateRuntimeROProbe(w, changed, bin); got.ROProbe != RuntimeROUnknown {
		t.Fatalf("stale cached probe = %q, want unknown", got.ROProbe)
	}
}

func TestRuntimeROProbeCacheInvalidatesOnSameMetadataBinaryReplacement(t *testing.T) {
	w := runtimeWorkspace(t)
	bin := filepath.Join(t.TempDir(), "agent")
	if err := os.WriteFile(bin, []byte("original"), 0o755); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(bin)
	if err != nil {
		t.Fatal(err)
	}
	rt := Runtime{Name: "fixture", Binary: bin, SandboxRO: []string{"--allowedTools", "Read"}}
	if err := SaveRuntimeROProbe(w, rt, bin, RuntimeROVerified, "test"); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(bin, []byte("replaced"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(bin, fi.ModTime(), fi.ModTime()); err != nil {
		t.Fatal(err)
	}
	replaced, err := os.Stat(bin)
	if err != nil {
		t.Fatal(err)
	}
	if replaced.Size() != fi.Size() || !replaced.ModTime().Equal(fi.ModTime()) {
		t.Fatalf("replacement metadata changed: size %d/%d mtime %v/%v", replaced.Size(), fi.Size(), replaced.ModTime(), fi.ModTime())
	}

	if got := HydrateRuntimeROProbe(w, rt, bin); got.ROProbe != RuntimeROUnknown {
		t.Fatalf("cached probe after byte-different same-metadata replacement = %q, want unknown", got.ROProbe)
	}
}

func TestRuntimeROProbeCacheContainsHandEditedNames(t *testing.T) {
	w := runtimeWorkspace(t)
	bin := filepath.Join(t.TempDir(), "agent")
	if err := os.WriteFile(bin, []byte("fixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	rt := Runtime{Name: "../../escaped", Binary: bin, SandboxRO: []string{"--allowedTools", "Read"}}
	if err := SaveRuntimeROProbe(w, rt, bin, RuntimeROVerified, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(w.Root, workspace.Dir, "escaped.json")); !os.IsNotExist(err) {
		t.Fatalf("malformed runtime name escaped the probe cache: %v", err)
	}
	if got := HydrateRuntimeROProbe(w, rt, bin); got.ROProbe != RuntimeROVerified {
		t.Fatalf("contained probe cache = %q, want verified", got.ROProbe)
	}
}

// TestRuntimeInlineListRoundTripsCommaContainingElements proves a list
// element containing a literal comma -- like the claude-code preset's
// --allowedTools value -- survives CreateRuntime + LoadRuntime as ONE
// element, not silently re-split into several argv tokens.
func TestRuntimeInlineListRoundTripsCommaContainingElements(t *testing.T) {
	w := runtimeWorkspace(t)

	sandboxRO := []string{"--allowedTools", "Read,Grep,Glob,LS,Bash(dacli:*)"}
	args := []string{"-p", "a, b, c"}
	env := []string{"HOME", "PATH,EXTRA"}

	rt := Runtime{
		Name:      "claude-code",
		Binary:    "claude",
		Mode:      "arg",
		Flag:      "-p",
		Args:      args,
		SandboxRO: sandboxRO,
		Env:       env,
	}
	if err := CreateRuntime(w, "a-root", rt, ""); err != nil {
		t.Fatalf("CreateRuntime: %v", err)
	}

	got, err := LoadRuntime(w, "claude-code")
	if err != nil {
		t.Fatalf("LoadRuntime: %v", err)
	}

	check := func(field string, want, got []string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s: got %d elements %#v, want %d elements %#v", field, len(got), got, len(want), want)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: got %#v, want %#v", field, got, want)
		}
	}
	check("SandboxRO", sandboxRO, got.SandboxRO)
	check("Args", args, got.Args)
	check("Env", env, got.Env)
}

// Historical launch evidence remains readable opaque data after its authorizing
// implementation is removed. Updating the independent cached RO observation
// must not erase that record or require the retired launch-contract schema.
func TestRuntimeProbeUpdatePreservesOpaqueHistoricalLaunchEvidence(t *testing.T) {
	w := runtimeWorkspace(t)
	binary := filepath.Join(t.TempDir(), "fixture-runtime")
	if err := os.WriteFile(binary, []byte("fixture binary bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	rt := Runtime{Name: "historical", Binary: binary, SandboxRO: []string{"--sandbox", "read-only"}}
	if err := SaveRuntimeROProbe(w, rt, binary, RuntimeROVerified, "archived observation"); err != nil {
		t.Fatal(err)
	}
	path := runtimeProbePath(w, rt.Name)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cache map[string]json.RawMessage
	if err := json.Unmarshal(raw, &cache); err != nil {
		t.Fatal(err)
	}
	historical := json.RawMessage(`{"old-fingerprint":{"state":"compatible","contract":{"schema":"historical-unknown/v7","opaque":[1,"retained"]}}}`)
	cache["launch"] = historical
	raw, err = json.Marshal(cache)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SaveRuntimeROProbe(w, rt, binary, RuntimeROFailed, "updated independent observation"); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cache = nil
	if err := json.Unmarshal(raw, &cache); err != nil {
		t.Fatal(err)
	}
	if len(cache["launch"]) == 0 {
		t.Fatal("historical launch evidence removed while updating RO observation")
	}
	var got, want any
	if err := json.Unmarshal(cache["launch"], &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(historical, &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("historical launch evidence changed: got %s, want %s", cache["launch"], historical)
	}
	if got := HydrateRuntimeROProbe(w, rt, binary).ROProbe; got != RuntimeROFailed {
		t.Fatalf("cached RO observation = %q, want failed", got)
	}
}
