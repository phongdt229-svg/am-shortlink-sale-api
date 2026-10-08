// Package redisx tạo client Redis (tuỳ chọn). Addr trống → nil: mọi nơi dùng phải chịu được nil (fail-open).
package redisx

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"am-shortlink-service/internal/config"
)

type Client struct {
	*redis.Client
	Prefix string
}

func Connect(ctx context.Context, c config.Redis) (*Client, error) {
	if c.Addr == "" {
		return nil, nil
	}
	rc := redis.NewClient(&redis.Options{
		Addr:         c.Addr,
		Password:     c.Password,
		DB:           c.DB,
		DialTimeout:  2 * time.Second,
		ReadTimeout:  200 * time.Millisecond,
		WriteTimeout: 200 * time.Millisecond,
		PoolSize:     64,
	})
	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := rc.Ping(pingCtx).Err(); err != nil {
		_ = rc.Close()
		return nil, fmt.Errorf("ping Redis: %w", err)
	}
	return &Client{Client: rc, Prefix: c.KeyPrefix}, nil
}

// Key thêm prefix môi trường.
func (c *Client) Key(parts ...string) string {
	k := c.Prefix
	for i, p := range parts {
		if i > 0 {
			k += ":"
		}
		k += p
	}
	return k
}
