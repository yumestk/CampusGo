package server

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/yumestk/CampusGo/internal/platform/config"
)

func Run(ctx context.Context, cfg config.Config, handler http.Handler, logger *slog.Logger) error {
	listener, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return err
	}
	logger.Info("server started", "service", cfg.Service, "addr", listener.Addr().String())
	return serve(ctx, listener, handler, cfg.ShutdownTimeout, logger)
}

func serve(ctx context.Context, listener net.Listener, handler http.Handler, timeout time.Duration, logger *slog.Logger) error {
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}
	defer srv.Close()
	result := make(chan error, 1)
	go func() { result <- srv.Serve(listener) }()
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		logger.Info("server draining")
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	err := srv.Shutdown(shutdownCtx)
	if err != nil {
		err = errors.Join(err, srv.Close())
	}
	serveErr := <-result
	if !errors.Is(serveErr, http.ErrServerClosed) {
		err = errors.Join(err, serveErr)
	}
	logger.Info("server stopped", "error", err)
	return err
}

func Liveness(service string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","service":"` + service + `"}`))
	}
}
