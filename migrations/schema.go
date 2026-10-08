// Package migrations: tạo collection + index MongoDB — HỢP ĐỒNG SCHEMA với Portal (Project 2).
//
// Chạy lại an toàn (idempotent). Đổi tên / xoá trường, đổi kiểu, đổi index Portal dùng → tăng Version,
// báo Portal trong MR và giữ tương thích ≥ 1 release (PLAN_SERVICE_API.md §5.6).
//
// Index khớp seed của Portal (am-shortlink-sale-portal/api/internal/seed/schema.go) để dev / test và
// production có cùng kế hoạch truy vấn.
package migrations

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// Version: phiên bản hợp đồng schema hiện tại.
const Version = 1

// Ensure tạo collection + index cho core (am_shortlink) và report (am_shortlink_report).
func Ensure(ctx context.Context, core, report *mongo.Database) error {
	if err := ensureTimeSeries(ctx, core, "clicks"); err != nil {
		return err
	}
	ttl := func(field string, d time.Duration) mongo.IndexModel {
		return mongo.IndexModel{Keys: bson.D{{Key: field, Value: 1}}, Options: options.Index().SetExpireAfterSeconds(int32(d.Seconds()))}
	}
	idx := map[*mongo.Database]map[string][]mongo.IndexModel{
		core: {
			"users":     {uniq("username")},
			"api_keys":  {uniq("key_hash"), ix("user_id")},
			"campaigns": {uniq("code"), ix("created_by", "created_at")},
			"links": {
				uniq("code"),
				ix("owner_username", "created_at"),
				ix("owner_username", "campaign_code", "created_at"),
				ix("owner_username", "long_url_hash"),
				ix("long_url_hash"),
				ix("campaign_code", "created_at"),
				ix("ctv_id", "created_at"),
			},
			"clicks":    {ix("meta.owner", "ts"), ix("meta.campaign_code", "ts"), ix("meta.ctv_id", "ts"), ix("meta.link_id", "ts")},
			"templates": {ix("status", "template_url")},
			"domains":   {ix("domain_name")},
		},
		report: {
			"stats_link_daily":       {uniq("link_id", "date"), ix("owner", "date"), ix("campaign_code", "date"), ix("ctv_id", "date"), ix("prefix", "date"), ix("date")},
			"stats_campaign_daily":   {uniq("owner", "campaign_code", "date"), ix("campaign_code", "date")},
			"stats_ctv_daily":        {uniq("ctv_id", "owner", "campaign_code", "date"), ix("owner", "date"), ix("campaign_code", "date")},
			"stats_owner_daily":      {uniq("owner", "date")},
			"stats_system_daily":     {uniq("date")},
			"stats_param_daily":      {uniq("owner", "campaign_code", "key", "value", "date"), ix("key", "date")},
			"stats_link_monthly":     {uniq("link_id", "month"), ix("owner", "month")},
			"stats_campaign_monthly": {uniq("owner", "campaign_code", "month"), ix("campaign_code", "month")},
			"stats_ctv_monthly":      {uniq("ctv_id", "owner", "campaign_code", "month"), ix("owner", "month")},
			"stats_owner_monthly":    {uniq("owner", "month")},
			"stats_system_monthly":   {uniq("month")},
			"stats_param_monthly":    {uniq("owner", "campaign_code", "key", "value", "month"), ix("key", "month")},
			"link_params":            {uniq("link_id", "key"), ix("owner", "key", "value"), ix("key", "value"), ix("campaign_code", "key", "value")},
			"param_values":           {uniq("owner", "key", "value")},
			"click_facets":           {uniq("owner", "field", "value"), ix("field", "clicks")},
			"ctvs":                   {ix("owner")},
			// riêng của Service
			"click_uniques":   {ttl("at", 48*time.Hour)},
			"click_event_ids": {ttl("at", 72*time.Hour)},
			"click_ip_hourly": {ttl("at", 2*time.Hour)},
		},
	}
	for db, colls := range idx {
		names := make([]string, 0, len(colls))
		for c := range colls {
			names = append(names, c)
		}
		sort.Strings(names)
		for _, c := range names {
			if _, err := db.Collection(c).Indexes().CreateMany(ctx, colls[c]); err != nil {
				return fmt.Errorf("index %s.%s: %w", db.Name(), c, err)
			}
		}
	}
	if err := seedDefaults(ctx, core, report); err != nil {
		return err
	}
	_, err := core.Collection("schema_migrations").UpdateOne(ctx, bson.D{{Key: "_id", Value: "service"}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "version", Value: Version}, {Key: "applied_at", Value: time.Now().UTC()}}}},
		options.UpdateOne().SetUpsert(true))
	return err
}

