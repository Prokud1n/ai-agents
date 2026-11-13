package config

import "time"

type HTTP struct {
	Address           string        `env:"HTTP_ADDRESS"             envDefault:":8080"`
	ReadHeaderTimeout time.Duration `env:"HTTP_READ_HEADER_TIMEOUT" envDefault:"5s"`
}
