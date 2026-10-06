// Package config loads the control-plane process configuration.
package config

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/mlnomadpy/dacli/cloud/internal/tenant"
	"io"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/mlnomadpy/dacli/cloud/internal/ratelimit"
)

const maxConfigBytes = 64 << 10

// File is the non-secret configuration persisted by operators.
type File struct {
	Hosted           *HostedFile   `json:"hosted,omitempty"`
	Mode             string        `json:"mode"`
	ListenAddress    string        `json:"listen_address"`
	PublicBaseURL    string        `json:"public_base_url"`
	RequestTimeout   string        `json:"request_timeout"`
	ShutdownTimeout  string        `json:"shutdown_timeout"`
	MaxRequestBytes  int64         `json:"max_request_bytes"`
	WorkerInterval   string        `json:"worker_interval"`
	DatabaseURLEnv   string        `json:"database_url_env"`
	ServiceSecretEnv string        `json:"service_secret_env"`
	ContractMinimum  int           `json:"contract_minimum"`
	ContractMaximum  int           `json:"contract_maximum"`
	RateLimits       RateLimitFile `json:"rate_limits"`
}

type LimitFile struct {
	Capacity       int    `json:"capacity"`
	RefillInterval string `json:"refill_interval"`
	MaxKeys        int    `json:"max_keys"`
	IdleTTL        string `json:"idle_ttl"`
}

type RateLimitFile struct {
	Identity   LimitFile `json:"identity"`
	TenantRead LimitFile `json:"tenant_read"`
	Sync       LimitFile `json:"sync"`
}

type RateLimits struct {
	Identity   ratelimit.Policy
	TenantRead ratelimit.Policy
	Sync       ratelimit.Policy
}

// Config is the validated runtime configuration. Secrets remain private.
type Config struct {
	hosted          *HostedRuntime
	Mode            string
	ListenAddress   string
	PublicBaseURL   *url.URL
	RequestTimeout  time.Duration
	ShutdownTimeout time.Duration
	MaxRequestBytes int64
	WorkerInterval  time.Duration
	ContractMinimum int
	ContractMaximum int
	RateLimits      RateLimits
	databaseURL     string
	serviceSecret   string
}

// LookupEnv allows tests and embedding processes to provide a controlled environment.
type LookupEnv func(string) (string, bool)

func Load(r io.Reader, lookup LookupEnv) (Config, error) {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	limited := io.LimitReader(r, maxConfigBytes+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return Config{}, fmt.Errorf("read configuration: %w", err)
	}
	if len(raw) > maxConfigBytes {
		return Config{}, errors.New("configuration exceeds 64 KiB")
	}
	var f File
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&f); err != nil {
		return Config{}, fmt.Errorf("decode configuration: %w", err)
	}
	if err := requireEOF(decoder); err != nil {
		return Config{}, err
	}
	return validate(f, lookup)
}

func LoadFile(path string, lookup LookupEnv) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open configuration: %w", err)
	}
	defer func() { _ = f.Close() }()
	return Load(f, lookup)
}

func requireEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("configuration contains multiple JSON values")
		}
		return fmt.Errorf("decode trailing configuration: %w", err)
	}
	return nil
}

