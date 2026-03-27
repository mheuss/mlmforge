package platform

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runMigrationsForTest applies all migrations and returns a cleanup function
// that rolls them back. The caller should defer the cleanup.
//
// Reused by PostgresTreeStore tests (Task 7) and integration tests (Task 11).
func runMigrationsForTest(t *testing.T, dbURL string) func() {
	t.Helper()

	migrationsPath := findMigrationsDir(t)

	err := MigrateUp(dbURL, migrationsPath)
	require.NoError(t, err, "MigrateUp failed")

	return func() {
		// Roll back all migrations one step at a time.
		for {
			err := MigrateDown(dbURL, migrationsPath)
			if err != nil {
				break
			}
		}
	}
}

// findMigrationsDir walks up from the test file to find the migrations/ directory.
func findMigrationsDir(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	require.NoError(t, err)

	for {
		candidate := filepath.Join(dir, "migrations")
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("migrations/ directory not found")
		}
		dir = parent
	}
}

func tableExists(t *testing.T, pool *pgxpool.Pool, tableName string) bool {
	t.Helper()
	var exists bool
	err := pool.QueryRow(context.Background(),
		`SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'public' AND table_name = $1
		)`, tableName).Scan(&exists)
	require.NoError(t, err)
	return exists
}

func TestMigrateUp(t *testing.T) {
	dsn := os.Getenv("EVENTSTORE_TEST_DSN")
	if dsn == "" {
		t.Skip("EVENTSTORE_TEST_DSN not set, skipping migration tests")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(func() { pool.Close() })

	// Clean slate.
	_, _ = pool.Exec(ctx, "DROP TABLE IF EXISTS schema_migrations, tree_nodes, events")

	cleanup := runMigrationsForTest(t, dsn)
	defer cleanup()

	assert.True(t, tableExists(t, pool, "events"), "events table should exist after MigrateUp")
}

func TestMigrateUpDown(t *testing.T) {
	dsn := os.Getenv("EVENTSTORE_TEST_DSN")
	if dsn == "" {
		t.Skip("EVENTSTORE_TEST_DSN not set, skipping migration tests")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(func() { pool.Close() })

	// Clean slate.
	_, _ = pool.Exec(ctx, "DROP TABLE IF EXISTS schema_migrations, tree_nodes, events")

	migrationsPath := findMigrationsDir(t)

	err = MigrateUp(dsn, migrationsPath)
	require.NoError(t, err)
	assert.True(t, tableExists(t, pool, "events"), "events table should exist after up")

	// Roll back all migrations.
	for {
		err := MigrateDown(dsn, migrationsPath)
		if err != nil {
			break
		}
	}

	assert.False(t, tableExists(t, pool, "events"), "events table should not exist after full rollback")
}
