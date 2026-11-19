package config

import "time"

// Transcription configures how long audio is split into chunks for Whisper.
type Transcription struct {
	// Pauses for cutting chunks: quieter than SilenceNoiseDB for at least SilenceMinDuration.
	// Raise the level (e.g. -25) for videos with background music, where -30 dB finds no pauses.
	SilenceNoiseDB     float64       `env:"TRANSCRIPTION_SILENCE_NOISE_DB"     envDefault:"-30"`
	SilenceMinDuration time.Duration `env:"TRANSCRIPTION_SILENCE_MIN_DURATION" envDefault:"500ms"`
}
