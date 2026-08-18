package networkengine

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEngineClient_StartAndPing(t *testing.T) {
	client, err := NewEngineClient(context.Background(), findWorkerBinary(t))
	require.NoError(t, err)
	defer func() { _ = client.Stop() }()

	err = client.Ping(context.Background())
	require.NoError(t, err)
}

func TestEngineClient_StopIsIdempotent(t *testing.T) {
	client, err := NewEngineClient(context.Background(), findWorkerBinary(t))
	require.NoError(t, err)

	err = client.Stop()
	require.NoError(t, err)

	// Second stop should not panic or return a surprising error.
	// The underlying process is already gone, so we accept any error.
	_ = client.Stop()
}

func TestEngineClient_WithMockTransport(t *testing.T) {
	mock := &mockTransport{
		response: json.RawMessage(`"pong"`),
	}
	client := NewEngineClientWithTransport(mock)

	err := client.Ping(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "ping", mock.lastOp)
}

func TestEngineClient_LoadPlan(t *testing.T) {
	mock := &mockTransport{
		response: json.RawMessage(`null`),
	}
	client := NewEngineClientWithTransport(mock)

	planJSON := json.RawMessage(`{"structures":[]}`)
	err := client.LoadPlan(context.Background(), planJSON)
	require.NoError(t, err)

	assert.Equal(t, "load_plan", mock.lastOp)
	assert.JSONEq(t, `{"structures":[]}`, string(mock.lastParams))
}

func TestEngineClient_CallMarshalError(t *testing.T) {
	mock := &mockTransport{
		response: json.RawMessage(`null`),
	}
	client := NewEngineClientWithTransport(mock)

	// Channels cannot be marshaled to JSON.
	_, err := client.call(context.Background(), "test", make(chan int))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "marshal params")
}

// --- Tree lifecycle tests (mock) ---

func TestEngineClient_CreateTree_MockParams(t *testing.T) {
	mock := &mockTransport{
		response: json.RawMessage(`{"created":true}`),
	}
	client := NewEngineClientWithTransport(mock)

	err := client.CreateTree(context.Background(), "Test", "unilevel")
	require.NoError(t, err)

	assert.Equal(t, "create_tree", mock.lastOp)
	assert.JSONEq(t, `{"structure":"Test","tree_type":"unilevel"}`, string(mock.lastParams))
}

func TestEngineClient_CreateMatrixTree_MockParams(t *testing.T) {
	mock := &mockTransport{
		response: json.RawMessage(`{"created":true}`),
	}
	client := NewEngineClientWithTransport(mock)

	err := client.CreateMatrixTree(context.Background(), "Test", 3, "breadth_first")
	require.NoError(t, err)

	// The matrix path reuses the create_tree op but must carry width and
	// spillover, or the Rust worker rejects it with MISSING_PARAM.
	assert.Equal(t, "create_tree", mock.lastOp)
	assert.JSONEq(t, `{"structure":"Test","tree_type":"matrix","width":3,"spillover":"breadth_first"}`, string(mock.lastParams))
}

// --- Tree mutation tests (mock) ---

func TestEngineClient_AddRoot_MockParams(t *testing.T) {
	mock := &mockTransport{
		response: json.RawMessage(`{"added":true}`),
	}
	client := NewEngineClientWithTransport(mock)

	err := client.AddRoot(context.Background(), "Test", "00000000-0000-0000-0000-000000000001", 100)
	require.NoError(t, err)

	assert.Equal(t, "add_root", mock.lastOp)
	assert.JSONEq(t, `{"structure":"Test","user_id":"00000000-0000-0000-0000-000000000001","enrolled_at":100}`, string(mock.lastParams))
}

func TestEngineClient_AddNode_MockParams(t *testing.T) {
	mock := &mockTransport{
		response: json.RawMessage(`{"added":true}`),
	}
	client := NewEngineClientWithTransport(mock)

	// Parent and sponsor are distinct so a transposition fails here. Both are
	// strings, so the swap would otherwise compile and pass silently.
	err := client.AddNode(context.Background(), "Test",
		"00000000-0000-0000-0000-000000000002",
		"00000000-0000-0000-0000-000000000001",
		"00000000-0000-0000-0000-000000000003",
		200)
	require.NoError(t, err)

	assert.Equal(t, "add_node", mock.lastOp)
	assert.JSONEq(t, `{
		"structure":"Test",
		"user_id":"00000000-0000-0000-0000-000000000002",
		"parent_id":"00000000-0000-0000-0000-000000000001",
		"sponsor_id":"00000000-0000-0000-0000-000000000003",
		"enrolled_at":200
	}`, string(mock.lastParams))
}

func TestEngineClient_AddNode_WithPosition(t *testing.T) {
	mock := &mockTransport{
		response: json.RawMessage(`{"added":true}`),
	}
	client := NewEngineClientWithTransport(mock)

	err := client.AddNode(context.Background(), "Binary",
		"00000000-0000-0000-0000-000000000002",
		"00000000-0000-0000-0000-000000000001",
		"00000000-0000-0000-0000-000000000001",
		200, WithPosition(0))
	require.NoError(t, err)

	assert.Equal(t, "add_node", mock.lastOp)
	assert.JSONEq(t, `{
		"structure":"Binary",
		"user_id":"00000000-0000-0000-0000-000000000002",
		"parent_id":"00000000-0000-0000-0000-000000000001",
		"sponsor_id":"00000000-0000-0000-0000-000000000001",
		"enrolled_at":200,
		"position":0
	}`, string(mock.lastParams))
}

func TestEngineClient_RemoveNode_MockParams(t *testing.T) {
	mock := &mockTransport{
		response: json.RawMessage(`{"removed":true}`),
	}
	client := NewEngineClientWithTransport(mock)

	err := client.RemoveNode(context.Background(), "Test", "00000000-0000-0000-0000-000000000001")
	require.NoError(t, err)

	assert.Equal(t, "remove_node", mock.lastOp)
	assert.JSONEq(t, `{"structure":"Test","user_id":"00000000-0000-0000-0000-000000000001"}`, string(mock.lastParams))
}

// --- Tree query tests (mock) ---

func TestEngineClient_GetParent_ReturnsNode(t *testing.T) {
	mock := &mockTransport{
		response: json.RawMessage(`{"user_id":"00000000-0000-0000-0000-000000000001","depth":0,"enrolled_at":100}`),
	}
	client := NewEngineClientWithTransport(mock)

	node, err := client.GetParent(context.Background(), "Test", "00000000-0000-0000-0000-000000000002")
	require.NoError(t, err)
	require.NotNil(t, node)

	assert.Equal(t, "00000000-0000-0000-0000-000000000001", node.UserID)
	assert.Equal(t, uint32(0), node.Depth)
	assert.Equal(t, int64(100), node.EnrolledAt)
	assert.Equal(t, "get_parent", mock.lastOp)
}

func TestEngineClient_GetParent_ReturnsNilForRoot(t *testing.T) {
	mock := &mockTransport{
		response: json.RawMessage(`null`),
	}
	client := NewEngineClientWithTransport(mock)

	node, err := client.GetParent(context.Background(), "Test", "00000000-0000-0000-0000-000000000001")
	require.NoError(t, err)
	assert.Nil(t, node)
}

func TestEngineClient_GetChildren_ReturnsNodes(t *testing.T) {
	mock := &mockTransport{
		response: json.RawMessage(`[{"user_id":"00000000-0000-0000-0000-000000000002","depth":1,"enrolled_at":200}]`),
	}
	client := NewEngineClientWithTransport(mock)

	nodes, err := client.GetChildren(context.Background(), "Test", "00000000-0000-0000-0000-000000000001")
	require.NoError(t, err)
	require.Len(t, nodes, 1)

	assert.Equal(t, "00000000-0000-0000-0000-000000000002", nodes[0].UserID)
	assert.Equal(t, uint32(1), nodes[0].Depth)
	assert.Equal(t, int64(200), nodes[0].EnrolledAt)
}

func TestEngineClient_GetChildren_EmptyList(t *testing.T) {
	mock := &mockTransport{
		response: json.RawMessage(`[]`),
	}
	client := NewEngineClientWithTransport(mock)

	nodes, err := client.GetChildren(context.Background(), "Test", "00000000-0000-0000-0000-000000000001")
	require.NoError(t, err)
	assert.Empty(t, nodes)
}

func TestEngineClient_GetUpline_MockParams(t *testing.T) {
	mock := &mockTransport{
		response: json.RawMessage(`[{"user_id":"00000000-0000-0000-0000-000000000001","depth":0,"enrolled_at":100}]`),
	}
	client := NewEngineClientWithTransport(mock)

	nodes, err := client.GetUpline(context.Background(), "Test", "00000000-0000-0000-0000-000000000002", 0)
	require.NoError(t, err)
	require.Len(t, nodes, 1)

	assert.Equal(t, "get_upline", mock.lastOp)
	assert.JSONEq(t, `{"structure":"Test","user_id":"00000000-0000-0000-0000-000000000002","depth":0}`, string(mock.lastParams))
}

func TestEngineClient_GetDownline_MockParams(t *testing.T) {
	mock := &mockTransport{
		response: json.RawMessage(`[{"user_id":"00000000-0000-0000-0000-000000000002","depth":1,"enrolled_at":200}]`),
	}
	client := NewEngineClientWithTransport(mock)

	nodes, err := client.GetDownline(context.Background(), "Test", "00000000-0000-0000-0000-000000000001", 0)
	require.NoError(t, err)
	require.Len(t, nodes, 1)

	assert.Equal(t, "get_downline", mock.lastOp)
	assert.JSONEq(t, `{"structure":"Test","user_id":"00000000-0000-0000-0000-000000000001","depth":0}`, string(mock.lastParams))
}

func TestEngineClient_GetPosition_MockResponse(t *testing.T) {
	mock := &mockTransport{
		response: json.RawMessage(`{
			"user_id":"00000000-0000-0000-0000-000000000002",
			"parent_user_id":"00000000-0000-0000-0000-000000000001",
			"sponsor_user_id":"00000000-0000-0000-0000-000000000001",
			"position":0,
			"depth":1,
			"child_count":0,
			"downline_counts":{},
			"enrolled_at":200
		}`),
	}
	client := NewEngineClientWithTransport(mock)

	pos, err := client.GetPosition(context.Background(), "Test", "00000000-0000-0000-0000-000000000002")
	require.NoError(t, err)

	assert.Equal(t, "00000000-0000-0000-0000-000000000002", pos.UserID)
	assert.NotNil(t, pos.ParentUserID)
	assert.Equal(t, "00000000-0000-0000-0000-000000000001", *pos.ParentUserID)
	assert.NotNil(t, pos.SponsorUserID)
	assert.Equal(t, "00000000-0000-0000-0000-000000000001", *pos.SponsorUserID)
	assert.Equal(t, 0, pos.Position)
	assert.Equal(t, uint32(1), pos.Depth)
	assert.Equal(t, 0, pos.ChildCount)
	assert.Empty(t, pos.DownlineCounts)
	assert.Equal(t, int64(200), pos.EnrolledAt)
}

func TestEngineClient_GetPosition_RootHasNilParentAndSponsor(t *testing.T) {
	mock := &mockTransport{
		response: json.RawMessage(`{
			"user_id":"00000000-0000-0000-0000-000000000001",
			"parent_user_id":null,
			"sponsor_user_id":null,
			"position":0,
			"depth":0,
			"child_count":1,
			"downline_counts":{"0":0},
			"enrolled_at":100
		}`),
	}
	client := NewEngineClientWithTransport(mock)

	pos, err := client.GetPosition(context.Background(), "Test", "00000000-0000-0000-0000-000000000001")
	require.NoError(t, err)

	assert.Nil(t, pos.ParentUserID)
	assert.Nil(t, pos.SponsorUserID)
	assert.Equal(t, 1, pos.ChildCount)
}

