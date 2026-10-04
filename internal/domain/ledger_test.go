package domain

import (
	"errors"
	"testing"
)

func TestLedgerEntryEquation(t *testing.T) {
	base := LedgerEntryData{
		ID: mustID("0192f291-27de-7aaa-8000-000000000001"), WalletID: walletA,
		TransactionID: mustID("0192f291-27de-7aaa-8000-000000000002"),
		Direction:     Debit, Amount: brl("25.00"), BalanceBefore: brl("100.00"), BalanceAfter: brl("75.00"),
		WalletVersion: 2, CreatedAt: t0,
	}
	if _, err := NewLedgerEntry(base); err != nil {
		t.Fatalf("valid debit: %v", err)
	}
	credit := base
	credit.Direction, credit.BalanceAfter = Credit, brl("125.00")
	if _, err := NewLedgerEntry(credit); err != nil {
		t.Fatalf("valid credit: %v", err)
	}

	bad := map[string]func(*LedgerEntryData){
		"wrong after (debit)":   func(d *LedgerEntryData) { d.BalanceAfter = brl("76.00") },
		"credit with debit sum": func(d *LedgerEntryData) { d.Direction = Credit },
		"zero amount":           func(d *LedgerEntryData) { d.Amount, d.BalanceAfter = brl("0.00"), d.BalanceBefore },
		"negative after":        func(d *LedgerEntryData) { d.Amount, d.BalanceAfter = brl("150.00"), brl("0.00") },
		"mixed currency":        func(d *LedgerEntryData) { d.Amount = usd("25.00") },
		"bad direction":         func(d *LedgerEntryData) { d.Direction = "SIDEWAYS" },
		"no transaction":        func(d *LedgerEntryData) { d.TransactionID = ID{} },
		"version 0":             func(d *LedgerEntryData) { d.WalletVersion = 0 },
	}
	for name, mutate := range bad {
		d := base
		mutate(&d)
		if _, err := NewLedgerEntry(d); !errors.Is(err, ErrInvariant) {
			t.Errorf("%s: error = %v", name, err)
		}
		if _, err := RehydrateLedgerEntry(d); !errors.Is(err, ErrInvariant) {
			t.Errorf("%s (rehydrate): error = %v", name, err)
		}
	}
}

func TestLedgerEntryIsReadOnly(t *testing.T) {
	ids := &seqIDs{}
	w := wallet(t, brl("10.00"))
	e, err := w.Debit(brl("1.00"), ids.NewID(), ids.NewID(), t0)
	if err != nil {
		t.Fatal(err)
	}
	d := e.Data()
	d.BalanceAfter = brl("999.00") // mexer na cópia não altera o lançamento
	if e.BalanceAfter() != brl("9.00") {
		t.Error("LedgerEntry must be immutable")
	}
}
