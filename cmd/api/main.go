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

	mysqlstore "github.com/yumestk/CampusGo/internal/adapter/mysql"
	"github.com/yumestk/CampusGo/internal/application"
	"github.com/yumestk/CampusGo/internal/auth"
	"github.com/yumestk/CampusGo/internal/fundmq"
	"github.com/yumestk/CampusGo/internal/notify"
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
	details := mysqlstore.NewDetailCache(cache, dbName, registry)
	refreshCtx, stopRefresh := context.WithCancel(ctx)
	refreshDone := make(chan struct{})
	go func() { defer close(refreshDone); details.RunRefresh(refreshCtx, db) }()
	defer func() { stopRefresh(); <-refreshDone }()
	g := &mysqlstore.TaskStore{DB: db, Redis: cache, IDs: ids, Namespace: dbName, Limits: mysqlstore.NewGrabLimits(cache, dbName)}
	identity := &auth.Service{DB: db, Redis: cache, Config: identityConfig}
	hub := notify.NewHub(identity, dbName)
	hubCtx, stopHub := context.WithCancel(ctx)
	hubDone := make(chan struct{})
	go func() { defer close(hubDone); hub.Run(hubCtx) }()
	defer func() { stopHub(); <-hubDone }()
	mq := &fundmq.Client{Namespace: dbName}
	funds := &application.Funds{Store: &mysqlstore.FundStore{DB: db, IDs: ids}, Send: mq.Send, Record: mysqlstore.NewFundRecorder(registry)}
	pushCtx, stopPush := context.WithCancel(ctx)
	pushDone := make(chan struct{})
	go func() {
		defer close(pushDone)
		fundmq.RunPush(pushCtx, funds, os.Getenv("CAMPUSGO_MQ_ENDPOINT"), dbName, func(c context.Context, e mysqlstore.FundEvent) error {
			for user, content := range application.FundNotices(e) {
				if x := hub.Publish(c, user, notify.Event{Type: "notification.new", Payload: map[string]string{"errandId": strconv.FormatInt(e.ErrandID, 10), "noticeType": e.Kind, "content": content}}); x != nil {
					return x
				}
			}
			return nil
		}, func(e error) { logger.Error("fund push unavailable", "cause_type", fmt.Sprintf("%T", e)) })
	}()
	defer func() { stopPush(); <-pushDone }()
	mqCtx, stopMQ := context.WithCancel(ctx)
	mqDone := make(chan struct{})
	go func() {
		defer close(mqDone)
		mq.Run(mqCtx, funds, os.Getenv("CAMPUSGO_MQ_ENDPOINT"), false, func(e error) { logger.Error("fund MQ operation failed", "cause_type", fmt.Sprintf("%T", e)) })
	}()
	defer func() { stopMQ(); <-mqDone }()
	httpapi.RegisterRealtime(router, identity, hub)
	grab, err := application.NewGrab(g, g, g)
	if err != nil {
		return err
	}
	httpapi.RegisterBusiness(router, identity, mysqlstore.Query{DB: db, Cache: details}, httpapi.BusinessServices{Notifications: &application.Notifications{Store: notify.ReadStore{DB: db}}, Publisher: &application.Publisher{Store: &mysqlstore.PublishStore{DB: db, IDs: ids}}, Grab: grab, Lifecycle: &application.Lifecycle{Store: g}, Funds: funds, Credit: &mysqlstore.CreditQuery{DB: g.DB, Redis: g.Redis, Namespace: g.Namespace}})
	return server.Run(ctx, cfg, router, logger)
}
