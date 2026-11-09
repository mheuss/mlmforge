package networkengine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// nulTakeEngine fails every TakeSnapshot with an error whose text holds a NUL byte.
type nulTakeEngine struct{ TreeRebuilder }

func (nulTakeEngine) TakeSnapshot(context.Context, string) (*SnapshotResultDTO, error) {
	return nil, errors.New("worker said \x00 here")
}

func TestTreeWriterSnapshotBackoff_RecordsATakeErrorThatHoldsANulByte(t *testing.T) {
	it := newWriterIntegration(t)
	ctx := context.Background()
	tree, root := testTreeUUID(9140), testUserUUID(1)
	it.addUnilevelRoot(t, tree, root)
	it.seedPlacements(t, tree, root, 2, snapshotInterval-1)

	res, err := NewTreeWriter(it.events, it.store, it.snapshots, nulTakeEngine{TreeRebuilder: it.engine(t)},
		NewPostgresTreeLocker(it.dsn)).
		Place(ctx, PlaceRequest{TreeID: tree, UserID: testUserUUID(snapshotInterval), ParentID: root, SponsorID: root,
			EnrolledAt: writeTime.Add(snapshotInterval * time.Second)})

	require.NoError(t, err)
	require.NoError(t, res.ProjectionErr)
	require.Equal(t, int64(snapshotInterval), res.Version)
	assert.Equal(t, []string{"taking a snapshot of tree " + tree + " at version 1000 returned: worker said \x00 here"},
		res.Warnings, "the printed warning keeps the bytes the engine sent")
	got, found, err := it.snapshots.LastAttempt(ctx, tree)
	require.NoError(t, err)
	require.True(t, found, "no attempt recorded; the write warned %q", res.Warnings)
	assert.Equal(t, SnapshotAttempt{TreeID: tree, Version: snapshotInterval,
		Observed: "taking a snapshot of tree " + tree + " at version 1000 returned: worker said \uFFFD here"}, got)
}
