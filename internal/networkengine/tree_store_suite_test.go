package networkengine

import (
	"context"
	"testing"
	"time"

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
// already disagree about both, which is HEU-817. HEU-403 is the adjacent
// question of whether the struct should carry fields the insert drops.
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
		grandchildUser := testUserUUID(4)

		require.NoError(t, s.InsertNode(ctx, makeUUIDNode(testNodeUUID(1), tree, rootUser, 0, nil, nil, nil)))
		require.NoError(t, s.InsertNode(ctx,
			makeUUIDNode(testNodeUUID(2), tree, keptUser, 1, ptr(rootUser), ptr(rootUser), intPtr(0))))
		require.NoError(t, s.InsertNode(ctx,
			makeUUIDNode(testNodeUUID(3), tree, removedUser, 1, ptr(rootUser), ptr(rootUser), intPtr(1))))
		// A grandchild, to prove GetChildren is one level and not a subtree walk.
		require.NoError(t, s.InsertNode(ctx,
			makeUUIDNode(testNodeUUID(4), tree, grandchildUser, 2, ptr(keptUser), ptr(keptUser), intPtr(0))))

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

		ordered, err := s.GetByTreeDepthOrdered(ctx, tree)
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{rootUser}, nodeUserIDs(ordered), "GetByTreeDepthOrdered")
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
		// The same user id in another tree, inserted before this tree's row. A
		// lookup that ignores the tree scans linearly and reaches this one
		// first, so inserting it after would let a tree-blind scan pass.
		otherTree := testTreeUUID(2)
		require.NoError(t, s.InsertNode(ctx, makeUUIDNode(testNodeUUID(4), otherTree, rootUser, 0, nil, nil, nil)))
		require.NoError(t, s.InsertNode(ctx,
			makeUUIDNode(testNodeUUID(5), otherTree, movedUser, 1, ptr(rootUser), ptr(rootUser), intPtr(0))))
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

		elsewhere, err := s.GetNode(ctx, otherTree, movedUser)
		require.NoError(t, err)
		require.NotNil(t, elsewhere)
		require.NotNil(t, elsewhere.SponsorID)
		assert.Equal(t, rootUser, *elsewhere.SponsorID, "the other tree's row keeps its own sponsor")
		assert.Equal(t, 1, elsewhere.Depth, "and is the other tree's row, not this tree's")
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
		validUser := testUserUUID(3)
		absentUser := testUserUUID(99)

		require.NoError(t, s.InsertNode(ctx, makeUUIDNode(testNodeUUID(1), tree, rootUser, 0, nil, nil, nil)))
		require.NoError(t, s.InsertNode(ctx,
			makeUUIDNode(testNodeUUID(2), tree, removedUser, 1, ptr(rootUser), ptr(rootUser), intPtr(0))))
		require.NoError(t, s.InsertNode(ctx,
			makeUUIDNode(testNodeUUID(3), tree, validUser, 2, ptr(removedUser), ptr(removedUser), intPtr(0))))

		// The valid recruit goes first so the batch has a sponsor write to
		// undo. With only the absent one, an implementation that wrote each
		// sponsor inline before the delete would pass having written nothing.
		err := s.DeleteNodeAndResponsor(ctx, tree, removedUser, []Responsored{
			{UserID: validUser, NewSponsorID: rootUser},
			{UserID: absentUser, NewSponsorID: rootUser},
		})
		require.Error(t, err, "re-sponsoring a user with no active row must fail")

		stillThere, err := s.GetNode(ctx, tree, removedUser)
		require.NoError(t, err)
		assert.NotNil(t, stillThere, "the soft delete must roll back with the sponsor updates")

		untouched, err := s.GetNode(ctx, tree, validUser)
		require.NoError(t, err)
		require.NotNil(t, untouched)
		require.NotNil(t, untouched.SponsorID)
		assert.Equal(t, removedUser, *untouched.SponsorID,
			"the valid recruit's sponsor write must roll back too")
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

	// Every other BulkInsert case starts from an empty store, where preserving
	// what was already there is vacuous. This one starts from a populated one,
	// which is how the loader actually uses it.
	t.Run("BulkInsert preserves rows already in the store", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		tree := testTreeUUID(1)
		rootUser := testUserUUID(1)
		childA := testUserUUID(2)
		childB := testUserUUID(3)

		require.NoError(t, s.InsertNode(ctx, makeUUIDNode(testNodeUUID(1), tree, rootUser, 0, nil, nil, nil)))

		require.NoError(t, s.BulkInsert(ctx, []TreeNodeRow{
			makeUUIDNode(testNodeUUID(2), tree, childA, 1, ptr(rootUser), ptr(rootUser), intPtr(0)),
			makeUUIDNode(testNodeUUID(3), tree, childB, 1, ptr(rootUser), ptr(rootUser), intPtr(1)),
		}))

		got, err := s.GetByTree(ctx, tree)
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{rootUser, childA, childB}, nodeUserIDs(got),
			"the batch adds to the store rather than replacing it")

		root, err := s.GetNode(ctx, tree, rootUser)
		require.NoError(t, err)
		require.NotNil(t, root, "the pre-existing row survives the batch")
		assert.Equal(t, testNodeUUID(1), root.ID, "and survives intact, not as a blank row")
	})

	t.Run("a failed BulkInsert leaves rows already in the store alone", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		tree := testTreeUUID(1)
		rootUser := testUserUUID(1)

		require.NoError(t, s.InsertNode(ctx, makeUUIDNode(testNodeUUID(1), tree, rootUser, 0, nil, nil, nil)))

		err := s.BulkInsert(ctx, []TreeNodeRow{
			makeUUIDNode(testNodeUUID(2), tree, testUserUUID(2), 1, ptr(rootUser), ptr(rootUser), intPtr(0)),
			makeUUIDNode(testNodeUUID(1), tree, testUserUUID(3), 1, ptr(rootUser), ptr(rootUser), intPtr(1)),
		})
		require.ErrorIs(t, err, ErrNodeAlreadyProjected)

		got, err := s.GetByTree(ctx, tree)
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{rootUser}, nodeUserIDs(got),
			"the rollback restores what was there, and no more")
	})

	t.Run("GetNodeIncludingRemoved returns nil for an absent user", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		require.NoError(t, s.InsertNode(ctx,
			makeUUIDNode(testNodeUUID(1), testTreeUUID(1), testUserUUID(1), 0, nil, nil, nil)))

		got, err := s.GetNodeIncludingRemoved(ctx, testTreeUUID(1), testUserUUID(99))
		require.NoError(t, err)
		assert.Nil(t, got)
	})

	t.Run("GetNodeIncludingRemoved returns the active row", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		tree, user := testTreeUUID(1), testUserUUID(1)
		require.NoError(t, s.InsertNode(ctx, makeUUIDNode(testNodeUUID(1), tree, user, 0, nil, nil, nil)))

		got, err := s.GetNodeIncludingRemoved(ctx, tree, user)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, testNodeUUID(1), got.ID)
		assert.Nil(t, got.RemovedAt)
	})

	t.Run("GetNodeIncludingRemoved returns the tombstone after a delete", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		tree, user := testTreeUUID(1), testUserUUID(1)
		require.NoError(t, s.InsertNode(ctx, makeUUIDNode(testNodeUUID(1), tree, user, 0, nil, nil, nil)))
		require.NoError(t, s.DeleteNode(ctx, tree, user))

		got, err := s.GetNodeIncludingRemoved(ctx, tree, user)
		require.NoError(t, err)
		require.NotNil(t, got, "GetNode cannot see this; that is the point of the method")
		assert.Equal(t, testNodeUUID(1), got.ID)
		assert.NotNil(t, got.RemovedAt)
	})

	t.Run("GetNodeIncludingRemoved prefers the active row over a tombstone", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		tree, user := testTreeUUID(1), testUserUUID(1)
		require.NoError(t, s.InsertNode(ctx, makeUUIDNode(testNodeUUID(1), tree, user, 0, nil, nil, nil)))
		require.NoError(t, s.DeleteNode(ctx, tree, user))
		require.NoError(t, s.InsertNode(ctx, makeUUIDNode(testNodeUUID(2), tree, user, 0, nil, nil, nil)))

		got, err := s.GetNodeIncludingRemoved(ctx, tree, user)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Nil(t, got.RemovedAt, "the live placement wins over its own history")
		assert.Equal(t, testNodeUUID(2), got.ID)
	})

	// Two tombstones and no active row. With one tombstone every ordering
	// agrees, so nothing until here can tell an ascending sort from a
	// descending one. The consumer asks this to decide whether the event it is
	// holding is the one that was removed, so the answer has to be the latest
	// removal rather than the first.
	t.Run("GetNodeIncludingRemoved returns the newest tombstone", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		tree, user := testTreeUUID(1), testUserUUID(1)
		require.NoError(t, s.InsertNode(ctx, makeUUIDNode(testNodeUUID(1), tree, user, 0, nil, nil, nil)))
		require.NoError(t, s.DeleteNode(ctx, tree, user))
		require.NoError(t, s.InsertNode(ctx, makeUUIDNode(testNodeUUID(2), tree, user, 0, nil, nil, nil)))
		require.NoError(t, s.DeleteNode(ctx, tree, user))

		got, err := s.GetNodeIncludingRemoved(ctx, tree, user)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, testNodeUUID(2), got.ID, "the most recent removal, not the first")
	})

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
		require.ErrorIs(t, err, ErrNodeAlreadyProjected, "a duplicate row id must fail the batch")

		got, err := s.GetByTree(ctx, tree)
		require.NoError(t, err)
		assert.Empty(t, nodeUserIDs(got), "a failed batch leaves no rows behind")
	})

	// GetByTreeDepthOrdered is the only TreeStore method with a stated
	// ordering promise, so the suite asserts the sequence positionally. Here
	// the order is the contract rather than an artifact of the query.
	t.Run("GetByTreeDepthOrdered sorts by depth then enrolled_at", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		tree := testTreeUUID(1)
		rootUser := testUserUUID(1)
		earlyUser := testUserUUID(2)
		lateUser := testUserUUID(3)
		deepUser := testUserUUID(4)

		root := makeUUIDNode(testNodeUUID(1), tree, rootUser, 0, nil, nil, nil)
		early := makeUUIDNode(testNodeUUID(2), tree, earlyUser, 1, ptr(rootUser), ptr(rootUser), intPtr(0))
		late := makeUUIDNode(testNodeUUID(3), tree, lateUser, 1, ptr(rootUser), ptr(rootUser), intPtr(1))
		deep := makeUUIDNode(testNodeUUID(4), tree, deepUser, 2, ptr(earlyUser), ptr(earlyUser), intPtr(0))

		root.EnrolledAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		early.EnrolledAt = time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
		late.EnrolledAt = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
		// Earlier than early's, so a store sorting by enrolled_at alone puts
		// deep second and diverges from the expected sequence. Without this the
		// fixture pins the tiebreak and says nothing about depth.
		deep.EnrolledAt = time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)

		// Inserted deepest first and with the same-depth pair reversed, so
		// insertion order cannot produce the expected sequence by accident.
		require.NoError(t, s.InsertNode(ctx, deep))
		require.NoError(t, s.InsertNode(ctx, late))
		require.NoError(t, s.InsertNode(ctx, early))
		require.NoError(t, s.InsertNode(ctx, root))

		// Another tree, at a depth that would sort into the middle. A
		// tree-scope leak here returns another tenant's nodes inside an
		// ordered read, where the caller has no reason to re-filter.
		other := makeUUIDNode(testNodeUUID(5), testTreeUUID(2), testUserUUID(5), 1, nil, nil, nil)
		other.EnrolledAt = time.Date(2026, 2, 15, 0, 0, 0, 0, time.UTC)
		require.NoError(t, s.InsertNode(ctx, other))

		got, err := s.GetByTreeDepthOrdered(ctx, tree)
		require.NoError(t, err)
		assert.Equal(t, []string{rootUser, earlyUser, lateUser, deepUser}, nodeUserIDs(got),
			"depth ascending, then enrolled_at ascending within a depth")
	})

	// EnrolledAt is caller-supplied, unlike CreatedAt and UpdatedAt, and it is
	// the tiebreak key above. Compared with Equal rather than assert.Equal
	// because Postgres returns TIMESTAMPTZ in the session zone while the
	// memory store returns the value as given. Same instant, different
	// representation, so assert.Equal fails on Postgres alone. HEU-795.
	t.Run("InsertNode round-trips EnrolledAt", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		tree := testTreeUUID(1)
		user := testUserUUID(1)
		node := makeUUIDNode(testNodeUUID(1), tree, user, 0, nil, nil, nil)
		node.EnrolledAt = time.Date(2026, 5, 17, 13, 45, 6, 0, time.UTC)

		require.NoError(t, s.InsertNode(ctx, node))

		got, err := s.GetNode(ctx, tree, user)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.True(t, node.EnrolledAt.Equal(got.EnrolledAt),
			"EnrolledAt round-trips as the same instant, got %v want %v", got.EnrolledAt, node.EnrolledAt)
	})

	t.Run("GetChildren does not cross tree boundaries", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		treeA := testTreeUUID(1)
		treeB := testTreeUUID(2)
		sharedUser := testUserUUID(1)
		kidA := testUserUUID(2)
		kidB := testUserUUID(3)

		require.NoError(t, s.InsertNode(ctx, makeUUIDNode(testNodeUUID(1), treeA, sharedUser, 0, nil, nil, nil)))
		require.NoError(t, s.InsertNode(ctx, makeUUIDNode(testNodeUUID(2), treeB, sharedUser, 0, nil, nil, nil)))
		require.NoError(t, s.InsertNode(ctx,
			makeUUIDNode(testNodeUUID(3), treeA, kidA, 1, ptr(sharedUser), ptr(sharedUser), intPtr(0))))
		require.NoError(t, s.InsertNode(ctx,
			makeUUIDNode(testNodeUUID(4), treeB, kidB, 1, ptr(sharedUser), ptr(sharedUser), intPtr(0))))

		children, err := s.GetChildren(ctx, treeA, sharedUser)
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{kidA}, nodeUserIDs(children),
			"the same user parents a child in both trees; only this tree's child comes back")
	})

	t.Run("DeleteNode does not cross tree boundaries", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		treeA := testTreeUUID(1)
		treeB := testTreeUUID(2)
		sharedUser := testUserUUID(1)

		require.NoError(t, s.InsertNode(ctx, makeUUIDNode(testNodeUUID(1), treeA, sharedUser, 0, nil, nil, nil)))
		require.NoError(t, s.InsertNode(ctx, makeUUIDNode(testNodeUUID(2), treeB, sharedUser, 0, nil, nil, nil)))

		// Deleting from treeB, the row inserted second. A linear scan that
		// ignores the tree lands on treeA's row instead, so deleting from treeA
		// would pass with the tree filter dropped.
		require.NoError(t, s.DeleteNode(ctx, treeB, sharedUser))

		survivor, err := s.GetNode(ctx, treeA, sharedUser)
		require.NoError(t, err)
		assert.NotNil(t, survivor, "the other tree's row is untouched")

		removed, err := s.GetNode(ctx, treeB, sharedUser)
		require.NoError(t, err)
		assert.Nil(t, removed, "the named tree's row is gone")
	})

	// The three constraints from migrations 000002 and 000004. UC-NET-014
	// rests on the double rejecting the same writes Postgres rejects.
	t.Run("InsertNode rejects a duplicate row id", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		require.NoError(t, s.InsertNode(ctx,
			makeUUIDNode(testNodeUUID(1), testTreeUUID(1), testUserUUID(1), 0, nil, nil, nil)))

		err := s.InsertNode(ctx,
			makeUUIDNode(testNodeUUID(1), testTreeUUID(2), testUserUUID(2), 0, nil, nil, nil))
		assert.ErrorIs(t, err, ErrNodeAlreadyProjected,
			"the primary key is not partial, so a different tree and user does not excuse it")
	})

	// The primary key is not partial, so soft-deleting the row does not free
	// its id. A redelivered event whose row was since removed must still be
	// refused, or redelivery inserts a second row in memory and is rejected in
	// Postgres.
	t.Run("InsertNode rejects a duplicate row id after a soft delete", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		tree := testTreeUUID(1)
		user := testUserUUID(1)
		node := makeUUIDNode(testNodeUUID(1), tree, user, 0, nil, nil, nil)

		require.NoError(t, s.InsertNode(ctx, node))
		require.NoError(t, s.DeleteNode(ctx, tree, user))

		err := s.InsertNode(ctx, node)
		assert.ErrorIs(t, err, ErrNodeAlreadyProjected, "a removed row still holds its id")
	})

	t.Run("InsertNode rejects a second active row for one user in a tree", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		tree := testTreeUUID(1)
		rootUser := testUserUUID(1)
		user := testUserUUID(2)

		require.NoError(t, s.InsertNode(ctx, makeUUIDNode(testNodeUUID(1), tree, rootUser, 0, nil, nil, nil)))
		// Depth 1 on the conflicting pair is deliberate; see HEU-810.
		require.NoError(t, s.InsertNode(ctx,
			makeUUIDNode(testNodeUUID(2), tree, user, 1, ptr(rootUser), ptr(rootUser), intPtr(0))))

		err := s.InsertNode(ctx,
			makeUUIDNode(testNodeUUID(3), tree, user, 1, ptr(rootUser), ptr(rootUser), intPtr(1)))
		require.ErrorIs(t, err, ErrActiveUserConflict)

		// The sentinel says which branch. It cannot say which row, and this is
		// the error an operator reads when a projection stops.
		assert.Contains(t, err.Error(), tree, "the message names the tree")
		assert.Contains(t, err.Error(), user, "the message names the user")
	})

	t.Run("InsertNode rejects a second active claim on one slot", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		tree := testTreeUUID(1)
		rootUser := testUserUUID(1)

		require.NoError(t, s.InsertNode(ctx, makeUUIDNode(testNodeUUID(1), tree, rootUser, 0, nil, nil, nil)))
		require.NoError(t, s.InsertNode(ctx,
			makeUUIDNode(testNodeUUID(2), tree, testUserUUID(2), 1, ptr(rootUser), ptr(rootUser), intPtr(0))))

		err := s.InsertNode(ctx,
			makeUUIDNode(testNodeUUID(3), tree, testUserUUID(3), 1, ptr(rootUser), ptr(rootUser), intPtr(0)))
		assert.ErrorIs(t, err, ErrSlotConflict, "one active claim per tree, parent and position")
	})

	t.Run("InsertNode rejects a second active root in a tree", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		tree := testTreeUUID(1)

		require.NoError(t, s.InsertNode(ctx, makeUUIDNode(testNodeUUID(1), tree, testUserUUID(1), 0, nil, nil, nil)))

		// Two different users, so this row violates the root index alone.
		// A same-user row would violate the active-user index too, and
		// Postgres picks which of two violated indexes it reports.
		err := s.InsertNode(ctx, makeUUIDNode(testNodeUUID(2), tree, testUserUUID(2), 0, nil, nil, nil))
		require.ErrorIs(t, err, ErrRootConflict)

		// A removed root does not block its replacement (ADR-023).
		require.NoError(t, s.DeleteNode(ctx, tree, testUserUUID(1)))
		require.NoError(t, s.InsertNode(ctx, makeUUIDNode(testNodeUUID(3), tree, testUserUUID(3), 0, nil, nil, nil)))
	})

	// A refused insert must not be a disguised update. On Postgres that is the
	// difference between DO NOTHING and DO UPDATE; in the double it is whether
	// the duplicate check runs before the append. Both stores keep the row the
	// first event wrote, so the caller cannot use a redelivery to rewrite it.
	t.Run("a refused insert leaves the stored row alone", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		tree := testTreeUUID(1)
		user := testUserUUID(1)
		require.NoError(t, s.InsertNode(ctx, makeUUIDNode(testNodeUUID(1), tree, user, 0, nil, nil, nil)))

		redelivered := makeUUIDNode(testNodeUUID(1), tree, user, 7, nil, nil, nil)
		require.ErrorIs(t, s.InsertNode(ctx, redelivered), ErrNodeAlreadyProjected)

		got, err := s.GetNode(ctx, tree, user)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, 0, got.Depth, "the stored depth is the first event's, not the redelivery's")
	})

	// The discriminator HEU-576 is built on. A redelivered event carries the
	// row id it already wrote, so it violates the primary key and the user
	// index against the same row, and only the primary key means "already
	// projected". Both stores must name that one.
	//
	// Asserted through errors.Is, which is the form a consumer branches on.
	// Both stores answer with the sentinel now: Postgres from the skipped
	// ON CONFLICT row, the memory double from its primary-key mirror.
	//
	// Which conflict wins when two are violated against different rows is
	// deliberately not asserted. The stores disagree there and Postgres's own
	// answer follows relation OID order, which an index rebuild changes with
	// nothing to catch it. HEU-794.
	t.Run("a redelivered row is refused by the primary key, not the user index", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		tree := testTreeUUID(1)
		user := testUserUUID(1)
		node := makeUUIDNode(testNodeUUID(1), tree, user, 0, nil, nil, nil)

		require.NoError(t, s.InsertNode(ctx, node))

		err := s.InsertNode(ctx, node)
		assert.ErrorIs(t, err, ErrNodeAlreadyProjected,
			"the primary key is the branch that means already projected")
	})
}
