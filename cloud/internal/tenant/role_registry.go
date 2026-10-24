package tenant

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/mlnomadpy/dacli/internal/rolebundle"
)

// ErrInvalidRoleRegistry identifies malformed or untrusted hosted role data.
var ErrInvalidRoleRegistry = errors.New("invalid hosted role registry object")

// MaxHostedRoleEnvelopeBytes bounds the exact signed JSON envelope retained by the hosted registry.
const MaxHostedRoleEnvelopeBytes = 1 << 20

// HostedCatalog is immutable tenant metadata for one explicitly scoped role collection.
type HostedCatalog struct {
	Tenant  OrganizationID
	ID      string
	Team    TeamID
	Name    string
	Version uint64
}

// HostedPublisherKey is a publisher key explicitly enrolled for one tenant catalog.
type HostedPublisherKey struct {
	Tenant     OrganizationID
	CatalogID  string
	KeyID      string
	PublicKey  ed25519.PublicKey
	EnrolledAt time.Time
}

// ExactReleaseKey identifies an immutable release by its tenant, catalog,
// role, version, and digest. The digest supplements the immutable version key.
type ExactReleaseKey struct {
	Tenant        OrganizationID
	CatalogID     string
	RoleID        string
	Version       string
	ContentDigest string
}

// HostedRelease preserves the exact serialized signed envelope received from
// the publisher. A semantically equivalent rewrite is not the stored object.
type HostedRelease struct {
	Key      ExactReleaseKey
	Envelope []byte
}

// HostedLifecycleState is the hosted lifecycle projection for an exact release.
type HostedLifecycleState string

const (
	HostedDraft      HostedLifecycleState = "draft"
	HostedReleased   HostedLifecycleState = "released"
	HostedDeprecated HostedLifecycleState = "deprecated"
	HostedRevoked    HostedLifecycleState = "revoked"
)

// HostedLifecycleEntry is append-only hosted state. Sequence is independent
// of the publisher-signed status Generation.
type HostedLifecycleEntry struct {
	Key        ExactReleaseKey
	Sequence   uint64
	State      HostedLifecycleState
	Status     *rolebundle.LifecycleStatus
	Actor      AccountID
	Reason     string
	RecordedAt time.Time
}

type PinTargetKind string

const (
	PinOrganization PinTargetKind = "organization"
	PinTeam         PinTargetKind = "team"
	PinProject      PinTargetKind = "project"
)

// PinTarget records which tenant-owned scope supplied an exact role pin.
type PinTarget struct {
	Tenant         OrganizationID
	Kind           PinTargetKind
	ScopeID        string
	OrganizationID OrganizationID
	TeamID         TeamID
	ProjectID      ProjectID
}

// PinScope carries tenant-owned ancestors used for deterministic pin lookup.
type PinScope struct {
	Tenant         OrganizationID
	OrganizationID OrganizationID
	TeamID         TeamID
	ProjectID      ProjectID
}

// RolePin is one append-only selection generation. Previous is nil only for
// the first pin; repositories compare Generation while holding their lock.
type RolePin struct {
	Tenant        OrganizationID
	Target        PinTarget
	RoleID        string
	CatalogID     string
	Version       string
	ContentDigest string
	Sequence      uint64
	Generation    uint64
	Previous      *ExactReleaseKey
	Actor         AccountID
	Reason        string
	PinnedAt      time.Time
}

type ResolvedRole struct {
	Release   rolebundle.Release
	Source    PinTarget
	Pin       RolePin
	Lifecycle *HostedLifecycleEntry
}

// HostedRoleExport preserves the exact selected release and its signed history.
type HostedRoleExport struct {
	Selected  ExactReleaseKey
	Releases  []HostedRelease
	Lifecycle []HostedLifecycleEntry
}

// ValidateHostedCatalog checks stable tenant identity and immutable version-one metadata.
func ValidateHostedCatalog(value HostedCatalog) error {
	if !validID(string(value.Tenant)) || !validID(value.ID) || value.Name == "" || len(value.Name) > 128 ||
		value.Name != strings.TrimSpace(value.Name) || value.Version != 1 ||
		(value.Team != "" && !validID(string(value.Team))) {
		return fmt.Errorf("%w: catalog identity, name, team, or version is invalid", ErrInvalidRoleRegistry)
	}
	return nil
}

// ValidateHostedPublisherKey checks the explicit enrollment tuple and Ed25519 key size.
func ValidateHostedPublisherKey(catalog HostedCatalog, value HostedPublisherKey) error {
	if ValidateHostedCatalog(catalog) != nil || value.Tenant != catalog.Tenant || value.CatalogID != catalog.ID ||
		!validID(value.KeyID) || len(value.PublicKey) != ed25519.PublicKeySize || value.EnrolledAt.IsZero() {
		return fmt.Errorf("%w: publisher key must be explicitly enrolled in its tenant catalog", ErrInvalidRoleRegistry)
	}
	return nil
}

