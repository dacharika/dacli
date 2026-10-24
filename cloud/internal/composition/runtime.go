package composition

import (
	"context"
	"database/sql"
	"errors"
	_ "github.com/lib/pq"
	"github.com/mlnomadpy/dacli/cloud/internal/config"
	"github.com/mlnomadpy/dacli/cloud/internal/envelopeworker"
	"github.com/mlnomadpy/dacli/cloud/internal/ratelimit"
	"github.com/mlnomadpy/dacli/cloud/internal/service"
	"github.com/mlnomadpy/dacli/cloud/internal/tenant"
	"github.com/mlnomadpy/dacli/cloud/internal/tenantapi"
	"github.com/mlnomadpy/dacli/cloud/internal/tenantcache"
	"github.com/mlnomadpy/dacli/cloud/internal/tenantrepo"
	"github.com/mlnomadpy/dacli/cloud/internal/tenantresource"
	"log/slog"
	"net/url"
	"path"
	"time"
)

// Runtime owns infrastructure lifetimes only. No startup migration, tenant
// identity injection, local workspace endpoint or agent execution is provided.
type Runtime struct {
	deliveryLimiter *ratelimit.Limiter
	db              *sql.DB
	API             *service.API
	repository      *envelopeworker.Repository
	cfg             config.Config
	hosted          *config.HostedRuntime
	keys            projectKeys
	inboxKeys       envelopeworker.KeySource
}

var hostedCatalog = []struct {
	version        int
	name, checksum string
}{
	{1, "service_state", "2500a7ff337ce2f24dd485c83d844a76c99fa04fca0f3c611627ca9caacb5d36"},
	{2, "event_cursor", "4d13aa9da02ecf3b0af720eed869ec08555dc911883ff5959fe5e517e7d76238"},
	{3, "tenant_repository", "149463eb13d3a286c350fc68fc87a9e899514720924b1cd01c9c529df61da5ca"},
	{4, "device_sessions", "b7b0584cbab5873d835035ab6d4b6a1a126b7f80c5df3e355d5ddde4d9f17bfc"},
	{5, "tenant_workflows", "e201477a0cd1a906c8879995af0319d9de419bf5ca2c8cbc8a84a795af089d57"},
	{6, "stable_pagination", "4f9d60e66d1b9843c14dc10d521c4b1983e275774c22f2384a817f0724e3a041"},
	{7, "envelope_worker", "429f3fb4ce26f23f2c20451dafe5918b71a88ea615b227ed1731c801eca2d457"},
	{8, "mutation_audit_identity", "b93d36bd8f79041a45f9708446a5537c44a17520bde60cead83774e398f08fd8"},
	{9, "mutation_attempts", "47ff94950a568531603007a17f36721be3f208fc4ee0deaccdbe8f661d81cc5e"},
	{10, "inbox_processing", "44a2d2a1e25ffdee982364a422f2d0ac805b2f8cabe6f8ce978b9d1496961112"},
	{11, "device_auth", "132929846e4407ff563563be63adde5da9ab4e2002d107d68b0e0be63bd4ca3c"},
	{12, "role_registry", "21493a8fa4b720efc6235374ae5d5c75e6a56421b0fc39158c1de5bd5e783d90"},
}

