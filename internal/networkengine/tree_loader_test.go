package networkengine

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// orderRecordingTransport records ops in order for verification.
type orderRecordingTransport struct {
	ops      []string
	response json.RawMessage
}

func newOrderRecordingTransport() *orderRecordingTransport {
	return &orderRecordingTransport{
		response: json.RawMessage(`{"ok":true}`),
	}
}

func (o *orderRecordingTransport) Call(_ context.Context, op string, _ json.RawMessage) (json.RawMessage, error) {
	o.ops = append(o.ops, op)
	return o.response, nil
}

func (o *orderRecordingTransport) Close() error { return nil }

func TestTreeLoader_LoadEmptyTree(t *testing.T) {
	store := NewMemoryTreeStore()
	transport := newOrderRecordingTransport()
	engine := NewEngineClientWithTransport(transport)
	loader := NewTreeLoader(store, engine)

	err := loader.LoadTree(context.Background(), "tree-1", "unilevel")
	require.NoError(t, err)
	assert.Empty(t, transport.ops, "no engine calls for empty tree")
}

func TestTreeLoader_LoadSingleRoot(t *testing.T) {
	store := NewMemoryTreeStore()
	root := makeNode("tree-1", "root-user", 0, nil, ptr("root-user"), nil)
	root.EnrolledAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, store.InsertNode(context.Background(), root))

	transport := newOrderRecordingTransport()
	engine := NewEngineClientWithTransport(transport)
	loader := NewTreeLoader(store, engine)

	err := loader.LoadTree(context.Background(), "tree-1", "unilevel")
	require.NoError(t, err)

	require.Len(t, transport.ops, 2)
	assert.Equal(t, "create_tree", transport.ops[0])
	assert.Equal(t, "add_root", transport.ops[1])
}

func TestTreeLoader_LoadChain(t *testing.T) {
	store := NewMemoryTreeStore()
	ctx := context.Background()
	enrolled := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	root := makeNode("tree-1", "user-0", 0, nil, ptr("user-0"), nil)
	root.EnrolledAt = enrolled
	require.NoError(t, store.InsertNode(ctx, root))

	node1 := makeNode("tree-1", "user-1", 1, ptr("user-0"), ptr("user-0"), nil)
	node1.EnrolledAt = enrolled.Add(time.Hour)
	require.NoError(t, store.InsertNode(ctx, node1))

	node2 := makeNode("tree-1", "user-2", 2, ptr("user-1"), ptr("user-1"), nil)
	node2.EnrolledAt = enrolled.Add(2 * time.Hour)
	require.NoError(t, store.InsertNode(ctx, node2))

	node3 := makeNode("tree-1", "user-3", 3, ptr("user-2"), ptr("user-2"), nil)
	node3.EnrolledAt = enrolled.Add(3 * time.Hour)
	require.NoError(t, store.InsertNode(ctx, node3))

	transport := newOrderRecordingTransport()
	engine := NewEngineClientWithTransport(transport)
	loader := NewTreeLoader(store, engine)

	err := loader.LoadTree(ctx, "tree-1", "unilevel")
	require.NoError(t, err)

	// create_tree + add_root + 3 add_node = 5 calls.
	require.Len(t, transport.ops, 5)
	assert.Equal(t, "create_tree", transport.ops[0])
	assert.Equal(t, "add_root", transport.ops[1])
	assert.Equal(t, "add_node", transport.ops[2])
	assert.Equal(t, "add_node", transport.ops[3])
	assert.Equal(t, "add_node", transport.ops[4])
}

func TestTreeLoader_SkipsRemovedNodes(t *testing.T) {
	store := NewMemoryTreeStore()
	ctx := context.Background()
	enrolled := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	root := makeNode("tree-1", "user-0", 0, nil, ptr("user-0"), nil)
	root.EnrolledAt = enrolled
	require.NoError(t, store.InsertNode(ctx, root))

	child := makeNode("tree-1", "user-1", 1, ptr("user-0"), ptr("user-0"), nil)
	child.EnrolledAt = enrolled.Add(time.Hour)
	require.NoError(t, store.InsertNode(ctx, child))

	// Soft-delete the child.
	require.NoError(t, store.DeleteNode(ctx, "tree-1", "user-1"))

	transport := newOrderRecordingTransport()
	engine := NewEngineClientWithTransport(transport)
	loader := NewTreeLoader(store, engine)

	err := loader.LoadTree(ctx, "tree-1", "unilevel")
	require.NoError(t, err)

	// Only root should be loaded: create_tree + add_root.
	require.Len(t, transport.ops, 2)
	assert.Equal(t, "create_tree", transport.ops[0])
	assert.Equal(t, "add_root", transport.ops[1])
}
