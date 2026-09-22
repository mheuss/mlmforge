package networkengine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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

// Gating the reconcile call to the first attempt leaves every other test in
// this file green, so nothing else observes that it runs on later ones.
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
			// The transport returns a bare EngineError and nothing between it
			// and reconcile wraps it, so the first row is the live shape. This
			// row exists so a caller need not know that. A type assertion in
			// place of errors.As passes every other row and fails this one.
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

const (
	// Hex letters, not just digits. strings.ToUpper on an all-digit uuid is
	// a no-op, so the case-difference row would compare a string with itself.
	posUser    = "aaaaaaaa-1111-4111-8111-aaaaaaaaaaaa"
	posParent  = "bbbbbbbb-2222-4222-8222-bbbbbbbbbbbb"
	posSponsor = "cccccccc-3333-4333-8333-cccccccccccc"
	posOther   = "dddddddd-4444-4444-8444-dddddddddddd"
	posTree    = "eeeeeeee-5555-4555-8555-eeeeeeeeeeee"
)

var posEnrolled = time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

// enginePos builds the engine's view of a placed node, matching payloadFor.
func enginePos(mutate func(*EnginePosition)) *EnginePosition {
	p := &EnginePosition{
		UserID:        posUser,
		ParentUserID:  ptr(posParent),
		SponsorUserID: ptr(posSponsor),
		Position:      1,
		Depth:         2,
		EnrolledAt:    posEnrolled.Unix(),
	}
	if mutate != nil {
		mutate(p)
	}
	return p
}

// storedRow is the projected row the engine's view is compared against. Only
// the fields that comparison reads are set.
func storedRow(depth int, sponsor *string) *TreeNodeRow {
	return &TreeNodeRow{
		TreeID:    posTree,
		UserID:    posUser,
		ParentID:  ptr(posParent),
		SponsorID: sponsor,
		Depth:     depth,
	}
}

func payloadFor(treeType string, position *int) NodePlacedPayload {
	return NodePlacedPayload{
		TreeID:     posTree,
		UserID:     posUser,
		ParentID:   posParent,
		SponsorID:  posSponsor,
		Position:   position,
		TreeType:   treeType,
		EnrolledAt: posEnrolled,
	}
}

