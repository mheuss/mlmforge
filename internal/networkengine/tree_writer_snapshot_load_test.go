package networkengine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/mlmforge/mlmforge/internal/platform"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// takeTreeSnapshot loads writerTree into a fake and snapshots it at version,
// storing nothing.
func takeTreeSnapshot(t *testing.T, env *writerEnv, version int64) Snapshot {
	t.Helper()
	w, engine := env.writer()
	_, err := w.Load(context.Background(), loadRequest())
	require.NoError(t, err)
	taken, err := engine.TakeSnapshot(context.Background(), writerTree)
	require.NoError(t, err)
	return Snapshot{TreeID: writerTree, Version: version, TreeType: taken.TreeType, Data: taken.Data}
}

// seedSnapshot takes a snapshot of writerTree at version and stores it.
func seedSnapshot(t *testing.T, env *writerEnv, version int64) Snapshot {
	t.Helper()
	snap := takeTreeSnapshot(t, env, version)
	require.NoError(t, env.snapshots.Put(context.Background(), snap))
	return snap
}

func TestTreeWriterSnapshotLoad_ReplaysOnlyTheEventsAfterTheSnapshot(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	mustPlace(t, env, writerChild, nil)
	seedSnapshot(t, env, 2)
	mustPlace(t, env, writerOther, nil)
	w, engine := env.writer()

	res, err := w.Load(context.Background(), loadRequest())

	require.NoError(t, err)
	assert.Empty(t, res.Warnings)
	assert.Equal(t, 3, res.Nodes)
	assert.Contains(t, engine.calls, "restore_snapshot")
	assert.Equal(t, []string{writerOther}, placementUsers(engine))
}

func TestTreeWriterSnapshotLoad_DeletesASnapshotAboveTheProjectedVersion(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	seedSnapshot(t, env, 9)
	w, engine := env.writer()

	res, err := w.Load(context.Background(), loadRequest())

	require.NoError(t, err)
	assert.Equal(t, []string{"the snapshot of tree " + writerTree +
		" is at version 9, above the projected version 1; deleted it and replayed the stream"}, res.Warnings)
	assert.NotContains(t, engine.calls, "restore_snapshot")
	_, found, err := env.snapshots.Get(context.Background(), writerTree)
	require.NoError(t, err)
	assert.False(t, found)
}

// getFailingSnapshots fails every Get.
type getFailingSnapshots struct {
	SnapshotStore
	err error
}

func (s getFailingSnapshots) Get(context.Context, string) (Snapshot, bool, error) {
	return Snapshot{}, false, s.err
}

// deleteFailingSnapshots fails every Delete.
type deleteFailingSnapshots struct {
	SnapshotStore
	err error
}

func (s deleteFailingSnapshots) Delete(context.Context, string, int64) error { return s.err }

// rejectionOf builds a rejection payload naming ev.
func rejectionOf(ev platform.Event) EventRejectedPayload {
	return EventRejectedPayload{TreeID: writerTree, RejectedEventID: ev.ID, RejectedVersion: ev.Version,
		RejectedType: ev.Type, Reason: "appended by the test"}
}

// midReplayLoad seeds a snapshot at version 2, places writerOther at version 3,
// and returns a writer whose engine refuses writerOther's next add once.
func midReplayLoad(t *testing.T) (*writerEnv, *TreeWriter, *fakeWriterEngine) {
	t.Helper()
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	mustPlace(t, env, writerChild, nil)
	seedSnapshot(t, env, 2)
	mustPlace(t, env, writerOther, nil)
	w, engine := env.writer()
	engine.failAddOnce[writerOther] = fakeEngineError("SPONSOR_NOT_FOUND", "refused by the test")
	return env, w, engine
}

// countCalls counts the engine calls named op.
func countCalls(engine *fakeWriterEngine, op string) int {
	n := 0
	for _, c := range engine.calls {
		if c == op {
			n++
		}
	}
	return n
}

// replayAtStage requires err to carry a TreeReplayError at stage, and returns it.
func replayAtStage(t *testing.T, err error, stage TreeReplayStage) *TreeReplayError {
	t.Helper()
	requireReplayStage(t, err, stage)
	var replay *TreeReplayError
	errors.As(err, &replay)
	return replay
}

// requireReplayStage requires err to carry a TreeReplayError at stage.
func requireReplayStage(t *testing.T, err error, stage TreeReplayStage) {
	t.Helper()
	var replay *TreeReplayError
	require.ErrorAs(t, err, &replay)
	require.Equal(t, stage, replay.Stage)
}

