package domain

import (
	"errors"
	"time"

	"jungle-gaming-backend/internal/domain/money"
)

// ReferencePolicy controla a espera por referências ausentes (ARCHITECTURE.md §8).
type ReferencePolicy struct {
	TTL       time.Duration // prazo total desde a primeira espera
	BaseDelay time.Duration // primeira espera; dobra a cada tentativa
	MaxDelay  time.Duration // teto entre tentativas
}

// DefaultReferencePolicy: TTL de 10 minutos, 1s → 2s → 4s ... até 60s.
var DefaultReferencePolicy = ReferencePolicy{TTL: 10 * time.Minute, BaseDelay: time.Second, MaxDelay: time.Minute}

// Limites de sanidade: evitam overflow de time.Duration no backoff e
// pendências que ficariam abertas por tempo absurdo por erro de configuração.
const (
	maxReferenceTTL   = 24 * time.Hour
	maxReferenceDelay = time.Hour
)

// Validate recusa políticas que gerariam espera infinita, nenhuma espera ou
// valores fora dos limites de sanidade.
func (p ReferencePolicy) Validate() error {
	if p.TTL <= 0 || p.BaseDelay <= 0 || p.MaxDelay < p.BaseDelay || p.BaseDelay > p.TTL || p.TTL > maxReferenceTTL || p.MaxDelay > maxReferenceDelay {
		return invariant("invalid reference policy %+v", p)
	}
	return nil
}

// Delay devolve a espera antes da tentativa seguinte à de número attempt (0 = primeira).
func (p ReferencePolicy) Delay(attempt int) time.Duration {
	d := p.BaseDelay
	for i := 0; i < attempt && d < p.MaxDelay; i++ {
		d *= 2
	}
	return min(d, p.MaxDelay)
}

// ReferenceLookup é o que a aplicação encontrou sobre a referência, sob o lock
// da carteira. Reference nil = não encontrada para este provedor.
type ReferenceLookup struct {
	Reference *WagerTransaction
	// AlreadyReversed: já existe reversão (REFUND ou ROLLBACK) PROCESSED para a referência.
	AlreadyReversed bool
}

// Outcome é o efeito de uma decisão, a ser persistido numa única transação SQL
// junto com a carteira e a transação.
type Outcome struct {
	Entry  *LedgerEntry // nil quando não há movimentação
	Events []Event      // dados dos eventos; a aplicação os embrulha em Envelope
}

// Process aplica as regras de negócio a uma transação PENDING (nova) ou
// PENDING_REFERENCE (retomada pelo worker) sobre a carteira já travada.
//
// O resultado de negócio fica registrado na própria transação (PROCESSED,
// REJECTED ou PENDING_REFERENCE). Erros retornados indicam uso incorreto ou
// dados inconsistentes — nunca uma rejeição de negócio.
func Process(tx *WagerTransaction, w *Wallet, ref ReferenceLookup, policy ReferencePolicy, ids IDGenerator, now time.Time) (Outcome, error) {
	if tx == nil || w == nil || ids == nil {
		return Outcome{}, ErrUninitialized
	}
	if err := policy.Validate(); err != nil {
		return Outcome{}, err
	}
	if tx.origin != OriginExternal {
		return Outcome{}, invariant("only external transactions are processed here")
	}
	if tx.status != StatusPending && tx.status != StatusPendingReference {
		return Outcome{}, ErrInvalidTransition
	}
	if tx.walletID != w.id {
		return Outcome{}, invariant("transaction wallet %s differs from loaded wallet %s", tx.walletID, w.id)
	}
	if now.IsZero() {
		return Outcome{}, invariant("time is required")
	}
	now = normTime(now)
	p := processor{tx: tx, w: w, policy: policy, ids: ids, now: now}

	// Divergências com a carteira são definitivas e independem da referência.
	if tx.playerID != w.playerID {
		return p.reject(CodeWalletPlayerMismatch, false) // não revela o saldo (VULN-002)
	}
	if tx.amount.Currency() != w.Currency() {
		return p.reject(CodeCurrencyMismatch, true)
	}

	refTx, waiting, code, err := p.resolveReference(ref)
	if err != nil {
		return Outcome{}, err
	}
	if code != "" {
		return p.rejectWithReference(code, true, refTx)
	}
	if waiting {
		return p.awaitReference()
	}
	return p.apply(refTx)
}

type processor struct {
	tx     *WagerTransaction
	w      *Wallet
	policy ReferencePolicy
	ids    IDGenerator
	now    time.Time
}

