package httpapi

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestShutdownFinishesInFlightRequest(t *testing.T) {
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	server := NewServer("127.0.0.1:0", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-release; w.WriteHeader(204) }), 20*time.Second)
	if err := server.Start(t.Context(), func() { t.Error("server failed") }); err != nil {
		t.Fatal(err)
	}
	defer server.Stop(context.Background())
	go func() {
		resp, err := http.Get("http://" + server.Address())
		if err == nil {
			resp.Body.Close()
		}
		close(finished)
	}()
	<-entered
	stopped := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		stopped <- server.Stop(ctx)
	}()
	select {
	case err := <-stopped:
		t.Fatalf("shutdown abandoned request: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	<-finished
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
}

func TestShutdownDeadlineCancelsActiveRequest(t *testing.T) {
	entered, canceled, requestDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	server := NewServer("127.0.0.1:0", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-r.Context().Done(); close(canceled) }), 20*time.Second)
	if err := server.Start(t.Context(), func() { t.Error("server failed") }); err != nil {
		t.Fatal(err)
	}
	defer server.Stop(context.Background())
	go func() {
		client := &http.Client{Timeout: 2 * time.Second}
		resp, err := client.Get("http://" + server.Address())
		if err == nil {
			resp.Body.Close()
		}
		close(requestDone)
	}()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := server.Stop(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown deadline: %v", err)
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("active request did not receive cancellation")
	}
	<-requestDone
}
