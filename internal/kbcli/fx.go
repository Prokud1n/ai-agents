package kbcli

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/caarlos0/env/v11"
	"go.uber.org/fx"

	"github.com/Prokud1n/ai-agents/internal/config"
)

// Module provides what the kb command needs: only its own config (no Redis/S3 settings required),
// a logger writing to stderr (stdout is for results) and the runner.
var Module = fx.Module("kbcli",
	fx.Provide(ParseConfig, NewLogger, NewRunner),
)

type Config struct {
	fx.Out

	Logging    config.Logging
	Postgres   *config.Postgres   `env:",init"`
	OpenAI     *config.OpenAI     `env:",init"`
	Embeddings *config.Embeddings `env:",init"`
}

func ParseConfig() (Config, error) {
	var cfg Config

	if err := env.Parse(&cfg); err != nil {
		return Config{}, fmt.Errorf("failed to parse config: %w", err)
	}

	return cfg, nil
}

func NewLogger(cfg config.Logging) (*slog.Logger, error) {
	var level slog.Level
	if err := level.UnmarshalText([]byte(cfg.LogLevel)); err != nil {
		return nil, fmt.Errorf("failed to parse log level: %w", err)
	}

	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level})), nil
}
