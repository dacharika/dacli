package composition

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/cloud/internal/migrations"
)

// TestHostedPostgresReadinessRequiresOrganizationsRLS verifies that both the
// runtime and its HTTP readiness endpoint require forced RLS on the tenant
// identity table. It uses the same disposable offline PostgreSQL fixture as
// the composed API qualification.
func TestHostedPostgresReadinessRequiresOrganizationsRLS(t *testing.T) {
	if os.Getenv("DACLI_QUALIFY_HOSTED_POSTGRES") != "1" {
		t.Skip("run scripts/qualify-hosted-postgres.sh for disposable real PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	const ownerDSN = "postgres://qualification_owner@/qualification?host=/var/run/postgresql&sslmode=disable"
	const appDSN = "postgres://qualification_app@/qualification?host=/var/run/postgresql&sslmode=disable"
	owner, err := sql.Open("postgres", ownerDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = owner.Close() }()
	catalog, err := migrations.Load(os.DirFS("/qualification/migrations"))
	if err != nil {
		t.Fatal(err)
	}
	if err = migrations.Run(ctx, migrations.SQLStore{DB: owner}, catalog); err != nil {
		t.Fatal(err)
	}
	if _, err = owner.ExecContext(ctx, `GRANT USAGE ON SCHEMA public TO qualification_app; GRANT SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA public TO qualification_app; GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA public TO qualification_app`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, restoreErr := owner.ExecContext(context.Background(), `ALTER TABLE public.controlplane_organizations ENABLE ROW LEVEL SECURITY; ALTER TABLE public.controlplane_organizations FORCE ROW LEVEL SECURITY`); restoreErr != nil {
			t.Errorf("restore organizations row-level security: %v", restoreErr)
		}
	}()
	if _, err = owner.ExecContext(ctx, `ALTER TABLE public.controlplane_organizations ENABLE ROW LEVEL SECURITY; ALTER TABLE public.controlplane_organizations FORCE ROW LEVEL SECURITY`); err != nil {
		t.Fatal(err)
	}
	app, err := sql.Open("postgres", appDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Close() }()
	serverPublic, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := Open(ctx, pgCompositionConfig(t, appDSN, serverPublic), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = runtime.Close() }()
	assertReadiness := func(wantStatus int, wantReady bool) {
		t.Helper()
		readyErr := runtime.Ready(ctx)
		if (readyErr == nil) != wantReady {
			t.Fatalf("Runtime.Ready() error = %v, wantReady=%v", readyErr, wantReady)
		}
		request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
		response := httptest.NewRecorder()
		runtime.API.Handler().ServeHTTP(response, request)
		if response.Code != wantStatus {
			t.Fatalf("/readyz status = %d body=%s, want %d", response.Code, response.Body.String(), wantStatus)
		}
	}
	setRLS := func(statement string) {
		t.Helper()
		if _, err := owner.ExecContext(ctx, statement); err != nil {
			t.Fatalf("change organizations row-level security: %v", err)
		}
	}
	assertReadiness(http.StatusOK, true)

	setRLS(`ALTER TABLE public.controlplane_organizations NO FORCE ROW LEVEL SECURITY`)
	assertReadiness(http.StatusServiceUnavailable, false)
	setRLS(`ALTER TABLE public.controlplane_organizations FORCE ROW LEVEL SECURITY`)
	assertReadiness(http.StatusOK, true)

	setRLS(`ALTER TABLE public.controlplane_organizations DISABLE ROW LEVEL SECURITY`)
	assertReadiness(http.StatusServiceUnavailable, false)
	setRLS(`ALTER TABLE public.controlplane_organizations ENABLE ROW LEVEL SECURITY; ALTER TABLE public.controlplane_organizations FORCE ROW LEVEL SECURITY`)
	assertReadiness(http.StatusOK, true)
}
