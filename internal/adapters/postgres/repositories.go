package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"jungle-gaming-backend/internal/application"
	"jungle-gaming-backend/internal/domain"
	"jungle-gaming-backend/internal/domain/money"
)

type repositories struct{ tx pgx.Tx }

func (r *repositories) Now(ctx context.Context) (time.Time, error) {
	var now time.Time
	err := r.tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now)
	return now.UTC(), err
}

// Explicit casts allow the domain to retain its own UUID type, with no pgx dependency.
const transactionColumns = `id::text, origin, kind, status, wallet_id::text, player_id::text,
	currency::text, amount, provider_id, external_transaction_id, idempotency_key,
	request_hash, request_hash_version, round_id, game_id, reference_external_transaction_id,
	reference_transaction_id::text, failure_code, result_balance_amount, result_balance_currency::text,
	reference_attempts, next_reference_attempt_at, reference_expires_at, created_at, updated_at`

func readTransaction(row pgx.Row) (*domain.WagerTransaction, error) {
	var s domain.TransactionSnapshot
	var id, wallet, player, currency string
	var units int64
	var provider, external, key, hash, round, game, refExternal, refID, failure, resultCurrency *string
	var hashVersion *int
	var resultUnits *int64
	var next, expires *time.Time
	err := row.Scan(&id, &s.Origin, &s.Kind, &s.Status, &wallet, &player, &currency, &units,
		&provider, &external, &key, &hash, &hashVersion, &round, &game, &refExternal,
		&refID, &failure, &resultUnits, &resultCurrency, &s.ReferenceAttempts, &next, &expires, &s.CreatedAt, &s.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if s.ID, err = domain.ParseID(id); err != nil {
		return nil, err
	}
	if s.WalletID, err = domain.ParseID(wallet); err != nil {
		return nil, err
	}
	if s.PlayerID, err = domain.ParseID(player); err != nil {
		return nil, err
	}
	if s.Money, err = readMoney(units, currency); err != nil {
		return nil, err
	}
	if provider != nil {
		if external == nil || key == nil || hash == nil || hashVersion == nil || round == nil || game == nil {
			return nil, domain.ErrInvariant
		}
		s.External = &domain.ExternalData{ProviderID: *provider, ExternalTransactionID: *external, IdempotencyKey: *key,
			RequestHash: *hash, RequestHashVersion: *hashVersion, RoundID: *round, GameID: *game}
		if refExternal != nil {
			s.External.ReferenceExternalTransactionID = *refExternal
		}
	}
	if refID != nil {
		if s.ReferenceTransactionID, err = domain.ParseID(*refID); err != nil {
			return nil, err
		}
	}
	if failure != nil {
		s.FailureCode = domain.FailureCode(*failure)
	}
	if resultUnits != nil {
		if resultCurrency == nil {
			return nil, domain.ErrInvariant
		}
		m, err := readMoney(*resultUnits, *resultCurrency)
		if err != nil {
			return nil, err
		}
		s.ResultBalance = &m
	}
	if next != nil {
		s.NextReferenceAttemptAt = *next
	}
	if expires != nil {
		s.ReferenceExpiresAt = *expires
	}
	return domain.RehydrateTransaction(s)
}

func readMoney(units int64, currency string) (money.Money, error) {
	c, err := money.ParseCurrency(currency)
	if err != nil {
		return money.Money{}, err
	}
	return money.FromMinorUnits(units, c)
}

func (r *repositories) FindDuplicate(ctx context.Context, req domain.ExternalRequest) (*domain.WagerTransaction, error) {
	// Key has precedence when both identifiers collide with different rows.
	return readTransaction(r.tx.QueryRow(ctx, `SELECT `+transactionColumns+` FROM wallet.wager_transactions
		WHERE provider_id=$1 AND (idempotency_key=$2 OR external_transaction_id=$3)
		ORDER BY (idempotency_key=$2) DESC LIMIT 1`, req.ProviderID(), req.IdempotencyKey(), req.ExternalTransactionID()))
}

func (r *repositories) LockWallet(ctx context.Context, id domain.ID) (*domain.Wallet, error) {
	return readWallet(r.tx.QueryRow(ctx, `SELECT `+walletColumns+` FROM wallet.wallets WHERE id=$1 FOR UPDATE`, id.String()))
}

func (r *repositories) FindReference(ctx context.Context, provider, external string) (domain.ReferenceLookup, error) {
	ref, err := readTransaction(r.tx.QueryRow(ctx, `SELECT `+transactionColumns+` FROM wallet.wager_transactions
		WHERE provider_id=$1 AND external_transaction_id=$2`, provider, external))
	if err != nil || ref == nil {
		return domain.ReferenceLookup{}, err
	}
	lookup := domain.ReferenceLookup{Reference: ref}
	err = r.tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM wallet.wager_transactions
		WHERE reference_transaction_id=$1 AND kind IN ('REFUND','ROLLBACK') AND status='PROCESSED')`, ref.ID().String()).Scan(&lookup.AlreadyReversed)
	return lookup, err
}

func (r *repositories) InsertWallet(ctx context.Context, w *domain.Wallet) error {
	s := w.Snapshot()
	_, err := r.tx.Exec(ctx, `INSERT INTO wallet.wallets (id,player_id,currency,balance_amount,version,created_at,updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`, s.ID.String(), s.PlayerID.String(), s.Balance.Currency().String(), s.Balance.MinorUnits(), s.Version, s.CreatedAt, s.UpdatedAt)
	return err
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
func nullableID(id domain.ID) any {
	if id.IsZero() {
		return nil
	}
	return id.String()
}
func nullableTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

func (r *repositories) InsertTransaction(ctx context.Context, tx *domain.WagerTransaction, trace application.Trace) error {
	s := tx.Snapshot()
	var provider, external, key, hash, hashVersion, round, game, refExternal any
	if e := s.External; e != nil {
		provider, external, key, hash, hashVersion, round, game, refExternal = e.ProviderID, e.ExternalTransactionID, e.IdempotencyKey,
			e.RequestHash, e.RequestHashVersion, e.RoundID, e.GameID, nullable(e.ReferenceExternalTransactionID)
	}
	var resultUnits, resultCurrency any
	if s.ResultBalance != nil {
		resultUnits, resultCurrency = s.ResultBalance.MinorUnits(), s.ResultBalance.Currency().String()
	}
	tag, err := r.tx.Exec(ctx, `INSERT INTO wallet.wager_transactions
		(id,origin,kind,status,wallet_id,player_id,currency,amount,provider_id,external_transaction_id,idempotency_key,
		request_hash,request_hash_version,round_id,game_id,reference_external_transaction_id,reference_transaction_id,
		failure_code,result_balance_amount,result_balance_currency,reference_attempts,next_reference_attempt_at,
		reference_expires_at,correlation_id,causation_id,created_at,updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27)
		ON CONFLICT ON CONSTRAINT tx_provider_idempotency_key_unique DO NOTHING`, s.ID.String(), string(s.Origin), string(s.Kind), string(s.Status), s.WalletID.String(), s.PlayerID.String(),
		s.Money.Currency().String(), s.Money.MinorUnits(), provider, external, key, hash, hashVersion, round, game, refExternal,
		nullableID(s.ReferenceTransactionID), nullable(string(s.FailureCode)), resultUnits, resultCurrency, s.ReferenceAttempts,
		nullableTime(s.NextReferenceAttemptAt), nullableTime(s.ReferenceExpiresAt), trace.CorrelationID, nullable(trace.CausationID), s.CreatedAt, s.UpdatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "tx_provider_external_id_unique" {
			return application.ErrConcurrentDuplicate
		}
		return err
	}
	if tag.RowsAffected() != 1 {
		return application.ErrConcurrentDuplicate
	}
	return nil
}

func (r *repositories) InsertLedger(ctx context.Context, e domain.LedgerEntry) error {
	d := e.Data()
	_, err := r.tx.Exec(ctx, `INSERT INTO wallet.ledger_entries
		(id,wallet_id,currency,transaction_id,direction,amount,balance_before,balance_after,wallet_version,created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, d.ID.String(), d.WalletID.String(), d.Amount.Currency().String(), d.TransactionID.String(),
		string(d.Direction), d.Amount.MinorUnits(), d.BalanceBefore.MinorUnits(), d.BalanceAfter.MinorUnits(), d.WalletVersion, d.CreatedAt)
	return err
}

func (r *repositories) UpdateWallet(ctx context.Context, w *domain.Wallet, expectedVersion int64) error {
	s := w.Snapshot()
	tag, err := r.tx.Exec(ctx, `UPDATE wallet.wallets SET balance_amount=$2,version=$3,updated_at=$4 WHERE id=$1 AND version=$5`,
		s.ID.String(), s.Balance.MinorUnits(), s.Version, s.UpdatedAt, expectedVersion)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("%w: wallet version changed", domain.ErrInvariant)
	}
	return nil
}

func (r *repositories) InsertEvent(ctx context.Context, envelope domain.Envelope) error {
	payload, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	e := envelope.Event()
	aggregateType := "WagerTransaction"
	if e.EventType() == "WalletBalanceChanged" {
		aggregateType = "Wallet"
	}
	_, err = r.tx.Exec(ctx, `INSERT INTO wallet.outbox_events
		(event_id,aggregate_type,aggregate_id,partition_key,event_type,event_version,payload,occurred_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb,$8)`, envelope.EventID().String(), aggregateType, e.AggregateID().String(),
		e.PartitionKey().String(), e.EventType(), e.EventVersion(), string(payload), e.OccurredAt())
	return err
}
