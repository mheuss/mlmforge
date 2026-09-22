package networkengine

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testTreeUUID generates a deterministic UUID for tree IDs.
func testTreeUUID(n int) string {
	return fmt.Sprintf("aaaaaaaa-aaaa-aaaa-aaaa-%012d", n)
}

// testUserUUID generates a deterministic UUID for user IDs.
func testUserUUID(n int) string {
	return fmt.Sprintf("00000000-0000-0000-0000-%012d", n)
}

// testNodeUUID generates a deterministic UUID for node row IDs.
func testNodeUUID(n int) string {
	return fmt.Sprintf("bbbbbbbb-bbbb-bbbb-bbbb-%012d", n)
}

// makeUUIDNode creates a TreeNodeRow with valid UUID strings for Postgres tests.
func makeUUIDNode(nodeID, treeID, userID string, depth int, parentID, sponsorID *string, position *int) TreeNodeRow {
	return TreeNodeRow{
		ID:         nodeID,
		TreeID:     treeID,
		UserID:     userID,
		ParentID:   parentID,
		SponsorID:  sponsorID,
		Position:   position,
		Depth:      depth,
		EnrolledAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

func newTestPostgresTreeStore(t *testing.T) *PostgresTreeStore {
	t.Helper()
	if pgContainer == nil {
		t.Skip("Postgres container not available")
	}
	pool := pgContainer.NewPool(t)
	return NewPostgresTreeStore(pool)
}

func TestPostgresTreeStore_InsertAndGetNode(t *testing.T) {
	store := newTestPostgresTreeStore(t)
	ctx := context.Background()

	tree1 := testTreeUUID(1)
	userA := testUserUUID(1)
	node := makeUUIDNode(testNodeUUID(1), tree1, userA, 0, nil, nil, nil)
	require.NoError(t, store.InsertNode(ctx, node))

	got, err := store.GetNode(ctx, tree1, userA)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, tree1, got.TreeID)
	assert.Equal(t, userA, got.UserID)
	assert.Equal(t, 0, got.Depth)
	assert.Nil(t, got.ParentID)
	assert.False(t, got.CreatedAt.IsZero())
}

func TestPostgresTreeStore_DeleteNodeAndResponsorMovesTheSponsor(t *testing.T) {
	store := newTestPostgresTreeStore(t)
	ctx := context.Background()

	tree1 := testTreeUUID(1)
	root := testUserUUID(1)
	recruiter := testUserUUID(2)
	recruit := testUserUUID(3)
	require.NoError(t, store.InsertNode(ctx, makeUUIDNode(testNodeUUID(1), tree1, root, 0, nil, nil, nil)))
	require.NoError(t, store.InsertNode(ctx, makeUUIDNode(testNodeUUID(2), tree1, recruiter, 1, &root, &root, nil)))
	require.NoError(t, store.InsertNode(ctx, makeUUIDNode(testNodeUUID(3), tree1, recruit, 1, &root, &recruiter, nil)))

	err := store.DeleteNodeAndResponsor(ctx, tree1, recruiter, testNodeUUID(9),
		[]Responsored{{UserID: recruit, NewSponsorID: root}})
	require.NoError(t, err)

	gone, err := store.GetNode(ctx, tree1, recruiter)
	require.NoError(t, err)
	assert.Nil(t, gone, "the removed node should no longer be active")

	moved, err := store.GetNode(ctx, tree1, recruit)
	require.NoError(t, err)
	require.NotNil(t, moved)
	require.NotNil(t, moved.SponsorID)
	assert.Equal(t, root, *moved.SponsorID)
}

func TestPostgresTreeStore_DeleteNodeAndResponsorRollsBackOnAMissingRow(t *testing.T) {
	store := newTestPostgresTreeStore(t)
	ctx := context.Background()

	tree1 := testTreeUUID(1)
	root := testUserUUID(1)
	recruiter := testUserUUID(2)
	require.NoError(t, store.InsertNode(ctx, makeUUIDNode(testNodeUUID(1), tree1, root, 0, nil, nil, nil)))
	require.NoError(t, store.InsertNode(ctx, makeUUIDNode(testNodeUUID(2), tree1, recruiter, 1, &root, &root, nil)))

	// testUserUUID(99) has no row, so the sponsor update matches nothing.
	err := store.DeleteNodeAndResponsor(ctx, tree1, recruiter, testNodeUUID(9),
		[]Responsored{{UserID: testUserUUID(99), NewSponsorID: root}})
	require.Error(t, err)

	// The soft delete must not survive a failed sponsor update.
	still, err := store.GetNode(ctx, tree1, recruiter)
	require.NoError(t, err)
	assert.NotNil(t, still, "a failed re-sponsor must roll the soft delete back")
}

func TestPostgresTreeStore_GetNodeNotFound(t *testing.T) {
	store := newTestPostgresTreeStore(t)
	ctx := context.Background()

	got, err := store.GetNode(ctx, testTreeUUID(1), testUserUUID(99))
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestPostgresTreeStore_DuplicateActiveSlotRejected(t *testing.T) {
	store := newTestPostgresTreeStore(t)
	ctx := context.Background()

	treeID := testTreeUUID(1)
	require.NoError(t, store.InsertNode(ctx,
		makeUUIDNode(testNodeUUID(1), treeID, testUserUUID(1), 0, nil, ptr(testUserUUID(1)), nil)))
	require.NoError(t, store.InsertNode(ctx,
		makeUUIDNode(testNodeUUID(2), treeID, testUserUUID(2), 1, ptr(testUserUUID(1)), ptr(testUserUUID(1)), intPtr(0))))

	err := store.InsertNode(ctx,
		makeUUIDNode(testNodeUUID(3), treeID, testUserUUID(3), 1, ptr(testUserUUID(1)), ptr(testUserUUID(1)), intPtr(0)))
	assert.ErrorIs(t, err, ErrSlotConflict)

	// Soft-delete frees the slot for a live replacement (ADR-023).
	require.NoError(t, store.DeleteNode(ctx, treeID, testUserUUID(2)))
	require.NoError(t, store.InsertNode(ctx,
		makeUUIDNode(testNodeUUID(4), treeID, testUserUUID(4), 1, ptr(testUserUUID(1)), ptr(testUserUUID(1)), intPtr(0))))

	// Two active rows with positions but NULL parents do not conflict:
	// the index treats NULLs as distinct. This pins the real database's
	// behavior so the MemoryTreeStore mirror has a reference to drift from.
	// Depth 1 is deliberate; see HEU-810.
	treeID2 := testTreeUUID(2)
	require.NoError(t, store.InsertNode(ctx,
		makeUUIDNode(testNodeUUID(5), treeID2, testUserUUID(5), 1, nil, ptr(testUserUUID(5)), intPtr(0))))
	require.NoError(t, store.InsertNode(ctx,
		makeUUIDNode(testNodeUUID(6), treeID2, testUserUUID(6), 1, nil, ptr(testUserUUID(6)), intPtr(0))))
}

func TestPostgresTreeStore_SecondRootRejected(t *testing.T) {
	store := newTestPostgresTreeStore(t)
	ctx := context.Background()

	treeID := testTreeUUID(1)
	require.NoError(t, store.InsertNode(ctx,
		makeUUIDNode(testNodeUUID(1), treeID, testUserUUID(1), 0, nil, nil, nil)))

	err := store.InsertNode(ctx,
		makeUUIDNode(testNodeUUID(2), treeID, testUserUUID(2), 0, nil, nil, nil))
	require.ErrorIs(t, err, ErrRootConflict)

	rows, gerr := store.GetByTree(ctx, treeID)
	require.NoError(t, gerr)
	roots := 0
	for _, r := range rows {
		if r.Depth == 0 {
			roots++
		}
	}
	assert.Equal(t, 1, roots, "the refused insert left no row behind")

	// A removed root does not block its replacement (ADR-023).
	require.NoError(t, store.DeleteNode(ctx, treeID, testUserUUID(1)))
	require.NoError(t, store.InsertNode(ctx,
		makeUUIDNode(testNodeUUID(3), treeID, testUserUUID(3), 0, nil, nil, nil)))
}

// The reason this constraint lives in the database rather than in a Go check.
// Two processes can both read an empty tree and both decide to insert a root.
func TestPostgresTreeStore_ConcurrentRootsLeaveOne(t *testing.T) {
	store := newTestPostgresTreeStore(t)
	ctx := context.Background()
	treeID := testTreeUUID(1)

	var started, done sync.WaitGroup
	started.Add(2)
	done.Add(2)
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		go func(i int) {
			defer done.Done()
			started.Done()
			started.Wait()
			errs[i] = store.InsertNode(ctx,
				makeUUIDNode(testNodeUUID(i+1), treeID, testUserUUID(i+1), 0, nil, nil, nil))
		}(i)
	}
	done.Wait()

	okCount := 0
	for _, err := range errs {
		if err == nil {
			okCount++
			continue
		}
		require.ErrorIs(t, err, ErrRootConflict, "the loser is refused by the root index")
	}
	assert.Equal(t, 1, okCount, "exactly one insert wins")

	// The requirement is about rows, not return values. One nil return
	// strongly implies one row and does not prove it.
	rows, gerr := store.GetByTree(ctx, treeID)
	require.NoError(t, gerr)
	roots := 0
	for _, r := range rows {
		if r.Depth == 0 {
			roots++
		}
	}
	assert.Equal(t, 1, roots, "and the tree holds exactly one active root")
}

func TestPostgresTreeStore_BulkInsertRejectsTwoRoots(t *testing.T) {
	store := newTestPostgresTreeStore(t)
	ctx := context.Background()
	treeID := testTreeUUID(1)

	err := store.BulkInsert(ctx, []TreeNodeRow{
		makeUUIDNode(testNodeUUID(1), treeID, testUserUUID(1), 0, nil, nil, nil),
		makeUUIDNode(testNodeUUID(2), treeID, testUserUUID(2), 0, nil, nil, nil),
	})
	require.ErrorIs(t, err, ErrRootConflict)

	rows, gerr := store.GetByTree(ctx, treeID)
	require.NoError(t, gerr)
	assert.Empty(t, rows, "the transaction rolled the whole batch back")
}

func TestPostgresTreeStore_DeleteNode(t *testing.T) {
	store := newTestPostgresTreeStore(t)
	ctx := context.Background()

	tree1 := testTreeUUID(1)
	userA := testUserUUID(1)
	node := makeUUIDNode(testNodeUUID(1), tree1, userA, 0, nil, nil, nil)
	require.NoError(t, store.InsertNode(ctx, node))
	require.NoError(t, store.DeleteNode(ctx, tree1, userA))

	got, err := store.GetNode(ctx, tree1, userA)
	require.NoError(t, err)
	assert.Nil(t, got, "soft-deleted node should not be returned by GetNode")
}

func TestPostgresTreeStore_GetChildren(t *testing.T) {
	store := newTestPostgresTreeStore(t)
	ctx := context.Background()

	tree1 := testTreeUUID(1)
	rootUser := testUserUUID(1)

	root := makeUUIDNode(testNodeUUID(1), tree1, rootUser, 0, nil, nil, nil)
	child1 := makeUUIDNode(testNodeUUID(2), tree1, testUserUUID(2), 1, ptr(rootUser), ptr(rootUser), intPtr(0))
	child2 := makeUUIDNode(testNodeUUID(3), tree1, testUserUUID(3), 1, ptr(rootUser), ptr(rootUser), intPtr(1))
	child3 := makeUUIDNode(testNodeUUID(4), tree1, testUserUUID(4), 1, ptr(rootUser), ptr(rootUser), intPtr(2))

	require.NoError(t, store.InsertNode(ctx, root))
	require.NoError(t, store.InsertNode(ctx, child1))
	require.NoError(t, store.InsertNode(ctx, child2))
	require.NoError(t, store.InsertNode(ctx, child3))

	children, err := store.GetChildren(ctx, tree1, rootUser)
	require.NoError(t, err)
	assert.Len(t, children, 3)
}

func TestPostgresTreeStore_GetByTree(t *testing.T) {
	store := newTestPostgresTreeStore(t)
	ctx := context.Background()

	tree1 := testTreeUUID(1)
	tree2 := testTreeUUID(2)
	userA := testUserUUID(1)
	userB := testUserUUID(2)
	userX := testUserUUID(3)

	require.NoError(t, store.InsertNode(ctx, makeUUIDNode(testNodeUUID(1), tree1, userA, 0, nil, nil, nil)))
	require.NoError(t, store.InsertNode(ctx, makeUUIDNode(testNodeUUID(2), tree1, userB, 1, ptr(userA), ptr(userA), nil)))
	require.NoError(t, store.InsertNode(ctx, makeUUIDNode(testNodeUUID(3), tree2, userX, 0, nil, nil, nil)))

	got, err := store.GetByTree(ctx, tree1)
	require.NoError(t, err)
	assert.Len(t, got, 2)
}

func TestPostgresTreeStore_GetByTreeDepthOrdered(t *testing.T) {
	store := newTestPostgresTreeStore(t)
	ctx := context.Background()

	tree1 := testTreeUUID(1)
	rootUser := testUserUUID(1)
	childUser := testUserUUID(2)
	grandchildUser := testUserUUID(3)

	// Insert out of depth order.
	require.NoError(t, store.InsertNode(ctx, makeUUIDNode(testNodeUUID(3), tree1, grandchildUser, 2, ptr(childUser), ptr(childUser), nil)))
	require.NoError(t, store.InsertNode(ctx, makeUUIDNode(testNodeUUID(1), tree1, rootUser, 0, nil, nil, nil)))
	require.NoError(t, store.InsertNode(ctx, makeUUIDNode(testNodeUUID(2), tree1, childUser, 1, ptr(rootUser), ptr(rootUser), nil)))

	got, err := store.GetByTreeDepthOrdered(ctx, tree1)
	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, 0, got[0].Depth)
	assert.Equal(t, 1, got[1].Depth)
	assert.Equal(t, 2, got[2].Depth)
}

