package composition

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/cloud/internal/deviceoauth"
	"github.com/mlnomadpy/dacli/cloud/internal/devicesession"
	"github.com/mlnomadpy/dacli/cloud/internal/migrations"
	"github.com/mlnomadpy/dacli/cloud/internal/tenant"
	"github.com/mlnomadpy/dacli/cloud/internal/tenantrepo"
	"github.com/mlnomadpy/dacli/internal/rolebundle"
	"github.com/mlnomadpy/dacli/internal/team"
)

// TestHostedPostgresRoleRegistry runs the public API and tenant repository
// against the restricted application role in the disposable offline fixture.
func TestHostedPostgresRoleRegistry(t *testing.T) {
	if os.Getenv("DACLI_QUALIFY_HOSTED_POSTGRES") != "1" {
		t.Skip("run scripts/qualify-hosted-postgres.sh for disposable real PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 110*time.Second)
	defer cancel()
	const ownerDSN = "postgres://qualification_owner@/qualification_roles?host=/var/run/postgresql&sslmode=disable"
	const appDSN = "postgres://qualification_app@/qualification_roles?host=/var/run/postgresql&sslmode=disable"
	owner, err := sql.Open("postgres", ownerDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = owner.Close() }()
	catalog, err := migrations.Load(os.DirFS("/qualification/migrations"))
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog) != 12 {
		t.Fatalf("role qualification requires schema12, got %d migrations", len(catalog))
	}
	if err = migrations.Run(ctx, migrations.SQLStore{DB: owner}, catalog[:11]); err != nil {
		t.Fatal(err)
	}
	grantRoleFixture(t, ctx, owner)
	app, err := sql.Open("postgres", appDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Close() }()
	serverPublic, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cfg := pgCompositionConfig(t, appDSN, serverPublic)
	if old, openErr := Open(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil))); openErr == nil {
		_ = old.Close()
		t.Fatal("schema11 startup unexpectedly succeeded")
	}
	assertMigrationCount(t, ctx, owner, 11)
	if err = migrations.Run(ctx, migrations.SQLStore{DB: owner}, catalog); err != nil {
		t.Fatal(err)
	}
	grantRoleFixture(t, ctx, owner)
	for _, org := range []string{"tenant-a", "tenant-b"} {
		pgCompositionSeed(t, ctx, owner, org)
	}
	seedRoleTeamAndAdmin(t, ctx, owner)
	if _, err = owner.ExecContext(ctx, `ALTER TABLE public.controlplane_role_catalogs NO FORCE ROW LEVEL SECURITY`); err != nil {
		t.Fatal(err)
	}
	if missing, openErr := Open(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil))); openErr == nil {
		_ = missing.Close()
		t.Fatal("startup accepted role catalogs without forced RLS")
	}
	if _, err = owner.ExecContext(ctx, `ALTER TABLE public.controlplane_role_catalogs FORCE ROW LEVEL SECURITY`); err != nil {
		t.Fatal(err)
	}
	app.SetMaxOpenConns(8)
	repo, err := tenantrepo.Open(ctx, app)
	if err != nil {
		t.Fatal(err)
	}
	ownerDevice := pgCompositionEnroll(t, ctx, repo, "tenant-a", "device-role-owner")
	adminDevice := pgHostedEnrollAccount(t, ctx, repo, "tenant-a", "account-admin", "device-role-admin")
	otherDevice := pgCompositionEnroll(t, ctx, repo, "tenant-b", "device-role-other")
	runtime, err := Open(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = runtime.Close() }()
	publisherPublic, publisherPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if got := pgRoleRequest(t, runtime, ownerDevice, http.MethodPost, "/v1/role-registry/catalogs", `{"catalog_id":"catalog-a","team_id":"team-a","name":"Team roles"}`); got.Code != http.StatusCreated {
		t.Fatalf("owner catalog create: %d %s", got.Code, got.Body.String())
	}
	publisherBody, _ := json.Marshal(struct {
		KeyID     string            `json:"key_id"`
		PublicKey ed25519.PublicKey `json:"public_key"`
	}{"publisher-a", publisherPublic})
	if got := pgRoleRequest(t, runtime, adminDevice, http.MethodPost, "/v1/role-registry/catalogs/catalog-a/publishers", string(publisherBody)); got.Code != http.StatusCreated {
		t.Fatalf("administrator publisher enrollment: %d %s", got.Code, got.Body.String())
	}

	keys := map[string]ed25519.PublicKey{"publisher-a": publisherPublic}
	roleNames := []string{"role-org", "role-team", "role-project"}
	releases := make(map[string]rolebundle.Release)
	activeStatuses := make(map[string]rolebundle.LifecycleStatus)
	for _, roleID := range roleNames {
		release := pgHostedSignedRelease(t, roleID, "v1.0.0", "publisher-a", publisherPrivate)
		releases[roleID] = release
		pgHostedImport(t, runtime, ownerDevice, release)
		status := pgHostedStatus(t, roleID, release, "active", 1, "", publisherPrivate)
		activeStatuses[roleID] = status
		pgHostedLifecycle(t, runtime, ownerDevice, release, 1, tenant.HostedReleased, &status)
	}
	duplicate := pgRoleImportResponse(t, runtime, ownerDevice, releases["role-org"])
	if duplicate.Code != http.StatusConflict {
		t.Fatalf("exact duplicate import status=%d body=%s", duplicate.Code, duplicate.Body.String())
	}
	replacement := pgHostedSignedReleaseVariant(t, "role-org", "v1.0.0", "different signed content", "publisher-a", publisherPrivate)
	if replaced := pgRoleImportResponse(t, runtime, ownerDevice, replacement); replaced.Code != http.StatusConflict {
		t.Fatalf("same-version replacement status=%d body=%s", replaced.Code, replaced.Body.String())
	}
	firstRace := pgHostedSignedReleaseVariant(t, "role-race", "v1.0.0", "race content one", "publisher-a", publisherPrivate)
	secondRace := pgHostedSignedReleaseVariant(t, "role-race", "v1.0.0", "race content two", "publisher-a", publisherPrivate)
	var importWG sync.WaitGroup
	importResults := make(chan error, 2)
	for i, value := range []rolebundle.Release{firstRace, secondRace} {
		importWG.Add(1)
		go func(i int, value rolebundle.Release) {
			defer importWG.Done()
			raw, _ := json.Marshal(value)
			key := tenant.ExactReleaseKey{Tenant: "tenant-a", CatalogID: "catalog-a", RoleID: "role-race", Version: value.Content.Version, ContentDigest: value.ContentDigest}
			m := tenant.Mutation{Actor: ownerDeviceAccount(ownerDevice), Device: ownerDevice.device, CorrelationID: "version-race-" + string(rune('a'+i)), OccurredAt: time.Now().UTC()}
			_, e := repo.ImportRoleRelease(ctx, roleIdentity(ownerDevice), m, "catalog-a", tenant.HostedRelease{Key: key, Envelope: raw})
			importResults <- e
		}(i, value)
	}
	importWG.Wait()
	close(importResults)
	importSuccess, importConflict := 0, 0
	for e := range importResults {
		if e == nil {
			importSuccess++
		} else if errors.Is(e, tenantrepo.ErrConflict) {
			importConflict++
		} else {
			t.Fatalf("concurrent version import error=%v", e)
		}
	}
	if importSuccess != 1 || importConflict != 1 {
		t.Fatalf("version CAS winners=%d conflicts=%d", importSuccess, importConflict)
	}
	// A foreign tenant identifier remains foreign all the way into the backend;
	// scope mismatch and PostgreSQL RLS/FKs leave both tenants without writes.
	foreignCatalog := tenant.HostedCatalog{Tenant: "tenant-b", ID: "foreign-catalog", Name: "foreign", Version: 1}
	if err := repo.CreateRoleCatalog(ctx, tenant.VerifiedIdentity{Scope: tenant.Scope{Organization: "tenant-a"}, Account: "account-tenant-a", Device: "device-role-owner", MembershipVersion: 1, PolicyRevision: 1}, tenant.Mutation{Actor: "account-tenant-a", Device: "device-role-owner", CorrelationID: "foreign-catalog", OccurredAt: time.Now().UTC()}, foreignCatalog); err == nil {
		t.Fatal("foreign tenant catalog write succeeded")
	}
	if count := pgRoleCount(t, ctx, app, "tenant-a", "controlplane_role_catalogs"); count != 1 {
		t.Fatalf("tenant-a catalogs after foreign attempt=%d", count)
	}
	if count := pgRoleCount(t, ctx, app, "tenant-b", "controlplane_role_catalogs"); count != 0 {
		t.Fatalf("tenant-b catalogs after foreign attempt=%d", count)
	}

	// Exact pin inheritance is project, current membership team, then organization.
	for _, roleID := range []string{"role-org", "role-team", "role-project"} {
		pinRole(t, runtime, ownerDevice, roleID, tenant.PinOrganization, "tenant-a", releases[roleID], 0)
	}
	pinRole(t, runtime, ownerDevice, "role-team", tenant.PinTeam, "team-a", releases["role-team"], 0)
	pinRole(t, runtime, ownerDevice, "role-project", tenant.PinTeam, "team-a", releases["role-project"], 0)
	pinRole(t, runtime, ownerDevice, "role-project", tenant.PinProject, "project", releases["role-project"], 0)
	secondVersion := pgHostedSignedRelease(t, "role-org", "v2.0.0", "publisher-a", publisherPrivate)
	pgHostedImport(t, runtime, ownerDevice, secondVersion)
	firstStatusDigest, err := rolebundle.LifecycleDigest(activeStatuses["role-org"])
	if err != nil {
		t.Fatal(err)
	}
	shortStatus := pgHostedStatusFor(t, "role-org", secondVersion, "active", 2, firstStatusDigest, 150*time.Millisecond, publisherPrivate)
	pgHostedLifecycle(t, runtime, ownerDevice, secondVersion, 3, tenant.HostedReleased, &shortStatus)
	time.Sleep(time.Until(shortStatus.ExpiresAt) + 20*time.Millisecond)
	for _, tc := range []struct {
		role string
		kind tenant.PinTargetKind
		id   string
	}{
		{"role-org", tenant.PinOrganization, "tenant-a"},
		{"role-team", tenant.PinTeam, "team-a"},
		{"role-project", tenant.PinProject, "project"},
	} {
		response := pgRoleRequest(t, runtime, ownerDevice, http.MethodGet, "/v1/role-registry/projects/project/roles/"+tc.role, "")
		var resolved tenant.ResolvedRole
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &resolved) != nil || resolved.Source.Kind != tc.kind || resolved.Source.ScopeID != tc.id || resolved.Release.Content.Role.Name != tc.role {
			t.Fatalf("resolve %s status=%d source=%+v body=%s", tc.role, response.Code, resolved.Source, response.Body.String())
		}
	}
	shortDigest, err := rolebundle.LifecycleDigest(shortStatus)
	if err != nil {
		t.Fatal(err)
	}
	deprecatedStatus := pgHostedStatus(t, "role-org", secondVersion, "active", 3, shortDigest, publisherPrivate)
	pgHostedLifecycle(t, runtime, ownerDevice, secondVersion, 4, tenant.HostedDeprecated, &deprecatedStatus)

	// Pin CAS has one winner when two callers present the same expected generation.
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			m := tenant.Mutation{Actor: ownerDeviceAccount(ownerDevice), Device: ownerDevice.device, CorrelationID: "concurrent-pin-" + string(rune('a'+i)), OccurredAt: time.Now().UTC()}
			_, e := repo.SetRolePin(ctx, roleIdentity(ownerDevice), m, tenant.PinTarget{Tenant: "tenant-a", Kind: tenant.PinOrganization, ScopeID: "tenant-a", OrganizationID: "tenant-a"}, "role-org", "catalog-a", "v1.0.0", releases["role-org"].ContentDigest, 1, "race")
			results <- e
		}(i)
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for e := range results {
		if e == nil {
			success++
		} else if errors.Is(e, tenantrepo.ErrConflict) {
			conflict++
		} else {
			t.Fatalf("concurrent pin error=%v", e)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("pin CAS winners=%d conflicts=%d", success, conflict)
	}

	// Export is an explicit signed-byte/history transfer that the caller must
	// verify with its own publisher trust before use.
	exportResponse := pgRoleRequest(t, runtime, ownerDevice, http.MethodGet, "/v1/role-registry/releases/catalog-a/role-org/v1.0.0/"+releases["role-org"].ContentDigest, "")
	if exportResponse.Code != http.StatusOK {
		t.Fatalf("export: %d %s", exportResponse.Code, exportResponse.Body.String())
	}
	var exported tenant.HostedRoleExport
	if err = json.Unmarshal(exportResponse.Body.Bytes(), &exported); err != nil || exported.Selected.ContentDigest != releases["role-org"].ContentDigest || len(exported.Releases) != 2 || len(exported.Lifecycle) != 5 {
		t.Fatalf("export=%+v err=%v", exported, err)
	}
	var roundTrip rolebundle.Release
	if err = json.Unmarshal(exported.Releases[0].Envelope, &roundTrip); err != nil || roundTrip.Verify(keys) != nil {
		t.Fatalf("exported signed release failed independent verification: %v", err)
	}
	if err = roundTrip.Verify(nil); err == nil {
		t.Fatal("exported release verified without an explicitly supplied local trust key")
	}
	if count := pgRoleCount(t, ctx, app, "tenant-a", "controlplane_role_publishers"); count != 1 {
		t.Fatalf("implicit local trust enrollment observed in hosted publisher count=%d", count)
	}
	if count := pgRoleCount(t, ctx, app, "tenant-a", "controlplane_role_pins"); count != 7 {
		t.Fatalf("export changed pin history, count=%d", count)
	}
	rlsTx := pgCompositionTenantTx(t, ctx, app, "tenant-a")
	if _, writeErr := rlsTx.ExecContext(ctx, `INSERT INTO controlplane_role_catalogs(tenant_id,catalog_id,catalog_version,name) VALUES('tenant-b','rls-foreign',1,'foreign')`); writeErr == nil {
		_ = rlsTx.Rollback()
		t.Fatal("real PostgreSQL RLS accepted a foreign tenant write")
	}
	_ = rlsTx.Rollback()
	if count := pgRoleCount(t, ctx, app, "tenant-b", "controlplane_role_catalogs"); count != 0 {
		t.Fatalf("RLS-refused foreign insert persisted: %d", count)
	}

	// A revoked current membership and actor/device disagreement must refuse
	// before writes; direct repository calls prove the tenant boundary as well.
	stale := roleIdentity(ownerDevice)
	stale.MembershipVersion++
	if err = repo.CreateRoleCatalog(ctx, stale, tenant.Mutation{Actor: stale.Account, Device: stale.Device, CorrelationID: "stale-membership", OccurredAt: time.Now().UTC()}, tenant.HostedCatalog{Tenant: "tenant-a", ID: "stale-catalog", Name: "stale", Version: 1}); err == nil {
		t.Fatal("stale membership created catalog")
	}
	wrongActor := roleIdentity(ownerDevice)
	if err = repo.CreateRoleCatalog(ctx, wrongActor, tenant.Mutation{Actor: "account-admin", Device: wrongActor.Device, CorrelationID: "wrong-actor", OccurredAt: time.Now().UTC()}, tenant.HostedCatalog{Tenant: "tenant-a", ID: "wrong-actor-catalog", Name: "wrong", Version: 1}); err == nil {
		t.Fatal("wrong actor created catalog")
	}
	if count := pgRoleCount(t, ctx, app, "tenant-a", "controlplane_role_catalogs"); count != 1 {
		t.Fatalf("refused writes changed catalog count=%d", count)
	}
	if _, err = repo.ExportRole(ctx, roleIdentity(otherDevice), tenant.ExactReleaseKey{Tenant: "tenant-a", CatalogID: "catalog-a", RoleID: "role-org", Version: "v1.0.0", ContentDigest: releases["role-org"].ContentDigest}); err == nil {
		t.Fatal("cross-tenant export succeeded")
	}
	if count := pgRoleCount(t, ctx, app, "tenant-b", "controlplane_role_releases"); count != 0 {
		t.Fatalf("cross-tenant request wrote releases: %d", count)
	}

	// Revoke the publisher-signed status and prove the immutable hosted history
	// captures the terminal transition without rewriting release bytes.
	prevDigest, err := rolebundle.LifecycleDigest(deprecatedStatus)
	if err != nil {
		t.Fatal(err)
	}
	revoked := pgHostedStatus(t, "role-org", releases["role-org"], "revoked", 4, prevDigest, publisherPrivate)
	pgHostedLifecycle(t, runtime, ownerDevice, releases["role-org"], 5, tenant.HostedRevoked, &revoked)
	terminal := pgRoleRequest(t, runtime, ownerDevice, http.MethodPost, "/v1/role-registry/releases/catalog-a/role-org/v1.0.0/"+releases["role-org"].ContentDigest+"/lifecycle", `{"expected_sequence":6,"state":"invalid","reason":"cannot reopen","status":{}}`)
	if terminal.Code != http.StatusBadRequest {
		t.Fatalf("post-revocation malformed lifecycle reached repository: %d", terminal.Code)
	}
	if count := pgRoleCount(t, ctx, app, "tenant-a", "controlplane_role_lifecycle"); count != 11 {
		t.Fatalf("unexpected append-only lifecycle count=%d", count)
	}

	// A catalog-local publisher ID can rotate to a different key without
	// colliding with the lower-priority catalog's trust map. The project pin
	// wins, and a revoked selected pin refuses instead of falling back to org.
	projectPublic, projectPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	statusPublic, statusPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pgHostedCreateCatalog(t, runtime, ownerDevice, "catalog-org", "", "Organization roles")
	pgHostedEnrollPublisher(t, runtime, ownerDevice, "catalog-org", "publisher-a", publisherPublic)
	pgHostedCreateCatalog(t, runtime, ownerDevice, "catalog-project", "team-a", "Project roles")
	pgHostedEnrollPublisher(t, runtime, ownerDevice, "catalog-project", "publisher-a", projectPublic)
	pgHostedEnrollPublisher(t, runtime, ownerDevice, "catalog-project", "publisher-rotated", statusPublic)
	orgOverride := pgHostedSignedRelease(t, "role-override", "v1.0.0", "publisher-a", publisherPrivate)
	pgHostedImportCatalog(t, runtime, ownerDevice, "catalog-org", orgOverride)
	orgOverrideStatus := pgHostedStatus(t, "role-override", orgOverride, "active", 1, "", publisherPrivate)
	pgHostedLifecycleCatalog(t, runtime, ownerDevice, "catalog-org", orgOverride, 1, tenant.HostedReleased, &orgOverrideStatus)
	pinRoleCatalog(t, runtime, ownerDevice, "role-override", tenant.PinOrganization, "tenant-a", "catalog-org", orgOverride, 0)
	projectOverride := pgHostedSignedReleaseVariant(t, "role-override", "v1.0.0", "project override", "publisher-a", projectPrivate)
	pgHostedImportCatalog(t, runtime, ownerDevice, "catalog-project", projectOverride)
	projectOverrideStatus := pgHostedStatusWithSigner(t, "role-override", projectOverride, "active", 1, "", "publisher-rotated", time.Hour, statusPrivate)
	pgHostedLifecycleCatalog(t, runtime, ownerDevice, "catalog-project", projectOverride, 1, tenant.HostedReleased, &projectOverrideStatus)
	pinRoleCatalog(t, runtime, ownerDevice, "role-override", tenant.PinProject, "project", "catalog-project", projectOverride, 0)
	resolveOverride := pgRoleRequest(t, runtime, ownerDevice, http.MethodGet, "/v1/role-registry/projects/project/roles/role-override", "")
	var override tenant.ResolvedRole
	if resolveOverride.Code != http.StatusOK || json.Unmarshal(resolveOverride.Body.Bytes(), &override) != nil || override.Source.Kind != tenant.PinProject || override.Release.ContentDigest != projectOverride.ContentDigest {
		t.Fatalf("valid project override with catalog-local rotated trust failed: %d %+v %s", resolveOverride.Code, override, resolveOverride.Body.String())
	}
	projectStatusDigest, err := rolebundle.LifecycleDigest(projectOverrideStatus)
	if err != nil {
		t.Fatal(err)
	}
	projectRevokedStatus := pgHostedStatusWithSigner(t, "role-override", projectOverride, "revoked", 2, projectStatusDigest, "publisher-rotated", time.Hour, statusPrivate)
	pgHostedLifecycleCatalog(t, runtime, ownerDevice, "catalog-project", projectOverride, 2, tenant.HostedRevoked, &projectRevokedStatus)
	if response := pgRoleRequest(t, runtime, ownerDevice, http.MethodGet, "/v1/role-registry/projects/project/roles/role-override", ""); response.Code != http.StatusBadRequest {
		t.Fatalf("invalid selected project pin fell back to organization pin: %d %s", response.Code, response.Body.String())
	}

	// Membership validity is checked again after a blocked role lock. Expiry
	// during the wait must roll back the pending pin and its audit entry.
	expiringRelease := pgHostedSignedRelease(t, "role-expiring", "v1.0.0", "publisher-a", publisherPrivate)
	pgHostedImport(t, runtime, ownerDevice, expiringRelease)
	expiringStatus := pgHostedStatus(t, "role-expiring", expiringRelease, "active", 1, "", publisherPrivate)
	pgHostedLifecycle(t, runtime, ownerDevice, expiringRelease, 1, tenant.HostedReleased, &expiringStatus)
	expiresUnix := time.Now().Add(3 * time.Second).Unix()
	expiryTx := pgCompositionTenantTx(t, ctx, owner, "tenant-a")
	if _, err = expiryTx.ExecContext(ctx, `UPDATE controlplane_memberships SET expires_unix=$1 WHERE tenant_id='tenant-a' AND account_id='account-tenant-a'`, expiresUnix); err != nil {
		_ = expiryTx.Rollback()
		t.Fatal(err)
	}
	if err = expiryTx.Commit(); err != nil {
		t.Fatal(err)
	}
	expiryLock := acquireHostedRoleLock(t, ctx, app, "tenant-a", "catalog-a", "role-expiring")
	expiryPinCount := pgRoleCount(t, ctx, app, "tenant-a", "controlplane_role_pins")
	expiryAuditCount := pgRoleCount(t, ctx, app, "tenant-a", "controlplane_role_audit_events")
	expiryLifecycleCount := pgRoleCount(t, ctx, app, "tenant-a", "controlplane_role_lifecycle")
	expiryResult := make(chan error, 1)
	go func() {
		mutation := tenant.Mutation{Actor: ownerDeviceAccount(ownerDevice), Device: ownerDevice.device, CorrelationID: "expiry-race-pin", OccurredAt: time.Now().UTC()}
		_, pinErr := repo.SetRolePin(ctx, roleIdentity(ownerDevice), mutation, tenant.PinTarget{Tenant: "tenant-a", Kind: tenant.PinOrganization, ScopeID: "tenant-a", OrganizationID: "tenant-a"}, "role-expiring", "catalog-a", "v1.0.0", expiringRelease.ContentDigest, 0, "expiry race")
		expiryResult <- pinErr
	}()
	waitForAdvisoryWaiters(t, ctx, app, 1)
	for time.Now().Unix() <= expiresUnix {
		time.Sleep(20 * time.Millisecond)
	}
	if err = expiryLock.Commit(); err != nil {
		t.Fatal(err)
	}
	if expiryErr := <-expiryResult; !errors.Is(expiryErr, tenant.ErrDenied) {
		t.Fatalf("membership expiry while blocked returned %v, want ErrDenied", expiryErr)
	}
	if count := pgRoleCount(t, ctx, app, "tenant-a", "controlplane_role_pins"); count != expiryPinCount {
		t.Fatalf("expired membership wrote a pin: before=%d after=%d", expiryPinCount, count)
	}
	if count := pgRoleCount(t, ctx, app, "tenant-a", "controlplane_role_audit_events"); count != expiryAuditCount {
		t.Fatalf("expired membership wrote audit evidence: before=%d after=%d", expiryAuditCount, count)
	}
	if count := pgRoleCount(t, ctx, app, "tenant-a", "controlplane_role_lifecycle"); count != expiryLifecycleCount {
		t.Fatalf("expired membership changed lifecycle: before=%d after=%d", expiryLifecycleCount, count)
	}
	resetExpiryTx := pgCompositionTenantTx(t, ctx, owner, "tenant-a")
	if _, err = resetExpiryTx.ExecContext(ctx, `UPDATE controlplane_memberships SET expires_unix=0 WHERE tenant_id='tenant-a' AND account_id='account-tenant-a'`); err != nil {
		_ = resetExpiryTx.Rollback()
		t.Fatal(err)
	}
	if err = resetExpiryTx.Commit(); err != nil {
		t.Fatal(err)
	}

	// Revoke wins the shared role-history lock before a queued pin. The pin
	// must observe terminal history after waking and leave no row behind.
	lockRaceRelease := pgHostedSignedRelease(t, "role-lock-race", "v1.0.0", "publisher-a", publisherPrivate)
	pgHostedImport(t, runtime, ownerDevice, lockRaceRelease)
	lockRaceActive := pgHostedStatus(t, "role-lock-race", lockRaceRelease, "active", 1, "", publisherPrivate)
	pgHostedLifecycle(t, runtime, ownerDevice, lockRaceRelease, 1, tenant.HostedReleased, &lockRaceActive)
	lockRaceDigest, err := rolebundle.LifecycleDigest(lockRaceActive)
	if err != nil {
		t.Fatal(err)
	}
	lockRaceRevoked := pgHostedStatus(t, "role-lock-race", lockRaceRelease, "revoked", 2, lockRaceDigest, publisherPrivate)
	roleLock := acquireHostedRoleLock(t, ctx, app, "tenant-a", "catalog-a", "role-lock-race")
	lockRacePinCount := pgRoleCount(t, ctx, app, "tenant-a", "controlplane_role_pins")
	lockRaceAuditCount := pgRoleCount(t, ctx, app, "tenant-a", "controlplane_role_audit_events")
	lockRaceLifecycleCount := pgRoleCount(t, ctx, app, "tenant-a", "controlplane_role_lifecycle")
	type lockRaceResult struct {
		operation string
		err       error
	}
	roleLockResults := make(chan lockRaceResult, 2)
	go func() {
		mutation := tenant.Mutation{Actor: ownerDeviceAccount(ownerDevice), Device: ownerDevice.device, CorrelationID: "revoke-lock-race", OccurredAt: time.Now().UTC()}
		_, revokeErr := repo.AppendRoleLifecycle(ctx, roleIdentity(ownerDevice), mutation, tenant.ExactReleaseKey{Tenant: "tenant-a", CatalogID: "catalog-a", RoleID: "role-lock-race", Version: "v1.0.0", ContentDigest: lockRaceRelease.ContentDigest}, 2, tenant.HostedRevoked, &lockRaceRevoked, "lock race revoke")
		roleLockResults <- lockRaceResult{operation: "revoke", err: revokeErr}
	}()
	waitForAdvisoryWaiters(t, ctx, app, 1)
	go func() {
		mutation := tenant.Mutation{Actor: ownerDeviceAccount(ownerDevice), Device: ownerDevice.device, CorrelationID: "pin-lock-race", OccurredAt: time.Now().UTC()}
		_, pinErr := repo.SetRolePin(ctx, roleIdentity(ownerDevice), mutation, tenant.PinTarget{Tenant: "tenant-a", Kind: tenant.PinOrganization, ScopeID: "tenant-a", OrganizationID: "tenant-a"}, "role-lock-race", "catalog-a", "v1.0.0", lockRaceRelease.ContentDigest, 0, "lock race pin")
		roleLockResults <- lockRaceResult{operation: "pin", err: pinErr}
	}()
	waitForAdvisoryWaiters(t, ctx, app, 2)
	if err = roleLock.Commit(); err != nil {
		t.Fatal(err)
	}
	var revokeErr, pinErr error
	for range 2 {
		result := <-roleLockResults
		if result.operation == "revoke" {
			revokeErr = result.err
		} else {
			pinErr = result.err
		}
	}
	if revokeErr != nil || pinErr == nil {
		t.Fatalf("revoke/pin shared-lock outcome: revoke=%v pin=%v", revokeErr, pinErr)
	}
	if count := pgRoleCount(t, ctx, app, "tenant-a", "controlplane_role_pins"); count != lockRacePinCount {
		t.Fatalf("pin was appended after role revocation: before=%d after=%d", lockRacePinCount, count)
	}
	if count := pgRoleCount(t, ctx, app, "tenant-a", "controlplane_role_audit_events"); count != lockRaceAuditCount+1 {
		t.Fatalf("revoke/pin race audit rows=%d, want only the winning revoke (%d)", count, lockRaceAuditCount+1)
	}
	if count := pgRoleCount(t, ctx, app, "tenant-a", "controlplane_role_lifecycle"); count != lockRaceLifecycleCount+1 {
		t.Fatalf("revoke/pin race lifecycle rows=%d, want only the winning revoke (%d)", count, lockRaceLifecycleCount+1)
	}

	revokeTx := pgCompositionTenantTx(t, ctx, owner, "tenant-a")
	if _, err = revokeTx.ExecContext(ctx, `UPDATE controlplane_memberships SET state=$1,version=version+1 WHERE tenant_id='tenant-a' AND account_id='account-tenant-a'`, tenant.LifecycleRevoked); err != nil {
		_ = revokeTx.Rollback()
		t.Fatal(err)
	}
	if err = revokeTx.Commit(); err != nil {
		t.Fatal(err)
	}
	revokedCatalogCount := pgRoleCount(t, ctx, app, "tenant-a", "controlplane_role_catalogs")
	revokedAuditCount := pgRoleCount(t, ctx, app, "tenant-a", "controlplane_role_audit_events")
	revokedLifecycleCount := pgRoleCount(t, ctx, app, "tenant-a", "controlplane_role_lifecycle")
	revokedPinCount := pgRoleCount(t, ctx, app, "tenant-a", "controlplane_role_pins")
	if response := pgRoleRequest(t, runtime, ownerDevice, http.MethodGet, "/v1/role-registry/catalogs", ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("revoked member HTTP status=%d body=%s", response.Code, response.Body.String())
	}
	revokedIdentity := roleIdentity(ownerDevice)
	revokedIdentity.MembershipVersion = 2
	if err = repo.CreateRoleCatalog(ctx, revokedIdentity, tenant.Mutation{Actor: revokedIdentity.Account, Device: revokedIdentity.Device, CorrelationID: "revoked-member", OccurredAt: time.Now().UTC()}, tenant.HostedCatalog{Tenant: "tenant-a", ID: "revoked-member-catalog", Name: "revoked", Version: 1}); err == nil {
		t.Fatal("revoked current membership created catalog")
	}
	if count := pgRoleCount(t, ctx, app, "tenant-a", "controlplane_role_catalogs"); count != revokedCatalogCount || count != 3 {
		t.Fatalf("revoked membership refusal changed catalog count: before=%d after=%d", revokedCatalogCount, count)
	}
	if count := pgRoleCount(t, ctx, app, "tenant-a", "controlplane_role_audit_events"); count != revokedAuditCount {
		t.Fatalf("revoked membership refusal wrote audit evidence: before=%d after=%d", revokedAuditCount, count)
	}
	if count := pgRoleCount(t, ctx, app, "tenant-a", "controlplane_role_lifecycle"); count != revokedLifecycleCount {
		t.Fatalf("revoked membership refusal changed lifecycle: before=%d after=%d", revokedLifecycleCount, count)
	}
	if count := pgRoleCount(t, ctx, app, "tenant-a", "controlplane_role_pins"); count != revokedPinCount {
		t.Fatalf("revoked membership refusal changed pins: before=%d after=%d", revokedPinCount, count)
	}
	t.Log("real PostgreSQL schema11 refusal, schema12 exact checksum and six forced-RLS relations, authenticated owner/admin API, exact signed import/export, role inheritance, no-write tenant/member refusals, immutable lifecycle, and concurrent CAS verified")
}

