package networkengine

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// removalStub answers RemoveNode and RemoveMatrixNode with fixed results and
// records each call.
type removalStub struct {
	TreeMutator
	moved  []Responsored
	matrix *MatrixRemovalResult
	calls  []string
}

func (s *removalStub) RemoveNode(_ context.Context, structure, userID string) ([]Responsored, error) {
	s.calls = append(s.calls, "remove_node "+structure+" "+userID)
	return s.moved, nil
}

func (s *removalStub) RemoveMatrixNode(_ context.Context, structure, userID string, mode PruningMode) (*MatrixRemovalResult, error) {
	s.calls = append(s.calls, "remove_matrix_node "+structure+" "+userID+" "+string(mode))
	return s.matrix, nil
}

func TestNodePlacedCall_SendsAMatrixPlacementThroughAddNodeAt(t *testing.T) {
	engine := newFakeWriterEngine()
	require.NoError(t, engine.CreateMatrixTree(context.Background(), writerTree, 3, "breadth_first"))
	require.NoError(t, engine.AddRoot(context.Background(), writerTree, writerRoot, writeTime.Unix()))
	two := 2
	p := NodePlacedPayload{TreeID: writerTree, UserID: writerChild, ParentID: writerRoot, SponsorID: writerRoot,
		Position: &two, TreeType: treeTypeMatrix, EnrolledAt: writeTime}

	call := nodePlacedCall(p)
	_, err := call.apply(context.Background(), engine)

	require.NoError(t, err)
	assert.Equal(t, "add_node_at", call.op)
	assert.Equal(t, []Mutation{CheckAddNodeAt(writerChild, writerRoot, writerRoot, 2, writeTime.Unix())}, engine.placements)
}

func TestNodePlacedCall_SendsABinaryPlacementThroughAddNodeWithItsPosition(t *testing.T) {
	engine := newFakeWriterEngine()
	require.NoError(t, engine.CreateTree(context.Background(), writerTree, treeTypeBinary))
	require.NoError(t, engine.AddRoot(context.Background(), writerTree, writerRoot, writeTime.Unix()))
	one := 1
	p := NodePlacedPayload{TreeID: writerTree, UserID: writerChild, ParentID: writerRoot, SponsorID: writerRoot,
		Position: &one, TreeType: treeTypeBinary, EnrolledAt: writeTime}

	call := nodePlacedCall(p)
	_, err := call.apply(context.Background(), engine)

	require.NoError(t, err)
	assert.Equal(t, "add_node", call.op)
	assert.Equal(t, []Mutation{CheckAddNode(writerChild, writerRoot, writerRoot, writeTime.Unix(), WithPosition(1))}, engine.placements)
}

func TestNodeRemovedCall_RefusesAMatrixPayloadWithNoMode(t *testing.T) {
	p := NodeRemovedPayload{TreeID: writerTree, UserID: writerChild, RemovedAt: writeTime}

	_, err := nodeRemovedCall(p, treeTypeMatrix)

	require.EqualError(t, err, "node_removed for "+writerChild+" in tree "+writerTree+
		": the caller names the tree matrix, and the payload carries no pruning mode")
}

func TestNodeRemovedCall_ReturnsTheRecruitsRemoveNodeMoved(t *testing.T) {
	stub := &removalStub{moved: []Responsored{{UserID: writerOther, NewSponsorID: writerRoot}}}
	p := NodeRemovedPayload{TreeID: writerTree, UserID: writerChild, RemovedAt: writeTime}

	call, err := nodeRemovedCall(p, "")
	require.NoError(t, err)
	out, err := call.apply(context.Background(), stub)

	require.NoError(t, err)
	assert.Equal(t, "remove_node", call.op)
	assert.Equal(t, []string{"remove_node " + writerTree + " " + writerChild}, stub.calls)
	assert.Equal(t, stub.moved, out.responsored)
	assert.Equal(t, []string{writerChild}, out.removed)
}

func TestNodeRemovedCall_SendsAPromoteEarliestRemovalThroughRemoveMatrixNode(t *testing.T) {
	stub := &removalStub{matrix: &MatrixRemovalResult{
		Removed:     writerChild,
		Relocated:   []Relocation{{UserID: writerOther, ParentID: writerRoot, Position: 0, Depth: 1}},
		Responsored: []Responsored{{UserID: writerOther, NewSponsorID: writerRoot}},
		MovedToTank: []string{},
	}}
	p := NodeRemovedPayload{TreeID: writerTree, UserID: writerChild, RemovedAt: writeTime,
		TreeType: treeTypeMatrix, PruningMode: PruningPromoteEarliest}

	call, err := nodeRemovedCall(p, treeTypeMatrix)
	require.NoError(t, err)
	out, err := call.apply(context.Background(), stub)

	require.NoError(t, err)
	assert.Equal(t, "remove_node", call.op)
	assert.Equal(t, []string{"remove_matrix_node " + writerTree + " " + writerChild + " promote_earliest"}, stub.calls)
	assert.Equal(t, engineOutcome{
		responsored: stub.matrix.Responsored,
		relocated:   stub.matrix.Relocated,
		removed:     []string{writerChild},
	}, out)
}

func TestNodeRemovedCall_ReportsAHoldingTankRemovalsWholeSetAsRemoved(t *testing.T) {
	stub := &removalStub{matrix: &MatrixRemovalResult{
		Removed:     writerChild,
		MovedToTank: []string{writerOther, writerChild},
		Relocated:   []Relocation{},
		Responsored: []Responsored{},
	}}
	p := NodeRemovedPayload{TreeID: writerTree, UserID: writerChild, RemovedAt: writeTime,
		TreeType: treeTypeMatrix, PruningMode: PruningHoldingTank}

	call, err := nodeRemovedCall(p, treeTypeMatrix)
	require.NoError(t, err)
	out, err := call.apply(context.Background(), stub)

	require.NoError(t, err)
	assert.Equal(t, []string{"remove_matrix_node " + writerTree + " " + writerChild + " holding_tank"}, stub.calls)
	assert.Equal(t, engineOutcome{
		responsored: stub.matrix.Responsored,
		relocated:   stub.matrix.Relocated,
		removed:     []string{writerOther, writerChild},
	}, out)
}

func TestTreeConsumer_ARemovalStillLandsTheRecruitsTheEngineMoved(t *testing.T) {
	store := NewMemoryTreeStore()
	transport := newRecordingTransport()
	transport.response = json.RawMessage(`{"removed":true,"responsored":[{"user_id":"user-recruit","new_sponsor_id":"user-root"}]}`)
	consumer := NewTreeEventConsumer(store, newEngineClientWithTransport(transport))
	ctx := context.Background()
	require.NoError(t, store.InsertNode(ctx, makeNode("tree1", "user-root", 0, nil, ptr("user-root"), nil)))
	require.NoError(t, store.InsertNode(ctx, makeNode("tree1", "user-leaf", 1, ptr("user-root"), ptr("user-root"), nil)))
	require.NoError(t, store.InsertNode(ctx, makeNode("tree1", "user-recruit", 1, ptr("user-root"), ptr("user-leaf"), nil)))

	err := consumer.HandleEvent(ctx, makeEvent(EventTypeNodeRemoved, NodeRemovedPayload{
		TreeID: "tree1", UserID: "user-leaf", RemovedAt: time.Now(),
	}))

	require.NoError(t, err)
	recruit, err := store.GetNode(ctx, "tree1", "user-recruit")
	require.NoError(t, err)
	require.NotNil(t, recruit)
	require.NotNil(t, recruit.SponsorID)
	assert.Equal(t, "user-root", *recruit.SponsorID)
}
