package domain

import (
	"errors"
	"strings"
	"testing"

	"jungle-gaming-backend/internal/domain/money"
)

func TestRequestZeroPolicyPerKind(t *testing.T) {
	cases := []struct {
		kind   Kind
		amount string
		ref    string
		ok     bool
	}{
		{KindBet, "25.00", "", true},
		{KindBet, "0.00", "", false},
		{KindWin, "10.00", "", true},
		{KindWin, "10.00", "bet-1", true},
		{KindWin, "0.00", "", false},
		{KindLoss, "0.00", "", true},
		{KindLoss, "0.01", "", false},
		{KindRefund, "25.00", "bet-1", true},
		{KindRefund, "0.00", "bet-1", false},
		{KindRollback, "25.00", "bet-1", true},
		{KindRollback, "0.00", "bet-1", false},
	}
	for _, c := range cases {
		_, err := NewExternalRequest(raw(c.kind, "tx-1", brl(c.amount), c.ref))
		if c.ok && err != nil {
			t.Errorf("%s %s: unexpected %v", c.kind, c.amount, err)
		}
		if !c.ok {
			var ve *ValidationError
			if !errors.As(err, &ve) || ve.Code != CodeInvalidAmount {
				t.Errorf("%s %s: error = %v, want INVALID_AMOUNT", c.kind, c.amount, err)
			}
		}
	}
}

func TestRequestValidation(t *testing.T) {
	negative, _ := money.ParseSigned("-1.00", "BRL")
	cases := []struct {
		name  string
		mod   func(*RawExternalRequest)
		code  FailureCode
		field string
	}{
		{"OPENING is internal only", func(r *RawExternalRequest) { r.Kind = "OPENING" }, CodeInvalidKind, "kind"},
		{"unknown kind", func(r *RawExternalRequest) { r.Kind = "JACKPOT" }, CodeInvalidKind, "kind"},
		{"lowercase kind", func(r *RawExternalRequest) { r.Kind = "bet" }, CodeInvalidKind, "kind"},
		{"missing key", func(r *RawExternalRequest) { r.IdempotencyKey = "" }, CodeMissingIdempotencyKey, "idempotencyKey"},
		{"key with space", func(r *RawExternalRequest) { r.IdempotencyKey = "a b" }, CodeValidationError, "idempotencyKey"},
		{"key too long", func(r *RawExternalRequest) { r.IdempotencyKey = strings.Repeat("k", 256) }, CodeValidationError, "idempotencyKey"},
		{"empty provider", func(r *RawExternalRequest) { r.ProviderID = "" }, CodeValidationError, "providerId"},
		{"provider with slash", func(r *RawExternalRequest) { r.ProviderID = "a/b" }, CodeValidationError, "providerId"},
		{"external id too long", func(r *RawExternalRequest) { r.ExternalTransactionID = strings.Repeat("x", 129) }, CodeValidationError, "externalTransactionId"},
		{"non-ASCII round", func(r *RawExternalRequest) { r.RoundID = "rodada-é" }, CodeValidationError, "roundId"},
		{"empty game", func(r *RawExternalRequest) { r.GameID = "" }, CodeValidationError, "gameId"},
		{"bad player", func(r *RawExternalRequest) { r.PlayerID = "player-1" }, CodeValidationError, "playerId"},
		{"bad wallet", func(r *RawExternalRequest) { r.WalletID = "" }, CodeValidationError, "walletId"},
		{"uninitialized money", func(r *RawExternalRequest) { r.Money = money.Money{} }, CodeInvalidAmount, "money"},
		{"negative money", func(r *RawExternalRequest) { r.Money = negative }, CodeInvalidAmount, "money"},
		{"BET with reference", func(r *RawExternalRequest) { r.ReferenceExternalTransactionID = "x" }, CodeValidationError, "referenceExternalTransactionId"},
	}
	for _, c := range cases {
		r := raw(KindBet, "tx-1", brl("25.00"), "")
		c.mod(&r)
		_, err := NewExternalRequest(r)
		var ve *ValidationError
		if !errors.As(err, &ve) || ve.Code != c.code || ve.Field != c.field || !errors.Is(err, ErrValidation) {
			t.Errorf("%s: error = %v, want %s on %s", c.name, err, c.code, c.field)
		}
	}

	refCases := map[string]RawExternalRequest{
		"REFUND without reference":   raw(KindRefund, "tx-1", brl("1.00"), ""),
		"ROLLBACK without reference": raw(KindRollback, "tx-1", brl("1.00"), ""),
		"LOSS with reference":        raw(KindLoss, "tx-1", brl("0.00"), "bet-1"),
		"self reference":             raw(KindRefund, "tx-1", brl("1.00"), "tx-1"),
		"invalid reference":          raw(KindRefund, "tx-1", brl("1.00"), "bet 1"),
	}
	for name, r := range refCases {
		if _, err := NewExternalRequest(r); !errors.Is(err, ErrValidation) {
			t.Errorf("%s: error = %v", name, err)
		}
	}
}