func TestPositionMatchesProjection(t *testing.T) {
	tests := []struct {
		name    string
		pos     *EnginePosition
		payload NodePlacedPayload
		stored  *TreeNodeRow
		want    bool
	}{
		{
			// Unilevel events carry no position and the gate in
			// handleNodePlaced refuses one, so the engine's Position, which is
			// an int and always holds a value, has nothing to be compared
			// against. Comparing it would fail every unilevel redelivery.
			name:    "unilevel matches without comparing position",
			pos:     enginePos(func(p *EnginePosition) { p.Position = 7 }),
			payload: payloadFor(treeTypeUnilevel, nil),
			stored:  storedRow(2, ptr(posSponsor)),
			want:    true,
		},
		{
			// Skipping position must not mean skipping the rest. Returning
			// true for unilevel straight after the nil guard passes every
			// other row in this table.
			name:    "unilevel with a different parent does not match",
			pos:     enginePos(func(p *EnginePosition) { p.ParentUserID = ptr(posOther) }),
			payload: payloadFor(treeTypeUnilevel, nil),
			stored:  storedRow(2, ptr(posSponsor)),
			want:    false,
		},
		{
			name:    "unilevel with a different enrolled_at does not match",
			pos:     enginePos(func(p *EnginePosition) { p.EnrolledAt = posEnrolled.Unix() + 1 }),
			payload: payloadFor(treeTypeUnilevel, nil),
			stored:  storedRow(2, ptr(posSponsor)),
			want:    false,
		},
		{
			name:    "unilevel with a different depth does not match",
			pos:     enginePos(nil),
			payload: payloadFor(treeTypeUnilevel, nil),
			stored:  storedRow(9, ptr(posSponsor)),
			want:    false,
		},
		{
			// The position rule names unilevel rather than naming binary and
			// matrix, so a fourth tree type is compared rather than skipped.
			// It then fails for want of a position, which is the same
			// fail-closed stance handleNodePlaced's default branch takes.
			name:    "an unknown tree type is compared, not skipped",
			pos:     enginePos(nil),
			payload: payloadFor("board", nil),
			stored:  storedRow(2, ptr(posSponsor)),
			want:    false,
		},
		{
			// The engine only ever receives EnrolledAt.Unix(), so a second is
			// the finest resolution it holds. Comparing more precisely would
			// make every redelivery diverge.
			name: "sub-second precision is below what the engine holds",
			pos:  enginePos(nil),
			payload: func() NodePlacedPayload {
				p := payloadFor(treeTypeBinary, intPtr(1))
				p.EnrolledAt = posEnrolled.Add(500 * time.Millisecond)
				return p
			}(),
			stored: storedRow(2, ptr(posSponsor)),
			want:   true,
		},
		{
			name:    "binary matches",
			pos:     enginePos(nil),
			payload: payloadFor(treeTypeBinary, intPtr(1)),
			stored:  storedRow(2, ptr(posSponsor)),
			want:    true,
		},
		{
			name:    "matrix matches",
			pos:     enginePos(func(p *EnginePosition) { p.Position = 5 }),
			payload: payloadFor(treeTypeMatrix, intPtr(5)),
			stored:  storedRow(2, ptr(posSponsor)),
			want:    true,
		},
		{
			name:    "the same uuid spelled in a different case matches",
			pos:     enginePos(func(p *EnginePosition) { p.UserID = strings.ToUpper(posUser) }),
			payload: payloadFor(treeTypeBinary, intPtr(1)),
			stored:  storedRow(2, ptr(posSponsor)),
			want:    true,
		},
		{
			name:    "a different user does not match",
			pos:     enginePos(func(p *EnginePosition) { p.UserID = posOther }),
			payload: payloadFor(treeTypeBinary, intPtr(1)),
			stored:  storedRow(2, ptr(posSponsor)),
			want:    false,
		},
		{
			name:    "a different parent does not match",
			pos:     enginePos(func(p *EnginePosition) { p.ParentUserID = ptr(posOther) }),
			payload: payloadFor(treeTypeBinary, intPtr(1)),
			stored:  storedRow(2, ptr(posSponsor)),
			want:    false,
		},
		{
			name:    "a sponsor the row does not name does not match",
			pos:     enginePos(func(p *EnginePosition) { p.SponsorUserID = ptr(posOther) }),
			payload: payloadFor(treeTypeBinary, intPtr(1)),
			stored:  storedRow(2, ptr(posSponsor)),
			want:    false,
		},
		{
			name:    "a different depth does not match",
			pos:     enginePos(nil),
			payload: payloadFor(treeTypeBinary, intPtr(1)),
			stored:  storedRow(3, ptr(posSponsor)),
			want:    false,
		},
		{
			name:    "a different enrolled_at does not match",
			pos:     enginePos(func(p *EnginePosition) { p.EnrolledAt = posEnrolled.Unix() + 1 }),
			payload: payloadFor(treeTypeBinary, intPtr(1)),
			stored:  storedRow(2, ptr(posSponsor)),
			want:    false,
		},
		{
			name:    "a different position does not match for binary",
			pos:     enginePos(func(p *EnginePosition) { p.Position = 0 }),
			payload: payloadFor(treeTypeBinary, intPtr(1)),
			stored:  storedRow(2, ptr(posSponsor)),
			want:    false,
		},
		{
			name:    "a different position does not match for matrix",
			pos:     enginePos(func(p *EnginePosition) { p.Position = 4 }),
			payload: payloadFor(treeTypeMatrix, intPtr(5)),
			stored:  storedRow(2, ptr(posSponsor)),
			want:    false,
		},
		{
			// Sponsor is read from the row, not the event. Removing a
			// sponsor re-sponsors their recruits in both the engine and the
			// store, and the placing event still names the original.
			// Comparing against the event would call this diverged.
			name:    "a re-sponsored node still matches its row",
			pos:     enginePos(func(p *EnginePosition) { p.SponsorUserID = ptr(posOther) }),
			payload: payloadFor(treeTypeBinary, intPtr(1)),
			stored:  storedRow(2, ptr(posOther)),
			want:    true,
		},
		{
			name:    "no stored row does not match",
			pos:     enginePos(nil),
			payload: payloadFor(treeTypeBinary, intPtr(1)),
			stored:  nil,
			want:    false,
		},
		{
			// Both sides absent agree. This is the only call site that can
			// reach samePtrUUID's nil-nil branch.
			name:    "a sponsor absent from both the engine and the row matches",
			pos:     enginePos(func(p *EnginePosition) { p.SponsorUserID = nil }),
			payload: payloadFor(treeTypeBinary, intPtr(1)),
			stored:  storedRow(2, nil),
			want:    true,
		},
		{
			name:    "a sponsor the engine holds but the row does not does not match",
			pos:     enginePos(nil),
			payload: payloadFor(treeTypeBinary, intPtr(1)),
			stored:  storedRow(2, nil),
			want:    false,
		},
		{
			// The engine holding a root where the event names a parent is a
			// real divergence, not a missing field.
			name:    "the engine holding no parent does not match",
			pos:     enginePos(func(p *EnginePosition) { p.ParentUserID = nil }),
			payload: payloadFor(treeTypeBinary, intPtr(1)),
			stored:  storedRow(2, ptr(posSponsor)),
			want:    false,
		},
		{
			name:    "the engine holding no sponsor does not match",
			pos:     enginePos(func(p *EnginePosition) { p.SponsorUserID = nil }),
			payload: payloadFor(treeTypeBinary, intPtr(1)),
			stored:  storedRow(2, ptr(posSponsor)),
			want:    false,
		},
		{
			name:    "no position at all does not match",
			pos:     nil,
			payload: payloadFor(treeTypeBinary, intPtr(1)),
			stored:  storedRow(2, ptr(posSponsor)),
			want:    false,
		},
		{
			// A binary payload reaching here with no position cannot be
			// confirmed. The comparison must report that rather than
			// dereferencing the nil.
			name:    "a binary payload with no position does not match",
			pos:     enginePos(nil),
			payload: payloadFor(treeTypeBinary, nil),
			stored:  storedRow(2, ptr(posSponsor)),
			want:    false,
		},
		{
			// Identifiers are compared as parsed UUIDs, so anything that is
			// not one cannot be confirmed equal to anything.
			name:    "an unparseable id from the engine does not match",
			pos:     enginePos(func(p *EnginePosition) { p.UserID = "not-a-uuid" }),
			payload: payloadFor(treeTypeBinary, intPtr(1)),
			stored:  storedRow(2, ptr(posSponsor)),
			want:    false,
		},
		{
			name: "an unparseable id in the payload does not match",
			pos:  enginePos(nil),
			payload: func() NodePlacedPayload {
				p := payloadFor(treeTypeBinary, intPtr(1))
				p.UserID = "not-a-uuid"
				return p
			}(),
			stored: storedRow(2, ptr(posSponsor)),
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, positionMatchesProjection(tt.pos, tt.payload, tt.stored))
		})
	}
}

// reconcileTransport fails the mutation with a chosen engine code and answers
// get_position separately, which is the pairing every reconcile path needs.
type reconcileTransport struct {
	mutationErr      error
	mutationErrs     []error
	mutationResponse json.RawMessage
	position         *EnginePosition
	positionErr      error
	mutationOps      []string
	positionOps      int
	positionAsked    []string
	onPosition       func()
	onMutation       func()
}

func (r *reconcileTransport) Call(_ context.Context, op string, params json.RawMessage) (json.RawMessage, error) {
	if op == "get_position" {
		r.positionOps++
		// Answer only for the user actually asked about. A transport that
		// returns the same position whoever is named lets the consumer
		// inspect the wrong user with every test still green.
		var q struct {
			UserID string `json:"user_id"`
		}
		if err := json.Unmarshal(params, &q); err != nil {
			return nil, err
		}
		r.positionAsked = append(r.positionAsked, q.UserID)
		if r.onPosition != nil {
			r.onPosition()
		}
		if r.positionErr != nil {
			return nil, r.positionErr
		}
		if r.position == nil || r.position.UserID != q.UserID {
			return nil, &EngineError{Code: engineCodeUserNotFound, Message: q.UserID}
		}
		return json.Marshal(r.position)
	}
	r.mutationOps = append(r.mutationOps, op)
	if r.onMutation != nil {
		r.onMutation()
	}
	if n := len(r.mutationOps) - 1; n < len(r.mutationErrs) {
		if e := r.mutationErrs[n]; e != nil {
			return nil, e
		}
	} else if r.mutationErr != nil {
		return nil, r.mutationErr
	}
	if r.mutationResponse != nil {
		return r.mutationResponse, nil
	}
	return json.RawMessage(`{"ok":true}`), nil
}

func (r *reconcileTransport) Close() error { return nil }