// snapshotStored reports whether writerTree has a stored snapshot.
func snapshotStored(t *testing.T, env *writerEnv) bool {
	t.Helper()
	_, found, err := env.snapshots.Get(context.Background(), writerTree)
	require.NoError(t, err)
	return found
}

func TestTreeWriterSnapshotLoad_RedeliversTheLastEventOnceAfterARestore(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	mustPlace(t, env, writerChild, nil)
	seedSnapshot(t, env, 2)
	appendDirect(t, env.events, EventTypeNodePlaced, childPlacedPayload(writerOther))
	w, engine := env.writer()

	res, err := w.Load(context.Background(), loadRequest())

	require.NoError(t, err)
	require.NotNil(t, res.CaughtUp)
	assert.Equal(t, int64(3), res.CaughtUp.Version)
	assert.Equal(t, []string{writerOther}, placementUsers(engine))
}

func TestTreeWriterSnapshotLoad_DeletesASnapshotWhoseNextEventRejectsIt(t *testing.T) {
	env := newWriterEnv()
	ctx := context.Background()
	mustAddRoot(t, env, treeTypeUnilevel)
	mustPlace(t, env, writerChild, nil)
	child := streamEvents(t, env.events, TreeStreamName(writerTree))[1]
	seedSnapshot(t, env, 2)
	rejection := appendDirect(t, env.events, EventTypeEventRejected, rejectionOf(child))
	require.NoError(t, env.store.ProjectRejection(ctx, writerTree, child.ID, 3))
	w, _ := env.writer()

	res, err := w.Load(ctx, loadRequest())

	require.NoError(t, err)
	assert.Equal(t, []string{"the snapshot of tree " + writerTree + " is at version 2, and rejection " + rejection.ID +
		" at version 3 names that version; deleted it and replayed the stream"}, res.Warnings)
	assert.Equal(t, 1, res.Nodes)
	assert.False(t, snapshotStored(t, env))
}

func TestTreeWriterSnapshotLoad_DeletesASnapshotOfAnotherType(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	require.NoError(t, env.snapshots.Put(context.Background(),
		Snapshot{TreeID: writerTree, Version: 1, TreeType: treeTypeBinary, Data: []byte(`{}`)}))
	w, engine := env.writer()

	res, err := w.Load(context.Background(), loadRequest())

	require.NoError(t, err)
	assert.Equal(t, []string{"the snapshot of tree " + writerTree + " records tree type binary, and version 1 of stream " +
		TreeStreamName(writerTree) + " records unilevel; deleted it and replayed the stream"}, res.Warnings)
	assert.NotContains(t, engine.calls, "restore_snapshot")
	assert.False(t, snapshotStored(t, env))
}

func TestTreeWriterSnapshotLoad_RecoversFromAnEngineRefusalMidReplay(t *testing.T) {
	env, w, engine := midReplayLoad(t)

	res, err := w.Load(context.Background(), loadRequest())

	require.NoError(t, err)
	assert.Equal(t, 1, countCalls(engine, "drop_structure"))
	require.Len(t, res.Warnings, 1)
	assert.True(t, strings.HasPrefix(res.Warnings[0], "the snapshot of tree "+writerTree+" at version 2 could not be used ("),
		res.Warnings[0])
	assert.True(t, strings.HasSuffix(res.Warnings[0], "); dropped the structure; deleted it and replayed the stream"),
		res.Warnings[0])
	assert.Equal(t, 3, res.Nodes)
	assert.False(t, snapshotStored(t, env))
}

func TestTreeWriterSnapshotLoad_RecoversFromARefusedRestore(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	seedSnapshot(t, env, 1)
	w, engine := env.writer()
	engine.restoreErr = fakeEngineError("INCONSISTENT_SNAPSHOT", "refused by the test")

	res, err := w.Load(context.Background(), loadRequest())

	require.NoError(t, err)
	assert.Equal(t, 1, countCalls(engine, "drop_structure"))
	assert.Len(t, res.Warnings, 1)
	assert.False(t, snapshotStored(t, env))
}