func validate(f File, lookup LookupEnv) (Config, error) {
	if f.Mode != "development" && f.Mode != "production" {
		return Config{}, errors.New("mode must be development or production")
	}
	if _, port, err := net.SplitHostPort(f.ListenAddress); err != nil || port == "" {
		return Config{}, errors.New("listen_address must include a host and port")
	}
	base, err := url.Parse(f.PublicBaseURL)
	if err != nil || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") {
		return Config{}, errors.New("public_base_url must be an absolute HTTP(S) URL")
	}
	if base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return Config{}, errors.New("public_base_url must not contain credentials, query, or fragment")
	}
	if f.Mode == "production" && base.Scheme != "https" {
		return Config{}, errors.New("production public_base_url must use HTTPS")
	}
	requestTimeout, err := boundedDuration("request_timeout", f.RequestTimeout, time.Second, 2*time.Minute)
	if err != nil {
		return Config{}, err
	}
	shutdownTimeout, err := boundedDuration("shutdown_timeout", f.ShutdownTimeout, time.Second, 2*time.Minute)
	if err != nil {
		return Config{}, err
	}
	workerInterval, err := boundedDuration("worker_interval", f.WorkerInterval, time.Second, time.Hour)
	if err != nil {
		return Config{}, err
	}
	if f.MaxRequestBytes < 1024 || f.MaxRequestBytes > 1<<20 {
		return Config{}, errors.New("max_request_bytes must be between 1024 and 1048576")
	}
	if f.ContractMinimum != 1 || f.ContractMaximum != 1 {
		return Config{}, errors.New("this binary supports exactly control-plane contract version 1")
	}
	rateLimits, err := validateRateLimits(f.RateLimits)
	if err != nil {
		return Config{}, err
	}
	databaseURL, err := secretFromEnv("database_url_env", f.DatabaseURLEnv, lookup)
	if err != nil {
		return Config{}, err
	}
	serviceSecret, err := secretFromEnv("service_secret_env", f.ServiceSecretEnv, lookup)
	if err != nil {
		return Config{}, err
	}
	if f.Mode == "production" {
		if weakSecret(serviceSecret) {
			return Config{}, errors.New("production service secret is missing, too short, or a known default")
		}
		dbURL, parseErr := url.Parse(databaseURL)
		if parseErr != nil || (dbURL.Scheme != "postgres" && dbURL.Scheme != "postgresql") || dbURL.Host == "" {
			return Config{}, errors.New("production database URL must be an absolute PostgreSQL URL")
		}
		switch dbURL.Query().Get("sslmode") {
		case "require", "verify-ca", "verify-full":
		default:
			return Config{}, errors.New("production database URL must explicitly require or verify TLS")
		}
		if dbURL.User == nil {
			return Config{}, errors.New("production database credential is missing, too short, or a known default")
		}
		password, present := dbURL.User.Password()
		if !present || weakSecret(password) {
			return Config{}, errors.New("production database credential is missing, too short, or a known default")
		}
	}
	hosted, err := validateHosted(f.Hosted, f.Mode, lookup)
	if err != nil {
		return Config{}, err
	}
	return Config{hosted: hosted,
		Mode: f.Mode, ListenAddress: f.ListenAddress, PublicBaseURL: base,
		RequestTimeout: requestTimeout, ShutdownTimeout: shutdownTimeout,
		MaxRequestBytes: f.MaxRequestBytes, WorkerInterval: workerInterval,
		ContractMinimum: f.ContractMinimum, ContractMaximum: f.ContractMaximum,
		RateLimits:  rateLimits,
		databaseURL: databaseURL, serviceSecret: serviceSecret,
	}, nil
}

func validateRateLimits(value RateLimitFile) (RateLimits, error) {
	identity, err := validateLimit("rate_limits.identity", value.Identity)
	if err != nil {
		return RateLimits{}, err
	}
	tenantRead, err := validateLimit("rate_limits.tenant_read", value.TenantRead)
	if err != nil {
		return RateLimits{}, err
	}
	syncLimit, err := validateLimit("rate_limits.sync", value.Sync)
	if err != nil {
		return RateLimits{}, err
	}
	return RateLimits{Identity: identity, TenantRead: tenantRead, Sync: syncLimit}, nil
}

func validateLimit(name string, value LimitFile) (ratelimit.Policy, error) {
	refill, err := boundedDuration(name+".refill_interval", value.RefillInterval, time.Millisecond, time.Hour)
	if err != nil {
		return ratelimit.Policy{}, err
	}
	idle, err := boundedDuration(name+".idle_ttl", value.IdleTTL, refill, 24*time.Hour)
	if err != nil {
		return ratelimit.Policy{}, err
	}
	policy := ratelimit.Policy{Capacity: value.Capacity, RefillInterval: refill, MaxKeys: value.MaxKeys, IdleTTL: idle}
	if err := policy.Validate(); err != nil {
		return ratelimit.Policy{}, fmt.Errorf("%s: %w", name, err)
	}
	return policy, nil
}

