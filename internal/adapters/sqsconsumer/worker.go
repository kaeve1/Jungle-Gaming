// Package sqsconsumer consumes one message per lane, preserving broker FIFO
// ordering within a wallet while independent groups advance in parallel.
package sqsconsumer

import (
	"context"
	"errors"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"jungle-gaming-backend/internal/application"
	"jungle-gaming-backend/internal/domain"
	"log/slog"
	"regexp"
	"strconv"
	"sync"
	"time"
)

type Client interface {
	GetQueueUrl(context.Context, *sqs.GetQueueUrlInput, ...func(*sqs.Options)) (*sqs.GetQueueUrlOutput, error)
	ReceiveMessage(context.Context, *sqs.ReceiveMessageInput, ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error)
	DeleteMessage(context.Context, *sqs.DeleteMessageInput, ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error)
	ChangeMessageVisibility(context.Context, *sqs.ChangeMessageVisibilityInput, ...func(*sqs.Options)) (*sqs.ChangeMessageVisibilityOutput, error)
}
type Processor interface {
	ConsumeWager(context.Context, string, string, string, domain.ExternalRequest) error
}
type Probe interface{ Ping(context.Context) error }

var regexpQueue = regexp.MustCompile(`^[A-Za-z0-9_-]+\.fifo$`)

type Options struct {
	Queue, Consumer string
	Providers       []string
	Concurrency     int
}

func (o Options) Validate() error {
	if o.Queue == "" || len(o.Queue) > 80 || !regexpQueue.MatchString(o.Queue) || len(o.Consumer) > 64 || !messageIDPattern.MatchString(o.Consumer) || o.Concurrency < 1 || o.Concurrency > 16 || len(o.Providers) == 0 {
		return errors.New("invalid consumer options")
	}
	for _, p := range o.Providers {
		if !messageIDPattern.MatchString(p) {
			return errors.New("invalid consumer provider")
		}
	}
	return nil
}

type Worker struct {
	diagnostics            application.Diagnostics
	client                 Client
	service                Processor
	probe                  Probe
	log                    *slog.Logger
	options                Options
	url                    string
	mu                     sync.Mutex
	cancelPoll, cancelWork context.CancelFunc
	done                   chan struct{}
}

func (w *Worker) SetDiagnostics(d application.Diagnostics) { w.diagnostics = d }

func New(client Client, service Processor, probe Probe, log *slog.Logger, o Options) (*Worker, error) {
	if client == nil || service == nil || probe == nil || log == nil {
		return nil, errors.New("consumer dependencies required")
	}
	if err := o.Validate(); err != nil {
		return nil, err
	}
	o.Providers = append([]string(nil), o.Providers...)
	return &Worker{client: client, service: service, probe: probe, log: log, options: o}, nil
}
func (w *Worker) Start(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.done != nil {
		return nil
	}
	out, err := w.client.GetQueueUrl(ctx, &sqs.GetQueueUrlInput{QueueName: aws.String(w.options.Queue)})
	if err != nil || out.QueueUrl == nil {
		return errors.New("consumer queue unavailable")
	}
	w.url = *out.QueueUrl
	poll, cancelPoll := context.WithCancel(context.Background())
	work, cancelWork := context.WithCancel(context.Background())
	w.cancelPoll, w.cancelWork = cancelPoll, cancelWork
	w.done = make(chan struct{})
	go func() {
		var wg sync.WaitGroup
		for range w.options.Concurrency {
			wg.Go(func() { w.run(poll, work) })
		}
		wg.Wait()
		close(w.done)
	}()
	return nil
}
func (w *Worker) Stop(ctx context.Context) error {
	w.mu.Lock()
	done, poll, cancel := w.done, w.cancelPoll, w.cancelWork
	w.mu.Unlock()
	if done == nil {
		return nil
	}
	poll() // stop acquiring; current financial work drains under its own 10s bound.
	select {
	case <-done:
		cancel()
		return nil
	case <-ctx.Done():
		cancel()
		return ctx.Err()
	}
}
func wait(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}
func (w *Worker) run(poll, work context.Context) {
	for poll.Err() == nil {
		check, cancel := context.WithTimeout(poll, time.Second)
		err := w.probe.Ping(check)
		cancel()
		if err != nil {
			wait(poll, time.Second)
			continue
		} // avoid burning delivery attempts during known DB outage.
		out, err := w.client.ReceiveMessage(poll, &sqs.ReceiveMessageInput{QueueUrl: aws.String(w.url), MaxNumberOfMessages: 1, WaitTimeSeconds: 20, VisibilityTimeout: 30, MessageSystemAttributeNames: []types.MessageSystemAttributeName{types.MessageSystemAttributeNameMessageGroupId, types.MessageSystemAttributeNameApproximateReceiveCount}})
		if err != nil {
			if poll.Err() == nil {
				w.log.Warn("consumer receive failed")
				wait(poll, time.Second)
			}
			continue
		}
		for _, m := range out.Messages {
			if poll.Err() != nil {
				w.release(m, 0)
				continue
			}
			_ = w.Handle(work, m)
		}
	}
}

