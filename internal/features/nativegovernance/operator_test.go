package nativegovernance

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/mcp/native"
	"github.com/mlnomadpy/dacli/internal/store"
)

// The connection is a capability, not a restriction on which server its holder
// starts. Operator enrollment must remain narrow through generic native MCP.
func TestNativeOperatorConnectionCannotReadThroughGenericMCP(t *testing.T) {
	f := setupVerification(t, "printf verified", false)
	if _, err := store.CreateProject(f.w, "root", "Foreign", "foreign", "goal", ""); err != nil {
		t.Fatal(err)
	}
	foreign, err := store.CreateTask(f.w, "root", "foreign", "Foreign private task", store.TaskOpts{})
	if err != nil {
		t.Fatal(err)
	}
	private := t.TempDir()
	connection := filepath.Join(private, "operator.json")
	if out, err := verificationRun(t, f.w, operatorAdd, "--config", f.config, "--project", "p", "--connection", connection, "--credential", filepath.Join(private, "operator.token")); err != nil {
		t.Fatalf("operator add: %v %s", err, out)
	}
	server, err := native.Open(connection)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	in, input := io.Pipe()
	output, out := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- server.ServeContext(ctx, in, out) }()
	t.Cleanup(func() {
		cancel()
		_ = input.Close()
		_ = output.Close()
		if err := <-done; err != nil {
			t.Errorf("native server cleanup: %v", err)
		}
	})
	enc, dec := json.NewEncoder(input), json.NewDecoder(output)
	exchange := func(id int, method string, params any) map[string]json.RawMessage {
		t.Helper()
		if err := enc.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
			t.Fatal(err)
		}
		var response map[string]json.RawMessage
		if err := dec.Decode(&response); err != nil {
			t.Fatal(err)
		}
		if response["error"] != nil {
			t.Fatalf("unexpected protocol failure: %s", response["error"])
		}
		return response
	}
	exchange(1, "initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "operator-regression", "version": "1"}})
	queries := []struct {
		name string
		args map[string]any
	}{
		{"task_list", map[string]any{"project": "foreign", "status": "open"}},
		{"task_get", map[string]any{"task": foreign.ID}},
		{"task_brief", map[string]any{"task": foreign.ID}},
		{"workspace_status", map[string]any{"project": "foreign"}},
		{"ready_frontier", map[string]any{"project": "foreign", "limit": 12}},
		{"task_list", map[string]any{"project": "p", "status": "open"}},
	}
	for i, query := range queries {
		query.args["schema_version"] = 1
		response := exchange(i+2, "tools/call", map[string]any{"name": query.name, "arguments": query.args})
		var result struct {
			IsError  bool `json:"isError"`
			Envelope struct {
				OK    bool `json:"ok"`
				Error struct {
					ExitCode int `json:"exit_code"`
				} `json:"error"`
			} `json:"structuredContent"`
		}
		if err := json.Unmarshal(response["result"], &result); err != nil {
			t.Fatal(err)
		}
		if !result.IsError || result.Envelope.OK || result.Envelope.Error.ExitCode != 3 {
			t.Fatalf("operator connection exposed generic %s: %s", query.name, response["result"])
		}
	}
}

func TestNativeOperatorAddProvisionsIndependentReadOnlyIdentity(t *testing.T) {
	f := setupVerification(t, "printf verified", false)
	private := t.TempDir()
	connection := filepath.Join(private, "operator.json")
	credential := filepath.Join(private, "operator.token")
	if out, err := verificationRun(t, f.w, operatorAdd, "--config", f.config, "--project", "p", "--connection", connection, "--credential", credential, "--expires", "1h"); err != nil {
		t.Fatalf("operator add: %v %s", err, out)
	}
	var c native.Connection
	if err := privateJSON(connection, &c); err != nil {
		t.Fatal(err)
	}
	token, err := native.ReadCredential(credential)
	if err != nil {
		t.Fatal(err)
	}
	actor, err := f.authority.Authenticate(token, c.SessionID)
	if err != nil || actor.ID == f.worker.SessionID || len(actor.Scope.Actions) != 1 || actor.Scope.Actions[0] != "operator.read" || len(actor.Scope.Resources) != 2 {
		t.Fatalf("operator identity not independently scoped: %v %+v", err, actor)
	}
	for _, path := range []string{connection, credential} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("private publication permissions: %v %v", info, err)
		}
	}
	snapshot, err := f.authority.ReadOperatorDashboard(token, c.SessionID, "p", "")
	if err != nil || snapshot.AuthorityVersion != 6 || len(snapshot.Records) == 0 {
		t.Fatalf("operator read or existing schema lost: %v %+v", err, snapshot)
	}
	if _, err := f.authority.RegisterAttempt(token, c.SessionID, sessions.AttemptRequest{RequestID: "operator-cannot-write", TaskID: f.task.ID, Checkout: f.w.Root}); err == nil {
		t.Fatal("read-only operator registered a coding attempt")
	}
	if _, err := f.authority.ReadOperatorDashboard(f.workerToken, f.worker.SessionID, "p", f.task.ID); err == nil {
		t.Fatal("ordinary worker accepted as operator")
	}
	if !AuthenticatesExplicitly("native operator add") {
		t.Fatal("owner provisioner bypassed explicit-owner dispatch")
	}
}

func TestNativeOperatorAddPreservesExistingDestinations(t *testing.T) {
	f := setupVerification(t, "printf verified", false)
	private := t.TempDir()
	connection := filepath.Join(private, "existing.json")
	credential := filepath.Join(private, "new.token")
	if err := os.WriteFile(connection, []byte("keep existing"), 0600); err != nil {
		t.Fatal(err)
	}
	authority := filepath.Join(filepath.Dir(f.config), "authority.json")
	before, err := os.ReadFile(authority)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verificationRun(t, f.w, operatorAdd, "--config", f.config, "--project", "p", "--connection", connection, "--credential", credential); err == nil {
		t.Fatal("existing connection overwritten")
	}
	if data, err := os.ReadFile(connection); err != nil || string(data) != "keep existing" {
		t.Fatal("existing private connection changed", err)
	}
	if _, err := os.Stat(credential); !os.IsNotExist(err) {
		t.Fatal("failed reservation retained new credential", err)
	}
	after, err := os.ReadFile(authority)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("refused provisioning changed authority", err)
	}
}
