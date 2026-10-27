package native

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/governance/sessions"
)

type queueWireClient struct {
	input  *io.PipeWriter
	output *bufio.Reader
	done   chan error
	seq    int
}

func startQueueWireClient(t *testing.T, server *Server) *queueWireClient {
	t.Helper()
	serverInput, clientInput := io.Pipe()
	clientOutput, serverOutput := io.Pipe()
	client := &queueWireClient{input: clientInput, output: bufio.NewReader(clientOutput), done: make(chan error, 1)}
	go func() { client.done <- server.ServeContext(context.Background(), serverInput, serverOutput) }()
	t.Cleanup(func() {
		_ = client.input.Close()
		select {
		case <-client.done:
		case <-time.After(7 * time.Second):
			t.Error("MCP server did not stop after client EOF")
		}
	})
	return client
}

func (c *queueWireClient) request(t *testing.T, method string, params any) map[string]any {
	t.Helper()
	c.seq++
	frame, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": c.seq, "method": method, "params": params})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.input.Write(append(frame, '\n')); err != nil {
		t.Fatal(err)
	}
	line, err := c.output.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var response map[string]any
	if err = json.Unmarshal(line, &response); err != nil {
		t.Fatal(err)
	}
	return response
}

func operatorConnectionForTest(t *testing.T, f fixture, actions []string) (string, Connection) {
	t.Helper()
	principal, token, err := f.a.Enroll(f.owner, sessions.Scope{Actions: actions, Resources: []string{"workspace:" + f.c.WorkspaceID, "project:p"}}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	private := t.TempDir()
	credential := filepath.Join(private, "operator.token")
	if err := os.WriteFile(credential, []byte(token+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	connection := f.c
	connection.SessionID = principal.ID
	connection.CredentialFile = credential
	path := filepath.Join(private, "operator.json")
	save(t, path, connection)
	return path, connection
}

func queueWorkspaceDigest(t *testing.T, root string) string {
	t.Helper()
	base := filepath.Join(root, ".dacli")
	var entries []string
	err := filepath.WalkDir(base, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(base, path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		line := fmt.Sprintf("%s\x00%o\x00%d", rel, info.Mode(), info.Size())
		if info.Mode().IsRegular() {
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(content)
			line += "\x00" + hex.EncodeToString(sum[:])
		}
		entries = append(entries, line)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(entries)
	hash := sha256.Sum256([]byte(strings.Join(entries, "\n")))
	return hex.EncodeToString(hash[:])
}

func TestQueueDiagnosticsMCPArgsAreStrictAndBounded(t *testing.T) {
	inputSchema := schema(reflect.TypeOf(queueDiagnosticsArgs{}))
	if !validJSON(inputSchema, []byte(`{"schema_version":1,"project":"p"}`)) {
		t.Fatal("public queue input schema rejected valid default-limit arguments")
	}
	for _, raw := range []string{
		`{"schema_version":2,"project":"p"}`,
		`{"schema_version":1,"project":"p","limit":0}`,
		`{"schema_version":1,"project":"p","limit":null}`,
		`{"schema_version":1,"project":"p","limit":101}`,
		`{"schema_version":1,"project":"p","reserve":true}`,
	} {
		if validJSON(inputSchema, []byte(raw)) {
			t.Fatalf("public queue input schema accepted invalid arguments: %s", raw)
		}
	}
	var valid queueDiagnosticsArgs
	if err := Decode([]byte(`{"schema_version":1,"project":"p"}`), &valid); err != nil {
		t.Fatalf("valid project-only queue request rejected: %v", err)
	}
	if valid.Project != "p" || valid.Limit != nil {
		t.Fatalf("omitted limit did not retain default sentinel: %+v", valid)
	}
	for _, raw := range []string{
		`{"schema_version":2,"project":"p"}`,
		`{"schema_version":1,"project":"p","limit":0}`,
		`{"schema_version":1,"project":"p","limit":101}`,
		`{"schema_version":1,"project":"p","reserve":true}`,
		`{"schema_version":1,"project":""}`,
	} {
		var args queueDiagnosticsArgs
		if err := Decode([]byte(raw), &args); err == nil {
			if _, err = queueDiagnostics(context.Background(), "/not/opened", Connection{}, args); err == nil || !strings.Contains(err.Error(), "denied") {
				t.Fatalf("invalid queue request accepted: %s err=%v", raw, err)
			}
		}
	}
}

func TestQueueDiagnosticsMCPRequiresOperatorReadAndReturnsSharedDTO(t *testing.T) {
	f := setup(t)
	limit := 10
	projectScope := queueDiagnosticsArgs{version: version{SchemaVersion: 1}, Project: "p", Limit: &limit}
	if _, err := queueDiagnostics(context.Background(), f.s.path, f.c, projectScope); err == nil {
		t.Fatal("task-bound ordinary reader gained operator queue diagnostics")
	}
	principal, token, err := f.a.Enroll(f.owner, sessions.Scope{Actions: []string{"operator.read"}, Resources: []string{"workspace:" + f.c.WorkspaceID, "project:p"}}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	private := t.TempDir()
	credential := filepath.Join(private, "operator.token")
	if err := os.WriteFile(credential, []byte(token+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	connection := f.c
	connection.SessionID = principal.ID
	connection.CredentialFile = credential
	connectionPath := filepath.Join(private, "operator.json")
	save(t, connectionPath, connection)
	snapshot, err := queueDiagnostics(context.Background(), connectionPath, connection, projectScope)
	if err != nil {
		t.Fatalf("operator-scoped queue tool denied exact project: %v", err)
	}
	if snapshot.Schema != "native-queue-diagnostics/v1" || !snapshot.DiagnosticOnly || snapshot.Project != "p" || snapshot.Limit != 10 {
		t.Fatalf("MCP returned an unexpected queue DTO: %+v", snapshot)
	}
	if _, err := queueDiagnostics(context.Background(), connectionPath, connection, queueDiagnosticsArgs{version: version{1}, Project: "other", Limit: &limit}); err == nil {
		t.Fatal("project-scoped operator received another project's queue diagnostics")
	}
}

func TestQueueDiagnosticsMCPWireFlowUnderHeldLock(t *testing.T) {
	f := setup(t)
	connectionPath, _ := operatorConnectionForTest(t, f, []string{"operator.read"})
	server, err := Open(connectionPath)
	if err != nil {
		t.Fatal(err)
	}
	client := startQueueWireClient(t, server)
	initialized := client.request(t, "initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "queue-test"}})
	if initialized["error"] != nil {
		t.Fatalf("initialize refused: %+v", initialized)
	}
	listed := client.request(t, "tools/list", map[string]any{})
	listResult := listed["result"].(map[string]any)
	tools := listResult["tools"].([]any)
	var names []string
	for _, item := range tools {
		name := item.(map[string]any)["name"].(string)
		names = append(names, name)
	}
	if !reflect.DeepEqual(names, []string{"identity", "queue_diagnostics"}) {
		t.Fatalf("operator-only catalog widened or omitted queue diagnostics: %v", names)
	}
	for _, arguments := range []map[string]any{
		{"schema_version": 2, "project": "p"},
		{"schema_version": 1, "project": "p", "limit": 0},
		{"schema_version": 1, "project": "p", "limit": nil},
		{"schema_version": 1, "project": "p", "limit": 101},
		{"schema_version": 1, "project": "p", "reserve": true},
		{"schema_version": 1, "project": "foreign"},
	} {
		response := client.request(t, "tools/call", map[string]any{"name": "queue_diagnostics", "arguments": arguments})
		result := response["result"].(map[string]any)
		structured := result["structuredContent"].(map[string]any)
		failure := structured["error"].(map[string]any)
		if result["isError"] != true || failure["exit_code"] != float64(3) {
			t.Fatalf("invalid or foreign tool request did not return policy refusal: %+v", response)
		}
	}
	_, otherConnection := operatorConnectionForTest(t, f, []string{"operator.read"})

	lockPath := f.c.Authority + ".lock"
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(lockPath)
	authorityBefore, err := os.ReadFile(f.c.Authority)
	if err != nil {
		t.Fatal(err)
	}
	workspaceBefore := queueWorkspaceDigest(t, f.c.Workspace)
	positive := client.request(t, "tools/call", map[string]any{"name": "queue_diagnostics", "arguments": map[string]any{"schema_version": 1, "project": "p", "limit": 10}})
	positiveResult := positive["result"].(map[string]any)
	if positiveResult["isError"] != false {
		t.Fatalf("queue tool failed after startup while the authority lock was held: %+v", positive)
	}
	diagnostic := positiveResult["structuredContent"].(map[string]any)["data"].(map[string]any)
	if diagnostic["schema"] != "native-queue-diagnostics/v1" || diagnostic["project"] != "p" {
		t.Fatalf("wire tool returned wrong diagnostic DTO: %+v", diagnostic)
	}
	authorityAfter, err := os.ReadFile(f.c.Authority)
	if err != nil || !reflect.DeepEqual(authorityBefore, authorityAfter) {
		t.Fatalf("queue tool changed authority bytes: %v", err)
	}
	if got := queueWorkspaceDigest(t, f.c.Workspace); got != workspaceBefore {
		t.Fatalf("queue tool changed canonical workspace files: %s -> %s", workspaceBefore, got)
	}

	save(t, connectionPath, otherConnection)
	pinned := client.request(t, "tools/call", map[string]any{"name": "queue_diagnostics", "arguments": map[string]any{"schema_version": 1, "project": "p"}})
	pinnedResult := pinned["result"].(map[string]any)
	if pinnedResult["isError"] != true {
		t.Fatalf("server accepted a changed pinned private connection: %+v", pinned)
	}
}

func TestQueueDiagnosticsMCPWireDeniesOrdinaryTaskReader(t *testing.T) {
	f := setup(t)
	client := startQueueWireClient(t, f.s)
	client.request(t, "initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{}})
	client.request(t, "tools/list", map[string]any{})
	response := client.request(t, "tools/call", map[string]any{"name": "queue_diagnostics", "arguments": map[string]any{"schema_version": 1, "project": "p"}})
	result := response["result"].(map[string]any)
	structured := result["structuredContent"].(map[string]any)
	if result["isError"] != true || structured["error"].(map[string]any)["exit_code"] != float64(3) {
		t.Fatalf("ordinary task reader gained queue diagnostics: %+v", response)
	}
}
