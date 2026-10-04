package postgres

import (
	"errors"
	"github.com/jackc/pgx/v5"
	"jungle-gaming-backend/internal/application"
	"jungle-gaming-backend/internal/domain"
)

const walletColumns = `id::text,player_id::text,currency::text,balance_amount,version,created_at,updated_at`

func readWallet(row pgx.Row) (*domain.Wallet, error) {
	var s domain.WalletSnapshot
	var id, player, currency string
	var units int64
	err := row.Scan(&id, &player, &currency, &units, &s.Version, &s.CreatedAt, &s.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, application.ErrWalletNotFound
	}
	if err != nil {
		return nil, err
	}
	if s.ID, err = domain.ParseID(id); err != nil {
		return nil, err
	}
	if s.PlayerID, err = domain.ParseID(player); err != nil {
		return nil, err
	}
	if s.Balance, err = readMoney(units, currency); err != nil {
		return nil, err
	}
	w, err := domain.RehydrateWallet(s)
	if err != nil {
		return nil, err
	}
	return w, nil
}

func walletRow(row pgx.Row) (domain.WalletSnapshot, error) {
	w, err := readWallet(row)
	if err != nil {
		return domain.WalletSnapshot{}, safeError(err)
	}
	return w.Snapshot(), nil
}
