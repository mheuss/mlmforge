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

	requireReplayInvariant(t, err, "a held event for the rejection to name",
		"rejection at version 3 names version 2; no event is held")
}

func TestReplayStream_RefusesARejectionOfARejection(t *testing.T) {
	env := newReplayEnv(t)
	child := env.place(t, writerChild)
	first := env.reject(t, child)
	env.reject(t, first)

	_, err := env.replay(1, 4)

	requireReplayInvariant(t, err, "a held event for the rejection to name",
		"rejection at version 4 names version 3; no event is held")
}

func TestReplayStream_RefusesARejectionWhoseVersionOrTypeDiffers(t *testing.T) {
	cases := map[string]func(p *EventRejectedPayload){
		"another version": func(p *EventRejectedPayload) { p.RejectedVersion = 1 },
		"another type":    func(p *EventRejectedPayload) { p.RejectedType = EventTypeNodeRemoved },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			env := newReplayEnv(t)
			child := env.place(t, writerChild)
			p := EventRejectedPayload{TreeID: writerTree, RejectedEventID: child.ID, RejectedVersion: child.Version,
				RejectedType: child.Type, Reason: "test"}
			mutate(&p)
			appendDirect(t, env.events, EventTypeEventRejected, p)

			_, err := env.replay(1, 3)

			var replay *TreeReplayError
			require.ErrorAs(t, err, &replay)
			assert.Equal(t, TreeReplayStageReplay, replay.Stage)
			assert.Equal(t, "event "+child.ID+" (tree.node_placed) at version 2", replay.Expected)
		})
	}
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

func TestReplayStream_SkipsARejectedEventWithABadPayload(t *testing.T) {
	one := 1
	wrongShape := childPlacedPayload(writerChild)
	wrongShape.TreeType, wrongShape.Position = treeTypeBinary, &one
	foreign := childPlacedPayload(writerChild)
	foreign.TreeID = testTreeUUID(2)
	unilevelWithPosition := childPlacedPayload(writerChild)
	unilevelWithPosition.Position = &one
	cases := map[string]any{
		"a payload for another tree":             foreign,
		"a placement of another shape":           wrongShape,
		"a placement that fails its shape check": unilevelWithPosition,
		"a malformed payload":                    "not an object",
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			env := newReplayEnv(t)
			bad := appendDirect(t, env.events, EventTypeNodePlaced, payload)
			env.reject(t, bad)

			res, err := env.replay(1, 3)

			require.NoError(t, err)
			assert.Equal(t, int64(3), res.reached)
			assert.ElementsMatch(t, []string{writerRoot}, env.users())
		})
	}
}

func TestReplayStream_RefusesARejectedUnknownType(t *testing.T) {
	env := newReplayEnv(t)
	bad := appendDirect(t, env.events, "tree.something", map[string]string{"tree_id": writerTree})
	env.reject(t, bad)

	_, err := env.replay(1, 3)

	requireReplayInvariant(t, err, "a tree event type", `type "tree.something"`)
}

func TestReplayStream_ReplaysAStreamAfterRejectEventSkipsABadPlacement(t *testing.T) {
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
	require.NoError(t, rejected.ProjectionErr)
	engine := newFakeWriterEngine()
	require.NoError(t, engine.CreateTree(context.Background(), writerTree, treeTypeUnilevel))

	res, err := replayStream(context.Background(), env.events, engine, writerTree, unilevelShape, 1, 3)

	require.NoError(t, err)
	assert.Equal(t, int64(3), res.reached)
	assert.Equal(t, writerRoot, res.root)
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
	assert.Equal(t, int64(0), replay.ReachedVersion)
	assert.ErrorIs(t, err, cause)
	assert.Empty(t, env.users(), "the engine was changed before the failed read")
}

// unlimitedEvents ignores the limit on every read.
type unlimitedEvents struct {
	*platform.MemoryEventStore
}

func (e unlimitedEvents) ReadStream(ctx context.Context, stream string, from, _ int64) ([]platform.Event, error) {
	return e.MemoryEventStore.ReadStream(ctx, stream, from, 0)
}

func TestReplayStream_RefusesAnEventPastTo(t *testing.T) {
	env := newReplayEnv(t)
	env.place(t, writerChild)
	env.place(t, writerOther)

	_, err := replayStream(context.Background(), unlimitedEvents{env.events}, env.engine, writerTree, unilevelShape, 1, 2)

	requireReplayInvariant(t, err, "events through version 2", "event at version 3")
	assert.NotContains(t, env.users(), writerOther)
}

