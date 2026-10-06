package deviceidentity

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/credentialstore"
)

// This store models the durable OS credential boundary without opening Keychain.
type refreshMemoryStore struct {
	mu     sync.Mutex
	key    string
	raw    []byte
	puts   int
	denied bool
}

func (s *refreshMemoryStore) Get(key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if key != s.key {
		return nil, credentialstore.ErrMissing
	}
	return bytes.Clone(s.raw), nil
}
func (s *refreshMemoryStore) Put(key string, raw []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.puts++
	if s.denied {
		return credentialstore.ErrDenied
	}
	if key != s.key {
		return credentialstore.ErrMissing
	}
	s.raw = bytes.Clone(raw)
	return nil
}
func (s *refreshMemoryStore) Delete(string) error {
	return errors.New("unexpected credential deletion")
}
func (s *refreshMemoryStore) snapshot(t *testing.T) (credentials, []byte, int) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	var secret credentials
	if err := json.Unmarshal(s.raw, &secret); err != nil {
		t.Fatal(err)
	}
	return secret, bytes.Clone(s.raw), s.puts
}
func refreshFixture(t *testing.T, provider string, pending bool) (*Client, *refreshMemoryStore) {
	t.Helper()
	// A mutant that incorrectly installs credentials must still stay in a temp profile.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	m := Metadata{SchemaVersion: 1, Provider: provider, TenantID: "tenant", AccountID: "account", DeviceID: "device", SigningKeyID: "device-key", LocalExpiresAt: time.Now().Add(-time.Hour)}
	secret := credentials{AccessToken: "test-old-access", RefreshToken: "test-old-refresh", PrivateKey: ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))}
	if pending {
		secret.PendingRefreshID = "persisted-refresh-request"
		secret.PendingRefreshAt = time.Now().UTC().Add(-time.Second)
	}
	raw, err := json.Marshal(secret)
	if err != nil {
		t.Fatal(err)
	}
	store := &refreshMemoryStore{key: credentialKey(provider, m.TenantID, m.DeviceID), raw: raw}
	return &Client{provider: provider, metadata: m, store: store, http: httpClient()}, store
}
func requireRefreshError(t *testing.T, err error, code string) {
	t.Helper()
	var state *Error
	if !errors.As(err, &state) || state.Code != code {
		t.Fatalf("expected %s error, got %v", code, err)
	}
}

type observedRefresh struct {
	GrantType    string `json:"grant_type"`
	RefreshToken string `json:"refresh_token"`
	RequestID    string `json:"request_id"`
}

