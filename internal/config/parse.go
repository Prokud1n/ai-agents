package config

import (
	"fmt"

	"github.com/caarlos0/env/v11"
)

func Parse() (App, error) {
	var cfg App

	if err := env.Parse(&cfg); err != nil {
		return App{}, fmt.Errorf("failed to parse config: %w", err)
	}

	return cfg, nil
}
