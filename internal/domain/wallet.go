package domain

import (
	"errors"
	"fmt"
	"time"

	"jungle-gaming-backend/internal/domain/money"
)

// Wallet é a raiz do agregado financeiro. O saldo só muda por Debit/Credit,
// que produzem o lançamento correspondente; a persistência grava os dois na
// mesma transação SQL (ARCHITECTURE.md §4).
//
// Não é segura para uso concorrente: cada operação carrega sua própria cópia
// sob o lock da linha no banco. A coordenação entre goroutines e processos é
// do PostgreSQL, nunca de memória compartilhada.
type Wallet struct {
	id        ID
	playerID  ID
	balance   money.Money
	version   int64
	createdAt time.Time
	updatedAt time.Time
}

// WalletSnapshot são os campos persistidos de uma carteira.
type WalletSnapshot struct {
	ID        ID
	PlayerID  ID
	Balance   money.Money
	Version   int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// RehydrateWallet reconstrói uma carteira lida do banco. Não gera lançamentos
// nem eventos; dados inconsistentes são recusados.
func RehydrateWallet(s WalletSnapshot) (*Wallet, error) {
	s.CreatedAt, s.UpdatedAt = normTime(s.CreatedAt), normTime(s.UpdatedAt)
	switch {
	case s.ID.IsZero() || s.PlayerID.IsZero():
		return nil, invariant("wallet ids are required")
	case !s.Balance.Valid() || s.Balance.IsNegative():
		return nil, invariant("wallet balance must be valid and non-negative")
	case s.Version < 1:
		return nil, invariant("wallet version must be >= 1")
	case s.CreatedAt.IsZero() || s.UpdatedAt.Before(s.CreatedAt):
		return nil, invariant("wallet timestamps are inconsistent")
	}
	return &Wallet{s.ID, s.PlayerID, s.Balance, s.Version, s.CreatedAt, s.UpdatedAt}, nil
}

// Snapshot devolve os campos para persistência.
func (w *Wallet) Snapshot() WalletSnapshot {
	return WalletSnapshot{w.id, w.playerID, w.balance, w.version, w.createdAt, w.updatedAt}
}

func (w *Wallet) ID() ID                   { return w.id }
func (w *Wallet) PlayerID() ID             { return w.playerID }
func (w *Wallet) Currency() money.Currency { return w.balance.Currency() }
func (w *Wallet) Balance() money.Money     { return w.balance }
func (w *Wallet) Version() int64           { return w.version }
func (w *Wallet) CreatedAt() time.Time     { return w.createdAt }
func (w *Wallet) UpdatedAt() time.Time     { return w.updatedAt }

// Debit retira amount do saldo. Saldo insuficiente → ErrInsufficientFunds,
// sem alterar a carteira.
func (w *Wallet) Debit(amount money.Money, transactionID, entryID ID, now time.Time) (LedgerEntry, error) {
	return w.move(Debit, amount, transactionID, entryID, now)
}

// Credit acrescenta amount ao saldo. Estouro do limite → ErrBalanceLimitExceeded.
func (w *Wallet) Credit(amount money.Money, transactionID, entryID ID, now time.Time) (LedgerEntry, error) {
	return w.move(Credit, amount, transactionID, entryID, now)
}

func (w *Wallet) move(dir Direction, amount money.Money, transactionID, entryID ID, now time.Time) (LedgerEntry, error) {
	if w == nil || w.id.IsZero() {
		return LedgerEntry{}, ErrUninitialized
	}
	if !amount.Valid() {
		return LedgerEntry{}, money.ErrUninitialized
	}
	if amount.Currency() != w.Currency() {
		return LedgerEntry{}, fmt.Errorf("%w: wallet %s, amount %s", money.ErrCurrencyMismatch, w.Currency(), amount.Currency())
	}
	if !amount.IsPositive() {
		return LedgerEntry{}, ErrNonPositiveAmount
	}
	var after money.Money
	var err error
	if dir == Credit {
		after, err = w.balance.Add(amount)
		if errors.Is(err, money.ErrOverflow) {
			return LedgerEntry{}, ErrBalanceLimitExceeded
		}
	} else {
		after, err = w.balance.Sub(amount)
		if err == nil && after.IsNegative() {
			return LedgerEntry{}, ErrInsufficientFunds
		}
	}
	if err != nil {
		return LedgerEntry{}, err
	}
	now = normTime(now)
	entry, err := NewLedgerEntry(LedgerEntryData{
		ID: entryID, WalletID: w.id, TransactionID: transactionID, Direction: dir,
		Amount: amount, BalanceBefore: w.balance, BalanceAfter: after,
		WalletVersion: w.version + 1, CreatedAt: now,
	})
	if err != nil {
		return LedgerEntry{}, err
	}
	// Só altera o estado depois de tudo validado.
	w.balance = after
	w.version++
	if now.After(w.updatedAt) {
		w.updatedAt = now
	}
	return entry, nil
}
