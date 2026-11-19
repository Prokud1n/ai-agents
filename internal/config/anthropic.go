package config

import "time"

// Anthropic configures the Claude API client. The key is optional: without it the application
// starts, and LLM calls fail with anthropic.ErrAPIKeyMissing.
type Anthropic struct {
	APIKey  string        `env:"ANTHROPIC_API_KEY"`
	BaseURL string        `env:"ANTHROPIC_BASE_URL" envDefault:"https://api.anthropic.com"`
	Timeout time.Duration `env:"ANTHROPIC_TIMEOUT"  envDefault:"5m"` // one HTTP attempt
	// Retries on 429, 5xx and network errors with exponential backoff: base, 2*base, 4*base... capped by max.
	MaxRetries     int           `env:"ANTHROPIC_MAX_RETRIES"      envDefault:"3"`
	RetryBaseDelay time.Duration `env:"ANTHROPIC_RETRY_BASE_DELAY" envDefault:"1s"`
	RetryMaxDelay  time.Duration `env:"ANTHROPIC_RETRY_MAX_DELAY"  envDefault:"30s"`
}
