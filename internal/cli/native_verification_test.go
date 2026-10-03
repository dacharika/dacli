package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/application"
	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/mcp/native"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

func TestNativeNamedVerificationDispatcherAndLocalOnly(t *testing.T) {
	t.Setenv("DACLI_AGENT", "invalid-ambient-legacy-token")
	w, e := workspace.Init(t.TempDir(), "fixture")
	if e != nil {
		t.Fatal(e)
	}
	git := func(args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = w.Root
		c.Env = store.VerificationEnvironment()
		if b, e := c.CombinedOutput(); e != nil {
			t.Fatalf("git %v %v %s", args, e, b)
		}
	}
	git("init", "-b", "main")
	if _, e = store.CreateProject(w, "root", "Fixture", "p", "goal", ""); e != nil {
		t.Fatal(e)
	}
	task, e := store.CreateTask(w, "root", "p", "Verify", store.TaskOpts{})
	if e != nil {
		t.Fatal(e)
	}
	if e = os.MkdirAll(filepath.Join(w.Root, "src"), 0700); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(w.Root, "src", "marker"), []byte("base"), 0600); e != nil {
		t.Fatal(e)
	}
	git("add", ".")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "base")
	w, e = native.ResolveWorkspace(w.Root, "")
	if e != nil {
		t.Fatal(e)
	}
	private := t.TempDir()
	cfg := filepath.Join(private, "owner.json")
	authority := filepath.Join(private, "authority.json")
	conn := filepath.Join(private, "worker.json")
	credential := filepath.Join(private, "worker.token")
	checks := filepath.Join(private, "checks.json")
	profile := filepath.Join(private, "runner.json")
	runnerToken := filepath.Join(private, "runner.token")
	request := filepath.Join(private, "request.json")
	marker := filepath.Join(private, "count")
	write := func(path string, value any) {
		t.Helper()
		b, e := json.Marshal(value)
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(path, b, 0600); e != nil {
			t.Fatal(e)
		}
	}
	write(cfg, map[string]any{"schema_version": 1, "workspace": w.Root, "authority": authority, "installation_id": "fixture", "owner_credential_file": filepath.Join(private, "owner.token")})
	call := func(jsonMode bool, args ...string) (string, error) {
		t.Helper()
		var out bytes.Buffer
		ctx := &Ctx{Cwd: w.Root, Stdout: &out, Stderr: &out, JSON: jsonMode}
		cmd, rest := dispatch(args)
		if cmd == nil {
			t.Fatal("native dispatcher command missing", args)
		}
		e := invoke(ctx, cmd, rest)
		return out.String(), e
	}
	must := func(jsonMode bool, args ...string) string {
		t.Helper()
		out, e := call(jsonMode, args...)
		if e != nil {
			t.Fatalf("native dispatcher %v: %v %s", args, e, out)
		}
		return out
	}
	must(false, "native", "bootstrap", "--config", cfg)
	for _, value := range []string{"false", "garbage", "0"} {
		if _, e := call(false, "native", "enroll", "--config", cfg, "--task", task.ID, "--connection", conn, "--credential", credential, "--verification="+value); clikit.ExitCode(e) != 2 {
			t.Fatal("invalid opt-in value accepted", value, e)
		}
	}
	must(false, "native", "enroll", "--config", cfg, "--task", task.ID, "--connection", conn, "--credential", credential, "--path", "src", "--verification")
	var connection native.Connection
	b, e := native.ReadPrivate(conn, 16384)
	if e != nil || native.Decode(b, &connection) != nil {
		t.Fatal("connection unreadable", e)
	}
	token, e := native.ReadCredential(credential)
	if e != nil {
		t.Fatal(e)
	}
	admission, e := application.NewLeaseAdmission(w)
	if e != nil {
		t.Fatal(e)
	}
	a, e := sessions.Open(authority, sessions.Binding{Workspace: w.ID, Installation: "fixture"}, admission)
	if e != nil {
		t.Fatal(e)
	}
	at, e := a.RegisterAttempt(token, connection.SessionID, sessions.AttemptRequest{RequestID: "attempt", TaskID: task.ID, Checkout: w.Root})
	if e != nil {
		t.Fatal(e)
	}
	lease, e := a.AcquireLease(token, connection.SessionID, sessions.AcquireLeaseRequest{OperationKey: "lease", AttemptID: at.ID, Resources: []sessions.Resource{{Kind: "path", Path: "src"}}, ExpiresAt: time.Now().Add(10 * time.Minute)})
	if e != nil {
		t.Fatal(e)
	}
	revision, e := a.PublishAttemptRevision(token, connection.SessionID, sessions.PublishRevisionRequest{OperationKey: "publish", AttemptID: at.ID, LeaseID: lease.ID, ExpectedFence: lease.Fence})
	if e != nil {
		t.Fatal(e)
	}
	write(checks, map[string]any{"schema_version": 1, "checks": []any{map[string]any{"id": "unit", "command": "printf ran > '" + marker + "'", "deadline": "4s"}}})
	must(false, "native", "verifier", "add", "--config", cfg, "--task", task.ID, "--checks-file", checks, "--out", profile, "--credential", runnerToken)
	write(request, map[string]any{"schema_version": 1, "operation_key": "verify", "attempt_id": at.ID, "lease_id": lease.ID, "revision_id": revision.ID, "check_id": "unit", "expected_fence": lease.Fence})
	verifyArgs := []string{"native", "verify", "--connection", conn, "--runner-config", profile, "--request", request}
	if _, e := call(true, append(append([]string{}, verifyArgs...), "--command", "caller-command")...); clikit.ExitCode(e) != 2 {
		t.Fatal("caller command flag accepted", e)
	}
	for _, args := range [][]string{verifyArgs, {"native", "verifier", "add", "--config", cfg, "--task", task.ID, "--checks-file", checks, "--out", profile + ".remote", "--credential", runnerToken + ".remote"}} {
		stdout, stderr, code := executor(w.Root)(args, args[1] == "verify")
		if code != 3 || stdout != "" || !strings.Contains(stderr, "local-only") {
			t.Fatal("legacy MCP exposed local verifier", code, stdout, stderr)
		}
	}
	if _, e = os.Stat(marker); !os.IsNotExist(e) {
		t.Fatal("refused transport ran command", e)
	}
	out := must(true, verifyArgs...)
	var result struct {
		Schema   string            `json:"schema"`
		Evidence sessions.Evidence `json:"evidence"`
	}
	if e = json.Unmarshal([]byte(out), &result); e != nil || result.Schema != "native-verification/v1" || result.Evidence.Provenance != "trusted-local-runner" || result.Evidence.ExitCode != 0 {
		t.Fatal("dispatcher missing trusted JSON", e, out)
	}
	b, e = os.ReadFile(marker)
	if e != nil || string(b) != "ran" {
		t.Fatal("actual named command did not execute", e, string(b))
	}
	if strings.Contains(out, token) {
		t.Fatal("worker token leaked")
	}
}
