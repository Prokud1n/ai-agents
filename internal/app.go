package internal

import (
	"fmt"
	"log/slog"
	"os"

	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	"github.com/Prokud1n/ai-agents/internal/config"
)

func DeclareApplicationOptions() []fx.Option {
	return []fx.Option{
		// Initialize application configuration
		fx.Provide(config.Parse),

		// Initialize logger
		fx.Provide(NewLogger),
		fx.WithLogger(func(log *slog.Logger) fxevent.Logger {
			return &fxevent.SlogLogger{Logger: log}
		}),

		// Initialize application modules
	}
}

func NewApplication() *fx.App {
	return fx.New(DeclareApplicationOptions()...)
}

func NewLogger(cfg config.Logging) (*slog.Logger, error) {
	var level slog.Level
	if err := level.UnmarshalText([]byte(cfg.LogLevel)); err != nil {
		return nil, fmt.Errorf("failed to parse log level: %w", err)
	}

	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})), nil
}
