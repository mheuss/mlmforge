package networkengine

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubMutator is a minimal TreeMutator that records what was called.
// It is not *EngineClient, verifying the interface indirection works.
type stubMutator struct {
	created       []string
	matrixCreated []matrixCreate
	roots         []string
	nodes         []string
	nodeCalls     []nodeAddCall
	nodesAt       []nodeAtCall
	removed       []string
	failWith      error
}

// Compile-time check: stubMutator must satisfy TreeMutator.
var _ TreeMutator = (*stubMutator)(nil)

// matrixCreate records the params of a CreateMatrixTree call so tests can
// assert that width and spillover were threaded through, not dropped.
type matrixCreate struct {
	structure string
	width     int
	spillover string
}

// nodeAtCall records the params of an AddNodeAt call so tests can assert that
// explicit placement was threaded through rather than re-derived.
type nodeAtCall struct {
	userID    string
	parentID  string
	sponsorID string
	position  int
}

// nodeAddCall records the params of an AddNode call, including the decoded
// position option, so binary and unilevel replay can be asserted the same way
// matrix replay can. The existing `nodes []string` field records user IDs only.
type nodeAddCall struct {
	userID    string
	parentID  string
	sponsorID string
	position  *int
}

func (s *stubMutator) CreateTree(_ context.Context, structure, _ string) error {
	s.created = append(s.created, structure)
	return s.failWith
}

func (s *stubMutator) CreateMatrixTree(_ context.Context, structure string, width int, spillover string) error {
	s.matrixCreated = append(s.matrixCreated, matrixCreate{structure: structure, width: width, spillover: spillover})
	return s.failWith
}

func (s *stubMutator) AddRoot(_ context.Context, _, userID string, _ int64) error {
	s.roots = append(s.roots, userID)
	return s.failWith
}

func (s *stubMutator) AddNode(_ context.Context, _, userID, parentID, sponsorID string, _ int64, opts ...AddNodeOption) error {
	s.nodes = append(s.nodes, userID)

	// Decode the option set the way EngineClient does, so tests see the
	// position that would actually go over the wire.
	params := map[string]any{}
	for _, opt := range opts {
		opt(params)
	}
	call := nodeAddCall{userID: userID, parentID: parentID, sponsorID: sponsorID}
	if p, ok := params["position"].(int); ok {
		call.position = &p
	}
	s.nodeCalls = append(s.nodeCalls, call)
	return s.failWith
}

func (s *stubMutator) AddNodeAt(_ context.Context, _, userID, parentID, sponsorID string, position int, _ int64) error {
	s.nodesAt = append(s.nodesAt, nodeAtCall{
		userID:    userID,
		parentID:  parentID,
		sponsorID: sponsorID,
		position:  position,
	})
	return s.failWith
}

// totalCalls counts every engine call the stub recorded. Preflight tests assert
// this is zero: the guarantee is that a rejected tree never reaches the engine,
// and counting one method at a time would miss a leak through another.
//
// nodeCalls is deliberately excluded — it mirrors `nodes` one-for-one, and
// double-counting AddNode would make a zero assertion no stricter while making
// a non-zero count harder to read.
//
// Every method records before consulting failWith, so a call that the stub
// then fails still counts. Recording after the guard would let a test that
// sets failWith assert zero calls and pass vacuously — on the one assertion
// the whole atomicity guarantee rests on.
func (s *stubMutator) totalCalls() int {
	return len(s.created) + len(s.matrixCreated) + len(s.roots) +
		len(s.nodes) + len(s.nodesAt) + len(s.removed)
}

func (s *stubMutator) RemoveNode(_ context.Context, _, userID string) error {
	s.removed = append(s.removed, userID)
	return nil
}

// TestTreeMutator_ConsumerAcceptsInterface verifies that TreeEventConsumer
// accepts any TreeMutator, not just *EngineClient.
func TestTreeMutator_ConsumerAcceptsInterface(t *testing.T) {
	store := NewMemoryTreeStore()
	mutator := &stubMutator{}
	consumer := NewTreeEventConsumer(store, mutator)

	payload := RootAddedPayload{
		TreeID:     "tree1",
		UserID:     "user-root",
		SponsorID:  "user-root",
		EnrolledAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	event := makeEvent(EventTypeRootAdded, payload)

	err := consumer.HandleEvent(context.Background(), event)
	require.NoError(t, err)

	assert.Equal(t, []string{"user-root"}, mutator.roots)
	assert.Empty(t, mutator.nodes)
	assert.Empty(t, mutator.removed)
	assert.Empty(t, mutator.created)
}

// TestTreeMutator_LoaderAcceptsInterface verifies that TreeLoader accepts
// any TreeMutator, not just *EngineClient.
func TestTreeMutator_LoaderAcceptsInterface(t *testing.T) {
	store := NewMemoryTreeStore()
	ctx := context.Background()

	root := makeNode("tree-1", "user-0", 0, nil, ptr("user-0"), nil)
	root.EnrolledAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, store.InsertNode(ctx, root))

	mutator := &stubMutator{}
	loader := NewTreeLoader(store, mutator)

	err := loader.LoadTree(ctx, "tree-1", "unilevel")
	require.NoError(t, err)

	assert.Equal(t, []string{"tree-1"}, mutator.created)
	assert.Equal(t, []string{"user-0"}, mutator.roots)
	assert.Empty(t, mutator.nodes)
	assert.Empty(t, mutator.removed)
}
