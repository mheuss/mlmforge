package networkengine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mlmforge/mlmforge/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// readCountingStore counts the store reads a load makes, and fails the ones a
// test names.
type readCountingStore struct {
	TreeStore
	versionReads   int
	depthReads     int
	treeReads      int
	versionErr     error
	versionErrFrom int
	treeErr        error
}

func (s *readCountingStore) ProjectedVersion(ctx context.Context, treeID string) (int64, bool, error) {
	s.versionReads++
	if s.versionErr != nil && s.versionReads >= s.versionErrFrom {
		return 0, false, s.versionErr
	}
	return s.TreeStore.ProjectedVersion(ctx, treeID)
}

func (s *readCountingStore) GetByTreeDepthOrdered(ctx context.Context, treeID string) ([]TreeNodeRow, error) {
	s.depthReads++
	return s.TreeStore.GetByTreeDepthOrdered(ctx, treeID)
}

func (s *readCountingStore) GetByTree(ctx context.Context, treeID string) ([]TreeNodeRow, error) {
	s.treeReads++
	if s.treeErr != nil {
		return nil, s.treeErr
	}
	return s.TreeStore.GetByTree(ctx, treeID)
}

func TestTreeWriter_AWriteKeepsItsProjectedVersionReadError(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	store := &readCountingStore{TreeStore: env.store, versionErr: errors.New("connection reset"), versionErrFrom: 1}
	w := NewTreeWriter(env.events, store, newFakeWriterEngine(), env.locker)

	_, err := w.Place(context.Background(), placeRequest(writerChild, nil))

	require.EqualError(t, err, "read the projected version of tree "+writerTree+
		"; nothing was appended: connection reset")
	var rejected *TreeLoadRejectedError
	assert.False(t, errors.As(err, &rejected), "a write's read failure was typed as a load rejection")
}

// projectedVersion reads writerTree's projected version from the env's store.
func projectedVersion(t *testing.T, env *writerEnv) (int64, bool) {
	t.Helper()
	version, found, err := env.store.ProjectedVersion(context.Background(), writerTree)
	require.NoError(t, err)
	return version, found
}

// loadRequest asks for writerTree as a unilevel tree.
func loadRequest() LoadRequest {
	return LoadRequest{TreeID: writerTree, TreeType: treeTypeUnilevel}
}

func TestTreeWriter_LoadsATreeWithNothingBehindWithoutRedelivering(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	mustPlace(t, env, writerChild, nil)
	store := &readCountingStore{TreeStore: env.store}
	w := NewTreeWriter(env.events, store, newFakeWriterEngine(), env.locker)

	res, err := w.Load(context.Background(), loadRequest())

	require.NoError(t, err)
	assert.Equal(t, LoadResult{Nodes: 2}, res)
	assert.Equal(t, 1, store.depthReads, "a load with nothing behind read the rows a different number of times")
	assert.Zero(t, store.treeReads, "a load with nothing behind read the rows again after loading")
}

func TestTreeWriter_LoadRedeliversTheLastEventWhenTheStoreIsOneBehind(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	pending := appendDirect(t, env.events, EventTypeNodePlaced, childPlacedPayload(writerChild))
	w, _ := env.writer()

	res, err := w.Load(context.Background(), loadRequest())

	require.NoError(t, err)
	assert.Equal(t, LoadResult{
		CaughtUp:       &CaughtUpEvent{EventID: pending.ID, Version: 2, Type: EventTypeNodePlaced},
		ProjectedAfter: &ProjectionObservation{Version: 2, Found: true},
		Nodes:          2,
	}, res)
	row, err := env.store.GetNode(context.Background(), writerTree, writerChild)
	require.NoError(t, err)
	assert.NotNil(t, row, "the redelivered placement left no row")
	version, _ := projectedVersion(t, env)
	assert.Equal(t, int64(2), version)
}