func ensureTimeSeries(ctx context.Context, db *mongo.Database, name string) error {
	err := db.CreateCollection(ctx, name, options.CreateCollection().SetTimeSeriesOptions(
		options.TimeSeries().SetTimeField("ts").SetMetaField("meta").SetGranularity("minutes")))
	var ce mongo.CommandError
	if err == nil || (errors.As(err, &ce) && ce.Code == 48) { // 48 = NamespaceExists
		return nil
	}
	return fmt.Errorf("tạo %s time-series: %w", name, err)
}

// seedDefaults: prefix sale / lm và tham số theo dõi mặc định (§5.4b) nếu chưa có.
func seedDefaults(ctx context.Context, core, report *mongo.Database) error {
	for _, p := range []bson.D{
		{{Key: "_id", Value: "sale"}, {Key: "is_default", Value: true}, {Key: "is_default_v3", Value: false}, {Key: "active", Value: true}, {Key: "description", Value: "Prefix mặc định /sale"}},
		{{Key: "_id", Value: "lm"}, {Key: "is_default", Value: false}, {Key: "is_default_v3", Value: true}, {Key: "active", Value: true}, {Key: "description", Value: "Prefix /lm (mặc định API v3)"}},
	} {
		if _, err := core.Collection("prefixes").UpdateOne(ctx, bson.D{{Key: "_id", Value: p[0].Value}},
			bson.D{{Key: "$setOnInsert", Value: p[1:]}}, options.UpdateOne().SetUpsert(true)); err != nil {
			return fmt.Errorf("seed prefixes: %w", err)
		}
	}
	now := time.Now().UTC()
	type reg struct {
		key, label, pii string
		tracked         bool
		norm            []string
		max             int
		status          string
	}
	for _, r := range []reg{
		{"utm_source", "Nguồn (utm_source)", "none", true, []string{"lower", "trim"}, 500, "active"},
		{"utm_medium", "Kênh (utm_medium)", "none", true, []string{"lower", "trim"}, 500, "active"},
		{"utm_campaign", "Chiến dịch (utm_campaign)", "none", true, []string{"lower", "trim"}, 2000, "active"},
		{"utm_content", "Nội dung (utm_content)", "none", true, []string{"trim"}, 2000, "active"},
		{"utm_term", "Từ khoá (utm_term)", "none", true, []string{"trim"}, 2000, "active"},
		{"utm_extra_ctv", "CTV (utm_extra_ctv)", "none", true, []string{"phone"}, 100000, "active"},
		{"phonenumber", "SĐT khách", "hash", false, []string{"phone"}, 0, "active"},
		{"fullname", "Họ tên khách", "drop", false, nil, 0, "active"},
		{"email", "Email khách", "drop", false, nil, 0, "active"},
		{"gclid", "gclid", "none", false, nil, 1000, "high_cardinality"},
		{"fbclid", "fbclid", "none", false, nil, 1000, "high_cardinality"},
	} {
		norm := r.norm
		if norm == nil {
			norm = []string{}
		}
		if _, err := report.Collection("param_registry").UpdateOne(ctx, bson.D{{Key: "_id", Value: r.key}},
			bson.D{{Key: "$setOnInsert", Value: bson.D{
				{Key: "key", Value: r.key}, {Key: "label", Value: r.label}, {Key: "tracked", Value: r.tracked},
				{Key: "pii", Value: r.pii}, {Key: "normalize", Value: norm}, {Key: "max_values", Value: r.max},
				{Key: "status", Value: r.status}, {Key: "created_by", Value: "system"}, {Key: "created_at", Value: now},
			}}}, options.UpdateOne().SetUpsert(true)); err != nil {
			return fmt.Errorf("seed param_registry: %w", err)
		}
	}
	return nil
}

func keys(fields ...string) bson.D {
	d := bson.D{}
	for _, f := range fields {
		d = append(d, bson.E{Key: f, Value: 1})
	}
	return d
}

func ix(fields ...string) mongo.IndexModel { return mongo.IndexModel{Keys: keys(fields...)} }

func uniq(fields ...string) mongo.IndexModel {
	return mongo.IndexModel{Keys: keys(fields...), Options: options.Index().SetUnique(true)}
}
