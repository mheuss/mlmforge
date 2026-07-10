package networkengine

import (
	"context"
	"fmt"
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

	if treeType == "matrix" {
		if !cfg.matrixParamsSet {
			return fmt.Errorf("create tree %s: matrix tree requires width and spillover (use WithMatrixParams)", treeID)
		}
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
