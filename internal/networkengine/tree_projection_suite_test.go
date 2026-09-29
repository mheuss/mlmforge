package networkengine

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// projectionState is what a projection test compares before and after a
// write: the tree's projected version, and the newest row for each user.
type projectionState struct {
	version int64
	found   bool
	rows    map[string]*TreeNodeRow
}

func readProjectionState(t *testing.T, s TreeStore, tree string, users ...string) projectionState {
	t.Helper()
	ctx := context.Background()
	version, found, err := s.ProjectedVersion(ctx, tree)
	require.NoError(t, err)
	rows := make(map[string]*TreeNodeRow, len(users))
	for _, u := range users {
		row, err := s.GetNodeIncludingRemoved(ctx, tree, u)
		require.NoError(t, err)
		rows[u] = row
	}
	return projectionState{version: version, found: found, rows: rows}
}

// runTreeProjectionSuite is the version contract both stores must pass.
// newStore returns a fresh, empty store on each call.
func runTreeProjectionSuite(t *testing.T, newStore func(t *testing.T) TreeStore) {
	t.Helper()

	tree := testTreeUUID(1)
	root, child, recruit := testUserUUID(1), testUserUUID(2), testUserUUID(3)
	rootRow := makeUUIDNode(testNodeUUID(1), tree, root, 0, nil, ptr(root), nil)
	childRow := makeUUIDNode(testNodeUUID(2), tree, child, 1, ptr(root), ptr(root), intPtr(0))
	recruitRow := makeUUIDNode(testNodeUUID(3), tree, recruit, 1, ptr(root), ptr(child), intPtr(1))

	t.Run("a tree with no projection has no projection row", func(t *testing.T) {
		s := newStore(t)

		got := readProjectionState(t, s, tree)

		assert.False(t, got.found)
		assert.Equal(t, int64(0), got.version)
	})

	t.Run("a projection above the version records its version", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		require.NoError(t, s.ProjectInsert(ctx, rootRow, 1))
		require.NoError(t, s.ProjectInsert(ctx, childRow, 2))

		got := readProjectionState(t, s, tree, child)
		assert.True(t, got.found)
		assert.Equal(t, int64(2), got.version)
		require.NotNil(t, got.rows[child])
		assert.Nil(t, got.rows[child].RemovedAt)
	})

	t.Run("an insert below the version is refused and writes nothing", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		require.NoError(t, s.ProjectInsert(ctx, rootRow, 1))
		require.NoError(t, s.ProjectInsert(ctx, childRow, 3))
		before := readProjectionState(t, s, tree, root, child, recruit)

		err := s.ProjectInsert(ctx, recruitRow, 2)

		var refused *ProjectionRefusedError
		require.ErrorAs(t, err, &refused)
		assert.Equal(t, ProjectionRefusedError{TreeID: tree, EventVersion: 2, ProjectedVersion: 3}, *refused)
		assert.Equal(t, before, readProjectionState(t, s, tree, root, child, recruit))
	})

	t.Run("a removal below the version is refused and writes nothing", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		require.NoError(t, s.ProjectInsert(ctx, rootRow, 1))
		require.NoError(t, s.ProjectInsert(ctx, childRow, 2))
		require.NoError(t, s.ProjectInsert(ctx, recruitRow, 4))
		before := readProjectionState(t, s, tree, root, child, recruit)

		err := s.ProjectRemoval(ctx, tree, child, testNodeUUID(9), 3,
			[]Responsored{{UserID: recruit, NewSponsorID: root}})

		var refused *ProjectionRefusedError
		require.ErrorAs(t, err, &refused)
		assert.Equal(t, ProjectionRefusedError{TreeID: tree, EventVersion: 3, ProjectedVersion: 4}, *refused)
		assert.Equal(t, before, readProjectionState(t, s, tree, root, child, recruit))
	})

	t.Run("a removal above the version tombstones the row and records its version", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		require.NoError(t, s.ProjectInsert(ctx, rootRow, 1))
		require.NoError(t, s.ProjectInsert(ctx, childRow, 2))

		require.NoError(t, s.ProjectRemoval(ctx, tree, child, testNodeUUID(9), 3, nil))

		got := readProjectionState(t, s, tree, child)
		assert.Equal(t, int64(3), got.version)
		require.NotNil(t, got.rows[child])
		assert.NotNil(t, got.rows[child].RemovedAt)
	})

	t.Run("a removal above the version repoints the moved recruits", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		require.NoError(t, s.ProjectInsert(ctx, rootRow, 1))
		require.NoError(t, s.ProjectInsert(ctx, childRow, 2))
		require.NoError(t, s.ProjectInsert(ctx, recruitRow, 3))

		require.NoError(t, s.ProjectRemoval(ctx, tree, child, testNodeUUID(9), 4,
			[]Responsored{{UserID: recruit, NewSponsorID: root}}))

		got := readProjectionState(t, s, tree, recruit)
		assert.Equal(t, int64(4), got.version)
		require.NotNil(t, got.rows[recruit])
		require.NotNil(t, got.rows[recruit].SponsorID)
		assert.Equal(t, root, *got.rows[recruit].SponsorID)
	})

	t.Run("a removal above the version that fails part way writes nothing", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		require.NoError(t, s.ProjectInsert(ctx, rootRow, 1))
		require.NoError(t, s.ProjectInsert(ctx, childRow, 2))
		require.NoError(t, s.ProjectInsert(ctx, recruitRow, 3))
		absent := testUserUUID(7)
		before := readProjectionState(t, s, tree, root, child, recruit)

		err := s.ProjectRemoval(ctx, tree, child, testNodeUUID(9), 4, []Responsored{
			{UserID: recruit, NewSponsorID: root},
			{UserID: absent, NewSponsorID: root},
		})

		require.Error(t, err)
		assert.Equal(t, before, readProjectionState(t, s, tree, root, child, recruit))
	})

	t.Run("a root_added event at the version leaves the store unchanged", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		require.NoError(t, s.ProjectInsert(ctx, rootRow, 1))
		before := readProjectionState(t, s, tree, root)

		err := s.ProjectInsert(ctx, rootRow, 1)

		require.ErrorIs(t, err, ErrNodeAlreadyProjected)
		assert.Equal(t, before, readProjectionState(t, s, tree, root))
	})

	t.Run("a node_placed event at the version leaves the store unchanged", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		require.NoError(t, s.ProjectInsert(ctx, rootRow, 1))
		require.NoError(t, s.ProjectInsert(ctx, childRow, 2))
		before := readProjectionState(t, s, tree, root, child)

		err := s.ProjectInsert(ctx, childRow, 2)

		require.ErrorIs(t, err, ErrNodeAlreadyProjected)
		assert.Equal(t, before, readProjectionState(t, s, tree, root, child))
	})

	t.Run("a node_removed event at the version leaves the store unchanged", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		removal := testNodeUUID(9)
		require.NoError(t, s.ProjectInsert(ctx, rootRow, 1))
		require.NoError(t, s.ProjectInsert(ctx, childRow, 2))
		require.NoError(t, s.ProjectInsert(ctx, recruitRow, 3))
		moved := []Responsored{{UserID: recruit, NewSponsorID: root}}
		require.NoError(t, s.ProjectRemoval(ctx, tree, child, removal, 4, moved))
		before := readProjectionState(t, s, tree, root, child, recruit)

		err := s.ProjectRemoval(ctx, tree, child, removal, 4, moved)

		require.ErrorContains(t, err, "soft delete for user "+child+" in tree "+tree+" matched 0 active rows")
		assert.Equal(t, before, readProjectionState(t, s, tree, root, child, recruit))
	})

	t.Run("a write refused for another reason leaves the version where it was", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		require.NoError(t, s.ProjectInsert(ctx, rootRow, 1))
		require.NoError(t, s.ProjectInsert(ctx, childRow, 2))
		second := makeUUIDNode(testNodeUUID(4), tree, child, 1, ptr(root), ptr(root), intPtr(5))

		err := s.ProjectInsert(ctx, second, 3)

		require.ErrorIs(t, err, ErrActiveUserConflict)
		got := readProjectionState(t, s, tree)
		assert.Equal(t, int64(2), got.version)
	})

	t.Run("a refused first projection leaves no projection row", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		other := testTreeUUID(2)
		require.NoError(t, s.ProjectInsert(ctx, rootRow, 1))
		sameID := makeUUIDNode(testNodeUUID(1), other, root, 0, nil, ptr(root), nil)

		err := s.ProjectInsert(ctx, sameID, 1)

		require.ErrorIs(t, err, ErrNodeAlreadyProjected)
		got := readProjectionState(t, s, other)
		assert.False(t, got.found)
	})

	t.Run("undoing a root at version 1 restores version 0", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		require.NoError(t, s.ProjectInsert(ctx, rootRow, 1))

		require.NoError(t, s.UndoRootProjection(ctx, tree, root, 1))

		got := readProjectionState(t, s, tree, root)
		assert.True(t, got.found)
		assert.Equal(t, int64(0), got.version)
		require.NotNil(t, got.rows[root])
		assert.NotNil(t, got.rows[root].RemovedAt)
	})

	t.Run("undoing a root at a later version restores the version before it", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		require.NoError(t, s.ProjectInsert(ctx, rootRow, 4))

		require.NoError(t, s.UndoRootProjection(ctx, tree, root, 4))

		got := readProjectionState(t, s, tree, root)
		assert.Equal(t, int64(3), got.version)
		require.NotNil(t, got.rows[root])
		assert.NotNil(t, got.rows[root].RemovedAt)
	})

	t.Run("undoing a root with no active row leaves the version where it was", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		require.NoError(t, s.ProjectInsert(ctx, rootRow, 1))
		before := readProjectionState(t, s, tree, root, child)

		err := s.UndoRootProjection(ctx, tree, child, 1)

		require.ErrorContains(t, err, "soft delete for root "+child+" in tree "+tree+" matched 0 active rows")
		assert.Equal(t, before, readProjectionState(t, s, tree, root, child))
	})

	t.Run("undoing a root in a tree with no projection row deletes nothing", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		require.NoError(t, s.InsertNode(ctx, rootRow))
		before := readProjectionState(t, s, tree, root)

		err := s.UndoRootProjection(ctx, tree, root, 1)

		require.EqualError(t, err, "tree "+tree+" has no projection row; the root row for "+root+" was not deleted")
		assert.Equal(t, before, readProjectionState(t, s, tree, root))
	})

	t.Run("an event version below 1 is refused and writes nothing", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		insertErr := s.ProjectInsert(ctx, rootRow, 0)
		removeErr := s.ProjectRemoval(ctx, tree, root, testNodeUUID(9), 0, nil)
		undoErr := s.UndoRootProjection(ctx, tree, root, 0)

		want := "tree " + tree + " was given event version 0, below 1; nothing was written"
		require.EqualError(t, insertErr, want)
		require.EqualError(t, removeErr, want)
		require.EqualError(t, undoErr, want)
		got := readProjectionState(t, s, tree, root)
		assert.False(t, got.found)
		assert.Nil(t, got.rows[root])
	})

	t.Run("undoing a root at a version the tree is not at deletes nothing", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		require.NoError(t, s.ProjectInsert(ctx, rootRow, 1))
		require.NoError(t, s.ProjectInsert(ctx, childRow, 2))
		before := readProjectionState(t, s, tree, root)

		err := s.UndoRootProjection(ctx, tree, root, 1)

		require.EqualError(t, err, "tree "+tree+" has projected version 2, not 1; the root row for "+root+" was not deleted")
		assert.Equal(t, before, readProjectionState(t, s, tree, root))
	})
}

