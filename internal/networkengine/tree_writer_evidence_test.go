package networkengine

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/mlmforge/mlmforge/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var unilevelShape = treeShape{treeType: treeTypeUnilevel}

// caughtUpFailure wraps err the way catch-up reports a failed redelivery of last.
func caughtUpFailure(last platform.Event, err error) error {
	return &CatchUpFailedError{TreeID: writerTree, EventID: last.ID, Version: last.Version, Type: last.Type, Err: err}
}

func TestRejectionEvidence_CountsWhatShowsTheEventCannotApply(t *testing.T) {
	w, _ := newWriterEnv().writer()
	last := platform.Event{ID: testNodeUUID(5), Type: EventTypeNodePlaced, Version: 2}
	counted := map[string]error{
		"a replayed placement":        fmt.Errorf("a: %w", ErrReplayedPlacement),
		"another event's active user": fmt.Errorf("store placed node: %w", ErrActiveUserConflict),
		"another event's slot":        fmt.Errorf("store placed node: %w", ErrSlotConflict),
		"another event's root":        fmt.Errorf("store root node: %w", ErrRootConflict),
		"an unprojectable event":      unprojectable(errors.New("unmarshal node_placed payload: bad")),
	}
	for _, code := range []string{
		"POSITION_OCCUPIED", "INVALID_POSITION", "HAS_CHILDREN", "CANNOT_REMOVE_ROOT", "TREE_EMPTY",
		"SPONSOR_NOT_FOUND", "SUBTREE_FULL", "SPONSORLESS_WITH_RECRUITS", "SPONSOR_CYCLE",
	} {
		counted["engine "+code] = fmt.Errorf("engine add_node failed after 2 retries: %w", fakeEngineError(code, "refused"))
	}
	for name, err := range counted {
		t.Run(name, func(t *testing.T) {
			got, gerr := w.rejectionEvidence(context.Background(), writerTree, unilevelShape, last, caughtUpFailure(last, err))

			require.NoError(t, gerr)
			assert.True(t, got)
		})
	}
}

func TestRejectionEvidence_RefusesWhatSaysNothingAboutTheEvent(t *testing.T) {
	w, _ := newWriterEnv().writer()
	last := platform.Event{ID: testNodeUUID(5), Type: EventTypeNodePlaced, Version: 2}
	refused := map[string]error{
		"a cancelled retry":         caughtUpFailure(last, fmt.Errorf("engine add_node cancelled during retry: %w", context.Canceled)),
		"a retry past its deadline": caughtUpFailure(last, context.DeadlineExceeded),
		"a cancelled retry that also carries a refusal code": caughtUpFailure(last,
			errors.Join(context.Canceled, fakeEngineError("SPONSOR_NOT_FOUND", "refused"))),
		"a store error":                     caughtUpFailure(last, errors.New("store placed node: connection reset by peer")),
		"a transport error":                 caughtUpFailure(last, errors.New("engine add_node failed after 2 retries: write |1: broken pipe")),
		"an untyped error outside catch-up": errors.New("read the last event of stream x; nothing was appended: connection reset"),
		"an incomplete load carrying a refusal code": &TreeLoadIncompleteError{
			TreeID: writerTree, Stage: TreeLoadStageNodes, Err: fakeEngineError("SPONSOR_NOT_FOUND", "refused"),
		},
		"a load whose store read failed": &TreeLoadRejectedError{
			TreeID: writerTree, Kind: TreeLoadStoreReadFailed, Err: errors.New("connection reset"),
		},
		"a fence refusal": &StreamMovedError{TreeID: writerTree, LoadedVersion: 1, LastVersion: 3},
	}
	for _, code := range []string{
		"USER_ALREADY_EXISTS", "ROOT_ALREADY_EXISTS", "USER_NOT_FOUND", "INTERNAL_ERROR",
		"INVALID_REQUEST", "INVALID_PARAMS", "MISSING_PARAM", "INVALID_UUID", "UNKNOWN_OP", "UNSUPPORTED_OP",
		"STRUCTURE_NOT_FOUND", "TREE_EXISTS", "INVALID_WIDTH", "UNSUPPORTED_SPILLOVER", "CALCULATION_ERROR",
	} {
		refused["engine "+code] = caughtUpFailure(last,
			fmt.Errorf("engine add_node failed after 2 retries: %w", fakeEngineError(code, "refused")))
	}
	for name, err := range refused {
		t.Run(name, func(t *testing.T) {
			got, gerr := w.rejectionEvidence(context.Background(), writerTree, unilevelShape, last, err)

			require.NoError(t, gerr)
			assert.False(t, got)
		})
	}
}

func TestRejectionEvidence_CountsALoadStoppedOnlyByTheStuckRow(t *testing.T) {
	env := newWriterEnv()
	stuck := stuckPlacement(t, env)
	w, _ := env.writer()
	_, retryErr := w.Place(context.Background(), placeRequest(testUserUUID(4), nil))

	got, err := w.rejectionEvidence(context.Background(), writerTree, unilevelShape, stuck, retryErr)

	require.NoError(t, err)
	assert.True(t, got)
}