// seedParent puts the parent node_placed needs in the store, at depth 0, so
// the placed child derives depth 1.
func seedParent(t *testing.T, store *MemoryTreeStore) {
	t.Helper()
	require.NoError(t, store.InsertNode(context.Background(), TreeNodeRow{
		ID:         "cafe0000-0000-4000-8000-000000000001",
		TreeID:     "tree1",
		UserID:     posParent,
		Depth:      0,
		EnrolledAt: posEnrolled,
	}))
}

func placedPayload(treeType string, position *int) NodePlacedPayload {
	return NodePlacedPayload{
		TreeID:     "tree1",
		UserID:     posUser,
		ParentID:   posParent,
		SponsorID:  posSponsor,
		Position:   position,
		TreeType:   treeType,
		EnrolledAt: posEnrolled,
	}
}

// enginePlaced is the engine's view agreeing with placedPayload as projected.
func enginePlaced(mutate func(*EnginePosition)) *EnginePosition {
	p := &EnginePosition{
		UserID:        posUser,
		ParentUserID:  ptr(posParent),
		SponsorUserID: ptr(posSponsor),
		Position:      1,
		Depth:         1,
		EnrolledAt:    posEnrolled.Unix(),
	}
	if mutate != nil {
		mutate(p)
	}
	return p
}

func newReconcileConsumer(t *testing.T, tr *reconcileTransport) (*TreeEventConsumer, *MemoryTreeStore) {
	t.Helper()
	store := NewMemoryTreeStore()
	seedParent(t, store)
	c := NewTreeEventConsumer(store, newEngineClientWithTransport(tr))
	c.retryDelay = 0
	return c, store
}

// The case the ticket exists for: the mutation landed and the reply was lost,
// so the redelivery is told the user already exists.
func TestHandleNodePlaced_ReconcileConverges(t *testing.T) {
	tr := &reconcileTransport{
		mutationErr: &EngineError{Code: engineCodeUserAlreadyExists, Message: "taken"},
		position:    enginePlaced(nil),
	}
	c, _ := newReconcileConsumer(t, tr)

	err := c.HandleEvent(context.Background(), makeEvent(EventTypeNodePlaced, placedPayload(treeTypeBinary, intPtr(1))))

	require.NoError(t, err, "the engine already holds exactly what was projected")
	assert.Len(t, tr.mutationOps, 1, "converged does not retry")
}

func TestHandleNodePlaced_ReconcileDivergesOnADifferentPlacement(t *testing.T) {
	tr := &reconcileTransport{
		mutationErr: &EngineError{Code: engineCodeUserAlreadyExists},
		position:    enginePlaced(func(p *EnginePosition) { p.ParentUserID = ptr(posOther) }),
	}
	c, _ := newReconcileConsumer(t, tr)

	err := c.HandleEvent(context.Background(), makeEvent(EventTypeNodePlaced, placedPayload(treeTypeBinary, intPtr(1))))

	require.Error(t, err)
	assert.Contains(t, err.Error(), posUser, "the error names the user")
	assert.Contains(t, err.Error(), "tree1", "and the tree")
	assert.Len(t, tr.mutationOps, 1, "divergence is not retryable")
}

// An inspection that could not answer is not divergence. It retries, and the
// caller is told what failed to apply rather than what failed to inspect.
func TestHandleNodePlaced_ReconcileInconclusiveRetries(t *testing.T) {
	tr := &reconcileTransport{
		mutationErr: &EngineError{Code: engineCodeUserAlreadyExists},
		positionErr: errors.New("get_position timed out"),
	}
	c, _ := newReconcileConsumer(t, tr)

	err := c.HandleEvent(context.Background(), makeEvent(EventTypeNodePlaced, placedPayload(treeTypeBinary, intPtr(1))))

	require.Error(t, err)
	assert.Len(t, tr.mutationOps, c.maxRetries+1, "an inconclusive inspection retries")
	assert.Contains(t, err.Error(), engineCodeUserAlreadyExists, "the mutation that failed")
	assert.NotContains(t, err.Error(), "timed out", "not the inspection that could not check it")
}

// Any other engine failure is not reconcile's business, and inspecting it
// would cost a round trip on every transport hiccup.
func TestHandleNodePlaced_ReconcileSkipsOtherEngineErrors(t *testing.T) {
	tr := &reconcileTransport{
		mutationErr: &EngineError{Code: "PARENT_NOT_FOUND"},
		position:    enginePlaced(nil),
	}
	c, _ := newReconcileConsumer(t, tr)

	err := c.HandleEvent(context.Background(), makeEvent(EventTypeNodePlaced, placedPayload(treeTypeBinary, intPtr(1))))

	require.Error(t, err)
	assert.Len(t, tr.mutationOps, c.maxRetries+1)
	assert.Equal(t, 0, tr.positionOps, "no inspection for an error reconcile cannot speak to")
}

// Matrix placements go through add_node_at, a separate withRetry call. The
// closure has to reach both or half the handler is unreconciled.
func TestHandleNodePlaced_ReconcileCoversTheMatrixPath(t *testing.T) {
	tr := &reconcileTransport{
		mutationErr: &EngineError{Code: engineCodeUserAlreadyExists},
		position:    enginePlaced(func(p *EnginePosition) { p.Position = 2 }),
	}
	c, _ := newReconcileConsumer(t, tr)

	err := c.HandleEvent(context.Background(), makeEvent(EventTypeNodePlaced, placedPayload(treeTypeMatrix, intPtr(2))))

	require.NoError(t, err, "the matrix path reconciles too")
	assert.Equal(t, []string{"add_node_at"}, tr.mutationOps)
	assert.Equal(t, 1, tr.positionOps)
}

func rootPayload() RootAddedPayload {
	return RootAddedPayload{
		TreeID:     "tree1",
		UserID:     posUser,
		SponsorID:  posUser,
		EnrolledAt: posEnrolled,
	}
}

func newRootConsumer(tr *reconcileTransport) (*TreeEventConsumer, *MemoryTreeStore) {
	store := NewMemoryTreeStore()
	c := NewTreeEventConsumer(store, newEngineClientWithTransport(tr))
	c.retryDelay = 0
	return c, store
}

func activeRow(t *testing.T, store *MemoryTreeStore, userID string) *TreeNodeRow {
	t.Helper()
	row, err := store.GetNode(context.Background(), "tree1", userID)
	require.NoError(t, err)
	return row
}

// The root landed and the reply was lost.
func TestHandleRootAdded_ReconcileConverges(t *testing.T) {
	tr := &reconcileTransport{
		mutationErr: &EngineError{Code: engineCodeRootAlreadyExists},
		position:    &EnginePosition{UserID: posUser, Depth: 0, EnrolledAt: posEnrolled.Unix()},
	}
	c, store := newRootConsumer(tr)

	err := c.HandleEvent(context.Background(), makeEvent(EventTypeRootAdded, rootPayload()))

	require.NoError(t, err)
	assert.Len(t, tr.mutationOps, 1, "converged does not retry")
	assert.NotNil(t, activeRow(t, store, posUser), "the row this event wrote stays")
}

