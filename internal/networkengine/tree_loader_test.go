package networkengine

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// orderRecordingTransport records ops in order for verification.
type orderRecordingTransport struct {
	ops      []string
	response json.RawMessage
}

func newOrderRecordingTransport() *orderRecordingTransport {
	return &orderRecordingTransport{
		response: json.RawMessage(`{"ok":true}`),
	}
}

func (o *orderRecordingTransport) Call(_ context.Context, op string, _ json.RawMessage) (json.RawMessage, error) {
	o.ops = append(o.ops, op)
	return o.response, nil
}

func (o *orderRecordingTransport) Close() error { return nil }

func TestTreeLoader_LoadEmptyTree(t *testing.T) {
	store := NewMemoryTreeStore()
	transport := newOrderRecordingTransport()
	engine := NewEngineClientWithTransport(transport)
	loader := NewTreeLoader(store, engine)

	err := loader.LoadTree(context.Background(), "tree-1", "unilevel")
	require.NoError(t, err)
	assert.Empty(t, transport.ops, "no engine calls for empty tree")
}

func TestTreeLoader_LoadSingleRoot(t *testing.T) {
	store := NewMemoryTreeStore()
	root := makeNode("tree-1", "root-user", 0, nil, ptr("root-user"), nil)
	root.EnrolledAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, store.InsertNode(context.Background(), root))

	transport := newOrderRecordingTransport()
	engine := NewEngineClientWithTransport(transport)
	loader := NewTreeLoader(store, engine)

	err := loader.LoadTree(context.Background(), "tree-1", "unilevel")
	require.NoError(t, err)

	require.Len(t, transport.ops, 2)
	assert.Equal(t, "create_tree", transport.ops[0])
	assert.Equal(t, "add_root", transport.ops[1])
}

func TestTreeLoader_LoadChain(t *testing.T) {
	store := NewMemoryTreeStore()
	ctx := context.Background()
	enrolled := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	root := makeNode("tree-1", "user-0", 0, nil, ptr("user-0"), nil)
	root.EnrolledAt = enrolled
	require.NoError(t, store.InsertNode(ctx, root))

	node1 := makeNode("tree-1", "user-1", 1, ptr("user-0"), ptr("user-0"), nil)
	node1.EnrolledAt = enrolled.Add(time.Hour)
	require.NoError(t, store.InsertNode(ctx, node1))

	node2 := makeNode("tree-1", "user-2", 2, ptr("user-1"), ptr("user-1"), nil)
	node2.EnrolledAt = enrolled.Add(2 * time.Hour)
	require.NoError(t, store.InsertNode(ctx, node2))

	node3 := makeNode("tree-1", "user-3", 3, ptr("user-2"), ptr("user-2"), nil)
	node3.EnrolledAt = enrolled.Add(3 * time.Hour)
	require.NoError(t, store.InsertNode(ctx, node3))

	transport := newOrderRecordingTransport()
	engine := NewEngineClientWithTransport(transport)
	loader := NewTreeLoader(store, engine)

	err := loader.LoadTree(ctx, "tree-1", "unilevel")
	require.NoError(t, err)

	// create_tree + add_root + 3 add_node = 5 calls.
	require.Len(t, transport.ops, 5)
	assert.Equal(t, "create_tree", transport.ops[0])
	assert.Equal(t, "add_root", transport.ops[1])
	assert.Equal(t, "add_node", transport.ops[2])
	assert.Equal(t, "add_node", transport.ops[3])
	assert.Equal(t, "add_node", transport.ops[4])
}

func TestTreeLoader_SkipsRemovedNodes(t *testing.T) {
	store := NewMemoryTreeStore()
	ctx := context.Background()
	enrolled := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	root := makeNode("tree-1", "user-0", 0, nil, ptr("user-0"), nil)
	root.EnrolledAt = enrolled
	require.NoError(t, store.InsertNode(ctx, root))

	child := makeNode("tree-1", "user-1", 1, ptr("user-0"), ptr("user-0"), nil)
	child.EnrolledAt = enrolled.Add(time.Hour)
	require.NoError(t, store.InsertNode(ctx, child))

	// Soft-delete the child.
	require.NoError(t, store.DeleteNode(ctx, "tree-1", "user-1"))

	transport := newOrderRecordingTransport()
	engine := NewEngineClientWithTransport(transport)
	loader := NewTreeLoader(store, engine)

	err := loader.LoadTree(ctx, "tree-1", "unilevel")
	require.NoError(t, err)

	// Only root should be loaded: create_tree + add_root.
	require.Len(t, transport.ops, 2)
	assert.Equal(t, "create_tree", transport.ops[0])
	assert.Equal(t, "add_root", transport.ops[1])
}

func TestTreeLoader_NoDepthZeroRoot(t *testing.T) {
	store := NewMemoryTreeStore()
	ctx := context.Background()

	// Insert a single node with depth 1 — invalid for a root.
	node := makeNode("tree-1", "user-0", 1, nil, ptr("user-0"), nil)
	require.NoError(t, store.InsertNode(ctx, node))

	transport := newOrderRecordingTransport()
	engine := NewEngineClientWithTransport(transport)
	loader := NewTreeLoader(store, engine)

	err := loader.LoadTree(ctx, "tree-1", "unilevel")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "has no depth-0 root node")

	// Preflight runs before the engine is touched, so nothing was created.
	assert.Empty(t, transport.ops, "validation failure must make no engine calls")
}

