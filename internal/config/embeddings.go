package config

import "time"

// Embeddings configures the embeddings provider; the API key and base URL come from OpenAI.
type Embeddings struct {
	// The model must return 1536-dimensional vectors: the size of the vector columns in the database.
	Model          string        `env:"EMBEDDINGS_MODEL"            envDefault:"text-embedding-3-small"`
	BatchSize      int           `env:"EMBEDDINGS_BATCH_SIZE"       envDefault:"100"`
	MaxRetries     int           `env:"EMBEDDINGS_MAX_RETRIES"      envDefault:"4"`
	RetryBaseDelay time.Duration `env:"EMBEDDINGS_RETRY_BASE_DELAY" envDefault:"1s"`
	Timeout        time.Duration `env:"EMBEDDINGS_TIMEOUT"          envDefault:"60s"`
}
