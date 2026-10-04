package sqsconsumer

import (
	"context"
	"errors"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"go.uber.org/goleak"
	"io"
	"jungle-gaming-backend/internal/application"
	"jungle-gaming-backend/internal/domain"
	"log/slog"
	"sync"
	"testing"
	"time"
)

type fakeBroker struct {
	mu       sync.Mutex
	deletes  int
	delay    int32
	message  *types.Message
	received chan struct{}
}

func (b *fakeBroker) GetQueueUrl(context.Context, *sqs.GetQueueUrlInput, ...func(*sqs.Options)) (*sqs.GetQueueUrlOutput, error) {
	return &sqs.GetQueueUrlOutput{QueueUrl: aws.String("queue")}, nil
}
func (b *fakeBroker) ReceiveMessage(ctx context.Context, _ *sqs.ReceiveMessageInput, _ ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
	b.mu.Lock()
	m := b.message
	b.message = nil
	b.mu.Unlock()
	if m != nil {
		return &sqs.ReceiveMessageOutput{Messages: []types.Message{*m}}, nil
	}
	<-ctx.Done()
	return nil, ctx.Err()
}
func (b *fakeBroker) DeleteMessage(context.Context, *sqs.DeleteMessageInput, ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.deletes++
	return &sqs.DeleteMessageOutput{}, nil
}
func (b *fakeBroker) ChangeMessageVisibility(_ context.Context, in *sqs.ChangeMessageVisibilityInput, _ ...func(*sqs.Options)) (*sqs.ChangeMessageVisibilityOutput, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.delay = in.VisibilityTimeout
	return &sqs.ChangeMessageVisibilityOutput{}, nil
}

type fakeService struct {
	entered, release chan struct{}
	err              error
}

func (s *fakeService) ConsumeWager(ctx context.Context, _ string, _ string, _ string, _ domain.ExternalRequest) error {
	if s.entered != nil {
		close(s.entered)
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return s.err
}

type fakeProbe struct{}

func (fakeProbe) Ping(context.Context) error { return nil }
func testWorker(t *testing.T, b *fakeBroker, s *fakeService) *Worker {
	t.Helper()
	w, err := New(b, s, fakeProbe{}, slog.New(slog.NewJSONHandler(io.Discard, nil)), Options{"wager-transactions.fifo", "wager-consumer", []string{"provider-a"}, 1})
	if err != nil {
		t.Fatal(err)
	}
	w.url = "queue"
	return w
}
func testMessage() types.Message {
	return types.Message{Body: aws.String(validBody), ReceiptHandle: aws.String("receipt"), Attributes: map[string]string{"MessageGroupId": wallet, "ApproximateReceiveCount": "3"}}
}
func TestAcknowledgementAndRetryPolicy(t *testing.T) {
	for _, tc := range []struct {
		err     error
		deletes int
		delay   int32
	}{{nil, 1, 0}, {application.ErrUnavailable, 0, 8}, {application.ErrMessageConflict, 0, 0}, {context.Canceled, 0, 0}} {
		b := &fakeBroker{}
		w := testWorker(t, b, &fakeService{err: tc.err})
		_ = w.Handle(t.Context(), testMessage())
		if b.deletes != tc.deletes || b.delay != tc.delay {
			t.Fatal("ACK/retry policy")
		}
	}
	if retryDelay("999999") != 60 || retryDelay("invalid") != 2 {
		t.Fatal("bounded backoff")
	}
}
func TestShutdownDrainsOrCancelsAndNeverAcknowledgesEarly(t *testing.T) {
	defer goleak.VerifyNone(t)
	for _, expire := range []bool{false, true} {
		s := &fakeService{entered: make(chan struct{}), release: make(chan struct{})}
		m := testMessage()
		b := &fakeBroker{message: &m}
		w := testWorker(t, b, s)
		if err := w.Start(t.Context()); err != nil {
			t.Fatal(err)
		}
		<-s.entered
		b.mu.Lock()
		before := b.deletes
		b.mu.Unlock()
		if before != 0 {
			t.Fatal("deleted before durable processing")
		}
		if expire {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
			err := w.Stop(ctx)
			cancel()
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("false stop completion")
			}
		} else {
			close(s.release)
		}
		if err := w.Stop(t.Context()); err != nil {
			t.Fatal(err)
		}
		b.mu.Lock()
		after := b.deletes
		delay := b.delay
		b.mu.Unlock()
		if expire && (after != 0 || delay != 0) {
			t.Fatal("canceled work must redeliver")
		}
		if !expire && after != 1 {
			t.Fatal("successful drain must ACK")
		}
	}
}
