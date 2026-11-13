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
	"go.uber.org/fx"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/migrations"
)

const (
	sourceName     = "iofs"
	databaseScheme = "pgx5"
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
	fsys, err := newMigrateFS(migrations.FS)
	if err != nil {
		return fmt.Errorf("failed to prepare migrations: %w", err)
	}

	source, err := iofs.New(fsys, ".")
	if err != nil {
		return fmt.Errorf("failed to open migrations source: %w", err)
	}

	databaseURL, err := migrateURL(m.cfg.DSN())
	if err != nil {
		return err
	}

	mg, err := migrate.NewWithSourceInstance(sourceName, source, databaseURL)
	if err != nil {
		return fmt.Errorf("failed to init migrations: %w", err)
	}

	defer func() {
		sourceErr, databaseErr := mg.Close()
		if closeErr := errors.Join(sourceErr, databaseErr); closeErr != nil {
			m.log.ErrorContext(ctx, "migrator: failed to close", "error", closeErr)
		}
	}()

	if err := mg.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("failed to apply migrations: %w", err)
	}

	version, dirty, err := mg.Version()
	if err != nil && !errors.Is(err, migrate.ErrNilVersion) {
		return fmt.Errorf("failed to get migrations version: %w", err)
	}

	m.log.InfoContext(ctx, "migrator: migrations applied", "version", version, "dirty", dirty)

	return nil
}

// RunOnStart applies migrations on application start when enabled in config.
func RunOnStart(lc fx.Lifecycle, cfg *config.Migrations, m *Migrator) {
	if !cfg.RunOnStart {
		return
	}

	lc.Append(fx.StartHook(m.Up))
}

// RunAndShutdown applies migrations and stops the application: used by the standalone migrate command.
func RunAndShutdown(lc fx.Lifecycle, m *Migrator, shutdowner fx.Shutdowner) {
	lc.Append(fx.StartHook(func(ctx context.Context) error {
		if err := m.Up(ctx); err != nil {
			return err
		}

		if err := shutdowner.Shutdown(); err != nil {
			return fmt.Errorf("failed to shut down after migrations: %w", err)
		}

		return nil
	}))
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
