// Package redirect: đường nóng GET /{prefix}/{code} → 302 long_url.
//
// Nguyên tắc (§3): không ghi DB đồng bộ, không gọi API ngoài. Tra link qua linkcache, phát ClickEvent
// bất đồng bộ. Mọi trường hợp không redirect được → trang HTML 404 (giữ hành vi hệ cũ).
package redirect

import (
	"context"
	_ "embed"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"am-shortlink-service/internal/events"
	"am-shortlink-service/internal/linkcache"
	"am-shortlink-service/internal/platform/httpx"
	"am-shortlink-service/internal/store"
)

//go:embed pages/404.html
var page404 []byte

//go:embed pages/500.html
var page500 []byte

const maxCodeLen = 64

type Server struct {
	links      *linkcache.Cache
	st         *store.Store
	pub        events.Publisher
	log        *slog.Logger
	legacyRoot bool

	mu       sync.RWMutex
	prefixes map[string]bool
}

func New(links *linkcache.Cache, st *store.Store, pub events.Publisher, legacyRoot bool, log *slog.Logger) *Server {
	return &Server{links: links, st: st, pub: pub, log: log, legacyRoot: legacyRoot,
		prefixes: map[string]bool{"sale": true, "lm": true}}
}

// RefreshPrefixes nạp danh sách prefix từ collection prefixes (thêm prefix không cần deploy — D25).
func (s *Server) RefreshPrefixes(ctx context.Context) error {
	ps, err := s.st.ActivePrefixes(ctx)
	if err != nil {
		return err
	}
	if len(ps) == 0 {
		return nil // collection trống → giữ mặc định sale, lm
	}
	m := make(map[string]bool, len(ps))
	for _, p := range ps {
		m[p.ID] = true
	}
	s.mu.Lock()
	s.prefixes = m
	s.mu.Unlock()
	return nil
}

// RunPrefixRefresher làm mới prefix mỗi phút tới khi ctx huỷ.
func (s *Server) RunPrefixRefresher(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.RefreshPrefixes(ctx); err != nil {
				s.log.Warn("refresh prefixes", "err", err)
			}
		}
	}
}

func (s *Server) isPrefix(p string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.prefixes[p]
}

// ServeHTTP: /{prefix}/{code} hoặc /{code} (legacy, khi proxy cắt prefix).
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		s.notFound(w, r) // giữ HTML; method lạ không lộ thông tin
		return
	}
	path := strings.Trim(r.URL.EscapedPath(), "/")
	segs := strings.Split(path, "/")
	var prefix, code string
	switch {
	case path == "":
		s.notFound(w, r)
		return
	case len(segs) == 2 && s.isPrefix(segs[0]):
		prefix, code = segs[0], segs[1]
	case len(segs) == 1 && s.legacyRoot && !s.isPrefix(segs[0]):
		code = segs[0] // /{code}; /sale, /lm (thiếu mã) rơi xuống 404
	default:
		s.notFound(w, r)
		return
	}
	code, err := url.PathUnescape(code)
	if err != nil {
		s.notFound(w, r)
		return
	}
	code = strings.TrimSpace(code)
	if code == "" || len(code) > maxCodeLen {
		s.notFound(w, r)
		return
	}

	e, err := s.links.Get(r.Context(), code)
	if err != nil {
		s.log.Error("lookup link", "err", err, "code", code)
		s.serverError(w)
		return
	}
	if !e.IsRedirectable() {
		s.notFound(w, r)
		return
	}

	if r.Method == http.MethodGet {
		s.pub.PublishClick(r.Context(), events.ClickEvent{
			EventID:      uuid.NewString(),
			TS:           time.Now().UTC(),
			IP:           httpx.ClientIP(r),
			UserAgent:    r.UserAgent(),
			Referer:      r.Referer(),
			AccessPrefix: prefix,
			Link:         e.Link,
		})
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer-when-downgrade")
	http.Redirect(w, r, e.Link.LongURL, http.StatusFound)
}

func (s *Server) notFound(w http.ResponseWriter, _ *http.Request) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write(page404)
}

func (s *Server) serverError(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusInternalServerError)
	_, _ = w.Write(page500)
}

// Recover: panic → trang HTML 500, không lộ stack trace (D9).
func Recover(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				log.Error("panic", "value", v, "path", r.URL.Path)
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write(page500)
			}
		}()
		next.ServeHTTP(w, r)
	})
}