// ValidateHostedRelease verifies the exact release key and its signature against enrolled keys.
func ValidateHostedRelease(catalog HostedCatalog, value HostedRelease, publishers map[string]ed25519.PublicKey) (rolebundle.Release, error) {
	if ValidateHostedCatalog(catalog) != nil || value.Key.Tenant != catalog.Tenant || value.Key.CatalogID != catalog.ID || !validExactReleaseKey(value.Key) || len(value.Envelope) == 0 || len(value.Envelope) > MaxHostedRoleEnvelopeBytes {
		return rolebundle.Release{}, fmt.Errorf("%w: release tenant or catalog mismatch", ErrInvalidRoleRegistry)
	}
	release, err := decodeHostedRoleEnvelope(value.Envelope)
	if err != nil {
		return rolebundle.Release{}, err
	}
	if release.Verify(publishers) != nil || release.Content.Role.Name != value.Key.RoleID || release.Content.Version != value.Key.Version || release.ContentDigest != value.Key.ContentDigest {
		return rolebundle.Release{}, fmt.Errorf("%w: signed release does not match its exact key or enrolled publisher", ErrInvalidRoleRegistry)
	}
	return release, nil
}

// ValidateHostedLifecycleEntry checks exact-release hosted transitions and the role-wide signed status chain.
func ValidateHostedLifecycleEntry(value HostedLifecycleEntry, previous *HostedLifecycleEntry, previousStatus *rolebundle.LifecycleStatus, publishers map[string]ed25519.PublicKey, now time.Time) error {
	if !validExactReleaseKey(value.Key) || value.Sequence == 0 || value.RecordedAt.IsZero() || value.RecordedAt.After(now.Add(time.Minute)) ||
		!validID(string(value.Actor)) || value.Reason == "" || len(value.Reason) > 1024 || value.Reason != strings.TrimSpace(value.Reason) {
		return fmt.Errorf("%w: lifecycle identity, sequence, or timestamp is invalid", ErrInvalidRoleRegistry)
	}
	if previous != nil {
		if previous.Key != value.Key || previous.Sequence == 0 || value.Sequence <= previous.Sequence || previous.State == HostedRevoked || previous.State != HostedDraft && previous.Status == nil {
			return fmt.Errorf("%w: lifecycle cannot change release identity or follow revocation", ErrInvalidRoleRegistry)
		}
	}
	if previous != nil && previous.Status != nil && (previousStatus == nil || previousStatus.Generation < previous.Status.Generation ||
		previousStatus.Generation == previous.Status.Generation && statusDigestMismatch(previous.Status, previousStatus)) {
		return fmt.Errorf("%w: role-wide predecessor omits an already recorded signed status", ErrInvalidRoleRegistry)
	}
	switch value.State {
	case HostedDraft:
		if value.Status != nil || previous != nil && previous.State != HostedDraft {
			return fmt.Errorf("%w: draft state cannot carry a signed status or follow publication", ErrInvalidRoleRegistry)
		}
		return nil
	case HostedReleased, HostedDeprecated, HostedRevoked:
	default:
		return fmt.Errorf("%w: unknown hosted lifecycle state", ErrInvalidRoleRegistry)
	}
	if value.State == HostedDeprecated && previous == nil {
		return fmt.Errorf("%w: a release must exist before deprecation", ErrInvalidRoleRegistry)
	}
	if value.Status == nil {
		return fmt.Errorf("%w: published lifecycle state requires a signed status", ErrInvalidRoleRegistry)
	}
	if previousStatus != nil && (previousStatus.RoleID != value.Key.RoleID || previousStatus.Generation == 0 ||
		previousStatus.State == "revoked" && previousStatus.Version == value.Key.Version && previousStatus.ContentDigest == value.Key.ContentDigest) {
		return fmt.Errorf("%w: prior role-wide signed status cannot authorize another lifecycle entry", ErrInvalidRoleRegistry)
	}
	status := value.Status
	wantState := "active"
	if value.State == HostedRevoked {
		wantState = "revoked"
	}
	if status.RoleID != value.Key.RoleID || status.Version != value.Key.Version || status.ContentDigest != value.Key.ContentDigest || status.State != wantState || status.Verify(publishers, now) != nil {
		return fmt.Errorf("%w: signed lifecycle status is stale, mismatched, or invalid", ErrInvalidRoleRegistry)
	}
	wantGeneration := uint64(1)
	wantPrevious := ""
	if previousStatus != nil {
		wantGeneration = previousStatus.Generation + 1
		digest, err := rolebundle.LifecycleDigest(*previousStatus)
		if err != nil {
			return fmt.Errorf("%w: prior signed status is invalid", ErrInvalidRoleRegistry)
		}
		wantPrevious = digest
	}
	if status.Generation != wantGeneration || status.PreviousStatusDigest != wantPrevious {
		return fmt.Errorf("%w: signed lifecycle chain has a generation or predecessor gap", ErrInvalidRoleRegistry)
	}
	if previous != nil && !validLifecycleTransition(previous.State, value.State) {
		return fmt.Errorf("%w: hosted lifecycle transition is not allowed", ErrInvalidRoleRegistry)
	}
	return nil
}

