package application

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNativeDispatchVersionedCapabilityReceipt(t *testing.T) {
	for _, mode := range []string{"valid", "declared-unexecuted", "empty-host-version", "whitespace-host-version", "malformed-host-version", "valid-prerelease-version", "missing-metadata", "malformed-binary-hash", "missing-scope-boolean", "probe-withdrawn", "probe-changed", "unattended-channel", "stale-probe", "unsupported-schema1"} {
		t.Run(mode, func(t *testing.T) {
			var probe map[string]any
			if e := json.Unmarshal([]byte(`{"schema_version":1,"probe":"dacli-codex-native-dispatch/v1","observed_at":"2000-01-01T00:00:00Z","host_version":"codex-cli 0.159.2","binary_sha256":"50ac633af64851511f9bbc71032cdae7f1ba20b3234c189687d61ba846c354c5","client_protocol_sha256":"f21e6307c8c2c7f2b5a32d434cdca238e3ec19b71a1f40b60f8c305289860436","declared_method_count":170,"app_server_dispatch":{"agent/spawn":{"declared_in_protocol":false,"unknown_method_refusal_observed":true,"status":"unsupported"},"collaboration/spawn_agent":{"declared_in_protocol":false,"unknown_method_refusal_observed":true,"status":"unsupported"}},"thread_config_schema":{"ThreadStartParams":{"config_field_declared":true,"schema_sha256":"dc8bebec4181a5502fc141b0122cc48e066b3b5c77d9fd2a4de875fcd6d66a68"},"ThreadForkParams":{"config_field_declared":true,"schema_sha256":"ea9dd8fb39979f32293a31f98e599509430b7cc80840b2a0afc90a2b953e942e"}},"native_collaboration":{"status":"requires-native-host-attestation"},"private_child_profile_delivery":{"status":"unverified"},"stable_dispatch_key_lookup":{"status":"unverified"},"cancellation":{"method_declared":true,"recognized_method_refusal":true,"running_turn_cancellation":"unverified"},"scope":{"isolated_codex_home":true,"thread_created":false,"model_turn_requested":false,"worker_credentials_read":false,"native_child_dispatched":false},"app_server_exit_code":0}`), &probe); e != nil {
				t.Fatal(e)
			}
			probe["observed_at"] = time.Now().UTC().Format(time.RFC3339Nano)
			switch mode {
			case "declared-unexecuted":
				probe["app_server_dispatch"].(map[string]any)["agent/spawn"] = map[string]any{"declared_in_protocol": true, "unknown_method_refusal_observed": false, "status": "unverified"}
			case "empty-host-version":
				probe["host_version"] = "codex-cli "
			case "whitespace-host-version":
				probe["host_version"] = "codex-cli    "
			case "malformed-host-version":
				probe["host_version"] = "codex-cli not-a-version"
			case "valid-prerelease-version":
				probe["host_version"] = "codex-cli 0.159.2-alpha.1+build.42"
			case "missing-metadata":
				delete(probe, "client_protocol_sha256")
			case "malformed-binary-hash":
				probe["binary_sha256"] = "invalid"
			case "missing-scope-boolean":
				delete(probe["scope"].(map[string]any), "native_child_dispatched")
			case "stale-probe":
				probe["observed_at"] = time.Now().Add(-25 * time.Hour).UTC().Format(time.RFC3339Nano)
			}
			dir := t.TempDir()
			probePath := filepath.Join(dir, "probe.json")
			data, e := json.Marshal(probe)
			if e != nil {
				t.Fatal(e)
			}
			if e = os.WriteFile(probePath, data, 0600); e != nil {
				t.Fatal(e)
			}
			sum := sha256.Sum256(data)
			profile := ManualSwarmCapability{SchemaVersion: 2, WorkspaceID: "workspace", ProjectID: "project", Harness: "codex", Source: "owner-manual-native-handoff", ObservedAt: time.Now(), ExpiresAt: time.Now().Add(30 * time.Minute), Codex: &CodexDispatchCapability{SchemaVersion: 1, ProbeFile: probePath, ProbeSHA256: hex.EncodeToString(sum[:]), NativeHostAttestation: "owner-observed-native-collaboration", ChildProfileChannel: "owner-manual-separate-process"}}
			if mode == "unattended-channel" {
				profile.Codex.ChildProfileChannel = "automatic"
			}
			if mode == "unsupported-schema1" {
				profile.SchemaVersion = 1
				profile.Codex = nil
			}
			profilePath := filepath.Join(dir, "capability.json")
			data, e = json.Marshal(profile)
			if e != nil {
				t.Fatal(e)
			}
			if e = os.WriteFile(profilePath, data, 0600); e != nil {
				t.Fatal(e)
			}
			sum = sha256.Sum256(data)
			if mode == "probe-withdrawn" {
				if e = os.Remove(probePath); e != nil {
					t.Fatal(e)
				}
			}
			if mode == "probe-changed" {
				if e = os.WriteFile(probePath, append(data, ' '), 0600); e != nil {
					t.Fatal(e)
				}
			}
			err := (manualSwarmCapabilityFile{path: profilePath}).AdmitDispatchCapability("workspace", "project", "codex", "sha256:"+hex.EncodeToString(sum[:]))
			valid := mode == "valid" || mode == "declared-unexecuted" || mode == "valid-prerelease-version"
			if valid != (err == nil) {
				t.Fatalf("capability %s: %v", mode, err)
			}
		})
	}
}
