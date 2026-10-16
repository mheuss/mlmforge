package networkengine

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRootAddedPayload_RecordsTheTreeShape(t *testing.T) {
	width, spillover := 3, "breadth_first"
	at := time.Unix(0, 0).UTC()

	matrix, err := json.Marshal(RootAddedPayload{
		TreeID: "t", UserID: "u", SponsorID: "u", EnrolledAt: at,
		TreeType: "matrix", MatrixWidth: &width, MatrixSpillover: &spillover,
	})
	require.NoError(t, err)
	assert.JSONEq(t, `{"tree_id":"t","user_id":"u","sponsor_id":"u","enrolled_at":"1970-01-01T00:00:00Z",
		"tree_type":"matrix","matrix_width":3,"matrix_spillover":"breadth_first"}`, string(matrix))

	unilevel, err := json.Marshal(RootAddedPayload{
		TreeID: "t", UserID: "u", SponsorID: "u", EnrolledAt: at, TreeType: "unilevel",
	})
	require.NoError(t, err)
	assert.JSONEq(t, `{"tree_id":"t","user_id":"u","sponsor_id":"u","enrolled_at":"1970-01-01T00:00:00Z",
		"tree_type":"unilevel"}`, string(unilevel))
}

func TestNodeRemovedPayload_RecordsTheTreeTypeAndMode(t *testing.T) {
	at := time.Unix(0, 0).UTC()

	matrix, err := json.Marshal(NodeRemovedPayload{
		TreeID: "t", UserID: "u", RemovedAt: at, TreeType: "matrix", PruningMode: PruningHoldingTank,
	})
	require.NoError(t, err)
	assert.JSONEq(t, `{"tree_id":"t","user_id":"u","removed_at":"1970-01-01T00:00:00Z",
		"tree_type":"matrix","pruning_mode":"holding_tank"}`, string(matrix))

	unilevel, err := json.Marshal(NodeRemovedPayload{TreeID: "t", UserID: "u", RemovedAt: at, TreeType: "unilevel"})
	require.NoError(t, err)
	assert.JSONEq(t, `{"tree_id":"t","user_id":"u","removed_at":"1970-01-01T00:00:00Z",
		"tree_type":"unilevel"}`, string(unilevel))

	var old NodeRemovedPayload
	require.NoError(t, json.Unmarshal([]byte(`{"tree_id":"t","user_id":"u","removed_at":"1970-01-01T00:00:00Z"}`), &old))
	assert.Equal(t, NodeRemovedPayload{TreeID: "t", UserID: "u", RemovedAt: at}, old)
}

func TestCheckNodePlacedShape(t *testing.T) {
	pos := func(n int) *int { return &n }
	cases := []struct {
		name     string
		treeType string
		position *int
		want     string
	}{
		{"unilevel without a position", "unilevel", nil, ""},
		{"binary left", "binary", pos(0), ""},
		{"binary right", "binary", pos(1), ""},
		{"matrix at the ceiling", "matrix", pos(255), ""},
		{"unsupported type", "streamline", nil,
			`node_placed for u in tree t has unsupported tree_type "streamline"`},
		{"negative position", "binary", pos(-1),
			"node_placed for u in tree t has negative position -1"},
		{"matrix without a position", "matrix", nil,
			"matrix node_placed for u in tree t has no position; matrix events must carry explicit placement"},
		{"matrix above the ceiling", "matrix", pos(256),
			"matrix node_placed for u in tree t has position 256 above the 255 slot ceiling"},
		{"binary without a position", "binary", nil,
			"binary node_placed for u in tree t needs position 0 or 1"},
		{"binary past the right slot", "binary", pos(2),
			"binary node_placed for u in tree t needs position 0 or 1"},
		{"unilevel with a position", "unilevel", pos(0),
			"unilevel node_placed for u in tree t carries position 0; unilevel trees have no slots"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkNodePlacedShape(NodePlacedPayload{
				TreeID: "t", UserID: "u", TreeType: tc.treeType, Position: tc.position,
			})
			if tc.want == "" {
				assert.NoError(t, err)
				return
			}
			assert.EqualError(t, err, tc.want)
		})
	}
}

