package envelopeworker

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/mlnomadpy/dacli/cloud/internal/tenant"
	"github.com/mlnomadpy/dacli/internal/cloudsync"
)

const (
	SchemaVersion         = 11
	payloadRetention      = 90 * 24 * time.Hour
	maxRetentionBatchSize = 1000
)

type Repository struct {
	db                   *sql.DB
	deliveryAttemptLimit int
}

func Open(ctx context.Context, db *sql.DB) (*Repository, error) {
	if db == nil {
		return nil, errors.New("envelope repository requires a database")
	}
	var version int
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM controlplane_schema_migrations`).Scan(&version); err != nil {
		return nil, fmt.Errorf("read envelope repository schema: %w", err)
	}
	if version != SchemaVersion {
		return nil, fmt.Errorf("unsupported envelope repository schema: database=%d binary=%d", version, SchemaVersion)
	}
	return &Repository{db: db}, nil
}

func (r *Repository) Accept(ctx context.Context, identity tenant.VerifiedIdentity, envelope cloudsync.Envelope, audit Audit) (cloudsync.Outcome, error) {
	var outcome cloudsync.Outcome
	err := r.withTenant(ctx, identity.Scope, false, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO controlplane_envelope_streams
(tenant_id, project_id, producer_key_id) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
			identity.Scope.Organization, envelope.ProjectID, envelope.Integrity.KeyID); err != nil {
			return fmt.Errorf("ensure envelope stream: %w", err)
		}
		var last, replayFloor uint64
		var minimum, maximum int
		if err := tx.QueryRowContext(ctx, `SELECT last_sequence, replay_floor, minimum_schema_version, maximum_schema_version
FROM controlplane_envelope_streams
WHERE tenant_id = $1 AND project_id = $2 AND producer_key_id = $3 FOR UPDATE`,
			identity.Scope.Organization, envelope.ProjectID, envelope.Integrity.KeyID).Scan(&last, &replayFloor, &minimum, &maximum); err != nil {
			return fmt.Errorf("lock envelope stream: %w", err)
		}
		var duplicate bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (
    SELECT 1 FROM controlplane_envelope_inbox_identities
    WHERE tenant_id = $1 AND project_id = $2 AND (event_id = $3 OR idempotency_key = $4)
)`, identity.Scope.Organization, envelope.ProjectID, envelope.EventID, envelope.IdempotencyKey).Scan(&duplicate); err != nil {
			return fmt.Errorf("check envelope identity: %w", err)
		}
		if duplicate {
			outcome = cloudsync.OutcomeDuplicate
			audit.Decision, audit.Reason = outcome, "duplicate"
			return appendAudit(ctx, tx, audit)
		}
		if envelope.SchemaVersion < minimum || envelope.SchemaVersion > maximum {
			outcome = cloudsync.OutcomeIncompatible
			audit.Decision, audit.Reason = outcome, "schema"
			return appendAudit(ctx, tx, audit)
		}
		if envelope.ProducerSequence < replayFloor {
			outcome = cloudsync.OutcomeReplay
			audit.Decision, audit.Reason = outcome, "replay_floor"
			return appendAudit(ctx, tx, audit)
		}
		var sequenceCollision bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (
    SELECT 1 FROM controlplane_envelope_inbox_identities
    WHERE tenant_id = $1 AND project_id = $2 AND producer_key_id = $3 AND producer_sequence = $4
)`, identity.Scope.Organization, envelope.ProjectID, envelope.Integrity.KeyID, envelope.ProducerSequence).Scan(&sequenceCollision); err != nil {
			return fmt.Errorf("check producer sequence identity: %w", err)
		}
		if sequenceCollision {
			outcome = cloudsync.OutcomeTampered
			audit.Decision, audit.Reason = outcome, "sequence_collision"
			return appendAudit(ctx, tx, audit)
		}
		outcome = cloudsync.OutcomeAccepted
		if envelope.ProducerSequence <= last {
			outcome = cloudsync.OutcomeReordered
		}
		raw, err := json.Marshal(envelope)
		if err != nil {
			return fmt.Errorf("encode inbox envelope: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO controlplane_envelope_inbox_identities
(tenant_id, project_id, producer_key_id, event_id, idempotency_key, producer_sequence, outcome, received_unix_milli)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, identity.Scope.Organization, envelope.ProjectID,
			envelope.Integrity.KeyID, envelope.EventID, envelope.IdempotencyKey, envelope.ProducerSequence, outcome, audit.OccurredAt.UnixMilli()); err != nil {
			return fmt.Errorf("persist inbox identity: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO controlplane_envelope_inbox_payloads
(tenant_id, project_id, event_id, envelope, expires_unix_milli) VALUES ($1, $2, $3, $4, $5)`,
			identity.Scope.Organization, envelope.ProjectID, envelope.EventID, raw, audit.OccurredAt.Add(payloadRetention).UnixMilli()); err != nil {
			return fmt.Errorf("persist inbox payload: %w", err)
		}
		rows, err := tx.QueryContext(ctx, `SELECT producer_sequence FROM controlplane_envelope_inbox_identities
WHERE tenant_id = $1 AND project_id = $2 AND producer_key_id = $3 AND producer_sequence > $4
ORDER BY producer_sequence`, identity.Scope.Organization, envelope.ProjectID, envelope.Integrity.KeyID, last)
		if err != nil {
			return fmt.Errorf("read contiguous envelope sequences: %w", err)
		}
		next := last
		for rows.Next() {
			var sequence uint64
			if err := rows.Scan(&sequence); err != nil {
				_ = rows.Close()
				return fmt.Errorf("scan envelope sequence: %w", err)
			}
			if sequence != next+1 {
				break
			}
			next = sequence
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return fmt.Errorf("iterate envelope sequences: %w", err)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("close envelope sequences: %w", err)
		}
		if next != last {
			if _, err := tx.ExecContext(ctx, `UPDATE controlplane_envelope_streams SET last_sequence = $4
WHERE tenant_id = $1 AND project_id = $2 AND producer_key_id = $3`, identity.Scope.Organization, envelope.ProjectID, envelope.Integrity.KeyID, next); err != nil {
				return fmt.Errorf("advance envelope cursor: %w", err)
			}
		}
		audit.Decision, audit.Reason = outcome, "accepted"
		return appendAudit(ctx, tx, audit)
	})
	return outcome, err
}