func TestTreeWriterSnapshotLoad_RecoversFromASnapshotThatDisagreesWithTheStore(t *testing.T) {
	env := newWriterEnv()
	ctx := context.Background()
	mustAddRoot(t, env, treeTypeUnilevel)
	mustPlace(t, env, writerChild, nil)
	snap := takeTreeSnapshot(t, env, 2)
	var held fakeSnapshot
	require.NoError(t, json.Unmarshal(snap.Data, &held))
	root := writerRoot
	held.Nodes[writerOther] = EnginePosition{UserID: writerOther, ParentUserID: &root, SponsorUserID: &root,
		Depth: 1, EnrolledAt: writeTime.Unix()}
	data, err := json.Marshal(held)
	require.NoError(t, err)
	snap.Data = data
	require.NoError(t, env.snapshots.Put(ctx, snap))
	w, engine := env.writer()

	res, err := w.Load(ctx, loadRequest())

	require.NoError(t, err)
	assert.Equal(t, 1, countCalls(engine, "drop_structure"))
	assert.Len(t, res.Warnings, 1)
	assert.Equal(t, 2, res.Nodes)
}

func TestTreeWriterSnapshotLoad_FailsWhenTheFullReplayAlsoDisagrees(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	seedSnapshot(t, env, 1)
	root := writerRoot
	require.NoError(t, env.store.InsertNode(context.Background(), TreeNodeRow{
		ID: testNodeUUID(70), TreeID: writerTree, UserID: writerOther, ParentID: &root, SponsorID: &root,
		Depth: 1, EnrolledAt: writeTime,
	}))
	w, _ := env.writer()

	_, err := w.Load(context.Background(), loadRequest())

	replay := replayAtStage(t, err, TreeReplayStageMembership)
	assert.Contains(t, replay.Mismatch, "only in the store: ["+writerOther+"]")
}

func TestTreeWriterSnapshotLoad_FailsWithoutADropOnAClosedTransport(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	seedSnapshot(t, env, 1)
	w, engine := env.writer()
	engine.restoreErr = fmt.Errorf("restore: %w", ErrTransportClosed)

	_, err := w.Load(context.Background(), loadRequest())

	requireReplayStage(t, err, TreeReplayStageRestore)
	assert.NotContains(t, engine.calls, "drop_structure")
	assert.True(t, snapshotStored(t, env))
}

func TestTreeWriterSnapshotLoad_ReadsTheFullStreamWhenTheSnapshotReadFails(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	w := NewTreeWriter(env.events, env.store, getFailingSnapshots{SnapshotStore: env.snapshots, err: errors.New("connection reset")},
		newFakeWriterEngine(), env.locker)

	res, err := w.Load(context.Background(), loadRequest())

	require.NoError(t, err)
	assert.Equal(t, []string{"reading the snapshot of tree " + writerTree + " returned: connection reset; replayed the stream"},
		res.Warnings)
}

func TestTreeWriterSnapshotLoad_ContinuesWhenADeleteFails(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	seedSnapshot(t, env, 9)
	w := NewTreeWriter(env.events, env.store, deleteFailingSnapshots{SnapshotStore: env.snapshots, err: errors.New("connection reset")},
		newFakeWriterEngine(), env.locker)

	res, err := w.Load(context.Background(), loadRequest())

	require.NoError(t, err)
	assert.Equal(t, []string{"the snapshot of tree " + writerTree +
		" is at version 9, above the projected version 1; deleting it returned: connection reset; replayed the stream"}, res.Warnings)
}

func TestTreeWriterSnapshotLoad_FailsWhenTheDropIsRefused(t *testing.T) {
	_, w, engine := midReplayLoad(t)
	engine.dropErr = fakeEngineError("INVALID_PARAMS", "refused by the test")

	_, err := w.Load(context.Background(), loadRequest())

	requireReplayStage(t, err, TreeReplayStageDrop)
}

func TestTreeWriterSnapshotLoad_FailsWhenTheDropFindsNoStructureAfterARestore(t *testing.T) {
	_, w, engine := midReplayLoad(t)
	engine.dropReportsAbsent = true

	_, err := w.Load(context.Background(), loadRequest())

	replay := replayAtStage(t, err, TreeReplayStageDrop)
	assert.Equal(t, "the restore succeeded, and drop_structure reported no structure named "+writerTree, replay.Observed)
}

func TestTreeWriterSnapshotLoad_CountsARefusedMembershipQueryAsAMismatch(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	seedSnapshot(t, env, 1)
	w, engine := env.writer()
	engine.downlineErrOnce = fakeEngineError(engineCodeUserNotFound, "refused by the test")

	_, err := w.Load(context.Background(), loadRequest())

	require.NoError(t, err)
	assert.Equal(t, 1, countCalls(engine, "drop_structure"))
}

