package application

import (
	"context"
	"errors"
	"jungle-gaming-backend/internal/domain"
	"time"
)

type Observation struct {
	Source, Status, CorrelationID, MessageID, TransactionID, WalletID, ProviderID string
	Replay                                                                        bool
	Error                                                                         error
	Duration                                                                      time.Duration
}
type Diagnostics interface {
	Observe(context.Context, Observation)
	Count(string)
}

func (s *Service) observe(ctx context.Context, o Observation) {
	if s.diagnostics != nil {
		s.diagnostics.Observe(ctx, o)
	}
}
func ErrorCategory(err error) string {
	switch {
	case err == nil:
		return "none"
	case errors.Is(err, ErrIdempotencyKeyReused) || errors.Is(err, ErrExternalTransactionConflict) || errors.Is(err, ErrMessageConflict):
		return "conflict"
	case errors.Is(err, ErrUnavailable) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
		return "unavailable"
	case errors.Is(err, domain.ErrValidation) || errors.Is(err, domain.ErrUninitialized):
		return "validation"
	default:
		return "internal"
	}
}
