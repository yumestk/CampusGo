package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/yumestk/CampusGo/internal/errand"
	"github.com/yumestk/CampusGo/internal/fundmq"
	"github.com/yumestk/CampusGo/internal/platform/config"
	"github.com/yumestk/CampusGo/internal/platform/database"
	"github.com/yumestk/CampusGo/internal/platform/idgen"
	"github.com/yumestk/CampusGo/internal/platform/rediscache"
	"github.com/yumestk/CampusGo/internal/platform/server"
	"github.com/yumestk/CampusGo/internal/platform/telemetry"
	"github.com/yumestk/CampusGo/internal/timeoutmq"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "worker")
	if err := run(logger); err != nil {
		logger.Error("worker failed", "error", err)
		os.Exit(1)
	}
}
func run(logger *slog.Logger) error {
	cfg, err := config.Load("worker")
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	name := os.Getenv("CAMPUSGO_DATABASE")
	if name == "" {
		name = "campusgo"
	}
	if name != "campusgo" && !(name == "campusgo_verification" && os.Getenv("CAMPUSGO_ENV") == "test") {
		return fmt.Errorf("invalid database for environment")
	}
	connect, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	db, err := database.Open(connect, name)
	if err != nil {
		return err
	}
	defer db.Close()
	cache, err := rediscache.Open()
	if err != nil {
		return err
	}
	defer cache.Close()
	workerID, err := strconv.ParseInt(os.Getenv("CAMPUSGO_WORKER_ID_WORKER"), 10, 64)
	if err != nil {
		return fmt.Errorf("CAMPUSGO_WORKER_ID_WORKER must be an explicit unique ID")
	}
	ids, err := idgen.New(workerID)
	if err != nil {
		return err
	}
	g := &errand.Grabber{DB: db, Redis: cache, IDs: ids, Namespace: name}
	registry := telemetry.NewRegistry("worker")
	mq := &fundmq.Client{Namespace: name}
	funds := &errand.Funds{Grabber: g, Send: mq.Send, Events: errand.NewFundMetrics(registry)}
	var workers sync.WaitGroup
	onError := func(err error) { logger.Error("worker operation failed", "cause_type", fmt.Sprintf("%T", err)) }
	for _, run := range []func(){func() { mq.Run(ctx, funds, os.Getenv("CAMPUSGO_MQ_ENDPOINT"), true, onError) }, func() { funds.Run(ctx, onError) }, func() { g.RunRecovery(ctx, onError) }, func() { g.RunTimeoutScan(ctx, onError) }, func() { timeoutmq.Run(ctx, g, os.Getenv("CAMPUSGO_MQ_ENDPOINT"), onError, funds) }} {
		workers.Add(1)
		go func() { defer workers.Done(); run() }()
	}
	router := http.NewServeMux()
	router.HandleFunc("GET /livez", server.Liveness("worker"))
	router.Handle("GET /metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
	err = server.Run(ctx, cfg, router, logger)
	stop()
	workers.Wait()
	return err
}
