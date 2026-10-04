package httpapi

import (
	"context"
	"errors"
	"jungle-gaming-backend/internal/application"
	"jungle-gaming-backend/internal/domain"
	"jungle-gaming-backend/internal/domain/money"
	"net/http"
)

func (h *Handler) inputError(w http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		fail(w, 413, "VALIDATION_ERROR", "body exceeds 64 KiB")
		return
	}
	code := "VALIDATION_ERROR"
	var validation *domain.ValidationError
	if errors.As(err, &validation) {
		code = string(validation.Code)
	} else if errors.Is(err, money.ErrUnsupportedCurrency) {
		code = "UNSUPPORTED_CURRENCY"
	} else if errors.Is(err, money.ErrInvalidAmount) || errors.Is(err, money.ErrInvalidScale) || errors.Is(err, money.ErrNegativeAmount) || errors.Is(err, money.ErrOverflow) {
		code = "INVALID_AMOUNT"
	}
	fail(w, 400, code, "invalid request fields or JSON")
}
func (h *Handler) error(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, application.ErrWalletNotFound):
		fail(w, 404, "WALLET_NOT_FOUND", "wallet not found")
	case errors.Is(err, application.ErrTransactionNotFound):
		fail(w, 404, "TRANSACTION_NOT_FOUND", "transaction not found")
	case errors.Is(err, application.ErrInvalidCursor):
		fail(w, 400, "INVALID_CURSOR", "invalid ledger cursor")
	case errors.Is(err, application.ErrIdempotencyKeyReused):
		fail(w, 409, "IDEMPOTENCY_KEY_REUSED", "key already used with different content")
	case errors.Is(err, application.ErrExternalTransactionConflict):
		fail(w, 409, "EXTERNAL_TRANSACTION_CONFLICT", "operation already registered with another key")
	case errors.Is(err, application.ErrUnavailable) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled):
		w.Header().Set("Retry-After", "1")
		fail(w, 503, "SERVICE_UNAVAILABLE", "dependency temporarily unavailable; retry with the same key")
	case errors.Is(err, domain.ErrValidation) || errors.Is(err, domain.ErrUninitialized):
		h.inputError(w, err)
	default:
		h.log.ErrorContext(r.Context(), "HTTP operation failed", "correlationId", w.Header().Get("X-Correlation-Id"))
		fail(w, 500, "INTERNAL_ERROR", "unexpected error")
	}
}
