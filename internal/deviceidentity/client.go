// Package deviceidentity implements provider-neutral device OAuth without
// persisting bearer, refresh or Ed25519 private keys in filesystem profiles.
package deviceidentity

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/mlnomadpy/dacli/internal/credentialstore"
	"io"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"time"
)

type Error struct {
	Code      string
	Retryable bool
	Next      string
}

func (e *Error) Error() string                           { return e.Code + ": " + e.Next }
func failure(code string, retry bool, next string) error { return &Error{code, retry, next} }
func storeFailure(e error) error {
	if errors.Is(e, credentialstore.ErrUnsupported) {
		return failure("unsupported_store", false, "use a macOS build with native Keychain support; no file credential fallback is available")
	}
	return failure("credential_store_refused", false, "allow the exact native credential operation or repair the OS credential store")
}

type credentials struct {
	AccessToken       string    `json:"access_token"`
	RefreshToken      string    `json:"refresh_token"`
	PrivateKey        []byte    `json:"private_key"`
	PendingRefreshID  string    `json:"pending_refresh_id,omitempty"`
	PendingRefreshAt  time.Time `json:"pending_refresh_at,omitempty"`
	InstalledMetadata *Metadata `json:"installed_metadata,omitempty"`
}
type Session struct {
	Provider, TenantID, AccountID, DeviceID, SigningKeyID string
	Scopes                                                []string
	ExpiresAt                                             time.Time
	TrustedKeys                                           map[string]ed25519.PublicKey
	access                                                string
	private                                               ed25519.PrivateKey
}

func (s Session) Authorization() string {
	if s.access == "" {
		return ""
	}
	return "Bearer " + s.access
}
func (s Session) Sign(raw []byte) []byte {
	if len(s.private) != ed25519.PrivateKeySize {
		return nil
	}
	return ed25519.Sign(s.private, raw)
}
func (s Session) SigningKey() ed25519.PrivateKey {
	return append(ed25519.PrivateKey(nil), s.private...)
}
func (s Session) String() string   { return "DeviceSession" }
func (s Session) GoString() string { return "DeviceSession" }
func (s Session) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Provider, TenantID, AccountID, DeviceID, SigningKeyID string
		Scopes                                                []string
		ExpiresAt                                             time.Time
	}{s.Provider, s.TenantID, s.AccountID, s.DeviceID, s.SigningKeyID, s.Scopes, s.ExpiresAt})
}

type Client struct {
	provider string
	metadata Metadata
	store    credentialstore.Store
	http     *http.Client
}

func (c Client) String() string   { return "DeviceIdentityClient" }
func (c Client) GoString() string { return "DeviceIdentityClient" }
func httpClient() *http.Client {
	return &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("identity redirects forbidden") }, Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 10 * time.Second, MaxResponseHeaderBytes: 65536}}
}
func Open(provider, tenant, device string) (*Client, error) {
	p, e := providerBase(provider)
	if e != nil {
		return nil, e
	}
	if !opaqueID(tenant) || !opaqueID(device) {
		return nil, failure("identity_required", false, "select exact provider, tenant and device")
	}
	store, e := credentialstore.Open()
	if e != nil {
		return nil, storeFailure(e)
	}
	m, e := readMetadata(p, tenant, device)
	if e != nil {
		return nil, e
	}
	return &Client{p, m, store, httpClient()}, nil
}
func requestID() string {
	var id [16]byte
	if _, e := rand.Read(id[:]); e != nil {
		return ""
	}
	return hex.EncodeToString(id[:])
}
func (c *Client) request(ctx context.Context, method, path string, input any, authorization string, out any) error {
	if ctx == nil {
		return failure("cancelled", false, "retry only after selecting a valid request context")
	}
	var body []byte
	if input != nil {
		var e error
		body, e = json.Marshal(input)
		if e != nil {
			return failure("invalid_request", false, "repair the device request")
		}
	}
	req, e := http.NewRequestWithContext(ctx, method, c.provider+path, bytes.NewReader(body))
	if e != nil {
		return failure("invalid_provider", false, "repair the explicit provider")
	}
	req.Header.Set("Content-Type", "application/json")
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	response, e := c.http.Do(req)
	if e != nil {
		if ctx.Err() != nil {
			return failure("cancelled", false, "device request cancelled; no credential success was inferred")
		}
		return failure("offline", true, "restore the selected provider connection; no authentication success was inferred")
	}
	defer func() { _ = response.Body.Close() }()
	raw, e := io.ReadAll(io.LimitReader(response.Body, 65537))
	if e != nil || len(raw) > 65536 {
		return failure("malformed_response", false, "provider response exceeded the device contract")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var refused struct {
			Error string `json:"error"`
		}
		if decodeClosed(raw, &refused) != nil {
			return failure("provider_refused", false, "inspect the selected provider; its response did not match the closed device contract")
		}
		switch refused.Error {
		case "authorization_pending", "slow_down":
			return failure(refused.Error, true, "wait for the provider polling interval")
		case "access_denied", "expired_token", "invalid_grant", "revoked":
			return failure(refused.Error, false, "run auth login for this exact tenant and device")
		case "temporarily_unavailable":
			return failure("offline", true, "restore the selected provider and retry the exact request")
		}
		return failure("provider_refused", false, "provider returned an unsupported authentication state")
	}
	if out == nil {
		return nil
	}
	if decodeClosed(raw, out) != nil {
		return failure("malformed_response", false, "provider response did not match the closed device contract")
	}
	return nil
}