// The engine holds this user, but not as the root. The row this call inserted
// claims depth 0, and leaving it there gives the tree two depth-0 rows.
func TestHandleRootAdded_ReconcileCompensatesWhenTheUserIsNotTheRoot(t *testing.T) {
	tr := &reconcileTransport{
		mutationErr: &EngineError{Code: engineCodeUserAlreadyExists},
		position:    &EnginePosition{UserID: posUser, Depth: 3, EnrolledAt: posEnrolled.Unix()},
	}
	c, store := newRootConsumer(tr)

	err := c.HandleEvent(context.Background(), makeEvent(EventTypeRootAdded, rootPayload()))

	require.Error(t, err)
	assert.Nil(t, activeRow(t, store, posUser), "the row this call inserted is undone")
}

// The tree already has a root and it is someone else, so the engine does not
// hold this user at all. get_position answers USER_NOT_FOUND, which is a
// definite answer rather than a failed inspection.
func TestHandleRootAdded_ReconcileCompensatesWhenAnotherUserIsRoot(t *testing.T) {
	tr := &reconcileTransport{
		mutationErr: &EngineError{Code: engineCodeRootAlreadyExists},
		position:    &EnginePosition{UserID: posOther, Depth: 0, EnrolledAt: posEnrolled.Unix()},
	}
	c, store := newRootConsumer(tr)

	err := c.HandleEvent(context.Background(), makeEvent(EventTypeRootAdded, rootPayload()))

	require.Error(t, err)
	assert.Nil(t, activeRow(t, store, posUser),
		"two active depth-0 rows would wedge every later startup load")
	assert.Len(t, tr.mutationOps, 1, "a second root is not retryable")
}

// A transient inspection failure must not destroy a root. The row survives and
// the call retries.
func TestHandleRootAdded_ReconcileKeepsTheRowWhenInspectionFails(t *testing.T) {
	tr := &reconcileTransport{
		mutationErr: &EngineError{Code: engineCodeRootAlreadyExists},
		positionErr: errors.New("get_position timed out"),
	}
	c, store := newRootConsumer(tr)

	err := c.HandleEvent(context.Background(), makeEvent(EventTypeRootAdded, rootPayload()))

	require.Error(t, err)
	assert.NotNil(t, activeRow(t, store, posUser), "a timeout is not evidence the root is wrong")
	assert.Len(t, tr.mutationOps, c.maxRetries+1)
}

func TestHandleRootAdded_ReconcileSkipsOtherEngineErrors(t *testing.T) {
	tr := &reconcileTransport{
		mutationErr: &EngineError{Code: "TREE_NOT_FOUND"},
		position:    &EnginePosition{UserID: posUser, Depth: 0},
	}
	c, store := newRootConsumer(tr)

	err := c.HandleEvent(context.Background(), makeEvent(EventTypeRootAdded, rootPayload()))

	require.Error(t, err)
	assert.Equal(t, 0, tr.positionOps, "no inspection for an error reconcile cannot speak to")
	assert.NotNil(t, activeRow(t, store, posUser), "and no compensation either")
}

// deleteRecordingStore records which users the compensation deleted, so a test
// can assert the blast radius rather than only that this event's row is gone.
//
// It refuses a cancelled context, which MemoryTreeStore does not (HEU-798) and
// PostgresTreeStore does, because a pool honours it. Without that the memory
// double cannot tell a compensation shielded from cancellation from one that
// is not.
type deleteRecordingStore struct {
	*MemoryTreeStore
	deleted         []string
	attempts        []string
	responsors      []string
	wroteResponsors []string
}

func (c *deleteRecordingStore) DeleteNodeAndResponsor(
	ctx context.Context, treeID, userID, removalEventID string, moved []Responsored,
) error {
	c.responsors = append(c.responsors, userID)
	if err := ctx.Err(); err != nil {
		return err
	}
	c.wroteResponsors = append(c.wroteResponsors, userID)
	return c.MemoryTreeStore.DeleteNodeAndResponsor(ctx, treeID, userID, removalEventID, moved)
}

func (c *deleteRecordingStore) DeleteNode(ctx context.Context, treeID, userID string) error {
	c.attempts = append(c.attempts, userID)
	if err := ctx.Err(); err != nil {
		return err
	}
	c.deleted = append(c.deleted, userID)
	return c.MemoryTreeStore.DeleteNode(ctx, treeID, userID)
}

// The design requires the root's enrolment to match, not just the depth. A
// root re-baselined to a later event's enrolled_at moves every tenure window
// computed from it.
func TestHandleRootAdded_ReconcileDivergesOnADifferentEnrolledAt(t *testing.T) {
	tr := &reconcileTransport{
		mutationErr: &EngineError{Code: engineCodeRootAlreadyExists},
		position: &EnginePosition{
			UserID: posUser, Depth: 0,
			EnrolledAt: posEnrolled.Add(72 * time.Hour).Unix(),
		},
	}
	c, store := newRootConsumer(tr)

	err := c.HandleEvent(context.Background(), makeEvent(EventTypeRootAdded, rootPayload()))

	require.Error(t, err, "the engine's root enrolled at a different time is not this event's root")
	assert.Nil(t, activeRow(t, store, posUser))
}

// The compensation names a user. A delete that ignored it would undo whichever
// row it reached first, and every other test here runs against a store holding
// only this event's row.
// readTrippingStore fails the test if GetNodeIncludingRemoved is called. The
// success and insert-conflict paths must gain no query, and a passing test that
// never asserts the absence would not notice one appearing.
type readTrippingStore struct {
	*MemoryTreeStore
	t *testing.T
}

func (s readTrippingStore) GetNodeIncludingRemoved(context.Context, string, string) (*TreeNodeRow, error) {
	s.t.Helper()
	s.t.Fatal("the cancellation read ran on a path that should not reach it")
	return nil, nil
}

