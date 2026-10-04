package domain

import (
	"errors"
	"slices"
	"testing"
	"time"

	"jungle-gaming-backend/internal/domain/money"
)

func TestProcessExternalKinds(t *testing.T) {
	cases := []struct {
		name        string
		kind        Kind
		amount      string
		wantBalance string
		wantVersion int64
		wantDir     Direction // "" = sem lançamento
		wantEvents  []string
	}{
		{"BET debits", KindBet, "25.00", "75.00", 2, Debit, []string{"WagerTransactionProcessed", "WalletBalanceChanged"}},
		{"WIN credits", KindWin, "40.00", "140.00", 2, Credit, []string{"WagerTransactionProcessed", "WalletBalanceChanged"}},
		{"LOSS moves nothing", KindLoss, "0.00", "100.00", 1, "", []string{"WagerTransactionProcessed"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ids := &seqIDs{}
			w := wallet(t, brl("100.00"))
			tx := newTx(t, ids, raw(c.kind, "tx-1", brl(c.amount), ""))
			out, err := Process(tx, w, ReferenceLookup{}, testPolicy, ids, t0)
			if err != nil {
				t.Fatal(err)
			}
			if tx.Status() != StatusProcessed {
				t.Fatalf("status %s (%s)", tx.Status(), tx.FailureCode())
			}
			if w.Balance() != brl(c.wantBalance) || w.Version() != c.wantVersion {
				t.Errorf("wallet %s v%d, want %s v%d", w.Balance(), w.Version(), c.wantBalance, c.wantVersion)
			}
			if got, _ := tx.ResultBalance(); got != brl(c.wantBalance) {
				t.Errorf("result balance %s", got)
			}
			if (out.Entry == nil) != (c.wantDir == "") || (out.Entry != nil && out.Entry.Direction() != c.wantDir) {
				t.Errorf("entry %+v, want direction %q", out.Entry, c.wantDir)
			}
			if !slices.Equal(eventTypes(out.Events), c.wantEvents) {
				t.Errorf("events %v, want %v", eventTypes(out.Events), c.wantEvents)
			}
		})
	}
}

// Enunciado §8 no nível do domínio: duas apostas de 80 sobre 100, em sequência
// (a serialização entre processos vem do lock da carteira no banco, testado na
// integração). Uma processa, a outra é rejeitada; saldo 20; um único débito.
func TestTwoBetsOf80On100(t *testing.T) {
	ids := &seqIDs{}
	w := wallet(t, brl("100.00"))
	first := newTx(t, ids, raw(KindBet, "bet-1", brl("80.00"), ""))
	second := newTx(t, ids, raw(KindBet, "bet-2", brl("80.00"), ""))

	out1, _ := Process(first, w, ReferenceLookup{}, testPolicy, ids, t0)
	out2, _ := Process(second, w, ReferenceLookup{}, testPolicy, ids, t0)

	if first.Status() != StatusProcessed || second.Status() != StatusRejected || second.FailureCode() != CodeInsufficientFunds {
		t.Fatalf("first %s, second %s/%s", first.Status(), second.Status(), second.FailureCode())
	}
	if w.Balance() != brl("20.00") || w.Version() != 2 {
		t.Errorf("wallet %s v%d", w.Balance(), w.Version())
	}
	if out1.Entry == nil || out2.Entry != nil {
		t.Error("exactly one ledger entry expected")
	}
	if bal, ok := second.ResultBalance(); !ok || bal != brl("20.00") {
		t.Errorf("rejection must record the observed balance, got %s", bal)
	}
	if !slices.Equal(eventTypes(out2.Events), []string{"WagerTransactionRejected"}) {
		t.Errorf("rejection events %v", eventTypes(out2.Events))
	}
}