func validLifecycleTransition(from, to HostedLifecycleState) bool {
	switch from {
	case HostedDraft:
		return to == HostedReleased || to == HostedRevoked
	case HostedReleased:
		return to == HostedReleased || to == HostedDeprecated || to == HostedRevoked
	case HostedDeprecated:
		return to == HostedDeprecated || to == HostedReleased || to == HostedRevoked
	default:
		return false
	}
}

// ValidateRolePin checks tenant scope and the immutable exact release selection.
func ValidateRolePin(scope Scope, catalog HostedCatalog, value RolePin, release HostedRelease) error {
	if ValidateHostedCatalog(catalog) != nil || !validID(string(scope.Organization)) || value.Tenant != scope.Organization || value.Tenant != catalog.Tenant ||
		value.Target.Tenant != value.Tenant || !validPinTarget(value.Target) || value.RoleID == "" || value.CatalogID != catalog.ID ||
		value.Sequence == 0 || value.Generation == 0 || !validID(string(value.Actor)) || value.Reason == "" || len(value.Reason) > 1024 ||
		value.Reason != strings.TrimSpace(value.Reason) || value.PinnedAt.IsZero() {
		return fmt.Errorf("%w: pin identity, scope, actor, reason, or generation is invalid", ErrInvalidRoleRegistry)
	}
	key := ExactReleaseKey{Tenant: value.Tenant, CatalogID: value.CatalogID, RoleID: value.RoleID, Version: value.Version, ContentDigest: value.ContentDigest}
	if key != release.Key || ValidateRoleReleaseReference(catalog, release) != nil {
		return fmt.Errorf("%w: pin must name the exact verified release envelope", ErrInvalidRoleRegistry)
	}
	if value.Target.Kind == PinTeam && catalog.Team != "" && value.Target.TeamID != catalog.Team {
		return fmt.Errorf("%w: team-scoped catalog cannot be pinned to another team", ErrInvalidRoleRegistry)
	}
	if (value.Generation == 1) != (value.Previous == nil) || value.Previous != nil && (value.Previous.Tenant != value.Tenant || value.Previous.RoleID != value.RoleID || !validExactReleaseKey(*value.Previous)) {
		return fmt.Errorf("%w: previous pin must name a valid same-tenant role release", ErrInvalidRoleRegistry)
	}
	return nil
}

func ValidateRoleReleaseReference(catalog HostedCatalog, release HostedRelease) error {
	if release.Key.Tenant != catalog.Tenant || release.Key.CatalogID != catalog.ID || !validExactReleaseKey(release.Key) {
		return ErrInvalidRoleRegistry
	}
	envelope, err := decodeHostedRoleEnvelope(release.Envelope)
	if err != nil || envelope.Content.Role.Name != release.Key.RoleID || envelope.Content.Version != release.Key.Version || envelope.ContentDigest != release.Key.ContentDigest {
		return ErrInvalidRoleRegistry
	}
	digest, err := rolebundle.ContentDigest(envelope.Content)
	if err != nil || digest != release.Key.ContentDigest {
		return ErrInvalidRoleRegistry
	}
	return nil
}

func decodeHostedRoleEnvelope(raw []byte) (rolebundle.Release, error) {
	if len(raw) == 0 || len(raw) > MaxHostedRoleEnvelopeBytes {
		return rolebundle.Release{}, fmt.Errorf("%w: release envelope size is outside 1..%d bytes", ErrInvalidRoleRegistry, MaxHostedRoleEnvelopeBytes)
	}
	var release rolebundle.Release
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&release); err != nil {
		return rolebundle.Release{}, fmt.Errorf("%w: malformed signed release envelope: %w", ErrInvalidRoleRegistry, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return rolebundle.Release{}, fmt.Errorf("%w: trailing release envelope data", ErrInvalidRoleRegistry)
	}
	return release, nil
}

