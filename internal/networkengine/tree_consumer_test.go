package networkengine

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/mlmforge/mlmforge/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingTransport records all calls and returns configurable responses.
type recordingTransport struct {
	calls    []transportCall
	response json.RawMessage
	err      error
	closed   bool
}

type transportCall struct {
	op     string
	params json.RawMessage
}

func (r *recordingTransport) Call(_ context.Context, op string, params json.RawMessage) (json.RawMessage, error) {
	r.calls = append(r.calls, transportCall{op: op, params: params})
	return r.response, r.err
}

func (r *recordingTransport) Close() error {
	r.closed = true
	return nil
}

func newRecordingTransport() *recordingTransport {
	return &recordingTransport{
		response: json.RawMessage(`{"ok":true}`),
	}
}

func makeEvent(eventType string, payload any) platform.Event {
	data, err := json.Marshal(payload)
	if err != nil {
		panic(fmt.Errorf("makeEvent: marshal payload: %w", err))
	}
	return platform.Event{
		ID:      "00000000-0000-0000-0000-000000000001",
		Stream:  "tree-tree1",
		Type:    eventType,
		Version: 1,
		Payload: data,
	}
}

func TestTreeConsumer_HandleRootAdded(t *testing.T) {
	store := NewMemoryTreeStore()
	transport := newRecordingTransport()
	engine := NewEngineClientWithTransport(transport)
	consumer := NewTreeEventConsumer(store, engine)

	payload := RootAddedPayload{
		TreeID:     "tree1",
		UserID:     "user-root",
		SponsorID:  "user-root",
		EnrolledAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	event := makeEvent(EventTypeRootAdded, payload)

	err := consumer.HandleEvent(context.Background(), event)
	require.NoError(t, err)

	// Verify store projection.
	node, err := store.GetNode(context.Background(), "tree1", "user-root")
	require.NoError(t, err)
	require.NotNil(t, node)
	assert.Equal(t, 0, node.Depth)
	assert.Nil(t, node.ParentID)

	// Verify engine call (add_root).
	require.Len(t, transport.calls, 1)
	assert.Equal(t, "add_root", transport.calls[0].op)
}

func TestTreeConsumer_HandleNodePlaced(t *testing.T) {
	store := NewMemoryTreeStore()
	transport := newRecordingTransport()
	engine := NewEngineClientWithTransport(transport)
	consumer := NewTreeEventConsumer(store, engine)

	// Set up parent in store so depth can be derived.
	parent := makeNode("tree1", "user-root", 0, nil, nil, nil)
	require.NoError(t, store.InsertNode(context.Background(), parent))

	payload := NodePlacedPayload{
		TreeID:     "tree1",
		UserID:     "user-child",
		ParentID:   "user-root",
		SponsorID:  "user-root",
		TreeType:   treeTypeUnilevel,
		EnrolledAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
	}
	event := makeEvent(EventTypeNodePlaced, payload)

	err := consumer.HandleEvent(context.Background(), event)
	require.NoError(t, err)

	// Verify store projection.
	node, err := store.GetNode(context.Background(), "tree1", "user-child")
	require.NoError(t, err)
	require.NotNil(t, node)
	assert.Equal(t, "user-root", *node.ParentID)
	assert.Equal(t, 1, node.Depth)

	// Verify engine call (add_node).
	require.Len(t, transport.calls, 1)
	assert.Equal(t, "add_node", transport.calls[0].op)
}

func TestTreeConsumer_HandleNodeRemoved(t *testing.T) {
	store := NewMemoryTreeStore()
	transport := newRecordingTransport()
	engine := NewEngineClientWithTransport(transport)
	consumer := NewTreeEventConsumer(store, engine)

	// Insert node first.
	node := makeNode("tree1", "user-leaf", 1, ptr("user-root"), ptr("user-root"), nil)
	require.NoError(t, store.InsertNode(context.Background(), node))

	payload := NodeRemovedPayload{
		TreeID:    "tree1",
		UserID:    "user-leaf",
		RemovedAt: time.Now(),
	}
	event := makeEvent(EventTypeNodeRemoved, payload)

	err := consumer.HandleEvent(context.Background(), event)
	require.NoError(t, err)

	// Verify store projection (soft-deleted).
	got, err := store.GetNode(context.Background(), "tree1", "user-leaf")
	require.NoError(t, err)
	assert.Nil(t, got, "node should be soft-deleted")

	// Verify engine call (remove_node).
	require.Len(t, transport.calls, 1)
	assert.Equal(t, "remove_node", transport.calls[0].op)
}

func TestTreeConsumer_NodePlacedMissingParent(t *testing.T) {
	store := NewMemoryTreeStore()
	transport := newRecordingTransport()
	engine := NewEngineClientWithTransport(transport)
	consumer := NewTreeEventConsumer(store, engine)

	// No parent in store — should error, not silently default to depth 0.
	payload := NodePlacedPayload{
		TreeID:     "tree1",
		UserID:     "user-child",
		ParentID:   "nonexistent-parent",
		SponsorID:  "nonexistent-parent",
		TreeType:   treeTypeUnilevel,
		EnrolledAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
	}
	event := makeEvent(EventTypeNodePlaced, payload)

	err := consumer.HandleEvent(context.Background(), event)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parent node nonexistent-parent not found")
	assert.Empty(t, transport.calls, "engine should not be called when parent is missing")
}

func TestTreeConsumer_NodePlacedGateRejections(t *testing.T) {
	valid := func() NodePlacedPayload {
		pos := 1
		return NodePlacedPayload{
			TreeID:     "tree1",
			UserID:     "user-child",
			ParentID:   "user-root",
			SponsorID:  "user-root",
			Position:   &pos,
			TreeType:   treeTypeMatrix,
			EnrolledAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		}
	}
	neg := -1
	two := 2

	cases := []struct {
		name    string
		mutate  func(*NodePlacedPayload)
		stream  string // non-empty overrides the event's stream
		wantErr string
	}{
		{name: "missing tree_type", mutate: func(p *NodePlacedPayload) { p.TreeType = "" }, wantErr: `unsupported tree_type ""`},
		{name: "unknown tree_type", mutate: func(p *NodePlacedPayload) { p.TreeType = "boardplan" }, wantErr: `unsupported tree_type "boardplan"`},
		{name: "wrong-case tree_type", mutate: func(p *NodePlacedPayload) { p.TreeType = "Matrix" }, wantErr: `unsupported tree_type "Matrix"`},
		{name: "matrix nil position", mutate: func(p *NodePlacedPayload) { p.Position = nil }, wantErr: "has no position"},
		{name: "matrix negative position", mutate: func(p *NodePlacedPayload) { p.Position = &neg }, wantErr: "negative position -1"},
		{name: "binary nil position", mutate: func(p *NodePlacedPayload) { p.TreeType = treeTypeBinary; p.Position = nil }, wantErr: "needs position 0 or 1"},
		{name: "binary negative position", mutate: func(p *NodePlacedPayload) { p.TreeType = treeTypeBinary; p.Position = &neg }, wantErr: "negative position -1"},
		{name: "binary position 2", mutate: func(p *NodePlacedPayload) { p.TreeType = treeTypeBinary; p.Position = &two }, wantErr: "needs position 0 or 1"},
		{name: "unilevel negative position", mutate: func(p *NodePlacedPayload) { p.TreeType = treeTypeUnilevel; p.Position = &neg }, wantErr: "negative position -1"},
		{name: "unilevel non-nil position", mutate: func(p *NodePlacedPayload) { p.TreeType = treeTypeUnilevel; p.Position = &two }, wantErr: "unilevel trees have no slots"},
		{name: "stream mismatch", mutate: func(p *NodePlacedPayload) {}, stream: "tree-other", wantErr: `arrived on stream "tree-other"`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := NewMemoryTreeStore()
			transport := newRecordingTransport()
			engine := NewEngineClientWithTransport(transport)
			consumer := NewTreeEventConsumer(store, engine)

			payload := valid()
			tc.mutate(&payload)
			event := makeEvent(EventTypeNodePlaced, payload)
			if tc.stream != "" {
				event.Stream = tc.stream
			}

			err := consumer.HandleEvent(context.Background(), event)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
			assert.Contains(t, err.Error(), payload.UserID, "error names the node")
			assert.Contains(t, err.Error(), "in tree "+payload.TreeID, "error names the tree")

			rows, storeErr := store.GetByTree(context.Background(), "tree1")
			require.NoError(t, storeErr)
			assert.Empty(t, rows, "no store projection for a rejected event")
			assert.Empty(t, transport.calls, "no engine call for a rejected event")
		})
	}
}

