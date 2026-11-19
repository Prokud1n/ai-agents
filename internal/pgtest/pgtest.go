// Package pgtest connects integration tests to a real, migrated PostgreSQL: the one from docker compose.
//
// An integration test has "Integration" in its name and calls Connect, which skips the test unless
// INTEGRATION=1. Run them with `make up && make test-integration` (in CI: the job "compose-smoke").
// The database is configured with the usual POSTGRES_* variables, the same as cmd/migrate;
// the compose service "migrate" has already applied the migrations.
//
// Isolation: go test runs packages in parallel, so each package works with its own tables only and
// cleans them with Truncate at the start of a test; tests that share a table do not call t.Parallel.
package pgtest

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	"go.uber.org/fx/fxtest"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/providers/postgres"
)

const enableEnv = "INTEGRATION"

// Connect returns a client connected to the test database; it is closed when the test ends.
// Without INTEGRATION=1 the test is skipped, so `make test` needs no database.
func Connect(t *testing.T) *postgres.Client {
	t.Helper()

	if os.Getenv(enableEnv) != "1" {
		t.Skip("integration test: run with INTEGRATION=1 against the compose stack (make up && make test-integration)")
	}

	cfg, err := config.ParseMigrate() // Postgres settings from POSTGRES_*, like cmd/migrate
	require.NoError(t, err)

	lc := fxtest.NewLifecycle(t)

	client, err := postgres.NewClient(lc, &cfg.Postgres, slog.New(slog.DiscardHandler))
	require.NoError(t, err)

	lc.RequireStart()
	t.Cleanup(lc.RequireStop)

	return client
}

// Truncate empties the tables and restarts their identity sequences.
func Truncate(t *testing.T, client *postgres.Client, tables ...string) {
	t.Helper()

	quoted := make([]string, 0, len(tables))
	for _, table := range tables {
		quoted = append(quoted, pgx.Identifier{table}.Sanitize())
	}

	query := "TRUNCATE " + strings.Join(quoted, ", ") + " RESTART IDENTITY CASCADE"

	_, err := client.GetWriteConnection().Exec(context.Background(), query)
	require.NoError(t, err)
}