func TestPostgresTreeStore_BulkInsert(t *testing.T) {
	store := newTestPostgresTreeStore(t)
	ctx := context.Background()

	tree1 := testTreeUUID(1)
	userA := testUserUUID(1)
	userB := testUserUUID(2)
	userC := testUserUUID(3)
	userD := testUserUUID(4)
	userE := testUserUUID(5)

	nodes := []TreeNodeRow{
		makeUUIDNode(testNodeUUID(1), tree1, userA, 0, nil, nil, nil),
		makeUUIDNode(testNodeUUID(2), tree1, userB, 1, ptr(userA), ptr(userA), nil),
		makeUUIDNode(testNodeUUID(3), tree1, userC, 1, ptr(userA), ptr(userA), nil),
		makeUUIDNode(testNodeUUID(4), tree1, userD, 2, ptr(userB), ptr(userB), nil),
		makeUUIDNode(testNodeUUID(5), tree1, userE, 2, ptr(userC), ptr(userC), nil),
	}

	require.NoError(t, store.BulkInsert(ctx, nodes))

	got, err := store.GetByTree(ctx, tree1)
	require.NoError(t, err)
	assert.Len(t, got, 5)
}

func TestPostgresTreeStore_DeletedNodeExcludedFromActive(t *testing.T) {
	store := newTestPostgresTreeStore(t)
	ctx := context.Background()

	tree1 := testTreeUUID(1)
	rootUser := testUserUUID(1)
	childUser := testUserUUID(2)

	root := makeUUIDNode(testNodeUUID(1), tree1, rootUser, 0, nil, nil, nil)
	child := makeUUIDNode(testNodeUUID(2), tree1, childUser, 1, ptr(rootUser), ptr(rootUser), nil)

	require.NoError(t, store.InsertNode(ctx, root))
	require.NoError(t, store.InsertNode(ctx, child))
	require.NoError(t, store.DeleteNode(ctx, tree1, childUser))

	byTree, err := store.GetByTree(ctx, tree1)
	require.NoError(t, err)
	assert.Len(t, byTree, 1)

	children, err := store.GetChildren(ctx, tree1, rootUser)
	require.NoError(t, err)
	assert.Len(t, children, 0)

	ordered, err := store.GetByTreeDepthOrdered(ctx, tree1)
	require.NoError(t, err)
	assert.Len(t, ordered, 1)
}

