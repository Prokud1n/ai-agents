package config

type Migrations struct {
	// Off by default: in docker compose migrations run in the separate "migrate" service.
	RunOnStart bool `env:"MIGRATIONS_RUN_ON_START" envDefault:"false"`
}
