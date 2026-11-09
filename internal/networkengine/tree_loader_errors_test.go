package networkengine

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// spacedNodes gives each node a distinct enrollment time.
func spacedNodes(nodes []TreeNodeRow) []TreeNodeRow {
	for i := range nodes {
		nodes[i].EnrolledAt = nodes[i].EnrolledAt.Add(time.Duration(i) * time.Minute)
	}
	return nodes
}

// TestPreflight_GoldenMessages records the exact text of every preflight exit a
// fixture can reach, using assert.Equal rather than assert.Contains.
func TestPreflight_GoldenMessages(t *testing.T) {
	tests := []struct {
		name     string
		treeType string
		cfg      loadTreeConfig
		nodes    []TreeNodeRow
		// direct seeds the store's rows in one go instead of inserting them
		// one by one.
		direct      bool
		want        string
		wantKind    TreeLoadRejectionKind
		wantNodeIDs []string
	}{
		// validateTreeConfig
		{
			name:     "unsupported tree type",
			treeType: "streamline",
			want:     `tree t has unsupported type "streamline"`,
			wantKind: TreeLoadConfigInvalid,
		},
		{
			name:     "matrix without params",
			treeType: treeTypeMatrix,
			want:     "tree t is a matrix tree, and its configuration does not mark a width and spillover as set",
			wantKind: TreeLoadConfigInvalid,
		},
		{
			name:     "matrix width below range",
			treeType: treeTypeMatrix,
			cfg:      matrixConfig(1, "breadth_first"),
			want:     "tree t has matrix width 1 outside the supported range 2..255",
			wantKind: TreeLoadConfigInvalid,
		},
		{
			name:     "unsupported spillover",
			treeType: treeTypeMatrix,
			cfg:      matrixConfig(3, "sideways"),
			want:     `tree t has unsupported spillover "sideways"`,
			wantKind: TreeLoadConfigInvalid,
		},

		// validateNodes
		{
			name:     "duplicate user",
			treeType: treeTypeUnilevel,
			nodes: []TreeNodeRow{
				makeNode("t", "u0", 0, nil, nil, nil),
				makeNode("t", "u0", 1, ptr("u0"), ptr("u0"), nil),
			},
			direct:      true,
			want:        "tree t has duplicate user u0 (data corruption?)",
			wantKind:    TreeLoadDataInvalid,
			wantNodeIDs: []string{"u0"},
		},
		{
			name:     "empty user ID",
			treeType: treeTypeUnilevel,
			nodes: []TreeNodeRow{
				makeNode("t", "u0", 0, nil, nil, nil),
				makeNode("t", "", 1, ptr("u0"), ptr("u0"), nil),
			},
			want:     `tree t has a row with an empty user ID (row ID "id-")`,
			wantKind: TreeLoadDataInvalid,
		},
		{
			name:     "nil user ID",
			treeType: treeTypeUnilevel,
			nodes: []TreeNodeRow{
				makeNode("t", "u0", 0, nil, nil, nil),
				makeNode("t", uuid.Nil.String(), 1, ptr("u0"), ptr("u0"), nil),
			},
			want:        "tree t has a row with the nil UUID as its user ID (row ID \"id-" + uuid.Nil.String() + "\")",
			wantKind:    TreeLoadDataInvalid,
			wantNodeIDs: []string{uuid.Nil.String()},
		},
		{
			// The empty row and b form a sponsor cycle.
			name:     "empty user ID inside a sponsor cycle",
			treeType: treeTypeUnilevel,
			nodes: []TreeNodeRow{
				makeNode("t", "u0", 0, nil, ptr("u0"), nil),
				makeNode("t", "", 1, ptr("u0"), ptr("b"), nil),
				makeNode("t", "b", 1, ptr("u0"), ptr(""), nil),
			},
			want:     `tree t has a row with an empty user ID (row ID "id-")`,
			wantKind: TreeLoadDataInvalid,
		},
		{
			name:     "two roots",
			treeType: treeTypeUnilevel,
			nodes: spacedNodes([]TreeNodeRow{
				makeNode("t", "u0", 0, nil, nil, nil),
				makeNode("t", "u9", 0, nil, nil, nil),
			}),
			// see HEU-810
			direct:      true,
			want:        "tree t has more than one depth-0 root (u0 and u9)",
			wantKind:    TreeLoadDataInvalid,
			wantNodeIDs: []string{"u0", "u9"},
		},
		{
			name:     "no root",
			treeType: treeTypeUnilevel,
			nodes: []TreeNodeRow{
				makeNode("t", "u1", 1, ptr("u0"), ptr("u0"), nil),
			},
			want:     "tree t has no depth-0 root node (data corruption?)",
			wantKind: TreeLoadDataInvalid,
		},
		{
			name:     "root carries a parent",
			treeType: treeTypeUnilevel,
			nodes: []TreeNodeRow{
				makeNode("t", "u0", 0, ptr("ghost"), nil, nil),
			},
			want:        "root u0 in tree t has parent ghost (the engine root has no parent)",
			wantKind:    TreeLoadDataInvalid,
			wantNodeIDs: []string{"u0", "ghost"},
		},
		{
			name:     "root carries a position",
			treeType: treeTypeUnilevel,
			nodes: []TreeNodeRow{
				makeNode("t", "u0", 0, nil, nil, intPtr(0)),
			},
			want:        "root u0 in tree t has position 0 (the engine root occupies no slot)",
			wantKind:    TreeLoadDataInvalid,
			wantNodeIDs: []string{"u0"},
		},
		{
			name:        "nil parent",
			wantKind:    TreeLoadDataInvalid,
			wantNodeIDs: []string{"u1"},
			treeType:    treeTypeUnilevel,
			nodes: []TreeNodeRow{
				makeNode("t", "u0", 0, nil, nil, nil),
				makeNode("t", "u1", 1, nil, ptr("u0"), nil),
			},
			want: "node u1 in tree t has nil parent or sponsor (data corruption?)",
		},
		{
			name:        "nil sponsor",
			wantKind:    TreeLoadDataInvalid,
			wantNodeIDs: []string{"u1"},
			treeType:    treeTypeUnilevel,
			nodes: []TreeNodeRow{
				makeNode("t", "u0", 0, nil, nil, nil),
				makeNode("t", "u1", 1, ptr("u0"), nil, nil),
			},
			want: "node u1 in tree t has nil parent or sponsor (data corruption?)",
		},
		{
			name:     "self parent",
			treeType: treeTypeUnilevel,
			nodes: []TreeNodeRow{
				makeNode("t", "u0", 0, nil, nil, nil),
				makeNode("t", "u1", 1, ptr("u1"), ptr("u0"), nil),
			},
			want:        "node u1 in tree t is its own parent",
			wantKind:    TreeLoadDataInvalid,
			wantNodeIDs: []string{"u1"},
		},
		{
			name:     "self sponsor",
			treeType: treeTypeUnilevel,
			nodes: []TreeNodeRow{
				makeNode("t", "u0", 0, nil, nil, nil),
				makeNode("t", "u1", 1, ptr("u0"), ptr("u1"), nil),
			},
			want:        "node u1 in tree t is its own sponsor",
			wantKind:    TreeLoadDataInvalid,
			wantNodeIDs: []string{"u1"},
		},
		{
			name:     "parent not in tree",
			treeType: treeTypeUnilevel,
			nodes: []TreeNodeRow{
				makeNode("t", "u0", 0, nil, nil, nil),
				makeNode("t", "u1", 1, ptr("ghost"), ptr("u0"), nil),
			},
			want:        "node u1 in tree t references parent ghost that is not in the tree",
			wantKind:    TreeLoadDataInvalid,
			wantNodeIDs: []string{"u1", "ghost"},
		},
		{
			name:     "sponsor not in tree",
			treeType: treeTypeUnilevel,
			nodes: []TreeNodeRow{
				makeNode("t", "u0", 0, nil, nil, nil),
				makeNode("t", "u1", 1, ptr("u0"), ptr("ghost"), nil),
			},
			want:        "node u1 in tree t references sponsor ghost that is not in the tree",
			wantKind:    TreeLoadDataInvalid,
			wantNodeIDs: []string{"u1", "ghost"},
		},
		{
			name:     "depth mismatch",
			treeType: treeTypeUnilevel,
			nodes: []TreeNodeRow{
				makeNode("t", "u0", 0, nil, nil, nil),
				makeNode("t", "u1", 3, ptr("u0"), ptr("u0"), nil),
			},
			want:        "node u1 in tree t has depth 3 but parent u0 has depth 0",
			wantKind:    TreeLoadDataInvalid,
			wantNodeIDs: []string{"u1", "u0"},
		},
		{
			name:     "unilevel position",
			treeType: treeTypeUnilevel,
			nodes: []TreeNodeRow{
				makeNode("t", "u0", 0, nil, nil, nil),
				makeNode("t", "u1", 1, ptr("u0"), ptr("u0"), intPtr(7)),
			},
			want:        "unilevel node u1 in tree t has position 7, expected none",
			wantKind:    TreeLoadDataInvalid,
			wantNodeIDs: []string{"u1"},
		},
		{
			name:     "matrix nil position",
			treeType: treeTypeMatrix,
			cfg:      matrixConfig(3, "breadth_first"),
			nodes: []TreeNodeRow{
				makeNode("t", "u0", 0, nil, nil, nil),
				makeNode("t", "u1", 1, ptr("u0"), ptr("u0"), nil),
			},
			want:        "matrix node u1 in tree t has nil position (the adjacency row is incomplete)",
			wantKind:    TreeLoadDataInvalid,
			wantNodeIDs: []string{"u1"},
		},
		{
			name:     "matrix position outside width",
			treeType: treeTypeMatrix,
			cfg:      matrixConfig(3, "breadth_first"),
			nodes: []TreeNodeRow{
				makeNode("t", "u0", 0, nil, nil, nil),
				makeNode("t", "u1", 1, ptr("u0"), ptr("u0"), intPtr(3)),
			},
			want:        "matrix node u1 in tree t has position 3 outside the range 0..2",
			wantKind:    TreeLoadDataInvalid,
			wantNodeIDs: []string{"u1"},
		},
		{
			name:     "matrix duplicate slot",
			treeType: treeTypeMatrix,
			cfg:      matrixConfig(3, "breadth_first"),
			nodes: spacedNodes([]TreeNodeRow{
				makeNode("t", "u0", 0, nil, nil, nil),
				makeNode("t", "u1", 1, ptr("u0"), ptr("u0"), intPtr(1)),
				makeNode("t", "u2", 1, ptr("u0"), ptr("u0"), intPtr(1)),
			}),
			direct:      true,
			want:        "matrix nodes u1 and u2 in tree t both claim parent u0 position 1",
			wantKind:    TreeLoadDataInvalid,
			wantNodeIDs: []string{"u1", "u2", "u0"},
		},

		// cycleError, the reachable branch
		{
			name:     "sponsor cycle",
			treeType: treeTypeUnilevel,
			nodes: []TreeNodeRow{
				makeNode("t", "u0", 0, nil, nil, nil),
				makeNode("t", "u1", 1, ptr("u0"), ptr("u2"), nil),
				makeNode("t", "u2", 1, ptr("u0"), ptr("u1"), nil),
			},
			want:        "tree t: 2 of 3 nodes cannot be replayed because their parent/sponsor references form a cycle: u1 -> u2 -> u1",
			wantKind:    TreeLoadDataInvalid,
			wantNodeIDs: []string{"u1", "u2", "u1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			run := preflightRows
			if tt.direct {
				run = preflightRowsDirect
			}
			err := run(t, tt.treeType, tt.cfg, tt.nodes)

			require.Error(t, err)
			assert.Equal(t, tt.want, err.Error())
			assert.NoError(t, errors.Unwrap(err), "this exit wraps nothing")
			var rejected *TreeLoadRejectedError
			require.ErrorAs(t, err, &rejected)
			assert.Equal(t, tt.wantKind, rejected.Kind)
			assert.Equal(t, "t", rejected.TreeID)
			assert.Equal(t, tt.wantNodeIDs, rejected.NodeIDs)
		})
	}
}

