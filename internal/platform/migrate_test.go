package platform

import (
	"context"
	"errors"
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
	if pgContainer == nil {
		t.Skip("Postgres container not available")
	}

	// The container already has migrations applied. Create a fresh pool
	// to verify the tables exist.
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, pgContainer.DSN)
	require.NoError(t, err)
	t.Cleanup(func() { pool.Close() })

	assert.True(t, tableExists(t, pool, "events"), "events table should exist after MigrateUp")
	assert.True(t, tableExists(t, pool, "tree_nodes"), "tree_nodes table should exist after MigrateUp")
}

func TestMigrateUpDown(t *testing.T) {
	if pgContainer == nil {
		t.Skip("Postgres container not available")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, pgContainer.DSN)
	require.NoError(t, err)
	t.Cleanup(func() { pool.Close() })

	migrationsPath := FindMigrationsDir(t)

	// Roll all migrations down, then back up to verify the full cycle.
	// The loop terminates on ErrNoChange or os.ErrNotExist. The latter
	// occurs when golang-migrate has rolled back past all numbered
	// migration files.
	for {
		err := MigrateDown(pgContainer.DSN, migrationsPath)
		if err == nil {
			continue
		}
		if errors.Is(err, ErrNoChange) || errors.Is(err, os.ErrNotExist) {
			break
		}
		require.NoError(t, err, "unexpected rollback error")
	}

	assert.False(t, tableExists(t, pool, "events"), "events table should not exist after full rollback")
	assert.False(t, tableExists(t, pool, "tree_nodes"), "tree_nodes table should not exist after full rollback")

	// Re-apply migrations so subsequent tests still have tables.
	err = MigrateUp(pgContainer.DSN, migrationsPath)
	require.NoError(t, err)
	assert.True(t, tableExists(t, pool, "events"), "events table should exist after up")
	assert.True(t, tableExists(t, pool, "tree_nodes"), "tree_nodes table should exist after up")
}
