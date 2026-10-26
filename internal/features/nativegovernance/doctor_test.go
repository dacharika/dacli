package nativegovernance

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/application"
	"github.com/mlnomadpy/dacli/internal/capabilities"
	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/guidance"
)

func TestNativeDoctorReportsCompatibleOlderAuthorityReadOnly(t *testing.T) {
	f := setupVerification(t, "", false)
	manifest := filepath.Join(t.TempDir(), "plugin.json")
	verificationJSON(t, manifest, map[string]any{"$schema": "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json", "name": "dacli-governance", "version": "0.5.0", "description": "Govern native agent swarms.", "author": map[string]string{"name": "Fixture"}, "repository": "https://example.invalid/dacli", "license": "BSD-3-Clause", "keywords": []string{"governance"}, "extensions": map[string]any{"com.openai": map[string]any{"interface": map[string]any{"displayName": "dacli Governance"}}}})
	artifact := filepath.Join(t.TempDir(), "plugin.zip")
	writeDoctorPackage(t, manifest, artifact)
	capability := filepath.Join(t.TempDir(), "capability.json")
	verificationJSON(t, capability, application.ManualSwarmCapability{SchemaVersion: 2, WorkspaceID: f.w.ID, ProjectID: "p", Harness: "codex", Source: "owner-manual-native-handoff", ObservedAt: time.Now().Add(-2 * time.Hour), ExpiresAt: time.Now().Add(-time.Hour), Codex: &application.CodexDispatchCapability{SchemaVersion: 1, NativeHostAttestation: "owner-observed-native-collaboration", ChildProfileChannel: "owner-manual-separate-process"}})
	paths := []string{f.worker.Authority, f.connection, f.worker.CredentialFile, capability, manifest, artifact}
	before := make(map[string][]byte, len(paths))
	var err error
	for _, selected := range paths {
		before[selected], err = os.ReadFile(selected)
		if err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	ctx := &clikit.Ctx{Stdout: &out, Stderr: &out, JSON: true}
	if err := nativeDoctor(ctx, []string{"--plugin-manifest", manifest, "--package-artifact", artifact, "--connection", f.connection, "--capability-file", capability, "--project", "p"}); err != nil {
		t.Fatal(err)
	}
	var got nativeDoctorResult
	raw, err := json.Marshal(ctx.Result)
	if err != nil || json.Unmarshal(raw, &got) != nil {
		t.Fatalf("invalid doctor result: %v", err)
	}
	if got.Schema != "native-doctor/v1" || got.Authority.Status != "compatible" || got.Authority.ObservedSchema == nil || *got.Authority.ObservedSchema != 6 || got.Authority.AuthorizedSchema == nil || *got.Authority.AuthorizedSchema != 6 {
		t.Fatalf("older supported authority not reported as compatible: %+v", got.Authority)
	}
	if got.Observations.AuthenticatedStartup != "authenticated" || got.Observations.ToolDiscovery != "unknown" || got.Observations.HostExecution != "unknown" || got.Observations.CapabilityEvidence != "stale" || got.Package.Version != "0.5.0" || got.Runtime.Version == got.Package.Version || got.Runtime.Version == "" || got.Package.ManifestDigest == "" || got.Package.ArtifactDigest == "" {
		t.Fatalf("observations were conflated: %+v", got.Observations)
	}
	for _, selected := range paths {
		after, readErr := os.ReadFile(selected)
		if readErr != nil || !bytes.Equal(before[selected], after) {
			t.Fatalf("doctor changed selected file bytes: read error=%v", readErr)
		}
	}
	if _, err := os.Stat(f.worker.Authority + ".lock"); !os.IsNotExist(err) {
		t.Fatalf("doctor left authority lock behind: %v", err)
	}
	for _, secret := range []string{f.workerToken, f.owner, f.connection, f.config, f.worker.CredentialFile, f.worker.Authority} {
		if secret != "" && strings.Contains(string(raw), secret) {
			t.Fatalf("doctor output disclosed credential or path: %q", secret)
		}
	}
}

func TestNativeDoctorKeepsUnsupportedAuthoritySchemaUnknown(t *testing.T) {
	f := setupVerification(t, "", false)
	manifest := filepath.Join(t.TempDir(), "plugin.json")
	verificationJSON(t, manifest, map[string]string{"name": "fixture", "version": "0.4.0"})
	artifact := filepath.Join(t.TempDir(), "plugin.zip")
	writeDoctorPackage(t, manifest, artifact)
	var state map[string]any
	if err := privateJSON(f.worker.Authority, &state); err != nil {
		t.Fatal(err)
	}
	state["Version"] = float64(maximumAuthoritySchema + 1)
	verificationJSON(t, f.worker.Authority, state)
	var out bytes.Buffer
	ctx := &clikit.Ctx{Stdout: &out, Stderr: &out, JSON: true}
	if err := nativeDoctor(ctx, []string{"--plugin-manifest", manifest, "--package-artifact", artifact, "--connection", f.connection}); err != nil {
		t.Fatal(err)
	}
	var got nativeDoctorResult
	raw, _ := json.Marshal(ctx.Result)
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Authority.Status != "unknown" || got.Authority.ObservedSchema != nil || got.Authority.AuthorizedSchema != nil || got.Observations.AuthenticatedStartup != "unavailable" {
		t.Fatalf("unauthenticated unsupported schema was classified: %+v", got.Authority)
	}
}

func TestNativeDoctorDistinguishesMissingRevokedAndExpiredEnrollment(t *testing.T) {
	for _, status := range []string{"missing_enrollment", "revoked_actor", "expired_enrollment"} {
		t.Run(status, func(t *testing.T) {
			f := setupVerification(t, "", false)
			manifest := filepath.Join(t.TempDir(), "plugin.json")
			verificationJSON(t, manifest, map[string]string{"name": "fixture", "version": "0.4.0"})
			artifact := filepath.Join(t.TempDir(), "plugin.zip")
			writeDoctorPackage(t, manifest, artifact)
			switch status {
			case "missing_enrollment":
				var connection map[string]any
				if err := privateJSON(f.connection, &connection); err != nil {
					t.Fatal(err)
				}
				connection["session_id"] = "unissued-session"
				verificationJSON(t, f.connection, connection)
			case "revoked_actor":
				if err := f.authority.Revoke(f.owner, f.worker.SessionID); err != nil {
					t.Fatal(err)
				}
			case "expired_enrollment":
				var state map[string]any
				if err := privateJSON(f.worker.Authority, &state); err != nil {
					t.Fatal(err)
				}
				sessions := state["Sessions"].(map[string]any)
				record := sessions[f.worker.SessionID].(map[string]any)
				record["ExpiresAt"] = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
				verificationJSON(t, f.worker.Authority, state)
			}
			var out bytes.Buffer
			ctx := &clikit.Ctx{Stdout: &out, Stderr: &out, JSON: true}
			if err := nativeDoctor(ctx, []string{"--plugin-manifest", manifest, "--package-artifact", artifact, "--connection", f.connection}); err != nil {
				t.Fatal(err)
			}
			var got nativeDoctorResult
			raw, _ := json.Marshal(ctx.Result)
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			if got.Authority.Status != status || got.Authority.AuthorizedSchema != nil || got.Authority.NextAction == "" || strings.Contains(got.Authority.NextAction, f.connection) || strings.Contains(got.Authority.NextAction, f.workerToken) {
				t.Fatalf("status = %q, want %q", got.Authority.Status, status)
			}
		})
	}
}

func writeDoctorPackage(t *testing.T, manifest, destination string) {
	t.Helper()
	content, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var selected pluginManifestInput
	if !decodePluginManifest(content, &selected) {
		t.Fatal("invalid package fixture manifest")
	}
	file, err := os.Create(destination)
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(file)
	if _, err := archive.Create("assets/"); err != nil {
		t.Fatal(err)
	}
	entry, err := archive.Create(selected.ManifestPath)
	if err == nil {
		_, err = entry.Write(content)
	}
	if closeErr := archive.Close(); err == nil {
		err = closeErr
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
}

func TestNativeDoctorManifestMismatchAndHostDifferences(t *testing.T) {
	var packageInput pluginManifestInput
	if !decodePluginManifest([]byte(`{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"dacli-governance","version":"0.5.0","description":"Fixture","author":{"name":"Fixture"},"repository":"https://example.invalid/dacli","license":"BSD-3-Clause","keywords":["governance"],"extensions":{"com.openai":{"interface":{"displayName":"dacli Governance"}}}}`), &packageInput) || packageInput.Name != "dacli-governance" || packageInput.Version != "0.5.0" {
		t.Fatalf("selected package metadata not reported independently: %+v", packageInput)
	}
	codex := filepath.Join(t.TempDir(), "codex.json")
	verificationJSON(t, codex, application.ManualSwarmCapability{SchemaVersion: 2, WorkspaceID: "workspace", ProjectID: "project", Harness: "codex", Source: "owner-manual-native-handoff", ObservedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute), Codex: &application.CodexDispatchCapability{SchemaVersion: 1, NativeHostAttestation: "owner-observed-native-collaboration", ChildProfileChannel: "owner-manual-separate-process"}})
	if got, _ := inspectCapability(codex, time.Now(), "workspace", "project"); got != "fresh_owner_attestation" {
		t.Fatalf("Codex evidence state = %q", got)
	}
	other := filepath.Join(t.TempDir(), "other.json")
	verificationJSON(t, other, application.ManualSwarmCapability{SchemaVersion: 2, WorkspaceID: "workspace", ProjectID: "project", Harness: "antigravity", Source: "owner-manual-native-handoff", ObservedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute)})
	if got, _ := inspectCapability(other, time.Now(), "workspace", "project"); got != "host_unverified" {
		t.Fatalf("other host evidence state = %q", got)
	}
	unsupported := filepath.Join(t.TempDir(), "unsupported.json")
	verificationJSON(t, unsupported, application.ManualSwarmCapability{SchemaVersion: 3, WorkspaceID: "workspace", ProjectID: "project", Harness: "codex", Source: "owner-manual-native-handoff", ObservedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute)})
	if got, _ := inspectCapability(unsupported, time.Now(), "workspace", "project"); got != "unsupported_or_invalid" {
		t.Fatalf("unsupported evidence schema state = %q", got)
	}
}

