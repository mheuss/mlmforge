package networkengine

import (
	"context"
	"testing"
	"time"

	"github.com/mlmforge/mlmforge/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTreeWriterReject_ClearsAForeignEventWhoseIDIsNotCanonical(t *testing.T) {
	cases := []struct {
		name      string
		tree      string
		eventType string
		payload   func(tree, root string) any
	}{
		{"a root whose user is not a UUID", testTreeUUID(9334), EventTypeRootAdded, func(tree, root string) any {
			return RootAddedPayload{TreeID: tree, UserID: "nope", SponsorID: root, TreeType: treeTypeUnilevel,
				EnrolledAt: writeTime.Add(time.Hour)}
		}},
		{"a placement whose sponsor is not a UUID", testTreeUUID(9330), EventTypeNodePlaced, func(tree, root string) any {
			return NodePlacedPayload{TreeID: tree, UserID: testUserUUID(2), ParentID: root, SponsorID: "nope",
				TreeType: treeTypeUnilevel, EnrolledAt: writeTime.Add(time.Hour)}
		}},
		{"a placement whose parent is empty", testTreeUUID(9331), EventTypeNodePlaced, func(tree, root string) any {
			return NodePlacedPayload{TreeID: tree, UserID: testUserUUID(2), ParentID: "", SponsorID: root,
				TreeType: treeTypeUnilevel, EnrolledAt: writeTime.Add(time.Hour)}
		}},
		{"a placement whose user is a urn", testTreeUUID(9332), EventTypeNodePlaced, func(tree, root string) any {
			return NodePlacedPayload{TreeID: tree, UserID: "urn:uuid:" + testUserUUID(2), ParentID: root, SponsorID: root,
				TreeType: treeTypeUnilevel, EnrolledAt: writeTime.Add(time.Hour)}
		}},
		{"a removal whose user is not a UUID", testTreeUUID(9333), EventTypeNodeRemoved, func(tree, _ string) any {
			return NodeRemovedPayload{TreeID: tree, UserID: "nope", RemovedAt: writeTime.Add(time.Hour), TreeType: treeTypeUnilevel}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			it := newWriterIntegration(t)
			ctx := context.Background()
			root, x := testUserUUID(1), testUserUUID(7)
			it.addUnilevelRoot(t, c.tree, root)
			foreign := appendForeign(t, it, c.tree, c.eventType, c.payload(c.tree, root))
			placeX := PlaceRequest{TreeID: c.tree, UserID: x, ParentID: root, SponsorID: root, EnrolledAt: writeTime.Add(2 * time.Hour)}

			_, refusedErr := it.writer(t).Place(ctx, placeX)

			var caught *CatchUpFailedError
			require.ErrorAs(t, refusedErr, &caught)
			assert.Equal(t, foreign.ID, caught.EventID)
			assert.ErrorIs(t, refusedErr, ErrUnprojectableEvent)
			projected, _, err := it.store.ProjectedVersion(ctx, c.tree)
			require.NoError(t, err)
			assert.Equal(t, foreign.Version-1, projected)

			rejected, err := it.writer(t).Reject(ctx, RejectRequest{TreeID: c.tree, EventID: foreign.ID, Reason: "id is not a canonical UUID"})

			require.NoError(t, err)
			require.Equal(t, RejectOutcomeRejected, rejected.Outcome, "the retry returned %v", rejected.RetryErr)
			assert.ErrorIs(t, rejected.RetryErr, ErrUnprojectableEvent)
			assert.NoError(t, rejected.ProjectionErr)
			placedX, err := it.writer(t).Place(ctx, placeX)
			require.NoError(t, err)
			require.NoError(t, placedX.ProjectionErr)
			assert.Equal(t, map[string]string{root: root, x: root}, activeSponsors(t, it.store, c.tree))
		})
	}
}

func TestTreeWriterReject_ClearsAForeignFirstRootWhoseUserIsNotCanonical(t *testing.T) {
	it := newWriterIntegration(t)
	ctx := context.Background()
	tree, root := testTreeUUID(9335), testUserUUID(1)
	stream := TreeStreamName(tree)
	ev, err := newTreeEvent(EventTypeRootAdded, RootAddedPayload{TreeID: tree, UserID: "nope", SponsorID: root,
		TreeType: treeTypeUnilevel, EnrolledAt: writeTime})
	require.NoError(t, err)
	require.NoError(t, it.events.Append(ctx, stream, 0, []platform.NewEvent{ev}))
	foreign, err := it.events.ReadLastEvent(ctx, stream)
	require.NoError(t, err)
	require.NotNil(t, foreign)
	addRoot := AddRootRequest{TreeID: tree, UserID: root, SponsorID: root, TreeType: treeTypeUnilevel, EnrolledAt: writeTime}

	_, refusedErr := it.writer(t).AddRoot(ctx, addRoot)

	var caught *CatchUpFailedError
	require.ErrorAs(t, refusedErr, &caught)
	assert.Equal(t, foreign.ID, caught.EventID)
	assert.ErrorIs(t, refusedErr, ErrUnprojectableEvent)
	_, found, err := it.store.ProjectedVersion(ctx, tree)
	require.NoError(t, err)
	assert.False(t, found, "the refused root left a projection row")

	rejected, err := it.writer(t).Reject(ctx, RejectRequest{TreeID: tree, EventID: foreign.ID, Reason: "user_id is not a canonical UUID"})

	require.NoError(t, err)
	require.Equal(t, RejectOutcomeRejected, rejected.Outcome, "the retry returned %v", rejected.RetryErr)
	assert.ErrorIs(t, rejected.RetryErr, ErrUnprojectableEvent)
	assert.NoError(t, rejected.ProjectionErr)
	added, err := it.writer(t).AddRoot(ctx, addRoot)
	require.NoError(t, err)
	require.NoError(t, added.ProjectionErr)
	assert.Equal(t, map[string]string{root: root}, activeSponsors(t, it.store, tree))
}
