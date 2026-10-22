package tenantrepo

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"github.com/mlnomadpy/dacli/cloud/internal/deviceoauth"
	"github.com/mlnomadpy/dacli/cloud/internal/devicesession"
	"github.com/mlnomadpy/dacli/cloud/internal/tenant"
	"time"
)

func (r *Repository) CreateAuth(ctx context.Context, scope tenant.Scope, id string, raw []byte, expires int64) ([]byte, error) {
	var out []byte
	e := r.withTenant(ctx, scope, false, func(tx *sql.Tx) error {
		if len(id) != 43 || len(raw) > 131104 || expires <= time.Now().Unix() {
			return deviceoauth.ErrDenied
		}
		if _, e := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,616))`, "device-auth:"+string(scope.Organization)); e != nil {
			return e
		}
		var count int
		if e := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM controlplane_device_auth WHERE tenant_id=$1 AND expires_unix>$2`, scope.Organization, time.Now().Unix()).Scan(&count); e != nil {
			return e
		}
		if count >= 10000 {
			return deviceoauth.ErrDenied
		}
		if _, e := tx.ExecContext(ctx, `INSERT INTO controlplane_device_auth(tenant_id,flow_id,ciphertext,expires_unix) VALUES($1,$2,$3,$4) ON CONFLICT(tenant_id,flow_id) DO NOTHING`, scope.Organization, id, raw, expires); e != nil {
			return e
		}
		return tx.QueryRowContext(ctx, `SELECT ciphertext FROM controlplane_device_auth WHERE tenant_id=$1 AND flow_id=$2 AND expires_unix>$3`, scope.Organization, id, time.Now().Unix()).Scan(&out)
	})
	return out, e
}
func (r *Repository) UpdateAuth(ctx context.Context, scope tenant.Scope, id string, update func([]byte) ([]byte, *deviceoauth.SessionChange, error)) error {
	if len(id) != 43 || update == nil {
		return deviceoauth.ErrDenied
	}
	return r.withTenant(ctx, scope, false, func(tx *sql.Tx) error {
		var raw []byte
		var version uint64
		if e := tx.QueryRowContext(ctx, `SELECT ciphertext,version FROM controlplane_device_auth WHERE tenant_id=$1 AND flow_id=$2 AND expires_unix>$3 FOR UPDATE`, scope.Organization, id, time.Now().Unix()).Scan(&raw, &version); e != nil {
			return deviceoauth.ErrDenied
		}
		next, change, e := update(raw)
		if e != nil {
			return e
		}
		if change == nil && bytes.Equal(raw, next) {
			return nil
		}
		if len(next) < 28 || len(next) > 131104 {
			return deviceoauth.ErrDenied
		}
		if change != nil {
			now := time.Now().UTC()
			record := change.Session
			account := record.Account
			device := record.Device
			if record.ID != "" {
				var membershipState tenant.Lifecycle
				var membershipVersion tenant.Version
				var membershipExpiry int64
				if e := tx.QueryRowContext(ctx, `SELECT state,version,expires_unix FROM controlplane_memberships WHERE tenant_id=$1 AND account_id=$2 FOR UPDATE`, scope.Organization, record.Account).Scan(&membershipState, &membershipVersion, &membershipExpiry); e != nil || membershipState != tenant.LifecycleActive || membershipVersion != change.ExpectedMembershipVersion || (membershipExpiry > 0 && membershipExpiry <= time.Now().Unix()) {
					return deviceoauth.ErrDenied
				}
				if e := registerAuthDevice(ctx, tx, scope, change.Device, change.CorrelationID); e != nil {
					if errors.Is(e, ErrConflict) {
						return deviceoauth.ErrDenied
					}
					return e
				}
			}
			for _, id := range change.RevokeSessionIDs {
				var prior devicesession.Record
				var key []byte
				e := tx.QueryRowContext(ctx, `SELECT account_id,device_id,credential_digest,state,version,expires_unix FROM controlplane_device_sessions WHERE tenant_id=$1 AND session_id=$2 FOR UPDATE`, scope.Organization, id).Scan(&prior.Account, &prior.Device, &key, &prior.State, &prior.Version, &prior.ExpiresUnix)
				if e != nil {
					return e
				}
				prior.ID = id
				prior.Tenant = scope.Organization
				copy(prior.CredentialDigest[:], key)
				if account == "" {
					account = prior.Account
					device = prior.Device
				}
				if prior.Account != account || prior.Device != device {
					return deviceoauth.ErrDenied
				}
				if prior.State == devicesession.StateRevoked {
					continue
				}
				if _, e = tx.ExecContext(ctx, `UPDATE controlplane_device_sessions SET state=2,version=version+1 WHERE tenant_id=$1 AND session_id=$2 AND state=1`, scope.Organization, id); e != nil {
					return e
				}
				after := prior
				after.State = devicesession.StateRevoked
				after.Version++
				m := Mutation{Actor: account, Device: device, CorrelationID: change.CorrelationID, OccurredAt: now}
				event, e := newAudit(scope, m, tenant.AuditActionRevoke, tenant.TargetSession, string(id), prior.Version, after.Version, digest(prior), digest(after))
				if e != nil {
					return e
				}
				if e = appendAudit(ctx, tx, change.CorrelationID+".revoke."+string(id), event); e != nil {
					return e
				}
			}
			if record.ID != "" {
				if _, e = tx.ExecContext(ctx, `INSERT INTO controlplane_device_sessions(tenant_id,session_id,account_id,device_id,credential_digest,state,version,expires_unix) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, scope.Organization, record.ID, record.Account, record.Device, record.CredentialDigest[:], record.State, record.Version, record.ExpiresUnix); e != nil {
					return e
				}
				m := Mutation{Actor: account, Device: device, CorrelationID: change.CorrelationID, OccurredAt: now}
				event, e := newAudit(scope, m, tenant.AuditActionCreate, tenant.TargetSession, string(record.ID), 0, 1, [32]byte{}, digest(record))
				if e != nil {
					return e
				}
				if e = appendAudit(ctx, tx, change.CorrelationID+".issue", event); e != nil {
					return e
				}
			}
		}
		_, e = tx.ExecContext(ctx, `UPDATE controlplane_device_auth SET ciphertext=$3,version=version+1 WHERE tenant_id=$1 AND flow_id=$2 AND version=$4`, scope.Organization, id, next, version)
		return e
	})
}
