package tenantrepo

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/mlnomadpy/dacli/cloud/internal/tenant"
	"github.com/mlnomadpy/dacli/internal/rolebundle"
	"github.com/mlnomadpy/dacli/internal/team"
)

type hostedRoleFixture struct {
	scope     tenant.Scope
	identity  tenant.VerifiedIdentity
	mutation  Mutation
	catalog   tenant.HostedCatalog
	release   tenant.HostedRelease
	public    ed25519.PublicKey
	private   ed25519.PrivateKey
	status    rolebundle.LifecycleStatus
	statusRaw []byte
	statusSum string
	now       time.Time
}

func newHostedRoleFixture(t *testing.T) hostedRoleFixture {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	content := rolebundle.ReleaseContent{SchemaVersion: rolebundle.SchemaVersion, Version: "1.2.3", Role: team.Role{
		Name: "implementer", Summary: "build", Skills: []string{"go"}, Scope: []string{"internal/**"}, OutOfScope: []string{"secrets/**"},
		Shortcuts: []string{"test"}, Grant: "rw", EscalateTo: []string{"reviewer"}, WIP: 2, Kind: "implementer", Runtime: "codex", Model: "model-x",
		Profile:    team.ModelProfile{ID: "model-x", CostTier: 2, MaxTaskPoints: 5, ContextLimit: 80000, CapabilityTags: []string{"code"}},
		FallbackTo: []string{"senior"}, MaxPoints: 7, Prompt: "inspect, change, verify",
	}, References: []rolebundle.Reference{{Kind: "skill", ID: "go", Version: "v2", Digest: "sha256:" + strings.Repeat("a", 64)}}}
	signed, err := rolebundle.Sign(content, "publisher-a", private)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := json.Marshal(signed)
	if err != nil {
		t.Fatal(err)
	}
	scope := mustScope(t, "tenant-role")
	now := time.Date(2026, 10, 24, 11, 15, 0, 0, time.UTC)
	status, err := rolebundle.SignStatus(rolebundle.LifecycleStatus{SchemaVersion: rolebundle.SchemaVersion, RoleID: signed.Content.Role.Name,
		Version: signed.Content.Version, ContentDigest: signed.ContentDigest, Generation: 1, State: "active", IssuedAt: now.Add(-time.Minute),
		ExpiresAt: now.Add(time.Hour), PublisherKeyID: "publisher-a"}, private)
	if err != nil {
		t.Fatal(err)
	}
	statusRaw, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	statusSum, err := rolebundle.LifecycleDigest(status)
	if err != nil {
		t.Fatal(err)
	}
	return hostedRoleFixture{
		scope:    scope,
		identity: tenant.VerifiedIdentity{Scope: scope, Account: "actor-role", MembershipVersion: 3, PolicyRevision: 1},
		mutation: Mutation{Actor: "actor-role", CorrelationID: "role-fixture-correlation", OccurredAt: now},
		catalog:  tenant.HostedCatalog{Tenant: scope.Organization, ID: "catalog-one", Name: "One", Version: 1},
		release: tenant.HostedRelease{Key: tenant.ExactReleaseKey{Tenant: scope.Organization, CatalogID: "catalog-one", RoleID: signed.Content.Role.Name,
			Version: signed.Content.Version, ContentDigest: signed.ContentDigest}, Envelope: envelope},
		public: public, private: private, status: status, statusRaw: statusRaw, statusSum: statusSum, now: now,
	}
}

func expectRoleActor(mock sqlmock.Sqlmock, fixture hostedRoleFixture, now time.Time, roles string, expiry int64) {
	mock.ExpectQuery(`SELECT state FROM controlplane_organizations WHERE tenant_id=\$1 AND organization_id=\$1 FOR SHARE`).
		WithArgs(fixture.scope.Organization).WillReturnRows(sqlmock.NewRows([]string{"state"}).AddRow(tenant.LifecycleActive))
	mock.ExpectQuery(`SELECT state FROM controlplane_accounts WHERE account_id=\$1 FOR SHARE`).
		WithArgs(fixture.identity.Account).WillReturnRows(sqlmock.NewRows([]string{"state"}).AddRow(tenant.LifecycleActive))
	mock.ExpectQuery(`SELECT tenant_id,account_id,COALESCE\(team_id,''\),roles,state,version,expires_unix\s+FROM controlplane_memberships WHERE tenant_id=\$1 AND account_id=\$2 FOR SHARE`).
		WithArgs(fixture.scope.Organization, fixture.identity.Account).WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "account_id", "team_id", "roles", "state", "version", "expires_unix"}).
		AddRow(fixture.scope.Organization, fixture.identity.Account, "", []byte(roles), tenant.LifecycleActive, fixture.identity.MembershipVersion, expiry))
	mock.ExpectQuery(`SELECT clock_timestamp\(\)`).WillReturnRows(sqlmock.NewRows([]string{"clock_timestamp"}).AddRow(now))
}

func expectRoleCatalog(mock sqlmock.Sqlmock, fixture hostedRoleFixture) {
	mock.ExpectQuery(`SELECT tenant_id,catalog_id,COALESCE\(team_id,''\),name,catalog_version FROM controlplane_role_catalogs WHERE tenant_id=\$1 AND catalog_id=\$2 FOR SHARE`).
		WithArgs(fixture.scope.Organization, fixture.catalog.ID).
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "catalog_id", "team_id", "name", "catalog_version"}).
			AddRow(fixture.catalog.Tenant, fixture.catalog.ID, "", fixture.catalog.Name, fixture.catalog.Version))
}

func expectRolePublishers(mock sqlmock.Sqlmock, fixture hostedRoleFixture) {
	mock.ExpectQuery(`SELECT key_id,public_key FROM controlplane_role_publishers WHERE tenant_id=\$1 AND catalog_id=\$2 ORDER BY key_id`).
		WithArgs(fixture.scope.Organization, fixture.catalog.ID, maxRolePublisherKeys).
		WillReturnRows(sqlmock.NewRows([]string{"key_id", "public_key"}).AddRow("publisher-a", []byte(fixture.public)))
}

func expectRoleHistory(mock sqlmock.Sqlmock, fixture hostedRoleFixture) {
	mock.ExpectQuery(`SELECT version,content_digest,sequence,hosted_state,signed_status_bytes,signed_status_digest,actor_id,reason,recorded_at\s+FROM controlplane_role_lifecycle`).
		WithArgs(fixture.scope.Organization, fixture.catalog.ID, fixture.release.Key.RoleID, maxRoleExportEntries).
		WillReturnRows(sqlmock.NewRows([]string{"version", "content_digest", "sequence", "hosted_state", "signed_status_bytes", "signed_status_digest", "actor_id", "reason", "recorded_at"}).
			AddRow(fixture.release.Key.Version, fixture.release.Key.ContentDigest, uint64(1), tenant.HostedDraft, nil, nil, fixture.identity.Account, "release imported as draft", fixture.now).
			AddRow(fixture.release.Key.Version, fixture.release.Key.ContentDigest, uint64(2), tenant.HostedReleased, fixture.statusRaw, []byte(fixture.statusSum), fixture.identity.Account, "published", fixture.now))
}

func expectRoleLifecycleQueries(mock sqlmock.Sqlmock, fixture hostedRoleFixture) {
	mock.ExpectQuery(`SELECT sequence,hosted_state,signed_status_bytes,signed_status_digest,actor_id,reason,recorded_at\s+FROM controlplane_role_lifecycle`).
		WithArgs(fixture.scope.Organization, fixture.catalog.ID, fixture.release.Key.RoleID, fixture.release.Key.Version, fixture.release.Key.ContentDigest).
		WillReturnRows(sqlmock.NewRows([]string{"sequence", "hosted_state", "signed_status_bytes", "signed_status_digest", "actor_id", "reason", "recorded_at"}).
			AddRow(uint64(1), tenant.HostedDraft, nil, nil, fixture.identity.Account, "release imported as draft", fixture.now))
	mock.ExpectQuery(`SELECT signed_status_bytes,signed_status_digest FROM controlplane_role_lifecycle`).
		WithArgs(fixture.scope.Organization, fixture.catalog.ID, fixture.release.Key.RoleID).
		WillReturnRows(sqlmock.NewRows([]string{"signed_status_bytes", "signed_status_digest"}))
}

