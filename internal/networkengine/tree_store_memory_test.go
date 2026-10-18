package networkengine

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ptr(s string) *string { return &s }
func intPtr(i int) *int    { return &i }

func makeNode(treeID, userID string, depth int, parentID, sponsorID *string, position *int) TreeNodeRow {
	return TreeNodeRow{
		ID:         "id-" + userID,
		TreeID:     treeID,
		UserID:     userID,
		ParentID:   parentID,
		SponsorID:  sponsorID,
		Position:   position,
		Depth:      depth,
		EnrolledAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

func TestMemoryTreeStore_InsertAndGetNode(t *testing.T) {
	store := NewMemoryTreeStore()
	ctx := context.Background()

	node := makeNode("tree-1", "user-a", 0, nil, nil, nil)
	require.NoError(t, store.insertNode(ctx, node))

	got, err := store.GetNode(ctx, "tree-1", "user-a")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "id-user-a", got.ID)
	assert.Equal(t, "tree-1", got.TreeID)
	assert.Equal(t, "user-a", got.UserID)
	assert.Equal(t, 0, got.Depth)
	assert.Nil(t, got.ParentID)
}

func TestMemoryTreeStore_GetNodeNotFound(t *testing.T) {
	store := NewMemoryTreeStore()
	ctx := context.Background()

	got, err := store.GetNode(ctx, "tree-1", "nonexistent")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestMemoryTreeStore_GetNodeSkipsATombstone(t *testing.T) {
	store := NewMemoryTreeStore()
	ctx := context.Background()

	node := makeNode("tree-1", "user-a", 0, nil, nil, nil)
	require.NoError(t, store.insertNode(ctx, node))
	require.NoError(t, store.deleteNode(ctx, "tree-1", "user-a"))

	got, err := store.GetNode(ctx, "tree-1", "user-a")
	require.NoError(t, err)
	assert.Nil(t, got, "soft-deleted node should not be returned by GetNode")
}

func TestMemoryTreeStore_GetChildren(t *testing.T) {
	store := NewMemoryTreeStore()
	ctx := context.Background()

	root := makeNode("tree-1", "root", 0, nil, nil, nil)
	child1 := makeNode("tree-1", "child-1", 1, ptr("root"), ptr("root"), intPtr(0))
	child2 := makeNode("tree-1", "child-2", 1, ptr("root"), ptr("root"), intPtr(1))
	child3 := makeNode("tree-1", "child-3", 1, ptr("root"), ptr("root"), intPtr(2))

	require.NoError(t, store.insertNode(ctx, root))
	require.NoError(t, store.insertNode(ctx, child1))
	require.NoError(t, store.insertNode(ctx, child2))
	require.NoError(t, store.insertNode(ctx, child3))

	children, err := store.GetChildren(ctx, "tree-1", "root")
	require.NoError(t, err)
	assert.Len(t, children, 3)
}

func TestMemoryTreeStore_GetByTree(t *testing.T) {
	store := NewMemoryTreeStore()
	ctx := context.Background()

	// Tree 1: 2 nodes.
	require.NoError(t, store.insertNode(ctx, makeNode("tree-1", "a", 0, nil, nil, nil)))
	require.NoError(t, store.insertNode(ctx, makeNode("tree-1", "b", 1, ptr("a"), ptr("a"), nil)))

	// Tree 2: 1 node.
	require.NoError(t, store.insertNode(ctx, makeNode("tree-2", "x", 0, nil, nil, nil)))

	got, err := store.GetByTree(ctx, "tree-1")
	require.NoError(t, err)
	assert.Len(t, got, 2)
}

func TestMemoryTreeStore_GetByTreeDepthOrdered(t *testing.T) {
	store := NewMemoryTreeStore()
	ctx := context.Background()

	// Insert out of depth order.
	require.NoError(t, store.insertNode(ctx, makeNode("tree-1", "grandchild", 2, ptr("child"), ptr("child"), nil)))
	require.NoError(t, store.insertNode(ctx, makeNode("tree-1", "root", 0, nil, nil, nil)))
	require.NoError(t, store.insertNode(ctx, makeNode("tree-1", "child", 1, ptr("root"), ptr("root"), nil)))

	got, err := store.GetByTreeDepthOrdered(ctx, "tree-1")
	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, 0, got[0].Depth)
	assert.Equal(t, 1, got[1].Depth)
	assert.Equal(t, 2, got[2].Depth)
}

func TestMemoryTreeStore_DuplicateActiveSlotRejected(t *testing.T) {
	store := NewMemoryTreeStore()
	ctx := context.Background()

	require.NoError(t, store.insertNode(ctx, makeNode("tree-1", "u2", 1, ptr("u1"), ptr("u1"), intPtr(0))))

	err := store.insertNode(ctx, makeNode("tree-1", "u3", 1, ptr("u1"), ptr("u1"), intPtr(0)))
	require.ErrorIs(t, err, ErrSlotConflict)
	assert.Contains(t, err.Error(), "held by u2", "error names the incumbent")

	// Same position under a different parent stays legal.
	require.NoError(t, store.insertNode(ctx, makeNode("tree-1", "u4", 2, ptr("u2"), ptr("u1"), intPtr(0))))

	// A soft-deleted claim does not block a live replacement (ADR-023).
	require.NoError(t, store.deleteNode(ctx, "tree-1", "u2"))
	require.NoError(t, store.insertNode(ctx, makeNode("tree-1", "u5", 1, ptr("u1"), ptr("u1"), intPtr(0))))

	// Two active rows with positions but nil parents do not conflict — the
	// index's parent_id is NULL for both, and Postgres treats NULLs as
	// distinct in unique indexes. The mirror must match that. Depth 1 is
	// deliberate; see HEU-810.
	require.NoError(t, store.insertNode(ctx, makeNode("tree-2", "r1", 1, nil, ptr("r1"), intPtr(0))))
	require.NoError(t, store.insertNode(ctx, makeNode("tree-2", "r2", 1, nil, ptr("r2"), intPtr(0))))
}

func TestMemoryTreeStore_DuplicateIDRejected(t *testing.T) {
	store := NewMemoryTreeStore()
	ctx := context.Background()

	row := makeNode("tree-1", "u1", 0, nil, nil, nil)
	require.NoError(t, store.insertNode(ctx, row))

	// Same event ID replayed into a different tree and user: the pkey
	// mirror rejects it before any per-tree uniqueness is consulted.
	dup := makeNode("tree-2", "u2", 0, nil, nil, nil)
	dup.ID = row.ID
	err := store.insertNode(ctx, dup)
	assert.ErrorIs(t, err, ErrNodeAlreadyProjected)
}

func TestMemoryTreeStore_DeletedNodeExcludedFromActive(t *testing.T) {
	store := NewMemoryTreeStore()
	ctx := context.Background()

	root := makeNode("tree-1", "root", 0, nil, nil, nil)
	child := makeNode("tree-1", "child", 1, ptr("root"), ptr("root"), nil)

	require.NoError(t, store.insertNode(ctx, root))
	require.NoError(t, store.insertNode(ctx, child))
	require.NoError(t, store.deleteNode(ctx, "tree-1", "child"))

	// GetByTree should exclude deleted node.
	byTree, err := store.GetByTree(ctx, "tree-1")
	require.NoError(t, err)
	assert.Len(t, byTree, 1)

	// GetChildren should exclude deleted node.
	children, err := store.GetChildren(ctx, "tree-1", "root")
	require.NoError(t, err)
	assert.Len(t, children, 0)

	// GetByTreeDepthOrdered should exclude deleted node.
	ordered, err := store.GetByTreeDepthOrdered(ctx, "tree-1")
	require.NoError(t, err)
	assert.Len(t, ordered, 1)
}

func TestMemoryTreeStore_Suite(t *testing.T) {
	runTreeStoreSuite(t, func(t *testing.T) TreeStore {
		return NewMemoryTreeStore()
	}, func(t *testing.T, s TreeStore, nodeID string) *string {
		for _, n := range s.(*MemoryTreeStore).nodes {
			if n.ID == uuidKey(nodeID) {
				return n.RemovedByEventID
			}
		}
		require.Failf(t, "no row", "no row with id %s", nodeID)
		return nil
	}, func(t *testing.T, s TreeStore, nodeID string, at time.Time) {
		nodes := s.(*MemoryTreeStore).nodes
		for i := range nodes {
			if nodes[i].ID == uuidKey(nodeID) {
				nodes[i].UpdatedAt = at
				return
			}
		}
		require.Failf(t, "no row", "no row with id %s", nodeID)
	}, func(t *testing.T, s TreeStore, nodeID string, at time.Time) {
		nodes := s.(*MemoryTreeStore).nodes
		for i := range nodes {
			if nodes[i].ID == uuidKey(nodeID) {
				nodes[i].RemovedAt = &at
				return
			}
		}
		require.Failf(t, "no row", "no row with id %s", nodeID)
	})
}

func TestMemoryTreeStore_RefusesARelocationIntoASlotAnotherRelocationVacates(t *testing.T) {
	s := NewMemoryTreeStore()
	ctx := context.Background()
	tree, root, child, recruit, grand := testTreeUUID(1), testUserUUID(1), testUserUUID(2), testUserUUID(3), testUserUUID(4)
	require.NoError(t, s.ProjectInsert(ctx, makeUUIDNode(testNodeUUID(1), tree, root, 0, nil, ptr(root), nil), 1))
	require.NoError(t, s.ProjectInsert(ctx, makeUUIDNode(testNodeUUID(2), tree, child, 1, ptr(root), ptr(root), intPtr(0)), 2))
	require.NoError(t, s.ProjectInsert(ctx, makeUUIDNode(testNodeUUID(3), tree, recruit, 1, ptr(root), ptr(child), intPtr(1)), 3))
	require.NoError(t, s.ProjectInsert(ctx, makeUUIDNode(testNodeUUID(4), tree, grand, 2, ptr(child), ptr(child), intPtr(0)), 4))
	before, err := s.GetByTree(ctx, tree)
	require.NoError(t, err)

	err = s.ProjectRemoval(ctx, tree, RemovalProjection{Users: []string{grand},
		Relocated: []Relocation{
			{UserID: recruit, ParentID: root, Position: 2, Depth: 1},
			{UserID: child, ParentID: root, Position: 1, Depth: 1},
		}}, testNodeUUID(9), 5)

	require.ErrorIs(t, err, ErrSlotConflict)
	assert.ErrorContains(t, err, "parent="+root+" position=1")
	assert.ErrorContains(t, err, "held by "+recruit)
	after, err := s.GetByTree(ctx, tree)
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

// hexUser returns a user id whose spelling changes under strings.ToUpper.
func hexUser(n int) string { return fmt.Sprintf("0000000a-0000-0000-0000-%012d", n) }

// Postgres compares uuid columns by value, so the memory twin must too.
func TestMemoryTreeStore_ProjectRemovalMatchesRowsWhateverTheCase(t *testing.T) {
	s := NewMemoryTreeStore()
	ctx := context.Background()
	tree, root, x, a, b := testTreeUUID(1), hexUser(1), hexUser(2), hexUser(3), hexUser(4)
	require.NoError(t, s.ProjectInsert(ctx, makeUUIDNode(testNodeUUID(1), tree, root, 0, nil, ptr(root), nil), 1))
	require.NoError(t, s.ProjectInsert(ctx, makeUUIDNode(testNodeUUID(2), tree, x, 1, ptr(root), ptr(root), intPtr(0)), 2))
	require.NoError(t, s.ProjectInsert(ctx, makeUUIDNode(testNodeUUID(3), tree, a, 2, ptr(x), ptr(x), intPtr(0)), 3))
	require.NoError(t, s.ProjectInsert(ctx, makeUUIDNode(testNodeUUID(4), tree, b, 1, ptr(root), ptr(x), intPtr(1)), 4))
	up := strings.ToUpper

	err := s.ProjectRemoval(ctx, tree, RemovalProjection{Users: []string{up(x)},
		Responsored: []Responsored{{UserID: up(b), NewSponsorID: root}},
		Relocated:   []Relocation{{UserID: up(a), ParentID: root, Position: 0, Depth: 1}}}, testNodeUUID(9), 5)

	require.NoError(t, err)
	rows, err := s.GetByTree(ctx, tree)
	require.NoError(t, err)
	assert.Len(t, rows, 3)
	for _, row := range rows {
		switch row.UserID {
		case a:
			assert.Equal(t, root, *row.ParentID)
			assert.Equal(t, 0, *row.Position)
		case b:
			assert.Equal(t, root, *row.SponsorID)
		}
	}
}

func TestMemoryTreeStore_RefusesARelocationIntoAHeldSlotWhateverTheCase(t *testing.T) {
	s := NewMemoryTreeStore()
	ctx := context.Background()
	tree, root, x, a, b := testTreeUUID(1), hexUser(1), hexUser(2), hexUser(3), hexUser(4)
	require.NoError(t, s.ProjectInsert(ctx, makeUUIDNode(testNodeUUID(1), tree, root, 0, nil, ptr(root), nil), 1))
	require.NoError(t, s.ProjectInsert(ctx, makeUUIDNode(testNodeUUID(2), tree, x, 1, ptr(root), ptr(root), intPtr(0)), 2))
	require.NoError(t, s.ProjectInsert(ctx, makeUUIDNode(testNodeUUID(3), tree, b, 1, ptr(root), ptr(root), intPtr(1)), 3))
	require.NoError(t, s.ProjectInsert(ctx, makeUUIDNode(testNodeUUID(4), tree, a, 2, ptr(x), ptr(x), intPtr(0)), 4))

	err := s.ProjectRemoval(ctx, tree, RemovalProjection{Users: []string{x},
		Responsored: []Responsored{{UserID: a, NewSponsorID: root}},
		Relocated:   []Relocation{{UserID: a, ParentID: strings.ToUpper(root), Position: 1, Depth: 1}}}, testNodeUUID(9), 5)

	require.ErrorIs(t, err, ErrSlotConflict)
}

// Two relocations into one empty slot, the second spelled in upper case.
func TestMemoryTreeStore_TwoRelocationsIntoOneEmptySlotWhateverTheCase(t *testing.T) {
	s := NewMemoryTreeStore()
	ctx := context.Background()
	tree, root, x, a, c := testTreeUUID(1), hexUser(1), hexUser(2), hexUser(3), hexUser(5)
	require.NoError(t, s.ProjectInsert(ctx, makeUUIDNode(testNodeUUID(1), tree, root, 0, nil, ptr(root), nil), 1))
	require.NoError(t, s.ProjectInsert(ctx, makeUUIDNode(testNodeUUID(2), tree, x, 1, ptr(root), ptr(root), intPtr(0)), 2))
	require.NoError(t, s.ProjectInsert(ctx, makeUUIDNode(testNodeUUID(3), tree, a, 2, ptr(x), ptr(x), intPtr(0)), 3))
	require.NoError(t, s.ProjectInsert(ctx, makeUUIDNode(testNodeUUID(4), tree, c, 2, ptr(x), ptr(x), intPtr(1)), 4))

	err := s.ProjectRemoval(ctx, tree, RemovalProjection{Users: []string{x},
		Responsored: []Responsored{{UserID: a, NewSponsorID: root}, {UserID: c, NewSponsorID: root}},
		Relocated: []Relocation{{UserID: a, ParentID: root, Position: 1, Depth: 1},
			{UserID: c, ParentID: strings.ToUpper(root), Position: 1, Depth: 1}}}, testNodeUUID(9), 5)

	require.ErrorIs(t, err, ErrSlotConflict)
}

// A relocation into a slot another relocation vacates, spelled in upper case.
func TestMemoryTreeStore_RelocationIntoAVacatedSlotWhateverTheCase(t *testing.T) {
	s := NewMemoryTreeStore()
	ctx := context.Background()
	tree, root, x, a, b := testTreeUUID(1), hexUser(1), hexUser(2), hexUser(3), hexUser(4)
	require.NoError(t, s.ProjectInsert(ctx, makeUUIDNode(testNodeUUID(1), tree, root, 0, nil, ptr(root), nil), 1))
	require.NoError(t, s.ProjectInsert(ctx, makeUUIDNode(testNodeUUID(2), tree, x, 1, ptr(root), ptr(root), intPtr(0)), 2))
	require.NoError(t, s.ProjectInsert(ctx, makeUUIDNode(testNodeUUID(3), tree, b, 1, ptr(root), ptr(root), intPtr(1)), 3))
	require.NoError(t, s.ProjectInsert(ctx, makeUUIDNode(testNodeUUID(4), tree, a, 2, ptr(x), ptr(x), intPtr(0)), 4))

	err := s.ProjectRemoval(ctx, tree, RemovalProjection{Users: []string{x},
		Responsored: []Responsored{{UserID: a, NewSponsorID: root}},
		Relocated: []Relocation{{UserID: a, ParentID: strings.ToUpper(root), Position: 1, Depth: 1},
			{UserID: b, ParentID: root, Position: 0, Depth: 1}}}, testNodeUUID(9), 5)

	require.ErrorIs(t, err, ErrSlotConflict)
}
