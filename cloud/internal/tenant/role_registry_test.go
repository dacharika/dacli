package tenant

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/rolebundle"
	"github.com/mlnomadpy/dacli/internal/team"
)

func hostedRoleFixture(t *testing.T) (HostedCatalog, HostedRelease, map[string]ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	catalog := HostedCatalog{Tenant: "tenant-a", ID: "catalog-a", Name: "Tenant roles", Version: 1}
	signed, err := rolebundle.Sign(rolebundle.ReleaseContent{
		SchemaVersion: rolebundle.SchemaVersion,
		Version:       "1.2.3",
		Role:          team.Role{Name: "implementer", Summary: "Build and verify", Prompt: "Implement the scoped change."},
	}, "publisher-a", private)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := json.Marshal(signed)
	if err != nil {
		t.Fatal(err)
	}
	key := ExactReleaseKey{Tenant: catalog.Tenant, CatalogID: catalog.ID, RoleID: signed.Content.Role.Name, Version: signed.Content.Version, ContentDigest: signed.ContentDigest}
	return catalog, HostedRelease{Key: key, Envelope: envelope}, map[string]ed25519.PublicKey{"publisher-a": public}, private
}

func TestValidateHostedReleaseRequiresExactTenantCatalogAndEnrolledSignature(t *testing.T) {
	catalog, release, publishers, _ := hostedRoleFixture(t)
	if _, err := ValidateHostedRelease(catalog, release, publishers); err != nil {
		t.Fatalf("valid signed release rejected: %v", err)
	}
	if _, err := ValidateHostedRelease(catalog, release, map[string]ed25519.PublicKey{}); err == nil {
		t.Fatal("release signed by a key not explicitly enrolled by the caller was accepted")
	}
	foreign := release
	foreign.Key.Tenant = "tenant-b"
	if _, err := ValidateHostedRelease(catalog, foreign, publishers); err == nil {
		t.Fatal("release key from another tenant was accepted")
	}
	wrongDigest := release
	wrongDigest.Key.ContentDigest = "sha256:" + strings.Repeat("0", 64)
	if _, err := ValidateHostedRelease(catalog, wrongDigest, publishers); err == nil {
		t.Fatal("release whose exact version key has a different digest was accepted")
	}
	var decoded rolebundle.Release
	if err := json.Unmarshal(release.Envelope, &decoded); err != nil {
		t.Fatal(err)
	}
	pretty, err := json.MarshalIndent(decoded, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	pretty = append(pretty, '\n')
	formatted := release
	formatted.Envelope = pretty
	if _, err := ValidateHostedRelease(catalog, formatted, publishers); err != nil {
		t.Fatalf("valid pretty-printed exact envelope rejected: %v", err)
	}
	if !bytes.Equal(formatted.Envelope, pretty) {
		t.Fatal("validation rewrote the exact hosted release bytes")
	}
	unknown := map[string]any{}
	if err := json.Unmarshal(release.Envelope, &unknown); err != nil {
		t.Fatal(err)
	}
	unknown["unrecognized"] = true
	unknownBytes, err := json.Marshal(unknown)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateHostedRelease(catalog, HostedRelease{Key: release.Key, Envelope: unknownBytes}, publishers); err == nil {
		t.Fatal("release envelope with unknown field was accepted")
	}
	if _, err := ValidateHostedRelease(catalog, HostedRelease{Key: release.Key, Envelope: append(append([]byte(nil), release.Envelope...), []byte(` {}`)...)}, publishers); err == nil {
		t.Fatal("release envelope with trailing JSON was accepted")
	}
	tampered := decoded
	tampered.Signature = append([]byte(nil), decoded.Signature...)
	tampered.Signature[0] ^= 1
	tamperedBytes, err := json.Marshal(tampered)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateHostedRelease(catalog, HostedRelease{Key: release.Key, Envelope: tamperedBytes}, publishers); err == nil {
		t.Fatal("release with tampered publisher signature was accepted")
	}
	oversized := append(bytes.Repeat([]byte(" "), MaxHostedRoleEnvelopeBytes), release.Envelope...)
	if _, err := ValidateHostedRelease(catalog, HostedRelease{Key: release.Key, Envelope: oversized}, publishers); err == nil {
		t.Fatal("oversized role envelope was accepted")
	}
}

func TestHostedLifecycleRequiresFreshSignedChainAndTerminalRevocation(t *testing.T) {
	catalog, release, publishers, private := hostedRoleFixture(t)
	now := time.Now().UTC().Truncate(time.Second)
	key := release.Key
	draft := HostedLifecycleEntry{Key: key, Sequence: 1, State: HostedDraft, Actor: "account-a", Reason: "draft", RecordedAt: now}
	if err := ValidateHostedLifecycleEntry(draft, nil, nil, publishers, now); err != nil {
		t.Fatalf("unsigned draft rejected: %v", err)
	}
	active := signHostedStatus(t, key, 1, "", "active", now, private)
	released := HostedLifecycleEntry{Key: key, Sequence: 2, State: HostedReleased, Status: &active, Actor: "account-a", Reason: "publish", RecordedAt: now}
	if err := ValidateHostedLifecycleEntry(released, &draft, nil, publishers, now); err != nil {
		t.Fatalf("valid released transition rejected: %v", err)
	}
	nonIncreasing := released
	nonIncreasing.Sequence = released.Sequence
	nextStatus := signHostedStatus(t, key, 2, statusDigest(t, active), "active", now, private)
	nonIncreasing.Status = &nextStatus
	if err := ValidateHostedLifecycleEntry(nonIncreasing, &released, &active, publishers, now); err == nil {
		t.Fatal("non-increasing role-wide hosted sequence was accepted")
	}
	badChain := released
	badChain.Sequence++
	status := signHostedStatus(t, key, 3, "", "active", now, private)
	badChain.Status = &status
	if err := ValidateHostedLifecycleEntry(badChain, &released, &active, publishers, now); err == nil {
		t.Fatal("signed generation with a missing predecessor digest was accepted")
	}
	revokedStatus := signHostedStatus(t, key, 2, statusDigest(t, active), "revoked", now, private)
	revoked := HostedLifecycleEntry{Key: key, Sequence: 3, State: HostedRevoked, Status: &revokedStatus, Actor: "account-a", Reason: "revoke", RecordedAt: now}
	if err := ValidateHostedLifecycleEntry(revoked, &released, &active, publishers, now); err != nil {
		t.Fatalf("valid revocation rejected: %v", err)
	}
	if err := ValidateHostedLifecycleEntry(released, &revoked, &revokedStatus, publishers, now); err == nil {
		t.Fatal("revoked release was allowed to return to released")
	}
	if err := ValidateHostedLifecycleEntry(HostedLifecycleEntry{Key: key, Sequence: 3, State: HostedReleased, Actor: "account-a", Reason: "publish", RecordedAt: now}, &draft, nil, publishers, now); err == nil {
		t.Fatal("published lifecycle state without fresh signed status was accepted")
	}
	if catalog.ID == "" {
		t.Fatal("fixture catalog unexpectedly empty")
	}
}

func TestRolePinRejectsForeignScopeAndResolvesMostSpecificExactPin(t *testing.T) {
	catalog, release, publishers, _ := hostedRoleFixture(t)
	organization := PinTarget{Tenant: "tenant-a", Kind: PinOrganization, ScopeID: "tenant-a", OrganizationID: "tenant-a"}
	project := PinTarget{Tenant: "tenant-a", Kind: PinProject, ScopeID: "project-a", ProjectID: "project-a"}
	orgPin := RolePin{Tenant: "tenant-a", Target: organization, RoleID: release.Key.RoleID, CatalogID: catalog.ID, Version: release.Key.Version, ContentDigest: release.Key.ContentDigest, Sequence: 1, Generation: 1, Actor: "account-a", Reason: "organization default", PinnedAt: time.Now().UTC()}
	projectPin := orgPin
	projectPin.Target = project
	projectPin.Sequence = 1
	projectPin.Generation = 1
	projectPin.Reason = "project override"
	if err := ValidateRolePin(Scope{Organization: "tenant-a"}, catalog, projectPin, release); err != nil {
		t.Fatalf("valid exact project pin rejected: %v", err)
	}
	foreign := projectPin
	foreign.Tenant = "tenant-b"
	if err := ValidateRolePin(Scope{Organization: "tenant-a"}, catalog, foreign, release); err == nil {
		t.Fatal("pin from another tenant was accepted")
	}
	status := HostedLifecycleEntry{Key: release.Key, Sequence: 1, State: HostedReleased, Actor: "account-a", Reason: "publish", RecordedAt: time.Now().UTC()}
	resolved, err := ResolveRole(PinScope{Tenant: "tenant-a", OrganizationID: "tenant-a", ProjectID: "project-a"}, release.Key.RoleID,
		[]RolePin{orgPin, projectPin}, map[ExactReleaseKey]HostedRelease{release.Key: release}, map[ExactReleaseKey]HostedLifecycleEntry{release.Key: status}, publishers)
	if err != nil {
		t.Fatalf("valid exact pins failed resolution: %v", err)
	}
	if resolved.Source != project || resolved.Pin.Reason != "project override" {
		t.Fatalf("resolution provenance = %#v / %q, want project override", resolved.Source, resolved.Pin.Reason)
	}
	if resolved.Lifecycle == nil || resolved.Lifecycle.Sequence != 1 {
		t.Fatalf("resolution omitted latest role lifecycle facts: %#v", resolved.Lifecycle)
	}
	malformedLatest := projectPin
	malformedLatest.Generation = 2
	if _, err := ResolveRole(PinScope{Tenant: "tenant-a", OrganizationID: "tenant-a", ProjectID: "project-a"}, release.Key.RoleID,
		[]RolePin{projectPin, malformedLatest}, map[ExactReleaseKey]HostedRelease{release.Key: release}, nil, publishers); err == nil {
		t.Fatal("malformed latest pin generation was ignored")
	}
	if _, err := ResolveRole(PinScope{Tenant: "tenant-a", OrganizationID: "tenant-a"}, "missing", []RolePin{orgPin}, nil, nil, publishers); err == nil {
		t.Fatal("resolution without an explicit pin for the requested role succeeded")
	}
}

func TestHostedLifecycleSignedGenerationContinuesAcrossReleaseVersions(t *testing.T) {
	catalog, release, publishers, private := hostedRoleFixture(t)
	now := time.Now().UTC().Truncate(time.Second)
	previous := signHostedStatus(t, release.Key, 4, "sha256:"+strings.Repeat("a", 64), "active", now, private)
	newKey := release.Key
	newKey.Version = "1.2.4"
	newKey.ContentDigest = "sha256:" + strings.Repeat("b", 64)
	current := signHostedStatus(t, newKey, 5, statusDigest(t, previous), "active", now, private)
	entry := HostedLifecycleEntry{Key: newKey, Sequence: 8, State: HostedReleased, Status: &current, Actor: "account-a", Reason: "release 1.2.4", RecordedAt: now}
	if err := ValidateHostedLifecycleEntry(entry, nil, &previous, publishers, now); err != nil {
		t.Fatalf("valid role-wide signed generation across versions rejected: %v", err)
	}
	if catalog.ID == "" {
		t.Fatal("fixture catalog unexpectedly empty")
	}
}

func TestRevokedExactReleaseDoesNotBlockActiveNewVersion(t *testing.T) {
	_, release, publishers, private := hostedRoleFixture(t)
	now := time.Now().UTC().Truncate(time.Second)
	oldRevoked := signHostedStatus(t, release.Key, 6, "sha256:"+strings.Repeat("c", 64), "revoked", now, private)
	newKey := release.Key
	newKey.Version = "1.2.4"
	newKey.ContentDigest = "sha256:" + strings.Repeat("d", 64)
	newActive := signHostedStatus(t, newKey, 7, statusDigest(t, oldRevoked), "active", now, private)
	entry := HostedLifecycleEntry{Key: newKey, Sequence: 7, State: HostedReleased, Status: &newActive, Actor: "account-a", Reason: "publish new version", RecordedAt: now}
	if err := ValidateHostedLifecycleEntry(entry, nil, &oldRevoked, publishers, now); err != nil {
		t.Fatalf("revocation of prior exact version blocked a new active version: %v", err)
	}
	sameVersionActive := signHostedStatus(t, release.Key, 7, statusDigest(t, oldRevoked), "active", now, private)
	if err := ValidateHostedLifecycleEntry(HostedLifecycleEntry{Key: release.Key, Sequence: 8, State: HostedReleased, Status: &sameVersionActive, Actor: "account-a", Reason: "republish", RecordedAt: now}, nil, &oldRevoked, publishers, now); err == nil {
		t.Fatal("revoked exact version was reactivated when its prior hosted row was omitted")
	}
}

func signHostedStatus(t *testing.T, key ExactReleaseKey, generation uint64, previous, state string, now time.Time, private ed25519.PrivateKey) rolebundle.LifecycleStatus {
	t.Helper()
	status, err := rolebundle.SignStatus(rolebundle.LifecycleStatus{
		SchemaVersion: rolebundle.SchemaVersion,
		RoleID:        key.RoleID, Version: key.Version, ContentDigest: key.ContentDigest,
		Generation: generation, PreviousStatusDigest: previous, State: state,
		IssuedAt: now, ExpiresAt: now.Add(time.Hour), PublisherKeyID: "publisher-a",
	}, private)
	if err != nil {
		t.Fatal(err)
	}
	return status
}

func statusDigest(t *testing.T, status rolebundle.LifecycleStatus) string {
	t.Helper()
	digest, err := rolebundle.LifecycleDigest(status)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}
