// Package logger builds the application logger: log/slog with JSON output to stdout.
// It is separate from internal so that small binaries (cmd/migrate) do not pull in the whole app.
package logger

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/Prokud1n/ai-agents/internal/config"
)

func New(cfg config.Logging) (*slog.Logger, error) {
	var level slog.Level
	if err := level.UnmarshalText([]byte(cfg.LogLevel)); err != nil {
		return nil, fmt.Errorf("failed to parse log level: %w", err)
	}

	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})), nil
}