func (r *Repository) RecordDecision(ctx context.Context, audit Audit) error {
	return r.withTenant(ctx, audit.Identity.Scope, false, func(tx *sql.Tx) error {
		return appendAudit(ctx, tx, audit)
	})
}

func appendAudit(ctx context.Context, tx *sql.Tx, audit Audit) error {
	if tenant.ValidateVerifiedIdentity(audit.Identity) != nil || len(audit.CorrelationID) == 0 || len(audit.CorrelationID) > 128 || len(audit.EventIdentity) != 64 || audit.Project == "" || audit.Decision == "" || len(audit.Reason) == 0 || len(audit.Reason) > 64 || audit.OccurredAt.IsZero() {
		return errors.New("invalid envelope audit event")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO controlplane_envelope_audit_events
(tenant_id, correlation_id, actor_id, actor_device_id, project_id, event_identity, decision, reason, occurred_unix_milli)
VALUES ($1, $2, $3, NULLIF($4, ''), $5, $6, $7, $8, $9)`, audit.Identity.Scope.Organization,
		audit.CorrelationID, audit.Identity.Account, audit.Identity.Device, audit.Project, audit.EventIdentity,
		audit.Decision, audit.Reason, audit.OccurredAt.UnixMilli()); err != nil {
		return fmt.Errorf("append envelope audit: %w", err)
	}
	return nil
}

func (r *Repository) Enqueue(ctx context.Context, scope tenant.Scope, id string, envelope cloudsync.Envelope, now time.Time) error {
	if !bounded(id, 128) || envelope.TenantID != string(scope.Organization) || !bounded(envelope.ProjectID, 128) || !bounded(envelope.IdempotencyKey, 256) || envelope.SchemaVersion != 1 || envelope.Integrity.Algorithm != "Ed25519" || !bounded(envelope.Integrity.KeyID, 128) || !bounded(envelope.Integrity.Signature, 256) || now.IsZero() {
		return errors.New("invalid outbound envelope")
	}
	if err := cloudsync.ValidatePayload(envelope.EventType, envelope.Payload); err != nil {
		return err
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	return r.withTenant(ctx, scope, false, func(tx *sql.Tx) error {
		var existing []byte
		err := tx.QueryRowContext(ctx, `SELECT envelope FROM controlplane_envelope_outbox
WHERE tenant_id = $1 AND project_id = $2 AND idempotency_key = $3 FOR UPDATE`, scope.Organization, envelope.ProjectID, envelope.IdempotencyKey).Scan(&existing)
		if err == nil {
			if !equalJSON(existing, raw) {
				return errors.New("outbound idempotency key collides with different envelope")
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("read outbound idempotency identity: %w", err)
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO controlplane_envelope_outbox
(tenant_id, project_id, outbox_id, idempotency_key, envelope, state, next_attempt_unix_milli, created_unix_milli)
VALUES ($1, $2, $3, $4, $5, 'pending', $6, $6)`, scope.Organization, envelope.ProjectID, id, envelope.IdempotencyKey, raw, now.UnixMilli())
		if err != nil {
			return fmt.Errorf("enqueue outbound envelope: %w", err)
		}
		return nil
	})
}