// Each row is a stream type, tree_type and mode. An empty expectation accepts.
// Any other is a phrase the error must contain.
func TestNodeRemovedShape_FollowsTheCompatibilityTable(t *testing.T) {
	const (
		modeOffMatrix = "only a matrix removal names a mode"
		noMode        = "has no pruning_mode"
		unknownMode   = `has unsupported pruning_mode "prune_all"`
		unknownType   = `has unsupported tree_type "ternary"`
		missingType   = "has no tree_type, and the stream records tree type matrix"
	)
	cases := []struct {
		name     string
		stream   string
		treeType string
		mode     PruningMode
		consumer string
		replay   string
	}{
		{"unilevel stream, no tree_type, no mode", treeTypeUnilevel, "", "", "", ""},
		{"binary stream, no tree_type, no mode", treeTypeBinary, "", "", "", ""},
		{"unilevel stream, tree_type unilevel, no mode", treeTypeUnilevel, treeTypeUnilevel, "", "", ""},
		{"binary stream, tree_type binary, no mode", treeTypeBinary, treeTypeBinary, "", "", ""},
		{"unilevel stream, no tree_type, a mode", treeTypeUnilevel, "", PruningPromoteEarliest, modeOffMatrix, modeOffMatrix},
		{"unilevel stream, tree_type unilevel, a mode", treeTypeUnilevel, treeTypeUnilevel, PruningHoldingTank, modeOffMatrix, modeOffMatrix},
		{"unilevel stream, tree_type binary", treeTypeUnilevel, treeTypeBinary, "", "",
			`has tree_type "binary", and the stream records tree type unilevel`},
		{"unilevel stream, tree_type binary, a mode", treeTypeUnilevel, treeTypeBinary, PruningPromoteEarliest,
			`carries pruning_mode "promote_earliest"`, `has tree_type "binary", and the stream records tree type unilevel`},
		{"unilevel stream, tree_type matrix, a mode", treeTypeUnilevel, treeTypeMatrix, PruningPromoteEarliest, "",
			`has tree_type "matrix", and the stream records tree type unilevel`},
		{"unilevel stream, tree_type matrix, no mode", treeTypeUnilevel, treeTypeMatrix, "", noMode,
			`has tree_type "matrix", and the stream records tree type unilevel`},
		{"matrix stream, promote_earliest", treeTypeMatrix, treeTypeMatrix, PruningPromoteEarliest, "", ""},
		{"matrix stream, holding_tank", treeTypeMatrix, treeTypeMatrix, PruningHoldingTank, "", ""},
		{"matrix stream, no mode", treeTypeMatrix, treeTypeMatrix, "", noMode, noMode},
		{"matrix stream, an unknown mode", treeTypeMatrix, treeTypeMatrix, "prune_all", unknownMode, unknownMode},
		{"matrix stream, no tree_type, no mode", treeTypeMatrix, "", "", "", missingType},
		{"matrix stream, no tree_type, a mode", treeTypeMatrix, "", PruningPromoteEarliest, modeOffMatrix, missingType},
		{"matrix stream, tree_type unilevel", treeTypeMatrix, treeTypeUnilevel, "", "",
			`has tree_type "unilevel", and the stream records tree type matrix`},
		{"matrix stream, tree_type unilevel, a mode", treeTypeMatrix, treeTypeUnilevel, PruningHoldingTank, modeOffMatrix,
			`has tree_type "unilevel", and the stream records tree type matrix`},
		{"unilevel stream, an unknown tree_type", treeTypeUnilevel, "ternary", "", unknownType, unknownType},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := NodeRemovedPayload{TreeID: writerTree, UserID: writerChild, RemovedAt: writeTime,
				TreeType: tc.treeType, PruningMode: tc.mode}

			assertShapeVerdict(t, checkNodeRemovedShape(p), tc.consumer)
			assertShapeVerdict(t, checkNodeRemovedOnStream(p, tc.stream), tc.replay)
		})
	}
}

// assertShapeVerdict passes a nil error when want is empty, and otherwise an
// error containing want.
func assertShapeVerdict(t *testing.T, err error, want string) {
	t.Helper()
	if want == "" {
		assert.NoError(t, err)
		return
	}
	assert.ErrorContains(t, err, want)
}
