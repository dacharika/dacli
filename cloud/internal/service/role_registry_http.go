package service

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/mlnomadpy/dacli/cloud/internal/ratelimit"
	"github.com/mlnomadpy/dacli/cloud/internal/tenant"
	"github.com/mlnomadpy/dacli/cloud/internal/tenantrepo"
	"github.com/mlnomadpy/dacli/internal/rolebundle"
)

var (
	roleVersionPattern = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-(?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)(?:\.(?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*))*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)
	roleDigestPattern  = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

// RoleRegistryBackend is intentionally separate from tenantapi.Backend: the
// signed catalog has a distinct immutable-history contract.
type RoleRegistryBackend interface {
	CreateRoleCatalog(context.Context, tenant.VerifiedIdentity, tenant.Mutation, tenant.HostedCatalog) error
	ListRoleCatalogs(context.Context, tenant.VerifiedIdentity) ([]tenant.HostedCatalog, error)
	EnrollRolePublisher(context.Context, tenant.VerifiedIdentity, tenant.Mutation, string, tenant.HostedPublisherKey) error
	ImportRoleRelease(context.Context, tenant.VerifiedIdentity, tenant.Mutation, string, tenant.HostedRelease) (tenant.HostedLifecycleEntry, error)
	AppendRoleLifecycle(context.Context, tenant.VerifiedIdentity, tenant.Mutation, tenant.ExactReleaseKey, uint64, tenant.HostedLifecycleState, *rolebundle.LifecycleStatus, string) (tenant.HostedLifecycleEntry, error)
	ExportRole(context.Context, tenant.VerifiedIdentity, tenant.ExactReleaseKey) (tenant.HostedRoleExport, error)
	SetRolePin(context.Context, tenant.VerifiedIdentity, tenant.Mutation, tenant.PinTarget, string, string, string, string, uint64, string) (tenant.RolePin, error)
	ResolveRole(context.Context, tenant.VerifiedIdentity, tenant.ProjectID, string) (tenant.ResolvedRole, error)
}

type roleRegistryHTTP struct {
	api            *API
	verifier       IdentityVerifier
	backend        RoleRegistryBackend
	policyRevision uint64
}

// EnableRoleRegistryAPI installs the signed hosted role routes after runtime
// policy has been loaded. Every request is checked against that exact revision.
func (a *API) EnableRoleRegistryAPI(verifier IdentityVerifier, backend RoleRegistryBackend, policyRevision uint64) error {
	if a == nil || verifier == nil || backend == nil || policyRevision == 0 {
		return errors.New("role registry API requires verifier, backend, and policy revision")
	}
	a.roleRegistry = &roleRegistryHTTP{api: a, verifier: verifier, backend: backend, policyRevision: policyRevision}
	return nil
}

func (h *roleRegistryHTTP) handle(w http.ResponseWriter, r *http.Request) {
	identity, ok := h.identity(w, r)
	if !ok {
		return
	}
	if !noQuery(w, r, h.api) {
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/v1/role-registry")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 1 && parts[0] == "catalogs" {
		h.catalogs(w, r, identity)
		return
	}
	if len(parts) == 3 && parts[0] == "catalogs" && parts[2] == "publishers" {
		h.publisher(w, r, identity, parts[1])
		return
	}
	if len(parts) == 3 && parts[0] == "catalogs" && parts[1] != "" && parts[2] == "releases" {
		h.catalogReleases(w, r, identity, parts[1])
		return
	}
	if len(parts) == 5 && parts[0] == "releases" {
		h.release(w, r, identity, parts[1], parts[2], parts[3], parts[4])
		return
	}
	if len(parts) == 6 && parts[0] == "releases" && parts[5] == "lifecycle" {
		h.lifecycle(w, r, identity, parts[1], parts[2], parts[3], parts[4])
		return
	}
	if len(parts) == 1 && parts[0] == "pins" {
		h.pin(w, r, identity)
		return
	}
	if len(parts) == 4 && parts[0] == "projects" && parts[2] == "roles" {
		h.resolve(w, r, identity, parts[1], parts[3])
		return
	}
	h.api.writeError(w, r, http.StatusNotFound, "not_found", "endpoint does not exist", false)
}