func TestCreateRoleCatalogLocksCurrentMembershipBeforeWriting(t *testing.T) {
	repo, mock, closeDB := testRepository(t)
	defer closeDB()
	scope := mustScope(t, "tenant-role")
	identity := tenant.VerifiedIdentity{Scope: scope, Account: "actor-role", MembershipVersion: 3, PolicyRevision: 1}
	change := Mutation{Actor: identity.Account, CorrelationID: "role-create-1", OccurredAt: time.Unix(100, 0)}
	catalog := tenant.HostedCatalog{Tenant: scope.Organization, ID: "catalog-one", Name: "One", Version: 1}
	now := time.Date(2026, 10, 24, 11, 15, 0, 0, time.UTC)

	mock.ExpectBegin()
	expectTenantBinding(mock, scope)
	mock.ExpectQuery(`SELECT state FROM controlplane_organizations WHERE tenant_id=\$1 AND organization_id=\$1 FOR SHARE`).
		WithArgs(scope.Organization).WillReturnRows(sqlmock.NewRows([]string{"state"}).AddRow(tenant.LifecycleActive))
	mock.ExpectQuery(`SELECT state FROM controlplane_accounts WHERE account_id=\$1 FOR SHARE`).
		WithArgs(identity.Account).WillReturnRows(sqlmock.NewRows([]string{"state"}).AddRow(tenant.LifecycleActive))
	mock.ExpectQuery(`SELECT tenant_id,account_id,COALESCE\(team_id,''\),roles,state,version,expires_unix\s+FROM controlplane_memberships WHERE tenant_id=\$1 AND account_id=\$2 FOR SHARE`).
		WithArgs(scope.Organization, identity.Account).
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "account_id", "team_id", "roles", "state", "version", "expires_unix"}).
			AddRow(scope.Organization, identity.Account, "", []byte(`["owner"]`), tenant.LifecycleActive, 3, int64(0)))
	mock.ExpectQuery(`SELECT clock_timestamp\(\)`).WillReturnRows(sqlmock.NewRows([]string{"clock_timestamp"}).AddRow(now))
	mock.ExpectQuery(`SELECT clock_timestamp\(\)`).WillReturnRows(sqlmock.NewRows([]string{"clock_timestamp"}).AddRow(now))
	// The catalog insert is expected only after both policy identity and the locked membership.
	mock.ExpectExec(`INSERT INTO controlplane_role_catalogs`).
		WithArgs(catalog.Tenant, catalog.ID, catalog.Version, "", catalog.Name, now).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO controlplane_role_audit_events`).
		WithArgs(scope.Organization, change.CorrelationID, change.Actor, change.Device, "catalog.create", catalog.ID, sqlmock.AnyArg(), "catalog created", now).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if err := repo.CreateRoleCatalog(context.Background(), identity, change, catalog); err != nil {
		t.Fatal(err)
	}
	assertExpectations(t, mock)
}

func TestRoleCatalogMutationRejectsActorMismatchBeforeTransaction(t *testing.T) {
	repo, mock, closeDB := testRepository(t)
	defer closeDB()
	scope := mustScope(t, "tenant-role")
	identity := tenant.VerifiedIdentity{Scope: scope, Account: "actor-role", MembershipVersion: 3, PolicyRevision: 1}
	change := Mutation{Actor: "somebody-else", CorrelationID: "role-create-mismatch", OccurredAt: time.Unix(100, 0)}
	catalog := tenant.HostedCatalog{Tenant: scope.Organization, ID: "catalog-one", Name: "One", Version: 1}

	err := repo.CreateRoleCatalog(context.Background(), identity, change, catalog)
	if err == nil || errors.Is(err, tenant.ErrDenied) {
		t.Fatalf("actor mismatch should fail before database authorization, got %v", err)
	}
	assertExpectations(t, mock)
}

func TestImportRoleReleaseRejectsInvalidSignatureBeforeAnyWrite(t *testing.T) {
	repo, mock, closeDB := testRepository(t)
	defer closeDB()
	scope := mustScope(t, "tenant-role")
	identity := tenant.VerifiedIdentity{Scope: scope, Account: "actor-role", MembershipVersion: 3, PolicyRevision: 1}
	change := Mutation{Actor: identity.Account, CorrelationID: "role-import-invalid-signature", OccurredAt: time.Unix(100, 0)}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	content := rolebundle.ReleaseContent{SchemaVersion: rolebundle.SchemaVersion, Version: "1.2.3", Role: team.Role{
		Name: "implementer", Summary: "build", Skills: []string{"go"}, Scope: []string{"internal/**"}, OutOfScope: []string{"secrets/**"},
		Shortcuts: []string{"test"}, Grant: "rw", EscalateTo: []string{"reviewer"}, WIP: 2, Kind: "implementer", Runtime: "codex", Model: "model-x",
		Profile:    team.ModelProfile{ID: "model-x", CostTier: 2, MaxTaskPoints: 5, ContextLimit: 80000, CapabilityTags: []string{"code"}},
		FallbackTo: []string{"senior"}, MaxPoints: 7, Prompt: "inspect, change, verify",
	}, References: []rolebundle.Reference{{Kind: "skill", ID: "go", Version: "v2", Digest: "sha256:" + strings.Repeat("a", 64)}}}
	release, err := rolebundle.Sign(content, "publisher-a", private)
	if err != nil {
		t.Fatal(err)
	}
	release.Signature[0] ^= 0x80
	envelope, err := json.Marshal(release)
	if err != nil {
		t.Fatal(err)
	}
	key := tenant.ExactReleaseKey{Tenant: scope.Organization, CatalogID: "catalog-one", RoleID: "implementer", Version: release.Content.Version, ContentDigest: release.ContentDigest}
	catalog := tenant.HostedCatalog{Tenant: scope.Organization, ID: key.CatalogID, Name: "One", Version: 1}

	mock.ExpectBegin()
	expectTenantBinding(mock, scope)
	mock.ExpectQuery(`SELECT tenant_id,catalog_id,COALESCE\(team_id,''\),name,catalog_version FROM controlplane_role_catalogs WHERE tenant_id=\$1 AND catalog_id=\$2 FOR SHARE`).
		WithArgs(scope.Organization, catalog.ID).
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "catalog_id", "team_id", "name", "catalog_version"}).AddRow(catalog.Tenant, catalog.ID, "", catalog.Name, catalog.Version))
	mock.ExpectQuery(`SELECT state FROM controlplane_organizations WHERE tenant_id=\$1 AND organization_id=\$1 FOR SHARE`).
		WithArgs(scope.Organization).WillReturnRows(sqlmock.NewRows([]string{"state"}).AddRow(tenant.LifecycleActive))
	mock.ExpectQuery(`SELECT state FROM controlplane_accounts WHERE account_id=\$1 FOR SHARE`).
		WithArgs(identity.Account).WillReturnRows(sqlmock.NewRows([]string{"state"}).AddRow(tenant.LifecycleActive))
	mock.ExpectQuery(`SELECT tenant_id,account_id,COALESCE\(team_id,''\),roles,state,version,expires_unix\s+FROM controlplane_memberships WHERE tenant_id=\$1 AND account_id=\$2 FOR SHARE`).
		WithArgs(scope.Organization, identity.Account).
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "account_id", "team_id", "roles", "state", "version", "expires_unix"}).
			AddRow(scope.Organization, identity.Account, "", []byte(`["owner"]`), tenant.LifecycleActive, 3, int64(0)))
	mock.ExpectQuery(`SELECT clock_timestamp\(\)`).WillReturnRows(sqlmock.NewRows([]string{"clock_timestamp"}).AddRow(time.Now().UTC()))
	mock.ExpectExec(`SELECT pg_advisory_xact_lock\(hashtextextended\(\$1,677\)\)`).
		WithArgs("hosted-role:" + string(scope.Organization) + ":" + catalog.ID + ":" + key.RoleID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT key_id,public_key FROM controlplane_role_publishers WHERE tenant_id=\$1 AND catalog_id=\$2 ORDER BY key_id`).
		WithArgs(scope.Organization, catalog.ID, maxRolePublisherKeys).
		WillReturnRows(sqlmock.NewRows([]string{"key_id", "public_key"}).AddRow("publisher-a", []byte(public)))
	mock.ExpectRollback()

	_, err = repo.ImportRoleRelease(context.Background(), identity, change, catalog.ID, tenant.HostedRelease{Key: key, Envelope: envelope})
	if !errors.Is(err, tenant.ErrInvalidRoleRegistry) {
		t.Fatalf("invalid signed release error = %v, want ErrInvalidRoleRegistry", err)
	}
	assertExpectations(t, mock)
}

func TestRoleExportRejectsPublishedEntryWithoutSignedStatus(t *testing.T) {
	key := tenant.ExactReleaseKey{Tenant: "tenant-role", CatalogID: "catalog-one", RoleID: "implementer", Version: "1.2.3", ContentDigest: "sha256:" + strings.Repeat("a", 64)}
	entries := []tenant.HostedLifecycleEntry{
		{Key: key, Sequence: 1, State: tenant.HostedDraft},
		{Key: key, Sequence: 2, State: tenant.HostedReleased},
	}
	if err := validateExportLifecycleHistory(entries, nil); err == nil || !strings.Contains(err.Error(), "hosted state and signed status disagree") {
		t.Fatalf("published lifecycle without a signed status returned %v", err)
	}
}

func TestSetRolePinLocksRoleHistoryBeforePinGeneration(t *testing.T) {
	repo, mock, closeDB := testRepository(t)
	defer closeDB()
	scope := mustScope(t, "tenant-role")
	identity := tenant.VerifiedIdentity{Scope: scope, Account: "actor-role", MembershipVersion: 3, PolicyRevision: 1}
	change := Mutation{Actor: identity.Account, CorrelationID: "role-pin-cas", OccurredAt: time.Unix(100, 0)}
	target := tenant.PinTarget{Tenant: scope.Organization, Kind: tenant.PinOrganization, ScopeID: string(scope.Organization), OrganizationID: scope.Organization}
	now := time.Date(2026, 10, 24, 11, 15, 0, 0, time.UTC)

	mock.ExpectBegin()
	expectTenantBinding(mock, scope)
	mock.ExpectQuery(`SELECT state FROM controlplane_organizations WHERE tenant_id=\$1 AND organization_id=\$1 FOR SHARE`).
		WithArgs(scope.Organization).WillReturnRows(sqlmock.NewRows([]string{"state"}).AddRow(tenant.LifecycleActive))
	mock.ExpectQuery(`SELECT state FROM controlplane_accounts WHERE account_id=\$1 FOR SHARE`).
		WithArgs(identity.Account).WillReturnRows(sqlmock.NewRows([]string{"state"}).AddRow(tenant.LifecycleActive))
	mock.ExpectQuery(`SELECT tenant_id,account_id,COALESCE\(team_id,''\),roles,state,version,expires_unix\s+FROM controlplane_memberships WHERE tenant_id=\$1 AND account_id=\$2 FOR SHARE`).
		WithArgs(scope.Organization, identity.Account).
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "account_id", "team_id", "roles", "state", "version", "expires_unix"}).
			AddRow(scope.Organization, identity.Account, "", []byte(`["owner"]`), tenant.LifecycleActive, 3, int64(0)))
	mock.ExpectQuery(`SELECT clock_timestamp\(\)`).WillReturnRows(sqlmock.NewRows([]string{"clock_timestamp"}).AddRow(now))
	mock.ExpectExec(`SELECT pg_advisory_xact_lock\(hashtextextended\(\$1,677\)\)`).
		WithArgs("hosted-role:" + string(scope.Organization) + ":catalog-one:implementer").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`SELECT pg_advisory_xact_lock\(hashtextextended\(\$1,678\)\)`).
		WithArgs("hosted-pin:" + string(scope.Organization) + ":organization:" + string(scope.Organization) + ":implementer").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT generation,sequence,catalog_id,version,content_digest\s+FROM controlplane_role_pins`).
		WithArgs(scope.Organization, tenant.PinOrganization, string(scope.Organization), "implementer").
		WillReturnRows(sqlmock.NewRows([]string{"generation", "sequence", "catalog_id", "version", "content_digest"}))
	mock.ExpectRollback()

	_, err := repo.SetRolePin(context.Background(), identity, change, target, "implementer", "catalog-one", "1.2.3", "sha256:"+strings.Repeat("a", 64), 1, "rollback")
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("stale pin generation error = %v, want ErrConflict", err)
	}
	assertExpectations(t, mock)
}

func TestRefreshRoleRegistryTimeRechecksMembershipExpiry(t *testing.T) {
	repo, mock, closeDB := testRepository(t)
	defer closeDB()
	scope := mustScope(t, "tenant-role")
	now := time.Date(2026, 10, 24, 11, 15, 0, 0, time.UTC)
	mock.ExpectBegin()
	expectTenantBinding(mock, scope)
	mock.ExpectQuery(`SELECT clock_timestamp\(\)`).WillReturnRows(sqlmock.NewRows([]string{"clock_timestamp"}).AddRow(now))
	mock.ExpectRollback()
	var got error
	err := repo.withTenant(context.Background(), scope, false, func(tx *sql.Tx) error {
		_, got = refreshRoleRegistryTime(context.Background(), tx, tenant.Membership{ExpiresUnix: now.Unix()})
		return got
	})
	if !errors.Is(err, tenant.ErrDenied) {
		t.Fatalf("expired membership refresh error = %v, want ErrDenied", err)
	}
	assertExpectations(t, mock)
}

func TestEnrollRolePublisherPersistsExplicitCatalogTrust(t *testing.T) {
	repo, mock, closeDB := testRepository(t)
	defer closeDB()
	fixture := newHostedRoleFixture(t)
	change := fixture.mutation
	change.CorrelationID = "role-key-enroll"
	mock.ExpectBegin()
	expectTenantBinding(mock, fixture.scope)
	expectRoleCatalog(mock, fixture)
	expectRoleActor(mock, fixture, fixture.now, `["owner"]`, 0)
	mock.ExpectQuery(`SELECT clock_timestamp\(\)`).WillReturnRows(sqlmock.NewRows([]string{"clock_timestamp"}).AddRow(fixture.now))
	mock.ExpectExec(`INSERT INTO controlplane_role_publishers`).
		WithArgs(fixture.scope.Organization, fixture.catalog.ID, "publisher-a", []byte(fixture.public), fixture.now).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO controlplane_role_audit_events`).
		WithArgs(fixture.scope.Organization, change.CorrelationID, change.Actor, change.Device, "publisher.enroll", fixture.catalog.ID+":publisher-a", sqlmock.AnyArg(), "publisher key explicitly enrolled", fixture.now).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	key := tenant.HostedPublisherKey{Tenant: fixture.scope.Organization, CatalogID: fixture.catalog.ID, KeyID: "publisher-a", PublicKey: fixture.public}
	if err := repo.EnrollRolePublisher(context.Background(), fixture.identity, change, fixture.catalog.ID, key); err != nil {
		t.Fatal(err)
	}
	assertExpectations(t, mock)
}

