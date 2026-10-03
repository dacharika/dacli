package nativegovernance

import (
	"encoding/json"
	"testing"
	"time"
)

func dispatchProbeFixture(t *testing.T) []byte {
	t.Helper()
	var p map[string]any
	if e := json.Unmarshal([]byte(`{"schema_version":1,"probe":"dacli-codex-native-dispatch/v1","observed_at":"2000-01-01T00:00:00Z","host_version":"codex-cli 0.159.2","binary_sha256":"50ac633af64851511f9bbc71032cdae7f1ba20b3234c189687d61ba846c354c5","client_protocol_sha256":"f21e6307c8c2c7f2b5a32d434cdca238e3ec19b71a1f40b60f8c305289860436","declared_method_count":170,"app_server_dispatch":{"agent/spawn":{"declared_in_protocol":false,"unknown_method_refusal_observed":true,"status":"unsupported"},"collaboration/spawn_agent":{"declared_in_protocol":false,"unknown_method_refusal_observed":true,"status":"unsupported"}},"thread_config_schema":{"ThreadStartParams":{"config_field_declared":true,"schema_sha256":"dc8bebec4181a5502fc141b0122cc48e066b3b5c77d9fd2a4de875fcd6d66a68"},"ThreadForkParams":{"config_field_declared":true,"schema_sha256":"ea9dd8fb39979f32293a31f98e599509430b7cc80840b2a0afc90a2b953e942e"}},"native_collaboration":{"status":"requires-native-host-attestation"},"private_child_profile_delivery":{"status":"unverified"},"stable_dispatch_key_lookup":{"status":"unverified"},"cancellation":{"method_declared":true,"recognized_method_refusal":true,"running_turn_cancellation":"unverified"},"scope":{"isolated_codex_home":true,"thread_created":false,"model_turn_requested":false,"worker_credentials_read":false,"native_child_dispatched":false},"app_server_exit_code":0}`), &p); e != nil {
		t.Fatal(e)
	}
	p["observed_at"] = time.Now().UTC().Format(time.RFC3339Nano)
	data, e := json.Marshal(p)
	if e != nil {
		t.Fatal(e)
	}
	return data
}