func TestMemoryTreeStore_ProjectionSuite(t *testing.T) {
	runTreeProjectionSuite(t, func(t *testing.T) TreeStore {
		return NewMemoryTreeStore()
	})
}

func TestPostgresTreeStore_ProjectionSuite(t *testing.T) {
	runTreeProjectionSuite(t, func(t *testing.T) TreeStore {
		return newTestPostgresTreeStore(t)
	})
}

func TestPostgresTreeStore_ConcurrentFirstProjectionsQueue(t *testing.T) {
	store := newTestPostgresTreeStore(t)
	ctx := context.Background()

	for i := range 20 {
		tree := testTreeUUID(100 + i)
		root := testUserUUID(1)
		row := makeUUIDNode(testNodeUUID(100+i), tree, root, 0, nil, ptr(root), nil)
		start := make(chan struct{})
		errs := make(chan error, 2)
		for range 2 {
			go func() {
				<-start
				errs <- store.ProjectInsert(ctx, row, 1)
			}()
		}
		close(start)
		first, second := <-errs, <-errs

		var failed error
		switch {
		case first == nil && second != nil:
			failed = second
		case second == nil && first != nil:
			failed = first
		default:
			t.Fatalf("round %d: the two projections returned %v and %v; want one nil and one error", i, first, second)
		}
		require.ErrorIs(t, failed, ErrNodeAlreadyProjected, "round %d", i)
		version, found, err := store.ProjectedVersion(ctx, tree)
		require.NoError(t, err)
		assert.True(t, found, "round %d", i)
		assert.Equal(t, int64(1), version, "round %d", i)
	}
}

