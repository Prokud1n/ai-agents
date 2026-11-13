package internal

import (
	"fmt"
	"log/slog"
	"os"

	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/providers/ffmpeg"
	"github.com/Prokud1n/ai-agents/internal/providers/whisper"
	"github.com/Prokud1n/ai-agents/internal/transcription"
)

// DeclareTranscribeOptions builds the standalone transcribe command. It reads only the OpenAI and Whisper
// config, so it runs without the database and storage settings. Logs go to stderr: stdout carries the JSON.
func DeclareTranscribeOptions() []fx.Option {
	return []fx.Option{
		fx.Provide(config.ParseTranscribe),
		fx.Provide(NewStderrLogger),
		fx.WithLogger(func(log *slog.Logger) fxevent.Logger {
			logger := &fxevent.SlogLogger{Logger: log}
			logger.UseLogLevel(slog.LevelDebug)

			return logger
		}),

		ffmpeg.Module,
		whisper.Module,
		transcription.Module,
	}
}

// NewStderrLogger is the logger of command-line tools whose stdout is their result.
func NewStderrLogger(cfg config.Logging) (*slog.Logger, error) {
	var level slog.Level
	if err := level.UnmarshalText([]byte(cfg.LogLevel)); err != nil {
		return nil, fmt.Errorf("failed to parse log level: %w", err)
	}

	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level})), nil
}
