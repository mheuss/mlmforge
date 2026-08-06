package testutil_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mlmforge/mlmforge/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var migrationContainer *testutil.PostgresContainer

func TestMain(m *testing.M) {
	var err error
	migrationContainer, err = testutil.StartPostgres()
	if err != nil {
		// Skip in environments without Docker; the test below will t.Skip.
		fmt.Fprintf(os.Stderr, "Postgres container unavailable: %v\n", err)
	}
	code := m.Run()
	if migrationContainer != nil {
		migrationContainer.Terminate()
	}
	os.Exit(code)
}

func TestMigrations_QualificationHistoryTableExists(t *testing.T) {
	if migrationContainer == nil {
		t.Skip("Postgres container not available")
	}
	pool, err := pgxpool.New(context.Background(), migrationContainer.DSN)
	require.NoError(t, err)
	defer pool.Close()

	var exists bool
	err = pool.QueryRow(context.Background(),
		`SELECT EXISTS (SELECT 1 FROM information_schema.tables
                        WHERE table_name = 'qualification_history')`,
	).Scan(&exists)
	require.NoError(t, err)
	assert.True(t, exists, "qualification_history table should exist after migrations")
}

func TestPostgresContainer_NewPool_TruncatesQualificationHistory(t *testing.T) {
	if migrationContainer == nil {
		t.Skip("Postgres container not available")
	}

	// Seed a row using an ad-hoc pool (we cannot use NewPool because
	// NewPool truncates).
	seedPool, err := pgxpool.New(context.Background(), migrationContainer.DSN)
	require.NoError(t, err)
	defer seedPool.Close()

	_, err = seedPool.Exec(context.Background(),
		`INSERT INTO qualification_history (period_id, user_id, rank, ordinal)
         VALUES ('2026-05', '00000000-0000-0000-0000-000000000001', 'silver', 2)`,
	)
	require.NoError(t, err)

	// NewPool should truncate.
	freshPool := migrationContainer.NewPool(t)

	var count int
	err = freshPool.QueryRow(context.Background(),
		"SELECT count(*) FROM qualification_history",
	).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 0, count, "qualification_history must be empty after NewPool truncate")
}

// TestMigrations_SlotUniqueDownUp proves migration 000004's down file works,
// not just that it contains words: migrate down to version 3, confirm the
// index is gone, migrate back up to 4, confirm it returns. The source-URL
// construction mirrors testutil.migrateUp. The down/up pair restores the
// schema, so the shared container is left exactly as other tests expect it.
func TestMigrations_SlotUniqueDownUp(t *testing.T) {
	if migrationContainer == nil {
		t.Skip("Postgres container not available")
	}

	// Relative to this package's directory — Go sets a test's cwd to the
	// package dir. testutil.findMigrationsDir resolves the same path via
	// runtime.Caller but is unexported.
	absPath, err := filepath.Abs("../../migrations")
	require.NoError(t, err)
	// The postgres database and file source drivers are registered by
	// internal/testutil's blank imports, which this package already pulls in.
	m, err := migrate.New(fmt.Sprintf("file://%s", absPath), migrationContainer.DSN)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = m.Close() })

	indexExists := func() bool {
		pool, err := pgxpool.New(context.Background(), migrationContainer.DSN)
		require.NoError(t, err)
		defer pool.Close()
		var exists bool
		require.NoError(t, pool.QueryRow(context.Background(),
			`SELECT EXISTS (SELECT 1 FROM pg_indexes
			                WHERE indexname = 'idx_tree_nodes_tree_parent_position_active')`,
		).Scan(&exists))
		return exists
	}

	require.True(t, indexExists(), "index present after full migrate up")
	// Self-heal the shared container on any failure from here down,
	// including a Migrate(3) that dies after dropping the index. Cleanups
	// are LIFO, so this restore fires before m.Close. This is the
	// failure-path net only — the happy path restores head explicitly below,
	// because a discarded error here would leave the shared container short
	// of head and break later tests in another package. On a dirty version
	// Up returns ErrDirty, which is safely discarded.
	t.Cleanup(func() { _ = m.Up() })
	// Pinned to versions 3 and 4, not Steps(-1): a relative step would roll
	// back whichever migration is newest and fail confusingly. This test is
	// about 000004's down file specifically.
	require.NoError(t, m.Migrate(3), "migrate down to version 3 (drops 000004)")
	require.False(t, indexExists(), "down file actually drops the index")
	require.NoError(t, m.Migrate(4), "migrate back up to version 4")
	require.True(t, indexExists(), "up file restores the index")
	// Migrate(3) rolled back every migration above 3, not just 000004, and
	// Migrate(4) restored only 000004. Head is now above 4, so the container
	// needs an explicit trip back or the tables those later migrations create
	// stay missing for every test that follows.
	require.NoError(t, m.Up(), "restore the shared container to head")
}
