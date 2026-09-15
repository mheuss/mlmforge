package networkengine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// failingTreeStore returns a fixed error from the depth-ordered read.
// rejectingTreeStore fails the test when read; this one lets the read happen
// and fails it.
type failingTreeStore struct {
	TreeStore
	err error
}

func (s *failingTreeStore) GetByTreeDepthOrdered(_ context.Context, _ string) ([]TreeNodeRow, error) {
	return nil, s.err
}

// failAtCallMutator succeeds for the first succeedFor engine calls and fails
// every call after that. stubMutator.failWith fails the first call, which
// cannot reach AddRoot or a mid-replay placement.
type failAtCallMutator struct {
	succeedFor int
	err        error
	calls      int
}

var _ TreeMutator = (*failAtCallMutator)(nil)

func (m *failAtCallMutator) next() error {
	m.calls++
	if m.calls > m.succeedFor {
		return m.err
	}
	return nil
}

func (m *failAtCallMutator) CreateTree(context.Context, string, string) error { return m.next() }

func (m *failAtCallMutator) CreateMatrixTree(context.Context, string, int, string) error {
	return m.next()
}

func (m *failAtCallMutator) AddRoot(context.Context, string, string, int64) error { return m.next() }

func (m *failAtCallMutator) AddNode(context.Context, string, string, string, string, int64, ...AddNodeOption) error {
	return m.next()
}

func (m *failAtCallMutator) AddNodeAt(context.Context, string, string, string, string, int, int64) error {
	return m.next()
}

func (m *failAtCallMutator) RemoveNode(context.Context, string, string) ([]Responsored, error) {
	return nil, m.next()
}

// loadWithMutator is loadWithStub with a caller-supplied mutator.
func loadWithMutator(t *testing.T, treeType string, nodes []TreeNodeRow, m TreeMutator, opts ...LoadTreeOption) error {
	t.Helper()
	store := NewMemoryTreeStore()
	ctx := context.Background()
	for _, n := range nodes {
		require.NoError(t, store.InsertNode(ctx, n))
	}
	return NewTreeLoader(store, m).LoadTree(ctx, "t", treeType, opts...)
}

// spacedNodes gives each node a distinct enrolment time, so replay order is
// fixed by the fixture rather than by how the store broke a tie.
func spacedNodes(nodes []TreeNodeRow) []TreeNodeRow {
	for i := range nodes {
		nodes[i].EnrolledAt = nodes[i].EnrolledAt.Add(time.Duration(i) * time.Minute)
	}
	return nodes
}

// unilevelFixture is a root plus four children of the root.
func unilevelFixture() []TreeNodeRow {
	nodes := []TreeNodeRow{makeNode("t", "u0", 0, nil, nil, nil)}
	for _, id := range []string{"u1", "u2", "u3", "u4"} {
		nodes = append(nodes, makeNode("t", id, 1, ptr("u0"), ptr("u0"), nil))
	}
	return spacedNodes(nodes)
}

// matrixFixture is a root plus three children in slots 0, 1 and 2 of a width-3
// matrix.
func matrixFixture() []TreeNodeRow {
	nodes := []TreeNodeRow{makeNode("t", "u0", 0, nil, nil, nil)}
	for i, id := range []string{"u1", "u2", "u3"} {
		nodes = append(nodes, makeNode("t", id, 1, ptr("u0"), ptr("u0"), intPtr(i)))
	}
	return spacedNodes(nodes)
}

func matrixOpts3() []LoadTreeOption {
	return []LoadTreeOption{WithMatrixParams(3, "breadth_first")}
}

