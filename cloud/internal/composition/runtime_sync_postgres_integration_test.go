package composition

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"github.com/mlnomadpy/dacli/cloud/internal/config"
	"github.com/mlnomadpy/dacli/cloud/internal/migrations"
	"github.com/mlnomadpy/dacli/cloud/internal/tenant"
	"github.com/mlnomadpy/dacli/cloud/internal/tenantrepo"
	"github.com/mlnomadpy/dacli/internal/cloudsync"
	"github.com/mlnomadpy/dacli/internal/ulid"
)

// Opt-in composed sync proof uses a disposable PostgreSQL database and synthetic
// device credentials. It exercises the HTTP API and local durable client, not OAuth.
func TestHostedPostgresProjectSync(t *testing.T) {
	if os.Getenv("DACLI_QUALIFY_HOSTED_POSTGRES") != "1" {
		t.Skip("run scripts/qualify-hosted-postgres.sh for disposable real PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	const ownerDSN = "postgres://qualification_owner@/qualification_sync?host=/var/run/postgresql&sslmode=disable"
	const appDSN = "postgres://qualification_app@/qualification_sync?host=/var/run/postgresql&sslmode=disable"
	owner := pgSyncOpen(t, ctx, ownerDSN)
	defer func() { _ = owner.Close() }()
	catalog, err := migrations.Load(os.DirFS("/qualification/migrations"))
	if err != nil || len(catalog) != 12 {
		t.Fatalf("load schema 12 catalog: count=%d err=%v", len(catalog), err)
	}
	if err = migrations.Run(ctx, migrations.SQLStore{DB: owner}, catalog); err != nil {
		t.Fatal(err)
	}
	if _, err = owner.ExecContext(ctx, `GRANT USAGE ON SCHEMA public TO qualification_app; GRANT SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA public TO qualification_app; GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA public TO qualification_app`); err != nil {
		t.Fatal(err)
	}
	app := pgSyncOpen(t, ctx, appDSN)
	defer func() { _ = app.Close() }()
	pgSyncSeed(t, ctx, owner)
	repo, err := tenantrepo.Open(ctx, app)
	if err != nil {
		t.Fatal(err)
	}
	deviceA := pgCompositionEnroll(t, ctx, repo, "sync-tenant", "sync-device-a")
	deviceB := pgCompositionEnroll(t, ctx, repo, "sync-tenant", "sync-device-b")
	serverPublic, serverPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cfg := pgSyncConfig(t, appDSN, serverPublic)
	runtime, err := Open(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = runtime.Close() }()
	server := httptest.NewServer(runtime.API.Handler())
	defer func() { server.Close() }()

	routeA := pgSyncRoute("sync-tenant", "project-a", "environment-a", deviceA.device)
	clientARoot := t.TempDir()
	clientA, transportA, bindingA := pgSyncClient(t, server.URL, routeA, deviceA, clientARoot, serverPublic)
	proposal, err := clientA.Enqueue("task_proposal", "sync-proposal-a", map[string]any{"proposal_id": "proposal-a", "title": "A proposal", "acceptance": []string{"Inspect metadata"}, "base_version": 0, "proposed_by": "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if err = clientA.Sync(ctx, transportA); err != nil || clientA.AcknowledgedSequence() != 1 {
		t.Fatalf("project A signed upload: ack=%d err=%v", clientA.AcknowledgedSequence(), err)
	}
	if err = runtime.Cycle(ctx); err != nil {
		t.Fatal(err)
	}
	pgCompositionProposal(t, ctx, app, proposal, deviceA.device)
	duplicate := pgSyncExchange(t, runtime, routeA, deviceA, bindingA.BindingVersion, []cloudsync.Envelope{proposal}, "")
	pgCompositionAck(t, duplicate, cloudsync.OutcomeDuplicate, proposal)

	// A successful first envelope followed by a bad signature leaves only the
	// first immutable identity. Retrying that exact envelope receives a duplicate ack.
	second, err := clientA.Enqueue("task_proposal", "sync-proposal-partial", map[string]any{"proposal_id": "proposal-partial", "title": "Partial retry", "acceptance": []string{"Inspect metadata"}, "base_version": 0, "proposed_by": "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	tampered := pgSyncEnvelope(t, deviceA, "project-a", "task_proposal", 3, "tampered", json.RawMessage(`{"proposal_id":"tampered","title":"Tampered","acceptance":["Inspect metadata"],"base_version":0,"proposed_by":"fixture"}`))
	tampered.Payload = json.RawMessage(`{"proposal_id":"changed","title":"Tampered","acceptance":["Inspect metadata"],"base_version":0,"proposed_by":"fixture"}`)
	before := pgCompositionCount(t, ctx, app, "sync-tenant", "controlplane_envelope_inbox_identities")
	pgCompositionDenied(t, pgSyncExchange(t, runtime, routeA, deviceA, bindingA.BindingVersion, []cloudsync.Envelope{second, tampered}, ""))
	if got := pgCompositionCount(t, ctx, app, "sync-tenant", "controlplane_envelope_inbox_identities"); got != before+1 {
		t.Fatalf("partial batch identities=%d, want %d", got, before+1)
	}
	pgCompositionAck(t, pgSyncExchange(t, runtime, routeA, deviceA, bindingA.BindingVersion, []cloudsync.Envelope{second}, ""), cloudsync.OutcomeDuplicate, second)
	unsupported := pgSyncEnvelope(t, deviceA, "project-a", "task_proposal", 4, "unsupported", second.Payload)
	unsupported.SchemaVersion = 2
	if err = cloudsync.Sign(&unsupported, deviceA.private); err != nil {
		t.Fatal(err)
	}
	pgCompositionDenied(t, pgSyncExchange(t, runtime, routeA, deviceA, bindingA.BindingVersion, []cloudsync.Envelope{unsupported}, ""))
	if got := pgCompositionCount(t, ctx, app, "sync-tenant", "controlplane_envelope_inbox_identities"); got != before+1 {
		t.Fatalf("refusals changed immutable identities: got %d want %d", got, before+1)
	}
	if err = runtime.Cycle(ctx); err != nil {
		t.Fatal(err)
	}
	pgCompositionProposal(t, ctx, app, second, deviceA.device)

	// Seed 101 valid signed server envelopes to force a bounded two-page pull.
	for i := 1; i <= 101; i++ {
		e := pgSyncServerEnvelope(t, "sync-tenant", "project-a", uint64(i), serverPrivate)
		if err = runtime.repository.Enqueue(ctx, tenant.Scope{Organization: "sync-tenant"}, "sync-outbox-a-"+e.EventID, e, time.Now().UTC()); err != nil {
			t.Fatalf("enqueue server envelope %d: %v", i, err)
		}
	}
	if err = clientA.Sync(ctx, transportA); err != nil {
		t.Fatalf("first page pull: %v", err)
	}
	if got := pgSyncInboxCount(t, filepath.Join(clientARoot, "inbox")); got != 100 {
		t.Fatalf("first inbound page count=%d want 100", got)
	}
	firstPageCursor := transportA.DeliveryCursor
	if firstPageCursor == "" {
		t.Fatal("first page returned no delivery cursor")
	}
	// A lost local cursor commit may replay the original page. The client must
	// recognize every inbound row as an exact duplicate and keep its inbox stable.
	transportA.DeliveryCursor = ""
	if err = clientA.Sync(ctx, transportA); err != nil {
		t.Fatalf("replay first inbound page: %v", err)
	}
	if got := pgSyncInboxCount(t, filepath.Join(clientARoot, "inbox")); got != 100 || transportA.DeliveryCursor != firstPageCursor {
		t.Fatalf("replayed page count=%d cursor-unchanged=%v", got, transportA.DeliveryCursor == firstPageCursor)
	}
	if err = runtime.Close(); err != nil {
		t.Fatal(err)
	}
	runtime, err = Open(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	server.Close()
	server = httptest.NewServer(runtime.API.Handler())
	transportA = &cloudsync.HTTPTransport{Provider: server.URL, Authorization: pgSyncBearer(deviceA), Binding: bindingA, DeliveryCursor: firstPageCursor}
	if err = clientA.Sync(ctx, transportA); err != nil {
		t.Fatalf("resumed cursor after runtime reopen: %v", err)
	}
	if got := pgSyncInboxCount(t, filepath.Join(clientARoot, "inbox")); got != 101 {
		t.Fatalf("resumed inbound count=%d want 101", got)
	}
	if transportA.DeliveryCursor == firstPageCursor {
		t.Fatal("completed page did not advance signed delivery cursor")
	}
	// A second project's route resolves independently and receives only its own outbox.
	routeB := pgSyncRoute("sync-tenant", "project-b", "environment-b", deviceB.device)
	clientBRoot := t.TempDir()
	clientB, transportB, bindingB := pgSyncClient(t, server.URL, routeB, deviceB, clientBRoot, serverPublic)
	eB := pgSyncServerEnvelope(t, "sync-tenant", "project-b", 1, serverPrivate)
	if err = runtime.repository.Enqueue(ctx, tenant.Scope{Organization: "sync-tenant"}, "sync-outbox-b-"+eB.EventID, eB, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err = clientB.Sync(ctx, transportB); err != nil {
		t.Fatalf("project B exchange: %v", err)
	}
	if got := pgSyncInboxCount(t, filepath.Join(clientBRoot, "inbox")); got != 1 {
		t.Fatalf("project B inbound count=%d want 1", got)
	}
	// Project B accepts a later sequence first, then fills the gap. Both remain
	// acknowledged as delayed until the immutable producer prefix is contiguous.
	proposalPayload := json.RawMessage(`{"proposal_id":"reordered","title":"Reordered metadata","acceptance":["Inspect metadata"],"base_version":0,"proposed_by":"fixture"}`)
	high := pgSyncEnvelope(t, deviceB, "project-b", "task_proposal", 2, "project-b-sequence-2", proposalPayload)
	low := pgSyncEnvelope(t, deviceB, "project-b", "task_proposal", 1, "project-b-sequence-1", proposalPayload)
	pgCompositionAck(t, pgSyncExchange(t, runtime, routeB, deviceB, bindingB.BindingVersion, []cloudsync.Envelope{high}, ""), cloudsync.OutcomeAccepted, high)
	pgCompositionAck(t, pgSyncExchange(t, runtime, routeB, deviceB, bindingB.BindingVersion, []cloudsync.Envelope{low}, ""), cloudsync.OutcomeAccepted, low)
	pgCompositionAck(t, pgSyncExchange(t, runtime, routeB, deviceB, bindingB.BindingVersion, []cloudsync.Envelope{low}, ""), cloudsync.OutcomeDuplicate, low)
	if err = runtime.Cycle(ctx); err != nil {
		t.Fatal(err)
	}
	pgCompositionProposal(t, ctx, app, high, deviceB.device)
	pgCompositionProposal(t, ctx, app, low, deviceB.device)
	tx := pgCompositionTenantTx(t, ctx, owner, "sync-tenant")
	if _, err = tx.ExecContext(ctx, `UPDATE controlplane_envelope_streams SET replay_floor=3 WHERE tenant_id='sync-tenant' AND project_id='project-b' AND producer_key_id=$1`, deviceB.keyID); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	replayed := pgSyncEnvelope(t, deviceB, "project-b", "task_proposal", 1, "project-b-below-replay-floor", proposalPayload)
	replayCount := pgCompositionCount(t, ctx, app, "sync-tenant", "controlplane_envelope_inbox_identities")
	pgCompositionDenied(t, pgSyncExchange(t, runtime, routeB, deviceB, bindingB.BindingVersion, []cloudsync.Envelope{replayed}, ""))
	if got := pgCompositionCount(t, ctx, app, "sync-tenant", "controlplane_envelope_inbox_identities"); got != replayCount {
		t.Fatalf("replay-floor refusal changed identities: got %d want %d", got, replayCount)
	}
	routeBAsA := routeB
	routeBAsA.DeviceID = string(deviceA.device)
	routeMismatch := routeA
	routeMismatch.EnvironmentID = "environment-b"
	routeMismatch.RepositoryID = "repo-mismatch"
	for name, response := range map[string]*httptest.ResponseRecorder{
		"cross-project-envelope":       pgSyncExchange(t, runtime, routeBAsA, deviceA, bindingB.BindingVersion, []cloudsync.Envelope{proposal}, ""),
		"environment-project-mismatch": pgSyncExchange(t, runtime, routeMismatch, deviceA, bindingA.BindingVersion, nil, ""),
		"cross-tenant-route":           pgSyncExchange(t, runtime, cloudsync.SyncRoute{WorkspaceID: "workspace", TenantID: "other-tenant", ProjectID: "project-a", EnvironmentID: "environment-a", RepositoryID: "repo-a", DeviceID: string(deviceA.device)}, deviceA, bindingA.BindingVersion, nil, ""),
		"cross-project-cursor":         pgSyncExchange(t, runtime, routeB, deviceB, bindingB.BindingVersion, nil, firstPageCursor),
	} {
		t.Run(name, func(t *testing.T) { pgCompositionDenied(t, response) })
	}
	if got := pgCompositionCount(t, ctx, app, "sync-tenant", "controlplane_envelope_inbox_identities"); got != before+3 {
		t.Fatalf("cross-route refusals changed identities: %d", got)
	}

	// Device revocation and then membership revocation both deny the composed API.
	queuedBeforeRevocation, err := clientA.Enqueue("task_proposal", "sync-proposal-revoked-device", map[string]any{"proposal_id": "revoked-device", "title": "Revoked device", "acceptance": []string{"Inspect metadata"}, "base_version": 0, "proposed_by": "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	pgCompositionAck(t, pgSyncExchange(t, runtime, routeA, deviceA, bindingA.BindingVersion, []cloudsync.Envelope{queuedBeforeRevocation}, ""), cloudsync.OutcomeAccepted, queuedBeforeRevocation)
	proposalCount := pgCompositionCount(t, ctx, app, "sync-tenant", "controlplane_envelope_proposals")
	tx = pgCompositionTenantTx(t, ctx, owner, "sync-tenant")
	if _, err = tx.ExecContext(ctx, `UPDATE controlplane_devices SET state=$1,version=version+1 WHERE tenant_id='sync-tenant' AND device_id=$2`, tenant.LifecycleRevoked, deviceA.device); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	pgCompositionDenied(t, pgSyncExchange(t, runtime, routeA, deviceA, bindingA.BindingVersion, nil, ""))
	if err = runtime.Cycle(ctx); err != nil {
		t.Fatal(err)
	}
	if got := pgCompositionCount(t, ctx, app, "sync-tenant", "controlplane_envelope_proposals"); got != proposalCount {
		t.Fatalf("revoked device produced a proposal: count=%d want %d", got, proposalCount)
	}
	tx = pgCompositionTenantTx(t, ctx, owner, "sync-tenant")
	if _, err = tx.ExecContext(ctx, `UPDATE controlplane_memberships SET state=$1,version=version+1 WHERE tenant_id='sync-tenant' AND account_id='account-sync-tenant'`, tenant.LifecycleRevoked); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	pgCompositionDenied(t, pgSyncExchange(t, runtime, routeB, deviceB, bindingB.BindingVersion, nil, ""))
	if got := pgCompositionCount(t, ctx, app, "sync-tenant", "controlplane_envelope_inbox_identities"); got != before+4 {
		t.Fatalf("revoked authority changed identities: %d", got)
	}
	t.Log("real PostgreSQL two-project bindings, signed proposal admission, immutable duplicate/partial retry, paged signed outbox pull across runtime reopen, cursor route binding, and device/member revocation verified")
}

func pgSyncOpen(t *testing.T, ctx context.Context, dsn string) *sql.DB {
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

func pgSyncSeed(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	tx := pgCompositionTenantTx(t, ctx, db, "sync-tenant")
	defer func() { _ = tx.Rollback() }()
	for _, statement := range []string{
		`INSERT INTO controlplane_accounts(account_id,name,state,version) VALUES('account-sync-tenant','Sync fixture',1,1)`,
		`INSERT INTO controlplane_organizations(tenant_id,organization_id,name,state,version) VALUES('sync-tenant','sync-tenant','Sync fixture',1,1)`,
		`INSERT INTO controlplane_memberships(tenant_id,account_id,roles,state,version) VALUES('sync-tenant','account-sync-tenant','["owner"]',1,1)`,
		`INSERT INTO controlplane_projects(tenant_id,project_id,name,state,version) VALUES('sync-tenant','project-a','Project A',1,1),('sync-tenant','project-b','Project B',1,1)`,
		`INSERT INTO controlplane_environments(tenant_id,project_id,environment_id,name,kind,state,version) VALUES('sync-tenant','project-a','environment-a','Environment A',1,1,1),('sync-tenant','project-b','environment-b','Environment B',1,1,1)`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func pgSyncConfig(t *testing.T, dsn string, serverPublic ed25519.PublicKey) config.Config {
	t.Helper()
	encoded := base64.StdEncoding.EncodeToString(serverPublic)
	hosted := &config.HostedFile{PolicyRevision: 1, DeviceOAuth: &config.DeviceOAuthFile{Issuer: "https://provider.invalid", DeviceURL: "https://provider.invalid/device", TokenURL: "https://provider.invalid/token", IdentityURL: "https://provider.invalid/identity", ClientID: "qualification", Scope: "openid", ServerSigningKeys: []config.ServerSigningKey{{KeyID: "server", PublicKey: encoded}}}}
	hosted.Tenants = []config.HostedTenantFile{{TenantID: "sync-tenant", TrustedKeys: []config.HostedKeyFile{{ProjectID: "project-a", KeyID: "server", PublicKey: encoded}, {ProjectID: "project-b", KeyID: "server", PublicKey: encoded}}}}
	hosted.DeviceOAuth.Subjects = []config.DeviceSubject{{TenantID: "sync-tenant", Subject: "fixture-sync-tenant", AccountID: "account-sync-tenant"}}
	for _, p := range []struct{ project, environment, repository string }{{"project-a", "environment-a", "repo-a"}, {"project-b", "environment-b", "repo-b"}} {
		hosted.SyncBindings = append(hosted.SyncBindings, config.SyncBinding{WorkspaceID: "workspace", TenantID: "sync-tenant", ProjectID: p.project, EnvironmentID: p.environment, RepositoryID: p.repository, RepositoryURL: "https://repository.invalid/" + p.project + ".git", DefaultBranch: "main", Version: 1, MinimumSchemaVersion: 1, MaximumSchemaVersion: 1})
	}
	hosted.SyncBindings = append(hosted.SyncBindings, config.SyncBinding{WorkspaceID: "workspace", TenantID: "sync-tenant", ProjectID: "project-a", EnvironmentID: "environment-b", RepositoryID: "repo-mismatch", RepositoryURL: "https://repository.invalid/project-a.git", DefaultBranch: "main", Version: 1, MinimumSchemaVersion: 1, MaximumSchemaVersion: 1})
	limit := config.LimitFile{Capacity: 1000, RefillInterval: "1h", MaxKeys: 1000, IdleTTL: "2h"}
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
			return strings.Repeat("s", 32), true
		default:
			return "", false
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func pgSyncRoute(org, project, environment string, device tenant.DeviceID) cloudsync.SyncRoute {
	repository := "repo-a"
	if project == "project-b" {
		repository = "repo-b"
	}
	return cloudsync.SyncRoute{WorkspaceID: "workspace", TenantID: org, ProjectID: project, EnvironmentID: environment, RepositoryID: repository, DeviceID: string(device)}
}

func pgSyncBearer(device pgCompositionDevice) string {
	return "Bearer " + base64.RawURLEncoding.EncodeToString([]byte(device.org)) + "." + base64.RawURLEncoding.EncodeToString(device.credential)
}

func pgSyncClient(t *testing.T, provider string, route cloudsync.SyncRoute, device pgCompositionDevice, root string, serverPublic ed25519.PublicKey) (*cloudsync.Client, *cloudsync.HTTPTransport, cloudsync.ProjectBindingResponse) {
	t.Helper()
	transport := &cloudsync.HTTPTransport{Provider: provider, Authorization: pgSyncBearer(device), Client: &http.Client{Timeout: 5 * time.Second}}
	binding, err := transport.ObserveBinding(context.Background(), route)
	if err != nil {
		t.Fatalf("observe exact project binding %s: %v", route.ProjectID, err)
	}
	if binding.Route != route || len(binding.TrustedKeys) != 1 || binding.TrustedKeys["server"] != base64.StdEncoding.EncodeToString(serverPublic) {
		t.Fatalf("unexpected project binding: %+v", binding)
	}
	transport.Binding = binding
	trusted := map[string]ed25519.PublicKey{"server": append(ed25519.PublicKey(nil), serverPublic...)}
	client, err := cloudsync.Open(root, cloudsync.Config{TenantID: route.TenantID, ProjectID: route.ProjectID, ProducerVersion: "qualification", SigningKeyID: device.keyID, SigningKey: device.private, TrustedKeys: trusted})
	if err != nil {
		t.Fatal(err)
	}
	return client, transport, binding
}

func pgSyncExchange(t *testing.T, runtime *Runtime, route cloudsync.SyncRoute, device pgCompositionDevice, binding uint64, envelopes []cloudsync.Envelope, cursor string) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(cloudsync.ExchangeRequest{SchemaVersion: 1, Route: route, BindingVersion: binding, Envelopes: envelopes, DeliveryCursor: cursor})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/sync/exchange", bytes.NewReader(raw))
	request.Header.Set("Authorization", pgSyncBearer(device))
	response := httptest.NewRecorder()
	runtime.API.Handler().ServeHTTP(response, request)
	return response
}

func pgSyncEnvelope(t *testing.T, device pgCompositionDevice, project, kind string, sequence uint64, idem string, payload json.RawMessage) cloudsync.Envelope {
	t.Helper()
	e := cloudsync.Envelope{SchemaVersion: 1, EventType: kind, TenantID: device.org, ProjectID: project, EventID: ulid.New(), ProducerSequence: sequence, OccurredAt: time.Now().UTC(), IdempotencyKey: idem, ProducerVersion: "qualification", Payload: payload, Integrity: cloudsync.Integrity{Algorithm: "Ed25519", KeyID: device.keyID}}
	if err := cloudsync.Sign(&e, device.private); err != nil {
		t.Fatal(err)
	}
	return e
}

func pgSyncServerEnvelope(t *testing.T, org, project string, sequence uint64, private ed25519.PrivateKey) cloudsync.Envelope {
	t.Helper()
	e := cloudsync.Envelope{SchemaVersion: 1, EventType: "task_proposal", TenantID: org, ProjectID: project, EventID: ulid.New(), ProducerSequence: sequence, OccurredAt: time.Now().UTC(), IdempotencyKey: "server-" + ulid.New(), ProducerVersion: "qualification", Payload: json.RawMessage(`{"proposal_id":"server-proposal","title":"Server metadata","acceptance":["Inspect metadata"],"base_version":0,"proposed_by":"server"}`), Integrity: cloudsync.Integrity{Algorithm: "Ed25519", KeyID: "server"}}
	if err := cloudsync.Sign(&e, private); err != nil {
		t.Fatal(err)
	}
	return e
}

func pgSyncInboxCount(t *testing.T, root string) int {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}