func TestImportRoleReleaseStoresExactEnvelopeAsDraft(t *testing.T) {
	repo, mock, closeDB := testRepository(t)
	defer closeDB()
	fixture := newHostedRoleFixture(t)
	change := fixture.mutation
	change.CorrelationID = "role-release-import"
	mock.ExpectBegin()
	expectTenantBinding(mock, fixture.scope)
	expectRoleCatalog(mock, fixture)
	expectRoleActor(mock, fixture, fixture.now, `["owner"]`, 0)
	mock.ExpectExec(`SELECT pg_advisory_xact_lock\(hashtextextended\(\$1,677\)\)`).
		WithArgs("hosted-role:" + string(fixture.scope.Organization) + ":" + fixture.catalog.ID + ":" + fixture.release.Key.RoleID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectRolePublishers(mock, fixture)
	mock.ExpectQuery(`SELECT 1 FROM controlplane_role_releases`).
		WithArgs(fixture.scope.Organization, fixture.catalog.ID, fixture.release.Key.RoleID, fixture.release.Key.Version).
		WillReturnRows(sqlmock.NewRows([]string{"1"}))
	mock.ExpectQuery(`SELECT clock_timestamp\(\)`).WillReturnRows(sqlmock.NewRows([]string{"clock_timestamp"}).AddRow(fixture.now))
	mock.ExpectExec(`INSERT INTO controlplane_role_releases`).
		WithArgs(fixture.scope.Organization, fixture.catalog.ID, fixture.release.Key.RoleID, fixture.release.Key.Version,
			fixture.release.Key.ContentDigest, fixture.release.Envelope, "publisher-a", fixture.now).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT COALESCE\(MAX\(sequence\),0\) FROM controlplane_role_lifecycle`).
		WithArgs(fixture.scope.Organization, fixture.catalog.ID, fixture.release.Key.RoleID).
		WillReturnRows(sqlmock.NewRows([]string{"coalesce"}).AddRow(uint64(0)))
	mock.ExpectExec(`INSERT INTO controlplane_role_lifecycle`).
		WithArgs(fixture.scope.Organization, fixture.catalog.ID, fixture.release.Key.RoleID, fixture.release.Key.Version, fixture.release.Key.ContentDigest,
			uint64(1), tenant.HostedDraft, nil, nil, fixture.identity.Account, "release imported as draft", fixture.now).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO controlplane_role_audit_events`).
		WithArgs(fixture.scope.Organization, change.CorrelationID, change.Actor, change.Device, "release.import", fixture.release.Key.RoleID+":"+fixture.release.Key.Version, sqlmock.AnyArg(), "signed release imported as draft", fixture.now).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	entry, err := repo.ImportRoleRelease(context.Background(), fixture.identity, change, fixture.catalog.ID, fixture.release)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Key != fixture.release.Key || entry.Sequence != 1 || entry.State != tenant.HostedDraft || entry.Actor != fixture.identity.Account {
		t.Fatalf("imported lifecycle entry = %+v", entry)
	}
	assertExpectations(t, mock)
}

func TestAppendRoleLifecycleValidatesSignedHistoryAndCommits(t *testing.T) {
	repo, mock, closeDB := testRepository(t)
	defer closeDB()
	fixture := newHostedRoleFixture(t)
	change := fixture.mutation
	change.CorrelationID = "role-release-publish"
	mock.ExpectBegin()
	expectTenantBinding(mock, fixture.scope)
	expectRoleCatalog(mock, fixture)
	expectRoleActor(mock, fixture, fixture.now, `["owner"]`, 0)
	mock.ExpectExec(`SELECT pg_advisory_xact_lock\(hashtextextended\(\$1,677\)\)`).
		WithArgs("hosted-role:" + string(fixture.scope.Organization) + ":" + fixture.catalog.ID + ":" + fixture.release.Key.RoleID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT COALESCE\(MAX\(sequence\),0\) FROM controlplane_role_lifecycle`).
		WithArgs(fixture.scope.Organization, fixture.catalog.ID, fixture.release.Key.RoleID).
		WillReturnRows(sqlmock.NewRows([]string{"coalesce"}).AddRow(uint64(1)))
	mock.ExpectQuery(`SELECT release_bytes FROM controlplane_role_releases`).
		WithArgs(fixture.scope.Organization, fixture.catalog.ID, fixture.release.Key.RoleID, fixture.release.Key.Version, fixture.release.Key.ContentDigest).
		WillReturnRows(sqlmock.NewRows([]string{"release_bytes"}).AddRow(fixture.release.Envelope))
	expectRolePublishers(mock, fixture)
	expectRoleLifecycleQueries(mock, fixture)
	mock.ExpectQuery(`SELECT clock_timestamp\(\)`).WillReturnRows(sqlmock.NewRows([]string{"clock_timestamp"}).AddRow(fixture.now))
	mock.ExpectExec(`INSERT INTO controlplane_role_lifecycle`).
		WithArgs(fixture.scope.Organization, fixture.catalog.ID, fixture.release.Key.RoleID, fixture.release.Key.Version, fixture.release.Key.ContentDigest,
			uint64(2), tenant.HostedReleased, sqlmock.AnyArg(), fixture.statusSum, fixture.identity.Account, "publish release", fixture.now).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO controlplane_role_audit_events`).
		WithArgs(fixture.scope.Organization, change.CorrelationID, change.Actor, change.Device, "lifecycle.released", fixture.catalog.ID+":"+fixture.release.Key.RoleID, sqlmock.AnyArg(), "publish release", fixture.now).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	entry, err := repo.AppendRoleLifecycle(context.Background(), fixture.identity, change, fixture.release.Key, 1, tenant.HostedReleased, &fixture.status, "publish release")
	if err != nil {
		t.Fatal(err)
	}
	if entry.Sequence != 2 || entry.State != tenant.HostedReleased || entry.Status == nil || entry.Status.ContentDigest != fixture.release.Key.ContentDigest {
		t.Fatalf("published lifecycle entry = %+v", entry)
	}
	assertExpectations(t, mock)
}

func TestListRoleCatalogsReturnsOnlyTenantAndCurrentTeamScopes(t *testing.T) {
	repo, mock, closeDB := testRepository(t)
	defer closeDB()
	fixture := newHostedRoleFixture(t)
	mock.ExpectBegin()
	expectTenantBinding(mock, fixture.scope)
	expectRoleActor(mock, fixture, fixture.now, `["owner"]`, 0)
	mock.ExpectQuery(`SELECT tenant_id,catalog_id,COALESCE\(team_id,''\),name,catalog_version\s+FROM controlplane_role_catalogs WHERE tenant_id=\$1 AND \(team_id IS NULL OR team_id=\$2\)`).
		WithArgs(fixture.scope.Organization, "", maxRoleCatalogListEntries).
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "catalog_id", "team_id", "name", "catalog_version"}).
			AddRow(fixture.scope.Organization, fixture.catalog.ID, "", fixture.catalog.Name, fixture.catalog.Version))
	mock.ExpectQuery(`SELECT clock_timestamp\(\)`).WillReturnRows(sqlmock.NewRows([]string{"clock_timestamp"}).AddRow(fixture.now))
	mock.ExpectCommit()

	catalogs, err := repo.ListRoleCatalogs(context.Background(), fixture.identity)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalogs) != 1 || catalogs[0] != fixture.catalog {
		t.Fatalf("visible catalogs = %+v, want only %+v", catalogs, fixture.catalog)
	}
	assertExpectations(t, mock)
}

func TestExportRoleReturnsExactReleaseAndSignedHistory(t *testing.T) {
	repo, mock, closeDB := testRepository(t)
	defer closeDB()
	fixture := newHostedRoleFixture(t)
	mock.ExpectBegin()
	expectTenantBinding(mock, fixture.scope)
	expectRoleActor(mock, fixture, fixture.now, `["owner"]`, 0)
	expectRoleCatalog(mock, fixture)
	mock.ExpectQuery(`SELECT release_bytes FROM controlplane_role_releases`).
		WithArgs(fixture.scope.Organization, fixture.catalog.ID, fixture.release.Key.RoleID, fixture.release.Key.Version, fixture.release.Key.ContentDigest).
		WillReturnRows(sqlmock.NewRows([]string{"release_bytes"}).AddRow(fixture.release.Envelope))
	mock.ExpectQuery(`SELECT version,content_digest,release_bytes FROM controlplane_role_releases`).
		WithArgs(fixture.scope.Organization, fixture.catalog.ID, fixture.release.Key.RoleID, maxRoleExportEntries).
		WillReturnRows(sqlmock.NewRows([]string{"version", "content_digest", "release_bytes"}).
			AddRow(fixture.release.Key.Version, fixture.release.Key.ContentDigest, fixture.release.Envelope))
	expectRolePublishers(mock, fixture)
	expectRoleHistory(mock, fixture)
	mock.ExpectQuery(`SELECT clock_timestamp\(\)`).WillReturnRows(sqlmock.NewRows([]string{"clock_timestamp"}).AddRow(fixture.now))
	mock.ExpectCommit()

	exported, err := repo.ExportRole(context.Background(), fixture.identity, fixture.release.Key)
	if err != nil {
		t.Fatal(err)
	}
	if exported.Selected != fixture.release.Key || len(exported.Releases) != 1 || string(exported.Releases[0].Envelope) != string(fixture.release.Envelope) || len(exported.Lifecycle) != 2 {
		t.Fatalf("export did not preserve exact release and history: %+v", exported)
	}
	assertExpectations(t, mock)
}

func TestSetRolePinCommitsExactCASGenerationAndProvenance(t *testing.T) {
	repo, mock, closeDB := testRepository(t)
	defer closeDB()
	fixture := newHostedRoleFixture(t)
	change := fixture.mutation
	change.CorrelationID = "role-pin-first-generation"
	target := tenant.PinTarget{Tenant: fixture.scope.Organization, Kind: tenant.PinOrganization, ScopeID: string(fixture.scope.Organization), OrganizationID: fixture.scope.Organization}
	mock.ExpectBegin()
	expectTenantBinding(mock, fixture.scope)
	expectRoleActor(mock, fixture, fixture.now, `["owner"]`, 0)
	mock.ExpectExec(`SELECT pg_advisory_xact_lock\(hashtextextended\(\$1,677\)\)`).
		WithArgs("hosted-role:" + string(fixture.scope.Organization) + ":" + fixture.catalog.ID + ":" + fixture.release.Key.RoleID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`SELECT pg_advisory_xact_lock\(hashtextextended\(\$1,678\)\)`).
		WithArgs("hosted-pin:" + string(fixture.scope.Organization) + ":organization:" + string(fixture.scope.Organization) + ":" + fixture.release.Key.RoleID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT generation,sequence,catalog_id,version,content_digest\s+FROM controlplane_role_pins`).
		WithArgs(fixture.scope.Organization, tenant.PinOrganization, string(fixture.scope.Organization), fixture.release.Key.RoleID).
		WillReturnRows(sqlmock.NewRows([]string{"generation", "sequence", "catalog_id", "version", "content_digest"}))
	expectRoleCatalog(mock, fixture)
	mock.ExpectQuery(`SELECT release_bytes FROM controlplane_role_releases`).
		WithArgs(fixture.scope.Organization, fixture.catalog.ID, fixture.release.Key.RoleID, fixture.release.Key.Version, fixture.release.Key.ContentDigest).
		WillReturnRows(sqlmock.NewRows([]string{"release_bytes"}).AddRow(fixture.release.Envelope))
	expectRolePublishers(mock, fixture)
	expectRoleHistory(mock, fixture)
	for range 2 {
		mock.ExpectQuery(`SELECT clock_timestamp\(\)`).WillReturnRows(sqlmock.NewRows([]string{"clock_timestamp"}).AddRow(fixture.now))
	}
	mock.ExpectExec(`INSERT INTO controlplane_role_pins`).
		WithArgs(fixture.scope.Organization, tenant.PinOrganization, string(fixture.scope.Organization), fixture.scope.Organization, nil, nil,
			fixture.release.Key.RoleID, uint64(1), uint64(1), fixture.catalog.ID, fixture.release.Key.Version, fixture.release.Key.ContentDigest,
			nil, nil, nil, fixture.identity.Account, "rollback-safe update", fixture.now).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO controlplane_role_audit_events`).
		WithArgs(fixture.scope.Organization, change.CorrelationID, change.Actor, change.Device, "pin.set", string(fixture.scope.Organization)+":"+fixture.release.Key.RoleID,
			sqlmock.AnyArg(), "rollback-safe update", fixture.now).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	pin, err := repo.SetRolePin(context.Background(), fixture.identity, change, target, fixture.release.Key.RoleID, fixture.catalog.ID,
		fixture.release.Key.Version, fixture.release.Key.ContentDigest, 0, "rollback-safe update")
	if err != nil {
		t.Fatal(err)
	}
	if pin.Generation != 1 || pin.Sequence != 1 || pin.Actor != fixture.identity.Account || pin.Target != target || pin.CatalogID != fixture.catalog.ID || pin.Previous != nil {
		t.Fatalf("first role pin = %+v", pin)
	}
	assertExpectations(t, mock)
}

