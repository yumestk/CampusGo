package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/gin-gonic/gin"
	"github.com/yumestk/CampusGo/internal/platform/config"
	"github.com/yumestk/CampusGo/internal/platform/server"
	"github.com/yumestk/CampusGo/internal/transport/httpapi"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "api")
	cfg, err := config.Load("api")
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	gin.SetMode(gin.ReleaseMode)
	if err := server.Run(ctx, cfg, httpapi.NewRouter(), logger); err != nil {
		logger.Error("server failed", "error", err)
		os.Exit(1)
	}
}
