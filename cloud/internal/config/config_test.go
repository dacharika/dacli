package config

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mlnomadpy/dacli/cloud/internal/tenant"
)

const validConfig = `{"mode":"production","listen_address":"127.0.0.1:8080","public_base_url":"https://control.example.test","request_timeout":"10s","shutdown_timeout":"15s","max_request_bytes":65536,"worker_interval":"5s","database_url_env":"TEST_DATABASE_URL","service_secret_env":"TEST_SERVICE_SECRET","contract_minimum":1,"contract_maximum":1,"rate_limits":{"identity":{"capacity":20,"refill_interval":"1s","max_keys":4096,"idle_ttl":"10m"},"tenant_read":{"capacity":100,"refill_interval":"100ms","max_keys":4096,"idle_ttl":"10m"},"sync":{"capacity":200,"refill_interval":"50ms","max_keys":4096,"idle_ttl":"10m"}}}`

func environment(name string) (string, bool) {
	values := map[string]string{
		"TEST_DATABASE_URL":   "postgres://service:p9x7v3m2q8k4z6n1c5b0r7t9w2y4u8i6@db.example.test/dacli?sslmode=require",
		"TEST_SERVICE_SECRET": "a-cryptographically-random-value-with-ample-length-2026",
	}
	v, ok := values[name]
	return v, ok
}

func TestProductionConfigurationIsStrictAndSecretSafe(t *testing.T) {
	cfg, err := Load(strings.NewReader(validConfig), environment)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(mapValues(cfg.SafeSummary()), " "), "p9x7v3") {
		t.Fatal("safe summary disclosed database credentials")
	}
	limits, ok := cfg.SafeSummary()["rate_limits"].(map[string]any)
	if !ok || len(limits) != 3 || cfg.RateLimits.Identity.MaxKeys != 4096 {
		t.Fatalf("safe rate-limit summary/config missing: %#v", cfg.SafeSummary())
	}
	if address, err := cfg.DatabaseAddress(); err != nil || address != "db.example.test:5432" {
		t.Fatalf("database address = %q, %v", address, err)
	}
	for name, replacement := range map[string]string{
		"unknown fields":        strings.Replace(validConfig, `"mode":`, `"surprise":true,"mode":`, 1),
		"plaintext production":  strings.Replace(validConfig, "https://", "http://", 1),
		"incompatible contract": strings.Replace(validConfig, `"contract_maximum":1`, `"contract_maximum":2`, 1),
		"unbounded rate keys":   strings.Replace(validConfig, `"max_keys":4096`, `"max_keys":0`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(strings.NewReader(replacement), environment); err == nil {
				t.Fatal("unsafe configuration was accepted")
			}
		})
	}
}

func TestProductionRefusesDatabaseWithoutExplicitTLS(t *testing.T) {
	lookup := func(name string) (string, bool) {
		if name == "TEST_DATABASE_URL" {
			return "postgres://service:p9x7v3m2q8k4z6n1c5b0r7t9w2y4u8i6@db.example.test/dacli", true
		}
		return environment(name)
	}
	if _, err := Load(strings.NewReader(validConfig), lookup); err == nil {
		t.Fatal("database URL without explicit TLS was accepted")
	}
}

func TestProductionRefusesDefaultSecret(t *testing.T) {
	lookup := func(name string) (string, bool) {
		if name == "TEST_SERVICE_SECRET" {
			return "changeme-this-is-deliberately-long-but-still-weak", true
		}
		return environment(name)
	}
	if _, err := Load(strings.NewReader(validConfig), lookup); err == nil {
		t.Fatal("default secret was accepted")
	}
}

func mapValues(values map[string]any) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, fmt.Sprint(value))
	}
	return out
}