func TestTreeLoader_ChildWithNilParent(t *testing.T) {
	store := NewMemoryTreeStore()
	ctx := context.Background()
	enrolled := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// Valid root.
	root := makeNode("tree-1", "user-0", 0, nil, ptr("user-0"), nil)
	root.EnrolledAt = enrolled
	require.NoError(t, store.InsertNode(ctx, root))

	// Child with nil ParentID and nil SponsorID — data corruption.
	child := makeNode("tree-1", "user-1", 1, nil, nil, nil)
	child.EnrolledAt = enrolled.Add(time.Hour)
	require.NoError(t, store.InsertNode(ctx, child))

	transport := newOrderRecordingTransport()
	engine := NewEngineClientWithTransport(transport)
	loader := NewTreeLoader(store, engine)

	err := loader.LoadTree(ctx, "tree-1", "unilevel")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "has nil parent or sponsor")
	assert.Empty(t, transport.ops, "validation failure must make no engine calls")
}

func TestTreeLoader_LoadMatrixTree_RootOnlyRoutesToCreateMatrixTree(t *testing.T) {
	store := NewMemoryTreeStore()
	ctx := context.Background()

	root := makeNode("m-tree", "user-0", 0, nil, ptr("user-0"), nil)
	root.EnrolledAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, store.InsertNode(ctx, root))

	mutator := &stubMutator{}
	loader := NewTreeLoader(store, mutator)

	err := loader.LoadTree(ctx, "m-tree", "matrix", WithMatrixParams(3, "breadth_first"))
	require.NoError(t, err)

	// A matrix tree must be created via CreateMatrixTree carrying width and
	// spillover. Plain CreateTree omits them and the engine rejects it.
	require.Equal(t, []matrixCreate{{structure: "m-tree", width: 3, spillover: "breadth_first"}}, mutator.matrixCreated)
	assert.Empty(t, mutator.created, "matrix load must not call plain CreateTree")
	assert.Equal(t, []string{"user-0"}, mutator.roots)
	assert.Empty(t, mutator.nodes)
}

func TestTreeLoader_LoadMatrixTree_MultiNodeReplaysExplicitPlacement(t *testing.T) {
	store := NewMemoryTreeStore()
	ctx := context.Background()
	enrolled := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	root := makeNode("m-tree", "user-0", 0, nil, ptr("user-0"), nil)
	root.EnrolledAt = enrolled
	require.NoError(t, store.InsertNode(ctx, root))

	// user-1 at slot 2. Spillover would have chosen slot 0.
	child := makeNode("m-tree", "user-1", 1, ptr("user-0"), ptr("user-0"), intPtr(2))
	child.EnrolledAt = enrolled.Add(time.Hour)
	require.NoError(t, store.InsertNode(ctx, child))

	// user-2 sits under user-1 but is sponsored by user-0, so parent and
	// sponsor differ. Without this the test cannot detect a transposition,
	// because identical values hide the swap.
	grandchild := makeNode("m-tree", "user-2", 2, ptr("user-1"), ptr("user-0"), intPtr(1))
	grandchild.EnrolledAt = enrolled.Add(2 * time.Hour)
	require.NoError(t, store.InsertNode(ctx, grandchild))

	mutator := &stubMutator{}
	loader := NewTreeLoader(store, mutator)

	err := loader.LoadTree(ctx, "m-tree", "matrix", WithMatrixParams(3, "breadth_first"))
	require.NoError(t, err)

	require.Equal(t, []matrixCreate{{structure: "m-tree", width: 3, spillover: "breadth_first"}}, mutator.matrixCreated)
	assert.Equal(t, []string{"user-0"}, mutator.roots)
	assert.Empty(t, mutator.nodes, "matrix nodes must not go through AddNode")
	assert.Equal(t, []nodeAtCall{
		{userID: "user-1", parentID: "user-0", sponsorID: "user-0", position: 2},
		{userID: "user-2", parentID: "user-1", sponsorID: "user-0", position: 1},
	}, mutator.nodesAt)
}

// TestTreeLoader_ReplayOrderIsDeterministic covers BR-6 at the level the
// requirement states it: repeated loads produce an identical engine call
// sequence, including arguments. TestOrderForReplay_IsIndependentOfInputOrder
// pins the helper; this pins the loader, including the root split and the
// matrix dispatch.
//
// The three children share a depth and an enrollment time, so the store's
// sort has no stable tiebreak and returns them in insertion order. Only the
// loader's own ordering makes the result deterministic.
func TestTreeLoader_ReplayOrderIsDeterministic(t *testing.T) {
	enrolled := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	root := makeNode("t", "u0", 0, nil, ptr("u0"), nil)
	root.EnrolledAt = enrolled

	child := func(id string, pos int) TreeNodeRow {
		n := makeNode("t", id, 1, ptr("u0"), ptr("u0"), intPtr(pos))
		n.EnrolledAt = enrolled.Add(time.Hour)
		return n
	}

	insertionOrders := [][]TreeNodeRow{
		{root, child("uc", 2), child("ua", 0), child("ub", 1)},
		{root, child("ua", 0), child("ub", 1), child("uc", 2)},
		{root, child("ub", 1), child("uc", 2), child("ua", 0)},
	}

	// The concrete sequence, not just "all runs agree". Comparing runs against
	// each other alone would still pass if the ordering rule changed wholesale.
	want := []nodeAtCall{
		{userID: "ua", parentID: "u0", sponsorID: "u0", position: 0},
		{userID: "ub", parentID: "u0", sponsorID: "u0", position: 1},
		{userID: "uc", parentID: "u0", sponsorID: "u0", position: 2},
	}
	for i, nodes := range insertionOrders {
		mutator, err := loadWithStub(t, "matrix", nodes, WithMatrixParams(3, "breadth_first"))
		require.NoError(t, err, "insertion order %d", i)
		require.Equal(t, []string{"u0"}, mutator.roots, "insertion order %d", i)
		assert.Equal(t, want, mutator.nodesAt,
			"insertion order %d produced a different call sequence", i)
	}
}