// TestPreflight_GoldenMessages_DirectCalls covers the exits no fixture reaches
// through a full load. Each sits behind a check that runs earlier, so the only
// way to observe its message is to call the function that produces it.
func TestPreflight_GoldenMessages_DirectCalls(t *testing.T) {
	t.Run("tree type with no slot rule", func(t *testing.T) {
		nodes := []TreeNodeRow{makeNode("t", "u0", 0, nil, nil, nil)}

		err := validateNodes("t", "streamline", loadTreeConfig{}, nodes)

		require.Error(t, err)
		assert.Equal(t,
			`tree t has type "streamline" with no slot rule (add one to validateNodes)`,
			err.Error())

		var rejected *TreeLoadRejectedError
		require.ErrorAs(t, err, &rejected)
		assert.Equal(t, TreeLoadConfigInvalid, rejected.Kind,
			"a missing slot rule tells a developer to write code, not an operator to fix data")
		assert.Empty(t, rejected.NodeIDs)
		assert.NoError(t, errors.Unwrap(err))
	})

	t.Run("walk stalls without closing a loop", func(t *testing.T) {
		nodes := []TreeNodeRow{
			makeNode("t", "u0", 0, nil, ptr("u0"), nil),
			makeNode("t", "z", 1, ptr("ghost"), ptr("ghost"), nil),
			makeNode("t", "z", 1, ptr("u0"), ptr("u0"), nil),
			makeNode("t", "a", 2, ptr("z"), ptr("z"), nil),
		}

		_, err := orderForReplay("t", nodes)

		require.Error(t, err)
		assert.Equal(t,
			"tree t: 3 of 4 nodes cannot be replayed (the replay order stops at z, reached from a, whose parent or sponsor cannot be resolved)",
			err.Error())

		var rejected *TreeLoadRejectedError
		require.ErrorAs(t, err, &rejected)
		assert.Equal(t, TreeLoadDataInvalid, rejected.Kind)
		assert.Equal(t, []string{"z", "a"}, rejected.NodeIDs,
			"the node the walk stopped at, then whoever reached it")
		assert.NoError(t, errors.Unwrap(err))
	})

	t.Run("every node emitted but the counts disagree", func(t *testing.T) {
		nodes := []TreeNodeRow{
			makeNode("t", "u0", 0, nil, ptr("u0"), nil),
			makeNode("t", "u1", 1, ptr("u0"), ptr("u0"), nil),
			makeNode("t", "u1", 1, ptr("u0"), ptr("u0"), nil),
		}

		_, err := orderForReplay("t", nodes)

		require.Error(t, err)
		assert.Equal(t,
			"tree t: replay produced 2 of 3 nodes with no unreplayable node (duplicate user IDs?)",
			err.Error())

		var rejected *TreeLoadRejectedError
		require.ErrorAs(t, err, &rejected)
		assert.Equal(t, TreeLoadDataInvalid, rejected.Kind)
		assert.Empty(t, rejected.NodeIDs, "this message names no user")
		assert.NoError(t, errors.Unwrap(err))
	})
}

