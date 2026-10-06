// Package deviceoauth translates owner-configured OAuth device authorization into
// DB-backed sessions. Provider tokens never become local bearer credentials.
package deviceoauth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var ErrDenied = errors.New("device authorization denied")

type OAuthError string

func (e OAuthError) Error() string { return string(e) }

type ProviderConfig struct{ Issuer, DeviceURL, TokenURL, IdentityURL, ClientID, ClientSecret, Scope string }

func (ProviderConfig) String() string   { return "ProviderConfig" }
func (ProviderConfig) GoString() string { return "ProviderConfig" }

type Provider interface {
	Begin(context.Context) (Challenge, error)
	Poll(context.Context, string) (string, error)
}
type Challenge struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int64  `json:"expires_in"`
	Interval        int64  `json:"interval"`
}
type HTTPProvider struct {
	config ProviderConfig
	client *http.Client
}

func NewProvider(c ProviderConfig) (*HTTPProvider, error) {
	for _, raw := range []string{c.Issuer, c.DeviceURL, c.TokenURL, c.IdentityURL} {
		u, e := url.Parse(raw)
		if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
			return nil, ErrDenied
		}
	}
	if c.ClientID == "" || c.Scope == "" {
		return nil, ErrDenied
	}
	return &HTTPProvider{c, &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return ErrDenied }}}, nil
}
func (p *HTTPProvider) request(ctx context.Context, endpoint string, values url.Values, token string, out any) error {
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	method := http.MethodPost
	var body io.Reader = strings.NewReader(values.Encode())
	if token != "" {
		method = http.MethodGet
		body = nil
	}
	req, e := http.NewRequestWithContext(bounded, method, endpoint, body)
	if e != nil {
		return ErrDenied
	}
	req.Header.Set("Accept", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	} else {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	response, e := p.client.Do(req)
	if e != nil {
		return OAuthError("temporarily_unavailable")
	}
	defer response.Body.Close()
	raw, e := io.ReadAll(io.LimitReader(response.Body, 65537))
	if e != nil || len(raw) > 65536 {
		return OAuthError("temporarily_unavailable")
	}
	var failure struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(raw, &failure)
	if failure.Error != "" {
		switch failure.Error {
		case "authorization_pending", "slow_down", "access_denied", "expired_token", "invalid_grant":
			return OAuthError(failure.Error)
		default:
			return OAuthError("temporarily_unavailable")
		}
	}
	if response.StatusCode != http.StatusOK || json.Unmarshal(raw, out) != nil {
		return OAuthError("temporarily_unavailable")
	}
	return nil
}
func (p *HTTPProvider) Begin(ctx context.Context) (Challenge, error) {
	var c Challenge
	e := p.request(ctx, p.config.DeviceURL, url.Values{"client_id": {p.config.ClientID}, "scope": {p.config.Scope}}, "", &c)
	u, ue := url.Parse(c.VerificationURI)
	if e != nil {
		return c, e
	}
	if c.DeviceCode == "" || c.UserCode == "" || c.ExpiresIn < 1 || c.ExpiresIn > 1800 || c.Interval < 0 || c.Interval > 60 || ue != nil || u.Scheme != "https" || u.User != nil {
		return Challenge{}, ErrDenied
	}
	if c.Interval == 0 {
		c.Interval = 5
	}
	return c, nil
}
func (p *HTTPProvider) Poll(ctx context.Context, code string) (string, error) {
	var token struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
	}
	e := p.request(ctx, p.config.TokenURL, url.Values{"client_id": {p.config.ClientID}, "client_secret": {p.config.ClientSecret}, "grant_type": {"urn:ietf:params:oauth:grant-type:device_code"}, "device_code": {code}}, "", &token)
	if e != nil {
		return "", e
	}
	if token.AccessToken == "" || !strings.EqualFold(token.TokenType, "Bearer") || strings.ContainsAny(token.AccessToken, "\r\n") {
		return "", ErrDenied
	}
	var identity struct {
		Subject string `json:"sub"`
		Issuer  string `json:"iss"`
	}
	if e = p.request(ctx, p.config.IdentityURL, nil, token.AccessToken, &identity); e != nil {
		return "", e
	}
	// UserInfo is fetched only from the owner's fixed authenticated provider URL.
	if identity.Subject == "" || len(identity.Subject) > 256 || (identity.Issuer != "" && identity.Issuer != p.config.Issuer) {
		return "", ErrDenied
	}
	return identity.Subject, nil
}
