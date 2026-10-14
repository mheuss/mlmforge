package networkengine

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/mlmforge/mlmforge/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedPlacements appends and projects a placement under root at each version
// from through to, placing testUserUUID(version).
func (it *writerIntegration) seedPlacements(t *testing.T, tree, root string, from, to int64) {
	t.Helper()
	ctx := context.Background()
	stream := TreeStreamName(tree)
	for v := from; v <= to; v++ {
		user := testUserUUID(int(v))
		p := NodePlacedPayload{TreeID: tree, UserID: user, ParentID: root, SponsorID: root,
			TreeType: treeTypeUnilevel, EnrolledAt: writeTime.Add(time.Duration(v) * time.Second)}
		ev, err := newTreeEvent(EventTypeNodePlaced, p)
		require.NoError(t, err)
		require.NoError(t, it.events.Append(ctx, stream, v-1, []platform.NewEvent{ev}))
		parent := root
		require.NoError(t, it.store.ProjectInsert(ctx, TreeNodeRow{ID: ev.ID, TreeID: tree, UserID: user,
			ParentID: &parent, SponsorID: &parent, Depth: 1, EnrolledAt: p.EnrolledAt}, v))
	}
}

// strayWriteAtTheInterval has a writer lose its lock right after appending
// version snapshotInterval, while a load by another writer redelivers that
// version first. It returns the stray writer's result.
func strayWriteAtTheInterval(t *testing.T, it *writerIntegration, tree, root string) WriteResult {
	t.Helper()
	ctx := context.Background()
	it.addUnilevelRoot(t, tree, root)
	it.seedPlacements(t, tree, root, 2, snapshotInterval-1)
	var caught LoadResult
	var caughtErr error
	events := &lockKillingEvents{EventStore: it.events, t: t, pool: it.pool, meanwhile: func() {
		caught, caughtErr = it.writer(t).Load(ctx, LoadRequest{TreeID: tree, TreeType: treeTypeUnilevel})
	}}

	stray, err := NewTreeWriter(events, it.store, it.snapshots, it.engine(t), NewPostgresTreeLocker(it.dsn)).
		Place(ctx, PlaceRequest{TreeID: tree, UserID: testUserUUID(snapshotInterval), ParentID: root, SponsorID: root,
			EnrolledAt: writeTime.Add(snapshotInterval * time.Second)})

	require.NoError(t, err)
	require.NoError(t, caughtErr)
	require.NotNil(t, caught.CaughtUp, "the other writer's load redelivered nothing")
	require.Equal(t, int64(snapshotInterval), caught.CaughtUp.Version)
	require.Equal(t, int64(snapshotInterval), stray.Version)
	require.NoError(t, stray.ProjectionErr)
	return stray
}

func TestTreeWriterSnapshot_AStrayWritersSnapshotMatchesTheStreamAtItsVersion(t *testing.T) {
	it := newWriterIntegration(t)
	ctx := context.Background()
	tree, root := testTreeUUID(2990), testUserUUID(1)
	stray := strayWriteAtTheInterval(t, it, tree, root)
	assert.Empty(t, stray.Warnings)
	snap, found, err := it.snapshots.Get(ctx, tree)
	require.NoError(t, err)
	require.True(t, found, "the stray writer stored no snapshot")
	require.Equal(t, int64(snapshotInterval), snap.Version)

	restored := it.engine(t)
	require.NoError(t, restored.RestoreSnapshot(ctx, tree, snap.TreeType, json.RawMessage(snap.Data)))
	replayed := it.engine(t)
	require.NoError(t, replayed.CreateTree(ctx, tree, treeTypeUnilevel))
	_, err = replayStream(ctx, it.events, replayed, tree, treeShape{treeType: treeTypeUnilevel}, 1, snap.Version)
	require.NoError(t, err)

	for i := 1; i <= snapshotInterval; i++ {
		user := testUserUUID(i)
		want, err := replayed.GetPosition(ctx, tree, user)
		require.NoError(t, err, "replayed engine, user %s", user)
		got, err := restored.GetPosition(ctx, tree, user)
		require.NoError(t, err, "restored engine, user %s", user)
		require.Equal(t, want, got, "user %s", user)
	}
}

func TestTreeWriterSnapshot_AStraySnapshotOfARejectedEventIsDeletedAtLoad(t *testing.T) {
	it := newWriterIntegration(t)
	ctx := context.Background()
	tree, root := testTreeUUID(2991), testUserUUID(1)
	strayWriteAtTheInterval(t, it, tree, root)
	stream := TreeStreamName(tree)
	read, err := it.events.ReadStream(ctx, stream, snapshotInterval, 1)
	require.NoError(t, err)
	require.Len(t, read, 1)
	rejected := read[0]
	rejection, err := newTreeEvent(EventTypeEventRejected, EventRejectedPayload{TreeID: tree,
		RejectedEventID: rejected.ID, RejectedVersion: rejected.Version, RejectedType: rejected.Type,
		Reason: "appended by the test"})
	require.NoError(t, err)
	require.NoError(t, it.events.Append(ctx, stream, snapshotInterval, []platform.NewEvent{rejection}))
	require.NoError(t, it.store.ProjectRejection(ctx, tree, rejected.ID, snapshotInterval+1))
	_, found, err := it.snapshots.Get(ctx, tree)
	require.NoError(t, err)
	require.True(t, found, "the stray writer stored no snapshot")
	engine := it.engine(t)

	res, err := NewTreeWriter(it.events, it.store, it.snapshots, engine, NewPostgresTreeLocker(it.dsn)).
		Load(ctx, LoadRequest{TreeID: tree, TreeType: treeTypeUnilevel})

	require.NoError(t, err)
	require.Len(t, res.Warnings, 1)
	assert.Contains(t, res.Warnings[0], "rejection "+rejection.ID)
	assert.Contains(t, res.Warnings[0], "deleted it and replayed the stream")
	assert.Equal(t, snapshotInterval-1, res.Nodes)
	_, found, err = it.snapshots.Get(ctx, tree)
	require.NoError(t, err)
	assert.False(t, found)
	_, err = engine.GetPosition(ctx, tree, testUserUUID(snapshotInterval))
	assert.True(t, isEngineCode(err, engineCodeUserNotFound), "GetPosition returned %v", err)
}
