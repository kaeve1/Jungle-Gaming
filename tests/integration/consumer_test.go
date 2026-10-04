//go:build integration

package integration

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"io"
	appRuntime "jungle-gaming-backend/internal/adapters/runtime"
	"jungle-gaming-backend/internal/adapters/sqsconsumer"
	"jungle-gaming-backend/internal/application"
	"jungle-gaming-backend/internal/domain"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func stage8Body(t *testing.T, req domain.ExternalRequest, id string) string {
	t.Helper()
	var data map[string]json.RawMessage
	if err := json.Unmarshal(req.CanonicalJSON(), &data); err != nil {
		t.Fatal(err)
	}
	key, _ := json.Marshal(req.IdempotencyKey())
	data["idempotencyKey"] = key
	body, err := json.Marshal(map[string]any{"messageId": id, "type": "WagerTransactionRequested", "occurredAt": "2026-09-08T12:00:00Z", "data": data})
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}
func stage8Consume(t *testing.T, f financialFixture, req domain.ExternalRequest, id string) error {
	t.Helper()
	m, err := sqsconsumer.Decode(stage8Body(t, req, id), req.WalletID().String(), []string{"provider-a", "provider-b"})
	if err != nil {
		t.Fatal(err)
	}
	return f.service.ConsumeWager(t.Context(), "wager-consumer", m.ID, m.Hash, m.Request)
}
func stage8Send(t *testing.T, body, group string) {
	t.Helper()
	c := sqsClient(t, "wager-producer")
	out, err := c.SendMessage(t.Context(), &sqs.SendMessageInput{QueueUrl: aws.String(queueURL(t, c, stage8Queue(t).name)), MessageBody: aws.String(body), MessageGroupId: aws.String(group), MessageDeduplicationId: aws.String(rand.Text())})
	if err != nil || out.MessageId == nil {
		t.Fatalf("send: %v", err)
	}
}
func eventuallyStage8(t *testing.T, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("consumer condition timed out")
}
func TestStage8InboxFinancialAtomicityAndRaces(t *testing.T) {
	f := financialCore(t)
	w := stage5Wallet(t, f, "100.00")
	req := stage5Request(t, w, "provider-a", "bet", "key", "BET", "20.00")
	body := stage8Body(t, req, "same-message")
	m, _ := sqsconsumer.Decode(body, w.ID.String(), []string{"provider-a"})
	var wg sync.WaitGroup
	errs := make(chan error, 50)
	for range 50 {
		wg.Go(func() { errs <- f.service.ConsumeWager(t.Context(), "wager-consumer", m.ID, m.Hash, m.Request) })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	stage5HistoryWithoutTrace(t, f, w, 8000, 2, 2, 2, 4)
	var count int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM wallet.inbox_messages`).Scan(&count); err != nil || count != 1 {
		t.Fatal("one durable inbox record required")
	}
	conflicting := stage5Request(t, w, "provider-a", "other-bet", "other-key", "BET", "30.00")
	if err := stage8Consume(t, f, conflicting, "same-message"); !errors.Is(err, application.ErrMessageConflict) {
		t.Fatalf("inbox conflict: %v", err)
	}
	stage5HistoryWithoutTrace(t, f, w, 8000, 2, 2, 2, 4)
	var invalid int
	err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM wallet.outbox_events WHERE aggregate_id=$1 AND (payload->>'eventId'<>event_id::text OR payload->>'correlationId'<>'same-message' OR payload->>'causationId'<>'same-message')`, processStage5(t, f, req).Transaction.ID.String()).Scan(&invalid)
	if err != nil || invalid != 0 {
		t.Fatal("SQS event metadata inconsistent")
	}
	// A different envelope ID may repeat the financial command, without money again.
	if err := stage8Consume(t, f, req, "new-message"); err != nil {
		t.Fatal(err)
	}
	stage5HistoryWithoutTrace(t, f, w, 8000, 2, 2, 2, 4)
}

type inboxFailUOW struct {
	base   application.UnitOfWork
	target int
}
type inboxFailSession struct {
	application.InboxSession
	target, step int
}