func validFile(t *testing.T) File {
	t.Helper()
	var f File
	if err := json.Unmarshal([]byte(validConfig), &f); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestLoadRejectsInvalidDocumentBoundaries(t *testing.T) {
	tests := []struct {
		name string
		read io.Reader
		want string
	}{
		{name: "reader error", read: failingReader{}, want: "read configuration"},
		{name: "over limit", read: strings.NewReader(strings.Repeat(" ", maxConfigBytes+1)), want: "configuration exceeds 64 KiB"},
		{name: "malformed JSON", read: strings.NewReader("{"), want: "decode configuration"},
		{name: "multiple values", read: strings.NewReader(validConfig + ` {}`), want: "multiple JSON values"},
		{name: "trailing syntax", read: strings.NewReader(validConfig + ` garbage`), want: "decode trailing configuration"},
		{name: "unknown field", read: strings.NewReader(strings.Replace(validConfig, `"mode":`, `"unknown":true,"mode":`, 1)), want: "unknown field"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(tt.read, environment)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Load error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("fixture read failure") }

func TestLoadFileReportsOpenFailureAndDefaultsEnvironmentLookup(t *testing.T) {
	if _, err := LoadFile(filepath.Join(t.TempDir(), "missing.json"), environment); err == nil || !strings.Contains(err.Error(), "open configuration") {
		t.Fatalf("missing config error = %v", err)
	}
	name := "DACLI_CONFIG_TEST_SECRET"
	t.Setenv(name, strings.Repeat("s", 40))
	development := strings.ReplaceAll(validConfig, `"mode":"production"`, `"mode":"development"`)
	development = strings.ReplaceAll(development, `"public_base_url":"https://`, `"public_base_url":"http://`)
	development = strings.ReplaceAll(development, `"TEST_SERVICE_SECRET"`, `"`+name+`"`)
	development = strings.ReplaceAll(development, `"TEST_DATABASE_URL"`, `"`+name+`"`)
	if _, err := Load(strings.NewReader(development), nil); err != nil {
		t.Fatalf("nil lookup did not use process environment: %v", err)
	}
}

func TestValidationRefusesUnsafeCommonConfiguration(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*File)
		lookup LookupEnv
		want   string
	}{
		{"mode", func(f *File) { f.Mode = "staging" }, environment, "mode must"},
		{"listen address", func(f *File) { f.ListenAddress = "localhost" }, environment, "listen_address"},
		{"URL syntax", func(f *File) { f.PublicBaseURL = "://bad" }, environment, "absolute HTTP(S)"},
		{"URL credentials", func(f *File) { f.PublicBaseURL = "https://user:pass@control.example.test" }, environment, "must not contain credentials"},
		{"URL query", func(f *File) { f.PublicBaseURL = "https://control.example.test/?tenant=x" }, environment, "must not contain credentials"},
		{"URL fragment", func(f *File) { f.PublicBaseURL = "https://control.example.test/#x" }, environment, "must not contain credentials"},
		{"request timeout", func(f *File) { f.RequestTimeout = "0s" }, environment, "request_timeout"},
		{"shutdown timeout", func(f *File) { f.ShutdownTimeout = "3m" }, environment, "shutdown_timeout"},
		{"worker interval", func(f *File) { f.WorkerInterval = "bad" }, environment, "worker_interval"},
		{"request bytes", func(f *File) { f.MaxRequestBytes = 1023 }, environment, "max_request_bytes"},
		{"contract range", func(f *File) { f.ContractMinimum = 0 }, environment, "exactly control-plane contract"},
		{"refill interval", func(f *File) { f.RateLimits.Identity.RefillInterval = "0s" }, environment, "refill_interval"},
		{"idle TTL below refill", func(f *File) { f.RateLimits.Sync.IdleTTL = "1ms" }, environment, "idle_ttl"},
		{"capacity", func(f *File) { f.RateLimits.TenantRead.Capacity = 0 }, environment, "invalid bounded rate-limit policy"},
		{"max keys", func(f *File) { f.RateLimits.Sync.MaxKeys = 0 }, environment, "invalid bounded rate-limit policy"},
		{"invalid env name", func(f *File) { f.DatabaseURLEnv = "BAD NAME" }, environment, "database_url_env must name"},
		{"unset database env", func(f *File) { f.DatabaseURLEnv = "MISSING_DATABASE" }, environment, "environment variable is not set"},
		{"unset service env", func(f *File) { f.ServiceSecretEnv = "MISSING_SECRET" }, environment, "environment variable is not set"},
		{"weak service secret", func(*File) {}, func(name string) (string, bool) {
			if name == "TEST_SERVICE_SECRET" {
				return strings.Repeat("x", 20), true
			}
			return environment(name)
		}, "service secret is missing"},
		{"nonpostgres production database", func(*File) {}, func(name string) (string, bool) {
			if name == "TEST_DATABASE_URL" {
				return "mysql://u:p@db.example.test/db", true
			}
			return environment(name)
		}, "absolute PostgreSQL URL"},
		{"postgres without tls", func(*File) {}, func(name string) (string, bool) {
			if name == "TEST_DATABASE_URL" {
				return "postgres://u:long-enough-password-value@db.example.test/db", true
			}
			return environment(name)
		}, "explicitly require or verify TLS"},
		{"database without username", func(*File) {}, func(name string) (string, bool) {
			if name == "TEST_DATABASE_URL" {
				return "postgres://db.example.test/db?sslmode=require", true
			}
			return environment(name)
		}, "database credential is missing"},
		{"database without password", func(*File) {}, func(name string) (string, bool) {
			if name == "TEST_DATABASE_URL" {
				return "postgres://service@db.example.test/db?sslmode=require", true
			}
			return environment(name)
		}, "database credential is missing"},
		{"weak database password", func(*File) {}, func(name string) (string, bool) {
			if name == "TEST_DATABASE_URL" {
				return "postgres://service:password-is-long-but-still-weak@db.example.test/db?sslmode=require", true
			}
			return environment(name)
		}, "database credential is missing"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := validFile(t)
			tt.mutate(&f)
			_, err := validate(f, tt.lookup)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("validate error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestDevelopmentConfigurationAllowsLoopbackHTTPAndDefaultDatabasePort(t *testing.T) {
	f := validFile(t)
	f.Mode = "development"
	f.PublicBaseURL = "http://localhost:8080"
	lookup := func(name string) (string, bool) {
		if name == f.DatabaseURLEnv {
			return "postgres://user:pass@db.example.test/db", true
		}
		if name == f.ServiceSecretEnv {
			return "development-secret", true
		}
		return "", false
	}
	cfg, err := validate(f, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := cfg.DatabaseAddress(); err != nil || got != "db.example.test:5432" {
		t.Fatalf("DatabaseAddress() = %q, %v", got, err)
	}
	if _, err := (Config{databaseURL: "postgres:///database"}).DatabaseAddress(); err == nil {
		t.Fatal("database URL without host was accepted")
	}
	if _, err := validate(func() File { bad := f; bad.Mode = "production"; bad.PublicBaseURL = "http://localhost"; return bad }(), lookup); err == nil || !strings.Contains(err.Error(), "production public_base_url must use HTTPS") {
		t.Fatalf("production HTTP error = %v", err)
	}
}

func hostedFixture() *HostedFile {
	pub := base64.StdEncoding.EncodeToString(make([]byte, ed25519.PublicKeySize))
	return &HostedFile{PolicyRevision: 1, Tenants: []HostedTenantFile{{TenantID: "org_alpha", TrustedKeys: []HostedKeyFile{{ProjectID: "project_one", KeyID: "server-1", PublicKey: pub}}}}}
}

func TestHostedConfigurationValidatesAndReturnsDefensiveCopies(t *testing.T) {
	f := validFile(t)
	f.Hosted = hostedFixture()
	cfg, err := validate(f, environment)
	if err != nil {
		t.Fatal(err)
	}
	hosted, err := cfg.HostedRuntime()
	if err != nil {
		t.Fatal(err)
	}
	if hosted.PolicyRevision != 1 || len(hosted.Tenants()) != 1 || hosted.Tenants()[0].Tenant != tenant.OrganizationID("org_alpha") {
		t.Fatalf("hosted tenant binding = %#v", hosted.Tenants())
	}
	copyTenants := hosted.Tenants()
	copyTenants[0].Tenant = tenant.OrganizationID("changed")
	if got := hosted.Tenants(); len(got) != 1 || got[0].Tenant != tenant.OrganizationID("org_alpha") {
		t.Fatal("tenant slice copy mutated hosted runtime")
	}
	if got := hosted.DeliveryCredential(tenant.OrganizationID("org_alpha")); got != "" {
		t.Fatalf("unexpected credential for tenant without delivery: %q", got)
	}
	if _, err := (Config{}).HostedRuntime(); err == nil {
		t.Fatal("missing hosted composition was accepted")
	}
}

func TestHostedConfigurationRejectsInvalidTenantAndDeliveryBindings(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*HostedFile)
		lookup LookupEnv
		want   string
	}{
		{"missing tenants", func(h *HostedFile) { h.Tenants = nil }, environment, "1..100 explicit tenants"},
		{"duplicate tenant", func(h *HostedFile) { h.Tenants = append(h.Tenants, h.Tenants[0]) }, environment, "tenant invalid or repeated"},
		{"no trusted key", func(h *HostedFile) { h.Tenants[0].TrustedKeys = nil }, environment, "requires bounded explicit project signing keys"},
		{"bad project id", func(h *HostedFile) { h.Tenants[0].TrustedKeys[0].ProjectID = "../bad" }, environment, "signing key invalid"},
		{"bad public key", func(h *HostedFile) { h.Tenants[0].TrustedKeys[0].PublicKey = "bad" }, environment, "signing key invalid"},
		{"duplicate key id", func(h *HostedFile) {
			h.Tenants[0].TrustedKeys = append(h.Tenants[0].TrustedKeys, h.Tenants[0].TrustedKeys[0])
		}, environment, "signing key repeated"},
		{"invalid destination", func(h *HostedFile) {
			h.Tenants[0].DestinationURL = "http://public.example.test"
			h.Tenants[0].CredentialEnv = "DELIVERY"
		}, environment, "explicit credential-free HTTPS destination"},
		{"missing delivery credential", func(h *HostedFile) {
			h.Tenants[0].DestinationURL = "https://delivery.example.test"
			h.Tenants[0].CredentialEnv = "MISSING_DELIVERY"
		}, environment, "credential unavailable or invalid"},
		{"credential without destination", func(h *HostedFile) { h.Tenants[0].CredentialEnv = "DELIVERY" }, environment, "credential requires an explicit destination"},
		{"newline credential", func(h *HostedFile) {
			h.Tenants[0].DestinationURL = "https://delivery.example.test"
			h.Tenants[0].CredentialEnv = "DELIVERY"
		}, func(name string) (string, bool) {
			if name == "DELIVERY" {
				return "line1\nline2", true
			}
			return environment(name)
		}, "credential unavailable or invalid"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := validFile(t)
			f.Hosted = hostedFixture()
			tt.mutate(f.Hosted)
			_, err := validate(f, tt.lookup)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("validate error = %v, want containing %q", err, tt.want)
			}
		})
	}
	good := validFile(t)
	good.Hosted = hostedFixture()
	good.Hosted.Tenants[0].DestinationURL = "https://delivery.example.test"
	good.Hosted.Tenants[0].CredentialEnv = "DELIVERY"
	lookup := func(name string) (string, bool) {
		if name == "DELIVERY" {
			return "delivery-credential", true
		}
		return environment(name)
	}
	cfg, err := validate(good, lookup)
	if err != nil {
		t.Fatal(err)
	}
	hosted, err := cfg.HostedRuntime()
	if err != nil || hosted.DeliveryCredential(tenant.OrganizationID("org_alpha")) != "delivery-credential" {
		t.Fatalf("delivery credential not bound to tenant: runtime=%#v err=%v", hosted, err)
	}
}

func TestDeviceOAuthAndSyncConfigurationValidateAndCopy(t *testing.T) {
	f := validFile(t)
	hosted := hostedFixture()
	pub := hosted.Tenants[0].TrustedKeys[0].PublicKey
	hosted.DeviceOAuth = &DeviceOAuthFile{
		Issuer: "https://issuer.example.test", DeviceURL: "https://issuer.example.test/device",
		TokenURL: "https://issuer.example.test/token", IdentityURL: "https://issuer.example.test/me",
		ClientID: "dacli", Scope: "openid", Subjects: []DeviceSubject{{TenantID: "org_alpha", Subject: "sub-1", AccountID: "acct_1"}},
		ServerSigningKeys: []ServerSigningKey{{KeyID: "server-1", PublicKey: pub}},
	}
	hosted.SyncBindings = []SyncBinding{{WorkspaceID: "workspace-1", TenantID: "org_alpha", ProjectID: "project_one", EnvironmentID: "environment_one", RepositoryID: "repo-1", RepositoryURL: "https://git.example.test/org/repo", DefaultBranch: "main", Version: 1, MinimumSchemaVersion: 1, MaximumSchemaVersion: 2}}
	f.Hosted = hosted
	cfg, err := validate(f, environment)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := cfg.HostedRuntime()
	if err != nil {
		t.Fatal(err)
	}
	device := runtime.DeviceOAuth()
	if device == nil || len(device.Subjects) != 1 || device.ServerSigningKeys["server-1"] != pub {
		t.Fatalf("device runtime = %#v", device)
	}
	device.Subjects[0].Subject = "changed"
	device.ServerSigningKeys["server-1"] = "changed"
	if runtime.DeviceOAuth().Subjects[0].Subject != "sub-1" || runtime.DeviceOAuth().ServerSigningKeys["server-1"] != pub {
		t.Fatal("device OAuth copy mutated runtime")
	}
	bindings := runtime.SyncBindings()
	bindings[0].WorkspaceID = "changed"
	if runtime.SyncBindings()[0].WorkspaceID != "workspace-1" {
		t.Fatal("sync binding copy mutated runtime")
	}
	if (*HostedRuntime)(nil).DeviceOAuth() != nil {
		t.Fatal("nil hosted runtime returned device configuration")
	}
	if (HostedRuntime{}).String() != "HostedRuntime" || (HostedRuntime{}).GoString() != "HostedRuntime" || (DeviceOAuthRuntime{}).String() != "DeviceOAuthRuntime" || (DeviceOAuthRuntime{}).GoString() != "DeviceOAuthRuntime" {
		t.Fatal("safe runtime formatting changed")
	}
}

func TestDeviceOAuthAndSyncConfigurationRejectInvalidBindings(t *testing.T) {
	validOAuth := func() *DeviceOAuthFile {
		return &DeviceOAuthFile{Issuer: "https://issuer.example.test", DeviceURL: "https://issuer.example.test/device", TokenURL: "https://issuer.example.test/token", IdentityURL: "https://issuer.example.test/me", ClientID: "dacli", Scope: "openid", Subjects: []DeviceSubject{{TenantID: "org_alpha", Subject: "sub-1", AccountID: "acct_1"}}, ServerSigningKeys: []ServerSigningKey{{KeyID: "server-1", PublicKey: base64.StdEncoding.EncodeToString(make([]byte, 32))}}}
	}
	tests := []struct {
		name   string
		mutate func(*HostedFile)
		want   string
	}{
		{"bad OAuth endpoint", func(h *HostedFile) { h.DeviceOAuth = validOAuth(); h.DeviceOAuth.Issuer = "http://issuer.example.test" }, "device OAuth requires explicit HTTPS endpoints"},
		{"missing OAuth settings", func(h *HostedFile) { h.DeviceOAuth = validOAuth(); h.DeviceOAuth.ClientID = "" }, "requires bounded explicit subject bindings"},
		{"invalid subject account", func(h *HostedFile) { h.DeviceOAuth = validOAuth(); h.DeviceOAuth.Subjects[0].AccountID = "bad account" }, "subject binding invalid"},
		{"subject tenant not configured", func(h *HostedFile) { h.DeviceOAuth = validOAuth(); h.DeviceOAuth.Subjects[0].TenantID = "org_missing" }, "subject binding invalid"},
		{"duplicate subject", func(h *HostedFile) {
			h.DeviceOAuth = validOAuth()
			h.DeviceOAuth.Subjects = append(h.DeviceOAuth.Subjects, h.DeviceOAuth.Subjects[0])
		}, "subject binding invalid"},
		{"no server key", func(h *HostedFile) { h.DeviceOAuth = validOAuth(); h.DeviceOAuth.ServerSigningKeys = nil }, "requires explicit server public keys"},
		{"invalid server key", func(h *HostedFile) {
			h.DeviceOAuth = validOAuth()
			h.DeviceOAuth.ServerSigningKeys[0].PublicKey = "bad"
		}, "server public key invalid"},
		{"sync without OAuth", func(h *HostedFile) { h.SyncBindings = []SyncBinding{{}} }, "sync bindings require device OAuth"},
		{"invalid sync binding", func(h *HostedFile) {
			h.DeviceOAuth = validOAuth()
			h.SyncBindings = []SyncBinding{{TenantID: "org_alpha"}}
		}, "sync binding invalid"},
		{"sync missing static key", func(h *HostedFile) {
			h.DeviceOAuth = validOAuth()
			h.SyncBindings = []SyncBinding{{WorkspaceID: "w", TenantID: "org_alpha", ProjectID: "project_two", EnvironmentID: "environment_one", RepositoryID: "repo", RepositoryURL: "https://git.example.test/r", DefaultBranch: "main", Version: 1, MinimumSchemaVersion: 1, MaximumSchemaVersion: 1}}
		}, "requires static server project keys"},
		{"sync key mismatch", func(h *HostedFile) {
			h.DeviceOAuth = validOAuth()
			h.DeviceOAuth.ServerSigningKeys[0].PublicKey = base64.StdEncoding.EncodeToString(bytesOf(7, 32))
			h.SyncBindings = []SyncBinding{{WorkspaceID: "w", TenantID: "org_alpha", ProjectID: "project_one", EnvironmentID: "environment_one", RepositoryID: "repo", RepositoryURL: "https://git.example.test/r", DefaultBranch: "main", Version: 1, MinimumSchemaVersion: 1, MaximumSchemaVersion: 1}}
		}, "must match device OAuth server keys"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := validFile(t)
			f.Hosted = hostedFixture()
			tt.mutate(f.Hosted)
			_, err := validate(f, environment)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("validate error = %v, want containing %q", err, tt.want)
			}
		})
	}
	missingClientSecret := validFile(t)
	missingClientSecret.Hosted = hostedFixture()
	missingClientSecret.Hosted.DeviceOAuth = validOAuth()
	missingClientSecret.Hosted.DeviceOAuth.ClientSecretEnv = "MISSING_OAUTH_SECRET"
	if _, err := validate(missingClientSecret, environment); err == nil || !strings.Contains(err.Error(), "environment variable is not set") {
		t.Fatalf("missing OAuth client secret error = %v", err)
	}
}

