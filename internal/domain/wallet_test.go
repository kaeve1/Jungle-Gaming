package domain

import (
	"errors"
	"testing"
	"time"

	"jungle-gaming-backend/internal/domain/money"
)

func TestWalletDebitCredit(t *testing.T) {
	ids := &seqIDs{}
	w := wallet(t, brl("100.00"))

	e, err := w.Debit(brl("80.00"), ids.NewID(), ids.NewID(), t0)
	if err != nil {
		t.Fatal(err)
	}
	if w.Balance() != brl("20.00") || w.Version() != 2 {
		t.Errorf("after debit: %s v%d", w.Balance(), w.Version())
	}
	if e.Direction() != Debit || e.BalanceBefore() != brl("100.00") || e.BalanceAfter() != brl("20.00") || e.WalletVersion() != 2 {
		t.Errorf("entry: %+v", e.Data())
	}

	if _, err := w.Credit(brl("5.50"), ids.NewID(), ids.NewID(), t0); err != nil {
		t.Fatal(err)
	}
	if w.Balance() != brl("25.50") || w.Version() != 3 {
		t.Errorf("after credit: %s v%d", w.Balance(), w.Version())
	}
}

func TestWalletRejectsInvalidMovementsWithoutChangingState(t *testing.T) {
	ids := &seqIDs{}
	maxBRL, _ := money.FromMinorUnits(1<<62, money.BRL)
	cases := []struct {
		name    string
		balance money.Money
		credit  bool
		amount  money.Money
		want    error
	}{
		{"insufficient", brl("100.00"), false, brl("100.01"), ErrInsufficientFunds},
		{"zero debit", brl("100.00"), false, brl("0.00"), ErrNonPositiveAmount},
		{"zero credit", brl("100.00"), true, brl("0.00"), ErrNonPositiveAmount},
		{"currency debit", brl("100.00"), false, usd("1.00"), money.ErrCurrencyMismatch},
		{"currency credit", brl("100.00"), true, usd("1.00"), money.ErrCurrencyMismatch},
		{"uninitialized amount", brl("100.00"), true, money.Money{}, money.ErrUninitialized},
		{"credit overflow", maxBRL, true, maxBRL, ErrBalanceLimitExceeded},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := wallet(t, c.balance)
			before := w.Snapshot()
			var err error
			if c.credit {
				_, err = w.Credit(c.amount, ids.NewID(), ids.NewID(), t0)
			} else {
				_, err = w.Debit(c.amount, ids.NewID(), ids.NewID(), t0)
			}
			if !errors.Is(err, c.want) {
				t.Fatalf("error = %v, want %v", err, c.want)
			}
			if w.Snapshot() != before {
				t.Error("failed movement must not change the wallet")
			}
		})
	}
}

func TestWalletDebitToExactlyZero(t *testing.T) {
	ids := &seqIDs{}
	w := wallet(t, brl("80.00"))
	if _, err := w.Debit(brl("80.00"), ids.NewID(), ids.NewID(), t0); err != nil || !w.Balance().IsZero() {
		t.Fatalf("balance %s, err %v", w.Balance(), err)
	}
}

func TestZeroValueWalletIsRejected(t *testing.T) {
	var w Wallet
	if _, err := w.Credit(brl("1.00"), walletA, walletA, t0); !errors.Is(err, ErrUninitialized) {
		t.Errorf("error = %v", err)
	}
}

func TestRehydrateWallet(t *testing.T) {
	good := WalletSnapshot{ID: walletA, PlayerID: playerA, Balance: brl("10.00"), Version: 3, CreatedAt: t0, UpdatedAt: t0}
	w, err := RehydrateWallet(good)
	if err != nil {
		t.Fatal(err)
	}
	if w.Snapshot() != good {
		t.Errorf("rehydration must preserve every field: %+v", w.Snapshot())
	}

	negative, _ := money.ParseSigned("-1.00", "BRL")
	bad := map[string]func(*WalletSnapshot){
		"no id":             func(s *WalletSnapshot) { s.ID = ID{} },
		"no player":         func(s *WalletSnapshot) { s.PlayerID = ID{} },
		"negative balance":  func(s *WalletSnapshot) { s.Balance = negative },
		"invalid balance":   func(s *WalletSnapshot) { s.Balance = money.Money{} },
		"version 0":         func(s *WalletSnapshot) { s.Version = 0 },
		"no createdAt":      func(s *WalletSnapshot) { s.CreatedAt, s.UpdatedAt = time.Time{}, time.Time{} },
		"updated < created": func(s *WalletSnapshot) { s.UpdatedAt = t0.Add(-1) },
	}
	for name, mutate := range bad {
		s := good
		mutate(&s)
		if _, err := RehydrateWallet(s); !errors.Is(err, ErrInvariant) {
			t.Errorf("%s: error = %v", name, err)
		}
	}
}
