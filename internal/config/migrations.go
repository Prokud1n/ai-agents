package config

type Migrations struct {
	RunOnStart bool `env:"MIGRATIONS_RUN_ON_START" envDefault:"true"`
}
