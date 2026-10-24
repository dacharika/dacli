package store

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/rolebundle"
	"github.com/mlnomadpy/dacli/internal/team"
)

func TestRoleRegistryManagementImportVerifiesExactFilesWithoutSelecting(t *testing.T) {
	registry, _, private, now := registryFixture(t)
	assertNotCached := func(roleID, version, digest string) {
		t.Helper()
		path, err := registry.releasePath(roleID, version, digest)
		if err != nil {
			t.Fatalf("build exact cache path: %v", err)
		}
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("rejected import left a release cache entry or unexpected filesystem result: %v", err)
		}
		// ReadRelease may normalize an invalid cached file to ErrRoleRegistry;
		// that API error alone cannot prove the rejected import wrote nothing.
		if _, err := registry.ReadRelease(roleID, version, digest); err == nil {
			t.Fatalf("rejected import left a readable cached release at %s", path)
		}
	}
	release := registryRelease(t, private, "1.0.0", "same canonical role")
	status := registryStatus(t, private, release, 1, "", "active", now)
	if err := registry.ImportRoleRelease(release, status, now); err != nil {
		t.Fatalf("import signed exact files: %v", err)
	}
	pins, err := registry.ReadPins("core")
	if err != nil || pins.Generation != 0 || len(pins.Pins) != 0 || len(pins.History) != 0 {
		t.Fatalf("import implicitly selected a release: pins=%+v err=%v", pins, err)
	}

	tampered := registryRelease(t, private, "2.0.0", "tamper fixture")
	tampered.Signature[0] ^= 0xff
	tamperedStatus := registryStatus(t, private, tampered, 1, "", "active", now)
	if err := registry.ImportRoleRelease(tampered, tamperedStatus, now); !errors.Is(err, ErrRoleRegistry) {
		t.Fatalf("tampered release signature accepted: %v", err)
	}
	assertNotCached("builder", "2.0.0", tampered.ContentDigest)

	mismatchedRelease := registryRelease(t, private, "2.1.0", "mismatched pair fixture")
	otherRelease := registryRelease(t, private, "2.2.0", "different exact pair")
	otherStatus := registryStatus(t, private, otherRelease, 1, "", "active", now)
	if err := registry.ImportRoleRelease(mismatchedRelease, otherStatus, now); !errors.Is(err, ErrRoleRegistry) {
		t.Fatalf("mismatched signed release/lifecycle pair accepted: %v", err)
	}
	assertNotCached("builder", mismatchedRelease.Content.Version, mismatchedRelease.ContentDigest)

	expired := registryRelease(t, private, "3.0.0", "expired fixture")
	expiredStatus, err := rolebundle.SignStatus(rolebundle.LifecycleStatus{SchemaVersion: 1, RoleID: "builder", Version: expired.Content.Version, ContentDigest: expired.ContentDigest, Generation: 1, State: "active", IssuedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Hour), PublisherKeyID: "publisher"}, private)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.ImportRoleRelease(expired, expiredStatus, now); !errors.Is(err, ErrRoleRegistry) {
		t.Fatalf("expired lifecycle accepted: %v", err)
	}
	assertNotCached("builder", "3.0.0", expired.ContentDigest)
}

