package outboxpublisher

import (
	"context"
	"errors"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"go.uber.org/goleak"
	"io"
	"jungle-gaming-backend/internal/application"
	"log/slog"
	"sync"
	"testing"
	"time"
)

type testStore struct {
	mu                 sync.Mutex
	used               bool
	completed, retried int
}

func TestEventFieldsDistinguishesInputMessageFromPendingEvent(t *testing.T) {
	for _, messageID := range []string{"original-sqs-message", ""} {
		r := application.OutboxRecord{EventID: "final-event", PartitionKey: "wallet", MessageID: messageID,
			Payload: []byte(`{"correlationId":"original-correlation","causationId":"pending-event","data":{"transactionId":"transaction","providerId":"provider-a"}}`)}
		fields := eventFields(r)
		values := map[string]any{}
		for i := 0; i < len(fields); i += 2 {
			values[fields[i].(string)] = fields[i+1]
		}
		if values["causationId"] != "pending-event" {
			t.Fatal("pending event cause lost")
		}
		if messageID == "" {
			if _, exists := values["messageId"]; exists {
				t.Fatal("HTTP publication invented a messageId")
			}
		} else if values["messageId"] != messageID {
			t.Fatal("input message identity lost")
		}
	}
}

func (s *testStore) CanSend(context.Context, application.OutboxRecord) (bool, error) {
	return true, nil
}

func (s *testStore) Claim(context.Context, string, time.Duration) (*application.OutboxRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.used {
		return nil, nil
	}
	s.used = true
	return &application.OutboxRecord{EventID: "event", PartitionKey: "wallet", Payload: []byte(`{"eventId":"event"}`), Token: "token", Attempts: 1}, nil
}
func (s *testStore) Complete(context.Context, application.OutboxRecord) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completed++
	return true, nil
}
func (s *testStore) Retry(context.Context, application.OutboxRecord, time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.retried++
	return true, nil
}

type testClient struct{ entered, release chan struct{} }

func (c testClient) GetQueueUrl(context.Context, *sqs.GetQueueUrlInput, ...func(*sqs.Options)) (*sqs.GetQueueUrlOutput, error) {
	return &sqs.GetQueueUrlOutput{QueueUrl: aws.String("queue")}, nil
}
func (c testClient) SendMessage(ctx context.Context, in *sqs.SendMessageInput, _ ...func(*sqs.Options)) (*sqs.SendMessageOutput, error) {
	close(c.entered)
	select {
	case <-c.release:
		return &sqs.SendMessageOutput{}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func TestShutdownDrainAndDeadline(t *testing.T) {
	defer goleak.VerifyNone(t)
	for _, deadline := range []bool{false, true} {
		t.Run(map[bool]string{false: "drain", true: "cancel"}[deadline], func(t *testing.T) {
			store := &testStore{}
			client := testClient{make(chan struct{}), make(chan struct{})}
			w, e := New(client, store, slog.New(slog.NewTextHandler(io.Discard, nil)), "events.fifo", "owner", 1)
			if e != nil {
				t.Fatal(e)
			}
			if e = w.Start(t.Context()); e != nil {
				t.Fatal(e)
			}
			<-client.entered
			store.mu.Lock()
			n := store.completed
			store.mu.Unlock()
			if n != 0 {
				t.Fatal("confirmed before send")
			}
			if deadline {
				ctx, c := context.WithTimeout(context.Background(), time.Millisecond)
				defer c()
				if e = w.Stop(ctx); !errors.Is(e, context.DeadlineExceeded) {
					t.Fatal("deadline hidden")
				}
			} else {
				close(client.release)
			}
			ctx, c := context.WithTimeout(context.Background(), time.Second)
			defer c()
			if e = w.Stop(ctx); e != nil {
				t.Fatal(e)
			}
			store.mu.Lock()
			defer store.mu.Unlock()
			if deadline && (store.completed != 0 || store.retried != 1) {
				t.Fatal("canceled work lost")
			}
			if !deadline && store.completed != 1 {
				t.Fatal("drained send unconfirmed")
			}
		})
	}
}
