package native

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/eventlog"
	"github.com/mlnomadpy/dacli/internal/governance/checkout"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

type fixture struct {
	s            *Server
	a            *sessions.Authority
	owner, token string
	c            Connection
	task         *store.Task
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	c := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if b, e := c.CombinedOutput(); e != nil {
		t.Fatalf("git: %v %s", e, b)
	}
}
func save(t *testing.T, p string, v any) {
	t.Helper()
	b, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(p, b, 0600); e != nil {
		t.Fatal(e)
	}
}
func setup(t *testing.T) fixture {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init")
	w, e := workspace.Init(dir, "native-fixture")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = store.CreateProject(w, "root", "Fixture", "p", "goal", ""); e != nil {
		t.Fatal(e)
	}
	task, e := store.CreateTask(w, "root", "p", "Implement fixture", store.TaskOpts{})
	if e != nil {
		t.Fatal(e)
	}
	git(t, dir, "add", ".")
	git(t, dir, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "fixture")
	private := t.TempDir()
	a, e := sessions.Open(filepath.Join(private, "authority.json"), sessions.Binding{Workspace: w.ID, Installation: "install"})
	if e != nil {
		t.Fatal(e)
	}
	owner, e := a.Bootstrap()
	if e != nil {
		t.Fatal(e)
	}
	obs, e := checkout.Observe(dir)
	if e != nil {
		t.Fatal(e)
	}
	if e = a.BindTask(owner, sessions.TaskBinding{TaskID: task.ID, ProjectID: "p", Repository: obs.Repository, AdmissionEnabled: true}); e != nil {
		t.Fatal(e)
	}
	principal, token, e := a.Enroll(owner, sessions.Scope{Actions: []string{"read", "attempt.register", "lease.acquire", "lease.renew", "lease.release", "attempt.publish", "evidence.submit"}, Resources: []string{"workspace:" + w.ID, "task:" + task.ID, "path:src"}}, time.Now().Add(time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	cred := filepath.Join(private, "session.token")
	if e = os.WriteFile(cred, []byte(token), 0600); e != nil {
		t.Fatal(e)
	}
	c := Connection{1, w.Root, filepath.Join(private, "authority.json"), w.ID, "install", principal.ID, cred}
	path := filepath.Join(private, "connection.json")
	save(t, path, c)
	s, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	return fixture{s, a, owner, token, c, task}
}
func call(t *testing.T, f fixture, name string, args any) any {
	t.Helper()
	b, _ := json.Marshal(args)
	v, e := f.s.call(name, b)
	if e != nil {
		t.Fatalf("%s: %v", name, e)
	}
	r := result(name, v, nil).(map[string]any)
	if r["isError"] != false {
		t.Fatalf("%s returned data outside output schema", name)
	}
	encoded, _ := json.Marshal(r["structuredContent"])
	if !validJSON(outputSchema(name), encoded) {
		t.Fatalf("%s invalid output", name)
	}
	return v
}
func TestNativeLifecycleDirectServices(t *testing.T) {
	f := setup(t)
	reg := call(t, f, "register_attempt", registerArgs{version{1}, sessions.AttemptRequest{RequestID: "register", TaskID: f.task.ID, Checkout: f.c.Workspace}}).(sessions.Attempt)
	lease := call(t, f, "acquire_lease", acquireArgs{version{1}, sessions.AcquireLeaseRequest{OperationKey: "lease", AttemptID: reg.ID, Resources: []sessions.Resource{{Kind: "path", Path: "src"}}, ExpiresAt: time.Now().Add(time.Minute)}}).(sessions.Lease)
	rev := call(t, f, "publish_revision", publishArgs{version{1}, sessions.PublishRevisionRequest{OperationKey: "publish", AttemptID: reg.ID, LeaseID: lease.ID, ExpectedFence: lease.Fence}}).(sessions.AttemptRevision)
	ev := call(t, f, "submit_evidence", evidenceArgs{version{1}, sessions.EvidenceRequest{OperationKey: "report", AttemptID: reg.ID, LeaseID: lease.ID, RevisionID: rev.ID, CheckID: "check", ExpectedFence: lease.Fence, OutputDigest: sessions.OutputDigest([]byte("public"))}}).(sessions.Evidence)
	if ev.Provenance != "self-reported" || ev.IssuerSession != f.c.SessionID || rev.Fence != lease.Fence {
		t.Fatal("evidence authority mismatch")
	}
	if e := os.WriteFile(filepath.Join(f.c.Workspace, "dirty"), []byte("pending"), 0600); e != nil {
		t.Fatal(e)
	}
	b, _ := json.Marshal(publishArgs{version{1}, sessions.PublishRevisionRequest{OperationKey: "dirty", AttemptID: reg.ID, LeaseID: lease.ID, ExpectedFence: lease.Fence, ExpectedRevision: 1}})
	if _, e := f.s.call("publish_revision", b); e == nil {
		t.Fatal("dirty revision allowed")
	}
	call(t, f, "renew_lease", renewArgs{version{1}, sessions.RenewLeaseRequest{OperationKey: "renew", AttemptID: reg.ID, LeaseID: lease.ID, ExpectedFence: lease.Fence, ExpiresAt: time.Now().Add(2 * time.Minute)}})
	call(t, f, "release_lease", releaseArgs{version{1}, sessions.ReleaseLeaseRequest{OperationKey: "release", AttemptID: reg.ID, LeaseID: lease.ID, ExpectedFence: lease.Fence}})
}
func TestNativeVersionAndCatalogRefusal(t *testing.T) {
	f := setup(t)
	for _, raw := range []string{`{}`, `{"schema_version":0}`, `{"schema_version":2}`, `{"schema_version":1,"token":"private"}`, `{"schema_version":1} {}`, `null`} {
		if _, e := f.s.call("identity", []byte(raw)); e == nil {
			t.Fatalf("schema accepted %s", raw)
		}
	}
	want := []string{"identity", "workspace_status", "task_list", "task_get", "register_attempt", "acquire_lease", "renew_lease", "release_lease", "publish_revision", "submit_evidence"}
	got := []string{}
	for _, tool := range catalog() {
		got = append(got, tool.Name)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("catalog widened: %v", got)
	}
	for _, n := range []string{"cli", "shell", "spawn", "enroll", "bootstrap", "verification_run", "accept", "grant"} {
		if _, e := f.s.call(n, []byte(`{"schema_version":1}`)); e == nil {
			t.Fatalf("forbidden tool %s allowed", n)
		}
	}
}
func TestNativeRevocationReauthenticatesCatalogAndCalls(t *testing.T) {
	f := setup(t)
	call(t, f, "identity", version{1})
	if e := f.a.Revoke(f.owner, f.c.SessionID); e != nil {
		t.Fatal(e)
	}
	if _, e := f.s.call("identity", []byte(`{"schema_version":1}`)); e == nil {
		t.Fatal("revoked identity accepted")
	}
	var out bytes.Buffer
	if e := f.s.Serve(strings.NewReader("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/list\",\"params\":{}}\n"), &out); e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(out.String(), "-32003") || strings.Contains(out.String(), "inputSchema") {
		t.Fatal("revoked catalog disclosed", out.String())
	}
}
func TestNativeWrongBindingCredentialAndReadScope(t *testing.T) {
	for _, kind := range []string{"workspace", "installation", "session", "token", "mode", "symlink", "unknown-config"} {
		t.Run(kind, func(t *testing.T) {
			f := setup(t)
			switch kind {
			case "workspace":
				f.c.WorkspaceID = "wrong"
			case "installation":
				f.c.InstallationID = "wrong"
			case "session":
				f.c.SessionID = "wrong"
			case "token":
				_ = os.WriteFile(f.c.CredentialFile, []byte("private-invalid"), 0600)
			case "mode":
				_ = os.Chmod(f.c.CredentialFile, 0644)
			case "symlink":
				_ = os.Remove(f.c.CredentialFile)
				_ = os.Symlink(f.s.path, f.c.CredentialFile)
			}
			save(t, f.s.path, f.c)
			if kind == "unknown-config" {
				_ = os.WriteFile(f.s.path, []byte(`{"schema_version":1,"secret":"private"}`), 0600)
			}
			if _, e := Open(f.s.path); e == nil {
				t.Fatal("invalid binding accepted")
			}
		})
	}
	f := setup(t)
	p, token, e := f.a.Enroll(f.owner, sessions.Scope{Actions: []string{"read"}, Resources: []string{"task:" + f.task.ID}}, time.Now().Add(time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	f.c.SessionID = p.ID
	save(t, f.s.path, f.c)
	_ = os.WriteFile(f.c.CredentialFile, []byte(token), 0600)
	f.s, e = Open(f.s.path)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.call("task_list", []byte(`{"schema_version":1,"project":"","status":""}`)); e == nil {
		t.Fatal("task-only scope read whole workspace")
	}
}
func TestNativeProtocolStructuredFallbackAndBoundedFrames(t *testing.T) {
	f := setup(t)
	input := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"fixture"}}}` + "\n" + `{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n" + `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}` + "\n" + `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"identity","arguments":{"schema_version":1}}}` + "\n" + `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"cli","arguments":{"schema_version":1}}}` + "\n"
	var out bytes.Buffer
	if e := f.s.Serve(strings.NewReader(input), &out); e != nil {
		t.Fatal(e)
	}
	d := json.NewDecoder(&out)
	n := 0
	for {
		var r map[string]json.RawMessage
		e := d.Decode(&r)
		if errors.Is(e, io.EOF) {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		n++
		if n < 3 {
			continue
		}
		var result struct {
			Structured json.RawMessage `json:"structuredContent"`
			Content    []struct {
				Text string `json:"text"`
			} `json:"content"`
		}
		if e = json.Unmarshal(r["result"], &result); e != nil {
			t.Fatal(e)
		}
		var a, b any
		_ = json.Unmarshal(result.Structured, &a)
		_ = json.Unmarshal([]byte(result.Content[0].Text), &b)
		if !reflect.DeepEqual(a, b) {
			t.Fatal("fallback disagrees")
		}
	}
	if n != 4 {
		t.Fatalf("responses=%d", n)
	}
	if e := f.s.Serve(strings.NewReader(strings.Repeat("x", 1<<20)), io.Discard); e == nil {
		t.Fatal("oversized frame accepted")
	}
}
func TestNativeTypedOutputSchemas(t *testing.T) {
	f := setup(t)
	call(t, f, "workspace_status", projectArgs{version{1}, ""})
	call(t, f, "task_list", listArgs{version{1}, "", ""})
	call(t, f, "task_get", taskArgs{version{1}, f.task.ID})
	for _, n := range names {
		r := result(n, nil, ErrDenied).(map[string]any)
		b, _ := json.Marshal(r["structuredContent"])
		if !validJSON(outputSchema(n), b) {
			t.Fatalf("invalid refusal for %s", n)
		}
		bad := envelope{1, true, "cooperative-session", map[string]any{}, nil}
		b, _ = json.Marshal(bad)
		if validJSON(outputSchema(n), b) {
			t.Fatalf("untyped empty success accepted for %s", n)
		}
	}
}
func TestNativeProtocolRejectsMalformedIDsAndMissingVersion(t *testing.T) {
	f := setup(t)
	for _, id := range []string{`{}`, `[]`, `true`} {
		var out bytes.Buffer
		if e := f.s.Serve(strings.NewReader(`{"jsonrpc":"2.0","id":`+id+`,"method":"ping"}`+"\n"), &out); e != nil {
			t.Fatal(e)
		}
		if !strings.Contains(out.String(), "-32600") {
			t.Fatalf("malformed ID accepted: %s", out.String())
		}
	}
	var out bytes.Buffer
	if e := f.s.Serve(strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"","capabilities":{},"clientInfo":{}}}`+"\n"), &out); e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(out.String(), "-32602") {
		t.Fatal("missing protocol version accepted")
	}
}
func TestNativeProtocolNegotiatesNewerVersion(t *testing.T) {
	f := setup(t)
	var out bytes.Buffer
	if e := f.s.Serve(strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2026-07-28","capabilities":{},"clientInfo":{"name":"fixture","version":"1"}}}`+"\n"), &out); e != nil {
		t.Fatal(e)
	}
	var r struct {
		Result struct {
			Version string `json:"protocolVersion"`
		} `json:"result"`
	}
	if e := json.Unmarshal(out.Bytes(), &r); e != nil {
		t.Fatal(e)
	}
	if r.Result.Version != "2025-06-18" {
		t.Fatalf("newer offer did not negotiate supported version: %s", out.String())
	}
}
func TestNativeProtocolMetadataIsNotIdentity(t *testing.T) {
	f := setup(t)
	init := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{}}}` + "\n"
	for _, method := range []string{"tools/call", "tools/list", "initialize"} {
		t.Run(method, func(t *testing.T) {
			params := map[string]any{"_meta": map[string]any{"progressToken": "public-progress", "session_id": "forged-session", "credential": "public-invalid"}}
			prefix := init
			switch method {
			case "tools/call":
				params["name"] = "identity"
				params["arguments"] = map[string]any{"schema_version": 1}
			case "initialize":
				prefix = ""
				params["protocolVersion"] = "2025-06-18"
				params["capabilities"] = map[string]any{}
				params["clientInfo"] = map[string]any{}
			}
			b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 2, "method": method, "params": params})
			var out bytes.Buffer
			if e := f.s.Serve(strings.NewReader(prefix+string(b)+"\n"), &out); e != nil {
				t.Fatal(e)
			}
			d := json.NewDecoder(&out)
			var response map[string]json.RawMessage
			for d.Decode(&response) == nil {
			}
			if len(response["error"]) > 0 {
				t.Fatalf("standard metadata refused: %s", response["error"])
			}
			if method == "tools/call" {
				var result struct {
					Structured struct {
						Data identityData `json:"data"`
					} `json:"structuredContent"`
				}
				if e := json.Unmarshal(response["result"], &result); e != nil {
					t.Fatal(e)
				}
				if result.Structured.Data.SessionID != f.c.SessionID {
					t.Fatal("metadata replaced authenticated session")
				}
			}
		})
	}
	// Metadata belongs to protocol params, never to the typed tool argument object.
	if _, e := f.s.call("identity", []byte(`{"schema_version":1,"_meta":{"progressToken":"public"}}`)); e == nil {
		t.Fatal("tool argument metadata bypassed strict schema")
	}
}
func TestNativeProtocolRejectsNullAndFractionalIDs(t *testing.T) {
	f := setup(t)
	for _, id := range []string{"null", "1.5", "-2.5"} {
		t.Run(id, func(t *testing.T) {
			var out bytes.Buffer
			if e := f.s.Serve(strings.NewReader(`{"jsonrpc":"2.0","id":`+id+`,"method":"ping"}`+"\n"), &out); e != nil {
				t.Fatal(e)
			}
			if !strings.Contains(out.String(), "-32600") {
				t.Fatalf("invalid MCP request ID accepted: %s", out.String())
			}
		})
	}
}

func TestNativeStatusIncompleteReadsAreSanitizedRefusals(t *testing.T) {
	for _, kind := range []string{"tasks", "events"} {
		t.Run(kind, func(t *testing.T) {
			f := setup(t)
			w, err := ResolveWorkspace(f.c.Workspace, f.c.WorkspaceID)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "tasks" {
				path := w.TasksDir("p", model.StatusOpen)
				if err = os.RemoveAll(path); err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(path, []byte("private task content"), 0600); err != nil {
					t.Fatal(err)
				}
				if _, err = store.ListTasks(w, "p", ""); err == nil {
					t.Fatal("fixture did not produce task read failure")
				}
			} else {
				if err = os.MkdirAll(w.EventsDir(), 0755); err != nil {
					t.Fatal(err)
				}
				if err = os.Symlink(filepath.Join(t.TempDir(), "missing-private-event"), filepath.Join(w.EventsDir(), "broken.md")); err != nil {
					t.Fatal(err)
				}
				_, holes, readErr := eventlog.ListReport(w, eventlog.Query{Pending: true})
				if readErr != nil || len(holes) != 1 {
					t.Fatalf("fixture did not produce event hole: %v %v", holes, readErr)
				}
			}
			input := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}` + "\n" + `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"workspace_status","arguments":{"schema_version":1,"project":"p"}}}` + "\n"
			var out bytes.Buffer
			if err = f.s.Serve(strings.NewReader(input), &out); err != nil {
				t.Fatal(err)
			}
			raw := out.String()
			for _, secret := range []string{f.c.Workspace, f.c.Authority, f.c.CredentialFile, f.token, "private task content", "missing-private-event"} {
				if strings.Contains(raw, secret) {
					t.Fatal("native response leaked private data")
				}
			}
			decoder := json.NewDecoder(strings.NewReader(raw))
			var response struct {
				Result struct {
					IsError    bool     `json:"isError"`
					Structured envelope `json:"structuredContent"`
				} `json:"result"`
			}
			if err = decoder.Decode(&response); err != nil {
				t.Fatal(err)
			}
			if err = decoder.Decode(&response); err != nil {
				t.Fatal(err)
			}
			got := response.Result
			if !got.IsError || got.Structured.OK || got.Structured.Error == nil || got.Structured.Error.ExitCode != 3 || got.Structured.Error.Code != "refused" {
				t.Fatalf("incomplete status did not produce typed refusal: %s", raw)
			}
		})
	}
}
