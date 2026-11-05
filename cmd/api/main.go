package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/yumestk/CampusGo/internal/auth"
	"github.com/yumestk/CampusGo/internal/errand"
	"github.com/yumestk/CampusGo/internal/platform/config"
	"github.com/yumestk/CampusGo/internal/platform/database"
	"github.com/yumestk/CampusGo/internal/platform/idgen"
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
	worker, err := strconv.ParseInt(os.Getenv("CAMPUSGO_ID_WORKER"), 10, 64)
	if err != nil {
		return fmt.Errorf("CAMPUSGO_ID_WORKER must be explicitly set to a unique ID in [0,1023]")
	}
	ids, err := idgen.New(worker)
	if err != nil {
		return err
	}
	registry := telemetry.NewRegistry("api")
	router := httpapi.NewRouter(logger, registry)
	details := errand.NewDetailCache(cache, dbName, registry)
	refreshCtx, stopRefresh := context.WithCancel(ctx)
	refreshDone := make(chan struct{})
	go func() { defer close(refreshDone); details.RunRefresh(refreshCtx, db) }()
	defer func() { stopRefresh(); <-refreshDone }()
	httpapi.RegisterBusiness(router, &auth.Service{DB: db, Redis: cache, Config: identityConfig}, errand.Query{DB: db, Cache: details}, &errand.Publisher{DB: db, IDs: ids}, &errand.Grabber{DB: db, Redis: cache, IDs: ids, Namespace: dbName})
	return server.Run(ctx, cfg, router, logger)
}
