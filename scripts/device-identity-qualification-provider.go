//go:build ignore

// Command device-identity-qualification-provider is a disposable loopback-only
// provider for exercising an extracted dacli artifact against native Keychain.
// It serves synthetic identities only and exposes its secret state solely on
// an authenticated loopback control route so the harness can scan outputs.
package main

import (
	"bufio"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"time"
)

const signingKeyID = "qualification-signing-key"

type identity struct {
	tenant     string
	account    string
	device     string
	deviceCode string
	access     string
	refresh    string
	allSecrets []string
	oldAccess  []string
	revoked    bool
	refreshes  int
	revokes    int
	oldDenied  int
	lastExpiry time.Time
}

type provider struct {
	mu          sync.Mutex
	controlKey  []byte
	publicKey   string
	identities  map[string]*identity
	deviceCodes map[string]*identity
}

func main() {
	control, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil || len(control) < 33 || len(control) > 257 {
		fmt.Fprintln(os.Stderr, "qualification provider requires a private control key on stdin")
		os.Exit(2)
	}
	controlKey := []byte(strings.TrimSuffix(control, "\n"))
	public, err := ed25519PublicKey()
	if err != nil {
		fmt.Fprintln(os.Stderr, "qualification provider could not initialize signing key")
		os.Exit(1)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintln(os.Stderr, "qualification provider could not bind loopback")
		os.Exit(1)
	}
	p := &provider{
		controlKey: controlKey,
		publicKey:  public,
		identities: map[string]*identity{}, deviceCodes: map[string]*identity{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/auth/device", p.begin)
	mux.HandleFunc("POST /v1/auth/token", p.token)
	mux.HandleFunc("GET /v1/auth/whoami", p.whoami)
	mux.HandleFunc("POST /v1/auth/revoke", p.revoke)
	mux.HandleFunc("GET /__qualification/state", p.control(p.state))
	mux.HandleFunc("POST /__qualification/revoke-tenant", p.control(p.revokeTenant))
	mux.HandleFunc("POST /__qualification/probe-old-access", p.control(p.probeOldAccess))
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 10 * time.Second}
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			fmt.Fprintln(os.Stderr, "qualification provider stopped unexpectedly")
			os.Exit(1)
		}
	}()
	fmt.Printf("http://%s\n", listener.Addr())
	select {}
}

// Keep generated private signing material inside this process only.
func ed25519PublicKey() (string, error) {
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(public), nil
}

func (p *provider) control(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		got := []byte(r.Header.Get("X-Qualification-Control"))
		if len(got) != len(p.controlKey) || subtle.ConstantTimeCompare(got, p.controlKey) != 1 {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		next(w, r)
	}
}

