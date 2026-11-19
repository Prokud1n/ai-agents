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
// t.Parallel. The configured user needs the CREATEDB privilege, and the migrations run CREATE EXTENSION
// vector in every new database, which needs a superuser unless the extension is trusted or already in
// template1 (the compose postgres user is a superuser).
//
// Databases left behind by a killed test run are dropped by the first Connect of a later run once they
// are older than staleAfter (the creation time is part of the name).
package pgtest

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	// Prefix of the temporary databases: pgtest_<unix seconds>_<pid>_<counter>.
	databasePrefix = "pgtest_"
	staleAfter     = time.Hour
	decimalBase    = 10
	int64Bits      = 64
)

var (
	databaseCounter atomic.Int64 // unique names within one test binary
	dropStaleOnce   sync.Once
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

	dropStaleOnce.Do(func() { dropStaleDatabases(t, admin, time.Now()) })

	// Unique across parallel tests (counter) and test binaries of different packages running at once (pid).
	name := fmt.Sprintf("%s%d_%d_%d", databasePrefix, time.Now().Unix(), os.Getpid(), databaseCounter.Add(1))
	ident := pgx.Identifier{name}.Sanitize()

	if _, err := admin.Exec(ctx, "CREATE DATABASE "+ident); err != nil {
		_ = admin.Close(ctx)

		require.NoError(t, err, "failed to create test database %s", name)
	}

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

// dropStaleDatabases drops pgtest databases created more than staleAfter ago: left by killed runs.
// Failures are only logged: a leftover database does not break the tests.
func dropStaleDatabases(t *testing.T, admin *pgx.Conn, now time.Time) {
	t.Helper()

	ctx := context.Background()

	rows, err := admin.Query(ctx, `SELECT datname FROM pg_database WHERE starts_with(datname, $1)`, databasePrefix)
	if err != nil {
		t.Logf("pgtest: failed to list stale databases: %v", err)

		return
	}

	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Logf("pgtest: failed to list stale databases: %v", err)

		return
	}

	for _, name := range names {
		if !isStale(name, now) {
			continue
		}

		if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Logf("pgtest: failed to drop stale database %s: %v", name, err)
		}
	}
}

// isStale reports whether a pgtest_<unix>_... database was created more than staleAfter before now.
// Names it cannot parse are left alone.
func isStale(name string, now time.Time) bool {
	created, _, ok := strings.Cut(strings.TrimPrefix(name, databasePrefix), "_")
	if !ok {
		return false
	}

	unix, err := strconv.ParseInt(created, decimalBase, int64Bits)
	if err != nil {
		return false
	}

	return now.Sub(time.Unix(unix, 0)) > staleAfter
}
