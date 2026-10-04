// Package referenceworker runs durable database-only reference recovery.
package referenceworker

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

type Resumer interface {
	ResumeReference(context.Context) (bool, error)
}
type Worker struct {
	service Resumer
	log     *slog.Logger
	mu      sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
}

func New(service Resumer, log *slog.Logger) *Worker { return &Worker{service: service, log: log} }
func (w *Worker) Start(context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.done != nil {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	w.done = make(chan struct{})
	go func() {
		defer close(w.done)
		for ctx.Err() == nil {
			worked, err := w.service.ResumeReference(ctx)
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				w.log.Warn("reference recovery attempt failed")
			}
			if worked && err == nil {
				continue
			}
			timer := time.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()
	return nil
}
func (w *Worker) Stop(ctx context.Context) error {
	w.mu.Lock()
	cancel, done := w.cancel, w.done
	w.mu.Unlock()
	if done == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
