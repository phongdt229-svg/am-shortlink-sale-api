// Package aggregator: xử lý ClickEvent / LinkEvent → ghi MongoDB theo hợp đồng schema với Portal.
//
// Định nghĩa chỉ số (§5.2):
//   - clicks: lượt redirect KHÔNG phải bot. by_* chỉ tính click không bot.
//   - unique_clicks: khách duy nhất / link / ngày, khách = hash(ip|user_agent).
//   - bot_clicks: tách riêng. suspicious_clicks: click thứ > N của 1 IP / link / giờ.
//   - active_links: link có ≥ 1 click (không bot) trong kỳ. new_links: link tạo trong kỳ.
//
// Idempotent theo event_id (click_event_ids, TTL 3 ngày): replay từ Kafka không cộng trùng.
package aggregator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"am-shortlink-service/internal/analytics"
	"am-shortlink-service/internal/config"
	"am-shortlink-service/internal/domain"
	"am-shortlink-service/internal/events"
	"am-shortlink-service/internal/store"
	"am-shortlink-service/internal/urlparams"
)

// Tracker: gọi hệ ngoài (TrackingApi / FMI) cho click — chạy bất đồng bộ, lỗi không ảnh hưởng thống kê.
type Tracker interface {
	Track(e events.ClickEvent)
}

type Aggregator struct {
	st      *store.Store
	core    *mongo.Database
	report  *mongo.Database
	reg     *registry
	salt    string
	suspN   int
	tracker Tracker
	log     *slog.Logger
}

func New(st *store.Store, cfg *config.Config, tracker Tracker, log *slog.Logger) *Aggregator {
	return &Aggregator{
		st: st, core: st.DB().Core, report: st.DB().Report,
		reg: newRegistry(st, cfg.Consumer.RegistryRefresh), salt: cfg.PIIHashSalt,
		suspN: cfg.Consumer.SuspiciousThreshold, tracker: tracker, log: log,
	}
}

// enriched: click sau khi làm giàu.
type enriched struct {
	e          events.ClickEvent
	ua         analytics.UA
	refHost    string
	source     string
	date       time.Time
	month      time.Time
	hour       int
	weekday    int
	repeat     bool
	suspicious bool
	parsed     urlparams.Parsed
}

// HandleClicks xử lý một lô click.
func (a *Aggregator) HandleClicks(ctx context.Context, es []events.ClickEvent) error {
	if len(es) == 0 {
		return nil
	}
	fresh, err := a.markProcessed(ctx, es)
	if err != nil {
		return err
	}
	if len(fresh) == 0 {
		return nil
	}
	if err := a.processClicks(ctx, fresh); err != nil {
		// Bỏ đánh dấu để lần đọc lại xử lý lại lô (rủi ro cộng trùng phần đã ghi — job đối soát sửa).
		a.unmark(ctx, fresh)
		return err
	}
	return nil
}

