package application

import (
	"context"
	"errors"
	"jungle-gaming-backend/internal/domain"
	"regexp"
	"time"
)

var ErrMessageConflict = errors.New("message identity reused with different content")
var inboxName = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)
var inboxHash = regexp.MustCompile(`^[0-9a-f]{64}$`)

type InboxSession interface {
	Session
	FindInbox(context.Context, string, string) (string, error)
	InsertInbox(context.Context, string, string, string, domain.ID) error
}

// ConsumeWager confirms the inbox and financial outcome in the SAME transaction.
// Non-locking inbox read first; financial wallet lock before inbox insertion.
// An index race rolls back ALL effects and retries the winner's durable result.
func (s *Service) ConsumeWager(ctx context.Context, consumer, messageID, hash string, req domain.ExternalRequest) (err error) {
	start := time.Now()
	var result Result
	defer func() {
		s.observe(ctx, Observation{Source: "sqs", Status: string(result.Transaction.Status), CorrelationID: messageID, MessageID: messageID, TransactionID: result.Transaction.ID.String(), WalletID: req.WalletID().String(), ProviderID: req.ProviderID(), Replay: result.IdempotentReplay, Error: err, Duration: time.Since(start)})
	}()
	if !inboxName.MatchString(consumer) || !traceID.MatchString(messageID) || !inboxHash.MatchString(hash) || req.ProviderID() == "" || req.WalletID().IsZero() {
		return domain.ErrValidation
	}
	return s.uow.Within(ctx, func(ctx context.Context, session Session) error {
		result = Result{}
		db, ok := session.(InboxSession)
		if !ok {
			return domain.ErrInvariant
		}
		stored, err := db.FindInbox(ctx, consumer, messageID)
		if err != nil {
			return err
		}
		if stored != "" {
			if stored != hash {
				return ErrMessageConflict
			}
			tx, e := db.FindDuplicate(ctx, req)
			if e != nil {
				return e
			}
			if tx == nil {
				return domain.ErrInvariant
			}
			result = Result{Transaction: tx.Snapshot(), IdempotentReplay: true}
			return nil
		}
		result, err = s.processWagerSession(ctx, db, req, Trace{CorrelationID: messageID, CausationID: messageID, MessageID: messageID})
		if err != nil {
			return err
		}
		// FAILED is a durable terminal outcome as well; replay must not retry money.
		return db.InsertInbox(ctx, consumer, messageID, hash, result.Transaction.ID)
	})
}
