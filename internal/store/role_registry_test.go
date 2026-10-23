package store

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/rolebundle"
	"github.com/mlnomadpy/dacli/internal/team"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

func registryFixture(t *testing.T) (*RoleRegistry, *workspace.Workspace, ed25519.PrivateKey, time.Time) {
	t.Helper()
	w, err := workspace.Init(t.TempDir(), "roles")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = CreateProject(w, "a-root", "Core", "core", "role pins", ""); err != nil {
		t.Fatal(err)
	}
	pub, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewRoleRegistry(w, map[string]ed25519.PublicKey{"publisher": pub})
	if err != nil {
		t.Fatal(err)
	}
	return registry, w, private, time.Now().UTC().Truncate(time.Second)
}

func registryRelease(t *testing.T, private ed25519.PrivateKey, version, prompt string) rolebundle.Release {
	t.Helper()
	release, err := rolebundle.Sign(rolebundle.ReleaseContent{SchemaVersion: 1, Version: version, Role: team.Role{Name: "builder", Summary: "build", Grant: "rw", Scope: []string{"internal/**"}, Prompt: prompt}}, "publisher", private)
	if err != nil {
		t.Fatal(err)
	}
	return release
}

func registryStatus(t *testing.T, private ed25519.PrivateKey, release rolebundle.Release, generation uint64, previous string, state string, now time.Time) rolebundle.LifecycleStatus {
	t.Helper()
	status, err := rolebundle.SignStatus(rolebundle.LifecycleStatus{SchemaVersion: 1, RoleID: "builder", Version: release.Content.Version, ContentDigest: release.ContentDigest, Generation: generation, PreviousStatusDigest: previous, State: state, IssuedAt: now, ExpiresAt: now.Add(time.Hour), PublisherKeyID: "publisher"}, private)
	if err != nil {
		t.Fatal(err)
	}
	return status
}

