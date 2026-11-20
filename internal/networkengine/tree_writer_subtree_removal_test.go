package networkengine

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeLayout places each user in layout into writerTree through a writer over env, in order.
func writeLayout(t *testing.T, env *writerEnv, layout []subtreePlacement) {
	t.Helper()
	for _, p := range layout {
		slot := p.slot
		w, _ := env.writer()
		res, err := w.Place(context.Background(), PlaceRequest{TreeID: writerTree, UserID: p.user, ParentID: p.parent,
			SponsorID: p.sponsor, Position: &slot, EnrolledAt: time.Unix(p.enrolledAt, 0).UTC()})
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
