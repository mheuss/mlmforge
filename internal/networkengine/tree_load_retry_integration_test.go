package networkengine

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// refusingMutator forwards every call. Its nth AddNode names a parent the
// engine does not hold.
type refusingMutator struct {
	*EngineClient
	refuseOn int
	calls    int
}

func (m *refusingMutator) AddNode(ctx context.Context, structure, userID, parentID, sponsorID string, enrolledAt int64, opts ...AddNodeOption) error {
	m.calls++
	if m.calls == m.refuseOn {
		parentID = testUserUUID(999)
	}
	return m.EngineClient.AddNode(ctx, structure, userID, parentID, sponsorID, enrolledAt, opts...)
}

// TestTreeLoad_RetriesInTheSameWorkerAfterADrop proves a load the engine
// refused partway can be dropped and loaded again without a new worker.
func TestTreeLoad_RetriesInTheSameWorkerAfterADrop(t *testing.T) {
	ctx := context.Background()
	client, err := NewEngineClient(ctx, findWorkerBinary(t))
	require.NoError(t, err)
	defer func() { _ = client.Stop() }()

	store := NewMemoryTreeStore()
	treeID := testTreeUUID(1)
	root := testUserUUID(1)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rows := []TreeNodeRow{
		makeUUIDNode(testNodeUUID(1), treeID, root, 0, nil, ptr(root), nil),
		makeUUIDNode(testNodeUUID(2), treeID, testUserUUID(2), 1, ptr(root), ptr(root), nil),
		makeUUIDNode(testNodeUUID(3), treeID, testUserUUID(3), 1, ptr(root), ptr(root), nil),
		makeUUIDNode(testNodeUUID(4), treeID, testUserUUID(4), 1, ptr(root), ptr(root), nil),
	}
	for i := range rows {
		rows[i].EnrolledAt = base.Add(time.Duration(i) * time.Hour)
		require.NoError(t, store.InsertNode(ctx, rows[i]))
	}

	_, err = NewTreeLoader(store, &refusingMutator{EngineClient: client, refuseOn: 2}).
		LoadTree(ctx, treeID, treeTypeUnilevel)
	var incomplete *TreeLoadIncompleteError
	require.ErrorAs(t, err, &incomplete)
	assert.Equal(t, TreeLoadStageNodes, incomplete.Stage)
	assert.Equal(t, 1, incomplete.Confirmed, "expected one child placed before the refusal")
	var refused *EngineError
	require.ErrorAs(t, err, &refused)
	assert.Equal(t, "USER_NOT_FOUND", refused.Code)

	dropped, err := client.DropStructure(ctx, treeID)
	require.NoError(t, err)
	assert.True(t, dropped, "expected DropStructure to report a structure after the refused load")

	n, err := NewTreeLoader(store, client).LoadTree(ctx, treeID, treeTypeUnilevel)
	require.NoError(t, err)
	assert.Equal(t, len(rows), n)

	downline, err := client.GetDownline(ctx, treeID, root, 0)
	require.NoError(t, err)
	assert.Len(t, downline, len(rows)-1)
}