func Open(ctx context.Context, cfg config.Config, logger *slog.Logger) (*Runtime, error) {
	if ctx == nil {
		return nil, errors.New("hosted context required")
	}
	hosted, e := cfg.HostedRuntime()
	if e != nil {
		return nil, e
	}
	// Socket URLs omit the authority host so lib/pq cannot sort duplicate host
	// settings and override the explicit socket with localhost (core/637).
	u, e := url.Parse(cfg.DatabaseURL())
	if e != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || (u.Hostname() == "" && !path.IsAbs(u.Query().Get("host"))) {
		return nil, errors.New("hosted runtime requires a PostgreSQL URL")
	}
	q := u.Query()
	q.Set("search_path", "public,pg_catalog")
	u.RawQuery = q.Encode()
	db, e := sql.Open("postgres", u.String())
	if e != nil {
		return nil, errors.New("hosted database unavailable")
	}
	db.SetMaxOpenConns(16)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(30 * time.Minute)
	r := &Runtime{db: db, cfg: cfg, hosted: hosted, keys: projectKeys{hosted.Tenants()}}
	bounded, cancel := context.WithTimeout(ctx, cfg.RequestTimeout)
	defer cancel()
	if e = r.Ready(bounded); e != nil {
		_ = db.Close()
		return nil, e
	}
	repo, e := tenantrepo.Open(bounded, db)
	if e != nil {
		_ = db.Close()
		return nil, errors.New("tenant repository unavailable")
	}
	resources, e := tenantresource.New(repo, repo, nil)
	if e != nil {
		_ = db.Close()
		return nil, e
	}
	backend, e := tenantapi.New(repo, resources, tenantcache.New[tenant.Project](1000), hosted.PolicyRevision)
	if e != nil {
		_ = db.Close()
		return nil, e
	}
	identity, e := newDeviceIdentity(repo, hosted.PolicyRevision)
	if e != nil {
		_ = db.Close()
		return nil, e
	}
	envelopes, e := envelopeworker.Open(bounded, db)
	if e != nil {
		_ = db.Close()
		return nil, errors.New("envelope repository unavailable")
	}
	r.repository = envelopes
	limiter, e := ratelimit.New(cfg.RateLimits.Sync, nil)
	if e != nil {
		_ = db.Close()
		return nil, e
	}
	inbox, e := envelopeworker.NewLimitedService(identity, r.keys, envelopes, limiter, []byte(cfg.ServiceSecret()))
	if e != nil {
		_ = db.Close()
		return nil, e
	}
	r.deliveryLimiter, e = ratelimit.New(cfg.RateLimits.Sync, nil)
	if e != nil {
		_ = db.Close()
		return nil, e
	}
	r.API = service.NewAPI(cfg, r, logger)
	if e = r.API.EnableTenantAPI(identity, backend, cfg.ServiceSecret()); e != nil {
		_ = db.Close()
		return nil, e
	}
	if e = r.API.EnableRoleRegistryAPI(identity, repo, hosted.PolicyRevision); e != nil {
		_ = db.Close()
		return nil, e
	}
	if e = r.API.EnableEnvelopeAPI(inbox, identity, envelopes); e != nil {
		_ = db.Close()
		return nil, e
	}
	keys := deviceKeys{static: r.keys, repo: repo, bindings: hosted.SyncBindings()}
	if device := hosted.DeviceOAuth(); device != nil {
		keys.serverKeys = device.ServerSigningKeys
	}
	// Inbox replay must resolve the same tenant/project device keys as sync
	// admission; static delivery keys alone reject enrolled devices (core/628).
	r.inboxKeys = keys
	if e = enableDevice(r, repo, keys); e != nil {
		_ = db.Close()
		return nil, e
	}
	if len(hosted.SyncBindings()) > 0 {
		exchangeInbox, e := envelopeworker.NewLimitedService(exchangeIdentity{identity}, keys, envelopes, limiter, []byte(cfg.ServiceSecret()))
		if e != nil {
			_ = db.Close()
			return nil, e
		}
		if e = r.API.EnableSyncAPI(identity, syncBindings{repo: repo, keys: keys, routes: hosted.SyncBindings()}, exchangeInbox, envelopes, []byte(cfg.ServiceSecret())); e != nil {
			_ = db.Close()
			return nil, e
		}
	}
	return r, nil
}
func (r *Runtime) Close() error {
	if r == nil || r.db == nil {
		return nil
	}
	return r.db.Close()
}
func (r *Runtime) Ready(ctx context.Context) error {
	if r == nil || r.db == nil || r.db.PingContext(ctx) != nil {
		return errors.New("hosted database not ready")
	}
	var super, bypass bool
	if e := r.db.QueryRowContext(ctx, `SELECT rolsuper,rolbypassrls FROM pg_roles WHERE rolname=current_user`).Scan(&super, &bypass); e != nil || super || bypass {
		return errors.New("hosted runtime requires a tenant-isolated application database role")
	}
	rows, e := r.db.QueryContext(ctx, `SELECT version,name,checksum FROM public.controlplane_schema_migrations ORDER BY version LIMIT 13`)
	if e != nil {
		return errors.New("hosted migration ledger unavailable")
	}
	n := 0
	for rows.Next() {
		var version int
		var name, checksum string
		if rows.Scan(&version, &name, &checksum) != nil || n >= len(hostedCatalog) {
			_ = rows.Close()
			return errors.New("hosted migration catalog incompatible")
		}
		expected := hostedCatalog[n]
		if version != expected.version || name != expected.name || checksum != expected.checksum {
			_ = rows.Close()
			return errors.New("hosted migration checksum or version incompatible")
		}
		n++
	}
	e = rows.Err()
	closeErr := rows.Close()
	if e != nil || closeErr != nil || n != 12 {
		return errors.New("hosted runtime requires the complete schema12 migration catalog")
	}
	for _, relation := range []string{"controlplane_device_auth", "controlplane_teams", "controlplane_invitations", "controlplane_project_assignments", "controlplane_environment_assignments", "controlplane_tenant_audit_events", "controlplane_organizations", "controlplane_projects", "controlplane_environments", "controlplane_devices", "controlplane_memberships", "controlplane_device_sessions", "controlplane_envelope_streams", "controlplane_envelope_inbox_identities", "controlplane_envelope_inbox_payloads", "controlplane_envelope_outbox", "controlplane_envelope_audit_events", "controlplane_envelope_inbox_jobs", "controlplane_envelope_proposals", "controlplane_role_catalogs", "controlplane_role_publishers", "controlplane_role_releases", "controlplane_role_lifecycle", "controlplane_role_pins", "controlplane_role_audit_events"} {
		var enabled, forced bool
		if e = r.db.QueryRowContext(ctx, `SELECT relrowsecurity,relforcerowsecurity FROM pg_class WHERE oid=to_regclass($1)`, "public."+relation).Scan(&enabled, &forced); e != nil || !enabled || !forced {
			return errors.New("hosted schema dependency or tenant isolation unavailable")
		}
	}
	return nil
}