func TestEngineClient_IsDescendantOf_MockResponse(t *testing.T) {
	mock := &mockTransport{
		response: json.RawMessage(`{"is_descendant":true}`),
	}
	client := NewEngineClientWithTransport(mock)

	result, err := client.IsDescendantOf(context.Background(), "Test", "00000000-0000-0000-0000-000000000002", "00000000-0000-0000-0000-000000000001")
	require.NoError(t, err)
	assert.True(t, result)

	assert.Equal(t, "is_descendant_of", mock.lastOp)
	assert.JSONEq(t, `{"structure":"Test","user_id":"00000000-0000-0000-0000-000000000002","ancestor_id":"00000000-0000-0000-0000-000000000001"}`, string(mock.lastParams))
}

func TestEngineClient_IsDescendantOf_False(t *testing.T) {
	mock := &mockTransport{
		response: json.RawMessage(`{"is_descendant":false}`),
	}
	client := NewEngineClientWithTransport(mock)

	result, err := client.IsDescendantOf(context.Background(), "Test", "00000000-0000-0000-0000-000000000001", "00000000-0000-0000-0000-000000000002")
	require.NoError(t, err)
	assert.False(t, result)
}

// --- Sponsor query tests (mock) ---

func TestEngineClient_GetSponsor_ReturnsNode(t *testing.T) {
	mock := &mockTransport{
		response: json.RawMessage(`{"user_id":"00000000-0000-0000-0000-000000000001","depth":0,"enrolled_at":100}`),
	}
	client := NewEngineClientWithTransport(mock)

	node, err := client.GetSponsor(context.Background(), "Test", "00000000-0000-0000-0000-000000000002")
	require.NoError(t, err)
	require.NotNil(t, node)

	assert.Equal(t, "00000000-0000-0000-0000-000000000001", node.UserID)
	assert.Equal(t, "get_sponsor", mock.lastOp)
}

func TestEngineClient_GetSponsor_ReturnsNilForRoot(t *testing.T) {
	mock := &mockTransport{
		response: json.RawMessage(`null`),
	}
	client := NewEngineClientWithTransport(mock)

	node, err := client.GetSponsor(context.Background(), "Test", "00000000-0000-0000-0000-000000000001")
	require.NoError(t, err)
	assert.Nil(t, node)
}

func TestEngineClient_GetSponsorUpline_MockParams(t *testing.T) {
	mock := &mockTransport{
		response: json.RawMessage(`[{"user_id":"00000000-0000-0000-0000-000000000001","depth":0,"enrolled_at":100}]`),
	}
	client := NewEngineClientWithTransport(mock)

	nodes, err := client.GetSponsorUpline(context.Background(), "Test", "00000000-0000-0000-0000-000000000002", 0)
	require.NoError(t, err)
	require.Len(t, nodes, 1)

	assert.Equal(t, "get_sponsor_upline", mock.lastOp)
	assert.JSONEq(t, `{"structure":"Test","user_id":"00000000-0000-0000-0000-000000000002","depth":0}`, string(mock.lastParams))
}

func TestEngineClient_GetSponsored_MockParams(t *testing.T) {
	mock := &mockTransport{
		response: json.RawMessage(`[{"user_id":"00000000-0000-0000-0000-000000000002","depth":1,"enrolled_at":200}]`),
	}
	client := NewEngineClientWithTransport(mock)

	nodes, err := client.GetSponsored(context.Background(), "Test", "00000000-0000-0000-0000-000000000001")
	require.NoError(t, err)
	require.Len(t, nodes, 1)

	assert.Equal(t, "get_sponsored", mock.lastOp)
	assert.JSONEq(t, `{"structure":"Test","user_id":"00000000-0000-0000-0000-000000000001"}`, string(mock.lastParams))
}

// --- Error handling tests (mock) ---

func TestEngineClient_TransportErrorPropagation(t *testing.T) {
	transportErr := fmt.Errorf("transport down")
	mock := &mockTransport{err: transportErr}
	client := NewEngineClientWithTransport(mock)

	err := client.Ping(context.Background())
	assert.ErrorIs(t, err, transportErr)

	err = client.AddRoot(context.Background(), "Test", "user-1", 1000)
	assert.ErrorIs(t, err, transportErr)

	_, err = client.GetChildren(context.Background(), "Test", "user-1")
	assert.ErrorIs(t, err, transportErr)
}

func TestEngineClient_UnmarshalError(t *testing.T) {
	mock := &mockTransport{
		response: json.RawMessage(`{not json}`),
	}
	client := NewEngineClientWithTransport(mock)

	_, err := client.GetChildren(context.Background(), "Test", "user-1")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unmarshal")
}

func TestEngineClient_StopClosesTransport(t *testing.T) {
	mock := &mockTransport{response: json.RawMessage(`"pong"`)}
	client := NewEngineClientWithTransport(mock)

	err := client.Stop()
	require.NoError(t, err)
	assert.True(t, mock.closed)
}

// --- Integration tests (real binary) ---

// structureName is used by all integration tests that need a named tree.
const structureName = "Test"

func TestEngineClient_TreeOperations(t *testing.T) {
	client, err := NewEngineClient(context.Background(), findWorkerBinary(t))
	require.NoError(t, err)
	defer func() { _ = client.Stop() }()

	ctx := context.Background()
	rootID := "00000000-0000-0000-0000-000000000001"
	childID := "00000000-0000-0000-0000-000000000002"

	// Create the tree instance.
	require.NoError(t, client.CreateTree(ctx, structureName, "unilevel"))

	// Add root.
	err = client.AddRoot(ctx, structureName, rootID, 100)
	require.NoError(t, err)

	// Add child. Sponsor is root.
	err = client.AddNode(ctx, structureName, childID, rootID, rootID, 200)
	require.NoError(t, err)

	// Get children of root.
	children, err := client.GetChildren(ctx, structureName, rootID)
	require.NoError(t, err)
	require.Len(t, children, 1)
	assert.Equal(t, childID, children[0].UserID)
	assert.Equal(t, uint32(1), children[0].Depth)
	assert.Equal(t, int64(200), children[0].EnrolledAt)
}

func TestEngineClient_TreeQueries(t *testing.T) {
	client, err := NewEngineClient(context.Background(), findWorkerBinary(t))
	require.NoError(t, err)
	defer func() { _ = client.Stop() }()

	ctx := context.Background()
	rootID := "00000000-0000-0000-0000-000000000001"
	childID := "00000000-0000-0000-0000-000000000002"
	grandchildID := "00000000-0000-0000-0000-000000000003"

	// Create the tree instance and build a 3-node chain: root -> child -> grandchild.
	require.NoError(t, client.CreateTree(ctx, structureName, "unilevel"))
	require.NoError(t, client.AddRoot(ctx, structureName, rootID, 100))
	require.NoError(t, client.AddNode(ctx, structureName, childID, rootID, rootID, 200))
	require.NoError(t, client.AddNode(ctx, structureName, grandchildID, childID, childID, 300))

	t.Run("GetParent_returnsParent", func(t *testing.T) {
		parent, err := client.GetParent(ctx, structureName, childID)
		require.NoError(t, err)
		require.NotNil(t, parent)
		assert.Equal(t, rootID, parent.UserID)
		assert.Equal(t, uint32(0), parent.Depth)
	})

	t.Run("GetParent_rootReturnsNil", func(t *testing.T) {
		parent, err := client.GetParent(ctx, structureName, rootID)
		require.NoError(t, err)
		assert.Nil(t, parent)
	})

	t.Run("GetUpline_fullChain", func(t *testing.T) {
		upline, err := client.GetUpline(ctx, structureName, grandchildID, 0)
		require.NoError(t, err)
		require.Len(t, upline, 2)
		// Upline should be ordered from closest ancestor to farthest.
		assert.Equal(t, childID, upline[0].UserID)
		assert.Equal(t, rootID, upline[1].UserID)
	})

	t.Run("GetUpline_limitedDepth", func(t *testing.T) {
		upline, err := client.GetUpline(ctx, structureName, grandchildID, 1)
		require.NoError(t, err)
		require.Len(t, upline, 1)
		assert.Equal(t, childID, upline[0].UserID)
	})

	t.Run("GetDownline_fullChain", func(t *testing.T) {
		downline, err := client.GetDownline(ctx, structureName, rootID, 0)
		require.NoError(t, err)
		require.Len(t, downline, 2)
	})

	t.Run("GetDownline_limitedDepth", func(t *testing.T) {
		downline, err := client.GetDownline(ctx, structureName, rootID, 1)
		require.NoError(t, err)
		require.Len(t, downline, 1)
		assert.Equal(t, childID, downline[0].UserID)
	})

	t.Run("GetPosition_rootNode", func(t *testing.T) {
		pos, err := client.GetPosition(ctx, structureName, rootID)
		require.NoError(t, err)
		assert.Equal(t, rootID, pos.UserID)
		assert.Nil(t, pos.ParentUserID)
		assert.Nil(t, pos.SponsorUserID)
		assert.Equal(t, uint32(0), pos.Depth)
		assert.Equal(t, 1, pos.ChildCount)
		assert.Equal(t, int64(100), pos.EnrolledAt)
	})

	t.Run("GetPosition_childNode", func(t *testing.T) {
		pos, err := client.GetPosition(ctx, structureName, childID)
		require.NoError(t, err)
		assert.Equal(t, childID, pos.UserID)
		require.NotNil(t, pos.ParentUserID)
		assert.Equal(t, rootID, *pos.ParentUserID)
		require.NotNil(t, pos.SponsorUserID)
		assert.Equal(t, rootID, *pos.SponsorUserID)
		assert.Equal(t, 0, pos.Position)
		assert.Equal(t, uint32(1), pos.Depth)
		assert.Equal(t, 1, pos.ChildCount)
	})

	t.Run("IsDescendantOf_true", func(t *testing.T) {
		result, err := client.IsDescendantOf(ctx, structureName, grandchildID, rootID)
		require.NoError(t, err)
		assert.True(t, result)
	})

	t.Run("IsDescendantOf_false", func(t *testing.T) {
		result, err := client.IsDescendantOf(ctx, structureName, rootID, grandchildID)
		require.NoError(t, err)
		assert.False(t, result)
	})

	t.Run("GetSponsor_returnsSponsor", func(t *testing.T) {
		sponsor, err := client.GetSponsor(ctx, structureName, childID)
		require.NoError(t, err)
		require.NotNil(t, sponsor)
		assert.Equal(t, rootID, sponsor.UserID)
	})

	t.Run("GetSponsor_rootReturnsNil", func(t *testing.T) {
		sponsor, err := client.GetSponsor(ctx, structureName, rootID)
		require.NoError(t, err)
		assert.Nil(t, sponsor)
	})

	t.Run("GetSponsorUpline_fullChain", func(t *testing.T) {
		upline, err := client.GetSponsorUpline(ctx, structureName, grandchildID, 0)
		require.NoError(t, err)
		require.Len(t, upline, 2)
		assert.Equal(t, childID, upline[0].UserID)
		assert.Equal(t, rootID, upline[1].UserID)
	})

	t.Run("GetSponsored_returnsSponsored", func(t *testing.T) {
		sponsored, err := client.GetSponsored(ctx, structureName, rootID)
		require.NoError(t, err)
		require.Len(t, sponsored, 1)
		assert.Equal(t, childID, sponsored[0].UserID)
	})

	t.Run("RemoveNode_andVerify", func(t *testing.T) {
		err := client.RemoveNode(ctx, structureName, grandchildID)
		require.NoError(t, err)

		children, err := client.GetChildren(ctx, structureName, childID)
		require.NoError(t, err)
		assert.Empty(t, children)
	})
}

