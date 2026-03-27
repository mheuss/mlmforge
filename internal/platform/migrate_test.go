package platform

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

	_, _ = pool.Exec(ctx, "DROP TABLE IF EXISTS schema_migrations, tree_nodes, events")

	cleanup := RunMigrationsForTest(t, dsn)
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

	_, _ = pool.Exec(ctx, "DROP TABLE IF EXISTS schema_migrations, tree_nodes, events")

	migrationsPath := FindMigrationsDir(t)

	err = MigrateUp(dsn, migrationsPath)
	require.NoError(t, err)
	assert.True(t, tableExists(t, pool, "events"), "events table should exist after up")

	for {
		err := MigrateDown(dsn, migrationsPath)
		if err != nil {
			break
		}
	}

	assert.False(t, tableExists(t, pool, "events"), "events table should not exist after full rollback")
}
