package application

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/mdstore"
	"github.com/mlnomadpy/dacli/internal/rolebundle"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/team"
)

func TestRootUnpinnedNativeSwarmPreservesIndependentRoleFileBehavior(t *testing.T) {
	w, _ := readFixture(t)
	if _, err := store.CreateProject(w, "owner", "Core", "core", "unconfigured role registry", ""); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(w.RolesDir(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(w.RolePath("unrelated"), []byte("---\nkind: unrelated-document\n---\n# Existing unrelated file\n"), 0600); err != nil {
		t.Fatal(err)
	}
	admission, err := NewLeaseAdmission(w)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admission.ObserveSwarm(w.ID, "core"); err != nil {
		t.Fatalf("unpinned project acquired new role-parser dependency: %v", err)
	}
}

func TestNativeSwarmEnforcesExactPinnedRoleWithoutPreflightRouting(t *testing.T) {
	w, _ := readFixture(t)
	if _, err := store.CreateProject(w, "owner", "Core", "core", "role registry", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateRole(w, "owner", team.Role{Name: "builder", Summary: "original", Grant: "rw", Scope: []string{"src"}}); err != nil {
		t.Fatal(err)
	}
	roles, err := store.ReadCheckpointRoles(w)
	if err != nil || len(roles) != 1 {
		t.Fatalf("canonical role read: %v roles=%d", err, len(roles))
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	registryDir := filepath.Join(w.Root, ".dacli", "role-registry")
	if err := os.MkdirAll(registryDir, 0700); err != nil {
		t.Fatal(err)
	}
	keyring, err := json.Marshal(map[string][]byte{"test-publisher": public})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(registryDir, "publishers.json"), keyring, 0600); err != nil {
		t.Fatal(err)
	}
	registry, err := store.NewRoleRegistry(w, map[string]ed25519.PublicKey{"test-publisher": public})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	release, err := rolebundle.Sign(rolebundle.ReleaseContent{SchemaVersion: 1, Version: "1.2.3", Role: roles[0]}, "test-publisher", private)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.StoreRelease(release); err != nil {
		t.Fatal(err)
	}
	status, err := rolebundle.SignStatus(rolebundle.LifecycleStatus{SchemaVersion: 1, RoleID: roles[0].Name, Version: release.Content.Version, ContentDigest: release.ContentDigest, Generation: 1, State: "active", IssuedAt: now, ExpiresAt: now.Add(time.Hour), PublisherKeyID: "test-publisher"}, private)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.StoreLifecycle(status, now); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Pin("core", roles[0].Name, release.Content.Version, release.ContentDigest, "owner", "approve exact signed role", now); err != nil {
		t.Fatal(err)
	}
	admission, err := NewLeaseAdmission(w)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := admission.ObserveSwarm(w.ID, "core")
	if err != nil || len(snapshot.RoleRegistry) != 1 || snapshot.RoleRegistry[0].ContentDigest != release.ContentDigest {
		t.Fatalf("matching exact role pin did not pass native swarm admission: %+v %v", snapshot.RoleRegistry, err)
	}
	rolePath := w.RolePath("builder")
	doc, err := mdstore.ReadFile(rolePath)
	if err != nil {
		t.Fatal(err)
	}
	doc.Front.Set("summary", "drifted local role")
	if err := mdstore.WriteFile(rolePath, doc); err != nil {
		t.Fatal(err)
	}
	if _, err := admission.ObserveSwarm(w.ID, "core"); err == nil {
		t.Fatal("native swarm admitted after canonical pinned role drift")
	}
}

func TestSafeRoleReferenceIDUsesSegmentAndControlCharacterRules(t *testing.T) {
	for _, id := range []string{"lint", "review", "build-go"} {
		if !safeRoleReferenceID(id) {
			t.Errorf("ordinary skill ID rejected: %q", id)
		}
	}
	for _, id := range []string{"", "../escape", "two/segments", "nul\x00", "line\nfeed"} {
		if safeRoleReferenceID(id) {
			t.Errorf("unsafe skill ID accepted: %q", id)
		}
	}
}
