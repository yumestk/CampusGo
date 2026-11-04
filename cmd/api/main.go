package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/yumestk/CampusGo/internal/auth"
	"github.com/yumestk/CampusGo/internal/errand"
	"github.com/yumestk/CampusGo/internal/platform/config"
	"github.com/yumestk/CampusGo/internal/platform/database"
	"github.com/yumestk/CampusGo/internal/platform/rediscache"
	"github.com/yumestk/CampusGo/internal/platform/server"
	"github.com/yumestk/CampusGo/internal/platform/telemetry"
	"github.com/yumestk/CampusGo/internal/transport/httpapi"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "api")
	if err := run(logger); err != nil {
		logger.Error("api failed", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load("api")
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	identityConfig, err := auth.LoadConfig()
	if err != nil {
		return err
	}
	dbName := os.Getenv("CAMPUSGO_DATABASE")
	if dbName == "" {
		dbName = "campusgo"
	}
	if dbName != "campusgo" && !(dbName == "campusgo_verification" && identityConfig.Environment == "test") {
		return fmt.Errorf("invalid database for environment")
	}
	connectCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	db, err := database.Open(connectCtx, dbName)
	if err != nil {
		return err
	}
	defer db.Close()
	cache, err := rediscache.Open()
	if err != nil {
		return err
	}
	defer cache.Close()
	gin.SetMode(gin.ReleaseMode)
	router := httpapi.NewRouter(logger, telemetry.NewRegistry("api"))
	httpapi.RegisterBusiness(router, &auth.Service{DB: db, Redis: cache, Config: identityConfig}, errand.Query{DB: db})
	return server.Run(ctx, cfg, router, logger)
}
