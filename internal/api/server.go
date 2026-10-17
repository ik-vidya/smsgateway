// Package api, файл server.go — HTTP-сервер с graceful shutdown.
package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/ik-vidya/smsgateway/internal/config"
	"github.com/ik-vidya/smsgateway/internal/notify"
	"github.com/ik-vidya/smsgateway/internal/service"
	"github.com/ik-vidya/smsgateway/internal/storage"
)

// Server — HTTP-сервер API.
type Server struct {
	cfg       *config.Config
	store     *storage.Store
	svc       *service.Service
	notifier  notify.Notifier
	logger    *slog.Logger
	http      *http.Server
	startTime time.Time
}

// New создаёт сервер с готовыми маршрутами. notifier может быть nil.
func New(cfg *config.Config, store *storage.Store, svc *service.Service,
	notifier notify.Notifier, logger *slog.Logger) *Server {
	if notifier == nil {
		notifier = notify.Noop{}
	}
	s := &Server{
		cfg:       cfg,
		store:     store,
		svc:       svc,
		notifier:  notifier,
		logger:    logger,
		startTime: time.Now(),
	}

	writeTimeout := 5 * time.Minute
	if cfg.TestSMSTimeout > 0 {
		wt := cfg.TestSMSTimeout + 30*time.Second
		if wt > writeTimeout {
			writeTimeout = wt
		}
	}

	s.http = &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           s.routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       60 * time.Second,
	}
	return s
}

// Start запускает сервер и блокируется до Shutdown или ошибки.
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.cfg.HTTPAddr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.cfg.HTTPAddr, err)
	}
	s.logger.Info("HTTP API слушает", "addr", ln.Addr().String())

	err = s.http.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Shutdown останавливает сервер с таймаутом.
func (s *Server) Shutdown(ctx context.Context) error {
	s.logger.Info("HTTP API: shutdown")
	return s.http.Shutdown(ctx)
}

// routes собирает маршруты.
func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /readyz", s.handleReadyz)

	protected := http.NewServeMux()
	protected.HandleFunc("GET /api/v1/status", s.handleStatus)
	protected.HandleFunc("POST /api/v1/messages", s.handleSendMessage)
	protected.HandleFunc("GET /api/v1/messages", s.handleListMessages)
	protected.HandleFunc("GET /api/v1/messages/{id}", s.handleGetMessage)
	protected.HandleFunc("DELETE /api/v1/messages/{id}", s.handleDeleteMessage)
	protected.HandleFunc("POST /api/v1/test-sms", s.handleTestSMS)
	protected.HandleFunc("GET /api/v1/diagnostics", s.handleDiagnostics)

	mux.Handle("/api/v1/", s.withAuth(protected))

	return s.withRecover(s.withLogging(mux))
}
