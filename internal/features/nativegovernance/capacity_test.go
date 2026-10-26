package nativegovernance

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

func TestCapacityCommandsDeclareMutationAndJSONCapabilities(t *testing.T) {
	want := map[string]bool{
		"native capacity upgrade":          true,
		"native capacity observer add":     true,
		"native capacity policy configure": true,
		"native capacity qualify":          true,
		"native capacity observe":          true,
		"native capacity status":           false,
	}
	seen := map[string]bool{}
	for _, command := range CapacityCommands {
		mutates, ok := want[command.Path]
		if !ok {
			t.Fatalf("unexpected capacity command %q", command.Path)
		}
		if seen[command.Path] {
			t.Fatalf("duplicate capacity command %q", command.Path)
		}
		seen[command.Path] = true
		if command.Mutates != mutates || !command.JSON {
			t.Fatalf("%s capabilities: mutates=%v JSON=%v", command.Path, command.Mutates, command.JSON)
		}
	}
	if len(seen) != len(want) {
		t.Fatalf("capacity command count=%d want=%d", len(seen), len(want))
	}
}

func TestCapacityCLIRejectsFutureOwnerContractVersions(t *testing.T) {
	w, err := workspace.Init(t.TempDir(), "capacity-cli-contract")
	if err != nil {
		t.Fatal(err)
	}
	private := t.TempDir()
	authorityPath := filepath.Join(private, "authority.json")
	ownerCredential := filepath.Join(private, "owner.token")
	configPath := filepath.Join(private, "owner.json")
	a, err := sessions.Open(authorityPath, sessions.Binding{Workspace: w.ID, Installation: "capacity-cli"})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := a.Bootstrap()
	if err != nil {
		t.Fatal(err)
	}
	for _, upgrade := range []func(string) error{a.UpgradeSwarms, a.UpgradeDispatchIntents, a.UpgradeLifecycle, a.UpgradeRecovery, a.UpgradePreflight, a.UpgradeCheckpoints, a.UpgradeBudgets, a.UpgradeNativeAcceptance, a.UpgradeNativePolicy, a.UpgradeController, a.UpgradeNativeModelRouting, a.UpgradeNativeIntegration, a.UpgradeNativeExternal, a.UpgradeNativeRoleRegistry} {
		if err := upgrade(owner); err != nil {
			t.Fatalf("fixture upgrade to schema 20: %v", err)
		}
	}
	if err := a.UpgradeHostCapacity(owner); err != nil {
		t.Fatalf("fixture upgrade to schema 21: %v", err)
	}
	host, _, err := a.Enroll(owner, sessions.Scope{Actions: []string{"host.observe"}, Resources: []string{"workspace:" + w.ID}}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.EnrollHostCapacityObserver(owner, sessions.HostCapacityObserver{SchemaVersion: 1, HostSessionID: host.ID, ExecutorInstanceID: "executor-1", HostID: "host-a", Runtime: "codex", Build: "build-1", CapabilityDigest: strings.Repeat("a", 64), ExpiresAt: time.Now().Add(30 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ConfigureHostCapacityPolicy(owner, sessions.HostCapacityPolicy{SchemaVersion: 1, WorkspaceID: w.ID, InstallationID: "capacity-cli", HostSessionID: host.ID, ExecutorInstanceID: "executor-1", HostID: "host-a", Runtime: "codex", Build: "build-1", CapabilityDigest: strings.Repeat("a", 64), State: sessions.CapacityKnown, RequestedSlots: 1, Freshness: time.Minute, Revision: "cli-policy-v1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.QualifyHostCapacity(owner, sessions.HostCapacityQualification{SchemaVersion: 1, HostSessionID: host.ID, ExecutorInstanceID: "executor-1", HostID: "host-a", Runtime: "codex", Build: "build-1", CapabilityDigest: strings.Repeat("a", 64), State: sessions.CapacityKnown, QualifiedSlots: 1, EvidenceDigest: strings.Repeat("b", 64), QualificationID: "cli-qualification-v1", ExpiresAt: time.Now().Add(20 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ownerCredential, []byte(owner+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	configData, _ := json.Marshal(ownerConfig{SchemaVersion: 1, Workspace: w.Root, Authority: authorityPath, InstallationID: "capacity-cli", OwnerCredentialFile: ownerCredential})
	if err := os.WriteFile(configPath, configData, 0600); err != nil {
		t.Fatal(err)
	}
	requestPath := filepath.Join(private, "future-request.json")
	before, err := os.ReadFile(authorityPath)
	if err != nil {
		t.Fatal(err)
	}
	run := func(name string, handler func(*clikit.Ctx, []string) error, args ...string) {
		t.Helper()
		var output bytes.Buffer
		ctx := &clikit.Ctx{Cwd: w.Root, Stdout: &output, Stderr: &output, JSON: true}
		if err := handler(ctx, args); err == nil {
			t.Fatalf("%s CLI accepted invalid owner contract version; output=%q", name, output.String())
		}
	}
	observer := sessions.HostCapacityObserver{SchemaVersion: 1, HostSessionID: host.ID, ExecutorInstanceID: "executor-1", HostID: "host-a", Runtime: "codex", Build: "build-1", CapabilityDigest: strings.Repeat("a", 64), ExpiresAt: time.Now().Add(10 * time.Minute)}
	policy := sessions.HostCapacityPolicy{SchemaVersion: 1, WorkspaceID: w.ID, InstallationID: "capacity-cli", HostSessionID: host.ID, ExecutorInstanceID: "executor-1", HostID: "host-a", Runtime: "codex", Build: "build-1", CapabilityDigest: strings.Repeat("a", 64), State: sessions.CapacityKnown, RequestedSlots: 1, Freshness: time.Minute, Revision: "cli-invalid-policy"}
	qualification := sessions.HostCapacityQualification{SchemaVersion: 1, HostSessionID: host.ID, ExecutorInstanceID: "executor-1", HostID: "host-a", Runtime: "codex", Build: "build-1", CapabilityDigest: strings.Repeat("a", 64), State: sessions.CapacityKnown, QualifiedSlots: 1, EvidenceDigest: strings.Repeat("c", 64), QualificationID: "cli-invalid-qualification", ExpiresAt: time.Now().Add(10 * time.Minute)}
	for _, contract := range []struct {
		name    string
		version *int
	}{{"version-zero", func() *int { v := 0; return &v }()}, {"future-version", func() *int { v := 2; return &v }()}, {"unversioned", nil}} {
		for _, request := range []struct {
			name    string
			value   any
			handler func(*clikit.Ctx, []string) error
		}{{"observer", observer, capacityObserverAdd}, {"policy", policy, capacityPolicyConfigure}, {"qualification", qualification, capacityQualify}} {
			data, err := json.Marshal(request.value)
			if err != nil {
				t.Fatal(err)
			}
			var payload map[string]json.RawMessage
			if err := json.Unmarshal(data, &payload); err != nil {
				t.Fatal(err)
			}
			if contract.version == nil {
				delete(payload, "schema_version")
			} else {
				payload["schema_version"], _ = json.Marshal(*contract.version)
			}
			data, _ = json.Marshal(payload)
			if err := os.WriteFile(requestPath, data, 0600); err != nil {
				t.Fatal(err)
			}
			if request.name == "observer" {
				credential := filepath.Join(private, "observer-"+contract.name+".token")
				run(request.name+"-"+contract.name, request.handler, "--config", configPath, "--request", requestPath, "--credential", credential)
				if _, err := os.Stat(credential); !os.IsNotExist(err) {
					t.Fatalf("invalid observer contract created credential output: err=%v", err)
				}
			} else {
				run(request.name+"-"+contract.name, request.handler, "--config", configPath, "--request", requestPath)
			}
		}
	}
	after, err := os.ReadFile(authorityPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("future-version CLI requests changed authority: err=%v", err)
	}
}

func TestCapacityReportVersionPeekRoutesOnlyKnownVersions(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  int
		ok    bool
	}{{`{"schema_version":1,"ignored":"for strict decoder"}`, 1, true}, {`{"schema_version":2}`, 2, true}, {`{"schema_version":3}`, 3, true}, {`{}`, 0, false}, {`{"schema_version":null}`, 0, false}, {`{"schema_version":"2"}`, 0, false}, {`{"schema_version":2}{}`, 0, false}, {`{"schema_version":2,"schema_version":1}`, 0, false}, {`{"schema_version":1,"schema_version":1}`, 0, false}} {
		got, err := capacityReportVersion([]byte(tc.input))
		if (err == nil) != tc.ok || (tc.ok && got != tc.want) {
			t.Fatalf("capacityReportVersion(%s)=(%d,%v), want (%d, ok=%v)", tc.input, got, err, tc.want, tc.ok)
		}
	}
}
