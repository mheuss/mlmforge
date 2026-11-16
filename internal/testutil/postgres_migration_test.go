package testutil_test

import (
	"context"
	"errors"
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
	testutil.RequirePostgresInCI(err)
	code := m.Run()
	if migrationContainer != nil {
		migrationContainer.Terminate()
	}
	os.Exit(code)
}

// dirtiedBy is the test whose restore cleanup last read the shared container's record as dirty, and the version it read.
var dirtiedBy struct {
	test    string
	version uint
}

// requireCleanContainer stops the test when the shared container's migration record is dirty.
func requireCleanContainer(t *testing.T, m *migrate.Migrate) {
	t.Helper()
	version, dirty, err := m.Version()
	if errors.Is(err, migrate.ErrNilVersion) {
		return
	}
	require.NoError(t, err, "read the shared container's migration record")
	if !dirty {
		return
	}
	if dirtiedBy.test == "" {
		t.Fatalf("the shared container reads version %d, dirty. No earlier test in this run recorded leaving it dirty", version)
	}
	t.Fatalf("the shared container reads version %d, dirty. %s's restore cleanup read version %d, dirty",
		version, dirtiedBy.test, dirtiedBy.version)
}

// restoreHead returns a cleanup that migrates the shared container back to head and fails the test when it cannot.
func restoreHead(t *testing.T, m *migrate.Migrate) func() {
	return func() {
		err := m.Up()
		if err == nil || errors.Is(err, migrate.ErrNoChange) {
			return
		}
		version, dirty, readErr := m.Version()
		if readErr != nil {
			t.Errorf("restoring the shared container to head failed, and reading its migration record then failed with %v: %v", readErr, err)
			return
		}
		if dirty {
			dirtiedBy.test, dirtiedBy.version = t.Name(), version
		}
		t.Errorf("restoring the shared container to head failed, and its migration record then read version %d, dirty=%t: %v", version, dirty, err)
	}
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
	requireCleanContainer(t, m)

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
	// Cleanups are LIFO, so this restore runs before m.Close.
	t.Cleanup(restoreHead(t, m))
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

// TestMigrations_RootUniqueDownUp proves migration 000006's down file works,
// not just that it contains words.
func TestMigrations_RootUniqueDownUp(t *testing.T) {
	if migrationContainer == nil {
		t.Skip("Postgres container not available")
	}

	// Relative to this package's directory — Go sets a test's cwd to the
	// package dir.
	absPath, err := filepath.Abs("../../migrations")
	require.NoError(t, err)
	m, err := migrate.New(fmt.Sprintf("file://%s", absPath), migrationContainer.DSN)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = m.Close() })
	requireCleanContainer(t, m)

	indexExists := func() bool {
		pool, err := pgxpool.New(context.Background(), migrationContainer.DSN)
		require.NoError(t, err)
		defer pool.Close()
		var exists bool
		require.NoError(t, pool.QueryRow(context.Background(),
			`SELECT EXISTS (SELECT 1 FROM pg_indexes
			                WHERE indexname = 'idx_tree_nodes_tree_root_active')`,
		).Scan(&exists))
		return exists
	}

	require.True(t, indexExists(), "index present after full migrate up")
	// Cleanups are LIFO, so this restore runs before m.Close.
	t.Cleanup(restoreHead(t, m))
	// Pinned versions, not Steps(-1): a relative step would roll back
	// whichever migration is newest and fail confusingly. This test is about
	// 000006's down file specifically.
	require.NoError(t, m.Migrate(5), "migrate down to version 5 (drops 000006)")
	require.False(t, indexExists(), "down file actually drops the index")
	require.NoError(t, m.Migrate(6), "migrate back up to version 6")
	require.True(t, indexExists(), "up file restores the index")
	// Restore head. Up reports ErrNoChange when the steps above already
	// reached it, which is not a failure.
	if err := m.Up(); err != nil {
		require.ErrorIs(t, err, migrate.ErrNoChange, "restore the shared container to head")
	}
}

