package aggregator

import (
	"context"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"am-shortlink-service/internal/store"
	"am-shortlink-service/internal/urlparams"
)

// registry: cache param_registry (Portal admin bật/tắt theo dõi tham số), làm mới định kỳ.
type registry struct {
	db      *store.Store
	refresh time.Duration

	mu     sync.RWMutex
	rules  map[string]urlparams.Rule
	loaded time.Time
}

func newRegistry(db *store.Store, refresh time.Duration) *registry {
	if refresh <= 0 {
		refresh = time.Minute
	}
	return &registry{db: db, refresh: refresh}
}

// Rules trả luật hiện tại (lỗi đọc → giữ bản cũ / DefaultRules).
func (r *registry) Rules(ctx context.Context) map[string]urlparams.Rule {
	r.mu.RLock()
	rules, fresh := r.rules, time.Since(r.loaded) < r.refresh
	r.mu.RUnlock()
	if rules != nil && fresh {
		return rules
	}
	loaded, err := r.load(ctx)
	r.mu.Lock()
	defer r.mu.Unlock()
	if err == nil {
		r.rules = loaded
	}
	r.loaded = time.Now() // lỗi cũng chờ chu kỳ sau, tránh dồn truy vấn
	if r.rules == nil {
		return urlparams.DefaultRules
	}
	return r.rules
}

func (r *registry) load(ctx context.Context) (map[string]urlparams.Rule, error) {
	cur, err := r.db.DB().Report.Collection(store.CollParamRegistry).Find(ctx, bson.D{})
	if err != nil {
		return nil, err
	}
	var docs []struct {
		Key       string   `bson:"key"`
		Tracked   bool     `bson:"tracked"`
		PII       string   `bson:"pii"`
		Normalize []string `bson:"normalize"`
		Status    string   `bson:"status"`
	}
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	out := make(map[string]urlparams.Rule, len(docs)+len(urlparams.DefaultRules))
	for k, v := range urlparams.DefaultRules {
		out[k] = v
	}
	for _, d := range docs {
		out[d.Key] = urlparams.Rule{
			// high_cardinality / disabled: không cộng stats_param_* (vẫn lọc được qua link_params).
			Tracked:   d.Tracked && (d.Status == "" || d.Status == "active"),
			PII:       d.PII,
			Normalize: d.Normalize,
		}
	}
	return out, nil
}
