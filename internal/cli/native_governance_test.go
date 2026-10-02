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
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
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
	for _, path := range [][]string{{"native", "bootstrap"}, {"native", "enroll"}, {"native", "serve"}, {"native", "revoke"}, {"native", "upgrade"}} {
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

func TestNativeUpgradeExplicitOwnerPreservesProfile(t *testing.T) {
	for _, ambient := range []string{"", "invalid-legacy-token"} {
		t.Run("ambient-"+ambient, func(t *testing.T) {
			t.Setenv("DACLI_AGENT", ambient)
			dir := t.TempDir()
			w, err := workspace.Init(dir, "upgrade")
			if err != nil {
				t.Fatal(err)
			}
			private := t.TempDir()
			authority := filepath.Join(private, "authority.json")
			ownerFile := filepath.Join(private, "owner.token")
			cfg := filepath.Join(private, "owner.json")
			a, err := sessions.Open(authority, sessions.Binding{Workspace: w.ID, Installation: "fixture"})
			if err != nil {
				t.Fatal(err)
			}
			owner, err := a.Bootstrap()
			if err != nil {
				t.Fatal(err)
			}
			session, token, err := a.Enroll(owner, sessions.Scope{Actions: []string{"read"}, Resources: []string{"workspace:" + w.ID}}, time.Now().Add(time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(ownerFile, []byte(owner), 0600); err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(map[string]any{"schema_version": 1, "workspace": dir, "authority": authority, "installation_id": "fixture", "owner_credential_file": ownerFile})
			if err = os.WriteFile(cfg, raw, 0600); err != nil {
				t.Fatal(err)
			}
			current, err := os.ReadFile(authority)
			if err != nil {
				t.Fatal(err)
			}
			var legacy map[string]json.RawMessage
			if err = json.Unmarshal(current, &legacy); err != nil {
				t.Fatal(err)
			}
			// Compatibility fixture only; smoke.py separately proves a genuine old binary producer.
			legacy["Version"] = json.RawMessage("5")
			for _, key := range []string{"PublicationPolicies", "PublicationDecisions", "PublicationConsumed"} {
				delete(legacy, key)
			}
			old, err := json.Marshal(legacy)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(authority, old, 0600); err != nil {
				t.Fatal(err)
			}
			run := func(args ...string) (string, error) {
				var out bytes.Buffer
				ctx := &Ctx{Cwd: dir, Stdout: &out, Stderr: &out}
				cmd, rest := dispatch(append([]string{"native", "upgrade"}, args...))
				if cmd == nil {
					t.Fatal("native upgrade missing")
				}
				err := invoke(ctx, cmd, rest)
				return out.String(), err
			}
			if _, err = run("--help"); err != nil {
				t.Fatal(err)
			}
			for _, args := range [][]string{{}, {"--json"}, {"--config", cfg, "--bogus"}, {"--config", cfg, "extra"}, {"--config", cfg, "--config", cfg}} {
				if _, err = run(args...); clikit.ExitCode(err) != 2 {
					t.Fatalf("upgrade usage accepted %v: %v", args, err)
				}
			}
			unchanged := func(want []byte) {
				t.Helper()
				got, e := os.ReadFile(authority)
				if e != nil || !bytes.Equal(got, want) {
					t.Fatal("refused upgrade changed authority")
				}
			}
			for _, bad := range []string{"missing-config", "missing-owner", "forged-owner"} {
				configPath := cfg
				switch bad {
				case "missing-config":
					configPath = filepath.Join(private, "absent")
				case "missing-owner":
					if err = os.Remove(ownerFile); err != nil {
						t.Fatal(err)
					}
				case "forged-owner":
					if err = os.WriteFile(ownerFile, []byte("forged"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if _, err = run("--config", configPath); clikit.ExitCode(err) != 3 {
					t.Fatalf("invalid upgrade owner accepted (%s): %v", bad, err)
				}
				unchanged(old)
			}
			if err = os.WriteFile(ownerFile, []byte(owner), 0600); err != nil {
				t.Fatal(err)
			}
			out, err := run("--config", cfg)
			if err != nil {
				t.Fatalf("explicit upgrade refused: %v", err)
			}
			if strings.Contains(out, owner) || strings.Contains(out, token) {
				t.Fatal("upgrade leaked credential")
			}
			upgraded, err := os.ReadFile(authority)
			if err != nil {
				t.Fatal(err)
			}
			var state map[string]json.RawMessage
			if err = json.Unmarshal(upgraded, &state); err != nil {
				t.Fatal(err)
			}
			if string(state["Version"]) != "6" {
				t.Fatal("target schema not6")
			}
			for key, want := range legacy {
				if key != "Version" && !bytes.Equal(state[key], want) {
					t.Fatalf("upgrade changed old field %s", key)
				}
			}
			if _, err = a.Authenticate(token, session.ID); err != nil {
				t.Fatal("session lost", err)
			}
			if _, err = run("--config", cfg); clikit.ExitCode(err) != 3 {
				t.Fatalf("already-target upgrade accepted: %v", err)
			}
			unchanged(upgraded)
			for _, data := range [][]byte{[]byte("{malformed"), bytes.Replace(old, []byte(`"Version":5`), []byte(`"Version":99`), 1)} {
				if err = os.WriteFile(authority, data, 0600); err != nil {
					t.Fatal(err)
				}
				if _, err = run("--config", cfg); clikit.ExitCode(err) != 3 {
					t.Fatalf("unsupported authority accepted: %v", err)
				}
				unchanged(data)
			}
		})
	}
}
