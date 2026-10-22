package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mlnomadpy/dacli/cloud/internal/deviceoauth"
	"github.com/mlnomadpy/dacli/cloud/internal/devicesession"
	"github.com/mlnomadpy/dacli/cloud/internal/envelopeworker"
	"github.com/mlnomadpy/dacli/cloud/internal/tenant"
)

type httpDeviceProvider struct{}

func (httpDeviceProvider) Begin(context.Context) (deviceoauth.Challenge, error) {
	return deviceoauth.Challenge{}, deviceoauth.ErrDenied
}
func (httpDeviceProvider) Poll(context.Context, string) (string, error) {
	return "", deviceoauth.ErrDenied
}

type httpDeviceStore struct{}

func (httpDeviceStore) CreateAuth(context.Context, tenant.Scope, string, []byte, int64) ([]byte, error) {
	return nil, errors.New("unused")
}
func (httpDeviceStore) UpdateAuth(context.Context, tenant.Scope, string, func([]byte) ([]byte, *deviceoauth.SessionChange, error)) error {
	return errors.New("unused")
}
func (httpDeviceStore) CurrentMembership(context.Context, tenant.Scope, tenant.AccountID) (tenant.Membership, error) {
	return tenant.Membership{}, errors.New("unused")
}

type httpSessionStore struct{}

func (httpSessionStore) Issue(context.Context, tenant.Scope, devicesession.Mutation, devicesession.Record) error {
	return errors.New("unused")
}
func (httpSessionStore) Lookup(context.Context, tenant.Scope, [32]byte) (devicesession.Snapshot, error) {
	return devicesession.Snapshot{}, errors.New("unused")
}
func (httpSessionStore) Revoke(context.Context, tenant.Scope, devicesession.Mutation, [32]byte, tenant.Version) error {
	return errors.New("unused")
}
func (httpSessionStore) Rotate(context.Context, tenant.Scope, devicesession.Mutation, [32]byte, tenant.Version, devicesession.Record) error {
	return errors.New("unused")
}

type httpDeviceKeys struct{}

func (httpDeviceKeys) Approved(tenant.Scope, []byte) (string, map[string]string, error) {
	return "", nil, deviceoauth.ErrDenied
}

type envelopeHTTPStatus struct {
	inboxErr, deadErr error
	calls             int
}

func (s *envelopeHTTPStatus) InboxStatus(context.Context, tenant.Scope, tenant.ProjectID, int) ([]envelopeworker.InboxJob, error) {
	s.calls++
	return []envelopeworker.InboxJob{}, s.inboxErr
}
func (s *envelopeHTTPStatus) DeadLetters(context.Context, tenant.Scope, tenant.ProjectID, int) ([]envelopeworker.DeadLetter, error) {
	s.calls++
	return []envelopeworker.DeadLetter{}, s.deadErr
}

func TestDeviceHTTPBoundaryEnforcesMethodClosedBodyAndSafeErrors(t *testing.T) {
	manager, err := devicesession.New(httpSessionStore{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := deviceoauth.New(httpDeviceProvider{}, httpDeviceStore{}, manager, []byte(strings.Repeat("s", 32)), []deviceoauth.SubjectBinding{{Tenant: "tenant-a", Subject: "subject-a", Account: "account-a"}}, httpDeviceKeys{})
	if err != nil {
		t.Fatal(err)
	}
	api := NewAPI(limitedConfig(10, 10), nil, nil)
	if err := api.EnableDeviceAPI(service); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, path, body string
		status             int
	}{
		{http.MethodPost, "/v1/auth/whoami", "", http.StatusMethodNotAllowed},
		{http.MethodGet, "/v1/auth/token", "", http.StatusMethodNotAllowed},
		{http.MethodPost, "/v1/auth/device", `{} trailing`, http.StatusForbidden},
		{http.MethodPost, "/v1/auth/device", `{"tenant_id":"bad"}`, http.StatusForbidden},
		{http.MethodPost, "/v1/auth/token", `{}`, http.StatusForbidden},
		{http.MethodPost, "/v1/auth/revoke", `{"token_type_hint":"access_token","request_id":"r1"}`, http.StatusForbidden},
		{http.MethodGet, "/v1/auth/whoami", "", http.StatusForbidden},
	} {
		r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		r.RemoteAddr = "192.0.2.4:1234"
		w := httptest.NewRecorder()
		api.Handler().ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%s %s status=%d body=%s", tc.method, tc.path, w.Code, w.Body.String())
		}
		if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Pragma") != "no-cache" {
			t.Fatalf("sensitive auth response cache headers: %v", w.Header())
		}
		if strings.Contains(w.Body.String(), "unused") {
			t.Fatalf("internal error escaped: %s", w.Body.String())
		}
	}
}