func equalJSON(left, right []byte) bool {
	var leftValue, rightValue any
	leftDecoder, rightDecoder := json.NewDecoder(bytes.NewReader(left)), json.NewDecoder(bytes.NewReader(right))
	leftDecoder.UseNumber()
	rightDecoder.UseNumber()
	if leftDecoder.Decode(&leftValue) != nil || rightDecoder.Decode(&rightValue) != nil {
		return false
	}
	leftCanonical, leftErr := json.Marshal(leftValue)
	rightCanonical, rightErr := json.Marshal(rightValue)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftCanonical, rightCanonical)
}

// WithDeliveryAttemptLimit binds an operator-selected retry ceiling to this
// worker repository view, without mutating shared HTTP repository policy.
func (r *Repository) WithDeliveryAttemptLimit(max int) (*Repository, error) {
	if r == nil || r.db == nil || max < 1 || max > 100 {
		return nil, errors.New("invalid delivery attempt limit")
	}
	return &Repository{db: r.db, deliveryAttemptLimit: max}, nil
}

func (r *Repository) ClaimDue(ctx context.Context, scope tenant.Scope, now, leaseUntil time.Time, limit int) ([]Delivery, error) {
	if limit < 1 || limit > maxDeliveryBatch || now.IsZero() || !leaseUntil.After(now) {
		return nil, errors.New("invalid outbox claim")
	}
	var result []Delivery
	err := r.withTenant(ctx, scope, false, func(tx *sql.Tx) error {
		if r.deliveryAttemptLimit > 0 {
			if _, e := tx.ExecContext(ctx, `UPDATE controlplane_envelope_outbox SET state='dead-letter',last_error_code='delivery_retry_exhausted',lease_until_unix_milli=0 WHERE tenant_id=$1 AND attempts>=$2 AND (state='pending' OR (state='delivering' AND lease_until_unix_milli<=$3))`, scope.Organization, r.deliveryAttemptLimit, now.UnixMilli()); e != nil {
				return e
			}
		}
		rows, err := tx.QueryContext(ctx, `WITH due AS (
    SELECT tenant_id, project_id, outbox_id FROM controlplane_envelope_outbox
    WHERE tenant_id = $1 AND ($5=0 OR attempts<$5) AND ((state = 'pending' AND next_attempt_unix_milli <= $2)
       OR (state = 'delivering' AND lease_until_unix_milli <= $2))
    ORDER BY next_attempt_unix_milli, outbox_id
    FOR UPDATE SKIP LOCKED LIMIT $3
)
UPDATE controlplane_envelope_outbox AS o SET state = 'delivering', attempts = o.attempts + 1, lease_until_unix_milli = $4
FROM due WHERE o.tenant_id = due.tenant_id AND o.project_id = due.project_id AND o.outbox_id = due.outbox_id
RETURNING o.tenant_id, o.project_id, o.outbox_id, o.envelope, o.attempts - 1`, scope.Organization, now.UnixMilli(), limit, leaseUntil.UnixMilli(), r.deliveryAttemptLimit)
		if err != nil {
			return fmt.Errorf("claim due outbox rows: %w", err)
		}
		for rows.Next() {
			var row Delivery
			var raw []byte
			if err := rows.Scan(&row.Tenant, &row.Project, &row.ID, &raw, &row.Attempts); err != nil {
				return fmt.Errorf("scan claimed outbox row: %w", err)
			}
			if err := json.Unmarshal(raw, &row.Envelope); err != nil {
				return fmt.Errorf("decode claimed outbox envelope: %w", err)
			}
			if row.Tenant != scope.Organization || row.Project != tenant.ProjectID(row.Envelope.ProjectID) || row.Envelope.TenantID != string(scope.Organization) {
				return errors.New("stored outbox route violates tenant scope")
			}
			result = append(result, row)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		return rows.Close()
	})
	return result, err
}

func (r *Repository) MarkDelivered(ctx context.Context, scope tenant.Scope, row Delivery, at time.Time) error {
	return r.transition(ctx, scope, row, `UPDATE controlplane_envelope_outbox
SET state = 'delivered', attempts = attempts, delivered_unix_milli = $4, lease_until_unix_milli = 0, last_error_code = $5
WHERE tenant_id = $1 AND project_id = $2 AND outbox_id = $3 AND state = 'delivering' AND attempts = $6 + 1 AND lease_until_unix_milli > (EXTRACT(EPOCH FROM clock_timestamp()) * 1000)::BIGINT`, at.UnixMilli(), "")
}

func (r *Repository) Reschedule(ctx context.Context, scope tenant.Scope, row Delivery, at time.Time, code string) error {
	return r.transition(ctx, scope, row, `UPDATE controlplane_envelope_outbox
SET state = 'pending', attempts = attempts, next_attempt_unix_milli = $4, lease_until_unix_milli = 0, last_error_code = $5
WHERE tenant_id = $1 AND project_id = $2 AND outbox_id = $3 AND state = 'delivering' AND attempts = $6 + 1 AND lease_until_unix_milli > (EXTRACT(EPOCH FROM clock_timestamp()) * 1000)::BIGINT`, at.UnixMilli(), code)
}

func (r *Repository) MarkDeadLetter(ctx context.Context, scope tenant.Scope, row Delivery, at time.Time, code string) error {
	return r.transition(ctx, scope, row, `UPDATE controlplane_envelope_outbox
SET state = 'dead-letter', attempts = attempts, next_attempt_unix_milli = $4, lease_until_unix_milli = 0, last_error_code = $5
WHERE tenant_id = $1 AND project_id = $2 AND outbox_id = $3 AND state = 'delivering' AND attempts = $6 + 1 AND lease_until_unix_milli > (EXTRACT(EPOCH FROM clock_timestamp()) * 1000)::BIGINT`, at.UnixMilli(), code)
}

func (r *Repository) transition(ctx context.Context, scope tenant.Scope, row Delivery, query string, at int64, code string) error {
	if row.Tenant != scope.Organization || row.Project == "" || !bounded(row.ID, 128) || at <= 0 || len(code) > 64 {
		return errors.New("invalid outbox transition")
	}
	return r.withTenant(ctx, scope, false, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, query, scope.Organization, row.Project, row.ID, at, code, row.Attempts)
		if err != nil {
			return fmt.Errorf("transition outbox: %w", err)
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			return errors.New("outbox lease was lost")
		}
		return nil
	})
}

