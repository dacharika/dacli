package cli

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/mcp/native"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

func TestNativeOwnerDispatcherUsesOnlyExplicitAuthority(t *testing.T) {
	dir := t.TempDir()
	w, e := workspace.Init(dir, "native-owner")
	if e != nil {
		t.Fatal(e)
	}
	for _, args := range [][]string{{"init"}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "fixture"}} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if b, e := cmd.CombinedOutput(); e != nil {
			t.Fatalf("git %v %s", e, b)
		}
	}
	if _, e = store.CreateProject(w, "root", "Fixture", "p", "goal", ""); e != nil {
		t.Fatal(e)
	}
	task, e := store.CreateTask(w, "root", "p", "Implement fixture", store.TaskOpts{})
	if e != nil {
		t.Fatal(e)
	}
	token := "public-legacy-ro-fixture"
	sum := sha256.Sum256([]byte(token))
	data := fmt.Sprintf("---\nid: a-fixture\nkind: agent\ngrant: ro\ntoken_hash: sha256:%s\n---\n# Fixture\n", hex.EncodeToString(sum[:]))
	if e = os.WriteFile(filepath.Join(w.Root, ".dacli", "agents", "a-fixture.md"), []byte(data), 0600); e != nil {
		t.Fatal(e)
	}
	for _, ambient := range []string{token, "invalid-legacy-token"} {
		t.Run(ambient, func(t *testing.T) {
			t.Setenv("DACLI_AGENT", ambient)
			p := t.TempDir()
			cfg := filepath.Join(p, "owner.json")
			owner := filepath.Join(p, "owner.token")
			b, _ := json.Marshal(map[string]any{"schema_version": 1, "workspace": w.Root, "authority": filepath.Join(p, "authority.json"), "installation_id": "fixture", "owner_credential_file": owner})
			if e = os.WriteFile(cfg, b, 0600); e != nil {
				t.Fatal(e)
			}
			run := func(argv ...string) (string, error) {
				var out bytes.Buffer
				ctx := &Ctx{Cwd: dir, Stdout: &out, Stderr: &out}
				cmd, args := dispatch(argv)
				if cmd == nil {
					t.Fatal("native command missing")
				}
				returnText := func() (string, error) { err := invoke(ctx, cmd, args); return out.String(), err }
				return returnText()
			}
			if _, e := run("native", "revoke", "--help"); e != nil {
				t.Fatalf("revoke help refused: %v", e)
			}
			for _, args := range [][]string{{"--json"}, {}, {"--config", cfg}, {"--config", cfg, "--session", "id", "--bogus"}, {"--config", cfg, "--session", "id", "extra"}, {"--config", cfg, "--session", "id", "--session", "other"}} {
				if _, e := run(append([]string{"native", "revoke"}, args...)...); clikit.ExitCode(e) != 2 {
					t.Fatalf("revoke usage %v: %v", args, e)
				}
			}
			if _, e := run("native", "bootstrap", "--config", cfg); e != nil {
				t.Fatalf("explicit bootstrap blocked by ambient: %v", e)
			}
			before, err := os.ReadFile(filepath.Join(p, "authority.json"))
			if err != nil {
				t.Fatal(err)
			}
			badConn, badCred := filepath.Join(p, "traversal.json"), filepath.Join(p, "traversal.token")
			if _, err = run("native", "enroll", "--config", cfg, "--task", task.ID, "--connection", badConn, "--credential", badCred, "--path", "../src"); clikit.ExitCode(err) != 3 {
				t.Fatalf("traversal enrollment accepted: %v", err)
			}
			after, err := os.ReadFile(filepath.Join(p, "authority.json"))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("refused traversal changed authority")
			}
			for _, path := range []string{badConn, badCred} {
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Fatal("refused traversal left output file")
				}
			}
			raw, e := native.ReadCredential(owner)
			if e != nil {
				t.Fatal(e)
			}
			conn := filepath.Join(p, "connection.json")
			cred := filepath.Join(p, "session.token")
			out, e := run("native", "enroll", "--config", cfg, "--task", task.ID, "--connection", conn, "--credential", cred, "--path", "src")
			if e != nil {
				t.Fatalf("explicit enrollment blocked: %v", e)
			}
			sessionToken, e := native.ReadCredential(cred)
			if e != nil {
				t.Fatal(e)
			}
			if strings.Contains(out, raw) || strings.Contains(out, sessionToken) {
				t.Fatal("credential leaked")
			}
			server, err := native.Open(conn)
			if err != nil {
				t.Fatal(err)
			}
			request := nativeLiveRequests(t, server)
			request(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"fixture","version":"1"}}}`, false)
			request(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`, false)
			connBytes, err := os.ReadFile(conn)
			if err != nil {
				t.Fatal(err)
			}
			var connection native.Connection
			if err = json.Unmarshal(connBytes, &connection); err != nil {
				t.Fatal(err)
			}
			for _, badConfig := range []string{filepath.Join(p, "absent"), cfg} {
				if err = os.WriteFile(owner, []byte("invalid-native-owner"), 0600); err != nil {
					t.Fatal(err)
				}
				before, err := os.ReadFile(filepath.Join(p, "authority.json"))
				if err != nil {
					t.Fatal(err)
				}
				out, err := run("native", "revoke", "--config", badConfig, "--session", connection.SessionID)
				if clikit.ExitCode(err) != 3 {
					t.Fatalf("invalid revoke owner accepted: %v", err)
				}
				if strings.Contains(out, raw) || strings.Contains(out, sessionToken) {
					t.Fatal("revoke credential leaked")
				}
				after, err := os.ReadFile(filepath.Join(p, "authority.json"))
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(before, after) {
					t.Fatal("invalid revoke changed authority")
				}
				request(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"identity","arguments":{"schema_version":1}}}`, false)
			}
			if err = os.WriteFile(owner, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err = run("native", "revoke", "--config", cfg, "--session", "unknown-session"); clikit.ExitCode(err) != 3 {
				t.Fatalf("unknown session accepted: %v", err)
			}
			out, err = run("native", "revoke", "--config", cfg, "--session", connection.SessionID)
			if err != nil {
				t.Fatalf("explicit revocation blocked by ambient: %v", err)
			}
			if strings.Contains(out, raw) || strings.Contains(out, sessionToken) {
				t.Fatal("revoke credential leaked")
			}
			if _, err = run("native", "revoke", "--config", cfg, "--session", connection.SessionID); err != nil {
				t.Fatalf("repeat revocation: %v", err)
			}
			if saved, err := os.ReadFile(conn); err != nil || !bytes.Equal(saved, connBytes) {
				t.Fatal("revocation changed connection file")
			}
			if saved, err := native.ReadCredential(cred); err != nil || saved != sessionToken {
				t.Fatal("revocation changed credential file")
			}
			request(`{"jsonrpc":"2.0","id":4,"method":"tools/list","params":{}}`, true)
			request(`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"identity","arguments":{"schema_version":1}}}`, true)
			if _, e = run("native", "bootstrap", "--config", cfg); clikit.ExitCode(e) != 3 {
				t.Fatalf("existing bootstrap not refused: %v", e)
			}
			if e = os.WriteFile(owner, []byte("invalid-native-owner"), 0600); e != nil {
				t.Fatal(e)
			}
			if _, e = run("native", "enroll", "--config", cfg, "--task", task.ID, "--connection", filepath.Join(p, "bad-conn"), "--credential", filepath.Join(p, "bad-token")); clikit.ExitCode(e) != 3 {
				t.Fatalf("invalid native owner accepted: %v", e)
			}
			if _, e = run("native", "enroll", "--config", filepath.Join(p, "absent"), "--task", task.ID, "--connection", conn, "--credential", cred); clikit.ExitCode(e) != 3 {
				t.Fatalf("missing owner fallback: %v", e)
			}
		})
	}
}
func TestNativeOwnerCommandsNeverReachLegacyMCP(t *testing.T) {
	for _, path := range [][]string{{"native", "bootstrap"}, {"native", "enroll"}, {"native", "serve"}, {"native", "revoke"}} {
		_, msg, code := executor(t.TempDir())(path, false)
		if code != 3 || !strings.Contains(msg, "local-only") {
			t.Fatalf("native command reached legacy MCP: %v code%d %s", path, code, msg)
		}
	}
}

// The same running server must reauthenticate after the real owner command.
func nativeLiveRequests(t *testing.T, server *native.Server) func(string, bool) {
	t.Helper()
	input, writer := io.Pipe()
	output, reader := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- server.Serve(input, reader); _ = reader.Close() }()
	t.Cleanup(func() {
		_ = writer.Close()
		_ = output.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("native server did not stop")
		}
	})
	scan := bufio.NewReader(output)
	return func(frame string, refused bool) {
		t.Helper()
		result := make(chan []byte, 1)
		go func() { _, _ = fmt.Fprintln(writer, frame); b, _ := scan.ReadBytes('\n'); result <- b }()
		select {
		case b := <-result:
			var response struct {
				Error *struct {
					Code int `json:"code"`
				} `json:"error"`
				Result json.RawMessage `json:"result"`
			}
			if err := json.Unmarshal(b, &response); err != nil {
				t.Fatalf("invalid native response %s: %v", b, err)
			}
			if refused {
				if response.Error == nil || response.Error.Code != -32003 {
					t.Fatalf("revoked live request accepted: %s", b)
				}
			} else if response.Error != nil || len(response.Result) == 0 {
				t.Fatalf("live request refused: %s", b)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("native response timeout")
		}
	}
}
