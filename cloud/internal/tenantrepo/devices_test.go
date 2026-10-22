package tenantrepo

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/mlnomadpy/dacli/cloud/internal/deviceoauth"
	"github.com/mlnomadpy/dacli/cloud/internal/devicesession"
	"github.com/mlnomadpy/dacli/cloud/internal/tenant"
)

func TestDeviceReadsOnlyWithinBoundTenantAndValidatesStoredKey(t *testing.T) {
	repo, mock, closeDB := testRepository(t)
	defer closeDB()
	scope := mustScope(t, "tenant-a")
	pub := make([]byte, ed25519.PublicKeySize)
	pub[0] = 7
	mock.ExpectBegin()
	expectTenantBinding(mock, scope)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT account_id,name,platform,public_key,state,version FROM controlplane_devices WHERE tenant_id=$1 AND device_id=$2`)).
		WithArgs(scope.Organization, tenant.DeviceID("device-a")).
		WillReturnRows(sqlmock.NewRows([]string{"account_id", "name", "platform", "public_key", "state", "version"}).AddRow("account-a", "laptop", tenant.PlatformLinux, pub, tenant.LifecycleActive, 2))
	mock.ExpectCommit()
	got, err := repo.Device(context.Background(), scope, "device-a")
	if err != nil || got.PublicKey[0] != 7 || got.Version != 2 || got.Tenant != scope.Organization {
		t.Fatalf("Device=%+v error=%v", got, err)
	}
	mock.ExpectBegin()
	expectTenantBinding(mock, scope)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT account_id,name,platform,public_key,state,version FROM controlplane_devices WHERE tenant_id=$1 AND device_id=$2`)).
		WithArgs(scope.Organization, tenant.DeviceID("device-a")).WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()
	if _, err := repo.Device(context.Background(), scope, "device-a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing Device error=%v", err)
	}
	assertExpectations(t, mock)
}

func TestProjectDeviceKeysRequiresActiveProjectAndKeepsTenantBoundQueries(t *testing.T) {
	repo, mock, closeDB := testRepository(t)
	defer closeDB()
	scope := mustScope(t, "tenant-a")
	mock.ExpectBegin()
	expectTenantBinding(mock, scope)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT account_id,public_key FROM controlplane_devices WHERE tenant_id=$1 AND state=$2 ORDER BY device_id LIMIT 1001`)).
		WithArgs(scope.Organization, tenant.LifecycleActive).
		WillReturnRows(sqlmock.NewRows([]string{"account_id", "public_key"}))
	mock.ExpectCommit()
	mock.ExpectBegin()
	expectTenantBinding(mock, scope)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT tenant_id, project_id, name, state, version
FROM controlplane_projects WHERE tenant_id = $1 AND project_id = $2`)).
		WithArgs(scope.Organization, tenant.ProjectID("project-a")).
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "project_id", "name", "state", "version"}).AddRow(scope.Organization, "project-a", "Project A", tenant.LifecycleSuspended, 3))
	mock.ExpectCommit()
	keys, err := repo.ProjectDeviceKeys(context.Background(), scope, "project-a")
	if !errors.Is(err, ErrConflict) || keys != nil {
		t.Fatalf("suspended project returned keys=%v error=%v", keys, err)
	}
	assertExpectations(t, mock)
}

