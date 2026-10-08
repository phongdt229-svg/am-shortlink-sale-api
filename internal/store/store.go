// Package store: truy cập MongoDB. Chỉ package này (và migrations, aggregator) import driver.
package store

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"am-shortlink-service/internal/platform/mongodb"
)

// Collection trong am_shortlink (Core).
const (
	CollLinks     = "links"
	CollUsers     = "users"
	CollAPIKeys   = "api_keys"
	CollCampaigns = "campaigns"
	CollPrefixes  = "prefixes"
	CollDomains   = "domains"
	CollTemplates = "templates"
	CollCounters  = "counters"
	CollClicks    = "clicks"
)

// Collection trong am_shortlink_report (Report).
const (
	CollStatsLinkDaily       = "stats_link_daily"
	CollStatsCampaignDaily   = "stats_campaign_daily"
	CollStatsCTVDaily        = "stats_ctv_daily"
	CollStatsOwnerDaily      = "stats_owner_daily"
	CollStatsSystemDaily     = "stats_system_daily"
	CollStatsParamDaily      = "stats_param_daily"
	CollStatsLinkMonthly     = "stats_link_monthly"
	CollStatsCampaignMonthly = "stats_campaign_monthly"
	CollStatsCTVMonthly      = "stats_ctv_monthly"
	CollStatsOwnerMonthly    = "stats_owner_monthly"
	CollStatsSystemMonthly   = "stats_system_monthly"
	CollStatsParamMonthly    = "stats_param_monthly"
	CollLinkParams           = "link_params"
	CollParamRegistry        = "param_registry"
	CollParamValues          = "param_values"
	CollClickFacets          = "click_facets"
	CollCTVs                 = "ctvs"
	// Riêng của Service (Portal không đọc).
	CollClickUniques  = "click_uniques"   // (link_id, date, visitor_hash) — đếm unique, TTL 2 ngày
	CollClickEventIDs = "click_event_ids" // event_id đã xử lý — idempotent, TTL 3 ngày
	CollClickIPHourly = "click_ip_hourly" // đếm click / IP / link / giờ — suspicious, TTL 2 giờ
)

var ErrNotFound = errors.New("not found")

type Store struct {
	db *mongodb.DB
}

func New(db *mongodb.DB) *Store { return &Store{db: db} }

func (s *Store) DB() *mongodb.DB { return s.db }

func (s *Store) core(c string) *mongo.Collection   { return s.db.Core.Collection(c) }
func (s *Store) report(c string) *mongo.Collection { return s.db.Report.Collection(c) }

// ctx đặt trần thời gian cho mỗi truy vấn.
func (s *Store) ctx(ctx context.Context) (context.Context, context.CancelFunc) {
	d := s.db.MaxTime
	if d <= 0 {
		d = 5 * time.Second
	}
	return context.WithTimeout(ctx, d)
}

func notFound(err error) error {
	if errors.Is(err, mongo.ErrNoDocuments) {
		return ErrNotFound
	}
	return err
}
