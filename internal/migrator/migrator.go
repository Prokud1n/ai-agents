package migrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5" // registers the "pgx5" database driver
	"github.com/golang-migrate/migrate/v4/source/iofs"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/migrations"
)

const (
	sourceName     = "iofs"
	databaseScheme = "pgx5"
	downOneStep    = -1
)

type Migrator struct {
	cfg *config.Postgres
	log *slog.Logger
}

func NewMigrator(cfg *config.Postgres, log *slog.Logger) *Migrator {
	return &Migrator{cfg: cfg, log: log}
}

// Up applies all pending migrations. golang-migrate takes a Postgres advisory lock,
// so several instances starting at once do not race.
func (m *Migrator) Up(ctx context.Context) error {
	return m.run(ctx, UpCommand, func(mg *migrate.Migrate) error {
		if err := mg.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
			return fmt.Errorf("failed to apply migrations: %w", err)
		}

		return nil
	})
}

// Down rolls back exactly one migration.
func (m *Migrator) Down(ctx context.Context) error {
	return m.run(ctx, DownCommand, func(mg *migrate.Migrate) error {
		if err := mg.Steps(downOneStep); err != nil {
			return fmt.Errorf("failed to roll back migration: %w", err)
		}

		return nil
	})
}

// Run executes the given command.
func (m *Migrator) Run(ctx context.Context, cmd Command) error {
	if cmd == DownCommand {
		return m.Down(ctx)
	}

	return m.Up(ctx)
}

func (m *Migrator) run(ctx context.Context, cmd Command, action func(*migrate.Migrate) error) error {
	mg, err := m.newMigrate()
	if err != nil {
		return err
	}

	defer func() {
		sourceErr, databaseErr := mg.Close()
		if closeErr := errors.Join(sourceErr, databaseErr); closeErr != nil {
			m.log.ErrorContext(ctx, "migrator: failed to close", "error", closeErr)
		}
	}()

	// On cancellation (SIGINT/SIGTERM in cmd/migrate) ask golang-migrate to stop gracefully: it never
	// interrupts the migration in progress, lets it finish and does not start the next one.
	// Nothing else cancels ctx: there is deliberately no timeout, because a migration killed midway
	// leaves the database "dirty" and needs manual repair.
	done := make(chan struct{})
	defer close(done)

	go func() {
		select {
		case <-ctx.Done():
			mg.GracefulStop <- true
		case <-done:
		}
	}()

	if err := action(mg); err != nil {
		return err
	}

	version, dirty, err := mg.Version()
	if err != nil && !errors.Is(err, migrate.ErrNilVersion) {
		return fmt.Errorf("failed to get migrations version: %w", err)
	}

	m.log.InfoContext(ctx, "migrator: done", "command", cmd, "version", version, "dirty", dirty)

	// A graceful stop returns no error from golang-migrate, but the command is not complete:
	// report it, so the "migrate" service does not exit successfully and app does not start.
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("failed to finish migrations, stopped by signal: %w", err)
	}

	return nil
}

func (m *Migrator) newMigrate() (*migrate.Migrate, error) {
	source, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return nil, fmt.Errorf("failed to open migrations source: %w", err)
	}

	databaseURL, err := migrateURL(m.cfg.DSN())
	if err != nil {
		return nil, err
	}

	mg, err := migrate.NewWithSourceInstance(sourceName, source, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("failed to init migrations: %w", err)
	}

	return mg, nil
}

// migrateURL turns a postgres DSN into a URL for the golang-migrate pgx/v5 driver.
func migrateURL(dsn string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", fmt.Errorf("failed to parse postgres dsn: %w", err)
	}

	u.Scheme = databaseScheme

	return u.String(), nil
}
