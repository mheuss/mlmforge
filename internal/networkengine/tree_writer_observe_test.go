package networkengine

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// failingRemovalStore fails every ProjectRemoval.
type failingRemovalStore struct {
	TreeStore
}

func (s failingRemovalStore) ProjectRemoval(context.Context, string, string, string, int64, []Responsored) error {
	return errors.New("failingRemovalStore: ProjectRemoval refused")
}

// cancellingInsertStore cancels the write's context, then fails the insert,
// for user's row only.
type cancellingInsertStore struct {
	TreeStore
	user   string
	cancel context.CancelFunc
}

func (s cancellingInsertStore) ProjectInsert(ctx context.Context, node TreeNodeRow, eventVersion int64) error {
	if node.UserID != s.user {
		return s.TreeStore.ProjectInsert(ctx, node, eventVersion)
	}
	s.cancel()
	return errors.New("cancellingInsertStore: ProjectInsert refused")
}

func TestTreeWriter_ObservesNothingWhenTheProjectionSucceeds(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)

	res := mustPlace(t, env, writerChild, nil)

	assert.Nil(t, res.Observed)
}

func TestTreeWriter_ObservesTheStoreCurrentAfterAnEngineFailure(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	w, engine := env.writer()
	engine.failAdd[writerChild] = errors.New("worker gone")

	res, err := w.Place(context.Background(), placeRequest(writerChild, nil))

	require.NoError(t, err)
	require.Error(t, res.ProjectionErr)
	assert.Equal(t, &ProjectionObservation{Version: 2, Found: true}, res.Observed)
}

func TestTreeWriter_ObservesTheStoreBehindAfterAStoreFailure(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	mustPlace(t, env, writerChild, nil)
	w := NewTreeWriter(env.events, failingRemovalStore{TreeStore: env.store}, newFakeWriterEngine(), env.locker)

	res, err := w.Remove(context.Background(), RemoveRequest{TreeID: writerTree, UserID: writerChild, RemovedAt: writeTime})

	require.NoError(t, err)
	require.Error(t, res.ProjectionErr)
	assert.Equal(t, int64(3), res.Version)
	assert.Equal(t, &ProjectionObservation{Version: 2, Found: true}, res.Observed)
}

func TestTreeWriter_ReportsAnObservationThatFailed(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	mustPlace(t, env, writerChild, nil)
	cause := errors.New("connection reset")
	store := &readCountingStore{TreeStore: failingRemovalStore{TreeStore: env.store}, versionErr: cause, versionErrFrom: 2}
	w := NewTreeWriter(env.events, store, newFakeWriterEngine(), env.locker)

	res, err := w.Remove(context.Background(), RemoveRequest{TreeID: writerTree, UserID: writerChild, RemovedAt: writeTime})

	require.NoError(t, err)
	require.Error(t, res.ProjectionErr)
	require.NotNil(t, res.Observed)
	assert.Equal(t, cause, res.Observed.Err)
}

func TestTreeWriter_ObservesAfterTheCallersContextEnded(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := NewTreeWriter(env.events, cancellingInsertStore{TreeStore: env.store, user: writerChild, cancel: cancel},
		newFakeWriterEngine(), env.locker)

	res, err := w.Place(ctx, placeRequest(writerChild, nil))

	require.NoError(t, err)
	require.Error(t, res.ProjectionErr)
	assert.Equal(t, &ProjectionObservation{Version: 1, Found: true}, res.Observed)
}