func TestResolveRoleUsesHighestExactPinAndSelectedCatalogTrust(t *testing.T) {
	repo, mock, closeDB := testRepository(t)
	defer closeDB()
	fixture := newHostedRoleFixture(t)
	target := tenant.PinTarget{Tenant: fixture.scope.Organization, Kind: tenant.PinOrganization, ScopeID: string(fixture.scope.Organization), OrganizationID: fixture.scope.Organization}
	mock.ExpectBegin()
	expectTenantBinding(mock, fixture.scope)
	expectRoleActor(mock, fixture, fixture.now, `["owner"]`, 0)
	mock.ExpectQuery(`SELECT scope_kind,scope_id,organization_id,team_id,project_id,catalog_id,version,content_digest,sequence,generation,\s+previous_catalog_id,previous_version,previous_digest,actor_id,reason,pinned_at\s+FROM controlplane_role_pins`).
		WithArgs(fixture.scope.Organization, fixture.release.Key.RoleID, fixture.scope.Organization, "", "").
		WillReturnRows(sqlmock.NewRows([]string{"scope_kind", "scope_id", "organization_id", "team_id", "project_id", "catalog_id", "version", "content_digest", "sequence", "generation", "previous_catalog_id", "previous_version", "previous_digest", "actor_id", "reason", "pinned_at"}).
			AddRow(tenant.PinOrganization, string(fixture.scope.Organization), string(fixture.scope.Organization), nil, nil,
				fixture.catalog.ID, fixture.release.Key.Version, fixture.release.Key.ContentDigest, uint64(2), uint64(2), fixture.catalog.ID, "1.0.0", "sha256:"+strings.Repeat("b", 64),
				fixture.identity.Account, "selected exact release", fixture.now))
	expectRoleCatalog(mock, fixture)
	mock.ExpectQuery(`SELECT release_bytes FROM controlplane_role_releases`).
		WithArgs(fixture.scope.Organization, fixture.catalog.ID, fixture.release.Key.RoleID, fixture.release.Key.Version, fixture.release.Key.ContentDigest).
		WillReturnRows(sqlmock.NewRows([]string{"release_bytes"}).AddRow(fixture.release.Envelope))
	expectRolePublishers(mock, fixture)
	expectRoleHistory(mock, fixture)
	mock.ExpectQuery(`SELECT clock_timestamp\(\)`).WillReturnRows(sqlmock.NewRows([]string{"clock_timestamp"}).AddRow(fixture.now))
	mock.ExpectCommit()

	resolved, err := repo.ResolveRole(context.Background(), fixture.identity, "", fixture.release.Key.RoleID)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Pin.Target != target || resolved.Pin.Generation != 2 || resolved.Pin.Previous == nil || resolved.Source != target || resolved.Release.ContentDigest != fixture.release.Key.ContentDigest || resolved.Lifecycle == nil || resolved.Lifecycle.Status == nil {
		t.Fatalf("resolved exact role provenance = %+v", resolved)
	}
	assertExpectations(t, mock)
}

func TestRolePolicyHelpersRequireActiveTeamAndOwnedDevice(t *testing.T) {
	t.Run("active team accepted", func(t *testing.T) {
		repo, mock, closeDB := testRepository(t)
		defer closeDB()
		scope := mustScope(t, "tenant-role")
		mock.ExpectBegin()
		expectTenantBinding(mock, scope)
		mock.ExpectQuery(`SELECT state FROM controlplane_teams WHERE tenant_id=\$1 AND team_id=\$2 FOR SHARE`).
			WithArgs(scope.Organization, tenant.TeamID("team-blue")).WillReturnRows(sqlmock.NewRows([]string{"state"}).AddRow(tenant.LifecycleActive))
		mock.ExpectCommit()
		err := repo.withTenant(context.Background(), scope, false, func(tx *sql.Tx) error {
			return ensureActiveTeam(context.Background(), tx, scope.Organization, "team-blue", true)
		})
		if err != nil {
			t.Fatal(err)
		}
		assertExpectations(t, mock)
	})
	t.Run("inactive team denied", func(t *testing.T) {
		repo, mock, closeDB := testRepository(t)
		defer closeDB()
		scope := mustScope(t, "tenant-role")
		mock.ExpectBegin()
		expectTenantBinding(mock, scope)
		mock.ExpectQuery(`SELECT state FROM controlplane_teams WHERE tenant_id=\$1 AND team_id=\$2 FOR SHARE`).
			WithArgs(scope.Organization, tenant.TeamID("team-blue")).WillReturnRows(sqlmock.NewRows([]string{"state"}).AddRow(tenant.LifecycleSuspended))
		mock.ExpectRollback()
		err := repo.withTenant(context.Background(), scope, false, func(tx *sql.Tx) error {
			return ensureActiveTeam(context.Background(), tx, scope.Organization, "team-blue", true)
		})
		if !errors.Is(err, tenant.ErrDenied) {
			t.Fatalf("inactive team error = %v, want ErrDenied", err)
		}
		assertExpectations(t, mock)
	})
	t.Run("device owner must match", func(t *testing.T) {
		repo, mock, closeDB := testRepository(t)
		defer closeDB()
		scope := mustScope(t, "tenant-role")
		mock.ExpectBegin()
		expectTenantBinding(mock, scope)
		mock.ExpectQuery(`SELECT account_id,state FROM controlplane_devices\s+WHERE tenant_id=\$1 AND device_id=\$2 FOR SHARE`).
			WithArgs(scope.Organization, tenant.DeviceID("device-one")).
			WillReturnRows(sqlmock.NewRows([]string{"account_id", "state"}).AddRow(tenant.AccountID("different-account"), tenant.LifecycleActive))
		mock.ExpectRollback()
		err := repo.withTenant(context.Background(), scope, false, func(tx *sql.Tx) error {
			return lockRoleDevice(context.Background(), tx, scope.Organization, "actor-role", "device-one", true)
		})
		if !errors.Is(err, tenant.ErrDenied) {
			t.Fatalf("foreign device error = %v, want ErrDenied", err)
		}
		assertExpectations(t, mock)
	})
}

func TestValidatePinTargetChecksCurrentTeamAndProject(t *testing.T) {
	scope := mustScope(t, "tenant-role")
	repo, mock, closeDB := testRepository(t)
	defer closeDB()
	currentTeam := tenant.Membership{Team: "team-blue", Roles: mustTenantRoles(t, tenant.RoleManager)}
	owner := tenant.Membership{Roles: mustTenantRoles(t, tenant.RoleOwner)}
	orgTarget := tenant.PinTarget{Tenant: scope.Organization, Kind: tenant.PinOrganization, ScopeID: string(scope.Organization), OrganizationID: scope.Organization}
	if err := validatePinTarget(context.Background(), nil, scope.Organization, owner, orgTarget); err != nil {
		t.Fatalf("valid organization target error = %v", err)
	}
	badOrgTarget := orgTarget
	badOrgTarget.ScopeID = "other-tenant"
	if err := validatePinTarget(context.Background(), nil, scope.Organization, owner, badOrgTarget); err == nil {
		t.Fatal("malformed organization scope was accepted")
	}
	mock.ExpectBegin()
	expectTenantBinding(mock, scope)
	mock.ExpectQuery(`SELECT state FROM controlplane_teams WHERE tenant_id=\$1 AND team_id=\$2 FOR SHARE`).
		WithArgs(scope.Organization, tenant.TeamID("team-red")).WillReturnRows(sqlmock.NewRows([]string{"state"}).AddRow(tenant.LifecycleActive))
	mock.ExpectQuery(`SELECT state FROM controlplane_projects WHERE tenant_id=\$1 AND project_id=\$2 FOR SHARE`).
		WithArgs(scope.Organization, tenant.ProjectID("project-green")).WillReturnRows(sqlmock.NewRows([]string{"state"}).AddRow(tenant.LifecycleActive))
	mock.ExpectCommit()
	err := repo.withTenant(context.Background(), scope, false, func(tx *sql.Tx) error {
		otherTeam := tenant.PinTarget{Tenant: scope.Organization, Kind: tenant.PinTeam, ScopeID: "team-red", TeamID: "team-red"}
		if err := validatePinTarget(context.Background(), tx, scope.Organization, currentTeam, otherTeam); !errors.Is(err, tenant.ErrDenied) {
			return fmt.Errorf("team outside current membership error = %w, want ErrDenied", err)
		}
		project := tenant.PinTarget{Tenant: scope.Organization, Kind: tenant.PinProject, ScopeID: "project-green", ProjectID: "project-green"}
		return validatePinTarget(context.Background(), tx, scope.Organization, owner, project)
	})
	if err != nil {
		t.Fatal(err)
	}
	assertExpectations(t, mock)
}