func TestManifestOnlyDiagnosisRemainsAvailable(t *testing.T) {
	manifest := repositoryDoctorManifest(t, "plugin.json")
	var out bytes.Buffer
	ctx := &clikit.Ctx{Stdout: &out, Stderr: &out, JSON: true}
	if err := nativeDoctor(ctx, []string{"--plugin-manifest", manifest}); err != nil {
		t.Fatalf("manifest-only diagnosis refused instead of unknown archive digest: exit=%d error=%v", clikit.ExitCode(err), err)
	}
	var got nativeDoctorResult
	raw, err := json.Marshal(ctx.Result)
	if err != nil || json.Unmarshal(raw, &got) != nil {
		t.Fatalf("invalid manifest-only result: %v", err)
	}
	if got.Package.ArtifactDigest != "unknown" || got.Package.ArtifactStatus != "not_selected" || got.Package.Name != "dacli-governance" || got.Runtime.Version == "" {
		t.Fatalf("manifest-only diagnostic lost available observations: package=%+v runtime=%+v", got.Package, got.Runtime)
	}
}

func TestActualCodexSourceManifestAccepted(t *testing.T) {
	for _, name := range []string{"plugin.json", filepath.Join(".codex-plugin", "plugin.json")} {
		t.Run(name, func(t *testing.T) {
			manifest := repositoryDoctorManifest(t, name)
			content, err := os.ReadFile(manifest)
			if err != nil {
				t.Fatal(err)
			}
			artifact := filepath.Join(t.TempDir(), "plugin.zip")
			writeDoctorArchive(t, artifact, "plugin.json", content)
			var out bytes.Buffer
			ctx := &clikit.Ctx{Stdout: &out, Stderr: &out, JSON: true}
			if err := nativeDoctor(ctx, []string{"--plugin-manifest", manifest, "--package-artifact", artifact}); err != nil {
				t.Fatalf("actual public Codex manifest dialect refused: exit=%d error=%v", clikit.ExitCode(err), err)
			}
		})
	}
}

