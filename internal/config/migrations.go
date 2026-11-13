package config

import (
	"fmt"

	"github.com/caarlos0/env/v11"
)

// Migrate is the config of the standalone migrate command (cmd/migrate): it needs only the database
// and logging, so it does not require settings of other subsystems (S3 keys and so on).
type Migrate struct {
	Logging  Logging
	Postgres Postgres
}

func ParseMigrate() (Migrate, error) {
	var cfg Migrate

	if err := env.Parse(&cfg); err != nil {
		return Migrate{}, fmt.Errorf("failed to parse migrate config: %w", err)
	}

	return cfg, nil
}