// waitForLockWaiters polls until at least n backends in this database wait on
// a lock.
func waitForLockWaiters(t *testing.T, pool *pgxpool.Pool, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var waiting int
		require.NoError(t, pool.QueryRow(context.Background(),
			`SELECT count(*) FROM pg_stat_activity WHERE wait_event_type = 'Lock' AND datname = current_database()`,
		).Scan(&waiting))
		if waiting >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("after 10s, %d backends were waiting on a lock; want %d", waiting, n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestPostgresTreeStore_ALowerVersionQueuedBehindAHigherOneIsRefused(t *testing.T) {
	store := newTestPostgresTreeStore(t)
	ctx := context.Background()
	tree, root := testTreeUUID(1), testUserUUID(1)
	require.NoError(t, store.ProjectInsert(ctx, makeUUIDNode(testNodeUUID(1), tree, root, 0, nil, ptr(root), nil), 1))
	require.NoError(t, store.ProjectInsert(ctx,
		makeUUIDNode(testNodeUUID(2), tree, testUserUUID(2), 1, ptr(root), ptr(root), intPtr(0)), 2))

	blocker, err := store.pool.Begin(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = blocker.Rollback(context.Background()) })
	_, err = blocker.Exec(ctx, `SELECT 1 FROM tree_projections WHERE tree_id = $1 FOR UPDATE`, tree)
	require.NoError(t, err)

	higher, lower := make(chan error, 1), make(chan error, 1)
	go func() {
		higher <- store.ProjectInsert(ctx,
			makeUUIDNode(testNodeUUID(4), tree, testUserUUID(4), 1, ptr(root), ptr(root), intPtr(2)), 4)
	}()
	waitForLockWaiters(t, store.pool, 1)
	go func() {
		lower <- store.ProjectInsert(ctx,
			makeUUIDNode(testNodeUUID(3), tree, testUserUUID(3), 1, ptr(root), ptr(root), intPtr(1)), 3)
	}()
	waitForLockWaiters(t, store.pool, 2)
	require.NoError(t, blocker.Commit(ctx))

	require.NoError(t, receive(t, higher, 10*time.Second))
	var refused *ProjectionRefusedError
	require.ErrorAs(t, receive(t, lower, 10*time.Second), &refused)
	assert.Equal(t, ProjectionRefusedError{TreeID: tree, EventVersion: 3, ProjectedVersion: 4}, *refused)
	version, _, err := store.ProjectedVersion(ctx, tree)
	require.NoError(t, err)
	assert.Equal(t, int64(4), version)
	row, err := store.GetNode(ctx, tree, testUserUUID(3))
	require.NoError(t, err)
	assert.Nil(t, row, "the lower version's row landed")
}