func TestActualCurrentExportAccepted(t *testing.T) {
	manifest := repositoryDoctorManifest(t, "plugin.json")
	artifact := filepath.Join(t.TempDir(), "current-export.zip")
	writeDoctorPackageAt(t, manifest, artifact, "dacli-governance/")
	var out bytes.Buffer
	ctx := &clikit.Ctx{Stdout: &out, Stderr: &out, JSON: true}
	if err := nativeDoctor(ctx, []string{"--plugin-manifest", manifest, "--package-artifact", artifact}); err != nil {
		t.Fatalf("current source package.py export refused: exit=%d error=%v", clikit.ExitCode(err), err)
	}
}

func TestAlteredSameVersionManifestDoesNotMatch(t *testing.T) {
	manifest := repositoryDoctorManifest(t, "plugin.json")
	altered := []byte(`{"name":"dacli-governance","version":"0.5.0","description":"unrelated configuration"}`)
	artifact := filepath.Join(t.TempDir(), "altered.zip")
	writeDoctorArchive(t, artifact, "plugin.json", altered)
	var out bytes.Buffer
	ctx := &clikit.Ctx{Stdout: &out, Stderr: &out, JSON: true}
	err := nativeDoctor(ctx, []string{"--plugin-manifest", manifest, "--package-artifact", artifact})
	if err == nil {
		t.Fatal("archive with different manifest bytes but same name/version passed matching check")
	}
}

