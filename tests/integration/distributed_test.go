//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"jungle-gaming-backend/internal/adapters/postgres"
	"jungle-gaming-backend/internal/domain"
	"strings"
	"sync"
	"testing"
	"time"
)

const stage11Roles = "api,reference-worker,consumer,outbox-publisher"

func stage11Wallet(t *testing.T, f financialFixture, m map[string]any) domain.WalletSnapshot {
	t.Helper()
	id, e := domain.ParseID(m["id"].(string))
	if e != nil {
		t.Fatal(e)
	}
	w, e := postgres.NewReader(f.pool, f.options.OperationTimeout).Wallet(t.Context(), id)
	if e != nil {
		t.Fatal(e)
	}
	return w
}
func stage11PendingBody(w map[string]any, ext, ref, kind, amount string) string {
	return strings.TrimSuffix(wagerBody(w, "provider-a", ext, amount, kind), "}") + fmt.Sprintf(`,"referenceExternalTransactionId":%q}`, ref)
}
func stage11Status(t *testing.T, f financialFixture, ext, status string) {
	t.Helper()
	stage11Wait(t, 15*time.Second, func() bool {
		var s string
		_ = f.pool.QueryRow(t.Context(), `SELECT status FROM wallet.wager_transactions WHERE provider_id='provider-a' AND external_transaction_id=$1`, ext).Scan(&s)
		return s == status
	})
}
func stage11Published(t *testing.T, f financialFixture) {
	t.Helper()
	stage11Wait(t, 45*time.Second, func() bool {
		var n int
		_ = f.pool.QueryRow(t.Context(), `SELECT count(*) FROM wallet.outbox_events WHERE published_at IS NULL`).Scan(&n)
		return n == 0
	})
}