func TestRoleRegistryManagementCompareAndSelectHistoryRollbackAndStalePreservation(t *testing.T) {
	registry, w, private, now := registryFixture(t)
	if err := CreateRole(w, "owner:test", team.Role{Name: "builder", Summary: "build", Grant: "rw", Scope: []string{"internal/**"}, Prompt: "same canonical role"}); err != nil {
		t.Fatal(err)
	}
	canonicalRoles, err := ReadCheckpointRoles(w)
	if err != nil || len(canonicalRoles) != 1 {
		t.Fatalf("read created canonical role: count=%d err=%v", len(canonicalRoles), err)
	}
	signCanonicalRelease := func(version string) rolebundle.Release {
		t.Helper()
		release, err := rolebundle.Sign(rolebundle.ReleaseContent{SchemaVersion: 1, Version: version, Role: canonicalRoles[0]}, "publisher", private)
		if err != nil {
			t.Fatal(err)
		}
		return release
	}
	v1 := signCanonicalRelease("1.0.0")
	v2 := signCanonicalRelease("2.0.0")
	if err := registry.ImportRoleRelease(v1, registryStatus(t, private, v1, 1, "", "active", now), now); err != nil {
		t.Fatal(err)
	}
	previous := registryStatus(t, private, v1, 1, "", "active", now)
	previousDigest, err := rolebundle.LifecycleDigest(previous)
	if err != nil {
		t.Fatal(err)
	}
	status2 := registryStatus(t, private, v2, 2, previousDigest, "active", now.Add(time.Minute))
	if err := registry.ImportRoleRelease(v2, status2, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	status2Digest, err := rolebundle.LifecycleDigest(status2)
	if err != nil {
		t.Fatal(err)
	}
	v3 := signCanonicalRelease("3.0.0")
	if err := registry.ImportRoleRelease(v3, registryStatus(t, private, v3, 3, status2Digest, "active", now.Add(2*time.Minute)), now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}

	pins, err := registry.CompareAndSelect("core", "builder", v1.Content.Version, v1.ContentDigest, 0, nil, "owner:install", "initial selection", "pin", now)
	if err != nil || pins.Generation != 1 {
		t.Fatalf("initial exact pin failed: pins=%+v err=%v", pins, err)
	}
	projectPath := w.ProjectPath("core")
	before, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	current := pins.Pins["builder"]
	if _, err := registry.CompareAndSelect("core", "builder", v2.Content.Version, v2.ContentDigest, 0, &current, "owner:install", "stale upgrade", "upgrade", now.Add(2*time.Minute)); !errors.Is(err, ErrRoleRegistry) || !strings.Contains(err.Error(), "project role registry generation changed") {
		t.Fatalf("stale project generation was not the refusal cause: %v", err)
	}
	after, err := os.ReadFile(projectPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("stale mutation changed project bytes: err=%v", err)
	}

	wrongCurrent := current
	wrongCurrent.Digest = "sha256:" + string(bytes.Repeat([]byte("0"), 64))
	if _, err := registry.CompareAndSelect("core", "builder", v2.Content.Version, v2.ContentDigest, 1, &wrongCurrent, "owner:install", "wrong current pin", "upgrade", now.Add(2*time.Minute)); !errors.Is(err, ErrRoleRegistry) || !strings.Contains(err.Error(), "current role pin changed") {
		t.Fatalf("wrong current pin was not the refusal cause: %v", err)
	}
	upgraded, err := registry.CompareAndSelect("core", "builder", v2.Content.Version, v2.ContentDigest, 1, &current, "owner:install", "explicit upgrade", "upgrade", now.Add(3*time.Minute))
	if err != nil || upgraded.Generation != 2 || upgraded.Pins["builder"].Version != "2.0.0" {
		t.Fatalf("exact upgrade failed: pins=%+v err=%v", upgraded, err)
	}
	current = upgraded.Pins["builder"]
	rolledBack, err := registry.CompareAndSelect("core", "builder", v1.Content.Version, v1.ContentDigest, 2, &current, "owner:install", "restore recorded prior selection", "rollback", now.Add(4*time.Minute))
	if err != nil || rolledBack.Generation != 3 || rolledBack.Pins["builder"].Version != "1.0.0" || len(rolledBack.History) != 3 {
		t.Fatalf("explicit rollback did not retain ordered history: pins=%+v err=%v", rolledBack, err)
	}
	last := rolledBack.History[2]
	if last.ChangedBy != "owner:install" || last.Reason != "restore recorded prior selection" || last.Previous == nil || last.Previous.Version != "2.0.0" {
		t.Fatalf("rollback lost actor/reason/previous selection: %+v", last)
	}
	current = rolledBack.Pins["builder"]
	beforeUnhistoricalRollback, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.CompareAndSelect("core", "builder", v3.Content.Version, v3.ContentDigest, 3, &current, "owner:install", "not historical", "rollback", now.Add(5*time.Minute)); !errors.Is(err, ErrRoleRegistry) || !strings.Contains(err.Error(), "rollback target is not a prior selection") {
		t.Fatalf("valid unselected release was not refused by the history guard: %v", err)
	}
	afterUnhistoricalRollback, err := os.ReadFile(projectPath)
	if err != nil || !bytes.Equal(beforeUnhistoricalRollback, afterUnhistoricalRollback) {
		t.Fatalf("refused nonhistorical rollback changed project bytes: %v", err)
	}
}

func TestRoleRegistryManagementRevocationMakesExactReleaseUnselectable(t *testing.T) {
	registry, _, private, now := registryFixture(t)
	release := registryRelease(t, private, "1.0.0", "same canonical role")
	active := registryStatus(t, private, release, 1, "", "active", now)
	if err := registry.ImportRoleRelease(release, active, now); err != nil {
		t.Fatal(err)
	}
	previous, err := rolebundle.LifecycleDigest(active)
	if err != nil {
		t.Fatal(err)
	}
	revoked := registryStatus(t, private, release, 2, previous, "revoked", now.Add(time.Minute))
	if err := registry.ImportRoleRelease(release, revoked, now.Add(time.Minute)); err != nil {
		t.Fatalf("cache signed revocation: %v", err)
	}
	if _, err := registry.VerifyRoleRelease("builder", "1.0.0", release.ContentDigest, now.Add(2*time.Minute)); !errors.Is(err, ErrRoleRegistry) {
		t.Fatalf("revoked release remained selectable: %v", err)
	}
}
