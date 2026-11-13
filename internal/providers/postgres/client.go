package postgres

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"

	"github.com/Prokud1n/ai-agents/internal/config"
)

// Client gives access to PostgreSQL connections.
// Read and write connections share one pool for now; a replica can be plugged in without changing callers.
type Client struct {
	pool *pgxpool.Pool
}

func NewClient(lc fx.Lifecycle, cfg *config.Postgres, log *slog.Logger) (*Client, error) {
	pool, err := pgxpool.New(context.Background(), cfg.DSN())
	if err != nil {
		return nil, fmt.Errorf("failed to create postgres pool: %w", err)
	}

	client := &Client{pool: pool}

	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			if err := client.Ping(ctx); err != nil {
				return fmt.Errorf("failed to connect to postgres: %w", err)
			}

			log.InfoContext(ctx, "postgres: connected", "host", cfg.Host, "database", cfg.Database)

			return nil
		},
		OnStop: func(context.Context) error {
			pool.Close()

			return nil
		},
	})

	return client, nil
}

func (c *Client) GetReadConnection() *pgxpool.Pool {
	return c.pool
}

func (c *Client) GetWriteConnection() *pgxpool.Pool {
	return c.pool
}

func (c *Client) Ping(ctx context.Context) error {
	if err := c.pool.Ping(ctx); err != nil {
		return fmt.Errorf("failed to ping postgres: %w", err)
	}

	return nil
}