// TestTreeLoader_MatrixSponsorDeeperThanNode is the matrix counterpart to
// TestTreeLoader_BinaryTreeLoadsUnchanged: it exercises explicit-placement
// dispatch and dependency-aware reordering together. That combination is the
// whole point of HEU-534 — an admin override can put a node shallower than its
// own sponsor, which is exactly what depth-ordered replay cannot restore — and
// no other matrix test covers it, because every other matrix fixture has
// sponsors at or above their nodes.
func TestTreeLoader_MatrixSponsorDeeperThanNode(t *testing.T) {
	enrolled := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	root := makeNode("t", "u0", 0, nil, ptr("u0"), intPtr(0))
	root.Position = nil
	root.EnrolledAt = enrolled

	u1 := makeNode("t", "u1", 1, ptr("u0"), ptr("u0"), intPtr(0))
	u1.EnrolledAt = enrolled.Add(time.Hour)

	// Sponsored by u2, which sits a level deeper. Depth order would replay this
	// first and the engine would reject an unknown sponsor.
	u3 := makeNode("t", "u3", 1, ptr("u0"), ptr("u2"), intPtr(2))
	u3.EnrolledAt = enrolled.Add(2 * time.Hour)

	u2 := makeNode("t", "u2", 2, ptr("u1"), ptr("u1"), intPtr(1))
	u2.EnrolledAt = enrolled.Add(3 * time.Hour)

	mutator, err := loadWithStub(t, "matrix", []TreeNodeRow{root, u1, u3, u2},
		WithMatrixParams(3, "breadth_first"))
	require.NoError(t, err)

	assert.Empty(t, mutator.nodes, "matrix must not route through AddNode")
	// u2 precedes u3 because it sponsors it, even though u3 is shallower.
	assert.Equal(t, []nodeAtCall{
		{userID: "u1", parentID: "u0", sponsorID: "u0", position: 0},
		{userID: "u2", parentID: "u1", sponsorID: "u1", position: 1},
		{userID: "u3", parentID: "u0", sponsorID: "u2", position: 2},
	}, mutator.nodesAt)
}

// loadWithStub seeds an in-memory store and runs LoadTree against a stub
// mutator, so a caller can assert both the error and that nothing reached the
// engine. Every node must carry TreeID "t".
func loadWithStub(t *testing.T, treeType string, nodes []TreeNodeRow, opts ...LoadTreeOption) (*stubMutator, error) {
	t.Helper()
	store := NewMemoryTreeStore()
	ctx := context.Background()
	for _, n := range nodes {
		require.NoError(t, store.InsertNode(ctx, n))
	}
	mutator := &stubMutator{}
	err := NewTreeLoader(store, mutator).LoadTree(ctx, "t", treeType, opts...)
	return mutator, err
}