// --- Binary tree integration tests (real binary) ---

func TestEngineClient_BinaryTreeOperations(t *testing.T) {
	client, err := NewEngineClient(context.Background(), findWorkerBinary(t))
	require.NoError(t, err)
	defer func() { _ = client.Stop() }()

	ctx := context.Background()
	rootID := "00000000-0000-0000-0000-000000000010"
	leftID := "00000000-0000-0000-0000-000000000011"
	rightID := "00000000-0000-0000-0000-000000000012"

	// Create a binary tree instance.
	require.NoError(t, client.CreateTree(ctx, "binary_test", "binary"))

	// Add root.
	require.NoError(t, client.AddRoot(ctx, "binary_test", rootID, 1000))

	// Add left child at position 0.
	err = client.AddNode(ctx, "binary_test", leftID, rootID, rootID, 2000, WithPosition(0))
	require.NoError(t, err)

	// Add right child at position 1.
	err = client.AddNode(ctx, "binary_test", rightID, rootID, rootID, 3000, WithPosition(1))
	require.NoError(t, err)

	t.Run("GetChildren_returnsBothInOrder", func(t *testing.T) {
		children, err := client.GetChildren(ctx, "binary_test", rootID)
		require.NoError(t, err)
		require.Len(t, children, 2)
		assert.Equal(t, leftID, children[0].UserID, "first child should be left (position 0)")
		assert.Equal(t, rightID, children[1].UserID, "second child should be right (position 1)")
	})

	t.Run("GetPosition_leftChildPosition0", func(t *testing.T) {
		pos, err := client.GetPosition(ctx, "binary_test", leftID)
		require.NoError(t, err)
		assert.Equal(t, 0, pos.Position)
	})

	t.Run("GetPosition_rightChildPosition1", func(t *testing.T) {
		pos, err := client.GetPosition(ctx, "binary_test", rightID)
		require.NoError(t, err)
		assert.Equal(t, 1, pos.Position)
	})

	t.Run("AddNode_occupiedPositionReturnsError", func(t *testing.T) {
		dupID := "00000000-0000-0000-0000-000000000013"
		err := client.AddNode(ctx, "binary_test", dupID, rootID, rootID, 4000, WithPosition(0))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "POSITION_OCCUPIED")
	})
}

func TestEngineClient_BinaryGetPosition(t *testing.T) {
	client, err := NewEngineClient(context.Background(), findWorkerBinary(t))
	require.NoError(t, err)
	defer func() { _ = client.Stop() }()

	ctx := context.Background()
	rootID := "00000000-0000-0000-0000-000000000020"
	childID := "00000000-0000-0000-0000-000000000021"

	// Create a binary tree and add root + one child.
	require.NoError(t, client.CreateTree(ctx, "binary_pos_test", "binary"))
	require.NoError(t, client.AddRoot(ctx, "binary_pos_test", rootID, 1000))
	require.NoError(t, client.AddNode(ctx, "binary_pos_test", childID, rootID, rootID, 2000, WithPosition(1)))

	// Verify that SponsorUserID comes back correctly in the position response.
	pos, err := client.GetPosition(ctx, "binary_pos_test", childID)
	require.NoError(t, err)

	assert.Equal(t, childID, pos.UserID)
	require.NotNil(t, pos.ParentUserID)
	assert.Equal(t, rootID, *pos.ParentUserID)
	require.NotNil(t, pos.SponsorUserID)
	assert.Equal(t, rootID, *pos.SponsorUserID)
	assert.Equal(t, 1, pos.Position)
	assert.Equal(t, uint32(1), pos.Depth)
	assert.Equal(t, 0, pos.ChildCount)
	assert.Equal(t, int64(2000), pos.EnrolledAt)
}

// --- Commission calculation tests (mock) ---

func TestEngineClient_CalculateUnilevel_MockParams(t *testing.T) {
	mock := &mockTransport{
		response: json.RawMessage(`[{"earner_id":"00000000-0000-0000-0000-000000000001","source_id":"00000000-0000-0000-0000-000000000002","level":1,"rate":0.05,"cv_amount":100.0,"dollar_amount":2.0}]`),
	}
	client := NewEngineClientWithTransport(mock)

	req := CalculateUnilevelRequest{
		StructureName: "Test",
		Snapshots: map[string]DistributorSnapshotDTO{
			"00000000-0000-0000-0000-000000000001": {
				Rank:             "member",
				PersonalVolume:   100.0,
				Status:           "active",
				HasOrderInPeriod: true,
			},
		},
		Volume: []VolumeSourceDTO{
			{SourceID: "00000000-0000-0000-0000-000000000002", CVAmount: 100.0},
		},
	}

	earnings, err := client.CalculateUnilevel(context.Background(), req)
	require.NoError(t, err)
	require.Len(t, earnings, 1)

	assert.Equal(t, "calculate_unilevel", mock.lastOp)
	assert.Equal(t, "00000000-0000-0000-0000-000000000001", earnings[0].EarnerID)
	assert.Equal(t, "00000000-0000-0000-0000-000000000002", earnings[0].SourceID)
	assert.Equal(t, 1, earnings[0].Level)
	assert.InDelta(t, 0.05, earnings[0].Rate, 1e-9)
	assert.InDelta(t, 100.0, earnings[0].CVAmount, 1e-9)
	assert.InDelta(t, 2.0, earnings[0].DollarAmount, 1e-9)
}

func TestEngineClient_CalculateUnilevel_EmptyEarnings(t *testing.T) {
	mock := &mockTransport{
		response: json.RawMessage(`[]`),
	}
	client := NewEngineClientWithTransport(mock)

	req := CalculateUnilevelRequest{
		StructureName: "Test",
		Snapshots:     map[string]DistributorSnapshotDTO{},
		Volume:        []VolumeSourceDTO{},
	}

	earnings, err := client.CalculateUnilevel(context.Background(), req)
	require.NoError(t, err)
	assert.Empty(t, earnings)
}

// --- Generation commission calculation tests (mock) ---

func TestEngineClient_CalculateGeneration_MockParams(t *testing.T) {
	mock := &mockTransport{
		response: json.RawMessage(`[{"earner_id":"00000000-0000-0000-0000-000000000001","source_id":"00000000-0000-0000-0000-000000000002","level":1,"rate":0.10,"cv_amount":100.0,"dollar_amount":10.0}]`),
	}
	client := NewEngineClientWithTransport(mock)

	req := CalculateGenerationRequest{
		StructureName: "GenTree",
		Snapshots: map[string]DistributorSnapshotDTO{
			"00000000-0000-0000-0000-000000000001": {
				Rank:             "director",
				PersonalVolume:   150.0,
				Status:           "active",
				HasOrderInPeriod: true,
			},
		},
		Volume: []VolumeSourceDTO{
			{SourceID: "00000000-0000-0000-0000-000000000002", CVAmount: 100.0},
		},
	}

	earnings, err := client.CalculateGeneration(context.Background(), req)
	require.NoError(t, err)
	require.Len(t, earnings, 1)

	assert.Equal(t, "calculate_generation", mock.lastOp)
	assert.Equal(t, "00000000-0000-0000-0000-000000000001", earnings[0].EarnerID)
	assert.Equal(t, "00000000-0000-0000-0000-000000000002", earnings[0].SourceID)
	assert.Equal(t, 1, earnings[0].Level)
	assert.InDelta(t, 0.10, earnings[0].Rate, 1e-9)
	assert.InDelta(t, 100.0, earnings[0].CVAmount, 1e-9)
	assert.InDelta(t, 10.0, earnings[0].DollarAmount, 1e-9)
}

func TestEngineClient_CalculateGeneration_EmptyEarnings(t *testing.T) {
	mock := &mockTransport{
		response: json.RawMessage(`[]`),
	}
	client := NewEngineClientWithTransport(mock)

	req := CalculateGenerationRequest{
		StructureName: "GenTree",
		Snapshots:     map[string]DistributorSnapshotDTO{},
		Volume:        []VolumeSourceDTO{},
	}

	earnings, err := client.CalculateGeneration(context.Background(), req)
	require.NoError(t, err)
	assert.Empty(t, earnings)
}

// --- Matrix commission calculation tests (mock) ---

func TestEngineClient_CalculateMatrix_MockParams(t *testing.T) {
	mock := &mockTransport{
		response: json.RawMessage(`[{"earner_id":"00000000-0000-0000-0000-000000000001","source_id":"00000000-0000-0000-0000-000000000002","level":1,"rate":0.05,"cv_amount":100.0,"dollar_amount":2.0}]`),
	}
	client := NewEngineClientWithTransport(mock)

	req := CalculateMatrixRequest{
		StructureName: "Test",
		Snapshots: map[string]DistributorSnapshotDTO{
			"00000000-0000-0000-0000-000000000001": {
				Rank:             "member",
				PersonalVolume:   100.0,
				Status:           "active",
				HasOrderInPeriod: true,
			},
		},
		Volume: []VolumeSourceDTO{
			{SourceID: "00000000-0000-0000-0000-000000000002", CVAmount: 100.0},
		},
	}

	earnings, err := client.CalculateMatrix(context.Background(), req)
	require.NoError(t, err)
	require.Len(t, earnings, 1)

	assert.Equal(t, "calculate_matrix", mock.lastOp)
	assert.JSONEq(t, `{"structure":"Test","snapshots":{"00000000-0000-0000-0000-000000000001":{"rank":"member","personal_volume":100,"status":"active","has_order_in_period":true}},"volume":[{"source_id":"00000000-0000-0000-0000-000000000002","cv_amount":100}]}`, string(mock.lastParams))
	assert.Equal(t, "00000000-0000-0000-0000-000000000001", earnings[0].EarnerID)
	assert.InDelta(t, 2.0, earnings[0].DollarAmount, 1e-9)
}

func TestEngineClient_CalculateMatrix_EmptyEarnings(t *testing.T) {
	mock := &mockTransport{response: json.RawMessage(`[]`)}
	client := NewEngineClientWithTransport(mock)

	req := CalculateMatrixRequest{
		StructureName: "Test",
		Snapshots:     map[string]DistributorSnapshotDTO{},
		Volume:        []VolumeSourceDTO{},
	}

	earnings, err := client.CalculateMatrix(context.Background(), req)
	require.NoError(t, err)
	assert.Empty(t, earnings)
}

// --- Stairstep commission calculation tests (mock) ---

func TestEngineClient_CalculateStairstep_MockParams(t *testing.T) {
	mock := &mockTransport{
		response: json.RawMessage(`[{"earner_id":"00000000-0000-0000-0000-000000000001","source_id":"00000000-0000-0000-0000-000000000003","level":2,"rate":0.05,"cv_amount":100.0,"dollar_amount":2.0}]`),
	}
	client := NewEngineClientWithTransport(mock)

	req := CalculateStairstepRequest{
		StructureName: "Test",
		Snapshots: map[string]DistributorSnapshotDTO{
			"00000000-0000-0000-0000-000000000001": {
				Rank:             "member",
				PersonalVolume:   100.0,
				Status:           "active",
				HasOrderInPeriod: true,
			},
		},
		Volume: []VolumeSourceDTO{
			{SourceID: "00000000-0000-0000-0000-000000000003", CVAmount: 100.0},
		},
	}

	earnings, err := client.CalculateStairstep(context.Background(), req)
	require.NoError(t, err)
	require.Len(t, earnings, 1)

	assert.Equal(t, "calculate_stairstep", mock.lastOp)
	assert.JSONEq(t, `{"structure":"Test","snapshots":{"00000000-0000-0000-0000-000000000001":{"rank":"member","personal_volume":100,"status":"active","has_order_in_period":true}},"volume":[{"source_id":"00000000-0000-0000-0000-000000000003","cv_amount":100}]}`, string(mock.lastParams))
	assert.Equal(t, "00000000-0000-0000-0000-000000000001", earnings[0].EarnerID)
	assert.InDelta(t, 2.0, earnings[0].DollarAmount, 1e-9)
}