type Key struct {
	ID        string `json:"key_id"`
	PublicKey string `json:"public_key"`
}
type tokenResponse struct {
	TokenType         string    `json:"token_type"`
	AccessToken       string    `json:"access_token"`
	RefreshToken      string    `json:"refresh_token"`
	ExpiresIn         int64     `json:"expires_in"`
	ServerTime        time.Time `json:"server_time"`
	TenantID          string    `json:"tenant_id"`
	AccountID         string    `json:"account_id"`
	DeviceID          string    `json:"device_id"`
	Scope             string    `json:"scope"`
	SigningKeyID      string    `json:"signing_key_id"`
	ServerSigningKeys []Key     `json:"server_signing_keys"`
}
type whoResponse struct {
	TenantID          string    `json:"tenant_id"`
	AccountID         string    `json:"account_id"`
	DeviceID          string    `json:"device_id"`
	Scope             string    `json:"scope"`
	ExpiresAt         time.Time `json:"expires_at"`
	ServerTime        time.Time `json:"server_time"`
	SigningKeyID      string    `json:"signing_key_id"`
	ServerSigningKeys []Key     `json:"server_signing_keys"`
}

func trustedKeys(keys []Key) (map[string]string, error) {
	if len(keys) == 0 || len(keys) > 100 {
		return nil, failure("malformed_response", false, "provider signing keys unavailable")
	}
	out := map[string]string{}
	for _, key := range keys {
		public, e := base64.StdEncoding.DecodeString(key.PublicKey)
		if !opaqueID(key.ID) || e != nil || len(public) != ed25519.PublicKeySize || out[key.ID] != "" {
			return nil, failure("malformed_response", false, "provider signing key invalid")
		}
		out[key.ID] = key.PublicKey
	}
	return out, nil
}
func (c *Client) install(t tokenResponse, private ed25519.PrivateKey, started time.Time) error {
	if t.TokenType != "Bearer" || t.TenantID != c.metadata.TenantID || !opaqueID(t.AccountID) || !opaqueID(t.DeviceID) || !opaqueID(t.SigningKeyID) || t.AccessToken == "" || t.RefreshToken == "" || len(t.AccessToken) > 8192 || len(t.RefreshToken) > 8192 || strings.ContainsAny(t.AccessToken+t.RefreshToken, "\r\n") || t.ExpiresIn < 1 || t.ExpiresIn > 86400 || t.ServerTime.IsZero() || len(private) != ed25519.PrivateKeySize {
		return failure("malformed_response", false, "provider did not bind a complete device identity")
	}
	if c.metadata.DeviceID != "" && (t.DeviceID != c.metadata.DeviceID || t.AccountID != c.metadata.AccountID || t.SigningKeyID != c.metadata.SigningKeyID) {
		return failure("identity_mismatch", false, "provider changed the selected device binding")
	}
	keys, e := trustedKeys(t.ServerSigningKeys)
	if e != nil {
		return e
	}
	ttl := time.Duration(t.ExpiresIn) * time.Second
	safety := min(30*time.Second, ttl/10)
	local := time.Now().Add(ttl - time.Since(started) - safety)
	if !local.After(time.Now()) {
		return failure("expired_token", false, "provider token expired before it could be stored")
	}
	next := Metadata{SchemaVersion: 1, Provider: c.provider, TenantID: t.TenantID, AccountID: t.AccountID, DeviceID: t.DeviceID, Scopes: strings.Fields(t.Scope), ExpiresAt: t.ServerTime.Add(ttl), LocalExpiresAt: local.UTC(), SigningKeyID: t.SigningKeyID, TrustedKeys: keys}
	raw, e := json.Marshal(credentials{AccessToken: t.AccessToken, RefreshToken: t.RefreshToken, PrivateKey: private, InstalledMetadata: &next})
	if e != nil {
		return e
	}
	if e = c.store.Put(credentialKey(c.provider, next.TenantID, next.DeviceID), raw); e != nil {
		return storeFailure(e)
	}
	if e = saveMetadata(next); e != nil {
		return failure("metadata_unavailable", false, "credentials are held by the OS store; repair the exact non-secret device metadata")
	}
	c.metadata = next
	return nil
}
func (c *Client) credentials() (credentials, error) {
	var secret credentials
	raw, e := c.store.Get(credentialKey(c.provider, c.metadata.TenantID, c.metadata.DeviceID))
	if e != nil {
		return secret, storeFailure(e)
	}
	if decodeClosed(raw, &secret) != nil || secret.AccessToken == "" || secret.RefreshToken == "" || len(secret.PrivateKey) != ed25519.PrivateKeySize {
		return credentials{}, failure("credential_store_refused", false, "repair the selected native credential record")
	}
	return secret, nil
}
func (c *Client) session(secret credentials, m Metadata) (Session, error) {
	keys := map[string]ed25519.PublicKey{}
	for id, text := range m.TrustedKeys {
		key, e := base64.StdEncoding.DecodeString(text)
		if e != nil || len(key) != ed25519.PublicKeySize {
			return Session{}, failure("metadata_refused", false, "repair invalid public signing key metadata")
		}
		keys[id] = ed25519.PublicKey(key)
	}
	return Session{Provider: c.provider, TenantID: m.TenantID, AccountID: m.AccountID, DeviceID: m.DeviceID, SigningKeyID: m.SigningKeyID, Scopes: append([]string(nil), m.Scopes...), ExpiresAt: m.ExpiresAt, TrustedKeys: keys, access: secret.AccessToken, private: append(ed25519.PrivateKey(nil), secret.PrivateKey...)}, nil
}

