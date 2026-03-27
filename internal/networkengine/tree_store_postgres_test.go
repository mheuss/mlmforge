package networkengine

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mlmforge/mlmforge/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestPostgresTreeStore(t *testing.T) (*PostgresTreeStore, *pgxpool.Pool) {
	t.Helper()

	dsn := os.Getenv("EVENTSTORE_TEST_DSN")
	if dsn == "" {
		t.Skip("EVENTSTORE_TEST_DSN not set, skipping PostgreSQL integration tests")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(func() { pool.Close() })

	// Clean slate: drop all managed tables, run migrations.
	_, _ = pool.Exec(ctx, "DROP TABLE IF EXISTS schema_migrations, tree_nodes, events")
	cleanup := platform.RunMigrationsForTest(t, dsn)
	t.Cleanup(cleanup)

	// Truncate tree_nodes for test isolation.
	_, err = pool.Exec(ctx, "TRUNCATE tree_nodes RESTART IDENTITY")
	require.NoError(t, err)

	return NewPostgresTreeStore(pool), pool
}

func TestPostgresTreeStore_InsertAndGetNode(t *testing.T) {
	store, _ := newTestPostgresTreeStore(t)
	ctx := context.Background()

	node := makeNode("tree-1", "user-a", 0, nil, nil, nil)
	require.NoError(t, store.InsertNode(ctx, node))

	got, err := store.GetNode(ctx, "tree-1", "user-a")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "tree-1", got.TreeID)
	assert.Equal(t, "user-a", got.UserID)
	assert.Equal(t, 0, got.Depth)
	assert.Nil(t, got.ParentID)
	assert.False(t, got.CreatedAt.IsZero())
}

func TestPostgresTreeStore_GetNodeNotFound(t *testing.T) {
	store, _ := newTestPostgresTreeStore(t)
	ctx := context.Background()

	got, err := store.GetNode(ctx, "tree-1", "nonexistent")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestPostgresTreeStore_DeleteNode(t *testing.T) {
	store, _ := newTestPostgresTreeStore(t)
	ctx := context.Background()

	node := makeNode("tree-1", "user-a", 0, nil, nil, nil)
	require.NoError(t, store.InsertNode(ctx, node))
	require.NoError(t, store.DeleteNode(ctx, "tree-1", "user-a"))

	got, err := store.GetNode(ctx, "tree-1", "user-a")
	require.NoError(t, err)
	assert.Nil(t, got, "soft-deleted node should not be returned by GetNode")
}

func TestPostgresTreeStore_GetChildren(t *testing.T) {
	store, _ := newTestPostgresTreeStore(t)
	ctx := context.Background()

	root := makeNode("tree-1", "root", 0, nil, nil, nil)
	child1 := makeNode("tree-1", "child-1", 1, ptr("root"), ptr("root"), intPtr(0))
	child2 := makeNode("tree-1", "child-2", 1, ptr("root"), ptr("root"), intPtr(1))
	child3 := makeNode("tree-1", "child-3", 1, ptr("root"), ptr("root"), intPtr(2))

	require.NoError(t, store.InsertNode(ctx, root))
	require.NoError(t, store.InsertNode(ctx, child1))
	require.NoError(t, store.InsertNode(ctx, child2))
	require.NoError(t, store.InsertNode(ctx, child3))

	children, err := store.GetChildren(ctx, "tree-1", "root")
	require.NoError(t, err)
	assert.Len(t, children, 3)
}

func TestPostgresTreeStore_GetByTree(t *testing.T) {
	store, _ := newTestPostgresTreeStore(t)
	ctx := context.Background()

	require.NoError(t, store.InsertNode(ctx, makeNode("tree-1", "a", 0, nil, nil, nil)))
	require.NoError(t, store.InsertNode(ctx, makeNode("tree-1", "b", 1, ptr("a"), ptr("a"), nil)))
	require.NoError(t, store.InsertNode(ctx, makeNode("tree-2", "x", 0, nil, nil, nil)))

	got, err := store.GetByTree(ctx, "tree-1")
	require.NoError(t, err)
	assert.Len(t, got, 2)
}

func TestPostgresTreeStore_GetByTreeDepthOrdered(t *testing.T) {
	store, _ := newTestPostgresTreeStore(t)
	ctx := context.Background()

	// Insert out of depth order.
	require.NoError(t, store.InsertNode(ctx, makeNode("tree-1", "grandchild", 2, ptr("child"), ptr("child"), nil)))
	require.NoError(t, store.InsertNode(ctx, makeNode("tree-1", "root", 0, nil, nil, nil)))
	require.NoError(t, store.InsertNode(ctx, makeNode("tree-1", "child", 1, ptr("root"), ptr("root"), nil)))

	got, err := store.GetByTreeDepthOrdered(ctx, "tree-1")
	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, 0, got[0].Depth)
	assert.Equal(t, 1, got[1].Depth)
	assert.Equal(t, 2, got[2].Depth)
}

