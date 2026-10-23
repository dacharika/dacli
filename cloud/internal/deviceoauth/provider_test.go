package deviceoauth

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func providerForTestServer(t *testing.T, server *httptest.Server) *HTTPProvider {
	t.Helper()
	provider, err := NewProvider(ProviderConfig{
		Issuer:       "https://issuer.example",
		DeviceURL:    server.URL + "/device",
		TokenURL:     server.URL + "/token",
		IdentityURL:  server.URL + "/identity",
		ClientID:     "dacli-client",
		ClientSecret: "synthetic-client-secret",
		Scope:        "openid profile",
	})
	if err != nil {
		t.Fatal(err)
	}
	provider.client.Transport = server.Client().Transport
	return provider
}

func newTLSServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.Listener = listener
	server.StartTLS()
	t.Cleanup(server.Close)
	return server
}

func TestHTTPProviderUsesDeviceGrantFormAndIssuerBoundUserInfo(t *testing.T) {
	var deviceSeen, tokenSeen, identitySeen bool
	server := newTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/device":
			deviceSeen = true
			values, err := readForm(r)
			if err != nil || r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" || values.Get("client_id") != "dacli-client" || values.Get("scope") != "openid profile" {
				t.Errorf("device grant request method=%s type=%q values=%v err=%v", r.Method, r.Header.Get("Content-Type"), values, err)
			}
			_, _ = io.WriteString(w, `{"device_code":"upstream-device","user_code":"USER-123","verification_uri":"https://accounts.example/device","expires_in":600,"interval":2}`)
		case "/token":
			tokenSeen = true
			values, err := readForm(r)
			if err != nil || r.Method != http.MethodPost || values.Get("client_id") != "dacli-client" || values.Get("client_secret") != "synthetic-client-secret" || values.Get("grant_type") != "urn:ietf:params:oauth:grant-type:device_code" || values.Get("device_code") != "upstream-device" {
				t.Errorf("token request method=%s values=%v err=%v", r.Method, values, err)
			}
			_, _ = io.WriteString(w, `{"access_token":"synthetic-provider-access","token_type":"Bearer"}`)
		case "/identity":
			identitySeen = true
			if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer synthetic-provider-access" {
				t.Errorf("userinfo request method=%s authorization=%q", r.Method, r.Header.Get("Authorization"))
			}
			_, _ = io.WriteString(w, `{"sub":"subject-123","iss":"https://issuer.example"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	provider := providerForTestServer(t, server)

	challenge, err := provider.Begin(context.Background())
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if challenge.DeviceCode != "upstream-device" || challenge.UserCode != "USER-123" || challenge.Interval != 2 {
		t.Fatalf("challenge = %+v", challenge)
	}
	subject, err := provider.Poll(context.Background(), challenge.DeviceCode)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if subject != "subject-123" || !deviceSeen || !tokenSeen || !identitySeen {
		t.Fatalf("subject=%q calls device=%v token=%v userinfo=%v", subject, deviceSeen, tokenSeen, identitySeen)
	}
}

func TestHTTPProviderPreservesDeviceGrantPollingErrors(t *testing.T) {
	for _, code := range []string{"authorization_pending", "slow_down", "access_denied", "expired_token", "invalid_grant"} {
		t.Run(code, func(t *testing.T) {
			server := newTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/token" {
					http.NotFound(w, r)
					return
				}
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"error":"`+code+`"}`)
			}))
			_, err := providerForTestServer(t, server).Poll(context.Background(), "upstream-device")
			var oauth OAuthError
			if !errors.As(err, &oauth) || oauth != OAuthError(code) {
				t.Fatalf("Poll error=%v, want OAuth %q", err, code)
			}
		})
	}
}

func TestHTTPProviderRejectsWrongIssuerAndUnsafeChallenges(t *testing.T) {
	t.Run("wrong userinfo issuer", func(t *testing.T) {
		server := newTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/token":
				_, _ = io.WriteString(w, `{"access_token":"provider-access","token_type":"Bearer"}`)
			case "/identity":
				_, _ = io.WriteString(w, `{"sub":"subject-123","iss":"https://attacker.example"}`)
			default:
				http.NotFound(w, r)
			}
		}))
		if _, err := providerForTestServer(t, server).Poll(context.Background(), "upstream-device"); !errors.Is(err, ErrDenied) {
			t.Fatalf("wrong issuer accepted: %v", err)
		}
	})
	for _, tc := range []struct {
		name string
		body string
	}{
		{"device code equals user code", `{"device_code":"same","user_code":"same","verification_uri":"https://accounts.example/device","expires_in":600,"interval":2}`},
		{"device code inside user code", `{"device_code":"private-code","user_code":"before-private-code-after","verification_uri":"https://accounts.example/device","expires_in":600,"interval":2}`},
		{"device code leaked in uri", `{"device_code":"secret-device-code","user_code":"USER-123","verification_uri":"https://accounts.example/device?code=secret-device-code","expires_in":600,"interval":2}`},
		{"verification uri has no host", `{"device_code":"secret-device-code","user_code":"USER-123","verification_uri":"https:device","expires_in":600,"interval":2}`},
		{"verification uri fragment", `{"device_code":"secret-device-code","user_code":"USER-123","verification_uri":"https://accounts.example/device#fragment","expires_in":600,"interval":2}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := newTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, tc.body) }))
			if _, err := providerForTestServer(t, server).Begin(context.Background()); !errors.Is(err, ErrDenied) {
				t.Fatalf("unsafe challenge accepted: %v", err)
			}
		})
	}
}

func TestHTTPProviderDefaultsZeroPollingInterval(t *testing.T) {
	server := newTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"device_code":"upstream-device","user_code":"USER-123","verification_uri":"https://accounts.example/device","expires_in":600,"interval":0}`)
	}))
	challenge, err := providerForTestServer(t, server).Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if challenge.Interval != 5 {
		t.Fatalf("zero interval defaulted to %d, want 5", challenge.Interval)
	}
}

func TestHTTPProviderRefusesRedirectMalformedOversizedAndOfflineResponses(t *testing.T) {
	t.Run("redirect", func(t *testing.T) {
		var destinationCalls int
		destination := newTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { destinationCalls++ }))
		origin := newTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, destination.URL+"/steal", http.StatusFound)
		}))
		_, err := providerForTestServer(t, origin).Poll(context.Background(), "upstream-device")
		var oauth OAuthError
		if !errors.As(err, &oauth) || oauth != "temporarily_unavailable" || destinationCalls != 0 {
			t.Fatalf("redirect result=%v destination calls=%d", err, destinationCalls)
		}
	})
	for _, tc := range []struct {
		name string
		body string
	}{
		{"malformed", `{"access_token":`},
		{"oversized", strings.Repeat("x", 65537)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := newTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, tc.body) }))
			_, err := providerForTestServer(t, server).Poll(context.Background(), "upstream-device")
			var oauth OAuthError
			if !errors.As(err, &oauth) || oauth != "temporarily_unavailable" {
				t.Fatalf("Poll error=%v, want temporarily_unavailable", err)
			}
		})
	}
	t.Run("offline", func(t *testing.T) {
		server := newTLSServer(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		provider := providerForTestServer(t, server)
		server.Close()
		_, err := provider.Poll(context.Background(), "upstream-device")
		var oauth OAuthError
		if !errors.As(err, &oauth) || oauth != "temporarily_unavailable" {
			t.Fatalf("offline Poll error=%v", err)
		}
	})
}

func readForm(r *http.Request) (url.Values, error) {
	if err := r.ParseForm(); err != nil {
		return nil, err
	}
	return r.PostForm, nil
}