func TestHandleRootAdded_UncancelledPathsDoNotReadBack(t *testing.T) {
	t.Run("the insert succeeds and the engine accepts", func(t *testing.T) {
		store := readTrippingStore{MemoryTreeStore: NewMemoryTreeStore(), t: t}
		c := NewTreeEventConsumer(store, newEngineClientWithTransport(&reconcileTransport{}))
		c.retryDelay = 0

		require.NoError(t, c.HandleEvent(context.Background(), makeEvent(EventTypeRootAdded, rootPayload())))
	})

	t.Run("the engine fails without a cancellation", func(t *testing.T) {
		store := readTrippingStore{MemoryTreeStore: NewMemoryTreeStore(), t: t}
		tr := &reconcileTransport{mutationErr: &EngineError{Code: "TRANSPORT_HICCUP"}}
		c := NewTreeEventConsumer(store, newEngineClientWithTransport(tr))
		c.retryDelay = 0

		err := c.HandleEvent(context.Background(), makeEvent(EventTypeRootAdded, rootPayload()))

		require.Error(t, err)
		assert.NotErrorIs(t, err, context.Canceled)
		assert.NotErrorIs(t, err, context.DeadlineExceeded)
	})

	t.Run("the insert is refused by the root index", func(t *testing.T) {
		store := readTrippingStore{MemoryTreeStore: NewMemoryTreeStore(), t: t}
		ctx := context.Background()
		require.NoError(t, store.InsertNode(ctx, TreeNodeRow{
			ID: "cafe0000-0000-4000-8000-00000000beef", TreeID: "tree1",
			UserID: posOther, Depth: 0, EnrolledAt: posEnrolled,
		}))
		c := NewTreeEventConsumer(store, newEngineClientWithTransport(&reconcileTransport{}))
		c.retryDelay = 0

		require.ErrorIs(t, c.HandleEvent(ctx, makeEvent(EventTypeRootAdded, rootPayload())), ErrRootConflict)
	})
}

// ctxReadingStore fails a read on a cancelled context, and records every read
// attempt above that check so a refused read and a read that never happened are
// distinguishable.
type ctxReadingStore struct {
	*deleteRecordingStore
	reads []string
}

func (s *ctxReadingStore) GetNodeIncludingRemoved(ctx context.Context, treeID, userID string) (*TreeNodeRow, error) {
	s.reads = append(s.reads, userID)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.deleteRecordingStore.GetNodeIncludingRemoved(ctx, treeID, userID)
}

// A cancellation arriving before reconcile is entered leaves the row this
// delivery inserted. The error names the cancellation. It must also name what
// the store was observed to hold, and must claim nothing about the engine.
func TestHandleRootAdded_CancelledInsertReportsTheRow(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tr := &reconcileTransport{
		mutationErr: &EngineError{Code: engineCodeRootAlreadyExists},
		onMutation:  cancel,
	}
	store := &ctxReadingStore{deleteRecordingStore: &deleteRecordingStore{MemoryTreeStore: NewMemoryTreeStore()}}
	c := NewTreeEventConsumer(store, newEngineClientWithTransport(tr))
	// Not zero: at zero this test reports the cancellation only about half the
	// time. Nothing waits for the value, because ctx is already done.
	c.retryDelay = time.Minute

	// Held rather than inlined, so the assertion can name the event id the
	// row carries.
	event := makeEvent(EventTypeRootAdded, rootPayload())
	err := c.HandleEvent(ctx, event)

	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled, "the cancellation is still the cause")
	assert.Contains(t, err.Error(), "an active row carrying event", "the message says what the read returned")
	assert.Contains(t, err.Error(), event.ID, "and which row it was")
	assert.Equal(t, []string{posUser}, store.reads, "the read was attempted")
}

// An expired deadline must report the stored row the same way a cancellation
// does.
func TestHandleRootAdded_ExpiredDeadlineReportsTheRow(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	tr := &reconcileTransport{mutationErr: &EngineError{Code: engineCodeRootAlreadyExists}}
	store := &ctxReadingStore{deleteRecordingStore: &deleteRecordingStore{MemoryTreeStore: NewMemoryTreeStore()}}
	c := NewTreeEventConsumer(store, newEngineClientWithTransport(tr))
	// Not zero: at zero the select races its own timer.
	c.retryDelay = time.Minute

	event := makeEvent(EventTypeRootAdded, rootPayload())
	err := c.HandleEvent(ctx, event)

	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded, "the deadline is still the cause")
	assert.Contains(t, err.Error(), "an active row carrying event", "the message says what the read returned")
	assert.Contains(t, err.Error(), event.ID, "and which row it was")
	assert.Equal(t, []string{posUser}, store.reads, "the read was attempted")
}

// The defect's own scenario, after migration 000006. The insert is refused, so
// no engine call exists to be cancelled and there is nothing to compensate.
func TestHandleRootAdded_SecondRootRefusedBeforeTheEngine(t *testing.T) {
	tr := &reconcileTransport{}
	store := &deleteRecordingStore{MemoryTreeStore: NewMemoryTreeStore()}
	ctx := context.Background()
	require.NoError(t, store.InsertNode(ctx, TreeNodeRow{
		ID: "cafe0000-0000-4000-8000-00000000beef", TreeID: "tree1",
		UserID: posOther, Depth: 0, EnrolledAt: posEnrolled,
	}))
	c := NewTreeEventConsumer(store, newEngineClientWithTransport(tr))
	c.retryDelay = 0

	err := c.HandleEvent(ctx, makeEvent(EventTypeRootAdded, rootPayload()))

	require.ErrorIs(t, err, ErrRootConflict)
	assert.Empty(t, tr.mutationOps, "no engine call was made")
	assert.Empty(t, store.attempts, "nothing to compensate")
	assert.Nil(t, activeRow(t, store.MemoryTreeStore, posUser), "no row was written")
}

// preIndexStore accepts a second active depth-0 row, so a test can reach code
// that only runs once such a row exists.
type preIndexStore struct {
	*deleteRecordingStore
}

func (s preIndexStore) InsertNode(ctx context.Context, node TreeNodeRow) error {
	err := s.deleteRecordingStore.InsertNode(ctx, node)
	if errors.Is(err, ErrRootConflict) {
		return s.appendUnchecked(node)
	}
	return err
}

func TestHandleRootAdded_CompensationTouchesOnlyThisEventsRow(t *testing.T) {
	tr := &reconcileTransport{
		mutationErr: &EngineError{Code: engineCodeRootAlreadyExists},
		position:    &EnginePosition{UserID: posOther, Depth: 0, EnrolledAt: posEnrolled.Unix()},
	}
	store := preIndexStore{deleteRecordingStore: &deleteRecordingStore{MemoryTreeStore: NewMemoryTreeStore()}}
	ctx := context.Background()
	// The root that is really there, which this event must not disturb.
	require.NoError(t, store.InsertNode(ctx, TreeNodeRow{
		ID: "cafe0000-0000-4000-8000-00000000beef", TreeID: "tree1",
		UserID: posOther, Depth: 0, EnrolledAt: posEnrolled,
	}))
	c := NewTreeEventConsumer(store, newEngineClientWithTransport(tr))
	c.retryDelay = 0

	err := c.HandleEvent(ctx, makeEvent(EventTypeRootAdded, rootPayload()))

	require.Error(t, err)
	assert.Equal(t, []string{posUser}, store.deleted, "only this event's user is compensated")

	theirs, gerr := store.GetNode(ctx, "tree1", posOther)
	require.NoError(t, gerr)
	assert.NotNil(t, theirs, "the root that was already there survives")
}

