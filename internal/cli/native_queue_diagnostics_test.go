package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/agentid"
	"github.com/mlnomadpy/dacli/internal/governance/nativeconfig"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

func TestNativeQueueDiagnosticsThroughMainUsesOnlyOperatorScope(t *testing.T) {
	w, err := workspace.Init(t.TempDir(), "queue-main")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.CreateProject(w, "root", "Fixture", "p", "goal", ""); err != nil {
		t.Fatal(err)
	}
	if _, err = store.CreateTask(w, "root", "p", "Implement fixture", store.TaskOpts{}); err != nil {
		t.Fatal(err)
	}
	private := t.TempDir()
	authorityPath := filepath.Join(private, "authority.json")
	binding := sessions.Binding{Workspace: w.ID, Installation: "queue-main"}
	authority, err := sessions.Open(authorityPath, binding)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := authority.Bootstrap()
	if err != nil {
		t.Fatal(err)
	}
	connection := func(name string, actions, resources []string) string {
		t.Helper()
		actor, token, err := authority.Enroll(owner, sessions.Scope{Actions: actions, Resources: resources}, time.Now().Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		credential := filepath.Join(private, name+".token")
		if err = os.WriteFile(credential, []byte(token+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		selected := nativeconfig.Connection{SchemaVersion: 1, Workspace: w.Root, WorkspaceID: w.ID, Authority: authorityPath, InstallationID: binding.Installation, SessionID: actor.ID, CredentialFile: credential}
		raw, err := json.Marshal(selected)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(private, name+".json")
		if err = os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	resources := []string{"workspace:" + w.ID, "project:p"}
	operator := connection("operator", []string{"operator.read"}, resources)
	reader := connection("reader", []string{"read"}, resources)
	before, err := os.ReadFile(authorityPath)
	if err != nil {
		t.Fatal(err)
	}
	lock := authorityPath + ".lock"
	if err = os.Remove(lock); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	t.Setenv(agentid.EnvVar, "invalid-legacy-token")
	if out, code := runMainWithJSON(t, "native", "queue", "diagnostics"); code != 2 {
		t.Fatalf("missing explicit arguments: code=%d %s", code, out)
	}
	if out, code := runMainWithJSON(t, "native", "queue", "diagnostics", "--connection", operator, "--project", "p", "--bad-queue-flag"); code != 2 || !strings.Contains(out, "--bad-queue-flag") {
		t.Fatalf("unknown flag was not rejected by name: code=%d %s", code, out)
	}
	for _, limit := range []string{"0", "101", "nonnumeric"} {
		if out, code := runMainWithJSON(t, "native", "queue", "diagnostics", "--connection", operator, "--project", "p", "--limit", limit); code != 2 {
			t.Fatalf("invalid limit%s: code=%d %s", limit, code, out)
		}
	}
	if out, code := runMainWithJSON(t, "native", "queue", "diagnostics", "--connection", reader, "--project", "p"); code != 3 {
		t.Fatalf("ordinary reader became operator: code=%d %s", code, out)
	}
	if out, code := runMainWithJSON(t, "native", "queue", "diagnostics", "--connection", operator, "--project", "foreign"); code != 3 {
		t.Fatalf("foreign scope accepted: code=%d %s", code, out)
	}
	output, code := runMainWithJSON(t, "native", "queue", "diagnostics", "--connection", operator, "--project", "p", "--limit", "1")
	var snapshot sessions.QueueDiagnosticsSnapshot
	if code != 0 || json.Unmarshal([]byte(output), &snapshot) != nil || snapshot.Schema != "native-queue-diagnostics/v1" || !snapshot.DiagnosticOnly || snapshot.Project != "p" || snapshot.TotalCount != 1 || len(snapshot.Rows) != 1 {
		t.Fatalf("real dispatcher did not return queue JSON: code=%d %s", code, output)
	}
	after, err := os.ReadFile(authorityPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("read changed authority: %v", err)
	}
	if _, err = os.Lstat(lock); !os.IsNotExist(err) {
		t.Fatalf("read created an authority lock: %v", err)
	}
	_, message, exit := executor(w.Root)([]string{"native", "queue", "diagnostics", "--connection", operator, "--project", "p"}, false)
	if exit != 3 || !strings.Contains(message, "local-only") {
		t.Fatalf("generic CLI MCP escaped explicit operator tool: exit=%d %s", exit, message)
	}
}
