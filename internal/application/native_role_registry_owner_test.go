package application

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/rolebundle"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/team"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

type roleRegistryOwnerFixture struct {
	w             *workspace.Workspace
	owner         string
	config        string
	authorityPath string
	registry      *store.RoleRegistry
	private       ed25519.PrivateKey
	now           time.Time
}

func newRoleRegistryOwnerFixture(t *testing.T) roleRegistryOwnerFixture {
	t.Helper()
	w, err := workspace.Init(t.TempDir(), "role-management")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProject(w, "owner", "Core", "core", "role registry fixture", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateRole(w, "owner", team.Role{Name: "builder", Summary: "build", Grant: "rw", Scope: []string{"src/**"}, Prompt: "Build carefully."}); err != nil {
		t.Fatal(err)
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	registryDir := filepath.Join(w.Root, workspace.Dir, "role-registry")
	if err := os.MkdirAll(registryDir, 0700); err != nil {
		t.Fatal(err)
	}
	keyring, err := json.Marshal(map[string][]byte{"publisher": public})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(registryDir, "publishers.json"), keyring, 0600); err != nil {
		t.Fatal(err)
	}
	registry, err := store.NewRoleRegistry(w, map[string]ed25519.PublicKey{"publisher": public})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "authority.json")
	a, err := sessions.Open(path, sessions.Binding{Workspace: w.ID, Installation: "role-owner-installation"})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := a.Bootstrap()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	credential := filepath.Join(dir, "owner.token")
	if err := os.WriteFile(credential, []byte(owner), 0600); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(dir, "owner.json")
	c := recoveryOwnerConfig{SchemaVersion: 1, Workspace: w.Root, Authority: path, InstallationID: "role-owner-installation", OwnerCredentialFile: credential}
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, data, 0600); err != nil {
		t.Fatal(err)
	}
	return roleRegistryOwnerFixture{w: w, owner: owner, config: config, authorityPath: path, registry: registry, private: private, now: time.Now().UTC().Truncate(time.Second)}
}

func (f roleRegistryOwnerFixture) addRelease(t *testing.T, version string, generation uint64, previous string) rolebundle.Release {
	t.Helper()
	roles, err := store.ReadCheckpointRoles(f.w)
	if err != nil || len(roles) != 1 {
		t.Fatalf("read canonical roles: count=%d err=%v", len(roles), err)
	}
	release, err := rolebundle.Sign(rolebundle.ReleaseContent{SchemaVersion: rolebundle.SchemaVersion, Version: version, Role: roles[0]}, "publisher", f.private)
	if err != nil {
		t.Fatal(err)
	}
	status, err := rolebundle.SignStatus(rolebundle.LifecycleStatus{SchemaVersion: rolebundle.SchemaVersion, RoleID: "builder", Version: version, ContentDigest: release.ContentDigest, Generation: generation, PreviousStatusDigest: previous, State: "active", IssuedAt: f.now, ExpiresAt: f.now.Add(time.Hour), PublisherKeyID: "publisher"}, f.private)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.registry.ImportRoleRelease(release, status, f.now); err != nil {
		t.Fatal(err)
	}
	return release
}

func TestNativeRoleRegistryOwnerRequiresExplicitOwnerAndExactWorkspace(t *testing.T) {
	f := newRoleRegistryOwnerFixture(t)
	if _, err := OpenNativeRoleRegistryOwner(f.config, "missing-project"); err == nil {
		t.Fatal("unknown project accepted")
	}
	wrongWorkspace, err := workspace.Init(t.TempDir(), "wrong-workspace")
	if err != nil {
		t.Fatal(err)
	}
	var config recoveryOwnerConfig
	data, err := os.ReadFile(f.config)
	if err != nil || json.Unmarshal(data, &config) != nil {
		t.Fatal("read owner fixture")
	}
	config.Workspace = wrongWorkspace.Root
	wrongConfig := filepath.Join(t.TempDir(), "wrong-workspace-owner.json")
	data, err = json.Marshal(config)
	if err != nil || os.WriteFile(wrongConfig, data, 0600) != nil {
		t.Fatal("write wrong-workspace profile")
	}
	if _, err := OpenNativeRoleRegistryOwner(wrongConfig, "core"); !errors.Is(err, sessions.ErrDenied) {
		t.Fatalf("authority was accepted for a different workspace: %v", err)
	}
	config.Workspace = f.w.Root
	config.OwnerCredentialFile = filepath.Join(t.TempDir(), "worker.token")
	if err := os.WriteFile(config.OwnerCredentialFile, []byte("worker-credential-not-owner"), 0600); err != nil {
		t.Fatal(err)
	}
	wrongCredential := filepath.Join(t.TempDir(), "wrong-owner.json")
	data, err = json.Marshal(config)
	if err != nil || os.WriteFile(wrongCredential, data, 0600) != nil {
		t.Fatal("write wrong-credential profile")
	}
	if _, err := OpenNativeRoleRegistryOwner(wrongCredential, "core"); !errors.Is(err, sessions.ErrDenied) {
		t.Fatalf("worker credential opened owner profile: %v", err)
	}
}