func TestFingerprint(t *testing.T) {
	base := raw(KindBet, "transaction-123", brl("25.00"), "")
	req := request(t, base)

	wantJSON := `{"externalTransactionId":"transaction-123","gameId":"fortune-chimp","kind":"BET",` +
		`"money":{"amount":"25.00","currency":"BRL"},"playerId":"0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",` +
		`"providerId":"provider-a","roundId":"round-987","walletId":"0192f291-27dd-7d3f-8071-5f8685deef37"}`
	if got := string(req.CanonicalJSON()); got != wantJSON {
		t.Fatalf("canonical JSON:\n got %s\nwant %s", got, wantJSON)
	}
	// Valor fixo: se mudar, o algoritmo mudou e FingerprintVersion precisa mudar junto.
	// Conferido de forma independente: printf JSON_CANONICO | sha256sum (docs/TESTES.md).
	const golden = "629836932b79106b99523d06a1e7fa80689b0ea1e1c47aa3f0a5a2c87d0c4344"
	if got := req.Fingerprint(); got != golden {
		t.Errorf("fingerprint = %s, want %s", got, golden)
	}

	// A chave de idempotência não entra no hash.
	otherKey := base
	otherKey.IdempotencyKey = "another-key"
	if request(t, otherKey).Fingerprint() != req.Fingerprint() {
		t.Error("idempotency key must not affect the fingerprint")
	}
	// UUID em maiúsculas é normalizado: mesmo hash.
	upper := base
	upper.WalletID = strings.ToUpper(base.WalletID)
	if request(t, upper).Fingerprint() != req.Fingerprint() {
		t.Error("UUID case must not affect the fingerprint")
	}
	// Qualquer campo de negócio diferente muda o hash.
	changes := map[string]func(*RawExternalRequest){
		"amount":   func(r *RawExternalRequest) { r.Money = brl("25.01") },
		"currency": func(r *RawExternalRequest) { r.Money = usd("25.00") },
		"round":    func(r *RawExternalRequest) { r.RoundID = "round-988" },
		"game":     func(r *RawExternalRequest) { r.GameID = "other" },
		"player":   func(r *RawExternalRequest) { r.PlayerID = playerB.String() },
		"provider": func(r *RawExternalRequest) { r.ProviderID = "provider-b" },
		"extId":    func(r *RawExternalRequest) { r.ExternalTransactionID = "transaction-124" },
		"kind":     func(r *RawExternalRequest) { r.Kind = string(KindWin) },
	}
	for name, mod := range changes {
		r := base
		mod(&r)
		if request(t, r).Fingerprint() == req.Fingerprint() {
			t.Errorf("changing %s must change the fingerprint", name)
		}
	}
	// Referência presente entra no hash.
	win := request(t, raw(KindWin, "w-1", brl("5.00"), ""))
	winRef := request(t, raw(KindWin, "w-1", brl("5.00"), "bet-1"))
	if win.Fingerprint() == winRef.Fingerprint() {
		t.Error("reference must affect the fingerprint")
	}
}

func TestClassifyDuplicate(t *testing.T) {
	ids := &seqIDs{}
	existing := newTx(t, ids, raw(KindBet, "tx-1", brl("25.00"), ""))

	same := request(t, raw(KindBet, "tx-1", brl("25.00"), ""))
	if k, err := ClassifyDuplicate(existing, same); err != nil || k != Replay {
		t.Errorf("same key and payload: %v %v, want Replay", k, err)
	}

	changed := request(t, raw(KindBet, "tx-1", brl("30.00"), ""))
	if k, _ := ClassifyDuplicate(existing, changed); k != KeyReused {
		t.Errorf("same key, different payload: %v, want KeyReused", k)
	}

	r := raw(KindBet, "tx-1", brl("25.00"), "")
	r.IdempotencyKey = "different-key"
	if k, _ := ClassifyDuplicate(existing, request(t, r)); k != ExternalIDConflict {
		t.Errorf("same operation, different key: %v, want ExternalIDConflict", k)
	}

	other := raw(KindBet, "tx-1", brl("25.00"), "")
	other.ProviderID = "provider-b"
	if _, err := ClassifyDuplicate(existing, request(t, other)); !errors.Is(err, ErrInvariant) {
		t.Errorf("other provider must never be classified as duplicate: %v", err)
	}
}

// Um hash calculado por outra versão do algoritmo não pode ser comparado
// diretamente: daria falso conflito (ou falso replay).
func TestClassifyDuplicateRequiresSameHashVersion(t *testing.T) {
	ids := &seqIDs{}
	s := newTx(t, ids, raw(KindBet, "tx-1", brl("25.00"), "")).Snapshot()
	s.External.RequestHashVersion = FingerprintVersion + 1
	future, err := RehydrateTransaction(s)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ClassifyDuplicate(future, request(t, raw(KindBet, "tx-1", brl("25.00"), ""))); !errors.Is(err, ErrInvariant) {
		t.Errorf("error = %v", err)
	}
}
