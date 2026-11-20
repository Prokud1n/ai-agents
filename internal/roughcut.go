package internal

import (
	"log/slog"

	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/logger"
	"github.com/Prokud1n/ai-agents/internal/providers/ffmpeg"
	"github.com/Prokud1n/ai-agents/internal/providers/whisper"
	"github.com/Prokud1n/ai-agents/internal/roughcut"
	"github.com/Prokud1n/ai-agents/internal/transcription"
)

// DeclareRoughCutOptions builds the standalone roughcut command: the rough cut service and, for videos
// without a ready transcript, the transcription service. Like transcribe it reads no database or storage
// settings, logs to stderr and is used with fx.Populate without app.Start, so a long render is not limited
// by the fx start timeout.
func DeclareRoughCutOptions() []fx.Option {
	return []fx.Option{
		fx.Provide(config.ParseRoughCut),
		fx.Provide(logger.NewStderr),
		fx.WithLogger(func(log *slog.Logger) fxevent.Logger {
			fxLogger := &fxevent.SlogLogger{Logger: log}
			fxLogger.UseLogLevel(slog.LevelDebug)

			return fxLogger
		}),

		ffmpeg.Module,
		whisper.Module,
		transcription.Module,
		roughcut.Module,
	}
}
