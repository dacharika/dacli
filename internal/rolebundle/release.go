// Package rolebundle defines immutable, signed releases of dacli's existing
// team.Role model. It does not introduce a second role behavior system.
package rolebundle

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/mlnomadpy/dacli/internal/team"
)

const SchemaVersion = 1
const signatureDomain = "dacli-role-release/v1\x00"
const statusDomain = "dacli-role-lifecycle/v1\x00"

var (
	ErrInvalidRelease = errors.New("invalid role release")
	ErrInvalidStatus  = errors.New("invalid role lifecycle status")
	semverPattern     = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-(?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)(?:\.(?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*))*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)
)

// Reference pins a governed object by both stable identity and exact digest.
type Reference struct {
	Kind    string `json:"kind"`
	ID      string `json:"id"`
	Version string `json:"version"`
	Digest  string `json:"digest"`
}

// ReleaseContent is the immutable signed payload. Encoding the complete
// team.Role directly ensures Prompt and every current exported behavior field
// are committed to the digest; adding a new exported field automatically adds
// it to the JSON representation.
type ReleaseContent struct {
	SchemaVersion int         `json:"schema_version"`
	Version       string      `json:"version"`
	Role          team.Role   `json:"role"`
	References    []Reference `json:"references"`
}

// Release is a content-addressed role release signed by an enrolled publisher.
type Release struct {
	Content        ReleaseContent `json:"content"`
	ContentDigest  string         `json:"content_digest"`
	PublisherKeyID string         `json:"publisher_key_id"`
	Signature      []byte         `json:"signature"`
}

// LifecycleStatus is a publisher-signed, short-lived statement of the current
// state of one exact release. Generation must increase monotonically at store.
type LifecycleStatus struct {
	SchemaVersion        int       `json:"schema_version"`
	RoleID               string    `json:"role_id"`
	Version              string    `json:"version"`
	ContentDigest        string    `json:"content_digest"`
	Generation           uint64    `json:"generation"`
	PreviousStatusDigest string    `json:"previous_status_digest,omitempty"`
	State                string    `json:"state"` // active or revoked
	IssuedAt             time.Time `json:"issued_at"`
	ExpiresAt            time.Time `json:"expires_at"`
	PublisherKeyID       string    `json:"publisher_key_id"`
	Signature            []byte    `json:"signature"`
}

func validVersion(version string) bool { return semverPattern.MatchString(version) }

func validReference(ref Reference) bool {
	return (ref.Kind == "policy" || ref.Kind == "skill") &&
		validID(ref.ID) && validReferenceVersion(ref.Version) && validDigest(ref.Digest)
}

func validReferenceVersion(version string) bool {
	if !validID(version) || strings.EqualFold(version, "latest") || strings.ContainsAny(version, "*^~<>=|,") {
		return false
	}
	for _, part := range strings.Split(version, ".") {
		if strings.EqualFold(part, "x") {
			return false
		}
	}
	return true
}

func validID(v string) bool {
	return v != "" && len(v) <= 256 && strings.TrimSpace(v) == v && !strings.ContainsAny(v, "/\\\x00\r\n")
}

func validDigest(v string) bool {
	if len(v) != 71 || !strings.HasPrefix(v, "sha256:") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(v, "sha256:"))
	return err == nil
}

func canonicalReferences(refs []Reference) ([]Reference, error) {
	out := append([]Reference(nil), refs...)
	for _, ref := range out {
		if !validReference(ref) {
			return nil, fmt.Errorf("%w: invalid governed reference", ErrInvalidRelease)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		if out[i].ID != out[j].ID {
			return out[i].ID < out[j].ID
		}
		return out[i].Version < out[j].Version
	})
	for i := 1; i < len(out); i++ {
		if out[i-1].Kind == out[i].Kind && out[i-1].ID == out[i].ID && out[i-1].Version == out[i].Version {
			return nil, fmt.Errorf("%w: duplicate governed reference", ErrInvalidRelease)
		}
	}
	return out, nil
}

func normalizeContent(in ReleaseContent) (ReleaseContent, error) {
	if in.SchemaVersion != SchemaVersion || !validVersion(in.Version) || strings.TrimSpace(in.Role.Name) == "" {
		return ReleaseContent{}, ErrInvalidRelease
	}
	refs, err := canonicalReferences(in.References)
	if err != nil {
		return ReleaseContent{}, err
	}
	in.References = refs
	return in, nil
}

