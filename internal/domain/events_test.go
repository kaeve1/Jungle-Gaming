package domain

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestEnvelopeJSON(t *testing.T) {
	ids := &seqIDs{}
	w := wallet(t, brl("1000.00"))
	tx := newTx(t, ids, raw(KindBet, "transaction-123", brl("25.00"), ""))
	at := t0.Add(123 * time.Millisecond)
	out, err := Process(tx, w, ReferenceLookup{}, testPolicy, ids, at)
	if err != nil {
		t.Fatal(err)
	}
	changed := out.Events[1]
	env, err := NewEnvelope(mustID("0192f298-3460-7000-8000-000000000001"), changed, "msg-123", "msg-123")
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"eventId":"0192f298-3460-7000-8000-000000000001","eventType":"WalletBalanceChanged",` +
		`"aggregateId":"0192f291-27dd-7d3f-8071-5f8685deef37","correlationId":"msg-123","causationId":"msg-123",` +
		`"occurredAt":"2026-09-08T12:00:00.123Z","version":1,"data":{"walletId":"0192f291-27dd-7d3f-8071-5f8685deef37",` +
		`"transactionId":"` + tx.ID().String() + `","direction":"DEBIT","money":{"amount":"25.00","currency":"BRL"},` +
		`"balanceBefore":{"amount":"1000.00","currency":"BRL"},"balanceAfter":{"amount":"975.00","currency":"BRL"},"walletVersion":2}}`
	if string(b) != want {
		t.Errorf("envelope:\n got %s\nwant %s", b, want)
	}
}

func TestEventsCarryFixedTypeVersionAndExactMoney(t *testing.T) {
	ids := &seqIDs{}
	w := wallet(t, brl("100.00"))
	tx := newTx(t, ids, raw(KindBet, "tx-1", brl("25.00"), ""))
	out, _ := Process(tx, w, ReferenceLookup{}, testPolicy, ids, t0)
	processedEvt := out.Events[0]
	if processedEvt.EventType() != "WagerTransactionProcessed" || processedEvt.EventVersion() != 1 ||
		processedEvt.AggregateID() != tx.ID() || processedEvt.PartitionKey() != walletA {
		t.Errorf("processed event metadata: %s v%d", processedEvt.EventType(), processedEvt.EventVersion())
	}
	b, _ := json.Marshal(processedEvt)
	for _, want := range []string{`"balanceAfter":{"amount":"75.00","currency":"BRL"}`, `"processedAt":"2026-09-08T12:00:00.000Z"`, `"providerId":"provider-a"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("payload %s missing %s", b, want)
		}
	}

	// Snapshot imutável: mudanças posteriores na transação não alteram o evento.
	before := string(b)
	processed(t, ids, w, raw(KindBet, "tx-2", brl("1.00"), ""), nil)
	if after, _ := json.Marshal(processedEvt); string(after) != before {
		t.Error("event payload must be an immutable snapshot")
	}
}

func TestEnvelopeValidation(t *testing.T) {
	ids := &seqIDs{}
	w := wallet(t, brl("100.00"))
	tx := newTx(t, ids, raw(KindLoss, "tx-1", brl("0.00"), ""))
	out, _ := Process(tx, w, ReferenceLookup{}, testPolicy, ids, t0)
	if _, err := NewEnvelope(ID{}, out.Events[0], "c", ""); !errors.Is(err, ErrUninitialized) {
		t.Errorf("zero event id: %v", err)
	}
	if _, err := NewEnvelope(walletA, nil, "c", ""); !errors.Is(err, ErrUninitialized) {
		t.Errorf("nil event: %v", err)
	}
	if _, err := NewEnvelope(walletA, out.Events[0], "", ""); !errors.Is(err, ErrInvariant) {
		t.Errorf("missing correlation: %v", err)
	}
	env, _ := NewEnvelope(walletA, out.Events[0], "c", "")
	if b, _ := json.Marshal(env); !strings.Contains(string(b), `"causationId":null`) {
		t.Errorf("optional causation must be null: %s", b)
	}
}
