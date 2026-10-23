package composition

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/cloud/internal/envelopeworker"
	"github.com/mlnomadpy/dacli/cloud/internal/migrations"
	"github.com/mlnomadpy/dacli/cloud/internal/tenant"
	"github.com/mlnomadpy/dacli/cloud/internal/tenantrepo"
	"github.com/mlnomadpy/dacli/internal/cloudsync"
)

// This opt-in test is driven in separate processes around an immediate
// PostgreSQL stop/restart and a whole-database pg_dump/pg_restore by
// scripts/qualify-hosted-postgres.sh. Fixture secrets live only in its private
// container tmpfs.
func TestHostedPostgresRecovery(t *testing.T) {
	if os.Getenv("DACLI_QUALIFY_HOSTED_POSTGRES") != "1" {
		t.Skip("run scripts/qualify-hosted-postgres.sh for disposable real PostgreSQL")
	}
	phase := os.Getenv("DACLI_QUALIFY_RECOVERY_PHASE")
	if phase == "" {
		t.Fatal("recovery phase is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	switch phase {
	case "seed":
		pgRecoverySeed(t, ctx)
	case "recover":
		pgRecoveryReplay(t, ctx)
	case "snapshot":
		pgRecoverySnapshot(t, ctx, "qualification_recovery", "/qualification/recovery-snapshot.json")
	case "restored":
		pgRecoveryCompareSnapshot(t, ctx, "qualification_restored", "/qualification/recovery-snapshot.json")
		pgRecoveryReplayDuplicate(t, ctx, "qualification_restored")
		pgRecoveryCheckTenantIsolation(t, ctx, "qualification_restored")
	default:
		t.Fatalf("unknown recovery phase %q", phase)
	}
}

type pgRecoveryFixture struct {
	Device         string             `json:"device"`
	PrivateKey     string             `json:"private_key"`
	Credential     string             `json:"credential"`
	KeyID          string             `json:"key_id"`
	DeviceB        string             `json:"device_b"`
	PrivateKeyB    string             `json:"private_key_b"`
	CredentialB    string             `json:"credential_b"`
	KeyIDB         string             `json:"key_id_b"`
	ServerPublic   string             `json:"server_public"`
	LeaseUntil     int64              `json:"lease_until_unix_milli"`
	EnvelopeA      cloudsync.Envelope `json:"envelope_a"`
	EnvelopeB      cloudsync.Envelope `json:"envelope_b"`
	EnvelopePoison cloudsync.Envelope `json:"envelope_poison"`
}

const (
	pgRecoveryEvent = "event-crash-recovery"
	pgRecoveryOrg   = "tenant-a"
	pgRecoveryDB    = "qualification_recovery"
	pgRestoredDB    = "qualification_restored"
)

func pgRecoverySeed(t *testing.T, ctx context.Context) {
	t.Helper()
	const ownerDSN = "postgres://qualification_owner@/qualification_recovery?host=/var/run/postgresql&sslmode=disable"
	const appDSN = "postgres://qualification_app@/qualification_recovery?host=/var/run/postgresql&sslmode=disable"
	owner := pgRecoveryOpen(t, ctx, ownerDSN)
	defer func() { _ = owner.Close() }()
	catalog, err := migrations.Load(os.DirFS("/qualification/migrations"))
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog) != 11 {
		t.Fatalf("recovery qualification requires all eleven migrations, got %d", len(catalog))
	}
	if err = migrations.Run(ctx, migrations.SQLStore{DB: owner}, catalog); err != nil {
		t.Fatal(err)
	}
	if _, err = owner.ExecContext(ctx, `GRANT USAGE ON SCHEMA public TO qualification_app; GRANT SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA public TO qualification_app; GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA public TO qualification_app`); err != nil {
		t.Fatal(err)
	}
	app := pgRecoveryOpen(t, ctx, appDSN)
	defer func() { _ = app.Close() }()
	for _, org := range []string{"tenant-a", "tenant-b"} {
		pgCompositionSeed(t, ctx, owner, org)
	}
	repo, err := tenantrepo.Open(ctx, app)
	if err != nil {
		t.Fatal(err)
	}
	device := pgCompositionEnroll(t, ctx, repo, pgRecoveryOrg, "device-recovery")
	deviceB := pgCompositionEnroll(t, ctx, repo, "tenant-b", "device-recovery-b")
	serverPublic, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	fixture := pgRecoveryFixture{
		Device:         string(device.device),
		PrivateKey:     base64.RawStdEncoding.EncodeToString(device.private),
		Credential:     base64.RawStdEncoding.EncodeToString(device.credential),
		KeyID:          device.keyID,
		DeviceB:        string(deviceB.device),
		PrivateKeyB:    base64.RawStdEncoding.EncodeToString(deviceB.private),
		CredentialB:    base64.RawStdEncoding.EncodeToString(deviceB.credential),
		KeyIDB:         deviceB.keyID,
		ServerPublic:   base64.StdEncoding.EncodeToString(serverPublic),
		EnvelopeA:      pgRecoveryEnvelope(t, device, pgRecoveryEvent),
		EnvelopeB:      pgRecoveryEnvelope(t, deviceB, "event-tenant-b"),
		EnvelopePoison: pgRecoveryEnvelope(t, device, "event-outbox-poison"),
	}
	cfg := pgCompositionConfig(t, appDSN, serverPublic)
	runtime, err := Open(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	envelope := fixture.EnvelopeA
	pgCompositionAck(t, pgCompositionExchange(t, runtime, device, []cloudsync.Envelope{envelope}), cloudsync.OutcomeAccepted, envelope)
	scope := tenant.Scope{Organization: tenant.OrganizationID(pgRecoveryOrg)}
	if err = runtime.repository.Enqueue(ctx, scope, "outbox-recovery", envelope, time.Now().UTC()); err != nil {
		_ = runtime.Close()
		t.Fatal(err)
	}
	if err = runtime.repository.Enqueue(ctx, scope, "outbox-poison", fixture.EnvelopePoison, time.Now().UTC()); err != nil {
		_ = runtime.Close()
		t.Fatal(err)
	}
	envelopeB := fixture.EnvelopeB
	pgCompositionAck(t, pgCompositionExchange(t, runtime, deviceB, []cloudsync.Envelope{envelopeB}), cloudsync.OutcomeAccepted, envelopeB)
	standaloneOutbox := pgRecoveryEnvelope(t, deviceB, "event-outbox-only")
	scopeB := tenant.Scope{Organization: tenant.OrganizationID("tenant-b")}
	if err = runtime.repository.Enqueue(ctx, scopeB, "outbox-standalone-b", standaloneOutbox, time.Now().UTC()); err != nil {
		_ = runtime.Close()
		t.Fatal(err)
	}
	if err = runtime.repository.ProcessInbox(ctx, scopeB, runtime.inboxKeys, 5); err != nil {
		_ = runtime.Close()
		t.Fatal(err)
	}
	pgCompositionProposal(t, ctx, app, envelopeB, deviceB.device)
	claimTime := time.Now().UTC()
	claimed, err := runtime.repository.ClaimInbox(ctx, scope, claimTime, claimTime.Add(2*time.Second), 10)
	if err != nil {
		_ = runtime.Close()
		t.Fatal(err)
	}
	if len(claimed) != 1 || claimed[0].EventID != pgRecoveryEvent || claimed[0].Attempts != 1 || claimed[0].Fence != 1 {
		_ = runtime.Close()
		t.Fatalf("pre-crash inbox claim=%+v; want one committed attempt 1 fence 1", claimed)
	}
	fixture.LeaseUntil = pgRecoveryJobDeadline(t, ctx, app)
	pgRecoveryWriteFixture(t, fixture)
	if err = runtime.Close(); err != nil {
		t.Fatal(err)
	}
}

func pgRecoveryReplay(t *testing.T, ctx context.Context) {
	t.Helper()
	device, deviceB, serverPublic, expectedLeaseUntil := pgRecoveryLoadDevices(t)
	appDSN := pgRecoveryDSN("qualification_app", pgRecoveryDB)
	app := pgRecoveryOpen(t, ctx, appDSN)
	defer func() { _ = app.Close() }()
	cfg := pgCompositionConfig(t, appDSN, serverPublic)
	runtime, err := Open(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = runtime.Close() }()
	deadline := pgRecoveryJobDeadline(t, ctx, app)
	if deadline != expectedLeaseUntil {
		t.Fatalf("immediate restart changed the committed lease deadline: got=%d want=%d", deadline, expectedLeaseUntil)
	}
	if wait := time.Until(time.UnixMilli(deadline)) + 25*time.Millisecond; wait > 0 {
		if wait > 3*time.Second {
			t.Fatalf("unexpected recovery lease duration: %s", wait)
		}
		time.Sleep(wait)
	}
	if err = runtime.Cycle(ctx); err != nil {
		t.Fatal(err)
	}
	pgRecoveryExerciseOutboxDelivery(t, ctx, runtime.repository, app)
	pgRecoveryAssertJob(t, ctx, app, "processed", 2, 2)
	envelope := pgRecoveryLoadEnvelopes(t).EnvelopeA
	pgCompositionProposal(t, ctx, app, envelope, device.device)
	pgCompositionAck(t, pgCompositionExchange(t, runtime, device, []cloudsync.Envelope{envelope}), cloudsync.OutcomeDuplicate, envelope)
	if err = runtime.Cycle(ctx); err != nil {
		t.Fatal(err)
	}
	if got := pgCompositionCount(t, ctx, app, pgRecoveryOrg, "controlplane_envelope_proposals"); got != 1 {
		t.Fatalf("recovery replay produced %d proposals, want 1", got)
	}
	if got := pgCompositionCount(t, ctx, app, "tenant-b", "controlplane_envelope_proposals"); got != 1 {
		t.Fatalf("second-tenant recovery fixture has %d proposals, want 1", got)
	}
	pgCompositionProposal(t, ctx, app, pgRecoveryLoadEnvelopes(t).EnvelopeB, deviceB.device)
}

func pgRecoveryReplayDuplicate(t *testing.T, ctx context.Context, dbname string) {
	t.Helper()
	device, deviceB, serverPublic, _ := pgRecoveryLoadDevices(t)
	appDSN := pgRecoveryDSN("qualification_app", dbname)
	app := pgRecoveryOpen(t, ctx, appDSN)
	defer func() { _ = app.Close() }()
	cfg := pgCompositionConfig(t, appDSN, serverPublic)
	runtime, err := Open(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = runtime.Close() }()
	fixture := pgRecoveryLoadEnvelopes(t)
	envelope := fixture.EnvelopeA
	pgCompositionAck(t, pgCompositionExchange(t, runtime, device, []cloudsync.Envelope{envelope}), cloudsync.OutcomeDuplicate, envelope)
	envelopeB := fixture.EnvelopeB
	pgCompositionAck(t, pgCompositionExchange(t, runtime, deviceB, []cloudsync.Envelope{envelopeB}), cloudsync.OutcomeDuplicate, envelopeB)
	if err = runtime.Cycle(ctx); err != nil {
		t.Fatal(err)
	}
	if got := pgCompositionCount(t, ctx, app, pgRecoveryOrg, "controlplane_envelope_proposals"); got != 1 {
		t.Fatalf("restored duplicate replay produced %d proposals, want 1", got)
	}
	if got := pgCompositionCount(t, ctx, app, "tenant-b", "controlplane_envelope_proposals"); got != 1 {
		t.Fatalf("restored tenant-b duplicate replay produced %d proposals, want 1", got)
	}
}

func pgRecoverySnapshot(t *testing.T, ctx context.Context, dbname, path string) {
	t.Helper()
	app := pgRecoveryOpen(t, ctx, pgRecoveryDSN("qualification_app", dbname))
	defer func() { _ = app.Close() }()
	snapshot := make(map[string][]string)
	snapshot["controlplane_schema_migrations"] = pgRecoveryRows(t, ctx, app, `SELECT to_jsonb(m)::text FROM controlplane_schema_migrations m ORDER BY version`)
	snapshot["controlplane_accounts"] = pgRecoveryRows(t, ctx, app, `SELECT to_jsonb(r)::text FROM controlplane_accounts r ORDER BY account_id`)
	for _, relation := range []string{
		"controlplane_organizations",
		"controlplane_projects",
		"controlplane_environments",
		"controlplane_devices",
		"controlplane_device_sessions",
		"controlplane_memberships",
		"controlplane_tenant_audit_events",
		"controlplane_envelope_streams",
		"controlplane_envelope_inbox_identities",
		"controlplane_envelope_inbox_payloads",
		"controlplane_envelope_inbox_jobs",
		"controlplane_envelope_proposals",
		"controlplane_envelope_outbox",
		"controlplane_envelope_audit_events",
	} {
		var rows []string
		for _, org := range []string{"tenant-a", "tenant-b"} {
			tx := pgCompositionTenantTx(t, ctx, app, org)
			rows = append(rows, pgRecoveryRows(t, ctx, tx, "SELECT to_jsonb(r)::text FROM "+relation+" r WHERE tenant_id=$1 ORDER BY to_jsonb(r)::text", org)...)
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
		}
		snapshot[relation] = rows
	}
	if len(snapshot["controlplane_schema_migrations"]) != 11 || len(snapshot["controlplane_accounts"]) != 2 || len(snapshot["controlplane_organizations"]) != 2 || len(snapshot["controlplane_projects"]) != 2 || len(snapshot["controlplane_environments"]) != 2 || len(snapshot["controlplane_devices"]) != 2 || len(snapshot["controlplane_device_sessions"]) != 2 || len(snapshot["controlplane_memberships"]) != 2 || len(snapshot["controlplane_tenant_audit_events"]) < 4 || len(snapshot["controlplane_envelope_streams"]) != 2 || len(snapshot["controlplane_envelope_inbox_identities"]) != 2 || len(snapshot["controlplane_envelope_inbox_payloads"]) != 2 || len(snapshot["controlplane_envelope_inbox_jobs"]) != 2 || len(snapshot["controlplane_envelope_proposals"]) != 2 || len(snapshot["controlplane_envelope_outbox"]) != 3 || len(snapshot["controlplane_envelope_audit_events"]) < 3 {
		t.Fatalf("snapshot lacks required durable recovery evidence: %+v", snapshot)
	}
	pgRecoveryAssertStandaloneOutbox(t, ctx, app)
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func pgRecoveryCompareSnapshot(t *testing.T, ctx context.Context, dbname, path string) {
	t.Helper()
	wantRaw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var want map[string][]string
	if err = json.Unmarshal(wantRaw, &want); err != nil {
		t.Fatal(err)
	}
	app := pgRecoveryOpen(t, ctx, pgRecoveryDSN("qualification_app", dbname))
	defer func() { _ = app.Close() }()
	got := make(map[string][]string)
	got["controlplane_schema_migrations"] = pgRecoveryRows(t, ctx, app, `SELECT to_jsonb(m)::text FROM controlplane_schema_migrations m ORDER BY version`)
	got["controlplane_accounts"] = pgRecoveryRows(t, ctx, app, `SELECT to_jsonb(r)::text FROM controlplane_accounts r ORDER BY account_id`)
	for _, relation := range []string{
		"controlplane_organizations",
		"controlplane_projects",
		"controlplane_environments",
		"controlplane_devices",
		"controlplane_device_sessions",
		"controlplane_memberships",
		"controlplane_tenant_audit_events",
		"controlplane_envelope_streams",
		"controlplane_envelope_inbox_identities",
		"controlplane_envelope_inbox_payloads",
		"controlplane_envelope_inbox_jobs",
		"controlplane_envelope_proposals",
		"controlplane_envelope_outbox",
		"controlplane_envelope_audit_events",
	} {
		var rows []string
		for _, org := range []string{"tenant-a", "tenant-b"} {
			tx := pgCompositionTenantTx(t, ctx, app, org)
			rows = append(rows, pgRecoveryRows(t, ctx, tx, "SELECT to_jsonb(r)::text FROM "+relation+" r WHERE tenant_id=$1 ORDER BY to_jsonb(r)::text", org)...)
			if err = tx.Commit(); err != nil {
				t.Fatal(err)
			}
		}
		got[relation] = rows
	}
	pgRecoveryAssertStandaloneOutbox(t, ctx, app)
	for relation, expected := range want {
		if actual := got[relation]; !pgRecoveryEqualJSON(t, actual, expected) {
			t.Fatalf("restored %s differs from pre-dump evidence: got=%v want=%v", relation, actual, expected)
		}
	}
}

func pgRecoveryCheckTenantIsolation(t *testing.T, ctx context.Context, dbname string) {
	t.Helper()
	app := pgRecoveryOpen(t, ctx, pgRecoveryDSN("qualification_app", dbname))
	defer func() { _ = app.Close() }()
	tx := pgCompositionTenantTx(t, ctx, app, "tenant-a")
	var visible int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM controlplane_projects WHERE tenant_id='tenant-b'`).Scan(&visible); err != nil || visible != 0 {
		_ = tx.Rollback()
		t.Fatalf("tenant-a scope exposed tenant-b project: count=%d err=%v", visible, err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM controlplane_envelope_outbox WHERE tenant_id='tenant-b'`).Scan(&visible); err != nil || visible != 0 {
		_ = tx.Rollback()
		t.Fatalf("tenant-a scope exposed standalone tenant-b outbox: count=%d err=%v", visible, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO controlplane_envelope_streams(tenant_id,project_id,producer_key_id) VALUES('tenant-b','project','forged-cross-tenant')`); err == nil {
		_ = tx.Rollback()
		t.Fatal("tenant-a scope inserted a tenant-b envelope stream")
	}
	_ = tx.Rollback()
}

func pgRecoveryAssertStandaloneOutbox(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	tx := pgCompositionTenantTx(t, ctx, db, "tenant-b")
	defer func() { _ = tx.Rollback() }()
	var outboxCount, identityCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM controlplane_envelope_outbox WHERE outbox_id='outbox-standalone-b'`).Scan(&outboxCount); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM controlplane_envelope_inbox_identities WHERE event_id='event-outbox-only'`).Scan(&identityCount); err != nil {
		t.Fatal(err)
	}
	if outboxCount != 1 || identityCount != 0 {
		t.Fatalf("standalone outbox fixture count=%d matching inbox identities=%d, want 1/0", outboxCount, identityCount)
	}
}

type pgRecoverySender struct {
	calls map[string]int
}

func (s *pgRecoverySender) Send(_ context.Context, envelope cloudsync.Envelope) error {
	s.calls[envelope.EventID]++
	if envelope.EventID == "event-outbox-poison" || (envelope.EventID == pgRecoveryEvent && s.calls[envelope.EventID] == 1) {
		return errors.New("fixture delivery unavailable")
	}
	return nil
}

func pgRecoveryExerciseOutboxDelivery(t *testing.T, ctx context.Context, repository *envelopeworker.Repository, db *sql.DB) {
	t.Helper()
	bounded, err := repository.WithDeliveryAttemptLimit(2)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	sender := &pgRecoverySender{calls: make(map[string]int)}
	deliverer, err := envelopeworker.NewDeliverer(bounded, sender, envelopeworker.DeliveryConfig{
		BatchSize: 2, MaxAttempts: 2, Lease: time.Minute, BaseBackoff: time.Millisecond, MaxBackoff: time.Millisecond,
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	scope := tenant.Scope{Organization: tenant.OrganizationID(pgRecoveryOrg)}
	if err = deliverer.RunTenant(ctx, scope); err != nil {
		t.Fatal(err)
	}
	pgRecoveryAssertOutbox(t, ctx, db, "outbox-recovery", "pending", 1, "delivery_failed", false)
	pgRecoveryAssertOutbox(t, ctx, db, "outbox-poison", "pending", 1, "delivery_failed", false)
	now = now.Add(time.Second)
	if err = deliverer.RunTenant(ctx, scope); err != nil {
		t.Fatal(err)
	}
	pgRecoveryAssertOutbox(t, ctx, db, "outbox-recovery", "delivered", 2, "", true)
	pgRecoveryAssertOutbox(t, ctx, db, "outbox-poison", "dead-letter", 2, "delivery_failed", false)
	if sender.calls[pgRecoveryEvent] != 2 || sender.calls["event-outbox-poison"] != 2 {
		t.Fatalf("outbox sender calls=%v; want exactly two bounded attempts per row", sender.calls)
	}
	now = now.Add(time.Second)
	if err = deliverer.RunTenant(ctx, scope); err != nil {
		t.Fatal(err)
	}
	if sender.calls[pgRecoveryEvent] != 2 || sender.calls["event-outbox-poison"] != 2 {
		t.Fatalf("delivered/dead-letter rows were sent again: calls=%v", sender.calls)
	}
}

func pgRecoveryAssertOutbox(t *testing.T, ctx context.Context, db *sql.DB, id, wantState string, wantAttempts int, wantError string, wantDelivered bool) {
	t.Helper()
	tx := pgCompositionTenantTx(t, ctx, db, pgRecoveryOrg)
	defer func() { _ = tx.Rollback() }()
	var state, lastError string
	var attempts int
	var leaseUntil, deliveredAt int64
	if err := tx.QueryRowContext(ctx, `SELECT state,attempts,last_error_code,lease_until_unix_milli,delivered_unix_milli FROM controlplane_envelope_outbox WHERE outbox_id=$1`, id).Scan(&state, &attempts, &lastError, &leaseUntil, &deliveredAt); err != nil {
		t.Fatal(err)
	}
	if state != wantState || attempts != wantAttempts || lastError != wantError || leaseUntil != 0 || ((deliveredAt > 0) != wantDelivered) {
		t.Fatalf("outbox %s state=%s attempts=%d error=%q lease=%d delivered=%d; want %s/%d/%q/lease0/delivered=%t", id, state, attempts, lastError, leaseUntil, deliveredAt, wantState, wantAttempts, wantError, wantDelivered)
	}
}

func pgRecoveryRows(t *testing.T, ctx context.Context, db interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, query string, args ...any) []string {
	t.Helper()
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var result []string
	for rows.Next() {
		var value string
		if err = rows.Scan(&value); err != nil {
			t.Fatal(err)
		}
		result = append(result, value)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func pgRecoveryOpen(t *testing.T, ctx context.Context, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.PingContext(ctx); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	return db
}

func pgRecoveryDSN(role, dbname string) string {
	return "postgres://" + role + "@/" + dbname + "?host=/var/run/postgresql&sslmode=disable"
}

func pgRecoveryWriteFixture(t *testing.T, fixture pgRecoveryFixture) {
	t.Helper()
	raw, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile("/qualification/recovery-fixture.json", raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func pgRecoveryLoadEnvelopes(t *testing.T) pgRecoveryFixture {
	t.Helper()
	raw, err := os.ReadFile("/qualification/recovery-fixture.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture pgRecoveryFixture
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func pgRecoveryLoadDevices(t *testing.T) (pgCompositionDevice, pgCompositionDevice, ed25519.PublicKey, int64) {
	t.Helper()
	raw, err := os.ReadFile("/qualification/recovery-fixture.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture pgRecoveryFixture
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	private, err := base64.RawStdEncoding.DecodeString(fixture.PrivateKey)
	if err != nil || len(private) != ed25519.PrivateKeySize {
		t.Fatal("invalid synthetic recovery private key")
	}
	credential, err := base64.RawStdEncoding.DecodeString(fixture.Credential)
	if err != nil || len(credential) != 32 {
		t.Fatal("invalid synthetic recovery credential")
	}
	serverPublic, err := base64.StdEncoding.DecodeString(fixture.ServerPublic)
	if err != nil || len(serverPublic) != ed25519.PublicKeySize {
		t.Fatal("invalid synthetic server public key")
	}
	privateB, err := base64.RawStdEncoding.DecodeString(fixture.PrivateKeyB)
	if err != nil || len(privateB) != ed25519.PrivateKeySize {
		t.Fatal("invalid synthetic tenant-b recovery private key")
	}
	credentialB, err := base64.RawStdEncoding.DecodeString(fixture.CredentialB)
	if err != nil || len(credentialB) != 32 {
		t.Fatal("invalid synthetic tenant-b recovery credential")
	}
	return pgCompositionDevice{org: pgRecoveryOrg, device: tenant.DeviceID(fixture.Device), private: ed25519.PrivateKey(private), credential: credential, keyID: fixture.KeyID}, pgCompositionDevice{org: "tenant-b", device: tenant.DeviceID(fixture.DeviceB), private: ed25519.PrivateKey(privateB), credential: credentialB, keyID: fixture.KeyIDB}, ed25519.PublicKey(serverPublic), fixture.LeaseUntil
}

func pgRecoveryEnvelope(t *testing.T, device pgCompositionDevice, event string) cloudsync.Envelope {
	t.Helper()
	envelope := pgCompositionEnvelope(t, device, event)
	return envelope
}

func pgRecoveryJobDeadline(t *testing.T, ctx context.Context, db *sql.DB) int64 {
	t.Helper()
	tx := pgCompositionTenantTx(t, ctx, db, pgRecoveryOrg)
	defer func() { _ = tx.Rollback() }()
	var deadline int64
	var state string
	var attempts int
	var fence uint64
	if err := tx.QueryRowContext(ctx, `SELECT state,attempts,fence,lease_until_unix_milli FROM controlplane_envelope_inbox_jobs WHERE event_id=$1`, pgRecoveryEvent).Scan(&state, &attempts, &fence, &deadline); err != nil {
		t.Fatal(err)
	}
	if state != "processing" || attempts != 1 || fence != 1 || deadline <= 0 {
		t.Fatalf("post-restart job state=%s attempts=%d fence=%d; want processing/1/1", state, attempts, fence)
	}
	return deadline
}

func pgRecoveryAssertJob(t *testing.T, ctx context.Context, db *sql.DB, wantState string, wantAttempts int, wantFence uint64) {
	t.Helper()
	tx := pgCompositionTenantTx(t, ctx, db, pgRecoveryOrg)
	defer func() { _ = tx.Rollback() }()
	var state string
	var attempts int
	var fence uint64
	if err := tx.QueryRowContext(ctx, `SELECT state,attempts,fence FROM controlplane_envelope_inbox_jobs WHERE event_id=$1`, pgRecoveryEvent).Scan(&state, &attempts, &fence); err != nil {
		t.Fatal(err)
	}
	if state != wantState || attempts != wantAttempts || fence != wantFence {
		t.Fatalf("recovered job state=%s attempts=%d fence=%d, want %s/%d/%d", state, attempts, fence, wantState, wantAttempts, wantFence)
	}
}

func pgRecoveryEqualJSON(t *testing.T, left, right []string) bool {
	t.Helper()
	leftRaw, err := json.Marshal(left)
	if err != nil {
		t.Fatal(err)
	}
	rightRaw, err := json.Marshal(right)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(leftRaw)) == strings.TrimSpace(string(rightRaw))
}
