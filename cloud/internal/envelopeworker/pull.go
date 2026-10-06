package envelopeworker

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/lib/pq"
	"github.com/mlnomadpy/dacli/cloud/internal/tenant"
	"github.com/mlnomadpy/dacli/internal/cloudsync"
)

var ErrPullTooLarge = errors.New("signed outbox item exceeds response limit")

type PullPage struct {
	Envelopes []cloudsync.Envelope
	After     uint64
	Snapshot  uint64
	More      bool
}

// Pull reads original signed outbox payloads only; it does not claim delivery or
// turn inbox job metadata into an envelope. Explicit migration supplies a stable
// monotonic sequence so concurrent inserts cannot disappear behind a timestamp.
func (r *Repository) Pull(ctx context.Context, scope tenant.Scope, project tenant.ProjectID, after, snapshot uint64, keys []string) (PullPage, error) {
	page := PullPage{After: after, Snapshot: snapshot, Envelopes: []cloudsync.Envelope{}}
	e := r.withTenant(ctx, scope, true, func(tx *sql.Tx) (resultErr error) {
		if snapshot == 0 {
			if e := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(delivery_sequence),0) FROM controlplane_envelope_outbox WHERE tenant_id=$1 AND project_id=$2`, scope.Organization, project).Scan(&page.Snapshot); e != nil {
				return e
			}
		}
		if page.After > page.Snapshot {
			return ErrDenied
		}
		rows, e := tx.QueryContext(ctx, `SELECT delivery_sequence,envelope FROM controlplane_envelope_outbox WHERE tenant_id=$1 AND project_id=$2 AND delivery_sequence>$3 AND delivery_sequence<=$4 AND envelope->'integrity'->>'key_id'=ANY($5) ORDER BY delivery_sequence LIMIT 101`, scope.Organization, project, after, page.Snapshot, pq.Array(keys))
		if e != nil {
			return e
		}
		defer func() {
			if closeErr := rows.Close(); resultErr == nil {
				resultErr = closeErr
			}
		}()
		total := 0
		for rows.Next() {
			var seq uint64
			var raw []byte
			if e = rows.Scan(&seq, &raw); e != nil {
				return e
			}
			if len(raw) > 512<<10 {
				return ErrPullTooLarge
			}
			if len(page.Envelopes) >= 100 || total+len(raw) > 512<<10 {
				page.More = true
				break
			}
			if len(raw) > maxEnvelopePayloadBytes {
				return errors.New("outbox payload exceeds bound")
			}
			var envelope cloudsync.Envelope
			if json.Unmarshal(raw, &envelope) != nil || envelope.TenantID != string(scope.Organization) || envelope.ProjectID != string(project) {
				return ErrDenied
			}
			page.Envelopes = append(page.Envelopes, envelope)
			page.After = seq
			total += len(raw)
		}
		return rows.Err()
	})
	if e != nil {
		return PullPage{}, e
	}
	return page, nil
}

// Confirmed compares the durable producer identity before returning an ack. A
// duplicate event/key collision alone is insufficient to acknowledge a sequence.
func (r *Repository) Confirmed(ctx context.Context, scope tenant.Scope, envelope cloudsync.Envelope) (bool, error) {
	var confirmed bool
	e := r.withTenant(ctx, scope, true, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM controlplane_envelope_inbox_identities WHERE tenant_id=$1 AND project_id=$2 AND event_id=$3 AND idempotency_key=$4 AND producer_key_id=$5 AND producer_sequence=$6)`, scope.Organization, envelope.ProjectID, envelope.EventID, envelope.IdempotencyKey, envelope.Integrity.KeyID, envelope.ProducerSequence).Scan(&confirmed)
	})
	return confirmed, e
}