func TestEnvironmentAccessOwnerUsesCurrentTenantMembershipWithoutAssignment(t *testing.T) {
	repo, mock, closeDB := testRepository(t)
	defer closeDB()
	scope := mustScope(t, "tenant-a")
	roles, _ := json.Marshal([]string{"owner"})
	mock.ExpectBegin()
	expectTenantBinding(mock, scope)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT tenant_id, account_id, COALESCE(team_id, ''), roles, state, version, expires_unix
FROM controlplane_memberships WHERE tenant_id = $1 AND account_id = $2`)).
		WithArgs(scope.Organization, tenant.AccountID("account-a")).
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "account_id", "team_id", "roles", "state", "version", "expires_unix"}).AddRow(scope.Organization, "account-a", "", roles, tenant.LifecycleActive, 4, int64(0)))
	mock.ExpectCommit()
	if err := repo.EnvironmentAccess(context.Background(), scope, "account-a", "environment-a"); err != nil {
		t.Fatalf("owner environment access=%v", err)
	}
	assertExpectations(t, mock)
}

func TestCreateAuthAndUpdateAuthPreserveIdempotentCiphertext(t *testing.T) {
	repo, mock, closeDB := testRepository(t)
	defer closeDB()
	scope := mustScope(t, "tenant-a")
	id := "1234567890123456789012345678901234567890123"
	raw := []byte("encrypted-device-flow")
	expires := time.Now().Add(time.Hour).Unix()
	mock.ExpectBegin()
	expectTenantBinding(mock, scope)
	mock.ExpectExec(regexp.QuoteMeta(`SELECT pg_advisory_xact_lock(hashtextextended($1,616))`)).WithArgs("device-auth:tenant-a").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT COUNT(*) FROM controlplane_device_auth WHERE tenant_id=$1 AND expires_unix>$2`)).WithArgs(scope.Organization, sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO controlplane_device_auth(tenant_id,flow_id,ciphertext,expires_unix) VALUES($1,$2,$3,$4) ON CONFLICT(tenant_id,flow_id) DO NOTHING`)).WithArgs(scope.Organization, id, raw, expires).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT ciphertext FROM controlplane_device_auth WHERE tenant_id=$1 AND flow_id=$2 AND expires_unix>$3`)).WithArgs(scope.Organization, id, sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"ciphertext"}).AddRow(raw))
	mock.ExpectCommit()
	got, err := repo.CreateAuth(context.Background(), scope, id, raw, expires)
	if err != nil || string(got) != string(raw) {
		t.Fatalf("CreateAuth=%q error=%v", got, err)
	}

	mock.ExpectBegin()
	expectTenantBinding(mock, scope)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT ciphertext,version FROM controlplane_device_auth WHERE tenant_id=$1 AND flow_id=$2 AND expires_unix>$3 FOR UPDATE`)).WithArgs(scope.Organization, id, sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"ciphertext", "version"}).AddRow(raw, 4))
	mock.ExpectCommit()
	err = repo.UpdateAuth(context.Background(), scope, id, func(stored []byte) ([]byte, *deviceoauth.SessionChange, error) {
		return append([]byte(nil), stored...), nil, nil
	})
	if err != nil {
		t.Fatalf("idempotent UpdateAuth=%v", err)
	}
	assertExpectations(t, mock)
}

func TestProjectDeviceKeysReturnsOnlyCurrentlyAuthorizedAssignedDevice(t *testing.T) {
	repo, mock, closeDB := testRepository(t)
	defer closeDB()
	scope := mustScope(t, "tenant-a")
	pub := make([]byte, ed25519.PublicKeySize)
	pub[0] = 9
	roles, _ := json.Marshal([]string{"developer"})
	rows := func() *sqlmock.Rows {
		return sqlmock.NewRows([]string{"tenant_id", "account_id", "team_id", "roles", "state", "version", "expires_unix"}).AddRow(scope.Organization, "account-a", "", roles, tenant.LifecycleActive, 2, int64(0))
	}
	expectMembership := func() {
		mock.ExpectBegin()
		expectTenantBinding(mock, scope)
		mock.ExpectQuery(regexp.QuoteMeta(`SELECT tenant_id, account_id, COALESCE(team_id, ''), roles, state, version, expires_unix
FROM controlplane_memberships WHERE tenant_id = $1 AND account_id = $2`)).WithArgs(scope.Organization, tenant.AccountID("account-a")).WillReturnRows(rows())
		mock.ExpectCommit()
	}
	mock.ExpectBegin()
	expectTenantBinding(mock, scope)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT account_id,public_key FROM controlplane_devices WHERE tenant_id=$1 AND state=$2 ORDER BY device_id LIMIT 1001`)).WithArgs(scope.Organization, tenant.LifecycleActive).WillReturnRows(sqlmock.NewRows([]string{"account_id", "public_key"}).AddRow("account-a", pub))
	mock.ExpectCommit()
	mock.ExpectBegin()
	expectTenantBinding(mock, scope)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT tenant_id, project_id, name, state, version
FROM controlplane_projects WHERE tenant_id = $1 AND project_id = $2`)).WithArgs(scope.Organization, tenant.ProjectID("project-a")).WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "project_id", "name", "state", "version"}).AddRow(scope.Organization, "project-a", "Project A", tenant.LifecycleActive, 1))
	mock.ExpectCommit()
	expectMembership() // candidate's current membership
	expectMembership() // Authorizer's independent membership recheck
	expectMembership() // ProjectAccess membership
	expectMembership() // ProjectAccess Authorizer recheck
	mock.ExpectBegin()
	expectTenantBinding(mock, scope)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT EXISTS(SELECT 1 FROM controlplane_project_assignments WHERE tenant_id=$1 AND project_id=$2 AND account_id=$3 AND state=$4)`)).WithArgs(scope.Organization, tenant.ProjectID("project-a"), tenant.AccountID("account-a"), tenant.LifecycleActive).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectCommit()
	keys, err := repo.ProjectDeviceKeys(context.Background(), scope, "project-a")
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(pub)
	want := "device-" + hex.EncodeToString(digest[:])
	if len(keys) != 1 || string(keys[want]) != string(pub) {
		t.Fatalf("authorized project keys=%v", keys)
	}
	assertExpectations(t, mock)
}

