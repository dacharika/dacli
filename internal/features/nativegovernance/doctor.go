// Native doctor reports only observations it can make from explicitly selected
// bounded inputs. It never repairs authority or treats host attestations as
// proof of host execution (issue #1090).
package nativegovernance

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/mlnomadpy/dacli/internal/application"
	"github.com/mlnomadpy/dacli/internal/buildinfo"
	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/governance/nativeconfig"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/mcp/native"
)

const (
	doctorManifestLimit    = 64 << 10
	doctorArtifactLimit    = 64 << 20
	doctorCapabilityLimit  = 16 << 10
	minimumAuthoritySchema = sessions.MinNativeCompatibleSchema
	maximumAuthoritySchema = sessions.MaxNativeCompatibleSchema
)

type pluginManifestInput struct {
	Name                  string
	Version               string
	Dialect               string
	ManifestPath          string
	AlternateManifestPath string
}

type manifestAuthor struct {
	Name string `json:"name"`
}

type manifestInterface struct {
	DisplayName      string   `json:"displayName"`
	ShortDescription string   `json:"shortDescription"`
	LongDescription  string   `json:"longDescription"`
	DeveloperName    string   `json:"developerName"`
	Category         string   `json:"category"`
	Capabilities     []string `json:"capabilities"`
	DefaultPrompt    string   `json:"defaultPrompt"`
}

type portableManifest struct {
	Schema      string         `json:"$schema"`
	Name        string         `json:"name"`
	Version     string         `json:"version"`
	Description string         `json:"description"`
	Author      manifestAuthor `json:"author"`
	Repository  string         `json:"repository"`
	License     string         `json:"license"`
	Keywords    []string       `json:"keywords"`
	Extensions  struct {
		OpenAI struct {
			Interface manifestInterface `json:"interface"`
		} `json:"com.openai"`
	} `json:"extensions"`
}

type codexManifest struct {
	Name        string            `json:"name"`
	Version     string            `json:"version"`
	Description string            `json:"description"`
	Author      manifestAuthor    `json:"author"`
	Repository  string            `json:"repository"`
	License     string            `json:"license"`
	Skills      string            `json:"skills"`
	MCPServers  string            `json:"mcpServers"`
	Interface   manifestInterface `json:"interface"`
}

type nativeDoctorResult struct {
	Schema  string `json:"schema"`
	Version int    `json:"version"`
	Runtime struct {
		Version                 string `json:"version"`
		Revision                string `json:"revision"`
		Dirty                   *bool  `json:"dirty"`
		Go                      string `json:"go_version"`
		Availability            string `json:"availability"`
		HostRuntimeAvailability string `json:"host_runtime_availability"`
		SelectedArtifact        string `json:"selected_runtime_artifact"`
		SelectedArtifactAction  string `json:"selected_runtime_artifact_next_action"`
	} `json:"runtime"`
	Package struct {
		Name               string `json:"name"`
		Version            string `json:"version"`
		ManifestDigest     string `json:"manifest_digest"`
		ArtifactDigest     string `json:"artifact_digest"`
		ArtifactStatus     string `json:"artifact_status"`
		ArtifactNextAction string `json:"artifact_next_action"`
	} `json:"package"`
	Authority struct {
		SupportedSchema struct {
			Minimum int `json:"minimum"`
			Maximum int `json:"maximum"`
		} `json:"supported_schema"`
		ObservedSchema   *int   `json:"observed_schema"`
		AuthorizedSchema *int   `json:"authorized_schema"`
		Status           string `json:"status"`
		NextAction       string `json:"next_action"`
	} `json:"authority"`
	Observations struct {
		MetadataInspection      string `json:"metadata_inspection"`
		AuthenticatedStartup    string `json:"authenticated_startup"`
		ToolDiscovery           string `json:"tool_discovery"`
		HostExecution           string `json:"host_execution"`
		HostSupport             string `json:"host_support"`
		ToolDiscoveryNextAction string `json:"tool_discovery_next_action"`
		HostExecutionNextAction string `json:"host_execution_next_action"`
		CapabilityEvidence      string `json:"capability_evidence"`
		CapabilityNextAction    string `json:"capability_next_action"`
	} `json:"observations"`
}