func grantRoleFixture(t *testing.T, ctx context.Context, owner *sql.DB) {
	t.Helper()
	if _, err := owner.ExecContext(ctx, `GRANT USAGE ON SCHEMA public TO qualification_app; GRANT SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA public TO qualification_app; GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA public TO qualification_app`); err != nil {
		t.Fatal(err)
	}
}
func assertMigrationCount(t *testing.T, ctx context.Context, db *sql.DB, want int) {
	t.Helper()
	var got int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM controlplane_schema_migrations`).Scan(&got); err != nil || got != want {
		t.Fatalf("migration count=%d err=%v want=%d", got, err, want)
	}
}
func seedRoleTeamAndAdmin(t *testing.T, ctx context.Context, owner *sql.DB) {
	t.Helper()
	tx := pgCompositionTenantTx(t, ctx, owner, "tenant-a")
	defer func() { _ = tx.Rollback() }()
	for _, q := range []string{
		`INSERT INTO controlplane_teams(tenant_id,team_id,name,state,version) VALUES('tenant-a','team-a','Team A',1,1)`,
		`UPDATE controlplane_memberships SET team_id='team-a' WHERE tenant_id='tenant-a' AND account_id='account-tenant-a'`,
		`INSERT INTO controlplane_accounts(account_id,name,state,version) VALUES('account-admin','Admin',1,1)`,
		`INSERT INTO controlplane_memberships(tenant_id,account_id,team_id,roles,state,version) VALUES('tenant-a','account-admin','team-a','["administrator"]',1,1)`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}
func pgHostedEnrollAccount(t *testing.T, ctx context.Context, repo *tenantrepo.Repository, org, account, device string) pgCompositionDevice {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	credential := make([]byte, 32)
	if _, err = rand.Read(credential); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte(org + device))
	flow := base64.RawURLEncoding.EncodeToString(hash[:])
	raw := bytes.Repeat([]byte{0x55}, 32)
	if _, err = repo.CreateAuth(ctx, tenant.Scope{Organization: tenant.OrganizationID(org)}, flow, raw, time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	d := tenant.Device{Tenant: tenant.OrganizationID(org), ID: tenant.DeviceID(device), Account: tenant.AccountID(account), Name: "Fixture", Platform: tenant.PlatformLinux, State: tenant.LifecycleActive, Version: 1}
	copy(d.PublicKey[:], public)
	session := devicesession.Record{Tenant: d.Tenant, ID: devicesession.ID("session-" + device), Account: d.Account, Device: d.ID, CredentialDigest: sha256.Sum256(credential), State: devicesession.StateActive, Version: 1, ExpiresUnix: time.Now().Add(time.Hour).Unix()}
	if err = repo.UpdateAuth(ctx, tenant.Scope{Organization: d.Tenant}, flow, func([]byte) ([]byte, *deviceoauth.SessionChange, error) {
		return raw, &deviceoauth.SessionChange{Device: d, Session: session, ExpectedMembershipVersion: 1, CorrelationID: "register-" + device}, nil
	}); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(public)
	return pgCompositionDevice{org, d.ID, private, credential, "device-" + hex.EncodeToString(digest[:])}
}
func roleIdentity(d pgCompositionDevice) tenant.VerifiedIdentity {
	account := tenant.AccountID("account-" + d.org)
	if d.device == "device-role-admin" {
		account = "account-admin"
	}
	return tenant.VerifiedIdentity{Scope: tenant.Scope{Organization: tenant.OrganizationID(d.org)}, Account: account, Device: d.device, MembershipVersion: 1, PolicyRevision: 1}
}
func ownerDeviceAccount(d pgCompositionDevice) tenant.AccountID { return roleIdentity(d).Account }

var pgRoleRequestSequence atomic.Uint64

func pgRoleRequest(t *testing.T, runtime *Runtime, d pgCompositionDevice, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	bearer := base64.RawURLEncoding.EncodeToString([]byte(d.org)) + "." + base64.RawURLEncoding.EncodeToString(d.credential)
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+bearer)
	r.Header.Set("X-Request-ID", "role-pg-correlation-"+strconv.FormatUint(pgRoleRequestSequence.Add(1), 10))
	w := httptest.NewRecorder()
	runtime.API.Handler().ServeHTTP(w, r)
	return w
}
func pgHostedSignedRelease(t *testing.T, roleID, version, keyID string, private ed25519.PrivateKey) rolebundle.Release {
	t.Helper()
	value, err := rolebundle.Sign(rolebundle.ReleaseContent{SchemaVersion: rolebundle.SchemaVersion, Version: version, Role: team.Role{Name: roleID, Summary: "Hosted qualification role"}}, keyID, private)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
func pgHostedSignedReleaseVariant(t *testing.T, roleID, version, summary, keyID string, private ed25519.PrivateKey) rolebundle.Release {
	t.Helper()
	value, err := rolebundle.Sign(rolebundle.ReleaseContent{SchemaVersion: rolebundle.SchemaVersion, Version: version, Role: team.Role{Name: roleID, Summary: summary}}, keyID, private)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
func pgHostedImport(t *testing.T, runtime *Runtime, d pgCompositionDevice, release rolebundle.Release) {
	t.Helper()
	w := pgRoleImportResponse(t, runtime, d, release)
	if w.Code != http.StatusCreated {
		t.Fatalf("import %s: %d %s", release.Content.Role.Name, w.Code, w.Body.String())
	}
}
func pgRoleImportResponse(t *testing.T, runtime *Runtime, d pgCompositionDevice, release rolebundle.Release) *httptest.ResponseRecorder {
	t.Helper()
	envelope, err := json.Marshal(release)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(struct {
		RoleID   string `json:"role_id"`
		Version  string `json:"version"`
		Digest   string `json:"content_digest"`
		Envelope []byte `json:"envelope"`
	}{release.Content.Role.Name, release.Content.Version, release.ContentDigest, envelope})
	return pgRoleRequest(t, runtime, d, http.MethodPost, "/v1/role-registry/catalogs/catalog-a/releases", string(body))
}
func pgHostedCreateCatalog(t *testing.T, runtime *Runtime, device pgCompositionDevice, catalogID, teamID, name string) {
	t.Helper()
	body, err := json.Marshal(struct {
		ID   string `json:"catalog_id"`
		Team string `json:"team_id,omitempty"`
		Name string `json:"name"`
	}{catalogID, teamID, name})
	if err != nil {
		t.Fatal(err)
	}
	response := pgRoleRequest(t, runtime, device, http.MethodPost, "/v1/role-registry/catalogs", string(body))
	if response.Code != http.StatusCreated {
		t.Fatalf("create catalog %s: %d %s", catalogID, response.Code, response.Body.String())
	}
}
func pgHostedEnrollPublisher(t *testing.T, runtime *Runtime, device pgCompositionDevice, catalogID, keyID string, public ed25519.PublicKey) {
	t.Helper()
	body, err := json.Marshal(struct {
		KeyID     string            `json:"key_id"`
		PublicKey ed25519.PublicKey `json:"public_key"`
	}{keyID, public})
	if err != nil {
		t.Fatal(err)
	}
	response := pgRoleRequest(t, runtime, device, http.MethodPost, "/v1/role-registry/catalogs/"+catalogID+"/publishers", string(body))
	if response.Code != http.StatusCreated {
		t.Fatalf("enroll publisher %s/%s: %d %s", catalogID, keyID, response.Code, response.Body.String())
	}
}
func pgHostedImportCatalog(t *testing.T, runtime *Runtime, device pgCompositionDevice, catalogID string, release rolebundle.Release) {
	t.Helper()
	envelope, err := json.Marshal(release)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(struct {
		RoleID   string `json:"role_id"`
		Version  string `json:"version"`
		Digest   string `json:"content_digest"`
		Envelope []byte `json:"envelope"`
	}{release.Content.Role.Name, release.Content.Version, release.ContentDigest, envelope})
	if err != nil {
		t.Fatal(err)
	}
	response := pgRoleRequest(t, runtime, device, http.MethodPost, "/v1/role-registry/catalogs/"+catalogID+"/releases", string(body))
	if response.Code != http.StatusCreated {
		t.Fatalf("import %s from %s: %d %s", release.Content.Role.Name, catalogID, response.Code, response.Body.String())
	}
}
func pgHostedLifecycleCatalog(t *testing.T, runtime *Runtime, device pgCompositionDevice, catalogID string, release rolebundle.Release, sequence uint64, state tenant.HostedLifecycleState, status *rolebundle.LifecycleStatus) {
	t.Helper()
	body, err := json.Marshal(struct {
		Expected uint64                      `json:"expected_sequence"`
		State    tenant.HostedLifecycleState `json:"state"`
		Status   *rolebundle.LifecycleStatus `json:"status"`
		Reason   string                      `json:"reason"`
	}{sequence, state, status, "qualification"})
	if err != nil {
		t.Fatal(err)
	}
	path := "/v1/role-registry/releases/" + catalogID + "/" + release.Content.Role.Name + "/" + release.Content.Version + "/" + release.ContentDigest + "/lifecycle"
	response := pgRoleRequest(t, runtime, device, http.MethodPost, path, string(body))
	if response.Code != http.StatusCreated {
		t.Fatalf("lifecycle %s/%s sequence %d: %d %s", catalogID, release.Content.Role.Name, sequence, response.Code, response.Body.String())
	}
}
func pinRoleCatalog(t *testing.T, runtime *Runtime, device pgCompositionDevice, role string, kind tenant.PinTargetKind, scopeID, catalogID string, release rolebundle.Release, expected uint64) {
	t.Helper()
	body, err := json.Marshal(struct {
		Kind      tenant.PinTargetKind `json:"target_kind"`
		ScopeID   string               `json:"scope_id"`
		RoleID    string               `json:"role_id"`
		CatalogID string               `json:"catalog_id"`
		Version   string               `json:"version"`
		Digest    string               `json:"content_digest"`
		Expected  uint64               `json:"expected_generation"`
		Reason    string               `json:"reason"`
	}{kind, scopeID, role, catalogID, release.Content.Version, release.ContentDigest, expected, "qualification"})
	if err != nil {
		t.Fatal(err)
	}
	response := pgRoleRequest(t, runtime, device, http.MethodPut, "/v1/role-registry/pins", string(body))
	if response.Code != http.StatusOK {
		t.Fatalf("pin %s %s from %s: %d %s", role, kind, catalogID, response.Code, response.Body.String())
	}
}
func pgHostedStatusWithSigner(t *testing.T, roleID string, release rolebundle.Release, state string, generation uint64, previous, signer string, ttl time.Duration, private ed25519.PrivateKey) rolebundle.LifecycleStatus {
	t.Helper()
	now := time.Now().UTC()
	status, err := rolebundle.SignStatus(rolebundle.LifecycleStatus{SchemaVersion: 1, RoleID: roleID, Version: release.Content.Version, ContentDigest: release.ContentDigest, Generation: generation, PreviousStatusDigest: previous, State: state, IssuedAt: now.Add(-time.Second), ExpiresAt: now.Add(ttl), PublisherKeyID: signer}, private)
	if err != nil {
		t.Fatal(err)
	}
	return status
}
func acquireHostedRoleLock(t *testing.T, ctx context.Context, db *sql.DB, tenantID, catalogID, roleID string) *sql.Tx {
	t.Helper()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	key := "hosted-role:" + tenantID + ":" + catalogID + ":" + roleID
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,677))`, key); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	return tx
}
func waitForAdvisoryWaiters(t *testing.T, ctx context.Context, db *sql.DB, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var lockWaiters, activityWaiters int
	for time.Now().Before(deadline) {
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM pg_locks WHERE locktype='advisory' AND NOT granted`).Scan(&lockWaiters); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND wait_event='advisory'`).Scan(&activityWaiters); err != nil {
			t.Fatal(err)
		}
		if lockWaiters >= want && activityWaiters >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("blocked advisory workers: pg_locks=%d pg_stat_activity=%d, want at least %d", lockWaiters, activityWaiters, want)
}

