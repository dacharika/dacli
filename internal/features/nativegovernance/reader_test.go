package nativegovernance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/mcp/native"
	"github.com/mlnomadpy/dacli/internal/store"
)

type readerRPCResult struct {
	IsError           bool `json:"isError"`
	StructuredContent struct {
		OK   bool            `json:"ok"`
		Data json.RawMessage `json:"data"`
	} `json:"structuredContent"`
}

func readerRPC(t *testing.T, connection string, calls ...map[string]any) []readerRPCResult {
	t.Helper()
	server, err := native.Open(connection)
	if err != nil {
		t.Fatalf("open reader server: %v", err)
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
			t.Errorf("reader server cleanup: %v", err)
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
			t.Fatalf("unexpected MCP protocol error: %s", response["error"])
		}
		return response
	}
	exchange(1, "initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "reader-regression", "version": "1"}})
	results := make([]readerRPCResult, 0, len(calls))
	for i, call := range calls {
		response := exchange(i+2, "tools/call", call)
		var wire struct {
			Result readerRPCResult `json:"result"`
		}
		if err := json.Unmarshal(mustReaderJSON(response), &wire); err != nil {
			t.Fatal(err)
		}
		results = append(results, wire.Result)
	}
	return results
}

func mustReaderJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func readerTool(name string, args map[string]any) map[string]any {
	args["schema_version"] = 1
	return map[string]any{"name": name, "arguments": args}
}

