package domain

import (
	"errors"
	"fmt"
	"time"
)

// Erros classificáveis com errors.Is. Rejeições de negócio NÃO são erros:
// são resultados persistidos (status REJECTED + FailureCode). Estes erros
// representam entrada inválida, uso incorreto da API do domínio ou dados
// corrompidos na reidratação.
var (
	// ErrValidation: entrada externa inválida; corrigível, nada é persistido.
	ErrValidation = errors.New("validation error")
	// ErrUninitialized: valor de domínio não construído por um construtor.
	ErrUninitialized = errors.New("uninitialized domain value")
	// ErrInvalidTransition: transição de estado não permitida (ex.: a partir de estado terminal).
	ErrInvalidTransition = errors.New("invalid state transition")
	// ErrInvariant: dados que violam uma invariante (reidratação de registro corrompido
	// ou chamada com objetos que não pertencem um ao outro).
	ErrInvariant = errors.New("domain invariant violated")

	ErrInsufficientFunds    = errors.New("insufficient funds")
	ErrBalanceLimitExceeded = errors.New("balance limit exceeded")
	ErrNonPositiveAmount    = errors.New("amount must be positive")
)

// ValidationError descreve uma entrada inválida. errors.Is(err, ErrValidation) é verdadeiro.
type ValidationError struct {
	Code   FailureCode
	Field  string
	Reason string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("%s: %s: %s", e.Code, e.Field, e.Reason)
}

func (e *ValidationError) Is(target error) bool { return target == ErrValidation }

func invalid(code FailureCode, field, reason string) error {
	return &ValidationError{Code: code, Field: field, Reason: reason}
}

func invariant(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvariant, fmt.Sprintf(format, args...))
}

// FailureCode é o código estável de falha documentado em docs/CONTRATOS.md §5.
type FailureCode string

// Entrada (não persistidos).
const (
	CodeValidationError       FailureCode = "VALIDATION_ERROR"
	CodeMissingIdempotencyKey FailureCode = "MISSING_IDEMPOTENCY_KEY"
	CodeInvalidKind           FailureCode = "INVALID_KIND"
	CodeInvalidAmount         FailureCode = "INVALID_AMOUNT"
	CodeUnsupportedCurrency   FailureCode = "UNSUPPORTED_CURRENCY"
)

// Rejeições de negócio (status REJECTED, persistidas).
const (
	CodeInsufficientFunds         FailureCode = "INSUFFICIENT_FUNDS"
	CodeReversalInsufficientFunds FailureCode = "REVERSAL_INSUFFICIENT_FUNDS"
	CodeWalletPlayerMismatch      FailureCode = "WALLET_PLAYER_MISMATCH"
	CodeCurrencyMismatch          FailureCode = "CURRENCY_MISMATCH"
	CodeBalanceLimitExceeded      FailureCode = "BALANCE_LIMIT_EXCEEDED"
	CodeReferenceNotFound         FailureCode = "REFERENCE_NOT_FOUND"
	CodeReferenceNotProcessed     FailureCode = "REFERENCE_NOT_PROCESSED"
	CodeReferenceKindNotAllowed   FailureCode = "REFERENCE_KIND_NOT_ALLOWED"
	CodeReferenceMismatch         FailureCode = "REFERENCE_MISMATCH"
	CodeReferenceAmountMismatch   FailureCode = "REFERENCE_AMOUNT_MISMATCH"
	CodeReferenceAlreadyReversed  FailureCode = "REFERENCE_ALREADY_REVERSED"
	CodeInternalProcessingFailed  FailureCode = "INTERNAL_PROCESSING_FAILED" // status FAILED
)

var rejectionCodes = map[FailureCode]bool{
	CodeInsufficientFunds: true, CodeReversalInsufficientFunds: true, CodeWalletPlayerMismatch: true,
	CodeCurrencyMismatch: true, CodeBalanceLimitExceeded: true, CodeReferenceNotFound: true,
	CodeReferenceNotProcessed: true, CodeReferenceKindNotAllowed: true, CodeReferenceMismatch: true,
	CodeReferenceAmountMismatch: true, CodeReferenceAlreadyReversed: true,
}

var failedCodes = map[FailureCode]bool{CodeInternalProcessingFailed: true}

// IsRejection informa se o código representa uma rejeição de negócio definitiva.
func (c FailureCode) IsRejection() bool { return rejectionCodes[c] }

// IsFailure informa se o código representa falha permanente (status FAILED).
func (c FailureCode) IsFailure() bool { return failedCodes[c] }

// normTime padroniza instantes em UTC com precisão de microssegundo
// (a do PostgreSQL), para que criação e reidratação produzam o mesmo valor.
func normTime(t time.Time) time.Time { return t.UTC().Truncate(time.Microsecond) }