func TestTreeLoader_ValidationFailures_Structural(t *testing.T) {
	matrixOpts := []LoadTreeOption{WithMatrixParams(3, "breadth_first")}

	tests := []struct {
		name     string
		treeType string
		opts     []LoadTreeOption
		nodes    []TreeNodeRow
		wantErr  string
	}{
		{
			name:     "unsupported tree type",
			treeType: "streamline",
			nodes:    []TreeNodeRow{makeNode("t", "u0", 0, nil, ptr("u0"), nil)},
			wantErr:  `unsupported type "streamline"`,
		},
		{
			name:     "matrix width below 2",
			treeType: "matrix",
			opts:     []LoadTreeOption{WithMatrixParams(1, "breadth_first")},
			nodes:    []TreeNodeRow{makeNode("t", "u0", 0, nil, ptr("u0"), nil)},
			wantErr:  "matrix width 1 outside the supported range 2..255",
		},
		{
			name:     "matrix width above 255",
			treeType: "matrix",
			opts:     []LoadTreeOption{WithMatrixParams(256, "breadth_first")},
			nodes:    []TreeNodeRow{makeNode("t", "u0", 0, nil, ptr("u0"), nil)},
			wantErr:  "matrix width 256 outside the supported range 2..255",
		},
		{
			name:     "unsupported spillover",
			treeType: "matrix",
			opts:     []LoadTreeOption{WithMatrixParams(3, "depth_first")},
			nodes:    []TreeNodeRow{makeNode("t", "u0", 0, nil, ptr("u0"), nil)},
			wantErr:  `unsupported spillover "depth_first"`,
		},
		{
			name:     "no root",
			treeType: "unilevel",
			nodes:    []TreeNodeRow{makeNode("t", "u1", 1, ptr("u0"), ptr("u0"), nil)},
			wantErr:  "no depth-0 root node",
		},
		{
			name:     "two roots",
			treeType: "unilevel",
			nodes: []TreeNodeRow{
				makeNode("t", "u0", 0, nil, ptr("u0"), nil),
				makeNode("t", "u1", 0, nil, ptr("u1"), nil),
			},
			wantErr: "more than one depth-0 root",
		},
		{
			name:     "root carries a parent",
			treeType: "unilevel",
			nodes:    []TreeNodeRow{makeNode("t", "u0", 0, ptr("ghost"), ptr("u0"), nil)},
			wantErr:  "root u0 in tree t has parent ghost",
		},
		{
			name:     "root carries a position",
			treeType: "matrix",
			opts:     matrixOpts,
			nodes:    []TreeNodeRow{makeNode("t", "u0", 0, nil, ptr("u0"), intPtr(0))},
			wantErr:  "root u0 in tree t has position 0",
		},
		{
			name:     "non-root is its own parent",
			treeType: "unilevel",
			nodes: []TreeNodeRow{
				makeNode("t", "u0", 0, nil, ptr("u0"), nil),
				makeNode("t", "u1", 1, ptr("u1"), ptr("u0"), nil),
			},
			wantErr: "node u1 in tree t is its own parent",
		},
		{
			name:     "non-root is its own sponsor",
			treeType: "unilevel",
			nodes: []TreeNodeRow{
				makeNode("t", "u0", 0, nil, ptr("u0"), nil),
				makeNode("t", "u1", 1, ptr("u0"), ptr("u1"), nil),
			},
			wantErr: "node u1 in tree t is its own sponsor",
		},
		{
			name:     "nil parent",
			treeType: "unilevel",
			nodes: []TreeNodeRow{
				makeNode("t", "u0", 0, nil, ptr("u0"), nil),
				makeNode("t", "u1", 1, nil, ptr("u0"), nil),
			},
			wantErr: "nil parent or sponsor",
		},
		{
			// Same combined check as "nil parent", opposite side. Both
			// branches of `ParentID == nil || SponsorID == nil` need cover.
			name:     "nil sponsor",
			treeType: "unilevel",
			nodes: []TreeNodeRow{
				makeNode("t", "u0", 0, nil, ptr("u0"), nil),
				makeNode("t", "u1", 1, ptr("u0"), nil, nil),
			},
			wantErr: "nil parent or sponsor",
		},
		{
			name:     "parent not in tree",
			treeType: "unilevel",
			nodes: []TreeNodeRow{
				makeNode("t", "u0", 0, nil, ptr("u0"), nil),
				makeNode("t", "u1", 1, ptr("ghost"), ptr("u0"), nil),
			},
			wantErr: "references parent ghost that is not in the tree",
		},
		{
			name:     "sponsor not in tree",
			treeType: "unilevel",
			nodes: []TreeNodeRow{
				makeNode("t", "u0", 0, nil, ptr("u0"), nil),
				makeNode("t", "u1", 1, ptr("u0"), ptr("ghost"), nil),
			},
			wantErr: "references sponsor ghost that is not in the tree",
		},
		{
			name:     "depth disagrees with parent",
			treeType: "unilevel",
			nodes: []TreeNodeRow{
				makeNode("t", "u0", 0, nil, ptr("u0"), nil),
				makeNode("t", "u1", 3, ptr("u0"), ptr("u0"), nil),
			},
			wantErr: "has depth 3 but parent u0 has depth 0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mutator, err := loadWithStub(t, tt.treeType, tt.nodes, tt.opts...)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
			assert.Zero(t, mutator.totalCalls(), "preflight failure must make no engine calls")
		})
	}
}

// TestValidateNodes_RootSponsorIsExempt pins the deliberate exemption in
// validateNodes: the root's sponsor is not validated at all. AddRoot takes no
// sponsor and the engine root is sponsor: None, so whatever a root row stores
// is projection metadata, never a replay input.
//
// The claim under test is "exempt means unvalidated," not "these particular
// sponsor values are good data." That distinction is why the last case is
// here. The first three shapes all survive a hoisted sponsor-existence check
// — nil short-circuits on the nil guard, and the other two resolve in byID —
// so only a sponsor that is absent from the tree can catch that regression.
// Drop it and the test passes while the exemption is gone.
//
// Task 5 asserts the ordering half of the exemption; this is the validation
// half.
func TestValidateNodes_RootSponsorIsExempt(t *testing.T) {
	tests := []struct {
		name        string
		rootSponsor *string
	}{
		{name: "nil sponsor", rootSponsor: nil},
		{name: "self sponsor", rootSponsor: ptr("u0")},
		{name: "another node in the tree", rootSponsor: ptr("u1")},
		{name: "a user absent from the tree", rootSponsor: ptr("ghost")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nodes := []TreeNodeRow{
				makeNode("t", "u0", 0, nil, tt.rootSponsor, nil),
				makeNode("t", "u1", 1, ptr("u0"), ptr("u0"), nil),
			}

			require.NoError(t, validateNodes("t", "unilevel", loadTreeConfig{}, nodes))
		})
	}
}

// TestValidateNodes_DuplicateUserID is the one validation case that cannot be
// driven through LoadTree: MemoryTreeStore.InsertNode rejects a duplicate
// active user ID (tree_store_memory.go:25-31), mirroring the Postgres partial
// unique index, so the fixture is unconstructable through the store.
func TestValidateNodes_DuplicateUserID(t *testing.T) {
	nodes := []TreeNodeRow{
		makeNode("t", "u0", 0, nil, ptr("u0"), nil),
		makeNode("t", "u0", 1, ptr("u0"), ptr("u0"), nil),
	}

	err := validateNodes("t", "unilevel", loadTreeConfig{}, nodes)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate user u0")
}

