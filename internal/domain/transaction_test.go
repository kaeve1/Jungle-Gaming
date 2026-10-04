package domain

import (
	"errors"
	"testing"
	"time"

	"jungle-gaming-backend/internal/domain/money"
)

func TestNewExternalTransactionStartsPending(t *testing.T) {
	ids := &seqIDs{}
	tx := newTx(t, ids, raw(KindBet, "tx-1", brl("25.00"), ""))
	if tx.Status() != StatusPending || tx.Origin() != OriginExternal || tx.External() == nil {
		t.Fatalf("new transaction: %+v", tx.Snapshot())
	}
	if tx.External().RequestHash != request(t, raw(KindBet, "tx-1", brl("25.00"), "")).Fingerprint() {
		t.Error("request hash must be stored")
	}
	if _, ok := tx.ResultBalance(); ok {
		t.Error("pending transaction has no result balance")
	}
}

func TestStateMachine(t *testing.T) {
	ids := &seqIDs{}
	bal := brl("10.00")
	later := t0.Add(time.Minute)

	type step func(*WagerTransaction) error
	process := func(tx *WagerTransaction) error { return tx.MarkProcessed(bal, walletA, later) }
	reject := func(tx *WagerTransaction) error { return tx.Reject(CodeInsufficientFunds, &bal, ID{}, later) }
	await := func(tx *WagerTransaction) error { return tx.AwaitReference(later, later, later) }
	retry := func(tx *WagerTransaction) error { return tx.ScheduleReferenceRetry(later, later) }
	fail := func(tx *WagerTransaction) error { return tx.Fail(CodeInternalProcessingFailed, later) }

	steps := map[string]step{"process": process, "reject": reject, "await": await, "retry": retry, "fail": fail}

	// estado inicial → transição → permitido?
	cases := []struct {
		setup []string
		next  string
		ok    bool
		want  Status
	}{
		{nil, "process", true, StatusProcessed},
		{nil, "reject", true, StatusRejected},
		{nil, "await", true, StatusPendingReference},
		{nil, "fail", true, StatusFailed},
		{nil, "retry", false, StatusPending},
		{[]string{"await"}, "retry", true, StatusPendingReference},
		{[]string{"await"}, "process", true, StatusProcessed},
		{[]string{"await"}, "reject", true, StatusRejected},
		{[]string{"await"}, "fail", true, StatusFailed},
		{[]string{"await"}, "await", false, StatusPendingReference},
	}
	// Estados terminais não aceitam nenhuma transição.
	for _, terminal := range []string{"process", "reject", "fail"} {
		for next := range steps {
			cases = append(cases, struct {
				setup []string
				next  string
				ok    bool
				want  Status
			}{[]string{terminal}, next, false, ""})
		}
	}

	for _, c := range cases {
		tx := newTx(t, ids, raw(KindRefund, "r-"+c.next, brl("10.00"), "bet-1"))
		for _, s := range c.setup {
			if err := steps[s](tx); err != nil {
				t.Fatalf("setup %v: %v", c.setup, err)
			}
		}
		before := tx.Snapshot()
		err := steps[c.next](tx)
		if c.ok {
			if err != nil || tx.Status() != c.want {
				t.Errorf("%v → %s: err %v, status %s, want %s", c.setup, c.next, err, tx.Status(), c.want)
			}
			continue
		}
		if err == nil {
			t.Errorf("%v → %s must be refused", c.setup, c.next)
		}
		if tx.Snapshot().Status != before.Status || tx.Snapshot().FailureCode != before.FailureCode {
			t.Errorf("%v → %s: refused transition changed the transaction", c.setup, c.next)
		}
	}
}

func TestTransitionGuards(t *testing.T) {
	ids := &seqIDs{}
	bal := brl("1.00")
	tx := newTx(t, ids, raw(KindBet, "tx-1", brl("1.00"), ""))
	if err := tx.Reject(CodeInternalProcessingFailed, &bal, ID{}, t0); !errors.Is(err, ErrInvariant) {
		t.Errorf("Reject with FAILED code: %v", err)
	}
	if err := tx.Fail(CodeInsufficientFunds, t0); !errors.Is(err, ErrInvariant) {
		t.Errorf("Fail with rejection code: %v", err)
	}
	if err := tx.AwaitReference(t0, t0, t0); !errors.Is(err, ErrInvariant) {
		t.Errorf("BET cannot await a reference: %v", err)
	}
	if err := tx.MarkProcessed(money.Money{}, ID{}, t0); !errors.Is(err, ErrInvariant) {
		t.Errorf("processed without balance: %v", err)
	}
	if err := tx.MarkProcessed(bal, walletA, t0); !errors.Is(err, ErrInvariant) {
		t.Errorf("BET with reference: %v", err)
	}
	if err := tx.Reject(CodeInsufficientFunds, &bal, walletA, t0); !errors.Is(err, ErrInvariant) {
		t.Errorf("BET rejected with reference: %v", err)
	}
	if tx.Status() != StatusPending {
		t.Errorf("guards must not change status, got %s", tx.Status())
	}
}