// Attempt 0 returns a code reconcile ignores, attempt 1 the one it acts on.
// Compensation must still run exactly once.
func TestHandleRootAdded_CompensatesOnceAcrossAttempts(t *testing.T) {
	tr := &reconcileTransport{
		mutationErrs: []error{
			&EngineError{Code: "TRANSPORT_HICCUP"},
			&EngineError{Code: engineCodeRootAlreadyExists},
		},
		position: &EnginePosition{UserID: posOther, Depth: 0, EnrolledAt: posEnrolled.Unix()},
	}
	store := &deleteRecordingStore{MemoryTreeStore: NewMemoryTreeStore()}
	c := NewTreeEventConsumer(store, newEngineClientWithTransport(tr))
	c.retryDelay = 0

	err := c.HandleEvent(context.Background(), makeEvent(EventTypeRootAdded, rootPayload()))

	require.Error(t, err)
	assert.Len(t, tr.mutationOps, 2, "the first failure retried, the second diverged")
	assert.Len(t, store.deleted, 1, "compensation runs once, not once per attempt")
}

// A compensating action has to outlive the failure that triggered it. On the
// caller's context a shutdown mid-reconcile leaves two active depth-0 rows,
// and nothing later repairs them.
func TestHandleRootAdded_CompensationSurvivesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	tr := &reconcileTransport{
		mutationErr: &EngineError{Code: engineCodeRootAlreadyExists},
		position:    &EnginePosition{UserID: posOther, Depth: 0, EnrolledAt: posEnrolled.Unix()},
		onPosition:  cancel,
	}
	store := &deleteRecordingStore{MemoryTreeStore: NewMemoryTreeStore()}
	c := NewTreeEventConsumer(store, newEngineClientWithTransport(tr))
	c.retryDelay = 0

	err := c.HandleEvent(ctx, makeEvent(EventTypeRootAdded, rootPayload()))

	require.Error(t, err)
	assert.Equal(t, []string{posUser}, store.attempts, "the compensation was attempted")
	assert.Equal(t, []string{posUser}, store.deleted, "and was not refused by the cancelled context")
	assert.Nil(t, activeRow(t, store.MemoryTreeStore, posUser), "so the row is gone")
}

func removedPayload() NodeRemovedPayload {
	return NodeRemovedPayload{TreeID: "tree1", UserID: posUser, RemovedAt: posEnrolled}
}

// seedRemovable puts an active row in the store for the user the event removes.
func seedRemovable(t *testing.T, store *deleteRecordingStore) {
	t.Helper()
	require.NoError(t, store.InsertNode(context.Background(), TreeNodeRow{
		ID: "cafe0000-0000-4000-8000-0000000000aa", TreeID: "tree1",
		UserID: posUser, Depth: 1, ParentID: ptr(posParent), EnrolledAt: posEnrolled,
	}))
}

func newRemovalConsumer(tr *reconcileTransport) (*TreeEventConsumer, *deleteRecordingStore) {
	store := &deleteRecordingStore{MemoryTreeStore: NewMemoryTreeStore()}
	c := NewTreeEventConsumer(store, newEngineClientWithTransport(tr))
	c.retryDelay = 0
	return c, store
}

// The removal ran to completion and the event came back. The engine no longer
// holds the user and the store's row is already a tombstone.
func TestHandleNodeRemoved_ReconcileConvergesOnAProjectedRemoval(t *testing.T) {
	tr := &reconcileTransport{mutationErr: &EngineError{Code: engineCodeUserNotFound}}
	c, store := newRemovalConsumer(tr)
	ctx := context.Background()
	seedRemovable(t, store)
	require.NoError(t, store.DeleteNode(ctx, "tree1", posUser))
	store.deleted = nil

	err := c.HandleEvent(ctx, makeEvent(EventTypeNodeRemoved, removedPayload()))

	require.NoError(t, err, "the whole event was already projected")
	assert.Empty(t, store.responsors, "the store write must not run a second time")
	assert.Len(t, tr.mutationOps, 1, "converged does not retry")
}

// The engine applied the removal and the store write never landed. The moved
// list is gone with the reply, so this cannot be repaired here.
func TestHandleNodeRemoved_ReconcileFailsWhenAnActiveRowRemains(t *testing.T) {
	tr := &reconcileTransport{mutationErr: &EngineError{Code: engineCodeUserNotFound}}
	c, store := newRemovalConsumer(tr)
	seedRemovable(t, store)

	err := c.HandleEvent(context.Background(), makeEvent(EventTypeNodeRemoved, removedPayload()))

	var target *RemovalNotProjectedError
	require.ErrorAs(t, err, &target)
	assert.Equal(t, posUser, target.UserID)
	assert.Equal(t, "tree1", target.TreeID)
	assert.NotEmpty(t, target.EventID, "the error carries the event that could not be completed")
	assert.Empty(t, store.responsors, "an unrepairable removal writes nothing")
}

// Neither side holds the user. There is nothing to remove and nothing to
// disagree about.
func TestHandleNodeRemoved_ReconcileConvergesWhenNoRowExists(t *testing.T) {
	tr := &reconcileTransport{mutationErr: &EngineError{Code: engineCodeUserNotFound}}
	c, store := newRemovalConsumer(tr)

	err := c.HandleEvent(context.Background(), makeEvent(EventTypeNodeRemoved, removedPayload()))

	require.NoError(t, err)
	assert.Empty(t, store.responsors)
}

func TestHandleNodeRemoved_ReconcileSkipsOtherEngineErrors(t *testing.T) {
	tr := &reconcileTransport{mutationErr: &EngineError{Code: "HAS_CHILDREN"}}
	c, store := newRemovalConsumer(tr)
	seedRemovable(t, store)

	err := c.HandleEvent(context.Background(), makeEvent(EventTypeNodeRemoved, removedPayload()))

	require.Error(t, err)
	assert.Len(t, tr.mutationOps, c.maxRetries+1, "an error reconcile cannot speak to still retries")
	assert.Empty(t, store.responsors)
}

