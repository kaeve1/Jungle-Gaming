package domain

import (
	"errors"
	"slices"
	"testing"

	"jungle-gaming-backend/internal/domain/money"
)

func TestOpenWalletWithPositiveBalance(t *testing.T) {
	ids := &seqIDs{}
	o, err := OpenWallet(ids, playerA, brl("1000.00"), t0)
	if err != nil {
		t.Fatal(err)
	}
	if o.Wallet.Version() != 1 || o.Wallet.Balance() != brl("1000.00") || o.Wallet.PlayerID() != playerA {
		t.Errorf("wallet %+v", o.Wallet.Snapshot())
	}
	tx := o.Opening
	if tx == nil || tx.Kind() != KindOpening || tx.Origin() != OriginInternal || tx.Status() != StatusProcessed || tx.External() != nil {
		t.Fatalf("opening transaction %+v", tx)
	}
	e := o.Entry
	if e == nil || e.Direction() != Credit || !e.BalanceBefore().IsZero() || e.BalanceAfter() != brl("1000.00") ||
		e.WalletVersion() != 1 || e.TransactionID() != tx.ID() {
		t.Fatalf("opening entry %+v", e)
	}
	if !slices.Equal(eventTypes(o.Events), []string{"WagerTransactionProcessed", "WalletBalanceChanged"}) {
		t.Errorf("events %v", eventTypes(o.Events))
	}
	p := o.Events[0].(WagerTransactionProcessed)
	if p.ProviderID != nil || p.ExternalTransactionID != nil || p.RoundID != nil || p.GameID != nil {
		t.Error("internal events must not carry external metadata")
	}
	if _, err := RehydrateTransaction(tx.Snapshot()); err != nil {
		t.Errorf("opening must be persistable and rehydratable: %v", err)
	}
}

func TestOpenWalletWithZeroBalance(t *testing.T) {
	o, err := OpenWallet(&seqIDs{}, playerA, brl("0.00"), t0)
	if err != nil {
		t.Fatal(err)
	}
	if o.Opening != nil || o.Entry != nil || len(o.Events) != 0 {
		t.Error("zero opening creates no OPENING, ledger entry or events")
	}
	if o.Wallet.Version() != 1 || !o.Wallet.Balance().IsZero() {
		t.Errorf("wallet %+v", o.Wallet.Snapshot())
	}
}

func TestOpenWalletValidation(t *testing.T) {
	negative, _ := money.ParseSigned("-1.00", "BRL")
	for name, amount := range map[string]money.Money{"negative": negative, "uninitialized": {}} {
		if _, err := OpenWallet(&seqIDs{}, playerA, amount, t0); !errors.Is(err, ErrValidation) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := OpenWallet(&seqIDs{}, ID{}, brl("1.00"), t0); !errors.Is(err, ErrValidation) {
		t.Errorf("no player: %v", err)
	}
}
