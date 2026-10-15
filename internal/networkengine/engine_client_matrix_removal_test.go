package networkengine

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const removedUser = "00000000-0000-0000-0000-000000000002"

func removeMatrixNodeWith(t *testing.T, response string, mode PruningMode) (*MatrixRemovalResult, *mockTransport, error) {
	t.Helper()
	mock := &mockTransport{response: json.RawMessage(response)}
	got, err := newEngineClientWithTransport(mock).RemoveMatrixNode(context.Background(), "Test", removedUser, mode)
	return got, mock, err
}

func TestEngineClient_RemoveMatrixNode_DecodesEveryField(t *testing.T) {
	got, mock, err := removeMatrixNodeWith(t, `{"removed":"`+removedUser+`",
		"promoted":"00000000-0000-0000-0000-000000000003",
		"repositioned":["00000000-0000-0000-0000-000000000004"],
		"moved_to_tank":[],
		"relocated":[{"user_id":"00000000-0000-0000-0000-000000000003","parent_id":"00000000-0000-0000-0000-000000000001","position":0,"depth":1}],
		"responsored":[{"user_id":"00000000-0000-0000-0000-000000000004","new_sponsor_id":"00000000-0000-0000-0000-000000000001"}]}`,
		PruningPromoteEarliest)

	require.NoError(t, err)
	promoted := "00000000-0000-0000-0000-000000000003"
	assert.Equal(t, &MatrixRemovalResult{
		Removed:      removedUser,
		Promoted:     &promoted,
		Repositioned: []string{"00000000-0000-0000-0000-000000000004"},
		MovedToTank:  []string{},
		Relocated: []Relocation{{UserID: "00000000-0000-0000-0000-000000000003",
			ParentID: "00000000-0000-0000-0000-000000000001", Position: 0, Depth: 1}},
		Responsored: []Responsored{{UserID: "00000000-0000-0000-0000-000000000004",
			NewSponsorID: "00000000-0000-0000-0000-000000000001"}},
	}, got)
	assert.Equal(t, "remove_node", mock.lastOp)
	assert.JSONEq(t, `{"structure":"Test","user_id":"`+removedUser+`","pruning_mode":"promote_earliest"}`,
		string(mock.lastParams))
}

func TestEngineClient_RemoveMatrixNode_RefusesAnAbsentOrNullList(t *testing.T) {
	cases := []struct{ name, response, want string }{
		{"no relocated", `{"removed":"x","promoted":null,"repositioned":[],"moved_to_tank":[],"responsored":[]}`,
			`remove_node: response "relocated" is absent or null`},
		{"null relocated", `{"removed":"x","promoted":null,"repositioned":[],"moved_to_tank":[],"relocated":null,"responsored":[]}`,
			`remove_node: response "relocated" is absent or null`},
		{"no moved_to_tank", `{"removed":"x","promoted":null,"repositioned":[],"relocated":[],"responsored":[]}`,
			`remove_node: response "moved_to_tank" is absent or null`},
		{"null moved_to_tank", `{"removed":"x","promoted":null,"repositioned":[],"moved_to_tank":null,"relocated":[],"responsored":[]}`,
			`remove_node: response "moved_to_tank" is absent or null`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := removeMatrixNodeWith(t, tc.response, PruningPromoteEarliest)

			require.EqualError(t, err, tc.want)
		})
	}
}

func TestEngineClient_RemoveMatrixNode_RefusesAnIncompleteRelocation(t *testing.T) {
	const moved = "00000000-0000-0000-0000-000000000003"
	cases := []struct{ name, relocation, want string }{
		{"no position", `{"user_id":"` + moved + `","parent_id":"p","depth":1}`,
			`remove_node: relocation of ` + moved + ` has no "position"`},
		{"null parent_id", `{"user_id":"` + moved + `","parent_id":null,"position":0,"depth":1}`,
			`remove_node: relocation of ` + moved + ` has no "parent_id"`},
		{"no depth", `{"user_id":"` + moved + `","parent_id":"p","position":0}`,
			`remove_node: relocation of ` + moved + ` has no "depth"`},
		{"no user_id", `{"parent_id":"p","position":0,"depth":1}`,
			`remove_node: relocation 0 has no "user_id"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := removeMatrixNodeWith(t, `{"removed":"x","promoted":null,"repositioned":[],"moved_to_tank":[],
				"relocated":[`+tc.relocation+`],"responsored":[]}`, PruningPromoteEarliest)

			require.EqualError(t, err, tc.want)
		})
	}
}

func TestEngineClient_RemoveMatrixNode_RefusesAnIncompleteHoldingTank(t *testing.T) {
	cases := []struct{ name, tank, want string }{
		{"an empty tank", `[]`,
			`remove_node: holding_tank removal of ` + removedUser + ` answered an empty "moved_to_tank"`},
		{"a tank without the removed user", `["00000000-0000-0000-0000-000000000003"]`,
			`remove_node: holding_tank removal of ` + removedUser +
				` answered a "moved_to_tank" that lacks it: [00000000-0000-0000-0000-000000000003]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := removeMatrixNodeWith(t, `{"removed":"`+removedUser+`","promoted":null,"repositioned":[],
				"moved_to_tank":`+tc.tank+`,"relocated":[],"responsored":[]}`, PruningHoldingTank)

			require.EqualError(t, err, tc.want)
		})
	}
}

