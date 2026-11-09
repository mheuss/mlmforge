package networkengine

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPreflight_EmptyTreeWithValidConfigPasses pins that a zero-row tree whose
// configuration is sound passes preflight.
func TestPreflight_EmptyTreeWithValidConfigPasses(t *testing.T) {
	tests := []struct {
		name     string
		treeType string
		cfg      loadTreeConfig
	}{
		{name: "unilevel", treeType: "unilevel"},
		{name: "matrix", treeType: "matrix", cfg: matrixConfig(3, "breadth_first")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.NoError(t, preflightRows(t, tt.treeType, tt.cfg, nil))
		})
	}
}

func TestPreflight_NoDepthZeroRoot(t *testing.T) {
	store := NewMemoryTreeStore()
	ctx := context.Background()

	// Insert a single node with depth 1 — invalid for a root.
	node := makeNode("t", "user-0", 1, nil, ptr("user-0"), nil)
	require.NoError(t, store.insertNode(ctx, node))

	err := preflightStore(t, store, "unilevel", loadTreeConfig{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "has no depth-0 root node")
}

func TestPreflight_ChildWithNilParent(t *testing.T) {
	store := NewMemoryTreeStore()
	ctx := context.Background()
	enrolled := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// Valid root.
	root := makeNode("t", "user-0", 0, nil, ptr("user-0"), nil)
	root.EnrolledAt = enrolled
	require.NoError(t, store.insertNode(ctx, root))

	// Child with nil ParentID and nil SponsorID — data corruption.
	child := makeNode("t", "user-1", 1, nil, nil, nil)
	child.EnrolledAt = enrolled.Add(time.Hour)
	require.NoError(t, store.insertNode(ctx, child))

	err := preflightStore(t, store, "unilevel", loadTreeConfig{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "has nil parent or sponsor")
}

// TestPreflight_AcceptsAMatrixSponsorDeeperThanItsNode pins that preflight
// accepts a matrix node placed shallower than its own sponsor.
func TestPreflight_AcceptsAMatrixSponsorDeeperThanItsNode(t *testing.T) {
	enrolled := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	root := makeNode("t", "u0", 0, nil, ptr("u0"), nil)
	root.EnrolledAt = enrolled

	u1 := makeNode("t", "u1", 1, ptr("u0"), ptr("u0"), intPtr(0))
	u1.EnrolledAt = enrolled.Add(time.Hour)

	// Sponsored by u2, which sits a level deeper. Depth order would replay this
	// first and the engine would reject an unknown sponsor.
	u3 := makeNode("t", "u3", 1, ptr("u0"), ptr("u2"), intPtr(2))
	u3.EnrolledAt = enrolled.Add(2 * time.Hour)

	u2 := makeNode("t", "u2", 2, ptr("u1"), ptr("u1"), intPtr(1))
	u2.EnrolledAt = enrolled.Add(3 * time.Hour)

	require.NoError(t, preflightRows(t, "matrix", matrixConfig(3, "breadth_first"), []TreeNodeRow{root, u1, u3, u2}))
}

// preflightRows seeds an in-memory store with nodes and runs the checks a load
// runs: the configuration, then preflight over the rows the store returns.
func preflightRows(t *testing.T, treeType string, cfg loadTreeConfig, nodes []TreeNodeRow) error {
	t.Helper()
	store := NewMemoryTreeStore()
	for _, n := range nodes {
		require.NoError(t, store.insertNode(context.Background(), n))
	}
	return preflightStore(t, store, treeType, cfg)
}

// preflightRowsDirect is preflightRows for fixtures the store's insert refuses.
func preflightRowsDirect(t *testing.T, treeType string, cfg loadTreeConfig, nodes []TreeNodeRow) error {
	t.Helper()
	return preflightStore(t, &MemoryTreeStore{nodes: nodes}, treeType, cfg)
}

// preflightStore runs the configuration check and preflight against tree "t".
func preflightStore(t *testing.T, store TreeStore, treeType string, cfg loadTreeConfig) error {
	t.Helper()
	if err := validateTreeConfig("t", treeType, cfg); err != nil {
		return err
	}
	rows, err := store.GetByTreeDepthOrdered(context.Background(), "t")
	require.NoError(t, err)
	return preflight("t", treeType, cfg, rows)
}

// matrixConfig is the configuration a matrix tree of this shape loads with.
func matrixConfig(width int, spillover string) loadTreeConfig {
	return loadTreeConfig{matrixParamsSet: true, matrixWidth: width, matrixSpillover: spillover}
}

func TestPreflight_ValidationFailures_Structural(t *testing.T) {
	matrixCfg := matrixConfig(3, "breadth_first")

	tests := []struct {
		name     string
		treeType string
		cfg      loadTreeConfig
		nodes    []TreeNodeRow
		wantErr  string
		// direct seeds the store's rows in one go instead of inserting them
		// one by one.
		direct bool
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
			cfg:      matrixConfig(1, "breadth_first"),
			nodes:    []TreeNodeRow{makeNode("t", "u0", 0, nil, ptr("u0"), nil)},
			wantErr:  "matrix width 1 outside the supported range 2..255",
		},
		{
			name:     "matrix width above 255",
			treeType: "matrix",
			cfg:      matrixConfig(256, "breadth_first"),
			nodes:    []TreeNodeRow{makeNode("t", "u0", 0, nil, ptr("u0"), nil)},
			wantErr:  "matrix width 256 outside the supported range 2..255",
		},
		{
			name:     "unsupported spillover",
			treeType: "matrix",
			cfg:      matrixConfig(3, "depth_first"),
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
			// see HEU-810
			direct:  true,
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
			cfg:      matrixCfg,
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
		{
			name:     "empty user ID",
			treeType: "unilevel",
			nodes: []TreeNodeRow{
				makeNode("t", "u0", 0, nil, ptr("u0"), nil),
				makeNode("t", "", 1, ptr("u0"), ptr("u0"), nil),
			},
			wantErr: "tree t has a row with an empty user ID",
		},
		{
			name:     "unilevel node carries a position",
			treeType: "unilevel",
			nodes: []TreeNodeRow{
				makeNode("t", "u0", 0, nil, ptr("u0"), nil),
				makeNode("t", "u1", 1, ptr("u0"), ptr("u0"), intPtr(7)),
			},
			wantErr: "unilevel node u1 in tree t has position 7",
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
			assert.Contains(t, err.Error(), tt.wantErr)
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

// TestValidateNodes_DuplicateUserID calls validateNodes directly because the
// fixture cannot be inserted through the memory store.
func TestValidateNodes_DuplicateUserID(t *testing.T) {
	nodes := []TreeNodeRow{
		makeNode("t", "u0", 0, nil, ptr("u0"), nil),
		makeNode("t", "u0", 1, ptr("u0"), ptr("u0"), nil),
	}

	err := validateNodes("t", "unilevel", loadTreeConfig{}, nodes)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate user u0")
}

func TestPreflight_ValidationFailures_Positions(t *testing.T) {
	matrixCfg := matrixConfig(3, "breadth_first")

	tests := []struct {
		name     string
		treeType string
		cfg      loadTreeConfig
		nodes    []TreeNodeRow
		wantErr  string
		// direct seeds the store without its insert. Required for fixtures
		// the store itself now refuses (duplicate slots, per the migration
		// 000004 mirror), which preflight must still catch when they predate
		// the index in a real database.
		direct bool
	}{
		{
			name:     "matrix nil position",
			treeType: "matrix",
			cfg:      matrixCfg,
			nodes: []TreeNodeRow{
				makeNode("t", "u0", 0, nil, ptr("u0"), nil),
				makeNode("t", "u1", 1, ptr("u0"), ptr("u0"), nil),
			},
			wantErr: "matrix node u1 in tree t has nil position",
		},
		{
			name:     "matrix negative position",
			treeType: "matrix",
			cfg:      matrixCfg,
			nodes: []TreeNodeRow{
				makeNode("t", "u0", 0, nil, ptr("u0"), nil),
				makeNode("t", "u1", 1, ptr("u0"), ptr("u0"), intPtr(-1)),
			},
			wantErr: "position -1 outside the range 0..2",
		},
		{
			name:     "matrix position equals width",
			treeType: "matrix",
			cfg:      matrixCfg,
			nodes: []TreeNodeRow{
				makeNode("t", "u0", 0, nil, ptr("u0"), nil),
				makeNode("t", "u1", 1, ptr("u0"), ptr("u0"), intPtr(3)),
			},
			wantErr: "position 3 outside the range 0..2",
		},
		{
			name:     "matrix duplicate slot",
			treeType: "matrix",
			cfg:      matrixCfg,
			nodes: []TreeNodeRow{
				makeNode("t", "u0", 0, nil, ptr("u0"), nil),
				makeNode("t", "u1", 1, ptr("u0"), ptr("u0"), intPtr(1)),
				makeNode("t", "u2", 1, ptr("u0"), ptr("u0"), intPtr(1)),
			},
			wantErr: "both claim parent u0 position 1",
			direct:  true,
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
			direct:  true,
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
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestPreflight_ValidPositionsAccepted(t *testing.T) {
	// Same position under different parents is fine, and width-1 is in range.
	nodes := []TreeNodeRow{
		makeNode("t", "u0", 0, nil, ptr("u0"), nil),
		makeNode("t", "u1", 1, ptr("u0"), ptr("u0"), intPtr(2)),
		makeNode("t", "u2", 2, ptr("u1"), ptr("u1"), intPtr(2)),
	}

	require.NoError(t, preflightRows(t, "matrix", matrixConfig(3, "breadth_first"), nodes))
}

// TestPreflight_MatrixParamsIgnoredForNonMatrix pins that matrix params are
// inert for other tree types. A width and spillover that would be rejected
// outright for a matrix must not affect a binary tree.
func TestPreflight_MatrixParamsIgnoredForNonMatrix(t *testing.T) {
	nodes := []TreeNodeRow{
		makeNode("t", "u0", 0, nil, ptr("u0"), nil),
		makeNode("t", "u1", 1, ptr("u0"), ptr("u0"), intPtr(1)),
	}

	// Width 0 must not leak into the binary slot limit: position 1 is only
	// valid because binary hardcodes 2.
	require.NoError(t, preflightRows(t, "binary", matrixConfig(0, "depth_first"), nodes))
}

func TestValidateNodes_UnilevelPositionIsRejected(t *testing.T) {
	nodes := []TreeNodeRow{
		makeNode("t", "u0", 0, nil, ptr("u0"), nil),
		makeNode("t", "u1", 1, ptr("u0"), ptr("u0"), intPtr(7)),
	}

	err := validateNodes("t", "unilevel", loadTreeConfig{}, nodes)

	var rejected *TreeLoadRejectedError
	require.ErrorAs(t, err, &rejected)
	assert.Equal(t, TreeLoadDataInvalid, rejected.Kind)
	assert.Equal(t, []string{"u1"}, rejected.NodeIDs)
}

// Two rows with an empty user ID are reported as an empty ID, not as a duplicate.
func TestValidateNodes_TwoEmptyUserIDsReportTheEmptyID(t *testing.T) {
	first := makeNode("t", "", 1, ptr("u0"), ptr("u0"), nil)
	first.ID = "r1"
	second := makeNode("t", "", 1, ptr("u0"), ptr("u0"), nil)
	second.ID = "r2"
	nodes := []TreeNodeRow{makeNode("t", "u0", 0, nil, ptr("u0"), nil), first, second}

	err := validateNodes("t", "unilevel", loadTreeConfig{}, nodes)

	var rejected *TreeLoadRejectedError
	require.ErrorAs(t, err, &rejected)
	assert.Equal(t, `tree t has a row with an empty user ID (row ID "r1")`, err.Error())
	assert.Empty(t, rejected.NodeIDs)
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

func TestPreflight_CyclicReferences(t *testing.T) {
	// u1 and u2 sponsor each other. Parents are valid, so only the sponsor
	// edges form the cycle and nothing can be replayed first.
	nodes := []TreeNodeRow{
		makeNode("t", "u0", 0, nil, ptr("u0"), nil),
		makeNode("t", "u1", 1, ptr("u0"), ptr("u2"), nil),
		makeNode("t", "u2", 1, ptr("u0"), ptr("u1"), nil),
	}

	err := preflightRows(t, "unilevel", loadTreeConfig{}, nodes)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "form a cycle")
	// BR-4: the error must name the offending nodes, not just count them.
	// Asserting the rendered cycle rather than a substring of a node list,
	// because "u1, u2" would also match a message that wrongly included u0.
	assert.Contains(t, err.Error(), "u1 -> u2 -> u1")
	assert.Contains(t, err.Error(), "2 of 3 nodes", "the root is replayable and must not be counted")
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

// TestOrderForReplay_DuplicateUserIDsDoNotPanic pins that duplicate rows make
// orderForReplay return an error instead of panicking.
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

func TestOrderForReplay_RootSponsorIsNotADependency(t *testing.T) {
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

// TestPreflight_AcceptsARootSponsoredByItsDescendant covers the root-sponsor
// exemption through both validateNodes and orderForReplay together.
func TestPreflight_AcceptsARootSponsoredByItsDescendant(t *testing.T) {
	nodes := []TreeNodeRow{
		// Root sponsored by a node that is itself downstream of the root.
		makeNode("t", "u0", 0, nil, ptr("u1"), nil),
		makeNode("t", "u1", 1, ptr("u0"), ptr("u0"), nil),
	}

	require.NoError(t, preflightRows(t, "unilevel", loadTreeConfig{}, nodes),
		"a root sponsored by its own descendant must still pass")
}

// TestPreflight_AcceptsABinarySponsorDeeperThanItsNode pins that preflight
// accepts a binary node placed shallower than its own sponsor.
func TestPreflight_AcceptsABinarySponsorDeeperThanItsNode(t *testing.T) {
	enrolled := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	root := makeNode("t", "u0", 0, nil, ptr("u0"), nil)
	root.EnrolledAt = enrolled

	left := makeNode("t", "u1", 1, ptr("u0"), ptr("u0"), intPtr(0))
	left.EnrolledAt = enrolled.Add(time.Hour)

	// Sponsored by u3, which sits deeper. Parent and sponsor also differ here.
	right := makeNode("t", "u2", 1, ptr("u0"), ptr("u3"), intPtr(1))
	right.EnrolledAt = enrolled.Add(2 * time.Hour)

	deep := makeNode("t", "u3", 2, ptr("u1"), ptr("u1"), intPtr(0))
	deep.EnrolledAt = enrolled.Add(3 * time.Hour)

	require.NoError(t, preflightRows(t, "binary", loadTreeConfig{}, []TreeNodeRow{root, left, right, deep}))
}

func TestPreflight_MatrixRequiresParams(t *testing.T) {
	root := makeNode("t", "user-0", 0, nil, ptr("user-0"), nil)
	root.EnrolledAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// Assert the guard's own message, not just "matrix". A zero-valued cfg also
	// trips the width range check, whose message contains "matrix" too, so the
	// looser assertion passes even with this guard deleted.
	err := preflightRows(t, "matrix", loadTreeConfig{}, []TreeNodeRow{root})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is a matrix tree, and its configuration does not mark a width and spillover as set")
}