func TestEnvelopeJobsUsesVerifiedIdentityAndMapsRepositoryFailures(t *testing.T) {
	d := &syncLimitDependencies{identity: tenant.VerifiedIdentity{Scope: tenant.Scope{Organization: "tenant-a"}, Account: "account-a", Device: "device-a", MembershipVersion: 1, PolicyRevision: 1}}
	inbox, err := envelopeworker.NewService(d, d, d)
	if err != nil {
		t.Fatal(err)
	}
	status := &envelopeHTTPStatus{}
	api := NewAPI(testConfig(), nil, nil)
	if err := api.EnableEnvelopeAPI(inbox, d, status); err != nil {
		t.Fatal(err)
	}
	call := func(method, path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, nil)
		r.Header.Set("Authorization", "Bearer test")
		w := httptest.NewRecorder()
		api.Handler().ServeHTTP(w, r)
		return w
	}
	if got := call(http.MethodPost, "/v1/sync/jobs?project=project-a"); got.Code != http.StatusMethodNotAllowed || status.calls != 0 {
		t.Fatalf("method gate=%d calls=%d", got.Code, status.calls)
	}
	if got := call(http.MethodGet, "/v1/sync/jobs"); got.Code != http.StatusBadRequest || status.calls != 0 {
		t.Fatalf("project gate=%d calls=%d body=%s", got.Code, status.calls, got.Body.String())
	}
	ok := call(http.MethodGet, "/v1/sync/jobs?project=project-a")
	if ok.Code != http.StatusOK || status.calls != 2 || !strings.Contains(ok.Body.String(), `"schema":"controlplane-jobs/v1"`) {
		t.Fatalf("jobs response=%d calls=%d body=%s", ok.Code, status.calls, ok.Body.String())
	}
	status.inboxErr = errors.New("database secret")
	failed := call(http.MethodGet, "/v1/sync/jobs?project=project-a")
	if failed.Code != http.StatusServiceUnavailable || strings.Contains(failed.Body.String(), "database secret") {
		t.Fatalf("repository failure leaked: %d %s", failed.Code, failed.Body.String())
	}
	status.inboxErr = nil
	status.deadErr = errors.New("database secret")
	failed = call(http.MethodGet, "/v1/sync/jobs?project=project-a")
	if failed.Code != http.StatusServiceUnavailable || strings.Contains(failed.Body.String(), "database secret") {
		t.Fatalf("dead-letter failure leaked: %d %s", failed.Code, failed.Body.String())
	}
	d.authErr = errors.New("bad credential")
	denied := call(http.MethodGet, "/v1/sync/jobs?project=project-a")
	if denied.Code != http.StatusForbidden {
		t.Fatalf("identity refusal=%d", denied.Code)
	}
}

func TestEnvelopeReceiveRejectsMethodBodyAndTenantRouteBeforeKeyLookup(t *testing.T) {
	d := &syncLimitDependencies{identity: tenant.VerifiedIdentity{Scope: tenant.Scope{Organization: "tenant-a"}, Account: "account-a", Device: "device-a", MembershipVersion: 1, PolicyRevision: 1}}
	inbox, err := envelopeworker.NewService(d, d, d)
	if err != nil {
		t.Fatal(err)
	}
	api := NewAPI(testConfig(), nil, nil)
	if err = api.EnableEnvelopeAPI(inbox, d, &envelopeHTTPStatus{}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, body string
		status       int
	}{{http.MethodGet, `{}`, http.StatusMethodNotAllowed}, {http.MethodPost, `{} trailing`, http.StatusBadRequest}, {http.MethodPost, `{"envelope":{"schema_version":1,"event_type":"test","tenant_id":"tenant-b","project_id":"project-a","event_id":"event-a","idempotency_key":"idem-a","producer_version":"v1","producer_sequence":1,"occurred_at":"2026-10-22T00:00:00Z","payload":{},"integrity":{"key_id":"key-a"}}}`, http.StatusForbidden}} {
		r := httptest.NewRequest(tc.method, "/v1/sync/envelopes", strings.NewReader(tc.body))
		r.Header.Set("Authorization", "Bearer test")
		w := httptest.NewRecorder()
		api.Handler().ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("receive status=%d body=%s", w.Code, w.Body.String())
		}
	}
	if d.keys != 0 || d.writes != 1 {
		t.Fatalf("route refusal crossed trust boundary: key lookups=%d audits=%d", d.keys, d.writes)
	}
	validRoute := `{"envelope":{"schema_version":1,"event_type":"test","tenant_id":"tenant-a","project_id":"project-a","event_id":"event-a","idempotency_key":"idem-a","producer_version":"v1","producer_sequence":1,"occurred_at":"2026-10-22T00:00:00Z","payload":{},"integrity":{"key_id":"key-a"}}}`
	r := httptest.NewRequest(http.MethodPost, "/v1/sync/envelopes", strings.NewReader(validRoute))
	r.Header.Set("Authorization", "Bearer test")
	w := httptest.NewRecorder()
	api.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable || d.keys != 1 || strings.Contains(w.Body.String(), "unexpected signing-key lookup") {
		t.Fatalf("key source failure=%d keys=%d body=%s", w.Code, d.keys, w.Body.String())
	}
}