func TestEngineClient_RemoveMatrixNode_RefusesATankOnPromoteEarliest(t *testing.T) {
	_, _, err := removeMatrixNodeWith(t, `{"removed":"`+removedUser+`","promoted":null,"repositioned":[],
		"moved_to_tank":["`+removedUser+`"],"relocated":[],"responsored":[]}`, PruningPromoteEarliest)

	require.EqualError(t, err, `remove_node: promote_earliest removal of `+removedUser+
		` answered a non-empty "moved_to_tank": [`+removedUser+`]`)
}

func TestEngineClient_CheckRemoveMatrixNode_SendsTheParamsRemoveMatrixNodeSends(t *testing.T) {
	ctx := context.Background()
	applied := &mockTransport{response: json.RawMessage(`{"removed":"` + removedUser + `","promoted":null,"repositioned":[],
		"moved_to_tank":["` + removedUser + `"],"relocated":[],"responsored":[]}`)}
	_, err := newEngineClientWithTransport(applied).RemoveMatrixNode(ctx, "t", removedUser, PruningHoldingTank)
	require.NoError(t, err)
	checked := &mockTransport{response: json.RawMessage(`{"checked":true}`)}
	require.NoError(t, newEngineClientWithTransport(checked).CheckMutation(ctx, "t", CheckRemoveMatrixNode(removedUser, PruningHoldingTank)))

	var appliedParams, checkedParams map[string]any
	require.NoError(t, json.Unmarshal(applied.lastParams, &appliedParams))
	require.NoError(t, json.Unmarshal(checked.lastParams, &checkedParams))
	assert.Equal(t, "remove_node", checkedParams["mutation"])
	delete(checkedParams, "mutation")
	assert.Equal(t, appliedParams, checkedParams)
}

func TestEngineClient_RemoveMatrixNode_OverARealWorker(t *testing.T) {
	ctx := context.Background()
	binary := findWorkerBinary(t)
	const tree = "t"
	u := testUserUUID
	build := func(t *testing.T) *EngineClient {
		t.Helper()
		c, err := NewEngineClient(ctx, binary)
		require.NoError(t, err)
		t.Cleanup(func() { _ = c.Stop() })
		require.NoError(t, c.CreateMatrixTree(ctx, tree, 2, "breadth_first"))
		require.NoError(t, c.AddRoot(ctx, tree, u(1), 1000))
		require.NoError(t, c.AddNodeAt(ctx, tree, u(2), u(1), u(1), 0, 2000))
		require.NoError(t, c.AddNodeAt(ctx, tree, u(3), u(2), u(2), 0, 3000))
		require.NoError(t, c.AddNodeAt(ctx, tree, u(4), u(2), u(2), 1, 4000))
		require.NoError(t, c.AddNodeAt(ctx, tree, u(5), u(3), u(3), 0, 5000))
		require.NoError(t, c.AddNodeAt(ctx, tree, u(6), u(3), u(3), 1, 6000))
		return c
	}

	t.Run("promote_earliest", func(t *testing.T) {
		c := build(t)

		got, err := c.RemoveMatrixNode(ctx, tree, u(2), PruningPromoteEarliest)

		require.NoError(t, err)
		promoted := u(3)
		assert.Equal(t, &MatrixRemovalResult{
			Removed: u(2), Promoted: &promoted, Repositioned: []string{u(4)}, MovedToTank: []string{},
			Relocated: []Relocation{
				{UserID: u(3), ParentID: u(1), Position: 0, Depth: 1},
				{UserID: u(5), ParentID: u(3), Position: 0, Depth: 2},
				{UserID: u(6), ParentID: u(3), Position: 1, Depth: 2},
				{UserID: u(4), ParentID: u(5), Position: 0, Depth: 3},
			},
			Responsored: []Responsored{{UserID: u(3), NewSponsorID: u(1)}, {UserID: u(4), NewSponsorID: u(1)}},
		}, got)
		for _, r := range got.Relocated {
			pos, err := c.GetPosition(ctx, tree, r.UserID)
			require.NoError(t, err)
			require.NotNil(t, pos.ParentUserID)
			assert.Equal(t, r.ParentID, *pos.ParentUserID, "parent of %s", r.UserID)
			assert.Equal(t, r.Position, pos.Position, "position of %s", r.UserID)
			assert.Equal(t, r.Depth, int(pos.Depth), "depth of %s", r.UserID)
		}
	})

	t.Run("holding_tank", func(t *testing.T) {
		c := build(t)

		got, err := c.RemoveMatrixNode(ctx, tree, u(2), PruningHoldingTank)

		require.NoError(t, err)
		assert.Equal(t, &MatrixRemovalResult{
			Removed: u(2), Repositioned: []string{}, MovedToTank: []string{u(3), u(4), u(5), u(6), u(2)},
			Relocated: []Relocation{}, Responsored: []Responsored{},
		}, got)
	})
}
