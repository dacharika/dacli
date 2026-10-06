package deviceoauth

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/mlnomadpy/dacli/cloud/internal/devicesession"
	"github.com/mlnomadpy/dacli/cloud/internal/tenant"
	"strings"
	"time"
)

type SubjectBinding struct{ Tenant, Subject, Account string }
type Keys interface {
	Approved(tenant.Scope, []byte) (string, map[string]string, error)
}
type SessionChange struct {
	Device                    tenant.Device
	Session                   devicesession.Record
	RevokeSessionIDs          []devicesession.ID
	CorrelationID             string
	ExpectedMembershipVersion tenant.Version
}
type Store interface {
	CreateAuth(context.Context, tenant.Scope, string, []byte, int64) ([]byte, error)
	UpdateAuth(context.Context, tenant.Scope, string, func([]byte) ([]byte, *SessionChange, error)) error
	CurrentMembership(context.Context, tenant.Scope, tenant.AccountID) (tenant.Membership, error)
}
type Service struct {
	provider Provider
	store    Store
	manager  *devicesession.Manager
	cipher   cipher.AEAD
	subjects []SubjectBinding
	keys     Keys
}
type BeginRequest struct {
	TenantID   string `json:"tenant_id"`
	DeviceName string `json:"device_name"`
	Platform   string `json:"platform"`
	PublicKey  string `json:"public_key"`
	RequestID  string `json:"request_id"`
}
type BeginResponse struct {
	DeviceCode      string    `json:"device_code"`
	UserCode        string    `json:"user_code"`
	VerificationURI string    `json:"verification_uri"`
	ExpiresIn       int64     `json:"expires_in"`
	Interval        int64     `json:"interval"`
	ServerTime      time.Time `json:"server_time"`
}
type TokenRequest struct {
	GrantType    string `json:"grant_type"`
	DeviceCode   string `json:"device_code,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
	RequestID    string `json:"request_id"`
}
type SigningKey struct {
	KeyID     string `json:"key_id"`
	PublicKey string `json:"public_key"`
}
type TokenResponse struct {
	TokenType         string       `json:"token_type"`
	AccessToken       string       `json:"access_token"`
	RefreshToken      string       `json:"refresh_token"`
	ExpiresIn         int64        `json:"expires_in"`
	ServerTime        time.Time    `json:"server_time"`
	TenantID          string       `json:"tenant_id"`
	AccountID         string       `json:"account_id"`
	DeviceID          string       `json:"device_id"`
	Scope             string       `json:"scope"`
	SigningKeyID      string       `json:"signing_key_id"`
	ServerSigningKeys []SigningKey `json:"server_signing_keys"`
}
type RevokeRequest struct {
	Token         string `json:"token"`
	TokenTypeHint string `json:"token_type_hint"`
	RequestID     string `json:"request_id"`
}
type WhoResponse struct {
	TenantID          string       `json:"tenant_id"`
	AccountID         string       `json:"account_id"`
	DeviceID          string       `json:"device_id"`
	Scope             string       `json:"scope"`
	ExpiresAt         time.Time    `json:"expires_at"`
	ServerTime        time.Time    `json:"server_time"`
	SigningKeyID      string       `json:"signing_key_id"`
	ServerSigningKeys []SigningKey `json:"server_signing_keys"`
}
type flow struct {
	Begin         BeginRequest
	InitialSecret string
	Challenge     Challenge
	Expires       int64
	NextPoll      int64
	Account       string
	Device        string
	PublicKey     []byte
	RefreshDigest [32]byte
	Sessions      []devicesession.ID
	Revoked       bool
	Responses     map[string]tokenReplay
	RevokeRequest string
}
type tokenReplay struct {
	Digest   [32]byte
	Response TokenResponse
}

func New(provider Provider, store Store, manager *devicesession.Manager, secret []byte, subjects []SubjectBinding, keys Keys) (*Service, error) {
	if provider == nil || store == nil || manager == nil || len(secret) < 32 || len(subjects) == 0 || keys == nil {
		return nil, ErrDenied
	}
	digest := sha256.Sum256(append([]byte("device-auth-aead/v1:"), secret...))
	block, e := aes.NewCipher(digest[:])
	if e != nil {
		return nil, e
	}
	aead, e := cipher.NewGCM(block)
	if e != nil {
		return nil, e
	}
	return &Service{provider, store, manager, aead, append([]SubjectBinding(nil), subjects...), keys}, nil
}
func random() string {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func (s *Service) seal(scope tenant.Scope, id string, f flow) ([]byte, error) {
	raw, e := json.Marshal(f)
	if e != nil || len(raw) > 131072 {
		return nil, ErrDenied
	}
	nonce := make([]byte, s.cipher.NonceSize())
	if _, e = rand.Read(nonce); e != nil {
		return nil, e
	}
	return s.cipher.Seal(nonce, nonce, raw, []byte(string(scope.Organization)+":"+id)), nil
}
func (s *Service) open(scope tenant.Scope, id string, raw []byte) (flow, error) {
	var f flow
	n := s.cipher.NonceSize()
	if len(raw) < n || len(raw) > 131104 {
		return f, ErrDenied
	}
	body, e := s.cipher.Open(nil, raw[:n], raw[n:], []byte(string(scope.Organization)+":"+id))
	if e != nil || json.Unmarshal(body, &f) != nil {
		return f, ErrDenied
	}
	return f, nil
}
func locator(scope tenant.Scope, id, secret string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(scope.Organization)) + "." + id + "." + secret
}
func parse(raw string) (tenant.Scope, string, string, error) {
	var empty tenant.Scope
	p := strings.Split(raw, ".")
	if len(p) != 3 || len(raw) > 512 {
		return empty, "", "", ErrDenied
	}
	b, e := base64.RawURLEncoding.DecodeString(p[0])
	org, e2 := tenant.NewOrganizationID(string(b))
	scope, e3 := tenant.NewScope(org)
	secret, e4 := base64.RawURLEncoding.DecodeString(p[2])
	if e != nil || e2 != nil || e3 != nil || e4 != nil || len(secret) != 32 || len(p[1]) != 43 {
		return empty, "", "", ErrDenied
	}
	return scope, p[1], p[2], nil
}
func (s *Service) Begin(ctx context.Context, r BeginRequest) (BeginResponse, error) {
	var out BeginResponse
	org, e := tenant.NewOrganizationID(r.TenantID)
	scope, e2 := tenant.NewScope(org)
	pub, e3 := base64.StdEncoding.DecodeString(r.PublicKey)
	if e != nil || e2 != nil || e3 != nil || len(pub) != 32 || r.RequestID == "" || len(r.RequestID) > 128 || len(r.DeviceName) < 1 || len(r.DeviceName) > 128 {
		return out, ErrDenied
	}
	if r.Platform != "darwin" && r.Platform != "linux" && r.Platform != "windows" {
		return out, ErrDenied
	}
	challenge, e := s.provider.Begin(ctx)
	if e != nil {
		return out, e
	}
	if challenge.ExpiresIn > 900 {
		challenge.ExpiresIn = 900
	}
	idDigest := sha256.Sum256([]byte(string(scope.Organization) + ":" + r.RequestID))
	id, secret := base64.RawURLEncoding.EncodeToString(idDigest[:]), random()
	if id == "" || secret == "" {
		return out, OAuthError("temporarily_unavailable")
	}
	now := time.Now().UTC()
	f := flow{Begin: r, InitialSecret: secret, Challenge: challenge, Expires: now.Unix() + challenge.ExpiresIn, PublicKey: pub, RefreshDigest: sha256.Sum256([]byte(secret)), Responses: map[string]tokenReplay{}}
	raw, e := s.seal(scope, id, f)
	if e != nil {
		return out, e
	}
	stored, e := s.store.CreateAuth(ctx, scope, id, raw, f.Expires+30*24*3600)
	if e != nil {
		return out, OAuthError("temporarily_unavailable")
	}
	existing, e := s.open(scope, id, stored)
	if e != nil || existing.Begin != r || existing.Revoked || existing.Expires <= now.Unix() {
		return out, ErrDenied
	}
	return BeginResponse{locator(scope, id, existing.InitialSecret), existing.Challenge.UserCode, existing.Challenge.VerificationURI, existing.Expires - now.Unix(), existing.Challenge.Interval, now}, nil
}
func (s *Service) Token(ctx context.Context, r TokenRequest) (TokenResponse, error) {
	var out TokenResponse
	if r.RequestID == "" || len(r.RequestID) > 128 {
		return out, ErrDenied
	}
	if r.GrantType == "refresh_token" && r.DeviceCode != "" || r.GrantType != "refresh_token" && r.RefreshToken != "" {
		return out, ErrDenied
	}
	credential := r.DeviceCode
	if r.GrantType == "refresh_token" {
		credential = r.RefreshToken
	} else if r.GrantType != "urn:ietf:params:oauth:grant-type:device_code" {
		return out, ErrDenied
	}
	scope, id, secret, e := parse(credential)
	if e != nil {
		return out, e
	}
	digest := sha256.Sum256([]byte(secret))
	requestDigest := sha256.Sum256([]byte(r.GrantType + ":" + credential))
	var observed error
	e = s.store.UpdateAuth(ctx, scope, id, func(raw []byte) ([]byte, *SessionChange, error) {
		f, e := s.open(scope, id, raw)
		if e != nil {
			return nil, nil, e
		}
		if f.Revoked {
			return nil, nil, ErrDenied
		}
		if prior, ok := f.Responses[r.RequestID]; ok {
			if prior.Digest != requestDigest {
				return nil, nil, ErrDenied
			}
			if _, e := s.Who(ctx, "Bearer "+prior.Response.AccessToken); e != nil {
				return nil, nil, ErrDenied
			}
			out = prior.Response
			return raw, nil, nil
		}
		if f.Revoked || subtle.ConstantTimeCompare(digest[:], f.RefreshDigest[:]) != 1 {
			return nil, nil, ErrDenied
		}
		now := time.Now().UTC()
		if r.GrantType != "refresh_token" {
			if f.Account != "" || now.Unix() >= f.Expires {
				return nil, nil, OAuthError("expired_token")
			}
			if now.Unix() < f.NextPoll {
				observed = OAuthError("slow_down")
				f.Challenge.Interval += 5
			} else {
				f.NextPoll = now.Unix() + f.Challenge.Interval
				subject, pe := s.provider.Poll(ctx, f.Challenge.DeviceCode)
				if pe != nil {
					if errors.Is(pe, OAuthError("authorization_pending")) || errors.Is(pe, OAuthError("slow_down")) {
						observed = pe
						if errors.Is(pe, OAuthError("slow_down")) {
							f.Challenge.Interval += 5
						}
					} else {
						return nil, nil, pe
					}
				} else {
					for _, b := range s.subjects {
						if b.Tenant == string(scope.Organization) && b.Subject == subject {
							f.Account = b.Account
							break
						}
					}
					if f.Account == "" {
						return nil, nil, ErrDenied
					}
				}
			}
			if observed != nil {
				sealed, se := s.seal(scope, id, f)
				return sealed, nil, se
			}
		}
		account, ae := tenant.NewAccountID(f.Account)
		membership, me := s.store.CurrentMembership(ctx, scope, account)
		if ae != nil || me != nil || membership.State != tenant.LifecycleActive || membership.Account != account || (membership.ExpiresUnix > 0 && membership.ExpiresUnix <= now.Unix()) {
			return nil, nil, ErrDenied
		}
		keyID, keys, ke := s.keys.Approved(scope, f.PublicKey)
		if ke != nil {
			return nil, nil, ErrDenied
		}
		if f.Device == "" {
			f.Device = "device-" + id
		}
		device, de := tenant.NewDeviceID(f.Device)
		if de != nil {
			return nil, nil, ErrDenied
		}
		access, nextRefresh, sessionID := random(), random(), "session-"+random()
		if access == "" || nextRefresh == "" || len(sessionID) != 51 {
			return nil, nil, ErrDenied
		}
		accessBytes, _ := base64.RawURLEncoding.DecodeString(access)
		record := devicesession.Record{Tenant: scope.Organization, ID: devicesession.ID(sessionID), Account: account, Device: device, CredentialDigest: sha256.Sum256(accessBytes), State: devicesession.StateActive, Version: 1, ExpiresUnix: now.Add(15 * time.Minute).Unix()}
		platform := tenant.PlatformLinux
		if f.Begin.Platform == "darwin" {
			platform = tenant.PlatformDarwin
		} else if f.Begin.Platform == "windows" {
			platform = tenant.PlatformWindows
		}
		d := tenant.Device{Tenant: scope.Organization, ID: device, Account: account, Name: f.Begin.DeviceName, Platform: platform, State: tenant.LifecycleActive, Version: 1}
		copy(d.PublicKey[:], f.PublicKey)
		out = TokenResponse{TokenType: "Bearer", AccessToken: base64.RawURLEncoding.EncodeToString([]byte(scope.Organization)) + "." + access, RefreshToken: locator(scope, id, nextRefresh), ExpiresIn: 900, ServerTime: now, TenantID: string(scope.Organization), AccountID: f.Account, DeviceID: f.Device, Scope: "device sync", SigningKeyID: keyID}
		for key, pub := range keys {
			out.ServerSigningKeys = append(out.ServerSigningKeys, SigningKey{key, pub})
		}
		if len(f.Responses) >= 32 {
			return nil, nil, ErrDenied
		}
		f.Responses[r.RequestID] = tokenReplay{requestDigest, out}
		f.RefreshDigest = sha256.Sum256([]byte(nextRefresh))
		change := &SessionChange{Device: d, Session: record, RevokeSessionIDs: append([]devicesession.ID(nil), f.Sessions...), CorrelationID: authCorrelation(id, r.RequestID), ExpectedMembershipVersion: membership.Version}
		f.Sessions = append(f.Sessions, record.ID)
		sealed, se := s.seal(scope, id, f)
		return sealed, change, se
	})
	if e != nil {
		var oe OAuthError
		if errors.As(e, &oe) {
			return out, e
		}
		if errors.Is(e, ErrDenied) {
			return out, ErrDenied
		}
		return out, OAuthError("temporarily_unavailable")
	}
	if observed != nil {
		return TokenResponse{}, observed
	}
	return out, nil
}
func (s *Service) Revoke(ctx context.Context, r RevokeRequest) error {
	if r.TokenTypeHint != "refresh_token" || r.RequestID == "" || len(r.RequestID) > 128 {
		return ErrDenied
	}
	scope, id, secret, e := parse(r.Token)
	if e != nil {
		return e
	}
	digest := sha256.Sum256([]byte(secret))
	return s.store.UpdateAuth(ctx, scope, id, func(raw []byte) ([]byte, *SessionChange, error) {
		f, e := s.open(scope, id, raw)
		if e != nil {
			return nil, nil, e
		}
		if f.Revoked && f.RevokeRequest == r.RequestID {
			return raw, nil, nil
		}
		if f.Revoked || subtle.ConstantTimeCompare(digest[:], f.RefreshDigest[:]) != 1 {
			return nil, nil, ErrDenied
		}
		f.Revoked = true
		f.RevokeRequest = r.RequestID
		next, e := s.seal(scope, id, f)
		return next, &SessionChange{RevokeSessionIDs: f.Sessions, CorrelationID: authCorrelation(id, r.RequestID)}, e
	})
}
func (s *Service) Who(ctx context.Context, bearer string) (WhoResponse, error) {
	var out WhoResponse
	if !strings.HasPrefix(bearer, "Bearer ") {
		return out, ErrDenied
	}
	p := strings.Split(strings.TrimPrefix(bearer, "Bearer "), ".")
	if len(p) != 2 {
		return out, ErrDenied
	}
	b, e := base64.RawURLEncoding.DecodeString(p[0])
	org, e2 := tenant.NewOrganizationID(string(b))
	scope, e3 := tenant.NewScope(org)
	credential, e4 := base64.RawURLEncoding.DecodeString(p[1])
	if e != nil || e2 != nil || e3 != nil || e4 != nil {
		return out, ErrDenied
	}
	snapshot, e := s.manager.Authorize(ctx, scope, credential)
	if e != nil {
		return out, ErrDenied
	}
	id, keys, e := s.keys.Approved(scope, snapshot.Device.PublicKey[:])
	if e != nil {
		return out, ErrDenied
	}
	out = WhoResponse{TenantID: string(org), AccountID: string(snapshot.Session.Account), DeviceID: string(snapshot.Device.ID), Scope: "device sync", ExpiresAt: time.Unix(snapshot.Session.ExpiresUnix, 0).UTC(), ServerTime: time.Now().UTC(), SigningKeyID: id}
	for key, pub := range keys {
		out.ServerSigningKeys = append(out.ServerSigningKeys, SigningKey{key, pub})
	}
	return out, nil
}

func authCorrelation(id, request string) string {
	sum := sha256.Sum256([]byte(id + ":" + request))
	return hex.EncodeToString(sum[:])
}
