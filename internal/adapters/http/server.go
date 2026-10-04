package httpapi

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"
)

// Server has explicit lifecycle methods; bootstrap owns their Fx hooks.
type Server struct {
	server   *http.Server
	mu       sync.Mutex
	listener net.Listener
	done     chan error
	cancel   context.CancelFunc
}

func NewServer(address string, handler http.Handler, operationTimeout time.Duration) *Server {
	// Opening conflicts can perform a write attempt followed by a read. Reserve
	// both bounded DB calls plus time for authentication and body ingestion.
	return &Server{server: &http.Server{Addr: address, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 2*operationTimeout + 10*time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}}
}
func (s *Server) Start(ctx context.Context, onFailure func()) error {
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", s.server.Addr)
	if err != nil {
		return errors.New("HTTP listener unavailable")
	}
	base, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.server.BaseContext = func(net.Listener) context.Context { return base }
	s.mu.Lock()
	s.listener = listener
	s.done = make(chan error, 1)
	s.mu.Unlock()
	go func() {
		err := s.server.Serve(listener)
		if !errors.Is(err, http.ErrServerClosed) && err != nil {
			onFailure()
		}
		s.done <- err
		close(s.done)
	}()
	return nil
}
func (s *Server) Stop(ctx context.Context) error {
	err := s.server.Shutdown(ctx)
	if err != nil {
		_ = s.server.Close()
	}
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Lock()
	done := s.done
	s.mu.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}
func (s *Server) Address() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}
