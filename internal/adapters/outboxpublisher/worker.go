// Package outboxpublisher publishes committed snapshots with at-least-once delivery.
package outboxpublisher

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"jungle-gaming-backend/internal/application"
	"log/slog"
	"regexp"
	"sync"
	"time"
)

var queuePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+\.fifo$`)

type Client interface {
	GetQueueUrl(context.Context, *sqs.GetQueueUrlInput, ...func(*sqs.Options)) (*sqs.GetQueueUrlOutput, error)
	SendMessage(context.Context, *sqs.SendMessageInput, ...func(*sqs.Options)) (*sqs.SendMessageOutput, error)
}
type Worker struct {
	diagnostics            application.Diagnostics
	client                 Client
	store                  application.OutboxStore
	log                    *slog.Logger
	queue, owner, url      string
	concurrency            int
	mu                     sync.Mutex
	cancelPoll, cancelWork context.CancelFunc
	done                   chan struct{}
}

func (w *Worker) SetDiagnostics(d application.Diagnostics) { w.diagnostics = d }

func New(client Client, store application.OutboxStore, log *slog.Logger, queue, owner string, concurrency int) (*Worker, error) {
	if client == nil || store == nil || log == nil || !queuePattern.MatchString(queue) || len(queue) > 80 || owner == "" || len(owner) > 128 || concurrency < 1 || concurrency > 16 {
		return nil, errors.New("invalid publisher configuration")
	}
	return &Worker{client: client, store: store, log: log, queue: queue, owner: owner, concurrency: concurrency}, nil
}
func (w *Worker) Start(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.done != nil {
		return nil
	}
	q, err := w.client.GetQueueUrl(ctx, &sqs.GetQueueUrlInput{QueueName: aws.String(w.queue)})
	if err != nil || q.QueueUrl == nil {
		return errors.New("publisher queue unavailable")
	}
	w.url = *q.QueueUrl
	poll, p := context.WithCancel(context.Background())
	work, c := context.WithCancel(context.Background())
	w.cancelPoll = p
	w.cancelWork = c
	w.done = make(chan struct{})
	go func() {
		var wg sync.WaitGroup
		for range w.concurrency {
			wg.Go(func() { w.run(poll, work) })
		}
		wg.Wait()
		close(w.done)
	}()
	return nil
}
func (w *Worker) Stop(ctx context.Context) error {
	w.mu.Lock()
	done, p, c := w.done, w.cancelPoll, w.cancelWork
	w.mu.Unlock()
	if done == nil {
		return nil
	}
	p()
	select {
	case <-done:
		c()
		return nil
	case <-ctx.Done():
		c()
		return ctx.Err()
	}
}
func pause(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
func (w *Worker) run(poll, work context.Context) {
	for poll.Err() == nil {
		r, err := w.store.Claim(poll, w.owner+"/"+rand.Text(), 30*time.Second)
		if err != nil {
			if poll.Err() == nil {
				w.log.Warn("outbox claim failed")
			}
			pause(poll, time.Second)
			continue
		}
		if r == nil {
			pause(poll, 200*time.Millisecond)
			continue
		}
		if poll.Err() != nil {
			w.retry(*r, 0)
			return
		}
		_ = w.Publish(work, *r)
	}
}

// No new envelope is generated on retry; eventId and the exact snapshot survive.
func (w *Worker) Publish(parent context.Context, r application.OutboxRecord) error {
	fields := eventFields(r)
	ready, err := w.store.CanSend(parent, r)
	if err != nil || !ready {
		w.retry(r, 0)
		if err == nil {
			err = errors.New("outbox send lease unavailable")
		}
		return err
	}
	send, cancel := context.WithTimeout(parent, 10*time.Second)
	_, err = w.client.SendMessage(send, &sqs.SendMessageInput{QueueUrl: aws.String(w.url), MessageBody: aws.String(string(r.Payload)), MessageGroupId: aws.String(r.PartitionKey), MessageDeduplicationId: aws.String(r.EventID)})
	cancel()
	if err == nil {
		ack, c := context.WithTimeout(parent, 3*time.Second)
		var owned bool
		owned, err = w.store.Complete(ack, r)
		c()
		if err == nil && owned {
			w.log.Info("outbox event published", fields...)
			return nil
		}
		if err == nil {
			err = errors.New("outbox lease lost")
		}
	}
	// Failure/uncertain send or confirmation remains recoverable indefinitely.
	delay := time.Duration(1<<min(max(r.Attempts, 1), 6)) * time.Second
	if parent.Err() != nil {
		delay = 0
	}
	w.retry(r, delay)
	if w.diagnostics != nil {
		w.diagnostics.Count("publisher_retry")
	}
	w.log.Warn("outbox publication unconfirmed", append(fields, "attempt", r.Attempts)...)
	return err
}

func eventFields(r application.OutboxRecord) []any {
	var e struct {
		CorrelationID string `json:"correlationId"`
		CausationID   string `json:"causationId"`
		Data          struct {
			TransactionID string `json:"transactionId"`
			ProviderID    string `json:"providerId"`
		} `json:"data"`
	}
	_ = json.Unmarshal(r.Payload, &e)
	fields := []any{"eventId", r.EventID, "walletId", r.PartitionKey}
	for _, v := range []struct{ k, v string }{{"correlationId", e.CorrelationID}, {"causationId", e.CausationID}, {"messageId", r.MessageID}, {"transactionId", e.Data.TransactionID}, {"providerId", e.Data.ProviderID}} {
		if v.v != "" {
			fields = append(fields, v.k, v.v)
		}
	}
	return fields
}
func (w *Worker) retry(r application.OutboxRecord, d time.Duration) {
	ctx, c := context.WithTimeout(context.Background(), 2*time.Second)
	defer c()
	if _, err := w.store.Retry(ctx, r, min(d, time.Minute)); err != nil {
		w.log.Warn("outbox retry scheduling failed", "eventId", r.EventID)
	}
}
