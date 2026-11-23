package internal

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/caarlos0/env/v11"
	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/editjob"
	"github.com/Prokud1n/ai-agents/internal/logger"
	"github.com/Prokud1n/ai-agents/internal/media"
	"github.com/Prokud1n/ai-agents/internal/mediaworker"
	"github.com/Prokud1n/ai-agents/internal/providers/ffmpeg"
	"github.com/Prokud1n/ai-agents/internal/providers/redis"
	"github.com/Prokud1n/ai-agents/internal/providers/s3"
	"github.com/Prokud1n/ai-agents/internal/providers/whisper"
	"github.com/Prokud1n/ai-agents/internal/roughcut"
	"github.com/Prokud1n/ai-agents/internal/subtitles"
	"github.com/Prokud1n/ai-agents/internal/transcription"
)

// DeclareMediaWorkerOptions builds the media worker (cmd/media-worker): the editor's jobs from Redis, the source
// videos from S3 and the editing tools. It reads no Telegram, Claude API or PostgreSQL settings: ffmpeg parses
// files that come from users, so this process holds no secrets of the app but the S3, Redis and OpenAI keys.
// A job runs in the worker's goroutine between start and stop, not in a start hook, so APP_START_TIMEOUT does not
// limit it.
func DeclareMediaWorkerOptions() []fx.Option {
	return []fx.Option{
		fx.Provide(config.ParseMediaWorker),
		fx.Provide(logger.New),
		fx.WithLogger(func(log *slog.Logger) fxevent.Logger {
			fxLogger := &fxevent.SlogLogger{Logger: log}
			fxLogger.UseLogLevel(slog.LevelDebug)

			return fxLogger
		}),

		redis.Module,
		s3.Module,
		media.Module,
		ffmpeg.Module,
		whisper.Module,
		transcription.Module,
		roughcut.Module,
		subtitles.Module,
		editjob.Module,
		mediaworker.Module,
	}
}

func NewMediaWorker() *fx.App {
	return fx.New(append(DeclareMediaWorkerOptions(), lifecycleOptions())...)
}

// CheckMediaWorkerHealth is the container healthcheck of the media worker: its heartbeat file is fresh.
func CheckMediaWorkerHealth(now time.Time) error {
	var cfg config.MediaWorker

	if err := env.Parse(&cfg); err != nil {
		return fmt.Errorf("failed to parse media worker config: %w", err)
	}

	return mediaworker.CheckHeartbeat(mediaworker.HeartbeatFile(&cfg), mediaworker.HeartbeatMaxAge, now)
}