// LocalSession supports offline signing of queued metadata only. It carries no
// bearer credential and does not establish current remote authority.
func (c *Client) LocalSession() (Session, error) {
	secret, e := c.credentials()
	if e != nil {
		return Session{}, e
	}
	secret.AccessToken = ""
	return c.session(secret, c.metadata)
}
func (c *Client) Authenticate(ctx context.Context) (Session, error) {
	secret, e := c.credentials()
	if e != nil {
		return Session{}, e
	}
	if secret.InstalledMetadata != nil {
		m := *secret.InstalledMetadata
		if m.Provider != c.provider || m.TenantID != c.metadata.TenantID || m.DeviceID != c.metadata.DeviceID || m.AccountID != c.metadata.AccountID || m.SigningKeyID != c.metadata.SigningKeyID {
			return Session{}, failure("identity_mismatch", false, "repair the exact native identity binding")
		}
		if e = saveMetadata(m); e != nil {
			return Session{}, failure("metadata_unavailable", false, "repair the exact non-secret device metadata before refreshing")
		}
		c.metadata = m
	}
	if secret.PendingRefreshID != "" || !time.Now().Before(c.metadata.LocalExpiresAt) {
		id := secret.PendingRefreshID
		if id == "" {
			id = requestID()
		}
		if id == "" {
			return Session{}, failure("entropy_unavailable", false, "repair the system random source")
		}
		started := secret.PendingRefreshAt
		if started.IsZero() {
			started = time.Now().UTC()
			secret.PendingRefreshID = id
			secret.PendingRefreshAt = started
			raw, err := json.Marshal(secret)
			if err != nil {
				return Session{}, failure("invalid_request", false, "repair native credential state")
			}
			if err = c.store.Put(credentialKey(c.provider, c.metadata.TenantID, c.metadata.DeviceID), raw); err != nil {
				return Session{}, storeFailure(err)
			}
		}
		var token tokenResponse
		e = c.request(ctx, http.MethodPost, "/v1/auth/token", struct {
			GrantType    string `json:"grant_type"`
			RefreshToken string `json:"refresh_token"`
			RequestID    string `json:"request_id"`
		}{"refresh_token", secret.RefreshToken, id}, "", &token)
		if e != nil {
			return Session{}, e
		}
		if e = c.install(token, secret.PrivateKey, started); e != nil {
			return Session{}, e
		}
		secret, e = c.credentials()
		if e != nil {
			return Session{}, e
		}
	}
	m, e := c.Whoami(ctx)
	if e != nil {
		return Session{}, e
	}
	return c.session(secret, m)
}
func (c *Client) Whoami(ctx context.Context) (Metadata, error) {
	secret, e := c.credentials()
	if e != nil {
		return Metadata{}, e
	}
	var remote whoResponse
	started := time.Now()
	if e = c.request(ctx, http.MethodGet, "/v1/auth/whoami", nil, "Bearer "+secret.AccessToken, &remote); e != nil {
		return Metadata{}, e
	}
	if remote.TenantID != c.metadata.TenantID || remote.AccountID != c.metadata.AccountID || remote.DeviceID != c.metadata.DeviceID || remote.SigningKeyID != c.metadata.SigningKeyID || remote.ServerTime.IsZero() {
		return Metadata{}, failure("identity_mismatch", false, "provider did not confirm the selected device identity")
	}
	ttl := remote.ExpiresAt.Sub(remote.ServerTime)
	if ttl <= 0 || ttl > 24*time.Hour {
		return Metadata{}, failure("expired_token", false, "run auth login for the selected tenant/device")
	}
	keys, e := trustedKeys(remote.ServerSigningKeys)
	if e != nil {
		return Metadata{}, e
	}
	m := c.metadata
	m.ExpiresAt = remote.ExpiresAt
	m.LocalExpiresAt = time.Now().Add(ttl - time.Since(started) - min(30*time.Second, ttl/10))
	m.Scopes = strings.Fields(remote.Scope)
	m.TrustedKeys = keys
	return m, nil
}

