package networkengine

import (
	"encoding/json"
	"testing"

	"github.com/mlmforge/mlmforge/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCanonicalID_GivesEquivalentSpellingsOneForm(t *testing.T) {
	lower, err := canonicalID("tree_id", "aaaaaaaa-aaaa-aaaa-aaaa-000000000001")
	require.NoError(t, err)
	upper, err := canonicalID("tree_id", "AAAAAAAA-AAAA-AAAA-AAAA-000000000001")
	require.NoError(t, err)

	assert.Equal(t, lower, upper)
	assert.Equal(t, "aaaaaaaa-aaaa-aaaa-aaaa-000000000001", upper.String())
}

func TestCanonicalID_NamesTheFieldItRefuses(t *testing.T) {
	_, err := canonicalID("parent_id", "not-a-uuid")

	require.ErrorContains(t, err, `parent_id "not-a-uuid" is not a UUID: `)
}

// firstEvent builds a version-1 event on stream tree-t.
func firstEvent(t *testing.T, eventType string, payload any) platform.Event {
	t.Helper()
	data, err := json.Marshal(payload)
	require.NoError(t, err)
	return platform.Event{ID: "e1", Stream: "tree-t", Type: eventType, Version: 1, Payload: data}
}

func TestReadTreeShape(t *testing.T) {
	width, narrow, spillover := 3, 1, "breadth_first"
	cases := []struct {
		name    string
		event   platform.Event
		want    treeShape
		wantErr string
	}{
		{"unilevel",
			firstEvent(t, EventTypeRootAdded, RootAddedPayload{TreeID: "t", TreeType: "unilevel"}),
			treeShape{treeType: "unilevel"}, ""},
		{"matrix",
			firstEvent(t, EventTypeRootAdded, RootAddedPayload{TreeID: "t", TreeType: "matrix", MatrixWidth: &width, MatrixSpillover: &spillover}),
			treeShape{treeType: "matrix", width: 3, spillover: "breadth_first"}, ""},
		{"another event type",
			firstEvent(t, EventTypeNodePlaced, NodePlacedPayload{TreeID: "t"}),
			treeShape{}, `stream tree-t holds "tree.node_placed" at version 1, not tree.root_added`},
		{"no tree_type",
			firstEvent(t, EventTypeRootAdded, RootAddedPayload{TreeID: "t"}),
			treeShape{}, "root_added at version 1 in stream tree-t has no tree_type"},
		{"an unsupported tree_type",
			firstEvent(t, EventTypeRootAdded, RootAddedPayload{TreeID: "t", TreeType: "streamline"}),
			treeShape{}, `root_added at version 1 in stream tree-t has unsupported tree_type "streamline"`},
		{"matrix without a width",
			firstEvent(t, EventTypeRootAdded, RootAddedPayload{TreeID: "t", TreeType: "matrix", MatrixSpillover: &spillover}),
			treeShape{}, "matrix root_added at version 1 in stream tree-t has no matrix_width"},
		{"matrix without a spillover",
			firstEvent(t, EventTypeRootAdded, RootAddedPayload{TreeID: "t", TreeType: "matrix", MatrixWidth: &width}),
			treeShape{}, "matrix root_added at version 1 in stream tree-t has no matrix_spillover"},
		{"another tree's root",
			firstEvent(t, EventTypeRootAdded, RootAddedPayload{TreeID: "u", TreeType: "unilevel"}),
			treeShape{}, `root_added at version 1 in stream tree-t names tree "u"`},
		{"a unilevel root with a width",
			firstEvent(t, EventTypeRootAdded, RootAddedPayload{TreeID: "t", TreeType: "unilevel", MatrixWidth: &width}),
			treeShape{}, "unilevel root_added at version 1 in stream tree-t carries a matrix width or spillover, which only a matrix root carries"},
		{"a binary root with a spillover",
			firstEvent(t, EventTypeRootAdded, RootAddedPayload{TreeID: "t", TreeType: "binary", MatrixSpillover: &spillover}),
			treeShape{}, "binary root_added at version 1 in stream tree-t carries a matrix width or spillover, which only a matrix root carries"},
		{"matrix with a width the engine refuses",
			firstEvent(t, EventTypeRootAdded, RootAddedPayload{TreeID: "t", TreeType: "matrix", MatrixWidth: &narrow, MatrixSpillover: &spillover}),
			treeShape{}, "root_added at version 1 in stream tree-t: tree t has matrix width 1 outside the supported range 2..255"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := readTreeShape("tree-t", tc.event)
			if tc.wantErr != "" {
				require.EqualError(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestReadTreeShape_RefusesAPayloadThatDoesNotDecode(t *testing.T) {
	event := platform.Event{Stream: "tree-t", Type: EventTypeRootAdded, Version: 1, Payload: json.RawMessage(`{"tree_type":`)}

	_, err := readTreeShape("tree-t", event)

	require.ErrorContains(t, err, "unmarshal root_added at version 1 in stream tree-t: ")
}

func TestShapeFromRequest(t *testing.T) {
	width, zero, spillover := 3, 0, "breadth_first"
	cases := []struct {
		name      string
		treeType  string
		width     *int
		spillover *string
		want      treeShape
		wantErr   string
	}{
		{"unilevel", "unilevel", nil, nil, treeShape{treeType: "unilevel"}, ""},
		{"matrix", "matrix", &width, &spillover, treeShape{treeType: "matrix", width: 3, spillover: "breadth_first"}, ""},
		{"unilevel with a width", "unilevel", &width, nil, treeShape{},
			`add root to tree t: matrix width and spillover apply only to matrix trees, and the request names "unilevel"`},
		{"binary with a spillover", "binary", nil, &spillover, treeShape{},
			`add root to tree t: matrix width and spillover apply only to matrix trees, and the request names "binary"`},
		{"matrix without a spillover", "matrix", &width, nil, treeShape{},
			"add root to tree t: a matrix tree needs a width and a spillover"},
		{"matrix without a width", "matrix", nil, &spillover, treeShape{},
			"add root to tree t: a matrix tree needs a width and a spillover"},
		{"matrix with a zero width", "matrix", &zero, &spillover, treeShape{},
			"tree t has matrix width 0 outside the supported range 2..255"},
		{"an unsupported type", "streamline", nil, nil, treeShape{},
			`tree t has unsupported type "streamline"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := shapeFromRequest("t", tc.treeType, tc.width, tc.spillover)
			if tc.wantErr != "" {
				require.EqualError(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