// The ordinary path is unchanged: the engine removes, then the store writes.
func TestHandleNodeRemoved_SucceedsAndWritesTheStore(t *testing.T) {
	tr := &reconcileTransport{mutationResponse: json.RawMessage(`{"responsored":[]}`)}
	c, store := newRemovalConsumer(tr)
	seedRemovable(t, store)

	err := c.HandleEvent(context.Background(), makeEvent(EventTypeNodeRemoved, removedPayload()))

	require.NoError(t, err)
	assert.Equal(t, []string{posUser}, store.responsors, "the store write still runs")
	assert.Nil(t, activeRow(t, store.MemoryTreeStore, posUser))
}

func TestHandleNodeRemoved_StampsTheTombstoneWithTheEventID(t *testing.T) {
	tr := &reconcileTransport{mutationResponse: json.RawMessage(`{"responsored":[]}`)}
	c, store := newRemovalConsumer(tr)
	seedRemovable(t, store)
	event := makeEvent(EventTypeNodeRemoved, removedPayload())

	require.NoError(t, c.HandleEvent(context.Background(), event))

	tomb, err := store.GetNodeIncludingRemoved(context.Background(), "tree1", posUser)
	require.NoError(t, err)
	require.NotNil(t, tomb)
	require.NotNil(t, tomb.RemovedByEventID, "the tombstone carries a removal stamp")
	assert.Equal(t, event.ID, *tomb.RemovedByEventID)
}

// The engine has already applied the removal and its reply carried the only
// copy of moved, so giving up on the store write is not a clean abort. It is
// the divergence HEU-777 owns, and a shutdown landing in this window must not
// be what causes it.
func TestHandleNodeRemoved_StoreWriteSurvivesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	tr := &reconcileTransport{
		mutationResponse: json.RawMessage(`{"responsored":[]}`),
		onMutation:       cancel,
	}
	store := &deleteRecordingStore{MemoryTreeStore: NewMemoryTreeStore()}
	c := NewTreeEventConsumer(store, newEngineClientWithTransport(tr))
	c.retryDelay = 0
	seedRemovable(t, store)

	err := c.HandleEvent(ctx, makeEvent(EventTypeNodeRemoved, removedPayload()))

	require.NoError(t, err, "the removal completes even though the caller gave up")
	assert.Equal(t, []string{posUser}, store.wroteResponsors, "the store write landed")
	assert.Nil(t, activeRow(t, store.MemoryTreeStore, posUser))
}

// The skipped-insert guard.

const supersedingEventID = "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"

// seedRow writes a placed row straight into the store, tombstone included.
func seedRow(t *testing.T, store *MemoryTreeStore, id, userID string, removedAt *time.Time) {
	t.Helper()
	require.NoError(t, store.InsertNode(context.Background(), TreeNodeRow{
		ID:         id,
		TreeID:     "tree1",
		UserID:     userID,
		ParentID:   ptr(posParent),
		SponsorID:  ptr(posSponsor),
		Depth:      1,
		EnrolledAt: posEnrolled,
		RemovedAt:  removedAt,
	}))
}

// seedRootRow writes a depth-0 row with no parent, the shape handleRootAdded
// stores.
func seedRootRow(t *testing.T, store *MemoryTreeStore, id, userID string, removedAt *time.Time) {
	t.Helper()
	require.NoError(t, store.InsertNode(context.Background(), TreeNodeRow{
		ID:         id,
		TreeID:     "tree1",
		UserID:     userID,
		SponsorID:  ptr(posSponsor),
		Depth:      0,
		EnrolledAt: posEnrolled,
		RemovedAt:  removedAt,
	}))
}

// readFailingStore fails the tombstone-aware read and leaves every other
// method to the embedded store.
type readFailingStore struct {
	*MemoryTreeStore
	err error
}

func (s *readFailingStore) GetNodeIncludingRemoved(_ context.Context, _, _ string) (*TreeNodeRow, error) {
	return nil, s.err
}

// Exactly one tombstone, so the read cannot return someone else's row and
// pass this test for the wrong reason.
func TestHandleNodePlaced_ReplayedAfterRemoval(t *testing.T) {
	tr := &reconcileTransport{}
	c, store := newReconcileConsumer(t, tr)
	ev := makeEvent(EventTypeNodePlaced, placedPayload(treeTypeBinary, intPtr(1)))
	removed := posEnrolled.Add(time.Hour)
	seedRow(t, store, ev.ID, posUser, &removed)

	err := c.HandleEvent(context.Background(), ev)

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrReplayedPlacement)
	assert.Contains(t, err.Error(), "removed at", "the read returned a tombstone, not an absent or active row")
	assert.Empty(t, tr.mutationOps, "a replayed placement never reaches the engine")
}

func TestHandleNodePlaced_SkippedInsertOfThisEventsActiveRowReachesTheEngine(t *testing.T) {
	tr := &reconcileTransport{}
	c, store := newReconcileConsumer(t, tr)
	ev := makeEvent(EventTypeNodePlaced, placedPayload(treeTypeBinary, intPtr(1)))
	seedRow(t, store, ev.ID, posUser, nil)

	err := c.HandleEvent(context.Background(), ev)

	require.NoError(t, err)
	assert.Equal(t, []string{"add_node"}, tr.mutationOps, "the lost-reply case still reaches the engine")
}

// The colliding event ID belongs to another user, so the read for this tree
// and user finds nothing at all.
func TestHandleNodePlaced_SkippedInsertWithNoRowForThisUser(t *testing.T) {
	tr := &reconcileTransport{}
	c, store := newReconcileConsumer(t, tr)
	ev := makeEvent(EventTypeNodePlaced, placedPayload(treeTypeBinary, intPtr(1)))
	seedRow(t, store, ev.ID, posOther, nil)

	err := c.HandleEvent(context.Background(), ev)

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrReplayedPlacement)
	assert.Contains(t, err.Error(), "no row", "the message reports what the read returned")
	assert.Empty(t, tr.mutationOps, "the engine is not called")
}

// A later event placed this user again, so the read returns that row rather
// than the tombstone the insert collided with.
func TestHandleNodePlaced_SupersededByALaterPlacement(t *testing.T) {
	tr := &reconcileTransport{}
	c, store := newReconcileConsumer(t, tr)
	ev := makeEvent(EventTypeNodePlaced, placedPayload(treeTypeBinary, intPtr(1)))
	removed := posEnrolled.Add(time.Hour)
	seedRow(t, store, ev.ID, posUser, &removed)
	seedRow(t, store, supersedingEventID, posUser, nil)

	err := c.HandleEvent(context.Background(), ev)

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrReplayedPlacement)
	assert.Contains(t, err.Error(), supersedingEventID, "the message names the row the read returned")
	assert.Empty(t, tr.mutationOps, "a superseded placement never reaches the engine")
}