func (a *Aggregator) processClicks(ctx context.Context, es []events.ClickEvent) error {
	rules := a.reg.Rules(ctx)
	cs := make([]*enriched, len(es))
	for i, e := range es {
		ts := e.TS.UTC()
		vn := ts.In(analytics.VN)
		c := &enriched{e: e, ua: analytics.ParseUA(e.UserAgent), refHost: analytics.RefererHost(e.Referer)}
		c.source = analytics.SourceGroup(c.refHost, c.ua.InApp)
		c.date = analytics.DateOf(ts)
		c.month = analytics.MonthOf(c.date)
		c.hour, c.weekday = vn.Hour(), int(vn.Weekday())
		c.parsed = urlparams.Parse(e.Link.LongURL, rules, a.salt)
		cs[i] = c
	}
	if err := a.flagRepeat(ctx, cs); err != nil {
		return err
	}
	if err := a.flagSuspicious(ctx, cs); err != nil {
		return err
	}
	if err := a.insertClicks(ctx, cs); err != nil {
		return err
	}

	ac := newAcc()
	linkClicks := map[int64]int64{}
	facets := map[string]*facetInc{}
	paramVals := map[string]*paramValInc{}
	for _, c := range cs {
		a.addClick(ac, c, rules)
		if c.ua.IsBot {
			continue
		}
		linkClicks[c.e.Link.ID]++
		l := c.e.Link
		for f, v := range map[string]string{
			"device": c.ua.Device, "os": c.ua.OS, "browser": c.ua.Browser, "source_group": c.source,
			"referer_host": c.refHost, "access_prefix": c.e.AccessPrefix, "link_api_version": l.APIVersion,
			"dest_host": l.DestHost,
		} {
			if v == "" {
				continue
			}
			k := l.Owner + "|" + f + "|" + v
			fi, ok := facets[k]
			if !ok {
				fi = &facetInc{owner: l.Owner, field: f, value: v}
				facets[k] = fi
			}
			fi.n++
			if c.date.After(fi.last) {
				fi.last = c.date
			}
		}
		for _, p := range c.parsed.Params {
			k := l.Owner + "|" + p.Key + "|" + p.Value
			pv, ok := paramVals[k]
			if !ok {
				pv = &paramValInc{owner: l.Owner, key: p.Key, value: p.Value}
				paramVals[k] = pv
			}
			pv.n++
		}
	}
	if err := ac.flush(ctx, a.report); err != nil {
		return err
	}
	if err := a.st.IncLinkClicks(ctx, linkClicks); err != nil {
		return fmt.Errorf("links.clicks: %w", err)
	}
	if err := a.writeFacets(ctx, facets); err != nil {
		return err
	}
	if err := a.writeParamValueClicks(ctx, paramVals); err != nil {
		return err
	}
	if a.tracker != nil {
		for _, c := range cs {
			if !c.ua.IsBot {
				a.tracker.Track(c.e)
			}
		}
	}
	return nil
}

// addClick cộng một click vào mọi document thống kê liên quan.
func (a *Aggregator) addClick(ac *acc, c *enriched, rules map[string]urlparams.Rule) {
	l := c.e.Link
	for _, monthly := range []bool{false, true} {
		period, pk, suffix := c.date, "date", "_daily"
		if monthly {
			period, pk, suffix = c.month, "month", "_monthly"
		}
		linkE, _ := ac.get("stats_link"+suffix,
			bson.D{{Key: "link_id", Value: l.ID}, {Key: pk, Value: period}},
			bson.D{{Key: "owner", Value: l.Owner}, {Key: "campaign_code", Value: l.Campaign}, {Key: "ctv_id", Value: l.CTVID}, {Key: "prefix", Value: l.Prefix}},
			true, false)
		linkE.detectActive = true
		if linkE.deps == nil {
			linkE.deps = map[string]bool{}
		}
		targets := []*entry{linkE}
		add := func(e *entry, k string) {
			targets = append(targets, e)
			linkE.deps[k] = true
		}
		e, k := ac.get("stats_campaign"+suffix,
			bson.D{{Key: "owner", Value: l.Owner}, {Key: "campaign_code", Value: l.Campaign}, {Key: pk, Value: period}}, nil, true, true)
		add(e, k)
		e, k = ac.get("stats_ctv"+suffix,
			bson.D{{Key: "ctv_id", Value: l.CTVID}, {Key: "owner", Value: l.Owner}, {Key: "campaign_code", Value: l.Campaign}, {Key: pk, Value: period}}, nil, false, true)
		add(e, k)
		e, k = ac.get("stats_owner"+suffix, bson.D{{Key: "owner", Value: l.Owner}, {Key: pk, Value: period}}, nil, true, true)
		add(e, k)
		e, k = ac.get("stats_system"+suffix, bson.D{{Key: pk, Value: period}}, nil, true, true)
		add(e, k)
		for _, p := range c.parsed.Params {
			if !rules[p.Key].Tracked {
				continue
			}
			e, k = ac.get("stats_param"+suffix, bson.D{
				{Key: "owner", Value: l.Owner}, {Key: "campaign_code", Value: l.Campaign},
				{Key: "key", Value: p.Key}, {Key: "value", Value: p.Value}, {Key: pk, Value: period},
			}, nil, true, true)
			add(e, k)
		}

		for _, t := range targets {
			if c.ua.IsBot {
				t.inc["bot_clicks"]++
				continue
			}
			t.inc["clicks"]++
			if !c.repeat {
				t.inc["unique_clicks"]++
			}
			if c.suspicious {
				t.inc["suspicious_clicks"]++
			}
			if !t.hasBy && t != linkE {
				continue
			}
			t.inc["by_hour."+strconv.Itoa(c.hour)]++
			t.inc["by_device."+urlparams.EscapeKey(c.ua.Device)]++
			t.inc["by_referer."+urlparams.EscapeKey(c.refHost)]++
			t.inc["by_country."+urlparams.EscapeKey(nz(countryOf(c), "unknown"))]++
			t.inc["by_os."+urlparams.EscapeKey(c.ua.OS)]++
			t.inc["by_browser."+urlparams.EscapeKey(c.ua.Browser)]++
			t.inc["by_source_group."+urlparams.EscapeKey(c.source)]++
			t.inc["by_prefix."+urlparams.EscapeKey(nz(c.e.AccessPrefix, "(none)"))]++ // /{code}: proxy đã cắt prefix
		}
	}
}