func TestTreeWriterSnapshotLoad_FailsWithoutADropWhenTheMembershipQueryLosesItsTransport(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	seedSnapshot(t, env, 1)
	w, engine := env.writer()
	engine.downlineErr = ErrTransportClosed

	_, err := w.Load(context.Background(), loadRequest())

	requireReplayStage(t, err, TreeReplayStageMembership)
	assert.NotContains(t, engine.calls, "drop_structure")
}

func TestTreeWriterSnapshotLoad_FailsWithoutADropWhenTheReadAfterTheSnapshotFails(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	mustPlace(t, env, writerChild, nil)
	seedSnapshot(t, env, 1)
	engine := newFakeWriterEngine()
	events := &failingReadEvents{MemoryEventStore: env.events.(*platform.MemoryEventStore), from: 2,
		err: errors.New("connection reset")}
	w := NewTreeWriter(events, env.store, env.snapshots, engine, env.locker)

	_, err := w.Load(context.Background(), loadRequest())

	requireReplayStage(t, err, TreeReplayStageReplay)
	assert.NotContains(t, engine.calls, "drop_structure")
	assert.NotContains(t, engine.calls, "restore_snapshot")
}

func TestTreeWriterSnapshotLoad_FailsWithoutADropWhenAReplayReadFails(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	mustPlace(t, env, writerChild, nil)
	seedSnapshot(t, env, 2)
	mustPlace(t, env, writerOther, nil)
	mustPlace(t, env, testUserUUID(4), nil)
	engine := newFakeWriterEngine()
	events := &failingReadEvents{MemoryEventStore: env.events.(*platform.MemoryEventStore), from: 3, limit: 2,
		err: errors.New("connection reset")}
	w := NewTreeWriter(events, env.store, env.snapshots, engine, env.locker)

	_, err := w.Load(context.Background(), loadRequest())

	requireReplayStage(t, err, TreeReplayStageReplay)
	assert.Contains(t, engine.calls, "restore_snapshot")
	assert.NotContains(t, engine.calls, "drop_structure")
}

func TestTreeWriterSnapshotLoad_RefusesAPendingRejectionOfTheSnapshotVersion(t *testing.T) {
	env := newWriterEnv()
	ctx := context.Background()
	mustAddRoot(t, env, treeTypeUnilevel)
	mustPlace(t, env, writerChild, nil)
	child := streamEvents(t, env.events, TreeStreamName(writerTree))[1]
	seedSnapshot(t, env, 2)
	rejection := appendDirect(t, env.events, EventTypeEventRejected, rejectionOf(child))
	w, _ := env.writer()

	res, err := w.Load(ctx, loadRequest())

	var pending *RejectionPendingError
	require.ErrorAs(t, err, &pending)
	assert.Empty(t, res.Warnings)
	assert.True(t, snapshotStored(t, env))

	require.NoError(t, env.store.ProjectRejection(ctx, writerTree, child.ID, 3))
	w, engine := env.writer()
	res, err = w.Load(ctx, loadRequest())

	require.NoError(t, err)
	require.Len(t, res.Warnings, 1)
	assert.Contains(t, res.Warnings[0], rejection.ID)
	assert.False(t, snapshotStored(t, env))
	_, err = engine.GetPosition(ctx, writerTree, writerChild)
	assert.True(t, isEngineCode(err, engineCodeUserNotFound))
}

func TestTreeWriterSnapshotLoad_CountsZeroRowsAsAMismatchOnTheSnapshotPath(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	seedSnapshot(t, env, 1)
	require.NoError(t, env.store.DeleteNode(context.Background(), writerTree, writerRoot))
	w, engine := env.writer()

	_, err := w.Load(context.Background(), loadRequest())

	assert.Equal(t, 1, countCalls(engine, "drop_structure"))
	requireReplayStage(t, err, TreeReplayStageMembership)
	assert.False(t, snapshotStored(t, env))
}

func TestTreeWriterSnapshotLoad_FailsWithoutADropOnACancelledContext(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	seedSnapshot(t, env, 1)
	w, engine := env.writer()
	engine.restoreErr = context.Canceled

	_, err := w.Load(context.Background(), loadRequest())

	requireReplayStage(t, err, TreeReplayStageRestore)
	assert.NotContains(t, engine.calls, "drop_structure")
}

func TestTreeWriterSnapshotLoad_FailsWithoutADropWhenReplayLosesItsTransport(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	mustPlace(t, env, writerChild, nil)
	seedSnapshot(t, env, 2)
	mustPlace(t, env, writerOther, nil)
	w, engine := env.writer()
	engine.failAddOnce[writerOther] = fmt.Errorf("add: %w", ErrTransportClosed)

	_, err := w.Load(context.Background(), loadRequest())

	requireReplayStage(t, err, TreeReplayStageReplay)
	assert.Contains(t, engine.calls, "restore_snapshot")
	assert.NotContains(t, engine.calls, "drop_structure")
	assert.True(t, snapshotStored(t, env))
}

