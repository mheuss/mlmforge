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

	err := client.AddNode(context.Background(), "Test",
		"00000000-0000-0000-0000-000000000002",
		"00000000-0000-0000-0000-000000000001",
		"00000000-0000-0000-0000-000000000001",
		200)
	require.NoError(t, err)

	assert.Equal(t, "add_node", mock.lastOp)
	assert.JSONEq(t, `{
		"structure":"Test",
		"user_id":"00000000-0000-0000-0000-000000000002",
		"parent_id":"00000000-0000-0000-0000-000000000001",
		"sponsor_id":"00000000-0000-0000-0000-000000000001",
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
