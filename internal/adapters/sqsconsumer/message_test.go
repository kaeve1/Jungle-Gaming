package sqsconsumer

import (
	"strings"
	"testing"
)

const wallet = "0192f291-27dd-7d3f-8071-5f8685deef37"
const validBody = `{"messageId":"msg-1","type":"WagerTransactionRequested","occurredAt":"2026-09-08T12:00:00Z","data":{"providerId":"provider-a","externalTransactionId":"bet-1","idempotencyKey":"key-1","playerId":"0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1","walletId":"0192f291-27dd-7d3f-8071-5f8685deef37","roundId":"r","gameId":"g","kind":"BET","money":{"amount":"1.00","currency":"BRL"}}}`

func TestEnvelopeStrictnessAndCanonicalHashes(t *testing.T) {
	m, err := Decode(validBody, wallet, []string{"provider-a"})
	if err != nil {
		t.Fatal(err)
	}
	equivalent := strings.Replace(validBody, "12:00:00Z", "09:00:00-03:00", 1)
	n, err := Decode(equivalent, wallet, []string{"provider-a"})
	if err != nil || n.Hash != m.Hash {
		t.Fatal("timestamp normalization")
	}
	changed := strings.Replace(validBody, "key-1", "key-2", 1)
	n, err = Decode(changed, wallet, []string{"provider-a"})
	if err != nil || n.Hash == m.Hash || n.Request.Fingerprint() != m.Request.Fingerprint() {
		t.Fatal("inbox must hash key; financial hash excludes it")
	}
	for _, body := range []string{
		strings.Replace(validBody, `"messageId":"msg-1"`, `"messageId":"msg-1","messageId":"msg-1"`, 1),
		strings.Replace(validBody, `"kind":"BET"`, `"kind":"BET","Kind":"WIN"`, 1),
		strings.Replace(validBody, `"money":`, `"unknown":1,"money":`, 1),
		strings.Replace(validBody, `"amount":"1.00"`, `"amount":1.00`, 1),
		strings.Replace(validBody, `"provider-a"`, `"untrusted"`, 1),
		strings.Replace(validBody, `"BET"`, `"OPENING"`, 1),
		strings.Replace(validBody, `"type"`, `"Type"`, 1),
		strings.Replace(validBody, `"occurredAt":"2026-09-08T12:00:00Z"`, `"occurredAt":null`, 1),
		validBody + `{}`, strings.Repeat("x", (64<<10)+1),
	} {
		if _, err := Decode(body, wallet, []string{"provider-a"}); err == nil {
			t.Fatal("unsafe message accepted")
		}
	}
	if _, err := Decode(validBody, "another-wallet", []string{"provider-a"}); err == nil {
		t.Fatal("wrong broker group accepted")
	}
}