func (h *roleRegistryHTTP) catalogs(w http.ResponseWriter, r *http.Request, identity tenant.VerifiedIdentity) {
	switch r.Method {
	case http.MethodGet:
		if !h.readAllowed(w, r, identity, "catalogs") || !noQuery(w, r, h.api) {
			return
		}
		values, err := h.backend.ListRoleCatalogs(r.Context(), identity)
		if err != nil {
			h.backendError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"schema": "hosted-role-catalogs/v1", "items": values})
	case http.MethodPost:
		if !h.writeAllowed(w, r, identity, "catalogs") {
			return
		}
		var input struct {
			ID   string `json:"catalog_id"`
			Team string `json:"team_id"`
			Name string `json:"name"`
		}
		if err := decodeClosed(r.Body, &input); err != nil {
			h.decodeError(w, r, err)
			return
		}
		var team tenant.TeamID
		if input.Team != "" {
			parsedTeam, err := tenant.NewTeamID(input.Team)
			if err != nil {
				h.invalid(w, r)
				return
			}
			team = parsedTeam
		}
		catalog := tenant.HostedCatalog{Tenant: identity.Scope.Organization, ID: input.ID, Team: team, Name: input.Name, Version: 1}
		if err := tenant.ValidateHostedCatalog(catalog); err != nil {
			h.invalid(w, r)
			return
		}
		if err := h.backend.CreateRoleCatalog(r.Context(), identity, h.mutation(r, identity), catalog); err != nil {
			h.backendError(w, r, err)
			return
		}
		writeJSON(w, http.StatusCreated, catalog)
	default:
		h.api.writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "endpoint requires GET or POST", false)
	}
}

func (h *roleRegistryHTTP) publisher(w http.ResponseWriter, r *http.Request, identity tenant.VerifiedIdentity, catalogID string) {
	if r.Method != http.MethodPost {
		h.api.writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "endpoint requires POST", false)
		return
	}
	if !h.writeAllowed(w, r, identity, "publisher") {
		return
	}
	if _, err := opaqueID(catalogID); err != nil {
		h.invalid(w, r)
		return
	}
	var input struct {
		KeyID     string            `json:"key_id"`
		PublicKey ed25519.PublicKey `json:"public_key"`
	}
	if err := decodeClosed(r.Body, &input); err != nil {
		h.decodeError(w, r, err)
		return
	}
	if _, err := opaqueID(input.KeyID); err != nil || len(input.PublicKey) != ed25519.PublicKeySize {
		h.invalid(w, r)
		return
	}
	key := tenant.HostedPublisherKey{Tenant: identity.Scope.Organization, CatalogID: catalogID, KeyID: input.KeyID, PublicKey: input.PublicKey, EnrolledAt: time.Now().UTC()}
	if err := h.backend.EnrollRolePublisher(r.Context(), identity, h.mutation(r, identity), catalogID, key); err != nil {
		h.backendError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"catalog_id": catalogID, "key_id": input.KeyID, "public_key": hex.EncodeToString(input.PublicKey)})
}

func (h *roleRegistryHTTP) catalogReleases(w http.ResponseWriter, r *http.Request, identity tenant.VerifiedIdentity, catalogID string) {
	if r.Method != http.MethodPost {
		h.api.writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "endpoint requires POST", false)
		return
	}
	if !h.writeAllowed(w, r, identity, "release-import") {
		return
	}
	if _, err := opaqueID(catalogID); err != nil {
		h.invalid(w, r)
		return
	}
	var input struct {
		RoleID        string `json:"role_id"`
		Version       string `json:"version"`
		ContentDigest string `json:"content_digest"`
		Envelope      []byte `json:"envelope"`
	}
	if err := decodeClosed(r.Body, &input); err != nil {
		h.decodeError(w, r, err)
		return
	}
	if len(input.Envelope) > tenant.MaxHostedRoleEnvelopeBytes {
		h.api.writeError(w, r, http.StatusRequestEntityTooLarge, "request_too_large", "signed release envelope exceeds the configured byte limit", false)
		return
	}
	key, err := h.exactKey(identity, catalogID, input.RoleID, input.Version, input.ContentDigest)
	if err != nil || len(input.Envelope) == 0 {
		h.invalid(w, r)
		return
	}
	entry, err := h.backend.ImportRoleRelease(r.Context(), identity, h.mutation(r, identity), catalogID, tenant.HostedRelease{Key: key, Envelope: input.Envelope})
	if err != nil {
		h.backendError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, entry)
}

