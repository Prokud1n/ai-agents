package kbcli

import (
	"fmt"
	"log/slog"
	"os"

	"go.uber.org/fx"

	"github.com/Prokud1n/ai-agents/internal/config"
)

// Module provides what the kb command needs: only its own config (no Redis/S3 settings required),
// a logger writing to stderr (stdout is for results) and the runner.
var Module = fx.Module("kbcli",
	fx.Provide(config.ParseKB, NewLogger, NewRunner),
)

func NewLogger(cfg config.Logging) (*slog.Logger, error) {
	var level slog.Level
	if err := level.UnmarshalText([]byte(cfg.LogLevel)); err != nil {
		return nil, fmt.Errorf("failed to parse log level: %w", err)
	}

	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level})), nil
}
