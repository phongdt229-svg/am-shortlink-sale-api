// api-svc: API đối tác v1/v2/v3 (tương thích hệ PHP).
package main

import (
	"context"
	"os"
	"time"

	"github.com/go-chi/chi/v5"

	"am-shortlink-service/internal/app"
	"am-shortlink-service/internal/legacy"
	"am-shortlink-service/internal/linkcache"
	"am-shortlink-service/internal/platform/httpx"
)

func main() {
	ctx := context.Background()
	a := app.Init(ctx, "am-shortlink-api")
	defer a.Close(context.Background())

	pub, closeExtra := a.Publisher()
	rc := a.Cfg.Redirect
	links := linkcache.New(a.Store, a.Redis, 1000, 5*time.Second, rc.RedisTTL, rc.NegativeTTL, a.Log)
	h := legacy.NewHandlers(a.Store, links, a.Redis, pub, a.Cfg, a.Log)

	r := chi.NewRouter()
	live, ready := httpx.Health(a.Ready)
	r.Get("/healthz", live)
	r.Get("/readyz", ready)
	r.Group(h.Routes)
	r.NotFound(legacy.NotFound)
	r.MethodNotAllowed(legacy.MethodNotAllowed)

	err := httpx.Run(ctx, a.Log, a.Cfg.API.HTTPAddr, r)
	shCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if cerr := pub.Close(shCtx); cerr != nil {
		a.Log.Warn("close publisher", "err", cerr)
	}
	closeExtra()
	if err != nil {
		a.Log.Error("server", "err", err)
		os.Exit(1)
	}
}
