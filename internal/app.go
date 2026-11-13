package internal

import (
	"log/slog"

	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/health"
	"github.com/Prokud1n/ai-agents/internal/logger"
	"github.com/Prokud1n/ai-agents/internal/providers/postgres"
	"github.com/Prokud1n/ai-agents/internal/providers/redis"
	"github.com/Prokud1n/ai-agents/internal/providers/s3"
	"github.com/Prokud1n/ai-agents/internal/readiness"
	"github.com/Prokud1n/ai-agents/internal/server"
)

func DeclareApplicationOptions() []fx.Option {
	return append(declareBaseOptions(),
		// Initialize external service clients.
		// Migrations are not applied here: that is cmd/migrate (the "migrate" service in docker compose).
		postgres.Module,
		redis.Module,
		s3.Module,

		// Initialize application modules
		health.Module,
		readiness.Module,

		// HTTP server goes last: it starts accepting requests once dependencies are connected
		server.Module,
	)
}

func NewApplication() *fx.App {
	return fx.New(append(DeclareApplicationOptions(), lifecycleOptions())...)
}

func declareBaseOptions() []fx.Option {
	return []fx.Option{
		// Initialize application configuration
		fx.Provide(config.Parse),

		// Initialize logger; fx lifecycle events are noisy, so they go to debug
		fx.Provide(logger.New),
		fx.WithLogger(func(log *slog.Logger) fxevent.Logger {
			fxLogger := &fxevent.SlogLogger{Logger: log}
			fxLogger.UseLogLevel(slog.LevelDebug)

			return fxLogger
		}),
	}
}

// lifecycleOptions sets fx start/stop timeouts. They must be top-level options,
// so the config is read before the fx graph is built.
func lifecycleOptions() fx.Option {
	cfg, err := config.ParseLifecycle()
	if err != nil {
		return fx.Error(err)
	}

	return fx.Options(
		fx.StartTimeout(cfg.StartTimeout),
		fx.StopTimeout(cfg.StopTimeout),
	)
}