func pgHostedStatus(t *testing.T, roleID string, release rolebundle.Release, state string, generation uint64, previous string, private ed25519.PrivateKey) rolebundle.LifecycleStatus {
	return pgHostedStatusFor(t, roleID, release, state, generation, previous, time.Hour, private)
}
func pgHostedStatusFor(t *testing.T, roleID string, release rolebundle.Release, state string, generation uint64, previous string, ttl time.Duration, private ed25519.PrivateKey) rolebundle.LifecycleStatus {
	t.Helper()
	now := time.Now().UTC()
	status, err := rolebundle.SignStatus(rolebundle.LifecycleStatus{SchemaVersion: 1, RoleID: roleID, Version: release.Content.Version, ContentDigest: release.ContentDigest, Generation: generation, PreviousStatusDigest: previous, State: state, IssuedAt: now.Add(-time.Second), ExpiresAt: now.Add(ttl), PublisherKeyID: release.PublisherKeyID}, private)
	if err != nil {
		t.Fatal(err)
	}
	return status
}
func pgHostedLifecycle(t *testing.T, runtime *Runtime, d pgCompositionDevice, release rolebundle.Release, sequence uint64, state tenant.HostedLifecycleState, status *rolebundle.LifecycleStatus) {
	t.Helper()
	body, _ := json.Marshal(struct {
		Expected uint64                      `json:"expected_sequence"`
		State    tenant.HostedLifecycleState `json:"state"`
		Status   *rolebundle.LifecycleStatus `json:"status"`
		Reason   string                      `json:"reason"`
	}{sequence, state, status, "qualification"})
	path := "/v1/role-registry/releases/catalog-a/" + release.Content.Role.Name + "/" + release.Content.Version + "/" + release.ContentDigest + "/lifecycle"
	w := pgRoleRequest(t, runtime, d, http.MethodPost, path, string(body))
	if w.Code != http.StatusCreated {
		t.Fatalf("lifecycle %s sequence %d: %d %s", release.Content.Role.Name, sequence, w.Code, w.Body.String())
	}
}
func pinRole(t *testing.T, runtime *Runtime, d pgCompositionDevice, role string, kind tenant.PinTargetKind, scopeID string, release rolebundle.Release, expected uint64) {
	t.Helper()
	body, _ := json.Marshal(struct {
		Kind      tenant.PinTargetKind `json:"target_kind"`
		ScopeID   string               `json:"scope_id"`
		RoleID    string               `json:"role_id"`
		CatalogID string               `json:"catalog_id"`
		Version   string               `json:"version"`
		Digest    string               `json:"content_digest"`
		Expected  uint64               `json:"expected_generation"`
		Reason    string               `json:"reason"`
	}{kind, scopeID, role, "catalog-a", release.Content.Version, release.ContentDigest, expected, "qualification"})
	w := pgRoleRequest(t, runtime, d, http.MethodPut, "/v1/role-registry/pins", string(body))
	if w.Code != http.StatusOK {
		t.Fatalf("pin %s %s: %d %s", role, kind, w.Code, w.Body.String())
	}
}
func pgRoleCount(t *testing.T, ctx context.Context, db *sql.DB, org, relation string) int {
	t.Helper()
	allowed := map[string]bool{"controlplane_role_catalogs": true, "controlplane_role_publishers": true, "controlplane_role_releases": true, "controlplane_role_lifecycle": true, "controlplane_role_pins": true, "controlplane_role_audit_events": true}
	if !allowed[relation] {
		t.Fatal("unapproved hosted-role relation")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `SELECT set_config('dacli.tenant_id',$1,true)`, org); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM "+relation).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}
