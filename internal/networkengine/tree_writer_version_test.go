package networkengine

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rootAddedPayload is writerRoot as the root of a unilevel writerTree.
func rootAddedPayload() RootAddedPayload {
	return RootAddedPayload{
		TreeID: writerTree, UserID: writerRoot, SponsorID: writerRoot,
		TreeType: treeTypeUnilevel, EnrolledAt: writeTime,
	}
}

// childPlacedPayload places user under writerRoot in a unilevel writerTree.
func childPlacedPayload(user string) NodePlacedPayload {
	return NodePlacedPayload{
		TreeID: writerTree, UserID: user, ParentID: writerRoot, SponsorID: writerRoot,
		TreeType: treeTypeUnilevel, EnrolledAt: writeTime,
	}
}

func TestTreeWriter_RefusesATreeWithNoProjectionRowPastVersion1(t *testing.T) {
	env := newWriterEnv()
	appendDirect(t, env.events, EventTypeRootAdded, rootAddedPayload())
	appendDirect(t, env.events, EventTypeNodePlaced, childPlacedPayload(writerChild))
	w, _ := env.writer()

	_, err := w.Place(context.Background(), placeRequest(writerOther, nil))

	var missing *ProjectionMissingError
	require.ErrorAs(t, err, &missing)
	assert.Equal(t, ProjectionMissingError{TreeID: writerTree, LastVersion: 2}, *missing)
	assert.EqualError(t, err, "tree "+writerTree+" has no projection row and stream "+
		TreeStreamName(writerTree)+" ends at version 2; nothing was appended")
	assert.Len(t, streamEvents(t, env.events, TreeStreamName(writerTree)), 2)
	_, found, err := env.store.ProjectedVersion(context.Background(), writerTree)
	require.NoError(t, err)
	assert.False(t, found, "the refused write left a projection row behind")
}

// projectingLoadStore records its version reads and full-tree loads, and runs
// meanwhile once, before its first full-tree load.
type projectingLoadStore struct {
	TreeStore
	meanwhile func()
	once      sync.Once
	calls     []string
}

func (s *projectingLoadStore) ProjectedVersion(ctx context.Context, treeID string) (int64, bool, error) {
	s.calls = append(s.calls, "ProjectedVersion")
	return s.TreeStore.ProjectedVersion(ctx, treeID)
}

func (s *projectingLoadStore) GetByTreeDepthOrdered(ctx context.Context, treeID string) ([]TreeNodeRow, error) {
	s.calls = append(s.calls, "GetByTreeDepthOrdered")
	s.once.Do(s.meanwhile)
	return s.TreeStore.GetByTreeDepthOrdered(ctx, treeID)
}

func TestTreeWriter_ConvergesWhenTheLastEventProjectsBetweenTheVersionReadAndTheLoad(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	pending := appendDirect(t, env.events, EventTypeNodePlaced, childPlacedPayload(writerChild))
	store := &projectingLoadStore{TreeStore: env.store, meanwhile: func() {
		require.NoError(t, env.store.ProjectInsert(context.Background(), TreeNodeRow{
			ID: pending.ID, TreeID: writerTree, UserID: writerChild,
			ParentID: &writerRoot, SponsorID: &writerRoot, Depth: 1, EnrolledAt: writeTime,
		}, pending.Version))
	}}
	w := NewTreeWriter(env.events, store, newFakeWriterEngine(), env.locker)

	res, err := w.Place(context.Background(), placeRequest(writerOther, nil))

	require.NoError(t, err)
	require.NoError(t, res.ProjectionErr)
	assert.Equal(t, []string{"ProjectedVersion", "GetByTreeDepthOrdered"}, store.calls)
	assert.Equal(t, &CaughtUpEvent{EventID: pending.ID, Version: 2, Type: EventTypeNodePlaced}, res.CaughtUp)
	assert.Equal(t, int64(3), res.Version)
	version, _, err := env.store.ProjectedVersion(context.Background(), writerTree)
	require.NoError(t, err)
	assert.Equal(t, int64(3), version)
}
