package postgres

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"jungle-gaming-backend/internal/application"
	"time"
)

type Outbox struct{ pool *pgxpool.Pool }

func NewOutbox(pool *pgxpool.Pool) *Outbox { return &Outbox{pool: pool} }

// A paused lane must not begin a new send with a known expired/short lease.
// The database remains the time authority, including across different hosts.
func (o *Outbox) CanSend(parent context.Context, r application.OutboxRecord) (bool, error) {
	ctx, c := context.WithTimeout(parent, 3*time.Second)
	defer c()
	var ready bool
	err := o.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM wallet.outbox_events WHERE event_id=$1 AND published_at IS NULL AND claimed_by=$2 AND claim_expires_at>clock_timestamp()+interval '15 seconds')`, r.EventID, r.Token).Scan(&ready)
	if err != nil {
		return false, safeError(err)
	}
	return ready, nil
}
func (o *Outbox) Claim(parent context.Context, token string, lease time.Duration) (*application.OutboxRecord, error) {
	if token == "" || len(token) > 256 || lease <= 0 || lease > time.Minute {
		return nil, errors.New("invalid outbox claim")
	}
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()
	r := &application.OutboxRecord{Token: token}
	// Single autocommit statement: no financial lock and no transaction held over I/O.
	err := o.pool.QueryRow(ctx, `WITH candidate AS (
 SELECT event_id FROM wallet.outbox_events WHERE published_at IS NULL
 AND next_attempt_at<=clock_timestamp()
 AND (claim_expires_at IS NULL OR claim_expires_at<=clock_timestamp())
 ORDER BY next_attempt_at,event_id LIMIT 1 FOR UPDATE SKIP LOCKED)
 UPDATE wallet.outbox_events e SET claimed_by=$1,
 claim_expires_at=clock_timestamp()+$2*interval '1 millisecond',attempts=e.attempts+1
 FROM candidate c WHERE e.event_id=c.event_id
	 RETURNING e.event_id::text,e.partition_key::text,e.payload::text,e.attempts,
	 COALESCE((SELECT t.causation_id FROM wallet.wager_transactions t
	 WHERE t.id=(e.payload->'data'->>'transactionId')::uuid),'')`, token, lease.Milliseconds()).Scan(&r.EventID, &r.PartitionKey, &r.Payload, &r.Attempts, &r.MessageID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, safeError(err)
	}
	return r, nil
}
func (o *Outbox) Complete(parent context.Context, r application.OutboxRecord) (bool, error) {
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()
	tag, err := o.pool.Exec(ctx, `UPDATE wallet.outbox_events SET published_at=clock_timestamp(),claimed_by=NULL,claim_expires_at=NULL,last_error=NULL
 WHERE event_id=$1 AND published_at IS NULL AND claimed_by=$2 AND claim_expires_at>clock_timestamp()`, r.EventID, r.Token)
	if err != nil {
		return false, safeError(err)
	}
	return tag.RowsAffected() == 1, nil
}
func (o *Outbox) Retry(parent context.Context, r application.OutboxRecord, delay time.Duration) (bool, error) {
	if delay < 0 || delay > time.Minute {
		return false, errors.New("invalid outbox retry")
	}
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()
	tag, err := o.pool.Exec(ctx, `UPDATE wallet.outbox_events SET next_attempt_at=clock_timestamp()+$3*interval '1 millisecond',claimed_by=NULL,claim_expires_at=NULL,last_error='SEND_OR_CONFIRMATION_FAILED'
 WHERE event_id=$1 AND published_at IS NULL AND claimed_by=$2 AND claim_expires_at>clock_timestamp()`, r.EventID, r.Token, delay.Milliseconds())
	if err != nil {
		return false, safeError(err)
	}
	return tag.RowsAffected() == 1, nil
}
