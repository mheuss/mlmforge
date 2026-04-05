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
	created  []string
	roots    []string
	nodes    []string
	removed  []string
	failWith error
}

func (s *stubMutator) CreateTree(_ context.Context, structure, _ string) error {
	if s.failWith != nil {
		return s.failWith
	}
	s.created = append(s.created, structure)
	return nil
}

func (s *stubMutator) AddRoot(_ context.Context, structure, userID string, _ int64) error {
	if s.failWith != nil {
		return s.failWith
	}
	s.roots = append(s.roots, userID)
	_ = structure
	return nil
}

func (s *stubMutator) AddNode(_ context.Context, structure, userID, _, _ string, _ int64, _ ...AddNodeOption) error {
	if s.failWith != nil {
		return s.failWith
	}
	s.nodes = append(s.nodes, userID)
	_ = structure
	return nil
}

func (s *stubMutator) RemoveNode(_ context.Context, structure, userID string) error {
	if s.failWith != nil {
		return s.failWith
	}
	s.removed = append(s.removed, userID)
	_ = structure
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
}