func TestRejectionsAgainstWallet(t *testing.T) {
	t.Run("player mismatch hides balance (VULN-002)", func(t *testing.T) {
		ids := &seqIDs{}
		w := wallet(t, brl("100.00"))
		r := raw(KindBet, "tx-1", brl("1.00"), "")
		r.PlayerID = playerB.String()
		tx := newTx(t, ids, r)
		out, err := Process(tx, w, ReferenceLookup{}, testPolicy, ids, t0)
		if err != nil || tx.FailureCode() != CodeWalletPlayerMismatch {
			t.Fatalf("got %s, %v", tx.FailureCode(), err)
		}
		if _, ok := tx.ResultBalance(); ok {
			t.Error("WALLET_PLAYER_MISMATCH must not record the wallet balance")
		}
		if w.Balance() != brl("100.00") || out.Entry != nil {
			t.Error("rejection must not move money")
		}
	})
	t.Run("currency mismatch", func(t *testing.T) {
		ids := &seqIDs{}
		w := wallet(t, brl("100.00"))
		tx := newTx(t, ids, raw(KindLoss, "tx-1", usd("0.00"), ""))
		if _, err := Process(tx, w, ReferenceLookup{}, testPolicy, ids, t0); err != nil || tx.FailureCode() != CodeCurrencyMismatch {
			t.Fatalf("got %s, %v", tx.FailureCode(), err)
		}
	})
	t.Run("credit beyond limit", func(t *testing.T) {
		ids := &seqIDs{}
		huge, _ := money.FromMinorUnits(1<<62, money.BRL)
		w := wallet(t, huge)
		tx := newTx(t, ids, raw(KindWin, "tx-1", huge, ""))
		if _, err := Process(tx, w, ReferenceLookup{}, testPolicy, ids, t0); err != nil || tx.FailureCode() != CodeBalanceLimitExceeded {
			t.Fatalf("got %s, %v", tx.FailureCode(), err)
		}
	})
}

func TestProcessMisuse(t *testing.T) {
	ids := &seqIDs{}
	w := wallet(t, brl("100.00"))
	other, _ := RehydrateWallet(WalletSnapshot{ID: mustID("0192f291-27dd-7d3f-8071-000000000000"), PlayerID: playerA, Balance: brl("1.00"), Version: 1, CreatedAt: t0, UpdatedAt: t0})

	tx := newTx(t, ids, raw(KindBet, "tx-1", brl("1.00"), ""))
	if _, err := Process(tx, other, ReferenceLookup{}, testPolicy, ids, t0); !errors.Is(err, ErrInvariant) {
		t.Errorf("wrong wallet: %v", err)
	}
	if _, err := Process(tx, w, ReferenceLookup{}, ReferencePolicy{}, ids, t0); !errors.Is(err, ErrInvariant) {
		t.Errorf("invalid policy: %v", err)
	}
	processedTx := processed(t, ids, w, raw(KindBet, "tx-2", brl("1.00"), ""), nil)
	if _, err := Process(processedTx, w, ReferenceLookup{}, testPolicy, ids, t0); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("reprocessing a terminal transaction: %v", err)
	}
	if w.Balance() != brl("99.00") {
		t.Errorf("misuse must not move money; balance %s", w.Balance())
	}
}