func (h *roleRegistryHTTP) release(w http.ResponseWriter, r *http.Request, identity tenant.VerifiedIdentity, catalogID, roleID, version, digest string) {
	if r.Method != http.MethodGet {
		h.api.writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "endpoint requires GET", false)
		return
	}
	if !h.readAllowed(w, r, identity, "release-export") || !noQuery(w, r, h.api) {
		return
	}
	key, err := h.exactKey(identity, catalogID, roleID, version, digest)
	if err != nil {
		h.invalid(w, r)
		return
	}
	value, err := h.backend.ExportRole(r.Context(), identity, key)
	if err != nil {
		h.backendError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func (h *roleRegistryHTTP) lifecycle(w http.ResponseWriter, r *http.Request, identity tenant.VerifiedIdentity, catalogID, roleID, version, digest string) {
	if r.Method != http.MethodPost {
		h.api.writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "endpoint requires POST", false)
		return
	}
	if !h.writeAllowed(w, r, identity, "lifecycle") {
		return
	}
	key, err := h.exactKey(identity, catalogID, roleID, version, digest)
	if err != nil {
		h.invalid(w, r)
		return
	}
	var input struct {
		ExpectedSequence uint64                      `json:"expected_sequence"`
		State            tenant.HostedLifecycleState `json:"state"`
		Status           *rolebundle.LifecycleStatus `json:"status"`
		Reason           string                      `json:"reason"`
	}
	if err := decodeClosed(r.Body, &input); err != nil {
		h.decodeError(w, r, err)
		return
	}
	validState := input.State == tenant.HostedDraft || input.State == tenant.HostedReleased || input.State == tenant.HostedDeprecated || input.State == tenant.HostedRevoked
	if !validState || strings.TrimSpace(input.Reason) == "" || input.Reason != strings.TrimSpace(input.Reason) || len(input.Reason) > 1024 ||
		(input.State == tenant.HostedDraft && input.Status != nil) || (input.State != tenant.HostedDraft && input.Status == nil) {
		h.invalid(w, r)
		return
	}
	entry, err := h.backend.AppendRoleLifecycle(r.Context(), identity, h.mutation(r, identity), key, input.ExpectedSequence, input.State, input.Status, input.Reason)
	if err != nil {
		h.backendError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, entry)
}

