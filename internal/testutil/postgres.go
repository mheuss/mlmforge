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

// RequirePostgresInCI ends the run when StartPostgres failed and CI is set.
//
// Call it from TestMain immediately after StartPostgres, before m.Run(). go
// test counts a skipped test as a success, so a CI run whose container never
// started would report green having asserted nothing about any Postgres seam
// (HEU-678). With CI unset it reports and returns, leaving the per-test
// container checks to skip as they always have.
//
// Exported: an external test package calls it.
func RequirePostgresInCI(err error) {
	if err == nil {
		return
	}
	ci := os.Getenv("CI")
	if postgresStartupIsFatal(err, ci) {
		fmt.Fprintf(os.Stderr, "Postgres container failed to start and CI=%q; not skipping (HEU-678): %v\n", ci, err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "Postgres container unavailable: %v\n", err)
}

// postgresStartupIsFatal reports whether a failed StartPostgres should end the
// run rather than let the per-test container checks skip.
func postgresStartupIsFatal(err error, ci string) bool {
	return err != nil && ci != ""
}

// Terminate stops and removes the container.
func (c *PostgresContainer) Terminate() {
	if c != nil && c.container != nil {
		_ = c.container.Terminate(context.Background())
	}
}

// NewPool creates a connection pool and truncates all application tables
// for test isolation. The pool is closed when the test finishes.
//
// It takes testing.TB rather than *testing.T so benchmarks can use it too.
// Every existing caller passes a *testing.T, which already satisfies
// testing.TB, so no call site changes.
func (c *PostgresContainer) NewPool(tb testing.TB) *pgxpool.Pool {
	tb.Helper()
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, c.DSN)
	if err != nil {
		tb.Fatalf("create pool: %v", err)
	}
	tb.Cleanup(func() { pool.Close() })

	// One TRUNCATE covers all tables atomically, so the foreign key from
	// commission_results to commission_runs needs neither CASCADE nor a
	// particular order.
	_, err = pool.Exec(ctx, "TRUNCATE events, tree_nodes, qualification_history, "+
		"commission_results, commission_runs RESTART IDENTITY")
	if err != nil {
		tb.Fatalf("truncate tables: %v", err)
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
	// project root is three levels up: testutil -> internal -> repo root
	projectRoot := filepath.Dir(filepath.Dir(filepath.Dir(thisFile)))
	dir := filepath.Join(projectRoot, "migrations")
	if _, err := os.Stat(dir); err != nil {
		panic(fmt.Sprintf("migrations directory not found at %s: %v", dir, err))
	}
	return dir
}
