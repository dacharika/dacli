// Package tenantrepo persists the tenant domain behind an explicitly scoped
// PostgreSQL transaction. Tenant identity is carried both in every query and
// in the transaction-local RLS setting so a missed predicate still fails shut.
package tenantrepo

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"github.com/mlnomadpy/dacli/cloud/internal/tenant"
)

const SchemaVersion = 11

var (
	ErrNotFound          = errors.New("tenant resource not found")
	ErrConflict          = errors.New("tenant resource version conflict")
	ErrUnsupportedSchema = errors.New("unsupported tenant repository schema")
	ErrAuditConflict     = errors.New("tenant audit correlation conflicts with existing evidence")
	correlationPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
)

type Repository struct{ db *sql.DB }

type Mutation = tenant.Mutation

func Open(ctx context.Context, db *sql.DB) (*Repository, error) {
	if db == nil {
		return nil, errors.New("tenant repository requires a database")
	}
	var version int
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM controlplane_schema_migrations`).Scan(&version); err != nil {
		return nil, fmt.Errorf("read tenant repository schema: %w", err)
	}
	if version != SchemaVersion {
		return nil, fmt.Errorf("%w: database=%d binary=%d", ErrUnsupportedSchema, version, SchemaVersion)
	}
	return &Repository{db: db}, nil
}

func (r *Repository) Project(ctx context.Context, scope tenant.Scope, id tenant.ProjectID) (tenant.Project, error) {
	var project tenant.Project
	err := r.withTenant(ctx, scope, true, func(tx *sql.Tx) error {
		return scanProject(tx.QueryRowContext(ctx, `SELECT tenant_id, project_id, name, state, version
FROM controlplane_projects WHERE tenant_id = $1 AND project_id = $2`, scope.Organization, id), &project)
	})
	return project, err
}

func (r *Repository) CreateProject(ctx context.Context, scope tenant.Scope, mutation Mutation, project tenant.Project) error {
	if err := tenant.ValidateProject(scope, project); err != nil {
		return err
	}
	if project.Version != 1 || project.State != tenant.LifecycleActive {
		return errors.New("new project must be active at version 1")
	}
	return r.mutate(ctx, scope, mutation, func(tx *sql.Tx) (tenant.AuditEvent, error) {
		_, err := tx.ExecContext(ctx, `INSERT INTO controlplane_projects
(tenant_id, project_id, name, state, version) VALUES ($1, $2, $3, $4, $5)`,
			project.Tenant, project.ID, project.Name, project.State, project.Version)
		if err != nil {
			return tenant.AuditEvent{}, fmt.Errorf("insert project: %w", classifyWrite(err))
		}
		return newAudit(scope, mutation, tenant.AuditActionCreate, tenant.TargetProject, string(project.ID), 0, project.Version, [32]byte{}, digest(project))
	})
}

func (r *Repository) UpdateProject(ctx context.Context, scope tenant.Scope, mutation Mutation, expected tenant.Version, project tenant.Project) error {
	if err := tenant.ValidateProject(scope, project); err != nil {
		return err
	}
	if expected == 0 || project.Version != expected+1 {
		return errors.New("project update must advance the expected version exactly once")
	}
	return r.mutate(ctx, scope, mutation, func(tx *sql.Tx) (tenant.AuditEvent, error) {
		var before tenant.Project
		if err := scanProject(tx.QueryRowContext(ctx, `SELECT tenant_id, project_id, name, state, version
FROM controlplane_projects WHERE tenant_id = $1 AND project_id = $2`, scope.Organization, project.ID), &before); err != nil {
			return tenant.AuditEvent{}, err
		}
		if before.Version != expected {
			return tenant.AuditEvent{}, ErrConflict
		}
		result, err := tx.ExecContext(ctx, `UPDATE controlplane_projects SET name = $3, state = $4, version = $5
WHERE tenant_id = $1 AND project_id = $2 AND version = $6`,
			project.Tenant, project.ID, project.Name, project.State, project.Version, expected)
		if err != nil {
			return tenant.AuditEvent{}, fmt.Errorf("update project: %w", err)
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return tenant.AuditEvent{}, fmt.Errorf("read project update result: %w", err)
		}
		if changed != 1 {
			return tenant.AuditEvent{}, ErrConflict
		}
		return newAudit(scope, mutation, lifecycleAuditAction(before.State, project.State), tenant.TargetProject, string(project.ID), before.Version, project.Version, digest(before), digest(project))
	})
}

// CurrentMembership implements tenant.MembershipSource. The account identity is
// never queried without the caller's explicit scope and the database RLS scope.
func (r *Repository) CurrentMembership(ctx context.Context, scope tenant.Scope, account tenant.AccountID) (tenant.Membership, error) {
	var membership tenant.Membership
	err := r.withTenant(ctx, scope, true, func(tx *sql.Tx) error {
		var rolesJSON []byte
		err := tx.QueryRowContext(ctx, `SELECT tenant_id, account_id, COALESCE(team_id, ''), roles, state, version, expires_unix
FROM controlplane_memberships WHERE tenant_id = $1 AND account_id = $2`, scope.Organization, account).
			Scan(&membership.Tenant, &membership.Account, &membership.Team, &rolesJSON, &membership.State, &membership.Version, &membership.ExpiresUnix)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("read membership: %w", err)
		}
		var names []string
		if err := json.Unmarshal(rolesJSON, &names); err != nil {
			return fmt.Errorf("decode membership roles: %w", err)
		}
		roles := make([]tenant.Role, 0, len(names))
		for _, name := range names {
			role, err := tenant.ParseRole(name)
			if err != nil {
				return fmt.Errorf("decode membership roles: %w", err)
			}
			roles = append(roles, role)
		}
		membership.Roles, err = tenant.Roles(roles...)
		if err != nil {
			return fmt.Errorf("decode membership roles: %w", err)
		}
		if err := tenant.ValidateMembership(scope, membership); err != nil {
			return fmt.Errorf("validate stored membership: %w", err)
		}
		return nil
	})
	return membership, err
}

func (r *Repository) mutate(ctx context.Context, scope tenant.Scope, mutation Mutation, change func(*sql.Tx) (tenant.AuditEvent, error)) error {
	if err := validateMutation(mutation); err != nil {
		return err
	}
	return r.withTenant(ctx, scope, false, func(tx *sql.Tx) error {
		event, err := change(tx)
		if err != nil {
			return err
		}
		return appendAudit(ctx, tx, mutation.CorrelationID, event)
	})
}

func appendAudit(ctx context.Context, tx *sql.Tx, correlationID string, event tenant.AuditEvent) error {
	if err := tenant.ValidateAuditEvent(event); err != nil {
		return fmt.Errorf("validate tenant audit event: %w", err)
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO controlplane_tenant_audit_events
(tenant_id, correlation_id, actor_id, actor_device_id, action, target_kind, target_id,
 version_before, version_after, before_digest, after_digest, action_digest, result, reason, occurred_unix_milli)
VALUES ($1, $2, $3, NULLIF($4, ''), $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
ON CONFLICT (tenant_id, correlation_id) DO NOTHING`,
		event.Tenant, correlationID, event.Actor, event.ActorDevice, event.Action,
		event.TargetKind, event.Target(), event.VersionBefore, event.VersionAfter,
		event.BeforeDigest[:], event.AfterDigest[:], event.ActionDigest[:], event.Result.String(), event.ResultReason(), event.OccurredUnixMilli)
	if err != nil {
		return fmt.Errorf("append tenant audit event: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read tenant audit append result: %w", err)
	}
	if inserted == 1 {
		return nil
	}
	var actionDigest []byte
	var actor, device, storedResult, reason string
	if err := tx.QueryRowContext(ctx, `SELECT actor_id, COALESCE(actor_device_id, ''), action_digest, result, reason
FROM controlplane_tenant_audit_events WHERE tenant_id = $1 AND correlation_id = $2`, event.Tenant, correlationID).Scan(&actor, &device, &actionDigest, &storedResult, &reason); err != nil {
		return fmt.Errorf("read existing tenant audit event: %w", err)
	}
	if actor == string(event.Actor) && device == string(event.ActorDevice) && len(actionDigest) == len(event.ActionDigest) && string(actionDigest) == string(event.ActionDigest[:]) && storedResult == event.Result.String() && reason == event.ResultReason() {
		return nil
	}
	return ErrAuditConflict
}

