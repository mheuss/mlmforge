package networkengine

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fixtureSoftDeleteSQL soft-deletes a user's active row without a removal stamp.
const fixtureSoftDeleteSQL = `UPDATE tree_nodes SET removed_at = now(), updated_at = now()
		 WHERE tree_id = $1 AND user_id = $2 AND removed_at IS NULL`

// insertRow stores an active row and records no projected version.
func insertRow(ctx context.Context, s TreeStore, row TreeNodeRow) error {
	switch store := s.(type) {
	case *MemoryTreeStore:
		return store.InsertNode(ctx, row)
	case *PostgresTreeStore:
		return insertNode(ctx, store.pool, row)
	default:
		return fmt.Errorf("insertRow has no fixture write for store type %T", s)
	}
}

// tombstoneUser soft-deletes a user's active row, with no removal stamp and no
// projected version.
func tombstoneUser(ctx context.Context, s TreeStore, treeID, userID string) error {
	switch store := s.(type) {
	case *MemoryTreeStore:
		return store.DeleteNode(ctx, treeID, userID)
	case *PostgresTreeStore:
		_, err := store.pool.Exec(ctx, fixtureSoftDeleteSQL, treeID, userID)
		return err
	default:
		return fmt.Errorf("tombstoneUser has no fixture write for store type %T", s)
	}
}

// stampRemoval tombstones one user's active row and stamps it with
// removalEventID, at version.
func stampRemoval(ctx context.Context, s TreeStore, treeID, userID, removalEventID string, version int64) error {
	return s.ProjectRemoval(ctx, treeID, RemovalProjection{Users: []string{userID}}, removalEventID, version)
}

func runFixtureHelperChecks(t *testing.T, newStore func(t *testing.T) TreeStore) {
	t.Helper()
	tree := testTreeUUID(1)
	root, child := testUserUUID(1), testUserUUID(2)
	rootRow := makeUUIDNode(testNodeUUID(1), tree, root, 0, nil, nil, nil)
	childRow := makeUUIDNode(testNodeUUID(2), tree, child, 1, ptr(root), ptr(root), intPtr(0))

	t.Run("insertRow stores an active row and no projection row", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		require.NoError(t, insertRow(ctx, s, rootRow))

		got, err := s.GetNode(ctx, tree, root)
		require.NoError(t, err)
		require.NotNil(t, got)
		_, found, err := s.ProjectedVersion(ctx, tree)
		require.NoError(t, err)
		assert.False(t, found, "projection row found after insertRow")
	})

	t.Run("tombstoneUser leaves an unstamped tombstone and no projection row", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		require.NoError(t, insertRow(ctx, s, rootRow))
		require.NoError(t, insertRow(ctx, s, childRow))

		require.NoError(t, tombstoneUser(ctx, s, tree, child))

		got, err := s.GetNodeIncludingRemoved(ctx, tree, child)
		require.NoError(t, err)
		require.NotNil(t, got)
		require.NotNil(t, got.RemovedAt, "no removed_at read back on the tombstoned row")
		assert.Nil(t, got.RemovedByEventID, "removal stamp read back on the tombstoned row")
		assert.True(t, got.UpdatedAt.Equal(*got.RemovedAt),
			"updated_at read back as %v, removed_at as %v", got.UpdatedAt, *got.RemovedAt)
		_, found, err := s.ProjectedVersion(ctx, tree)
		require.NoError(t, err)
		assert.False(t, found, "projection row found after tombstoneUser")
	})

	t.Run("stampRemoval tombstones and stamps the user's active row", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		require.NoError(t, insertRow(ctx, s, rootRow))
		require.NoError(t, insertRow(ctx, s, childRow))

		require.NoError(t, stampRemoval(ctx, s, tree, child, testNodeUUID(9), 1))

		got, err := s.GetNodeIncludingRemoved(ctx, tree, child)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.NotNil(t, got.RemovedAt, "no removed_at read back on the stamped row")
		require.NotNil(t, got.RemovedByEventID, "no removal stamp read back on the stamped row")
		assert.Equal(t, testNodeUUID(9), *got.RemovedByEventID)
		version, found, err := s.ProjectedVersion(ctx, tree)
		require.NoError(t, err)
		assert.True(t, found, "no projection row after stampRemoval")
		assert.Equal(t, int64(1), version)
	})
}

func TestMemoryTreeStore_FixtureHelpers(t *testing.T) {
	runFixtureHelperChecks(t, func(t *testing.T) TreeStore { return NewMemoryTreeStore() })
}

func TestPostgresTreeStore_FixtureHelpers(t *testing.T) {
	runFixtureHelperChecks(t, func(t *testing.T) TreeStore { return newTestPostgresTreeStore(t) })
}