func TestReversals(t *testing.T) {
	t.Run("REFUND credits the BET back", func(t *testing.T) {
		ids := &seqIDs{}
		w := wallet(t, brl("100.00"))
		bet := processed(t, ids, w, raw(KindBet, "bet-1", brl("30.00"), ""), nil)
		refund := processed(t, ids, w, raw(KindRefund, "ref-1", brl("30.00"), "bet-1"), bet)
		if w.Balance() != brl("100.00") || refund.ReferenceTransactionID() != bet.ID() {
			t.Errorf("balance %s, reference %s", w.Balance(), refund.ReferenceTransactionID())
		}
	})
	t.Run("ROLLBACK of BET credits, of WIN and REFUND debits", func(t *testing.T) {
		ids := &seqIDs{}
		w := wallet(t, brl("100.00"))
		bet := processed(t, ids, w, raw(KindBet, "bet-1", brl("30.00"), ""), nil)      // 70
		win := processed(t, ids, w, raw(KindWin, "win-1", brl("50.00"), "bet-1"), bet) // 120
		processed(t, ids, w, raw(KindRollback, "rb-win", brl("50.00"), "win-1"), win)  // 70
		processed(t, ids, w, raw(KindRollback, "rb-bet", brl("30.00"), "bet-1"), bet)  // 100
		if w.Balance() != brl("100.00") || w.Version() != 5 {
			t.Errorf("wallet %s v%d", w.Balance(), w.Version())
		}
		bet2 := processed(t, ids, w, raw(KindBet, "bet-2", brl("10.00"), ""), nil)          // 90
		ref2 := processed(t, ids, w, raw(KindRefund, "ref-2", brl("10.00"), "bet-2"), bet2) // 100
		processed(t, ids, w, raw(KindRollback, "rb-ref", brl("10.00"), "ref-2"), ref2)      // 90
		if w.Balance() != brl("90.00") {
			t.Errorf("after rollback of refund: %s", w.Balance())
		}
	})
	t.Run("one successful reversal per transaction", func(t *testing.T) {
		ids := &seqIDs{}
		w := wallet(t, brl("100.00"))
		bet := processed(t, ids, w, raw(KindBet, "bet-1", brl("30.00"), ""), nil)
		processed(t, ids, w, raw(KindRefund, "ref-1", brl("30.00"), "bet-1"), bet)
		// A aplicação informa que a BET já tem reversão processada.
		for _, kind := range []Kind{KindRefund, KindRollback} {
			tx := newTx(t, ids, raw(kind, "again-"+string(kind), brl("30.00"), "bet-1"))
			if _, err := Process(tx, w, ReferenceLookup{Reference: bet, AlreadyReversed: true}, testPolicy, ids, t0); err != nil {
				t.Fatal(err)
			}
			if tx.FailureCode() != CodeReferenceAlreadyReversed {
				t.Errorf("%s after REFUND: %s", kind, tx.FailureCode())
			}
		}
		if w.Balance() != brl("100.00") {
			t.Errorf("the same debit must never be returned twice; balance %s", w.Balance())
		}
	})
	t.Run("ROLLBACK without funds has its own code", func(t *testing.T) {
		ids := &seqIDs{}
		w := wallet(t, brl("0.00"))
		win := processed(t, ids, w, raw(KindWin, "win-1", brl("50.00"), ""), nil)
		processed(t, ids, w, raw(KindBet, "bet-1", brl("40.00"), ""), nil) // saldo 10
		tx := newTx(t, ids, raw(KindRollback, "rb-1", brl("50.00"), "win-1"))
		if _, err := Process(tx, w, ReferenceLookup{Reference: win}, testPolicy, ids, t0); err != nil {
			t.Fatal(err)
		}
		if tx.FailureCode() != CodeReversalInsufficientFunds || tx.FailureCode() == CodeInsufficientFunds {
			t.Errorf("code %s", tx.FailureCode())
		}
		if w.Balance() != brl("10.00") {
			t.Errorf("balance %s", w.Balance())
		}
	})
}

