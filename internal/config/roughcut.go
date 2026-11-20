package config

import (
	"fmt"
	"time"

	"github.com/caarlos0/env/v11"
	"go.uber.org/fx"
)

// RoughCut configures the automatic rough cut (cmd/roughcut); command-line flags override it.
type RoughCut struct {
	// Pauses between words longer than this are cut down to short margins around the words.
	MaxPause time.Duration `env:"ROUGHCUT_MAX_PAUSE"         envDefault:"400ms"`
	// Share of an earlier phrase's words (0..1) said again, in order, for it to count as a failed take.
	RetakeSimilarity float64 `env:"ROUGHCUT_RETAKE_SIMILARITY" envDefault:"0.7"`
	// Fillers cut everywhere; repeated letters are collapsed when compared ("эээ" matches "э").
	Fillers []string `env:"ROUGHCUT_FILLERS" envDefault:"э,эм,м,хм,uh,um,hm,er,erm" envSeparator:","`
}

// RoughCutCommand is the config of the standalone roughcut command. Without -transcript it transcribes
// the video itself, so it reads the transcription settings too; none of them is required to parse.
type RoughCutCommand struct {
	fx.Out

	Logging       Logging
	OpenAI        *OpenAI        `env:",init"`
	Whisper       *Whisper       `env:",init"`
	FFmpeg        *FFmpeg        `env:",init"`
	Transcription *Transcription `env:",init"`
	RoughCut      *RoughCut      `env:",init"`
}

func ParseRoughCut() (RoughCutCommand, error) {
	var cfg RoughCutCommand

	if err := env.Parse(&cfg); err != nil {
		return RoughCutCommand{}, fmt.Errorf("failed to parse roughcut config: %w", err)
	}

	return cfg, nil
}
