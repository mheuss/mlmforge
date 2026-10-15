package networkengine

import (
	"context"
	"sync"
	"testing"

	"github.com/mlmforge/mlmforge/internal/platform"
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

func TestTreeWriter_RefusesAnEmptyStreamWhenTheStoreHasAVersion(t *testing.T) {
	env := newWriterEnv()
	require.NoError(t, env.store.ProjectInsert(context.Background(), TreeNodeRow{
		ID: testNodeUUID(1), TreeID: writerTree, UserID: writerRoot, SponsorID: &writerRoot,
		EnrolledAt: writeTime,
	}, 1))
	w, _ := env.writer()

	_, err := w.AddRoot(context.Background(), unilevelRootRequest())

	var moved *StreamMovedError
	require.ErrorAs(t, err, &moved)
	assert.Equal(t, StreamMovedError{TreeID: writerTree, LoadedVersion: 1, LastVersion: 0}, *moved)
	assert.Empty(t, streamEvents(t, env.events, TreeStreamName(writerTree)))
}

func TestTreeWriter_RefusesAStreamTwoEventsPastTheLoad(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	appendDirect(t, env.events, EventTypeNodePlaced, childPlacedPayload(writerChild))
	appendDirect(t, env.events, EventTypeNodePlaced, childPlacedPayload(writerOther))
	w, _ := env.writer()

	res, err := w.Place(context.Background(), placeRequest(testUserUUID(4), nil))

	var moved *StreamMovedError
	require.ErrorAs(t, err, &moved)
	assert.Equal(t, StreamMovedError{TreeID: writerTree, LoadedVersion: 1, LastVersion: 3}, *moved)
	assert.EqualError(t, err, "tree "+writerTree+" had projected version 1 before its load, and stream "+
		TreeStreamName(writerTree)+" ends at version 3; nothing was appended")
	assert.Len(t, streamEvents(t, env.events, TreeStreamName(writerTree)), 3)
	assert.Nil(t, res.CaughtUp, "the refused write redelivered an event")
	version, _, err := env.store.ProjectedVersion(context.Background(), writerTree)
	require.NoError(t, err)
	assert.Equal(t, int64(1), version)
	for _, user := range []string{writerChild, writerOther} {
		row, err := env.store.GetNode(context.Background(), writerTree, user)
		require.NoError(t, err)
		assert.Nil(t, row, "the refused write projected %s", user)
	}
}

// projectingLoadStore records its load reads, and runs meanwhile once, either
// before or after its first read.
type projectingLoadStore struct {
	TreeStore
	meanwhile func()
	// before runs meanwhile ahead of the read instead of after it.
	before bool
	once   sync.Once
	calls  []string
}

func (s *projectingLoadStore) ProjectedRows(ctx context.Context, treeID string) (int64, bool, []TreeNodeRow, error) {
	s.calls = append(s.calls, "ProjectedRows")
	if s.before {
		s.once.Do(s.meanwhile)
	}
	version, found, rows, err := s.TreeStore.ProjectedRows(ctx, treeID)
	if !s.before {
		s.once.Do(s.meanwhile)
	}
	return version, found, rows, err
}

// runLateProjection places writerOther while the projection of a pending
// placement lands before or after the load's read.
func runLateProjection(t *testing.T, before bool) (*writerEnv, *projectingLoadStore, *fakeWriterEngine, platform.Event, WriteResult) {
	t.Helper()
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	pending := appendDirect(t, env.events, EventTypeNodePlaced, childPlacedPayload(writerChild))
	store := &projectingLoadStore{TreeStore: env.store, before: before, meanwhile: func() {
		require.NoError(t, env.store.ProjectInsert(context.Background(), TreeNodeRow{
			ID: pending.ID, TreeID: writerTree, UserID: writerChild,
			ParentID: &writerRoot, SponsorID: &writerRoot, Depth: 1, EnrolledAt: writeTime,
		}, pending.Version))
	}}
	engine := newFakeWriterEngine()
	w := NewTreeWriter(env.events, store, env.snapshots, engine, env.locker)
	res, err := w.Place(context.Background(), placeRequest(writerOther, nil))
	require.NoError(t, err)
	require.NoError(t, res.ProjectionErr)
	return env, store, engine, pending, res
}

func TestTreeWriter_ConvergesWhenTheLastEventProjectsAfterTheLoadRead(t *testing.T) {
	env, store, engine, pending, res := runLateProjection(t, false)

	assert.Equal(t, []string{"ProjectedRows"}, store.calls)
	assert.Equal(t, &CaughtUpEvent{EventID: pending.ID, Version: 2, Type: EventTypeNodePlaced}, res.CaughtUp)
	assert.Equal(t, []string{writerChild, writerOther}, placementUsers(engine), "only the redelivery placed the child")
	assert.Equal(t, int64(3), res.Version)
	version, _, err := env.store.ProjectedVersion(context.Background(), writerTree)
	require.NoError(t, err)
	assert.Equal(t, int64(3), version)
}

func TestTreeWriter_ConvergesWhenTheLastEventProjectsBeforeTheLoadRead(t *testing.T) {
	env, store, engine, pending, res := runLateProjection(t, true)

	assert.Equal(t, []string{"ProjectedRows"}, store.calls)
	assert.Equal(t, &CaughtUpEvent{EventID: pending.ID, Version: 2, Type: EventTypeNodePlaced}, res.CaughtUp)
	assert.Equal(t, []string{writerChild, writerChild, writerOther}, placementUsers(engine),
		"the replay placed the child, then the redelivery asked to place it again")
	down, err := engine.GetDownline(context.Background(), writerTree, writerRoot, 0)
	require.NoError(t, err)
	var held []string
	for _, n := range down {
		held = append(held, n.UserID)
	}
	assert.ElementsMatch(t, []string{writerChild, writerOther}, held, "the engine holds the child once")
	assert.Equal(t, int64(3), res.Version)
	version, _, err := env.store.ProjectedVersion(context.Background(), writerTree)
	require.NoError(t, err)
	assert.Equal(t, int64(3), version)
}