func TestReplayStream_ReportsAnEngineRefusalBeforeTheLastEvent(t *testing.T) {
	env := newReplayEnv(t)
	env.place(t, writerChild)
	env.place(t, writerOther)
	env.engine.failAdd[writerChild] = fakeEngineError("SPONSOR_NOT_FOUND", "refused")

	_, err := env.replay(1, 3)

	var replay *TreeReplayError
	require.ErrorAs(t, err, &replay)
	assert.Equal(t, int64(2), replay.FailedVersion)
	assert.Equal(t, int64(1), replay.ReachedVersion)
	assert.NotContains(t, env.users(), writerOther)
}

func TestReplayStream_StartsPartWayThroughAStream(t *testing.T) {
	env := newReplayEnv(t)
	env.place(t, writerChild)
	env.place(t, writerOther)
	require.NoError(t, env.engine.AddRoot(context.Background(), writerTree, writerRoot, writeTime.Unix()))

	res, err := env.replay(2, 3)

	require.NoError(t, err)
	assert.Equal(t, int64(3), res.reached)
	assert.ElementsMatch(t, []string{writerRoot, writerChild, writerOther}, env.users())
}

func TestReplayStream_ReportsReachedFromAPartWayStart(t *testing.T) {
	env := newReplayEnv(t)
	env.place(t, writerChild)
	env.place(t, writerOther)
	require.NoError(t, env.engine.AddRoot(context.Background(), writerTree, writerRoot, writeTime.Unix()))
	env.engine.failAdd[writerOther] = fakeEngineError("SPONSOR_NOT_FOUND", "refused")

	_, err := env.replay(2, 3)

	var replay *TreeReplayError
	require.ErrorAs(t, err, &replay)
	assert.Equal(t, int64(2), replay.ReachedVersion)
	assert.Equal(t, int64(3), replay.FailedVersion)
}

func TestReplayStream_LeavesNoRootWhenTheRootIsRejected(t *testing.T) {
	events := platform.NewMemoryEventStore()
	root := appendDirect(t, events, EventTypeRootAdded, RootAddedPayload{
		TreeID: writerTree, UserID: writerRoot, SponsorID: writerRoot, EnrolledAt: writeTime, TreeType: treeTypeUnilevel,
	})
	appendDirect(t, events, EventTypeEventRejected, EventRejectedPayload{
		TreeID: writerTree, RejectedEventID: root.ID, RejectedVersion: 1, RejectedType: EventTypeRootAdded, Reason: "test",
	})
	engine := newFakeWriterEngine()
	require.NoError(t, engine.CreateTree(context.Background(), writerTree, treeTypeUnilevel))

	res, err := replayStream(context.Background(), events, engine, writerTree, unilevelShape, 1, 2)

	require.NoError(t, err)
	assert.Equal(t, replayResult{reached: 2}, res)
	assert.Empty(t, engine.trees[writerTree].nodes, "the rejected root reached the engine")
}

func TestReplayStream_ReportsTheStartWhenTheFirstEventFails(t *testing.T) {
	env := newReplayEnv(t)
	env.place(t, writerChild)
	env.place(t, writerOther)
	require.NoError(t, env.engine.AddRoot(context.Background(), writerTree, writerRoot, writeTime.Unix()))
	env.engine.failAdd[writerChild] = fakeEngineError("SPONSOR_NOT_FOUND", "refused")

	_, err := env.replay(2, 3)

	var replay *TreeReplayError
	require.ErrorAs(t, err, &replay)
	assert.Equal(t, int64(1), replay.ReachedVersion)
	assert.Equal(t, int64(2), replay.FailedVersion)
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
	placed := appendDirect(t, env.events, EventTypeNodePlaced, payload)

	_, err := env.replay(1, 2)

	requireReplayInvariant(t, err, "tree type unilevel", "tree type binary")
	var replay *TreeReplayError
	require.ErrorAs(t, err, &replay)
	assert.True(t, sameUUID(placed.ID, replay.FailedEventID), "replay named event %q, not %s", replay.FailedEventID, placed.ID)
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
	placed := streamEvents(t, env.events, TreeStreamName(writerTree))[1]
	assert.True(t, sameUUID(placed.ID, replay.FailedEventID), "replay named event %q, not %s", replay.FailedEventID, placed.ID)
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

func TestReplayStream_ReplaysAMatrixRemoval(t *testing.T) {
	events, engine, shape := removalStream(t, treeTypeMatrix)
	appendDirect(t, events, EventTypeNodeRemoved, NodeRemovedPayload{TreeID: writerTree, UserID: writerChild,
		RemovedAt: writeTime, TreeType: treeTypeMatrix, PruningMode: PruningHoldingTank})

	res, err := replayStream(context.Background(), events, engine, writerTree, shape, 1, 3)

	require.NoError(t, err)
	assert.Equal(t, int64(3), res.reached)
	assert.NotContains(t, engine.trees[writerTree].nodes, writerChild)
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
		require.NoError(t, deliver(context.Background(), consumer, ev), "event at version %d", ev.Version)
	}
	_, err := env.replay(1, int64(len(stream)))
	require.NoError(t, err)

	assert.Equal(t, consumed.trees[writerTree], env.engine.trees[writerTree])
}

