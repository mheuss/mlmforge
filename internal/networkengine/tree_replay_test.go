package networkengine

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/mlmforge/mlmforge/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// replayEnv is a unilevel stream on writerTree and a fresh fake engine.
type replayEnv struct {
	events *platform.MemoryEventStore
	engine *fakeWriterEngine
}

func newReplayEnv(t *testing.T) *replayEnv {
	t.Helper()
	env := &replayEnv{events: platform.NewMemoryEventStore(), engine: newFakeWriterEngine()}
	appendDirect(t, env.events, EventTypeRootAdded, RootAddedPayload{
		TreeID: writerTree, UserID: writerRoot, SponsorID: writerRoot, EnrolledAt: writeTime, TreeType: treeTypeUnilevel,
	})
	require.NoError(t, env.engine.CreateTree(context.Background(), writerTree, treeTypeUnilevel))
	return env
}

func (e *replayEnv) place(t *testing.T, user string) platform.Event {
	t.Helper()
	return appendDirect(t, e.events, EventTypeNodePlaced, childPlacedPayload(user))
}

func (e *replayEnv) reject(t *testing.T, target platform.Event) platform.Event {
	t.Helper()
	return appendDirect(t, e.events, EventTypeEventRejected, EventRejectedPayload{
		TreeID: writerTree, RejectedEventID: target.ID, RejectedVersion: target.Version,
		RejectedType: target.Type, Reason: "test",
	})
}

func (e *replayEnv) replay(from, to int64) (replayResult, error) {
	return replayStream(context.Background(), e.events, e.engine, writerTree, unilevelShape, from, to)
}

func (e *replayEnv) users() []string {
	t := e.engine.trees[writerTree]
	var out []string
	for id := range t.nodes {
		out = append(out, id)
	}
	return out
}

func requireReplayInvariant(t *testing.T, err error, expected, observed string) {
	t.Helper()
	var replay *TreeReplayError
	require.ErrorAs(t, err, &replay)
	assert.Equal(t, TreeReplayStageReplay, replay.Stage)
	assert.Equal(t, expected, replay.Expected)
	assert.Equal(t, observed, replay.Observed)
}

func TestReplayStream_AppliesEveryEventThroughTo(t *testing.T) {
	env := newReplayEnv(t)
	env.place(t, writerChild)
	env.place(t, writerOther)

	res, err := env.replay(1, 3)

	require.NoError(t, err)
	assert.Equal(t, replayResult{reached: 3, root: writerRoot}, res)
	assert.ElementsMatch(t, []string{writerRoot, writerChild, writerOther}, env.users())
}

func TestReplayStream_StopsAtTo(t *testing.T) {
	env := newReplayEnv(t)
	env.place(t, writerChild)
	env.place(t, writerOther)

	res, err := env.replay(1, 2)

	require.NoError(t, err)
	assert.Equal(t, int64(2), res.reached)
	assert.ElementsMatch(t, []string{writerRoot, writerChild}, env.users())
}

func TestReplayStream_MakesNoCallsForAnEmptyRange(t *testing.T) {
	env := newReplayEnv(t)
	env.engine = newFakeWriterEngine()

	res, err := env.replay(1, 0)

	require.NoError(t, err)
	assert.Equal(t, replayResult{reached: 0}, res)
	assert.Empty(t, env.engine.trees)
}

func TestReplayStream_SkipsAnEventARejectionInRangeNames(t *testing.T) {
	env := newReplayEnv(t)
	child := env.place(t, writerChild)
	env.reject(t, child)
	env.place(t, writerOther)

	res, err := env.replay(1, 4)

	require.NoError(t, err)
	assert.Equal(t, int64(4), res.reached)
	assert.ElementsMatch(t, []string{writerRoot, writerOther}, env.users())
}

func TestReplayStream_AppliesAnEventWhoseRejectionIsPastTo(t *testing.T) {
	env := newReplayEnv(t)
	child := env.place(t, writerChild)
	env.reject(t, child)

	_, err := env.replay(1, 2)

	require.NoError(t, err)
	assert.ElementsMatch(t, []string{writerRoot, writerChild}, env.users())
}

func TestReplayStream_LooksAheadAcrossAPageBoundary(t *testing.T) {
	env := newReplayEnv(t)
	var last platform.Event
	for i := 0; i < replayPageSize-1; i++ {
		last = env.place(t, testUserUUID(1000+i))
	}
	require.Equal(t, int64(replayPageSize), last.Version)
	env.reject(t, last)

	res, err := env.replay(1, replayPageSize+1)

	require.NoError(t, err)
	assert.Equal(t, int64(replayPageSize+1), res.reached)
	_, held := env.engine.trees[writerTree].nodes[testUserUUID(1000+replayPageSize-2)]
	assert.False(t, held, "the event the rejection on the next page names was applied")
}