// ResolveRole applies project, team, then organization precedence and returns exact provenance.
func ResolveRole(scope PinScope, roleID string, pins []RolePin, releases map[ExactReleaseKey]HostedRelease, lifecycle map[ExactReleaseKey]HostedLifecycleEntry, publishers map[string]ed25519.PublicKey) (ResolvedRole, error) {
	if !validPinScope(scope) || !validID(roleID) {
		return ResolvedRole{}, fmt.Errorf("%w: resolution scope or role is invalid", ErrInvalidRoleRegistry)
	}
	for _, target := range resolutionTargets(scope) {
		var selected *RolePin
		for i := range pins {
			pin := &pins[i]
			if pin.Tenant != scope.Tenant || pin.RoleID != roleID || pin.Target != target {
				continue
			}
			if selected == nil || pin.Generation > selected.Generation {
				selected = pin
			} else if pin.Generation == selected.Generation {
				return ResolvedRole{}, fmt.Errorf("%w: ambiguous pin generation", ErrInvalidRoleRegistry)
			}
		}
		if selected == nil {
			continue
		}
		key := ExactReleaseKey{Tenant: selected.Tenant, CatalogID: selected.CatalogID, RoleID: selected.RoleID, Version: selected.Version, ContentDigest: selected.ContentDigest}
		release, ok := releases[key]
		if !ok {
			return ResolvedRole{}, fmt.Errorf("%w: exact pinned release is absent", ErrInvalidRoleRegistry)
		}
		catalog := HostedCatalog{Tenant: key.Tenant, ID: key.CatalogID, Version: 1, Name: "resolved"}
		verified, err := ValidateHostedRelease(catalog, release, publishers)
		if err != nil {
			return ResolvedRole{}, err
		}
		if err := ValidateRolePin(Scope{Organization: scope.Tenant}, catalog, *selected, release); err != nil {
			return ResolvedRole{}, err
		}
		var current *HostedLifecycleEntry
		if entry, ok := lifecycle[key]; ok {
			copy := entry
			current = &copy
		}
		return ResolvedRole{Release: verified, Source: selected.Target, Pin: *selected, Lifecycle: current}, nil
	}
	return ResolvedRole{}, fmt.Errorf("%w: no explicit pin for role", ErrInvalidRoleRegistry)
}

func statusDigestMismatch(a, b *rolebundle.LifecycleStatus) bool {
	da, ea := rolebundle.LifecycleDigest(*a)
	db, eb := rolebundle.LifecycleDigest(*b)
	return ea != nil || eb != nil || da != db
}

func validPinTarget(value PinTarget) bool {
	if !validID(string(value.Tenant)) || !validID(value.ScopeID) {
		return false
	}
	switch value.Kind {
	case PinOrganization:
		return value.ScopeID == string(value.Tenant) && value.OrganizationID == value.Tenant && value.TeamID == "" && value.ProjectID == ""
	case PinTeam:
		return value.TeamID != "" && value.ScopeID == string(value.TeamID) && value.OrganizationID == "" && value.ProjectID == "" && validID(string(value.TeamID))
	case PinProject:
		return value.ProjectID != "" && value.ScopeID == string(value.ProjectID) && value.OrganizationID == "" && value.TeamID == "" && validID(string(value.ProjectID))
	default:
		return false
	}
}

func validPinScope(value PinScope) bool {
	if !validID(string(value.Tenant)) || value.OrganizationID != value.Tenant {
		return false
	}
	return (value.TeamID == "" || validID(string(value.TeamID))) && (value.ProjectID == "" || validID(string(value.ProjectID)))
}

func resolutionTargets(scope PinScope) []PinTarget {
	targets := make([]PinTarget, 0, 3)
	if scope.ProjectID != "" {
		targets = append(targets, PinTarget{Tenant: scope.Tenant, Kind: PinProject, ScopeID: string(scope.ProjectID), ProjectID: scope.ProjectID})
	}
	if scope.TeamID != "" {
		targets = append(targets, PinTarget{Tenant: scope.Tenant, Kind: PinTeam, ScopeID: string(scope.TeamID), TeamID: scope.TeamID})
	}
	targets = append(targets, PinTarget{Tenant: scope.Tenant, Kind: PinOrganization, ScopeID: string(scope.Tenant), OrganizationID: scope.Tenant})
	return targets
}

func validExactReleaseKey(value ExactReleaseKey) bool {
	if !validID(string(value.Tenant)) || !validID(value.CatalogID) || !validID(value.RoleID) || value.ContentDigest == "" {
		return false
	}
	if value.Version == "" || strings.EqualFold(value.Version, "latest") || strings.ContainsAny(value.Version, "*^~<>=|,") || len(value.ContentDigest) != 71 || !strings.HasPrefix(value.ContentDigest, "sha256:") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value.ContentDigest, "sha256:"))
	return err == nil
}
