package networkengine

import (
	"context"
	"fmt"
	"math"
)

// TreeLoader rebuilds the Rust engine's in-memory state from the
// adjacency table on startup.
type TreeLoader struct {
	store  TreeStore
	engine TreeMutator
}

func NewTreeLoader(store TreeStore, engine TreeMutator) *TreeLoader {
	return &TreeLoader{store: store, engine: engine}
}

// LoadTreeOption configures optional parameters for LoadTree.
type LoadTreeOption func(*loadTreeConfig)

type loadTreeConfig struct {
	matrixParamsSet bool
	matrixWidth     int
	matrixSpillover string
}

// WithMatrixParams supplies the width and spillover a matrix tree needs to be
// recreated. It is required when treeType is "matrix" (the adjacency store
// keeps per-node data, not the structure's width/spillover) and ignored
// otherwise.
func WithMatrixParams(width int, spillover string) LoadTreeOption {
	return func(c *loadTreeConfig) {
		c.matrixParamsSet = true
		c.matrixWidth = width
		c.matrixSpillover = spillover
	}
}

// LoadTree reads all active nodes for a tree from the store (depth-ordered)
// and replays them into the engine via CreateTree/CreateMatrixTree +
// AddRoot/AddNode. Matrix trees need width and spillover supplied through
// WithMatrixParams, which plain CreateTree does not carry.
func (l *TreeLoader) LoadTree(ctx context.Context, treeID, treeType string, opts ...LoadTreeOption) error {
	nodes, err := l.store.GetByTreeDepthOrdered(ctx, treeID)
	if err != nil {
		return fmt.Errorf("load tree %s: %w", treeID, err)
	}
	if len(nodes) == 0 {
		return nil
	}

	var cfg loadTreeConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	if treeType == "matrix" && !cfg.matrixParamsSet {
		return fmt.Errorf("create tree %s: matrix tree requires width and spillover (use WithMatrixParams)", treeID)
	}

	// Preflight. Nothing below this point runs until validation succeeds.
	if err := validateNodes(treeID, treeType, cfg, nodes); err != nil {
		return err
	}

	if treeType == "matrix" {
		// The worker's matrix add_node re-derives BFS placement from sponsor_id
		// and ignores the stored parent/position, so replaying past the root
		// would silently reconstruct a different topology than the adjacency
		// store. Refuse it rather than corrupt state. Explicit-placement replay
		// (add_node_at) is tracked in HEU-534.
		if len(nodes) > 1 {
			return fmt.Errorf("load tree %s: multi-node matrix reload is not yet supported", treeID)
		}
		if err := l.engine.CreateMatrixTree(ctx, treeID, cfg.matrixWidth, cfg.matrixSpillover); err != nil {
			return fmt.Errorf("create tree %s: %w", treeID, err)
		}
	} else if err := l.engine.CreateTree(ctx, treeID, treeType); err != nil {
		return fmt.Errorf("create tree %s: %w", treeID, err)
	}

	for i, node := range nodes {
		if i == 0 {
			if node.Depth != 0 {
				return fmt.Errorf("first node in tree %s has depth %d, expected 0 (data corruption?)", treeID, node.Depth)
			}
			if err := l.engine.AddRoot(ctx, treeID, node.UserID, node.EnrolledAt.Unix()); err != nil {
				return fmt.Errorf("add root %s: %w", node.UserID, err)
			}
			continue
		}

		if node.ParentID == nil || node.SponsorID == nil {
			return fmt.Errorf("node %s in tree %s has nil parent or sponsor (data corruption?)", node.UserID, treeID)
		}
		parentID := *node.ParentID
		sponsorID := *node.SponsorID

		var opts []AddNodeOption
		if node.Position != nil {
			opts = append(opts, WithPosition(*node.Position))
		}

		if err := l.engine.AddNode(ctx, treeID, node.UserID, parentID, sponsorID, node.EnrolledAt.Unix(), opts...); err != nil {
			return fmt.Errorf("add node %s: %w", node.UserID, err)
		}
	}
	return nil
}

// supportedTreeTypes are the structures LoadTree knows how to replay. Anything
// else would reach the worker as an unknown type after the engine had already
// been mutated.
//
// This mirrors the worker's create_tree dispatch
// (engine/network-engine-worker/src/handlers/tree.rs) and is not generated
// from it. A new tree type has to be added in both places.
var supportedTreeTypes = map[string]bool{
	"unilevel": true,
	"binary":   true,
	"matrix":   true,
}

