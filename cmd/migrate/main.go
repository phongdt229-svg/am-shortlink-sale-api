// migrate: tạo collection + index MongoDB theo hợp đồng schema (chạy trước khi deploy, idempotent).
package main

import (
	"context"
	"os"
	"time"

	"am-shortlink-service/internal/app"
	"am-shortlink-service/internal/store"
	"am-shortlink-service/migrations"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	a := app.Init(ctx, "am-shortlink-migrate")
	defer a.Close(context.Background())

	if err := migrations.Ensure(ctx, a.DB.Core, a.DB.Report); err != nil {
		a.Log.Error("migrate", "err", err)
		os.Exit(1)
	}
	for _, c := range []string{store.CollLinks, store.CollUsers, store.CollCampaigns} {
		if err := a.Store.EnsureCounter(ctx, c); err != nil {
			a.Log.Error("counter", "collection", c, "err", err)
			os.Exit(1)
		}
	}
	a.Log.Info("schema ready", "version", migrations.Version)
}