// countryOf: chưa có GeoIP trong bản này → ""; GeoIP City bật sau (D29).
func countryOf(*enriched) string { return "" }

func nz(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func asBWE(err error, target *mongo.BulkWriteException) bool { return errors.As(err, target) }

// ---------- idempotent ----------

func (a *Aggregator) markProcessed(ctx context.Context, es []events.ClickEvent) ([]events.ClickEvent, error) {
	now := time.Now().UTC()
	docs := make([]any, len(es))
	for i, e := range es {
		docs[i] = bson.D{{Key: "_id", Value: e.EventID}, {Key: "at", Value: now}}
	}
	_, err := a.report.Collection(store.CollClickEventIDs).InsertMany(ctx, docs, options.InsertMany().SetOrdered(false))
	dup, err := duplicateIndexes(err)
	if err != nil {
		return nil, fmt.Errorf("mark events: %w", err)
	}
	out := make([]events.ClickEvent, 0, len(es))
	for i, e := range es {
		if !dup[i] {
			out = append(out, e)
		}
	}
	return out, nil
}

func (a *Aggregator) unmark(ctx context.Context, es []events.ClickEvent) {
	ids := make([]string, len(es))
	for i, e := range es {
		ids[i] = e.EventID
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if _, err := a.report.Collection(store.CollClickEventIDs).DeleteMany(ctx, bson.D{{Key: "_id", Value: bson.D{{Key: "$in", Value: ids}}}}); err != nil {
		a.log.Error("unmark events", "err", err)
	}
}

// ---------- unique / suspicious ----------

func visitorHash(ip, ua string) string {
	s := sha256.Sum256([]byte(ip + "|" + ua))
	return hex.EncodeToString(s[:8])
}

// flagRepeat: khách đã click link này trong ngày (click_uniques, TTL 2 ngày).
func (a *Aggregator) flagRepeat(ctx context.Context, cs []*enriched) error {
	var docs []any
	var idx []int
	now := time.Now().UTC()
	for i, c := range cs {
		if c.ua.IsBot {
			continue
		}
		id := fmt.Sprintf("%d|%s|%s", c.e.Link.ID, c.date.Format("20060102"), visitorHash(c.e.IP, c.e.UserAgent))
		docs = append(docs, bson.D{{Key: "_id", Value: id}, {Key: "at", Value: now}})
		idx = append(idx, i)
	}
	if len(docs) == 0 {
		return nil
	}
	_, err := a.report.Collection(store.CollClickUniques).InsertMany(ctx, docs, options.InsertMany().SetOrdered(false))
	dup, err := duplicateIndexes(err)
	if err != nil {
		return fmt.Errorf("click_uniques: %w", err)
	}
	for j, i := range idx {
		cs[i].repeat = dup[j]
	}
	return nil
}

// flagSuspicious: click thứ > suspN của một IP vào một link trong cùng giờ (giờ VN).
func (a *Aggregator) flagSuspicious(ctx context.Context, cs []*enriched) error {
	groups := map[string][]int{}
	var keys []string
	for i, c := range cs {
		if c.ua.IsBot || c.e.IP == "" {
			continue
		}
		k := fmt.Sprintf("%d|%s|%s", c.e.Link.ID, c.e.IP, c.e.TS.In(analytics.VN).Format("2006010215"))
		if _, ok := groups[k]; !ok {
			keys = append(keys, k)
		}
		groups[k] = append(groups[k], i)
	}
	now := time.Now().UTC()
	for _, k := range keys {
		n := int64(len(groups[k]))
		var after struct {
			N int64 `bson:"n"`
		}
		err := a.report.Collection(store.CollClickIPHourly).FindOneAndUpdate(ctx, bson.D{{Key: "_id", Value: k}},
			bson.D{{Key: "$inc", Value: bson.D{{Key: "n", Value: n}}}, {Key: "$setOnInsert", Value: bson.D{{Key: "at", Value: now}}}},
			options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(options.After)).Decode(&after)
		if err != nil {
			return fmt.Errorf("click_ip_hourly: %w", err)
		}
		start := after.N - n // số click trước lô
		for j, i := range groups[k] {
			cs[i].suspicious = start+int64(j)+1 > int64(a.suspN)
		}
	}
	return nil
}

// ---------- clicks (time-series) ----------

func (a *Aggregator) insertClicks(ctx context.Context, cs []*enriched) error {
	docs := make([]any, len(cs))
	for i, c := range cs {
		l := c.e.Link
		d := bson.D{
			{Key: "ts", Value: c.e.TS.UTC()},
			{Key: "meta", Value: bson.D{{Key: "link_id", Value: l.ID}, {Key: "owner", Value: l.Owner},
				{Key: "campaign_code", Value: l.Campaign}, {Key: "ctv_id", Value: l.CTVID}}},
			{Key: "ip", Value: c.e.IP}, {Key: "country", Value: countryOf(c)}, {Key: "province", Value: ""},
			{Key: "device", Value: c.ua.Device}, {Key: "os", Value: c.ua.OS}, {Key: "browser", Value: c.ua.Browser},
			{Key: "in_app", Value: c.ua.InApp}, {Key: "referer", Value: c.e.Referer}, {Key: "referer_host", Value: c.refHost},
			{Key: "source_group", Value: c.source}, {Key: "access_prefix", Value: c.e.AccessPrefix},
			{Key: "link_prefix", Value: l.Prefix}, {Key: "link_api_version", Value: l.APIVersion},
			{Key: "link_is_custom", Value: l.IsCustom}, {Key: "link_created_at", Value: l.CreatedAt.UTC()},
			{Key: "dest_host", Value: l.DestHost}, {Key: "hour", Value: c.hour}, {Key: "weekday", Value: c.weekday},
			{Key: "is_bot", Value: c.ua.IsBot}, {Key: "is_suspicious", Value: c.suspicious}, {Key: "is_repeat", Value: c.repeat},
			{Key: "user_agent", Value: c.e.UserAgent}, {Key: "event_id", Value: c.e.EventID},
		}
		for _, k := range []string{"utm_source", "utm_medium", "utm_campaign", "utm_content", "utm_term"} {
			if v, ok := c.parsed.UTM[k]; ok {
				d = append(d, bson.E{Key: k, Value: v})
			}
		}
		docs[i] = d
	}
	if _, err := a.core.Collection(store.CollClicks).InsertMany(ctx, docs, options.InsertMany().SetOrdered(false)); err != nil {
		return fmt.Errorf("insert clicks: %w", err)
	}
	return nil
}

// ---------- facets, param_values ----------

type facetInc struct {
	owner, field, value string
	n                   int64
	last                time.Time
}

func (a *Aggregator) writeFacets(ctx context.Context, fs map[string]*facetInc) error {
	if len(fs) == 0 {
		return nil
	}
	models := make([]mongo.WriteModel, 0, len(fs))
	for _, f := range fs {
		models = append(models, mongo.NewUpdateOneModel().
			SetFilter(bson.D{{Key: "owner", Value: f.owner}, {Key: "field", Value: f.field}, {Key: "value", Value: f.value}}).
			SetUpdate(bson.D{{Key: "$inc", Value: bson.D{{Key: "clicks", Value: f.n}}}, {Key: "$max", Value: bson.D{{Key: "last_date", Value: f.last}}}}).
			SetUpsert(true))
	}
	_, err := a.report.Collection(store.CollClickFacets).BulkWrite(ctx, models, options.BulkWrite().SetOrdered(false))
	if err != nil {
		return fmt.Errorf("click_facets: %w", err)
	}
	return nil
}

type paramValInc struct {
	owner, key, value string
	n                 int64
}

func (a *Aggregator) writeParamValueClicks(ctx context.Context, ps map[string]*paramValInc) error {
	if len(ps) == 0 {
		return nil
	}
	models := make([]mongo.WriteModel, 0, len(ps))
	for _, p := range ps {
		models = append(models, mongo.NewUpdateOneModel().
			SetFilter(bson.D{{Key: "owner", Value: p.owner}, {Key: "key", Value: p.key}, {Key: "value", Value: p.value}}).
			SetUpdate(bson.D{{Key: "$inc", Value: bson.D{{Key: "clicks_total", Value: p.n}}}}).
			SetUpsert(true))
	}
	_, err := a.report.Collection(store.CollParamValues).BulkWrite(ctx, models, options.BulkWrite().SetOrdered(false))
	if err != nil {
		return fmt.Errorf("param_values: %w", err)
	}
	return nil
}

// ---------- link events ----------

// HandleLink: tạo / sửa link → link_params, param_values, new_links.
func (a *Aggregator) HandleLink(ctx context.Context, e events.LinkEvent) error {
	if e.EventID != "" {
		fresh, err := a.markProcessed(ctx, []events.ClickEvent{{EventID: "link:" + e.EventID}})
		if err != nil {
			return err
		}
		if len(fresh) == 0 {
			return nil
		}
	}
	err := a.handleLink(ctx, e)
	if err != nil && e.EventID != "" {
		a.unmark(ctx, []events.ClickEvent{{EventID: "link:" + e.EventID}})
	}
	return err
}

func (a *Aggregator) handleLink(ctx context.Context, e events.LinkEvent) error {
	l := e.Link
	switch e.Type {
	case events.LinkCreated, events.LinkUpdated:
	default:
		return nil // xoá / khôi phục: total_links_snapshot do job snapshot tính lại
	}
	rules := a.reg.Rules(ctx)
	parsed := urlparams.Parse(l.LongURL, rules, a.salt)

	if e.Type == events.LinkUpdated {
		if _, err := a.report.Collection(store.CollLinkParams).DeleteMany(ctx, bson.D{{Key: "link_id", Value: l.ID}}); err != nil {
			return fmt.Errorf("link_params delete: %w", err)
		}
	}
	if len(parsed.Params) > 0 {
		models := make([]mongo.WriteModel, 0, len(parsed.Params))
		pvModels := make([]mongo.WriteModel, 0, len(parsed.Params))
		seen := map[string]bool{}
		for _, p := range parsed.Params {
			if seen[p.Key] {
				continue // unique (link_id, key): tham số lặp chỉ giữ giá trị đầu
			}
			seen[p.Key] = true
			doc := bson.D{
				{Key: "link_id", Value: l.ID}, {Key: "owner", Value: l.Owner}, {Key: "campaign_code", Value: l.Campaign},
				{Key: "ctv_id", Value: l.CTVID}, {Key: "key", Value: p.Key}, {Key: "value", Value: p.Value},
				{Key: "link_created_at", Value: l.CreatedAt.UTC()},
			}
			if p.Raw != "" {
				doc = append(doc, bson.E{Key: "value_raw", Value: p.Raw})
			}
			models = append(models, mongo.NewReplaceOneModel().
				SetFilter(bson.D{{Key: "link_id", Value: l.ID}, {Key: "key", Value: p.Key}}).
				SetReplacement(doc).SetUpsert(true))
			if e.Type == events.LinkCreated {
				pvModels = append(pvModels, mongo.NewUpdateOneModel().
					SetFilter(bson.D{{Key: "owner", Value: l.Owner}, {Key: "key", Value: p.Key}, {Key: "value", Value: p.Value}}).
					SetUpdate(bson.D{
						{Key: "$inc", Value: bson.D{{Key: "links", Value: 1}}},
						{Key: "$min", Value: bson.D{{Key: "first_seen", Value: l.CreatedAt.UTC()}}},
						{Key: "$max", Value: bson.D{{Key: "last_seen", Value: l.CreatedAt.UTC()}}},
						{Key: "$setOnInsert", Value: bson.D{{Key: "clicks_total", Value: int64(0)}}},
					}).SetUpsert(true))
			}
		}
		if _, err := a.report.Collection(store.CollLinkParams).BulkWrite(ctx, models, options.BulkWrite().SetOrdered(false)); err != nil {
			return fmt.Errorf("link_params: %w", err)
		}
		if len(pvModels) > 0 {
			if _, err := a.report.Collection(store.CollParamValues).BulkWrite(ctx, pvModels, options.BulkWrite().SetOrdered(false)); err != nil {
				return fmt.Errorf("param_values: %w", err)
			}
		}
	}
	if e.Type != events.LinkCreated {
		return nil
	}
	if l.DestHost != "" {
		_, err := a.report.Collection(store.CollClickFacets).UpdateOne(ctx,
			bson.D{{Key: "owner", Value: l.Owner}, {Key: "field", Value: "dest_host"}, {Key: "value", Value: l.DestHost}},
			bson.D{{Key: "$inc", Value: bson.D{{Key: "clicks", Value: int64(0)}}}, {Key: "$max", Value: bson.D{{Key: "last_date", Value: analytics.DateOf(l.CreatedAt)}}}},
			options.UpdateOne().SetUpsert(true))
		if err != nil {
			return fmt.Errorf("click_facets dest_host: %w", err)
		}
	}

	// new_links (không có ở stats_link_*)
	ac := newAcc()
	day := analytics.DateOf(l.CreatedAt)
	for _, monthly := range []bool{false, true} {
		period, pk, suffix := day, "date", "_daily"
		if monthly {
			period, pk, suffix = analytics.MonthOf(day), "month", "_monthly"
		}
		var ts []*entry
		e1, _ := ac.get("stats_campaign"+suffix, bson.D{{Key: "owner", Value: l.Owner}, {Key: "campaign_code", Value: l.Campaign}, {Key: pk, Value: period}}, nil, true, true)
		e2, _ := ac.get("stats_ctv"+suffix, bson.D{{Key: "ctv_id", Value: l.CTVID}, {Key: "owner", Value: l.Owner}, {Key: "campaign_code", Value: l.Campaign}, {Key: pk, Value: period}}, nil, false, true)
		e3, _ := ac.get("stats_owner"+suffix, bson.D{{Key: "owner", Value: l.Owner}, {Key: pk, Value: period}}, nil, true, true)
		e4, _ := ac.get("stats_system"+suffix, bson.D{{Key: pk, Value: period}}, nil, true, true)
		ts = append(ts, e1, e2, e3, e4)
		for _, p := range parsed.Params {
			if rules[p.Key].Tracked {
				ep, _ := ac.get("stats_param"+suffix, bson.D{
					{Key: "owner", Value: l.Owner}, {Key: "campaign_code", Value: l.Campaign},
					{Key: "key", Value: p.Key}, {Key: "value", Value: p.Value}, {Key: pk, Value: period},
				}, nil, true, true)
				ts = append(ts, ep)
			}
		}
		for _, t := range ts {
			t.inc["new_links"]++
		}
	}
	return ac.flush(ctx, a.report)
}

// ---------- total_links_snapshot ----------

// Snapshot tính total_links_snapshot hôm nay cho stats_owner_daily / stats_system_daily
// (link chưa xoá, tạo đến hết hôm nay).
func (a *Aggregator) Snapshot(ctx context.Context) error {
	today := analytics.DateOf(time.Now())
	cur, err := a.core.Collection(store.CollLinks).Aggregate(ctx, mongo.Pipeline{
		{{Key: "$match", Value: bson.D{{Key: "status", Value: bson.D{{Key: "$ne", Value: domain.StatusDeleted}}}}}},
		{{Key: "$group", Value: bson.D{{Key: "_id", Value: "$owner_username"}, {Key: "n", Value: bson.D{{Key: "$sum", Value: 1}}}}}},
	}, options.Aggregate().SetAllowDiskUse(true))
	if err != nil {
		return fmt.Errorf("snapshot aggregate: %w", err)
	}
	var rows []struct {
		Owner string `bson:"_id"`
		N     int64  `bson:"n"`
	}
	if err := cur.All(ctx, &rows); err != nil {
		return err
	}
	ac := newAcc()
	var total int64
	for _, r := range rows {
		total += r.N
		ac.get(store.CollStatsOwnerDaily, bson.D{{Key: "owner", Value: r.Owner}, {Key: "date", Value: today}}, nil, true, true)
	}
	ac.get(store.CollStatsSystemDaily, bson.D{{Key: "date", Value: today}}, nil, true, true)
	if err := ac.flush(ctx, a.report); err != nil { // chỉ tạo document còn thiếu
		return err
	}
	models := make([]mongo.WriteModel, 0, len(rows))
	for _, r := range rows {
		models = append(models, mongo.NewUpdateOneModel().
			SetFilter(bson.D{{Key: "owner", Value: r.Owner}, {Key: "date", Value: today}}).
			SetUpdate(bson.D{{Key: "$set", Value: bson.D{{Key: "total_links_snapshot", Value: r.N}}}}))
	}
	if len(models) > 0 {
		if _, err := a.report.Collection(store.CollStatsOwnerDaily).BulkWrite(ctx, models, options.BulkWrite().SetOrdered(false)); err != nil {
			return err
		}
	}
	_, err = a.report.Collection(store.CollStatsSystemDaily).UpdateOne(ctx, bson.D{{Key: "date", Value: today}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "total_links_snapshot", Value: total}}}})
	return err
}

// duplicateIndexes: từ lỗi InsertMany unordered → các chỉ số bị trùng khoá; lỗi khác trả về.
func duplicateIndexes(err error) (map[int]bool, error) {
	dup := map[int]bool{}
	if err == nil {
		return dup, nil
	}
	var bwe mongo.BulkWriteException
	if !asBWE(err, &bwe) || bwe.WriteConcernError != nil {
		return nil, err
	}
	for _, we := range bwe.WriteErrors {
		if we.Code != 11000 {
			return nil, err
		}
		dup[we.Index] = true
	}
	return dup, nil
}