// RecordAttempt appends authenticated non-success evidence in its own tenant
// transaction. Callers invoke it only after a failed mutation transaction has
// ended, so rollback cannot erase the refusal it is meant to explain.
func (r *Repository) RecordAttempt(ctx context.Context, scope tenant.Scope, mutation Mutation, event tenant.AuditEvent) error {
	if err := validateMutation(mutation); err != nil {
		return err
	}
	if event.Tenant != scope.Organization || event.Actor != mutation.Actor || event.ActorDevice != mutation.Device || event.OccurredUnixMilli != mutation.OccurredAt.UnixMilli() || event.Result == tenant.AuditResultSucceeded {
		return errors.New("tenant audit attempt does not match its verified mutation")
	}
	return r.withTenant(ctx, scope, false, func(tx *sql.Tx) error {
		return appendAudit(ctx, tx, mutation.CorrelationID, event)
	})
}

func (r *Repository) withTenant(ctx context.Context, scope tenant.Scope, readOnly bool, operation func(*sql.Tx) error) error {
	if r == nil || r.db == nil {
		return errors.New("tenant repository is not open")
	}
	if _, err := tenant.NewScope(scope.Organization); err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: readOnly})
	if err != nil {
		return fmt.Errorf("begin tenant transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if _, err := tx.ExecContext(ctx, `SELECT set_config('dacli.tenant_id', $1, true)`, scope.Organization); err != nil {
		return fmt.Errorf("bind tenant transaction: %w", err)
	}
	if err := operation(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit tenant transaction: %w", err)
	}
	committed = true
	return nil
}

