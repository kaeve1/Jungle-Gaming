package application

import (
	"context"
	"errors"
	"jungle-gaming-backend/internal/domain"
	"time"
)

// ReferenceSession claims work through transaction locks, wallet first.
// No claim survives rollback; financial effects and scheduling share the commit.
type ReferenceSession interface {
	Session
	ClaimReference(context.Context) (*domain.Wallet, *domain.WagerTransaction, Trace, error)
	UpdateReference(context.Context, *domain.WagerTransaction) error
	RecordReferenceFailure(context.Context, domain.ID, domain.ID, time.Duration) (domain.Status, error)
}

// ResumeReference processes at most one due operation. true means committed work.
func (s *Service) ResumeReference(ctx context.Context) (done bool, finalErr error) {
	start := time.Now()
	var observation Observation
	defer func() {
		if observation.TransactionID != "" || finalErr != nil {
			observation.Source = "reference"
			observation.Error = finalErr
			observation.Duration = time.Since(start)
			s.observe(ctx, observation)
		}
	}()
	worked := false
	var failedWallet, failedTX domain.ID
	err := s.uow.Within(ctx, func(ctx context.Context, session Session) error {
		worked = false
		observation = Observation{}
		failedWallet, failedTX = domain.ID{}, domain.ID{}
		db, ok := session.(ReferenceSession)
		if !ok {
			return domain.ErrInvariant
		}
		w, tx, trace, err := db.ClaimReference(ctx)
		if w != nil && tx != nil {
			failedWallet, failedTX = w.ID(), tx.ID()
			observation = Observation{TransactionID: tx.ID().String(), WalletID: w.ID().String(), ProviderID: tx.External().ProviderID, CorrelationID: trace.CorrelationID, MessageID: trace.MessageID}
		}
		if err != nil || tx == nil {
			return err
		}
		ref, err := db.FindReference(ctx, tx.External().ProviderID, tx.External().ReferenceExternalTransactionID)
		if err != nil {
			return err
		}
		now, err := db.Now(ctx)
		if err != nil {
			return err
		}
		version := w.Version()
		out, err := domain.Process(tx, w, ref, s.policy, s.ids, now)
		if err != nil {
			return err
		}
		if err = db.UpdateReference(ctx, tx); err != nil {
			return err
		}
		if out.Entry != nil {
			if err = db.InsertLedger(ctx, *out.Entry); err != nil {
				return err
			}
			if err = db.UpdateWallet(ctx, w, version); err != nil {
				return err
			}
		}
		if err = s.events(ctx, db, out.Events, trace); err != nil {
			return err
		}
		worked = true
		observation.Status = string(tx.Status())
		return nil
	})
	if err != nil && !failedTX.IsZero() && permanentReferenceError(err) {
		var auditedStatus domain.Status
		auditErr := s.uow.Within(ctx, func(ctx context.Context, session Session) error {
			auditedStatus = ""
			db, ok := session.(ReferenceSession)
			if !ok {
				return domain.ErrInvariant
			}
			var auditWriteErr error
			auditedStatus, auditWriteErr = db.RecordReferenceFailure(ctx, failedWallet, failedTX, s.policy.BaseDelay)
			return auditWriteErr
		})
		if auditErr != nil {
			return false, errors.Join(err, auditErr)
		}
		// Only a confirmed audit commit may report the terminal FAILED outcome.
		// Preserve the original attempt error for diagnostics and the worker.
		if auditedStatus == domain.StatusFailed {
			observation.Status = string(auditedStatus)
		}
	}
	return worked && err == nil, err
}

// Unknown errors are not enough evidence to permanently fail financial work.
func permanentReferenceError(err error) bool {
	if errors.Is(err, ErrUnavailable) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var classified interface{ Permanent() bool }
	return errors.Is(err, domain.ErrInvariant) || errors.Is(err, domain.ErrInvalidTransition) || (errors.As(err, &classified) && classified.Permanent())
}
