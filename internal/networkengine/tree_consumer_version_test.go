package networkengine

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandleRootAdded_CompensationRestoresTheProjectedVersion(t *testing.T) {
	tr := &reconcileTransport{
		mutationErr: &EngineError{Code: engineCodeUserAlreadyExists},
		position:    &EnginePosition{UserID: posUser, Depth: 3, EnrolledAt: posEnrolled.Unix()},
	}
	c, store := newRootConsumer(tr)
	event := makeEvent(EventTypeRootAdded, rootPayload())

	err := deliver(context.Background(), c, event)

	require.Error(t, err)
	assert.Nil(t, activeRow(t, store, posUser), "the row this call inserted is undone")
	version, found, verr := store.ProjectedVersion(context.Background(), consumerTree)
	require.NoError(t, verr)
	assert.True(t, found)
	assert.Equal(t, event.Version-1, version)
}

func TestHandleRootAdded_CompensationRestoresTheVersionBeforeALaterRoot(t *testing.T) {
	tr := &reconcileTransport{
		mutationErr: &EngineError{Code: engineCodeUserAlreadyExists},
		position:    &EnginePosition{UserID: posUser, Depth: 3, EnrolledAt: posEnrolled.Unix()},
	}
	c, store := newRootConsumer(tr)
	ctx := context.Background()
	earlier := "bbbbbbbb-2222-4222-8222-bbbbbbbbbbbb"
	require.NoError(t, store.ProjectInsert(ctx, TreeNodeRow{
		ID: "eeeeeeee-eeee-eeee-eeee-000000000001", TreeID: consumerTree, UserID: earlier,
		SponsorID: &earlier, EnrolledAt: posEnrolled,
	}, 1))
	require.NoError(t, store.ProjectRemoval(ctx, consumerTree, RemovalProjection{Users: []string{earlier}}, "eeeeeeee-eeee-eeee-eeee-000000000002", 2))
	event := makeEvent(EventTypeRootAdded, rootPayload())
	event.Version = 3

	err := deliver(ctx, c, event)

	require.Error(t, err)
	assert.Nil(t, activeRow(t, store, posUser), "the row this call inserted is undone")
	version, found, verr := store.ProjectedVersion(ctx, consumerTree)
	require.NoError(t, verr)
	assert.True(t, found)
	assert.Equal(t, int64(2), version)
}

// replacedAfterRemoval projects E1 placing posUser at version 1, removal
// removing them at version 2, and E2 placing them again at version 3. It
// returns E2's row ID.
func replacedAfterRemoval(t *testing.T, store TreeStore, removalID string) string {
	t.Helper()
	ctx := context.Background()
	row := func(id string) TreeNodeRow {
		return TreeNodeRow{
			ID: id, TreeID: consumerTree, UserID: posUser,
			ParentID: ptr(posParent), SponsorID: ptr(posParent), Depth: 1, EnrolledAt: posEnrolled,
		}
	}
	require.NoError(t, store.ProjectInsert(ctx, row("cafe0000-0000-4000-8000-0000000000e1"), 1))
	require.NoError(t, stampRemoval(ctx, store, consumerTree, posUser, removalID, 2))
	e2 := "cafe0000-0000-4000-8000-0000000000e2"
	require.NoError(t, store.ProjectInsert(ctx, row(e2), 3))
	return e2
}

func TestHandleNodeRemoved_RefusesARemovalBelowTheProjectedVersionBeforeTheEngine(t *testing.T) {
	tr := &reconcileTransport{mutationResponse: json.RawMessage(`{"responsored":[]}`)}
	c, store := newRemovalConsumer(tr)
	event := makeEvent(EventTypeNodeRemoved, removedPayload())
	event.Version = 2
	e2 := replacedAfterRemoval(t, store.MemoryTreeStore, event.ID)

	err := deliver(context.Background(), c, event)

	require.Empty(t, tr.mutationOps, "engine mutations sent for a removal below the projected version")
	var refused *ProjectionRefusedError
	require.ErrorAs(t, err, &refused)
	assert.Equal(t, int64(2), refused.EventVersion)
	assert.Equal(t, int64(3), refused.ProjectedVersion)
	active := activeRow(t, store.MemoryTreeStore, posUser)
	require.NotNil(t, active, "no active row for the user after the refused removal")
	assert.Equal(t, e2, active.ID, "the active row after the refused removal")
}