func TestReplayStream_RefusesARejectionNamingAnEventItDidNotRead(t *testing.T) {
	env := newReplayEnv(t)
	child := env.place(t, writerChild)
	env.reject(t, child)

	_, err := env.replay(3, 3)

	requireReplayInvariant(t, err, "a rejection of an event this replay read",
		"rejection at version 3 names version 2, which this replay did not read")
}

func TestReplayStream_RefusesARejectionWhoseTargetDiffers(t *testing.T) {
	env := newReplayEnv(t)
	child := env.place(t, writerChild)
	rejectedID := uuid.NewString()
	appendDirect(t, env.events, EventTypeEventRejected, EventRejectedPayload{
		TreeID: writerTree, RejectedEventID: rejectedID, RejectedVersion: 2,
		RejectedType: EventTypeNodePlaced, Reason: "test",
	})

	_, err := env.replay(1, 3)

	requireReplayInvariant(t, err, "event "+child.ID+" (tree.node_placed) at version 2",
		"event "+rejectedID+" (tree.node_placed) at version 2")
}

func TestReplayStream_RefusesToEndShortOfTo(t *testing.T) {
	env := newReplayEnv(t)
	env.place(t, writerChild)

	_, err := env.replay(1, 5)

	requireReplayInvariant(t, err, "events through version 5", "events through version 2")
}

// gappedEvents drops one version from every read.
type gappedEvents struct {
	*platform.MemoryEventStore
	drop int64
}

func (g *gappedEvents) ReadStream(ctx context.Context, stream string, from, limit int64) ([]platform.Event, error) {
	got, err := g.MemoryEventStore.ReadStream(ctx, stream, from, limit)
	var out []platform.Event
	for _, ev := range got {
		if ev.Version != g.drop {
			out = append(out, ev)
		}
	}
	return out, err
}

func TestReplayStream_RefusesAGapInVersions(t *testing.T) {
	env := newReplayEnv(t)
	env.place(t, writerChild)
	env.place(t, writerOther)

	_, err := replayStream(context.Background(), &gappedEvents{MemoryEventStore: env.events, drop: 2},
		env.engine, writerTree, unilevelShape, 1, 3)

	requireReplayInvariant(t, err, "version 2", "version 3")
}

// failingReadEvents fails every ReadStream from version from, and only reads
// of size limit when limit is set.
type failingReadEvents struct {
	*platform.MemoryEventStore
	from  int64
	limit int64
	err   error
}

func (e *failingReadEvents) ReadStream(ctx context.Context, stream string, from, limit int64) ([]platform.Event, error) {
	if from == e.from && (e.limit == 0 || limit == e.limit) {
		return nil, e.err
	}
	return e.MemoryEventStore.ReadStream(ctx, stream, from, limit)
}

func TestReplayStream_ChecksAnEventARejectionNames(t *testing.T) {
	one := 1
	wrongShape := childPlacedPayload(writerChild)
	wrongShape.TreeType, wrongShape.Position = treeTypeBinary, &one
	foreign := childPlacedPayload(writerChild)
	foreign.TreeID = testTreeUUID(2)
	cases := map[string]struct {
		eventType string
		payload   any
	}{
		"a payload for another tree":   {EventTypeNodePlaced, foreign},
		"a placement of another shape": {EventTypeNodePlaced, wrongShape},
		"an unknown type":              {"tree.something", map[string]string{"tree_id": writerTree}},
		"a malformed payload":          {EventTypeNodePlaced, "not an object"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			env := newReplayEnv(t)
			bad := appendDirect(t, env.events, tc.eventType, tc.payload)
			env.reject(t, bad)

			_, err := env.replay(1, 3)

			var replay *TreeReplayError
			require.ErrorAs(t, err, &replay)
			assert.Equal(t, TreeReplayStageReplay, replay.Stage)
			assert.Equal(t, int64(2), replay.FailedVersion)
		})
	}
}

func TestReplayStream_ReportsAFailedStreamRead(t *testing.T) {
	env := newReplayEnv(t)
	env.place(t, writerChild)
	cause := errors.New("connection reset")

	_, err := replayStream(context.Background(), &failingReadEvents{MemoryEventStore: env.events, from: 1, err: cause},
		env.engine, writerTree, unilevelShape, 1, 2)

	var replay *TreeReplayError
	require.ErrorAs(t, err, &replay)
	assert.Equal(t, TreeReplayStageReplay, replay.Stage)
	assert.ErrorIs(t, err, cause)
}

