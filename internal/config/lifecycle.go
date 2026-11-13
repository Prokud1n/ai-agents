package config

import (
	"fmt"
	"time"

	"github.com/caarlos0/env/v11"
)

// Lifecycle holds fx start/stop timeouts. They are top-level fx options,
// so this config is parsed before the fx graph is built (see ParseLifecycle).
type Lifecycle struct {
	StartTimeout time.Duration `env:"APP_START_TIMEOUT" envDefault:"15s"`
	StopTimeout  time.Duration `env:"APP_STOP_TIMEOUT"  envDefault:"15s"`
}

func ParseLifecycle() (Lifecycle, error) {
	var cfg Lifecycle

	if err := env.Parse(&cfg); err != nil {
		return Lifecycle{}, fmt.Errorf("failed to parse lifecycle config: %w", err)
	}

	return cfg, nil
}