func TestPostgresTreeStore_PartialIndex(t *testing.T) {
	store := newTestPostgresTreeStore(t)
	pool := pgContainer.NewPool(t)
	ctx := context.Background()

	tree1 := testTreeUUID(1)
	userA := testUserUUID(1)

	node := makeUUIDNode(testNodeUUID(1), tree1, userA, 0, nil, nil, nil)
	require.NoError(t, store.InsertNode(ctx, node))
	require.NoError(t, store.DeleteNode(ctx, tree1, userA))

	got, err := store.GetNode(ctx, tree1, userA)
	require.NoError(t, err)
	assert.Nil(t, got)

	// Verify the row still exists in the table (soft delete, not hard delete).
	var count int
	err = pool.QueryRow(ctx,
		"SELECT count(*) FROM tree_nodes WHERE tree_id = $1 AND user_id = $2",
		tree1, userA,
	).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count, "soft-deleted row should still exist in table")
}

func TestPostgresTreeStore_BulkInsertTransaction(t *testing.T) {
	store := newTestPostgresTreeStore(t)
	pool := pgContainer.NewPool(t)
	ctx := context.Background()

	tree1 := testTreeUUID(1)
	userA := testUserUUID(1)
	userB := testUserUUID(2)

	node1 := makeUUIDNode(testNodeUUID(1), tree1, userA, 0, nil, nil, nil)
	require.NoError(t, store.InsertNode(ctx, node1))

	// Bulk insert includes a node with the same ID as node1 — should fail atomically.
	duplicate := makeUUIDNode(testNodeUUID(1), tree1, userA, 0, nil, nil, nil)
	node2 := makeUUIDNode(testNodeUUID(2), tree1, userB, 1, ptr(userA), ptr(userA), nil)

	err := store.BulkInsert(ctx, []TreeNodeRow{node2, duplicate})
	require.Error(t, err, "bulk insert with duplicate should fail")

	// Verify node2 was NOT inserted (transaction rolled back).
	var count int
	err = pool.QueryRow(ctx,
		"SELECT count(*) FROM tree_nodes WHERE user_id = $1 AND removed_at IS NULL",
		userB,
	).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 0, count, "node2 should not exist after failed bulk insert")
}

