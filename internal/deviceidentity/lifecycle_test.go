package deviceidentity

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/credentialstore"
)

type lifecycleStore struct {
	mu      sync.Mutex
	items   map[string][]byte
	deleted []string
}

func newLifecycleStore() *lifecycleStore { return &lifecycleStore{items: map[string][]byte{}} }
func (s *lifecycleStore) Get(key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, ok := s.items[key]
	if !ok {
		return nil, credentialstore.ErrMissing
	}
	return bytes.Clone(raw), nil
}
func (s *lifecycleStore) Put(key string, raw []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[key] = bytes.Clone(raw)
	return nil
}
func (s *lifecycleStore) Delete(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleted = append(s.deleted, key)
	delete(s.items, key)
	return nil
}

func lifecycleChallenge(t *testing.T, expires int64) (*httptest.Server, *lifecycleStore, *http.Client, func(context.Context, time.Duration) error, *[]time.Duration) {
	t.Helper()
	store := newLifecycleStore()
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/auth/device" {
			_ = json.NewEncoder(w).Encode(map[string]any{"device_code": "device-secret-marker", "user_code": "human-code", "verification_uri": "https://identity.example/activate", "expires_in": expires, "interval": 1, "server_time": time.Now().UTC()})
			return
		}
		if r.URL.Path != "/v1/auth/token" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"authorization_pending"}`))
	}))
	var waits []time.Duration
	wait := func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		return nil
	}
	return server, store, &http.Client{Timeout: 2 * time.Second}, wait, &waits
}

func loginTest(t *testing.T, ctx context.Context, server *httptest.Server, store credentialstore.Store, client *http.Client, wait func(context.Context, time.Duration) error) (Metadata, error) {
	t.Helper()
	home := canonicalTestHome(t)
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	return loginWithStore(ctx, server.URL, "tenant", "test-device", func(Challenge) error { return nil }, store, client, wait)
}

func canonicalTestHome(t *testing.T) string {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return home
}

func tokenReply(t *testing.T) []byte {
	t.Helper()
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	value := tokenResponse{TokenType: "Bearer", AccessToken: "access-secret-marker", RefreshToken: "refresh-secret-marker", ExpiresIn: 3600, ServerTime: time.Now().UTC(), TenantID: "tenant", AccountID: "account", DeviceID: "device", Scope: "device sync", SigningKeyID: "signing-key", ServerSigningKeys: []Key{{ID: "server-key", PublicKey: base64.StdEncoding.EncodeToString(public)}}}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestLoginPendingSlowDownAndSuccess(t *testing.T) {
	server, store, client, wait, waits := lifecycleChallenge(t, 60)
	defer server.Close()
	var polls int
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/auth/device" {
			_ = json.NewEncoder(w).Encode(map[string]any{"device_code": "device-secret-marker", "user_code": "human-code", "verification_uri": "https://identity.example/activate", "expires_in": 60, "interval": 1, "server_time": time.Now().UTC()})
			return
		}
		polls++
		switch polls {
		case 1:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"authorization_pending"}`))
		case 2:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"slow_down"}`))
		default:
			_, _ = w.Write(tokenReply(t))
		}
	})
	server.Start()
	metadata, err := loginTest(t, context.Background(), server, store, client, wait)
	if err != nil {
		t.Fatal(err)
	}
	if polls != 3 || !reflect.DeepEqual(*waits, []time.Duration{time.Second, time.Second, 6 * time.Second}) {
		t.Fatalf("polls=%d waits=%v", polls, *waits)
	}
	key := credentialKey(server.URL, "tenant", "device")
	raw, err := os.ReadFile(filepath.Join(mustMetadataDirectory(t), key+".json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"access-secret-marker", "refresh-secret-marker", "device-secret-marker"} {
		if bytes.Contains(raw, []byte(marker)) {
			t.Fatalf("metadata persisted secret marker %q", marker)
		}
	}
	if metadata.DeviceID != "device" {
		t.Fatalf("installed device = %q", metadata.DeviceID)
	}
	stored, err := store.Get(key)
	if err != nil || !bytes.Contains(stored, []byte("access-secret-marker")) {
		t.Fatalf("credential not stored at native-store boundary: err=%v", err)
	}
}

func mustMetadataDirectory(t *testing.T) string {
	t.Helper()
	dir, err := metadataDirectory()
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLoginDeniedAndOfflineAreTerminal(t *testing.T) {
	for _, tc := range []struct {
		name, response, want string
	}{
		{"denied", `{"error":"access_denied"}`, "access_denied"},
		{"offline", `{"error":"temporarily_unavailable"}`, "offline"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, store, client, wait, _ := lifecycleChallenge(t, 60)
			defer server.Close()
			server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/auth/device" {
					_ = json.NewEncoder(w).Encode(map[string]any{"device_code": "device-secret-marker", "user_code": "human-code", "verification_uri": "https://identity.example/activate", "expires_in": 60, "interval": 1, "server_time": time.Now().UTC()})
					return
				}
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, tc.response)
			})
			server.Start()
			_, err := loginTest(t, context.Background(), server, store, client, wait)
			var state *Error
			if !errors.As(err, &state) || state.Code != tc.want {
				t.Fatalf("error = %v, want %s", err, tc.want)
			}
			if len(store.items) != 0 {
				t.Fatal("terminal denial/offline response installed credentials")
			}
		})
	}
}

func TestLoginChallengeNeverExposesDeviceCodeInHumanFields(t *testing.T) {
	for _, tc := range []struct {
		name, userCode, uri string
	}{
		{name: "user code substring", userCode: "hint-private-code/hidden", uri: "https://identity.example/activate"},
		{name: "escaped verification uri", userCode: "human-code", uri: "https://identity.example/activate?next=" + url.QueryEscape("private-code/hidden")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, store, client, _, _ := lifecycleChallenge(t, 60)
			defer server.Close()
			server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"device_code": "private-code/hidden", "user_code": tc.userCode, "verification_uri": tc.uri, "expires_in": 60, "interval": 1, "server_time": time.Now().UTC()})
			})
			server.Start()
			shown := false
			home := canonicalTestHome(t)
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", home)
			_, err := loginWithStore(context.Background(), server.URL, "tenant", "test-device", func(Challenge) error {
				shown = true
				return nil
			}, store, client, func(context.Context, time.Duration) error {
				t.Fatal("unsafe challenge reached polling")
				return nil
			})
			var state *Error
			if !errors.As(err, &state) || state.Code != "malformed_response" || shown {
				t.Fatalf("unsafe challenge err=%v shown=%v", err, shown)
			}
		})
	}
}

func TestLoginDeadlineDuringPollIsExpiryAndCallerCancelStaysCancelled(t *testing.T) {
	t.Run("challenge deadline", func(t *testing.T) {
		server, store, client, wait, _ := lifecycleChallenge(t, 1)
		defer server.Close()
		server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/auth/device" {
				_ = json.NewEncoder(w).Encode(map[string]any{"device_code": "device-secret-marker", "user_code": "human-code", "verification_uri": "https://identity.example/activate", "expires_in": 1, "interval": 1, "server_time": time.Now().UTC()})
				return
			}
			timer := time.NewTimer(1100 * time.Millisecond)
			defer timer.Stop()
			<-timer.C
			w.WriteHeader(http.StatusServiceUnavailable)
		})
		server.Start()
		_, err := loginTest(t, context.Background(), server, store, client, wait)
		requireRefreshError(t, err, "expired_token")
	})
	t.Run("caller cancellation", func(t *testing.T) {
		server, store, client, wait, _ := lifecycleChallenge(t, 60)
		defer server.Close()
		ctx, cancel := context.WithCancel(context.Background())
		server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/auth/device" {
				_ = json.NewEncoder(w).Encode(map[string]any{"device_code": "device-secret-marker", "user_code": "human-code", "verification_uri": "https://identity.example/activate", "expires_in": 60, "interval": 1, "server_time": time.Now().UTC()})
				return
			}
			cancel()
			timer := time.NewTimer(20 * time.Millisecond)
			defer timer.Stop()
			<-timer.C
			w.WriteHeader(http.StatusServiceUnavailable)
		})
		server.Start()
		_, err := loginTest(t, ctx, server, store, client, wait)
		requireRefreshError(t, err, "cancelled")
	})
}

func TestLoginChallengeDeadlineWhileWaitingExpiresWithoutPolling(t *testing.T) {
	var polls int
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/auth/device" {
			_ = json.NewEncoder(w).Encode(map[string]any{"device_code": "device-secret-marker", "user_code": "human-code", "verification_uri": "https://identity.example/activate", "expires_in": 1, "interval": 60, "server_time": time.Now().UTC()})
			return
		}
		polls++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	server.Start()
	defer server.Close()
	store := newLifecycleStore()
	home := canonicalTestHome(t)
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	_, err := loginWithStore(context.Background(), server.URL, "tenant", "test-device", func(Challenge) error { return nil }, store, &http.Client{Timeout: 2 * time.Second}, waitForPoll)
	requireRefreshError(t, err, "expired_token")
	if polls != 0 || len(store.items) != 0 {
		t.Fatalf("expired challenge made %d token polls and stored %d credentials", polls, len(store.items))
	}
}

func TestInstallAndWhoamiKeepServerExpirySeparateFromLocalClock(t *testing.T) {
	for _, offset := range []time.Duration{-48 * time.Hour, 48 * time.Hour} {
		t.Run(offset.String(), func(t *testing.T) {
			const ttl = 10 * time.Minute
			serverTime := time.Now().UTC().Add(offset).Truncate(time.Second)
			public, _, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			trusted := []Key{{ID: "server-key", PublicKey: base64.StdEncoding.EncodeToString(public)}}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1/auth/device":
					_ = json.NewEncoder(w).Encode(map[string]any{"device_code": "device-secret-marker", "user_code": "human-code", "verification_uri": "https://identity.example/activate", "expires_in": int64(ttl.Seconds()), "interval": 1, "server_time": serverTime})
				case "/v1/auth/token":
					_ = json.NewEncoder(w).Encode(tokenResponse{TokenType: "Bearer", AccessToken: "access-secret-marker", RefreshToken: "refresh-secret-marker", ExpiresIn: int64(ttl.Seconds()), ServerTime: serverTime, TenantID: "tenant", AccountID: "account", DeviceID: "device", Scope: "device sync", SigningKeyID: "signing-key", ServerSigningKeys: trusted})
				case "/v1/auth/whoami":
					_ = json.NewEncoder(w).Encode(whoResponse{TenantID: "tenant", AccountID: "account", DeviceID: "device", Scope: "device sync", ExpiresAt: serverTime.Add(ttl), ServerTime: serverTime, SigningKeyID: "signing-key", ServerSigningKeys: trusted})
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()

			store := newLifecycleStore()
			home := canonicalTestHome(t)
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", home)
			started := time.Now()
			installed, err := loginWithStore(context.Background(), server.URL, "tenant", "test-device", func(Challenge) error { return nil }, store, server.Client(), func(context.Context, time.Duration) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			assertLocalExpiry(t, installed.LocalExpiresAt, started, ttl)
			wantServerExpiry := serverTime.Add(ttl)
			if !installed.ExpiresAt.Equal(wantServerExpiry) {
				t.Fatalf("installed server expiry = %s, want %s", installed.ExpiresAt, wantServerExpiry)
			}

			client := &Client{provider: server.URL, metadata: installed, store: store, http: server.Client()}
			whoStarted := time.Now()
			confirmed, err := client.Whoami(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			assertLocalExpiry(t, confirmed.LocalExpiresAt, whoStarted, ttl)
			if !confirmed.ExpiresAt.Equal(wantServerExpiry) {
				t.Fatalf("whoami server expiry = %s, want %s", confirmed.ExpiresAt, wantServerExpiry)
			}

			metadataBytes, err := os.ReadFile(filepath.Join(mustMetadataDirectory(t), credentialKey(server.URL, "tenant", "device")+".json"))
			if err != nil {
				t.Fatal(err)
			}
			for _, marker := range []string{"access-secret-marker", "refresh-secret-marker", "device-secret-marker"} {
				if bytes.Contains(metadataBytes, []byte(marker)) {
					t.Fatalf("public metadata exposed secret marker %q", marker)
				}
			}
		})
	}
}

func assertLocalExpiry(t *testing.T, got time.Time, started time.Time, ttl time.Duration) {
	t.Helper()
	want := started.Add(ttl - min(30*time.Second, ttl/10))
	if delta := got.Sub(want); delta < -2*time.Second || delta > 2*time.Second {
		t.Fatalf("local expiry %s is not relative to local clock (started %s, near %s; delta %s)", got, started, want, delta)
	}
}

func TestSessionStringAndJSONRedactCredentials(t *testing.T) {
	session := Session{access: "access-secret-marker", private: ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))}
	encoded, err := json.Marshal(session)
	if err != nil {
		t.Fatal(err)
	}
	for _, output := range []string{session.String(), session.GoString(), string(encoded)} {
		if strings.Contains(output, "access-secret-marker") || strings.Contains(output, "private_key") || strings.Contains(output, "access_token") {
			t.Fatalf("session output exposed credentials: %s", output)
		}
	}
}

func TestOpenForLogoutRemovesExactCredentialWithCorruptMetadata(t *testing.T) {
	assertLogoutWithUnusableMetadata(t, []byte("corrupt metadata"))
}

func TestOpenForLogoutRemovesExactCredentialWithMissingMetadata(t *testing.T) {
	assertLogoutWithUnusableMetadata(t, nil)
}

func assertLogoutWithUnusableMetadata(t *testing.T, selectedMetadata []byte) {
	t.Helper()
	home := canonicalTestHome(t)
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	store := newLifecycleStore()
	secret, err := json.Marshal(credentials{AccessToken: "access-secret-marker", RefreshToken: "refresh-secret-marker", PrivateKey: ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))})
	if err != nil {
		t.Fatal(err)
	}
	dir, err := metadataDirectory()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":"temporarily_unavailable"}`)
	}))
	defer server.Close()
	provider := server.URL
	selectedKey := credentialKey(provider, "tenant", "device")
	neighborKey := credentialKey(provider, "tenant", "other-device")
	_ = store.Put(selectedKey, secret)
	_ = store.Put(neighborKey, []byte("neighbor credential"))
	selectedMeta := filepath.Join(dir, selectedKey+".json")
	neighborMeta := filepath.Join(dir, neighborKey+".json")
	if selectedMetadata != nil {
		if err := os.WriteFile(selectedMeta, selectedMetadata, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(neighborMeta, []byte(`{"neighbor":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	client, err := newLogoutClient(provider, "tenant", "device", store, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Logout(context.Background())
	var state *Error
	if !errors.As(err, &state) || state.Code != "offline" || !result.LocalRemoved || result.RemoteRevoked || result.State != "remote_revoke_unknown" {
		t.Fatalf("logout result=%+v error=%v", result, err)
	}
	if _, err := store.Get(selectedKey); !errors.Is(err, credentialstore.ErrMissing) {
		t.Fatalf("selected credential remains: %v", err)
	}
	if got, err := store.Get(neighborKey); err != nil || string(got) != "neighbor credential" {
		t.Fatalf("neighbor credential changed: %q, %v", got, err)
	}
	if !reflect.DeepEqual(store.deleted, []string{selectedKey}) {
		t.Fatalf("deleted keys = %v", store.deleted)
	}
	if _, err := os.Stat(selectedMeta); !os.IsNotExist(err) {
		t.Fatalf("selected metadata remains: %v", err)
	}
	if _, err := os.Stat(neighborMeta); err != nil {
		t.Fatalf("neighbor metadata changed: %v", err)
	}
	if strings.Contains(err.Error(), "access-secret-marker") || strings.Contains(err.Error(), "refresh-secret-marker") {
		t.Fatal("logout diagnostic exposed a credential marker")
	}
}
