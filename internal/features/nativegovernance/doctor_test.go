package nativegovernance

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/application"
	"github.com/mlnomadpy/dacli/internal/clikit"
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
			return
		}
	}
	t.Fatal("native doctor command is not registered")
}
