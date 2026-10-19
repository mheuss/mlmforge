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

// lostReplyEngine applies its first RemoveNode and then returns an error.
type lostReplyEngine struct {
	TreeRebuilder
	removes int
}

func (e *lostReplyEngine) RemoveNode(ctx context.Context, structure, userID string) ([]Responsored, error) {
	e.removes++
	moved, err := e.TreeRebuilder.RemoveNode(ctx, structure, userID)
	if e.removes == 1 && err == nil {
		return nil, errors.New("lostReplyEngine: the reply to the first RemoveNode was dropped")
	}
	return moved, err
}

func TestTreeWriter_ARemovalWhoseReplyWasLostConvergesOnTheStampAnotherWriterLeft(t *testing.T) {
	it := newWriterIntegration(t)
	ctx := context.Background()
	tree, root, a, b, c := testTreeUUID(330), testUserUUID(1), testUserUUID(2), testUserUUID(3), testUserUUID(4)
	it.buildSponsorChain(t, tree, root, a, b, c)

	var load LoadResult
	var loadErr error
	events := &lockKillingEvents{EventStore: it.events, t: t, pool: it.pool, meanwhile: func() {
		load, loadErr = it.writer(t).Load(ctx, LoadRequest{TreeID: tree, TreeType: treeTypeUnilevel})
	}}
	engine := &lostReplyEngine{TreeRebuilder: it.engine(t)}
	first, firstErr := NewTreeWriter(events, it.store, NewMemorySnapshotStore(), engine, NewPostgresTreeLocker(it.dsn)).
		Remove(ctx, RemoveRequest{TreeID: tree, UserID: b, RemovedAt: writeTime.Add(4 * time.Hour)})

	require.NoError(t, firstErr)
	require.NoError(t, loadErr)
	require.NotNil(t, load.CaughtUp, "the load reported no caught-up event")
	assert.Equal(t, first.EventID, load.CaughtUp.EventID)
	assert.Equal(t, 2, engine.removes, "RemoveNode calls on the first writer's engine")
	assert.NoError(t, first.ProjectionErr)
	assert.Equal(t, map[string]string{root: root, a: root, c: a}, activeSponsors(t, it.store, tree))
	stamped, err := it.store.GetNodeByRemovalEvent(ctx, tree, first.EventID)
	require.NoError(t, err)
	require.NotNil(t, stamped, "GetNodeByRemovalEvent returned no row for the removal event")
	assert.Equal(t, b, stamped.UserID)
}

// appendForeign appends one tree event straight to the event store after the
// stream's last event, and returns the stored copy.
func appendForeign(t *testing.T, it *writerIntegration, tree, eventType string, payload any) platform.Event {
	t.Helper()
	ctx := context.Background()
	stream := TreeStreamName(tree)
	last, err := it.events.ReadLastEvent(ctx, stream)
	require.NoError(t, err)
	require.NotNil(t, last, "stream %s is empty", stream)
	ev, err := newTreeEvent(eventType, payload)
	require.NoError(t, err)
	require.NoError(t, it.events.Append(ctx, stream, last.Version, []platform.NewEvent{ev}))
	stored, err := it.events.ReadStream(ctx, stream, last.Version+1, 1)
	require.NoError(t, err)
	require.Len(t, stored, 1)
	return stored[0]
}

func TestTreeWriter_AForeignRemovalOfAUserNeverHeldIsRefusedAndRejectEventClearsIt(t *testing.T) {
	it := newWriterIntegration(t)
	ctx := context.Background()
	tree, root, a, z, x, y := testTreeUUID(331), testUserUUID(1), testUserUUID(2), testUserUUID(9), testUserUUID(7), testUserUUID(8)
	it.addUnilevelRoot(t, tree, root)
	placedA, err := it.writer(t).Place(ctx, PlaceRequest{
		TreeID: tree, UserID: a, ParentID: root, SponsorID: root, EnrolledAt: writeTime.Add(time.Hour)})
	require.NoError(t, err)
	require.NoError(t, placedA.ProjectionErr)
	foreign := appendForeign(t, it, tree, EventTypeNodeRemoved, NodeRemovedPayload{
		TreeID: tree, UserID: z, RemovedAt: writeTime.Add(2 * time.Hour), TreeType: treeTypeUnilevel})
	placeX := PlaceRequest{TreeID: tree, UserID: x, ParentID: root, SponsorID: root, EnrolledAt: writeTime.Add(3 * time.Hour)}

	_, refusedErr := it.writer(t).Place(ctx, placeX)

	var caught *CatchUpFailedError
	require.ErrorAs(t, refusedErr, &caught)
	assert.Equal(t, foreign.ID, caught.EventID)
	assert.ErrorIs(t, refusedErr, ErrUnprojectableEvent)
	last, err := it.events.ReadLastEvent(ctx, TreeStreamName(tree))
	require.NoError(t, err)
	assert.Equal(t, foreign.Version, last.Version, "the refused write appended an event")

	rejected, err := it.writer(t).Reject(ctx, RejectRequest{TreeID: tree, EventID: foreign.ID, Reason: "removes a user the tree never held"})
	require.NoError(t, err)
	assert.Equal(t, RejectOutcomeRejected, rejected.Outcome)
	assert.NoError(t, rejected.ProjectionErr)

	placedX, err := it.writer(t).Place(ctx, placeX)
	require.NoError(t, err)
	require.NoError(t, placedX.ProjectionErr)
	placedY, err := it.writer(t).Place(ctx, PlaceRequest{
		TreeID: tree, UserID: y, ParentID: root, SponsorID: root, EnrolledAt: writeTime.Add(4 * time.Hour)})
	require.NoError(t, err, "the place of Y after the rejection returned an error")
	require.NoError(t, placedY.ProjectionErr)
	assert.Equal(t, map[string]string{root: root, a: root, x: root, y: root}, activeSponsors(t, it.store, tree))
}

