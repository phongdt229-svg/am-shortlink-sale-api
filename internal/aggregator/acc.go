package aggregator

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// entry: một document stats_* cần cộng dồn trong lô.
type entry struct {
	coll   string
	filter bson.D // khoá unique của collection
	attrs  bson.D // trường mô tả ghi khi tạo mới (ngoài khoá)
	hasBy  bool   // có by_hour / by_* map
	hasNew bool   // có new_links / active_links
	inc    map[string]int64

	// Với stats_link_*: entry khác cần +1 active_links khi link có click (không bot) đầu tiên trong kỳ.
	detectActive bool
	deps         map[string]bool
}

// acc gom các entry của một lô theo khoá.
type acc struct {
	entries map[string]*entry
	order   []string
}

func newAcc() *acc { return &acc{entries: map[string]*entry{}} }

func (a *acc) get(coll string, filter, attrs bson.D, hasBy, hasNew bool) (*entry, string) {
	var b strings.Builder
	b.WriteString(coll)
	for _, e := range filter {
		fmt.Fprintf(&b, "|%v", e.Value)
	}
	k := b.String()
	en, ok := a.entries[k]
	if !ok {
		en = &entry{coll: coll, filter: filter, attrs: attrs, hasBy: hasBy, hasNew: hasNew, inc: map[string]int64{}}
		a.entries[k] = en
		a.order = append(a.order, k)
	}
	return en, k
}

var byMaps = []string{"by_device", "by_referer", "by_country", "by_os", "by_browser", "by_source_group", "by_prefix"}

// initDoc: $setOnInsert khi tạo document (by_hour phải là mảng 24 phần tử để $inc "by_hour.N").
func (e *entry) initDoc() bson.D {
	d := append(bson.D{}, e.attrs...)
	for _, c := range []string{"clicks", "unique_clicks", "bot_clicks", "suspicious_clicks"} {
		d = append(d, bson.E{Key: c, Value: int64(0)})
	}
	if e.hasNew {
		d = append(d, bson.E{Key: "new_links", Value: int64(0)}, bson.E{Key: "active_links", Value: int64(0)})
	}
	if e.hasBy {
		d = append(d, bson.E{Key: "by_hour", Value: make([]int64, 24)})
		for _, m := range byMaps {
			d = append(d, bson.E{Key: m, Value: bson.D{}})
		}
	}
	return d
}

func (e *entry) incDoc() bson.D {
	d := make(bson.D, 0, len(e.inc))
	for k, v := range e.inc {
		if v != 0 {
			d = append(d, bson.E{Key: k, Value: v})
		}
	}
	return d
}

// flush ghi toàn bộ lô:
//  1. Tạo document chưa có (upsert $setOnInsert) — tách khỏi $inc vì $inc "by_hour.N" cần mảng có sẵn.
//  2. stats_link_*: $inc + đọc giá trị TRƯỚC → phát hiện link chuyển 0 → ≥1 click (active_links).
//  3. Các entry còn lại: $inc theo lô.
func (a *acc) flush(ctx context.Context, report *mongo.Database) error {
	if len(a.order) == 0 {
		return nil
	}
	byColl := map[string][]*entry{}
	var colls []string
	for _, k := range a.order {
		e := a.entries[k]
		if _, ok := byColl[e.coll]; !ok {
			colls = append(colls, e.coll)
		}
		byColl[e.coll] = append(byColl[e.coll], e)
	}

	// 1. khởi tạo
	for _, c := range colls {
		models := make([]mongo.WriteModel, 0, len(byColl[c]))
		for _, e := range byColl[c] {
			models = append(models, mongo.NewUpdateOneModel().SetFilter(e.filter).
				SetUpdate(bson.D{{Key: "$setOnInsert", Value: e.initDoc()}}).SetUpsert(true))
		}
		if _, err := report.Collection(c).BulkWrite(ctx, models, options.BulkWrite().SetOrdered(false)); err != nil {
			return fmt.Errorf("init %s: %w", c, err)
		}
	}

	// 2. link-level + phát hiện active
	var (
		mu     sync.Mutex
		active []*entry
		wg     sync.WaitGroup
		errMu  sync.Mutex
		first  error
	)
	sem := make(chan struct{}, 16)
	for _, k := range a.order {
		e := a.entries[k]
		if !e.detectActive || len(e.incDoc()) == 0 {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(e *entry) {
			defer wg.Done()
			defer func() { <-sem }()
			var before struct {
				Clicks int64 `bson:"clicks"`
			}
			err := report.Collection(e.coll).FindOneAndUpdate(ctx, e.filter,
				bson.D{{Key: "$inc", Value: e.incDoc()}},
				options.FindOneAndUpdate().SetReturnDocument(options.Before).
					SetProjection(bson.D{{Key: "clicks", Value: 1}})).Decode(&before)
			if err != nil {
				errMu.Lock()
				if first == nil {
					first = fmt.Errorf("inc %s: %w", e.coll, err)
				}
				errMu.Unlock()
				return
			}
			if before.Clicks == 0 && e.inc["clicks"] > 0 {
				mu.Lock()
				active = append(active, e)
				mu.Unlock()
			}
		}(e)
	}
	wg.Wait()
	if first != nil {
		return first
	}
	for _, e := range active {
		for dk := range e.deps {
			if d, ok := a.entries[dk]; ok {
				d.inc["active_links"]++
			}
		}
	}

	// 3. phần còn lại
	for _, c := range colls {
		var models []mongo.WriteModel
		for _, e := range byColl[c] {
			if e.detectActive {
				continue
			}
			inc := e.incDoc()
			if len(inc) == 0 {
				continue
			}
			models = append(models, mongo.NewUpdateOneModel().SetFilter(e.filter).
				SetUpdate(bson.D{{Key: "$inc", Value: inc}}))
		}
		if len(models) == 0 {
			continue
		}
		if _, err := report.Collection(c).BulkWrite(ctx, models, options.BulkWrite().SetOrdered(false)); err != nil {
			return fmt.Errorf("inc %s: %w", c, err)
		}
	}
	return nil
}
