package service

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/cloud/internal/tenant"
	"github.com/mlnomadpy/dacli/cloud/internal/tenantrepo"
	"github.com/mlnomadpy/dacli/internal/rolebundle"
)

type roleRegistryFake struct {
	calls                 int
	err                   error
	identity              tenant.VerifiedIdentity
	catalog               tenant.HostedCatalog
	catalogs              []tenant.HostedCatalog
	mutation              tenant.Mutation
	publisherCatalog      string
	publisherKey          tenant.HostedPublisherKey
	releaseCatalog        string
	release               tenant.HostedRelease
	importEntry           tenant.HostedLifecycleEntry
	lifecycleKey          tenant.ExactReleaseKey
	lifecycleSequence     uint64
	lifecycleState        tenant.HostedLifecycleState
	lifecycleStatus       *rolebundle.LifecycleStatus
	lifecycleReason       string
	lifecycleEntry        tenant.HostedLifecycleEntry
	key                   tenant.ExactReleaseKey
	exportValue           tenant.HostedRoleExport
	pinTarget             tenant.PinTarget
	pinRoleID             string
	pinCatalogID          string
	pinVersion            string
	pinDigest             string
	pinExpectedGeneration uint64
	pinReason             string
	pinValue              tenant.RolePin
	resolveProject        tenant.ProjectID
	resolveRoleID         string
	resolveValue          tenant.ResolvedRole
}

func (f *roleRegistryFake) CreateRoleCatalog(_ context.Context, id tenant.VerifiedIdentity, m tenant.Mutation, c tenant.HostedCatalog) error {
	f.calls++
	f.identity, f.catalog, f.mutation = id, c, m
	return f.err
}
func (f *roleRegistryFake) ListRoleCatalogs(_ context.Context, id tenant.VerifiedIdentity) ([]tenant.HostedCatalog, error) {
	f.calls++
	f.identity = id
	return f.catalogs, f.err
}
func (f *roleRegistryFake) EnrollRolePublisher(_ context.Context, id tenant.VerifiedIdentity, m tenant.Mutation, catalogID string, key tenant.HostedPublisherKey) error {
	f.calls++
	f.identity, f.mutation, f.publisherCatalog, f.publisherKey = id, m, catalogID, key
	return f.err
}
func (f *roleRegistryFake) ImportRoleRelease(_ context.Context, id tenant.VerifiedIdentity, m tenant.Mutation, catalogID string, release tenant.HostedRelease) (tenant.HostedLifecycleEntry, error) {
	f.calls++
	f.identity, f.mutation, f.releaseCatalog, f.release = id, m, catalogID, release
	return f.importEntry, f.err
}
func (f *roleRegistryFake) AppendRoleLifecycle(_ context.Context, id tenant.VerifiedIdentity, m tenant.Mutation, key tenant.ExactReleaseKey, sequence uint64, state tenant.HostedLifecycleState, status *rolebundle.LifecycleStatus, reason string) (tenant.HostedLifecycleEntry, error) {
	f.calls++
	f.identity, f.mutation, f.lifecycleKey, f.lifecycleSequence, f.lifecycleState = id, m, key, sequence, state
	if status != nil {
		copy := *status
		f.lifecycleStatus = &copy
	} else {
		f.lifecycleStatus = nil
	}
	f.lifecycleReason = reason
	return f.lifecycleEntry, f.err
}
func (f *roleRegistryFake) ExportRole(_ context.Context, id tenant.VerifiedIdentity, key tenant.ExactReleaseKey) (tenant.HostedRoleExport, error) {
	f.calls++
	f.identity, f.key = id, key
	if f.exportValue.Selected == (tenant.ExactReleaseKey{}) {
		return tenant.HostedRoleExport{Selected: key, Releases: []tenant.HostedRelease{}, Lifecycle: []tenant.HostedLifecycleEntry{}}, f.err
	}
	return f.exportValue, f.err
}
func (f *roleRegistryFake) SetRolePin(_ context.Context, id tenant.VerifiedIdentity, m tenant.Mutation, target tenant.PinTarget, roleID, catalogID, version, digest string, generation uint64, reason string) (tenant.RolePin, error) {
	f.calls++
	f.identity, f.mutation, f.pinTarget = id, m, target
	f.pinRoleID, f.pinCatalogID, f.pinVersion, f.pinDigest, f.pinExpectedGeneration, f.pinReason = roleID, catalogID, version, digest, generation, reason
	return f.pinValue, f.err
}
func (f *roleRegistryFake) ResolveRole(_ context.Context, id tenant.VerifiedIdentity, project tenant.ProjectID, roleID string) (tenant.ResolvedRole, error) {
	f.calls++
	f.identity, f.resolveProject, f.resolveRoleID = id, project, roleID
	return f.resolveValue, f.err
}