// resolveReference devolve a referência encontrada e, conforme o caso, se é
// preciso esperar ou o código de rejeição. Erro = a aplicação entregou uma
// referência que não corresponde à busca (bug), o que nunca deve virar uma
// rejeição de negócio persistida.
func (p processor) resolveReference(lookup ReferenceLookup) (*WagerTransaction, bool, FailureCode, error) {
	tx := p.tx
	ref := lookup.Reference
	if tx.ext.ReferenceExternalTransactionID == "" {
		if ref != nil {
			return nil, false, "", invariant("%s without reference received a reference", tx.kind)
		}
		return nil, false, "", nil // BET, LOSS ou WIN sem referência
	}
	if ref == nil {
		return nil, true, "", nil
	}
	if ref.ext == nil || ref.ext.ProviderID != tx.ext.ProviderID ||
		ref.ext.ExternalTransactionID != tx.ext.ReferenceExternalTransactionID {
		return nil, false, "", invariant("reference does not match lookup (provider, referenceExternalTransactionId)")
	}
	if ref.id == tx.id {
		return nil, false, "", invariant("transaction cannot reference itself")
	}
	if ref.status == StatusPending || ref.status == StatusPendingReference {
		return nil, true, "", nil
	}
	if ref.status != StatusProcessed {
		return ref, false, CodeReferenceNotProcessed, nil
	}
	if !referenceKindAllowed(tx.kind, ref.kind) {
		return ref, false, CodeReferenceKindNotAllowed, nil
	}
	if ref.playerID != tx.playerID || ref.walletID != tx.walletID ||
		ref.amount.Currency() != tx.amount.Currency() || ref.ext.RoundID != tx.ext.RoundID {
		return ref, false, CodeReferenceMismatch, nil
	}
	if tx.kind.IsReversal() {
		if ref.amount != tx.amount {
			return ref, false, CodeReferenceAmountMismatch, nil
		}
		if lookup.AlreadyReversed {
			return ref, false, CodeReferenceAlreadyReversed, nil
		}
	}
	return ref, false, "", nil
}

// referenceKindAllowed: REFUND→BET; ROLLBACK→BET/WIN/REFUND; WIN→BET.
func referenceKindAllowed(kind, refKind Kind) bool {
	switch kind {
	case KindRefund, KindWin:
		return refKind == KindBet
	case KindRollback:
		return refKind == KindBet || refKind == KindWin || refKind == KindRefund
	}
	return false
}

func (p processor) awaitReference() (Outcome, error) {
	tx := p.tx
	if tx.status == StatusPending {
		expires := p.now.Add(p.policy.TTL)
		if err := tx.AwaitReference(expires, p.now.Add(p.policy.Delay(0)), p.now); err != nil {
			return Outcome{}, err
		}
		return Outcome{Events: []Event{newWagerTransactionPendingReference(tx)}}, nil
	}
	// Retomada: só expira numa tentativa real, com o banco respondendo — uma
	// queda do banco nunca vira "referência não encontrada" (R72).
	if !p.now.Before(tx.refExpiresAt) {
		return p.reject(CodeReferenceNotFound, true)
	}
	next := p.now.Add(p.policy.Delay(tx.refAttempts + 1))
	if next.After(tx.refExpiresAt) {
		next = tx.refExpiresAt // última tentativa exatamente no prazo
	}
	return Outcome{}, tx.ScheduleReferenceRetry(next, p.now)
}

func (p processor) apply(ref *WagerTransaction) (Outcome, error) {
	tx := p.tx
	var refID ID
	if ref != nil {
		refID = ref.id
	}

	dir, moves := movement(tx.kind, ref)
	var entry *LedgerEntry
	if moves {
		var e LedgerEntry
		var err error
		if dir == Debit {
			e, err = p.w.Debit(tx.amount, tx.id, p.ids.NewID(), p.now)
		} else {
			e, err = p.w.Credit(tx.amount, tx.id, p.ids.NewID(), p.now)
		}
		switch {
		case errors.Is(err, ErrInsufficientFunds) && tx.kind == KindRollback:
			return p.rejectWithReference(CodeReversalInsufficientFunds, true, ref)
		case errors.Is(err, ErrInsufficientFunds):
			return p.reject(CodeInsufficientFunds, true)
		case errors.Is(err, ErrBalanceLimitExceeded):
			return p.rejectWithReference(CodeBalanceLimitExceeded, true, ref)
		case err != nil:
			return Outcome{}, err
		}
		entry = &e
	}

	if err := tx.MarkProcessed(p.w.balance, refID, p.now); err != nil {
		return Outcome{}, err
	}
	out := Outcome{Entry: entry, Events: []Event{newWagerTransactionProcessed(tx)}}
	if entry != nil {
		out.Events = append(out.Events, newWalletBalanceChanged(*entry))
	}
	return out, nil
}

// movement: BET debita; WIN e REFUND creditam; LOSS não move; ROLLBACK faz o
// movimento contrário ao da operação referenciada.
func movement(kind Kind, ref *WagerTransaction) (Direction, bool) {
	switch kind {
	case KindBet:
		return Debit, true
	case KindWin, KindRefund:
		return Credit, true
	case KindRollback:
		if ref.kind == KindBet {
			return Credit, true
		}
		return Debit, true // desfaz WIN ou REFUND
	}
	return "", false // LOSS
}

func (p processor) reject(code FailureCode, revealBalance bool) (Outcome, error) {
	return p.rejectWithReference(code, revealBalance, nil)
}

func (p processor) rejectWithReference(code FailureCode, revealBalance bool, ref *WagerTransaction) (Outcome, error) {
	var balance *money.Money
	if revealBalance {
		b := p.w.balance
		balance = &b
	}
	var refID ID
	if ref != nil {
		refID = ref.id
	}
	if err := p.tx.Reject(code, balance, refID, p.now); err != nil {
		return Outcome{}, err
	}
	return Outcome{Events: []Event{newWagerTransactionRejected(p.tx)}}, nil
}
