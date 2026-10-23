package deviceoauth

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/mlnomadpy/dacli/cloud/internal/devicesession"
	"github.com/mlnomadpy/dacli/cloud/internal/tenant"
)

type lifecycleProvider struct{}

func (lifecycleProvider) Begin(context.Context) (Challenge, error) {
	return Challenge{DeviceCode: "provider-device-code", UserCode: "USER-CODE", VerificationURI: "https://issuer.example/verify", ExpiresIn: 900, Interval: 1}, nil
}
func (lifecycleProvider) Poll(context.Context, string) (string, error) { return "subject-a", nil }

type lifecycleKeys struct{}

func (lifecycleKeys) Approved(tenant.Scope, []byte) (string, map[string]string, error) {
	return "server-key", map[string]string{"server-key": base64.StdEncoding.EncodeToString(make([]byte, ed25519.PublicKeySize))}, nil
}

type lifecycleStore struct {
	mu          sync.Mutex
	flows       map[string][]byte
	memberships map[string]tenant.Membership
	sessions    map[string]devicesession.Snapshot
}

func newLifecycleStore() *lifecycleStore {
	return &lifecycleStore{flows: map[string][]byte{}, memberships: map[string]tenant.Membership{}, sessions: map[string]devicesession.Snapshot{}}
}
func authKey(scope tenant.Scope, id string) string { return string(scope.Organization) + ":" + id }
func sessionKey(scope tenant.Scope, digest [32]byte) string {
	return fmt.Sprintf("%s:%x", scope.Organization, digest)
}
func (s *lifecycleStore) CreateAuth(_ context.Context, scope tenant.Scope, id string, raw []byte, _ int64) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := authKey(scope, id)
	if old, ok := s.flows[key]; ok {
		return append([]byte(nil), old...), nil
	}
	s.flows[key] = append([]byte(nil), raw...)
	return append([]byte(nil), raw...), nil
}
func (s *lifecycleStore) UpdateAuth(_ context.Context, scope tenant.Scope, id string, update func([]byte) ([]byte, *SessionChange, error)) error {
	key := authKey(scope, id)
	s.mu.Lock()
	raw, ok := s.flows[key]
	raw = append([]byte(nil), raw...)
	s.mu.Unlock()
	if !ok {
		return ErrDenied
	}
	next, change, err := update(raw)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if change != nil {
		for _, oldID := range change.RevokeSessionIDs {
			for k, snapshot := range s.sessions {
				if snapshot.Session.Tenant == scope.Organization && snapshot.Session.ID == oldID {
					snapshot.Session.State = devicesession.StateRevoked
					snapshot.Session.Version++
					s.sessions[k] = snapshot
				}
			}
		}
		if change.Session.ID != "" {
			membership, ok := s.memberships[string(scope.Organization)+":"+string(change.Session.Account)]
			if !ok || membership.Version != change.ExpectedMembershipVersion || membership.State != tenant.LifecycleActive {
				return ErrDenied
			}
			s.sessions[sessionKey(scope, change.Session.CredentialDigest)] = devicesession.Snapshot{Session: change.Session, Device: change.Device, Membership: membership}
		}
	}
	s.flows[key] = append([]byte(nil), next...)
	return nil
}
func (s *lifecycleStore) CurrentMembership(_ context.Context, scope tenant.Scope, account tenant.AccountID) (tenant.Membership, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	membership, ok := s.memberships[string(scope.Organization)+":"+string(account)]
	if !ok {
		return tenant.Membership{}, ErrDenied
	}
	return membership, nil
}
func (s *lifecycleStore) Issue(_ context.Context, scope tenant.Scope, _ devicesession.Mutation, record devicesession.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	membership := s.memberships[string(scope.Organization)+":"+string(record.Account)]
	s.sessions[sessionKey(scope, record.CredentialDigest)] = devicesession.Snapshot{Session: record, Device: tenant.Device{Tenant: scope.Organization, ID: record.Device, Account: record.Account, Name: "test", Platform: tenant.PlatformDarwin, PublicKey: [32]byte{1}, State: tenant.LifecycleActive, Version: 1}, Membership: membership}
	return nil
}
func (s *lifecycleStore) Lookup(_ context.Context, scope tenant.Scope, digest [32]byte) (devicesession.Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot, ok := s.sessions[sessionKey(scope, digest)]
	if !ok {
		return devicesession.Snapshot{}, devicesession.ErrDenied
	}
	return snapshot, nil
}
func (*lifecycleStore) Revoke(context.Context, tenant.Scope, devicesession.Mutation, [32]byte, tenant.Version) error {
	return errors.New("unexpected standalone revoke")
}
func (*lifecycleStore) Rotate(context.Context, tenant.Scope, devicesession.Mutation, [32]byte, tenant.Version, devicesession.Record) error {
	return errors.New("unexpected standalone rotate")
}
func (s *lifecycleStore) setMembership(tenantID string, state tenant.Lifecycle) {
	s.mu.Lock()
	defer s.mu.Unlock()
	account := tenant.AccountID("account-a")
	s.memberships[tenantID+":"+string(account)] = tenant.Membership{Tenant: tenant.OrganizationID(tenantID), Account: account, Roles: 1 << tenant.RoleOwner, State: state, Version: 1}
}
func (s *lifecycleStore) flowBytes(scope tenant.Scope, id string) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.flows[authKey(scope, id)]...)
}
func (s *lifecycleStore) activeSessionCount(tenantID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, session := range s.sessions {
		if string(session.Session.Tenant) == tenantID && session.Session.State == devicesession.StateActive {
			count++
		}
	}
	return count
}
func (s *lifecycleStore) setMembershipState(tenantID string, state tenant.Lifecycle) {
	s.mu.Lock()
	defer s.mu.Unlock()
	membership := s.memberships[tenantID+":account-a"]
	membership.State = state
	s.memberships[tenantID+":account-a"] = membership
}