func TestTreeWriterSnapshotLoad_FailsWhenTheDropLosesItsTransport(t *testing.T) {
	_, w, engine := midReplayLoad(t)
	engine.dropErr = ErrTransportClosed

	_, err := w.Load(context.Background(), loadRequest())

	requireReplayStage(t, err, TreeReplayStageDrop)
}

func TestTreeWriterSnapshotLoad_FailsOnARefusedMembershipQueryAfterAFullReplay(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	w, engine := env.writer()
	engine.downlineErr = fakeEngineError(engineCodeUserNotFound, "refused by the test")

	_, err := w.Load(context.Background(), loadRequest())

	requireReplayStage(t, err, TreeReplayStageMembership)
	assert.NotContains(t, engine.calls, "drop_structure")
}

func TestTreeWriterSnapshotLoad_TakesTheNextSnapshotOnlyAfterAnotherThousandEvents(t *testing.T) {
	env := newWriterEnv()
	ctx := context.Background()
	mustAddRoot(t, env, treeTypeUnilevel)
	seedSnapshot(t, env, 1)
	placeUnderRoot(t, env, 10, snapshotInterval-1)
	snap, found, err := env.snapshots.Get(ctx, writerTree)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, int64(1), snap.Version)

	res := mustPlace(t, env, testUserUUID(9000), nil)

	require.Equal(t, int64(snapshotInterval+1), res.Version)
	snap, _, err = env.snapshots.Get(ctx, writerTree)
	require.NoError(t, err)
	assert.Equal(t, int64(snapshotInterval+1), snap.Version)
}

func TestTreeWriterSnapshotLoad_KeepsTheLoadsWarningBeforeTheSnapshotWarning(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	seedSnapshot(t, env, 1)
	placeUnderRoot(t, env, 10, snapshotInterval-1)
	engine := newFakeWriterEngine()
	engine.failAddOnce[testUserUUID(10)] = fakeEngineError("SPONSOR_NOT_FOUND", "refused by the test")
	w := NewTreeWriter(env.events, env.store, failingPutStore{SnapshotStore: env.snapshots, err: errors.New("disk full")},
		engine, env.locker)

	res, err := w.Place(context.Background(), placeRequest(testUserUUID(9000), nil))

	require.NoError(t, err)
	require.Equal(t, int64(snapshotInterval+1), res.Version)
	require.Len(t, res.Warnings, 2)
	assert.True(t, strings.HasPrefix(res.Warnings[0], "the snapshot of tree "+writerTree+" at version 1 could not be used ("),
		res.Warnings[0])
	assert.Equal(t, "storing the snapshot of tree "+writerTree+" at version 1001 returned: disk full", res.Warnings[1])
}

func TestTreeWriterSnapshotLoad_RefusesARestoreOverAStructureTheLoadDidNotBuild(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	seedSnapshot(t, env, 1)
	engine := newFakeWriterEngine()
	require.NoError(t, engine.CreateTree(context.Background(), writerTree, treeTypeUnilevel))
	w := NewTreeWriter(env.events, env.store, env.snapshots, engine, env.locker)

	_, err := w.Load(context.Background(), loadRequest())

	require.ErrorContains(t, err, "load tree "+writerTree+"; nothing was appended: ")
	require.ErrorContains(t, err, engineCodeTreeExists)
	requireReplayStage(t, err, TreeReplayStageRestore)
	assert.NotContains(t, engine.calls, "drop_structure")
}

func TestTreeWriterSnapshotLoad_RecordsTheSnapshotVersionForTheWrite(t *testing.T) {
	cases := map[string]struct {
		seed int64
		want int64
	}{
		"a usable snapshot":     {seed: 2, want: 2},
		"a snapshot it deletes": {seed: 9, want: 0},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			env := newWriterEnv()
			mustAddRoot(t, env, treeTypeUnilevel)
			mustPlace(t, env, writerChild, nil)
			seedSnapshot(t, env, tc.seed)
			w, _ := env.writer()

			prep, err := w.prepare(context.Background(), writerTree, treeShape{treeType: treeTypeUnilevel})

			require.NoError(t, err)
			assert.Equal(t, tc.want, prep.snapshotVersion)
		})
	}
}
