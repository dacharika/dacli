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
	observer, observerToken, err := owner.EnrollObserver(sessions.HostCapacityObserver{SchemaVersion: 1, HostSessionID: host.ID, ExecutorInstanceID: "executor-1", HostID: "host-a", Runtime: "codex", Build: "build-1", CapabilityDigest: strings.Repeat("a", 64), ExpiresAt: time.Now().Add(30 * time.Minute)})
	if err != nil {
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
	missing, err := access.Read(host.ID)
	if err != nil || missing.RequestedState != sessions.CapacityUnknown || executor.PositiveCapacityEvidence(missing, time.Now()) {
		t.Fatalf("missing requested policy did not project typed unknown: facts=%+v err=%v", missing, err)
	}
	encoded, err := json.Marshal(missing)
	if err != nil || !strings.Contains(string(encoded), `"requested_state":"unknown"`) {
		t.Fatalf("application JSON omitted typed unknown policy: %s err=%v", encoded, err)
	}
	if _, err = owner.Configure(sessions.HostCapacityPolicy{SchemaVersion: 1, WorkspaceID: w.ID, InstallationID: "capacity-installation", HostID: "host-a", Runtime: "codex", Build: "build-1", HostSessionID: host.ID, ExecutorInstanceID: "executor-1", CapabilityDigest: strings.Repeat("a", 64), State: sessions.CapacityKnown, RequestedSlots: 1, Freshness: time.Minute, Revision: "requested-v1"}); err != nil {
		t.Fatal(err)
	}
	if _, err = owner.Qualify(sessions.HostCapacityQualification{SchemaVersion: 1, HostSessionID: host.ID, ExecutorInstanceID: "executor-1", HostID: "host-a", Runtime: "codex", Build: "build-1", CapabilityDigest: strings.Repeat("a", 64), State: sessions.CapacityKnown, QualifiedSlots: 1, EvidenceDigest: strings.Repeat("b", 64), QualificationID: "qualified-v1", ExpiresAt: time.Now().Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	report := executor.CapacityReport{SchemaVersion: 1, OperationKey: "observation-1", HostID: "host-a", Runtime: "codex", Build: "build-1", HostSessionID: host.ID, ExecutorInstanceID: "executor-1", CapabilityDigest: strings.Repeat("a", 64), Sequence: 1, DeclarationState: sessions.CapacityKnown, DeclaredSlots: 2, ObservationState: sessions.CapacityKnown, OccupiedSlots: 1, AvailableSlots: 1, ObservedAt: time.Now().Add(-time.Second)}
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

func TestNativeCapacityCurrentReaderAndOwnerUpgradeSlots(t *testing.T) {
	w, path, ownerToken, _, host, _ := capacityApplicationFixture(t)
	profile := writeCapacityOwnerProfile(t, w, path, ownerToken)
	owner, err := OpenNativeCapacityOwner(profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.Upgrade(); err != nil {
		t.Fatalf("explicit schema-20 to 21 migration: %v", err)
	}
	binding := sessions.Binding{Workspace: w.ID, Installation: "capacity-installation"}
	if _, err := sessions.OpenCapacityCurrent(path, binding); err != nil {
		t.Fatalf("current capacity reader did not select schema 21: %v", err)
	}
	if err := owner.UpgradeSlots(); err != nil {
		t.Fatalf("explicit schema-21 to 22 migration: %v", err)
	}
	if _, err := sessions.OpenHostCapacity(path, binding); err == nil {
		t.Fatal("legacy exact-21 opener silently selected schema 22")
	}
	if _, err := sessions.OpenHostSlots(path, binding); err != nil {
		t.Fatalf("schema-22 slot opener refused migrated authority: %v", err)
	}
	observer, observerToken, err := owner.EnrollObserver(sessions.HostCapacityObserver{SchemaVersion: 1, HostSessionID: host.ID, ExecutorInstanceID: "executor-2", HostID: "host-a", Runtime: "codex", Build: "build-1", CapabilityDigest: strings.Repeat("a", 64), ExpiresAt: time.Now().Add(30 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	private := t.TempDir()
	credential := filepath.Join(private, "observer.token")
	connectionPath := filepath.Join(private, "connection.json")
	if err := os.WriteFile(credential, []byte(observerToken), 0600); err != nil {
		t.Fatal(err)
	}
	connection := nativeconfig.Connection{SchemaVersion: 1, Workspace: w.Root, WorkspaceID: w.ID, Authority: path, InstallationID: "capacity-installation", SessionID: observer.ObserverSessionID, CredentialFile: credential}
	encoded, err := json.Marshal(connection)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(connectionPath, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	access, err := OpenNativeCapacity(connectionPath, w)
	if err != nil {
		t.Fatalf("authenticated current capacity reader refused schema 22: %v", err)
	}
	capacityPolicy := sessions.HostCapacityPolicy{SchemaVersion: 1, WorkspaceID: w.ID, InstallationID: "capacity-installation", HostID: observer.HostID, Runtime: observer.Runtime, Build: observer.Build, HostSessionID: observer.HostSessionID, ExecutorInstanceID: observer.ExecutorInstanceID, CapabilityDigest: observer.CapabilityDigest, State: sessions.CapacityKnown, RequestedSlots: 1, Freshness: 10 * time.Minute, Revision: "capacity-v2-app"}
	terminalExpires := time.Now().Add(15 * time.Minute)
	slotPolicy := sessions.HostSlotAdmissionPolicy{SchemaVersion: 1, Mode: sessions.HostSlotSingleAuthority, Revision: "host-slots-v2-app", AuthorityDomainID: "domain-app", WorkspaceID: w.ID, InstallationID: "capacity-installation", HostID: observer.HostID, HostSessionID: observer.HostSessionID, ExecutorInstanceID: observer.ExecutorInstanceID, Runtime: observer.Runtime, Build: observer.Build, CapabilityDigest: observer.CapabilityDigest, CapacityPolicyRevision: capacityPolicy.Revision, QualificationID: "qualified-v2-app", TerminalObservationSource: "owner-bridge-terminal", TerminalQualificationID: "terminal-v2-app", TerminalQualification: &sessions.HostSlotTerminalQualification{SchemaVersion: 1, ID: "terminal-v2-app", State: "supported", Source: "owner-bridge-terminal", EvidenceDigest: strings.Repeat("b", 64), AuthorityDomainID: "domain-app", HostID: observer.HostID, HostSessionID: observer.HostSessionID, ExecutorInstanceID: observer.ExecutorInstanceID, Runtime: observer.Runtime, Build: observer.Build, CapabilityDigest: observer.CapabilityDigest, QualifiedAt: time.Now().Add(-time.Second), ExpiresAt: terminalExpires}}
	ownerAuthority, err := sessions.OpenHostSlots(path, binding)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ownerAuthority.ConfigureHostCapacityPolicy(ownerToken, capacityPolicy); err != nil {
		t.Fatalf("configure schema-22 capacity policy for application observation: %v", err)
	}
	if _, err = ownerAuthority.QualifyHostCapacity(ownerToken, sessions.HostCapacityQualification{SchemaVersion: 1, HostSessionID: observer.HostSessionID, ExecutorInstanceID: observer.ExecutorInstanceID, HostID: observer.HostID, Runtime: observer.Runtime, Build: observer.Build, CapabilityDigest: observer.CapabilityDigest, State: sessions.CapacityKnown, QualifiedSlots: 2, EvidenceDigest: strings.Repeat("c", 64), QualificationID: "qualified-v2-app", ExpiresAt: time.Now().Add(14 * time.Minute)}); err != nil {
		t.Fatalf("qualify schema-22 capacity for application observation: %v", err)
	}
	if err = ownerAuthority.BindTask(ownerToken, sessions.TaskBinding{TaskID: "task-a", ProjectID: "capacity-project", Repository: w.Root, AdmissionEnabled: true}); err != nil {
		t.Fatalf("bind owner task for schema-22 host-slot policy: %v", err)
	}
	swarmPolicy := sessions.SwarmPolicy{ProjectID: "capacity-project", Revision: "swarm-v2-app", Harness: "codex", CapabilityDigest: "capacity-capability", Width: 1, WIP: 1, TTL: 10 * time.Minute, Roles: []sessions.SwarmRole{{ID: "implement", Kind: "implement", Harness: "codex", Capacity: 1}, {ID: "review", Kind: "review", Harness: "codex", Capacity: 1}}, Placements: map[string]sessions.SwarmPlacement{"task-a": {Checkout: w.Root, ImplementKind: "implement", ReviewKind: "review"}}, HostSlots: &slotPolicy}
	if err = ownerAuthority.ConfigureSwarm(ownerToken, swarmPolicy); err != nil {
		t.Fatalf("configure owner host-slot policy for application observation: %v", err)
	}
	report := executor.CapacityReportV2{SchemaVersion: 2, OperationKey: "capacity-v2-through-app", HostID: observer.HostID, Runtime: observer.Runtime, Build: observer.Build, HostSessionID: observer.HostSessionID, ExecutorInstanceID: observer.ExecutorInstanceID, CapabilityDigest: observer.CapabilityDigest, Sequence: 1, DeclarationState: sessions.CapacityKnown, DeclaredSlots: 2, ObservationState: sessions.CapacityKnown, OccupiedSlots: 0, AvailableSlots: 2, ObservedAt: time.Now().Add(-time.Second).UTC(), Census: &executor.HostCapacityCensusV2{SchemaVersion: 1, AuthorityDomainID: "domain-app", IncludedLocalExecutions: []executor.HostCapacityObservationBinding{}}}
	if _, duplicate, err := access.ObserveV2(report); err != nil || duplicate {
		t.Fatalf("schema-2 observation through authenticated application adapter failed: duplicate=%v err=%v", duplicate, err)
	}
	if _, duplicate, err := access.ObserveV2(report); err != nil || !duplicate {
		t.Fatalf("schema-2 observation receipt was not persisted for exact replay: duplicate=%v err=%v", duplicate, err)
	}
}
