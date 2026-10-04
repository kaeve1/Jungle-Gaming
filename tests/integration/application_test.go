//go:build integration

package integration

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"jungle-gaming-backend/internal/adapters/postgres"
	appRuntime "jungle-gaming-backend/internal/adapters/runtime"
	"jungle-gaming-backend/internal/application"
	"jungle-gaming-backend/internal/domain"
	"jungle-gaming-backend/internal/domain/money"
)

type financialFixture struct {
	service *application.Service
	uow     *postgres.UnitOfWork
	pool    *pgxpool.Pool
	db      string
	options postgres.Options
}

var stage5Trace = application.Trace{CorrelationID: "stage5-test", CausationID: "trusted-test"}

func financialCore(t *testing.T) financialFixture {
	t.Helper()
	db := scratchDatabase(t, true)
	options := postgres.DefaultOptions()
	options.MaxConns = 12
	pool, uow, service := financialPool(t, db, options)
	return financialFixture{service, uow, pool, db, options}
}

func financialPool(t *testing.T, db string, options postgres.Options) (*pgxpool.Pool, *postgres.UnitOfWork, *application.Service) {
	t.Helper()
	dsn := fmt.Sprintf("postgres://wallet_app:%s@%s:5432/%s?sslmode=disable", url.QueryEscape(env(t, "TEST_PG_APP_PASSWORD")), env(t, "TEST_PG_HOST"), db)
	pool, err := postgres.OpenPool(t.Context(), dsn, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	uow, err := postgres.NewUnitOfWork(pool, options)
	if err != nil {
		t.Fatal(err)
	}
	service, err := application.NewService(uow, appRuntime.IDs{}, domain.DefaultReferencePolicy, nil)
	if err != nil {
		t.Fatal(err)
	}
	return pool, uow, service
}

func stage5Money(t *testing.T, amount, currency string) money.Money {
	t.Helper()
	m, err := money.Parse(amount, currency)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func stage5Wallet(t *testing.T, f financialFixture, amount string) domain.WalletSnapshot {
	t.Helper()
	w, err := f.service.OpenWallet(t.Context(), appRuntime.IDs{}.NewID(), stage5Money(t, amount, "BRL"), stage5Trace)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func stage5Request(t *testing.T, w domain.WalletSnapshot, provider, ext, key, kind, amount string) domain.ExternalRequest {
	t.Helper()
	r, err := domain.NewExternalRequest(domain.RawExternalRequest{ProviderID: provider, ExternalTransactionID: ext, IdempotencyKey: key,
		PlayerID: w.PlayerID.String(), WalletID: w.ID.String(), RoundID: "round-1", GameID: "game-1", Kind: kind, Money: stage5Money(t, amount, "BRL")})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func processStage5(t *testing.T, f financialFixture, req domain.ExternalRequest) application.Result {
	t.Helper()
	r, err := f.service.ProcessWager(t.Context(), req, stage5Trace)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// Observe independent SQL facts rather than accepting just the returned object.
func stage5History(t *testing.T, f financialFixture, w domain.WalletSnapshot, balance, version int64, txs, ledger, events int) {
	t.Helper()
	var actualBalance, actualVersion, ledgerBalance int64
	var actualTX, actualLedger, actualEvents int
	err := f.pool.QueryRow(t.Context(), `SELECT balance_amount,version,
		(SELECT count(*) FROM wallet.wager_transactions WHERE wallet_id=$1),
		(SELECT count(*) FROM wallet.ledger_entries WHERE wallet_id=$1),
		(SELECT count(*) FROM wallet.outbox_events WHERE partition_key=$1),
		COALESCE((SELECT sum(CASE direction WHEN 'CREDIT' THEN amount ELSE -amount END) FROM wallet.ledger_entries WHERE wallet_id=$1),0)::bigint
		FROM wallet.wallets WHERE id=$1`, w.ID.String()).Scan(&actualBalance, &actualVersion, &actualTX, &actualLedger, &actualEvents, &ledgerBalance)
	if err != nil {
		t.Fatal(err)
	}
	if actualBalance != balance || ledgerBalance != balance || actualVersion != version || actualTX != txs || actualLedger != ledger || actualEvents != events {
		t.Fatalf("history: balance=%d ledgerBalance=%d version=%d tx=%d ledger=%d events=%d; want %d/%d/%d/%d/%d/%d", actualBalance, ledgerBalance, actualVersion, actualTX, actualLedger, actualEvents, balance, balance, version, txs, ledger, events)
	}
	var invalid int
	err = f.pool.QueryRow(t.Context(), `SELECT count(*) FROM wallet.outbox_events
		WHERE partition_key=$1 AND (published_at IS NOT NULL OR payload->>'eventId' <> event_id::text OR
		payload->>'correlationId' <> 'stage5-test' OR payload->>'eventType' <> event_type OR
		(payload->>'version')::integer <> event_version)`, w.ID.String()).Scan(&invalid)
	if err != nil || invalid != 0 {
		t.Fatalf("outbox snapshots invalid=%d err=%v", invalid, err)
	}
}

func TestStage5OpeningAndBusinessResults(t *testing.T) {
	f := financialCore(t)
	w := stage5Wallet(t, f, "100.00")
	stage5History(t, f, w, 10000, 1, 1, 1, 2)
	if _, err := f.service.OpenWallet(t.Context(), w.PlayerID, stage5Money(t, "0.00", "BRL"), stage5Trace); !errors.Is(err, application.ErrWalletConflict) {
		t.Fatalf("duplicate opening: %v", err)
	}
	zero := stage5Wallet(t, f, "0.00")
	stage5History(t, f, zero, 0, 1, 0, 0, 0)
	processStage5(t, f, stage5Request(t, zero, "provider-a", "zero-win", "zero-win", "WIN", "1.00"))
	stage5History(t, f, zero, 100, 2, 1, 1, 2)

	bet := stage5Request(t, w, "provider-a", "bet", "bet-key", "BET", "25.00")
	first := processStage5(t, f, bet)
	if first.IdempotentReplay || first.Transaction.Status != domain.StatusProcessed || first.Transaction.ResultBalance.Amount() != "75.00" {
		t.Fatalf("bet: %+v", first)
	}
	processStage5(t, f, stage5Request(t, w, "provider-a", "win", "win-key", "WIN", "10.00"))
	loss := processStage5(t, f, stage5Request(t, w, "provider-a", "loss", "loss-key", "LOSS", "0.00"))
	if loss.Transaction.ResultBalance.Amount() != "85.00" {
		t.Fatal("LOSS result must capture current locked balance")
	}
	stage5History(t, f, w, 8500, 3, 4, 3, 7)
	replay := processStage5(t, f, bet)
	if !replay.IdempotentReplay || replay.Transaction.ID != first.Transaction.ID || replay.Transaction.ResultBalance.Amount() != "75.00" {
		t.Fatal("replay must retain original result, not current balance")
	}
	if _, err := f.service.ProcessWager(t.Context(), stage5Request(t, w, "provider-a", "bet", "bet-key", "BET", "26.00"), stage5Trace); !errors.Is(err, application.ErrIdempotencyKeyReused) {
		t.Fatalf("key reuse: %v", err)
	}
	if _, err := f.service.ProcessWager(t.Context(), stage5Request(t, w, "provider-a", "bet", "other-key", "BET", "25.00"), stage5Trace); !errors.Is(err, application.ErrExternalTransactionConflict) {
		t.Fatalf("external conflict: %v", err)
	}
	// Identical external/key values under another trusted provider are independent.
	processStage5(t, f, stage5Request(t, w, "provider-b", "bet", "bet-key", "BET", "1.00"))
	rejectReq := stage5Request(t, w, "provider-a", "reject", "reject", "BET", "100.00")
	rejected := processStage5(t, f, rejectReq)
	if rejected.Transaction.Status != domain.StatusRejected || rejected.Transaction.FailureCode != domain.CodeInsufficientFunds || rejected.Transaction.ResultBalance.Amount() != "84.00" {
		t.Fatalf("rejection: %+v", rejected)
	}
	processStage5(t, f, stage5Request(t, w, "provider-a", "after-reject", "after-reject", "WIN", "2.00"))
	rejectedReplay := processStage5(t, f, rejectReq)
	if !rejectedReplay.IdempotentReplay || rejectedReplay.Transaction.ResultBalance.Amount() != "84.00" {
		t.Fatal("rejection replay changed")
	}
	stage5History(t, f, w, 8600, 5, 7, 5, 12)
	// Recreate the complete pool and service: no replay state exists in memory.
	f.pool.Close()
	f.pool, f.uow, f.service = financialPool(t, f.db, f.options)
	restarted := processStage5(t, f, bet)
	if !restarted.IdempotentReplay || restarted.Transaction.ID != first.Transaction.ID || restarted.Transaction.ResultBalance.Amount() != "75.00" {
		t.Fatal("replay did not survive pool/service restart")
	}
	stage5History(t, f, w, 8600, 5, 7, 5, 12)
}

func TestStage5FiftyConcurrentDuplicates(t *testing.T) {
	f := financialCore(t)
	w := stage5Wallet(t, f, "100.00")
	req := stage5Request(t, w, "provider-a", "same-bet", "same-key", "BET", "25.00")
	type outcome struct {
		result application.Result
		err    error
	}
	results := make(chan outcome, 50)
	start := make(chan struct{})
	for i := 0; i < 50; i++ {
		go func() {
			<-start
			r, err := f.service.ProcessWager(t.Context(), req, stage5Trace)
			results <- outcome{r, err}
		}()
	}
	close(start)
	var original domain.ID
	replays := 0
	for i := 0; i < 50; i++ {
		out := <-results
		if out.err != nil {
			t.Fatal(out.err)
		}
		if original.IsZero() {
			original = out.result.Transaction.ID
		}
		if out.result.Transaction.ID != original || out.result.Transaction.ResultBalance.Amount() != "75.00" {
			t.Fatal("duplicate result differs")
		}
		if out.result.IdempotentReplay {
			replays++
		}
	}
	if replays != 49 {
		t.Fatalf("replays=%d", replays)
	}
	stage5History(t, f, w, 7500, 2, 2, 2, 4)
}

func TestStage5TwoConcurrentBets(t *testing.T) {
	f := financialCore(t)
	w := stage5Wallet(t, f, "100.00")
	reqs := []domain.ExternalRequest{stage5Request(t, w, "provider-a", "bet-1", "key-1", "BET", "80.00"), stage5Request(t, w, "provider-a", "bet-2", "key-2", "BET", "80.00")}
	results := make(chan application.Result, 2)
	errs := make(chan error, 2)
	start := make(chan struct{})
	for _, req := range reqs {
		go func() {
			<-start
			r, err := f.service.ProcessWager(t.Context(), req, stage5Trace)
			results <- r
			errs <- err
		}()
	}
	close(start)
	processed, rejected := 0, 0
	for i := 0; i < 2; i++ {
		r := <-results
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
		if r.Transaction.Status == domain.StatusProcessed {
			processed++
		}
		if r.Transaction.Status == domain.StatusRejected && r.Transaction.FailureCode == domain.CodeInsufficientFunds {
			rejected++
		}
	}
	if processed != 1 || rejected != 1 {
		t.Fatalf("processed=%d rejected=%d", processed, rejected)
	}
	stage5History(t, f, w, 2000, 2, 3, 2, 5)
}

// Fault adapters exist only in this test file. Each injected failure occurs
// AFTER an actual repository write, proving the shared transaction rolls back.
type failedUOW struct {
	base  application.UnitOfWork
	after int
}

func (f failedUOW) Within(ctx context.Context, fn func(context.Context, application.Session) error) error {
	return f.base.Within(ctx, func(ctx context.Context, db application.Session) error {
		s := &failedSession{Session: db, after: f.after}
		err := fn(ctx, s)
		if err == nil && f.after == 0 {
			return errors.New("test failure immediately before commit")
		}
		return err
	})
}

type failedSession struct {
	application.Session
	after, n int
}

func (f *failedSession) fail(err error) error {
	if err != nil {
		return err
	}
	f.n++
	if f.n == f.after {
		return errors.New("test failure after SQL write")
	}
	return nil
}
func (f *failedSession) InsertWallet(ctx context.Context, w *domain.Wallet) error {
	return f.fail(f.Session.InsertWallet(ctx, w))
}
func (f *failedSession) InsertTransaction(ctx context.Context, tx *domain.WagerTransaction, trace application.Trace) error {
	return f.fail(f.Session.InsertTransaction(ctx, tx, trace))
}
func (f *failedSession) InsertLedger(ctx context.Context, e domain.LedgerEntry) error {
	return f.fail(f.Session.InsertLedger(ctx, e))
}
func (f *failedSession) UpdateWallet(ctx context.Context, w *domain.Wallet, v int64) error {
	return f.fail(f.Session.UpdateWallet(ctx, w, v))
}
func (f *failedSession) InsertEvent(ctx context.Context, e domain.Envelope) error {
	return f.fail(f.Session.InsertEvent(ctx, e))
}

func TestStage5RollbackAtEveryWrite(t *testing.T) {
	f := financialCore(t)
	for after := 0; after <= 5; after++ {
		t.Run(fmt.Sprintf("after_%d", after), func(t *testing.T) {
			broken, err := application.NewService(failedUOW{f.uow, after}, appRuntime.IDs{}, domain.DefaultReferencePolicy, nil)
			if err != nil {
				t.Fatal(err)
			}
			player := appRuntime.IDs{}.NewID()
			if _, err = broken.OpenWallet(t.Context(), player, stage5Money(t, "100.00", "BRL"), stage5Trace); err == nil {
				t.Fatal("expected opening rollback")
			}
			var count int
			if err = f.pool.QueryRow(t.Context(), `SELECT count(*) FROM wallet.wallets WHERE player_id=$1`, player.String()).Scan(&count); err != nil || count != 0 {
				t.Fatalf("partial opening: %d %v", count, err)
			}
			w := stage5Wallet(t, f, "100.00")
			req := stage5Request(t, w, "provider-a", fmt.Sprintf("fault-bet-%d", after), fmt.Sprintf("fault-key-%d", after), "BET", "25.00")
			if _, err = broken.ProcessWager(t.Context(), req, stage5Trace); err == nil {
				t.Fatal("expected movement rollback")
			}
			stage5History(t, f, w, 10000, 1, 1, 1, 2)
			// The failed transaction did not consume the idempotency key.
			result := processStage5(t, f, req)
			if result.IdempotentReplay {
				t.Fatal("failed transaction persisted")
			}
			stage5History(t, f, w, 7500, 2, 2, 2, 4)
		})
	}
}

func TestStage5IndependentWalletsAndCancellation(t *testing.T) {
	f := financialCore(t)
	a, b := stage5Wallet(t, f, "100.00"), stage5Wallet(t, f, "100.00")
	blocker, err := f.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(context.Background())
	if _, err = blocker.Exec(t.Context(), `SELECT id FROM wallet.wallets WHERE id=$1 FOR UPDATE`, a.ID.String()); err != nil {
		t.Fatal(err)
	}
	blockedReq := stage5Request(t, a, "provider-a", "blocked", "blocked", "BET", "1.00")
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := f.service.ProcessWager(ctx, blockedReq, stage5Trace); done <- err }()
	otherReq := stage5Request(t, b, "provider-a", "other", "other", "BET", "1.00")
	otherCtx, otherCancel := context.WithTimeout(t.Context(), time.Second)
	defer otherCancel()
	if _, err = f.service.ProcessWager(otherCtx, otherReq, stage5Trace); err != nil {
		t.Fatalf("independent wallet blocked: %v", err)
	}
	if err = <-done; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancellation: %v", err)
	}
	if err = blocker.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	stage5History(t, f, a, 10000, 1, 1, 1, 2)
	processStage5(t, f, blockedReq)
	stage5History(t, f, a, 9900, 2, 2, 2, 4)
	stage5History(t, f, b, 9900, 2, 2, 2, 4)
}

func TestStage5ReplayDoesNotAcquireWalletLock(t *testing.T) {
	f := financialCore(t)
	w := stage5Wallet(t, f, "100.00")
	req := stage5Request(t, w, "provider-a", "replay-locked", "replay-locked", "BET", "1.00")
	original := processStage5(t, f, req)
	blocker, err := f.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(context.Background())
	if _, err = blocker.Exec(t.Context(), `SELECT id FROM wallet.wallets WHERE id=$1 FOR UPDATE`, w.ID.String()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	replayed, err := f.service.ProcessWager(ctx, req, stage5Trace)
	if err != nil || !replayed.IdempotentReplay || replayed.Transaction.ID != original.Transaction.ID {
		t.Fatalf("replay while wallet locked: %+v %v", replayed, err)
	}
	if err = blocker.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	stage5History(t, f, w, 9900, 2, 2, 2, 4)
}

func TestStage5CrossWalletIdentityCollision(t *testing.T) {
	f := financialCore(t)
	a, b := stage5Wallet(t, f, "100.00"), stage5Wallet(t, f, "100.00")
	reqs := []domain.ExternalRequest{stage5Request(t, a, "provider-a", "same-external", "same-key", "BET", "1.00"), stage5Request(t, b, "provider-a", "same-external", "same-key", "BET", "1.00")}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	start := make(chan struct{})
	for _, req := range reqs {
		wg.Go(func() { <-start; _, err := f.service.ProcessWager(t.Context(), req, stage5Trace); errs <- err })
	}
	close(start)
	wg.Wait()
	close(errs)
	success, conflicts := 0, 0
	for err := range errs {
		if err == nil {
			success++
		} else if errors.Is(err, application.ErrIdempotencyKeyReused) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflicts != 1 {
		t.Fatalf("success=%d conflicts=%d", success, conflicts)
	}
	var balanceSum, ledgerSum int64
	if err := f.pool.QueryRow(t.Context(), `SELECT (SELECT sum(balance_amount)::bigint FROM wallet.wallets),
		(SELECT sum(CASE direction WHEN 'CREDIT' THEN amount ELSE -amount END)::bigint FROM wallet.ledger_entries)`).Scan(&balanceSum, &ledgerSum); err != nil {
		t.Fatal(err)
	}
	if balanceSum != 19900 || ledgerSum != balanceSum {
		t.Fatalf("cross-wallet effects: %d/%d", balanceSum, ledgerSum)
	}
}