func TestTreeWriter_LoadLeavesAnEmptyStreamAsItFindsIt(t *testing.T) {
	env := newWriterEnv()
	w, _ := env.writer()

	res, err := w.Load(context.Background(), loadRequest())

	require.NoError(t, err)
	assert.Equal(t, LoadResult{}, res)
	_, found := projectedVersion(t, env)
	assert.False(t, found, "the load created a projection row")
}

func TestTreeWriter_LoadRefusesAStreamTwoPastTheProjectedVersion(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	appendDirect(t, env.events, EventTypeNodePlaced, childPlacedPayload(writerChild))
	appendDirect(t, env.events, EventTypeNodePlaced, childPlacedPayload(writerOther))
	w, _ := env.writer()

	res, err := w.Load(context.Background(), loadRequest())

	var moved *StreamMovedError
	require.ErrorAs(t, err, &moved)
	assert.Equal(t, StreamMovedError{TreeID: writerTree, LoadedVersion: 1, LastVersion: 3}, *moved)
	assert.Nil(t, res.CaughtUp)
	version, _ := projectedVersion(t, env)
	assert.Equal(t, int64(1), version)
	for _, user := range []string{writerChild, writerOther} {
		row, err := env.store.GetNode(context.Background(), writerTree, user)
		require.NoError(t, err)
		assert.Nil(t, row, "the refused load projected %s", user)
	}
}

func TestTreeWriter_LoadRefusesAStreamBehindTheProjectedVersion(t *testing.T) {
	env := newWriterEnv()
	require.NoError(t, env.store.ProjectInsert(context.Background(), TreeNodeRow{
		ID: testNodeUUID(1), TreeID: writerTree, UserID: writerRoot, SponsorID: &writerRoot,
		EnrolledAt: writeTime,
	}, 1))
	w, _ := env.writer()

	res, err := w.Load(context.Background(), loadRequest())

	var moved *StreamMovedError
	require.ErrorAs(t, err, &moved)
	assert.Equal(t, StreamMovedError{TreeID: writerTree, LoadedVersion: 1, LastVersion: 0}, *moved)
	assert.Nil(t, res.CaughtUp)
	version, _ := projectedVersion(t, env)
	assert.Equal(t, int64(1), version)
}

func TestTreeWriter_LoadRefusesATreeWithNoProjectionRowPastVersion1(t *testing.T) {
	env := newWriterEnv()
	appendDirect(t, env.events, EventTypeRootAdded, rootAddedPayload())
	appendDirect(t, env.events, EventTypeNodePlaced, childPlacedPayload(writerChild))
	w, _ := env.writer()

	res, err := w.Load(context.Background(), loadRequest())

	var missing *ProjectionMissingError
	require.ErrorAs(t, err, &missing)
	assert.Equal(t, ProjectionMissingError{TreeID: writerTree, LastVersion: 2}, *missing)
	assert.Nil(t, res.CaughtUp)
	_, found := projectedVersion(t, env)
	assert.False(t, found, "the refused load left a projection row behind")
}

func TestTreeWriter_LoadReportsARedeliveryThatFailed(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	orphan := childPlacedPayload(writerChild)
	orphan.ParentID = writerOther
	pending := appendDirect(t, env.events, EventTypeNodePlaced, orphan)
	w, _ := env.writer()

	res, err := w.Load(context.Background(), loadRequest())

	var failed *CatchUpFailedError
	require.ErrorAs(t, err, &failed)
	assert.Equal(t, pending.ID, failed.EventID)
	assert.Nil(t, res.CaughtUp)
	assert.Zero(t, res.Nodes)
}

func TestTreeWriter_LoadReportsARedeliveryThatLeftTheVersionBehind(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	pending := appendDirect(t, env.events, EventTypeNodeRemoved, NodeRemovedPayload{
		TreeID: writerTree, UserID: writerOther, RemovedAt: writeTime,
	})
	w, _ := env.writer()

	res, err := w.Load(context.Background(), loadRequest())

	require.NoError(t, err)
	assert.Equal(t, LoadResult{
		CaughtUp:       &CaughtUpEvent{EventID: pending.ID, Version: 2, Type: EventTypeNodeRemoved},
		ProjectedAfter: &ProjectionObservation{Version: 1, Found: true},
		Nodes:          1,
	}, res)
}