func TestTreeConsumer_NodePlacedGateAccepts(t *testing.T) {
	// The reject table above pins what the gate refuses. This pins what it
	// lets through, so a botched bound (e.g. >= for >) fails a test instead
	// of silently rejecting every valid placement. No engine-op assertions:
	// dispatch shape is covered by the routing tests, not the gate.
	zero, one := 0, 1
	cases := []struct {
		name     string
		treeType string
		position *int
	}{
		{name: "unilevel without position", treeType: treeTypeUnilevel, position: nil},
		{name: "binary at position 0", treeType: treeTypeBinary, position: &zero},
		{name: "binary at position 1", treeType: treeTypeBinary, position: &one},
		{name: "matrix at position 0", treeType: treeTypeMatrix, position: &zero},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := NewMemoryTreeStore()
			transport := newRecordingTransport()
			engine := NewEngineClientWithTransport(transport)
			consumer := NewTreeEventConsumer(store, engine)

			parent := makeNode("tree1", "user-root", 0, nil, nil, nil)
			require.NoError(t, store.InsertNode(context.Background(), parent))

			payload := NodePlacedPayload{
				TreeID: "tree1", UserID: "user-child",
				ParentID: "user-root", SponsorID: "user-root",
				Position: tc.position, TreeType: tc.treeType,
				EnrolledAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
			}
			require.NoError(t, consumer.HandleEvent(context.Background(), makeEvent(EventTypeNodePlaced, payload)))

			node, err := store.GetNode(context.Background(), "tree1", "user-child")
			require.NoError(t, err)
			require.NotNil(t, node, "accepted event projects to the store")
			require.Len(t, transport.calls, 1, "accepted event reaches the engine")
		})
	}
}

