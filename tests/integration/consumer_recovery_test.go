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
	"jungle-gaming-backend/internal/adapters/postgres"
	appRuntime "jungle-gaming-backend/internal/adapters/runtime"
	"jungle-gaming-backend/internal/adapters/sqsconsumer"
	"jungle-gaming-backend/internal/application"
	"jungle-gaming-backend/internal/domain"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestStage8MalformedMessagesReachRealDLQ(t *testing.T) {
	f := financialCore(t)
	w := stage5Wallet(t, f, "100.00")
	stage8Worker(t, f)
	req := stage5Request(t, w, "provider-a", "bad", "bad-key", "BET", "1.00")
	prefix := "dlq-" + rand.Text()
	bodies := []string{`{"messageId":"` + prefix + `-json",`, strings.Replace(stage8Body(t, req, prefix+"-opening"), `"BET"`, `"OPENING"`, 1), strings.Replace(stage8Body(t, req, prefix+"-provider"), `"provider-a"`, `"untrusted-provider"`, 1)}
	for i, body := range bodies {
		stage8Send(t, body, fmt.Sprintf("invalid-%s-%d", prefix, i))
	}
	admin := sqsClientWithKeys(t, env(t, "TEST_SQS_ADMIN_ACCESS_KEY_ID"), env(t, "TEST_SQS_ADMIN_SECRET_ACCESS_KEY"))
	url := queueURL(t, admin, stage8Queue(t).dlq)
	seen := map[string]bool{}
	deadline := time.Now().Add(20 * time.Second)
	for len(seen) < len(bodies) && time.Now().Before(deadline) {
		out, err := admin.ReceiveMessage(t.Context(), &sqs.ReceiveMessageInput{QueueUrl: aws.String(url), MaxNumberOfMessages: 10, WaitTimeSeconds: 1, MessageSystemAttributeNames: []types.MessageSystemAttributeName{types.MessageSystemAttributeNameApproximateReceiveCount}})
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range out.Messages {
			body := aws.ToString(m.Body)
			if strings.Contains(body, prefix) {
				seen[body] = true
				_, err = admin.DeleteMessage(t.Context(), &sqs.DeleteMessageInput{QueueUrl: aws.String(url), ReceiptHandle: m.ReceiptHandle})
				if err != nil {
					t.Fatal(err)
				}
			} else {
				_, _ = admin.ChangeMessageVisibility(t.Context(), &sqs.ChangeMessageVisibilityInput{QueueUrl: aws.String(url), ReceiptHandle: m.ReceiptHandle, VisibilityTimeout: 0})
			}
		}
	}
	if len(seen) != 3 {
		t.Fatalf("real redrive delivered %d/3", len(seen))
	}
	stage5HistoryWithoutTrace(t, f, w, 10000, 1, 1, 1, 2)
	var n int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM wallet.inbox_messages`).Scan(&n); err != nil || n != 0 {
		t.Fatal("invalid message persisted")
	}
}

func TestStage8BusinessRejectionAcknowledgedAndInboxConflictRedriven(t *testing.T) {
	f := financialCore(t)
	w := stage5Wallet(t, f, "100.00")
	stage8Worker(t, f)
	first := stage5Request(t, w, "provider-a", "winner", "winner-key", "BET", "80.00")
	rejected := stage5Request(t, w, "provider-a", "rejected", "rejected-key", "BET", "80.00")
	id := "stable-" + rand.Text()
	stage8Send(t, stage8Body(t, first, id), w.ID.String())
	stage8Send(t, stage8Body(t, rejected, "reject-"+rand.Text()), w.ID.String())
	eventuallyStage8(t, func() bool {
		var n int
		err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM wallet.inbox_messages`).Scan(&n)
		return err == nil && n == 2
	})
	// Reuse durable envelope ID with a different valid command: must NOT apply it.
	conflict := stage5Request(t, w, "provider-a", "must-not-credit", "different-key", "WIN", "50.00")
	body := stage8Body(t, conflict, id)
	stage8Send(t, body, w.ID.String())
	admin := sqsClientWithKeys(t, env(t, "TEST_SQS_ADMIN_ACCESS_KEY_ID"), env(t, "TEST_SQS_ADMIN_SECRET_ACCESS_KEY"))
	url := queueURL(t, admin, stage8Queue(t).dlq)
	eventuallyStage8(t, func() bool {
		out, err := admin.ReceiveMessage(t.Context(), &sqs.ReceiveMessageInput{QueueUrl: aws.String(url), WaitTimeSeconds: 1})
		if err != nil {
			return false
		}
		for _, m := range out.Messages {
			if aws.ToString(m.Body) == body {
				_, err = admin.DeleteMessage(t.Context(), &sqs.DeleteMessageInput{QueueUrl: aws.String(url), ReceiptHandle: m.ReceiptHandle})
				return err == nil
			}
		}
		return false
	})
	stage5HistoryWithoutTrace(t, f, w, 2000, 2, 3, 2, 5)
	var status, code string
	if err := f.pool.QueryRow(t.Context(), `SELECT status,failure_code FROM wallet.wager_transactions WHERE external_transaction_id='rejected'`).Scan(&status, &code); err != nil || status != "REJECTED" || code != "INSUFFICIENT_FUNDS" {
		t.Fatal("business rejection not durable")
	}
	var count int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM wallet.inbox_messages`).Scan(&count); err != nil || count != 2 {
		t.Fatal("conflict changed inbox")
	}
}

type unavailableStage8Processor struct{ attempts atomic.Int32 }

func (p *unavailableStage8Processor) ConsumeWager(context.Context, string, string, string, domain.ExternalRequest) error {
	p.attempts.Add(1)
	return application.ErrUnavailable
}
func TestStage8ExhaustedTransientRetryReachesDLQ(t *testing.T) {
	f := financialCore(t)
	w := stage5Wallet(t, f, "100.00")
	q := stage8Queue(t)
	admin := sqsClientWithKeys(t, env(t, "TEST_SQS_ADMIN_ACCESS_KEY_ID"), env(t, "TEST_SQS_ADMIN_SECRET_ACCESS_KEY"))
	input := queueURL(t, admin, q.name)
	policy, _ := json.Marshal(map[string]string{"deadLetterTargetArn": "arn:aws:sqs:us-east-1:000000000000:" + q.dlq, "maxReceiveCount": "2"})
	if _, err := admin.SetQueueAttributes(t.Context(), &sqs.SetQueueAttributesInput{QueueUrl: aws.String(input), Attributes: map[string]string{"RedrivePolicy": string(policy)}}); err != nil {
		t.Fatal(err)
	}
	processor := &unavailableStage8Processor{}
	worker, err := sqsconsumer.New(sqsClient(t, "wallet-service"), processor, f.pool, slog.New(slog.NewJSONHandler(io.Discard, nil)), sqsconsumer.Options{Queue: q.name, Consumer: "wager-consumer", Providers: []string{"provider-a"}, Concurrency: 1})
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
	req := stage5Request(t, w, "provider-a", "unavailable", "unavailable-key", "BET", "1.00")
	body := stage8Body(t, req, "retry-"+rand.Text())
	stage8Send(t, body, w.ID.String())
	dlq := queueURL(t, admin, q.dlq)
	eventuallyStage8(t, func() bool {
		out, err := admin.ReceiveMessage(t.Context(), &sqs.ReceiveMessageInput{QueueUrl: aws.String(dlq), WaitTimeSeconds: 1})
		if err != nil {
			return false
		}
		for _, m := range out.Messages {
			if aws.ToString(m.Body) == body {
				_, err = admin.DeleteMessage(t.Context(), &sqs.DeleteMessageInput{QueueUrl: aws.String(dlq), ReceiptHandle: m.ReceiptHandle})
				return err == nil
			}
		}
		return false
	})
	if processor.attempts.Load() != 2 {
		t.Fatalf("retry attempts %d", processor.attempts.Load())
	}
	stage5HistoryWithoutTrace(t, f, w, 10000, 1, 1, 1, 2)
}

// A separate test process is killed with committed inbox/money and a broker
// receipt still outstanding. No crash hooks are compiled into production.
func TestStage8ConsumerKilledAfterCommitBeforeDelete(t *testing.T) {
	f := financialCore(t)
	w := stage5Wallet(t, f, "100.00")
	req := stage5Request(t, w, "provider-a", "crash-bet", "crash-key", "BET", "20.00")
	id := "crash-" + rand.Text()
	stage8Send(t, stage8Body(t, req, id), w.ID.String())
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	barrier := filepath.Join(t.TempDir(), "committed")
	child := exec.Command(binary, "-test.run=^TestStage8CrashConsumerHelper$", "-test.timeout=30s")
	child.Env = append(os.Environ(), "STAGE8_CHILD_DB="+f.db, "STAGE8_COMMIT_BARRIER="+barrier, "STAGE8_EXPECTED_MESSAGE="+id, "STAGE8_CHILD_QUEUE="+stage8Queue(t).name)
	// Output is omitted: credentials and financial payloads never enter parent logs.
	child.Stdout = io.Discard
	child.Stderr = io.Discard
	if err = child.Start(); err != nil {
		t.Fatal(err)
	}
	killed := false
	t.Cleanup(func() {
		if !killed {
			_ = child.Process.Kill()
			_ = child.Wait()
		}
	})
	eventuallyStage8(t, func() bool { _, err := os.Stat(barrier); return err == nil })
	if err = child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	err = child.Wait()
	killed = true
	if err == nil {
		t.Fatal("child was not interrupted")
	}
	var inbox int
	if err = f.pool.QueryRow(t.Context(), `SELECT count(*) FROM wallet.inbox_messages WHERE message_id=$1`, id).Scan(&inbox); err != nil || inbox != 1 {
		t.Fatal("commit barrier before durable inbox")
	}
	stage5HistoryWithoutTrace(t, f, w, 8000, 2, 2, 2, 4)
	// Visibility expires, another actual worker processes the durable replay.
	observed := &observedStage8Client{Client: sqsClient(t, "wallet-service"), deliveries: make(chan types.Message, 4)}
	worker := stage8Worker(t, f, observed)
	_ = worker
	client := sqsClient(t, "wallet-service")
	url := queueURL(t, client, stage8Queue(t).name)
	eventuallyStage8(t, func() bool {
		out, err := client.GetQueueAttributes(t.Context(), &sqs.GetQueueAttributesInput{QueueUrl: aws.String(url), AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameApproximateNumberOfMessages, types.QueueAttributeNameApproximateNumberOfMessagesNotVisible}})
		return err == nil && out.Attributes["ApproximateNumberOfMessages"] == "0" && out.Attributes["ApproximateNumberOfMessagesNotVisible"] == "0"
	})
	stage5HistoryWithoutTrace(t, f, w, 8000, 2, 2, 2, 4)
	select {
	case delivered := <-observed.deliveries:
		if delivered.Attributes["ApproximateReceiveCount"] != "2" || !strings.Contains(aws.ToString(delivered.Body), id) {
			t.Fatal("actual broker redelivery not proven")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("replacement consumer did not receive")
	}
}
func TestStage8CrashConsumerHelper(t *testing.T) {
	db := os.Getenv("STAGE8_CHILD_DB")
	if db == "" {
		t.Skip("child process only")
	}
	pool, _, service := financialPool(t, db, postgres.DefaultOptions())
	processor := &barrierStage8Processor{service: service}
	client := &shortVisibilityStage8Client{Client: sqsClient(t, "wallet-service")}
	worker, err := sqsconsumer.New(client, processor, pool, slog.New(slog.NewJSONHandler(io.Discard, nil)), sqsconsumer.Options{Queue: stage8Queue(t).name, Consumer: "wager-consumer", Providers: []string{"provider-a"}, Concurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err = worker.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Runs the production Worker/Handle; parent kills after real commit, before ACK.
	select {}
}

type barrierStage8Processor struct{ service *application.Service }

func (p *barrierStage8Processor) ConsumeWager(ctx context.Context, consumer, id, hash string, req domain.ExternalRequest) error {
	if id != os.Getenv("STAGE8_EXPECTED_MESSAGE") {
		return errors.New("unexpected child message")
	}
	if err := p.service.ConsumeWager(ctx, consumer, id, hash, req); err != nil {
		return err
	}
	if err := os.WriteFile(os.Getenv("STAGE8_COMMIT_BARRIER"), []byte("committed"), 0600); err != nil {
		return err
	}
	<-ctx.Done()
	return ctx.Err()
}

// Short visibility belongs only to this test's isolated queue/process.
// Production still uses 30 seconds; no hooks are compiled into the service.
type shortVisibilityStage8Client struct{ *sqs.Client }

func (c *shortVisibilityStage8Client) ReceiveMessage(ctx context.Context, in *sqs.ReceiveMessageInput, opt ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
	short := *in
	short.VisibilityTimeout = 2
	return c.Client.ReceiveMessage(ctx, &short, opt...)
}
func TestStage8GroupsParallelAndRetryKeepsFIFOOrder(t *testing.T) {
	f := financialCore(t)
	a := stage5Wallet(t, f, "100.00")
	b := stage5Wallet(t, f, "100.00")
	blocker, err := f.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(context.Background())
	if _, err = blocker.Exec(t.Context(), `SELECT 1 FROM wallet.wallets WHERE id=$1 FOR UPDATE`, a.ID.String()); err != nil {
		t.Fatal(err)
	}
	// Short lock timeout/retry budget yields a real transient DB failure.
	options := f.options
	options.LockTimeout = 30 * time.Millisecond
	options.StatementTimeout = 100 * time.Millisecond
	options.MaxRetries = 0
	uow, err := postgres.NewUnitOfWork(f.pool, options)
	if err != nil {
		t.Fatal(err)
	}
	service, err := application.NewService(uow, appRuntime.IDs{}, domain.DefaultReferencePolicy, nil)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := sqsconsumer.New(sqsClient(t, "wallet-service"), service, f.pool, slog.New(slog.NewJSONHandler(io.Discard, nil)), sqsconsumer.Options{Queue: stage8Queue(t).name, Consumer: "wager-consumer", Providers: []string{"provider-a"}, Concurrency: 2})
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
	first := stage5Request(t, a, "provider-a", "first", "first-key", "BET", "80.00")
	second := stage5Request(t, a, "provider-a", "second", "second-key", "BET", "80.00")
	independent := stage5Request(t, b, "provider-a", "independent", "b-key", "BET", "10.00")
	stage8Send(t, stage8Body(t, first, "first-"+rand.Text()), a.ID.String())
	stage8Send(t, stage8Body(t, second, "second-"+rand.Text()), a.ID.String())
	stage8Send(t, stage8Body(t, independent, "other-"+rand.Text()), b.ID.String())
	eventuallyStage8(t, func() bool {
		var n int
		_ = f.pool.QueryRow(t.Context(), `SELECT count(*) FROM wallet.inbox_messages`).Scan(&n)
		return n == 1
	})
	var state string
	var n int
	_ = f.pool.QueryRow(t.Context(), `SELECT count(*) FROM wallet.wager_transactions WHERE wallet_id=$1 AND origin='EXTERNAL'`, a.ID.String()).Scan(&n)
	if n != 0 {
		t.Fatal("locked wallet processed")
	}
	if err = blocker.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	eventuallyStage8(t, func() bool {
		_ = f.pool.QueryRow(t.Context(), `SELECT count(*) FROM wallet.inbox_messages`).Scan(&n)
		return n == 3
	})
	_ = f.pool.QueryRow(t.Context(), `SELECT status FROM wallet.wager_transactions WHERE external_transaction_id='first'`).Scan(&state)
	if state != "PROCESSED" {
		t.Fatal("FIFO first must win")
	}
	_ = f.pool.QueryRow(t.Context(), `SELECT status FROM wallet.wager_transactions WHERE external_transaction_id='second'`).Scan(&state)
	if state != "REJECTED" {
		t.Fatal("FIFO second must reject")
	}
	stage5HistoryWithoutTrace(t, f, a, 2000, 2, 3, 2, 5)
	stage5HistoryWithoutTrace(t, f, b, 9000, 2, 2, 2, 4)
	// Persisted pending event snapshots and financial metadata remain typed JSON.
	var invalid int
	_ = f.pool.QueryRow(t.Context(), `SELECT count(*) FROM wallet.outbox_events WHERE jsonb_typeof(payload->'data')<>'object'`).Scan(&invalid)
	if invalid != 0 {
		t.Fatal("invalid snapshots")
	}
}

type observedStage8Client struct {
	*sqs.Client
	deliveries chan types.Message
}

func (c *observedStage8Client) ReceiveMessage(ctx context.Context, in *sqs.ReceiveMessageInput, opt ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
	out, err := c.Client.ReceiveMessage(ctx, in, opt...)
	if err == nil {
		for _, m := range out.Messages {
			select {
			case c.deliveries <- m:
			default:
			}
		}
	}
	return out, err
}
