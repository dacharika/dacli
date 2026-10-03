package application

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/mlnomadpy/dacli/internal/governance/nativeconfig"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"regexp"
	"strings"
	"time"
)

// CodexDispatchCapability pins diagnostics and an explicit cooperative channel.
// Diagnostic schema declarations do not prove private child delivery support.
type CodexDispatchCapability struct {
	SchemaVersion         int    `json:"schema_version"`
	ProbeFile             string `json:"probe_file"`
	ProbeSHA256           string `json:"probe_sha256"`
	NativeHostAttestation string `json:"native_host_attestation"`
	ChildProfileChannel   string `json:"child_profile_channel"`
}
type codexProbe struct {
	SchemaVersion        int       `json:"schema_version"`
	Probe                string    `json:"probe"`
	ObservedAt           time.Time `json:"observed_at"`
	HostVersion          string    `json:"host_version"`
	BinarySHA256         string    `json:"binary_sha256"`
	ClientProtocolSHA256 string    `json:"client_protocol_sha256"`
	DeclaredMethodCount  int       `json:"declared_method_count"`
	AppServerDispatch    map[string]struct {
		Declared bool   `json:"declared_in_protocol"`
		Refused  bool   `json:"unknown_method_refusal_observed"`
		Status   string `json:"status"`
	} `json:"app_server_dispatch"`
	ThreadConfigSchema map[string]struct {
		Config bool   `json:"config_field_declared"`
		SHA256 string `json:"schema_sha256"`
	} `json:"thread_config_schema"`
	NativeCollaboration struct {
		Status string `json:"status"`
	} `json:"native_collaboration"`
	PrivateChildProfileDelivery struct {
		Status string `json:"status"`
	} `json:"private_child_profile_delivery"`
	StableDispatchKeyLookup struct {
		Status string `json:"status"`
	} `json:"stable_dispatch_key_lookup"`
	Cancellation struct {
		Declared   bool   `json:"method_declared"`
		Recognized bool   `json:"recognized_method_refusal"`
		Running    string `json:"running_turn_cancellation"`
	} `json:"cancellation"`
	Scope struct {
		Isolated    bool `json:"isolated_codex_home"`
		Thread      bool `json:"thread_created"`
		Turn        bool `json:"model_turn_requested"`
		Credentials bool `json:"worker_credentials_read"`
		Child       bool `json:"native_child_dispatched"`
	} `json:"scope"`
	ExitCode int `json:"app_server_exit_code"`
}