func TestPostgresTreeStore_TimestampsPopulated(t *testing.T) {
	store := newTestPostgresTreeStore(t)
	ctx := context.Background()

	tree1 := testTreeUUID(1)
	userA := testUserUUID(1)

	before := time.Now().Add(-time.Second)
	node := makeUUIDNode(testNodeUUID(1), tree1, userA, 0, nil, nil, nil)
	node.CreatedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	node.UpdatedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, store.InsertNode(ctx, node))

	got, err := store.GetNode(ctx, tree1, userA)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.True(t, got.CreatedAt.After(before), "created_at should be set by database")
	assert.True(t, got.UpdatedAt.After(before), "updated_at should be set by database")
}

func TestPostgresTreeStore_Suite(t *testing.T) {
	runTreeStoreSuite(t, func(t *testing.T) TreeStore {
		return newTestPostgresTreeStore(t)
	}, func(t *testing.T, s TreeStore, nodeID string) *string {
		var stamp *string
		err := s.(*PostgresTreeStore).pool.QueryRow(context.Background(),
			`SELECT removed_by_event_id::text FROM tree_nodes WHERE id = $1`, nodeID,
		).Scan(&stamp)
		require.NoError(t, err)
		return stamp
	})
}

// BulkInsert maps index conflicts to the same sentinels InsertNode uses. The
// skipped-row branch covers a duplicate event id; this covers the other two
// arms, which a batch reaches by claiming a slot or a user already taken.
func TestPostgresTreeStore_BulkInsert_SlotConflictIsTyped(t *testing.T) {
	store := newTestPostgresTreeStore(t)
	ctx := context.Background()

	tree := testTreeUUID(1)
	parent := testUserUUID(1)
	require.NoError(t, store.InsertNode(ctx, makeUUIDNode(testNodeUUID(1), tree, parent, 0, nil, nil, nil)))
	require.NoError(t, store.InsertNode(ctx,
		makeUUIDNode(testNodeUUID(2), tree, testUserUUID(2), 1, ptr(parent), ptr(parent), intPtr(0))))

	err := store.BulkInsert(ctx, []TreeNodeRow{
		makeUUIDNode(testNodeUUID(3), tree, testUserUUID(3), 1, ptr(parent), ptr(parent), intPtr(1)),
		makeUUIDNode(testNodeUUID(4), tree, testUserUUID(4), 1, ptr(parent), ptr(parent), intPtr(0)),
	})
	assert.ErrorIs(t, err, ErrSlotConflict)

	var count int
	require.NoError(t, store.pool.QueryRow(ctx,
		`SELECT count(*) FROM tree_nodes WHERE user_id = $1`, testUserUUID(3)).Scan(&count))
	assert.Equal(t, 0, count, "the row ahead of the conflict rolls back with it")
}

func TestPostgresTreeStore_BulkInsert_UserConflictIsTyped(t *testing.T) {
	store := newTestPostgresTreeStore(t)
	ctx := context.Background()

	tree := testTreeUUID(1)
	user := testUserUUID(1)
	require.NoError(t, store.InsertNode(ctx, makeUUIDNode(testNodeUUID(1), tree, user, 0, nil, nil, nil)))

	err := store.BulkInsert(ctx, []TreeNodeRow{
		makeUUIDNode(testNodeUUID(2), tree, user, 0, nil, nil, nil),
	})
	assert.ErrorIs(t, err, ErrActiveUserConflict)
}