func TestTreeLoader_ValidationFailures_Positions(t *testing.T) {
	matrixOpts := []LoadTreeOption{WithMatrixParams(3, "breadth_first")}

	tests := []struct {
		name     string
		treeType string
		opts     []LoadTreeOption
		nodes    []TreeNodeRow
		wantErr  string
	}{
		{
			name:     "matrix nil position",
			treeType: "matrix",
			opts:     matrixOpts,
			nodes: []TreeNodeRow{
				makeNode("t", "u0", 0, nil, ptr("u0"), nil),
				makeNode("t", "u1", 1, ptr("u0"), ptr("u0"), nil),
			},
			wantErr: "matrix node u1 in tree t has nil position",
		},
		{
			name:     "matrix negative position",
			treeType: "matrix",
			opts:     matrixOpts,
			nodes: []TreeNodeRow{
				makeNode("t", "u0", 0, nil, ptr("u0"), nil),
				makeNode("t", "u1", 1, ptr("u0"), ptr("u0"), intPtr(-1)),
			},
			wantErr: "position -1 outside the range 0..2",
		},
		{
			name:     "matrix position equals width",
			treeType: "matrix",
			opts:     matrixOpts,
			nodes: []TreeNodeRow{
				makeNode("t", "u0", 0, nil, ptr("u0"), nil),
				makeNode("t", "u1", 1, ptr("u0"), ptr("u0"), intPtr(3)),
			},
			wantErr: "position 3 outside the range 0..2",
		},
		{
			name:     "matrix duplicate slot",
			treeType: "matrix",
			opts:     matrixOpts,
			nodes: []TreeNodeRow{
				makeNode("t", "u0", 0, nil, ptr("u0"), nil),
				makeNode("t", "u1", 1, ptr("u0"), ptr("u0"), intPtr(1)),
				makeNode("t", "u2", 1, ptr("u0"), ptr("u0"), intPtr(1)),
			},
			wantErr: "both claim parent u0 position 1",
		},
		{
			name:     "binary nil position",
			treeType: "binary",
			nodes: []TreeNodeRow{
				makeNode("t", "u0", 0, nil, ptr("u0"), nil),
				makeNode("t", "u1", 1, ptr("u0"), ptr("u0"), nil),
			},
			wantErr: "binary node u1 in tree t has nil position",
		},
		{
			name:     "binary position outside 0..1",
			treeType: "binary",
			nodes: []TreeNodeRow{
				makeNode("t", "u0", 0, nil, ptr("u0"), nil),
				makeNode("t", "u1", 1, ptr("u0"), ptr("u0"), intPtr(2)),
			},
			wantErr: "position 2 outside the range 0..1",
		},
		{
			name:     "binary duplicate slot",
			treeType: "binary",
			nodes: []TreeNodeRow{
				makeNode("t", "u0", 0, nil, ptr("u0"), nil),
				makeNode("t", "u1", 1, ptr("u0"), ptr("u0"), intPtr(0)),
				makeNode("t", "u2", 1, ptr("u0"), ptr("u0"), intPtr(0)),
			},
			wantErr: "both claim parent u0 position 0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mutator, err := loadWithStub(t, tt.treeType, tt.nodes, tt.opts...)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
			assert.Zero(t, mutator.totalCalls(), "preflight failure must make no engine calls")
		})
	}
}

// TestTreeLoader_ValidPositionsAccepted drives through LoadTree rather than
// calling validateNodes directly. It used to do the latter, because the
// multi-node matrix guard would have refused this three-node fixture before
// validation ran — that guard is gone, so the bypass is no longer needed and
// the test can assert the placements actually reached the engine.
func TestTreeLoader_ValidPositionsAccepted(t *testing.T) {
	// Same position under different parents is fine, and width-1 is in range.
	nodes := []TreeNodeRow{
		makeNode("t", "u0", 0, nil, ptr("u0"), nil),
		makeNode("t", "u1", 1, ptr("u0"), ptr("u0"), intPtr(2)),
		makeNode("t", "u2", 2, ptr("u1"), ptr("u1"), intPtr(2)),
	}

	mutator, err := loadWithStub(t, "matrix", nodes, WithMatrixParams(3, "breadth_first"))
	require.NoError(t, err)

	assert.Equal(t, []string{"u0"}, mutator.roots)
	assert.Equal(t, []nodeAtCall{
		{userID: "u1", parentID: "u0", sponsorID: "u0", position: 2},
		{userID: "u2", parentID: "u1", sponsorID: "u1", position: 2},
	}, mutator.nodesAt)
}

// TestTreeLoader_MatrixParamsIgnoredForNonMatrix pins the documented contract
// that WithMatrixParams is inert for other tree types. A width and spillover
// that would be rejected outright for a matrix must not affect a binary load.
func TestTreeLoader_MatrixParamsIgnoredForNonMatrix(t *testing.T) {
	nodes := []TreeNodeRow{
		makeNode("t", "u0", 0, nil, ptr("u0"), nil),
		makeNode("t", "u1", 1, ptr("u0"), ptr("u0"), intPtr(1)),
	}

	mutator, err := loadWithStub(t, "binary", nodes, WithMatrixParams(0, "depth_first"))
	require.NoError(t, err)

	// Width 0 must not leak into the binary slot limit: position 1 is only
	// valid because binary hardcodes 2.
	assert.Equal(t, []nodeAddCall{
		{userID: "u1", parentID: "u0", sponsorID: "u0", position: intPtr(1)},
	}, mutator.nodeCalls)

	// The create path is unaffected too — a matrix option must not reroute a
	// binary tree through CreateMatrixTree.
	assert.Equal(t, []string{"t"}, mutator.created)
	assert.Empty(t, mutator.matrixCreated)
}

