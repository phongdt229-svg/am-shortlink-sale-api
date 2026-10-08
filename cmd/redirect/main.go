// redirect-svc: GET /{prefix}/{code} → 302. Đường nóng, scale ngang.
package main

import (
	"context"
	"net/http"
	"os"
	"time"

	"am-shortlink-service/internal/app"
	"am-shortlink-service/internal/linkcache"
	"am-shortlink-service/internal/platform/httpx"
	"am-shortlink-service/internal/redirect"
)

func main() {
	ctx := context.Background()
	a := app.Init(ctx, "am-shortlink-redirect")
	defer a.Close(context.Background())
	cfg := a.Cfg.Redirect

	pub, closeExtra := a.Publisher()
	links := linkcache.New(a.Store, a.Redis, cfg.LRUSize, cfg.LRUTTL, cfg.RedisTTL, cfg.NegativeTTL, a.Log)
	srv := redirect.New(links, a.Store, pub, cfg.LegacyRootRedirect, a.Log)
	if err := srv.RefreshPrefixes(ctx); err != nil {
		a.Log.Warn("load prefixes", "err", err)
	}
	bg, stop := context.WithCancel(ctx)
	go srv.RunPrefixRefresher(bg)

	live, ready := httpx.Health(a.Ready)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", live)
	mux.HandleFunc("GET /readyz", ready)
	mux.Handle("/", srv)

	err := httpx.Run(ctx, a.Log, cfg.HTTPAddr, redirect.Recover(a.Log, mux))
	stop()
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