func TestTreeWriter_LoadReportsATreeThatStillHasNoProjectionRow(t *testing.T) {
	env := newWriterEnv()
	root := appendDirect(t, env.events, EventTypeRootAdded, rootAddedPayload())
	require.NoError(t, env.store.InsertNode(context.Background(), TreeNodeRow{
		ID: root.ID, TreeID: writerTree, UserID: writerRoot, SponsorID: &writerRoot, EnrolledAt: writeTime,
	}))
	w, _ := env.writer()

	res, err := w.Load(context.Background(), loadRequest())

	require.NoError(t, err)
	assert.Equal(t, LoadResult{
		CaughtUp:       &CaughtUpEvent{EventID: root.ID, Version: 1, Type: EventTypeRootAdded},
		ProjectedAfter: &ProjectionObservation{},
		Nodes:          1,
	}, res)
}

func TestTreeWriter_LoadKeepsAReleaseFailureAlongsideAnError(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	appendDirect(t, env.events, EventTypeNodePlaced, childPlacedPayload(writerChild))
	store := &readCountingStore{TreeStore: env.store, treeErr: errors.New("connection reset")}
	release := errors.New("pg_advisory_unlock returned false")
	w := NewTreeWriter(env.events, store, newFakeWriterEngine(), releaseFailingLocker{err: release})

	res, err := w.Load(context.Background(), loadRequest())

	require.Error(t, err)
	require.NotNil(t, res.CaughtUp)
	assert.Equal(t, release, res.ReleaseErr)
}

func TestTreeWriter_LoadReturnsTheRedeliveryWhenTheCountFails(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	pending := appendDirect(t, env.events, EventTypeNodePlaced, childPlacedPayload(writerChild))
	store := &readCountingStore{TreeStore: env.store, treeErr: errors.New("connection reset")}
	w := NewTreeWriter(env.events, store, newFakeWriterEngine(), env.locker)

	res, err := w.Load(context.Background(), loadRequest())

	require.EqualError(t, err, "read the active rows of tree "+writerTree+
		" after redelivering version 2: connection reset")
	require.NotNil(t, res.CaughtUp)
	assert.Equal(t, pending.ID, res.CaughtUp.EventID)
	var rejected *TreeLoadRejectedError
	assert.False(t, errors.As(err, &rejected), "a failure after the redelivery was typed as TreeLoadRejectedError")
}

func TestTreeWriter_LoadRefusesATreeTypeThatDiffersFromTheStream(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	w, _ := env.writer()

	_, err := w.Load(context.Background(), LoadRequest{TreeID: writerTree, TreeType: treeTypeBinary})

	require.EqualError(t, err, "load tree "+writerTree+": stream "+TreeStreamName(writerTree)+
		" records tree type unilevel at version 1, and the request names binary")
}

func TestTreeWriter_LoadMatchesMatrixParametersOnlyWhenGiven(t *testing.T) {
	width, otherWidth, spillover := 3, 4, "breadth_first"
	cases := []struct {
		name    string
		req     LoadRequest
		wantErr string
	}{
		{name: "omitted", req: LoadRequest{TreeType: treeTypeMatrix}},
		{name: "matching", req: LoadRequest{TreeType: treeTypeMatrix, MatrixWidth: &width, MatrixSpillover: &spillover}},
		{
			name: "conflicting", req: LoadRequest{TreeType: treeTypeMatrix, MatrixWidth: &otherWidth},
			wantErr: "load tree " + writerTree + ": stream " + TreeStreamName(writerTree) +
				" records matrix width 3 at version 1, and the request names 4",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newWriterEnv()
			mustAddRoot(t, env, treeTypeMatrix)
			w, engine := env.writer()
			tc.req.TreeID = writerTree

			res, err := w.Load(context.Background(), tc.req)

			if tc.wantErr != "" {
				require.EqualError(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, 1, res.Nodes)
			require.NotEmpty(t, engine.calls, "the load made no engine call")
			assert.Equal(t, "create_matrix_tree 3 breadth_first", engine.calls[0])
		})
	}
}