func TestMigrations_RemovedByEventDownUp(t *testing.T) {
	if migrationContainer == nil {
		t.Skip("Postgres container not available")
	}

	// This is relative to this package's directory. Go sets a test's cwd to
	// the package dir.
	absPath, err := filepath.Abs("../../migrations")
	require.NoError(t, err)
	m, err := migrate.New(fmt.Sprintf("file://%s", absPath), migrationContainer.DSN)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = m.Close() })
	requireCleanContainer(t, m)

	// Proves the migration files actually work, not just that they contain words.
	nullableUUIDColumnExists := func() bool {
		pool, err := pgxpool.New(context.Background(), migrationContainer.DSN)
		require.NoError(t, err)
		defer pool.Close()
		var exists bool
		require.NoError(t, pool.QueryRow(context.Background(),
			`SELECT EXISTS (SELECT 1 FROM information_schema.columns
			                WHERE table_schema = 'public'
			                  AND table_name = 'tree_nodes'
			                  AND column_name = 'removed_by_event_id'
			                  AND data_type = 'uuid'
			                  AND is_nullable = 'YES')`,
		).Scan(&exists))
		return exists
	}

	require.True(t, nullableUUIDColumnExists(), "nullable uuid column present after full migrate up")
	// Cleanups are LIFO, so this restore runs before m.Close.
	t.Cleanup(restoreHead(t, m))
	// Pinned versions, not Steps(-1): a relative step would roll back
	// whichever migration is newest and fail confusingly.
	require.NoError(t, m.Migrate(6), "migrate down to version 6 (drops 000007)")
	require.False(t, nullableUUIDColumnExists(), "down file actually drops the column")
	require.NoError(t, m.Migrate(7), "migrate back up to version 7")
	require.True(t, nullableUUIDColumnExists(), "up file restores it as a nullable uuid")
	// Restore head. Up reports ErrNoChange when the steps above already
	// reached it, which is not a failure.
	if err := m.Up(); err != nil {
		require.ErrorIs(t, err, migrate.ErrNoChange, "restore the shared container to head")
	}
}

func TestMigrations_RemovalStampIndexDownUp(t *testing.T) {
	if migrationContainer == nil {
		t.Skip("Postgres container not available")
	}

	// This is relative to this package's directory. Go sets a test's cwd to
	// the package dir.
	absPath, err := filepath.Abs("../../migrations")
	require.NoError(t, err)
	m, err := migrate.New(fmt.Sprintf("file://%s", absPath), migrationContainer.DSN)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = m.Close() })
	requireCleanContainer(t, m)

	indexExists := func() bool {
		pool, err := pgxpool.New(context.Background(), migrationContainer.DSN)
		require.NoError(t, err)
		defer pool.Close()
		var exists bool
		require.NoError(t, pool.QueryRow(context.Background(),
			`SELECT EXISTS (SELECT 1 FROM pg_indexes
			                WHERE indexname = 'idx_tree_nodes_tree_removed_by_event'
			                  AND indexdef LIKE '%WHERE (removed_by_event_id IS NOT NULL)')`,
		).Scan(&exists))
		return exists
	}

	require.True(t, indexExists(), "partial index present after full migrate up")
	// Cleanups are LIFO, so this restore runs before m.Close.
	t.Cleanup(restoreHead(t, m))
	// Pinned versions, not Steps(-1): a relative step would roll back
	// whichever migration is newest and fail confusingly.
	require.NoError(t, m.Migrate(7), "migrate down to version 7 (drops 000008)")
	require.False(t, indexExists(), "down file actually drops the index")
	require.NoError(t, m.Migrate(8), "migrate back up to version 8")
	require.True(t, indexExists(), "up file restores the index")
	// Restore head. Up reports ErrNoChange when the steps above already
	// reached it, which is not a failure.
	if err := m.Up(); err != nil {
		require.ErrorIs(t, err, migrate.ErrNoChange, "restore the shared container to head")
	}
}

// TestMigrations_SnapshotAttemptsDownUp proves migration 000011's files work.
func TestMigrations_SnapshotAttemptsDownUp(t *testing.T) {
	if migrationContainer == nil {
		t.Skip("Postgres container not available")
	}

	absPath, err := filepath.Abs("../../migrations")
	require.NoError(t, err)
	m, err := migrate.New(fmt.Sprintf("file://%s", absPath), migrationContainer.DSN)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = m.Close() })
	requireCleanContainer(t, m)

	tableExists := func() bool {
		pool, err := pgxpool.New(context.Background(), migrationContainer.DSN)
		require.NoError(t, err)
		defer pool.Close()
		var exists bool
		require.NoError(t, pool.QueryRow(context.Background(),
			`SELECT EXISTS (SELECT 1 FROM information_schema.tables
			                WHERE table_schema = 'public' AND table_name = 'tree_snapshot_attempts')`,
		).Scan(&exists))
		return exists
	}

	require.True(t, tableExists(), "table present after full migrate up")
	t.Cleanup(restoreHead(t, m))
	require.NoError(t, m.Migrate(10), "migrate down to version 10 (drops 000011)")
	require.False(t, tableExists(), "down file actually drops the table")
	require.NoError(t, m.Migrate(11), "migrate back up to version 11")
	require.True(t, tableExists(), "up file restores the table")
	if err := m.Up(); err != nil {
		require.ErrorIs(t, err, migrate.ErrNoChange, "restore the shared container to head")
	}
}

