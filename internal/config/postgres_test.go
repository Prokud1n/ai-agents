package config

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPostgresDSN(t *testing.T) {
	t.Parallel()

	type dsnParts struct {
		user     string
		password string
		host     string
		database string
		sslMode  string
	}

	tests := []struct {
		name     string
		input    Postgres
		expected dsnParts
	}{
		{
			name: "plain credentials",
			input: Postgres{
				Host: "localhost", Port: 5432, User: "agent", Password: "plain",
				Database: "ai_agents", SSLMode: "disable",
			},
			expected: dsnParts{
				user: "agent", password: "plain", host: "localhost:5432", database: "ai_agents", sslMode: "disable",
			},
		},
		{
			name: "special characters survive escaping",
			input: Postgres{
				Host: "db", Port: 6432, User: "app", Password: "p@ss/w:rd?#",
				Database: "agents", SSLMode: "require",
			},
			expected: dsnParts{
				user: "app", password: "p@ss/w:rd?#", host: "db:6432", database: "agents", sslMode: "require",
			},
		},
		{
			name: "ipv6 host",
			input: Postgres{
				Host: "::1", Port: 5432, User: "root", Password: "pwd",
				Database: "main", SSLMode: "prefer",
			},
			expected: dsnParts{
				user: "root", password: "pwd", host: "[::1]:5432", database: "main", sslMode: "prefer",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dsn, err := url.Parse(tt.input.DSN())
			require.NoError(t, err)

			password, _ := dsn.User.Password()
			got := dsnParts{
				user:     dsn.User.Username(),
				password: password,
				host:     dsn.Host,
				database: dsn.Path[1:],
				sslMode:  dsn.Query().Get("sslmode"),
			}

			assert.Equal(t, "postgres", dsn.Scheme)
			assert.Equal(t, tt.expected, got)
		})
	}
}
