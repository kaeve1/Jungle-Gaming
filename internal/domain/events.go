package domain

import (
	"encoding/json"
	"time"

	"jungle-gaming-backend/internal/domain/money"
)

// timeLayout: UTC, RFC 3339 com milissegundos (docs/CONTRATOS.md §1).
const timeLayout = "2006-01-02T15:04:05.000Z07:00"

func formatTime(t time.Time) string { return t.UTC().Format(timeLayout) }

// Event é um evento de integração. Tipo e versão são fixados por cada tipo
// concreto; só este pacote cria implementações (método não exportado).
// Os campos são preenchidos pelo construtor e formam um snapshot imutável:
// valores monetários e instantes já estão no formato final.
type Event interface {
	EventType() string
	EventVersion() int
	AggregateID() ID
	// PartitionKey é a carteira: define o MessageGroupId (ordem por carteira).
	PartitionKey() ID
	OccurredAt() time.Time
	isEvent()
}

// TransactionIdentity identifica a operação em todos os eventos de transação.
// Campos externos são nulos em operações internas (OPENING).
type TransactionIdentity struct {
	TransactionID         ID          `json:"transactionId"`
	Origin                Origin      `json:"origin"`
	Kind                  Kind        `json:"kind"`
	WalletID              ID          `json:"walletId"`
	PlayerID              ID          `json:"playerId"`
	ProviderID            *string     `json:"providerId"`
	ExternalTransactionID *string     `json:"externalTransactionId"`
	RoundID               *string     `json:"roundId"`
	GameID                *string     `json:"gameId"`
	Money                 money.Money `json:"money"`
}

func identityOf(t *WagerTransaction) TransactionIdentity {
	id := TransactionIdentity{
		TransactionID: t.id, Origin: t.origin, Kind: t.kind,
		WalletID: t.walletID, PlayerID: t.playerID, Money: t.amount,
	}
	if t.ext != nil {
		id.ProviderID = ptr(t.ext.ProviderID)
		id.ExternalTransactionID = ptr(t.ext.ExternalTransactionID)
		id.RoundID = ptr(t.ext.RoundID)
		id.GameID = ptr(t.ext.GameID)
	}
	return id
}

func ptr[T any](v T) *T { return &v }

// WagerTransactionProcessed: conclusão bem-sucedida (inclui LOSS e OPENING).
type WagerTransactionProcessed struct {
	TransactionIdentity
	ReferenceTransactionID *ID         `json:"referenceTransactionId"`
	BalanceAfter           money.Money `json:"balanceAfter"`
	ProcessedAt            string      `json:"processedAt"`
	at                     time.Time
}

func newWagerTransactionProcessed(t *WagerTransaction) WagerTransactionProcessed {
	e := WagerTransactionProcessed{
		TransactionIdentity: identityOf(t), BalanceAfter: t.resultBalance,
		ProcessedAt: formatTime(t.updatedAt), at: t.updatedAt,
	}
	if !t.referenceTxID.IsZero() {
		e.ReferenceTransactionID = ptr(t.referenceTxID)
	}
	return e
}

func (WagerTransactionProcessed) EventType() string       { return "WagerTransactionProcessed" }
func (WagerTransactionProcessed) EventVersion() int       { return 1 }
func (e WagerTransactionProcessed) AggregateID() ID       { return e.TransactionID }
func (e WagerTransactionProcessed) PartitionKey() ID      { return e.WalletID }
func (e WagerTransactionProcessed) OccurredAt() time.Time { return e.at }
func (WagerTransactionProcessed) isEvent()                {}

// WagerTransactionRejected: rejeição definitiva por regra de negócio.
type WagerTransactionRejected struct {
	TransactionIdentity
	ReferenceExternalTransactionID *string     `json:"referenceExternalTransactionId"`
	FailureCode                    FailureCode `json:"failureCode"`
	RejectedAt                     string      `json:"rejectedAt"`
	at                             time.Time
}

func newWagerTransactionRejected(t *WagerTransaction) WagerTransactionRejected {
	e := WagerTransactionRejected{
		TransactionIdentity: identityOf(t), FailureCode: t.failureCode,
		RejectedAt: formatTime(t.updatedAt), at: t.updatedAt,
	}
	if t.ext != nil && t.ext.ReferenceExternalTransactionID != "" {
		e.ReferenceExternalTransactionID = ptr(t.ext.ReferenceExternalTransactionID)
	}
	return e
}

func (WagerTransactionRejected) EventType() string       { return "WagerTransactionRejected" }
func (WagerTransactionRejected) EventVersion() int       { return 1 }
func (e WagerTransactionRejected) AggregateID() ID       { return e.TransactionID }
func (e WagerTransactionRejected) PartitionKey() ID      { return e.WalletID }
func (e WagerTransactionRejected) OccurredAt() time.Time { return e.at }
func (WagerTransactionRejected) isEvent()                {}

// WagerTransactionPendingReference: operação aguardando sua referência.
type WagerTransactionPendingReference struct {
	TransactionIdentity
	ReferenceExternalTransactionID string `json:"referenceExternalTransactionId"`
	ExpiresAt                      string `json:"expiresAt"`
	at                             time.Time
}

func newWagerTransactionPendingReference(t *WagerTransaction) WagerTransactionPendingReference {
	return WagerTransactionPendingReference{
		TransactionIdentity:            identityOf(t),
		ReferenceExternalTransactionID: t.ext.ReferenceExternalTransactionID,
		ExpiresAt:                      formatTime(t.refExpiresAt),
		at:                             t.updatedAt,
	}
}