func TestTreeLoadRejectedError_CarriesFieldsAndMessage(t *testing.T) {
	storeErr := errors.New("connection refused")
	err := newTreeLoadRejected(TreeLoadStoreReadFailed, "t", storeErr,
		"load tree t: connection refused")

	assert.Equal(t, "load tree t: connection refused", err.Error())
	assert.Equal(t, TreeLoadStoreReadFailed, err.Kind)
	assert.Equal(t, "t", err.TreeID)
	assert.Empty(t, err.NodeIDs)
	assert.True(t, errors.Is(err, storeErr), "store error stays reachable")
}

func TestTreeLoadRejectedError_CarriesEveryNamedNode(t *testing.T) {
	err := newTreeLoadRejected(TreeLoadDataInvalid, "t", nil,
		"tree t has more than one depth-0 root (u0 and u9)", "u0", "u9")

	assert.Equal(t, []string{"u0", "u9"}, err.NodeIDs)
	assert.NoError(t, errors.Unwrap(err))
}

func TestTreeLoadErrors_StayReachableThroughAnOuterWrap(t *testing.T) {
	rejected := newTreeLoadRejected(TreeLoadConfigInvalid, "t", nil,
		"tree t has an unsupported spillover")

	var asRejected *TreeLoadRejectedError
	require.True(t, errors.As(fmt.Errorf("start engine: %w", rejected), &asRejected))
	assert.Equal(t, TreeLoadConfigInvalid, asRejected.Kind)
}

