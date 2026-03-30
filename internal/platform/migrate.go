package platform

import (
	"fmt"
	"path/filepath"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
)

// migrationSourceURL converts a migrations directory path to a file:// URL,
// normalizing to an absolute path so behavior is not CWD-dependent.
func migrationSourceURL(migrationsPath string) (string, error) {
	absPath, err := filepath.Abs(migrationsPath)
	if err != nil {
		return "", fmt.Errorf("resolve migrations path: %w", err)
	}
	return fmt.Sprintf("file://%s", absPath), nil
}

// MigrateUp applies all pending database migrations from the given directory.
func MigrateUp(dbURL, migrationsPath string) error {
	sourceURL, err := migrationSourceURL(migrationsPath)
	if err != nil {
		return err
	}
	m, err := migrate.New(sourceURL, dbURL)
	if err != nil {
		return fmt.Errorf("create migrator: %w", err)
	}
	defer func() { _, _ = m.Close() }()

	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}

// ErrNoChange is returned by MigrateDown when there are no migrations to roll back.
var ErrNoChange = migrate.ErrNoChange

// MigrateDown rolls back the most recent migration.
// Returns ErrNoChange when there are no migrations left to roll back.
func MigrateDown(dbURL, migrationsPath string) error {
	sourceURL, err := migrationSourceURL(migrationsPath)
	if err != nil {
		return err
	}
	m, err := migrate.New(sourceURL, dbURL)
	if err != nil {
		return fmt.Errorf("create migrator: %w", err)
	}
	defer func() { _, _ = m.Close() }()

	if err := m.Steps(-1); err != nil {
		if err == migrate.ErrNoChange {
			return ErrNoChange
		}
		return fmt.Errorf("rollback migration: %w", err)
	}
	return nil
}

// MigrateVersion returns the current migration version and dirty flag.
func MigrateVersion(dbURL, migrationsPath string) (uint, bool, error) {
	sourceURL, err := migrationSourceURL(migrationsPath)
	if err != nil {
		return 0, false, err
	}
	m, err := migrate.New(sourceURL, dbURL)
	if err != nil {
		return 0, false, fmt.Errorf("create migrator: %w", err)
	}
	defer func() { _, _ = m.Close() }()

	version, dirty, err := m.Version()
	if err != nil {
		if err == migrate.ErrNilVersion {
			return 0, false, nil
		}
		return 0, false, err
	}
	return version, dirty, nil
}
