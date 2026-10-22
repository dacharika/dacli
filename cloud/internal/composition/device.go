package composition

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"github.com/mlnomadpy/dacli/cloud/internal/config"
	"github.com/mlnomadpy/dacli/cloud/internal/deviceoauth"
	"github.com/mlnomadpy/dacli/cloud/internal/devicesession"
	"github.com/mlnomadpy/dacli/cloud/internal/tenant"
	"github.com/mlnomadpy/dacli/cloud/internal/tenantrepo"
)

type deviceKeys struct {
	static     projectKeys
	repo       *tenantrepo.Repository
	bindings   []config.SyncBinding
	serverKeys map[string]string
}

func (k deviceKeys) Approved(scope tenant.Scope, pub []byte) (string, map[string]string, error) {
	if len(pub) != 32 {
		return "", nil, deviceoauth.ErrDenied
	}
	found := false
	for _, t := range k.static.tenants {
		if t.Tenant == scope.Organization {
			found = true
			break
		}
	}
	if !found {
		return "", nil, deviceoauth.ErrDenied
	}
	hash := sha256.Sum256(pub)
	return "device-" + hex.EncodeToString(hash[:]), clonePublicStrings(k.serverKeys), nil
}
func (k deviceKeys) TrustedKeys(ctx context.Context, scope tenant.Scope, project tenant.ProjectID) (map[string]ed25519.PublicKey, error) {
	keys, e := k.static.TrustedKeys(ctx, scope, project)
	if e != nil {
		return nil, e
	}
	bound := false
	for _, b := range k.bindings {
		if b.TenantID == string(scope.Organization) && b.ProjectID == string(project) {
			bound = true
			break
		}
	}
	if !bound {
		return keys, nil
	}
	devices, e := k.repo.ProjectDeviceKeys(ctx, scope, project)
	if e != nil {
		return nil, e
	}
	for id, pub := range devices {
		if old, ok := keys[id]; ok && string(old) != string(pub) {
			return nil, errors.New("device key collision")
		}
		keys[id] = append(ed25519.PublicKey(nil), pub...)
	}
	return keys, nil
}
func enableDevice(r *Runtime, repo *tenantrepo.Repository, keys deviceKeys) error {
	c := r.hosted.DeviceOAuth()
	if c == nil {
		return nil
	}
	provider, e := deviceoauth.NewProvider(deviceoauth.ProviderConfig{Issuer: c.Issuer, DeviceURL: c.DeviceURL, TokenURL: c.TokenURL, IdentityURL: c.IdentityURL, ClientID: c.ClientID, ClientSecret: c.ClientSecret, Scope: c.Scope})
	if e != nil {
		return e
	}
	manager, e := devicesession.New(repo, nil)
	if e != nil {
		return e
	}
	subjects := make([]deviceoauth.SubjectBinding, 0, len(c.Subjects))
	for _, b := range c.Subjects {
		subjects = append(subjects, deviceoauth.SubjectBinding{Tenant: b.TenantID, Subject: b.Subject, Account: b.AccountID})
	}
	device, e := deviceoauth.New(provider, repo, manager, []byte(r.cfg.ServiceSecret()), subjects, keys)
	if e != nil {
		return e
	}
	return r.API.EnableDeviceAPI(device)
}
func publicKeys(keys map[string]ed25519.PublicKey) map[string]string {
	out := map[string]string{}
	for id, pub := range keys {
		out[id] = base64.StdEncoding.EncodeToString(pub)
	}
	return out
}

func clonePublicStrings(in map[string]string) map[string]string {
	out := map[string]string{}
	for id, pub := range in {
		out[id] = pub
	}
	return out
}

// Exchange validates exact project/environment routing before using this narrower
// write authenticator. Legacy management endpoints keep their original verifier.
type exchangeIdentity struct{ base *deviceIdentity }

func (i exchangeIdentity) VerifyWork(ctx context.Context, bearer string) (tenant.VerifiedIdentity, error) {
	identity, e := i.base.Verify(ctx, bearer)
	if e != nil {
		return tenant.VerifiedIdentity{}, e
	}
	if _, e = i.base.authorizer.Authorize(ctx, tenant.Authorization{Scope: identity.Scope, Account: identity.Account, Permission: tenant.PermissionProjectWrite, ExpectedMembershipVersion: identity.MembershipVersion}); e != nil {
		return tenant.VerifiedIdentity{}, errors.New("sync write authority denied")
	}
	return identity, nil
}
