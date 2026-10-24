package composition

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/cloud/internal/migrations"
	"github.com/mlnomadpy/dacli/cloud/internal/tenant"
	"github.com/mlnomadpy/dacli/cloud/internal/tenantrepo"
	"github.com/mlnomadpy/dacli/internal/application"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/rolebundle"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/team"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

// TestHostedPostgresRoleManagement composes a real tenant-scoped exact export
// with independently provisioned local trust and explicit local selection.
func TestHostedPostgresRoleManagement(t *testing.T) {
	if os.Getenv("DACLI_QUALIFY_HOSTED_POSTGRES") != "1" {
		t.Skip("run scripts/qualify-hosted-postgres.sh for disposable real PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 110*time.Second)
	defer cancel()
	const ownerDSN = "postgres://qualification_owner@/qualification_role_management?host=/var/run/postgresql&sslmode=disable"
	const appDSN = "postgres://qualification_app@/qualification_role_management?host=/var/run/postgresql&sslmode=disable"
	ownerDB, err := sql.Open("postgres", ownerDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ownerDB.Close() }()
	catalog, err := migrations.Load(os.DirFS("/qualification/migrations"))
	if err != nil || len(catalog) != 12 {
		t.Fatalf("role management qualification migration catalog=%d err=%v", len(catalog), err)
	}
	if err := migrations.Run(ctx, migrations.SQLStore{DB: ownerDB}, catalog); err != nil {
		t.Fatal(err)
	}
	grantRoleFixture(t, ctx, ownerDB)
	for _, org := range []string{"tenant-a", "tenant-b"} {
		pgCompositionSeed(t, ctx, ownerDB, org)
	}
	seedRoleTeamAndAdmin(t, ctx, ownerDB)
	appDB, err := sql.Open("postgres", appDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = appDB.Close() }()
	serverPublic, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cfg := pgCompositionConfig(t, appDSN, serverPublic)
	runtime, err := Open(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = runtime.Close() }()
	repo, err := tenantrepo.Open(ctx, appDB)
	if err != nil {
		t.Fatal(err)
	}
	device := pgCompositionEnroll(t, ctx, repo, "tenant-a", "device-role-lifecycle")
	publisherPublic, publisherPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ id, team, name string }{{"catalog-org", "", "Organization roles"}, {"catalog-team", "team-a", "Team roles"}, {"catalog-project", "team-a", "Project roles"}} {
		pgHostedCreateCatalog(t, runtime, device, item.id, item.team, item.name)
		pgHostedEnrollPublisher(t, runtime, device, item.id, "publisher", publisherPublic)
	}
	requestedRole := team.Role{Name: "builder", Summary: "Hosted qualification role", Grant: "rw", Scope: []string{"src/**"}, Prompt: "Build with reviewed instructions."}
	local := newRoleLifecycleWorkspace(t, requestedRole, publisherPublic)
	canonicalRoles, err := store.ReadCheckpointRoles(local.workspace)
	if err != nil || len(canonicalRoles) != 1 {
		t.Fatalf("read locally persisted canonical role: count=%d err=%v", len(canonicalRoles), err)
	}
	canonical := canonicalRoles[0]
	sign := func(version string) rolebundle.Release {
		t.Helper()
		release, signErr := rolebundle.Sign(rolebundle.ReleaseContent{SchemaVersion: 1, Version: version, Role: canonical}, "publisher", publisherPrivate)
		if signErr != nil {
			t.Fatal(signErr)
		}
		return release
	}
	orgRole := pgHostedSignedRelease(t, "role-org", "1.0.0", "publisher", publisherPrivate)
	teamRole := pgHostedSignedRelease(t, "role-team", "1.0.0", "publisher", publisherPrivate)
	v1, v2, v3 := sign("1.0.0"), sign("2.0.0"), sign("3.0.0")
	if !store.RoleFileMatches(v1, canonicalRoles) {
		t.Fatal("hosted release fixture does not exactly match the separately created local canonical role")
	}
	var projectStatus1 rolebundle.LifecycleStatus
	for _, pair := range []struct {
		catalog string
		release rolebundle.Release
	}{{"catalog-org", orgRole}, {"catalog-team", teamRole}, {"catalog-project", v1}} {
		pgHostedImportCatalog(t, runtime, device, pair.catalog, pair.release)
		status := pgHostedStatus(t, pair.release.Content.Role.Name, pair.release, "active", 1, "", publisherPrivate)
		pgHostedLifecycleCatalog(t, runtime, device, pair.catalog, pair.release, 1, tenant.HostedReleased, &status)
		switch pair.catalog {
		case "catalog-org":
			pinRoleCatalog(t, runtime, device, "role-org", tenant.PinOrganization, "tenant-a", pair.catalog, pair.release, 0)
		case "catalog-team":
			pinRoleCatalog(t, runtime, device, "role-team", tenant.PinTeam, "team-a", pair.catalog, pair.release, 0)
		case "catalog-project":
			projectStatus1 = status
			pinRoleCatalog(t, runtime, device, "builder", tenant.PinProject, "project", pair.catalog, pair.release, 0)
		}
	}
	for _, tc := range []struct {
		role string
		want tenant.PinTargetKind
	}{{"builder", tenant.PinProject}, {"role-team", tenant.PinTeam}, {"role-org", tenant.PinOrganization}} {
		response := pgRoleRequest(t, runtime, device, http.MethodGet, "/v1/role-registry/projects/project/roles/"+tc.role, "")
		var resolved tenant.ResolvedRole
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &resolved) != nil || resolved.Source.Kind != tc.want {
			t.Fatalf("hosted %s provenance=%s, want %s: %d %s", tc.role, resolved.Source.Kind, tc.want, response.Code, response.Body.String())
		}
	}
	// Extend the project catalog with an expired intermediate statement and a
	// fresh newest version. The selected old v1 remains current and exportable.
	pgHostedImportCatalog(t, runtime, device, "catalog-project", v2)
	digest1, err := rolebundle.LifecycleDigest(projectStatus1)
	if err != nil {
		t.Fatal(err)
	}
	status2 := pgHostedStatusFor(t, "builder", v2, "active", 2, digest1, 200*time.Millisecond, publisherPrivate)
	pgHostedLifecycleCatalog(t, runtime, device, "catalog-project", v2, 3, tenant.HostedReleased, &status2)
	time.Sleep(time.Until(status2.ExpiresAt) + 30*time.Millisecond)
	pgHostedImportCatalog(t, runtime, device, "catalog-project", v3)
	digest2, err := rolebundle.LifecycleDigest(status2)
	if err != nil {
		t.Fatal(err)
	}
	status3 := pgHostedStatus(t, "builder", v3, "active", 3, digest2, publisherPrivate)
	pgHostedLifecycleCatalog(t, runtime, device, "catalog-project", v3, 5, tenant.HostedReleased, &status3)
	resolvedResponse := pgRoleRequest(t, runtime, device, http.MethodGet, "/v1/role-registry/projects/project/roles/builder", "")
	var selected tenant.ResolvedRole
	if resolvedResponse.Code != http.StatusOK || json.Unmarshal(resolvedResponse.Body.Bytes(), &selected) != nil || selected.Source.Kind != tenant.PinProject || selected.Release.ContentDigest != v1.ContentDigest {
		t.Fatalf("project exact selected export provenance changed: %d %+v %s", resolvedResponse.Code, selected.Source, resolvedResponse.Body.String())
	}
	exportResponse := pgRoleRequest(t, runtime, device, http.MethodGet, "/v1/role-registry/releases/catalog-project/builder/1.0.0/"+v1.ContentDigest, "")
	var hosted tenant.HostedRoleExport
	if exportResponse.Code != http.StatusOK || json.Unmarshal(exportResponse.Body.Bytes(), &hosted) != nil || hosted.Selected.ContentDigest != v1.ContentDigest || len(hosted.Releases) != 3 || len(hosted.Lifecycle) != 6 {
		t.Fatalf("hosted export omitted signed history or exact selection: %d %+v %s", exportResponse.Code, hosted.Selected, exportResponse.Body.String())
	}
	localExport, err := application.DecodeNativeRoleRegistryCatalogExport(exportResponse.Body.Bytes())
	if err != nil || localExport.Selected.ContentDigest != v1.ContentDigest || len(localExport.Lifecycle) != 6 {
		t.Fatalf("actual hosted export JSON did not decode through the local import contract: %+v %v", localExport.Selected, err)
	}
	localOwner, err := application.OpenNativeRoleRegistryOwner(local.config, "core")
	if err != nil {
		t.Fatal(err)
	}
	importReceipt, err := localOwner.ImportCatalogExport(localExport, time.Now().UTC())
	if err != nil || importReceipt.Selected || importReceipt.ReleaseCount != 3 || importReceipt.LifecycleCount != 6 {
		t.Fatalf("independently trusted local import failed or activated: %+v %v", importReceipt, err)
	}
	if _, err := localOwner.Verify("builder", v1.Content.Version, v1.ContentDigest, time.Now().UTC()); err != nil {
		t.Fatalf("old exact release with fresh lifecycle failed local verification after unrelated expiry: %v", err)
	}
	if _, err := localOwner.Verify("builder", v2.Content.Version, v2.ContentDigest, time.Now().UTC()); err == nil {
		t.Fatal("expired intermediate hosted lifecycle was treated as current locally")
	}
	localPin, err := localOwner.Select("builder", v1.Content.Version, v1.ContentDigest, 0, "", "", "imported hosted exact release", "pin", time.Now().UTC())
	if err != nil || localPin.Generation != 1 {
		t.Fatalf("explicit local pin failed: %+v %v", localPin, err)
	}
	// Remote default changes to v3; local selection remains at v1 until the
	// owner explicitly upgrades it using the exact local history.
	localBeforeRemoteChange, err := os.ReadFile(local.workspace.ProjectPath("core"))
	if err != nil {
		t.Fatal(err)
	}
	pinRoleCatalog(t, runtime, device, "builder", tenant.PinProject, "project", "catalog-project", v3, 1)
	localPins, err := local.registry.ReadPins("core")
	if err != nil || localPins.Pins["builder"].Version != v1.Content.Version || localPins.Generation != 1 {
		t.Fatalf("remote default change auto-activated locally: %+v %v", localPins, err)
	}
	localAfterRemoteChange, _ := os.ReadFile(local.workspace.ProjectPath("core"))
	if !bytes.Equal(localBeforeRemoteChange, localAfterRemoteChange) {
		t.Fatal("hosted default change wrote local project selection")
	}
	if _, err := localOwner.Select("builder", v3.Content.Version, v3.ContentDigest, 1, v1.Content.Version, v1.ContentDigest, "explicit hosted version upgrade", "upgrade", time.Now().UTC()); err != nil {
		t.Fatalf("explicit local upgrade failed: %v", err)
	}
	if _, err := localOwner.Select("builder", v1.Content.Version, v1.ContentDigest, 2, v3.Content.Version, v3.ContentDigest, "restore prior local version", "rollback", time.Now().UTC()); err != nil {
		t.Fatalf("explicit local rollback failed: %v", err)
	}
	roleBytes, _ := os.ReadFile(local.workspace.RolePath("builder"))
	if !bytes.Equal(local.roleBefore, roleBytes) || len(localBeforeRemoteChange) == 0 {
		t.Fatal("local transfer or selection changed canonical role instructions")
	}
}

