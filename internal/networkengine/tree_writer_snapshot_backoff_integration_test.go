package networkengine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// takeCountingEngine counts TakeSnapshot calls into a counter shared across engines.
type takeCountingEngine struct {
	TreeRebuilder
	takes *int
}

func (e takeCountingEngine) TakeSnapshot(ctx context.Context, structure string) (*SnapshotResultDTO, error) {
	*e.takes++
	return e.TreeRebuilder.TakeSnapshot(ctx, structure)
}

// takesAcrossWriters seeds tree to version 999, then runs writes 1000 to 1010,
// each through a new TreeWriter and worker. It returns the TakeSnapshot calls.
func takesAcrossWriters(t *testing.T, it *writerIntegration, tree, root string, snapshots SnapshotStore) int {
	t.Helper()
	ctx := context.Background()
	it.addUnilevelRoot(t, tree, root)
	it.seedPlacements(t, tree, root, 2, snapshotInterval-1)
	takes := 0
	for v := int64(snapshotInterval); v <= snapshotInterval+10; v++ {
		engine := takeCountingEngine{TreeRebuilder: it.engine(t), takes: &takes}
		res, err := NewTreeWriter(it.events, it.store, snapshots, engine, NewPostgresTreeLocker(it.dsn)).
			Place(ctx, PlaceRequest{TreeID: tree, UserID: testUserUUID(int(v)), ParentID: root, SponsorID: root,
				EnrolledAt: writeTime.Add(time.Duration(v) * time.Second)})
		require.NoError(t, err)
		require.NoError(t, res.ProjectionErr)
		require.Equal(t, v, res.Version)
	}
	return takes
}

func TestTreeWriterSnapshotBackoff_AFailingPutBacksOffAcrossWriters(t *testing.T) {
	it := newWriterIntegration(t)
	tree, root := testTreeUUID(914), testUserUUID(1)
	failing := failingPutStore{SnapshotStore: it.snapshots, err: errors.New("disk full")}

	takes := takesAcrossWriters(t, it, tree, root, failing)

	assert.Equal(t, 1, takes)
	got, found, err := it.snapshots.LastAttempt(context.Background(), tree)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, SnapshotAttempt{TreeID: tree, Version: snapshotInterval,
		Observed: "storing the snapshot of tree " + tree + " at version 1000 returned: disk full"}, got)
}

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
