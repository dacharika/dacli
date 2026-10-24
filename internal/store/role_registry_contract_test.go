package store

import (
	"crypto/ed25519"
	"os"
	"strings"
	"testing"

	"github.com/mlnomadpy/dacli/internal/rolebundle"
	"github.com/mlnomadpy/dacli/internal/team"
)

func TestRoleRegistryReadsV2AndRejectsUnknownContractSemantics(t *testing.T) {
	registry, _, private, _ := registryFixture(t)
	role := team.Role{Name: "builder", Summary: "build", Grant: "rw", Scope: []string{"src/**"}, Prompt: "inspect and build", Runtime: "codex", Model: "model-a"}
	contract := rolebundle.RoleContract{
		Purpose: role.Summary, Instructions: role.Prompt,
		CapabilityScope: rolebundle.CapabilityScope{Grant: role.Grant, Scope: role.Scope},
		Runtime:         rolebundle.RuntimePreference{Runtime: role.Runtime, Model: role.Model, Profile: role.Profile},
		Host:            rolebundle.HostRequirements{Sandbox: "cooperative", Network: "unrestricted", Filesystem: "role-scope", Process: "unrestricted"},
		Budget:          rolebundle.BudgetRequirement{Mode: "none"},
		Scenarios:       []rolebundle.Scenario{{ID: "read", RequestedActions: []string{"read"}, AllowedActions: []string{"read"}, BudgetState: "valid", Escalation: "none", ExpectedOutcome: "allowed"}},
	}
	release, err := rolebundle.Sign(rolebundle.ReleaseContent{SchemaVersion: rolebundle.ReleaseContractSchemaVersion, Version: "2.0.0", Role: role, Contract: &contract}, "publisher", private)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.StoreRelease(release); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.ReadRelease(role.Name, release.Content.Version, release.ContentDigest); err != nil {
		t.Fatalf("valid schema-2 release refused: %v", err)
	}
	path, err := registry.releasePath(role.Name, release.Content.Version, release.ContentDigest)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	changed := strings.Replace(string(data), `"purpose":"build"`, `"purpose":"build","unknown_required_gate":"mystery"`, 1)
	if changed == string(data) {
		t.Fatal("test fixture did not locate canonical contract field")
	}
	if err := os.WriteFile(path, []byte(changed), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.ReadRelease(role.Name, release.Content.Version, release.ContentDigest); err == nil {
		t.Fatal("release reader ignored an unknown contract semantic")
	}

	if len(release.Signature) != ed25519.SignatureSize {
		t.Fatal("fixture release signature malformed")
	}
}
