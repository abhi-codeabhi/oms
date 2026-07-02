// Package grpcx provides the shared Connect server (HTTP/2 over h2c) plus the
// standard interceptor stack (auth, logging, OTel, recover) and health probes.
//
// Services build a *Server with NewServer, Mount their generated Connect
// handlers, and call Run for graceful, SIGTERM-aware shutdown with /healthz and
// /readyz endpoints.
package grpcx

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/restorna/platform/pkg/config"
	"github.com/rs/zerolog/log"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

// Server wraps a net/http server speaking HTTP/2 cleartext (h2c) so Connect/gRPC
// and gRPC-Web share one port, with liveness/readiness endpoints.
type Server struct {
	base  config.Base
	mux   *http.ServeMux
	ready atomic.Bool
}

// NewServer constructs a Server from base config and wires /healthz + /readyz.
func NewServer(base config.Base) *Server {
	s := &Server{base: base, mux: http.NewServeMux()}
	s.mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	s.mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !s.ready.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("not ready"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready"))
	})
	return s
}

// Mount registers a Connect handler (path + handler from the generated
// NewXxxServiceHandler) on the server's mux.
func (s *Server) Mount(pattern string, h http.Handler) {
	s.mux.Handle(pattern, h)
}

// Run starts the h2c server and blocks until ctx is cancelled or SIGINT/SIGTERM
// is received, then shuts down gracefully (draining in-flight requests).
func (s *Server) Run(ctx context.Context) error {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM, os.Interrupt)
	defer stop()

	h2s := &http2.Server{}
	srv := &http.Server{
		Addr:              net.JoinHostPort("", s.base.Port),
		Handler:           h2c.NewHandler(s.mux, h2s),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		s.ready.Store(true)
		log.Info().Str("port", s.base.Port).Str("env", s.base.Env).Msg("grpcx: server listening")
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		s.ready.Store(false)
		log.Info().Msg("grpcx: shutdown signal received, draining")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}
