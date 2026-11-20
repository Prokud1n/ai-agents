package internal

import (
	"log/slog"

	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/logger"
	"github.com/Prokud1n/ai-agents/internal/providers/ffmpeg"
	"github.com/Prokud1n/ai-agents/internal/subtitles"
)

// DeclareSubtitlesOptions builds the standalone subtitles command: ffmpeg and the subtitle settings only.
// Logs go to stderr. Like transcribe, the command takes the service with fx.Populate and never calls app.Start,
// so a long render is not limited by the fx start timeout.
func DeclareSubtitlesOptions() []fx.Option {
	return []fx.Option{
		fx.Provide(config.ParseSubtitlesCommand),
		fx.Provide(logger.NewStderr),
		fx.WithLogger(func(log *slog.Logger) fxevent.Logger {
			fxLogger := &fxevent.SlogLogger{Logger: log}
			fxLogger.UseLogLevel(slog.LevelDebug)

			return fxLogger
		}),

		ffmpeg.Module,
		subtitles.Module,
	}
}