func TestReferenceValidation(t *testing.T) {
	ids := &seqIDs{}
	w := wallet(t, brl("1000.00"))
	bet := processed(t, ids, w, raw(KindBet, "bet-1", brl("30.00"), ""), nil)
	win := processed(t, ids, w, raw(KindWin, "win-1", brl("5.00"), ""), nil)
	loss := processed(t, ids, w, raw(KindLoss, "loss-1", brl("0.00"), ""), nil)
	rejected := newTx(t, ids, raw(KindBet, "bet-big", brl("999999.00"), ""))
	Process(rejected, w, ReferenceLookup{}, testPolicy, ids, t0)

	otherRound := raw(KindBet, "bet-r2", brl("30.00"), "")
	otherRound.RoundID = "round-2"
	betRound2 := processed(t, ids, w, otherRound, nil)

	cases := []struct {
		name string
		req  RawExternalRequest
		ref  *WagerTransaction
		want FailureCode
	}{
		{"REFUND of WIN", raw(KindRefund, "x1", brl("5.00"), "win-1"), win, CodeReferenceKindNotAllowed},
		{"ROLLBACK of LOSS", raw(KindRollback, "x2", brl("0.01"), "loss-1"), loss, CodeReferenceKindNotAllowed},
		{"WIN referencing WIN", raw(KindWin, "x3", brl("1.00"), "win-1"), win, CodeReferenceKindNotAllowed},
		{"partial REFUND", raw(KindRefund, "x4", brl("10.00"), "bet-1"), bet, CodeReferenceAmountMismatch},
		{"REFUND of rejected BET", raw(KindRefund, "x5", brl("999999.00"), "bet-big"), rejected, CodeReferenceNotProcessed},
		{"REFUND in another round", raw(KindRefund, "x6", brl("30.00"), "bet-r2"), betRound2, CodeReferenceMismatch},
	}
	for _, c := range cases {
		tx := newTx(t, ids, c.req)
		if _, err := Process(tx, w, ReferenceLookup{Reference: c.ref}, testPolicy, ids, t0); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if tx.Status() != StatusRejected || tx.FailureCode() != c.want {
			t.Errorf("%s: %s/%s, want %s", c.name, tx.Status(), tx.FailureCode(), c.want)
		}
	}

	// Referência de outro jogador.
	otherPlayer, _ := RehydrateTransaction(func() TransactionSnapshot {
		s := bet.Snapshot()
		s.ID, s.PlayerID = ids.NewID(), playerB
		return s
	}())
	tx := newTx(t, ids, raw(KindRefund, "x7", brl("30.00"), "bet-1"))
	Process(tx, w, ReferenceLookup{Reference: otherPlayer}, testPolicy, ids, t0)
	if tx.FailureCode() != CodeReferenceMismatch {
		t.Errorf("reference of another player: %s", tx.FailureCode())
	}
	if w.Balance() != brl("945.00") { // 1000 - 30 (bet) + 5 (win) - 30 (bet round 2)
		t.Errorf("rejections must not move money; balance %s", w.Balance())
	}
}

