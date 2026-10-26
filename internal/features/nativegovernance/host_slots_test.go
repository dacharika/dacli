package nativegovernance

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

func TestHostSlotCommandsDeclareExplicitOwnerMutation(t *testing.T) {
	if len(HostSlotCommands) != 1 {
		t.Fatalf("host slot command count=%d want=1", len(HostSlotCommands))
	}
	command := HostSlotCommands[0]
	if command.Path != "native capacity slots upgrade" || !command.Mutates || !command.JSON || command.Usage != "dacli native capacity slots upgrade --config <private-owner-file> --json" {
		t.Fatalf("host slot command contract drifted: %+v", command)
	}
}

func TestHostSlotUpgradeRequiresExplicitOwnerAndOnlyMigrates21To22(t *testing.T) {
	w, err := workspace.Init(t.TempDir(), "host-slot-upgrade")
	if err != nil {
		t.Fatal(err)
	}
	private := t.TempDir()
	path := filepath.Join(private, "authority.json")
	a, err := sessions.Open(path, sessions.Binding{Workspace: w.ID, Installation: "host-slot-upgrade"})
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
	encoded, err := json.Marshal(ownerConfig{SchemaVersion: 1, Workspace: w.Root, Authority: path, InstallationID: "host-slot-upgrade", OwnerCredentialFile: ownerCredential})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	ctx := &clikit.Ctx{Cwd: w.Root, Stdout: &output, Stderr: &output, JSON: true}
	if err := hostSlotsUpgrade(ctx, nil); clikit.ExitCode(err) != 2 {
		t.Fatalf("missing explicit owner config exit=%d err=%v", clikit.ExitCode(err), err)
	}
	badCredential := filepath.Join(private, "bad.token")
	if err := os.WriteFile(badCredential, []byte("worker-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	badConfig := filepath.Join(private, "worker-owner.json")
	encoded, _ = json.Marshal(ownerConfig{SchemaVersion: 1, Workspace: w.Root, Authority: path, InstallationID: "host-slot-upgrade", OwnerCredentialFile: badCredential})
	if err := os.WriteFile(badConfig, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := hostSlotsUpgrade(ctx, []string{"--config", badConfig}); clikit.ExitCode(err) != 3 {
		t.Fatalf("worker credential was not refused with policy exit 3: exit=%d err=%v", clikit.ExitCode(err), err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("refused worker config changed authority: err=%v", err)
	}
	output.Reset()
	if err := hostSlotsUpgrade(ctx, []string{"--config", configPath}); err != nil {
		t.Fatalf("explicit owner failed schema-21 to 22 upgrade: %v", err)
	}
	if output.String() != `{"schema_version":1,"authority_schema":22,"upgraded":true}`+"\n" {
		t.Fatalf("upgrade output=%q", output.String())
	}
	if _, err := sessions.OpenHostSlots(path, sessions.Binding{Workspace: w.ID, Installation: "host-slot-upgrade"}); err != nil {
		t.Fatalf("schema-22 authority did not reopen via exact slot opener: %v", err)
	}
	if err := hostSlotsUpgrade(ctx, []string{"--config", configPath}); clikit.ExitCode(err) != 3 {
		t.Fatalf("repeat 21-to-22 upgrade was not refused: exit=%d err=%v", clikit.ExitCode(err), err)
	}
}
