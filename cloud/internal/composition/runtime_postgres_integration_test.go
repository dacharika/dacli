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
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/cloud/internal/config"
	"github.com/mlnomadpy/dacli/cloud/internal/deviceoauth"
	"github.com/mlnomadpy/dacli/cloud/internal/devicesession"
	"github.com/mlnomadpy/dacli/cloud/internal/migrations"
	"github.com/mlnomadpy/dacli/cloud/internal/tenant"
	"github.com/mlnomadpy/dacli/cloud/internal/tenantrepo"
	"github.com/mlnomadpy/dacli/internal/cloudsync"
)

// Opt-in composition proof, using only disposable local synthetic identities.
// It does not exercise external OAuth, Keychain, backup restore, or crash recovery.
func TestHostedPostgresComposition(t *testing.T) {
	if os.Getenv("DACLI_QUALIFY_HOSTED_POSTGRES") != "1" {
		t.Skip("run scripts/qualify-hosted-postgres.sh for disposable real PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	const ownerDSN = "postgres://qualification_owner@localhost/qualification?host=/var/run/postgresql&sslmode=disable"
	const appDSN = "postgres://qualification_app@localhost/qualification?host=/var/run/postgresql&sslmode=disable"
	owner, err := sql.Open("postgres", ownerDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = owner.Close() }()
	catalog, err := migrations.Load(os.DirFS("/qualification/migrations"))
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog) != 11 {
		t.Fatalf("qualification requires all eleven migrations, got %d", len(catalog))
	}
	if err = migrations.Run(ctx, migrations.SQLStore{DB: owner}, catalog); err != nil {
		t.Fatal(err)
	}
	// These grants are fixture provisioning; the composed runtime never uses owner.
	if _, err = owner.ExecContext(ctx, `GRANT USAGE ON SCHEMA public TO qualification_app; GRANT SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA public TO qualification_app; GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA public TO qualification_app`); err != nil {
		t.Fatal(err)
	}
	app, err := sql.Open("postgres", appDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Close() }()
	var super, bypass bool
	if err = app.QueryRowContext(ctx, `SELECT rolsuper,rolbypassrls FROM pg_roles WHERE rolname=current_user`).Scan(&super, &bypass); err != nil || super || bypass {
		t.Fatalf("application role must be isolated: super=%v bypass=%v err=%v", super, bypass, err)
	}
	for _, org := range []string{"tenant-a", "tenant-b"} {
		pgCompositionSeed(t, ctx, owner, org)
	}
	repo, err := tenantrepo.Open(ctx, app)
	if err != nil {
		t.Fatal(err)
	}
	a := pgCompositionEnroll(t, ctx, repo, "tenant-a", "device-a")
	revoked := pgCompositionEnroll(t, ctx, repo, "tenant-a", "device-revoked")
	b := pgCompositionEnroll(t, ctx, repo, "tenant-b", "device-b")
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
	envelope := pgCompositionEnvelope(t, a, "event-success")
	accepted := pgCompositionExchange(t, runtime, a, []cloudsync.Envelope{envelope})
	pgCompositionAck(t, accepted, cloudsync.OutcomeAccepted, envelope)
	if count := pgCompositionCount(t, ctx, app, "tenant-a", "controlplane_envelope_inbox_jobs"); count != 1 {
		t.Fatalf("admission persisted %d jobs", count)
	}
	if err = runtime.Cycle(ctx); err != nil {
		t.Fatal(err)
	}
	pgCompositionProposal(t, ctx, app, envelope)
	replay := pgCompositionExchange(t, runtime, a, []cloudsync.Envelope{envelope})
	pgCompositionAck(t, replay, cloudsync.OutcomeDuplicate, envelope)
	if err = runtime.Cycle(ctx); err != nil {
		t.Fatal(err)
	}
	if count := pgCompositionCount(t, ctx, app, "tenant-a", "controlplane_envelope_proposals"); count != 1 {
		t.Fatalf("replay produced %d proposals", count)
	}
	// Both the trigger and actual restricted role must reject proposal mutation.
	for _, statement := range []string{`UPDATE controlplane_envelope_proposals SET state='proposed' WHERE event_id='event-success'`, `DELETE FROM controlplane_envelope_proposals WHERE event_id='event-success'`} {
		tx := pgCompositionTenantTx(t, ctx, app, "tenant-a")
		_, changeErr := tx.ExecContext(ctx, statement)
		_ = tx.Rollback()
		if changeErr == nil {
			t.Fatal("immutable proposal permitted mutation")
		}
	}
	// A foreign signing key cannot impersonate the route's current enrolled key.
	foreign := pgCompositionEnvelope(t, a, "event-foreign")
	if err = cloudsync.Sign(&foreign, b.private); err != nil {
		t.Fatal(err)
	}
	foreignResponse := pgCompositionExchange(t, runtime, a, []cloudsync.Envelope{foreign})
	// Existing exchange maps inbox ErrDenied to this retryable transport refusal;
	// no acknowledgment or immutable identity may be created for the bad signature.
	if foreignResponse.Code != http.StatusServiceUnavailable || !strings.Contains(foreignResponse.Body.String(), `"code":"sync_unavailable"`) {
		t.Fatalf("foreign signing key was not refused: %d %s", foreignResponse.Code, foreignResponse.Body.String())
	}
	if count := pgCompositionCount(t, ctx, app, "tenant-b", "controlplane_envelope_inbox_jobs"); count != 0 {
		t.Fatal("foreign signature crossed tenant boundary")
	}
	// Admit under an active enrolled key, then revoke it before worker replay.
	queued := pgCompositionEnvelope(t, revoked, "event-revoked-key")
	pgCompositionAck(t, pgCompositionExchange(t, runtime, revoked, []cloudsync.Envelope{queued}), cloudsync.OutcomeAccepted, queued)
	tx := pgCompositionTenantTx(t, ctx, app, "tenant-a")
	if _, err = tx.ExecContext(ctx, `UPDATE controlplane_devices SET state=$1,version=version+1 WHERE tenant_id='tenant-a' AND device_id='device-revoked'`, tenant.LifecycleRevoked); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = runtime.Cycle(ctx); err != nil {
		t.Fatal(err)
	}
	if count := pgCompositionCount(t, ctx, app, "tenant-a", "controlplane_envelope_proposals"); count != 1 {
		t.Fatal("revoked enrolled key produced proposal")
	}
	tx = pgCompositionTenantTx(t, ctx, app, "tenant-a")
	var state, reason string
	if err = tx.QueryRowContext(ctx, `SELECT state,last_error_code FROM controlplane_envelope_inbox_jobs WHERE event_id='event-revoked-key'`).Scan(&state, &reason); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	_ = tx.Rollback()
	if state != "pending" || reason != "inbox_processing_failed" {
		t.Fatalf("revoked key not refused during replay: %s %s", state, reason)
	}
	pgCompositionDenied(t, pgCompositionExchange(t, runtime, revoked, nil))
	manager, err := devicesession.New(repo, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = manager.Revoke(ctx, tenant.Scope{Organization: "tenant-a"}, devicesession.Mutation{Actor: "account-tenant-a", Device: a.device, CorrelationID: "revoke-session", OccurredAt: time.Now().UTC()}, a.credential, 1); err != nil {
		t.Fatal(err)
	}
	pgCompositionDenied(t, pgCompositionExchange(t, runtime, a, nil))
	invalid := a
	invalid.credential = bytes.Repeat([]byte{0x99}, 32)
	pgCompositionDenied(t, pgCompositionExchange(t, runtime, invalid, nil))
	// A tenant locator in a bearer is comparison data, never recovered authority.
	forged := b
	forged.org = "tenant-a"
	pgCompositionDenied(t, pgCompositionExchange(t, runtime, forged, nil))
	if pgCompositionCount(t, ctx, app, "tenant-a", "controlplane_envelope_inbox_identities") != 2 || pgCompositionCount(t, ctx, app, "tenant-b", "controlplane_envelope_inbox_identities") != 0 {
		t.Fatal("refused requests changed immutable tenant identities")
	}
	var visible int
	if err = app.QueryRowContext(ctx, `SELECT COUNT(*) FROM controlplane_envelope_proposals`).Scan(&visible); err != nil || visible != 0 {
		t.Fatalf("unbound application connection escaped RLS: count=%d err=%v", visible, err)
	}
	t.Log("real SQLStore schema11, restricted Runtime API, enrolled device inbox replay, immutable proposal and duplicate replay, foreign/revoked keys and session refusals verified")
}

type pgCompositionDevice struct {
	org        string
	device     tenant.DeviceID
	private    ed25519.PrivateKey
	credential []byte
	keyID      string
}

func pgCompositionSeed(t *testing.T, ctx context.Context, owner *sql.DB, org string) {
	t.Helper()
	tx := pgCompositionTenantTx(t, ctx, owner, org)
	defer func() { _ = tx.Rollback() }()
	for _, statement := range []string{
		`INSERT INTO controlplane_accounts(account_id,name,state,version) VALUES('account-'||$1,'Fixture',1,1)`,
		`INSERT INTO controlplane_organizations(tenant_id,organization_id,name,state,version) VALUES($1,$1,'Fixture',1,1)`,
		`INSERT INTO controlplane_memberships(tenant_id,account_id,roles,state,version) VALUES($1,'account-'||$1,'["owner"]',1,1)`,
		`INSERT INTO controlplane_projects(tenant_id,project_id,name,state,version) VALUES($1,'project','Fixture',1,1)`,
		`INSERT INTO controlplane_environments(tenant_id,project_id,environment_id,name,kind,state,version) VALUES($1,'project','environment','Fixture',1,1,1)`,
	} {
		if _, err := tx.ExecContext(ctx, statement, org); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}
func pgCompositionTenantTx(t *testing.T, ctx context.Context, db *sql.DB, org string) *sql.Tx {
	t.Helper()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(ctx, `SELECT set_config('dacli.tenant_id',$1,true)`, org); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	return tx
}
func pgCompositionEnroll(t *testing.T, ctx context.Context, repo *tenantrepo.Repository, org, device string) pgCompositionDevice {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	credential := make([]byte, 32)
	if _, err = rand.Read(credential); err != nil {
		t.Fatal(err)
	}
	flowHash := sha256.Sum256([]byte(org + device))
	flow := base64.RawURLEncoding.EncodeToString(flowHash[:])
	// Opaque fixture bytes stand in for provider flow ciphertext. The repository's
	// real atomic registration/session transaction is exercised, not OAuth login.
	raw := bytes.Repeat([]byte{0x55}, 32)
	if _, err = repo.CreateAuth(ctx, tenant.Scope{Organization: tenant.OrganizationID(org)}, flow, raw, time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	d := tenant.Device{Tenant: tenant.OrganizationID(org), ID: tenant.DeviceID(device), Account: tenant.AccountID("account-" + org), Name: "Fixture", Platform: tenant.PlatformLinux, State: tenant.LifecycleActive, Version: 1}
	copy(d.PublicKey[:], public)
	session := devicesession.Record{Tenant: d.Tenant, ID: devicesession.ID("session-" + device), Account: d.Account, Device: d.ID, CredentialDigest: sha256.Sum256(credential), State: devicesession.StateActive, Version: 1, ExpiresUnix: time.Now().Add(time.Hour).Unix()}
	err = repo.UpdateAuth(ctx, tenant.Scope{Organization: d.Tenant}, flow, func([]byte) ([]byte, *deviceoauth.SessionChange, error) {
		return raw, &deviceoauth.SessionChange{Device: d, Session: session, ExpectedMembershipVersion: 1, CorrelationID: "register-" + device}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(public)
	return pgCompositionDevice{org, d.ID, private, credential, "device-" + hex.EncodeToString(digest[:])}
}
func pgCompositionConfig(t *testing.T, dsn string, serverPublic ed25519.PublicKey) config.Config {
	t.Helper()
	encoded := base64.StdEncoding.EncodeToString(serverPublic)
	hosted := &config.HostedFile{PolicyRevision: 1, DeviceOAuth: &config.DeviceOAuthFile{Issuer: "https://provider.invalid", DeviceURL: "https://provider.invalid/device", TokenURL: "https://provider.invalid/token", IdentityURL: "https://provider.invalid/identity", ClientID: "qualification", Scope: "openid", ServerSigningKeys: []config.ServerSigningKey{{KeyID: "server", PublicKey: encoded}}}}
	for _, org := range []string{"tenant-a", "tenant-b"} {
		hosted.Tenants = append(hosted.Tenants, config.HostedTenantFile{TenantID: org, TrustedKeys: []config.HostedKeyFile{{ProjectID: "project", KeyID: "server", PublicKey: encoded}}})
		hosted.DeviceOAuth.Subjects = append(hosted.DeviceOAuth.Subjects, config.DeviceSubject{TenantID: org, Subject: "fixture-" + org, AccountID: "account-" + org})
		hosted.SyncBindings = append(hosted.SyncBindings, config.SyncBinding{WorkspaceID: "workspace", TenantID: org, ProjectID: "project", EnvironmentID: "environment", RepositoryID: "repository", RepositoryURL: "https://repository.invalid/fixture.git", DefaultBranch: "main", Version: 1, MinimumSchemaVersion: 1, MaximumSchemaVersion: 1})
	}
	limit := config.LimitFile{Capacity: 100, RefillInterval: "1h", MaxKeys: 100, IdleTTL: "2h"}
	file := config.File{Hosted: hosted, Mode: "development", ListenAddress: "127.0.0.1:0", PublicBaseURL: "http://127.0.0.1", RequestTimeout: "10s", ShutdownTimeout: "5s", WorkerInterval: "1s", MaxRequestBytes: 1 << 20, DatabaseURLEnv: "QUALIFICATION_DATABASE", ServiceSecretEnv: "QUALIFICATION_SECRET", ContractMinimum: 1, ContractMaximum: 1, RateLimits: config.RateLimitFile{Identity: limit, TenantRead: limit, Sync: limit}}
	raw, err := json.Marshal(file)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(bytes.NewReader(raw), func(name string) (string, bool) {
		switch name {
		case "QUALIFICATION_DATABASE":
			return dsn, true
		case "QUALIFICATION_SECRET":
			return strings.Repeat("q", 32), true
		}
		return "", false
	})
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}
func pgCompositionEnvelope(t *testing.T, d pgCompositionDevice, event string) cloudsync.Envelope {
	t.Helper()
	envelope := cloudsync.Envelope{SchemaVersion: 1, EventType: "task_proposal", TenantID: d.org, ProjectID: "project", EventID: event, ProducerSequence: 1, OccurredAt: time.Now().UTC(), IdempotencyKey: "idempotency-" + event, ProducerVersion: "qualification", Payload: json.RawMessage(`{"proposal_id":"fixture","title":"Qualification","acceptance":["Verify metadata"],"base_version":0,"proposed_by":"fixture"}`), Integrity: cloudsync.Integrity{Algorithm: "Ed25519", KeyID: d.keyID}}
	if err := cloudsync.Sign(&envelope, d.private); err != nil {
		t.Fatal(err)
	}
	return envelope
}
func pgCompositionExchange(t *testing.T, r *Runtime, d pgCompositionDevice, envelopes []cloudsync.Envelope) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(cloudsync.ExchangeRequest{SchemaVersion: 1, Route: cloudsync.SyncRoute{WorkspaceID: "workspace", TenantID: d.org, ProjectID: "project", EnvironmentID: "environment", RepositoryID: "repository", DeviceID: string(d.device)}, BindingVersion: 1, Envelopes: envelopes})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/sync/exchange", bytes.NewReader(raw))
	request.Header.Set("Authorization", "Bearer "+base64.RawURLEncoding.EncodeToString([]byte(d.org))+"."+base64.RawURLEncoding.EncodeToString(d.credential))
	recorder := httptest.NewRecorder()
	r.API.Handler().ServeHTTP(recorder, request)
	return recorder
}
func pgCompositionAck(t *testing.T, response *httptest.ResponseRecorder, want cloudsync.Outcome, envelope cloudsync.Envelope) {
	t.Helper()
	var out cloudsync.ExchangeResponse
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &out) != nil || len(out.Acknowledged) != 1 || out.Acknowledged[0].Outcome != want || out.Acknowledged[0].EventID != envelope.EventID || out.Acknowledged[0].IdempotencyKey != envelope.IdempotencyKey || out.Acknowledged[0].Sequence != envelope.ProducerSequence {
		t.Fatalf("exchange did not acknowledge exact durable envelope: status=%d body=%s", response.Code, response.Body.String())
	}
}
func pgCompositionDenied(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), `"code":"sync_denied"`) {
		t.Fatalf("expected bounded sync refusal: %d %s", response.Code, response.Body.String())
	}
}
func pgCompositionCount(t *testing.T, ctx context.Context, db *sql.DB, org, table string) int {
	t.Helper()
	switch table {
	case "controlplane_envelope_inbox_jobs", "controlplane_envelope_proposals", "controlplane_envelope_inbox_identities":
	default:
		t.Fatal("unapproved fixture count relation")
	}
	tx := pgCompositionTenantTx(t, ctx, db, org)
	defer func() { _ = tx.Rollback() }()
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}
func pgCompositionProposal(t *testing.T, ctx context.Context, db *sql.DB, envelope cloudsync.Envelope) {
	t.Helper()
	tx := pgCompositionTenantTx(t, ctx, db, envelope.TenantID)
	defer func() { _ = tx.Rollback() }()
	var state, jobState string
	var digest, payload []byte
	err := tx.QueryRowContext(ctx, `SELECT p.state,p.envelope_digest,p.payload,j.state FROM controlplane_envelope_proposals p JOIN controlplane_envelope_inbox_jobs j USING(tenant_id,project_id,event_id) WHERE p.event_id=$1`, envelope.EventID).Scan(&state, &digest, &payload, &jobState)
	if err != nil {
		t.Fatalf("enrolled-device worker did not produce proposal: %v", err)
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(raw)
	var stored, wanted any
	if json.Unmarshal(payload, &stored) != nil || json.Unmarshal(envelope.Payload, &wanted) != nil {
		t.Fatal("proposal payload unavailable")
	}
	storedRaw, _ := json.Marshal(stored)
	wantedRaw, _ := json.Marshal(wanted)
	if state != "proposed" || jobState != "processed" || !bytes.Equal(digest, want[:]) || !bytes.Equal(storedRaw, wantedRaw) {
		t.Fatalf("proposal differs from original immutable envelope: proposal=%s job=%s", state, jobState)
	}
}
