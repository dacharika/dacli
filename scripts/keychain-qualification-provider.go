// Command keychain-qualification-provider is a loopback-only synthetic provider
// used to qualify the extracted release binary through its existing auth commands.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"time"
)

const (
	tenantID    = "qualification-tenant"
	accountID   = "qualification-account"
	deviceID    = "qualification-device"
	signingKey  = "qualification-key"
	accessToken = "dacli-synthetic-access-marker"
	refresh     = "dacli-synthetic-refresh-marker"
)

type key struct {
	ID        string `json:"key_id"`
	PublicKey string `json:"public_key"`
}

type deviceRequest struct {
	TenantID string `json:"tenant_id"`
}

type tokenRequest struct {
	GrantType string `json:"grant_type"`
}

func main() {
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		log.Fatal("generate synthetic provider signing key")
	}
	keys := []key{{ID: signingKey, PublicKey: base64.StdEncoding.EncodeToString(public)}}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal("listen on loopback")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/auth/device", func(w http.ResponseWriter, r *http.Request) {
		var request deviceRequest
		if json.NewDecoder(r.Body).Decode(&request) != nil || request.TenantID != tenantID {
			http.Error(w, "invalid synthetic tenant", http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{
			"device_code": "fixture-device-code", "user_code": "FIXTURE-CODE",
			"verification_uri": "http://localhost/fixture", "expires_in": 120,
			"interval": 1, "server_time": time.Now().UTC(),
		})
	})
	mux.HandleFunc("POST /v1/auth/token", func(w http.ResponseWriter, r *http.Request) {
		var request tokenRequest
		if json.NewDecoder(r.Body).Decode(&request) != nil || request.GrantType != "urn:ietf:params:oauth:grant-type:device_code" {
			writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
			return
		}
		writeJSON(w, map[string]any{
			"token_type": "Bearer", "access_token": accessToken, "refresh_token": refresh,
			"expires_in": 3600, "server_time": time.Now().UTC(), "tenant_id": tenantID,
			"account_id": accountID, "device_id": deviceID, "scope": "qualification",
			"signing_key_id": signingKey, "server_signing_keys": keys,
		})
	})
	mux.HandleFunc("GET /v1/auth/whoami", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+accessToken {
			writeJSONStatus(w, http.StatusUnauthorized, map[string]string{"error": "invalid_grant"})
			return
		}
		now := time.Now().UTC()
		writeJSON(w, map[string]any{
			"tenant_id": tenantID, "account_id": accountID, "device_id": deviceID,
			"scope": "qualification", "expires_at": now.Add(time.Hour),
			"server_time": now, "signing_key_id": signingKey, "server_signing_keys": keys,
		})
	})
	mux.HandleFunc("POST /v1/auth/revoke", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+accessToken {
			http.Error(w, "invalid synthetic authorization", http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 2 * time.Second}
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Fatal("synthetic provider stopped")
		}
	}()
	fmt.Printf("http://%s\n", listener.Addr())
	select {}
}

func writeJSON(w http.ResponseWriter, value any) {
	writeJSONStatus(w, http.StatusOK, value)
}

func writeJSONStatus(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Print("write synthetic provider response failed")
	}
}