func repositoryDoctorManifest(t *testing.T, name string) string {
	t.Helper()
	manifest, err := filepath.Abs(filepath.Join("..", "..", "..", "plugins", "codex", name))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.ReadFile(manifest); err != nil {
		t.Fatal(err)
	}
	return manifest
}

func writeDoctorPackageAt(t *testing.T, manifest, destination, prefix string) {
	t.Helper()
	content, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var selected pluginManifestInput
	if !decodePluginManifest(content, &selected) {
		t.Fatal("invalid package fixture manifest")
	}
	writeDoctorArchive(t, destination, prefix+selected.ManifestPath, content)
}

func writeDoctorArchive(t *testing.T, destination, manifestPath string, manifest []byte) {
	t.Helper()
	file, err := os.Create(destination)
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(file)
	if manifestPath != "plugin.json" && !strings.Contains(manifestPath, "/") {
		manifestPath = "dacli-governance/" + manifestPath
	}
	if strings.HasPrefix(manifestPath, "dacli-governance/") {
		if _, err := archive.Create("dacli-governance/assets/"); err != nil {
			t.Fatal(err)
		}
	}
	entry, err := archive.Create(manifestPath)
	if err == nil {
		_, err = entry.Write(manifest)
	}
	if closeErr := archive.Close(); err == nil {
		err = closeErr
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
}

func TestNativeDoctorCommandDeclaresReadOnlyJSON(t *testing.T) {
	for _, command := range Commands {
		if command.Path == "native doctor" {
			if command.Mutates || !command.JSON {
				t.Fatalf("native doctor capability declaration: mutates=%v json=%v", command.Mutates, command.JSON)
			}
			if !strings.Contains(command.Usage, "--runtime-capabilities") || !strings.Contains(command.Usage, "--skill-root") {
				t.Fatalf("native doctor usage omits selected runtime or skill-root diagnostics: %s", command.Usage)
			}
			return
		}
	}
	t.Fatal("native doctor command is not registered")
}

func TestNativeDoctorComparesSelectedRuntimeCapabilitiesAndKeepsFreshnessSeparate(t *testing.T) {
	root := canonicalNativeTestTemp(t, "dacli-doctor-legacy-fixture-")
	fixture := filepath.Join("..", "..", "guidance", "testdata", "known-legacy")
	if err := filepath.WalkDir(fixture, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(fixture, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(filepath.Join(root, rel), 0o755)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err = os.MkdirAll(filepath.Dir(filepath.Join(root, rel)), 0o755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(root, rel), b, 0o644)
	}); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(t.TempDir(), "runtime-capabilities.json")
	manifest := capabilities.Manifest{SchemaVersion: capabilities.ManifestSchema, Binary: capabilities.Binary{Name: "dacli", Version: "fixture", Path: "/private/fixture/dacli", GOOS: "darwin", GOARCH: "arm64"}, Commands: []capabilities.Command{{ID: "cli.command.capabilities", Path: "capabilities", SchemaVersion: 1}}}
	verificationJSON(t, manifestPath, manifest)
	got := inspectInstalledGuidance(root, manifestPath)
	selected := false
	for _, copy := range got.Copies {
		if copy.Root == root {
			selected = true
			if copy.Status != guidance.StatusLegacy {
				t.Fatalf("legacy copy classification=%s", copy.Status)
			}
		}
	}
	if !selected {
		t.Fatal("explicitly selected skill root was not diagnosed")
	}
	if got.FreshProcessState != "unknown" || !strings.Contains(got.CachedConnectionState, "unknown") {
		t.Fatalf("file inspection claimed process freshness: %+v", got)
	}
	if got.CapabilityStatus != "declared_manifest_incompatible_unverified" || !strings.Contains(got.CapabilitySummary, "missing") {
		t.Fatalf("required legacy commands were not compared with selected runtime manifest: %+v", got)
	}
}

func TestNativeDoctorRejectsMalformedRequirementsAndCapabilityManifests(t *testing.T) {
	if validSelectedRequirements(capabilities.Requirements{SchemaVersion: capabilities.RequirementsSchema}) {
		t.Fatal("empty requirements were treated as a selected contract")
	}
	if validSelectedRequirements(capabilities.Requirements{SchemaVersion: capabilities.RequirementsSchema, Required: []capabilities.Requirement{{ID: "cli.command.status"}, {ID: "cli.command.status"}}}) {
		t.Fatal("duplicate requirement identities were accepted")
	}
	if validSelectedCapabilityManifest(capabilities.Manifest{SchemaVersion: capabilities.ManifestSchema, Binary: capabilities.Binary{Name: "dacli", Version: "fixture", Path: "relative", GOOS: "darwin", GOARCH: "arm64"}, Commands: []capabilities.Command{{ID: "cli.command.status", Path: "status", SchemaVersion: 1}}}) {
		t.Fatal("relative runtime identity was accepted")
	}
	if validSelectedCapabilityManifest(capabilities.Manifest{SchemaVersion: capabilities.ManifestSchema, Binary: capabilities.Binary{Name: "dacli", Version: "fixture", Path: "/private/fixture/dacli", GOOS: "darwin", GOARCH: "arm64"}}) {
		t.Fatal("empty runtime command catalog was accepted")
	}
}

func TestNativeDoctorCommandReportsSelectedSkillAndRuntimeManifest(t *testing.T) {
	root := canonicalNativeTestTemp(t, "dacli-doctor-cli-")
	var err error
	fixture := filepath.Join("..", "..", "guidance", "testdata", "known-legacy")
	if err := filepath.WalkDir(fixture, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(fixture, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(filepath.Join(root, rel), 0o755)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err = os.MkdirAll(filepath.Dir(filepath.Join(root, rel)), 0o755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(root, rel), b, 0o644)
	}); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(t.TempDir(), "runtime-capabilities.json")
	manifest := capabilities.Manifest{SchemaVersion: capabilities.ManifestSchema, Binary: capabilities.Binary{Name: "dacli", Version: "fixture", Path: "/private/fixture/dacli", GOOS: "darwin", GOARCH: "arm64"}, Commands: []capabilities.Command{{ID: "cli.command.capabilities", Path: "capabilities", SchemaVersion: 1}}}
	verificationJSON(t, manifestPath, manifest)
	pluginPath := filepath.Join(t.TempDir(), "plugin.json")
	verificationJSON(t, pluginPath, map[string]string{"name": "dacli-governance", "version": "0.5.0"})
	var out bytes.Buffer
	ctx := &clikit.Ctx{Stdout: &out, Stderr: &out, JSON: true}
	if err := nativeDoctor(ctx, []string{"--plugin-manifest", pluginPath, "--skill-root", root, "--runtime-capabilities", manifestPath}); err != nil {
		t.Fatal(err)
	}
	var got nativeDoctorResult
	raw, err := json.Marshal(ctx.Result)
	if err != nil || json.Unmarshal(raw, &got) != nil {
		t.Fatalf("doctor result: %v", err)
	}
	selected := false
	for _, copy := range got.Guidance.Copies {
		if copy.Root == root {
			selected = true
			if copy.Status != guidance.StatusLegacy {
				t.Fatalf("copy status=%s", copy.Status)
			}
		}
	}
	if !selected || got.Guidance.SelectedRuntimeIdentity != "unknown" || got.Guidance.SelectedRuntimeDigest != "unknown" || got.Guidance.CapabilityManifestDigest == "unknown" || got.Guidance.RuntimeBindingStatus != "unverified_manifest_only" || got.Guidance.FreshProcessState != "unknown" || !strings.Contains(got.Guidance.CachedConnectionState, "unknown") || got.Guidance.CapabilityStatus != "declared_manifest_incompatible_unverified" {
		t.Fatalf("doctor conflated selected files, runtime, capabilities or process freshness: %+v", got.Guidance)
	}
}

func TestNativeDoctorLabelsUnboundCapabilityManifestUnverified(t *testing.T) {
	t.Setenv("HOME", canonicalNativeTestTemp(t, "dacli-doctor-home-"))
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller path unavailable")
	}
	repo := filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", ".."))
	root := filepath.Join(repo, "internal", "guidance", "assets", "current")
	requirementsRaw, err := os.ReadFile(filepath.Join(repo, "skills", "dacli", "capabilities.json"))
	if err != nil {
		t.Fatal(err)
	}
	var requirements capabilities.Requirements
	if err := json.Unmarshal(requirementsRaw, &requirements); err != nil {
		t.Fatal(err)
	}
	manifest := capabilities.Manifest{SchemaVersion: capabilities.ManifestSchema, Binary: capabilities.Binary{Name: "dacli", Version: "caller-asserted", Path: filepath.Join(t.TempDir(), "nonexistent-runtime"), GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}}
	commands := map[string]int{}
	for _, requirement := range requirements.Required {
		if strings.Contains(requirement.ID, ".flag.") {
			continue
		}
		path := strings.ReplaceAll(strings.TrimPrefix(requirement.ID, "cli.command."), ".", " ")
		commands[requirement.ID] = len(manifest.Commands)
		manifest.Commands = append(manifest.Commands, capabilities.Command{ID: requirement.ID, Path: path, SchemaVersion: 1})
	}
	for _, requirement := range requirements.Required {
		parts := strings.SplitN(requirement.ID, ".flag.", 2)
		if len(parts) != 2 {
			continue
		}
		index, ok := commands[parts[0]]
		if !ok {
			t.Fatalf("required flag has no required command: %s", requirement.ID)
		}
		manifest.Commands[index].Flags = append(manifest.Commands[index].Flags, capabilities.Flag{ID: requirement.ID, Name: "--" + parts[1], SchemaVersion: 1})
	}
	manifestPath := filepath.Join(t.TempDir(), "runtime-capabilities.json")
	verificationJSON(t, manifestPath, manifest)
	got := inspectInstalledGuidance(root, manifestPath)
	if got.CapabilityStatus != "declared_manifest_compatible_unverified" || got.RuntimeBindingStatus != "unverified_manifest_only" || got.SelectedRuntimeIdentity != "unknown" || got.SelectedRuntimeDigest != "unknown" || !strings.HasPrefix(got.CapabilityManifestDigest, "sha256:") || !strings.Contains(got.CapabilitySummary, "runtime binding remains unverified") {
		t.Fatalf("caller-supplied manifest claimed a selected runtime: %+v", got)
	}
}