func (p *provider) begin(w http.ResponseWriter, r *http.Request) {
	var request struct {
		TenantID string `json:"tenant_id"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&request) != nil || !validTenant(request.TenantID) {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.identities[request.TenantID] != nil {
		writeError(w, http.StatusConflict, "invalid_request")
		return
	}
	deviceCode, err := randomSecret("device-code-")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "temporarily_unavailable")
		return
	}
	access, err := randomSecret("access-")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "temporarily_unavailable")
		return
	}
	refresh, err := randomSecret("refresh-")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "temporarily_unavailable")
		return
	}
	i := &identity{tenant: request.TenantID, account: "account-" + request.TenantID, device: "device-" + request.TenantID, deviceCode: deviceCode, access: access, refresh: refresh, allSecrets: []string{deviceCode, access, refresh}}
	p.identities[i.tenant] = i
	p.deviceCodes[deviceCode] = i
	writeJSON(w, map[string]any{
		"device_code": deviceCode, "user_code": "QUALIFY-" + strings.ToUpper(request.TenantID),
		"verification_uri": "http://127.0.0.1/qualification", "expires_in": 120,
		"interval": 1, "server_time": time.Now().UTC(),
	})
}

func (p *provider) token(w http.ResponseWriter, r *http.Request) {
	var request struct {
		GrantType    string `json:"grant_type"`
		DeviceCode   string `json:"device_code"`
		RefreshToken string `json:"refresh_token"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&request) != nil {
		writeError(w, http.StatusBadRequest, "invalid_grant")
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	var i *identity
	switch request.GrantType {
	case "urn:ietf:params:oauth:grant-type:device_code":
		i = p.deviceCodes[request.DeviceCode]
		if i == nil || i.revoked || i.deviceCode != request.DeviceCode {
			writeError(w, http.StatusBadRequest, "invalid_grant")
			return
		}
		i.deviceCode = ""
	case "refresh_token":
		for _, candidate := range p.identities {
			if candidate.refresh == request.RefreshToken {
				i = candidate
				break
			}
		}
		if i == nil || i.revoked {
			writeError(w, http.StatusUnauthorized, "invalid_grant")
			return
		}
		newAccess, err := randomSecret("access-")
		if err != nil {
			writeError(w, http.StatusInternalServerError, "temporarily_unavailable")
			return
		}
		newRefresh, err := randomSecret("refresh-")
		if err != nil {
			writeError(w, http.StatusInternalServerError, "temporarily_unavailable")
			return
		}
		i.oldAccess = append(i.oldAccess, i.access)
		i.access, i.refresh = newAccess, newRefresh
		i.allSecrets = append(i.allSecrets, newAccess, newRefresh)
		i.refreshes++
	case "qualification_disable_auth":
		writeError(w, http.StatusBadRequest, "invalid_grant")
		return
	default:
		writeError(w, http.StatusBadRequest, "invalid_grant")
		return
	}
	now := time.Now().UTC()
	// Short expiry makes the artifact's refresh branch deterministic without
	// waiting for production-duration credentials.
	i.lastExpiry = now.Add(2 * time.Second)
	writeJSON(w, p.tokenDocument(i, now, 2))
}

func (p *provider) whoami(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, i := range p.identities {
		if r.Header.Get("Authorization") == "Bearer "+i.access {
			if i.revoked || time.Now().After(i.lastExpiry) {
				writeError(w, http.StatusUnauthorized, "invalid_grant")
				return
			}
			now := time.Now().UTC()
			writeJSON(w, map[string]any{
				"tenant_id": i.tenant, "account_id": i.account, "device_id": i.device,
				"scope": "qualification", "expires_at": i.lastExpiry, "server_time": now,
				"signing_key_id": signingKeyID, "server_signing_keys": []map[string]string{{"key_id": signingKeyID, "public_key": p.publicKey}},
			})
			return
		}
		for _, old := range i.oldAccess {
			if r.Header.Get("Authorization") == "Bearer "+old {
				i.oldDenied++
				writeError(w, http.StatusUnauthorized, "invalid_grant")
				return
			}
		}
	}
	writeError(w, http.StatusUnauthorized, "invalid_grant")
}

func (p *provider) revoke(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Token string `json:"token"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&request) != nil {
		writeError(w, http.StatusBadRequest, "invalid_grant")
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, i := range p.identities {
		if request.Token == i.refresh && r.Header.Get("Authorization") == "Bearer "+i.access {
			i.revoked = true
			i.revokes++
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	writeError(w, http.StatusUnauthorized, "invalid_grant")
}

func (p *provider) revokeTenant(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Tenant string `json:"tenant"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&request) != nil {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	i := p.identities[request.Tenant]
	if i == nil {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	i.revoked = true
	writeJSON(w, map[string]bool{"revoked": true})
}

func (p *provider) probeOldAccess(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Tenant string `json:"tenant"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&request) != nil {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	p.mu.Lock()
	i := p.identities[request.Tenant]
	if i == nil || len(i.oldAccess) == 0 {
		p.mu.Unlock()
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	oldAccess := append([]string(nil), i.oldAccess...)
	p.mu.Unlock()
	// Exercise the actual whoami handler with each superseded bearer held only
	// in process memory; neither token crosses the control API.
	for _, old := range oldAccess {
		probe := httptest.NewRequest(http.MethodGet, "/v1/auth/whoami", nil)
		probe.Header.Set("Authorization", "Bearer "+old)
		response := httptest.NewRecorder()
		p.whoami(response, probe)
		if response.Code != http.StatusUnauthorized {
			writeJSON(w, map[string]bool{"rejected": false})
			return
		}
	}
	writeJSON(w, map[string]bool{"rejected": true})
}

func (p *provider) state(w http.ResponseWriter, _ *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := map[string]any{}
	for tenant, i := range p.identities {
		out[tenant] = map[string]any{
			"account": i.account, "device": i.device, "refreshes": i.refreshes,
			"revokes": i.revokes, "revoked": i.revoked, "old_access_rejected": i.oldDenied,
			"secrets_for_in_memory_scan": append([]string(nil), i.allSecrets...),
		}
	}
	writeJSON(w, out)
}

func (p *provider) tokenDocument(i *identity, now time.Time, expires int64) map[string]any {
	return map[string]any{
		"token_type": "Bearer", "access_token": i.access, "refresh_token": i.refresh,
		"expires_in": expires, "server_time": now, "tenant_id": i.tenant,
		"account_id": i.account, "device_id": i.device, "scope": "qualification",
		"signing_key_id":      signingKeyID,
		"server_signing_keys": []map[string]string{{"key_id": signingKeyID, "public_key": p.publicKey}},
	}
}

func validTenant(value string) bool {
	if len(value) < 1 || len(value) > 64 {
		return false
	}
	for _, char := range value {
		if !(char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '-') {
			return false
		}
	}
	return true
}

func randomSecret(prefix string) (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(raw), nil
}

func writeError(w http.ResponseWriter, status int, code string) {
	writeJSONStatus(w, status, map[string]string{"error": code})
}

func writeJSON(w http.ResponseWriter, value any) { writeJSONStatus(w, http.StatusOK, value) }

func writeJSONStatus(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		fmt.Fprintln(os.Stderr, "qualification provider response write failed")
	}
}