type localRoleLifecycleFixture struct {
	workspace  *workspace.Workspace
	config     string
	roleBefore []byte
	registry   *store.RoleRegistry
}

func newRoleLifecycleWorkspace(t *testing.T, role team.Role, publisher ed25519.PublicKey) localRoleLifecycleFixture {
	t.Helper()
	w, err := workspace.Init(t.TempDir(), "role-lifecycle-local")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProject(w, "owner:test", "Core", "core", "hosted transfer", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateRole(w, "owner:test", role); err != nil {
		t.Fatal(err)
	}
	registryDir := filepath.Join(w.Root, workspace.Dir, "role-registry")
	if err := os.MkdirAll(registryDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(registryDir, "publishers.json"), mustJSON(t, map[string][]byte{"publisher": publisher}), 0600); err != nil {
		t.Fatal(err)
	}
	registry, err := store.NewRoleRegistry(w, map[string]ed25519.PublicKey{"publisher": publisher})
	if err != nil {
		t.Fatal(err)
	}
	authorityPath := filepath.Join(t.TempDir(), "authority.json")
	authority, err := sessions.Open(authorityPath, sessions.Binding{Workspace: w.ID, Installation: "hosted-transfer-local"})
	if err != nil {
		t.Fatal(err)
	}
	ownerToken, err := authority.Bootstrap()
	if err != nil {
		t.Fatal(err)
	}
	credentialPath := filepath.Join(filepath.Dir(authorityPath), "owner.token")
	if err := os.WriteFile(credentialPath, []byte(ownerToken), 0600); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(filepath.Dir(authorityPath), "owner.json")
	configBytes := mustJSON(t, map[string]any{"schema_version": 1, "workspace": w.Root, "authority": authorityPath, "installation_id": "hosted-transfer-local", "owner_credential_file": credentialPath})
	if err := os.WriteFile(config, configBytes, 0600); err != nil {
		t.Fatal(err)
	}
	upgradeCompositionAuthorityTo20(t, authorityPath, w.ID, "hosted-transfer-local", ownerToken)
	roleBefore, err := os.ReadFile(w.RolePath("builder"))
	if err != nil {
		t.Fatal(err)
	}
	return localRoleLifecycleFixture{workspace: w, config: config, roleBefore: roleBefore, registry: registry}
}

func upgradeCompositionAuthorityTo20(t *testing.T, path, workspaceID, installation, ownerToken string) {
	t.Helper()
	for {
		authority, err := sessions.OpenCurrent(path, sessions.Binding{Workspace: workspaceID, Installation: installation})
		if err != nil {
			t.Fatal(err)
		}
		snapshot, err := authority.ObserveStorage(ownerToken)
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
			upgradeErr = authority.UpgradeSwarms(ownerToken)
		case 7:
			upgradeErr = authority.UpgradeDispatchIntents(ownerToken)
		case 8:
			upgradeErr = authority.UpgradeLifecycle(ownerToken)
		case 9:
			upgradeErr = authority.UpgradeRecovery(ownerToken)
		case 10:
			upgradeErr = authority.UpgradePreflight(ownerToken)
		case 11:
			upgradeErr = authority.UpgradeCheckpoints(ownerToken)
		case 12:
			upgradeErr = authority.UpgradeBudgets(ownerToken)
		case 13:
			upgradeErr = authority.UpgradeNativeAcceptance(ownerToken)
		case 14:
			upgradeErr = authority.UpgradeNativePolicy(ownerToken)
		case 15:
			upgradeErr = authority.UpgradeController(ownerToken)
		case 16:
			upgradeErr = authority.UpgradeNativeModelRouting(ownerToken)
		case 17:
			upgradeErr = authority.UpgradeNativeIntegration(ownerToken)
		case 18:
			upgradeErr = authority.UpgradeNativeExternal(ownerToken)
		case 19:
			upgradeErr = authority.UpgradeNativeRoleRegistry(ownerToken)
		default:
			t.Fatalf("no local schema upgrade path from %d", snapshot.AuthorityVersion)
		}
		if upgradeErr != nil {
			t.Fatalf("local owner schema upgrade %d: %v", snapshot.AuthorityVersion, upgradeErr)
		}
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