func (f *inboxFailSession) after(err error) error {
	if err != nil {
		return err
	}
	f.step++
	if f.step == f.target {
		return errors.New("injected after durable write")
	}
	return nil
}
func (f *inboxFailSession) InsertTransaction(ctx context.Context, tx *domain.WagerTransaction, trace application.Trace) error {
	return f.after(f.InboxSession.InsertTransaction(ctx, tx, trace))
}
func (f *inboxFailSession) InsertLedger(ctx context.Context, e domain.LedgerEntry) error {
	return f.after(f.InboxSession.InsertLedger(ctx, e))
}
func (f *inboxFailSession) UpdateWallet(ctx context.Context, w *domain.Wallet, v int64) error {
	return f.after(f.InboxSession.UpdateWallet(ctx, w, v))
}
func (f *inboxFailSession) InsertEvent(ctx context.Context, e domain.Envelope) error {
	return f.after(f.InboxSession.InsertEvent(ctx, e))
}
func (f *inboxFailSession) InsertInbox(ctx context.Context, c, id, h string, tx domain.ID) error {
	return f.after(f.InboxSession.InsertInbox(ctx, c, id, h, tx))
}
func (f inboxFailUOW) Within(ctx context.Context, fn func(context.Context, application.Session) error) error {
	return f.base.Within(ctx, func(ctx context.Context, db application.Session) error {
		wrapped := &inboxFailSession{InboxSession: db.(application.InboxSession), target: f.target}
		if err := fn(ctx, wrapped); err != nil {
			return err
		}
		return wrapped.after(nil)
	})
}
func TestStage8InboxRollbackAtEveryWrite(t *testing.T) {
	for point := 1; point <= 7; point++ {
		t.Run(fmt.Sprint(point), func(t *testing.T) {
			f := financialCore(t)
			w := stage5Wallet(t, f, "100.00")
			req := stage5Request(t, w, "provider-a", "bet", "key", "BET", "20.00")
			broken, err := application.NewService(inboxFailUOW{f.uow, point}, appRuntime.IDs{}, domain.DefaultReferencePolicy, nil)
			if err != nil {
				t.Fatal(err)
			}
			m, _ := sqsconsumer.Decode(stage8Body(t, req, "atomic"), w.ID.String(), []string{"provider-a"})
			if err = broken.ConsumeWager(t.Context(), "wager-consumer", m.ID, m.Hash, m.Request); err == nil {
				t.Fatal("expected rollback")
			}
			stage5HistoryWithoutTrace(t, f, w, 10000, 1, 1, 1, 2)
			var count int
			_ = f.pool.QueryRow(t.Context(), `SELECT count(*) FROM wallet.inbox_messages`).Scan(&count)
			if count != 0 {
				t.Fatal("partial inbox commit")
			}
			if err = stage8Consume(t, f, req, "atomic"); err != nil {
				t.Fatal(err)
			}
			stage5HistoryWithoutTrace(t, f, w, 8000, 2, 2, 2, 4)
		})
	}
}
func TestStage8RealConsumerHTTPAndPendingReference(t *testing.T) {
	f := realAPI(t, map[string]string{"APP_ROLES": "api,reference-worker,consumer", "CONSUMER_QUEUE": stage8Queue(t).name, "REFERENCE_BASE_DELAY": "5ms", "REFERENCE_MAX_DELAY": "20ms", "REFERENCE_TTL": "3s"})
	internal := accessToken(t, "wallet-internal", "KC_SECRET_WALLET_INTERNAL")
	provider := accessToken(t, "provider-a", "KC_SECRET_PROVIDER_A")
	wallet := apiOpen(t, f, internal, "100.00")
	wager := wagerBody(wallet, "provider-a", "cross-bet", "20.00", "BET")
	// Exact same body/key enters both transports; subsequent replays stay original.
	var fields map[string]json.RawMessage
	_ = json.Unmarshal([]byte(wager), &fields)
	fields["idempotencyKey"] = json.RawMessage(`"cross-key"`)
	body, _ := json.Marshal(map[string]any{"messageId": "cross-message-" + rand.Text(), "type": "WagerTransactionRequested", "occurredAt": "2026-09-08T12:00:00Z", "data": fields})
	stage8Send(t, string(body), wallet["id"].(string))
	result := stage8HTTPRace(t, f, provider, "cross-key", wager)
	if result["status"] != "PROCESSED" {
		t.Fatal("HTTP did not process")
	}
	for range 12 {
		stage8Send(t, string(body), wallet["id"].(string))
	}
	eventuallyStage8(t, func() bool {
		var n int
		_ = f.pool.QueryRow(t.Context(), `SELECT count(*) FROM wallet.inbox_messages`).Scan(&n)
		return n == 1
	})
	replay := apiCall(t, f, "POST", "/wagering/transactions", provider, "cross-key", wager, 200)
	if replay["balance"].(map[string]any)["amount"] != "80.00" {
		t.Fatal("replay balance changed")
	}
	refund := wagerBody(wallet, "provider-a", "early-refund", "10.00", "REFUND")
	_ = json.Unmarshal([]byte(refund), &fields)
	fields["idempotencyKey"] = json.RawMessage(`"refund-key"`)
	fields["referenceExternalTransactionId"] = json.RawMessage(`"late-bet"`)
	body, _ = json.Marshal(map[string]any{"messageId": "pending-message-" + rand.Text(), "type": "WagerTransactionRequested", "occurredAt": "2026-09-08T12:00:00Z", "data": fields})
	stage8Send(t, string(body), wallet["id"].(string))
	eventuallyStage8(t, func() bool {
		var state string
		_ = f.pool.QueryRow(t.Context(), `SELECT status FROM wallet.wager_transactions WHERE external_transaction_id='early-refund'`).Scan(&state)
		return state == "PENDING_REFERENCE"
	})
	apiCall(t, f, "POST", "/wagering/transactions", provider, "late-key", wagerBody(wallet, "provider-a", "late-bet", "10.00", "BET"), 201)
	eventuallyStage8(t, func() bool {
		var state string
		_ = f.pool.QueryRow(t.Context(), `SELECT status FROM wallet.wager_transactions WHERE external_transaction_id='early-refund'`).Scan(&state)
		return state == "PROCESSED"
	})
	var b, sum int64
	var n int
	err := f.pool.QueryRow(t.Context(), `SELECT balance_amount,(SELECT sum(CASE direction WHEN 'CREDIT' THEN amount ELSE -amount END)::bigint FROM wallet.ledger_entries WHERE wallet_id=$1),(SELECT count(*) FROM wallet.inbox_messages) FROM wallet.wallets WHERE id=$1`, wallet["id"]).Scan(&b, &sum, &n)
	if err != nil || b != 8000 || sum != b || n != 2 {
		t.Fatalf("history %d/%d/%d %v", b, sum, n, err)
	}
	// Allow real DeleteMessage ACKs of every duplicate to complete before Stop.
	service := sqsClient(t, "wallet-service")
	url := queueURL(t, service, stage8Queue(t).name)
	eventuallyStage8(t, func() bool {
		out, err := service.GetQueueAttributes(t.Context(), &sqs.GetQueueAttributesInput{QueueUrl: aws.String(url), AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameApproximateNumberOfMessages, types.QueueAttributeNameApproximateNumberOfMessagesNotVisible}})
		return err == nil && out.Attributes["ApproximateNumberOfMessages"] == "0" && out.Attributes["ApproximateNumberOfMessagesNotVisible"] == "0"
	})
}