// TestValidateNodes_UnilevelPositionIsTolerated pins current behavior, which is
// not obviously correct: a unilevel node carrying a position is accepted, and
// LoadTree then forwards that position to the worker, whose unilevel add_node
// never reads it. So the value is silently dropped.
//
// validateNodes rejects a *root* that carries a position, on the grounds that
// the engine root occupies no slot. A unilevel node occupies no slot either, so
// the two cases are treated inconsistently. The likeliest cause of such a row
// is a binary or matrix tree being loaded under the wrong treeType, which would
// rebuild a structurally different tree from the same nodes and go unnoticed.
//
// Tracked in HEU-563. This test exists so the tolerance is a recorded decision
// rather than an oversight; if HEU-563 lands, replace it with a rejection test.
func TestValidateNodes_UnilevelPositionIsTolerated(t *testing.T) {
	nodes := []TreeNodeRow{
		makeNode("t", "u0", 0, nil, ptr("u0"), nil),
		makeNode("t", "u1", 1, ptr("u0"), ptr("u0"), intPtr(7)),
	}

	require.NoError(t, validateNodes("t", "unilevel", loadTreeConfig{}, nodes))
}

// userIDs extracts the replay order for readable assertions.
func userIDs(nodes []*TreeNodeRow) []string {
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, n.UserID)
	}
	return out
}

func TestOrderForReplay_LeavesSpilloverShapedTreesAlone(t *testing.T) {
	// Sponsor always equals parent, which is what automatic spillover produces.
	// Depth order already satisfies both constraints, so nothing should move.
	nodes := []TreeNodeRow{
		makeNode("t", "u0", 0, nil, ptr("u0"), nil),
		makeNode("t", "u1", 1, ptr("u0"), ptr("u0"), nil),
		makeNode("t", "u2", 2, ptr("u1"), ptr("u1"), nil),
	}

	ordered, err := orderForReplay("t", nodes)
	require.NoError(t, err)
	assert.Equal(t, []string{"u0", "u1", "u2"}, userIDs(ordered))
}

func TestOrderForReplay_PlacesSponsorBeforeSponsored(t *testing.T) {
	enrolled := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// u3 sits at depth 2 but sponsors u2, which sits at depth 1. Depth order
	// would replay u2 first and the engine would reject an unknown sponsor.
	root := makeNode("t", "u0", 0, nil, ptr("u0"), nil)
	root.EnrolledAt = enrolled

	u1 := makeNode("t", "u1", 1, ptr("u0"), ptr("u0"), nil)
	u1.EnrolledAt = enrolled.Add(time.Hour)

	u2 := makeNode("t", "u2", 1, ptr("u0"), ptr("u3"), nil)
	u2.EnrolledAt = enrolled.Add(2 * time.Hour)

	u3 := makeNode("t", "u3", 2, ptr("u1"), ptr("u1"), nil)
	u3.EnrolledAt = enrolled.Add(3 * time.Hour)

	ordered, err := orderForReplay("t", []TreeNodeRow{root, u1, u2, u3})
	require.NoError(t, err)

	got := userIDs(ordered)
	pos := make(map[string]int, len(got))
	for i, id := range got {
		pos[id] = i
	}
	assert.Less(t, pos["u3"], pos["u2"], "sponsor u3 must precede sponsored u2")
	assert.Less(t, pos["u1"], pos["u3"], "parent u1 must precede child u3")
	assert.Equal(t, "u0", got[0], "root replays first")
}

func TestOrderForReplay_IsIndependentOfInputOrder(t *testing.T) {
	enrolled := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	root := makeNode("t", "u0", 0, nil, ptr("u0"), nil)
	root.EnrolledAt = enrolled

	// Three siblings enrolled at the same instant: only the user ID breaks the
	// tie. Feeding different permutations must produce one stable order.
	sib := func(id string) TreeNodeRow {
		n := makeNode("t", id, 1, ptr("u0"), ptr("u0"), nil)
		n.EnrolledAt = enrolled.Add(time.Hour)
		return n
	}
	a, b, c := sib("ua"), sib("ub"), sib("uc")

	permutations := [][]TreeNodeRow{
		{root, a, b, c},
		{root, c, b, a},
		{root, b, a, c},
		{root, c, a, b},
	}

	want := []string{"u0", "ua", "ub", "uc"}
	for i, perm := range permutations {
		ordered, err := orderForReplay("t", perm)
		require.NoError(t, err, "permutation %d", i)
		assert.Equal(t, want, userIDs(ordered), "permutation %d", i)
	}
}

func TestTreeLoader_CyclicReferences(t *testing.T) {
	// u1 and u2 sponsor each other. Parents are valid, so only the sponsor
	// edges form the cycle and nothing can be replayed first.
	nodes := []TreeNodeRow{
		makeNode("t", "u0", 0, nil, ptr("u0"), nil),
		makeNode("t", "u1", 1, ptr("u0"), ptr("u2"), nil),
		makeNode("t", "u2", 1, ptr("u0"), ptr("u1"), nil),
	}

	mutator, err := loadWithStub(t, "unilevel", nodes)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "form a cycle")
	// BR-4: the error must name the offending nodes, not just count them.
	// Asserting the rendered cycle rather than a substring of a node list,
	// because "u1, u2" would also match a message that wrongly included u0.
	assert.Contains(t, err.Error(), "u1 -> u2 -> u1")
	assert.Contains(t, err.Error(), "2 of 3 nodes", "the root is replayable and must not be counted")
	assert.Zero(t, mutator.totalCalls(), "cycle must be detected before any engine call")
}