func TestUpdateAuthRegistersDeviceAndIssuesSessionAtomically(t *testing.T) {
	repo, mock, closeDB := testRepository(t)
	defer closeDB()
	scope := mustScope(t, "tenant-a")
	id := "1234567890123456789012345678901234567890123"
	oldRaw, nextRaw := []byte("old-encrypted-flow"), []byte("next-encrypted-flow-with-valid-length")
	pub := make([]byte, ed25519.PublicKeySize)
	pub[0] = 3
	var publicKey [32]byte
	copy(publicKey[:], pub)
	device := tenant.Device{Tenant: scope.Organization, ID: "device-a", Account: "account-a", Name: "laptop", Platform: tenant.PlatformLinux, PublicKey: publicKey, State: tenant.LifecycleActive, Version: 1}
	record := devicesession.Record{Tenant: scope.Organization, ID: "session-a", Account: "account-a", Device: "device-a", CredentialDigest: sha256.Sum256([]byte("credential")), State: devicesession.StateActive, Version: 1, ExpiresUnix: time.Now().Add(time.Hour).Unix()}
	change := &deviceoauth.SessionChange{Device: device, Session: record, CorrelationID: "auth-correlation", ExpectedMembershipVersion: 2}
	mock.ExpectBegin()
	expectTenantBinding(mock, scope)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT ciphertext,version FROM controlplane_device_auth WHERE tenant_id=$1 AND flow_id=$2 AND expires_unix>$3 FOR UPDATE`)).WithArgs(scope.Organization, id, sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"ciphertext", "version"}).AddRow(oldRaw, 1))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT state,version,expires_unix FROM controlplane_memberships WHERE tenant_id=$1 AND account_id=$2 FOR UPDATE`)).WithArgs(scope.Organization, tenant.AccountID("account-a")).WillReturnRows(sqlmock.NewRows([]string{"state", "version", "expires_unix"}).AddRow(tenant.LifecycleActive, 2, int64(0)))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT account_id,name,platform,public_key,state,version FROM controlplane_devices WHERE tenant_id=$1 AND device_id=$2 FOR UPDATE`)).WithArgs(scope.Organization, tenant.DeviceID("device-a")).WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO controlplane_devices(tenant_id,device_id,account_id,name,platform,public_key,state,version) VALUES($1,$2,$3,$4,$5,$6,$7,1)`)).WithArgs(scope.Organization, device.ID, device.Account, device.Name, device.Platform, device.PublicKey[:], device.State).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO controlplane_tenant_audit_events`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO controlplane_device_sessions(tenant_id,session_id,account_id,device_id,credential_digest,state,version,expires_unix) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`)).WithArgs(record.Tenant, record.ID, record.Account, record.Device, record.CredentialDigest[:], record.State, record.Version, record.ExpiresUnix).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO controlplane_tenant_audit_events`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE controlplane_device_auth SET ciphertext=$3,version=version+1 WHERE tenant_id=$1 AND flow_id=$2 AND version=$4`)).WithArgs(scope.Organization, id, nextRaw, uint64(1)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	err := repo.UpdateAuth(context.Background(), scope, id, func([]byte) ([]byte, *deviceoauth.SessionChange, error) { return nextRaw, change, nil })
	if err != nil {
		t.Fatalf("atomic auth/device/session update=%v", err)
	}
	assertExpectations(t, mock)
}

func TestProjectAccessRejectsRoleWithoutWriteGrantBeforeAssignmentLookup(t *testing.T) {
	repo, mock, closeDB := testRepository(t)
	defer closeDB()
	scope := mustScope(t, "tenant-a")
	roles, _ := json.Marshal([]string{"billing"})
	for i := 0; i < 2; i++ { // ProjectAccess loads membership, then Authorizer independently reloads it.
		mock.ExpectBegin()
		expectTenantBinding(mock, scope)
		mock.ExpectQuery(regexp.QuoteMeta(`SELECT tenant_id, account_id, COALESCE(team_id, ''), roles, state, version, expires_unix
FROM controlplane_memberships WHERE tenant_id = $1 AND account_id = $2`)).WithArgs(scope.Organization, tenant.AccountID("account-a")).WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "account_id", "team_id", "roles", "state", "version", "expires_unix"}).AddRow(scope.Organization, "account-a", "", roles, tenant.LifecycleActive, 2, int64(0)))
		mock.ExpectCommit()
	}
	err := repo.ProjectAccess(context.Background(), scope, "account-a", "project-a")
	if !errors.Is(err, tenant.ErrDenied) {
		t.Fatalf("billing project write=%v", err)
	}
	assertExpectations(t, mock)
}