func TestTreeWriter_ARemovalOfAUserWhosePlacementWasRejectedIsRefused(t *testing.T) {
	it := newWriterIntegration(t)
	ctx := context.Background()
	tree, root, b, ghost, y := testTreeUUID(332), testUserUUID(1), testUserUUID(3), testUserUUID(9), testUserUUID(8)
	width, spillover := 2, "breadth_first"
	added, err := it.writer(t).AddRoot(ctx, AddRootRequest{TreeID: tree, UserID: root, SponsorID: root,
		TreeType: treeTypeMatrix, MatrixWidth: &width, MatrixSpillover: &spillover, EnrolledAt: writeTime})
	require.NoError(t, err)
	require.NoError(t, added.ProjectionErr)
	stuck := appendForeign(t, it, tree, EventTypeNodePlaced, NodePlacedPayload{
		TreeID: tree, UserID: b, ParentID: root, SponsorID: ghost, Position: intPtr(0), TreeType: treeTypeMatrix,
		EnrolledAt: writeTime.Add(time.Hour)})
	rejected, err := it.writer(t).Reject(ctx, RejectRequest{TreeID: tree, EventID: stuck.ID, Reason: "sponsor never enrolled"})
	require.NoError(t, err)
	require.Equal(t, RejectOutcomeRejected, rejected.Outcome, "the retry returned %v", rejected.RetryErr)
	tomb, err := it.store.GetNodeIncludingRemoved(ctx, tree, b)
	require.NoError(t, err)
	require.NotNil(t, tomb, "the rejected placement left no row for %s", b)
	require.NotNil(t, tomb.RemovedAt, "the rejected placement's row for %s is active", b)
	assert.Nil(t, tomb.RemovedByEventID, "the rejection's tombstone carries a removal stamp")

	removeRes, removeErr := it.writer(t).Remove(ctx, RemoveRequest{TreeID: tree, UserID: b,
		RemovedAt: writeTime.Add(2 * time.Hour), PruningMode: PruningPromoteEarliest})
	require.Error(t, removeErr, "Remove of %s returned no error", b)
	assert.ErrorContains(t, removeErr, "check_mutation")
	assert.Empty(t, removeRes.EventID)

	foreign := appendForeign(t, it, tree, EventTypeNodeRemoved, NodeRemovedPayload{
		TreeID: tree, UserID: b, RemovedAt: writeTime.Add(2 * time.Hour), TreeType: treeTypeMatrix,
		PruningMode: PruningPromoteEarliest})
	placeY := PlaceRequest{TreeID: tree, UserID: y, ParentID: root, SponsorID: root, Position: intPtr(0),
		EnrolledAt: writeTime.Add(3 * time.Hour)}
	_, refusedErr := it.writer(t).Place(ctx, placeY)
	var caught *CatchUpFailedError
	require.ErrorAs(t, refusedErr, &caught)
	assert.Equal(t, foreign.ID, caught.EventID)
	assert.ErrorIs(t, refusedErr, ErrUnprojectableEvent)

	cleared, err := it.writer(t).Reject(ctx, RejectRequest{TreeID: tree, EventID: foreign.ID, Reason: "removes a user whose placement was rejected"})
	require.NoError(t, err)
	assert.Equal(t, RejectOutcomeRejected, cleared.Outcome)
	placedY, err := it.writer(t).Place(ctx, placeY)
	require.NoError(t, err)
	require.NoError(t, placedY.ProjectionErr)
}

func TestTreeWriter_ANilUserPlacementAnotherWriteDeliveredIsClearedByRejectEvent(t *testing.T) {
	it := newWriterIntegration(t)
	ctx := context.Background()
	tree, root, x := testTreeUUID(333), testUserUUID(1), testUserUUID(7)
	nilUser := uuid.Nil.String()
	it.addUnilevelRoot(t, tree, root)
	stuck := appendForeign(t, it, tree, EventTypeNodePlaced, NodePlacedPayload{
		TreeID: tree, UserID: nilUser, ParentID: root, SponsorID: root, TreeType: treeTypeUnilevel,
		EnrolledAt: writeTime.Add(time.Hour)})
	placeX := PlaceRequest{TreeID: tree, UserID: x, ParentID: root, SponsorID: root, EnrolledAt: writeTime.Add(2 * time.Hour)}
	_, refusedErr := it.writer(t).Place(ctx, placeX)
	require.ErrorContains(t, refusedErr, "NIL_USER_ID")
	stored, err := it.store.GetNodeIncludingRemoved(ctx, tree, nilUser)
	require.NoError(t, err)
	require.NotNil(t, stored, "the refused place projected no row for the nil user")

	rejected, err := it.writer(t).Reject(ctx, RejectRequest{TreeID: tree, EventID: stuck.ID, Reason: "nil user id"})

	require.NoError(t, err)
	require.Equal(t, RejectOutcomeRejected, rejected.Outcome, "the retry returned %v", rejected.RetryErr)
	assert.NoError(t, rejected.ProjectionErr)
	_, err = it.writer(t).Load(ctx, LoadRequest{TreeID: tree, TreeType: treeTypeUnilevel})
	require.NoError(t, err, "the load after the rejection returned an error")
	placedX, err := it.writer(t).Place(ctx, placeX)
	require.NoError(t, err)
	require.NoError(t, placedX.ProjectionErr)
	assert.Equal(t, map[string]string{root: root, x: root}, activeSponsors(t, it.store, tree))
}
