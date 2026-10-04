//go:build integration

package integration

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"io"
	"jungle-gaming-backend/internal/adapters/outboxpublisher"
	"jungle-gaming-backend/internal/adapters/postgres"
	"jungle-gaming-backend/internal/application"
	"log/slog"
	"net/url"
	"sync"
	"testing"
	"time"
)

func stage9Queue(t *testing.T) string {
	t.Helper()
	name := "stage9-" + rand.Text()[:12] + ".fifo"
	admin := sqsClientWithKeys(t, env(t, "TEST_SQS_ADMIN_ACCESS_KEY_ID"), env(t, "TEST_SQS_ADMIN_SECRET_ACCESS_KEY"))
	q, err := admin.CreateQueue(t.Context(), &sqs.CreateQueueInput{QueueName: aws.String(name), Attributes: map[string]string{"FifoQueue": "true", "ContentBasedDeduplication": "false"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, user := range []string{"wallet-service", "events-reader"} {
		actions := []string{"sqs:GetQueueUrl", "sqs:GetQueueAttributes"}
		if user == "wallet-service" {
			actions = append(actions, "sqs:SendMessage")
		} else {
			actions = append(actions, "sqs:ReceiveMessage", "sqs:DeleteMessage")
		}
		policy, _ := json.Marshal(map[string]any{"Version": "2012-10-17", "Statement": []any{map[string]any{"Effect": "Allow", "Action": actions, "Resource": "arn:aws:sqs:us-east-1:000000000000:" + name}}})
		stage8IAM(t, t.Context(), url.Values{"Action": {"PutUserPolicy"}, "UserName": {user}, "PolicyName": {name}, "PolicyDocument": {string(policy)}})
	}
	t.Cleanup(func() {
		ctx, c := context.WithTimeout(context.Background(), 10*time.Second)
		defer c()
		_, err := admin.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: q.QueueUrl})
		if err != nil {
			t.Error(err)
		}
		for _, user := range []string{"wallet-service", "events-reader"} {
			stage8IAM(t, ctx, url.Values{"Action": {"DeleteUserPolicy"}, "UserName": {user}, "PolicyName": {name}})
		}
	})
	return name
}
func stage9Publisher(t *testing.T, store application.OutboxStore, client outboxpublisher.Client, queue, owner string) *outboxpublisher.Worker {
	t.Helper()
	w, err := outboxpublisher.New(client, store, slog.New(slog.NewJSONHandler(io.Discard, nil)), queue, owner, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, c := context.WithTimeout(context.Background(), 20*time.Second)
		defer c()
		if err := w.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	return w
}
func TestStage9ClaimsLeaseAndStaleConfirmation(t *testing.T) {
	f := financialCore(t)
	stage5Wallet(t, f, "100.00")
	store := postgres.NewOutbox(f.pool)
	// Competing independent pools cannot reserve the same record.
	pool, _, _ := financialPool(t, f.db, f.options)
	other := postgres.NewOutbox(pool)
	var wg sync.WaitGroup
	ids := make(chan string, 10)
	for n := range 10 {
		wg.Go(func() {
			s := store
			if n%2 == 0 {
				s = other
			}
			r, e := s.Claim(t.Context(), rand.Text(), 100*time.Millisecond)
			if e != nil {
				t.Error(e)
			} else if r != nil {
				ids <- r.EventID
			}
		})
	}
	wg.Wait()
	close(ids)
	seen := map[string]bool{}
	for id := range ids {
		if seen[id] {
			t.Fatal("duplicate claim")
		}
		seen[id] = true
	}
	if len(seen) != 2 {
		t.Fatal("not all committed events claimed")
	}
	time.Sleep(130 * time.Millisecond)
	r, e := store.Claim(t.Context(), "new-token", 30*time.Second)
	if e != nil || r == nil {
		t.Fatalf("expired lease not recovered: %v", e)
	}
	if ok, e := store.CanSend(t.Context(), *r); e != nil || !ok {
		t.Fatal("valid send margin rejected")
	}
	old := *r
	old.Token = "old-token"
	if ok, e := other.CanSend(t.Context(), old); e != nil || ok {
		t.Fatal("stale owner allowed to send")
	}
	if ok, e := other.Complete(t.Context(), old); e != nil || ok {
		t.Fatal("stale confirmation accepted")
	}
	if ok, e := other.Retry(t.Context(), old, 0); e != nil || ok {
		t.Fatal("stale retry accepted")
	}
	if ok, e := store.Complete(t.Context(), *r); e != nil || !ok {
		t.Fatal("owner confirmation failed")
	}
	r, e = store.Claim(t.Context(), "expires", 50*time.Millisecond)
	if e != nil || r == nil {
		t.Fatal(e)
	}
	time.Sleep(70 * time.Millisecond)
	if ok, e := store.CanSend(t.Context(), *r); e != nil || ok {
		t.Fatal("expired lease allowed to send")
	}
	if ok, e := store.Complete(t.Context(), *r); e != nil || ok {
		t.Fatal("expired owner confirmed")
	}
}

type stage9FailSend struct{ *sqs.Client }

func (c stage9FailSend) SendMessage(context.Context, *sqs.SendMessageInput, ...func(*sqs.Options)) (*sqs.SendMessageOutput, error) {
	return nil, errors.New("secret payload must never be persisted")
}

type stage9NoConfirm struct{ application.OutboxStore }

func (s stage9NoConfirm) Complete(context.Context, application.OutboxRecord) (bool, error) {
	return false, errors.New("confirmation interrupted")
}

type stage9Observed struct {
	*sqs.Client
	mu   sync.Mutex
	sent []string
}

func (c *stage9Observed) SendMessage(ctx context.Context, in *sqs.SendMessageInput, o ...func(*sqs.Options)) (*sqs.SendMessageOutput, error) {
	out, e := c.Client.SendMessage(ctx, in, o...)
	if e == nil {
		c.mu.Lock()
		c.sent = append(c.sent, aws.ToString(in.MessageBody))
		c.mu.Unlock()
	}
	return out, e
}
func TestStage9RealPublishersRetryRecoveryAndSnapshots(t *testing.T) {
	f := financialCore(t)
	w := stage5Wallet(t, f, "100.00")
	processStage5(t, f, stage5Request(t, w, "provider-a", "bet9", "bet9", "BET", "80.00"))
	processStage5(t, f, stage5Request(t, w, "provider-a", "reject9", "reject9", "BET", "80.00"))
	processStage5(t, f, stage5Request(t, w, "provider-a", "loss9", "loss9", "LOSS", "0.00"))
	processStage5(t, f, refRequest(t, w, "pending9", "REFUND", "1.00", "missing9"))
	queue := stage9Queue(t)
	store := postgres.NewOutbox(f.pool)
	client := sqsClient(t, "wallet-service")
	// Controlled interruption before send: row is durable, retry persists sanitized error.
	failed := stage9Publisher(t, store, stage9FailSend{client}, queue, "failed")
	eventuallyStage8(t, func() bool {
		var n int
		_ = f.pool.QueryRow(t.Context(), `SELECT count(*) FROM wallet.outbox_events WHERE last_error='SEND_OR_CONFIRMATION_FAILED'`).Scan(&n)
		return n == 7
	})
	ctx, c := context.WithTimeout(t.Context(), 15*time.Second)
	if err := failed.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	c()
	// After-send/before-confirm interruption: SDK spy proves actual successful SendMessage.
	observed := &stage9Observed{Client: client}
	unconfirmed := stage9Publisher(t, stage9NoConfirm{store}, observed, queue, "interrupted")
	eventuallyStage8(t, func() bool { observed.mu.Lock(); defer observed.mu.Unlock(); return len(observed.sent) >= 7 })
	ctx, c = context.WithTimeout(t.Context(), 15*time.Second)
	if err := unconfirmed.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	c()
	observed.mu.Lock()
	first := append([]string(nil), observed.sent...)
	observed.mu.Unlock()
	pool, _, _ := financialPool(t, f.db, f.options)
	stage9Publisher(t, store, observed, queue, "replacement-a")
	stage9Publisher(t, postgres.NewOutbox(pool), observed, queue, "replacement-b")
	eventuallyStage8(t, func() bool {
		var n int
		_ = f.pool.QueryRow(t.Context(), `SELECT count(*) FROM wallet.outbox_events WHERE published_at IS NOT NULL`).Scan(&n)
		return n == 7
	})
	observed.mu.Lock()
	all := append([]string(nil), observed.sent...)
	observed.mu.Unlock()
	bodies := map[string]int{}
	for _, b := range all {
		bodies[b]++
	}
	for _, b := range first {
		if bodies[b] < 2 {
			t.Fatal("republication did not preserve exact snapshot")
		}
	}
	reader := sqsClient(t, "events-reader")
	qurl := queueURL(t, reader, queue)
	captured := map[string]bool{}
	eventuallyStage8(t, func() bool {
		out, e := reader.ReceiveMessage(t.Context(), &sqs.ReceiveMessageInput{QueueUrl: aws.String(qurl), MaxNumberOfMessages: 10})
		if e != nil {
			t.Error(e)
			return false
		}
		for _, m := range out.Messages {
			var envelope map[string]json.RawMessage
			if e = json.Unmarshal([]byte(aws.ToString(m.Body)), &envelope); e != nil {
				t.Error(e)
			}
			var id string
			_ = json.Unmarshal(envelope["eventId"], &id)
			var original string
			if e = f.pool.QueryRow(t.Context(), `SELECT payload::text FROM wallet.outbox_events WHERE event_id=$1`, id).Scan(&original); e != nil || original != aws.ToString(m.Body) {
				t.Error("destination snapshot mismatch")
			}
			captured[id] = true
			_, e = reader.DeleteMessage(t.Context(), &sqs.DeleteMessageInput{QueueUrl: aws.String(qurl), ReceiptHandle: m.ReceiptHandle})
			if e != nil {
				t.Error(e)
			}
		}
		return len(captured) == 7
	})
	stage5HistoryWithoutTrace(t, f, w, 2000, 2, 5, 2, 7)
}
func TestStage9UncommittedInvisibleAndNetworkOutsideWalletLock(t *testing.T) {
	f := financialCore(t)
	w := stage5Wallet(t, f, "100.00")
	store := postgres.NewOutbox(f.pool)
	// Hold a financial wallet lock while claims are made: publishing never needs it.
	tx, e := f.pool.Begin(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(t.Context())
	_, e = tx.Exec(t.Context(), `SELECT id FROM wallet.wallets WHERE id=$1 FOR UPDATE`, w.ID.String())
	if e != nil {
		t.Fatal(e)
	}
	ctx, c := context.WithTimeout(t.Context(), time.Second)
	defer c()
	r, e := store.Claim(ctx, "independent", time.Second)
	if e != nil || r == nil {
		t.Fatalf("claim waited for wallet: %v", e)
	}
	// Uncommitted outbox row must never be visible to a publisher.
	_, e = tx.Exec(t.Context(), `INSERT INTO wallet.outbox_events(event_id,aggregate_type,aggregate_id,partition_key,event_type,event_version,payload,occurred_at) VALUES(gen_random_uuid(),'Wallet',$1,$1,'WalletBalanceChanged',1,'{}',now())`, w.ID.String())
	if e != nil {
		t.Fatal(e)
	}
	count := 0
	for {
		r, e = store.Claim(ctx, rand.Text(), time.Second)
		if e != nil {
			t.Fatal(e)
		}
		if r == nil {
			break
		}
		count++
	}
	if count != 1 {
		t.Fatal("uncommitted event visible")
	}
}

type stage9Reorder struct {
	*sqs.Client
	newer    chan struct{}
	mu       sync.Mutex
	versions []int
}

func (c *stage9Reorder) SendMessage(ctx context.Context, in *sqs.SendMessageInput, o ...func(*sqs.Options)) (*sqs.SendMessageOutput, error) {
	var e struct {
		EventType string `json:"eventType"`
		Data      struct {
			Version int `json:"walletVersion"`
		} `json:"data"`
	}
	_ = json.Unmarshal([]byte(aws.ToString(in.MessageBody)), &e)
	if e.EventType == "WalletBalanceChanged" && e.Data.Version == 1 {
		select {
		case <-c.newer:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	out, err := c.Client.SendMessage(ctx, in, o...)
	if err == nil && e.EventType == "WalletBalanceChanged" {
		c.mu.Lock()
		c.versions = append(c.versions, e.Data.Version)
		c.mu.Unlock()
		if e.Data.Version == 2 {
			close(c.newer)
		}
	}
	return out, err
}
func TestStage9WalletEventsMayArriveOutOfCommitOrder(t *testing.T) {
	f := financialCore(t)
	w := stage5Wallet(t, f, "100.00")
	processStage5(t, f, stage5Request(t, w, "provider-a", "order9", "order9", "BET", "20.00"))
	queue := stage9Queue(t)
	client := &stage9Reorder{Client: sqsClient(t, "wallet-service"), newer: make(chan struct{})}
	stage9Publisher(t, postgres.NewOutbox(f.pool), client, queue, "order-a")
	stage9Publisher(t, postgres.NewOutbox(f.pool), client, queue, "order-b")
	eventuallyStage8(t, func() bool { client.mu.Lock(); defer client.mu.Unlock(); return len(client.versions) == 2 })
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.versions[0] != 2 || client.versions[1] != 1 {
		t.Fatal("test did not exercise inverted commit order")
	}
}
