//go:build integration

package integration

import (
	"context"
	"fmt"
	appRuntime "jungle-gaming-backend/internal/adapters/runtime"
	"jungle-gaming-backend/internal/application"
	"jungle-gaming-backend/internal/domain"
	"sync"
	"testing"
	"time"
)

func refRequest(t *testing.T, w domain.WalletSnapshot, ext, kind, amount, ref string) domain.ExternalRequest {
	t.Helper()
	r, err := domain.NewExternalRequest(domain.RawExternalRequest{ProviderID: "provider-a", ExternalTransactionID: ext, IdempotencyKey: ext, PlayerID: w.PlayerID.String(), WalletID: w.ID.String(), RoundID: "round-1", GameID: "game-1", Kind: kind, Money: stage5Money(t, amount, "BRL"), ReferenceExternalTransactionID: ref})
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func shortReferences(t *testing.T, f *financialFixture, ttl time.Duration) {
	t.Helper()
	var err error
	f.service, err = application.NewService(f.uow, appRuntime.IDs{}, domain.ReferencePolicy{TTL: ttl, BaseDelay: 10 * time.Millisecond, MaxDelay: 20 * time.Millisecond}, nil)
	if err != nil {
		t.Fatal(err)
	}
}
func resumeUntil(t *testing.T, f financialFixture, id domain.ID, want domain.Status) domain.TransactionSnapshot {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := f.service.ResumeReference(t.Context()); err != nil {
			t.Fatal(err)
		}
		// Replay is the public result and remains provider scoped.
		var status string
		if err := f.pool.QueryRow(t.Context(), `SELECT status FROM wallet.wager_transactions WHERE id=$1`, id.String()).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status == string(want) {
			return domain.TransactionSnapshot{Status: want}
		}
		time.Sleep(12 * time.Millisecond)
	}
	t.Fatalf("operation %s never reached %s", id, want)
	return domain.TransactionSnapshot{}
}
func TestStage7DurableResolutionAndCombinedReversals(t *testing.T) {
	f := financialCore(t)
	shortReferences(t, &f, time.Second)
	w := stage5Wallet(t, f, "100.00")
	req := refRequest(t, w, "refund", "REFUND", "25.00", "bet")
	pending := processStage5(t, f, req)
	if pending.Transaction.Status != domain.StatusPendingReference {
		t.Fatal("expected pending")
	}
	time.Sleep(15 * time.Millisecond)
	worked, err := f.service.ResumeReference(t.Context())
	if err != nil || !worked {
		t.Fatalf("retry: %v %v", worked, err)
	}
	var attempts int
	var next, expires time.Time
	if err = f.pool.QueryRow(t.Context(), `SELECT reference_attempts,next_reference_attempt_at,reference_expires_at FROM wallet.wager_transactions WHERE id=$1`, pending.Transaction.ID.String()).Scan(&attempts, &next, &expires); err != nil || attempts != 1 || next.After(expires) {
		t.Fatalf("backoff %d %v", attempts, err)
	}
	processStage5(t, f, stage5Request(t, w, "provider-a", "bet", "bet", "BET", "25.00"))
	// Recreate all database/application resources while the operation is pending.
	f.pool.Close()
	f.pool, f.uow, f.service = financialPool(t, f.db, f.options)
	resumeUntil(t, f, pending.Transaction.ID, domain.StatusProcessed)
	replay := processStage5(t, f, req)
	if !replay.IdempotentReplay || replay.Transaction.ResultBalance.Amount() != "100.00" {
		t.Fatal("resolution replay")
	}
	processStage5(t, f, refRequest(t, w, "undo-refund", "ROLLBACK", "25.00", "refund"))
	denied := processStage5(t, f, refRequest(t, w, "second-refund", "REFUND", "25.00", "bet"))
	if denied.Transaction.FailureCode != domain.CodeReferenceAlreadyReversed {
		t.Fatal("combined reversal")
	}
	stage5HistoryWithoutTrace(t, f, w, 7500, 4, 5, 4, 10)
	var linked int
	err = f.pool.QueryRow(t.Context(), `SELECT count(*) FROM wallet.outbox_events final JOIN wallet.outbox_events pending ON final.payload->>'causationId'=pending.event_id::text WHERE final.aggregate_id=$1 AND pending.event_type='WagerTransactionPendingReference'`, pending.Transaction.ID.String()).Scan(&linked)
	if err != nil || linked != 1 {
		t.Fatalf("causation %d %v", linked, err)
	}
}
func stage5HistoryWithoutTrace(t *testing.T, f financialFixture, w domain.WalletSnapshot, balance, version int64, txs, ledger, events int) {
	t.Helper()
	var b, v, sum int64
	var nt, nl, ne int
	err := f.pool.QueryRow(t.Context(), `SELECT balance_amount,version,(SELECT count(*) FROM wallet.wager_transactions WHERE wallet_id=$1),(SELECT count(*) FROM wallet.ledger_entries WHERE wallet_id=$1),(SELECT count(*) FROM wallet.outbox_events WHERE partition_key=$1), (SELECT sum(CASE direction WHEN 'CREDIT' THEN amount ELSE -amount END)::bigint FROM wallet.ledger_entries WHERE wallet_id=$1) FROM wallet.wallets WHERE id=$1`, w.ID.String()).Scan(&b, &v, &nt, &nl, &ne, &sum)
	if err != nil || b != balance || sum != balance || v != version || nt != txs || nl != ledger || ne != events {
		t.Fatalf("history %d/%d/%d/%d/%d/%d err %v", b, v, sum, nt, nl, ne, err)
	}
}
func TestStage7ExpiryTerminalInvalidAndInsufficient(t *testing.T) {
	f := financialCore(t)
	shortReferences(t, &f, 40*time.Millisecond)
	w := stage5Wallet(t, f, "10.00")
	missing := refRequest(t, w, "missing", "REFUND", "1.00", "absent")
	p := processStage5(t, f, missing)
	resumeUntil(t, f, p.Transaction.ID, domain.StatusRejected)
	if got := processStage5(t, f, missing); got.Transaction.FailureCode != domain.CodeReferenceNotFound {
		t.Fatal("expiry code")
	}
	// Final lookup wins over expiry if the reference has arrived during downtime.
	late := refRequest(t, w, "late", "REFUND", "1.00", "late-bet")
	p = processStage5(t, f, late)
	time.Sleep(50 * time.Millisecond)
	processStage5(t, f, stage5Request(t, w, "provider-a", "late-bet", "late-bet", "BET", "1.00"))
	resumeUntil(t, f, p.Transaction.ID, domain.StatusProcessed)
	for _, tc := range []struct {
		ext, kind, amount string
		code              domain.FailureCode
	}{
		{"rejected-bet", "BET", "100.00", domain.CodeReferenceNotProcessed},
		{"wrong-kind", "WIN", "1.00", domain.CodeReferenceKindNotAllowed},
		{"wrong-amount", "BET", "2.00", domain.CodeReferenceAmountMismatch},
	} {
		req := refRequest(t, w, "ref-"+tc.ext, "REFUND", "1.00", tc.ext)
		p = processStage5(t, f, req)
		processStage5(t, f, stage5Request(t, w, "provider-a", tc.ext, tc.ext, tc.kind, tc.amount))
		resumeUntil(t, f, p.Transaction.ID, domain.StatusRejected)
		if got := processStage5(t, f, req); got.Transaction.FailureCode != tc.code {
			t.Fatalf("%s: %s", tc.ext, got.Transaction.FailureCode)
		}
	}
	win := processStage5(t, f, stage5Request(t, w, "provider-a", "big-win", "big-win", "WIN", "20.00"))
	_ = win
	processStage5(t, f, stage5Request(t, w, "provider-a", "spend", "spend", "BET", "29.00"))
	rejected := processStage5(t, f, refRequest(t, w, "undo-win", "ROLLBACK", "20.00", "big-win"))
	if rejected.Transaction.FailureCode != domain.CodeReversalInsufficientFunds {
		t.Fatalf("insufficient: %s", rejected.Transaction.FailureCode)
	}
}
func TestStage7WorkersCompeteAndSkipLockedWallet(t *testing.T) {
	f := financialCore(t)
	shortReferences(t, &f, time.Second)
	w := stage5Wallet(t, f, "100.00")
	for i := 0; i < 12; i++ {
		processStage5(t, f, refRequest(t, w, fmt.Sprintf("refund-%d", i), "REFUND", "10.00", "bet"))
	}
	processStage5(t, f, stage5Request(t, w, "provider-a", "bet", "bet", "BET", "10.00"))
	time.Sleep(15 * time.Millisecond)
	blocker, err := f.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = blocker.Exec(t.Context(), `SELECT 1 FROM wallet.wallets WHERE id=$1 FOR UPDATE`, w.ID.String()); err != nil {
		t.Fatal(err)
	}
	worked, err := f.service.ResumeReference(t.Context())
	if err != nil || worked {
		t.Fatalf("busy wallet must skip %v %v", worked, err)
	}
	if err = blocker.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	_, _, other := financialPool(t, f.db, f.options)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Go(func() {
			s := f.service
			if i%2 == 0 {
				s = other
			}
			for j := 0; j < 100; j++ {
				time.Sleep(time.Millisecond)
				if _, err := s.ResumeReference(context.Background()); err != nil {
					errs <- err
					return
				}
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	var processed, rejected int
	err = f.pool.QueryRow(t.Context(), `SELECT count(*) FILTER(WHERE status='PROCESSED'),count(*) FILTER(WHERE status='REJECTED') FROM wallet.wager_transactions WHERE kind='REFUND'`).Scan(&processed, &rejected)
	if err != nil || processed != 1 || rejected != 11 {
		t.Fatalf("competition %d/%d %v", processed, rejected, err)
	}
	stage5HistoryWithoutTrace(t, f, w, 10000, 3, 14, 3, 29)
}

type referenceFailureUOW struct {
	base  application.UnitOfWork
	after int
}

func (f referenceFailureUOW) Within(ctx context.Context, fn func(context.Context, application.Session) error) error {
	return f.base.Within(ctx, func(ctx context.Context, db application.Session) error {
		rs := db.(application.ReferenceSession)
		wrapped := &referenceFailureSession{failedSession: failedSession{Session: db, after: f.after}, reference: rs}
		err := fn(ctx, wrapped)
		if err == nil && f.after == 0 {
			return fmt.Errorf("test failure before recovery commit")
		}
		return err
	})
}

type referenceFailureSession struct {
	failedSession
	reference application.ReferenceSession
}

func (f *referenceFailureSession) ClaimReference(ctx context.Context) (*domain.Wallet, *domain.WagerTransaction, application.Trace, error) {
	return f.reference.ClaimReference(ctx)
}
func (f *referenceFailureSession) UpdateReference(ctx context.Context, tx *domain.WagerTransaction) error {
	return f.fail(f.reference.UpdateReference(ctx, tx))
}
func TestStage7RecoveryRollbackAtEveryWrite(t *testing.T) {
	for after := 0; after <= 5; after++ {
		t.Run(fmt.Sprint(after), func(t *testing.T) {
			f := financialCore(t)
			shortReferences(t, &f, time.Second)
			w := stage5Wallet(t, f, "100.00")
			req := refRequest(t, w, "refund", "REFUND", "10.00", "bet")
			p := processStage5(t, f, req)
			processStage5(t, f, stage5Request(t, w, "provider-a", "bet", "bet", "BET", "10.00"))
			time.Sleep(15 * time.Millisecond)
			broken, err := application.NewService(referenceFailureUOW{f.uow, after}, appRuntime.IDs{}, domain.DefaultReferencePolicy, nil)
			if err != nil {
				t.Fatal(err)
			}
			if worked, err := broken.ResumeReference(t.Context()); err == nil || worked {
				t.Fatal("partial work accepted")
			}
			stage5HistoryWithoutTrace(t, f, w, 9000, 2, 3, 2, 5)
			replay := processStage5(t, f, req)
			if replay.Transaction.Status != domain.StatusPendingReference || replay.Transaction.ReferenceAttempts != 0 {
				t.Fatal("rollback changed pending")
			}
			resumeUntil(t, f, p.Transaction.ID, domain.StatusProcessed)
			stage5HistoryWithoutTrace(t, f, w, 10000, 3, 3, 3, 7)
		})
	}
}
func TestStage7PendingChain(t *testing.T) {
	f := financialCore(t)
	shortReferences(t, &f, 100*time.Millisecond)
	w := stage5Wallet(t, f, "100.00")
	undo := refRequest(t, w, "undo", "ROLLBACK", "10.00", "refund")
	p := processStage5(t, f, undo)
	refund := refRequest(t, w, "refund", "REFUND", "10.00", "bet")
	r := processStage5(t, f, refund)
	time.Sleep(15 * time.Millisecond)
	if _, err := f.service.ResumeReference(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := processStage5(t, f, undo); got.Transaction.Status != domain.StatusPendingReference {
		t.Fatal("pending reference must wait")
	}
	processStage5(t, f, stage5Request(t, w, "provider-a", "bet", "bet", "BET", "10.00"))
	resumeUntil(t, f, r.Transaction.ID, domain.StatusProcessed)
	resumeUntil(t, f, p.Transaction.ID, domain.StatusProcessed)
	stage5HistoryWithoutTrace(t, f, w, 9000, 4, 4, 4, 10)
}

func TestStage7AbandonedDatabaseSession(t *testing.T) {
	f := financialCore(t)
	shortReferences(t, &f, time.Second)
	w := stage5Wallet(t, f, "100.00")
	req := refRequest(t, w, "refund", "REFUND", "10.00", "bet")
	p := processStage5(t, f, req)
	processStage5(t, f, stage5Request(t, w, "provider-a", "bet", "bet", "BET", "10.00"))
	time.Sleep(15 * time.Millisecond)
	entered := make(chan error, 1)
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- f.uow.Within(t.Context(), func(ctx context.Context, db application.Session) error {
			_, tx, _, err := db.(application.ReferenceSession).ClaimReference(ctx)
			if err == nil && tx == nil {
				err = fmt.Errorf("no claim")
			}
			entered <- err
			<-release
			if err != nil {
				return err
			}
			_, err = db.Now(ctx)
			return err
		})
	}()
	if err := <-entered; err != nil {
		close(release)
		<-done
		t.Fatal(err)
	}
	admin := connect(t, "postgres", "TEST_PG_SUPERUSER_PASSWORD", f.db)
	var killed int
	err := admin.QueryRow(t.Context(), `SELECT count(*) FROM (SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname=$1 AND usename='wallet_app' AND state='idle in transaction') terminated`, f.db).Scan(&killed)
	close(release)
	abandonedErr := <-done
	if err != nil || killed != 1 || abandonedErr == nil {
		t.Fatalf("termination %d %v / %v", killed, err, abandonedErr)
	}
	// A second independent pool recovers the persisted pending operation.
	_, _, f.service = financialPool(t, f.db, f.options)
	resumeUntil(t, f, p.Transaction.ID, domain.StatusProcessed)
	stage5HistoryWithoutTrace(t, f, w, 10000, 3, 3, 3, 7)
}

func TestStage7DirectRollbackDirections(t *testing.T) {
	f := financialCore(t)
	w := stage5Wallet(t, f, "100.00")
	processStage5(t, f, stage5Request(t, w, "provider-a", "bet", "bet", "BET", "10.00"))
	processStage5(t, f, refRequest(t, w, "undo-bet", "ROLLBACK", "10.00", "bet"))
	processStage5(t, f, stage5Request(t, w, "provider-a", "win", "win", "WIN", "20.00"))
	processStage5(t, f, refRequest(t, w, "undo-win", "ROLLBACK", "20.00", "win"))
	stage5HistoryWithoutTrace(t, f, w, 10000, 5, 5, 5, 10)
}

func (f *referenceFailureSession) RecordReferenceFailure(ctx context.Context, wallet, id domain.ID, delay time.Duration) (domain.Status, error) {
	return f.reference.RecordReferenceFailure(ctx, wallet, id, delay)
}

func TestStage7PermanentFailureIsDurableAndTerminal(t *testing.T) {
	f := financialCore(t)
	shortReferences(t, &f, time.Second)
	w := stage5Wallet(t, f, "100.00")
	req := refRequest(t, w, "refund", "REFUND", "10.00", "bet")
	processStage5(t, f, req)
	processStage5(t, f, stage5Request(t, w, "provider-a", "bet", "bet", "BET", "10.00"))
	admin := connect(t, "postgres", "TEST_PG_SUPERUSER_PASSWORD", f.db)
	_, err := admin.Exec(t.Context(), `CREATE FUNCTION wallet.test_permanent_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test permanent failure' USING ERRCODE='23000',CONSTRAINT='test_reference_failure'; END $$; CREATE TRIGGER test_reference_failure BEFORE INSERT ON wallet.ledger_entries FOR EACH ROW EXECUTE FUNCTION wallet.test_permanent_failure()`)
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= 3; attempt++ {
		time.Sleep(15 * time.Millisecond)
		worked, err := f.service.ResumeReference(t.Context())
		if err == nil || worked {
			t.Fatal("expected permanent SQL failure")
		}
		var count int
		var status string
		if err = f.pool.QueryRow(t.Context(), `SELECT reference_failure_attempts,status FROM wallet.wager_transactions WHERE external_transaction_id='refund'`).Scan(&count, &status); err != nil || count != attempt {
			t.Fatalf("audit %d %v", count, err)
		}
		if attempt < 3 && status != "PENDING_REFERENCE" {
			t.Fatal("premature failure")
		}
		if attempt == 1 {
			f.pool.Close()
			f.pool, f.uow, f.service = financialPool(t, f.db, f.options)
			shortReferences(t, &f, time.Second)
		}
	}
	failed := processStage5(t, f, req)
	if failed.Transaction.Status != domain.StatusFailed || failed.Transaction.FailureCode != domain.CodeInternalProcessingFailed || failed.Transaction.ResultBalance != nil {
		t.Fatal("failed replay")
	}
	stage5HistoryWithoutTrace(t, f, w, 9000, 2, 3, 2, 5)
	if worked, err := f.service.ResumeReference(t.Context()); err != nil || worked {
		t.Fatal("terminal work reclaimed")
	}
}

type unavailableReferenceUOW struct{ base application.UnitOfWork }
type unavailableReferenceSession struct{ application.ReferenceSession }

func (f unavailableReferenceSession) FindReference(context.Context, string, string) (domain.ReferenceLookup, error) {
	return domain.ReferenceLookup{}, application.ErrUnavailable
}
func (f unavailableReferenceUOW) Within(ctx context.Context, fn func(context.Context, application.Session) error) error {
	return f.base.Within(ctx, func(ctx context.Context, db application.Session) error {
		return fn(ctx, unavailableReferenceSession{db.(application.ReferenceSession)})
	})
}
func TestStage7TransientFailureNeverFinalizes(t *testing.T) {
	f := financialCore(t)
	shortReferences(t, &f, 30*time.Millisecond)
	w := stage5Wallet(t, f, "100.00")
	req := refRequest(t, w, "refund", "REFUND", "10.00", "bet")
	p := processStage5(t, f, req)
	unavailable, err := application.NewService(unavailableReferenceUOW{f.uow}, appRuntime.IDs{}, domain.DefaultReferencePolicy, nil)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(40 * time.Millisecond)
	for i := 0; i < 4; i++ {
		if worked, err := unavailable.ResumeReference(t.Context()); err == nil || worked {
			t.Fatal("transient accepted")
		}
	}
	var count int
	if err = f.pool.QueryRow(t.Context(), `SELECT reference_failure_attempts FROM wallet.wager_transactions WHERE id=$1`, p.Transaction.ID.String()).Scan(&count); err != nil || count != 0 {
		t.Fatalf("transient counted %d %v", count, err)
	}
	if got := processStage5(t, f, req); got.Transaction.Status != domain.StatusPendingReference {
		t.Fatal("transient finalized")
	}
	processStage5(t, f, stage5Request(t, w, "provider-a", "bet", "bet", "BET", "10.00"))
	resumeUntil(t, f, p.Transaction.ID, domain.StatusProcessed)
}

type claimFailureUOW struct{ base application.UnitOfWork }
type claimFailureSession struct{ application.ReferenceSession }

func (f claimFailureSession) ClaimReference(ctx context.Context) (*domain.Wallet, *domain.WagerTransaction, application.Trace, error) {
	w, tx, trace, err := f.ReferenceSession.ClaimReference(ctx)
	if err == nil && tx != nil {
		err = domain.ErrInvariant
	}
	return w, tx, trace, err
}
func (f claimFailureUOW) Within(ctx context.Context, fn func(context.Context, application.Session) error) error {
	return f.base.Within(ctx, func(ctx context.Context, db application.Session) error {
		return fn(ctx, claimFailureSession{db.(application.ReferenceSession)})
	})
}
func TestStage7KnownClaimFailureIsAudited(t *testing.T) {
	f := financialCore(t)
	shortReferences(t, &f, time.Second)
	w := stage5Wallet(t, f, "100.00")
	req := refRequest(t, w, "refund", "REFUND", "10.00", "bet")
	processStage5(t, f, req)
	broken, err := application.NewService(claimFailureUOW{f.uow}, appRuntime.IDs{}, domain.ReferencePolicy{TTL: time.Second, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		time.Sleep(15 * time.Millisecond)
		if worked, err := broken.ResumeReference(t.Context()); err == nil || worked {
			t.Fatal("expected claim failure")
		}
	}
	if got := processStage5(t, f, req); got.Transaction.Status != domain.StatusFailed {
		t.Fatal("known claim failure abandoned")
	}
	stage5HistoryWithoutTrace(t, f, w, 10000, 1, 2, 1, 3)
}
