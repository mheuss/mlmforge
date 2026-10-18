package networkengine

import (
	"context"
	"strings"
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
// stampOf reads one row's removal stamp by the row's ID, whether or not the
// row is active. setUpdatedAt and setRemovedAt overwrite one row's update time
// and removal time by the row's ID.
func runTreeStoreSuite(
	t *testing.T,
	newStore func(t *testing.T) TreeStore,
	stampOf func(t *testing.T, s TreeStore, nodeID string) *string,
	setUpdatedAt func(t *testing.T, s TreeStore, nodeID string, at time.Time),
	setRemovedAt func(t *testing.T, s TreeStore, nodeID string, at time.Time),
) {
	t.Helper()

	t.Run("ProjectInsert then GetNode returns the row", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		tree := testTreeUUID(1)
		rootUser := testUserUUID(1)
		childUser := testUserUUID(2)

		require.NoError(t, s.ProjectInsert(ctx, makeUUIDNode(testNodeUUID(1), tree, rootUser, 0, nil, nil, nil), 1))
		require.NoError(t, s.ProjectInsert(ctx,
			makeUUIDNode(testNodeUUID(2), tree, childUser, 1, ptr(rootUser), ptr(rootUser), intPtr(0)), 2))

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

		require.NoError(t, insertRow(ctx, s,
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
		require.NoError(t, insertRow(ctx, s,
			makeUUIDNode(testNodeUUID(1), testTreeUUID(1), user, 0, nil, nil, nil)))

		got, err := s.GetNode(ctx, testTreeUUID(2), user)
		require.NoError(t, err)
		assert.Nil(t, got, "the user exists, but not in this tree")
	})

	t.Run("an active row carries no removal event id", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		tree := testTreeUUID(1)
		user := testUserUUID(1)

		require.NoError(t, insertRow(ctx, s, makeUUIDNode(testNodeUUID(1), tree, user, 0, nil, nil, nil)))

		got, err := s.GetNode(ctx, tree, user)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Nil(t, got.RemovedByEventID, "an inserted row has no removal stamp")

		all, err := s.GetByTree(ctx, tree)
		require.NoError(t, err)
		require.Len(t, all, 1)
		assert.Nil(t, all[0].RemovedByEventID, "an inserted row has no removal stamp")
	})

	t.Run("GetNode returns nil for a soft-deleted user", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		tree := testTreeUUID(1)
		user := testUserUUID(1)

		require.NoError(t, insertRow(ctx, s, makeUUIDNode(testNodeUUID(1), tree, user, 0, nil, nil, nil)))
		require.NoError(t, tombstoneUser(ctx, s, tree, user))

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

		require.NoError(t, insertRow(ctx, s, makeUUIDNode(testNodeUUID(1), tree, rootUser, 0, nil, nil, nil)))
		require.NoError(t, insertRow(ctx, s,
			makeUUIDNode(testNodeUUID(2), tree, keptUser, 1, ptr(rootUser), ptr(rootUser), intPtr(0))))
		require.NoError(t, insertRow(ctx, s,
			makeUUIDNode(testNodeUUID(3), tree, removedUser, 1, ptr(rootUser), ptr(rootUser), intPtr(1))))
		// A grandchild, to prove GetChildren is one level and not a subtree walk.
		require.NoError(t, insertRow(ctx, s,
			makeUUIDNode(testNodeUUID(4), tree, grandchildUser, 2, ptr(keptUser), ptr(keptUser), intPtr(0))))

		require.NoError(t, tombstoneUser(ctx, s, tree, removedUser))

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

		require.NoError(t, insertRow(ctx, s, makeUUIDNode(testNodeUUID(1), tree, rootUser, 0, nil, nil, nil)))
		require.NoError(t, insertRow(ctx, s,
			makeUUIDNode(testNodeUUID(2), tree, keptUser, 1, ptr(rootUser), ptr(rootUser), intPtr(0))))
		require.NoError(t, insertRow(ctx, s,
			makeUUIDNode(testNodeUUID(3), tree, removedUser, 1, ptr(rootUser), ptr(rootUser), intPtr(1))))
		require.NoError(t, insertRow(ctx, s,
			makeUUIDNode(testNodeUUID(4), otherTree, testUserUUID(4), 0, nil, nil, nil)))

		require.NoError(t, tombstoneUser(ctx, s, tree, removedUser))

		got, err := s.GetByTree(ctx, tree)
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{rootUser, keptUser}, nodeUserIDs(got),
			"active rows of this tree only")
	})

	// The tombstone is asserted through every read path rather than GetNode
	// alone, so a soft delete that one query honors and another ignores fails.
	t.Run("a tombstoned row is invisible to every read", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		tree := testTreeUUID(1)
		rootUser := testUserUUID(1)
		childUser := testUserUUID(2)

		require.NoError(t, insertRow(ctx, s, makeUUIDNode(testNodeUUID(1), tree, rootUser, 0, nil, nil, nil)))
		require.NoError(t, insertRow(ctx, s,
			makeUUIDNode(testNodeUUID(2), tree, childUser, 1, ptr(rootUser), ptr(rootUser), intPtr(0))))

		require.NoError(t, tombstoneUser(ctx, s, tree, childUser))

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

	t.Run("GetNodeByRemovalEvent returns the row that event removed", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		tree := testTreeUUID(1)
		rootUser := testUserUUID(1)
		removedUser := testUserUUID(2)
		otherRemoved := testUserUUID(3)
		removalEvent := testNodeUUID(9)

		require.NoError(t, insertRow(ctx, s, makeUUIDNode(testNodeUUID(1), tree, rootUser, 0, nil, nil, nil)))
		require.NoError(t, insertRow(ctx, s,
			makeUUIDNode(testNodeUUID(2), tree, otherRemoved, 1, ptr(rootUser), ptr(rootUser), intPtr(0))))
		require.NoError(t, insertRow(ctx, s,
			makeUUIDNode(testNodeUUID(3), tree, removedUser, 1, ptr(rootUser), ptr(rootUser), intPtr(1))))
		require.NoError(t, stampRemoval(ctx, s, tree, otherRemoved, testNodeUUID(8), 1))
		require.NoError(t, stampRemoval(ctx, s, tree, removedUser, removalEvent, 2))

		got, err := s.GetNodeByRemovalEvent(ctx, tree, removalEvent)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, testNodeUUID(3), got.ID, "the placement this event removed, not another tombstone")
		require.NotNil(t, got.RemovedByEventID)
		assert.Equal(t, removalEvent, *got.RemovedByEventID)
	})

	t.Run("GetNodeByRemovalEvent returns the newest of two rows one event stamped", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		tree := testTreeUUID(1)
		rootUser := testUserUUID(1)
		user := testUserUUID(2)
		removalEvent := testNodeUUID(9)

		require.NoError(t, insertRow(ctx, s, makeUUIDNode(testNodeUUID(1), tree, rootUser, 0, nil, nil, nil)))
		require.NoError(t, insertRow(ctx, s,
			makeUUIDNode(testNodeUUID(2), tree, user, 1, ptr(rootUser), ptr(rootUser), intPtr(0))))
		require.NoError(t, stampRemoval(ctx, s, tree, user, removalEvent, 1))
		require.NoError(t, insertRow(ctx, s,
			makeUUIDNode(testNodeUUID(3), tree, user, 1, ptr(rootUser), ptr(rootUser), intPtr(1))))
		require.NoError(t, stampRemoval(ctx, s, tree, user, removalEvent, 2))

		got, err := s.GetNodeByRemovalEvent(ctx, tree, removalEvent)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, testNodeUUID(3), got.ID, "the later of the two tombstones this event stamped")
	})

	t.Run("GetNodeByRemovalEvent returns nil for an event that removed nothing", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		tree := testTreeUUID(1)
		rootUser := testUserUUID(1)
		removedUser := testUserUUID(2)

		require.NoError(t, insertRow(ctx, s, makeUUIDNode(testNodeUUID(1), tree, rootUser, 0, nil, nil, nil)))
		require.NoError(t, insertRow(ctx, s,
			makeUUIDNode(testNodeUUID(2), tree, removedUser, 1, ptr(rootUser), ptr(rootUser), intPtr(0))))
		require.NoError(t, stampRemoval(ctx, s, tree, removedUser, testNodeUUID(8), 1))

		got, err := s.GetNodeByRemovalEvent(ctx, tree, testNodeUUID(9))
		require.NoError(t, err, "an event that removed nothing is not an error")
		assert.Nil(t, got)
	})

	t.Run("GetNodeByRemovalEvent does not cross tree boundaries", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		tree := testTreeUUID(1)
		otherTree := testTreeUUID(2)
		rootUser := testUserUUID(1)
		removedUser := testUserUUID(2)
		removalEvent := testNodeUUID(9)

		require.NoError(t, insertRow(ctx, s, makeUUIDNode(testNodeUUID(1), tree, rootUser, 0, nil, nil, nil)))
		require.NoError(t, insertRow(ctx, s, makeUUIDNode(testNodeUUID(2), otherTree, rootUser, 0, nil, nil, nil)))
		require.NoError(t, insertRow(ctx, s,
			makeUUIDNode(testNodeUUID(3), otherTree, removedUser, 1, ptr(rootUser), ptr(rootUser), intPtr(0))))
		require.NoError(t, stampRemoval(ctx, s, otherTree, removedUser, removalEvent, 1))

		got, err := s.GetNodeByRemovalEvent(ctx, tree, removalEvent)
		require.NoError(t, err)
		assert.Nil(t, got, "the stamp exists, but not in this tree")
	})

	t.Run("GetNodeIncludingRemoved returns nil for an absent user", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		require.NoError(t, insertRow(ctx, s,
			makeUUIDNode(testNodeUUID(1), testTreeUUID(1), testUserUUID(1), 0, nil, nil, nil)))

		got, err := s.GetNodeIncludingRemoved(ctx, testTreeUUID(1), testUserUUID(99))
		require.NoError(t, err)
		assert.Nil(t, got)
	})

	t.Run("GetNodeIncludingRemoved returns the active row", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		tree, user := testTreeUUID(1), testUserUUID(1)
		require.NoError(t, insertRow(ctx, s, makeUUIDNode(testNodeUUID(1), tree, user, 0, nil, nil, nil)))

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
		require.NoError(t, insertRow(ctx, s, makeUUIDNode(testNodeUUID(1), tree, user, 0, nil, nil, nil)))
		require.NoError(t, tombstoneUser(ctx, s, tree, user))

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
		require.NoError(t, insertRow(ctx, s, makeUUIDNode(testNodeUUID(1), tree, user, 0, nil, nil, nil)))
		require.NoError(t, tombstoneUser(ctx, s, tree, user))
		require.NoError(t, insertRow(ctx, s, makeUUIDNode(testNodeUUID(2), tree, user, 0, nil, nil, nil)))

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
		require.NoError(t, insertRow(ctx, s, makeUUIDNode(testNodeUUID(1), tree, user, 0, nil, nil, nil)))
		require.NoError(t, tombstoneUser(ctx, s, tree, user))
		require.NoError(t, insertRow(ctx, s, makeUUIDNode(testNodeUUID(2), tree, user, 0, nil, nil, nil)))
		require.NoError(t, tombstoneUser(ctx, s, tree, user))

		got, err := s.GetNodeIncludingRemoved(ctx, tree, user)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, testNodeUUID(2), got.ID, "the most recent removal, not the first")
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
		require.NoError(t, insertRow(ctx, s, deep))
		require.NoError(t, insertRow(ctx, s, late))
		require.NoError(t, insertRow(ctx, s, early))
		require.NoError(t, insertRow(ctx, s, root))

		// Another tree, at a depth that would sort into the middle. A
		// tree-scope leak here returns another tenant's nodes inside an
		// ordered read, where the caller has no reason to re-filter.
		other := makeUUIDNode(testNodeUUID(5), testTreeUUID(2), testUserUUID(5), 1, nil, nil, nil)
		other.EnrolledAt = time.Date(2026, 2, 15, 0, 0, 0, 0, time.UTC)
		require.NoError(t, insertRow(ctx, s, other))

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
	t.Run("ProjectInsert round-trips EnrolledAt", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		tree := testTreeUUID(1)
		user := testUserUUID(1)
		node := makeUUIDNode(testNodeUUID(1), tree, user, 0, nil, nil, nil)
		node.EnrolledAt = time.Date(2026, 5, 17, 13, 45, 6, 0, time.UTC)

		require.NoError(t, s.ProjectInsert(ctx, node, 1))

		got, err := s.GetNode(ctx, tree, user)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.True(t, node.EnrolledAt.Equal(got.EnrolledAt),
			"EnrolledAt round-trips as the same instant, got %v want %v", got.EnrolledAt, node.EnrolledAt)
	})

	t.Run("ProjectInsert stamps CreatedAt and UpdatedAt over the caller's values", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		tree := testTreeUUID(1)
		user := testUserUUID(1)
		node := makeUUIDNode(testNodeUUID(1), tree, user, 0, nil, nil, nil)
		node.CreatedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		node.UpdatedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

		before := time.Now().Add(-time.Second)
		require.NoError(t, s.ProjectInsert(ctx, node, 1))

		got, err := s.GetNode(ctx, tree, user)
		require.NoError(t, err)
		require.NotNil(t, got)
		after := time.Now().Add(time.Second)
		assert.True(t, got.CreatedAt.After(before) && got.CreatedAt.Before(after),
			"CreatedAt read back as %v, want between %v and %v", got.CreatedAt, before, after)
		assert.True(t, got.CreatedAt.Equal(got.UpdatedAt),
			"CreatedAt read back as %v and UpdatedAt as %v, want the same instant", got.CreatedAt, got.UpdatedAt)
	})

	t.Run("GetChildren does not cross tree boundaries", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		treeA := testTreeUUID(1)
		treeB := testTreeUUID(2)
		sharedUser := testUserUUID(1)
		kidA := testUserUUID(2)
		kidB := testUserUUID(3)

		require.NoError(t, insertRow(ctx, s, makeUUIDNode(testNodeUUID(1), treeA, sharedUser, 0, nil, nil, nil)))
		require.NoError(t, insertRow(ctx, s, makeUUIDNode(testNodeUUID(2), treeB, sharedUser, 0, nil, nil, nil)))
		require.NoError(t, insertRow(ctx, s,
			makeUUIDNode(testNodeUUID(3), treeA, kidA, 1, ptr(sharedUser), ptr(sharedUser), intPtr(0))))
		require.NoError(t, insertRow(ctx, s,
			makeUUIDNode(testNodeUUID(4), treeB, kidB, 1, ptr(sharedUser), ptr(sharedUser), intPtr(0))))

		children, err := s.GetChildren(ctx, treeA, sharedUser)
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{kidA}, nodeUserIDs(children),
			"the same user parents a child in both trees; only this tree's child comes back")
	})

	// UC-NET-014 rests on the double rejecting the same writes Postgres
	// rejects, so each constraint below has a case on both stores.
	t.Run("ProjectInsert rejects a duplicate row id", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		require.NoError(t, s.ProjectInsert(ctx,
			makeUUIDNode(testNodeUUID(1), testTreeUUID(1), testUserUUID(1), 0, nil, nil, nil), 1))

		err := s.ProjectInsert(ctx,
			makeUUIDNode(testNodeUUID(1), testTreeUUID(2), testUserUUID(2), 0, nil, nil, nil), 1)
		assert.ErrorIs(t, err, ErrNodeAlreadyProjected,
			"the primary key is not partial, so a different tree and user does not excuse it")
	})

	// The primary key is not partial, so soft-deleting the row does not free
	// its id. A redelivered event whose row was since removed must still be
	// refused, or redelivery inserts a second row in memory and is rejected in
	// Postgres.
	t.Run("ProjectInsert rejects a duplicate row id after a soft delete", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		tree := testTreeUUID(1)
		user := testUserUUID(1)
		node := makeUUIDNode(testNodeUUID(1), tree, user, 0, nil, nil, nil)

		require.NoError(t, s.ProjectInsert(ctx, node, 1))
		require.NoError(t, tombstoneUser(ctx, s, tree, user))

		err := s.ProjectInsert(ctx, node, 1)
		assert.ErrorIs(t, err, ErrNodeAlreadyProjected, "a removed row still holds its id")
	})

	t.Run("ProjectInsert rejects a second active row for one user in a tree", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		tree := testTreeUUID(1)
		rootUser := testUserUUID(1)
		user := testUserUUID(2)

		require.NoError(t, s.ProjectInsert(ctx, makeUUIDNode(testNodeUUID(1), tree, rootUser, 0, nil, nil, nil), 1))
		// Depth 1 on the conflicting pair is deliberate; see HEU-810.
		require.NoError(t, s.ProjectInsert(ctx,
			makeUUIDNode(testNodeUUID(2), tree, user, 1, ptr(rootUser), ptr(rootUser), intPtr(0)), 2))

		err := s.ProjectInsert(ctx,
			makeUUIDNode(testNodeUUID(3), tree, user, 1, ptr(rootUser), ptr(rootUser), intPtr(1)), 3)
		require.ErrorIs(t, err, ErrActiveUserConflict)

		// The sentinel says which branch. It cannot say which row, and this is
		// the error an operator reads when a projection stops.
		assert.Contains(t, err.Error(), tree, "the message names the tree")
		assert.Contains(t, err.Error(), user, "the message names the user")
	})

	t.Run("ProjectInsert rejects a second active claim on one slot", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		tree := testTreeUUID(1)
		rootUser := testUserUUID(1)

		require.NoError(t, s.ProjectInsert(ctx, makeUUIDNode(testNodeUUID(1), tree, rootUser, 0, nil, nil, nil), 1))
		require.NoError(t, s.ProjectInsert(ctx,
			makeUUIDNode(testNodeUUID(2), tree, testUserUUID(2), 1, ptr(rootUser), ptr(rootUser), intPtr(0)), 2))

		err := s.ProjectInsert(ctx,
			makeUUIDNode(testNodeUUID(3), tree, testUserUUID(3), 1, ptr(rootUser), ptr(rootUser), intPtr(0)), 3)
		assert.ErrorIs(t, err, ErrSlotConflict, "one active claim per tree, parent and position")
	})

	t.Run("ProjectInsert rejects a second active root in a tree", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		tree := testTreeUUID(1)

		require.NoError(t, s.ProjectInsert(ctx, makeUUIDNode(testNodeUUID(1), tree, testUserUUID(1), 0, nil, nil, nil), 1))

		// Two different users, so this row violates the root index alone.
		// A same-user row would violate the active-user index too, and
		// Postgres picks which of two violated indexes it reports.
		err := s.ProjectInsert(ctx, makeUUIDNode(testNodeUUID(2), tree, testUserUUID(2), 0, nil, nil, nil), 2)
		require.ErrorIs(t, err, ErrRootConflict)

		// A removed root does not block its replacement (ADR-023).
		require.NoError(t, tombstoneUser(ctx, s, tree, testUserUUID(1)))
		require.NoError(t, s.ProjectInsert(ctx, makeUUIDNode(testNodeUUID(3), tree, testUserUUID(3), 0, nil, nil, nil), 3))
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
		require.NoError(t, s.ProjectInsert(ctx, makeUUIDNode(testNodeUUID(1), tree, user, 0, nil, nil, nil), 1))

		redelivered := makeUUIDNode(testNodeUUID(1), tree, user, 7, nil, nil, nil)
		require.ErrorIs(t, s.ProjectInsert(ctx, redelivered, 1), ErrNodeAlreadyProjected)

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

		require.NoError(t, s.ProjectInsert(ctx, node, 1))

		err := s.ProjectInsert(ctx, node, 1)
		assert.ErrorIs(t, err, ErrNodeAlreadyProjected,
			"the primary key is the branch that means already projected")
	})

	t.Run("every read refuses a cancelled context", func(t *testing.T) {
		s := newStore(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		tree := testTreeUUID(1)
		user := testUserUUID(1)

		calls := []struct {
			name string
			call func() error
		}{
			{"GetNode", func() error {
				_, err := s.GetNode(ctx, tree, user)
				return err
			}},
			{"GetNodeIncludingRemoved", func() error {
				_, err := s.GetNodeIncludingRemoved(ctx, tree, user)
				return err
			}},
			{"GetNodeByRemovalEvent", func() error {
				_, err := s.GetNodeByRemovalEvent(ctx, tree, testNodeUUID(9))
				return err
			}},
			{"GetChildren", func() error {
				_, err := s.GetChildren(ctx, tree, user)
				return err
			}},
			{"GetByTree", func() error {
				_, err := s.GetByTree(ctx, tree)
				return err
			}},
			{"GetByTreeDepthOrdered", func() error {
				_, err := s.GetByTreeDepthOrdered(ctx, tree)
				return err
			}},
		}
		for _, c := range calls {
			assert.ErrorIs(t, c.call(), context.Canceled, c.name)
		}
	})

	t.Run("a rejection moves UpdatedAt forward", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		tree := testTreeUUID(1)
		user := testUserUUID(1)
		stale := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

		require.NoError(t, s.ProjectInsert(ctx, makeUUIDNode(testNodeUUID(1), tree, user, 0, nil, nil, nil), 1))
		setUpdatedAt(t, s, testNodeUUID(1), stale)
		backdated, err := s.GetNode(ctx, tree, user)
		require.NoError(t, err)
		require.NotNil(t, backdated)
		require.True(t, backdated.UpdatedAt.Equal(stale),
			"UpdatedAt read back as %v after setUpdatedAt, want %v", backdated.UpdatedAt, stale)

		require.NoError(t, s.ProjectRejection(ctx, tree, testNodeUUID(1), 2))

		got, err := s.GetNodeIncludingRemoved(ctx, tree, user)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.NotNil(t, got.RemovedAt, "no removed_at read back after the rejection")
		assert.True(t, got.UpdatedAt.After(stale),
			"UpdatedAt read back as %v after the rejection, want later than %v", got.UpdatedAt, stale)
	})

	t.Run("GetNodeIncludingRemoved breaks a removal-time tie by the highest id", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		tree := testTreeUUID(1)
		userA := testUserUUID(1)
		userB := testUserUUID(2)
		tie := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

		// userA's higher id is placed second and userB's first, so neither a
		// first-found nor a last-found rule returns both expected rows.
		for _, p := range []struct{ id, user string }{
			{testNodeUUID(1), userA}, {testNodeUUID(2), userA},
			{testNodeUUID(4), userB}, {testNodeUUID(3), userB},
		} {
			require.NoError(t, insertRow(ctx, s, makeUUIDNode(p.id, tree, p.user, 1, nil, nil, nil)))
			require.NoError(t, tombstoneUser(ctx, s, tree, p.user))
			setRemovedAt(t, s, p.id, tie)
			back, err := s.GetNodeIncludingRemoved(ctx, tree, p.user)
			require.NoError(t, err)
			require.NotNil(t, back)
			require.NotNil(t, back.RemovedAt)
			require.True(t, back.RemovedAt.Equal(tie),
				"removed_at read back as %v after setRemovedAt on %s, want %v", *back.RemovedAt, p.id, tie)
		}

		gotA, err := s.GetNodeIncludingRemoved(ctx, tree, userA)
		require.NoError(t, err)
		require.NotNil(t, gotA)
		assert.Equal(t, testNodeUUID(2), gotA.ID, "tombstone id read back for userA, whose two tombstones share removed_at")

		gotB, err := s.GetNodeIncludingRemoved(ctx, tree, userB)
		require.NoError(t, err)
		require.NotNil(t, gotB)
		assert.Equal(t, testNodeUUID(4), gotB.ID, "tombstone id read back for userB, whose two tombstones share removed_at")
	})

	t.Run("GetNodeByRemovalEvent breaks a removal-time tie by the highest id", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		tree := testTreeUUID(1)
		user := testUserUUID(1)
		eventA := testNodeUUID(8)
		eventB := testNodeUUID(9)
		tie := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

		// eventA stamps its higher id second and eventB stamps its higher id
		// first, so neither a first-found nor a last-found rule returns both.
		for i, p := range []struct{ id, event string }{
			{testNodeUUID(1), eventA}, {testNodeUUID(2), eventA},
			{testNodeUUID(4), eventB}, {testNodeUUID(3), eventB},
		} {
			require.NoError(t, insertRow(ctx, s, makeUUIDNode(p.id, tree, user, 1, nil, nil, nil)))
			require.NoError(t, stampRemoval(ctx, s, tree, user, p.event, int64(i+1)))
			setRemovedAt(t, s, p.id, tie)
			back, err := s.GetNodeByRemovalEvent(ctx, tree, p.event)
			require.NoError(t, err)
			require.NotNil(t, back)
			require.NotNil(t, back.RemovedAt)
			require.True(t, back.RemovedAt.Equal(tie),
				"removed_at read back as %v after setRemovedAt on %s, want %v", *back.RemovedAt, p.id, tie)
		}

		gotA, err := s.GetNodeByRemovalEvent(ctx, tree, eventA)
		require.NoError(t, err)
		require.NotNil(t, gotA)
		assert.Equal(t, testNodeUUID(2), gotA.ID, "tombstone id read back for eventA, whose two tombstones share removed_at")

		gotB, err := s.GetNodeByRemovalEvent(ctx, tree, eventB)
		require.NoError(t, err)
		require.NotNil(t, gotB)
		assert.Equal(t, testNodeUUID(4), gotB.ID, "tombstone id read back for eventB, whose two tombstones share removed_at")
	})

	t.Run("the removal-time tie-break reads a mixed-case id as lowercase", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		tree := testTreeUUID(1)
		user := testUserUUID(1)
		tie := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
		lower := "bbbbbbbb-bbbb-bbbb-bbbb-00000000000a"
		upper := "BBBBBBBB-BBBB-BBBB-BBBB-00000000000B"

		event := testNodeUUID(9)

		for i, id := range []string{lower, upper} {
			require.NoError(t, insertRow(ctx, s, makeUUIDNode(id, tree, user, 1, nil, nil, nil)))
			require.NoError(t, stampRemoval(ctx, s, tree, user, event, int64(i+1)))
			setRemovedAt(t, s, id, tie)
			back, err := s.GetNodeIncludingRemoved(ctx, tree, user)
			require.NoError(t, err)
			require.NotNil(t, back)
			require.NotNil(t, back.RemovedAt)
			require.True(t, back.RemovedAt.Equal(tie),
				"removed_at read back as %v after setRemovedAt on %s, want %v", *back.RemovedAt, id, tie)
		}

		byUser, err := s.GetNodeIncludingRemoved(ctx, tree, user)
		require.NoError(t, err)
		require.NotNil(t, byUser)
		assert.True(t, strings.EqualFold(upper, byUser.ID),
			"GetNodeIncludingRemoved returned id %s, want %s, which is the higher id once lowercased", byUser.ID, upper)

		byEvent, err := s.GetNodeByRemovalEvent(ctx, tree, event)
		require.NoError(t, err)
		require.NotNil(t, byEvent)
		assert.True(t, strings.EqualFold(upper, byEvent.ID),
			"GetNodeByRemovalEvent returned id %s, want %s, which is the higher id once lowercased", byEvent.ID, upper)
	})

	t.Run("a later removal wins over a higher id in both reads", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		tree := testTreeUUID(1)
		user := testUserUUID(1)
		event := testNodeUUID(9)
		earlier := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
		later := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)

		// The higher id gets the earlier removal, so an order on id alone
		// returns the wrong row. Both stamps are in the past, so a hook that did
		// not land leaves a later removed_at than either.
		for i, p := range []struct {
			id string
			at time.Time
		}{
			{testNodeUUID(2), earlier},
			{testNodeUUID(1), later},
		} {
			require.NoError(t, insertRow(ctx, s, makeUUIDNode(p.id, tree, user, 1, nil, nil, nil)))
			require.NoError(t, stampRemoval(ctx, s, tree, user, event, int64(i+1)))
			setRemovedAt(t, s, p.id, p.at)
			back, err := s.GetNodeIncludingRemoved(ctx, tree, user)
			require.NoError(t, err)
			require.NotNil(t, back)
			require.NotNil(t, back.RemovedAt)
			require.False(t, back.RemovedAt.After(later),
				"removed_at read back as %v after setRemovedAt on %s, want no later than %v", *back.RemovedAt, p.id, later)
		}

		byUser, err := s.GetNodeIncludingRemoved(ctx, tree, user)
		require.NoError(t, err)
		require.NotNil(t, byUser)
		assert.Equal(t, testNodeUUID(1), byUser.ID,
			"GetNodeIncludingRemoved returned id %s, want %s, the later removal", byUser.ID, testNodeUUID(1))

		byEvent, err := s.GetNodeByRemovalEvent(ctx, tree, event)
		require.NoError(t, err)
		require.NotNil(t, byEvent)
		assert.Equal(t, testNodeUUID(1), byEvent.ID,
			"GetNodeByRemovalEvent returned id %s, want %s, the later removal", byEvent.ID, testNodeUUID(1))
	})

	t.Run("a removal stamps the active row, not an older tombstone", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		tree := testTreeUUID(1)
		rootUser := testUserUUID(1)
		user := testUserUUID(2)
		removalEvent := testNodeUUID(9)

		require.NoError(t, insertRow(ctx, s, makeUUIDNode(testNodeUUID(1), tree, rootUser, 0, nil, nil, nil)))
		require.NoError(t, insertRow(ctx, s,
			makeUUIDNode(testNodeUUID(2), tree, user, 1, ptr(rootUser), ptr(rootUser), intPtr(0))))
		require.NoError(t, tombstoneUser(ctx, s, tree, user))
		require.NoError(t, insertRow(ctx, s,
			makeUUIDNode(testNodeUUID(3), tree, user, 1, ptr(rootUser), ptr(rootUser), intPtr(1))))

		require.NoError(t, stampRemoval(ctx, s, tree, user, removalEvent, 1))

		assert.Nil(t, stampOf(t, s, testNodeUUID(2)), "removal stamp read back on the older tombstone")
		got, err := s.GetNodeIncludingRemoved(ctx, tree, user)
		require.NoError(t, err)
		require.NotNil(t, got)
		require.Equal(t, testNodeUUID(3), got.ID,
			"GetNodeIncludingRemoved returned id %s, want %s, the later placement", got.ID, testNodeUUID(3))
		assert.NotNil(t, got.RemovedAt, "no removed_at read back on the user's latest row")
		require.NotNil(t, got.RemovedByEventID, "no removal stamp read back on the user's latest row")
		assert.Equal(t, removalEvent, *got.RemovedByEventID)
	})

	t.Run("a removal moves UpdatedAt forward on the removed row and the recruit", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		tree := testTreeUUID(1)
		rootUser := testUserUUID(1)
		removedUser := testUserUUID(2)
		recruit := testUserUUID(3)
		stale := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

		require.NoError(t, insertRow(ctx, s, makeUUIDNode(testNodeUUID(1), tree, rootUser, 0, nil, nil, nil)))
		require.NoError(t, insertRow(ctx, s,
			makeUUIDNode(testNodeUUID(2), tree, removedUser, 1, ptr(rootUser), ptr(rootUser), intPtr(0))))
		require.NoError(t, insertRow(ctx, s,
			makeUUIDNode(testNodeUUID(3), tree, recruit, 2, ptr(removedUser), ptr(removedUser), intPtr(0))))
		setUpdatedAt(t, s, testNodeUUID(2), stale)
		setUpdatedAt(t, s, testNodeUUID(3), stale)
		for _, u := range []string{removedUser, recruit} {
			backdated, err := s.GetNode(ctx, tree, u)
			require.NoError(t, err)
			require.NotNil(t, backdated)
			require.True(t, backdated.UpdatedAt.Equal(stale),
				"%s's UpdatedAt read back as %v after setUpdatedAt, want %v", u, backdated.UpdatedAt, stale)
		}

		require.NoError(t, s.ProjectRemoval(ctx, tree, RemovalProjection{
			Users:       []string{removedUser},
			Responsored: []Responsored{{UserID: recruit, NewSponsorID: rootUser}},
		}, testNodeUUID(9), 1))

		removed, err := s.GetNodeIncludingRemoved(ctx, tree, removedUser)
		require.NoError(t, err)
		require.NotNil(t, removed)
		assert.True(t, removed.UpdatedAt.After(stale),
			"removed row's UpdatedAt read back as %v, want later than %v", removed.UpdatedAt, stale)

		moved, err := s.GetNode(ctx, tree, recruit)
		require.NoError(t, err)
		require.NotNil(t, moved)
		assert.True(t, moved.UpdatedAt.After(stale),
			"recruit's UpdatedAt read back as %v, want later than %v", moved.UpdatedAt, stale)
	})

	t.Run("a removal does not cross tree boundaries", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		treeA := testTreeUUID(1)
		treeB := testTreeUUID(2)
		sharedUser := testUserUUID(1)

		require.NoError(t, insertRow(ctx, s, makeUUIDNode(testNodeUUID(1), treeA, sharedUser, 0, nil, nil, nil)))
		require.NoError(t, insertRow(ctx, s, makeUUIDNode(testNodeUUID(2), treeB, sharedUser, 0, nil, nil, nil)))

		// Removing from treeB, the row inserted second, so a removal that lands
		// on the first row it finds does not pass.
		require.NoError(t, stampRemoval(ctx, s, treeB, sharedUser, testNodeUUID(9), 1))

		survivor, err := s.GetNode(ctx, treeA, sharedUser)
		require.NoError(t, err)
		assert.NotNil(t, survivor, "no active row read back in treeA after a removal in treeB")

		removed, err := s.GetNode(ctx, treeB, sharedUser)
		require.NoError(t, err)
		assert.Nil(t, removed, "treeB's active row read back after its removal")
	})

	t.Run("a removal of a user with only a tombstone fails and leaves the tombstone alone", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		tree := testTreeUUID(1)
		rootUser := testUserUUID(1)
		goneUser := testUserUUID(2)

		require.NoError(t, insertRow(ctx, s, makeUUIDNode(testNodeUUID(1), tree, rootUser, 0, nil, nil, nil)))
		require.NoError(t, insertRow(ctx, s,
			makeUUIDNode(testNodeUUID(2), tree, goneUser, 1, ptr(rootUser), ptr(rootUser), intPtr(0))))
		require.NoError(t, tombstoneUser(ctx, s, tree, goneUser))
		before, err := s.GetNodeIncludingRemoved(ctx, tree, goneUser)
		require.NoError(t, err)
		require.NotNil(t, before)
		require.NotNil(t, before.RemovedAt)

		err = stampRemoval(ctx, s, tree, goneUser, testNodeUUID(9), 1)

		assert.ErrorContains(t, err, "matched 0 of 1 active rows")
		assert.Nil(t, stampOf(t, s, testNodeUUID(2)), "removal stamp read back on the existing tombstone")
		after, err := s.GetNodeIncludingRemoved(ctx, tree, goneUser)
		require.NoError(t, err)
		require.NotNil(t, after)
		require.NotNil(t, after.RemovedAt, "no removed_at read back on the existing tombstone")
		assert.True(t, before.RemovedAt.Equal(*after.RemovedAt),
			"removed_at read back as %v, was %v before the removal", *after.RemovedAt, *before.RemovedAt)
	})

	t.Run("a removal re-sponsors only in its own tree", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		treeA := testTreeUUID(1)
		treeB := testTreeUUID(2)
		rootUser := testUserUUID(1)
		removedUser := testUserUUID(2)
		recruit := testUserUUID(3)

		// The recruit's treeA row goes in first, so a re-sponsor that ignores
		// the tree meets it before treeB's row.
		require.NoError(t, insertRow(ctx, s, makeUUIDNode(testNodeUUID(1), treeA, recruit, 0, nil, nil, nil)))
		require.NoError(t, insertRow(ctx, s, makeUUIDNode(testNodeUUID(2), treeB, rootUser, 0, nil, nil, nil)))
		require.NoError(t, insertRow(ctx, s,
			makeUUIDNode(testNodeUUID(3), treeB, removedUser, 1, ptr(rootUser), ptr(rootUser), intPtr(0))))
		require.NoError(t, insertRow(ctx, s,
			makeUUIDNode(testNodeUUID(4), treeB, recruit, 2, ptr(removedUser), ptr(removedUser), intPtr(0))))

		require.NoError(t, s.ProjectRemoval(ctx, treeB, RemovalProjection{
			Users:       []string{removedUser},
			Responsored: []Responsored{{UserID: recruit, NewSponsorID: rootUser}},
		}, testNodeUUID(9), 1))

		other, err := s.GetNode(ctx, treeA, recruit)
		require.NoError(t, err)
		require.NotNil(t, other, "no active row read back for the recruit in treeA")
		assert.Nil(t, other.SponsorID, "sponsor read back on treeA's recruit after a removal in treeB")

		moved, err := s.GetNode(ctx, treeB, recruit)
		require.NoError(t, err)
		require.NotNil(t, moved, "no active row read back for the recruit in treeB")
		require.NotNil(t, moved.SponsorID, "no sponsor read back on treeB's recruit")
		assert.Equal(t, rootUser, *moved.SponsorID)
	})

	t.Run("an uppercase tree id reads the same tree", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		tree := testTreeUUID(1)
		upper := strings.ToUpper(tree)
		root, child := hexUser(1), hexUser(2)

		require.NoError(t, s.InsertNode(ctx, makeUUIDNode(testNodeUUID(1), tree, root, 0, nil, nil, nil)))
		require.NoError(t, s.InsertNode(ctx,
			makeUUIDNode(testNodeUUID(2), tree, child, 1, ptr(root), ptr(root), intPtr(0))))

		got, err := s.GetNode(ctx, upper, child)
		require.NoError(t, err)
		require.NotNil(t, got, "GetNode found no row under tree %s", upper)
		assert.Equal(t, tree, got.TreeID)

		all, err := s.GetByTree(ctx, upper)
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{root, child}, nodeUserIDs(all))

		ordered, err := s.GetByTreeDepthOrdered(ctx, upper)
		require.NoError(t, err)
		assert.Equal(t, []string{root, child}, nodeUserIDs(ordered))

		children, err := s.GetChildren(ctx, upper, root)
		require.NoError(t, err)
		assert.Equal(t, []string{child}, nodeUserIDs(children))
	})

	t.Run("an uppercase tree id cannot add a second root", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		tree := testTreeUUID(1)

		require.NoError(t, s.InsertNode(ctx,
			makeUUIDNode(testNodeUUID(1), tree, hexUser(1), 0, nil, nil, nil)))

		err := s.InsertNode(ctx,
			makeUUIDNode(testNodeUUID(2), strings.ToUpper(tree), hexUser(2), 0, nil, nil, nil))
		assert.ErrorIs(t, err, ErrRootConflict)
	})

	t.Run("an uppercase user or parent id reads the same row", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		tree := testTreeUUID(1)
		root, child := hexUser(1), hexUser(2)

		require.NoError(t, s.InsertNode(ctx, makeUUIDNode(testNodeUUID(1), tree, root, 0, nil, nil, nil)))
		require.NoError(t, s.InsertNode(ctx,
			makeUUIDNode(testNodeUUID(2), tree, child, 1, ptr(root), ptr(root), intPtr(0))))

		got, err := s.GetNode(ctx, tree, strings.ToUpper(child))
		require.NoError(t, err)
		require.NotNil(t, got, "GetNode found no row for user %s", strings.ToUpper(child))
		assert.Equal(t, child, got.UserID)

		children, err := s.GetChildren(ctx, tree, strings.ToUpper(root))
		require.NoError(t, err)
		assert.Equal(t, []string{child}, nodeUserIDs(children))
	})

	t.Run("an uppercase id deletes the same row and reads its tombstone", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		tree := testTreeUUID(1)
		root, child := hexUser(1), hexUser(2)
		up := strings.ToUpper

		require.NoError(t, s.InsertNode(ctx, makeUUIDNode(testNodeUUID(1), tree, root, 0, nil, nil, nil)))
		require.NoError(t, s.InsertNode(ctx,
			makeUUIDNode(testNodeUUID(2), tree, child, 1, ptr(root), ptr(root), intPtr(0))))

		require.NoError(t, s.DeleteNode(ctx, up(tree), up(child)))

		active, err := s.GetNode(ctx, tree, child)
		require.NoError(t, err)
		assert.Nil(t, active, "DeleteNode under the uppercase ids left the row active")

		tombstone, err := s.GetNodeIncludingRemoved(ctx, up(tree), up(child))
		require.NoError(t, err)
		require.NotNil(t, tombstone, "GetNodeIncludingRemoved found no row under the uppercase ids")
		assert.NotNil(t, tombstone.RemovedAt)
	})

	t.Run("an uppercase id removes and re-sponsors the same rows", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		tree := testTreeUUID(1)
		root, removed, moved := hexUser(1), hexUser(2), hexUser(3)
		event := testNodeUUID(9)
		up := strings.ToUpper

		require.NoError(t, s.InsertNode(ctx, makeUUIDNode(testNodeUUID(1), tree, root, 0, nil, nil, nil)))
		require.NoError(t, s.InsertNode(ctx,
			makeUUIDNode(testNodeUUID(2), tree, removed, 1, ptr(root), ptr(root), intPtr(0))))
		require.NoError(t, s.InsertNode(ctx,
			makeUUIDNode(testNodeUUID(3), tree, moved, 2, ptr(removed), ptr(removed), intPtr(0))))

		require.NoError(t, s.DeleteNodeAndResponsor(ctx, up(tree), up(removed), event,
			[]Responsored{{UserID: up(moved), NewSponsorID: up(root)}}))

		byEvent, err := s.GetNodeByRemovalEvent(ctx, up(tree), event)
		require.NoError(t, err)
		require.NotNil(t, byEvent, "GetNodeByRemovalEvent found no row under tree %s", up(tree))
		assert.Equal(t, removed, byEvent.UserID)

		recruit, err := s.GetNode(ctx, tree, moved)
		require.NoError(t, err)
		require.NotNil(t, recruit)
		require.NotNil(t, recruit.SponsorID)
		assert.Equal(t, root, *recruit.SponsorID)
	})

	t.Run("an uppercase row id is refused as a duplicate", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		require.NoError(t, s.InsertNode(ctx,
			makeUUIDNode(testNodeUUID(1), testTreeUUID(1), hexUser(1), 0, nil, nil, nil)))

		err := s.InsertNode(ctx,
			makeUUIDNode(strings.ToUpper(testNodeUUID(1)), testTreeUUID(2), hexUser(2), 0, nil, nil, nil))
		assert.ErrorIs(t, err, ErrNodeAlreadyProjected)
	})

	t.Run("an uppercase removal event id stamps the row in canonical form", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		tree := testTreeUUID(1)
		root, removed := hexUser(1), hexUser(2)
		event := testNodeUUID(9)

		require.NoError(t, s.InsertNode(ctx, makeUUIDNode(testNodeUUID(1), tree, root, 0, nil, nil, nil)))
		require.NoError(t, s.InsertNode(ctx,
			makeUUIDNode(testNodeUUID(2), tree, removed, 1, ptr(root), ptr(root), intPtr(0))))

		require.NoError(t, s.DeleteNodeAndResponsor(ctx, tree, removed, strings.ToUpper(event), nil))

		byEvent, err := s.GetNodeByRemovalEvent(ctx, tree, event)
		require.NoError(t, err)
		require.NotNil(t, byEvent, "GetNodeByRemovalEvent found no row stamped with %s", event)
		require.NotNil(t, byEvent.RemovedByEventID)
		assert.Equal(t, event, *byEvent.RemovedByEventID)

		upper, err := s.GetNodeByRemovalEvent(ctx, tree, strings.ToUpper(event))
		require.NoError(t, err)
		require.NotNil(t, upper, "GetNodeByRemovalEvent found no row for %s", strings.ToUpper(event))
		assert.Equal(t, removed, upper.UserID)
	})

	t.Run("ids written in uppercase read back in canonical form", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		tree := testTreeUUID(1)
		root, child := hexUser(1), hexUser(2)
		up := strings.ToUpper

		require.NoError(t, s.InsertNode(ctx, makeUUIDNode(testNodeUUID(1), up(tree), up(root), 0, nil, nil, nil)))
		require.NoError(t, s.InsertNode(ctx,
			makeUUIDNode(testNodeUUID(2), up(tree), up(child), 1, ptr(up(root)), ptr(up(root)), intPtr(0))))

		got, err := s.GetNode(ctx, tree, child)
		require.NoError(t, err)
		require.NotNil(t, got, "GetNode found no row under the canonical ids")
		assert.Equal(t, tree, got.TreeID)
		assert.Equal(t, child, got.UserID)
		require.NotNil(t, got.ParentID)
		assert.Equal(t, root, *got.ParentID)
		require.NotNil(t, got.SponsorID)
		assert.Equal(t, root, *got.SponsorID)
	})
}
