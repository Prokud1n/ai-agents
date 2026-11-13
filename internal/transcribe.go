package internal

import (
	"log/slog"

	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/logger"
	"github.com/Prokud1n/ai-agents/internal/providers/ffmpeg"
	"github.com/Prokud1n/ai-agents/internal/providers/whisper"
	"github.com/Prokud1n/ai-agents/internal/transcription"
)

// DeclareTranscribeOptions builds the standalone transcribe command. It reads only the OpenAI and Whisper
// config, so it runs without the database and storage settings. Logs go to stderr: stdout carries the JSON.
// The modules have no lifecycle hooks: the command takes the service with fx.Populate and never calls
// app.Start, so a long transcription is not limited by the fx start timeout.
func DeclareTranscribeOptions() []fx.Option {
	return []fx.Option{
		fx.Provide(config.ParseTranscribe),
		fx.Provide(logger.NewStderr),
		fx.WithLogger(func(log *slog.Logger) fxevent.Logger {
			fxLogger := &fxevent.SlogLogger{Logger: log}
			fxLogger.UseLogLevel(slog.LevelDebug)

			return fxLogger
		}),

		ffmpeg.Module,
		whisper.Module,
		transcription.Module,
	}
}
