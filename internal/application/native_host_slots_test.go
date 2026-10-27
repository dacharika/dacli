package application

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/governance/nativeconfig"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

func TestNativeHostSlotsReaderRequiresSchema22AndExactConnection(t *testing.T) {
	w, err := workspace.Init(t.TempDir(), "host-slots-reader")
	if err != nil {
		t.Fatal(err)
	}
	private := t.TempDir()
	authorityPath := filepath.Join(private, "authority.json")
	a, err := sessions.Open(authorityPath, sessions.Binding{Workspace: w.ID, Installation: "host-slots-reader"})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := a.Bootstrap()
	if err != nil {
		t.Fatal(err)
	}
	for _, upgrade := range []func(string) error{a.UpgradeSwarms, a.UpgradeDispatchIntents, a.UpgradeLifecycle, a.UpgradeRecovery, a.UpgradePreflight, a.UpgradeCheckpoints, a.UpgradeBudgets, a.UpgradeNativeAcceptance, a.UpgradeNativePolicy, a.UpgradeController, a.UpgradeNativeModelRouting, a.UpgradeNativeIntegration, a.UpgradeNativeExternal, a.UpgradeNativeRoleRegistry, a.UpgradeHostCapacity} {
		if err := upgrade(owner); err != nil {
			t.Fatalf("fixture upgrade to schema 21: %v", err)
		}
	}
	controller, controllerToken, err := a.Enroll(owner, sessions.Scope{Actions: []string{"controller.read"}, Resources: []string{"workspace:" + w.ID, "project:core"}}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	credentialPath := filepath.Join(private, "controller.token")
	if err := os.WriteFile(credentialPath, []byte(controllerToken), 0600); err != nil {
		t.Fatal(err)
	}
	connectionPath := filepath.Join(private, "connection.json")
	connection := nativeconfig.Connection{SchemaVersion: 1, Workspace: w.Root, WorkspaceID: w.ID, Authority: authorityPath, InstallationID: "host-slots-reader", SessionID: controller.ID, CredentialFile: credentialPath}
	encoded, err := json.Marshal(connection)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(connectionPath, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenNativeHostSlots(connectionPath, w); err == nil {
		t.Fatal("schema-21 authority opened through schema-22 host-slot reader")
	}
	if err := a.UpgradeHostSlots(owner); err != nil {
		t.Fatalf("explicit fixture schema migration: %v", err)
	}
	reader, err := OpenNativeHostSlots(connectionPath, w)
	if err != nil {
		t.Fatalf("schema-22 reader refused exact connection: %v", err)
	}
	if _, err := reader.Read("core"); err == nil {
		t.Fatal("a merely enrolled controller.read session bypassed the authority controller record")
	}
	changed := connection
	changed.SessionID = "substituted-session"
	encoded, _ = json.Marshal(changed)
	if err := os.WriteFile(connectionPath, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Read("core"); err == nil {
		t.Fatal("mutated private connection remained authenticated")
	}
}
