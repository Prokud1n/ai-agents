package config

// LLM configures agent runs and usage accounting.
type LLM struct {
	DefaultModel     string `env:"LLM_DEFAULT_MODEL"      envDefault:"claude-sonnet-5-5"`
	DefaultMaxTokens int    `env:"LLM_DEFAULT_MAX_TOKENS" envDefault:"8192"`
	// Prices in USD per million tokens: "model=input/output/cache_write/cache_read,...".
	// Defaults: https://platform.claude.com/docs/en/about-claude/pricing (cache_write is the 5-minute cache).
	// A model without a price is still accounted in tokens, its cost is NULL.
	Prices string `env:"LLM_PRICES" envDefault:"claude-sonnet-5-5=2/10/2.5/0.1,claude-opus-5-5=4/20/5/0.2"`
}
