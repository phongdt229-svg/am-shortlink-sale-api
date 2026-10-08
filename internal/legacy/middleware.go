package legacy

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"am-shortlink-service/internal/domain"
	"am-shortlink-service/internal/platform/httpx"
	"am-shortlink-service/internal/store"
)

type ctxKey int

const (
	ctxUser ctxKey = iota
	ctxInput
)

func userFrom(r *http.Request) *domain.User {
	u, _ := r.Context().Value(ctxUser).(*domain.User)
	return u
}

func inputFrom(r *http.Request) Input {
	in, _ := r.Context().Value(ctxInput).(Input)
	if in == nil {
		in = Input{}
	}
	return in
}

// withInput đọc tham số một lần cho cả chuỗi middleware + handler.
func withInput(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		in, err := parseInput(r)
		if err != nil {
			fail(w, "Invalid or missing parameters.", nil)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxInput, in)))
	})
}

// apiAuth: middleware "api" — tham số `key` → api_keys.key_hash → users (active, api_active) + quota.
func (h *Handlers) apiAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimSpace(inputFrom(r).Str("key"))
		if key == "" {
			fail(w, "Authentication token required.", nil)
			return
		}
		u, err := h.auth.User(r.Context(), key)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				fail(w, "Authentication token invalid.", nil)
				return
			}
			h.internalError(w, r, err)
			return
		}
		exceeded, err := h.quota.Exceeded(r.Context(), u)
		if err != nil {
			h.log.Warn("quota check", "err", err) // lỗi đếm quota → cho qua (fail-open)
		} else if exceeded {
			fail(w, "Quota exceeded.", nil)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxUser, u)))
	})
}

// checkIP: whitelist ALLOW_IP (IP hoặc CIDR). Trống = cho qua.
func (h *Handlers) checkIP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(h.allowNets) == 0 || ipAllowed(httpx.ClientIP(r), h.allowNets) {
			next.ServeHTTP(w, r)
			return
		}
		fail(w, "This page could not be found.", nil)
	})
}

func parseNets(xs []string) []*net.IPNet {
	var out []*net.IPNet
	for _, x := range xs {
		x = strings.TrimSpace(x)
		if x == "" {
			continue
		}
		if !strings.Contains(x, "/") {
			if strings.Contains(x, ":") {
				x += "/128"
			} else {
				x += "/32"
			}
		}
		if _, n, err := net.ParseCIDR(x); err == nil {
			out = append(out, n)
		}
	}
	return out
}

func ipAllowed(ip string, nets []*net.IPNet) bool {
	p := net.ParseIP(ip)
	if p == nil {
		return false
	}
	for _, n := range nets {
		if n.Contains(p) {
			return true
		}
	}
	return false
}

// forwardedHost: X-Forwarded-Host (nếu có) phải thuộc danh sách cho phép + domains.is_active (D11: gộp đúng).
func (h *Handlers) forwardedHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fh := strings.TrimSpace(r.Header.Get("X-Forwarded-Host"))
		if fh == "" || h.hostAllowed(r.Context(), fh) {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("Forbidden"))
	})
}

func (h *Handlers) hostAllowed(ctx context.Context, host string) bool {
	host = strings.ToLower(host)
	if h.fwdHosts[host] {
		return true
	}
	h.domMu.Lock()
	defer h.domMu.Unlock()
	if time.Since(h.domLoaded) > time.Minute {
		if ds, err := h.st.ActiveDomains(ctx); err == nil {
			m := map[string]bool{}
			for _, d := range ds {
				m[strings.ToLower(d.DomainName)] = true
			}
			h.domains = m
		} else {
			h.log.Warn("load domains", "err", err)
		}
		h.domLoaded = time.Now()
	}
	return h.domains[host]
}

// rateLimit: giới hạn request / IP / giây cho shorten (D8 — hệ cũ comment mất phần chặn).
type rateLimiter struct {
	limit int
	mu    sync.Mutex
	sec   int64
	count map[string]int
}

func newRateLimiter(limit int) *rateLimiter {
	return &rateLimiter{limit: limit, count: map[string]int{}}
}

func (rl *rateLimiter) allow(ip string) bool {
	if rl.limit <= 0 {
		return true
	}
	now := time.Now().Unix()
	rl.mu.Lock()
	defer rl.mu.Unlock()
	if now != rl.sec {
		rl.sec = now
		clear(rl.count)
	}
	rl.count[ip]++
	return rl.count[ip] <= rl.limit
}

func (h *Handlers) rateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !h.rl.allow(httpx.ClientIP(r)) {
			writeJSON(w, http.StatusTooManyRequests, envelope{Error: 1, ErrorDescription: "Too Many Requests", Data: nil})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// recoverer: panic → JSON lỗi chung, không lộ chi tiết (D9).
func recoverer(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if v := recover(); v != nil {
					log.Error("panic", "value", v, "path", r.URL.Path)
					fail(w, "Có lỗi xảy ra, Vui lòng thử lại", nil)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

func (h *Handlers) internalError(w http.ResponseWriter, r *http.Request, err error) {
	h.log.Error("internal error", "err", err, "path", r.URL.Path)
	fail(w, "Có lỗi xảy ra, Vui lòng thử lại", nil)
}
