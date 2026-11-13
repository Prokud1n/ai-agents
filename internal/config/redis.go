package config

type Redis struct {
	Address  string `env:"REDIS_ADDRESS"  envDefault:"localhost:6379"`
	Password string `env:"REDIS_PASSWORD"`
	DB       int    `env:"REDIS_DB"       envDefault:"0"`
}