type rowScanner interface{ Scan(...any) error }

func scanProject(row rowScanner, project *tenant.Project) error {
	if err := row.Scan(&project.Tenant, &project.ID, &project.Name, &project.State, &project.Version); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return fmt.Errorf("read project: %w", err)
	}
	return nil
}

func validateMutation(value Mutation) error {
	if _, err := tenant.NewAccountID(string(value.Actor)); err != nil {
		return errors.New("tenant mutation requires a valid actor")
	}
	if value.Device != "" {
		if _, err := tenant.NewDeviceID(string(value.Device)); err != nil {
			return errors.New("tenant mutation device is invalid")
		}
	}
	if !correlationPattern.MatchString(value.CorrelationID) {
		return errors.New("tenant mutation requires an opaque correlation id")
	}
	if value.OccurredAt.IsZero() {
		return errors.New("tenant mutation requires an occurrence time")
	}
	return nil
}

func newAudit(scope tenant.Scope, mutation Mutation, action tenant.AuditAction, kind tenant.TargetKind, target string, before, after tenant.Version, beforeDigest, afterDigest [32]byte) (tenant.AuditEvent, error) {
	return tenant.NewAuditEvent(scope, mutation.Actor, mutation.Device, action, kind, target, before, after, beforeDigest, afterDigest, mutation.OccurredAt.UnixMilli())
}

func digest(value any) [32]byte {
	raw, err := json.Marshal(value)
	if err != nil {
		panic("tenant domain value is not serializable: " + err.Error())
	}
	return sha256.Sum256(raw)
}

type sqlStateError interface{ SQLState() string }

func classifyWrite(err error) error {
	var state sqlStateError
	if errors.As(err, &state) && state.SQLState() == "23505" {
		return ErrConflict
	}
	return err
}
