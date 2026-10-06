package tenantrepo

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"github.com/mlnomadpy/dacli/cloud/internal/tenant"
	"time"
)

// registerAuthDevice operates only inside the same tenant transaction as session
// issuance. Existing revoked/foreign-key devices cannot be silently reactivated.
func registerAuthDevice(ctx context.Context, tx *sql.Tx, scope tenant.Scope, d tenant.Device, correlation string) error {
	if tenant.ValidateDevice(scope, d) != nil {
		return ErrConflict
	}
	var current tenant.Device
	var pub []byte
	e := tx.QueryRowContext(ctx, `SELECT account_id,name,platform,public_key,state,version FROM controlplane_devices WHERE tenant_id=$1 AND device_id=$2 FOR UPDATE`, scope.Organization, d.ID).Scan(&current.Account, &current.Name, &current.Platform, &pub, &current.State, &current.Version)
	if e == nil {
		if current.Account != d.Account || current.State != tenant.LifecycleActive || string(pub) != string(d.PublicKey[:]) {
			return ErrConflict
		}
		return nil
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO controlplane_devices(tenant_id,device_id,account_id,name,platform,public_key,state,version) VALUES($1,$2,$3,$4,$5,$6,$7,1)`, scope.Organization, d.ID, d.Account, d.Name, d.Platform, d.PublicKey[:], d.State); e != nil {
		return e
	}
	mutation := Mutation{Actor: d.Account, Device: d.ID, CorrelationID: correlation, OccurredAt: time.Now().UTC()}
	event, e := newAudit(scope, mutation, tenant.AuditActionCreate, tenant.TargetDevice, string(d.ID), 0, 1, [32]byte{}, digest(d))
	if e != nil {
		return e
	}
	return appendAudit(ctx, tx, correlation+".device", event)
}

// ProjectDeviceKeys exposes only active registered devices whose current account
// can write the explicitly selected project. Registration alone grants no project.
func (r *Repository) ProjectDeviceKeys(ctx context.Context, scope tenant.Scope, project tenant.ProjectID) (map[string]ed25519.PublicKey, error) {
	out := map[string]ed25519.PublicKey{}
	type candidate struct {
		account tenant.AccountID
		pub     []byte
	}
	var candidates []candidate
	e := r.withTenant(ctx, scope, true, func(tx *sql.Tx) error {
		rows, e := tx.QueryContext(ctx, `SELECT account_id,public_key FROM controlplane_devices WHERE tenant_id=$1 AND state=$2 ORDER BY device_id LIMIT 1001`, scope.Organization, tenant.LifecycleActive)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var c candidate
			if e = rows.Scan(&c.account, &c.pub); e != nil {
				return e
			}
			if len(c.pub) != 32 {
				return ErrConflict
			}
			candidates = append(candidates, c)
			if len(candidates) > 1000 {
				return ErrConflict
			}
		}
		return rows.Err()
	})
	if e != nil {
		return nil, e
	}
	p, e := r.Project(ctx, scope, project)
	if e != nil || p.State != tenant.LifecycleActive {
		return nil, ErrConflict
	}
	authorizer, e := tenant.NewAuthorizer(r, nil)
	if e != nil {
		return nil, e
	}
	for _, c := range candidates {
		membership, e := r.CurrentMembership(ctx, scope, c.account)
		if e != nil {
			continue
		}
		if _, e = authorizer.Authorize(ctx, tenant.Authorization{Scope: scope, Account: c.account, Permission: tenant.PermissionProjectWrite, ExpectedMembershipVersion: membership.Version}); e != nil {
			continue
		}
		if r.ProjectAccess(ctx, scope, c.account, project) != nil {
			continue
		}
		hash := sha256.Sum256(c.pub)
		out["device-"+hex.EncodeToString(hash[:])] = append(ed25519.PublicKey(nil), c.pub...)
	}
	return out, nil
}

func (r *Repository) Device(ctx context.Context, scope tenant.Scope, id tenant.DeviceID) (tenant.Device, error) {
	var d tenant.Device
	var pub []byte
	e := r.withTenant(ctx, scope, true, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT account_id,name,platform,public_key,state,version FROM controlplane_devices WHERE tenant_id=$1 AND device_id=$2`, scope.Organization, id).Scan(&d.Account, &d.Name, &d.Platform, &pub, &d.State, &d.Version)
	})
	d.Tenant = scope.Organization
	d.ID = id
	if e != nil || len(pub) != 32 {
		return tenant.Device{}, ErrNotFound
	}
	copy(d.PublicKey[:], pub)
	if tenant.ValidateDevice(scope, d) != nil {
		return tenant.Device{}, ErrConflict
	}
	return d, nil
}
func (r *Repository) ProjectAccess(ctx context.Context, scope tenant.Scope, account tenant.AccountID, project tenant.ProjectID) error {
	m, e := r.CurrentMembership(ctx, scope, account)
	if e != nil {
		return e
	}
	a, e := tenant.NewAuthorizer(r, nil)
	if e != nil {
		return e
	}
	if _, e = a.Authorize(ctx, tenant.Authorization{Scope: scope, Account: account, Permission: tenant.PermissionProjectWrite, ExpectedMembershipVersion: m.Version}); e != nil {
		return e
	}
	if m.Roles.Has(tenant.RoleOwner) || m.Roles.Has(tenant.RoleAdministrator) {
		return nil
	}
	var allowed bool
	e = r.withTenant(ctx, scope, true, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM controlplane_project_assignments WHERE tenant_id=$1 AND project_id=$2 AND account_id=$3 AND state=$4)`, scope.Organization, project, account, tenant.LifecycleActive).Scan(&allowed)
	})
	if e != nil || !allowed {
		return ErrConflict
	}
	return nil
}
func (r *Repository) EnvironmentAccess(ctx context.Context, scope tenant.Scope, account tenant.AccountID, environment tenant.EnvironmentID) error {
	m, e := r.CurrentMembership(ctx, scope, account)
	if e != nil {
		return e
	}
	if m.Roles.Has(tenant.RoleOwner) || m.Roles.Has(tenant.RoleAdministrator) {
		return nil
	}
	var allowed bool
	e = r.withTenant(ctx, scope, true, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM controlplane_environment_assignments WHERE tenant_id=$1 AND environment_id=$2 AND account_id=$3 AND state=$4)`, scope.Organization, environment, account, tenant.LifecycleActive).Scan(&allowed)
	})
	if e != nil || !allowed {
		return ErrConflict
	}
	return nil
}