// Handle is also used with broker-delivered messages by integration tests.
// All ACKs are downstream of a successful durable application commit.
func (w *Worker) Handle(parent context.Context, m types.Message) error {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	parsed, err := Decode(aws.ToString(m.Body), m.Attributes["MessageGroupId"], w.options.Providers)
	if err == nil {
		err = w.service.ConsumeWager(ctx, w.options.Consumer, parsed.ID, parsed.Hash, parsed.Request)
	}
	cancel()
	fields := []any{"messageId", parsed.ID, "brokerMessageId", aws.ToString(m.MessageId), "consumerName", w.options.Consumer}
	if parsed.ID != "" {
		fields = append(fields, "correlationId", parsed.ID, "providerId", parsed.Request.ProviderID(), "walletId", parsed.Request.WalletID().String())
	}
	if err == nil {
		ack, stop := context.WithTimeout(parent, 3*time.Second)
		_, err = w.client.DeleteMessage(ack, &sqs.DeleteMessageInput{QueueUrl: aws.String(w.url), ReceiptHandle: m.ReceiptHandle})
		stop()
		if err == nil {
			w.log.Info("consumer durable handling acknowledged", fields...)
			return nil
		}
	}
	delay := retryDelay(m.Attributes["ApproximateReceiveCount"])
	if w.diagnostics != nil {
		w.diagnostics.Count("consumer_retry")
		if permanent(err) {
			w.diagnostics.Count("consumer_permanent")
		}
		n, _ := strconv.Atoi(m.Attributes["ApproximateReceiveCount"])
		if n >= 10 {
			w.diagnostics.Count("consumer_exhausted")
		}
	}
	if permanent(err) || errors.Is(err, context.Canceled) || parent.Err() != nil {
		delay = 0
	}
	// Receipt remains safe for redelivery even when visibility release fails.
	w.release(m, delay)
	w.log.Warn("consumer handling not acknowledged", append(fields, "retryDelaySeconds", delay)...)
	return err
}
func (w *Worker) release(m types.Message, delay int32) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := w.client.ChangeMessageVisibility(ctx, &sqs.ChangeMessageVisibilityInput{QueueUrl: aws.String(w.url), ReceiptHandle: m.ReceiptHandle, VisibilityTimeout: delay}); err != nil {
		w.log.Warn("consumer visibility release failed")
	}
}
func retryDelay(raw string) int32 {
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		n = 1
	}
	n = min(n, 6)
	return min(int32(1<<n), 60)
}
func permanent(err error) bool {
	var classified interface{ Permanent() bool }
	return errors.Is(err, ErrInvalidMessage) || errors.Is(err, application.ErrMessageConflict) || errors.Is(err, application.ErrWalletNotFound) || errors.Is(err, application.ErrIdempotencyKeyReused) || errors.Is(err, application.ErrExternalTransactionConflict) || errors.Is(err, domain.ErrValidation) || errors.Is(err, domain.ErrInvariant) || (errors.As(err, &classified) && classified.Permanent())
}
