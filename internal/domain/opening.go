package domain

import (
	"time"

	"jungle-gaming-backend/internal/domain/money"
)

// OpenedWallet é o resultado da abertura, a ser gravado num único commit.
type OpenedWallet struct {
	Wallet  *Wallet
	Opening *WagerTransaction // nil quando o saldo inicial é zero
	Entry   *LedgerEntry      // nil quando o saldo inicial é zero
	Events  []Event           // vazio quando o saldo inicial é zero
}

// OpenWallet cria uma carteira na versão 1. Com saldo inicial positivo, cria
// também a transação interna OPENING (PROCESSED), o lançamento de crédito
// (0 → saldo inicial) e os eventos WagerTransactionProcessed e
// WalletBalanceChanged. Saldo zero cria só a carteira (enunciado §9).
func OpenWallet(ids IDGenerator, playerID ID, initial money.Money, now time.Time) (OpenedWallet, error) {
	if ids == nil {
		return OpenedWallet{}, ErrUninitialized
	}
	if playerID.IsZero() {
		return OpenedWallet{}, invalid(CodeValidationError, "playerId", "must be a UUID")
	}
	if !initial.Valid() || initial.IsNegative() {
		return OpenedWallet{}, invalid(CodeInvalidAmount, "initialBalance", "must be a valid non-negative amount")
	}
	if now.IsZero() {
		return OpenedWallet{}, invariant("time is required")
	}
	now = normTime(now)
	w := &Wallet{id: ids.NewID(), playerID: playerID, balance: initial, version: 1, createdAt: now, updatedAt: now}
	if initial.IsZero() {
		return OpenedWallet{Wallet: w}, nil
	}

	zero, err := money.Zero(initial.Currency())
	if err != nil {
		return OpenedWallet{}, err
	}
	tx := newOpeningTransaction(ids.NewID(), w.id, playerID, initial, now)
	entry, err := NewLedgerEntry(LedgerEntryData{
		ID: ids.NewID(), WalletID: w.id, TransactionID: tx.id, Direction: Credit,
		Amount: initial, BalanceBefore: zero, BalanceAfter: initial, WalletVersion: 1, CreatedAt: now,
	})
	if err != nil {
		return OpenedWallet{}, err
	}
	if err := tx.MarkProcessed(initial, ID{}, now); err != nil {
		return OpenedWallet{}, err
	}
	return OpenedWallet{
		Wallet: w, Opening: tx, Entry: &entry,
		Events: []Event{newWagerTransactionProcessed(tx), newWalletBalanceChanged(entry)},
	}, nil
}
