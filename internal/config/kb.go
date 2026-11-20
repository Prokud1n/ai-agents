package config

import (
	"fmt"

	"github.com/caarlos0/env/v11"
	"go.uber.org/fx"
)

// KB is the config of the standalone kb command (cmd/kb): database and embeddings only, so it needs
// no Redis or S3 settings.
type KB struct {
	fx.Out

	Logging       Logging
	Postgres      *Postgres      `env:",init"`
	OpenAI        *OpenAI        `env:",init"`
	Embeddings    *Embeddings    `env:",init"`
	KnowledgeBase *KnowledgeBase `env:",init"`
}

func ParseKB() (KB, error) {
	var cfg KB

	if err := env.Parse(&cfg); err != nil {
		return KB{}, fmt.Errorf("failed to parse kb config: %w", err)
	}

	return cfg, nil
}
