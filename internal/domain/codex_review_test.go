package domain

import (
	"encoding/json"
	"jungle-gaming-backend/internal/domain/money"
	"testing"
)

func TestCodexReviewEnvelopeIsSnapshot(t *testing.T) {
	ids := &seqIDs{}
	w := wallet(t, brl("100.00"))
	tx := processed(t, ids, w, raw(KindBet, "snapshot-review", brl("25.00"), ""), nil)
	e := newWagerTransactionProcessed(tx)
	envelope, err := NewEnvelope(ids.NewID(), e, "review", "")
	if err != nil {
		t.Fatal(err)
	}
	before, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	*e.ProviderID = "provider-b"
	after, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("BUG: evento externo ao envelope altera o snapshot depois da construção")
	}
	copy := envelope.Event().(WagerTransactionProcessed)
	*copy.ProviderID = "provider-c"
	after, _ = json.Marshal(envelope)
	if string(before) != string(after) {
		t.Fatal("Event() exposed mutable snapshot")
	}
}

// Testes de revisão: exigem a rejeição de estados inválidos pelas APIs públicas.
func TestCodexReviewPublicInvariants(t *testing.T) {
	t.Run("rehydrate BET zero", func(t *testing.T) {
		ids := &seqIDs{}
		tx := newTx(t, ids, raw(KindBet, "review-bet", brl("1.00"), ""))
		s := tx.Snapshot()
		s.Money = brl("0.00")
		if _, err := RehydrateTransaction(s); err == nil {
			t.Fatal("BUG: RehydrateTransaction aceitou BET de zero")
		}
	})
	t.Run("reject negative result", func(t *testing.T) {
		ids := &seqIDs{}
		tx := newTx(t, ids, raw(KindBet, "review-reject", brl("1.00"), ""))
		negative, _ := money.ParseSigned("-1.00", "BRL")
		if err := tx.Reject(CodeInsufficientFunds, &negative, ID{}, t0); err == nil {
			t.Fatal("BUG: Reject aceitou saldo de resultado negativo")
		}
		if tx.Status() != StatusPending {
			t.Fatal("failed validation mutated status")
		}
	})
	t.Run("process refund without resolved reference", func(t *testing.T) {
		ids := &seqIDs{}
		tx := newTx(t, ids, raw(KindRefund, "review-refund", brl("1.00"), "bet-original"))
		if err := tx.MarkProcessed(brl("10.00"), ID{}, t0); err == nil {
			t.Fatal("BUG: MarkProcessed aceitou REFUND sem referência resolvida")
		}
		if tx.Status() != StatusPending {
			t.Fatal("failed validation mutated status")
		}
	})
}
