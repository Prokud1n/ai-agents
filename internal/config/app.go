package config

import (
	"go.uber.org/fx"
)

type App struct {
	fx.Out

	Logging    Logging
	HTTP       *HTTP       `env:",init"`
	Postgres   *Postgres   `env:",init"`
	Redis      *Redis      `env:",init"`
	S3         *S3         `env:",init"`
	Telegram   *Telegram   `env:",init"`
	OpenAI     *OpenAI     `env:",init"`
	Embeddings *Embeddings `env:",init"`
	Anthropic  *Anthropic  `env:",init"`
	LLM        *LLM        `env:",init"`
	Agents     *Agents     `env:",init"`
}
