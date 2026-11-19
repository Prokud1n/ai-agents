package config

import "time"

// Whisper configures speech recognition through the OpenAI /audio/transcriptions endpoint.
// The API key and base URL come from the shared OpenAI config.
type Whisper struct {
	// Word-level timestamps are supported only by whisper-1.
	Model string `env:"WHISPER_MODEL"            envDefault:"whisper-1"`
	// Default speech language (ISO 639-1); empty means auto-detect.
	Language       string        `env:"WHISPER_LANGUAGE"         envDefault:"ru"`
	RequestTimeout time.Duration `env:"WHISPER_REQUEST_TIMEOUT"  envDefault:"5m"`
	// Retries on 429 and 5xx responses and network errors; 0 disables retries.
	MaxRetries     int           `env:"WHISPER_MAX_RETRIES"      envDefault:"3"`
	RetryBaseDelay time.Duration `env:"WHISPER_RETRY_BASE_DELAY" envDefault:"2s"`
}