// Login exposes only the human verification URI/code, never the device code.
type Challenge struct {
	VerificationURI string `json:"verification_uri"`
	UserCode        string `json:"user_code"`
	ExpiresIn       int64  `json:"expires_in"`
	Interval        int64  `json:"interval"`
}

func Login(ctx context.Context, provider, tenant, name string, show func(Challenge) error) (Metadata, error) {
	p, e := providerBase(provider)
	if e != nil {
		return Metadata{}, e
	}
	if !opaqueID(tenant) || len(name) < 1 || len(name) > 128 || show == nil {
		return Metadata{}, failure("invalid_request", false, "select an exact tenant and bounded device name")
	}
	store, e := credentialstore.Open()
	if e != nil {
		return Metadata{}, storeFailure(e)
	}
	public, private, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		return Metadata{}, failure("entropy_unavailable", false, "repair system random source")
	}
	c := &Client{provider: p, metadata: Metadata{TenantID: tenant}, store: store, http: httpClient()}
	id := requestID()
	if id == "" {
		return Metadata{}, failure("entropy_unavailable", false, "repair system random source")
	}
	var begun struct {
		DeviceCode      string    `json:"device_code"`
		UserCode        string    `json:"user_code"`
		VerificationURI string    `json:"verification_uri"`
		ExpiresIn       int64     `json:"expires_in"`
		Interval        int64     `json:"interval"`
		ServerTime      time.Time `json:"server_time"`
	}
	started := time.Now()
	e = c.request(ctx, http.MethodPost, "/v1/auth/device", struct {
		TenantID   string `json:"tenant_id"`
		DeviceName string `json:"device_name"`
		Platform   string `json:"platform"`
		PublicKey  string `json:"public_key"`
		RequestID  string `json:"request_id"`
	}{tenant, name, runtime.GOOS, base64.StdEncoding.EncodeToString(public), id}, "", &begun)
	if e != nil {
		return Metadata{}, e
	}
	uri, e := url.Parse(begun.VerificationURI)
	if e != nil || uri.Hostname() == "" || uri.User != nil || uri.Fragment != "" || (uri.Scheme != "https" && !(uri.Scheme == "http" && (uri.Hostname() == "localhost" || uri.Hostname() == "127.0.0.1" || uri.Hostname() == "::1"))) || begun.DeviceCode == "" || len(begun.DeviceCode) > 4096 || begun.UserCode == "" || len(begun.UserCode) > 128 || begun.DeviceCode == begun.UserCode || strings.Contains(begun.VerificationURI, begun.DeviceCode) || begun.ExpiresIn < 1 || begun.ExpiresIn > 900 || begun.Interval < 1 || begun.Interval > 60 || begun.ServerTime.IsZero() {
		return Metadata{}, failure("malformed_response", false, "provider returned an unsafe or unsupported device challenge")
	}
	if e = show(Challenge{begun.VerificationURI, begun.UserCode, begun.ExpiresIn, begun.Interval}); e != nil {
		return Metadata{}, e
	}
	bounded, cancel := context.WithDeadline(ctx, started.Add(time.Duration(begun.ExpiresIn)*time.Second))
	defer cancel()
	interval := time.Duration(begun.Interval) * time.Second
	pollID := requestID()
	if pollID == "" {
		return Metadata{}, failure("entropy_unavailable", false, "repair system random source")
	}
	for {
		timer := time.NewTimer(interval)
		select {
		case <-bounded.Done():
			timer.Stop()
			if ctx.Err() != nil {
				return Metadata{}, failure("cancelled", false, "device authorization cancelled; start a fresh login when ready")
			}
			return Metadata{}, failure("expired_token", false, "device authorization expired or was cancelled; start a fresh login")
		case <-timer.C:
		}
		var token tokenResponse
		sent := time.Now()
		e = c.request(bounded, http.MethodPost, "/v1/auth/token", struct {
			GrantType  string `json:"grant_type"`
			DeviceCode string `json:"device_code"`
			RequestID  string `json:"request_id"`
		}{"urn:ietf:params:oauth:grant-type:device_code", begun.DeviceCode, pollID}, "", &token)
		if e != nil {
			var state *Error
			if errors.As(e, &state) && state.Code == "authorization_pending" {
				continue
			}
			if errors.As(e, &state) && state.Code == "slow_down" {
				interval += 5 * time.Second
				continue
			}
			return Metadata{}, e
		}
		if e = c.install(token, private, sent); e != nil {
			return Metadata{}, e
		}
		return c.metadata, nil
	}
}

