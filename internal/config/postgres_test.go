package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPostgresDSN(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    Postgres
		expected string
	}{
		{
			name: "plain credentials",
			input: Postgres{
				Host: "localhost", Port: 5432, User: "agent", Password: "secret",
				Database: "ai_agents", SSLMode: "disable",
			},
			expected: "postgres://agent:secret@localhost:5432/ai_agents?sslmode=disable",
		},
		{
			name: "special characters are escaped",
			input: Postgres{
				Host: "db", Port: 6432, User: "app", Password: "p@ss/w:rd",
				Database: "agents", SSLMode: "require",
			},
			expected: "postgres://app:p%40ss%2Fw%3Ard@db:6432/agents?sslmode=require",
		},
		{
			name: "ipv6 host",
			input: Postgres{
				Host: "::1", Port: 5432, User: "root", Password: "pwd",
				Database: "main", SSLMode: "prefer",
			},
			expected: "postgres://root:pwd@[::1]:5432/main?sslmode=prefer",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, tt.input.DSN())
		})
	}
}