func TestPendingReference(t *testing.T) {
	t.Run("waits, retries with backoff and resolves when the BET arrives", func(t *testing.T) {
		ids := &seqIDs{}
		w := wallet(t, brl("100.00"))
		refund := newTx(t, ids, raw(KindRefund, "ref-1", brl("30.00"), "bet-1"))

		out, err := Process(refund, w, ReferenceLookup{}, testPolicy, ids, t0)
		if err != nil || refund.Status() != StatusPendingReference {
			t.Fatalf("status %s, %v", refund.Status(), err)
		}
		if !slices.Equal(eventTypes(out.Events), []string{"WagerTransactionPendingReference"}) || out.Entry != nil {
			t.Errorf("pending outcome %v", eventTypes(out.Events))
		}
		if refund.ReferenceExpiresAt() != t0.Add(time.Minute) || refund.NextReferenceAttemptAt() != t0.Add(time.Second) {
			t.Errorf("schedule %s / %s", refund.NextReferenceAttemptAt(), refund.ReferenceExpiresAt())
		}

		// Tentativas sem a referência: 2s, 4s, 8s, 8s (teto)...
		now := t0
		var delays []time.Duration
		for range 4 {
			now = refund.NextReferenceAttemptAt()
			out, err := Process(refund, w, ReferenceLookup{}, testPolicy, ids, now)
			if err != nil || refund.Status() != StatusPendingReference || len(out.Events) != 0 {
				t.Fatalf("retry: %s %v %v", refund.Status(), out.Events, err)
			}
			delays = append(delays, refund.NextReferenceAttemptAt().Sub(now))
		}
		if !slices.Equal(delays, []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 8 * time.Second}) {
			t.Errorf("backoff %v", delays)
		}
		if refund.ReferenceAttempts() != 4 {
			t.Errorf("attempts %d", refund.ReferenceAttempts())
		}

		bet := processed(t, ids, w, raw(KindBet, "bet-1", brl("30.00"), ""), nil) // 70
		out, err = Process(refund, w, ReferenceLookup{Reference: bet}, testPolicy, ids, refund.NextReferenceAttemptAt())
		if err != nil || refund.Status() != StatusProcessed || w.Balance() != brl("100.00") {
			t.Fatalf("resolution: %s %s %v", refund.Status(), w.Balance(), err)
		}
		if !slices.Equal(eventTypes(out.Events), []string{"WagerTransactionProcessed", "WalletBalanceChanged"}) {
			t.Errorf("resolution events %v", eventTypes(out.Events))
		}
	})
	t.Run("referenced transaction still pending keeps waiting", func(t *testing.T) {
		ids := &seqIDs{}
		w := wallet(t, brl("100.00"))
		pendingRefund := newTx(t, ids, raw(KindRefund, "ref-1", brl("30.00"), "bet-1"))
		Process(pendingRefund, w, ReferenceLookup{}, testPolicy, ids, t0)
		rollback := newTx(t, ids, raw(KindRollback, "rb-1", brl("30.00"), "ref-1"))
		Process(rollback, w, ReferenceLookup{Reference: pendingRefund}, testPolicy, ids, t0)
		if rollback.Status() != StatusPendingReference {
			t.Errorf("status %s", rollback.Status())
		}
	})
	t.Run("expires only on an actual attempt after the TTL", func(t *testing.T) {
		ids := &seqIDs{}
		w := wallet(t, brl("100.00"))
		refund := newTx(t, ids, raw(KindRefund, "ref-1", brl("30.00"), "bet-1"))
		Process(refund, w, ReferenceLookup{}, testPolicy, ids, t0)

		// Nunca agenda além do prazo: a última tentativa cai exatamente no TTL.
		for refund.Status() == StatusPendingReference && refund.NextReferenceAttemptAt().Before(refund.ReferenceExpiresAt()) {
			Process(refund, w, ReferenceLookup{}, testPolicy, ids, refund.NextReferenceAttemptAt())
		}
		if refund.NextReferenceAttemptAt() != refund.ReferenceExpiresAt() {
			t.Fatalf("last attempt %s, expiry %s", refund.NextReferenceAttemptAt(), refund.ReferenceExpiresAt())
		}
		out, _ := Process(refund, w, ReferenceLookup{}, testPolicy, ids, refund.ReferenceExpiresAt())
		if refund.Status() != StatusRejected || refund.FailureCode() != CodeReferenceNotFound {
			t.Fatalf("status %s/%s", refund.Status(), refund.FailureCode())
		}
		if !slices.Equal(eventTypes(out.Events), []string{"WagerTransactionRejected"}) {
			t.Errorf("expiry events %v", eventTypes(out.Events))
		}
	})
	t.Run("reference found after the TTL is still applied (R72)", func(t *testing.T) {
		// O banco ficou fora do ar além do prazo e a BET chegou nesse meio-tempo:
		// a primeira tentativa real encontra a referência e processa.
		ids := &seqIDs{}
		w := wallet(t, brl("100.00"))
		refund := newTx(t, ids, raw(KindRefund, "ref-1", brl("30.00"), "bet-1"))
		Process(refund, w, ReferenceLookup{}, testPolicy, ids, t0)
		bet := processed(t, ids, w, raw(KindBet, "bet-1", brl("30.00"), ""), nil)
		Process(refund, w, ReferenceLookup{Reference: bet}, testPolicy, ids, t0.Add(time.Hour))
		if refund.Status() != StatusProcessed || w.Balance() != brl("100.00") {
			t.Errorf("status %s, balance %s", refund.Status(), w.Balance())
		}
	})
}

