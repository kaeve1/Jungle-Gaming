package sqsconsumer

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"jungle-gaming-backend/internal/adapters/jsonstrict"
	"jungle-gaming-backend/internal/domain"
	"jungle-gaming-backend/internal/domain/money"
	"regexp"
	"time"
)

var ErrInvalidMessage = errors.New("invalid message envelope or untrusted provider")
var messageIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

type Message struct {
	ID, Hash string
	Request  domain.ExternalRequest
}

// Broker authenticates a trusted INTERNAL gateway, never an arbitrary provider.
// Hash covers normalized envelope, business data AND idempotency key. The domain
// financial hash remains transport-independent and excludes that key.
func Decode(body, group string, providers []string) (Message, error) {
	var envelope struct {
		ID   string          `json:"messageId"`
		Type string          `json:"type"`
		At   string          `json:"occurredAt"`
		Data json.RawMessage `json:"data"`
	}
	if len(body) > 64<<10 || jsonstrict.Decode([]byte(body), &envelope, "messageId", "type", "occurredAt", "data") != nil || !messageIDPattern.MatchString(envelope.ID) || envelope.Type != "WagerTransactionRequested" {
		return Message{}, ErrInvalidMessage
	}
	at, err := time.Parse(time.RFC3339Nano, envelope.At)
	if err != nil || at.IsZero() {
		return Message{}, ErrInvalidMessage
	}
	var data struct {
		Provider  string      `json:"providerId"`
		External  string      `json:"externalTransactionId"`
		Key       string      `json:"idempotencyKey"`
		Player    string      `json:"playerId"`
		Wallet    string      `json:"walletId"`
		Round     string      `json:"roundId"`
		Game      string      `json:"gameId"`
		Kind      string      `json:"kind"`
		Money     money.Money `json:"money"`
		Reference string      `json:"referenceExternalTransactionId"`
	}
	if jsonstrict.Decode(envelope.Data, &data, "providerId", "externalTransactionId", "idempotencyKey", "playerId", "walletId", "roundId", "gameId", "kind", "money", "referenceExternalTransactionId") != nil {
		return Message{}, ErrInvalidMessage
	}
	allowed := false
	for _, p := range providers {
		if p == data.Provider {
			allowed = true
		}
	}
	if !allowed {
		return Message{}, ErrInvalidMessage
	}
	req, err := domain.NewExternalRequest(domain.RawExternalRequest{ProviderID: data.Provider, ExternalTransactionID: data.External, IdempotencyKey: data.Key, PlayerID: data.Player, WalletID: data.Wallet, RoundID: data.Round, GameID: data.Game, Kind: data.Kind, Money: data.Money, ReferenceExternalTransactionID: data.Reference})
	if err != nil || group != req.WalletID().String() {
		return Message{}, ErrInvalidMessage
	}
	canonical, _ := json.Marshal(map[string]any{"messageId": envelope.ID, "type": envelope.Type, "occurredAt": at.UTC().Format(time.RFC3339Nano), "data": json.RawMessage(req.CanonicalJSON()), "idempotencyKey": req.IdempotencyKey()})
	hash := sha256.Sum256(canonical)
	return Message{envelope.ID, hex.EncodeToString(hash[:]), req}, nil
}