func TestTreeLoadErrors_DoNotAliasTheCallersSlice(t *testing.T) {
	ids := []string{"u0", "u9"}

	rejected := newTreeLoadRejected(TreeLoadDataInvalid, "t", nil,
		"tree t has more than one depth-0 root (u0 and u9)", ids...)

	ids[0] = "mutated"

	assert.Equal(t, []string{"u0", "u9"}, rejected.NodeIDs)
}

// Every kind constant is non-empty. The list is maintained by hand, so a new
// constant has to be added here to be covered.
func TestTreeLoadErrors_NoConstantCollidesWithTheSentinel(t *testing.T) {
	for _, k := range []TreeLoadRejectionKind{
		TreeLoadDataInvalid, TreeLoadConfigInvalid, TreeLoadStoreReadFailed,
	} {
		assert.NotEmpty(t, k, "kind constant is empty")
	}
}

// A row with an empty user ID sorts ahead of every other stuck node, and the
// walk stops before it starts. The message then names nobody, so NodeIDs must
// name nobody either.
func TestCycleError_EmptyUserIDNamesNoNode(t *testing.T) {
	nodes := []TreeNodeRow{
		makeNode("t", "u0", 0, nil, ptr("u0"), nil),
		makeNode("t", "", 1, ptr("ghost"), ptr("ghost"), nil),
	}

	_, err := orderForReplay("t", nodes)

	var rejected *TreeLoadRejectedError
	require.ErrorAs(t, err, &rejected)
	assert.Equal(t,
		"tree t: 1 of 2 nodes cannot be replayed (the replay order stops at , whose parent or sponsor cannot be resolved)",
		err.Error(),
		"pins which exit this covers; the no-unreplayable-node exit also returns empty NodeIDs")
	assert.Empty(t, rejected.NodeIDs,
		"a message naming no user must carry no user")
}
