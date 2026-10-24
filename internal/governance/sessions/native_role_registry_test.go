package sessions

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestRolePolicyReferencesResolveExactCurrentPolicyAndRejectDrift(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	swarm := SwarmPolicy{ProjectID: "project", Revision: "v1", Harness: "codex", TTL: time.Hour, Width: 1, WIP: 1, Roles: []SwarmRole{{ID: "builder", Kind: "implementer", Harness: "codex", Capacity: 1}}, Placements: map[string]SwarmPlacement{}}
	preflight := PreflightPolicy{SchemaVersion: 1, ProjectID: "project", Revision: "v2", Harness: "codex", ExpiresAt: now.Add(time.Hour), Tasks: map[string]PreflightTask{}}
	state := &state{SwarmPolicies: map[string]SwarmPolicy{"project": swarm}, PreflightPolicies: map[string]PreflightPolicy{"project": preflight}}
	provenance := []RoleRegistryProvenance{{RoleID: "builder", PolicyReferences: []RolePolicyReference{
		{Kind: "native-swarm", Version: "v1", Digest: RolePolicyDigest("native-swarm", swarm)},
		{Kind: "native-preflight", Version: "v2", Digest: RolePolicyDigest("native-preflight", preflight)},
	}}}
	if err := resolveRolePolicyReferences(state, "project", provenance, now); err != nil {
		t.Fatalf("matching current policy refs refused: %v", err)
	}
	changed := *state
	changedPolicy := swarm
	changedPolicy.Revision = "v2"
	changed.SwarmPolicies = map[string]SwarmPolicy{"project": changedPolicy}
	if err := resolveRolePolicyReferences(&changed, "project", provenance, now); !errors.Is(err, ErrDenied) {
		t.Fatalf("stale exact policy revision accepted after drift: %v", err)
	}
	stopped := *state
	stoppedPolicy := swarm
	stoppedPolicy.Stopped = true
	stopped.SwarmPolicies = map[string]SwarmPolicy{"project": stoppedPolicy}
	if err := resolveRolePolicyReferences(&stopped, "project", provenance, now); !errors.Is(err, ErrDenied) {
		t.Fatalf("required stopped policy accepted: %v", err)
	}
	expired := *state
	expiredPolicy := preflight
	expiredPolicy.ExpiresAt = now
	expired.PreflightPolicies = map[string]PreflightPolicy{"project": expiredPolicy}
	if err := resolveRolePolicyReferences(&expired, "project", provenance, now); !errors.Is(err, ErrDenied) {
		t.Fatalf("expired required preflight policy accepted: %v", err)
	}
}

func TestNativeRoleRegistrySchemaUpgradePreservesHistoryAndOldReaderRefuses(t *testing.T) {
	a, owner, _ := authorityGrowthFixture(t, 0)
	const key = "retained-existing-history"
	if err := a.transaction(false, true, func(s *state) error {
		s.NativeExternalHistory[key] = NativeExternalPolicy{ProjectID: "project", Revision: "revision-v1"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := a.UpgradeNativeRoleRegistry(owner); err != nil {
		t.Fatalf("explicit schema-20 owner migration: %v", err)
	}
	opened, err := OpenCurrent(a.path, a.binding)
	if err != nil {
		t.Fatalf("new exact-version reader rejected schema 20: %v", err)
	}
	if err := opened.transaction(false, false, func(s *state) error {
		if s.Version != 20 || s.NativeExternalHistory[key].Revision != "revision-v1" {
			t.Fatalf("migration did not preserve schema history: version=%d history=%+v", s.Version, s.NativeExternalHistory[key])
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for name, open := range map[string]func() (*Authority, error){
		"swarm": func() (*Authority, error) { return OpenSwarm(a.path, a.binding, nil) },
		"review": func() (*Authority, error) {
			return OpenReviewAuthority(a.path, a.binding, &testReviewObserver{})
		},
		"dashboard": func() (*Authority, error) { return OpenDashboardAuthority(a.path, a.binding) },
	} {
		t.Run(name, func(t *testing.T) {
			current, err := open()
			if err != nil {
				t.Fatalf("schema-20 operational opener refused current state: %v", err)
			}
			if err := current.transaction(false, false, func(s *state) error {
				if s.Version != 20 {
					t.Fatalf("opened wrong schema: %d", s.Version)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
	raw, err := readAuthority(a.path, false)
	if err != nil {
		t.Fatal(err)
	}
	var oldReader state
	if json.Unmarshal(raw, &oldReader) != nil {
		t.Fatal("schema 20 state was not valid JSON")
	}
	if _, err := decodeAuthorityState(raw, a.binding, 6); !errors.Is(err, ErrStorage) {
		t.Fatalf("legacy bounded reader accepted schema 20: %v", err)
	}
	legacy, err := Open(a.path, a.binding)
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.transaction(false, false, func(*state) error { return nil }); !errors.Is(err, ErrStorage) {
		t.Fatalf("legacy bounded operation accepted schema 20: %v", err)
	}
	if _, err := Open(filepath.Join(filepath.Dir(a.path), "missing.json"), a.binding); err != nil {
		t.Fatalf("legacy Open should preserve absent authority creation behavior: %v", err)
	}
}