func mustTenantRoles(t *testing.T, roles ...tenant.Role) tenant.RoleSet {
	t.Helper()
	value, err := tenant.Roles(roles...)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestLoadRoleActorLocksCurrentTeamAndDeviceThenChecksPermission(t *testing.T) {
	repo, mock, closeDB := testRepository(t)
	defer closeDB()
	fixture := newHostedRoleFixture(t)
	fixture.identity.Device = "device-one"
	fixture.mutation.Device = fixture.identity.Device
	mock.ExpectBegin()
	expectTenantBinding(mock, fixture.scope)
	mock.ExpectQuery("SELECT state FROM controlplane_organizations").
		WithArgs(fixture.scope.Organization).WillReturnRows(sqlmock.NewRows([]string{"state"}).AddRow(tenant.LifecycleActive))
	mock.ExpectQuery("SELECT state FROM controlplane_accounts").
		WithArgs(fixture.identity.Account).WillReturnRows(sqlmock.NewRows([]string{"state"}).AddRow(tenant.LifecycleActive))
	mock.ExpectQuery("FROM controlplane_memberships").
		WithArgs(fixture.scope.Organization, fixture.identity.Account).WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "account_id", "team_id", "roles", "state", "version", "expires_unix"}).
		AddRow(fixture.scope.Organization, fixture.identity.Account, "team-blue", []byte("[\"manager\"]"), tenant.LifecycleActive, fixture.identity.MembershipVersion, int64(0)))
	mock.ExpectQuery("SELECT clock_timestamp").WillReturnRows(sqlmock.NewRows([]string{"clock_timestamp"}).AddRow(fixture.now))
	mock.ExpectQuery("SELECT state FROM controlplane_teams").
		WithArgs(fixture.scope.Organization, tenant.TeamID("team-blue")).WillReturnRows(sqlmock.NewRows([]string{"state"}).AddRow(tenant.LifecycleActive))
	mock.ExpectQuery("SELECT account_id,state FROM controlplane_devices").
		WithArgs(fixture.scope.Organization, tenant.DeviceID("device-one")).
		WillReturnRows(sqlmock.NewRows([]string{"account_id", "state"}).AddRow(fixture.identity.Account, tenant.LifecycleActive))
	mock.ExpectQuery("SELECT clock_timestamp").WillReturnRows(sqlmock.NewRows([]string{"clock_timestamp"}).AddRow(fixture.now))
	mock.ExpectCommit()
	var membership tenant.Membership
	err := repo.withTenant(context.Background(), fixture.scope, false, func(tx *sql.Tx) error {
		var err error
		membership, _, err = loadRoleActor(context.Background(), tx, fixture.identity, &fixture.mutation, tenant.PermissionTeamManage, true)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if membership.Team != "team-blue" || !roleHasPermission(membership, tenant.PermissionTeamManage) {
		t.Fatalf("locked current membership = %+v", membership)
	}
	assertExpectations(t, mock)
}

func TestAuthorizeCatalogTeamRequiresMatchingManager(t *testing.T) {
	repo, mock, closeDB := testRepository(t)
	defer closeDB()
	fixture := newHostedRoleFixture(t)
	catalog := fixture.catalog
	catalog.Team = "team-blue"
	mock.ExpectBegin()
	expectTenantBinding(mock, fixture.scope)
	mock.ExpectQuery("SELECT state FROM controlplane_teams").
		WithArgs(fixture.scope.Organization, catalog.Team).WillReturnRows(sqlmock.NewRows([]string{"state"}).AddRow(tenant.LifecycleActive))
	mock.ExpectCommit()
	err := repo.withTenant(context.Background(), fixture.scope, false, func(tx *sql.Tx) error {
		return authorizeCatalogTeam(context.Background(), tx, catalog, tenant.Membership{Team: catalog.Team, Roles: mustTenantRoles(t, tenant.RoleManager)})
	})
	if err != nil {
		t.Fatalf("matching team manager authorization error = %v", err)
	}
	assertExpectations(t, mock)
	if err := authorizeCatalogRead(catalog, tenant.Membership{Team: "team-red"}); !errors.Is(err, tenant.ErrDenied) {
		t.Fatalf("cross-team read error = %v, want ErrDenied", err)
	}
	if err := validateIdentityMutation(fixture.identity, fixture.mutation, "another-tenant"); err == nil {
		t.Fatal("mutation for a different tenant was accepted")
	}
}

func TestLatestRoleStatusVerifiesHistoricalSignedStatus(t *testing.T) {
	repo, mock, closeDB := testRepository(t)
	defer closeDB()
	fixture := newHostedRoleFixture(t)
	mock.ExpectBegin()
	expectTenantBinding(mock, fixture.scope)
	mock.ExpectQuery("SELECT signed_status_bytes,signed_status_digest FROM controlplane_role_lifecycle").
		WithArgs(fixture.scope.Organization, fixture.catalog.ID, fixture.release.Key.RoleID).
		WillReturnRows(sqlmock.NewRows([]string{"signed_status_bytes", "signed_status_digest"}).AddRow(fixture.statusRaw, []byte(fixture.statusSum)))
	mock.ExpectCommit()
	var got *rolebundle.LifecycleStatus
	err := repo.withTenant(context.Background(), fixture.scope, true, func(tx *sql.Tx) error {
		var err error
		got, err = latestRoleStatus(context.Background(), tx, fixture.scope.Organization, fixture.catalog.ID, fixture.release.Key.RoleID,
			map[string]ed25519.PublicKey{"publisher-a": fixture.public})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Generation != 1 || got.ContentDigest != fixture.release.Key.ContentDigest {
		t.Fatalf("latest signed status = %+v", got)
	}
	assertExpectations(t, mock)
}

func TestLoadHostedReleaseRefusesMissingExactVersion(t *testing.T) {
	repo, mock, closeDB := testRepository(t)
	defer closeDB()
	fixture := newHostedRoleFixture(t)
	mock.ExpectBegin()
	expectTenantBinding(mock, fixture.scope)
	mock.ExpectQuery("SELECT release_bytes FROM controlplane_role_releases").
		WithArgs(fixture.scope.Organization, fixture.catalog.ID, fixture.release.Key.RoleID, fixture.release.Key.Version, fixture.release.Key.ContentDigest).
		WillReturnRows(sqlmock.NewRows([]string{"release_bytes"}))
	mock.ExpectRollback()
	var got error
	err := repo.withTenant(context.Background(), fixture.scope, false, func(tx *sql.Tx) error {
		_, got = loadHostedRelease(context.Background(), tx, fixture.release.Key)
		return got
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing exact release error = %v, want ErrNotFound", err)
	}
	assertExpectations(t, mock)
}

func TestImportRoleReleaseRejectsDuplicateVersionWithoutWrites(t *testing.T) {
	repo, mock, closeDB := testRepository(t)
	defer closeDB()
	fixture := newHostedRoleFixture(t)
	change := fixture.mutation
	change.CorrelationID = "role-release-duplicate"
	mock.ExpectBegin()
	expectTenantBinding(mock, fixture.scope)
	expectRoleCatalog(mock, fixture)
	expectRoleActor(mock, fixture, fixture.now, "[\"owner\"]", 0)
	mock.ExpectExec("SELECT pg_advisory_xact_lock").
		WithArgs("hosted-role:" + string(fixture.scope.Organization) + ":" + fixture.catalog.ID + ":" + fixture.release.Key.RoleID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectRolePublishers(mock, fixture)
	mock.ExpectQuery("SELECT 1 FROM controlplane_role_releases").
		WithArgs(fixture.scope.Organization, fixture.catalog.ID, fixture.release.Key.RoleID, fixture.release.Key.Version).
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
	mock.ExpectRollback()
	_, err := repo.ImportRoleRelease(context.Background(), fixture.identity, change, fixture.catalog.ID, fixture.release)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate exact version error = %v, want ErrConflict", err)
	}
	assertExpectations(t, mock)
}

func TestAppendRoleLifecycleRejectsStaleRoleWideSequence(t *testing.T) {
	repo, mock, closeDB := testRepository(t)
	defer closeDB()
	fixture := newHostedRoleFixture(t)
	change := fixture.mutation
	change.CorrelationID = "role-lifecycle-stale-head"
	mock.ExpectBegin()
	expectTenantBinding(mock, fixture.scope)
	expectRoleCatalog(mock, fixture)
	expectRoleActor(mock, fixture, fixture.now, "[\"owner\"]", 0)
	mock.ExpectExec("SELECT pg_advisory_xact_lock").
		WithArgs("hosted-role:" + string(fixture.scope.Organization) + ":" + fixture.catalog.ID + ":" + fixture.release.Key.RoleID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT COALESCE").
		WithArgs(fixture.scope.Organization, fixture.catalog.ID, fixture.release.Key.RoleID).
		WillReturnRows(sqlmock.NewRows([]string{"coalesce"}).AddRow(uint64(2)))
	mock.ExpectRollback()
	_, err := repo.AppendRoleLifecycle(context.Background(), fixture.identity, change, fixture.release.Key, 1, tenant.HostedRevoked, nil, "stale request")
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("stale lifecycle sequence error = %v, want ErrConflict", err)
	}
	assertExpectations(t, mock)
}

func TestResolveRoleRejectsNoExplicitPin(t *testing.T) {
	repo, mock, closeDB := testRepository(t)
	defer closeDB()
	fixture := newHostedRoleFixture(t)
	mock.ExpectBegin()
	expectTenantBinding(mock, fixture.scope)
	expectRoleActor(mock, fixture, fixture.now, "[\"owner\"]", 0)
	mock.ExpectQuery("SELECT scope_kind,scope_id,organization_id,team_id,project_id,catalog_id").
		WithArgs(fixture.scope.Organization, fixture.release.Key.RoleID, fixture.scope.Organization, "", "").
		WillReturnRows(sqlmock.NewRows([]string{"scope_kind", "scope_id", "organization_id", "team_id", "project_id", "catalog_id", "version", "content_digest", "sequence", "generation", "previous_catalog_id", "previous_version", "previous_digest", "actor_id", "reason", "pinned_at"}))
	mock.ExpectRollback()
	_, err := repo.ResolveRole(context.Background(), fixture.identity, "", fixture.release.Key.RoleID)
	if !errors.Is(err, tenant.ErrInvalidRoleRegistry) {
		t.Fatalf("unconfigured role resolution error = %v, want ErrInvalidRoleRegistry", err)
	}
	assertExpectations(t, mock)
}

func TestExportRoleRejectsCrossTenantExactKeyBeforeTransaction(t *testing.T) {
	repo, mock, closeDB := testRepository(t)
	defer closeDB()
	fixture := newHostedRoleFixture(t)
	key := fixture.release.Key
	key.Tenant = "other-tenant"
	_, err := repo.ExportRole(context.Background(), fixture.identity, key)
	if err == nil {
		t.Fatal("cross-tenant exact release export was accepted")
	}
	assertExpectations(t, mock)
}

func TestHostedLifecycleTransitionsRejectTerminalRevocation(t *testing.T) {
	if !validHostedLifecycleTransition(tenant.HostedDraft, tenant.HostedReleased) ||
		!validHostedLifecycleTransition(tenant.HostedReleased, tenant.HostedDeprecated) ||
		!validHostedLifecycleTransition(tenant.HostedDeprecated, tenant.HostedReleased) ||
		validHostedLifecycleTransition(tenant.HostedRevoked, tenant.HostedReleased) ||
		validHostedLifecycleTransition(tenant.HostedDraft, tenant.HostedDeprecated) {
		t.Fatal("hosted lifecycle transition rules do not enforce the append-only terminal revocation")
	}
}

func TestLoadRoleActorRejectsStaleExpiredAndUnderprivilegedMembership(t *testing.T) {
	cases := []struct {
		name      string
		version   uint64
		roles     string
		expiresAt int64
	}{
		{name: "stale version", version: 2, roles: "[\"owner\"]"},
		{name: "expired", version: 3, roles: "[\"owner\"]", expiresAt: -1},
		{name: "missing permission", version: 3, roles: "[\"developer\"]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo, mock, closeDB := testRepository(t)
			defer closeDB()
			fixture := newHostedRoleFixture(t)
			mock.ExpectBegin()
			expectTenantBinding(mock, fixture.scope)
			mock.ExpectQuery("SELECT state FROM controlplane_organizations").
				WithArgs(fixture.scope.Organization).WillReturnRows(sqlmock.NewRows([]string{"state"}).AddRow(tenant.LifecycleActive))
			mock.ExpectQuery("SELECT state FROM controlplane_accounts").
				WithArgs(fixture.identity.Account).WillReturnRows(sqlmock.NewRows([]string{"state"}).AddRow(tenant.LifecycleActive))
			mock.ExpectQuery("FROM controlplane_memberships").
				WithArgs(fixture.scope.Organization, fixture.identity.Account).WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "account_id", "team_id", "roles", "state", "version", "expires_unix"}).
				AddRow(fixture.scope.Organization, fixture.identity.Account, "", []byte(tc.roles), tenant.LifecycleActive, tc.version, tc.expiresAt))
			mock.ExpectQuery("SELECT clock_timestamp").WillReturnRows(sqlmock.NewRows([]string{"clock_timestamp"}).AddRow(fixture.now))
			mock.ExpectRollback()
			err := repo.withTenant(context.Background(), fixture.scope, false, func(tx *sql.Tx) error {
				_, _, err := loadRoleActor(context.Background(), tx, fixture.identity, nil, tenant.PermissionOrganizationManage, true)
				return err
			})
			if !errors.Is(err, tenant.ErrDenied) {
				t.Fatalf("membership refusal = %v, want ErrDenied", err)
			}
			assertExpectations(t, mock)
		})
	}
}

