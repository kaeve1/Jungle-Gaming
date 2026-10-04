package postgres

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"jungle-gaming-backend/internal/application"
	"jungle-gaming-backend/internal/domain"
	"jungle-gaming-backend/internal/domain/money"
	"math/big"
)

// One SQL statement means one MVCC snapshot for wallet, sum and count. SUM(bigint)
// is PostgreSQL NUMERIC; big.Int checks the final value without intermediate overflow.
func (r *Reader) Reconcile(parent context.Context, id domain.ID) (application.Reconciliation, error) {
	ctx, c := context.WithTimeout(parent, r.timeout)
	defer c()
	out := application.Reconciliation{WalletID: id}
	var stored int64
	var currency, calculated string
	err := r.pool.QueryRow(ctx, `SELECT w.balance_amount,w.currency,COALESCE(l.balance,0)::text,l.entries
 FROM wallet.wallets w CROSS JOIN LATERAL (
 SELECT sum(CASE direction WHEN 'CREDIT' THEN amount ELSE -amount END) balance,count(*) entries
 FROM wallet.ledger_entries WHERE wallet_id=w.id) l WHERE w.id=$1`, id.String()).Scan(&stored, &currency, &calculated, &out.CheckedEntries)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, application.ErrWalletNotFound
	}
	if err != nil {
		return out, safeError(err)
	}
	value, ok := new(big.Int).SetString(calculated, 10)
	if !ok || !value.IsInt64() {
		return out, money.ErrOverflow
	}
	curr, err := money.ParseCurrency(currency)
	if err != nil {
		return out, err
	}
	out.StoredBalance, err = money.FromMinorUnits(stored, curr)
	if err != nil {
		return out, err
	}
	out.CalculatedBalance, err = money.FromMinorUnits(value.Int64(), curr)
	if err != nil {
		return out, err
	}
	out.Difference, err = out.StoredBalance.Sub(out.CalculatedBalance)
	if err != nil {
		return out, err
	}
	out.Consistent = out.Difference.IsZero()
	return out, nil
}
