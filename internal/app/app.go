// Package app: khởi tạo phụ thuộc dùng chung cho các binary (config, log, Mongo, Redis, publisher).
package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"am-shortlink-service/internal/aggregator"
	"am-shortlink-service/internal/config"
	"am-shortlink-service/internal/events"
	"am-shortlink-service/internal/platform/logger"
	"am-shortlink-service/internal/platform/mongodb"
	"am-shortlink-service/internal/platform/redisx"
	"am-shortlink-service/internal/store"
	"am-shortlink-service/internal/tracking"
)

type App struct {
	Cfg   *config.Config
	Log   *slog.Logger
	DB    *mongodb.DB
	Store *store.Store
	Redis *redisx.Client
}

// Init đọc cấu hình, kết nối MongoDB (+ Redis nếu cấu hình). Lỗi → in ra stderr và thoát.
func Init(ctx context.Context, service string) *App {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	log := logger.New(cfg.LogLevel, service, cfg.AppEnv)
	db, err := mongodb.Connect(ctx, cfg.Mongo, service)
	if err != nil {
		log.Error("mongodb", "err", err)
		os.Exit(1)
	}
	rdb, err := redisx.Connect(ctx, cfg.Redis)
	if err != nil {
		// Redis là cache: lỗi kết nối lúc khởi động → chạy không Redis (fail-open), ghi cảnh báo.
		log.Warn("redis unavailable, running without redis", "err", err)
		rdb = nil
	}
	return &App{Cfg: cfg, Log: log, DB: db, Store: store.New(db), Redis: rdb}
}

func (a *App) Close(ctx context.Context) {
	if a.Redis != nil {
		_ = a.Redis.Close()
	}
	_ = a.DB.Close(ctx)
}

// Ready: kiểm MongoDB cho /readyz.
func (a *App) Ready(ctx context.Context) error { return a.DB.Ping(ctx) }

// Publisher theo CLICK_SINK. Sink direct chạy aggregator ngay trong tiến trình (dev / không Kafka).
func (a *App) Publisher() (events.Publisher, func()) {
	switch a.Cfg.Redirect.ClickSink {
	case "kafka":
		p, err := events.NewKafkaPublisher(a.Cfg.Kafka.Brokers, a.Cfg.Kafka.ClickTopic, a.Cfg.Kafka.LinkTopic, a.Log)
		if err != nil {
			a.Log.Error("kafka publisher", "err", err)
			os.Exit(1)
		}
		return p, func() {}
	case "log":
		return events.Nop{Log: a.Log}, func() {}
	default:
		tr := tracking.New(a.Cfg.Tracking, a.Log)
		var t aggregator.Tracker
		if tr != nil {
			t = tr
		}
		agg := aggregator.New(a.Store, a.Cfg, t, a.Log)
		return events.NewDirect(agg, a.Log, a.Cfg.Redirect.ClickBuffer), func() {
			if tr != nil {
				tr.Close()
			}
		}
	}
}
