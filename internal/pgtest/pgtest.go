// Package pgtest gives integration tests a real, migrated PostgreSQL database of their own.
//
// An integration test has "Integration" in its name and calls Connect, which skips the test unless
// INTEGRATION=1. Run them with `make up && make test-integration` (in CI: the job "compose-smoke").
//
// Rule: with INTEGRATION=1 a test with "Integration" in its name must not skip (no t.Skip of its own,
// no skipping subtests): `make test-integration` treats any "--- SKIP" as a failure, because a skipped
// test proves nothing. A test that cannot run in that environment fails or does not have "Integration"
// in its name.
// The server is configured with the usual POSTGRES_* variables, the same as cmd/migrate.
//
// Isolation: Connect creates a new database for every test, applies all migrations to it and drops it
// when the test ends. Tests never see each other's rows or the data in POSTGRES_DB, so they may call
// t.Parallel. The configured user needs the CREATEDB privilege (the compose postgres user has it).
package pgtest

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	"go.uber.org/fx/fxtest"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/migrator"
	"github.com/Prokud1n/ai-agents/internal/providers/postgres"
)

const (
	enableEnv = "INTEGRATION"
	// Prefix of the temporary databases: one left behind by a killed test run can be found and dropped by it.
	databasePrefix = "pgtest_"
)

// Connect creates a fresh database with all migrations applied and returns a client for it; the client
// is closed and the database dropped when the test ends. Without INTEGRATION=1 the test is skipped,
// so `make test` needs no database.
func Connect(t *testing.T) *postgres.Client {
	t.Helper()

	if os.Getenv(enableEnv) != "1" {
		t.Skip("integration test: run with INTEGRATION=1 against the compose stack (make up && make test-integration)")
	}

	cfg, err := config.ParseMigrate() // Postgres settings from POSTGRES_*, like cmd/migrate
	require.NoError(t, err)

	testCfg := createDatabase(t, &cfg.Postgres)
	log := slog.New(slog.DiscardHandler)

	require.NoError(t, migrator.NewMigrator(&testCfg, log).Up(context.Background()))

	lc := fxtest.NewLifecycle(t)

	client, err := postgres.NewClient(lc, &testCfg, log)
	require.NoError(t, err)

	lc.RequireStart()
	t.Cleanup(lc.RequireStop) // runs before the database is dropped: cleanups run in reverse order

	return client
}

// createDatabase creates a uniquely named database next to the configured one and registers its removal.
func createDatabase(t *testing.T, cfg *config.Postgres) config.Postgres {
	t.Helper()

	ctx := context.Background()

	admin, err := pgx.Connect(ctx, cfg.DSN())
	require.NoError(t, err)

	// Unique across parallel tests and across test binaries of different packages running at once.
	name := fmt.Sprintf("%s%d_%d", databasePrefix, os.Getpid(), time.Now().UnixNano())
	ident := pgx.Identifier{name}.Sanitize()

	_, err = admin.Exec(ctx, "CREATE DATABASE "+ident)
	require.NoError(t, err)

	t.Cleanup(func() {
		// FORCE closes connections the test may have leaked.
		if _, err := admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+ident+" WITH (FORCE)"); err != nil {
			t.Errorf("failed to drop test database %s: %v", name, err)
		}

		if err := admin.Close(context.Background()); err != nil {
			t.Errorf("failed to close admin connection: %v", err)
		}
	})

	testCfg := *cfg
	testCfg.Database = name

	return testCfg
}