func TestEngineClient_CalculateStairstep_EmptyEarnings(t *testing.T) {
	mock := &mockTransport{response: json.RawMessage(`[]`)}
	client := NewEngineClientWithTransport(mock)

	req := CalculateStairstepRequest{
		StructureName: "Test",
		Snapshots:     map[string]DistributorSnapshotDTO{},
		Volume:        []VolumeSourceDTO{},
	}

	earnings, err := client.CalculateStairstep(context.Background(), req)
	require.NoError(t, err)
	assert.Empty(t, earnings)
}

// --- Commission calculation integration test (real binary) ---

// testPlanJSON is a minimal compensation plan that matches the Rust
// TEST_PLAN_JSON fixture. One rank ("member"), one unilevel structure
// ("Test"), rate 0.05 at levels 1-3, broad_commission_percent 0.40,
// volume_to_dollar_multiplier 1.0.
const testPlanJSON = `{
    "name": "Integration Test Plan",
    "version": 1,
    "structures": [
        {
            "type": "unilevel",
            "config": {
                "name": "Test",
                "level_commission": {
                    "broad_commission_percent": 0.40,
                    "volume_to_dollar_multiplier": null,
                    "commissionable_depth": 3,
                    "rate_table": {
                        "member": { "1": 0.05, "2": 0.05, "3": 0.05 }
                    }
                },
                "compression": null
            }
        }
    ],
    "period": {
        "length": "month",
        "start_date": "2026-03-01",
        "payout_lag_days": 14
    },
    "volume": {
        "inhibit_signup_volume": false,
        "base_currency": "USD",
        "volume_to_dollar_multiplier": 1.0,
        "deduct_qualifying_volume": false
    },
    "ranks": [
        {
            "name": "member",
            "ordinal": 1,
            "qualification": {
                "structures": [],
                "required_products": []
            },
            "qualified_structures": ["Test"],
            "demotion_policy": "promotion_only"
        }
    ],
    "rank_tracking": { "track_achieved_rank": false },
    "rank_features": { "constraints_enabled": false, "overrides_enabled": false },
    "commission_eligibility": {
        "min_personal_volume": 0.0,
        "require_order_in_period": false,
        "eligible_statuses": [],
        "active_leg_tiers": []
    },
    "bonuses": {
        "matching": null,
        "sponsor": null,
        "fast_start": null,
        "rank_advancement": null,
        "leadership_development": null,
        "infinity": null,
        "lifestyle": null,
        "pool": null,
        "matrix_completion": null,
        "position": null,
        "board_cycling": null
    },
    "payout": {
        "base_currency": "USD",
        "minimum_amount": 50.0,
        "split_payouts_enabled": true,
        "methods": [
            { "type": "bank_transfer", "fee": 2.50 }
        ]
    },
    "caps": {
        "per_distributor_per_period": null,
        "company_payout_cap_percent": 0.42,
        "cap_enforcement": "pro_rata",
        "clawback_on_refund": false
    },
    "placement": {
        "donated_placement": null,
        "holding_tank": null,
        "binary_placement": null
    }
}`

// streamlinePlanJSON carries a streamline structure named StreamTest and a
// companion unilevel. calculate_streamline resolves its config from the loaded
// plan (HEU-583), so this plan must be loaded before the calculate call.
//
// The unilevel is required, not decoration. validateStreamlineCompanion
// (internal/config/rules.go:839) requires every streamline structure to have a
// companion unilevel. Delete StreamTestUnilevel and this constant goes back to
// describing a plan those rules reject.
//
// Nothing here would catch that. Rust's CompensationPlan::validate does not
// enforce the companion rule, so the worker loads either shape and every test
// still passes. Pipeline does enforce it, but this constant can never reach
// Pipeline: that path takes authoring-shape YAML, not the Rust-shape JSON
// below. The pairing is held by hand, which is why it is written down here.
//
// Mirrors STREAMLINE_TEST_PLAN_JSON in worker_integration.rs, which carries
// the same pairing for the same reason. Both are copies HEU-604 will
// consolidate.
const streamlinePlanJSON = `{
    "name": "Streamline Test Plan",
    "version": 1,
    "structures": [
        {
            "type": "streamline",
            "config": {
                "name": "StreamTest",
                "streamline_commission": {
                    "volume_to_dollar_multiplier": 1.0,
                    "commissionable_depth": 5,
                    "dynamic_compression": [
                        { "level": 1, "min_rank": "member", "percent": 0.10 }
                    ],
                    "streams": null
                }
            }
        },
        {
            "type": "unilevel",
            "config": {
                "name": "StreamTestUnilevel",
                "level_commission": {
                    "broad_commission_percent": 0.40,
                    "volume_to_dollar_multiplier": null,
                    "commissionable_depth": 3,
                    "rate_table": { "member": { "1": 0.05, "2": 0.05, "3": 0.05 } }
                },
                "compression": null
            }
        }
    ],
    "period": { "length": "month", "start_date": "2026-03-01", "payout_lag_days": 14 },
    "volume": { "inhibit_signup_volume": false, "base_currency": "USD", "volume_to_dollar_multiplier": 1.0, "deduct_qualifying_volume": false },
    "ranks": [
        { "name": "member", "ordinal": 1, "qualification": { "structures": [], "required_products": [] }, "qualified_structures": ["StreamTest", "StreamTestUnilevel"], "demotion_policy": "promotion_only" }
    ],
    "rank_tracking": { "track_achieved_rank": false },
    "rank_features": { "constraints_enabled": false, "overrides_enabled": false },
    "commission_eligibility": { "min_personal_volume": 0.0, "require_order_in_period": false, "eligible_statuses": [], "active_leg_tiers": [] },
    "bonuses": { "matching": null, "sponsor": null, "fast_start": null, "rank_advancement": null, "leadership_development": null, "infinity": null, "lifestyle": null, "pool": null, "matrix_completion": null, "position": null, "board_cycling": null },
    "payout": { "base_currency": "USD", "minimum_amount": 50.0, "split_payouts_enabled": true, "methods": [ { "type": "bank_transfer", "fee": 2.50 } ] },
    "caps": { "per_distributor_per_period": null, "company_payout_cap_percent": 0.42, "cap_enforcement": "pro_rata", "clawback_on_refund": false },
    "placement": { "donated_placement": null, "holding_tank": null, "binary_placement": null }
}`

func TestEngineClient_CalculateUnilevel(t *testing.T) {
	client, err := NewEngineClient(context.Background(), findWorkerBinary(t))
	require.NoError(t, err)
	defer func() { _ = client.Stop() }()

	ctx := context.Background()
	rootID := "00000000-0000-0000-0000-000000000001"
	midID := "00000000-0000-0000-0000-000000000002"
	leafID := "00000000-0000-0000-0000-000000000003"

	// 1. Load the compensation plan and create the tree instance.
	err = client.LoadPlan(ctx, json.RawMessage(testPlanJSON))
	require.NoError(t, err)
	require.NoError(t, client.CreateTree(ctx, structureName, "unilevel"))

	// 2. Build tree: root -> mid -> leaf.
	require.NoError(t, client.AddRoot(ctx, structureName, rootID, 100))
	require.NoError(t, client.AddNode(ctx, structureName, midID, rootID, rootID, 200))
	require.NoError(t, client.AddNode(ctx, structureName, leafID, midID, midID, 300))

	// 3. Calculate commissions.
	//    Volume source: leaf generates 100 CV.
	//    Expected upline walk: mid at level 1, root at level 2.
	//    Dollar amount per earning: 100 * 0.40 * 1.0 * 0.05 = 2.0
	snap := DistributorSnapshotDTO{
		Rank:             "member",
		PersonalVolume:   100.0,
		Status:           "active",
		HasOrderInPeriod: true,
	}
	req := CalculateUnilevelRequest{
		StructureName: "Test",
		Snapshots: map[string]DistributorSnapshotDTO{
			rootID: snap,
			midID:  snap,
			leafID: snap,
		},
		Volume: []VolumeSourceDTO{
			{SourceID: leafID, CVAmount: 100.0},
		},
	}

	earnings, err := client.CalculateUnilevel(ctx, req)
	require.NoError(t, err)
	require.Len(t, earnings, 2, "expected 2 earnings from 3-node chain")

	// Find mid's earning at level 1.
	var midEarning, rootEarning *CommissionEarningDTO
	for i := range earnings {
		switch earnings[i].EarnerID {
		case midID:
			midEarning = &earnings[i]
		case rootID:
			rootEarning = &earnings[i]
		}
	}

	require.NotNil(t, midEarning, "mid should have an earning")
	assert.Equal(t, leafID, midEarning.SourceID)
	assert.Equal(t, 1, midEarning.Level)
	assert.InDelta(t, 0.05, midEarning.Rate, 1e-9)
	assert.InDelta(t, 100.0, midEarning.CVAmount, 1e-9)
	assert.True(t, math.Abs(midEarning.DollarAmount-2.0) < 1e-9,
		"mid dollar_amount should be 2.0, got %f", midEarning.DollarAmount)

	require.NotNil(t, rootEarning, "root should have an earning")
	assert.Equal(t, leafID, rootEarning.SourceID)
	assert.Equal(t, 2, rootEarning.Level)
	assert.InDelta(t, 0.05, rootEarning.Rate, 1e-9)
	assert.InDelta(t, 100.0, rootEarning.CVAmount, 1e-9)
	assert.True(t, math.Abs(rootEarning.DollarAmount-2.0) < 1e-9,
		"root dollar_amount should be 2.0, got %f", rootEarning.DollarAmount)
}

func TestEngineClient_CalculateUnilevel_EmptyVolume(t *testing.T) {
	client, err := NewEngineClient(context.Background(), findWorkerBinary(t))
	require.NoError(t, err)
	defer func() { _ = client.Stop() }()

	ctx := context.Background()
	rootID := "00000000-0000-0000-0000-000000000001"

	// Load plan, create tree, and build a minimal tree.
	require.NoError(t, client.LoadPlan(ctx, json.RawMessage(testPlanJSON)))
	require.NoError(t, client.CreateTree(ctx, structureName, "unilevel"))
	require.NoError(t, client.AddRoot(ctx, structureName, rootID, 100))

	req := CalculateUnilevelRequest{
		StructureName: "Test",
		Snapshots:     map[string]DistributorSnapshotDTO{},
		Volume:        []VolumeSourceDTO{},
	}

	earnings, err := client.CalculateUnilevel(ctx, req)
	require.NoError(t, err)
	assert.Empty(t, earnings)
}

