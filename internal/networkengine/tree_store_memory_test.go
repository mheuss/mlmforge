package networkengine

import (
	"context"
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
		CreatedAt:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

func TestMemoryTreeStore_InsertAndGetNode(t *testing.T) {
	store := NewMemoryTreeStore()
	ctx := context.Background()

	node := makeNode("tree-1", "user-a", 0, nil, nil, nil)
	require.NoError(t, store.InsertNode(ctx, node))

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

func TestMemoryTreeStore_DeleteNode(t *testing.T) {
	store := NewMemoryTreeStore()
	ctx := context.Background()

	node := makeNode("tree-1", "user-a", 0, nil, nil, nil)
	require.NoError(t, store.InsertNode(ctx, node))
	require.NoError(t, store.DeleteNode(ctx, "tree-1", "user-a"))

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

	require.NoError(t, store.InsertNode(ctx, root))
	require.NoError(t, store.InsertNode(ctx, child1))
	require.NoError(t, store.InsertNode(ctx, child2))
	require.NoError(t, store.InsertNode(ctx, child3))

	children, err := store.GetChildren(ctx, "tree-1", "root")
	require.NoError(t, err)
	assert.Len(t, children, 3)
}

func TestMemoryTreeStore_GetByTree(t *testing.T) {
	store := NewMemoryTreeStore()
	ctx := context.Background()

	// Tree 1: 2 nodes.
	require.NoError(t, store.InsertNode(ctx, makeNode("tree-1", "a", 0, nil, nil, nil)))
	require.NoError(t, store.InsertNode(ctx, makeNode("tree-1", "b", 1, ptr("a"), ptr("a"), nil)))

	// Tree 2: 1 node.
	require.NoError(t, store.InsertNode(ctx, makeNode("tree-2", "x", 0, nil, nil, nil)))

	got, err := store.GetByTree(ctx, "tree-1")
	require.NoError(t, err)
	assert.Len(t, got, 2)
}

func TestMemoryTreeStore_GetByTreeDepthOrdered(t *testing.T) {
	store := NewMemoryTreeStore()
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

func TestMemoryTreeStore_BulkInsert(t *testing.T) {
	store := NewMemoryTreeStore()
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

func TestMemoryTreeStore_DuplicateActiveSlotRejected(t *testing.T) {
	store := NewMemoryTreeStore()
	ctx := context.Background()

	require.NoError(t, store.InsertNode(ctx, makeNode("tree-1", "u2", 1, ptr("u1"), ptr("u1"), intPtr(0))))

	err := store.InsertNode(ctx, makeNode("tree-1", "u3", 1, ptr("u1"), ptr("u1"), intPtr(0)))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate active slot")
	assert.Contains(t, err.Error(), "held by u2", "error names the incumbent")

	// Same position under a different parent stays legal.
	require.NoError(t, store.InsertNode(ctx, makeNode("tree-1", "u4", 2, ptr("u2"), ptr("u1"), intPtr(0))))

	// A soft-deleted claim does not block a live replacement (ADR-023).
	require.NoError(t, store.DeleteNode(ctx, "tree-1", "u2"))
	require.NoError(t, store.InsertNode(ctx, makeNode("tree-1", "u5", 1, ptr("u1"), ptr("u1"), intPtr(0))))

	// Two active rows with positions but nil parents do not conflict — the
	// index's parent_id is NULL for both, and Postgres treats NULLs as
	// distinct in unique indexes. The mirror must match that.
	require.NoError(t, store.InsertNode(ctx, makeNode("tree-2", "r1", 0, nil, ptr("r1"), intPtr(0))))
	require.NoError(t, store.InsertNode(ctx, makeNode("tree-2", "r2", 0, nil, ptr("r2"), intPtr(0))))
}

func TestMemoryTreeStore_DeletedNodeExcludedFromActive(t *testing.T) {
	store := NewMemoryTreeStore()
	ctx := context.Background()

	root := makeNode("tree-1", "root", 0, nil, nil, nil)
	child := makeNode("tree-1", "child", 1, ptr("root"), ptr("root"), nil)

	require.NoError(t, store.InsertNode(ctx, root))
	require.NoError(t, store.InsertNode(ctx, child))
	require.NoError(t, store.DeleteNode(ctx, "tree-1", "child"))

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