func storeStatus(t *testing.T, registry *RoleRegistry, status rolebundle.LifecycleStatus, now time.Time) string {
	t.Helper()
	if err := registry.StoreLifecycle(status, now); err != nil {
		t.Fatal(err)
	}
	digest, err := rolebundle.LifecycleDigest(status)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func TestRoleRegistryCachesExactReleaseAndMonotonicPins(t *testing.T) {
	registry, w, private, now := registryFixture(t)
	v1 := registryRelease(t, private, "1.0.0", "build carefully")
	if err := registry.StoreRelease(v1); err != nil {
		t.Fatal(err)
	}
	if err := registry.StoreRelease(v1); err != nil {
		t.Fatalf("exact cached import is not idempotent: %v", err)
	}
	first := registryStatus(t, private, v1, 1, "", "active", now)
	previous := storeStatus(t, registry, first, now)
	pins, err := registry.Pin("core", "builder", "1.0.0", v1.ContentDigest, "a-root", "initial owner pin", now)
	if err != nil {
		t.Fatal(err)
	}
	if pins.Generation != 1 || pins.Pins["builder"].Generation != 1 {
		t.Fatalf("first pin generation mismatch: %+v", pins)
	}
	resolved, pin, status, err := registry.Resolve("core", "builder", now.Add(time.Minute))
	if err != nil || resolved.ContentDigest != v1.ContentDigest || pin.Generation != 1 || status.Generation != 1 {
		t.Fatalf("exact pin did not resolve: pin=%+v status=%+v err=%v", pin, status, err)
	}

	v2 := registryRelease(t, private, "2.0.0", "build more carefully")
	if err := registry.StoreRelease(v2); err != nil {
		t.Fatal(err)
	}
	second := registryStatus(t, private, v2, 2, previous, "active", now.Add(time.Minute))
	previous = storeStatus(t, registry, second, now.Add(time.Minute))
	// A new release's active status does not revoke an unchanged exact v1 pin.
	if _, err := registry.LifecycleForRelease("builder", "1.0.0", v1.ContentDigest, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("new release invalidated still-fresh exact v1 lifecycle: %v", err)
	}
	if _, err := registry.Pin("core", "builder", "2.0.0", v2.ContentDigest, "a-root", "upgrade", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	rollback := registryStatus(t, private, v1, 3, previous, "active", now.Add(2*time.Minute))
	storeStatus(t, registry, rollback, now.Add(2*time.Minute))
	pins, err = registry.Pin("core", "builder", "1.0.0", v1.ContentDigest, "a-root", "explicit rollback after regression", now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if pins.Generation != 3 || pins.Pins["builder"].Generation != 3 || pins.History[2].Previous == nil || pins.History[2].Previous.Version != "2.0.0" {
		t.Fatalf("rollback did not record monotonic generation and prior pin: %+v", pins)
	}
	loaded, err := LoadProject(w, "core")
	if err != nil {
		t.Fatal(err)
	}
	if generation, ok := loaded.Doc.Front.Get("role_registry_generation"); !ok || generation != "3" {
		t.Fatalf("project.md omitted owner pin generation: %q", generation)
	}
}

func TestRoleRegistryHistoricalExpiryDoesNotBlockFreshLifecycleRefresh(t *testing.T) {
	registry, _, private, now := registryFixture(t)
	v1 := registryRelease(t, private, "1.0.0", "first")
	if err := registry.StoreRelease(v1); err != nil {
		t.Fatal(err)
	}
	first, err := rolebundle.SignStatus(rolebundle.LifecycleStatus{SchemaVersion: 1, RoleID: "builder", Version: "1.0.0", ContentDigest: v1.ContentDigest, Generation: 1, State: "active", IssuedAt: now, ExpiresAt: now.Add(time.Minute), PublisherKeyID: "publisher"}, private)
	if err != nil {
		t.Fatal(err)
	}
	previous := storeStatus(t, registry, first, now)
	v2 := registryRelease(t, private, "2.0.0", "second")
	if err := registry.StoreRelease(v2); err != nil {
		t.Fatal(err)
	}
	second, err := rolebundle.SignStatus(rolebundle.LifecycleStatus{SchemaVersion: 1, RoleID: "builder", Version: "2.0.0", ContentDigest: v2.ContentDigest, Generation: 2, PreviousStatusDigest: previous, State: "active", IssuedAt: now.Add(2 * time.Minute), ExpiresAt: now.Add(3 * time.Minute), PublisherKeyID: "publisher"}, private)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.StoreLifecycle(second, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("expired historical statement blocked fresh lifecycle refresh: %v", err)
	}
	if _, err := registry.CurrentLifecycle("builder", now.Add(2*time.Minute+time.Second)); err != nil {
		t.Fatalf("fresh latest statement did not authorize after refresh: %v", err)
	}
}

func TestRoleRegistryFreshOldPinSurvivesExpiredUnrelatedNewerStatus(t *testing.T) {
	registry, _, private, now := registryFixture(t)
	v1 := registryRelease(t, private, "1.0.0", "old still pinned")
	if err := registry.StoreRelease(v1); err != nil {
		t.Fatal(err)
	}
	first := registryStatus(t, private, v1, 1, "", "active", now)
	first.ExpiresAt = now.Add(time.Hour)
	first, err := rolebundle.SignStatus(first, private)
	if err != nil {
		t.Fatal(err)
	}
	previous := storeStatus(t, registry, first, now)
	v2 := registryRelease(t, private, "2.0.0", "unrelated latest")
	if err := registry.StoreRelease(v2); err != nil {
		t.Fatal(err)
	}
	second := registryStatus(t, private, v2, 2, previous, "active", now.Add(time.Minute))
	second.ExpiresAt = now.Add(2 * time.Minute)
	second, err = rolebundle.SignStatus(second, private)
	if err != nil {
		t.Fatal(err)
	}
	storeStatus(t, registry, second, now.Add(time.Minute))
	if _, err := registry.LifecycleForRelease("builder", "1.0.0", v1.ContentDigest, now.Add(3*time.Minute)); err != nil {
		t.Fatalf("fresh pinned v1 was blocked by expired unrelated v2 status: %v", err)
	}
	if _, err := registry.LifecycleForRelease("builder", "2.0.0", v2.ContentDigest, now.Add(3*time.Minute)); !errors.Is(err, ErrRoleRegistry) {
		t.Fatalf("expired selected v2 status authorized: %v", err)
	}
}

func TestRoleRegistryRejectsStaleStatusRevocationResurrectionAndVersionMutation(t *testing.T) {
	registry, _, private, now := registryFixture(t)
	v1 := registryRelease(t, private, "1.0.0", "signed")
	if err := registry.StoreRelease(v1); err != nil {
		t.Fatal(err)
	}
	first := registryStatus(t, private, v1, 1, "", "active", now)
	previous := storeStatus(t, registry, first, now)
	stale := registryStatus(t, private, v1, 1, "", "active", now.Add(time.Second))
	if err := registry.StoreLifecycle(stale, now.Add(time.Second)); !errors.Is(err, ErrRoleRegistry) {
		t.Fatalf("replayed lifecycle generation accepted: %v", err)
	}
	revoked := registryStatus(t, private, v1, 2, previous, "revoked", now.Add(time.Minute))
	previous = storeStatus(t, registry, revoked, now.Add(time.Minute))
	resurrection := registryStatus(t, private, v1, 3, previous, "active", now.Add(2*time.Minute))
	if err := registry.StoreLifecycle(resurrection, now.Add(2*time.Minute)); !errors.Is(err, ErrRoleRegistry) {
		t.Fatalf("revoked digest resurrected: %v", err)
	}
	changed := registryRelease(t, private, "1.0.0", "mutated signed release")
	if err := registry.StoreRelease(changed); !errors.Is(err, ErrRoleRegistry) {
		t.Fatalf("second content for immutable role/version accepted: %v", err)
	}
}

func TestRoleRegistryRejectsSymlinkCacheAndProjectPinDrift(t *testing.T) {
	registry, w, private, now := registryFixture(t)
	release := registryRelease(t, private, "1.0.0", "signed")
	if err := registry.StoreRelease(release); err != nil {
		t.Fatal(err)
	}
	status := registryStatus(t, private, release, 1, "", "active", now)
	storeStatus(t, registry, status, now)
	if _, err := registry.Pin("core", "builder", "1.0.0", release.ContentDigest, "a-root", "owner pin", now); err != nil {
		t.Fatal(err)
	}
	projectPath := w.ProjectPath("core")
	if err := os.Remove(projectPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "outside.md"), projectPath); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.ReadPins("core"); !errors.Is(err, ErrRoleRegistry) {
		t.Fatalf("symlinked project pin file accepted: %v", err)
	}
}

func TestRoleRegistryRequiresFreshCachedLifecycleOffline(t *testing.T) {
	registry, _, private, now := registryFixture(t)
	release := registryRelease(t, private, "1.0.0", "offline signed")
	if err := registry.StoreRelease(release); err != nil {
		t.Fatal(err)
	}
	status, err := rolebundle.SignStatus(rolebundle.LifecycleStatus{SchemaVersion: 1, RoleID: "builder", Version: "1.0.0", ContentDigest: release.ContentDigest, Generation: 1, State: "active", IssuedAt: now, ExpiresAt: now.Add(time.Minute), PublisherKeyID: "publisher"}, private)
	if err != nil {
		t.Fatal(err)
	}
	storeStatus(t, registry, status, now)
	if _, err := registry.CurrentLifecycle("builder", now.Add(30*time.Second)); err != nil {
		t.Fatalf("fresh cached signed status unavailable offline: %v", err)
	}
	if _, err := registry.CurrentLifecycle("builder", now.Add(time.Minute)); !errors.Is(err, ErrRoleRegistry) {
		t.Fatalf("expired cached status authorized: %v", err)
	}
}

func TestRoleRegistryPublisherKeyRotationRetainsOldVerificationKey(t *testing.T) {
	w, err := workspace.Init(t.TempDir(), "key-rotation")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CreateProject(w, "a-root", "Core", "core", "role pins", ""); err != nil {
		t.Fatal(err)
	}
	oldPub, oldPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	newPub, newPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewRoleRegistry(w, map[string]ed25519.PublicKey{"old": oldPub})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	oldRelease, err := rolebundle.Sign(rolebundle.ReleaseContent{SchemaVersion: 1, Version: "1.0.0", Role: team.Role{Name: "builder", Grant: "rw"}}, "old", oldPrivate)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.StoreRelease(oldRelease); err != nil {
		t.Fatal(err)
	}
	first, err := rolebundle.SignStatus(rolebundle.LifecycleStatus{SchemaVersion: 1, RoleID: "builder", Version: oldRelease.Content.Version, ContentDigest: oldRelease.ContentDigest, Generation: 1, State: "active", IssuedAt: now, ExpiresAt: now.Add(time.Hour), PublisherKeyID: "old"}, oldPrivate)
	if err != nil {
		t.Fatal(err)
	}
	previous := storeStatus(t, registry, first, now)

	rotated, err := NewRoleRegistry(w, map[string]ed25519.PublicKey{"old": oldPub, "new": newPub})
	if err != nil {
		t.Fatal(err)
	}
	newRelease, err := rolebundle.Sign(rolebundle.ReleaseContent{SchemaVersion: 1, Version: "2.0.0", Role: team.Role{Name: "builder", Grant: "rw", Prompt: "new signing key"}}, "new", newPrivate)
	if err != nil {
		t.Fatal(err)
	}
	if err := rotated.StoreRelease(newRelease); err != nil {
		t.Fatal(err)
	}
	second, err := rolebundle.SignStatus(rolebundle.LifecycleStatus{SchemaVersion: 1, RoleID: "builder", Version: newRelease.Content.Version, ContentDigest: newRelease.ContentDigest, Generation: 2, PreviousStatusDigest: previous, State: "active", IssuedAt: now.Add(time.Minute), ExpiresAt: now.Add(2 * time.Hour), PublisherKeyID: "new"}, newPrivate)
	if err != nil {
		t.Fatal(err)
	}
	storeStatus(t, rotated, second, now.Add(time.Minute))
	if _, err := rotated.ReadRelease("builder", "1.0.0", oldRelease.ContentDigest); err != nil {
		t.Fatalf("rotation dropped retained old verification key: %v", err)
	}
	current, err := rotated.CurrentLifecycle("builder", now.Add(2*time.Minute))
	if err != nil || current.PublisherKeyID != "new" || current.Generation != 2 {
		t.Fatalf("new key not active after chained rotation: %+v %v", current, err)
	}
}
