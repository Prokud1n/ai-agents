package redis

import (
	"context"
	"fmt"
	"log/slog"

	goredis "github.com/redis/go-redis/v9"
	"go.uber.org/fx"

	"github.com/Prokud1n/ai-agents/internal/config"
)

type Client struct {
	rdb *goredis.Client
}

func NewClient(lc fx.Lifecycle, cfg *config.Redis, log *slog.Logger) *Client {
	client := &Client{
		rdb: goredis.NewClient(&goredis.Options{
			Addr:     cfg.Address,
			Password: cfg.Password,
			DB:       cfg.DB,
		}),
	}

	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			if err := client.Ping(ctx); err != nil {
				return fmt.Errorf("failed to connect to redis: %w", err)
			}

			log.InfoContext(ctx, "redis: connected", "address", cfg.Address)

			return nil
		},
		OnStop: func(context.Context) error {
			if err := client.rdb.Close(); err != nil {
				return fmt.Errorf("failed to close redis client: %w", err)
			}

			return nil
		},
	})

	return client
}

// Conn returns the underlying go-redis client for commands.
func (c *Client) Conn() *goredis.Client {
	return c.rdb
}

// Name is the dependency name in /readyz.
func (*Client) Name() string {
	return "redis"
}

func (c *Client) Ping(ctx context.Context) error {
	if err := c.rdb.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("failed to ping redis: %w", err)
	}

	return nil
}
