package composition

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"github.com/mlnomadpy/dacli/cloud/internal/config"
	"github.com/mlnomadpy/dacli/cloud/internal/devicesession"
	"github.com/mlnomadpy/dacli/cloud/internal/tenant"
	"github.com/mlnomadpy/dacli/cloud/internal/tenantrepo"
	"strings"
)

// Device bearer v1 is base64url(tenant locator).base64url(opaque credential).
// The locator selects only a scoped lookup; authority comes exclusively from
// the current DB session, device and membership validated by Manager.Authorize.
type deviceIdentity struct {
	manager        *devicesession.Manager
	authorizer     *tenant.Authorizer
	policyRevision uint64
}

func (i *deviceIdentity) Verify(ctx context.Context, authorization string) (tenant.VerifiedIdentity, error) {
	var denied tenant.VerifiedIdentity
	if i == nil || !strings.HasPrefix(authorization, "Bearer ") || len(authorization) > 8192 {
		return denied, errors.New("device identity denied")
	}
	parts := strings.Split(strings.TrimPrefix(authorization, "Bearer "), ".")
	if len(parts) != 2 {
		return denied, errors.New("device identity denied")
	}
	locator, e := base64.RawURLEncoding.DecodeString(parts[0])
	credential, e2 := base64.RawURLEncoding.DecodeString(parts[1])
	org, e3 := tenant.NewOrganizationID(string(locator))
	if e != nil || e2 != nil || e3 != nil {
		return denied, errors.New("device identity denied")
	}
	scope, _ := tenant.NewScope(org)
	snapshot, e := i.manager.Authorize(ctx, scope, credential)
	if e != nil {
		return denied, errors.New("device identity denied")
	}
	identity := tenant.VerifiedIdentity{Scope: scope, Account: snapshot.Session.Account, Device: snapshot.Session.Device, MembershipVersion: snapshot.Membership.Version, PolicyRevision: i.policyRevision}
	if tenant.ValidateVerifiedIdentity(identity) != nil {
		return denied, errors.New("device identity denied")
	}
	return identity, nil
}
func (i *deviceIdentity) VerifyWork(ctx context.Context, credential string) (tenant.VerifiedIdentity, error) {
	identity, e := i.Verify(ctx, credential)
	if e != nil {
		return tenant.VerifiedIdentity{}, e
	}
	_, e = i.authorizer.Authorize(ctx, tenant.Authorization{Scope: identity.Scope, Account: identity.Account, Permission: tenant.PermissionProjectManage, ExpectedMembershipVersion: identity.MembershipVersion})
	if e != nil {
		return tenant.VerifiedIdentity{}, errors.New("device work authorization denied")
	}
	return identity, nil
}
func newDeviceIdentity(repo *tenantrepo.Repository, revision uint64) (*deviceIdentity, error) {
	manager, e := devicesession.New(repo, nil)
	if e != nil {
		return nil, e
	}
	authorizer, e := tenant.NewAuthorizer(repo, nil)
	if e != nil {
		return nil, e
	}
	return &deviceIdentity{manager, authorizer, revision}, nil
}

type projectKeys struct{ tenants []config.HostedTenant }

func (k projectKeys) TrustedKeys(_ context.Context, scope tenant.Scope, project tenant.ProjectID) (map[string]ed25519.PublicKey, error) {
	for _, t := range k.tenants {
		if t.Tenant != scope.Organization {
			continue
		}
		keys, ok := t.Keys[project]
		if !ok {
			return nil, errors.New("project signing keys unavailable")
		}
		copy := map[string]ed25519.PublicKey{}
		for id, key := range keys {
			copy[id] = append(ed25519.PublicKey(nil), key...)
		}
		return copy, nil
	}
	return nil, errors.New("tenant signing keys unavailable")
}
