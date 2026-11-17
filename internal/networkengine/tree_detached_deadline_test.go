package networkengine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mlmforge/mlmforge/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// requireFlatDeadline requires a recorded time-to-deadline of storeWriteTimeout, less at most one second.
func requireFlatDeadline(t *testing.T, left time.Duration, ok bool, site string) {
	t.Helper()
	require.True(t, ok, "%s got a context with no deadline", site)
	assert.LessOrEqual(t, left, storeWriteTimeout, site)
	assert.Greater(t, left, storeWriteTimeout-time.Second, site)
}

// deadlines records the time left on each context that carries a deadline.
type deadlines struct {
	left []time.Duration
}

func (d *deadlines) record(ctx context.Context) {
	if dl, ok := ctx.Deadline(); ok {
		d.left = append(d.left, time.Until(dl))
	}
}

// requireOneFlatDeadline requires exactly one recorded deadline, of storeWriteTimeout less at most one second.
func (d *deadlines) requireOneFlatDeadline(t *testing.T, site string) {
	t.Helper()
	require.Len(t, d.left, 1, "%s calls made with a deadline", site)
	requireFlatDeadline(t, d.left[0], true, site)
}

type undoDeadlineStore struct {
	*MemoryTreeStore
	deadlines
}

func (s *undoDeadlineStore) UndoRootProjection(ctx context.Context, treeID, userID string, eventVersion int64) error {
	s.record(ctx)
	return s.MemoryTreeStore.UndoRootProjection(ctx, treeID, userID, eventVersion)
}

func TestDetachedDeadline_UndoRootProjection(t *testing.T) {
	tr := &reconcileTransport{
		mutationErr: &EngineError{Code: engineCodeRootAlreadyExists},
		position: &EnginePosition{
			UserID: posUser, Depth: 0,
			EnrolledAt: posEnrolled.Add(72 * time.Hour).Unix(),
		},
	}
	store := &undoDeadlineStore{MemoryTreeStore: NewMemoryTreeStore()}
	c := NewTreeEventConsumer(store, newEngineClientWithTransport(tr))
	c.retryDelay = 0

	err := deliver(context.Background(), c, makeEvent(EventTypeRootAdded, rootPayload()))

	require.Error(t, err)
	store.requireOneFlatDeadline(t, "UndoRootProjection")
}

type readBackDeadlineStore struct {
	*deleteRecordingStore
	deadlines
}

func (s *readBackDeadlineStore) GetNodeIncludingRemoved(ctx context.Context, treeID, userID string) (*TreeNodeRow, error) {
	s.record(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.deleteRecordingStore.GetNodeIncludingRemoved(ctx, treeID, userID)
}

func TestDetachedDeadline_RootReadBack(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tr := &reconcileTransport{
		mutationErr: &EngineError{Code: engineCodeRootAlreadyExists},
		onMutation:  cancel,
	}
	store := &readBackDeadlineStore{deleteRecordingStore: &deleteRecordingStore{MemoryTreeStore: NewMemoryTreeStore()}}
	c := NewTreeEventConsumer(store, newEngineClientWithTransport(tr))
	c.retryDelay = time.Minute

	err := deliver(ctx, c, makeEvent(EventTypeRootAdded, rootPayload()))

	require.ErrorIs(t, err, context.Canceled)
	store.requireOneFlatDeadline(t, "GetNodeIncludingRemoved")
}

type projectedVersionDeadlineStore struct {
	TreeStore
	deadlines
}

func (s *projectedVersionDeadlineStore) ProjectedVersion(ctx context.Context, treeID string) (int64, bool, error) {
	s.record(ctx)
	return s.TreeStore.ProjectedVersion(ctx, treeID)
}

func TestDetachedDeadline_ObserveProjectedVersion(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := &projectedVersionDeadlineStore{TreeStore: env.store}
	w := NewTreeWriter(env.events, cancellingInsertStore{TreeStore: store, user: writerChild, cancel: cancel},
		env.snapshots, newFakeWriterEngine(), env.locker)

	res, err := w.Place(ctx, placeRequest(writerChild, nil))

	require.NoError(t, err)
	require.Error(t, res.ProjectionErr)
	store.requireOneFlatDeadline(t, "ProjectedVersion")
}

type readStreamDeadlineEvents struct {
	*scriptedEvents
	deadlines
}

func (s *readStreamDeadlineEvents) ReadStream(ctx context.Context, stream string, from, limit int64) ([]platform.Event, error) {
	s.record(ctx)
	return s.scriptedEvents.ReadStream(ctx, stream, from, limit)
}

func TestDetachedDeadline_AppendOutcomeRead(t *testing.T) {
	env := newWriterEnv()
	scripted := &scriptedEvents{MemoryEventStore: platform.NewMemoryEventStore()}
	events := &readStreamDeadlineEvents{scriptedEvents: scripted}
	env.events = events
	mustAddRoot(t, env, treeTypeUnilevel)
	scripted.appendErr, scripted.readErr = errors.New("connection reset by peer"), errors.New("read timed out")
	w, _ := env.writer()

	_, err := w.Place(context.Background(), placeRequest(writerChild, nil))

	var unknown *AppendOutcomeUnknownError
	require.ErrorAs(t, err, &unknown)
	events.requireOneFlatDeadline(t, "ReadStream")
}

type recordAttemptDeadlineStore struct {
	SnapshotStore
	deadlines
}

func (s *recordAttemptDeadlineStore) RecordAttempt(ctx context.Context, a SnapshotAttempt) error {
	s.record(ctx)
	return s.SnapshotStore.RecordAttempt(ctx, a)
}

func TestDetachedDeadline_RecordAttempt(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	placeUnderRoot(t, env, 10, snapshotInterval-3)
	snapshots := &recordAttemptDeadlineStore{SnapshotStore: env.snapshots}

	backoffWrites(t, env, snapshots, newFakeWriterEngine, snapshotInterval-1, snapshotInterval)

	snapshots.requireOneFlatDeadline(t, "RecordAttempt")
}
