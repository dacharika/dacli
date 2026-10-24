package application

import (
	"errors"
	"os"
	"testing"

	"github.com/mlnomadpy/dacli/internal/governance/sessions"
)

func TestNativeRoleRegistryOutcomesRequiresCurrentExplicitOwner(t *testing.T) {
	f := newRoleRegistryOwnerFixture(t)
	authority, err := sessions.OpenCurrent(f.authorityPath, sessions.Binding{Workspace: f.w.ID, Installation: "role-owner-installation"})
	if err != nil {
		t.Fatal(err)
	}
	for _, upgrade := range []func(string) error{authority.UpgradeSwarms, authority.UpgradeDispatchIntents, authority.UpgradeLifecycle} {
		if err := upgrade(f.owner); err != nil {
			t.Fatal(err)
		}
	}
	owner, err := OpenNativeRoleRegistryOwner(f.config, "core")
	if err != nil {
		t.Fatal(err)
	}
	out, err := owner.Outcomes()
	if err != nil || len(out) != 0 {
		t.Fatalf("empty project outcomes: %+v %v", out, err)
	}
	if err := os.WriteFile(f.config, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Outcomes(); !errors.Is(err, sessions.ErrDenied) {
		t.Fatalf("outcomes accepted a replaced owner config: %v", err)
	}
}
