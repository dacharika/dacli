package cli

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/application"
	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/rolebundle"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

func TestRoleRegistryLifecycleCommandsComposeExactOfflineHistory(t *testing.T) {
	dir := t.TempDir()
	run(t, dir, 0, "init", "--name", "role-lifecycle")
	run(t, dir, 0, "project", "add", "Core", "--slug", "core", "--goal", "role lifecycle")
	run(t, dir, 0, "role", "add", "builder")
	w, err := workspace.Find(dir)
	if err != nil {
		t.Fatal(err)
	}
	roles, err := store.ReadCheckpointRoles(w)
	if err != nil || len(roles) != 1 {
		t.Fatalf("read canonical role: %v", err)
	}
	keyOld := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{41}, ed25519.SeedSize))
	keyNew := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{42}, ed25519.SeedSize))
	registryDir := filepath.Join(w.Root, workspace.Dir, "role-registry")
	if err := os.MkdirAll(registryDir, 0700); err != nil {
		t.Fatal(err)
	}
	writeRoleLifecycleJSON(t, filepath.Join(registryDir, "publishers.json"), map[string][]byte{"publisher-old": keyOld.Public().(ed25519.PublicKey), "publisher-new": keyNew.Public().(ed25519.PublicKey)})
	privateDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(privateDir, "owner.json")
	writeRoleLifecycleJSON(t, config, map[string]any{"schema_version": 1, "workspace": w.Root, "authority": filepath.Join(privateDir, "authority.json"), "installation_id": "role-lifecycle-owner", "owner_credential_file": filepath.Join(privateDir, "owner.token")})
	run(t, dir, 0, "native", "bootstrap", "--config", config)
	ownerTokenBytes, err := os.ReadFile(filepath.Join(privateDir, "owner.token"))
	if err != nil {
		t.Fatal(err)
	}
	upgradeRoleLifecycleAuthorityTo20(t, w, filepath.Join(privateDir, "authority.json"), "role-lifecycle-owner", strings.TrimSpace(string(ownerTokenBytes)))

	now := time.Now().UTC().Truncate(time.Second)
	newRelease := func(version, publisher string, private ed25519.PrivateKey) rolebundle.Release {
		t.Helper()
		release, signErr := rolebundle.Sign(rolebundle.ReleaseContent{SchemaVersion: rolebundle.SchemaVersion, Version: version, Role: roles[0]}, publisher, private)
		if signErr != nil {
			t.Fatal(signErr)
		}
		return release
	}
	status := func(release rolebundle.Release, generation uint64, previous, state, publisher string, private ed25519.PrivateKey, issued, expires time.Time) rolebundle.LifecycleStatus {
		t.Helper()
		value, signErr := rolebundle.SignStatus(rolebundle.LifecycleStatus{SchemaVersion: 1, RoleID: "builder", Version: release.Content.Version, ContentDigest: release.ContentDigest, Generation: generation, PreviousStatusDigest: previous, State: state, IssuedAt: issued, ExpiresAt: expires, PublisherKeyID: publisher}, private)
		if signErr != nil {
			t.Fatal(signErr)
		}
		return value
	}
	v1 := newRelease("1.0.0", "publisher-old", keyOld)
	v2 := newRelease("2.0.0", "publisher-new", keyNew)
	v3 := newRelease("3.0.0", "publisher-new", keyNew)
	v4 := newRelease("4.0.0", "publisher-new", keyNew)
	s1 := status(v1, 1, "", "active", "publisher-old", keyOld, now, now.Add(time.Hour))
	d1, err := rolebundle.LifecycleDigest(s1)
	if err != nil {
		t.Fatal(err)
	}
	s2 := status(v2, 2, d1, "active", "publisher-new", keyNew, now, now.Add(time.Hour))
	d2, err := rolebundle.LifecycleDigest(s2)
	if err != nil {
		t.Fatal(err)
	}
	s3 := status(v3, 3, d2, "active", "publisher-new", keyNew, now.Add(-2*time.Hour), now.Add(-time.Hour))
	d3, err := rolebundle.LifecycleDigest(s3)
	if err != nil {
		t.Fatal(err)
	}
	s4 := status(v4, 4, d3, "revoked", "publisher-new", keyNew, now, now.Add(time.Hour))
	export := buildCLIHostedRoleExport(t, now, []rolebundle.Release{v1, v2, v3, v4}, []rolebundle.LifecycleStatus{s1, s2, s3, s4}, v1)
	exportPath := filepath.Join(privateDir, "catalog-export.json")
	writeRoleLifecycleJSON(t, exportPath, export)
	projectBeforeImport, err := os.ReadFile(w.ProjectPath("core"))
	if err != nil {
		t.Fatal(err)
	}
	roleBeforeImport, err := os.ReadFile(w.RolePath("builder"))
	if err != nil {
		t.Fatal(err)
	}
	base := []string{"--config", config, "--project", "core"}
	// The pre-batch single-file sequence cannot carry an expired signed
	// predecessor needed to validate the selected current generation.
	gapFirst := newRelease("9.0.0", "publisher-old", keyOld)
	gapSecond := newRelease("10.0.0", "publisher-new", keyNew)
	expiredFirst := status(gapFirst, 1, "", "active", "publisher-old", keyOld, now.Add(-2*time.Hour), now.Add(-time.Hour))
	expiredFirstDigest, err := rolebundle.LifecycleDigest(expiredFirst)
	if err != nil {
		t.Fatal(err)
	}
	freshSecond := status(gapSecond, 2, expiredFirstDigest, "active", "publisher-new", keyNew, now, now.Add(time.Hour))
	writeRoleLifecycleJSON(t, filepath.Join(privateDir, "expired-v1.json"), gapFirst)
	writeRoleLifecycleJSON(t, filepath.Join(privateDir, "expired-v1-status.json"), expiredFirst)
	writeRoleLifecycleJSON(t, filepath.Join(privateDir, "fresh-v2.json"), gapSecond)
	writeRoleLifecycleJSON(t, filepath.Join(privateDir, "fresh-v2-status.json"), freshSecond)
	for _, pair := range [][2]string{{"expired-v1.json", "expired-v1-status.json"}, {"fresh-v2.json", "fresh-v2-status.json"}} {
		args := append(append([]string{}, base...), "--release", pair[0], "--lifecycle", pair[1])
		if _, singleErr := invokeRoleRegistry(t, privateDir, append([]string{"role", "registry", "import"}, args...)...); clikit.ExitCode(singleErr) != 3 {
			t.Fatalf("legacy one-release import accepted stale or missing historical chain entry (%s): %v", pair[0], singleErr)
		}
	}
	text, err := invokeRoleRegistry(t, privateDir, append([]string{"role", "registry", "import"}, append(base, "--catalog-export", filepath.Base(exportPath))...)...)
	var receipt application.NativeRoleRegistryCatalogImportReceipt
	if err != nil || json.Unmarshal([]byte(text), &receipt) != nil || receipt.Selected || receipt.ReleaseCount != 4 || receipt.LifecycleCount != 8 {
		t.Fatalf("composed hosted export import failed or activated a pin: %v %s", err, text)
	}
	projectAfterImport, _ := os.ReadFile(w.ProjectPath("core"))
	roleAfterImport, _ := os.ReadFile(w.RolePath("builder"))
	if !bytes.Equal(projectBeforeImport, projectAfterImport) || !bytes.Equal(roleBeforeImport, roleAfterImport) {
		t.Fatal("nonactivating import changed project or canonical role bytes")
	}
	verifyTarget := func(release rolebundle.Release, wantErr bool, expected string) {
		t.Helper()
		text, verifyErr := invokeRoleRegistry(t, dir, append([]string{"role", "registry", "verify"}, append(base, "--role", "builder", "--version", release.Content.Version, "--digest", release.ContentDigest)...)...)
		if wantErr {
			if clikit.ExitCode(verifyErr) != 3 {
				t.Fatalf("verify %s did not refuse stale/revoked lifecycle: %v %s", release.Content.Version, verifyErr, text)
			}
			return
		}
		var view application.NativeRoleRegistryView
		if verifyErr != nil || json.Unmarshal([]byte(text), &view) != nil || view.Target.Lifecycle.State != expected {
			t.Fatalf("verify %s failed offline or lost current signed lifecycle: %v %s", release.Content.Version, verifyErr, text)
		}
	}
	verifyTarget(v1, false, "active") // A later expired status for another version does not stale this exact release.
	verifyTarget(v2, false, "active") // The new publisher key is separately trusted after rotation.
	verifyTarget(v3, true, "")        // Historical import does not make an expired exact status fresh.
	verifyTarget(v4, true, "")        // A fresh revocation remains terminal for this exact release.
	projectBeforeRefusals, _ := os.ReadFile(w.ProjectPath("core"))
	roleBeforeRefusals, _ := os.ReadFile(w.RolePath("builder"))
	if _, err := invokeRoleRegistry(t, dir, append([]string{"role", "registry", "import"}, append(base, "--release", "missing-release.json", "--lifecycle", "missing-status.json")...)...); clikit.ExitCode(err) != 3 {
		t.Fatalf("stale single import path was not refused: %v", err)
	}
	projectAfterRefusals, _ := os.ReadFile(w.ProjectPath("core"))
	roleAfterRefusals, _ := os.ReadFile(w.RolePath("builder"))
	if !bytes.Equal(projectBeforeRefusals, projectAfterRefusals) || !bytes.Equal(roleBeforeRefusals, roleAfterRefusals) {
		t.Fatal("refused stale import or lifecycle verification changed canonical role/project bytes")
	}
	target := append(base, "--role", "builder", "--version", v1.Content.Version, "--digest", v1.ContentDigest)
	if _, err := invokeRoleRegistry(t, dir, append([]string{"role", "registry", "pin"}, append(target, "--expected-generation", "0", "--reason", "initial exact pin")...)...); err != nil {
		t.Fatalf("pin exact fresh old release: %v", err)
	}
	if text, err := invokeRoleRegistry(t, dir, append([]string{"role", "registry", "diff"}, append(base, "--role", "builder", "--version", v2.Content.Version, "--digest", v2.ContentDigest)...)...); err != nil || !strings.Contains(text, "publisher-new") {
		t.Fatalf("diff did not compose target and current selection: %v %s", err, text)
	}
	currentV1 := store.RolePin{RoleID: "builder", Version: v1.Content.Version, Digest: v1.ContentDigest}
	upgradeArgs := append(append([]string{}, base...), "--role", "builder", "--version", v2.Content.Version, "--digest", v2.ContentDigest, "--expected-generation", "1", "--expected-version", currentV1.Version, "--expected-digest", currentV1.Digest, "--reason", "explicit rotated publisher upgrade")
	if _, err := invokeRoleRegistry(t, dir, append([]string{"role", "registry", "upgrade"}, upgradeArgs...)...); err != nil {
		t.Fatalf("explicit upgrade failed: %v", err)
	}
	rollbackArgs := append(append([]string{}, base...), "--role", "builder", "--version", v1.Content.Version, "--digest", v1.ContentDigest, "--expected-generation", "2", "--expected-version", v2.Content.Version, "--expected-digest", v2.ContentDigest, "--reason", "restore known prior selection")
	if _, err := invokeRoleRegistry(t, dir, append([]string{"role", "registry", "rollback"}, rollbackArgs...)...); err != nil {
		t.Fatalf("explicit rollback failed: %v", err)
	}
	registry, err := store.NewRoleRegistry(w, map[string]ed25519.PublicKey{"publisher-old": keyOld.Public().(ed25519.PublicKey), "publisher-new": keyNew.Public().(ed25519.PublicKey)})
	if err != nil {
		t.Fatal(err)
	}
	pins, err := registry.ReadPins("core")
	if err != nil || pins.Generation != 3 || len(pins.History) != 3 || pins.Pins["builder"].Version != v1.Content.Version {
		t.Fatalf("pin lifecycle is not monotonic after rollback: %+v %v", pins, err)
	}
	for i, want := range []string{"initial exact pin", "explicit rotated publisher upgrade", "restore known prior selection"} {
		if pins.History[i].Generation != uint64(i+1) || pins.History[i].ChangedBy != "owner:role-lifecycle-owner" || pins.History[i].Reason != want {
			t.Fatalf("pin history actor/reason/generation[%d]=%+v", i, pins.History[i])
		}
	}
}

