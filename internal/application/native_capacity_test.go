package application

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/governance/executor"
	"github.com/mlnomadpy/dacli/internal/governance/nativeconfig"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

func capacityApplicationFixture(t *testing.T) (*workspace.Workspace, string, string, *sessions.Authority, sessions.Session, string) {
	t.Helper()
	w, err := workspace.Init(t.TempDir(), "capacity-contract")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "authority.json")
	a, err := sessions.Open(path, sessions.Binding{Workspace: w.ID, Installation: "capacity-installation"})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := a.Bootstrap()
	if err != nil {
		t.Fatal(err)
	}
	for _, upgrade := range []func(string) error{a.UpgradeSwarms, a.UpgradeDispatchIntents, a.UpgradeLifecycle, a.UpgradeRecovery, a.UpgradePreflight, a.UpgradeCheckpoints, a.UpgradeBudgets, a.UpgradeNativeAcceptance, a.UpgradeNativePolicy, a.UpgradeController, a.UpgradeNativeModelRouting, a.UpgradeNativeIntegration, a.UpgradeNativeExternal, a.UpgradeNativeRoleRegistry} {
		if err := upgrade(owner); err != nil {
			t.Fatalf("fixture schema upgrade: %v", err)
		}
	}
	host, hostToken, err := a.Enroll(owner, sessions.Scope{Actions: []string{"host.observe"}, Resources: []string{"workspace:" + w.ID}}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return w, path, owner, a, host, hostToken
}

func writeCapacityOwnerProfile(t *testing.T, w *workspace.Workspace, authority, owner string) string {
	t.Helper()
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "owner.token")
	configPath := filepath.Join(dir, "owner.json")
	if err := os.WriteFile(tokenPath, []byte(owner), 0600); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(recoveryOwnerConfig{SchemaVersion: 1, Workspace: w.Root, Authority: authority, InstallationID: "capacity-installation", OwnerCredentialFile: tokenPath})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(configPath, b, 0600); err != nil {
		t.Fatal(err)
	}
	return configPath
}

func TestNativeCapacityOwnerMigrationAndAuthenticatedAdapter(t *testing.T) {
	w, path, ownerToken, _, host, _ := capacityApplicationFixture(t)
	profile := writeCapacityOwnerProfile(t, w, path, ownerToken)
	owner, err := OpenNativeCapacityOwner(profile)
	if err != nil {
		t.Fatal(err)
	}
	if err = owner.Upgrade(); err != nil {
		t.Fatalf("explicit owner schema 20→21 migration: %v", err)
	}
	observer, observerToken, err := owner.EnrollObserver(sessions.HostCapacityObserver{HostSessionID: host.ID, HostID: "host-a", Runtime: "codex", Build: "build-1", CapabilityDigest: strings.Repeat("a", 64), ExpiresAt: time.Now().Add(30 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = owner.Configure(sessions.HostCapacityPolicy{WorkspaceID: w.ID, InstallationID: "capacity-installation", HostID: "host-a", Runtime: "codex", Build: "build-1", HostSessionID: host.ID, CapabilityDigest: strings.Repeat("a", 64), State: sessions.CapacityKnown, RequestedSlots: 1, Freshness: time.Minute, Revision: "requested-v1"}); err != nil {
		t.Fatal(err)
	}
	if _, err = owner.Qualify(sessions.HostCapacityQualification{HostSessionID: host.ID, HostID: "host-a", Runtime: "codex", Build: "build-1", CapabilityDigest: strings.Repeat("a", 64), State: sessions.CapacityKnown, QualifiedSlots: 1, EvidenceDigest: strings.Repeat("b", 64), QualificationID: "qualified-v1", ExpiresAt: time.Now().Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	credential := filepath.Join(dir, "observer.token")
	connection := filepath.Join(dir, "connection.json")
	if err = os.WriteFile(credential, []byte(observerToken), 0600); err != nil {
		t.Fatal(err)
	}
	c := nativeconfig.Connection{SchemaVersion: 1, Workspace: w.Root, WorkspaceID: w.ID, Authority: path, InstallationID: "capacity-installation", SessionID: observer.ObserverSessionID, CredentialFile: credential}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(connection, b, 0600); err != nil {
		t.Fatal(err)
	}
	check, err := sessions.OpenHostCapacity(path, sessions.Binding{Workspace: w.ID, Installation: "capacity-installation"})
	if err != nil {
		t.Fatalf("direct capacity reader open: %v", err)
	}
	checkToken, err := nativeconfig.ReadCredential(credential)
	if err != nil {
		t.Fatalf("observer token read: %v", err)
	}
	if _, err = check.Authenticate(checkToken, observer.ObserverSessionID); err != nil {
		t.Fatalf("observer authentication: %v", err)
	}
	access, err := OpenNativeCapacity(connection, w)
	if err != nil {
		t.Fatal(err)
	}
	report := executor.CapacityReport{SchemaVersion: 1, OperationKey: "observation-1", HostID: "host-a", Runtime: "codex", Build: "build-1", HostSessionID: host.ID, CapabilityDigest: strings.Repeat("a", 64), Sequence: 1, DeclarationState: sessions.CapacityKnown, DeclaredSlots: 2, ObservationState: sessions.CapacityKnown, OccupiedSlots: 1, AvailableSlots: 1, ObservedAt: time.Now().Add(-time.Second)}
	if _, duplicate, err := access.Observe(report); err != nil || duplicate {
		t.Fatalf("authenticated observation refused: duplicate=%v err=%v", duplicate, err)
	}
	facts, err := access.Read(host.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !executor.PositiveCapacityEvidence(facts, time.Now()) {
		t.Fatalf("expected four-fact fresh evidence: %+v", facts)
	}
	// Revocation of either bound session invalidates a later read.
	current, err := sessions.OpenCurrent(path, sessions.Binding{Workspace: w.ID, Installation: "capacity-installation"})
	if err != nil {
		t.Fatal(err)
	}
	if err = current.Revoke(ownerToken, observer.ObserverSessionID); err != nil {
		t.Fatal(err)
	}
	if _, err = access.Read(host.ID); err == nil {
		t.Fatal("revoked observer credential remained usable")
	}
}

func TestNativeCapacityRequiresSchema21AndExactConnection(t *testing.T) {
	w, path, owner, a, host, _ := capacityApplicationFixture(t)
	dir := t.TempDir()
	token := filepath.Join(dir, "observer.token")
	connection := filepath.Join(dir, "connection.json")
	// A normal schema-20 executor connection is refused by the capacity reader.
	if err := os.WriteFile(token, []byte(owner), 0600); err != nil {
		t.Fatal(err)
	}
	c := nativeconfig.Connection{SchemaVersion: 1, Workspace: w.Root, WorkspaceID: w.ID, Authority: path, InstallationID: "capacity-installation", SessionID: host.ID, CredentialFile: token}
	b, _ := json.Marshal(c)
	if err := os.WriteFile(connection, b, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenNativeCapacity(connection, w); err == nil {
		t.Fatal("capacity reader opened schema 20 before explicit upgrade")
	}
	if err := a.UpgradeHostCapacity(owner); err != nil {
		t.Fatal(err)
	}
	// Schema 21 alone does not grant an unrelated host session observer scope.
	if _, err := OpenNativeCapacity(connection, w); err == nil {
		t.Fatal("un-enrolled host session opened as observer")
	}
}
