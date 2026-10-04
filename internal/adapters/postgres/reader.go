package postgres

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"jungle-gaming-backend/internal/application"
	"jungle-gaming-backend/internal/domain"
	"jungle-gaming-backend/internal/domain/money"
)

type Reader struct {
	pool    *pgxpool.Pool
	timeout time.Duration
}

func NewReader(pool *pgxpool.Pool, timeout time.Duration) *Reader { return &Reader{pool, timeout} }

func (r *Reader) Wallet(parent context.Context, id domain.ID) (domain.WalletSnapshot, error) {
	ctx, cancel := context.WithTimeout(parent, r.timeout)
	defer cancel()
	return walletRow(r.pool.QueryRow(ctx, `SELECT `+walletColumns+` FROM wallet.wallets WHERE id=$1`, id.String()))
}
func (r *Reader) WalletForPlayer(parent context.Context, id domain.ID, c money.Currency) (domain.WalletSnapshot, error) {
	ctx, cancel := context.WithTimeout(parent, r.timeout)
	defer cancel()
	return walletRow(r.pool.QueryRow(ctx, `SELECT `+walletColumns+` FROM wallet.wallets WHERE player_id=$1 AND currency=$2`, id.String(), c.String()))
}
func (r *Reader) Transaction(parent context.Context, id domain.ID, provider string) (domain.TransactionSnapshot, error) {
	ctx, cancel := context.WithTimeout(parent, r.timeout)
	defer cancel()
	tx, err := readTransaction(r.pool.QueryRow(ctx, `SELECT `+transactionColumns+` FROM wallet.wager_transactions WHERE id=$1 AND ($2='' OR provider_id=$2)`, id.String(), provider))
	return transactionResult(tx, err)
}
func (r *Reader) ExternalTransaction(parent context.Context, provider, external string) (domain.TransactionSnapshot, error) {
	ctx, cancel := context.WithTimeout(parent, r.timeout)
	defer cancel()
	tx, err := readTransaction(r.pool.QueryRow(ctx, `SELECT `+transactionColumns+` FROM wallet.wager_transactions WHERE provider_id=$1 AND external_transaction_id=$2`, provider, external))
	return transactionResult(tx, err)
}
func transactionResult(tx *domain.WagerTransaction, err error) (domain.TransactionSnapshot, error) {
	if err != nil {
		return domain.TransactionSnapshot{}, safeError(err)
	}
	if tx == nil {
		return domain.TransactionSnapshot{}, application.ErrTransactionNotFound
	}
	return tx.Snapshot(), nil
}

type ledgerCursor struct {
	Wallet  string `json:"w"`
	Version int64  `json:"v"`
	ID      string `json:"i"`
}

func decodeCursor(raw string, wallet domain.ID) (ledgerCursor, error) {
	c := ledgerCursor{Wallet: wallet.String(), ID: "00000000-0000-0000-0000-000000000000"}
	if raw == "" {
		return c, nil
	}
	if len(raw) > 512 {
		return c, application.ErrInvalidCursor
	}
	b, err := base64.RawURLEncoding.Strict().DecodeString(raw)
	if err != nil {
		return c, application.ErrInvalidCursor
	}
	if json.Unmarshal(b, &c) != nil || c.Wallet != wallet.String() || c.Version < 1 {
		return c, application.ErrInvalidCursor
	}
	canonical, _ := json.Marshal(c)
	if !bytes.Equal(b, canonical) {
		return c, application.ErrInvalidCursor
	}
	if _, err = domain.ParseID(c.ID); err != nil {
		return c, application.ErrInvalidCursor
	}
	return c, nil
}
func (r *Reader) Ledger(parent context.Context, wallet domain.ID, raw string, limit int) (application.LedgerPage, error) {
	if limit < 1 || limit > 200 {
		return application.LedgerPage{}, application.ErrInvalidCursor
	}
	c, err := decodeCursor(raw, wallet)
	if err != nil {
		return application.LedgerPage{}, err
	}
	ctx, cancel := context.WithTimeout(parent, r.timeout)
	defer cancel()
	if _, err = r.Wallet(ctx, wallet); err != nil {
		return application.LedgerPage{}, err
	}
	rows, err := r.pool.Query(ctx, `SELECT id::text,transaction_id::text,currency::text,direction,amount,balance_before,balance_after,wallet_version,created_at
		FROM wallet.ledger_entries WHERE wallet_id=$1 AND (wallet_version,id)>($2,$3::uuid) ORDER BY wallet_version,id LIMIT $4`, wallet.String(), c.Version, c.ID, limit+1)
	if err != nil {
		return application.LedgerPage{}, safeError(err)
	}
	defer rows.Close()
	page := application.LedgerPage{Entries: make([]domain.LedgerEntryData, 0, limit)}
	for rows.Next() {
		var e domain.LedgerEntryData
		var id, tx, currency string
		var amount, before, after int64
		err = rows.Scan(&id, &tx, &currency, &e.Direction, &amount, &before, &after, &e.WalletVersion, &e.CreatedAt)
		if err != nil {
			return page, safeError(err)
		}
		e.WalletID = wallet
		if e.ID, err = domain.ParseID(id); err != nil {
			return page, err
		}
		if e.TransactionID, err = domain.ParseID(tx); err != nil {
			return page, err
		}
		if e.Amount, err = readMoney(amount, currency); err != nil {
			return page, err
		}
		if e.BalanceBefore, err = readMoney(before, currency); err != nil {
			return page, err
		}
		if e.BalanceAfter, err = readMoney(after, currency); err != nil {
			return page, err
		}
		if _, err = domain.RehydrateLedgerEntry(e); err != nil {
			return page, err
		}
		page.Entries = append(page.Entries, e)
	}
	if err = rows.Err(); err != nil {
		return page, safeError(err)
	}
	if len(page.Entries) > limit {
		page.Entries = page.Entries[:limit]
		last := page.Entries[limit-1]
		b, _ := json.Marshal(ledgerCursor{wallet.String(), last.WalletVersion, last.ID.String()})
		page.NextCursor = base64.RawURLEncoding.EncodeToString(b)
	}
	return page, nil
}