func TestLoadRolePublishersRejectsMalformedStoredKey(t *testing.T) {
	repo, mock, closeDB := testRepository(t)
	defer closeDB()
	fixture := newHostedRoleFixture(t)
	mock.ExpectBegin()
	expectTenantBinding(mock, fixture.scope)
	mock.ExpectQuery("SELECT key_id,public_key FROM controlplane_role_publishers").
		WithArgs(fixture.scope.Organization, fixture.catalog.ID, maxRolePublisherKeys).
		WillReturnRows(sqlmock.NewRows([]string{"key_id", "public_key"}).AddRow("publisher-a", []byte{1, 2, 3}))
	mock.ExpectRollback()
	var got error
	err := repo.withTenant(context.Background(), fixture.scope, false, func(tx *sql.Tx) error {
		_, got = loadRolePublishers(context.Background(), tx, fixture.scope.Organization, fixture.catalog.ID)
		return got
	})
	if got == nil || err == nil {
		t.Fatalf("malformed stored publisher key returned got=%v err=%v", got, err)
	}
	assertExpectations(t, mock)
}

func TestAuthorizeOrganizationCatalogRequiresOrganizationManager(t *testing.T) {
	fixture := newHostedRoleFixture(t)
	if err := authorizeCatalogTeam(context.Background(), nil, fixture.catalog, tenant.Membership{Roles: mustTenantRoles(t, tenant.RoleOwner)}); err != nil {
		t.Fatalf("organization manager was denied catalog authorization: %v", err)
	}
	if err := authorizeCatalogTeam(context.Background(), nil, fixture.catalog, tenant.Membership{Roles: mustTenantRoles(t, tenant.RoleDeveloper)}); !errors.Is(err, tenant.ErrDenied) {
		t.Fatalf("non-manager organization catalog error = %v, want ErrDenied", err)
	}
	if permissionForCatalog(fixture.catalog) != tenant.PermissionOrganizationManage {
		t.Fatal("organization catalog has incorrect required permission")
	}
	teamCatalog := fixture.catalog
	teamCatalog.Team = "team-blue"
	if permissionForCatalog(teamCatalog) != tenant.PermissionTeamManage {
		t.Fatal("team catalog has incorrect required permission")
	}
}

func TestCreateTeamRoleCatalogRequiresAndLocksCurrentTeam(t *testing.T) {
	repo, mock, closeDB := testRepository(t)
	defer closeDB()
	fixture := newHostedRoleFixture(t)
	fixture.catalog.Team = "team-blue"
	catalog := fixture.catalog
	change := fixture.mutation
	change.CorrelationID = "role-team-catalog-create"
	mock.ExpectBegin()
	expectTenantBinding(mock, fixture.scope)
	mock.ExpectQuery("SELECT state FROM controlplane_organizations").
		WithArgs(fixture.scope.Organization).WillReturnRows(sqlmock.NewRows([]string{"state"}).AddRow(tenant.LifecycleActive))
	mock.ExpectQuery("SELECT state FROM controlplane_accounts").
		WithArgs(fixture.identity.Account).WillReturnRows(sqlmock.NewRows([]string{"state"}).AddRow(tenant.LifecycleActive))
	mock.ExpectQuery("FROM controlplane_memberships").
		WithArgs(fixture.scope.Organization, fixture.identity.Account).WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "account_id", "team_id", "roles", "state", "version", "expires_unix"}).
		AddRow(fixture.scope.Organization, fixture.identity.Account, "team-blue", []byte("[\"manager\"]"), tenant.LifecycleActive, fixture.identity.MembershipVersion, int64(0)))
	mock.ExpectQuery("SELECT clock_timestamp").WillReturnRows(sqlmock.NewRows([]string{"clock_timestamp"}).AddRow(fixture.now))
	mock.ExpectQuery("SELECT state FROM controlplane_teams").
		WithArgs(fixture.scope.Organization, tenant.TeamID("team-blue")).WillReturnRows(sqlmock.NewRows([]string{"state"}).AddRow(tenant.LifecycleActive))
	mock.ExpectQuery("SELECT state FROM controlplane_teams").
		WithArgs(fixture.scope.Organization, tenant.TeamID("team-blue")).WillReturnRows(sqlmock.NewRows([]string{"state"}).AddRow(tenant.LifecycleActive))
	mock.ExpectQuery("SELECT clock_timestamp").WillReturnRows(sqlmock.NewRows([]string{"clock_timestamp"}).AddRow(fixture.now))
	mock.ExpectExec("INSERT INTO controlplane_role_catalogs").
		WithArgs(catalog.Tenant, catalog.ID, catalog.Version, "team-blue", catalog.Name, fixture.now).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO controlplane_role_audit_events").
		WithArgs(fixture.scope.Organization, change.CorrelationID, change.Actor, change.Device, "catalog.create", catalog.ID, sqlmock.AnyArg(), "catalog created", fixture.now).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := repo.CreateRoleCatalog(context.Background(), fixture.identity, change, catalog); err != nil {
		t.Fatal(err)
	}
	assertExpectations(t, mock)
}

func TestEnrollRolePublisherRejectsMalformedPublicKeyWithoutWrite(t *testing.T) {
	repo, mock, closeDB := testRepository(t)
	defer closeDB()
	fixture := newHostedRoleFixture(t)
	change := fixture.mutation
	change.CorrelationID = "role-key-invalid"
	mock.ExpectBegin()
	expectTenantBinding(mock, fixture.scope)
	expectRoleCatalog(mock, fixture)
	expectRoleActor(mock, fixture, fixture.now, "[\"owner\"]", 0)
	mock.ExpectQuery("SELECT clock_timestamp").WillReturnRows(sqlmock.NewRows([]string{"clock_timestamp"}).AddRow(fixture.now))
	mock.ExpectRollback()
	key := tenant.HostedPublisherKey{Tenant: fixture.scope.Organization, CatalogID: fixture.catalog.ID, KeyID: "publisher-a", PublicKey: []byte("short")}
	err := repo.EnrollRolePublisher(context.Background(), fixture.identity, change, fixture.catalog.ID, key)
	if !errors.Is(err, tenant.ErrInvalidRoleRegistry) {
		t.Fatalf("malformed publisher key error = %v, want ErrInvalidRoleRegistry", err)
	}
	assertExpectations(t, mock)
}

func TestPinPermissionAndCurrentTeamTargetMapping(t *testing.T) {
	if permissionForPin(tenant.PinOrganization) != tenant.PermissionOrganizationManage ||
		permissionForPin(tenant.PinTeam) != tenant.PermissionTeamManage ||
		permissionForPin(tenant.PinProject) != tenant.PermissionProjectManage {
		t.Fatal("pin target kinds map to the wrong management permissions")
	}
	repo, mock, closeDB := testRepository(t)
	defer closeDB()
	scope := mustScope(t, "tenant-role")
	membership := tenant.Membership{Team: "team-blue", Roles: mustTenantRoles(t, tenant.RoleManager)}
	target := tenant.PinTarget{Tenant: scope.Organization, Kind: tenant.PinTeam, ScopeID: "team-blue", TeamID: "team-blue"}
	mock.ExpectBegin()
	expectTenantBinding(mock, scope)
	mock.ExpectQuery("SELECT state FROM controlplane_teams").
		WithArgs(scope.Organization, tenant.TeamID("team-blue")).WillReturnRows(sqlmock.NewRows([]string{"state"}).AddRow(tenant.LifecycleActive))
	mock.ExpectCommit()
	err := repo.withTenant(context.Background(), scope, false, func(tx *sql.Tx) error {
		return validatePinTarget(context.Background(), tx, scope.Organization, membership, target)
	})
	if err != nil {
		t.Fatal(err)
	}
	assertExpectations(t, mock)
}

func TestRoleAuditCorrelationConflictFailsTheMutation(t *testing.T) {
	repo, mock, closeDB := testRepository(t)
	defer closeDB()
	fixture := newHostedRoleFixture(t)
	mock.ExpectBegin()
	expectTenantBinding(mock, fixture.scope)
	mock.ExpectExec("INSERT INTO controlplane_role_audit_events").WillReturnError(errors.New("duplicate correlation"))
	mock.ExpectRollback()
	err := repo.withTenant(context.Background(), fixture.scope, false, func(tx *sql.Tx) error {
		return appendRoleAudit(context.Background(), tx, fixture.scope.Organization, fixture.mutation, fixture.now, "catalog.create", fixture.catalog.ID, digest(fixture.catalog), "catalog created")
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate correlation") {
		t.Fatalf("audit correlation conflict returned %v", err)
	}
	assertExpectations(t, mock)
}

func TestReadSnapshotRejectsInvalidScopeAndBeginFailure(t *testing.T) {
	repo, mock, closeDB := testRepository(t)
	defer closeDB()
	if err := repo.withTenantReadSnapshot(context.Background(), tenant.Scope{}, func(*sql.Tx) error { return nil }); err == nil {
		t.Fatal("snapshot accepted an invalid tenant scope")
	}
	mock.ExpectBegin().WillReturnError(errors.New("connection unavailable"))
	scope := mustScope(t, "tenant-role")
	err := repo.withTenantReadSnapshot(context.Background(), scope, func(*sql.Tx) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "connection unavailable") {
		t.Fatalf("snapshot begin failure returned %v", err)
	}
	assertExpectations(t, mock)
}

func TestEnsureActiveRoleIdentityRejectsSuspendedAccount(t *testing.T) {
	repo, mock, closeDB := testRepository(t)
	defer closeDB()
	fixture := newHostedRoleFixture(t)
	mock.ExpectBegin()
	expectTenantBinding(mock, fixture.scope)
	mock.ExpectQuery("SELECT state FROM controlplane_organizations").
		WithArgs(fixture.scope.Organization).WillReturnRows(sqlmock.NewRows([]string{"state"}).AddRow(tenant.LifecycleActive))
	mock.ExpectQuery("SELECT state FROM controlplane_accounts").
		WithArgs(fixture.identity.Account).WillReturnRows(sqlmock.NewRows([]string{"state"}).AddRow(tenant.LifecycleSuspended))
	mock.ExpectRollback()
	err := repo.withTenant(context.Background(), fixture.scope, false, func(tx *sql.Tx) error {
		return ensureActiveRoleIdentity(context.Background(), tx, fixture.scope.Organization, fixture.identity.Account, true)
	})
	if !errors.Is(err, tenant.ErrDenied) {
		t.Fatalf("suspended account error = %v, want ErrDenied", err)
	}
	assertExpectations(t, mock)
}

func TestLoadRoleCatalogRejectsInvalidStoredVersion(t *testing.T) {
	repo, mock, closeDB := testRepository(t)
	defer closeDB()
	fixture := newHostedRoleFixture(t)
	mock.ExpectBegin()
	expectTenantBinding(mock, fixture.scope)
	mock.ExpectQuery("SELECT tenant_id,catalog_id").
		WithArgs(fixture.scope.Organization, fixture.catalog.ID).
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "catalog_id", "team_id", "name", "catalog_version"}).
			AddRow(fixture.catalog.Tenant, fixture.catalog.ID, "", fixture.catalog.Name, 2))
	mock.ExpectRollback()
	err := repo.withTenant(context.Background(), fixture.scope, false, func(tx *sql.Tx) error {
		_, err := loadRoleCatalog(context.Background(), tx, fixture.scope.Organization, fixture.catalog.ID, false)
		return err
	})
	if !errors.Is(err, tenant.ErrInvalidRoleRegistry) {
		t.Fatalf("invalid stored catalog error = %v, want ErrInvalidRoleRegistry", err)
	}
	assertExpectations(t, mock)
}