func TestEnvironmentAccessRequiresActiveAssignmentForDeveloper(t *testing.T) {
	repo, mock, closeDB := testRepository(t)
	defer closeDB()
	scope := mustScope(t, "tenant-a")
	roles, _ := json.Marshal([]string{"developer"})
	mock.ExpectBegin()
	expectTenantBinding(mock, scope)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT tenant_id, account_id, COALESCE(team_id, ''), roles, state, version, expires_unix
FROM controlplane_memberships WHERE tenant_id = $1 AND account_id = $2`)).WithArgs(scope.Organization, tenant.AccountID("account-a")).WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "account_id", "team_id", "roles", "state", "version", "expires_unix"}).AddRow(scope.Organization, "account-a", "", roles, tenant.LifecycleActive, 2, int64(0)))
	mock.ExpectCommit()
	mock.ExpectBegin()
	expectTenantBinding(mock, scope)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT EXISTS(SELECT 1 FROM controlplane_environment_assignments WHERE tenant_id=$1 AND environment_id=$2 AND account_id=$3 AND state=$4)`)).WithArgs(scope.Organization, tenant.EnvironmentID("environment-a"), tenant.AccountID("account-a"), tenant.LifecycleActive).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectCommit()
	err := repo.EnvironmentAccess(context.Background(), scope, "account-a", "environment-a")
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("unassigned environment access=%v", err)
	}
	assertExpectations(t, mock)
}

func TestDeviceAuthCapacityAndCiphertextBoundsFailClosed(t *testing.T) {
	repo, mock, closeDB := testRepository(t)
	defer closeDB()
	scope := mustScope(t, "tenant-a")
	id := "1234567890123456789012345678901234567890123"
	mock.ExpectBegin()
	expectTenantBinding(mock, scope)
	mock.ExpectExec(regexp.QuoteMeta(`SELECT pg_advisory_xact_lock(hashtextextended($1,616))`)).WithArgs("device-auth:tenant-a").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT COUNT(*) FROM controlplane_device_auth WHERE tenant_id=$1 AND expires_unix>$2`)).WithArgs(scope.Organization, sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(10000))
	mock.ExpectRollback()
	if _, err := repo.CreateAuth(context.Background(), scope, id, []byte("ciphertext"), time.Now().Add(time.Hour).Unix()); !errors.Is(err, deviceoauth.ErrDenied) {
		t.Fatalf("full auth table error=%v", err)
	}

	mock.ExpectBegin()
	expectTenantBinding(mock, scope)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT ciphertext,version FROM controlplane_device_auth WHERE tenant_id=$1 AND flow_id=$2 AND expires_unix>$3 FOR UPDATE`)).WithArgs(scope.Organization, id, sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"ciphertext", "version"}).AddRow([]byte("encrypted flow"), 3))
	mock.ExpectRollback()
	err := repo.UpdateAuth(context.Background(), scope, id, func([]byte) ([]byte, *deviceoauth.SessionChange, error) { return []byte("too short"), nil, nil })
	if !errors.Is(err, deviceoauth.ErrDenied) {
		t.Fatalf("short replacement ciphertext error=%v", err)
	}
	assertExpectations(t, mock)
}

