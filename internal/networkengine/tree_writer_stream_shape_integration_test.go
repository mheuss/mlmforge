package networkengine

import (
	"context"
	"testing"
	"time"

	"github.com/mlmforge/mlmforge/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// refuseThenReject expects next to be refused behind foreign, rejects
// foreign, and expects next and a load to succeed.
func refuseThenReject(t *testing.T, it *writerIntegration, foreign platform.Event, next PlaceRequest, load LoadRequest) {
	t.Helper()
	ctx := context.Background()

	_, refusedErr := it.writer(t).Place(ctx, next)

	var caught *CatchUpFailedError
	require.ErrorAs(t, refusedErr, &caught)
	assert.Equal(t, foreign.ID, caught.EventID)
	require.ErrorIs(t, refusedErr, ErrUnprojectableEvent)
	last, err := it.events.ReadLastEvent(ctx, TreeStreamName(next.TreeID))
	require.NoError(t, err)
	assert.Equal(t, foreign.Version, last.Version, "the refused write appended an event")
	projected, _, err := it.store.ProjectedVersion(ctx, next.TreeID)
	require.NoError(t, err)
	assert.Equal(t, foreign.Version-1, projected, "the refused event moved the projected version")

	rejected, err := it.writer(t).Reject(ctx, RejectRequest{TreeID: next.TreeID, EventID: foreign.ID, Reason: "tree type disagrees with the stream"})
	require.NoError(t, err)
	assert.Equal(t, RejectOutcomeRejected, rejected.Outcome)
	assert.ErrorIs(t, rejected.RetryErr, ErrUnprojectableEvent)
	assert.NoError(t, rejected.ProjectionErr)

	placed, err := it.writer(t).Place(ctx, next)
	require.NoError(t, err)
	require.NoError(t, placed.ProjectionErr)
	_, err = it.writer(t).Load(ctx, load)
	require.NoError(t, err)
}

// placeUnilevelChild adds a unilevel root and places child under it.
func placeUnilevelChild(t *testing.T, it *writerIntegration, tree, root, child string) {
	t.Helper()
	it.addUnilevelRoot(t, tree, root)
	placed, err := it.writer(t).Place(context.Background(), PlaceRequest{
		TreeID: tree, UserID: child, ParentID: root, SponsorID: root, EnrolledAt: writeTime.Add(time.Hour)})
	require.NoError(t, err)
	require.NoError(t, placed.ProjectionErr)
}

func TestTreeWriter_AForeignRemovalLabelledBinaryOnAUnilevelStreamIsRefusedAndRejectEventClearsIt(t *testing.T) {
	it := newWriterIntegration(t)
	tree, root, a, x := testTreeUUID(9420), testUserUUID(1), testUserUUID(2), testUserUUID(7)
	placeUnilevelChild(t, it, tree, root, a)
	foreign := appendForeign(t, it, tree, EventTypeNodeRemoved, NodeRemovedPayload{
		TreeID: tree, UserID: a, RemovedAt: writeTime.Add(2 * time.Hour), TreeType: treeTypeBinary})

	refuseThenReject(t, it, foreign,
		PlaceRequest{TreeID: tree, UserID: x, ParentID: root, SponsorID: root, EnrolledAt: writeTime.Add(3 * time.Hour)},
		LoadRequest{TreeID: tree, TreeType: treeTypeUnilevel})

	stamped, err := it.store.GetNodeByRemovalEvent(context.Background(), tree, foreign.ID)
	require.NoError(t, err)
	assert.Nil(t, stamped, "the refused removal stamped a row")
	assert.Equal(t, map[string]string{root: root, a: root, x: root}, activeSponsors(t, it.store, tree))
}

func TestTreeWriter_AForeignRemovalLabelledMatrixOnAUnilevelStreamIsRefusedAndRejectEventClearsIt(t *testing.T) {
	it := newWriterIntegration(t)
	tree, root, a, x := testTreeUUID(9421), testUserUUID(1), testUserUUID(2), testUserUUID(7)
	placeUnilevelChild(t, it, tree, root, a)
	foreign := appendForeign(t, it, tree, EventTypeNodeRemoved, NodeRemovedPayload{
		TreeID: tree, UserID: a, RemovedAt: writeTime.Add(2 * time.Hour), TreeType: treeTypeMatrix,
		PruningMode: PruningPromoteEarliest})

	refuseThenReject(t, it, foreign,
		PlaceRequest{TreeID: tree, UserID: x, ParentID: root, SponsorID: root, EnrolledAt: writeTime.Add(3 * time.Hour)},
		LoadRequest{TreeID: tree, TreeType: treeTypeUnilevel})

	stamped, err := it.store.GetNodeByRemovalEvent(context.Background(), tree, foreign.ID)
	require.NoError(t, err)
	assert.Nil(t, stamped, "the refused removal stamped a row")
	assert.Equal(t, map[string]string{root: root, a: root, x: root}, activeSponsors(t, it.store, tree))
}

func TestTreeWriter_AForeignRemovalWithNoTreeTypeOnAMatrixStreamIsRefusedAndRejectEventClearsIt(t *testing.T) {
	it := newWriterIntegration(t)
	ctx := context.Background()
	tree, root, a, x := testTreeUUID(9422), testUserUUID(1), testUserUUID(2), testUserUUID(7)
	width, spillover := 2, "breadth_first"
	added, err := it.writer(t).AddRoot(ctx, AddRootRequest{TreeID: tree, UserID: root, SponsorID: root,
		TreeType: treeTypeMatrix, MatrixWidth: &width, MatrixSpillover: &spillover, EnrolledAt: writeTime})
	require.NoError(t, err)
	require.NoError(t, added.ProjectionErr)
	placedA, err := it.writer(t).Place(ctx, PlaceRequest{TreeID: tree, UserID: a, ParentID: root, SponsorID: root,
		Position: intPtr(0), EnrolledAt: writeTime.Add(time.Hour)})
	require.NoError(t, err)
	require.NoError(t, placedA.ProjectionErr)
	foreign := appendForeign(t, it, tree, EventTypeNodeRemoved, NodeRemovedPayload{
		TreeID: tree, UserID: a, RemovedAt: writeTime.Add(2 * time.Hour)})

	refuseThenReject(t, it, foreign,
		PlaceRequest{TreeID: tree, UserID: x, ParentID: root, SponsorID: root, Position: intPtr(1),
			EnrolledAt: writeTime.Add(3 * time.Hour)},
		LoadRequest{TreeID: tree, TreeType: treeTypeMatrix, MatrixWidth: &width, MatrixSpillover: &spillover})

	stamped, err := it.store.GetNodeByRemovalEvent(ctx, tree, foreign.ID)
	require.NoError(t, err)
	assert.Nil(t, stamped, "the refused removal stamped a row")
	assert.Equal(t, map[string]string{root: root, a: root, x: root}, activeSponsors(t, it.store, tree))
}

func TestTreeWriter_AForeignPlacementLabelledBinaryOnAUnilevelStreamIsRefusedAndRejectEventClearsIt(t *testing.T) {
	it := newWriterIntegration(t)
	ctx := context.Background()
	tree, root, x, y := testTreeUUID(9423), testUserUUID(1), testUserUUID(7), testUserUUID(8)
	it.addUnilevelRoot(t, tree, root)
	foreign := appendForeign(t, it, tree, EventTypeNodePlaced, NodePlacedPayload{
		TreeID: tree, UserID: x, ParentID: root, SponsorID: root, Position: intPtr(0), TreeType: treeTypeBinary,
		EnrolledAt: writeTime.Add(time.Hour)})

	refuseThenReject(t, it, foreign,
		PlaceRequest{TreeID: tree, UserID: y, ParentID: root, SponsorID: root, EnrolledAt: writeTime.Add(2 * time.Hour)},
		LoadRequest{TreeID: tree, TreeType: treeTypeUnilevel})

	row, err := it.store.GetNodeIncludingRemoved(ctx, tree, x)
	require.NoError(t, err)
	assert.Nil(t, row, "the refused placement left a row")
	assert.Equal(t, map[string]string{root: root, y: root}, activeSponsors(t, it.store, tree))
}

func TestTreeWriterReject_RefusesAMismatchedRemovalTheStoreAlreadyStamped(t *testing.T) {
	it := newWriterIntegration(t)
	ctx := context.Background()
	tree, root, a := testTreeUUID(9424), testUserUUID(1), testUserUUID(2)
	placeUnilevelChild(t, it, tree, root, a)
	foreign := appendForeign(t, it, tree, EventTypeNodeRemoved, NodeRemovedPayload{
		TreeID: tree, UserID: a, RemovedAt: writeTime.Add(2 * time.Hour), TreeType: treeTypeBinary})
	require.NoError(t, it.store.ProjectRemoval(ctx, tree, RemovalProjection{Users: []string{a}}, foreign.ID, foreign.Version))

	res, err := it.writer(t).Reject(ctx, RejectRequest{TreeID: tree, EventID: foreign.ID, Reason: "stamped before the check"})

	require.ErrorContains(t, err, "returned an error that is not one reject-event accepts as evidence; nothing was appended")
	assert.Empty(t, res.Outcome)
	var replay *TreeReplayError
	require.ErrorAs(t, res.RetryErr, &replay)
	assert.Equal(t, TreeReplayStageReplay, replay.Stage)
	assert.Equal(t, foreign.Version, replay.FailedVersion)
	assert.Equal(t, EventTypeNodeRemoved, replay.FailedType)
	assert.ErrorContains(t, replay.Err, "the stream records tree type unilevel")
	last, err := it.events.ReadLastEvent(ctx, TreeStreamName(tree))
	require.NoError(t, err)
	assert.Equal(t, foreign.Version, last.Version, "reject-event appended an event")
	projected, _, err := it.store.ProjectedVersion(ctx, tree)
	require.NoError(t, err)
	assert.Equal(t, foreign.Version, projected)
	stamped, err := it.store.GetNodeByRemovalEvent(ctx, tree, foreign.ID)
	require.NoError(t, err)
	require.NotNil(t, stamped, "the refusal removed the stamp")
	assert.Equal(t, a, stamped.UserID)
}
