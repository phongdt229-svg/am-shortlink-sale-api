// consumer: đọc ClickEvent / LinkEvent từ Kafka → ghi clicks, stats_*, link_params; job total_links_snapshot.
//
// Không có KAFKA_BROKERS → chỉ chạy job snapshot (sink direct đã ghi thống kê trong redirect/api).
package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"am-shortlink-service/internal/aggregator"
	"am-shortlink-service/internal/app"
	"am-shortlink-service/internal/events"
	"am-shortlink-service/internal/platform/httpx"
	"am-shortlink-service/internal/tracking"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	a := app.Init(ctx, "am-shortlink-consumer")
	defer a.Close(context.Background())

	tr := tracking.New(a.Cfg.Tracking, a.Log)
	var t aggregator.Tracker
	if tr != nil {
		t = tr
		defer tr.Close()
	}
	agg := aggregator.New(a.Store, a.Cfg, t, a.Log)

	// health
	live, ready := httpx.Health(a.Ready)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", live)
	mux.HandleFunc("GET /readyz", ready)
	go func() {
		if err := httpx.Run(ctx, a.Log, a.Cfg.Consumer.HTTPAddr, mux); err != nil {
			a.Log.Error("health server", "err", err)
		}
	}()

	// job total_links_snapshot
	go func() {
		interval := a.Cfg.Consumer.SnapshotInterval
		if interval <= 0 {
			interval = 30 * time.Minute
		}
		run := func() {
			c, cancel := context.WithTimeout(ctx, 5*time.Minute)
			defer cancel()
			if err := agg.Snapshot(c); err != nil {
				a.Log.Error("snapshot", "err", err)
			}
		}
		run()
		tk := time.NewTicker(interval)
		defer tk.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tk.C:
				run()
			}
		}
	}()

	if len(a.Cfg.Kafka.Brokers) == 0 {
		a.Log.Info("KAFKA_BROKERS trống — chỉ chạy job snapshot")
		<-ctx.Done()
		return
	}
	k := a.Cfg.Kafka
	if err := events.RunKafkaConsumer(ctx, k.Brokers, k.GroupID, k.ClickTopic, k.LinkTopic, agg, a.Log); err != nil {
		a.Log.Error("consumer", "err", err)
		os.Exit(1)
	}
}
