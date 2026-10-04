// Package application coordinates domain decisions through infrastructure ports.
package application

import (
	"context"
	"errors"
	"time"

	"jungle-gaming-backend/internal/domain"
)

var (
	ErrWalletNotFound              = errors.New("wallet not found")
	ErrWalletConflict              = errors.New("wallet already exists for player and currency")
	ErrIdempotencyKeyReused        = errors.New("idempotency key reused")
	ErrExternalTransactionConflict = errors.New("external transaction conflict")
	ErrUnavailable                 = errors.New("database temporarily unavailable; retry with the same key")
	// ErrConcurrentDuplicate requests rollback and a fresh lookup of the winner.
	ErrConcurrentDuplicate = errors.New("concurrent duplicate")
)

// Trace is supplied by the trusted transport adapter; it is not authentication.
type Trace struct {
	CorrelationID, CausationID string
	// MessageID remains the original input message while CausationID may become
	// the pending-reference event that caused the worker's resolution.
	MessageID string
}

// Session repositories all share one SQL transaction. No method commits alone.
type Session interface {
	// Now is the database wall clock sampled inside the current transaction.
	Now(context.Context) (time.Time, error)
	FindDuplicate(context.Context, domain.ExternalRequest) (*domain.WagerTransaction, error)
	LockWallet(context.Context, domain.ID) (*domain.Wallet, error)
	FindReference(context.Context, string, string) (domain.ReferenceLookup, error)
	InsertWallet(context.Context, *domain.Wallet) error
	InsertTransaction(context.Context, *domain.WagerTransaction, Trace) error
	InsertLedger(context.Context, domain.LedgerEntry) error
	UpdateWallet(context.Context, *domain.Wallet, int64) error
	InsertEvent(context.Context, domain.Envelope) error
}

type UnitOfWork interface {
	// The callback may run again only after a known aborted transaction.
	// It must not publish events or perform any external side effect.
	Within(context.Context, func(context.Context, Session) error) error
}

type Result struct {
	Transaction      domain.TransactionSnapshot
	IdempotentReplay bool
}