func nativeDoctor(ctx *clikit.Ctx, args []string) error {
	f, err := flags(args, "plugin-manifest", "package-artifact", "runtime-artifact", "connection", "capability-file", "project")
	if err != nil {
		return err
	}
	if f.Get("plugin-manifest") == "" {
		return clikit.Usagef("--plugin-manifest is required")
	}
	if f.Get("capability-file") != "" && (f.Get("connection") == "" || f.Get("project") == "") {
		return clikit.Usagef("--capability-file requires --connection and --project for binding")
	}
	if f.Get("capability-file") == "" && f.Get("project") != "" {
		return clikit.Usagef("--project requires --capability-file")
	}
	result := nativeDoctorResult{Schema: "native-doctor/v1", Version: 1}
	identity := buildinfo.Identity()
	result.Runtime.Version, result.Runtime.Revision, result.Runtime.Dirty, result.Runtime.Go = identity.Version, identity.Revision, identity.Dirty, runtime.Version()
	result.Runtime.Availability = "running"
	result.Runtime.HostRuntimeAvailability = "unknown"
	result.Runtime.SelectedArtifact = "not_selected"
	result.Runtime.SelectedArtifactAction = "Select a specific host runtime executable with --runtime-artifact to inspect its bounded executable metadata."
	if f.Get("runtime-artifact") != "" {
		if nativeconfig.InspectExecutableMetadata(f.Get("runtime-artifact"), doctorArtifactLimit) != nil {
			result.Runtime.SelectedArtifact = "unavailable"
			result.Runtime.SelectedArtifactAction = "Ask the host owner to provide the selected runtime executable as a readable regular file within the inspection limit."
		} else {
			result.Runtime.SelectedArtifact = "metadata_available"
			result.Runtime.SelectedArtifactAction = "The selected executable was not run; confirm runtime compatibility and host execution with separate owner-authorized probes."
		}
	}
	result.Authority.SupportedSchema.Minimum, result.Authority.SupportedSchema.Maximum = minimumAuthoritySchema, maximumAuthoritySchema
	result.Observations.MetadataInspection = "complete"
	result.Observations.AuthenticatedStartup = "not_attempted"
	result.Observations.ToolDiscovery = "unknown"
	result.Observations.HostExecution = "unknown"
	result.Observations.HostSupport = "unverified"
	result.Observations.ToolDiscoveryNextAction = "Run the documented host-specific tool discovery probe and retain its versioned observation."
	result.Observations.HostExecutionNextAction = "Run a separately authorized host execution probe and review its versioned receipt; metadata and discovery alone do not prove execution."
	result.Observations.CapabilityEvidence = "not_selected"
	result.Observations.CapabilityNextAction = "Select an owner-observed capability receipt with --capability-file to inspect its age and host label."

	manifest, err := nativeconfig.ReadMetadata(f.Get("plugin-manifest"), doctorManifestLimit)
	var packageInput pluginManifestInput
	if err != nil || !decodePluginManifest(manifest, &packageInput) {
		return clikit.Refusedf("plugin manifest is unavailable or invalid; select a bounded plugin manifest")
	}
	digest := sha256.Sum256(manifest)
	result.Package.Name, result.Package.Version = packageInput.Name, packageInput.Version
	result.Package.ManifestDigest = "sha256:" + hex.EncodeToString(digest[:])
	result.Package.ArtifactDigest = "unknown"
	result.Package.ArtifactStatus = "not_selected"
	result.Package.ArtifactNextAction = "Select the corresponding package archive with --package-artifact to verify its embedded manifest bytes and compute its digest."
	if f.Get("package-artifact") != "" {
		artifact, readErr := nativeconfig.ReadMetadata(f.Get("package-artifact"), doctorArtifactLimit)
		if readErr != nil {
			return clikit.Refusedf("selected package artifact is unavailable or exceeds the inspection limit")
		}
		if !packageArtifactMatches(artifact, manifest, packageInput) {
			return clikit.Refusedf("selected package artifact is not a bounded package archive containing the exact selected manifest bytes")
		}
		artifactDigest := sha256.Sum256(artifact)
		result.Package.ArtifactDigest = "sha256:" + hex.EncodeToString(artifactDigest[:])
		result.Package.ArtifactStatus = "verified"
		result.Package.ArtifactNextAction = "The selected archive contains the exact selected manifest; runtime and host execution remain separate observations."
	}

	if f.Get("capability-file") != "" {
		workspaceID := ""
		if f.Get("connection") != "" {
			var connection native.Connection
			if b, e := native.ReadPrivate(f.Get("connection"), 16<<10); e == nil && native.Decode(b, &connection) == nil {
				workspaceID = connection.WorkspaceID
			}
		}
		result.Observations.CapabilityEvidence, result.Observations.CapabilityNextAction = inspectCapability(f.Get("capability-file"), time.Now().UTC(), workspaceID, f.Get("project"))
	}
	if f.Get("connection") != "" {
		inspectNativeConnection(f.Get("connection"), &result)
	} else {
		result.Authority.Status = "not_selected"
		result.Authority.NextAction = "Select the intended private connection with --connection to inspect enrollment and authenticated startup."
	}
	ctx.Result = result
	if ctx.JSON {
		return clikit.EmitJSON(ctx, result)
	}
	fmt.Fprintf(ctx.Stdout, "Native compatibility diagnosis: package %s (%s); authority %s; host discovery and execution %s.\n", result.Package.Name, result.Package.Version, result.Authority.Status, result.Observations.HostExecution)
	return nil
}

var pluginNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
var pluginVersionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.+-]{0,63}$`)

func decodePluginManifest(raw []byte, out *pluginManifestInput) bool {
	var root map[string]json.RawMessage
	if json.Unmarshal(raw, &root) != nil || len(root) == 0 {
		return false
	}
	portableOnly := map[string]bool{"$schema": true, "keywords": true, "extensions": true}
	codexOnly := map[string]bool{"skills": true, "mcpServers": true, "interface": true}
	knownCommon := map[string]bool{"name": true, "version": true, "description": true, "author": true, "repository": true, "license": true}
	dialect := "portable"
	for key := range root {
		if !knownCommon[key] && !portableOnly[key] && !codexOnly[key] {
			return false
		}
		if codexOnly[key] {
			if dialect == "portable" && hasAnyManifestKey(root, portableOnly) {
				return false
			}
			dialect = "codex"
		}
	}
	if hasAnyManifestKey(root, portableOnly) && hasAnyManifestKey(root, codexOnly) {
		return false
	}
	if dialect == "codex" {
		var value codexManifest
		if !decodeStrictManifest(raw, &value) {
			return false
		}
		out.Name, out.Version, out.Dialect, out.ManifestPath, out.AlternateManifestPath = value.Name, value.Version, "codex", ".codex-plugin/plugin.json", "plugin.json"
	} else {
		var value portableManifest
		if !decodeStrictManifest(raw, &value) {
			return false
		}
		out.Name, out.Version, out.Dialect, out.ManifestPath = value.Name, value.Version, "portable", "plugin.json"
	}
	return pluginNamePattern.MatchString(out.Name) && pluginVersionPattern.MatchString(out.Version)
}

func hasAnyManifestKey(root map[string]json.RawMessage, keys map[string]bool) bool {
	for key := range keys {
		if _, ok := root[key]; ok {
			return true
		}
	}
	return false
}

func decodeStrictManifest(raw []byte, out any) bool {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil {
		return false
	}
	var extra any
	return d.Decode(&extra) == io.EOF
}

func packageArtifactMatches(raw, selectedBytes []byte, selected pluginManifestInput) bool {
	archive, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil || len(archive.File) == 0 || len(archive.File) > 4096 {
		return false
	}
	seen := make(map[string]bool, len(archive.File))
	manifestCount := 0
	matchedManifest := false
	for _, file := range archive.File {
		name := file.Name
		isDirectory := strings.HasSuffix(name, "/")
		canonicalName := strings.TrimSuffix(name, "/")
		cleanName := path.Clean(canonicalName)
		if name == "" || path.IsAbs(name) || cleanName != canonicalName || cleanName == "." || cleanName == ".." || strings.HasPrefix(cleanName, "../") || seen[cleanName] {
			return false
		}
		seen[cleanName] = true
		if isDirectory {
			continue
		}
		if !selectedManifestArchivePath(name, selected) {
			continue
		}
		manifestCount++
		if manifestCount > 4 || !file.Mode().IsRegular() || file.UncompressedSize64 > doctorManifestLimit {
			return false
		}
		reader, err := file.Open()
		if err != nil {
			return false
		}
		content, readErr := io.ReadAll(io.LimitReader(reader, doctorManifestLimit+1))
		closeErr := reader.Close()
		if readErr != nil || closeErr != nil || len(content) > doctorManifestLimit {
			return false
		}
		var embedded pluginManifestInput
		if decodePluginManifest(content, &embedded) && embedded.Name == selected.Name && embedded.Version == selected.Version && embedded.Dialect == selected.Dialect && bytes.Equal(content, selectedBytes) {
			if matchedManifest {
				return false
			}
			matchedManifest = true
		}
	}
	return manifestCount > 0 && matchedManifest
}

func selectedManifestArchivePath(name string, selected pluginManifestInput) bool {
	for _, manifestPath := range []string{selected.ManifestPath, selected.AlternateManifestPath} {
		if manifestPath == "" {
			continue
		}
		if name == manifestPath || name == "dacli-governance/"+manifestPath {
			return true
		}
	}
	return false
}

func inspectCapability(path string, now time.Time, workspaceID, projectID string) (string, string) {
	raw, err := nativeconfig.ReadPrivate(path, doctorCapabilityLimit)
	if err != nil {
		return "unavailable", "Ask the capability receipt owner to provide the exact private receipt, then rerun native doctor."
	}
	var capability application.ManualSwarmCapability
	if nativeconfig.Decode(raw, &capability) != nil || capability.SchemaVersion < 1 || capability.SchemaVersion > 2 || capability.Source != "owner-manual-native-handoff" || capability.Harness == "" || capability.ExpiresAt.IsZero() || capability.ObservedAt.IsZero() {
		return "unsupported_or_invalid", "Have the host owner create a current capability receipt using a supported schema, then rerun native doctor."
	}
	if workspaceID == "" || capability.WorkspaceID != workspaceID || projectID == "" || capability.ProjectID != projectID {
		return "binding_unverified", "Select the matching connection and project for this receipt; host support remains unverified until the host probe is observed."
	}
	if !capability.ExpiresAt.After(now) || !capability.ExpiresAt.After(capability.ObservedAt) || capability.ExpiresAt.After(capability.ObservedAt.Add(time.Hour)) || now.Sub(capability.ObservedAt) > time.Hour || capability.ObservedAt.After(now.Add(30*time.Second)) {
		return "stale", "Ask the host owner to refresh the capability receipt for the selected workspace, project and host."
	}
	if capability.Harness != "codex" || capability.SchemaVersion == 2 && (capability.Codex == nil || capability.Codex.SchemaVersion != 1 || capability.Codex.NativeHostAttestation != "owner-observed-native-collaboration" || capability.Codex.ChildProfileChannel != "owner-manual-separate-process") {
		return "host_unverified", "Run the documented host-specific compatibility probe and review its versioned receipt."
	}
	return "fresh_owner_attestation", "Run the documented Codex host probe; a fresh owner attestation does not prove tool discovery or execution."
}

func inspectNativeConnection(path string, result *nativeDoctorResult) {
	connectionBytes, err := native.ReadPrivate(path, 16<<10)
	var connection native.Connection
	if err != nil || native.Decode(connectionBytes, &connection) != nil || connection.SchemaVersion != 1 || connection.WorkspaceID == "" || connection.InstallationID == "" || connection.SessionID == "" || !filepath.IsAbs(connection.Authority) || !filepath.IsAbs(connection.CredentialFile) || !filepath.IsAbs(connection.Workspace) {
		result.Authority.Status = "profile_schema_mismatch"
		result.Authority.NextAction = "Select a supported private connection profile and verify its workspace and installation binding with the authority owner."
		result.Observations.AuthenticatedStartup = "failed"
		return
	}
	authority, openErr := sessions.OpenCurrent(connection.Authority, sessions.Binding{Workspace: connection.WorkspaceID, Installation: connection.InstallationID})
	if openErr != nil {
		result.Authority.Status = "unknown"
		result.Authority.NextAction = "Ask the authority owner to verify native authority readability, schema and workspace/installation binding; this inspection could not authenticate those observations."
		result.Observations.AuthenticatedStartup = "unavailable"
		return
	}
	credential, credentialErr := native.ReadCredential(connection.CredentialFile)
	if credentialErr != nil {
		result.Authority.Status = "unknown"
		result.Authority.NextAction = "Ask the authority owner to verify the selected credential is readable and bound to an enrollment; this inspection could not classify enrollment state."
		result.Observations.AuthenticatedStartup = "unavailable"
		return
	}
	compatibility := authority.InspectNativeCompatibility(credential, connection.SessionID)
	switch compatibility.Status {
	case "unsupported_schema", "missing_enrollment", "expired_enrollment", "revoked_actor", "profile_schema_mismatch":
		result.Authority.Status = compatibility.Status
		result.Authority.NextAction = nextAuthorityAction(compatibility.Status)
		result.Observations.AuthenticatedStartup = "failed"
		return
	case "authorized":
		version := compatibility.AuthoritySchema
		result.Authority.ObservedSchema = &version
	default:
		result.Authority.Status = "unknown"
		result.Authority.NextAction = "Ask the authority owner to verify native authority schema, readability and binding; this inspection could not authenticate those observations."
		result.Observations.AuthenticatedStartup = "unavailable"
		return
	}
	server, openErr := native.Open(path)
	if openErr != nil {
		result.Authority.Status = "profile_schema_mismatch"
		result.Authority.NextAction = "Ask the authority owner to review connection, profile and authority compatibility; rerun native doctor after an owner-approved correction."
		result.Observations.AuthenticatedStartup = "failed"
		return
	}
	_ = server.Close()
	version := compatibility.AuthoritySchema
	result.Authority.AuthorizedSchema = &version
	result.Authority.Status = "compatible"
	result.Authority.NextAction = "No authority repair is indicated by this inspection. Confirm tool discovery and host execution separately with the documented host probe."
	result.Observations.AuthenticatedStartup = "authenticated"
}

func nextAuthorityAction(status string) string {
	switch status {
	case "missing_enrollment":
		return "Ask the authority owner to provision a new actor enrollment for the selected connection."
	case "expired_enrollment":
		return "Ask the authority owner to enroll a fresh actor; expired credentials cannot be renewed by inspection."
	case "revoked_actor":
		return "Ask the authority owner to review revocation and issue a separate enrollment if access is still intended."
	case "profile_schema_mismatch":
		return "Ask the authority owner to check the connection binding and documented profile schema before restarting."
	default:
		return "Ask the authority owner to inspect the native authority schema and use its documented migration path."
	}
}
