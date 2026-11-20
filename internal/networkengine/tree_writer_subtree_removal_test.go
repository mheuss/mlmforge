package networkengine

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeLayout places each user in layout into writerTree through a writer over env, in order,
// enrolling each user enrolledAt seconds after writeTime.
func writeLayout(t *testing.T, env *writerEnv, layout []subtreePlacement) {
	t.Helper()
	for _, p := range layout {
		slot := p.slot
		w, _ := env.writer()
		res, err := w.Place(context.Background(), PlaceRequest{TreeID: writerTree, UserID: p.user, ParentID: p.parent,
			SponsorID: p.sponsor, Position: &slot, EnrolledAt: writeTime.Add(time.Duration(p.enrolledAt) * time.Second)})
		require.NoError(t, err, "placing %s", p.user)
		require.NoError(t, res.ProjectionErr, "projecting %s", p.user)
	}
}

func TestTreeWriterRemove_TombstonesEveryUserAHoldingTankRemovalTanks(t *testing.T) {
	ctx := context.Background()
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeMatrix)
	writeLayout(t, env, tankSubtreeLayout)
	w, _ := env.writer()

	res, err := w.Remove(ctx, RemoveRequest{TreeID: writerTree, UserID: tankX, RemovedAt: writeTime,
		PruningMode: PruningHoldingTank})

	require.NoError(t, err)
	require.NoError(t, res.ProjectionErr)
	for _, user := range []string{tankC0, tankC1, tankG, tankX} {
		row, err := env.store.GetNodeIncludingRemoved(ctx, writerTree, user)
		require.NoError(t, err)
		require.NotNil(t, row, "the row of %s", user)
		require.NotNil(t, row.RemovedByEventID, "%s is still active", user)
		assert.Equal(t, res.EventID, *row.RemovedByEventID, "the event that removed %s", user)
	}
	for _, want := range []struct{ user, sponsor string }{
		{tankO3, tankS}, {tankO2, tankT}, {tankO1, tankS}, {tankS, writerRoot}, {tankT, writerRoot},
	} {
		row, err := env.store.GetNode(ctx, writerTree, want.user)
		require.NoError(t, err)
		require.NotNil(t, row, "the active row of %s", want.user)
		require.NotNil(t, row.SponsorID, "the sponsor of %s", want.user)
		assert.Equal(t, want.sponsor, *row.SponsorID, "the sponsor of %s", want.user)
	}
}

func TestTreeWriterRemove_MovesEveryRowAPromotionRelocates(t *testing.T) {
	ctx := context.Background()
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeMatrix)
	a, b, a0 := testUserUUID(61), testUserUUID(62), testUserUUID(63)
	writeLayout(t, env, []subtreePlacement{
		{writerChild, writerRoot, writerRoot, 0, 200},
		{a, writerChild, writerChild, 0, 400},
		{b, writerChild, writerChild, 1, 300},
		{a0, a, a, 0, 500},
	})
	unchanged := map[string]*TreeNodeRow{}
	for _, user := range []string{writerRoot, a0} {
		row, err := env.store.GetNode(ctx, writerTree, user)
		require.NoError(t, err)
		require.NotNil(t, row, "the row of %s before the removal", user)
		unchanged[user] = row
	}
	w, _ := env.writer()

	res, err := w.Remove(ctx, RemoveRequest{TreeID: writerTree, UserID: writerChild, RemovedAt: writeTime,
		PruningMode: PruningPromoteEarliest})

	require.NoError(t, err)
	require.NoError(t, res.ProjectionErr)
	for _, want := range []struct {
		user, parent    string
		position, depth int
	}{
		{b, writerRoot, 0, 1},
		{a, b, 0, 2},
	} {
		row, err := env.store.GetNode(ctx, writerTree, want.user)
		require.NoError(t, err)
		require.NotNil(t, row, "the active row of %s", want.user)
		require.NotNil(t, row.ParentID, "the parent of %s", want.user)
		require.NotNil(t, row.Position, "the position of %s", want.user)
		assert.Equal(t, want.parent, *row.ParentID, "the parent of %s", want.user)
		assert.Equal(t, want.position, *row.Position, "the position of %s", want.user)
		assert.Equal(t, want.depth, row.Depth, "the depth of %s", want.user)
		require.NotNil(t, row.SponsorID, "the sponsor of %s", want.user)
		assert.Equal(t, writerRoot, *row.SponsorID, "the sponsor of %s", want.user)
	}
	for user, before := range unchanged {
		row, err := env.store.GetNode(ctx, writerTree, user)
		require.NoError(t, err)
		assert.Equal(t, before, row, "the row of %s", user)
	}
	removed, err := env.store.GetNodeIncludingRemoved(ctx, writerTree, writerChild)
	require.NoError(t, err)
	require.NotNil(t, removed)
	require.NotNil(t, removed.RemovedByEventID, "the removed user is still active")
	assert.Equal(t, res.EventID, *removed.RemovedByEventID)
}
