package internal

import (
	"log/slog"

	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	"github.com/Prokud1n/ai-agents/internal/agents"
	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/editjob"
	"github.com/Prokud1n/ai-agents/internal/editor"
	"github.com/Prokud1n/ai-agents/internal/editplan"
	"github.com/Prokud1n/ai-agents/internal/gateway"
	"github.com/Prokud1n/ai-agents/internal/health"
	"github.com/Prokud1n/ai-agents/internal/knowledge"
	"github.com/Prokud1n/ai-agents/internal/knowledge/kbtool"
	"github.com/Prokud1n/ai-agents/internal/llmusage"
	"github.com/Prokud1n/ai-agents/internal/logger"
	"github.com/Prokud1n/ai-agents/internal/media"
	"github.com/Prokud1n/ai-agents/internal/providers/anthropic"
	"github.com/Prokud1n/ai-agents/internal/providers/embeddings"
	"github.com/Prokud1n/ai-agents/internal/providers/postgres"
	"github.com/Prokud1n/ai-agents/internal/providers/redis"
	"github.com/Prokud1n/ai-agents/internal/providers/s3"
	"github.com/Prokud1n/ai-agents/internal/readiness"
	"github.com/Prokud1n/ai-agents/internal/server"
	"github.com/Prokud1n/ai-agents/internal/tasks"
	"github.com/Prokud1n/ai-agents/internal/teamchat"
)

func DeclareApplicationOptions() []fx.Option {
	return append(declareBaseOptions(),
		// Initialize external service clients.
		// Migrations are not applied here: that is cmd/migrate (the "migrate" service in docker compose).
		postgres.Module,
		redis.Module,
		s3.Module,
		anthropic.Module,
		embeddings.Module,

		// Initialize application modules
		health.Module,
		readiness.Module,
		llmusage.Module,
		knowledge.Module,
		kbtool.Module,
		// The editor's decisions: the submit_edit tool of agents/editor and the Decider
		editplan.Module,
		agents.Module,
		// Incoming videos of the team chat in S3
		media.Module,
		// Telegram gateway: off without bot tokens
		gateway.Module,
		// Task workers
		tasks.Module,
		// Gateway ⇄ tasks: group events become tasks, drafts go to the group (to the log without bot tokens)
		teamchat.Module,
		// The editor's tasks go to the media worker (cmd/media-worker) through Redis: the app runs no ffmpeg
		editjob.Module,
		editor.Module,

		// HTTP server goes last: it starts accepting requests once dependencies are connected
		server.Module,
	)
}

// DeclareAgentRunOptions builds the agent-run CLI: agents, the Claude API client, usage accounting and
// the agents' tools (the knowledge base, the editor's submit_edit), without the HTTP server and other dependencies.
func DeclareAgentRunOptions() []fx.Option {
	return append(declareBaseOptions(),
		postgres.Module,
		anthropic.Module,
		embeddings.Module,
		llmusage.Module,
		knowledge.Module,
		kbtool.Module,
		editplan.Module,
		agents.Module,
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
