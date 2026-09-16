package networkengine

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// failingTreeStore returns a fixed error from the depth-ordered read instead of
// failing the test when it is read.
type failingTreeStore struct {
	TreeStore
	err error
}

func (s *failingTreeStore) GetByTreeDepthOrdered(_ context.Context, _ string) ([]TreeNodeRow, error) {
	return nil, s.err
}

// failAtCallMutator succeeds for the first succeedFor engine calls and fails
// every call after that, so a fixture can stop the load on a chosen call.
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

// loadWithMutator seeds an in-memory store and runs a load against a
// caller-supplied mutator.
func loadWithMutator(t *testing.T, treeType string, nodes []TreeNodeRow, m TreeMutator, opts ...LoadTreeOption) error {
	t.Helper()
	store := NewMemoryTreeStore()
	ctx := context.Background()
	for _, n := range nodes {
		require.NoError(t, store.InsertNode(ctx, n))
	}
	return NewTreeLoader(store, m).LoadTree(ctx, "t", treeType, opts...)
}

// spacedNodes gives each node a distinct enrolment time.
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

func matrixOpts(width int) []LoadTreeOption {
	return []LoadTreeOption{WithMatrixParams(width, "breadth_first")}
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
		// direct seeds the store's rows in one go instead of inserting them one
		// by one, for fixtures an insert would reject before the load runs.
		direct bool
		// engineFailsAfter is how many engine calls to allow before failing.
		// Negative means the mutator never fails, which is what every preflight
		// row wants. Set on every row so the zero value never reads as
		// "fail the first call".
		engineFailsAfter int
		want             string
		// wantKind and wantStage name the type this exit returns once it is
		// converted. A row with neither set asserts the exit is still untyped,
		// so converting an exit without updating its row fails here. The empty
		// value is reserved as that sentinel and cannot name a real kind or
		// stage.
		wantKind    TreeLoadRejectionKind
		wantStage   TreeLoadStage
		wantNodeIDs []string
		// wantErrIs is the cause this exit must keep reachable. Rendering it
		// into the message is not enough.
		wantErrIs error
		// wantNoCause says this exit wraps nothing. A converted row sets it or
		// sets wantErrIs, so dropping a cause during conversion fails here.
		wantNoCause bool
	}{
		// validateTreeConfig
		{
			name:             "unsupported tree type",
			treeType:         "streamline",
			engineFailsAfter: -1,
			want:             `tree t has unsupported type "streamline"`,
			wantKind:         TreeLoadConfigInvalid,
			wantNoCause:      true,
		},
		{
			name:             "matrix without params",
			treeType:         treeTypeMatrix,
			engineFailsAfter: -1,
			want:             "tree t requires width and spillover (use WithMatrixParams)",
			wantKind:         TreeLoadConfigInvalid,
			wantNoCause:      true,
		},
		{
			name:             "matrix width below range",
			treeType:         treeTypeMatrix,
			opts:             []LoadTreeOption{WithMatrixParams(1, "breadth_first")},
			engineFailsAfter: -1,
			want:             "tree t has matrix width 1 outside the supported range 2..255",
			wantKind:         TreeLoadConfigInvalid,
			wantNoCause:      true,
		},
		{
			name:             "unsupported spillover",
			treeType:         treeTypeMatrix,
			opts:             []LoadTreeOption{WithMatrixParams(3, "sideways")},
			engineFailsAfter: -1,
			want:             `tree t has unsupported spillover "sideways"`,
			wantKind:         TreeLoadConfigInvalid,
			wantNoCause:      true,
		},

		// the store read
		{
			name:             "store read fails",
			treeType:         treeTypeUnilevel,
			store:            &failingTreeStore{TreeStore: NewMemoryTreeStore(), err: storeErr},
			engineFailsAfter: -1,
			want:             "load tree t: connection refused",
			wantErrIs:        storeErr,
			wantKind:         TreeLoadStoreReadFailed,
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
			nodes: spacedNodes([]TreeNodeRow{
				makeNode("t", "u0", 0, nil, nil, nil),
				makeNode("t", "u9", 0, nil, nil, nil),
			}),
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
			opts:     matrixOpts(3),
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
			opts:     matrixOpts(3),
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
			opts:     matrixOpts(3),
			nodes: spacedNodes([]TreeNodeRow{
				makeNode("t", "u0", 0, nil, nil, nil),
				makeNode("t", "u1", 1, ptr("u0"), ptr("u0"), intPtr(1)),
				makeNode("t", "u2", 1, ptr("u0"), ptr("u0"), intPtr(1)),
			}),
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
			opts:             matrixOpts(3),
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
			opts:             matrixOpts(3),
			nodes:            matrixFixture(),
			engineFailsAfter: 3,
			want:             "add node u2 (2 of 3, tree t left partly built): engine error [BOOM]: worker said no",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var err error
			var m *failAtCallMutator
			var storeRowMutator *stubMutator

			require.False(t, tt.direct && tt.engineFailsAfter >= 0,
				"a row cannot both seed past InsertNode and drive an engine failure")
			require.False(t, tt.store != nil && (tt.direct || tt.engineFailsAfter >= 0),
				"a row bringing its own store cannot also seed past InsertNode or drive an engine failure")
			if tt.wantKind != "" || tt.wantStage != "" {
				require.True(t, (tt.wantErrIs != nil) != tt.wantNoCause,
					"a converted row sets exactly one of wantErrIs and wantNoCause")
			}
			require.False(t, tt.wantKind != "" && tt.wantStage != "",
				"a row carries a kind or a stage, not both")

			switch {
			case tt.store != nil:
				storeRowMutator = &stubMutator{}
				err = NewTreeLoader(tt.store, storeRowMutator).LoadTree(
					context.Background(), "t", tt.treeType, tt.opts...)
			case tt.direct:
				_, err = loadWithStubDirect(t, tt.treeType, tt.nodes, tt.opts...)
			case tt.engineFailsAfter >= 0:
				m = &failAtCallMutator{
					succeedFor: tt.engineFailsAfter,
					err:        &EngineError{Code: "BOOM", Message: "worker said no"},
				}
				err = loadWithMutator(t, tt.treeType, tt.nodes, m, tt.opts...)
			default:
				_, err = loadWithStub(t, tt.treeType, tt.nodes, tt.opts...)
			}

			require.Error(t, err)
			assert.Equal(t, tt.want, err.Error())

			switch {
			case tt.wantErrIs != nil:
				assert.ErrorIs(t, err, tt.wantErrIs,
					"the cause must stay reachable, not just rendered")
			case tt.wantNoCause:
				assert.NoError(t, errors.Unwrap(err), "this exit wraps nothing")
			}

			var rejected *TreeLoadRejectedError
			var incomplete *TreeLoadIncompleteError
			switch {
			case tt.wantKind != "":
				require.ErrorAs(t, err, &rejected)
				assert.Equal(t, tt.wantKind, rejected.Kind)
				assert.Equal(t, "t", rejected.TreeID)
				assert.Equal(t, tt.wantNodeIDs, rejected.NodeIDs)
			case tt.wantStage != "":
				require.ErrorAs(t, err, &incomplete)
				assert.Equal(t, tt.wantStage, incomplete.Stage)
				assert.Equal(t, "t", incomplete.TreeID)
				assert.Equal(t, tt.wantNodeIDs, incomplete.NodeIDs)
			default:
				assert.False(t, errors.As(err, &rejected),
					"this exit is typed now; give the row a wantKind")
				assert.False(t, errors.As(err, &incomplete),
					"this exit is typed now; give the row a wantStage")
			}

			if storeRowMutator != nil {
				assert.Zero(t, storeRowMutator.totalCalls(),
					"a row bringing its own store must make no engine call")
			}

			if m != nil {
				assert.Equal(t, tt.engineFailsAfter+1, m.calls,
					"LoadTree must return on engine call %d and make no call after it",
					tt.engineFailsAfter+1)
			}
		})
	}
}