func bytesOf(value byte, count int) []byte {
	out := make([]byte, count)
	for i := range out {
		out[i] = value
	}
	return out
}

func TestHostedLimitsAndProductionLoopbackDeliveryPolicy(t *testing.T) {
	for name, hosted := range map[string]*HostedFile{
		"zero policy revision": {Tenants: hostedFixture().Tenants},
		"too many tenants":     {PolicyRevision: 1, Tenants: make([]HostedTenantFile, 101)},
	} {
		t.Run(name, func(t *testing.T) {
			f := validFile(t)
			f.Hosted = hosted
			if _, err := validate(f, environment); err == nil || !strings.Contains(err.Error(), "1..100 explicit tenants") {
				t.Fatalf("validate error = %v", err)
			}
		})
	}
	f := validFile(t)
	f.Mode = "development"
	f.Hosted = hostedFixture()
	f.Hosted.Tenants[0].DestinationURL = "http://localhost:8080"
	f.Hosted.Tenants[0].CredentialEnv = "DELIVERY"
	lookup := func(name string) (string, bool) {
		if name == "DELIVERY" {
			return "fixture-delivery", true
		}
		return environment(name)
	}
	cfg, err := validate(f, lookup)
	if err != nil {
		t.Fatalf("development loopback delivery refused: %v", err)
	}
	hosted, err := cfg.HostedRuntime()
	if err != nil || hosted.DeliveryCredential(tenant.OrganizationID("org_alpha")) != "fixture-delivery" {
		t.Fatalf("development delivery credential binding = %#v, %v", hosted, err)
	}
	f.Mode = "production"
	if _, err := validate(f, lookup); err == nil || !strings.Contains(err.Error(), "explicit credential-free HTTPS destination") {
		t.Fatalf("production loopback delivery error = %v", err)
	}
	f = validFile(t)
	f.Hosted = hostedFixture()
	f.Hosted.Tenants[0].DestinationURL = "https://delivery.example.test"
	f.Hosted.Tenants[0].CredentialEnv = "DELIVERY"
	tooLong := func(name string) (string, bool) {
		if name == "DELIVERY" {
			return strings.Repeat("x", 4097), true
		}
		return environment(name)
	}
	if _, err := validate(f, tooLong); err == nil || !strings.Contains(err.Error(), "credential unavailable or invalid") {
		t.Fatalf("oversized delivery credential error = %v", err)
	}
}

func TestURLPortParsingAndPrivateSummary(t *testing.T) {
	cfg, err := Load(strings.NewReader(validConfig), environment)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DatabaseURL() == "" || cfg.ServiceSecret() == "" {
		t.Fatal("resolved infrastructure secrets missing")
	}
	address, err := (Config{databaseURL: "postgres://user:pass@[2001:db8::1]:5544/db"}).DatabaseAddress()
	if err != nil || address != "[2001:db8::1]:5544" {
		t.Fatalf("IPv6 DatabaseAddress() = %q, %v", address, err)
	}
	if _, err := url.Parse(cfg.SafeSummary()["public_base_url"].(string)); err != nil {
		t.Fatalf("safe summary URL malformed: %v", err)
	}
}