// removalStream appends a root and one child placement in a tree of
// streamType. It returns the stream, an engine holding the created tree, and
// the stream's shape.
func removalStream(t *testing.T, streamType string) (platform.EventStore, *fakeWriterEngine, treeShape) {
	t.Helper()
	ctx := context.Background()
	events := platform.NewMemoryEventStore()
	engine := newFakeWriterEngine()
	root := RootAddedPayload{TreeID: writerTree, UserID: writerRoot, SponsorID: writerRoot,
		EnrolledAt: writeTime, TreeType: streamType}
	shape := treeShape{treeType: streamType}
	var position *int
	switch streamType {
	case treeTypeMatrix:
		width, spillover := 3, "breadth_first"
		root.MatrixWidth, root.MatrixSpillover = &width, &spillover
		shape.width, shape.spillover = width, spillover
		require.NoError(t, engine.CreateMatrixTree(ctx, writerTree, width, spillover))
		position = intPtr(0)
	case treeTypeBinary:
		require.NoError(t, engine.CreateTree(ctx, writerTree, streamType))
		position = intPtr(0)
	default:
		require.NoError(t, engine.CreateTree(ctx, writerTree, streamType))
	}
	appendDirect(t, events, EventTypeRootAdded, root)
	appendDirect(t, events, EventTypeNodePlaced, NodePlacedPayload{TreeID: writerTree, UserID: writerChild,
		ParentID: writerRoot, SponsorID: writerRoot, Position: position, TreeType: streamType, EnrolledAt: writeTime})
	return events, engine, shape
}

func TestReplayStream_RefusesANodeRemovedItsStreamDisagreesWith(t *testing.T) {
	cases := []struct {
		name     string
		stream   string
		treeType string
		mode     PruningMode
		want     string
	}{
		{"a mode on a unilevel stream", treeTypeUnilevel, "", PruningPromoteEarliest, `carries pruning_mode "promote_earliest"`},
		{"a mode with tree_type unilevel", treeTypeUnilevel, treeTypeUnilevel, PruningHoldingTank, `carries pruning_mode "holding_tank"`},
		{"tree_type binary on a unilevel stream", treeTypeUnilevel, treeTypeBinary, "",
			`has tree_type "binary", and the stream records tree type unilevel`},
		{"tree_type binary and a mode on a unilevel stream", treeTypeUnilevel, treeTypeBinary, PruningPromoteEarliest,
			`has tree_type "binary", and the stream records tree type unilevel`},
		{"a matrix removal with no mode", treeTypeMatrix, treeTypeMatrix, "", "has no pruning_mode"},
		{"a matrix removal with an unknown mode", treeTypeMatrix, treeTypeMatrix, "prune_all", `has unsupported pruning_mode "prune_all"`},
		{"no tree_type on a matrix stream", treeTypeMatrix, "", "", "has no tree_type, and the stream records tree type matrix"},
		{"no tree_type and a mode on a matrix stream", treeTypeMatrix, "", PruningPromoteEarliest,
			"has no tree_type, and the stream records tree type matrix"},
		{"tree_type unilevel on a matrix stream", treeTypeMatrix, treeTypeUnilevel, "",
			`has tree_type "unilevel", and the stream records tree type matrix`},
		{"an unknown tree_type", treeTypeUnilevel, "ternary", "", `has unsupported tree_type "ternary"`},
		{"tree_type unilevel on a binary stream", treeTypeBinary, treeTypeUnilevel, "",
			`has tree_type "unilevel", and the stream records tree type binary`},
		{"a matrix removal on a binary stream", treeTypeBinary, treeTypeMatrix, PruningHoldingTank,
			`has tree_type "matrix", and the stream records tree type binary`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			events, engine, shape := removalStream(t, tc.stream)
			appendDirect(t, events, EventTypeNodeRemoved, NodeRemovedPayload{TreeID: writerTree, UserID: writerChild,
				RemovedAt: writeTime, TreeType: tc.treeType, PruningMode: tc.mode})

			_, err := replayStream(context.Background(), events, engine, writerTree, shape, 1, 3)

			var replayErr *TreeReplayError
			require.ErrorAs(t, err, &replayErr)
			assert.Equal(t, TreeReplayStageReplay, replayErr.Stage)
			assert.Equal(t, int64(3), replayErr.FailedVersion)
			assert.ErrorContains(t, err, "event at version 3 of type "+EventTypeNodeRemoved+" failed")
			assert.ErrorContains(t, err, tc.want)
		})
	}
}
