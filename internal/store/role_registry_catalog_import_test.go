package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/rolebundle"
	"github.com/mlnomadpy/dacli/internal/team"
)

func TestImportRoleCatalogHistoryAcceptsExpiredPredecessorAndKeepsSelectionUnchanged(t *testing.T) {
	registry, w, private, now := registryFixture(t)
	if err := CreateRole(w, "owner:test", team.Role{Name: "builder", Summary: "build", Grant: "rw", Scope: []string{"internal/**"}, Prompt: "canonical role"}); err != nil {
		t.Fatal(err)
	}
	first := registryRelease(t, private, "1.0.0", "old exact release")
	second := registryRelease(t, private, "2.0.0", "current exact release")
	oldStatus, err := rolebundle.SignStatus(rolebundle.LifecycleStatus{SchemaVersion: 1, RoleID: "builder", Version: first.Content.Version, ContentDigest: first.ContentDigest, Generation: 1, State: "active", IssuedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Hour), PublisherKeyID: "publisher"}, private)
	if err != nil {
		t.Fatal(err)
	}
	oldDigest, err := rolebundle.LifecycleDigest(oldStatus)
	if err != nil {
		t.Fatal(err)
	}
	currentStatus := registryStatus(t, private, second, 2, oldDigest, "active", now)
	projectBefore, err := os.ReadFile(w.ProjectPath("core"))
	if err != nil {
		t.Fatal(err)
	}
	roleBefore, err := os.ReadFile(w.RolePath("builder"))
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.ImportRoleRelease(first, oldStatus, now); !errors.Is(err, ErrRoleRegistry) {
		t.Fatalf("single-release import unexpectedly consumed an expired predecessor: %v", err)
	}
	envelopes := catalogReleaseEnvelopes(t, first, second)
	if _, err := registry.ImportRoleCatalogHistory([]rolebundle.Release{first, second}, envelopes, []rolebundle.LifecycleStatus{oldStatus, currentStatus}, "builder", second.Content.Version, second.ContentDigest, now); err != nil {
		t.Fatalf("complete signed catalog history with expired predecessor was refused: %v", err)
	}
	verified, err := registry.VerifyRoleRelease("builder", second.Content.Version, second.ContentDigest, now)
	if err != nil || verified.Lifecycle.Generation != 2 || verified.Lifecycle.PreviousStatusDigest != oldDigest {
		t.Fatalf("current imported exact release lost its linked history: %+v %v", verified, err)
	}
	firstPath, err := registry.releasePath("builder", first.Content.Version, first.ContentDigest)
	if err != nil {
		t.Fatal(err)
	}
	firstBytes, err := os.ReadFile(firstPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.ImportRoleCatalogHistory([]rolebundle.Release{first, second}, envelopes, []rolebundle.LifecycleStatus{oldStatus, currentStatus}, "builder", second.Content.Version, second.ContentDigest, now); err != nil {
		t.Fatalf("identical catalog transfer was not idempotent: %v", err)
	}
	afterBytes, err := os.ReadFile(firstPath)
	if err != nil || !bytes.Equal(firstBytes, afterBytes) {
		t.Fatalf("repeat import changed immutable cached release bytes: %v", err)
	}
	projectAfter, err := os.ReadFile(w.ProjectPath("core"))
	if err != nil || !bytes.Equal(projectBefore, projectAfter) {
		t.Fatalf("catalog import changed project selection: %v", err)
	}
	roleAfter, err := os.ReadFile(w.RolePath("builder"))
	if err != nil || !bytes.Equal(roleBefore, roleAfter) {
		t.Fatalf("catalog import changed canonical role bytes: %v", err)
	}
}