func buildCLIHostedRoleExport(t *testing.T, now time.Time, releases []rolebundle.Release, statuses []rolebundle.LifecycleStatus, selected rolebundle.Release) application.NativeRoleRegistryCatalogExport {
	t.Helper()
	entries := make([]application.NativeRoleRegistryCatalogRelease, 0, len(releases))
	for _, release := range releases {
		raw, err := json.Marshal(release)
		if err != nil {
			t.Fatal(err)
		}
		key := application.NativeRoleRegistryCatalogKey{Tenant: "tenant-a", CatalogID: "catalog-a", RoleID: "builder", Version: release.Content.Version, ContentDigest: release.ContentDigest}
		entries = append(entries, application.NativeRoleRegistryCatalogRelease{Key: key, Envelope: raw})
	}
	items := make([]application.NativeRoleRegistryCatalogEvent, 0, len(releases)*2)
	sequence := uint64(0)
	for i, release := range releases {
		key := application.NativeRoleRegistryCatalogKey{Tenant: "tenant-a", CatalogID: "catalog-a", RoleID: "builder", Version: release.Content.Version, ContentDigest: release.ContentDigest}
		sequence++
		items = append(items, application.NativeRoleRegistryCatalogEvent{Key: key, Sequence: sequence, State: "draft", Actor: "account-owner", Reason: "import fixture", RecordedAt: now})
		sequence++
		state := "released"
		if statuses[i].State == "revoked" {
			state = "revoked"
		}
		status := statuses[i]
		items = append(items, application.NativeRoleRegistryCatalogEvent{Key: key, Sequence: sequence, State: state, Status: &status, Actor: "account-owner", Reason: "signed lifecycle fixture", RecordedAt: now})
	}
	return application.NativeRoleRegistryCatalogExport{Selected: application.NativeRoleRegistryCatalogKey{Tenant: "tenant-a", CatalogID: "catalog-a", RoleID: "builder", Version: selected.Content.Version, ContentDigest: selected.ContentDigest}, Releases: entries, Lifecycle: items}
}

func writeRoleLifecycleJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func upgradeRoleLifecycleAuthorityTo20(t *testing.T, w *workspace.Workspace, path, installation, owner string) {
	t.Helper()
	binding := sessions.Binding{Workspace: w.ID, Installation: installation}
	for {
		authority, err := sessions.OpenCurrent(path, binding)
		if err != nil {
			t.Fatal(err)
		}
		snapshot, err := authority.ObserveStorage(owner)
		if err != nil {
			t.Fatal(err)
		}
		for i := range snapshot.PrivateBytes {
			snapshot.PrivateBytes[i] = 0
		}
		if snapshot.AuthorityVersion == 20 {
			return
		}
		var upgradeErr error
		switch snapshot.AuthorityVersion {
		case 6:
			upgradeErr = authority.UpgradeSwarms(owner)
		case 7:
			upgradeErr = authority.UpgradeDispatchIntents(owner)
		case 8:
			upgradeErr = authority.UpgradeLifecycle(owner)
		case 9:
			upgradeErr = authority.UpgradeRecovery(owner)
		case 10:
			upgradeErr = authority.UpgradePreflight(owner)
		case 11:
			upgradeErr = authority.UpgradeCheckpoints(owner)
		case 12:
			upgradeErr = authority.UpgradeBudgets(owner)
		case 13:
			upgradeErr = authority.UpgradeNativeAcceptance(owner)
		case 14:
			upgradeErr = authority.UpgradeNativePolicy(owner)
		case 15:
			upgradeErr = authority.UpgradeController(owner)
		case 16:
			upgradeErr = authority.UpgradeNativeModelRouting(owner)
		case 17:
			upgradeErr = authority.UpgradeNativeIntegration(owner)
		case 18:
			upgradeErr = authority.UpgradeNativeExternal(owner)
		case 19:
			upgradeErr = authority.UpgradeNativeRoleRegistry(owner)
		default:
			t.Fatalf("no synthetic authority migration path from schema %d", snapshot.AuthorityVersion)
		}
		if upgradeErr != nil {
			t.Fatalf("synthetic explicit schema migration from %d: %v", snapshot.AuthorityVersion, upgradeErr)
		}
	}
}