// TestEngineClient_CalculateStreamline exercises EngineClient.CalculateStreamline
// against a real worker. This op had no Go client test, so the typed
// CommissionEarningDTO marshal/unmarshal on the streamline path was unguarded.
// Mirrors TestEngineClient_CalculateUnilevel. HEU-514.
func TestEngineClient_CalculateStreamline(t *testing.T) {
	client, err := NewEngineClient(context.Background(), findWorkerBinary(t))
	require.NoError(t, err)
	defer func() { _ = client.Stop() }()

	ctx := context.Background()
	structure := "StreamTest"
	member1 := "00000000-0000-0000-0000-000000000001"
	member2 := "00000000-0000-0000-0000-000000000002"

	// The streamline structure config is resolved from the loaded plan,
	// not from the request (HEU-583).
	err = client.LoadPlan(ctx, json.RawMessage(streamlinePlanJSON))
	require.NoError(t, err)

	// 1. Create the streamline and build a 2-member sponsor chain:
	//    member1 is the stream root, member2 sits one level below it.
	err = client.CreateStreamline(ctx, structure, "sponsor_stream", false, true, 1000)
	require.NoError(t, err)

	_, err = client.StreamlineAddMember(ctx, structure, StreamlineAddMemberRequest{
		UserID: member1, SponsorID: "00000000-0000-0000-0000-000000000009", Timestamp: 1000,
	})
	require.NoError(t, err)

	_, err = client.StreamlineAddMember(ctx, structure, StreamlineAddMemberRequest{
		UserID: member2, SponsorID: member1, Timestamp: 2000,
	})
	require.NoError(t, err)

	// 2. Calculate streamline commissions. The plan and structure config come
	//    from the plan loaded above, not the request (HEU-583).
	//    member2 generates 100 CV, which walks up one level to member1.
	//    Dollar amount: 100 * 1.0 (broad) * 1.0 (multiplier) * 0.10 (level 1) = 10.0
	snap := DistributorSnapshotDTO{
		Rank:             "member",
		PersonalVolume:   100.0,
		Status:           "active",
		HasOrderInPeriod: true,
	}
	req := CalculateStreamlineRequest{
		StructureName: structure,
		Snapshots: map[string]DistributorSnapshotDTO{
			member1: snap,
			member2: snap,
		},
		Volume: []VolumeSourceDTO{
			{SourceID: member2, CVAmount: 100.0},
		},
	}

	earnings, err := client.CalculateStreamline(ctx, req)
	require.NoError(t, err)
	require.Len(t, earnings, 1, "expected 1 earning: member1 at level 1")

	// Assert the full CommissionEarningDTO unmarshal, not just the dollar amount.
	earning := earnings[0]
	assert.Equal(t, member1, earning.EarnerID)
	assert.Equal(t, member2, earning.SourceID)
	assert.Equal(t, 1, earning.Level)
	assert.InDelta(t, 0.10, earning.Rate, 1e-9)
	assert.InDelta(t, 100.0, earning.CVAmount, 1e-9)
	assert.InDelta(t, 10.0, earning.DollarAmount, 1e-9)
}

// --- Streamline commission calculation test (mock) ---

// TestEngineClient_CalculateStreamline_MockParams pins the serialized param set.
// Streamline was the only calculate op without a _MockParams sibling, and this
// change is precisely a wire-shape change, so the gap is worth closing here.
func TestEngineClient_CalculateStreamline_MockParams(t *testing.T) {
	mock := &mockTransport{
		response: json.RawMessage(`[{"earner_id":"00000000-0000-0000-0000-000000000001","source_id":"00000000-0000-0000-0000-000000000002","level":1,"rate":0.10,"cv_amount":100.0,"dollar_amount":10.0}]`),
	}
	client := NewEngineClientWithTransport(mock)

	req := CalculateStreamlineRequest{
		StructureName: "StreamTest",
		Snapshots: map[string]DistributorSnapshotDTO{
			"00000000-0000-0000-0000-000000000001": {
				Rank:             "member",
				PersonalVolume:   100.0,
				Status:           "active",
				HasOrderInPeriod: true,
			},
		},
		Volume: []VolumeSourceDTO{
			{SourceID: "00000000-0000-0000-0000-000000000002", CVAmount: 100.0},
		},
	}

	earnings, err := client.CalculateStreamline(context.Background(), req)
	require.NoError(t, err)
	require.Len(t, earnings, 1)

	assert.Equal(t, "calculate_streamline", mock.lastOp)
	// The plan and structure config no longer cross the wire (HEU-583).
	// Asserting the exact param set is what makes their removal stick.
	assert.JSONEq(t, `{
		"structure": "StreamTest",
		"snapshots": {"00000000-0000-0000-0000-000000000001": {"rank":"member","personal_volume":100.0,"status":"active","has_order_in_period":true}},
		"volume": [{"source_id":"00000000-0000-0000-0000-000000000002","cv_amount":100.0}]
	}`, string(mock.lastParams))

	// These duplicate what JSONEq above already catches — JSONEq decodes into
	// interface{}, so it sees an extra key. They earn their place as an
	// independent tripwire: someone re-adding a field is likely to "fix" the
	// failure by regenerating the golden JSON, which silences JSONEq but not
	// these. Deleting the guard then has to be deliberate.
	//
	// Neither catches a field re-added with `,omitempty` and left unpopulated.
	// That shape is dead weight rather than a bypass: the worker ignores unknown
	// params, and calculate_streamline_ignores_request_scoped_config is what pins
	// that it keeps doing so. That Rust test builds its own request string, so it
	// watches the worker, not this client.
	assert.NotContains(t, string(mock.lastParams), `"plan"`)
	assert.NotContains(t, string(mock.lastParams), `"structure_config"`)
}

// --- Binary pairing commission calculation tests (mock) ---

func TestEngineClient_CalculateBinaryPairing_MockParams(t *testing.T) {
	mock := &mockTransport{
		response: json.RawMessage(`{
			"earnings":[{"earner_id":"00000000-0000-0000-0000-000000000001","left_volume":500.0,"right_volume":500.0,"matched_volume":500.0,"ratio":1.0,"percent":0.10,"dollar_amount":50.0,"capped":false}],
			"carry_forward":{"00000000-0000-0000-0000-000000000001":{"left":0.0,"right":0.0}}
		}`),
	}
	client := NewEngineClientWithTransport(mock)

	req := CalculateBinaryPairingRequest{
		StructureName: "BinaryCalc",
		Snapshots: map[string]DistributorSnapshotDTO{
			"00000000-0000-0000-0000-000000000001": {
				Rank:             "associate",
				PersonalVolume:   150.0,
				Status:           "active",
				HasOrderInPeriod: true,
			},
		},
		Volume: []VolumeSourceDTO{
			{SourceID: "00000000-0000-0000-0000-000000000002", CVAmount: 500.0},
		},
		CarryForward: map[string]LegVolumesDTO{
			"00000000-0000-0000-0000-000000000001": {Left: 100.0, Right: 200.0},
		},
	}

	result, err := client.CalculateBinaryPairing(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.Equal(t, "calculate_binary_pairing", mock.lastOp)
	require.Len(t, result.Earnings, 1)
	assert.Equal(t, "00000000-0000-0000-0000-000000000001", result.Earnings[0].EarnerID)
	assert.InDelta(t, 500.0, result.Earnings[0].LeftVolume, 1e-9)
	assert.InDelta(t, 500.0, result.Earnings[0].RightVolume, 1e-9)
	assert.InDelta(t, 500.0, result.Earnings[0].MatchedVolume, 1e-9)
	assert.InDelta(t, 1.0, result.Earnings[0].Ratio, 1e-9)
	assert.InDelta(t, 0.10, result.Earnings[0].Percent, 1e-9)
	assert.InDelta(t, 50.0, result.Earnings[0].DollarAmount, 1e-9)
	assert.False(t, result.Earnings[0].Capped)

	require.Contains(t, result.CarryForward, "00000000-0000-0000-0000-000000000001")
	cf := result.CarryForward["00000000-0000-0000-0000-000000000001"]
	assert.InDelta(t, 0.0, cf.Left, 1e-9)
	assert.InDelta(t, 0.0, cf.Right, 1e-9)

	// Verify carry_forward was sent in params.
	assert.Contains(t, string(mock.lastParams), "carry_forward")
}

func TestEngineClient_CalculateBinaryPairing_EmptyResult(t *testing.T) {
	mock := &mockTransport{
		response: json.RawMessage(`{"earnings":[],"carry_forward":{}}`),
	}
	client := NewEngineClientWithTransport(mock)

	req := CalculateBinaryPairingRequest{
		StructureName: "BinaryCalc",
		Snapshots:     map[string]DistributorSnapshotDTO{},
		Volume:        []VolumeSourceDTO{},
	}

	result, err := client.CalculateBinaryPairing(context.Background(), req)
	require.NoError(t, err)
	assert.Empty(t, result.Earnings)
	assert.Empty(t, result.CarryForward)
}

func TestEngineClient_EvaluateRanks_MockParams(t *testing.T) {
	mock := &mockTransport{
		response: json.RawMessage(`{"ranks":{"00000000-0000-0000-0000-000000000001":{"kind":"unranked"}}}`),
	}
	client := NewEngineClientWithTransport(mock)

	req := EvaluateRanksRequest{
		Distributors: map[string]DistributorPrimitivesDTO{
			"00000000-0000-0000-0000-000000000001": {
				PersonalVolume: 0.0,
				Status:         "active",
				ActiveProducts: []string{},
			},
		},
		VolumeSources: []VolumeSourceDTO{},
	}

	result, err := client.EvaluateRanks(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "evaluate_ranks", mock.lastOp)

	got, ok := result.Ranks["00000000-0000-0000-0000-000000000001"]
	require.True(t, ok)
	assert.Equal(t, "unranked", got.Kind)
}

func TestEngineClient_EvaluateRanks_VariadicSignature_AcceptsWithPersistence(t *testing.T) {
	mock := &mockTransport{
		response: json.RawMessage(`{"ranks":{}}`),
	}
	client := NewEngineClientWithTransport(mock)

	req := EvaluateRanksRequest{
		Distributors:  map[string]DistributorPrimitivesDTO{},
		VolumeSources: []VolumeSourceDTO{},
	}

	store := NewMemoryQualificationHistoryStore()
	// The compile-only assertion: WithPersistence must be a valid option type.
	_, err := client.EvaluateRanks(context.Background(), req, WithPersistence("2026-05", store))
	require.NoError(t, err)
}

// --- Rank evaluation integration test (real binary) ---

// rankIntegrationPlanJSON is a minimal compensation plan tailored for the
// EvaluateRanks integration test. The "member" rank carries a trivial
// structure qualification (personal_volume >= 0 on "Test"), so every
// distributor present in the tree with primitives qualifies. The worker's
// rank handler only registers a tree in the navigator map when at least
// one rank's qualification references that structure, so an entirely
// empty qualification would yield an empty ranks map.
const rankIntegrationPlanJSON = `{
    "name": "Rank Integration Test Plan",
    "version": 1,
    "structures": [
        {
            "type": "unilevel",
            "config": {
                "name": "Test",
                "level_commission": {
                    "broad_commission_percent": 0.40,
                    "volume_to_dollar_multiplier": null,
                    "commissionable_depth": 3,
                    "rate_table": {
                        "member": { "1": 0.05, "2": 0.05, "3": 0.05 }
                    }
                },
                "compression": null
            }
        }
    ],
    "period": {
        "length": "month",
        "start_date": "2026-03-01",
        "payout_lag_days": 14
    },
    "volume": {
        "inhibit_signup_volume": false,
        "base_currency": "USD",
        "volume_to_dollar_multiplier": 1.0,
        "deduct_qualifying_volume": false
    },
    "ranks": [
        {
            "name": "member",
            "ordinal": 1,
            "qualification": {
                "structures": [
                    {
                        "structure": "Test",
                        "personal_volume": 0.0,
                        "group_volume": 0.0,
                        "max_group_volume_per_leg": 1e12,
                        "min_retail_volume": 0.0,
                        "distributor_count": null
                    }
                ],
                "required_products": []
            },
            "qualified_structures": ["Test"],
            "demotion_policy": "promotion_only"
        }
    ],
    "rank_tracking": { "track_achieved_rank": false },
    "rank_features": { "constraints_enabled": false, "overrides_enabled": false },
    "commission_eligibility": {
        "min_personal_volume": 0.0,
        "require_order_in_period": false,
        "eligible_statuses": [],
        "active_leg_tiers": []
    },
    "bonuses": {
        "matching": null,
        "sponsor": null,
        "fast_start": null,
        "rank_advancement": null,
        "leadership_development": null,
        "infinity": null,
        "lifestyle": null,
        "pool": null,
        "matrix_completion": null,
        "position": null,
        "board_cycling": null
    },
    "payout": {
        "base_currency": "USD",
        "minimum_amount": 50.0,
        "split_payouts_enabled": true,
        "methods": [
            { "type": "bank_transfer", "fee": 2.50 }
        ]
    },
    "caps": {
        "per_distributor_per_period": null,
        "company_payout_cap_percent": 0.42,
        "cap_enforcement": "pro_rata",
        "clawback_on_refund": false
    },
    "placement": {
        "donated_placement": null,
        "holding_tank": null,
        "binary_placement": null
    }
}`

func TestEngineClient_EvaluateRanks(t *testing.T) {
	client, err := NewEngineClient(context.Background(), findWorkerBinary(t))
	require.NoError(t, err)
	defer func() { _ = client.Stop() }()

	ctx := context.Background()
	rootID := "00000000-0000-0000-0000-000000000001"
	childID := "00000000-0000-0000-0000-000000000002"

	require.NoError(t, client.LoadPlan(ctx, json.RawMessage(rankIntegrationPlanJSON)))
	require.NoError(t, client.CreateTree(ctx, structureName, "unilevel"))
	require.NoError(t, client.AddRoot(ctx, structureName, rootID, 100))
	require.NoError(t, client.AddNode(ctx, structureName, childID, rootID, rootID, 200))

	req := EvaluateRanksRequest{
		Distributors: map[string]DistributorPrimitivesDTO{
			rootID: {
				PersonalVolume:   100.0,
				RetailVolume:     0.0,
				Status:           "active",
				HasOrderInPeriod: true,
				ActiveProducts:   []string{},
			},
			childID: {
				PersonalVolume:   100.0,
				RetailVolume:     0.0,
				Status:           "active",
				HasOrderInPeriod: true,
				ActiveProducts:   []string{},
			},
		},
		VolumeSources: []VolumeSourceDTO{},
	}

	result, err := client.EvaluateRanks(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, result.Ranks, 2)

	for _, id := range []string{rootID, childID} {
		got, ok := result.Ranks[id]
		require.True(t, ok, "expected entry for %s", id)
		// The plan defines one rank (member, ordinal 1) with a structure
		// qualification on "Test" requiring PV >= 0. Both distributors are
		// in the tree with positive PV, so both qualify.
		assert.Equal(t, "qualified", got.Kind)
		assert.Equal(t, "member", got.Rank)
		assert.Equal(t, uint16(1), got.Ordinal)
	}
}

func TestEngineClient_EvaluateRanks_WithPersistence_WritesEntries(t *testing.T) {
	mock := &mockTransport{
		response: json.RawMessage(`{"ranks":{
            "00000000-0000-0000-0000-000000000001":{"kind":"qualified","rank":"silver","ordinal":2},
            "00000000-0000-0000-0000-000000000002":{"kind":"unranked"}
        }}`),
	}
	client := NewEngineClientWithTransport(mock)
	store := NewMemoryQualificationHistoryStore()
	ctx := context.Background()

	req := EvaluateRanksRequest{
		Distributors:  map[string]DistributorPrimitivesDTO{},
		VolumeSources: []VolumeSourceDTO{},
	}

	result, err := client.EvaluateRanks(ctx, req, WithPersistence("2026-05", store))
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, result.Ranks, 2)

	rows, err := store.GetByPeriod(ctx, "2026-05")
	require.NoError(t, err)
	require.Len(t, rows, 2)

	byUser := map[string]QualificationHistoryRow{}
	for _, r := range rows {
		byUser[r.UserID.String()] = r
	}

	q, ok := byUser["00000000-0000-0000-0000-000000000001"]
	require.True(t, ok, "qualified user must be present in persisted rows")
	require.NotNil(t, q.Rank)
	assert.Equal(t, "silver", *q.Rank)
	require.NotNil(t, q.Ordinal)
	assert.Equal(t, uint16(2), *q.Ordinal)

	u, ok := byUser["00000000-0000-0000-0000-000000000002"]
	require.True(t, ok, "unranked user must be present in persisted rows")
	assert.Nil(t, u.Rank)
	assert.Nil(t, u.Ordinal)
}

