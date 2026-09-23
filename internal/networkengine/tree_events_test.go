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