// The engine no longer holds the user, and the store holds E2 active behind a
// tombstone this removal stamped.
func TestHandleNodeRemoved_ARedeliveryBehindALaterPlacementDoesNotConverge(t *testing.T) {
	tr := &reconcileTransport{mutationErr: &EngineError{Code: engineCodeUserNotFound}}
	c, store := newRemovalConsumer(tr)
	event := makeEvent(EventTypeNodeRemoved, removedPayload())
	event.Version = 2
	e2 := replacedAfterRemoval(t, store.MemoryTreeStore, event.ID)

	err := deliver(context.Background(), c, event)

	var refused *ProjectionRefusedError
	require.ErrorAs(t, err, &refused)
	assert.Empty(t, tr.mutationOps, "engine mutations sent for a removal below the projected version")
	assert.Empty(t, store.responsors, "store removal writes attempted")
	active := activeRow(t, store.MemoryTreeStore, posUser)
	require.NotNil(t, active, "no active row for the user after the refused removal")
	assert.Equal(t, e2, active.ID, "the active row after the refused removal")
}

// A removal at exactly the projected version reaches the engine and converges.
func TestHandleNodeRemoved_ARedeliveryAtTheProjectedVersionReachesTheEngine(t *testing.T) {
	tr := &reconcileTransport{mutationErr: &EngineError{Code: engineCodeUserNotFound}}
	c, store := newRemovalConsumer(tr)
	ctx := context.Background()
	event := makeEvent(EventTypeNodeRemoved, removedPayload())
	event.Version = 2
	require.NoError(t, store.ProjectInsert(ctx, TreeNodeRow{
		ID: "cafe0000-0000-4000-8000-0000000000e1", TreeID: consumerTree, UserID: posUser,
		ParentID: ptr(posParent), SponsorID: ptr(posParent), Depth: 1, EnrolledAt: posEnrolled,
	}, 1))
	require.NoError(t, stampRemoval(ctx, store.MemoryTreeStore, consumerTree, posUser, event.ID, 2))

	err := deliver(ctx, c, event)

	require.NoError(t, err, "the redelivered removal at the projected version")
	assert.Equal(t, []string{"remove_node"}, tr.mutationOps, "engine mutations sent")
	assert.Empty(t, store.responsors, "store removal writes attempted")
}

// projectedVersionFailingStore fails the projected-version read and leaves
// every other method to the embedded store.
type projectedVersionFailingStore struct {
	*deleteRecordingStore
	err error
}

func (s *projectedVersionFailingStore) ProjectedVersion(context.Context, string) (int64, bool, error) {
	return 0, false, s.err
}

func TestHandleNodeRemoved_AFailedProjectedVersionReadReachesNeitherEngineNorStore(t *testing.T) {
	tr := &reconcileTransport{mutationResponse: json.RawMessage(`{"responsored":[]}`)}
	inner := &deleteRecordingStore{MemoryTreeStore: NewMemoryTreeStore()}
	seedRemovable(t, inner)
	readErr := errors.New("read failed")
	c := NewTreeEventConsumer(&projectedVersionFailingStore{deleteRecordingStore: inner, err: readErr},
		newEngineClientWithTransport(tr))
	c.retryDelay = 0

	err := deliver(context.Background(), c, makeEvent(EventTypeNodeRemoved, removedPayload()))

	require.Empty(t, tr.mutationOps, "engine mutations sent after the projected-version read failed")
	require.ErrorIs(t, err, readErr)
	assert.Empty(t, inner.responsors, "store removal writes attempted")
	assert.NotNil(t, activeRow(t, inner.MemoryTreeStore, posUser), "no active row for the user after the failed read")
}