func TestEngineClient_EvaluateRanks_WithPersistence_NilStoreReturnsResultAndError(t *testing.T) {
	mock := &mockTransport{
		response: json.RawMessage(`{"ranks":{"00000000-0000-0000-0000-000000000001":{"kind":"unranked"}}}`),
	}
	client := NewEngineClientWithTransport(mock)

	req := EvaluateRanksRequest{
		Distributors:  map[string]DistributorPrimitivesDTO{},
		VolumeSources: []VolumeSourceDTO{},
	}

	result, err := client.EvaluateRanks(context.Background(), req, WithPersistence("2026-05", nil))
	require.Error(t, err)
	require.NotNil(t, result, "engine result must be returned even when persistence rejects nil store")
	assert.Contains(t, err.Error(), "non-nil store")
}

func TestEngineClient_EvaluateRanks_WithPersistence_EmptyPeriodIDReturnsResultAndError(t *testing.T) {
	mock := &mockTransport{
		response: json.RawMessage(`{"ranks":{"00000000-0000-0000-0000-000000000001":{"kind":"unranked"}}}`),
	}
	client := NewEngineClientWithTransport(mock)
	store := NewMemoryQualificationHistoryStore()

	req := EvaluateRanksRequest{
		Distributors:  map[string]DistributorPrimitivesDTO{},
		VolumeSources: []VolumeSourceDTO{},
	}

	result, err := client.EvaluateRanks(context.Background(), req, WithPersistence("", store))
	require.Error(t, err)
	require.NotNil(t, result)
	assert.Contains(t, err.Error(), "non-empty period_id")

	// No rows written.
	rows, err := store.GetByPeriod(context.Background(), "")
	require.NoError(t, err)
	assert.Empty(t, rows)
}

func TestEngineClient_EvaluateRanks_WithPersistence_BothInvalidReturnsResultAndError(t *testing.T) {
	// The both-invalid case (empty period_id AND nil store) must surface an
	// error rather than silently no-op as "no option was passed." This is
	// why evaluateRanksConfig has an explicit persistRequested flag.
	mock := &mockTransport{
		response: json.RawMessage(`{"ranks":{}}`),
	}
	client := NewEngineClientWithTransport(mock)

	req := EvaluateRanksRequest{
		Distributors:  map[string]DistributorPrimitivesDTO{},
		VolumeSources: []VolumeSourceDTO{},
	}

	result, err := client.EvaluateRanks(context.Background(), req, WithPersistence("", nil))
	require.Error(t, err, "WithPersistence('', nil) must error rather than no-op")
	require.NotNil(t, result, "engine result must still be returned")
	assert.Contains(t, err.Error(), "non-empty period_id")
}

// --- Board plan contract tests (mock) ---

func TestEngineClient_BoardCreateBoardPlan_MockParams(t *testing.T) {
	mock := &mockTransport{
		response: json.RawMessage(`null`),
	}
	client := NewEngineClientWithTransport(mock)

	config := json.RawMessage(`{"cycle_commission":10.0,"re_entry":true}`)
	err := client.CreateBoardPlan(context.Background(), "BoardTest", 2, 3, config)
	require.NoError(t, err)

	assert.Equal(t, "create_board_plan", mock.lastOp)
	assert.JSONEq(t, `{
		"structure":"BoardTest",
		"width":2,
		"height":3,
		"config":{"cycle_commission":10.0,"re_entry":true}
	}`, string(mock.lastParams))
}

func TestEngineClient_BoardAddMember_MockResponse(t *testing.T) {
	mock := &mockTransport{
		response: json.RawMessage(`{
			"board_id":"board-001",
			"position":3,
			"cycle_events":[{
				"board_id":"board-001",
				"cycled_member":"00000000-0000-0000-0000-000000000001",
				"new_boards":["board-002"],
				"re_entry_board":"board-003"
			}]
		}`),
	}
	client := NewEngineClientWithTransport(mock)

	result, err := client.BoardAddMember(context.Background(), "BoardTest",
		"00000000-0000-0000-0000-000000000001",
		"00000000-0000-0000-0000-000000000002",
		1000)
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.Equal(t, "board_add_member", mock.lastOp)
	assert.Equal(t, "board-001", result.BoardID)
	assert.Equal(t, 3, result.Position)
	require.Len(t, result.CycleEvents, 1)
	assert.Equal(t, "00000000-0000-0000-0000-000000000001", result.CycleEvents[0].CycledMember)
	assert.Equal(t, []string{"board-002"}, result.CycleEvents[0].NewBoards)
	require.NotNil(t, result.CycleEvents[0].ReEntryBoard)
	assert.Equal(t, "board-003", *result.CycleEvents[0].ReEntryBoard)
}

func TestEngineClient_BoardRemoveMember_MockResponse(t *testing.T) {
	mock := &mockTransport{
		response: json.RawMessage(`{
			"compacted":["00000000-0000-0000-0000-000000000002","00000000-0000-0000-0000-000000000003"],
			"cycle_events":[]
		}`),
	}
	client := NewEngineClientWithTransport(mock)

	result, err := client.BoardRemoveMember(context.Background(), "BoardTest",
		"00000000-0000-0000-0000-000000000001", 2000)
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.Equal(t, "board_remove_member", mock.lastOp)
	assert.Equal(t, []string{
		"00000000-0000-0000-0000-000000000002",
		"00000000-0000-0000-0000-000000000003",
	}, result.Compacted)
	assert.Empty(t, result.CycleEvents)
}

func TestEngineClient_BoardCompressInactive_MockResponse(t *testing.T) {
	mock := &mockTransport{
		response: json.RawMessage(`{
			"compressed":[{"user_id":"00000000-0000-0000-0000-000000000001","board_id":"board-001"}],
			"cycle_events":[{
				"board_id":"board-001",
				"cycled_member":"00000000-0000-0000-0000-000000000002",
				"new_boards":["board-002"],
				"re_entry_board":null
			}]
		}`),
	}
	client := NewEngineClientWithTransport(mock)

	result, err := client.BoardCompressInactive(context.Background(), "BoardTest",
		[]string{"00000000-0000-0000-0000-000000000001"}, 3000)
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.Equal(t, "board_compress_inactive", mock.lastOp)
	require.Len(t, result.Compressed, 1)
	assert.Equal(t, "00000000-0000-0000-0000-000000000001", result.Compressed[0].UserID)
	assert.Equal(t, "board-001", result.Compressed[0].BoardID)
	require.Len(t, result.CycleEvents, 1)
	assert.Equal(t, "00000000-0000-0000-0000-000000000002", result.CycleEvents[0].CycledMember)
	assert.Nil(t, result.CycleEvents[0].ReEntryBoard)
}

func TestEngineClient_BoardDetectStalled_MockResponse(t *testing.T) {
	mock := &mockTransport{
		response: json.RawMessage(`[{
			"board_id":"board-001",
			"last_activity_at":500,
			"filled_positions":3,
			"total_positions":7,
			"members":["00000000-0000-0000-0000-000000000001","00000000-0000-0000-0000-000000000002"]
		}]`),
	}
	client := NewEngineClientWithTransport(mock)

	stalled, err := client.BoardDetectStalled(context.Background(), "BoardTest", 1000)
	require.NoError(t, err)
	require.Len(t, stalled, 1)

	assert.Equal(t, "board_detect_stalled", mock.lastOp)
	assert.Equal(t, "board-001", stalled[0].BoardID)
	assert.Equal(t, int64(500), stalled[0].LastActivityAt)
	assert.Equal(t, 3, stalled[0].FilledPositions)
	assert.Equal(t, 7, stalled[0].TotalPositions)
	assert.Len(t, stalled[0].Members, 2)
}