func TestTreeWriter_LoadWaitsForTheLockBeforeReadingTheStore(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	release, err := env.locker.Lock(context.Background(), uuid.MustParse(writerTree))
	require.NoError(t, err)
	defer func() { _ = release() }()
	store := &readCountingStore{TreeStore: env.store}
	w := NewTreeWriter(env.events, store, newFakeWriterEngine(), env.locker, WithLockWait(10*time.Millisecond))

	_, err = w.Load(context.Background(), loadRequest())

	var wait *TreeLockWaitError
	require.ErrorAs(t, err, &wait)
	assert.Zero(t, store.versionReads+store.depthReads+store.treeReads, "the load read the store without the lock")
}

func TestTreeWriter_LoadTypesAFailedProjectedVersionReadAsARejection(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	cause := errors.New("connection reset")
	store := &readCountingStore{TreeStore: env.store, versionErr: cause, versionErrFrom: 1}
	w := NewTreeWriter(env.events, store, newFakeWriterEngine(), env.locker)

	_, err := w.Load(context.Background(), loadRequest())

	var rejected *TreeLoadRejectedError
	require.ErrorAs(t, err, &rejected)
	assert.Equal(t, TreeLoadStoreReadFailed, rejected.Kind)
	assert.Equal(t, cause, rejected.Err)
	assert.Zero(t, store.depthReads, "the rows were read after the version read failed")
}

func TestTreeWriter_LoadTypesAFailedVersion1ReadAsARejection(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	cause := errors.New("connection reset")
	events := &scriptedEvents{MemoryEventStore: env.events.(*platform.MemoryEventStore), failed: true, readErr: cause}
	w := NewTreeWriter(events, env.store, newFakeWriterEngine(), env.locker)

	_, err := w.Load(context.Background(), loadRequest())

	var rejected *TreeLoadRejectedError
	require.ErrorAs(t, err, &rejected)
	assert.Equal(t, TreeLoadStoreReadFailed, rejected.Kind)
	assert.Equal(t, cause, rejected.Err)
}

func TestTreeWriter_LoadUsesTheTypeRecordedUnderTheLock(t *testing.T) {
	env := newWriterEnv()
	log := &orderLog{}
	locker := &hookLocker{inner: env.locker, before: func() {
		appendDirect(t, env.events, EventTypeRootAdded, RootAddedPayload{
			TreeID: writerTree, UserID: writerOther, SponsorID: writerOther,
			EnrolledAt: writeTime, TreeType: treeTypeBinary,
		})
	}}
	w := NewTreeWriter(env.events, &loggingStore{TreeStore: env.store, log: log, name: "w"},
		newFakeWriterEngine(), locker)

	_, err := w.Load(context.Background(), loadRequest())

	require.EqualError(t, err, "load tree "+writerTree+": stream "+TreeStreamName(writerTree)+
		" records tree type binary at version 1, and the request names unilevel")
	assert.Equal(t, -1, log.indexOf("w load"), "the refusal must come before the load: %v", log.snapshot())
}

func TestTreeWriter_LoadLeavesAFailedVersionReadAfterTheRedeliveryUntyped(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	pending := appendDirect(t, env.events, EventTypeNodePlaced, childPlacedPayload(writerChild))
	store := &readCountingStore{TreeStore: env.store, versionErr: errors.New("connection reset"), versionErrFrom: 2}
	w := NewTreeWriter(env.events, store, newFakeWriterEngine(), env.locker)

	res, err := w.Load(context.Background(), loadRequest())

	require.EqualError(t, err, "read the projected version of tree "+writerTree+
		" after redelivering version 2: connection reset")
	require.NotNil(t, res.CaughtUp)
	assert.Equal(t, pending.ID, res.CaughtUp.EventID)
	var rejected *TreeLoadRejectedError
	assert.False(t, errors.As(err, &rejected), "a failure after the redelivery was typed as TreeLoadRejectedError")
}