// TestTreeLoader_GoldenMessages_ThroughLoadTree records the exact text of every
// exit a fixture can drive through LoadTree, using assert.Equal rather than
// assert.Contains. The existing loader tests assert substrings, which would
// accept a changed prefix or suffix.
func TestTreeLoader_GoldenMessages_ThroughLoadTree(t *testing.T) {
	storeErr := errors.New("connection refused")

	tests := []struct {
		name     string
		treeType string
		opts     []LoadTreeOption
		store    TreeStore
		nodes    []TreeNodeRow
		// direct seeds MemoryTreeStore's slice instead of calling InsertNode,
		// for fixtures the store's index mirrors refuse.
		direct bool
		// engineFailsAfter is how many engine calls to allow before failing.
		// Negative means the mutator never fails, which is what every preflight
		// row wants. Set on every row so the zero value never reads as
		// "fail the first call".
		engineFailsAfter int
		want             string
	}{
		// validateTreeConfig
		{
			name:             "unsupported tree type",
			treeType:         "streamline",
			engineFailsAfter: -1,
			want:             `tree t has unsupported type "streamline"`,
		},
		{
			name:             "matrix without params",
			treeType:         treeTypeMatrix,
			engineFailsAfter: -1,
			want:             "tree t requires width and spillover (use WithMatrixParams)",
		},
		{
			name:             "matrix width below range",
			treeType:         treeTypeMatrix,
			opts:             []LoadTreeOption{WithMatrixParams(1, "breadth_first")},
			engineFailsAfter: -1,
			want:             "tree t has matrix width 1 outside the supported range 2..255",
		},
		{
			name:             "unsupported spillover",
			treeType:         treeTypeMatrix,
			opts:             []LoadTreeOption{WithMatrixParams(3, "sideways")},
			engineFailsAfter: -1,
			want:             `tree t has unsupported spillover "sideways"`,
		},

		// the store read
		{
			name:             "store read fails",
			treeType:         treeTypeUnilevel,
			store:            &failingTreeStore{TreeStore: NewMemoryTreeStore(), err: storeErr},
			engineFailsAfter: -1,
			want:             "load tree t: connection refused",
		},

		// validateNodes
		{
			name:     "duplicate user",
			treeType: treeTypeUnilevel,
			nodes: []TreeNodeRow{
				makeNode("t", "u0", 0, nil, nil, nil),
				makeNode("t", "u0", 1, ptr("u0"), ptr("u0"), nil),
			},
			direct:           true,
			engineFailsAfter: -1,
			want:             "tree t has duplicate user u0 (data corruption?)",
		},
		{
			name:     "two roots",
			treeType: treeTypeUnilevel,
			nodes: []TreeNodeRow{
				makeNode("t", "u0", 0, nil, nil, nil),
				makeNode("t", "u9", 0, nil, nil, nil),
			},
			engineFailsAfter: -1,
			want:             "tree t has more than one depth-0 root (u0 and u9)",
		},
		{
			name:     "no root",
			treeType: treeTypeUnilevel,
			nodes: []TreeNodeRow{
				makeNode("t", "u1", 1, ptr("u0"), ptr("u0"), nil),
			},
			engineFailsAfter: -1,
			want:             "tree t has no depth-0 root node (data corruption?)",
		},
		{
			name:     "root carries a parent",
			treeType: treeTypeUnilevel,
			nodes: []TreeNodeRow{
				makeNode("t", "u0", 0, ptr("ghost"), nil, nil),
			},
			engineFailsAfter: -1,
			want:             "root u0 in tree t has parent ghost (the engine root has no parent)",
		},
		{
			name:     "root carries a position",
			treeType: treeTypeUnilevel,
			nodes: []TreeNodeRow{
				makeNode("t", "u0", 0, nil, nil, intPtr(0)),
			},
			engineFailsAfter: -1,
			want:             "root u0 in tree t has position 0 (the engine root occupies no slot)",
		},
		{
			name:     "nil parent",
			treeType: treeTypeUnilevel,
			nodes: []TreeNodeRow{
				makeNode("t", "u0", 0, nil, nil, nil),
				makeNode("t", "u1", 1, nil, ptr("u0"), nil),
			},
			engineFailsAfter: -1,
			want:             "node u1 in tree t has nil parent or sponsor (data corruption?)",
		},
		{
			name:     "nil sponsor",
			treeType: treeTypeUnilevel,
			nodes: []TreeNodeRow{
				makeNode("t", "u0", 0, nil, nil, nil),
				makeNode("t", "u1", 1, ptr("u0"), nil, nil),
			},
			engineFailsAfter: -1,
			want:             "node u1 in tree t has nil parent or sponsor (data corruption?)",
		},
		{
			name:     "self parent",
			treeType: treeTypeUnilevel,
			nodes: []TreeNodeRow{
				makeNode("t", "u0", 0, nil, nil, nil),
				makeNode("t", "u1", 1, ptr("u1"), ptr("u0"), nil),
			},
			engineFailsAfter: -1,
			want:             "node u1 in tree t is its own parent",
		},
		{
			name:     "self sponsor",
			treeType: treeTypeUnilevel,
			nodes: []TreeNodeRow{
				makeNode("t", "u0", 0, nil, nil, nil),
				makeNode("t", "u1", 1, ptr("u0"), ptr("u1"), nil),
			},
			engineFailsAfter: -1,
			want:             "node u1 in tree t is its own sponsor",
		},
		{
			name:     "parent not in tree",
			treeType: treeTypeUnilevel,
			nodes: []TreeNodeRow{
				makeNode("t", "u0", 0, nil, nil, nil),
				makeNode("t", "u1", 1, ptr("ghost"), ptr("u0"), nil),
			},
			engineFailsAfter: -1,
			want:             "node u1 in tree t references parent ghost that is not in the tree",
		},
		{
			name:     "sponsor not in tree",
			treeType: treeTypeUnilevel,
			nodes: []TreeNodeRow{
				makeNode("t", "u0", 0, nil, nil, nil),
				makeNode("t", "u1", 1, ptr("u0"), ptr("ghost"), nil),
			},
			engineFailsAfter: -1,
			want:             "node u1 in tree t references sponsor ghost that is not in the tree",
		},
		{
			name:     "depth mismatch",
			treeType: treeTypeUnilevel,
			nodes: []TreeNodeRow{
				makeNode("t", "u0", 0, nil, nil, nil),
				makeNode("t", "u1", 3, ptr("u0"), ptr("u0"), nil),
			},
			engineFailsAfter: -1,
			want:             "node u1 in tree t has depth 3 but parent u0 has depth 0",
		},
		{
			name:     "matrix nil position",
			treeType: treeTypeMatrix,
			opts:     matrixOpts3(),
			nodes: []TreeNodeRow{
				makeNode("t", "u0", 0, nil, nil, nil),
				makeNode("t", "u1", 1, ptr("u0"), ptr("u0"), nil),
			},
			engineFailsAfter: -1,
			want:             "matrix node u1 in tree t has nil position (the adjacency row is incomplete)",
		},
		{
			name:     "matrix position outside width",
			treeType: treeTypeMatrix,
			opts:     matrixOpts3(),
			nodes: []TreeNodeRow{
				makeNode("t", "u0", 0, nil, nil, nil),
				makeNode("t", "u1", 1, ptr("u0"), ptr("u0"), intPtr(3)),
			},
			engineFailsAfter: -1,
			want:             "matrix node u1 in tree t has position 3 outside the range 0..2",
		},
		{
			name:     "matrix duplicate slot",
			treeType: treeTypeMatrix,
			opts:     matrixOpts3(),
			nodes: []TreeNodeRow{
				makeNode("t", "u0", 0, nil, nil, nil),
				makeNode("t", "u1", 1, ptr("u0"), ptr("u0"), intPtr(1)),
				makeNode("t", "u2", 1, ptr("u0"), ptr("u0"), intPtr(1)),
			},
			direct:           true,
			engineFailsAfter: -1,
			want:             "matrix nodes u1 and u2 in tree t both claim parent u0 position 1",
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
			engineFailsAfter: -1,
			want:             "tree t: 2 of 3 nodes cannot be replayed because their parent/sponsor references form a cycle: u1 -> u2 -> u1",
		},

		// LoadTree, after the first engine call
		{
			name:             "CreateTree fails",
			treeType:         treeTypeUnilevel,
			nodes:            unilevelFixture(),
			engineFailsAfter: 0,
			want:             "create tree t: engine error [BOOM]: worker said no",
		},
		{
			name:             "CreateMatrixTree fails",
			treeType:         treeTypeMatrix,
			opts:             matrixOpts3(),
			nodes:            matrixFixture(),
			engineFailsAfter: 0,
			want:             "create tree t: engine error [BOOM]: worker said no",
		},
		{
			name:             "AddRoot fails",
			treeType:         treeTypeUnilevel,
			nodes:            unilevelFixture(),
			engineFailsAfter: 1,
			want:             "add root u0 (tree t created but left empty): engine error [BOOM]: worker said no",
		},
		{
			name:             "AddNode fails",
			treeType:         treeTypeUnilevel,
			nodes:            unilevelFixture(),
			engineFailsAfter: 4,
			want:             "add node u3 (3 of 4, tree t left partly built): engine error [BOOM]: worker said no",
		},
		{
			name:             "AddNodeAt fails",
			treeType:         treeTypeMatrix,
			opts:             matrixOpts3(),
			nodes:            matrixFixture(),
			engineFailsAfter: 3,
			want:             "add node u2 (2 of 3, tree t left partly built): engine error [BOOM]: worker said no",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var err error
			var m *failAtCallMutator

			switch {
			case tt.store != nil:
				err = NewTreeLoader(tt.store, &stubMutator{}).LoadTree(
					context.Background(), "t", tt.treeType, tt.opts...)
			case tt.engineFailsAfter >= 0:
				m = &failAtCallMutator{
					succeedFor: tt.engineFailsAfter,
					err:        &EngineError{Code: "BOOM", Message: "worker said no"},
				}
				err = loadWithMutator(t, tt.treeType, tt.nodes, m, tt.opts...)
			case tt.direct:
				_, err = loadWithStubDirect(t, tt.treeType, tt.nodes, tt.opts...)
			default:
				_, err = loadWithStub(t, tt.treeType, tt.nodes, tt.opts...)
			}

			require.Error(t, err)
			assert.Equal(t, tt.want, err.Error())

			if m != nil {
				assert.Equal(t, tt.engineFailsAfter+1, m.calls,
					"the fixture must stop on the intended engine call, not an earlier or later one")
			}
		})
	}
}

// TestTreeLoader_GoldenMessages_DirectCalls covers the three exits no fixture
// reaches through LoadTree. validateTreeConfig rejects an unsupported type
// before the slot-rule arm runs, and cycleError's two degenerate branches need
// a precondition validateNodes refuses.
//
// Two exits have no golden test at all: the nil-parent-or-sponsor and
// nil-position guards inside LoadTree's replay loop. validateNodes proves both
// conditions impossible before the loop runs, so no fixture produces them.
func TestTreeLoader_GoldenMessages_DirectCalls(t *testing.T) {
	t.Run("tree type with no slot rule", func(t *testing.T) {
		nodes := []TreeNodeRow{makeNode("t", "u0", 0, nil, nil, nil)}

		err := validateNodes("t", "streamline", loadTreeConfig{}, nodes)

		require.Error(t, err)
		assert.Equal(t,
			`tree t has type "streamline" with no slot rule (add one to validateNodes)`,
			err.Error())
	})

	// Fixture copied from TestOrderForReplay_StalledWalkNamesWhereItStopped.
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
	})

	// Fixture copied from TestOrderForReplay_DuplicateUserIDsDoNotPanic.
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
	})
}