func TestEngineClient_BoardDissolve_MockResponse(t *testing.T) {
	mock := &mockTransport{
		response: json.RawMessage(`{
			"dissolved_board_id":"board-001",
			"displaced_members":["00000000-0000-0000-0000-000000000001","00000000-0000-0000-0000-000000000002"]
		}`),
	}
	client := NewEngineClientWithTransport(mock)

	result, err := client.BoardDissolve(context.Background(), "BoardTest", "board-001", 4000)
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.Equal(t, "board_dissolve", mock.lastOp)
	assert.Equal(t, "board-001", result.DissolvedBoardID)
	assert.Equal(t, []string{
		"00000000-0000-0000-0000-000000000001",
		"00000000-0000-0000-0000-000000000002",
	}, result.DisplacedMembers)
}

func TestEngineClient_BoardGetState_MockResponse(t *testing.T) {
	mock := &mockTransport{
		response: json.RawMessage(`{"board_id":"board-001","positions":[null,"00000000-0000-0000-0000-000000000001"]}`),
	}
	client := NewEngineClientWithTransport(mock)

	result, err := client.BoardGetState(context.Background(), "BoardTest", "board-001")
	require.NoError(t, err)

	assert.Equal(t, "board_get_state", mock.lastOp)
	assert.JSONEq(t, `{"board_id":"board-001","positions":[null,"00000000-0000-0000-0000-000000000001"]}`, string(result))
}

func TestEngineClient_BoardGetMember_MockResponse(t *testing.T) {
	mock := &mockTransport{
		response: json.RawMessage(`{"board_id":"board-001"}`),
	}
	client := NewEngineClientWithTransport(mock)

	info, err := client.BoardGetMember(context.Background(), "BoardTest", "00000000-0000-0000-0000-000000000001")
	require.NoError(t, err)
	require.NotNil(t, info)

	assert.Equal(t, "board_get_member", mock.lastOp)
	assert.Equal(t, "board-001", info.BoardID)
}

func TestEngineClient_BoardGetMember_ReturnsNilWhenNotFound(t *testing.T) {
	mock := &mockTransport{
		response: json.RawMessage(`null`),
	}
	client := NewEngineClientWithTransport(mock)

	info, err := client.BoardGetMember(context.Background(), "BoardTest", "00000000-0000-0000-0000-000000000099")
	require.NoError(t, err)
	assert.Nil(t, info)
}

func TestEngineClient_BoardListBoards_MockResponse(t *testing.T) {
	parentID := "board-000"
	mock := &mockTransport{
		response: json.RawMessage(`[{
			"id":"board-001",
			"filled_count":5,
			"total_positions":7,
			"created_at":1000,
			"last_activity_at":2000,
			"parent_board_id":"board-000"
		}]`),
	}
	client := NewEngineClientWithTransport(mock)

	boards, err := client.BoardListBoards(context.Background(), "BoardTest")
	require.NoError(t, err)
	require.Len(t, boards, 1)

	assert.Equal(t, "board_list", mock.lastOp)
	assert.Equal(t, "board-001", boards[0].ID)
	assert.Equal(t, 5, boards[0].FilledCount)
	assert.Equal(t, 7, boards[0].TotalPositions)
	assert.Equal(t, int64(1000), boards[0].CreatedAt)
	assert.Equal(t, int64(2000), boards[0].LastActivityAt)
	require.NotNil(t, boards[0].ParentBoardID)
	assert.Equal(t, parentID, *boards[0].ParentBoardID)
}

