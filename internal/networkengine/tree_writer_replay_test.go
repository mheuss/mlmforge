package networkengine

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// placementUsers returns the user each placement call named, in call order.
func placementUsers(engine *fakeWriterEngine) []string {
	var out []string
	for _, m := range engine.placements {
		out = append(out, m.params["user_id"].(string))
	}
	return out
}

func TestTreeWriter_LoadBuildsTheEngineInStreamOrder(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	w, _ := env.writer()
	_, err := w.Place(context.Background(), PlaceRequest{TreeID: writerTree, UserID: writerChild,
		ParentID: writerRoot, SponsorID: writerRoot, EnrolledAt: writeTime.Add(2 * time.Hour)})
	require.NoError(t, err)
	w, _ = env.writer()
	_, err = w.Place(context.Background(), PlaceRequest{TreeID: writerTree, UserID: writerOther,
		ParentID: writerRoot, SponsorID: writerRoot, EnrolledAt: writeTime.Add(time.Hour)})
	require.NoError(t, err)

	w, engine := env.writer()
	res, err := w.Load(context.Background(), loadRequest())

	require.NoError(t, err)
	assert.Equal(t, 3, res.Nodes)
	assert.Equal(t, []string{writerChild, writerOther}, placementUsers(engine))
}

// writeCountingStore fails the test on any store write.
type writeCountingStore struct {
	TreeStore
	t *testing.T
}

func (s writeCountingStore) ProjectInsert(context.Context, TreeNodeRow, int64) error {
	s.t.Error("the load wrote a row")
	return nil
}

func (s writeCountingStore) ProjectRemoval(context.Context, string, string, string, int64, []Responsored) error {
	s.t.Error("the load wrote a removal")
	return nil
}

func (s writeCountingStore) ProjectRejection(context.Context, string, string, int64) error {
	s.t.Error("the load wrote a rejection")
	return nil
}

func (s writeCountingStore) UndoRootProjection(context.Context, string, string, int64) error {
	s.t.Error("the load undid a root")
	return nil
}

func (s writeCountingStore) InsertNode(context.Context, TreeNodeRow) error {
	s.t.Error("the load inserted a row")
	return nil
}

func (s writeCountingStore) DeleteNode(context.Context, string, string) error {
	s.t.Error("the load deleted a row")
	return nil
}

func (s writeCountingStore) DeleteNodeAndResponsor(context.Context, string, string, string, []Responsored) error {
	s.t.Error("the load deleted a row and repointed sponsors")
	return nil
}

func (s writeCountingStore) BulkInsert(context.Context, []TreeNodeRow) error {
	s.t.Error("the load bulk-inserted rows")
	return nil
}

func TestTreeWriter_ALoadWithNothingBehindWritesNothingToTheStore(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	mustPlace(t, env, writerChild, nil)
	w := NewTreeWriter(env.events, writeCountingStore{TreeStore: env.store, t: t}, newFakeWriterEngine(), env.locker)

	_, err := w.Load(context.Background(), loadRequest())

	require.NoError(t, err)
}

func TestTreeWriter_LoadFailsWhenTheStoreHoldsARowTheStreamLacks(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	require.NoError(t, env.store.InsertNode(context.Background(), TreeNodeRow{
		ID: testNodeUUID(9), TreeID: writerTree, UserID: writerOther,
		ParentID: &writerRoot, SponsorID: &writerRoot, Depth: 1, EnrolledAt: writeTime,
	}))
	w, _ := env.writer()

	_, err := w.Load(context.Background(), loadRequest())

	var replay *TreeReplayError
	require.ErrorAs(t, err, &replay)
	assert.Equal(t, TreeReplayStageMembership, replay.Stage)
	assert.Contains(t, replay.Mismatch, "the engine holds 1 nodes under root "+writerRoot+"; the store holds 2 active rows")
	assert.Contains(t, replay.Mismatch, "only in the store: ["+writerOther+"]")
}

func TestTreeWriter_ALoadAfterTheRootIsRemovedLeavesAnEmptyTree(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	w, _ := env.writer()
	res, err := w.Remove(context.Background(), RemoveRequest{TreeID: writerTree, UserID: writerRoot, RemovedAt: writeTime})
	require.NoError(t, err)
	require.NoError(t, res.ProjectionErr)

	w, _ = env.writer()
	loaded, err := w.Load(context.Background(), loadRequest())

	require.NoError(t, err)
	assert.Zero(t, loaded.Nodes)
}

func TestTreeWriter_ALoadAppliesAnEventWhoseRejectionIsPending(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	child := mustPlace(t, env, writerChild, nil)
	appendDirect(t, env.events, EventTypeEventRejected, EventRejectedPayload{
		TreeID: writerTree, RejectedEventID: child.EventID, RejectedVersion: child.Version,
		RejectedType: EventTypeNodePlaced, Reason: "test",
	})
	w, engine := env.writer()

	_, err := w.Load(context.Background(), loadRequest())

	var pending *RejectionPendingError
	require.ErrorAs(t, err, &pending)
	_, held := engine.trees[writerTree].nodes[writerChild]
	assert.True(t, held, "the event at the projected version was not applied")
}

func TestTreeWriter_ALoadSkipsABadPlacementRejectEventRejected(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	one := 1
	bad := childPlacedPayload(writerChild)
	bad.Position = &one
	stuck := appendDirect(t, env.events, EventTypeNodePlaced, bad)
	w, _ := env.writer()
	rejected, err := w.Reject(context.Background(), rejectRequest(stuck.ID))
	require.NoError(t, err)
	require.Equal(t, RejectOutcomeRejected, rejected.Outcome)

	w, _ = env.writer()
	res, err := w.Load(context.Background(), loadRequest())

	require.NoError(t, err)
	assert.Equal(t, 1, res.Nodes)
}

func TestTreeWriter_APreflightFailureBeforeAPendingRejectionReportsThePendingRejection(t *testing.T) {
	env := newWriterEnv()
	stuck := stuckPlacement(t, env)
	appendDirect(t, env.events, EventTypeEventRejected, EventRejectedPayload{
		TreeID: writerTree, RejectedEventID: stuck.ID, RejectedVersion: stuck.Version,
		RejectedType: stuck.Type, Reason: "test",
	})
	w, _ := env.writer()

	_, err := w.Load(context.Background(), loadRequest())

	var pending *RejectionPendingError
	require.ErrorAs(t, err, &pending)
}

func TestTreeWriter_AFailedMatrixCreateIsACreateStageFailure(t *testing.T) {
	env := newWriterEnv()
	engine := newFakeWriterEngine()
	require.NoError(t, engine.CreateMatrixTree(context.Background(), writerTree, 3, "breadth_first"))
	w := NewTreeWriter(env.events, env.store, engine, env.locker)
	width, spillover := 3, "breadth_first"

	_, err := w.Load(context.Background(), LoadRequest{
		TreeID: writerTree, TreeType: treeTypeMatrix, MatrixWidth: &width, MatrixSpillover: &spillover,
	})

	var replay *TreeReplayError
	require.ErrorAs(t, err, &replay)
	assert.Equal(t, TreeReplayStageCreate, replay.Stage)
	assert.ErrorContains(t, err, "TREE_EXISTS")
}
