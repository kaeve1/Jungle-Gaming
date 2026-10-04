package postgres

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"jungle-gaming-backend/internal/application"
	"jungle-gaming-backend/internal/domain"
	"time"
)

func (r *repositories) ClaimReference(ctx context.Context) (*domain.Wallet, *domain.WagerTransaction, application.Trace, error) {
	var id string
	// Never lock a pending transaction before its wallet. Busy wallets are skipped.
	err := r.tx.QueryRow(ctx, `SELECT w.id::text FROM wallet.wallets w
 JOIN (SELECT wallet_id,min(next_reference_attempt_at) AS due FROM wallet.wager_transactions
 WHERE status='PENDING_REFERENCE' AND next_reference_attempt_at <= clock_timestamp()
 GROUP BY wallet_id) p ON p.wallet_id=w.id
 ORDER BY p.due,w.id LIMIT 1 FOR UPDATE OF w SKIP LOCKED`).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, application.Trace{}, nil
	}
	if err != nil {
		return nil, nil, application.Trace{}, err
	}
	walletID, err := domain.ParseID(id)
	if err != nil {
		return nil, nil, application.Trace{}, err
	}
	w, err := r.LockWallet(ctx, walletID)
	if err != nil {
		return nil, nil, application.Trace{}, err
	}
	tx, err := readTransaction(r.tx.QueryRow(ctx, `SELECT `+transactionColumns+` FROM wallet.wager_transactions
 WHERE wallet_id=$1 AND status='PENDING_REFERENCE' AND next_reference_attempt_at <= clock_timestamp()
 ORDER BY next_reference_attempt_at,id LIMIT 1 FOR UPDATE`, id))
	if err != nil || tx == nil {
		return nil, nil, application.Trace{}, err
	}
	var trace application.Trace
	// Keep original correlation; resolution is caused by the durable pending event.
	err = r.tx.QueryRow(ctx, `SELECT t.correlation_id, e.event_id::text,COALESCE(t.causation_id,'')
 FROM wallet.wager_transactions t JOIN wallet.outbox_events e
 ON e.aggregate_id=t.id AND e.event_type='WagerTransactionPendingReference'
 WHERE t.id=$1`, tx.ID().String()).Scan(&trace.CorrelationID, &trace.CausationID, &trace.MessageID)
	if errors.Is(err, pgx.ErrNoRows) {
		err = fmt.Errorf("%w: missing durable pending event", domain.ErrInvariant)
	}
	return w, tx, trace, err
}

func (r *repositories) UpdateReference(ctx context.Context, tx *domain.WagerTransaction) error {
	s := tx.Snapshot()
	var units, currency any
	if s.ResultBalance != nil {
		units = s.ResultBalance.MinorUnits()
		currency = s.ResultBalance.Currency().String()
	}
	tag, err := r.tx.Exec(ctx, `UPDATE wallet.wager_transactions SET status=$2,failure_code=$3,
 result_balance_amount=$4,result_balance_currency=$5,reference_transaction_id=$6,
 reference_attempts=$7,next_reference_attempt_at=$8,reference_expires_at=$9,updated_at=$10
 WHERE id=$1 AND status='PENDING_REFERENCE'`, s.ID.String(), string(s.Status), nullable(string(s.FailureCode)),
		units, currency, nullableID(s.ReferenceTransactionID), s.ReferenceAttempts, nullableTime(s.NextReferenceAttemptAt), nullableTime(s.ReferenceExpiresAt), s.UpdatedAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return domain.ErrInvariant
	}
	return nil
}

// Called only after the financial attempt rolled back. No financial write here.
func (r *repositories) RecordReferenceFailure(ctx context.Context, wallet, id domain.ID, delay time.Duration) (domain.Status, error) {
	if _, err := r.LockWallet(ctx, wallet); err != nil {
		return "", err
	}
	tx, err := readTransaction(r.tx.QueryRow(ctx, `SELECT `+transactionColumns+` FROM wallet.wager_transactions
 WHERE id=$1 AND wallet_id=$2 AND status='PENDING_REFERENCE' FOR UPDATE`, id.String(), wallet.String()))
	if err != nil || tx == nil {
		return "", err
	}
	now, err := r.Now(ctx)
	if err != nil {
		return "", err
	}
	var failures int
	err = r.tx.QueryRow(ctx, `UPDATE wallet.wager_transactions SET reference_failure_attempts=reference_failure_attempts+1,
 next_reference_attempt_at=LEAST(reference_expires_at,$2::timestamptz + $3 * interval '1 millisecond'),updated_at=$2
 WHERE id=$1 RETURNING reference_failure_attempts`, id.String(), now, max(1, delay.Milliseconds())).Scan(&failures)
	if err != nil {
		return "", err
	}
	if failures == 3 {
		if err = tx.Fail(domain.CodeInternalProcessingFailed, now); err != nil {
			return "", err
		}
		if err = r.UpdateReference(ctx, tx); err != nil {
			return "", err
		}
	}
	return tx.Status(), nil
}
