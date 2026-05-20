package db

import (
	"errors"
	"fmt"
	"log/slog"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
)

// Migrate runs all migrations under migrationsDir against the database
// pointed to by dsn. Safe to call on each boot; no-op if up-to-date.
func Migrate(dsn, migrationsDir string, log *slog.Logger) error {
	m, err := migrate.New("file://"+migrationsDir, dsn)
	if err != nil {
		return fmt.Errorf("db: migrate.New: %w", err)
	}
	defer func() {
		_, _ = m.Close()
	}()

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("db: migrate up: %w", err)
	}
	v, dirty, err := m.Version()
	if err != nil && !errors.Is(err, migrate.ErrNilVersion) {
		return fmt.Errorf("db: migrate version: %w", err)
	}
	log.Info("migrations applied", "version", v, "dirty", dirty)
	return nil
}