// Both rows are tombstones, so the read returns the more recent one and this
// event's own tombstone is invisible.
func TestHandleNodePlaced_SupersededAndRemovedAgain(t *testing.T) {
	tr := &reconcileTransport{}
	c, store := newReconcileConsumer(t, tr)
	ev := makeEvent(EventTypeNodePlaced, placedPayload(treeTypeBinary, intPtr(1)))
	early := posEnrolled.Add(time.Hour)
	late := posEnrolled.Add(2 * time.Hour)
	seedRow(t, store, ev.ID, posUser, &early)
	seedRow(t, store, supersedingEventID, posUser, &late)

	err := c.HandleEvent(context.Background(), ev)

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrReplayedPlacement)
	assert.Contains(t, err.Error(), supersedingEventID, "the message names the row the read returned")
	assert.Empty(t, tr.mutationOps, "the engine is not called")
}

func TestHandleRootAdded_ReplayedAfterRemoval(t *testing.T) {
	tr := &reconcileTransport{}
	c, store := newRootConsumer(tr)
	ev := makeEvent(EventTypeRootAdded, rootPayload())
	removed := posEnrolled.Add(time.Hour)
	seedRootRow(t, store, ev.ID, posUser, &removed)

	err := c.HandleEvent(context.Background(), ev)

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrReplayedPlacement)
	assert.Contains(t, err.Error(), "removed at", "the read returned a tombstone, not an absent or active row")
	assert.Empty(t, tr.mutationOps, "a replayed root never reaches the engine")
}

func TestHandleRootAdded_SkippedInsertOfThisEventsActiveRowReachesTheEngine(t *testing.T) {
	tr := &reconcileTransport{}
	c, store := newRootConsumer(tr)
	ev := makeEvent(EventTypeRootAdded, rootPayload())
	seedRootRow(t, store, ev.ID, posUser, nil)

	err := c.HandleEvent(context.Background(), ev)

	require.NoError(t, err)
	assert.Equal(t, []string{"add_root"}, tr.mutationOps, "the lost-reply case still reaches the engine")
}

// A redelivered root whose row is already stored must not lose that row when
// the engine disagrees. The row is durable and depth 0, and a tree with no
// active depth-0 row stops loading.
func TestHandleRootAdded_ReplayKeepsTheStoredRowWhenTheEngineDisagrees(t *testing.T) {
	tr := &reconcileTransport{
		mutationErr: &EngineError{Code: engineCodeRootAlreadyExists},
		position:    &EnginePosition{UserID: posUser, Depth: 0, EnrolledAt: posEnrolled.Unix() + 1},
	}
	c, store := newRootConsumer(tr)
	ev := makeEvent(EventTypeRootAdded, rootPayload())
	seedRootRow(t, store, ev.ID, posUser, nil)

	err := c.HandleEvent(context.Background(), ev)

	require.Error(t, err, "a disagreeing engine is still a divergence")
	assert.NotNil(t, activeRow(t, store, posUser), "the stored row survives a replay this call did not insert")
}

func TestHandleNodePlaced_SkippedInsertReportsAFailedRead(t *testing.T) {
	tr := &reconcileTransport{}
	backing := NewMemoryTreeStore()
	seedParent(t, backing)
	store := &readFailingStore{MemoryTreeStore: backing, err: errors.New("read timed out")}
	c := NewTreeEventConsumer(store, newEngineClientWithTransport(tr))
	c.retryDelay = 0
	ev := makeEvent(EventTypeNodePlaced, placedPayload(treeTypeBinary, intPtr(1)))
	seedRow(t, backing, ev.ID, posUser, nil)

	err := c.HandleEvent(context.Background(), ev)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "read timed out", "the read failure reaches the caller")
	assert.NotErrorIs(t, err, ErrReplayedPlacement, "a failed read is not a refusal")
	assert.ErrorIs(t, err, ErrNodeAlreadyProjected, "and it still carries the insert that was skipped")
	assert.Empty(t, tr.mutationOps, "the engine is not called on an unanswered read")
}

// The assembled refusal message is pinned as it reads today, which is wrong on
// three of these four cases: it opens by claiming a soft-deleted row with this
// event's id and then reports something else. HEU-804 owns the wording and it
// is unruled, so this pins the string rather than endorsing it. Rewording the
// message is meant to fail here, so that the change is a decision rather than
// a side effect.
//
// The event id is fixed rather than taken from makeEvent, whose counter moves
// with test selection, and RemovedAt is fixed so the rendered timestamp does
// not move between runs. Both are pinned at the fixture so the assertion can
// be on the whole string.
func TestCheckReplayedInsert_MessageText(t *testing.T) {
	const evID = "ffffffff-ffff-4fff-8fff-ffffffffffff"
	early := posEnrolled.Add(time.Hour)
	late := posEnrolled.Add(2 * time.Hour)
	skipped := "(insert affected no rows; a row with this event id exists: id=" + evID + ")"

	cases := []struct {
		name  string
		seed  func(*testing.T, *MemoryTreeStore)
		reads string
	}{
		{
			name:  "no row for this tree and user",
			seed:  func(t *testing.T, s *MemoryTreeStore) { seedRow(t, s, evID, posOther, nil) },
			reads: "no row",
		},
		{
			name:  "this event's own tombstone",
			seed:  func(t *testing.T, s *MemoryTreeStore) { seedRow(t, s, evID, posUser, &early) },
			reads: "a row carrying event " + evID + ", removed at 2026-03-04T06:06:07Z",
		},
		{
			name: "a later event holds an active row",
			seed: func(t *testing.T, s *MemoryTreeStore) {
				seedRow(t, s, evID, posUser, &early)
				seedRow(t, s, supersedingEventID, posUser, nil)
			},
			reads: "an active row carrying event " + supersedingEventID,
		},
		{
			name: "a later event's tombstone",
			seed: func(t *testing.T, s *MemoryTreeStore) {
				seedRow(t, s, evID, posUser, &early)
				seedRow(t, s, supersedingEventID, posUser, &late)
			},
			reads: "a row carrying event " + supersedingEventID + ", removed at 2026-03-04T07:06:07Z",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := &reconcileTransport{}
			c, store := newReconcileConsumer(t, tr)
			tc.seed(t, store)
			ev := makeEvent(EventTypeNodePlaced, placedPayload(treeTypeBinary, intPtr(1)))
			ev.ID = evID

			err := c.HandleEvent(context.Background(), ev)

			require.Error(t, err)
			assert.Equal(t,
				"a row with this event id exists and is soft-deleted: the read for "+
					posUser+" in tree tree1 returned "+tc.reads+" "+skipped,
				err.Error())
		})
	}
}