func TestEngineClient_CalculateBoardCommissions_MockParams(t *testing.T) {
	mock := &mockTransport{
		response: json.RawMessage(`{
			"earnings":[{
				"earner_id":"00000000-0000-0000-0000-000000000001",
				"board_id":"board-001",
				"dollar_amount":25.50,
				"cycle_number":2,
				"capped":false
			}],
			"updated_cycle_counts":{"00000000-0000-0000-0000-000000000001":3}
		}`),
	}
	client := NewEngineClientWithTransport(mock)

	req := CalculateBoardCommissionsRequest{
		StructureName: "BoardTest",
		CycleEvents: []CycleEventDTO{
			{
				BoardID:      "board-001",
				CycledMember: "00000000-0000-0000-0000-000000000001",
				NewBoards:    []string{"board-002"},
				ReEntryBoard: nil,
			},
		},
		PeriodCycleCounts: map[string]int{
			"00000000-0000-0000-0000-000000000001": 2,
		},
	}

	result, err := client.CalculateBoardCommissions(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.Equal(t, "board_calculate_commissions", mock.lastOp)
	// The board cycling config no longer crosses the wire (HEU-603).
	// Asserting the exact param set is what makes its removal stick.
	assert.JSONEq(t, `{
		"structure": "BoardTest",
		"cycle_events": [{
			"board_id": "board-001",
			"cycled_member": "00000000-0000-0000-0000-000000000001",
			"new_boards": ["board-002"],
			"re_entry_board": null
		}],
		"period_cycle_counts": {"00000000-0000-0000-0000-000000000001": 2}
	}`, string(mock.lastParams))

	// Duplicates what JSONEq already catches, and earns its place for the same
	// reason the streamline twin above gives: someone re-adding the field is
	// likely to "fix" the red JSONEq by regenerating the golden, which silences
	// it but not this. Deleting the guard then has to be deliberate.
	//
	// Neither catches a field re-added with `,omitempty` and left unpopulated.
	// That shape is dead weight rather than a bypass: the worker ignores unknown
	// params, and board_calculate_ignores_request_scoped_config pins that it
	// keeps doing so. That Rust test builds its own request string, so it
	// watches the worker, not this client.
	assert.NotContains(t, string(mock.lastParams), `"config"`)

	require.Len(t, result.Earnings, 1)
	assert.Equal(t, "00000000-0000-0000-0000-000000000001", result.Earnings[0].EarnerID)
	assert.Equal(t, "board-001", result.Earnings[0].BoardID)
	assert.InDelta(t, 25.50, result.Earnings[0].DollarAmount, 1e-9)
	assert.Equal(t, 2, result.Earnings[0].CycleNumber)
	assert.False(t, result.Earnings[0].Capped)

	require.Contains(t, result.UpdatedCycleCounts, "00000000-0000-0000-0000-000000000001")
	assert.Equal(t, 3, result.UpdatedCycleCounts["00000000-0000-0000-0000-000000000001"])
}

// TestEngineClient_CalculateBoardCommissions_NilCollections pins the wire shape
// of a first-period call, where the natural Go request leaves both collections
// nil: no prior cycle counts, and nothing cycled.
//
// The two fields are treated differently on purpose, and this test is what
// holds that difference in place.
//
// PeriodCycleCounts is optional, so it carries omitempty and drops off the
// wire. CarryForward on CalculateBinaryPairingRequest (wire_types.go:92)
// already does the same.
//
// CycleEvents must NOT get omitempty, which is why the assertion pins it
// present as null rather than merely absent. It is required on the Rust side,
// and dropping the key entirely returns INVALID_PARAMS "missing field". That
// loud failure is deliberate: a caller who forgets the field should hear about
// it, not be paid zero. The worker reads an explicit null as empty
// (null_as_default in handlers/board_plan.rs), so null is the correct shape to
// send. board_calculate_still_requires_cycle_events guards the other half from
// the Rust side, but it builds its own JSON and never touches this struct.
func TestEngineClient_CalculateBoardCommissions_NilCollections(t *testing.T) {
	mock := &mockTransport{
		response: json.RawMessage(`{"earnings":[],"updated_cycle_counts":{}}`),
	}
	client := NewEngineClientWithTransport(mock)

	req := CalculateBoardCommissionsRequest{
		StructureName:     "BoardTest",
		CycleEvents:       nil,
		PeriodCycleCounts: nil,
	}

	_, err := client.CalculateBoardCommissions(context.Background(), req)
	require.NoError(t, err)

	// Positive on the whole param set, matching the _MockParams sibling. A
	// NotContains on one key would also pass if the field were renamed away.
	assert.JSONEq(t, `{
		"structure": "BoardTest",
		"cycle_events": null
	}`, string(mock.lastParams))
}

// mockTransport is a test double for EngineTransport.
type mockTransport struct {
	response   json.RawMessage
	err        error
	lastOp     string
	lastParams json.RawMessage
	closed     bool
}

func (m *mockTransport) Call(_ context.Context, op string, params json.RawMessage) (json.RawMessage, error) {
	m.lastOp = op
	m.lastParams = params
	return m.response, m.err
}

func (m *mockTransport) Close() error {
	m.closed = true
	return nil
}

// --- Streamline contract tests ---

func TestEngineClient_StreamlineLifecycle(t *testing.T) {
	client, err := NewEngineClient(context.Background(), findWorkerBinary(t))
	require.NoError(t, err)
	defer func() { _ = client.Stop() }()

	ctx := context.Background()
	structure := "TestStreamline"
	user1 := "00000000-0000-0000-0000-000000000011"
	user2 := "00000000-0000-0000-0000-000000000012"
	user3 := "00000000-0000-0000-0000-000000000013"

	// Create streamline.
	err = client.CreateStreamline(ctx, structure, "sponsor_stream", false, true, 1000)
	require.NoError(t, err)

	// Add 3 members.
	r1, err := client.StreamlineAddMember(ctx, structure, StreamlineAddMemberRequest{
		UserID: user1, SponsorID: "00000000-0000-0000-0000-000000000099", Timestamp: 1001,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, r1.StreamID)
	assert.Equal(t, 0, r1.Position)

	r2, err := client.StreamlineAddMember(ctx, structure, StreamlineAddMemberRequest{
		UserID: user2, SponsorID: user1, Timestamp: 1002,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, r2.StreamID)
	assert.Equal(t, 1, r2.Position)

	r3, err := client.StreamlineAddMember(ctx, structure, StreamlineAddMemberRequest{
		UserID: user3, SponsorID: user1, Timestamp: 1003,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, r3.StreamID)

	// Verify member info.
	info, err := client.StreamlineGetMember(ctx, structure, user2)
	require.NoError(t, err)
	assert.Len(t, info.Streams, 1)
	assert.Equal(t, 1, info.Streams[0].StreamID)

	// List streams.
	streams, err := client.StreamlineListStreams(ctx, structure)
	require.NoError(t, err)
	assert.Len(t, streams, 1)
	assert.Equal(t, 3, streams[0].MemberCount)

	// Get stream detail.
	stream, err := client.StreamlineGetStream(ctx, structure, 1)
	require.NoError(t, err)
	assert.Equal(t, 3, stream.MemberCount)
	assert.False(t, stream.Frozen)
}

func TestEngineClient_StreamlineExpandFreeze(t *testing.T) {
	client, err := NewEngineClient(context.Background(), findWorkerBinary(t))
	require.NoError(t, err)
	defer func() { _ = client.Stop() }()

	ctx := context.Background()
	structure := "TestStreamline"
	user1 := "00000000-0000-0000-0000-000000000011"

	err = client.CreateStreamline(ctx, structure, "sponsor_stream", false, true, 1000)
	require.NoError(t, err)

	_, err = client.StreamlineAddMember(ctx, structure, StreamlineAddMemberRequest{
		UserID: user1, SponsorID: "00000000-0000-0000-0000-000000000099", Timestamp: 1001,
	})
	require.NoError(t, err)

	// Expand to 3 streams.
	expandResult, err := client.StreamlineExpandStreams(ctx, structure, StreamlineExpandRequest{
		UserID: user1, TotalAllowed: 3, Timestamp: 1002,
	})
	require.NoError(t, err)
	assert.Len(t, expandResult.NewStreamIDs, 2)

	// Freeze back to 1.
	freezeResult, err := client.StreamlineUpdateAllowance(ctx, structure, StreamlineUpdateAllowanceRequest{
		UserID: user1, TotalAllowed: 1, Timestamp: 2000,
	})
	require.NoError(t, err)
	assert.Len(t, freezeResult.Frozen, 2)

	// Verify frozen.
	stream2, err := client.StreamlineGetStream(ctx, structure, 2)
	require.NoError(t, err)
	assert.True(t, stream2.Frozen)
}

func TestEngineClient_StreamlineRemoveMember(t *testing.T) {
	client, err := NewEngineClient(context.Background(), findWorkerBinary(t))
	require.NoError(t, err)
	defer func() { _ = client.Stop() }()

	ctx := context.Background()
	structure := "TestStreamline"
	user1 := "00000000-0000-0000-0000-000000000011"
	user2 := "00000000-0000-0000-0000-000000000012"

	err = client.CreateStreamline(ctx, structure, "sponsor_stream", false, true, 1000)
	require.NoError(t, err)

	_, err = client.StreamlineAddMember(ctx, structure, StreamlineAddMemberRequest{
		UserID: user1, SponsorID: "00000000-0000-0000-0000-000000000099", Timestamp: 1001,
	})
	require.NoError(t, err)

	_, err = client.StreamlineAddMember(ctx, structure, StreamlineAddMemberRequest{
		UserID: user2, SponsorID: user1, Timestamp: 1002,
	})
	require.NoError(t, err)

	// Remove user2.
	removeResult, err := client.StreamlineRemoveMember(ctx, structure, user2, 1003)
	require.NoError(t, err)
	assert.Len(t, removeResult.RemovedFrom, 1)

	// Verify stream now has 1 member.
	stream, err := client.StreamlineGetStream(ctx, structure, 1)
	require.NoError(t, err)
	assert.Equal(t, 1, stream.MemberCount)
}

func TestEngineClient_StreamlineSnapshotRoundTrip(t *testing.T) {
	client, err := NewEngineClient(context.Background(), findWorkerBinary(t))
	require.NoError(t, err)
	defer func() { _ = client.Stop() }()

	ctx := context.Background()
	structure := "TestStreamline"
	user1 := "00000000-0000-0000-0000-000000000011"

	err = client.CreateStreamline(ctx, structure, "sponsor_stream", false, true, 1000)
	require.NoError(t, err)

	_, err = client.StreamlineAddMember(ctx, structure, StreamlineAddMemberRequest{
		UserID: user1, SponsorID: "00000000-0000-0000-0000-000000000099", Timestamp: 1001,
	})
	require.NoError(t, err)

	// Take snapshot.
	snapshot, err := client.TakeSnapshot(ctx, structure)
	require.NoError(t, err)
	assert.Equal(t, "streamline", snapshot.TreeType)

	// Restore under different name.
	err = client.RestoreSnapshot(ctx, "Restored", snapshot.TreeType, snapshot.Data)
	require.NoError(t, err)

	// Verify restored.
	info, err := client.StreamlineGetMember(ctx, "Restored", user1)
	require.NoError(t, err)
	assert.Len(t, info.Streams, 1)
}

// TestEngineClient_AddNodeAt_WireParams pins the mapping from Go parameters to
// wire keys. AddNodeAt and AddNode both take (userID, parentID, sponsorID) and
// all three are strings, so a transposition compiles clean and would silently
// place every matrix node under the wrong parent. Distinct sentinel values make
// a swap fail here instead of in production.
func TestEngineClient_AddNodeAt_WireParams(t *testing.T) {
	mock := &mockTransport{response: json.RawMessage(`{"added":true}`)}
	client := NewEngineClientWithTransport(mock)

	err := client.AddNodeAt(context.Background(), "m-tree",
		"user-child", "user-parent", "user-sponsor", 2, 1700000000)
	require.NoError(t, err)

	assert.Equal(t, "add_node_at", mock.lastOp)
	assert.JSONEq(t, `{
		"structure":   "m-tree",
		"user_id":     "user-child",
		"parent_id":   "user-parent",
		"sponsor_id":  "user-sponsor",
		"position":    2,
		"enrolled_at": 1700000000
	}`, string(mock.lastParams))
}

// --- Nil collection wire shapes (HEU-626) ---
//
// A nil Go map or slice marshals to JSON null, not {} or []. These pin the
// shape each request DTO puts on the wire, and pin which fields are
// deliberately optional.
//
// What they catch: adding omitempty to a required field. That would drop the
// key, and the Rust side takes deserialize_with with no serde default on every
// required collection, so the key going missing is a hard serde error --
// INVALID_PARAMS "missing field" at runtime, on a money path. These tests turn
// that into a test failure instead. The silent-zero hazard the fail-loud rule
// warns about needs omitempty AND a serde default; the required fields
// deliberately have neither, which is what keeps the failure loud. See the
// board comment above for the same reasoning stated per-field.
//
// Every case asserts positively over the whole param set with assert.JSONEq. A
// NotContains on one key would also pass if the field were renamed away.
// JSONEq compares parsed JSON, so it catches a null that should be [] and any
// extra or missing key, but not key order or whitespace.
//
// Eight of the nine are the Go twin named in the doc comment of the matching
// Rust test in engine/network-engine-worker/tests/worker_integration.rs.
// EvaluateRanks_OmitsEmptyHistory is the exception: it pins the omitempty
// shape, which no Rust test names because there is nothing on that side to
// observe about a key that never arrives.

func TestEngineClient_CalculateUnilevel_NilCollections(t *testing.T) {
	mock := &mockTransport{response: json.RawMessage(`[]`)}
	client := NewEngineClientWithTransport(mock)

	_, err := client.CalculateUnilevel(context.Background(), CalculateUnilevelRequest{
		StructureName: "Test",
		Snapshots:     nil,
		Volume:        nil,
	})
	require.NoError(t, err)

	assert.JSONEq(t, `{
		"structure": "Test",
		"snapshots": null,
		"volume": null
	}`, string(mock.lastParams))
}

func TestEngineClient_CalculateGeneration_NilCollections(t *testing.T) {
	mock := &mockTransport{response: json.RawMessage(`[]`)}
	client := NewEngineClientWithTransport(mock)

	_, err := client.CalculateGeneration(context.Background(), CalculateGenerationRequest{
		StructureName: "GenTree",
		Snapshots:     nil,
		Volume:        nil,
	})
	require.NoError(t, err)

	assert.JSONEq(t, `{
		"structure": "GenTree",
		"snapshots": null,
		"volume": null
	}`, string(mock.lastParams))
}

func TestEngineClient_CalculateMatrix_NilCollections(t *testing.T) {
	mock := &mockTransport{response: json.RawMessage(`[]`)}
	client := NewEngineClientWithTransport(mock)

	_, err := client.CalculateMatrix(context.Background(), CalculateMatrixRequest{
		StructureName: "Test",
		Snapshots:     nil,
		Volume:        nil,
	})
	require.NoError(t, err)

	assert.JSONEq(t, `{
		"structure": "Test",
		"snapshots": null,
		"volume": null
	}`, string(mock.lastParams))
}

func TestEngineClient_CalculateStairstep_NilCollections(t *testing.T) {
	mock := &mockTransport{response: json.RawMessage(`[]`)}
	client := NewEngineClientWithTransport(mock)

	_, err := client.CalculateStairstep(context.Background(), CalculateStairstepRequest{
		StructureName: "Test",
		Snapshots:     nil,
		Volume:        nil,
	})
	require.NoError(t, err)

	assert.JSONEq(t, `{
		"structure": "Test",
		"snapshots": null,
		"volume": null
	}`, string(mock.lastParams))
}

func TestEngineClient_CalculateStreamline_NilCollections(t *testing.T) {
	mock := &mockTransport{response: json.RawMessage(`[]`)}
	client := NewEngineClientWithTransport(mock)

	_, err := client.CalculateStreamline(context.Background(), CalculateStreamlineRequest{
		StructureName: "TestStreamline",
		Snapshots:     nil,
		Volume:        nil,
	})
	require.NoError(t, err)

	assert.JSONEq(t, `{
		"structure": "TestStreamline",
		"snapshots": null,
		"volume": null
	}`, string(mock.lastParams))
}

// CarryForward and Ownership both carry omitempty, so a nil map drops the key
// rather than sending null. That is correct for these two, though by different
// routes: carry_forward pairs serde default with null_as_empty, while ownership
// is an Option<HashMap<..>> whose serde default plus Option absorb null
// natively without the helper. Either way absent and null mean the same thing.
// Snapshots and Volume have no omitempty and must still appear as null.
func TestEngineClient_CalculateBinaryPairing_NilCollections(t *testing.T) {
	mock := &mockTransport{
		response: json.RawMessage(`{"earnings":[],"carry_forward":{}}`),
	}
	client := NewEngineClientWithTransport(mock)

	_, err := client.CalculateBinaryPairing(context.Background(), CalculateBinaryPairingRequest{
		StructureName: "BinaryCalc",
		Snapshots:     nil,
		Volume:        nil,
		CarryForward:  nil,
		Ownership:     nil,
	})
	require.NoError(t, err)

	assert.JSONEq(t, `{
		"structure": "BinaryCalc",
		"snapshots": null,
		"volume": null
	}`, string(mock.lastParams))
}

func TestEngineClient_EvaluateRanks_NilCollections(t *testing.T) {
	mock := &mockTransport{response: json.RawMessage(`{"ranks":{}}`)}
	client := NewEngineClientWithTransport(mock)

	_, err := client.EvaluateRanks(context.Background(), EvaluateRanksRequest{
		Distributors:  nil,
		VolumeSources: nil,
	})
	require.NoError(t, err)

	assert.JSONEq(t, `{
		"distributors": null,
		"volume_sources": null
	}`, string(mock.lastParams))
}

// The nested field needs a populated Distributors map: a nil outer map leaves
// no distributor to carry it. ActiveProducts has no omitempty, so a nil slice
// reaches the wire as null and the Rust DistributorPrimitives must read it as
// empty.
func TestEngineClient_EvaluateRanks_NilActiveProducts(t *testing.T) {
	mock := &mockTransport{response: json.RawMessage(`{"ranks":{}}`)}
	client := NewEngineClientWithTransport(mock)

	_, err := client.EvaluateRanks(context.Background(), EvaluateRanksRequest{
		Distributors: map[string]DistributorPrimitivesDTO{
			"00000000-0000-0000-0000-000000000001": {
				PersonalVolume: 0.0,
				Status:         "active",
				ActiveProducts: nil,
			},
		},
		VolumeSources: []VolumeSourceDTO{},
	})
	require.NoError(t, err)

	assert.JSONEq(t, `{
		"distributors": {
			"00000000-0000-0000-0000-000000000001": {
				"personal_volume": 0,
				"retail_volume": 0,
				"status": "active",
				"has_order_in_period": false,
				"active_products": null
			}
		},
		"volume_sources": []
	}`, string(mock.lastParams))
}

// HistoryWindow and History both carry omitempty, so nil drops them entirely
// rather than sending null. Unlike the required collections above, that is the
// intended shape: the Rust side pairs serde default with the null tolerance, so
// absent means "no history" and a no-gate plan never sends the keys.
func TestEngineClient_EvaluateRanks_OmitsEmptyHistory(t *testing.T) {
	mock := &mockTransport{response: json.RawMessage(`{"ranks":{}}`)}
	client := NewEngineClientWithTransport(mock)

	_, err := client.EvaluateRanks(context.Background(), EvaluateRanksRequest{
		Distributors:  map[string]DistributorPrimitivesDTO{},
		VolumeSources: []VolumeSourceDTO{},
		HistoryWindow: nil,
		History:       nil,
	})
	require.NoError(t, err)

	assert.JSONEq(t, `{
		"distributors": {},
		"volume_sources": []
	}`, string(mock.lastParams))
}
