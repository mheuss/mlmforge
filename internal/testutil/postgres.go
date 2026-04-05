package testutil

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// PostgresContainer holds a running Postgres test container.
type PostgresContainer struct {
	DSN       string
	container testcontainers.Container
}

// StartPostgres starts a throwaway Postgres container and runs migrations.
// Call Terminate() when done (typically in TestMain after m.Run()).
func StartPostgres() (*PostgresContainer, error) {
	ctx := context.Background()

	container, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("mlmforge_test"),
		postgres.WithUsername("test"),
		postgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(30*time.Second),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("start postgres container: %w", err)
	}

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		_ = container.Terminate(ctx)
		return nil, fmt.Errorf("get connection string: %w", err)
	}

	if err := migrateUp(dsn, findMigrationsDir()); err != nil {
		_ = container.Terminate(ctx)
		return nil, fmt.Errorf("run migrations: %w", err)
	}

	return &PostgresContainer{
		DSN:       dsn,
		container: container,
	}, nil
}

// Terminate stops and removes the container.
func (c *PostgresContainer) Terminate() {
	if c != nil && c.container != nil {
		_ = c.container.Terminate(context.Background())
	}
}

// NewPool creates a connection pool and truncates all application tables
// for test isolation. The pool is closed when the test finishes.
func (c *PostgresContainer) NewPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, c.DSN)
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}
	t.Cleanup(func() { pool.Close() })

	_, err = pool.Exec(ctx, "TRUNCATE events, tree_nodes RESTART IDENTITY")
	if err != nil {
		t.Fatalf("truncate tables: %v", err)
	}

	return pool
}

// migrateUp applies all pending migrations. Uses golang-migrate directly
// to avoid an import cycle with the platform package.
func migrateUp(dbURL, migrationsPath string) error {
	absPath, err := filepath.Abs(migrationsPath)
	if err != nil {
		return fmt.Errorf("resolve migrations path: %w", err)
	}
	sourceURL := fmt.Sprintf("file://%s", absPath)

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

// findMigrationsDir locates the migrations/ directory relative to the
// project root by walking up from this source file.
func findMigrationsDir() string {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		panic("cannot determine source file path")
	}
	// thisFile is internal/testutil/postgres.go
	// project root is two levels up
	projectRoot := filepath.Dir(filepath.Dir(filepath.Dir(thisFile)))
	dir := filepath.Join(projectRoot, "migrations")
	if _, err := os.Stat(dir); err != nil {
		panic(fmt.Sprintf("migrations directory not found at %s: %v", dir, err))
	}
	return dir
}