func (r *Repository) DeadLetters(ctx context.Context, scope tenant.Scope, project tenant.ProjectID, limit int) ([]DeadLetter, error) {
	if limit < 1 || limit > maxDeliveryBatch {
		return nil, errors.New("invalid dead-letter limit")
	}
	var result []DeadLetter
	err := r.withTenant(ctx, scope, true, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT project_id, outbox_id, idempotency_key, attempts, last_error_code, created_unix_milli
FROM controlplane_envelope_outbox WHERE tenant_id = $1 AND project_id = $2 AND state = 'dead-letter'
ORDER BY created_unix_milli, outbox_id LIMIT $3`, scope.Organization, project, limit)
		if err != nil {
			return err
		}
		for rows.Next() {
			var row DeadLetter
			var created int64
			if err := rows.Scan(&row.Project, &row.ID, &row.IdempotencyKey, &row.Attempts, &row.LastErrorCode, &created); err != nil {
				return err
			}
			row.CreatedAt = time.UnixMilli(created).UTC()
			result = append(result, row)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		return rows.Close()
	})
	return result, err
}

// ExpirePayloads removes only payload material. Permanent identity rows,
// sequence history, replay floors, and audits remain untouched.
func (r *Repository) ExpirePayloads(ctx context.Context, scope tenant.Scope, before time.Time, limit int) (int64, error) {
	if before.IsZero() || limit < 1 || limit > maxRetentionBatchSize {
		return 0, errors.New("invalid payload retention request")
	}
	var changed int64
	err := r.withTenant(ctx, scope, false, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `DELETE FROM controlplane_envelope_inbox_payloads
WHERE (tenant_id, project_id, event_id) IN (
    SELECT tenant_id, project_id, event_id FROM controlplane_envelope_inbox_payloads
    WHERE tenant_id = $1 AND expires_unix_milli <= $2 ORDER BY expires_unix_milli LIMIT $3
)`, scope.Organization, before.UnixMilli(), limit)
		if err != nil {
			return fmt.Errorf("expire inbox payloads: %w", err)
		}
		changed, err = result.RowsAffected()
		return err
	})
	return changed, err
}

func (r *Repository) withTenant(ctx context.Context, scope tenant.Scope, readOnly bool, operation func(*sql.Tx) error) error {
	if r == nil || r.db == nil {
		return errors.New("envelope repository is not open")
	}
	if _, err := tenant.NewScope(scope.Organization); err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: readOnly})
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if _, err := tx.ExecContext(ctx, `SELECT set_config('dacli.tenant_id', $1, true)`, scope.Organization); err != nil {
		return err
	}
	if err := operation(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}
