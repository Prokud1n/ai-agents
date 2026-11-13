package config

import (
	"go.uber.org/fx"
)

type App struct {
	fx.Out

	Logging Logging
}