// Worker's real lifecycle can also be tested separately from HTTP composition.
func stage8Worker(t *testing.T, f financialFixture, clients ...sqsconsumer.Client) *sqsconsumer.Worker {
	t.Helper()
	client := sqsconsumer.Client(sqsClient(t, "wallet-service"))
	if len(clients) > 0 {
		client = clients[0]
	}
	worker, err := sqsconsumer.New(client, f.service, f.pool, slog.New(slog.NewJSONHandler(io.Discard, nil)), sqsconsumer.Options{Queue: stage8Queue(t).name, Consumer: "wager-consumer", Providers: []string{"provider-a", "provider-b"}, Concurrency: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err = worker.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := worker.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	return worker
}

func stage8HTTPRace(t *testing.T, f apiFixture, token, key, body string) map[string]any {
	t.Helper()
	r, err := http.NewRequestWithContext(t.Context(), "POST", f.url+"/wagering/transactions", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Idempotency-Key", key)
	r.Header.Set("Content-Type", "application/json")
	resp, err := f.client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 && resp.StatusCode != 201 {
		t.Fatalf("race status %d", resp.StatusCode)
	}
	var result map[string]any
	d := json.NewDecoder(resp.Body)
	d.UseNumber()
	if err = d.Decode(&result); err != nil {
		t.Fatal(err)
	}
	return result
}
