package domain

import (
	"encoding/hex"
	"fmt"
	"time"

	"jungle-gaming-backend/internal/domain/money"
)

// Kind é o tipo da operação.
type Kind string

const (
	KindOpening  Kind = "OPENING" // interno: abertura de carteira
	KindBet      Kind = "BET"
	KindWin      Kind = "WIN"
	KindLoss     Kind = "LOSS"
	KindRefund   Kind = "REFUND"
	KindRollback Kind = "ROLLBACK"
)

// IsExternal informa se o tipo pode chegar por HTTP/SQS (OPENING não pode).
func (k Kind) IsExternal() bool {
	switch k {
	case KindBet, KindWin, KindLoss, KindRefund, KindRollback:
		return true
	}
	return false
}

func (k Kind) valid() bool { return k == KindOpening || k.IsExternal() }

type referenceRule int

const (
	referenceForbidden referenceRule = iota
	referenceOptional
	referenceRequired
)

func (k Kind) referencePolicy() referenceRule {
	switch k {
	case KindRefund, KindRollback:
		return referenceRequired
	case KindWin:
		return referenceOptional
	}
	return referenceForbidden
}

// IsReversal informa se o tipo desfaz outra operação.
func (k Kind) IsReversal() bool { return k == KindRefund || k == KindRollback }

// Status é o estado da transação (ARCHITECTURE.md §7).
type Status string

const (
	StatusPending          Status = "PENDING"
	StatusPendingReference Status = "PENDING_REFERENCE"
	StatusProcessed        Status = "PROCESSED"
	StatusRejected         Status = "REJECTED"
	StatusFailed           Status = "FAILED"
)

// IsTerminal informa se o estado não admite novas transições.
func (s Status) IsTerminal() bool {
	return s == StatusProcessed || s == StatusRejected || s == StatusFailed
}

func (s Status) valid() bool {
	return s == StatusPending || s == StatusPendingReference || s.IsTerminal()
}

// Origin separa operações internas (OPENING) de externas (provedores).
type Origin string

const (
	OriginInternal Origin = "INTERNAL"
	OriginExternal Origin = "EXTERNAL"
)

// ExternalData são os metadados que só existem em operações de provedores.
type ExternalData struct {
	ProviderID                     string
	ExternalTransactionID          string
	IdempotencyKey                 string
	RequestHash                    string
	RequestHashVersion             int // FingerprintVersion usada para calcular RequestHash
	RoundID                        string
	GameID                         string
	ReferenceExternalTransactionID string // vazio = sem referência
}

// WagerTransaction é uma operação financeira. Criação (NewExternalTransaction,
// newOpeningTransaction) e reidratação (RehydrateTransaction) são separadas; a
// reidratação não aplica movimentações nem gera eventos.
type WagerTransaction struct {
	id            ID
	origin        Origin
	kind          Kind
	status        Status
	walletID      ID
	playerID      ID
	amount        money.Money
	ext           *ExternalData
	referenceTxID ID
	failureCode   FailureCode
	resultBalance money.Money // inválido (zero) quando não há saldo de resultado
	refAttempts   int
	refNextAt     time.Time
	refExpiresAt  time.Time
	createdAt     time.Time
	updatedAt     time.Time
}

