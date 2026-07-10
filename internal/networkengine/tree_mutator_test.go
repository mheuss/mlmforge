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
	removed       []string
	failWith      error
}

// matrixCreate records the params of a CreateMatrixTree call so tests can
// assert that width and spillover were threaded through, not dropped.
type matrixCreate struct {
	structure string
	width     int
	spillover string
}

func (s *stubMutator) CreateTree(_ context.Context, structure, _ string) error {
	if s.failWith != nil {
		return s.failWith
	}
	s.created = append(s.created, structure)
	return nil
}

func (s *stubMutator) CreateMatrixTree(_ context.Context, structure string, width int, spillover string) error {
	if s.failWith != nil {
		return s.failWith
	}
	s.matrixCreated = append(s.matrixCreated, matrixCreate{structure: structure, width: width, spillover: spillover})
	return nil
}

func (s *stubMutator) AddRoot(_ context.Context, _, userID string, _ int64) error {
	if s.failWith != nil {
		return s.failWith
	}
	s.roots = append(s.roots, userID)
	return nil
}

func (s *stubMutator) AddNode(_ context.Context, _, userID, _, _ string, _ int64, _ ...AddNodeOption) error {
	if s.failWith != nil {
		return s.failWith
	}
	s.nodes = append(s.nodes, userID)
	return nil
}

func (s *stubMutator) RemoveNode(_ context.Context, _, userID string) error {
	if s.failWith != nil {
		return s.failWith
	}
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