func lifecycleFixture(t *testing.T, tenantID string) (*Service, *lifecycleStore, BeginResponse) {
	t.Helper()
	store := newLifecycleStore()
	store.setMembership(tenantID, tenant.LifecycleActive)
	manager, err := devicesession.New(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(lifecycleProvider{}, store, manager, []byte("0123456789abcdef0123456789abcdef"), []SubjectBinding{{Tenant: tenantID, Subject: "subject-a", Account: "account-a"}}, lifecycleKeys{})
	if err != nil {
		t.Fatal(err)
	}
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	begin, err := service.Begin(context.Background(), BeginRequest{TenantID: tenantID, DeviceName: "test-device", Platform: "darwin", PublicKey: base64.StdEncoding.EncodeToString(pub), RequestID: "begin-" + tenantID})
	if err != nil {
		t.Fatal(err)
	}
	return service, store, begin
}

func TestTokenRotationKeepsOnlyLatestReplayAndRevokesSupersededSessions(t *testing.T) {
	service, store, begun := lifecycleFixture(t, "tenant-a")
	response, err := service.Token(context.Background(), TokenRequest{GrantType: "urn:ietf:params:oauth:grant-type:device_code", DeviceCode: begun.DeviceCode, RequestID: "device-poll"})
	if err != nil {
		t.Fatal(err)
	}
	initialRefresh := response.RefreshToken
	for rotation := 0; rotation < 40; rotation++ {
		prior := response
		requestID := fmt.Sprintf("refresh-%02d", rotation)
		response, err = service.Token(context.Background(), TokenRequest{GrantType: "refresh_token", RefreshToken: prior.RefreshToken, RequestID: requestID})
		if err != nil {
			t.Fatalf("rotation %d: %v", rotation+1, err)
		}
		if _, err = service.Who(context.Background(), "Bearer "+prior.AccessToken); !errors.Is(err, ErrDenied) {
			t.Fatalf("superseded access token remained valid after rotation %d: %v", rotation+1, err)
		}
		if replay, replayErr := service.Token(context.Background(), TokenRequest{GrantType: "refresh_token", RefreshToken: prior.RefreshToken, RequestID: requestID}); replayErr != nil || !reflect.DeepEqual(replay, response) {
			t.Fatalf("latest ambiguous refresh replay mismatch at %d: equal=%v err=%v", rotation+1, reflect.DeepEqual(replay, response), replayErr)
		}
	}
	if _, err = service.Token(context.Background(), TokenRequest{GrantType: "refresh_token", RefreshToken: initialRefresh, RequestID: "stale-refresh"}); !errors.Is(err, ErrDenied) {
		t.Fatalf("superseded refresh credential accepted: %v", err)
	}
	if store.activeSessionCount("tenant-a") != 1 {
		t.Fatalf("active sessions=%d, want exactly the latest session", store.activeSessionCount("tenant-a"))
	}
	scope, id, _, err := parse(response.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	f, err := service.open(scope, id, store.flowBytes(scope, id))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Responses) != 1 || len(f.Sessions) != 1 || f.Sessions[0] == "" {
		t.Fatalf("flow retained unbounded history: responses=%d sessions=%d", len(f.Responses), len(f.Sessions))
	}
	if err = service.Revoke(context.Background(), RevokeRequest{Token: response.RefreshToken, TokenTypeHint: "refresh_token", RequestID: "logout"}); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Who(context.Background(), "Bearer "+response.AccessToken); !errors.Is(err, ErrDenied) {
		t.Fatalf("revoked access token remained valid: %v", err)
	}
	if _, err = service.Token(context.Background(), TokenRequest{GrantType: "refresh_token", RefreshToken: response.RefreshToken, RequestID: "after-logout"}); !errors.Is(err, ErrDenied) {
		t.Fatalf("revoked refresh credential accepted: %v", err)
	}
}

func TestRefreshAndRevokeAreTenantBoundAndMembershipChecked(t *testing.T) {
	service, store, begun := lifecycleFixture(t, "tenant-a")
	issued, err := service.Token(context.Background(), TokenRequest{GrantType: "urn:ietf:params:oauth:grant-type:device_code", DeviceCode: begun.DeviceCode, RequestID: "poll"})
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(issued.RefreshToken, ".")
	parts[0] = base64.RawURLEncoding.EncodeToString([]byte("tenant-b"))
	crossTenant := strings.Join(parts, ".")
	if _, err = service.Token(context.Background(), TokenRequest{GrantType: "refresh_token", RefreshToken: crossTenant, RequestID: "cross-tenant"}); !errors.Is(err, ErrDenied) {
		t.Fatalf("cross-tenant refresh accepted: %v", err)
	}
	store.setMembershipState("tenant-a", tenant.LifecycleSuspended)
	if _, err = service.Token(context.Background(), TokenRequest{GrantType: "refresh_token", RefreshToken: issued.RefreshToken, RequestID: "membership-refused"}); !errors.Is(err, ErrDenied) {
		t.Fatalf("suspended membership refreshed: %v", err)
	}
	if store.activeSessionCount("tenant-a") != 1 {
		t.Fatalf("membership refusal changed sessions: %d", store.activeSessionCount("tenant-a"))
	}
	if err = service.Revoke(context.Background(), RevokeRequest{Token: crossTenant, TokenTypeHint: "refresh_token", RequestID: "cross-tenant-revoke"}); !errors.Is(err, ErrDenied) {
		t.Fatalf("cross-tenant revoke accepted: %v", err)
	}
}

func TestBeginRejectsSuspiciousProviderChallenges(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Challenge)
	}{
		{"equal_codes", func(c *Challenge) { c.UserCode = c.DeviceCode }},
		{"device_code_in_user_code", func(c *Challenge) { c.UserCode = "prefix-" + c.DeviceCode + "-suffix" }},
		{"device_code_in_uri", func(c *Challenge) { c.VerificationURI += "?device_code=" + c.DeviceCode }},
		{"missing_host", func(c *Challenge) { c.VerificationURI = "https:verify" }},
		{"fragment", func(c *Challenge) { c.VerificationURI += "#fragment" }},
		{"oversized_user_code", func(c *Challenge) { c.UserCode = strings.Repeat("x", 129) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newLifecycleStore()
			store.setMembership("tenant-a", tenant.LifecycleActive)
			manager, err := devicesession.New(store, nil)
			if err != nil {
				t.Fatal(err)
			}
			service, err := New(challengeProvider{edit: tc.edit}, store, manager, []byte("0123456789abcdef0123456789abcdef"), []SubjectBinding{{Tenant: "tenant-a", Subject: "subject-a", Account: "account-a"}}, lifecycleKeys{})
			if err != nil {
				t.Fatal(err)
			}
			pub, _, err := ed25519.GenerateKey(nil)
			if err != nil {
				t.Fatal(err)
			}
			_, err = service.Begin(context.Background(), BeginRequest{TenantID: "tenant-a", DeviceName: "test", Platform: "darwin", PublicKey: base64.StdEncoding.EncodeToString(pub), RequestID: "bad-challenge"})
			if !errors.Is(err, ErrDenied) {
				t.Fatalf("suspicious challenge accepted: %v", err)
			}
		})
	}
}

type challengeProvider struct{ edit func(*Challenge) }

func (p challengeProvider) Begin(context.Context) (Challenge, error) {
	challenge := Challenge{DeviceCode: "provider-device-code", UserCode: "USER-CODE", VerificationURI: "https://issuer.example/verify", ExpiresIn: 900, Interval: 1}
	p.edit(&challenge)
	return challenge, nil
}
func (challengeProvider) Poll(context.Context, string) (string, error) { return "subject-a", nil }