// TestOrderForReplay_CycleErrorNamesCycleNotBystanders is the case that
// exposed the original defect. A cycle blocks its whole subtree, so the stuck
// set is mostly innocent nodes. Reporting a sorted prefix of that set named
// twenty bystanders and neither culprit, which is precisely what BR-4 forbids.
//
// The bystanders here sort ahead of the cycle members on purpose: "a00".."a29"
// precede "z1"/"z2", so any implementation that sorts the stuck set and prints
// a prefix fails this test.
func TestOrderForReplay_CycleErrorNamesCycleNotBystanders(t *testing.T) {
	nodes := []TreeNodeRow{
		makeNode("t", "u0", 0, nil, ptr("u0"), nil),
		// z1 and z2 sponsor each other. Neither can ever replay.
		makeNode("t", "z1", 1, ptr("u0"), ptr("z2"), nil),
		makeNode("t", "z2", 1, ptr("u0"), ptr("z1"), nil),
	}
	// Thirty innocent nodes parented under z1, blocked only because z1 is.
	for i := 0; i < 30; i++ {
		id := fmt.Sprintf("a%02d", i)
		nodes = append(nodes, makeNode("t", id, 2, ptr("z1"), ptr("z1"), nil))
	}

	_, err := orderForReplay("t", nodes)
	require.Error(t, err)

	assert.Contains(t, err.Error(), "z1", "the error must name a real cycle member")
	assert.Contains(t, err.Error(), "z2", "the error must name a real cycle member")
	assert.Contains(t, err.Error(), "->", "the cycle must be rendered as a path")
	assert.Contains(t, err.Error(), "32 of 33 nodes", "the stuck count is the blast radius")

	// The bystanders are counted but must not be listed — naming thirty
	// blocked nodes buries the two that actually need fixing.
	assert.NotContains(t, err.Error(), "a00")
	assert.NotContains(t, err.Error(), "a29")
}

// TestOrderForReplay_UnresolvableReferenceNamesTheReferrer covers the branch
// taken when the walk runs out of unmet dependencies instead of closing a
// loop. That is a dangling reference, not a cycle, and it needs its own test:
// the cycle branch above already had one, this branch did not, and a rewrite
// silently reintroduced an unbounded bystander list here that the cycle test
// could not see.
//
// Exactly one node carries the bad reference; the 25 "a" nodes are bystanders
// blocked behind it. That split is the point. An earlier version of this test
// pointed all 25 at the missing user, which made every stuck node a legitimate
// referrer — so it could not distinguish "names the referrer" from "lists the
// stuck set", and a truncated list passed it.
//
// The bystanders sort ahead of z0, so asserting NotContains on the *first*
// stuck node catches any prefix, even a one-element one.
func TestOrderForReplay_UnresolvableReferenceNamesTheReferrer(t *testing.T) {
	nodes := []TreeNodeRow{
		makeNode("t", "u0", 0, nil, ptr("u0"), nil),
		makeNode("t", "z0", 1, ptr("ghost"), ptr("ghost"), nil),
	}
	for i := 0; i < 25; i++ {
		id := fmt.Sprintf("a%02d", i)
		nodes = append(nodes, makeNode("t", id, 2, ptr("z0"), ptr("z0"), nil))
	}

	_, err := orderForReplay("t", nodes)
	require.Error(t, err)

	assert.Contains(t, err.Error(), "ghost", "the unresolvable target must be named")
	assert.Contains(t, err.Error(), "z0", "the node that references it must be named")
	assert.Contains(t, err.Error(), "26 of 27 nodes", "the stuck count is the blast radius")

	// a00 is the alphabetically first stuck node, so this fails on any prefix
	// of the stuck set, however short.
	assert.NotContains(t, err.Error(), "a00", "bystanders are counted, never listed")
	assert.NotContains(t, err.Error(), "form a cycle", "a dangling reference is not a cycle")
}

// TestOrderForReplay_StalledWalkNamesWhereItStopped covers the same exit taken
// for the other reason: duplicate rows whose copies carry different references,
// so byID resolves the target while one copy's edge stays unmet. The single
// fallback message has to be true here too — an earlier split version claimed
// this node was "not in the tree" when it plainly was.
func TestOrderForReplay_StalledWalkNamesWhereItStopped(t *testing.T) {
	nodes := []TreeNodeRow{
		makeNode("t", "u0", 0, nil, ptr("u0"), nil),
		makeNode("t", "z", 1, ptr("ghost"), ptr("ghost"), nil),
		// Same user ID, different references. byID keeps this copy.
		makeNode("t", "z", 1, ptr("u0"), ptr("u0"), nil),
		makeNode("t", "a", 2, ptr("z"), ptr("z"), nil),
	}

	require.NotPanics(t, func() {
		_, err := orderForReplay("t", nodes)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "z", "must name where the walk stopped")
		assert.NotContains(t, err.Error(), "is not in the tree",
			"z resolves in byID; claiming it is absent would be false")
	})
}