func TestRehydrateTransactionRoundTrip(t *testing.T) {
	ids := &seqIDs{}
	w := wallet(t, brl("100.00"))
	tx := processed(t, ids, w, raw(KindBet, "tx-1", brl("25.00"), ""), nil)

	again, err := RehydrateTransaction(tx.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	a, b := tx.Snapshot(), again.Snapshot()
	if *a.External != *b.External || *a.ResultBalance != *b.ResultBalance || a.Status != b.Status || a.UpdatedAt != b.UpdatedAt {
		t.Errorf("rehydration changed data:\n%+v\n%+v", a, b)
	}
	if w.Balance() != brl("75.00") {
		t.Errorf("rehydration must not move money; wallet %s", w.Balance())
	}
	// Reidratada como terminal, continua imutável.
	if err := again.MarkProcessed(brl("1.00"), ID{}, t0); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("terminal after rehydration: %v", err)
	}
}

func TestRehydrateTransactionRejectsInconsistentData(t *testing.T) {
	ids := &seqIDs{}
	w := wallet(t, brl("100.00"))
	good := processed(t, ids, w, raw(KindBet, "tx-1", brl("25.00"), ""), nil).Snapshot()

	bad := map[string]func(*TransactionSnapshot){
		"internal BET":          func(s *TransactionSnapshot) { s.Origin = OriginInternal },
		"external without data": func(s *TransactionSnapshot) { s.External = nil },
		"OPENING external":      func(s *TransactionSnapshot) { s.Kind = KindOpening },
		"processed with code":   func(s *TransactionSnapshot) { s.FailureCode = CodeInsufficientFunds },
		"rejected without code": func(s *TransactionSnapshot) { s.Status = StatusRejected },
		"rejected with FAILED code": func(s *TransactionSnapshot) {
			s.Status, s.FailureCode = StatusRejected, CodeInternalProcessingFailed
		},
		"processed without balance": func(s *TransactionSnapshot) { s.ResultBalance = nil },
		"pending ref no schedule":   func(s *TransactionSnapshot) { s.Status, s.ResultBalance = StatusPendingReference, nil },
		"BET with reference id":     func(s *TransactionSnapshot) { s.ReferenceTransactionID = walletA },
		"bad status":                func(s *TransactionSnapshot) { s.Status = "DONE" },
		"bad hash":                  func(s *TransactionSnapshot) { e := *s.External; e.RequestHash = "x"; s.External = &e },
		"negative attempts":         func(s *TransactionSnapshot) { s.ReferenceAttempts = -1 },
		"negative result balance": func(s *TransactionSnapshot) {
			neg, _ := money.ParseSigned("-1.00", "BRL")
			s.ResultBalance = &neg
		},
		"REFUND without reference": func(s *TransactionSnapshot) { s.Kind = KindRefund },
		"updated before created":   func(s *TransactionSnapshot) { s.UpdatedAt = s.CreatedAt.Add(-time.Second) },
		"hash version 0":           func(s *TransactionSnapshot) { e := *s.External; e.RequestHashVersion = 0; s.External = &e },
		"BET with external ref": func(s *TransactionSnapshot) {
			e := *s.External
			e.ReferenceExternalTransactionID = "x"
			s.External = &e
		},
		"invalid external ref": func(s *TransactionSnapshot) {
			e := *s.External
			s.Kind, e.ReferenceExternalTransactionID = KindRefund, "bad ref"
			s.External = &e
		},
	}
	for name, mutate := range bad {
		s := good
		mutate(&s)
		if _, err := RehydrateTransaction(s); !errors.Is(err, ErrInvariant) {
			t.Errorf("%s: error = %v", name, err)
		}
	}
}

func TestSnapshotIsACopy(t *testing.T) {
	ids := &seqIDs{}
	tx := newTx(t, ids, raw(KindBet, "tx-1", brl("25.00"), ""))
	s := tx.Snapshot()
	s.External.ProviderID = "provider-b"
	if tx.External().ProviderID != "provider-a" {
		t.Error("changing a snapshot must not change the transaction")
	}
}

// O worker de referências lê a pendência do banco e a retoma: a reidratação
// precisa preservar prazo, tentativas e permitir a resolução normal.
func TestPendingReferenceSurvivesRehydration(t *testing.T) {
	ids := &seqIDs{}
	w := wallet(t, brl("100.00"))
	refund := newTx(t, ids, raw(KindRefund, "ref-1", brl("30.00"), "bet-1"))
	if _, err := Process(refund, w, ReferenceLookup{}, testPolicy, ids, t0); err != nil {
		t.Fatal(err)
	}
	loaded, err := RehydrateTransaction(refund.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status() != StatusPendingReference || loaded.ReferenceExpiresAt() != refund.ReferenceExpiresAt() ||
		loaded.NextReferenceAttemptAt() != refund.NextReferenceAttemptAt() {
		t.Fatalf("pending schedule lost: %+v", loaded.Snapshot())
	}
	bet := processed(t, ids, w, raw(KindBet, "bet-1", brl("30.00"), ""), nil)
	if _, err := Process(loaded, w, ReferenceLookup{Reference: bet}, testPolicy, ids, loaded.NextReferenceAttemptAt()); err != nil {
		t.Fatal(err)
	}
	if loaded.Status() != StatusProcessed || w.Balance() != brl("100.00") {
		t.Errorf("resumed refund: %s, balance %s", loaded.Status(), w.Balance())
	}
}
