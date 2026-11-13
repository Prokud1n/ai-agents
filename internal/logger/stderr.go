package logger

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/Prokud1n/ai-agents/internal/config"
)

// NewStderr is the logger of command-line tools whose stdout is their result (cmd/transcribe prints JSON).
func NewStderr(cfg config.Logging) (*slog.Logger, error) {
	var level slog.Level
	if err := level.UnmarshalText([]byte(cfg.LogLevel)); err != nil {
		return nil, fmt.Errorf("failed to parse log level: %w", err)
	}

	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level})), nil
}
