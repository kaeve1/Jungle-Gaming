//go:build integration

package integration

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"jungle-gaming-backend/internal/adapters/postgres"
	appRuntime "jungle-gaming-backend/internal/adapters/runtime"
	"jungle-gaming-backend/internal/application"
	"jungle-gaming-backend/internal/domain"
)

func TestStage5MoneyLimitsAndPrivateRejections(t *testing.T) {
	f := financialCore(t)
	w := stage5Wallet(t, f, "92233720368547758.07")
	stage5History(t, f, w, 9223372036854775807, 1, 1, 1, 2)
	limit := processStage5(t, f, stage5Request(t, w, "provider-a", "overflow", "overflow", "WIN", "0.01"))
	if limit.Transaction.Status != domain.StatusRejected || limit.Transaction.FailureCode != domain.CodeBalanceLimitExceeded || limit.Transaction.ResultBalance.Amount() != "92233720368547758.07" {
		t.Fatalf("overflow: %+v", limit)
	}
	processStage5(t, f, stage5Request(t, w, "provider-a", "limit-bet", "limit-bet", "BET", "0.01"))
	stage5History(t, f, w, 9223372036854775806, 2, 3, 2, 5)

	otherPlayer := w
	otherPlayer.PlayerID = appRuntime.IDs{}.NewID()
	private := processStage5(t, f, stage5Request(t, otherPlayer, "provider-a", "wrong-player", "wrong-player", "BET", "1.00"))
	if private.Transaction.FailureCode != domain.CodeWalletPlayerMismatch || private.Transaction.ResultBalance != nil {
		t.Fatal("wallet mismatch leaked balance")
	}
	privateReplay := processStage5(t, f, stage5Request(t, otherPlayer, "provider-a", "wrong-player", "wrong-player", "BET", "1.00"))
	if !privateReplay.IdempotentReplay || privateReplay.Transaction.ResultBalance != nil {
		t.Fatal("private replay leaked balance")
	}
	currencyReq, err := domain.NewExternalRequest(domain.RawExternalRequest{ProviderID: "provider-a", ExternalTransactionID: "wrong-currency", IdempotencyKey: "wrong-currency",
		WalletID: w.ID.String(), PlayerID: w.PlayerID.String(), RoundID: "round-1", GameID: "game-1", Kind: "BET", Money: stage5Money(t, "1.00", "USD")})
	if err != nil {
		t.Fatal(err)
	}
	mismatch := processStage5(t, f, currencyReq)
	if mismatch.Transaction.FailureCode != domain.CodeCurrencyMismatch || mismatch.Transaction.ResultBalance.Currency().String() != "BRL" {
		t.Fatal("currency rejection result corrupted")
	}
	if !processStage5(t, f, currencyReq).IdempotentReplay {
		t.Fatal("currency rejection not rehydrated")
	}
	missing := w
	missing.ID = appRuntime.IDs{}.NewID()
	if _, err = f.service.ProcessWager(t.Context(), stage5Request(t, missing, "provider-a", "missing", "missing", "BET", "1.00"), stage5Trace); !errors.Is(err, application.ErrWalletNotFound) {
		t.Fatalf("missing wallet: %v", err)
	}
	stage5History(t, f, w, 9223372036854775806, 2, 5, 2, 7)
}

