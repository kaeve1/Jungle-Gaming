package domain

import (
	"encoding/binary"
	"testing"
	"time"

	"jungle-gaming-backend/internal/domain/money"
)

// seqIDs gera IDs determinísticos (1, 2, 3...) para testes reproduzíveis.
type seqIDs struct{ n uint64 }

func (s *seqIDs) NewID() ID {
	s.n++
	var id ID
	id[0] = 0xAA // nunca zero
	binary.BigEndian.PutUint64(id[8:], s.n)
	return id
}

var t0 = time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)

var (
	playerA = mustID("0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1")
	playerB = mustID("0192f28f-5dc0-7d58-bdb2-814ad6a0f4b2")
	walletA = mustID("0192f291-27dd-7d3f-8071-5f8685deef37")
)

func mustID(s string) ID {
	id, err := ParseID(s)
	if err != nil {
		panic(err)
	}
	return id
}

func brl(amount string) money.Money {
	m, err := money.Parse(amount, "BRL")
	if err != nil {
		panic(err)
	}
	return m
}

func usd(amount string) money.Money {
	m, err := money.Parse(amount, "USD")
	if err != nil {
		panic(err)
	}
	return m
}

// wallet cria uma carteira reidratada de walletA/playerA com o saldo dado.
func wallet(t *testing.T, balance money.Money) *Wallet {
	t.Helper()
	w, err := RehydrateWallet(WalletSnapshot{ID: walletA, PlayerID: playerA, Balance: balance, Version: 1, CreatedAt: t0, UpdatedAt: t0})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

// raw devolve um pedido externo válido de provider-a sobre walletA/playerA.
func raw(kind Kind, extID string, amount money.Money, ref string) RawExternalRequest {
	return RawExternalRequest{
		ProviderID: "provider-a", ExternalTransactionID: extID, IdempotencyKey: "provider-a:" + extID,
		PlayerID: playerA.String(), WalletID: walletA.String(), RoundID: "round-987", GameID: "fortune-chimp",
		Kind: string(kind), Money: amount, ReferenceExternalTransactionID: ref,
	}
}

func request(t *testing.T, r RawExternalRequest) ExternalRequest {
	t.Helper()
	req, err := NewExternalRequest(r)
	if err != nil {
		t.Fatalf("NewExternalRequest: %v", err)
	}
	return req
}

func newTx(t *testing.T, ids IDGenerator, r RawExternalRequest) *WagerTransaction {
	t.Helper()
	tx, err := NewExternalTransaction(ids.NewID(), request(t, r), t0)
	if err != nil {
		t.Fatal(err)
	}
	return tx
}

var testPolicy = ReferencePolicy{TTL: time.Minute, BaseDelay: time.Second, MaxDelay: 8 * time.Second}

// processed cria e processa uma operação, falhando o teste se não terminar PROCESSED.
func processed(t *testing.T, ids IDGenerator, w *Wallet, r RawExternalRequest, ref *WagerTransaction) *WagerTransaction {
	t.Helper()
	tx := newTx(t, ids, r)
	if _, err := Process(tx, w, ReferenceLookup{Reference: ref}, testPolicy, ids, t0); err != nil {
		t.Fatal(err)
	}
	if tx.Status() != StatusProcessed {
		t.Fatalf("%s %s: status %s (%s), want PROCESSED", r.Kind, r.ExternalTransactionID, tx.Status(), tx.FailureCode())
	}
	return tx
}

func eventTypes(events []Event) []string {
	var out []string
	for _, e := range events {
		out = append(out, e.EventType())
	}
	return out
}