func TestUpdateAuthTreatsExpiredFlowAsDenied(t *testing.T) {
	repo, mock, closeDB := testRepository(t)
	defer closeDB()
	scope := mustScope(t, "tenant-a")
	id := "1234567890123456789012345678901234567890123"
	mock.ExpectBegin()
	expectTenantBinding(mock, scope)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT ciphertext,version FROM controlplane_device_auth WHERE tenant_id=$1 AND flow_id=$2 AND expires_unix>$3 FOR UPDATE`)).WithArgs(scope.Organization, id, sqlmock.AnyArg()).WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()
	err := repo.UpdateAuth(context.Background(), scope, id, func(raw []byte) ([]byte, *deviceoauth.SessionChange, error) { return raw, nil, nil })
	if !errors.Is(err, deviceoauth.ErrDenied) {
		t.Fatalf("expired flow error=%v", err)
	}
	assertExpectations(t, mock)
}

func TestUpdateAuthPropagatesValidatedFlowMutationRefusal(t *testing.T) {
	repo, mock, closeDB := testRepository(t)
	defer closeDB()
	scope := mustScope(t, "tenant-a")
	id := "1234567890123456789012345678901234567890123"
	mock.ExpectBegin()
	expectTenantBinding(mock, scope)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT ciphertext,version FROM controlplane_device_auth WHERE tenant_id=$1 AND flow_id=$2 AND expires_unix>$3 FOR UPDATE`)).WithArgs(scope.Organization, id, sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"ciphertext", "version"}).AddRow([]byte("encrypted flow"), 2))
	mock.ExpectRollback()
	err := repo.UpdateAuth(context.Background(), scope, id, func([]byte) ([]byte, *deviceoauth.SessionChange, error) { return nil, nil, deviceoauth.ErrDenied })
	if !errors.Is(err, deviceoauth.ErrDenied) {
		t.Fatalf("mutation refusal=%v", err)
	}
	assertExpectations(t, mock)
}

func TestCreateAuthFailsClosedWhenActiveFlowCountIsUnavailable(t *testing.T) {
	repo, mock, closeDB := testRepository(t)
	defer closeDB()
	scope := mustScope(t, "tenant-a")
	mock.ExpectBegin()
	expectTenantBinding(mock, scope)
	mock.ExpectExec(regexp.QuoteMeta(`SELECT pg_advisory_xact_lock(hashtextextended($1,616))`)).WithArgs("device-auth:tenant-a").WillReturnResult(sqlmock.NewResult(0, 1))
	countFailure := errors.New("count unavailable")
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT COUNT(*) FROM controlplane_device_auth WHERE tenant_id=$1 AND expires_unix>$2`)).WithArgs(scope.Organization, sqlmock.AnyArg()).WillReturnError(countFailure)
	mock.ExpectRollback()
	_, err := repo.CreateAuth(context.Background(), scope, "1234567890123456789012345678901234567890123", []byte("ciphertext"), time.Now().Add(time.Hour).Unix())
	if !errors.Is(err, countFailure) {
		t.Fatalf("auth capacity query error=%v", err)
	}
	assertExpectations(t, mock)
}

func TestProjectDeviceKeysRejectsMalformedAndUnboundedCandidateSets(t *testing.T) {
	for _, tc := range []struct {
		name            string
		count, keyBytes int
	}{
		{name: "malformed public key", count: 1, keyBytes: 31},
		{name: "candidate cap", count: 1001, keyBytes: ed25519.PublicKeySize},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, mock, closeDB := testRepository(t)
			defer closeDB()
			scope := mustScope(t, "tenant-a")
			rows := sqlmock.NewRows([]string{"account_id", "public_key"})
			for i := 0; i < tc.count; i++ {
				key := make([]byte, tc.keyBytes)
				if tc.keyBytes == ed25519.PublicKeySize {
					key[0] = 1
				}
				rows = rows.AddRow("account-a", key)
			}
			mock.ExpectBegin()
			expectTenantBinding(mock, scope)
			mock.ExpectQuery(regexp.QuoteMeta(`SELECT account_id,public_key FROM controlplane_devices WHERE tenant_id=$1 AND state=$2 ORDER BY device_id LIMIT 1001`)).WithArgs(scope.Organization, tenant.LifecycleActive).WillReturnRows(rows)
			mock.ExpectRollback()
			keys, err := repo.ProjectDeviceKeys(context.Background(), scope, "project-a")
			if !errors.Is(err, ErrConflict) || keys != nil {
				t.Fatalf("invalid candidate set keys=%v error=%v", keys, err)
			}
			assertExpectations(t, mock)
		})
	}
}

func TestCreateAuthRejectsMalformedFlowBeforeLocking(t *testing.T) {
	repo, mock, closeDB := testRepository(t)
	defer closeDB()
	scope := mustScope(t, "tenant-a")
	mock.ExpectBegin()
	expectTenantBinding(mock, scope)
	mock.ExpectRollback()
	if _, err := repo.CreateAuth(context.Background(), scope, "short", []byte("ciphertext"), time.Now().Add(time.Hour).Unix()); !errors.Is(err, deviceoauth.ErrDenied) {
		t.Fatalf("malformed auth flow=%v", err)
	}
	assertExpectations(t, mock)
}