// supportedSpillover are the spillover values create_tree accepts. The worker
// maps only "breadth_first", and MatrixTree::new rejects DepthFirst outright
// (matrix.rs:57-59), so this set has one member today.
var supportedSpillover = map[string]bool{
	"breadth_first": true,
}

// validateNodes proves the node set is structurally consistent before any
// engine mutation. A tree that fails here leaves the engine untouched, so the
// load can be retried once the data is corrected. This matters because the
// worker has no operation to drop a structure (HEU-557): a partially built
// tree would be stuck until the process restarts.
//
// Structural consistency is only half of "reconstructable". This function
// proves every parent and sponsor exists in the set; it does not prove they
// can be replayed in an order that satisfies the engine. A node may legally
// sit shallower than its own sponsor, and the engine resolves a sponsor at
// insert time, so depth order alone can still fail mid-replay. orderForReplay
// closes that half.
//
// cfg supplies matrix width and spillover. Binary positions are fixed at 0
// and 1; unilevel nodes carry no position.
func validateNodes(treeID, treeType string, cfg loadTreeConfig, nodes []TreeNodeRow) error {
	if !supportedTreeTypes[treeType] {
		return fmt.Errorf("tree %s has unsupported type %q", treeID, treeType)
	}
	if treeType == "matrix" {
		// MatrixTree::new rejects width < 2 (matrix.rs:54), and width is a u8.
		if cfg.matrixWidth < 2 || cfg.matrixWidth > math.MaxUint8 {
			return fmt.Errorf("tree %s has matrix width %d outside the supported range 2..%d",
				treeID, cfg.matrixWidth, math.MaxUint8)
		}
		if !supportedSpillover[cfg.matrixSpillover] {
			return fmt.Errorf("tree %s has unsupported spillover %q", treeID, cfg.matrixSpillover)
		}
	}

	// Pointers, not values: a distributor network can hold hundreds of
	// thousands of rows, and TreeNodeRow is wide. nodes is never mutated here,
	// so the pointers stay valid for the life of the call.
	byID := make(map[string]*TreeNodeRow, len(nodes))
	var root *TreeNodeRow

	for i := range nodes {
		n := &nodes[i]
		if _, dup := byID[n.UserID]; dup {
			return fmt.Errorf("tree %s has duplicate user %s (data corruption?)", treeID, n.UserID)
		}
		byID[n.UserID] = n
		if n.Depth == 0 {
			if root != nil {
				return fmt.Errorf("tree %s has more than one depth-0 root (%s and %s)",
					treeID, root.UserID, n.UserID)
			}
			root = n
		}
	}
	if root == nil {
		return fmt.Errorf("tree %s has no depth-0 root node (data corruption?)", treeID)
	}

	// Root invariants. AddRoot takes neither a parent nor a position, so a row
	// carrying either describes a topology the engine cannot reproduce. Root
	// sponsorship is deliberately exempt — see design section 5.
	if root.ParentID != nil {
		return fmt.Errorf("root %s in tree %s has parent %s (the engine root has no parent)",
			root.UserID, treeID, *root.ParentID)
	}
	if root.Position != nil {
		return fmt.Errorf("root %s in tree %s has position %d (the engine root occupies no slot)",
			root.UserID, treeID, *root.Position)
	}

	for _, n := range nodes {
		if n.UserID == root.UserID {
			continue
		}
		if n.ParentID == nil || n.SponsorID == nil {
			return fmt.Errorf("node %s in tree %s has nil parent or sponsor (data corruption?)",
				n.UserID, treeID)
		}
		// Only the root may reference itself. A non-root self-sponsor resolves
		// against byID and would pass an existence check, then fail in the
		// engine: the same call that creates the node resolves its sponsor, so
		// the sponsor does not exist yet.
		if *n.ParentID == n.UserID {
			return fmt.Errorf("node %s in tree %s is its own parent", n.UserID, treeID)
		}
		if *n.SponsorID == n.UserID {
			return fmt.Errorf("node %s in tree %s is its own sponsor", n.UserID, treeID)
		}
		parent, ok := byID[*n.ParentID]
		if !ok {
			return fmt.Errorf("node %s in tree %s references parent %s that is not in the tree",
				n.UserID, treeID, *n.ParentID)
		}
		if _, ok := byID[*n.SponsorID]; !ok {
			return fmt.Errorf("node %s in tree %s references sponsor %s that is not in the tree",
				n.UserID, treeID, *n.SponsorID)
		}
		if n.Depth != parent.Depth+1 {
			return fmt.Errorf("node %s in tree %s has depth %d but parent %s has depth %d",
				n.UserID, treeID, n.Depth, parent.UserID, parent.Depth)
		}
	}
	return nil
}
