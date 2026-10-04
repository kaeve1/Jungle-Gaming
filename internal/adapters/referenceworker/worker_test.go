package referenceworker

import (
	"context"
	"errors"
	"go.uber.org/goleak"
	"io"
	"log/slog"
	"testing"
	"time"
)

type blocked struct {
	entered chan struct{}
	release chan struct{}
	honor   bool
}

func (b *blocked) ResumeReference(ctx context.Context) (bool, error) {
	close(b.entered)
	if b.honor {
		<-ctx.Done()
		return false, ctx.Err()
	}
	<-b.release
	return false, errors.New("test unavailable")
}
func TestStopWaitsForActualTermination(t *testing.T) {
	defer goleak.VerifyNone(t)
	b := &blocked{entered: make(chan struct{}), release: make(chan struct{})}
	w := New(b, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := w.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	<-b.entered
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	if err := w.Stop(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stop reported completion: %v", err)
	}
	close(b.release)
	if err := w.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
}
func TestStopCancelsIOAndIdleWait(t *testing.T) {
	defer goleak.VerifyNone(t)
	b := &blocked{entered: make(chan struct{}), honor: true}
	w := New(b, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := w.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	<-b.entered
	if err := w.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := w.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
}