func TestStage11ThreeProcessesConcurrencyRestartAndTransports(t *testing.T) {
	f := financialCore(t)
	input := stage8Queue(t)
	output := stage9Queue(t)
	var ps []*stage11Process
	for range 3 {
		ps = append(ps, stage11Start(t, f.db, input.name, output, stage11Roles, "", ""))
	}
	if ps[0].cmd.Process.Pid == ps[1].cmd.Process.Pid || ps[1].cmd.Process.Pid == ps[2].cmd.Process.Pid {
		t.Fatal("shared process")
	}
	internal := accessToken(t, "wallet-internal", "KC_SECRET_WALLET_INTERNAL")
	a := accessToken(t, "provider-a", "KC_SECRET_PROVIDER_A")
	wm := apiOpen(t, ps[0].api, internal, "100.00")
	w := stage11Wallet(t, f, wm)
	body := wagerBody(wm, "provider-a", "fifty11", "20.00", "BET")
	var wg sync.WaitGroup
	var mu sync.Mutex
	fresh, replays := 0, 0
	for n := range 50 {
		wg.Go(func() {
			status, r, e := stage11Call(t, ps[n%3], a, "fifty11", body, fmt.Sprintf("stage11-%d", n))
			if e != nil {
				t.Error(e)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if status == 201 {
				fresh++
			} else if status == 200 && r["idempotentReplay"] == true {
				replays++
			} else {
				t.Errorf("unexpected duplicate status %d", status)
			}
		})
	}
	wg.Wait()
	if fresh != 1 || replays != 49 {
		t.Fatalf("fresh=%d replays=%d", fresh, replays)
	}
	for n := range 3 {
		stage11AssertLog(t, ps[n], fmt.Sprintf("stage11-%d", n))
	}
	stage5HistoryWithoutTrace(t, f, w, 8000, 2, 2, 2, 4)
	// Same financial command through SQS, with distinct broker dedup IDs.
	req := stage5Request(t, w, "provider-a", "fifty11", "fifty11", "BET", "20.00")
	for range 5 {
		stage8Send(t, stage8Body(t, req, "cross11"), w.ID.String())
	}
	stage11Wait(t, 15*time.Second, func() bool {
		var n int
		_ = f.pool.QueryRow(t.Context(), `SELECT count(*) FROM wallet.inbox_messages WHERE message_id='cross11'`).Scan(&n)
		return n == 1
	})
	status, _, e := stage11Call(t, ps[1], a, "other-key", body, "alternative11")
	if e != nil || status != 409 {
		t.Fatal("alternate key accepted", status, e)
	}
	status, _, e = stage11Call(t, ps[2], a, "fifty11", wagerBody(wm, "provider-a", "fifty11", "21.00", "BET"), "conflict11")
	if e != nil || status != 409 {
		t.Fatal("conflicting body accepted", status, e)
	}
	contested := apiOpen(t, ps[1].api, internal, "100.00")
	cw := stage11Wallet(t, f, contested)
	statuses := make(chan int, 2)
	for n := range 2 {
		wg.Go(func() {
			ext := fmt.Sprintf("contest11-%d", n)
			s, _, e := stage11Call(t, ps[n], a, ext, wagerBody(contested, "provider-a", ext, "80.00", "BET"), ext)
			if e != nil {
				t.Error(e)
			}
			statuses <- s
		})
	}
	wg.Wait()
	s1, s2 := <-statuses, <-statuses
	if !((s1 == 201 && s2 == 422) || (s1 == 422 && s2 == 201)) {
		t.Fatal("bad contest statuses", s1, s2)
	}
	stage5HistoryWithoutTrace(t, f, cw, 2000, 2, 3, 2, 5)
	// A fresh operation races between HTTP and SQS across independent processes.
	xwm := apiOpen(t, ps[0].api, internal, "100.00")
	xw := stage11Wallet(t, f, xwm)
	xr := stage5Request(t, xw, "provider-a", "race11", "race11", "BET", "20.00")
	start := make(chan struct{})
	cross := make(chan int, 1)
	wg.Go(func() {
		<-start
		s, _, e := stage11Call(t, ps[2], a, "race11", wagerBody(xwm, "provider-a", "race11", "20.00", "BET"), "race11")
		if e != nil {
			t.Error(e)
		}
		cross <- s
	})
	wg.Go(func() {
		<-start
		for range 5 {
			stage8Send(t, stage8Body(t, xr, "race-message11"), xw.ID.String())
		}
	})
	close(start)
	wg.Wait()
	sCross := <-cross
	if sCross != 200 && sCross != 201 {
		t.Fatal("HTTP/SQS race failed", sCross)
	}
	stage11Wait(t, 15*time.Second, func() bool {
		var n int
		_ = f.pool.QueryRow(t.Context(), `SELECT count(*) FROM wallet.inbox_messages WHERE message_id='race-message11'`).Scan(&n)
		return n == 1
	})
	stage5HistoryWithoutTrace(t, f, xw, 8000, 2, 2, 2, 4)
	sqReject := stage5Request(t, cw, "provider-a", "sqreject11", "sqreject11", "BET", "80.00")
	stage8Send(t, stage8Body(t, sqReject, "sqreject11"), cw.ID.String())
	stage11Status(t, f, "sqreject11", "REJECTED")
	// An actual SQL lock stalls wallet A; wallet B advances on another process.
	other := apiOpen(t, ps[2].api, internal, "100.00")
	ow := stage11Wallet(t, f, other)
	lock, e := f.pool.Begin(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	defer lock.Rollback(context.Background())
	if _, e = lock.Exec(t.Context(), `SELECT id FROM wallet.wallets WHERE id=$1 FOR UPDATE`, w.ID.String()); e != nil {
		t.Fatal(e)
	}
	blocked := make(chan int, 1)
	go func() {
		s, _, e := stage11Call(t, ps[0], a, "blocked11", wagerBody(wm, "provider-a", "blocked11", "1.00", "WIN"), "blocked11")
		if e != nil {
			t.Error(e)
		}
		blocked <- s
	}()
	stage11Wait(t, time.Second, func() bool {
		var n int
		_ = f.pool.QueryRow(t.Context(), `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock'`).Scan(&n)
		return n > 0
	})
	s, _, e := stage11Call(t, ps[2], a, "parallel11", wagerBody(other, "provider-a", "parallel11", "1.00", "WIN"), "parallel11")
	if e != nil || s != 201 {
		t.Fatal("other wallet stalled", e)
	}
	select {
	case <-blocked:
		t.Fatal("wallet lock bypassed")
	default:
	}
	if e = lock.Rollback(t.Context()); e != nil {
		t.Fatal(e)
	}
	if <-blocked != 201 {
		t.Fatal("blocked wallet did not resume")
	}
	// Pending survives termination of every Fx process, then resolves on replacements.
	apiCall(t, ps[0].api, "POST", "/wagering/transactions", a, "refund11", stage11PendingBody(wm, "refund11", "future11", "REFUND", "5.00"), 202)
	stage11Published(t, f)
	for _, p := range ps {
		p.stop(t, false)
	}
	ps = nil
	for range 3 {
		ps = append(ps, stage11Start(t, f.db, input.name, output, stage11Roles, "", ""))
	}
	replay := apiCall(t, ps[2].api, "POST", "/wagering/transactions", a, "fifty11", body, 200)
	if replay["balance"].(map[string]any)["amount"] != "80.00" {
		t.Fatal("original replay balance lost on restart")
	}
	apiCall(t, ps[1].api, "POST", "/wagering/transactions", a, "future11", wagerBody(wm, "provider-a", "future11", "5.00", "BET"), 201)
	stage11Status(t, f, "refund11", "PROCESSED")
	// Duplicate reversal with a distinct external ID cannot credit again.
	apiCall(t, ps[0].api, "POST", "/wagering/transactions", a, "refund11-again", stage11PendingBody(wm, "refund11-again", "future11", "REFUND", "5.00"), 422)
	// Three distinct reversal IDs compete for the same successful debit.
	rwm := apiOpen(t, ps[0].api, internal, "100.00")
	rw := stage11Wallet(t, f, rwm)
	apiCall(t, ps[0].api, "POST", "/wagering/transactions", a, "reversal-bet11", wagerBody(rwm, "provider-a", "reversal-bet11", "10.00", "BET"), 201)
	reversals := make(chan int, 3)
	for n := range 3 {
		wg.Go(func() {
			ext := fmt.Sprintf("refund-race11-%d", n)
			s, _, e := stage11Call(t, ps[n], a, ext, stage11PendingBody(rwm, ext, "reversal-bet11", "REFUND", "10.00"), ext)
			if e != nil {
				t.Error(e)
			}
			reversals <- s
		})
	}
	wg.Wait()
	success, rejected := 0, 0
	for range 3 {
		switch <-reversals {
		case 201:
			success++
		case 422:
			rejected++
		}
	}
	if success != 1 || rejected != 2 {
		t.Fatal("concurrent reversal effects duplicated")
	}
	stage5HistoryWithoutTrace(t, f, rw, 10000, 3, 5, 3, 8)
	// Malformed input reaches the actual DLQ with all three consumers alive.
	stage8Send(t, "{}", w.ID.String())
	admin := sqsClientWithKeys(t, env(t, "TEST_SQS_ADMIN_ACCESS_KEY_ID"), env(t, "TEST_SQS_ADMIN_SECRET_ACCESS_KEY"))
	dlq := queueURL(t, admin, input.dlq)
	// A canceled Receive with an unknown receipt recovers by the real 30s visibility expiry.
	stage11Wait(t, 45*time.Second, func() bool {
		out, e := admin.ReceiveMessage(t.Context(), &sqs.ReceiveMessageInput{QueueUrl: aws.String(dlq)})
		if e != nil {
			return false
		}
		for _, m := range out.Messages {
			if aws.ToString(m.Body) == "{}" {
				_, e = admin.DeleteMessage(t.Context(), &sqs.DeleteMessageInput{QueueUrl: aws.String(dlq), ReceiptHandle: m.ReceiptHandle})
				return e == nil
			}
		}
		return false
	})
	stage5HistoryWithoutTrace(t, f, w, 8100, 5, 6, 5, 12)
	stage5HistoryWithoutTrace(t, f, cw, 2000, 2, 4, 2, 6)
	stage5HistoryWithoutTrace(t, f, ow, 10100, 2, 2, 2, 4)
	stage11Published(t, f)
	// Capture every unique committed event at the real output destination.
	reader := sqsClient(t, "events-reader")
	qurl := queueURL(t, reader, output)
	captured := map[string]bool{}
	stage11Wait(t, 20*time.Second, func() bool {
		out, e := reader.ReceiveMessage(t.Context(), &sqs.ReceiveMessageInput{QueueUrl: aws.String(qurl), MaxNumberOfMessages: 10})
		if e != nil {
			t.Error(e)
			return false
		}
		for _, m := range out.Messages {
			var envelope struct {
				ID string `json:"eventId"`
			}
			if e = json.Unmarshal([]byte(aws.ToString(m.Body)), &envelope); e != nil {
				t.Error(e)
			}
			var payload string
			if e = f.pool.QueryRow(t.Context(), `SELECT payload::text FROM wallet.outbox_events WHERE event_id=$1`, envelope.ID).Scan(&payload); e != nil || payload != aws.ToString(m.Body) {
				t.Error("event changed/lost")
			}
			captured[envelope.ID] = true
			_, _ = reader.DeleteMessage(t.Context(), &sqs.DeleteMessageInput{QueueUrl: aws.String(qurl), ReceiptHandle: m.ReceiptHandle})
		}
		return len(captured) == 34
	})
}

func TestStage11HTTPKillBeforeAndAfterCommit(t *testing.T) {
	f := financialCore(t)
	input := stage8Queue(t)
	output := stage9Queue(t)
	p := stage11Start(t, f.db, input.name, output, "api", "", "")
	other := stage11Start(t, f.db, input.name, output, "api", "", "")
	internal := accessToken(t, "wallet-internal", "KC_SECRET_WALLET_INTERNAL")
	a := accessToken(t, "provider-a", "KC_SECRET_PROVIDER_A")
	wm := apiOpen(t, p.api, internal, "100.00")
	w := stage11Wallet(t, f, wm)
	// Only this scratch DB gets a barrier trigger. Financial writes already exist
	// inside the transaction when INSERT outbox blocks on our advisory lock.
	admin := connect(t, "postgres", "TEST_PG_SUPERUSER_PASSWORD", f.db)
	_, e := admin.Exec(t.Context(), `CREATE FUNCTION wallet.stage11_commit_barrier() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.payload->>'correlationId'='before11' THEN PERFORM pg_advisory_xact_lock(911); END IF; RETURN NEW; END $$;
 CREATE TRIGGER stage11_commit_barrier AFTER INSERT ON wallet.outbox_events FOR EACH ROW EXECUTE FUNCTION wallet.stage11_commit_barrier()`)
	if e != nil {
		t.Fatal(e)
	}
	lock, e := f.pool.Acquire(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	defer lock.Release()
	_, e = lock.Exec(t.Context(), `SELECT pg_advisory_lock(911)`)
	if e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() {
		_, _, e := stage11Call(t, p, a, "before11", wagerBody(wm, "provider-a", "before11", "20.00", "BET"), "before11")
		done <- e
	}()
	stage11Wait(t, time.Second, func() bool {
		var n int
		_ = f.pool.QueryRow(t.Context(), `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event='advisory'`).Scan(&n)
		return n > 0
	})
	p.stop(t, true)
	if <-done == nil {
		t.Fatal("precommit request unexpectedly returned")
	}
	_, _ = lock.Exec(t.Context(), `SELECT pg_advisory_unlock(911)`)
	_, _ = admin.Exec(t.Context(), `DROP TRIGGER stage11_commit_barrier ON wallet.outbox_events; DROP FUNCTION wallet.stage11_commit_barrier()`)
	stage5HistoryWithoutTrace(t, f, w, 10000, 1, 1, 1, 2)
	apiCall(t, other.api, "POST", "/wagering/transactions", a, "before11", wagerBody(wm, "provider-a", "before11", "20.00", "BET"), 201)
	after := stage11Start(t, f.db, input.name, output, "api", "http-after", "")
	go func() {
		_, _, e := stage11Call(t, after, a, "after11", wagerBody(wm, "provider-a", "after11", "10.00", "BET"), "after11")
		done <- e
	}()
	after.barrier(t)
	stage5HistoryWithoutTrace(t, f, w, 7000, 3, 3, 3, 6)
	after.stop(t, true)
	if <-done == nil {
		t.Fatal("postcommit response unexpectedly delivered")
	}
	r := apiCall(t, other.api, "POST", "/wagering/transactions", a, "after11", wagerBody(wm, "provider-a", "after11", "10.00", "BET"), 200)
	if r["idempotentReplay"] != true {
		t.Fatal("postcommit replay lost")
	}
	stage5HistoryWithoutTrace(t, f, w, 7000, 3, 3, 3, 6)
}
