package service

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/cloud/internal/envelopeworker"
	"github.com/mlnomadpy/dacli/cloud/internal/ratelimit"
	"github.com/mlnomadpy/dacli/cloud/internal/tenant"
	"github.com/mlnomadpy/dacli/internal/cloudsync"
)

type syncLimitDependencies struct {
	identity                                                                  tenant.VerifiedIdentity
	authErr                                                                   error
	verifies, resolves, deviceKeys, publicKeys, pulls, confirms, keys, writes int
	scopes                                                                    []tenant.Scope
}

func (d *syncLimitDependencies) Verify(context.Context, string) (tenant.VerifiedIdentity, error) {
	d.verifies++
	return d.identity, d.authErr
}
func (d *syncLimitDependencies) VerifyWork(ctx context.Context, credential string) (tenant.VerifiedIdentity, error) {
	return d.Verify(ctx, credential)
}
func (d *syncLimitDependencies) Resolve(_ context.Context, id tenant.VerifiedIdentity, route cloudsync.SyncRoute) (cloudsync.ProjectBindingResponse, error) {
	d.resolves++
	d.scopes = append(d.scopes, id.Scope)
	return cloudsync.ProjectBindingResponse{SchemaVersion: 1, Route: route, BindingVersion: 1}, nil
}
func (d *syncLimitDependencies) DeviceKey(context.Context, tenant.VerifiedIdentity) (string, error) {
	d.deviceKeys++
	return "device-key", nil
}
func (d *syncLimitDependencies) DevicePublicKey(context.Context, tenant.VerifiedIdentity) ([]byte, error) {
	d.publicKeys++
	return make([]byte, ed25519.PublicKeySize), nil
}
func (d *syncLimitDependencies) Pull(_ context.Context, scope tenant.Scope, _ tenant.ProjectID, _, _ uint64, _ []string) (envelopeworker.PullPage, error) {
	d.pulls++
	d.scopes = append(d.scopes, scope)
	return envelopeworker.PullPage{}, nil
}
func (d *syncLimitDependencies) Confirmed(context.Context, tenant.Scope, cloudsync.Envelope) (bool, error) {
	d.confirms++
	return true, nil
}
func (d *syncLimitDependencies) TrustedKeys(context.Context, tenant.Scope, tenant.ProjectID) (map[string]ed25519.PublicKey, error) {
	d.keys++
	return nil, errors.New("unexpected signing-key lookup")
}
func (d *syncLimitDependencies) Accept(context.Context, tenant.VerifiedIdentity, cloudsync.Envelope, envelopeworker.Audit) (cloudsync.Outcome, error) {
	d.writes++
	return cloudsync.OutcomeAccepted, nil
}
func (d *syncLimitDependencies) RecordDecision(context.Context, envelopeworker.Audit) error {
	d.writes++
	return nil
}
func syncLimitFixture(t *testing.T, identityCapacity, readCapacity int) (*API, *syncLimitDependencies) {
	t.Helper()
	d := &syncLimitDependencies{identity: tenant.VerifiedIdentity{Scope: tenant.Scope{Organization: "tenant-a"}, Account: "account-a", Device: "device-a", MembershipVersion: 1, PolicyRevision: 1}}
	inbox, err := envelopeworker.NewService(d, d, d)
	if err != nil {
		t.Fatal(err)
	}
	api := NewAPI(limitedConfig(identityCapacity, readCapacity), nil, nil)
	if err = api.EnableSyncAPI(d, d, inbox, d, []byte(testSigningSecret)); err != nil {
		t.Fatal(err)
	}
	return api, d
}
func syncLimitRequest(t *testing.T, api *API, method, path, peer string, envelopes []cloudsync.Envelope) *httptest.ResponseRecorder {
	t.Helper()
	var body []byte
	if method == http.MethodPost {
		var err error
		body, err = json.Marshal(cloudsync.ExchangeRequest{SchemaVersion: 1, Route: cloudsync.SyncRoute{TenantID: "tenant-a", ProjectID: "project-a", DeviceID: "device-a"}, BindingVersion: 1, Envelopes: envelopes})
		if err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	request.RemoteAddr = peer
	request.Header.Set("Authorization", "Bearer test-device")
	request.Header.Set("X-Forwarded-For", "203.0.113.1")
	recorder := httptest.NewRecorder()
	api.Handler().ServeHTTP(recorder, request)
	return recorder
}
func requireSyncRateLimit(t *testing.T, response *httptest.ResponseRecorder, retry string) {
	t.Helper()
	var out errorResponse
	if err := json.Unmarshal(response.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusTooManyRequests || response.Header().Get("Retry-After") != retry || out.Code != "rate_limited" || !out.Retryable || out.Schema != errorSchema {
		t.Fatalf("rate limit response=%d retry=%q body=%s", response.Code, response.Header().Get("Retry-After"), response.Body.String())
	}
}
func TestSyncPreAuthenticationLimitsPreventDependencyWork(t *testing.T) {
	for _, endpoint := range []struct{ method, path string }{{http.MethodGet, "/v1/sync/binding"}, {http.MethodPost, "/v1/sync/exchange"}} {
		t.Run(endpoint.path, func(t *testing.T) {
			api, d := syncLimitFixture(t, 1, 10)
			d.authErr = errors.New("invalid device credential")
			first := syncLimitRequest(t, api, endpoint.method, endpoint.path, "192.0.2.1:1234", nil)
			if first.Code != http.StatusForbidden || d.verifies != 1 {
				t.Fatalf("first=%d verifies=%d", first.Code, d.verifies)
			}
			limited := syncLimitRequest(t, api, endpoint.method, endpoint.path, "192.0.2.1:5678", nil)
			requireSyncRateLimit(t, limited, "3600")
			if d.verifies != 1 || d.resolves != 0 || d.deviceKeys != 0 || d.publicKeys != 0 || d.pulls != 0 || d.writes != 0 {
				t.Fatalf("limited request reached dependencies: %+v", d)
			}
			other := syncLimitRequest(t, api, endpoint.method, endpoint.path, "192.0.2.2:1234", nil)
			if other.Code != http.StatusForbidden || d.verifies != 2 {
				t.Fatalf("distinct direct peer=%d verifies=%d", other.Code, d.verifies)
			}
		})
	}
}
func TestSyncReadLimitsCoverBindingAndEmptyExchange(t *testing.T) {
	for _, endpoint := range []struct {
		method, path    string
		resolves, pulls int
	}{{http.MethodGet, "/v1/sync/binding", 1, 0}, {http.MethodPost, "/v1/sync/exchange", 2, 1}} {
		t.Run(endpoint.path, func(t *testing.T) {
			api, d := syncLimitFixture(t, 20, 1)
			first := syncLimitRequest(t, api, endpoint.method, endpoint.path, "192.0.2.1:1234", nil)
			if first.Code != http.StatusOK || d.resolves != endpoint.resolves || d.pulls != endpoint.pulls {
				t.Fatalf("first=%d dependencies=%+v", first.Code, d)
			}
			limited := syncLimitRequest(t, api, endpoint.method, endpoint.path, "192.0.2.2:1234", nil)
			requireSyncRateLimit(t, limited, "3600")
			if d.resolves != endpoint.resolves || d.pulls != endpoint.pulls || d.deviceKeys != endpoint.pulls || d.publicKeys != endpoint.pulls {
				t.Fatalf("limited read reached binding or pull: %+v", d)
			}
		})
	}
}
func TestSyncReadBucketsUseVerifiedTenantAccountAndDevice(t *testing.T) {
	for _, endpoint := range []struct{ method, path string }{{http.MethodGet, "/v1/sync/binding?tenant_id=forged"}, {http.MethodPost, "/v1/sync/exchange"}} {
		t.Run(endpoint.path, func(t *testing.T) {
			api, d := syncLimitFixture(t, 30, 1)
			original := d.identity
			if got := syncLimitRequest(t, api, endpoint.method, endpoint.path, "192.0.2.1:1", nil); got.Code != http.StatusOK {
				t.Fatal(got.Code)
			}
			identities := []tenant.VerifiedIdentity{original, original, original}
			identities[0].Scope.Organization = "tenant-b"
			identities[1].Account = "account-b"
			identities[2].Device = "device-b"
			for _, identity := range identities {
				d.identity = identity
				got := syncLimitRequest(t, api, endpoint.method, endpoint.path, "192.0.2.1:1", nil)
				if got.Code != http.StatusOK {
					t.Fatalf("distinct verified identity shared exhausted bucket: %d", got.Code)
				}
			}
			d.identity = original
			requireSyncRateLimit(t, syncLimitRequest(t, api, endpoint.method, endpoint.path, "192.0.2.1:1", nil), "3600")
			if len(d.scopes) == 0 || d.scopes[0] != original.Scope {
				t.Fatal("payload tenant replaced verified scope")
			}
		})
	}
}
func TestSyncIngestionLimitReturns429BeforePersistenceOrPull(t *testing.T) {
	for _, interval := range []time.Duration{time.Millisecond, 1500 * time.Millisecond, time.Hour} {
		t.Run(interval.String(), func(t *testing.T) {
			api, d := syncLimitFixture(t, 10, 10)
			limiter, err := ratelimit.New(ratelimit.Policy{Capacity: 1, RefillInterval: interval, MaxKeys: 4, IdleTTL: time.Hour}, func() time.Time { return time.Unix(100, 0) })
			if err != nil {
				t.Fatal(err)
			}
			decision, err := limiter.Allow(context.Background(), ratelimit.IdentityKey([]byte(testSigningSecret), d.identity, "sync-ingest"))
			if err != nil || !decision.Allowed {
				t.Fatal("could not exhaust real ingestion limiter")
			}
			inbox, err := envelopeworker.NewLimitedService(d, d, d, limiter, []byte(testSigningSecret))
			if err != nil {
				t.Fatal(err)
			}
			api.sync.inbox = inbox
			envelope := cloudsync.Envelope{TenantID: "tenant-a", ProjectID: "project-a"}
			envelope.Integrity.KeyID = "device-key"
			response := syncLimitRequest(t, api, http.MethodPost, "/v1/sync/exchange", "192.0.2.1:1", []cloudsync.Envelope{envelope})
			retry := "1"
			if interval == 1500*time.Millisecond {
				retry = "2"
			}
			if interval == time.Hour {
				retry = "3600"
			}
			requireSyncRateLimit(t, response, retry)
			if d.keys != 0 || d.writes != 0 || d.confirms != 0 || d.pulls != 0 || strings.Contains(response.Body.String(), "sync_unavailable") {
				t.Fatalf("ingestion limit proceeded or became 503: %+v", d)
			}
		})
	}
}