func TestNativeReaderScopesAreReadOnlyThroughNativeMCP(t *testing.T) {
	f := setupVerification(t, "", false)
	sibling, err := store.CreateTask(f.w, "root", "p", "Sibling task", store.TaskOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.CreateProject(f.w, "root", "Foreign", "foreign", "goal", ""); err != nil {
		t.Fatal(err)
	}
	foreign, err := store.CreateTask(f.w, "root", "foreign", "Foreign task", store.TaskOpts{})
	if err != nil {
		t.Fatal(err)
	}
	private := t.TempDir()
	for _, tc := range []struct {
		name, selector, target, resource string
		allowedTask, deniedTask          string
	}{
		{"workspace", "--workspace", "", "workspace:" + f.w.ID, f.task.ID, ""},
		{"project", "--project", "p", "project:p", f.task.ID, foreign.ID},
		{"task", "--task", f.task.ID, "task:" + f.task.ID, f.task.ID, sibling.ID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			connection := filepath.Join(private, tc.name+".json")
			credential := filepath.Join(private, tc.name+".token")
			args := []string{"--config", f.config, tc.selector}
			if tc.target != "" {
				args = append(args, tc.target)
			}
			args = append(args, "--connection", connection, "--credential", credential)
			out, err := verificationRun(t, f.w, readerAdd, args...)
			if err != nil {
				t.Fatalf("reader add: %v %s", err, out)
			}
			if strings.Contains(out, mustToken(t, credential)) {
				t.Fatal("reader credential appeared in command output")
			}
			var conn native.Connection
			if err := privateJSON(connection, &conn); err != nil {
				t.Fatal(err)
			}
			token := mustToken(t, credential)
			actor, err := f.authority.Authenticate(token, conn.SessionID)
			if err != nil || len(actor.Scope.Actions) != 1 || actor.Scope.Actions[0] != "read" || len(actor.Scope.Resources) != 1 || actor.Scope.Resources[0] != tc.resource {
				t.Fatalf("reader had broader-than-selected authority: %v %+v", err, actor.Scope)
			}
			for _, path := range []string{connection, credential} {
				info, err := os.Stat(path)
				if err != nil || info.Mode().Perm() != 0600 {
					t.Fatalf("private output mode: %v %v", info, err)
				}
			}

			calls := []map[string]any{readerTool("identity", map[string]any{})}
			expectedOK := []bool{true}
			if tc.name == "workspace" {
				calls = append(calls, readerTool("task_list", map[string]any{"project": "foreign", "status": "open"}))
				expectedOK = append(expectedOK, true)
			} else {
				calls = append(calls, readerTool("task_brief", map[string]any{"task": tc.allowedTask}))
				expectedOK = append(expectedOK, true)
				if tc.name == "project" {
					calls = append(calls, readerTool("ready_frontier", map[string]any{"project": "p", "limit": 20}))
				} else {
					calls = append(calls, readerTool("ready_frontier", map[string]any{"task": tc.allowedTask, "limit": 20}))
				}
				expectedOK = append(expectedOK, true)
				calls = append(calls, readerTool("task_brief", map[string]any{"task": tc.deniedTask}))
				expectedOK = append(expectedOK, false)
			}
			mutations := []map[string]any{
				readerTool("register_attempt", map[string]any{"request": map[string]any{"RequestID": "reader-mutation", "TaskID": f.task.ID, "Checkout": f.w.Root}}),
				readerTool("acquire_lease", map[string]any{"request": map[string]any{"OperationKey": "reader-mutation", "AttemptID": "attempt", "Resources": []any{}, "ExpiresAt": time.Now().Add(time.Minute)}}),
				readerTool("renew_lease", map[string]any{"request": map[string]any{"OperationKey": "reader-mutation", "AttemptID": "attempt", "LeaseID": "lease", "ExpectedFence": 1, "ExpiresAt": time.Now().Add(time.Minute)}}),
				readerTool("release_lease", map[string]any{"request": map[string]any{"OperationKey": "reader-mutation", "AttemptID": "attempt", "LeaseID": "lease", "ExpectedFence": 1}}),
				readerTool("publish_revision", map[string]any{"request": map[string]any{"OperationKey": "reader-mutation", "AttemptID": "attempt", "LeaseID": "lease", "ExpectedFence": 1, "ExpectedRevision": 0}}),
				readerTool("submit_evidence", map[string]any{"request": map[string]any{"OperationKey": "reader-mutation", "AttemptID": "attempt", "LeaseID": "lease", "RevisionID": "revision", "CheckID": "check", "ExpectedFence": 1, "ExitCode": 0, "OutputDigest": strings.Repeat("a", 64)}}),
				readerTool("request_verification", map[string]any{"request": map[string]any{"operation_key": "reader-mutation", "worker_session": "session", "attempt_id": "attempt", "lease_id": "lease", "revision_id": "revision", "check_id": "check", "expected_fence": 1}}),
				readerTool("cancel_verification", map[string]any{"request": map[string]any{"operation_key": "reader-mutation", "worker_session": "session", "attempt_id": "attempt", "lease_id": "lease", "revision_id": "revision", "check_id": "check", "expected_fence": 1}}),
				readerTool("evaluate_review", map[string]any{"review_id": "review"}),
				readerTool("controller_step", map[string]any{"schema_version": 1}),
				readerTool("native_enroll", map[string]any{"schema_version": 1}),
				readerTool("native_revoke", map[string]any{"schema_version": 1}),
			}
			for _, mutation := range mutations {
				calls = append(calls, mutation)
				expectedOK = append(expectedOK, false)
			}
			results := readerRPC(t, connection, calls...)
			if results[0].IsError || !results[0].StructuredContent.OK {
				t.Fatalf("reader identity unavailable: %+v", results[0])
			}
			var identity struct {
				Scope sessions.Scope `json:"scope"`
			}
			if err := json.Unmarshal(results[0].StructuredContent.Data, &identity); err != nil || len(identity.Scope.Actions) != 1 || identity.Scope.Actions[0] != "read" || len(identity.Scope.Resources) != 1 || identity.Scope.Resources[0] != tc.resource {
				t.Fatalf("MCP identity did not report exact read-only authority: %v %+v", err, identity.Scope)
			}
			for i := 1; i < len(results); i++ {
				if expectedOK[i] != (!results[i].IsError && results[i].StructuredContent.OK) {
					t.Fatalf("native MCP call %s had unexpected result: %+v", calls[i]["name"], results[i])
				}
			}
		})
	}
}