func roleRegistryHandler(t *testing.T, backend *roleRegistryFake, policy uint64) (*API, string, tenant.VerifiedIdentity) {
	t.Helper()
	id, token := httpIdentity(t, "tenant-a")
	verifier, err := NewHMACIdentityVerifier(testSigningSecret)
	if err != nil {
		t.Fatal(err)
	}
	api := NewAPI(testConfig(), nil, nil)
	if err := api.EnableRoleRegistryAPI(verifier, backend, policy); err != nil {
		t.Fatal(err)
	}
	return api, token, id
}

func roleRegistryRequest(api *API, token, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("X-Request-ID", "role-correlation-1")
	w := httptest.NewRecorder()
	api.Handler().ServeHTTP(w, r)
	return w
}

func TestRoleRegistryDerivesTenantAndMutationFromVerifiedIdentity(t *testing.T) {
	backend := &roleRegistryFake{}
	api, token, id := roleRegistryHandler(t, backend, 3)
	w := roleRegistryRequest(api, token, http.MethodPost, "/v1/role-registry/catalogs", `{"catalog_id":"catalog-a","team_id":"team-a","name":"Team roles"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", w.Code, w.Body.String())
	}
	if backend.calls != 1 || backend.catalog.Tenant != id.Scope.Organization || backend.catalog.Team != "team-a" || backend.catalog.Version != 1 {
		t.Fatalf("backend catalog/calls = %+v / %d", backend.catalog, backend.calls)
	}
	if backend.mutation.Actor != id.Account || backend.mutation.Device != id.Device || backend.mutation.CorrelationID != "role-correlation-1" || backend.mutation.OccurredAt.IsZero() {
		t.Fatalf("mutation = %+v", backend.mutation)
	}
}

func TestRoleRegistryCreatesOrganizationCatalogWithoutTeam(t *testing.T) {
	backend := &roleRegistryFake{}
	api, token, id := roleRegistryHandler(t, backend, 3)
	w := roleRegistryRequest(api, token, http.MethodPost, "/v1/role-registry/catalogs", `{"catalog_id":"catalog-org","name":"Organization roles"}`)
	if w.Code != http.StatusCreated || backend.calls != 1 || backend.catalog.Tenant != id.Scope.Organization || backend.catalog.Team != "" {
		t.Fatalf("status=%d calls=%d catalog=%+v body=%s", w.Code, backend.calls, backend.catalog, w.Body.String())
	}
}

func TestRoleRegistryRejectsInvalidRequestsBeforeBackend(t *testing.T) {
	backend := &roleRegistryFake{}
	api, token, _ := roleRegistryHandler(t, backend, 3)
	for _, tc := range []struct {
		name, method, path, body string
		want                     int
	}{
		{"foreign tenant field", http.MethodPost, "/v1/role-registry/catalogs", `{"tenant":"tenant-b","catalog_id":"catalog-a","team_id":"team-a","name":"Team roles"}`, 400},
		{"unknown authority field", http.MethodPost, "/v1/role-registry/catalogs", `{"catalog_id":"catalog-a","team_id":"team-a","name":"Team roles","actor":"owner"}`, 400},
		{"trailing value", http.MethodPost, "/v1/role-registry/catalogs", `{"catalog_id":"catalog-a","team_id":"team-a","name":"Team roles"} {}`, 400},
		{"unknown query", http.MethodGet, "/v1/role-registry/catalogs?tenant=tenant-b", "", 400},
		{"duplicate mutation query", http.MethodPost, "/v1/role-registry/catalogs?team_id=team-a&team_id=team-b", `{"catalog_id":"catalog-a","name":"Roles"}`, 400},
		{"floating target", http.MethodGet, "/v1/role-registry/releases/catalog-a/role-a/latest/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "", 400},
		{"bad exact digest", http.MethodGet, "/v1/role-registry/releases/catalog-a/role-a/1/nope", "", 400},
		{"oversize", http.MethodPost, "/v1/role-registry/catalogs", strings.Repeat("x", 1100), 413},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := roleRegistryRequest(api, token, tc.method, tc.path, tc.body)
			if w.Code != tc.want {
				t.Fatalf("status=%d body=%s want=%d", w.Code, w.Body.String(), tc.want)
			}
		})
	}
	if backend.calls != 0 {
		t.Fatalf("invalid requests reached backend %d times", backend.calls)
	}
}

func TestRoleRegistryRequiresVerifiedIdentityBeforeBackend(t *testing.T) {
	backend := &roleRegistryFake{}
	api, _, _ := roleRegistryHandler(t, backend, 3)
	request := httptest.NewRequest(http.MethodGet, "/v1/role-registry/catalogs", nil)
	response := httptest.NewRecorder()
	api.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || backend.calls != 0 {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, backend.calls, response.Body.String())
	}
}

func TestRoleRegistryRequiresCurrentPolicyRevision(t *testing.T) {
	backend := &roleRegistryFake{}
	api, token, _ := roleRegistryHandler(t, backend, 4)
	w := roleRegistryRequest(api, token, http.MethodGet, "/v1/role-registry/catalogs", "")
	if w.Code != http.StatusUnauthorized || backend.calls != 0 {
		t.Fatalf("status=%d calls=%d body=%s", w.Code, backend.calls, w.Body.String())
	}
}

func TestRoleRegistryMapsAuditCorrelationConflictToConflict(t *testing.T) {
	backend := &roleRegistryFake{err: tenantrepo.ErrAuditConflict}
	api, token, _ := roleRegistryHandler(t, backend, 3)
	w := roleRegistryRequest(api, token, http.MethodPost, "/v1/role-registry/catalogs", `{"catalog_id":"catalog-a","team_id":"team-a","name":"Team roles"}`)
	if w.Code != http.StatusConflict || backend.calls != 1 {
		t.Fatalf("status=%d calls=%d body=%s", w.Code, backend.calls, w.Body.String())
	}
}

func TestRoleRegistryMapsBackendAuthorizationRefusal(t *testing.T) {
	backend := &roleRegistryFake{err: tenant.ErrDenied}
	api, token, _ := roleRegistryHandler(t, backend, 3)
	w := roleRegistryRequest(api, token, http.MethodPost, "/v1/role-registry/catalogs", `{"catalog_id":"catalog-a","team_id":"team-a","name":"Team roles"}`)
	if w.Code != http.StatusForbidden || backend.calls != 1 {
		t.Fatalf("status=%d calls=%d body=%s", w.Code, backend.calls, w.Body.String())
	}
}

func TestRoleRegistryUsesExactReleaseIdentityAndPreservesExportShape(t *testing.T) {
	backend := &roleRegistryFake{}
	api, token, id := roleRegistryHandler(t, backend, 3)
	digest := "sha256:" + strings.Repeat("a", 64)
	w := roleRegistryRequest(api, token, http.MethodGet, "/v1/role-registry/releases/catalog-a/role-a/v1.2.3/"+digest, "")
	if w.Code != http.StatusOK || backend.calls != 1 {
		t.Fatalf("status=%d calls=%d body=%s", w.Code, backend.calls, w.Body.String())
	}
	if backend.key.Tenant != id.Scope.Organization || backend.key.Version != "v1.2.3" || backend.key.ContentDigest != digest {
		t.Fatalf("key=%+v", backend.key)
	}
	var export tenant.HostedRoleExport
	if err := json.Unmarshal(w.Body.Bytes(), &export); err != nil || export.Selected != backend.key {
		t.Fatalf("export=%+v error=%v", export, err)
	}
}

func TestRoleRegistrySuccessfulRoutesPreserveTypedInputs(t *testing.T) {
	backend := &roleRegistryFake{}
	api, token, identity := roleRegistryHandler(t, backend, 3)
	backend.catalogs = []tenant.HostedCatalog{{Tenant: identity.Scope.Organization, ID: "catalog-a", Name: "Roles", Version: 1}}
	list := roleRegistryRequest(api, token, http.MethodGet, "/v1/role-registry/catalogs", "")
	var listed struct {
		Schema string                 `json:"schema"`
		Items  []tenant.HostedCatalog `json:"items"`
	}
	if list.Code != http.StatusOK || json.Unmarshal(list.Body.Bytes(), &listed) != nil || listed.Schema != "hosted-role-catalogs/v1" || !reflect.DeepEqual(listed.Items, backend.catalogs) {
		t.Fatalf("catalog list status=%d value=%+v body=%s", list.Code, listed, list.Body.String())
	}

	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	publisherBody, _ := json.Marshal(struct {
		KeyID     string            `json:"key_id"`
		PublicKey ed25519.PublicKey `json:"public_key"`
	}{"publisher-a", publicKey})
	publisher := roleRegistryRequest(api, token, http.MethodPost, "/v1/role-registry/catalogs/catalog-a/publishers", string(publisherBody))
	if publisher.Code != http.StatusCreated || backend.publisherCatalog != "catalog-a" || backend.publisherKey.Tenant != identity.Scope.Organization || backend.publisherKey.CatalogID != "catalog-a" || backend.publisherKey.KeyID != "publisher-a" || !bytes.Equal(backend.publisherKey.PublicKey, publicKey) || backend.publisherKey.EnrolledAt.IsZero() || backend.mutation.Actor != identity.Account || backend.mutation.Device != identity.Device {
		t.Fatalf("publisher status=%d key=%+v mutation=%+v body=%s", publisher.Code, backend.publisherKey, backend.mutation, publisher.Body.String())
	}
	var publisherOutput struct {
		CatalogID string `json:"catalog_id"`
		KeyID     string `json:"key_id"`
		PublicKey string `json:"public_key"`
	}
	if err := json.Unmarshal(publisher.Body.Bytes(), &publisherOutput); err != nil || publisherOutput.CatalogID != "catalog-a" || publisherOutput.KeyID != "publisher-a" || len(publisherOutput.PublicKey) != ed25519.PublicKeySize*2 {
		t.Fatalf("publisher output=%+v error=%v", publisherOutput, err)
	}

	digest := "sha256:" + strings.Repeat("b", 64)
	envelope := []byte("{\n  \"content\": { \"signed\": true }\n}\n")
	importBody, _ := json.Marshal(struct {
		RoleID        string `json:"role_id"`
		Version       string `json:"version"`
		ContentDigest string `json:"content_digest"`
		Envelope      []byte `json:"envelope"`
	}{"role-a", "v1.2.3", digest, envelope})
	key := tenant.ExactReleaseKey{Tenant: identity.Scope.Organization, CatalogID: "catalog-a", RoleID: "role-a", Version: "v1.2.3", ContentDigest: digest}
	backend.importEntry = tenant.HostedLifecycleEntry{Key: key, Sequence: 1, State: tenant.HostedDraft, Actor: identity.Account, Reason: "imported", RecordedAt: time.Now().UTC()}
	imported := roleRegistryRequest(api, token, http.MethodPost, "/v1/role-registry/catalogs/catalog-a/releases", string(importBody))
	if imported.Code != http.StatusCreated || backend.releaseCatalog != "catalog-a" || backend.release.Key != key || !bytes.Equal(backend.release.Envelope, envelope) || backend.mutation.CorrelationID != "role-correlation-1" || backend.mutation.OccurredAt.IsZero() {
		t.Fatalf("import status=%d release=%+v mutation=%+v body=%s", imported.Code, backend.release, backend.mutation, imported.Body.String())
	}
	var importedEntry tenant.HostedLifecycleEntry
	if err := json.Unmarshal(imported.Body.Bytes(), &importedEntry); err != nil || importedEntry.Key != key || importedEntry.Sequence != 1 {
		t.Fatalf("import response=%+v error=%v", importedEntry, err)
	}

	status := &rolebundle.LifecycleStatus{SchemaVersion: 1, RoleID: "role-a", Version: "v1.2.3", ContentDigest: digest, Generation: 4, State: "active", PublisherKeyID: "publisher-a", Signature: []byte{1, 2, 3}}
	lifecycleBody, _ := json.Marshal(struct {
		ExpectedSequence uint64                      `json:"expected_sequence"`
		State            tenant.HostedLifecycleState `json:"state"`
		Status           *rolebundle.LifecycleStatus `json:"status"`
		Reason           string                      `json:"reason"`
	}{7, tenant.HostedReleased, status, "publisher update"})
	backend.lifecycleEntry = tenant.HostedLifecycleEntry{Key: key, Sequence: 8, State: tenant.HostedReleased, Status: status, Actor: identity.Account, Reason: "publisher update", RecordedAt: time.Now().UTC()}
	lifecycle := roleRegistryRequest(api, token, http.MethodPost, "/v1/role-registry/releases/catalog-a/role-a/v1.2.3/"+digest+"/lifecycle", string(lifecycleBody))
	if lifecycle.Code != http.StatusCreated || backend.lifecycleKey != key || backend.lifecycleSequence != 7 || backend.lifecycleState != tenant.HostedReleased || backend.lifecycleReason != "publisher update" || !reflect.DeepEqual(backend.lifecycleStatus, status) || backend.mutation.Actor != identity.Account || backend.mutation.Device != identity.Device {
		t.Fatalf("lifecycle status=%d key=%+v sequence=%d state=%s status=%+v body=%s", lifecycle.Code, backend.lifecycleKey, backend.lifecycleSequence, backend.lifecycleState, backend.lifecycleStatus, lifecycle.Body.String())
	}

	backend.pinValue = tenant.RolePin{Tenant: identity.Scope.Organization, RoleID: "role-a", CatalogID: "catalog-a", Version: "v1.2.3", ContentDigest: digest, Generation: 9, Actor: identity.Account, Reason: "project selection"}
	pinBody, _ := json.Marshal(struct {
		Kind               tenant.PinTargetKind `json:"target_kind"`
		ScopeID            string               `json:"scope_id"`
		RoleID             string               `json:"role_id"`
		CatalogID          string               `json:"catalog_id"`
		Version            string               `json:"version"`
		ContentDigest      string               `json:"content_digest"`
		ExpectedGeneration uint64               `json:"expected_generation"`
		Reason             string               `json:"reason"`
	}{tenant.PinProject, "project-a", "role-a", "catalog-a", "v1.2.3", digest, 8, "project selection"})
	pinned := roleRegistryRequest(api, token, http.MethodPut, "/v1/role-registry/pins", string(pinBody))
	if pinned.Code != http.StatusOK || backend.pinTarget.Tenant != identity.Scope.Organization || backend.pinTarget.Kind != tenant.PinProject || backend.pinTarget.ProjectID != "project-a" || backend.pinTarget.TeamID != "" || backend.pinRoleID != "role-a" || backend.pinCatalogID != "catalog-a" || backend.pinVersion != "v1.2.3" || backend.pinDigest != digest || backend.pinExpectedGeneration != 8 || backend.pinReason != "project selection" || backend.mutation.Actor != identity.Account {
		t.Fatalf("pin status=%d target=%+v role=%s catalog=%s version=%s digest=%s generation=%d reason=%s body=%s", pinned.Code, backend.pinTarget, backend.pinRoleID, backend.pinCatalogID, backend.pinVersion, backend.pinDigest, backend.pinExpectedGeneration, backend.pinReason, pinned.Body.String())
	}
	var pinOutput tenant.RolePin
	if err := json.Unmarshal(pinned.Body.Bytes(), &pinOutput); err != nil || pinOutput.Generation != 9 || pinOutput.RoleID != "role-a" {
		t.Fatalf("pin output=%+v error=%v", pinOutput, err)
	}

	backend.resolveValue = tenant.ResolvedRole{Source: backend.pinTarget, Pin: backend.pinValue}
	resolved := roleRegistryRequest(api, token, http.MethodGet, "/v1/role-registry/projects/project-a/roles/role-a", "")
	var resolveOutput tenant.ResolvedRole
	if resolved.Code != http.StatusOK || json.Unmarshal(resolved.Body.Bytes(), &resolveOutput) != nil || backend.resolveProject != "project-a" || backend.resolveRoleID != "role-a" || resolveOutput.Source.Kind != tenant.PinProject || resolveOutput.Pin.Generation != 9 {
		t.Fatalf("resolve status=%d project=%s role=%s result=%+v body=%s", resolved.Code, backend.resolveProject, backend.resolveRoleID, resolveOutput, resolved.Body.String())
	}
	if backend.calls != 6 {
		t.Fatalf("successful catalog/publisher/import/lifecycle/pin/resolve calls=%d, want 6", backend.calls)
	}
}

func TestRoleRegistryBuildsEveryPinScopeFromVerifiedTenant(t *testing.T) {
	for _, tc := range []struct {
		kind        tenant.PinTargetKind
		scope       string
		wantOrg     tenant.OrganizationID
		wantTeam    tenant.TeamID
		wantProject tenant.ProjectID
	}{
		{tenant.PinOrganization, "tenant-a", "tenant-a", "", ""},
		{tenant.PinTeam, "team-a", "", "team-a", ""},
		{tenant.PinProject, "project-a", "", "", "project-a"},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			backend := &roleRegistryFake{}
			api, token, identity := roleRegistryHandler(t, backend, 3)
			body, _ := json.Marshal(struct {
				Kind               tenant.PinTargetKind `json:"target_kind"`
				ScopeID            string               `json:"scope_id"`
				RoleID             string               `json:"role_id"`
				CatalogID          string               `json:"catalog_id"`
				Version            string               `json:"version"`
				Digest             string               `json:"content_digest"`
				ExpectedGeneration uint64               `json:"expected_generation"`
				Reason             string               `json:"reason"`
			}{tc.kind, tc.scope, "role-a", "catalog-a", "1.2.3", "sha256:" + strings.Repeat("c", 64), 0, "selection"})
			w := roleRegistryRequest(api, token, http.MethodPut, "/v1/role-registry/pins", string(body))
			if w.Code != http.StatusOK || backend.calls != 1 || backend.pinTarget.Tenant != identity.Scope.Organization || backend.pinTarget.OrganizationID != tc.wantOrg || backend.pinTarget.TeamID != tc.wantTeam || backend.pinTarget.ProjectID != tc.wantProject {
				t.Fatalf("status=%d calls=%d target=%+v body=%s", w.Code, backend.calls, backend.pinTarget, w.Body.String())
			}
		})
	}
}

func TestRoleRegistryRefusesMalformedOperationPayloadsBeforeBackend(t *testing.T) {
	backend := &roleRegistryFake{}
	api, token, _ := roleRegistryHandler(t, backend, 3)
	for _, tc := range []struct {
		method, path, body string
	}{
		{http.MethodPost, "/v1/role-registry/catalogs/catalog-a/publishers", `{"key_id":"publisher-a","public_key":"AQI="}`},
		{http.MethodPost, "/v1/role-registry/catalogs/catalog-a/releases", `{"role_id":"role-a","version":"1.2.3","content_digest":"sha256:` + strings.Repeat("a", 64) + `","envelope":""}`},
		{http.MethodPost, "/v1/role-registry/releases/catalog-a/role-a/1.2.3/sha256:" + strings.Repeat("a", 64) + "/lifecycle", `{"expected_sequence":0,"state":"released","reason":"missing status"}`},
		{http.MethodPost, "/v1/role-registry/releases/catalog-a/role-a/1.2.3/sha256:" + strings.Repeat("a", 64) + "/lifecycle", `{"expected_sequence":0,"state":"draft","status":{},"reason":"draft has no status"}`},
		{http.MethodPost, "/v1/role-registry/releases/catalog-a/role-a/1.2.3/sha256:" + strings.Repeat("a", 64) + "/lifecycle", `{"expected_sequence":0,"state":"released","status":{},"reason":"   "}`},
		{http.MethodPost, "/v1/role-registry/releases/catalog-a/role-a/1.2.3/sha256:" + strings.Repeat("a", 64) + "/lifecycle", `{"expected_sequence":0,"state":"released","status":{},"reason":" padded "}`},
		{http.MethodPut, "/v1/role-registry/pins", `{"target_kind":"organization","scope_id":"tenant-a","role_id":"role-a","catalog_id":"catalog-a","version":"1.2.3","content_digest":"sha256:` + strings.Repeat("a", 64) + `","reason":" padded "}`},
		{http.MethodPut, "/v1/role-registry/pins", `{"target_kind":"organization","scope_id":"tenant-b","role_id":"role-a","catalog_id":"catalog-a","version":"1.2.3","content_digest":"sha256:` + strings.Repeat("a", 64) + `","reason":"foreign"}`},
		{http.MethodPut, "/v1/role-registry/pins", `{"target_kind":"unknown","scope_id":"scope-a","role_id":"role-a","catalog_id":"catalog-a","version":"1.2.3","content_digest":"sha256:` + strings.Repeat("a", 64) + `","reason":"bad target"}`},
		{http.MethodGet, "/v1/role-registry/projects/!invalid/roles/role-a", ""},
	} {
		w := roleRegistryRequest(api, token, tc.method, tc.path, tc.body)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s %s status=%d body=%s, want 400", tc.method, tc.path, w.Code, w.Body.String())
		}
	}
	if backend.calls != 0 {
		t.Fatalf("malformed requests reached backend %d times", backend.calls)
	}
}

func TestRoleRegistryMethodAndRouteRefusalsDoNotReachBackend(t *testing.T) {
	backend := &roleRegistryFake{}
	api, token, _ := roleRegistryHandler(t, backend, 3)
	for _, tc := range []struct{ method, path string }{
		{http.MethodDelete, "/v1/role-registry/catalogs"},
		{http.MethodGet, "/v1/role-registry/catalogs/catalog-a/publishers"},
		{http.MethodGet, "/v1/role-registry/catalogs/catalog-a/releases"},
		{http.MethodPost, "/v1/role-registry/releases/catalog-a/role-a/1.2.3/sha256:" + strings.Repeat("a", 64)},
		{http.MethodPut, "/v1/role-registry/releases/catalog-a/role-a/1.2.3/sha256:" + strings.Repeat("a", 64) + "/lifecycle"},
		{http.MethodPost, "/v1/role-registry/pins"},
		{http.MethodPost, "/v1/role-registry/projects/project-a/roles/role-a"},
		{http.MethodGet, "/v1/role-registry/not-a-route"},
	} {
		w := roleRegistryRequest(api, token, tc.method, tc.path, "")
		want := http.StatusMethodNotAllowed
		if strings.HasSuffix(tc.path, "not-a-route") {
			want = http.StatusNotFound
		}
		if w.Code != want {
			t.Errorf("%s %s status=%d body=%s want=%d", tc.method, tc.path, w.Code, w.Body.String(), want)
		}
	}
	if backend.calls != 0 {
		t.Fatalf("method/route refusals reached backend %d times", backend.calls)
	}
}

func TestRoleRegistryMapsBackendErrorClasses(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"not found", tenantrepo.ErrNotFound, http.StatusNotFound},
		{"version conflict", tenantrepo.ErrConflict, http.StatusConflict},
		{"invalid signed data", tenant.ErrInvalidRoleRegistry, http.StatusBadRequest},
		{"dependency failure", context.DeadlineExceeded, http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend := &roleRegistryFake{err: tc.err}
			api, token, _ := roleRegistryHandler(t, backend, 3)
			w := roleRegistryRequest(api, token, http.MethodGet, "/v1/role-registry/catalogs", "")
			if w.Code != tc.want || backend.calls != 1 {
				t.Fatalf("status=%d calls=%d body=%s want=%d", w.Code, backend.calls, w.Body.String(), tc.want)
			}
		})
	}
}

func TestEnableRoleRegistryAPIRequiresDependencies(t *testing.T) {
	backend := &roleRegistryFake{}
	verifier, err := NewHMACIdentityVerifier(testSigningSecret)
	if err != nil {
		t.Fatal(err)
	}
	api := NewAPI(testConfig(), nil, nil)
	for name, call := range map[string]func() error{
		"nil api":      func() error { return (*API)(nil).EnableRoleRegistryAPI(verifier, backend, 3) },
		"nil verifier": func() error { return api.EnableRoleRegistryAPI(nil, backend, 3) },
		"nil backend":  func() error { return api.EnableRoleRegistryAPI(verifier, nil, 3) },
		"zero policy":  func() error { return api.EnableRoleRegistryAPI(verifier, backend, 0) },
	} {
		t.Run(name, func(t *testing.T) {
			if call() == nil {
				t.Fatal("missing dependency was accepted")
			}
		})
	}
}
