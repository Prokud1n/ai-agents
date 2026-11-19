package config

import (
	"fmt"

	"github.com/caarlos0/env/v11"
	"go.uber.org/fx"
)

// Transcribe is the config of the standalone transcribe command: it needs neither the database nor storage,
// so it does not require their settings. The Whisper, ffmpeg and transcription settings live only here
// until the transcription modules are wired into the app, so they cannot break the app start.
type Transcribe struct {
	fx.Out

	Logging       Logging
	OpenAI        *OpenAI        `env:",init"`
	Whisper       *Whisper       `env:",init"`
	FFmpeg        *FFmpeg        `env:",init"`
	Transcription *Transcription `env:",init"`
}

func ParseTranscribe() (Transcribe, error) {
	var cfg Transcribe

	if err := env.Parse(&cfg); err != nil {
		return Transcribe{}, fmt.Errorf("failed to parse transcribe config: %w", err)
	}

	return cfg, nil
}