func mustToken(t *testing.T, path string) string {
	t.Helper()
	token, err := native.ReadCredential(path)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestNativeReaderRejectsMissingAndNoncanonicalScopes(t *testing.T) {
	f := setupVerification(t, "", false)
	private := t.TempDir()
	base := []string{"--config", f.config, "--connection", filepath.Join(private, "c.json"), "--credential", filepath.Join(private, "c.token")}
	for _, extra := range [][]string{{}, {"--project", "p", "--task", f.task.ID}, {"--project", "missing"}, {"--task", "Verify native revision"}, {"--task", "core/" + f.task.ID}, {"--workspace=false"}, {"--workspace", "false"}, {"--workspace", "--expires", "25h"}, {"--workspace", "--expires", "0s"}} {
		if _, err := verificationRun(t, f.w, readerAdd, append(append([]string(nil), base...), extra...)...); err == nil {
			t.Fatalf("invalid reader scope accepted: %v", extra)
		}
	}
}

func TestNativeReaderRefusesOutputCollisionsWithoutIssuing(t *testing.T) {
	f := setupVerification(t, "", false)
	private := t.TempDir()
	for _, tc := range []struct {
		name    string
		prepare func(string) error
	}{
		{"existing file", func(path string) error { return os.WriteFile(path, []byte("preserve"), 0600) }},
		{"symlink", func(path string) error {
			target := path + ".target"
			if err := os.WriteFile(target, []byte("preserve"), 0600); err != nil {
				return err
			}
			return os.Symlink(target, path)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			connection := filepath.Join(private, tc.name+".json")
			credential := filepath.Join(private, tc.name+".token")
			if err := tc.prepare(connection); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(filepath.Join(filepath.Dir(f.config), "authority.json"))
			if err != nil {
				t.Fatal(err)
			}
			out, err := verificationRun(t, f.w, readerAdd, "--config", f.config, "--workspace", "--connection", connection, "--credential", credential)
			if err == nil || strings.Contains(out, f.owner) {
				t.Fatal("collision was accepted or leaked a credential")
			}
			after, err := os.ReadFile(filepath.Join(filepath.Dir(f.config), "authority.json"))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("collision changed authority", err)
			}
			if _, err := os.Lstat(credential); !os.IsNotExist(err) {
				t.Fatal("collision left the reserved credential", err)
			}
		})
	}
	if _, err := verificationRun(t, f.w, readerAdd, "--config", f.config, "--workspace", "--connection", filepath.Join(private, "same"), "--credential", filepath.Join(private, "same")); err == nil {
		t.Fatal("same output path accepted")
	}
}

func TestNativeReaderPreservesIssuedActorOnPersistenceFailure(t *testing.T) {
	f := setupVerification(t, "", false)
	private := t.TempDir()
	connection, credential := filepath.Join(private, "reader.json"), filepath.Join(private, "reader.token")
	oldWriter := readerWritePrivate
	t.Cleanup(func() { readerWritePrivate = oldWriter })
	writes := 0
	readerWritePrivate = func(file *os.File, data []byte) error {
		writes++
		if writes == 2 {
			return errors.New("injected persistence failure")
		}
		return writePrivate(file, data)
	}
	out, err := verificationRun(t, f.w, readerAdd, "--config", f.config, "--workspace", "--connection", connection, "--credential", credential)
	if err == nil || clikit.ExitCode(err) != 1 || !strings.Contains(err.Error(), "reconcile the reserved files") || !strings.Contains(err.Error(), connection) || !strings.Contains(err.Error(), credential) {
		t.Fatalf("uncertain issuance lacked reconciliation details: %v", err)
	}
	token := mustToken(t, credential)
	if strings.Contains(out, token) || strings.Contains(err.Error(), token) {
		t.Fatal("reader token leaked from persistence failure")
	}
	fields := strings.Fields(err.Error())
	if len(fields) < 4 || fields[0] != "native" || fields[1] != "MCP" || fields[2] != "reader" {
		t.Fatalf("reconciliation error omitted a safe session identifier: %v", err)
	}
	sessionID := fields[3]
	// The profile write failed after issuance, so verify the issued identity is
	// still present in authority and was not revoked as an error cleanup step.
	if _, err := f.authority.Authenticate(token, sessionID); err != nil {
		t.Fatalf("issued reader was not retained: %v", err)
	}
	if info, err := os.Stat(connection); err != nil || info.Mode().Perm() != 0600 || info.Size() != 0 {
		t.Fatalf("reserved connection was not preserved privately: %v %v", info, err)
	}
	if info, err := os.Stat(credential); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("reserved credential was not preserved privately: %v %v", info, err)
	}
}

func TestNativeReaderExpiryAndRevocationAreRechecked(t *testing.T) {
	f := setupVerification(t, "", false)
	private := t.TempDir()
	connection, credential := filepath.Join(private, "reader.json"), filepath.Join(private, "reader.token")
	if _, err := verificationRun(t, f.w, readerAdd, "--config", f.config, "--workspace", "--connection", connection, "--credential", credential); err != nil {
		t.Fatal(err)
	}
	var conn native.Connection
	if err := privateJSON(connection, &conn); err != nil {
		t.Fatal(err)
	}
	if err := f.authority.Revoke(f.owner, conn.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := native.Open(connection); err == nil {
		t.Fatal("revoked reader authenticated through native MCP")
	}

	expiredConnection := filepath.Join(private, "expired.json")
	expiredCredential := filepath.Join(private, "expired.token")
	if _, err := verificationRun(t, f.w, readerAdd, "--config", f.config, "--workspace", "--connection", expiredConnection, "--credential", expiredCredential, "--expires", "10ms"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if _, err := native.Open(expiredConnection); err == nil {
		t.Fatal("expired reader authenticated through native MCP")
	}
}