func (f manualSwarmCapabilityFile) AdmitDispatchCapability(w, p, h, d string) error {
	if f.AdmitSwarmCapability(w, p, h, d) != nil {
		return sessions.ErrDenied
	}
	data, e := nativeconfig.ReadPrivate(f.path, 16384)
	if e != nil {
		return sessions.ErrDenied
	}
	var c ManualSwarmCapability
	if nativeconfig.Decode(data, &c) != nil || c.SchemaVersion != 2 || c.Codex == nil || h != "codex" {
		return sessions.ErrDenied
	}
	v := c.Codex
	if v.SchemaVersion != 1 || v.NativeHostAttestation != "owner-observed-native-collaboration" || v.ChildProfileChannel != "owner-manual-separate-process" {
		return sessions.ErrDenied
	}
	probeData, e := nativeconfig.ReadPrivate(v.ProbeFile, 65536)
	if e != nil {
		return sessions.ErrDenied
	}
	digest := sha256.Sum256(probeData)
	var probe codexProbe
	if hex.EncodeToString(digest[:]) != v.ProbeSHA256 || nativeconfig.Decode(probeData, &probe) != nil || probe.SchemaVersion != 1 || probe.Probe != "dacli-codex-native-dispatch/v1" || !validCodexHostVersion(probe.HostVersion) || !probeReceiptShape(probeData) || !probeHash(probe.BinarySHA256) || !probeHash(probe.ClientProtocolSHA256) || probe.DeclaredMethodCount < 1 || probe.ExitCode != 0 || !probe.Scope.Isolated || probe.Scope.Thread || probe.Scope.Turn || probe.Scope.Credentials || probe.Scope.Child || probe.ObservedAt.After(time.Now().Add(30*time.Second)) || time.Since(probe.ObservedAt) > 24*time.Hour {
		return sessions.ErrDenied
	}
	for _, method := range []string{"agent/spawn", "collaboration/spawn_agent"} {
		m, ok := probe.AppServerDispatch[method]
		if !ok || !((m.Status == "unsupported" && !m.Declared && m.Refused) || (m.Status == "unverified" && m.Declared && !m.Refused)) {
			return sessions.ErrDenied
		}
	}
	if probe.NativeCollaboration.Status != "requires-native-host-attestation" || probe.PrivateChildProfileDelivery.Status != "unverified" || probe.StableDispatchKeyLookup.Status != "unverified" {
		return sessions.ErrDenied
	}
	for _, name := range []string{"ThreadStartParams", "ThreadForkParams"} {
		v, ok := probe.ThreadConfigSchema[name]
		if !ok || !probeHash(v.SHA256) {
			return sessions.ErrDenied
		}
	}
	if probe.Cancellation.Running != "unverified" || probe.Cancellation.Declared != probe.Cancellation.Recognized {
		return sessions.ErrDenied
	}
	return f.AdmitSwarmCapability(w, p, h, d)
}
func (p *NativeSwarmAccess) RequestDispatch(req sessions.RequestDispatchIntent) (sessions.DispatchIntentResult, error) {
	a, t, e := p.authenticate()
	if e != nil {
		return sessions.DispatchIntentResult{}, e
	}
	return a.RequestDispatchIntent(t, p.connection.SessionID, req)
}
func (p *NativeSwarmAccess) CorrelateDispatch(req sessions.CorrelateDispatchIntent) (sessions.DispatchIntent, error) {
	a, t, e := p.authenticate()
	if e != nil {
		return sessions.DispatchIntent{}, e
	}
	return a.CorrelateDispatchIntent(t, p.connection.SessionID, req)
}
func (p *NativeSwarmAccess) AcknowledgeDispatch(req sessions.AcknowledgeDispatchIntent) (sessions.DispatchIntent, error) {
	a, t, e := p.authenticate()
	if e != nil {
		return sessions.DispatchIntent{}, e
	}
	return a.AcknowledgeDispatchIntent(t, p.connection.SessionID, req)
}
func (p *NativeSwarmAccess) DispatchStatus(id string) (sessions.DispatchIntent, error) {
	a, t, e := p.authenticate()
	if e != nil {
		return sessions.DispatchIntent{}, e
	}
	return a.DispatchIntentStatus(t, p.connection.SessionID, id)
}

func probeHash(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, e := hex.DecodeString(value)
	return e == nil
}
func probeReceiptShape(data []byte) bool {
	var root map[string]json.RawMessage
	if json.Unmarshal(data, &root) != nil {
		return false
	}
	required := func(m map[string]json.RawMessage, names ...string) bool {
		for _, name := range names {
			v, ok := m[name]
			if !ok || string(v) == "null" {
				return false
			}
		}
		return true
	}
	if !required(root, "schema_version", "probe", "observed_at", "host_version", "binary_sha256", "client_protocol_sha256", "declared_method_count", "app_server_dispatch", "thread_config_schema", "native_collaboration", "private_child_profile_delivery", "stable_dispatch_key_lookup", "cancellation", "scope", "app_server_exit_code") {
		return false
	}
	for _, row := range []struct {
		key   string
		names []string
	}{{"scope", []string{"isolated_codex_home", "thread_created", "model_turn_requested", "worker_credentials_read", "native_child_dispatched"}}, {"cancellation", []string{"method_declared", "recognized_method_refusal", "running_turn_cancellation"}}} {
		var m map[string]json.RawMessage
		if json.Unmarshal(root[row.key], &m) != nil || !required(m, row.names...) {
			return false
		}
	}
	for _, row := range []struct {
		key             string
		entries, fields []string
	}{{"app_server_dispatch", []string{"agent/spawn", "collaboration/spawn_agent"}, []string{"declared_in_protocol", "unknown_method_refusal_observed", "status"}}, {"thread_config_schema", []string{"ThreadStartParams", "ThreadForkParams"}, []string{"config_field_declared", "schema_sha256"}}} {
		var m map[string]json.RawMessage
		if json.Unmarshal(root[row.key], &m) != nil {
			return false
		}
		for _, name := range row.entries {
			var e map[string]json.RawMessage
			if json.Unmarshal(m[name], &e) != nil || !required(e, row.fields...) {
				return false
			}
		}
	}
	return true
}

var codexHostVersionPattern = regexp.MustCompile(`^codex-cli (0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$`)

func validCodexHostVersion(value string) bool {
	return len(value) <= 160 && codexHostVersionPattern.MatchString(value)
}