type LogoutResult struct {
	TenantID, DeviceID, State   string
	LocalRemoved, RemoteRevoked bool
	Next                        string
}

func (c *Client) Logout(ctx context.Context) (LogoutResult, error) {
	out := LogoutResult{TenantID: c.metadata.TenantID, DeviceID: c.metadata.DeviceID, State: "logged_out"}
	secret, e := c.credentials()
	var remote error
	if e == nil {
		remote = c.request(ctx, http.MethodPost, "/v1/auth/revoke", struct {
			Token         string `json:"token"`
			TokenTypeHint string `json:"token_type_hint"`
			RequestID     string `json:"request_id"`
		}{secret.RefreshToken, "refresh_token", requestID()}, "Bearer "+secret.AccessToken, nil)
		out.RemoteRevoked = remote == nil
	} else {
		remote = e
	}
	local := c.store.Delete(credentialKey(c.provider, c.metadata.TenantID, c.metadata.DeviceID))
	if local != nil {
		out.State = "local_removal_refused"
		out.Next = "remove the exact credential through the native OS store"
		return out, storeFailure(local)
	}
	out.LocalRemoved = true
	if e = removeMetadata(c.metadata); e != nil {
		return out, failure("metadata_unavailable", false, "native credential removed; repair exact non-secret metadata")
	}
	if remote != nil {
		out.State = "remote_revoke_unknown"
		out.Next = "local credential removed; revoke this exact device through the provider when reachable"
		return out, remote
	}
	return out, nil
}