// orphanRow is an active row naming a sponsor the tree does not hold, written
// with no event behind it.
func orphanRow() TreeNodeRow {
	sponsor := testUserUUID(6)
	root := writerRoot
	return TreeNodeRow{
		ID: testNodeUUID(50), TreeID: writerTree, UserID: testUserUUID(5),
		ParentID: &root, SponsorID: &sponsor, Depth: 1, EnrolledAt: time.Unix(5, 0).UTC(),
	}
}

func TestRejectionEvidence_RefusesALoadFailureWithNoRowForTheEvent(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	mustPlace(t, env, writerChild, nil)
	require.NoError(t, env.store.InsertNode(context.Background(), orphanRow()))
	removal := appendDirect(t, env.events, EventTypeNodeRemoved, NodeRemovedPayload{
		TreeID: writerTree, UserID: writerChild, RemovedAt: writeTime,
	})
	w, _ := env.writer()
	_, retryErr := w.Place(context.Background(), placeRequest(writerOther, nil))
	require.ErrorContains(t, retryErr, "references sponsor "+testUserUUID(6))

	got, err := w.rejectionEvidence(context.Background(), writerTree, unilevelShape, removal, retryErr)

	require.NoError(t, err)
	assert.False(t, got)
}

func TestRejectionEvidence_RefusesALoadThatStillFailsWithoutTheRow(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	mustPlace(t, env, writerChild, nil)
	placed := streamEvents(t, env.events, TreeStreamName(writerTree))[1]
	require.NoError(t, env.store.InsertNode(context.Background(), orphanRow()))
	w, _ := env.writer()
	_, retryErr := w.Place(context.Background(), placeRequest(writerOther, nil))
	require.ErrorContains(t, retryErr, "references sponsor "+testUserUUID(6))

	got, err := w.rejectionEvidence(context.Background(), writerTree, unilevelShape, placed, retryErr)

	require.NoError(t, err)
	assert.False(t, got)
}

// depthReadFailingStore fails every GetByTreeDepthOrdered.
type depthReadFailingStore struct{ TreeStore }

func (depthReadFailingStore) GetByTreeDepthOrdered(context.Context, string) ([]TreeNodeRow, error) {
	return nil, errors.New("connection reset")
}

func TestRejectionEvidence_ReportsAFailedRowRead(t *testing.T) {
	env := newWriterEnv()
	w := NewTreeWriter(env.events, depthReadFailingStore{env.store}, newFakeWriterEngine(), env.locker)
	last := platform.Event{ID: testNodeUUID(5), Type: EventTypeNodePlaced, Version: 2}

	_, err := w.rejectionEvidence(context.Background(), writerTree, unilevelShape, last,
		&TreeLoadRejectedError{TreeID: writerTree, Kind: TreeLoadDataInvalid})

	require.EqualError(t, err, "read the active rows of tree "+writerTree+" to preflight them without event "+
		last.ID+"'s row: connection reset")
}

func TestTreeConsumer_ARedeliveryMeetsItsOwnTombstoneBeforeAnotherEventsRow(t *testing.T) {
	tree, root, child := testTreeUUID(1), testUserUUID(1), testUserUUID(2)
	store := NewMemoryTreeStore()
	ctx := context.Background()
	require.NoError(t, store.ProjectInsert(ctx, makeUUIDNode(testNodeUUID(1), tree, root, 0, nil, ptr(root), nil), 1))
	require.NoError(t, store.ProjectInsert(ctx, makeUUIDNode(testNodeUUID(2), tree, child, 1, ptr(root), ptr(root), intPtr(0)), 2))
	require.NoError(t, store.ProjectRemoval(ctx, tree, child, testNodeUUID(9), 3, nil))
	require.NoError(t, store.ProjectInsert(ctx, makeUUIDNode(testNodeUUID(5), tree, child, 1, ptr(root), ptr(root), intPtr(0)), 4))
	transport := newRecordingTransport()
	consumer := NewTreeEventConsumer(store, newEngineClientWithTransport(transport))
	zero := 0
	event := platform.Event{
		ID: testNodeUUID(2), Stream: TreeStreamName(tree), Type: EventTypeNodePlaced, Version: 4,
		Payload: mustJSON(t, NodePlacedPayload{
			TreeID: tree, UserID: child, ParentID: root, SponsorID: root,
			Position: &zero, TreeType: treeTypeBinary, EnrolledAt: time.Unix(2, 0).UTC(),
		}),
	}

	err := consumer.HandleEvent(ctx, event)

	require.ErrorIs(t, err, ErrReplayedPlacement)
	for _, conflict := range []error{ErrActiveUserConflict, ErrSlotConflict, ErrRootConflict} {
		assert.NotErrorIs(t, err, conflict)
	}
	assert.Empty(t, transport.calls)
}
