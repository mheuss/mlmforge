package networkengine

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSnapshotStore_CarriesAMatrixHoldingTankThroughARestore(t *testing.T) {
	worker := findWorkerBinary(t)
	ctx := context.Background()
	first, err := NewEngineClient(ctx, worker)
	require.NoError(t, err)
	t.Cleanup(func() { _ = first.Stop() })
	tree, root, a, b := testTreeUUID(297), testUserUUID(1), testUserUUID(2), testUserUUID(3)
	require.NoError(t, first.CreateMatrixTree(ctx, tree, 3, "breadth_first"))
	require.NoError(t, first.AddRoot(ctx, tree, root, 100))
	require.NoError(t, first.AddNodeAt(ctx, tree, a, root, root, 0, 200))
	require.NoError(t, first.AddNodeAt(ctx, tree, b, a, a, 0, 300))
	_, err = first.RemoveMatrixNode(ctx, tree, a, "holding_tank")
	require.NoError(t, err)
	before, err := first.GetHoldingTank(ctx, tree, "")
	require.NoError(t, err)
	require.Len(t, before, 2, "the removal left the tank empty, so a round trip would prove nothing")

	taken, err := first.TakeSnapshot(ctx, tree)
	require.NoError(t, err)
	store := NewMemorySnapshotStore()
	require.NoError(t, store.Put(ctx, Snapshot{TreeID: tree, Version: 1000, TreeType: taken.TreeType, Data: taken.Data}))
	stored, found, err := store.Get(ctx, tree)
	require.NoError(t, err)
	require.True(t, found)

	second, err := NewEngineClient(ctx, worker)
	require.NoError(t, err)
	t.Cleanup(func() { _ = second.Stop() })
	require.NoError(t, second.RestoreSnapshot(ctx, tree, stored.TreeType, json.RawMessage(stored.Data)))
	after, err := second.GetHoldingTank(ctx, tree, "")
	require.NoError(t, err)

	assert.Equal(t, before, after)
}
