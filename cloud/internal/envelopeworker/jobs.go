package envelopeworker

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/mlnomadpy/dacli/cloud/internal/tenant"
	"github.com/mlnomadpy/dacli/internal/cloudsync"
	"time"
)

// Inbox work is projected into immutable metadata proposals only. No local
// task, policy, approval or process authority is changed by a hosted worker.
type InboxJob struct {
	Project     tenant.ProjectID   `json:"project_id"`
	EventID     string             `json:"event_id"`
	State       string             `json:"state"`
	Attempts    int                `json:"attempts"`
	Fence       uint64             `json:"fence"`
	LastError   string             `json:"last_error"`
	NextAttempt int64              `json:"next_attempt_unix_milli"`
	Envelope    cloudsync.Envelope `json:"-"`
}

func (r *Repository) ClaimInbox(ctx context.Context, scope tenant.Scope, now, until time.Time, limit int) ([]InboxJob, error) {
	if now.IsZero() || !until.After(now) || limit < 1 || limit > 100 {
		return nil, errors.New("invalid inbox claim")
	}
	var result []InboxJob
	e := r.withTenant(ctx, scope, false, func(tx *sql.Tx) error {
		rows, e := tx.QueryContext(ctx, `WITH due AS (
 SELECT tenant_id,project_id,event_id FROM controlplane_envelope_inbox_jobs
 WHERE tenant_id=$1 AND ((state='pending' AND next_attempt_unix_milli<=$2) OR (state='processing' AND lease_until_unix_milli<=$2))
 ORDER BY next_attempt_unix_milli,event_id FOR UPDATE SKIP LOCKED LIMIT $3
 ) UPDATE controlplane_envelope_inbox_jobs j SET state='processing',attempts=j.attempts+1,fence=j.fence+1,lease_until_unix_milli=$4
 FROM due WHERE j.tenant_id=due.tenant_id AND j.project_id=due.project_id AND j.event_id=due.event_id
 RETURNING j.project_id,j.event_id,j.attempts,j.fence`, scope.Organization, now.UnixMilli(), limit, until.UnixMilli())
		if e != nil {
			return e
		}
		for rows.Next() {
			var row InboxJob
			if e = rows.Scan(&row.Project, &row.EventID, &row.Attempts, &row.Fence); e != nil {
				_ = rows.Close()
				return e
			}
			result = append(result, row)
		}
		if e = rows.Err(); e != nil {
			_ = rows.Close()
			return e
		}
		if e = rows.Close(); e != nil {
			return e
		}
		for i := range result {
			var raw []byte
			e = tx.QueryRowContext(ctx, `SELECT envelope FROM controlplane_envelope_inbox_payloads WHERE tenant_id=$1 AND project_id=$2 AND event_id=$3`, scope.Organization, result[i].Project, result[i].EventID).Scan(&raw)
			if errors.Is(e, sql.ErrNoRows) {
				continue
			}
			if e != nil {
				return e
			}
			if e = json.Unmarshal(raw, &result[i].Envelope); e != nil {
				return e
			}
		}
		return nil
	})
	if e != nil {
		return nil, e
	}
	return result, nil
}
func (r *Repository) CompleteInbox(ctx context.Context, scope tenant.Scope, row InboxJob, now time.Time) error {
	if row.Envelope.TenantID != string(scope.Organization) || row.Envelope.ProjectID != string(row.Project) || row.Envelope.EventID != row.EventID || cloudsync.ValidatePayload(row.Envelope.EventType, row.Envelope.Payload) != nil {
		return ErrDenied
	}
	raw, e := json.Marshal(row.Envelope)
	if e != nil {
		return e
	}
	digest := sha256.Sum256(raw)
	return r.withTenant(ctx, scope, false, func(tx *sql.Tx) error {
		var fence uint64
		var attempts int
		var state string
		var deadline int64
		if e := tx.QueryRowContext(ctx, `SELECT fence,attempts,state,lease_until_unix_milli FROM controlplane_envelope_inbox_jobs WHERE tenant_id=$1 AND project_id=$2 AND event_id=$3 FOR UPDATE`, scope.Organization, row.Project, row.EventID).Scan(&fence, &attempts, &state, &deadline); e != nil {
			return e
		}
		if fence != row.Fence || attempts != row.Attempts || state != "processing" || deadline <= now.UnixMilli() {
			return errors.New("inbox lease no longer current")
		}
		var stored []byte
		if e := tx.QueryRowContext(ctx, `SELECT envelope FROM controlplane_envelope_inbox_payloads WHERE tenant_id=$1 AND project_id=$2 AND event_id=$3`, scope.Organization, row.Project, row.EventID).Scan(&stored); e != nil {
			return e
		}
		if !equalJSON(stored, raw) {
			return errors.New("inbox payload changed")
		}
		if _, e := tx.ExecContext(ctx, `INSERT INTO controlplane_envelope_proposals(tenant_id,project_id,event_id,event_type,envelope_digest,payload,state,created_unix_milli) VALUES($1,$2,$3,$4,$5,$6,'proposed',$7) ON CONFLICT DO NOTHING`, scope.Organization, row.Project, row.EventID, row.Envelope.EventType, digest[:], []byte(row.Envelope.Payload), now.UnixMilli()); e != nil {
			return e
		}
		var old []byte
		if e := tx.QueryRowContext(ctx, `SELECT envelope_digest FROM controlplane_envelope_proposals WHERE tenant_id=$1 AND project_id=$2 AND event_id=$3`, scope.Organization, row.Project, row.EventID).Scan(&old); e != nil {
			return e
		}
		if string(old) != string(digest[:]) {
			return errors.New("inbox proposal identity collision")
		}
		result, e := tx.ExecContext(ctx, `UPDATE controlplane_envelope_inbox_jobs SET state='processed',processed_unix_milli=$4,lease_until_unix_milli=0,last_error_code='' WHERE tenant_id=$1 AND project_id=$2 AND event_id=$3 AND state='processing' AND fence=$5 AND lease_until_unix_milli>(EXTRACT(EPOCH FROM clock_timestamp())*1000)::BIGINT`, scope.Organization, row.Project, row.EventID, now.UnixMilli(), row.Fence)
		if e != nil {
			return e
		}
		count, e := result.RowsAffected()
		if e != nil || count != 1 {
			return errors.New("inbox lease expired before completion")
		}
		return nil
	})
}
func (r *Repository) FailInbox(ctx context.Context, scope tenant.Scope, row InboxJob, at time.Time, max int, code string) error {
	if max < 1 || len(code) == 0 || len(code) > 64 {
		return errors.New("invalid inbox retry")
	}
	state := "pending"
	if row.Attempts >= max {
		state = "dead-letter"
	}
	return r.withTenant(ctx, scope, false, func(tx *sql.Tx) error {
		result, e := tx.ExecContext(ctx, `UPDATE controlplane_envelope_inbox_jobs SET state=$4,next_attempt_unix_milli=$5,lease_until_unix_milli=0,last_error_code=$6 WHERE tenant_id=$1 AND project_id=$2 AND event_id=$3 AND state='processing' AND fence=$7 AND attempts=$8 AND lease_until_unix_milli>(EXTRACT(EPOCH FROM clock_timestamp())*1000)::BIGINT`, scope.Organization, row.Project, row.EventID, state, at.UnixMilli(), code, row.Fence, row.Attempts)
		if e != nil {
			return e
		}
		n, e := result.RowsAffected()
		if e != nil || n != 1 {
			return errors.New("inbox lease was lost")
		}
		return nil
	})
}
func (r *Repository) InboxStatus(ctx context.Context, scope tenant.Scope, project tenant.ProjectID, limit int) ([]InboxJob, error) {
	if project == "" || limit < 1 || limit > 100 {
		return nil, ErrDenied
	}
	var out []InboxJob
	e := r.withTenant(ctx, scope, true, func(tx *sql.Tx) error {
		rows, e := tx.QueryContext(ctx, `SELECT project_id,event_id,state,attempts,fence,last_error_code,next_attempt_unix_milli FROM controlplane_envelope_inbox_jobs WHERE tenant_id=$1 AND project_id=$2 ORDER BY event_id LIMIT $3`, scope.Organization, project, limit)
		if e != nil {
			return e
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var v InboxJob
			if e = rows.Scan(&v.Project, &v.EventID, &v.State, &v.Attempts, &v.Fence, &v.LastError, &v.NextAttempt); e != nil {
				return e
			}
			out = append(out, v)
		}
		return rows.Err()
	})
	return out, e
}
func (r *Repository) ProcessInbox(ctx context.Context, scope tenant.Scope, keys KeySource, max int) error {
	if keys == nil || max < 1 {
		return ErrUnavailable
	}
	now := time.Now().UTC()
	e := r.withTenant(ctx, scope, false, func(tx *sql.Tx) error {
		_, e := tx.ExecContext(ctx, `UPDATE controlplane_envelope_inbox_jobs SET state='dead-letter',last_error_code='inbox_retry_exhausted',lease_until_unix_milli=0 WHERE tenant_id=$1 AND attempts>=$2 AND (state='pending' OR (state='processing' AND lease_until_unix_milli<=$3))`, scope.Organization, max, now.UnixMilli())
		return e
	})
	if e != nil {
		return e
	}
	rows, e := r.ClaimInbox(ctx, scope, now, now.Add(2*time.Minute), 25)
	if e != nil {
		return e
	}
	for _, row := range rows {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		trusted, e := keys.TrustedKeys(ctx, scope, row.Project)
		if e == nil && !cloudsync.Verify(row.Envelope, trusted) {
			e = ErrDenied
		}
		if e == nil {
			e = r.CompleteInbox(ctx, scope, row, time.Now().UTC())
		}
		if e != nil {
			delay := time.Second
			for n := 1; n < row.Attempts && delay < time.Minute; n++ {
				delay *= 2
			}
			if delay > time.Minute {
				delay = time.Minute
			}
			if record := r.FailInbox(ctx, scope, row, time.Now().UTC().Add(delay), max, "inbox_processing_failed"); record != nil {
				return fmt.Errorf("record inbox failure: %w", record)
			}
		}
	}
	return nil
}
