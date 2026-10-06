package composition

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/mlnomadpy/dacli/cloud/internal/config"
	"github.com/mlnomadpy/dacli/cloud/internal/tenant"
	"github.com/mlnomadpy/dacli/cloud/internal/tenantrepo"
	"github.com/mlnomadpy/dacli/internal/cloudsync"
)

type syncBindings struct {
	repo   *tenantrepo.Repository
	keys   deviceKeys
	routes []config.SyncBinding
}

func (b syncBindings) Resolve(ctx context.Context, i tenant.VerifiedIdentity, r cloudsync.SyncRoute) (cloudsync.ProjectBindingResponse, error) {
	var out cloudsync.ProjectBindingResponse
	if tenant.ValidateVerifiedIdentity(i) != nil || r.TenantID != string(i.Scope.Organization) || r.DeviceID != string(i.Device) || r.WorkspaceID == "" || len(r.WorkspaceID) > 128 {
		return out, errors.New("sync route denied")
	}
	project, e := tenant.NewProjectID(r.ProjectID)
	environment, e2 := tenant.NewEnvironmentID(r.EnvironmentID)
	if e != nil || e2 != nil {
		return out, errors.New("sync route denied")
	}
	p, e := b.repo.Project(ctx, i.Scope, project)
	env, e2 := b.repo.Environment(ctx, i.Scope, environment)
	if e != nil || e2 != nil || p.State != tenant.LifecycleActive || env.State != tenant.LifecycleActive || env.Project != project {
		return out, errors.New("sync route denied")
	}
	m, e := b.repo.CurrentMembership(ctx, i.Scope, i.Account)
	if e != nil || m.Version != i.MembershipVersion || b.repo.ProjectAccess(ctx, i.Scope, i.Account, project) != nil || b.repo.EnvironmentAccess(ctx, i.Scope, i.Account, environment) != nil {
		return out, errors.New("sync route denied")
	}
	if _, e = b.DeviceKey(ctx, i); e != nil {
		return out, e
	}
	for _, route := range b.routes {
		if route.TenantID == r.TenantID && route.ProjectID == r.ProjectID && route.EnvironmentID == r.EnvironmentID && route.RepositoryID == r.RepositoryID && route.WorkspaceID == r.WorkspaceID {
			keys, e := b.keys.static.TrustedKeys(ctx, i.Scope, project)
			if e != nil {
				return out, e
			}
			serverKeys := map[string]string{}
			for id, pub := range publicKeys(keys) {
				if approved, ok := b.keys.serverKeys[id]; ok {
					if approved != pub {
						return out, errors.New("server project key mismatch")
					}
					serverKeys[id] = pub
				}
			}
			if len(serverKeys) == 0 {
				return out, errors.New("server project keys unavailable")
			}
			return cloudsync.ProjectBindingResponse{SchemaVersion: 1, Route: r, BindingVersion: route.Version, RepositoryURL: route.RepositoryURL, DefaultBranch: route.DefaultBranch, MinimumSchemaVersion: route.MinimumSchemaVersion, MaximumSchemaVersion: route.MaximumSchemaVersion, TrustedKeys: serverKeys}, nil
		}
	}
	return out, errors.New("sync route unavailable")
}
func (b syncBindings) DeviceKey(ctx context.Context, i tenant.VerifiedIdentity) (string, error) {
	d, e := b.repo.Device(ctx, i.Scope, i.Device)
	if e != nil || d.State != tenant.LifecycleActive || d.Account != i.Account {
		return "", errors.New("device signing key denied")
	}
	sum := sha256.Sum256(d.PublicKey[:])
	return "device-" + hex.EncodeToString(sum[:]), nil
}

func (b syncBindings) DevicePublicKey(ctx context.Context, i tenant.VerifiedIdentity) ([]byte, error) {
	d, e := b.repo.Device(ctx, i.Scope, i.Device)
	if e != nil || d.Account != i.Account || d.State != tenant.LifecycleActive {
		return nil, errors.New("device key unavailable")
	}
	return append([]byte(nil), d.PublicKey[:]...), nil
}
