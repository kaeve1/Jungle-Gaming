package domain

import (
	"time"

	"jungle-gaming-backend/internal/domain/money"
)

// Direction é o sentido do lançamento.
type Direction string

const (
	Debit  Direction = "DEBIT"
	Credit Direction = "CREDIT"
)

func (d Direction) valid() bool { return d == Debit || d == Credit }

// LedgerEntry é um lançamento imutável: só tem leitura depois de construído.
type LedgerEntry struct {
	d LedgerEntryData
}

// LedgerEntryData são os campos de um lançamento (para construção e persistência).
type LedgerEntryData struct {
	ID            ID
	WalletID      ID
	TransactionID ID
	Direction     Direction
	Amount        money.Money
	BalanceBefore money.Money
	BalanceAfter  money.Money
	// WalletVersion é a versão da carteira depois deste lançamento; encadeia
	// os lançamentos e dá a ordem estável do ledger.
	WalletVersion int64
	CreatedAt     time.Time
}

// NewLedgerEntry valida e cria um lançamento. A equação
// balanceAfter = balanceBefore ± amount é verificada aqui.
func NewLedgerEntry(d LedgerEntryData) (LedgerEntry, error) {
	d.CreatedAt = normTime(d.CreatedAt)
	if err := d.validate(); err != nil {
		return LedgerEntry{}, err
	}
	return LedgerEntry{d: d}, nil
}

// RehydrateLedgerEntry reconstrói um lançamento lido do banco, com as mesmas
// validações (dados corrompidos são recusados, não corrigidos).
func RehydrateLedgerEntry(d LedgerEntryData) (LedgerEntry, error) { return NewLedgerEntry(d) }

func (d LedgerEntryData) validate() error {
	switch {
	case d.ID.IsZero() || d.WalletID.IsZero() || d.TransactionID.IsZero():
		return invariant("ledger entry ids are required")
	case !d.Direction.valid():
		return invariant("invalid ledger direction %q", d.Direction)
	case !d.Amount.IsPositive():
		return invariant("ledger amount must be positive, got %s", d.Amount)
	case d.BalanceBefore.IsNegative() || d.BalanceAfter.IsNegative() || !d.BalanceBefore.Valid() || !d.BalanceAfter.Valid():
		return invariant("ledger balances must be valid and non-negative")
	case d.WalletVersion < 1:
		return invariant("ledger wallet version must be >= 1")
	case d.CreatedAt.IsZero():
		return invariant("ledger createdAt is required")
	}
	var expected money.Money
	var err error
	if d.Direction == Credit {
		expected, err = d.BalanceBefore.Add(d.Amount)
	} else {
		expected, err = d.BalanceBefore.Sub(d.Amount)
	}
	if err != nil {
		return invariant("ledger equation: %v", err)
	}
	if expected != d.BalanceAfter {
		return invariant("ledger equation: %s %s %s != %s", d.BalanceBefore, d.Direction, d.Amount, d.BalanceAfter)
	}
	return nil
}

// Data devolve uma cópia dos campos.
func (e LedgerEntry) Data() LedgerEntryData { return e.d }

func (e LedgerEntry) ID() ID                     { return e.d.ID }
func (e LedgerEntry) WalletID() ID               { return e.d.WalletID }
func (e LedgerEntry) TransactionID() ID          { return e.d.TransactionID }
func (e LedgerEntry) Direction() Direction       { return e.d.Direction }
func (e LedgerEntry) Amount() money.Money        { return e.d.Amount }
func (e LedgerEntry) BalanceBefore() money.Money { return e.d.BalanceBefore }
func (e LedgerEntry) BalanceAfter() money.Money  { return e.d.BalanceAfter }
func (e LedgerEntry) WalletVersion() int64       { return e.d.WalletVersion }
func (e LedgerEntry) CreatedAt() time.Time       { return e.d.CreatedAt }