func TestNativeRoleRegistryOwnerReauthenticatesChangedPrivateConfigAndCredential(t *testing.T) {
	f := newRoleRegistryOwnerFixture(t)
	release := f.addRelease(t, "1.0.0", 1, "")
	owner, err := OpenNativeRoleRegistryOwner(f.config, "core")
	if err != nil {
		t.Fatal(err)
	}
	var config recoveryOwnerConfig
	configBytes, err := os.ReadFile(f.config)
	if err != nil || json.Unmarshal(configBytes, &config) != nil {
		t.Fatal("read owner fixture config")
	}
	if err := os.WriteFile(config.OwnerCredentialFile, []byte("replaced-worker-credential"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Verify("builder", release.Content.Version, release.ContentDigest, f.now); !errors.Is(err, sessions.ErrDenied) {
		t.Fatalf("capability accepted replaced owner credential: %v", err)
	}
	if err := os.WriteFile(config.OwnerCredentialFile, []byte(f.owner), 0600); err != nil {
		t.Fatal(err)
	}
	owner, err = OpenNativeRoleRegistryOwner(f.config, "core")
	if err != nil {
		t.Fatal(err)
	}
	config.Workspace = filepath.Join(f.w.Root, "changed-workspace-binding")
	changedConfig, err := json.Marshal(config)
	if err != nil || os.WriteFile(f.config, changedConfig, 0600) != nil {
		t.Fatal("replace owner config")
	}
	if _, err := owner.Verify("builder", release.Content.Version, release.ContentDigest, f.now); !errors.Is(err, sessions.ErrDenied) {
		t.Fatalf("capability accepted replaced private owner config: %v", err)
	}
}

func TestNativeRoleRegistryOwnerPreSchema20RefusesWithoutProjectWrite(t *testing.T) {
	f := newRoleRegistryOwnerFixture(t)
	release := f.addRelease(t, "1.0.0", 1, "")
	owner, err := OpenNativeRoleRegistryOwner(f.config, "core")
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(f.w.ProjectPath("core"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = owner.Select("builder", release.Content.Version, release.ContentDigest, 0, "", "", "initial pin", "pin", f.now)
	if !errors.Is(err, sessions.ErrDenied) {
		t.Fatalf("pre-schema-20 authority selected a registry pin: %v", err)
	}
	after, err := os.ReadFile(f.w.ProjectPath("core"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("refused selection changed project bytes: %v", err)
	}
}

func TestNativeRoleRegistryImportReturnsRevocationReceiptWithoutSelection(t *testing.T) {
	f := newRoleRegistryOwnerFixture(t)
	release := f.addRelease(t, "1.0.0", 1, "")
	active, err := rolebundle.SignStatus(rolebundle.LifecycleStatus{SchemaVersion: rolebundle.SchemaVersion, RoleID: "builder", Version: release.Content.Version, ContentDigest: release.ContentDigest, Generation: 1, State: "active", IssuedAt: f.now, ExpiresAt: f.now.Add(time.Hour), PublisherKeyID: "publisher"}, f.private)
	if err != nil {
		t.Fatal(err)
	}
	previous, err := rolebundle.LifecycleDigest(active)
	if err != nil {
		t.Fatal(err)
	}
	revoked, err := rolebundle.SignStatus(rolebundle.LifecycleStatus{SchemaVersion: rolebundle.SchemaVersion, RoleID: "builder", Version: release.Content.Version, ContentDigest: release.ContentDigest, Generation: 2, PreviousStatusDigest: previous, State: "revoked", IssuedAt: f.now.Add(time.Minute), ExpiresAt: f.now.Add(time.Hour), PublisherKeyID: "publisher"}, f.private)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := OpenNativeRoleRegistryOwner(f.config, "core")
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := owner.Import(release, revoked, f.now.Add(time.Minute))
	if err != nil || receipt.LifecycleState != "revoked" || receipt.Selected || receipt.LifecycleGeneration != 2 {
		t.Fatalf("revocation import did not return truthful nonselecting receipt: %+v %v", receipt, err)
	}
	pins, err := f.registry.ReadPins("core")
	if err != nil || pins.Generation != 0 || len(pins.Pins) != 0 || len(pins.History) != 0 {
		t.Fatalf("revocation import changed project selection: %+v %v", pins, err)
	}
}

func TestNativeRoleRegistryOwnerSchema20SelectionUpgradeAndRollback(t *testing.T) {
	f := newRoleRegistryOwnerFixture(t)
	v1 := f.addRelease(t, "1.0.0", 1, "")
	firstStatus := rolebundle.LifecycleStatus{SchemaVersion: 1, RoleID: "builder", Version: v1.Content.Version, ContentDigest: v1.ContentDigest, Generation: 1, State: "active", IssuedAt: f.now, ExpiresAt: f.now.Add(time.Hour), PublisherKeyID: "publisher"}
	firstStatus, err := rolebundle.SignStatus(firstStatus, f.private)
	if err != nil {
		t.Fatal(err)
	}
	previous, err := rolebundle.LifecycleDigest(firstStatus)
	if err != nil {
		t.Fatal(err)
	}
	v2 := f.addRelease(t, "2.0.0", 2, previous)
	upgradeAuthorityTo20(t, f, f.owner)
	owner, err := OpenNativeRoleRegistryOwner(f.config, "core")
	if err != nil {
		t.Fatal(err)
	}
	rolePath := f.w.RolePath("builder")
	roleBefore, err := os.ReadFile(rolePath)
	if err != nil {
		t.Fatal(err)
	}
	first, err := owner.Select("builder", v1.Content.Version, v1.ContentDigest, 0, "", "", "initial pin", "pin", f.now)
	if err != nil || first.Generation != 1 {
		t.Fatalf("schema-20 initial pin failed: %+v %v", first, err)
	}
	current := first.Pins["builder"]
	upgraded, err := owner.Select("builder", v2.Content.Version, v2.ContentDigest, 1, current.Version, current.Digest, "higher signed release", "upgrade", f.now)
	if err != nil || upgraded.Generation != 2 {
		t.Fatalf("schema-20 upgrade failed: %+v %v", upgraded, err)
	}
	current = upgraded.Pins["builder"]
	projectBeforeDowngrade, err := os.ReadFile(f.w.ProjectPath("core"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Select("builder", v1.Content.Version, v1.ContentDigest, 2, current.Version, current.Digest, "reject downgrade mislabeled as upgrade", "upgrade", f.now); !errors.Is(err, store.ErrRoleRegistry) {
		t.Fatalf("public owner API accepted a downgrade as upgrade: %v", err)
	}
	projectAfterDowngrade, err := os.ReadFile(f.w.ProjectPath("core"))
	if err != nil || !bytes.Equal(projectBeforeDowngrade, projectAfterDowngrade) {
		t.Fatalf("refused downgrade-as-upgrade changed project bytes: %v", err)
	}
	rolledBack, err := owner.Select("builder", v1.Content.Version, v1.ContentDigest, 2, current.Version, current.Digest, "restore prior release", "rollback", f.now)
	if err != nil || rolledBack.Generation != 3 || rolledBack.Pins["builder"].Version != "1.0.0" {
		t.Fatalf("schema-20 historical rollback failed: %+v %v", rolledBack, err)
	}
	roleAfter, err := os.ReadFile(rolePath)
	if err != nil || !bytes.Equal(roleBefore, roleAfter) {
		t.Fatalf("metadata selection changed canonical role file: %v", err)
	}
}

func TestNativeRoleRegistryDiffReportsSignedAndCanonicalDifferencesDeterministically(t *testing.T) {
	f := newRoleRegistryOwnerFixture(t)
	v1 := f.addRelease(t, "1.0.0", 1, "")
	firstStatus := rolebundle.LifecycleStatus{SchemaVersion: 1, RoleID: "builder", Version: v1.Content.Version, ContentDigest: v1.ContentDigest, Generation: 1, State: "active", IssuedAt: f.now, ExpiresAt: f.now.Add(time.Hour), PublisherKeyID: "publisher"}
	firstStatus, err := rolebundle.SignStatus(firstStatus, f.private)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.registry.Pin("core", "builder", v1.Content.Version, v1.ContentDigest, "owner:test", "fixture pin", f.now); err != nil {
		t.Fatal(err)
	}
	previous, err := rolebundle.LifecycleDigest(firstStatus)
	if err != nil {
		t.Fatal(err)
	}
	roles, err := store.ReadCheckpointRoles(f.w)
	if err != nil {
		t.Fatal(err)
	}
	targetRole := roles[0]
	targetRole.Grant = "ro"
	targetRole.Model = "model-target"
	targetRole.Prompt = "Target signed prompt."
	targetRole.Skills = []string{"target-skill"}
	target, err := rolebundle.Sign(rolebundle.ReleaseContent{SchemaVersion: 1, Version: "2.0.0", Role: targetRole, References: []rolebundle.Reference{{Kind: "policy", ID: "native-swarm", Version: "policy-v2", Digest: "sha256:" + string(bytes.Repeat([]byte("a"), 64))}}}, "publisher", f.private)
	if err != nil {
		t.Fatal(err)
	}
	status, err := rolebundle.SignStatus(rolebundle.LifecycleStatus{SchemaVersion: 1, RoleID: "builder", Version: target.Content.Version, ContentDigest: target.ContentDigest, Generation: 2, PreviousStatusDigest: previous, State: "active", IssuedAt: f.now, ExpiresAt: f.now.Add(time.Hour), PublisherKeyID: "publisher"}, f.private)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.registry.ImportRoleRelease(target, status, f.now); err != nil {
		t.Fatal(err)
	}
	owner, err := OpenNativeRoleRegistryOwner(f.config, "core")
	if err != nil {
		t.Fatal(err)
	}
	projectBefore, err := os.ReadFile(f.w.ProjectPath("core"))
	if err != nil {
		t.Fatal(err)
	}
	one, err := owner.Diff("builder", target.Content.Version, target.ContentDigest, f.now)
	if err != nil {
		t.Fatal(err)
	}
	two, err := owner.Diff("builder", target.Content.Version, target.ContentDigest, f.now)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(one, two) || !one.CurrentReleaseAvailable || !one.CurrentLifecycleUsable || one.CanonicalRoleMatches {
		t.Fatalf("diff was not deterministic or failed to flag drift: first=%+v second=%+v", one, two)
	}
	fields := map[string]bool{}
	for _, change := range one.Changes {
		fields[change.Field] = true
	}
	for _, field := range []string{"role.grant", "role.model", "role.prompt", "role.skills", "references", "content.version"} {
		if !fields[field] {
			t.Errorf("diff omitted signed field %q: %+v", field, one.Changes)
		}
	}
	if _, err := owner.Verify("builder", target.Content.Version, target.ContentDigest, f.now); err != nil {
		t.Fatalf("verify should expose exact signed release and canonical mismatch: %v", err)
	}
	projectAfter, err := os.ReadFile(f.w.ProjectPath("core"))
	if err != nil || !bytes.Equal(projectBefore, projectAfter) {
		t.Fatalf("read-only diff/verify changed project bytes: %v", err)
	}
}

func upgradeAuthorityTo20(t *testing.T, f roleRegistryOwnerFixture, owner string) {
	t.Helper()
	binding := sessions.Binding{Workspace: f.w.ID, Installation: "role-owner-installation"}
	for {
		authority, err := sessions.OpenCurrent(f.authorityPath, binding)
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
		var errUpgrade error
		switch snapshot.AuthorityVersion {
		case 6:
			errUpgrade = authority.UpgradeSwarms(owner)
		case 7:
			errUpgrade = authority.UpgradeDispatchIntents(owner)
		case 8:
			errUpgrade = authority.UpgradeLifecycle(owner)
		case 9:
			errUpgrade = authority.UpgradeRecovery(owner)
		case 10:
			errUpgrade = authority.UpgradePreflight(owner)
		case 11:
			errUpgrade = authority.UpgradeCheckpoints(owner)
		case 12:
			errUpgrade = authority.UpgradeBudgets(owner)
		case 13:
			errUpgrade = authority.UpgradeNativeAcceptance(owner)
		case 14:
			errUpgrade = authority.UpgradeNativePolicy(owner)
		case 15:
			errUpgrade = authority.UpgradeController(owner)
		case 16:
			errUpgrade = authority.UpgradeNativeModelRouting(owner)
		case 17:
			errUpgrade = authority.UpgradeNativeIntegration(owner)
		case 18:
			errUpgrade = authority.UpgradeNativeExternal(owner)
		case 19:
			errUpgrade = authority.UpgradeNativeRoleRegistry(owner)
		default:
			t.Fatalf("no explicit migration path from schema %d", snapshot.AuthorityVersion)
		}
		if errUpgrade != nil {
			t.Fatalf("explicit owner migration from schema %d: %v", snapshot.AuthorityVersion, errUpgrade)
		}
	}
}

func TestRoleVersionGreaterUsesSemVerPrecedenceWithoutIntegerOverflow(t *testing.T) {
	cases := []struct {
		target, current string
		want            bool
	}{
		{"1.0.0-alpha.1", "1.0.0-alpha", true},
		{"1.0.0-alpha", "1.0.0-alpha.0", false},
		{"1.0.0", "1.0.0-rc.9", true},
		{"1.0.0+build.2", "1.0.0+build.1", false},
		{"1.0.0-9007199254740993", "1.0.0-9007199254740992", true},
		{"999999999999999999999999.0.0", "999999999999999999999998.0.0", true},
		{"1.0.0-alpha", "1.0.0-1", true},
		{"1.0.0-1", "1.0.0-alpha", false},
	}
	for _, tc := range cases {
		if got := store.RoleVersionGreater(tc.target, tc.current); got != tc.want {
			t.Errorf("RoleVersionGreater(%q, %q) = %t, want %t", tc.target, tc.current, got, tc.want)
		}
	}
}