func TestImportRoleCatalogHistoryReusesSingleImportCacheWithoutRewriting(t *testing.T) {
	registry, _, private, now := registryFixture(t)
	release := registryRelease(t, private, "1.0.0", "single-import release")
	status := registryStatus(t, private, release, 1, "", "active", now)
	if err := registry.ImportRoleRelease(release, status, now); err != nil {
		t.Fatalf("seed the existing single-release importer cache: %v", err)
	}
	path, err := registry.releasePath("builder", release.Content.Version, release.ContentDigest)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.ImportRoleCatalogHistory([]rolebundle.Release{release}, catalogReleaseEnvelopes(t, release), []rolebundle.LifecycleStatus{status}, "builder", release.Content.Version, release.ContentDigest, now); err != nil {
		t.Fatalf("catalog import did not accept the identical signed single-import cache: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("idempotent catalog import rewrote existing cache bytes: %v", err)
	}
}

func TestImportRoleCatalogHistoryRejectsVersionConflictBeforeAnyWrites(t *testing.T) {
	registry, _, private, now := registryFixture(t)
	original := registryRelease(t, private, "1.0.0", "original immutable release")
	originalStatus := registryStatus(t, private, original, 1, "", "active", now)
	if _, err := registry.ImportRoleCatalogHistory([]rolebundle.Release{original}, catalogReleaseEnvelopes(t, original), []rolebundle.LifecycleStatus{originalStatus}, "builder", original.Content.Version, original.ContentDigest, now); err != nil {
		t.Fatal(err)
	}
	originalPath, err := registry.releasePath("builder", original.Content.Version, original.ContentDigest)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(originalPath)
	if err != nil {
		t.Fatal(err)
	}
	conflict := registryRelease(t, private, "1.0.0", "different immutable release")
	conflictStatus := registryStatus(t, private, conflict, 1, "", "active", now)
	if _, err := registry.ImportRoleCatalogHistory([]rolebundle.Release{conflict}, catalogReleaseEnvelopes(t, conflict), []rolebundle.LifecycleStatus{conflictStatus}, "builder", conflict.Content.Version, conflict.ContentDigest, now); !errors.Is(err, ErrRoleRegistry) {
		t.Fatalf("different digest for an already cached role/version was accepted: %v", err)
	}
	after, err := os.ReadFile(originalPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("refused immutable version conflict changed original bytes: %v", err)
	}
	conflictPath, err := registry.releasePath("builder", conflict.Content.Version, conflict.ContentDigest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(conflictPath); !os.IsNotExist(err) {
		t.Fatalf("refused immutable version conflict wrote its release: %v", err)
	}
}

func TestImportRoleCatalogHistoryPrevalidatesTerminalRevocationBeforeWrites(t *testing.T) {
	registry, _, private, now := registryFixture(t)
	release := registryRelease(t, private, "1.0.0", "terminal revocation")
	revoked, err := rolebundle.SignStatus(rolebundle.LifecycleStatus{SchemaVersion: 1, RoleID: "builder", Version: release.Content.Version, ContentDigest: release.ContentDigest, Generation: 1, State: "revoked", IssuedAt: now, ExpiresAt: now.Add(time.Hour), PublisherKeyID: "publisher"}, private)
	if err != nil {
		t.Fatal(err)
	}
	revokedDigest, err := rolebundle.LifecycleDigest(revoked)
	if err != nil {
		t.Fatal(err)
	}
	resurrected := registryStatus(t, private, release, 2, revokedDigest, "active", now)
	if _, err := registry.ImportRoleCatalogHistory([]rolebundle.Release{release}, catalogReleaseEnvelopes(t, release), []rolebundle.LifecycleStatus{revoked, resurrected}, "builder", release.Content.Version, release.ContentDigest, now); !errors.Is(err, ErrRoleRegistry) {
		t.Fatalf("terminal revocation followed by active status was accepted: %v", err)
	}
	path, err := registry.releasePath("builder", release.Content.Version, release.ContentDigest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("invalid terminal history left a release cache entry: %v", err)
	}
	if _, err := registry.readLifecycleHistory(filepath.Join(registry.lifecycleDir(), "builder")); !os.IsNotExist(err) {
		t.Fatalf("invalid terminal history left lifecycle entries: %v", err)
	}
}

func TestImportRoleCatalogHistoryRejectsGapAndStaleSelectedStatus(t *testing.T) {
	registry, _, private, now := registryFixture(t)
	first := registryRelease(t, private, "1.0.0", "first")
	second := registryRelease(t, private, "2.0.0", "second")
	firstStatus := registryStatus(t, private, first, 1, "", "active", now)
	firstDigest, err := rolebundle.LifecycleDigest(firstStatus)
	if err != nil {
		t.Fatal(err)
	}
	secondStatus := registryStatus(t, private, second, 3, firstDigest, "active", now)
	envelopes := catalogReleaseEnvelopes(t, first, second)
	if _, err := registry.ImportRoleCatalogHistory([]rolebundle.Release{first, second}, envelopes, []rolebundle.LifecycleStatus{firstStatus, secondStatus}, "builder", second.Content.Version, second.ContentDigest, now); !errors.Is(err, ErrRoleRegistry) {
		t.Fatalf("generation gap accepted: %v", err)
	}
	stale, err := rolebundle.SignStatus(rolebundle.LifecycleStatus{SchemaVersion: 1, RoleID: "builder", Version: second.Content.Version, ContentDigest: second.ContentDigest, Generation: 2, PreviousStatusDigest: firstDigest, State: "active", IssuedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Hour), PublisherKeyID: "publisher"}, private)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.ImportRoleCatalogHistory([]rolebundle.Release{first, second}, envelopes, []rolebundle.LifecycleStatus{firstStatus, stale}, "builder", second.Content.Version, second.ContentDigest, now); !errors.Is(err, ErrRoleRegistry) {
		t.Fatalf("stale selected lifecycle accepted: %v", err)
	}
	path, err := registry.releasePath("builder", first.Content.Version, first.ContentDigest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("refused catalog left a partial release import: %v", err)
	}
}

func catalogReleaseEnvelopes(t *testing.T, releases ...rolebundle.Release) [][]byte {
	t.Helper()
	out := make([][]byte, len(releases))
	for i, release := range releases {
		var err error
		out[i], err = json.Marshal(release)
		if err != nil {
			t.Fatal(err)
		}
	}
	return out
}
