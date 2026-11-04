package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/yumestk/CampusGo/internal/platform/config"
	"github.com/yumestk/CampusGo/internal/platform/server"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "worker")
	cfg, err := config.Load("worker")
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	router := http.NewServeMux()
	router.HandleFunc("GET /livez", server.Liveness("worker"))
	if err := server.Run(ctx, cfg, router, logger); err != nil {
		logger.Error("server failed", "error", err)
		os.Exit(1)
	}
}