func (h *roleRegistryHTTP) pin(w http.ResponseWriter, r *http.Request, identity tenant.VerifiedIdentity) {
	if r.Method != http.MethodPut {
		h.api.writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "endpoint requires PUT", false)
		return
	}
	if !h.writeAllowed(w, r, identity, "pin") {
		return
	}
	var input struct {
		Kind               tenant.PinTargetKind `json:"target_kind"`
		ScopeID            string               `json:"scope_id"`
		RoleID             string               `json:"role_id"`
		CatalogID          string               `json:"catalog_id"`
		Version            string               `json:"version"`
		ContentDigest      string               `json:"content_digest"`
		ExpectedGeneration uint64               `json:"expected_generation"`
		Reason             string               `json:"reason"`
	}
	if err := decodeClosed(r.Body, &input); err != nil {
		h.decodeError(w, r, err)
		return
	}
	target, err := h.pinTarget(identity, input.Kind, input.ScopeID)
	if err != nil {
		h.invalid(w, r)
		return
	}
	if _, err := h.exactKey(identity, input.CatalogID, input.RoleID, input.Version, input.ContentDigest); err != nil || strings.TrimSpace(input.Reason) == "" || input.Reason != strings.TrimSpace(input.Reason) || len(input.Reason) > 1024 {
		h.invalid(w, r)
		return
	}
	value, err := h.backend.SetRolePin(r.Context(), identity, h.mutation(r, identity), target, input.RoleID, input.CatalogID, input.Version, input.ContentDigest, input.ExpectedGeneration, input.Reason)
	if err != nil {
		h.backendError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func (h *roleRegistryHTTP) resolve(w http.ResponseWriter, r *http.Request, identity tenant.VerifiedIdentity, rawProject, roleID string) {
	if r.Method != http.MethodGet {
		h.api.writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "endpoint requires GET", false)
		return
	}
	if !h.readAllowed(w, r, identity, "resolve") || !noQuery(w, r, h.api) {
		return
	}
	project, err := tenant.NewProjectID(rawProject)
	if err != nil {
		h.invalid(w, r)
		return
	}
	if _, err := opaqueID(roleID); err != nil {
		h.invalid(w, r)
		return
	}
	value, err := h.backend.ResolveRole(r.Context(), identity, project, roleID)
	if err != nil {
		h.backendError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func (h *roleRegistryHTTP) identity(w http.ResponseWriter, r *http.Request) (tenant.VerifiedIdentity, bool) {
	if !h.api.allow(w, r, h.api.identityLimit, ratelimit.NetworkKey(h.api.limitSecret, r.RemoteAddr)) {
		return tenant.VerifiedIdentity{}, false
	}
	identity, err := h.verifier.Verify(r.Context(), r.Header.Get("Authorization"))
	if err != nil || tenant.ValidateVerifiedIdentity(identity) != nil || identity.PolicyRevision != h.policyRevision {
		h.api.writeError(w, r, http.StatusUnauthorized, "unauthorized", "verified identity with current policy is required", false)
		return tenant.VerifiedIdentity{}, false
	}
	return identity, true
}

func (h *roleRegistryHTTP) readAllowed(w http.ResponseWriter, r *http.Request, id tenant.VerifiedIdentity, kind string) bool {
	return h.api.allow(w, r, h.api.tenantReadLimit, ratelimit.IdentityKey(h.api.limitSecret, id, "role-registry:"+kind))
}
func (h *roleRegistryHTTP) writeAllowed(w http.ResponseWriter, r *http.Request, id tenant.VerifiedIdentity, kind string) bool {
	return h.readAllowed(w, r, id, "write:"+kind)
}
func (h *roleRegistryHTTP) mutation(r *http.Request, id tenant.VerifiedIdentity) tenant.Mutation {
	return tenant.Mutation{Actor: id.Account, Device: id.Device, CorrelationID: r.Header.Get("X-Request-ID"), OccurredAt: time.Now().UTC()}
}
func (h *roleRegistryHTTP) exactKey(id tenant.VerifiedIdentity, catalogID, roleID, version, digest string) (tenant.ExactReleaseKey, error) {
	if _, err := opaqueID(catalogID); err != nil {
		return tenant.ExactReleaseKey{}, err
	}
	if _, err := opaqueID(roleID); err != nil {
		return tenant.ExactReleaseKey{}, err
	}
	if !roleVersionPattern.MatchString(version) {
		return tenant.ExactReleaseKey{}, errors.New("invalid exact semantic version")
	}
	if !roleDigestPattern.MatchString(digest) {
		return tenant.ExactReleaseKey{}, errors.New("invalid exact digest")
	}
	return tenant.ExactReleaseKey{Tenant: id.Scope.Organization, CatalogID: catalogID, RoleID: roleID, Version: version, ContentDigest: digest}, nil
}
func (h *roleRegistryHTTP) pinTarget(id tenant.VerifiedIdentity, kind tenant.PinTargetKind, scopeID string) (tenant.PinTarget, error) {
	if _, err := opaqueID(scopeID); err != nil {
		return tenant.PinTarget{}, err
	}
	target := tenant.PinTarget{Tenant: id.Scope.Organization, Kind: kind, ScopeID: scopeID}
	switch kind {
	case tenant.PinOrganization:
		if string(id.Scope.Organization) != scopeID {
			return tenant.PinTarget{}, errors.New("foreign organization target")
		}
		target.OrganizationID = id.Scope.Organization
	case tenant.PinTeam:
		team, err := tenant.NewTeamID(scopeID)
		if err != nil {
			return tenant.PinTarget{}, err
		}
		target.TeamID = team
	case tenant.PinProject:
		project, err := tenant.NewProjectID(scopeID)
		if err != nil {
			return tenant.PinTarget{}, err
		}
		target.ProjectID = project
	default:
		return tenant.PinTarget{}, errors.New("unknown pin target")
	}
	return target, nil
}
func (h *roleRegistryHTTP) invalid(w http.ResponseWriter, r *http.Request) {
	h.api.writeError(w, r, http.StatusBadRequest, "invalid_request", "request data is invalid", false)
}
func (h *roleRegistryHTTP) decodeError(w http.ResponseWriter, r *http.Request, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		h.api.writeError(w, r, http.StatusRequestEntityTooLarge, "request_too_large", "request exceeds the configured byte limit", false)
		return
	}
	h.invalid(w, r)
}
func (h *roleRegistryHTTP) backendError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, tenantrepo.ErrNotFound):
		h.api.writeError(w, r, http.StatusNotFound, "resource_unavailable", "resource is unavailable", false)
	case errors.Is(err, tenantrepo.ErrConflict), errors.Is(err, tenantrepo.ErrAuditConflict):
		h.api.writeError(w, r, http.StatusConflict, "version_conflict", "resource version changed", false)
	case errors.Is(err, tenant.ErrDenied):
		h.api.writeError(w, r, http.StatusForbidden, "forbidden", "operation is not permitted", false)
	case errors.Is(err, tenant.ErrInvalidRoleRegistry):
		h.api.writeError(w, r, http.StatusBadRequest, "invalid_release", "signed role registry data is invalid", false)
	default:
		h.api.writeError(w, r, http.StatusServiceUnavailable, "dependency_unavailable", "service dependency is unavailable", true)
	}
}

func opaqueID(value string) (string, error) {
	if _, err := tenant.NewProjectID(value); err != nil {
		return "", err
	}
	return value, nil
}
func noQuery(w http.ResponseWriter, r *http.Request, api *API) bool {
	if len(r.URL.Query()) == 0 {
		return true
	}
	api.writeError(w, r, http.StatusBadRequest, "invalid_query", "query parameters are not supported", false)
	return false
}