// TestOrderForReplay_DuplicateUserIDsDoNotPanic covers the other new branch.
// ordered counts rows while the emitted set counts IDs, so duplicate rows make
// the length check fail with an empty stuck set. Indexing stuck[0] there
// panicked, and a panic at startup takes down the process rather than failing
// one tree load.
//
// validateNodes rejects duplicates, so LoadTree cannot reach this — the guard
// exists because the invariant spans two functions and four tests call
// orderForReplay directly.
func TestOrderForReplay_DuplicateUserIDsDoNotPanic(t *testing.T) {
	nodes := []TreeNodeRow{
		makeNode("t", "u0", 0, nil, ptr("u0"), nil),
		makeNode("t", "u1", 1, ptr("u0"), ptr("u0"), nil),
		makeNode("t", "u1", 1, ptr("u0"), ptr("u0"), nil),
	}

	require.NotPanics(t, func() {
		_, err := orderForReplay("t", nodes)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no unreplayable node")
	})
}

func TestTreeLoader_RootSponsorIsNotADependency(t *testing.T) {
	// AddRoot takes no sponsor and the engine root is sponsor: None, so
	// whatever a root row stores is projection metadata. Treating it as a
	// replay dependency makes the root wait on a node downstream of itself and
	// rejects the whole tree as a cycle. The "another node" case is the one
	// that regressed.
	tests := []struct {
		name        string
		rootSponsor *string
	}{
		{name: "nil sponsor", rootSponsor: nil},
		{name: "self sponsor", rootSponsor: ptr("u0")},
		{name: "another node in the tree", rootSponsor: ptr("u1")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nodes := []TreeNodeRow{
				makeNode("t", "u0", 0, nil, tt.rootSponsor, nil),
				makeNode("t", "u1", 1, ptr("u0"), ptr("u0"), nil),
			}

			ordered, err := orderForReplay("t", nodes)
			require.NoError(t, err)
			assert.Equal(t, []string{"u0", "u1"}, userIDs(ordered))
		})
	}
}

// TestTreeLoader_RootSponsorLoadsThroughLoadTree covers the same invariant one
// layer up. The regression this guards lived at the boundary between
// validateNodes and orderForReplay, not inside either, so exercising the
// helper alone would not have caught it.
func TestTreeLoader_RootSponsorLoadsThroughLoadTree(t *testing.T) {
	nodes := []TreeNodeRow{
		// Root sponsored by a node that is itself downstream of the root.
		makeNode("t", "u0", 0, nil, ptr("u1"), nil),
		makeNode("t", "u1", 1, ptr("u0"), ptr("u0"), nil),
	}

	mutator, err := loadWithStub(t, "unilevel", nodes)
	require.NoError(t, err, "a root sponsored by its own descendant must still load")
	assert.Equal(t, []string{"u0"}, mutator.roots)
	assert.Equal(t, []string{"u1"}, mutator.nodes)
}

// TestTreeLoader_BinaryTreeLoadsUnchanged is BR-5's evidence for the binary
// half. Binary now passes through position preflight and dependency-aware
// ordering, and every other unchanged-behavior test loads a unilevel tree, so
// without this the compatibility claim rests on nothing.
func TestTreeLoader_BinaryTreeLoadsUnchanged(t *testing.T) {
	enrolled := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	root := makeNode("t", "u0", 0, nil, ptr("u0"), nil)
	root.EnrolledAt = enrolled

	left := makeNode("t", "u1", 1, ptr("u0"), ptr("u0"), intPtr(0))
	left.EnrolledAt = enrolled.Add(time.Hour)

	// Sponsored by u3, which sits deeper, so depth order alone would replay
	// this before its sponsor exists. Parent and sponsor also differ here.
	right := makeNode("t", "u2", 1, ptr("u0"), ptr("u3"), intPtr(1))
	right.EnrolledAt = enrolled.Add(2 * time.Hour)

	deep := makeNode("t", "u3", 2, ptr("u1"), ptr("u1"), intPtr(0))
	deep.EnrolledAt = enrolled.Add(3 * time.Hour)

	mutator, err := loadWithStub(t, "binary", []TreeNodeRow{root, left, right, deep})
	require.NoError(t, err)

	assert.Equal(t, []string{"t"}, mutator.created, "binary must use plain CreateTree")
	assert.Empty(t, mutator.matrixCreated)
	assert.Equal(t, []string{"u0"}, mutator.roots)
	assert.Empty(t, mutator.nodesAt, "binary must not route through AddNodeAt")

	// u3 precedes u2 because it sponsors it, even though u2 is shallower.
	assert.Equal(t, []nodeAddCall{
		{userID: "u1", parentID: "u0", sponsorID: "u0", position: intPtr(0)},
		{userID: "u3", parentID: "u1", sponsorID: "u1", position: intPtr(0)},
		{userID: "u2", parentID: "u0", sponsorID: "u3", position: intPtr(1)},
	}, mutator.nodeCalls)
}

func TestTreeLoader_LoadMatrixTree_RequiresParams(t *testing.T) {
	store := NewMemoryTreeStore()
	ctx := context.Background()

	root := makeNode("m-tree", "user-0", 0, nil, ptr("user-0"), nil)
	root.EnrolledAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, store.InsertNode(ctx, root))

	mutator := &stubMutator{}
	loader := NewTreeLoader(store, mutator)

	// Matrix tree with no WithMatrixParams: the loader has no width/spillover
	// to create with, so it must fail fast before touching the engine.
	err := loader.LoadTree(ctx, "m-tree", "matrix")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "matrix")
	assert.Empty(t, mutator.matrixCreated)
	assert.Empty(t, mutator.created)
	assert.Empty(t, mutator.roots)
}
