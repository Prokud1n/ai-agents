package internal

import (
	"fmt"
	"log/slog"
	"os"

	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/health"
	"github.com/Prokud1n/ai-agents/internal/migrator"
	"github.com/Prokud1n/ai-agents/internal/providers/postgres"
	"github.com/Prokud1n/ai-agents/internal/providers/redis"
	"github.com/Prokud1n/ai-agents/internal/providers/s3"
	"github.com/Prokud1n/ai-agents/internal/server"
)

func DeclareApplicationOptions() []fx.Option {
	return append(declareBaseOptions(),
		// Apply migrations before anything else touches the database.
		// Module order matters: fx runs start hooks in the order constructors and invokes ran.
		migrator.Module,

		// Initialize external service clients
		postgres.Module,
		redis.Module,
		s3.Module,

		// Initialize application modules
		health.Module,

		// HTTP server goes last: it starts accepting requests once dependencies are connected
		server.Module,
	)
}

// DeclareMigrationOptions builds the standalone migrate command: apply migrations and exit.
func DeclareMigrationOptions() []fx.Option {
	return append(declareBaseOptions(),
		migrator.CommandModule,
	)
}

func NewApplication() *fx.App {
	return fx.New(DeclareApplicationOptions()...)
}

func NewMigrationApplication() *fx.App {
	return fx.New(DeclareMigrationOptions()...)
}

func NewLogger(cfg config.Logging) (*slog.Logger, error) {
	var level slog.Level
	if err := level.UnmarshalText([]byte(cfg.LogLevel)); err != nil {
		return nil, fmt.Errorf("failed to parse log level: %w", err)
	}

	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})), nil
}

func declareBaseOptions() []fx.Option {
	return []fx.Option{
		// Initialize application configuration
		fx.Provide(config.Parse),

		// Initialize logger
		fx.Provide(NewLogger),
		fx.WithLogger(func(log *slog.Logger) fxevent.Logger {
			return &fxevent.SlogLogger{Logger: log}
		}),
	}
}
