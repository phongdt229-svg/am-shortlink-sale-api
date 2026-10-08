package legacy

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"am-shortlink-service/internal/cache"
	"am-shortlink-service/internal/domain"
	"am-shortlink-service/internal/platform/redisx"
	"am-shortlink-service/internal/store"
)

// keyAuth: tra API key có cache ngắn (30s) trong RAM; thu hồi / xoay key có hiệu lực tối đa sau 30s.
type keyAuth struct {
	st    *store.Store
	cache *cache.LRU[*domain.User]

	touchMu sync.Mutex
	touched map[string]time.Time
}

func newKeyAuth(st *store.Store) *keyAuth {
	return &keyAuth{st: st, cache: cache.NewLRU[*domain.User](10000, 30*time.Second), touched: map[string]time.Time{}}
}

// User: key → user hợp lệ (active + api_active). Không hợp lệ → store.ErrNotFound.
func (a *keyAuth) User(ctx context.Context, key string) (*domain.User, error) {
	h := store.HashAPIKey(key)
	if u, ok := a.cache.Get(h); ok {
		if u == nil {
			return nil, store.ErrNotFound
		}
		return u, nil
	}
	u, err := a.st.UserByAPIKey(ctx, key)
	if err == store.ErrNotFound || (err == nil && (!u.Active || !u.APIActive)) {
		a.cache.Set(h, nil, 5*time.Second)
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	a.cache.Set(h, u, 0)
	a.touch(key)
	return u, nil
}

// touch cập nhật last_used_at tối đa 1 lần / 10 phút / key.
func (a *keyAuth) touch(key string) {
	a.touchMu.Lock()
	last := a.touched[key]
	if time.Since(last) < 10*time.Minute {
		a.touchMu.Unlock()
		return
	}
	a.touched[key] = time.Now()
	a.touchMu.Unlock()
	go a.st.TouchAPIKey(context.Background(), key)
}

// quota: api_quota = số link API / phút (âm = không giới hạn). Redis sorted set (sliding window);
// không có Redis → đếm trên links (giống hệ cũ).
type quota struct {
	st  *store.Store
	rdb *redisx.Client
}

func (q *quota) key(u *domain.User) string { return q.rdb.Key("quota", u.Username) }

func (q *quota) Exceeded(ctx context.Context, u *domain.User) (bool, error) {
	if u.APIQuota < 0 {
		return false, nil
	}
	since := time.Now().Add(-time.Minute)
	if q.rdb != nil {
		n, err := q.rdb.ZCount(ctx, q.key(u), strconv.FormatInt(since.UnixMilli(), 10), "+inf").Result()
		if err == nil {
			return n >= int64(u.APIQuota), nil
		}
	}
	n, err := q.st.CountAPILinksSince(ctx, u.Username, since)
	if err != nil {
		return false, err
	}
	return n >= int64(u.APIQuota), nil
}

// Record ghi nhận n link vừa tạo (chỉ khi có Redis).
func (q *quota) Record(ctx context.Context, u *domain.User, n int) {
	if q.rdb == nil || n <= 0 {
		return
	}
	now := time.Now()
	k := q.key(u)
	members := make([]redis.Z, n)
	for i := range members {
		members[i] = redis.Z{Score: float64(now.UnixMilli()), Member: strconv.FormatInt(now.UnixNano(), 36) + "-" + strconv.Itoa(i)}
	}
	pipe := q.rdb.TxPipeline()
	pipe.ZAdd(ctx, k, members...)
	pipe.ZRemRangeByScore(ctx, k, "-inf", strconv.FormatInt(now.Add(-time.Minute).UnixMilli(), 10))
	pipe.Expire(ctx, k, 2*time.Minute)
	_, _ = pipe.Exec(ctx)
}