func TestPostgresTreeStore_BulkInsert(t *testing.T) {
	store, _ := newTestPostgresTreeStore(t)
	ctx := context.Background()

	nodes := []TreeNodeRow{
		makeNode("tree-1", "a", 0, nil, nil, nil),
		makeNode("tree-1", "b", 1, ptr("a"), ptr("a"), nil),
		makeNode("tree-1", "c", 1, ptr("a"), ptr("a"), nil),
		makeNode("tree-1", "d", 2, ptr("b"), ptr("b"), nil),
		makeNode("tree-1", "e", 2, ptr("c"), ptr("c"), nil),
	}

	require.NoError(t, store.BulkInsert(ctx, nodes))

	got, err := store.GetByTree(ctx, "tree-1")
	require.NoError(t, err)
	assert.Len(t, got, 5)
}

func TestPostgresTreeStore_DeletedNodeExcludedFromActive(t *testing.T) {
	store, _ := newTestPostgresTreeStore(t)
	ctx := context.Background()

	root := makeNode("tree-1", "root", 0, nil, nil, nil)
	child := makeNode("tree-1", "child", 1, ptr("root"), ptr("root"), nil)

	require.NoError(t, store.InsertNode(ctx, root))
	require.NoError(t, store.InsertNode(ctx, child))
	require.NoError(t, store.DeleteNode(ctx, "tree-1", "child"))

	byTree, err := store.GetByTree(ctx, "tree-1")
	require.NoError(t, err)
	assert.Len(t, byTree, 1)

	children, err := store.GetChildren(ctx, "tree-1", "root")
	require.NoError(t, err)
	assert.Len(t, children, 0)

	ordered, err := store.GetByTreeDepthOrdered(ctx, "tree-1")
	require.NoError(t, err)
	assert.Len(t, ordered, 1)
}

func TestPostgresTreeStore_PartialIndex(t *testing.T) {
	store, pool := newTestPostgresTreeStore(t)
	ctx := context.Background()

	node := makeNode("tree-1", "user-a", 0, nil, nil, nil)
	require.NoError(t, store.InsertNode(ctx, node))
	require.NoError(t, store.DeleteNode(ctx, "tree-1", "user-a"))

	// The partial index (WHERE removed_at IS NULL) should not contain the deleted row.
	// Verify by querying active nodes — a seq scan would still find it, but
	// the active query filters correctly regardless.
	got, err := store.GetNode(ctx, "tree-1", "user-a")
	require.NoError(t, err)
	assert.Nil(t, got)

	// Verify the row still exists in the table (soft delete, not hard delete).
	var count int
	err = pool.QueryRow(ctx,
		"SELECT count(*) FROM tree_nodes WHERE tree_id = $1 AND user_id = $2",
		"tree-1", "user-a",
	).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count, "soft-deleted row should still exist in table")
}

func TestPostgresTreeStore_BulkInsertTransaction(t *testing.T) {
	store, pool := newTestPostgresTreeStore(t)
	ctx := context.Background()

	// Insert one valid node and one that will conflict on the PK (same ID).
	node1 := makeNode("tree-1", "a", 0, nil, nil, nil)
	require.NoError(t, store.InsertNode(ctx, node1))

	// Bulk insert includes a node with the same ID as node1 — should fail atomically.
	duplicate := makeNode("tree-1", "a", 0, nil, nil, nil)
	node2 := makeNode("tree-1", "b", 1, ptr("a"), ptr("a"), nil)

	err := store.BulkInsert(ctx, []TreeNodeRow{node2, duplicate})
	require.Error(t, err, "bulk insert with duplicate should fail")

	// Verify node2 was NOT inserted (transaction rolled back).
	var count int
	err = pool.QueryRow(ctx,
		"SELECT count(*) FROM tree_nodes WHERE user_id = $1 AND removed_at IS NULL",
		"b",
	).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 0, count, "node2 should not exist after failed bulk insert")
}

func TestPostgresTreeStore_TimestampsPopulated(t *testing.T) {
	store, _ := newTestPostgresTreeStore(t)
	ctx := context.Background()

	before := time.Now().Add(-time.Second)
	node := makeNode("tree-1", "user-a", 0, nil, nil, nil)
	require.NoError(t, store.InsertNode(ctx, node))

	got, err := store.GetNode(ctx, "tree-1", "user-a")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.True(t, got.CreatedAt.After(before), "created_at should be set by database")
	assert.True(t, got.UpdatedAt.After(before), "updated_at should be set by database")
}