// CanonicalContent returns stable JSON bytes for the complete behavior model.
func CanonicalContent(content ReleaseContent) ([]byte, error) {
	normalized, err := normalizeContent(content)
	if err != nil {
		return nil, err
	}
	return json.Marshal(normalized)
}

func ContentDigest(content ReleaseContent) (string, error) {
	payload, err := CanonicalContent(content)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func releaseSigningBytes(content ReleaseContent, keyID string) ([]byte, string, error) {
	if !validID(keyID) {
		return nil, "", ErrInvalidRelease
	}
	digest, err := ContentDigest(content)
	if err != nil {
		return nil, "", err
	}
	return []byte(signatureDomain + keyID + "\x00" + digest), digest, nil
}

// Sign constructs a release with the supplied publisher key. It is intended
// for explicit owner/publisher tooling; imports must call Verify before cache.
func Sign(content ReleaseContent, keyID string, privateKey ed25519.PrivateKey) (Release, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return Release{}, ErrInvalidRelease
	}
	canonical, err := normalizeContent(content)
	if err != nil {
		return Release{}, err
	}
	message, digest, err := releaseSigningBytes(canonical, keyID)
	if err != nil {
		return Release{}, err
	}
	return Release{Content: canonical, ContentDigest: digest, PublisherKeyID: keyID, Signature: ed25519.Sign(privateKey, message)}, nil
}

// Verify validates strict semver, canonical references, content digest, and
// publisher signature before returning any trusted role content.
func (r Release) Verify(publishers map[string]ed25519.PublicKey) error {
	if len(r.Signature) != ed25519.SignatureSize || !validID(r.PublisherKeyID) {
		return ErrInvalidRelease
	}
	key, ok := publishers[r.PublisherKeyID]
	if !ok || len(key) != ed25519.PublicKeySize {
		return ErrInvalidRelease
	}
	message, digest, err := releaseSigningBytes(r.Content, r.PublisherKeyID)
	if err != nil || digest != r.ContentDigest || !ed25519.Verify(key, message, r.Signature) {
		return ErrInvalidRelease
	}
	canonical, err := normalizeContent(r.Content)
	if err != nil || !equalContent(canonical, r.Content) {
		return ErrInvalidRelease
	}
	return nil
}

func equalContent(a, b ReleaseContent) bool {
	x, e1 := json.Marshal(a)
	y, e2 := json.Marshal(b)
	return e1 == nil && e2 == nil && string(x) == string(y)
}

func statusSigningBytes(s LifecycleStatus) ([]byte, error) {
	if s.SchemaVersion != SchemaVersion || !validID(s.RoleID) || !validVersion(s.Version) || !validDigest(s.ContentDigest) || s.Generation == 0 || (s.State != "active" && s.State != "revoked") || s.IssuedAt.IsZero() || !s.ExpiresAt.After(s.IssuedAt) || s.ExpiresAt.After(s.IssuedAt.Add(24*time.Hour)) || !validID(s.PublisherKeyID) {
		return nil, ErrInvalidStatus
	}
	s.IssuedAt = s.IssuedAt.Round(0).UTC()
	s.ExpiresAt = s.ExpiresAt.Round(0).UTC()
	s.Signature = nil
	payload, err := json.Marshal(s)
	if err != nil {
		return nil, ErrInvalidStatus
	}
	return append([]byte(statusDomain), payload...), nil
}

// LifecycleDigest identifies one signed status in the monotonic history.
func LifecycleDigest(status LifecycleStatus) (string, error) {
	message, err := statusSigningBytes(status)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(message)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func SignStatus(status LifecycleStatus, privateKey ed25519.PrivateKey) (LifecycleStatus, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return LifecycleStatus{}, ErrInvalidStatus
	}
	status.IssuedAt = status.IssuedAt.Round(0).UTC()
	status.ExpiresAt = status.ExpiresAt.Round(0).UTC()
	message, err := statusSigningBytes(status)
	if err != nil {
		return LifecycleStatus{}, err
	}
	status.Signature = ed25519.Sign(privateKey, message)
	return status, nil
}

func (s LifecycleStatus) Verify(publishers map[string]ed25519.PublicKey, now time.Time) error {
	key, ok := publishers[s.PublisherKeyID]
	message, err := statusSigningBytes(s)
	if err != nil || !ok || len(key) != ed25519.PublicKeySize || len(s.Signature) != ed25519.SignatureSize || now.Before(s.IssuedAt) || !now.Before(s.ExpiresAt) || !ed25519.Verify(key, message, s.Signature) {
		return ErrInvalidStatus
	}
	return nil
}