func TestResolveRoleLoadsAndLocksActiveProjectScope(t *testing.T) {
	repo, mock, closeDB := testRepository(t)
	defer closeDB()
	fixture := newHostedRoleFixture(t)
	projectID := tenant.ProjectID("project-green")
	target := tenant.PinTarget{Tenant: fixture.scope.Organization, Kind: tenant.PinProject, ScopeID: string(projectID), ProjectID: projectID}
	mock.ExpectBegin()
	expectTenantBinding(mock, fixture.scope)
	expectRoleActor(mock, fixture, fixture.now, "[\"owner\"]", 0)
	mock.ExpectQuery("SELECT state FROM controlplane_projects").
		WithArgs(fixture.scope.Organization, projectID).WillReturnRows(sqlmock.NewRows([]string{"state"}).AddRow(tenant.LifecycleActive))
	mock.ExpectQuery("SELECT scope_kind,scope_id,organization_id,team_id,project_id,catalog_id").
		WithArgs(fixture.scope.Organization, fixture.release.Key.RoleID, fixture.scope.Organization, "", projectID).
		WillReturnRows(sqlmock.NewRows([]string{"scope_kind", "scope_id", "organization_id", "team_id", "project_id", "catalog_id", "version", "content_digest", "sequence", "generation", "previous_catalog_id", "previous_version", "previous_digest", "actor_id", "reason", "pinned_at"}).
			AddRow(tenant.PinProject, string(projectID), nil, nil, string(projectID), fixture.catalog.ID, fixture.release.Key.Version, fixture.release.Key.ContentDigest,
				uint64(1), uint64(1), nil, nil, nil, fixture.identity.Account, "project exact pin", fixture.now))
	expectRoleCatalog(mock, fixture)
	mock.ExpectQuery("SELECT release_bytes FROM controlplane_role_releases").
		WithArgs(fixture.scope.Organization, fixture.catalog.ID, fixture.release.Key.RoleID, fixture.release.Key.Version, fixture.release.Key.ContentDigest).
		WillReturnRows(sqlmock.NewRows([]string{"release_bytes"}).AddRow(fixture.release.Envelope))
	expectRolePublishers(mock, fixture)
	expectRoleHistory(mock, fixture)
	mock.ExpectQuery("SELECT clock_timestamp").WillReturnRows(sqlmock.NewRows([]string{"clock_timestamp"}).AddRow(fixture.now))
	mock.ExpectCommit()
	resolved, err := repo.ResolveRole(context.Background(), fixture.identity, projectID, fixture.release.Key.RoleID)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Source != target || resolved.Pin.Target != target || resolved.Release.ContentDigest != fixture.release.Key.ContentDigest {
		t.Fatalf("project role pin provenance = %+v", resolved)
	}
	assertExpectations(t, mock)
}

func TestSetRolePinRefusesRevokedExactReleaseWithoutWriting(t *testing.T) {
	repo, mock, closeDB := testRepository(t)
	defer closeDB()
	fixture := newHostedRoleFixture(t)
	revoked, err := rolebundle.SignStatus(rolebundle.LifecycleStatus{SchemaVersion: rolebundle.SchemaVersion, RoleID: fixture.release.Key.RoleID,
		Version: fixture.release.Key.Version, ContentDigest: fixture.release.Key.ContentDigest, Generation: 2, PreviousStatusDigest: fixture.statusSum,
		State: "revoked", IssuedAt: fixture.now, ExpiresAt: fixture.now.Add(time.Hour), PublisherKeyID: "publisher-a"}, fixture.private)
	if err != nil {
		t.Fatal(err)
	}
	revokedRaw, err := json.Marshal(revoked)
	if err != nil {
		t.Fatal(err)
	}
	revokedDigest, err := rolebundle.LifecycleDigest(revoked)
	if err != nil {
		t.Fatal(err)
	}
	target := tenant.PinTarget{Tenant: fixture.scope.Organization, Kind: tenant.PinOrganization, ScopeID: string(fixture.scope.Organization), OrganizationID: fixture.scope.Organization}
	mock.ExpectBegin()
	expectTenantBinding(mock, fixture.scope)
	expectRoleActor(mock, fixture, fixture.now, "[\"owner\"]", 0)
	mock.ExpectExec("SELECT pg_advisory_xact_lock").
		WithArgs("hosted-role:" + string(fixture.scope.Organization) + ":" + fixture.catalog.ID + ":" + fixture.release.Key.RoleID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("SELECT pg_advisory_xact_lock").
		WithArgs("hosted-pin:" + string(fixture.scope.Organization) + ":organization:" + string(fixture.scope.Organization) + ":" + fixture.release.Key.RoleID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT generation,sequence,catalog_id,version,content_digest").
		WithArgs(fixture.scope.Organization, tenant.PinOrganization, string(fixture.scope.Organization), fixture.release.Key.RoleID).
		WillReturnRows(sqlmock.NewRows([]string{"generation", "sequence", "catalog_id", "version", "content_digest"}))
	expectRoleCatalog(mock, fixture)
	mock.ExpectQuery("SELECT release_bytes FROM controlplane_role_releases").
		WithArgs(fixture.scope.Organization, fixture.catalog.ID, fixture.release.Key.RoleID, fixture.release.Key.Version, fixture.release.Key.ContentDigest).
		WillReturnRows(sqlmock.NewRows([]string{"release_bytes"}).AddRow(fixture.release.Envelope))
	expectRolePublishers(mock, fixture)
	mock.ExpectQuery("SELECT version,content_digest,sequence,hosted_state,signed_status_bytes,signed_status_digest,actor_id,reason,recorded_at").
		WithArgs(fixture.scope.Organization, fixture.catalog.ID, fixture.release.Key.RoleID, maxRoleExportEntries).
		WillReturnRows(sqlmock.NewRows([]string{"version", "content_digest", "sequence", "hosted_state", "signed_status_bytes", "signed_status_digest", "actor_id", "reason", "recorded_at"}).
			AddRow(fixture.release.Key.Version, fixture.release.Key.ContentDigest, uint64(1), tenant.HostedDraft, nil, nil, fixture.identity.Account, "draft", fixture.now).
			AddRow(fixture.release.Key.Version, fixture.release.Key.ContentDigest, uint64(2), tenant.HostedReleased, fixture.statusRaw, []byte(fixture.statusSum), fixture.identity.Account, "published", fixture.now).
			AddRow(fixture.release.Key.Version, fixture.release.Key.ContentDigest, uint64(3), tenant.HostedRevoked, revokedRaw, []byte(revokedDigest), fixture.identity.Account, "revoked", fixture.now))
	mock.ExpectQuery("SELECT clock_timestamp").WillReturnRows(sqlmock.NewRows([]string{"clock_timestamp"}).AddRow(fixture.now))
	mock.ExpectRollback()
	_, err = repo.SetRolePin(context.Background(), fixture.identity, fixture.mutation, target, fixture.release.Key.RoleID, fixture.catalog.ID,
		fixture.release.Key.Version, fixture.release.Key.ContentDigest, 0, "must stay revoked")
	if !errors.Is(err, tenant.ErrInvalidRoleRegistry) {
		t.Fatalf("revoked exact release pin error = %v, want ErrInvalidRoleRegistry", err)
	}
	assertExpectations(t, mock)
}

func TestAppendRoleLifecycleExtendsRoleWideSignedChain(t *testing.T) {
	repo, mock, closeDB := testRepository(t)
	defer closeDB()
	fixture := newHostedRoleFixture(t)
	change := fixture.mutation
	change.CorrelationID = "role-release-deprecate"
	status, err := rolebundle.SignStatus(rolebundle.LifecycleStatus{SchemaVersion: rolebundle.SchemaVersion, RoleID: fixture.release.Key.RoleID,
		Version: fixture.release.Key.Version, ContentDigest: fixture.release.Key.ContentDigest, Generation: 2, PreviousStatusDigest: fixture.statusSum,
		State: "active", IssuedAt: fixture.now, ExpiresAt: fixture.now.Add(time.Hour), PublisherKeyID: "publisher-a"}, fixture.private)
	if err != nil {
		t.Fatal(err)
	}
	statusDigest, err := rolebundle.LifecycleDigest(status)
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectBegin()
	expectTenantBinding(mock, fixture.scope)
	expectRoleCatalog(mock, fixture)
	expectRoleActor(mock, fixture, fixture.now, "[\"owner\"]", 0)
	mock.ExpectExec("SELECT pg_advisory_xact_lock").
		WithArgs("hosted-role:" + string(fixture.scope.Organization) + ":" + fixture.catalog.ID + ":" + fixture.release.Key.RoleID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT COALESCE").
		WithArgs(fixture.scope.Organization, fixture.catalog.ID, fixture.release.Key.RoleID).
		WillReturnRows(sqlmock.NewRows([]string{"coalesce"}).AddRow(uint64(2)))
	mock.ExpectQuery("SELECT release_bytes FROM controlplane_role_releases").
		WithArgs(fixture.scope.Organization, fixture.catalog.ID, fixture.release.Key.RoleID, fixture.release.Key.Version, fixture.release.Key.ContentDigest).
		WillReturnRows(sqlmock.NewRows([]string{"release_bytes"}).AddRow(fixture.release.Envelope))
	expectRolePublishers(mock, fixture)
	mock.ExpectQuery("SELECT sequence,hosted_state,signed_status_bytes,signed_status_digest,actor_id,reason,recorded_at").
		WithArgs(fixture.scope.Organization, fixture.catalog.ID, fixture.release.Key.RoleID, fixture.release.Key.Version, fixture.release.Key.ContentDigest).
		WillReturnRows(sqlmock.NewRows([]string{"sequence", "hosted_state", "signed_status_bytes", "signed_status_digest", "actor_id", "reason", "recorded_at"}).
			AddRow(uint64(2), tenant.HostedReleased, fixture.statusRaw, []byte(fixture.statusSum), fixture.identity.Account, "published", fixture.now))
	mock.ExpectQuery("SELECT signed_status_bytes,signed_status_digest FROM controlplane_role_lifecycle").
		WithArgs(fixture.scope.Organization, fixture.catalog.ID, fixture.release.Key.RoleID).
		WillReturnRows(sqlmock.NewRows([]string{"signed_status_bytes", "signed_status_digest"}).AddRow(fixture.statusRaw, []byte(fixture.statusSum)))
	mock.ExpectQuery("SELECT clock_timestamp").WillReturnRows(sqlmock.NewRows([]string{"clock_timestamp"}).AddRow(fixture.now))
	mock.ExpectExec("INSERT INTO controlplane_role_lifecycle").
		WithArgs(fixture.scope.Organization, fixture.catalog.ID, fixture.release.Key.RoleID, fixture.release.Key.Version, fixture.release.Key.ContentDigest,
			uint64(3), tenant.HostedDeprecated, sqlmock.AnyArg(), statusDigest, fixture.identity.Account, "deprecate release", fixture.now).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO controlplane_role_audit_events").
		WithArgs(fixture.scope.Organization, change.CorrelationID, change.Actor, change.Device, "lifecycle.deprecated", fixture.catalog.ID+":"+fixture.release.Key.RoleID, sqlmock.AnyArg(), "deprecate release", fixture.now).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	entry, err := repo.AppendRoleLifecycle(context.Background(), fixture.identity, change, fixture.release.Key, 2, tenant.HostedDeprecated, &status, "deprecate release")
	if err != nil {
		t.Fatal(err)
	}
	if entry.Sequence != 3 || entry.Status == nil || entry.Status.Generation != 2 || entry.Status.PreviousStatusDigest != fixture.statusSum {
		t.Fatalf("deprecation entry did not extend signed status chain: %+v", entry)
	}
	assertExpectations(t, mock)
}

func TestExportHistoryRequiresContinuousSequenceAndExactSignedIdentity(t *testing.T) {
	fixture := newHostedRoleFixture(t)
	gap := []tenant.HostedLifecycleEntry{{Key: fixture.release.Key, Sequence: 2, State: tenant.HostedDraft}}
	if err := validateExportLifecycleHistory(gap, nil); err == nil || !strings.Contains(err.Error(), "sequence has a gap") {
		t.Fatalf("lifecycle sequence gap error = %v", err)
	}
	orphan := []tenant.HostedLifecycleEntry{{Key: fixture.release.Key, Sequence: 1, State: tenant.HostedReleased, Status: &fixture.status}}
	if err := validateExportLifecycleHistory(orphan, map[string]ed25519.PublicKey{"publisher-a": fixture.public}); err == nil || !strings.Contains(err.Error(), "begin with its draft") {
		t.Fatalf("release without draft error = %v", err)
	}
	mismatched, err := rolebundle.SignStatus(rolebundle.LifecycleStatus{SchemaVersion: rolebundle.SchemaVersion, RoleID: "different-role",
		Version: fixture.release.Key.Version, ContentDigest: fixture.release.Key.ContentDigest, Generation: 1, State: "active",
		IssuedAt: fixture.now.Add(-time.Minute), ExpiresAt: fixture.now.Add(time.Hour), PublisherKeyID: "publisher-a"}, fixture.private)
	if err != nil {
		t.Fatal(err)
	}
	entries := []tenant.HostedLifecycleEntry{
		{Key: fixture.release.Key, Sequence: 1, State: tenant.HostedDraft},
		{Key: fixture.release.Key, Sequence: 2, State: tenant.HostedReleased, Status: &mismatched},
	}
	if err := validateExportLifecycleHistory(entries, map[string]ed25519.PublicKey{"publisher-a": fixture.public}); err == nil || !strings.Contains(err.Error(), "does not match its exact release") {
		t.Fatalf("mismatched signed role status error = %v", err)
	}
}

func TestRoleActorFailsClosedForMissingOrganizationAndMalformedRoles(t *testing.T) {
	t.Run("organization missing", func(t *testing.T) {
		repo, mock, closeDB := testRepository(t)
		defer closeDB()
		fixture := newHostedRoleFixture(t)
		mock.ExpectBegin()
		expectTenantBinding(mock, fixture.scope)
		mock.ExpectQuery("SELECT state FROM controlplane_organizations").
			WithArgs(fixture.scope.Organization).WillReturnRows(sqlmock.NewRows([]string{"state"}))
		mock.ExpectRollback()
		err := repo.withTenant(context.Background(), fixture.scope, false, func(tx *sql.Tx) error {
			return ensureActiveRoleIdentity(context.Background(), tx, fixture.scope.Organization, fixture.identity.Account, true)
		})
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("missing organization error = %v, want ErrNotFound", err)
		}
		assertExpectations(t, mock)
	})
	t.Run("malformed membership roles", func(t *testing.T) {
		repo, mock, closeDB := testRepository(t)
		defer closeDB()
		fixture := newHostedRoleFixture(t)
		mock.ExpectBegin()
		expectTenantBinding(mock, fixture.scope)
		mock.ExpectQuery("SELECT state FROM controlplane_organizations").
			WithArgs(fixture.scope.Organization).WillReturnRows(sqlmock.NewRows([]string{"state"}).AddRow(tenant.LifecycleActive))
		mock.ExpectQuery("SELECT state FROM controlplane_accounts").
			WithArgs(fixture.identity.Account).WillReturnRows(sqlmock.NewRows([]string{"state"}).AddRow(tenant.LifecycleActive))
		mock.ExpectQuery("FROM controlplane_memberships").
			WithArgs(fixture.scope.Organization, fixture.identity.Account).
			WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "account_id", "team_id", "roles", "state", "version", "expires_unix"}).
				AddRow(fixture.scope.Organization, fixture.identity.Account, "", []byte("{"), tenant.LifecycleActive, fixture.identity.MembershipVersion, int64(0)))
		mock.ExpectQuery("SELECT clock_timestamp").WillReturnRows(sqlmock.NewRows([]string{"clock_timestamp"}).AddRow(fixture.now))
		mock.ExpectRollback()
		err := repo.withTenant(context.Background(), fixture.scope, false, func(tx *sql.Tx) error {
			_, _, err := loadRoleActor(context.Background(), tx, fixture.identity, nil, tenant.PermissionOrganizationRead, true)
			return err
		})
		if err == nil || !strings.Contains(err.Error(), "decode current role registry membership") {
			t.Fatalf("malformed membership roles error = %v", err)
		}
		assertExpectations(t, mock)
	})
}

