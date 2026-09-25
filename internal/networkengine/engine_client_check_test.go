package networkengine

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEngineClient_CheckMutation_WireParams(t *testing.T) {
	cases := []struct {
		name string
		m    Mutation
		want string
	}{
		{"add_root", CheckAddRoot("u1", 100),
			`{"structure":"t","mutation":"add_root","user_id":"u1","enrolled_at":100}`},
		{"add_node without a position", CheckAddNode("u2", "u1", "u3", 200),
			`{"structure":"t","mutation":"add_node","user_id":"u2","parent_id":"u1","sponsor_id":"u3","enrolled_at":200}`},
		{"add_node with a position", CheckAddNode("u2", "u1", "u3", 200, WithPosition(1)),
			`{"structure":"t","mutation":"add_node","user_id":"u2","parent_id":"u1","sponsor_id":"u3","position":1,"enrolled_at":200}`},
		{"add_node_at", CheckAddNodeAt("u2", "u1", "u3", 2, 200),
			`{"structure":"t","mutation":"add_node_at","user_id":"u2","parent_id":"u1","sponsor_id":"u3","position":2,"enrolled_at":200}`},
		{"remove_node", CheckRemoveNode("u2"),
			`{"structure":"t","mutation":"remove_node","user_id":"u2"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mock := &mockTransport{response: json.RawMessage(`{"checked":true}`)}
			client := newEngineClientWithTransport(mock)

			require.NoError(t, client.CheckMutation(context.Background(), "t", tc.m))

			assert.Equal(t, "check_mutation", mock.lastOp)
			assert.JSONEq(t, tc.want, string(mock.lastParams))
		})
	}
}

func TestEngineClient_CheckMutation_SendsTheParamsTheRealOpSends(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name  string
		apply func(*EngineClient) error
		check Mutation
	}{
		{"add_root",
			func(c *EngineClient) error { return c.AddRoot(ctx, "t", "u1", 100) },
			CheckAddRoot("u1", 100)},
		{"add_node",
			func(c *EngineClient) error { return c.AddNode(ctx, "t", "u2", "u1", "u3", 200, WithPosition(1)) },
			CheckAddNode("u2", "u1", "u3", 200, WithPosition(1))},
		{"add_node_at",
			func(c *EngineClient) error { return c.AddNodeAt(ctx, "t", "u2", "u1", "u3", 2, 200) },
			CheckAddNodeAt("u2", "u1", "u3", 2, 200)},
		{"remove_node",
			func(c *EngineClient) error { _, err := c.RemoveNode(ctx, "t", "u2"); return err },
			CheckRemoveNode("u2")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			applied := &mockTransport{response: json.RawMessage(`{"added":true,"removed":true,"responsored":[]}`)}
			require.NoError(t, tc.apply(newEngineClientWithTransport(applied)))
			checked := &mockTransport{response: json.RawMessage(`{"checked":true}`)}
			require.NoError(t, newEngineClientWithTransport(checked).CheckMutation(ctx, "t", tc.check))

			var appliedParams, checkedParams map[string]any
			require.NoError(t, json.Unmarshal(applied.lastParams, &appliedParams))
			require.NoError(t, json.Unmarshal(checked.lastParams, &checkedParams))
			assert.Equal(t, tc.name, applied.lastOp)
			assert.Equal(t, tc.name, checkedParams["mutation"])
			delete(checkedParams, "mutation")
			assert.Equal(t, appliedParams, checkedParams)
		})
	}
}

func TestEngineClient_CheckMutation_RefusesTheZeroMutation(t *testing.T) {
	mock := &mockTransport{response: json.RawMessage(`{"checked":true}`)}
	client := newEngineClientWithTransport(mock)

	err := client.CheckMutation(context.Background(), "t", Mutation{})

	require.EqualError(t, err, "check_mutation: the Mutation has no op")
	assert.Empty(t, mock.lastOp, "nothing may reach the worker")
}

func TestEngineClient_CheckMutation_AgreesWithTheRealOp(t *testing.T) {
	binary := findWorkerBinary(t)
	ctx := context.Background()
	const tree = "t"
	root, child, other := testUserUUID(1), testUserUUID(2), testUserUUID(3)

	create := func(treeType string) func(*testing.T, *EngineClient) {
		return func(t *testing.T, c *EngineClient) {
			if treeType == treeTypeMatrix {
				require.NoError(t, c.CreateMatrixTree(ctx, tree, 3, "breadth_first"))
				return
			}
			require.NoError(t, c.CreateTree(ctx, tree, treeType))
		}
	}
	withRoot := func(treeType string) func(*testing.T, *EngineClient) {
		return func(t *testing.T, c *EngineClient) {
			create(treeType)(t, c)
			require.NoError(t, c.AddRoot(ctx, tree, root, 100))
		}
	}
	withChild := func(treeType string) func(*testing.T, *EngineClient) {
		return func(t *testing.T, c *EngineClient) {
			withRoot(treeType)(t, c)
			switch treeType {
			case treeTypeMatrix:
				require.NoError(t, c.AddNodeAt(ctx, tree, child, root, root, 0, 200))
			case treeTypeBinary:
				require.NoError(t, c.AddNode(ctx, tree, child, root, root, 200, WithPosition(0)))
			default:
				require.NoError(t, c.AddNode(ctx, tree, child, root, root, 200))
			}
		}
	}

	cases := []struct {
		name     string
		setup    func(*testing.T, *EngineClient)
		check    Mutation
		apply    func(*EngineClient) error
		wantCode string
	}{
		{"add_root on an empty tree", create(treeTypeUnilevel), CheckAddRoot(root, 100),
			func(c *EngineClient) error { return c.AddRoot(ctx, tree, root, 100) }, ""},
		{"add_root with a root present", withRoot(treeTypeUnilevel), CheckAddRoot(child, 100),
			func(c *EngineClient) error { return c.AddRoot(ctx, tree, child, 100) }, "ROOT_ALREADY_EXISTS"},
		{"unilevel add_node", withRoot(treeTypeUnilevel), CheckAddNode(child, root, root, 200),
			func(c *EngineClient) error { return c.AddNode(ctx, tree, child, root, root, 200) }, ""},
		{"unilevel add_node under a missing parent", withRoot(treeTypeUnilevel), CheckAddNode(child, other, root, 200),
			func(c *EngineClient) error { return c.AddNode(ctx, tree, child, other, root, 200) }, "USER_NOT_FOUND"},
		{"binary add_node", withRoot(treeTypeBinary), CheckAddNode(child, root, root, 200, WithPosition(0)),
			func(c *EngineClient) error { return c.AddNode(ctx, tree, child, root, root, 200, WithPosition(0)) }, ""},
		{"binary add_node on an occupied slot", withChild(treeTypeBinary), CheckAddNode(other, root, root, 300, WithPosition(0)),
			func(c *EngineClient) error { return c.AddNode(ctx, tree, other, root, root, 300, WithPosition(0)) }, "POSITION_OCCUPIED"},
		{"matrix add_node_at", withRoot(treeTypeMatrix), CheckAddNodeAt(child, root, root, 2, 200),
			func(c *EngineClient) error { return c.AddNodeAt(ctx, tree, child, root, root, 2, 200) }, ""},
		{"matrix add_node_at past the width", withRoot(treeTypeMatrix), CheckAddNodeAt(child, root, root, 3, 200),
			func(c *EngineClient) error { return c.AddNodeAt(ctx, tree, child, root, root, 3, 200) }, "INVALID_POSITION"},
		{"remove_node of a leaf", withChild(treeTypeUnilevel), CheckRemoveNode(child),
			func(c *EngineClient) error { _, err := c.RemoveNode(ctx, tree, child); return err }, ""},
		{"remove_node of a parent", withChild(treeTypeUnilevel), CheckRemoveNode(root),
			func(c *EngineClient) error { _, err := c.RemoveNode(ctx, tree, root); return err }, "HAS_CHILDREN"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, err := NewEngineClient(ctx, binary)
			require.NoError(t, err)
			defer func() { _ = client.Stop() }()
			tc.setup(t, client)

			// The real op runs second, on the state the check left. A check that
			// mutated would make a passing apply fail.
			checkErr := client.CheckMutation(ctx, tree, tc.check)
			applyErr := tc.apply(client)

			assert.Equal(t, tc.wantCode, engineCodeOf(checkErr))
			assert.Equal(t, engineErrorOf(applyErr), engineErrorOf(checkErr))
		})
	}
}

// engineCodeOf renders an error as the engine code it carries.
func engineCodeOf(err error) string {
	if err == nil {
		return ""
	}
	var e *EngineError
	if errors.As(err, &e) {
		return e.Code
	}
	return "not an engine error: " + err.Error()
}

// engineErrorOf renders an error as the engine code and message it carries.
func engineErrorOf(err error) string {
	var e *EngineError
	if errors.As(err, &e) {
		return e.Code + ": " + e.Message
	}
	return engineCodeOf(err)
}