// TestTreeLoader_GoldenMessages_DirectCalls covers the three exits no fixture
// reaches through a full load. Each sits behind a check that runs earlier, so
// the only way to observe its message is to call the function that produces it.
//
// Two exits have no golden test at all: the nil-parent-or-sponsor and
// nil-position guards inside the replay loop. Validation rejects both
// conditions before the loop runs, so no fixture produces them.
func TestTreeLoader_GoldenMessages_DirectCalls(t *testing.T) {
	// assertStillUntyped is the same guard the table above puts in its default
	// branch. These three exits convert in later tasks, and without it a
	// conversion here leaves every assertion in this test satisfied.
	assertStillUntyped := func(t *testing.T, err error) {
		t.Helper()
		var rejected *TreeLoadRejectedError
		var incomplete *TreeLoadIncompleteError
		assert.False(t, errors.As(err, &rejected),
			"this exit is typed now; assert its kind instead of this guard")
		assert.False(t, errors.As(err, &incomplete),
			"this exit is typed now; assert its stage instead of this guard")
	}

	t.Run("tree type with no slot rule", func(t *testing.T) {
		nodes := []TreeNodeRow{makeNode("t", "u0", 0, nil, nil, nil)}

		err := validateNodes("t", "streamline", loadTreeConfig{}, nodes)

		require.Error(t, err)
		assert.Equal(t,
			`tree t has type "streamline" with no slot rule (add one to validateNodes)`,
			err.Error())
		assertStillUntyped(t, err)
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
		assertStillUntyped(t, err)
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
		assertStillUntyped(t, err)
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

func TestTreeLoadIncompleteError_CarriesProgress(t *testing.T) {
	engineErr := &EngineError{Code: "BOOM", Message: "worker said no"}
	err := newTreeLoadIncomplete(TreeLoadStageNodes, "t", engineErr, 3, 5,
		"add node u3 (3 of 5, tree t left partly built): engine error [BOOM]: worker said no",
		"u3")

	assert.Equal(t, TreeLoadStageNodes, err.Stage)
	assert.Equal(t, 2, err.Confirmed)
	assert.Equal(t, 3, err.Attempted)
	assert.Equal(t, 5, err.Total)

	var target *EngineError
	assert.True(t, errors.As(err, &target), "engine error stays reachable")
}

// This asserts the constructor, not the guards. Feeding a literal message in
// and reading the same literal back cannot catch a typo at the return site, so
// it is named for what it does. The two replay-loop guards have no message
// test because both conditions are rejected during validation, so no fixture
// reaches them.
func TestTreeLoadIncompleteError_ConstructorStoresWhatItIsGiven(t *testing.T) {
	nilRefs := newTreeLoadIncomplete(TreeLoadStageNodes, "t", nil, 3, 5,
		"node u3 in tree t has nil parent or sponsor (data corruption; 3 of 5, tree left partly built)",
		"u3")
	assert.Equal(t,
		"node u3 in tree t has nil parent or sponsor (data corruption; 3 of 5, tree left partly built)",
		nilRefs.Error())

	assert.Equal(t, TreeLoadStageNodes, nilRefs.Stage)
	assert.Equal(t, 2, nilRefs.Confirmed)
	assert.Equal(t, 3, nilRefs.Attempted)
	assert.Equal(t, 5, nilRefs.Total)
	assert.Equal(t, []string{"u3"}, nilRefs.NodeIDs)
	assert.Equal(t, "t", nilRefs.TreeID)
	assert.NoError(t, errors.Unwrap(nilRefs), "a guard wraps nothing")
}

func TestTreeLoadErrors_DoNotMatchEachOther(t *testing.T) {
	rejected := newTreeLoadRejected(TreeLoadConfigInvalid, "t", nil,
		"tree t has type unilevel with no slot rule (add one to validateNodes)")
	incomplete := newTreeLoadIncomplete(TreeLoadStageCreate, "t", nil, 0, 0,
		"create tree t: engine error [BOOM]: worker said no")

	var asIncomplete *TreeLoadIncompleteError
	assert.False(t, errors.As(rejected, &asIncomplete),
		"a rejection must not satisfy the incomplete type")

	var asRejected *TreeLoadRejectedError
	assert.False(t, errors.As(incomplete, &asRejected),
		"an incomplete load must not satisfy the rejection type")
}

func TestTreeLoadErrors_StayReachableThroughAnOuterWrap(t *testing.T) {
	rejected := newTreeLoadRejected(TreeLoadConfigInvalid, "t", nil,
		"tree t has an unsupported spillover")
	incomplete := newTreeLoadIncomplete(TreeLoadStageRoot, "t", nil, 0, 0,
		"add root u0 (tree t left partly built): engine error [BOOM]: worker said no",
		"u0")

	var asRejected *TreeLoadRejectedError
	require.True(t, errors.As(fmt.Errorf("start engine: %w", rejected), &asRejected))
	assert.Equal(t, TreeLoadConfigInvalid, asRejected.Kind)

	var asIncomplete *TreeLoadIncompleteError
	require.True(t, errors.As(fmt.Errorf("start engine: %w", incomplete), &asIncomplete))
	assert.Equal(t, TreeLoadStageRoot, asIncomplete.Stage)
	assert.Equal(t, []string{"u0"}, asIncomplete.NodeIDs)
}

func TestTreeLoadIncompleteError_CountsAreZeroBeforeTheNodesStage(t *testing.T) {
	create := newTreeLoadIncomplete(TreeLoadStageCreate, "t", nil, 0, 0,
		"create tree t: engine error [BOOM]: worker said no")

	assert.Equal(t, TreeLoadStageCreate, create.Stage)
	assert.Equal(t, 0, create.Confirmed, "a derived Confirmed must not go negative")
	assert.Equal(t, 0, create.Attempted)
	assert.Equal(t, 0, create.Total)
	assert.Empty(t, create.NodeIDs)
}

func TestTreeLoadErrors_DoNotAliasTheCallersSlice(t *testing.T) {
	ids := []string{"u0", "u9"}

	rejected := newTreeLoadRejected(TreeLoadDataInvalid, "t", nil,
		"tree t has more than one depth-0 root (u0 and u9)", ids...)
	incomplete := newTreeLoadIncomplete(TreeLoadStageNodes, "t", nil, 3, 5,
		"add node u0 (3 of 5, tree t left partly built): engine error", ids...)

	ids[0] = "mutated"

	assert.Equal(t, []string{"u0", "u9"}, rejected.NodeIDs)
	assert.Equal(t, []string{"u0", "u9"}, incomplete.NodeIDs)
}

// The golden table above reads an empty kind or stage as "this exit is not
// converted yet". A constant declared empty would make a converted exit read as
// unconverted, and the table would go green on it. The two lists are maintained
// by hand, so a new constant has to be added here to be covered.
func TestTreeLoadErrors_NoConstantCollidesWithTheSentinel(t *testing.T) {
	for _, k := range []TreeLoadRejectionKind{
		TreeLoadDataInvalid, TreeLoadConfigInvalid, TreeLoadStoreReadFailed,
	} {
		assert.NotEmpty(t, k, "an empty kind collides with the golden table's sentinel")
	}
	for _, stage := range []TreeLoadStage{
		TreeLoadStageCreate, TreeLoadStageRoot, TreeLoadStageNodes,
	} {
		assert.NotEmpty(t, stage, "an empty stage collides with the golden table's sentinel")
	}
}
