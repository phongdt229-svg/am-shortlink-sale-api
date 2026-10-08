// Package linkcache: tra link theo mã qua 3 tầng LRU (RAM) → Redis → MongoDB, có negative cache.
//
// API gọi Invalidate khi tạo / sửa / xoá link: xoá khoá Redis; LRU của các pod redirect tự hết hạn
// sau REDIRECT_LRU_TTL (mặc định 60s).
package linkcache

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"

	"am-shortlink-service/internal/cache"
	"am-shortlink-service/internal/domain"
	"am-shortlink-service/internal/events"
	"am-shortlink-service/internal/platform/redisx"
	"am-shortlink-service/internal/store"
)

// Entry: dữ liệu link đủ để redirect + phát ClickEvent.
type Entry struct {
	Found  bool                `json:"f"`
	Status string              `json:"s,omitempty"`
	Link   events.LinkSnapshot `json:"l"`
}

type Cache struct {
	st       *store.Store
	rdb      *redisx.Client
	lru      *cache.LRU[Entry]
	redisTTL time.Duration
	negTTL   time.Duration
	log      *slog.Logger
}

func New(st *store.Store, rdb *redisx.Client, lruSize int, lruTTL, redisTTL, negTTL time.Duration, log *slog.Logger) *Cache {
	return &Cache{st: st, rdb: rdb, lru: cache.NewLRU[Entry](lruSize, lruTTL), redisTTL: redisTTL, negTTL: negTTL, log: log}
}

func (c *Cache) key(code string) string { return c.rdb.Key("link", code) }

// Get trả Entry (Found=false nếu không có mã). Lỗi chỉ khi MongoDB lỗi.
func (c *Cache) Get(ctx context.Context, code string) (Entry, error) {
	if e, ok := c.lru.Get(code); ok {
		return e, nil
	}
	if c.rdb != nil {
		b, err := c.rdb.Get(ctx, c.key(code)).Bytes()
		switch {
		case err == nil:
			var e Entry
			if json.Unmarshal(b, &e) == nil {
				c.lru.Set(code, e, c.ttlFor(e))
				return e, nil
			}
		case !errors.Is(err, redis.Nil):
			c.log.Warn("redis get link", "err", err) // Redis lỗi → đọc MongoDB (fail-open)
		}
	}
	l, err := c.st.LinkByCode(ctx, code)
	var e Entry
	switch {
	case errors.Is(err, store.ErrNotFound):
		e = Entry{Found: false}
	case err != nil:
		return Entry{}, err
	default:
		e = Entry{Found: true, Status: l.Status, Link: events.SnapshotOf(l)}
	}
	c.lru.Set(code, e, c.ttlFor(e))
	if c.rdb != nil {
		if b, err := json.Marshal(e); err == nil {
			ttl := c.redisTTL
			if !e.Found {
				ttl = c.negTTL
			}
			if err := c.rdb.Set(ctx, c.key(code), b, ttl).Err(); err != nil {
				c.log.Warn("redis set link", "err", err)
			}
		}
	}
	return e, nil
}

func (c *Cache) ttlFor(e Entry) time.Duration {
	if !e.Found {
		return c.negTTL
	}
	return 0 // TTL mặc định của LRU
}

// Invalidate xoá cache của mã (gọi sau khi tạo / sửa / đổi trạng thái link).
func (c *Cache) Invalidate(ctx context.Context, codes ...string) {
	for _, code := range codes {
		c.lru.Delete(code)
	}
	if c.rdb == nil || len(codes) == 0 {
		return
	}
	keys := make([]string, len(codes))
	for i, code := range codes {
		keys[i] = c.key(code)
	}
	if err := c.rdb.Del(ctx, keys...).Err(); err != nil {
		c.log.Warn("redis del link", "err", err)
	}
}

// IsRedirectable: link đang hoạt động (deleted / disabled → 404, như hệ cũ).
func (e Entry) IsRedirectable() bool { return e.Found && e.Status == domain.StatusActive }
