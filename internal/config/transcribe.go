package config

import (
	"fmt"

	"github.com/caarlos0/env/v11"
	"go.uber.org/fx"
)

// Transcribe is the config of the standalone transcribe command: it needs neither the database nor storage,
// so it does not require their settings.
type Transcribe struct {
	fx.Out

	Logging Logging
	OpenAI  *OpenAI  `env:",init"`
	Whisper *Whisper `env:",init"`
}

func ParseTranscribe() (Transcribe, error) {
	var cfg Transcribe

	if err := env.Parse(&cfg); err != nil {
		return Transcribe{}, fmt.Errorf("failed to parse transcribe config: %w", err)
	}

	return cfg, nil
}