// TransactionSnapshot são os campos persistidos de uma transação.
type TransactionSnapshot struct {
	ID                     ID
	Origin                 Origin
	Kind                   Kind
	Status                 Status
	WalletID               ID
	PlayerID               ID
	Money                  money.Money
	External               *ExternalData // nil em operações internas
	ReferenceTransactionID ID            // zero se não resolvida/aplicável
	FailureCode            FailureCode   // vazio se não REJECTED/FAILED
	ResultBalance          *money.Money  // saldo devolvido ao provedor; nil se ausente
	ReferenceAttempts      int
	NextReferenceAttemptAt time.Time
	ReferenceExpiresAt     time.Time
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

// NewExternalTransaction cria a transação de um pedido validado, em PENDING.
func NewExternalTransaction(id ID, req ExternalRequest, now time.Time) (*WagerTransaction, error) {
	if id.IsZero() || req.kind == "" || now.IsZero() {
		return nil, ErrUninitialized
	}
	now = normTime(now)
	return &WagerTransaction{
		id: id, origin: OriginExternal, kind: req.kind, status: StatusPending,
		walletID: req.walletID, playerID: req.playerID, amount: req.amount,
		ext: &ExternalData{
			ProviderID: req.providerID, ExternalTransactionID: req.externalID,
			IdempotencyKey: req.idempotencyKey, RequestHash: req.Fingerprint(), RequestHashVersion: FingerprintVersion,
			RoundID: req.roundID, GameID: req.gameID, ReferenceExternalTransactionID: req.referenceExtID,
		},
		createdAt: now, updatedAt: now,
	}, nil
}

func newOpeningTransaction(id, walletID, playerID ID, amount money.Money, now time.Time) *WagerTransaction {
	return &WagerTransaction{
		id: id, origin: OriginInternal, kind: KindOpening, status: StatusPending,
		walletID: walletID, playerID: playerID, amount: amount, createdAt: now, updatedAt: now,
	}
}

// RehydrateTransaction reconstrói uma transação lida do banco, validando a
// coerência dos campos. Não executa transições nem gera eventos.
func RehydrateTransaction(s TransactionSnapshot) (*WagerTransaction, error) {
	s.CreatedAt, s.UpdatedAt = normTime(s.CreatedAt), normTime(s.UpdatedAt)
	if !s.NextReferenceAttemptAt.IsZero() {
		s.NextReferenceAttemptAt = normTime(s.NextReferenceAttemptAt)
	}
	if !s.ReferenceExpiresAt.IsZero() {
		s.ReferenceExpiresAt = normTime(s.ReferenceExpiresAt)
	}
	if err := s.validate(); err != nil {
		return nil, err
	}
	t := &WagerTransaction{
		id: s.ID, origin: s.Origin, kind: s.Kind, status: s.Status, walletID: s.WalletID,
		playerID: s.PlayerID, amount: s.Money, referenceTxID: s.ReferenceTransactionID,
		failureCode: s.FailureCode, refAttempts: s.ReferenceAttempts, refNextAt: s.NextReferenceAttemptAt,
		refExpiresAt: s.ReferenceExpiresAt, createdAt: s.CreatedAt, updatedAt: s.UpdatedAt,
	}
	if s.External != nil {
		ext := *s.External
		t.ext = &ext
	}
	if s.ResultBalance != nil {
		t.resultBalance = *s.ResultBalance
	}
	return t, nil
}

func (s TransactionSnapshot) validate() error {
	switch {
	case s.ID.IsZero() || s.WalletID.IsZero() || s.PlayerID.IsZero():
		return invariant("transaction ids are required")
	case !s.Kind.valid() || !s.Status.valid():
		return invariant("invalid kind %q or status %q", s.Kind, s.Status)
	case s.Origin != OriginInternal && s.Origin != OriginExternal:
		return invariant("invalid origin %q", s.Origin)
	case (s.Origin == OriginInternal) != (s.Kind == KindOpening):
		return invariant("OPENING must be internal and only OPENING may be internal")
	case (s.Origin == OriginExternal) != (s.External != nil):
		return invariant("external metadata must exist only for external origin")
	case !s.Money.Valid() || s.Money.IsNegative():
		return invariant("transaction money must be valid and non-negative")
	case (s.Kind == KindLoss && !s.Money.IsZero()) || (s.Kind != KindLoss && !s.Money.IsPositive()):
		return invariant("amount violates the policy for %s", s.Kind)
	case s.ReferenceTransactionID == s.ID:
		return invariant("transaction cannot reference itself")
	case (s.Status == StatusRejected || s.Status == StatusFailed) != (s.FailureCode != ""):
		return invariant("failure code must be present only for REJECTED/FAILED")
	case s.Status == StatusRejected && !s.FailureCode.IsRejection():
		return invariant("REJECTED requires a rejection code, got %q", s.FailureCode)
	case s.Status == StatusFailed && !s.FailureCode.IsFailure():
		return invariant("FAILED requires a failure code, got %q", s.FailureCode)
	case s.Status == StatusProcessed && s.ResultBalance == nil:
		return invariant("PROCESSED requires a result balance")
	case s.Status == StatusPendingReference && (s.NextReferenceAttemptAt.IsZero() || s.ReferenceExpiresAt.IsZero()):
		return invariant("PENDING_REFERENCE requires schedule")
	case !s.ReferenceTransactionID.IsZero() && s.Kind.referencePolicy() == referenceForbidden:
		return invariant("%s cannot have a reference", s.Kind)
	case s.ReferenceAttempts < 0:
		return invariant("negative reference attempts")
	case s.CreatedAt.IsZero() || s.UpdatedAt.Before(s.CreatedAt):
		return invariant("transaction timestamps are inconsistent")
	}
	if s.ResultBalance != nil && (!s.ResultBalance.Valid() || s.ResultBalance.IsNegative()) {
		return invariant("result balance must be valid and non-negative")
	}
	if s.Status == StatusProcessed && s.ResultBalance.Currency() != s.Money.Currency() {
		return invariant("processed result currency must match transaction")
	}
	if s.Status == StatusPendingReference {
		if s.External == nil || s.External.ReferenceExternalTransactionID == "" || s.NextReferenceAttemptAt.After(s.ReferenceExpiresAt) {
			return invariant("reference wait requires a reference and a valid schedule")
		}
	}
	if s.External != nil {
		e := s.External
		if _, err := hex.DecodeString(e.RequestHash); err != nil {
			return invariant("invalid request hash")
		}
		if !validExternalID(e.ProviderID) || !validExternalID(e.ExternalTransactionID) || !validIdempotencyKey(e.IdempotencyKey) ||
			len(e.RequestHash) != 64 || e.RequestHashVersion < 1 || !validExternalID(e.RoundID) || !validExternalID(e.GameID) {
			return invariant("invalid external metadata")
		}
		if (s.Kind.referencePolicy() == referenceRequired) && e.ReferenceExternalTransactionID == "" {
			return invariant("%s requires a reference", s.Kind)
		}
		if e.ReferenceExternalTransactionID != "" &&
			(s.Kind.referencePolicy() == referenceForbidden || !validExternalID(e.ReferenceExternalTransactionID)) {
			return invariant("invalid external reference for %s", s.Kind)
		}
		if e.ReferenceExternalTransactionID == e.ExternalTransactionID {
			return invariant("external transaction cannot reference itself")
		}
		if e.ReferenceExternalTransactionID == "" && !s.ReferenceTransactionID.IsZero() {
			return invariant("resolved reference requires external reference")
		}
		if s.Status == StatusProcessed && e.ReferenceExternalTransactionID != "" && s.ReferenceTransactionID.IsZero() {
			return invariant("processed transaction requires its resolved reference")
		}
	}
	return nil
}

// Snapshot devolve uma cópia dos campos para persistência.
func (t *WagerTransaction) Snapshot() TransactionSnapshot {
	s := TransactionSnapshot{
		ID: t.id, Origin: t.origin, Kind: t.kind, Status: t.status, WalletID: t.walletID,
		PlayerID: t.playerID, Money: t.amount, ReferenceTransactionID: t.referenceTxID,
		FailureCode: t.failureCode, ReferenceAttempts: t.refAttempts, NextReferenceAttemptAt: t.refNextAt,
		ReferenceExpiresAt: t.refExpiresAt, CreatedAt: t.createdAt, UpdatedAt: t.updatedAt,
	}
	if t.ext != nil {
		ext := *t.ext
		s.External = &ext
	}
	if t.resultBalance.Valid() {
		b := t.resultBalance
		s.ResultBalance = &b
	}
	return s
}

func (t *WagerTransaction) ID() ID                            { return t.id }
func (t *WagerTransaction) Origin() Origin                    { return t.origin }
func (t *WagerTransaction) Kind() Kind                        { return t.kind }
func (t *WagerTransaction) Status() Status                    { return t.status }
func (t *WagerTransaction) WalletID() ID                      { return t.walletID }
func (t *WagerTransaction) PlayerID() ID                      { return t.playerID }
func (t *WagerTransaction) Money() money.Money                { return t.amount }
func (t *WagerTransaction) ReferenceTransactionID() ID        { return t.referenceTxID }
func (t *WagerTransaction) FailureCode() FailureCode          { return t.failureCode }
func (t *WagerTransaction) ReferenceAttempts() int            { return t.refAttempts }
func (t *WagerTransaction) NextReferenceAttemptAt() time.Time { return t.refNextAt }
func (t *WagerTransaction) ReferenceExpiresAt() time.Time     { return t.refExpiresAt }
func (t *WagerTransaction) CreatedAt() time.Time              { return t.createdAt }
func (t *WagerTransaction) UpdatedAt() time.Time              { return t.updatedAt }

// External devolve uma cópia dos metadados externos (nil em operações internas).
func (t *WagerTransaction) External() *ExternalData {
	if t.ext == nil {
		return nil
	}
	ext := *t.ext
	return &ext
}

// ResultBalance devolve o saldo registrado no processamento (para replays).
func (t *WagerTransaction) ResultBalance() (money.Money, bool) {
	return t.resultBalance, t.resultBalance.Valid()
}

func (t *WagerTransaction) transition(to Status, now time.Time, allowed ...Status) error {
	if t == nil || now.IsZero() {
		return ErrUninitialized
	}
	if err := t.Snapshot().validate(); err != nil {
		return err
	}
	for _, from := range allowed {
		if t.status == from {
			t.status = to
			t.touch(now)
			return nil
		}
	}
	return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, t.status, to)
}

