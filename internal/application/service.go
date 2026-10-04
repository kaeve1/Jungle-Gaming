package application

import (
	"context"
	"fmt"
	"regexp"
	"time"

	"jungle-gaming-backend/internal/domain"
	"jungle-gaming-backend/internal/domain/money"
)

var traceID = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

type Service struct {
	diagnostics Diagnostics
	uow         UnitOfWork
	ids         domain.IDGenerator
	policy      domain.ReferencePolicy
}

func NewService(uow UnitOfWork, ids domain.IDGenerator, policy domain.ReferencePolicy, diagnostics Diagnostics) (*Service, error) {
	if uow == nil || ids == nil {
		return nil, domain.ErrUninitialized
	}
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	return &Service{uow: uow, ids: ids, policy: policy, diagnostics: diagnostics}, nil
}

func validateTrace(t Trace) error {
	if !traceID.MatchString(t.CorrelationID) || (t.CausationID != "" && !traceID.MatchString(t.CausationID)) {
		return fmt.Errorf("%w: invalid correlationId or causationId", domain.ErrValidation)
	}
	return nil
}

// OpenWallet commits the wallet, positive OPENING, ledger and events together.
// A zero opening commits only the wallet. Duplicate player/currency is conflict.
func (s *Service) OpenWallet(ctx context.Context, player domain.ID, initial money.Money, trace Trace) (result domain.WalletSnapshot, err error) {
	start := time.Now()
	var transactionID string
	defer func() {
		status := "PROCESSED"
		if err != nil {
			status = "ERROR"
		}
		s.observe(ctx, Observation{Source: "opening", Status: status, CorrelationID: trace.CorrelationID, TransactionID: transactionID, WalletID: result.ID.String(), Error: err, Duration: time.Since(start)})
	}()
	if err := validateTrace(trace); err != nil {
		return domain.WalletSnapshot{}, err
	}
	err = s.uow.Within(ctx, func(ctx context.Context, db Session) error {
		transactionID = ""
		now, err := db.Now(ctx)
		if err != nil {
			return err
		}
		opened, err := domain.OpenWallet(s.ids, player, initial, now)
		if err != nil {
			return err
		}
		if err = db.InsertWallet(ctx, opened.Wallet); err != nil {
			return err
		}
		if opened.Opening != nil {
			transactionID = opened.Opening.ID().String()
			if err = db.InsertTransaction(ctx, opened.Opening, trace); err != nil {
				return err
			}
			if err = db.InsertLedger(ctx, *opened.Entry); err != nil {
				return err
			}
		}
		if err = s.events(ctx, db, opened.Events, trace); err != nil {
			return err
		}
		result = opened.Wallet.Snapshot()
		return nil
	})
	if err != nil {
		return domain.WalletSnapshot{}, err
	}
	return result, nil
}

// ProcessWager assumes provider identity was established by a trusted adapter.
// Every duplicate lookup is scoped by that provider, including the fast replay.
// Only a successful commit exposes a result to its caller.
func (s *Service) ProcessWager(ctx context.Context, req domain.ExternalRequest, trace Trace) (result Result, err error) {
	start := time.Now()
	defer func() {
		s.observe(ctx, Observation{Source: "http", Status: string(result.Transaction.Status), CorrelationID: trace.CorrelationID, TransactionID: result.Transaction.ID.String(), WalletID: req.WalletID().String(), ProviderID: req.ProviderID(), Replay: result.IdempotentReplay, Error: err, Duration: time.Since(start)})
	}()
	if req.ProviderID() == "" || req.WalletID().IsZero() {
		return Result{}, domain.ErrUninitialized
	}
	if err := validateTrace(trace); err != nil {
		return Result{}, err
	}
	err = s.uow.Within(ctx, func(ctx context.Context, db Session) error {
		var attemptErr error
		result, attemptErr = s.processWagerSession(ctx, db, req, trace)
		return attemptErr
	})
	if err != nil {
		return Result{}, err
	}
	return result, nil
}

func duplicate(tx *domain.WagerTransaction, req domain.ExternalRequest) (Result, error) {
	kind, err := domain.ClassifyDuplicate(tx, req)
	if err != nil {
		return Result{}, err
	}
	switch kind {
	case domain.Replay:
		return Result{tx.Snapshot(), true}, nil
	case domain.KeyReused:
		return Result{}, ErrIdempotencyKeyReused
	default:
		return Result{}, ErrExternalTransactionConflict
	}
}

func (s *Service) events(ctx context.Context, db Session, events []domain.Event, trace Trace) error {
	for _, event := range events {
		envelope, err := domain.NewEnvelope(s.ids.NewID(), event, trace.CorrelationID, trace.CausationID)
		if err != nil {
			return err
		}
		if err = db.InsertEvent(ctx, envelope); err != nil {
			return err
		}
	}
	return nil
}

// Shared by HTTP and inbox processing; the caller owns the SQL commit.
func (s *Service) processWagerSession(ctx context.Context, db Session, req domain.ExternalRequest, trace Trace) (Result, error) {
	existing, err := db.FindDuplicate(ctx, req)
	if err != nil {
		return Result{}, err
	}
	if existing != nil {
		return duplicate(existing, req)
	}
	w, err := db.LockWallet(ctx, req.WalletID())
	if err != nil {
		return Result{}, err
	}
	// A waiter sees the winner's commit under READ COMMITTED. Avoid computing
	// or inserting a second movement; cross-wallet races remain index-protected.
	existing, err = db.FindDuplicate(ctx, req)
	if err != nil {
		return Result{}, err
	}
	if existing != nil {
		return duplicate(existing, req)
	}
	ref := domain.ReferenceLookup{}
	if req.ReferenceExternalTransactionID() != "" {
		ref, err = db.FindReference(ctx, req.ProviderID(), req.ReferenceExternalTransactionID())
		if err != nil {
			return Result{}, err
		}
	}
	now, err := db.Now(ctx)
	if err != nil {
		return Result{}, err
	}
	tx, err := domain.NewExternalTransaction(s.ids.NewID(), req, now)
	if err != nil {
		return Result{}, err
	}
	oldVersion := w.Version()
	out, err := domain.Process(tx, w, ref, s.policy, s.ids, now)
	if err != nil {
		return Result{}, err
	}
	if err = db.InsertTransaction(ctx, tx, trace); err != nil {
		return Result{}, err
	}
	if out.Entry != nil {
		if err = db.InsertLedger(ctx, *out.Entry); err != nil {
			return Result{}, err
		}
		if err = db.UpdateWallet(ctx, w, oldVersion); err != nil {
			return Result{}, err
		}
	}
	if err = s.events(ctx, db, out.Events, trace); err != nil {
		return Result{}, err
	}
	return Result{Transaction: tx.Snapshot()}, nil
}