func TestRolePolicyRowsFailClosedWhenTeamOrDeviceIsMissing(t *testing.T) {
	t.Run("team missing", func(t *testing.T) {
		repo, mock, closeDB := testRepository(t)
		defer closeDB()
		scope := mustScope(t, "tenant-role")
		mock.ExpectBegin()
		expectTenantBinding(mock, scope)
		mock.ExpectQuery("SELECT state FROM controlplane_teams").
			WithArgs(scope.Organization, tenant.TeamID("team-missing")).WillReturnRows(sqlmock.NewRows([]string{"state"}))
		mock.ExpectRollback()
		err := repo.withTenant(context.Background(), scope, false, func(tx *sql.Tx) error {
			return ensureActiveTeam(context.Background(), tx, scope.Organization, "team-missing", true)
		})
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("missing team error = %v, want ErrNotFound", err)
		}
		assertExpectations(t, mock)
	})
	t.Run("device missing", func(t *testing.T) {
		repo, mock, closeDB := testRepository(t)
		defer closeDB()
		scope := mustScope(t, "tenant-role")
		mock.ExpectBegin()
		expectTenantBinding(mock, scope)
		mock.ExpectQuery("SELECT account_id,state FROM controlplane_devices").
			WithArgs(scope.Organization, tenant.DeviceID("device-missing")).
			WillReturnRows(sqlmock.NewRows([]string{"account_id", "state"}))
		mock.ExpectRollback()
		err := repo.withTenant(context.Background(), scope, false, func(tx *sql.Tx) error {
			return lockRoleDevice(context.Background(), tx, scope.Organization, "actor-role", "device-missing", true)
		})
		if !errors.Is(err, tenant.ErrDenied) {
			t.Fatalf("missing device error = %v, want ErrDenied", err)
		}
		assertExpectations(t, mock)
	})
}

func TestValidatePinTargetRejectsUnknownAndIncompleteKinds(t *testing.T) {
	scope := mustScope(t, "tenant-role")
	membership := tenant.Membership{Roles: mustTenantRoles(t, tenant.RoleOwner)}
	for _, target := range []tenant.PinTarget{
		{Tenant: scope.Organization, Kind: "unknown", ScopeID: "some-scope"},
		{Tenant: scope.Organization, Kind: tenant.PinTeam, ScopeID: "team-blue"},
		{Tenant: scope.Organization, Kind: tenant.PinProject, ScopeID: "project-green"},
	} {
		if err := validatePinTarget(context.Background(), nil, scope.Organization, membership, target); err == nil {
			t.Fatalf("malformed target was accepted: %+v", target)
		}
	}
}

func TestRoleRegistryReadsAndLocksPropagateStoreFailures(t *testing.T) {
	t.Run("catalog missing", func(t *testing.T) {
		repo, mock, closeDB := testRepository(t)
		defer closeDB()
		fixture := newHostedRoleFixture(t)
		mock.ExpectBegin()
		expectTenantBinding(mock, fixture.scope)
		mock.ExpectQuery("SELECT tenant_id,catalog_id").
			WithArgs(fixture.scope.Organization, fixture.catalog.ID).WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "catalog_id", "team_id", "name", "catalog_version"}))
		mock.ExpectRollback()
		err := repo.withTenant(context.Background(), fixture.scope, false, func(tx *sql.Tx) error {
			_, err := loadRoleCatalog(context.Background(), tx, fixture.scope.Organization, fixture.catalog.ID, false)
			return err
		})
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("missing catalog error = %v, want ErrNotFound", err)
		}
		assertExpectations(t, mock)
	})
	t.Run("role advisory lock failure", func(t *testing.T) {
		repo, mock, closeDB := testRepository(t)
		defer closeDB()
		fixture := newHostedRoleFixture(t)
		mock.ExpectBegin()
		expectTenantBinding(mock, fixture.scope)
		mock.ExpectExec("SELECT pg_advisory_xact_lock").
			WithArgs("hosted-role:" + string(fixture.scope.Organization) + ":" + fixture.catalog.ID + ":" + fixture.release.Key.RoleID).
			WillReturnError(errors.New("lock store unavailable"))
		mock.ExpectRollback()
		err := repo.withTenant(context.Background(), fixture.scope, false, func(tx *sql.Tx) error {
			return lockRole(context.Background(), tx, fixture.scope.Organization, fixture.catalog.ID, fixture.release.Key.RoleID)
		})
		if err == nil || !strings.Contains(err.Error(), "lock store unavailable") {
			t.Fatalf("role lock failure = %v", err)
		}
		assertExpectations(t, mock)
	})
	t.Run("oversized stored release", func(t *testing.T) {
		repo, mock, closeDB := testRepository(t)
		defer closeDB()
		fixture := newHostedRoleFixture(t)
		mock.ExpectBegin()
		expectTenantBinding(mock, fixture.scope)
		mock.ExpectQuery("SELECT release_bytes FROM controlplane_role_releases").
			WithArgs(fixture.scope.Organization, fixture.catalog.ID, fixture.release.Key.RoleID, fixture.release.Key.Version, fixture.release.Key.ContentDigest).
			WillReturnRows(sqlmock.NewRows([]string{"release_bytes"}).AddRow(make([]byte, tenant.MaxHostedRoleEnvelopeBytes+1)))
		mock.ExpectRollback()
		err := repo.withTenant(context.Background(), fixture.scope, false, func(tx *sql.Tx) error {
			_, err := loadHostedRelease(context.Background(), tx, fixture.release.Key)
			return err
		})
		if err == nil || !strings.Contains(err.Error(), "size limit") {
			t.Fatalf("oversized stored release error = %v", err)
		}
		assertExpectations(t, mock)
	})
}

func TestLoadRolePublishersEnforcesCatalogTrustBound(t *testing.T) {
	repo, mock, closeDB := testRepository(t)
	defer closeDB()
	fixture := newHostedRoleFixture(t)
	rows := sqlmock.NewRows([]string{"key_id", "public_key"})
	for index := 0; index < maxRolePublisherKeys; index++ {
		rows.AddRow(fmt.Sprintf("publisher-%03d", index), []byte(fixture.public))
	}
	mock.ExpectBegin()
	expectTenantBinding(mock, fixture.scope)
	mock.ExpectQuery("SELECT key_id,public_key FROM controlplane_role_publishers").
		WithArgs(fixture.scope.Organization, fixture.catalog.ID, maxRolePublisherKeys).WillReturnRows(rows)
	mock.ExpectRollback()
	err := repo.withTenant(context.Background(), fixture.scope, false, func(tx *sql.Tx) error {
		_, err := loadRolePublishers(context.Background(), tx, fixture.scope.Organization, fixture.catalog.ID)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "exceeds its 512 key bound") {
		t.Fatalf("publisher key overflow error = %v", err)
	}
	assertExpectations(t, mock)
}
