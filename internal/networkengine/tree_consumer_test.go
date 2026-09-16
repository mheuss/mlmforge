package networkengine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
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

// unitEventCounter gives every makeEvent a distinct ID. A fixed ID would
// trip MemoryTreeStore's tree_nodes_pkey mirror the moment a test pushes
// two events through one store.
var unitEventCounter uint64

func makeEvent(eventType string, payload any) platform.Event {
	data, err := json.Marshal(payload)
	if err != nil {
		panic(fmt.Errorf("makeEvent: marshal payload: %w", err))
	}
	n := atomic.AddUint64(&unitEventCounter, 1)
	return platform.Event{
		ID:      fmt.Sprintf("dddddddd-dddd-dddd-dddd-%012d", n),
		Stream:  "tree-tree1",
		Type:    eventType,
		Version: 1,
		Payload: data,
	}
}

func TestTreeConsumer_HandleRootAdded(t *testing.T) {
	store := NewMemoryTreeStore()
	transport := newRecordingTransport()
	engine := newEngineClientWithTransport(transport)
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

func TestTreeConsumer_RootAddedRejectsWrongStream(t *testing.T) {
	store := NewMemoryTreeStore()
	transport := newRecordingTransport()
	engine := newEngineClientWithTransport(transport)
	consumer := NewTreeEventConsumer(store, engine)

	payload := RootAddedPayload{
		TreeID:     "tree1",
		UserID:     "user-root",
		SponsorID:  "user-root",
		EnrolledAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	event := makeEvent(EventTypeRootAdded, payload)
	event.Stream = "tree-other"

	err := consumer.HandleEvent(context.Background(), event)
	require.Error(t, err)
	assert.EqualError(t, err,
		`root_added for user-root in tree tree1 arrived on stream "tree-other", want "tree-tree1"`,
		"the message names the event, the node, the tree, and both streams")

	rows, storeErr := store.GetByTree(context.Background(), "tree1")
	require.NoError(t, storeErr)
	assert.Empty(t, rows, "no store projection for a rejected event")
	assert.Empty(t, transport.calls, "no engine call for a rejected event")
}

func TestTreeConsumer_HandleNodePlaced(t *testing.T) {
	store := NewMemoryTreeStore()
	transport := newRecordingTransport()
	engine := newEngineClientWithTransport(transport)
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

func TestTreeConsumer_NodePlacedRejectsWrongStream(t *testing.T) {
	store := NewMemoryTreeStore()
	transport := newRecordingTransport()
	engine := newEngineClientWithTransport(transport)
	consumer := NewTreeEventConsumer(store, engine)

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
	event.Stream = "tree-other"

	err := consumer.HandleEvent(context.Background(), event)
	require.Error(t, err)
	assert.EqualError(t, err,
		`node_placed for user-child in tree tree1 arrived on stream "tree-other", want "tree-tree1"`,
		"the message names the event, the node, the tree, and both streams")

	assert.Empty(t, transport.calls, "no engine call for a rejected event")
}

func TestTreeConsumer_HandleNodeRemoved(t *testing.T) {
	store := NewMemoryTreeStore()
	transport := newRecordingTransport()
	transport.response = json.RawMessage(`{"removed":true,"responsored":[]}`)
	engine := newEngineClientWithTransport(transport)
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

func TestTreeConsumer_NodeRemovedRejectsWrongStream(t *testing.T) {
	store := NewMemoryTreeStore()
	transport := newRecordingTransport()
	transport.response = json.RawMessage(`{"removed":true,"responsored":[]}`)
	engine := newEngineClientWithTransport(transport)
	consumer := NewTreeEventConsumer(store, engine)

	node := makeNode("tree1", "user-leaf", 1, ptr("user-root"), ptr("user-root"), nil)
	require.NoError(t, store.InsertNode(context.Background(), node))

	payload := NodeRemovedPayload{
		TreeID:    "tree1",
		UserID:    "user-leaf",
		RemovedAt: time.Now(),
	}
	event := makeEvent(EventTypeNodeRemoved, payload)
	event.Stream = "tree-other"

	err := consumer.HandleEvent(context.Background(), event)
	require.Error(t, err)
	assert.EqualError(t, err,
		`node_removed for user-leaf in tree tree1 arrived on stream "tree-other", want "tree-tree1"`,
		"the message names the event, the node, the tree, and both streams")

	assert.Empty(t, transport.calls, "no engine call for a rejected event")

	got, storeErr := store.GetNode(context.Background(), "tree1", "user-leaf")
	require.NoError(t, storeErr)
	assert.NotNil(t, got, "the existing row must survive a rejected removal")
}

func TestTreeConsumer_NodePlacedMissingParent(t *testing.T) {
	store := NewMemoryTreeStore()
	transport := newRecordingTransport()
	engine := newEngineClientWithTransport(transport)
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
	big := 256

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
		{name: "matrix position above u8 ceiling", mutate: func(p *NodePlacedPayload) { p.Position = &big }, wantErr: "above the 255 slot ceiling"},
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
			engine := newEngineClientWithTransport(transport)
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
			engine := newEngineClientWithTransport(transport)
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

func TestTreeConsumer_MatrixNodePlacedRoutesThroughAddNodeAt(t *testing.T) {
	store := NewMemoryTreeStore()
	transport := newRecordingTransport()
	engine := newEngineClientWithTransport(transport)
	consumer := NewTreeEventConsumer(store, engine)

	parent := makeNode("tree1", "user-root", 0, nil, nil, nil)
	require.NoError(t, store.InsertNode(context.Background(), parent))

	pos := 2
	payload := NodePlacedPayload{
		TreeID:     "tree1",
		UserID:     "user-child",
		ParentID:   "user-root",
		SponsorID:  "user-sponsor", // differs from parent so a transposition is visible
		Position:   &pos,
		TreeType:   treeTypeMatrix,
		EnrolledAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
	}
	require.NoError(t, consumer.HandleEvent(context.Background(), makeEvent(EventTypeNodePlaced, payload)))

	// Store projection carries the event's placement.
	node, err := store.GetNode(context.Background(), "tree1", "user-child")
	require.NoError(t, err)
	require.NotNil(t, node)
	assert.Equal(t, "user-root", *node.ParentID)
	assert.Equal(t, "user-sponsor", *node.SponsorID)
	assert.Equal(t, 2, *node.Position)

	// Engine got add_node_at with the same values, at the wire level.
	// Asserting raw params is the transposition catch: parent and sponsor
	// are both strings, so a swap compiles and only this notices.
	require.Len(t, transport.calls, 1)
	assert.Equal(t, "add_node_at", transport.calls[0].op)

	var params map[string]any
	require.NoError(t, json.Unmarshal(transport.calls[0].params, &params))
	assert.Equal(t, "tree1", params["structure"])
	assert.Equal(t, "user-child", params["user_id"])
	assert.Equal(t, "user-root", params["parent_id"])
	assert.Equal(t, "user-sponsor", params["sponsor_id"])
	assert.Equal(t, float64(2), params["position"])
	assert.Equal(t, float64(payload.EnrolledAt.Unix()), params["enrolled_at"])
}

func TestTreeConsumer_UnilevelAndBinaryDispatchUnchanged(t *testing.T) {
	cases := []struct {
		name         string
		treeType     string
		position     *int
		wantPosition bool
	}{
		{name: "unilevel has no position", treeType: treeTypeUnilevel, position: nil, wantPosition: false},
		{name: "binary carries its position", treeType: treeTypeBinary, position: intPtr(1), wantPosition: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := NewMemoryTreeStore()
			transport := newRecordingTransport()
			engine := newEngineClientWithTransport(transport)
			consumer := NewTreeEventConsumer(store, engine)

			parent := makeNode("tree1", "user-root", 0, nil, nil, nil)
			require.NoError(t, store.InsertNode(context.Background(), parent))

			// Sponsor differs from parent so a transposition at the AddNode
			// call site is visible in the wire params, mirroring the matrix
			// routing test. EngineClient pins its internal mapping; call
			// sites need their own assertion (engine_client.go).
			payload := NodePlacedPayload{
				TreeID: "tree1", UserID: "user-child",
				ParentID: "user-root", SponsorID: "user-sponsor",
				Position: tc.position, TreeType: tc.treeType,
				EnrolledAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
			}
			require.NoError(t, consumer.HandleEvent(context.Background(), makeEvent(EventTypeNodePlaced, payload)))

			require.Len(t, transport.calls, 1)
			assert.Equal(t, "add_node", transport.calls[0].op)

			var params map[string]any
			require.NoError(t, json.Unmarshal(transport.calls[0].params, &params))
			assert.Equal(t, "user-root", params["parent_id"])
			assert.Equal(t, "user-sponsor", params["sponsor_id"])
			_, hasPosition := params["position"]
			assert.Equal(t, tc.wantPosition, hasPosition)
			if tc.wantPosition {
				assert.Equal(t, float64(1), params["position"])
			}
		})
	}
}

func TestTreeConsumer_MatrixStoreProjectionPrecedesEngine(t *testing.T) {
	store := NewMemoryTreeStore()
	transport := newFailNTransport(10) // engine always fails
	engine := newEngineClientWithTransport(transport)
	consumer := NewTreeEventConsumer(store, engine)
	consumer.retryDelay = 0 // ordering is the behavior under test, not retry pacing

	parent := makeNode("tree1", "user-root", 0, nil, nil, nil)
	require.NoError(t, store.InsertNode(context.Background(), parent))

	pos := 0
	payload := NodePlacedPayload{
		TreeID: "tree1", UserID: "user-child",
		ParentID: "user-root", SponsorID: "user-root",
		Position: &pos, TreeType: treeTypeMatrix,
		EnrolledAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
	}
	err := consumer.HandleEvent(context.Background(), makeEvent(EventTypeNodePlaced, payload))
	require.Error(t, err, "engine failure surfaces after retries")

	node, storeErr := store.GetNode(context.Background(), "tree1", "user-child")
	require.NoError(t, storeErr)
	require.NotNil(t, node, "store projection lands before the engine call (ADR-021)")

	// Retry exhaustion counts are TestTreeConsumer_EngineRetriesExhausted's
	// behavior; this test owns ordering, plus the fact that the matrix arm
	// is wrapped in withRetry at all (more than one call proves the wrapper
	// without pinning its arithmetic).
	require.NotEmpty(t, transport.calls)
	assert.Equal(t, "add_node_at", transport.calls[0].op)
	assert.Greater(t, len(transport.calls), 1, "matrix dispatch goes through withRetry")
}

func TestTreeConsumer_ContextCancellation(t *testing.T) {
	store := NewMemoryTreeStore()
	transport := newFailNTransport(10) // Fail all attempts.
	engine := newEngineClientWithTransport(transport)
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
	engine := newEngineClientWithTransport(transport)
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
	engine := newEngineClientWithTransport(transport)
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
	engine := newEngineClientWithTransport(transport)
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
	engine := newEngineClientWithTransport(transport)
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
	engine := newEngineClientWithTransport(transport)
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
	engine := newEngineClientWithTransport(transport)
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
	engine := newEngineClientWithTransport(transport)
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

// newRetryTestConsumer builds a consumer with no retry delay, so the retry
// tests do not spend maxRetries * retryDelay sleeping.
func newRetryTestConsumer() *TreeEventConsumer {
	c := NewTreeEventConsumer(NewMemoryTreeStore(), &stubMutator{})
	c.retryDelay = 0
	return c
}

func TestWithRetry_DoesNotReconcileOnFirstSuccess(t *testing.T) {
	c := newRetryTestConsumer()
	reconcileCalls := 0

	err := c.withRetry(context.Background(), "add_node", "tree-42", "user-99",
		func() error { return nil },
		func(context.Context, error) (reconcileOutcome, error) {
			reconcileCalls++
			return reconcileNotApplicable, nil
		})

	require.NoError(t, err)
	assert.Equal(t, 0, reconcileCalls, "reconcile must not run when fn succeeds")
}

func TestWithRetry_ConvergedStopsAndSucceeds(t *testing.T) {
	c := newRetryTestConsumer()
	attempts := 0

	err := c.withRetry(context.Background(), "add_node", "tree-42", "user-99",
		func() error { attempts++; return errors.New("USER_ALREADY_EXISTS") },
		func(context.Context, error) (reconcileOutcome, error) {
			return reconcileConverged, nil
		})

	require.NoError(t, err, "a mutation the engine already applied is not a failure")
	assert.Equal(t, 1, attempts, "converged must not retry")
}

func TestWithRetry_DivergedReturnsTheReconcileError(t *testing.T) {
	c := newRetryTestConsumer()
	attempts := 0
	divergence := errors.New("engine holds a different parent")

	err := c.withRetry(context.Background(), "add_node", "tree-42", "user-99",
		func() error { attempts++; return errors.New("USER_ALREADY_EXISTS") },
		func(context.Context, error) (reconcileOutcome, error) {
			return reconcileDiverged, divergence
		})

	require.ErrorIs(t, err, divergence)
	assert.Equal(t, 1, attempts, "divergence is not retryable")
}

// A handler returning diverged with no error would otherwise make withRetry
// return nil, so the consumer would report success for the one outcome that
// means the store and the engine disagree.
func TestWithRetry_DivergedWithoutAnErrorStillFails(t *testing.T) {
	c := newRetryTestConsumer()
	attempts := 0

	err := c.withRetry(context.Background(), "add_node", "tree-42", "user-99",
		func() error { attempts++; return errors.New("USER_ALREADY_EXISTS") },
		func(context.Context, error) (reconcileOutcome, error) {
			return reconcileDiverged, nil
		})

	assert.Equal(t, 1, attempts, "divergence is not retryable, synthesised error or not")
	require.Error(t, err, "diverged must never read as success")
	assert.Contains(t, err.Error(), "add_node")
	assert.Contains(t, err.Error(), "tree-42")
	assert.Contains(t, err.Error(), "user-99")
}

func TestWithRetry_NotApplicableRetriesAndReportsTheEngineError(t *testing.T) {
	c := newRetryTestConsumer()
	attempts := 0
	engineErr := errors.New("PIPE_DESYNC")

	err := c.withRetry(context.Background(), "add_node", "tree-42", "user-99",
		func() error { attempts++; return engineErr },
		func(context.Context, error) (reconcileOutcome, error) {
			return reconcileNotApplicable, nil
		})

	require.ErrorIs(t, err, engineErr)
	assert.Equal(t, c.maxRetries+1, attempts)
}

// The inconclusive case is why there are four outcomes rather than three. An
// inspection that could not answer must not be read as divergence, and the
// error the caller sees has to name the mutation that failed rather than the
// inspection that could not check it.
func TestWithRetry_InconclusiveRetriesAndReportsTheEngineError(t *testing.T) {
	c := newRetryTestConsumer()
	attempts := 0
	engineErr := errors.New("USER_ALREADY_EXISTS")
	inspectErr := errors.New("get_position timed out")

	err := c.withRetry(context.Background(), "add_node", "tree-42", "user-99",
		func() error { attempts++; return engineErr },
		func(context.Context, error) (reconcileOutcome, error) {
			return reconcileInconclusive, inspectErr
		})

	assert.Equal(t, c.maxRetries+1, attempts, "an inconclusive inspection retries")
	require.ErrorIs(t, err, engineErr, "the caller is told what failed to apply")
	assert.NotErrorIs(t, err, inspectErr, "not what failed to inspect")
}

func TestWithRetry_ReconcileSeesTheEngineError(t *testing.T) {
	c := newRetryTestConsumer()
	engineErr := errors.New("USER_ALREADY_EXISTS")
	var seen error

	_ = c.withRetry(context.Background(), "add_node", "tree-42", "user-99",
		func() error { return engineErr },
		func(_ context.Context, err error) (reconcileOutcome, error) {
			seen = err
			return reconcileDiverged, errors.New("x")
		})

	assert.ErrorIs(t, seen, engineErr, "reconcile decides from the error fn returned")
}

func TestWithRetry_NilReconcileKeepsTheOldBehavior(t *testing.T) {
	c := newRetryTestConsumer()
	attempts := 0
	engineErr := errors.New("boom")

	err := c.withRetry(context.Background(), "add_node", "tree-42", "user-99",
		func() error { attempts++; return engineErr }, nil)

	require.ErrorIs(t, err, engineErr)
	assert.Equal(t, c.maxRetries+1, attempts)
}

// The property Tasks 15 to 17 rest on. Gating the reconcile call to the first
// attempt leaves every other test in this file green, so nothing else observes
// that it runs again.
func TestWithRetry_ReconcileRunsOnEveryFailedAttempt(t *testing.T) {
	c := newRetryTestConsumer()
	reconcileCalls := 0

	err := c.withRetry(context.Background(), "add_node", "tree-42", "user-99",
		func() error { return errors.New("PIPE_DESYNC") },
		func(context.Context, error) (reconcileOutcome, error) {
			reconcileCalls++
			return reconcileNotApplicable, nil
		})

	require.Error(t, err)
	assert.Equal(t, c.maxRetries+1, reconcileCalls, "every failed attempt is inspected, not just the first")
}

// The lost-reply case the ticket exists for. Attempt 0 fails in transit and
// reconcile cannot speak to it; attempt 1 reports USER_ALREADY_EXISTS because
// attempt 0 actually landed, and reconcile converges.
func TestWithRetry_ConvergesOnALaterAttempt(t *testing.T) {
	c := newRetryTestConsumer()
	attempts := 0

	err := c.withRetry(context.Background(), "add_node", "tree-42", "user-99",
		func() error {
			attempts++
			if attempts == 1 {
				return errors.New("PIPE_DESYNC")
			}
			return errors.New("USER_ALREADY_EXISTS")
		},
		func(_ context.Context, err error) (reconcileOutcome, error) {
			if err.Error() == "USER_ALREADY_EXISTS" {
				return reconcileConverged, nil
			}
			return reconcileNotApplicable, nil
		})

	require.NoError(t, err, "the mutation landed; the reply was lost")
	assert.Equal(t, 2, attempts)
}

// A cancelled context cannot carry an inspection RPC, and consulting reconcile
// anyway fills the ERROR log on every clean shutdown.
func TestWithRetry_DoesNotReconcileOnACancelledContext(t *testing.T) {
	c := newRetryTestConsumer()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reconcileCalls := 0

	err := c.withRetry(ctx, "add_node", "tree-42", "user-99",
		func() error { return errors.New("PIPE_DESYNC") },
		func(context.Context, error) (reconcileOutcome, error) {
			reconcileCalls++
			return reconcileNotApplicable, nil
		})

	require.Error(t, err)
	assert.Equal(t, 0, reconcileCalls, "nothing is inspected once the context is done")
}

func TestIsEngineCode(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code string
		want bool
	}{
		{
			name: "bare EngineError with the code",
			err:  &EngineError{Code: engineCodeUserNotFound, Message: "no such user"},
			code: engineCodeUserNotFound,
			want: true,
		},
		{
			name: "bare EngineError with a different code",
			err:  &EngineError{Code: engineCodeUserAlreadyExists, Message: "taken"},
			code: engineCodeUserNotFound,
			want: false,
		},
		{
			// withRetry wraps every engine failure before any caller sees it,
			// so this is the shape reconcile actually receives. A type
			// assertion in place of errors.As passes every other row here and
			// fails this one.
			name: "wrapped EngineError with the code",
			err:  fmt.Errorf("engine add_node failed after 2 retries: %w", &EngineError{Code: engineCodeUserAlreadyExists}),
			code: engineCodeUserAlreadyExists,
			want: true,
		},
		{
			name: "twice-wrapped EngineError with the code",
			err: fmt.Errorf("handle node_placed: %w",
				fmt.Errorf("engine add_node failed: %w", &EngineError{Code: engineCodeRootAlreadyExists})),
			code: engineCodeRootAlreadyExists,
			want: true,
		},
		{
			name: "wrapped EngineError with a different code",
			err:  fmt.Errorf("engine add_node failed: %w", &EngineError{Code: engineCodeUserAlreadyExists}),
			code: engineCodeUserNotFound,
			want: false,
		},
		{
			// The message carries the code text, so a Contains-style check
			// would say true here.
			name: "plain error whose text names the code",
			err:  errors.New("engine error [USER_NOT_FOUND]: no such user"),
			code: engineCodeUserNotFound,
			want: false,
		},
		{
			name: "nil error",
			err:  nil,
			code: engineCodeUserNotFound,
			want: false,
		},
		{
			name: "empty code matches nothing",
			err:  &EngineError{Code: engineCodeUserNotFound},
			code: "",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isEngineCode(tt.err, tt.code))
		})
	}
}

// The three codes are a contract with the Rust worker, which maps its
// TreeError variants to these strings. A typo here is invisible: reconcile
// simply never fires, and the consumer reports the engine failure it was
// meant to resolve.
func TestEngineCodeConstantsMatchTheWorker(t *testing.T) {
	assert.Equal(t, "USER_ALREADY_EXISTS", engineCodeUserAlreadyExists)
	assert.Equal(t, "ROOT_ALREADY_EXISTS", engineCodeRootAlreadyExists)
	assert.Equal(t, "USER_NOT_FOUND", engineCodeUserNotFound)
}
