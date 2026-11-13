package config

import (
	"net"
	"net/url"
	"strconv"
)

type Postgres struct {
	Host     string `env:"POSTGRES_HOST"     envDefault:"localhost"`
	Port     int    `env:"POSTGRES_PORT"     envDefault:"5432"`
	User     string `env:"POSTGRES_USER"     envDefault:"postgres"`
	Password string `env:"POSTGRES_PASSWORD"`
	Database string `env:"POSTGRES_DB"       envDefault:"ai_agents"`
	SSLMode  string `env:"POSTGRES_SSL_MODE" envDefault:"disable"`
}

// DSN returns a connection URL; credentials are escaped, so they may contain any characters.
func (p *Postgres) DSN() string {
	dsn := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(p.User, p.Password),
		Host:     net.JoinHostPort(p.Host, strconv.Itoa(p.Port)),
		Path:     "/" + p.Database,
		RawQuery: url.Values{"sslmode": {p.SSLMode}}.Encode(),
	}

	return dsn.String()
}
