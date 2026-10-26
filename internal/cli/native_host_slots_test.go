package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/agentid"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

// runMainWithJSON exercises Main's global --json preprocessing and the real
// command dispatcher while capturing its process output.
func runMainWithJSON(t *testing.T, args ...string) (string, int) {
	t.Helper()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = write, write
	code := Main(append(args, "--json"))
	_ = write.Close()
	os.Stdout, os.Stderr = oldOut, oldErr
	output, readErr := io.ReadAll(read)
	_ = read.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	return string(output), code
}

func TestNativeCapacitySlotsUpgradeThroughMainUsesExplicitOwner(t *testing.T) {
	w, err := workspace.Init(t.TempDir(), "native-capacity-slots")
	if err != nil {
		t.Fatal(err)
	}
	private := t.TempDir()
	authorityPath := filepath.Join(private, "authority.json")
	a, err := sessions.Open(authorityPath, sessions.Binding{Workspace: w.ID, Installation: "native-capacity-slots"})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := a.Bootstrap()
	if err != nil {
		t.Fatal(err)
	}
	upgrades := []func(string) error{a.UpgradeSwarms, a.UpgradeDispatchIntents, a.UpgradeLifecycle, a.UpgradeRecovery, a.UpgradePreflight, a.UpgradeCheckpoints, a.UpgradeBudgets, a.UpgradeNativeAcceptance, a.UpgradeNativePolicy, a.UpgradeController, a.UpgradeNativeModelRouting, a.UpgradeNativeIntegration, a.UpgradeNativeExternal, a.UpgradeNativeRoleRegistry, a.UpgradeHostCapacity}
	for _, upgrade := range upgrades {
		if err := upgrade(owner); err != nil {
			t.Fatalf("fixture upgrade to schema 21: %v", err)
		}
	}
	ownerCredential := filepath.Join(private, "owner.token")
	if err := os.WriteFile(ownerCredential, []byte(owner+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(private, "owner.json")
	config := map[string]any{"schema_version": 1, "workspace": w.Root, "authority": authorityPath, "installation_id": "native-capacity-slots", "owner_credential_file": ownerCredential}
	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	worker, workerToken, err := a.Enroll(owner, sessions.Scope{Actions: []string{"read"}, Resources: []string{"workspace:" + w.ID}}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	_ = worker
	workerCredential := filepath.Join(private, "worker.token")
	if err := os.WriteFile(workerCredential, []byte(workerToken+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	workerConfig := filepath.Join(private, "worker-owner.json")
	config["owner_credential_file"] = workerCredential
	encoded, _ = json.Marshal(config)
	if err := os.WriteFile(workerConfig, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(authorityPath)
	if err != nil {
		t.Fatal(err)
	}
	if output, code := runMainWithJSON(t, "native", "capacity", "slots", "upgrade"); code != 2 {
		t.Fatalf("missing config exit=%d output=%q", code, output)
	}
	if output, code := runMainWithJSON(t, "native", "capacity", "slots", "upgrade", "--config", workerConfig); code != 3 {
		t.Fatalf("worker config exit=%d output=%q", code, output)
	}
	after, err := os.ReadFile(authorityPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("missing or worker config changed authority: err=%v", err)
	}
	t.Setenv(agentid.EnvVar, "invalid-legacy-agent-token")
	if output, code := runMainWithJSON(t, "native", "capacity", "slots", "upgrade", "--config", configPath); code != 0 || output != `{"schema_version":1,"authority_schema":22,"upgraded":true}`+"\n" {
		t.Fatalf("explicit owner did not upgrade schema 21 to 22 with ambient legacy identity present: code=%d output=%q", code, output)
	}
	if output, code := runMainWithJSON(t, "native", "capacity", "slots", "upgrade", "--config", configPath); code != 3 {
		t.Fatalf("repeat schema 21 to 22 upgrade was not refused: code=%d output=%q", code, output)
	}
	_, message, code := executor(w.Root)([]string{"native", "capacity", "slots", "upgrade"}, false)
	if code != 3 || !strings.Contains(message, "local-only") {
		t.Fatalf("generic MCP did not refuse local-only schema upgrade: code=%d message=%q", code, message)
	}
}
