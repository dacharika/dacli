package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

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
			if _, e = native.Open(conn); e != nil {
				t.Fatal(e)
			}
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
	for _, path := range [][]string{{"native", "bootstrap"}, {"native", "enroll"}, {"native", "serve"}} {
		_, msg, code := executor(t.TempDir())(path, false)
		if code != 3 || !strings.Contains(msg, "local-only") {
			t.Fatalf("native command reached legacy MCP: %v code%d %s", path, code, msg)
		}
	}
}
