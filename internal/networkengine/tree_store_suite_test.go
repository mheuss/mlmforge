package networkengine

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// nodeUserIDs extracts the UserID of every row, for order-independent comparison.
// Neither implementation orders GetChildren or GetByTree, so a suite that
// compared slices positionally would pin an order neither one promises.
func nodeUserIDs(nodes []TreeNodeRow) []string {
	ids := make([]string, 0, len(nodes))
	for _, n := range nodes {
		ids = append(ids, n.UserID)
	}
	return ids
}

// runTreeStoreSuite is the shared behavioral contract. Both the memory and
// Postgres implementations must pass it identically. newStore returns a
// fresh, empty store on each call.
//
// Nothing here asserts on CreatedAt or UpdatedAt. The two implementations
// already disagree about both, and HEU-403 owns that.
func runTreeStoreSuite(t *testing.T, newStore func(t *testing.T) TreeStore) {
	t.Helper()

	t.Run("InsertNode then GetNode returns the row", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		tree := testTreeUUID(1)
		rootUser := testUserUUID(1)
		childUser := testUserUUID(2)

		require.NoError(t, s.InsertNode(ctx, makeUUIDNode(testNodeUUID(1), tree, rootUser, 0, nil, nil, nil)))
		require.NoError(t, s.InsertNode(ctx,
			makeUUIDNode(testNodeUUID(2), tree, childUser, 1, ptr(rootUser), ptr(rootUser), intPtr(0))))

		got, err := s.GetNode(ctx, tree, childUser)
		require.NoError(t, err)
		require.NotNil(t, got, "the row just inserted should be readable")

		assert.Equal(t, testNodeUUID(2), got.ID, "the row id is the event id and must round-trip")
		assert.Equal(t, tree, got.TreeID)
		assert.Equal(t, childUser, got.UserID)
		assert.Equal(t, 1, got.Depth)
		require.NotNil(t, got.ParentID)
		assert.Equal(t, rootUser, *got.ParentID)
		require.NotNil(t, got.SponsorID)
		assert.Equal(t, rootUser, *got.SponsorID)
		require.NotNil(t, got.Position)
		assert.Equal(t, 0, *got.Position)
		assert.Nil(t, got.RemovedAt, "an inserted row is active")
	})

	t.Run("GetNode returns nil for an absent user", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		require.NoError(t, s.InsertNode(ctx,
			makeUUIDNode(testNodeUUID(1), testTreeUUID(1), testUserUUID(1), 0, nil, nil, nil)))

		got, err := s.GetNode(ctx, testTreeUUID(1), testUserUUID(99))
		require.NoError(t, err, "an absent user is not an error")
		assert.Nil(t, got)
	})

	// A tree is scoped by tree_id on every read. Asking the wrong tree for a
	// user that exists in another one must miss, or a multi-tenant read leaks.
	t.Run("GetNode does not cross tree boundaries", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		user := testUserUUID(1)
		require.NoError(t, s.InsertNode(ctx,
			makeUUIDNode(testNodeUUID(1), testTreeUUID(1), user, 0, nil, nil, nil)))

		got, err := s.GetNode(ctx, testTreeUUID(2), user)
		require.NoError(t, err)
		assert.Nil(t, got, "the user exists, but not in this tree")
	})

	t.Run("GetNode returns nil for a soft-deleted user", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		tree := testTreeUUID(1)
		user := testUserUUID(1)

		require.NoError(t, s.InsertNode(ctx, makeUUIDNode(testNodeUUID(1), tree, user, 0, nil, nil, nil)))
		require.NoError(t, s.DeleteNode(ctx, tree, user))

		got, err := s.GetNode(ctx, tree, user)
		require.NoError(t, err, "a soft-deleted row reads as absent, not as an error")
		assert.Nil(t, got)
	})

	t.Run("GetChildren returns only active children", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		tree := testTreeUUID(1)
		rootUser := testUserUUID(1)
		keptUser := testUserUUID(2)
		removedUser := testUserUUID(3)
		otherParentUser := testUserUUID(4)

		require.NoError(t, s.InsertNode(ctx, makeUUIDNode(testNodeUUID(1), tree, rootUser, 0, nil, nil, nil)))
		require.NoError(t, s.InsertNode(ctx,
			makeUUIDNode(testNodeUUID(2), tree, keptUser, 1, ptr(rootUser), ptr(rootUser), intPtr(0))))
		require.NoError(t, s.InsertNode(ctx,
			makeUUIDNode(testNodeUUID(3), tree, removedUser, 1, ptr(rootUser), ptr(rootUser), intPtr(1))))
		// A grandchild, to prove GetChildren is one level and not a subtree walk.
		require.NoError(t, s.InsertNode(ctx,
			makeUUIDNode(testNodeUUID(4), tree, otherParentUser, 2, ptr(keptUser), ptr(keptUser), intPtr(0))))

		require.NoError(t, s.DeleteNode(ctx, tree, removedUser))

		children, err := s.GetChildren(ctx, tree, rootUser)
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{keptUser}, nodeUserIDs(children),
			"only the active direct child of the root")
	})

	t.Run("GetByTree returns only active nodes", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		tree := testTreeUUID(1)
		otherTree := testTreeUUID(2)
		rootUser := testUserUUID(1)
		keptUser := testUserUUID(2)
		removedUser := testUserUUID(3)

		require.NoError(t, s.InsertNode(ctx, makeUUIDNode(testNodeUUID(1), tree, rootUser, 0, nil, nil, nil)))
		require.NoError(t, s.InsertNode(ctx,
			makeUUIDNode(testNodeUUID(2), tree, keptUser, 1, ptr(rootUser), ptr(rootUser), intPtr(0))))
		require.NoError(t, s.InsertNode(ctx,
			makeUUIDNode(testNodeUUID(3), tree, removedUser, 1, ptr(rootUser), ptr(rootUser), intPtr(1))))
		require.NoError(t, s.InsertNode(ctx,
			makeUUIDNode(testNodeUUID(4), otherTree, testUserUUID(4), 0, nil, nil, nil)))

		require.NoError(t, s.DeleteNode(ctx, tree, removedUser))

		got, err := s.GetByTree(ctx, tree)
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{rootUser, keptUser}, nodeUserIDs(got),
			"active rows of this tree only")
	})

	// DeleteNode is asserted through every read path rather than GetNode
	// alone. A soft delete that one query honors and another ignores is the
	// shape that breaks startup bulk-load.
	t.Run("DeleteNode makes the row invisible to every read", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		tree := testTreeUUID(1)
		rootUser := testUserUUID(1)
		childUser := testUserUUID(2)

		require.NoError(t, s.InsertNode(ctx, makeUUIDNode(testNodeUUID(1), tree, rootUser, 0, nil, nil, nil)))
		require.NoError(t, s.InsertNode(ctx,
			makeUUIDNode(testNodeUUID(2), tree, childUser, 1, ptr(rootUser), ptr(rootUser), intPtr(0))))

		require.NoError(t, s.DeleteNode(ctx, tree, childUser))

		got, err := s.GetNode(ctx, tree, childUser)
		require.NoError(t, err)
		assert.Nil(t, got, "GetNode")

		children, err := s.GetChildren(ctx, tree, rootUser)
		require.NoError(t, err)
		assert.Empty(t, nodeUserIDs(children), "GetChildren")

		byTree, err := s.GetByTree(ctx, tree)
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{rootUser}, nodeUserIDs(byTree), "GetByTree")
	})

	t.Run("DeleteNodeAndResponsor repoints the moved recruits", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		tree := testTreeUUID(1)
		rootUser := testUserUUID(1)
		removedUser := testUserUUID(2)
		movedUser := testUserUUID(3)

		require.NoError(t, s.InsertNode(ctx, makeUUIDNode(testNodeUUID(1), tree, rootUser, 0, nil, nil, nil)))
		require.NoError(t, s.InsertNode(ctx,
			makeUUIDNode(testNodeUUID(2), tree, removedUser, 1, ptr(rootUser), ptr(rootUser), intPtr(0))))
		require.NoError(t, s.InsertNode(ctx,
			makeUUIDNode(testNodeUUID(3), tree, movedUser, 2, ptr(removedUser), ptr(removedUser), intPtr(0))))

		require.NoError(t, s.DeleteNodeAndResponsor(ctx, tree, removedUser,
			[]Responsored{{UserID: movedUser, NewSponsorID: rootUser}}))

		gone, err := s.GetNode(ctx, tree, removedUser)
		require.NoError(t, err)
		assert.Nil(t, gone, "the removed user is soft-deleted")

		moved, err := s.GetNode(ctx, tree, movedUser)
		require.NoError(t, err)
		require.NotNil(t, moved, "the recruit stays active")
		require.NotNil(t, moved.SponsorID)
		assert.Equal(t, rootUser, *moved.SponsorID, "the recruit is re-sponsored to the new sponsor")
	})

	// Both writes or neither. The store's own doc comment says a soft delete
	// landing without the sponsor updates leaves the tree unable to reload,
	// so the failure case is part of the contract, not an implementation
	// detail. The two implementations word the error differently, so this
	// asserts that one is returned and that nothing was written, not its text.
	t.Run("DeleteNodeAndResponsor writes nothing when a recruit is absent", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		tree := testTreeUUID(1)
		rootUser := testUserUUID(1)
		removedUser := testUserUUID(2)
		absentUser := testUserUUID(99)

		require.NoError(t, s.InsertNode(ctx, makeUUIDNode(testNodeUUID(1), tree, rootUser, 0, nil, nil, nil)))
		require.NoError(t, s.InsertNode(ctx,
			makeUUIDNode(testNodeUUID(2), tree, removedUser, 1, ptr(rootUser), ptr(rootUser), intPtr(0))))

		err := s.DeleteNodeAndResponsor(ctx, tree, removedUser,
			[]Responsored{{UserID: absentUser, NewSponsorID: rootUser}})
		require.Error(t, err, "re-sponsoring a user with no active row must fail")

		stillThere, err := s.GetNode(ctx, tree, removedUser)
		require.NoError(t, err)
		assert.NotNil(t, stillThere, "the soft delete must roll back with the sponsor updates")
	})

	t.Run("BulkInsert inserts every row", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		tree := testTreeUUID(1)
		rootUser := testUserUUID(1)
		childA := testUserUUID(2)
		childB := testUserUUID(3)

		require.NoError(t, s.BulkInsert(ctx, []TreeNodeRow{
			makeUUIDNode(testNodeUUID(1), tree, rootUser, 0, nil, nil, nil),
			makeUUIDNode(testNodeUUID(2), tree, childA, 1, ptr(rootUser), ptr(rootUser), intPtr(0)),
			makeUUIDNode(testNodeUUID(3), tree, childB, 1, ptr(rootUser), ptr(rootUser), intPtr(1)),
		}))

		got, err := s.GetByTree(ctx, tree)
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{rootUser, childA, childB}, nodeUserIDs(got))
	})

	// The known divergence. Postgres runs the batch in one transaction;
	// MemoryTreeStore loops InsertNode and keeps what it already appended.
	// Task 4 makes the memory store stage, and this passes on both.
	t.Run("BulkInsert writes nothing when one row in the batch is bad", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		tree := testTreeUUID(1)
		rootUser := testUserUUID(1)
		childUser := testUserUUID(2)

		err := s.BulkInsert(ctx, []TreeNodeRow{
			makeUUIDNode(testNodeUUID(1), tree, rootUser, 0, nil, nil, nil),
			makeUUIDNode(testNodeUUID(2), tree, childUser, 1, ptr(rootUser), ptr(rootUser), intPtr(0)),
			// Same row id as the first. The primary key is not partial, so
			// both implementations reject it whatever its tree or state.
			makeUUIDNode(testNodeUUID(1), tree, testUserUUID(3), 1, ptr(rootUser), ptr(rootUser), intPtr(1)),
		})
		require.Error(t, err, "a duplicate row id must fail the batch")

		got, err := s.GetByTree(ctx, tree)
		require.NoError(t, err)
		assert.Empty(t, nodeUserIDs(got), "a failed batch leaves no rows behind")
	})
}