func (WagerTransactionPendingReference) EventType() string       { return "WagerTransactionPendingReference" }
func (WagerTransactionPendingReference) EventVersion() int       { return 1 }
func (e WagerTransactionPendingReference) AggregateID() ID       { return e.TransactionID }
func (e WagerTransactionPendingReference) PartitionKey() ID      { return e.WalletID }
func (e WagerTransactionPendingReference) OccurredAt() time.Time { return e.at }
func (WagerTransactionPendingReference) isEvent()                {}

// WalletBalanceChanged: alteração efetiva de saldo (um por lançamento).
type WalletBalanceChanged struct {
	WalletID      ID          `json:"walletId"`
	TransactionID ID          `json:"transactionId"`
	Direction     Direction   `json:"direction"`
	Money         money.Money `json:"money"`
	BalanceBefore money.Money `json:"balanceBefore"`
	BalanceAfter  money.Money `json:"balanceAfter"`
	WalletVersion int64       `json:"walletVersion"`
	at            time.Time
}

func newWalletBalanceChanged(e LedgerEntry) WalletBalanceChanged {
	d := e.d
	return WalletBalanceChanged{
		WalletID: d.WalletID, TransactionID: d.TransactionID, Direction: d.Direction, Money: d.Amount,
		BalanceBefore: d.BalanceBefore, BalanceAfter: d.BalanceAfter, WalletVersion: d.WalletVersion, at: d.CreatedAt,
	}
}

func (WalletBalanceChanged) EventType() string       { return "WalletBalanceChanged" }
func (WalletBalanceChanged) EventVersion() int       { return 1 }
func (e WalletBalanceChanged) AggregateID() ID       { return e.WalletID }
func (e WalletBalanceChanged) PartitionKey() ID      { return e.WalletID }
func (e WalletBalanceChanged) OccurredAt() time.Time { return e.at }
func (WalletBalanceChanged) isEvent()                {}

// Envelope é a forma publicada de um evento (docs/CONTRATOS.md §4).
type Envelope struct {
	eventID       ID
	correlationID string
	causationID   string
	event         Event
}

// NewEnvelope embrulha um evento. eventID é estável: republicações reutilizam o
// mesmo envelope gravado na outbox. causationID é opcional.
func NewEnvelope(eventID ID, e Event, correlationID, causationID string) (Envelope, error) {
	if eventID.IsZero() || e == nil {
		return Envelope{}, ErrUninitialized
	}
	if !validExternalID(correlationID) || (causationID != "" && !validExternalID(causationID)) {
		return Envelope{}, invariant("invalid correlationId or causationId")
	}
	frozen := cloneEvent(e)
	if frozen == nil || frozen.AggregateID().IsZero() || frozen.PartitionKey().IsZero() || frozen.OccurredAt().IsZero() {
		return Envelope{}, invariant("invalid event snapshot")
	}
	if _, err := json.Marshal(frozen); err != nil {
		return Envelope{}, invariant("invalid event payload: %v", err)
	}
	return Envelope{eventID: eventID, correlationID: correlationID, causationID: causationID, event: frozen}, nil
}

func (v Envelope) EventID() ID  { return v.eventID }
func (v Envelope) Event() Event { return cloneEvent(v.event) }

func copyPtr[T any](p *T) *T {
	if p == nil {
		return nil
	}
	return ptr(*p)
}

func cloneIdentity(v TransactionIdentity) TransactionIdentity {
	v.ProviderID = copyPtr(v.ProviderID)
	v.ExternalTransactionID = copyPtr(v.ExternalTransactionID)
	v.RoundID = copyPtr(v.RoundID)
	v.GameID = copyPtr(v.GameID)
	return v
}

// Cada fronteira copia todos os ponteiros; nenhum chamador retém aliases do snapshot.
func cloneEvent(e Event) Event {
	switch v := e.(type) {
	case WagerTransactionProcessed:
		v.TransactionIdentity = cloneIdentity(v.TransactionIdentity)
		v.ReferenceTransactionID = copyPtr(v.ReferenceTransactionID)
		return v
	case WagerTransactionRejected:
		v.TransactionIdentity = cloneIdentity(v.TransactionIdentity)
		v.ReferenceExternalTransactionID = copyPtr(v.ReferenceExternalTransactionID)
		return v
	case WagerTransactionPendingReference:
		v.TransactionIdentity = cloneIdentity(v.TransactionIdentity)
		return v
	case WalletBalanceChanged:
		return v
	case *WagerTransactionProcessed:
		if v != nil {
			return cloneEvent(*v)
		}
	case *WagerTransactionRejected:
		if v != nil {
			return cloneEvent(*v)
		}
	case *WagerTransactionPendingReference:
		if v != nil {
			return cloneEvent(*v)
		}
	case *WalletBalanceChanged:
		if v != nil {
			return cloneEvent(*v)
		}
	}
	return nil
}

// MarshalJSON produz o envelope do contrato; tipo e versão vêm do evento.
func (v Envelope) MarshalJSON() ([]byte, error) {
	if v.event == nil || v.eventID.IsZero() {
		return nil, ErrUninitialized
	}
	var causation *string
	if v.causationID != "" {
		causation = &v.causationID
	}
	return json.Marshal(struct {
		EventID       ID      `json:"eventId"`
		EventType     string  `json:"eventType"`
		AggregateID   ID      `json:"aggregateId"`
		CorrelationID string  `json:"correlationId"`
		CausationID   *string `json:"causationId"`
		OccurredAt    string  `json:"occurredAt"`
		Version       int     `json:"version"`
		Data          Event   `json:"data"`
	}{
		v.eventID, v.event.EventType(), v.event.AggregateID(), v.correlationID, causation,
		formatTime(v.event.OccurredAt()), v.event.EventVersion(), v.event,
	})
}