func TestReferencePolicyDelay(t *testing.T) {
	p := ReferencePolicy{TTL: time.Hour, BaseDelay: time.Second, MaxDelay: time.Minute}
	want := []time.Duration{1, 2, 4, 8, 16, 32, 60, 60}
	for i, w := range want {
		if got := p.Delay(i); got != w*time.Second {
			t.Errorf("Delay(%d) = %s, want %ds", i, got, w)
		}
	}
	if got := p.Delay(1 << 30); got != time.Minute {
		t.Errorf("huge attempt must not overflow: %s", got)
	}
	if DefaultReferencePolicy.Validate() != nil {
		t.Error("default policy must be valid")
	}
}

// A rejeição guarda qual referência foi encontrada, para auditoria
// (enunciado §6.3: "persista também a referência interna resolvida").
func TestRejectionRecordsResolvedReference(t *testing.T) {
	ids := &seqIDs{}
	w := wallet(t, brl("100.00"))
	bet := processed(t, ids, w, raw(KindBet, "bet-1", brl("30.00"), ""), nil)
	tx := newTx(t, ids, raw(KindRefund, "ref-2", brl("30.00"), "bet-1"))
	if _, err := Process(tx, w, ReferenceLookup{Reference: bet, AlreadyReversed: true}, testPolicy, ids, t0); err != nil {
		t.Fatal(err)
	}
	if tx.FailureCode() != CodeReferenceAlreadyReversed || tx.ReferenceTransactionID() != bet.ID() {
		t.Errorf("code %s, reference %s", tx.FailureCode(), tx.ReferenceTransactionID())
	}
	if _, err := RehydrateTransaction(tx.Snapshot()); err != nil {
		t.Errorf("rejected reversal with reference must be persistable: %v", err)
	}
}

// Se a aplicação entregar uma referência que não corresponde à busca, é um
// bug: o domínio devolve erro e NÃO grava uma rejeição definitiva.
func TestWrongReferenceFromApplicationIsAnError(t *testing.T) {
	ids := &seqIDs{}
	w := wallet(t, brl("100.00"))
	bet := processed(t, ids, w, raw(KindBet, "bet-1", brl("30.00"), ""), nil)
	otherBet := processed(t, ids, w, raw(KindBet, "bet-2", brl("30.00"), ""), nil)

	refund := newTx(t, ids, raw(KindRefund, "ref-1", brl("30.00"), "bet-1"))
	if _, err := Process(refund, w, ReferenceLookup{Reference: otherBet}, testPolicy, ids, t0); !errors.Is(err, ErrInvariant) {
		t.Errorf("wrong reference: %v", err)
	}
	plainBet := newTx(t, ids, raw(KindBet, "bet-3", brl("1.00"), ""))
	if _, err := Process(plainBet, w, ReferenceLookup{Reference: bet}, testPolicy, ids, t0); !errors.Is(err, ErrInvariant) {
		t.Errorf("reference given to BET: %v", err)
	}
	if refund.Status() != StatusPending || plainBet.Status() != StatusPending || w.Balance() != brl("40.00") {
		t.Errorf("application bugs must not change state: %s %s %s", refund.Status(), plainBet.Status(), w.Balance())
	}
}

func TestProcessRequiresTime(t *testing.T) {
	ids := &seqIDs{}
	w := wallet(t, brl("100.00"))
	tx := newTx(t, ids, raw(KindBet, "tx-1", brl("1.00"), ""))
	if _, err := Process(tx, w, ReferenceLookup{}, testPolicy, ids, time.Time{}); !errors.Is(err, ErrInvariant) {
		t.Errorf("zero time: %v", err)
	}
}

func TestReferencePolicyLimits(t *testing.T) {
	bad := []ReferencePolicy{
		{},
		{TTL: time.Minute, BaseDelay: 2 * time.Second, MaxDelay: time.Second},
		{TTL: 25 * time.Hour, BaseDelay: time.Second, MaxDelay: time.Minute},
		{TTL: time.Hour, BaseDelay: time.Second, MaxDelay: 2 * time.Hour},
	}
	for _, p := range bad {
		if err := p.Validate(); !errors.Is(err, ErrInvariant) {
			t.Errorf("%+v: %v", p, err)
		}
	}
}
