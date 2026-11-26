package internal

import (
	"log/slog"

	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	"github.com/Prokud1n/ai-agents/internal/agents"
	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/editjob"
	"github.com/Prokud1n/ai-agents/internal/editlocal"
	"github.com/Prokud1n/ai-agents/internal/editplan"
	"github.com/Prokud1n/ai-agents/internal/ideas"
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
)

// DeclareEditLocalOptions builds the edit-local CLI (make edit): the editor of the app — its executor, the editor's
// model with usage accounting and all the agents' tools (the registry loads every agent) — with Redis, S3 and the
// board of the media worker, without the Telegram gateway, the task queue, teamchat or the HTTP server. Logs go to
// stderr: stdout carries the summary. The edit runs between app.Start and app.Stop, not in a start hook.
func DeclareEditLocalOptions() []fx.Option {
	return []fx.Option{
		fx.Provide(config.Parse),
		fx.Provide(logger.NewStderr),
		fx.WithLogger(func(log *slog.Logger) fxevent.Logger {
			fxLogger := &fxevent.SlogLogger{Logger: log}
			fxLogger.UseLogLevel(slog.LevelDebug)

			return fxLogger
		}),

		postgres.Module,
		redis.Module,
		s3.Module,
		anthropic.Module,
		embeddings.Module,
		llmusage.Module,
		knowledge.Module,
		kbtool.Module,
		editplan.Module,
		ideas.ToolModule,
		agents.Module,
		media.Module,
		editjob.Module,
		editlocal.Module,
	}
}