func TestStage5InitialReferenceIsDurable(t *testing.T) {
	f := financialCore(t)
	w := stage5Wallet(t, f, "100.00")
	request := func(ext, key, kind, ref string) domain.ExternalRequest {
		r, err := domain.NewExternalRequest(domain.RawExternalRequest{ProviderID: "provider-a", ExternalTransactionID: ext, IdempotencyKey: key,
			WalletID: w.ID.String(), PlayerID: w.PlayerID.String(), RoundID: "round-1", GameID: "game-1", Kind: kind, Money: stage5Money(t, "25.00", "BRL"), ReferenceExternalTransactionID: ref})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	waitingReq := request("early-win", "early-win", "WIN", "late-bet")
	waiting := processStage5(t, f, waitingReq)
	if waiting.Transaction.Status != domain.StatusPendingReference || waiting.Transaction.ReferenceExpiresAt.IsZero() || waiting.Transaction.NextReferenceAttemptAt.IsZero() {
		t.Fatal("reference schedule not persisted")
	}
	if !processStage5(t, f, waitingReq).IdempotentReplay {
		t.Fatal("pending replay did not rehydrate")
	}
	processStage5(t, f, stage5Request(t, w, "provider-a", "bet", "bet", "BET", "25.00"))
	win := processStage5(t, f, request("referenced-win", "referenced-win", "WIN", "bet"))
	if win.Transaction.Status != domain.StatusProcessed || win.Transaction.ReferenceTransactionID.IsZero() {
		t.Fatal("reference lookup failed")
	}
	stage5History(t, f, w, 10000, 3, 4, 3, 7)
	// No reference worker runs in stage 5; the early WIN remains scheduled.
}

type countedUOW struct {
	base     application.UnitOfWork
	attempts atomic.Int32
}

func (u *countedUOW) Within(ctx context.Context, fn func(context.Context, application.Session) error) error {
	return u.base.Within(ctx, func(ctx context.Context, s application.Session) error { u.attempts.Add(1); return fn(ctx, s) })
}

func TestStage5LockRetryIsBounded(t *testing.T) {
	f := financialCore(t)
	w := stage5Wallet(t, f, "100.00")
	options := f.options
	options.LockTimeout = 30 * time.Millisecond
	options.StatementTimeout = time.Second
	options.OperationTimeout = 2 * time.Second
	options.MaxRetries = 2
	uow, err := postgres.NewUnitOfWork(f.pool, options)
	if err != nil {
		t.Fatal(err)
	}
	counted := &countedUOW{base: uow}
	service, err := application.NewService(counted, appRuntime.IDs{}, domain.DefaultReferencePolicy, nil)
	if err != nil {
		t.Fatal(err)
	}
	blocker, err := f.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(context.Background())
	if _, err = blocker.Exec(t.Context(), `SELECT id FROM wallet.wallets WHERE id=$1 FOR UPDATE`, w.ID.String()); err != nil {
		t.Fatal(err)
	}
	_, err = service.ProcessWager(t.Context(), stage5Request(t, w, "provider-a", "lock-timeout", "lock-timeout", "BET", "1.00"), stage5Trace)
	if !errors.Is(err, application.ErrUnavailable) || counted.attempts.Load() != 3 {
		t.Fatalf("err=%v attempts=%d", err, counted.attempts.Load())
	}
	if err = blocker.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	stage5History(t, f, w, 10000, 1, 1, 1, 2)
}

func TestStage5RealDeadlockRecovery(t *testing.T) {
	f := financialCore(t)
	a, b := stage5Wallet(t, f, "0.00"), stage5Wallet(t, f, "0.00")
	arrived := make(chan struct{}, 2)
	proceed := make(chan struct{})
	errs := make(chan error, 2)
	var attempts atomic.Int32
	for _, pair := range [][2]domain.ID{{a.ID, b.ID}, {b.ID, a.ID}} {
		go func() {
			first := true
			errs <- f.uow.Within(t.Context(), func(ctx context.Context, s application.Session) error {
				attempts.Add(1)
				if _, err := s.LockWallet(ctx, pair[0]); err != nil {
					return err
				}
				if first {
					first = false
					arrived <- struct{}{}
					select {
					case <-proceed:
					case <-ctx.Done():
						return ctx.Err()
					}
				}
				_, err := s.LockWallet(ctx, pair[1])
				return err
			})
		}()
	}
	<-arrived
	<-arrived
	close(proceed)
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if attempts.Load() < 3 {
		t.Fatal("test did not exercise deadlock rollback/retry")
	}
	stage5History(t, f, a, 0, 1, 0, 0, 0)
	stage5History(t, f, b, 0, 1, 0, 0, 0)
}

func TestStage5CommitConstraintFailureRollsBack(t *testing.T) {
	f := financialCore(t)
	opened, err := domain.OpenWallet(appRuntime.IDs{}, appRuntime.IDs{}.NewID(), stage5Money(t, "1.00", "BRL"), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// Deliberately omit the opening history. The INSERT succeeds and a deferred
	// database constraint must abort COMMIT. Production cannot accept it as a rejection.
	err = f.uow.Within(t.Context(), func(ctx context.Context, s application.Session) error { return s.InsertWallet(ctx, opened.Wallet) })
	var dbErr *postgres.DatabaseError
	if !errors.As(err, &dbErr) || dbErr.SQLState != "23000" {
		t.Fatalf("commit integrity error: %v", err)
	}
	var count int
	if err = f.pool.QueryRow(t.Context(), `SELECT count(*) FROM wallet.wallets WHERE id=$1`, opened.Wallet.ID().String()).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed commit persisted wallet: %d %v", count, err)
	}
}

type gatedUOW struct {
	base    application.UnitOfWork
	arrived chan struct{}
	proceed chan struct{}
	writes  atomic.Int32
}

func (u *gatedUOW) Within(ctx context.Context, fn func(context.Context, application.Session) error) error {
	return u.base.Within(ctx, func(ctx context.Context, s application.Session) error { return fn(ctx, &gatedSession{s, u}) })
}

type gatedSession struct {
	application.Session
	gate *gatedUOW
}

func (s *gatedSession) InsertTransaction(ctx context.Context, tx *domain.WagerTransaction, trace application.Trace) error {
	if s.gate.writes.Add(1) <= 2 {
		s.gate.arrived <- struct{}{}
		select {
		case <-s.gate.proceed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return s.Session.InsertTransaction(ctx, tx, trace)
}

func TestStage5ExternalIDIndexRace(t *testing.T) {
	f := financialCore(t)
	a, b := stage5Wallet(t, f, "100.00"), stage5Wallet(t, f, "100.00")
	gate := &gatedUOW{base: f.uow, arrived: make(chan struct{}, 2), proceed: make(chan struct{})}
	service, err := application.NewService(gate, appRuntime.IDs{}, domain.DefaultReferencePolicy, nil)
	if err != nil {
		t.Fatal(err)
	}
	errCh := make(chan error, 2)
	for _, req := range []domain.ExternalRequest{stage5Request(t, a, "provider-a", "same-operation", "key-a", "BET", "1.00"), stage5Request(t, b, "provider-a", "same-operation", "key-b", "BET", "1.00")} {
		go func() { _, err := service.ProcessWager(t.Context(), req, stage5Trace); errCh <- err }()
	}
	// Both transactions have passed both duplicate lookups and locked different
	// wallets BEFORE either INSERT. Only the unique external-ID index can arbitrate.
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for i := 0; i < 2; i++ {
		select {
		case <-gate.arrived:
		case <-ctx.Done():
			close(gate.proceed)
			t.Fatal("insert barrier timed out")
		}
	}
	close(gate.proceed)
	success, conflicts := 0, 0
	for i := 0; i < 2; i++ {
		err := <-errCh
		if err == nil {
			success++
		} else if errors.Is(err, application.ErrExternalTransactionConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflicts != 1 || gate.writes.Load() != 2 {
		t.Fatalf("success=%d conflicts=%d attempted writes=%d", success, conflicts, gate.writes.Load())
	}
	var balance int64
	var txs, entries, events int
	err = f.pool.QueryRow(t.Context(), `SELECT (SELECT sum(balance_amount)::bigint FROM wallet.wallets),
		(SELECT count(*) FROM wallet.wager_transactions),(SELECT count(*) FROM wallet.ledger_entries),(SELECT count(*) FROM wallet.outbox_events)`).Scan(&balance, &txs, &entries, &events)
	if err != nil || balance != 19900 || txs != 3 || entries != 3 || events != 6 {
		t.Fatalf("partial loser history: balance=%d tx=%d ledger=%d events=%d err=%v", balance, txs, entries, events, err)
	}
}