func boundedDuration(name, value string, minimum, maximum time.Duration) (time.Duration, error) {
	d, err := time.ParseDuration(value)
	if err != nil || d < minimum || d > maximum {
		return 0, fmt.Errorf("%s must be a duration between %s and %s", name, minimum, maximum)
	}
	return d, nil
}

func secretFromEnv(field, name string, lookup LookupEnv) (string, error) {
	if name == "" || strings.ContainsAny(name, "= \t\r\n") {
		return "", fmt.Errorf("%s must name one environment variable", field)
	}
	value, ok := lookup(name)
	if !ok || value == "" {
		return "", fmt.Errorf("%s environment variable is not set", field)
	}
	return value, nil
}

func weakSecret(value string) bool {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if len(value) < 32 {
		return true
	}
	for _, marker := range []string{"changeme", "change-me", "password", "default", "example", "secret"} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

// DatabaseURL returns the resolved database credential to infrastructure adapters only.
func (c Config) DatabaseURL() string { return c.databaseURL }

// ServiceSecret returns the resolved signing credential to infrastructure adapters only.
func (c Config) ServiceSecret() string { return c.serviceSecret }

// DatabaseAddress returns the credential-free PostgreSQL network address.
func (c Config) DatabaseAddress() (string, error) {
	database, err := url.Parse(c.databaseURL)
	if err != nil || database.Hostname() == "" {
		return "", errors.New("database URL has no network host")
	}
	port := database.Port()
	if port == "" {
		port = "5432"
	}
	return net.JoinHostPort(database.Hostname(), port), nil
}

// SafeSummary is deliberately safe for structured logs and diagnostics.
func (c Config) SafeSummary() map[string]any {
	return map[string]any{"mode": c.Mode, "listen_address": c.ListenAddress, "public_base_url": c.PublicBaseURL.String(), "contract_minimum": c.ContractMinimum, "contract_maximum": c.ContractMaximum, "rate_limits": map[string]any{"identity": safeLimit(c.RateLimits.Identity), "tenant_read": safeLimit(c.RateLimits.TenantRead), "sync": safeLimit(c.RateLimits.Sync)}}
}

func safeLimit(value ratelimit.Policy) map[string]any {
	return map[string]any{"capacity": value.Capacity, "refill_interval": value.RefillInterval.String(), "max_keys": value.MaxKeys, "idle_ttl": value.IdleTTL.String()}
}

// Hosted composition is opt-in in configuration but mandatory for these
// production entrypoints. The legacy config parser remains compatible.
type HostedFile struct {
	PolicyRevision uint64             `json:"policy_revision"`
	Tenants        []HostedTenantFile `json:"tenants"`
}
type HostedTenantFile struct {
	TenantID       string          `json:"tenant_id"`
	DestinationURL string          `json:"destination_url,omitempty"`
	CredentialEnv  string          `json:"credential_env,omitempty"`
	TrustedKeys    []HostedKeyFile `json:"trusted_keys"`
}
type HostedKeyFile struct {
	ProjectID string `json:"project_id"`
	KeyID     string `json:"key_id"`
	PublicKey string `json:"public_key"`
}
type HostedTenant struct {
	Tenant      tenant.OrganizationID
	Destination string
	Keys        map[tenant.ProjectID]map[string]ed25519.PublicKey
}
type HostedRuntime struct {
	PolicyRevision uint64
	tenants        []HostedTenant
	credentials    map[tenant.OrganizationID]string
}

func validateHosted(f *HostedFile, mode string, lookup LookupEnv) (*HostedRuntime, error) {
	if f == nil {
		return nil, nil
	}
	if f.PolicyRevision == 0 || len(f.Tenants) == 0 || len(f.Tenants) > 100 {
		return nil, errors.New("hosted requires a policy revision and 1..100 explicit tenants")
	}
	out := &HostedRuntime{PolicyRevision: f.PolicyRevision, credentials: map[tenant.OrganizationID]string{}}
	seen := map[tenant.OrganizationID]bool{}
	for _, v := range f.Tenants {
		id, e := tenant.NewOrganizationID(v.TenantID)
		if e != nil || seen[id] {
			return nil, errors.New("hosted tenant invalid or repeated")
		}
		seen[id] = true
		t := HostedTenant{Tenant: id, Keys: map[tenant.ProjectID]map[string]ed25519.PublicKey{}}
		if len(v.TrustedKeys) == 0 || len(v.TrustedKeys) > 1000 {
			return nil, errors.New("hosted tenant requires bounded explicit project signing keys")
		}
		for _, k := range v.TrustedKeys {
			project, e := tenant.NewProjectID(k.ProjectID)
			key, e2 := base64.StdEncoding.DecodeString(k.PublicKey)
			if e != nil || e2 != nil || len(key) != ed25519.PublicKeySize || k.KeyID == "" || len(k.KeyID) > 128 {
				return nil, errors.New("hosted signing key invalid")
			}
			if t.Keys[project] == nil {
				t.Keys[project] = map[string]ed25519.PublicKey{}
			}
			if _, ok := t.Keys[project][k.KeyID]; ok {
				return nil, errors.New("hosted signing key repeated")
			}
			t.Keys[project][k.KeyID] = ed25519.PublicKey(append([]byte(nil), key...))
		}
		if v.DestinationURL != "" {
			u, e := url.Parse(v.DestinationURL)
			if e != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && (mode != "development" || u.Scheme != "http" || (u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1" && u.Hostname() != "::1"))) {
				return nil, errors.New("hosted delivery requires an explicit credential-free HTTPS destination (loopback HTTP only in development)")
			}
			credential, e := secretFromEnv("hosted delivery credential", v.CredentialEnv, lookup)
			if e != nil || len(credential) > 4096 || strings.ContainsAny(credential, "\r\n") {
				return nil, errors.New("hosted delivery credential unavailable or invalid")
			}
			t.Destination = u.String()
			out.credentials[id] = credential
		} else if v.CredentialEnv != "" {
			return nil, errors.New("delivery credential requires an explicit destination")
		}
		out.tenants = append(out.tenants, t)
	}
	return out, nil
}
func (c Config) HostedRuntime() (*HostedRuntime, error) {
	if c.hosted == nil {
		return nil, errors.New("hosted composition requires explicit hosted configuration")
	}
	copy := &HostedRuntime{PolicyRevision: c.hosted.PolicyRevision, credentials: map[tenant.OrganizationID]string{}}
	for _, t := range c.hosted.tenants {
		v := HostedTenant{Tenant: t.Tenant, Destination: t.Destination, Keys: map[tenant.ProjectID]map[string]ed25519.PublicKey{}}
		for project, keys := range t.Keys {
			v.Keys[project] = map[string]ed25519.PublicKey{}
			for id, key := range keys {
				v.Keys[project][id] = append(ed25519.PublicKey(nil), key...)
			}
		}
		copy.tenants = append(copy.tenants, v)
		copy.credentials[t.Tenant] = c.hosted.credentials[t.Tenant]
	}
	return copy, nil
}
func (h *HostedRuntime) Tenants() []HostedTenant                           { return append([]HostedTenant(nil), h.tenants...) }
func (h *HostedRuntime) DeliveryCredential(t tenant.OrganizationID) string { return h.credentials[t] }
func (h HostedRuntime) String() string                                     { return "HostedRuntime" }
func (h HostedRuntime) GoString() string                                   { return "HostedRuntime" }