func (t *WagerTransaction) touch(now time.Time) {
	if now = normTime(now); now.After(t.updatedAt) {
		t.updatedAt = now
	}
}

// MarkProcessed conclui a operação com o saldo observado (devolvido em replays).
func (t *WagerTransaction) MarkProcessed(resultBalance money.Money, referenceTxID ID, now time.Time) error {
	if t == nil {
		return ErrUninitialized
	}
	if !resultBalance.Valid() || resultBalance.IsNegative() {
		return invariant("processed transaction requires a valid result balance")
	}
	if !referenceTxID.IsZero() && t.kind.referencePolicy() == referenceForbidden {
		return invariant("%s cannot have a reference", t.kind)
	}
	if referenceTxID == t.id || resultBalance.Currency() != t.amount.Currency() {
		return invariant("invalid processed reference or result currency")
	}
	if t.ext != nil && t.ext.ReferenceExternalTransactionID != "" && referenceTxID.IsZero() {
		return invariant("processing requires a resolved reference")
	}
	if t.ext != nil && t.ext.ReferenceExternalTransactionID == "" && !referenceTxID.IsZero() {
		return invariant("resolved reference requires external reference")
	}
	if err := t.transition(StatusProcessed, now, StatusPending, StatusPendingReference); err != nil {
		return err
	}
	t.resultBalance, t.referenceTxID = resultBalance, referenceTxID
	return nil
}