func TestPostgresContainer_NewPool_TruncatesSnapshotAttempts(t *testing.T) {
	if migrationContainer == nil {
		t.Skip("Postgres container not available")
	}
	seedPool, err := pgxpool.New(context.Background(), migrationContainer.DSN)
	require.NoError(t, err)
	defer seedPool.Close()
	_, err = seedPool.Exec(context.Background(),
		`INSERT INTO tree_snapshot_attempts (tree_id, version, stored, observed)
		 VALUES ('aaaaaaaa-aaaa-aaaa-aaaa-000000000001', 1000, false, 'seeded by the test')`)
	require.NoError(t, err)

	freshPool := migrationContainer.NewPool(t)

	var count int
	require.NoError(t, freshPool.QueryRow(context.Background(), "SELECT count(*) FROM tree_snapshot_attempts").Scan(&count))
	assert.Equal(t, 0, count, "tree_snapshot_attempts must be empty after NewPool truncate")
}

// TestMigrations_QualificationHistoryPeriodCollationDownUp checks that
// migration 000012 sets and restores period_id's collation.
func TestMigrations_QualificationHistoryPeriodCollationDownUp(t *testing.T) {
	if migrationContainer == nil {
		t.Skip("Postgres container not available")
	}

	absPath, err := filepath.Abs("../../migrations")
	require.NoError(t, err)
	m, err := migrate.New(fmt.Sprintf("file://%s", absPath), migrationContainer.DSN)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = m.Close() })
	requireCleanContainer(t, m)

	// Matched on attnum, so period_id's key position in each index does not
	// matter.
	indexCollations := func() map[string]string {
		pool, err := pgxpool.New(context.Background(), migrationContainer.DSN)
		require.NoError(t, err)
		defer pool.Close()
		rows, err := pool.Query(context.Background(),
			`SELECT i.indexrelid::regclass::text, coll.collname
			   FROM pg_index i
			   CROSS JOIN LATERAL unnest(i.indkey::int2[], i.indcollation::oid[]) AS k(attnum, colloid)
			   JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = k.attnum
			   JOIN pg_collation coll ON coll.oid = k.colloid
			  WHERE i.indrelid = 'public.qualification_history'::regclass
			    AND a.attname = 'period_id'`)
		require.NoError(t, err)
		defer rows.Close()
		got := map[string]string{}
		for rows.Next() {
			var index, collation string
			require.NoError(t, rows.Scan(&index, &collation))
			got[index] = collation
		}
		require.NoError(t, rows.Err())
		return got
	}
	collatedAs := func(collation string) map[string]string {
		return map[string]string{
			"qualification_history_pkey":            collation,
			"qualification_history_user_period_idx": collation,
		}
	}
	columnCollation := func() string {
		pool, err := pgxpool.New(context.Background(), migrationContainer.DSN)
		require.NoError(t, err)
		defer pool.Close()
		var collation string
		require.NoError(t, pool.QueryRow(context.Background(),
			`SELECT coll.collname
			   FROM pg_attribute a
			   JOIN pg_collation coll ON coll.oid = a.attcollation
			  WHERE a.attrelid = 'public.qualification_history'::regclass
			    AND a.attname = 'period_id'`,
		).Scan(&collation))
		return collation
	}

	assert.Equal(t, collatedAs("C"), indexCollations(), "index collations at head")
	require.Equal(t, "C", columnCollation(), "period_id collation at head")
	t.Cleanup(restoreHead(t, m))
	require.NoError(t, m.Migrate(11), "migrate down to version 11 (drops 000012)")
	assert.Equal(t, collatedAs("default"), indexCollations(), "index collations after migrating down to 11")
	require.Equal(t, "default", columnCollation(), "period_id collation after migrating down to 11")
	require.NoError(t, m.Migrate(12), "migrate back up to version 12")
	assert.Equal(t, collatedAs("C"), indexCollations(), "index collations after migrating up to 12")
	require.Equal(t, "C", columnCollation(), "period_id collation after migrating up to 12")
	if err := m.Up(); err != nil {
		require.ErrorIs(t, err, migrate.ErrNoChange, "restore the shared container to head")
	}
}