func TestTreeConsumer_ContextCancellation(t *testing.T) {
	store := NewMemoryTreeStore()
	transport := newFailNTransport(10) // Fail all attempts.
	engine := NewEngineClientWithTransport(transport)
	consumer := NewTreeEventConsumer(store, engine)
	consumer.retryDelay = 500 * time.Millisecond // Slow enough to cancel during wait.

	payload := RootAddedPayload{
		TreeID:     "tree1",
		UserID:     "user-root",
		SponsorID:  "user-root",
		EnrolledAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	event := makeEvent(EventTypeRootAdded, payload)

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel after a short delay so we interrupt the retry wait.
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	err := consumer.HandleEvent(ctx, event)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cancelled")

	// Should have been interrupted before exhausting all retries.
	assert.Less(t, len(transport.calls), 3, "should not exhaust all retries when cancelled")
}

func TestTreeConsumer_UnknownEventType(t *testing.T) {
	store := NewMemoryTreeStore()
	transport := newRecordingTransport()
	engine := NewEngineClientWithTransport(transport)
	consumer := NewTreeEventConsumer(store, engine)

	event := makeEvent("tree.unknown", map[string]string{"foo": "bar"})

	err := consumer.HandleEvent(context.Background(), event)
	require.NoError(t, err, "unknown event types should be silently ignored")
	assert.Empty(t, transport.calls)
}

// failNTransport fails the first N calls, then succeeds.
type failNTransport struct {
	failCount int
	maxFails  int
	response  json.RawMessage
	calls     []transportCall
}

func newFailNTransport(maxFails int) *failNTransport {
	return &failNTransport{
		maxFails: maxFails,
		response: json.RawMessage(`{"ok":true}`),
	}
}

func (f *failNTransport) Call(_ context.Context, op string, params json.RawMessage) (json.RawMessage, error) {
	f.calls = append(f.calls, transportCall{op: op, params: params})
	f.failCount++
	if f.failCount <= f.maxFails {
		return nil, fmt.Errorf("simulated engine failure %d", f.failCount)
	}
	return f.response, nil
}

func (f *failNTransport) Close() error { return nil }

func TestTreeConsumer_EngineRetry(t *testing.T) {
	store := NewMemoryTreeStore()
	transport := newFailNTransport(1) // Fail first call, succeed on retry.
	engine := NewEngineClientWithTransport(transport)
	consumer := NewTreeEventConsumer(store, engine)

	payload := RootAddedPayload{
		TreeID:     "tree1",
		UserID:     "user-root",
		SponsorID:  "user-root",
		EnrolledAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	event := makeEvent(EventTypeRootAdded, payload)

	err := consumer.HandleEvent(context.Background(), event)
	require.NoError(t, err)

	// Store should have been written (happens before engine call).
	node, err := store.GetNode(context.Background(), "tree1", "user-root")
	require.NoError(t, err)
	require.NotNil(t, node)

	// Engine should have been called twice (initial + 1 retry).
	assert.Len(t, transport.calls, 2)
}

func TestTreeConsumer_EngineRetriesExhausted(t *testing.T) {
	store := NewMemoryTreeStore()
	transport := newFailNTransport(10) // Fail all retries.
	engine := NewEngineClientWithTransport(transport)
	consumer := NewTreeEventConsumer(store, engine)

	payload := RootAddedPayload{
		TreeID:     "tree1",
		UserID:     "user-root",
		SponsorID:  "user-root",
		EnrolledAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	event := makeEvent(EventTypeRootAdded, payload)

	err := consumer.HandleEvent(context.Background(), event)
	require.Error(t, err, "should return error when retries exhausted")

	// Store should have been written despite engine failure.
	node, err := store.GetNode(context.Background(), "tree1", "user-root")
	require.NoError(t, err)
	require.NotNil(t, node, "store projection should succeed even when engine fails")

	// Engine should have been called maxRetries+1 times (initial + retries).
	assert.Len(t, transport.calls, 3, "1 initial + 2 retries = 3 calls")
}

// --- Malformed payload tests ---

func TestTreeConsumer_MalformedRootAdded(t *testing.T) {
	store := NewMemoryTreeStore()
	transport := newRecordingTransport()
	engine := NewEngineClientWithTransport(transport)
	consumer := NewTreeEventConsumer(store, engine)

	event := platform.Event{
		ID:      "00000000-0000-0000-0000-000000000001",
		Stream:  "tree-tree1",
		Type:    EventTypeRootAdded,
		Version: 1,
		Payload: json.RawMessage(`{not json}`),
	}

	err := consumer.HandleEvent(context.Background(), event)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unmarshal root_added payload")
	assert.Empty(t, transport.calls, "engine should not be called on bad payload")
}

func TestTreeConsumer_MalformedNodePlaced(t *testing.T) {
	store := NewMemoryTreeStore()
	transport := newRecordingTransport()
	engine := NewEngineClientWithTransport(transport)
	consumer := NewTreeEventConsumer(store, engine)

	event := platform.Event{
		ID:      "00000000-0000-0000-0000-000000000001",
		Stream:  "tree-tree1",
		Type:    EventTypeNodePlaced,
		Version: 1,
		Payload: json.RawMessage(`{not json}`),
	}

	err := consumer.HandleEvent(context.Background(), event)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unmarshal node_placed payload")
	assert.Empty(t, transport.calls, "engine should not be called on bad payload")
}

func TestTreeConsumer_MalformedNodeRemoved(t *testing.T) {
	store := NewMemoryTreeStore()
	transport := newRecordingTransport()
	engine := NewEngineClientWithTransport(transport)
	consumer := NewTreeEventConsumer(store, engine)

	event := platform.Event{
		ID:      "00000000-0000-0000-0000-000000000001",
		Stream:  "tree-tree1",
		Type:    EventTypeNodeRemoved,
		Version: 1,
		Payload: json.RawMessage(`{not json}`),
	}

	err := consumer.HandleEvent(context.Background(), event)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unmarshal node_removed payload")
	assert.Empty(t, transport.calls, "engine should not be called on bad payload")
}

func TestTreeConsumer_NilPayload(t *testing.T) {
	store := NewMemoryTreeStore()
	transport := newRecordingTransport()
	engine := NewEngineClientWithTransport(transport)
	consumer := NewTreeEventConsumer(store, engine)

	event := platform.Event{
		ID:      "00000000-0000-0000-0000-000000000001",
		Stream:  "tree-tree1",
		Type:    EventTypeRootAdded,
		Version: 1,
		Payload: nil,
	}

	err := consumer.HandleEvent(context.Background(), event)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unmarshal root_added payload")
	assert.Empty(t, transport.calls, "engine should not be called on nil payload")
}
