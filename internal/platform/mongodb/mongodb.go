// Package mongodb tạo client MongoDB dùng chung.
//
// Hai database (cùng cụm, D31):
//   - am_shortlink (Core): links, users, api_keys, campaigns, prefixes, domains, templates, counters, clicks.
//   - am_shortlink_report (Report): stats_*, link_params, param_*, click_facets, ctvs — chỉ consumer / job ghi.
package mongodb

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"

	"am-shortlink-service/internal/config"
)

type DB struct {
	Client  *mongo.Client
	Core    *mongo.Database
	Report  *mongo.Database
	MaxTime time.Duration
}

func Connect(ctx context.Context, c config.Mongo, appName string) (*DB, error) {
	opts := options.Client().
		ApplyURI(c.URI).
		SetConnectTimeout(c.ConnectTimeout).
		SetServerSelectionTimeout(c.ConnectTimeout).
		SetAppName(appName)

	client, err := mongo.Connect(opts)
	if err != nil {
		return nil, fmt.Errorf("kết nối MongoDB: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, c.ConnectTimeout)
	defer cancel()
	if err := client.Ping(pingCtx, readpref.Primary()); err != nil {
		_ = client.Disconnect(context.Background())
		return nil, fmt.Errorf("ping MongoDB: %w", err)
	}
	return &DB{
		Client:  client,
		Core:    client.Database(c.CoreDB),
		Report:  client.Database(c.ReportDB),
		MaxTime: c.MaxTime,
	}, nil
}

func (d *DB) Ping(ctx context.Context) error { return d.Client.Ping(ctx, readpref.Primary()) }

func (d *DB) Close(ctx context.Context) error { return d.Client.Disconnect(ctx) }