// Reject registra uma rejeição de negócio definitiva. balance pode ser nil
// quando o saldo não deve ser revelado (ex.: WALLET_PLAYER_MISMATCH).
// referenceTxID é a referência encontrada, se houver, para auditoria (ex.:
// qual transação já tinha sido revertida); zero quando não se aplica.
func (t *WagerTransaction) Reject(code FailureCode, balance *money.Money, referenceTxID ID, now time.Time) error {
	if t == nil {
		return ErrUninitialized
	}
	if balance != nil && (!balance.Valid() || balance.IsNegative()) {
		return invariant("rejection result balance must be valid and non-negative")
	}
	if referenceTxID == t.id {
		return invariant("transaction cannot reference itself")
	}
	if t.ext != nil && t.ext.ReferenceExternalTransactionID == "" && !referenceTxID.IsZero() {
		return invariant("resolved reference requires external reference")
	}
	if !code.IsRejection() {
		return invariant("%q is not a rejection code", code)
	}
	if !referenceTxID.IsZero() && t.kind.referencePolicy() == referenceForbidden {
		return invariant("%s cannot have a reference", t.kind)
	}
	if err := t.transition(StatusRejected, now, StatusPending, StatusPendingReference); err != nil {
		return err
	}
	t.failureCode, t.referenceTxID = code, referenceTxID
	if balance != nil {
		t.resultBalance = *balance
	}
	return nil
}

// AwaitReference coloca em espera pela referência, com prazo e próxima tentativa.
func (t *WagerTransaction) AwaitReference(expiresAt, nextAttemptAt, now time.Time) error {
	if t == nil {
		return ErrUninitialized
	}
	if t.kind.referencePolicy() == referenceForbidden {
		return invariant("%s has no reference to await", t.kind)
	}
	if expiresAt.IsZero() || nextAttemptAt.IsZero() || nextAttemptAt.After(expiresAt) || t.ext == nil || t.ext.ReferenceExternalTransactionID == "" {
		return invariant("reference schedule is required")
	}
	if err := t.transition(StatusPendingReference, now, StatusPending); err != nil {
		return err
	}
	t.refExpiresAt, t.refNextAt = normTime(expiresAt), normTime(nextAttemptAt)
	return nil
}

// ScheduleReferenceRetry registra uma tentativa sem sucesso e agenda a próxima.
func (t *WagerTransaction) ScheduleReferenceRetry(nextAttemptAt, now time.Time) error {
	if t == nil || now.IsZero() {
		return ErrUninitialized
	}
	if err := t.Snapshot().validate(); err != nil {
		return err
	}
	if nextAttemptAt.IsZero() || nextAttemptAt.After(t.refExpiresAt) || t.refAttempts == int(^uint(0)>>1) {
		return invariant("invalid reference retry schedule")
	}
	if t.status != StatusPendingReference {
		return fmt.Errorf("%w: retry requires %s, got %s", ErrInvalidTransition, StatusPendingReference, t.status)
	}
	t.refAttempts++
	t.refNextAt = normTime(nextAttemptAt)
	t.touch(now)
	return nil
}

// Fail registra falha permanente de infraestrutura (auditoria).
func (t *WagerTransaction) Fail(code FailureCode, now time.Time) error {
	if !code.IsFailure() {
		return invariant("%q is not a failure code", code)
	}
	if err := t.transition(StatusFailed, now, StatusPending, StatusPendingReference); err != nil {
		return err
	}
	t.failureCode = code
	return nil
}
