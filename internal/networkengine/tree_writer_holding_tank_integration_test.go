package networkengine

import (
	"context"
	"testing"
	"time"

	"github.com/mlmforge/mlmforge/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tankedPlacement builds a width-2 matrix tree, removes a into the holding
// tank, and appends a foreign placement of a at version 4.
func tankedPlacement(t *testing.T, it *writerIntegration, tree, root, a string) (platform.Event, LoadRequest) {
	t.Helper()
	ctx := context.Background()
	width, spillover := 2, "breadth_first"
	added, err := it.writer(t).AddRoot(ctx, AddRootRequest{TreeID: tree, UserID: root, SponsorID: root,
		TreeType: treeTypeMatrix, MatrixWidth: &width, MatrixSpillover: &spillover, EnrolledAt: writeTime})
	require.NoError(t, err)
	require.NoError(t, added.ProjectionErr)
	placed, err := it.writer(t).Place(ctx, PlaceRequest{TreeID: tree, UserID: a, ParentID: root, SponsorID: root,
		Position: intPtr(0), EnrolledAt: writeTime.Add(time.Hour)})
	require.NoError(t, err)
	require.NoError(t, placed.ProjectionErr)
	removed, err := it.writer(t).Remove(ctx, RemoveRequest{TreeID: tree, UserID: a, RemovedAt: writeTime,
		PruningMode: PruningHoldingTank})
	require.NoError(t, err)
	require.NoError(t, removed.ProjectionErr)
	foreign := appendForeign(t, it, tree, EventTypeNodePlaced, NodePlacedPayload{TreeID: tree, UserID: a, ParentID: root,
		SponsorID: root, Position: intPtr(0), TreeType: treeTypeMatrix, EnrolledAt: writeTime.Add(2 * time.Hour)})
	require.Equal(t, int64(4), foreign.Version)
	return foreign, LoadRequest{TreeID: tree, TreeType: treeTypeMatrix, MatrixWidth: &width, MatrixSpillover: &spillover}
}

// placeAfterRejecting expects a placement of b and a load to succeed, and a to
// hold no active row.
func placeAfterRejecting(t *testing.T, it *writerIntegration, tree, root, a, b string, load LoadRequest) {
	t.Helper()
	ctx := context.Background()
	placed, err := it.writer(t).Place(ctx, PlaceRequest{TreeID: tree, UserID: b, ParentID: root, SponsorID: root,
		Position: intPtr(1), EnrolledAt: writeTime.Add(3 * time.Hour)})
	require.NoError(t, err)
	require.NoError(t, placed.ProjectionErr)
	_, err = it.writer(t).Load(ctx, load)
	require.NoError(t, err)
	row, err := it.store.GetNode(ctx, tree, a)
	require.NoError(t, err)
	assert.Nil(t, row, "the rejected placement left an active row for a")
}

func TestTreeWriterReject_ClearsATankedUsersPlacementItDeliversFirst(t *testing.T) {
	it := newWriterIntegration(t)
	ctx := context.Background()
	tree, root, a, b := testTreeUUID(9300), testUserUUID(1), testUserUUID(2), testUserUUID(3)
	foreign, load := tankedPlacement(t, it, tree, root, a)

	rejected, err := it.writer(t).Reject(ctx, RejectRequest{TreeID: tree, EventID: foreign.ID, Reason: "the user is in the holding tank"})

	require.NoError(t, err)
	assert.Equal(t, RejectOutcomeRejected, rejected.Outcome)
	var caught *CatchUpFailedError
	require.ErrorAs(t, rejected.RetryErr, &caught)
	var engineErr *EngineError
	require.ErrorAs(t, rejected.RetryErr, &engineErr)
	assert.Equal(t, "USER_IN_HOLDING_TANK", engineErr.Code)
	assert.NoError(t, rejected.ProjectionErr)
	placeAfterRejecting(t, it, tree, root, a, b, load)
}

func TestTreeWriterReject_ClearsATankedUsersPlacementAWriteDeliveredFirst(t *testing.T) {
	it := newWriterIntegration(t)
	ctx := context.Background()
	tree, root, a, b := testTreeUUID(9301), testUserUUID(1), testUserUUID(2), testUserUUID(3)
	foreign, load := tankedPlacement(t, it, tree, root, a)
	_, writeErr := it.writer(t).Place(ctx, PlaceRequest{TreeID: tree, UserID: b, ParentID: root, SponsorID: root,
		Position: intPtr(1), EnrolledAt: writeTime.Add(3 * time.Hour)})
	var engineErr *EngineError
	require.ErrorAs(t, writeErr, &engineErr)
	require.Equal(t, "USER_IN_HOLDING_TANK", engineErr.Code)
	projected, _, err := it.store.ProjectedVersion(ctx, tree)
	require.NoError(t, err)
	require.Equal(t, foreign.Version, projected, "the write did not commit the placement before the engine refused it")

	rejected, err := it.writer(t).Reject(ctx, RejectRequest{TreeID: tree, EventID: foreign.ID, Reason: "the user is in the holding tank"})

	require.NoError(t, err)
	assert.Equal(t, RejectOutcomeRejected, rejected.Outcome)
	var replay *TreeReplayError
	require.ErrorAs(t, rejected.RetryErr, &replay)
	assert.Equal(t, TreeReplayStageReplay, replay.Stage)
	assert.Equal(t, foreign.Version, replay.FailedVersion)
	assert.True(t, sameUUID(foreign.ID, replay.FailedEventID), "replay failed on event %s, not %s", replay.FailedEventID, foreign.ID)
	assert.NoError(t, rejected.ProjectionErr)
	placeAfterRejecting(t, it, tree, root, a, b, load)
}
