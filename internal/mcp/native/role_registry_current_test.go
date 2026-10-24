package native

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/governance/sessions"
)

// A schema-20 migration is useful only if the actual selected MCP connection
// can reopen and use it. A sessions-only opener test misses this adapter.
func TestNativeConnectionUsesMigratedRoleRegistryAuthority(t *testing.T) {
	f := setup(t)
	for _, upgrade := range []struct {
		name string
		run  func(string) error
	}{
		{"swarm", f.a.UpgradeSwarms},
		{"dispatch", f.a.UpgradeDispatchIntents},
		{"lifecycle", f.a.UpgradeLifecycle},
		{"recovery", f.a.UpgradeRecovery},
		{"preflight", f.a.UpgradePreflight},
		{"checkpoints", f.a.UpgradeCheckpoints},
		{"budgets", f.a.UpgradeBudgets},
		{"acceptance", f.a.UpgradeNativeAcceptance},
		{"policy", f.a.UpgradeNativePolicy},
		{"controller", f.a.UpgradeController},
		{"model-routing", f.a.UpgradeNativeModelRouting},
		{"integration", f.a.UpgradeNativeIntegration},
		{"external", f.a.UpgradeNativeExternal},
	} {
		if err := upgrade.run(f.owner); err != nil {
			t.Fatalf("explicit %s migration: %v", upgrade.name, err)
		}
	}
	beforeBytes, err := os.ReadFile(f.c.Authority)
	if err != nil {
		t.Fatal(err)
	}
	var before map[string]json.RawMessage
	if err := json.Unmarshal(beforeBytes, &before); err != nil {
		t.Fatal(err)
	}
	if err := f.a.UpgradeNativeRoleRegistry(f.owner); err != nil {
		t.Fatal(err)
	}
	afterBytes, err := os.ReadFile(f.c.Authority)
	if err != nil {
		t.Fatal(err)
	}
	var after map[string]json.RawMessage
	if err := json.Unmarshal(afterBytes, &after); err != nil {
		t.Fatal(err)
	}
	delete(before, "Version")
	delete(after, "Version")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("role registry migration changed existing authority records")
	}
	legacy, err := sessions.Open(f.c.Authority, sessions.Binding{Workspace: f.c.WorkspaceID, Installation: f.c.InstallationID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Authenticate(f.token, f.c.SessionID); !errors.Is(err, sessions.ErrStorage) {
		t.Fatalf("legacy bounded reader accepted schema20: %v", err)
	}
	f.s, err = Open(f.s.path)
	if err != nil {
		t.Fatalf("selected MCP connection cannot reopen migrated authority: %v", err)
	}
	call(t, f, "identity", version{1})
	attempt := call(t, f, "register_attempt", registerArgs{version{1}, sessions.AttemptRequest{RequestID: "schema20-attempt", TaskID: f.task.ID, Checkout: f.c.Workspace}}).(sessions.Attempt)
	lease := call(t, f, "acquire_lease", acquireArgs{version{1}, sessions.AcquireLeaseRequest{OperationKey: "schema20-lease", AttemptID: attempt.ID, Resources: []sessions.Resource{{Kind: "path", Path: "src"}}, ExpiresAt: time.Now().Add(time.Minute)}}).(sessions.Lease)
	revision := call(t, f, "publish_revision", publishArgs{version{1}, sessions.PublishRevisionRequest{OperationKey: "schema20-revision", AttemptID: attempt.ID, LeaseID: lease.ID, ExpectedFence: lease.Fence}}).(sessions.AttemptRevision)
	if revision.SessionID != f.c.SessionID || revision.AttemptID != attempt.ID || revision.Fence != lease.Fence {
		t.Fatal("current MCP writer lost the original enrolled identity or fence")
	}
	call(t, f, "release_lease", releaseArgs{version{1}, sessions.ReleaseLeaseRequest{OperationKey: "schema20-release", AttemptID: attempt.ID, LeaseID: lease.ID, ExpectedFence: lease.Fence}})
}
