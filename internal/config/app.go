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
	Migrations *Migrations `env:",init"`
	Telegram   *Telegram   `env:",init"`
}