func TestReplayStream_RefusesAPayloadForAnotherTree(t *testing.T) {
	env := newReplayEnv(t)
	payload := childPlacedPayload(writerChild)
	payload.TreeID = testTreeUUID(2)
	appendDirect(t, env.events, EventTypeNodePlaced, payload)

	_, err := env.replay(1, 2)

	requireReplayInvariant(t, err, "tree_id "+writerTree, "tree_id "+testTreeUUID(2))
}

func TestReplayStream_RefusesAPlacementOfAnotherShape(t *testing.T) {
	env := newReplayEnv(t)
	one := 1
	payload := childPlacedPayload(writerChild)
	payload.TreeType, payload.Position = treeTypeBinary, &one
	appendDirect(t, env.events, EventTypeNodePlaced, payload)

	_, err := env.replay(1, 2)

	requireReplayInvariant(t, err, "tree type unilevel", "tree type binary")
}

func TestReplayStream_RefusesAnUnknownEventType(t *testing.T) {
	env := newReplayEnv(t)
	appendDirect(t, env.events, "tree.something", map[string]string{"tree_id": writerTree})

	_, err := env.replay(1, 2)

	requireReplayInvariant(t, err, "a tree event type", `type "tree.something"`)
}

func TestReplayStream_ReportsTheEventAnEngineRefused(t *testing.T) {
	env := newReplayEnv(t)
	env.place(t, writerChild)
	refusal := fakeEngineError("SPONSOR_NOT_FOUND", "refused")
	env.engine.failAdd[writerChild] = refusal

	_, err := env.replay(1, 2)

	var replay *TreeReplayError
	require.ErrorAs(t, err, &replay)
	assert.Equal(t, int64(2), replay.FailedVersion)
	assert.Equal(t, int64(1), replay.ReachedVersion)
	var engineErr *EngineError
	assert.True(t, errors.As(err, &engineErr), "the engine's refusal is not reachable through errors.As")
}

func TestReplayStream_LeavesNoRootAfterTheRootIsRemoved(t *testing.T) {
	env := newReplayEnv(t)
	appendDirect(t, env.events, EventTypeNodeRemoved, NodeRemovedPayload{
		TreeID: writerTree, UserID: writerRoot, RemovedAt: writeTime,
	})

	res, err := env.replay(1, 2)

	require.NoError(t, err)
	assert.Empty(t, res.root)
}

func TestReplayStream_RefusesAMatrixRemoval(t *testing.T) {
	events := platform.NewMemoryEventStore()
	engine := newFakeWriterEngine()
	width, spillover := 3, "breadth_first"
	appendDirect(t, events, EventTypeRootAdded, RootAddedPayload{
		TreeID: writerTree, UserID: writerRoot, SponsorID: writerRoot, EnrolledAt: writeTime,
		TreeType: treeTypeMatrix, MatrixWidth: &width, MatrixSpillover: &spillover,
	})
	appendDirect(t, events, EventTypeNodeRemoved, NodeRemovedPayload{TreeID: writerTree, UserID: writerChild, RemovedAt: writeTime})
	require.NoError(t, engine.CreateMatrixTree(context.Background(), writerTree, 3, "breadth_first"))

	_, err := replayStream(context.Background(), events, engine, writerTree,
		treeShape{treeType: treeTypeMatrix, width: 3, spillover: "breadth_first"}, 1, 2)

	require.ErrorContains(t, err, "the caller names the tree matrix, and the payload carries no pruning mode")
}

func TestEngineCalls_ConsumerAndReplayLeaveTheSameEngineState(t *testing.T) {
	env := newReplayEnv(t)
	env.place(t, writerChild)
	env.place(t, writerOther)
	appendDirect(t, env.events, EventTypeNodeRemoved, NodeRemovedPayload{
		TreeID: writerTree, UserID: writerOther, RemovedAt: writeTime,
	})
	stream := streamEvents(t, env.events, TreeStreamName(writerTree))

	consumed := newFakeWriterEngine()
	require.NoError(t, consumed.CreateTree(context.Background(), writerTree, treeTypeUnilevel))
	consumer := NewTreeEventConsumer(NewMemoryTreeStore(), consumed)
	for _, ev := range stream {
		require.NoError(t, consumer.HandleEvent(context.Background(), ev), "event at version %d", ev.Version)
	}
	_, err := env.replay(1, int64(len(stream)))
	require.NoError(t, err)

	assert.Equal(t, consumed.trees[writerTree], env.engine.trees[writerTree])
}
