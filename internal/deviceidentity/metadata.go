package deviceidentity

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Metadata is the only filesystem record. Tokens and signing secrets are
// exclusively OS credentials; no credential-store locator is public output.
type Metadata struct {
	SchemaVersion  int               `json:"schema_version"`
	Provider       string            `json:"provider"`
	TenantID       string            `json:"tenant_id"`
	AccountID      string            `json:"account_id"`
	DeviceID       string            `json:"device_id"`
	Scopes         []string          `json:"scopes"`
	ExpiresAt      time.Time         `json:"expires_at"`
	LocalExpiresAt time.Time         `json:"local_expires_at"`
	SigningKeyID   string            `json:"signing_key_id"`
	TrustedKeys    map[string]string `json:"server_signing_keys"`
}

func providerBase(raw string) (string, error) {
	u, e := url.Parse(raw)
	if e != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && (u.Scheme != "http" || (u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1" && u.Hostname() != "::1"))) {
		return "", failure("invalid_provider", false, "choose an explicit HTTPS provider; HTTP is limited to loopback development")
	}
	return strings.TrimRight(u.String(), "/"), nil
}
func opaqueID(v string) bool {
	if len(v) < 1 || len(v) > 128 {
		return false
	}
	for _, c := range v {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("._:-", c)) {
			return false
		}
	}
	return true
}
func credentialKey(provider, tenant, device string) string {
	h := sha256.Sum256([]byte(provider + "\x00" + tenant + "\x00" + device))
	return hex.EncodeToString(h[:])
}
func metadataDirectory() (string, error) {
	root, e := os.UserConfigDir()
	if e != nil {
		return "", failure("metadata_unavailable", false, "repair the user configuration directory")
	}
	return filepath.Join(root, "dacli", "device-identity"), nil
}
func decodeClosed(raw []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil || d.Decode(&struct{}{}) != io.EOF {
		return errors.New("closed device document required")
	}
	return nil
}
func readMetadata(provider, tenant, device string) (Metadata, error) {
	var out Metadata
	root, e := metadataDirectory()
	if e != nil {
		return out, e
	}
	path := filepath.Join(root, credentialKey(provider, tenant, device)+".json")
	info, e := os.Lstat(path)
	if e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 65536 {
		return out, failure("not_logged_in", false, "run auth login for this exact provider and tenant")
	}
	real, e := filepath.EvalSymlinks(path)
	if e != nil || real != path {
		return out, failure("metadata_refused", false, "repair aliased device metadata")
	}
	f, e := os.Open(path)
	if e != nil {
		return out, e
	}
	defer func() { _ = f.Close() }()
	raw, e := io.ReadAll(io.LimitReader(f, 65537))
	if e != nil || len(raw) > 65536 || decodeClosed(raw, &out) != nil || out.SchemaVersion != 1 || out.Provider != provider || out.TenantID != tenant || out.DeviceID != device || !opaqueID(out.AccountID) || !opaqueID(out.SigningKeyID) {
		return Metadata{}, failure("metadata_refused", false, "repair malformed device metadata")
	}
	return out, nil
}
func saveMetadata(m Metadata) error {
	root, e := metadataDirectory()
	if e != nil {
		return e
	}
	if e = os.MkdirAll(root, 0700); e != nil {
		return failure("metadata_unavailable", false, "repair the user configuration directory")
	}
	real, e := filepath.EvalSymlinks(root)
	if e != nil || real != root {
		return failure("metadata_refused", false, "remove aliased device metadata directory")
	}
	raw, e := json.Marshal(m)
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(root, ".device-")
	if e != nil {
		return e
	}
	name := f.Name()
	defer func() { _ = os.Remove(name) }()
	if e = f.Chmod(0600); e == nil {
		_, e = f.Write(append(raw, '\n'))
	}
	if e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(name, filepath.Join(root, credentialKey(m.Provider, m.TenantID, m.DeviceID)+".json"))
}
func removeMetadata(m Metadata) error {
	root, e := metadataDirectory()
	if e != nil {
		return e
	}
	path := filepath.Join(root, credentialKey(m.Provider, m.TenantID, m.DeviceID)+".json")
	e = os.Remove(path)
	if errors.Is(e, os.ErrNotExist) {
		return nil
	}
	return e
}