func TestAuthenticatePersistsRefreshBeforeHTTPAndReplaysIdentity(t *testing.T) {
	type observation struct {
		request                     observedRefresh
		persisted                   credentials
		decodeErr                   error
		puts                        int
		method, path, authorization string
	}
	observations := make(chan observation, 2)
	var store *refreshMemoryStore
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request observedRefresh
		err := json.NewDecoder(r.Body).Decode(&request)
		store.mu.Lock()
		var persisted credentials
		if decodeErr := json.Unmarshal(store.raw, &persisted); err == nil {
			err = decodeErr
		}
		puts := store.puts
		store.mu.Unlock()
		observations <- observation{request, persisted, err, puts, r.Method, r.URL.Path, r.Header.Get("Authorization")}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"temporarily_unavailable"}`))
	}))
	defer server.Close()
	client, memory := refreshFixture(t, server.URL, false)
	store = memory
	var first observation
	for attempt := 0; attempt < 2; attempt++ {
		// Reconstructing the client models a process restart after an ambiguous reply.
		restarted := &Client{provider: client.provider, metadata: client.metadata, store: store, http: httpClient()}
		_, err := restarted.Authenticate(context.Background())
		requireRefreshError(t, err, "offline")
		got := <-observations
		if got.decodeErr != nil {
			t.Fatal(got.decodeErr)
		}
		if got.method != http.MethodPost || got.path != "/v1/auth/token" || got.authorization != "" {
			t.Fatal("refresh used unexpected HTTP contract")
		}
		if got.request.GrantType != "refresh_token" || got.request.RefreshToken != "test-old-refresh" {
			t.Fatal("refresh changed its grant or original credential")
		}
		if got.request.RequestID == "" || got.persisted.PendingRefreshID != got.request.RequestID || got.persisted.PendingRefreshAt.IsZero() || got.puts != 1 {
			t.Fatal("HTTP reached provider before a single durable refresh intent")
		}
		if attempt == 0 {
			first = got
		} else if got.request != first.request || !got.persisted.PendingRefreshAt.Equal(first.persisted.PendingRefreshAt) {
			t.Fatal("restart changed the persisted refresh replay identity or start time")
		}
	}
	secret, _, puts := store.snapshot(t)
	if puts != 1 || secret.AccessToken != "test-old-access" || secret.RefreshToken != "test-old-refresh" {
		t.Fatal("ambiguous provider reply changed installed credentials")
	}
}
func TestAuthenticateRefusesHTTPWhenRefreshPersistenceFails(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	client, store := refreshFixture(t, server.URL, false)
	_, before, _ := store.snapshot(t)
	store.denied = true
	_, err := client.Authenticate(context.Background())
	requireRefreshError(t, err, "credential_store_refused")
	_, after, puts := store.snapshot(t)
	if calls.Load() != 0 || puts != 1 || !bytes.Equal(before, after) {
		t.Fatal("failed persistence allowed HTTP or changed the durable credential")
	}
}
func TestAuthenticateMalformedRefreshDoesNotInstallCredentials(t *testing.T) {
	valid := tokenResponse{TokenType: "Bearer", AccessToken: "test-new-access", RefreshToken: "test-new-refresh", ExpiresIn: 3600, ServerTime: time.Now().UTC(), TenantID: "tenant", AccountID: "account", DeviceID: "device", Scope: "read", SigningKeyID: "device-key", ServerSigningKeys: []Key{{ID: "server-key", PublicKey: base64.StdEncoding.EncodeToString(make([]byte, ed25519.PublicKeySize))}}}
	cases := []struct {
		name, code string
		change     func(*tokenResponse)
		raw        string
	}{
		{name: "invalid JSON", code: "malformed_response", raw: `{"token_type":`},
		{name: "unknown field", code: "malformed_response", raw: `{"unexpected":true}`},
		{name: "missing refresh token", code: "malformed_response", change: func(v *tokenResponse) { v.RefreshToken = "" }},
		{name: "invalid token type", code: "malformed_response", change: func(v *tokenResponse) { v.TokenType = "Basic" }},
		{name: "invalid public signing key", code: "malformed_response", change: func(v *tokenResponse) { v.ServerSigningKeys = []Key{{ID: "server-key", PublicKey: "invalid"}} }},
		{name: "changed device binding", code: "identity_mismatch", change: func(v *tokenResponse) { v.DeviceID = "other-device" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reply := tc.raw
			if tc.change != nil {
				token := valid
				tc.change(&token)
				raw, err := json.Marshal(token)
				if err != nil {
					t.Fatal(err)
				}
				reply = string(raw)
			}
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != "/v1/auth/token" {
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				_, _ = w.Write([]byte(reply))
			}))
			defer server.Close()
			client, store := refreshFixture(t, server.URL, true)
			_, before, _ := store.snapshot(t)
			metadataBefore := client.metadata
			session, err := client.Authenticate(context.Background())
			requireRefreshError(t, err, tc.code)
			_, after, puts := store.snapshot(t)
			if calls.Load() != 1 || puts != 0 || !bytes.Equal(before, after) || !reflect.DeepEqual(client.metadata, metadataBefore) || session.Authorization() != "" {
				t.Fatal("malformed refresh installed credentials, changed metadata, or continued authentication")
			}
		})
	}
}
